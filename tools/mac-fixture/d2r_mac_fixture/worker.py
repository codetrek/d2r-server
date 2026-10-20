"""Execute unchanged upstream preparation scripts in a scoped child process."""

import argparse
from contextlib import redirect_stdout
import importlib
import os
from pathlib import Path
import runpy
import sys

from .process import sha256


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("action", choices=("vault", "hud", "bright", "lock", "header", "translate"))
    parser.add_argument("--source", type=Path, required=True)
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--main-sha256")
    args = parser.parse_args()
    dm = args.source / "dm"
    generators = {
        "vault": (dm / "ios/tools/gen_vault_table.py", dm / "host/dm_vault_tab.h"),
        "hud": (dm / "ios/tools/gen_hud_exp.py", dm / "host/dm_hud_exp.h"),
        "bright": (dm / "tools/gen_bright_levels.py", dm / "host/dm_bright_levels.inc"),
    }
    if args.action in generators:
        script, output = generators[args.action]
        sys.path.insert(0, str(script.parent))
        sys.argv = [str(script), str(args.root / "generator-romfs")]
        with output.open("w", encoding="utf-8") as stream, redirect_stdout(stream):
            runpy.run_path(str(script), run_name="__main__")
        if not output.stat().st_size:
            raise ValueError(f"generator produced an empty header: {output.name}")
        print(f"Generated {output.name}: {output.stat().st_size} bytes", flush=True)
        return
    tools = dm / "gfx/tools"
    if args.action == "translate":
        image_path = args.root / "runtime-modules/main.patched.bin"
        if not args.main_sha256 or sha256(image_path) != args.main_sha256:
            raise ValueError("main layout does not match the byte-preserving inspector result")
        image = image_path.read_bytes()
        sys.path.insert(0, str(dm / "verify"))
        nsoimg = importlib.import_module("nsoimg")

        def read_layout(path: str) -> bytes:
            if Path(path).resolve() != image_path.resolve():
                raise ValueError("only the explicitly inspected main layout may be read")
            return image

        # We adapt the static reader's input representation. No guest bytes or
        # upstream implementation files are changed, and no original NSO is claimed.
        nsoimg.load = read_layout
        os.environ["MAIN"] = str(image_path)
        arguments = [str(tools / "dmsh_build.py"), str(args.root / "shader-packs"),
                     str(args.root / "shader-cache")]
    else:
        arguments = [str(tools / "dmsh_chain.py"),
                     "--update-lock" if args.action == "lock" else "--header"]
    script = tools / "nometal_run.py"
    sys.argv = [str(script), *arguments]
    runpy.run_path(str(script), run_name="__main__")


if __name__ == "__main__":
    main()
