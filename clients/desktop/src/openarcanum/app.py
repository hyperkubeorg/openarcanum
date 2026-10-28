"""Window creation and process lifecycle.

pywebview set-up: the Qt backend, the UI loaded straight off disk through a
file:// URI (no loopback HTTP server), Ctrl+Shift+I opening a local DevTools
window via setDevToolsPage (never `debug=True`) only when started with
--debug or OPENARCANUM_DEVTOOLS=1, and the shortcut installed on the Qt main
thread through `create_window_trigger`. The window never leaves the app's
own page (harden_webview) and the page can call only JsApi's public methods
(api.install_bridge_guard). Every thread we start is a daemon and only the
bus sender calls evaluate_js, because evaluate_js waits on the Qt loop and a
non-daemon thread stuck there blocks exit forever; pywebview's own bridge-call
threads are made daemons too (daemon_bridge_threads).

    python -m openarcanum [--home DIR] [--debug] [--smoke] [--verbose] [--] [openarcanum://invite/...]

Every scheme registration passes the link after `--` (Windows substitutes
`%1` unescaped), so nothing in a link can be taken as an option.

An invite link argument (how the OS starts the app for a `openarcanum://` link)
prefills the Join dialog; if the profile is already open, the link goes to
that window instead (openarcanum/instance.py).

Where the desktop has a system tray, the app shows its icon there while it
runs (openarcanum/tray.py), and closing the window hides it there instead of
quitting (the `close_to_tray` preference). Quitting is then Quit OpenArcanum in
the tray menu, Ctrl+Q (Cmd+Q), the macOS app menu, or the system ending the
session (SIGTERM, logout); every one of them sets `Gui.quitting` and
closes the window, so run() saves and exits exactly as when the window
closes without a tray. While communities hosted on this computer run
(openarcanum/hosting.py), the deliberate ways to quit (the tray menu, Ctrl+Q,
closing the window without a tray) first bring the window up and ask, since
those communities go offline; the system ending the session doesn't ask.
"""

from __future__ import annotations

import argparse
import contextlib
import logging
import logging.handlers
import os
import signal
import subprocess
import sys
import threading
import time
from pathlib import Path

from openarcanum import PRODUCT_NAME, __version__, codes, crashlog, instance, xerrors
from openarcanum import notify as notify_mod
from openarcanum import settings as settings_mod
from openarcanum import tray as tray_mod
from openarcanum.api import GuiHooks, JsApi, LoopThread, install_bridge_guard
from openarcanum.bus import Bus
from openarcanum.i18n import _
from openarcanum.paths import ENGINE_SANDBOX, UI_DIR, Paths, ensure_dir
from openarcanum.updates import launch as launch_update

FRONTEND_INDEX = UI_DIR / "index.html"
SMOKE_TIMEOUT = 90

log = logging.getLogger("openarcanum")


def parse_args(argv: list[str]) -> argparse.Namespace:
    p = argparse.ArgumentParser(prog="openarcanum", description=f"{PRODUCT_NAME} desktop client")
    p.add_argument("--home", type=Path, help="keep config, data, cache and logs under DIR")
    p.add_argument("--debug", action="store_true", help="open DevTools when the window loads")
    p.add_argument(
        "--allow-remote-debugging",
        action="store_true",
        help="keep a DevTools port from the environment (QTWEBENGINE_REMOTE_DEBUGGING, --remote-debugging-port): "
        "any program on this computer can then drive the app; for automated UI tests and development only",
    )
    p.add_argument("--smoke", action="store_true", help="open the window, load the UI, exit 0")
    p.add_argument("--verbose", action="store_true", help="debug logging")
    p.add_argument(
        "--maintainer",
        action="append",
        default=[],
        metavar="URL",
        help="use this maintainer node (repeatable) when the profile names none, e.g. a self-hosted network",
    )
    p.add_argument(
        "link",
        nargs="?",
        metavar="openarcanum://invite/...",
        help="open the Join dialog with this invite link filled in (you still confirm the join)",
    )
    return p.parse_args(argv)


def restart_args(args: argparse.Namespace) -> list[str]:
    """How the new version starts after an update: with the same profile and
    options, but never the invite link this launch was opened with (it
    would open the Join dialog again) or one-off modes (--smoke, --debug)."""
    out: list[str] = []
    if args.home:
        out += ["--home", str(args.home.expanduser().resolve())]
    for url in args.maintainer:
        out += ["--maintainer", url]
    if args.verbose:
        out.append("--verbose")
    return out


def isolate_toolkit_dirs(home: Path) -> None:
    """With --home, point the toolkits' own caches (Qt, fontconfig, Mesa) into
    DIR too, so a run writes nothing outside it."""
    tk = home / ".toolkit"
    for var, sub in (
        ("XDG_CONFIG_HOME", "config"),
        ("XDG_DATA_HOME", "data"),
        ("XDG_CACHE_HOME", "cache"),
        ("XDG_STATE_HOME", "state"),
    ):
        d = tk / sub
        ensure_dir(d)
        os.environ[var] = str(d)
    os.environ["MESA_SHADER_CACHE_DIR"] = str(tk / "cache" / "mesa")


def setup_logging(paths: Paths, verbose: bool) -> None:
    """Logs carry IDs and sizes only: never message content, keys or passphrases."""
    ensure_dir(paths.log)
    handler = logging.handlers.RotatingFileHandler(paths.log_file, maxBytes=1 << 20, backupCount=3, encoding="utf-8")
    handler.setFormatter(crashlog.SafeFormatter("%(asctime)s %(levelname)s %(name)s: %(message)s"))
    root = logging.getLogger()
    root.setLevel(logging.DEBUG if verbose else logging.INFO)
    root.addHandler(handler)
    if verbose:
        err = logging.StreamHandler(sys.stderr)
        err.setFormatter(crashlog.SafeFormatter("%(levelname)s %(name)s: %(message)s"))
        root.addHandler(err)
    for noisy in ("websockets", "asyncio"):
        logging.getLogger(noisy).setLevel(logging.WARNING)
    if paths.log_file.exists():
        os.chmod(paths.log_file, 0o600)


