"""DMs and group DMs end to end (docs/protocol.md §9): real relay, maintainer
node and community server binaries, several clients."""

import asyncio
import sqlite3
import time
from pathlib import Path

import conftest
import pytest
from conftest import PASS, Events, network, new_core, run, wait_until
from test_e2e_community import WIZARD, general

from openarcanum import groups, keys
from openarcanum.core import Core, UserError
from openarcanum.dm import b64, unb64
from openarcanum.paths import Paths

pytestmark = pytest.mark.timeout(300)


async def join_as(tmp_path, meta, owner, cid, name):
    core, ev = await new_core(tmp_path / name.lower(), maintainers=[meta])
    inv = await owner.admin(cid, "create_invite", {})
    await core.join({"invite": inv["invite"]}, "new", name)
    await general(core, cid)
    await core.dms.set_contact_card(cid, True)
    return core, ev


async def community_of(tmp_path, meta, node, *names):
    owner, oev = await new_core(tmp_path / "owner", "owner-desktop", maintainers=[meta])
    res = await owner.claim(node.claim_code, WIZARD, "Olive")
    cid = res["community"]
    await general(owner, cid)
    await owner.dms.set_contact_card(cid, True)
    people = [(owner, oev)]
    for n in names:
        people.append(await join_as(tmp_path, meta, owner, cid, n))
    return cid, people


async def key_of(core, cid, name):
    return next(m["key"] for m in await core.members(cid) if m["display_name"] == name)


async def poll(*cores):
    for c in cores:
        await c.dms.poll_once()


async def texts(core, gid):
    return [m["text"] for m in core.dms.history(gid) if not m.get("system")]


async def until_text(core, gid, text, *others):
    async def check():
        await poll(core, *others)
        return text in await texts(core, gid)

    for _ in range(60):
        if await check():
            return
        await conftest.asyncio.sleep(0.25)
    raise AssertionError(f"{text!r} never arrived: {await texts(core, gid)}")


