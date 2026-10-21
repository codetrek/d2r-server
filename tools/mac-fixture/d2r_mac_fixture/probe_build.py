"""Build a distinct observer variant from a verified, unchanged prepared fixture."""

import argparse
from dataclasses import dataclass
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import platform
import shlex
import shutil
import subprocess
import sys
import tarfile

from .prepare import PRODUCT, UPSTREAM_REVISION, UPSTREAM_URL, verify_module_metadata
from .process import Runner, check_scopes, new_output, sha256, write_json


BASELINE_CFLAGS = "-O1 -g -Wall -Wno-unused-function -mmacosx-version-min=26.0 -DDM_SHELL=1"
MAIN_RENAME = "-Drun_module_inits=d2r_observed_run_module_inits"
HLE_RENAME = "-Dgate_obs_set=d2r_upstream_gate_obs_set"
UPSTREAM_TRANSLATION_UNITS = {"main.c": MAIN_RENAME, "hle.c": HLE_RENAME}
MODULE_PROFILE = {
    "main": (54378496, "30384eabe394cbc6b6a0b7f3788983d58dc14a92b7abe1638853dc4833cf81f1"),
    "sdk": (15204352, "d48f2cc33f3e506d623f873ba447c88fe0aea989b98c4057e2481b6ca3988fa2"),
}


@dataclass(frozen=True)
class ProbeOptions:
    fixture: Path
    probe_source: Path
    out: Path
    timeout_minutes: float = 30
    keep_failed: bool = False


def inside(root: Path, relative: str) -> Path:
    path = (root / relative).resolve()
    if path == root or root not in path.parents:
        raise ValueError(f"fixture artifact escapes its scope: {relative}")
    return path


def verify_file(path: Path, expected: str, size: int | None = None) -> None:
    if (size is not None and path.stat().st_size != size) or sha256(path) != expected:
        raise ValueError(f"input identity mismatch: {path}")


def verify_parent(fixture: Path, manifest: dict, manifest_hash: str) -> None:
    verify_file(fixture / "manifest.json", manifest_hash)
    if (manifest["schema"] != "d2r.private-mac-fixture.v1"
            or manifest["state"] != "prepared_unexecuted"
            or manifest["upstream"]["revision"] != UPSTREAM_REVISION
            or manifest["upstream"]["repository"] != UPSTREAM_URL
            or manifest["upstream"]["working_tree_used"] is not False
            or manifest["host"]["compile_flag"] != "DM_SHELL=1"
            or manifest["host"]["minimum_macos"] != "26.0"):
        raise ValueError("parent fixture does not match the supported prepared baseline")
    if (fixture / "source/dm/PRODUCT").read_text().strip() != PRODUCT:
        raise ValueError("parent source product differs from the native profile")
    verify_file(inside(fixture, manifest["host"]["path"]), manifest["host"]["sha256"])
    for module, (size, digest) in MODULE_PROFILE.items():
        inspected = manifest["module_inspection"][module]
        if (inspected["module"] != module or inspected["schema_version"] != 2
                or inspected["layout_profile"] != "dm-split-sections-v1"
                or inspected["reconstructed"]["sha256"] != digest
                or inspected["reconstructed"]["size"] != size
                or inspected["relocations_applied"] or inspected["runtime_initialization_performed"]
                or not inspected["source_sections_verified"]):
            raise ValueError(f"module differs from native profile: {module}")
        expected_names = {f"{module}.{suffix}" for suffix in ("patched.bin", "fixups.tsv", "gates.tsv")}
        if set(inspected["runtime_files"]) != expected_names:
            raise ValueError(f"runtime file set differs from the required module contract: {module}")
        for name, identity in inspected["runtime_files"].items():
            if identity["path"] != f"runtime-modules/{name}":
                raise ValueError("runtime file must use the canonical prepared path")
            verify_file(inside(fixture, identity["path"]), identity["sha256"], identity["size"])
        if (inspected["runtime_files"][f"{module}.patched.bin"]["sha256"] != digest
                or inspected["runtime_files"][f"{module}.fixups.tsv"]["sha256"]
                != inspected["input_fixups"]["sha256"]):
            raise ValueError(f"runtime file identity differs from inspected input: {module}")
    verify_module_metadata(manifest["module_inspection"], fixture)
    if (set(manifest["generated_header_sha256"]) != {"dm_vault_tab.h", "dm_hud_exp.h", "dm_bright_levels.inc"}
            or set(manifest["private_source_config_sha256"]) !=
            {"dm/gfx/tools/dmsh_chain.lock", "dm/gfx/nvn/dmsh_chain.h"}):
        raise ValueError("parent generated-header or shader-configuration set differs from the baseline")
    for name, digest in manifest["generated_header_sha256"].items():
        verify_file(inside(fixture / "source/dm/host", name), digest)
    for name, digest in manifest["private_source_config_sha256"].items():
        verify_file(inside(fixture / "source", name), digest)
    for name, digest in manifest["metadata_sha256"].items():
        verify_file(inside(fixture / "metadata", name), digest)
    chain = manifest["shader_chain"]
    if not (fixture / "metadata/c1_full.tsv").read_text().startswith(f"# chain b-{chain} "):
        raise ValueError("parent C1 metadata does not match its shader chain")
    proof = json.loads((fixture / "shader-qualification.json").read_text())
    if proof["chain"] != chain or proof["keys"] != manifest["shader_keys"]:
        raise ValueError("parent shader qualification differs from its manifest")
    for artifact in proof["artifacts"]:
        verify_file(inside(fixture, artifact["path"]), artifact["sha256"], artifact["size"])
    app = Path(manifest["input_paths"]["app"])
    verify_file(app / "Frameworks/Guest.framework/Guest", manifest["inputs"]["guest"])
    verify_file(app / "romfs_overlay/data/.build.config", manifest["inputs"]["config"])
    for identity in manifest["resource_identities"].values():
        verify_file(Path(identity["path"]), identity["sha256"], identity["size"])


