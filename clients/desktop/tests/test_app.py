"""App-level checks: smoke launch, --home isolation, the storage guard,
engine flags, and exit with a dead window."""

import os
import stat
import subprocess
import sys
import textwrap
import time
from pathlib import Path

import pytest
from conftest import GUI, headless

from openarcanum import guard
from openarcanum.paths import Paths, atomic_write, ensure_dir

# Windows can open here (conftest.GUI); tests that open them are marked
# `gui`: skipped locally without a display, failed in CI.
HEADLESS = GUI


def os_owned(f: Path) -> bool:
    """A file in the home folder that Windows itself keeps writing while the
    user is logged in (the registry hive NTUSER.DAT and its logs): hidden
    and system, never an app's."""
    if os.name != "nt":
        return False
    hidden_or_system = stat.FILE_ATTRIBUTE_HIDDEN | stat.FILE_ATTRIBUTE_SYSTEM
    return bool(f.stat().st_file_attributes & hidden_or_system)


# Linux runs the engine with its PipeWire screen capturer (Wayland screen
# sharing; app.with_pipewire_capture), in the one --enable-features.
PIPEWIRE = ["--enable-features=WebRTCPipeWireCapturer"] if sys.platform.startswith("linux") else []


def features(*names: str) -> str:
    return "--enable-features=" + ",".join(["WebRTCPipeWireCapturer", *names] if PIPEWIRE else names)


@pytest.mark.gui
def test_smoke_writes_nothing_outside_home(tmp_path):
    home = Path.home()
    marker = tmp_path / "marker"
    marker.touch()
    time.sleep(1.1)
    run_home = tmp_path / "home"
    p = subprocess.run(
        headless([sys.executable, "-m", "openarcanum", "--smoke", "--home", str(run_home)]),
        capture_output=True,
        text=True,
        timeout=150,
    )
    assert p.returncode == 0, p.stderr[-2000:]
    log = (run_home / "log" / "client.log").read_text(encoding="utf-8")
    assert "smoke: UI loaded" in log
    assert (run_home / "config" / "preferences.toml").exists()
    assert not (home / ".pywebview").exists() or (home / ".pywebview").stat().st_mtime < marker.stat().st_mtime
    # Where the app would write if it ignored --home: its own folders on this
    # system, pywebview's folder, the user's documents and downloads, and
    # files directly in the home folder. (All of ~/Library or ~/.config is
    # too much: other programs write there while this runs.)
    import platformdirs

    from openarcanum.paths import Paths

    own = Paths.default()
    roots = [own.config, own.data, own.cache, own.log, home / ".pywebview"]
    roots += [Path(platformdirs.user_documents_dir()), Path(platformdirs.user_downloads_dir())]
    newer = [
        str(f) for f in home.iterdir() if f.is_file() and f.stat().st_mtime > marker.stat().st_mtime and not os_owned(f)
    ]
    for root, _dirs, files in (w for r in roots if r.is_dir() for w in os.walk(r)):
        for f in files:
            fp = os.path.join(root, f)
            try:
                if os.stat(fp).st_mtime > marker.stat().st_mtime:
                    newer.append(fp)
            except OSError:
                pass
    assert newer == [], f"smoke run wrote outside --home: {newer[:10]}"


@pytest.mark.skipif(os.name != "posix", reason="file modes")
def test_the_apps_folders_are_private_but_a_named_home_keeps_its_mode(tmp_path):
    """`--home DIR` makes the app's own folders readable by the user only,
    but a directory the user named (`--home .`) keeps its mode, as does a
    folder a file is saved into."""
    home = tmp_path / "home"
    home.mkdir()
    home.chmod(0o755)
    ensure_dir(home)
    paths = Paths.under(home)
    paths.ensure()
    assert stat.S_IMODE(home.stat().st_mode) == 0o755
    for d in (paths.config, paths.data, paths.cache, paths.log):
        assert stat.S_IMODE(d.stat().st_mode) == 0o700
    # The app's own folders, even when they exist already.
    paths.data.chmod(0o755)
    paths.ensure()
    assert stat.S_IMODE(paths.data.stat().st_mode) == 0o700
    # A new folder is private; a write into an existing one leaves it alone.
    ensure_dir(tmp_path / "fresh")
    assert stat.S_IMODE((tmp_path / "fresh").stat().st_mode) == 0o700
    shared = tmp_path / "shared"
    shared.mkdir()
    shared.chmod(0o755)
    atomic_write(shared / "f", b"x")
    assert stat.S_IMODE(shared.stat().st_mode) == 0o755
    assert stat.S_IMODE((shared / "f").stat().st_mode) == 0o600


