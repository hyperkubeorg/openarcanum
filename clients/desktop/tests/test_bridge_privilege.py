"""What script in the page can't do through the bridge, even with every API
method callable (docs/threat-model.md, "The page and the bridge"):

- name a file: every path Python reads or writes for the page was chosen in
  one of Python's own file dialogs (api.PickedPaths); a path the page makes
  up is refused before anything is read or written;
- act for the user without the passphrase: exporting a backup, linking or
  removing a device take the profile's passphrase again, checked in Python
  against the profile file (never a flag from the page), with the unlock
  backoff, so the page can't guess through them either.

Runs a real Core and JsApi; the window layer is a stand-in whose file
dialog "picks" what the test tells it to."""

from __future__ import annotations

import base64
import time

import pytest
from conftest import PASS

from openarcanum import core as core_mod
from openarcanum.api import JsApi, LoopThread, PickedPaths
from openarcanum.core import Core
from openarcanum.paths import Paths

PNG = base64.b64decode(
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
)


class Bus:
    def __init__(self):
        self.events: list[tuple[str, dict]] = []

    def push(self, event, data=None):
        self.events.append((event, data))

    def mark_ready(self):
        pass


class Gui:
    """The file dialog answers with `answer` (what the user picks)."""

    def __init__(self, bus: Bus):
        self.bus = bus
        self.answer: list[str] = []
        self.opened: list[tuple] = []

    def open_file_dialog(self, request_id, kind, save, default_name="", start="", grant=None):
        self.opened.append((kind, save, default_name, start))
        if grant is not None:
            grant(self.answer)
        self.bus.push("dialog", {"id": request_id, "path": self.answer[0] if self.answer else "", "paths": self.answer})

    def __getattr__(self, name):
        return lambda *a, **k: None


@pytest.fixture
def bridge(tmp_path):
    loop = LoopThread()
    core = loop.call(lambda: Core(Paths.under(tmp_path / "home"), emit=lambda *a: None))
    loop.run(core.create_profile(PASS, "laptop"), 60)
    bus = Bus()
    gui = Gui(bus)
    api = JsApi(loop, core, bus, gui)
    try:
        yield api, core, gui, tmp_path
    finally:
        loop.run(core.shutdown(), 30)
        loop.close()


def refused(res: dict, why: str = "file dialog") -> bool:
    return res["ok"] is False and why in res["error"]


def test_picked_paths_only_what_a_dialog_returned():
    p = PickedPaths()
    p.grant("open", ["/home/u/a.png"])
    p.grant("save", ["/home/u/out.fbak"])
    assert p.granted("open", "/home/u/a.png")
    assert not p.granted("save", "/home/u/a.png")  # read, not write
    assert not p.granted("open", "/home/u/out.fbak")
    for bad in ("", None, 3, ["/home/u/a.png"], "/home/u/a.png/../../.ssh/id_ed25519", "/home/u/A.png"):
        assert not p.granted("open", bad)
    for i in range(PickedPaths.LIMIT + 10):
        p.grant("open", [f"/f{i}"])
    assert not p.granted("open", "/f0") and p.granted("open", f"/f{PickedPaths.LIMIT + 9}")


def test_the_page_cant_name_a_file(bridge):
    api, _core, gui, tmp = bridge
    secret = tmp / "id_ed25519"
    secret.write_bytes(PNG)  # readable, and a valid image
    target = tmp / "written"
    # Reads: refused, nothing read.
    assert refused(api.read_image(str(secret)))
    assert refused(api.set_avatar("cid", str(secret)))
    assert refused(api.upload("cid", 1, str(secret)))
    assert refused(api.send("cid", 1, "x", 0, [str(secret)]))
    assert refused(api.dm_send("gid", "x", "", [str(secret)]))
    assert refused(api.dm_set_icon("gid", str(secret)))
    assert refused(api.emoji_upload("cid", "e", str(secret)))
    assert refused(api.restore_backup(str(secret), "a", "b"))
    # Writes: refused, nothing written.
    assert refused(api.save_file("cid", {"name": "x"}, str(target)))
    assert refused(api.dm_save_file("gid", "eid", "00", str(target)))
    assert refused(api.save_community_binary(str(target)))
    assert refused(api.export_backup(str(target), "a new backup passphrase", PASS))
    assert refused(api.set_setting("data_dir", str(tmp / "elsewhere")))
    assert not target.exists() and not (tmp / "elsewhere").exists()
    # An opened file isn't a place to write, nor a saved one a file to read.
    gui.answer = [str(secret)]
    api.pick_file("1", "image")
    assert refused(api.save_community_binary(str(secret)))
    assert api.read_image(str(secret)) == {"ok": True, "value": base64.b64encode(PNG).decode()}
    gui.answer = [str(target)]
    api.pick_save("2", "any", "x")
    assert refused(api.read_image(str(target)))


def test_a_typed_path_goes_through_a_dialog(bridge):
    api, _core, gui, tmp = bridge
    typed = str(tmp / "typed.png")
    (tmp / "typed.png").write_bytes(PNG)
    gui.answer = [typed]  # the user confirms what the dialog opened on
    api.pick_file("1", "image", typed)
    assert gui.opened[-1] == ("image", False, "", typed)
    assert api.read_image(typed)["ok"]
    # Asked again for a path already chosen: answered without a dialog.
    n = len(gui.opened)
    api.pick_file("2", "image", typed)
    assert len(gui.opened) == n
    assert api._bus.events[-1] == ("dialog", {"id": "2", "path": typed, "paths": [typed]})