def verify_source_archive(archive: Path, source: Path, config: dict[str, str]) -> dict:
    records = {}
    with tarfile.open(archive) as tar:
        for member in tar.getmembers():
            name = PurePosixPath(member.name)
            if name.is_absolute() or ".." in name.parts or not name.parts or name.parts[0] != "dm":
                raise ValueError("unexpected pinned source archive path")
            path = source.joinpath(*name.parts)
            if member.isdir():
                continue
            if member.issym():
                if not path.is_symlink() or os.readlink(path) != member.linkname:
                    raise ValueError(f"upstream symlink changed: {member.name}")
                records[member.name] = {"symlink": member.linkname}
            elif member.isfile():
                stream = tar.extractfile(member)
                if stream is None:
                    raise ValueError(f"missing archive body: {member.name}")
                with stream:
                    original_hash = hashlib.file_digest(stream, "sha256").hexdigest()
                digest = config[member.name] if member.name in config else original_hash
                if path.is_symlink():
                    raise ValueError(f"upstream file replaced by a symlink: {member.name}")
                verify_file(path, digest)
                records[member.name] = {"sha256": digest, "original_git_sha256": original_hash}
            else:
                raise ValueError(f"unsupported source member: {member.name}")
    if set(config) - set(records):
        raise ValueError("parent configuration override is not an upstream file")
    return records


def recheck_source(source: Path, records: dict) -> None:
    for name, identity in records.items():
        path = source / name
        if "symlink" in identity:
            if not path.is_symlink() or os.readlink(path) != identity["symlink"]:
                raise ValueError(f"upstream symlink changed during build: {name}")
        else:
            if path.is_symlink():
                raise ValueError(f"upstream file became a symlink: {name}")
            verify_file(path, identity["sha256"])


