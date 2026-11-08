package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/gitstore"
)

// Ref discovery limits; see docs/ref/mcp-protocol.md "refs".
const (
	refCatalogTTL          = 5 * time.Minute
	refCatalogKindLimit    = 200_000
	refCatalogMemoryRefs   = 1_000_000
	refRefreshBytes        = 256 << 20
	refResolveBytes        = 16 << 20
	refDownloadTotalBytes  = 4 << 30
	refRefreshConcurrency  = 4
	refLookupConcurrency   = 8
	refLookupPerRepository = 2
	refLookupTTL           = 30 * time.Second
	refLookupEntries       = 10_000
	refLookupPending       = 64
	refLookupTimeout       = 2 * time.Minute
	refBackoffInitial      = 10 * time.Second
	refBackoffMax          = 5 * time.Minute
	refTagDepth            = 8
)

// RefEntry is one branch or tag. Commit is set only when the ref was verified to
// end at a commit; otherwise TargetType names what it ends at.
type RefEntry struct {
	Name          string     `json:"name"`
	Ref           string     `json:"ref"`
	Kind          string     `json:"kind"`
	Commit        string     `json:"commit,omitempty"`
	Object        string     `json:"object,omitempty"`
	TargetType    string     `json:"target_type,omitempty"`
	Annotated     bool       `json:"annotated,omitempty"`
	CreatedAt     *time.Time `json:"created_at,omitempty"`
	CommitterTime *time.Time `json:"committer_time,omitempty"`
	Author        string     `json:"author,omitempty"`
	Title         string     `json:"title,omitempty"`
	Monitored     bool       `json:"monitored"`
	Indexed       bool       `json:"indexed"`
}

type RefCatalogInfo struct {
	State       string    `json:"state"`
	Reason      string    `json:"reason,omitempty"`
	RefreshedAt time.Time `json:"refreshed_at"`
	Total       int       `json:"total"`
	Truncated   bool      `json:"truncated"`
}

type RefsQuery struct {
	Kind, Query, Author, Title, Since, Until, Sort, After string
	First                                                 int
}

type RefsPage struct {
	Repo       string         `json:"repo"`
	Catalog    RefCatalogInfo `json:"catalog"`
	Sort       string         `json:"sort"`
	Refs       []RefEntry     `json:"refs"`
	NextCursor string         `json:"next_cursor,omitempty"`
	HasMore    bool           `json:"has_more"`
	Hint       string         `json:"hint,omitempty"`
}

// ResolveResult extends the resolved version with how it was found.
type ResolveResult struct {
	control.Version
	Ref       string     `json:"ref,omitempty"`
	Kind      string     `json:"kind"`
	Source    string     `json:"source"`
	Monitored bool       `json:"monitored"`
	CreatedAt *time.Time `json:"created_at,omitempty"`
	Author    string     `json:"author,omitempty"`
	Title     string     `json:"title,omitempty"`
	Hint      string     `json:"hint,omitempty"`
}

// refCatalog is immutable once published; readers never see it change.
type refCatalog struct {
	entries     []RefEntry // created desc for ready catalogs, name order otherwise
	byName      []int
	byRef       map[string]int
	state       string
	reason      string
	refreshedAt time.Time
	duration    time.Duration
	truncated   bool
}

func (c *refCatalog) fresh(now time.Time) bool {
	ttl := 3 * c.duration
	if ttl < refCatalogTTL {
		ttl = refCatalogTTL
	}
	return now.Before(c.refreshedAt.Add(ttl))
}

type refFailure struct {
	err   error
	count int
	until time.Time
}

type refLookup struct {
	entries []RefEntry
	err     error
	expires time.Time
	used    time.Time
	done    chan struct{}
}

// refManager owns ref discovery state. Every catalog write for a repository runs
// in its single refresh; published catalogs are replaced, never modified.
type refManager struct {
	mu         sync.Mutex
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	catalogs   map[string]*refCatalog
	used       map[string]time.Time
	refreshes  map[string]chan struct{}
	failures   map[string]refFailure
	lookups    map[string]*refLookup
	repoSlots  map[string]chan struct{}
	downloads  map[string]int64
	refreshes4 chan struct{}
	lookups8   chan struct{}
}

func (m *refManager) init() {
	if m.catalogs != nil {
		return
	}
	m.ctx, m.cancel = context.WithCancel(context.Background())
	m.catalogs, m.used, m.refreshes = map[string]*refCatalog{}, map[string]time.Time{}, map[string]chan struct{}{}
	m.failures, m.lookups, m.repoSlots, m.downloads = map[string]refFailure{}, map[string]*refLookup{}, map[string]chan struct{}{}, map[string]int64{}
	m.refreshes4, m.lookups8 = make(chan struct{}, refRefreshConcurrency), make(chan struct{}, refLookupConcurrency)
}

