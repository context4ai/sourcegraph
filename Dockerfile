FROM oven/bun:1.3.9 AS web
WORKDIR /src
COPY package.json bun.lock ./
RUN bun install --frozen-lockfile
COPY web ./web
COPY config ./config
COPY scripts/build-web.mjs ./scripts/build-web.mjs
COPY docs/ref/repository-plugins.md ./docs/ref/repository-plugins.md
RUN bun run build:web

FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY config ./config
COPY --from=web /src/internal/webui/dist ./internal/webui/dist
ARG BUILD_REVISION=development
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.buildRevision=${BUILD_REVISION}" -o /out/repo-service ./cmd/repo-service \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/git-credential-sourcegraph ./cmd/git-credential-sourcegraph \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/sourcegraph-cli ./cmd/sourcegraph-cli \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/zoekt-webserver github.com/sourcegraph/zoekt/cmd/zoekt-webserver

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git tini sqlite3 && rm -rf /var/lib/apt/lists/* \
 && useradd --uid 10001 --create-home sourcegraph && mkdir /data && chown sourcegraph:sourcegraph /data
COPY --from=build /out/ /usr/local/bin/
COPY scripts/entrypoint.sh /usr/local/bin/entrypoint.sh
ENV SOURCEGRAPH_DATA_ROOT=/data
EXPOSE 8080
ENTRYPOINT ["/usr/bin/tini", "-s", "-g", "--", "/bin/sh", "/usr/local/bin/entrypoint.sh"]
