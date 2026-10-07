<div align="center">

# Context Source Graph

**在固定 Git 版本上毫秒级搜索与阅读代码 —— 面向人、脚本与 AI Agent。**

[在线演示](https://context4ai.org/sourcegraph/) · [文档](docs/README.md) · [开发指南](DEVELOPMENT.md) · [English](README.md)

[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)
![MCP](https://img.shields.io/badge/MCP-Streamable%20HTTP-6E56CF)
![Search](https://img.shields.io/badge/search-Zoekt-F05032)

</div>

---

Context Source Graph 是一个可自托管的代码搜索服务，基于 Go、Git 与 [Zoekt](https://github.com/sourcegraph/zoekt) 构建。它为每个仓库维护三元组（trigram）索引，每次请求都先解析到确定的 commit，并通过 Web 界面、HTTP API、MCP Server 和 CLI 提供同一套能力。

它面向 AI 编码 Agent 的典型负载设计：对本地没有检出的代码，进行大量小而精确的 `search` → `read` 往返。

## 特性

- **毫秒级，而不是秒级。** 基于索引的搜索在服务端通常 **5 ms 以内** 完成。与调用方部署在同一机房时，包含网络在内的完整请求也能 **远低于 100 ms**，比在本地检出目录里跑 `grep` 或 `rg` 更快。
- **结果可复现。** 分支和标签在读取前先解析为 commit，搜索结果、它指向的文件以及之后的 diff 都对应同一个快照。
- **一套契约，四种入口。** 浏览器、HTTP、MCP 与 CLI 共享相同的操作：`repositories`、`availability`、`resolve`、`search`、`read`、`read_many`、`list`、`diff` 和 `prepare`。
- **为 Agent 而生。** 无状态的 Streamable HTTP MCP 端点可接入任意 MCP 客户端；匿名访问只读，且仅限公开仓库。
- **轻量。** 单个进程即可管理 Git 镜像、Zoekt 子进程和后台索引。默认使用 SQLite，同时支持 PostgreSQL 与 MongoDB。
- **可扩展的读取。** 仓库可以自带 Wasm 插件（`<name>.sourcegraph.wasm`），定制 `read` 与 `read_many` 的文件呈现方式，详见[仓库插件](docs/ref/repository-plugins.md)。

## 性能

基于公开演示站（`context4ai/context`，已索引 2,359 个文件）与同一 commit 的本地检出实测：

| 查询 | Context Source Graph（服务端） | `rg`（本地） | `git grep`（本地） | `grep -r`（本地） |
| --- | ---: | ---: | ---: | ---: |
| 字面量，命中 13 个文件 | **0.7–0.9 ms** | 61 ms | — | — |
| 字面量，命中约 100+ 个文件 | **3.6–5.2 ms** | 61 ms | 39 ms | 3.6 s |
| 正则，无命中 | **0.4 ms** | 62 ms | — | — |

<sub>服务端耗时取自 Zoekt 返回的搜索耗时。本地工具运行于 Apple M4 Pro，页缓存已预热；`grep -r` 已排除 `node_modules`、`.git` 和 `dist`。服务只返回有上限的首页结果，因此宽泛查询的开销不会无限增长。端到端延迟需再加上调用方与服务之间的网络往返 —— 同机房通常只有几毫秒。</sub>

`grep` 和 `rg` 每次调用都要重复扫描文件，而索引把这部分工作提前做完了，仓库越大差距越明显。此外，本地工具要求每个仓库都检出到正确的版本，而服务端不需要。

## 快速体验

打开[在线演示](https://context4ai.org/sourcegraph/)：已索引 [`context4ai/context`](https://github.com/context4ai/context) 与 [`context4ai/sourcegraph`](https://github.com/context4ai/sourcegraph)，支持匿名阅读。

接入支持 Streamable HTTP 的 MCP 客户端：

```json
{
  "mcpServers": {
    "sourcegraph": { "url": "https://context4ai.org/sourcegraph/mcp" }
  }
}
```

或直接调用 HTTP API：

```sh
curl -s -X POST 'https://context4ai.org/sourcegraph/api/search?repo=context4ai/context' \
  -H 'content-type: application/json' \
  -d '{"Pattern":"TODO","FixedStrings":true}'
```

站点的 **Connect** 页面始终展示最新的 HTTP 与客户端示例。MCP、HTTP、CLI 的细节见[接口文档](docs/interfaces.md)。

## 自行部署

```sh
docker build -t context-sourcegraph .
docker volume create sourcegraph-data
docker run --rm -p 8080:8080 -v sourcegraph-data:/data \
  -e SOURCEGRAPH_ORIGIN=http://localhost:8080 \
  -e SOURCEGRAPH_SEED_DEMO=true \
  context-sourcegraph
```

打开 <http://localhost:8080/sourcegraph/>。公开 GitHub 仓库无需 Token。未配置 OAuth 时，站点是匿名只读实例；配置 Google 或 GitHub OAuth 后即可启用管理功能，第一个完成验证的用户成为管理员（见[认证](docs/authentication.md)）。

全部环境变量见 [DEVELOPMENT.md](DEVELOPMENT.md#configuration)。

## 部署建议

每个实例在本地磁盘上独占自己的 Git 镜像与 Zoekt 索引。搜索速度来自这种数据本地性，因此容量规划的重点是磁盘，而不是 CPU。

**后续扩展方向：分片实例 + 按仓库粘性路由。** 当前运行时尚不支持这种部署：后台任务会扫描数据库中的全部仓库。共享数据库分片需要先在后台任务、索引生命周期操作与请求路由中实现仓库归属约束，仅配置入口路由并不足够。

**简单：单实例 + 大磁盘。** 对于单个团队或仓库数量适中的场景，一个挂载大容量高速持久卷的实例是最省心的选择。卷容量按裸 Git 仓库加索引估算，并保证剩余空间始终高于 `SOURCEGRAPH_MIN_FREE_BYTES`。

无论哪种方式，都请把实例部署在调用它的 Agent 与服务所在的同一机房 —— 这正是端到端延迟保持在 100 ms 以内的关键。

> [!IMPORTANT]
> 不要让多个副本服务同一批仓库或共享同一个数据目录。索引归属是进程级的；扩容靠按仓库分片，而不是复制实例。

Fly.io 配置、数据库、备份与恢复见[部署文档](docs/deployment.md)。

## 文档

| | |
| --- | --- |
| [认证](docs/authentication.md) | Google / GitHub 登录、管理员初始化、注册策略 |
| [部署](docs/deployment.md) | Fly.io、数据库、备份与恢复 |
| [接口](docs/interfaces.md) | 浏览器、HTTP、MCP 与 CLI |
| [仓库插件](docs/ref/repository-plugins.md) | 用于 `read` / `read_many` 的 Wasm 插件 |
| [开发指南](DEVELOPMENT.md) | 构建、测试、本地运行与配置 |

## 许可证

[MIT](LICENSE)。Context Source Graph 使用了 Zoekt（Apache-2.0）及其他开源组件，详见 [NOTICE](NOTICE)。项目名称不代表与 Sourcegraph 平台存在任何关联。

本项目是 [Context](https://github.com/context4ai/context) 的一部分。