def test_direct_dm_request_and_message_features(tmp_path):
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            cid, [(owner, _), (alice, _)] = await community_of(tmp_path, meta, node, "Alice")
            gid = (await alice.dms.open_with_member(cid, await key_of(alice, cid, "Olive")))["id"]
            # Opening again finds the same conversation.
            assert (await alice.dms.open_with_member(cid, await key_of(alice, cid, "Olive")))["id"] == gid
            await poll(owner)
            # Contacts only (the default): a stranger's first DM is a request.
            assert [(c["id"], c["status"], c["direct"]) for c in owner.dms.list()] == [(gid, "request", True)]
            # Each side's list names the other person (the UI's Message
            # finds a 1:1 by it).
            for core in (owner, alice):
                info = core.dms.info(gid)
                other = [p["id"] for p in info["members"] + info["invited"] if not p["me"]]
                assert len(other) == 1 and core.dms.list()[0]["peers"] == other
            with pytest.raises(UserError, match="Accept"):
                await owner.dms.send(gid, "too early")
            # A request shows what was sent, so the recipient can decide.
            await alice.dms.send(gid, "can we talk?")
            await until_text(owner, gid, "can we talk?", alice)
            assert owner.dms.list()[0]["status"] == "request"
            await owner.dms.accept(gid)
            await poll(alice)
            assert alice.dms.info(gid)["status"] == "active"
            secret = "the falcon flies at midnight"
            await alice.dms.send(gid, secret)
            await until_text(owner, gid, secret, alice)
            await owner.dms.send(gid, "message received")
            await until_text(alice, gid, "message received", owner)
            hist = alice.dms.history(gid)
            assert [m["mine"] for m in hist if not m.get("system")] == [True, True, False]
            first = next(m for m in owner.dms.history(gid) if m.get("text") == secret)
            assert owner.dms.list()[0]["unread"] == 2
            await owner.dms.mark_read(gid)
            assert owner.dms.list()[0]["unread"] == 0

            # Replies, edits, reactions, pins, deletes.
            await owner.dms.send(gid, "a reply", reply_to=first["id"])
            await until_text(alice, gid, "a reply", owner)
            reply = next(m for m in alice.dms.history(gid) if m.get("text") == "a reply")
            assert reply["reply_to"] == first["id"]
            await alice.dms.edit(gid, first["id"], "the falcon flies at noon")
            await alice.dms.react(gid, reply["id"], "👍")
            await alice.dms.pin(gid, reply["id"])

            async def applied():
                await poll(owner, alice)
                m = owner.dms.store.find(gid, first["id"])
                r = owner.dms.store.find(gid, reply["id"])
                return m["text"] == "the falcon flies at noon" and m.get("edited_at") and r["reactions"] and r["pinned"]

            await wait_until(applied, 20, 0.25)
            assert owner.dms.info(gid)["pins"][0]["id"] == reply["id"]
            with pytest.raises(UserError, match="your own"):
                await owner.dms.edit(gid, first["id"], "not mine to edit")
            with pytest.raises(UserError, match="your own"):
                await owner.dms.delete(gid, first["id"])
            await alice.dms.delete(gid, first["id"])

            async def deleted():
                await poll(owner, alice)
                return owner.dms.store.find(gid, first["id"]).get("deleted")

            await wait_until(deleted, 20, 0.25)
            assert owner.dms.store.find(gid, first["id"])["text"] == ""
            # Limits.
            with pytest.raises(UserError, match="at most"):
                await owner.dms.send(gid, "x" * 4001)
            with pytest.raises(UserError, match="empty"):
                await owner.dms.send(gid, "   ")
            # A direct conversation can't grow or be renamed.
            assert not owner.dms.info(gid)["can_add"]
            with pytest.raises(UserError):
                await owner.dms.rename(gid, "a name")

            # Offline delivery: Alice is gone while the owner writes.
            await alice.shutdown()
            await owner.dms.send(gid, "while you were away")
            alice2 = Core(Paths.under(tmp_path / "alice"), emit=Events())
            await alice2.unlock(PASS)
            await until_text(alice2, gid, "while you were away")
            # Nobody else can read DMs: the community server holds no plaintext.
            for db in Path(node.data).glob("community.db*"):
                assert secret.encode() not in db.read_bytes()
            await owner.shutdown()
            await alice2.shutdown()

        run(main(), 180)
        db = Path(conftest.META_DIRS[meta]) / "meta.db"
        con = sqlite3.connect(db)
        blobs = [b for (b,) in con.execute("SELECT blob FROM mailbox")]
        con.close()
        assert not any(b"falcon" in b or b"while you were away" in b for b in blobs)


