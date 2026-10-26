"""The developer tooling run on a developer's computer: scripts/dev-client (app
copies in podman), scripts/desktop.sh (the app run natively), scripts/clean.sh
and the Makefile targets around them. podman, kind and a desktop aren't
available where the tests run, so these check argument handling, the commands
the targets would run and the clean-up's refusals."""

from __future__ import annotations

import importlib.machinery
import importlib.util
import os
import shutil
import socket
import subprocess
import sys
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[3]
SCRIPTS = ROOT / "scripts"
BASH = shutil.which("bash")
MAKE = shutil.which("make")

pytestmark = pytest.mark.skipif(sys.platform == "win32", reason="the developer scripts run on Linux and macOS")


def _load(name: str):
    loader = importlib.machinery.SourceFileLoader(name.replace("-", "_"), str(SCRIPTS / name))
    spec = importlib.util.spec_from_loader(loader.name, loader)
    mod = importlib.util.module_from_spec(spec)
    sys.modules[loader.name] = mod
    loader.exec_module(mod)
    return mod


devclient = _load("dev-client")


def run(*cmd: str, env: dict | None = None, cwd: Path = ROOT, stdin=subprocess.DEVNULL) -> subprocess.CompletedProcess:
    return subprocess.run(
        list(cmd), cwd=cwd, capture_output=True, text=True, env={**os.environ, **(env or {})}, stdin=stdin
    )


def make_n(*args: str) -> str:
    if MAKE is None:
        pytest.skip("no make")
    r = run(MAKE, "--no-print-directory", "-n", *args)
    assert r.returncode == 0, r.stderr
    return r.stdout


def unix_socket(path: Path) -> socket.socket:
    path.parent.mkdir(parents=True, exist_ok=True)
    s = socket.socket(socket.AF_UNIX)
    s.bind(str(path))
    return s


# ---------------------------------------------------------------- dev-client


def test_dev_client_help_lists_the_media_options():
    r = run(sys.executable, str(SCRIPTS / "dev-client"), "--help")
    assert r.returncode == 0, r.stderr
    for opt in ("--fake-media", "--no-media", "--maintainer", "--list", "--stop", "--reset"):
        assert opt in r.stdout


def test_dev_client_without_a_name_prints_usage():
    r = run(sys.executable, str(SCRIPTS / "dev-client"))
    assert r.returncode == 2
    assert "usage:" in r.stdout


@pytest.mark.parametrize("name", ["Alice", "a b", "../x", "x" * 41])
def test_dev_client_rejects_bad_names(name):
    r = run(sys.executable, str(SCRIPTS / "dev-client"), "--fake-media", name)
    assert r.returncode == 1
    assert "NAME is 1-40" in r.stderr


@pytest.fixture
def host(tmp_path, monkeypatch):
    """A runtime folder with PulseAudio and PipeWire sockets, no cameras, and
    a display that needs no X server."""
    runtime = tmp_path / "run"
    socks = [unix_socket(runtime / "pulse" / "native"), unix_socket(runtime / "pipewire-0")]
    monkeypatch.setenv("XDG_RUNTIME_DIR", str(runtime))
    monkeypatch.delenv("PULSE_SERVER", raising=False)
    monkeypatch.setenv("PULSE_COOKIE", str(tmp_path / "no-cookie"))
    monkeypatch.setattr(devclient, "camera_args", lambda: (["--device", "/dev/video0"], "cameras: /dev/video0"))
    monkeypatch.setattr(devclient, "display_args", lambda: ["--env", "DISPLAY=:0"])
    yield runtime
    for s in socks:
        s.close()


def _env(args: list[str]) -> dict[str, str]:
    return dict(args[i + 1].split("=", 1) for i, a in enumerate(args) if a == "--env")


