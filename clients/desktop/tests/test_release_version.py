"""scripts/version (docs/release.md "Versions"): the CalVer scheme, its
copies, bumps, and the container image tags the release workflows push."""

from __future__ import annotations

import datetime
import importlib.machinery
import importlib.util
import shutil
import subprocess
import sys
from pathlib import Path

import pytest

from openarcanum import __version__, updates

ROOT = Path(__file__).resolve().parents[3]
SCRIPT = ROOT / "scripts" / "version"


def _load():
    loader = importlib.machinery.SourceFileLoader("version_script", str(SCRIPT))
    spec = importlib.util.spec_from_loader("version_script", loader)
    mod = importlib.util.module_from_spec(spec)
    sys.modules["version_script"] = mod
    loader.exec_module(mod)
    return mod


V = _load()


@pytest.mark.parametrize("good", ["2026.10.0", "2026.10.12", "2027.1.0", "2027.1.0-beta.1", "2026.12.3-beta.10"])
def test_scheme_accepts(good):
    v = V.Version(good)
    assert str(v) == good
    # Every version is a Semantic Version the updater understands.
    updates.version_key(good)


@pytest.mark.parametrize(
    "bad",
    ["0.1.0", "2026.10", "2027.01.0", "2026.13.0", "2026.0.1", "2026.10.01", "v2026.10.0", "2026.10.0-rc.1",
     "2026.10.0-beta.0", "2026.10.0-beta", "2026.10.0+build"],
)  # fmt: skip
def test_scheme_refuses(bad):
    with pytest.raises(V.VersionError):
        V.Version(bad)


def test_derived_forms():
    v = V.Version("2026.11.2-beta.3")
    assert (v.numeric, v.series, v.pep440, v.tag, v.prerelease) == (
        "2026.11.2",
        "2026.11",
        "2026.11.2b3",
        "v2026.11.2-beta.3",
        True,
    )
    f = V.Version("2026.10.0")
    assert (f.pep440, f.tag, f.prerelease) == ("2026.10.0", "v2026.10.0", False)


def test_order_matches_the_updater():
    order = ["2026.10.0-beta.1", "2026.10.0-beta.2", "2026.10.0-beta.10", "2026.10.0", "2026.10.1", "2026.11.0",
             "2027.1.0-beta.1", "2027.1.0"]  # fmt: skip
    assert sorted(order, key=lambda s: V.Version(s).key()) == order
    assert sorted(order, key=updates.version_key) == order


def test_bumps():
    oct_, nov = datetime.date(2026, 10, 26), datetime.date(2026, 11, 3)
    b = lambda cur, kind, beta=False, today=oct_: str(V.bumped(V.Version(cur), kind, beta, today))  # noqa: E731
    assert b("2026.10.0", "patch") == "2026.10.1"
    assert b("2026.10.0", "patch", beta=True) == "2026.10.1-beta.1"
    assert b("2026.10.1", "month", today=nov) == "2026.11.0"
    assert b("2026.10.1", "month", beta=True, today=nov) == "2026.11.0-beta.1"
    assert b("2026.12.4", "month", today=datetime.date(2027, 1, 2)) == "2027.1.0"
    assert b("2026.11.0-beta.1", "beta") == "2026.11.0-beta.2"
    assert b("2026.11.0-beta.2", "final") == "2026.11.0"
    assert b("2026.10.0", "next") == "2026.10.1"
    assert b("2026.10.0", "next", today=nov) == "2026.11.0"
    for cur, kind, today in (("2026.10.0", "month", oct_), ("2026.10.0", "beta", oct_), ("2026.10.0", "final", oct_)):
        with pytest.raises(V.VersionError):
            V.bumped(V.Version(cur), kind, False, today)


def test_image_tags():
    imgs = ["ghcr.io/hyperkubeorg/openarcanum-relay"]
    sha = "0123456789abcdef"
    final = V.image_tags(imgs, sha, V.Version("2026.10.0"))
    assert [t.split(":")[1] for t in final] == ["2026.10.0", "2026.10", "latest", "sha-0123456"]
    beta = V.image_tags(imgs, sha, V.Version("2026.11.0-beta.1"))
    assert [t.split(":")[1] for t in beta] == ["2026.11.0-beta.1", "sha-0123456"]
    assert V.image_tags(imgs, sha, None) == ["ghcr.io/hyperkubeorg/openarcanum-relay:sha-0123456"]


def test_the_repository_agrees_with_VERSION():
    v = V.check()
    assert str(v) == __version__
    V.check(tag=v.tag)
    V.check(tag=f"refs/tags/{v.tag}")
    with pytest.raises(V.VersionError, match="does not match"):
        V.check(tag="v2000.1.0")


def test_set_writes_every_copy(tmp_path):
    for p in V.COPIES:
        dest = tmp_path / p.relative_to(ROOT)
        dest.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(p, dest)
    new = V.Version("2031.2.5-beta.4")
    V.write(new, tmp_path)
    assert str(V.check(tmp_path)) == "2031.2.5-beta.4"
    assert (tmp_path / "VERSION").read_text() == "2031.2.5-beta.4\n"
    lock = (tmp_path / V.LOCK.relative_to(ROOT)).read_text()
    assert 'name = "openarcanum"\nversion = "2031.2.5b4"' in lock
    # Only the openarcanum package's version changed in the lock.
    before = V.LOCK.read_text().splitlines()
    assert sum(a != b for a, b in zip(before, lock.splitlines(), strict=True)) == 1
    (tmp_path / "VERSION").write_text("2031.2.6\n")
    with pytest.raises(V.VersionError, match="VERSION says"):
        V.check(tmp_path)


def test_cli_info(tmp_path):
    out = tmp_path / "out"
    r = subprocess.run(
        [sys.executable, str(SCRIPT), "info", "--github-output", str(out)], capture_output=True, text=True
    )
    assert r.returncode == 0, r.stderr
    info = dict(line.split("=", 1) for line in out.read_text().splitlines())
    assert info["version"] == __version__ and info["tag"] == f"v{__version__}"
    assert info["prerelease"] in ("true", "false")
    r = subprocess.run([sys.executable, str(SCRIPT), "check", "--tag", "v1999.1.0"], capture_output=True, text=True)
    assert r.returncode == 1 and "does not match" in r.stderr