def test_group_dm_lifecycle(tmp_path):
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            cid, people = await community_of(tmp_path, meta, node, "Alice", "Bob", "Carol")
            (owner, _), (alice, _), (bob, _), (carol, _) = people
            for c in (alice, bob, carol):
                await owner.dms.add_contact(
                    cid, await key_of(owner, cid, c.vault.data["communities"][cid]["display_name"])
                )
                await c.dms.add_contact(cid, await key_of(c, cid, "Olive"))
            gid = (
                await owner.dms.create_group(
                    cid, [await key_of(owner, cid, "Alice"), await key_of(owner, cid, "Bob")], "Plans"
                )
            )["id"]
            await poll(alice, bob)
            # Group invitations always need accepting, even from contacts.
            assert alice.dms.list()[0]["status"] == "invited"
            await alice.dms.accept(gid)
            await bob.dms.accept(gid)

            # They accepted at the same time; the group converges on both.
            async def everyone_in():
                await poll(owner, alice, bob)
                return all(
                    sorted(m["name"] for m in c.dms.info(gid)["members"]) == ["Alice", "Bob", "Olive"]
                    for c in (owner, alice, bob)
                )

            await wait_until(everyone_in, 20, 0.25)
            await owner.dms.send(gid, "hello group")
            await until_text(alice, gid, "hello group", owner, bob)
            await until_text(bob, gid, "hello group", owner, alice)
            await bob.dms.send(gid, "hi from bob")
            await until_text(alice, gid, "hi from bob", owner, bob)

            # Only the owner can rename or change settings, enforced by every client.
            with pytest.raises(UserError):
                await alice.dms.rename(gid, "Alice's group")
            with pytest.raises(UserError):
                await alice.dms.set_options(gid, members_can_add=True)
            assert not alice.dms.info(gid)["can_add"]
            await owner.dms.rename(gid, "Weekend plans")
            await owner.dms.set_options(gid, members_can_add=True)

            async def renamed():
                await poll(alice, bob)
                return alice.dms.info(gid)["name"] == "Weekend plans" and alice.dms.info(gid)["can_add"]

            await wait_until(renamed, 20, 0.25)

            # A new member doesn't see anything from before they joined. (Carol's
            # policy is contacts only, so she adds Alice first.)
            await carol.dms.add_contact(cid, await key_of(carol, cid, "Alice"))
            await alice.dms.invite(gid, cid, [await key_of(alice, cid, "Carol")])
            await poll(carol)
            await carol.dms.accept(gid)
            await poll(owner, alice, bob)
            await alice.dms.send(gid, "welcome carol")
            await until_text(carol, gid, "welcome carol", owner, alice, bob)
            assert "hello group" not in await texts(carol, gid)
            assert "hi from bob" not in await texts(carol, gid)

            # Removing Bob: he learns it, and can't read anything new.
            bob_id = next(m["id"] for m in owner.dms.info(gid)["members"] if m["name"] == "Bob")
            await owner.dms.remove_member(gid, bob_id)

            async def bob_removed():
                await poll(bob)
                return bob.dms.info(gid)["status"] == "removed"

            await wait_until(bob_removed, 20, 0.25)
            await poll(alice, carol)
            await owner.dms.send(gid, "bob is gone")
            await until_text(carol, gid, "bob is gone", owner, alice)
            await poll(bob)
            assert "bob is gone" not in await texts(bob, gid)
            with pytest.raises(UserError):
                await bob.dms.send(gid, "still here?")

            # Leaving; the owner must hand over first.
            with pytest.raises(UserError, match="owner"):
                await owner.dms.leave(gid)
            alice_id = next(m["id"] for m in owner.dms.info(gid)["members"] if m["name"] == "Alice")
            await owner.dms.transfer(gid, alice_id)
            await owner.dms.leave(gid)
            assert gid not in [c["id"] for c in owner.dms.list()]

            async def left():
                await poll(alice, carol)
                return [m["name"] for m in alice.dms.info(gid)["members"]] == ["Alice", "Carol"] and alice.dms.info(
                    gid
                )["owner"]

            await wait_until(left, 20, 0.25)
            for c in (owner, alice, bob, carol):
                await c.shutdown()

        run(main(), 240)


def test_member_who_leaves_is_gone_for_everyone(tmp_path):
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            cid, people = await community_of(tmp_path, meta, node, "Alice", "Bob")
            (owner, _), (alice, _), (bob, _) = people
            for c in (alice, bob):
                await c.dms.add_contact(cid, await key_of(c, cid, "Olive"))
            gid = (
                await owner.dms.create_group(
                    cid, [await key_of(owner, cid, "Alice"), await key_of(owner, cid, "Bob")], "Plans"
                )
            )["id"]
            await poll(alice, bob)
            await alice.dms.accept(gid)
            await bob.dms.accept(gid)

            async def everyone_in():
                await poll(owner, alice, bob)
                return all(len(c.dms.info(gid)["members"]) == 3 for c in (owner, alice, bob))

            await wait_until(everyone_in, 20, 0.25)
            # Bob (not the owner) leaves: the owner and Alice see him go.
            await bob.dms.leave(gid)

            async def bob_gone():
                await poll(owner, alice)
                return all(
                    sorted(m["name"] for m in c.dms.info(gid)["members"]) == ["Alice", "Olive"] for c in (owner, alice)
                )

            await wait_until(bob_gone, 20, 0.25)
            await owner.dms.send(gid, "after bob")
            await until_text(alice, gid, "after bob", owner)
            for c in (owner, alice, bob):
                await c.shutdown()

        run(main(), 180)


