"""The built desktop bundles start: the PyInstaller app (`make bundle`, every
OS) and the Linux AppImage (`make appimage`) each pass the app's own --smoke
run with a fresh --home. Skipped only when that artifact hasn't been built
(OPENARCANUM_BUNDLE_EXE and OPENARCANUM_APPIMAGE point the tests at other copies).
CI builds both and runs this (.github/workflows/ci.yml, job "bundle")."""

import os
import subprocess
import sys
from pathlib import Path

import pytest
from conftest import headless

DIST = Path(__file__).resolve().parents[1] / "dist"
SHARED_UI = Path(__file__).resolve().parents[2] / "shared" / "ui"
APPIMAGE = Path(os.environ.get("OPENARCANUM_APPIMAGE", DIST / "OpenArcanum-x86_64.AppImage"))


def bundled_executable() -> Path:
    # CI points this at an installed copy (packaging/windows/test_installer.sh,
    # packaging/macos/test_dmg.sh).
    if os.environ.get("OPENARCANUM_BUNDLE_EXE"):
        return Path(os.environ["OPENARCANUM_BUNDLE_EXE"])
    if sys.platform == "darwin":
        return DIST / "OpenArcanum.app" / "Contents" / "MacOS" / "OpenArcanum"
    return DIST / "OpenArcanum" / ("OpenArcanum.exe" if os.name == "nt" else "OpenArcanum")


def smoke(cmd: list[str], home: Path) -> None:
    p = subprocess.run(
        headless([*cmd, "--smoke", "--home", str(home)]),
        stdout=subprocess.DEVNULL,
        stderr=subprocess.PIPE,
        text=True,
        timeout=300,
    )
    log_file = home / "log" / "client.log"
    log = log_file.read_text(encoding="utf-8") if log_file.exists() else ""
    assert p.returncode == 0, f"exit {p.returncode}\n{p.stderr[-3000:]}\n{log[-3000:]}"
    assert "smoke: UI loaded" in log, log[-3000:]
    # The page came from inside the bundle, as a file:// URI (no loopback server).
    assert "UI not found" not in log


@pytest.mark.gui
def test_bundle_smoke(tmp_path):
    exe = bundled_executable()
    if not exe.exists():
        if os.environ.get("OPENARCANUM_BUNDLE_EXE"):
            pytest.fail(f"OPENARCANUM_BUNDLE_EXE={exe} does not exist")
        pytest.skip(f"{exe} not built (run `make bundle`)")
    smoke([str(exe)], tmp_path / "home")


def test_bundle_carries_the_community_server(tmp_path):
    """The bundle holds the community server (servers/, openarcanum/hosting.py)
    for hosting communities on this computer, and it runs here."""
    exe = bundled_executable()
    if not exe.exists():
        if os.environ.get("OPENARCANUM_BUNDLE_EXE"):
            pytest.fail(f"OPENARCANUM_BUNDLE_EXE={exe} does not exist")
        pytest.skip(f"{exe} not built (run `make bundle`)")
    root = exe.parents[1] if sys.platform == "darwin" else exe.parent
    name = "community.exe" if os.name == "nt" else "community"
    found = [p for p in root.rglob(name) if p.parent.name == "servers" and p.is_file()]
    assert found, f"no servers/{name} under {root}"
    out = subprocess.run(
        [str(found[0]), "init", "-data", str(tmp_path / "server"), "-relay", "ws://relay.example.invalid/ws"],
        capture_output=True,
        text=True,
        timeout=60,
    )
    assert out.returncode == 0, out.stderr
    assert "openarcanum-claim:" in out.stdout


def test_bundle_carries_the_web_ui():
    """The bundle holds the whole web UI from clients/shared/ui as openarcanum/ui
    (packaging/openarcanum.spec; openarcanum/paths.py UI_DIR), file for file."""
    exe = bundled_executable()
    if not exe.exists():
        if os.environ.get("OPENARCANUM_BUNDLE_EXE"):
            pytest.fail(f"OPENARCANUM_BUNDLE_EXE={exe} does not exist")
        pytest.skip(f"{exe} not built (run `make bundle`)")
    root = exe.parents[1] if sys.platform == "darwin" else exe.parent
    found = [
        p.parent for p in root.rglob("index.html") if p.parent.name == "ui" and p.parent.parent.name == "openarcanum"
    ]
    assert found, f"no openarcanum/ui/index.html under {root}"

    def files(d: Path) -> set[str]:
        return {p.relative_to(d).as_posix() for p in d.rglob("*") if p.is_file()}

    assert files(found[0]) == files(SHARED_UI)


def test_bundle_carries_the_licences():
    """The bundle ships the project's licence, the third-party notices and
    each bundled component's licence (licenses/, packaging/licenses.py)."""
    exe = bundled_executable()
    if not exe.exists():
        if os.environ.get("OPENARCANUM_BUNDLE_EXE"):
            pytest.fail(f"OPENARCANUM_BUNDLE_EXE={exe} does not exist")
        pytest.skip(f"{exe} not built (run `make bundle`)")
    root = exe.parents[1] if sys.platform == "darwin" else exe.parent
    found = [p.parent for p in root.rglob("INDEX.txt") if p.parent.name == "licenses"]
    assert found, f"no licenses/INDEX.txt under {root}"
    lic = found[0]
    assert "GNU AFFERO GENERAL PUBLIC LICENSE" in (lic / "LICENSE.txt").read_text(encoding="utf-8")
    assert (lic / "THIRD_PARTY_NOTICES.md").is_file()
    assert (lic / "LICENSES" / "LGPL-3.0-only.txt").is_file()
    index = (lic / "INDEX.txt").read_text(encoding="utf-8")
    for name in ("PySide6", "pywebview", "cryptography", "CPython", "modernc.org/sqlite"):
        assert f"  {name}  " in index, name


@pytest.mark.skipif(not sys.platform.startswith("linux"), reason="AppImages are Linux only")
@pytest.mark.gui
def test_appimage_smoke(tmp_path):
    if not APPIMAGE.exists():
        pytest.skip(f"{APPIMAGE} not built (run `make appimage`)")
    # Extract-and-run needs no FUSE (containers and CI runners lack it).
    smoke([str(APPIMAGE), "--appimage-extract-and-run"], tmp_path / "home")
    # The desktop entry desktop integration installs registers openarcanum://.
    subprocess.run([str(APPIMAGE), "--appimage-extract", "openarcanum.desktop"], cwd=tmp_path, check=True, timeout=120)
    desktop = (tmp_path / "squashfs-root" / "openarcanum.desktop").read_text()
    assert "MimeType=x-scheme-handler/openarcanum;" in desktop and "Exec=OpenArcanum -- %u" in desktop
