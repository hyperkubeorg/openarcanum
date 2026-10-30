"""User statuses, end to end (statuses.py; docs/protocol.md §4.11, §9.8):
real relay, maintainer node and community server binaries, several clients.

What the servers store and forward is checked as well as what clients show:
the community server, the relay and the maintainer node only ever hold
ciphertext, a member who left can't read the next status, un-sharing makes
members stop seeing it, Invisible stops the presence broadcast, and Do not
disturb stops notifications."""

import asyncio
import os
import time
from pathlib import Path

import conftest
import pytest
import vodozemac as vz
from conftest import PASS, network, new_core, run, wait_until
from test_e2e_community import WIZARD, general

from openarcanum import keys, statuses
from openarcanum.errors import UserError
from openarcanum.olm import OlmError, OlmStore
from openarcanum.wire import dm_pb2 as dm

TEXT = "Deep in the quokka migration report"
CONTACT_TEXT = "Walking the pangolin, back soon"


async def joined(tmp_path: Path, meta: str, owner, cid: str, name: str):
    core, ev = await new_core(tmp_path / name.lower(), maintainers=[meta])
    inv = await owner.admin(cid, "create_invite", {})
    await core.join({"invite": inv["invite"]}, "new", name)
    await general(core, cid)
    return core, ev


async def member_key(core, cid: str) -> str:
    return keys.encode(core.conns[cid].state.self.key)


def files_under(*dirs: Path) -> list[Path]:
    return [p for d in dirs for p in Path(d).rglob("*") if p.is_file()]


def assert_absent(needle: str, paths: list[Path]) -> None:
    raw = needle.encode()
    for p in paths:
        assert raw not in p.read_bytes(), f"{needle!r} found in {p}"


async def sees(core, cid: str, of: str, pred, timeout: float = 20.0):
    def check():
        r = core.statuses.received.get(cid, {}).get(of)
        return pred(r)

    await wait_until(check, timeout=timeout)


def test_community_status_is_end_to_end_encrypted_and_follows_its_audience(tmp_path):
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            owner, _ = await new_core(tmp_path / "owner", "owner-desktop", maintainers=[meta])
            cid = (await owner.claim(node.claim_code, WIZARD, "Olive"))["community"]
            await general(owner, cid)
            alice, _ = await joined(tmp_path, meta, owner, cid, "Alice")
            bob, _ = await joined(tmp_path, meta, owner, cid, "Bob")
            a, b = await member_key(alice, cid), await member_key(bob, cid)
            # Every member's devices registered to receive statuses (no
            # contact card needed).
            for core, other in ((alice, b), (bob, a)):
                await wait_until(lambda core=core, other=other: other in core.statuses.devices.get(cid, {}), 20)

            # The default for a community: the state, not the custom text.
            await alice.statuses.set_status("dnd", TEXT, "🎧", "1h")
            await sees(bob, cid, a, lambda r: r is not None and r["state"] == "dnd")
            assert bob.statuses.received[cid][a]["text"] == ""
            assert bob.statuses.received[cid][a]["expires_at"] > time.time() * 1000

            # Shared in full: the text too, for every member.
            await alice.set_status_community(cid, "full")
            await sees(bob, cid, a, lambda r: r is not None and r["text"] == TEXT and r["emoji"] == "🎧")
            await sees(owner, cid, a, lambda r: r is not None and r["text"] == TEXT)

            # What the server forwards is ciphertext: the blob Bob was given
            # doesn't hold the text, and neither does anything the community
            # server or the maintainer node keeps.
            blob = bob.statuses.blobs[cid][a]
            assert TEXT.encode() not in blob and b"quokka" not in blob
            assert_absent("quokka", files_under(node.data, conftest.META_DIRS[meta]))

            # Un-shared: members stop seeing it.
            await alice.set_status_community(cid, "off")
            await sees(bob, cid, a, lambda r: r is None)
            await sees(owner, cid, a, lambda r: r is None)

            # Bob leaves; Alice shares again and posts. A new session key:
            # what Bob's device still holds can't read it.
            old_sessions = set(bob.dms.olm.inbound)
            await bob.leave(cid)
            await wait_until(lambda: b not in owner.statuses.devices.get(cid, {}), 20)
            await wait_until(lambda: b not in alice.statuses.devices.get(cid, {}), 20)
            await alice.set_status_community(cid, "full")
            await alice.statuses.set_status("online", "after bob left", "", "never")
            await sees(owner, cid, a, lambda r: r is not None and r["text"] == "after bob left")
            new = owner.statuses.blobs[cid][a]
            env = dm.Envelope.FromString(new[len(statuses.MAGIC) :]).megolm
            assert env.session_id not in old_sessions
            with pytest.raises(OlmError):
                bob.dms.olm.decrypt_megolm(env.session_id, env.sender_curve25519, env.ciphertext)
            for c in (owner, alice, bob):
                await c.shutdown()

        run(main(), 180)


