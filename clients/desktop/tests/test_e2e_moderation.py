"""Moderation and custom emoji end to end against the real relay and community
server binaries: ban with history deletion, timeouts, bulk delete, member
screening, AutoMod, reports, the filtered audit log and custom emoji, and a
ban's network block on a dedicated relay."""

import asyncio
import contextlib
import contextvars
import struct
import time
import zlib

import pytest
from conftest import (
    CommunityNode,
    Proc,
    binary,
    free_port,
    new_core,
    run,
    run_community,
    run_relay,
    wait_port,
    wait_until,
)
from test_e2e_community import WIZARD, general

import openarcanum.relay as openarcanum_relay
from openarcanum.core import UserError
from openarcanum.moderation import rule_from_dict
from openarcanum.relay import Relay, RelayBlocked


def png(width: int, height: int) -> bytes:
    """A valid PNG of the given size (one colour)."""

    def chunk(kind: bytes, data: bytes) -> bytes:
        return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data))

    raw = b"".join(b"\x00" + b"\x00\x80\xff" * width for _ in range(height))
    ihdr = struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0)
    return b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", ihdr) + chunk(b"IDAT", zlib.compress(raw)) + chunk(b"IEND", b"")


def test_rule_from_dict_validates():
    r = rule_from_dict({"name": "w", "trigger": "keyword", "keywords": "a, b\nc", "patterns": "x,y\nz+", "block": True})
    assert list(r.keywords) == ["a", "b", "c"] and list(r.patterns) == ["x,y", "z+"]
    with pytest.raises(UserError):
        rule_from_dict({"name": "w", "trigger": "nonsense"})
    with pytest.raises(UserError):
        rule_from_dict({"name": "w", "trigger": "links", "timeout_seconds": 5})


def test_ban_op_network_block():
    from openarcanum import keys
    from openarcanum.admin_ops import OpError, build_op

    member = keys.encode(b"\x01" * 32)
    field, op = build_op("ban", {"member": member, "block_network": True, "block_seconds": 2592000}, None)
    assert field == "ban" and op.block_network and op.block_seconds == 2592000
    _, op = build_op("ban", {"member": member, "block_seconds": 86400}, None)
    assert not op.block_network and op.block_seconds == 0
    with pytest.raises(OpError):
        build_op("ban", {"member": member, "block_network": True, "block_seconds": 3600}, None)


