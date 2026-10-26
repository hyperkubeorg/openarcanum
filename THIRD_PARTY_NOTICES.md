# Third-party notices

This project is licensed under the GNU Affero General Public License, version 3
only (`AGPL-3.0-only`, the file `LICENSE`). It includes or links the components
below, each under its own licence.

Every desktop build carries this file, `LICENSE`, the texts in `LICENSES/`, and
the licence files of every bundled Python distribution and Go module with an
`INDEX.txt` listing name, version and licence (collected at build time by
`clients/desktop/packaging/licenses.py`). They are in the app's `licenses/`
folder: `_internal/licenses/` next to the program on Linux and Windows,
`OpenArcanum.app/Contents/Resources/licenses/` on macOS.

## Vendored in this repository

| Component | Licence | Where |
|---|---|---|
| Noise Protocol Framework for Python (`noiseprotocol`), by Piotr Lizończyk | MIT | `clients/desktop/src/openarcanum/_vendor/noise/` (`LICENSE`, local changes in `VENDORED.md`) |
| "EFF's Long Wordlist" by the Electronic Frontier Foundation (https://www.eff.org/dice) | CC BY 3.0 US (`LICENSES/CC-BY-3.0-US.txt`) | `clients/desktop/src/openarcanum/_vendor/eff_wordlist/` (`VENDORED.md`); attribution also in Settings → About |
| Lucide icons (and the Feather icons they derive from) | ISC (Feather: MIT) | `clients/shared/ui/vendor/lucide/` (`LICENSE`) |
| axe-core (accessibility tests only; not shipped) | MPL-2.0 | `clients/desktop/tests/vendor/axe/` |

## Bundled in the desktop app

| Component | Licence |
|---|---|
| CPython runtime | PSF-2.0 |
| Qt 6, Qt WebEngine (with Chromium), PySide6, shiboken6 | LGPL-3.0-only (`LICENSES/LGPL-3.0-only.txt`, which builds on `LICENSES/GPL-3.0-only.txt`); Chromium's own third-party licences are listed by Qt WebEngine's documentation |
| pywebview | BSD-3-Clause |
| QtPy | MIT |
| cryptography (with OpenSSL in its wheels) | Apache-2.0 OR BSD-3-Clause (OpenSSL: Apache-2.0) |
| cffi, pycparser | MIT-0 / BSD-3-Clause |
| protobuf | BSD-3-Clause |
| vodozemac (Python bindings of the Matrix Olm/Megolm library) | Apache-2.0 |
| spake2 | MIT |
| websockets | BSD-3-Clause |
| keyring, jaraco.*, more-itertools, platformdirs, jeepney, SecretStorage, bottle, proxy_tools, typing_extensions, packaging | MIT, BSD-3-Clause, PSF-2.0, or Apache-2.0 OR BSD-2-Clause (each one's own file in `licenses/python/`) |
| Windows only: pythonnet, clr-loader, pywin32-ctypes | MIT, MIT, BSD-3-Clause |
| macOS only: PyObjC (pyobjc-core and the framework wrappers pywebview uses) | MIT |
| The community server (`servers/community`): Go standard library and runtime, and the Go modules it links (filippo.io/age, coder/websocket, flynn/noise, golang.org/x/crypto, golang.org/x/sys, google.golang.org/protobuf, modernc.org/sqlite and its libc, and others) | BSD-3-Clause, MIT, Apache-2.0 (each one's own file in `licenses/go/`) |

**Qt and the LGPL.** The desktop app is a folder of separate files (PyInstaller
"onedir"), not a single executable: the Qt and PySide6 libraries are ordinary
shared libraries inside it (`_internal/PySide6/` on Linux and Windows,
`Contents/Frameworks/` on macOS), so a user can replace them with a modified
build of the same Qt version. Qt's source is at https://download.qt.io/ and
https://code.qt.io/; PySide6's at https://code.qt.io/pyside/pyside-setup.git.
This project's own source, including the build scripts, is at
https://github.com/hyperkubeorg/openarcanum.

## The servers and container images

The server binaries and the images `ghcr.io/hyperkubeorg/openarcanum-*` contain
the Go standard library and the Go modules in `go.mod` (BSD-3-Clause, MIT,
Apache-2.0), on Google's distroless `static-debian12` base (Debian packages
`ca-certificates`, `tzdata`, `base-files`, `netbase`, under their Debian
licences; listed in each image's SBOM attestation).
