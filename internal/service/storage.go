package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/gitstore"
)

type StorageValues struct {
	BaseDirectory string `json:"base_directory"`
}
type StorageSettings struct {
	Revision           int64         `json:"revision"`
	Values             StorageValues `json:"values"`
	EffectiveDirectory string        `json:"effective_directory"`
	ID                 string        `json:"storage_id"`
	UpdatedAt          time.Time     `json:"updated_at"`
	CleanupPending     bool          `json:"cleanup_pending"`
	PreviousSpace      string        `json:"previous_space,omitempty"`
}
type storageSlot struct {
	current      atomic.Pointer[StorageSettings]
	lock         *os.File
	previousLock *os.File
}

func (s *Service) StorageSettings() StorageSettings {
	if v := s.storage.current.Load(); v != nil {
		return *v
	}
	return StorageSettings{Values: StorageValues{BaseDirectory: s.Root}, EffectiveDirectory: filepath.Join(s.Root, "space")}
}
func (s *Service) gitRoot() string {
	if v := s.storage.current.Load(); v != nil {
		return v.EffectiveDirectory
	}
	return s.Root
}
func (s *Service) storageID() string {
	if v := s.storage.current.Load(); v != nil {
		return v.ID
	}
	return ""
}

// InitStorage runs before serving requests. The settings file belongs to this
// instance, not the shared database: a directory is meaningful on one machine.
func (s *Service) InitStorage() error {
	file := filepath.Join(s.Root, "storage.json")
	raw, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		_, err = s.ChangeStorage(context.Background(), 0, s.Root, true)
		return err
	}
	if err != nil {
		return err
	}
	var v StorageSettings
	if contract.DecodeObject(raw, &v) != nil || v.Revision < 1 || v.ID == "" || v.EffectiveDirectory != filepath.Join(v.Values.BaseDirectory, "space") {
		return errors.New("invalid storage configuration")
	}
	base, root, lock, err := prepareDirectory(v.Values.BaseDirectory, true)
	if err != nil {
		return err
	}
	if base != v.Values.BaseDirectory || root != v.EffectiveDirectory {
		lock.Close()
		return errors.New("storage path changed")
	}
	g, err := gitstore.New(root)
	if err != nil {
		lock.Close()
		return err
	}
	s.Git = g
	s.Builder.GitRoot = root
	s.storage.lock = lock
	s.storage.current.Store(&v)
	if v.CleanupPending {
		s.finishStorageCleanup(v)
	}
	return nil
}

// ChangeStorage starts afresh and removes only service-managed old data.
// Holding preparation excludes fetch/index/cleanup; TryLock avoids waiting for
// a long build or blocking queries while a settings request is queued.
func (s *Service) ChangeStorage(ctx context.Context, revision int64, base string, create bool) (StorageSettings, error) {
	if !s.preparation.TryLock() {
		return StorageSettings{}, contract.Fail("PREPARATION_ACTIVE", "Wait for repository preparation to finish before changing storage.", 409)
	}
	defer s.preparation.Unlock()
	if !s.mu.TryLock() {
		return StorageSettings{}, contract.Fail("STORAGE_BUSY", "Queries or management operations are active; retry changing storage shortly.", 409)
	}
	locked := true
	defer func() {
		if locked {
			s.mu.Unlock()
		}
	}()
	old := s.StorageSettings()
	if old.Revision != revision {
		return old, contract.Fail("SETTINGS_REVISION_CONFLICT", "Storage configuration changed; reload before saving.", 412)
	}
	if revision >= 9007199254740991 {
		return old, contract.Fail("INVALID_SETTINGS_REVISION", "Storage revision limit reached.", 400)
	}
	base = filepath.Clean(strings.TrimSpace(base))
	if old.Revision > 0 && base == old.Values.BaseDirectory {
		s.mu.Unlock()
		locked = false
		return s.finishStorageCleanup(old), nil
	}
	if old.CleanupPending {
		return old, contract.Fail("STORAGE_CLEANUP_PENDING", "Retry saving the current directory to finish its cleanup first.", 409)
	}
	base, root, lock, err := prepareDirectory(base, create)
	if err != nil {
		return old, err
	}
	keep := false
	defer func() {
		if !keep {
			lock.Close()
		}
	}()
	if old.Revision > 0 && (within(old.EffectiveDirectory, root) || within(root, old.EffectiveDirectory)) {
		return old, contract.Fail("STORAGE_PATH_OVERLAP", "New and previous space directories must not overlap.", 400)
	}
	canonicalRoot, _ := filepath.EvalSymlinks(s.Root)
	if within(filepath.Join(canonicalRoot, "index"), root) || within(filepath.Join(canonicalRoot, "manifests"), root) || within(filepath.Join(canonicalRoot, "repos"), root) {
		return old, contract.Fail("STORAGE_PATH_OVERLAP", "Choose a directory outside managed indexes and repositories.", 400)
	}
	g, err := gitstore.New(root)
	if err != nil {
		return old, err
	}
	next := StorageSettings{Revision: revision + 1, Values: StorageValues{base}, EffectiveDirectory: root, ID: control.NewID(), UpdatedAt: time.Now().UTC()}
	if old.Revision > 0 {
		next.CleanupPending = true
		next.PreviousSpace = old.EffectiveDirectory
	}
	if err = ctx.Err(); err != nil {
		return old, err
	}
	if err = s.persistStorage(next); err != nil {
		return old, contract.Fail("STORAGE_SAVE_FAILED", "Storage configuration was not saved; the previous directory remains active.", 503)
	}

	s.Git = g
	s.Builder.GitRoot = root
	previous := s.storage.lock
	s.storage.lock = lock
	s.storage.current.Store(&next)
	keep = true
	s.storage.previousLock = previous
	s.mu.Unlock()
	locked = false
	return s.finishStorageCleanup(next), nil
}

