# Interfaces

All routes live under `/sourcegraph/`. Repository names use `owner/repository`, for example `context4ai/context`. Reads and searches resolve to a fixed commit; index preparation is asynchronous. Anonymous access exposes read-only operations for public repositories. Administrative mutations require authorization, and invalid bearer credentials never silently become anonymous access.

| Surface | Entry | Operations |
| --- | --- | --- |
| Browser | `/sourcegraph/` | Search, code reader, repositories, Connect page |
| MCP | `/sourcegraph/mcp` (Streamable HTTP, stateless JSON responses) | `repositories`, `availability`, `resolve`, `search`, `read`, `read_many`, `list`, `diff`, `prepare` |
| HTTP | Routes below | Same operations except `read_many`; read several ranges with repeated `read` calls |
| CLI | `output/bin/sourcegraph-cli` | `repositories`, `availability`, `resolve`, `search`, `read`, `list`, `diff`, `prepare` |

Use MCP discovery to obtain the current tool input schemas. `GET /sourcegraph/v1/capabilities` lists the HTTP operations.

## Code reader links

```text
/sourcegraph/code?repo=owner%2Frepository&path=src%2Fmain.go&commit=<full SHA>#L20-L35
```

Use `revision=<branch or tag>` instead of `commit`, or omit both for the default branch; the reader resolves it to a fixed commit and rewrites the address to `commit=<full SHA>&branch=<branch>`. A `commit` value that is not a full SHA, such as `commit=main`, is read as a revision. A pinned SHA outside retention is never replaced silently: when the same path exists on the latest default-branch commit, the reader offers to open it there and states which commit it replaced, since line numbers may differ.

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
| `GET /sourcegraph/v1/code-sources` | Configured code sources in display order: `id/name/host/username/anonymous/editable/locked` |
| `GET /sourcegraph/v1/repo/availability` | Indexed commits and readiness; `/v1/repo/commits` is an alias |
| `GET /sourcegraph/v1/repo/plugins` | Read plugins available for a revision |
| `POST /sourcegraph/v1/repo/resolve` | Resolve a branch or SHA to a fixed commit |
| `POST /sourcegraph/v1/repo/read` | Read a line range |
| `POST /sourcegraph/v1/repo/list` | List a directory |
| `POST /sourcegraph/v1/repo/diff` | Changed paths between two commits |
| `POST /sourcegraph/api/search` | Search, with the same options as the CLI |
| `POST /sourcegraph/api/admin/v1/repo/prepare` | Queue indexing or synchronization |

Repository registration, maintenance, statistics and site settings live under `/sourcegraph/api/admin/` and are used by the website's administration pages. Use the Connect page for current request examples.

### Repository managers

Site administrators manage every repository. The site setting **Let signed-in users register repositories** (`access.allow_user_repositories`) defaults to off; older settings without the field read as off, so upgrading does not change who can register. When it is on, any signed-in user can register a repository and becomes its only manager. Repositories registered before managers existed treat their creator as manager until the list is first edited; after that the stored list is used even when empty, leaving management to administrators. A repository has at most 50 managers.

Repository managers and administrators can use every `?repo=` management route on that repository: edit settings, preview, prepare and add indexes, list commit options, clean up, cancel jobs, delete and purge, and add or remove managers, including themselves. Others receive `403 REPOSITORY_MANAGEMENT_DENIED`; registration while the setting is off returns `403 REGISTRATION_DISABLED`. Users, site settings, global code-source tokens and other site-wide routes stay administrator-only, and personal API keys keep their read-only scope.

| Route | Purpose |
| --- | --- |
| `POST /sourcegraph/api/admin/v1/repo/managers?repo=` | Body `{"add":"<user id>"}` or `{"remove":"<user id>"}` with `If-Match: "<revision>"`; the added user must exist and be enabled (`404 USER_NOT_FOUND`) |
| `GET /sourcegraph/auth/users/search?q=` | Signed-in users find up to 20 enabled users by name or email (`id/name/email/department`) to choose a manager |

Repository details (`/sourcegraph/v1/repo/availability`, `GET /sourcegraph/api/admin/v1/repo`) add `can_manage`, and for signed-in callers `creator` and `managers` with `id/name/email/department`. Anonymous public reads see neither. `GET /sourcegraph/access-policy` adds `user_repositories`.

Fetching a user-registered repository uses the same credentials as any other: the creator's own code-source token first, then the site-wide token. Turning the setting on therefore lets signed-in users register any repository the site-wide token can read. Fetch failures name the cause (`GIT_REPOSITORY_UNAVAILABLE` for a missing repository or one the token cannot see, `GIT_ACCESS_DENIED`, `GIT_AUTH_FAILED`, `GIT_CREDENTIAL_UNAVAILABLE`, `GIT_FETCH_FAILED`), and the website links to the creator's code-source token settings.

### Code sources

Code sources, their settings and token order are described in [code sources and tokens](code-sources.md).

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

## Source identity and recovery

Carry repository, original full SHA and path together. Do not reconstruct SHAs or reuse them across repositories. Group `read_many` calls by `(repo, revision)` and use paths returned by search/list. `end_line` is an inclusive absolute line number, not a count.

`REVISION_NOT_ELIGIBLE` means the revision is absent from this repository's currently observed versions. Verify the repository/SHA pair, monitored branches, synchronization and retention before assuming expiry; never substitute another baseline.

HTTP availability includes bounded historical `job_failures` summaries; see [diagnostic retention](deployment.md#task-and-query-diagnostics). MCP availability with a specific `job_id` returns `job_failure` and `job_lookup.state=archived_failure` when the live task was pruned but its failure summary remains. It is historical evidence, not current readiness or a runnable task. Missing history does not imply success.
