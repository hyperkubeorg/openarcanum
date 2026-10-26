"""The tray icon's logic (openarcanum/tray.py) and closing the window to the
tray (app.py Gui.on_closing), without a desktop. The real tray, driven over
D-Bus under Xvfb, is tests/test_ui_tray.py."""

import json
import os
import subprocess
import sys
import textwrap

import pytest

from openarcanum import settings as settings_mod
from openarcanum import tray
from openarcanum.app import Gui
from openarcanum.settings import Preferences as Prefs
from openarcanum.settings import SettingError

# ---------------------------------------------------------------- state -> icon and tooltip


def test_nothing_waiting_no_dot():
    alert, tip = tray.tray_view(tray.Attention(), "unlocked", muted=False)
    assert not alert
    assert tip == "OpenArcanum\nNothing new"


@pytest.mark.parametrize(
    ("attention", "line"),
    [
        (tray.Attention(mentions=1), "1 unread mention"),
        (tray.Attention(mentions=3), "3 unread mentions"),
        (tray.Attention(direct=1), "1 unread direct message"),
        (tray.Attention(direct=2), "2 unread direct messages"),
        (tray.Attention(review=4), "4 waiting for review"),
    ],
)
def test_each_kind_of_waiting_shows_the_dot_and_says_what(attention, line):
    alert, tip = tray.tray_view(attention, "unlocked", muted=False)
    assert alert
    assert tip.splitlines() == ["OpenArcanum", line]


def test_everything_waiting_is_listed_in_order():
    alert, tip = tray.tray_view(tray.Attention(2, 1, 5), "unlocked", muted=True)
    assert alert
    assert tip.splitlines() == [
        "OpenArcanum",
        "2 unread mentions",
        "1 unread direct message",
        "5 waiting for review",
        "Notifications muted",
    ]


def test_locked_or_no_profile_shows_nothing_waiting():
    a = tray.Attention(2, 1, 5)
    assert tray.tray_view(a, "locked", muted=True) == (False, "OpenArcanum\nLocked")
    assert tray.tray_view(a, "no_profile", muted=False) == (False, "OpenArcanum")


def test_counts_from_the_page_are_cleaned():
    assert tray.Attention.clean(2, 0, 1) == tray.Attention(2, 0, 1)
    assert tray.Attention.clean(-1, "3", None) == tray.Attention(0, 0, 0)
    assert tray.Attention.clean(True, float("nan"), 2.0) == tray.Attention(0, 0, 2)
    assert tray.Attention.clean(10**12, 0, 0).mentions == tray.MAX_COUNT


def test_icon_files_exist():
    for alert in (False, True):
        files = tray.icon_files(alert)
        assert files and all(f.exists() for f in files)


def test_hidden_hint_says_how_to_quit():
    title, body = tray.hidden_hint()
    assert "still running" in title
    assert "Quit OpenArcanum" in body and tray.quit_keys() in body


# ---------------------------------------------------------------- the preference


def test_close_to_tray_preference(tmp_path):
    s = settings_mod.BY_KEY["close_to_tray"]
    assert s.store == "prefs" and s.default is True
    assert settings_mod.validate("close_to_tray", False) is False
    with pytest.raises(SettingError):
        settings_mod.validate("close_to_tray", "yes")
    p = Prefs(tmp_path / "preferences.toml")
    assert p.values["close_to_tray"] is True
    p.set("close_to_tray", False)
    assert Prefs(tmp_path / "preferences.toml").values["close_to_tray"] is False
    # A bad value in the file falls back to the default.
    (tmp_path / "preferences.toml").write_text('close_to_tray = "sometimes"\n')
    assert Prefs(tmp_path / "preferences.toml").values["close_to_tray"] is True


def test_one_time_notices_are_remembered(tmp_path):
    p = Prefs(tmp_path / "preferences.toml")
    assert "tray_hint_shown" not in p.shown
    p.mark_shown("tray_hint_shown")
    again = Prefs(tmp_path / "preferences.toml")
    assert "tray_hint_shown" in again.shown
    with pytest.raises(ValueError):
        p.mark_shown("anything_else")
    (tmp_path / "preferences.toml").write_text("tray_hint_shown = 3\n")
    assert "tray_hint_shown" not in Prefs(tmp_path / "preferences.toml").shown


# ---------------------------------------------------------------- closing the window


