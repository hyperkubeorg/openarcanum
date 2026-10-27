"""Operations end to end (docs/operations.md): the real relay, maintainer
node and community server
binaries with JSON logs to rotated files and their status listeners on,
driven by real clients through a community chat and a DM; then the logs and
metrics are checked for structure and for content. Also the guided
`community init` through the binary with scripted answers."""

from __future__ import annotations

import base64
import json
import os
import subprocess
import time
import urllib.request

import conftest
from conftest import PASS, CommunityNode, binary, free_port, run, run_meta, run_relay, wait_port
from test_e2e_dm import community_of, key_of, poll, until_text


def get(url: str) -> tuple[int, str, str]:
    try:
        with urllib.request.urlopen(url, timeout=10) as r:
            return r.status, r.headers.get("Content-Type", ""), r.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.headers.get("Content-Type", ""), e.read().decode()


def samples(text: str) -> dict[str, float]:
    """The samples of a text exposition, after checking each line is a
    HELP, a TYPE or a sample."""
    out = {}
    for line in text.splitlines():
        if line.startswith(("# HELP ", "# TYPE ")):
            continue
        name, _, value = line.rpartition(" ")
        assert name and value, line
        out[name] = float(value)
    return out


def encodings(s: bytes) -> list[str]:
    forms = [s.hex(), base64.b64encode(s).decode(), base64.urlsafe_b64encode(s).decode().rstrip("=")]
    try:
        forms.append(s.decode())
    except UnicodeDecodeError:
        pass
    return [f for f in forms if len(f) >= 5]


def test_logs_and_metrics_carry_no_content(tmp_path):
    logs = tmp_path / "logs"
    ops = {"log_format": "json", "log_level": "debug"}
    ports = {n: free_port() for n in ("relay", "meta", "community")}
    meta_env = dict(ops, log_file=logs / "meta.log", metrics_listen=f"127.0.0.1:{ports['meta']}")
    relay_env = dict(ops, log_file=logs / "relay.log", metrics_listen=f"127.0.0.1:{ports['relay']}")
    with (
        run_meta(**meta_env, writes_per_minute="60000") as meta,
        run_relay(**relay_env, registry_urls=meta, public_url="{url}", heartbeat_interval="1s") as relay,
    ):
        node = CommunityNode(tmp_path / "community", [relay], [meta])
        node.start(**ops, log_file=logs / "community.log", metrics_listen=f"127.0.0.1:{ports['community']}")
        try:
            wait_port(ports["community"])

            async def main():
                cid, [(owner, oev), (alice, _)] = await community_of(tmp_path, meta, node, "Alice")
                gen = next(c["id"] for c in owner.community_state(cid)["channels"] if c["name"] == "general")
                await owner.send(cid, gen, "the ocelot meets at the old mill")
                await oev.wait(
                    lambda n, p: n == "message" and p["message"]["text"] == "the ocelot meets at the old mill"
                )
                gid = (await alice.dms.open_with_member(cid, await key_of(alice, cid, "Olive")))["id"]
                await poll(owner)
                await owner.dms.accept(gid)
                await poll(alice)
                await alice.dms.send(gid, "a private note about the heron")
                await until_text(owner, gid, "a private note about the heron", alice)
                invite = await owner.admin(cid, "create_invite", {"label": "Lynx invite label"})
                keys = [await key_of(owner, cid, n) for n in ("Olive", "Alice")]
                for c in (owner, alice):
                    await c.shutdown()
                return invite["invite"], keys

            invite, member_keys = run(main(), 120)

            # Health and readiness on every node's status listener.
            for n, port in ports.items():
                assert get(f"http://127.0.0.1:{port}/healthz")[:1] == (200,), n
                assert get(f"http://127.0.0.1:{port}/readyz")[0] == 200, n
            # The binaries' own healthcheck command (container health checks).
            r = subprocess.run(
                [str(binary("relay")), "healthcheck", f"http://127.0.0.1:{ports['relay']}/healthz"], capture_output=True
            )
            assert r.returncode == 0
            metrics = {}
            for n, port in ports.items():
                code, ctype, body = get(f"http://127.0.0.1:{port}/metrics")
                assert code == 200 and ctype.startswith("text/plain; version=0.0.4"), (n, code, ctype)
                metrics[n] = body
            s = {n: samples(t) for n, t in metrics.items()}
            assert s["community"]["openarcanum_community_messages_total"] >= 1
            assert s["community"]["openarcanum_community_relays_attached"] == 1
            assert s["community"]["openarcanum_community_members"] == 2
            assert s["relay"]["openarcanum_relay_connections_accepted_total"] >= 3
            assert s["relay"]["openarcanum_relay_deliveries_total"] >= 1
            assert s["meta"]['openarcanum_meta_http_requests_total{route="POST /v1/mailbox/drop",code="200"}'] >= 1
            assert s["meta"]["openarcanum_meta_relays"] == 1
        finally:
            node.stop()

    # Structured: every line of every log is one JSON record.
    records = {}
    for n in ("relay", "meta", "community"):
        lines = (logs / f"{n}.log").read_text(encoding="utf-8").splitlines()
        assert lines, n
        records[n] = [json.loads(ln) for ln in lines]
        assert all({"time", "level", "msg"} <= r.keys() for r in records[n]), n
        if os.name == "posix":  # file modes are a POSIX thing
            assert (logs / f"{n}.log").stat().st_mode & 0o777 == 0o600
    assert any(r["msg"] == "admin action" for r in records["community"])
    assert any(r["msg"] == "relay listening" for r in records["relay"])

    secrets = [
        b"the ocelot meets at the old mill",
        b"a private note about the heron",
        b"Lynx invite label",
        b"Test Town",
        b"a test community",
        b"Alice",
        b"Olive",
        PASS.encode(),
        invite.encode(),
        invite.removeprefix("openarcanum-invite:").encode(),
        node.claim_code.encode(),
        node.claim_code.removeprefix("openarcanum-claim:").encode(),
    ]
    for k in member_keys:
        secrets.append(k.encode())
        try:
            secrets.append(base64.urlsafe_b64decode(k + "=" * (-len(k) % 4)))
        except ValueError:
            pass
    everything = "\n".join((logs / f"{n}.log").read_text(encoding="utf-8") for n in records)
    everything += "\n".join(metrics.values())
    for secret in secrets:
        for form in encodings(secret):
            assert form not in everything, f"logs or metrics contain {form!r}"