def test_storage_guard_passes_on_the_real_ui():
    assert guard.scan_ui() == []
    assert guard.scan_window() == []


def test_storage_guard_catches_storage_use(tmp_path):
    ui = tmp_path / "ui"
    ui.mkdir()
    (ui / "a.js").write_text("const x = window.localStorage.getItem('k');\n")
    (ui / "b.js").write_text("document .cookie = 'a=b';\n")
    (ui / "c.js").write_text("const db = indexedDB.open('x');\n")
    (ui / "ok.js").write_text("// talks to Python only\n")
    problems = guard.scan_ui(ui)
    assert len(problems) == 3
    app = tmp_path / "app.py"
    app.write_text("import webview\nwebview.start(gui='qt')\n")
    assert any("private_mode=True" in p for p in guard.scan_window(app))
    app.write_text("import webview\nwebview.start(gui='qt', private_mode=False)\n")
    assert guard.scan_window(app)
    app.write_text("import webview\nwebview.start(gui='qt', private_mode=True, storage_path='/x')\n")
    assert any("storage_path" in p for p in guard.scan_window(app))
    app.write_text("import webview\nwebview.start(gui='qt', private_mode=True, debug=True)\n")
    assert any("debug" in p for p in guard.scan_window(app))


def test_bus_push_never_blocks_exit(tmp_path):
    """A window whose evaluate_js never returns must not keep the process
    alive, even when pushes come from non-daemon threads like pywebview's
    bridge threads."""
    script = tmp_path / "hang.py"
    script.write_text(
        textwrap.dedent(
            """
            import threading, time
            from openarcanum.bus import Bus

            class DeadWindow:
                def evaluate_js(self, payload):
                    time.sleep(3600)  # the GUI loop that would answer is gone

            bus = Bus()
            bus.set_window(DeadWindow())
            bus.mark_ready()
            t = threading.Thread(target=lambda: [bus.push("message", {"n": i}) for i in range(5)])
            t.start()
            t.join(2)
            assert not t.is_alive(), "push blocked the caller"
            """
        )
    )
    p = subprocess.run([sys.executable, str(script)], capture_output=True, text=True, timeout=10)
    assert p.returncode == 0, p.stderr


def test_bus_holds_events_until_ready():
    from openarcanum.bus import Bus

    got = []

    class W:
        def evaluate_js(self, js):
            got.append(js)

    bus = Bus()
    bus.set_window(W())
    bus.push("a", 1)
    bus.push("b", {"x": " "})
    time.sleep(0.2)
    assert got == []
    bus.mark_ready()
    deadline = time.time() + 2
    while len(got) < 2 and time.time() < deadline:
        time.sleep(0.02)
    assert len(got) == 2 and '"event": "a"' in got[0] and "\\u2028" in got[1]


def test_engine_flags_are_hardened(monkeypatch):
    """pywebview's --use-fake-ui-for-media-stream (silent media grants) and
    --no-sandbox never reach the engine; other flags pass through."""
    import webview.platforms.qt as qt_platform

    from openarcanum import app

    monkeypatch.setenv(
        app.ENGINE_FLAGS, "--use-fake-device-for-media-stream --no-sandbox --use-fake-ui-for-media-stream"
    )
    monkeypatch.setattr(qt_platform, "environ_append", qt_platform.environ_append)
    app.harden_engine_flags(sandbox=True)
    assert os.environ[app.ENGINE_FLAGS].split() == ["--use-fake-device-for-media-stream", *PIPEWIRE]
    # What pywebview appends later is filtered too (and its features merged).
    qt_platform.environ_append(
        app.ENGINE_FLAGS, "--use-fake-ui-for-media-stream", "--enable-features=X", "--no-sandbox"
    )
    assert os.environ[app.ENGINE_FLAGS].split() == ["--use-fake-device-for-media-stream", features("X")]


def test_engine_runs_without_sandbox_only_when_it_cant(monkeypatch):
    import webview.platforms.qt as qt_platform

    from openarcanum import app

    monkeypatch.delenv(app.ENGINE_FLAGS, raising=False)
    monkeypatch.setattr(qt_platform, "environ_append", qt_platform.environ_append)
    app.harden_engine_flags(sandbox=False)
    assert os.environ[app.ENGINE_FLAGS].split() == ["--no-sandbox", *PIPEWIRE]
    qt_platform.environ_append(app.ENGINE_FLAGS, "--use-fake-ui-for-media-stream")
    assert "--use-fake-ui-for-media-stream" not in os.environ[app.ENGINE_FLAGS]
    # The probe agrees with the system: on Linux without the AppArmor
    # restriction (Ubuntu 24.04 turns it on, CI runners included) the
    # sandbox can start.
    restricted = Path("/proc/sys/kernel/apparmor_restrict_unprivileged_userns")
    if sys.platform.startswith("linux") and not (restricted.exists() and restricted.read_text().strip() == "1"):
        assert app.linux_sandbox_available()