class Gui(GuiHooks):
    """Everything that must run on the Qt main thread goes through
    BrowserView.create_window_trigger, which queues a callable onto it."""

    def __init__(self, window, bus: Bus, smoke: bool, debug: bool, devtools: bool = False):
        self.window = window
        self.bus = bus
        self.smoke = smoke
        self.debug = debug
        # DevTools only with --debug or OPENARCANUM_DEVTOOLS=1 (devtools_allowed).
        self.devtools = devtools or debug
        # File dialogs answer themselves (the UI tests): never when frozen.
        self.answer_dialogs = answer_dialogs_for_tests()
        self.smoke_ok = threading.Event()
        self._devtools_view = None
        self._shortcut = None
        self._media_allowed = False
        self._media_policy = False
        self._screen_wait = None  # (nested loop, [choice]) while the picker is open
        self._notifier = notify_mod.make_notifier()
        self.sandbox_file: Path | None = None
        self._link_lock = threading.Lock()
        self._link: str | None = None
        # The system tray (start_tray), and whether closing the window is
        # quitting (quit) rather than hiding it there.
        self.tray: tray_mod.Tray | None = None
        self.tray_available = False
        self.quitting = False
        # Quitting was confirmed although communities hosted on this
        # computer then go offline (confirm_quit).
        self.quit_confirmed = False
        self.core = None
        self.loop: LoopThread | None = None
        self._quit_shortcut = None

    # -- main-thread plumbing

    def _browser_view(self):
        from webview.platforms.qt import BrowserView

        if self.window is None:
            return None
        return BrowserView.instances.get(self.window.uid)

    def on_main(self, fn) -> bool:
        bv = self._browser_view()
        if bv is None:
            return False
        bv.create_window_trigger.emit(fn)
        return True

    # -- GuiHooks

    def ui_loaded(self) -> None:
        if self.sandbox_file is not None:
            sandbox_started(self.sandbox_file, __version__)
        self.on_main(self._install_shortcut)
        self.on_main(self._install_media_policy)
        self.on_main(self._install_terminate)

    def _install_terminate(self) -> None:
        """A terminate request (logout, system shutdown, kill) closes the
        window the normal way, so run() saves everything before leaving.
        Installed once the engine is up: it sets its own SIGTERM handling
        while starting. (pywebview runs Python periodically under Qt's loop,
        so the handler gets to run; Ctrl+C is pywebview's own handler, which
        ends the loop the same way.)"""
        signal.signal(signal.SIGTERM, lambda *_args: self._quit_now())
        if self.debug:
            self.open_devtools()

    def _install_shortcut(self) -> None:
        if self._shortcut is not None:
            return
        from qtpy.QtCore import Qt
        from qtpy.QtGui import QKeySequence

        try:
            from qtpy.QtGui import QShortcut
        except ImportError:  # Qt5 layout
            from qtpy.QtWidgets import QShortcut
        bv = self._browser_view()
        if bv is None:
            return
        if self.devtools:
            sc = QShortcut(QKeySequence("Ctrl+Shift+I"), bv)
            sc.setContext(Qt.ApplicationShortcut)
            sc.activated.connect(self._attach_devtools)
            self._shortcut = sc
        else:
            self._shortcut = False
        # Quit (Cmd+Q on macOS, where Qt's Ctrl is the Command key): with the
        # tray in use, closing the window doesn't.
        q = QShortcut(QKeySequence("Ctrl+Q"), bv)
        q.setContext(Qt.ApplicationShortcut)
        q.activated.connect(self._quit_asked)
        self._quit_shortcut = q

    # -- media permissions and the screen picker

    def media_access(self, allowed: bool) -> None:
        self._media_allowed = allowed

    def _install_media_policy(self) -> None:
        """The page may capture media only while in a call (or
        testing devices), and only this app's own page; nothing else (location,
        notifications, clipboard, ...) is ever granted. Screens and windows are
        chosen in the app's own picker."""
        if self._media_policy:
            return
        bv = self._browser_view()
        if bv is None:
            return
        page = bv.webview.page()
        try:
            from qtpy.QtWebEngineCore import QWebEnginePermission
        except ImportError:
            log.warning("this Qt has no permission API; calls can't use the microphone")
            return
        # pywebview's own handler grants the microphone and camera to any
        # page at any time; this one replaces it.
        with contextlib.suppress(TypeError, RuntimeError):
            page.featurePermissionRequested.disconnect()
        media = {
            QWebEnginePermission.PermissionType.MediaAudioCapture,
            QWebEnginePermission.PermissionType.MediaVideoCapture,
            QWebEnginePermission.PermissionType.MediaAudioVideoCapture,
            QWebEnginePermission.PermissionType.DesktopVideoCapture,
            QWebEnginePermission.PermissionType.DesktopAudioVideoCapture,
        }

        def on_permission(p) -> None:
            ours = p.origin().scheme() == "file"
            granted = ours and self._media_allowed and p.permissionType() in media
            log.info("permission %s: %s", p.permissionType().name, "granted" if granted else "refused")
            if granted:
                p.grant()
            else:
                p.deny()

        # Decide every request here: Qt would otherwise remember an answer
        # (a refusal before a call would stick for the session).
        with contextlib.suppress(AttributeError):
            from qtpy.QtWebEngineCore import QWebEngineProfile

            page.profile().setPersistentPermissionsPolicy(QWebEngineProfile.PersistentPermissionsPolicy.AskEveryTime)
        page.permissionRequested.connect(on_permission)
        page.desktopMediaRequested.connect(self._on_screen_request)
        # Screen capture on X11 can hit a window it can't read; Xlib's
        # default would exit the app over it (xerrors.py).
        xerrors.ignore_x_errors()
        self._media_policy = True
        log.info("media permission policy installed")

    def _on_screen_request(self, request) -> None:
        """The page asked to share a screen or window. The engine needs the
        answer before this handler returns, so it waits here (in a nested
        event loop, like a modal dialog) while the choice is made in the
        app's own picker in the page."""
        from qtpy.QtCore import QEventLoop, Qt, QTimer

        log.info("screen share: picker requested")
        if not self._media_allowed or self._screen_wait is not None:
            request.cancel()
            return
        screens, windows = request.screensModel(), request.windowsModel()

        def names(model) -> list[str]:
            return [str(model.data(model.index(i, 0), Qt.DisplayRole) or "") for i in range(model.rowCount())]

        def offer() -> None:
            self.bus.push("screen_pick", {"screens": names(screens), "windows": names(windows)})

        # Windows appear in the list as the engine finds them.
        for model in (screens, windows):
            model.rowsInserted.connect(lambda *_args: offer())
            model.modelReset.connect(offer)
        loop = QEventLoop()
        self._screen_wait = (loop, [None])
        QTimer.singleShot(SCREEN_PICK_SECONDS * 1000, loop.quit)
        offer()
        loop.exec()
        choice = self._screen_wait[1][0]
        self._screen_wait = None
        kind, index = choice if choice else ("", -1)
        if kind == "screen" and 0 <= index < screens.rowCount():
            log.info("screen share: a screen was chosen")
            request.selectScreen(screens.index(index, 0))
        elif kind == "window" and 0 <= index < windows.rowCount():
            log.info("screen share: a window was chosen")
            request.selectWindow(windows.index(index, 0))
        else:
            request.cancel()
            self.bus.push("screen_pick", {"closed": True})

    def screen_pick(self, kind: str, index: int) -> None:
        def do() -> None:
            if self._screen_wait is not None:
                loop, out = self._screen_wait
                out[0] = (kind, index)
                loop.quit()

        self.on_main(do)

    def open_devtools(self) -> None:
        if not self.devtools:
            log.info("developer tools are off (start with --debug or %s=1)", DEVTOOLS_ENV)
            return
        self.on_main(self._attach_devtools)

    def _attach_devtools(self) -> None:
        if not self.devtools:
            return
        # Local Chromium DevTools in a sibling window, wired with
        # setDevToolsPage (never debug=True, whose remote-debugging inspector
        # loads from a hosted page). The DevTools page
        # uses an off-the-record profile so it doesn't store anything either.
        from qtpy.QtWebEngineWidgets import QWebEngineView

        try:
            from qtpy.QtWebEngineCore import QWebEnginePage, QWebEngineProfile
        except ImportError:
            from qtpy.QtWebEngineWidgets import QWebEnginePage, QWebEngineProfile
        bv = self._browser_view()
        if bv is None:
            return
        if self._devtools_view is not None:
            self._devtools_view.show()
            self._devtools_view.raise_()
            self._devtools_view.activateWindow()
            return
        view = QWebEngineView()
        profile = QWebEngineProfile(view)  # nameless = off the record
        page = QWebEnginePage(profile, view)
        view.setPage(page)
        view.setWindowTitle(f"DevTools - {self.window.title}")
        view.resize(1000, 700)
        bv.webview.page().setDevToolsPage(page)
        view.show()
        log.info("developer tools opened")
        self._devtools_view = view  # keep a reference so it isn't collected

    def copy_text(self, text: str) -> bool:
        """Put text on the clipboard from the Qt main thread (the only one
        that may touch it). False when there is no window to run it on."""

        def do():
            from qtpy.QtWidgets import QApplication

            try:
                QApplication.clipboard().setText(text)
            except Exception as e:
                log.warning("clipboard: couldn't set the text (%s)", type(e).__name__)

        return self.on_main(do)

    def open_folder(self, path: str) -> None:
        """Show one of the app's folders in the system's file manager, where
        the desktop has one; says so in the page when it can't."""

        def do():
            from qtpy.QtCore import QUrl
            from qtpy.QtGui import QDesktopServices

            if not QDesktopServices.openUrl(QUrl.fromLocalFile(path)):
                self.bus.push(
                    "toast",
                    {
                        "text": _("This system has no file manager OpenArcanum can open. The path is: {path}").format(
                            path=path
                        ),
                        "error": True,
                    },
                )

        self.on_main(do)

    def open_file_dialog(
        self,
        request_id: str,
        kind: str,
        save: bool,
        default_name: str = "",
        start: str = "",
        grant=None,
    ) -> None:
        """A native file dialog, opened on `start` (a typed path) or, for
        saving, on the save folder with `default_name` made safe. What is
        picked is granted (`grant`, api.PickedPaths) before the page hears
        about it. The dialog blocks until it's answered, so it runs on a
        daemon thread, never a bridge thread; the answer arrives as an
        event."""
        from openarcanum.dmfiles import safe_name
        from openarcanum.paths import save_folder

        suggested = safe_name(default_name) if default_name else ""

        def worker():
            import webview

            types = {
                "image": (_("Images ({patterns})").format(patterns="*.png;*.jpg;*.jpeg;*.gif;*.webp"),),
                "backup": (
                    _("OpenArcanum backups ({patterns})").format(patterns="*.fbak"),
                    _("All files ({patterns})").format(patterns="*.*"),
                ),
            }.get(kind, (_("All files ({patterns})").format(patterns="*.*"),))
            try:
                if self.answer_dialogs:
                    # Tests (OPENARCANUM_TEST_FILE_DIALOGS=accept, never in a
                    # release build): accept what the dialog opened on.
                    res = [start] if start else ([str(save_folder() / suggested)] if save and suggested else None)
                elif kind == "folder":
                    res = self.window.create_file_dialog(webview.FOLDER_DIALOG, directory=start)
                elif kind == "files":
                    res = self.window.create_file_dialog(webview.OPEN_DIALOG, directory=start, allow_multiple=True)
                elif save:
                    target = start or (str(save_folder() / suggested) if suggested else "")
                    res = self.window.create_file_dialog(webview.SAVE_DIALOG, save_filename=target)
                else:
                    res = self.window.create_file_dialog(webview.OPEN_DIALOG, directory=start, file_types=types)
            except Exception:
                log.exception("file dialog failed")
                res = None
            paths = [str(p) for p in res] if isinstance(res, list | tuple) else ([str(res)] if res else [])
            paths = [p for p in paths if p]
            if grant is not None:
                grant(paths)
            self.bus.push("dialog", {"id": request_id, "path": paths[0] if paths else "", "paths": paths})

        threading.Thread(target=worker, daemon=True, name="file-dialog").start()

    def notify(self, title: str, body: str) -> None:
        if self._notifier is not None:
            # Linux: the desktop's notification service (works without a tray).
            self._notifier.send(title, body)
            return

        def do():
            # Windows and macOS: a message from the tray icon (a toast; the
            # Notification Center).
            if self.tray is not None:
                self.tray.message(title, body)

        self.on_main(do)

    # -- the system tray (openarcanum/tray.py)

    def start_tray(self, core, loop: LoopThread) -> None:
        """Show the tray icon where the desktop has a tray. Qt main thread,
        before the window opens."""
        from qtpy.QtWidgets import QSystemTrayIcon

        self.core, self.loop = core, loop
        self.tray_available = QSystemTrayIcon.isSystemTrayAvailable()
        if not self.tray_available:
            log.info("tray: this desktop has no system tray; closing the window quits")
            return
        self.tray = tray_mod.Tray(
            tray_mod.TrayActions(
                show_window=self._raise_window,
                set_muted=self._set_muted,
                lock=self._lock,
                quit=self._quit_asked,
                profile=core.profile_state,
                muted=lambda: not core.settings.get("notifications", True),
            )
        )
        self.tray.show()
        log.info("tray: icon shown")

    def tray_status(self) -> dict:
        if self.tray_available:
            return {"available": True, "reason": ""}
        return {
            "available": False,
            "reason": _(
                "This desktop has no system tray (GNOME and many Wayland desktops don't, unless a tray "
                "extension such as AppIndicator is installed), so closing the window quits OpenArcanum."
            ),
        }

    def keeps_running(self) -> bool:
        """Whether closing the window leaves the app running in the tray."""
        from qtpy.QtWidgets import QSystemTrayIcon

        return (
            self.tray is not None
            and self.tray.visible()
            and QSystemTrayIcon.isSystemTrayAvailable()
            and self.core is not None
            and bool(self.core.prefs.values.get("close_to_tray", True))
        )

    def on_closing(self):
        """The window is closing (pywebview's closing event, on the Qt main
        thread). Returns False to keep it open: with the tray in use and
        OpenArcanum not quitting, it hides there instead, and the first time
        a notification says how to quit."""
        if self.quitting or not self.keeps_running():
            if not self.quitting and not self._may_quit():
                return False
            self.quitting = True
            return None
        bv = self._browser_view()
        if bv is None:
            return None
        bv.hide()
        log.info("tray: window hidden")
        prefs = self.core.prefs
        if "tray_hint_shown" not in prefs.shown:
            self.notify(*tray_mod.hidden_hint())
            try:
                prefs.mark_shown("tray_hint_shown")
            except OSError:
                log.exception("saving preferences")
        return False

    def attention(self, mentions: int, direct: int, review: int) -> None:
        a = tray_mod.Attention.clean(mentions, direct, review)
        if self.tray is not None:
            self.on_main(lambda: self.tray.set_attention(a))

    def profile_changed(self) -> None:
        """The profile was opened, locked or created: the tray's menu and
        tooltip follow, and a locked profile has nothing to count."""
        if self.tray is None:
            return

        def do():
            if self.core.profile_state() != "unlocked":
                self.tray.attention = tray_mod.Attention()
            self.tray.refresh()

        self.on_main(do)

    def _set_muted(self, muted: bool) -> None:
        """Mute notifications (tray menu): Settings → Notifications →
        Desktop notifications, off or on."""
        if self.core is None or self.loop is None:
            return
        refresh = (lambda: self.on_main(self.tray.refresh)) if self.tray is not None else None
        self.loop.submit(self.core.set_setting("notifications", not muted), done=refresh)

    def _lock(self) -> None:
        if self.core is not None and self.loop is not None:
            self.loop.submit(self.core.lock())

    # -- invite links the OS opened (openarcanum/instance.py)

    def offer_link(self, link: str | None) -> None:
        """An invite link to prefill the Join dialog with (None: just show the
        window). The page picks it up with take_invite_link once unlocked."""
        if link:
            with self._link_lock:
                self._link = link
            self.bus.push("invite_link", {})
        self.on_main(self._raise_window)

    def take_invite_link(self) -> str | None:
        with self._link_lock:
            link, self._link = self._link, None
        return link

    def _raise_window(self) -> None:
        bv = self._browser_view()
        if bv is None:
            return
        if not bv.isVisible():
            bv.show()
        if bv.isMinimized():
            bv.showNormal()
        bv.raise_()
        bv.activateWindow()

    def quit(self) -> None:
        """Close the window for good (never to the tray); run() then shuts
        down as when the window is closed."""
        self.quitting = True
        if self.window is not None:
            threading.Thread(target=self.window.destroy, daemon=True, name="quit").start()

    def _quit_now(self) -> None:
        """Quit from the Qt main thread (a terminate signal, or once quitting
        was confirmed)."""
        self.quitting = True
        if self.window is not None:
            self.window.destroy()

    def _quit_asked(self) -> None:
        """Quit from the tray menu or Ctrl+Q: first warn when
        communities hosted on this computer would go offline."""
        if self._may_quit():
            self._quit_now()

    def _may_quit(self) -> bool:
        """Whether quitting can go ahead now. With hosted community servers
        running, the window comes up and the page asks first (Quit anyway
        calls confirm_quit); a terminate signal or the system quitting
        never asks."""
        running = self.core.hosting_running() if self.core is not None else 0
        if self.smoke or self.quit_confirmed or running == 0:
            return True
        self._raise_window()
        self.bus.push("confirm_quit", {"count": running})
        return False

    def confirm_quit(self) -> None:
        """The page's "Quit anyway" (hosted communities go offline)."""
        self.quit_confirmed = True
        self.quit()

    def smoke_ready(self, info: dict) -> None:
        log.info("smoke: UI loaded (%s)", ", ".join(f"{k}={v}" for k, v in sorted(info.items())))
        self.smoke_ok.set()


