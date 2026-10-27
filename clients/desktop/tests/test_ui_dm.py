"""DMs and group DMs, clicked through in two real client windows."""

import pytest
from conftest import network, run
from uidriver import App
from uiflows import (
    ask_for_passphrase_at_start,
    claim,
    community_member_menu,
    create_profile,
    join,
    publish_card,
    set_maintainers_in_settings,
    unlock,
)

pytestmark = pytest.mark.gui

# Records the text of every toast that appears (they go by themselves).
RECORD_TOASTS = (
    "window.seenToasts = []; new MutationObserver(() => document.querySelectorAll('#toasts .toast')"
    ".forEach((t) => window.seenToasts.push(t.textContent)))"
    ".observe(document.querySelector('#toasts'), { childList: true }); 0"
)


async def set_maintainer(app: App, url: str) -> None:
    await set_maintainers_in_settings(app, [url])


async def two_people(tmp_path, meta, node):
    olive = await App(tmp_path / "olive", name="olive").start()
    alice = await App(tmp_path / "alice", name="alice").start()
    for app in (olive, alice):
        await create_profile(app)
        await set_maintainer(app, meta)
    invite = await claim(olive, node.claim_code, "DM Town", "Olive")
    await join(alice, invite, "Alice")
    await publish_card(olive)
    await publish_card(alice)
    return olive, alice


async def open_dms(app: App) -> None:
    await app.click('.rail-item[title="Direct messages"]')
    await app.wait_text(".side-head .name", "Direct messages")


async def check_mail(app: App) -> None:
    await app.click('button[aria-label="Check for new messages"]')


async def wait_dm_text(app: App, text: str) -> None:
    for _ in range(40):
        await check_mail(app)
        if any(text in t for t in await app.texts("#dm-messages .mtext")):
            return
        await app.wait_js("true", "tick")
        await __import__("asyncio").sleep(0.4)
    await app._fail_shot(text)
    raise AssertionError(f"{app.name}: {text!r} never shown; JS errors {app.errors}")


