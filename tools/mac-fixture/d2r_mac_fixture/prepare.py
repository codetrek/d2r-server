from dataclasses import dataclass
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import plistlib
import platform
import re
import shutil
import sys
import tarfile

from .process import Runner, check_scopes, new_output, sha256, write_json
from .resources import prepare_resources, recover_index
from .tvfs import Limits


UPSTREAM_REVISION = "120fe55c02a5633476998a44f8c3c3c809fa48b0"
UPSTREAM_URL = "https://github.com/jhaizhou-ops/d2r-mobile"
PRODUCT = "3.3.94012-r44"


@dataclass(frozen=True)
class Options:
    app: Path
    resources: Path
    source: Path
    dotnet: Path
    spirv_cross: Path
    inspector: Path
    out: Path
    jobs: int = 4
    timeout_minutes: float = 30
    keep_failed: bool = False
    limits: Limits = Limits()


def extract_source(archive: Path, destination: Path) -> None:
    """Extract trusted pinned Git blobs without tar traversal or link side effects."""
    destination.mkdir()
    with tarfile.open(archive) as tar:
        links = []
        for member in tar.getmembers():
            path = PurePosixPath(member.name)
            if path.is_absolute() or ".." in path.parts or not path.parts or path.parts[0] != "dm":
                raise ValueError(f"unexpected source archive path: {member.name}")
            target = destination.joinpath(*path.parts)
            if member.isdir():
                target.mkdir(parents=True, exist_ok=True)
            elif member.isfile():
                target.parent.mkdir(parents=True, exist_ok=True)
                stream = tar.extractfile(member)
                if stream is None:
                    raise ValueError(f"missing source archive body: {member.name}")
                with stream, target.open("xb") as output:
                    shutil.copyfileobj(stream, output)
                target.chmod(member.mode & 0o777)
            elif member.issym():
                links.append((target, member.linkname))
            else:
                raise ValueError(f"unsupported source archive member: {member.name}")
        for target, link in links:
            resolved = (target.parent / link).resolve()
            if Path(link).is_absolute() or destination.resolve() not in resolved.parents:
                raise ValueError(f"source symlink escapes snapshot: {target}")
            target.parent.mkdir(parents=True, exist_ok=True)
            target.symlink_to(link)


def validate_inputs(options: Options) -> None:
    if sys.platform != "darwin" or platform.machine() != "arm64":
        raise ValueError("complete fixture preparation requires an Apple Silicon macOS host")
    if options.jobs < 1 or options.timeout_minutes <= 0:
        raise ValueError("jobs and stage timeout must be positive")
    check_scopes(options.out, (options.app, options.resources, options.source,
                              options.dotnet, options.spirv_cross, options.inspector))
    for tool in (options.dotnet, options.spirv_cross, options.inspector):
        if not tool.is_file() or not os.access(tool, os.X_OK):
            raise ValueError(f"explicit tool must be an executable file: {tool}")
    for path in (options.app / "Frameworks/Guest.framework/Guest",
                 options.app / "romfs_overlay/data/.build.config",
                 *(options.app / "dm" / f"{module}.{suffix}"
                   for module in ("main", "sdk") for suffix in ("fixups.tsv", "gates.tsv"))):
        if not path.is_file():
            raise FileNotFoundError(path)
    if not (options.resources / "data").is_dir():
        raise ValueError("--resources must identify the resource folder containing data/")
    with (options.app / "Info.plist").open("rb") as stream:
        app_info = plistlib.load(stream)
    info = dict(line.split("=", 1) for line in
                (options.app / "dm/shell_info.txt").read_text().splitlines() if "=" in line)
    if info.get("PRODUCT") != PRODUCT:
        raise ValueError(f"unsupported app product; expected documented identity {PRODUCT}")
    if app_info.get("DTPlatformName") != "iphoneos":
        raise ValueError("the app input must be an iPhone device package")