def test_dev_client_shares_sound_and_cameras(host):
    args, notes = devclient.run_args("alice", "img", [], [], share_media=True, fake_media=False)
    joined = " ".join(args)
    assert f"source={host}/pulse/native,target=/tmp/.runtime/pulse/native" in joined
    assert f"source={host}/pipewire-0,target=/tmp/.runtime/pipewire-0" in joined
    env = _env(args)
    assert env["PULSE_SERVER"] == "unix:/tmp/.runtime/pulse/native"
    assert env["PIPEWIRE_RUNTIME_DIR"] == env["XDG_RUNTIME_DIR"] == "/tmp/.runtime"
    assert "QTWEBENGINE_CHROMIUM_FLAGS" not in env
    # The runtime folder is mounted before the sockets inside it.
    assert joined.index("target=/tmp/.runtime ") < joined.index("target=/tmp/.runtime/")
    assert args[args.index("--device") + 1] == "/dev/video0"
    # The image, then the app's own arguments.
    assert args[args.index("img") + 1 :] == ["--maintainer", devclient.DEFAULT_MAINTAINER]
    assert "sound: PulseAudio and PipeWire" in notes


def test_dev_client_fake_media_uses_fake_devices_not_fake_ui(host):
    args, notes = devclient.run_args("bob", "img", ["http://m"], ["--debug"], share_media=True, fake_media=True)
    env = _env(args)
    assert env["QTWEBENGINE_CHROMIUM_FLAGS"] == "--use-fake-device-for-media-stream"
    assert "--use-fake-ui-for-media-stream" not in " ".join(args)
    # Fake cameras replace the real ones; sound still plays on the host.
    assert "--device" not in args
    assert "PULSE_SERVER" in env
    assert args[args.index("img") + 1 :] == ["--maintainer", "http://m", "--debug"]
    assert any("fake camera" in n for n in notes)


def test_dev_client_no_media(host):
    args, notes = devclient.run_args("carol", "img", [], [], share_media=False, fake_media=False)
    joined = " ".join(args)
    assert "pulse" not in joined and "pipewire" not in joined and "--device" not in args
    assert "target=/tmp/.runtime" not in " ".join(args) and "XDG_RUNTIME_DIR" not in _env(args)
    assert notes == ["no sound or cameras (--no-media)"]


def test_dev_client_without_sound_sockets(tmp_path, monkeypatch):
    monkeypatch.setenv("XDG_RUNTIME_DIR", str(tmp_path))
    monkeypatch.delenv("PULSE_SERVER", raising=False)
    args, note = devclient.audio_args()
    assert args == [] and "no PulseAudio or PipeWire socket" in note
    monkeypatch.delenv("XDG_RUNTIME_DIR")
    assert devclient.audio_args() == ([], "no XDG_RUNTIME_DIR here, so no sound")


def test_dev_client_pulse_server_and_cookie(tmp_path, monkeypatch):
    sock = unix_socket(tmp_path / "elsewhere" / "pulse.sock")
    cookie = tmp_path / "cookie"
    cookie.write_bytes(b"c" * 256)
    monkeypatch.setenv("XDG_RUNTIME_DIR", str(tmp_path / "run"))
    monkeypatch.setenv("PULSE_SERVER", f"unix:{tmp_path}/elsewhere/pulse.sock")
    monkeypatch.setenv("PULSE_COOKIE", str(cookie))
    try:
        args, note = devclient.audio_args()
    finally:
        sock.close()
    joined = " ".join(args)
    assert f"source={tmp_path}/elsewhere/pulse.sock,target=/tmp/.runtime/pulse/native" in joined
    assert f"source={cookie},target=/tmp/.pulse-cookie,ro=true" in joined
    assert _env(args)["PULSE_COOKIE"] == "/tmp/.pulse-cookie"
    assert note == "sound: PulseAudio"