def test_invisible_hides_presence_and_dnd_stops_notifications(tmp_path):
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            owner, owner_ev = await new_core(tmp_path / "owner", "owner-desktop", maintainers=[meta])
            cid = (await owner.claim(node.claim_code, WIZARD, "Olive"))["community"]
            ch = await general(owner, cid)
            alice, aev = await joined(tmp_path, meta, owner, cid, "Alice")
            a = await member_key(alice, cid)

            async def online_for_owner() -> bool:
                return next(m["online"] for m in await owner.members(cid) if m["key"] == a)

            await wait_until(online_for_owner, 15)
            # Do not disturb: no notification, but the unread count grows.
            await alice.statuses.set_status("dnd")
            before = alice.community_summary(cid)["unread"]
            await owner.send(cid, ch, "a message during do not disturb")
            await aev.wait(lambda n, p: n == "message" and "during do not disturb" in p["message"]["text"])
            assert alice.community_summary(cid)["unread"] > before
            await asyncio.sleep(0.5)
            assert not [n for n in aev.of("notify") if n.get("community") == cid]
            await alice.statuses.set_status("online")
            await owner.send(cid, ch, "a message after")
            await wait_until(lambda: [n for n in aev.of("notify") if n.get("community") == cid], 15)
            # Invisible: the server stops announcing her; she still sees
            # herself online, and her connection works as before.
            await alice.statuses.set_status("invisible")
            await wait_until(lambda: any(p["member"] == a and not p["online"] for p in owner_ev.of("presence")), 15)
            assert not await online_for_owner()
            assert next(m["online"] for m in await alice.members(cid) if m["key"] == a)
            # Reconnecting keeps her hidden.
            await alice.conns[cid].stop()
            alice._start(cid)
            await general(alice, cid)
            await asyncio.sleep(1.5)
            assert not await online_for_owner()
            await alice.statuses.set_status("online")
            await wait_until(online_for_owner, 15)

            for c in (owner, alice):
                await c.shutdown()

        run(main(), 180)


def test_contacts_status_through_mailboxes_and_removal_rotates(tmp_path):
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            owner, _ = await new_core(tmp_path / "owner", "owner-desktop", maintainers=[meta])
            cid = (await owner.claim(node.claim_code, WIZARD, "Olive"))["community"]
            await general(owner, cid)
            alice, _ = await joined(tmp_path, meta, owner, cid, "Alice")
            bob, _ = await joined(tmp_path, meta, owner, cid, "Bob")
            # Contacts need contact cards; nobody shares a community status
            # here, so only the contacts path can carry the text.
            for c in (owner, alice, bob):
                await c.set_status_community(cid, "off")
                await c.dms.set_contact_card(cid, True)
            a, o, b = await member_key(alice, cid), await member_key(owner, cid), await member_key(bob, cid)
            alice_ident = (await owner.dms.add_contact(cid, a))["id"]
            assert (await bob.dms.add_contact(cid, a))["id"] == alice_ident
            await alice.dms.add_contact(cid, o)
            await alice.dms.add_contact(cid, b)

            await alice.statuses.set_status("idle", CONTACT_TEXT, "", "4h")

            async def seen_by(core, pred):
                async def check():
                    await core.dms.poll_once()
                    return pred(core.statuses.d.get("recv", {}).get(alice_ident))

                await wait_until(check, 30, 0.25)

            for core in (owner, bob):
                await seen_by(core, lambda r: r is not None and r["text"] == CONTACT_TEXT and r["state"] == "idle")
            # Fresh for a while: contacts have no presence to go by.
            assert owner.statuses.d["recv"][alice_ident]["fresh_until"] > time.time() * 1000
            # Unless a community we share says otherwise: Alice is online
            # there, and Bob's view has her status; once that community sees
            # her offline, so does Bob, fresh contacts status or not.
            await bob.members(cid)
            assert bob.statuses.view()["people"][alice_ident]["state"] == "idle"
            alice_online = next(m for m in bob._members[cid] if m["key"] == a)
            alice_online["online"] = False
            assert alice_ident not in bob.statuses.view()["people"]
            alice_online["online"] = True
            assert bob.statuses.view()["people"][alice_ident]["state"] == "idle"
            # The maintainer node carried only ciphertext.
            assert_absent("pangolin", files_under(conftest.META_DIRS[meta]))

            # Alice removes Olive from her contacts: Olive's view is cleared,
            # and the next status uses a new key that Bob gets and Olive doesn't.
            pub = alice.vault.data["communities"][cid]["dm_identity"]
            gid = keys.decode(alice.statuses.d["aud"][f"k:{pub}"]["id"])

            def session_id() -> str:
                info = alice.dms.olm.outbound_info(f"dm:{pub}", gid)
                return vz.GroupSession.from_pickle(info["pickle"], alice.dms.olm.pickle_key).session_id

            old = session_id()
            olive_ident = next(c["id"] for c in alice.dms.contacts() if c["member"] == o)
            await alice.remove_contact(olive_ident)
            await seen_by(owner, lambda r: r is None)
            await alice.statuses.set_status("online", "only for my remaining contacts", "", "never")
            await seen_by(bob, lambda r: r is not None and r["text"] == "only for my remaining contacts")
            new = session_id()
            assert new != old
            assert new in bob.dms.olm.inbound and new not in owner.dms.olm.inbound
            assert owner.statuses.d.get("recv", {}).get(alice_ident) is None
            for c in (owner, alice, bob):
                await c.shutdown()

        run(main(), 180)


