package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/gitstore"
)

const (
	MaxReadManyItems = 10
	MaxReadManyBytes = 256 << 10
	MaxReadManyLines = 1000
	ReadManyTimeout  = 30 * time.Second
)

type ReadManyError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ReadManyItem struct {
	Request contract.ReadItemRequest
	File    *gitstore.File `json:",omitempty"`
	Error   *ReadManyError `json:",omitempty"`
}

type ReadManyResult struct {
	Repository    string
	Commit        string
	Items         []ReadManyItem
	ReturnedBytes int
	ReturnedLines int
}

func readManyError(err error) *ReadManyError {
	var problem *contract.Error
	if errors.As(err, &problem) {
		return &ReadManyError{Code: problem.Code, Message: problem.Message}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &ReadManyError{Code: "READ_TIMEOUT", Message: "File read exceeded its time budget."}
	}
	return &ReadManyError{Code: "READ_FAILED", Message: "File read could not complete."}
}

func readManyBudgetError() *ReadManyError {
	return &ReadManyError{Code: "READ_BUDGET_EXCEEDED", Message: "Batch content budget reached; continue from NextStartLine when present, otherwise retry the requested range in a new batch."}
}

// ReadMany authorizes and selects the repository version once. Items execute in
// request order against that immutable commit, with no fetch or preparation.
// Item failures do not spend content budget. A cancellation returns the items
// completed so far and stops launching work for later items.
func (s *Service) ReadMany(ctx context.Context, name, revision string, requests []contract.ReadItemRequest) (out ReadManyResult, err error) {
	started, failed := time.Now(), false
	defer func() {
		s.metrics.Observe(name, "read_many", started, failed || err != nil)
		problem := err
		issues := []map[string]any{}
		for i, item := range out.Items {
			if item.Error != nil {
				item.Error.Message = fmt.Sprintf("Item %d: %s", i+1, item.Error.Message)
				issues = append(issues, map[string]any{"item": i, "item_number": i + 1, "code": item.Error.Code, "message": item.Error.Message})
				if problem == nil {
					problem = contract.Fail(item.Error.Code, item.Error.Message, 422)
				}
			}
		}
		s.logQuery(ctx, name, "read_many", started, map[string]any{"revision": revision, "resolved_commit": out.Commit, "items": requests, "failures": issues}, problem)
	}()
	out = ReadManyResult{Repository: name, Items: []ReadManyItem{}}
	defer func() {
		if errors.Is(err, context.DeadlineExceeded) && len(out.Items) > 0 {
			for len(out.Items) < len(requests) {
				out.Items = append(out.Items, ReadManyItem{Request: requests[len(out.Items)], Error: readManyError(err)})
			}
			failed, err = true, nil
		}
	}()
	if len(requests) < 1 || len(requests) > MaxReadManyItems {
		return out, contract.Fail("INVALID_READ_ITEMS", "Request 1..10 file ranges in a batch.", 400)
	}
	deadline := time.Now().Add(ReadManyTimeout)
	if outer, ok := ctx.Deadline(); ok {
		// Leave time for the transport to serialize completed items before its
		// own deadline. Short caller deadlines retain 90% for actual reads.
		reserve := max(time.Duration(0), min(time.Second, time.Until(outer)/10))
		if workDeadline := outer.Add(-reserve); workDeadline.Before(deadline) {
			deadline = workDeadline
		}
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	// A pending storage change must not make acquiring the query lock exceed the
	// batch deadline. No detached goroutine can retain the lock after cancellation.
	for !s.mu.TryRLock() {
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return out, ctx.Err()
		case <-timer.C:
		}
	}
	defer s.mu.RUnlock()
	if err = ctx.Err(); err != nil {
		return out, err
	}
	r, err := s.Repo(ctx, name)
	if err != nil {
		return out, err
	}
	v, err := eligible(r, revision)
	if err != nil {
		return out, err
	}
	if r.EffectiveIndexMode() == control.IndexModeMonorepo {
		r, err = s.contentRepository(r, v.Commit)
		if err != nil {
			return out, err
		}
	}
	out.Commit = v.Commit
	type cachedReader struct {
		reader *gitstore.Reader
		err    error
	}
	readers := map[string]cachedReader{}
	for _, q := range requests {
		if err = ctx.Err(); err != nil {
			return out, err
		}
		item := ReadManyItem{Request: q}
		itemBytes, itemLines := 0, 0
		remainingBytes, remainingLines := MaxReadManyBytes-out.ReturnedBytes, MaxReadManyLines-out.ReturnedLines
		if remainingBytes == 0 || remainingLines == 0 {
			item.Error = readManyBudgetError()
		} else if e := gitstore.ValidateReadRange(q.Path, q.StartLine, q.EndLine); e != nil {
			item.Error = readManyError(e)
		} else {
			scope, e := readScope(r, v.Commit, q.Path)
			if e != nil {
				item.Error = readManyError(e)
			} else {
				cached, exists := readers[scope.name]
				if !exists {
					g, openErr := s.scopeStore(name, scope)
					if openErr == nil {
						cached.reader, openErr = g.OpenReader(ctx, name, v.Commit)
					}
					cached.err = openErr
					readers[scope.name] = cached
				}
				if cached.err != nil {
					item.Error = readManyError(cached.err)
				} else {
					end := q.EndLine
					if q.EndLine-q.StartLine+1 > remainingLines {
						end = q.StartLine + remainingLines - 1
					}
					file, readErr := cached.reader.Read(ctx, q.Path, q.StartLine, end, remainingBytes)
					if readErr != nil {
						item.Error = readManyError(readErr)
					}
					if readErr == nil || (item.Error != nil && item.Error.Code == "READ_BUDGET_EXCEEDED") {
						item.File = &file
						itemBytes = len(file.Content)
						if file.ReturnedStartLine != nil && file.ReturnedEndLine != nil {
							itemLines = *file.ReturnedEndLine - *file.ReturnedStartLine + 1
						}
						if end < q.EndLine && file.HasMore != nil && *file.HasMore && file.ReturnedEndLine != nil && *file.ReturnedEndLine == end {
							file.Truncated = true
							item.Error = readManyBudgetError()
						}
					}
				}
			}
		}
		if err = ctx.Err(); err != nil {
			return out, err
		}
		failed = failed || item.Error != nil
		out.Items = append(out.Items, item)
		out.ReturnedBytes += itemBytes
		out.ReturnedLines += itemLines
	}
	return out, nil
}
