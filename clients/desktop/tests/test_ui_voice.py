"""Voice and video calls, clicked through in two real client windows with
Chromium's fake camera and a tone-burst microphone. Only the fake devices
are faked: permissions go through the app's own policy (no fake permission
UI) and screen sharing through the app's own picker."""

import asyncio
import sys

import pytest
from conftest import ENGINE_TEST_FLAGS, fake_media_flags, network, run
from uidriver import App, UIError
from uiflows import (
    FAKE_SCREEN,
    assign_role,
    claim,
    close_settings,
    create_profile,
    create_role,
    join,
    open_settings,
    set_feature,
    toggle_role_perms,
)

pytestmark = [pytest.mark.gui, pytest.mark.timeout(460)]


@pytest.fixture(scope="module")
def tone(tmp_path_factory):
    sys.path.insert(0, str(__import__("pathlib").Path(__file__).parent / "webengine"))
    import callharness

    path = tmp_path_factory.mktemp("media") / "tone.wav"
    callharness.tone_bursts(path)
    return path


def media_env(tone) -> dict:
    return {"QTWEBENGINE_CHROMIUM_FLAGS": (f"{fake_media_flags(tone)} {ENGINE_TEST_FLAGS}")}


async def create_voice_channel(app: App, name: str) -> None:
    for attempt in range(3):
        await app.click('button[aria-label="Create voice channel"]')
        try:
            await app.wait_js("document.querySelector('.modal input')", "the new channel dialog", 5)
            break
        except UIError:
            if attempt == 2:
                raise
    await app.fill(".modal input", name)
    await app.button("Create")
    await app.wait_text(".chan.voice .cname", name)


async def join_voice(app: App, name: str) -> None:
    await app.click(".chan.voice .cname", name)
    await app.wait_text(".voice-bar", "Voice connected", 40)


def debug(app: App):
    return app.js("voiceUI.call ? voiceUI.call.debug() : null")


async def wait_media(app: App, what: str, cond: str, timeout: float = 40) -> None:
    try:
        await app.wait_js(
            f"voiceUI.call && (() => {{ const d = voiceUI.call.debug(); return {cond}; }})()", what, timeout
        )
    except UIError as e:
        # The call's own view of its media, so a failure says why.
        raise UIError(f"{e}; call state: {await debug(app)!r}") from None


async def voice_user_menu(app: App, name: str) -> None:
    await app.hover(".voice-user .spacer", name)
    await app.click(f'.voice-user button[aria-label="Options for {name}"]')


