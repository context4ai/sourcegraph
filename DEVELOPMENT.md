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
| `SOURCEGRAPH_DATA_ROOT` | `/data`; persistent Git, indexes and manifests |
| `SOURCEGRAPH_ORIGIN` | `http://localhost:8080`; public origin, without a path |
| `SOURCEGRAPH_LISTEN` | `0.0.0.0:8080` |
| `SOURCEGRAPH_MIN_FREE_BYTES` | `1073741824`; index/fetch free-space floor |
| `SOURCEGRAPH_SEED_DEMO` | Set `true` to register the two public example repositories |
| `SOURCEGRAPH_GITHUB_TOKEN` | Optional GitHub token, used until a global GitHub token is saved; the earlier name `SOURCEGRAPH_GIT_TOKEN` is still read when this is unset |
| `SOURCEGRAPH_CUSTOM_GIT_TOKEN` | Optional token for the custom Git source, used until a global one is saved |
| `SOURCEGRAPH_CONFIG_FILE` | Optional JSON file that overrides code sources at startup; see [code sources](docs/code-sources.md) |
| `SOURCEGRAPH_CREDENTIAL_KEY` | First-upgrade seed: base64 32-byte key for existing code credentials. Otherwise imports the existing `code-credentials.key`; a fresh database generates its own key. The database becomes authoritative |
| `SOURCEGRAPH_API_TOKEN` | First-start seed for the database-backed service bearer, at least 32 characters; generated if absent |
| `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET` | Google web OAuth client, imported together into the database on first configuration |
| `GITHUB_CLIENT_ID`, `GITHUB_CLIENT_SECRET` | GitHub OAuth app, imported together into the database on first configuration |

Repository read plugins are tuned with `SOURCEGRAPH_PLUGINS_*`; see [repository plugins](docs/ref/repository-plugins.md). The CLI reads `SOURCEGRAPH_URL` and `SOURCEGRAPH_API_TOKEN`; see [interfaces](docs/interfaces.md#cli).

Never put credentials in deployment JSON, source code or images. Public non-secret defaults live in `config/deployment.json`.

## Further reading

- [Self-Hosting](https://context4ai.org/context/pages/87e9a0f083fa7c393fae3f5bbb123a11.html) and [Operations](https://context4ai.org/context/pages/b6b5d0b7d4370275aa82611da7136e4f.html) — guides on the documentation site
- [Interfaces](docs/interfaces.md) — HTTP routes, MCP and CLI
- [Deployment](docs/deployment.md) — Fly.io, databases, backups and recovery
- [Repository plugins](docs/ref/repository-plugins.md) — Wasm plugin contract

## Runtime Git timeout

System settings stores the global Git timeout in the database (15 minutes by default). Repositories inherit it by default or override it with 15, 30, 45 or 60 minutes. A new task snapshots the effective timeout; editing it does not interrupt running work or remove indexes. This covers Git fetch and object hydration, not total indexing time.

See [deployment](docs/deployment.md#application-secrets) for credential migration and backup requirements.
