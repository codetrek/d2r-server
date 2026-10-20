import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch

from d2r_mac_fixture.prepare import (Options, environment, extract_source, inspector_modules,
                                     verify_module_metadata)
from d2r_mac_fixture.process import Runner, check_scopes, new_output
from d2r_mac_fixture.resources import prepare_resources
from d2r_mac_fixture.tvfs import ArchiveReader, File, Index, Span

from test_tvfs import ekey


class ScopeTest(unittest.TestCase):
    def test_no_replace_or_input_scope_overlap(self):
        with tempfile.TemporaryDirectory() as directory:
            parent = Path(directory)
            output = parent / "fixture"
            with new_output(output) as staging:
                (staging / "complete").write_text("yes")
            self.assertEqual((output / "complete").read_text(), "yes")
            with self.assertRaises(FileExistsError):
                with new_output(output):
                    self.fail("an existing output must not be opened")
            collision = parent / "collision"
            with self.assertRaises(OSError):
                with new_output(collision) as staging:
                    (staging / "owned").write_text("owned")
                    collision.mkdir()
                    (collision / "other").write_text("preserve")
            self.assertEqual((collision / "other").read_text(), "preserve")
            self.assertFalse((collision / "owned").exists())
            with self.assertRaises(ValueError):
                check_scopes(parent / "input/out", (parent / "input",))
            with self.assertRaises(ValueError):
                check_scopes(parent / "input", (parent / "input/source",))

    def test_failed_preparation_removes_only_owned_staging(self):
        with tempfile.TemporaryDirectory() as directory:
            parent = Path(directory)
            with self.assertRaisesRegex(ValueError, "controlled"):
                with new_output(parent / "out") as staging:
                    (staging / "partial").write_text("owned")
                    raise ValueError("controlled")
            self.assertEqual(list(parent.iterdir()), [])

    def test_archive_traversal_and_external_symlinks_rejected(self):
        for name, link in (("../escape", None), ("dm/link", "/outside")):
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                archive = root / "source.tar"
                with tarfile.open(archive, "w") as tar:
                    member = tarfile.TarInfo(name)
                    if link is None:
                        member.size = 1
                        tar.addfile(member, io.BytesIO(b"x"))
                    else:
                        member.type = tarfile.SYMTYPE
                        member.linkname = link
                        tar.addfile(member)
                with self.assertRaises(ValueError):
                    extract_source(archive, root / "source")

    def test_dotnet_first_invocation_and_caches_are_scoped(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            executable = Path(sys.executable)
            options = Options(root / "app", root / "resources", root / "source",
                              executable, executable, executable, root / "out")
            with patch.dict(os.environ, {"PRIVATE_TOKEN": "never-record",
                                         "DM_CLOUD_REAL": "1"}):
                env = environment(root, options)
            self.assertNotIn("PRIVATE_TOKEN", env)
            self.assertNotIn("DM_CLOUD_REAL", env)
            self.assertEqual(env.get("HOME"), os.environ.get("HOME"))
            self.assertEqual(env["DOTNET_GENERATE_ASPNET_CERTIFICATE"], "false")
            for key in ("DOTNET_CLI_HOME", "NUGET_PACKAGES", "NUGET_HTTP_CACHE_PATH",
                        "NUGET_PLUGINS_CACHE_PATH", "TMPDIR", "CLANG_MODULE_CACHE_PATH"):
                self.assertTrue(Path(env[key]).is_relative_to(root), key)

    def test_streaming_failure_and_timeout_receipts(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            runner = Runner(root, dict(os.environ), 0.2)
            with self.assertRaises(subprocess.TimeoutExpired):
                runner.run("bounded", [sys.executable, "-u", "-c",
                                       "import time; print('started', flush=True); time.sleep(60)"], root)
            self.assertIn("started", (root / "logs/bounded.log").read_text())
            receipt = json.loads((root / "stage-receipts.json").read_text())[0]
            self.assertTrue(receipt["interrupted_or_timed_out"])
            self.assertIsNotNone(receipt["exit_code"])
            runner.timeout = 5
            with self.assertRaises(subprocess.CalledProcessError):
                runner.run("failed", [sys.executable, "-c", "raise SystemExit(7)"], root)
            self.assertEqual(runner.receipts[-1]["exit_code"], 7)

    def test_derivative_manifest_has_real_hashes_and_labeled_ids(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            archives = root / "archives"
            archives.mkdir()
            content, shader = b"Level\tAmazon\n0\t0\n", b"BTCDBTCDsynthetic"
            archive = archives / "00-00000001.data"
            archive.write_bytes(content + shader)
            entries = []
            for path, data, offset in (("data/global/excel/experience.txt", content, 0),
                                       ("data/hd/shaders/cs_synthetic.bin", shader, len(content))):
                span = Span(0, len(data), 0, ekey(data, offset).hex(), len(data), len(data), 0, "n")
                entries.append(File(path, len(entries) * 12, (span,)))
            output = root / "out"
            output.mkdir()
            original = hashlib.sha256(archive.read_bytes()).hexdigest()
            result = prepare_resources(Index(tuple(entries), "synthetic"), ArchiveReader((archives,)), output)
            manifest = (output / "generator-romfs/data/build_manifest.psv").read_text()
            self.assertIn("recovered-ft-0", manifest)
            self.assertIn("recovered-tvfs-0a", manifest)
            self.assertIn(hashlib.md5(content).hexdigest(), manifest)
            self.assertFalse(result["generator_manifest"]["original_extraction_manifest"])
            self.assertEqual(hashlib.sha256(archive.read_bytes()).hexdigest(), original)
            self.assertEqual((output / "shader-packs/cs_synthetic.bin").read_bytes(), shader)


class ModuleMetadataTest(unittest.TestCase):
    def fixture(self, root, mutate=None, lie=False):
        app = root / "app"
        (app / "dm").mkdir(parents=True)
        originals = {}
        for module in ("main", "sdk"):
            for suffix in ("fixups.tsv", "gates.tsv"):
                name = f"{module}.{suffix}"
                originals[name] = f"synthetic original {name}".encode()
                (app / "dm" / name).write_bytes(originals[name])
        output = root / "fixture"
        output.mkdir()
        executable = Path(sys.executable)
        options = Options(app, root, root, executable, executable, executable, root / "out")

        class Inspector:
            def run(self, stage, command, cwd):
                module = command[command.index("-module") + 1]
                fixups = Path(command[command.index("-fixups") + 1])
                self_outer.assertTrue(fixups.is_relative_to(output / "input-snapshot"))
                target = Path(command[command.index("-out") + 1])
                target.mkdir()
                image = b"synthetic inspected image"
                name = f"{module}.patched.reconstructed.bin"
                (target / name).write_bytes(image)
                metadata = {"size": fixups.stat().st_size,
                            "sha256": hashlib.sha256(fixups.read_bytes()).hexdigest()}
                if lie:
                    metadata["sha256"] = "0" * 64
                manifest = {"schema_version": 2, "module": module,
                            "layout_profile": "dm-split-sections-v1",
                            "source_sections_verified": True, "relocations_applied": False,
                            "runtime_initialization_performed": False,
                            "input_fixups": metadata,
                            "reconstructed": {"name": name, "size": len(image),
                                              "sha256": hashlib.sha256(image).hexdigest()}}
                (target / "manifest.json").write_text(json.dumps(manifest))
                if mutate and module == "main":
                    mutate(app)

        self_outer = self
        return options, output, Inspector(), originals

    def test_external_mutation_never_changes_inspected_or_runtime_metadata(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)

            def mutate(app):
                for path in (app / "dm").iterdir():
                    path.write_bytes(b"changed while first inspector returned")

            options, output, runner, originals = self.fixture(root, mutate=mutate)
            modules = inspector_modules(options, output, runner)
            for module in ("main", "sdk"):
                for suffix in ("fixups.tsv", "gates.tsv"):
                    name = f"{module}.{suffix}"
                    identity = modules[module]["runtime_files"][name]
                    self.assertEqual((output / identity["path"]).read_bytes(), originals[name])
                    self.assertEqual(identity["sha256"], hashlib.sha256(originals[name]).hexdigest())
                self.assertEqual(modules[module]["input_fixups"]["sha256"],
                                 modules[module]["runtime_files"][f"{module}.fixups.tsv"]["sha256"])
            with self.assertRaisesRegex(ValueError, "app module metadata changed"):
                verify_module_metadata(modules, output)

    def test_gate_and_binary_identity_are_manifested_and_rechecked(self):
        with tempfile.TemporaryDirectory() as directory:
            options, output, runner, originals = self.fixture(Path(directory))
            modules = inspector_modules(options, output, runner)
            verify_module_metadata(modules, output)
            for module in ("main", "sdk"):
                self.assertEqual(set(modules[module]["runtime_files"]),
                                 {f"{module}.{suffix}" for suffix in
                                  ("patched.bin", "fixups.tsv", "gates.tsv")})
            gate = output / modules["sdk"]["runtime_files"]["sdk.gates.tsv"]["path"]
            gate.write_bytes(b"altered owned runtime gate")
            with self.assertRaisesRegex(ValueError, "runtime module file changed: sdk.gates.tsv"):
                verify_module_metadata(modules, output)

    def test_source_gate_mutation_is_rejected_before_publication(self):
        with tempfile.TemporaryDirectory() as directory:
            options, output, runner, originals = self.fixture(Path(directory))
            modules = inspector_modules(options, output, runner)
            (options.app / "dm/sdk.gates.tsv").write_bytes(b"external gate mutation")
            with self.assertRaisesRegex(ValueError, "app module metadata changed.*sdk.gates.tsv"):
                verify_module_metadata(modules, output)

    def test_inspector_fixups_digest_must_match_snapshot(self):
        with tempfile.TemporaryDirectory() as directory:
            options, output, runner, originals = self.fixture(Path(directory), lie=True)
            with self.assertRaisesRegex(ValueError, "inspector fixups identity differs"):
                inspector_modules(options, output, runner)


if __name__ == "__main__":
    unittest.main()