def test_voice_call_click_through(tmp_path, tone):
    with network(tmp_path) as (_meta, _relay, community):
        node = community()

        async def main():
            env = media_env(tone)
            olive = await App(tmp_path / "olive", env=env, name="olive").start()
            alice = await App(tmp_path / "alice", env=env, name="alice").start()
            try:
                for app in (olive, alice):
                    await create_profile(app)
                invite = await claim(olive, node.claim_code, "Voice Town", "Olive")
                await join(alice, invite, "Alice")
                await create_voice_channel(olive, "Lounge")
                await create_voice_channel(olive, "Stage")
                await alice.wait_text(".chan.voice .cname", "Stage")

                # Both join by clicking the channel; each hears the other.
                await join_voice(olive, "Lounge")
                await alice.wait_text(".voice-user .spacer", "Olive")  # rooms are visible before joining
                await join_voice(alice, "Lounge")
                for app in (olive, alice):
                    await wait_media(
                        app,
                        "remote audio decrypting",
                        "d.remote.length === 1 && d.remote[0].audio && d.stats.decrypted > 50",
                    )
                    await app.wait_js("document.querySelector('.stage .tile')", "the call stage")
                # Speaking indicators follow the tone bursts.
                await olive.wait_js(
                    "document.querySelector('.voice-user.speaking[data-voice-member]')", "a speaking indicator", 20
                )

                # Alice turns her camera on: it takes her place on Olive's
                # stage, and Olive clicks it to watch.
                await alice.click('button[aria-label="Turn on camera"]')
                await olive.click(".stage .video-tile.unwatched .tile-main", "Watch Alice's camera")
                await olive.wait_js(
                    "[...document.querySelectorAll('.stage .video-tile video')].some((v) => v.videoWidth > 0)",
                    "alice's video on olive's stage",
                    40,
                )
                await olive.wait_text(".voice-user .spacer", "Alice")
                await olive.wait_js("document.querySelector('.voice-user .flag[title=\"Camera on\"]')", "camera flag")
                # Camera off again: the tile goes back to her avatar.
                await alice.click('button[aria-label="Turn off camera"]')
                await olive.wait_js(
                    "!document.querySelector('.voice-user .flag[title=\"Camera on\"]')", "camera flag gone"
                )

                # Alice switches microphone mid-call in Settings: the call
                # carries on with the new device.
                await open_settings(alice)
                await alice.click(".settings nav .item", "Voice & video")
                # The device lists come once the engine has answered.
                await alice.wait_js(
                    "document.querySelectorAll('.settings section select[data-devices=audioinput] option').length >= 3",
                    "the fake microphones listed",
                )
                mic = ".settings section select[data-devices=audioinput]"
                other = await alice.js(f"document.querySelector('{mic}').options[2].textContent")
                assert other, "the fake device list has a second microphone"
                await alice.select(mic, other)
                await alice.wait_text(".settings section .saved", "Saved")
                await alice.click(".settings .close")
                await alice.wait_js(
                    f"voiceUI.call.mic && voiceUI.call.mic.label === {other!r}", "the new microphone in use", 20
                )
                before = (await debug(olive))["stats"]["decrypted"]
                await olive.wait_js(
                    f"voiceUI.call.debug().stats.decrypted > {before + 50}", "olive still hearing alice"
                )

                # Alice mutes: nothing goes out, and Olive sees it.
                await alice.click('button[aria-label="Mute"]')
                await alice.wait_js("!voiceUI.call.debug().transmitting", "alice not transmitting")
                await olive.wait_js("document.querySelector('.voice-user .flag[title=\"Muted\"]')", "muted flag")
                await alice.click('button[aria-label="Unmute"]')

                # Olive (owner) shares her screen through the app's own picker:
                # the engine waits while she picks. Where the system can capture
                # the screen (desktops; not Xvfb), Alice sees it; otherwise the
                # app says so and the call goes on. The screen media path itself
                # is covered with the fake capture source in test_calls.py.
                await olive.click('button[aria-label="Share your screen"]')
                await olive.wait_text(".modal h2", "Share your screen")
                await olive.click(".modal button", "Screen: Entire screen")
                await olive.wait_gone(".modal h2", "Share your screen")
                await olive.wait_js(
                    "(voiceUI.call && voiceUI.call.local.screen) || [...document.querySelectorAll('.toast')]"
                    '.some((t) => t.textContent.includes("couldn\'t capture that screen"))',
                    "the screen shared, or the capture failure reported",
                    20,
                )
                if await olive.js("!!voiceUI.call.local.screen"):
                    await alice.click(".stage .video-tile.screen .tile-main", "Watch Olive's screen")
                    await alice.wait_js(
                        "[...document.querySelectorAll('.stage .video-tile.screen video')]"
                        ".some((v) => v.videoWidth > 0)",
                        "olive's screen on alice's stage",
                        40,
                    )
                    await olive.click('button[aria-label="Stop sharing"]')
                else:
                    print("\nscreen capture isn't available on this display; the app reported it")
                await olive.wait_js("voiceUI.call.debug().connection === 'connected'", "call still up")

                # Per-user volume, on Olive's devices only.
                await voice_user_menu(olive, "Alice")
                # The slider is in the menu itself, no dialog.
                await olive.js(
                    "(() => { const s = document.querySelector('#ctx-menu .mi-range input[type=range]');"
                    " s.value = '50'; s.dispatchEvent(new Event('input')); s.dispatchEvent(new Event('change')); })()"
                )
                assert await olive.js("document.querySelector('#ctx-menu .mi-range-value').textContent") == "50%"
                await olive.wait_js(
                    "voiceUI.call.volumes[Object.keys(voiceUI.call.volumes)[0]] === 0.5", "volume applied"
                )
                for _ in range(100):  # kept when the slider is let go (saved in the background)
                    vols = await olive.js("api('get_settings').then((d) => d.vault.voice_volumes)")
                    if list((vols or {}).values()) == [0.5]:
                        break
                    await asyncio.sleep(0.1)
                assert list(vols.values()) == [0.5], vols
                await olive.key("Escape")

                # Priority speaker: Olive (the owner) may, but it's off until
                # she switches it on beside the camera; then Alice sees it.
                prio = '.voice-bar button[aria-label="Turn on priority speaker"]'
                assert await olive.js(f"document.querySelector('{prio}').getAttribute('aria-pressed')") == "false"
                assert not await alice.exists('.voice-bar button[aria-label="Turn on priority speaker"]')
                assert not await alice.exists('.voice-user .flag[title="Priority speaker"]')
                await olive.click(prio)
                await alice.wait_js(
                    "document.querySelector('.voice-user .flag[title=\"Priority speaker\"]')", "olive's priority flag"
                )
                await olive.click('.voice-bar button[aria-label="Turn off priority speaker"]')
                await alice.wait_gone('.voice-user .flag[title="Priority speaker"]')

                # Server mute: Alice is told, everyone sees it, and the relay stops her audio.
                await voice_user_menu(olive, "Alice")
                await olive.click(".menu .mi", "Server mute")
                await alice.wait_text(".voice-bar", "server muted")
                await olive.wait_js(
                    "document.querySelector('.voice-user .flag[title=\"Server muted\"]')", "server muted flag"
                )
                await voice_user_menu(olive, "Alice")
                await olive.click(".menu .mi", "Server unmute")
                await alice.wait_js(
                    "!document.querySelector('.voice-bar').textContent.includes('server muted')", "unmuted"
                )

                # Screen share turned off and on for the community mid-call:
                # the relay hands Alice's receivers over again, and she keeps
                # hearing Olive without errors.
                await set_feature(olive, "screen_share", False)
                await set_feature(olive, "screen_share", True)
                await close_settings(olive)
                await asyncio.sleep(3)  # the relay renegotiates
                before = (await debug(alice))["stats"]["decrypted"]
                await alice.wait_js(
                    f"voiceUI.call.debug().stats.decrypted > {before + 50}", "alice still hearing olive"
                )
                alice.assert_no_errors()

                # Move Alice to Stage: her client follows.
                await voice_user_menu(olive, "Alice")
                await olive.click(".menu .mi", "Move to…")
                await olive.select(".modal select", "Stage")
                await olive.button("Move")
                await alice.wait_js(
                    "voiceUI.state.active && st.states[voiceUI.state.community].channels"
                    ".find((c) => c.id === voiceUI.state.channel).name === 'Stage'",
                    "alice in Stage",
                    40,
                )
                await alice.wait_text(".voice-bar", "Voice connected", 40)
                await olive.wait_js("voiceUI.state.participants.length === 1", "olive alone in Lounge")

                # Push to talk: Alice only sends while she holds the key.
                await open_settings(alice)
                await alice.click(".settings nav .item", "Voice & video")
                # The Input mode select (the one offering Push to talk).
                nth = await alice.js(
                    "[...document.querySelectorAll('.settings section select')]"
                    ".findIndex((s) => [...s.options].some((o) => o.text === 'Push to talk'))"
                )
                await alice.select(".settings section select", "Push to talk", nth=nth)
                await alice.wait_text(".settings section .saved", "Saved")
                await alice.click(".settings .close")
                await alice.wait_js("voiceUI.call.s.mode === 'ptt'", "ptt mode")
                await alice.js("document.activeElement && document.activeElement.blur()")
                assert not (await debug(alice))["transmitting"]
                await alice.send(
                    "Input.dispatchKeyEvent", type="keyDown", key=" ", code="Space", windowsVirtualKeyCode=32
                )
                await alice.wait_js("voiceUI.call.debug().transmitting", "transmitting while held")
                await alice.send(
                    "Input.dispatchKeyEvent", type="keyUp", key=" ", code="Space", windowsVirtualKeyCode=32
                )
                await alice.wait_js("!voiceUI.call.debug().transmitting", "silent when released")

                # Leaving: Olive's list shows Alice gone from Stage.
                await alice.click('button[aria-label="Leave call"]')
                await alice.wait_gone(".voice-bar")
                await olive.wait_js(
                    "[...document.querySelectorAll('.voice-user')].every((e) => !e.textContent.includes('Alice'))",
                    "alice gone",
                )
                for app in (olive, alice):
                    app.assert_local_only()
                    app.assert_no_errors()
            finally:
                await olive.stop()
                await alice.stop()

        run(main(), 400)


