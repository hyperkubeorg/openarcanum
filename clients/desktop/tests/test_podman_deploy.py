"""The rootless podman deployment kit (deploy/podman): Quadlet files that
parse and mount host directories (no named volumes) with SELinux labels,
config files whose keys exist in the Go config structs, environment
variables the servers know, ports that match between the configs, the
images' health checks and the firewall, scripts that pass bash -n (and
shellcheck when installed), and documents without secrets, placeholders,
old names or em dashes."""

import json
import os
import re
import shutil
import subprocess
import sys
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[3]
KIT = ROOT / "deploy" / "podman"
CONTAINERFILE = ROOT / "deploy" / "container" / "Containerfile"

# host directory -> (Quadlet stem, binary, image, Containerfile stage, config, Go package)
HOSTS = {
    "maintainer-01.openarcanum.org": (
        "openarcanum-maintainer",
        "meta",
        "openarcanum-maintainer",
        "meta",
        "meta.json",
        "meta",
    ),
    "relay-01.openarcanum.org": ("openarcanum-relay", "relay", "openarcanum-relay", "relay", "relay.json", "relay"),
    "openarcanum.org": ("openarcanum-site", "site", "openarcanum-site", "site", "site.json", "site"),
}
ENV_NODE = {"meta": "META", "relay": "RELAY", "site": "SITE"}


def parse_unit(path: Path) -> dict[str, list[tuple[str, str]]]:
    """A systemd/Quadlet unit file: sections of (key, value) pairs, keys may
    repeat (Volume=, Environment=). Raises on anything that isn't one."""
    sections: dict[str, list[tuple[str, str]]] = {}
    current = None
    for n, raw in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        line = raw.strip()
        if not line or line.startswith(("#", ";")):
            continue
        m = re.fullmatch(r"\[([A-Za-z]+)\]", line)
        if m:
            assert m.group(1) not in sections, f"{path}:{n}: section [{m.group(1)}] twice"
            current = sections.setdefault(m.group(1), [])
            continue
        m = re.fullmatch(r"([A-Za-z][A-Za-z0-9]*)=(.*)", line)
        assert m and current is not None, f"{path}:{n}: not an INI line: {raw!r}"
        assert not raw.endswith("\\"), f"{path}:{n}: no continuation lines"
        current.append((m.group(1), m.group(2)))
    return sections


def values(sec: list[tuple[str, str]], key: str) -> list[str]:
    return [v for k, v in sec if k == key]


def one(sec: list[tuple[str, str]], key: str) -> str:
    v = values(sec, key)
    assert len(v) == 1, f"{key}: {v}"
    return v[0]


def go_tags(kind: str, *files: str) -> set[str]:
    return {t for f in files for t in re.findall(rf'{kind}:"([A-Za-z0-9_]+)"', (ROOT / f).read_text(encoding="utf-8"))}


def config_files(pkg: str) -> tuple[str, ...]:
    return (f"internal/{pkg}/config.go", "internal/ops/settings.go")


def env_prefixes(binary: str) -> set[str]:
    """The environment prefix the binary reads."""
    sources = [p for p in (ROOT / "cmd" / binary).glob("*.go") if not p.name.endswith("_test.go")]
    src = "\n".join(p.read_text(encoding="utf-8") for p in sources)
    found = set(re.findall(r'(?:config\.Load\([^,]+, |EnvPrefix\s*=\s*)"([A-Z_]+_)"', src))
    assert found, f"no env prefix found in cmd/{binary}"
    return found


def setup_vars(host: str) -> dict[str, str]:
    text = (KIT / host / "setup.sh").read_text(encoding="utf-8")
    return dict(re.findall(r'^([A-Z_]+)="?([^"\n]*)"?$', text, re.M))


def image_stage(stage: str) -> str:
    text = CONTAINERFILE.read_text(encoding="utf-8")
    m = re.search(rf"^FROM \S+ AS {stage}\n(.*?)(?=^FROM |\Z)", text, re.M | re.S)
    assert m, f"no stage {stage}"
    return m.group(1)


