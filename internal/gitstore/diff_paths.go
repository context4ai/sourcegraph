package gitstore

import (
	"bytes"
	"context"
	"strings"

	"github.com/context4ai/sourcegraph/internal/contract"
)

type DiffPathStatus struct {
	Path       string `json:"path"`
	BaseExists bool   `json:"base_exists"`
	HeadExists bool   `json:"head_exists"`
}

// Inspect only effective registered prefixes. Batched literal ls-tree queries
// distinguish absence from Git failure, without fetching or following links.
func (s *Store) DiffPathStatus(ctx context.Context, repo, base, head string) ([]DiffPathStatus, error) {
	if len(s.paths) == 0 {
		return nil, nil
	}
	p, err := s.fixed(ctx, repo, base)
	if err != nil {
		return nil, err
	}
	if _, err = s.fixed(ctx, repo, head); err != nil {
		return nil, err
	}
	inspect := func(commit string) (map[string]bool, error) {
		args := append([]string{"ls-tree", "-z", "--full-tree", commit, "--"}, s.paths...)
		raw, err := s.run(ctx, p, 8<<20, args...)
		if err != nil {
			return nil, err
		}
		found := map[string]bool{}
		for _, record := range bytes.Split(raw, []byte{0}) {
			if len(record) == 0 {
				continue
			}
			_, name, ok := strings.Cut(string(record), "\t")
			if !ok {
				return nil, contract.Fail("INVALID_GIT_RESULT", "Malformed path presence response.", 500)
			}
			for _, path := range s.paths {
				prefix := strings.TrimSuffix(path, "/")
				if name == prefix || strings.HasPrefix(name, prefix+"/") {
					found[path] = true
				}
			}
		}
		return found, nil
	}
	a, err := inspect(base)
	if err != nil {
		return nil, err
	}
	b := a
	if head != base {
		b, err = inspect(head)
		if err != nil {
			return nil, err
		}
	}
	out := make([]DiffPathStatus, 0, len(s.paths))
	for _, path := range s.paths {
		out = append(out, DiffPathStatus{Path: path, BaseExists: a[path], HeadExists: b[path]})
	}
	return out, nil
}