def environment(root: Path, options: Options) -> dict[str, str]:
    inherited = {"HOME", "USER", "LOGNAME", "PATH", "LANG", "LC_ALL", "LC_CTYPE",
                 "TZ", "DEVELOPER_DIR", "__CF_USER_TEXT_ENCODING"}
    result = {key: value for key, value in os.environ.items() if key in inherited}
    for name in ("tmp", "dotnet-home", "nuget/packages", "nuget/http", "tool-bin",
                 "clang-cache", "shader-cas"):
        (root / name).mkdir(parents=True)
    (root / "tool-bin/spirv-cross").symlink_to(options.spirv_cross)
    (root / "tool-bin/python3").symlink_to(sys.executable)
    result.update({
        "DOTNET_ROOT": str(options.dotnet.parent),
        "DOTNET_CLI_HOME": str(root / "dotnet-home"),
        "DOTNET_GENERATE_ASPNET_CERTIFICATE": "false",
        "DOTNET_SKIP_FIRST_TIME_EXPERIENCE": "1",
        "DOTNET_CLI_TELEMETRY_OPTOUT": "1",
        "DOTNET_CLI_WORKLOAD_UPDATE_NOTIFY_DISABLE": "true",
        "DOTNET_CLI_USE_MSBUILD_SERVER": "0",
        "MSBUILDDISABLENODEREUSE": "1",
        "MSBUILDTERMINALLOGGER": "off",
        "NUGET_PACKAGES": str(root / "nuget/packages"),
        "NUGET_HTTP_CACHE_PATH": str(root / "nuget/http"),
        "NUGET_PLUGINS_CACHE_PATH": str(root / "nuget/plugins"),
        "TMPDIR": str(root / "tmp"), "WORK": str(root / "tmp"),
        "CLANG_MODULE_CACHE_PATH": str(root / "clang-cache"),
        "DM_ROOT": str(root), "DM_ROMFS_SRC": str(root / "generator-romfs"),
        "DMSH_CAS": str(root / "shader-cas"),
        "XLATE": str(root / "build/gfx/dmsh_xlate/dmsh_xlate"),
        "PYTHONDONTWRITEBYTECODE": "1", "PYTHONNOUSERSITE": "1",
        "PYTHONPATH": str(Path(__file__).resolve().parent.parent),
        "PATH": str(root / "tool-bin") + os.pathsep + os.environ["PATH"],
        "J": str(options.jobs),
    })
    return result


def file_identity(path: Path) -> dict:
    before = path.stat()
    digest = sha256(path)
    after = path.stat()
    if (before.st_size, before.st_mtime_ns, before.st_ino) != (
            after.st_size, after.st_mtime_ns, after.st_ino):
        raise ValueError(f"input changed while fingerprinting: {path.name}")
    return {"size": after.st_size, "mtime_ns": after.st_mtime_ns,
            "inode": after.st_ino, "sha256": digest}


def snapshot_module_metadata(app: Path, root: Path) -> dict:
    directory = root / "input-snapshot/dm"
    directory.mkdir(parents=True)
    result = {}
    for module in ("main", "sdk"):
        for suffix in ("fixups.tsv", "gates.tsv"):
            name = f"{module}.{suffix}"
            original = app / "dm" / name
            identity = file_identity(original)
            snapshot = directory / name
            shutil.copyfile(original, snapshot)
            if (snapshot.stat().st_size != identity["size"]
                    or sha256(snapshot) != identity["sha256"]
                    or file_identity(original) != identity):
                raise ValueError(f"module metadata changed during snapshot: {name}")
            snapshot.chmod(0o444)
            result[name] = {"source_path": str(original),
                            "snapshot_path": str(snapshot.relative_to(root)), **identity}
    return result


