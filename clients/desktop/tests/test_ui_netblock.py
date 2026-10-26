"""The ban dialog's "Also block their network address" by
clicks, in a real window, on a community pinned to a relay dedicated to it
(docs/protocol.md §2 "Dedicated relays"). The option's absence on an
ordinary relay is checked in test_ui_moderation.py's ban step. Bob is a
headless client connecting from another loopback address, so the relay sees
him as another network than the community server."""

import pytest
from conftest import new_core, run, wait_until
from test_e2e_community import general
from test_e2e_moderation import dedicated_network, from_address, sourced_relays  # noqa: F401 (fixture)
from uidriver import App
from uiflows import claim, community_section, create_profile, member_action

from openarcanum.relay import Relay, RelayBlocked

pytestmark = [pytest.mark.gui, pytest.mark.timeout(240)]


def test_ban_dialog_blocks_network_on_dedicated_relay(tmp_path, sourced_relays):  # noqa: F811
    with dedicated_network(tmp_path) as (node, url):

        async def main():
            owner = await App(tmp_path / "owner", name="owner").start()
            bob = None
            try:
                await create_profile(owner)
                invite = await claim(owner, node.claim_code, "Guarded Town", "Tester")

                async def join_bob():
                    core, _ = await new_core(tmp_path / "bob")
                    await core.join({"invite": invite}, "new", "Bob")
                    return core

                bob = await from_address("127.0.0.7", join_bob())
                cid = next(iter(bob.conns))
                await general(bob, cid)

                await member_action(owner, "Bob", "Ban…")
                await owner.wait_js("!!document.querySelector('.modal .ban-block-network')", "network block option")
                hint = " ".join(await owner.texts(".modal .ban-block-network .hint"))
                assert "VPN" in hint and "mobile carrier" in hint
                # The duration appears once the option is ticked.
                assert not await owner.js(
                    "(() => { const s = document.querySelector('.modal select[aria-label=\"Block the address for\"]');"
                    " return !!(s && s.offsetParent); })()"
                )
                await owner.click(".modal .ban-block-network input")
                await owner.select('.modal select[aria-label="Block the address for"]', "30 days")
                await owner.button("Ban")
                await owner.wait_text(".toast", "Their network address is blocked.")
                await wait_until(lambda: bob.vault.data["communities"][cid]["status"] == "banned", 15)

                # The relay refuses Bob's address now.
                with pytest.raises(RelayBlocked):
                    await from_address("127.0.0.7", Relay(url).connect(timeout=5))

                # The ban list says so; the audit log names the block, not the address.
                await community_section(owner, "Bans")
                await owner.wait_text(".ban-network-block", "Network address blocked until")
                await community_section(owner, "Audit log")
                await owner.wait_text(".audit-row td", "blocked the network address of Bob")
                assert not any("127.0.0" in t for t in await owner.texts(".audit-row td"))

                owner.assert_no_errors()
                owner.assert_local_only()
            finally:
                await owner.stop()
                if bob is not None:
                    await bob.shutdown()

        run(main(), 240)
