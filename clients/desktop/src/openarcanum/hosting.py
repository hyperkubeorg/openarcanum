"""Communities hosted on this computer (docs/user-guide.md "Host a community
on this computer").

The app ships the Go community server (`community`, the same program an
operator runs on a VPS). Hosting a community here runs `community init` into
a folder of this profile's data folder, and then `community run` as a child
process for as long as the app runs, also while its window is closed to the
tray. Each hosted community has its own folder:

    <data>/hosted/<id>/host.json    whether it should run (Start/Stop)
    <data>/hosted/<id>/server/      the server's data directory: its keys,
                                    database, storage key and backups
    <log>/hosted/<id>.log           the server's own log (rotated by it)

`<id>` is random and says nothing about the community; which community a
folder serves is recorded in the encrypted profile (the community's
`hosted` field), so the folder names reveal nothing before unlock. The
folders are created 0700 and the server writes its key files 0600.

Supervision: a child that exits on its own is started again, after 1 s,
then 2, 4 … up to 60 s between attempts (back to 1 s once one has run for a
minute). The child runs with `-stop-with-stdin` and a pipe on its standard
input: stopping it (Stop, Quit, a moved data folder) closes the pipe, and
the server shuts down cleanly and tells the maintainer nodes it is going
offline; if the app itself dies, the operating system closes the pipe, so a
server is never left running without the app. Only the server's own log
lines (IDs and sizes, never content or keys) and its exit status are logged.
"""

from __future__ import annotations

import asyncio
import contextlib
import json
import logging
import os
import secrets
import shutil
import subprocess
import sys
import time
from collections.abc import Callable
from dataclasses import dataclass, field
from pathlib import Path

from openarcanum.errors import UserError
from openarcanum.i18n import _
from openarcanum.paths import atomic_write, ensure_dir

log = logging.getLogger(__name__)

HOST_FILE = "host.json"
SERVER_DIR = "server"
CLAIM_FILE = "claim-code.txt"
# What the server's own log may use (log_max_size, log_keep).
LOG_MAX_SIZE = 4 << 20
LOG_KEEP = 3
# Restart backoff, and how long a run must last to reset it.
BACKOFF_FIRST = 1.0
BACKOFF_MAX = 60.0
STABLE_AFTER = 60.0
# How long a stopping server gets to shut down cleanly before it is killed.
STOP_GRACE = 8.0
# The environment variable that points the app at another `community`
# program (development; tests).
BINARY_ENV = "OPENARCANUM_COMMUNITY_BIN"


def binary_name() -> str:
    return "community.exe" if os.name == "nt" else "community"


def community_binary() -> Path | None:
    """The bundled `community` program. In a built app (frozen) only the
    copy inside the bundle (`servers/`, docs/packaging.md): never a program
    found next to the app or named by the environment, which would then run
    with the user's rights whenever a community is hosted. From source:
    $OPENARCANUM_COMMUNITY_BIN, else the repository's bin/ from `make build`."""
    bundle = getattr(sys, "_MEIPASS", None)
    if getattr(sys, "frozen", False):
        if not bundle:
            return None
        p = Path(bundle) / "servers" / binary_name()
        return p if p.is_file() else None
    override = os.environ.get(BINARY_ENV)
    if override:
        p = Path(override)
        return p if p.is_file() else None
    # clients/desktop/src/openarcanum/hosting.py -> the repository root.
    p = Path(__file__).resolve().parents[4] / "bin" / binary_name()
    return p if p.is_file() else None


