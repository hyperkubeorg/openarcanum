"""JavaScript unit tests (tests/js/*.test.js and *.test.mjs) under Node's test runner:
code that runs in the webview but must be verified on its own, such as SFrame
against the RFC 9605 vectors."""

import shutil
import subprocess
from pathlib import Path

import pytest


@pytest.mark.skipif(shutil.which("node") is None, reason="needs Node.js")
def test_js_unit_tests():
    js = Path(__file__).parent / "js"
    tests = sorted(str(p) for p in [*js.glob("*.test.js"), *js.glob("*.test.mjs")])
    assert tests
    p = subprocess.run(["node", "--test", "--test-reporter=tap", *tests], capture_output=True, text=True, timeout=300)
    assert p.returncode == 0, p.stdout[-4000:] + p.stderr[-2000:]
    assert "# fail 0" in p.stdout
