#!/usr/bin/env bash
set -euo pipefail
bun run build:web
mkdir -p output/bin
revision="${SOURCEGRAPH_BUILD_REVISION:-development}"
for name in repo-service git-credential-sourcegraph sourcegraph-cli; do
  CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.buildRevision=$revision" -o "output/bin/$name" "./cmd/$name"
done
CGO_ENABLED=0 go build -trimpath -o output/bin/zoekt-webserver github.com/sourcegraph/zoekt/cmd/zoekt-webserver
