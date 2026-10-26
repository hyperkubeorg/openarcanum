"""End-to-end flows for the directory, moderation, files and search, and relay
failover, against the real binaries."""

import asyncio
import os

import pytest
from conftest import CommunityNode, kill_relay, new_core, run, run_community, run_meta, run_relay, wait_until
from test_e2e_community import WIZARD, general, online

from openarcanum.core import UserError


def test_search_and_join_from_directory(tmp_path):
    with run_meta() as meta, run_relay(registry_urls=meta, heartbeat_interval="1s", public_url="{url}") as relay:
        node = CommunityNode(tmp_path / "c", [relay], [meta])
        node.start()
        try:

            async def main():
                owner, _ = await new_core(tmp_path / "owner", maintainers=[meta])
                wiz = dict(
                    WIZARD,
                    name="Sourdough Bakers",
                    description="Wild yeast and long ferments",
                    tags="bread, baking",
                    visibility="public",
                    join_policy="open",
                )
                cid = (await owner.claim(node.claim_code, wiz, "Olive"))["community"]
                await general(owner, cid)

                seeker, _ = await new_core(tmp_path / "seeker", maintainers=[meta])
                results = await wait_until(lambda: seeker.directory_search("ferment"), timeout=20)
                assert len(results) == 1 and results[0]["name"] == "Sourdough Bakers"
                assert results[0]["join_policy"] == "open"
                assert await seeker.directory_search("knitting") == []
                assert len(await seeker.directory_search("", tag="bread")) == 1
                preview = await seeker.preview({"listing": results[0]})
                assert preview["description"] == "Wild yeast and long ferments"
                r = await seeker.join({"listing": results[0]}, "new", "Sam")
                assert r["status"] == "admitted"
                await general(seeker, cid)
                await owner.shutdown()
                await seeker.shutdown()

            run(main(), 90)
        finally:
            node.stop()


def test_made_public_later_anyone_finds_and_joins_without_a_key(tmp_path):
    """A community created private and invite-only, then made public with the
    Open policy (Community settings → Visibility, Invites & join policy):
    search finds it and anyone joins from the listing with no key. With
    Request to join they wait for approval; with Invite only and no public
    key the server refuses."""
    with run_meta() as meta, run_relay(registry_urls=meta, heartbeat_interval="1s", public_url="{url}") as relay:
        node = CommunityNode(tmp_path / "c", [relay], [meta])
        node.start()
        try:

            async def main():
                owner, _ = await new_core(tmp_path / "owner", maintainers=[meta])
                cid = (await owner.claim(node.claim_code, dict(WIZARD, name="Rye Club"), "Olive"))["community"]
                await general(owner, cid)
                seeker, _ = await new_core(tmp_path / "seeker", maintainers=[meta])
                assert await seeker.directory_search("rye") == []

                async def listing(policy):
                    await owner.admin(cid, "update_settings", {"visibility": "public", "join_policy": policy})

                    async def found():
                        res = await seeker.directory_search("rye")
                        return res if res and res[0]["join_policy"] == policy else None

                    (res,) = await wait_until(found, timeout=20, interval=0.5)
                    assert not res.get("public_join_key")
                    return res

                # Invite only, listed: the listing alone isn't enough.
                res = await listing("invite_only")
                r = await seeker.join({"listing": res}, "new", "Sam")
                assert r["status"] == "refused" and "invite" in r["reason"]
                # Request to join: pending until approved.
                res = await listing("request")
                asker, _ = await new_core(tmp_path / "asker", maintainers=[meta])
                assert (await asker.join({"listing": res}, "new", "Ash"))["status"] == "pending"
                # Open: straight in, no key.
                res = await listing("open")
                preview = await seeker.preview({"listing": res})
                assert preview["join_policy"] == "open" and not preview["has_key"]
                r = await seeker.join({"listing": res}, "new", "Sam")
                assert r["status"] == "admitted"
                await general(seeker, cid)
                for c in (owner, seeker, asker):
                    await c.shutdown()

            run(main(), 120)
        finally:
            node.stop()


def test_pending_approval_and_ban(tmp_path):
    with run_relay() as relay, run_community(tmp_path, [relay]) as node:

        async def main():
            owner, _ = await new_core(tmp_path / "owner")
            cid = (await owner.claim(node.claim_code, WIZARD, "Olive"))["community"]
            await general(owner, cid)
            req = await owner.admin(cid, "create_invite", {"label": "ask first", "request_only": True})
            bob, bev = await new_core(tmp_path / "bob")
            r = await bob.join({"invite": req["invite"]}, "new", "Bob")
            assert r["status"] == "pending"
            await wait_until(lambda: bob.conns[cid].status == "pending")
            assert bob.community_summary(cid)["connection"] == "pending"
            pending = await owner.admin_query(cid, "pending")
            assert [p["display_name"] for p in pending] == ["Bob"]
            await owner.admin(cid, "approve", {"member": pending[0]["key"]})
            await online(bob, cid)
            await bev.wait(lambda n, p: n == "toast" and "admitted" in p["text"])
            # Ban: Bob is cut off and can't rejoin with a fresh invite.
            await owner.admin(cid, "ban", {"member": pending[0]["key"], "reason": "test"})
            await wait_until(lambda: bob.vault.data["communities"][cid]["status"] == "banned")
            bans = await owner.admin_query(cid, "bans")
            assert bans[0]["reason"] == "test"
            audit = await owner.admin_query(cid, "audit")
            assert all(e["signature_ok"] for e in audit)
            assert any("banned Bob" in e["summary"] for e in audit)
            await owner.shutdown()
            await bob.shutdown()

        run(main(), 90)


