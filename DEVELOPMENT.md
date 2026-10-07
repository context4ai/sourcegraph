# Development

[← Back to README](README.md) · [简体中文 README](README.zh-CN.md)

## Prerequisites

- Go 1.26+
- Bun 1.3.9
- Git
- ripgrep (used by tests)
- Optional: Rust with the `wasm32-unknown-unknown` target, to reproduce the checked-in Wasm test fixture

## Build, test and run

```sh
bun install --frozen-lockfile
bun run build:web
bun run check
go test -p 1 ./...
bun run build
SOURCEGRAPH_DATA_ROOT="$PWD/.tmp/data" SOURCEGRAPH_ORIGIN=http://localhost:8080 output/bin/repo-service
```

`output/bin` contains the service (`repo-service`), the CLI (`sourcegraph-cli`), the Git credential helper (`git-credential-sourcegraph`) and the Zoekt server. The service manages the Zoekt child process and the background indexing lifecycle.

CI runs the same verification on pull requests and `main`; see `.github/workflows/ci.yml`.

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
| `SOURCEGRAPH_CREDENTIAL_KEY` | Optional base64 32-byte key for stored code credentials; otherwise `code-credentials.key` is generated in the data root. Required, and identical, on all instances sharing a database |
| `SOURCEGRAPH_API_TOKEN` | Optional service automation bearer credential, at least 32 characters |
| `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET` | Google web OAuth client |
| `GITHUB_CLIENT_ID`, `GITHUB_CLIENT_SECRET` | GitHub OAuth app |

Never put credentials in deployment JSON, source code or images. Public non-secret defaults live in `config/deployment.json`.

## Further reading

- [Interfaces](docs/interfaces.md) — HTTP routes, MCP and CLI
- [Deployment](docs/deployment.md) — Fly.io, databases, backups and recovery
- [Repository plugins](docs/ref/repository-plugins.md) — Wasm plugin contract
