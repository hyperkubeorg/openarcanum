"""Multiple owner keys (docs/protocol.md §4.5), end to end
against the real relay, maintainer and community binaries: a second owner
is added, rotates the server key, removes the founder, can't remove the last
owner, and moves the community to new hardware."""

import asyncio
import subprocess

import pytest
from conftest import CommunityNode, binary, network, new_core, run, wait_relay_registered, wait_until
from test_e2e_community import WIZARD, general, online

from openarcanum import keys, owners
from openarcanum.community import RpcError, make_verifier
from openarcanum.core import UserError
from openarcanum.keys import SigningKey
from openarcanum.session import IdentityError
from openarcanum.wire import community_pb2 as cp
from openarcanum.wire import session_pb2 as sp

pytestmark = pytest.mark.timeout(210)


async def add_owner(owner, member, cid: str) -> None:
    """The UI flow without the clicks: invite, accept on the member's
    device, confirm on the owner's."""
    me = member.community_state(cid)["self"]["key"]
    await owner.invite_owner(cid, me)
    await wait_until(lambda: member.community_state(cid)["self"]["owner_invited"])
    await member.accept_owner_invite(cid)
    await wait_until(lambda: any(p["submitted"] for p in owner.community_state(cid)["pending_owners"]))
    await owner.confirm_owner(cid, me)
    await wait_until(lambda: member.community_summary(cid)["is_owner"], 20)


def test_second_owner_rotates_removes_founder(tmp_path):
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            founder, _ = await new_core(tmp_path / "founder", maintainers=[meta])
            res = await founder.claim(node.claim_code, WIZARD, "Olive")
            cid = res["community"]
            await general(founder, cid)
            st = founder.community_state(cid)
            assert st["single_owner"] and [o["display_name"] for o in st["owners"]] == ["Olive"]
            alice, aev = await new_core(tmp_path / "alice", maintainers=[meta])
            await alice.join({"invite": res["invite"]}, "new", "Alice")
            await general(alice, cid)
            bob, _ = await new_core(tmp_path / "bob", maintainers=[meta])
            await bob.join({"invite": res["invite"]}, "new", "Bob")
            await general(bob, cid)

            # A member who wasn't invited can't make themselves an owner.
            with pytest.raises(UserError, match="invited"):
                await bob.accept_owner_invite(cid)
            with pytest.raises(UserError, match="owner"):
                await bob.invite_owner(cid, bob.community_state(cid)["self"]["key"])

            await add_owner(founder, alice, cid)
            await aev.wait(lambda n, p: n == "toast" and "now an owner" in p["text"])
            st = founder.community_state(cid)
            assert not st["single_owner"] and sorted(o["display_name"] for o in st["owners"]) == ["Alice", "Olive"]
            # Alice's owner key never left her device: the founder's vault
            # only knows its public half.
            alice_iid = alice.vault.data["communities"][cid]["owner_identity"]
            assert alice_iid not in founder.vault.data["identities"]

            # The second owner rotates the server key; members keep connecting.
            old_key = bob.vault.data["communities"][cid]["server_key"]
            new_key = (await alice.rotate_server_key(cid))["server_key"]
            assert new_key != old_key
            await wait_until(lambda: bob.vault.data["communities"][cid]["server_key"] == new_key, 20)
            # The server moves its relay endpoint to the new key, which ends
            # sessions to the old one; Bob reconnects to the new key.
            gen = await general(bob, cid)

            async def bob_posts():
                try:
                    await bob.send(cid, gen, "still here after rotation")
                except UserError:
                    bob.conns[cid].reconnect_now()
                    return False
                return True

            await wait_until(bob_posts, 30, 0.5)

            # Alice removes the founder. The founder's client drops the key.
            founder_key = founder.identity_key(founder.vault.data["communities"][cid]["owner_identity"])
            await alice.remove_owner(cid, cid)
            await wait_until(lambda: not founder.community_summary(cid)["is_owner"], 20)
            assert founder_key.public != alice.identity_public(alice_iid)
            st = alice.community_state(cid)
            assert st["single_owner"] and [o["display_name"] for o in st["owners"]] == ["Alice"]
            # The server refuses the removed founder's ownership actions...
            with pytest.raises(RpcError):
                await founder.conns[cid].admin("prepare_key_rotation", cp.PrepareKeyRotation())
            with pytest.raises(UserError, match="Only an owner"):
                await founder.rotate_server_key(cid)
            # ...and clients refuse a server key certified by the founder's
            # key, even with the current owner chain attached.
            rec = bob.vault.data["communities"][cid]
            rogue = SigningKey.generate()
            forged = owners.issue_cert(
                founder_key, rogue.public, rec["cert_seq"] + 1, owners.OwnerSet.founder(founder_key.public), []
            )
            forged.owner_chain.extend(bob.conns[cid].state.cert.owner_chain)
            with pytest.raises(IdentityError):
                make_verifier(rec)(sp.ServerIdentity(server_key=rogue.public, cert=forged))
            # A cert from Alice for the same key would be accepted.
            good = owners.issue_cert(
                alice.identity_key(alice_iid),
                rogue.public,
                rec["cert_seq"] + 1,
                bob.conns[cid].owner_set,
                bob.conns[cid].state.cert.owner_chain,
            )
            make_verifier(dict(rec))(sp.ServerIdentity(server_key=rogue.public, cert=good))

            # The last owner can't be removed.
            with pytest.raises(UserError, match="last owner"):
                await alice.remove_owner(cid, keys.encode(alice.identity_public(alice_iid)))
            # And an owner can't leave while they are one.
            with pytest.raises(UserError):
                await alice.leave(cid)
            for c in (founder, alice, bob):
                await c.shutdown()

        run(main(), 150)


