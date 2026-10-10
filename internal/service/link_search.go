package service

import (
	"encoding/json"
	"sort"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/gitstore"
	"github.com/context4ai/sourcegraph/internal/zoektclient"
)

func canonicalSearchFiles(native map[string]any, targets []zoektclient.Target, scopes []selectedScope) error {
	manifests := map[string]*gitstore.LinkSnapshot{}
	objects := map[string]map[string]string{}
	for i, t := range targets {
		manifests[t.EngineName()] = scopes[i].version.Links
		ids, err := linkObjects(scopes[i].version.Links)
		if err != nil {
			return err
		}
		objects[t.EngineName()] = ids
	}
	files, _ := native["Files"].([]any)
	merged := map[string]map[string]any{}
	order := []string{}
	mergedAny := false
	for _, value := range files {
		f, ok := value.(map[string]any)
		if !ok {
			return contract.Fail("INVALID_ENGINE_RESULT", "Invalid search file.", 502)
		}
		name, _ := f["FileName"].(string)
		engine, _ := f["Repository"].(string)
		manifest := manifests[engine]
		// Some engine adapters omit Repository for a single target.
		if manifest == nil && len(targets) == 1 {
			manifest = scopes[0].version.Links
			engine = targets[0].EngineName()
		}
		oid := objects[engine][name]
		if manifest != nil {
			if link, ok := manifest.Links[name]; ok {
				if link.FollowError != "" || link.TargetKind != "file" {
					return contract.Fail("ENGINE_SCOPE_MISMATCH", "Engine returned an unexpanded link.", 502)
				}
				f["Via"] = []string{name}
				name = link.Target
				f["FileName"] = name
				oid = link.BlobOID
			}
		}
		f["Repository"] = targets[0].Repo
		delete(f, "RepositoryID")
		if previous, ok := merged[name]; ok {
			mergedAny = true
			if before, _ := previous["linkOID"].(string); before != "" && oid != "" && before != oid {
				return contract.Fail("ENGINE_SCOPE_MISMATCH", "Canonical path has conflicting snapshot objects.", 502)
			}
			aliases, _ := previous["Via"].([]string)
			more, _ := f["Via"].([]string)
			aliases = append(aliases, more...)
			sort.Strings(aliases)
			unique := []string{}
			for _, a := range aliases {
				if len(unique) == 0 || unique[len(unique)-1] != a {
					unique = append(unique, a)
				}
			}
			if len(unique) > 0 {
				previous["Via"] = unique
			}
			lines, _ := previous["LineMatches"].([]any)
			extra, _ := f["LineMatches"].([]any)
			joined := mergeLinkLines(append(lines, extra...))
			if lines != nil || extra != nil {
				previous["LineMatches"] = joined
			}
			if oid != "" {
				previous["linkOID"] = oid
			}
		} else {
			f["linkOID"] = oid
			merged[name] = f
			order = append(order, name)
		}
	}
	result := []any{}
	matches := 0
	for _, name := range order {
		f := merged[name]
		delete(f, "linkOID")
		result = append(result, f)
		lines, _ := f["LineMatches"].([]any)
		for _, l := range lines {
			m, _ := l.(map[string]any)
			fragments, _ := m["LineFragments"].([]any)
			matches += len(fragments)
		}
	}
	native["Files"] = result
	if mergedAny {
		native["FileCount"] = len(result)
		hasLines := false
		for _, f := range merged {
			if _, ok := f["LineMatches"]; ok {
				hasLines = true
				break
			}
		}
		if hasLines {
			native["MatchCount"] = matches
		} else {
			delete(native, "MatchCount")
		}
	}
	return nil
}
func linkCoverage(scopes []selectedScope) map[string]any {
	followed := 0
	unsupportedPaths := false
	complete := true
	skipped := map[string]int{}
	for _, scope := range scopes {
		m := scope.version.Links
		if m != nil && m.UnsupportedPathsSkipped {
			unsupportedPaths = true
		}
		if m != nil && m.Followed {
			followed++
			for reason, n := range m.Skipped {
				skipped[reason] += n
				if n > 0 {
					complete = false
				}
			}
		} else {
			complete = false
		}
	}
	mode := "mixed"
	if followed == 0 {
		mode = "not_followed"
	} else if followed == len(scopes) {
		mode = "followed"
	}
	coverage := map[string]any{"Symlinks": mode, "SymlinkExpansionComplete": complete, "SymlinkSkipped": skipped}
	if unsupportedPaths {
		coverage["UnsupportedPathsSkipped"] = true
	}
	return coverage
}

func mergeLinkLines(lines []any) []any {
	out := []any{}
	byLine := map[string]map[string]any{}
	for _, value := range lines {
		line, ok := value.(map[string]any)
		if !ok {
			continue
		}
		key, _ := json.Marshal([]any{line["LineNumber"], line["Line"], line["FileName"]})
		if previous, ok := byLine[string(key)]; ok {
			existing, _ := previous["LineFragments"].([]any)
			extra, _ := line["LineFragments"].([]any)
			seen := map[string]bool{}
			fragments := []any{}
			for _, f := range append(existing, extra...) {
				raw, _ := json.Marshal(f)
				if !seen[string(raw)] {
					seen[string(raw)] = true
					fragments = append(fragments, f)
				}
			}
			sort.SliceStable(fragments, func(i, j int) bool {
				a, _ := fragments[i].(map[string]any)
				b, _ := fragments[j].(map[string]any)
				x, _ := a["LineOffset"].(float64)
				y, _ := b["LineOffset"].(float64)
				return x < y
			})
			previous["LineFragments"] = fragments
		} else {
			byLine[string(key)] = line
			out = append(out, line)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a := out[i].(map[string]any)
		b := out[j].(map[string]any)
		x, _ := a["LineNumber"].(float64)
		y, _ := b["LineNumber"].(float64)
		return x < y
	})
	return out
}

// Derive once when a manifest enters the cache. Direct unit fixtures may omit
// this runtime-only lookup. A canonical document participates in the same check
// as its aliases; result order cannot suppress object conflicts.
func linkObjects(m *gitstore.LinkSnapshot) (map[string]string, error) {
	if m == nil {
		return nil, nil
	}
	if m.Objects != nil {
		return m.Objects, nil
	}
	out := map[string]string{}
	for _, link := range m.Links {
		if link.FollowError != "" || link.TargetKind != "file" {
			continue
		}
		if before := out[link.Target]; before != "" && link.BlobOID != "" && before != link.BlobOID {
			return nil, contract.Fail("ENGINE_SCOPE_MISMATCH", "Canonical path has conflicting snapshot objects.", 502)
		}
		if link.BlobOID != "" {
			out[link.Target] = link.BlobOID
		}
	}
	return out, nil
}
