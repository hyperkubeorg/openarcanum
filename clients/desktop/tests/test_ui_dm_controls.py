"""DM controls clicked through in real windows, with the other person's
window showing the effect: typing, formatting, unread pill and "New"
marker, mentions, jump-to-message, safety-number verification, blocking,
the identity-key-change banner, group icon, adding and removing members,
"Members can add people", making someone owner, the attachment limits and
the "expired" file card."""

import asyncio

import pytest
from conftest import PASS, Events, network, run
from test_e2e_community import online
from test_e2e_dm_files import blob_count, png
from test_ui_dm import check_mail, open_dms, publish_card, set_maintainer, two_people, wait_dm_text
from uidriver import App
from uiflows import ask_for_passphrase_at_start, community_member_menu, create_profile, join, unlock

from openarcanum.core import Core
from openarcanum.paths import Paths

# The expiry test waits out a one-minute blob lifetime.
pytestmark = [pytest.mark.gui, pytest.mark.timeout(300)]


async def until(app: App, pred, what: str, tries: int = 60) -> None:
    """Check the mail until pred() (an async check of the window) holds."""
    for _ in range(tries):
        if await app.exists('button[aria-label="Check for new messages"]'):
            await check_mail(app)
        if await pred():
            return
        await asyncio.sleep(0.4)
    await app._fail_shot(what)
    raise AssertionError(f"{app.name}: timed out waiting for {what}; JS errors {app.errors}")


async def send_dm(app: App, text: str, shown: str | None = None) -> None:
    await app.click("#dm-input")
    await app.type(text)
    await app.key("Enter")
    await app.wait_text("#dm-messages .mtext", shown or text)


async def start_direct(olive: App, alice: App) -> None:
    """Alice messages Olive from the member list; Olive accepts."""
    await community_member_menu(alice, "Olive")
    await alice.click(".menu .mi", "Message")
    await alice.wait_js("document.querySelector('#dm-input')", "DM composer")
    await send_dm(alice, "hello olive")
    await open_dms(olive)
    await until(olive, lambda: olive.exists("button.chan[data-dm]"), "the request")
    await olive.click("button.chan[data-dm]")
    await olive.button("Accept")
    await olive.wait_gone(".banner", None)
    await send_dm(olive, "hi alice")
    await wait_dm_text(alice, "hi alice")


async def member_option(app: App, name: str, option: str) -> None:
    """Conversation details → a member's ⋯ menu → option."""
    await app.click('button[aria-label="Conversation details"]')
    await app.click(f'.modal .member-row button[aria-label="Options for {name}"]')
    await app.click(".menu .mi", option)


async def close_modals(app: App) -> None:
    while await app.exists("#modal-root .backdrop"):
        await app.key("Escape")
        await asyncio.sleep(0.1)


