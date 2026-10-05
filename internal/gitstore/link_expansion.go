package gitstore

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"sort"
	"strings"

	"github.com/context4ai/sourcegraph/internal/contract"
)

// LinkLimits bound traversal independently of the searchable-file budget.
type LinkLimits struct {
	Links, Files, Scanned int
	ContentBytes          int64
}

func DefaultLinkLimits() LinkLimits { return LinkLimits{1000, 2000, 100000, 64 << 20} }

// ExpandLinks adds canonical and alias entries. A directory link is admitted as
// a whole. The returned manifest is published alongside the generated shards.
func (s *Store) ExpandLinks(ctx context.Context, repo, commit string, entries []Entry, paths []string, limits LinkLimits, hydrate func([]Entry) error, indexable func(string, []byte) bool) ([]Entry, *LinkSnapshot, error) {
	snapshot := &LinkSnapshot{Format: 1, Followed: true, Paths: append([]string(nil), paths...), Links: map[string]LinkInfo{}, Skipped: map[string]int{}}
	dir, err := s.fixed(ctx, repo, commit)
	if err != nil {
		return nil, nil, err
	}
	r := linkResolver{store: s, ctx: ctx, repo: repo, commit: commit, dir: dir, hydrate: hydrate}
	out := map[string]Entry{}
	for _, e := range entries {
		if e.Kind != "symlink" {
			out[e.Path] = e
		}
	}
	scanned, links := 0, 0
	var used int64
	manifestBytes := 0
	type classifiedBlob struct {
		size      int64
		indexable bool
	}
	classified := map[string]classifiedBlob{}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	for _, entry := range entries {
		if entry.Kind != "symlink" {
			continue
		}
		stage := map[string]Entry{}
		mappings := map[string]LinkInfo{}
		targets := map[string]bool{}
		skipped := map[string]int{}
		count := 0
		var size int64
		var visit func(Entry, string, map[string]bool, int) error
		visit = func(e Entry, alias string, stack map[string]bool, depth int) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if ValidatePath(alias, false) != nil {
				return linkError("LINK_INVALID_TARGET", "Expanded alias cannot be represented as a Git path.")
			}
			scanned++
			if scanned > limits.Scanned {
				return linkError("LINK_TARGET_TOO_LARGE", "Link traversal exceeded its tree budget.")
			}
			if e.Kind == "symlink" {
				links++
				if links > limits.Links {
					return linkError("LINK_TARGET_TOO_LARGE", "Snapshot link count exceeded its budget.")
				}
				if depth >= 8 {
					return linkError("LINK_DEPTH_EXCEEDED", "Link expansion exceeds eight levels.")
				}
				r.hops = 0
				resolved, err := r.resolve(e.Path, map[string]bool{}, 0)
				if err != nil {
					return err
				}
				mappings[alias] = LinkInfo{Target: resolved.Path, TargetKind: resolved.Kind, BlobOID: resolved.BlobOID}
				if depth+r.hops > 8 {
					return linkError("LINK_DEPTH_EXCEEDED", "Link expansion exceeds eight levels.")
				}
				return visit(resolved, alias, stack, depth+r.hops)
			}
			if e.Kind == "submodule" {
				return linkError("LINK_TO_SUBMODULE", "Link traversal reached a submodule.")
			}
			if e.Kind == "directory" {
				if stack[e.Path] {
					return linkError("LINK_LOOP", "Directory link traverses an ancestor.")
				}
				// A link to an ancestor of its own source recursively includes itself.
				if e.Path == "" || strings.HasPrefix(entry.Path, e.Path+"/") {
					return linkError("LINK_LOOP", "Directory link points to an ancestor.")
				}
				stack[e.Path] = true
				defer delete(stack, e.Path)
				childMap, err := r.children(e.Path)
				if err != nil {
					return err
				}
				children := make([]Entry, 0, len(childMap))
				for _, child := range childMap {
					children = append(children, child)
				}
				sortEntries(children)
				if scanned+len(children) > limits.Scanned {
					return linkError("LINK_TARGET_TOO_LARGE", "Link directory exceeds the tree scan budget.")
				}
				// Hydrate siblings in bounded batches, not one remote round trip
				// per document. The hydrator memoizes already checked objects.
				if hydrate != nil {
					batch := []Entry{}
					for _, child := range children {
						if child.Kind == "file" || child.Kind == "symlink" {
							batch = append(batch, child)
						}
					}
					for start := 0; start < len(batch); start += 256 {
						end := min(start+256, len(batch))
						if err := hydrate(batch[start:end]); err != nil {
							return err
						}
					}
				}
				mappings[alias] = LinkInfo{Target: e.Path, TargetKind: "directory", BlobOID: e.BlobOID}
				targets[e.Path] = true
				for i, child := range children {
					if i%32 == 0 {
						pending := []Entry{}
						for _, child := range children[i:min(i+32, len(children))] {
							if child.Kind == "file" {
								if _, ok := classified[child.Path]; !ok {
									pending = append(pending, child)
								}
							}
						}
						if len(pending) > 0 {
							if err := s.VisitBlobs(ctx, repo, commit, pending, 2<<20, func(doc Entry, data []byte) error {
								classified[doc.Path] = classifiedBlob{*doc.Size, *doc.Size > 2<<20 || (!strings.ContainsRune(string(data), 0) && indexable(doc.Path, data))}
								return nil
							}); err != nil {
								return err
							}
						}

					}
					childAlias := alias + "/" + child.Name
					if err := visit(child, childAlias, stack, depth); err != nil {
						code := expansionErrorCode(err)
						if code == "" || code == "LINK_TARGET_TOO_LARGE" {
							return err
						}
						mappings[childAlias] = LinkInfo{FollowError: code}
						skipped[code]++
					}
				}
				return nil
			}
			if e.Kind != "file" {
				return nil
			}
			if hydrate != nil {
				if err := hydrate([]Entry{e}); err != nil {
					return err
				}
			}
			body, ok := classified[e.Path]
			if !ok {
				if err := s.VisitBlobs(ctx, repo, commit, []Entry{e}, 2<<20, func(doc Entry, data []byte) error {
					body = classifiedBlob{*doc.Size, *doc.Size > 2<<20 || (!strings.ContainsRune(string(data), 0) && indexable(doc.Path, data))}
					return nil
				}); err != nil {
					return err
				}
				classified[e.Path] = body
			}
			n := body.size
			mappings[alias] = LinkInfo{Target: e.Path, TargetKind: "file", BlobOID: e.BlobOID}
			targets[e.Path] = true
			if !body.indexable {
				return nil
			}
			logicalBytes := n
			if n <= 2<<20 {
				count++
				if count > limits.Files {
					return linkError("LINK_TARGET_TOO_LARGE", "Directory link has too many indexable files.")
				}
			} else {
				logicalBytes = 0
			}

			for _, name := range []string{e.Path, alias} {
				if _, ok := out[name]; ok {
					continue
				}
				if _, ok := stage[name]; ok {
					continue
				}
				size += logicalBytes
				if used+size > limits.ContentBytes {
					return linkError("LINK_TARGET_TOO_LARGE", "Link documents exceeded their byte budget.")
				}
				doc := e
				doc.CanonicalPath = e.Path
				doc.Path = name
				doc.Name = path.Base(name)
				doc.Size = &n
				stage[name] = doc
			}
			return nil
		}
		err := visit(entry, entry.Path, map[string]bool{}, 0)
		if err != nil {
			code := expansionErrorCode(err)
			if code == "" {
				return nil, nil, err
			}
			snapshot.Links[entry.Path] = LinkInfo{FollowError: code}
			snapshot.Skipped[code]++
			continue
		}
		// Reserve room for base coverage, failures and shard inventory in the
		// 32 MiB artifact. Admission is per entrance, before publication.
		encoded, _ := json.Marshal(struct {
			Links   map[string]LinkInfo
			Targets map[string]bool
		}{mappings, targets})
		if manifestBytes+len(encoded) > 24<<20 {
			snapshot.Links[entry.Path] = LinkInfo{FollowError: "LINK_TARGET_TOO_LARGE"}
			snapshot.Skipped["LINK_TARGET_TOO_LARGE"]++
			continue
		}
		manifestBytes += len(encoded)
		for code, n := range skipped {
			snapshot.Skipped[code] += n
		}
		used += size
		for name, e := range stage {
			out[name] = e
		}
		for name, m := range mappings {
			snapshot.Links[name] = m
		}
		if len(paths) > 0 {
			for name := range targets {
				snapshot.Paths = append(snapshot.Paths, name)
			}
		}
	}
	// Internal expanded coverage can exceed the public 128-path input limit.
	snapshot.Paths = compactLinkPaths(snapshot.Paths)
	result := make([]Entry, 0, len(out))
	for _, e := range out {
		result = append(result, e)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, snapshot, nil
}
func compactLinkPaths(paths []string) []string {
	sort.Strings(paths)
	out := []string{}
	for _, p := range paths {
		covered := false
		for _, q := range out {
			if p == q || strings.HasPrefix(p, q+"/") {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, p)
		}
	}
	return out
}

func expansionErrorCode(err error) string {
	if code := LinkErrorCode(err); code != "" {
		return code
	}
	var e *contract.Error
	if errors.As(err, &e) && e.Code == "GIT_HYDRATION_BUDGET_EXCEEDED" {
		return "LINK_TARGET_TOO_LARGE"
	}
	return ""
}
