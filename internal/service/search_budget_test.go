package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/zoektclient"
)

type searchBudgetStore struct {
	Store
	repo control.Repository
}

func (s searchBudgetStore) Get(context.Context, string) (control.Repository, error) {
	return s.repo, nil
}

func TestSearchBudgetMetadata(t *testing.T) {
	for _, oversized := range []int{1, 2} {
		t.Run(string(rune('0'+oversized)), func(t *testing.T) {
			sha := strings.Repeat("a", 40)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/list" {
					json.NewEncoder(w).Encode(map[string]any{"List": map[string]any{"Repos": []any{map[string]any{"Repository": map[string]any{"ID": 7, "Name": "snapshot/7", "Branches": []any{map[string]any{"Name": "snapshot", "Version": sha}}}, "Stats": map[string]any{"Shards": 1, "Documents": 1}}}}})
					return
				}
				calls++
				if calls <= oversized {
					w.Write([]byte(strings.Repeat(" ", zoektclient.MaxResponseBytes+1)))
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"Result": map[string]any{"Files": []any{map[string]any{"Repository": "snapshot/7", "RepositoryID": 7, "Version": sha, "FileName": "src/a.go", "LineMatches": []any{map[string]any{"FileName": oversized == 2, "LineNumber": 1, "Line": "a2s="}}}}, "MatchCount": 1, "FileCount": 1}})
			}))
			defer server.Close()
			engine, err := zoektclient.New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			s := &Service{Engine: engine, DB: searchBudgetStore{repo: control.Repository{Name: "org/repo", Enabled: true, Versions: []control.Version{{Commit: sha, Generation: 7, Shards: 1, Files: 1}}}}}
			value, err := s.Search(context.Background(), "org/repo", contract.SearchRequest{Revision: sha, Pattern: "kk"})
			if err != nil {
				t.Fatal(err)
			}
			out := value.(map[string]any)
			meta := out["Meta"].(map[string]any)
			if meta["Truncated"] != true || meta["TruncationReason"] != "response_bytes" || meta["FilesOnly"] != (oversized == 2) || meta["Commit"] != sha {
				t.Fatalf("invalid metadata: %v", meta)
			}
			result := out["Result"].(map[string]any)
			if _, exists := result["ResponseTruncated"]; exists {
				t.Fatal("internal marker leaked")
			}
			file := result["Files"].([]any)[0].(map[string]any)
			if file["Repository"] != "org/repo" || file["Version"] != sha {
				t.Fatal("identity lost")
			}
			if oversized == 2 && file["LineMatches"] != nil {
				t.Fatal("file-only projection fabricated path matches")
			}
		})
	}
}