def test_direct_dm_activity_verification_blocking_and_key_change(tmp_path):
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            olive, alice = await two_people(tmp_path, meta, node)
            try:
                await start_direct(olive, alice)

                # Typing: Olive sees Alice typing before she sends.
                await alice.click("#dm-input")
                await alice.type("typing this")
                await olive.wait_text("#dm-typing", "Alice is typing…", 20)
                await alice.key("Enter")
                await wait_dm_text(olive, "typing this")

                # Formatting and spoilers render as in channels.
                await send_dm(alice, "a **bold move** and ||hidden bit||", "a bold move and hidden bit")
                await olive.wait_text("#dm-messages .mtext strong", "bold move")
                await olive.wait_text("#dm-messages .spoiler", "hidden bit")
                await olive.click("#dm-messages .spoiler")
                await olive.wait_js("document.querySelector('#dm-messages .spoiler.revealed')", "spoiler revealed")

                # Unread: Olive is in the community; two messages arrive, one
                # mentioning her. The rail and the list count them, opening
                # the conversation shows the "New" marker above the first,
                # and the mention is highlighted.
                await olive.click(".rail-item[data-community]")
                await olive.wait_js("document.querySelector('#composer-input')", "the community")
                # (While there, Olive adds Alice as a contact: her client pins
                # Alice's identity key in this community.)
                await community_member_menu(olive, "Alice")
                await olive.click(".menu .mi", "Add as contact")
                await olive.wait_text("#toasts .toast", "Added to contacts.")
                await send_dm(alice, "unread one")
                await send_dm(alice, "look here @Olive")
                await olive.wait_text('.rail-item[title="Direct messages"] .badge', "2", 40)
                await open_dms(olive)
                await olive.wait_text("button.chan[data-dm] .pill", "2")
                await olive.wait_text("button.chan[data-dm] .pill.mention", "@")
                await olive.click("button.chan[data-dm]")
                await olive.wait_text("#dm-messages .mtext", "look here")
                after_marker = await olive.js(
                    "(() => { const m = document.querySelector('#dm-messages .new-marker');"
                    " let e = m && m.nextElementSibling;"
                    " while (e && !e.classList.contains('msg')) e = e.nextElementSibling;"
                    " return e ? e.textContent : null; })()"
                )
                assert after_marker and "unread one" in after_marker, after_marker
                assert "look here" in "".join(await olive.texts("#dm-messages .msg.mentioned .mtext"))
                assert "unread one" not in "".join(await olive.texts("#dm-messages .msg.mentioned .mtext"))
                await olive.wait_gone('.rail-item[title="Direct messages"] .badge')

                # Jump to message: Alice replies to her first message; Olive
                # clicks the quote and the original is scrolled to and flashed.
                await alice.message_action("#dm-messages .msg:not(.system)", "Reply", nth=0)
                await alice.wait_text("#dm-reply", "Replying to yourself")
                await send_dm(alice, "see above")
                await wait_dm_text(olive, "see above")
                first = await olive.js(
                    "[...document.querySelectorAll('#dm-messages .msg')]"
                    ".find((m) => (m.querySelector('.mtext') || {}).textContent === 'hello olive').dataset.id"
                )
                await olive.js("document.querySelector('#dm-messages').scrollTop = 1e9")
                await olive.click("#dm-messages .reply-ref", "Alice: hello olive")
                await olive.wait_js(
                    f"document.querySelector('#dm-messages .msg[data-id=\"{first}\"]').classList.contains('flash')",
                    "the original message flashed",
                    5,
                )

                # Verification: Olive marks Alice verified after comparing.
                await member_option(olive, "Alice", "Safety number")
                await olive.wait_text(".modal p.small", "Not verified yet.")
                await olive.button("Mark as verified")
                await olive.wait_gone(".safety-number")
                await close_modals(olive)
                await olive.click('button[aria-label="Conversation details"]')
                await olive.wait_text(".modal .member-row .pill.ok", "verified")
                await close_modals(olive)

                # Blocking: Olive blocks Alice; Alice's messages no longer
                # reach her and she can't write to Alice. Unblocking restores both.
                await member_option(olive, "Alice", "Block")
                await close_modals(olive)
                await olive.wait_text("#dm-banners .banner", "You blocked this person.")
                await olive.click("#dm-input")
                await olive.type("to someone I blocked")
                await olive.key("Enter")
                await olive.wait_text("#toasts .toast.error", "blocked")
                await olive.click("#dm-input")
                await olive.js("document.activeElement.select()")
                await olive.key("Backspace")
                await send_dm(alice, "while blocked")
                for _ in range(10):
                    await olive.js("api('dm_poll').then(() => dmUI.reload())")
                    await asyncio.sleep(0.3)
                assert not any("while blocked" in t for t in await olive.texts("#dm-messages .mtext"))
                await olive.click("#dm-banners button", "Unblock")
                await olive.wait_gone("#dm-banners .banner")
                await send_dm(alice, "after unblocking")
                await wait_dm_text(olive, "after unblocking")
                assert not any("while blocked" in t for t in await olive.texts("#dm-messages .mtext"))

                # Identity-key change. Precondition, outside the UI: Alice
                # starts over with a new DM identity (as after a reinstall)
                # and publishes a new card.
                await alice.stop()
                core = Core(Paths.under(tmp_path / "alice"), emit=Events())
                await core.unlock(PASS)
                cid = next(iter(core.vault.data["communities"]))
                await online(core, cid)
                core.vault.data["communities"][cid].pop("dm_identity")
                await core.dms.set_contact_card(cid, True)
                await core.shutdown()
                # Olive meets her card again (adding her as a contact): the
                # conversation warns, and the warning clears once she accepts
                # the new key, which is no longer verified.
                await olive.click(".rail-item[data-community]")
                await community_member_menu(olive, "Alice")
                await olive.click(".menu .mi", "Add as contact")
                await olive.wait_text("#toasts .toast", "Added to contacts.")
                await open_dms(olive)
                await olive.click("button.chan[data-dm]")
                await olive.wait_text("#dm-banners .banner.warn", "Alice's key changed", 30)
                await olive.click("#dm-banners .banner.warn button", "Safety number")
                await olive.wait_text(".modal p.warn", "Their key changed")
                assert not await olive.exists(".modal button", "Mark as verified")
                await olive.button("Accept new key")
                await olive.wait_gone("#dm-banners .banner.warn")
                await olive.click('button[aria-label="Conversation details"]')
                await olive.wait_text(".modal .member-row", "Alice")
                assert not await olive.exists(".modal .member-row .pill.ok")
                assert not await olive.exists(".modal .member-row .pill.warn")
                await close_modals(olive)
                olive.assert_no_errors()
                olive.assert_local_only()
            finally:
                await olive.stop()
                await alice.stop()

        run(main(), 360)