def test_sandbox_decision_learns_from_a_failed_start(tmp_path, monkeypatch):
    """The sandbox is tried; a sandboxed start that never reached the UI
    turns it off for this version (the engine aborts where the system can't
    run it); an update tries again; the environment's own choice wins."""
    from openarcanum import app

    f = tmp_path / app.SANDBOX_STATE
    monkeypatch.delenv("QTWEBENGINE_DISABLE_SANDBOX", raising=False)
    monkeypatch.setattr(app, "linux_sandbox_available", lambda: True)
    assert app.decide_sandbox(f, "1.0") == (True, "")
    assert f.read_text().split() == ["1", "trying", "1.0"]  # format 1 (test_formats.py)
    # The UI loaded: it works; the next start keeps it on.
    app.sandbox_started(f, "1.0")
    assert app.decide_sandbox(f, "1.0") == (True, "")
    # This start crashed before the UI: the next one runs without it.
    on, why = app.decide_sandbox(f, "1.0")
    assert not on and "couldn't start with its sandbox" in why
    assert not app.decide_sandbox(f, "1.0")[0]  # and stays off for 1.0
    assert app.decide_sandbox(f, "1.1") == (True, "")  # an update tries again
    monkeypatch.setenv("QTWEBENGINE_DISABLE_SANDBOX", "1")
    on, why = app.decide_sandbox(f, "1.1")
    assert not on and "QTWEBENGINE_DISABLE_SANDBOX" in why
    monkeypatch.delenv("QTWEBENGINE_DISABLE_SANDBOX")
    monkeypatch.setattr(app, "linux_sandbox_available", lambda: False)
    f.unlink()
    assert not app.decide_sandbox(f, "1.1")[0]


BRIDGE_HANG = """
import time
import webview.util

from openarcanum.app import daemon_bridge_threads

{setup}


class GoneWindow:
    # The Qt loop that would answer evaluate_js has stopped.
    _functions = {{"install": lambda: {{"ok": True}}}}
    _js_api = None

    def evaluate_js(self, script):
        time.sleep(3600)


webview.util.js_bridge_call(GoneWindow(), "install", [], "1")
time.sleep(0.5)
"""


@pytest.mark.parametrize("fixed", [True, False])
def test_bridge_call_left_returning_to_a_closed_window_does_not_block_exit(fixed):
    """A bridge call that finishes after the window closed ("Restart and
    install" closes it itself) waits forever in pywebview's evaluate_js; the
    process must still exit. Without daemon_bridge_threads it hangs (the
    unfixed case proves the test reproduces the hang)."""
    script = BRIDGE_HANG.format(setup="daemon_bridge_threads()" if fixed else "")
    try:
        # Well over a cold start (Qt loads for the import), far under the hang.
        p = subprocess.run([sys.executable, "-c", script], capture_output=True, text=True, timeout=20)
    except subprocess.TimeoutExpired:
        assert not fixed, "the process hung at exit with a bridge thread stuck in evaluate_js"
        return
    assert fixed, "the unfixed case exited, so this test no longer reproduces the hang"
    assert p.returncode == 0, p.stderr[-2000:]


def test_restart_after_an_update_keeps_options_but_not_the_link(tmp_path):
    """The new version starts on the same profile with the same options; an
    invite link the app was opened with would bring the Join dialog back."""
    from test_instance import LINK

    from openarcanum import app

    home = tmp_path / "home"
    args = app.parse_args(
        ["--home", str(home), "--debug", "--verbose", "--maintainer", "https://m.example.invalid", LINK]
    )
    assert app.restart_args(args) == [
        "--home",
        str(home.resolve()),
        "--maintainer",
        "https://m.example.invalid",
        "--verbose",
    ]
    assert app.restart_args(app.parse_args([LINK])) == []


def test_a_link_after_double_dash_cant_carry_options():
    """The OS passes the link after `--` (openarcanum.iss, openarcanum.desktop):
    a crafted link with quotes and spaces, split by Windows into several
    arguments, can't add options such as --home or --debug."""
    from test_instance import LINK

    from openarcanum import app

    assert app.parse_args(["--", LINK]).link == LINK
    with pytest.raises(SystemExit):
        app.parse_args(["--", "openarcanum://invite/x", "--home", "\\\\host\\share", "--debug", ""])
    args = app.parse_args(["--", "--debug"])
    assert args.link == "--debug" and not args.debug  # a positional, then refused as a link


