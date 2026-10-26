"""One running app per profile, and invite links the OS opens with it.

When someone opens a `openarcanum://invite/...` link, the OS starts the app with
the link as its argument (Windows, Linux) or sends the running app a URL-open
event (macOS). If the profile is already open in another process, the new
process hands the link to it over a local socket and exits; the running app
raises its window and prefills the Join dialog. A launch without a link just
raises the running window.

The socket's name is derived from the user and the profile's config folder, so
`--home` profiles (and other users) never collide. On Linux it is a Unix
socket in the user's private runtime directory (`$XDG_RUNTIME_DIR`) when there
is one, else in the profile's own config folder (0700); on macOS in the
per-user temporary folder; on Windows a named pipe. Qt creates it with
user-only access (`UserAccessOption`).

Names in shared namespaces (Windows pipes, `/tmp` when the path would be too
long for a socket) can be taken first by another local user. So before
handing anything over, and before backing off as a second instance, the
other end is checked: on Linux and macOS the socket file must be ours and the
peer's credentials (SO_PEERCRED, LOCAL_PEERCRED) our user ID; on Windows the
pipe server process's token user must be our SID. If it isn't, the app logs
it and runs on its own, without single-instance hand-off; it never sends a
link to, or exits for, someone else's program.

Messages are one JSON line of at most MAX_MESSAGE bytes; anything else is
dropped. A link only ever prefills the Join dialog, which the user still
confirms (docs/threat-model.md, "Installers and invite links").

`hand_off` works without a QApplication (plain sockets and pipes), so a
second launch exits before it loads anything else; `Listener` lives on the
Qt main thread.
"""

from __future__ import annotations

import contextlib
import getpass
import hashlib
import json
import logging
import os
import socket
import stat
import struct
import sys
import time
from collections.abc import Callable
from pathlib import Path

from openarcanum import PRODUCT_NAME, codes

log = logging.getLogger(__name__)

MAX_MESSAGE = codes.MAX_LINK + 64
CONNECT_MS = 1500


# Room for a Unix socket path (sun_path is 108 bytes on Linux, 104 on macOS).
MAX_SOCKET_PATH = 100
SOCKET_FILE = "instance.sock"

# What the probe found at the name.
NONE, OURS, FOREIGN = "none", "ours", "foreign"


def server_name(config_dir: Path) -> str:
    try:
        user = getpass.getuser()
    except Exception:
        user = str(os.getuid()) if hasattr(os, "getuid") else ""
    digest = hashlib.sha256(f"{user}\0{Path(config_dir).resolve()}".encode()).hexdigest()[:20]
    name = f"{PRODUCT_NAME.lower()}-{digest}"
    if sys.platform.startswith("linux"):
        runtime = os.environ.get("XDG_RUNTIME_DIR", "")
        if runtime and _private_dir(Path(runtime)):
            return str(Path(runtime) / name)
        # Not /tmp, where any user can create the name first: the profile's
        # own 0700 config folder, when the path fits in a socket address.
        own = Path(config_dir).resolve() / SOCKET_FILE
        if len(os.fsencode(str(own))) <= MAX_SOCKET_PATH and _private_dir(own.parent):
            return str(own)
    return name


def _private_dir(p: Path) -> bool:
    try:
        st = p.stat()
    except OSError:
        return False
    return p.is_dir() and st.st_uid == os.getuid() and (st.st_mode & 0o077) == 0


def socket_path(name: str) -> str:
    """Where Qt puts a local socket named `name` on Linux and macOS: the name
    itself when it is a path, else in the temporary folder."""
    if os.path.isabs(name):
        return name
    return os.path.join(_qt_temp_dir(), name)


def _qt_temp_dir() -> str:
    """QDir::tempPath() (without needing Qt): $TMPDIR, else on macOS the
    per-user temporary folder (NSTemporaryDirectory), else /tmp."""
    tmp = os.environ.get("TMPDIR", "")
    if not tmp and sys.platform == "darwin":
        with contextlib.suppress(OSError, ValueError):
            tmp = os.confstr(65537) or ""  # _CS_DARWIN_USER_TEMP_DIR
    return os.path.normpath(tmp) if tmp else "/tmp"