def test_moderation_end_to_end(tmp_path):
    with run_relay() as relay, run_community(tmp_path, [relay]) as node:

        async def main():
            owner, oev = await new_core(tmp_path / "owner")
            res = await owner.claim(node.claim_code, WIZARD, "Olive")
            cid = res["community"]
            gen = await general(owner, cid)
            alice, aev = await new_core(tmp_path / "alice")
            await alice.join({"invite": res["invite"]}, "new", "Alice")
            await general(alice, cid)
            bob, _ = await new_core(tmp_path / "bob")
            await bob.join({"invite": res["invite"]}, "new", "Bob")
            await general(bob, cid)
            akey = alice.community_state(cid)["self"]["key"]
            bkey = bob.community_state(cid)["self"]["key"]

            # Reports reach moderators live; they resolve them.
            m = await bob.send(cid, gen, "something rude")
            with pytest.raises(UserError, match="at most 500"):
                await alice.moderation.report_message(cid, m["id"], "x" * 501)
            await alice.moderation.report_message(cid, m["id"], "please look")
            alert = await oev.wait(lambda n, p: n == "mod_alert" and p["report"]["kind"] == "member")
            assert alert["report"]["reason"] == "please look" and alert["report"]["reporter_name"] == "Alice"
            with pytest.raises(UserError, match="already reported"):
                await alice.moderation.report_message(cid, m["id"])
            queue = await owner.admin_query(cid, "reports")
            assert [r["message_id"] for r in queue] == [m["id"]]
            with pytest.raises(UserError):
                await alice.admin_query(cid, "reports")
            await owner.admin(cid, "resolve_report", {"id": queue[0]["id"]})
            assert await owner.admin_query(cid, "reports") == []
            assert (await owner.admin_query(cid, "reports", {"include_closed": True}))[0]["status"] == "resolved"

            # AutoMod: a keyword rule blocks, with a clear reason.
            await owner.admin(
                cid,
                "save_automod_rule",
                {"name": "No mackerel", "trigger": "keyword", "keywords": "mackerel", "block": True, "enabled": True},
            )
            rules = await owner.admin_query(cid, "automod_rules")
            assert rules[0]["name"] == "No mackerel" and rules[0]["keywords"] == ["mackerel"]
            with pytest.raises(UserError, match="AutoMod blocked your message"):
                await alice.send(cid, gen, "I love MACKEREL")
            await owner.admin(cid, "delete_automod_rule", {"id": rules[0]["id"]})
            await alice.send(cid, gen, "mackerel is fine now")

            # AutoMod alert: the message goes through; moderators get a live
            # alert and an entry in the report queue pointing at it.
            await owner.admin(
                cid,
                "save_automod_rule",
                {"name": "Watch herring", "trigger": "keyword", "keywords": "herring", "alert": True, "enabled": True},
            )
            watched = await alice.send(cid, gen, "a red herring")
            alert = await oev.wait(lambda n, p: n == "mod_alert" and p["report"]["kind"] == "automod")
            assert alert["report"]["rule_name"] == "Watch herring"
            assert alert["report"]["message_id"] == watched["id"] and alert["report"]["author_name"] == "Alice"
            queue = await owner.admin_query(cid, "reports")
            assert [(r["kind"], r["message_id"]) for r in queue] == [("automod", watched["id"])]
            await owner.admin(cid, "resolve_report", {"id": queue[0]["id"]})
            # AutoMod timeout: the message is blocked and the member timed out
            # (a system audit entry); she can't post until it's lifted.
            await owner.admin(
                cid,
                "save_automod_rule",
                {
                    "name": "No sardines",
                    "trigger": "keyword",
                    "keywords": "sardine",
                    "block": True,
                    "timeout_seconds": 60,
                    "enabled": True,
                },
            )
            with pytest.raises(UserError, match="AutoMod blocked your message"):
                await alice.send(cid, gen, "sardine sale")
            await wait_until(lambda: alice.community_state(cid)["self"]["timeout_until"] > 0, 15)
            with pytest.raises(UserError, match="timed out until"):
                await alice.send(cid, gen, "hello?")
            auto = await owner.admin_query(cid, "audit", {"action": "automod"})
            assert any("timed out Alice for 1 minute" in e["summary"] for e in auto), auto
            await owner.admin(cid, "timeout", {"member": akey, "seconds": 0})
            await wait_until(lambda: alice.community_state(cid)["self"]["timeout_until"] == 0, 15)
            for r in await owner.admin_query(cid, "automod_rules"):
                await owner.admin(cid, "delete_automod_rule", {"id": r["id"]})

            # Timeout: the member sees when it ends and can't post.
            await owner.admin(cid, "timeout", {"member": akey, "seconds": 600, "reason": "cool down"})
            await wait_until(lambda: alice.community_state(cid)["self"]["timeout_until"] > 0, 15)
            with pytest.raises(UserError, match="timed out until"):
                await alice.send(cid, gen, "hello?")
            assert not alice.community_state(cid)["channels"][0]["can"]["send_messages"]
            mods_view = {x["display_name"]: x for x in await owner.members(cid)}
            assert mods_view["Alice"]["timeout_until"] > 0
            assert {x["display_name"]: x for x in await bob.members(cid)}["Alice"]["timeout_until"] == 0
            await owner.admin(cid, "timeout", {"member": akey, "seconds": 0})
            await wait_until(lambda: alice.community_state(cid)["self"]["timeout_until"] == 0, 15)
            await alice.send(cid, gen, "back")
            with pytest.raises(UserError, match="60 seconds"):
                await owner.admin(cid, "timeout", {"member": akey, "seconds": 30})

            # Bulk delete the last 2 messages.
            await bob.send(cid, gen, "one")
            await bob.send(cid, gen, "two")
            r = await owner.admin(cid, "bulk_delete", {"channel_id": gen, "last": 2})
            assert r["deleted"] == 2
            hist = (await owner.history(cid, gen))["messages"]
            assert [x["deleted"] for x in hist[-2:]] == [True, True]
            last = hist[-1]["id"]
            await aev.wait(lambda n, p: n == "message" and p["message"]["id"] == last and p["message"]["deleted"])

            # Ban with "delete the last 24 hours".
            await bob.send(cid, gen, "bob's last words")
            await owner.admin(cid, "ban", {"member": bkey, "reason": "spam", "delete_message_seconds": 86400})
            hist = (await owner.history(cid, gen))["messages"]
            # Everything Bob posted; his join notice (a system message) stays.
            assert all(x["deleted"] for x in hist if x["author"] == bkey and not x["system"])
            assert any(x["author"] == bkey and not x["deleted"] for x in hist if x["system"])
            with pytest.raises(UserError, match="the last hour, 6 hours"):
                await owner.admin(cid, "ban", {"member": akey, "delete_message_seconds": 100})

            # The audit log, filtered on the server.
            page = await owner.admin_query(cid, "audit_page", {"action": "timeout", "target": akey})
            # The AutoMod timeout was lifted by the owner too: three timeout actions.
            assert [e["action"] for e in page["entries"]] == ["timeout"] * 3 and not page["more"]
            assert all(e["signature_ok"] and e["target_name"] == "Alice" for e in page["entries"])
            bans = await owner.admin_query(cid, "audit", {"action": "ban"})
            assert "deleted" in bans[0]["summary"] and "24 hours" in bans[0]["summary"]
            assert await owner.admin_query(cid, "audit", {"since": "2001-01-01", "until": "2001-01-02"}) == []
            with pytest.raises(UserError, match="Dates look like"):
                await owner.admin_query(cid, "audit", {"since": "yesterday"})

            # Custom emoji: upload from a file, fetch as a data: URI, toggle off.
            path = tmp_path / "party.png"
            path.write_bytes(png(48, 48))
            up = await owner.moderation.emoji_upload(cid, "party", str(path))
            await wait_until(lambda: [e["name"] for e in alice.community_state(cid)["emoji"]] == ["party"], 15)
            uri = await alice.moderation.emoji_image(cid, up["id"])
            assert uri.startswith("data:image/png;base64,")
            await alice.send(cid, gen, f"yay :party:{up['id']}")
            big = tmp_path / "big.png"
            big.write_bytes(png(200, 20))
            with pytest.raises(UserError, match="at most 128"):
                await owner.moderation.emoji_upload(cid, "big", str(big))
            with pytest.raises(UserError, match="2 to 32"):
                await owner.moderation.emoji_upload(cid, "Bad Name", str(path))
            await owner.admin(cid, "rename_emoji", {"id": up["id"], "name": "fiesta"})
            await owner.admin(cid, "update_settings", {"features": {"custom_emoji": False}})
            await wait_until(lambda: alice.community_state(cid)["emoji"] == [], 15)
            with pytest.raises(UserError, match="turned off"):
                await owner.moderation.emoji_upload(cid, "again", str(path))
            await owner.admin(cid, "update_settings", {"features": {"custom_emoji": True}})

            # Member screening: a newcomer waits before posting.
            await owner.admin(cid, "update_settings", {"screening_minutes": 30})
            carol, _ = await new_core(tmp_path / "carol")
            await carol.join({"invite": res["invite"]}, "new", "Carol")
            await general(carol, cid)
            assert carol.community_state(cid)["self"]["can_post_at"] > 0
            with pytest.raises(UserError, match="New members can post from"):
                await carol.send(cid, gen, "hi")
            with pytest.raises(UserError, match="at most 7 days"):
                await owner.admin(cid, "update_settings", {"screening_minutes": 10081})
            await owner.admin(cid, "update_settings", {"screening_minutes": 0})
            await wait_until(lambda: carol.community_state(cid)["self"]["can_post_at"] == 0, 15)
            await carol.send(cid, gen, "hi")
            for c in (owner, alice, bob, carol):
                await c.shutdown()

        run(main(), 180)


