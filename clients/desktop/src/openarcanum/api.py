"""Methods exposed to JS as `window.pywebview.api.*`.

pywebview exposes only methods defined here, so every method JS calls is
defined explicitly. Each one runs on a pywebview bridge thread, so it only
ever waits on the core's asyncio loop, with a timeout, never on the Qt loop. Anything that
needs the GUI thread (file dialogs, clipboard, DevTools) is handed off and
answered later through a bus event.

Every method returns {"ok": true, "value": ...} or {"ok": false, "error": "..."}.
"""

from __future__ import annotations

import asyncio
import contextlib
import inspect
import logging
import sys
import threading
import time
from collections.abc import Callable
from concurrent.futures import CancelledError
from concurrent.futures import TimeoutError as FutureTimeout
from typing import TYPE_CHECKING, Any, Protocol

from openarcanum import i18n
from openarcanum import updates as updates_mod
from openarcanum.errors import UserError
from openarcanum.i18n import _
from openarcanum.paths import read_at_most

if TYPE_CHECKING:
    from openarcanum.bus import Bus
    from openarcanum.core import Core

log = logging.getLogger(__name__)


class LoopThread:
    """The background asyncio loop that owns all crypto and network I/O."""

    def __init__(self):
        self.loop = asyncio.new_event_loop()
        self.closed = False
        self._thread = threading.Thread(target=self._run, daemon=True, name="core-loop")
        self._thread.start()

    def _run(self) -> None:
        from openarcanum import crashlog

        asyncio.set_event_loop(self.loop)
        self.loop.set_exception_handler(crashlog.loop_exception_handler)
        self.loop.run_forever()

    def run(self, coro, timeout: float):
        if self.closed:
            coro.close()
            raise UserError(_("OpenArcanum is closing."))
        return asyncio.run_coroutine_threadsafe(coro, self.loop).result(timeout)

    def close(self, timeout: float = 5.0) -> None:
        """At exit, after the core has saved everything: cancel whatever is
        still running (so bridge threads waiting on it return at once and the
        process can exit normally), stop the loop and wait for its thread."""
        if self.closed:
            return
        self.closed = True

        async def cancel_all() -> None:
            tasks = [t for t in asyncio.all_tasks() if t is not asyncio.current_task()]
            for t in tasks:
                t.cancel()
            await asyncio.gather(*tasks, return_exceptions=True)

        with contextlib.suppress(Exception):
            asyncio.run_coroutine_threadsafe(cancel_all(), self.loop).result(timeout)
        self.loop.call_soon_threadsafe(self.loop.stop)
        self._thread.join(timeout)

    def call(self, fn: Callable, timeout: float = 10.0):
        async def wrap():
            return fn()

        return self.run(wrap(), timeout)

    def submit(self, coro, done: Callable[[], None] | None = None) -> None:
        """Run a coroutine on the loop without waiting for it (the Qt main
        thread must not block); failures are logged, and `done` runs after
        either way (on the loop's thread)."""
        if self.closed:
            coro.close()
            return

        def finished(fut) -> None:
            if not fut.cancelled() and fut.exception() is not None:
                log.error("background call failed", exc_info=fut.exception())
            if done is not None:
                done()

        asyncio.run_coroutine_threadsafe(coro, self.loop).add_done_callback(finished)


class PickedPaths:
    """The paths chosen in a native file dialog this session: the only
    paths the page may hand back to Python (docs/threat-model.md, "The page
    and the bridge"). Python opens every dialog itself, so a path the page
    makes up (script that got in, a crafted value) is refused: nothing reads
    or writes a file nobody picked.

    Kinds: "open" (read: open dialogs), "save" (write: save dialogs) and
    "folder" (folder dialogs)."""

    LIMIT = 500

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._paths: dict[str, list[str]] = {"open": [], "save": [], "folder": []}

    def grant(self, kind: str, paths: list[str]) -> None:
        with self._lock:
            got = self._paths[kind]
            for path in paths:
                if path and path not in got:
                    got.append(path)
            del got[: max(0, len(got) - self.LIMIT)]

    def granted(self, kind: str, path: object) -> bool:
        with self._lock:
            return isinstance(path, str) and bool(path) and path in self._paths[kind]

    def check(self, kind: str, path: object) -> str:
        if not self.granted(kind, path):
            log.warning("bridge: refused a %s path that wasn't chosen in a file dialog", kind)
            raise UserError(_("Choose the file in the file dialog (Browse…)."))
        return str(path)


def dialog_mode(kind: str, save: bool) -> str:
    return "folder" if kind == "folder" else "save" if save else "open"


