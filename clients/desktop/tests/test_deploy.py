"""Self-tests for the server deployment checks:
scripts/check-deploy (systemd units, Containerfile) and
scripts/check-release-bins (static ELF binaries)."""

from __future__ import annotations

import importlib.machinery
import importlib.util
import shutil
import subprocess
import sys
from pathlib import Path

import pytest
from conftest import BIN, binary

ROOT = Path(__file__).resolve().parents[3]


def _load(name: str):
    loader = importlib.machinery.SourceFileLoader(name.replace("-", "_"), str(ROOT / "scripts" / name))
    spec = importlib.util.spec_from_loader(loader.name, loader)
    mod = importlib.util.module_from_spec(spec)
    sys.modules[loader.name] = mod
    loader.exec_module(mod)
    return mod


bins = _load("check-release-bins")


def run_check(root: Path) -> subprocess.CompletedProcess:
    binary("relay")  # skips without `make build`
    return subprocess.run(
        [sys.executable, str(ROOT / "scripts" / "check-deploy"), "--root", str(root), "--bin", str(BIN)],
        capture_output=True,
        text=True,
    )


@pytest.fixture
def deploy_copy(tmp_path):
    shutil.copytree(ROOT / "deploy", tmp_path / "deploy")
    return tmp_path


def test_current_deploy_files_pass(deploy_copy):
    r = run_check(deploy_copy)
    assert r.returncode == 0, r.stderr
    assert "Containerfile: 4 images checked" in r.stdout
    if shutil.which("systemd-analyze"):
        assert "systemd-analyze verify: 6 units OK" in r.stdout
        assert r.stdout.count("exposure") == 5


def edit(path: Path, old: str, new: str) -> None:
    text = path.read_text()
    assert old in text, old
    path.write_text(text.replace(old, new, 1))


RELAY, META, COMMUNITY = (
    "systemd/openarcanum-relay.service",
    "systemd/openarcanum-meta.service",
    "systemd/openarcanum-community.service",
)
BACKUP, CF = "systemd/openarcanum-meta-backup.service", "container/Containerfile"
META_USER = "COPY --from=build --chown=65532:65532 /out/data /data\nUSER 65532:65532\nENV OPENARCANUM_META_LISTEN"
HC = 'CMD ["/usr/local/bin/{}", "healthcheck"'

WEAKENED = [
    (RELAY, "NoNewPrivileges=yes", "NoNewPrivileges=no", "NoNewPrivileges='yes' required"),
    (META, "DynamicUser=yes", "User=root", "DynamicUser='yes' required"),
    (COMMUNITY, "ProtectSystem=strict", "ProtectSystem=full", "ProtectSystem='strict'"),
    (BACKUP, "StateDirectory=openarcanum-meta", "StateDirectory=other", "StateDirectory=openarcanum-meta"),
    (RELAY, "SystemCallFilter=@system-service", "", "SystemCallFilter=@system-service"),
    (CF, "nonroot@sha256:", "nonroot#", "RUNTIME_IMAGE must be pinned by digest"),
    (CF, META_USER, META_USER.replace("USER 65532:65532", "USER 0:0"), "meta: USER must be a non-root"),
    (CF, HC.format("relay"), HC.format("meta"), "relay: HEALTHCHECK must run"),
    (CF, " && CGO_ENABLED=0 GOOS", " && GOOS", "CGO_ENABLED=0 is required"),
    (CF, ".Release=$VERSION", ".Release=dev", "link ARG VERSION into internal/version.Release"),
    (CF, 'licenses="AGPL-3.0-only"', 'licenses="MIT"', "org.opencontainers.image.licenses"),
    (CF, 'VOLUME ["/data"]\nEXPOSE 7100/tcp', "EXPOSE 7100/tcp", 'meta: VOLUME ["/data"] required'),
    (CF, 'VOLUME ["/data", "/keys"]', 'VOLUME ["/data"]', 'community: VOLUME ["/data", "/keys"] required'),
    (CF, "STORAGE_KEY_FILE=/keys/storage.key", "STORAGE_KEY_FILE=/data/storage.key", "must point into /keys"),
]


@pytest.mark.parametrize(("file", "old", "new", "error"), WEAKENED)
def test_weakened_deploy_files_fail(deploy_copy, file, old, new, error):
    edit(deploy_copy / "deploy" / file, old, new)
    r = run_check(deploy_copy)
    assert r.returncode == 1
    assert error in r.stderr, r.stderr


@pytest.mark.skipif(not shutil.which("systemd-analyze"), reason="needs systemd-analyze")
def test_invalid_unit_fails_verify(deploy_copy):
    edit(
        deploy_copy / "deploy/systemd/openarcanum-meta-backup.timer",
        "OnCalendar=daily",
        "OnCalendar=daily\nBogusSetting=1",
    )
    r = run_check(deploy_copy)
    assert r.returncode == 1 and "systemd-analyze verify failed" in r.stderr, r.stderr


@pytest.mark.skipif(not shutil.which("systemd-analyze"), reason="needs systemd-analyze")
def test_exposure_over_threshold_fails(deploy_copy):
    unit = deploy_copy / "deploy/systemd/openarcanum-relay.service"
    keep = ("[", "Type=", "ExecStart=", "Environment=", "Description=", "After=", "Wants=", "WantedBy=",
            "DynamicUser=", "StateDirectory", "UMask=", "NoNewPrivileges=", "CapabilityBoundingSet=", "ProtectSystem=",
            "ProtectHome=", "PrivateTmp=", "PrivateDevices=", "ProtectKernelTunables=", "ProtectKernelModules=",
            "ProtectControlGroups=", "RestrictSUIDSGID=", "MemoryDenyWriteExecute=", "LockPersonality=",
            "SystemCallArchitectures=", "SystemCallFilter=@system-service")  # fmt: skip
    unit.write_text("\n".join(ln for ln in unit.read_text().splitlines() if ln.startswith(keep)) + "\n")
    r = run_check(deploy_copy)
    assert r.returncode == 1 and "openarcanum-relay.service: systemd-analyze security over" in r.stderr, r.stderr


def test_elf_checks(tmp_path):
    exe = binary("relay")
    machine, _ = bins.elf_info(exe)
    assert machine in (62, 183)
    # A dynamically linked executable (the Python interpreter) is reported.
    real = Path(sys.executable).resolve()
    with real.open("rb") as f:
        is_elf = f.read(4) == b"\x7fELF"
    if is_elf:
        try:
            _, py_static = bins.elf_info(real)
        except ValueError:
            py_static = None  # a position-independent executable (ET_DYN)
        assert py_static in (False, None)
    text = tmp_path / "relay"
    text.write_text("#!/bin/sh\n")
    with pytest.raises(ValueError, match="not an ELF"):
        bins.elf_info(text)
