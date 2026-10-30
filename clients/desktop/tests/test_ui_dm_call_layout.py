"""A DM call's panel (dm.js, voice.js): the divider under it resizes it
(from the keyboard here) and keeps its height when messages come and go;
refreshing the conversation doesn't redraw unchanged messages (no
flicker); Hide chat gives the call the whole conversation; Videos only;
a video pops out into a window of its own; turning the camera off asks
first."""

import pytest
from conftest import network, run
from test_ui_dm import set_maintainer
from test_ui_stream_zoom import zoom_pan_minimap
from test_ui_voice import media_env, tone  # noqa: F401 (tone is a fixture)
from uidriver import App
from uiflows import claim, community_member_menu, create_profile, join, publish_card

pytestmark = pytest.mark.gui


def test_dm_call_panel_divider_hide_chat_videos_only_popout(tmp_path, tone):  # noqa: F811
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            olive = await App(tmp_path / "olive", name="olive").start()
            alice = await App(tmp_path / "alice", env=media_env(tone), name="alice").start()
            try:
                for app in (olive, alice):
                    await create_profile(app)
                    await set_maintainer(app, meta)
                invite = await claim(olive, node.claim_code, "Call Town", "Olive")
                await join(alice, invite, "Alice")
                await publish_card(olive)
                await publish_card(alice)
                await community_member_menu(alice, "Olive")
                await alice.click(".menu .mi", "Message")
                await alice.wait_js("document.querySelector('#dm-input')", "DM composer")
                await alice.click("#dm-input")
                await alice.type("hello olive")
                await alice.key("Enter")
                await alice.wait_text("#dm-messages .mtext", "hello olive")

                await alice.click('button[aria-label="Start a video call"]')
                await alice.wait_js(
                    "document.querySelector('#dm-call .stage .video-tile video')", "her own camera on the stage", 40
                )
                height = "document.getElementById('dm-call').offsetHeight"

                # The divider, from the keyboard: taller by two steps.
                before = await alice.js(height)
                await alice.js("document.querySelector('.dm-call-divider').focus()")
                await alice.key("ArrowDown")
                await alice.key("ArrowDown")
                after = await alice.js(height)
                assert after >= before + 40, (before, after)
                # A message sent (the conversation redraws) keeps the height.
                await alice.click("#dm-input")
                await alice.type("after resizing")
                await alice.key("Enter")
                await alice.wait_text("#dm-messages .mtext", "after resizing")
                assert abs(await alice.js(height) - after) <= 1

                # A refresh with nothing new draws no message again.
                await alice.js("document.querySelector('#dm-messages .msg').dataset.mark = 'kept'; 0")
                await alice.js("dmUI.reload().then(() => 0)")
                assert await alice.js("document.querySelector('#dm-messages .msg').dataset.mark") == "kept"

                # Hide chat: the call takes the conversation; Show chat brings it back.
                await alice.click('.stage-controls button[aria-label="Hide chat"]')
                await alice.wait_js(
                    "document.getElementById('dm-view').classList.contains('chat-hidden')", "chat hidden"
                )
                assert await alice.js("getComputedStyle(document.getElementById('dm-messages')).display") == "none"
                await alice.click('.stage-controls button[aria-label="Show chat"]')
                await alice.wait_js(
                    "!document.getElementById('dm-view').classList.contains('chat-hidden')", "chat back"
                )

                # Videos only: just video tiles on the stage.
                await alice.click('.stage-controls button[aria-label="Videos only"]')
                await alice.wait_js("voiceUI.videosOnly", "videos only on")
                assert await alice.js("document.querySelectorAll('#dm-call .stage .tile:not(.video-tile)').length") == 0
                await alice.click('.stage-controls button[aria-label="Show everyone"]')

                # Zoom, pan and the minimap, in a conversation's call too.
                await zoom_pan_minimap(alice, "document.querySelector('#dm-call .video-tile[data-kind=camera]')")

                # Pop out: a window of its own with the same stream.
                await alice.click('#dm-call .video-tile button[aria-label^="Pop out"]')
                popped = (
                    "[...voiceUI.popouts.values()].some((w) => !w.closed && w.document.querySelector('video')"
                    " && w.document.querySelector('video').srcObject)"
                )
                await alice.wait_js(popped, "the pop-out window with the video")

                # Turning the camera off asks; Cancel keeps it on.
                await alice.click('.voice-bar button[aria-label="Turn off camera"]')
                await alice.wait_text(".modal h2", "Turn off your camera?")
                await alice.click(".modal-foot button", "Cancel")
                assert await alice.js("!!voiceUI.call.local.camera")
                await alice.click('.voice-bar button[aria-label="Turn off camera"]')
                await alice.click(".modal-foot button", "Turn off")
                await alice.wait_js("!voiceUI.call.local.camera", "camera off")
                # Its pop-out closed with it.
                await alice.wait_js("voiceUI.popouts.size === 0", "pop-out closed")
                for app in (olive, alice):
                    app.assert_no_errors()
            finally:
                await olive.stop()
                await alice.stop()

        run(main(), 300)