def main(argv: list[str] | None = None) -> None:
    argv = sys.argv[1:] if argv is None else argv
    if argv == [USERNS_PROBE]:
        probe_user_namespaces()
    args = parse_args(argv)
    if args.home:
        home = args.home.expanduser().resolve()
        ensure_dir(home)
        isolate_toolkit_dirs(home)
        paths = Paths.under(home)
    else:
        paths = Paths.default()
    paths.ensure()
    setup_logging(paths, args.verbose)
    link = codes.link_from_os(args.link) if args.link else None
    # A second launch on an open profile hands its link to the running app
    # and leaves before touching anything else (a smoke run always opens its
    # own window). Logging is up first: what the hand-off refuses is logged.
    if not args.smoke and instance.hand_off(instance.server_name(paths.config), link):
        sys.exit(0)
    # Only now: a launch that handed its link over must not touch the
    # running app's crash marker.
    crashlog.install(paths.log)
    if args.link and not link:
        log.warning("ignored a launch argument that is not an invite link")
    try:
        settings_mod.set_launch_maintainers(args.maintainer)
    except settings_mod.SettingError as e:
        sys.exit(f"--maintainer: {e}")
    log.info("%s %s starting", PRODUCT_NAME, __version__)
    sys.exit(run(paths, args))


# How long the screen picker waits for a choice.
SCREEN_PICK_SECONDS = 120
# Chromium flags pywebview adds that weaken the app (docs/threat-model.md,
# "Webview"): --use-fake-ui-for-media-stream grants the microphone and camera
# and picks a screen to share without asking, which would bypass the app's
# own permission policy and screen picker; --no-sandbox (added on some Linux
# distributions) turns off the renderer sandbox.
UNSAFE_ENGINE_FLAGS = ("--use-fake-ui-for-media-stream", "--no-sandbox")
# Flags (and their `=value` forms) that open the page to other programs or
# lift the page's origin rules; dropped unless --debug or
# --allow-remote-debugging is given. A DevTools port on 127.0.0.1 lets any
# local user attach to the unlocked page and call the bridge.
DEBUG_ENGINE_FLAGS = (
    "--remote-debugging-port",
    "--remote-debugging-pipe",
    "--remote-debugging-address",
    "--remote-allow-origins",
    "--disable-web-security",
    "--allow-file-access-from-files",
    "--allow-universal-access-from-files",
    "--user-data-dir",
)
REMOTE_DEBUGGING_ENV = "QTWEBENGINE_REMOTE_DEBUGGING"
ENGINE_FLAGS = "QTWEBENGINE_CHROMIUM_FLAGS"
# A bundled app has no `python -c`: it runs itself with this argument to probe
# for user namespaces.
USERNS_PROBE = "--probe-user-namespaces"


