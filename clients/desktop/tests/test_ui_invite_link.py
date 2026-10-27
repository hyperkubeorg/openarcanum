"""Opening an openarcanum://invite/ link: the app starts with the Join dialog
prefilled (after the profile is unlocked), and a later launch with another
link hands it to the running window instead of starting a second app."""

import asyncio
import os
import subprocess
import sys

import pytest
from conftest import run
from test_instance import LINK
from uidriver import App
from uiflows import create_profile

pytestmark = [pytest.mark.gui, pytest.mark.timeout(300)]

PREFILLED = "[...document.querySelectorAll('#main .add-page textarea')].map((e) => e.value)"


def test_invite_link_prefills_join_dialog_and_second_launch_hands_over(tmp_path):
    home = tmp_path / "home"
    second_link = LINK + "AA"

    async def main():
        app = await App(home, name="linked", args=[LINK]).start()
        try:
            # Locked (first run): the link waits until the profile is open.
            await app.wait_js("document.querySelector('#lock button')", "first-run screen", 60)
            assert await app.js(PREFILLED) == []
            await create_profile(app)
            await app.wait_js(f"{PREFILLED}.includes({LINK!r})", "Join dialog with the link", 60)
            assert (
                await app.js("document.querySelector('.add-page .add-way.sel .add-way-label').textContent")
                == "Join with an invite"
            )
            # Nothing happens until the user acts: no join was started.
            assert await app.exists("button", "Continue")

            # A second launch on the same profile (as the OS does for the next
            # link) hands the link over and exits; it never opens a window.
            env = {k: v for k, v in os.environ.items() if k not in ("DISPLAY", "WAYLAND_DISPLAY")}
            p = await asyncio.to_thread(
                subprocess.run,
                [sys.executable, "-m", "openarcanum", "--home", str(home), second_link],
                env=env,
                capture_output=True,
                text=True,
                timeout=60,
            )
            assert p.returncode == 0, p.stderr[-2000:]
            await app.wait_js(f"{PREFILLED}.includes({second_link!r})", "Join dialog with the second link", 60)

            # The user can still back out.
            await app.key("Escape")
            await app.key("Escape")
            await app.wait_gone("#modal-root .backdrop")
            app.assert_no_errors()
            app.assert_local_only()
        finally:
            await app.stop()
        log = (home / "log" / "client.log").read_text(encoding="utf-8")
        assert "another launch handed over an invite link" in log
        assert LINK not in log and second_link not in log

    run(main(), timeout=240)