def parse_host_command(output: str) -> tuple[list[str], list[str], list[str]]:
    commands = []
    for line in output.splitlines():
        tokens = shlex.split(line)
        if tokens and Path(tokens[0]).name == "clang":
            commands.append(tokens)
    if len(commands) != 1:
        raise ValueError("dry make must produce exactly one clang host command")
    command = commands[0]
    if command.count("-o") != 1 or any(command.count(name) != 1 for name in
                                      (*UPSTREAM_TRANSLATION_UNITS, "loader.c")):
        raise ValueError("baseline host command must contain one main.c, hle.c, loader.c and output")
    output_index = command.index("-o")
    if command[output_index + 1] != "dmhost":
        raise ValueError("baseline command has an unexpected output")
    prefix = command[:output_index]
    if prefix[1:1 + len(shlex.split(BASELINE_CFLAGS))] != shlex.split(BASELINE_CFLAGS):
        raise ValueError("baseline compiler flags differ from the prepared contract")
    if any(token.startswith(("-Drun_module_inits", "-Dgate_obs_set")) for token in prefix):
        raise ValueError("translation-unit instrumentation cannot enter baseline compiler flags")
    body = command[output_index + 2:]
    framework_index = body.index("-framework")
    sources, framework_flags = body[:framework_index], body[framework_index:]
    if (not sources or any(Path(path).suffix not in (".c", ".m", ".S") for path in sources)
            or len(set(sources)) != len(sources)):
        raise ValueError("baseline compiler command contains unexpected or duplicate source inputs")
    return prefix, sources, framework_flags


def symbol_rows(output: str, symbol: str) -> list[str]:
    return [line.split()[-2] for line in output.splitlines()
            if len(line.split()) >= 2 and line.split()[-1] == symbol]


def verify_symbols(main_object: str, hle_object: str, final_binary: str, required: list[str]) -> dict:
    wrapper, original = "_d2r_observed_run_module_inits", "_run_module_inits"
    if symbol_rows(main_object, wrapper) != ["U"] or symbol_rows(main_object, original):
        raise ValueError("original main object must reference only the observed init wrapper")
    gate_wrapper, gate_original = "_gate_obs_set", "_d2r_upstream_gate_obs_set"
    if (symbol_rows(hle_object, gate_original) != ["T"]
            or symbol_rows(hle_object, gate_wrapper)):
        raise ValueError("original hle object must define only the renamed upstream gate setter")
    result = {}
    for symbol in ("_" + name for name in required):
        rows = symbol_rows(final_binary, symbol)
        if len(rows) != 1 or rows[0] not in ("T", "t"):
            raise ValueError(f"final binary must define exactly one required symbol: {symbol}")
        result[symbol] = rows[0]
    return {"main_object_undefined": wrapper, "hle_object_single_definition": gate_original,
            "final_single_definitions": result}


def validate_native_profile(profile: dict) -> None:
    if (profile["schema_version"] != 1 or profile["upstream_revision"] != UPSTREAM_REVISION
            or profile["platform"] != "darwin" or profile["architecture"] != "arm64"
            or profile["pointer_bytes"] != 8 or profile["trace_format_version"] != 2
            or profile["main_translation_unit_define"] != MAIN_RENAME.removeprefix("-D")
            or profile["hle_translation_unit_define"] != HLE_RENAME.removeprefix("-D")
            or profile["wrapper_symbol"] != "d2r_observed_run_module_inits"
            or profile["original_init_symbol"] != "run_module_inits"
            or profile["gate_set_symbol"] != "gate_obs_set"
            or profile["original_gate_set_symbol"] != "d2r_upstream_gate_obs_set"
            or profile["integration_source"] != "integration.c"
            or profile["build_sources"] != ["integration.c", "profile.c", "observer.c", "shims.S",
                                            "lifecycle.c", "gate.c", "relay.c"]
            or profile["build_headers"] != ["format.h", "abi.h", "probe.h", "internal.h", "lifecycle.h", "relay.h"]
            or len(profile["required_symbols"]) != 12
            or set(profile["required_symbols"]) != {"d2r_observed_run_module_inits", "run_module_inits",
                "d2r_probe_server_send", "d2r_probe_client_send", "d2r_probe_system_pop", "d2r_probe_gameplay_pop",
                "d2r_probe_client_high_pop", "d2r_probe_client_low_pop", "gate_obs_set",
                "d2r_upstream_gate_obs_set", "d2r_pipe_configure", "d2r_pipe_close"}
            or profile["required_environment"] != ["D2R_PROBE_MODULES", "D2R_TRANSPORT_TRACE",
                                                    "D2R_PROBE_STOP_PATH", "D2R_LIFECYCLE_TRACE", "D2R_PROBE_MODE"]
            or profile["probe_modes"] != ["observe", "relay"]
            or profile["relay_environment"] != ["D2R_RELAY_READ_FD", "D2R_RELAY_WRITE_FD"]):
        raise ValueError("native observer source profile differs from the supported build contract")
    for module, (size, digest) in MODULE_PROFILE.items():
        if profile["modules"][module] != {"filename": f"{module}.patched.bin", "size": size, "sha256": digest}:
            raise ValueError(f"native observer module profile differs: {module}")


