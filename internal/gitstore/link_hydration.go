package gitstore

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/context4ai/sourcegraph/internal/contract"
)

// ObjectGrowthGuard limits additional on-disk Git objects during link expansion.
// The fetch monitor calls it while Git writes, not only after hydration finishes.
// Existing objects do not consume this budget; sparse objects remain worker-only.
func (s *Store) ObjectGrowthGuard(repo string, limit int64) (func() error, error) {
	dir, err := s.repoPath(repo)
	if err != nil {
		return nil, err
	}
	size := func() (int64, error) {
		var total int64
		err := filepath.WalkDir(filepath.Join(dir, "objects"), func(p string, d fs.DirEntry, e error) error {
			if e != nil {
				if os.IsNotExist(e) {
					return nil
				}
				return e
			}
			if d.Type().IsRegular() {
				st, e := d.Info()
				if os.IsNotExist(e) {
					return nil
				}
				if e != nil {
					return e
				}
				total += st.Size()
			}
			return nil
		})
		return total, err
	}
	baseline, err := size()
	if err != nil {
		return nil, err
	}
	return func() error {
		now, err := size()
		if err != nil {
			return err
		}
		if now-baseline > limit {
			return contract.Fail("GIT_HYDRATION_BUDGET_EXCEEDED", "Link expansion exceeded its additional Git object storage budget.", 422)
		}
		return nil
	}, nil
}

// BudgetLinkHydrator also bounds logical object bytes, independently of pack
// compression. The disk guard bounds writes during a download; this check bounds
// admission and subsequent requests. Unknown remote object sizes cannot be known
// exactly before their first download.
func (s *Store) BudgetLinkHydrator(ctx context.Context, repo, commit string, fetch func([]Entry) error, limit int64) func([]Entry) error {
	seen := map[string]bool{}
	var used int64
	return func(entries []Entry) error {
		missing := make([]Entry, 0, len(entries))
		pending := map[string]bool{}
		for _, e := range entries {
			if !seen[e.BlobOID] && !pending[e.BlobOID] {
				missing = append(missing, e)
				pending[e.BlobOID] = true
			}
		}
		if len(missing) == 0 {
			return nil
		}
		entries = missing
		if used >= limit {
			return contract.Fail("GIT_HYDRATION_BUDGET_EXCEEDED", "Link object byte budget exhausted.", 422)
		}
		if err := fetch(entries); err != nil {
			return err
		}
		sizes, err := s.BlobSizes(ctx, repo, commit, entries)
		if err != nil {
			return err
		}
		next := used
		for _, e := range entries {
			next += sizes[e.BlobOID]
		}
		if next > limit {
			return contract.Fail("GIT_HYDRATION_BUDGET_EXCEEDED", "Link objects exceeded the expanded byte budget.", 422)
		}
		used = next
		for _, e := range entries {
			seen[e.BlobOID] = true
		}
		return nil
	}
}
