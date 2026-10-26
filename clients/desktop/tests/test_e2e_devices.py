"""Linked devices end to end (docs/protocol.md §10): relay (registered with the
maintainer node), maintainer node, community servers and several clients."""

import asyncio
import json
from pathlib import Path

import pytest
from conftest import PASS, Events, network, new_core, run, wait_relay_registered, wait_until
from test_e2e_community import WIZARD, general, online

from openarcanum import devices, keychain, keys
from openarcanum.core import Core, UserError
from openarcanum.devices import LinkSession
from openarcanum.paths import Paths
from openarcanum.wire import community_pb2 as cp


async def dm_pair(tmp_path, meta, node):
    owner, oev = await new_core(tmp_path / "owner", "owner-desktop", maintainers=[meta])
    res = await owner.claim(node.claim_code, WIZARD, "Olive")
    cid = res["community"]
    await general(owner, cid)
    alice, aev = await new_core(tmp_path / "alice", maintainers=[meta])
    await alice.join({"invite": res["invite"]}, "new", "Alice")
    await general(alice, cid)
    await owner.dms.set_contact_card(cid, True)
    await alice.dms.set_contact_card(cid, True)
    return owner, oev, alice, aev, cid, res


async def settle(pred, *cores, timeout=20):
    """Poll every core's mailboxes until pred() holds."""

    async def check():
        for c in cores:
            if c.vault is not None:
                await c.dms.poll_once()
        return pred()

    await wait_until(check, timeout, 0.2)


def texts(core, gid):
    return [m["text"] for m in core.dms.history(gid) if not m.get("system")]


async def start_dm(owner, alice, cid):
    members = await alice.members(cid)
    olive = next(m for m in members if m["display_name"] == "Olive")
    conv = (await alice.dms.open_with_member(cid, olive["key"]))["id"]
    await owner.dms.poll_once()
    return conv


class Topic:
    """One relay topic without the relay: every session on it hears what the
    others publish (not its own, as on a relay)."""

    def __init__(self):
        self.subs: list[tuple[object, object]] = []

    def relay(self):
        topic = self

        class R:
            async def subscribe(self, _t, cb):
                topic.subs.append((self, cb))

            async def publish(self, t, data):
                for owner, cb in list(topic.subs):
                    if owner is not self:
                        cb(t, data)

            async def close(self):
                pass

        return R()


def test_link_handshake_ignores_third_parties_on_the_topic():
    """Anyone who learns a nameplate's topic can publish on it. A link in
    progress skips what can't be the other device's: an abort before the
    keys exist, or after them unless it's sealed under the session key, a
    PAKE message that isn't even shaped like one, a confirm that doesn't
    open. The code still gets one guess: the first well-formed PAKE message
    is the one used, and a plain abort while waiting for the confirmation is
    taken as the mismatch it reports."""
    code = "42-purple-sausage-anchor"

    async def pair(topic: Topic) -> tuple[LinkSession, LinkSession, object]:
        existing, new = LinkSession(topic.relay(), b"t"), LinkSession(topic.relay(), b"t")
        await existing.open()
        await new.open()
        return existing, new, topic.relay()

    async def noise(third, *msgs: dict) -> None:
        for m in msgs:
            await third.publish(b"t", json.dumps(m).encode())

    async def main():
        existing, new, third = await pair(Topic())
        await noise(
            third,
            {"t": "abort", "why": "go away"},
            {"t": "pake"},
            {"t": "pake", "m": 7},
            {"t": "pake", "m": "not base64!"},
            {"t": "pake", "m": keys.encode(b"S" + b"x" * 7)},
            {"t": "confirm", "box": "zz"},
            {"t": "confirm", "box": 1},
            {"t": "hello", "box": "zz"},
        )
        new_side = asyncio.ensure_future(devices.link_handshake_new(new, code, "phone"))
        peer = await devices.link_handshake_existing(existing, code)
        await new_side
        assert peer["name"] == "phone" and new.k_in and existing.k_in
        # With the keys made, only a sealed abort counts.
        await noise(third, {"t": "abort", "why": "cancelled"}, {"t": "abort", "box": "zz"}, {"t": "abort", "box": 3})
        with pytest.raises(TimeoutError):
            await new.recv("transfer", timeout=0.3)
        await existing.abort("Cancelled on the existing device.")
        with pytest.raises(UserError, match="Cancelled on the existing device"):
            await new.recv("transfer", timeout=5)
        # A well-formed PAKE message from someone else, first: that is the
        # one guess, and the link fails on both sides (a plain abort while
        # the new device waits for the confirmation).
        existing, new, third = await pair(Topic())
        await noise(
            third,
            {"t": "pake", "m": keys.encode(devices.SPAKE2_Symmetric(b"0-a-b-c", idSymmetric=devices.LINK_ID).start())},
        )
        new_side = asyncio.ensure_future(devices.link_handshake_new(new, code, "phone"))
        with pytest.raises(UserError, match="didn't match"):
            await devices.link_handshake_existing(existing, code)
        with pytest.raises(UserError, match="didn't match"):
            await new_side

    run(main(), 60)


