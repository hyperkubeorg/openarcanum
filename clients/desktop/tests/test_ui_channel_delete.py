"""Deleting channels from their right-click menu (chat.js channelMenu):
text and voice channels alike, for members who can manage channels; the
open channel moves to another one, and members who can't don't see it."""

import pytest
from conftest import network, run
from test_ui_voice import create_voice_channel
from uidriver import App
from uiflows import claim, create_profile, join

pytestmark = pytest.mark.gui

MENU = "[...document.querySelectorAll('#ctx-menu .mi')].map((e) => e.getAttribute('aria-label'))"


def test_delete_text_and_voice_channels_from_their_menu(tmp_path):
    with network(tmp_path) as (_meta, _relay, community):
        node = community()

        async def main():
            olive = await App(tmp_path / "olive", name="olive").start()
            alice = await App(tmp_path / "alice", name="alice").start()
            try:
                for app in (olive, alice):
                    await create_profile(app)
                invite = await claim(olive, node.claim_code, "Delete Town", "Olive")
                await join(alice, invite, "Alice")
                await olive.click('button[aria-label="Create text channel"]')
                await olive.fill(".modal input", "scratch")
                await olive.button("Create")
                await olive.wait_text(".chan .cname", "scratch")
                await create_voice_channel(olive, "Lounge")

                # A member without Manage channels has no Delete.
                await alice.wait_text(".chan.voice .cname", "Lounge")
                await alice.right_click(".chan .cname", "scratch")
                await alice.wait_js("document.querySelector('#ctx-menu .mi')", "alice's channel menu")
                assert not [x for x in await alice.js(MENU) if x.startswith("Delete channel")]
                await alice.key("Escape")

                # The open text channel: deleted, and the view moves on.
                await olive.click(".chan .cname", "scratch")
                await olive.right_click(".chan .cname", "scratch")
                await olive.click("#ctx-menu .mi", "Delete channel…")
                await olive.wait_text(".modal h2", "Delete channel?")
                await olive.click(".modal-foot button", "Delete")
                await olive.wait_gone(".chan .cname", "scratch")
                await alice.wait_gone(".chan .cname", "scratch")
                await olive.wait_js("document.querySelector('#composer-input')", "another channel open")

                # A voice channel, the same way.
                await olive.right_click(".chan.voice .cname", "Lounge")
                await olive.click("#ctx-menu .mi", "Delete voice channel…")
                await olive.wait_text(".modal h2", "Delete voice channel?")
                await olive.click(".modal-foot button", "Delete")
                await olive.wait_gone(".chan.voice .cname", "Lounge")
                await alice.wait_gone(".chan.voice .cname", "Lounge")
                for app in (olive, alice):
                    app.assert_no_errors()
            finally:
                await olive.stop()
                await alice.stop()

        run(main(), 240)
