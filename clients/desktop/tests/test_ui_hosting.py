"""Hosting a community on this computer, through the real UI (Linux, under
Xvfb with the stand-in tray of test_ui_tray.py): the Add a community page in
the center section with its four ways in; Host on this computer creates a
community that works; closing the window to the tray keeps serving it and
another member joins meanwhile; stopping hosting shows it offline (in the
rail and to the other member); quitting asks first, and the hosted server
stops with the app."""

import asyncio
import sys

import pytest
from conftest import binary, network, run, wait_until
from test_e2e_community import general
from test_ui_tray import Desktop, log_of, wait_for
from uidriver import App
from uiflows import create_profile, send_message

from openarcanum import hosting
from openarcanum.core import Core
from openarcanum.paths import Paths

pytestmark = [
    pytest.mark.skipif(not sys.platform.startswith("linux"), reason="Linux desktop bus and X11 test"),
    pytest.mark.gui,
]

ENV = {"OPENARCANUM_COMMUNITY_HEARTBEAT_INTERVAL": "1s", "OPENARCANUM_COMMUNITY_PUBLISH_INTERVAL": "2s"}


async def server_said(core: Core, cid: str, state: str, timeout: float = 60.0) -> dict:
    async def check():
        conn = core.conns.get(cid)
        if conn is None:
            return None
        await conn._check_status(force=True)
        st = conn.server_status
        return st if st and st["state"] == state else None

    return await wait_until(check, timeout, 0.5)


def test_host_on_this_computer_through_the_page_tray_and_quit(tmp_path):
    desk = Desktop(tmp_path)
    with network(tmp_path, community_offline_after="10s") as (meta, _relay, _community):

        async def main():
            home = tmp_path / "olive"
            olive = await App(
                home,
                name="olive",
                env={hosting.BINARY_ENV: str(binary("community")), **ENV},
                args=["--maintainer", meta],
                session_helper=desk.helper,
                session_ready=desk.ready,
            ).start()
            alice = None
            try:
                desk.connect()
                await desk.item()
                pid = olive.app_pid()
                await create_profile(olive)

                # The page takes the center section, with the four ways in.
                await olive.click(".rail-add")
                await olive.wait_js("document.querySelector('#main .add-page')", "the Add a community page")
                assert await olive.texts(".add-way .add-way-label") == [
                    "Join with an invite",
                    "Find a community",
                    "Host on this computer",
                    "Host on your own server",
                ]
                assert not await olive.exists(".modal")
                await olive.click('.add-way[data-tab="server"]')
                await olive.wait_text(".add-page .form-section h3", "1. Copy the community server")
                await olive.wait_js("document.querySelector('.add-page input[aria-label=\"Save to\"]').value", "path")
                # Copy the community server: the bundled program, saved where asked.
                target = tmp_path / "copied-community"
                await olive.fill('.add-page input[aria-label="Save to"]', str(target))
                await olive.button("Save the community server")
                await olive.wait_text("#toasts .toast", f"Saved to {target}.")
                assert target.read_bytes() == binary("community").read_bytes()

                # Host on this computer: no code to copy.
                await olive.click('.add-way[data-tab="host"]')
                await olive.wait_text(".add-page .form-section h3", "Set up your community")
                await olive.fill('.add-page input[placeholder="Community name"]', "Home Base")
                await olive.fill('.add-page input[placeholder="Your display name"]', "Olive")
                await olive.button("Create community")
                await olive.wait_js("document.querySelector('.invite-code .code-text')", "community ready", 90)
                invite = await olive.js("document.querySelector('.invite-code .code-text').textContent")
                await olive.button("Export backup…")
                await olive.button("Cancel")
                await olive.wait_gone("#modal-root .backdrop")
                await olive.wait_gone(".add-page")
                await olive.wait_js("document.querySelector('#composer-input')", "composer", 30)
                await send_message(olive, "hosted hello")
                await olive.wait_text("#messages .mtext", "hosted hello")
                await olive.click(".side-head")
                await olive.wait_text(".menu .mi-head", "Hosted on this computer: Running")
                await olive.key("Escape")
                hosted = await olive.call("api", "hosting_list")
                assert [(h["name"], h["state"]) for h in hosted] == [("Home Base", "running")]
                locations = {loc["id"]: loc for loc in await olive.call("api", "storage_locations")}
                assert locations["hosted"]["exists"] and locations["hosted"]["path"] == str(home / "data" / "hosted")

                # Closed to the tray, the computer keeps serving the community:
                # another member joins and reads it meanwhile.
                assert desk.x11("close", pid) == 1
                await wait_for("the window hidden", lambda: not desk.main_window(pid)["mapped"])
                alice = Core(Paths.under(tmp_path / "alice"))
                await alice.create_profile("alice passphrase 1234")
                await alice.set_setting("maintainers", [meta])
                cid = (await alice.join({"invite": invite}, "new", "Alice"))["community"]
                ch = await general(alice, cid)
                hist = await alice.history(cid, ch)
                assert "hosted hello" in [m["text"] for m in hist["messages"]]
                await alice.send(cid, ch, "hi from outside")
                desk.click_icon()
                await wait_for("the window shown", lambda: desk.main_window(pid)["mapped"])
                await olive.wait_text("#messages .mtext", "hi from outside")

                # Stop hosting: the community goes offline, for this window
                # and for the other member.
                await olive.click(".side-head")
                await olive.click(".menu .mi", "Stop hosting")
                await olive.wait_text(".modal h2", "Stop hosting Home Base?")
                await olive.button("Stop hosting")
                await olive.wait_text(".banner.hosted-banner", "hosting is stopped")
                off = await server_said(alice, cid, "offline")
                assert off["stopping"]
                await olive.wait_js(
                    "document.querySelector('.rail-item[data-community]').title.includes('Offline · last seen')",
                    "offline, last seen, in the rail",
                    60,
                )
                await olive.click(".banner.hosted-banner button", "Start hosting")
                await olive.wait_gone(".banner.hosted-banner", timeout=30)
                await server_said(alice, cid, "online")

                # Quitting asks first; Cancel keeps it running.
                desk.click_menu("Quit OpenArcanum")
                await olive.wait_text(".modal h2", "Quit OpenArcanum?")
                await olive.wait_text(".modal .quit-hosting", "This computer hosts 1 community")
                await olive.button("Cancel")
                await asyncio.sleep(1)
                assert olive.app_pid() == pid
                desk.click_menu("Quit OpenArcanum")
                await olive.wait_text(".modal h2", "Quit OpenArcanum?")
                await olive.button("Quit anyway")
                await asyncio.to_thread(olive.wait_exit, pid, 30)
                # The hosted server stopped with the app and said so.
                gone = await server_said(alice, cid, "offline", 30)
                assert gone["stopping"]
                olive.assert_no_errors()
            finally:
                if alice is not None:
                    await alice.shutdown()
                await olive.stop()
            log = log_of(home)
            assert "exiting with status 0" in log and "Traceback" not in log, log[-3000:]
            assert "hosting: " in log and invite not in log

        run(main(), 400)