def test_status_expiry_idle_and_validation(tmp_path):
    async def main():
        core, _ev = await new_core(tmp_path / "solo")
        s = core.statuses
        await s.set_status("dnd", "  In a   meeting ", "📅", "30m")
        assert s.mine()["text"] == "In a meeting" and s.mine()["state"] == "dnd"
        assert s.dnd()
        # Expired: back to online with no text.
        await s.tick(now=time.time() + 31 * 60)
        assert s.mine()["state"] == "online" and s.mine()["text"] == "" and not s.dnd()
        # Idle after the configured minutes without input, and back on input.
        await core.set_setting("status_idle_minutes", 1)
        core.last_activity = time.monotonic() - 120
        await s.tick()
        assert s.effective() == "idle"
        core.activity()
        assert s.effective() == "online"
        # Idle: the status says since when (the last input, to the minute),
        # and never when invisible or for an audience that gets nothing.
        core.last_activity = time.monotonic() - 120
        await s.tick()
        assert s.effective() == "idle"

        def sent(level: str, state: str = "") -> dict:
            ev = dm.DmEvent.FromString(s._plaintext(b"s" * 32, b"d" * 32, b"a" * 16, level, state))
            return statuses.received_view(ev.status, ev.at)

        seen = sent("state")
        expect = (time.time() - 120) // 60 * 60 * 1000
        assert seen["shared"] and abs(seen["idle_since"] - expect) <= 60_000
        assert seen["idle_since"] % 60_000 == 0
        assert sent("full", "invisible")["idle_since"] == 0
        off = sent("off")
        assert off["idle_since"] == 0 and not off["shared"]
        # A time ahead of the status itself is dropped.
        ahead = dm.DmStatus(state=dm.STATUS_STATE_ONLINE, idle_since=int(time.time() * 1000) + 3_600_000)
        assert statuses.received_view(ahead, int(time.time() * 1000))["idle_since"] == 0
        core.activity()
        assert sent("state")["idle_since"] == 0
        # In a call, no input is still being there: not idle, and no
        # auto-lock (which would end the call); both count from its end.
        await core.set_setting("status_idle_minutes", 1)
        await core.set_setting("auto_lock_minutes", 1)
        core.last_activity = time.monotonic() - 600
        core.voice.call = object()
        await s.tick()
        assert s.effective() == "online"
        await core._idle_check()
        assert core.unlocked and time.monotonic() - core.last_activity < 5
        core.voice.call = None
        core.last_activity = time.monotonic() - 600
        await s.tick()
        assert s.effective() == "idle"
        await core._idle_check()
        assert not core.unlocked
        await core.unlock(PASS)
        await core.set_setting("auto_lock_minutes", 0)
        # 0 = never idle.
        await core.set_setting("status_idle_minutes", 0)
        core.last_activity = time.monotonic() - 10_000
        await s.tick()
        assert s.effective() == "online"
        # Bounds.
        with pytest.raises(UserError):
            await s.set_status("busy")
        with pytest.raises(UserError):
            await s.set_status("online", "x" * 129)
        with pytest.raises(UserError):
            await s.set_status("online", "ok", "ab")
        with pytest.raises(UserError):
            await s.set_status("online", "bad‮text")
        with pytest.raises(UserError):
            await s.set_status("online", "ok", "", "tomorrow")
        # "today" ends at the next local midnight.
        end = statuses.expiry_for("today", now=time.time())
        assert 0 < end - time.time() * 1000 <= 24 * 3600 * 1000
        await core.shutdown()

    run(main(), 60)


