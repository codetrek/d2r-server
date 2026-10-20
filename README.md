# d2r-server

面向修改后的 iPhone `d2r-mobile` 客户端的四人私有服务器工程。目标是在 Linux / Docker 中运行权威游戏逻辑，每次入房导入手机当前的本地角色，结束后可靠地把结果交回手机。

**当前还不能联机玩。** 已完成静态模块检查、内部入口编解码、控制进程及私人 Mac 候选准备工具；基线和独立观察变体均已在 Mac 上进入单人世界并保存退出，观察窗口内系统/玩法载荷完成逐通道对照。Linux 房间工作进程、双进程桥接、四人入场与角色恢复仍未通过 G0–G5，HTTP 就绪和能力声明保持未开放。

## 当前能力

| 组件或记录 | 当前状态 |
|---|---|
| `d2r-inspect` | 校验自有 ARM64 Guest 的 `main` / `sdk` 模块与配套 fixups，逐节重建并输出来源清单。 |
| `internal/guestingress` | 精确编解码单条内部队列项及接收缓冲区，并取得自有副本；尚未接入游戏或网络。 |
| `d2r-trace` / 原生观察器 | 固定 Darwin ARM64 私人研究窗口的记录与紧凑报告，发送尝试不等于投递；不是网络桥接。 |
| `d2r-server` | 提供健康、就绪和能力查询，支持带超时的关闭；游戏工作进程保持不可用。 |
| [`tools/mac-fixture`](tools/mac-fixture/README.md) | 使用固定上游源码与自有资源，生成真实数据表、MSL、运行元数据和 Mac 宿主。另可构建独立观察变体；两个入口均不启动游戏。 |
| 私人 Mac 执行基线 | 历史基线推进 159 tick；后续观察变体移动场景推进 213 tick，系统 62 条、玩法 26 条输入与各自出队逐条一致，正常保存退出。 |
| Linux `amd64` / `arm64` | Go 工具与控制进程可交叉构建；原游戏 Linux 执行仍待资格验证，服务器 CPU 未定。 |
| 四人联机与可靠结果返回 | 设计已定义，尚未通过运行验收。 |

限定 Hopper/Ghidra 分析已经查明内部双队列与接收长度约定。静态默认下行方法只服务本地连接 `0`，发送字节范围也不等于客户端事件边界；远程路由和完整生命周期仍需验证。[G0 静态报告](docs/research/2026-10-20-g0-transport-boundaries.md)与 [Mac 运行报告](docs/research/2026-10-20-mac-runtime-fixture.md)分别记录静态和实际执行证据；[原生观察报告](docs/research/2026-10-20-native-transport-probe.md)记录后续独立变体、窗口屏障及实际逐通道消息对照。

## 构建与控制进程

需要 Go 1.26 和 Make；CI 与 Docker 固定 Go 1.26.10。普通检查只使用合成数据，无需游戏文件。

```sh
make check
make build
./bin/d2r-server
```

控制进程默认监听 `127.0.0.1:8080`，关闭超时为 `5s`，可显式设置：

```sh
./bin/d2r-server -listen 127.0.0.1:8080 -shutdown-timeout 5s
```

| 接口 | 当前语义 |
|---|---|
| `GET /healthz` | 进程存活时返回 200。 |
| `GET /readyz` | 房间引擎未通过资格验证，返回 503。Mac 单机运行不改变这一状态。 |
| `GET /api/v1/capabilities` | 返回实际可用能力，以及四人房间的计划约束。 |

```json
{"game_worker":"unavailable","multiplayer_available":false,"target_max_players_per_room":4}
```

其他构建入口：

```sh
make cross-build
make docker-test
make docker-build
docker run --rm -p 127.0.0.1:8080:8080 d2r-server:dev
```

最终 `scratch` 镜像使用 UID/GID `65532:65532`，容器内监听 `0.0.0.0:8080`，只包含 Go 工具（含 `d2r-trace`）与控制进程。游戏文件、存档、Mac 候选和原游戏工作进程均不进入公开镜像。

## 检查自有模块

