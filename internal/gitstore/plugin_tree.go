package gitstore

import (
	"bytes"
	"context"
	"strings"
)

// PluginPaths reads only tree objects, never hydrating blobs outside coverage.
func (s *Store) PluginPaths(ctx context.Context, repo, commit, root, suffix string) ([]string, error) {
	if err := ValidatePath(root, true); err != nil {
		return nil, err
	}
	p, err := s.fixed(ctx, repo, commit)
	if err != nil {
		return nil, err
	}
	args := []string{"ls-tree", "-r", "-z", "--full-tree", commit}
	if root != "" {
		args = append(args, "--", root+"/")
	}
	data, err := s.run(ctx, p, 64<<20, args...)
	if err != nil {
		return nil, err
	}
	return pluginPathsFromTree(data, root, suffix, s.allows)
}

// Unrelated Git filenames need not satisfy the API's readable-path contract.
// Validate plugin candidates only; a fixture containing a backslash or invalid
// UTF-8 elsewhere must not prevent discovering plugins in the repository.
func pluginPathsFromTree(data []byte, root, suffix string, allows func(string, bool) bool) ([]string, error) {
	out := []string{}
	for _, row := range bytes.Split(data, []byte{0}) {
		_, name, ok := bytes.Cut(row, []byte{'\t'})
		if !ok || !bytes.HasSuffix(name, []byte(suffix)) {
			continue
		}
		entries, err := parseEntries(row, "")
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.Kind == "file" && allows(e.Path, false) && (root == "" || strings.HasPrefix(e.Path, root+"/")) {
				out = append(out, e.Path)
			}
		}
	}
	return out, nil
}
