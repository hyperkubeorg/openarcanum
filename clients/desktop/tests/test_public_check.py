"""scripts/public-check: it refuses the maintainers' private files, build
output and secrets, allows the product's own files (test-fixture keys
included), catches absolute development paths and the git user's email in text, fails
on a repository that tracks a private file, and passes on the current tree.
"""

from __future__ import annotations

import importlib.machinery
import importlib.util
import os
import shutil
import subprocess
import sys
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[3]
SCRIPT = ROOT / "scripts" / "public-check"

# Spelled in parts, so this file passes its own check.
HOME = "/home/" + "dev"
WS = "/work" + "space"


def _load():
    loader = importlib.machinery.SourceFileLoader("public_check", str(SCRIPT))
    spec = importlib.util.spec_from_loader("public_check", loader)
    mod = importlib.util.module_from_spec(spec)
    sys.modules["public_check"] = mod
    loader.exec_module(mod)
    return mod


pc = _load()


@pytest.mark.parametrize(
    "path",
    [
        ".dev/client-a/config/preferences.toml",
        "bin/relay",
        "dist/servers/linux-amd64/relay",
        "clients/desktop/dist/OpenArcanum.AppImage",
        "clients/desktop/build/x",
        "clients/desktop/.venv/bin/python",
        "clients/desktop/src/openarcanum/__pycache__/app.cpython-314.pyc",
        "load-results.jsonl",
        "release.pem",
        "deploy/config/server.key",
        ".env",
        "deploy/.env.production",
        "id_ed25519",
        "clients/desktop/packaging/windows/cert.pfx",
    ],
)
def test_excluded(path):
    assert pc.excluded_reason(path), path


@pytest.mark.parametrize(
    "path",
    [
        "README.md",
        "Makefile",
        ".gitignore",
        "deploy/container/Containerfile",
        "deploy/container/Containerfile.dockerignore",
        "docs/notes/plan.md",
        "clients/desktop/src/openarcanum/app.py",
        "internal/community/testdata/v1/server.key",
        "clients/desktop/packaging/linux/build_appimage.py",
        "scripts/build-notes.txt",
        "docs/keys.md",
    ],
)
def test_allowed(path):
    assert pc.excluded_reason(path) is None, path


def test_leftover_paths():
    text = f"cd {WS}/client\nok line\nhome is {HOME}/x\n"
    found = pc.leftovers("docs/QA_GUIDE.md", text, [])
    assert [f.split(":")[1] for f in found] == ["1", "3"]
    # Prefixes of other words don't count.
    assert pc.leftovers("docs/x.md", f"/home/{'dev'}eloper and my{WS}s\n", []) == []


def test_leftover_email_anywhere():
    found = pc.leftovers("docs/notes.md", "mail Someone@Example.org please\n", ["someone@example.org"])
    assert len(found) == 1 and "email" in found[0]
    assert pc.leftovers("README.md", "nothing here\n", ["someone@example.org"]) == []


def _repo(tmp_path: Path) -> Path:
    repo = tmp_path / "repo"
    repo.mkdir()
    env = {**os.environ, "GIT_CONFIG_GLOBAL": os.devnull, "GIT_CONFIG_NOSYSTEM": "1"}

    def git(*args):
        subprocess.run(["git", "-C", str(repo), *args], check=True, capture_output=True, env=env)

    git("init", "-q")
    git("config", "user.email", "maintainer@example.org")
    (repo / "README.md").write_text("OpenArcanum\n")
    (repo / "internal").mkdir()
    (repo / "internal" / "x.go").write_text("package x\n")
    git("add", "-A")
    return repo


def _run(repo: Path) -> subprocess.CompletedProcess:
    env = {**os.environ, "GIT_CONFIG_GLOBAL": os.devnull, "GIT_CONFIG_NOSYSTEM": "1", "PUBLIC_CHECK_EMAILS": ""}
    return subprocess.run([sys.executable, str(SCRIPT), str(repo)], capture_output=True, text=True, env=env)


@pytest.mark.skipif(shutil.which("git") is None, reason="needs git")
def test_clean_repo_passes_and_summarises(tmp_path):
    repo = _repo(tmp_path)
    (repo / "notes.txt").write_text("untracked\n")
    p = _run(repo)
    assert p.returncode == 0, p.stderr
    assert "2 tracked files" in p.stdout
    assert "internal/" in p.stdout and "README.md" in p.stdout
    assert "neither tracked nor ignored" in p.stdout and "notes.txt" in p.stdout


@pytest.mark.skipif(shutil.which("git") is None, reason="needs git")
def test_private_file_and_email_fail(tmp_path):
    repo = _repo(tmp_path)
    (repo / "bin").mkdir()
    (repo / "bin" / "relay").write_text("binary\n")
    (repo / "docs").mkdir()
    (repo / "docs" / "a.md").write_text("Contact maintainer@example.org\n")
    subprocess.run(["git", "-C", str(repo), "add", "-A"], check=True, capture_output=True)
    p = _run(repo)
    assert p.returncode == 1
    assert "bin/relay: must not be published" in p.stderr
    assert "docs/a.md:1: the git user's email address" in p.stderr


@pytest.mark.skipif(not (ROOT / ".git").exists() or shutil.which("git") is None, reason="needs the git checkout")
def test_current_tree_passes():
    p = subprocess.run([sys.executable, str(SCRIPT), str(ROOT)], capture_output=True, text=True)
    assert p.returncode == 0, p.stderr
    assert "public-check: ok" in p.stdout
