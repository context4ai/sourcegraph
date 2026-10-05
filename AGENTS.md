# Development

This repository contains Context Source Graph: Go HTTP/MCP services, Git/Zoekt indexing, and an embedded website.

- Keep source, fixtures, documentation, skills, and artifacts suitable for public distribution.
- Read README.md and docs/README.md before changes. Update documentation with behavior.
- Run Go tests for affected packages, `bun run check:web`, and the production build.
- Persist control state and identity changes before acknowledging success. Preserve revision, lease, and generation checks.
- Never commit credentials, environment files, databases, indexes, or generated build output.
- Deployment is single-instance with persistent storage. Do not enable replica scaling without designing index ownership.