def test_dev_client_wayland_socket_lives_in_the_runtime_folder(tmp_path, monkeypatch):
    sock = unix_socket(tmp_path / "wayland-1")
    monkeypatch.setenv("XDG_RUNTIME_DIR", str(tmp_path))
    monkeypatch.setenv("WAYLAND_DISPLAY", "wayland-1")
    monkeypatch.delenv("DISPLAY", raising=False)
    try:
        args = devclient.display_args()
    finally:
        sock.close()
    assert _env(args)["WAYLAND_DISPLAY"] == "wayland-1"
    runtime = devclient.runtime_args("alice", args)
    assert runtime[-1] == "XDG_RUNTIME_DIR=/tmp/.runtime"
    # A bind of a folder of the user's, not a tmpfs: podman versions disagree
    # on the tmpfs options that set an owner ("unknown mount option uid=1000").
    assert "--tmpfs" not in runtime
    assert f"type=bind,source={devclient.RUN_DIR / 'alice'},target=/tmp/.runtime" in runtime


# ---------------------------------------------------------------- desktop.sh


def desktop(*args: str, env: dict | None = None) -> subprocess.CompletedProcess:
    if BASH is None:
        pytest.skip("no bash")
    return run(BASH, str(SCRIPTS / "desktop.sh"), *args, env=env)


def test_desktop_runs_from_source_with_a_profile_in_the_project():
    r = desktop("-n", "me", env={"UV": "uv"})
    assert r.returncode == 0, r.stderr
    cmd = r.stdout.split()
    assert cmd[:6] == ["uv", "run", "--project", str(ROOT / "clients" / "desktop"), "--locked", "python"]
    assert cmd[6:] == [
        "-m",
        "openarcanum",
        "--home",
        str(ROOT / ".dev" / "desktop" / "me"),
        "--maintainer",
        "http://127.0.0.1:7100",
    ]


def test_desktop_fake_media_and_app_args():
    r = desktop("-n", "--fake-media", "bob", "--", "--debug", env={"QTWEBENGINE_CHROMIUM_FLAGS": ""})
    assert r.returncode == 0, r.stderr
    cmd = r.stdout.split()
    assert cmd[:2] == ["env", "QTWEBENGINE_CHROMIUM_FLAGS=--use-fake-device-for-media-stream"]
    assert cmd[-1] == "--debug"
    assert str(ROOT / ".dev" / "desktop" / "bob") in cmd


def test_desktop_app_maintainer_replaces_the_default():
    r = desktop("-n", "me", "--", "--maintainer", "http://m:1")
    assert r.returncode == 0, r.stderr
    assert "127.0.0.1:7100" not in r.stdout
    assert r.stdout.split()[-2:] == ["--maintainer", "http://m:1"]


def test_desktop_appimage():
    r = desktop("-n", "--appimage", "--rebuild", "me")
    assert r.returncode == 0, r.stderr
    lines = r.stdout.splitlines()
    assert lines[0].endswith(f"-C {ROOT} appimage")
    assert str(ROOT / "clients" / "desktop" / "dist" / "OpenArcanum-x86_64.AppImage") in lines[-1]
    assert f"--home {ROOT}/.dev/desktop/me" in lines[-1]


@pytest.mark.parametrize("name", ["Me", "a/b", "..", ""])
def test_desktop_rejects_bad_names(name):
    r = desktop(name)
    assert r.returncode != 0


def test_desktop_says_when_uv_is_missing():
    r = desktop("me", env={"UV": "uv-that-is-not-installed"})
    assert r.returncode == 1
    assert "uv isn't installed" in r.stderr


def test_desktop_lists_missing_libraries(tmp_path):
    # An ldconfig that knows no libraries.
    fake = tmp_path / "bin"
    fake.mkdir()
    (fake / "ldconfig").write_text("#!/bin/sh\necho '0 libs found in cache'\n")
    (fake / "ldconfig").chmod(0o755)
    (fake / "uv").write_text("#!/bin/sh\nexit 0\n")
    (fake / "uv").chmod(0o755)
    r = desktop("me", env={"PATH": f"{fake}:{os.environ['PATH']}", "UV": str(fake / "uv")})
    if sys.platform != "linux":
        pytest.skip("the library check is Linux only")
    assert r.returncode == 1
    assert "Qt WebEngine needs system libraries" in r.stderr
    assert "sudo pacman -S --needed nss nspr" in r.stderr


