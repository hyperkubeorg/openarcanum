"""The system tray, driven for real (Linux, under Xvfb): the app's
QSystemTrayIcon registers with a stand-in tray (tests/fake_sni.py, the
StatusNotifierItem D-Bus protocol most Linux desktops use), and the test
reads its tooltip and icon and clicks it and its menu the way a desktop
panel does. The window is closed the way a window manager does
(WM_DELETE_WINDOW, tests/x11win.py) and its X11 state and icon are read the
same way. Without the stand-in, Xvfb has no tray at all: that is the
no-tray case (closing quits, and Settings says why the option is off).

Windows' and macOS's trays are checked by hand."""

import asyncio
import json
import subprocess
import sys
import time
from pathlib import Path

import pytest
from conftest import PASS, Events, network, run, wait_until
from test_e2e_community import general
from uidriver import App
from uiflows import claim, create_profile, open_settings, send_message, unlock

from openarcanum.core import Core
from openarcanum.paths import Paths

pytestmark = [
    pytest.mark.skipif(not sys.platform.startswith("linux"), reason="Linux desktop bus and X11 test"),
    pytest.mark.gui,
]

HERE = Path(__file__).resolve().parent
ITEM = "/StatusNotifierItem"
RED = (242, 63, 67)


class Desktop:
    """The app's session, seen from outside: its tray item over D-Bus and its
    windows over X11."""

    def __init__(self, tmp: Path):
        self.ready = tmp / "tray-ready.json"
        self.notes_file = tmp / "notifications.jsonl"
        self.helper = [sys.executable, str(HERE / "fake_sni.py"), str(self.ready), str(self.notes_file)]
        self.env: dict = {}
        self.conn = None
        self.service = ""

    def app(self, home: Path, name: str) -> App:
        return App(home, name=name, session_helper=self.helper, session_ready=self.ready)

    def connect(self) -> None:
        from jeepney.io.blocking import open_dbus_connection

        self.env = json.loads(self.ready.read_text())
        self.conn = open_dbus_connection(bus=self.env["DBUS_SESSION_BUS_ADDRESS"])

    def _call(self, path: str, iface: str, member: str, sig: str = "", body: tuple = (), dest: str | None = None):
        from jeepney import DBusAddress, new_method_call

        addr = DBusAddress(path, bus_name=dest or self.service, interface=iface)
        reply = self.conn.send_and_get_reply(new_method_call(addr, member, sig, body), timeout=10)
        assert reply.header.message_type.name == "method_return", reply.body
        return reply.body

    async def item(self, timeout: float = 30) -> None:
        """Wait for the app's tray icon to register."""

        def registered():
            items = self._call(
                "/StatusNotifierWatcher",
                "org.freedesktop.DBus.Properties",
                "Get",
                "ss",
                ("org.kde.StatusNotifierWatcher", "RegisteredStatusNotifierItems"),
                dest="org.kde.StatusNotifierWatcher",
            )[0][1]
            if items:
                self.service = items[-1]
            return bool(items)

        await wait_until(registered, timeout, 0.2)

    def props(self) -> dict:
        body = self._call(ITEM, "org.freedesktop.DBus.Properties", "GetAll", "s", ("org.kde.StatusNotifierItem",))
        return {k: v[1] for k, v in body[0].items()}

    def tooltip(self) -> str:
        return self.props()["ToolTip"][2]

    def has_dot(self) -> bool:
        """The red dot (openarcanum-alert-*.png) in the icon Qt exported:
        ARGB32 pixmaps, big-endian."""
        for w, h, data in self.props()["IconPixmap"]:
            red = 0
            for y in range(h // 2):
                for x in range(w // 2, w):
                    a, r, g, b = data[(y * w + x) * 4 : (y * w + x) * 4 + 4]
                    if a > 200 and abs(r - RED[0]) < 24 and abs(g - RED[1]) < 24 and abs(b - RED[2]) < 24:
                        red += 1
            if red >= max(4, w * h // 40):
                return True
        return False

    def menu(self) -> list[dict]:
        menu = self.props()["Menu"]
        _rev, (_id, _props, children) = self._call(menu, "com.canonical.dbusmenu", "GetLayout", "iias", (0, -1, []))
        out = []
        for _sig, (cid, props, _kids) in children:
            p = {k: v[1] for k, v in props.items()}
            if p.get("type") == "separator":
                continue
            out.append(
                {
                    "id": cid,
                    "label": p.get("label", "").replace("_", ""),
                    "enabled": p.get("enabled", True),
                    "checked": p.get("toggle-state") == 1,
                }
            )
        return out

    def entry(self, label: str) -> dict:
        entries = {e["label"]: e for e in self.menu()}
        assert label in entries, entries
        return entries[label]

    def click_menu(self, label: str) -> None:
        e = self.entry(label)
        assert e["enabled"], e
        menu = self.props()["Menu"]
        self._call(menu, "com.canonical.dbusmenu", "Event", "isvu", (e["id"], "clicked", ("i", 0), 0))

    def click_icon(self) -> None:
        """A left click on the tray icon."""
        self._call(ITEM, "org.kde.StatusNotifierItem", "Activate", "ii", (0, 0))

    def x11(self, cmd: str, pid: int):
        env = {"PATH": "/usr/bin:/bin", "DISPLAY": self.env["DISPLAY"], "XAUTHORITY": self.env["XAUTHORITY"]}
        r = subprocess.run(
            [sys.executable, "-I", str(HERE / "x11win.py"), cmd, str(pid)],
            capture_output=True,
            text=True,
            env=env,
            timeout=30,
        )
        assert r.returncode == 0, r.stderr
        return json.loads(r.stdout)

    def main_window(self, pid: int) -> dict | None:
        wins = [w for w in self.x11("list", pid) if w["title"] == "OpenArcanum"]
        return wins[0] if wins else None

    def notes(self) -> list[dict]:
        if not self.notes_file.exists():
            return []
        return [json.loads(ln) for ln in self.notes_file.read_text().splitlines()]


async def wait_for(what: str, fn, timeout: float = 20.0):
    deadline = time.monotonic() + timeout
    while True:
        v = await asyncio.to_thread(fn)
        if v:
            return v
        if time.monotonic() > deadline:
            raise AssertionError(f"never happened: {what}")
        await asyncio.sleep(0.2)


def log_of(home: Path) -> str:
    return (home / "log" / "client.log").read_text()


def test_tray_icon_close_to_tray_and_quit(tmp_path):
    """The tray icon is there from the start; closing the window hides it
    there (telling the user once how to quit); a click brings it back; the
    menu mutes, locks and quits, and quitting saves everything."""
    desk = Desktop(tmp_path)
    with network(tmp_path) as (_meta, _relay, community):
        node = community()

        async def main():
            home = tmp_path / "olive"
            olive = await desk.app(home, "olive").start()
            try:
                desk.connect()
                await desk.item()
                pid = olive.app_pid()
                assert desk.tooltip() == "OpenArcanum"
                assert [e["label"] for e in desk.menu()] == [
                    "Open OpenArcanum",
                    "Mute notifications",
                    "Lock",
                    "Quit OpenArcanum",
                ]
                # No profile yet: nothing to mute or lock.
                assert not desk.entry("Mute notifications")["enabled"] and not desk.entry("Lock")["enabled"]
                # The window carries the app icon in every size (the taskbar's).
                win = await wait_for("the main window", lambda: desk.main_window(pid))
                assert win["mapped"]
                assert {16, 32, 48, 64, 256} <= {w for w, h in win["icon_sizes"] if w == h}, win["icon_sizes"]
                assert not desk.has_dot()

                await create_profile(olive)
                await claim(olive, node.claim_code, "Tray Town", "Olive")
                await wait_for("tooltip: nothing new", lambda: desk.tooltip() == "OpenArcanum\nNothing new")
                assert desk.entry("Lock")["enabled"] and desk.entry("Mute notifications")["enabled"]

                # Settings: the option is on, and available here.
                await open_settings(olive)
                await olive.click(".settings nav .item", "Appearance")
                box = (
                    "document.querySelector('input[aria-label=\"Keep running in the tray when the window is closed\"]')"
                )
                assert await olive.js(f"{box}.checked && !{box}.disabled")
                await olive.click(".settings .close")

                # Close the window: it hides, the app keeps running, and the
                # user is told once how to quit.
                assert desk.x11("close", pid) == 1
                await wait_for("the window hidden", lambda: not desk.main_window(pid)["mapped"])
                assert olive.app_pid() == pid
                await wait_for("the one-time notice", lambda: desk.notes())
                assert [n["summary"] for n in desk.notes()] == ["OpenArcanum is still running"]
                assert "Quit OpenArcanum" in desk.notes()[0]["body"] and "Ctrl+Q" in desk.notes()[0]["body"]
                assert "tray_hint_shown = true" in (home / "config" / "preferences.toml").read_text()
                # A click on the tray icon shows it again.
                desk.click_icon()
                await wait_for("the window shown", lambda: desk.main_window(pid)["mapped"])
                # Closed again: hidden again, and no second notice.
                desk.x11("close", pid)
                await wait_for("hidden again", lambda: not desk.main_window(pid)["mapped"])
                await asyncio.sleep(1)
                assert len(desk.notes()) == 1
                desk.click_menu("Open OpenArcanum")
                await wait_for("shown from the menu", lambda: desk.main_window(pid)["mapped"])

                # Mute notifications: the Desktop notifications setting.
                desk.click_menu("Mute notifications")
                await wait_for("muted", lambda: desk.entry("Mute notifications")["checked"])
                assert (await olive.call("api", "get_settings"))["vault"]["notifications"] is False
                assert desk.tooltip().endswith("Notifications muted")
                desk.click_menu("Mute notifications")
                await wait_for("unmuted", lambda: not desk.entry("Mute notifications")["checked"])
                assert (await olive.call("api", "get_settings"))["vault"]["notifications"] is True

                # Lock from the tray: the window shows the lock screen.
                desk.click_menu("Lock")
                await olive.wait_js("document.querySelector('#lock input[type=password]')", "lock screen")
                await wait_for("tooltip: locked", lambda: desk.tooltip() == "OpenArcanum\nLocked")
                assert not desk.entry("Lock")["enabled"]
                await unlock(olive)
                await wait_for("unlocked again", lambda: desk.entry("Lock")["enabled"])

                # Quit from the tray menu: a normal exit that saves everything.
                await send_message(olive, "saved by quit")
                await olive.wait_text("#messages .mtext", "saved by quit")
                desk.click_menu("Quit OpenArcanum")
                took = await asyncio.to_thread(olive.wait_exit, pid, 20)
                assert took < 10, took
            finally:
                await olive.stop()
            log = log_of(home)
            assert "tray: icon shown" in log and "tray: window hidden" in log
            assert "exiting with status 0" in log, log[-2000:]
            assert "Traceback" not in log, log[-2000:]
            node.stop()
            core = Core(Paths.under(home), emit=Events())
            await core.unlock(PASS)
            cid = next(iter(core.vault.data["communities"]))
            texts = [m.get("text") for msgs in core.cache(cid).messages.values() for m in msgs]
            await core.shutdown()
            assert "saved by quit" in texts, texts

        run(main(), 300)


def test_tray_dot_follows_unread_mentions(tmp_path):
    """A mention while the user is elsewhere puts the red dot on the tray
    icon and says so in its tooltip; reading it clears both."""
    desk = Desktop(tmp_path)
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            olive = await desk.app(tmp_path / "olive", "olive").start()
            try:
                desk.connect()
                await desk.item()
                await create_profile(olive)
                invite = await claim(olive, node.claim_code, "Dot Town", "Olive")
                alice = Core(Paths.under(tmp_path / "alice"), emit=Events())
                await alice.create_profile("alice passphrase 1234")
                await alice.set_setting("maintainers", [meta])
                cid = (await alice.join({"invite": invite}, "new", "Alice"))["community"]
                ch = await general(alice, cid)
                await olive.js("st.view = 'dms'; render(); 0")
                await alice.send(cid, ch, "hey @Olive, look")
                await wait_for("the dot", desk.has_dot, 30)
                assert desk.tooltip() == "OpenArcanum\n1 unread mention"
                assert await olive.exists(".rail-item .badge.mention-badge")
                # Read it: open the community (its #general shows the mention).
                await olive.click(".rail-item[data-community]")
                await olive.wait_text("#messages .msg.mentioned", "hey")
                await olive.wait_gone(".rail-item .badge.mention-badge")
                await wait_for("the dot gone", lambda: not desk.has_dot(), 30)
                assert desk.tooltip() == "OpenArcanum\nNothing new"
                await alice.shutdown()
            finally:
                await olive.stop()

        run(main(), 300)


def test_preference_off_closing_quits(tmp_path):
    """With Keep running in the tray turned off, closing the window quits
    (cleanly) even though there is a tray."""
    desk = Desktop(tmp_path)

    async def main():
        home = tmp_path / "olive"
        olive = await desk.app(home, "olive").start()
        try:
            desk.connect()
            await desk.item()
            await create_profile(olive)
            await open_settings(olive)
            await olive.click(".settings nav .item", "Appearance")
            await olive.click('input[aria-label="Keep running in the tray when the window is closed"]')
            await olive.wait_text(".settings .saved", "Saved")
            assert "close_to_tray = false" in (home / "config" / "preferences.toml").read_text()
            pid = olive.app_pid()
            await wait_for("the main window", lambda: desk.main_window(pid))
            desk.x11("close", pid)
            await asyncio.to_thread(olive.wait_exit, pid, 20)
        finally:
            await olive.stop()
        log = log_of(home)
        assert "exiting with status 0" in log and "tray: window hidden" not in log
        assert desk.notes() == []

    run(main(), 200)


def test_without_a_tray_closing_quits_and_settings_say_why(tmp_path):
    """Plain Xvfb has no tray (like GNOME without a tray extension): no tray
    icon, the option is off and disabled with the reason, and closing the
    window quits normally."""
    ready = tmp_path / "x11-ready.json"
    helper = [sys.executable, "-c", PRINT_SESSION, str(ready)]

    async def main():
        home = tmp_path / "olive"
        olive = await App(home, name="olive", session_helper=helper, session_ready=ready).start()
        try:
            await create_profile(olive)
            await open_settings(olive)
            await olive.click(".settings nav .item", "Appearance")
            box = "document.querySelector('input[aria-label=\"Keep running in the tray when the window is closed\"]')"
            assert await olive.js(f"!{box}.checked && {box}.disabled")
            assert "no system tray" in await olive.js(
                f"document.getElementById({box}.getAttribute('aria-describedby')).textContent"
            )
            await olive.click(".settings .close")
            desk = Desktop(tmp_path)
            desk.env = json.loads(ready.read_text())
            pid = olive.app_pid()
            await wait_for("the main window", lambda: desk.main_window(pid))
            desk.x11("close", pid)
            await asyncio.to_thread(olive.wait_exit, pid, 20)
        finally:
            await olive.stop()
        log = log_of(home)
        assert "this desktop has no system tray" in log
        assert "exiting with status 0" in log and "Traceback" not in log

    run(main(), 200)


# Writes the session's DISPLAY and XAUTHORITY for the no-tray test (nothing
# on the bus).
PRINT_SESSION = (
    "import json, os, sys; p = sys.argv[1]; "
    "open(p + '.tmp', 'w').write(json.dumps({k: os.environ.get(k, '') for k in ('DISPLAY', 'XAUTHORITY')})); "
    "os.replace(p + '.tmp', p)"
)
