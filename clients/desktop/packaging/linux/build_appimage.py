"""Build the Linux AppImage from the PyInstaller bundle (docs/packaging.md).

    python packaging/linux/build_appimage.py --bundle dist/OpenArcanum \\
        --out dist/OpenArcanum-x86_64.AppImage --tools build/appimage-tools

Downloads appimagetool and the AppImage runtime at pinned versions into
--tools (once), checks their SHA-256 before use, lays out an AppDir (the
bundle under usr/lib/openarcanum, AppRun, the .desktop file that registers the
openarcanum:// scheme, the app icon in every icon theme size) and packs it. Needs no root and no FUSE:
appimagetool runs extracted (APPIMAGE_EXTRACT_AND_RUN=1).

Standard library only.
"""

from __future__ import annotations

import argparse
import hashlib
import importlib.util
import os
import shutil
import subprocess
import sys
import tempfile
import urllib.request
from pathlib import Path

HERE = Path(__file__).resolve().parent
CLIENT = HERE.parents[1]
DESKTOP = HERE / "openarcanum.desktop"

# Pinned tools: appimagetool 1.9.1 (2025-11-18) and the type2 runtime release
# 20251108, from github.com/AppImage. The hashes match the digests GitHub
# publishes for these release assets.
TOOLS = {
    "appimagetool-x86_64.AppImage": (
        "https://github.com/AppImage/appimagetool/releases/download/1.9.1/appimagetool-x86_64.AppImage",
        "ed4ce84f0d9caff66f50bcca6ff6f35aae54ce8135408b3fa33abfc3cb384eb0",
    ),
    "runtime-x86_64": (
        "https://github.com/AppImage/type2-runtime/releases/download/20251108/runtime-x86_64",
        "2fca8b443c92510f1483a883f60061ad09b46b978b2631c807cd873a47ec260d",
    ),
}

APPRUN = """#!/bin/sh
# AppImage entry point: run the bundled app with the arguments given (an
# invite link when the OS opens an openarcanum:// link).
HERE="$(dirname "$(readlink -f "$0")")"
exec "$HERE/usr/lib/openarcanum/OpenArcanum" "$@"
"""


def sha256(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def fetch_tool(tools: Path, name: str) -> Path:
    url, digest = TOOLS[name]
    path = tools / name
    if path.exists() and sha256(path) == digest:
        return path
    tools.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(".part")
    print(f"downloading {url}", flush=True)
    with urllib.request.urlopen(url, timeout=120) as r, tmp.open("wb") as f:
        shutil.copyfileobj(r, f)
    got = sha256(tmp)
    if got != digest:
        tmp.unlink()
        sys.exit(f"{name}: SHA-256 mismatch (got {got}, want {digest}); not using it")
    tmp.chmod(0o755)
    tmp.replace(path)
    return path


def make_appdir(bundle: Path, appdir: Path) -> None:
    if not (bundle / "OpenArcanum").is_file():
        sys.exit(f"{bundle} has no OpenArcanum executable; run `make bundle` first")
    shutil.copytree(bundle, appdir / "usr" / "lib" / "openarcanum", symlinks=True)
    apprun = appdir / "AppRun"
    apprun.write_text(APPRUN)
    apprun.chmod(0o755)
    shutil.copy(DESKTOP, appdir / "openarcanum.desktop")
    apps = appdir / "usr" / "share" / "applications"
    apps.mkdir(parents=True)
    shutil.copy(DESKTOP, apps / "openarcanum.desktop")
    install_icons(appdir)


def _icons():
    """packaging/icons.py, which renders the app icon from its SVG master."""
    spec = importlib.util.spec_from_file_location("openarcanum_icons", HERE.parent / "icons.py")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def install_icons(appdir: Path) -> None:
    """The app icon in the hicolor theme (each PNG size, and the SVG master
    as the scalable one), which the .desktop file's Icon=openarcanum and the
    window's app id find, plus the AppImage's own top-level icon."""
    icons = _icons()
    hicolor = appdir / "usr" / "share" / "icons" / "hicolor"
    for n in icons.HICOLOR_SIZES:
        d = hicolor / f"{n}x{n}" / "apps"
        d.mkdir(parents=True)
        (d / "openarcanum.png").write_bytes(icons.icon_png(n))
    scalable = hicolor / "scalable" / "apps"
    scalable.mkdir(parents=True)
    shutil.copy(icons.MASTER, scalable / "openarcanum.svg")
    shutil.copy(hicolor / "256x256" / "apps" / "openarcanum.png", appdir / "openarcanum.png")
    (appdir / ".DirIcon").symlink_to("openarcanum.png")


def main() -> None:
    p = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    p.add_argument("--bundle", type=Path, required=True, help="PyInstaller output folder (dist/OpenArcanum)")
    p.add_argument("--out", type=Path, required=True, help="the AppImage to write")
    p.add_argument("--tools", type=Path, required=True, help="where the pinned tools are cached")
    args = p.parse_args()

    tools = args.tools.resolve()
    tool = fetch_tool(tools, "appimagetool-x86_64.AppImage")
    runtime = fetch_tool(tools, "runtime-x86_64")
    args.out.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="appdir-", dir=args.out.parent) as tmp:
        appdir = Path(tmp) / "OpenArcanum.AppDir"
        make_appdir(args.bundle.resolve(), appdir)
        env = dict(os.environ, APPIMAGE_EXTRACT_AND_RUN="1", ARCH="x86_64")
        out = args.out.resolve()
        out.unlink(missing_ok=True)
        subprocess.run(
            [str(tool), "--no-appstream", "--runtime-file", str(runtime), str(appdir), str(out)],
            check=True,
            env=env,
            cwd=tmp,
        )
    print(f"built {args.out} ({args.out.stat().st_size >> 20} MiB, sha256 {sha256(args.out)})")


if __name__ == "__main__":
    main()