def test_files_and_search(tmp_path):
    with run_relay() as relay, run_community(tmp_path, [relay]) as node:

        async def main():
            owner, _ = await new_core(tmp_path / "owner")
            res = await owner.claim(node.claim_code, WIZARD, "Olive")
            cid = res["community"]
            gen = await general(owner, cid)
            alice, aev = await new_core(tmp_path / "alice")
            await alice.join({"invite": res["invite"]}, "new", "Alice")
            await general(alice, cid)

            data = os.urandom(2 * 1024 * 1024 + 5)
            f = tmp_path / "blob.bin"
            f.write_bytes(data)
            m = await owner.upload(cid, gen, str(f), "here's the file")
            assert m["files"][0]["name"] == "blob.bin" and m["files"][0]["size"] == len(data)
            got = await aev.wait(lambda n, p: n == "message" and p["message"].get("files"))
            out = tmp_path / "downloaded.bin"
            await alice.save_file(cid, got["message"]["files"][0], str(out))
            assert out.read_bytes() == data
            # Downloaded once, then served from the encrypted cache.
            assert alice.cache(cid).get_file(m["files"][0]["file_id"]) == data

            # A new community allows 10 MB per file; an admin can raise it to 25.
            big = tmp_path / "big.iso"
            with big.open("wb") as fh:
                fh.truncate(10 * 1024 * 1024 + 1)
            with pytest.raises(UserError, match="allows up to 10 MB per file"):
                await owner.upload(cid, gen, str(big))
            await owner.admin(cid, "update_settings", {"max_file_size": 25 * 1024 * 1024})
            await wait_until(lambda: owner.conns[cid].state.settings.max_file_size == 25 * 1024 * 1024)
            with big.open("wb") as fh:
                fh.truncate(25 * 1024 * 1024 + 1)
            with pytest.raises(UserError, match="25 MB"):
                await owner.upload(cid, gen, str(big))

            await owner.send(cid, gen, "the quarterly pumpkin report")
            hits = await alice.search_messages(cid, "pumpkin")
            assert [h["text"] for h in hits] == ["the quarterly pumpkin report"]
            await owner.shutdown()
            await alice.shutdown()

        run(main(), 120)


def test_relay_failover_mid_chat(tmp_path):
    """Kill a relay mid-chat; clients recover without losing stored messages."""
    with run_relay() as r1, run_relay() as r2:
        node = CommunityNode(tmp_path / "c", [r1, r2])
        node.start()
        try:

            async def main():
                owner, _ = await new_core(tmp_path / "owner")
                res = await owner.claim(node.claim_code, WIZARD, "Olive")
                cid = res["community"]
                gen = await general(owner, cid)
                alice, aev = await new_core(tmp_path / "alice")
                await alice.join({"invite": res["invite"]}, "new", "Alice")
                await general(alice, cid)
                for i in range(3):
                    await owner.send(cid, gen, f"before {i}")
                await wait_until(lambda: len(alice.cache(cid).channel(gen)) == 3)
                return owner, alice, aev, cid, gen

            async def phase2(owner, alice, aev, cid, gen, killed):
                # Whoever was on the dead relay fails over to the other one.
                await wait_until(
                    lambda: owner.conns[cid].status == "online" and owner.conns[cid].relay_url != killed, 30
                )
                await wait_until(
                    lambda: alice.conns[cid].status == "online" and alice.conns[cid].relay_url != killed, 30
                )
                for i in range(2):
                    await owner.send(cid, gen, f"after {i}")
                # Join messages (on by default) aren't part of the chat.
                await wait_until(
                    lambda: len([m for m in alice.cache(cid).channel(gen) if not m.get("system")]) == 5, 20
                )
                texts = [m["text"] for m in (await alice.history(cid, gen))["messages"] if not m.get("system")]
                assert texts == ["before 0", "before 1", "before 2", "after 0", "after 1"]
                await owner.shutdown()
                await alice.shutdown()

            loop = asyncio.new_event_loop()
            try:
                owner, alice, aev, cid, gen = loop.run_until_complete(asyncio.wait_for(main(), 60))
                killed = alice.conns[cid].relay_url
                kill_relay(killed)  # the relay Alice is on dies mid-chat
                loop.run_until_complete(asyncio.wait_for(phase2(owner, alice, aev, cid, gen, killed), 90))
            finally:
                loop.close()
        finally:
            node.stop()
