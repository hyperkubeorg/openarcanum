"""Mailboxes with a maintainer node down (docs/protocol.md §7.1). Items go to
every node that's up, so a node that's down when a message is sent doesn't
lose it. A message whose room key only reached a node that's now down can't
be read yet: it stays on its node (also across a restart) until the key
arrives, then it's shown once and deleted from every node."""

import asyncio
import contextlib
import os
import subprocess
import time

import pytest
from conftest import (
    META_PROCS,
    PASS,
    Events,
    Proc,
    binary,
    free_port,
    network,
    run,
    run_meta,
    start_meta_again,
    stop_meta,
    wait_port,
    wait_until,
)
from test_e2e_dm import community_of, key_of, poll, texts, until_text

from openarcanum import keys
from openarcanum.core import Core
from openarcanum.dm import unb64
from openarcanum.meta import MetaClient, inbox_address
from openarcanum.paths import Paths
from openarcanum.wire import meta_pb2 as mp

pytestmark = pytest.mark.timeout(300)


def inbox_tokens(core):
    d = core.dms.d
    return [unb64(e["inbox_token"]) for e in d["ids"].values()] + [
        unb64(g["inbox_token"]) for g in d["groups"].values()
    ]


async def items_on(base, tokens):
    client = MetaClient([base])
    return sum([len(await client.fetch_from(base, t)) for t in tokens])


def test_dm_survives_a_node_down_at_send_and_at_fetch(tmp_path):
    with contextlib.ExitStack() as stack:
        m2 = stack.enter_context(run_meta(writes_per_minute="60000"))
        meta, _relay, community = stack.enter_context(network(tmp_path))
        node = community()

        async def main():
            cid, people = await community_of(tmp_path, meta, node, "Alice")
            (olive, _), (alice, _) = people
            for c in (olive, alice):
                await c.set_setting("maintainers", [meta, m2])
            await olive.set_setting("dm_policy", "members")
            gid = (await alice.dms.open_with_member(cid, await key_of(alice, cid, "Olive")))["id"]
            await alice.dms.send(gid, "first")
            await until_text(olive, gid, "first", alice)
            tokens = inbox_tokens(olive)
            # Olive goes offline: what follows reaches her only by mailbox.
            await olive.shutdown()

            # The first node is down when Alice sends: the second takes the
            # message and the new room key that goes with it.
            stop_meta(meta)
            await alice.dms.send(gid, "while the first node was down")
            start_meta_again(meta)
            assert await items_on(meta, tokens) == 0
            # Back up: the next message goes to both, under that same key.
            await alice.dms.send(gid, "on both nodes")
            assert await items_on(meta, tokens) == 1

            # The second node is down when Olive comes back: the copy on the
            # first can't be read without the key, so it stays there, also
            # after she restarts.
            stop_meta(m2)
            home = Paths.under(tmp_path / "owner")
            for _ in range(2):
                back = Core(home, emit=Events())
                await back.unlock(PASS)
                for _ in range(3):
                    await poll(back)
                assert await texts(back, gid) == ["first"]
                await back.shutdown()
            assert await items_on(meta, tokens) == 1, "the unreadable copy was deleted"

            # The second node returns: both messages, once each, and nothing
            # left on either node.
            start_meta_again(m2)
            back = Core(home, emit=Events())
            await back.unlock(PASS)
            await until_text(back, gid, "on both nodes")
            await until_text(back, gid, "while the first node was down")
            for _ in range(3):
                await poll(back)
            shown = await texts(back, gid)
            assert shown == ["first", "while the first node was down", "on both nodes"], shown
            assert await items_on(meta, tokens) == 0
            assert await items_on(m2, tokens) == 0
            for c in (back, alice):
                await c.shutdown()

        run(main(), 180)


def unreadable(n):
    """A Megolm event for a session nobody has the key to."""
    from openarcanum.dm import MAGIC
    from openarcanum.wire import dm_pb2 as dm

    env = dm.Envelope(
        megolm=dm.MegolmEnvelope(sender_curve25519=bytes(32), session_id=f"no-key-{n}", ciphertext=b"x" * 64)
    )
    return MAGIC + env.SerializeToString()


def test_unreadable_items_neither_block_the_inbox_nor_get_lost(tmp_path, monkeypatch):
    """More than a page (200) of items waiting for keys that never come:
    newer messages behind them still arrive; the waiting ones stay on the
    node, and are read again after they expire from memory."""
    import openarcanum.dm as dmmod

    # Room for 250 drops in a minute (the per-inbox limit is a production default).
    with network(tmp_path, mailbox_drops_per_minute="1000") as (meta, _relay, community):
        node = community()

        async def main():
            cid, people = await community_of(tmp_path, meta, node, "Alice")
            (olive, _), (alice, _) = people
            await olive.set_setting("dm_policy", "members")
            gid = (await alice.dms.open_with_member(cid, await key_of(alice, cid, "Olive")))["id"]
            await alice.dms.send(gid, "first")
            await until_text(olive, gid, "first", alice)
            # Olive's mailbox for this group, as Alice knows it.
            inbox = unb64(next(iter(alice.dms.d["groups"][gid]["inboxes"].values())))
            for n in range(250):
                await alice.meta.drop(inbox, unreadable(n))
            await alice.dms.send(gid, "behind them")
            await until_text(olive, gid, "behind them")
            token = unb64(olive.dms.d["groups"][gid]["inbox_token"])
            assert len(await olive.meta.fetch_from(meta, token)) == 200  # first page: all still there
            held = sum(len(v) for v in olive.dms._waiting.values())
            assert held == 250, held
            # Expired from memory: read again from the node, not lost.
            monkeypatch.setattr(dmmod, "EARLY_SECONDS", -1)
            olive.dms._sweep()
            assert not olive.dms._waiting
            monkeypatch.setattr(dmmod, "EARLY_SECONDS", 600)
            await poll(olive)
            assert sum(len(v) for v in olive.dms._waiting.values()) == 250
            assert (await texts(olive, gid)).count("behind them") == 1
            for c in (olive, alice):
                await c.shutdown()

        run(main(), 120)


