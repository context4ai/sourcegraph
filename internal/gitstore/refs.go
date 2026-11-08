package gitstore

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/context4ai/sourcegraph/internal/contract"
)

// MissingTipError reports a ref tip the filtered transfer cannot deliver, such as
// a tag pointing at a tree or blob. Callers exclude refs at SHA and retry.
type MissingTipError struct{ SHA string }

func (e *MissingTipError) Error() string { return "ref tip " + e.SHA + " is not a commit or tag" }

var missingTip = regexp.MustCompile(`bad (?:object|revision) '?([0-9a-f]{40})`)

// MaxAdvertisedRefs bounds how many advertised refs one listing reads.
const MaxAdvertisedRefs = 1_000_000

// RemoteRef is one advertised branch or tag; Peeled is the ^{} target of an annotated tag.
type RemoteRef struct {
	Name   string
	Object string
	Peeled string
}

// RefObject is the metadata of one commit or tag object in a ref scratch repository.
// Type is "missing" when the object was not downloaded.
type RefObject struct {
	Type       string
	Target     string
	TargetType string
	Author     string
	Time       time.Time
	Title      string
}

// InitScratch creates an empty bare repository used only for ref discovery.
func (s *Store) InitScratch(ctx context.Context, dir string) error {
	_, err := s.run(ctx, dir, 1024, "init", "--bare", "--quiet", dir)
	return err
}

// ListRemoteRefs reads advertised branches and tags. Without names it lists every
// branch and tag; with names it returns only exact matches of those full ref names.
// More is true when the remote advertised more than MaxAdvertisedRefs refs.
func (s *Store) ListRemoteRefs(ctx context.Context, dir, repo string, o FetchOptions, names ...string) (refs []RemoteRef, more bool, err error) {
	if err = ValidateRepo(repo); err != nil {
		return nil, false, err
	}
	remote := remoteURL(repo, o.Host)
	args := append(fetchConfiguration(o), "ls-remote")
	if len(names) == 0 {
		args = append(args, "--heads", "--tags", remote)
	} else {
		args = append(args, remote)
		for _, name := range names {
			args = append(args, name, name+"^{}")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, refTimeout(o))
	defer cancel()
	command := s.command(ctx, dir, args...)
	command.Env = append(command.Env, credentialEnv(remote, o)...)
	diagnostic := &prefixTrace{limit: 64 << 10}
	command.Stderr = diagnostic
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, false, err
	}
	if err = command.Start(); err != nil {
		return nil, false, err
	}
	wanted := map[string]bool{}
	for _, name := range names {
		wanted[name] = true
	}
	index := map[string]int{}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), 64<<10)
	lines := 0
	for scanner.Scan() {
		sha, name, ok := strings.Cut(scanner.Text(), "\t")
		if !ok || !fullSHA.MatchString(sha) {
			continue
		}
		peeled := strings.HasSuffix(name, "^{}")
		name = strings.TrimSuffix(name, "^{}")
		if !strings.HasPrefix(name, "refs/heads/") && !strings.HasPrefix(name, "refs/tags/") {
			continue
		}
		if len(names) > 0 && !wanted[name] {
			continue
		}
		if lines++; lines > MaxAdvertisedRefs {
			more = true
			cancel()
			break
		}
		i, seen := index[name]
		if !seen {
			i = len(refs)
			index[name] = i
			refs = append(refs, RemoteRef{Name: name})
		}
		if peeled {
			refs[i].Peeled = sha
		} else {
			refs[i].Object = sha
		}
	}
	_, _ = io.Copy(io.Discard, stdout)
	waitErr := command.Wait()
	if more {
		return refs, true, nil
	}
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	if waitErr != nil || scanner.Err() != nil {
		return nil, false, remoteFailure(diagnostic.String(), "GIT_FETCH_FAILED", "Cannot list remote refs.")
	}
	return refs, false, nil
}

// FetchRefs downloads only commit and tag objects of the given refspecs into a
// scratch repository; refspecs may include negative "^refs/..." exclusions. guard runs before and every second during the transfer;
// its error aborts the fetch. Servers without partial clone are rejected first
// because Git would otherwise silently download whole trees.
func (s *Store) FetchRefs(ctx context.Context, dir, repo string, o FetchOptions, refspecs []string, guard func() error) error {
	if err := ValidateRepo(repo); err != nil {
		return err
	}
	remote := remoteURL(repo, o.Host)
	ctx, cancel := context.WithTimeout(ctx, refTimeout(o))
	defer cancel()
	if err := s.requirePartialClone(ctx, dir, remote, o); err != nil {
		return err
	}
	if err := guard(); err != nil {
		return err
	}
	args := append(fetchConfiguration(o), "fetch", "--quiet", "--no-tags", "--force", "--no-write-fetch-head", "--depth=1", "--filter=tree:0", remote)
	args = append(args, refspecs...)
	command := s.command(ctx, dir, args...)
	command.Env = append(command.Env, credentialEnv(remote, o)...)
	command.Stdout = io.Discard
	diagnostic := &prefixTrace{limit: 64 << 10}
	command.Stderr = diagnostic
	done := make(chan struct{})
	var capacity error
	go func() {
		defer close(done)
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if e := guard(); e != nil {
					capacity = e
					cancel()
					return
				}
			}
		}
	}()
	err := command.Run()
	contextErr := ctx.Err()
	cancel()
	<-done
	if capacity != nil {
		return capacity
	}
	if contextErr != nil {
		return contextErr
	}
	if err != nil {
		if m := missingTip.FindStringSubmatch(diagnostic.String()); m != nil {
			return &MissingTipError{SHA: m[1]}
		}
		return remoteFailure(diagnostic.String(), "GIT_FETCH_FAILED", "Cannot download remote refs.")
	}
	return guard()
}