def kit_files() -> list[Path]:
    return sorted(p for p in KIT.rglob("*") if p.is_file())


@pytest.mark.parametrize("host", sorted(HOSTS))
def test_quadlet_file(host):
    stem, _, image, _, cfg, _ = HOSTS[host]
    path = KIT / host / f"{stem}.container"
    unit = parse_unit(path)
    assert set(unit) == {"Unit", "Container", "Service", "Install"}
    c = unit["Container"]
    m = re.fullmatch(rf"ghcr\.io/hyperkubeorg/{image}:(\d{{4}}\.\d{{1,2}}\.\d+(?:-[a-z]+\.\d+)?)", one(c, "Image"))
    assert m, "the image must be pinned to a release version"
    assert one(c, "ContainerName") == stem
    assert one(c, "AutoUpdate") == "registry"
    assert one(c, "Network") == "host"
    assert one(c, "UserNS") == "keep-id:uid=65532,gid=65532"
    assert one(c, "LogDriver") == "journald"
    assert one(c, "HealthOnFailure") == "kill"
    assert one(c, "ReadOnly") == "true" and one(c, "NoNewPrivileges") == "true"
    assert one(c, "DropCapability") == "ALL"
    assert one(c, "Exec") == f"-config /etc/openarcanum/{cfg}"
    assert one(unit["Service"], "Restart") == "always"
    assert one(unit["Install"], "WantedBy") == "default.target"

    app = setup_vars(host)["APP_DIR"]
    targets = {}
    for v in values(c, "Volume"):
        src, dst, opts = v.split(":")
        # Bind mounts of host directories only: no named volumes.
        assert src.startswith(f"/home/openarcanum/{app}/"), v
        assert "Z" in opts.split(","), f"{v}: needs an SELinux label (:Z)"
        targets[dst] = (src, opts)
    assert targets["/etc/openarcanum"] == (f"/home/openarcanum/{app}/config", "ro,Z")
    mounted = sorted(src.rsplit("/", 1)[1] for src, _ in targets.values() if not src.endswith("/config"))
    assert mounted == sorted(setup_vars(host)["DATA_DIRS"].split())


@pytest.mark.parametrize("host", sorted(HOSTS))
def test_environment_variables_exist(host):
    stem, binary, _, stage, _, pkg = HOSTS[host]
    node = ENV_NODE[binary]
    tags = go_tags("env", *config_files(pkg))
    prefixes = env_prefixes(binary)
    assert prefixes == {f"OPENARCANUM_{node}_"}, prefixes
    container = parse_unit(KIT / host / f"{stem}.container")["Container"]
    env = dict(v.split("=", 1) for v in values(container, "Environment"))
    assert env, "the public listener must be set in the environment"
    for name in env:
        assert name.startswith(f"OPENARCANUM_{node}_"), f"{name}: the kit uses the OPENARCANUM_ names"
        assert name.removeprefix(f"OPENARCANUM_{node}_") in tags, f"{name} is not a {binary} setting"
    # The image sets LISTEN, and the environment wins over the config file.
    assert env[f"OPENARCANUM_{node}_LISTEN"] == ":443"
    assert re.search(rf"_{node}_LISTEN=", image_stage(stage)), "the image no longer sets LISTEN; recheck the kit"


@pytest.mark.parametrize("host", sorted(HOSTS))
def test_config_file(host):
    _, _, _, stage, cfg, pkg = HOSTS[host]
    conf = json.loads((KIT / host / cfg).read_text(encoding="utf-8"))
    known = go_tags("json", *config_files(pkg))
    unknown = set(conf) - known
    assert not unknown, f"{cfg}: keys the server refuses: {unknown}"
    # The environment sets the listener; a value here would be ignored.
    assert "listen" not in conf
    assert conf["tls_mode"] == "acme"
    # The site also answers for www (redirected to the first name).
    assert conf["tls_domains"] == [host] + (["www." + host] if host == "openarcanum.org" else [])
    # The image's own health check asks the status listener.
    check = re.search(r"HEALTHCHECK .*http://127\.0\.0\.1:(\d+)/healthz", image_stage(stage))
    assert check, f"no health check in stage {stage}"
    assert conf["metrics_listen"] == f"127.0.0.1:{check.group(1)}"
    for key in ("acme_cache_dir", "data_dir", "registry_key_file", "storage_key_file"):
        if key in conf:
            assert conf[key].startswith(("/data", "/keys", "/acme")), f"{key} must be on a mount"
    blob = json.dumps(conf).lower()
    assert "example" not in blob and "fragment" not in blob


