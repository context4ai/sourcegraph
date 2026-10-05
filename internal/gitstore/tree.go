package gitstore

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/context4ai/sourcegraph/internal/contract"
)

type Entry struct {
	CanonicalPath string `json:"-"`
	Target        string `json:",omitempty"`
	TargetKind    string `json:",omitempty"`
	FollowError   string `json:",omitempty"`
	Name          string
	Path          string
	Kind          string
	BlobOID       string
	Size          *int64
}
type Tree struct {
	Via            string `json:",omitempty"`
	Commit         string
	TreeOID        string
	Entries        []Entry
	NextCursor     string
	HasMoreResults bool
}
type treeCursor struct {
	Commit string
	Path   string
	Last   string
	Scope  string
}

// Cursor is an immutable tree position, not an access token. Production transport
// must authenticate its own cursors and recheck policy on every call.
func (s *Store) List(ctx context.Context, repo, commit, path string, first int, after string) (Tree, error) {
	result := Tree{Commit: commit, Entries: []Entry{}}
	if err := ValidatePath(path, true); err != nil {
		return result, err
	}
	if !s.allows(path, true) {
		return result, contract.Fail("PATH_NOT_INDEXED", "Path is outside the registered scope.", 404)
	}
	if first < 1 || first > 1000 {
		return result, contract.Fail("INVALID_LIMIT", "Directory limit must be in 1..1000.", 400)
	}
	p, err := s.fixed(ctx, repo, commit)
	if err != nil {
		return result, err
	}
	requestedPath := path
	if path != "" {
		resolved, e := s.resolvePath(ctx, p, repo, commit, path, nil)
		if e != nil {
			return result, e
		}
		if resolved.Kind != "directory" {
			return result, contract.Fail("NOT_A_DIRECTORY", "Use list with a directory.", 422)
		}
		if !s.allows(resolved.Path, true) {
			return result, contract.Fail("PATH_NOT_INDEXED", "Link target is outside this snapshot coverage.", 409)
		}
		path = resolved.Path
		if path != requestedPath {
			result.Via = requestedPath
		}
	}
	spec := commit + ":" + path
	oid, err := s.run(ctx, p, 100, "rev-parse", "--verify", spec)
	if err != nil {
		var problem *contract.Error
		if !errors.As(err, &problem) || problem.Code != "GIT_OPERATION_FAILED" {
			return result, err
		}
		return result, contract.Fail("FILE_NOT_FOUND", "Directory was not found.", 404)
	}
	result.TreeOID = strings.TrimSpace(string(oid))
	typ, err := s.run(ctx, p, 100, "cat-file", "-t", result.TreeOID)
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(string(typ)) != "tree" {
		return result, contract.Fail("NOT_A_DIRECTORY", "Path is not a directory.", 422)
	}
	data, err := s.run(ctx, p, 8<<20, "ls-tree", "-z", result.TreeOID)
	if err != nil {
		return result, err
	}
	entries, err := parseEntries(data, path)
	if err != nil {
		return result, err
	}
	filtered := entries[:0]
	for _, e := range entries {
		if s.allows(e.Path, e.Kind == "directory") {
			filtered = append(filtered, e)
		}
	}
	entries = filtered
	var last string
	if after != "" {
		raw, e := base64.RawURLEncoding.DecodeString(after)
		if e != nil || len(raw) > 8192 {
			return result, contract.Fail("INVALID_CURSOR", "Invalid directory cursor.", 400)
		}
		var c treeCursor
		if e = contract.DecodeObject(raw, &c); e != nil || c.Commit != commit || c.Path != requestedPath || c.Scope != s.scopeKey() {
			return result, contract.Fail("INVALID_CURSOR", "Cursor does not match this immutable tree.", 400)
		}
		last = c.Last
		found := false
		for _, entry := range entries {
			if entry.Name == last {
				found = true
				break
			}
		}
		if !found {
			return result, contract.Fail("INVALID_CURSOR", "Cursor entry does not exist.", 400)
		}
	}
	// Git's tree order differs from byte order for directories; use explicit sort.
	sortEntries(entries)
	for _, entry := range entries {
		if after != "" && entry.Name <= last {
			continue
		}
		if len(result.Entries) == first {
			result.HasMoreResults = true
			break
		}
		result.Entries = append(result.Entries, entry)
	}
	if result.HasMoreResults {
		b, _ := json.Marshal(treeCursor{Commit: commit, Path: requestedPath, Last: result.Entries[len(result.Entries)-1].Name, Scope: s.scopeKey()})
		result.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
	sizes, err := s.blobSizes(ctx, p, result.Entries)
	if err != nil {
		return result, err
	}
	resolver := linkResolver{store: s, ctx: ctx, repo: repo, commit: commit, dir: p}
	for i := range result.Entries {
		if result.Entries[i].Kind == "symlink" {
			resolver.hops = 0
			resolved, e := resolver.resolve(result.Entries[i].Path, map[string]bool{}, 0)
			if e != nil {
				code := LinkErrorCode(e)
				if code == "" {
					return result, e
				}
				result.Entries[i].FollowError = code
			} else if !s.allows(resolved.Path, true) {
				result.Entries[i].FollowError = "PATH_NOT_INDEXED"
			} else {
				result.Entries[i].Target = resolved.Path
				result.Entries[i].TargetKind = resolved.Kind
			}
		}
		if size, ok := sizes[result.Entries[i].BlobOID]; ok {
			result.Entries[i].Size = &size
		}
	}
	return result, nil
}
func parseEntries(data []byte, parent string) ([]Entry, error) {
	result := []Entry{}
	for _, row := range bytes.Split(data, []byte{0}) {
		if len(row) == 0 {
			continue
		}
		head, name, ok := bytes.Cut(row, []byte{'\t'})
		if !ok || !utf8.Valid(name) {
			return nil, contract.Fail("UNSUPPORTED_PATH_ENCODING", "Git tree path cannot be represented.", 422)
		}
		fields := strings.Fields(string(head))
		if (len(fields) != 4 && len(fields) != 3) || !fullSHA.MatchString(fields[2]) {
			return nil, contract.Fail("INVALID_GIT_RESULT", "Malformed tree response.", 500)
		}
		path := string(name)
		if parent != "" {
			path = parent + "/" + path
		}
		if err := ValidatePath(path, false); err != nil {
			return nil, contract.Fail("UNSUPPORTED_PATH_ENCODING", "Git tree path cannot be represented.", 422)
		}
		e := Entry{Name: string(name), Path: path, BlobOID: fields[2], Kind: "file"}
		switch fields[0] {
		case "040000":
			e.Kind = "directory"
		case "120000":
			e.Kind = "symlink"
		case "160000":
			e.Kind = "submodule"
		}
		if len(fields) == 4 && fields[3] != "-" {
			size, err := strconv.ParseInt(fields[3], 10, 64)
			if err != nil || size < 0 {
				return nil, contract.Fail("INVALID_GIT_RESULT", "Invalid object size.", 500)
			}
			e.Size = &size
		}
		result = append(result, e)
	}
	return result, nil
}
func (s *Store) entry(ctx context.Context, p, commit, path string) (Entry, error) {
	return s.lookupEntry(ctx, p, commit, path, true)
}
func (s *Store) treeEntry(ctx context.Context, p, commit, path string) (Entry, error) {
	return s.lookupEntry(ctx, p, commit, path, false)
}
func (s *Store) lookupEntry(ctx context.Context, p, commit, path string, sizes bool) (Entry, error) {
	args := []string{"ls-tree", "-z"}
	if sizes {
		args = append(args, "-l")
	}
	args = append(args, commit, "--", path)
	data, err := s.run(ctx, p, 16384, args...)
	if err != nil {
		return Entry{}, err
	}
	entries, err := parseEntries(data, "")
	if err != nil {
		return Entry{}, err
	}
	for _, e := range entries {
		if e.Path == path {
			return e, nil
		}
	}
	return Entry{}, contract.Fail("FILE_NOT_FOUND", "File was not found.", 404)
}
