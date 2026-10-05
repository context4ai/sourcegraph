package service

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func (s *Service) persistStorage(v StorageSettings) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(s.Root, ".storage-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	_, err = temp.Write(data)
	if err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temp.Name(), filepath.Join(s.Root, "storage.json"))
}

// The persisted pending flag survives interruption. No new preparation starts
// until old data is removed. Retrying never touches unrelated directory entries.
// Caller holds preparation, after switching readers away from the old data,
// or is initializing before serve.
func (s *Service) finishStorageCleanup(v StorageSettings) StorageSettings {
	if !v.CleanupPending {
		return v
	}
	if !filepath.IsAbs(v.PreviousSpace) || filepath.Base(v.PreviousSpace) != "space" || within(v.PreviousSpace, v.EffectiveDirectory) || within(v.EffectiveDirectory, v.PreviousSpace) {
		return v
	}
	if s.storage.previousLock == nil {
		if _, err := os.Stat(v.PreviousSpace); err == nil {
			_, root, lock, err := prepareDirectory(filepath.Dir(v.PreviousSpace), false)
			if err != nil {
				return v
			}
			if root != v.PreviousSpace {
				lock.Close()
				return v
			}
			s.storage.previousLock = lock
		} else if !os.IsNotExist(err) {
			return v
		}
	}

	for _, target := range []struct{ dir, pattern string }{
		{filepath.Join(v.PreviousSpace, "repos"), `^[a-f0-9]{64}$`},
		{filepath.Join(s.Root, "repos"), `^[a-f0-9]{64}$`},
		{filepath.Join(s.Root, "index"), `^snapshot-[0-9]+_[A-Za-z0-9_.-]+\.zoekt$`},
		{filepath.Join(s.Root, "manifests"), `^[0-9]+\.json$`},
	} {
		if err := removeManaged(target.dir, target.pattern); err != nil {
			return v
		}
	}
	next := v
	next.CleanupPending = false
	next.PreviousSpace = ""
	if s.persistStorage(next) != nil {
		return v
	}
	s.storage.current.Store(&next)
	if s.storage.previousLock != nil {
		s.storage.previousLock.Close()
		s.storage.previousLock = nil
	}
	return next
}
func removeManaged(dir, pattern string) error {
	st, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return errors.New("managed directory must not be a symlink")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	match := regexp.MustCompile(pattern)
	for _, e := range entries {
		if match.MatchString(e.Name()) {
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}
