// Package zoektclient connects only to a private loopback Zoekt process.
// Callers must resolve an authorized, ready generation before invoking Search.
package zoektclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/gitstore"
)

const MaxResponseBytes = 2 << 20
const MaxQueryBytes = 8192

// Generated queries spell out Unicode classes and glob filters, so they may
// legitimately exceed the caller's pattern budget.
const maxEngineQueryBytes = 1 << 20

var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Target is an internal registry result, never a public request field.
// RepositoryID identifies one immutable generation, not the logical repository.
type Target struct {
	Repo         string
	Group        string
	Commit       string
	RepositoryID uint32
}

type Client struct {
	endpoint string
	http     *http.Client
}

func New(endpoint string) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("engine endpoint must be a loopback HTTP origin")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() || u.Port() == "" {
		return nil, errors.New("engine endpoint must use a loopback IP and explicit port")
	}
	transport := &http.Transport{Proxy: nil, MaxIdleConns: 8, MaxIdleConnsPerHost: 8, IdleConnTimeout: 30 * time.Second,
		DialContext: (&net.Dialer{Timeout: time.Second}).DialContext, ResponseHeaderTimeout: 3 * time.Second}
	return &Client{endpoint: strings.TrimSuffix(endpoint, "/") + "/api/search", http: &http.Client{
		Transport: transport, Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("engine redirects are disabled") },
	}}, nil
}
func (c *Client) Close() { c.http.CloseIdleConnections() }

// Search validates every match and bounds the native Result projection.
// It does not interpret an empty result as index readiness or complete coverage.
func (c *Client) Search(ctx context.Context, target Target, query string) (json.RawMessage, error) {
	return c.SearchMany(ctx, []Target{target}, query)
}

// SearchMany applies one shared engine budget to all groups of the same commit.
type DisplayOptions struct {
	FilesOnly bool
	Pattern   *Pattern
}

func (c *Client) SearchMany(ctx context.Context, targets []Target, query string) (json.RawMessage, error) {
	return c.SearchManyWithOptions(ctx, targets, query, DisplayOptions{})
}
func (c *Client) SearchManyWithOptions(ctx context.Context, targets []Target, query string, options DisplayOptions) (json.RawMessage, error) {
	if len(targets) < 1 || len(targets) > 16 {
		return nil, fail("INVALID_ENGINE_TARGET", "Choose 1..16 resolved groups.", 500, false)
	}
	identities := map[uint32]Target{}
	ids := make([]uint32, 0, len(targets))
	for _, target := range targets {
		if target.Validate() != nil || target.Repo != targets[0].Repo || target.Commit != targets[0].Commit {
			return nil, fail("INVALID_ENGINE_TARGET", "Groups must use the same repository and commit.", 500, false)
		}
		if _, exists := identities[target.RepositoryID]; exists {
			return nil, fail("INVALID_ENGINE_TARGET", "Duplicate generation.", 500, false)
		}
		identities[target.RepositoryID] = target
		ids = append(ids, target.RepositoryID)
	}
	if len(query) == 0 || len(query) > maxEngineQueryBytes || !utf8.ValidString(query) || strings.ContainsRune(query, 0) {
		return nil, fail("INVALID_QUERY", "Query must be valid UTF-8 within the query budget.", 400, false)
	}
	var filter *patternFilter
	if options.Pattern != nil {
		var err error
		filter, err = newPatternFilter(*options.Pattern)
		if err != nil {
			return nil, err
		}
	}
	// Only oversized responses are retried. All attempts retain the same pinned
	// targets and share a deadline, so fallback cannot turn into unbounded work.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	budgets := []displayBudget{{100, 1000, 2, false}, {20, 100, 0, false}, {100, 1000, 0, true}}
	if options.FilesOnly {
		budgets = []displayBudget{{100, 1000, 0, true}, {20, 100, 0, true}}
	}
	if filter.active() && filter.boundary != nil {
		budgets = []displayBudget{{100, 1000, 2, false}, {20, 100, 0, false}}
	}
	for attempt, budget := range budgets {
		result, err := c.searchAttempt(ctx, identities, ids, query, budget)
		var ce *contract.Error
		if errors.As(err, &ce) && ce.Code == "ENGINE_RESPONSE_TOO_LARGE" {
			continue
		}
		if err != nil {
			return nil, err
		}
		if filter.active() {
			result, err = filter.apply(ctx, result)
			if errors.As(err, &ce) && ce.Code == "ENGINE_RESPONSE_TOO_LARGE" {
				continue
			}
			if err != nil {
				return nil, err
			}
		}
		if options.FilesOnly {
			budget.filesOnly = true
		}
		if attempt > 0 || budget.filesOnly {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(result, &fields); err != nil {
				return nil, err
			}
			if attempt > 0 {
				fields["ResponseTruncated"] = json.RawMessage("true")
			}
			if budget.filesOnly {
				fields["ResponseFilesOnly"] = json.RawMessage("true")
				// Zoekt represents a file-only projection as a synthetic path
				// match. Do not claim that the user's predicate matched its path.
				var files []map[string]json.RawMessage
				if err := json.Unmarshal(fields["Files"], &files); err != nil {
					return nil, err
				}
				for _, file := range files {
					delete(file, "LineMatches")
					delete(file, "ChunkMatches")
				}
				fields["Files"], _ = json.Marshal(files)
			}
			return json.Marshal(fields)
		}
		return result, nil
	}
	return nil, fail("ENGINE_RESPONSE_TOO_LARGE", "Search response exceeded the budget; narrow the query or file path.", 502, false)
}

