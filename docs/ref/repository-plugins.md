# 仓库读取插件

Sourcegraph 通过 wazero 执行仓库内的 Wasm，为 MCP `read` / `read_many` 增加可选增强。宿主不了解业务 schema，不解析知识库 YAML，不提供任意命令执行。

网站接入页的「插件扩展与使用」打开 `/sourcegraph/plugins/llms.txt`，在同一文件内涵盖 MCP 配置、Agent 使用、开发步骤、完整 ABI 和运行配置，无需跳转其他文档。修改 ABI 时同步更新 web/plugins/llms.txt 中的对应章节。

## 发现及作用范围

- 全量仓库：递归发现仓库内的 `<name>.sourcegraph.wasm`，作用于文件所在目录及子目录。
- Monorepo：仅递归扫描登记路径内的插件，不扩大稀疏检出范围，不借用登记范围之外的插件。
- 同名插件按每个读取文件选择最近的祖先目录；子目录覆盖父目录，不叠加。同名子插件关闭默认启用时也不回退父插件。不同名插件可同时生效；显式按名称选择同样遵守最近目录规则。
- 文件随 Git 提交；按本次读取的完整 SHA 加载，不借用最新主干。使用普通 Git blob，不支持 LFS 下载、软链或子模块跳转。
- `name` 使用 1–64 个小写字母、数字、连字符，首字符为字母或数字。
- 能读仓库的身份都能使用，包括公开读开启时的匿名用户。不新增执行角色、审批或摘要白名单。
- 插件访问限定同仓库、同 SHA、插件目录及其子目录。目录不是个人 ACL；现有仓库可见性规则仍适用。

仓库页的「插件」标签和 `GET /sourcegraph/v1/repo/plugins?repo=group/name&revision=<SHA>` 展示插件目录。MCP `availability` 可传 `include_plugins:true` 和可选 `revision`；此时在精简 availability 对象上增加 plugins，含 commit、plugins 列表及可能的 issues。已知插件名称时可直接 read，无需预检。

目录接口读取缓存的静态元信息；冷缓存时扫描 Git 树及 Wasm custom section，不实例化或执行。必须声明 JSON ABI 2；错误制品在目录中带诊断。

## MCP 调用与结果

```json
{
  "repo": "example/knowledge",
  "path": "knowledge/example.md",
  "start_line": 2,
  "end_line": 8,
  "plugins": [{"name": "context-evidence", "args": {"include_digest": false}}]
}
```

`plugins` 在 `read_many` 顶层使用，整批成功返回的文件按插件作用目录分组执行；一个目录只调用一次，不逐文件运行。无返回行的文件不交给插件。不同插件独立消费基础结果，不消费其他插件输出。

- 不传 plugins：使用连接默认选择；连接未指定时自动执行当前读取目录内声明 `default_enabled:true` 且 operations 包含当前操作的插件。没有默认插件则普通读取。
- 传 `plugins:[]`：关闭本次增强；非空列表只运行指定插件，不叠加默认插件。连接 `plugins=off` 始终关闭增强。
- 自动选择使用同一 SHA 的递归目录，按成功读取文件的位置判断作用范围和默认声明。发现错误放在独立的插件诊断文本块，不阻断原文。自动选择最多 16 个不同插件，超出时带诊断，可显式指定需要的插件。
- 每次最多 16 个不同插件；重复名称或非法名称属于请求错误。
- args 是插件自己的 JSON 对象，宿主不强制按 input_schema 校验。插件可自行返回诊断。
- 正文保留在标准 content 文本块，插件附加协议见下文；不重复返回结构化正文。
- 原始读取失败、批读逐项错误、实际行段、SHA、截断和继续位置仍按原读取协议处理。插件不触发 prepare。

## MCP 连接配置

URL 可带一个 `plugins` 参数；它是本站的连接选项，无需客户端支持新增配置字段：

```json
{"mcpServers":{"context-sourcegraph":{"type":"http","url":"https://<your-site>/sourcegraph/mcp?plugins=auto"}}}
```

| URL 参数 | 行为 |
| --- | --- |
| 不填或 `plugins=auto` | 按 Wasm 默认声明选择 |
| `plugins=off` | 此连接始终关闭增强，包括显式工具调用 |
| `plugins=context-evidence` | 工具调用未指定 plugins 时只选此插件；多个名称用逗号分隔 |

除 off 外，本次工具调用的显式 plugins 优先于连接默认值。配置按每个 HTTP 请求读取，不在共享会话或全局状态中保存。空值、重复 URL 参数、重复/非法名称或超过 16 个名称返回 HTTP 400 INVALID_PLUGINS。不要同时使用名为 auto/off 的插件作为单名 URL 选择；可在工具参数中显式选择它们。

