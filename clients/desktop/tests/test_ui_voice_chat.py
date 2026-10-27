"""A voice channel's text chat in the right-hand panel (like Discord's text
in voice), clicked through in two real client windows."""

import pytest
from conftest import network, run
from test_ui_voice import create_voice_channel, join_voice, media_env, tone  # noqa: F401 (tone is a fixture)
from uidriver import App
from uiflows import claim, close_settings, community_section, create_profile, join, send_message, set_feature

pytestmark = [pytest.mark.gui, pytest.mark.timeout(460)]


def test_voice_channel_text_chat(tmp_path, tone):  # noqa: F811
    """New messages mark the voice channel unread for someone not in the
    call; its Chat button opens them (and clears the mark) with a composer;
    someone in the call opens the same chat from the stage. Joining works
    from the panel. The panel takes the member list's place and gives it
    back when closed; with voice channels turned off it's gone."""
    with network(tmp_path) as (_meta, _relay, community):
        node = community()

        async def main():
            env = media_env(tone)
            olive = await App(tmp_path / "olive", env=env, name="olive").start()
            alice = await App(tmp_path / "alice", env=env, name="alice").start()
            try:
                for app in (olive, alice):
                    await create_profile(app)
                invite = await claim(olive, node.claim_code, "Chat Town", "Olive")
                await join(alice, invite, "Alice")
                await create_voice_channel(olive, "Lounge")
                await alice.wait_text(".chan.voice .cname", "Lounge")

                # Olive joins the call and writes in its chat from the stage.
                await join_voice(olive, "Lounge")
                await olive.click('.main-head button[aria-label="Chat"]')
                await olive.wait_js(
                    "document.querySelector('#members.voice-chat #vc-composer-input')", "the chat panel"
                )
                await olive.wait_text("#members .vc-who", "In the call: Olive")
                await send_message(olive, "game in five, bring snacks", "#vc-composer-input")
                await olive.wait_text("#vc-messages .mtext", "game in five")
                assert (
                    await olive.js(
                        "document.querySelector('.main-head button[aria-pressed=true]').getAttribute('aria-label')"
                    )
                    == "Chat"
                )

                # Alice, not in the call, sees the voice channel unread.
                await alice.wait_js(
                    "document.querySelector('.chan.voice.unread .unread-dot')", "unread on the voice channel"
                )
                assert not await alice.js("voiceUI.state.active")
                # Opened from the keyboard: the Chat button, Enter.
                await alice.js("document.querySelector('.chan.voice .chat-btn').focus()")
                await alice.key("Enter")
                await alice.wait_text("#vc-messages .mtext", "game in five")
                await alice.wait_text("#members .vc-head .title", "Lounge")
                await alice.wait_text("#members .vc-who", "In the call: Olive")
                await alice.wait_js("!document.querySelector('.chan.voice.unread')", "unread mark gone")
                await alice.wait_js(
                    "document.activeElement && document.activeElement.id === 'vc-composer-input'",
                    "focus in the composer",
                )
                assert not await alice.js("voiceUI.state.active"), "opening the chat doesn't join the call"
                await alice.type("on my way")
                await alice.key("Enter")
                await olive.wait_text("#vc-messages .mtext", "on my way")
                await alice.wait_text("#vc-messages .mtext", "on my way")

                # Slowmode on the voice channel's chat, set in Community
                # settings → Channels; the server holds Alice to it.
                await community_section(olive, "Channels")
                await olive.click('.settings section .channel-row button[aria-label="Edit Lounge"]')
                await olive.wait_js("document.querySelector('.channel-dialog')", "channel dialog")
                assert not await olive.exists('.channel-dialog textarea[aria-label="Channel topic"]'), "no topic"
                await olive.select('.channel-dialog select[aria-label="Slowmode"]', "30 seconds")
                await olive.button("Save")
                await olive.wait_gone(".channel-dialog")
                await close_settings(olive)
                await alice.wait_text("#members .slowmode", "Slowmode is on", 30)
                await send_message(alice, "one more thing", "#vc-composer-input")
                await olive.wait_text("#vc-messages .mtext", "one more thing")
                await send_message(alice, "and another", "#vc-composer-input")
                await alice.wait_text(".toast", "slowmode is on")
                assert not await olive.exists("#vc-messages .mtext", "and another")

                # She joins from the panel; the panel stays.
                await alice.click("#members .vc-head button", "Join voice")
                await alice.wait_text(".voice-bar", "Voice connected", 40)
                await alice.wait_text("#members .vc-who", "Alice")
                assert await alice.exists("#vc-messages")
                # Close: the member list comes back, focus back on Chat.
                await alice.click('#members button[aria-label="Close chat"]')
                await alice.wait_gone("#members.voice-chat")
                assert await alice.js("document.activeElement.classList.contains('chat-btn')"), "focus back on Chat"
                # A text channel's composer is unaffected.
                await alice.click(".chan .cname", "general")
                await send_message(alice, "back in general")
                await alice.wait_text("#messages .mtext", "back in general")

                # Voice channels turned off: the chat panel goes with them.
                await olive.wait_text("#vc-messages .mtext", "on my way")
                await set_feature(olive, "voice", False)
                await close_settings(olive)
                await olive.wait_gone("#members.voice-chat")
                await olive.wait_gone(".chan.voice")
                for app in (olive, alice):
                    app.assert_local_only()
                    app.assert_no_errors()
            finally:
                await olive.stop()
                await alice.stop()

        run(main(), 400)
