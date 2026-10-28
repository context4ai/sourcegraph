# Documentation

- [Authentication and demo access](authentication.md)
- [Code sources and tokens](code-sources.md)
- [Snapshot synchronization and retention](snapshots.md)
- [Deployment, storage and backups](deployment.md)
- [HTTP, MCP and CLI](interfaces.md)
- [Repository plugins](ref/repository-plugins.md)

The service owns `/sourcegraph/`. A separately deployed portal owns `/` and `/context/`, forwarding `/sourcegraph/` to this service. Build and release behavior is defined by the Dockerfile and `.github/workflows/ci.yml`.
