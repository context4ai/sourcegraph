# Contributing to Context Source Graph

Thank you for helping improve Context Source Graph. Contributions can include
bug reports, documentation, search and read behavior, MCP and HTTP interfaces,
repository plugins, deployment guidance and tests.

## Before opening a pull request

- Search existing Issues and Discussions first.
- Discuss changes to public HTTP, MCP or CLI contracts, storage layout,
  authentication or the repository plugin ABI in an Issue or Discussion before
  implementation.
- Small documentation fixes and isolated bug fixes can go directly to a pull
  request.
- Report security vulnerabilities privately by following [SECURITY.md](SECURITY.md).

## Development setup

```sh
git clone https://github.com/context4ai/sourcegraph.git
cd sourcegraph
bun install --frozen-lockfile
bun run build:web
bun run check
go test -p 1 ./...
```

[DEVELOPMENT.md](DEVELOPMENT.md) covers prerequisites, running a local
instance and configuration. The concepts behind the service are explained in
the [Source Graph documentation](https://context4ai.org/context/pages/e1ce6ff3105ae09d13eec7cbe39ae133.html).

## Pull request expectations

A change is ready for review when:

- `bun run check`, the Go tests for affected packages and `bun run build` pass;
- documentation under `docs/` and both README files reflect the behavior;
- browser, HTTP, MCP and CLI surfaces stay consistent for a shared operation;
- control state and identity changes are persisted before success is reported;
- fixtures and examples use public repositories only;
- credentials, environment files, databases, indexes and build output are not
  committed.

Keep pull requests focused. Explain the problem, observable behavior,
compatibility impact and verification evidence.

## Commit messages

Use an English conventional commit:

```text
feat: add read_many to the MCP server
fix: isolate unsupported Git paths during indexing
docs: link the documentation site
```

## Conduct

Participation is governed by the [Code of Conduct](CODE_OF_CONDUCT.md).
