"""Channels, categories, roles, overrides, invites and feature toggles end to
end: two clients against the real relay and community server binaries."""

import pytest
from conftest import new_core, run, run_community, run_relay, wait_until
from test_e2e_community import WIZARD, general

from openarcanum.core import UserError


def chan(core, cid, name):
    return next(c for c in core.community_state(cid)["channels"] if c["name"] == name)


def role(core, cid, name):
    return next(r for r in core.community_state(cid)["roles"] if r["name"] == name)


def test_channels_roles_overrides_and_toggles(tmp_path):
    with run_relay() as relay, run_community(tmp_path, [relay]) as node:

        async def main():
            owner, _ = await new_core(tmp_path / "owner")
            res = await owner.claim(node.claim_code, WIZARD, "Olive")
            cid = res["community"]
            await general(owner, cid)
            alice, _ = await new_core(tmp_path / "alice")
            await alice.join({"invite": res["invite"]}, "new", "Alice")
            gen = await general(alice, cid)

            # A category with a channel, a topic and slowmode.
            cat = (await owner.admin(cid, "create_category", {"name": "Lounge"}))["id"]
            await owner.admin(cid, "create_channel", {"name": "tea", "category_id": cat, "topic": "Hot drinks"})
            await wait_until(lambda: any(c["name"] == "tea" for c in alice.community_state(cid)["channels"]), 15)
            tea = chan(alice, cid, "tea")
            assert tea["category_id"] == cat and tea["synced"] and tea["topic"] == "Hot drinks"
            assert [c["name"] for c in alice.community_state(cid)["categories"]] == ["Lounge"]
            await owner.admin(cid, "edit_channel", {"id": tea["id"], "slowmode": 30})
            await wait_until(lambda: chan(alice, cid, "tea")["slowmode"] == 30, 15)
            await alice.send(cid, tea["id"], "first cup")
            with pytest.raises(UserError, match="slowmode"):
                await alice.send(cid, tea["id"], "second cup")
            await owner.send(cid, tea["id"], "owners are exempt")
            await owner.send(cid, tea["id"], "twice")

            # A hoisted, colored role with a permission, assigned to Alice.
            await owner.admin(
                cid, "create_role", {"name": "Hosts", "color": "#22aa44", "hoist": True, "permissions": ["kick"]}
            )
            hosts = role(owner, cid, "Hosts")
            assert hosts["hoist"] and hosts["color"] == "#22aa44" and hosts["perms"] == ["kick"]
            akey = alice.community_state(cid)["self"]["key"]
            await owner.admin(cid, "assign_roles", {"member": akey, "role_ids": [hosts["id"]]})
            await wait_until(lambda: alice.community_summary(cid)["can"]["kick"], 15)

            # A per-member override: Alice can't send in #general any more.
            await owner.admin(cid, "set_override", {"channel_id": gen, "member": akey, "deny": ["send_messages"]})
            await wait_until(lambda: not chan(alice, cid, "general")["can"]["send_messages"], 15)
            with pytest.raises(UserError):
                await alice.send(cid, gen, "muted")
            ov = chan(owner, cid, "general")["overrides"]
            assert ov == [{"role_id": 0, "member": akey, "allow": [], "deny": ["send_messages"]}]

            # Invites: Alice (no Create Invite) can't make one; the owner
            # lists them with creator names and revokes.
            assert not alice.community_summary(cid)["can"]["create_invite"]
            with pytest.raises(UserError):
                await alice.admin(cid, "create_invite", {"label": "mine"})
            made = await owner.admin(cid, "create_invite", {"label": "party", "max_uses": 3})
            keys = await owner.admin_query(cid, "join_keys")
            party = next(k for k in keys if k["label"] == "party")
            assert party["created_by_name"] == "Olive" and party["max_uses"] == 3 and not party["revoked"]
            await owner.admin(cid, "revoke_invite", {"id": made["join_key"]["id"]})
            assert next(k for k in await owner.admin_query(cid, "join_keys") if k["label"] == "party")["revoked"]

            # Toggles: search and voice off are hidden on the member's side and
            # refused by the server; the audit log records each change.
            await owner.admin(cid, "update_settings", {"features": {"search": False, "voice": False}})
            await wait_until(lambda: alice.community_summary(cid)["features"]["search"] is False, 15)
            assert alice.community_summary(cid)["features"]["voice"] is False
            with pytest.raises(UserError, match="turned off search"):
                await alice.search_messages(cid, "cup")
            with pytest.raises(UserError, match="turned off voice channels"):
                await owner.admin(cid, "create_channel", {"name": "talk", "kind": "voice"})
            audit = [e["summary"] for e in await owner.admin_query(cid, "audit")]
            assert "changed turned voice channels off, turned search off" in audit
            # A channel-level toggle shows up in the member's channel features.
            await owner.admin(cid, "edit_channel", {"id": tea["id"], "reactions": "off"})
            await wait_until(lambda: chan(alice, cid, "tea")["features"]["reactions"] is False, 15)
            assert chan(alice, cid, "general")["features"]["reactions"] is True
            with pytest.raises(UserError):
                await owner.admin(cid, "update_settings", {"features": {"flying": True}})

            for c in (owner, alice):
                await c.shutdown()

        run(main(), 120)
