"""Calls when something is missing or blocked, clicked through in real
client windows: no microphone, no camera, a refused microphone, a busy
one, and a relay whose UDP can't be reached. Chromium's fake devices
provide the media; for "no device" a page-level stand-in (DEVICES) hides
a kind from the test page, and the app's own media-permission policy
still decides every request that gets through; for "UDP blocked",
Chromium's switch that keeps WebRTC off UDP."""

import re

import pytest
from conftest import network, run
from test_ui_voice import create_voice_channel, debug, join_voice, media_env, tone, wait_media  # noqa: F401 (tone is a fixture)
from uidriver import App
from uiflows import claim, create_profile, join

pytestmark = [pytest.mark.gui, pytest.mark.timeout(460)]

# A computer without a microphone or camera (or one where the system
# refuses them): getUserMedia rejects for that kind with the error the
# engine gives (NotFoundError, NotAllowedError) and the device list leaves
# it out. Called again with None, the device "appears" (devicechange).
DEVICES = """
(audio, video) => {
  const md = navigator.mediaDevices;
  if (!window.__devices) {
    const gum = md.getUserMedia.bind(md);
    const list = md.enumerateDevices.bind(md);
    window.__devices = {};
    md.getUserMedia = async (c) => {
      for (const k of ['audio', 'video']) {
        const why = c && c[k] && window.__devices[k];
        const message = why === 'NotAllowedError' ? 'Permission denied' : 'Requested device not found';
        if (why) throw new DOMException(message, why);
      }
      return gum(c);
    };
    md.enumerateDevices = async () => (await list()).filter((d) =>
      !(window.__devices.audio && d.kind === 'audioinput') && !(window.__devices.video && d.kind === 'videoinput'));
  }
  const appeared = (window.__devices.audio && !audio) || (window.__devices.video && !video);
  window.__devices.audio = audio;
  window.__devices.video = video;
  if (appeared) md.dispatchEvent(new Event('devicechange'));
}
"""

HEARS_ONE = "d.remote.length === 1 && d.remote[0].audio && d.stats.decrypted > 50"


def test_join_without_microphone_or_camera(tmp_path, tone):  # noqa: F811
    """Without a microphone (and without a camera) joining works: Alice is in
    the call listening, hears Olive, and the voice bar says no microphone
    was found; turning on the camera says there's none and the call goes
    on. When a microphone appears, the call picks it up and Olive hears
    her. A refused microphone also leaves her in the call, listening, until
    Try again works."""
    with network(tmp_path) as (_meta, _relay, community):
        node = community()

        async def main():
            env = media_env(tone)
            olive = await App(tmp_path / "olive", env=env, name="olive").start()
            alice = await App(tmp_path / "alice", env=env, name="alice").start()
            try:
                for app in (olive, alice):
                    await create_profile(app)
                invite = await claim(olive, node.claim_code, "Quiet Town", "Olive")
                await join(alice, invite, "Alice")
                alice_key = await alice.js("st.states[st.cid].self.key")
                await create_voice_channel(olive, "Lounge")
                await alice.wait_text(".chan.voice .cname", "Lounge")
                await alice.call(DEVICES, "NotFoundError", "NotFoundError")
                await join_voice(olive, "Lounge")
                await join_voice(alice, "Lounge")

                # In the call, listening: she hears Olive, and is told why
                # nothing she says goes out.
                await alice.wait_text(".voice-bar .voice-notice", "No microphone found")
                await alice.wait_text(".voice-bar .voice-notice", "You can still hear everyone")
                await wait_media(alice, "olive's audio", HEARS_ONE)
                assert (await debug(alice))["mic"] == "none"
                assert not (await debug(alice))["transmitting"]
                await alice.wait_text("#main .stage-status", "No microphone found")
                await olive.wait_js("voiceUI.state.participants.length === 2", "alice in the call")

                # No camera either: said in plain words; the call goes on.
                await alice.click('button[aria-label="Turn on camera"]')
                await alice.wait_text(".toast", "No camera found")
                assert await alice.js("voiceUI.call.debug().connection") == "connected"
                assert not await alice.js("!!voiceUI.call.local.camera")

                # A microphone is plugged in: the call starts sending it.
                await alice.call(DEVICES, None, "NotFoundError")
                await alice.wait_js("voiceUI.call.micState === 'ok'", "the microphone picked up", 20)
                await alice.wait_gone(".voice-bar .voice-notice")
                await olive.wait_js(f"voiceUI.speaking({alice_key!r})", "olive hears alice", 20)

                # Refused (the system's privacy settings): in the call,
                # listening, told so; Try again once it's allowed.
                await alice.click('button[aria-label="Leave call"]')
                await alice.wait_gone(".voice-bar")
                await alice.call(DEVICES, "NotAllowedError", "NotFoundError")
                await join_voice(alice, "Lounge")
                await alice.wait_text(".voice-bar .voice-notice", "isn't allowed to use the microphone")
                await wait_media(alice, "olive's audio again", HEARS_ONE)
                await alice.call(DEVICES, None, "NotFoundError")
                assert await alice.js("voiceUI.call.micState") == "denied", "a refusal isn't retried by itself"
                await alice.click(".voice-bar .voice-notice button", "Try again")
                await alice.wait_js("voiceUI.call.micState === 'ok'", "the microphone after Try again", 20)
                await olive.wait_js(f"voiceUI.speaking({alice_key!r})", "olive hears alice again", 20)
                for app in (olive, alice):
                    app.assert_local_only()
                    app.assert_no_errors()
            finally:
                await olive.stop()
                await alice.stop()

        run(main(), 400)