class GuiHooks(Protocol):
    """What the API needs from the window layer (app.py `Gui`). All non-blocking."""

    def open_file_dialog(
        self,
        request_id: str,
        kind: str,
        save: bool,
        default_name: str = "",
        start: str = "",
        grant: Callable[[list[str]], None] | None = None,
    ) -> None: ...
    def copy_text(self, text: str) -> bool | None: ...
    def open_folder(self, path: str) -> None: ...
    def open_devtools(self) -> None: ...
    def smoke_ready(self, info: dict) -> None: ...
    def notify(self, title: str, body: str) -> None: ...
    def ui_loaded(self) -> None: ...
    def media_access(self, allowed: bool) -> None: ...
    def screen_pick(self, kind: str, index: int) -> None: ...
    def take_invite_link(self) -> str | None: ...
    def quit(self) -> None: ...
    def confirm_quit(self) -> None: ...
    def attention(self, mentions: int, direct: int, review: int) -> None: ...
    def tray_status(self) -> dict: ...


def bridge_methods() -> frozenset[str]:
    """The names the page may call: public methods defined on JsApi itself
    (not inherited, no leading underscore, nothing else)."""
    return frozenset(
        name
        for name, value in vars(JsApi).items()
        if not name.startswith("_") and inspect.isfunction(value) and name.isidentifier()
    )


def bridge_allows(name: object, allowed: frozenset[str]) -> bool:
    """Whether a call from the page may go to pywebview's dispatch. Checked
    before pywebview resolves the name (its resolver follows dotted names
    through any attribute, private ones included)."""
    return isinstance(name, str) and "." not in name and not name.startswith("_") and name in allowed


def install_bridge_guard() -> None:
    """Replace the name lookup of pywebview's js_api bridge with JsApi's
    allowlist (docs/threat-model.md, "The page and the bridge").

    pywebview resolves a call with
    `window._functions.get(name) or get_nested_attribute(window._js_api, name)`,
    which walks dotted names through any attribute: page script could call
    `_core.vault.data.__repr__`. Every backend module imports js_bridge_call
    by name, so each loaded one is rebound to a guard that refuses anything
    but an allowed name, and pywebview's own internal calls (window moving,
    DOM events, shared state, evaluate_js callbacks) which this app doesn't
    use, before any attribute is looked up."""
    import webview.util

    original = getattr(webview.util.js_bridge_call, "_openarcanum_original", webview.util.js_bridge_call)
    allowed = bridge_methods()

    def guarded(window, func_name, param, value_id):
        if not bridge_allows(func_name, allowed):
            log.warning("bridge: refused a call to a name that isn't part of the API")
            return None
        return original(window, func_name, param, value_id)

    guarded._openarcanum_original = original  # type: ignore[attr-defined]
    webview.util.js_bridge_call = guarded
    for mod_name in ("qt", "gtk", "cocoa", "edgechromium", "mshtml", "cef", "android"):
        mod = sys.modules.get(f"webview.platforms.{mod_name}")
        if mod is None and mod_name == "qt":
            import webview.platforms.qt as mod
        if mod is not None and hasattr(mod, "js_bridge_call"):
            mod.js_bridge_call = guarded