def test_guided_init_through_the_binary(tmp_path):
    """`community init` asks, a person (here: stdin) answers; the server it
    creates runs and reports ready on its status listener."""
    with run_relay() as relay:
        data = tmp_path / "srv"
        answers = "\n".join(
            [
                "home",
                str(data),
                "pinned",
                "http://not-a-relay.example.invalid",
                relay,
                "",
                "",  # no maintainers
                "",  # storage key: default
                "n",  # status listener: set below instead
                "y",
            ]
        )
        r = subprocess.run(
            [str(binary("community")), "init", "-interactive"], input=answers + "\n", capture_output=True, text=True
        )
        assert r.returncode == 0, r.stderr + r.stdout
        assert "do NOT put this machine in your router's DMZ" in r.stdout
        assert "must be a ws:// or wss:// URL" in r.stdout
        claim = next(ln for ln in r.stdout.splitlines() if ln.startswith("openarcanum-claim:"))
        cfg = json.loads((data / "config.json").read_text())
        assert cfg["relays"] == [relay] and cfg["storage_key_file"] == str(data / "storage.key")
        assert claim
        port = free_port()
        proc = conftest.Proc(
            [str(binary("community")), "run", "-data", str(data)],
            env={"OPENARCANUM_COMMUNITY_METRICS_LISTEN": f"127.0.0.1:{port}"},
        )
        try:
            wait_port(port)

            def ready():
                return subprocess.run(
                    [str(binary("community")), "healthcheck", f"http://127.0.0.1:{port}/readyz"], capture_output=True
                ).returncode

            for _ in range(100):
                if ready() == 0:
                    break
                time.sleep(0.1)
            assert ready() == 0
        finally:
            proc.stop()
    # Input that ends early creates nothing.
    other = tmp_path / "other"
    r = subprocess.run(
        [str(binary("community")), "init", "-interactive"], input=f"vps\n{other}\n", capture_output=True, text=True
    )
    assert r.returncode == 1 and "nothing was created" in r.stderr
    assert not (other / "server.key").exists()


def test_log_file_rotation_at_start(tmp_path):
    """A node started on a log file older than log_max_age rotates it aside
    and keeps only log_keep rotated files; the new file holds only the new
    run's records, mode 0600. (Rotation by size and by age while running is
    tested in internal/ops.)"""
    log = tmp_path / "relay.log"
    log.write_text('{"msg":"from the last run"}\n')
    old = time.time() - 2 * 86400
    os.utime(log, (old, old))
    for stamp in ("20250101T000000.000Z", "20250102T000000.000Z", "20250103T000000.000Z"):
        (tmp_path / f"relay.log.{stamp}").write_text("{}\n")
    with run_relay(log_file=log, log_format="json", log_max_age="24h", log_keep="2"):
        pass
    rotated = sorted(p.name for p in tmp_path.glob("relay.log.*"))
    assert len(rotated) == 2 and rotated[0] == "relay.log.20250103T000000.000Z", rotated
    assert (tmp_path / rotated[1]).read_text() == '{"msg":"from the last run"}\n'
    records = [json.loads(ln) for ln in log.read_text(encoding="utf-8").splitlines()]
    assert any(r["msg"] == "relay listening" for r in records)
    assert all(r.get("msg") != "from the last run" for r in records)
    if os.name == "posix":
        assert log.stat().st_mode & 0o777 == 0o600
