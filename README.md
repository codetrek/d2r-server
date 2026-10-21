# d2r-server

面向修改后的 iPhone `d2r-mobile` 客户端的四人私有服务器工程。目标是在 Linux / Docker 中运行权威游戏逻辑，每次入房导入手机当前的本地角色，结束后可靠地把结果交回手机。

**当前还不能联机玩。** 已有代码用于静态产物检查、内部入口字节编解码和控制进程基础设施；原版游戏工作进程、完整双向桥接、四人入场与角色恢复仍需完成设计中的 G0–G5 验证。HTTP 服务就绪状态会如实反映这一点。

## 当前能力

| 组件 | 当前状态 |
|---|---|
| `d2r-inspect` | 校验自有 ARM64 Guest Mach-O 与配套 fixups，重建分析映像，逐节回读校验并输出 SHA-256 来源清单。 |
| `d2r-server` | 提供健康、就绪和能力查询接口，支持带超时的优雅关闭。引擎未获资格验证时保持未就绪。 |
| `internal/guestingress` | 编解码单条内部队列项及接收缓冲区，严格核对长度并取得自有字节副本；尚未接入游戏或网络。 |
| Linux `amd64` / `arm64` 构建 | 提供 Go 工具与控制进程的交叉构建入口；ARM64 游戏二进制的执行平台仍需独立验证。 |
| 原版游戏工作进程与联机 | 尚未实现。四人是产品约束，不是当前可运行能力。 |
| 检查点、角色导入及结果返回 | 设计已定义，尚未实现。 |

`d2r-inspect` 已在自有 v1.0.18 Guest 上重建出 54,378,496 字节映像，SHA-256 为 `30384eabe394cbc6b6a0b7f3788983d58dc14a92b7abe1638853dc4833cf81f1`，与前期调研记录一致。它不认证 Apple 代码签名，不应用重定位，不执行游戏，也不生成可运行的 Linux 服务器。

限定静态分析已经查明内部双队列及接收长度约定，并用 Hopper/Ghidra 交叉确认默认发送方法。已观察到的静态默认下行仅服务本地连接 `0`；远程四人路由、完整入场及下行流边界仍需验证。详见 [G0 传输边界报告](docs/research/2026-10-20-g0-transport-boundaries.md)。

## 构建与运行

需要 Go 1.26 和 Make；CI 与 Docker 构建固定 Go 1.26.10。普通检查只使用合成数据，无需游戏文件。

```sh
make check
make build
./bin/d2r-server
```

控制进程默认监听 `127.0.0.1:8080`，关闭超时为 `5s`。可通过参数修改：

```sh
./bin/d2r-server -listen 127.0.0.1:8080 -shutdown-timeout 5s
```

| 接口 | 语义 |
|---|---|
| `GET /healthz` | 进程存活时返回 HTTP 200。 |
| `GET /readyz` | 引擎未获资格验证时返回 HTTP 503，不能据健康接口判断可以接纳玩家。 |
| `GET /api/v1/capabilities` | 返回实际能力和计划约束；明确报告游戏引擎不可用。 |

当前能力响应为：

```json
{"game_worker":"unavailable","multiplayer_available":false,"target_max_players_per_room":4}
```

`target_max_players_per_room` 表示设计约束。它不宣称当前已支持四人游戏。

其他验证和构建入口：

```sh
make cross-build
make docker-test
make docker-build
docker run --rm -p 127.0.0.1:8080:8080 d2r-server:dev
```

Docker 测试阶段运行 Linux 源码测试及静态检查。最终镜像以 `scratch` 为基础，使用 UID/GID `65532:65532` 运行控制进程，容器内监听 `0.0.0.0:8080`。镜像不包含游戏文件、真实存档或可用的游戏工作进程。CI 配置 Linux/macOS 源码通道及单独的 Docker 通道；配置与实际执行结果分别记录。

## 检查自有 Guest

准备匹配的 `Guest` 和 `main.fixups.tsv`，将私人输入放在本仓库已忽略的 `.tmp/` 内。输出目录必须不存在，其父目录必须已经存在；工具拒绝覆盖现有目录。

```sh
mkdir -p .tmp/inspect
./bin/d2r-inspect \
  -guest .tmp/inputs/Guest \
  -fixups .tmp/inputs/main.fixups.tsv \
  -out .tmp/inspect/v1.0.18
```

成功后输出目录包含 `main.patched.reconstructed.bin` 与 `manifest.json`，标准输出也返回清单 JSON。清单记录输入及输出摘要、复制范围、逐节验证和分析限制。公开测试仅使用合成 Mach-O/fixups，不上传真实游戏材料。

## 仓库边界

服务端代码在本仓库维护；[手机 fork](https://github.com/codetrek/d2r-mobile) 保留联机入口、客户端桥接、手机存档交接及必要的共享接口改造，以便继续跟进上游。共享代码需要形成单一来源及明确版本契约；当前没有复制整个宿主，也没有已经接入的共享 SDK 或子模块。

研究基线固定为 [原始上游](https://github.com/jhaizhou-ops/d2r-mobile) 的 `120fe55c02a5633476998a44f8c3c3c809fa48b0`（v1.0.18）。[`upstream.lock.json`](upstream.lock.json) 是静态来源记录，不是已经安装的运行依赖，也不代表客户端与服务器已通过兼容性验证。

## 文档

- [四人私服设计及 G0–G5 验证条件](docs/design/2026-10-20-d2r-server-design.md)
- [协议、运行时与 REA 证据报告](docs/design/2026-10-20-protocol-research.md)
- [G0 内部传输、连接与缓冲区边界](docs/research/2026-10-20-g0-transport-boundaries.md)
- [开发、来源固定及私人材料管理](docs/development.md)

本次源码检查通过，核心包覆盖率为 88.4%，其中内部入口编解码包为 90.6%。G0 的静态局部进展与运行验收分别记录；Docker 和远程 CI 以对应分支的实际结果为准。

下一项关键工作是补齐加载到活跃、逐连接下行路由、待处理回调生命周期和完整流边界，并完成受控双进程消息往返。随后验证独立 Linux 游戏生命周期。G0–G5 仍未通过，服务端 CPU 选择仍待实测结果决定。
