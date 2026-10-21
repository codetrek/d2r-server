# 开发与来源管理

## 工程状态

本仓库维护服务端工具、Go 控制进程，以及后续 Linux 游戏宿主、网关、房间管理、持久化和部署代码。当前实现包括 `d2r-inspect` 双模块静态检查、`internal/guestingress` 内部入口编解码、`d2r-server` 控制进程与 `tools/mac-fixture` 私人候选/观察变体准备工具、原生观察器和 `d2r-trace`；游戏工作进程、联网游戏、SQLite 状态目录和存档恢复尚未实现。已有固定的 macOS ARM64 单机基线及独立观察变体进入世界并保存退出；历史 v1 变体完成系统/玩法逐通道输入与出队对照；当前 v2 另有 205 tick 六处采样，34/571 条下行范围与客户端高/低队列逐条一致，首次断开原因仍未解决；它不等于房间工作进程或联机。设计中的 G0–G5 均未通过。

普通源码检查不需要专有输入，使用合成 Mach-O/fixups 测试 main/sdk 布局解析、失败条件、来源摘要、输出发布和命令行行为。Python 准备工具测试同样只使用合成数据。内部入口测试使用合成载荷核对精确字节、声明长度、截断/尾随数据、整数范围及独立字节归属。控制进程测试核对健康、未就绪与能力声明。游戏实测必须另有私有验证记录，不能用 Go 测试或跨平台编译替代。

## 目录与代码归属

| 位置 | 职责 |
|---|---|
| `cmd/d2r-inspect/` | 静态布局检查命令行入口。 |
| `internal/guestlayout/` | 显式选择 main/sdk 的 Mach-O/fixups 校验、映像重建、schema 2 来源清单和新目录发布。 |
| `internal/guestingress/` | 单条内部队列项和接收缓冲区的精确编解码与自有字节副本。 |
| `internal/transporttrace/`、`internal/lifecycletrace/`、`cmd/d2r-trace/` | 传输 trace v2 与独立状态 trace v1 的有界读取、完整性判据和紧凑报告；不推定断开原因。 |
| `native/transport-probe/` | 固定六处传输画像、UI/tick 宿主回调包装、原始状态快照及双文件结束屏障。 |
| `cmd/d2r-server/` | 控制服务入口与关闭生命周期。 |
| `internal/control/` | 健康、就绪和能力查询 HTTP 处理及请求日志。 |
| `tools/mac-fixture/` | uv 锁定的 Python 私人 Mac 基线与观察变体构建工具，均不执行游戏。 |
| `docs/design/` | 已接受的有条件设计、证据、未解问题与验收条件。 |
| `docs/research/` | 按实验主题整理的静态研究结论、证据身份和运行验证限制。 |
| `upstream.lock.json` | 固定原始上游提交与私人研究产物身份。 |
| `.tmp/` | 忽略的私人输入、实验输出、调研工具及验证材料。 |
| `bin/` | 本地构建输出。 |