def probe_user_namespaces() -> None:
    os.unshare(os.CLONE_NEWUSER)
    sys.exit(0)


def linux_sandbox_available() -> bool:
    """Whether Chromium's Linux sandbox can start: it needs unprivileged
    user namespaces, which some systems turn off (or, like Ubuntu 24.04,
    restrict with AppArmor unless the app's profile is installed)."""
    if not sys.platform.startswith("linux"):
        return True

    def read(path: str) -> str:
        try:
            return Path(path).read_text().strip()
        except OSError:
            return ""

    if read("/proc/sys/kernel/unprivileged_userns_clone") == "0" or read("/proc/sys/user/max_user_namespaces") == "0":
        return False
    if read("/proc/sys/kernel/apparmor_restrict_unprivileged_userns") == "1":
        return Path("/etc/apparmor.d/openarcanum").exists()
    # In a child process (forking a threaded process isn't safe).
    if getattr(sys, "frozen", False):
        probe = [sys.executable, USERNS_PROBE]
    else:
        probe = [sys.executable, "-I", "-c", "import os; os.unshare(os.CLONE_NEWUSER)"]
    try:
        r = subprocess.run(probe, capture_output=True, timeout=10)
    except OSError, subprocess.TimeoutExpired:
        return False
    return r.returncode == 0


