"""Community basics (create, join, chat, restart, backups, removal) end to end against the real relay and community
server binaries."""

import asyncio
import os
import sqlite3
import stat
import time

import pytest
from conftest import PASS, new_core, run, run_community, run_relay, wait_until

from openarcanum import keys
from openarcanum.core import Core, UserError
from openarcanum.paths import Paths
from openarcanum.wire import community_pb2 as cp

WIZARD = {
    "name": "Test Town",
    "description": "a test community",
    "tags": "test, town",
    "visibility": "private",
    "join_policy": "invite_only",
    "message_retention_days": 30,
    "file_retention_days": 7,
}


async def online(core: Core, cid: str):
    await wait_until(lambda: cid in core.conns and core.conns[cid].status == "online")


async def general(core: Core, cid: str) -> int:
    await online(core, cid)
    st = core.community_state(cid)
    return next(c["id"] for c in st["channels"] if c["name"] == "general")


def test_two_clients_chat_and_refusals(tmp_path):
    with run_relay() as relay, run_community(tmp_path, [relay]) as node:

        async def main():
            owner, oev = await new_core(tmp_path / "owner")
            res = await owner.claim(node.claim_code, WIZARD, "Olive")
            cid = res["community"]
            assert res["invite"].startswith("openarcanum-invite:")
            gen = await general(owner, cid)

            alice, aev = await new_core(tmp_path / "alice")
            preview = await alice.preview({"invite": res["invite"]})
            assert preview["name"] == "Test Town"
            assert "Admins of this community can read everything" in preview["notice"]
            assert preview["member_range"] == "1-10"
            joined = await alice.join({"invite": res["invite"]}, "new", "Alice")
            assert joined == {"status": "admitted", "community": cid}
            agen = await general(alice, cid)
            assert agen == gen

            # Chat both ways; each side gets the other's message through the relay topic.
            await owner.send(cid, gen, "hello alice")
            await aev.wait(lambda n, p: n == "message" and p["message"]["text"] == "hello alice")
            await alice.send(cid, gen, "hi olive")
            got = await oev.wait(lambda n, p: n == "message" and p["message"]["text"] == "hi olive")
            assert got["message"]["author_name"] == "Alice"
            # A notification for the owner (not for Alice's own message).
            await oev.wait(lambda n, p: n == "notify" and p["body"] == "hi olive")
            assert not [p for p in aev.of("notify") if p["body"] == "hi alice"]

            # Revoked and expired join keys are refused.
            made = await owner.admin(cid, "create_invite", {"label": "soon revoked", "expires_days": 7})
            await owner.admin(cid, "revoke_invite", {"id": made["join_key"]["id"]})
            bob, _ = await new_core(tmp_path / "bob")
            r = await bob.join({"invite": made["invite"]}, "new", "Bob")
            assert r["status"] == "refused" and "revoked" in r["reason"]
            short = await owner.admin(cid, "create_invite", {"label": "short", "expires_days": 2 / 86400})
            await asyncio.sleep(2.2)
            r = await bob.join({"invite": short["invite"]}, "new", "Bob")
            assert r["status"] == "refused" and "expired" in r["reason"]
            # Refused joins leave no keys behind.
            assert not [i for i in bob.vault.data["identities"].values() if i["kind"] == "member"]
            for c in (owner, alice, bob):
                await c.shutdown()

        run(main(), 90)


def test_history_survives_server_restart(tmp_path):
    with run_relay() as relay, run_community(tmp_path, [relay]) as node:

        async def main():
            owner, _ = await new_core(tmp_path / "owner")
            cid = (await owner.claim(node.claim_code, WIZARD, "Olive"))["community"]
            gen = await general(owner, cid)
            for i in range(3):
                await owner.send(cid, gen, f"message {i}")
            node.stop()
            await wait_until(lambda: owner.conns[cid].status != "online")
            node.start()
            await online(owner, cid)  # reconnects by itself
            h = await owner.history(cid, gen)
            assert [m["text"] for m in h["messages"]] == ["message 0", "message 1", "message 2"]
            assert not h["offline"]
            await owner.shutdown()

        run(main(), 90)


def test_vault_wrong_passphrase_and_backoff(tmp_path):
    async def main():
        core, _ = await new_core(tmp_path / "p")
        await core.shutdown()
        again = Core(Paths.under(tmp_path / "p"))
        assert again.profile_status()["state"] == "locked"
        with pytest.raises(UserError, match="Wrong passphrase"):
            await again.unlock("not the passphrase")
        # The next attempt has to wait, even with the right passphrase.
        with pytest.raises(UserError, match="Wait"):
            await again.unlock(PASS)
        assert again.guard.remaining() > 0
        again.guard.now = lambda: time.time() + 5
        await again.unlock(PASS)
        assert again.profile_status()["state"] == "unlocked"
        # The vault on disk is encrypted: no plaintext keys or settings.
        blob = (tmp_path / "p" / "data" / "vault.bin").read_bytes()
        assert b"identities" not in blob and b"device_name" not in blob
        await again.shutdown()

    run(main())