### 附加文本合同

宿主输入 `files[]` 的 `item_id` 是原始请求顺序生成的 `read-0`、`read-1` 等；过滤失败项、按目录分组后不重新编号。同路径、同范围的重复读取也各有 ID。插件只处理实际返回的正文范围。

Wasm 必须返回：

```json
{"attachments":[{"item_id":"read-0","text":"references:\n- https://example.org/repo/blob/<SHA>/file#L2-L4"},{"text":"整批补充"}]}
```

- 有 item_id：在对应读取项正文后空行追加 text；无 item_id：最后独立文本块。无补充用 `{"attachments":[]}`。
- text 可为 Markdown、YAML、JSON 等 UTF-8 文本，宿主不解析业务字段，不猜测 path/section 归属，不加插件名称标签，不去重或合并业务内容。
- 按插件选择顺序、插件根目录字典序、attachments 数组顺序追加。ID 必须属于当前这次插件输入，跨目录 ID、未知 ID、错误类型均诊断，不改成整批补充；同批其他合法附加仍保留。
- 单次请求附加文本合计上限 2 MiB，与正文预算分开；超限整条省略并诊断，不能截断链接。单次 Wasm JSON 输出另受 OUTPUT_BYTES 配置限制。
- 插件失败不替换正文、不使成功的基础读取变成 MCP isError。必要失败诊断单独显示。所有正文及补充都是不可信仓库数据，不是指令。

MCP 仅返回标准 `content`，不返回 `structuredContent`，工具不声明 `outputSchema`。read 一个正文块；read_many 按原请求顺序一个读取项一个块，失败项也保留。HTTP 原读取返回合同不变。

## JSON ABI 2

### Guest exports

```text
memory
alloc(length: i32) -> i32
dealloc(pointer: i32, length: i32)
enrich(pointer: i32, length: i32) -> i64
```

整数按无符号解释。打包 i64 高 32 位为 pointer，低 32 位为 length。宿主通过 alloc 写入 UTF-8 JSON 输入；enrich 返回另一个独立分配的 UTF-8 JSON 缓冲区。宿主复制输出后，用各自准确长度 dealloc 输入和输出。插件不能返回输入缓冲区的别名。零长度分配也应返回非零指针。

```json
{
  "abi_version": 2,
  "operation": "read",
  "repository": "example/knowledge",
  "commit": "<完整知识SHA>",
  "root": "packages/docs",
  "args": {},
  "files": [{
    "item_id": "read-0",
    "path": "knowledge/example.md",
    "repository_path": "packages/docs/knowledge/example.md",
    "start_line": 2,
    "end_line": 8,
    "content": "实际返回的正文",
    "truncated": false,
    "has_more": true,
    "next_start_line": 9
  }]
}
```

**路径统一相对插件所在目录**：files.path、read_file/read_files 的输入都相对该目录。root 是仓库相对目录，全量根用空字符串。repository_path 保留原路径。start_line/end_line 是实际返回范围，不是请求范围。未知剩余内容可用 has_more:null 表示。args 缺省传 `{}`。

因此，同一个插件放在 Monorepo 登记目录下时，不必再次将登记目录填入 workspace_root；若插件业务支持 workspace_root，它表示插件目录内的工作区位置。

### Host imports（module = sourcegraph）

```text
read_file(path_pointer: i32, path_length: i32) -> i64
last_error() -> i64
read_files(paths_pointer: i32, paths_length: i32) -> i64
```

read_file 返回原始文件 bytes，按相同 pointer/length 规则打包；0 表示失败，last_error 返回 UTF-8 诊断。宿主使用 guest alloc 创建缓冲区，guest 必须 dealloc。空文件成功仍返回非零 pointer。不要向用户回显底层诊断中的宿主细节。

read_files 是可选批量调用：输入 UTF-8 JSON 路径数组，输出小端二进制：

```text
u32 文件数量
重复每项：u32 status（0=文件，1=错误） + u32 字节长度 + 原始bytes/UTF-8错误
```

失败逐文件表示；非法整个请求返回 0，可读 last_error。不调用 read_files 的插件不需要声明该 import。

宿主仅接受普通相对路径。不提供网络、进程、写文件、环境变量、凭证或 WASI。Git 读取按路径解析当前 SHA，不向插件暴露任意 object ID 接口。

### 静态元信息