# ---------------------------------------------------------------- maintainer clusters (docs/protocol.md §7.5)


def test_merge_from_two_members_handles_each_item_once(tmp_path, monkeypatch):
    """A client reads every member of its maintainer cluster and merges by
    global id: disjoint items (a sender reached only one member) and
    overlapping ones (a sender reached both) are each handled once and
    deleted everywhere. A delete on one member isn't undone by the other:
    an item still on a member that was down when it was handled elsewhere
    is skipped and deleted there when it comes back. (The two nodes here
    don't replicate, the worst case: a partition that never heals.)"""
    with run_meta(writes_per_minute="60000") as m1, run_meta(writes_per_minute="60000") as m2:

        async def main():
            core = Core(Paths.under(tmp_path / "me"), emit=Events())
            await core.create_profile(PASS, "me")
            await core.set_maintainers([m1, m2])
            core.meta.groups = [[m1, m2]]  # one cluster
            handled: list[bytes] = []

            async def record(blob, source=None):
                handled.append(bytes(blob))
                return True

            monkeypatch.setattr(core.dms, "on_blob", record)
            token = os.urandom(32)
            inbox = inbox_address(token)

            async def read(full=True):
                for base in core.meta.poll_nodes(full):
                    await core.dms._poll_node(base, token)

            # Disjoint.
            await MetaClient([m1]).drop(inbox, b"A: reached m1 only")
            await MetaClient([m2]).drop(inbox, b"B: reached m2 only")
            await read(full=False)  # the fast loop reads one member...
            assert handled == [b"A: reached m1 only"]
            await read()  # ...the slow loop all of them
            assert sorted(handled) == [b"A: reached m1 only", b"B: reached m2 only"]
            # Overlapping: one item under one global id, handled once.
            await MetaClient([m1, m2]).drop(inbox, b"C: reached both")
            await read()
            assert handled.count(b"C: reached both") == 1
            assert await items_on(m1, [token]) == 0 and await items_on(m2, [token]) == 0

            # D reaches both; m2 is down when it's handled from m1.
            await MetaClient([m1, m2]).drop(inbox, b"D: on both, m2 then down")
            stop_meta(m2)
            await read()
            assert handled.count(b"D: on both, m2 then down") == 1
            start_meta_again(m2)
            assert await items_on(m2, [token]) == 1  # the copy on m2 is still there
            core.meta.health.ok(m2, 0.01)
            await read()
            assert handled.count(b"D: on both, m2 then down") == 1, "a handled item came back from another member"
            assert await items_on(m2, [token]) == 0
            await core.shutdown()

        run(main(), 120)


class Proxy:
    """A TCP proxy in front of a node, for the links between cluster
    members: cut() is a network partition between them (open connections
    drop, new ones are refused), heal() ends it. Clients reach the nodes
    directly."""

    def __init__(self, target: int):
        self.port = free_port()
        self.target = target
        self.server = None
        self.conns: set = set()

    async def start(self):
        self.server = await asyncio.start_server(self._handle, "127.0.0.1", self.port)

    async def _handle(self, r, w):
        try:
            tr, tw = await asyncio.open_connection("127.0.0.1", self.target)
        except OSError:
            w.close()
            return
        self.conns |= {w, tw}

        async def pipe(a, b):
            with contextlib.suppress(Exception):
                while data := await a.read(65536):
                    b.write(data)
                    await b.drain()
            b.close()

        await asyncio.gather(pipe(r, tw), pipe(tr, w))

    async def cut(self):
        self.server.close()
        for w in self.conns:
            w.close()
        self.conns.clear()
        await self.server.wait_closed()

    async def heal(self):
        await self.start()


def node_key(data_dir: str) -> str:
    out = subprocess.run(
        [str(binary("meta")), "id", "-data", data_dir], capture_output=True, text=True, check=True
    ).stdout
    return next(line.split()[-1] for line in out.splitlines() if line.startswith("node key:"))