def test_backup_restore_reconnects(tmp_path):
    with run_relay() as relay, run_community(tmp_path, [relay]) as node:

        async def main():
            owner, _ = await new_core(tmp_path / "owner")
            cid = (await owner.claim(node.claim_code, WIZARD, "Olive"))["community"]
            gen = await general(owner, cid)
            assert not owner.vault.backup_current
            await owner.send(cid, gen, "before backup")
            path = tmp_path / "backup.fbak"
            with pytest.raises(UserError):
                await owner.export_backup(str(path), "short")
            await owner.export_backup(str(path), "backup passphrase here")
            assert owner.vault.backup_current
            await owner.shutdown()

            # A new device restores from the file and reconnects to the community.
            fresh = Core(Paths.under(tmp_path / "restored"))
            with pytest.raises(UserError, match="Wrong backup passphrase"):
                await fresh.restore_backup(str(path), "nope nope nope", PASS)
            await fresh.restore_backup(str(path), "backup passphrase here", PASS, "restored-desktop")
            await online(fresh, cid)
            h = await fresh.history(cid, gen)
            assert [m["text"] for m in h["messages"]] == ["before backup"]
            # It got its own device key: the server now lists two devices.
            devs = await fresh.conns[cid].call(list_devices=cp.ListDevices())
            assert len(devs.devices.devices) == 2
            # Owner powers survived: the restored profile can still admin.
            await fresh.admin(cid, "create_channel", {"name": "after-restore"})
            await fresh.shutdown()

        run(main(), 90)


def test_backup_file_is_private_and_carries_the_sync_identity(tmp_path):
    """The backup file is readable by its user only (it holds every key),
    the folder it goes into keeps its mode, and the device group's sync
    identity is in it: a restored profile keeps it."""

    async def main():
        core, _ = await new_core(tmp_path / "p")
        sync_pub = core.dms.d["sync"]["pub"]
        folder = tmp_path / "exports"
        folder.mkdir()
        if os.name == "posix":
            folder.chmod(0o755)
        await core.export_backup(str(folder), "backup passphrase here")
        path = folder / "openarcanum-backup.fbak"
        if os.name == "posix":
            assert stat.S_IMODE(path.stat().st_mode) == 0o600
            assert stat.S_IMODE(folder.stat().st_mode) == 0o755
        data = keys.import_backup(path.read_bytes(), "backup passphrase here")
        assert [i["kind"] for i in data["identities"].values()] == ["sync"]
        await core.shutdown()

        fresh = Core(Paths.under(tmp_path / "restored"))
        await fresh.restore_backup(str(path), "backup passphrase here", PASS, "restored-desktop")
        assert fresh.dms.d["sync"]["pub"] == sync_pub
        assert [i["kind"] for i in fresh.vault.data["identities"].values()] == ["sync"]
        await fresh.shutdown()

    run(main(), 60)


def test_removed_member_stops_reconnecting(tmp_path):
    with run_relay() as relay, run_community(tmp_path, [relay]) as node:

        async def main():
            owner, _ = await new_core(tmp_path / "owner")
            res = await owner.claim(node.claim_code, WIZARD, "Olive")
            cid = res["community"]
            await general(owner, cid)
            mal, _ = await new_core(tmp_path / "mal")
            await mal.join({"invite": res["invite"]}, "new", "Mallory")
            await general(mal, cid)
            mal.cache(cid).upsert(
                {
                    "id": 99,
                    "channel_id": 1,
                    "created_at": int(time.time() * 1000),
                    "text": "x",
                    "author": "",
                    "author_name": "",
                    "deleted": False,
                    "file": None,
                }
            )
            mal.cache(cid).save()
            members = await owner.members(cid)
            target = next(m for m in members if m["display_name"] == "Mallory")
            await owner.admin(cid, "kick", {"member": target["key"], "reason": "test"})
            await wait_until(lambda: mal.vault.data["communities"][cid]["status"] == "removed")
            # Local data is gone and the connection stopped for good.
            assert mal.cache(cid).messages == {}
            await asyncio.sleep(1.5)
            assert mal.conns[cid].status == "removed"
            assert mal.conns[cid]._task.done()
            # The banner says why, while the ended connection is still there.
            summary = mal.community_summary(cid)
            assert (summary["status"], summary["error"]) == ("removed", "You were removed from this community.")
            # After a restart it doesn't reconnect either.
            await mal.shutdown()
            again = Core(Paths.under(tmp_path / "mal"))
            await again.unlock(PASS)
            assert cid not in again.conns
            assert again.community_summary(cid)["error"] == "You were removed from this community."
            await again.dismiss(cid)
            assert cid not in again.vault.data["communities"]
            await again.shutdown()
            await owner.shutdown()

        run(main(), 90)


def test_messages_past_retention_leave_this_device(tmp_path):
    """The server purges by age; once it has, an online client's history and
    cache drop what it no longer has (the creation times are moved back in
    the database here instead of waiting a day)."""
    with run_relay() as relay, run_community(tmp_path, [relay]) as node:

        async def main():
            core, _ = await new_core(tmp_path / "owner")
            cid = (await core.claim(node.claim_code, WIZARD, "Olive"))["community"]
            gen = await general(core, cid)
            short = (await core.admin(cid, "create_channel", {"name": "short"}))["id"]
            await core.admin(cid, "edit_channel", {"id": short, "message_retention_days": 1})
            for text in ("old one", "old two"):
                await core.send(cid, short, text)
            await core.send(cid, gen, "stays")
            assert [m["text"] for m in core.cache(cid).channel(short)] == ["old one", "old two"]

            node.stop()
            db = sqlite3.connect(node.community / "community.db")
            db.execute("UPDATE messages SET created_at = created_at - 2*86400000 WHERE channel_id = ?", (short,))
            db.commit()
            db.close()
            node.start()  # the retention job runs at start
            await asyncio.sleep(1)
            await core.retry(cid)
            await online(core, cid)

            async def purged():
                res = await core.history(cid, short)
                return not res["offline"] and res["messages"] == []

            await wait_until(purged, 20, 0.5)
            assert core.cache(cid).channel(short) == []
            assert (await core.history(cid, gen))["messages"][-1]["text"] == "stays"
            assert any(m["text"] == "stays" for m in core.cache(cid).channel(gen))
            await core.shutdown()

        run(main(), 90)