async def three_people(tmp_path, meta, node):
    olive, alice = await two_people(tmp_path, meta, node)
    bob = await App(tmp_path / "bob", name="bob").start()
    await create_profile(bob)
    await set_maintainer(bob, meta)
    invite = await olive.js("api('admin', st.cid, 'create_invite', {}).then((r) => r.invite)")
    await join(bob, invite, "Bob")
    await publish_card(bob)
    return olive, alice, bob


async def sees_members(app: App, n: int) -> None:
    """Wait until `app`'s Conversation details list `n` members and nobody
    still invited. (An acceptance that races an owner's change is signed
    again on top of it; until the owner has it, the others don't count the
    new member in, so the test waits instead of racing it.)"""

    async def check():
        await app.click('button[aria-label="Conversation details"]')
        ok = f"Members ({n} of 10)" in await app.texts(".modal h3") and not await app.exists(
            ".modal .member-row .pill.warn", "invited"
        )
        await close_modals(app)
        return ok

    await until(app, check, f"{n} members on {app.name}'s side")


async def join_invitation(app: App) -> None:
    await open_dms(app)
    await until(app, lambda: app.exists("button.chan[data-dm]"), "the invitation")
    await app.click("button.chan[data-dm]")
    await app.wait_text(".banner", "You were invited")
    await app.button("Join")
    await app.wait_js("document.querySelector('#dm-input')", "composer after joining")


def test_group_dm_icon_members_options_and_owner(tmp_path):
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            olive, alice, bob = await three_people(tmp_path, meta, node)
            try:
                # Everyone takes Olive as a contact (their DM policy then
                # lets her invite them), and Bob takes Alice.
                for app in (alice, bob):
                    await community_member_menu(app, "Olive")
                    await app.click(".menu .mi", "Add as contact")
                    await app.wait_text("#toasts .toast", "Added to contacts.")
                await community_member_menu(bob, "Alice")
                await bob.click(".menu .mi", "Add as contact")

                await open_dms(olive)
                await olive.click('button[aria-label="New group DM"]')
                await olive.click(".modal .pick-list input[type=checkbox]", "Alice")
                await olive.fill(".modal input[placeholder='Optional']", "Crew")
                await olive.button("Create")
                await olive.wait_text(".main-head .title", "Crew")
                await join_invitation(alice)
                await sees_members(olive, 2)

                # Group icon: Olive sets one from a picture; Alice's list shows it.
                pic = tmp_path / "icon.png"
                pic.write_bytes(png(256, 256))
                await olive.click('button[aria-label="Conversation details"]')
                await olive.button("Set icon…")
                await olive.fill('.modal input[aria-label="Image path"]', str(pic))
                await olive.button("Set icon")
                await olive.wait_js("document.querySelector('button.chan[data-dm] img.group-icon')", "olive's icon")
                await until(
                    alice, lambda: alice.exists("button.chan[data-dm] img.group-icon"), "the icon on Alice's side"
                )
                # A member can't add people until the owner allows it.
                await alice.click('button[aria-label="Conversation details"]')
                assert not await alice.exists(".modal button", "Add people")
                assert not await alice.exists(".modal button", "Set icon…")
                await close_modals(alice)
                await olive.click('button[aria-label="Conversation details"]')
                await olive.click(".modal input[type=checkbox]", "Members can add people")
                await olive.wait_js(
                    "[...document.querySelectorAll('.modal label')]"
                    ".find((l) => l.textContent.includes('Members can add')).querySelector('input').checked",
                    "the option saved",
                )
                await close_modals(olive)

                async def alice_can_add():
                    await alice.click('button[aria-label="Conversation details"]')
                    ok = await alice.exists(".modal button", "Add people")
                    await close_modals(alice)
                    return ok

                await until(alice, alice_can_add, "Add people for Alice")
                # Olive removes the icon; Alice's list loses it.
                await olive.click('button[aria-label="Conversation details"]')
                await olive.button("Remove icon")
                await olive.wait_gone("button.chan[data-dm] img.group-icon")
                await close_modals(olive)
                await until(
                    alice, lambda: _not(alice.exists("button.chan[data-dm] img.group-icon")), "the icon gone for Alice"
                )

                # Add people: Olive invites Bob; he joins and all three are members.
                await olive.click('button[aria-label="Conversation details"]')
                await olive.button("Add people")
                await olive.click(".modal .pick-list input[type=checkbox]", "Bob")
                await olive.button("Invite")
                await olive.wait_text("#toasts .toast", "Invitations sent.")
                await join_invitation(bob)
                for app in (olive, alice, bob):
                    await sees_members(app, 3)
                await send_dm(bob, "bob here")
                await wait_dm_text(olive, "bob here")
                await wait_dm_text(alice, "bob here")

                # Remove a member: Bob is told and can't write any more.
                await member_option(olive, "Bob", "Remove from group")
                await olive.wait_text(".modal h2", "Remove")
                await olive.button("Remove")
                await close_modals(olive)

                async def two_left():
                    await olive.click('button[aria-label="Conversation details"]')
                    ok = "Members (2 of 10)" in await olive.texts(".modal h3")
                    await close_modals(olive)
                    return ok

                await until(olive, two_left, "Bob gone from Olive's member list")
                await until(bob, lambda: bob.exists("#dm-banners .banner", None), "Bob's removed banner")
                await bob.wait_text("#dm-banners .banner", "You were removed from this conversation.")
                assert not await bob.exists("#dm-input")

                # Make owner: Alice gets the owner controls and Olive loses them.
                await member_option(olive, "Alice", "Make owner")
                await olive.wait_text(".modal h2", "Make owner")
                await olive.button("Make owner")
                await close_modals(olive)

                async def alice_is_owner():
                    await alice.click('button[aria-label="Conversation details"]')
                    ok = await alice.exists(".modal button", "Rename")
                    await close_modals(alice)
                    return ok

                await until(alice, alice_is_owner, "Alice owning the group")
                await olive.click('button[aria-label="Conversation details"]')
                await olive.wait_text(".modal .member-row", "Alice")
                assert not await olive.exists(".modal button", "Rename")
                assert "owner" in " ".join(
                    await olive.texts(".modal .member-row:has(button[aria-label='Options for Alice']) .pill")
                )
                await close_modals(olive)
                for app in (olive, alice, bob):
                    app.assert_no_errors()
            finally:
                for app in (olive, alice, bob):
                    await app.stop()

        run(main(), 420)