def test_maintainer_and_relay_settings():
    meta = json.loads((KIT / "maintainer-01.openarcanum.org" / "meta.json").read_text(encoding="utf-8"))
    assert meta["directory_enabled"] is False
    assert meta["relay_proof"] is True
    assert meta["allow_private_relays"] is False
    relay = json.loads((KIT / "relay-01.openarcanum.org" / "relay.json").read_text(encoding="utf-8"))
    assert relay["public_url"] == "wss://relay-01.openarcanum.org/ws"
    assert relay["registry_urls"] == ["https://maintainer-01.openarcanum.org"]
    assert relay["calls"] is True
    assert relay["acme_cache_dir"] == "/acme"
    v = setup_vars("relay-01.openarcanum.org")
    assert v["UDP_PORTS"].split() == [f"{relay['media_udp_port']}/udp"]
    for host in HOSTS:
        assert "443/tcp" in setup_vars(host)["TCP_PORTS"].split()


@pytest.mark.parametrize("host", ["maintainer-01.openarcanum.org", "openarcanum.org"])
def test_backup_units(host):
    v = setup_vars(host)
    units = v["EXTRA_UNITS"].split()
    # The backup timer is enabled only when backups are on (backup_choice).
    assert "EXTRA_TIMERS" not in v and v["BACKUP_TIMERS"] in units
    stem = HOSTS[host][0]
    for name in units:
        unit = parse_unit(KIT / host / name)
        if name.endswith("-backup-sync.service"):
            cmd = one(unit["Service"], "ExecStart")
            assert cmd.startswith("/usr/bin/rsync ") and "/backups/" in cmd
            assert one(unit["Unit"], "ConditionPathExists").endswith("/backup-remote/env")
        elif name.endswith(".service"):
            cmd = one(unit["Service"], "ExecStart")
            assert cmd.startswith(f"/usr/bin/podman exec {stem} {HOSTS[host][1]} backup ")
            assert "/data/backups" in cmd
        else:
            assert one(unit["Install"], "WantedBy") == "timers.target"


@pytest.mark.skipif(sys.platform == "win32", reason="the deployment scripts run on Linux")
def test_scripts_parse():
    scripts = [KIT / "lib" / "common.sh", *(KIT / h / "setup.sh" for h in HOSTS)]
    for s in scripts:
        r = subprocess.run(["bash", "-n", str(s)], capture_output=True, text=True)
        assert r.returncode == 0, f"{s}: {r.stderr}"
    for h in HOSTS:
        s = KIT / h / "setup.sh"
        assert os.access(s, os.X_OK), f"{s} must be executable"
        text = s.read_text(encoding="utf-8")
        assert "set -Eeuo pipefail" in text and "setup_main" in text
        assert setup_vars(h)["SERVICE"] == HOSTS[h][0]


@pytest.mark.skipif(sys.platform == "win32", reason="the deployment scripts run on Linux")
def test_scripts_pass_shellcheck():
    sc = shutil.which("shellcheck")
    if not sc:
        pytest.skip("shellcheck not installed")
    scripts = [str(KIT / "lib" / "common.sh"), *(str(KIT / h / "setup.sh") for h in HOSTS)]
    r = subprocess.run([sc, "-x", "-s", "bash", *scripts], capture_output=True, text=True)
    assert r.returncode == 0, r.stdout + r.stderr


