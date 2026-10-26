# Building and signing the desktop installers

How the client becomes a Linux AppImage, a Windows installer and a macOS
`.dmg`, how each is signed, and how CI checks that each one starts. The signed
update manifest, the release key and the updater are in `docs/release.md`.
What testers do by hand is in `docs/manual-tests/installers.md`.

## What is where

| Piece | File |
|---|---|
| One PyInstaller spec for all three OSes | `clients/desktop/packaging/openarcanum.spec` (entry point `clients/desktop/packaging/launcher.py`) |
| The app icon: SVG master, and every PNG, `.ico` and `.icns` rendered from it (standard library only; see "The app icon") | `clients/shared/ui/brand/openarcanum.svg`, `clients/desktop/packaging/icons.py` |
| Linux AppImage: AppDir layout, pinned and hash-checked tools | `clients/desktop/packaging/linux/build_appimage.py`, `clients/desktop/packaging/linux/openarcanum.desktop` |
| Windows installer (Inno Setup) and its signing | `clients/desktop/packaging/windows/openarcanum.iss`, `clients/desktop/packaging/windows/build_installer.sh` |
| macOS app, entitlements, `.dmg`, signing and notarization | `clients/desktop/packaging/macos/build_dmg.sh`, `clients/desktop/packaging/macos/entitlements.plist` |
| CI checks of each artifact | `clients/desktop/packaging/windows/test_installer.sh`, `clients/desktop/packaging/macos/test_dmg.sh`, `clients/desktop/tests/test_bundle.py`, `clients/desktop/tests/test_packaging.py` |
| The licences shipped in the bundle | `LICENSE`, `THIRD_PARTY_NOTICES.md`, `LICENSES/`, `clients/desktop/packaging/licenses.py` |
| Workflows | `.github/workflows/ci.yml` job `bundle` (every push, unsigned), `.github/workflows/release.yml` (version tags, signed when the secrets exist; `docs/release.md`) |

## The app icon

A violet rounded tile with a white speech bubble cracked into four glass
shards, the top-right shard breaking away and a cyan chip flying off it: a
message breaking open. The master is `clients/shared/ui/brand/openarcanum.svg`,
drawn on a 256-unit grid in a small SVG subset (linear gradients, rectangles
with rounded corners, polygons, circles, flat fills) so that
`clients/desktop/packaging/icons.py` can render it with the standard library alone:
every size comes out pixel for pixel the same on every OS and build machine,
with no image library or SVG renderer to install or pin.