async def menu_items(app: App) -> list[dict]:
    return await app.js(
        "[...document.querySelectorAll('#ctx-menu .mi, #ctx-menu .mi-range input, #ctx-menu .mi-head')]"
        ".map((e) => ({ role: e.getAttribute('role'), label: e.getAttribute('aria-label') || e.textContent,"
        " checked: e.getAttribute('aria-checked') }))"
    )


def sending(app: App, kind: str) -> str:
    return f"voiceUI.call && voiceUI.call.debug().sending[{kind!r}]"


def test_voice_bar_menus_streams_and_mute_for_me(tmp_path, tone):
    """The call is run from the voice bar's icons, each with a menu of its
    options (no control row under the stage); a camera takes its owner's
    place on the stage and a shared screen is a block of its own marked
    Live, each received only once clicked to watch (and the sender only
    sends while someone watches); a watched video goes into focus and back;
    a stream's sound can be muted by whoever watches it; and anyone can
    mute someone for themselves only, which also stops that voice reaching
    them (and being sent, when nobody else listens)."""
    with network(tmp_path) as (_meta, _relay, community):
        node = community()

        async def main():
            env = media_env(tone)
            olive = await App(tmp_path / "olive", env=env, name="olive").start()
            alice = await App(tmp_path / "alice", env=env, name="alice").start()
            try:
                for app in (olive, alice):
                    await create_profile(app)
                invite = await claim(olive, node.claim_code, "Stream Town", "Olive")
                await join(alice, invite, "Alice")
                await create_voice_channel(olive, "Lounge")
                await alice.wait_text(".chan.voice .cname", "Lounge")
                await join_voice(olive, "Lounge")
                await join_voice(alice, "Lounge")
                for app in (olive, alice):
                    await wait_media(
                        app, "remote audio", "d.remote.length === 1 && d.remote[0].audio && d.stats.decrypted > 50"
                    )
                alice_key = await alice.js("st.states[st.cid].self.key")

                # The call's controls sit right under the stage (its own row),
                # and the voice bar has them too; the bar's top has no line.
                assert await olive.exists('.stage-controls-host .stage-controls[aria-label="Call controls"]')
                assert await olive.js(
                    "document.querySelector('.stage').nextElementSibling.classList.contains('stage-controls-host')"
                )
                assert await olive.js("getComputedStyle(document.querySelector('.voice-bar')).borderTopWidth") == "0px"
                for label in (
                    "Mute",
                    "Deafen",
                    "Turn on camera",
                    "Share your screen",
                    "Voice & video settings",
                    "Leave call",
                ):
                    assert await olive.exists(f'.voice-bar button[aria-label="{label}"]'), label

                # The microphone's menu, from the keyboard: its arrow, Enter.
                await olive.js("document.querySelector('.voice-bar button[aria-label=\"Microphone options\"]').focus()")
                await olive.key("Enter")
                await olive.wait_js("document.querySelector('#ctx-menu')", "the microphone menu")
                items = await menu_items(olive)
                labels = [i["label"] for i in items]
                for want in ("Voice activity sensitivity", "Input device", "System default", "Input mode"):
                    assert want in labels, labels
                modes = [
                    (i["label"], i["checked"])
                    for i in items
                    if i["role"] == "menuitemradio" and i["label"] in ("Voice activity", "Studio", "Push to talk")
                ]
                assert modes == [("Voice activity", "true"), ("Studio", "false"), ("Push to talk", "false")], modes
                assert await olive.js("document.activeElement.closest('#ctx-menu') !== null"), "focus in the menu"
                await olive.key("Escape")
                await olive.wait_gone("#ctx-menu")
                assert await olive.js("document.activeElement.getAttribute('aria-label')") == "Microphone options"
                # Studio: no gate and none of the engine's processing.
                await olive.click('.voice-bar button[aria-label="Microphone options"]')
                await olive.click("#ctx-menu .mi", "Studio")
                await olive.wait_js(
                    "voiceUI.call.mode() === 'studio' && voiceUI.call.debug().processing === false", "studio mode", 20
                )
                settings = await olive.js("voiceUI.call.mic.getSettings()")
                assert (
                    not settings["echoCancellation"]
                    and not settings["noiseSuppression"]
                    and not settings["autoGainControl"]
                ), settings
                assert await olive.js("voiceUI.call.debug().transmitting")
                await olive.wait_text(".voice-bar", "studio mode")
                assert (await olive.js("api('get_settings').then((d) => d.vault.voice_mode)")) == "studio"
                await olive.click('.voice-bar button[aria-label="Microphone options"]')
                await olive.click("#ctx-menu .mi", "Voice activity")
                await olive.wait_js(
                    "voiceUI.call.mode() === 'vad' && voiceUI.call.debug().processing === true",
                    "voice activity again",
                    20,
                )
                # The other menus.
                await olive.right_click('.voice-bar button[aria-label="Deafen"]')
                labels = [i["label"] for i in await menu_items(olive)]
                assert "Output volume" in labels and "Output device" in labels, labels
                await olive.key("Escape")
                await olive.click('.voice-bar button[aria-label="Camera options"]')
                labels = [i["label"] for i in await menu_items(olive)]
                assert "Turn on camera" in labels and "Camera" in labels, labels
                await olive.key("Escape")
                await olive.click('.voice-bar button[aria-label="Screen share options"]')
                items = await menu_items(olive)
                assert {"role": "menuitemcheckbox", "label": "Share sound", "checked": "true"} in items, items
                await olive.key("Escape")

                # Alice's camera takes her place on Olive's stage, not
                # watched: nothing is received, and Alice sends nothing.
                await alice.click('.voice-bar button[aria-label="Turn on camera"]')
                await olive.find(
                    '.stage .video-tile.unwatched[data-kind="camera"] .tile-main', "Watch Alice's camera", 0, 30
                )
                assert await olive.js(
                    "document.querySelector('.stage .video-tile.unwatched').textContent.includes('Click to watch')"
                )
                assert not await olive.exists(".stage .video-tile video")
                await alice.wait_js(f"{sending(alice, 'camera')} === false", "alice's camera not sent")
                assert await olive.js("voiceUI.call.layers.size") == 0
                # Clicked: watched and in focus; sent and shown.
                await olive.click(".stage .video-tile .tile-main", "Watch Alice's camera")
                await alice.wait_js(f"{sending(alice, 'camera')} === true", "alice's camera sent", 20)
                await olive.wait_js(
                    "[...document.querySelectorAll('.stage .video-tile video')].some((v) => v.videoWidth > 0)",
                    "alice's video",
                    40,
                )
                assert await olive.js("document.querySelector('.stage').classList.contains('has-focus')")
                # Click again: back among the others; again and Escape too.
                await olive.click(".stage .video-tile .tile-main", "Show Alice's camera with everyone")
                await olive.wait_js("!document.querySelector('.stage.has-focus')", "out of focus")
                await olive.click(".stage .video-tile .tile-main", "Focus on Alice's camera")
                await olive.wait_js("document.querySelector('.stage.has-focus')", "in focus")
                await olive.key("Escape")
                await olive.wait_js("!document.querySelector('.stage.has-focus')", "out of focus with Escape")
                # Stop watching: Alice stops sending.
                await olive.click('.stage .video-tile button[aria-label="Stop watching Alice\'s camera"]')
                await alice.wait_js(f"{sending(alice, 'camera')} === false", "alice's camera not sent again", 20)

                # Olive (the owner, who may) shares her screen with its sound:
                # Live in Alice's list and on its own block, received once
                # Alice watches it.
                await olive.js(FAKE_SCREEN)
                await olive.click('.voice-bar button[aria-label="Share your screen"]')
                await alice.wait_js(
                    "[...document.querySelectorAll('.voice-user')].some((e) => e.textContent.includes('Olive')"
                    " && e.querySelector('.live-badge'))",
                    "the Live badge in the list",
                    30,
                )
                await alice.wait_js(
                    "document.querySelector('.stage .video-tile.screen .live-badge')", "Live on the screen's block"
                )
                await olive.wait_js(f"{sending(olive, 'screen')} === false", "olive's screen not sent unwatched", 20)
                await alice.click(".stage .video-tile.screen .tile-main", "Watch Olive's screen")
                await alice.wait_js(
                    "[...document.querySelectorAll('.stage .video-tile.screen video')].some((v) => v.videoWidth > 0)",
                    "olive's screen",
                    40,
                )
                await alice.wait_js(
                    "voiceUI.call.debug().listen.screenAudio.length === 1", "alice asks for the screen's sound"
                )
                await olive.wait_js(f"{sending(olive, 'screen-audio')} === true", "the screen's sound sent", 20)
                # Alice mutes the stream: not played, not received, not sent.
                await alice.click('.stage .video-tile.screen button[aria-label="Mute stream"]')
                await alice.wait_js(
                    "document.querySelector('.stage .video-tile.screen button[aria-label=\"Unmute stream\"]')"
                    ".getAttribute('aria-pressed') === 'true'",
                    "stream muted",
                )
                assert await alice.js("voiceUI.call.debug().listen.screenAudio.length") == 0
                await olive.wait_js(f"{sending(olive, 'screen-audio')} === false", "the screen's sound not sent", 20)
                await olive.click('.voice-bar button[aria-label="Stop sharing"]')
                await alice.wait_gone(".voice-user .live-badge")

                # Mute for me (right-click Alice): Olive's device neither plays
                # nor receives her; Alice, heard by nobody, sends no voice.
                await olive.right_click(".voice-user .spacer", "Alice")
                items = await menu_items(olive)
                assert {"role": "menuitemcheckbox", "label": "Mute for me", "checked": "false"} in items, items
                await olive.click("#ctx-menu .mi", "Mute for me")
                await olive.wait_js(
                    "(() => { const r = voiceUI.call.remote.values().next().value;"
                    " return r && r.audio && r.audio.gain.gain.value === 0; })()",
                    "alice not played",
                )
                await olive.wait_js("voiceUI.call.debug().listen.noAudio.length === 1", "alice not received")
                await alice.wait_js(f"{sending(alice, 'audio')} === false", "alice's voice not sent", 20)
                await olive.wait_js(
                    "document.querySelector('.voice-user .flag[title=\"Muted for you\"]')", "muted-for-you flag"
                )
                assert not await alice.exists('.voice-user .flag[title="Server muted"]'), "it isn't a server mute"
                mutes = None
                for _ in range(100):
                    mutes = await olive.js("api('get_settings').then((d) => d.vault.voice_local_mutes)")
                    if mutes == [alice_key]:
                        break
                    await asyncio.sleep(0.1)
                assert mutes == [alice_key], mutes
                await olive.right_click(".voice-user .spacer", "Alice")
                assert {"role": "menuitemcheckbox", "label": "Mute for me", "checked": "true"} in await menu_items(
                    olive
                )
                await olive.click("#ctx-menu .mi", "Mute for me")
                await alice.wait_js(f"{sending(alice, 'audio')} === true", "alice's voice sent again", 20)
                await olive.wait_js(f"voiceUI.speaking({alice_key!r})", "olive hears alice again", 40)
                for app in (olive, alice):
                    app.assert_local_only()
                    app.assert_no_errors()
            finally:
                await olive.stop()
                await alice.stop()

        run(main(), 400)