// close cancels background ref work and waits for it.
func (m *refManager) close() {
	m.mu.Lock()
	cancel := m.cancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.wg.Wait()
}

func (m *refManager) failed(name string, err error) refFailure {
	f := m.failures[name]
	f.count++
	delay := refBackoffInitial << (f.count - 1)
	if delay > refBackoffMax || delay <= 0 {
		delay = refBackoffMax
	}
	f.err, f.until = err, time.Now().Add(delay)
	m.failures[name] = f
	return f
}

func retrySeconds(until time.Time) int {
	seconds := int(time.Until(until).Seconds()) + 1
	if seconds < 1 {
		seconds = 1
	}
	return seconds
}

// requestWait is how long a request may wait for ref work: its remaining
// deadline minus a response margin. Zero means answer "retry" immediately.
func requestWait(ctx context.Context) time.Duration {
	wait := 28 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		wait = time.Until(deadline) - 2*time.Second
	}
	if wait < time.Second {
		return 0
	}
	return wait
}

func refsRoot(root string) string { return filepath.Join(root, "refcache", "tmp") }

// scratch creates a download directory that is always removed by the returned
// cleanup; a process restart removes leftovers (see New).
func (s *Service) refScratch(ctx context.Context) (string, func(), error) {
	parent := refsRoot(s.Root)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp(parent, "refs-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() {
		s.refState.mu.Lock()
		delete(s.refState.downloads, dir)
		s.refState.mu.Unlock()
		_ = os.RemoveAll(dir)
	}
	if err = s.Git.InitScratch(ctx, dir); err != nil {
		cleanup()
		return "", nil, err
	}
	return dir, cleanup, nil
}

var (
	errRefDownloadTooLarge = contract.Fail("REF_DOWNLOAD_LIMIT", "Ref objects exceed the download limit.", 507)
	errRefDownloadsBusy    = contract.FailRetryAfter("REMOTE_UNAVAILABLE", "Ref downloads are using their whole disk allowance; retry shortly.", 503, 30)
)

// downloadGuard bounds one scratch download by size, the shared download
// allowance and the instance free-space reserve. Fetches call it every second.
func (s *Service) downloadGuard(dir string, max int64) func() error {
	return func() error {
		size := gitstore.DirectorySize(dir)
		s.refState.mu.Lock()
		s.refState.downloads[dir] = size
		var total int64
		for _, bytes := range s.refState.downloads {
			total += bytes
		}
		s.refState.mu.Unlock()
		if size > max {
			return errRefDownloadTooLarge
		}
		if total > refDownloadTotalBytes {
			return errRefDownloadsBusy
		}
		return gitstore.CheckFreeAt(s.Root, s.MinFree)
	}
}

func refKind(ref string) (string, string) {
	if name, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
		return "branch", name
	}
	return "tag", strings.TrimPrefix(ref, "refs/tags/")
}