def test_engine_debugging_needs_an_explicit_option(monkeypatch):
    """A DevTools port or origin-rule switch inherited from the environment
    is dropped unless the app was started with --debug or
    --allow-remote-debugging (any local program could drive the page)."""
    import webview.platforms.qt as qt_platform

    from openarcanum import app

    unsafe = "--remote-debugging-port=9222 --disable-web-security --allow-file-access-from-files --user-data-dir=/x"
    original = qt_platform.environ_append
    for allowed in (False, True):
        monkeypatch.setattr(qt_platform, "environ_append", original)
        monkeypatch.setenv(app.ENGINE_FLAGS, f"--use-fake-device-for-media-stream {unsafe}")
        monkeypatch.setenv(app.REMOTE_DEBUGGING_ENV, "127.0.0.1:9222")
        app.harden_engine_flags(sandbox=True, allow_debugging=allowed)
        qt_platform.environ_append(app.ENGINE_FLAGS, "--remote-debugging-pipe", "--enable-features=X")
        flags = os.environ[app.ENGINE_FLAGS].split()
        if allowed:
            # The one --enable-features stays where the first one was: ours
            # (Linux) before pywebview's appended flags, else where X came.
            tail = (
                [features("X"), "--remote-debugging-pipe"] if PIPEWIRE else ["--remote-debugging-pipe", features("X")]
            )
            assert flags == ["--use-fake-device-for-media-stream", *unsafe.split(), *tail]
            assert os.environ[app.REMOTE_DEBUGGING_ENV] == "127.0.0.1:9222"
        else:
            assert flags == ["--use-fake-device-for-media-stream", features("X")]
            assert app.REMOTE_DEBUGGING_ENV not in os.environ
    assert app.parse_args(["--allow-remote-debugging"]).allow_remote_debugging
    assert app.restart_args(app.parse_args(["--allow-remote-debugging"])) == []


def test_a_missing_harness_fails_in_ci(monkeypatch):
    """Locally a test whose harness is missing skips; in CI (CI=true) it
    fails, so a green CI run can't hide UI tests that never ran. Windows and
    macOS count as able to open windows (their desktop session)."""
    import shutil as sh

    import conftest

    monkeypatch.setattr(conftest, "CI", True)
    with pytest.raises(pytest.fail.Exception, match="CI must provide it"):
        conftest.missing_harness("needs a display")
    monkeypatch.setattr(conftest, "CI", False)
    with pytest.raises(pytest.skip.Exception):
        conftest.missing_harness("needs a display")
    for plat in ("win32", "darwin"):
        monkeypatch.setattr(sys, "platform", plat)
        assert conftest.gui_available()
    monkeypatch.setattr(sys, "platform", "linux")
    monkeypatch.setattr(sh, "which", lambda name: None)
    assert not conftest.gui_available()


def test_an_untested_python_is_refused_with_a_clear_message(monkeypatch):
    """Python 3.15 breaks inside the Qt process (pyproject.toml): the app
    says which version it needs before importing anything heavy."""
    import runpy

    monkeypatch.setattr(sys, "version_info", (3, 15, 1, "final", 0))
    monkeypatch.setattr(sys, "version", "3.15.1 (main) [GCC]")
    with pytest.raises(SystemExit) as e:
        runpy.run_module("openarcanum.__main__", run_name="__main__")
    assert e.value.code == "OpenArcanum runs on Python 3.14; this is 3.15.1."


def test_pipewire_screen_capture_is_enabled_and_merged():
    """Wayland screen sharing needs the engine's PipeWire capturer: added, and
    merged into an --enable-features already given (never a second one)."""
    from openarcanum.app import with_pipewire_capture

    assert with_pipewire_capture([]) == ["--enable-features=WebRTCPipeWireCapturer"]
    assert with_pipewire_capture(["--foo", "--enable-features=A,B"]) == [
        "--foo",
        "--enable-features=A,B,WebRTCPipeWireCapturer",
    ]
    assert with_pipewire_capture(["--enable-features=WebRTCPipeWireCapturer"]) == [
        "--enable-features=WebRTCPipeWireCapturer"
    ]


def test_enable_features_are_merged_into_one():
    """The engine honours only the last --enable-features: pywebview's own
    (appended after ours) would otherwise drop the PipeWire capturer."""
    from openarcanum.app import merge_enable_features

    assert merge_enable_features(["--a", "--enable-features=X", "--b", "--enable-features=Y,X"]) == [
        "--a",
        "--enable-features=X,Y",
        "--b",
    ]
    assert merge_enable_features(["--a"]) == ["--a"]
