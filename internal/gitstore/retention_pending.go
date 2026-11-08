package gitstore

import (
	"os"
	"path/filepath"
	"strings"
)

const retentionMarker = "snapshot-retention"

// InvalidateRetention removes the retention marker before snapshots are
// revoked, so the next RetainSnapshots repacks and RetentionPending reports
// work until a collection succeeds and writes a new marker.
func (s *Store) InvalidateRetention(repo string) error {
	dir, err := s.repositoryDir(repo)
	if err != nil {
		return err
	}
	// Only a repository directory that does not exist yet has no marker to
	// invalidate; access or validation failures must stop the revocation.
	if _, err = os.Lstat(dir); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	p, err := s.repoPath(repo)
	if err != nil {
		return err
	}
	if err = os.Remove(filepath.Join(p, retentionMarker)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// RetentionPending reports whether Git objects may await collection: the
// marker is missing (invalidated before a revocation, or never written) or
// names a commit that commits no longer contains. A repository directory that
// does not exist has nothing to collect. Any other failure to inspect is
// returned, so callers can tell "nothing to do" from "cannot check". It never
// runs Git.
func (s *Store) RetentionPending(repo string, commits []string) (bool, error) {
	if len(commits) == 0 {
		return false, nil
	}
	dir, err := s.repositoryDir(repo)
	if err != nil {
		return false, err
	}
	if _, err = os.Lstat(dir); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	p, err := s.repoPath(repo)
	if err != nil {
		return false, err
	}
	previous, err := os.ReadFile(filepath.Join(p, retentionMarker))
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	current := map[string]bool{}
	for _, sha := range commits {
		current[sha] = true
	}
	for _, sha := range strings.Fields(string(previous)) {
		if !current[sha] {
			return true, nil
		}
	}
	return false, nil
}