def test_choose_microphone_from_the_voice_bar(tmp_path, tone):  # noqa: F811
    """The default microphone can't be opened (another app holds it): the
    voice bar says so, and its Choose microphone lists the microphones
    there are; picking one starts it without leaving the call and saves it
    as the microphone setting."""
    with network(tmp_path) as (_meta, _relay, community):
        node = community()

        async def main():
            app = await App(tmp_path / "solo", env=media_env(tone), name="solo").start()
            try:
                await create_profile(app)
                await claim(app, node.claim_code, "Solo Town", "Sam")
                await create_voice_channel(app, "Lounge")
                # The default microphone is busy (the system allowed it, then
                # couldn't open it); a named one works.
                await app.js(
                    "(() => { const md = navigator.mediaDevices; const gum = md.getUserMedia.bind(md);"
                    " md.getUserMedia = async (c) => { const s = await gum(c);"
                    " if (c.audio && !(c.audio.deviceId && c.audio.deviceId.exact)) {"
                    " s.getTracks().forEach((x) => x.stop());"
                    " throw new DOMException('Could not start audio source', 'NotReadableError'); }"
                    " return s; }; })()"
                )
                await join_voice(app, "Lounge")
                await app.wait_text(".voice-bar .voice-notice", "Another app may be using it")
                await app.click(".voice-bar .voice-notice button", "Choose microphone")
                await app.wait_js("document.querySelectorAll('#ctx-menu .mi').length >= 3", "the microphone list")
                names = await app.texts("#ctx-menu .mi")
                device = next(n for n in names if n not in ("System default", "Voice & video settings…"))
                await app.click("#ctx-menu .mi", device)
                await app.wait_js("voiceUI.call.micState === 'ok'", "the chosen microphone", 20)
                await app.wait_gone(".voice-bar .voice-notice")
                await app.wait_js(
                    f"api('get_settings').then((d) => d.vault.voice_input === {device!r})", "the choice saved"
                )
                assert await app.js("voiceUI.call.mic.label") == device
                assert await app.js("voiceUI.state.active")

                # Alone in the channel, leave and join again: the new call
                # starts at the same key id under a new key and sends (a new
                # key is not a reused one), and the voice bar leaves
                # "Connecting…".
                await app.click('button[aria-label="Leave call"]')
                await app.wait_gone(".voice-bar")
                await join_voice(app, "Lounge")
                await app.wait_js("voiceUI.call && voiceUI.call.sframe.send", "a send key on the second call", 20)
                # Alone, nobody can hear: the relay says so, and nothing is
                # sent. With a listener (as the relay would say when someone
                # joins) the voice goes out, encrypted under the new key.
                await wait_media(app, "nothing sent alone", "d.watchers && !d.sending.audio")
                await app.js("voiceUI.call.onWatchers([{ kind: 'audio', receivers: 1, layers: [] }])")
                await wait_media(app, "frames going out", "d.stats.sentFrames.audio > 50 && d.stats.encrypted > 50")
                app.assert_no_errors()
            finally:
                await app.stop()

        run(main(), 200)


def test_call_says_when_udp_is_blocked(tmp_path, tone):  # noqa: F811
    """UDP to the relay is blocked (Chromium's own switch that stops WebRTC
    using UDP, as a firewall that drops it would): the call doesn't sit on
    "Connecting…" or rejoin over and over. The voice bar says calls need
    UDP, with the relay's media address to allow and what to try, offers
    Try again, Copy details and Dismiss, and the member leaves the call
    (nobody sees a ghost in it)."""
    with network(tmp_path) as (_meta, _relay, community):
        node = community()

        async def main():
            env = media_env(tone)
            env["QTWEBENGINE_CHROMIUM_FLAGS"] += " --force-webrtc-ip-handling-policy=disable_non_proxied_udp"
            app = await App(tmp_path / "solo", env=env, name="solo").start()
            try:
                await create_profile(app)
                await claim(app, node.claim_code, "Firewall Town", "Sam")
                await create_voice_channel(app, "Lounge")
                await app.click(".chan.voice .cname", "Lounge")
                await app.wait_text(".voice-bar", "Connecting", 20)
                await app.wait_text(".voice-bar.failed", "Calls need UDP", 40)
                text = await app.js("document.querySelector('.voice-bar.failed .voice-status').textContent")
                assert re.search(r"at \S+:\d+\.", text) and "firewall" in text and "VPN" in text, text
                await app.wait_text(".toast", "Calls need UDP")
                await app.wait_js("!voiceUI.state.active", "out of the call")
                await app.wait_js(
                    "!(voiceUI.rooms[st.cid] || []).some((r) => r.participants.length)", "nobody left in the room"
                )
                await app.click(".voice-bar.failed button", "Copy details")
                await app.wait_text(".toast", "Copied")
                # Try again takes the same path (and fails the same way).
                await app.click(".voice-bar.failed button", "Try again")
                await app.wait_text(".voice-bar", "Connecting", 20)
                await app.wait_text(".voice-bar.failed", "Calls need UDP", 40)
                await app.click(".voice-bar.failed button", "Dismiss")
                await app.wait_gone(".voice-bar")
                app.assert_no_errors()
            finally:
                await app.stop()

        run(main(), 240)
