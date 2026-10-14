# Code sources and tokens

A code source is the Git host a repository is registered against; the service fetches the repository over that host's HTTPS address. This page covers the available sources, how to configure them, and which token each fetch uses.

## Sources

Every instance has exactly two code sources, listed GitHub first:

| id | Default host | Token variable | Anonymous | Editable on the website |
| --- | --- | --- | --- | --- |
| `github` | `https://github.com` | `SOURCEGRAPH_GITHUB_TOKEN` | yes | no |
| `git` | none | `SOURCEGRAPH_CUSTOM_GIT_TOKEN` | no | yes |

- GitHub has a fixed host. Public repositories need no token; a read-only token lifts anonymous rate limits and reaches private repositories.
- The custom Git source connects one self-hosted Git server, such as Gitea or GitLab. It has no host until one is set, and repositories cannot be registered against it before then.

Each source has these fields:

| Field | Meaning |
| --- | --- |
| `name` | Name shown when registering a repository |
| `host` | HTTPS origin of the Git server, without a path, user info, query or fragment |
| `username` | Username Git sends with the token over HTTPS; most hosts accept `oauth2`, the default |
| `token_env` | Environment variable holding the deployment token; set only in release defaults or the startup file |

## Settings pages

- **System settings → Code sources** (administrators) shows one card per source. The custom Git card holds its name, host and HTTPS username; every card holds the global token and an optional expiry, used to warn 7 days ahead. One save button submits whatever changed on the card.
- **User settings → Code source tokens** holds your personal token for each source.

Saved tokens are never shown again; leave the field blank to keep the current one. Changes apply to the next Git operation without a restart.

## Which token a fetch uses

Each fetch tries, in order:

1. the repository creator's personal token for that source;
2. the global token for that source;
3. the environment token for that source, only while no global token is saved;
4. anonymous access, for GitHub only.

Sources never share tokens: a custom Git repository never reads a GitHub token. Tokens are sent only to the host of the current remote, with the source's HTTPS username. Authentication failures move on to the next token; timeouts, cancellations and disk errors do not.

`github` still reads the earlier variable `SOURCEGRAPH_GIT_TOKEN` while `SOURCEGRAPH_GITHUB_TOKEN` is unset (`legacy_token_env` in the release defaults); a startup file that sets `token_env` stops that fallback. Clearing a saved global token does not bring back the environment token.

## Configuration layers

Settings apply in this order, later non-empty fields winning:

1. Release defaults in `config/deployment.json`, which fix the set and order of sources.
2. The custom Git card on the website, stored in site settings and applied immediately.
3. The startup file named by `SOURCEGRAPH_CONFIG_FILE`, read once at startup:

```json
{"code_sources": {"git": {"name": "Gitea", "host": "https://git.example.com", "username": "oauth2", "token_env": "SOURCEGRAPH_CUSTOM_GIT_TOKEN"}}}
```

- The file can only override existing ids; an unknown id, an invalid host or an invalid `token_env` stops startup. Never put a token in the file.
- Fields set by the file are read-only on the website (`409 CODE_SOURCE_LOCKED`).

## Registered repositories

- Registration pins the source host to the repository as `code_host`; later configuration changes never move existing repositories. Startup backfills a missing `code_host` and only logs when it differs from the current configuration.
- While any repository record uses the custom source, changing its host returns `409 CODE_SOURCE_IN_USE`; name and username can still change.
- Registering against a source without a host returns `409 CODE_SOURCE_NOT_CONFIGURED`.

## HTTP

| Route | Method | Purpose |
| --- | --- | --- |
| `/sourcegraph/v1/code-sources` | GET | Sources in display order: `id/name/host/username/anonymous/editable/locked`; public, without tokens or variable names |
| `/sourcegraph/api/admin/settings/code_source` | PATCH | Administrators edit the custom Git source with `{revision, values:{name, host, username}}` |
| `/sourcegraph/auth/code-credentials` | GET / PUT | Your personal tokens |
| `/sourcegraph/api/admin/settings/code-credentials` | GET / PUT | Global tokens, administrators only |

Token routes need a browser session, and writes are CSRF-protected; API tokens cannot manage them. GET returns `configured`, `expires_at`, `revision`, `state` and `source` per source, never the token. PUT updates one source with `{kind, revision, token?, expires_at, clear?}`: an omitted `token` keeps the current one, while an omitted or null `expires_at` clears the expiry; a stale `revision` returns `REVISION_CONFLICT`.

Tokens are stored encrypted with AES-256-GCM; see [deployment](deployment.md) for the encryption key and backups.
