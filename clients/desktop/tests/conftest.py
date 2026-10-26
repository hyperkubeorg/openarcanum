"""Shared fixtures: run the real Go binaries (bin/) for integration tests."""

from __future__ import annotations

import asyncio
import contextlib
import json
import os
import random
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time
from pathlib import Path

import pytest

# No test reaches the project's public maintainer node: the built-in list is a
# reserved name (.invalid) here, in this process and in every app it starts.
os.environ["OPENARCANUM_TEST_BUILTIN_MAINTAINERS"] = "https://maintainer.example.invalid"

from openarcanum import crypto

REPO = Path(__file__).resolve().parents[3]
BIN = Path(os.environ.get("OPENARCANUM_BIN", REPO / "bin"))


# GitHub Actions (and most CI systems) set CI=true. There, a missing test
# harness (a program, a display) fails the test instead of skipping it, so a
# green run can't hide tests that never ran.
CI = os.environ.get("CI", "").lower() == "true"


def missing_harness(reason: str) -> None:
    """Skip, or in CI fail: `reason` names what's missing."""
    if CI:
        pytest.fail(f"{reason} (CI must provide it)", pytrace=False)
    pytest.skip(reason)


def gui_available() -> bool:
    """Whether real windows can open: on Linux under Xvfb with a private
    session bus (xvfb-run, dbus-run-session); Windows and macOS (CI runners
    included) have a desktop session."""
    if sys.platform.startswith("linux"):
        return bool(shutil.which("xvfb-run") and shutil.which("dbus-run-session"))
    return sys.platform in ("win32", "darwin")


GUI = gui_available()


def pytest_configure(config):
    config.addinivalue_line("markers", "gui: opens real windows (a display; Xvfb and a session bus on Linux)")
    config.addinivalue_line("markers", "os_keychain: uses this computer's real keychain (no stand-in)")


@pytest.fixture(autouse=True)
def _no_os_keychain(request):
    """Every profile a test creates stays signed in (keychain.py), so no test
    touches this computer's real keychain unless marked os_keychain: without
    one, the key goes to the key file under the test's own --home. Tests
    that want a keychain install a stand-in (test_keychain.py file_keyring);
    UI tests get the same through uidriver's environment."""
    if request.node.get_closest_marker("os_keychain"):
        yield
        return
    import keyring
    from keyring.backends import fail

    old = keyring.get_keyring()
    keyring.set_keyring(fail.Keyring())
    try:
        yield
    finally:
        keyring.set_keyring(old)


def pytest_runtest_setup(item):
    if item.get_closest_marker("gui") and not GUI:
        missing_harness("needs a display: xvfb-run and dbus-run-session on Linux")


def freeze(pid: int) -> None:
    """Stop a process in place (it keeps its sockets open but answers
    nothing), as a hung server would: SIGSTOP, or NtSuspendProcess on Windows."""
    if os.name == "nt":
        _nt_process_call("NtSuspendProcess", pid)
    else:
        os.kill(pid, signal.SIGSTOP)


def thaw(pid: int) -> None:
    if os.name == "nt":
        _nt_process_call("NtResumeProcess", pid)
    else:
        os.kill(pid, signal.SIGCONT)


def _nt_process_call(fn: str, pid: int) -> None:
    import ctypes

    k32 = ctypes.WinDLL("kernel32", use_last_error=True)
    k32.OpenProcess.restype = ctypes.c_void_p
    h = k32.OpenProcess(0x0800, False, pid)  # PROCESS_SUSPEND_RESUME
    if not h:
        raise OSError(ctypes.get_last_error(), f"OpenProcess({pid})")
    try:
        status = getattr(ctypes.WinDLL("ntdll"), fn)(ctypes.c_void_p(h))
        if status != 0:
            raise OSError(status, f"{fn}({pid})")
    finally:
        k32.CloseHandle(ctypes.c_void_p(h))


def free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def binary(name: str) -> Path:
    path = BIN / (name + (".exe" if os.name == "nt" else ""))
    if not path.exists():
        missing_harness(f"{path} not built (run `make build`)")
    return path


def wait_port(port: int, timeout: float = 30.0) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        with socket.socket() as s:
            if s.connect_ex(("127.0.0.1", port)) == 0:
                return
        time.sleep(0.05)
    raise TimeoutError(f"port {port} never opened")


