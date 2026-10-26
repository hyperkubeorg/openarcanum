"""Collect the licences that ship inside the desktop bundle (docs/packaging.md).

    python packaging/licenses.py OUT --requirements FILE [--go GO]

Run by `make bundle` from clients/desktop/ with the client's environment
(the one PyInstaller bundles from), after `make bundle-server`. Writes OUT/:

  LICENSE.txt                   the project's licence (AGPL-3.0-only)
  THIRD_PARTY_NOTICES.md        what is bundled and under which licence
  LICENSES/                     full texts the notices refer to (LGPL-3.0,
                                GPL-3.0, CC-BY-3.0-US) from the repository
  python/<name>/                each bundled Python distribution's own licence
                                files (from its installed .dist-info), and
                                the Python runtime's licence
  go/<module>/                  each Go module compiled into servers/community
  INDEX.txt                     one line per component: name, version, licence,
                                where its texts are

FILE is the runtime requirement list, `uv export --no-dev --no-emit-project
--format requirements-txt` (what `make bundle` passes): every distribution
named there that is installed in this environment is listed. A distribution
without licence files of its own (PySide6, shiboken6: their wheels carry
none) is listed with the licence its metadata declares; for LGPL and GPL that
text is in LICENSES/. Standard library only.
"""

from __future__ import annotations

import argparse
import importlib.metadata as md
import re
import shutil
import subprocess
import sys
from pathlib import Path

CLIENT = Path(__file__).resolve().parent.parent
REPO = CLIENT.parent.parent
COMMUNITY_PKG = "./cmd/community"

LICENSE_NAME = re.compile(r"^(licen[cs]e|copying|notice|authors|copyright|patents)", re.IGNORECASE)
REQ_NAME = re.compile(r"^([A-Za-z0-9][A-Za-z0-9._-]*)==")


def runtime_names(requirements: Path) -> list[str]:
    """Distribution names in a `uv export` requirements file (markers ignored:
    a distribution not installed for this platform is skipped later)."""
    names = []
    for line in requirements.read_text().splitlines():
        m = REQ_NAME.match(line.strip())
        if m:
            names.append(m[1])
    return sorted(set(names), key=str.lower)


def dist_licence(dist: md.Distribution) -> str:
    meta = dist.metadata
    expr = meta.get("License-Expression")
    if expr:
        return expr
    lic = (meta.get("License") or "").strip()
    if lic and "\n" not in lic and len(lic) < 100:
        return lic
    classifiers = [c.split(" :: ")[-1] for c in meta.get_all("Classifier") or [] if c.startswith("License ::")]
    return " / ".join(classifiers) or "see files"


def dist_licence_files(dist: md.Distribution) -> list[tuple[str, Path]]:
    """(relative name, path) of the licence files inside a .dist-info."""
    out = []
    for f in dist.files or []:
        parts = Path(str(f)).parts
        if not parts or not parts[0].endswith(".dist-info"):
            continue
        rel = Path(*parts[1:])
        if rel.parts and (rel.parts[0] in ("licenses", "sboms") or LICENSE_NAME.match(rel.name)):
            path = Path(str(dist.locate_file(f)))
            if path.is_file():
                out.append((rel.as_posix(), path))
    return out


def collect_python(out: Path, names: list[str], index: list[str]) -> None:
    for name in names:
        try:
            dist = md.distribution(name)
        except md.PackageNotFoundError:
            continue  # not for this platform
        dest = out / "python" / dist.metadata["Name"]
        files = dist_licence_files(dist)
        for rel, path in files:
            target = dest / rel
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(path, target)
        lic = dist_licence(dist)
        if files:
            where = f"python/{dist.metadata['Name']}/"
        elif "GPL" in lic:
            where = "LICENSES/LGPL-3.0-only.txt, LICENSES/GPL-3.0-only.txt (the wheel carries no licence file)"
        else:
            where = "(the wheel carries no licence file)"
        index.append(f"python  {dist.metadata['Name']}  {dist.version}  {lic}  {where}")
    # The interpreter itself (PSF-2.0): LICENSE.txt next to the standard library.
    ver = f"python{sys.version_info.major}.{sys.version_info.minor}"
    for cand in (Path(sys.base_prefix) / "lib" / ver / "LICENSE.txt", Path(sys.base_prefix) / "LICENSE.txt"):
        if cand.is_file():
            dest = out / "python" / "CPython"
            dest.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(cand, dest / "LICENSE.txt")
            index.append(f"python  CPython  {sys.version.split()[0]}  PSF-2.0  python/CPython/")
            break
    else:
        raise SystemExit(f"licenses.py: the Python runtime's LICENSE.txt is not under {sys.base_prefix}")


def collect_go(out: Path, go: str, index: list[str]) -> None:
    """Every module linked into the community server, with its licence files."""
    fmt = "{{with .Module}}{{.Path}}\t{{.Version}}\t{{.Dir}}{{end}}"
    r = subprocess.run(
        [go, "list", "-deps", "-f", fmt, COMMUNITY_PKG], cwd=REPO, capture_output=True, text=True, check=True
    )
    goroot = subprocess.run([go, "env", "GOROOT"], capture_output=True, text=True, check=True).stdout.strip()
    goversion = subprocess.run([go, "env", "GOVERSION"], capture_output=True, text=True, check=True).stdout.strip()
    modules = {}
    for line in r.stdout.splitlines():
        if line.strip():
            path, version, folder = line.split("\t")
            modules[path] = (version, folder)
    own = subprocess.run([go, "list", "-m"], cwd=REPO, capture_output=True, text=True, check=True).stdout.strip()
    modules.pop(own, None)
    modules["go (standard library and runtime)"] = (goversion, goroot)
    for path, (version, folder) in sorted(modules.items()):
        files = [f for f in Path(folder).iterdir() if f.is_file() and LICENSE_NAME.match(f.name)] if folder else []
        if not files and folder == goroot:
            # Distribution packages of Go move GOROOT's LICENSE (Arch:
            # /usr/share/licenses/go/); the official toolchain keeps it.
            alt = Path(goroot).parent.parent / "share" / "licenses" / "go"
            files = [f for f in alt.glob("*") if f.is_file() and LICENSE_NAME.match(f.name)]
        if not files:
            raise SystemExit(f"licenses.py: Go module {path} has no licence file in {folder}")
        dest = out / "go" / path.split(" ")[0]
        dest.mkdir(parents=True, exist_ok=True)
        for f in files:
            shutil.copyfile(f, dest / f.name)
        index.append(f"go      {path}  {version}  see files  go/{path.split(' ')[0]}/")


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("out", type=Path)
    ap.add_argument("--requirements", type=Path, required=True, help="uv export --no-dev ... output")
    ap.add_argument("--go", default="go")
    a = ap.parse_args()

    if a.out.exists():
        shutil.rmtree(a.out)
    a.out.mkdir(parents=True)
    shutil.copyfile(REPO / "LICENSE", a.out / "LICENSE.txt")
    shutil.copyfile(REPO / "THIRD_PARTY_NOTICES.md", a.out / "THIRD_PARTY_NOTICES.md")
    shutil.copytree(REPO / "LICENSES", a.out / "LICENSES")
    index = ["# kind  name  version  licence  texts (paths relative to this folder)"]
    collect_python(a.out, runtime_names(a.requirements), index)
    collect_go(a.out, a.go, index)
    (a.out / "INDEX.txt").write_text("\n".join(index) + "\n", encoding="utf-8")
    print(f"licenses.py: {len(index) - 1} components in {a.out}")


if __name__ == "__main__":
    main()