把匹配的 Guest 和对应模块的 fixups 放在忽略的 `.tmp/` 中。输出目录必须不存在，父目录必须存在。

```sh
mkdir -p .tmp/inspect
./bin/d2r-inspect -module main \
  -guest .tmp/inputs/Guest \
  -fixups .tmp/inputs/main.fixups.tsv \
  -out .tmp/inspect/main
./bin/d2r-inspect -module sdk \
  -guest .tmp/inputs/Guest \
  -fixups .tmp/inputs/sdk.fixups.tsv \
  -out .tmp/inspect/sdk
```

CLI 的 `-module` 默认 `main`；Go API 必须显式选择模块。清单使用 schema 2，包含 `module` 与 `layout_profile: "dm-split-sections-v1"`，分别输出 `main.patched.reconstructed.bin` 或 `sdk.patched.reconstructed.bin`。工具不认证 Apple 签名，不初始化或执行游戏。

自有 v1.0.18 输入的 main 重建为 54,378,496 字节，SHA-256 `30384eabe394cbc6b6a0b7f3788983d58dc14a92b7abe1638853dc4833cf81f1`；sdk 为 15,204,352 字节，SHA-256 `d48f2cc33f3e506d623f873ba447c88fe0aea989b98c4057e2481b6ca3988fa2`。这些模块字节不是独立可执行服务器。

## 准备私人 Mac 候选

需要 Apple Silicon Mac、macOS 26、Xcode 工具链，以及预先提供的 .NET 10、SPIRV-Cross、uv 和自有游戏输入。完整输入命令、阶段及输出限制见[准备工具文档](tools/mac-fixture/README.md)。

准备工具从固定提交归档上游宿主到私人输出，不复制到公开源码，也不修改提供的工作区。它先快照运行元数据，使用同一份输入检查并发布全部运行文件，再生成匹配实际 MSL 链的 C1 元数据。准备成功后仍需另行建立受约束的执行实验；工具没有自动启动游戏的步骤。

观察变体构建入口同样见工具文档；它只对原始 `main.c` 单元增加初始化包装宏，不修改上游源文件字节。运行采样需要显式停止标记和在途调用屏障，不能依赖宿主退出自动关闭。

合成测试通过以下入口运行，环境及缓存都留在仓库 `.tmp/`：

```sh
make fixture-test
# 下面的原生合成检查要求 Darwin ARM64
make native-probe-test
```

该入口使用 uv 锁文件和 Python 3.12，当前 23 项测试通过。Go 完整检查的核心覆盖率为 91.2%。CI 已配置 Linux/macOS 合成测试及 `macos-26` ARM 原生测试；配置与该分支实际运行结果分别核对，公共 CI 不接触专有资源或玩家存档。

## 仓库与文档

本仓库负责服务端和资格工具；[手机 fork](https://github.com/codetrek/d2r-mobile)保留联机入口、客户端桥接和存档交接，以便跟进上游。共享代码保持单一来源，目前尚未建立共享 SDK。源码基线为 [jhaizhou-ops/d2r-mobile](https://github.com/jhaizhou-ops/d2r-mobile) 的 `120fe55c02a5633476998a44f8c3c3c809fa48b0`。[`upstream.lock.json`](upstream.lock.json)只记录来源及限定资格摘要，不是运行依赖或配置。

- [四人私服设计及 G0–G5](docs/design/2026-10-20-d2r-server-design.md)
- [协议、运行时与 REA 证据](docs/design/2026-10-20-protocol-research.md)
- [G0 内部传输边界](docs/research/2026-10-20-g0-transport-boundaries.md)
- [Mac 准备与单机执行基线](docs/research/2026-10-20-mac-runtime-fixture.md)
- [原生观察、窗口完整性及实际消息对照](docs/research/2026-10-20-native-transport-probe.md)
- [开发、来源及私人材料管理](docs/development.md)

接下来在已固定的原生观察基线上补齐消费端与生命周期证据，再验证双进程往返，再分别推进 Linux 无界面生命周期、四人状态归属和一致结果恢复。Mac 正常保存记录不能替代服务器检查点或断电持久化验证。