SAMPLE_TX = """
(ms) => new Promise((done) => {
  const seen = { vad: false, tx: false, n: 0 };
  const t = setInterval(() => {
    const c = voiceUI.call;
    if (c) { seen.n++; seen.vad = seen.vad || c.vadOpen; seen.tx = seen.tx || c.transmitting(); }
  }, 25);
  setTimeout(() => { clearInterval(t); done(seen); }, ms);
})
"""

SAMPLE_SPEAKING = """
(member, ms) => new Promise((done) => {
  let speaking = false;
  const t = setInterval(() => { speaking = speaking || voiceUI.speaking(member); }, 25);
  setTimeout(() => { clearInterval(t); done(speaking); }, ms);
})
"""


async def space(app: App, kind: str) -> None:
    await app.send("Input.dispatchKeyEvent", type=kind, key=" ", code="Space", windowsVirtualKeyCode=32)


def test_without_use_voice_activity_only_push_to_talk_sends(tmp_path, tone):
    """Use Voice Activity is enforced by the client (media is end-to-end
    encrypted, so neither the server nor the relay can tell voice activity
    from push to talk; docs/threat-model.md "Calls"). The
    permission reaches the client in its voice session from the server: a
    member without it, in voice-activity mode, sends nothing while the
    microphone hears the tone, and sends while holding the push-to-talk key.
    Given back mid-call, voice activity works again."""
    with network(tmp_path) as (_meta, _relay, community):
        node = community()

        async def main():
            env = media_env(tone)
            olive = await App(tmp_path / "olive", env=env, name="olive").start()
            alice = await App(tmp_path / "alice", env=env, name="alice").start()
            try:
                for app in (olive, alice):
                    await create_profile(app)
                invite = await claim(olive, node.claim_code, "PTT Town", "Olive")
                await join(alice, invite, "Alice")
                alice_key = await alice.js("st.states[st.cid].self.key")
                # The owner takes Use Voice Activity away from @everyone.
                await toggle_role_perms(olive, "@everyone", ("use_voice_activity",))
                await close_settings(olive)
                await create_voice_channel(olive, "Lounge")
                await alice.wait_text(".chan.voice .cname", "Lounge")
                await join_voice(olive, "Lounge")
                await join_voice(alice, "Lounge")
                await wait_media(olive, "alice's audio track", "d.remote.length === 1 && d.remote[0].audio")
                # Alice's settings say voice activity; the call says otherwise.
                assert await alice.js("voiceUI.call.s.mode") == "vad"
                assert await alice.js("voiceUI.call.s.perms.vad") is False
                await alice.wait_text(".voice-bar", "push to talk only")
                await alice.js("document.activeElement && document.activeElement.blur()")

                # The tone opens the voice detector, but nothing is sent.
                seen = await alice.call(SAMPLE_TX, 4000)
                assert seen["n"] > 50 and seen["vad"] and not seen["tx"], seen
                assert not await olive.call(SAMPLE_SPEAKING, alice_key, 3000), "olive heard alice without the key"

                # Holding the key sends; Olive hears her speak.
                await space(alice, "keyDown")
                await alice.wait_js("voiceUI.call.debug().transmitting", "transmitting while held")
                await olive.wait_js(f"voiceUI.speaking({alice_key!r})", "olive hears alice while she holds the key", 20)
                await space(alice, "keyUp")
                await alice.wait_js("!voiceUI.call.debug().transmitting", "silent when released")

                # Given back mid-call: the new session turns voice activity on.
                await toggle_role_perms(olive, "@everyone", ("use_voice_activity",))
                await close_settings(olive)
                await alice.wait_js("voiceUI.call && voiceUI.call.s.perms.vad === true", "voice activity allowed", 40)
                await alice.wait_js(
                    "!document.querySelector('.voice-bar').textContent.includes('push to talk only')", "note gone"
                )
                seen = await alice.call(SAMPLE_TX, 4000)
                assert seen["tx"], seen
                await olive.wait_js(f"voiceUI.speaking({alice_key!r})", "olive hears alice by voice activity", 20)
                for app in (olive, alice):
                    app.assert_no_errors()
            finally:
                await olive.stop()
                await alice.stop()

        run(main(), 300)