class FakeView:
    def __init__(self):
        self.hidden = False

    def hide(self):
        self.hidden = True


class FakeTray:
    def __init__(self, visible=True):
        self._visible = visible

    def visible(self):
        return self._visible


class FakeCore:
    def __init__(self, prefs, hosting=0):
        self.prefs = prefs
        self.hosting = hosting

    def hosting_running(self):
        return self.hosting


class FakeBus:
    def __init__(self):
        self.events = []

    def push(self, event, data):
        self.events.append((event, data))


def closing_gui(tmp_path, monkeypatch, *, tray_here=True, pref=True):
    from qtpy.QtWidgets import QSystemTrayIcon

    monkeypatch.setattr(QSystemTrayIcon, "isSystemTrayAvailable", staticmethod(lambda: tray_here))
    prefs = Prefs(tmp_path / "preferences.toml")
    prefs.set("close_to_tray", pref)
    gui = Gui(None, bus=None, smoke=False, debug=False)
    gui.core = FakeCore(prefs)
    gui.tray = FakeTray() if tray_here else None
    view = FakeView()
    monkeypatch.setattr(gui, "_browser_view", lambda: view)
    told = []
    monkeypatch.setattr(gui, "notify", lambda title, body: told.append((title, body)))
    return gui, view, told, prefs


def test_closing_hides_to_the_tray_and_tells_once(tmp_path, monkeypatch):
    gui, view, told, prefs = closing_gui(tmp_path, monkeypatch)
    assert gui.on_closing() is False  # pywebview keeps the window
    assert view.hidden and not gui.quitting
    assert told == [tray.hidden_hint()]
    assert "tray_hint_shown" in Prefs(prefs.path).shown
    view.hidden = False
    assert gui.on_closing() is False
    assert view.hidden and len(told) == 1  # told once, ever


def test_quitting_closes_for_real(tmp_path, monkeypatch):
    gui, view, told, _prefs = closing_gui(tmp_path, monkeypatch)
    gui.quitting = True  # Quit OpenArcanum, Ctrl+Q, the system ending the session
    assert gui.on_closing() is None
    assert not view.hidden and told == []


def test_preference_off_closes_and_quits(tmp_path, monkeypatch):
    gui, view, told, _prefs = closing_gui(tmp_path, monkeypatch, pref=False)
    assert gui.on_closing() is None
    assert gui.quitting and not view.hidden and told == []


def test_no_tray_closes_and_quits(tmp_path, monkeypatch):
    gui, view, _told, _prefs = closing_gui(tmp_path, monkeypatch, tray_here=False)
    assert gui.on_closing() is None
    assert gui.quitting and not view.hidden
    status = gui.tray_status()
    assert status["available"] is False and "no system tray" in status["reason"]


def test_tray_that_went_away_closes_and_quits(tmp_path, monkeypatch):
    """The panel was closed after the app started: closing must not leave
    an app nobody can reach."""
    gui, view, _told, _prefs = closing_gui(tmp_path, monkeypatch)
    gui.tray = FakeTray(visible=False)
    assert gui.on_closing() is None and not view.hidden


def test_quit_marks_quitting():
    gui = Gui(None, bus=None, smoke=False, debug=False)
    gui.quit()
    assert gui.quitting


def hosting_gui(tmp_path, monkeypatch, **kw):
    gui, view, _told, prefs = closing_gui(tmp_path, monkeypatch, **kw)
    gui.core = FakeCore(prefs, hosting=2)
    gui.bus = FakeBus()
    raised = []
    monkeypatch.setattr(gui, "_raise_window", lambda: raised.append(True))
    destroyed = []
    monkeypatch.setattr(gui, "_quit_now", lambda: destroyed.append(True))
    return gui, view, raised, destroyed


def test_quitting_asks_first_while_communities_are_hosted(tmp_path, monkeypatch):
    """Quit OpenArcanum and Ctrl+Q with hosted community servers running: the
    window comes up and the page asks; nothing quits until it's confirmed."""
    gui, _view, raised, destroyed = hosting_gui(tmp_path, monkeypatch)
    gui._quit_asked()
    assert raised and not destroyed and not gui.quitting
    assert gui.bus.events == [("confirm_quit", {"count": 2})]
    # Nothing hosted any more: quits at once.
    gui.core.hosting = 0
    gui._quit_asked()
    assert destroyed