// refEntries turns advertised refs plus downloaded objects into entries. A ref
// is reported as a commit only when the commit object itself was read.
func refEntries(refs []gitstore.RemoteRef, objects map[string]gitstore.RefObject, excluded map[string]bool) []RefEntry {
	out := make([]RefEntry, 0, len(refs))
	for _, ref := range refs {
		kind, name := refKind(ref.Name)
		e := RefEntry{Name: name, Ref: ref.Name, Kind: kind}
		o, ok := objects[ref.Object]
		switch {
		case excluded[ref.Name]:
			// A filtered transfer omits only trees and blobs.
			e.Object, e.TargetType = ref.Object, "tree_or_blob"
			if ref.Peeled != "" {
				e.Object, e.Annotated = ref.Peeled, true
			}
		case objects == nil:
			e.Object = ref.Object
			if ref.Peeled != "" {
				e.Object, e.Annotated = ref.Peeled, true
			}
		case ok && o.Type == "commit":
			e.Commit = ref.Object
			e.CreatedAt, e.CommitterTime = timePtr(o.Time), timePtr(o.Time)
			e.Author, e.Title = o.Author, o.Title
		case ok && o.Type == "tag":
			e.Annotated = true
			e.CreatedAt, e.Author, e.Title = timePtr(o.Time), o.Author, o.Title
			target, targetType := o.Target, o.TargetType
			for depth := 0; targetType == "tag" && depth < refTagDepth; depth++ {
				next, ok := objects[target]
				if !ok || next.Type != "tag" {
					break
				}
				target, targetType = next.Target, next.TargetType
			}
			if commit, ok := objects[target]; ok && targetType == "commit" && commit.Type == "commit" {
				e.Commit, e.CommitterTime = target, timePtr(commit.Time)
			} else {
				e.Object, e.TargetType = target, targetType
				if targetType == "commit" || targetType == "tag" {
					e.TargetType = "unknown"
				}
			}
		default:
			e.Object, e.TargetType = ref.Object, "unknown"
			if ok && o.Type != "missing" {
				e.TargetType = o.Type
			}
		}
		out = append(out, e)
	}
	return out
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// fetchRefTips downloads ref tips, excluding refs whose tip a filtered transfer
// cannot deliver: such tips are trees or blobs. It returns the refs as actually
// stored, which may differ from the listing when the remote moved meanwhile, and
// the names it excluded. Repeated exclusions are bounded.
func (s *Service) fetchRefTips(ctx context.Context, dir, repo string, o gitstore.FetchOptions, refs []gitstore.RemoteRef, refspecs []string, guard func() error) ([]gitstore.RemoteRef, map[string]bool, error) {
	excluded := map[string]bool{}
	for attempt := 0; ; attempt++ {
		err := s.Git.FetchRefs(ctx, dir, repo, o, refspecs, guard)
		var missing *gitstore.MissingTipError
		if err == nil {
			break
		}
		if !errors.As(err, &missing) {
			return nil, nil, err
		}
		found := false
		for _, ref := range refs {
			if (ref.Object == missing.SHA || ref.Peeled == missing.SHA) && !excluded[ref.Name] {
				refspecs = append(refspecs, "^"+ref.Name)
				excluded[ref.Name], found = true, true
			}
		}
		if !found || attempt >= 16 {
			return nil, nil, contract.Fail("REF_DOWNLOAD_LIMIT", "Too many refs point to objects other than commits.", 507)
		}
		if len(refspecs) == 1+countNegative(refspecs) {
			// The only requested ref was excluded: nothing remains to download.
			break
		}
	}
	stored, err := s.Git.LocalRefs(ctx, dir)
	if err != nil {
		return nil, nil, err
	}
	out := make([]gitstore.RemoteRef, 0, len(refs))
	for _, ref := range refs {
		if excluded[ref.Name] {
			out = append(out, ref)
		} else if sha, ok := stored[ref.Name]; ok {
			out = append(out, gitstore.RemoteRef{Name: ref.Name, Object: sha})
		}
		// A ref deleted on the remote meanwhile is no longer listed.
	}
	return out, excluded, nil
}

func countNegative(refspecs []string) int {
	n := 0
	for _, spec := range refspecs {
		if strings.HasPrefix(spec, "^") {
			n++
		}
	}
	return n
}

// readRefObjects reads ref objects and follows tag targets a bounded number of levels.
func (s *Service) readRefObjects(ctx context.Context, dir string, refs []gitstore.RemoteRef) (map[string]gitstore.RefObject, error) {
	objects := map[string]gitstore.RefObject{}
	pending := make([]string, 0, len(refs))
	for _, ref := range refs {
		pending = append(pending, ref.Object)
	}
	for depth := 0; depth <= refTagDepth && len(pending) > 0; depth++ {
		read, err := s.Git.ReadRefObjects(ctx, dir, pending)
		if err != nil {
			return nil, err
		}
		pending = pending[:0]
		for sha, o := range read {
			objects[sha] = o
			if o.Type == "tag" && o.Target != "" {
				if _, seen := objects[o.Target]; !seen {
					pending = append(pending, o.Target)
				}
			}
		}
	}
	return objects, nil
}

func newRefCatalog(entries []RefEntry, state, reason string, truncated bool, started time.Time) *refCatalog {
	c := &refCatalog{entries: entries, state: state, reason: reason, truncated: truncated, refreshedAt: time.Now().UTC(), duration: time.Since(started), byRef: make(map[string]int, len(entries))}
	if state == "ready" {
		sort.SliceStable(c.entries, func(i, j int) bool {
			a, b := c.entries[i].CreatedAt, c.entries[j].CreatedAt
			if (a == nil) != (b == nil) {
				return a != nil
			}
			if a != nil && !a.Equal(*b) {
				return a.After(*b)
			}
			return c.entries[i].Ref < c.entries[j].Ref
		})
	} else {
		sort.SliceStable(c.entries, func(i, j int) bool { return c.entries[i].Ref < c.entries[j].Ref })
	}
	c.byName = make([]int, len(c.entries))
	for i, e := range c.entries {
		c.byRef[e.Ref] = i
		c.byName[i] = i
	}
	sort.SliceStable(c.byName, func(i, j int) bool { return c.entries[c.byName[i]].Name < c.entries[c.byName[j]].Name })
	return c
}

// limitNames keeps the first refCatalogKindLimit names of each kind.
func limitNames(refs []gitstore.RemoteRef) ([]gitstore.RemoteRef, bool) {
	sort.Slice(refs, func(i, j int) bool { return refs[i].Name < refs[j].Name })
	counts := map[string]int{}
	out := refs[:0:0]
	truncated := false
	for _, ref := range refs {
		kind, _ := refKind(ref.Name)
		if counts[kind] >= refCatalogKindLimit {
			truncated = true
			continue
		}
		counts[kind]++
		out = append(out, ref)
	}
	return out, truncated
}

// buildRefCatalog lists the remote first so the ref limit applies before any
// object is downloaded, then downloads commit and tag objects into a scratch
// directory, parses them and removes the directory.
func (s *Service) buildRefCatalog(ctx context.Context, r control.Repository) (*refCatalog, error) {
	started := time.Now()
	dir, cleanup, err := s.refScratch(ctx)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	var catalog *refCatalog
	err = s.withGitCredential(ctx, r, func(token string) error {
		o := s.fetchOptions(r, token)
		o.CheckGrowth = nil
		refs, more, err := s.Git.ListRemoteRefs(ctx, dir, r.Name, o)
		if err != nil {
			return err
		}
		limited, truncated := limitNames(refs)
		if more || truncated {
			catalog = newRefCatalog(refEntries(limited, nil, nil), "names_only", "ref_limit", true, started)
			return nil
		}
		stored, excluded, err := s.fetchRefTips(ctx, dir, r.Name, o, limited, []string{"+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*"}, s.downloadGuard(dir, refRefreshBytes))
		var ce *contract.Error
		if errors.As(err, &ce) && (ce.Code == "PARTIAL_CLONE_UNSUPPORTED" || ce.Code == "REF_DOWNLOAD_LIMIT") {
			reason := "filter_unsupported"
			if ce.Code == "REF_DOWNLOAD_LIMIT" {
				reason = "size_limit"
			}
			catalog = newRefCatalog(refEntries(limited, nil, nil), "names_only", reason, false, started)
			return nil
		}
		if err != nil {
			return err
		}
		objects, err := s.readRefObjects(ctx, dir, stored)
		if err != nil {
			return err
		}
		catalog = newRefCatalog(refEntries(stored, objects, excluded), "ready", "", false, started)
		return nil
	})
	return catalog, err
}

// startRefresh begins one background refresh for the repository unless one is
// running or the repository is backing off. It returns the refresh's done channel.
func (s *Service) startRefresh(r control.Repository) chan struct{} {
	m := &s.refState
	if done, ok := m.refreshes[r.Name]; ok {
		return done
	}
	if f := m.failures[r.Name]; time.Now().Before(f.until) {
		return nil
	}
	done := make(chan struct{})
	m.refreshes[r.Name] = done
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer close(done)
		ctx, cancel := context.WithTimeout(m.ctx, time.Duration(s.gitTimeoutMinutes(r))*time.Minute)
		defer cancel()
		var catalog *refCatalog
		var err error
		select {
		case m.refreshes4 <- struct{}{}:
			catalog, err = s.buildRefCatalog(ctx, r)
			<-m.refreshes4
		case <-ctx.Done():
			err = ctx.Err()
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.refreshes, r.Name)
		if err != nil {
			m.failed(r.Name, err)
			return
		}
		delete(m.failures, r.Name)
		m.catalogs[r.Name], m.used[r.Name] = catalog, time.Now()
		m.trimCatalogs(r.Name)
	}()
	return done
}

