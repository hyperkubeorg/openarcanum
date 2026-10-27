#!/bin/sh
# Build vodozemac (the Olm library the client uses for direct messages) for
# Pyodide: no browser build of it is published.
#
#   clients/web/build-vodozemac.sh OUTDIR
#
# Needs Python 3.14 (the version Pyodide 314 runs) and Rust 1.93.0 with the
# wasm32-unknown-emscripten target (`pyodide config get rust_toolchain`).
# pyodide-build fetches Pyodide's cross-build environment and Emscripten
# into ~/.cache/pyodide-build. The wheel lands in OUTDIR.
set -eu

PYODIDE_VERSION=314.0.7
PYODIDE_BUILD_VERSION=0.39.2
# Keep in step with clients/desktop/uv.lock.
VODOZEMAC_SDIST=https://files.pythonhosted.org/packages/f9/0a/ced0576c7e2bbc03f056c9791ae5edfbd0ac6daa66033368f947e89885ed/vodozemac-0.10.0.tar.gz
VODOZEMAC_SHA256=9602edb87ab7d16e7b4aad5a087473d8fb6b3ee99043f9aaf0f54bb78970dde9

out=$(mkdir -p "$1" && cd "$1" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

python3 -m venv "$work/venv"
"$work/venv/bin/pip" install --quiet "pyodide-build==$PYODIDE_BUILD_VERSION"
PATH="$work/venv/bin:$PATH"
pyodide xbuildenv install "$PYODIDE_VERSION"
pyodide xbuildenv install-emscripten
want=$(pyodide config get rust_toolchain)
have=$(rustc --version | cut -d' ' -f2)
[ "$want" = "$have" ] || { echo "build-vodozemac.sh: Pyodide $PYODIDE_VERSION wants Rust $want, this is $have" >&2; exit 1; }

curl -fsSL -o "$work/vodozemac.tar.gz" "$VODOZEMAC_SDIST"
echo "$VODOZEMAC_SHA256  $work/vodozemac.tar.gz" | sha256sum -c - >/dev/null
tar -xzf "$work/vodozemac.tar.gz" -C "$work"
# shellcheck disable=SC1090
. "$(pyodide config get emscripten_dir)/../../emsdk_env.sh" >/dev/null 2>&1
pyodide build "$work"/vodozemac-*/ -o "$out"
ls "$out"/vodozemac-*_wasm32.whl
