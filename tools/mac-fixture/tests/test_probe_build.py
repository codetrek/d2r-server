import copy
import io
import json
import os
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

from d2r_mac_fixture.probe_build import (
    BASELINE_CFLAGS, MODULE_PROFILE, UPSTREAM_REVISION, inside, parse_host_command,
    probe_environment, recheck_source, snapshot_native_sources, validate_native_profile,
    verify_native_sources, verify_source_archive, verify_symbols,
)
from d2r_mac_fixture.process import sha256


REQUIRED = ["d2r_observed_run_module_inits", "run_module_inits", "d2r_probe_server_send",
            "d2r_probe_client_send", "d2r_probe_system_pop", "d2r_probe_gameplay_pop",
            "d2r_probe_client_high_pop", "d2r_probe_client_low_pop"]


def native_profile():
    return {"schema_version": 1, "upstream_revision": UPSTREAM_REVISION,
            "platform": "darwin", "architecture": "arm64", "pointer_bytes": 8,
            "trace_format_version": 2,
            "main_translation_unit_define": "run_module_inits=d2r_observed_run_module_inits",
            "wrapper_symbol": "d2r_observed_run_module_inits", "original_init_symbol": "run_module_inits",
            "integration_source": "integration.c", "build_sources": ["integration.c", "profile.c", "observer.c", "shims.S"],
            "build_headers": ["format.h", "abi.h", "probe.h", "internal.h"],
            "required_symbols": REQUIRED, "required_environment": ["D2R_PROBE_MODULES", "D2R_TRANSPORT_TRACE", "D2R_PROBE_STOP_PATH"],
            "modules": {name: {"filename": f"{name}.patched.bin", "size": size, "sha256": digest}
                        for name, (size, digest) in MODULE_PROFILE.items()}}


class ProbeBuildTest(unittest.TestCase):
    def test_dry_run_command_preserves_flags_and_exact_single_source_ownership(self):
        command = (f"clang {BASELINE_CFLAGS} -I. -I../gfx/nvn/include -fobjc-arc -o dmhost "
                   "app_mac.m main.c loader.c ../gfx/nvn/nvn_entry.S -framework Cocoa -framework Metal")
        output = 'make: Entering directory "/synthetic path"\npython3 generator.py > output\n' + command
        prefix, sources, frameworks = parse_host_command(output)
        self.assertEqual(prefix[1:7], BASELINE_CFLAGS.split())
        self.assertEqual(sources, ["app_mac.m", "main.c", "loader.c", "../gfx/nvn/nvn_entry.S"])
        self.assertEqual(frameworks, ["-framework", "Cocoa", "-framework", "Metal"])
        for altered in (command.replace("main.c", "main.c main.c"),
                        command.replace("loader.c", "loader.c loader.c"),
                        command.replace("-O1", "-O3"), command + "\n" + command,
                        command.replace("-o dmhost", "-o unexpected")):
            with self.subTest(altered=altered), self.assertRaises(ValueError):
                parse_host_command(altered)

    def test_symbol_proof_requires_one_real_init_and_all_observer_entries(self):
        main = "                 U _d2r_observed_run_module_inits\n"
        binary = "\n".join(f"00001234 T _{name}" for name in REQUIRED)
        result = verify_symbols(main, binary, REQUIRED)
        self.assertEqual(set(result["final_single_definitions"]), {"_" + name for name in REQUIRED})
        for altered_main, altered_binary in (
                (main + " U _run_module_inits\n", binary),
                (main, binary + "\n00005678 T _run_module_inits"),
                (main, binary.replace(" T _d2r_probe_system_pop", " U _d2r_probe_system_pop"))):
            with self.assertRaises(ValueError):
                verify_symbols(altered_main, altered_binary, REQUIRED)

    def test_full_source_verification_preserves_explicit_config_only(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "source"
            (source / "dm/host").mkdir(parents=True)
            archive = root / "upstream.tar"
            originals = {"dm/host/main.c": b"original-main", "dm/host/loader.c": b"original-loader",
                         "dm/config.h": b"original-config"}
            with tarfile.open(archive, "w") as tar:
                for name, data in originals.items():
                    info = tarfile.TarInfo(name)
                    info.size = len(data)
                    tar.addfile(info, io.BytesIO(data))
                    (source / name).write_bytes(data)
            (source / "dm/config.h").write_bytes(b"honest-generated-config")
            records = verify_source_archive(archive, source, {"dm/config.h": sha256(source / "dm/config.h")})
            self.assertEqual(len(records), 3)
            recheck_source(source, records)
            (source / "dm/host/main.c").write_bytes(b"silently-patched-main")
            with self.assertRaisesRegex(ValueError, "input identity mismatch"):
                recheck_source(source, records)
            with self.assertRaises(ValueError):
                verify_source_archive(archive, source, {"dm/config.h": sha256(source / "dm/config.h")})

    def test_native_profile_does_not_allow_different_module_or_platform(self):
        profile = native_profile()
        validate_native_profile(profile)
        for change in (lambda value: value.update(architecture="x86_64"),
                       lambda value: value.update(trace_format_version=1),
                       lambda value: value["required_symbols"].remove("d2r_probe_client_high_pop"),
                       lambda value: value["modules"]["sdk"].update(sha256="0" * 64),
                       lambda value: value["build_sources"].append("other.c"),
                       lambda value: value["required_environment"].remove("D2R_PROBE_STOP_PATH")):
            altered = copy.deepcopy(profile)
            change(altered)
            with self.assertRaises(ValueError):
                validate_native_profile(altered)

    def test_native_profile_is_read_from_owned_snapshot_and_original_rechecked(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "native"
            source.mkdir()
            (source / "profile.json").write_text(json.dumps(native_profile()))
            snapshot, hashes = snapshot_native_sources(source, root)
            (source / "profile.json").write_text("changed after snapshot")
            validate_native_profile(json.loads((snapshot / "profile.json").read_text()))
            with self.assertRaises(ValueError):
                verify_native_sources(source, snapshot, hashes)

    def test_scope_and_environment_keep_capture_configuration_out_of_build(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with self.assertRaises(ValueError):
                inside(root, "../outside")
            with patch.dict(os.environ, {"PRIVATE_TOKEN": "secret", "D2R_TRANSPORT_TRACE": "private",
                                         "D2R_PROBE_STOP_PATH": "stop-marker",
                                         "DM_SAVE": "original-save", "DM_CLOUD_REAL": "1"}):
                env = probe_environment(root, root / "parent")
            for key in ("PRIVATE_TOKEN", "D2R_TRANSPORT_TRACE", "D2R_PROBE_STOP_PATH", "DM_SAVE", "DM_CLOUD_REAL"):
                self.assertNotIn(key, env)
            self.assertEqual(env["HOME"], os.environ["HOME"])
            self.assertTrue(Path(env["TMPDIR"]).is_relative_to(root))
            self.assertTrue(Path(env["CLANG_MODULE_CACHE_PATH"]).is_relative_to(root))


if __name__ == "__main__":
    unittest.main()
