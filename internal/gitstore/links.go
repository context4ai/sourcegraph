package gitstore

import (
	"context"
	"errors"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/context4ai/sourcegraph/internal/contract"
)

// LinkInfo is immutable metadata from one Git tree, never an OS symlink.
type LinkInfo struct {
	Target      string `json:"Target,omitempty"`
	TargetKind  string `json:"TargetKind,omitempty"`
	BlobOID     string `json:"BlobOID,omitempty"`
	FollowError string `json:"FollowError,omitempty"`
}
type LinkSnapshot struct {
	UnsupportedPathsSkipped bool `json:",omitempty"`
	Format                  int
	Followed                bool
	Paths                   []string
	Links                   map[string]LinkInfo
	Skipped                 map[string]int
	Objects                 map[string]string `json:"-"`
}

func linkError(code, message string) error { return contract.Fail(code, message, 422) }
func LinkErrorCode(err error) string {
	var e *contract.Error
	if errors.As(err, &e) && strings.HasPrefix(e.Code, "LINK_") {
		return e.Code
	}
	return ""
}

type linkResolver struct {
	unsupportedPathsSkipped bool
	hops                    int
	trees                   map[string]map[string]Entry
	blobs                   map[string][]byte
	store                   *Store
	ctx                     context.Context
	repo, commit, dir       string
	hydrate                 func([]Entry) error
}

func (r *linkResolver) blob(e Entry) ([]byte, error) {
	if b, ok := r.blobs[e.BlobOID]; ok {
		return b, nil
	}
	if r.hydrate != nil {
		if err := r.hydrate([]Entry{e}); err != nil {
			return nil, err
		}
	}
	sizes, err := r.store.blobSizes(r.ctx, r.dir, []Entry{e})
	if err != nil {
		return nil, err
	}
	size, ok := sizes[e.BlobOID]
	if !ok {
		return nil, contract.Fail("GIT_OBJECT_NOT_HYDRATED", "Link object is unavailable; prepare this scope.", 503)
	}
	if size == 0 || size > 4096 {
		return nil, linkError("LINK_INVALID_TARGET", "Link target must contain 1..4096 UTF-8 bytes.")
	}
	b, err := r.store.prefix(r.ctx, r.dir, e.BlobOID, 4097)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(b) || strings.ContainsRune(string(b), 0) {
		return nil, linkError("LINK_INVALID_TARGET", "Link target is not a UTF-8 path.")
	}
	for _, c := range string(b) {
		if unicode.IsControl(c) || c == '\\' {
			return nil, linkError("LINK_INVALID_TARGET", "Link target uses an unsupported Git path character.")
		}
	}
	if r.blobs == nil {
		r.blobs = map[string][]byte{}
	}
	r.blobs[e.BlobOID] = b
	return b, nil
}
func (r *linkResolver) resolve(input string, active map[string]bool, depth int) (Entry, error) {
	if strings.HasPrefix(input, "/") {
		return Entry{}, linkError("LINK_OUTSIDE_REPOSITORY", "Absolute link targets are not followed.")
	}
	parts := strings.Split(input, "/")
	current := ""
	for i, part := range parts {
		if err := r.ctx.Err(); err != nil {
			return Entry{}, err
		}
		switch part {
		case "", ".":
			continue
		case "..":
			if current == "" {
				return Entry{}, linkError("LINK_OUTSIDE_REPOSITORY", "Link leaves the repository.")
			}
			current = path.Dir(current)
			if current == "." {
				current = ""
			}
			continue
		}
		if strings.EqualFold(part, ".git") {
			return Entry{}, linkError("LINK_OUTSIDE_REPOSITORY", "Git metadata is not followed.")
		}
		candidate := part
		if current != "" {
			candidate = current + "/" + part
		}
		e, err := r.entry(candidate)
		if err != nil {
			var ce *contract.Error
			if errors.As(err, &ce) && ce.Code == "FILE_NOT_FOUND" && depth > 0 {
				return Entry{}, linkError("LINK_DANGLING", "Link target is absent from this commit: "+candidate)
			}
			return Entry{}, err
		}
		if e.Kind == "submodule" {
			return Entry{}, linkError("LINK_TO_SUBMODULE", "Links cannot enter a submodule.")
		}
		if e.Kind == "symlink" {
			if active[candidate] {
				return Entry{}, linkError("LINK_LOOP", "Link resolution contains a cycle.")
			}
			r.hops++
			if depth >= 8 || r.hops > 8 {
				return Entry{}, linkError("LINK_DEPTH_EXCEEDED", "Link resolution exceeds eight links.")
			}
			b, err := r.blob(e)
			if err != nil {
				return Entry{}, err
			}
			target := string(b)
			if strings.HasPrefix(target, "/") {
				return Entry{}, linkError("LINK_OUTSIDE_REPOSITORY", "Absolute link targets are not followed.")
			}
			if current != "" {
				target = current + "/" + target
			}
			active[candidate] = true
			resolved, err := r.resolve(target, active, depth+1)
			delete(active, candidate)
			if err != nil {
				var problem *contract.Error
				if errors.As(err, &problem) && LinkErrorCode(err) != "" {
					return Entry{}, linkError(problem.Code, "Link "+candidate+": "+problem.Message)
				}
				return Entry{}, err
			}
			if resolved.Kind == "directory" && (resolved.Path == "" || strings.HasPrefix(candidate, resolved.Path+"/")) {
				return Entry{}, linkError("LINK_LOOP", "Directory link points to an ancestor.")
			}
			// Keep the resolved path before processing subsequent '..' components.
			e = resolved
		}
		if i < len(parts)-1 && e.Kind != "directory" {
			if depth > 0 {
				return Entry{}, linkError("LINK_DANGLING", "An intermediate link target is not a directory.")
			}
			return Entry{}, contract.Fail("FILE_NOT_FOUND", "Requested path does not name a file or directory.", 404)
		}
		current = e.Path
	}
	if current == "" {
		return Entry{Path: "", Kind: "directory", BlobOID: r.commit}, nil
	}
	return r.entry(current)
}

// ResolvePath follows links within one immutable tree. It never hydrates objects.
func (s *Store) ResolvePath(ctx context.Context, repo, commit, input string) (Entry, error) {
	p, err := s.fixed(ctx, repo, commit)
	if err != nil {
		return Entry{}, err
	}
	return s.resolvePath(ctx, p, repo, commit, input, nil)
}
func (s *Store) resolvePath(ctx context.Context, p, repo, commit, input string, hydrate func([]Entry) error) (Entry, error) {
	// Ordinary reads keep their single tree lookup; only links need a walk.
	e, err := s.entry(ctx, p, commit, input)
	if err == nil && e.Kind != "symlink" {
		return e, nil
	}
	var ce *contract.Error
	if err != nil && (!errors.As(err, &ce) || ce.Code != "FILE_NOT_FOUND") {
		return Entry{}, err
	}
	r := linkResolver{store: s, ctx: ctx, repo: repo, commit: commit, dir: p, hydrate: hydrate}
	resolved, err := r.resolve(input, map[string]bool{}, 0)
	if err != nil {
		return Entry{}, err
	}

	return resolved, nil
}
