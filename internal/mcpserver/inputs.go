package mcpserver

import "github.com/google/jsonschema-go/jsonschema"
import "github.com/context4ai/sourcegraph/internal/wasmplugin"

type repositoriesInput struct {
	After string `json:"after,omitempty" jsonschema:"Previous response next_cursor; omit for first page. Page size is fixed at 100."`
}

type availabilityInput struct {
	JobID          string `json:"job_id,omitempty" jsonschema:"Inspect one retained preparation task by job_id, including its terminal result. Omit for active/latest tasks."`
	IncludePlugins bool   `json:"include_plugins,omitempty" jsonschema:"Optionally list plugin files and advisory metadata without executing them."`
	Revision       string `json:"revision,omitempty" jsonschema:"Version for optional plugin discovery; omitted uses the default branch."`
	Repo           string `json:"repo" jsonschema:"Canonical namespace/repository, for example context4ai/context; not a URL."`
}

type resolveInput struct {
	Repo     string `json:"repo" jsonschema:"Canonical namespace/repository, for example context4ai/context; not a URL."`
	Revision string `json:"revision,omitempty" jsonschema:"Configured branch or eligible full 40-character lowercase SHA; omit for the default described in server instructions."`
}

type searchInput struct {
	Repo            string   `json:"repo" jsonschema:"Canonical namespace/repository, for example context4ai/context; not a URL."`
	Revision        string   `json:"revision,omitempty" jsonschema:"Configured branch or eligible full 40-character lowercase SHA; omit for the default described in server instructions."`
	Pattern         string   `json:"pattern" jsonschema:"Regular expression (RE2, close to rg's default syntax), matched line by line against file contents, never paths. At most 8192 bytes. Example: WithTimeout\\(|context\\.WithCancel"`
	FixedStrings    bool     `json:"fixed_strings,omitempty" jsonschema:"Like rg -F: match pattern as literal text."`
	IgnoreCase      bool     `json:"ignore_case,omitempty" jsonschema:"Like rg -i. Default false: case-sensitive."`
	Glob            []string `json:"glob,omitempty" jsonschema:"Like rg -g: include files matching any glob, e.g. *.go or src/**/*.ts; prefix ! to exclude, e.g. !**/*_test.go or !vendor. A glob without / matches names at any depth."`
	Paths           []string `json:"paths,omitempty" jsonschema:"Like rg's PATH arguments: repository-relative directories within registered coverage. Omit to search all registered groups at the same commit; missing group observations return SCOPE_NOT_READY, missing searchable indexes INDEX_NOT_READY; never mixed versions."`
	Output          string   `json:"output,omitempty" jsonschema:"content (default) returns matching lines; files returns only matched paths, like rg -l."`
	MaxLinesPerFile int      `json:"max_lines_per_file,omitempty" jsonschema:"Maximum displayed matching lines per file, 1-1000; default 10. Ignored for output=files. Omitted lines are marked."`
}

type readInput struct {
	Plugins   []wasmplugin.Request `json:"plugins,omitempty" jsonschema:"Optional read enhancements, e.g. [{name: context-evidence}]. Omit to use connection/Wasm defaults; [] disables enhancements. Connection plugins=off always disables. Plugin failures preserve original content."`
	Repo      string               `json:"repo" jsonschema:"Canonical namespace/repository, for example context4ai/context; not a URL."`
	Revision  string               `json:"revision,omitempty" jsonschema:"Configured branch or eligible full 40-character lowercase SHA; omit for the default described in server instructions."`
	Path      string               `json:"path" jsonschema:"Repository-relative file path."`
	StartLine *int                 `json:"start_line,omitempty" jsonschema:"Inclusive first line; default 1."`
	EndLine   *int                 `json:"end_line,omitempty" jsonschema:"Inclusive last line; omitted means start_line + 499. Returns at most 500 lines; larger ranges are paged with continuation fields."`
}

type readManyFileInput struct {
	Path      string `json:"path" jsonschema:"Repository-relative file path."`
	StartLine *int   `json:"start_line,omitempty" jsonschema:"Inclusive first line; default 1."`
	EndLine   *int   `json:"end_line,omitempty" jsonschema:"Inclusive last line; default start_line + 499, returns at most 500 lines per item, with continuation for larger ranges."`
}