SANDBOX_STATE = ENGINE_SANDBOX
# The probe record starts with its format version: "1 <state> <app
# version>". Records from before it had one
# ("<state> <app version>") are read the same way; anything else counts as
# no record, so the probe simply runs again.
SANDBOX_FORMAT = "1"
_SANDBOX_STATES = ("trying", "failed", "ok")


def _read_sandbox(state_file: Path) -> list[str]:
    try:
        words = state_file.read_text().split()
    except OSError:
        return []
    if words[:1] == [SANDBOX_FORMAT]:
        words = words[1:]
    return words if words[:1] and words[0] in _SANDBOX_STATES else []


def _write_sandbox(state_file: Path, state: str, version: str) -> None:
    state_file.write_text(f"{SANDBOX_FORMAT} {state} {version}\n")


def decide_sandbox(state_file: Path, version: str) -> tuple[bool, str]:
    """Whether to run the engine's sandbox, and if not, why. A sandboxed start
    that never reached the UI (the engine aborts where the system can't run
    its sandbox, even when user namespaces work) turns it off for this
    version; an update tries again. QTWEBENGINE_DISABLE_SANDBOX=1 in the
    environment is the user's own choice."""
    if os.environ.get("QTWEBENGINE_DISABLE_SANDBOX") == "1":
        return False, "turned off in the environment (QTWEBENGINE_DISABLE_SANDBOX=1)"
    prev = _read_sandbox(state_file)
    if prev[:2] in (["trying", version], ["failed", version]):
        _write_sandbox(state_file, "failed", version)
        return False, "the browser engine couldn't start with its sandbox on this system"
    if not linux_sandbox_available():
        return False, "this system doesn't allow the sandbox (user namespaces)"
    _write_sandbox(state_file, "trying", version)
    return True, ""


def sandbox_started(state_file: Path, version: str) -> None:
    """The UI loaded: the sandbox works here."""
    try:
        if _read_sandbox(state_file)[:1] == ["trying"]:
            _write_sandbox(state_file, "ok", version)
    except OSError:
        pass


def _flag_name(flag: str) -> str:
    return flag.split("=", 1)[0]