def snapshot_native_sources(source: Path, root: Path) -> tuple[Path, dict[str, str]]:
    native = root / "native-source"
    native.mkdir()
    hashes = {}
    for path in sorted(source.rglob("*")):
        if path.is_symlink():
            raise ValueError("native observer source must not contain symlinks")
        if path.is_file():
            name = str(path.relative_to(source))
            destination = native / name
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(path, destination)
            hashes[name] = sha256(destination)
            verify_file(path, hashes[name])
            destination.chmod(0o444)
    return native, hashes


def verify_native_sources(source: Path, snapshot: Path, hashes: dict[str, str]) -> None:
    if any(path.is_symlink() for path in source.rglob("*")):
        raise ValueError("native source acquired a symlink during build")
    if set(hashes) != {str(path.relative_to(source)) for path in source.rglob("*") if path.is_file()}:
        raise ValueError("native source file set changed during build")
    for name, digest in hashes.items():
        verify_file(source / name, digest)
        verify_file(snapshot / name, digest)


def probe_environment(root: Path, fixture: Path) -> dict[str, str]:
    allowed = {"HOME", "USER", "LOGNAME", "PATH", "LANG", "LC_ALL", "LC_CTYPE", "TZ",
               "DEVELOPER_DIR", "__CF_USER_TEXT_ENCODING"}
    result = {name: value for name, value in os.environ.items() if name in allowed}
    for name in ("tmp", "clang-cache"):
        (root / name).mkdir()
    result.update({"TMPDIR": str(root / "tmp"), "CLANG_MODULE_CACHE_PATH": str(root / "clang-cache"),
                   "DM_ROOT": str(root), "DM_ROMFS_SRC": str(fixture / "generator-romfs"),
                   "PYTHONDONTWRITEBYTECODE": "1", "PYTHONNOUSERSITE": "1"})
    return result


def compile_variant(runner: Runner, prefix: list[str], sources: list[str], frameworks: list[str],
                    host: Path, native: Path, native_hashes: dict[str, str],
                    build_sources: list[str], root: Path) -> tuple[Path, dict[str, Path], list[str]]:
    objects = root / "objects"
    objects.mkdir()
    upstream_objects = {}
    for name, rename in UPSTREAM_TRANSLATION_UNITS.items():
        stem = Path(name).stem
        output = objects / f"upstream-{stem}.o"
        runner.run(f"compile-{stem}", [*prefix, rename, "-c", name, "-o", str(output)], host)
        upstream_objects[name] = output
    native_objects = []
    for index, name in enumerate(build_sources):
        path = inside(native, name)
        if name not in native_hashes or path.suffix not in (".c", ".S"):
            raise ValueError(f"native build source is not a recorded C/assembly input: {name}")
        output = objects / f"native-{index}.o"
        runner.run(f"compile-native-{index}", [*prefix, "-I", str(native), "-I", str(host),
                   "-c", str(path), "-o", str(output)], host)
        native_objects.append(str(output))
    (root / "bin").mkdir()
    binary = root / "bin/dmhost-transport-probe"
    remaining = [name for name in sources if name not in upstream_objects]
    runner.run("link-host", [*prefix, "-o", str(binary), *remaining,
               *(str(path) for path in upstream_objects.values()), *native_objects, *frameworks], host)
    return binary, upstream_objects, remaining


