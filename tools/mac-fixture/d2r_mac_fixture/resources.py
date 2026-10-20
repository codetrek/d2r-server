from collections import Counter
import hashlib
import json
from pathlib import Path
import shutil

from .process import write_json
from .tvfs import ArchiveReader, File, Index, Limits, Span, parse_index


def read_config(path: Path) -> dict[str, str]:
    values = {}
    for line in path.read_text(encoding="utf-8").splitlines():
        if not line or line.startswith("#"):
            continue
        if " = " not in line:
            raise ValueError(f"invalid static-CASC configuration line: {line!r}")
        key, value = line.split(" = ", 1)
        if key in values:
            raise ValueError(f"duplicate static-CASC key: {key}")
        values[key] = value
    if values.get("key-layout-0") != "8 8 40 1":
        raise ValueError("unsupported static-CASC key layout")
    return values


def config_span(config: dict[str, str], key: str) -> tuple[str, Span]:
    ckey, ekey = config[key].split()
    decoded, encoded = map(int, config[key + "-size"].split())
    if len(bytes.fromhex(ckey)) != 16 or len(bytes.fromhex(ekey)) != 16:
        raise ValueError(f"invalid configuration keys: {key}")
    return ckey, Span(0, decoded, 0, ekey, encoded, decoded, 0,
                      config[key + "-espec"])


def recover_index(app: Path, resources: Path, root: Path,
                  limits: Limits) -> tuple[Index, ArchiveReader, dict]:
    overlay = app / "romfs_overlay" / "data"
    data = resources / "data"
    reader = ArchiveReader((overlay, data), limits)
    config = read_config(overlay / ".build.config")
    recovered = {}
    for key in ("vfs-root", "vfs-1"):
        ckey, span = config_span(config, key)
        if (span.content_size > limits.index_bytes
                or span.encoded_size > limits.index_bytes):
            raise ValueError("configured TVFS index limit exceeded before decoding")
        content = reader.read(span)
        if len(content) > limits.index_bytes:
            raise ValueError("configured TVFS index limit exceeded")
        if hashlib.md5(content).hexdigest() != ckey:
            raise ValueError(f"configuration content checksum mismatch: {key}")
        (root / f"{key}.tvfs").write_bytes(content)
        index = parse_index(content, limits)
        recovered[key] = {"decoded_sha256": index.sha256,
                          "content_md5": ckey, "files": len(index.files),
                          "ekey": span.ekey}
        if key == "vfs-1":
            full_index = index
    with (root / "recovered-index.jsonl").open("w", encoding="utf-8") as stream:
        for entry in full_index.files:
            stream.write(json.dumps(entry.json(), ensure_ascii=False) + "\n")
    return full_index, reader, recovered


def generator_input(path: str) -> bool:
    key = path.casefold()
    return ((key.startswith("data/global/excel/") and key.endswith(".txt"))
            or key in {f"data/hd/items/{name}.json" for name in ("uniques", "sets", "items")}
            or (key.startswith(("data/hd/env/biome/", "data/hd/env/vis/",
                                "data/hd/env/preset/")) and key.endswith(".json"))
            or (key.startswith("data/hd/global/ui/items/") and key.endswith(".sprite")))


def shader_input(path: str) -> bool:
    return path.casefold().startswith("data/hd/shaders/") and path.casefold().endswith(".bin")


def require_single_span(entry: File) -> Span:
    if len(entry.spans) != 1 or entry.spans[0].logical_offset:
        raise ValueError(f"generator adapter requires one full-content span: {entry.path}")
    return entry.spans[0]


def prepare_resources(index: Index, reader: ArchiveReader, root: Path) -> dict:
    """Expose only honestly recovered rows to the unchanged upstream generators."""
    fixture = root / "generator-romfs" / "data"
    fixture.mkdir(parents=True)
    source_files = {}
    for directory in reversed(reader.directories):
        for path in directory.iterdir():
            if path.name == ".build.config" or path.name.endswith(".data"):
                if path.is_file():
                    source_files[path.name] = path.resolve()
    for name, target in source_files.items():
        (fixture / name).symlink_to(target)
    (root / "shader-packs").mkdir()
    rows = ["FileId|KeyLayout|Chunk|Archive|CKey|CSize|EKey|ESize|Encoding|Path|Flags"]
    proof, missing, counts = [], Counter(), Counter()
    names = set()
    for entry in index.files:
        for span in entry.spans:
            try:
                reader.location(span)
            except FileNotFoundError:
                ekey = bytes.fromhex(span.ekey)
                missing[f"{ekey[9]:02d}-{ekey[10]:08d}.data"] += 1
        if not (generator_input(entry.path) or shader_input(entry.path)):
            continue
        span = require_single_span(entry)
        content = reader.read(span)
        ekey = bytes.fromhex(span.ekey)
        ckey = hashlib.md5(content).hexdigest()
        proof.append({"path": entry.path, "content_md5": ckey,
                      "content_sha256": hashlib.sha256(content).hexdigest(),
                      "content_size": len(content), "ekey": span.ekey,
                      "file_table_size": span.file_table_size})
        if generator_input(entry.path):
            rows.append("|".join((f"recovered-ft-{entry.file_table_offset:x}", "0",
                                  str(ekey[9]), str(ekey[10]), ckey,
                                  str(span.content_size), span.ekey,
                                  str(span.encoded_size), span.encoding,
                                  "data:" + entry.path, "recovered-tvfs-0a")))
            counts["generator_files"] += 1
        if shader_input(entry.path):
            name = Path(entry.path).name
            if name in names:
                raise ValueError(f"duplicate shader pack basename: {name}")
            names.add(name)
            (root / "shader-packs" / name).write_bytes(content)
            counts["shader_packs"] += 1
    if not counts["generator_files"] or not counts["shader_packs"]:
        raise ValueError("required generator or shader input set is empty")
    (fixture / "build_manifest.psv").write_text("\n".join(rows) + "\n", encoding="utf-8")
    write_json(root / "resource-input-proof.json", proof)
    result = {"counts": dict(counts), "unavailable_archive_references": dict(missing),
              "generator_manifest": {
                  "path": "generator-romfs/data/build_manifest.psv",
                  "kind": "explicit reconstructed subset adapter for unchanged generators",
                  "original_extraction_manifest": False,
                  "file_ids": "derived file-table offsets, not original FileIds",
                  "content_checksums": "computed from verified decoded content",
                  "flags": "derivative marker, not recovered original Flags"},
              "references": {name: str(path) for name, path in sorted(source_files.items())}}
    write_json(root / "resource-qualification.json", result)
    return result