def verify_module_metadata(modules: dict, root: Path) -> None:
    for module in modules.values():
        for name, identity in module["metadata_inputs"].items():
            expected = {key: identity[key] for key in ("size", "mtime_ns", "inode", "sha256")}
            if file_identity(Path(identity["source_path"])) != expected:
                raise ValueError(f"app module metadata changed during preparation: {name}")
            snapshot = root / identity["snapshot_path"]
            if snapshot.stat().st_size != identity["size"] or sha256(snapshot) != identity["sha256"]:
                raise ValueError(f"module metadata snapshot changed: {name}")
        for identity in module["runtime_files"].values():
            path = root / identity["path"]
            if path.stat().st_size != identity["size"] or sha256(path) != identity["sha256"]:
                raise ValueError(f"runtime module file changed: {path.name}")


def inspector_modules(options: Options, root: Path, runner: Runner) -> dict:
    metadata = snapshot_module_metadata(options.app, root)
    runtime = root / "runtime-modules"
    runtime.mkdir()
    result = {}
    for module in ("main", "sdk"):
        output = root / f"inspected-{module}"
        fixups = metadata[f"{module}.fixups.tsv"]
        runner.run(f"inspect-{module}", [str(options.inspector), "-module", module,
                   "-guest", str(options.app / "Frameworks/Guest.framework/Guest"),
                   "-fixups", str(root / fixups["snapshot_path"]),
                   "-out", str(output)], root)
        manifest = json.loads((output / "manifest.json").read_text())
        if (manifest["schema_version"] != 2 or manifest["module"] != module
                or manifest["layout_profile"] != "dm-split-sections-v1"
                or not manifest["source_sections_verified"]
                or manifest["relocations_applied"] or manifest["runtime_initialization_performed"]):
            raise ValueError(f"unexpected inspector contract: {module}")
        if (manifest["input_fixups"]["sha256"] != fixups["sha256"]
                or manifest["input_fixups"]["size"] != fixups["size"]):
            raise ValueError(f"inspector fixups identity differs from the owned snapshot: {module}")
        name = manifest["reconstructed"]["name"]
        if Path(name).name != name:
            raise ValueError("inspector artifact name must be a single path component")
        image = output / name
        if (sha256(image) != manifest["reconstructed"]["sha256"]
                or image.stat().st_size != manifest["reconstructed"]["size"]):
            raise ValueError(f"inspector output checksum mismatch: {module}")
        files = {f"{module}.patched.bin": (image, manifest["reconstructed"])}
        module_metadata = {name: metadata[name] for name in
                           (f"{module}.fixups.tsv", f"{module}.gates.tsv")}
        files.update({name: (root / identity["snapshot_path"], identity)
                      for name, identity in module_metadata.items()})
        runtime_files = {}
        for name, (source, identity) in files.items():
            destination = runtime / name
            shutil.copyfile(source, destination)
            digest = sha256(destination)
            size = destination.stat().st_size
            if digest != identity["sha256"] or size != identity["size"]:
                raise ValueError(f"runtime module copy differs from its inspected source: {name}")
            runtime_files[name] = {"path": str(destination.relative_to(root)),
                                   "size": size, "sha256": digest}
        result[module] = {**manifest, "metadata_inputs": module_metadata,
                          "runtime_files": runtime_files}
    return result


