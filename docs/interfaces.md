# Interfaces

All routes live under `/sourcegraph/`. Repository names use `owner/repository`, for example `context4ai/context`. Reads and searches resolve to a fixed commit; index preparation is asynchronous. Anonymous access exposes read-only operations for public repositories. Administrative mutations require authorization, and invalid bearer credentials never silently become anonymous access.

| Surface | Entry | Operations |
| --- | --- | --- |
| Browser | `/sourcegraph/` | Search, code reader, repositories, Connect page |
| MCP | `/sourcegraph/mcp` (Streamable HTTP, stateless JSON responses) | `repositories`, `availability`, `resolve`, `refs`, `search`, `read`, `read_many`, `list`, `diff`, `prepare` |
| HTTP | Routes below | Same operations as MCP except `read_many`; read several ranges with repeated `read` calls |
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

Other tools accept a monitored branch or a full SHA. To work with any other branch or a tag, find it with `refs` if needed, `resolve` it to a full SHA, and use that SHA. When `indexed` is false, a repository manager prepares that SHA first.

### Branches, tags and resolve

`resolve` takes `repo` and an optional `revision` and returns `commit/indexed/kind/source/monitored`, plus `ref/committer_time/created_at/author/title` when known and a `hint` when the commit has no index (a repository manager, or this caller if it may prepare, can prepare the SHA). It never downloads code or builds an index.

| `revision` | Resolution | `source` |
| --- | --- | --- |
| Omitted, or a monitored branch (also as `refs/heads/<name>`) | Last synchronized head; the remote is not asked, so it may lag the remote's latest commit. A bare name equal to a monitored branch is not checked for a same-named tag; pass `refs/tags/<name>` for the tag | `observed` |
| Full 40-character SHA | An observed or retained version; otherwise `REVISION_NOT_ELIGIBLE` | `observed` |
| Any other branch or tag name, `refs/heads/<name>`, `refs/tags/<name>` | The repository's ref catalog when it is fresh and not truncated; otherwise the remote is asked for exactly these names and the matching ref is downloaded to a scratch directory, without file trees, to confirm its object type and commit details, then deleted | `catalog` or `remote` |

A bare name that is both a branch and a tag returns `REF_AMBIGUOUS` listing both; pass the full form. A missing name returns `REF_NOT_FOUND`, and a tag confirmed to end at a tree or blob returns `REF_NOT_COMMIT`. Resolution uses the ref as actually downloaded, so a branch that moves between listing and download resolves to the downloaded commit; an object that cannot be verified returns a retryable `REMOTE_UNAVAILABLE`, never `REF_NOT_COMMIT`. Annotated tags resolve to their commit; `created_at/author/title` describe the tag and `committer_time` the commit. Short SHAs, `HEAD`, revision expressions and branch or tag names with characters the service does not accept (such as parentheses or spaces) are rejected with `SHORT_SHA_UNSUPPORTED` or `INVALID_REVISION`; such names still appear in `refs`. Identical concurrent remote lookups share one request, and results, including "not found", are cached for 30 seconds (at most 10,000 entries). Remote lookups run at most 2 per repository and 8 per instance, at most 64 may be running or waiting (beyond that, `REMOTE_UNAVAILABLE` with `Retry-After`), and they back off per repository after failures, from 10 seconds up to 5 minutes. Visibility is checked before every cached answer.

`refs` lists a registered repository's branches or tags. Optional arguments: `kind` (`branch` default, `tag`, `all`); `query`, `author` and `title`, case-insensitive substrings of the name, the author or tagger, and the commit title or tag message first line; `since/until` (RFC 3339 or `YYYY-MM-DD`, compared with `created_at`); `sort` (`created`, newest first, the default, or `name`); `first` (1–100, default 30); `after` (the previous `next_cursor`, with the other arguments unchanged). It returns `repo/catalog/sort/refs/next_cursor/has_more` and sometimes a `hint`. Each entry has `name/ref/kind/monitored/indexed`; entries verified to end at a commit add `commit/created_at/committer_time/author/title`, annotated tags add `annotated`, and tags of trees, blobs or unverifiable objects carry `object/target_type` instead of `commit`. `created_at` is the commit time for branches and lightweight tags and the tagging time for annotated tags.

`catalog.state` is `ready`; `stale` (expired, or its refresh failed: the last successful catalog is returned with its `refreshed_at` while a refresh runs in the background); or `names_only`, with `catalog.reason` `filter_unsupported` (the server cannot send commit objects without trees), `ref_limit` (more than 200,000 branches or tags) or `size_limit` (the download exceeded 256 MiB). A `names_only` catalog sorts by name when `sort` is omitted; an explicit `author/title/since/until` or `sort=created` returns `REFS_FILTER_UNAVAILABLE`. `catalog.truncated: true` means only the first 200,000 names are listed and `query` searches only those; `resolve` with a complete name does not depend on the catalog. The cursor is an offset, so a refresh between pages can repeat or skip a few entries.