def harden_engine_flags(sandbox: bool, allow_debugging: bool = False) -> None:
    """Remove the unsafe flags from the engine's flags and keep pywebview from
    adding them; without a usable sandbox, run without it (logged). Unless
    debugging is allowed (--debug, --allow-remote-debugging), DevTools ports
    and origin-rule switches from the environment are dropped too (logged
    by name)."""
    import webview.platforms.qt as qt_platform

    blocked = set(UNSAFE_ENGINE_FLAGS) if sandbox else {"--use-fake-ui-for-media-stream"}
    if not allow_debugging:
        blocked |= set(DEBUG_ENGINE_FLAGS)
        if os.environ.pop(REMOTE_DEBUGGING_ENV, None) is not None:
            log.warning(
                "ignored %s from the environment (start with --allow-remote-debugging to keep it)", REMOTE_DEBUGGING_ENV
            )
    flags = os.environ.get(ENGINE_FLAGS, "").split()
    dropped = sorted({_flag_name(f) for f in flags if _flag_name(f) in blocked} - set(UNSAFE_ENGINE_FLAGS))
    if dropped:
        log.warning("ignored engine flags from the environment: %s", " ".join(dropped))
    flags = [f for f in flags if _flag_name(f) not in blocked]
    if not sandbox:
        # Qt's own switch (it also stops Qt preparing the sandbox), and the
        # engine flag.
        os.environ["QTWEBENGINE_DISABLE_SANDBOX"] = "1"
        if "--no-sandbox" not in flags:
            flags.append("--no-sandbox")
    if flags:
        os.environ[ENGINE_FLAGS] = " ".join(flags)
    else:
        os.environ.pop(ENGINE_FLAGS, None)
    original = qt_platform.environ_append

    def append(var: str, *values: str) -> None:
        keep = [v for v in values if not (var == ENGINE_FLAGS and _flag_name(v) in blocked)]
        if keep:
            original(var, *keep)

    qt_platform.environ_append = append


class _DaemonThread(threading.Thread):
    def __init__(self, *args, **kwargs) -> None:
        kwargs["daemon"] = True
        super().__init__(*args, **kwargs)


def daemon_bridge_threads() -> None:
    """Make the threads pywebview runs bridge calls on daemons.

    pywebview hands each call's result back to the page with evaluate_js,
    which waits for the Qt main loop. A call that finishes after the window
    is gone (one that closed it, like "Restart and install", or any call in
    flight at exit) waits forever, and as a non-daemon thread it would keep
    the process from exiting: CPython joins non-daemon threads at shutdown.
    By the time those threads are all that's left, run() has saved
    everything and closed the core, so nothing they hold needs to finish."""
    import webview.util

    webview.util.Thread = _DaemonThread  # type: ignore[attr-defined]


DEVTOOLS_ENV = "OPENARCANUM_DEVTOOLS"


def devtools_allowed(debug: bool, env: dict | None = None) -> bool:
    """DevTools (Ctrl+Shift+I) only when asked for: started with --debug, or
    OPENARCANUM_DEVTOOLS=1 in the environment. Off otherwise, so script in the
    page can't open them and nobody can be talked into pasting code there
    without first restarting the app with a flag."""
    env = os.environ if env is None else env
    return bool(debug) or env.get(DEVTOOLS_ENV, "") == "1"


TEST_DIALOGS_ENV = "OPENARCANUM_TEST_FILE_DIALOGS"


def answer_dialogs_for_tests(env: dict | None = None) -> bool:
    """The UI tests can't drive native file dialogs: with
    OPENARCANUM_TEST_FILE_DIALOGS=accept, each dialog is answered with the path
    it would open on (the path typed in the page). Ignored in a release
    (frozen) build."""
    env = os.environ if env is None else env
    return env.get(TEST_DIALOGS_ENV, "") == "accept" and not getattr(sys, "frozen", False)


def is_app_page(url, index_url: str) -> bool:
    """Whether a QUrl is the app's own page (its file:// index, any openarcanum)."""
    from qtpy.QtCore import QUrl

    ours = QUrl(index_url)
    return (
        url.isLocalFile()
        and not url.hasQuery()
        and url.host() == ours.host()
        and url.toLocalFile() == ours.toLocalFile()
    )


class _DropBlocker:
    """Eats drag-and-drop events for the app's window before the engine sees
    them: a file or link dropped on the page would otherwise navigate it."""

    def __init__(self):
        from qtpy.QtCore import QEvent, QObject

        types = {QEvent.Type.DragEnter, QEvent.Type.DragMove, QEvent.Type.DragLeave, QEvent.Type.Drop}
        self.blocked = 0

        blocker = self

        class Filter(QObject):
            def eventFilter(self, obj, event) -> bool:
                if event.type() in types:
                    if event.type() != QEvent.Type.DragLeave:
                        event.ignore()
                    blocker.blocked += 1
                    return True
                return False

        self.filter = Filter()

    def install(self, widget) -> None:
        """On a widget and every child it has now or gets later (the engine's
        render widget comes and goes)."""
        from qtpy.QtCore import QEvent, QObject

        widget.setAcceptDrops(False)
        widget.installEventFilter(self.filter)
        for child in widget.findChildren(QObject):
            if hasattr(child, "setAcceptDrops"):
                child.setAcceptDrops(False)
            child.installEventFilter(self.filter)

        blocker = self

        class ChildWatch(QObject):
            def eventFilter(self, obj, event) -> bool:
                if event.type() == QEvent.Type.ChildAdded:
                    child = event.child()
                    if hasattr(child, "setAcceptDrops"):
                        child.setAcceptDrops(False)
                    child.installEventFilter(blocker.filter)
                    child.installEventFilter(self)
                return False

        self._watch = ChildWatch()
        widget.installEventFilter(self._watch)
        for child in widget.findChildren(QObject):
            child.installEventFilter(self._watch)


