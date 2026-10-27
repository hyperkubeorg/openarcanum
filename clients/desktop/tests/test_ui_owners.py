"""Ownership by clicks (multiple owner keys): accept an owner invitation, add
an owner, confirm their key, remove the founder and another owner, and see
the single-owner warning. Bob (the founder) and Carol are headless clients
acting alongside the window."""

import asyncio

import pytest
from conftest import new_core, run, run_community, run_relay, wait_until
from test_e2e_community import WIZARD, general
from uidriver import App
from uiflows import create_profile, join

pytestmark = [pytest.mark.gui, pytest.mark.timeout(360)]


async def owner_names(app: App) -> str:
    return ",".join(sorted(await app.texts(".settings section .owner-row b")))


async def wait_in_ownership(app: App, pred, what: str, timeout: float = 60.0) -> None:
    """The Ownership section shows a snapshot: reopen it until pred holds
    (other people's devices act in the meantime)."""
    loop = asyncio.get_running_loop()
    deadline = loop.time() + timeout
    while loop.time() < deadline:
        await app.click(".settings nav .item", "Ownership")
        await asyncio.sleep(0.7)
        if await pred():
            return
        await asyncio.sleep(0.5)
    raise AssertionError(f"timed out waiting for {what}")


async def remove_owner(app: App, name: str) -> None:
    await app.click(".settings section .owner-row button", f"Remove {name} as an owner")
    await app.button("Remove")


def test_owners_ui_flow(tmp_path):
    with run_relay() as relay, run_community(tmp_path, [relay]) as node:

        async def main():
            bob, _ = await new_core(tmp_path / "bob")
            res = await bob.claim(node.claim_code, WIZARD, "Bob")
            cid = res["community"]
            await general(bob, cid)
            carol, _ = await new_core(tmp_path / "carol")
            await carol.join({"invite": res["invite"]}, "new", "Carol")
            await general(carol, cid)

            async def others():
                # Bob invites the window's user once they've joined, and
                # confirms the owner key their client sends.
                async def joined():
                    return next((m for m in await bob.members(cid) if m["display_name"] == "Tester"), None)

                t = await wait_until(joined, 150, 0.5)
                await bob.invite_owner(cid, t["key"])
                await wait_until(
                    lambda: any(p["submitted"] for p in bob.community_state(cid)["pending_owners"]), 120, 0.3
                )
                await bob.confirm_owner(cid, t["key"])
                # Carol accepts the invitation the window's user sends her.
                await wait_until(lambda: carol.community_state(cid)["self"]["owner_invited"], 120, 0.3)
                await carol.accept_owner_invite(cid)

            helper = asyncio.ensure_future(others())
            app = await App(tmp_path / "tester", name="tester").start()
            try:
                await create_profile(app)
                await join(app, res["invite"], "Tester")
                # Bob's invitation shows as a banner that leads to Ownership.
                await app.wait_text(".banner", "invited to become an owner", 60)
                await app.button("Review")
                await app.wait_js("document.querySelector('.settings section .owner-invite')", "invitation card")
                await app.click(".settings section .owner-invite button", "Accept")

                async def two():
                    return await owner_names(app) == "Bob,Tester"

                await wait_in_ownership(app, two, "two owners")
                assert not await app.exists(".settings section .single-owner")

                # Add Carol: pick her, invite, confirm once her device answers.
                await app.select(".settings section .add-owner select", "Carol")
                await app.click(".settings section .add-owner button", "Invite as owner")

                async def carol_key():
                    return await app.exists(".settings section button", "Confirm")

                await wait_in_ownership(app, carol_key, "Carol's owner key")
                await app.click(".settings section button", "Confirm")

                async def three():
                    return await owner_names(app) == "Bob,Carol,Tester"

                await wait_in_ownership(app, three, "three owners")

                # Remove the founder, then Carol: the window's user is the last owner.
                await remove_owner(app, "Bob")

                async def bob_gone():
                    return await owner_names(app) == "Carol,Tester"

                await wait_in_ownership(app, bob_gone, "Bob removed")
                await remove_owner(app, "Carol")

                async def alone():
                    return await owner_names(app) == "Tester"

                await wait_in_ownership(app, alone, "Carol removed")
                await app.wait_text(".settings section .single-owner", "Add a second owner")
                assert await app.js("document.querySelector('.settings section .owner-row button').disabled"), (
                    "the last owner can be removed in the UI"
                )
                # The warning also shows above the chat.
                await app.click(".settings .close")
                await app.wait_js("document.querySelector('.banner.single-owner')", "single-owner banner", 30)
                app.assert_no_errors()
            finally:
                await app.stop()
            await asyncio.wait_for(helper, 10)
            # Bob's client agrees: the window's user is the only owner now.
            await wait_until(lambda: not bob.community_summary(cid)["is_owner"], 20)
            st = bob.community_state(cid)
            assert [o["display_name"] for o in st["owners"]] == ["Tester"] and st["single_owner"]
            await wait_until(lambda: not carol.community_summary(cid)["is_owner"], 20)
            await bob.shutdown()
            await carol.shutdown()

        run(main(), 300)
