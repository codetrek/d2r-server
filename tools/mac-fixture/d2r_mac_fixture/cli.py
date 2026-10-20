import argparse
from pathlib import Path
import subprocess
import sys

from .prepare import Options, prepare


def main() -> int:
    parser = argparse.ArgumentParser(description="从自有输入准备私有 Mac 运行候选；不执行游戏。")
    for name in ("app", "resources", "source", "dotnet", "spirv-cross", "inspector", "out"):
        parser.add_argument("--" + name, type=Path, required=True)
    parser.add_argument("--jobs", type=int, default=4)
    parser.add_argument("--stage-timeout-minutes", type=float, default=30)
    parser.add_argument("--keep-failed", action="store_true")
    args = parser.parse_args()
    try:
        options = Options(*(getattr(args, name).expanduser().resolve() for name in
                            ("app", "resources", "source", "dotnet", "spirv_cross", "inspector", "out")),
                          jobs=args.jobs, timeout_minutes=args.stage_timeout_minutes,
                          keep_failed=args.keep_failed)
        manifest = prepare(options)
    except (OSError, ValueError, KeyError, RuntimeError, subprocess.SubprocessError) as error:
        print(f"准备失败：{error}", file=sys.stderr)
        return 1
    print(f"已准备未执行的运行候选：{options.out}")
    print(f"实际源码着色器链：{manifest['shader_chain']}；G0 未通过。")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