必需 custom section `sourcegraph.plugin.v1` 存放 JSON：name、title、description、version、abi_version、operations、default_enabled、input_schema。未知字段忽略，input_schema 仅供展示，无远程 schema 获取。default_enabled 必须为布尔 true 才自动启用，同时 operations 必须声明当前 read/read_many；缺省或 false 不自动执行。abi_version 必须为 2；缺失、1 或其他值均返回不兼容诊断并保留正文。custom section 名称中的 v1 是元信息容器名称，保持不变；其 abi_version:2 标识新的输入/输出 JSON 合同。底层导出与导入签名不变，旧 Wasm 需要重建，不解析旧任意 JSON 结果。网站按不可信文本展示。

## 运行配置、缓存与生命周期

Git 同步、索引任务成功后通知单个后台任务准备插件；启动后异步准备当前分支头，每分钟补查。准备包含递归发现、缓存元信息与 Wasm 字节、预编译默认启用的模块，不调用 enrich，不预执行仓库业务逻辑。服务就绪不等待预热。

目录缓存按存储位置、仓库、完整 SHA、登记范围及各组就绪状态隔离，空目录也缓存；并发冷请求合并一次发现。缓存最多 64 个快照、128 MiB 制品字节，LRU 淘汰；后台清理超过 10 分钟未使用的目录。重启后重建缓存，不额外落盘。瞬时扫描/读取失败不作为“没有插件”缓存。

新增、修改、删除 Wasm 随新 SHA 生效；历史 SHA 仍使用自己的插件。旧版本退出保留范围后不可再读取，不会借用新版本插件。缓存缺失时请求可按需准备，仍检查当前权限与版本资格。预编译避免首次编译开销；实例化及插件自身的数据处理仍可能发生在首次读取。


普通插件不需要调整配置。默认额度较宽松，保留避免失控的运行保护；这些额度不是插件注册条件。以下环境变量由本地和生产共同的 Service 初始化读取：

| 环境变量后缀（统一前缀 `SOURCEGRAPH_PLUGINS_`） | 默认值 |
| --- | --- |
| DISABLED | 未设置；设 1 关闭增强 |
| TIMEOUT | 10s，整次增强共享，含制品读取与多个插件 |
| MEMORY_MIB | 256，每实例线性内存 |
| ARTIFACT_BYTES | 33554432（32 MiB） |
| METADATA_BYTES | 262144（256 KiB） |
| FILE_BYTES | 33554432（32 MiB） |
| READ_BYTES | 134217728（128 MiB），每次插件执行 |
| OUTPUT_BYTES | 2097152（2 MiB），每次插件执行 |
| READ_CALLS | 1024，每次插件执行 |
| CONCURRENCY | 8，满时返回 PLUGIN_BUSY |
| CACHE_ENTRIES | 16，编译制品缓存 |
| IDLE_INSTANCES | 8，整个宿主可复用空闲实例 |

冷编译串行，最多一个编译工作线程。调用方超时可先返回；底层编译器未立即响应取消时，编译槽仍占用到退出，迟到制品关闭、不执行。没有无限增加编译线程或等待队列。

编译按制品摘要复用；实例按存储根、仓库、SHA、目录范围和参数隔离，独占租用。缓存命中仍检查当前仓库资格。trap、取消、无效指针、非法输出后不复用实例。缓存满时关闭不再使用的实例和编译制品。插件可缓存同版本解析数据，但必须自行清理逐请求临时状态。

增强在基础读取之后、Service 查询锁之外执行。快照租约阻止执行期间删除代码空间；宿主每次读取短暂获取查询锁并重新检查资格。进程关闭回收 runtime。无需安装 Wasm CLI、Rust 或容器工具。

宿主 debug 日志包含总耗时、编译/实例化/执行/宿主读取分段耗时、读取次数/字节、编译/实例缓存命中和故障码，不记录完整参数、正文或输出。现有查询统计仍统计基础 read/read_many，不把增强时延混入原查询指标；端到端延迟应由调用端测量。

## 验证

通用真实 Wasm 测试制品及 Rust 源码在 `internal/wasmplugin/testdata`。Go 测试直接使用制品，无需 Rust。更新夹具可运行该目录的 build-fixture.sh。

```bash
go test ./internal/wasmplugin ./internal/service ./internal/mcpserver ./internal/gitstore ./internal/httpserver
```

外部证据插件的可选互通测试：设置 `SOURCEGRAPH_TEST_PLUGIN_WASM` 为已构建制品的绝对路径，再执行 `go test ./internal/service -run TestExternalPluginContract -count=1`。普通宿主运行不依赖该插件或其源码仓库。