class Proc:
    """A child process with line-oriented stdout."""

    def __init__(self, args: list[str], env: dict | None = None, cwd: Path | None = None):
        full_env = dict(os.environ)
        full_env.update(env or {})
        self.args = args
        # Logs go to a file, never a pipe nobody reads: a full pipe blocks
        # the child's next write (the servers log to stderr).
        self.err = tempfile.TemporaryFile(mode="w+")  # noqa: SIM115 (open for the child's lifetime; closed in stop)
        self.p = subprocess.Popen(
            args,
            stdout=subprocess.PIPE,
            stderr=self.err,
            stdin=subprocess.DEVNULL,
            env=full_env,
            cwd=cwd,
            text=True,
        )

    def json_line(self, timeout: float = 15.0) -> dict:
        deadline = time.monotonic() + timeout
        assert self.p.stdout is not None
        while time.monotonic() < deadline:
            line = self.p.stdout.readline()
            if not line:
                if self.p.poll() is not None:
                    raise RuntimeError(f"{self.args[0]} exited: {self.stderr()[-4000:]}")
                continue
            line = line.strip()
            if line.startswith("{"):
                return json.loads(line)
        raise TimeoutError("no JSON line")

    def stderr(self) -> str:
        """What the process has logged so far."""
        self.err.flush()
        self.err.seek(0)
        return self.err.read()

    def stop(self) -> None:
        if self.p.poll() is None:
            self.p.terminate()
            try:
                self.p.wait(5)
            except subprocess.TimeoutExpired:
                self.p.kill()
                self.p.wait()
        self.err.close()


# Chromium in a container: /dev/shm is small (64 MB here), and many test
# windows at once can exhaust it, which makes the engine abort as it starts.
# This flag makes it use the temp dir instead (tests only: desktops have
# plenty). Test helpers that set their own engine flags add ENGINE_TEST_FLAGS.
ENGINE_TEST_FLAGS = "--disable-dev-shm-usage"
os.environ["QTWEBENGINE_CHROMIUM_FLAGS"] = " ".join(
    f for f in (os.environ.get("QTWEBENGINE_CHROMIUM_FLAGS", ""), ENGINE_TEST_FLAGS) if f
)


def headless(cmd: list[str]) -> list[str]:
    """cmd under a private display (Xvfb) and session bus on Linux. The
    display number is random: `xvfb-run -a` picks the lowest free one, and
    two runs starting together can both pick it."""
    if not sys.platform.startswith("linux"):
        return cmd
    display = str(random.randint(1000, 59999))
    return ["dbus-run-session", "--", "xvfb-run", "-n", display, "-s", "-screen 0 1280x800x24", *cmd]


RELAY_PROCS: dict[str, Proc] = {}


@contextlib.contextmanager
def run_relay(**env):
    """Run a relay; "{url}" in a setting is replaced with the relay's own URL."""
    port = free_port()
    url = f"ws://127.0.0.1:{port}/ws"
    e = {
        "OPENARCANUM_RELAY_LISTEN": f"127.0.0.1:{port}",
        "OPENARCANUM_RELAY_MEDIA_UDP_PORT": "0",
        "OPENARCANUM_RELAY_MEDIA_LOOPBACK": "true",
    }
    e.update({f"OPENARCANUM_RELAY_{k.upper()}": str(v).replace("{url}", url) for k, v in env.items()})
    p = Proc([str(binary("relay"))], env=e)
    RELAY_PROCS[url] = p
    try:
        wait_port(port)
        yield url
    finally:
        p.stop()
        RELAY_PROCS.pop(url, None)


def kill_relay(url: str) -> None:
    RELAY_PROCS[url].stop()


@pytest.fixture
def relay_url():
    with run_relay() as url:
        yield url


def run(coro, timeout: float = 60.0):
    """Run a coroutine to completion with an overall timeout."""
    return asyncio.run(asyncio.wait_for(coro, timeout))


# ---------------------------------------------------------------- nodes for end-to-end tests

# Cheap scrypt for clients in this test process (vault files record their own
# parameters). Clients started as separate processes use the real cost.
crypto.DEFAULT_SCRYPT = crypto.ScryptParams(n=1 << 10)


class CommunityNode:
    """A `community` server process with its own data directory."""

    def __init__(self, data: Path, relays: list[str], maintainers: list[str] | None = None, automatic: bool = False):
        self.data = data
        self.relays = relays
        args = [str(binary("community")), "init", "-data", str(data)]
        for r in relays:
            args += ["-relay", r]
        for m in maintainers or []:
            args += ["-maintainer", m]
        if automatic:
            args.append("-automatic-relays")
        out = subprocess.run(args, capture_output=True, text=True, check=True).stdout
        self.claim_code = next(line for line in out.splitlines() if line.startswith("openarcanum-claim:"))
        self.proc: Proc | None = None

    def start(self, program: str = "community", **env) -> CommunityNode:
        """Run the server. program="previewcommunity" runs it with the
        link-preview fetcher allowed to reach loopback addresses (a test
        command, internal/testutil/cmd/previewcommunity)."""
        e = {"OPENARCANUM_COMMUNITY_PUBLISH_INTERVAL": "2s"}
        e.update({f"OPENARCANUM_COMMUNITY_{k.upper()}": str(v) for k, v in env.items()})
        args = ["run"] if program == "community" else []
        self.proc = Proc([str(binary(program)), *args, "-data", str(self.data)], env=e)
        time.sleep(0.3)
        return self

    def stop(self) -> None:
        if self.proc:
            self.proc.stop()
            self.proc = None