// ReadRefObjects reads commit and tag headers from a scratch repository. Objects
// that were not downloaded report Type "missing"; nothing is fetched lazily.
func (s *Store) ReadRefObjects(ctx context.Context, dir string, shas []string) (map[string]RefObject, error) {
	out := map[string]RefObject{}
	if len(shas) == 0 {
		return out, nil
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	command := s.command(ctx, dir, "cat-file", "--batch")
	var input bytes.Buffer
	for _, sha := range shas {
		if fullSHA.MatchString(sha) {
			input.WriteString(sha + "\n")
		}
	}
	command.Stdin = &input
	command.Stderr = io.Discard
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = command.Start(); err != nil {
		return nil, err
	}
	reader := bufio.NewReaderSize(stdout, 64<<10)
	parseErr := func() error {
		for {
			header, err := reader.ReadString('\n')
			if err == io.EOF && header == "" {
				return nil
			}
			if err != nil {
				return err
			}
			fields := strings.Fields(header)
			if len(fields) == 2 && fields[1] == "missing" {
				out[fields[0]] = RefObject{Type: "missing"}
				continue
			}
			if len(fields) != 3 {
				return contract.Fail("GIT_OBJECT_UNREADABLE", "Unexpected object header.", 500)
			}
			size, err := strconv.ParseInt(fields[2], 10, 64)
			if err != nil || size < 0 {
				return contract.Fail("GIT_OBJECT_UNREADABLE", "Unexpected object size.", 500)
			}
			// Headers and the first message line are enough; long messages are skipped.
			keep := size
			if keep > 64<<10 {
				keep = 64 << 10
			}
			body := make([]byte, keep)
			if _, err = io.ReadFull(reader, body); err != nil {
				return err
			}
			if _, err = io.CopyN(io.Discard, reader, size-keep+1); err != nil {
				return err
			}
			out[fields[0]] = parseRefObject(fields[1], body)
		}
	}()
	waitErr := command.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if parseErr != nil {
		return nil, parseErr
	}
	if waitErr != nil {
		return nil, contract.Fail("GIT_OBJECT_UNREADABLE", "Cannot read downloaded ref objects.", 500)
	}
	return out, nil
}

func parseRefObject(kind string, body []byte) RefObject {
	o := RefObject{Type: kind}
	if kind != "commit" && kind != "tag" {
		return o
	}
	head, message, _ := strings.Cut(string(body), "\n\n")
	for _, line := range strings.Split(head, "\n") {
		key, value, _ := strings.Cut(line, " ")
		switch {
		case kind == "tag" && key == "object":
			o.Target = value
		case kind == "tag" && key == "type":
			o.TargetType = value
		case (kind == "commit" && key == "author") || (kind == "tag" && key == "tagger"):
			o.Author, _ = splitIdentity(value)
			if kind == "tag" {
				_, o.Time = splitIdentity(value)
			}
		case kind == "commit" && key == "committer":
			_, o.Time = splitIdentity(value)
		}
	}
	o.Title, _, _ = strings.Cut(strings.TrimLeft(message, "\n"), "\n")
	o.Title = strings.TrimSpace(o.Title)
	return o
}

// splitIdentity parses "Name <email> 1700000000 +0800".
func splitIdentity(value string) (string, time.Time) {
	end := strings.LastIndex(value, ">")
	if end < 0 {
		return strings.TrimSpace(value), time.Time{}
	}
	fields := strings.Fields(value[end+1:])
	var at time.Time
	if len(fields) > 0 {
		if seconds, err := strconv.ParseInt(fields[0], 10, 64); err == nil {
			at = time.Unix(seconds, 0).UTC()
		}
	}
	return strings.TrimSpace(value[:end+1]), at
}

// DirectorySize sums regular file sizes below dir.
func DirectorySize(dir string) int64 {
	var total int64
	var walk func(string)
	walk = func(p string) {
		entries, err := os.ReadDir(p)
		if err != nil {
			return
		}
		for _, entry := range entries {
			path := p + "/" + entry.Name()
			if entry.IsDir() {
				walk(path)
			} else if info, err := entry.Info(); err == nil && info.Mode().IsRegular() {
				total += info.Size()
			}
		}
	}
	walk(dir)
	return total
}

// CheckFreeAt reports INSUFFICIENT_STORAGE when root's free space is below min.
func CheckFreeAt(root string, min uint64) error { return checkFree(root, min) }

func refTimeout(o FetchOptions) time.Duration {
	if o.Timeout <= 0 || o.Timeout > 60*time.Minute {
		return 15 * time.Minute
	}
	return o.Timeout
}

// CommitObject reads one commit header from a repository this store owns.
func (s *Store) CommitObject(ctx context.Context, repo, sha string) (RefObject, error) {
	p, err := s.repoPath(repo)
	if err != nil {
		return RefObject{}, err
	}
	objects, err := s.ReadRefObjects(ctx, p, []string{sha})
	if err != nil {
		return RefObject{}, err
	}
	return objects[sha], nil
}

// LocalRefs returns the refs a scratch download actually stored. The remote can
// move a ref between listing and fetching, so callers read these, not the
// advertisement.
func (s *Store) LocalRefs(ctx context.Context, dir string) (map[string]string, error) {
	out, err := s.run(ctx, dir, 256<<20, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads", "refs/tags")
	if err != nil {
		return nil, err
	}
	refs := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if name, sha, ok := strings.Cut(line, " "); ok && fullSHA.MatchString(sha) {
			refs[name] = sha
		}
	}
	return refs, nil
}
