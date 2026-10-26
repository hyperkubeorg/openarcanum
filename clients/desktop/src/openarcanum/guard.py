"""Storage guard (docs/threat-model.md "Webview (UI)"), run by `make check`:

    python -m openarcanum.guard

Fails if UI code uses webview storage (localStorage, sessionStorage,
IndexedDB, cookies), or if the window is started without an explicit
`private_mode=True`, or with a `storage_path`.
"""

from __future__ import annotations

import ast
import re
import sys
from pathlib import Path

from openarcanum.paths import UI_DIR

PKG = Path(__file__).resolve().parent
UI = UI_DIR
FORBIDDEN = re.compile(r"\b(localStorage|sessionStorage|indexedDB|document\s*\.\s*cookie)\b")


def scan_ui(root: Path = UI) -> list[str]:
    problems = []
    for path in sorted(root.rglob("*")):
        if path.suffix not in {".html", ".js", ".css", ".mjs"}:
            continue
        for n, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
            if m := FORBIDDEN.search(line):
                where = f"{path.relative_to(root.parent)}:{n}"
                problems.append(f"{where}: UI code uses {m.group(1)} (webview storage is not allowed)")
    return problems


def scan_window(path: Path = PKG / "app.py") -> list[str]:
    tree = ast.parse(path.read_text(encoding="utf-8"))
    problems = []
    starts = 0
    for node in ast.walk(tree):
        if not isinstance(node, ast.Call):
            continue
        func = node.func
        name = func.attr if isinstance(func, ast.Attribute) else func.id if isinstance(func, ast.Name) else ""
        if name not in ("start", "create_window"):
            continue
        owner = func.value.id if isinstance(func, ast.Attribute) and isinstance(func.value, ast.Name) else ""
        if owner != "webview":
            continue
        kw = {k.arg: k.value for k in node.keywords}
        if "storage_path" in kw:
            problems.append(f"{path.name}:{node.lineno}: webview.{name} sets storage_path")
        if name == "start":
            starts += 1
            pm = kw.get("private_mode")
            if not (isinstance(pm, ast.Constant) and pm.value is True):
                problems.append(f"{path.name}:{node.lineno}: webview.start must pass private_mode=True explicitly")
            dbg = kw.get("debug")
            if dbg is not None and not (isinstance(dbg, ast.Constant) and dbg.value is False):
                problems.append(f"{path.name}:{node.lineno}: webview.start must not enable debug (remote debugging)")
    if starts == 0:
        problems.append(f"{path.name}: no webview.start call found")
    return problems


def main() -> int:
    problems = scan_ui() + scan_window()
    for p in problems:
        print(p, file=sys.stderr)
    if problems:
        print(f"storage guard: {len(problems)} problem(s)", file=sys.stderr)
        return 1
    print("storage guard: ok")
    return 0


if __name__ == "__main__":
    sys.exit(main())
