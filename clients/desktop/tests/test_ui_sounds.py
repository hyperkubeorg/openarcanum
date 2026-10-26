"""Sounds (clients/shared/ui/js/sounds.js) in two real client windows: a mention in
a channel Alice isn't reading asks for the "mention" sound, its Play button
in Settings → Notifications → Sounds plays it, and its switch turned off
stops it while plain messages keep theirs. The page announces each sound it
plays as an `app-sound` window event; the test listens for it (no audio
device is needed under Xvfb). The rules themselves are unit-tested in
tests/js/sounds.test.js."""

import asyncio

import pytest
from conftest import run, run_community, run_relay
from test_ui_messaging import two_members
from uiflows import close_settings, create_channel, open_channel, open_settings, send_message

pytestmark = pytest.mark.gui

RECORD = "window.__sounds = []; window.addEventListener('app-sound', (e) => window.__sounds.push(e.detail.name)); 0"


async def sounds(app) -> list[str]:
    return await app.js("window.__sounds.slice()")


def test_mention_sound_and_its_switch(tmp_path):
    with run_relay() as relay, run_community(tmp_path, [relay]) as node:

        async def main():
            olive, alice = await two_members(tmp_path, node)
            try:
                await alice.js(RECORD)
                await alice.wait_js("!!soundsUI.vault", "sound settings read")
                assert await alice.js("soundsUI.vault.sounds === true && soundsUI.vault.sound_volume === 50")
                # Alice reads #random; the mention arrives in #general.
                await create_channel(olive, "random")
                await open_channel(olive, "general")
                await alice.wait_text(".chan .cname", "random")
                await open_channel(alice, "random")
                await send_message(olive, "hey @Alice, a sound for you")
                await alice.wait_text(".chan .pill.mention", "@1")
                await alice.wait_js("window.__sounds.includes('mention')", "the mention sound")
                assert await sounds(alice) == ["mention"]

                # Settings → Notifications → Sounds: Play previews it; the
                # switch turns it off.
                await open_settings(alice)
                await alice.click(".settings nav .item", "Notifications")
                await alice.wait_text(".settings section .form-section h3", "Sounds")
                await alice.js("window.__sounds.length = 0; 0")
                await alice.click("button.sound-preview", "Play the sound: Mention")
                await alice.wait_js("window.__sounds.includes('mention')", "the preview")
                assert await alice.js("document.querySelector('input[data-sound=\"mention\"]').checked")
                await alice.click('input[data-sound="mention"]')
                await alice.wait_js(
                    "(soundsUI.vault.sounds_off || []).includes('mention')", "the Mention sound switched off"
                )
                await close_settings(alice)
                await open_channel(alice, "random")
                await alice.js("window.__sounds.length = 0; 0")

                # Another mention: no sound. A plain message still has its own.
                await send_message(olive, "again @Alice, quietly")
                await alice.wait_text(".chan .pill.mention", "@2")
                await asyncio.sleep(1.5)
                assert await sounds(alice) == []
                await send_message(olive, "just a plain line")
                await alice.wait_js("window.__sounds.includes('message')", "the message sound")
                assert await sounds(alice) == ["message"]

                # Reading the channel: nothing for what arrives there.
                await open_channel(alice, "general")
                await alice.js("window.__sounds.length = 0; 0")
                await asyncio.sleep(2.2)  # past the message sound's cooldown
                await send_message(olive, "while you watch")
                await alice.wait_text("#messages .msg .mtext", "while you watch")
                await asyncio.sleep(1)
                assert await alice.js("document.hasFocus()"), "the window isn't focused under the test"
                assert await sounds(alice) == []

                # Saved for this device: the switch is still off after reopening.
                await open_settings(alice)
                await alice.click(".settings nav .item", "Notifications")
                await alice.wait_js("document.querySelector('input[data-sound=\"mention\"]')", "the switches")
                assert not await alice.js("document.querySelector('input[data-sound=\"mention\"]').checked")
                await close_settings(alice)
                for app in (olive, alice):
                    app.assert_no_errors()
            finally:
                await olive.stop()
                await alice.stop()

        run(main())
