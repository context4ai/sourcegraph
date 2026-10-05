package sitesettings

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/context4ai/sourcegraph/internal/contract"
)

type Service struct {
	store   Store
	mu      sync.Mutex
	current atomic.Pointer[Snapshot]
}

// New loads a complete validated snapshot before exposing the service. The seed
// affects absent documents only; a failed initial load fails initialization.
func New(ctx context.Context, store Store, publicReadSeed bool) (*Service, error) {
	if store == nil {
		return nil, errors.New("site settings store is required")
	}
	s := &Service{store: store}
	now := time.Now().UTC()
	records := []Record{
		{Name: RepositoryDefaultsName, Revision: 1, Values: map[string]any{"retention_mode": "days", "retention_days": 30, "retention_count": 20}, UpdatedAt: now, UpdatedBy: "system:bootstrap"},
		{Name: AccessName, Revision: 1, Values: map[string]any{"public_read": publicReadSeed, "allow_registration": false, "registration_notice_zh": DefaultNoticeZH, "registration_notice_en": DefaultNoticeEN}, UpdatedAt: now, UpdatedBy: "system:bootstrap"},
	}
	for _, r := range records {
		if err := store.Ensure(ctx, r); err != nil {
			return nil, errors.New("site settings initialization failed")
		}
	}
	if err := s.Refresh(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// Snapshot returns a value copy; callers cannot mutate the cached policy.
func (s *Service) Snapshot() Snapshot {
	if v := s.current.Load(); v != nil {
		return *v
	}
	return Snapshot{}
}
func (s *Service) PublicRead() bool { return s.Snapshot().Access.Values.PublicRead }

// Refresh never replaces a valid cache with an error, corrupt record or older
// revision. Query authorization reads the atomic cache without database I/O.
func (s *Service) Refresh(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.store.Load(ctx)
	if err != nil {
		return errors.New("site settings refresh failed")
	}
	next, err := snapshot(rows)
	if err != nil {
		return err
	}
	if old := s.current.Load(); old != nil && (next.RepositoryDefaults.Revision < old.RepositoryDefaults.Revision || next.Access.Revision < old.Access.Revision) {
		return errors.New("site settings revision moved backwards")
	}
	s.current.Store(&next)
	return nil
}

func (s *Service) RunRefresh(ctx context.Context, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			budget, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := s.Refresh(budget)
			cancel()
			if err != nil && ctx.Err() == nil {
				log.Warn("site settings refresh failed; retaining last known configuration")
			}
		}
	}
}

// Update uses the caller's exact revision. A conflicting or uncertain write must
// be reloaded by the caller; no automatic retry can overwrite another admin.
func (s *Service) Update(ctx context.Context, section string, revision int64, data []byte, actor string) (Record, error) {
	if !validSection(section) {
		return Record{}, contract.Fail("SETTINGS_SECTION_NOT_FOUND", "Unknown settings section.", 404)
	}
	if revision < 1 || revision >= maxRevision {
		return Record{}, contract.Fail("INVALID_SETTINGS_REVISION", "Provide the current positive settings revision.", 400)
	}
	if strings.TrimSpace(actor) == "" || len(actor) > 512 {
		return Record{}, contract.Fail("SETTINGS_ACTOR_REQUIRED", "An authenticated administrator actor is required.", 403)
	}
	values, err := decodeValues(section, data)
	if err != nil {
		return Record{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.Snapshot()
	if next.Access.Revision == 0 || next.RepositoryDefaults.Revision == 0 {
		return Record{}, contract.Fail("SETTINGS_UNAVAILABLE", "Site settings are not initialized.", 503)
	}
	knownRevision := next.Access.Revision
	if section == RepositoryDefaultsName {
		knownRevision = next.RepositoryDefaults.Revision
	}
	if revision < knownRevision {
		return Record{}, contract.Fail("SETTINGS_REVISION_CONFLICT", "Settings changed; reload this section before saving.", 412)
	}
	r := Record{Name: section, Revision: revision + 1, Values: values, UpdatedAt: time.Now().UTC(), UpdatedBy: actor}
	if err = s.store.Replace(ctx, r, revision); err != nil {
		if errors.Is(err, ErrConflict) {
			return Record{}, contract.Fail("SETTINGS_REVISION_CONFLICT", "Settings changed; reload this section and review your values before saving again.", 412)
		}
		return Record{}, contract.Fail("SETTINGS_UNAVAILABLE", "Save result could not be confirmed. Reload settings before retrying.", 503)
	}
	meta := Metadata{r.Revision, r.UpdatedAt, r.UpdatedBy}
	if section == RepositoryDefaultsName {
		next.RepositoryDefaults = RepositorySection{Metadata: meta, Values: RepositoryDefaults{values["retention_mode"].(string), values["retention_days"].(int), values["retention_count"].(int)}}
	} else {
		next.Access = AccessSection{Metadata: meta, Values: Access{PublicRead: values["public_read"].(bool), AllowRegistration: values["allow_registration"].(bool), RegistrationNoticeZH: values["registration_notice_zh"].(string), RegistrationNoticeEN: values["registration_notice_en"].(string)}}
	}
	s.current.Store(&next)
	return r, nil
}