def test_backup_export_takes_the_passphrase(bridge):
    api, _core, gui, tmp = bridge
    out = tmp / "backup.fbak"
    gui.answer = [str(out)]
    api.pick_save("1", "backup", "openarcanum-backup.fbak", str(out))
    backup_pass = "a long new backup passphrase"
    assert refused(api.export_backup(str(out), backup_pass), "Enter your passphrase")
    assert refused(api.export_backup(str(out), backup_pass, "not the passphrase"), "wrong")
    assert not out.exists()
    # Right away: the backoff refuses even the right passphrase.
    assert refused(api.export_backup(str(out), backup_pass, PASS), "Wait")
    time.sleep(1.1)
    assert api.export_backup(str(out), backup_pass, PASS)["ok"]
    assert out.exists()


def test_saved_ui_state_is_bounded(bridge):
    """state.bin keeps drafts and the like: the page can't grow it without
    bound, in the number of keys or the size of one value."""
    api, core, _gui, _tmp = bridge
    assert api.ui_state_set("draft:1", "x" * 1000)["ok"]
    assert api.ui_state_get("draft:1")["value"] == "x" * 1000
    assert refused(api.ui_state_set("draft:1", "x" * (64 * 1024 + 1)), "saved state")
    assert refused(api.ui_state_set("k" * 201, "x"), "saved state")
    assert api.ui_state_get("draft:1")["value"] == "x" * 1000
    for i in range(core_mod.MAX_UI_STATE_KEYS - 1):
        assert api.ui_state_set(f"k{i}", i)["ok"]
    assert refused(api.ui_state_set("one too many", 1), "saved state")
    # An existing key can still change, and removing one makes room.
    assert api.ui_state_set("draft:1", "y")["ok"]
    assert api.ui_state_set("draft:1", None)["ok"]
    assert api.ui_state_set("one too many", 1)["ok"]
    assert len(core.state.data) == core_mod.MAX_UI_STATE_KEYS


def test_devices_take_the_passphrase(bridge, monkeypatch):
    api, core, _gui, _tmp = bridge
    called = []

    async def remove(device_id):
        called.append(("remove", device_id))
        return {"failures": []}

    async def enter(code):
        called.append(("enter", code))
        return {}

    monkeypatch.setattr(core.devices, "remove", remove)
    monkeypatch.setattr(core.devices, "existing_enter_code", enter)
    assert refused(api.remove_device("dev"), "Enter your passphrase")
    assert refused(api.link_enter_code("1-a-b-c"), "Enter your passphrase")
    assert refused(api.link_enter_code("1-a-b-c", "guess"), "wrong")
    time.sleep(1.1)
    assert refused(api.remove_device("dev", "guess"), "wrong")
    assert called == []  # nothing contacted, nothing removed
    time.sleep(2.1)
    assert api.link_enter_code("1-a-b-c", PASS)["ok"]
    assert api.remove_device("dev", PASS)["ok"]
    assert called == [("enter", "1-a-b-c"), ("remove", "dev")]


def test_every_passphrase_check_counts_toward_the_backoff(bridge):
    """set_remember and change_passphrase check the passphrase too: wrong
    guesses there wait like wrong unlocks."""
    api, core, _gui, _tmp = bridge
    assert refused(api.set_remember(True, "guess one"), "wrong")
    assert refused(api.change_passphrase("guess two", "a fine new passphrase here"), "Wait")
    assert core.guard.remaining() > 0


class _DialogWindow:
    def __init__(self, answer):
        self.answer = answer
        self.calls: list[tuple] = []

    def create_file_dialog(self, dialog_type, **kw):
        self.calls.append((dialog_type, kw))
        return self.answer


@pytest.mark.parametrize(
    "name",
    ["../../.config/autostart/x.desktop", "/etc/passwd", "invoice\u202efdp.exe", "C:\\Windows\\evil.dll", "CON .txt"],
)
def test_the_save_dialog_suggests_only_a_safe_name_in_the_save_folder(monkeypatch, tmp_path, name):
    """A community file's name comes from whoever posted it: the save dialog
    opens in the save folder on a cleaned-up name, never on a path in it."""
    from openarcanum import app as app_mod
    from openarcanum.dmfiles import safe_name

    monkeypatch.setattr("openarcanum.paths.save_folder", lambda: tmp_path)
    monkeypatch.delenv("OPENARCANUM_TEST_FILE_DIALOGS", raising=False)
    bus = Bus()
    gui = app_mod.Gui(_DialogWindow([str(tmp_path / "chosen")]), bus, smoke=False, debug=False)
    granted = []
    gui.open_file_dialog("1", "any", save=True, default_name=name, grant=granted.extend)
    for _ in range(100):
        if bus.events:
            break
        time.sleep(0.02)
    ((_kind, kw),) = gui.window.calls
    suggested = kw["save_filename"]
    assert suggested == str(tmp_path / safe_name(name))
    leaf = suggested[len(str(tmp_path)) + 1 :]
    assert "/" not in leaf and "\\" not in leaf
    assert granted == [str(tmp_path / "chosen")]
    assert bus.events == [
        ("dialog", {"id": "1", "path": str(tmp_path / "chosen"), "paths": [str(tmp_path / "chosen")]})
    ]