# What this device plays, measured on the audio after the deafen stage (an
# analyser the test adds to the call's own output), and what it receives
# from the other participant before any volume stage.
PLAYED = (
    "(() => { const c = voiceUI.call; if (!c) return null;"
    " if (!window.__tap || window.__tap.node !== c.master) {"
    " const a = c.ctx.createAnalyser(); a.fftSize = 512; c.master.connect(a);"
    " window.__tap = { node: c.master, a, buf: new Float32Array(512) }; }"
    " const r = c.debug().remote[0];"
    " return { played: levelDb(window.__tap.a, window.__tap.buf), received: r ? r.level : -120,"
    " gain: c.master.gain.value }; })()"
)


async def loudest(app: App, seconds: float = 3.0) -> dict:
    """Loudest played and received levels over a few seconds (the tone
    microphone beeps every second)."""
    out = {"played": -120.0, "received": -120.0}
    end = asyncio.get_running_loop().time() + seconds
    while asyncio.get_running_loop().time() < end:
        s = await app.js(PLAYED)
        out["played"] = max(out["played"], s["played"])
        out["received"] = max(out["received"], s["received"])
        await asyncio.sleep(0.05)
    return out


def test_voice_deafen_moderation_and_priority_speaker(tmp_path, tone):
    """Self-deafen silences what this device plays (measured on the audio
    output) and tells others; server deafen stops the relay sending audio
    to the member; Priority Speaker given through Roles reaches everyone's
    page; Disconnect removes the member, who is told and can rejoin."""
    with network(tmp_path) as (_meta, _relay, community):
        node = community()

        async def main():
            env = media_env(tone)
            olive = await App(tmp_path / "olive", env=env, name="olive").start()
            alice = await App(tmp_path / "alice", env=env, name="alice").start()
            try:
                for app in (olive, alice):
                    await create_profile(app)
                invite = await claim(olive, node.claim_code, "Deaf Town", "Olive")
                await join(alice, invite, "Alice")
                await create_voice_channel(olive, "Lounge")
                await alice.wait_text(".chan.voice .cname", "Lounge")
                await join_voice(olive, "Lounge")
                await join_voice(alice, "Lounge")
                for app in (olive, alice):
                    await wait_media(
                        app,
                        "remote audio decrypting",
                        "d.remote.length === 1 && d.remote[0].audio && d.stats.decrypted > 50",
                    )
                # Alice plays Olive's voice.
                heard = await loudest(alice)
                assert heard["played"] > -40 and heard["received"] > -40, heard

                # Alice deafens herself: her device plays nothing, and she
                # stops sending. Nobody else can hear Olive, so Olive's voice
                # isn't sent at all (the relay tells her) and doesn't arrive.
                await alice.click('button[aria-label="Deafen"]')
                await alice.wait_js("voiceUI.call.selfDeaf && voiceUI.call.selfMute", "deafened (and muted)")
                await olive.wait_js(
                    "document.querySelector('.voice-user .flag[title=\"Deafened\"]')", "deafened flag at olive"
                )
                await alice.wait_js("!voiceUI.call.debug().transmitting", "alice not transmitting")
                await olive.wait_js("!voiceUI.call.debug().sending.audio", "olive's voice not sent to nobody")
                await asyncio.sleep(1.5)  # what was in flight plays out
                deaf = await loudest(alice)
                assert deaf["received"] == -120 and deaf["played"] == -120, deaf
                await alice.click('button[aria-label="Undeafen"]')
                await alice.wait_js("!voiceUI.call.selfDeaf", "undeafened")
                await olive.wait_js(
                    "!document.querySelector('.voice-user .flag[title=\"Deafened\"]')", "deafened flag gone"
                )
                back = await loudest(alice)
                assert back["played"] > -40, back

                # Server deafen: the relay stops sending Alice any audio.
                await voice_user_menu(olive, "Alice")
                await olive.click(".menu .mi", "Server deafen")
                await olive.wait_js(
                    "document.querySelector('.voice-user .flag[title=\"Server deafened\"]')", "server deafened flag"
                )
                await alice.wait_js(
                    "document.querySelector('.voice-user .flag[title=\"Server deafened\"]')",
                    "alice sees she is server deafened",
                )
                await asyncio.sleep(1.5)  # what was in flight plays out
                cut = await loudest(alice)
                assert cut["received"] == -120 and cut["played"] == -120, cut
                await voice_user_menu(olive, "Alice")
                await olive.click(".menu .mi", "Server undeafen")
                await olive.wait_js(
                    "!document.querySelector('.voice-user .flag[title=\"Server deafened\"]')", "server undeafened"
                )
                await alice.wait_js(
                    "(() => { const r = voiceUI.call.debug().remote[0]; return r && r.level > -40; })()",
                    "olive heard again",
                    20,
                )

                # Priority Speaker through Roles: a role with it, given to
                # Alice, gives her the toggle beside the camera, off; once
                # she switches it on everyone's page gets the flag (and
                # call.js ducks others while she speaks: test_calls.py
                # measures that).
                prio_alice = "voiceUI.call && voiceUI.call.s.participants.some((p) => p.name === 'Alice' && p.priority)"
                prio_on = '.voice-bar button[aria-label="Turn on priority speaker"]'
                assert not await olive.js(prio_alice), "alice has priority before the role"
                assert not await alice.exists(prio_on)
                await create_role(olive, "Host", perms=("priority_speaker",))
                await assign_role(olive, "Alice", "Host")
                await close_settings(olive)
                await alice.find(prio_on, None, 0, 30)
                assert await alice.js(f"document.querySelector('{prio_on}').getAttribute('aria-pressed')") == "false"
                await asyncio.sleep(1)
                assert not await olive.js(prio_alice), "priority on before alice switched it on"
                await alice.click(prio_on)
                await olive.wait_js(prio_alice, "alice's priority on olive's page")
                await alice.wait_js(prio_alice, "alice's priority on her own page")
                await olive.wait_js(
                    "[...document.querySelectorAll('.voice-user')].some((e) => e.textContent.includes('Alice')"
                    " && e.querySelector('.flag[title=\"Priority speaker\"]'))",
                    "priority flag on alice's row",
                )

                # Disconnect: Alice is told and leaves the call; she can join again.
                await voice_user_menu(olive, "Alice")
                await olive.click(".menu .mi", "Disconnect")
                await alice.wait_text(".toast", "A moderator disconnected you", 20)
                await alice.wait_gone(".voice-bar")
                await olive.wait_js("voiceUI.state.participants.length === 1", "olive alone")
                await join_voice(alice, "Lounge")
                await olive.wait_js("voiceUI.state.participants.length === 2", "alice back")
                for app in (olive, alice):
                    app.assert_local_only()
                    app.assert_no_errors()
            finally:
                await olive.stop()
                await alice.stop()

        run(main(), 400)