def test_owner_removes_themselves(tmp_path):
    """An owner removes their own key while another owner remains: the
    server stops treating them as an owner, the other owner's client signs
    the removal into the chain, and the last owner still can't leave."""
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            founder, _ = await new_core(tmp_path / "founder", maintainers=[meta])
            res = await founder.claim(node.claim_code, WIZARD, "Olive")
            cid = res["community"]
            await general(founder, cid)
            alice, aev = await new_core(tmp_path / "alice", maintainers=[meta])
            await alice.join({"invite": res["invite"]}, "new", "Alice")
            await general(alice, cid)
            await add_owner(founder, alice, cid)
            await aev.wait(lambda n, p: n == "toast" and "now an owner" in p["text"])
            alice_iid = alice.vault.data["communities"][cid]["owner_identity"]
            alice_owner_key = keys.encode(alice.identity_public(alice_iid))

            await alice.remove_owner(cid, alice_owner_key)
            # The founder's client completes it; Alice's client drops the key.
            await wait_until(lambda: not alice.community_summary(cid)["is_owner"], 30)
            await aev.wait(lambda n, p: n == "toast" and "no longer an owner" in p["text"])
            # The founder's client gets the new state a moment later.
            await wait_until(lambda: founder.community_state(cid)["single_owner"], 30)
            st = founder.community_state(cid)
            assert st["single_owner"] and [o["display_name"] for o in st["owners"]] == ["Olive"]
            assert not founder.conns[cid].owner_set.has(keys.decode(alice_owner_key))
            # Alice's ownership actions are refused by the server.
            with pytest.raises(RpcError):
                await alice.conns[cid].admin("prepare_key_rotation", cp.PrepareKeyRotation())
            # The founder, now the last owner, can't remove themselves.
            with pytest.raises(UserError, match="last owner"):
                await founder.remove_owner(cid, cid)
            for c in (founder, alice):
                await c.shutdown()

        run(main(), 120)


def test_move_to_new_hardware_signed_by_added_owner(tmp_path):
    with network(tmp_path) as (meta, relay, community):
        node = community()

        async def main():
            founder, _ = await new_core(tmp_path / "founder", maintainers=[meta])
            res = await founder.claim(node.claim_code, WIZARD, "Olive")
            cid = res["community"]
            gen = await general(founder, cid)
            alice, _ = await new_core(tmp_path / "alice", maintainers=[meta])
            await alice.join({"invite": res["invite"]}, "new", "Alice")
            await general(alice, cid)
            await founder.send(cid, gen, "carried over")
            # The backup predates Alice becoming an owner: her client carries
            # the newer owner chain to the restored server.
            backup = (await founder.admin(cid, "server_backup", {}))["backup_path"]
            await add_owner(founder, alice, cid)
            assert alice.vault.data["communities"][cid]["owner_seq"] == 1
            return founder, alice, cid, backup

        loop = asyncio.new_event_loop()
        try:
            founder, alice, cid, backup = loop.run_until_complete(asyncio.wait_for(main(), 90))
            node.stop()
            move_to_new_hardware(loop, tmp_path, relay, meta, node, founder, alice, cid, backup)
        finally:
            loop.close()