def test_raid_revoke_end_to_end(tmp_path):
    """Raid revoke through the client: the invite is revoked and everyone
    who joined with it in the window is removed; others stay."""
    with run_relay() as relay, run_community(tmp_path, [relay]) as node:

        async def main():
            owner, _ = await new_core(tmp_path / "owner")
            res = await owner.claim(node.claim_code, WIZARD, "Olive")
            cid = res["community"]
            gen = await general(owner, cid)
            raid = await owner.admin(cid, "create_invite", {"label": "raid key"})
            cores = {}
            for name, invite in (("Rae", raid["invite"]), ("Rex", raid["invite"]), ("Sue", res["invite"])):
                core, _ = await new_core(tmp_path / name.lower())
                await core.join({"invite": invite}, "new", name)
                await general(core, cid)
                cores[name] = core
            key = next(k for k in await owner.admin_query(cid, "join_keys") if k["label"] == "raid key")
            assert key["uses"] == 2
            with pytest.raises(UserError):
                await cores["Sue"].admin(cid, "raid_revoke", {"join_key_id": key["id"], "hours": 1})
            r = await owner.admin(cid, "raid_revoke", {"join_key_id": key["id"], "hours": 1})
            assert r["removed"] == 2
            for name in ("Rae", "Rex"):
                await wait_until(lambda n=name: cores[n].vault.data["communities"][cid]["status"] == "removed", 15)
            await cores["Sue"].send(cid, gen, "still here")
            names = {m["display_name"] for m in await owner.members(cid)}
            assert "Sue" in names and not names & {"Rae", "Rex"}
            key = next(k for k in await owner.admin_query(cid, "join_keys") if k["label"] == "raid key")
            assert key["revoked"]
            late, _ = await new_core(tmp_path / "late")
            r = await late.join({"invite": raid["invite"]}, "new", "Late")
            assert r["status"] == "refused" and "revoked" in r["reason"]
            audit = await owner.admin_query(cid, "audit", {"action": "raid_revoke"})
            assert len(audit) == 1 and audit[0]["signature_ok"]
            for c in (owner, late, *cores.values()):
                await c.shutdown()

        run(main(), 120)


