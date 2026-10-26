"""Self-test for scripts/check-lock: uv.lock pins every
package with sha256 hashes."""

from __future__ import annotations

import importlib.machinery
import importlib.util
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
SCRIPT = ROOT / "scripts" / "check-lock"

_loader = importlib.machinery.SourceFileLoader("check_lock", str(SCRIPT))
_spec = importlib.util.spec_from_loader("check_lock", _loader)
cl = importlib.util.module_from_spec(_spec)
_loader.exec_module(cl)

REG = {"registry": "https://pypi.org/simple"}
H = "sha256:" + "0" * 64


def test_flags_missing_hashes_and_unpinned_sources():
    lock = {
        "package": [
            {"name": "app", "version": "0.1.0", "source": {"editable": "."}},
            {"name": "ok", "version": "1", "source": REG, "sdist": {"url": "u", "hash": H}, "wheels": [{"hash": H}]},
            {"name": "nohash", "version": "1", "source": REG, "wheels": [{"url": "w", "hash": H}, {"url": "x"}]},
            {"name": "md5", "version": "1", "source": REG, "sdist": {"url": "s", "hash": "md5:abc"}},
            {"name": "empty", "version": "1", "source": REG},
            {"name": "gitdep", "version": "1", "source": {"git": "https://example.invalid/x"}},
            {"name": "nover", "source": REG, "sdist": {"hash": H}},
        ]
    }
    found = cl.problems(lock)
    flagged = {p.split(":", 1)[0] for p in found}
    assert flagged == {"nohash", "md5", "empty", "gitdep", "nover"}


def test_real_lock_is_pinned_with_hashes():
    proc = subprocess.run([sys.executable, str(SCRIPT)], capture_output=True, text=True)
    assert proc.returncode == 0, proc.stderr
