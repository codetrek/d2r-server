# 私有 Mac 运行候选与观察变体构建工具

本子项目提供两个独立入口：`d2r-mac-fixture` 从使用者自有的 iPhone App、资源档案和固定上游源码准备基线运行候选；`d2r-mac-probe-build` 在已经准备好的基线上构建原生传输观察变体。两个入口都只生成文件，**不执行游戏**。构建成功不代表启动、Metal 编译、联网或 G0 已通过。

当前支持 `d2r-mobile` 提交 `120fe55c02a5633476998a44f8c3c3c809fa48b0`，对应 `3.3.94012-r44`。完整上游宿主和游戏资料仅存在于私有输出中。

## 环境与目录约束

需要 Apple Silicon Mac、macOS 26 或以上、可用的 Xcode C 编译器和 macOS SDK，以及 Python 3.11 或以上与 uv。Python 依赖由本子项目的 `uv.lock` 固定。把 `UV_CACHE_DIR`、`UV_PROJECT_ENVIRONMENT` 和需要时的 `UV_PYTHON_INSTALL_DIR` 指向仓库 `.tmp/`，让环境和缓存留在项目内；完整命令的集成入口由仓库根目录维护。

所有命令的输出父目录须已存在，输出本身须不存在，且不能与任何输入目录重叠。工具在同一父目录建立独占暂存目录，全部阶段成功后使用操作系统的“不替换”重命名原子发布；并发创建的目标也会被保留。失败默认删除本次暂存内容，`--keep-failed` 可保留调查材料。外部阶段采用独立进程组、流式日志和超时清理，`--stage-timeout-minutes` 设置每阶段上限，默认 30 分钟。

构建环境只继承常规用户、路径、语言及 Xcode 选择信息，不继承游戏启动或观察文件环境变量。工具不会安装系统工具，不写原 App、资源档案、上游 Git 工作区或父候选。

## 准备基线候选

额外需要预先准备的 .NET 10 SDK、SPIRV-Cross，以及本仓库构建且支持 `-module main`、`-module sdk` 的 `d2r-inspect`。首次 .NET 调用起就关闭开发证书生成，并把 CLI、NuGet、临时文件和编译缓存限定在输出暂存目录中。NuGet 还原使用独立配置，只连接公开 NuGet 源。

在仓库根目录运行，将尖括号中的内容替换为实际路径：

```sh
UV_CACHE_DIR="$PWD/.tmp/uv" UV_PROJECT_ENVIRONMENT="$PWD/.tmp/venv-mac-fixture" \
UV_PYTHON_INSTALL_DIR="$PWD/.tmp/python" uv run --locked --project tools/mac-fixture d2r-mac-fixture \
  --app '<已解包的 iPhone App.app>' \
  --resources '<包含 data/ 的游戏资源文件夹>' \
  --source '<含固定上游提交的 Git 仓库>' \
  --dotnet '<实际 dotnet 可执行文件>' \
  --spirv-cross '<实际 spirv-cross 可执行文件>' \
  --inspector '<当前仓库构建的 d2r-inspect>' \
  --out '<尚不存在的私有基线输出目录>'
```

准备流程完成以下工作：

1. 从上游 Git 仓库导出固定提交的 `dm/` 快照，工作区未提交改动不进入构建。先统一快照 main、sdk 的 fixups 和 gates，让检查器与运行文件使用相同输入；逐一记录并核对全部运行文件的大小和哈希。重建模块按字节不变地复制为 `.patched.bin`，不声称恢复了原始 NSO。
2. 校验 App 覆盖层中的 TVFS/BLTE 索引与存储内容，恢复带类型的索引。文件表大小、CFT 内容大小和编码字符串表字节偏移分别保留。真实内容的校验值用于未修改的上游生成器；原提取工具的 FileId、Flags 和缺失资源不会冒充为已恢复内容。
3. 运行上游生成器，生成三个真实宿主头文件；恢复真实着色器包及 main 中的内嵌着色器，核对内嵌控制段键。
4. 编译上游 .NET 翻译器，通过上游 nometal 路径生成完整 MSL 源码、实际工具指纹及独立着色器链。缓存中的 `.metallib` 扩展名沿用上游约定，内容是源码，不是已编译的 Metal 库。
5. 从实际 MSL 运行 `c1_guard.py`，将匹配当前链的 C1 元数据、原始着色器映射和管线预热清单放在一起，随后构建带 `DM_SHELL` 的 Mac 宿主。

`--jobs` 设置着色器并发数，默认 4。清单、日志和阶段回执记录输入、工具、源码、模块、资源、着色器与宿主身份。私有源码快照中允许变化的配置只有实际工具指纹锁和生成的链头；上游实现不会被静默修改。