# The local address a client's relay connections leave from, per asyncio
# context (every task a core starts inherits it). Any 127.0.0.0/8 address
# reaches the loopback relay on Linux, so clients look like different
# networks to it while the community server connects from 127.0.0.1.
SOURCE: contextvars.ContextVar[str | None] = contextvars.ContextVar("relay_source", default=None)


@pytest.fixture
def sourced_relays(monkeypatch):
    real = openarcanum_relay.connect

    def connect(url, **kw):
        if SOURCE.get():
            kw["local_addr"] = (SOURCE.get(), 0)
        return real(url, **kw)

    monkeypatch.setattr(openarcanum_relay, "connect", connect)


async def from_address(source: str, coro):
    token = SOURCE.set(source)
    try:
        return await coro
    finally:
        SOURCE.reset(token)


@contextlib.contextmanager
def dedicated_network(tmp_path):
    """A community server pinned to one relay dedicated to it. The relay
    reads the server's key from the file the server writes, as the Helm
    chart sets it up (docs/kubernetes.md). Yields the node and relay URL."""
    port = free_port()
    url = f"ws://127.0.0.1:{port}/ws"
    key_file = tmp_path / "server.pub"
    node = CommunityNode(tmp_path / "community", [url])
    node.start(server_public_key_file=key_file)
    relay = None
    try:
        deadline = time.monotonic() + 15
        while not key_file.exists():
            assert time.monotonic() < deadline, "the community server never wrote its public key file"
            time.sleep(0.05)
        relay = Proc(
            [str(binary("relay"))],
            env={
                "OPENARCANUM_RELAY_LISTEN": f"127.0.0.1:{port}",
                "OPENARCANUM_RELAY_MEDIA_UDP_PORT": "0",
                "OPENARCANUM_RELAY_MEDIA_LOOPBACK": "true",
                "OPENARCANUM_RELAY_DEDICATED_SERVER_KEY_FILE": str(key_file),
                "OPENARCANUM_RELAY_BLOCK_FILE": str(tmp_path / "blocks.json"),
            },
        )
        wait_port(port)
        run(wait_endpoint(url, bytes.fromhex(key_file.read_text().strip())))
        yield node, url
    finally:
        if relay is not None:
            relay.stop()
        node.stop()


