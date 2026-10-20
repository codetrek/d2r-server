import hashlib
from pathlib import Path
import struct
import tempfile
import unittest
import zlib

from d2r_mac_fixture.tvfs import ArchiveReader, Limits, Span, decode, parse_index


def ekey(encoded: bytes, offset: int = 0, header_size: int | None = None) -> bytes:
    hashed = encoded if header_size is None else encoded[:header_size]
    return hashlib.md5(hashed).digest()[:9] + b"\0\x01" + offset.to_bytes(5, "big")


def index_image(name: bytes = b"data/file", spec: int = 2, file_size: int = 3,
                content_size: int = 7, file_offset: int = 0) -> bytes:
    tree = bytes([len(name)]) + name + b"\xff" + struct.pack(">I", file_offset)
    return index_with_tree(tree, spec, file_size, content_size)


def index_with_tree(tree: bytes, spec: int = 2, file_size: int = 3,
                    content_size: int = 7) -> bytes:
    specs = b"z\0n\0b:64K*=n\0"
    container = bytes(16) + struct.pack(">I", 7) + bytes([spec]) + struct.pack(">I", content_size)
    files = b"\x01" + struct.pack(">II", 0, file_size) + b"\0"
    paths_at = 46
    specs_at = paths_at + len(tree)
    containers_at = specs_at + len(specs)
    files_at = containers_at + len(container)
    header = (b"TVFS\x01\x2e\x10\x10\0\0\0\x0a"
              + struct.pack(">IIIIII", paths_at, len(tree), files_at, len(files),
                            containers_at, len(container))
              + struct.pack(">HII", 1, specs_at, len(specs)))
    return header + tree + specs + container + files


def blte(pieces: list[tuple[bytes, bytes]]) -> tuple[bytes, bytes]:
    blocks = [(mode + (content if mode == b"N" else zlib.compress(content)), content)
              for mode, content in pieces]
    header_size = 12 + 24 * len(blocks)
    header = b"BLTE" + struct.pack(">I", header_size) + b"\x0f" + len(blocks).to_bytes(3, "big")
    for encoded, content in blocks:
        header += struct.pack(">II", len(encoded), len(content)) + hashlib.md5(encoded).digest()
    raw = header + b"".join(encoded for encoded, _ in blocks)
    return raw, ekey(raw, header_size=header_size)


class TVFSTest(unittest.TestCase):
    def test_encoding_offset_and_distinct_sizes(self):
        parsed = parse_index(index_image())
        self.assertEqual(len(parsed.files), 1)
        entry = parsed.files[0]
        self.assertEqual(entry.path, "data/file")
        self.assertEqual(entry.spans[0].encoding, "n")
        self.assertEqual(entry.spans[0].encoding_spec_offset, 2)
        self.assertEqual(entry.spans[0].file_table_size, 3)
        self.assertEqual(entry.spans[0].content_size, 7)
        self.assertEqual(entry.json()["spans"][0]["file_table_size"], 3)

    def test_real_prefix_tree_directory(self):
        child = b"\x04file\xff" + bytes(4)
        tree = b"\x04data\0\xff" + struct.pack(">I", 0x80000000 | (4 + len(child))) + child
        self.assertEqual(parse_index(index_with_tree(tree)).files[0].path, "data/file")

    def test_implicit_separator_is_part_of_the_path(self):
        tree = b"\x04data\x04file\xff" + bytes(4)
        self.assertEqual(parse_index(index_with_tree(tree)).files[0].path, "data/file")

    def test_rejects_malformed_tables_tree_and_paths(self):
        valid = index_image()
        for truncated in (valid[:5], valid[:-1], valid[:46]):
            with self.subTest(size=len(truncated)), self.assertRaises(ValueError):
                parse_index(truncated)
        for name in (b"../escape", b"/absolute", b"data//file", b"data/f|row", b"data/f\nrow"):
            with self.subTest(name=name), self.assertRaises(ValueError):
                parse_index(index_image(name))
        for spec in (1, 3, 13, 255):
            with self.subTest(spec=spec), self.assertRaises(ValueError):
                parse_index(index_image(spec=spec))
        with self.assertRaises(ValueError):
            parse_index(index_image(file_offset=10))
        with self.assertRaises(ValueError):
            parse_index(index_image(), Limits(index_bytes=8))
        child = b"\x01x\xff" + bytes(4)
        tree = b"\x01d\0\xff" + struct.pack(">I", 0x800000ff) + child
        with self.assertRaises(ValueError):
            parse_index(index_with_tree(tree))

    def test_raw_and_zlib_storage_integrity(self):
        content = b"actual synthetic content"
        for encoding, raw in (("n", content), ("z", zlib.compress(content))):
            with self.subTest(encoding=encoding):
                self.assertEqual(decode(raw, ekey(raw), encoding, len(content)), content)
                with self.assertRaises(ValueError):
                    decode(raw, bytes(16), encoding, len(content))
                with self.assertRaises(ValueError):
                    decode(raw, ekey(raw), encoding, len(content) - 1)
        raw = zlib.compress(content) + b"extra"
        with self.assertRaises(ValueError):
            decode(raw, ekey(raw), "z", len(content))
        with self.assertRaises(ValueError):
            decode(zlib.compress(bytes(10000)), ekey(zlib.compress(bytes(10000))), "z", 1)

    def test_blte_blocks_checksums_and_exact_boundaries(self):
        raw, key = blte([(b"N", b"abc"), (b"Z", b"defgh")])
        self.assertEqual(decode(raw, key, "b:64K*=n", 8), b"abcdefgh")
        for bad in (raw[:-1], raw + b"extra", raw[:-1] + bytes([raw[-1] ^ 1])):
            with self.subTest(size=len(bad)), self.assertRaises(ValueError):
                decode(bad, key, "b:64K*=n", 8)
        with self.assertRaises(ValueError):
            decode(raw, key, "b:64K*=n", 7)
        with self.assertRaises(ValueError):
            decode(raw, key, "b:64K*=n", 8, Limits(file_bytes=4))

    def test_archive_layer_and_range_boundaries(self):
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            first, second = base / "first", base / "second"
            first.mkdir()
            second.mkdir()
            data = b"content"
            (second / "00-00000001.data").write_bytes(data)
            span = Span(0, len(data), 0, ekey(data).hex(), len(data), len(data), 0, "n")
            reader = ArchiveReader((first, second))
            self.assertEqual(reader.read(span), data)
            (first / "00-00000001.data").write_bytes(b"short")
            with self.assertRaises(ValueError):
                reader.read(span)
            (first / "00-00000001.data").unlink()
            (second / "00-00000001.data").unlink()
            with self.assertRaises(FileNotFoundError):
                reader.read(span)


if __name__ == "__main__":
    unittest.main()