def test_member_who_leaves_during_an_owner_change_is_still_gone(tmp_path):
    """Bob leaves at the same moment the owner renames the group: both
    changes extend the same state, the owner's wins, and Bob (who dropped
    the group when he left) can't sign his leaving again. The owner's client
    removes him on top of the rename, so nobody keeps him listed."""
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            cid, people = await community_of(tmp_path, meta, node, "Alice", "Bob")
            (owner, _), (alice, _), (bob, _) = people
            for c in (alice, bob):
                await c.dms.add_contact(cid, await key_of(c, cid, "Olive"))
            gid = (
                await owner.dms.create_group(
                    cid, [await key_of(owner, cid, "Alice"), await key_of(owner, cid, "Bob")], "Plans"
                )
            )["id"]
            await poll(alice, bob)
            await alice.dms.accept(gid)
            await bob.dms.accept(gid)

            async def everyone_in():
                await poll(owner, alice, bob)
                return all(len(c.dms.info(gid)["members"]) == 3 for c in (owner, alice, bob))

            await wait_until(everyone_in, 20, 0.25)
            seq = owner.dms._group(gid).state.seq
            # Both sign a change to the same state before seeing the other's.
            await asyncio.gather(owner.dms.rename(gid, "Renamed"), bob.dms.leave(gid))

            async def settled():
                await poll(owner, alice)
                return all(
                    c.dms.info(gid)["name"] == "Renamed"
                    and sorted(m["name"] for m in c.dms.info(gid)["members"]) == ["Alice", "Olive"]
                    for c in (owner, alice)
                )

            await wait_until(settled, 20, 0.25)
            # The rename won seq + 1, and the removal came on top of it.
            chain = owner.dms._group(gid).chain
            assert groups.open_state(chain[seq]).name == "Renamed"
            assert chain[-1].signer == owner.dms._group(gid).state.owner
            assert owner.dms._group(gid).chain == alice.dms._group(gid).chain
            for c in (owner, alice, bob):
                await c.shutdown()

        run(main(), 180)


def test_group_size_limit_and_policy(tmp_path):
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            cid, people = await community_of(tmp_path, meta, node, "Alice")
            (owner, _), (alice, _) = people
            # More than the group limit at creation is refused before anything is sent.
            fake = ["A" * 43] * groups.MAX_MEMBERS
            with pytest.raises(UserError, match="at most"):
                await owner.dms.create_group(cid, fake, "too big")
            # A group invitation from someone the DM policy doesn't allow is dropped.
            gid = (await owner.dms.create_group(cid, [await key_of(owner, cid, "Alice")], "Uninvited"))["id"]
            await poll(alice)
            assert gid not in [c["id"] for c in alice.dms.list()]
            # Under "members of shared communities", it arrives as an invitation.
            await alice.set_setting("dm_policy", "members")
            gid2 = (await owner.dms.create_group(cid, [await key_of(owner, cid, "Alice")], "Invited"))["id"]
            await poll(alice)
            assert [c["status"] for c in alice.dms.list() if c["id"] == gid2] == ["invited"]
            # And a direct DM from a shared-community member opens without a request.
            d = (await owner.dms.open_with_member(cid, await key_of(owner, cid, "Alice")))["id"]
            await poll(alice)
            assert [c["status"] for c in alice.dms.list() if c["id"] == d] == ["active"]

            # ...and the sender sees the acceptance: messages flow both ways.
            async def accepted():
                await poll(owner)
                return [m["name"] for m in owner.dms.info(d)["members"]] == ["Olive", "Alice"]

            await wait_until(accepted, 20, 0.25)
            await owner.dms.send(d, "no request needed")
            await until_text(alice, d, "no request needed", owner)
            for c in (owner, alice):
                await c.shutdown()

        run(main(), 180)


