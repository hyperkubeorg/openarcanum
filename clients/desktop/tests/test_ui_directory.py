"""Local directory search, clicked through, and the error shown when a relay
speaks no protocol version this build does."""

import os

import pytest
from conftest import CommunityNode, new_core, run, run_meta, run_relay, wait_until
from test_e2e_community import WIZARD
from test_versions_abuse import fake_relay
from uidriver import App
from uiflows import create_profile, open_settings

from openarcanum import codes
from openarcanum.wire import invite_pb2 as ip

pytestmark = pytest.mark.gui


def test_local_directory_search_and_outdated_relay(tmp_path):
    with run_meta() as meta, run_relay(registry_urls=meta, heartbeat_interval="1s", public_url="{url}") as relay:
        node = CommunityNode(tmp_path / "c", [relay], [meta])
        node.start()
        try:

            async def main():
                owner, _ = await new_core(tmp_path / "owner", maintainers=[meta])
                wiz = dict(WIZARD, name="Sourdough Bakers", description="Wild yeast", tags="bread", visibility="public")
                await owner.claim(node.claim_code, wiz, "Olive")
                await wait_until(lambda: owner.directory_search("sourdough"), timeout=20)

                app = await App(tmp_path / "seeker", name="seeker", args=["--maintainer", meta]).start()
                try:
                    await create_profile(app)
                    # Settings → Network → "Search the directory on this device".
                    await open_settings(app)
                    await app.click(".settings nav .item", "Network")
                    await app.wait_text(".settings section .setting-row", "Search the directory on this device")
                    box = (
                        "[...document.querySelectorAll('.settings section .setting-row')]"
                        ".find((r) => r.textContent.includes('Search the directory on this device'))"
                        ".querySelector('input[type=checkbox]')"
                    )
                    await app.js(f"{box}.setAttribute('data-test', 'local-dir')")
                    await app.click("input[data-test=local-dir]")
                    await app.wait_js(f"{box}.checked", "toggle on")
                    await app.wait_text(".settings section .setting-row", "Saved")
                    await app.click(".settings .close")

                    # Find a community: results come from the copy on this device.
                    await app.click(".rail-add")
                    await app.click('.add-way[data-tab="find"]')
                    await app.fill(".add-page input[type=search]", "sour")
                    await app.button("Search")
                    await app.wait_text(".add-page .result b", "Sourdough Bakers")
                    await app.wait_text(".add-page .directory-source", "Searching on this device")
                    await app.wait_text(".add-page .directory-source", "1 communities")
                    await app.button("Refresh copy")
                    await app.wait_text(".add-page .directory-source", "downloaded just now")
                    await app.fill(".add-page input[type=search]", "knitting")
                    await app.button("Search")
                    await app.wait_text(".add-page .empty-note", "No public communities match.")

                    # An invite whose relay only speaks newer protocol
                    # versions: the join pane says to update OpenArcanum.
                    async with fake_relay([2, 3]) as (url, _):
                        inv = codes.encode_invite(
                            ip.Invite(
                                route_id=os.urandom(16),
                                owner_key=os.urandom(32),
                                server_key=os.urandom(32),
                                relays=[url],
                                name="Future",
                            )
                        )
                        await app.click('.add-way[data-tab="join"]')
                        await app.fill(".add-page textarea", inv)
                        await app.button("Continue")
                        await app.wait_text(".add-page .error", "Update OpenArcanum", timeout=60)
                    app.assert_no_errors()
                finally:
                    await app.stop()
                await owner.shutdown()

            run(main(), 240)
        finally:
            node.stop()


def test_directory_off_hides_find_a_community(tmp_path):
    """Every maintainer node in use has its public directory switched off
    (directory_enabled false): Add a community has no Find a community, and
    the setup form offers only private communities, with a note saying
    joining goes through invites. The page passes axe."""
    from test_accessibility import audit

    with run_meta(directory_enabled="false") as meta:

        async def main():
            app = await App(tmp_path / "me", name="me", args=["--maintainer", meta]).start()
            try:
                await create_profile(app)
                # The app learns it from the node (GET /v1/node).
                await app.wait_js(
                    "st.status && st.status.maintainers && st.status.maintainers.directory === false", "directory off"
                )
                await app.click(".rail-add")
                await app.wait_js("document.querySelector('.add-page')", "add page")
                ways = await app.texts(".add-way .add-way-label")
                assert "Find a community" not in ways and "Join with an invite" in ways, ways
                assert not await app.exists('.add-way[data-tab="find"]')
                await app.click('.add-way[data-tab="host"]')
                await app.wait_text(".add-page .no-directory-note", "Public listing isn't available")
                options = await app.call(
                    "() => [...document.querySelectorAll('.add-page select')]"
                    ".flatMap((s) => [...s.options].map((o) => o.value))"
                )
                assert "public" not in options, options
                problems = await audit(app, "add a community with no directory")
                assert not problems, "\n".join(problems)
            finally:
                await app.stop()

        run(main(), 120)