async def link(existing: Core, new_home: Path, name: str, choices=None, events: Events | None = None):
    ev = events or Events()
    new = Core(Paths.under(new_home), emit=ev)
    # Maintainer lists are per device: the new one is set before linking
    # (the link screen's Change…), and linking uses it.
    await new.set_maintainers(existing.meta.urls)
    started = await new.devices.new_device_start(name)
    info = await existing.devices.existing_enter_code(started["code"])
    assert info["device_name"] == name
    assert "Never enter a code someone sent you" in info["warning"]
    picks = choices or {"communities": [c["id"] for c in info["communities"]], "copy_dms": True}
    approve = asyncio.ensure_future(existing.devices.existing_approve(picks))
    await ev.wait(lambda n, p: n == "link" and p["stage"] in ("received", "failed"), 30)
    assert ev.of("link")[-1]["stage"] == "received", ev.of("link")
    await new.devices.new_device_finish(PASS)
    await approve
    return new, ev


def test_link_second_device(tmp_path):
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            owner, oev, alice, _aev, cid, _res = await dm_pair(tmp_path, meta, node)
            await wait_relay_registered(owner)
            conv = await start_dm(owner, alice, cid)
            await owner.dms.accept(conv)
            await alice.dms.poll_once()
            await alice.dms.send(conv, "history before linking")
            await settle(lambda: "history before linking" in texts(owner, conv), owner, alice)

            laptop, _lev = await link(owner, tmp_path / "laptop", "laptop")
            # A linked device stays signed in, like a new profile.
            laptop_key = keychain.key_file(Paths.under(tmp_path / "laptop"))
            assert laptop.profile_status()["remember"]["stored"] == "file" and laptop_key.exists()
            # In every shared community, without an admin doing anything.
            await online(laptop, cid)
            gen = await general(laptop, cid)
            await laptop.send(cid, gen, "hello from the laptop")
            devs = await laptop.conns[cid].call(list_devices=cp.ListDevices())
            assert len(devs.devices.devices) == 2
            # DM history is copied in the background and is readable there.
            await settle(lambda: laptop.devices.copy_status()["done"], laptop, owner)
            assert texts(laptop, conv) == ["history before linking"]
            # New DMs reach the laptop too: Alice learns its keys from the
            # owner's updated device list and rotates.
            await settle(lambda: True, alice, owner, laptop)
            await alice.dms.send(conv, "after linking")
            await settle(lambda: "after linking" in texts(laptop, conv), laptop, alice, owner)
            # And the laptop can write in the conversation as the same person.
            await laptop.dms.send(conv, "from the laptop")
            await settle(lambda: "from the laptop" in texts(alice, conv), alice, laptop, owner)
            await settle(lambda: "from the laptop" in texts(owner, conv), owner, laptop)
            # Both devices list each other.
            names = sorted(d["name"] for d in await laptop.devices.list())
            assert names == ["laptop", "owner-desktop"]
            await oev.wait(lambda n, p: n == "notify" and p["title"] == "New device linked")

            # Joining on one device shows up on the other, even if it was offline.
            await laptop.shutdown()
            node2 = community("second")
            cid2 = (await owner.claim(node2.claim_code, dict(WIZARD, name="Second"), "Olive"))["community"]
            await general(owner, cid2)
            laptop2 = Core(Paths.under(tmp_path / "laptop"), emit=Events())
            await laptop2.unlock(PASS)
            await laptop2.dms.poll_once()
            await online(laptop2, cid2)
            # Settings sync: latest change wins. The maintainer list is per
            # device (preferences): changed on the owner first, it never
            # reaches the laptop, while the later setting does.
            await owner.set_maintainers([meta, "http://127.0.0.1:1"])
            await owner.set_setting("notify_mode", "mentions")
            await laptop2.dms.poll_once()
            await wait_until(lambda: laptop2.settings["notify_mode"] == "mentions", 15, 0.2)
            assert laptop2.prefs.values["maintainers"] == [meta] and laptop2.meta.urls == [meta]
            assert "maintainers" not in laptop2.settings
            await owner.set_maintainers([meta])

            # Removing the laptop: it's refused everywhere and wipes itself.
            laptop_id = next(d["id"] for d in await owner.devices.list() if d["name"] == "laptop")
            r = await owner.devices.remove(laptop_id)
            assert r["failures"] == []
            await settle(lambda: laptop2.vault is None, laptop2, owner)
            assert not (tmp_path / "laptop" / "data" / "vault.bin").exists()
            assert not laptop_key.exists()  # nor the key that opened it
            assert [d["name"] for d in await owner.devices.list()] == ["owner-desktop"]
            # The removed device's keys are out of every DM and the device group.
            await settle(lambda: True, alice, owner)
            await alice.dms.send(conv, "after removal")
            await settle(lambda: "after removal" in texts(owner, conv), owner, alice)
            assert all(d.device_id.hex() != laptop_id for pub in owner.dms.d["ids"] for d in owner.dms._devices_of(pub))
            await laptop2.shutdown()
            for c in (owner, alice):
                await c.shutdown()

        run(main(), 180)