def test_closing_without_a_tray_asks_while_communities_are_hosted(tmp_path, monkeypatch):
    gui, view, _raised, _destroyed = hosting_gui(tmp_path, monkeypatch, tray_here=False)
    assert gui.on_closing() is False  # kept open: the page asks
    assert not gui.quitting and not view.hidden
    assert gui.bus.events[-1][0] == "confirm_quit"
    # "Quit anyway" in the page.
    monkeypatch.setattr(gui, "quit", lambda: setattr(gui, "quitting", True))
    gui.confirm_quit()
    assert gui.quit_confirmed and gui.quitting
    assert gui.on_closing() is None


def test_closing_to_the_tray_keeps_hosting_without_asking(tmp_path, monkeypatch):
    """With a tray, closing the window only hides it: hosted communities keep
    running, so there is nothing to ask."""
    gui, view, _raised, _destroyed = hosting_gui(tmp_path, monkeypatch)
    assert gui.on_closing() is False
    assert view.hidden and gui.bus.events == []


def test_the_system_ending_the_session_never_asks(tmp_path, monkeypatch):
    gui, _view, _raised, _destroyed = hosting_gui(tmp_path, monkeypatch, tray_here=False)
    gui.quitting = True  # SIGTERM, logout, the macOS app menu
    assert gui.on_closing() is None and gui.bus.events == []


# ---------------------------------------------------------------- the Qt tray (offscreen)

QT_TRAY = textwrap.dedent(
    """
    import json, sys
    from qtpy.QtWidgets import QApplication
    from openarcanum import tray

    app = QApplication([])
    state = {"profile": "no_profile", "muted": False}
    calls = []
    t = tray.Tray(tray.TrayActions(
        show_window=lambda: calls.append("show"),
        set_muted=lambda m: calls.append(("muted", m)),
        lock=lambda: calls.append("lock"),
        quit=lambda: calls.append("quit"),
        profile=lambda: state["profile"],
        muted=lambda: state["muted"],
    ))
    out = {}
    out["labels"] = [a.text() for a in t.menu.actions() if not a.isSeparator()]
    out["first"] = (t.icon.toolTip(), t.mute_action.isEnabled(), t.lock_action.isEnabled())
    state["profile"] = "unlocked"
    t.set_attention(tray.Attention(mentions=2))
    out["mention"] = (t.alert, t.icon.toolTip(), t.mute_action.isEnabled(), t.lock_action.isEnabled())
    plain = t._icons[False].pixmap(32, 32).toImage()
    shown = t.icon.icon().pixmap(32, 32).toImage()
    out["icon_changed"] = plain != shown
    t.set_attention(tray.Attention())
    out["read"] = (t.alert, t.icon.toolTip(), t.icon.icon().pixmap(32, 32).toImage() == plain)
    t.mute_action.trigger()
    t.lock_action.trigger()
    t.open_action.trigger()
    t._activated(t.icon.ActivationReason.Trigger)
    t._activated(t.icon.ActivationReason.Context)
    t.quit_action.trigger()
    out["calls"] = calls
    state["muted"] = True
    t.refresh()
    out["muted"] = (t.mute_action.isChecked(), t.icon.toolTip())
    state["profile"] = "locked"
    t.refresh()
    out["locked"] = (t.icon.toolTip(), t.mute_action.isEnabled(), t.lock_action.isEnabled())
    print(json.dumps(out))
    """
)


def test_qt_tray_follows_state_and_runs_actions():
    r = subprocess.run(
        [sys.executable, "-c", QT_TRAY],
        capture_output=True,
        text=True,
        timeout=60,
        env=dict(os.environ, QT_QPA_PLATFORM="offscreen"),
    )
    assert r.returncode == 0, r.stderr
    out = json.loads(r.stdout.strip().splitlines()[-1])
    assert out["labels"] == ["Open OpenArcanum", "Mute notifications", "Lock", "Quit OpenArcanum"]
    assert out["first"] == ["OpenArcanum", False, False]
    assert out["mention"] == [True, "OpenArcanum\n2 unread mentions", True, True]
    assert out["icon_changed"] is True
    assert out["read"] == [False, "OpenArcanum\nNothing new", True]
    assert out["calls"] == [["muted", True], "lock", "show", "show", "quit"]
    assert out["muted"] == [True, "OpenArcanum\nNothing new\nNotifications muted"]
    assert out["locked"] == ["OpenArcanum\nLocked", False, False]