def build(options: ProbeOptions) -> dict:
    if sys.platform != "darwin" or platform.machine() != "arm64":
        raise ValueError("the native transport observer build requires Apple Silicon macOS")
    if options.timeout_minutes <= 0:
        raise ValueError("stage timeout must be positive")
    parent_hash = sha256(options.fixture / "manifest.json")
    parent = json.loads((options.fixture / "manifest.json").read_text())
    dependencies = (options.fixture, options.probe_source,
                    *(Path(parent["input_paths"][name]) for name in ("app", "resources", "source_repository")))
    check_scopes(options.out, dependencies)
    verify_parent(options.fixture, parent, parent_hash)
    with new_output(options.out, options.keep_failed) as root:
        env = probe_environment(root, options.fixture)
        runner = Runner(root, env, options.timeout_minutes * 60)
        runner.run("source-archive", ["git", "-C", parent["input_paths"]["source_repository"], "archive",
                   "--format=tar", "-o", str(root / "upstream.tar"), UPSTREAM_REVISION, "dm"], root)
        verify_file(root / "upstream.tar", parent["upstream"]["archive_sha256"])
        source = options.fixture / "source"
        source_records = verify_source_archive(root / "upstream.tar", source,
                                               parent["private_source_config_sha256"])
        (root / "upstream.tar").unlink()
        runner.run("clang-location", ["xcrun", "-sdk", "macosx", "--find", "clang"], root)
        runner.run("sdk-location", ["xcrun", "-sdk", "macosx", "--show-sdk-path"], root)
        clang = Path((root / "logs/clang-location.log").read_text().strip())
        sdk = Path((root / "logs/sdk-location.log").read_text().strip())
        if not clang.is_file() or not sdk.is_dir():
            raise ValueError("selected Xcode C compiler or macOS SDK is unavailable")
        env["SDKROOT"] = str(sdk)
        env["PATH"] = str(clang.parent) + os.pathsep + env["PATH"]
        host = source / "dm/host"
        stages = [stage for stage in parent["stages"] if stage["stage"] == "host-build"]
        if len(stages) != 1 or f"CFLAGS={BASELINE_CFLAGS}" not in stages[0]["command"]:
            raise ValueError("parent does not record the supported baseline host compilation")
        runner.run("baseline-command", ["make", "-n", "-B", "-C", str(host), "dmhost",
                   f"CFLAGS={BASELINE_CFLAGS}", f"ROMFS={options.fixture / 'generator-romfs'}"], root)
        prefix, sources, framework_flags = parse_host_command((root / "logs/baseline-command.log").read_text())
        prefix[0] = str(clang)
        for name in sources:
            relative = str((host / name).resolve().relative_to(source))
            if relative not in source_records:
                raise ValueError(f"unverified source entered the host compile command: {name}")
        native, native_hashes = snapshot_native_sources(options.probe_source, root)
        profile = json.loads((native / "profile.json").read_text())
        validate_native_profile(profile)
        binary, upstream_objects, remaining = compile_variant(
            runner, prefix, sources, framework_flags, host, native, native_hashes,
            profile["build_sources"], root)
        for name, path in upstream_objects.items():
            runner.run(f"nm-{Path(name).stem}", ["xcrun", "nm", "-g", str(path)], root)
        runner.run("nm-host", ["xcrun", "nm", "-g", str(binary)], root)
        symbols = verify_symbols((root / "logs/nm-main.log").read_text(),
                                 (root / "logs/nm-hle.log").read_text(),
                                 (root / "logs/nm-host.log").read_text(), profile["required_symbols"])
        recheck_source(source, source_records)
        verify_parent(options.fixture, parent, parent_hash)
        verify_native_sources(options.probe_source, native, native_hashes)
        manifest = {
            "schema": "d2r.private-mac-transport-probe.v1", "state": "built_unexecuted",
            "parent_fixture": {"path": str(options.fixture), "manifest_sha256": parent_hash,
                               "host_sha256": parent["host"]["sha256"], "shader_chain": parent["shader_chain"]},
            "upstream": parent["upstream"], "upstream_source_identity": source_records,
            "native_source": {"path": str(options.probe_source), "sha256": native_hashes, "profile": profile},
            "instrumentation": {"main_macro": MAIN_RENAME, "main_source": "dm/host/main.c",
                                "main_source_sha256": source_records["dm/host/main.c"]["sha256"],
                                "main_object_sha256": sha256(upstream_objects["main.c"]),
                                "hle_macro": HLE_RENAME, "hle_source": "dm/host/hle.c",
                                "hle_source_sha256": source_records["dm/host/hle.c"]["sha256"],
                                "hle_object_sha256": sha256(upstream_objects["hle.c"]), "symbols": symbols,
                                "original_main_occurrences": sources.count("main.c"),
                                "original_hle_occurrences": sources.count("hle.c"),
                                "original_loader_occurrences": remaining.count("loader.c"),
                                "baseline_cflags": BASELINE_CFLAGS},
            "host": {"path": "bin/dmhost-transport-probe", "sha256": sha256(binary)},
            "compiler": {"path": str(clang), "sha256": sha256(clang), "sdk": str(sdk)},
            "builder_sha256": sha256(Path(__file__)),
            "runtime_requirements": {"D2R_PROBE_MODULES": str(options.fixture / "runtime-modules"),
                                     "D2R_TRANSPORT_TRACE": "Explicit NEW private capture path required at execution",
                                     "D2R_PROBE_STOP_PATH": "Explicit absolute private stop-marker path, absent at installation",
                                     "D2R_LIFECYCLE_TRACE": "Explicit NEW private lifecycle diagnostic path required at execution",
                                     "D2R_PROBE_MODE": "Explicit observe or relay; no automatic selection",
                                     "D2R_RELAY_READ_FD": "Owned response pipe descriptor; relay only, absent for observe",
                                     "D2R_RELAY_WRITE_FD": "Owned request pipe descriptor; relay only, absent for observe"},
            "capture_shutdown": "After the save-exit goal, create this run's regular stop file; wait for CLOSED before terminating the host.",
            "stages": runner.receipts,
            "limitations": ["No engine, shader, transport or multiplayer execution was performed by this builder.",
                            "The binary is a distinct instrumented variant; parent runtime receipts do not qualify it.",
                            "The original upstream sources are unchanged; main and hle translation-unit macros change binary identity.",
                            "The output depends on the unchanged parent fixture and its original app/resource/source paths."],
        }
        write_json(root / "manifest.json", manifest)
    return manifest


def main() -> int:
    parser = argparse.ArgumentParser(description="构建独立的原生传输观察变体；不执行游戏。")
    for name in ("fixture", "probe-source", "out"):
        parser.add_argument("--" + name, required=True, type=Path)
    parser.add_argument("--stage-timeout-minutes", type=float, default=30)
    parser.add_argument("--keep-failed", action="store_true")
    args = parser.parse_args()
    try:
        options = ProbeOptions(*(getattr(args, name).expanduser().resolve() for name in
                                 ("fixture", "probe_source", "out")),
                               timeout_minutes=args.stage_timeout_minutes, keep_failed=args.keep_failed)
        result = build(options)
    except (OSError, ValueError, KeyError, RuntimeError, subprocess.SubprocessError) as error:
        print(f"观察变体构建失败：{error}", file=sys.stderr)
        return 1
    print(f"已构建未执行的独立观察变体：{options.out}")
    print(f"宿主 SHA256：{result['host']['sha256']}；G0 未通过。")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
