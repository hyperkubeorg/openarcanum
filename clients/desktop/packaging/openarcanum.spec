# PyInstaller spec for the desktop app: one spec for Linux, Windows and macOS
# (docs/packaging.md). Build with `make bundle`, which runs
#
#     python -m PyInstaller --noconfirm --distpath dist --workpath build/pyinstaller packaging/openarcanum.spec
#
# from clients/desktop/. Output: dist/OpenArcanum/ (one folder, "onedir"), and on macOS
# also dist/OpenArcanum.app.
#
# What it collects, besides the Python code PyInstaller finds by itself:
#   - the web UI, clients/shared/ui/, as openarcanum/ui/ (the page, loaded as a
#     real file:// URI, see paths.py UI_DIR and app.py FRONTEND_INDEX: no
#     loopback HTTP server, in the bundle as in development),
#     openarcanum/_vendor/ (the vendored Noise package), and every openarcanum
#     submodule (some are imported lazily);
#   - pywebview's injected scripts (webview/js/) and its Qt backend, which it
#     imports by name at run time;
#   - Qt WebEngine (QtWebEngineProcess, its resources and locales) through
#     PyInstaller's PySide6.QtWebEngine* hooks;
#   - protobuf (the upb module) and vodozemac's native module, found from
#     the imports;
#   - the Go community server (`community`, `community.exe` on Windows) in
#     servers/, which the app runs to host communities on this computer and
#     saves for "Host on your own server" (openarcanum/hosting.py). `make bundle`
#     builds it into build/servers/ first (static, stripped: about 15 MB);
#   - the licences, in licenses/: LICENSE.txt (AGPL-3.0-only),
#     THIRD_PARTY_NOTICES.md and every bundled component's licence files,
#     which `make bundle` collects into build/licenses/ first
#     (packaging/licenses.py).
#
# Environment:
#   OPENARCANUM_CODESIGN_IDENTITY  macOS: sign with this Developer ID identity
#                               (hardened runtime, packaging/macos/entitlements.plist);
#                               unset means an ad-hoc signature.

import os
import re
import sys
from pathlib import Path

from PyInstaller.depend import bindepend
from PyInstaller.utils.hooks import collect_data_files, collect_submodules

HERE = Path(SPECPATH)  # noqa: F821 (PyInstaller defines it)
CLIENT = HERE.parent
# The web UI, shared with the web client (clients/shared/ui).
UI = CLIENT.parent / "shared" / "ui"
if not (UI / "index.html").is_file():
    raise SystemExit(f"{UI} is missing the web UI (index.html)")
sys.path.insert(0, str(CLIENT / "src"))

from openarcanum import PRODUCT_NAME, __version__  # noqa: E402

# The release version (VERSION at the repository root, copied into
# openarcanum.__version__ by scripts/version): YYYY.MM.PATCH[-beta.N]. Apple's
# bundle versions and Windows' version resource take numbers only, so they
# get YYYY.MM.PATCH; the full version is in the strings that allow text.
_numeric = re.match(r"(\d+)\.(\d+)\.(\d+)", __version__)
if not _numeric:
    raise SystemExit(f"openarcanum.__version__ {__version__!r} is not YYYY.MM.PATCH[-beta.N]")
NUMERIC_VERSION = _numeric[0]

BUNDLE_ID = "org.openarcanum.OpenArcanum"
ICONS = CLIENT / "build" / "icons"

SERVER = CLIENT / "build" / "servers" / ("community.exe" if sys.platform == "win32" else "community")
if not SERVER.is_file():
    raise SystemExit(f"{SERVER} is missing: `make bundle` builds it (go build ./cmd/community)")
binaries = [(str(SERVER), "servers")]

LICENSES = CLIENT / "build" / "licenses"
if not (LICENSES / "INDEX.txt").is_file():
    raise SystemExit(f"{LICENSES} is missing: `make bundle` collects it (packaging/licenses.py)")

