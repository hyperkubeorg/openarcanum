"""Server-only behaviour checked through the built binaries (bin/community,
bin/relay). The Go suites test the same code in-process; these start the
real programs with their real configuration paths.

- At rest, a community server's data directory holds none of the
  community's names, members or messages (Go: TestAtRestNoPlaintextInDatabase).
- A relay with a fan-out threshold copies a large channel's messages only
  for the community server's token, not for a member publishing to the
  channel topic directly (Go: internal/relay abuse tests).
- The maintainer admin commands (`meta relays`, `meta directory`) and
  `meta backup`/`meta restore` (Go: cmd/meta, in-process).
- `community backup`/`community restore` (Go: internal/community backup
  tests, in-process).

Proof-of-work through the relay binary is test_versions_abuse.py
test_pow_solved_off_the_event_loop; owner-chain refusal through the
community binary is test_e2e_owners.py."""

import asyncio
import subprocess
import time
from pathlib import Path

from conftest import (
    META_DIRS,
    Proc,
    binary,
    free_port,
    new_core,
    run,
    run_community,
    run_meta,
    run_relay,
    wait_port,
    wait_until,
)
from test_e2e_community import WIZARD, general

from openarcanum.meta import MetaClient
from openarcanum.relay import Relay

MARK = "zebracrossing"


def test_community_data_dir_holds_no_plaintext(tmp_path):
    with run_relay() as relay, run_community(tmp_path, [relay]) as node:
        wizard = dict(WIZARD, name=f"{MARK} Town", description=f"{MARK} description", tags=f"{MARK}tag")

        async def main():
            owner, _ = await new_core(tmp_path / "owner")
            res = await owner.claim(node.claim_code, wizard, f"{MARK}Olive")
            cid = res["community"]
            gen = await general(owner, cid)
            alice, aev = await new_core(tmp_path / "alice")
            await alice.join({"invite": res["invite"]}, "new", f"{MARK}Alice")
            await general(alice, cid)
            chan = (await owner.admin(cid, "create_channel", {"name": f"{MARK}-room"}))["id"]
            await owner.admin(cid, "edit_channel", {"id": chan, "topic": f"{MARK} topic"})
            await owner.admin(cid, "create_role", {"name": f"{MARK}Role"})
            m = await owner.send(cid, gen, f"the {MARK} message")
            await aev.wait(lambda n, p: n == "message" and p["message"]["id"] == m["id"], 20)
            await alice.send(cid, gen, f"a {MARK} reply")
            # Everything above reached the server (Alice's copy came back
            # from it).
            await wait_until(lambda: any(MARK in c["name"] for c in owner.community_state(cid)["channels"]))
            await owner.shutdown()
            await alice.shutdown()

        run(main(), 120)
        # Stopped: the database and its WAL are closed and checkpointed.
        node.stop()
        files = [p for p in Path(node.data).rglob("*") if p.is_file()]
        assert any(p.name.startswith("community.db") for p in files)
        leaks = [str(p.relative_to(node.data)) for p in files if MARK.encode() in p.read_bytes()]
        assert not leaks, f"plaintext in {leaks}"


def test_fanout_token_required_through_relay_binary(tmp_path):
    # Two subscribers are already "large" with a threshold of 1.
    with run_relay(fanout_threshold=1) as relay, run_community(tmp_path, [relay]) as node:

        async def main():
            owner, _ = await new_core(tmp_path / "owner")
            res = await owner.claim(node.claim_code, WIZARD, "Olive")
            cid = res["community"]
            gen = await general(owner, cid)
            alice, aev = await new_core(tmp_path / "alice")
            await alice.join({"invite": res["invite"]}, "new", "Alice")
            await general(alice, cid)
            conn = alice.conns[cid]
            await wait_until(lambda: gen in conn.topics.values())
            topic = next(t for t, c in conn.topics.items() if c == gen)

            # A third subscriber that records what the relay copies to it.
            watcher = await Relay(relay).connect(timeout=30)
            seen: asyncio.Queue = asyncio.Queue()
            await watcher.subscribe(topic, lambda _t, d: seen.put_nowait(d))
            # A member publishing straight to the channel topic: refused
            # fan-out, nobody gets it.
            member = await Relay(relay).connect(timeout=30)
            await member.publish(topic, b"amplified by a member")
            await asyncio.sleep(1.5)
            assert seen.empty(), "the relay fanned out a member's publish without a token"

            # The server's own messages (it holds tokens) still reach everyone.
            m = await owner.send(cid, gen, "from the server, with a token")
            await aev.wait(lambda n, p: n == "message" and p["message"]["id"] == m["id"], 20)
            got = await asyncio.wait_for(seen.get(), 10)
            assert got and b"amplified by a member" not in got
            await member.close()
            await watcher.close()
            await owner.shutdown()
            await alice.shutdown()

        run(main(), 120)


