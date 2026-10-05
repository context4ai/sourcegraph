// Package service is the common execution boundary for HTTP, MCP and workers.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/context4ai/sourcegraph/internal/codecredentials"
	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/gitstore"
	"github.com/context4ai/sourcegraph/internal/identity"
	"github.com/context4ai/sourcegraph/internal/indexer"
	"github.com/context4ai/sourcegraph/internal/querylog"
	"github.com/context4ai/sourcegraph/internal/sqlstore"
	"github.com/context4ai/sourcegraph/internal/wasmplugin"
	"github.com/context4ai/sourcegraph/internal/zoektclient"
	"go.mongodb.org/mongo-driver/mongo"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Store interface {
	control.Store
	NextGeneration(context.Context) (uint32, error)
}
type Service struct {
	linkManifests       linkManifestCache
	Plugins             *wasmplugin.Host
	pluginSnapshots     sync.RWMutex
	pluginCache         pluginCacheState
	QueryLogs           *querylog.Log
	Credentials         *codecredentials.Manager
	diskWake            chan struct{}
	diskWakeOnce        sync.Once
	storage             storageSlot
	gcCursor            int
	metrics             Metrics
	preparation         sync.RWMutex
	repositoryWorkMu    sync.Mutex
	repositoryWork      map[string]bool
	jobCancels          map[string]context.CancelFunc
	DB                  Store
	Git                 *gitstore.Store
	Engine              *zoektclient.Client
	Builder             indexer.Builder
	Root, Helper, Token string
	MinFree             uint64
	mu                  sync.RWMutex
	boot                string
	lock                *os.File
}

func New(db Store, root, helper, token string) (*Service, error) {

	g, e := gitstore.New(root)
	if e != nil {
		return nil, e
	}
	c, e := zoektclient.New("http://127.0.0.1:6070")
	if e != nil {
		return nil, e
	}
	fd, e := syscall.Open(filepath.Join(root, "instance.lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		c.Close()
		return nil, e
	}
	f := os.NewFile(uintptr(fd), "instance.lock")
	if e = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		c.Close()
		return nil, errors.New("data directory already has a preparation owner")
	}
	// The exclusive root lock guarantees no live builder owns these stages.
	stages, err := filepath.Glob(filepath.Join(root, ".index-stage-*"))
	if err == nil {
		for _, stage := range stages {
			if err = os.RemoveAll(stage); err != nil {
				break
			}
		}
	}
	if err != nil {
		f.Close()
		c.Close()
		return nil, err
	}
	var credentialStore codecredentials.Store = &codecredentials.FileStore{Path: filepath.Join(root, "code-credentials.json")}
	if mongoDB, ok := db.(interface{ AuthDatabase() *mongo.Database }); ok {
		credentialStore = codecredentials.MongoStore{Collection: mongoDB.AuthDatabase().Collection("sourcegraph_code_credentials")}
	}
	if sqlDB, ok := db.(interface{ DocumentDB() *sqlstore.DB }); ok {
		credentialStore = codecredentials.SQLStore{DB: sqlDB.DocumentDB()}
	}
	credentials, err := codecredentials.New(root, token, credentialStore)
	if err != nil {
		f.Close()
		c.Close()
		return nil, err
	}
	var logStore querylog.Store = &querylog.FileStore{Path: filepath.Join(root, "query-errors.json")}
	if mongoDB, ok := db.(interface{ AuthDatabase() *mongo.Database }); ok {
		logStore = querylog.MongoStore{Collection: mongoDB.AuthDatabase().Collection("sourcegraph_query_errors")}
	}
	if sqlDB, ok := db.(interface{ DocumentDB() *sqlstore.DB }); ok {
		logStore = querylog.SQLStore{DB: sqlDB.DocumentDB()}
	}
	pluginConfig, err := wasmplugin.FromEnv()
	if err != nil {
		f.Close()
		c.Close()
		return nil, err
	}
	return &Service{Plugins: wasmplugin.New(pluginConfig), QueryLogs: querylog.New(logStore), Credentials: credentials, metrics: Metrics{queue: make(chan observation, 4096)}, DB: db, Git: g, Engine: c, Builder: indexer.Builder{Root: root, MinFree: 10 << 30}, Root: root, Helper: helper, Token: token, MinFree: 10 << 30, boot: control.NewID(), lock: f}, nil
}
func (s *Service) Close() {
	if s.Plugins != nil {
		s.Plugins.Close()
	}
	if s.storage.previousLock != nil {
		s.storage.previousLock.Close()
	}
	if s.storage.lock != nil {
		s.storage.lock.Close()
	}
	s.Engine.Close()
	s.lock.Close()
}
func digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func requestKey(actor, key string) (string, error) {
	if len(key) < 8 || len(key) > 128 || strings.ContainsAny(key, "\r\n\x00") {
		return "", contract.Fail("INVALID_IDEMPOTENCY_KEY", "Supply an idempotency key of 8..128 characters.", 400)
	}
	return digest([]string{actor, key}), nil
}
func (s *Service) Register(ctx context.Context, name string, p control.Policy, key string) (control.Repository, error) {
	return s.RegisterWithInterval(ctx, name, p, control.DefaultSyncIntervalMinutes, key)
}