def _my_uid() -> int:
    return os.getuid()


def _peer_uid(sock: socket.socket) -> int | None:
    """The user ID of the process at the other end of a Unix socket."""
    if hasattr(socket, "SO_PEERCRED"):  # Linux: struct ucred {pid, uid, gid}
        _pid, uid, _gid = struct.unpack("3i", sock.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12))
        return uid
    if sys.platform == "darwin":  # LOCAL_PEERCRED: struct xucred {version, uid, ...}
        sol_local, local_peercred = 0, 1
        raw = sock.getsockopt(sol_local, local_peercred, 76)
        return struct.unpack_from("I", raw, 4)[0]
    return None


def _posix_connect(name: str) -> tuple[str, socket.socket | None]:
    """(NONE | OURS | FOREIGN, a connected socket when OURS)."""
    path = socket_path(name)
    try:
        st = os.lstat(path)
    except OSError:
        return NONE, None
    if not stat.S_ISSOCK(st.st_mode) or st.st_uid != _my_uid():
        log.warning("single instance: the socket name is held by another user's file; running on our own")
        return FOREIGN, None
    sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    sock.settimeout(CONNECT_MS / 1000)
    try:
        sock.connect(path)
    except OSError:
        sock.close()
        return NONE, None  # left behind by a crashed instance
    try:
        uid = _peer_uid(sock)
    except OSError:
        uid = None
    if uid != _my_uid():
        sock.close()
        log.warning("single instance: the socket is served by another user's program; running on our own")
        return FOREIGN, None
    return OURS, sock


def _windows_connect(name: str):
    """(NONE | OURS | FOREIGN, an open pipe file when OURS)."""
    path = name if name.startswith("\\\\.\\pipe\\") else "\\\\.\\pipe\\" + name
    f = None
    for _ in range(2):
        try:
            f = open(path, "r+b", buffering=0)  # noqa: SIM115 (returned to the caller)
            break
        except FileNotFoundError:
            return NONE, None
        except OSError as e:
            if getattr(e, "winerror", None) == 231:  # ERROR_PIPE_BUSY: wait for a free instance
                _win.wait_named_pipe(path, CONNECT_MS)
                continue
            # The server's ACL keeps us out: Qt's user-only pipes let their
            # own user in, so this one is someone else's.
            log.warning("single instance: the pipe name is held by a program we can't open; running on our own")
            return FOREIGN, None
    if f is None:
        return NONE, None
    try:
        ours = _win.pipe_server_is_current_user(f)
    except OSError:
        ours = False
    if not ours:
        f.close()
        log.warning("single instance: the pipe is served by another user's program; running on our own")
        return FOREIGN, None
    return OURS, f


def _connect(name: str):
    if os.name == "nt":
        return _windows_connect(name)
    return _posix_connect(name)


def probe(name: str) -> str:
    """Who holds the single-instance name: NONE, OURS (a live instance of
    this user) or FOREIGN (anything else)."""
    state, conn = _connect(name)
    if conn is not None:
        conn.close()
    return state


def encode(link: str | None) -> bytes:
    return json.dumps({"link": link}).encode() + b"\n"


def decode(data: bytes) -> tuple[bool, str | None]:
    """(valid message, the invite link in it if any)."""
    if len(data) > MAX_MESSAGE:
        return False, None
    try:
        msg = json.loads(data.decode("utf-8"))
    except ValueError:
        return False, None
    if not isinstance(msg, dict) or set(msg) != {"link"}:
        return False, None
    link = msg["link"]
    if link is None:
        return True, None
    if not isinstance(link, str):
        return False, None
    link = codes.link_from_os(link)
    return link is not None, link


def hand_off(name: str, link: str | None) -> bool:
    """Give `link` (or just "show yourself") to the app already running on
    this profile. True when it took it, so this process should exit; False
    when nothing is running there, or the other end isn't this user's (then
    the link is not sent)."""
    _state, conn = _connect(name)
    if conn is None:
        return False
    try:
        if os.name == "nt":
            conn.write(encode(link))
            conn.flush()
        else:
            conn.sendall(encode(link))
            with contextlib.suppress(OSError):
                conn.shutdown(socket.SHUT_WR)
                # Wait (briefly) for the listener to close its end: it has read the line.
                deadline = time.monotonic() + CONNECT_MS / 1000
                while time.monotonic() < deadline and conn.recv(64):
                    pass
        return True
    except OSError:
        return False
    finally:
        conn.close()