| Output | Made | Used by |
|---|---|---|
| `ui/brand/openarcanum-N.png`, N = 16 to 256 | `python packaging/icons.py --brand`, committed | The window, taskbar and dock icon (`QApplication.setWindowIcon`, all sizes), the tray icon, the page's favicon |
| `ui/brand/openarcanum-alert-N.png`, N = 16 to 64 | the same | The tray icon while something waits (a red dot, top right, with a clear ring around it) |
| `ui/brand/openarcanum.svg` | the master itself | The lock and start screens and Settings → About (`<img>`, from the app's own folder) |
| `build/icons/openarcanum.ico` (16 to 256), `openarcanum.icns` (16 to 1024) | `python packaging/icons.py build/icons`, run by `make bundle` | The Windows executable and Inno Setup (`SetupIconFile`; shortcuts and the uninstall entry use the executable's icon), the macOS `.app` |
| AppDir `hicolor` PNGs and `scalable` SVG | `build_appimage.py` (renders them itself) | The `.desktop` file's `Icon=openarcanum`, the window's app id on Wayland (`setDesktopFileName("openarcanum")`) |

To change the icon, edit the SVG, run `python packaging/icons.py --brand` in
`clients/desktop/`, and commit the PNGs with it: `tests/test_packaging.py` fails while
a committed PNG doesn't match a fresh render of the master (it compares
pixels, so another zlib build doesn't matter).

## Building locally (Linux)

```sh
make bundle        # PyInstaller onedir app in clients/desktop/dist/OpenArcanum/
make appimage      # clients/desktop/dist/OpenArcanum-x86_64.AppImage (runs `make bundle` first)
make bundle-smoke  # --smoke run of both (tests/test_bundle.py), under Xvfb
```

`make bundle` also works on Windows and macOS (CI runs it there; on macOS it
also makes `clients/desktop/dist/OpenArcanum.app`). The Windows installer and the
`.dmg` are built by the scripts above on CI runners; they need Inno Setup,
`signtool`, `codesign`, `hdiutil` and `notarytool`, which only those OSes have.

`make check` runs `tests/test_bundle.py` too: each test is skipped only when
its artifact hasn't been built, and `tests/test_packaging.py` (scheme
registration in all three installers, AppDir layout, tool hash checks, icons)
always runs.

## The bundle (PyInstaller, onedir)

One folder, not one file: a one-file app unpacks Qt WebEngine (about 450 MB)
into a temporary folder on every start, and signing on macOS and Windows
expects real files. Same spec on every OS. Besides what PyInstaller finds
from the imports, the spec collects:

- `openarcanum/ui/`: the page, from the shared web UI in `clients/shared/ui/`
  (`openarcanum/paths.py` `UI_DIR` finds it there from source and here in the
  bundle). `app.py` loads it as a real `file://` URI
  (`FRONTEND_INDEX.as_uri()`), so no loopback HTTP server starts, in the
  bundle exactly as in development (`docs/architecture.md` §17).
  The smoke test proves the page loads from inside the bundle.
- `openarcanum/_vendor/` and every `openarcanum` submodule (some are imported
  lazily);
- pywebview's injected scripts (`webview/js/`) and its Qt backend, which it
  imports by name;
- Qt WebEngine: `QtWebEngineProcess`, `resources/` and the
  `qtwebengine_locales`, through PyInstaller's PySide6 hooks;
- protobuf's upb module and vodozemac's native module;
- the community server, `servers/community` (`community.exe` on Windows):
  the app runs it to host communities on this computer and saves it for
  "Host on your own server" (`openarcanum/hosting.py`, docs/user-guide.md).
  `make bundle` builds it first (`make bundle-server`: static, stripped,
  `-trimpath`, for the OS and architecture the bundle is built on, into
  `clients/desktop/build/servers/`); the spec refuses to build without it. It adds
  about 15 MB to the bundle (about 6 MB compressed in the AppImage and the
  installers), and every bundle job sets up Go for it. A built app runs only
  that bundled copy. From source the app
  uses `bin/community` from `make build` (`scripts/desktop.sh` builds it),
  or `$OPENARCANUM_COMMUNITY_BIN`. `tests/test_bundle.py` checks the bundle
  carries it and that it runs. It is linked with the release version
  (`community version`), like every Go binary (`docs/release.md` "Versions");
- `licenses/`: the project's licence (`LICENSE.txt`, AGPL-3.0-only),
  `THIRD_PARTY_NOTICES.md`, the texts in `LICENSES/` (LGPL-3.0 and GPL-3.0
  for Qt, CC BY 3.0 US for the EFF word list), and every bundled
  component's own licence files with an `INDEX.txt` (name, version,
  licence): each runtime Python distribution's from its `.dist-info`, the
  Python runtime's, and each Go module linked into the community server.
  `make bundle-licenses` (part of `make bundle`) collects them into
  `clients/desktop/build/licenses/` with `packaging/licenses.py`, from the
  runtime requirement list (`uv export --no-dev`) and `go list -deps`; the
  spec refuses to build without them, and `tests/test_bundle.py` checks
  they are in the bundle. The PySide6 wheels carry no licence file, so
  `INDEX.txt` points them at `LICENSES/`.

**Version metadata.** `openarcanum.__version__` is the release version
(`VERSION`). Where only numbers are allowed, the build uses `YYYY.MM.PATCH`
without a `-beta.N`: the macOS `CFBundleShortVersionString` and
`CFBundleVersion` (the full version is in `OpenArcanumReleaseVersion`), and the
Windows executable's version resource (`FileVersion`/`ProductVersion`
strings carry the full version) and the installer's `VersionInfoVersion`.

pywebview's other backends (GTK, Cocoa, WinForms, CEF, Android) are left out.
The QML plugin tree and the Qt libraries nothing links to any more (3D,
Charts, Quick Controls and so on, about 120 MB, which PyInstaller's QtQml
hook drags in) are dropped: on Linux and Windows the spec keeps only the Qt
libraries reachable from what is left; on macOS (frameworks) only the QML
tree is dropped. The Linux bundle is about 500 MB, the AppImage 185 MB.

A bundled app has no `python -c`, so the check for Chromium's Linux sandbox
(user namespaces) runs the app itself with `--probe-user-namespaces`.

## Linux: AppImage

