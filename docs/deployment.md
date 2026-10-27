# Deployment and persistence

## Topologies

Each instance owns its Git mirrors, Zoekt indexes and data directory. The database holds shared application state. Two topologies are supported:

- **Single instance with a large disk.** One instance serves every repository from one persistent volume. Size the volume for bare Git repositories plus indexes and keep headroom above `SOURCEGRAPH_MIN_FREE_BYTES`.
- **Sharded instances with repository-sticky routing.** Several instances share one PostgreSQL or MongoDB database, each serving a disjoint set of repositories from its own data volume. The access layer routes each request by its repository key (`owner/repository`) to the owning instance, using consistent hashing or an explicit repository → instance table. Requests not tied to one repository, such as listing repositories, may go to any instance.

Implementation notes for sharding:

- Use PostgreSQL or MongoDB; SQLite cannot be shared between instances.
- Application secrets and the encryption key are stored in the shared database. When upgrading existing instances, import the original credential key before allowing a replacement instance to initialize the database; do not replace it with a new key.
- Keep the repository → instance mapping stable. Moving a repository to another instance means it is fetched and indexed again there.
- Route by the repository named in the request: the `repo` query parameter for HTTP, and the `repo` argument of MCP tool calls.

Never let two instances serve the same repository or share a data directory: index ownership is per process. Scale by sharding repositories, not by adding replicas. Place instances in the same region as their callers to keep end-to-end latency low.

## Fly.io

`fly.toml` deploys one Source Graph Machine in `sin`, with 1 GB memory, auto-stop disabled, and a volume named `sourcegraph_data`. The starter volume should be at least 20 GB. Index memory and disk needs depend on repository size; observe real indexing before changing capacity. Do not scale this App to multiple Machines: Machines of one App would share repositories. For sharding, deploy one App per shard behind a routing access layer.

The portal is a separate App. For a unified domain it reverse-proxies the entire `/sourcegraph/` prefix, preserving query strings, cookies and authorization. Keep the configured external origin fixed; never trust arbitrary forwarded hosts. The portal must not cache auth/API/MCP responses. Static assets may be cached independently.

```sh
fly apps create context4ai-sourcegraph
fly volumes create sourcegraph_data --region sin --size 20 --app context4ai-sourcegraph
fly deploy --remote-only --ha=false
```

GitHub Actions runs verification on PRs and main. A successful main build deploys to Fly, using an app-scoped `FLY_API_TOKEN` repository secret. Deployments are serialized. A single-instance deployment may interrupt requests and indexing briefly; persisted tasks recover after restart.

## Databases

SQLite is the default. WAL and full synchronous commits are enabled; application writes use short transactions. The SQL schema stores BSON-encoded persistence models, including fields intentionally hidden from public JSON, with namespace/ID primary keys. PostgreSQL uses the same persistence contract with serializable transactions. MongoDB uses its native document collections; authentication admission requires transactions, so use a replica set or Atlas.

Changing `SOURCEGRAPH_DATABASE` does not migrate data. Start with an empty destination or perform a separately reviewed export/import. External databases do not replace the persistent Git/index volume. Sharded instances share one PostgreSQL or MongoDB database; each instance still needs its own volume.

## Backups and recovery

Back up the entire database, including its secrets and credential records, runtime storage configuration and repository metadata. Keep backups outside the Machine/volume, with restricted access. Fly volume snapshots are supplementary; test restoring the application state.

For SQLite use the SQLite online backup API / CLI `.backup`, or stop the service before copying the database and its WAL together. Never copy only a live `sourcegraph.db`. Example while the app is running:

```sh
sqlite3 /data/sourcegraph.db '.backup /data/sourcegraph-backup.db'
```

Copy the resulting backup and runtime config files to protected external storage. Retain a legacy key file only for pre-upgrade recovery. Indexes and bare Git repositories can be rebuilt, but identities, settings, credentials and their encryption key cannot be reconstructed from Git. For PostgreSQL use `pg_dump`; for MongoDB use an appropriate replica-set/Atlas backup.

To restore, stop the Machine, restore the complete database (including secrets) and runtime config; for pre-upgrade backups also restore the matching legacy key, then start one Machine and verify administrator access, repository metadata and index recovery. Image rollback and database restoration are separate operations; retain a known-good image and take a backup before schema changes.

## Task and query diagnostics

Terminal queue history retains its existing 24-hour/count limits. A separate `job_failures` field retains failure summaries for 7 days, up to 128 per repository (oldest entries are evicted at capacity). It stores task ID, kind, group, commit, error code, attempts, completion time and duration, without raw process errors, credentials or lease state. It does not consume queue/idempotency capacity or affect scheduling. Administrator failure-history cleanup removes summaries through the same cutoff. Previously deleted history cannot be reconstructed. These optional BSON fields work with SQLite, PostgreSQL and MongoDB without a schema migration.

After durable task completion, structured `repository_task_completed` logs include repository/task identity, state and error code, never raw preparation errors. Unchanged successful automatic scans are omitted. Queue history and summaries describe historical events, not current readiness.

Admin query logs include optional `batch` counters (`total`, `succeeded`, `failed`, `content_items`) independently of truncated request context. One log represents one request with failures. An empty File is successful; an item with both partial File and Error counts as failed with returned content. Unfinished items equal total minus succeeded and failed. Older records without counters remain unknown. Query-log retention is unchanged.


## Application secrets

Application secrets use AES-256-GCM, with the encryption key and encrypted values stored in the same database. This removes a separate host-key dependency; it does **not** protect against disclosure of the whole database. Restrict database access and protect backups.

The first upgrade imports `SOURCEGRAPH_CREDENTIAL_KEY` if set, otherwise the existing private `code-credentials.key` file. Existing encrypted code-source tokens continue using that key. Fresh databases generate a random key without creating a key file. Subsequent starts read the database and ignore obsolete host key seeds. Never initialize a replacement host against an old credentials database without importing its original key first.

The service API bearer and Google/GitHub OAuth client configuration are also persisted. Existing environment values seed missing records; existing database records take precedence. OAuth is left unconfigured when no provider is supplied. Keep environment secrets and the legacy key during the first rollout so the previous image remains usable on its original volume. After verified upgrade, replacement hosts need the database and non-secret runtime configuration, plus separate Git/index storage.

SQL stores secrets in the `secrets` namespace of `sourcegraph_documents`; MongoDB uses `sourcegraph_secrets`. Back up those records together with credentials and identity data. Changing an environment seed does not rotate an existing database secret. Rotation requires an authorized maintenance change to the corresponding database secret and verification of the new provider/service credential; do not delete the encryption key to rotate a token. Personal/global Git tokens remain editable through the website.

If a code token is already unreadable, the UI reports `unreadable` and allows replacing or clearing it with revision checks. Other users and repository records stay intact. An unreadable personal token does not block a usable global token.