class Listener:
    """The running app's end. Create on the Qt main thread after the
    QApplication; `on_open(link)` runs there for every valid message."""

    def __init__(self, name: str, on_open: Callable[[str | None], None]):
        from qtpy.QtNetwork import QLocalServer

        self.name = name
        self.on_open = on_open
        self.server = QLocalServer()
        self.server.setSocketOptions(QLocalServer.SocketOption.UserAccessOption)
        self.server.newConnection.connect(self._accept)
        self._clients: dict = {}

    def listen(self) -> bool:
        """Start listening. False when another live instance of this user
        holds the name (the caller hands its link to that one instead). A
        name left behind by a crashed instance is removed and taken over; one
        held by anyone else is left alone, and this instance runs without
        single-instance hand-off (True, not listening).

        Liveness is checked first: with user-only access Qt binds in a
        private folder and renames the socket into place, which would
        silently replace a live instance's socket."""
        from qtpy.QtNetwork import QLocalServer

        state = probe(self.name)
        if state == OURS:
            return False
        if state == FOREIGN:
            return True
        QLocalServer.removeServer(self.name)
        if self.server.listen(self.name):
            return True
        log.warning("single instance: could not listen (%s)", self.server.errorString())
        return True

    def close(self) -> None:
        self.server.close()

    def _accept(self) -> None:
        while self.server.hasPendingConnections():
            sock = self.server.nextPendingConnection()
            self._clients[sock] = bytearray()
            sock.readyRead.connect(lambda s=sock: self._read(s))
            sock.disconnected.connect(lambda s=sock: self._done(s))

    def _read(self, sock) -> None:
        buf = self._clients.get(sock)
        if buf is None:
            return
        buf += bytes(sock.readAll())
        if b"\n" in buf or len(buf) > MAX_MESSAGE:
            self._done(sock)

    def _done(self, sock) -> None:
        buf = self._clients.pop(sock, None)
        if buf is None:
            return
        with contextlib.suppress(RuntimeError):
            sock.disconnectFromServer()
            sock.deleteLater()
        line = bytes(buf).split(b"\n", 1)[0]
        valid, link = decode(line)
        if not valid:
            log.warning("single instance: ignored a malformed hand-off (%d bytes)", len(buf))
            return
        log.info("single instance: another launch handed over %s", "an invite link" if link else "a show request")
        self.on_open(link)


def create_application(argv: list[str]):
    """The QApplication, made before pywebview (which reuses it). On macOS a
    link opened while the app runs, or one that launched it, arrives as a
    QFileOpenEvent sent to the application object; `on_open_link` gets it
    (links arriving before it is set are kept until then). A Quit event (the
    macOS app menu or Dock, the session ending) first calls the quit handler,
    so the window closes for good rather than to the tray. Only events sent
    to the application itself pass through Python, unlike an app-wide event
    filter, which would see every event of every widget."""
    from qtpy.QtCore import QEvent
    from qtpy.QtWidgets import QApplication

    class Application(QApplication):
        def __init__(self, args: list[str]):
            super().__init__(args)
            self._on_open: Callable[[str | None], None] | None = None
            self._early: list[str] = []
            self._on_quit: Callable[[], None] | None = None

        def set_quit_handler(self, on_quit: Callable[[], None]) -> None:
            self._on_quit = on_quit

        def set_link_handler(self, on_open: Callable[[str | None], None]) -> None:
            self._on_open = on_open
            early, self._early = self._early, []
            for link in early:
                on_open(link)

        def event(self, e) -> bool:
            if e.type() == QEvent.Type.Quit and self._on_quit is not None:
                self._on_quit()
            if e.type() == QEvent.Type.FileOpen:
                link = codes.link_from_os(e.url().toString())
                if link and self._on_open is not None:
                    self._on_open(link)
                elif link:
                    self._early = [link]
                return True
            return super().event(e)

    return Application(argv)