# ---------------------------------------------------------------- clean.sh


@pytest.fixture
def project(tmp_path):
    """A throwaway project with scripts/clean.sh in it."""
    if BASH is None:
        pytest.skip("no bash")
    proj = tmp_path / "proj"
    (proj / "scripts").mkdir(parents=True)
    shutil.copy(SCRIPTS / "clean.sh", proj / "scripts" / "clean.sh")
    return proj


def clean(project: Path, *args: str, env: dict | None = None, stdin=subprocess.DEVNULL) -> subprocess.CompletedProcess:
    return run(BASH, str(project / "scripts" / "clean.sh"), *args, env=env, cwd=project, stdin=stdin)


def test_clean_removes_paths_inside_and_says_so(project):
    (project / "bin").mkdir()
    (project / "bin" / "relay").write_text("x")
    (project / "load-results.jsonl").write_text("{}")
    r = clean(project, "paths", "bin", "load-results.jsonl", "dist")
    assert r.returncode == 0, r.stderr
    assert r.stdout.splitlines() == ["removing bin", "removing load-results.jsonl"]
    assert not (project / "bin").exists() and not (project / "load-results.jsonl").exists()
    # Again: nothing left, nothing printed, still fine.
    r = clean(project, "paths", "bin", "load-results.jsonl", "dist")
    assert (r.returncode, r.stdout) == (0, "")


@pytest.mark.parametrize("path", ["/etc", "..", ".", "", "../proj", "bin/../..", "/"])
def test_clean_refuses_paths_outside_the_project(project, path):
    r = clean(project, "paths", path)
    assert r.returncode == 1
    assert "refusing to remove" in r.stderr
    assert project.exists()


def test_clean_does_not_follow_a_folder_link_out_of_the_project(project, tmp_path):
    outside = tmp_path / "outside"
    (outside / "keep").mkdir(parents=True)
    (project / "link").symlink_to(outside)
    r = clean(project, "paths", "link/keep")
    assert r.returncode == 1 and "refusing" in r.stderr
    assert (outside / "keep").is_dir()
    # The link itself is inside: removing it leaves its target alone.
    r = clean(project, "paths", "link")
    assert r.returncode == 0 and r.stdout == "removing link\n"
    assert (outside / "keep").is_dir()


def fake_podman(tmp_path: Path, script: str) -> str:
    """A PATH whose podman runs `script`."""
    fake = tmp_path / "bin"
    fake.mkdir(exist_ok=True)
    (fake / "podman").write_text("#!/bin/sh\n" + script)
    (fake / "podman").chmod(0o755)
    return f"{fake}:/usr/bin:/bin"


def test_clean_dev_asks_and_force_deletes(project, tmp_path):
    (project / ".dev" / "clients" / "alice").mkdir(parents=True)
    path = fake_podman(tmp_path, "exit 0\n")
    r = clean(project, "dev", env={"FORCE": "", "PATH": path})
    assert r.returncode == 1 and "FORCE=1" in r.stderr
    assert (project / ".dev").is_dir()
    r = clean(project, "dev", env={"FORCE": "1", "PATH": path})
    assert r.returncode == 0, r.stderr
    assert "removing .dev" in r.stdout
    assert not (project / ".dev").exists()
    r = clean(project, "dev", env={"FORCE": "1", "PATH": path})
    assert (r.returncode, r.stdout.strip()) == (0, "clean: no .dev/ to remove")


@pytest.mark.parametrize(("answer", "kept"), [("n", True), ("", True), ("y", False)])
def test_clean_dev_asks_on_a_terminal(project, tmp_path, answer, kept):
    import pty

    (project / ".dev" / "clients" / "alice").mkdir(parents=True)
    leader, follower = pty.openpty()
    try:
        os.write(leader, f"{answer}\n".encode())
        r = clean(project, "dev", env={"FORCE": "", "PATH": fake_podman(tmp_path, "exit 0\n")}, stdin=follower)
    finally:
        os.close(follower)
        os.close(leader)
    assert r.returncode == 0, r.stderr
    assert (project / ".dev").is_dir() is kept
    assert ("Kept .dev/." in r.stdout) is kept