Linux 平台适配、服务端调度、网关和持久化归本仓库。[手机 fork](https://github.com/codetrek/d2r-mobile) 负责 iPhone 界面、远程客户端桥接、账号存档租约、结果应用及必要的共享接口。修改两端契约时，在两仓库分别提交配套变更并记录支持的版本；不能只更新一个仓库就声称兼容。

原有宿主不能整套复制后各自演进。需要复用的加载、改写或游戏桥接代码应先明确平台与状态归属，再提取为单一来源、可固定版本的共享组件。组件提取前，静态来源记录不会自动成为 C SDK、子模块或构建依赖。提取时必须同时审查相关许可证和构建方式。Mac 准备工具只在私人输出里归档固定提交并构建，不把完整宿主纳入本仓库；归档不使用上游工作区的未提交变化。

## 来源固定及升级流程

当前原始上游为 [jhaizhou-ops/d2r-mobile](https://github.com/jhaizhou-ops/d2r-mobile)，研究基线为 v1.0.18 的完整提交 `120fe55c02a5633476998a44f8c3c3c809fa48b0`。客户端 fork 与原始上游分别记录，不能把 fork 的分支头当作已验证基线。

`upstream.lock.json` 采用以下语义：

- `upstream.revision` 固定研究来源；`tag_at_research` 仅记录研究时标签，完整提交才是身份。
- `source_acquisition: not_vendored` 和 `shared_component_status: not_extracted` 表示尚未接入共享代码。
- `client_fork.compatible_revision_status: not_qualified` 表示尚无通过联网验收的客户端提交。
- `research_artifacts` 固定自有输入及分析输出身份，只有元数据进入 Git。
- `runtime_qualification.guest_execution: true` 只对应已实际执行的 `macos-arm64` / `full_client_singleplayer` 基线，固定候选清单、宿主、着色器链、159 tick 和正常保存退出记录。
- `game_worker_qualified`、`linux_game_worker`、`multiplayer` 和 `two_process_transport` 保持 `false`，`gates_passed` 为空；检查器或准备工具成功不能修改这些运行结论。
- `bounded_observer` 保留历史 v1 的 213 tick 窗口；`bounded_client_observer` 另行固定 v2 的 205 tick 诊断窗口和初次断开未解决状态。两者不覆盖基线，也不证明观察稳定性、工作进程或 G0。
- 锁文件是静态来源摘要，程序不读取它来决定可用能力。正常单机保存不证明崩溃或断电恢复。

升级时先比较源码、补丁、映像布局及游戏构建，保留旧版本已验证的来源记录，再用新输入重新生成清单并完成受到影响的门槛验证。完整源码提交、Guest 与 fixups 哈希、资源与规则摘要及兼容性约定都要一起核对。只有获得新的验证记录后，才整体重写锁定文件及关联设计、研究和开发文档。当前不存在必须由代码兼容的旧服务端协议或持久化格式。

## 本地检查与 CI

需要 Go 1.26、Make；CI 和 Docker 固定 Go 1.26.10。Docker 仅用于容器测试及镜像构建。Go 缓存、模块缓存及临时文件由 Makefile 定位在本仓库 `.tmp/go/` 内。Python 工具使用 `tools/mac-fixture/uv.lock`；Makefile 把 uv 缓存、Python 下载和虚拟环境分别定位到 `.tmp/uv/`、`.tmp/python/` 和 `.tmp/venv-mac-fixture/`，不全局安装依赖。

```sh
make check
make fixture-test
make build
```

各入口的职责如下，配置本身不表示对应检查已经执行成功：

| 入口 | 行为 |
|---|---|
| `make check` | 格式检查、`go vet`、命令包竞态测试及核心包覆盖率检查。 |
| `make test` | 全部 Go 包的独立竞态测试。 |
| `make coverage` | 使用固定 `go-cov` 版本，对核心包执行竞态及覆盖率门槛检查。 |
| `make fixture-test` | 使用 uv `--locked`、Python 3.12 运行准备工具的纯合成测试。 |
| `make build` | 在 `bin/` 输出控制进程、模块检查器和 `d2r-trace`。 |
| `make cross-build` | 在 `.tmp/cross/` 输出 Linux `amd64` / `arm64` 的三个 Go 工具。 |
| `make native-probe-test` | 仅 Darwin ARM64 的原生合成 ABI/观察/关闭屏障测试。 |
| `make docker-test` | 在固定 Linux Go 镜像内执行全部源码竞态测试和 `go vet`。 |
| `make docker-build` | 构建包含静态 Go 工具的非 root `scratch` 镜像。 |

`go-cov` 固定为 `v0.1.4-0.20260617124522-d6caf2759e33`，使用 `--ci=true`、`--race=true`，核心包覆盖率门槛为总计 85%、逐包 80%、逐函数 50%。命令包从核心覆盖率统计中排除，在 `check` 中单独运行竞态测试；不会因统计分层重复运行核心测试。覆盖率用于发现未覆盖的重要行为，不能替代实际输入校验和故障测试。

CI 设置 Linux 和 macOS 的 `check` / `cross-build` 通道、Linux/macOS 的 Python 准备工具合成测试通道，以及独立的 Linux Docker 测试和镜像构建通道。uv 通过固定提交的 setup-uv 安装固定版本 0.10.2，并使用 Python 3.12；另有固定 `macos-26` ARM 原生测试通道。公开 CI 不构建或执行专有游戏宿主。Docker 基础镜像固定为 Go 1.26.10 Bookworm 的摘要；最终 `scratch` 镜像用 UID/GID `65532:65532` 运行，容器默认监听 `0.0.0.0:8080`。本机默认监听 `127.0.0.1:8080`，关闭超时为 `5s`。

2026-10-20 的基础设施基线已通过 `make check`、`make build`、Linux amd64/arm64 交叉构建，以及 Docker 中 Linux ARM64 的全包竞态测试和 `go vet`，当时核心包总覆盖率为 88.2%。对应最终镜像以默认非 root 身份启动，健康、未就绪及能力响应正确，正常停止状态码为 0；镜像内检查工具使用自有输入重建出历史一致映像，私人记录位于 `.tmp/container-validation/verification.json`。

本次 main/sdk 与候选工具变更的完整 `make check` 通过，核心包总覆盖率 92.7%；逐包为 `lifecycletrace` 99.2%、`transporttrace` 96.0%、`guestlayout` 86.3%、`guestingress` 90.6%、`control` 98.3%，`d2r-trace` 命令测试覆盖率为 92.0%。`make fixture-test` 的 24 项合成测试通过，真实自有输入的准备流程另有全部 15 阶段成功记录。独立审查指出的运行元数据检查与复制之间的身份问题已经修复并复核，无未解决 critical/major。各分支 Docker 和远程 CI 必须以该分支实际结果为准，配置和历史绿色结果不代表当前变更已通过。

最终准备候选另行在受约束的 Mac 实验中运行，进入本地世界推进 159 tick，正常保存退出返回菜单；退出请求后记录 3 次保存提交，角色输出变化，原始备份保持不变，随后进程退出码 0。完整身份、输入保护和证据范围见 [Mac 运行报告](research/2026-10-20-mac-runtime-fixture.md)。这证明限定的 Mac 完整客户端单机路径；Linux 引擎、房间、双进程桥接、多人及故障持久化均未获资格。后续独立原生变体还完成 213 tick 实际移动，系统 62 条、玩法 26 条输入与各自有效出队逐条精确且顺序一致；原生测试、ASAN/UBSAN 及独立审查通过，记录见[原生观察报告](research/2026-10-20-native-transport-probe.md)。

控制进程的 `/healthz` 表示进程存活，`/readyz` 在引擎未通过资格验证时返回 503。`/api/v1/capabilities` 当前返回 `game_worker: "unavailable"`、`multiplayer_available: false` 和计划约束 `target_max_players_per_room: 4`。普通源码与容器检查不替代私有引擎和真实设备集成记录。

## 内部入口编解码的范围

`internal/guestingress` 提供四个函数：`EncodeQueueEntry`、`DecodeQueueEntry`、`EncodeReceiverBuffer`、`DecodeReceiverBuffer`。前一对处理小端 `[u16 载荷长度][u32 连接号][载荷]`，后一对处理 `[u32 连接号][载荷]` 与单独的 `uint32` 载荷长度。解码要求恰好一条输入，所有结果拥有独立字节；不同调用不共享载荷存储。

`MaxQueuePayload = 65535` 只对应内部队列长度字段，不是网络帧或游戏可安全接收长度。接收表示独立检查 `uint32` 和平台整数范围。连接号 `0` 和空载荷可通过表示层编解码，游戏入口是否接受则由消息语义和生命周期验证决定。该包不处理通道分类、整个动态队列、WSS、认证、游戏线程或入口调用；网络输入仍不得直接交给游戏。

[G0 传输边界报告](research/2026-10-20-g0-transport-boundaries.md)记录具体方法、两级长度、默认本地下行、借用范围、同步复制及回调持有者约定；后续 Mac 单机执行不代表这些边界已经由透传或双进程验证。

## 双模块检查与私人 Mac 候选

`d2r-inspect -module main|sdk` 的 CLI 默认 `main`；Go API 的 `Options.Module` 必须显式指定。模块与 fixups 必须匹配。清单 schema 为 2，含 `module`、`layout_profile: "dm-split-sections-v1"`，不读取旧清单来兼容或推断模块。两种重建输出保留宿主补丁，不是原始 NSO 或独立可执行文件。

[`tools/mac-fixture`](../tools/mac-fixture/README.md)从自有 App、资源档案和固定上游 Git 提交，生成模块、TVFS 子集适配、三个真实数据表头文件、MSL 与匹配链元数据，再构建 Mac 宿主。工具只准备、不执行游戏、不安装系统工具；运行输入与预置工具的便携命令在工具文档中给出。只有工具指纹锁和生成的链配置可以在私人源码快照中改变。

输入路径和资源只读引用必须在候选使用期间保持不变。准备先快照四份 fixups/gates，再让检查器和运行文件读取相同快照，发布前复核原件、快照和全部六份运行文件身份。不能把检查过的 fixups 与后来改变的运行元数据混用。输入、输出和暂存目录互不覆盖，失败和并发目标冲突明确报错。

TVFS 的 133,200 条路径不代表所有内容档案都存在；已验证的 3,156 个生成器输入和 162 个着色器包仅证明对应子集。6,927 个键生成的 7,212 个 `.metallib` 命名条目是 MSL 源码，不是预编译 `MTLB`。真实运行只覆盖已观察到的场景，不能扩大为全资源、全着色器变体或 Linux 等价性。

实际执行另有独立的沙箱、进程所有权、期限和清理记录：禁止网络，保护输入与参考存档，写入只允许私人实验目录。准备工具不提供执行沙箱或启动命令。正常保存退出与原始备份不变均需断言；崩溃、断电和跨玩家检查点另行验证。

## 原生观察与紧凑 trace 报告

`d2r-mac-probe-build` 验证已准备父候选，再从未修改的固定上游源码和原生只读快照构建独立变体。原始 `main.c`、`hle.c` 分别使用独立的初始化和注册包装宏；每份对象和最终二进制的原始/包装符号均需单一定义。构建只生成 `built_unexecuted`，实际执行必须另有清单，具体便携命令见[工具文档](../tools/mac-fixture/README.md)。

采样固定 Darwin ARM64 画像并保留原方法参数、返回及 `x8` 间接结果。发送只记调用尝试，原忽略路径不读描述符；出队先检查有效字节，空结果不读未初始化内容。实际 trace 仅写私人目录，禁止网络，输入和参考备份受只读约束。

运行显式提供 `D2R_PROBE_STOP_PATH`，在游戏与保存退出断言达到后创建所属标记；观察器关闭新准入并等待在途原方法和出队复制完成，发布 `CLOSED` 后控制方才结束宿主。区间外继续纯转发，不能依赖宿主 `_exit` 调用 atexit。文件完整性只覆盖声明窗口。

`d2r-trace -input TRACEPATH [-format transport|lifecycle] [-report NEW_JSON]` 验证固定头、计数、记录、标志、模式与连接前缀，复用内部入口解码器；只读取提交前缀，分配受单条观察上限约束。默认读取传输 trace v2 / JSON schema 2 的六类统计；显式 `-format lifecycle` 读取独立状态 trace v1，输出两个门、嵌套、原始错误编号和首个待处理错误的有限上下文，不输出载荷、地址、玩家或无限记录表。报告文件排他创建为 0600；错误/缺口/未关闭或容量截断均不能通过，损坏和不合格退出 1。模式 1/3 错标客户端忽略的审查问题已有负例回归。

历史 v1 的精确对照不证明 S2C 消费；当前 v2 增加客户端 `+0/+8` 高/低队列出队，返回范围没有 CID，不能交给服务端接收表示解码器。605 条实际下行范围在各自分桶内逐条字节有序相等，客户端出队仍不等于状态应用或跨队列全局顺序。初次断开和后续两项转调对照、成功诊断的完整范围见[客户端出队报告](research/2026-10-21-s2c-client-consumption.md)。`capture_integrity_eligible` 不修改 G0 或生产引擎资格。原生传输格式和 ABI 以[格式文档](../native/transport-probe/format.md)为准；状态语义和双文件关闭见[状态格式](../native/transport-probe/lifecycle.md)及[两个实际窗口](research/2026-10-21-lifecycle-observation.md)。共享状态位不是错误独占标志，读取器使用非零待处理期限判定，原程序计时保持原值。

## 输出目录的原子发布

检查工具通过系统提供的排他重命名发布完整输出目录，避免并发调用覆盖已存在的结果。Darwin 使用 `renameatx_np` 与 `RENAME_EXCL`，Linux 使用 `renameat2` 与 `RENAME_NOREPLACE`；目前支持 macOS 及 Linux amd64/arm64，其他目标明确报错。原子可见性不等同于断电持久化保证。

常量来自固定版本的官方来源：[XNU 系统调用表](https://github.com/apple-oss-distributions/xnu/blob/f6217f891ac0bb64f3d375211650a4c1ff8ca1ea/bsd/kern/syscalls.master#L751)、[XNU 目标存在时的语义](https://github.com/apple-oss-distributions/xnu/blob/f6217f891ac0bb64f3d375211650a4c1ff8ca1ea/bsd/man/man2/rename.2#L139)、[Linux amd64 系统调用表](https://github.com/torvalds/linux/blob/f47d223627f80cb7a1eb14c585fe6cb35de5e984/arch/x86/entry/syscalls/syscall_64.tbl#L328)、[Go 1.26.0 的 Linux arm64 系统调用表](https://github.com/golang/go/blob/d90b98e65320778f3b1f99a6951ab20f04d218b3/src/syscall/zsysnum_linux_arm64.go#L269)。代码注释还引用了各平台目录和标志常量的定义。

## 私人材料与证据迁移

真实 Guest、游戏资源、角色存档、设置、玩家名称、截图、原始运行载荷、反编译全文及其派生字节不得进入公开 Git、公共 CI 工件或公开 Docker 镜像。将它们留在仓库内已忽略的 `.tmp/`。合成测试数据可以进入源码；它们不得夹带真实程序片段或玩家信息。

前期调研材料已经完整复制到本仓库。`.tmp/migration/manifest.json` 记录 5,827 个文件或符号链接条目，全部文件已比对源与目标 SHA-256，符号链接也已比对目标。真实证据 JSON 不作内容修改，其内嵌历史绝对路径继续保留为来源记录；当前副本位置由迁移索引和下表确定。

| 位置 | 材料与约定 |
|---|---|
| `.tmp/inputs/` | 已复制的自有 Guest 和配套 fixups；身份固定在 `upstream.lock.json`。 |
| `.tmp/rea/` | 保留历史相对位置的完整私人 REA 目录，包括真实证据导出、聚焦分析配置、派生物、写入记录和生成方式。 |
| `.tmp/rea-provider-recovery/` | 已复制的后端恢复调研记录。 |
| `.tmp/reviews/design-history/` | 已复制的原设计审查历史。 |
| `.tmp/migration/manifest.json` | 原工作区到新仓库的逐文件映射、大小、SHA-256 及符号链接信息。 |
| `.tmp/tools/` | 已复制、经来源和校验和固定的私人分析工具；不进入镜像或 Git。 |
| `.tmp/experiments/` | 后续 G0–G5 实验的输入清单、范围受限日志、指标与实际断言约定。 |
| `.tmp/mac-fixture/` | 准备候选、四份元数据快照和六份运行文件核对记录。 |
| `.tmp/inspect/` | `d2r-inspect` 每次调用新建输出目录的约定。 |

REA 证据 ID 与独立清单保持不变，真实 REA 输出和普通 shell 反汇编继续分别标识。当前 G0 材料分别保存在 `.tmp/experiments/g0-session/`、`.tmp/experiments/g0-hopper/`、`.tmp/experiments/g0-transport-source/`。它们包含完整私人结果、配置、清单及清理记录；公开文档只引用必要的身份和限定结论。后续分析在本仓库内配置工具和输入，不依赖旧手机工作区。静态工具和分析 ELF 未初始化导入、重定位、TLS 或构造器的限制继续保留；后续 Mac 完整宿主运行依赖自己的初始化与平台服务，不能将其证明套到静态文件或 Linux 工作进程。

每次实验记录源码与输入摘要、工具版本、平台及 CPU、命令、场景、实际和预期结果，以及通过或失败结论。失败保留原始原因；重试应说明发生改变的假设。公开文档只引用必要元数据和摘要，不公布专有输入或原始反编译内容。