datas = collect_data_files("openarcanum", excludes=["**/__pycache__/**"])
datas += [(str(UI), "openarcanum/ui")]
datas += collect_data_files("webview", includes=["js/**"])
datas += [(str(LICENSES), "licenses")]
hiddenimports = collect_submodules("openarcanum") + [
    "webview.platforms.qt",
    "qtpy.QtWebEngineCore",
    "qtpy.QtWebEngineWidgets",
    "qtpy.QtNetwork",
    "PySide6.QtWebEngineCore",
    "PySide6.QtWebEngineWidgets",
    "PySide6.QtWebChannel",
    "PySide6.QtNetwork",
]
# pywebview's other backends and toolkits this app never uses.
excludes = [
    "webview.platforms.android",
    "webview.platforms.cef",
    "webview.platforms.cocoa",
    "webview.platforms.edgechromium",
    "webview.platforms.gtk",
    "webview.platforms.mshtml",
    "webview.platforms.winforms",
    "clr",
    "gi",
    "objc",
    "AppKit",
    "Foundation",
    "WebKit",
    "Quartz",
    "Security",
    "tkinter",
    "PyQt5",
    "PyQt6",
    "PySide2",
    "pytest",
    "hypothesis",
    # qtpy imports it if present; nothing here uses it.
    "qtpy.QtDataVisualization",
    "PySide6.QtDataVisualization",
]

a = Analysis(  # noqa: F821
    [str(HERE / "launcher.py")],
    pathex=[str(CLIENT / "src")],
    binaries=binaries,
    datas=datas,
    hiddenimports=hiddenimports,
    excludes=excludes,
    noarchive=False,
)


# Trim what PyInstaller's QtQml hook drags in: the app has no QML, so the QML
# plugin tree goes, and (Linux, Windows: plain library files) every Qt library
# nothing left links to. About 120 MB of 3D, Charts, Quick Controls and the
# like. The smoke test (tests/test_bundle.py) proves the rest starts.
def in_qml_tree(dest: str) -> bool:
    parts = Path(dest).parts
    return "PySide6" in parts and "qml" in parts


QT_LIB = re.compile(r"^(lib)?Qt6\w+(\.so\.6|\.dll)$", re.IGNORECASE)


def reachable_qt_libs(binaries) -> set[str]:
    by_name = {Path(dest).name.lower(): src for dest, src, _ in binaries}
    todo = [src for dest, src, _ in binaries if not QT_LIB.match(Path(dest).name)]
    seen: set[str] = set()
    while todo:
        for name, _ in bindepend.get_imports(todo.pop()):
            base = Path(name).name.lower()
            if QT_LIB.match(base) and base not in seen and base in by_name:
                seen.add(base)
                todo.append(by_name[base])
    return seen


a.datas = [d for d in a.datas if not in_qml_tree(d[0])]
a.binaries = [b for b in a.binaries if not in_qml_tree(b[0])]
if sys.platform != "darwin":
    keep = reachable_qt_libs(a.binaries)

    def kept(entry) -> bool:
        name = Path(entry[0]).name
        return not QT_LIB.match(name) or name.lower() in keep

    a.binaries = [b for b in a.binaries if kept(b)]
    # Top-level symbolic links to Qt libraries are in datas (typecode SYMLINK).
    a.datas = [d for d in a.datas if d[2] != "SYMLINK" or kept(d)]

# Linux: libraries that must match the system they run on come from it,
# never from the build machine (the AppImage project's excludelist does the
# same). Every system the bundle runs on (glibc 2.35+) has them, at least as
# new as the build machine's (tests/test_bundle.py):
# - the C++ runtime: a newer graphics driver (Mesa) loaded next to an older
#   libstdc++ fails, and the engine aborts at start ("Could not initialize
#   GLX");
# - GBM: tied to the system's Mesa; the build machine's makes GPU
#   rendering fail ("did not find extension DRI_Mesa", "Failed to
#   initialize GBM device") and the window falls back to slow software;
# - fontconfig and the libraries it brings (FreeType, HarfBuzz, Expat): the
#   build machine's can't read a newer system's font configuration
#   ("Fontconfig error: ... invalid attribute 'xsi:nil'"), and fonts and
#   emoji fall back.
HOST_RUNTIME = (
    "libstdc++.so.6",
    "libgcc_s.so.1",
    "libgbm.so.1",
    "libfontconfig.so.1",
    "libfreetype.so.6",
    "libharfbuzz.so.0",
    "libexpat.so.1",
)
if sys.platform.startswith("linux"):
    a.binaries = [b for b in a.binaries if Path(b[0]).name not in HOST_RUNTIME]