资源通过只读引用供生成器使用。发布前会复核资源档案、原始 fixups/gates、私有元数据快照和全部运行模块文件；输入改变会使准备失败。部分精简资源可能缺少语言语音档案，索引会报告缺失，实际生成所需内容缺失时直接失败。成功不能证明完整原始资源集合已恢复。

## 构建原生传输观察变体

此入口接受已经成功发布的基线候选，以及本仓库的 `native/transport-probe/` 源码，不需要重新生成数据表或翻译着色器：

```sh
UV_CACHE_DIR="$PWD/.tmp/uv" UV_PROJECT_ENVIRONMENT="$PWD/.tmp/venv-mac-fixture" \
UV_PYTHON_INSTALL_DIR="$PWD/.tmp/python" uv run --locked --project tools/mac-fixture d2r-mac-probe-build \
  --fixture '<已准备好的私有基线目录>' \
  --probe-source '<本仓库的 native/transport-probe 目录>' \
  --out '<尚不存在的私有观察变体目录>'
```

构建前后复核父清单、六份运行模块文件、原始元数据、宿主、资源、生成头文件、着色器链及缓存身份。再次导出固定上游提交并核对档案哈希，逐一比较上游文件与父候选源码；两个已记录的着色器配置文件使用父清单中明确登记的身份。原生观察源码先复制到独立只读快照，按 `profile.json` 中的固定平台、模块哈希、源码和 ABI 契约构建，并在发布前复核原件及快照。

工具用 `make -n -B` 读取上游实际宿主编译命令，校验其中唯一的 `main.c`、`hle.c` 和 `loader.c`，不会执行生成规则。把两个未修改的编译单元分别编译，宏各自只作用于对应文件：

```text
main.c: -Drun_module_inits=d2r_observed_run_module_inits
hle.c:  -Dgate_obs_set=d2r_upstream_gate_obs_set
```

其余原始宿主源码保留相同编译参数，`loader.c` 只链接一次。原生包装器在真实模块初始化返回后、进入 `nnMain` 前安装观察器；只有 `integration.c` 依赖上游 `dm.h`。构建回执保留编译命令、两个源文件及对象的哈希、编译器身份和符号检查：主对象只能引用初始化包装器，HLE 对象只能定义重命名后的真实注册函数；最终二进制中的两对原始函数/包装器及六个观察垫片分别只有一个定义。

观察变体是**不同的二进制**，清单状态为 `built_unexecuted`。父候选的运行回执不自动适用于它。构建过程不会运行观察器、创建传输捕获、改写 Guest 代码页或改变父候选。后续受控执行必须显式提供 `D2R_PROBE_MODULES`、新的私有 `D2R_TRANSPORT_TRACE` 路径，新的绝对路径 `D2R_LIFECYCLE_TRACE`，以及安装时不存在的绝对路径 `D2R_PROBE_STOP_PATH`。

控制方完成游戏与保存退出判据后，在停止路径创建属于本次运行的普通文件，等待两份捕获头发布 `CLOSED` 标志，再结束宿主。观察器分别停止传输和生命周期准入，等待已接纳的完整方法、主回调及后快照，再同步两份文件；关闭后的调用继续转发，捕获内容保持稳定。控制线程只检查宿主文件和采样状态，不读取 Guest 内存。当前变体固定 trace 版本 2：两个发送尝试、两个服务端 CID 前缀出队、两个客户端纯载荷出队。传输格式见原生源码目录的 `format.md`，独立状态格式见 `lifecycle.md`。状态仅包围原有宿主回调，不代表 Guest 完整函数；两份文件都不能提供断开因果或多人资格。历史传输 v1/v2 回执保持原有范围。

## 生命周期与验证边界

基线候选依赖原 App、资源及源码路径；观察变体还依赖原父候选。不能移动、删除或改变这些输入后继续视为同一份已核对运行资料。两个入口都不建立游戏进程沙箱，也没有启动游戏的命令；Linux、多人、角色接入和 G0 仍需独立证明。

```sh
UV_CACHE_DIR="$PWD/.tmp/uv" UV_PROJECT_ENVIRONMENT="$PWD/.tmp/venv-mac-fixture" \
UV_PYTHON_INSTALL_DIR="$PWD/.tmp/python" uv run --locked --project tools/mac-fixture \
  python -m unittest discover -s tools/mac-fixture/tests
```

测试只使用合成数据，覆盖大小差异、编码偏移、损坏索引、BLTE 校验、解压边界、路径与发布隔离、输入修改时序、gates 身份、外部阶段失败与清理，以及观察变体的编译命令拆分、源码身份、原生快照、固定画像和符号约束。公开测试不需要游戏程序或玩家存档。
