"""Self-test for scripts/check-markers: the check catches every marker
kind, allows each allowed case, holds the vendored Noise package to exactly
the lines its VENDORED.md lists, and passes on the current tree.
"""

from __future__ import annotations

import importlib.machinery
import importlib.util
import subprocess
import sys
import textwrap
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[3]
SCRIPT = ROOT / "scripts" / "check-markers"


def _load():
    loader = importlib.machinery.SourceFileLoader("check_markers", str(SCRIPT))
    spec = importlib.util.spec_from_loader("check_markers", loader)
    mod = importlib.util.module_from_spec(spec)
    sys.modules["check_markers"] = mod
    loader.exec_module(mod)
    return mod


cm = _load()


def kinds(path: str, text: str) -> list[str]:
    return [f.kind for f in cm.scan_text(path, textwrap.dedent(text))]


# Spelled in pieces so this file reads cleanly to anyone grepping the tree.
TD, FM = "TO" + "DO", "FIX" + "ME"


@pytest.mark.parametrize(
    ("path", "line", "kind"),
    [
        ("internal/x/x.go", f"// {TD}: handle errors", "TODO"),
        ("internal/x/x.go", f"x := 1 // {FM}", "FIXME"),
        ("clients/desktop/src/openarcanum/x.py", f"# {TD}(sam) later", "TODO"),
        ("clients/desktop/src/openarcanum/x.py", "# an Interim scheme", "interim"),
        ("clients/desktop/src/openarcanum/x.py", 'name = "PLACEHOLDER"', "placeholder"),
        ("internal/x/x.go", "// placeholders until launch", "placeholder"),
        ("clients/desktop/src/openarcanum/x.py", "# good enough For  Now", "for now"),
        ("internal/x/x.go", 'panic("not implemented")', "not implemented"),
        ("internal/x/x.go", 'return errors.New("unimplemented")', "not implemented"),
        ("internal/x/x.go", "// stub", "stub"),
        ("clients/shared/ui/js/x.js", "throw new Error('not yet implemented');", "not implemented"),
        ("clients/desktop/src/openarcanum/x.py", "        raise NotImplementedError", "NotImplementedError"),
        ("clients/desktop/src/openarcanum/x.py", "        raise NotImplementedError('later')", "NotImplementedError"),
        ("Makefile", f"# {TD} add target", "TODO"),
        ("scripts/dev-up.sh", "echo interim", "interim"),
        # Outside UI files, an HTML-looking placeholder is still a marker.
        ("clients/desktop/src/openarcanum/x.py", "x = {'placeholder': 1}", "placeholder"),
        # In UI files, prose placeholders are still markers.
        ("clients/shared/ui/js/x.js", "// placeholder text until design lands", "placeholder"),
        ("clients/shared/ui/index.html", "<p>Placeholder</p>", "placeholder"),
    ],
)
def test_catches_line_markers(path, line, kind):
    assert kind in kinds(path, line + "\n")


def test_catches_python_stub_bodies():
    src = """
    def a():
        pass

    async def b(x):
        ...

    class C:
        def m(self):
            pass
    """
    found = cm.scan_text("clients/desktop/src/openarcanum/x.py", textwrap.dedent(src))
    assert [(f.kind, f.line) for f in found] == [("stub function", 2), ("stub function", 5), ("stub function", 9)]


@pytest.mark.parametrize(
    ("path", "text"),
    [
        # Only uppercase TODO counts.
        ("internal/x/x.go", "// a todo list of the user's"),
        # HTML input placeholders in UI code.
        ("clients/shared/ui/js/x.js", "const q = h('input', { type: 'search', placeholder: 'Search' });"),
        ("clients/shared/ui/js/x.js", "h('input', {placeholder: opts.placeholder || ''})"),
        ("clients/shared/ui/js/x.js", "input.placeholder = 'Message';"),
        ("clients/shared/ui/index.html", '<input id="q" placeholder="Search">'),
        ("clients/shared/ui/styles.css", "input::placeholder { color: var(--muted); }"),
        ("clients/shared/ui/styles.css", "input:placeholder-shown { opacity: .8; }"),
        # Ordinary words that contain the markers' letters.
        ("clients/desktop/src/openarcanum/x.py", "return NotImplemented  # rich comparison protocol"),
        ("clients/desktop/src/openarcanum/x.py", "stubborn = 1"),
        ("internal/x/x.go", "// Interims are not a word we use"),
    ],
)
def test_allows(path, text):
    assert kinds(path, text + "\n") == []


