"""Read the pinned Switch static-CASC profile without an extraction manifest.

The profile is documented and used by the pinned upstream overlay writer:
https://github.com/jhaizhou-ops/d2r-mobile/blob/120fe55c02a5633476998a44f8c3c3c809fa48b0/dm/tools/casc_overlay.py#L254
"""

from dataclasses import asdict, dataclass
import hashlib
from pathlib import Path, PurePosixPath
import struct
import zlib


@dataclass(frozen=True)
class Limits:
    index_bytes: int = 64 * 1024 * 1024
    file_bytes: int = 1024 * 1024 * 1024
    tree_depth: int = 256


def checked_slice(data: bytes, offset: int, size: int) -> bytes:
    if offset < 0 or size < 0 or offset > len(data) or size > len(data) - offset:
        raise ValueError(f"range {offset}+{size} exceeds {len(data)} bytes")
    return data[offset:offset + size]


def u32(data: bytes, offset: int) -> int:
    return int.from_bytes(checked_slice(data, offset, 4), "big")


def logical_path(value: bytes) -> str:
    path = value.decode("utf-8")
    if (not path or path.startswith("/") or "\\" in path or "|" in path
            or any(ord(character) < 32 or ord(character) == 127 for character in path)
            or any(part in {"", ".", ".."} for part in path.split("/"))):
        raise ValueError(f"unsafe TVFS logical path: {path!r}")
    return str(PurePosixPath(path))


@dataclass(frozen=True)
class Span:
    logical_offset: int
    file_table_size: int
    container_offset: int
    ekey: str
    encoded_size: int
    content_size: int
    encoding_spec_offset: int
    encoding: str


@dataclass(frozen=True)
class File:
    path: str
    file_table_offset: int
    spans: tuple[Span, ...]

    def json(self) -> dict:
        return asdict(self)


@dataclass(frozen=True)
class Index:
    files: tuple[File, ...]
    sha256: str