func (s *Service) RegisterWithInterval(ctx context.Context, name string, p control.Policy, interval int, key string) (control.Repository, error) {
	return s.RegisterWithOptions(ctx, name, p, interval, "full", nil, key)
}

func (s *Service) RegisterWithOptions(ctx context.Context, name string, p control.Policy, interval int, mode string, groups []control.PathGroup, key string) (control.Repository, error) {
	return s.RegisterWithVisibility(ctx, name, p, interval, mode, groups, nil, key)
}

func (s *Service) RegisterWithVisibility(ctx context.Context, name string, p control.Policy, interval int, mode string, groups []control.PathGroup, publicRead *bool, key string) (control.Repository, error) {
	return s.RegisterWithProvider(ctx, name, p, interval, mode, groups, publicRead, "github", key)
}

func (s *Service) RegisterWithProvider(ctx context.Context, name string, p control.Policy, interval int, mode string, groups []control.PathGroup, publicRead *bool, provider, key string) (control.Repository, error) {
	if provider == "" {
		provider = "github"
	}
	if provider != "github" && provider != "git" {
		return control.Repository{}, contract.Fail("INVALID_CODE_PROVIDER", "Choose github or git.", 400)
	}
	mode, groups, configErr := control.NormalizeIndexConfig(mode, groups)
	if configErr != nil {
		return control.Repository{}, configErr
	}
	if e := control.ValidateSyncInterval(interval); e != nil {
		return control.Repository{}, e
	}
	registrationDigest := digest(struct {
		Policy              control.Policy
		SyncIntervalMinutes int
	}{p, interval})
	if mode != control.IndexModeFull {
		registrationDigest = digest([]any{p, interval, mode, groups})
	}
	if publicRead != nil && !*publicRead {
		registrationDigest = digest([]any{registrationDigest, false})
	}
	if provider != "github" {
		registrationDigest = digest([]any{registrationDigest, provider})
	}
	if e := gitstore.ValidateRepo(name); e != nil {
		return control.Repository{}, e
	}
	if e := p.Validate(); e != nil {
		return control.Repository{}, e
	}
	k, e := requestKey(identity.Actor(ctx)+":register", key)
	if e != nil {
		return control.Repository{}, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, e := s.DB.Get(ctx, name); e == nil {
		for _, r := range existing.Receipts {
			if r.Key == k && (r.Digest == registrationDigest || ((publicRead == nil || *publicRead) && mode == control.IndexModeFull && existing.EffectiveIndexMode() == mode && interval == control.DefaultSyncIntervalMinutes && existing.SyncInterval() == interval && r.Digest == digest(p))) {
				return projectRepository(s.markStorage(existing)), nil
			}
		}
		return existing, contract.Fail("REPOSITORY_EXISTS", "Repository already exists; read its revision before updating.", 409)
	} else {
		var ce *contract.Error
		if !errors.As(e, &ce) || ce.Code != "REPOSITORY_NOT_FOUND" {
			return control.Repository{}, e
		}
	}
	r := control.Repository{CodeProvider: provider, CreatorID: identity.UserID(ctx), PublicRead: publicRead, IndexMode: mode, PathGroups: groups, ID: control.ID(name), Name: name, Revision: 1, PolicyRevision: 1, Enabled: true, Policy: p, SyncIntervalMinutes: interval, Updated: time.Now().UTC(), Heads: map[string]string{}, Versions: []control.Version{}}
	for _, group := range r.WorkGroups() {
		groupKey := k
		if group != "" {
			groupKey = digest([]string{k, group})
		}
		_, e = r.QueueScoped("sync", "", group, groupKey, registrationDigest, identity.Actor(ctx), control.SourceManual, r.Updated)
		if e != nil {
			return r, e
		}
	}
	// The registration receipt is independent of group jobs and preserves request replay.
	if mode == control.IndexModeMonorepo {
		r.Receipts = append(r.Receipts, control.Receipt{Key: k, Digest: registrationDigest, JobID: r.Jobs[0].ID, Expires: r.Updated.Add(24 * time.Hour)})
	}
	e = s.DB.Create(ctx, r)
	return projectRepository(s.markStorage(r)), e
}
func (s *Service) Queue(ctx context.Context, name, kind, commit, key string) (control.Job, error) {
	return s.QueueGroup(ctx, name, kind, commit, "", key)
}

type Summary struct {
	CodeProvider             string              `json:"code_provider"`
	NextSyncAt               *time.Time          `json:"next_sync_at,omitempty"`
	DiskUsage                *control.DiskUsage  `json:"disk_usage,omitempty"`
	PublicRead               bool                `json:"public_read"`
	DefaultBranch            string              `json:"default_branch,omitempty"`
	IndexMode                string              `json:"index_mode"`
	PathGroups               []control.PathGroup `json:"path_groups,omitempty"`
	LastIndexedAt            time.Time           `json:"last_indexed_at,omitempty,omitzero"`
	LastIndexDurationSeconds float64             `json:"last_index_duration_seconds,omitempty"`
	SyncIntervalMinutes      int                 `json:"sync_interval_minutes"`
	StorageMissing           bool                `json:"storage_missing"`
	ID                       string              `json:"repo_id"`
	Name                     string              `json:"name"`
	Enabled                  bool                `json:"enabled"`
	Deleted                  bool                `json:"deleted"`
	Policy                   control.Policy      `json:"policy"`
	Heads                    map[string]string   `json:"heads"`
	VersionCount             int                 `json:"version_count"`
	IndexedCount             int                 `json:"published_index_count"`
	ObservedAt               time.Time           `json:"observed_at"`
}

func (s *Service) Catalog(ctx context.Context, after string) ([]Summary, error) {
	rows, e := s.catalogRows(ctx, after)
	if e != nil {
		return nil, e
	}
	out := []Summary{}
	for _, r := range rows {
		r = projectRepository(s.markStorage(r))
		n := 0
		for _, v := range r.Versions {
			if v.Generation != 0 {
				n++
			}
		}
		out = append(out, Summary{CodeProvider: r.CodeProvider, NextSyncAt: s.nextSyncTime(r), DiskUsage: r.DiskUsage, PublicRead: r.AllowsAnonymous(), DefaultBranch: r.DefaultBranch, IndexMode: r.EffectiveIndexMode(), PathGroups: r.PathGroups, LastIndexedAt: r.LastIndexedAt, LastIndexDurationSeconds: r.LastIndexDurationSeconds, SyncIntervalMinutes: r.SyncInterval(), StorageMissing: r.StorageMissing, ID: r.ID, Name: r.Name, Enabled: r.Enabled, Deleted: r.Deleted, Policy: r.Policy, Heads: r.Heads, VersionCount: len(r.Versions), IndexedCount: n, ObservedAt: r.ObservedAt})
	}
	return out, nil
}

func (s *Service) Repo(ctx context.Context, name string) (control.Repository, error) {
	if e := gitstore.ValidateRepo(name); e != nil {
		return control.Repository{}, e
	}
	r, e := s.DB.Get(ctx, name)
	if e == nil && identity.IsAnonymous(ctx) && !r.AllowsAnonymous() {
		return control.Repository{}, contract.Fail("REPOSITORY_NOT_FOUND", "Repository is not available.", 404)
	}
	return projectRepository(s.markStorage(r)), e
}
func eligible(r control.Repository, revision string) (control.Version, error) {
	if r.StorageMissing {
		return control.Version{}, contract.Fail("REPOSITORY_STORAGE_MISSING", "Storage directory changed; synchronize the repository and rebuild its index. Synchronize and rebuild local files and indexes.", 409)
	}
	if !r.Enabled || r.Deleted {
		return control.Version{}, contract.Fail("REPOSITORY_DISABLED", "Repository is disabled.", 403)
	}
	if revision == "" && r.Policy.DefaultBranch {
		if r.DefaultBranch == "" {
			return control.Version{}, contract.Fail("DEFAULT_BRANCH_NOT_OBSERVED", "Synchronize the repository to discover its default branch.", 409)
		}
		revision = r.DefaultBranch
	}
	if revision == "" {
		var candidates []string
		for _, branch := range r.Policy.Branches {
			if branch == "master" || branch == "main" {
				candidates = append(candidates, branch)
			}
		}
		if len(candidates) != 1 {
			return control.Version{}, contract.Fail("DEFAULT_BRANCH_UNDETERMINED", "Specify revision; cannot uniquely identify master/main among configured branches: "+strings.Join(r.Policy.Branches, ", ")+". Inspect availability for observed heads.", 409)
		}
		revision = candidates[0]
		if r.Heads[revision] == "" {
			return control.Version{}, contract.Fail("DEFAULT_BRANCH_NOT_OBSERVED", "Default branch "+revision+" has no synchronized head yet; inspect availability or request sync.", 409)
		}
	}
	branch := strings.TrimPrefix(revision, "refs/heads/")
	sha, observed := r.Heads[branch]
	monitored := false
	for _, name := range monitoredBranches(r) {
		monitored = monitored || branch == name
	}
	// Branch identity takes precedence over SHA shape, including a monitored
	// hexadecimal branch whose first synchronization has not completed yet.
	if observed || monitored {
		if sha == "" {
			return control.Version{}, contract.Fail("BRANCH_NOT_OBSERVED", "The monitored branch has no synchronized head; inspect availability for its observed state.", 409)
		}
		v, ok := r.Version(sha)
		if !ok {
			return v, contract.Fail("REVISION_NOT_ELIGIBLE", "The observed branch head is outside the current retention window; inspect availability without substituting another commit.", 404)
		}
		return v, nil
	}
	if prepareSHA.MatchString(revision) {
		v, ok := r.Version(revision)
		if !ok {
			return v, contract.Fail("REVISION_NOT_ELIGIBLE", "The full commit SHA is outside the observed retention window; retain this baseline and use an independently authorized Git source if needed.", 404)
		}
		return v, nil
	}
	// refs/heads/ explicitly names a branch, even when its short name is hex.
	if revision == branch && len(revision) < 40 && isHexRevision(revision) {
		return control.Version{}, contract.Fail("SHORT_SHA_UNSUPPORTED", "Abbreviated commit SHAs are not supported; obtain the original full 40-character lowercase SHA from trusted source metadata. Do not guess or switch branches.", 400)
	}
	if (revision == branch && isHexRevision(revision)) || gitstore.ValidateRevision(revision) != nil || (strings.HasPrefix(revision, "refs/") && revision == branch) {
		return control.Version{}, contract.Fail("INVALID_REVISION", "Use a monitored branch or a full 40-character lowercase commit SHA; HEAD, tags and arbitrary refs are not supported.", 400)
	}
	return control.Version{}, contract.Fail("BRANCH_NOT_MONITORED", "The requested branch is not monitored; inspect availability for configured branches without substituting a different baseline.", 404)
}

func isHexRevision(revision string) bool {
	if revision == "" {
		return false
	}
	for _, c := range revision {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
func (s *Service) Resolve(ctx context.Context, name, revision string) (control.Version, error) {
	r, e := s.Repo(ctx, name)
	if e != nil {
		return control.Version{}, e
	}
	return eligible(r, revision)
}
