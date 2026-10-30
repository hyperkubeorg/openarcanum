"""The community's rail icon shows what's happening in its voice channels
(app.js voiceBadge), seen from elsewhere (the DMs): one icon, bottom right,
the strongest of a screen share, then a camera, then people in voice; gone
when they leave."""

import pytest
from conftest import network, run
from test_ui_voice import create_voice_channel, join_voice, media_env, tone  # noqa: F401 (tone is a fixture)
from uidriver import App
from uiflows import FAKE_SCREEN, claim, create_profile, join

pytestmark = pytest.mark.gui

BADGE = "document.querySelector('.rail-item[data-community] .voice-badge')"


def test_rail_badge_follows_voice_camera_and_screen(tmp_path, tone):  # noqa: F811
    with network(tmp_path) as (_meta, _relay, community):
        node = community()

        async def main():
            env = media_env(tone)
            olive = await App(tmp_path / "olive", env=env, name="olive").start()
            alice = await App(tmp_path / "alice", env=env, name="alice").start()
            try:
                for app in (olive, alice):
                    await create_profile(app)
                invite = await claim(olive, node.claim_code, "Badge Town", "Olive")
                await join(alice, invite, "Alice")
                await create_voice_channel(olive, "Lounge")
                await alice.wait_text(".chan.voice .cname", "Lounge")

                # Alice looks at her DMs: the community is only on the rail.
                # Olive (the owner: screen sharing is hers by default) goes
                # into voice, turns her camera on, then shares her screen.
                await alice.click('.rail-item[title="Direct messages"]')
                assert not await alice.js(f"!!{BADGE}")

                async def badge(kind: str, what: str) -> None:
                    await alice.wait_js(f"{BADGE} && {BADGE}.classList.contains('{kind}')", what, 30)

                await join_voice(olive, "Lounge")
                await badge("voice", "the voice badge")
                title = await alice.js("document.querySelector('.rail-item[data-community]').title")
                assert "1 in voice" in title, title
                await olive.click('.voice-bar button[aria-label="Turn on camera"]')
                await badge("camera", "the camera badge")
                await olive.js(FAKE_SCREEN)
                await olive.click('.voice-bar button[aria-label="Share your screen"]')
                await badge("screen", "the screen badge")
                assert "sharing a screen" in await alice.js(
                    "document.querySelector('.rail-item[data-community]').title"
                )
                assert (
                    await alice.js("document.querySelectorAll('.rail-item[data-community] .voice-badge').length") == 1
                )
                await olive.click('button[aria-label="Leave call"]')
                await alice.wait_js(f"!{BADGE}", "no badge once she's gone", 30)
                for app in (olive, alice):
                    app.assert_no_errors()
            finally:
                await olive.stop()
                await alice.stop()

        run(main(), 240)