def move_to_new_hardware(loop, tmp_path, relay, meta, node, founder, alice, cid, backup):
    """Restore the backup on new hardware and have the added owner adopt it."""
    new_dir = tmp_path / "moved"
    out = subprocess.run(
        [
            str(binary("community")),
            "restore",
            "-data",
            str(new_dir),
            "-backup",
            backup,
            "-storage-key",
            str(node.data / "storage.key"),
            "-relay",
            relay,
            "-maintainer",
            meta,
        ],
        capture_output=True,
        text=True,
        check=True,
    ).stdout
    move_code = next(line for line in out.splitlines() if line.startswith("openarcanum-move:"))
    moved = CommunityNode.__new__(CommunityNode)
    moved.data, moved.relays, moved.proc = new_dir, [relay], None
    moved.start()
    try:

        async def phase2():
            # The founder's key isn't needed: Alice certifies the new key.
            await alice.adopt_server(cid, move_code)
            new_key = alice.vault.data["communities"][cid]["server_key"]
            await online(alice, cid)
            # The founder's client finds the new server through the route,
            # which Alice's cert covers.
            founder.conns[cid].reconnect_now()
            await wait_until(
                lambda: (
                    founder.vault.data["communities"][cid]["server_key"] == new_key
                    and founder.conns[cid].status == "online"
                ),
                60,
            )
            gen = await general(founder, cid)
            history = (await founder.history(cid, gen))["messages"]
            assert [m["text"] for m in history if not m.get("system")] == ["carried over"]
            st = founder.community_state(cid)
            assert sorted(o["display_name"] for o in st["owners"]) == ["Alice", "Olive"]
            route = await founder.meta.route(
                keys.decode(founder.vault.data["communities"][cid]["route_id"]), keys.decode(cid)
            )
            assert route is not None and route[1].owner_key == alice.identity_public(
                alice.vault.data["communities"][cid]["owner_identity"]
            )
            await founder.shutdown()
            await alice.shutdown()

        loop.run_until_complete(asyncio.wait_for(phase2(), 120))
    finally:
        moved.stop()


def test_move_code_requires_an_owner(tmp_path):
    """A member without an owner key can't adopt a restored server."""
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            founder, _ = await new_core(tmp_path / "founder", maintainers=[meta])
            res = await founder.claim(node.claim_code, WIZARD, "Olive")
            cid = res["community"]
            bob, _ = await new_core(tmp_path / "bob", maintainers=[meta])
            await bob.join({"invite": res["invite"]}, "new", "Bob")
            with pytest.raises(UserError, match="Only an owner"):
                await bob.adopt_server(cid, "openarcanum-move:AAAA")
            await founder.shutdown()
            await bob.shutdown()

        run(main(), 60)


def test_owner_key_reaches_linked_device(tmp_path):
    """The owner key a device makes is copied to the member's other devices,
    which can then act as that owner."""
    from test_e2e_devices import link, settle

    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            founder, _ = await new_core(tmp_path / "founder", maintainers=[meta])
            res = await founder.claim(node.claim_code, WIZARD, "Olive")
            cid = res["community"]
            await general(founder, cid)
            alice, _ = await new_core(tmp_path / "alice", "alice-desktop", maintainers=[meta])
            await alice.join({"invite": res["invite"]}, "new", "Alice")
            await general(alice, cid)
            await wait_relay_registered(alice)
            laptop, _ = await link(alice, tmp_path / "laptop", "laptop")
            await online(laptop, cid)

            await add_owner(founder, alice, cid)
            iid = alice.vault.data["communities"][cid]["owner_identity"]
            # The laptop gets the key through device sync and promotes it once
            # its own connection sees the owner set.
            await settle(lambda: iid in laptop.vault.data["identities"], alice, laptop)
            laptop.conns[cid].reconnect_now()
            await wait_until(lambda: laptop.community_summary(cid)["is_owner"], 30)
            assert laptop.vault.data["communities"][cid]["owner_identity"] == iid
            # The key may have arrived (mailbox watch) while the connection
            # was reconnecting: rotate once it's back.
            await wait_until(lambda: laptop.conns[cid].status == "online", 30, 0.1)
            await laptop.rotate_server_key(cid)
            for c in (founder, alice, laptop):
                await c.shutdown()

        run(main(), 150)