// trimCatalogs drops the least recently used catalogs above the memory bound.
func (m *refManager) trimCatalogs(keep string) {
	total := 0
	for _, c := range m.catalogs {
		total += len(c.entries)
	}
	for total > refCatalogMemoryRefs {
		oldest := ""
		for name := range m.catalogs {
			if name != keep && (oldest == "" || m.used[name].Before(m.used[oldest])) {
				oldest = name
			}
		}
		if oldest == "" {
			return
		}
		total -= len(m.catalogs[oldest].entries)
		delete(m.catalogs, oldest)
		delete(m.used, oldest)
	}
}

// refCatalog returns the repository's catalog, refreshing it in the background
// when expired. Without any catalog it waits within the request's deadline.
func (s *Service) refCatalog(ctx context.Context, r control.Repository) (*refCatalog, bool, error) {
	m := &s.refState
	m.mu.Lock()
	m.init()
	catalog := m.catalogs[r.Name]
	if catalog != nil {
		m.used[r.Name] = time.Now()
		fresh := catalog.fresh(time.Now())
		if !fresh {
			s.startRefresh(r)
		}
		m.mu.Unlock()
		return catalog, fresh, nil
	}
	done := s.startRefresh(r)
	failure := m.failures[r.Name]
	m.mu.Unlock()
	if done == nil {
		return nil, false, refFailureError(failure)
	}
	wait := requestWait(ctx)
	timer := time.NewTimer(wait)
	defer timer.Stop()
	if wait > 0 {
		select {
		case <-done:
			m.mu.Lock()
			catalog, failure = m.catalogs[r.Name], m.failures[r.Name]
			m.mu.Unlock()
			if catalog != nil {
				return catalog, true, nil
			}
			return nil, false, refFailureError(failure)
		case <-timer.C:
		case <-ctx.Done():
		}
	}
	return nil, false, contract.FailRetryAfter("REFS_CATALOG_PREPARING", "The branch list is still being prepared in the background (large repositories may take a few minutes the first time); retry in about 30 seconds. An exact branch or tag name can be passed to resolve now.", 503, 30)
}