def test_allows_declarations_and_documented_noops():
    src = '''
    import abc
    from typing import Protocol, overload

    class T(Protocol):
        def send(self, b: bytes) -> None: ...
        async def recv(self) -> bytes: ...

    class Base(abc.ABC):
        @abc.abstractmethod
        def run(self): ...

    @overload
    def f(x: int) -> int: ...

    class Null:
        def play(self, pcm):
            """Discard the frame."""

        def stop(self):
            """Nothing to release."""
            pass
    '''
    assert kinds("clients/desktop/src/openarcanum/x.py", src) == []


@pytest.mark.parametrize(
    "path",
    [
        "internal/x/x_test.go",
        "clients/desktop/tests/test_x.py",
        "internal/wire/x.pb.go",
        "clients/desktop/src/openarcanum/wire/x_pb2.py",
        "docs/examples/x.py",
        "docs/x.md",
        "docs/protocol.md",
        "docs/threat-model.md",
        "clients/desktop/uv.lock",
        "go.sum",
        "internal/community/testdata/v1/x.json",
        "docs/settings.md",
    ],
)
def test_exempt_paths(path):
    assert cm.is_exempt(path)


def test_scanned_paths():
    paths = [
        "internal/x/x.go",
        "clients/desktop/src/openarcanum/x.py",
        "Makefile",
        "scripts/check-x",
        ".github/workflows/ci.yml",
    ]
    for path in paths:
        assert cm.is_code(path) and not cm.is_exempt(path), path


VENDORED = f"""# Vendored

## Upstream markers

| File:line | Text | Why |
|---|---|---|
| `a.py:3` | `{TD}: Document` | doc |
| `b.py:3,5` | `# Maybe handle ...` | comment |
| `c.py` | `raise NotImplementedError` | abstract |

## Updating
"""


def _vendor_repo(tmp_path: Path, files: dict[str, str]) -> Path:
    subprocess.run(["git", "init", "-q", str(tmp_path)], check=True)
    base = tmp_path / cm.VENDOR_DIR
    base.mkdir(parents=True)
    (base / "VENDORED.md").write_text(VENDORED)
    for name, text in files.items():
        (base / name).write_text(textwrap.dedent(text))
    return tmp_path


A_PY = f'''
class P:
    """{TD}: Document"""
'''
B_PY = """
x = 1
y = 2  # Maybe handle bad input
z = 3
w = 4  # Maybe handle the other case
"""
C_PY = """
import abc

class K(abc.ABC):
    @abc.abstractmethod
    def f(self):
        raise NotImplementedError
"""


def test_vendored_listed_occurrences_pass(tmp_path):
    root = _vendor_repo(tmp_path, {"a.py": A_PY, "b.py": B_PY, "c.py": C_PY})
    findings, errors = cm.scan_repo(root)
    assert findings == [] and errors == []


def test_vendored_new_marker_fails(tmp_path):
    root = _vendor_repo(tmp_path, {"a.py": A_PY + f"# {FM} new\n", "b.py": B_PY, "c.py": C_PY})
    findings, errors = cm.scan_repo(root)
    assert [(f.path.rsplit("/", 1)[1], f.kind) for f in findings] == [("a.py", "FIXME")]
    assert errors == []


def test_vendored_unlisted_not_implemented_fails(tmp_path):
    # Same text as the listed one, but not in an abstract method.
    c = C_PY + "\ndef g():\n    raise NotImplementedError\n"
    root = _vendor_repo(tmp_path, {"a.py": A_PY, "b.py": B_PY, "c.py": c})
    findings, _ = cm.scan_repo(root)
    assert [(f.line, f.kind) for f in findings] == [(10, "NotImplementedError")]


def test_vendored_moved_marker_fails(tmp_path):
    # The listed line moved: the old entry is stale and the new line is unlisted.
    root = _vendor_repo(tmp_path, {"a.py": "\n" + A_PY, "b.py": B_PY, "c.py": C_PY})
    findings, errors = cm.scan_repo(root)
    assert [(f.line, f.kind) for f in findings] == [(4, "TODO")]
    assert any("a.py:3" in e for e in errors)


def test_vendored_stale_comment_entry_fails(tmp_path):
    b = B_PY.replace("Maybe handle the other case", "ok")
    root = _vendor_repo(tmp_path, {"a.py": A_PY, "b.py": b, "c.py": C_PY})
    _, errors = cm.scan_repo(root)
    assert any("b.py:5" in e for e in errors)


def test_untracked_new_file_is_scanned(tmp_path):
    root = _vendor_repo(tmp_path, {"a.py": A_PY, "b.py": B_PY, "c.py": C_PY})
    (root / "new.go").write_text(f"package x // {TD}\n")
    findings, _ = cm.scan_repo(root)
    assert [f.path for f in findings] == ["new.go"]


def test_current_tree_is_clean():
    proc = subprocess.run([sys.executable, str(SCRIPT), str(ROOT)], capture_output=True, text=True)
    assert proc.returncode == 0, proc.stderr