def test_voice_settings_devices_and_meter(tmp_path, tone):
    async def main():
        app = await App(tmp_path / "solo", env=media_env(tone), name="solo").start()
        try:
            await create_profile(app)
            await open_settings(app)
            await app.click(".settings nav .item", "Voice & video")
            # Fake devices are listed by name; the meter moves with the tone.
            await app.wait_js(
                "[...document.querySelectorAll('.settings section select option')]"
                ".some((o) => o.textContent.includes('Fake'))",
                "device names",
            )
            await app.button("Test microphone")
            await app.wait_js(
                "parseFloat(document.querySelector('.settings .level i').style.width) > 50", "the meter moving", 15
            )
            await app.button("Stop test")
            await app.click(".settings .close")
            await asyncio.sleep(0.2)
            app.assert_no_errors()
        finally:
            await app.stop()

    run(main(), 120)


@pytest.mark.parametrize("session", ["x11", "wayland"])
def test_global_push_to_talk_availability_in_settings(tmp_path, tone, session):
    """Settings → Voice & video says why "Push to talk in other windows"
    can't work in a Wayland session (even with XWayland's DISPLAY set, as
    under Xvfb here) and turns the switch off; in an X11 session (and on
    Windows and macOS) the switch is live and nothing is shown."""
    env = media_env(tone) | {
        "XDG_SESSION_TYPE": session,
        "WAYLAND_DISPLAY": "wayland-0" if session == "wayland" else "",
    }

    async def main():
        app = await App(tmp_path / "solo", env=env, name="solo").start()
        try:
            await create_profile(app)
            await open_settings(app)
            await app.click(".settings nav .item", "Voice & video")
            await app.wait_text(".settings section .setting-row", "Push to talk in other windows")
            box = (
                "[...document.querySelectorAll('.settings section .setting-row')]"
                ".find((r) => r.textContent.includes('Push to talk in other windows'))"
                ".querySelector('input[type=checkbox]')"
            )
            if session == "wayland" and sys.platform.startswith("linux"):
                await app.wait_text(".settings section .ptt-global-unavailable", "This is a Wayland session")
                assert await app.js(f"{box}.disabled")
            else:
                assert not await app.exists(".settings section .ptt-global-unavailable")
                assert not await app.js(f"{box}.disabled")
            await app.click(".settings .close")
            app.assert_no_errors()
        finally:
            await app.stop()

    run(main(), 120)


