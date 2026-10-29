"""Lists that should follow what happens, or stay as they are: the member
list follows people joining while it's open (chat.js), and the + button
opened from the DMs keeps the DM list beside the Add a community page
(chat.js renderSidebarNow)."""

import pytest
from conftest import network, run
from test_ui_dm import set_maintainer
from uidriver import App
from uiflows import claim, create_profile, join, open_members

pytestmark = pytest.mark.gui


def test_someone_who_joins_shows_up_in_the_open_member_list(tmp_path):
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            olive = await App(tmp_path / "olive", name="olive").start()
            alice = await App(tmp_path / "alice", name="alice").start()
            try:
                for app in (olive, alice):
                    await create_profile(app)
                    await set_maintainer(app, meta)
                invite = await claim(olive, node.claim_code, "Live Town", "Olive")
                await open_members(olive)
                names = "[...document.querySelectorAll('#members .member')].map((e) => e.getAttribute('aria-label'))"
                assert await olive.js(names) == ["Olive"]
                await olive.js("document.getElementById('members').dataset.mark = 'kept'; 0")
                await join(alice, invite, "Alice")
                await olive.wait_js(f"{names}.includes('Alice')", "Alice in Olive's open member list", 30)
                # Same view, no navigation.
                assert await olive.js("document.getElementById('members').dataset.mark") == "kept"

                # From the DMs, + opens Add a community with the DM list kept
                # beside it, not the last community's channels.
                await olive.click('.rail-item[title="Direct messages"]')
                await olive.wait_text(".side-head .name", "Direct messages")
                await olive.click(".rail-item.rail-add")
                await olive.wait_js("document.querySelector('.add-panel')", "the Add a community page")
                assert await olive.texts(".side-head .name") == ["Direct messages"]
                assert not await olive.exists("#sidebar .chan[data-channel]")
                await olive.key("Escape")
                await olive.wait_gone(".add-panel")
                assert await olive.texts(".side-head .name") == ["Direct messages"]
                for app in (olive, alice):
                    app.assert_no_errors()
            finally:
                await olive.stop()
                await alice.stop()

        run(main(), 180)
