package gitstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/context4ai/sourcegraph/internal/contract"
)

var groupName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

func ValidateGroup(group string) error {
	if !groupName.MatchString(group) {
		return contract.Fail("INVALID_GROUP", "Use a group name with letters, numbers, underscores or hyphens.", 400)
	}
	return nil
}

// GroupRepositoryDir keeps independently fetched groups within the logical
// repository's cleanup boundary. No user path becomes a filesystem component.
func GroupRepositoryDir(root, repo, group string) (string, error) {
	p, err := RepositoryDir(root, repo)
	if err != nil {
		return "", err
	}
	if err = ValidateGroup(group); err != nil {
		return "", err
	}
	h := sha256.Sum256([]byte(group))
	return filepath.Join(filepath.Dir(p), "groups", hex.EncodeToString(h[:]), "bare.git"), nil
}
func (s *Store) repositoryDir(repo string) (string, error) {
	if s.boundRepo != "" && s.boundRepo != repo {
		return "", contract.Fail("INVALID_REPO", "Store is bound to a different repository.", 400)
	}
	if s.group != "" {
		return GroupRepositoryDir(s.root, repo, s.group)
	}
	return RepositoryDir(s.root, repo)
}
func (s *Store) ForGroup(repo, group string) (*Store, error) {
	if _, err := GroupRepositoryDir(s.root, repo, group); err != nil {
		return nil, err
	}
	copy := *s
	copy.group = group
	copy.boundRepo = repo
	copy.paths = nil
	return &copy, nil
}

// NormalizePaths canonicalizes literal directory/file prefixes, removing
// duplicate and nested prefixes. Empty input denotes the entire repository.
func NormalizePaths(paths []string) ([]string, error) {
	if len(paths) > 128 {
		return nil, contract.Fail("INVALID_PATHS", "Choose at most 128 paths.", 400)
	}
	result := append([]string(nil), paths...)
	for _, p := range result {
		if err := ValidatePath(p, false); err != nil {
			return nil, err
		}
		for _, segment := range strings.Split(p, "/") {
			if strings.EqualFold(segment, ".git") {
				return nil, contract.Fail("INVALID_PATHS", "Git metadata directories cannot be indexed.", 400)
			}
		}
	}
	sort.Strings(result)
	out := make([]string, 0, len(result))
	for _, p := range result {
		covered := false
		for _, parent := range out {
			if p == parent || strings.HasPrefix(p, parent+"/") {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, p)
		}
	}
	return out, nil
}
func (s *Store) WithPaths(paths []string) (*Store, error) {
	normalized, err := NormalizePaths(paths)
	if err != nil {
		return nil, err
	}
	copy := *s
	copy.paths = normalized
	return &copy, nil
}
func (s *Store) allows(path string, ancestors bool) bool {
	if len(s.paths) == 0 {
		return true
	}
	for _, p := range s.paths {
		if path == p || strings.HasPrefix(path, p+"/") || (ancestors && (path == "" || strings.HasPrefix(p, path+"/"))) {
			return true
		}
	}
	return false
}
func (s *Store) scopeKey() string {
	if len(s.paths) == 0 {
		return ""
	}
	h := sha256.Sum256([]byte(strings.Join(s.paths, "\x00")))
	return fmt.Sprintf("%s:%d", hex.EncodeToString(h[:]), s.generation)
}

// SnapshotEntries reads only tree metadata, never blob sizes. This remains safe
// on a partial clone even when unselected blobs are absent.
func (s *Store) SnapshotEntries(ctx context.Context, repo, commit string, paths []string) ([]Entry, error) {
	entries, _, err := s.SnapshotEntriesWithCoverage(ctx, repo, commit, paths)
	return entries, err
}

// SnapshotEntriesWithCoverage reports whether API-incompatible paths were
// excluded, without rewriting their names or weakening request validation.
func (s *Store) SnapshotEntriesWithCoverage(ctx context.Context, repo, commit string, paths []string) ([]Entry, bool, error) {
	normalized, err := NormalizePaths(paths)
	if err != nil {
		return nil, false, err
	}
	p, err := s.fixed(ctx, repo, commit)
	if err != nil {
		return nil, false, err
	}
	args := []string{"ls-tree", "-r", "-z", commit, "--"}
	args = append(args, normalized...)
	data, err := s.run(ctx, p, 64<<20, args...)
	if err != nil {
		return nil, false, err
	}
	return parseReadableEntries(data, "")
}

// Blob reads an already hydrated object. It never contacts a remote.
func (s *Store) Blob(ctx context.Context, repo, commit, oid string, max int) ([]byte, error) {
	if !fullSHA.MatchString(oid) || max < 1 || max > MaxScanBytes {
		return nil, contract.Fail("INVALID_BLOB", "Invalid object request.", 400)
	}
	p, err := s.fixed(ctx, repo, commit)
	if err != nil {
		return nil, err
	}
	return s.prefix(ctx, p, oid, max)
}

// BlobSizes uses one cat-file process for the selected objects only.
func (s *Store) BlobSizes(ctx context.Context, repo, commit string, entries []Entry) (map[string]int64, error) {
	p, err := s.fixed(ctx, repo, commit)
	if err != nil {
		return nil, err
	}
	return s.blobSizes(ctx, p, entries)
}

// WithCoverage accepts internally computed immutable coverage, not user input.
func (s *Store) WithCoverage(paths []string, generation uint32) (*Store, error) {
	for _, p := range paths {
		if err := ValidatePath(p, false); err != nil {
			return nil, err
		}
	}
	copy := *s
	copy.paths = compactLinkPaths(append([]string(nil), paths...))
	copy.generation = generation
	return &copy, nil
}