async def _not(aw) -> bool:
    return not await aw


def test_dm_attachment_limits_and_expired_file(tmp_path):
    with network(tmp_path, blob_ttl="1m") as (meta, _relay, community):
        node = community()

        async def main():
            olive, alice = await two_people(tmp_path, meta, node)
            try:
                await start_direct(olive, alice)

                # Eleven files in one message are refused, and so is a file
                # over 25 MB; the dialog stays open with the choice.
                files = []
                for i in range(11):
                    f = tmp_path / f"f{i:02}.txt"
                    f.write_bytes(b"x")
                    files.append(str(f))
                await olive.click('button[aria-label="Attach files"]')
                for p in files:
                    await olive.click('.modal input[aria-label="File path"]')
                    await olive.type(p)
                    await olive.key("Enter")
                    await olive.wait_text(".modal .mono", p)
                await olive.button("Send")
                await olive.wait_text("#toasts .toast.error", "at most 10 files")
                assert len(await olive.texts(".modal .mono")) == 11
                await olive.button("Cancel")
                big = tmp_path / "big.bin"
                with open(big, "wb") as fh:
                    fh.truncate(25 * 1024 * 1024 + 1)
                await olive.click('button[aria-label="Attach files"]')
                await olive.click('.modal input[aria-label="File path"]')
                await olive.type(str(big))
                await olive.key("Enter")
                await olive.button("Send")
                await olive.wait_text("#toasts .toast.error", "over the 25 MB limit")
                await olive.button("Cancel")
                assert not await olive.exists("#dm-messages .file")

                # Alice is away while Olive sends a file; it's stored on the
                # maintainer node and expires before she's back. Her card says
                # "expired" and offers no download. She unlocks with her
                # passphrase when she's back.
                await ask_for_passphrase_at_start(alice)
                await alice.stop()
                late = tmp_path / "late.txt"
                late.write_bytes(b"too late")
                await olive.click('button[aria-label="Attach files"]')
                await olive.click('.modal input[aria-label="File path"]')
                await olive.type(str(late))
                await olive.key("Enter")
                await olive.button("Send")
                await olive.wait_text("#dm-messages .fname", "late.txt")
                for _ in range(120):
                    if blob_count(meta) == 1:
                        break
                    await asyncio.sleep(0.5)
                assert blob_count(meta) == 1
                await asyncio.sleep(62)  # past the blob lifetime
                alice = await App(tmp_path / "alice", name="alice").start()
                await unlock(alice)
                await open_dms(alice)
                await alice.click("button.chan[data-dm]")
                await until(
                    alice,
                    lambda: _has(alice, "#dm-messages .file.expired .small", "expired"),
                    "the expired card",
                    90,
                )
                assert not await alice.exists('#dm-messages button[aria-label="Save late.txt"]')
                await alice.wait_text("#dm-messages .file.expired .fname", "late.txt")
                for app in (olive, alice):
                    app.assert_no_errors()
            finally:
                await olive.stop()
                await alice.stop()

        run(main(), 360)


async def _has(app: App, sel: str, text: str) -> bool:
    return any(text in t for t in await app.texts(sel))