class JsApi:
    def __init__(self, loop: LoopThread, core: Core, bus: Bus, gui: GuiHooks):
        self._loop = loop
        self._core = core
        self._bus = bus
        self._gui = gui
        self._picked = PickedPaths()

    # ---------------------------------------------------------------- plumbing

    def _open_path(self, path: object) -> str:
        return self._picked.check("open", path)

    def _save_path(self, path: object) -> str:
        return self._picked.check("save", path)

    def _wrap(self, fn: Callable[[], Any]) -> dict:
        try:
            return {"ok": True, "value": fn()}
        except UserError as e:
            return {"ok": False, "error": str(e)}
        except FutureTimeout:
            return {"ok": False, "error": _("That took too long. Check your connection and try again.")}
        except CancelledError:
            return {"ok": False, "error": _("OpenArcanum is closing.")}
        except Exception as e:
            log.exception("api call failed")
            return {"ok": False, "error": _("Something went wrong ({error}).").format(error=e.__class__.__name__)}

    def _a(self, coro_fn: Callable[[], Any], timeout: float = 60.0) -> dict:
        async def run():
            return await coro_fn()

        return self._wrap(lambda: self._loop.run(run(), timeout))

    def _s(self, fn: Callable[[], Any], timeout: float = 10.0) -> dict:
        return self._wrap(lambda: self._loop.call(fn, timeout))

    # ---------------------------------------------------------------- app

    def ready(self) -> dict:
        """The page loaded and can receive events."""
        self._bus.mark_ready()
        self._gui.ui_loaded()
        return self._s(self._core.profile_status)

    def ui_strings(self) -> dict:
        """The running language's translation table for the page's i18n.js
        (empty for English)."""
        return {"ok": True, "value": i18n.active().for_page()}

    def smoke_ready(self, info: dict) -> dict:
        self._gui.smoke_ready(info or {})
        return {"ok": True, "value": True}

    def set_attention(self, mentions: int, direct: int, review: int) -> dict:
        """What the window's badges count (unread mentions, unread direct
        messages, items waiting for review), for the tray icon's dot and
        tooltip (openarcanum/tray.py)."""
        self._gui.attention(mentions, direct, review)
        return {"ok": True, "value": None}

    def tray_status(self) -> dict:
        """Whether this desktop has a system tray (Settings → Appearance)."""
        return self._wrap(self._gui.tray_status)

    def quit_app(self) -> dict:
        """Quit anyway, after the warning that communities hosted on this
        computer go offline (the confirm_quit event)."""
        self._gui.confirm_quit()
        return {"ok": True, "value": None}

    def media_access(self, allowed: bool) -> dict:
        """The page may use the microphone and camera (a call, or testing
        devices in settings) or not. Everything else is always refused."""
        self._gui.media_access(bool(allowed))
        return {"ok": True, "value": None}

    def screen_pick(self, kind: str, index: int) -> dict:
        """The answer from the app's screen or window picker ("" cancels)."""
        if kind not in ("screen", "window", ""):
            return {"ok": False, "error": _("Unknown choice.")}
        self._gui.screen_pick(kind, int(index))
        return {"ok": True, "value": None}

    # Staying signed in (keychain.py).
    def unlock_remembered(self) -> dict:
        return self._a(self._core.unlock_remembered, timeout=30)

    def remember_status(self) -> dict:
        return self._a(self._core.remember_status, timeout=60)

    def set_remember(self, enabled: bool, passphrase: str = "") -> dict:
        return self._a(lambda: self._core.set_remember(bool(enabled), str(passphrase or "")), timeout=60)

    def answer_stay_signed_in(self, accept: bool) -> dict:
        return self._a(lambda: self._core.answer_stay_signed_in(bool(accept)), timeout=60)

    def report_page_error(self, kind: str, where: str) -> dict:
        """An uncaught error in the page, for the local log: its type and
        where (file:line), never its message (which can quote content)."""
        now = time.monotonic()
        self._page_errors = [t for t in getattr(self, "_page_errors", []) if now - t < 60]
        if len(self._page_errors) < 20:
            self._page_errors.append(now)
            safe_kind = "".join(c for c in str(kind)[:40] if c.isalnum() or c in "._") or "Error"
            safe_where = "".join(c for c in str(where)[:200] if c.isprintable())
            log.warning("page error %s at %s", safe_kind, safe_where)
        return {"ok": True, "value": None}

    def take_invite_link(self) -> dict:
        """The invite link the OS opened the app with (or a later launch
        handed over), once. It only prefills the Join dialog."""
        return {"ok": True, "value": self._gui.take_invite_link()}

    def open_devtools(self) -> dict:
        self._gui.open_devtools()
        return {"ok": True, "value": True}

    def copy_text(self, text: str) -> dict:
        # The window layer says when it couldn't hand the text on (no
        # window yet): the page then shows that instead of "Copied".
        if self._gui.copy_text(str(text)[:100_000]) is False:
            return {"ok": False, "error": _("Couldn't reach the clipboard.")}
        return {"ok": True, "value": True}

    def open_folder(self, location: str) -> dict:
        """Settings → Data & storage → Open folder: one of the app's own
        locations (by its id in storage_locations), never an arbitrary path."""
        return self._wrap(lambda: self._gui.open_folder(self._loop.call(lambda: self._core.folder_of(str(location)))))

    def storage_locations(self) -> dict:
        return self._s(self._core.storage_locations)

    def _dialog(self, request_id: str, kind: str, save: bool, default_name: str, start: str) -> dict:
        """Open a native file dialog; its answer (the "dialog" event) is the
        only kind of path the page can pass back (PickedPaths). `start` is a
        typed path: the dialog opens on it, to be confirmed there."""
        kind = str(kind or "any")
        mode = dialog_mode(kind, save)
        start = str(start or "")
        if start and self._picked.granted(mode, start):
            # Already chosen in a dialog: no need to ask again.
            self._bus.push("dialog", {"id": str(request_id), "path": start, "paths": [start]})
            return {"ok": True, "value": True}
        self._gui.open_file_dialog(
            str(request_id),
            kind,
            save=save,
            default_name=str(default_name or ""),
            start=start,
            grant=lambda paths: self._picked.grant(mode, paths),
        )
        return {"ok": True, "value": True}

    def pick_file(self, request_id: str, kind: str = "any", start: str = "") -> dict:
        return self._dialog(request_id, kind, False, "", start)

    def downloads_dir(self) -> dict:
        from openarcanum.paths import save_folder

        return {"ok": True, "value": str(save_folder())}

    def pick_save(self, request_id: str, kind: str = "any", default_name: str = "", start: str = "") -> dict:
        return self._dialog(request_id, kind, True, default_name, start)

    # ---------------------------------------------------------------- profile

    def status(self) -> dict:
        return self._s(self._core.profile_status)

    def create_profile(self, passphrase: str, device_name: str = "") -> dict:
        return self._a(lambda: self._core.create_profile(passphrase, device_name))

    def unlock(self, passphrase: str) -> dict:
        return self._a(lambda: self._core.unlock(passphrase))

    def lock(self) -> dict:
        return self._a(self._core.lock)

    # Maintainer nodes: preferences, so these work locked and before a
    # profile exists (the lock screens' Change…).

    def maintainers(self) -> dict:
        return self._s(self._core.maintainers_status)

    def check_new_maintainer(self, url: str, current: list) -> dict:
        return self._s(lambda: self._core.check_new_maintainer(url, current))

    def set_maintainers(self, urls: list) -> dict:
        return self._a(lambda: self._core.set_maintainers(urls))

    def maintainer_cluster_offer(self, url: str) -> dict:
        return self._a(lambda: self._core.maintainer_cluster_offer(url))

    def reset_profile(self) -> dict:
        return self._a(self._core.reset_profile)

    def change_passphrase(self, old: str, new: str) -> dict:
        return self._a(lambda: self._core.change_passphrase(old, new))

    def passphrase_strength(self, passphrase: str) -> dict:
        from openarcanum.settings import passphrase_strength

        return {"ok": True, "value": passphrase_strength(str(passphrase))}

    def generate_passphrase(self) -> dict:
        """A new random passphrase for a new-passphrase field (passgen.py).
        Works before a profile exists; nothing keeps it."""
        from openarcanum import passgen

        return self._wrap(lambda: {"passphrase": passgen.generate(), "bits": round(passgen.bits())})

    def activity(self) -> dict:
        return self._s(self._core.activity)

    def export_backup(self, path: str, passphrase: str, current: str = "") -> dict:
        """Export a backup to a file chosen in the save dialog, once the
        profile's passphrase (`current`, checked in Python) is confirmed;
        `passphrase` is the backup's own."""

        async def run():
            target = self._save_path(path)
            await self._core.confirm_passphrase(str(current or ""))
            return await self._core.export_backup(target, passphrase)

        return self._a(run)

    def backup_status(self) -> dict:
        return self._s(self._core.backup_status)

    def restore_backup(self, path: str, backup_passphrase: str, new_passphrase: str, device_name: str = "") -> dict:
        return self._a(
            lambda: self._core.restore_backup(self._open_path(path), backup_passphrase, new_passphrase, device_name)
        )

    # ---------------------------------------------------------------- communities

    def list_communities(self) -> dict:
        return self._s(self._core.list_communities)

    def community_state(self, cid: str) -> dict:
        return self._s(lambda: self._core.community_state(cid))

    def detect_code(self, text: str) -> dict:
        from openarcanum.codes import detect

        return {"ok": True, "value": detect(str(text))}

    def preview(self, source: dict) -> dict:
        return self._a(lambda: self._core.preview(source))

    def join(self, source: dict, identity: str, display_name: str) -> dict:
        return self._a(lambda: self._core.join(source, identity, display_name))

    def claim(self, claim_code: str, settings: dict, display_name: str) -> dict:
        return self._a(lambda: self._core.claim(claim_code, settings, display_name))

    # Hosting on this computer (hosting.py; docs/user-guide.md).

    def host_community(self, settings: dict, display_name: str) -> dict:
        """Create a community served from this computer and claim it."""
        return self._a(lambda: self._core.host_community(settings or {}, str(display_name or "")), timeout=120)

    def hosting_list(self) -> dict:
        """Every community hosted on this computer: id, community, name,
        state (stopped, starting, running, restarting, failed), enabled,
        error, restarts, since, data_dir, log_file, server_status."""
        return self._s(self._core.hosting_list)

    def hosting_start(self, cid: str) -> dict:
        return self._a(lambda: self._core.hosting_start(str(cid)))

    def hosting_stop(self, cid: str) -> dict:
        return self._a(lambda: self._core.hosting_stop(str(cid)), timeout=30)

    def hosting_reregister(self, cid: str) -> dict:
        return self._a(lambda: self._core.hosting_reregister(str(cid)), timeout=60)

    def community_binary_info(self) -> dict:
        """The community server program inside this app (Host on your own
        server → Copy the community server)."""
        return self._s(self._core.community_binary_info)

    def save_community_binary(self, path: str) -> dict:
        return self._a(lambda: self._core.save_community_binary(self._save_path(path)), timeout=60)

    def leave(self, cid: str, delete_key: bool = True) -> dict:
        return self._a(lambda: self._core.leave(cid, bool(delete_key)))

    def dismiss(self, cid: str) -> dict:
        return self._a(lambda: self._core.dismiss(cid))

    def retry(self, cid: str) -> dict:
        return self._a(lambda: self._core.retry(cid))

    def directory_search(self, query: str, tag: str = "") -> dict:
        return self._a(lambda: self._core.directory_search(query, tag))

    def directory_status(self) -> dict:
        return self._s(self._core.directory_status)

    def directory_refresh(self) -> dict:
        return self._a(self._core.directory_refresh)

    def identity_choices(self) -> dict:
        return self._s(self._core.identity_choices)

    # ---------------------------------------------------------------- messages

    def history(self, cid: str, channel_id: int, before_id: int = 0) -> dict:
        return self._a(lambda: self._core.history(cid, int(channel_id), int(before_id or 0)), timeout=30)

    def cached_messages(self, cid: str, channel_id: int) -> dict:
        return self._s(lambda: self._core.cached_messages(cid, int(channel_id)))

    def send(self, cid: str, channel_id: int, text: str, reply_to: int = 0, files: list | None = None) -> dict:
        try:
            paths = [self._open_path(p) for p in (files or [])]
        except UserError as e:
            return {"ok": False, "error": str(e)}
        return self._a(
            lambda: self._core.send(cid, int(channel_id), str(text), int(reply_to or 0), paths),
            timeout=600 if paths else 60,
        )

    def react(self, cid: str, message_id: int, emoji: str, remove: bool = False) -> dict:
        return self._a(lambda: self._core.react(cid, int(message_id), str(emoji), bool(remove)))

    def pin(self, cid: str, message_id: int, unpin: bool = False) -> dict:
        return self._a(lambda: self._core.pin(cid, int(message_id), bool(unpin)))

    def pins(self, cid: str, channel_id: int) -> dict:
        return self._a(lambda: self._core.pins(cid, int(channel_id)))

    def typing(self, cid: str, channel_id: int) -> dict:
        return self._a(lambda: self._core.typing(cid, int(channel_id)), timeout=15)

    def mark_read(self, cid: str, channel_id: int, message_id: int = 0) -> dict:
        return self._s(lambda: self._core.mark_read(cid, int(channel_id), int(message_id or 0)))

    def history_around(self, cid: str, channel_id: int, message_id: int) -> dict:
        return self._a(lambda: self._core.history_around(cid, int(channel_id), int(message_id)), timeout=40)

    def edit(self, cid: str, message_id: int, text: str) -> dict:
        return self._a(lambda: self._core.edit(cid, int(message_id), str(text)))

    def delete_message(self, cid: str, channel_id: int, message_id: int) -> dict:
        return self._a(lambda: self._core.delete_message(cid, int(channel_id), int(message_id)))

    def search_messages(self, cid: str, query: str, channel_id: int = 0) -> dict:
        return self._a(lambda: self._core.search_messages(cid, str(query), int(channel_id or 0)))

    def upload(self, cid: str, channel_id: int, path: str, text: str = "") -> dict:
        return self._a(
            lambda: self._core.upload(cid, int(channel_id), self._open_path(path), str(text or "")), timeout=600
        )

    def save_file(self, cid: str, file: dict, path: str) -> dict:
        return self._a(lambda: self._core.save_file(cid, file, self._save_path(path)), timeout=600)

    def file_preview(self, cid: str, file: dict) -> dict:
        return self._a(lambda: self._core.file_preview(cid, file), timeout=120)

    # ---------------------------------------------------------------- profile per community

    def set_nickname(self, cid: str, member: str, nickname: str) -> dict:
        return self._a(lambda: self._core.set_nickname(cid, str(member), str(nickname or "")))

    def update_profile(self, cid: str, about: str, pronouns: str) -> dict:
        return self._a(lambda: self._core.update_profile(cid, str(about or ""), str(pronouns or "")))

    def set_display_name(self, cid: str, name: str) -> dict:
        return self._a(lambda: self._core.set_display_name(cid, name))

    def set_avatar(self, cid: str, path: str | None) -> dict:
        return self._a(lambda: self._core.set_avatar(cid, self._open_path(path) if path else None))

    def avatar(self, cid: str, member: str, version: int = 0) -> dict:
        return self._a(lambda: self._core.avatar(cid, member, int(version or 0)))

    def members(self, cid: str) -> dict:
        return self._a(lambda: self._core.members(cid))

    def set_contact_card(self, cid: str, enabled: bool) -> dict:
        return self._a(lambda: self._core.dms.set_contact_card(cid, bool(enabled)))

    def dm_card_status(self, cid: str) -> dict:
        """Whether the community's server holds my contact card right now
        (what "Publish a contact card here" put there)."""
        return self._a(lambda: self._core.dms.card_status(cid))

    def read_image(self, path: str) -> dict:
        """Read a small image (icon) and return it base64-encoded."""

        def read():
            import base64
            from pathlib import Path

            try:
                data = read_at_most(Path(self._open_path(path)), 256 * 1024)
            except OSError as e:
                raise UserError(_("Can't read that image: {reason}").format(reason=e.strerror)) from None
            if data is None:
                raise UserError(_("Images can be at most 256 KB."))
            if not (
                data[:8] == b"\x89PNG\r\n\x1a\n"
                or data[:3] == b"\xff\xd8\xff"
                or data[:4] == b"GIF8"
                or (data[:4] == b"RIFF" and data[8:12] == b"WEBP")
            ):
                raise UserError(_("Use a PNG, JPEG, GIF or WebP image."))
            return base64.b64encode(data).decode()

        return self._wrap(read)

    # ---------------------------------------------------------------- admin

    def admin(self, cid: str, op: str, params: dict | None = None) -> dict:
        return self._a(lambda: self._core.admin(cid, str(op), params or {}))

    def report_message(self, cid: str, message_id: int, reason: str = "") -> dict:
        return self._a(lambda: self._core.moderation.report_message(cid, message_id, reason))

    def emoji_upload(self, cid: str, name: str, path: str) -> dict:
        return self._a(lambda: self._core.moderation.emoji_upload(cid, name, self._open_path(path)))

    def emoji_image(self, cid: str, emoji_id: int) -> dict:
        return self._a(lambda: self._core.moderation.emoji_image(cid, emoji_id))

    def admin_query(self, cid: str, what: str, params: dict | None = None) -> dict:
        return self._a(lambda: self._core.admin_query(cid, str(what), params or {}))

    def rotate_server_key(self, cid: str) -> dict:
        return self._a(lambda: self._core.rotate_server_key(cid))

    def adopt_server(self, cid: str, move_code: str) -> dict:
        return self._a(lambda: self._core.adopt_server(cid, move_code))

    def invite_owner(self, cid: str, member: str) -> dict:
        return self._a(lambda: self._core.invite_owner(cid, str(member)))

    def cancel_owner_invite(self, cid: str, member: str) -> dict:
        return self._a(lambda: self._core.cancel_owner_invite(cid, str(member)))

    def accept_owner_invite(self, cid: str) -> dict:
        return self._a(lambda: self._core.accept_owner_invite(cid))

    def decline_owner_invite(self, cid: str) -> dict:
        return self._a(lambda: self._core.decline_owner_invite(cid))

    def confirm_owner(self, cid: str, member: str) -> dict:
        return self._a(lambda: self._core.confirm_owner(cid, str(member)))

    def remove_owner(self, cid: str, owner_key: str) -> dict:
        return self._a(lambda: self._core.remove_owner(cid, str(owner_key)))

    # ---------------------------------------------------------------- settings & UI state

    def get_settings(self) -> dict:
        return self._s(self._core.get_settings)

    def set_setting(self, key: str, value: Any) -> dict:
        if str(key) == "data_dir" and value:
            # A folder chosen in the folder dialog, never one the page names.
            try:
                value = self._picked.check("folder", value)
            except UserError as e:
                return {"ok": False, "error": str(e)}
        return self._a(lambda: self._core.set_setting(str(key), value))

    def set_community_notify(self, cid: str, level: str) -> dict:
        return self._a(lambda: self._core.set_community_notify(cid, str(level or "")))

    # ---------------------------------------------------------------- statuses (statuses.py)

    def statuses(self) -> dict:
        return self._s(self._core.statuses.view)

    def set_status(self, state: str, text: str = "", emoji: str = "", clear_after: str = "never") -> dict:
        return self._a(lambda: self._core.statuses.set_status(str(state), text, emoji, str(clear_after or "never")))

    def clear_status_text(self) -> dict:
        return self._a(self._core.statuses.clear_text)

    def set_status_community(self, cid: str, level: str) -> dict:
        return self._a(lambda: self._core.set_status_community(str(cid), str(level or "")))

    def mark_community_read(self, cid: str) -> dict:
        return self._s(lambda: self._core.mark_community_read(cid))

    def toggle_mute(self, cid: str, channel_id: int | None = None) -> dict:
        return self._a(lambda: self._core.toggle_mute(cid, None if channel_id in (None, "") else int(channel_id)))

    def clear_community_cache(self, cid: str) -> dict:
        return self._a(lambda: self._core.clear_community_cache(cid))

    def clear_cache(self) -> dict:
        return self._a(self._core.clear_cache)

    def ui_state_get(self, key: str) -> dict:
        return self._s(lambda: self._core.ui_state_get(key))

    def ui_state_set(self, key: str, value: Any) -> dict:
        return self._s(lambda: self._core.ui_state_set(key, value))

    # ---------------------------------------------------------------- updates (docs/release.md)

    def update_status(self) -> dict:
        return self._s(self._core.updates.status)

    def update_check(self) -> dict:
        return self._a(self._core.update_check)

    def update_later(self, version: str) -> dict:
        return self._s(lambda: self._core.updates.later(str(version)))

    def update_skip(self, version: str) -> dict:
        return self._s(lambda: self._core.updates.skip(str(version)))

    def update_install(self, version: str, token: str) -> dict:
        """ "Restart and install" in the update prompt. The core refuses
        unless the profile is unlocked and this exact version was
        downloaded and verified (the copy runs off the event loop); then the
        window closes, the app shuts down normally, and app.py starts the new
        version (or the installer)."""
        res = self._a(
            lambda: self._core.update_install(str(version), str(token)), timeout=updates_mod.INSTALL_TIMEOUT + 30
        )
        if res["ok"]:
            self._gui.quit()
        return res

    # ---------------------------------------------------------------- devices

    def devices(self) -> dict:
        return self._a(self._core.devices.list)

    def rename_device(self, device_id: str, name: str) -> dict:
        return self._a(lambda: self._core.devices.rename(device_id, name))

    def remove_device(self, device_id: str, passphrase: str = "") -> dict:
        """Remove a linked device, after the passphrase (checked in Python)."""

        async def run():
            await self._core.confirm_passphrase(str(passphrase or ""))
            return await self._core.devices.remove(device_id)

        return self._a(run, timeout=120)

    def link_new_device_start(self, device_name: str) -> dict:
        """On the NEW device: get a code to type on an existing device (through
        this device's maintainer nodes)."""
        return self._a(lambda: self._core.devices.new_device_start(device_name), timeout=30)

    def link_new_device_finish(self, passphrase: str) -> dict:
        return self._a(lambda: self._core.devices.new_device_finish(passphrase), timeout=120)

    def link_enter_code(self, code: str, passphrase: str = "") -> dict:
        """On the EXISTING device: start linking with the code shown on the new
        one, after the passphrase (checked in Python). Approving
        (link_approve) needs a link started here."""

        async def run():
            await self._core.confirm_passphrase(str(passphrase or ""))
            return await self._core.devices.existing_enter_code(code)

        return self._a(run, timeout=60)

    def link_approve(self, choices: dict) -> dict:
        return self._a(lambda: self._core.devices.existing_approve(choices), timeout=300)

    def link_cancel(self) -> dict:
        return self._a(self._core.devices.cancel)

    # ---------------------------------------------------------------- DMs and group DMs

    def dm_list(self) -> dict:
        return self._s(self._core.dms.list)

    def dm_info(self, gid: str) -> dict:
        return self._s(lambda: self._core.dms.info(gid))

    def dm_open(self, cid: str, member: str) -> dict:
        return self._a(lambda: self._core.dms.open_with_member(cid, member))

    def dm_open_contact(self, ident: str) -> dict:
        return self._a(lambda: self._core.dms.open_with_contact(str(ident)))

    def dm_contact_code(self, name: str = "") -> dict:
        """My contact code, for the report form on the website or anyone who
        should be able to message me without sharing a community."""
        return self._a(lambda: self._core.dms.contact_code(str(name or "")))

    def dm_permanent_code_info(self) -> dict:
        """My permanent contact code, if I made one (None otherwise)."""
        return self._s(self._core.dms.permanent_code_info)

    def dm_permanent_code(self) -> dict:
        """My permanent contact code, made the first time it's asked for."""
        return self._a(self._core.dms.permanent_code)

    def dm_revoke_permanent_code(self) -> dict:
        return self._a(self._core.dms.revoke_permanent_code)

    def dm_call_join(self, gid: str, video: bool = False) -> dict:
        """Join the conversation's call, or start one (leaving any other call)."""
        return self._a(lambda: self._core.voice.join_dm(str(gid), bool(video)))

    def dm_calls(self) -> dict:
        """The calls going on in my conversations (who is in each)."""
        return self._s(self._core.dmcalls.all)

    def dm_add_contact_code(self, code: str) -> dict:
        return self._a(lambda: self._core.dms.add_contact_code(str(code or "")))

    def dm_create_group(self, cid: str, members: list, name: str = "") -> dict:
        return self._a(lambda: self._core.dms.create_group(cid, [str(m) for m in members], str(name)))

    def dm_invite(self, gid: str, cid: str, members: list) -> dict:
        return self._a(lambda: self._core.dms.invite(gid, cid, [str(m) for m in members]))

    def dm_rename(self, gid: str, name: str) -> dict:
        return self._a(lambda: self._core.dms.rename(gid, str(name)))

    def dm_set_icon(self, gid: str, path: str) -> dict:
        return self._a(lambda: self._core.dms.set_icon(gid, self._open_path(path) if path else ""))

    def dm_set_options(self, gid: str, options: dict) -> dict:
        return self._a(
            lambda: self._core.dms.set_options(
                gid, members_can_add=options.get("members_can_add"), allow_attachments=options.get("allow_attachments")
            )
        )

    def dm_remove_member(self, gid: str, ident: str) -> dict:
        return self._a(lambda: self._core.dms.remove_member(gid, ident))

    def dm_transfer(self, gid: str, ident: str) -> dict:
        return self._a(lambda: self._core.dms.transfer(gid, ident))

    def dm_leave(self, gid: str) -> dict:
        return self._a(lambda: self._core.dms.leave(gid))

    def dm_history(self, gid: str) -> dict:
        return self._s(lambda: self._core.dms.history(gid))

    def dm_send(self, gid: str, text: str, reply_to: str = "", files: list | None = None) -> dict:
        try:
            paths = [self._open_path(f) for f in files or []]
        except UserError as e:
            return {"ok": False, "error": str(e)}
        return self._a(lambda: self._core.dms.send(gid, str(text), str(reply_to or ""), paths), timeout=600)

    def dm_save_file(self, gid: str, eid: str, digest: str, path: str) -> dict:
        return self._a(lambda: self._core.dms.save_file(gid, eid, str(digest), self._save_path(path)), timeout=600)

    def dm_edit(self, gid: str, eid: str, text: str) -> dict:
        return self._a(lambda: self._core.dms.edit(gid, eid, str(text)))

    def dm_delete(self, gid: str, eid: str) -> dict:
        return self._a(lambda: self._core.dms.delete(gid, eid))

    def dm_react(self, gid: str, eid: str, emoji: str, remove: bool = False) -> dict:
        return self._a(lambda: self._core.dms.react(gid, eid, str(emoji), bool(remove)))

    def dm_pin(self, gid: str, eid: str, unpin: bool = False) -> dict:
        return self._a(lambda: self._core.dms.pin(gid, eid, bool(unpin)))

    def dm_typing(self, gid: str, typing: bool) -> dict:
        return self._a(lambda: self._core.dms.set_typing(gid, bool(typing)))

    def dm_mark_read(self, gid: str) -> dict:
        return self._a(lambda: self._core.dms.mark_read(gid))

    def dm_accept(self, gid: str) -> dict:
        return self._a(lambda: self._core.dms.accept(gid))

    def dm_decline(self, gid: str) -> dict:
        return self._a(lambda: self._core.dms.decline(gid))

    def dm_poll(self) -> dict:
        return self._a(self._core.dms.poll_once)

    def dm_add_contact(self, cid: str, member: str) -> dict:
        return self._a(lambda: self._core.dms.add_contact(cid, member))

    def dm_remove_contact(self, ident: str) -> dict:
        return self._a(lambda: self._core.remove_contact(ident))

    def dm_contacts(self) -> dict:
        return self._s(self._core.dms.contacts)

    def dm_safety(self, ident: str, gid: str = "") -> dict:
        return self._s(lambda: self._core.dms.safety(ident, str(gid or "")))

    def dm_set_verified(self, ident: str, verified: bool) -> dict:
        return self._a(lambda: self._core.dms.set_verified(ident, bool(verified)))

    def dm_accept_key_change(self, ident: str) -> dict:
        return self._a(lambda: self._core.dms.accept_key_change(ident))

    def dm_block(self, ident: str, blocked: bool) -> dict:
        return self._a(lambda: self._core.dms.set_blocked(ident, bool(blocked)))

    # ---------------------------------------------------------------- voice

    def voice_join(self, cid: str, channel_id: int) -> dict:
        return self._a(lambda: self._core.voice.join(cid, int(channel_id)))

    def voice_leave(self) -> dict:
        return self._a(self._core.voice.leave)

    def voice_state(self) -> dict:
        return self._s(self._core.voice.state)

    def call_signal(self, generation: int, kind: str, args: list) -> dict:
        """Signaling from the page's call (ui/js/call.js) to the relay."""
        if not isinstance(args, list):
            return {"ok": False, "error": _("Something went wrong with the call.")}
        return self._a(lambda: self._core.voice.signal(int(generation), str(kind), args), timeout=30)

    def voice_moderate(self, cid: str, member: str, action: str, value=None) -> dict:
        return self._a(lambda: self._core.voice.moderate(cid, str(member), str(action), value))

    def voice_volume(self, member: str, volume: float) -> dict:
        return self._a(lambda: self._core.voice.set_volume(str(member), float(volume)))

    def voice_local_mute(self, member: str, muted: bool) -> dict:
        return self._a(lambda: self._core.voice.set_local_mute(str(member), bool(muted)))