func refFailureError(f refFailure) error {
	err := remoteRefError(f.err)
	var ce *contract.Error
	if errors.As(err, &ce) && ce.RetryAfterSeconds > 0 && !f.until.IsZero() {
		copy := *ce
		copy.RetryAfterSeconds = retrySeconds(f.until)
		return &copy
	}
	return err
}

// remoteRefError maps Git failures to caller-safe codes without raw diagnostics.
func remoteRefError(err error) error {
	var ce *contract.Error
	if errors.As(err, &ce) {
		switch ce.Code {
		case "GIT_ACCESS_DENIED", "GIT_AUTH_FAILED", "GIT_REPOSITORY_UNAVAILABLE", "GIT_CREDENTIAL_UNAVAILABLE", "CODE_SOURCE_NOT_CONFIGURED", "REMOTE_ACCESS_DENIED":
			return contract.Fail("REMOTE_ACCESS_DENIED", "The code-service credential this repository uses cannot read its remote; ask a repository manager to check it. Your own token is not the cause.", 502)
		case "INSUFFICIENT_STORAGE":
			return contract.FailRetryAfter("INSUFFICIENT_STORAGE", "The server's free disk space is below its reserve; ask a site administrator. This is not the repository budget.", 507, 60)
		case "REMOTE_UNAVAILABLE", "REF_NOT_FOUND", "REF_AMBIGUOUS", "REF_NOT_COMMIT", "INVALID_REVISION":
			return err
		case "REF_DOWNLOAD_LIMIT":
			return contract.Fail("REMOTE_UNAVAILABLE", "The ref's objects exceed the download limit, so its type could not be verified.", 502)
		}
	}
	return contract.FailRetryAfter("REMOTE_UNAVAILABLE", "The repository's remote is temporarily unavailable; retry later. Cached branch lists remain available.", 502, 10)
}

func indexedCommit(r control.Repository, sha string) (control.Version, bool) {
	v, ok := r.Version(sha)
	return v, ok && v.Indexed
}

func refFlags(r control.Repository, e RefEntry) RefEntry {
	if e.Kind == "branch" {
		for _, b := range monitoredBranches(r) {
			e.Monitored = e.Monitored || b == e.Name
		}
	}
	if e.Commit != "" {
		_, e.Indexed = indexedCommit(r, e.Commit)
	}
	return e
}

func parseRefTime(value string, end bool) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, nil
	}
	t, err := time.Parse("2006-01-02", value)
	if err != nil {
		return time.Time{}, contract.Fail("INVALID_TIME", "Use RFC 3339 or YYYY-MM-DD for since and until.", 400)
	}
	if end {
		t = t.Add(24*time.Hour - time.Nanosecond)
	}
	return t, nil
}

