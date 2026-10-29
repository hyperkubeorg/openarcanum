"""Statuses clicked through in two real client windows (statuses.py,
ui/js/status.js): Olive sets her status in the picker from the user panel;
Alice sees it as a community member (the state, by default) and as a contact
(the custom text too, in her DM list); un-sharing the community in Settings →
Privacy & DMs makes members stop seeing it; Do not disturb stops Olive's
notifications while her badges still count.

Screenshots go to $OPENARCANUM_UI_SHOTS when it's set (a folder), else next to
the test's profiles."""

import asyncio
import os
from pathlib import Path

import pytest
from conftest import network, run
from test_ui_dm import check_mail, open_dms, two_people
from uidriver import App
from uiflows import close_settings, community_member_menu, open_members, open_settings, send_message

pytestmark = [pytest.mark.gui, pytest.mark.timeout(400)]

TEXT = "Planting quokka saplings"
# Every notification event the page is told about (app.js shows none for
# messages; app.py turns them into desktop notifications).
RECORD_NOTES = "window.__notes = []; on('notify', (n) => window.__notes.push(n)); 0"


async def shot(app: App, tmp_path: Path, name: str) -> None:
    folder = Path(os.environ.get("OPENARCANUM_UI_SHOTS") or tmp_path)
    folder.mkdir(parents=True, exist_ok=True)
    await app.screenshot(folder / f"{name}.png")


async def member_status(app: App, name: str) -> str | None:
    return await app.call(
        "(n) => { const b = [...document.querySelectorAll('#members .member')]"
        ".find((e) => e.getAttribute('aria-label') === n); return b ? b.dataset.status : null; }",
        name,
    )


async def wait_member_status(app: App, name: str, want: str) -> None:
    for _ in range(100):
        if await member_status(app, name) == want:
            return
        await asyncio.sleep(0.2)
    await app._fail_shot(f"{name} {want}")
    raise AssertionError(f"{app.name}: {name} never shown {want!r} (is {await member_status(app, name)!r})")


async def add_contact(app: App, name: str) -> None:
    """Member menu → Add as contact. The other person's card was just
    published (two_people): on a slow machine it may not have reached the
    server yet, so the error toast gets dismissed and the add tried again."""
    for _ in range(30):
        await community_member_menu(app, name)
        await app.click(".menu .mi", "Add as contact")
        await app.wait_js(
            "[...document.querySelectorAll('#toasts .toast')]"
            ".some((t) => /Added to contacts|contact card/.test(t.textContent))",
            "the add's outcome",
        )
        if any("Added to contacts." in t for t in await app.texts("#toasts .toast")):
            return
        await app.click("#toasts .toast.error button", "Dismiss")
        await app.wait_gone("#toasts .toast.error")
        await asyncio.sleep(1)
    raise AssertionError(f"{app.name}: {name}'s contact card never became available")


