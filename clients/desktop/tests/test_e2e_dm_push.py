"""Direct messages arrive as they're sent (dm.py "mailbox watch"), not at
the next poll: the inbox is watched on the maintainer node, which says
"check now" when mail lands (internal/meta/watch.go)."""

from __future__ import annotations

import time

from conftest import network, run, wait_until
from test_e2e_dm import community_of, key_of, texts, until_text

from openarcanum import dm


def test_a_direct_message_arrives_without_waiting_for_a_poll(tmp_path, monkeypatch):
    # No scheduled poll in this test: what arrives came through the watch.
    monkeypatch.setattr(dm, "POLL_SECONDS", 3600)
    monkeypatch.setattr(dm, "FULL_POLL_SECONDS", 3600)
    monkeypatch.setattr(dm, "WATCH_MAX", 24)  # off by default for now (dm.py)
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            cid, [(olive, _), (alice, _)] = await community_of(tmp_path, meta, node, "Alice")
            await olive.set_setting("dm_policy", "members")
            await alice.set_setting("dm_policy", "members")
            gid = (await alice.dms.open_with_member(cid, await key_of(alice, cid, "Olive")))["id"]
            await alice.dms.send(gid, "hello")
            await until_text(olive, gid, "hello", alice)
            # Both now watch the conversation's inbox: no poll for a while.
            for c in (olive, alice):
                await wait_until(lambda c=c: any(not t.done() for t in c.dms._watches.values()), 10, 0.1)
            started = time.monotonic()
            await alice.dms.send(gid, "right away")

            async def arrived():
                return "right away" in await texts(olive, gid)

            await wait_until(arrived, 10, 0.05)
            assert time.monotonic() - started < 10
            for c in (olive, alice):
                await c.shutdown()

        run(main(), 120)
