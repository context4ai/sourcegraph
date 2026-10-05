# Context Source Graph

Search and read code at fixed Git revisions, from a browser, HTTP client, MCP client, or CLI. This is an independent application built with Go, Git and Zoekt.

The demo indexes `context4ai/context` and `context4ai/sourcegraph`. Anonymous visitors can read public demo repositories. Registration is closed by default; the first verified Google or GitHub user becomes the administrator.

## Run with Docker

```sh
docker build -t context-sourcegraph .
docker volume create sourcegraph-data
docker run --rm -p 8080:8080 -v sourcegraph-data:/data \
  -e SOURCEGRAPH_ORIGIN=http://localhost:8080 \
  -e SOURCEGRAPH_SEED_DEMO=true context-sourcegraph
```

Open <http://localhost:8080/sourcegraph/>. Public GitHub repositories do not require a Git access token. Add OAuth credentials to enable administration; see [authentication](docs/authentication.md). Without OAuth credentials the site remains an anonymous, read-only demo.

## Development

Go 1.26+, Bun 1.3.9, and Git are required. The checked-in Wasm test fixture is reproducible with Rust's `wasm32-unknown-unknown` target.

```sh
bun install --frozen-lockfile
bun run build:web
bun run check
go test -p 1 ./...
bun run build
SOURCEGRAPH_DATA_ROOT="$PWD/.tmp/data" SOURCEGRAPH_ORIGIN=http://localhost:8080 output/bin/repo-service
```

The `output/bin` directory contains the service, CLI, Git credential helper, and Zoekt server. The service manages the Zoekt child process and background indexing lifecycle.

## Configuration

| Environment variable | Default / purpose |
| --- | --- |
| `SOURCEGRAPH_DATABASE` | `sqlite`; also `postgres` or `mongodb` |
| `DATABASE_URL` | SQLite absolute file path (default `/data/sourcegraph.db`), PostgreSQL DSN, or standard MongoDB URI |
| `SOURCEGRAPH_DATA_ROOT` | `/data`; persistent Git, indexes, manifests and encryption key |
| `SOURCEGRAPH_ORIGIN` | `http://localhost:8080`; public origin, without a path |
| `SOURCEGRAPH_LISTEN` | `0.0.0.0:8080` |
| `SOURCEGRAPH_MIN_FREE_BYTES` | `1073741824`; index/fetch free-space floor |
| `SOURCEGRAPH_SEED_DEMO` | Set `true` to register the two public example repositories |
| `SOURCEGRAPH_GIT_TOKEN` | Optional GitHub credential for private repository access |
| `SOURCEGRAPH_API_TOKEN` | Optional service automation bearer credential, at least 32 characters |
| `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET` | Google web OAuth client |
| `GITHUB_CLIENT_ID`, `GITHUB_CLIENT_SECRET` | GitHub OAuth app |

Never put credentials in deployment JSON, source code or images. Public non-secret defaults live in `config/deployment.json`.

[Documentation](docs/README.md) · [Context](https://github.com/context4ai/context)
