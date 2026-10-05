package zoektclient

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// Loaded confirms one immutable generation's inventory against a validated
// artifact manifest. A successful search alone cannot establish completeness.
// Callers must keep files immutable and retain them while a query holds a lease.
func (c *Client) Loaded(ctx context.Context, target Target, shards, documents int) (bool, error) {
	if shards < 1 || documents < 0 || target.RepositoryID == 0 || !fullSHA.MatchString(target.Commit) {
		return false, fail("INVALID_ENGINE_TARGET", "A validated artifact manifest is required.", 500, false)
	}
	body, _ := json.Marshal(map[string]any{"Q": "repo:^" + target.EngineName() + "$", "Opts": map[string]int{"Field": 0}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(c.endpoint, "search")+"list", bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, fail("ENGINE_UNAVAILABLE", "Engine inventory unavailable.", 503, true)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return false, fail("ENGINE_UNAVAILABLE", "Engine inventory unavailable.", 503, true)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 65537))
	if err != nil || len(data) > 65536 {
		return false, fail("ENGINE_RESPONSE_INVALID", "Engine inventory exceeded the budget or was incomplete.", 502, false)
	}
	var reply struct {
		List *struct {
			Crashes int
			Repos   []struct {
				Repository struct {
					ID       uint32
					Name     string
					Branches []struct {
						Name    string
						Version string
					}
				}
				Stats struct {
					Shards    int
					Documents int
				}
			}
		}
	}
	if json.Unmarshal(data, &reply) != nil || reply.List == nil || reply.List.Crashes != 0 || len(reply.List.Repos) > 1 {
		return false, fail("ENGINE_RESPONSE_INVALID", "Engine inventory is invalid.", 502, false)
	}
	if len(reply.List.Repos) == 0 {
		return false, nil
	}
	entry := reply.List.Repos[0]
	if entry.Repository.ID != target.RepositoryID || entry.Repository.Name != target.EngineName() || len(entry.Repository.Branches) != 1 || entry.Repository.Branches[0].Version != target.Commit || entry.Repository.Branches[0].Name != "snapshot" {
		return false, fail("ENGINE_VERSION_MISMATCH", "Engine inventory does not match the generation.", 502, false)
	}
	if entry.Stats.Shards > shards || entry.Stats.Documents > documents || entry.Stats.Shards < 0 || entry.Stats.Documents < 0 {
		return false, fail("ENGINE_RESPONSE_INVALID", "Engine inventory exceeds the artifact manifest.", 502, false)
	}
	return entry.Stats.Shards == shards && entry.Stats.Documents == documents, nil
}
