# 开发与来源管理

## 工程状态

本仓库维护服务端工具、Go 控制进程，以及后续 Linux 游戏宿主、网关、房间管理、持久化和部署代码。当前实现包括 `d2r-inspect` 静态布局检查与 `d2r-server` 控制进程；游戏工作进程、联网游戏、SQLite 状态目录和存档恢复尚未实现。设计中的 G0–G5 均未通过。

普通源码检查不需要专有输入，使用合成 Mach-O/fixups 测试布局解析、失败条件、来源摘要、输出发布和命令行行为。控制进程测试核对健康、未就绪与能力声明。游戏实测必须另有私有验证记录，不能用 Go 测试或跨平台编译替代。

## 目录与代码归属

| 位置 | 职责 |
|---|---|
| `cmd/d2r-inspect/` | 静态布局检查命令行入口。 |
| `internal/guestlayout/` | Mach-O/fixups 校验、映像重建、来源清单和新目录发布。 |
| `cmd/d2r-server/` | 控制服务入口与关闭生命周期。 |
| `internal/control/` | 健康、就绪和能力查询 HTTP 处理及请求日志。 |
| `docs/design/` | 已接受的有条件设计、证据、未解问题与验收条件。 |
| `upstream.lock.json` | 固定原始上游提交与私人研究产物身份。 |
| `.tmp/` | 忽略的私人输入、实验输出、调研工具及验证材料。 |
| `bin/` | 本地构建输出。 |