def test_client_logs_carry_no_content_or_keys(tmp_path, caplog):
    """Message content, file names, keys, join keys and passphrases never reach the log."""
    import logging

    caplog.set_level(logging.DEBUG)
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            cid, [(olive, _), (alice, _)] = await community_of(tmp_path, meta, node, "Alice")
            gid = (await alice.dms.open_with_member(cid, await key_of(alice, cid, "Olive")))["id"]
            await poll(olive)
            await olive.dms.accept(gid)
            secret_file = tmp_path / "quarterly-budget-zebra.txt"
            secret_file.write_bytes(b"contents nobody may log")
            await alice.dms.send(gid, "the walrus sings at dawn", files=[str(secret_file)])
            await until_text(olive, gid, "the walrus sings at dawn", alice)
            gen = await general(olive, cid)
            await olive.send(cid, gen, "channel words about a narwhal")
            seeds = [i["seed"] for c in (olive, alice) for i in c.vault.data["identities"].values() if i.get("seed")]
            for c in (olive, alice):
                await c.shutdown()
            return seeds

        seeds = run(main(), 120)
    text = caplog.text
    assert len(caplog.records) > 20, "logging wasn't captured"
    for needle in (
        "walrus sings",
        "quarterly-budget-zebra",
        "contents nobody may log",
        "narwhal",
        conftest.PASS,
        *seeds,
    ):
        assert needle not in text, f"logged: {needle[:12]}…"


def test_identity_key_change_warning(tmp_path):
    """A contact whose DM identity changes is flagged, unverified, until the
    new key is accepted."""
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            cid, [(olive, _), (alice, _)] = await community_of(tmp_path, meta, node, "Alice")
            alice_member = await key_of(olive, cid, "Alice")
            old = (await olive.dms.add_contact(cid, alice_member))["id"]
            await olive.dms.set_verified(old, True)
            assert olive.dms.contacts()[0]["verified"]
            # Alice starts over with a new DM identity in this community (as
            # after reinstalling), and publishes a new card.
            alice.vault.data["communities"][cid].pop("dm_identity")
            await alice.dms.set_contact_card(cid, True)
            # Olive meets her again: the card's identity differs from the pinned one.
            await olive.dms._fetch_card(cid, keys.decode(alice_member), claim=False)
            c = olive.dms.contacts()[0]
            assert c["key_changed"] and not c["verified"]
            assert olive.dms.safety(old)["key_changed"]
            new = (await olive.dms.accept_key_change(old))["id"]
            assert new != old
            c = olive.dms.contacts()[0]
            assert c["id"] == new and not c["key_changed"] and not c["verified"]
            with pytest.raises(UserError):
                await olive.dms.accept_key_change(new)  # nothing left to acknowledge
            for x in (olive, alice):
                await x.shutdown()

        run(main(), 120)


def test_blocking(tmp_path):
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            cid, [(olive, _), (alice, _)] = await community_of(tmp_path, meta, node, "Alice")
            await olive.set_setting("dm_policy", "members")
            await alice.set_setting("dm_policy", "members")
            gid = (await alice.dms.open_with_member(cid, await key_of(alice, cid, "Olive")))["id"]
            await alice.dms.send(gid, "before blocking")
            await until_text(olive, gid, "before blocking", alice)
            alice_id = next(m["id"] for m in olive.dms.info(gid)["members"] if not m["me"])
            # A nickname doesn't make her a contact; removing her as a
            # contact keeps it, and so does blocking her.
            await olive.dms.rename_contact(alice_id, "Al")
            assert olive.dms.contacts() == [] and olive.dms.list()[0]["name"] == "Al"
            await olive.dms.add_contact(cid, await key_of(olive, cid, "Alice"))
            await olive.dms.remove_contact(alice_id)
            await olive.dms.rename_contact(alice_id, "Ally")
            assert olive.dms.contacts() == [] and olive.dms.list()[0]["name"] == "Ally"
            await olive.dms.set_blocked(alice_id, True)
            assert olive.dms.list()[0]["blocked"] and olive.dms.list()[0]["name"] == "Ally"
            assert "before blocking" in await texts(olive, gid)
            with pytest.raises(UserError, match="blocked"):
                await olive.dms.send(gid, "to a blocked person")
            await alice.dms.send(gid, "after blocking")
            for _ in range(10):
                await poll(olive, alice)
            assert "after blocking" not in await texts(olive, gid)
            # Their group invitations are dropped too.
            g2 = (await alice.dms.create_group(cid, [await key_of(alice, cid, "Olive")], "From a blocked person"))["id"]
            for _ in range(5):
                await poll(olive)
            assert g2 not in [c["id"] for c in olive.dms.list()]
            # Unblocking: new messages show again.
            await olive.dms.set_blocked(alice_id, False)
            await alice.dms.send(gid, "after unblocking")
            await until_text(olive, gid, "after unblocking", alice)
            for x in (olive, alice):
                await x.shutdown()

        run(main(), 120)