type readManyInput struct {
	Plugins  []wasmplugin.Request `json:"plugins,omitempty" jsonschema:"Omit for connection/Wasm defaults; [] disables. Explicit list selects plugins, unless connection plugins=off. Runs once per scope over returned files."`
	Repo     string               `json:"repo" jsonschema:"Canonical namespace/repository; not a URL."`
	Revision string               `json:"revision,omitempty" jsonschema:"Configured branch or eligible full 40-character lowercase SHA; omit for the default described in server instructions. Resolved once for the entire batch."`
	Files    []readManyFileInput  `json:"files" jsonschema:"1-10 file ranges, processed in this order with a shared 1000-line and 256-KiB content budget. Check each returned item for errors and continuation."`
}

type listInput struct {
	Repo     string `json:"repo" jsonschema:"Canonical namespace/repository, for example context4ai/context; not a URL."`
	Revision string `json:"revision,omitempty" jsonschema:"Configured branch or eligible full 40-character lowercase SHA; omit for the default described in server instructions."`
	Path     string `json:"path,omitempty" jsonschema:"Repository-relative directory; omit or empty for root."`
	First    int    `json:"first" jsonschema:"Page size 1-1000; required, no default."`
	After    string `json:"after,omitempty" jsonschema:"next_cursor from the previous response for the same commit and path."`
}

type diffInput struct {
	Paths []string `json:"paths,omitempty" jsonschema:"Optional registered repository-relative directories to compare; omit for all registered coverage."`
	Repo  string   `json:"repo" jsonschema:"Canonical namespace/repository, for example context4ai/context; not a URL."`
	Base  string   `json:"base" jsonschema:"Explicit configured branch or eligible full 40-character lowercase SHA. Resolve once and pin the SHA; no default HEAD."`
	Head  string   `json:"head" jsonschema:"Explicit configured branch or eligible full 40-character lowercase SHA. Resolve once and pin the SHA; no default HEAD."`
	First int      `json:"first" jsonschema:"Page size 1-100; required, no default."`
	After string   `json:"after,omitempty" jsonschema:"next_cursor from the previous response for the same base/head commits."`
}

type prepareInput struct {
	Group    string `json:"group,omitempty" jsonschema:"Optional registered path-group name from availability. Omit to prepare all groups; full indexing has no group. Response includes the previous completed index duration when known, for reference only."`
	Repo     string `json:"repo" jsonschema:"Canonical namespace/repository, for example context4ai/context; not a URL."`
	Revision string `json:"revision,omitempty" jsonschema:"Omit to synchronize and index the default branch; alternatively specify a monitored branch or a full 40-character lowercase SHA within retention. Work is asynchronous; poll availability and reuse the job commit once resolved."`
	Key      string `json:"idempotency_key" jsonschema:"Stable operation key, 8-128 bytes, no CR/LF/NUL. Reuse for retries; a terminal job needs a new logical operation key to retry work."`
}

// Infer required fields from non-omitempty tags, then add discoverable bounds.
// Cross-field, byte-size and policy checks remain in the shared service.
func inputSchema[In any](name string) *jsonschema.Schema {
	schema, err := jsonschema.For[In](nil)
	if err != nil {
		panic(err)
	} // Static programmer error; never request-controlled.
	for _, field := range schema.Required {
		p := schema.Properties[field]
		if p.Type == "string" {
			p.MinLength = ptr(1)
		}
	}
	for _, field := range []string{"start_line", "end_line", "first"} {
		if p := schema.Properties[field]; p != nil {
			p.Minimum = ptr(float64(1))
		}
	}
	if p := schema.Properties["first"]; p != nil {
		limit := float64(1000)
		if name == "diff" {
			limit = 100
		}
		p.Maximum = &limit
	}
	if name == "search" {
		schema.Properties["output"].Enum = []any{"content", "files"}
		schema.Properties["max_lines_per_file"].Minimum = ptr(float64(1))
		schema.Properties["max_lines_per_file"].Maximum = ptr(float64(1000))
	}
	if name == "read_many" {
		files := schema.Properties["files"]
		files.MinItems, files.MaxItems = ptr(1), ptr(10)
		// Per-item ranges are validated by the shared service so one bad file
		// does not suppress valid evidence from other items in the batch.
	}
	if name == "prepare" {
		key := schema.Properties["idempotency_key"]
		// JSON Schema counts characters; the service enforces the 8–128 byte bound.
		key.MinLength, key.MaxLength = ptr(1), ptr(128)
		key.Pattern = `^[^\r\n\x00]+$`
	}
	return schema
}
func ptr[T any](v T) *T { return &v }