def parse_index(data: bytes, limits: Limits = Limits()) -> Index:
    if len(data) > limits.index_bytes:
        raise ValueError("TVFS index exceeds configured preparation limit")
    if checked_slice(data, 0, 12) != b"TVFS\x01\x2e\x10\x10\0\0\0\x0a":
        raise ValueError("unsupported TVFS header; expected version-1 flags-0x0a profile")
    path_at, path_size, files_at, files_size, cft_at, cft_size = struct.unpack(
        ">IIIIII", checked_slice(data, 12, 24))
    specs_at, specs_size = struct.unpack(">II", checked_slice(data, 38, 8))
    if not (path_at == 46 and path_at + path_size == specs_at
            and specs_at + specs_size == cft_at and cft_at + cft_size == files_at
            and files_at + files_size == len(data)):
        raise ValueError("TVFS tables are not contiguous within the index")
    specs = checked_slice(data, specs_at, specs_size)
    if not specs or specs[-1] != 0 or cft_size % 25:
        raise ValueError("invalid encoding string table or container table size")
    width = max(1, (cft_size.bit_length() + 7) // 8)
    if width > 4:
        raise ValueError("unsupported container offset width")
    result: list[File] = []
    names: set[str] = set()

    def file_record(path: bytes, offset: int) -> None:
        name = logical_path(path)
        if name.casefold() in names:
            raise ValueError(f"duplicate case-insensitive TVFS path: {name}")
        names.add(name.casefold())
        at = files_at + offset
        count = checked_slice(data, at, 1)[0]
        if not count or offset + 1 + count * (8 + width) > files_size:
            raise ValueError(f"file span table exceeds bounds: {name}")
        at += 1
        spans = []
        for _ in range(count):
            logical_offset, file_size = struct.unpack(">II", checked_slice(data, at, 8))
            container_offset = int.from_bytes(checked_slice(data, at + 8, width), "big")
            if container_offset % 25 or container_offset + 25 > cft_size:
                raise ValueError(f"container entry is unaligned or out of bounds: {name}")
            entry = checked_slice(data, cft_at + container_offset, 25)
            spec_offset = entry[20]
            if spec_offset >= len(specs) or (spec_offset and specs[spec_offset - 1] != 0):
                raise ValueError(f"encoding offset is not a string boundary: {name}")
            encoding = specs[spec_offset:].split(b"\0", 1)[0].decode("ascii")
            if encoding not in {"z", "n", "b:64K*=n", "b:256K*=z"}:
                raise ValueError(f"unsupported encoding {encoding!r}: {name}")
            spans.append(Span(logical_offset, file_size, container_offset,
                              entry[:16].hex(), u32(entry, 16), u32(entry, 21),
                              spec_offset, encoding))
            at += 8 + width
        result.append(File(name, offset, tuple(spans)))

    def walk(start: int, end: int, base: bytes, depth: int) -> None:
        if depth > limits.tree_depth:
            raise ValueError("TVFS tree exceeds configured depth limit")
        position, prefix = start, base
        while position < end:
            before = position
            if checked_slice(data, position, 1) == b"\0":
                prefix += b"/"
                position += 1
            marker = checked_slice(data, position, 1)[0]
            if marker != 0xff:
                if marker == 0:
                    raise ValueError("empty TVFS path fragment")
                position += 1
                prefix += checked_slice(data, position, marker)
                position += marker
            if position >= end:
                raise ValueError("unterminated TVFS path entry")
            separator = data[position] == 0
            if separator:
                prefix += b"/"
                position += 1
            if position >= end:
                raise ValueError("missing TVFS path value")
            if data[position] != 0xff:
                if not separator:
                    prefix += b"/"
                if position <= before:
                    raise ValueError("TVFS path traversal made no progress")
                continue
            value = u32(data, position + 1)
            position += 5
            if position > end:
                raise ValueError("TVFS path value exceeds parent")
            if value & 0x80000000:
                size = value & 0x7fffffff
                child_end = position + size - 4
                if size < 4 or child_end > end:
                    raise ValueError("TVFS directory exceeds parent")
                walk(position, child_end, prefix, depth + 1)
                position = child_end
            else:
                file_record(prefix, value)
            prefix = base
        if position != end:
            raise ValueError("TVFS tree did not end at its declared boundary")

    walk(path_at, path_at + path_size, b"", 0)
    return Index(tuple(result), hashlib.sha256(data).hexdigest())


def inflate_exact(raw: bytes, expected: int) -> bytes:
    decoder = zlib.decompressobj()
    data = decoder.decompress(raw, expected + 1)
    if (len(data) != expected or not decoder.eof or decoder.unused_data
            or decoder.unconsumed_tail):
        raise ValueError("zlib size, trailing data or end-of-stream mismatch")
    return data


def decode(raw: bytes, ekey: bytes, encoding: str, expected: int,
           limits: Limits = Limits()) -> bytes:
    if expected < 0 or expected > limits.file_bytes or len(raw) > limits.file_bytes:
        raise ValueError("resource exceeds configured preparation limit")
    if len(ekey) != 16:
        raise ValueError("EKey must contain exactly 16 bytes")
    if raw.startswith(b"BLTE"):
        header_size = u32(raw, 4)
        flag = checked_slice(raw, 8, 1)
        count = int.from_bytes(checked_slice(raw, 9, 3), "big")
        if flag != b"\x0f" or not count or header_size != 12 + 24 * count:
            raise ValueError("unsupported BLTE header")
        header = checked_slice(raw, 0, header_size)
        if hashlib.md5(header).digest()[:9] != ekey[:9]:
            raise ValueError("BLTE EKey storage-header checksum mismatch")
        position, content, decoded = header_size, [], 0
        for number in range(count):
            at = 12 + 24 * number
            encoded_size, content_size = struct.unpack(">II", checked_slice(raw, at, 8))
            if content_size > expected - decoded:
                raise ValueError("BLTE decoded blocks exceed declared content size")
            block = checked_slice(raw, position, encoded_size)
            if hashlib.md5(block).digest() != checked_slice(raw, at + 8, 16):
                raise ValueError("BLTE block checksum mismatch")
            mode = checked_slice(block, 0, 1)
            if mode == b"N":
                piece = block[1:]
                if len(piece) != content_size:
                    raise ValueError("BLTE raw block size mismatch")
            elif mode == b"Z":
                piece = inflate_exact(block[1:], content_size)
            else:
                raise ValueError(f"unsupported BLTE block mode: {mode!r}")
            content.append(piece)
            decoded += len(piece)
            position += encoded_size
        if position != len(raw) or decoded != expected:
            raise ValueError("BLTE trailing data or decoded size mismatch")
        return b"".join(content)
    if hashlib.md5(raw).digest()[:9] != ekey[:9]:
        raise ValueError("EKey storage checksum mismatch")
    if encoding == "n":
        if len(raw) != expected:
            raise ValueError("raw resource size mismatch")
        return raw
    if encoding == "z":
        return inflate_exact(raw, expected)
    raise ValueError("BLTE encoding requires a BLTE container")


class ArchiveReader:
    def __init__(self, directories: tuple[Path, ...], limits: Limits = Limits()):
        self.directories, self.limits = directories, limits

    def location(self, span: Span) -> tuple[Path, int]:
        ekey = bytes.fromhex(span.ekey)
        name = f"{ekey[9]:02d}-{ekey[10]:08d}.data"
        for directory in self.directories:
            path = directory / name
            if path.is_file():
                offset = int.from_bytes(ekey[11:], "big")
                if span.encoded_size > self.limits.file_bytes:
                    raise ValueError("encoded resource exceeds configured preparation limit")
                size = path.stat().st_size
                if offset > size or span.encoded_size > size - offset:
                    raise ValueError(f"archive range exceeds {path}")
                return path, offset
        raise FileNotFoundError(f"resource archive not available: {name}")

    def read(self, span: Span) -> bytes:
        path, offset = self.location(span)
        with path.open("rb") as stream:
            stream.seek(offset)
            raw = stream.read(span.encoded_size)
        if len(raw) != span.encoded_size:
            raise ValueError(f"resource changed during read: {path}")
        return decode(raw, bytes.fromhex(span.ekey), span.encoding,
                      span.content_size, self.limits)