def test_call_moves_when_its_relay_dies(tmp_path, tone):
    """The relay carrying a call dies: the server moves the call to another
    relay that carries calls, both clients reconnect there on their own, and
    they hear each other again under a new key."""
    import contextlib

    from conftest import CommunityNode, kill_relay, run_meta, run_relay

    with contextlib.ExitStack() as stack:
        meta = stack.enter_context(run_meta(writes_per_minute="60000"))
        sessions = stack.enter_context(run_relay(calls="false"))
        r1 = stack.enter_context(run_relay())
        r2 = stack.enter_context(run_relay())
        node = CommunityNode(tmp_path / "c", [sessions, r1, r2], [meta])
        node.start()
        stack.callback(node.stop)

        async def main():
            env = media_env(tone)
            olive = await App(tmp_path / "olive", env=env, name="olive").start()
            alice = await App(tmp_path / "alice", env=env, name="alice").start()
            try:
                for app in (olive, alice):
                    await create_profile(app)
                invite = await claim(olive, node.claim_code, "Failover", "Olive")
                await join(alice, invite, "Alice")
                await create_voice_channel(olive, "Lounge")
                await alice.wait_text(".chan.voice .cname", "Lounge")
                await join_voice(olive, "Lounge")
                await join_voice(alice, "Lounge")
                for app in (olive, alice):
                    await wait_media(
                        app, "audio", "d.remote.length === 1 && d.remote[0].audio && d.stats.decrypted > 50"
                    )
                relay = await olive.js("voiceUI.state.relay")
                epoch = await olive.js("voiceUI.state.epoch")
                assert relay in (r1, r2), relay
                kill_relay(relay)
                try:
                    for app in (olive, alice):
                        moved = f"voiceUI.state.relay !== {relay!r} && voiceUI.state.epoch > {epoch}"
                        await app.wait_js(
                            f"voiceUI.state.relay && {moved}",
                            "the call on the other relay",
                            60,
                        )
                        await wait_media(
                            app,
                            "audio again",
                            "d.remote.length === 1 && d.remote[0].audio && d.stats.decrypted > 50",
                            60,
                        )
                        await app.wait_text(".voice-bar", "Voice connected", 30)
                except UIError as e:
                    # Both sides' view of the call: relay, epoch and keys.
                    views = [
                        (
                            a.name,
                            await a.js("({relay: voiceUI.state.relay, epoch: voiceUI.state.epoch})"),
                            await debug(a),
                        )
                        for a in (olive, alice)
                    ]
                    raise UIError(f"{e}; both: {views!r}") from None
            finally:
                await olive.stop()
                await alice.stop()

        run(main(), 400)
