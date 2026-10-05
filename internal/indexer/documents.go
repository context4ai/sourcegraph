package indexer

import (
	"bytes"
	"context"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/context4ai/sourcegraph/internal/gitstore"
	"github.com/context4ai/sourcegraph/internal/zoektclient"
)

// Options are per-repository index settings. They apply to the next build;
// published generations keep the settings they were built with.
type Options struct {
	// SkipGenerated leaves minified JavaScript/CSS and source maps out of the
	// text index. Lock files and generated source code stay searchable.
	SkipGenerated bool
	// NoFollowLinks retains legacy indexing when explicitly disabled.
	NoFollowLinks bool
	LinkLimits    *gitstore.LinkLimits
	Paths         []string
	Hydrate       func([]gitstore.Entry) error
}

// minifiedLineBytes follows go-enry/linguist: a JS or CSS file whose average
// line is longer than this is treated as minified.
const minifiedLineBytes = 110

// Generated reports whether content is a minified bundle or a source map.
func Generated(name string, content []byte) bool {
	lower := strings.ToLower(name)
	ext := path.Ext(lower)
	if ext == ".map" {
		return strings.HasSuffix(lower, ".js.map") || strings.HasSuffix(lower, ".css.map") || bytes.HasPrefix(bytes.TrimLeft(content, " \t\r\n"), []byte(`{"version":`))
	}
	switch ext {
	case ".js", ".mjs", ".cjs", ".css":
	default:
		return false
	}
	lines := bytes.Count(content, []byte{'\n'})
	if len(content) > 0 && content[len(content)-1] != '\n' {
		lines++
	}
	return lines > 0 && (len(content)-bytes.Count(content, []byte{'\n'}))/lines > minifiedLineBytes
}

// indexBlobs builds one generation from Git objects without a checkout. Paths
// and the commit ID are the repository's own; nothing is rewritten.
func (b Builder) indexBlobs(ctx context.Context, store *gitstore.Store, t zoektclient.Target, entries []gitstore.Entry, metadata map[string]string, opts Options) (Artifact, error) {
	artifact := Artifact{Target: t}
	stage, err := os.MkdirTemp(b.Root, ".index-stage-")
	if err != nil {
		return artifact, err
	}
	defer os.RemoveAll(stage)
	idx := filepath.Join(stage, "shards")
	if err = os.Mkdir(idx, 0700); err != nil {
		return artifact, err
	}
	links := &gitstore.LinkSnapshot{Format: 1, Followed: false, Paths: opts.Paths}
	if !opts.NoFollowLinks {
		limits := gitstore.DefaultLinkLimits()
		if opts.LinkLimits != nil {
			limits = *opts.LinkLimits
		}
		entries, links, err = store.ExpandLinks(ctx, t.Repo, t.Commit, entries, opts.Paths, limits, opts.Hydrate, func(name string, data []byte) bool { return !opts.SkipGenerated || !Generated(name, data) })
		if err != nil {
			return artifact, err
		}
	}
	err = b.buildIsolated(ctx, store, t, entries, metadata, opts, idx)
	if err != nil {
		return artifact, err
	}
	if err = ctx.Err(); err != nil {
		return artifact, err
	}
	return b.publishLinks(ctx, t, idx, links)
}
