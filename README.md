<div align="center">

# Context Source Graph

**Millisecond code search and reading at pinned Git revisions — for people, scripts and AI agents.**

[Live demo](https://context4ai.org/sourcegraph/) · [Documentation](docs/README.md) · [Development](DEVELOPMENT.md) · [简体中文](README.zh-CN.md)

[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)
![MCP](https://img.shields.io/badge/MCP-Streamable%20HTTP-6E56CF)
![Search](https://img.shields.io/badge/search-Zoekt-F05032)

</div>

---

Context Source Graph is a self-hosted code search service built on Go, Git and [Zoekt](https://github.com/sourcegraph/zoekt). It keeps a trigram index of each repository, resolves every request to an exact commit, and exposes the same operations through a web UI, an HTTP API, an MCP server and a CLI.

It is built for the workload AI coding agents generate: many small, precise `search` → `read` round trips against code the agent does not have checked out.

## Why

- **Milliseconds, not seconds.** Indexed search typically completes in **under 5 ms** on the server. Deployed in the same region as its callers, a full request — network included — stays **well under 100 ms**, which is faster than running `grep` or `rg` over a local checkout.
- **Reproducible answers.** Branches and tags resolve to a commit before anything is read, so a search result, the file it points to and a later diff all refer to the same snapshot.
- **One contract, four surfaces.** Browser, HTTP, MCP and CLI share the same operations: `repositories`, `availability`, `resolve`, `search`, `read`, `read_many`, `list`, `diff` and `prepare`.
- **Agent-ready.** A stateless Streamable HTTP MCP endpoint works with any MCP client; anonymous access is read-only and limited to public repositories.
- **Small footprint.** One process manages Git mirrors, the Zoekt child process and background indexing. SQLite by default; PostgreSQL and MongoDB are supported.
- **Extensible reads.** Repositories can ship Wasm plugins (`<name>.sourcegraph.wasm`) that change how `read` and `read_many` present files. See [repository plugins](docs/ref/repository-plugins.md).

## Performance

Measured against the public demo (`context4ai/context`, 2,359 indexed files) and a local checkout of the same commit:

| Query | Context Source Graph (server) | `rg` (local) | `git grep` (local) | `grep -r` (local) |
| --- | ---: | ---: | ---: | ---: |
| Literal, 13 matching files | **0.7–0.9 ms** | 61 ms | — | — |
| Literal, ~100+ matching files | **3.6–5.2 ms** | 61 ms | 39 ms | 3.6 s |
| Regex, no matches | **0.4 ms** | 62 ms | — | — |

<sub>Server time is Zoekt's reported search duration. Local tools ran on an Apple M4 Pro with a warm page cache; `grep -r` excluded `node_modules`, `.git` and `dist`. The service returns a bounded first page of results, so very broad queries do not grow without limit. Add the network round trip between caller and server — a few milliseconds within one region — to get end-to-end latency.</sub>

The index does the work that `grep` and `rg` repeat on every call, so the gap widens as repositories grow. Local tools also require a checkout of every repository at the right revision; the service does not.

## Try it

Open the [live demo](https://context4ai.org/sourcegraph/) — it indexes [`context4ai/context`](https://github.com/context4ai/context) and [`context4ai/sourcegraph`](https://github.com/context4ai/sourcegraph) and allows anonymous reading.

Connect an MCP client that supports Streamable HTTP:

```json
{
  "mcpServers": {
    "sourcegraph": { "url": "https://context4ai.org/sourcegraph/mcp" }
  }
}
```

Or call the HTTP API directly:

```sh
curl -s -X POST 'https://context4ai.org/sourcegraph/api/search?repo=context4ai/context' \
  -H 'content-type: application/json' \
  -d '{"Pattern":"TODO","FixedStrings":true}'
```

The site's **Connect** page always shows current HTTP and client examples. See [interfaces](docs/interfaces.md) for MCP, HTTP and CLI details.

## Run your own

```sh
docker build -t context-sourcegraph .
docker volume create sourcegraph-data
docker run --rm -p 8080:8080 -v sourcegraph-data:/data \
  -e SOURCEGRAPH_ORIGIN=http://localhost:8080 \
  -e SOURCEGRAPH_SEED_DEMO=true \
  context-sourcegraph
```

Open <http://localhost:8080/sourcegraph/>. Public GitHub repositories need no token. Without OAuth credentials the site is an anonymous, read-only instance; add Google or GitHub OAuth to enable administration — the first verified user becomes the administrator ([authentication](docs/authentication.md)).

All environment variables are listed in [DEVELOPMENT.md](DEVELOPMENT.md#configuration).

## Deployment

Each instance owns its Git mirrors and Zoekt indexes on local disk. Search speed comes from that locality, so plan capacity around disk, not CPU.

**Recommended at scale: sharded instances with repository-sticky routing.** Run several instances that share one PostgreSQL or MongoDB database, each serving a disjoint set of repositories from its own data volume. Configure the access layer to route every request by its repository key (`owner/repository`) to the owning instance — for example with consistent hashing or an explicit repository → instance table. Every repository then has one warm index, capacity grows by adding instances, and losing one instance affects only its shard. See [deployment](docs/deployment.md#topologies) for the required settings.

**Simple: one instance with a large disk.** For a single team or a moderate number of repositories, one instance on a large, fast persistent volume is the easiest option. Size the volume for bare Git repositories plus indexes, and keep headroom above `SOURCEGRAPH_MIN_FREE_BYTES`.

Either way, place instances in the same region as the agents and services that call them — that is what keeps end-to-end latency below 100 ms.

> [!IMPORTANT]
> Do not run multiple replicas over the same repositories or the same data directory. Index ownership is per process; scale by sharding repositories, not by cloning instances.

Fly.io configuration, databases, backups and recovery are covered in [deployment](docs/deployment.md).

## Documentation

| | |
| --- | --- |
| [Authentication](docs/authentication.md) | Google / GitHub sign-in, administrator bootstrap, registration policy |
| [Deployment](docs/deployment.md) | Fly.io, databases, backups and recovery |
| [Interfaces](docs/interfaces.md) | Browser, HTTP, MCP and CLI |
| [Repository plugins](docs/ref/repository-plugins.md) | Wasm plugins for `read` / `read_many` |
| [Development](DEVELOPMENT.md) | Build, test, run locally and configure |

## License

[MIT](LICENSE). Context Source Graph uses Zoekt (Apache-2.0) and other open-source components; see [NOTICE](NOTICE). The project name does not imply affiliation with the Sourcegraph platform.

Part of [Context](https://github.com/context4ai/context).
