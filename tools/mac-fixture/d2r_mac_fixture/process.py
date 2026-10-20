"""Owned build process groups and atomic publication of a new fixture directory."""

from contextlib import contextmanager
import codecs
import ctypes
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import selectors
import subprocess
import sys
import tempfile
import time


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def write_json(path: Path, value: object) -> None:
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def publish_new(source: Path, destination: Path) -> None:
    """Publish with an OS no-replace rename; an existence precheck is insufficient."""
    library = ctypes.CDLL(None, use_errno=True)
    if sys.platform == "darwin":
        rename = library.renamex_np
        rename.argtypes = [ctypes.c_char_p, ctypes.c_char_p, ctypes.c_uint]
        result = rename(os.fsencode(source), os.fsencode(destination), 0x00000004)
    elif sys.platform.startswith("linux"):
        rename = library.renameat2
        rename.argtypes = [ctypes.c_int, ctypes.c_char_p, ctypes.c_int,
                           ctypes.c_char_p, ctypes.c_uint]
        result = rename(-100, os.fsencode(source), -100, os.fsencode(destination), 1)
    else:
        raise OSError("this platform has no implemented atomic no-replace publication")
    if result:
        code = ctypes.get_errno()
        raise OSError(code, os.strerror(code), str(destination))


@contextmanager
def new_output(destination: Path, keep_failed: bool = False):
    if destination.exists() or destination.is_symlink():
        raise FileExistsError(f"output already exists: {destination}")
    if not destination.parent.is_dir():
        raise FileNotFoundError(f"output parent must already exist: {destination.parent}")
    staging = Path(tempfile.mkdtemp(prefix=f".{destination.name}.prepare-",
                                    dir=destination.parent))
    published = False
    try:
        yield staging
        publish_new(staging, destination)
        published = True
    finally:
        if not published:
            if keep_failed:
                print(f"Failed preparation retained at {staging}", file=sys.stderr)
            else:
                shutil.rmtree(staging)


def contained(path: Path, root: Path) -> bool:
    return path == root or root in path.parents


def check_scopes(output: Path, inputs: tuple[Path, ...]) -> None:
    for path in inputs:
        if contained(output, path) or contained(path, output):
            raise ValueError(f"output and input scopes overlap: {output}, {path}")


class Runner:
    def __init__(self, root: Path, environment: dict[str, str], timeout: float):
        self.root, self.environment, self.timeout = root, environment, timeout
        (root / "logs").mkdir()
        self.receipts: list[dict] = []

    @staticmethod
    def stop(process: subprocess.Popen) -> None:
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except ProcessLookupError:
            return
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            pass
        # A finished leader can leave children holding the build output pipe.
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        process.wait(timeout=5)

    def run(self, stage: str, command: list[str], cwd: Path) -> None:
        if any(receipt["stage"] == stage for receipt in self.receipts):
            raise ValueError(f"duplicate stage receipt: {stage}")
        log_path = self.root / "logs" / f"{stage}.log"
        started = time.time()
        receipt = {"stage": stage, "command": command, "cwd": str(cwd),
                   "started_epoch": started, "log": str(log_path.relative_to(self.root)),
                   "environment": dict(self.environment)}
        print(f"Preparing {stage}", flush=True)
        with log_path.open("w", encoding="utf-8") as log:
            process = subprocess.Popen(command, cwd=cwd, env=self.environment,
                                       stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                       bufsize=0, start_new_session=True)
            assert process.stdout is not None
            os.set_blocking(process.stdout.fileno(), False)
            decoder = codecs.getincrementaldecoder("utf-8")(errors="replace")
            deadline = time.monotonic() + self.timeout
            try:
                with selectors.DefaultSelector() as selector:
                    selector.register(process.stdout, selectors.EVENT_READ)
                    while selector.get_map():
                        remaining = deadline - time.monotonic()
                        if remaining <= 0:
                            raise subprocess.TimeoutExpired(command, self.timeout)
                        for key, _ in selector.select(min(remaining, 0.2)):
                            chunk = os.read(key.fd, 65536)
                            if not chunk:
                                selector.unregister(key.fileobj)
                            else:
                                text = decoder.decode(chunk)
                                log.write(text)
                                log.flush()
                                print(text, end="", flush=True)
                final = decoder.decode(b"", final=True)
                log.write(final)
                print(final, end="", flush=True)
                receipt["exit_code"] = process.wait(timeout=max(0, deadline - time.monotonic()))
                try:
                    os.killpg(process.pid, 0)
                except ProcessLookupError:
                    pass
                else:
                    self.stop(process)
                    receipt["residual_owned_group_stopped"] = True
            except BaseException:
                self.stop(process)
                receipt["exit_code"] = process.returncode
                receipt["interrupted_or_timed_out"] = True
                raise
            finally:
                process.stdout.close()
                receipt["elapsed_seconds"] = time.time() - started
                self.receipts.append(receipt)
                write_json(self.root / "stage-receipts.json", self.receipts)
            if receipt["exit_code"]:
                raise subprocess.CalledProcessError(receipt["exit_code"], command)