def harden_webview(index_url: str) -> None:
    """Keep the window on the app's own page (docs/threat-model.md, "The page
    and the bridge"). pywebview's Qt backend accepts every navigation, opens
    new windows in the system browser, injects the bridge into whatever page
    finishes loading and lets drops through; its page and view classes are
    replaced before the window exists so that:

    - every navigation except to the app's own file:// index is refused
      (links, script, form posts, drops; frames included);
    - nothing opens a new window or the system browser (links open only
      through the page's own link dialog, which copies them);
    - the bridge is injected only into the app's own page;
    - drag-and-drop never reaches the engine;
    - nothing is downloaded;
    - no local file outside the page's own folder is read: the page loads
      its sibling scripts, styles and images (QtWebEngine needs
      LocalContentCanAccessFileUrls for those), and every other file://
      request (an image, a fetch) is refused by the request interceptor;
    - WebRTC (calls) offers only the default route's address, not every
      interface's (WebRTCPublicInterfacesOnly)."""
    import webview
    import webview.platforms.qt as qt
    from qtpy.QtCore import QUrl

    webview.settings["OPEN_EXTERNAL_LINKS_IN_BROWSER"] = False
    webview.settings["ALLOW_DOWNLOADS"] = False
    bv_cls = qt.BrowserView
    if getattr(bv_cls, "_openarcanum_hardened", False):
        return
    base_page, base_view = bv_cls.WebPage, bv_cls.WebView
    original_load_finished = bv_cls.on_load_finished

    # Patched in place: pywebview's own methods call
    # super(BrowserView.WebPage, self) and super(BrowserView.WebView, self),
    # so subclasses can't take their names. Patched before the window exists.
    original_view_init = base_view.__init__
    original_page_init = base_page.__init__
    original_intercept = bv_cls.RequestInterceptor.interceptRequest
    ui_dir = Path(QUrl(index_url).toLocalFile()).resolve().parent

    def accept_navigation(self, url, nav_type, is_main_frame) -> bool:
        if is_app_page(url, index_url):
            return True
        log.warning(
            "refused a navigation away from the app's page (%s, %s frame)",
            url.scheme() or "no scheme",
            "main" if is_main_frame else "sub",
        )
        return False

    # The one kind of window the page may open: a video pop-out (voice.js
    # popout), an empty window it fills itself with a stream it already has.
    # Same profile, so the page can reach it; no bridge (it isn't
    # pywebview's page), no navigation anywhere, no windows of its own.
    # A window asked to load anything (window.open with a URL) is refused
    # and closed without being shown: it's shown only once it has stayed
    # blank for a moment. Its first load doesn't always ask
    # acceptNavigationRequest, so its URL is watched too.
    from qtpy.QtCore import Qt, QTimer
    from qtpy.QtWebEngineCore import QWebEnginePage
    from qtpy.QtWebEngineWidgets import QWebEngineView

    class PopoutPage(QWebEnginePage):
        refused = False

        def refuse(self) -> None:
            if not self.refused:
                self.refused = True
                log.warning("refused a new window from the page")
                QTimer.singleShot(0, self.parent().close)

        def acceptNavigationRequest(self, url, nav_type, is_main_frame) -> bool:
            if url.toString() in ("", "about:blank"):
                return True
            self.refuse()
            return False

        def createWindow(self, _type):
            return None

    popouts: set = set()

    def create_window(self, win_type):
        if win_type not in (QWebEnginePage.WebWindowType.WebBrowserWindow, QWebEnginePage.WebWindowType.WebDialog):
            log.warning("refused a new window from the page")
            return None
        view = QWebEngineView()
        page = PopoutPage(self.profile(), view)
        view.setPage(page)
        view.setWindowTitle(PRODUCT_NAME)
        view.resize(960, 540)
        page.windowCloseRequested.connect(view.close)
        page.urlChanged.connect(lambda url, p=page: None if url.toString() in ("", "about:blank") else p.refuse())
        page.titleChanged.connect(
            lambda title, v=view: v.setWindowTitle(f"{title} - {PRODUCT_NAME}" if title else PRODUCT_NAME)
        )
        popouts.add(view)
        view.destroyed.connect(lambda *_a, v=view: popouts.discard(v))
        view.setAttribute(Qt.WidgetAttribute.WA_DeleteOnClose, True)
        QTimer.singleShot(400, lambda: None if page.refused or view not in popouts else view.show())
        return page

    def view_init(self, parent=None):
        original_view_init(self, parent)
        self._openarcanum_drops = _DropBlocker()
        self._openarcanum_drops.install(self)

    def page_init(self, parent=None, profile=None):
        original_page_init(self, parent, profile)
        s = self.settings()
        s.setAttribute(type(s).WebAttribute.WebRTCPublicInterfacesOnly, True)
        # window.open reaches create_window, which only lets a video
        # pop-out through.
        s.setAttribute(type(s).WebAttribute.JavascriptCanOpenWindows, True)

    def intercept_request(self, info):
        url = info.requestUrl()
        if url.isLocalFile() and not Path(url.toLocalFile()).resolve().is_relative_to(ui_dir):
            log.warning("refused a request for a local file outside the app's page")
            info.block(True)
            return
        original_intercept(self, info)

    def refuse_drag(self, e) -> None:
        e.ignore()

    def on_load_finished(self):
        page = self.webview.page()
        if not is_app_page(page.url(), index_url):
            log.warning("a page other than the app's own loaded; the bridge isn't given to it")
            page.triggerAction(type(page).WebAction.Stop)
            return None
        return original_load_finished(self)

    base_page.__init__ = page_init
    bv_cls.RequestInterceptor.interceptRequest = intercept_request
    base_page.acceptNavigationRequest = accept_navigation
    base_page.createWindow = create_window
    base_view.__init__ = view_init
    base_view.dragEnterEvent = refuse_drag
    base_view.dragMoveEvent = refuse_drag
    base_view.dropEvent = refuse_drag
    bv_cls.on_load_finished = on_load_finished
    bv_cls._openarcanum_hardened = True


