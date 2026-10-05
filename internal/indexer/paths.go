package indexer

import (
	"context"
	"fmt"
	"time"

	"github.com/context4ai/sourcegraph/internal/gitstore"
	"github.com/context4ai/sourcegraph/internal/zoektclient"
)

// BuildPaths uses the same pinned Zoekt builder as the full Git indexer, but
// supplies only selected path documents. Original repo paths and commit IDs are
// retained; no synthetic commits, checkout, or query-time filtering is used.
// Callers must hydrate selected objects and hold the group preparation lock.
func (b Builder) BuildPaths(ctx context.Context, t zoektclient.Target, paths []string, opts Options) (Artifact, error) {
	artifact := Artifact{Target: t}
	if err := t.Validate(); err != nil {
		return artifact, err
	}
	if t.Group == "" || len(paths) == 0 {
		return artifact, fmt.Errorf("scoped index requires group and paths")
	}
	root := b.GitRoot
	if root == "" {
		root = b.Root
	}
	store, err := gitstore.New(root)
	if err != nil {
		return artifact, err
	}
	store, err = store.ForGroup(t.Repo, t.Group)
	if err != nil {
		return artifact, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	entries, err := store.SnapshotEntries(ctx, t.Repo, t.Commit, paths)
	if err != nil {
		return artifact, err
	}
	opts.Paths = paths
	return b.indexBlobs(ctx, store, t, entries, map[string]string{"logical_repo": t.Repo, "commit": t.Commit, "group": t.Group}, opts)
}
