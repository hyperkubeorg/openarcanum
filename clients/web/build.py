#!/usr/bin/env python3
"""Build the web client: a folder of static files the site serves at /app/.

    clients/web/build.py --vodozemac WHEEL --out DIR [--cache DIR]

WHEEL is vodozemac built for Pyodide (clients/web/build-vodozemac.sh). The
rest is downloaded and checked against the hashes pinned here: Pyodide's npm
package (its pyodide-lock.json then pins every Pyodide package taken from the
Pyodide CDN) and the two pure-Python wheels from PyPI. Nothing is loaded from
anywhere but the site at run time.

The output:
    index.html, styles.css, js/, brand/, vendor/, strings/   the shared UI
    web.js, web.css, worker.js                               the browser glue
    pyodide/        Pyodide and the packages from its distribution
    wheels/         vodozemac, protobuf, spake2
    app.zip         the openarcanum package and clients/web/python
    manifest.json   what worker.js loads

Python standard library only.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import io
import json
import re
import shutil
import sys
import tarfile
import urllib.request
import zipfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
CLIENTS = HERE.parent
UI = CLIENTS / "shared" / "ui"
PACKAGE = CLIENTS / "desktop" / "src" / "openarcanum"

PYODIDE_VERSION = "314.0.7"
PYODIDE_NPM = f"https://registry.npmjs.org/pyodide/-/pyodide-{PYODIDE_VERSION}.tgz"
PYODIDE_NPM_SHA512 = "0YvXxEhfEdpLfb/XkM2BFAeMROq0iMUX2bzzH9pOttyMcWkwq+HbE5uyuGD82LN7y2q+SNvi/6V5JEsOlD2R1A=="
PYODIDE_CDN = f"https://cdn.jsdelivr.net/pyodide/v{PYODIDE_VERSION}/full/"
PYODIDE_FILES = ("pyodide.mjs", "pyodide.asm.mjs", "pyodide.asm.wasm", "python_stdlib.zip", "pyodide-lock.json")
# From Pyodide's distribution, with what they depend on.
PYODIDE_PACKAGES = ("cryptography", "platformdirs", "pyodide-http")

# Keep in step with clients/desktop/uv.lock.
WHEELS = {
    "protobuf-7.36.2-py3-none-any.whl": (
        "https://files.pythonhosted.org/packages/e4/04/d52c7016b04b6c5108f26691f9d33ec82a9b65d041f1a9c771137693d618/"
        "protobuf-7.36.2-py3-none-any.whl",
        "bdb3a345d48db958e6ce1f18e508beb0cc981d64f24088427549c866cd039f1e",
    ),
    "spake2-0.9-py3-none-any.whl": (
        "https://files.pythonhosted.org/packages/d2/42/a4e93badf46c20d862ce298d8873451e19f18141c6811ae7fdfaef0ea107/"
        "spake2-0.9-py3-none-any.whl",
        "2d7cbe936519c4172866649ddceefdec08e3d9b3ce4c8a75bbf84d6bdd07a2fb",
    ),
}

# The page's policy (the site sends a matching header): scripts and the
# worker only from the site, WebAssembly allowed, connections to the
# maintainer nodes and relays the person uses (https and wss anywhere).
CSP = (
    "default-src 'none'; script-src 'self' 'wasm-unsafe-eval'; worker-src 'self'; style-src 'self'; "
    "img-src 'self' data: blob:; media-src 'self' data: blob:; connect-src 'self' https: wss:; font-src 'self'; "
    "frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'"
)


def fetch(url: str, cache: Path) -> bytes:
    cached = cache / hashlib.sha256(url.encode()).hexdigest()
    if cached.exists():
        return cached.read_bytes()
    print(f"  downloading {url}", file=sys.stderr)
    with urllib.request.urlopen(url, timeout=120) as resp:
        data = resp.read()
    cache.mkdir(parents=True, exist_ok=True)
    cached.write_bytes(data)
    return data


def check(name: str, data: bytes, algo: str, want: str) -> None:
    h = hashlib.new(algo, data)
    got = base64.b64encode(h.digest()).decode() if algo == "sha512" else h.hexdigest()
    if got != want:
        sys.exit(f"build.py: {name}: {algo} is {got}, expected {want}")


def pyodide(out: Path, cache: Path) -> list[str]:
    """Pyodide's runtime and PYODIDE_PACKAGES with their dependencies; returns
    the package names for manifest.json."""
    tgz = fetch(PYODIDE_NPM, cache)
    check(PYODIDE_NPM, tgz, "sha512", PYODIDE_NPM_SHA512)
    dest = out / "pyodide"
    dest.mkdir(parents=True)
    with tarfile.open(fileobj=io.BytesIO(tgz)) as tar:
        for name in PYODIDE_FILES:
            (dest / name).write_bytes(tar.extractfile(f"package/{name}").read())
    lock = json.loads((dest / "pyodide-lock.json").read_text())["packages"]
    todo, names = list(PYODIDE_PACKAGES), []
    while todo:
        name = todo.pop()
        if name in names:
            continue
        names.append(name)
        todo.extend(lock[name].get("depends", []))
    for name in names:
        entry = lock[name]
        data = fetch(PYODIDE_CDN + entry["file_name"], cache)
        check(entry["file_name"], data, "sha256", entry["sha256"])
        (dest / entry["file_name"]).write_bytes(data)
    return sorted(names)


def wheels(out: Path, cache: Path, vodozemac: Path) -> list[str]:
    dest = out / "wheels"
    dest.mkdir(parents=True)
    for name, (url, sha256) in WHEELS.items():
        data = fetch(url, cache)
        check(name, data, "sha256", sha256)
        (dest / name).write_bytes(data)
    shutil.copyfile(vodozemac, dest / vodozemac.name)
    return [*WHEELS, vodozemac.name]


def app_zip(out: Path) -> str:
    """The openarcanum package and the web modules, at their repository paths
    (so paths.UI_DIR finds strings/ the way it does in a checkout)."""
    root = CLIENTS.parent
    sources = [PACKAGE, HERE / "python", UI / "strings"]
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w", zipfile.ZIP_DEFLATED) as z:
        for src in sources:
            for f in sorted(src.rglob("*")):
                if f.is_file() and "__pycache__" not in f.parts and f.suffix != ".pyc":
                    z.write(f, f.relative_to(root).as_posix())
    data = buf.getvalue()
    # Named by content, so a new build is never served from a stale cache.
    name = f"app-{hashlib.sha256(data).hexdigest()[:16]}.zip"
    (out / name).write_bytes(data)
    return name


def page(out: Path) -> None:
    shutil.copytree(UI, out, dirs_exist_ok=True, ignore=shutil.ignore_patterns("__pycache__"))
    for f in ("web.js", "web.css", "worker.js"):
        shutil.copyfile(HERE / f, out / f)
    index = (out / "index.html").read_text()
    index, n = re.subn(
        r'<meta http-equiv="Content-Security-Policy" content="[^"]*">',
        f'<meta http-equiv="Content-Security-Policy" content="{CSP}">',
        index,
    )
    if n != 1:
        sys.exit("build.py: index.html: no Content-Security-Policy meta tag to replace")
    index = index.replace(
        '<link rel="stylesheet" href="styles.css">',
        '<link rel="stylesheet" href="styles.css">\n<link rel="stylesheet" href="web.css">',
    )
    index = index.replace(
        '<script src="js/bridge.js"></script>', '<script src="web.js"></script>\n<script src="js/bridge.js"></script>'
    )
    if "web.js" not in index or "web.css" not in index:
        sys.exit("build.py: index.html: couldn't add web.js and web.css")
    (out / "index.html").write_text(index)


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--vodozemac", type=Path, required=True, help="vodozemac wheel built for Pyodide")
    ap.add_argument("--out", type=Path, required=True)
    ap.add_argument("--cache", type=Path, default=HERE / ".cache", help="downloads, kept between builds")
    args = ap.parse_args()
    if not args.vodozemac.name.endswith("_wasm32.whl"):
        sys.exit("build.py: --vodozemac must be a wheel built for Pyodide (…_wasm32.whl)")
    if args.out.exists():
        shutil.rmtree(args.out)
    args.out.mkdir(parents=True)
    page(args.out)
    manifest = {
        "packages": pyodide(args.out, args.cache),
        "wheels": wheels(args.out, args.cache, args.vodozemac),
        "app": app_zip(args.out),
    }
    (args.out / "manifest.json").write_text(json.dumps(manifest, indent=1) + "\n")
    print(f"web client built in {args.out}")


if __name__ == "__main__":
    main()