func prepareDirectory(base string, create bool) (string, string, *os.File, error) {
	fail := func(code, msg string, status int) (string, string, *os.File, error) {
		return "", "", nil, contract.Fail(code, msg, status)
	}
	if !filepath.IsAbs(base) || filepath.Clean(base) == "/" || len(base) > 2048 || strings.ContainsAny(base, "\x00\r\n") {
		return fail("INVALID_STORAGE_PATH", "Enter an absolute non-root server directory.", 400)
	}
	root := filepath.Join(base, "space")
	if _, err := os.Stat(root); os.IsNotExist(err) && !create {
		quoted := "'" + strings.ReplaceAll(root, "'", "'\\''") + "'"
		return fail("DIRECTORY_CREATE_REQUIRED", fmt.Sprintf("Directory does not exist. Confirm creation (equivalent to mkdir -p %s).", quoted), 409)
	}
	if create {
		if err := os.MkdirAll(root, 0750); err != nil {
			return fail("DIRECTORY_CREATE_FAILED", "Cannot create directory. Check server path and service-account write permissions; configuration was not saved.", 400)
		}
	}
	st, err := os.Lstat(root)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return fail("INVALID_STORAGE_PATH", "space must be an existing directory, not a symlink or file.", 400)
	}
	canonical, err := filepath.EvalSymlinks(base)
	if err != nil {
		return fail("INVALID_STORAGE_PATH", "Cannot resolve server directory.", 400)
	}
	root = filepath.Join(canonical, "space")
	probe, err := os.CreateTemp(root, ".write-check-")
	if err != nil {
		return fail("DIRECTORY_NOT_WRITABLE", "Service account cannot write this directory; configuration was not saved.", 400)
	}
	probe.Close()
	os.Remove(probe.Name())
	fd, err := syscall.Open(filepath.Join(root, ".sourcegraph-space.lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return fail("DIRECTORY_NOT_WRITABLE", "Cannot acquire directory ownership.", 400)
	}
	lock := os.NewFile(uintptr(fd), "storage lock")
	if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return fail("DIRECTORY_IN_USE", "Another service already owns this space directory.", 409)
	}
	return canonical, root, lock, nil
}

func (s *Service) markStorage(r control.Repository) control.Repository {
	r.SyncIntervalMinutes = r.SyncInterval()
	if r.DiskUsage != nil && r.DiskUsage.StorageID != s.storageID() {
		r.DiskUsage = nil
	}
	if id := s.storageID(); id != "" && r.StorageID != id && (!r.ObservedAt.IsZero() || len(r.Versions) > 0 || len(r.Scopes) > 0) {
		r.StorageMissing = true
		r.Versions = append([]control.Version(nil), r.Versions...)
		for i := range r.Versions {
			r.Versions[i].Generation = 0
			r.Versions[i].Shards = 0
			r.Versions[i].Files = 0
		}
		scopes := make(map[string]control.ScopeState, len(r.Scopes))
		for name, state := range r.Scopes {
			state.Versions = append([]control.Version(nil), state.Versions...)
			for i := range state.Versions {
				state.Versions[i].Generation = 0
				state.Versions[i].Shards = 0
				state.Versions[i].Files = 0
			}
			scopes[name] = state
		}
		r.Scopes = scopes
	}
	return r
}