def test_group_icon_inbox_and_timestamps(tmp_path):
    from test_e2e_dm_files import png

    from openarcanum.wire import dm_pb2

    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            cid, people = await community_of(tmp_path, meta, node, "Alice", "Bob")
            (olive, _), (alice, _), (bob, _) = people
            for c in (alice, bob):
                await c.dms.add_contact(cid, await key_of(c, cid, "Olive"))
            gid = (await olive.dms.create_group(cid, [await key_of(olive, cid, n) for n in ("Alice", "Bob")], "G"))[
                "id"
            ]
            for c in (alice, bob):
                await poll(c)
                await c.dms.accept(gid)
            await poll(olive, alice, bob)
            # Only the owner sets the icon; everyone sees it.
            pic = tmp_path / "icon.png"
            pic.write_bytes(png(400, 400))
            with pytest.raises(UserError):
                await alice.dms.set_icon(gid, str(pic))
            await olive.dms.set_icon(gid, str(pic))

            async def iconed():
                await poll(alice)
                return alice.dms.info(gid)["icon"].startswith("data:image/jpeg;base64,")

            await wait_until(iconed, 20, 0.25)
            assert alice.dms.list()[0]["icon"]
            await olive.dms.set_icon(gid, "")

            async def cleared():
                await poll(alice)
                return not alice.dms.info(gid)["icon"]

            await wait_until(cleared, 20, 0.25)
            # Alice can't point Bob's device at her own mailbox.
            bob_dev = next(d for d in bob.dms._devices_of(bob.dms.d["groups"][gid]["me"]))
            bob_curve = bob_dev.curve25519
            fake = dm_pb2.DmEvent(inboxes=dm_pb2.DmInboxes(curve25519=bob_curve, inbox=b"\x07" * 32))
            orig = alice.dms._handle_event

            async def skip_own(g, ev, mine):
                return None if ev.WhichOneof("body") == "inboxes" else await orig(g, ev, mine)

            alice.dms._handle_event = skip_own
            await alice.dms.send_event(gid, fake)
            for _ in range(5):
                await poll(olive)
            assert olive.dms.d["groups"][gid].get("inboxes", {}).get(b64(bob_curve)) != b64(b"\x07" * 32)
            # A message dated in the future doesn't hide later unread messages.
            future = dm_pb2.DmEvent(
                at=int(time.time() * 1000) + 10 * 365 * 24 * 3600 * 1000, text=dm_pb2.DmText(text="from the future")
            )
            await bob.dms.send_event(gid, future)
            await until_text(olive, gid, "from the future", bob)
            await olive.dms.mark_read(gid)
            await bob.dms.send(gid, "after that")
            await until_text(olive, gid, "after that", bob)
            assert next(c for c in olive.dms.list() if c["id"] == gid)["unread"] == 1
            for c in (olive, alice, bob):
                await c.shutdown()

        run(main(), 180)


def test_key_change_flagged_for_non_contacts(tmp_path):
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            cid, [(olive, _), (alice, _)] = await community_of(tmp_path, meta, node, "Alice")
            await olive.set_setting("dm_policy", "members")
            gid = (await alice.dms.open_with_member(cid, await key_of(alice, cid, "Olive")))["id"]
            await poll(olive)
            alice_member = await key_of(olive, cid, "Alice")
            await olive.dms._fetch_card(cid, keys.decode(alice_member), claim=False)
            old = next(m["id"] for m in olive.dms.info(gid)["members"] if not m["me"])
            assert old not in olive.dms.d["contacts"]
            alice.vault.data["communities"][cid].pop("dm_identity")
            await alice.dms.set_contact_card(cid, True)
            await olive.dms._fetch_card(cid, keys.decode(alice_member), claim=False)
            assert next(m for m in olive.dms.info(gid)["members"] if m["id"] == old)["key_changed"]
            await olive.dms.accept_key_change(old)
            assert not next(m for m in olive.dms.info(gid)["members"] if m["id"] == old)["key_changed"]
            for c in (olive, alice):
                await c.shutdown()

        run(main(), 120)