def test_status_text_is_never_logged(tmp_path, caplog):
    caplog.set_level("DEBUG")

    async def main():
        core, _ = await new_core(tmp_path / "solo")
        await core.statuses.set_status("dnd", "secret-wombat-plans", "", "never")
        await core.shutdown()

    run(main(), 30)
    assert "secret-wombat-plans" not in caplog.text


def test_exported_key_reads_the_newest_status_and_nothing_before(tmp_path):
    """A device that joins an audience gets the session exported at its
    newest message (OlmStore.exported_key): the current status, not older."""
    sender = OlmStore(tmp_path / "a.bin", os.urandom(32), os.urandom(32))
    late = OlmStore(tmp_path / "b.bin", os.urandom(32), os.urandom(32))
    gid = os.urandom(16)
    sender.rotate("status:c", gid)
    msgs = [sender.encrypt_megolm("status:c", gid, f"status {i}".encode()) for i in range(3)]
    sid, key = sender.exported_key("status:c", gid)
    curve = sender.curve("status:c")
    assert late.add_inbound_exported("status:c", sid, key, gid, curve, sender.ed("status:c"))
    assert late.decrypt_megolm(sid, curve, msgs[2][1]).plaintext == b"status 2"
    with pytest.raises(OlmError):
        late.decrypt_megolm(sid, curve, msgs[1][1])
    # The same session claimed by another sender is kept apart and doesn't
    # take the session from its sender; the sender can't move it to another group.
    other = os.urandom(32)
    assert late.add_inbound_exported("status:c", sid, key, gid, other, sender.ed("status:c"))
    assert late.decrypt_megolm(sid, curve, msgs[2][1]).sender_curve == curve
    assert late.decrypt_megolm(sid, other, msgs[2][1]).sender_curve == other
    with pytest.raises(OlmError):
        late.add_inbound_exported("status:c", sid, key, os.urandom(16), curve, sender.ed("status:c"))


def test_olm_batch_saves_once_at_the_end(tmp_path):
    store = OlmStore(tmp_path / "o.bin", os.urandom(32), os.urandom(32))
    store.account("status:c")
    before = (tmp_path / "o.bin").read_bytes()
    with store.batch():
        store.rotate("status:c", os.urandom(16))
        store.rotate("status:c", os.urandom(16))
        assert (tmp_path / "o.bin").read_bytes() == before
    after = OlmStore(tmp_path / "o.bin", store.seal_key, store.pickle_key)
    assert len(after.outbound["status:c"]) == 2


def test_a_contact_added_after_the_status_gets_it_on_the_next_tick(tmp_path):
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            owner, _ = await new_core(tmp_path / "owner", "owner-desktop", maintainers=[meta])
            cid = (await owner.claim(node.claim_code, WIZARD, "Olive"))["community"]
            await general(owner, cid)
            alice, _ = await joined(tmp_path, meta, owner, cid, "Alice")
            for c in (owner, alice):
                await c.set_status_community(cid, "off")
                await c.dms.set_contact_card(cid, True)
            # Alice's status is posted (to nobody yet: no contacts).
            await alice.statuses.set_status("dnd", CONTACT_TEXT, "", "4h")
            a, o = await member_key(alice, cid), await member_key(owner, cid)
            alice_ident = (await owner.dms.add_contact(cid, a))["id"]
            await alice.dms.add_contact(cid, o)
            # Not the 30-minute refresh: the new contact's device is noticed.
            assert time.time() - alice.statuses._last_refresh < statuses.REFRESH_SECONDS
            await alice.statuses.tick()

            async def seen():
                await owner.dms.poll_once()
                r = owner.statuses.d.get("recv", {}).get(alice_ident)
                return r is not None and r["state"] == "dnd" and r["text"] == CONTACT_TEXT

            await wait_until(seen, 30, 0.25)
            # Once given the key, later ticks don't post again.
            assert not alice.statuses._contacts_grew()
            for c in (owner, alice):
                await c.shutdown()

        run(main(), 180)