def test_status_picker_audiences_and_do_not_disturb(tmp_path):
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            olive, alice = await two_people(tmp_path, meta, node)
            try:
                await add_contact(olive, "Alice")
                await add_contact(alice, "Olive")
                # A conversation, so Olive is in Alice's DM list.
                await community_member_menu(alice, "Olive")
                await alice.click(".menu .mi", "Message")
                await alice.wait_js("document.querySelector('#dm-input')", "DM composer")
                await alice.click("#dm-input")
                await alice.type("hello olive")
                await alice.key("Enter")
                await alice.wait_text("#dm-messages .mtext", "hello olive")

                # Olive sets her status from the user panel.
                await olive.click(".user-panel .me-btn")
                await olive.click("#ctx-menu .mi", "Set status…")
                await olive.wait_js("document.querySelector('.status-picker')", "the status picker")
                await olive.click('.status-choice[data-state="dnd"]')
                await olive.fill(".status-picker input[type=text]", TEXT)
                await olive.click('.emoji-choice[data-emoji="🌴"]')
                await olive.select(".status-picker select", "1 hour")
                who = await olive.js("document.querySelector('.status-who').textContent")
                assert "your contacts" in who and "members of 1 community" in who, who
                await shot(olive, tmp_path, "status-picker")
                await olive.button("Save")
                await olive.wait_gone(".status-picker")
                await olive.wait_text(".user-panel .me-status", TEXT)
                assert await olive.js("document.querySelector('.user-panel .presence').classList.contains('dnd')")

                # Alice, as a member: the state only (the community default).
                await alice.click(".rail-item[data-community]")
                await open_members(alice)
                await wait_member_status(alice, "Olive", "dnd")
                row = (
                    "[...document.querySelectorAll('#members .member')]"
                    ".find((e) => e.getAttribute('aria-label') === 'Olive')"
                )
                assert not await alice.js(f"{row}.querySelector('.status-text')")
                # The dot has words: the row's description.
                desc = await alice.js(f"document.getElementById({row}.getAttribute('aria-describedby')).textContent")
                # She's active, so it says so ("Last active … ago" once idle).
                assert desc == "Do not disturb - Active now", desc
                assert await alice.js(f"{row}.title") == desc
                await shot(alice, tmp_path, "member-list-status")
                # Her menu says it too.
                await community_member_menu(alice, "Olive")
                await alice.wait_text("#ctx-menu .mi-note", "Do not disturb")
                await alice.key("Escape")

                # Alice, as a contact: the custom text in her DM list.
                await open_dms(alice)
                for _ in range(60):
                    await check_mail(alice)
                    if TEXT in " ".join(await alice.texts(".dm-list .status-text")):
                        break
                    await asyncio.sleep(0.4)
                await alice.wait_text(".dm-list .status-text", TEXT)
                assert await alice.js("document.querySelector('.dm-list .chan[data-status]').dataset.status") == "dnd"
                await shot(alice, tmp_path, "dm-list-status")

                # The Contacts page, left open, follows Olive's status like the
                # list does: repainted in place, not drawn again.
                await alice.click("button.chan.dm-home")
                row = "document.querySelector('.dm-home-page .member-row[data-contact]')"
                await alice.wait_js(f"{row} && {row}.querySelector('.presence.dnd')", "Olive dnd on Contacts")
                assert TEXT in await alice.js(f"{row}.querySelector('.contact-status').textContent")
                await alice.js("document.querySelector('.dm-home-page').dataset.mark = 'kept'; 0")
                await olive.js("api('set_status', 'idle', 'Back soon', '', 'never').then(() => 0)")

                async def contacts_row_idle():
                    # Fetch mail without the refresh button (which redraws).
                    await alice.js("api('dm_poll').then(() => 0)")
                    return await alice.js(f"!!{row}.querySelector('.presence.idle')")

                for _ in range(60):
                    if await contacts_row_idle():
                        break
                    await asyncio.sleep(0.4)
                assert await alice.js(f"!!{row}.querySelector('.presence.idle')"), "Contacts page never followed"
                assert "Back soon" in await alice.js(f"{row}.querySelector('.contact-status').textContent")
                assert (await alice.js(f"{row}.title")).startswith("Idle")
                assert await alice.js("document.querySelector('.dm-home-page').dataset.mark") == "kept"
                # Back to Do not disturb for what follows.
                await olive.js(f"api('set_status', 'dnd', {TEXT!r}, '🌴', '1h').then(() => 0)")
                for _ in range(60):
                    await check_mail(alice)
                    if await alice.js(f"!!{row}.querySelector('.presence.dnd')"):
                        break
                    await asyncio.sleep(0.4)

                # Do not disturb: no notification for Alice's message, but the
                # unread mark still shows.
                await olive.js(RECORD_NOTES)
                await olive.click(".rail-item[data-community]")
                await alice.click(".rail-item[data-community]")
                await alice.wait_js("document.querySelector('#composer-input')", "Alice's message box")
                await send_message(alice, "message during do not disturb")
                await olive.wait_text("#messages .mtext", "message during do not disturb")
                await asyncio.sleep(1)
                notes = await olive.js("window.__notes.filter((n) => n.community).length")
                assert notes == 0, "a notification during Do not disturb"

                # Un-share the community: Alice stops seeing the status there
                # (Olive is just online to her).
                await open_settings(olive)
                await olive.click(".settings nav .item", "Privacy & DMs")
                await olive.wait_js(
                    "document.querySelector('.status-privacy select[data-community]')", "status per community"
                )
                await shot(olive, tmp_path, "privacy-status")
                await olive.select(".status-privacy select[data-community]", "Off")
                await olive.wait_text(".status-privacy .saved", "Saved")
                await close_settings(olive)
                await wait_member_status(alice, "Olive", "online")

                # Back to online: notifications come back.
                await olive.click(".user-panel .me-btn")
                await olive.click("#ctx-menu .mi", "Set status…")
                await olive.click('.status-choice[data-state="online"]')
                await olive.button("Save")
                await olive.wait_gone(".status-picker")
                await olive.click(".rail-item[title='Direct messages']")
                await send_message(alice, "message after do not disturb")
                for _ in range(50):
                    if await olive.js("window.__notes.filter((n) => n.community).length"):
                        break
                    await asyncio.sleep(0.2)
                assert await olive.js("window.__notes.filter((n) => n.community).length") >= 1
                for app in (olive, alice):
                    app.assert_no_errors()
            finally:
                await olive.stop()
                await alice.stop()

        run(main(), 360)