META_DIRS: dict[str, str] = {}


@contextlib.contextmanager
def run_meta(**env):
    port = free_port()
    import tempfile

    d = tempfile.mkdtemp()
    META_DIRS[f"http://127.0.0.1:{port}"] = d
    e = {"OPENARCANUM_META_LISTEN": f"127.0.0.1:{port}", "OPENARCANUM_META_DATA_DIR": d}
    # The public directory is off by default; the tests exercise it, and
    # the ones about it being off pass directory_enabled="false".
    e["OPENARCANUM_META_DIRECTORY_ENABLED"] = "true"
    # Test relays register at ws://127.0.0.1 (refused on a real node).
    e["OPENARCANUM_META_ALLOW_PRIVATE_RELAYS"] = "true"
    e.update({f"OPENARCANUM_META_{k.upper()}": str(v) for k, v in env.items()})
    url = f"http://127.0.0.1:{port}"
    META_PROCS[url] = (Proc([str(binary("meta"))], env=e), e, port)
    try:
        wait_port(port)
        yield url
    finally:
        META_PROCS.pop(url)[0].stop()


META_PROCS: dict[str, tuple] = {}


def stop_meta(url: str) -> None:
    """Take a maintainer node down (its data stays)."""
    META_PROCS[url][0].stop()


def start_meta_again(url: str) -> None:
    """Bring a stopped maintainer back on the same port and data."""
    _, env, port = META_PROCS[url]
    META_PROCS[url] = (Proc([str(binary("meta"))], env=env), env, port)
    wait_port(port)


@contextlib.contextmanager
def run_community(
    tmp_path: Path, relays: list[str], maintainers: list[str] | None = None, program: str = "community", **env
):
    node = CommunityNode(tmp_path / f"community-{free_port()}", relays, maintainers)
    node.start(program, **env)
    try:
        yield node
    finally:
        node.stop()


class Events:
    """Collects core events for assertions."""

    def __init__(self):
        self.items: list[tuple[str, dict]] = []
        self.changed = asyncio.Event()

    def __call__(self, name: str, payload: dict) -> None:
        self.items.append((name, payload))
        self.changed.set()

    def of(self, name: str) -> list[dict]:
        return [p for n, p in self.items if n == name]

    async def wait(self, pred, timeout: float = 15.0):
        loop = asyncio.get_running_loop()
        deadline = loop.time() + timeout
        while True:
            for n, p in self.items:
                if pred(n, p):
                    return p
            remaining = deadline - loop.time()
            if remaining <= 0:
                raise TimeoutError("event never arrived")
            self.changed.clear()
            with contextlib.suppress(TimeoutError):
                await asyncio.wait_for(self.changed.wait(), remaining)


async def wait_until(pred, timeout: float = 15.0, interval: float = 0.05):
    loop = asyncio.get_running_loop()
    deadline = loop.time() + timeout
    while True:
        r = pred()
        if asyncio.iscoroutine(r):
            r = await r
        if r:
            return r
        if loop.time() > deadline:
            raise TimeoutError("condition never became true")
        await asyncio.sleep(interval)


PASS = "correct horse battery staple"


async def new_core(home: Path, name: str = "device", maintainers: list[str] | None = None):
    from openarcanum.core import Core
    from openarcanum.paths import Paths

    ev = Events()
    core = Core(Paths.under(home), emit=ev)
    await core.create_profile(PASS, name)
    if maintainers is not None:
        await core.set_setting("maintainers", maintainers)
    return core, ev


@contextlib.contextmanager
def network(tmp_path, **meta_env):
    # Every test client shares 127.0.0.1, so the maintainer's per-IP write
    # limit would count all of them as one; raise only that one (per-inbox
    # limits keep their production defaults).
    meta_env.setdefault("writes_per_minute", "60000")
    with (
        run_meta(**meta_env) as meta,
        run_relay(registry_urls=meta, public_url="{url}", heartbeat_interval="1s") as relay,
    ):
        nodes = []

        def community(name="c"):
            n = CommunityNode(tmp_path / f"{name}-{len(nodes)}", [relay], [meta])
            n.start()
            nodes.append(n)
            return n

        try:
            yield meta, relay, community
        finally:
            for n in nodes:
                n.stop()


async def wait_relay_registered(core):
    await wait_until(lambda: core.meta.relays(), timeout=15)


@pytest.fixture(autouse=True)
def _no_leftover_windows():
    """UI tests start real client processes; kill any a failed test left."""
    yield
    import uidriver

    for app in list(uidriver.RUNNING):
        app.kill()