async def wait_endpoint(url: str, server_key: bytes) -> None:
    """Wait until the community server is attached to the relay."""
    deadline = time.monotonic() + 20
    while True:
        r = await Relay(url).connect(timeout=5)
        try:
            await r.open_stream(server_key, timeout=2)
            return
        except Exception:
            assert time.monotonic() < deadline, "the community server never attached to its dedicated relay"
            await asyncio.sleep(0.2)
        finally:
            await r.close()


def test_ban_blocks_network_on_dedicated_relay(tmp_path, sourced_relays):
    """A ban with "also block their network address" makes the dedicated
    relay drop the member and refuse their address; unbanning lets it back
    in. Nobody on the community side ever sees the address."""
    with dedicated_network(tmp_path) as (node, url):

        async def main():
            owner, _ = await new_core(tmp_path / "owner")
            res = await owner.claim(node.claim_code, WIZARD, "Olive")
            cid = res["community"]
            await general(owner, cid)
            await wait_until(lambda: owner.community_summary(cid)["dedicated_relay"], 15)

            async def join(name: str):
                core, ev = await new_core(tmp_path / name.lower())
                await core.join({"invite": res["invite"]}, "new", name)
                await general(core, cid)
                return core, ev

            mallory, _ = await from_address("127.0.0.7", join("Mallory"))
            bob, _ = await from_address("127.0.0.8", join("Bob"))
            mkey = mallory.community_state(cid)["self"]["key"]
            # The member's client keeps the relay off its DM relays.
            assert mallory.vault.data["communities"][cid]["dedicated_relays"] == [url]
            assert await mallory.dms._pick_relay(cid) != url

            r = await owner.admin(
                cid, "ban", {"member": mkey, "reason": "spam", "block_network": True, "block_seconds": 604800}
            )
            assert r["network_block"] == "blocked"
            await wait_until(lambda: mallory.vault.data["communities"][cid]["status"] == "banned", 15)
            bans = await owner.admin_query(cid, "bans")
            assert bans[0]["network_block"] == "blocked" and bans[0]["network_block_until"] > time.time() * 1000

            # Reconnecting from the same address: the relay refuses it.
            with pytest.raises(RelayBlocked):
                await from_address("127.0.0.7", Relay(url).connect(timeout=5))
            # Others are unaffected.
            other = await from_address("127.0.0.9", Relay(url).connect(timeout=5))
            await other.close()
            assert bob.conns[cid].status == "online"

            audit = await owner.admin_query(cid, "audit", {"action": "network_block"})
            assert len(audit) == 1 and "blocked the network address of Mallory" in audit[0]["summary"]
            assert all("127.0.0" not in e["summary"] for e in await owner.admin_query(cid, "audit"))

            # Unbanning lifts the block.
            await owner.admin(cid, "unban", {"member": mkey})
            back = await from_address("127.0.0.7", Relay(url).connect(timeout=5))
            await back.close()
            lifted = await owner.admin_query(cid, "audit", {"action": "network_unblock"})
            assert len(lifted) == 1 and "lifted the network block on Mallory" in lifted[0]["summary"]
            for c in (owner, mallory, bob):
                await c.shutdown()

        run(main(), 120)


