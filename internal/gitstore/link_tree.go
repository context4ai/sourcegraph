package gitstore

import (
	"github.com/context4ai/sourcegraph/internal/contract"
	"path"
)

// Cache directory metadata per immutable traversal, not one Git process per
// component and per alias. It contains no file bodies and never hydrates.
func (r *linkResolver) children(parent string) (map[string]Entry, error) {
	if entries, ok := r.trees[parent]; ok {
		return entries, nil
	}
	data, err := r.store.run(r.ctx, r.dir, 64<<20, "ls-tree", "-z", r.commit+":"+parent)
	if err != nil {
		return nil, err
	}
	entries, skipped, err := parseReadableEntries(data, parent)
	r.unsupportedPathsSkipped = r.unsupportedPathsSkipped || skipped
	if err != nil {
		return nil, err
	}
	byPath := map[string]Entry{}
	for _, e := range entries {
		byPath[e.Path] = e
	}
	if r.trees == nil {
		r.trees = map[string]map[string]Entry{}
	}
	r.trees[parent] = byPath
	return byPath, nil
}
func (r *linkResolver) entry(name string) (Entry, error) {
	parent := path.Dir(name)
	if parent == "." {
		parent = ""
	}
	entries, err := r.children(parent)
	if err != nil {
		return Entry{}, err
	}
	if e, ok := entries[name]; ok {
		return e, nil
	}
	return Entry{}, contract.Fail("FILE_NOT_FOUND", "Path was not found.", 404)
}
