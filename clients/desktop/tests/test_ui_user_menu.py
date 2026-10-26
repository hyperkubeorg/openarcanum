"""A person's menu (ui/js/people.js), the same wherever a person is shown:
right-click a member-list row, a message's author, a mention, a DM row or a
Community Admin member row, as a member and as an admin. Message opens a DM,
Mention fills the message box, and Time out works from the menu.

Screenshots of the menus go to $OPENARCANUM_UI_SHOTS when it's set (a folder),
else next to the test's profiles."""

import asyncio
import os
from pathlib import Path

import pytest
from conftest import network, run
from test_ui_dm import check_mail, open_dms, two_people
from uidriver import App
from uiflows import close_settings, community_section, open_members, send_message

pytestmark = [pytest.mark.gui, pytest.mark.timeout(400)]

MENU = "[...document.querySelectorAll('#ctx-menu .mi')].map((e) => e.getAttribute('aria-label'))"
DANGER = "[...document.querySelectorAll('#ctx-menu .mi.danger')].map((e) => e.getAttribute('aria-label'))"
MEMBER_ONLY = ["View profile", "Message", "Mention", "Add as contact", "Copy user ID"]
MODERATION = ["Change nickname…", "Roles…", "Time out…", "Kick…", "Ban…"]


async def menu_of(app: App, sel: str, text: str | None = None, nth: int = 0) -> list[str]:
    """Right-click something and read the menu's entries."""
    await app.right_click(sel, text, nth)
    await app.wait_js("document.querySelector('#ctx-menu .mi')", f"the menu of {sel}")
    return await app.js(MENU)


async def close_menu(app: App) -> None:
    await app.key("Escape")
    await app.wait_gone("#ctx-menu")


async def shot(app: App, tmp_path: Path, name: str) -> None:
    folder = Path(os.environ.get("OPENARCANUM_UI_SHOTS") or tmp_path)
    folder.mkdir(parents=True, exist_ok=True)
    await app.screenshot(folder / f"user-menu-{name}.png")


def test_user_menu_everywhere(tmp_path):
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            # Olive owns the community (every permission); Alice is a member.
            olive, alice = await two_people(tmp_path, meta, node)
            try:
                await send_message(olive, "hello from olive")
                await send_message(alice, "hello from alice")
                await alice.wait_text("#messages .mtext", "hello from olive")
                await olive.wait_text("#messages .mtext", "hello from alice")

                # A member: the member list's row for someone else has the
                # ways to reach them, and nothing that manages them.
                await open_members(alice)
                got = await menu_of(alice, "#members .member", "Olive")
                await shot(alice, tmp_path, "member-list-as-member")
                assert got == MEMBER_ONLY, got
                await close_menu(alice)
                # A message's author: the same menu.
                got = await menu_of(alice, "#messages .msg .author", "Olive")
                assert got == MEMBER_ONLY, got
                await close_menu(alice)
                # Yourself: your profile here, no Message.
                got = await menu_of(alice, "#members .member", "Alice")
                assert got == ["View profile", "Copy user ID", "Edit your profile here"], got
                await close_menu(alice)

                # The admin: the same entries, then the moderation ones (the
                # destructive ones red).
                await open_members(olive)
                got = await menu_of(olive, "#members .member", "Alice")
                await shot(olive, tmp_path, "member-list-as-admin")
                assert got == MEMBER_ONLY + MODERATION, got
                assert await olive.js(DANGER) == ["Time out…", "Kick…", "Ban…"]
                await close_menu(olive)
                got = await menu_of(olive, "#messages .msg .author", "Alice")
                await shot(olive, tmp_path, "message-author-as-admin")
                assert got == MEMBER_ONLY + MODERATION, got

                # Mention puts @Alice in the message box.
                await olive.click("#ctx-menu .mi", "Mention")
                await olive.wait_gone("#ctx-menu")
                await olive.wait_js(
                    "document.querySelector('#composer-input').value === '@Alice '", "the mention typed"
                )
                assert await olive.js("document.activeElement.id") == "composer-input"
                await olive.key("Enter")
                await olive.wait_js("document.querySelector('#messages .mtext .mention')", "the sent mention")
                # The mention itself has her menu too.
                got = await menu_of(olive, "#messages .mtext .mention", "@Alice")
                assert got == MEMBER_ONLY + MODERATION, got

                # Time out from the menu: the usual dialog, and it works.
                await olive.click("#ctx-menu .mi", "Time out…")
                await olive.wait_text(".modal h2", "Time out Alice")
                await olive.select('.modal select[aria-label="Duration"]', "1 hour")
                await olive.button("Time out")
                await olive.wait_text("#toasts .toast", "Alice is timed out.")
                await olive.wait_text("#members .member", "timed out")
                await alice.wait_text(".composer-locked", "You're timed out", 30)
                got = await menu_of(olive, "#members .member", "Alice")
                assert "Remove timeout" in got and "Time out…" not in got, got
                await olive.click("#ctx-menu .mi", "Remove timeout")
                await olive.wait_text("#toasts .toast", "timeout was removed")

                # Community Admin's Members rows have the same menu, without
                # Mention (there's no message box there).
                await community_section(olive, "Members")
                await olive.wait_text(".settings .member-admin-row b", "Alice")
                row = await olive.js(
                    "[...document.querySelectorAll('.settings .member-admin-row')]"
                    ".findIndex((r) => r.querySelector('b').textContent === 'Alice')"
                )
                got = await menu_of(olive, ".settings .member-admin-row", None, row)
                await shot(olive, tmp_path, "community-admin-row")
                assert got == ["View profile", "Message", "Add as contact", "Copy user ID", *MODERATION], got
                await close_menu(olive)
                await close_settings(olive)

                # Message: Alice opens a DM with Olive from her message.
                await menu_of(alice, "#messages .msg .author", "Olive")
                await alice.click("#ctx-menu .mi", "Message")
                await alice.wait_js("document.querySelector('#dm-input')", "the DM composer")
                await alice.click("#dm-input")
                await alice.type("hello olive, privately")
                await alice.key("Enter")
                await alice.wait_text("#dm-messages .mtext", "hello olive, privately")
                # The open conversation's row: Olive, without Message (it's open).
                got = await menu_of(alice, "button.chan[data-dm]")
                assert got == ["View profile", "Add as contact", "Safety number", "Copy user ID", "Block"], got
                await close_menu(alice)

                # Olive's DM list: the request's row is Alice, with Message.
                await open_dms(olive)
                for _ in range(40):
                    await check_mail(olive)
                    if await olive.exists("button.chan[data-dm]"):
                        break
                    await asyncio.sleep(0.4)
                got = await menu_of(olive, "button.chan[data-dm]")
                await shot(olive, tmp_path, "dm-row")
                assert got == ["View profile", "Message", "Add as contact", "Safety number", "Copy user ID", "Block"], (
                    got
                )
                await olive.click("#ctx-menu .mi", "Message")
                await olive.wait_text("#dm-messages .mtext", "hello olive, privately")
                await olive.wait_text(".banner", "wants to message you")
                # A DM's author has the same menu (the conversation is open:
                # no Message).
                got = await menu_of(olive, "#dm-messages .msg .author", "Alice")
                assert got == ["View profile", "Add as contact", "Safety number", "Copy user ID", "Block"], got
                await close_menu(olive)
                assert not olive.errors and not alice.errors, (olive.errors, alice.errors)
            finally:
                await olive.stop()
                await alice.stop()

        run(main(), 380)