type displayBudget struct {
	files, matches, contextLines int
	filesOnly                    bool
}

func (c *Client) searchAttempt(ctx context.Context, identities map[uint32]Target, ids []uint32, query string, budget displayBudget) (json.RawMessage, error) {
	if budget.filesOnly {
		// This changes only the returned projection, not the matching predicate.
		// It also handles one minified line larger than the whole wire budget.
		query = "type:file ( " + query + " )"
	}
	body, _ := json.Marshal(struct {
		Q       string
		RepoIDs []uint32
		Opts    map[string]any
	}{query, ids, map[string]any{
		"MaxWallTime": int64(time.Second), "MaxDocDisplayCount": budget.files,
		"ShardMaxMatchCount": 10000, "TotalMaxMatchCount": 10000,
		"MaxMatchDisplayCount": budget.matches, "NumContextLines": budget.contextLines,
	}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fail("ENGINE_UNAVAILABLE", "Search engine request failed.", 503, true)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusBadRequest {
			return nil, fail("INVALID_QUERY", "Search engine rejected the query.", 400, false)
		}
		return nil, fail("ENGINE_UNAVAILABLE", "Search engine request failed.", 503, true)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fail("ENGINE_RESPONSE_INVALID", "Search engine response was incomplete.", 502, true)
	}
	if len(data) > MaxResponseBytes {
		return nil, fail("ENGINE_RESPONSE_TOO_LARGE", "Search response exceeded the budget.", 502, false)
	}
	var envelope struct{ Result json.RawMessage }
	if json.Unmarshal(data, &envelope) != nil || len(envelope.Result) == 0 || bytes.Equal(envelope.Result, []byte("null")) {
		return nil, fail("ENGINE_RESPONSE_INVALID", "Search engine response was invalid.", 502, false)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(envelope.Result, &fields) != nil || fields["Files"] == nil {
		return nil, fail("ENGINE_RESPONSE_INVALID", "Search engine response was invalid.", 502, false)
	}
	var result struct {
		Files []struct {
			Repository   string
			RepositoryID uint32
			Version      string
			FileName     string
		}
	}
	if json.Unmarshal(envelope.Result, &result) != nil || len(result.Files) > 100 {
		return nil, fail("ENGINE_RESPONSE_INVALID", "Search engine response was invalid.", 502, false)
	}
	for _, match := range result.Files {
		target, ok := identities[match.RepositoryID]
		if !ok || match.Repository != target.EngineName() || match.RepositoryID != target.RepositoryID || match.Version != target.Commit || gitstore.ValidatePath(match.FileName, false) != nil {
			return nil, fail("ENGINE_VERSION_MISMATCH", "Search results did not match the requested generation.", 502, false)
		}
	}
	return envelope.Result, nil
}

func fail(code, message string, status int, retryable bool) error {
	return &contract.Error{Code: code, Message: message, HTTPStatus: status, Retryable: retryable}
}

// EngineName must also be used by the indexer. Zoekt List aggregates by name;
// logical names would combine multiple generations and hide partial loads.
func (t Target) EngineName() string { return fmt.Sprintf("snapshot/%d", t.RepositoryID) }

func (t Target) Validate() error {
	if gitstore.ValidateRepo(t.Repo) != nil || (t.Group != "" && gitstore.ValidateGroup(t.Group) != nil) || !fullSHA.MatchString(t.Commit) || t.RepositoryID == 0 {
		return fail("INVALID_ENGINE_TARGET", "A resolved generation is required.", 500, false)
	}
	return nil
}
