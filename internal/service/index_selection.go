package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"time"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/gitstore"
	"github.com/context4ai/sourcegraph/internal/identity"
)

type IndexSelection struct {
	Branch  string `json:"branch"`
	Commit  string `json:"commit"`
	Preview string `json:"preview,omitempty"`
}
type IndexSelectionPreview struct {
	Preview        string   `json:"preview"`
	Revision       int64    `json:"revision"`
	Branch         string   `json:"branch"`
	Commit         string   `json:"commit"`
	Groups         []string `json:"groups"`
	AlreadyIndexed bool     `json:"already_indexed"`
}
type CommitChoice struct {
	gitstore.CommitOption
	Eligible bool `json:"eligible"`
	Indexed  bool `json:"indexed"`
}
type CommitOptions struct {
	Branches   []string       `json:"branches"`
	Branch     string         `json:"branch"`
	Head       string         `json:"head"`
	Commits    []CommitChoice `json:"commits"`
	ObservedAt time.Time      `json:"observed_at"`
}

func monitoredBranches(r control.Repository) []string {
	branches := append([]string(nil), r.Policy.Branches...)
	if r.Policy.DefaultBranch && r.DefaultBranch != "" {
		for _, b := range branches {
			if b == r.DefaultBranch {
				return branches
			}
		}
		branches = append([]string{r.DefaultBranch}, branches...)
	}
	return branches
}
func selectedBranch(r control.Repository, branch string) (string, error) {
	branches := monitoredBranches(r)
	if branch == "" {
		if r.Policy.DefaultBranch && r.DefaultBranch == "" {
			return "", contract.Fail("DEFAULT_BRANCH_NOT_OBSERVED", "Synchronize the repository to discover its default branch.", 409)
		}
		for _, b := range branches {
			if b == r.DefaultBranch {
				branch = b
				break
			}
		}
		if branch == "" {
			for _, b := range branches {
				if b == "main" || b == "master" {
					branch = b
					break
				}
			}
		}
		if branch == "" && len(branches) > 0 {
			branch = branches[0]
		}
	}
	for _, b := range branches {
		if branch == b {
			return b, nil
		}
	}
	return "", contract.Fail("INVALID_BRANCH", "Choose a monitored branch.", 400)
}
func (s *Service) CommitOptions(ctx context.Context, name, branch string) (CommitOptions, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, err := s.DB.Get(ctx, name)
	if err != nil {
		return CommitOptions{}, err
	}
	branch, err = selectedBranch(r, branch)
	if err != nil {
		return CommitOptions{}, err
	}
	group := r.WorkGroups()[0]
	scope := r.Scope(group)
	head := scope.Heads[branch]
	if head == "" {
		return CommitOptions{}, contract.Fail("BRANCH_NOT_OBSERVED", "Synchronize the repository before selecting commits.", 409)
	}
	result := CommitOptions{Branches: monitoredBranches(r), Branch: branch, Head: head, ObservedAt: scope.ObservedAt, Commits: []CommitChoice{}}
	versions := append([]control.Version(nil), scope.Versions...)
	sort.SliceStable(versions, func(i, j int) bool { return versions[i].ObservedAt.After(versions[j].ObservedAt) })
	for _, version := range versions {
		indexed := true
		for _, g := range r.WorkGroups() {
			v, ok := r.ScopedVersion(g, version.Commit)
			indexed = indexed && ok && v.Generation != 0 && !s.markStorage(r).StorageMissing
		}
		result.Commits = append(result.Commits, CommitChoice{gitstore.CommitOption{Commit: version.Commit, CommitterTime: version.Time}, true, indexed})
		if len(result.Commits) == 20 {
			break
		}
	}
	return result, nil
}
func (s *Service) indexSelection(ctx context.Context, r control.Repository, q IndexSelection) (IndexSelectionPreview, error) {
	if !r.Enabled || r.Deleted {
		return IndexSelectionPreview{}, contract.Fail("REPOSITORY_DISABLED", "Enable the repository before preparing an index.", 409)
	}
	branch, err := selectedBranch(r, q.Branch)
	if err != nil {
		return IndexSelectionPreview{}, err
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(q.Commit) {
		return IndexSelectionPreview{}, contract.Fail("INVALID_REVISION", "Enter a full 40-character commit SHA.", 400)
	}
	ready := !s.markStorage(r).StorageMissing
	for _, group := range r.WorkGroups() {
		version, ok := r.ScopedVersion(group, q.Commit)
		ready = ready && ok && version.Generation != 0
	}
	return IndexSelectionPreview{Preview: digest([]any{"index", r.Revision, branch, q.Commit, r.WorkGroups()}), Revision: r.Revision, Branch: branch, Commit: q.Commit, Groups: r.WorkGroups(), AlreadyIndexed: ready}, nil
}
func (s *Service) PreviewIndex(ctx context.Context, name string, q IndexSelection) (IndexSelectionPreview, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, err := s.DB.Get(ctx, name)
	if err != nil {
		return IndexSelectionPreview{}, err
	}
	return s.indexSelection(ctx, r, q)
}

// indexSelectionReplay checks durable receipts before preview validation: a client
// may retry after a successful enqueue whose response was lost.
func indexSelectionReplay(r control.Repository, q IndexSelection, key string) ([]control.Job, error) {
	branch, err := selectedBranch(r, q.Branch)
	if err != nil {
		return nil, err
	}
	replay := []control.Job{}
	now := time.Now()
	for _, group := range r.WorkGroups() {
		receiptKey := digest([]string{key, group})
		body := digest([]string{"index-selection", branch, q.Commit, group})
		if group != "" {
			sum := sha256.Sum256([]byte(group + "\x00" + body))
			body = hex.EncodeToString(sum[:])
		}
		for _, receipt := range r.Receipts {
			if receipt.Key != receiptKey || !now.Before(receipt.Expires) {
				continue
			}
			if receipt.Digest != body {
				return nil, contract.Fail("IDEMPOTENCY_CONFLICT", "Key was used for a different request.", 409)
			}
			for _, job := range r.Jobs {
				if job.ID == receipt.JobID {
					replay = append(replay, job)
					break
				}
			}
		}
	}
	if len(replay) != len(r.WorkGroups()) {
		return nil, nil
	}
	return replay, nil
}

func indexSelectionResult(jobs []control.Job) any {
	if len(jobs) == 1 {
		return jobs[0]
	}
	return map[string]any{"jobs": jobs, "status": "accepted"}
}

func (s *Service) CreateIndex(ctx context.Context, name string, q IndexSelection, revision int64, key string) (any, error) {
	k, err := requestKey(identity.Actor(ctx)+":index-selection", key)
	if err != nil {
		return nil, err
	}
	// Pin the storage root through validation and publication. The shared query
	// lock protects observed snapshot metadata during validation.
	s.preparation.RLock()
	defer s.preparation.RUnlock()
	var p IndexSelectionPreview
	var replay []control.Job
	err = func() error {
		s.mu.RLock()
		defer s.mu.RUnlock()
		r, err := s.DB.Get(ctx, name)
		if err != nil {
			return err
		}
		replay, err = indexSelectionReplay(r, q, k)
		if err != nil || replay != nil {
			return err
		}
		if r.Revision != revision {
			return contract.Fail("REVISION_CONFLICT", "Index preview expired; reload and preview again.", 412)
		}
		p, err = s.indexSelection(ctx, r, q)
		if err != nil {
			return err
		}
		if p.Preview != q.Preview {
			return contract.Fail("REVISION_CONFLICT", "Index preview expired; reload and preview again.", 412)
		}
		return nil
	}()
	if err != nil {
		return nil, err
	}
	if replay != nil {
		return indexSelectionResult(replay), nil
	}

	// Do not rerun Git under the exclusive lock. A policy change, maintenance or
	// worker publication since validation invalidates this exact revision.
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.DB.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	replay, err = indexSelectionReplay(r, q, k)
	if err != nil {
		return nil, err
	}
	if replay != nil {
		return indexSelectionResult(replay), nil
	}
	if r.Revision != p.Revision {
		return nil, contract.Fail("REVISION_CONFLICT", "Index preview expired; reload and preview again.", 412)
	}
	jobs := make([]control.Job, 0, len(p.Groups))
	for _, group := range p.Groups {
		j, err := r.QueueScoped("prepare", q.Commit, group, digest([]string{k, group}), digest([]string{"index-selection", p.Branch, q.Commit, group}), identity.Actor(ctx), control.SourceManual, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	r.Revision++
	r.Updated = time.Now().UTC()
	if err = s.DB.Replace(ctx, r, p.Revision); err != nil {
		return nil, err
	}
	return indexSelectionResult(jobs), nil
}