Catalogs live in service memory. A refresh lists the remote first, so the ref limit applies before anything is downloaded, then downloads branch and tag commit and tag objects, without file trees, into a scratch directory that is parsed and deleted. A catalog stays fresh for 5 minutes or three times its last refresh duration, whichever is longer; an expired catalog is served while one background refresh per repository runs (at most 4 per instance). Every download is checked each second against its size limit (256 MiB for a catalog, 16 MiB for a resolve lookup), a 4 GiB total for all scratch downloads and the instance free-space reserve, and is stopped and deleted when any is exceeded; scratch directories left by a stopped process are removed at startup. When catalogs together exceed 1,000,000 refs, the least recently used ones are dropped.

Without any catalog, a request waits for the first refresh within its remaining deadline less a 2-second margin and otherwise returns `REFS_CATALOG_PREPARING` with `Retry-After` while the refresh continues. A failed refresh without an earlier catalog returns the remote error; with one, the stale catalog is returned. Listing never indexes.

| Error | Meaning |
| --- | --- |
| `REF_NOT_FOUND`, `REF_AMBIGUOUS`, `REF_NOT_COMMIT` | No such branch or tag (names are case-sensitive; search with `refs`); a branch and a tag share the name; the tag does not end at a commit |
| `REFS_CATALOG_PREPARING` | The first listing is still being prepared; retry after `Retry-After`, or resolve a complete name meanwhile |
| `REFS_FILTER_UNAVAILABLE` | The catalog has names only; filter and sort by name |
| `REMOTE_ACCESS_DENIED` | The code-source token this repository uses cannot read its remote; a repository manager should check it. The caller's own key is not the cause |
| `REMOTE_UNAVAILABLE` | The remote is unavailable, timed out or backing off; retry after `Retry-After` |
| `INSUFFICIENT_STORAGE` | The instance's free disk space is below its reserve; not the repository budget |
| `BRANCH_NOT_MONITORED` | Returned by tools other than `resolve`, and by prepare, for an unmonitored branch; resolve it to a SHA first |

## HTTP

Repository routes take `?repo=owner/repository`.