**Why AppImage, not Flatpak.** An AppImage is one file that runs on any distribution with
a recent enough glibc, needs no runtime or repository, can be built and
tested here without root, and replaces itself atomically, which is what the
updater does (`docs/release.md`). A Flatpak would bring its own sandbox
(portals for the microphone, camera and screen, which QtWebEngine's capture
would have to go through) and its own update channel (Flathub or a repo),
duplicating the signed-manifest updater. Flatpak can be added later from the
same bundle.

`build_appimage.py` downloads **appimagetool 1.9.1** and the **type2 runtime
20251108** from github.com/AppImage, checks their SHA-256 against the
values in the script (which match the digests GitHub publishes for those
release assets) and refuses anything else, then packs this AppDir:

```
AppRun                          exec usr/lib/openarcanum/OpenArcanum "$@"
openarcanum.desktop, openarcanum.png, .DirIcon      (the 256 px icon)
usr/lib/openarcanum/               the PyInstaller bundle
usr/share/applications/openarcanum.desktop
usr/share/icons/hicolor/NxN/apps/openarcanum.png   N = 16, 24, 32, 48, 64, 128, 256, 512
usr/share/icons/hicolor/scalable/apps/openarcanum.svg
```

appimagetool runs extracted (`APPIMAGE_EXTRACT_AND_RUN=1`), so no FUSE and no
root are needed. The release builds on Ubuntu 22.04 (glibc 2.35), so the
AppImage needs glibc 2.35 or newer. One built locally needs at least the
glibc of the machine that built it, so build releases on the oldest
distribution you support.

The `.desktop` entry has `MimeType=x-scheme-handler/openarcanum` and
`Exec=OpenArcanum -- %u`. An AppImage doesn't install anything by itself: desktop
integration tools (AppImageLauncher, Gear Lever, appimaged) install the entry
with `Exec` pointing at the AppImage, which registers the scheme. Without one,
`docs/manual-tests/installers.md` gives the two commands that do it.

## Windows: Inno Setup installer

`openarcanum.iss` installs the bundle **per user by default** (no administrator
rights; installing for all users is offered too), in `%LOCALAPPDATA%\Programs\OpenArcanum`,
with a Start menu shortcut and an optional desktop shortcut. It registers
`openarcanum://` under `Software\Classes\openarcanum` in HKCU (HKLM for an
all-users install): `"…\OpenArcanum.exe" -- "%1"`. The `--` matters: Windows
substitutes `%1` without escaping, so a caller that passes a raw link with
quotes and spaces in it could otherwise add options such as `--home` or
`--debug`; after `--` everything is a positional argument, and anything
besides the one link makes the app refuse to start. On an upgrade it deletes the old
`_internal\` first so dropped files don't linger, closes a running copy
(Restart Manager), and after a **silent** upgrade (the updater runs
`/SILENT /SUPPRESSMSGBOXES /NORESTART`) starts the new version. The AppId GUID
in the script must never change: upgrades and the uninstaller find the
installation by it.

`build_installer.sh` (Git Bash on the runner, Inno Setup from the image or
installed with Chocolatey):

- with `WINDOWS_CERT_PFX` (base64 of the code-signing PFX) and
  `WINDOWS_CERT_PASSWORD`: signs `OpenArcanum.exe` inside the bundle, the
  installer and its uninstaller (Inno's `SignTool` with `SignedUninstaller`)
  with `signtool sign /fd sha256 /tr <timestamp> /td sha256`, then
  `signtool verify /pa` both. `WINDOWS_TIMESTAMP_URL` overrides the RFC 3161
  timestamp service (default DigiCert's public one);
- without them: builds unsigned and logs a workflow notice saying so
  (SmartScreen warns about unsigned installers).

`test_installer.sh` installs into a scratch folder, checks the `openarcanum://`
registration, smoke-runs the installed app, installs again silently and checks
the app was restarted, uninstalls and checks the files and the registration
are gone.

## macOS: app bundle and .dmg

PyInstaller's `BUNDLE` makes `OpenArcanum.app` with these `Info.plist` entries:
bundle id `org.openarcanum.OpenArcanum` (never change it after the first
signed release: macOS ties permissions and the keychain to it), minimum macOS 14 (what
the PySide6 wheels need), `CFBundleURLTypes` for the `openarcanum` scheme, and
usage strings for the microphone (`NSMicrophoneUsageDescription`), camera
(`NSCameraUsageDescription`), screen recording
(`NSScreenCaptureUsageDescription`) and system audio
(`NSAudioCaptureUsageDescription`), which calls use. macOS asks for screen
recording in its own prompt and System Settings; the string is shown where
macOS supports one.