pyz = PYZ(a.pure)  # noqa: F821

icon = None
if sys.platform == "win32":
    icon = str(ICONS / "openarcanum.ico")
elif sys.platform == "darwin":
    icon = str(ICONS / "openarcanum.icns")

codesign_identity = os.environ.get("OPENARCANUM_CODESIGN_IDENTITY") or None

# Windows: the executable's version resource (Properties -> Details), in the
# text form PyInstaller reads (its grab_version format).
version_file = None
if sys.platform == "win32":
    nums = (*(int(n) for n in NUMERIC_VERSION.split(".")), 0)
    strings = {
        "CompanyName": PRODUCT_NAME,
        "FileDescription": PRODUCT_NAME,
        "FileVersion": __version__,
        "InternalName": PRODUCT_NAME,
        "LegalCopyright": "Licensed under AGPL-3.0-only",
        "OriginalFilename": f"{PRODUCT_NAME}.exe",
        "ProductName": PRODUCT_NAME,
        "ProductVersion": __version__,
    }
    version_file = CLIENT / "build" / "version-info.txt"
    version_file.write_text(
        f"VSVersionInfo(ffi=FixedFileInfo(filevers={nums!r}, prodvers={nums!r}, mask=0x3f, flags=0x0, "
        "OS=0x40004, fileType=0x1, subtype=0x0, date=(0, 0)), kids=["
        "StringFileInfo([StringTable('040904B0', ["
        + ", ".join(f"StringStruct({k!r}, {v!r})" for k, v in strings.items())
        + "])]), VarFileInfo([VarStruct('Translation', [1033, 1200])])])\n",
        encoding="utf-8",
    )
    version_file = str(version_file)
entitlements = str(HERE / "macos" / "entitlements.plist") if sys.platform == "darwin" else None

exe = EXE(  # noqa: F821
    pyz,
    a.scripts,
    [],
    exclude_binaries=True,
    name=PRODUCT_NAME,
    console=False,
    icon=icon,
    # UPX breaks Qt WebEngine and signed binaries.
    upx=False,
    codesign_identity=codesign_identity,
    entitlements_file=entitlements,
    version=version_file,
)
coll = COLLECT(  # noqa: F821
    exe,
    a.binaries,
    a.datas,
    upx=False,
    name=PRODUCT_NAME,
)

if sys.platform == "darwin":
    app = BUNDLE(  # noqa: F821
        coll,
        name=f"{PRODUCT_NAME}.app",
        icon=icon,
        bundle_identifier=BUNDLE_ID,
        version=NUMERIC_VERSION,
        codesign_identity=codesign_identity,
        entitlements_file=entitlements,
        info_plist={
            "CFBundleName": PRODUCT_NAME,
            "CFBundleDisplayName": PRODUCT_NAME,
            "CFBundleShortVersionString": NUMERIC_VERSION,
            "CFBundleVersion": NUMERIC_VERSION,
            # The full release version, beta suffix included.
            "OpenArcanumReleaseVersion": __version__,
            "NSHumanReadableCopyright": "AGPL-3.0-only",
            "LSMinimumSystemVersion": "14.0",
            "NSHighResolutionCapable": True,
            "LSApplicationCategoryType": "public.app-category.social-networking",
            # Calls capture the microphone, the camera and the screen; macOS
            # shows these strings when it asks for permission.
            "NSMicrophoneUsageDescription": f"{PRODUCT_NAME} uses the microphone for voice calls you join.",
            "NSCameraUsageDescription": f"{PRODUCT_NAME} uses the camera when you turn on video in a call.",
            "NSScreenCaptureUsageDescription": f"{PRODUCT_NAME} shares your screen or a window when you choose to in a call.",
            "NSAudioCaptureUsageDescription": f"{PRODUCT_NAME} shares the sound from your computer along with your screen when you choose to.",
            # openarcanum://invite/... links open the app (openarcanum/instance.py).
            "CFBundleURLTypes": [
                {
                    "CFBundleURLName": f"{BUNDLE_ID}.invite",
                    "CFBundleURLSchemes": ["openarcanum"],
                    "CFBundleTypeRole": "Viewer",
                }
            ],
        },
    )
