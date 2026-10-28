"""Several communities on one server (docs/operations.md "Several
communities on one server"): an owner starts another on the server that
runs one of theirs, from the app, and it's claimed for them at once."""

from __future__ import annotations

import pytest
from conftest import new_core, run, run_community, run_relay
from test_e2e_community import WIZARD, general

from openarcanum.core import UserError


def test_owner_starts_another_community_on_the_same_server(tmp_path):
    with run_relay() as relay, run_community(tmp_path, [relay]) as node:

        async def main():
            owner, _ = await new_core(tmp_path / "owner")
            first = (await owner.claim(node.claim_code, WIZARD, "Olive"))["community"]
            ch = await general(owner, first)
            second = (await owner.host_on_server(first, {**WIZARD, "name": "Second Town"}, "Olive"))["community"]
            assert second != first
            ch2 = await general(owner, second)
            # Separate communities: each has its own messages.
            await owner.send(second, ch2, "only in the second")
            await owner.send(first, ch, "only in the first")
            names = {c["name"] for c in owner.list_communities() if c["status"] == "active"}
            assert {"Test Town", "Second Town"} <= names
            # Only owners can start one there.
            member, _ = await new_core(tmp_path / "member")
            inv = await owner.admin(first, "create_invite", {"label": "x"})
            await member.join({"invite": inv["invite"]}, "new", "Mem")
            await general(member, first)
            with pytest.raises(UserError):
                await member.host_on_server(first, WIZARD, "Mem")
            for c in (owner, member):
                await c.shutdown()

        run(main(), 120)
