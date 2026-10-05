// Package gitstore reads immutable objects from existing trusted bare repositories.
// It does not decide repository access, clone a remote or modify refs. Callers must
// authorize and retain the selected snapshot for the duration of each operation.
package gitstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/context4ai/sourcegraph/internal/contract"
)

var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)
var repoPart = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)
var refChars = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]*$`)

func ValidateRepo(repo string) error {
	parts := strings.Split(repo, "/")
	if len(parts) < 2 || len(repo) > 512 {
		return contract.Fail("INVALID_REPO", "Use a canonical namespace/repository name.", 400)
	}
	for _, p := range parts {
		if !repoPart.MatchString(p) || p == "." || p == ".." || strings.HasSuffix(p, ".git") {
			return contract.Fail("INVALID_REPO", "Invalid repository name.", 400)
		}
	}
	return nil
}
func ValidatePath(p string, allowRoot bool) error {
	if p == "" && allowRoot {
		return nil
	}
	if p == "" || len(p) > 4096 || !utf8.ValidString(p) || strings.Contains(p, "\\") || strings.HasPrefix(p, "/") {
		return contract.Fail("INVALID_PATH", "Use a relative Git tree path.", 400)
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return contract.Fail("INVALID_PATH", "Control characters are not supported.", 400)
		}
	}
	for _, s := range strings.Split(p, "/") {
		if s == "" || s == "." || s == ".." {
			return contract.Fail("INVALID_PATH", "Invalid Git path segment.", 400)
		}
	}
	return nil
}
func ValidateRevision(r string) error {
	if fullSHA.MatchString(r) {
		return nil
	}
	if r == "" || r == "HEAD" || len(r) > 1024 || !refChars.MatchString(r) || strings.Contains(r, "..") || strings.Contains(r, "//") || strings.HasSuffix(r, "/") || strings.HasSuffix(r, ".") {
		return contract.Fail("INVALID_REVISION", "Use a full SHA or a named branch/tag.", 400)
	}
	if strings.HasPrefix(r, "refs/") && !strings.HasPrefix(r, "refs/heads/") && !strings.HasPrefix(r, "refs/tags/") {
		return contract.Fail("INVALID_REVISION", "Unsupported ref namespace.", 400)
	}
	for _, s := range strings.Split(r, "/") {
		if strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".lock") {
			return contract.Fail("INVALID_REVISION", "Invalid ref name.", 400)
		}
	}
	return nil
}

// RepositoryDir is an opaque disk identity, not a way to infer authorization.
func RepositoryDir(root, repo string) (string, error) {
	if err := ValidateRepo(repo); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(repo))
	return filepath.Join(root, "repos", hex.EncodeToString(sum[:]), "bare.git"), nil
}

type Store struct {
	generation uint32
	root       string
	git        string
	timeout    time.Duration
	group      string
	boundRepo  string
	paths      []string
}

func New(root string) (*Store, error) {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("data directory must already exist")
	}
	abs, err := filepath.Abs(resolved)
	if err != nil {
		return nil, err
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("git executable not available")
	}
	return &Store{root: abs, git: git, timeout: 10 * time.Second}, nil
}
func (s *Store) repoPath(repo string) (string, error) {
	p, err := s.repositoryDir(repo)
	if err != nil {
		return "", err
	}
	actual, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", contract.Fail("REPOSITORY_NOT_READY", "Repository objects are unavailable.", 503)
	}
	rel, err := filepath.Rel(s.root, actual)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", contract.Fail("INVALID_REPOSITORY_PATH", "Repository is outside its data root.", 500)
	}
	if actual != p {
		return "", contract.Fail("INVALID_REPOSITORY_PATH", "Repository symlink layout is not supported.", 500)
	}
	if info, err := os.Stat(actual); err != nil || !info.IsDir() {
		return "", contract.Fail("REPOSITORY_NOT_READY", "Repository objects are unavailable.", 503)
	}
	return p, nil
}
func (s *Store) command(ctx context.Context, p string, args ...string) *exec.Cmd {
	fixed := []string{"--no-pager", "--git-dir=" + p, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "diff.external=", "-c", "core.quotePath=true"}
	c := exec.CommandContext(ctx, s.git, append(fixed, args...)...)
	c.Env = []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_ATTR_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_LITERAL_PATHSPECS=1", "GIT_NO_LAZY_FETCH=1"}
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error {
		if c.Process == nil {
			return os.ErrProcessDone
		}
		e := syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		if e == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return e
	}
	// Bound inherited-pipe cleanup within the HTTP response margin after cancellation.
	c.WaitDelay = 50 * time.Millisecond
	return c
}

type limitWriter struct {
	bytes.Buffer
	limit    int
	overflow bool
}

// bytes.Buffer's promoted ReadFrom would bypass Write when os/exec copies a
// command's stdout. Hide that fast path so every byte observes the same limit.
func (w *limitWriter) ReadFrom(r io.Reader) (int64, error) {
	return io.Copy(struct{ io.Writer }{w}, r)
}

func (w *limitWriter) Write(p []byte) (int, error) {
	if len(p) > w.limit-w.Len() {
		w.overflow = true
		return 0, errors.New("output limit")
	}
	return w.Buffer.Write(p)
}
func (s *Store) run(ctx context.Context, p string, limit int, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	c := s.command(ctx, p, args...)
	out := &limitWriter{limit: limit}
	c.Stdout = out
	c.Stderr = io.Discard
	err := c.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if out.overflow {
		return nil, contract.Fail("GIT_OUTPUT_LIMIT", "Git output exceeded the operation budget.", 422)
	}
	if err != nil {
		return nil, contract.Fail("GIT_OPERATION_FAILED", "Git could not complete the operation.", 500)
	}
	return out.Bytes(), nil
}
func (s *Store) Resolve(ctx context.Context, repo, revision string) (string, error) {
	if err := ValidateRevision(revision); err != nil {
		return "", err
	}
	p, err := s.repoPath(repo)
	if err != nil {
		return "", err
	}
	format, err := s.run(ctx, p, 100, "config", "--get", "extensions.objectFormat")
	if err != nil {
		var ce *contract.Error
		if !errors.As(err, &ce) || ce.Code != "GIT_OPERATION_FAILED" {
			return "", err
		}
	}
	if len(bytes.TrimSpace(format)) != 0 && string(bytes.TrimSpace(format)) != "sha1" {
		return "", contract.Fail("UNSUPPORTED_REPOSITORY_FORMAT", "Only SHA-1 repositories are supported.", 422)
	}
	bare, err := s.run(ctx, p, 20, "rev-parse", "--is-bare-repository")
	if err != nil {
		return "", err
	}
	if string(bytes.TrimSpace(bare)) != "true" {
		return "", contract.Fail("INVALID_REPOSITORY_FORMAT", "A bare repository is required.", 500)
	}
	candidates := []string{revision}
	if !fullSHA.MatchString(revision) && !strings.HasPrefix(revision, "refs/") {
		candidates = []string{"refs/heads/" + revision, "refs/tags/" + revision}
	}
	var commits []string
	for _, candidate := range candidates {
		out, e := s.run(ctx, p, 100, "rev-parse", "--verify", candidate+"^{commit}")
		if e != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			var ce *contract.Error
			if errors.As(e, &ce) && ce.Code == "GIT_OPERATION_FAILED" {
				continue
			}
			return "", e
		}
		sha := string(bytes.TrimSpace(out))
		if !fullSHA.MatchString(sha) {
			return "", contract.Fail("INVALID_GIT_RESULT", "Invalid Git commit response.", 500)
		}
		commits = append(commits, sha)
	}
	if len(commits) > 1 {
		return "", contract.Fail("AMBIGUOUS_REVISION", "Branch and tag names overlap; use a full ref.", 409)
	}
	if len(commits) == 0 {
		return "", contract.Fail("REVISION_NOT_FOUND", "Commit was not found.", 404)
	}
	return commits[0], nil
}
func (s *Store) fixed(ctx context.Context, repo, commit string) (string, error) {
	if !fullSHA.MatchString(commit) {
		return "", contract.Fail("INVALID_REVISION", "Object reads require a full SHA.", 400)
	}
	if _, err := s.Resolve(ctx, repo, commit); err != nil {
		return "", err
	}
	return s.repoPath(repo)
}
