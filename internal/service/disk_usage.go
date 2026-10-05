package service

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/gitstore"
)

var errDiskChanged = errors.New("repository changed during disk measurement")

func (s *Service) diskSignal() chan struct{} {
	s.diskWakeOnce.Do(func() { s.diskWake = make(chan struct{}, 1) })
	return s.diskWake
}

func (s *Service) wakeDiskUsage() {
	select {
	case s.diskSignal() <- struct{}{}:
	default:
	}
}

// Successful workers persist one pending bit, never walk disks in the request
// or preparation path. One scanner coalesces notifications, retries failures,
// and backfills existing repositories after startup. No unbounded goroutines.
func (s *Service) runDiskUsage(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		after := ""
		for ctx.Err() == nil {
			rows, err := s.DB.List(ctx, after, 100)
			if err != nil {
				break
			}
			for _, r := range rows {
				after = r.ID
				if r.Deleted || (!r.DiskUsagePending && r.DiskUsage != nil && r.DiskUsage.StorageID == s.storageID()) || (r.ObservedAt.IsZero() && len(r.Scopes) == 0) {
					continue
				}
				// A failed sample leaves the last good value and pending bit unchanged.
				budget, cancel := context.WithTimeout(ctx, 2*time.Minute)
				_ = s.sampleDiskUsage(budget, r.Name)
				cancel()
			}
			if len(rows) < 100 {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-s.diskSignal():
		case <-tick.C:
		}
	}
}

func (s *Service) sampleDiskUsage(ctx context.Context, name string) error {
	// Protect the configured disk root, not the global query lock. Fetch, index,
	// deletion and queries may continue; revision CAS rejects a changing sample.
	if !s.preparation.TryRLock() {
		return errDiskChanged
	}
	defer s.preparation.RUnlock()
	r, err := s.DB.Get(ctx, name)
	if err != nil {
		return err
	}
	if r.Deleted || s.markStorage(r).StorageMissing {
		return errDiskChanged
	}
	revision := r.Revision
	path, err := gitstore.RepositoryDir(s.gitRoot(), name)
	if err != nil {
		return err
	}
	meter := diskMeter{seen: map[[2]uint64]bool{}}
	if err = meter.walk(ctx, filepath.Dir(path)); err != nil {
		return err
	}
	artifacts, err := s.repositoryArtifacts(ctx, name)
	if err != nil {
		return err
	}
	for _, a := range artifacts {
		for _, shard := range a.Shards {
			if err = meter.walk(ctx, filepath.Join(s.Root, "index", shard)); err != nil {
				return err
			}
		}
		if err = meter.walk(ctx, filepath.Join(s.Root, "manifests", strconv.FormatUint(uint64(a.Target.RepositoryID), 10)+".json")); err != nil {
			return err
		}
	}
	sample := &control.DiskUsage{Bytes: meter.bytes, SampledAt: time.Now().UTC(), StorageID: s.storageID()}
	_, err = control.Mutate(ctx, s.DB, name, func(current *control.Repository) error {
		if current.Deleted || current.Revision != revision {
			return errDiskChanged
		}
		current.DiskUsage = sample
		current.DiskUsagePending = false
		return nil
	})
	return err
}

type diskMeter struct {
	bytes int64
	seen  map[[2]uint64]bool
}

// Allocated blocks, not logical file length. Do not follow symlinks and count
// hardlinked inodes only once within this repository (including all groups).
func (m *diskMeter) walk(ctx context.Context, root string) error {
	return filepath.WalkDir(root, func(_ string, entry fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return nil
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return errors.New("allocated disk measurement unsupported")
		}
		key := [2]uint64{uint64(st.Dev), uint64(st.Ino)}
		if !m.seen[key] {
			m.seen[key] = true
			m.bytes += st.Blocks * 512
		}
		return nil
	})
}
