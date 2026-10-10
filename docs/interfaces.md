# Interfaces

- Browser: `/sourcegraph/`
- Health: `/sourcegraph/healthz`
- MCP: `/sourcegraph/mcp` (Streamable HTTP, stateless JSON responses)
- HTTP: `/sourcegraph/v1/`; administration under `/sourcegraph/api/admin/`

Use MCP discovery to obtain the current tool input schemas. Repository names use `owner/repository`, for example `context4ai/context`. Reads and searches resolve to a fixed commit; index preparation is asynchronous. Anonymous MCP exposes read-only operations for public repositories. Administrative mutations require authorization.

For an MCP client supporting Streamable HTTP:

```json
{"mcpServers":{"sourcegraph":{"url":"https://context4ai-sourcegraph.fly.dev/sourcegraph/mcp"}}}
```

The CLI is built to `output/bin/sourcegraph-cli`; run it with `--help` for command-specific options. Use the website's Connect page for current HTTP and client examples. Personal API keys are created from a signed-in user's settings; prepare permission is additionally checked against the current administrator role. Invalid bearer credentials never silently become anonymous access.
# Unsupported Git paths

Indexing and directory enumeration skip Git filenames that cannot be addressed by the path API: invalid UTF-8, control characters, backslashes, paths over 4096 bytes, or noncanonical relative paths. Names are never rewritten, and other files remain available. Search reports `Meta.Coverage.UnsupportedPathsSkipped: true` for affected new indexes, including an MCP text warning; directory results use the same flag for excluded entries. An absent flag on an older index does not prove full coverage. Direct reads still validate paths. Diff still returns an error for unrepresentable changed paths rather than silently reporting no changes.