SECRET_PATTERNS = [
    r"gh[pousr]_[A-Za-z0-9]{20,}",
    r"github_pat_[A-Za-z0-9_]{20,}",
    r"-----BEGIN [A-Z ]*PRIVATE KEY-----",
    r"\b[0-9a-f]{64}\b",
    r"(?i)\b(password|passwd|token|secret)\s*[=:]\s*['\"]?[A-Za-z0-9+/_-]{8,}",
]
PLACEHOLDERS = [r"example\.(?:invalid|com|org)", r"\bTODO\b", r"\bFIXME\b", r"(?i)changeme", r"\bXXX\b", r"<[a-z-]+>"]


def test_no_secrets_placeholders_or_old_names():
    for p in kit_files():
        text = p.read_text(encoding="utf-8")
        # Declared checksums (COSIGN_SHA256_...=) are the one hex blob allowed.
        checked = "\n".join(line for line in text.splitlines() if not re.match(r"\w*SHA256\w*=[0-9a-f]{64}$", line))
        for pat in SECRET_PATTERNS:
            assert not re.search(pat, checked), f"{p}: looks like a secret ({pat})"
        assert "fragment" not in text.lower(), f"{p}: uses the old name"
        if p.suffix != ".md":
            for pat in PLACEHOLDERS:
                assert not re.search(pat, text), f"{p}: placeholder ({pat})"


def test_readmes():
    readmes = [KIT / "README.md", *(KIT / h / "README.md" for h in HOSTS)]
    for r in readmes:
        text = r.read_text(encoding="utf-8")
        assert "—" not in text and "–" not in text, f"{r}: em or en dash"
    for h in HOSTS:
        text = (KIT / h / "README.md").read_text(encoding="utf-8")
        for heading in (
            "## Where everything lives",
            "## How it was set up",
            "## Start, stop, status, logs",
            "## Updating to a new release, and rolling back",
            "## Backups and restore",
            "## Certificates",
            "## Firewall, ports, SELinux",
            "## Troubleshooting",
        ):
            assert heading in text, f"{h}/README.md lacks {heading!r}"
        # No metric name in the docs keeps the fragment_ prefix.
        assert not re.search(r"\bfragment_", text)


@pytest.mark.skipif(sys.platform == "win32", reason="the deployment scripts run on Linux")
@pytest.mark.parametrize(
    "rules", ["", 'rule family="ipv4" source address="203.0.113.7" port port="7100" protocol="tcp" accept']
)
def test_monitoring_step_runs_under_the_scripts_error_handling(tmp_path, rules):
    """ensure_monitoring (lib/common.sh) under set -Eeuo pipefail, as setup.sh
    runs it, against a stand-in firewall-cmd: with monitoring off and no rule
    for the port (grep finding nothing must not stop setup), and with an old
    rule to take away. It ends, and removes what it finds."""
    bin_dir = tmp_path / "bin"
    bin_dir.mkdir()
    calls = tmp_path / "calls"
    fw = bin_dir / "firewall-cmd"
    fw.write_text(
        f'#!/bin/sh\necho "$*" >> {calls}\ncase "$*" in *--list-rich-rules*) printf %s {json.dumps(rules)} ;; esac\n'
    )
    fw.chmod(0o755)
    script = f"""
set -Eeuo pipefail
say() {{ :; }}; info() {{ :; }}; die() {{ echo "$*"; exit 1; }}
source <(sed -n '/^ensure_monitoring() {{/,/^}}/p' {KIT / "lib" / "common.sh"})
OA_HOME={tmp_path} APP_DIR=maintainer METRICS_PORT=7100
ensure_monitoring ""
echo finished
"""
    r = subprocess.run(
        ["bash", "-c", script],
        capture_output=True,
        text=True,
        env={**os.environ, "PATH": f"{bin_dir}:{os.environ['PATH']}"},
    )
    assert r.returncode == 0 and "finished" in r.stdout, (r.returncode, r.stdout, r.stderr)
    log = calls.read_text()
    assert ("--remove-rich-rule" in log) == bool(rules), log
