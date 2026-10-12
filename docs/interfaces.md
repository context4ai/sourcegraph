# Interfaces

All routes live under `/sourcegraph/`. Repository names use `owner/repository`, for example `context4ai/context`. Reads and searches resolve to a fixed commit; index preparation is asynchronous. Anonymous access exposes read-only operations for public repositories. Administrative mutations require authorization, and invalid bearer credentials never silently become anonymous access.

| Surface | Entry | Operations |
| --- | --- | --- |
| Browser | `/sourcegraph/` | Search, code reader, repositories, Connect page |
| MCP | `/sourcegraph/mcp` (Streamable HTTP, stateless JSON responses) | `repositories`, `availability`, `resolve`, `search`, `read`, `read_many`, `list`, `diff`, `prepare` |
| HTTP | Routes below | Same operations except `read_many`; read several ranges with repeated `read` calls |
| CLI | `output/bin/sourcegraph-cli` | `repositories`, `availability`, `resolve`, `search`, `read`, `list`, `diff`, `prepare` |

Use MCP discovery to obtain the current tool input schemas. `GET /sourcegraph/v1/capabilities` lists the HTTP operations.

## MCP

For an MCP client supporting Streamable HTTP:

```json
{"mcpServers":{"sourcegraph":{"url":"https://context4ai.org/sourcegraph/mcp"}}}
```

`read_many` reads several files or line ranges in one call. Repository read plugins enrich `read` and `read_many`; see [repository plugins](ref/repository-plugins.md).

## HTTP

Repository routes take `?repo=owner/repository`.

| Method and path | Purpose |
| --- | --- |
| `GET /sourcegraph/healthz`, `GET /sourcegraph/readyz` | Liveness and readiness |
| `GET /sourcegraph/v1/capabilities` | Supported operations |
| `GET /sourcegraph/v1/repos` | Repository catalog |
| `GET /sourcegraph/v1/repo/availability` | Indexed commits and readiness; `/v1/repo/commits` is an alias |
| `GET /sourcegraph/v1/repo/plugins` | Read plugins available for a revision |
| `POST /sourcegraph/v1/repo/resolve` | Resolve a branch or SHA to a fixed commit |
| `POST /sourcegraph/v1/repo/read` | Read a line range |
| `POST /sourcegraph/v1/repo/list` | List a directory |
| `POST /sourcegraph/v1/repo/diff` | Changed paths between two commits |
| `POST /sourcegraph/api/search` | Search, with the same options as the CLI |
| `POST /sourcegraph/api/admin/v1/repo/prepare` | Queue indexing or synchronization |

Repository registration, maintenance, statistics and site settings live under `/sourcegraph/api/admin/` and are used by the website's administration pages. Use the Connect page for current request examples.

## CLI

The CLI calls the HTTP routes above. Flags precede the pattern, as with `rg`:

```bash
sourcegraph-cli search -repo context4ai/context [-revision REV] [-F] [-i] [-l] [-g GLOB]... PATTERN [PATH...]
sourcegraph-cli read -repo context4ai/context -path README.md -start-line 1 -end-line 40
```

| Flag | Purpose |
| --- | --- |
| `-repo`, `-revision` | Repository and full SHA or configured branch |
| `-e`, `-F`, `-i`, `-l`, `-g` | Search pattern, literal text, ignore case, paths only, include or `!exclude` glob (repeatable) |
| `-path`, `-start-line`, `-end-line` | Read target and range (default lines 1–100) |
| `-first`, `-after` | Page size and cursor for `list` and `diff` |
| `-base`, `-head` | Commits for `diff` |
| `-kind`, `-key` | `prepare` kind (`index` or `sync`) and idempotency key |

| Environment variable | Purpose |
| --- | --- |
| `SOURCEGRAPH_URL` | Service URL, default `https://context4ai.org/sourcegraph`; must be a credential-free HTTPS URL |
| `SOURCEGRAPH_API_TOKEN` | Optional bearer credential, at least 32 characters |

Personal API keys are created from a signed-in user's settings; prepare permission is additionally checked against the current administrator role.

## Unsupported Git paths

Indexing and directory enumeration skip Git filenames that cannot be addressed by the path API: invalid UTF-8, control characters, backslashes, paths over 4096 bytes, or noncanonical relative paths. Names are never rewritten, and other files remain available. Search reports `Meta.Coverage.UnsupportedPathsSkipped: true` for affected new indexes, including an MCP text warning; directory results use the same flag for excluded entries. An absent flag on an older index does not prove full coverage. Direct reads still validate paths. Diff still returns an error for unrepresentable changed paths rather than silently reporting no changes.
