package service

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/gitstore"
	"github.com/context4ai/sourcegraph/internal/indexer"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// Purge is explicit and only removes a disabled tombstone after its grace period.
// Single-instance locks protect both in-flight reads and background preparation.
func (s *Service) Purge(ctx context.Context, name string, revision int64) error {
	if !s.preparation.TryLock() {
		return contract.Fail("PREPARATION_ACTIVE", "Wait for preparation to stop before cleanup.", 409)
	}
	defer s.preparation.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	r, e := s.Repo(ctx, name)
	if e != nil {
		return e
	}
	if !r.Deleted || r.Enabled || r.Revision != revision || time.Since(r.Updated) < time.Minute {
		return contract.Fail("CLEANUP_NOT_READY", "Disable and delete the repository, then wait one minute before cleanup with its current revision.", 409)
	}
	db, ok := s.DB.(interface {
		Delete(context.Context, control.Repository) error
	})
	if !ok {
		return errors.New("cleanup unavailable")
	}
	entries, e := os.ReadDir(filepath.Join(s.Root, "manifests"))
	if e != nil && !os.IsNotExist(e) {
		return e
	}
	if len(entries) > 10000 {
		return errors.New("manifest scan budget exceeded")
	}
	for _, v := range entries {
		if v.IsDir() || !regexp.MustCompile(`^[0-9]+\.json$`).MatchString(v.Name()) {
			continue
		}
		p := filepath.Join(s.Root, "manifests", v.Name())
		st, e := os.Lstat(p)
		if e != nil {
			return e
		}
		if !st.Mode().IsRegular() || st.Size() > indexer.MaxManifestBytes {
			return errors.New("invalid manifest")
		}
		b, e := os.ReadFile(p)
		if e != nil {
			return e
		}
		var a indexer.Artifact
		if json.Unmarshal(b, &a) != nil {
			return errors.New("invalid manifest")
		}
		if a.Target.Repo != name {
			continue
		}
		if e = a.Target.Validate(); e != nil {
			return e
		}
		pattern := regexp.MustCompile(`^snapshot-` + regexp.QuoteMeta(v.Name()[:len(v.Name())-5]) + `_[A-Za-z0-9_.-]+\.zoekt$`)
		for _, shard := range a.Shards {
			if !pattern.MatchString(shard) {
				return errors.New("invalid shard manifest path")
			}
			if e = os.Remove(filepath.Join(s.Root, "index", shard)); e != nil && !os.IsNotExist(e) {
				return e
			}
		}
		if e = os.Remove(p); e != nil {
			return e
		}
	}
	p, e := gitstore.RepositoryDir(s.gitRoot(), name)
	if e != nil {
		return e
	}
	if e = os.RemoveAll(filepath.Dir(p)); e != nil {
		return e
	}
	return db.Delete(ctx, r)
}