def test_removed_device_cannot_connect(tmp_path):
    """Even a removed device that never hears about it (offline, vault intact)
    is refused by every community."""
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            owner, _ = await new_core(tmp_path / "owner", "desk", maintainers=[meta])
            cid = (await owner.claim(node.claim_code, WIZARD, "Olive"))["community"]
            await general(owner, cid)
            await wait_relay_registered(owner)
            laptop, _ = await link(owner, tmp_path / "laptop", "laptop")
            await online(laptop, cid)
            await laptop.shutdown()  # offline from here on: never gets the removal event
            laptop_id = next(d["id"] for d in await owner.devices.list() if d["name"] == "laptop")
            await owner.devices.remove(laptop_id)
            # Boot the laptop without letting it read its mailbox first.
            stale = Core(Paths.under(tmp_path / "laptop"), emit=Events())
            stale.dms.poll_once = lambda: asyncio.sleep(0)  # type: ignore[method-assign]
            await stale.unlock(PASS)
            await wait_until(lambda: stale.vault.data["communities"][cid]["status"] == "device_revoked", 20)
            await stale.shutdown()
            await owner.shutdown()

        run(main(), 120)


def test_revocation_reaches_a_community_that_was_down(tmp_path):
    """A community that can't be reached when a device is removed still gets
    the revocation: it's kept (also across a restart of the app) and sent
    when the community is back, so the removed device is refused there."""
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            owner, _ = await new_core(tmp_path / "owner", "desk", maintainers=[meta])
            cid = (await owner.claim(node.claim_code, WIZARD, "Olive"))["community"]
            await general(owner, cid)
            await wait_relay_registered(owner)
            laptop, _ = await link(owner, tmp_path / "laptop", "laptop")
            await online(laptop, cid)
            await laptop.shutdown()  # never hears about its removal
            laptop_id = next(d["id"] for d in await owner.devices.list() if d["name"] == "laptop")
            node.stop()
            r = await owner.devices.remove(laptop_id)
            assert len(r["failures"]) == 1 and "sent when the community is reachable" in r["failures"][0]
            assert len(owner.vault.data["pending_revocations"]) == 1
            await owner.shutdown()

            node.start()
            owner2 = Core(Paths.under(tmp_path / "owner"), emit=Events())
            await owner2.unlock(PASS)
            await wait_until(lambda: owner2.vault.data.get("pending_revocations") == [], 30, 0.2)
            stale = Core(Paths.under(tmp_path / "laptop"), emit=Events())
            stale.dms.poll_once = lambda: asyncio.sleep(0)  # type: ignore[method-assign]
            await stale.unlock(PASS)
            await wait_until(lambda: stale.vault.data["communities"][cid]["status"] == "device_revoked", 20)
            await stale.shutdown()
            await owner2.shutdown()

        run(main(), 180)


def test_wrong_code_cancels_and_nothing_is_sent(tmp_path):
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            owner, _ = await new_core(tmp_path / "owner", maintainers=[meta])
            await owner.claim(node.claim_code, WIZARD, "Olive")
            await wait_relay_registered(owner)
            ev = Events()
            new = Core(Paths.under(tmp_path / "new"), emit=ev)
            await new.set_maintainers([meta])
            started = await new.devices.new_device_start("phone")
            number, *words = started["code"].split("-")
            wrong = "-".join([number, *reversed(words)]) if words[0] != words[-1] else f"{number}-acorn-acorn-zebra"
            with pytest.raises(UserError, match="didn't match"):
                await owner.devices.existing_enter_code(wrong)
            await ev.wait(lambda n, p: n == "link" and p["stage"] == "failed", 20)
            assert new.devices.link.transfer is None and new.vault is None
            # One guess per code: the nameplate is spent, even with the right words now.
            with pytest.raises(UserError, match="expired or was already used"):
                await owner.devices.existing_enter_code(started["code"])
            await new.shutdown()
            await owner.shutdown()

        run(main(), 90)


