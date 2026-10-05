// Package indexer creates immutable, independently identified snapshot shards.
package indexer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/context4ai/sourcegraph/internal/gitstore"
	"github.com/context4ai/sourcegraph/internal/zoektclient"
	"github.com/sourcegraph/zoekt"
	"github.com/sourcegraph/zoekt/index"
	"github.com/sourcegraph/zoekt/query"
	"os"
	"path/filepath"
	"time"
)

const (
	MaxManifestBytes = 32 << 20
	maxFileBytes     = 2 << 20
	// Like rg, every non-binary file within the size limit is searchable. The
	// engine's default cap of 20000 distinct trigrams per file drops long CJK
	// prose and exported XML; a cap at the byte limit is never reached, so the
	// engine also skips counting.
	maxFileTrigrams = maxFileBytes
)

type Artifact struct {
	Links     *gitstore.LinkSnapshot `json:",omitempty"`
	Target    zoektclient.Target
	Shards    []string
	Documents int
	Bytes     int64
}
type Builder struct {
	GitRoot string
	MinFree uint64
	Root    string
}

// Build indexes every file of one commit from Git objects, like BuildPaths
// without a path selection. The caller holds the repository preparation lock.
func (b Builder) Build(ctx context.Context, t zoektclient.Target, opts Options) (Artifact, error) {
	a := Artifact{Target: t}
	if e := t.Validate(); e != nil {
		return a, e
	}
	gitRoot := b.GitRoot
	if gitRoot == "" {
		gitRoot = b.Root
	}
	store, e := gitstore.New(gitRoot)
	if e != nil {
		return a, e
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	entries, e := store.SnapshotEntries(ctx, t.Repo, t.Commit, nil)
	if e != nil {
		return a, e
	}
	return b.indexBlobs(ctx, store, t, entries, map[string]string{"commit": t.Commit, "logical_repo": t.Repo}, opts)
}

func (b Builder) publish(ctx context.Context, t zoektclient.Target, idx string) (Artifact, error) {
	return b.publishLinks(ctx, t, idx, nil)
}
func (b Builder) publishLinks(ctx context.Context, t zoektclient.Target, idx string, links *gitstore.LinkSnapshot) (result Artifact, resultErr error) {
	var published []string
	manifest := ""
	defer func() {
		if resultErr != nil {
			for _, p := range published {
				_ = os.Remove(p)
			}
			if manifest != "" {
				_ = os.Remove(manifest)
			}
		}
	}()
	a := Artifact{Target: t, Links: links}
	if err := ctx.Err(); err != nil {
		return a, err
	}
	files, e := filepath.Glob(filepath.Join(idx, "*.zoekt"))
	if e != nil || len(files) == 0 {
		return a, errors.New("index produced no shards")
	}
	for _, p := range files {
		n, e := inspect(ctx, p, t)
		if e != nil {
			return a, e
		}
		syncFile, e := os.Open(p)
		if e != nil {
			return a, e
		}
		e = syncFile.Sync()
		syncFile.Close()
		if e != nil {
			return a, e
		}
		a.Documents += n
		st, e := os.Stat(p)
		if e != nil {
			return a, e
		}
		a.Bytes += st.Size()
		a.Shards = append(a.Shards, filepath.Base(p))
	}
	// Manifest is durable before publication. Partial publication is never ready;
	// callers verify all expected shards and documents against engine inventory.
	data, err := json.Marshal(a)
	if err != nil {
		return a, err
	}
	if len(data) > MaxManifestBytes {
		return a, errors.New("snapshot link manifest exceeded its budget")
	}
	man := filepath.Join(b.Root, "manifests")
	if e = os.MkdirAll(man, 0700); e != nil {
		return a, e
	}
	if e = durable(filepath.Join(man, fmt.Sprintf("%d.json", t.RepositoryID)), data); e != nil {
		return a, e
	}
	manifest = filepath.Join(man, fmt.Sprintf("%d.json", t.RepositoryID))
	md, err := os.Open(man)
	if err != nil {
		return a, err
	}
	err = md.Sync()
	md.Close()
	if err != nil {
		return a, err
	}
	dest := filepath.Join(b.Root, "index")
	if e = os.MkdirAll(dest, 0700); e != nil {
		return a, e
	}
	for _, p := range files {
		if err := ctx.Err(); err != nil {
			return a, err
		}
		to := filepath.Join(dest, filepath.Base(p))
		if _, e = os.Lstat(to); !os.IsNotExist(e) {
			return a, errors.New("generation already exists")
		}
		if e = os.Rename(p, to); e != nil {
			return a, e
		}
		published = append(published, to)
	}
	d, e := os.Open(dest)
	if e != nil {
		return a, e
	}
	defer d.Close()
	if e = d.Sync(); e != nil {
		return a, e
	}
	if err := ctx.Err(); err != nil {
		return a, err
	}
	return a, nil
}
func durable(p string, data []byte) error {
	f, e := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	_, e = f.Write(data)
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	return ce
}
func inspect(ctx context.Context, p string, t zoektclient.Target) (int, error) {
	f, e := os.Open(p)
	if e != nil {
		return 0, e
	}
	inf, e := index.NewIndexFile(f)
	if e != nil {
		f.Close()
		return 0, e
	}
	s, e := index.NewSearcher(inf)
	if e != nil {
		inf.Close()
		return 0, e
	}
	defer s.Close()
	list, e := s.List(ctx, &query.Const{Value: true}, &zoekt.ListOptions{})
	if e != nil {
		return 0, e
	}
	if len(list.Repos) != 1 {
		return 0, errors.New("unexpected shard repository count")
	}
	r := list.Repos[0]
	if r.Repository.ID != t.RepositoryID || r.Repository.Name != t.EngineName() || len(r.Repository.Branches) != 1 || r.Repository.Branches[0].Version != t.Commit || r.Repository.Branches[0].Name != "snapshot" {
		return 0, errors.New("shard identity mismatch")
	}
	return r.Stats.Documents, nil
}