def embedded_shaders(source: Path, root: Path, main_sha256: str) -> list[dict]:
    image = root / "runtime-modules/main.patched.bin"
    if sha256(image) != main_sha256:
        raise ValueError("main image changed before embedded shader extraction")
    data = image.read_bytes()
    output = root / "embedded-main-shaders"
    output.mkdir()
    result = []
    for line in (source / "dm/gfx/nvn/shader_map.tsv").read_text().splitlines():
        fields = line.split("\t")
        if len(fields) < 5 or not fields[0].startswith("main.rodata@"):
            continue
        offset = int(fields[0].split("@")[1], 16)
        if offset < 0x1000 or offset + 0x880 > len(data):
            raise ValueError("embedded shader control offset is out of bounds")
        start = data.rfind(bytes.fromhex("78563412"), offset - 0x1000, offset)
        control = data[offset:offset + 0x880]
        if start < 0 or not control.startswith(bytes.fromhex("34127698")):
            raise ValueError("embedded shader markers do not match the pinned map")
        if hashlib.sha256(control).hexdigest()[:16] != fields[3]:
            raise ValueError("embedded shader control key mismatch")
        record = bytes(0x38) + data[start:offset + 0x880]
        path = output / f"{fields[3]}.record"
        path.write_bytes(record)
        result.append({"key": fields[3], "control_offset": offset,
                       "record_size": len(record), "record_sha256": sha256(path)})
    if not result:
        raise ValueError("pinned map contains no qualified embedded main shaders")
    write_json(root / "embedded-shader-qualification.json", result)
    return result


def qualify_shaders(root: Path, chain: str) -> dict:
    directory = root / "shader-cache" / f"b-{chain}"
    entries = json.loads((directory / "_b_build.json").read_text())
    if not entries or any(row["st"] != "ok" for row in entries):
        raise ValueError("one or more actual upstream shader translations failed")
    hashes, counts = [], {"reflection": 0, "msl": 0, "source_cache": 0}
    for path in sorted(directory.iterdir()):
        if not path.is_file():
            continue
        if path.name.endswith("_b.json"):
            reflection = json.loads(path.read_text())
            if reflection["chain"] != chain or reflection["drv_unfillable"]:
                raise ValueError(f"unqualified shader reflection: {path.name}")
            if any(binding["kind"] == "?" for binding in reflection["bindings"]):
                raise ValueError(f"unknown resource type: {path.name}")
            slot_kinds = {"cbuf", "samp", "samp_tex", "tex", "img", "tbuf", "sbuf", "ubo_ptr"}
            if any(binding["kind"] in slot_kinds and binding.get("reg") is None
                   for binding in reflection["bindings"]):
                raise ValueError(f"unresolved resource binding: {path.name}")
            counts["reflection"] += 1
        elif path.suffix == ".metal":
            path.read_text(encoding="utf-8")
            counts["msl"] += 1
        elif path.suffix == ".metallib":
            data = path.read_bytes()
            if data.startswith(b"MTLB"):
                raise ValueError("nometal output unexpectedly contains a precompiled library")
            data.decode("utf-8")
            counts["source_cache"] += 1
        hashes.append({"path": str(path.relative_to(root)), "size": path.stat().st_size,
                       "sha256": sha256(path)})
    if counts["reflection"] != len(entries) or counts["msl"] != len(entries):
        raise ValueError("shader translation output set is incomplete")
    result = {"chain": chain, "keys": len(entries), "counts": counts,
              "kind": "translated MSL source; Metal compilation and execution not performed",
              "artifacts": hashes}
    write_json(root / "shader-qualification.json", result)
    return result