def test_expired_code_refused(tmp_path):
    with network(tmp_path, nameplate_ttl="5s") as (meta, _relay, community):
        community()

        async def main():
            owner, _ = await new_core(tmp_path / "owner", maintainers=[meta])
            await wait_relay_registered(owner)
            new = Core(Paths.under(tmp_path / "new"), emit=Events())
            await new.set_maintainers([meta])
            started = await new.devices.new_device_start("phone")
            await asyncio.sleep(5.5)
            with pytest.raises(UserError, match="expired"):
                await owner.devices.existing_enter_code(started["code"])
            await new.shutdown()
            await owner.shutdown()

        run(main(), 60)


def test_history_copy_resumes_after_interruption(tmp_path, monkeypatch):
    """The link-time history copy runs in the background, one bounded chunk
    at a time, and picks up where it left off after the new device stops."""
    from openarcanum import devices

    monkeypatch.setattr(devices, "HISTORY_CHUNK", 4096)
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            owner, _oev, alice, _aev, cid, _res = await dm_pair(tmp_path, meta, node)
            await wait_relay_registered(owner)
            conv = await start_dm(owner, alice, cid)
            await owner.dms.accept(conv)
            await settle(lambda: alice.dms.info(conv)["status"] == "active", alice, owner)
            await alice.dms.send(conv, "seed")
            await settle(lambda: "seed" in texts(owner, conv), owner, alice)
            # Plenty of history: many chunks at this chunk size.
            base = owner.dms.history(conv)[-1]
            bulk = [dict(base, id=f"{i:032x}", at=base["at"] + i + 1, text=f"old message {i}") for i in range(300)]
            owner.dms.history_import(conv, bulk)
            # Bounded: no chunk is much bigger than the limit.
            items, nxt = owner.devices._chunk_after(None, True, [])
            assert nxt is not None and len(json.dumps(items)) <= devices.HISTORY_CHUNK + 512

            laptop, _ = await link(owner, tmp_path / "laptop", "laptop")
            # Let a few chunks arrive, then stop the laptop mid-copy.
            await settle(lambda: laptop.devices.copy_status()["received"] > 50, laptop, owner, timeout=30)
            assert not laptop.devices.copy_status()["done"]
            await laptop.shutdown()
            # While it's stopped, the owner's history changes before the
            # cursor: the first 30 old messages (already copied) are purged
            # and the stored order is shuffled. A positional cursor would
            # now point 30 items too far and skip them; the keyed one
            # resumes right after the last item the laptop saved.
            cursor = laptop.vault.data["history_copy"]["cursor"]
            assert cursor[:2] == ["dm", conv]
            hist = owner.dms.store.data[conv]
            hist[:] = [m for m in hist if m["text"] not in {f"old message {i}" for i in range(30)}]
            hist.reverse()
            owner.dms.store.save()
            laptop2 = Core(Paths.under(tmp_path / "laptop"), emit=Events())
            await laptop2.unlock(PASS)
            await settle(lambda: laptop2.devices.copy_status()["done"], laptop2, owner, timeout=60)
            got = texts(laptop2, conv)
            assert sorted(got) == sorted(["seed", *(f"old message {i}" for i in range(300))])
            assert len(got) == len(set(got))  # nothing twice
            for c in (owner, alice, laptop2):
                await c.shutdown()

        run(main(), 180)


def test_link_without_history_copy(tmp_path):
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            owner, _oev, alice, _aev, cid, _res = await dm_pair(tmp_path, meta, node)
            await wait_relay_registered(owner)
            conv = await start_dm(owner, alice, cid)
            await owner.dms.accept(conv)
            await settle(lambda: alice.dms.info(conv)["status"] == "active", alice, owner)
            await alice.dms.send(conv, "not copied")
            await settle(lambda: "not copied" in texts(owner, conv), owner, alice)
            laptop, _ = await link(owner, tmp_path / "laptop", "laptop", {"communities": [cid], "copy_dms": False})
            assert laptop.devices.copy_status() == {"active": False, "done": False, "received": 0}
            assert texts(laptop, conv) == []
            for c in (owner, alice, laptop):
                await c.shutdown()

        run(main(), 120)