@contextlib.contextmanager
def cluster_pair(tmp_path):
    """Two real maintainer nodes that trust each other and replicate through
    proxies (so the link between them can be cut), with a maintainer set
    signed by an operator key held here. Yields (m1, m2, operator, start)
    where `await start()` starts the proxies and nodes."""
    op = keys.SigningKey.generate()
    nodes = []
    for name in ("m1", "m2"):
        d = tmp_path / name
        d.mkdir()
        port = free_port()
        nodes.append({"dir": str(d), "port": port, "url": f"http://127.0.0.1:{port}", "key": node_key(str(d))})
    procs: list = []

    async def start():
        proxies = [Proxy(n["port"]) for n in nodes]
        for p in proxies:
            await p.start()
        for n, other, p in ((nodes[0], nodes[1], proxies[1]), (nodes[1], nodes[0], proxies[0])):
            env = {
                "OPENARCANUM_META_LISTEN": f"127.0.0.1:{n['port']}",
                "OPENARCANUM_META_DATA_DIR": n["dir"],
                "OPENARCANUM_META_WRITES_PER_MINUTE": "60000",
                "OPENARCANUM_META_TRUSTED_INTERVAL": "200ms",
                "OPENARCANUM_META_TRUSTED_PEERS": f"http://127.0.0.1:{p.port}#{other['key']}",
            }
            procs.append(Proc([str(binary("meta"))], env=env))
            wait_port(n["port"])
            META_PROCS[n["url"]] = (procs[-1], env, n["port"])
        # The set clients verify names the nodes' direct URLs.
        s = mp.MaintainerSet(operator_key=op.public, seq=1, issued_at=int(time.time() * 1000))
        s.expires_at = s.issued_at + 86400_000
        for i, n in enumerate(nodes):
            s.nodes.add(url=n["url"], node_key=keys.decode(n["key"]), role="root" if i == 0 else "member")
        body = s.SerializeToString()
        raw = mp.SignedMaintainerSet(set=body, signature=op.sign(keys.CTX_MAINTAINER_SET, body)).SerializeToString()
        (tmp_path / "set.bin").write_bytes(raw)
        for n in nodes:
            subprocess.run(
                [str(binary("meta")), "set", "publish", "-data", n["dir"], str(tmp_path / "set.bin")],
                check=True,
                capture_output=True,
            )
        return proxies

    try:
        yield nodes[0]["url"], nodes[1]["url"], op, start
    finally:
        for n in nodes:
            entry = META_PROCS.pop(n["url"], None)
            if entry:
                entry[0].stop()


def test_route_around_with_a_real_cluster(tmp_path):
    """Two real maintainer nodes in a cluster, replicating both ways. The
    sender reaches only m2; the recipient's fast loop reads the root (m1),
    which the partition keeps from getting the message, so it reads every
    member too (the slow loop) and gets it from m2, once. The delete reaches
    m1 by global id, and after the heal neither node holds anything and the
    message isn't shown twice. Then the root goes down: sending and
    receiving go through m2 with no change by the user."""
    with contextlib.ExitStack() as stack:
        meta, _relay, community = stack.enter_context(network(tmp_path))
        m1, m2, op, start = stack.enter_context(cluster_pair(tmp_path))
        node = community()

        async def main():
            proxies = await start()
            cid, people = await community_of(tmp_path, meta, node, "Alice")
            (olive, _), (alice, _) = people
            entry = f"{m1}#{keys.encode(op.public)}"
            await olive.set_maintainers([entry])
            await olive.refresh_clusters(force=True)
            assert olive.meta.urls == [m1, m2]
            await alice.set_maintainers([m2])  # the sender reaches only m2
            await olive.set_setting("dm_policy", "members")
            gid = (await alice.dms.open_with_member(cid, await key_of(alice, cid, "Olive")))["id"]
            await alice.dms.send(gid, "first")
            await until_text(olive, gid, "first", alice)
            tokens = inbox_tokens(olive)
            await olive.shutdown()

            for p in proxies:
                await p.cut()
            await alice.dms.send(gid, "only on m2")
            assert await items_on(m2, tokens) >= 1 and await items_on(m1, tokens) == 0

            home = Paths.under(tmp_path / "owner")
            back = Core(home, emit=Events())
            await back.unlock(PASS)
            assert back.meta.urls == [m1, m2]  # the cluster, from preferences
            await back.dms.poll_once(full=False)
            assert "only on m2" not in await texts(back, gid), "the fast loop read more than the root"
            await back.dms.poll_once(full=True)
            assert (await texts(back, gid)).count("only on m2") == 1

            for p in proxies:
                await p.heal()
            await wait_until(lambda: _settled(m1, m2, tokens), timeout=60)
            await back.dms.poll_once(full=True)
            assert (await texts(back, gid)).count("only on m2") == 1

            # The root goes down; the sender now uses the cluster too.
            await alice.set_maintainers([entry])
            await alice.refresh_clusters(force=True)
            stop_meta(m1)
            await alice.dms.send(gid, "with the root down")
            await back.dms.poll_once(full=True)
            await until_text(back, gid, "with the root down")
            assert (await texts(back, gid)).count("with the root down") == 1
            for c in (back, alice):
                await c.shutdown()

        run(main(), 240)


async def _settled(m1, m2, tokens):
    return await items_on(m1, tokens) == 0 and await items_on(m2, tokens) == 0