def test_direct_message_request_and_actions(tmp_path):
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            olive, alice = await two_people(tmp_path, meta, node)
            try:
                # New DMs show as badges only: every toast that appears in
                # Alice's window is recorded, and none may be Olive's reply.
                await alice.js(RECORD_TOASTS)
                # Alice starts a DM from the member list.
                await community_member_menu(alice, "Olive")
                await alice.click(".menu .mi", "Message")
                await alice.wait_js("document.querySelector('#dm-input')", "DM composer")
                await alice.click("#dm-input")
                await alice.type("hello olive")
                await alice.key("Enter")
                await alice.wait_text("#dm-messages .mtext", "hello olive")
                # Links in messages stay text: nothing remote is fetched.
                await alice.click("#dm-input")
                await alice.type("look https://example.invalid/pixel.png")
                await alice.key("Enter")
                await alice.wait_text("#dm-messages .mtext", "pixel.png")

                # Olive sees a request with the message, and accepts.
                await open_dms(olive)
                for _ in range(40):
                    await check_mail(olive)
                    if await olive.exists("button.chan[data-dm]"):
                        break
                    await __import__("asyncio").sleep(0.4)
                await olive.click("button.chan[data-dm]")
                await olive.wait_text("#dm-messages .mtext", "hello olive")
                await olive.wait_text(".banner", "wants to message you")
                await olive.button("Accept")
                await olive.wait_gone(".banner", None)
                await olive.click("#dm-input")
                await olive.type("hi alice")
                await olive.key("Enter")
                await olive.wait_text("#dm-messages .mtext", "hi alice")
                await wait_dm_text(alice, "hi alice")
                assert not [x for x in await alice.js("window.seenToasts") if "hi alice" in x], "a pop-up for a new DM"

                # Alice reacts to Olive's message, edits her own and pins it.
                await alice.message_action("#dm-messages .msg:not(.system)", "Add reaction", nth=-1)
                await alice.click(".menu .mi", "👍")
                await alice.wait_js("document.querySelector('#dm-messages .reaction')", "reaction")
                await alice.message_action("#dm-messages .msg:not(.system)", "Edit", nth=0)
                await alice.fill(".modal textarea", "hello olive (edited text)")
                await alice.button("Save")
                await alice.wait_text("#dm-messages .mtext", "hello olive (edited text)")
                await alice.message_action("#dm-messages .msg:not(.system)", "Pin", nth=0)
                await alice.wait_text("#dm-messages .pill", "pinned")
                await wait_dm_text(olive, "hello olive (edited text)")
                await olive.wait_js("document.querySelector('#dm-messages .reaction')", "reaction on Olive's side", 30)
                await olive.wait_text("#dm-messages .edited", "(edited)")

                # Reply to a message.
                await olive.message_action("#dm-messages .msg:not(.system)", "Reply", nth=0)
                await olive.wait_text("#dm-reply", "Replying to Alice")
                await olive.click("#dm-input")
                await olive.type("replying now")
                await olive.key("Enter")
                await wait_dm_text(alice, "replying now")
                await alice.wait_text("#dm-messages .reply-ref", "hello olive (edited text)")

                # Details: pinned message, then safety numbers match on both sides.
                numbers = []
                for app in (alice, olive):
                    await app.click('button[aria-label="Conversation details"]')
                    await app.wait_text(".modal .pin-row", "hello olive")
                    await app.click(".modal .member-row button[aria-label^='Options for']")
                    await app.click(".menu .mi", "Safety number")
                    await app.wait_js("document.querySelector('.safety-number')", "safety number")
                    numbers.append((await app.texts(".safety-number"))[0])
                    await app.button("Close")
                    await app.button("Close")
                assert numbers[0] == numbers[1] and len(numbers[0].replace(" ", "")) == 60

                # Deleting a message.
                await alice.message_action("#dm-messages .msg:not(.system)", "Delete", nth=0)
                await alice.button("Delete")
                await alice.wait_text("#dm-messages .mtext", "Message deleted")
                for app in (olive, alice):
                    app.assert_no_errors()
                    app.assert_local_only()

                # After a restart the conversation is listed as soon as the
                # profile is unlocked, not only once some DM event arrives
                # (DM events are held back here to show the difference).
                # The unlock screen is wanted, so the restart asks.
                await ask_for_passphrase_at_start(olive)
                await olive.stop()
                olive = await App(tmp_path / "olive", name="olive").start()
                await olive.js(
                    "(() => { const deliver = window.__openarcanumEvent;"
                    " window.__openarcanumEvent = (m) => { if (!m || !String(m.event).startsWith('dm')) deliver(m); };"
                    " return true; })()"
                )
                await unlock(olive)
                await open_dms(olive)
                await olive.wait_text("button.chan[data-dm] .cname", "Alice", 10)
                # Opened straight into DMs: the group picker still leaves
                # Olive herself out.
                await olive.click('button[aria-label="New group DM"]')
                await olive.wait_text(".modal .pick-list label", "Alice")
                assert "Olive" not in await olive.texts(".modal .pick-list label")
                await olive.button("Cancel")
                olive.assert_no_errors()
            finally:
                await olive.stop()
                await alice.stop()

        run(main(), 300)