def run(paths: Paths, args: argparse.Namespace) -> int:
    import webview

    daemon_bridge_threads()

    sandbox_file = paths.data / SANDBOX_STATE
    sandbox, why = decide_sandbox(sandbox_file, __version__)
    if not sandbox:
        log.warning("the browser engine runs without its sandbox: %s", why)
    harden_engine_flags(sandbox, allow_debugging=args.debug or args.allow_remote_debugging)
    # Only JsApi's own public methods are callable from the page, and the
    # window stays on the app's own page.
    install_bridge_guard()
    harden_webview(FRONTEND_INDEX.as_uri())

    from openarcanum.core import Core

    if not FRONTEND_INDEX.exists():
        log.error("UI not found at %s", FRONTEND_INDEX)
        return 1

    # Qt names its runtime folders after argv[0]; keep that the product name.
    sys.argv[0] = PRODUCT_NAME.lower()
    # The application object is made here rather than by pywebview (which
    # reuses it) so the single-instance socket and the macOS URL-open handler
    # exist before anything slow happens. No other arguments reach Qt.
    qapp = instance.create_application([sys.argv[0]])
    # The app icon for every window, the taskbar and the dock; on Linux the
    # desktop also finds it through the .desktop file named here (Wayland's
    # app id).
    qapp.setWindowIcon(tray_mod.qicon())
    if sys.platform.startswith("linux"):
        qapp.setDesktopFileName("openarcanum")
    bus = Bus()
    gui = Gui(None, bus, smoke=args.smoke, debug=args.debug, devtools=devtools_allowed(args.debug))
    # Quitting through the system (the macOS app menu or Dock, logging out)
    # is quitting, not closing the window to the tray. A sign-out (Windows
    # WM_QUERYENDSESSION, commitDataRequest here) gives the app a few
    # seconds: it saves and quits at once, as on a terminate signal.
    qapp.set_quit_handler(lambda: setattr(gui, "quitting", True))
    qapp.commitDataRequest.connect(lambda *_args: gui._quit_now())
    gui.sandbox_file = sandbox_file
    link = codes.link_from_os(args.link) if args.link else None
    listener = None
    if not args.smoke:
        listener = instance.Listener(instance.server_name(paths.config), gui.offer_link)
        if not listener.listen():
            # Another launch got there first after this one checked.
            return 0 if instance.hand_off(listener.name, link) else 1
    qapp.set_link_handler(gui.offer_link)
    if link:
        gui.offer_link(link)
    loop = LoopThread()
    core: Core = loop.call(lambda: Core(paths, emit=bus.push))
    prefs = core.prefs
    geo = prefs.window
    api = JsApi(loop, core, bus, gui)
    window = webview.create_window(
        PRODUCT_NAME,
        # A real file:// URI keeps pywebview from starting its loopback HTTP
        # server: it serves anything that isn't http(s):// or file:// itself.
        url=FRONTEND_INDEX.as_uri(),
        js_api=api,
        width=geo.get("window_width", 1200),
        height=geo.get("window_height", 800),
        x=geo.get("window_x"),
        y=geo.get("window_y"),
        min_size=(720, 480),
        background_color="#1e1f22",
        text_select=True,
    )
    gui.window = window
    bus.set_window(window)
    gui.start_tray(core, loop)
    window.events.closing += gui.on_closing
    if sys.platform == "darwin":
        # macOS: clicking the Dock icon (or switching to the app) brings back
        # a window closed to the menu bar.
        from qtpy.QtCore import Qt

        def activated(state) -> None:
            bv = gui._browser_view()
            if state == Qt.ApplicationState.ApplicationActive and bv is not None and not bv.isVisible():
                gui._raise_window()

        qapp.applicationStateChanged.connect(activated)

    # Desktop notifications come from the core as events; the tray follows
    # the profile's state.
    def forward_notify(event: str, data):
        bus.push(event, data)
        if event == "notify":
            gui.notify(data.get("title", PRODUCT_NAME), data.get("body", ""))
        elif event == "profile":
            gui.profile_changed()

    core.emit = forward_notify
    core.media_access = gui.media_access
    core.updates.restart_args = restart_args(args)
    if not args.smoke:
        loop.call(core.start_updates)
        loop.call(core.start_clusters)
        # Communities hosted on this computer run from the start, before
        # the profile is unlocked and while the window is in the tray.
        loop.call(core.start_hosting)

    # Remember window geometry (preferences; nothing identifying).
    geometry = {
        "w": geo.get("window_width", 1200),
        "h": geo.get("window_height", 800),
        "x": geo.get("window_x", 0),
        "y": geo.get("window_y", 0),
    }

    def on_resized(w, h):
        geometry.update(w=w, h=h)

    def on_moved(x, y):
        geometry.update(x=x, y=y)

    window.events.resized += on_resized
    window.events.moved += on_moved

    exit_code = {"code": 0}
    if args.smoke:

        def watchdog():
            ok = gui.smoke_ok.wait(SMOKE_TIMEOUT)
            if not ok:
                log.error("smoke: UI never reported ready")
                exit_code["code"] = 1
            time.sleep(0.2)
            gui.quit()

        threading.Thread(target=watchdog, daemon=True, name="smoke-watchdog").start()

    try:
        # private_mode=True is pywebview's default, set explicitly so a library
        # change can't flip it: the engine keeps no cookies, cache or storage
        # on disk. No storage_path, no debug=True.
        webview.start(gui="qt", private_mode=True, debug=False)
    except Exception:
        log.exception("the window could not be started")
        print("openarcanum: the window could not be started; see the log for details", file=sys.stderr)
        exit_code["code"] = 1
    finally:
        if gui.tray is not None:
            gui.tray.hide()
        try:
            prefs.set_window(geometry["x"], geometry["y"], geometry["w"], geometry["h"])
        except Exception:
            log.exception("saving window geometry")
        try:
            loop.run(core.shutdown(), timeout=5)
        except Exception:
            log.exception("core shutdown")
        if listener is not None:
            listener.close()
        # Everything is saved. Bridge calls still waiting on the core get
        # "closing" at once; a bridge thread left handing its result to the
        # page (evaluate_js, with the window gone) is a daemon
        # (daemon_bridge_threads), so the interpreter exits normally.
        loop.close()
        log.info("exiting with status %d", exit_code["code"])
        crashlog.clean_exit(paths.log)
        # An update accepted for install: start the new version (or the
        # installer) last, once this run's crash marker is gone, so the new
        # process neither reports this run as a crash nor has its own marker
        # removed by it.
        if core.updates.pending is not None:
            try:
                launch_update(core.updates.pending)
            except Exception:
                log.exception("starting the update")
    return exit_code["code"]


if __name__ == "__main__":
    signal.signal(signal.SIGINT, signal.default_int_handler)
    main()