Linux 平台适配、服务端调度、网关和持久化归本仓库。[手机 fork](https://github.com/codetrek/d2r-mobile) 负责 iPhone 界面、远程客户端桥接、账号存档租约、结果应用及必要的共享接口。修改两端契约时，在两仓库分别提交配套变更并记录支持的版本；不能只更新一个仓库就声称兼容。

原有宿主不能整套复制后各自演进。需要复用的加载、改写或游戏桥接代码应先明确平台与状态归属，再提取为单一来源、可固定版本的共享组件。组件提取前，静态来源记录不会自动成为 C SDK、子模块或构建依赖。提取时必须同时审查相关许可证和构建方式。

## 来源固定及升级流程

当前原始上游为 [jhaizhou-ops/d2r-mobile](https://github.com/jhaizhou-ops/d2r-mobile)，研究基线为 v1.0.18 的完整提交 `120fe55c02a5633476998a44f8c3c3c809fa48b0`。客户端 fork 与原始上游分别记录，不能把 fork 的分支头当作已验证基线。

`upstream.lock.json` 采用以下语义：

- `upstream.revision` 固定研究来源；`tag_at_research` 仅记录研究时标签，完整提交才是身份。
- `source_acquisition: not_vendored` 和 `shared_component_status: not_extracted` 表示尚未接入共享代码。
- `client_fork.compatible_revision_status: not_qualified` 表示尚无通过联网验收的客户端提交。
- `research_artifacts` 固定自有输入及分析输出身份，只有元数据进入 Git。
- `runtime_qualification` 记录当前资格状态；检查工具成功不能把游戏执行、Linux 工作进程或联机状态改成通过。

升级时先比较源码、补丁、映像布局及游戏构建，保留旧版本已验证的来源记录，再用新输入重新生成清单并完成受到影响的门槛验证。完整源码提交、Guest 与 fixups 哈希、资源与规则摘要及兼容性约定都要一起核对。只有获得新的验证记录后，才整体重写锁定文件及关联设计、研究和开发文档。当前不存在必须由代码兼容的旧服务端协议或持久化格式。

## 本地检查与 CI

需要 Go 1.26、Make；CI 和 Docker 固定 Go 1.26.10。Docker 仅用于容器测试及镜像构建。Go 缓存、模块缓存及临时文件由 Makefile 定位在本仓库 `.tmp/go/` 内。

```sh
make check
make build
```

各入口的职责如下，配置本身不表示对应检查已经执行成功：

| 入口 | 行为 |
|---|---|
| `make check` | 格式检查、`go vet`、命令包竞态测试及核心包覆盖率检查。 |
| `make test` | 全部 Go 包的独立竞态测试。 |
| `make coverage` | 使用固定 `go-cov` 版本，对核心包执行竞态及覆盖率门槛检查。 |
| `make build` | 在 `bin/` 输出两个本机工具。 |
| `make cross-build` | 在 `.tmp/cross/` 输出 Linux `amd64` / `arm64` 的两个 Go 工具。 |
| `make docker-test` | 在固定 Linux Go 镜像内执行全部源码竞态测试和 `go vet`。 |
| `make docker-build` | 构建包含静态 Go 工具的非 root `scratch` 镜像。 |

`go-cov` 固定为 `v0.1.4-0.20260617124522-d6caf2759e33`，使用 `--ci=true`、`--race=true`，核心包覆盖率门槛为总计 85%、逐包 80%、逐函数 50%。命令包从核心覆盖率统计中排除，在 `check` 中单独运行竞态测试；不会因统计分层重复运行核心测试。覆盖率用于发现未覆盖的重要行为，不能替代实际输入校验和故障测试。

CI 设置 Linux 和 macOS 的 `check` / `cross-build` 通道，以及独立的 Linux Docker 测试和镜像构建通道。Docker 基础镜像固定为 Go 1.26.10 Bookworm 的摘要；最终 `scratch` 镜像用 UID/GID `65532:65532` 运行，容器默认监听 `0.0.0.0:8080`。本机默认监听 `127.0.0.1:8080`，关闭超时为 `5s`。

2026-10-20 已通过 `make check`、`make build`、Linux amd64/arm64 交叉构建，以及 Docker 中 Linux ARM64 的全包竞态测试和 `go vet`，核心包总覆盖率为 88.2%。最终镜像已用默认非 root 身份启动，验证健康、未就绪和能力响应，并在正常停止后以状态码 0 退出。镜像内检查工具也已使用真实自有输入重建出与历史记录一致的映像；本地记录位于 `.tmp/container-validation/verification.json`。独立代码审查未发现未解决的 critical/major 问题。这些验证记录不替代远程 CI 或原游戏引擎验证门槛，也不声称已实际运行 Linux amd64 游戏程序。

控制进程的 `/healthz` 表示进程存活，`/readyz` 在引擎未通过资格验证时返回 503。`/api/v1/capabilities` 当前返回 `game_worker: "unavailable"`、`multiplayer_available: false` 和计划约束 `target_max_players_per_room: 4`。普通源码与容器检查不替代私有引擎和真实设备集成记录。

## 输出目录的原子发布

检查工具通过系统提供的排他重命名发布完整输出目录，避免并发调用覆盖已存在的结果。Darwin 使用 `renameatx_np` 与 `RENAME_EXCL`，Linux 使用 `renameat2` 与 `RENAME_NOREPLACE`；目前支持 macOS 及 Linux amd64/arm64，其他目标明确报错。原子可见性不等同于断电持久化保证。

常量来自固定版本的官方来源：[XNU 系统调用表](https://github.com/apple-oss-distributions/xnu/blob/f6217f891ac0bb64f3d375211650a4c1ff8ca1ea/bsd/kern/syscalls.master#L751)、[XNU 目标存在时的语义](https://github.com/apple-oss-distributions/xnu/blob/f6217f891ac0bb64f3d375211650a4c1ff8ca1ea/bsd/man/man2/rename.2#L139)、[Linux amd64 系统调用表](https://github.com/torvalds/linux/blob/f47d223627f80cb7a1eb14c585fe6cb35de5e984/arch/x86/entry/syscalls/syscall_64.tbl#L328)、[Go 1.26.0 的 Linux arm64 系统调用表](https://github.com/golang/go/blob/d90b98e65320778f3b1f99a6951ab20f04d218b3/src/syscall/zsysnum_linux_arm64.go#L269)。代码注释还引用了各平台目录和标志常量的定义。

## 私人材料与证据迁移

真实 Guest、游戏资源、角色存档、原始反编译全文及其派生字节不得进入公开 Git、公共 CI 工件或公开 Docker 镜像。将它们留在仓库内已忽略的 `.tmp/`。合成测试数据可以进入源码；它们不得夹带真实程序片段或玩家信息。

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
| `.tmp/inspect/` | `d2r-inspect` 每次调用新建输出目录的约定。 |

REA 证据 ID 与独立清单保持不变，真实 REA 输出和普通 shell 反汇编继续分别标识。后续分析在本仓库内配置工具和输入，不依赖旧手机工作区。导入、重定位、TLS、构造器及运行时对象未初始化的限制继续保留；分析 ELF 和重建字节不是可运行的服务器。

每次实验记录源码与输入摘要、工具版本、平台及 CPU、命令、场景、实际和预期结果，以及通过或失败结论。失败保留原始原因；重试应说明发生改变的假设。公开文档只引用必要元数据和摘要，不公布专有输入或原始反编译内容。