def test_clean_dev_refuses_while_a_copy_runs(project, tmp_path):
    (project / ".dev" / "clients" / "alice").mkdir(parents=True)
    path = fake_podman(tmp_path, "echo openarcanum-client-alice\necho other\n")
    r = clean(project, "dev", env={"FORCE": "", "PATH": path})
    assert r.returncode == 1
    assert "app copy alice" in r.stdout and "dev-client --stop" in r.stderr
    assert (project / ".dev").is_dir()


def test_clean_images_without_podman(project, tmp_path):
    empty = tmp_path / "empty"
    empty.mkdir()
    for tool in ("dirname", "sed"):
        if p := shutil.which(tool):
            (empty / tool).symlink_to(p)
    r = clean(project, "images", env={"PATH": str(empty)})
    assert r.returncode == 0, r.stderr
    assert "podman isn't installed" in r.stdout


def test_clean_images_removes_only_this_projects_images(project, tmp_path):
    log = tmp_path / "podman.log"
    path = fake_podman(
        tmp_path,
        f'echo "$@" >> {log}\n'
        'case "$1" in images) printf "%s\\n" "localhost/openarcanum-client:dev 1" '
        '"localhost/openarcanum-relay:dev-abc 2" "docker.io/library/archlinux:base 3" "<none>:<none> 4" '
        '"quay.io/me/app:x 5";; esac\n',
    )
    r = clean(project, "images", "quay.io/me/app:x", env={"PATH": path})
    assert r.returncode == 0, r.stderr
    assert (
        "rmi localhost/openarcanum-client:dev localhost/openarcanum-relay:dev-abc quay.io/me/app:x" in log.read_text()
    )
    assert "archlinux" not in log.read_text().split("rmi", 1)[1]


# ---------------------------------------------------------------- Makefile


def test_help_lists_the_new_targets():
    if MAKE is None:
        pytest.skip("no make")
    out = run(MAKE, "--no-print-directory", "help").stdout
    for target in ("desktop", "desktop-appimage", "clean", "clean-dev", "clean-images", "clean-all", "client"):
        assert f"\n  {target} " in out


def test_make_desktop_commands():
    assert "scripts/desktop.sh --maintainer http://127.0.0.1:7100  me --" in make_n("desktop")
    out = make_n("desktop", "NAME=bob", "ARGS=--debug", "FAKE_MEDIA=1", "MAINTAINER=http://m")
    assert "scripts/desktop.sh --maintainer http://m --fake-media bob -- --debug" in out
    out = make_n("desktop-appimage", "REBUILD=1")
    assert "scripts/desktop.sh --appimage --rebuild --maintainer http://127.0.0.1:7100  me --" in out


def test_make_client_passes_fake_media():
    out = make_n("client", "NAME=x", "ARGS=--fake-media")
    assert "scripts/dev-client --fake-media x" in out


def test_make_clean_commands():
    out = make_n("clean")
    assert out.startswith("scripts/clean.sh paths bin dist ")
    for path in (
        "clients/desktop/build",
        "clients/desktop/dist",
        "load-results.jsonl",
        "clients/desktop/.pytest_cache",
    ):
        assert f" {path} " in out
    assert ".dev" not in out.split()
    assert "FORCE= scripts/clean.sh dev" in make_n("clean-dev")
    assert "FORCE=1 scripts/clean.sh dev" in make_n("clean-dev", "FORCE=1")
    assert "scripts/clean.sh images localhost/openarcanum-client:dev" in make_n("clean-images")
    out = make_n("clean-all")
    assert "scripts/kind.sh down" in out
    for step in (
        "scripts/clean.sh dev",
        "scripts/clean.sh images",
        "scripts/clean.sh paths bin",
        "paths clients/desktop/.venv",
    ):
        assert step in out