A link opened while the app runs (or that starts it) arrives as a URL-open
event, not an argument; `openarcanum/instance.py` handles it.

**Hardened runtime and entitlements** (`entitlements.plist`, applied to every
binary including `QtWebEngineProcess.app`): `cs.allow-jit` and
`cs.allow-unsigned-executable-memory` (Chromium's V8), `device.audio-input`
and `device.camera` (calls). Nothing else; the app isn't sandboxed. Library
validation stays on: PyInstaller re-signs every Mach-O in the bundle with the
Developer ID, so all of them carry the app's Team ID; a signed build checks
that (and that `disable-library-validation` is absent), and the smoke run
from the `.dmg` shows everything loads under it. If a future library fails to
load there, name it in `entitlements.plist` before turning the exception
back on.

`build_dmg.sh`:

- with `APPLE_DEVELOPER_ID_P12` (base64 of the Developer ID Application
  certificate and key) and `APPLE_DEVELOPER_ID_PASSWORD`: imports them into a
  temporary keychain, builds the bundle with `OPENARCANUM_CODESIGN_IDENTITY` set,
  so PyInstaller signs every binary with `--options runtime --timestamp` and
  the entitlements, verifies with `codesign --verify --deep --strict`, checks
  the hardened-runtime flag and the entitlements, and signs the `.dmg`;
- with `APPLE_ID`, `APPLE_TEAM_ID` and `APPLE_APP_PASSWORD` (an app-specific
  password) as well: notarizes the app (`notarytool submit --wait`), staples
  it, builds the `.dmg`, notarizes and staples that, and checks it with
  `stapler validate` and `spctl --assess`;
- without the certificate: ad-hoc signed, not notarized, with a workflow
  notice (Gatekeeper blocks a downloaded copy; right-click → Open works).

The `.dmg` holds exactly `OpenArcanum.app` and an `Applications` link, which is
what the updater expects. `test_dmg.sh` mounts it, checks that and the
`Info.plist` entries, and smoke-runs the app from the image.

**macOS architecture.** The build is Apple silicon (arm64) only. PySide6,
shiboken6 and protobuf ship universal2 wheels, but a universal2 app needs
every native module as universal2 or both slices, and `cryptography` 50
publishes no x86_64 or universal2 macOS wheel for CPython 3.14 (only
`macosx_11_0_arm64`, checked in `uv.lock` on 2026-10-26), nor do `cffi` and
`vodozemac` publish universal2 ones. An Intel build would need cryptography
built from source (Rust, OpenSSL) on an Intel runner. The release manifest still
uses the updater's platform key `macos-universal` (`docs/release.md`) for this
`.dmg`. Intel Macs are not supported; supporting them would take a release
job on an Intel runner that builds those wheels from source, and separate
manifest keys for the two builds in `openarcanum/updates.py` and
`.github/workflows/release.yml`.

## Release workflow

`.github/workflows/release.yml` runs on a `v<VERSION>` tag (or by hand) and
builds, in parallel, the Linux AppImage and `.tar.gz` (ubuntu-22.04), the
Windows installer (windows-latest) and the macOS `.dmg` (macos-latest,
Apple silicon), each checked as in ci.yml's `bundle` job, plus the static
server binaries; then the signed update manifest and a draft GitHub
Release. The jobs, the file names, the secrets and what happens without
them are in `docs/release.md` ("Making a release", "What the repository
owner configures", "Without the secrets").

## Secrets and variables

| Name | Kind | Used for |
|---|---|---|
| `WINDOWS_CERT_PFX`, `WINDOWS_CERT_PASSWORD` | repository secret | Authenticode signing (`signtool`) |
| `WINDOWS_TIMESTAMP_URL` | env (optional) | RFC 3161 timestamp service |
| `APPLE_DEVELOPER_ID_P12`, `APPLE_DEVELOPER_ID_PASSWORD` | repository secret | `codesign` with the Developer ID Application identity |
| `APPLE_ID`, `APPLE_TEAM_ID`, `APPLE_APP_PASSWORD` | repository secret | `notarytool` + `stapler` |
| `RELEASE_SIGNING_KEY` | secret of the `release` environment (required reviewers) | the update manifest's signature |
| `RELEASE_BASE_URL`, `RELEASE_MANIFEST_DAYS` | repository variable (optional) | manifest URLs (default: the GitHub Release) and lifetime |