class _Windows:
    """The Win32 calls the pipe owner check needs (ctypes; Windows only)."""

    PROCESS_QUERY_LIMITED_INFORMATION = 0x1000
    TOKEN_QUERY = 0x0008
    TOKEN_USER = 1

    def __init__(self):
        import ctypes
        from ctypes import wintypes

        self.ctypes, self.wt = ctypes, wintypes
        self.k32 = ctypes.WinDLL("kernel32", use_last_error=True)
        self.adv = ctypes.WinDLL("advapi32", use_last_error=True)
        self.k32.GetNamedPipeServerProcessId.argtypes = [wintypes.HANDLE, ctypes.POINTER(wintypes.ULONG)]
        self.k32.OpenProcess.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.DWORD]
        self.k32.OpenProcess.restype = wintypes.HANDLE
        self.k32.GetCurrentProcess.restype = wintypes.HANDLE
        self.k32.CloseHandle.argtypes = [wintypes.HANDLE]
        self.k32.WaitNamedPipeW.argtypes = [wintypes.LPCWSTR, wintypes.DWORD]
        self.adv.OpenProcessToken.argtypes = [wintypes.HANDLE, wintypes.DWORD, ctypes.POINTER(wintypes.HANDLE)]
        self.adv.GetTokenInformation.argtypes = [
            wintypes.HANDLE,
            ctypes.c_int,
            ctypes.c_void_p,
            wintypes.DWORD,
            ctypes.POINTER(wintypes.DWORD),
        ]
        self.adv.ConvertSidToStringSidW.argtypes = [ctypes.c_void_p, ctypes.POINTER(wintypes.LPWSTR)]
        self.k32.LocalFree.argtypes = [ctypes.c_void_p]

    def _fail(self, what: str) -> OSError:
        return OSError(self.ctypes.get_last_error(), f"{what} failed")

    def wait_named_pipe(self, path: str, ms: int) -> None:
        self.k32.WaitNamedPipeW(path, ms)

    def _token_sid(self, process) -> str:
        ctypes, wt = self.ctypes, self.wt
        token = wt.HANDLE()
        if not self.adv.OpenProcessToken(process, self.TOKEN_QUERY, ctypes.byref(token)):
            raise self._fail("OpenProcessToken")
        try:
            size = wt.DWORD()
            self.adv.GetTokenInformation(token, self.TOKEN_USER, None, 0, ctypes.byref(size))
            buf = ctypes.create_string_buffer(size.value)
            if not self.adv.GetTokenInformation(token, self.TOKEN_USER, buf, size, ctypes.byref(size)):
                raise self._fail("GetTokenInformation")
            # TOKEN_USER starts with SID_AND_ATTRIBUTES, whose first field is the SID pointer.
            sid = ctypes.cast(buf, ctypes.POINTER(ctypes.c_void_p))[0]
            text = wt.LPWSTR()
            if not self.adv.ConvertSidToStringSidW(sid, ctypes.byref(text)):
                raise self._fail("ConvertSidToStringSidW")
            try:
                return text.value
            finally:
                self.k32.LocalFree(text)
        finally:
            self.k32.CloseHandle(token)

    def current_sid(self) -> str:
        return self._token_sid(self.k32.GetCurrentProcess())

    def pipe_server_sid(self, f) -> str:
        import msvcrt

        ctypes, wt = self.ctypes, self.wt
        pid = wt.ULONG()
        if not self.k32.GetNamedPipeServerProcessId(msvcrt.get_osfhandle(f.fileno()), ctypes.byref(pid)):
            raise self._fail("GetNamedPipeServerProcessId")
        proc = self.k32.OpenProcess(self.PROCESS_QUERY_LIMITED_INFORMATION, False, pid.value)
        if not proc:
            raise self._fail("OpenProcess")
        try:
            return self._token_sid(proc)
        finally:
            self.k32.CloseHandle(proc)

    def pipe_server_is_current_user(self, f) -> bool:
        return self.pipe_server_sid(f) == self.current_sid()


_win = _Windows() if os.name == "nt" else None