def _cli(*args: str) -> str:
    r = subprocess.run([str(a) for a in args], capture_output=True, text=True, timeout=60)
    assert r.returncode == 0, r.stderr + r.stdout
    return r.stdout


def test_meta_admin_and_backup_commands_through_binary(tmp_path):
    """`meta relays remove` on a running node's data directory drops the relay
    from what the node serves; `meta backup` (while it runs) and `meta
    restore` carry the decision to a new data directory."""
    meta_bin = binary("meta")
    with run_meta() as url, run_relay(registry_urls=url, public_url="{url}", heartbeat_interval="1s") as relay:
        data = META_DIRS[url]
        client = MetaClient([url])

        async def registered() -> bool:
            return any(r["url"] == relay for r in await client.relays())

        async def main():
            await wait_until(registered)
            assert relay in _cli(meta_bin, "relays", "list", "-data", data)
            out = _cli(meta_bin, "relays", "remove", "-data", data, "-url", relay, "-reason", "e2e check")
            assert "removed from the registry" in out
            # The relay keeps sending heartbeats; the node refuses them.
            await asyncio.sleep(2.5)
            assert not await registered()

        run(main(), 60)
        out = _cli(meta_bin, "backup", "-data", data, "-out", tmp_path / "backups")
        backup = next(p for p in (tmp_path / "backups").iterdir() if p.is_file())
        assert backup.name in out or str(backup) in out
    restored = tmp_path / "restored"
    _cli(meta_bin, "restore", "-data", restored, "-backup", backup)
    decisions = _cli(meta_bin, "directory", "decisions", "-data", restored)
    assert relay in decisions and "e2e check" in decisions
    with run_meta(data_dir=restored) as url2:
        assert run(MetaClient([url2]).relays(), 30) == []


def test_community_backup_and_restore_through_binary(tmp_path):
    """`community backup` on a stopped server's data directory and `community
    restore` on new hardware: the restored server starts, is ready, and
    prints the move code owners use to adopt it (test_e2e_owners.py runs the
    adoption)."""
    community_bin = binary("community")
    with run_relay() as relay:
        with run_community(tmp_path, [relay]) as node:

            async def main():
                owner, _ = await new_core(tmp_path / "owner")
                res = await owner.claim(node.claim_code, WIZARD, "Olive")
                gen = await general(owner, res["community"])
                await owner.send(res["community"], gen, "kept in the backup")
                await owner.shutdown()

            run(main(), 60)
            node.stop()
            path = _cli(community_bin, "backup", "-data", node.data).strip().splitlines()[-1]
            assert path.endswith(".fbk") and Path(path).is_file()
        new_dir = tmp_path / "moved"
        out = _cli(
            community_bin, "restore", "-data", new_dir, "-backup", path,
            "-storage-key", node.data / "storage.key", "-relay", relay,
        )  # fmt: skip
        assert any(ln.startswith("openarcanum-move:") for ln in out.splitlines())
        port = free_port()
        proc = Proc(
            [str(community_bin), "run", "-data", str(new_dir)],
            env={"OPENARCANUM_COMMUNITY_METRICS_LISTEN": f"127.0.0.1:{port}"},
        )
        try:
            wait_port(port)
            ready = f"http://127.0.0.1:{port}/readyz"
            deadline = time.monotonic() + 15
            while subprocess.run([str(community_bin), "healthcheck", ready], capture_output=True).returncode != 0:
                assert time.monotonic() < deadline, "restored server never became ready"
                time.sleep(0.1)
        finally:
            proc.stop()