def prepare(options: Options) -> dict:
    validate_inputs(options)
    with new_output(options.out, options.keep_failed) as root:
        env = environment(root, options)
        runner = Runner(root, env, options.timeout_minutes * 60)
        guest = options.app / "Frameworks/Guest.framework/Guest"
        inputs = {"guest": sha256(guest), "config": sha256(options.app / "romfs_overlay/data/.build.config"),
                  "tools": {name: {"path": str(path), "sha256": sha256(path)} for name, path in
                            (("dotnet", options.dotnet), ("spirv_cross", options.spirv_cross),
                             ("inspector", options.inspector))}}
        runner.run("source-archive", ["git", "-C", str(options.source), "archive",
                   "--format=tar", "-o", str(root / "upstream.tar"), UPSTREAM_REVISION, "dm"], root)
        archive_hash = sha256(root / "upstream.tar")
        source = root / "source"
        extract_source(root / "upstream.tar", source)
        (root / "upstream.tar").unlink()
        if (source / "dm/PRODUCT").read_text().strip() != PRODUCT:
            raise ValueError("source product identity differs from the supported baseline")
        modules = inspector_modules(options, root, runner)
        index, reader, index_proof = recover_index(options.app, options.resources, root, options.limits)
        resources = prepare_resources(index, reader, root)
        resource_identities = {}
        for name, value in resources["references"].items():
            path = Path(value)
            before = path.stat()
            digest = sha256(path)
            after = path.stat()
            if (before.st_size, before.st_mtime_ns, before.st_ino) != (
                    after.st_size, after.st_mtime_ns, after.st_ino):
                raise ValueError(f"resource changed while fingerprinting: {name}")
            resource_identities[name] = {"path": value, "size": after.st_size,
                                         "mtime_ns": after.st_mtime_ns,
                                         "inode": after.st_ino, "sha256": digest}
        main_hash = modules["main"]["reconstructed"]["sha256"]
        embedded = embedded_shaders(source, root, main_hash)
        worker = [sys.executable, "-u", "-m", "d2r_mac_fixture.worker"]
        for action in ("vault", "hud", "bright"):
            runner.run(f"generate-{action}", [*worker, action, "--source", str(source),
                       "--root", str(root)], root)
        runner.run("dotnet-version", [str(options.dotnet), "--version"], root)
        version = (root / "logs/dotnet-version.log").read_text().strip()
        if not re.fullmatch(r"10\.\d+\.\d+", version):
            raise ValueError("the provided dotnet executable must select a stable .NET 10 SDK")
        (root / "NuGet.Config").write_text(
            '<?xml version="1.0" encoding="utf-8"?>\n<configuration><packageSources>'
            '<clear/><add key="nuget.org" value="https://api.nuget.org/v3/index.json"/>'
            '</packageSources></configuration>\n', encoding="utf-8")
        runner.run("translator-build", [str(options.dotnet), "publish",
                   str(source / "dm/gfx/tools/dmsh_xlate/dmsh_xlate.csproj"), "-c", "Release",
                   "-r", "osx-arm64", "--self-contained", "false", "-o",
                   str(root / "build/gfx/dmsh_xlate"), "-v", "minimal",
                   "-p:UseSharedCompilation=false", "-nr:false",
                   f"-p:RestoreConfigFile={root / 'NuGet.Config'}"], source)
        for action in ("lock", "header"):
            runner.run(f"shader-{action}", [*worker, action, "--source", str(source),
                       "--root", str(root)], root)
        header = (source / "dm/gfx/nvn/dmsh_chain.h").read_text()
        match = re.search(r'#define DMSH_CHAIN "([0-9a-f]{12})"', header)
        if match is None:
            raise ValueError("upstream did not generate a valid actual shader chain")
        chain = match[1]
        runner.run("shader-translate", [*worker, "translate", "--source", str(source),
                   "--root", str(root), "--main-sha256", main_hash], root)
        shader_proof = qualify_shaders(root, chain)
        metadata = root / "metadata"
        metadata.mkdir()
        for name in ("shader_map.tsv", "pso_manifest.tsv"):
            shutil.copyfile(source / "dm/gfx/nvn" / name, metadata / name)
        runner.run("c1-metadata", [sys.executable, "-u", str(source / "dm/gfx/tools/c1_guard.py"),
                   str(root / "shader-cache" / f"b-{chain}"), str(metadata / "c1_full.tsv")], root)
        if not (metadata / "c1_full.tsv").read_text().startswith(f"# chain b-{chain} "):
            raise ValueError("genuine C1 metadata does not identify the current translation")
        runner.run("clang-location", ["xcrun", "-sdk", "macosx", "--find", "clang"], root)
        runner.run("sdk-location", ["xcrun", "-sdk", "macosx", "--show-sdk-path"], root)
        clang = Path((root / "logs/clang-location.log").read_text().strip())
        sdk = Path((root / "logs/sdk-location.log").read_text().strip())
        if not clang.is_file() or not sdk.is_dir():
            raise ValueError("selected Xcode C compiler or macOS SDK is unavailable")
        env["SDKROOT"] = str(sdk)
        env["PATH"] = str(clang.parent) + os.pathsep + env["PATH"]
        runner.run("host-build", ["make", "-C", str(source / "dm/host"), "dmhost",
                   "CFLAGS=-O1 -g -Wall -Wno-unused-function -mmacosx-version-min=26.0 -DDM_SHELL=1"], root)
        binary = source / "dm/host/dmhost"
        if chain.encode() not in binary.read_bytes():
            raise ValueError("built host does not contain the generated shader chain")
        (root / "bin").mkdir()
        shutil.copyfile(binary, root / "bin/dmhost-source-msl")
        (root / "bin/dmhost-source-msl").chmod(0o700)
        if sha256(guest) != inputs["guest"]:
            raise ValueError("app Guest changed during preparation")
        for name, identity in resource_identities.items():
            path = Path(identity["path"])
            current = path.stat()
            if (current.st_size != identity["size"]
                    or current.st_mtime_ns != identity["mtime_ns"]
                    or current.st_ino != identity["inode"]
                    or sha256(path) != identity["sha256"]):
                raise ValueError(f"referenced resource changed during preparation: {name}")
        verify_module_metadata(modules, root)
        metadata_hashes = {path.name: sha256(path) for path in metadata.iterdir()}
        header_hashes = {name: sha256(source / "dm/host" / name) for name in
                         ("dm_vault_tab.h", "dm_hud_exp.h", "dm_bright_levels.inc")}
        toolkit = {path.name: sha256(path) for path in Path(__file__).parent.glob("*.py")}
        manifest = {
            "schema": "d2r.private-mac-fixture.v1", "state": "prepared_unexecuted",
            "preparation_toolkit_sha256": toolkit,
            "upstream": {"repository": UPSTREAM_URL, "revision": UPSTREAM_REVISION,
                         "archive_sha256": archive_hash, "working_tree_used": False},
            "input_paths": {"app": str(options.app), "resources": str(options.resources),
                            "source_repository": str(options.source)},
            "inputs": inputs, "module_inspection": modules,
            "resource_index": index_proof, "resources": resources,
            "resource_identities": resource_identities,
            "embedded_shaders": embedded, "shader_chain": chain,
            "shader_keys": shader_proof["keys"], "metadata_sha256": metadata_hashes,
            "generated_header_sha256": header_hashes,
            "static_main_reader": "Explicit inspected original-layout input adapter in worker.py; no original NSO claimed.",
            "host": {"path": "bin/dmhost-source-msl", "sha256": sha256(root / "bin/dmhost-source-msl"),
                     "compile_flag": "DM_SHELL=1", "minimum_macos": "26.0"},
            "private_source_config_changes": ["dm/gfx/tools/dmsh_chain.lock", "dm/gfx/nvn/dmsh_chain.h"],
            "private_source_config_sha256": {name: sha256(source / name) for name in
                                            ("dm/gfx/tools/dmsh_chain.lock", "dm/gfx/nvn/dmsh_chain.h")},
            "stages": runner.receipts,
            "limitations": [
                "The guest was never executed; no boot, Metal shader compilation, networking or G0 pass is claimed.",
                "The shader cache contains translated MSL source using the upstream .metallib filename convention.",
                "Original extraction FileIds/Flags and absent voice assets are not recovered or fabricated.",
                "Resource symlinks require the original app/resources paths to remain available and unchanged.",
                "The Apple code signature is not authenticated by the static inspector.",
                "This Apple runtime candidate does not establish Linux or multiplayer compatibility.",
            ],
        }
        write_json(root / "manifest.json", manifest)
    return manifest