// Refs lists a registered repository's branches and tags from its catalog.
func (s *Service) Refs(ctx context.Context, name string, q RefsQuery) (RefsPage, error) {
	r, err := s.Repo(ctx, name)
	if err != nil {
		return RefsPage{}, err
	}
	if q.Kind == "" {
		q.Kind = "branch"
	}
	if q.Kind != "branch" && q.Kind != "tag" && q.Kind != "all" {
		return RefsPage{}, contract.Fail("INVALID_KIND", "Use kind branch, tag or all.", 400)
	}
	if q.Sort != "" && q.Sort != "created" && q.Sort != "name" {
		return RefsPage{}, contract.Fail("INVALID_SORT", "Use sort created or name.", 400)
	}
	if q.First == 0 {
		q.First = 30
	}
	if q.First < 1 || q.First > 100 {
		return RefsPage{}, contract.Fail("INVALID_LIMIT", "Use first between 1 and 100.", 400)
	}
	offset := 0
	if q.After != "" {
		if offset, err = strconv.Atoi(strings.TrimPrefix(q.After, "o")); err != nil || offset < 0 || !strings.HasPrefix(q.After, "o") {
			return RefsPage{}, contract.Fail("INVALID_CURSOR", "Pass next_cursor from the previous page unchanged.", 400)
		}
	}
	since, err := parseRefTime(q.Since, false)
	if err != nil {
		return RefsPage{}, err
	}
	until, err := parseRefTime(q.Until, true)
	if err != nil {
		return RefsPage{}, err
	}
	catalog, fresh, err := s.refCatalog(ctx, r)
	if err != nil {
		return RefsPage{}, err
	}
	sortBy := q.Sort
	if catalog.state == "names_only" {
		if sortBy == "created" || q.Author != "" || q.Title != "" || q.Since != "" || q.Until != "" {
			return RefsPage{}, contract.Fail("REFS_FILTER_UNAVAILABLE", "This repository's branch list has names only ("+catalog.reason+"), so it can only be filtered and sorted by name.", 409)
		}
		sortBy = "name"
	}
	if sortBy == "" {
		sortBy = "created"
	}
	state := catalog.state
	if !fresh {
		state = "stale"
	}
	page := RefsPage{Repo: r.Name, Sort: sortBy, Refs: []RefEntry{}, Catalog: RefCatalogInfo{State: state, Reason: catalog.reason, RefreshedAt: catalog.refreshedAt, Total: len(catalog.entries), Truncated: catalog.truncated}}
	if catalog.truncated {
		page.Hint = "The branch list was truncated at its limit and only covers part of the repository; pass a complete branch or tag name to resolve, which does not depend on this list."
	}
	query, author, title := strings.ToLower(q.Query), strings.ToLower(q.Author), strings.ToLower(q.Title)
	matched := 0
	for i := range catalog.entries {
		index := i
		if sortBy == "name" {
			index = catalog.byName[i]
		}
		e := catalog.entries[index]
		if (q.Kind != "all" && e.Kind != q.Kind) || (query != "" && !strings.Contains(strings.ToLower(e.Name), query)) ||
			(author != "" && !strings.Contains(strings.ToLower(e.Author), author)) || (title != "" && !strings.Contains(strings.ToLower(e.Title), title)) {
			continue
		}
		if (!since.IsZero() || !until.IsZero()) && (e.CreatedAt == nil || (!since.IsZero() && e.CreatedAt.Before(since)) || (!until.IsZero() && e.CreatedAt.After(until))) {
			continue
		}
		if matched >= offset && len(page.Refs) < q.First {
			page.Refs = append(page.Refs, refFlags(r, e))
		} else if len(page.Refs) == q.First {
			page.HasMore = true
			break
		}
		matched++
	}
	if page.HasMore {
		page.NextCursor = "o" + strconv.Itoa(offset+len(page.Refs))
	}
	return page, nil
}

// Resolve returns a fixed commit for a monitored branch, an observed full SHA,
// or any branch or tag name of the registered repository.
func (s *Service) Resolve(ctx context.Context, name, revision string) (ResolveResult, error) {
	r, err := s.Repo(ctx, name)
	if err != nil {
		return ResolveResult{}, err
	}
	v, err := eligible(r, revision)
	if err == nil {
		return s.observedResult(ctx, r, revision, v), nil
	}
	var ce *contract.Error
	if !errors.As(err, &ce) {
		return ResolveResult{}, err
	}
	switch {
	case ce.Code == "BRANCH_NOT_MONITORED":
		if branch, ok := strings.CutPrefix(revision, "refs/heads/"); ok {
			return s.resolveRef(ctx, r, []string{"refs/heads/" + branch})
		}
		return s.resolveRef(ctx, r, []string{"refs/heads/" + revision, "refs/tags/" + revision})
	case ce.Code == "INVALID_REVISION" && strings.HasPrefix(revision, "refs/tags/"):
		if gitstore.ValidateRevision(strings.TrimPrefix(revision, "refs/tags/")) != nil {
			return ResolveResult{}, err
		}
		return s.resolveRef(ctx, r, []string{revision})
	}
	return ResolveResult{}, err
}