def test_missed_state_change_is_recovered(tmp_path):
    """A member whose copy of one group change was lost catches up from the
    next change, which carries the states before it."""
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            cid, people = await community_of(tmp_path, meta, node, "Alice")
            (olive, _), (alice, _) = people
            await alice.dms.add_contact(cid, await key_of(alice, cid, "Olive"))
            gid = (await olive.dms.create_group(cid, [await key_of(olive, cid, "Alice")], "Start"))["id"]
            await poll(alice)
            await alice.dms.accept(gid)
            await poll(olive, alice)
            # Alice's relay connection is gone and her mailbox copy is lost.
            for r in list(alice.dms.relays.values()):
                await r.close()
            await olive.dms.rename(gid, "First rename")
            for token in [unb64(e["inbox_token"]) for e in alice.dms.d["ids"].values()] + [
                unb64(g["inbox_token"]) for g in alice.dms.d["groups"].values()
            ]:
                for base, it in await alice.meta.fetch(token):
                    await alice.meta.delete(base, token, [it.id])
            await olive.dms.rename(gid, "Second rename")

            async def caught_up():
                await poll(alice)
                return alice.dms.info(gid)["name"] == "Second rename"

            await wait_until(caught_up, 20, 0.25)
            for c in (olive, alice):
                await c.shutdown()

        run(main(), 120)


def test_new_members_key_arriving_before_the_change_that_adds_them(tmp_path):
    """Olive invites Bob into a group
    with Alice; Bob joins and writes at once. Alice reads Bob's room key
    (Olm) before Olive's change that names Bob (Megolm), so she can't check
    it yet; it's held until the group changes, not dropped, and she gets
    Bob's message."""
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            cid, people = await community_of(tmp_path, meta, node, "Alice", "Bob")
            (owner, _), (alice, _), (bob, _) = people
            for c in (alice, bob):
                await c.dms.add_contact(cid, await key_of(c, cid, "Olive"))
            gid = (await owner.dms.create_group(cid, [await key_of(owner, cid, "Alice")], "Crew"))["id"]
            await poll(alice)
            await alice.dms.accept(gid)
            await poll(owner, alice)
            await owner.dms.invite(gid, cid, [await key_of(owner, cid, "Bob")])
            await poll(bob)
            await bob.dms.accept(gid)
            await bob.dms.send(gid, "bob here")
            await until_text(owner, gid, "bob here", bob, alice)
            await until_text(alice, gid, "bob here", bob, owner)
            assert sorted(m["name"] for m in alice.dms.info(gid)["members"]) == ["Alice", "Bob", "Olive"]

            # Carol joins and writes at once while Olive renames the group
            # without having seen her acceptance: the two changes race, and
            # where the rename wins Carol is still invited until her client
            # signs its acceptance again. Her message waits for that instead
            # of being dropped.
            carol, _ = await join_as(tmp_path, meta, owner, cid, "Carol")
            await carol.dms.add_contact(cid, await key_of(carol, cid, "Olive"))
            await owner.dms.invite(gid, cid, [await key_of(owner, cid, "Carol")])
            await poll(carol)
            await carol.dms.accept(gid)
            await carol.dms.send(gid, "carol here")
            await owner.dms.rename(gid, "Crew, renamed")
            for c in (alice, bob, owner):
                await until_text(c, gid, "carol here", carol, alice, bob, owner)
            for c in (owner, alice, bob, carol):
                assert sorted(m["name"] for m in c.dms.info(gid)["members"]) == ["Alice", "Bob", "Carol", "Olive"]
            for c in (owner, alice, bob, carol):
                await c.shutdown()

        run(main(), 180)