def test_group_dm_create_rename_leave(tmp_path):
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            olive, alice = await two_people(tmp_path, meta, node)
            try:
                # Olive adds Alice as a contact (so the invitation is allowed),
                # then creates a group with her.
                await community_member_menu(alice, "Olive")
                await alice.click(".menu .mi", "Add as contact")
                await open_dms(olive)
                await olive.click('button[aria-label="New group DM"]')
                await olive.click(".modal .pick-list input[type=checkbox]", "Alice")
                await olive.fill(".modal input[placeholder='Optional']", "Plans")
                await olive.button("Create")
                await olive.wait_text(".main-head .title", "Plans")

                await open_dms(alice)
                for _ in range(40):
                    await check_mail(alice)
                    if await alice.exists("button.chan[data-dm]"):
                        break
                    await __import__("asyncio").sleep(0.4)
                await alice.click("button.chan[data-dm]")
                await alice.wait_text(".banner", "You were invited")
                await alice.button("Join")
                await alice.wait_js("document.querySelector('#dm-input')", "composer after joining")

                # Olive renames the group; Alice sees the new name.
                await olive.click('button[aria-label="Conversation details"]')
                await olive.button("Rename")
                await olive.fill(".modal:last-child input", "Weekend plans")
                await olive.button("Save")
                # The details dialog opens again once the rename is saved.
                await olive.wait_text(".modal .dm-info b", "Weekend plans")
                await olive.button("Close")
                await olive.wait_gone("#modal-root .backdrop")
                for _ in range(40):
                    await check_mail(alice)
                    if "Weekend plans" in await alice.texts(".main-head .title"):
                        break
                    await __import__("asyncio").sleep(0.4)
                await alice.wait_text(".main-head .title", "Weekend plans")
                # Olive turns attachments off: Alice's open conversation
                # loses its paperclip.
                assert await alice.exists('#main button[aria-label="Attach files"]')
                await olive.click('button[aria-label="Conversation details"]')
                await olive.click(".modal input[type=checkbox]", "Allow attachments")
                await olive.button("Close")
                for _ in range(40):
                    await check_mail(alice)
                    if not await alice.exists('#main button[aria-label="Attach files"]'):
                        break
                    await __import__("asyncio").sleep(0.4)
                assert not await alice.exists('#main button[aria-label="Attach files"]')
                # Alice isn't the owner: no rename, no settings.
                await alice.click('button[aria-label="Conversation details"]')
                assert not await alice.exists(".modal button", "Rename")
                assert not await alice.exists(".modal input[type=checkbox]", "Allow attachments")
                # And she leaves.
                await alice.button("Leave group")
                await alice.button("Leave")
                await alice.wait_gone("button.chan[data-dm]")
                for app in (olive, alice):
                    app.assert_no_errors()
            finally:
                await olive.stop()
                await alice.stop()

        run(main(), 300)


def test_dm_attachment_send_and_save(tmp_path):
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            from test_e2e_dm_files import png

            olive, alice = await two_people(tmp_path, meta, node)
            try:
                await community_member_menu(alice, "Olive")
                await alice.click(".menu .mi", "Message")
                await alice.wait_js("document.querySelector('#dm-input')", "DM composer")
                await alice.click("#dm-input")
                await alice.type("hi")
                await alice.key("Enter")
                await open_dms(olive)
                for _ in range(40):
                    await check_mail(olive)
                    if await olive.exists("button.chan[data-dm]"):
                        break
                    await __import__("asyncio").sleep(0.4)
                await olive.click("button.chan[data-dm]")
                await olive.button("Accept")
                await olive.wait_js("document.querySelector('#dm-input')", "composer")

                # Olive attaches a picture by typing its path, with a caption.
                pic = tmp_path / "garden.png"
                pic.write_bytes(png(800, 600))
                await olive.click("#dm-input")
                await olive.type("our garden")
                await olive.click('button[aria-label="Attach files"]')
                await olive.fill(".modal input[aria-label='File path']", str(pic))
                await olive.key("Enter")
                await olive.wait_text(".modal .mono", "garden.png")
                await olive.button("Send")
                await olive.wait_text("#dm-messages .fname", "garden.png")

                # Alice gets it, with a thumbnail made on Olive's device, and saves it.
                for _ in range(60):
                    await check_mail(alice)
                    if await alice.exists('#dm-messages button[aria-label="Save garden.png"]'):
                        break
                    await __import__("asyncio").sleep(0.4)
                await alice.wait_text("#dm-messages .mtext", "our garden")
                assert await alice.exists("#dm-messages img.thumb")
                # The card says when the file is still on its way.
                for _ in range(60):
                    await check_mail(alice)
                    if not any("not downloaded yet" in t for t in await alice.texts("#dm-messages .file")):
                        break
                    await __import__("asyncio").sleep(0.4)
                await alice.click('#dm-messages button[aria-label="Save garden.png"]')
                out = tmp_path / "saved-garden.png"
                await alice.fill(".modal input[aria-label='Save to']", str(out))
                await alice.button("Save")
                for _ in range(40):
                    if out.exists():
                        break
                    await __import__("asyncio").sleep(0.25)
                assert out.read_bytes() == pic.read_bytes()
                for app in (olive, alice):
                    app.assert_no_errors()
                    app.assert_local_only()
            finally:
                await olive.stop()
                await alice.stop()

        run(main(), 300)
