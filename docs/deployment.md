# Deployment and persistence

## Topologies

Each instance owns its Git mirrors, Zoekt indexes and data directory. The database holds shared application state. The supported topology and a future expansion option are:

- **Single instance with a large disk.** One instance serves every repository from one persistent volume. Size the volume for bare Git repositories plus indexes and keep headroom above `SOURCEGRAPH_MIN_FREE_BYTES`.
- **Future option: sharded instances with repository-sticky routing.** Not currently supported. Background workers scan all repositories in the shared database; access-layer routing cannot constrain them. Repository ownership must first be enforced across workers, indexing, cleanup and request routing. A future implementation would use separate volumes, a shared PostgreSQL or MongoDB database and the same `SOURCEGRAPH_CREDENTIAL_KEY` on all instances. SQLite cannot be shared between instances.

Never let two instances serve the same repository or share a data directory: index ownership is per process. Scale by sharding repositories, not by adding replicas. Place instances in the same region as their callers to keep end-to-end latency low.

## Fly.io

`fly.toml` deploys one Source Graph Machine in `sin`, with 2 GB memory, auto-stop disabled, and a volume named `sourcegraph_data`. The starter volume should be at least 20 GB. Index memory and disk needs depend on repository size; observe real indexing before changing capacity. Do not scale this App to multiple Machines: Machines of one App would share repositories. Do not deploy shared-database shards until ownership enforcement is implemented.

The portal is a separate App. For a unified domain it reverse-proxies the entire `/sourcegraph/` prefix, preserving query strings, cookies and authorization. Keep the configured external origin fixed; never trust arbitrary forwarded hosts. The portal must not cache auth/API/MCP responses. Static assets may be cached independently.

```sh
fly apps create context4ai-sourcegraph
fly volumes create sourcegraph_data --region sin --size 20 --app context4ai-sourcegraph
fly deploy --remote-only --ha=false
```

GitHub Actions runs verification on PRs and main. A successful main build deploys to Fly, using an app-scoped `FLY_API_TOKEN` repository secret. Deployments are serialized. A single-instance deployment may interrupt requests and indexing briefly; persisted tasks recover after restart.

## Databases

SQLite is the default. WAL and full synchronous commits are enabled; application writes use short transactions. The SQL schema stores BSON-encoded persistence models, including fields intentionally hidden from public JSON, with namespace/ID primary keys. PostgreSQL uses the same persistence contract with serializable transactions. MongoDB uses its native document collections; authentication admission requires transactions, so use a replica set or Atlas.

Changing `SOURCEGRAPH_DATABASE` does not migrate data. Start with an empty destination or perform a separately reviewed export/import. External databases do not replace the persistent Git/index/key volume. A shared PostgreSQL or MongoDB database alone does not enable horizontal scaling; repository ownership enforcement is also required.

## Backups and recovery

Back up the database, encryption key, runtime storage configuration and repository metadata. Keep backups outside the Machine/volume, with restricted access. Fly volume snapshots are supplementary; test restoring the application state.

For SQLite use the SQLite online backup API / CLI `.backup`, or stop the service before copying the database and its WAL together. Never copy only a live `sourcegraph.db`. Example while the app is running:

```sh
sqlite3 /data/sourcegraph.db '.backup /data/sourcegraph-backup.db'
```

Copy the resulting backup and key/config files to protected external storage. Indexes and bare Git repositories can be rebuilt, but identities, settings, credentials and their encryption key cannot be reconstructed from Git. For PostgreSQL use `pg_dump`; for MongoDB use an appropriate replica-set/Atlas backup.

To restore, stop the Machine, restore database and matching key/config, then start one Machine and verify administrator access, repository metadata and index recovery. Image rollback and database restoration are separate operations; retain a known-good image and take a backup before schema changes.