def test_mod_queue_counts_notifications_and_community_levels(tmp_path):
    """What the moderator badges show, from the server's queue counts on the
    community summary: open reports for members with Manage Messages, join
    requests for those who approve them, live and cleared when resolved. A
    new report gives moderators a desktop notification that follows the
    community's notification level (Nothing silences it; the count stays).
    Per-community levels, and Mark all as read."""
    with run_relay() as relay, run_community(tmp_path, [relay]) as node:

        async def main():
            owner, oev = await new_core(tmp_path / "owner")
            res = await owner.claim(node.claim_code, WIZARD, "Olive")
            cid = res["community"]
            gen = await general(owner, cid)
            alice, aev = await new_core(tmp_path / "alice")
            await alice.join({"invite": res["invite"]}, "new", "Alice")
            await general(alice, cid)
            assert owner.community_summary(cid)["mod_queue"] == {"reports": 0, "pending": 0}
            assert alice.community_summary(cid)["mod_queue"] == {"reports": 0, "pending": 0}

            def reports(core):
                return core.community_summary(cid)["mod_queue"]["reports"]

            # A report: the moderator's count goes up live, with a desktop
            # notification naming who reported whom; the member sees nothing.
            m = await owner.send(cid, gen, "owner says hi")
            await alice.moderation.report_message(cid, m["id"], "testing")
            await wait_until(lambda: reports(owner) == 1)
            note = await oev.wait(lambda n, p: n == "notify" and p.get("report"))
            assert note["title"] == "New report" and note["body"] == "Alice reported a message by Olive"
            assert note["community"] == cid and note["community_name"] == owner.community_summary(cid)["name"]
            assert reports(alice) == 0 and not [p for p in aev.of("notify") if p.get("report")]

            # Nothing for this community: no notification for the next report,
            # but the count (the badge) still goes up.
            await owner.set_community_notify(cid, "nothing")
            assert owner.community_summary(cid)["notify"] == "nothing"
            assert owner.community_summary(cid)["notify_level"] == "nothing"
            m2 = await owner.send(cid, gen, "second")
            await alice.moderation.report_message(cid, m2["id"])
            await wait_until(lambda: reports(owner) == 2)
            await asyncio.sleep(0.5)
            assert len([p for p in oev.of("notify") if p.get("report")]) == 1
            with pytest.raises(UserError):
                await owner.set_community_notify("not-a-community", "all")

            # Resolving and dismissing clear the count.
            for r in await owner.admin_query(cid, "reports"):
                await owner.admin(cid, "resolve_report", {"id": r["id"], "dismiss": r["message_id"] == m2["id"]})
            await wait_until(lambda: reports(owner) == 0)

            # A join request counts for those who approve them.
            ask = await owner.admin(cid, "create_invite", {"label": "ask", "request_only": True, "expires_days": 1})
            carol, _ = await new_core(tmp_path / "carol")
            assert (await carol.join({"invite": ask["invite"]}, "new", "Carol"))["status"] == "pending"
            await wait_until(lambda: owner.community_summary(cid)["mod_queue"]["pending"] == 1)
            assert alice.community_summary(cid)["mod_queue"]["pending"] == 0
            pending = await owner.admin_query(cid, "pending")
            await owner.admin(cid, "approve", {"member": pending[0]["key"]})
            await wait_until(lambda: owner.community_summary(cid)["mod_queue"]["pending"] == 0)

            # Back to the default level; Mark all as read.
            await owner.set_community_notify(cid, "")
            assert owner.community_summary(cid)["notify"] == ""
            assert owner.community_summary(cid)["notify_level"] == "all"
            await alice.send(cid, gen, "unread for the owner")
            await wait_until(lambda: owner.community_summary(cid)["unread"] > 0)
            owner.mark_community_read(cid)
            assert owner.community_summary(cid)["unread"] == 0
            for c in (owner, alice, carol):
                await c.shutdown()

        run(main(), 120)