def binary_platform() -> str:
    """What the bundled program runs on, for the Save dialog's file name."""
    import platform

    machine = platform.machine().lower()
    arches = {"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}
    arch = arches.get(machine, machine or "unknown")
    osname = "windows" if os.name == "nt" else "macos" if sys.platform == "darwin" else "linux"
    return f"{osname}-{arch}"


@dataclass
class Hosted:
    hid: str
    folder: Path
    enabled: bool = True
    # stopped, starting, running, restarting, failed
    state: str = "stopped"
    error: str = ""
    restarts: int = 0
    since: float = 0.0
    proc: asyncio.subprocess.Process | None = None
    task: asyncio.Task | None = None
    wake: asyncio.Event = field(default_factory=asyncio.Event)
    # Restart (not a crash): start again at once.
    again: bool = False

    @property
    def data(self) -> Path:
        return self.folder / SERVER_DIR

    @property
    def claim_file(self) -> Path:
        return self.folder / CLAIM_FILE


class HostManager:
    """Every community hosted from this profile, and their server processes.
    Runs on the core's asyncio loop; `running_count` may be read from any
    thread."""

    def __init__(self, root: Callable[[], Path], log_dir: Callable[[], Path], changed: Callable[[str], None]):
        self._root = root
        self._log_dir = log_dir
        self._changed = changed
        self.items: dict[str, Hosted] = {}
        self.running = 0
        self._loaded = False

    # ---------------------------------------------------------------- folders

    @property
    def root(self) -> Path:
        return self._root()

    def log_file(self, hid: str) -> Path:
        return self._log_dir() / "hosted" / f"{hid}.log"

    def load(self) -> None:
        """Find the hosted communities in the data folder (once)."""
        if self._loaded:
            return
        self._loaded = True
        if not self.root.is_dir():
            return
        for folder in sorted(self.root.iterdir()):
            # Each is a host: its communities in server/communities/<id>/.
            if not any((folder / SERVER_DIR / "communities").glob("*/server.key")):
                continue
            enabled = True
            with contextlib.suppress(OSError, ValueError):
                enabled = bool(json.loads((folder / HOST_FILE).read_text(encoding="utf-8")).get("enabled", True))
            self.items[folder.name] = Hosted(folder.name, folder, enabled)

    def reload(self) -> None:
        """Find them again (the data folder moved; none is running)."""
        self.items.clear()
        self._loaded = False
        self.load()

    def _save(self, h: Hosted) -> None:
        atomic_write(h.folder / HOST_FILE, json.dumps({"v": 1, "enabled": h.enabled}).encode())

    def get(self, hid: str) -> Hosted:
        h = self.items.get(hid)
        if h is None:
            raise UserError(_("That community isn't hosted on this computer."))
        return h

    def _set(self, h: Hosted, state: str, error: str = "") -> None:
        h.state, h.error = state, error
        self.running = sum(1 for x in self.items.values() if x.proc is not None)
        self._changed(h.hid)

    # ---------------------------------------------------------------- creating

    async def create(self, maintainers: list[str], relays: list[str]) -> Hosted:
        """`community init` into a new folder (automatic relays from the
        maintainers' registry, `relays` preferred), not started yet."""
        program = community_binary()
        if program is None:
            raise UserError(_("This copy of OpenArcanum doesn't include the community server program."))
        self.load()
        ensure_dir(self.root)
        hid = secrets.token_hex(8)
        h = Hosted(hid, self.root / hid)
        ensure_dir(h.folder)
        args = [str(program), "init", "-data", str(h.data), "-automatic-relays", "-claim-file", str(h.claim_file)]
        for m in maintainers:
            args += ["-maintainer", m]
        for r in relays:
            args += ["-relay", r]
        try:
            proc = await asyncio.create_subprocess_exec(
                *args, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, **_no_window()
            )
            _out, err = await proc.communicate()
        except OSError as e:
            shutil.rmtree(h.folder, ignore_errors=True)
            raise UserError(_("Couldn't start the community server program: {error}").format(error=e)) from None
        if proc.returncode != 0:
            shutil.rmtree(h.folder, ignore_errors=True)
            reason = (err or b"").decode(errors="replace").strip().splitlines()
            why = reason[-1] if reason else proc.returncode
            raise UserError(_("Couldn't set up the community server: {error}").format(error=why))
        if os.name == "posix":
            os.chmod(h.data, 0o700)
        self._save(h)
        self.items[hid] = h
        log.info("hosting: created %s", hid)
        return h

    def claim_code(self, hid: str) -> str:
        try:
            return self.get(hid).claim_file.read_text(encoding="utf-8").strip()
        except OSError:
            raise UserError(_("The new community server has no claim code.")) from None

    async def remove(self, hid: str) -> None:
        """Stop a hosted community and delete its folder (a community that
        was never claimed: creating it failed half-way)."""
        h = self.items.get(hid)
        if h is None:
            return
        await self.stop(hid, remember=False)
        self.items.pop(hid, None)
        await asyncio.to_thread(shutil.rmtree, h.folder, True)
        self._changed(hid)

    # ---------------------------------------------------------------- running

    def start_all(self) -> None:
        """Start every hosted community that isn't stopped (app start)."""
        self.load()
        for h in self.items.values():
            if h.enabled:
                self._launch(h)

    async def start(self, hid: str) -> None:
        h = self.get(hid)
        if not h.enabled:
            h.enabled = True
            self._save(h)
        self._launch(h)

    def _launch(self, h: Hosted) -> None:
        if h.task is None or h.task.done():
            h.task = asyncio.get_running_loop().create_task(self._supervise(h), name=f"hosted {h.hid}")

    async def stop(self, hid: str, remember: bool = True, grace: float = STOP_GRACE) -> None:
        """Stop a hosted community. remember: it stays stopped when the app
        starts again (Stop), rather than only until the app quits (Quit)."""
        h = self.get(hid)
        h.enabled = False
        if remember:
            self._save(h)
        h.wake.set()
        await self._end(h, grace)
        if h.task is not None:
            h.task.cancel()
            with contextlib.suppress(BaseException):
                await h.task
            h.task = None
        h.proc = None
        self._set(h, "stopped")

    async def stop_all(self, grace: float = 3.0) -> None:
        """Quit: stop every server (they start again next time)."""
        await asyncio.gather(
            *(self.stop(h.hid, remember=False, grace=grace) for h in list(self.items.values())), return_exceptions=True
        )

    async def restart(self, hid: str) -> None:
        h = self.get(hid)
        h.again = h.proc is not None
        await self._end(h, STOP_GRACE)
        await self.start(hid)

    async def _end(self, h: Hosted, grace: float) -> None:
        proc = h.proc
        if proc is None or proc.returncode is not None:
            return
        # Closing standard input is the clean stop (-stop-with-stdin).
        if proc.stdin is not None:
            with contextlib.suppress(Exception):
                proc.stdin.close()
        try:
            await asyncio.wait_for(proc.wait(), grace)
        except TimeoutError:
            log.warning("hosting: %s didn't stop within %.0f s; ending it", h.hid, grace)
            with contextlib.suppress(ProcessLookupError):
                proc.kill()
            with contextlib.suppress(Exception):
                await proc.wait()

    def _env(self, h: Hosted) -> dict:
        env = dict(os.environ)
        logf = self.log_file(h.hid)
        ensure_dir(logf.parent)
        env.update(
            OPENARCANUM_COMMUNITY_LOG_FILE=str(logf),
            OPENARCANUM_COMMUNITY_LOG_MAX_SIZE=str(LOG_MAX_SIZE),
            OPENARCANUM_COMMUNITY_LOG_KEEP=str(LOG_KEEP),
        )
        return env

    async def _supervise(self, h: Hosted) -> None:
        backoff = BACKOFF_FIRST
        while h.enabled:
            program = community_binary()
            if program is None:
                self._set(h, "failed", _("This copy of OpenArcanum doesn't include the community server program."))
                return
            self._set(h, "starting")
            try:
                proc = await asyncio.create_subprocess_exec(
                    str(program),
                    "run",
                    "-data",
                    str(h.data),
                    "-stop-with-stdin",
                    stdin=subprocess.PIPE,
                    stdout=subprocess.DEVNULL,
                    stderr=subprocess.PIPE,
                    env=self._env(h),
                    **_no_window(),
                )
            except OSError as e:
                log.warning("hosting: %s couldn't start: %s", h.hid, e)
                self._set(h, "restarting", _("The community server couldn't start: {error}").format(error=e))
                await self._pause(h, backoff)
                backoff = min(backoff * 2, BACKOFF_MAX)
                continue
            h.proc, h.since = proc, time.time()
            log.info("hosting: %s running (pid %d)", h.hid, proc.pid)
            self._set(h, "running")
            started = time.monotonic()
            last = await _drain(proc, h.hid)
            code = await proc.wait()
            h.proc = None
            if not h.enabled:
                break
            if h.again:
                h.again = False
                continue
            if time.monotonic() - started >= STABLE_AFTER:
                backoff = BACKOFF_FIRST
            h.restarts += 1
            log.warning("hosting: %s exited with status %s; starting it again in %.0f s", h.hid, code, backoff)
            why = last or _("exit status {code}").format(code=code)
            self._set(
                h,
                "restarting",
                _("The community server stopped ({reason}); starting it again in {seconds} s.").format(
                    reason=why, seconds=round(backoff)
                ),
            )
            await self._pause(h, backoff)
            backoff = min(backoff * 2, BACKOFF_MAX)
        self._set(h, "stopped")

    async def _pause(self, h: Hosted, seconds: float) -> None:
        h.wake.clear()
        with contextlib.suppress(TimeoutError):
            await asyncio.wait_for(h.wake.wait(), seconds)

    # ---------------------------------------------------------------- status

    def status(self, hid: str) -> dict:
        h = self.get(hid)
        return {
            "id": h.hid,
            "state": h.state,
            "enabled": h.enabled,
            "error": h.error,
            "restarts": h.restarts,
            "since": int(h.since * 1000) if h.proc is not None else 0,
            "data_dir": str(h.data),
            "log_file": str(self.log_file(h.hid)),
        }

    async def reregister(self, hid: str) -> None:
        """`community reregister` (after maintainers shut the community down
        while it was offline), then start it again so it publishes now."""
        h = self.get(hid)
        program = community_binary()
        if program is None:
            raise UserError(_("This copy of OpenArcanum doesn't include the community server program."))
        proc = await asyncio.create_subprocess_exec(
            str(program),
            "reregister",
            "-data",
            str(h.data),
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.PIPE,
            **_no_window(),
        )
        _out, err = await proc.communicate()
        if proc.returncode != 0:
            reason = (err or b"").decode(errors="replace").strip().splitlines()
            raise UserError(
                _("Couldn't bring the community back: {error}").format(error=reason[-1] if reason else proc.returncode)
            )
        log.info("hosting: %s re-registered", hid)
        await self.restart(hid)


async def _drain(proc: asyncio.subprocess.Process, hid: str) -> str:
    """Log what the server prints on its standard error (only before its
    log file opens, and its last words when it fails) until it closes;
    returns the last line."""
    last = ""
    if proc.stderr is None:
        return last
    while True:
        line = await proc.stderr.readline()
        if not line:
            return last
        text = line.decode(errors="replace").strip()[:300]
        if text:
            last = text
            log.info("hosting: %s: %s", hid, text)


def _no_window() -> dict:
    """Windows: no console window for the server process."""
    if os.name == "nt":
        return {"creationflags": subprocess.CREATE_NO_WINDOW}
    return {}


async def copy_binary(dest: str) -> str:
    """Save the bundled community server program to dest (a file, or a
    folder to put it in), executable. Returns where it went."""
    program = community_binary()
    if program is None:
        raise UserError(_("This copy of OpenArcanum doesn't include the community server program."))
    target = Path(dest).expanduser()
    if target.is_dir():
        target = target / binary_name()
    if not target.parent.is_dir():
        raise UserError(_("That folder doesn't exist."))
    try:
        await asyncio.to_thread(shutil.copyfile, program, target)
        if os.name == "posix":
            os.chmod(target, 0o755)
    except OSError as e:
        raise UserError(_("Couldn't save the file: {error}").format(error=e.strerror or e)) from None
    return str(target)