| Method and path | Purpose |
| --- | --- |
| `GET /sourcegraph/healthz`, `GET /sourcegraph/readyz` | Liveness and readiness |
| `GET /sourcegraph/v1/capabilities` | Supported operations |
| `GET /sourcegraph/v1/repos` | Repository catalog; signed-in callers also get each repository's `managers` |
| `GET /sourcegraph/v1/code-sources` | Configured code sources in display order: `id/name/host/username/anonymous/editable/locked` |
| `GET /sourcegraph/v1/repo/availability` | Indexed commits and readiness; `/v1/repo/commits` is an alias |
| `GET /sourcegraph/v1/repo/plugins` | Read plugins available for a revision |
| `POST /sourcegraph/v1/repo/resolve` | Resolve a branch, tag or SHA to a fixed commit; see [Branches, tags and resolve](#branches-tags-and-resolve) |
| `GET /sourcegraph/v1/repo/refs` | List and search branches and tags (`kind/query/author/title/since/until/sort/first/after`) |
| `POST /sourcegraph/v1/repo/read` | Read a line range |
| `POST /sourcegraph/v1/repo/list` | List a directory |
| `POST /sourcegraph/v1/repo/diff` | Changed paths between two commits |
| `POST /sourcegraph/api/search` | Search, with the same options as the CLI |
| `POST /sourcegraph/api/admin/v1/repo/prepare` | Queue indexing or synchronization |

Repository registration, maintenance, statistics and site settings live under `/sourcegraph/api/admin/` and are used by the website's administration pages. Use the Connect page for current request examples.

### Repository visibility

Each repository is public or private (`public_read`, default `true`; repositories registered before the field existed stay public). Anonymous callers read public repositories only, and only while the site allows public reading. A private repository is visible only to its creator, its managers, site administrators signed in on the website and the deployment credential. Personal API keys read as their owner: public repositories plus the private ones the owner created or manages, even when the owner is a site administrator. Every other caller gets the same `404 REPOSITORY_NOT_FOUND` as for an unregistered name from details, search, read, list, diff, plugins, prepare and MCP tools, and catalogs list only visible repositories. Statistics omit all repository names for anonymous callers; other callers without site-wide read keep only registered repositories they can see plus the `_overflow` aggregate, so unregistered, deleted and hidden names look the same. Registering an existing name returns `409 REPOSITORY_EXISTS` with its registrant in `creator` (`id/name/email/department`); the website suggests asking them to be added as a manager, or a site administrator when no registrant is recorded. Background synchronization, indexing and cleanup are not narrowed by visibility.

### Repository managers

Site administrators manage every repository. The site setting **Let signed-in users register repositories** (`access.allow_user_repositories`) defaults to off; older settings without the field read as off, so upgrading does not change who can register. When it is on, any signed-in user can register a repository and becomes its only manager. Repositories registered before managers existed treat their creator as manager until the list is first edited; after that the stored list is used even when empty, leaving management to administrators. A repository has at most 50 managers.

Repository managers and administrators can use every `?repo=` management route on that repository: edit settings, preview, prepare and add indexes, list commit options, clean up, cancel jobs, delete and purge, and add or remove managers, including themselves. Others receive `403 REPOSITORY_MANAGEMENT_DENIED`; registration while the setting is off returns `403 REGISTRATION_DISABLED`. Users, site settings, global code-source tokens and other site-wide routes stay administrator-only. Personal API keys read what their owner reads (see [Repository visibility](#repository-visibility)) and may call only the prepare route besides that; a site administrator's key prepares only repositories it can see.

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
| `-repo`, `-revision` | Repository, and a monitored branch or full SHA; `resolve` also accepts any branch or tag name and `refs/heads/<name>` or `refs/tags/<name>` |
| `-e`, `-F`, `-i`, `-l`, `-g` | Search pattern, literal text, ignore case, paths only, include or `!exclude` glob (repeatable) |
| `-path`, `-start-line`, `-end-line` | Read target and range (default lines 1–100) |
| `-first`, `-after` | Page size and cursor for `list` and `diff` |
| `-base`, `-head` | Commits for `diff` |
| `-kind`, `-key` | `prepare` kind (`index` or `sync`) and idempotency key |

| Environment variable | Purpose |
| --- | --- |
| `SOURCEGRAPH_URL` | Service URL, default `https://context4ai.org/sourcegraph`; must be a credential-free HTTPS URL |
| `SOURCEGRAPH_API_TOKEN` | Optional bearer credential, at least 32 characters |

Personal API keys are created from a signed-in user's settings. A key can prepare (MCP `prepare` or `POST /api/admin/v1/repo/prepare`) a repository its owner manages, or any repository it can see when its owner is currently a site administrator; this is checked on every call. MCP lists `prepare` only for keys whose owner is an administrator or manages at least one repository (refreshed within a minute). Keys are limited to 10 prepare calls a minute.

Manual preparation is paced per repository across the website, HTTP and MCP. A request identical to a queued or running job (same group, kind and revision) returns that job and adds no queue load. Any other new request within 30 seconds of the repository's last accepted manual request returns `429 RATE_LIMITED` with `Retry-After`; one request queuing every monorepo group is a single request. Scheduled work is not paced.

## Unsupported Git paths

Indexing and directory enumeration skip Git filenames that cannot be addressed by the path API: invalid UTF-8, control characters, backslashes, paths over 4096 bytes, or noncanonical relative paths. Names are never rewritten, and other files remain available. Search reports `Meta.Coverage.UnsupportedPathsSkipped: true` for affected new indexes, including an MCP text warning; directory results use the same flag for excluded entries. An absent flag on an older index does not prove full coverage. Direct reads still validate paths. Diff still returns an error for unrepresentable changed paths rather than silently reporting no changes.

## Source identity and recovery

Carry repository, original full SHA and path together. Do not reconstruct SHAs or reuse them across repositories. Group `read_many` calls by `(repo, revision)` and use paths returned by search/list. `end_line` is an inclusive absolute line number, not a count.

`REVISION_NOT_ELIGIBLE` means the revision is absent from this repository's currently observed versions. Verify the repository/SHA pair, monitored branches, synchronization and retention before assuming expiry; never substitute another baseline.

HTTP availability includes bounded historical `job_failures` summaries; see [diagnostic retention](deployment.md#task-and-query-diagnostics). MCP availability with a specific `job_id` returns `job_failure` and `job_lookup.state=archived_failure` when the live task was pruned but its failure summary remains. It is historical evidence, not current readiness or a runnable task. Missing history does not imply success.


## Git timeout settings

`repository_defaults.git_timeout_minutes` sets the global default (15 minutes initially); valid global values are 15, 30, 45 and 60. Existing settings without this field read as 15. An older settings client that omits the field preserves its saved value.

Registration and repository update accept `git_timeout_minutes`: 0 means inherit the current global value, and 15/30/45/60 set an override. Omitted registration values inherit; omitted PATCH values preserve the current setting. Repository details return the stored value. Preview/CAS and registration idempotency include the timeout. Only new tasks use edits; changing a timeout preserves running tasks and published indexes. The setting covers Git fetching and hydration and can be changed from system settings, registration, or repository settings.
