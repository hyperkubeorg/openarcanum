"""Click-through UI tests in the real window (headless): trusted input events
through DevTools (uidriver.py), real relay and community server binaries."""

import asyncio

import pytest
from conftest import run, run_community, run_relay
from uidriver import App
from uiflows import claim, create_channel, create_profile, open_settings, send_message, unlock, walk_settings

pytestmark = pytest.mark.gui


def test_first_run_claim_chat_settings_lock(tmp_path):
    with run_relay() as relay, run_community(tmp_path, [relay]) as node:

        async def main():
            app = await App(tmp_path / "home", name="owner").start()
            try:
                await create_profile(app)
                await claim(app, node.claim_code, "UI Town", "Tester")
                await send_message(app, "hello from the ui")
                await app.wait_text(".mtext", "hello from the ui")

                # Theme changes apply live (dark is the default).
                assert await app.js("document.documentElement.dataset.theme") == "dark"
                await open_settings(app)
                await app.click(".settings nav .item", "Appearance")
                await app.select(".settings section select", "Light")
                await app.wait_js("document.documentElement.dataset.theme === 'light'", "light theme")
                await app.click(".settings .close")

                await open_settings(app)
                await walk_settings(app, "user settings")
                await open_settings(app, "community")
                await walk_settings(app, "community settings")

                # Create a channel from the sidebar.
                await app.click('.side-group button[title="Create text channel"]')
                await app.fill(".modal input", "ui-made")
                await app.button("Create")
                await app.wait_text(".chan .cname", "ui-made")

                # DMs view and back.
                await app.click('.rail-item[title="Direct messages"]')
                await app.wait_text(".side-head .name", "Direct messages")
                await app.click(".rail-item", nth=1)
                await app.wait_js("document.querySelector('#composer-input')", "back to community")

                # Lock and unlock.
                await app.click('.user-panel button[title="Lock now"]')
                await app.wait_js("document.querySelector('#lock input[type=password]')", "lock screen")
                await unlock(app)
                app.assert_no_errors()
            finally:
                await app.stop()
            log = (tmp_path / "home" / "log" / "client.log").read_text()
            # Logs never carry message content.
            assert "hello from the ui" not in log

        run(main(), 240)


def test_new_channel_keeps_typing_in_its_composer(tmp_path):
    """Creating a text channel opens it with the composer focused, and it
    stays focused: what the user types next goes into the new channel."""
    with run_relay() as relay, run_community(tmp_path, [relay]) as node:

        async def main():
            app = await App(tmp_path / "home", name="owner").start()
            try:
                await create_profile(app)
                await claim(app, node.claim_code, "Focus Town", "Tester")
                await create_channel(app, "fresh")
                await app.wait_js(
                    "(document.querySelector('.main-head .title') || {}).textContent === 'fresh'", "#fresh open"
                )
                seen = []
                for _ in range(15):
                    seen.append(await app.js("document.activeElement && document.activeElement.id"))
                    await asyncio.sleep(0.2)
                assert set(seen) == {"composer-input"}, seen
                await app.type("typed straight away")
                assert await app.js("document.querySelector('#composer-input').value") == "typed straight away"
                # Any state update re-renders the channel; the composer keeps
                # the keyboard and the cursor.
                await app.js("document.querySelector('#composer-input').setSelectionRange(5, 5)")
                await app.js("renderMain()")
                assert await app.js("document.activeElement.id") == "composer-input"
                assert await app.js("document.activeElement.selectionStart") == 5
                await app.type("X")
                assert await app.js("document.querySelector('#composer-input').value") == "typedX straight away"
            finally:
                await app.stop()

        run(main(), 180)