func (s *Service) observedResult(ctx context.Context, r control.Repository, revision string, v control.Version) ResolveResult {
	out := ResolveResult{Version: v, Kind: "commit", Source: "observed"}
	if revision == "" || !prepareSHA.MatchString(revision) {
		out.Kind = "branch"
		branch := strings.TrimPrefix(revision, "refs/heads/")
		for _, name := range monitoredBranches(r) {
			if branch == "" && r.Heads[name] == v.Commit {
				branch = name
			}
		}
		out.Monitored = true
		if branch != "" {
			out.Ref = "refs/heads/" + branch
		}
	}
	if groups := r.WorkGroups(); len(groups) > 0 {
		if git, _, err := s.groupGit(r, groups[0]); err == nil {
			if o, err := git.CommitObject(ctx, r.Name, v.Commit); err == nil && o.Type == "commit" {
				out.CreatedAt, out.Author, out.Title = timePtr(o.Time), o.Author, o.Title
			}
		}
	}
	s.addPrepareHint(ctx, r.Name, &out)
	return out
}

func (s *Service) addPrepareHint(ctx context.Context, name string, out *ResolveResult) {
	if out.Indexed {
		return
	}
	out.Hint = "This commit has no index yet. A repository manager can prepare this full SHA."
	if s.AuthorizePrepare(ctx, name) == nil {
		out.Hint = "This commit has no index yet. You can call prepare with this full SHA."
	}
}

// resolveRef answers from a complete, fresh catalog when one exists; otherwise
// it asks the remote for exactly these names. Partial catalogs never prove absence.
func (s *Service) resolveRef(ctx context.Context, r control.Repository, refs []string) (ResolveResult, error) {
	var found []RefEntry
	source := "catalog"
	m := &s.refState
	m.mu.Lock()
	m.init()
	catalog := m.catalogs[r.Name]
	m.mu.Unlock()
	if catalog != nil && catalog.state == "ready" && !catalog.truncated && catalog.fresh(time.Now()) {
		for _, ref := range refs {
			if i, ok := catalog.byRef[ref]; ok {
				found = append(found, catalog.entries[i])
			}
		}
	} else {
		var err error
		source = "remote"
		if found, err = s.lookupRefs(ctx, r, refs); err != nil {
			return ResolveResult{}, err
		}
	}
	return s.refResult(ctx, r, refs, found, source)
}

func (s *Service) refResult(ctx context.Context, r control.Repository, refs []string, found []RefEntry, source string) (ResolveResult, error) {
	_, name := refKind(refs[0])
	switch len(found) {
	case 0:
		return ResolveResult{}, contract.Fail("REF_NOT_FOUND", "No branch or tag named "+name+" exists on the remote; names are case-sensitive. Use refs to search.", 404)
	case 1:
	default:
		parts := make([]string, 0, len(found))
		for _, e := range found {
			target := e.Commit
			if target == "" {
				target = e.Object
			}
			parts = append(parts, e.Ref+" ("+target+")")
		}
		return ResolveResult{}, contract.Fail("REF_AMBIGUOUS", "Both a branch and a tag are named "+name+": "+strings.Join(parts, ", ")+". Pass refs/heads/"+name+" or refs/tags/"+name+".", 409)
	}
	e := refFlags(r, found[0])
	if e.Commit == "" {
		switch e.TargetType {
		case "tree", "blob", "tree_or_blob":
			return ResolveResult{}, contract.Fail("REF_NOT_COMMIT", e.Ref+" does not point to a commit, so it cannot be used to read code.", 409)
		}
		// The object could not be verified, e.g. the ref moved while it was read.
		return ResolveResult{}, contract.FailRetryAfter("REMOTE_UNAVAILABLE", "What "+e.Ref+" points to could not be verified; retry shortly.", 503, 5)
	}
	v, ok := r.Version(e.Commit)
	if !ok {
		v = control.Version{Commit: e.Commit}
		if e.CommitterTime != nil {
			v.Time = *e.CommitterTime
		}
	}
	out := ResolveResult{Version: v, Ref: e.Ref, Kind: e.Kind, Source: source, Monitored: e.Monitored, CreatedAt: e.CreatedAt, Author: e.Author, Title: e.Title}
	s.addPrepareHint(ctx, r.Name, &out)
	return out, nil
}

