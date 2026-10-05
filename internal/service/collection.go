package service

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/indexer"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"
)

// Collect removes at most one retired generation per call. Readers and the
// preparation worker exclude collection; authoritative registry failures retain
// bytes. Git object GC is deliberately separate from index retention.
func (s *Service) Collect(ctx context.Context) error {
	if !s.preparation.TryLock() {
		return nil
	}
	defer s.preparation.Unlock()
	entries, e := os.ReadDir(filepath.Join(s.Root, "manifests"))
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	if len(entries) > 10000 {
		return errors.New("manifest scan budget exceeded")
	}
	if len(entries) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for checked := 0; checked < 10 && checked < len(entries); checked++ {
		s.gcCursor %= len(entries)
		v := entries[s.gcCursor]
		s.gcCursor++
		if v.IsDir() || !regexp.MustCompile(`^[0-9]+\.json$`).MatchString(v.Name()) {
			continue
		}
		p := filepath.Join(s.Root, "manifests", v.Name())
		info, e := os.Lstat(p)
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() || info.Size() > indexer.MaxManifestBytes {
			return errors.New("invalid manifest")
		}
		if time.Since(info.ModTime()) < 5*time.Minute {
			continue
		}
		b, e := os.ReadFile(p)
		if e != nil {
			return e
		}
		var a indexer.Artifact
		if json.Unmarshal(b, &a) != nil || a.Target.Validate() != nil || v.Name() != strconv.FormatUint(uint64(a.Target.RepositoryID), 10)+".json" {
			return errors.New("invalid generation manifest")
		}
		r, e := s.DB.Get(ctx, a.Target.Repo)
		if e != nil {
			var ce *contract.Error
			if !errors.As(e, &ce) || ce.Code != "REPOSITORY_NOT_FOUND" {
				return e
			}
		}
		referenced := false
		for _, version := range r.Scope(a.Target.Group).Versions {
			if version.Generation == a.Target.RepositoryID {
				referenced = true
				break
			}
		}
		if referenced {
			continue
		}
		allowed := regexp.MustCompile(`^snapshot-` + strconv.FormatUint(uint64(a.Target.RepositoryID), 10) + `_[A-Za-z0-9_.-]+\.zoekt$`)
		for _, name := range a.Shards {
			if !allowed.MatchString(name) {
				return errors.New("invalid shard path")
			}
			if e = os.Remove(filepath.Join(s.Root, "index", name)); e != nil && !os.IsNotExist(e) {
				return e
			}
		}
		return os.Remove(p)
	}
	return nil
}
