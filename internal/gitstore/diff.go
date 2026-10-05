package gitstore

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/context4ai/sourcegraph/internal/contract"
)

type Change struct {
	Path           string
	ChangeKind     string
	OldOID         string
	NewOID         string
	Binary         bool
	Patch          string
	PatchTruncated bool
}
type Diff struct {
	PathStatus     []DiffPathStatus `json:"-"`
	BaseCommit     string
	HeadCommit     string
	Changes        []Change
	NextCursor     string
	HasMoreResults bool
}
type diffCursor struct {
	Base  string
	Head  string
	Last  string
	Scope string
}

func (s *Store) Compare(ctx context.Context, repo, base, head string, first int, after string) (Diff, error) {
	result := Diff{BaseCommit: base, HeadCommit: head, Changes: []Change{}}
	if first < 1 || first > 100 {
		return result, contract.Fail("INVALID_LIMIT", "Diff limit must be in 1..100.", 400)
	}
	changes, err := s.Changes(ctx, repo, base, head)
	if err != nil {
		return result, err
	}
	last := ""
	if after != "" {
		b, e := base64.RawURLEncoding.DecodeString(after)
		if e != nil || len(b) > 8192 {
			return result, contract.Fail("INVALID_CURSOR", "Invalid diff cursor.", 400)
		}
		var cursor diffCursor
		if e = contract.DecodeObject(b, &cursor); e != nil || cursor.Base != base || cursor.Head != head || cursor.Scope != s.scopeKey() {
			return result, contract.Fail("INVALID_CURSOR", "Cursor does not match these commits.", 400)
		}
		last = cursor.Last
		found := false
		for _, c := range changes {
			if c.Path == last {
				found = true
			}
		}
		if !found {
			return result, contract.Fail("INVALID_CURSOR", "Diff cursor position does not exist.", 400)
		}
	}
	for _, c := range changes {
		if after != "" && c.Path <= last {
			continue
		}
		if len(result.Changes) == first {
			result.HasMoreResults = true
			break
		}
		result.Changes = append(result.Changes, c)
	}
	result.PathStatus, err = s.DiffPathStatus(ctx, repo, base, head)
	if err != nil {
		return result, err
	}
	result.Changes, _, err = s.PatchChanges(ctx, repo, base, head, result.Changes, MaxPatchBytes)
	if err != nil {
		return result, err
	}
	if result.HasMoreResults {
		b, _ := json.Marshal(diffCursor{Base: base, Head: head, Last: result.Changes[len(result.Changes)-1].Path, Scope: s.scopeKey()})
		result.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
	return result, nil
}

// Changes lists immutable path/object metadata without reading any blob content.
// It can therefore page a partially hydrated repository safely.
func (s *Store) Changes(ctx context.Context, repo, base, head string) ([]Change, error) {
	p, err := s.fixed(ctx, repo, base)
	if err != nil {
		return nil, err
	}
	if _, err = s.fixed(ctx, repo, head); err != nil {
		return nil, err
	}
	args := []string{"diff-tree", "-r", "--no-commit-id", "--raw", "-z", "--no-renames", base, head, "--"}
	args = append(args, s.paths...)
	data, err := s.run(ctx, p, 8<<20, args...)
	if err != nil {
		return nil, err
	}
	rows := bytes.Split(data, []byte{0})
	changes := []Change{}
	for i := 0; i < len(rows)-1; i += 2 {
		if i+1 >= len(rows) || len(rows[i]) == 0 {
			break
		}
		fields := strings.Fields(string(rows[i]))
		path := string(rows[i+1])
		if len(fields) != 5 || !fullSHA.MatchString(fields[2]) || !fullSHA.MatchString(fields[3]) {
			return nil, contract.Fail("INVALID_GIT_RESULT", "Malformed diff response.", 500)
		}
		if err = ValidatePath(path, false); err != nil {
			return nil, contract.Fail("UNSUPPORTED_PATH_ENCODING", "Diff path cannot be represented.", 422)
		}
		changes = append(changes, Change{Path: path, ChangeKind: fields[4], OldOID: fields[2], NewOID: fields[3]})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, nil
}

const MaxPatchBytes = 256 << 10

// PatchChanges enriches only an already selected page. remaining is shared by
// callers aggregating multiple groups, and may never exceed MaxPatchBytes.
func (s *Store) PatchChanges(ctx context.Context, repo, base, head string, selected []Change, remaining int) ([]Change, int, error) {
	if !fullSHA.MatchString(base) || !fullSHA.MatchString(head) {
		return nil, remaining, contract.Fail("INVALID_REVISION", "Object reads require full SHAs.", 400)
	}
	if len(selected) > 100 || remaining < 0 || remaining > MaxPatchBytes {
		return nil, remaining, contract.Fail("INVALID_LIMIT", "Patch page or budget exceeds the allowed maximum.", 400)
	}
	for _, c := range selected {
		if ValidatePath(c.Path, false) != nil || !s.allows(c.Path, false) {
			return nil, remaining, contract.Fail("PATH_NOT_INDEXED", "Change is outside the requested scope.", 400)
		}
	}
	if len(selected) == 0 {
		return []Change{}, remaining, nil
	}
	p, err := s.repoPath(repo)
	if err != nil {
		return nil, remaining, err
	}
	out := make([]Change, 0, len(selected))
	for _, c := range selected {
		c.Patch = ""
		c.Binary = false
		c.PatchTruncated = false
		// Neither operation can execute repository-provided diff drivers or fetch.
		stat, e := s.run(ctx, p, 16384, "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--numstat", "-z", base, head, "--", c.Path)
		if e != nil {
			return nil, remaining, e
		}
		c.Binary = bytes.HasPrefix(stat, []byte("-\t-\t"))
		if !c.Binary && remaining > 0 {
			limit := 64 << 10
			if remaining < limit {
				limit = remaining
			}
			patch, e := s.run(ctx, p, limit, "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--no-color", "--submodule=short", "--unified=3", base, head, "--", c.Path)
			if e != nil {
				var ce *contract.Error
				if errors.As(e, &ce) && ce.Code == "GIT_OUTPUT_LIMIT" {
					c.PatchTruncated = true
					remaining -= limit
				} else {
					return nil, remaining, e
				}
			} else if !utf8.Valid(patch) {
				c.Binary = true
			} else {
				c.Patch = string(patch)
				remaining -= len(patch)
			}
		} else if !c.Binary {
			c.PatchTruncated = true
		}
		out = append(out, c)
	}
	return out, remaining, nil
}