// lookupRefs asks the remote for exact ref names. Identical lookups share one
// remote request and its result for refLookupTTL (also when nothing was found);
// lookups are bounded per repository and globally, and failures back off.
// Callers have already passed the visibility check.
func (s *Service) lookupRefs(ctx context.Context, r control.Repository, refs []string) ([]RefEntry, error) {
	m := &s.refState
	key := r.Name + "\x00" + strings.Join(refs, "\x00")
	m.mu.Lock()
	call := m.lookups[key]
	if call != nil && call.done == nil && time.Now().After(call.expires) {
		delete(m.lookups, key)
		call = nil
	}
	if call == nil {
		if f := m.failures["lookup\x00"+r.Name]; time.Now().Before(f.until) {
			m.mu.Unlock()
			return nil, refFailureError(f)
		}
		pending := 0
		for _, other := range m.lookups {
			if other.done != nil {
				pending++
			}
		}
		if pending >= refLookupPending {
			m.mu.Unlock()
			return nil, contract.FailRetryAfter("REMOTE_UNAVAILABLE", "Too many remote ref lookups are in progress; retry shortly.", 503, 5)
		}
		call = &refLookup{done: make(chan struct{})}
		m.lookups[key] = call
		m.trimLookups()
		m.wg.Add(1)
		go s.runLookup(r, refs, key, call)
	}
	call.used = time.Now()
	done := call.done
	m.mu.Unlock()
	if done != nil {
		wait := requestWait(ctx)
		if wait == 0 {
			return nil, contract.FailRetryAfter("REMOTE_UNAVAILABLE", "Not enough request time remains to query the remote; retry.", 503, 1)
		}
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			return nil, contract.FailRetryAfter("REMOTE_UNAVAILABLE", "The remote is still answering this lookup; retry in a few seconds.", 503, 5)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return call.entries, call.err
}

func (s *Service) runLookup(r control.Repository, refs []string, key string, call *refLookup) {
	m := &s.refState
	defer m.wg.Done()
	ctx, cancel := context.WithTimeout(m.ctx, refLookupTimeout)
	defer cancel()
	entries, err := s.remoteLookup(ctx, r, refs)
	if err != nil {
		err = remoteRefError(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	call.entries, call.err, call.expires = entries, err, time.Now().Add(refLookupTTL)
	var ce *contract.Error
	if err != nil && !(errors.As(err, &ce) && ce.Code == "REF_NOT_FOUND") {
		m.failed("lookup\x00"+r.Name, err)
		delete(m.lookups, key)
	} else {
		delete(m.failures, "lookup\x00"+r.Name)
	}
	close(call.done)
	call.done = nil
}

// trimLookups keeps at most refLookupEntries cached lookups, dropping the least recently used.
func (m *refManager) trimLookups() {
	for len(m.lookups) > refLookupEntries {
		oldest := ""
		for key, call := range m.lookups {
			if call.done == nil && (oldest == "" || call.used.Before(m.lookups[oldest].used)) {
				oldest = key
			}
		}
		if oldest == "" {
			return
		}
		delete(m.lookups, oldest)
	}
}

func (m *refManager) slot(name string) chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	slot := m.repoSlots[name]
	if slot == nil {
		slot = make(chan struct{}, refLookupPerRepository)
		m.repoSlots[name] = slot
	}
	return slot
}

// remoteLookup lists exactly the requested names, then downloads only the
// matching ref's objects into a scratch directory to verify what it points to.
func (s *Service) remoteLookup(ctx context.Context, r control.Repository, refs []string) ([]RefEntry, error) {
	m := &s.refState
	for _, slot := range []chan struct{}{m.lookups8, m.slot(r.Name)} {
		select {
		case slot <- struct{}{}:
			defer func(slot chan struct{}) { <-slot }(slot)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	dir, cleanup, err := s.refScratch(ctx)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	var entries []RefEntry
	err = s.withGitCredential(ctx, r, func(token string) error {
		o := s.fetchOptions(r, token)
		o.CheckGrowth = nil
		advertised, _, err := s.Git.ListRemoteRefs(ctx, dir, r.Name, o, refs...)
		if err != nil {
			return err
		}
		if len(advertised) != 1 {
			// Absent or ambiguous: names alone answer it, nothing is downloaded.
			entries = refEntries(advertised, nil, nil)
			return nil
		}
		ref := advertised[0].Name
		stored, excluded, err := s.fetchRefTips(ctx, dir, r.Name, o, advertised, []string{"+" + ref + ":" + ref}, s.downloadGuard(dir, refResolveBytes))
		if err != nil {
			return err
		}
		objects, err := s.readRefObjects(ctx, dir, stored)
		if err != nil {
			return err
		}
		entries = refEntries(stored, objects, excluded)
		return nil
	})
	return entries, err
}
