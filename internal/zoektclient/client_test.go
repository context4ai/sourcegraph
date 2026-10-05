package zoektclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/context4ai/sourcegraph/internal/contract"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSearchResponseBudgetFallback(t *testing.T) {
	target := Target{Repo: "org/repo", Commit: strings.Repeat("a", 40), RepositoryID: 7}
	for _, oversized := range []int{0, 1, 2, 3} {
		t.Run(string(rune('0'+oversized)), func(t *testing.T) {
			calls := 0
			c, _ := New("http://127.0.0.1:6070")
			c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				var body struct {
					Q       string
					RepoIDs []uint32
					Opts    struct{ MaxDocDisplayCount, MaxMatchDisplayCount, NumContextLines int }
				}
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if len(body.RepoIDs) != 1 || body.RepoIDs[0] != 7 {
					t.Fatal("fallback changed target")
				}
				wantFiles := []int{100, 20, 100}[calls]
				wantMatches := []int{1000, 100, 1000}[calls]
				wantContext := []int{2, 0, 0}[calls]
				if body.Opts.MaxDocDisplayCount != wantFiles || body.Opts.MaxMatchDisplayCount != wantMatches || body.Opts.NumContextLines != wantContext {
					t.Fatalf("wrong budget: %+v", body.Opts)
				}
				wantQ := `content:"kk"`
				if calls == 2 {
					wantQ = "type:file ( " + wantQ + " )"
				}
				if body.Q != wantQ {
					t.Fatalf("predicate changed: %q", body.Q)
				}
				calls++
				data := strings.Repeat(" ", MaxResponseBytes+1)
				if calls > oversized {
					b, _ := json.Marshal(map[string]any{"Result": map[string]any{"Files": []any{map[string]any{"Repository": target.EngineName(), "RepositoryID": 7, "Version": target.Commit, "FileName": "src/a.go"}}, "MatchCount": 1, "FileCount": 1}})
					data = string(b)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(data))}, nil
			})
			result, err := c.Search(context.Background(), target, `content:"kk"`)
			if oversized == 3 {
				var ce *contract.Error
				if !errors.As(err, &ce) || ce.Code != "ENGINE_RESPONSE_TOO_LARGE" || calls != 3 {
					t.Fatalf("unbounded or wrong failure: %d %v", calls, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := json.Unmarshal(result, &fields); err != nil {
				t.Fatal(err)
			}
			if calls != oversized+1 {
				t.Fatalf("unexpected calls: %d", calls)
			}
			if (fields["ResponseTruncated"] == true) != (oversized > 0) || (fields["ResponseFilesOnly"] == true) != (oversized == 2) {
				t.Fatalf("missing degradation markers: %s", result)
			}
		})
	}
}

func TestSearchDoesNotRetryInvalidResponses(t *testing.T) {
	target := Target{Repo: "org/repo", Commit: strings.Repeat("a", 40), RepositoryID: 7}
	for _, tc := range []struct {
		status     int
		data, code string
	}{
		{400, "", "INVALID_QUERY"},
		{503, "", "ENGINE_UNAVAILABLE"},
		{200, `{"Result":{"Files":[{"Repository":"snapshot/8","RepositoryID":8,"Version":"` + target.Commit + `","FileName":"a.go"}]}}`, "ENGINE_VERSION_MISMATCH"},
		{200, `{"Result":`, "ENGINE_RESPONSE_INVALID"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			calls := 0
			c, _ := New("http://127.0.0.1:6070")
			c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.data))}, nil
			})
			_, err := c.Search(context.Background(), target, "kk")
			var ce *contract.Error
			if calls != 1 || !errors.As(err, &ce) || ce.Code != tc.code {
				t.Fatalf("%d calls, %v", calls, err)
			}
		})
	}
}

func TestRequestedFilesOnlyUsesEngineProjection(t *testing.T) {
	target := Target{Repo: "org/repo", Commit: strings.Repeat("a", 40), RepositoryID: 7}
	c, _ := New("http://127.0.0.1:6070")
	calls := 0
	c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		var body struct {
			Q    string
			Opts struct{ NumContextLines int }
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Q != "type:file ( content:needle )" || body.Opts.NumContextLines != 0 {
			t.Fatal(body)
		}
		b, _ := json.Marshal(map[string]any{"Result": map[string]any{"Files": []any{map[string]any{"Repository": target.EngineName(), "RepositoryID": 7, "Version": target.Commit, "FileName": "a.go", "LineMatches": []any{map[string]any{"LineNumber": 1}}}}, "FileCount": 1, "MatchCount": 1}})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(b)))}, nil
	})
	raw, err := c.SearchManyWithOptions(context.Background(), []Target{target}, "content:needle", DisplayOptions{FilesOnly: true})
	if err != nil || calls != 1 || strings.Contains(string(raw), "LineMatches") || strings.Contains(string(raw), "ResponseTruncated") || !strings.Contains(string(raw), "ResponseFilesOnly") {
		t.Fatalf("%s %v", raw, err)
	}
}
