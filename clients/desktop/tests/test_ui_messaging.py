"""Community messaging clicked through in two real client windows (trusted
DevTools input, real relay and community server binaries): formatting,
replies and jump-to-message, edits, pins, reactions, mentions and badges,
typing, unread markers, the member list with presence and nicknames,
several attachments in one message, and feature toggles hiding controls."""

import asyncio
import sqlite3

import pytest
from conftest import run, run_community, run_relay
from uidriver import App
from uiflows import (
    attach_files,
    claim,
    close_settings,
    create_channel,
    create_profile,
    edit_channel,
    edit_my_profile,
    join,
    message_selector,
    open_channel,
    open_members,
    open_settings,
    react,
    send_message,
)

pytestmark = pytest.mark.gui


async def two_members(tmp_path, node) -> tuple[App, App]:
    olive = await App(tmp_path / "olive", name="olive").start()
    alice = await App(tmp_path / "alice", name="alice").start()
    for app in (olive, alice):
        await create_profile(app)
    invite = await claim(olive, node.claim_code, "Msg Town", "Olive")
    await join(alice, invite, "Alice")
    return olive, alice


def test_messaging_features(tmp_path):
    with run_relay() as relay, run_community(tmp_path, [relay]) as node:

        async def main():
            olive, alice = await two_members(tmp_path, node)
            try:
                # Formatting: bold, a code block with its language, a spoiler.
                await olive.click("#composer-input")
                await olive.type("**bold move**\n```python\nprint('hi')\n```\nthe end is ||a secret||")
                await olive.key("Enter")
                for app in (olive, alice):
                    await app.wait_text("#messages .mtext strong", "bold move")
                    await app.wait_text("#messages .codeblock .code-lang", "python")
                    await app.wait_text("#messages .codeblock code", "print('hi')")
                spoiler = "#messages .spoiler"
                assert await alice.js(
                    f"getComputedStyle(document.querySelector('{spoiler} .spoiler-content')).visibility === 'hidden'"
                )
                await alice.click(spoiler)
                await alice.wait_js(
                    f"document.querySelector('{spoiler}').classList.contains('revealed')", "spoiler revealed"
                )
                assert await alice.js(
                    f"getComputedStyle(document.querySelector('{spoiler} .spoiler-content')).visibility === 'visible'"
                )

                # Reply, then jump back to the original from the reply.
                first = await message_selector(alice, "bold move")
                await alice.message_action(first, "Reply")
                await alice.wait_text("#composer-reply", "Replying to Olive")
                await send_message(alice, "replying to the bold one")
                await olive.wait_text("#messages .reply-ref", "bold move")
                await olive.click("#messages .reply-ref")
                await olive.wait_js("document.querySelector('#messages .msg.flash')", "jumped to the replied message")

                # Edit shows (edited) to the other member.
                mine = await message_selector(alice, "replying to the bold one")
                await alice.message_action(mine, "Edit")
                await alice.fill(".modal textarea", "replying to the bold one, edited")
                await alice.button("Save")
                await olive.wait_text("#messages .msg", "replying to the bold one, edited")
                await olive.wait_text("#messages .edited", "(edited)")

                # Pin (owner has Manage Messages), then open the pinned list and jump.
                await olive.message_action(await message_selector(olive, "bold move"), "Pin")
                await olive.wait_text("#messages .pill", "pinned")
                await alice.click('button[aria-label="Pinned messages"]')
                await alice.wait_text(".modal .pin-row", "bold move")
                await alice.click(".modal .pin-row")
                await alice.wait_gone("#modal-root .backdrop")
                await alice.wait_js("document.querySelector('#messages .msg.flash')", "jumped to the pinned message")
                # Members can't pin: no Pin action for Alice.
                await alice.hover(first)
                assert not await alice.exists(f"{first} .actions button", "Pin")

                # React with an emoji; the other client sees it.
                await react(alice, await message_selector(alice, "bold move"), "👍")
                await olive.wait_js(
                    "[...document.querySelectorAll('#messages .reaction')].some((e) => e.textContent.includes('👍 1'))",
                    "reaction",
                )

                # Typing shows on the other client.
                await alice.click("#composer-input")
                await alice.type("thinking about it")
                await olive.wait_text("#typing", "Alice is typing")
                await alice.js("document.querySelector('#composer-input').value = ''")

                # @mention: highlighted, with badges while Alice is elsewhere.
                await create_channel(olive, "random")
                await open_channel(olive, "general")
                await alice.wait_text(".chan .cname", "random")
                await open_channel(alice, "random")
                await send_message(olive, "hey @Alice, look at this")
                await send_message(olive, "and an unread line")
                await alice.wait_text(".chan .pill.mention", "@1")
                await alice.wait_js(
                    "document.querySelector('.rail-item .badge.mention-badge')", "community mention badge"
                )
                assert (await alice.texts(".rail-item .badge.mention-badge"))[0] == "1"
                await alice.click(".chan .pill.mention")
                await alice.wait_js("document.querySelector('#messages .msg.mentioned.flash')", "jumped to the mention")
                await alice.wait_text("#messages .msg.mentioned .mention.me", "@Alice")
                # Opening the channel showed the unread marker and cleared the badges.
                await alice.wait_text("#messages .new-marker", "New")
                await alice.wait_gone(".chan .pill.mention")
                await alice.wait_gone(".rail-item .badge.mention-badge")

                # Unread marker for plain messages too.
                await open_channel(alice, "random")
                await send_message(olive, "while you were away")
                await alice.wait_js(
                    "[...document.querySelectorAll('.chan.unread .cname')].some((e) => e.textContent === 'general')",
                    "general marked unread",
                )
                await open_channel(alice, "general")
                await alice.wait_js(
                    "(() => { const m = document.querySelector('#messages .new-marker');"
                    " const next = m && m.nextElementSibling;"
                    " return next && next.textContent.includes('while you were away'); })()",
                    "the New marker before the unread message",
                )

                # Member list: online status, then a nickname Alice sets.
                await open_members(olive)
                await olive.wait_js(
                    "[...document.querySelectorAll('#members .member.online')]"
                    ".some((e) => e.getAttribute('aria-label') === 'Alice')",
                    "Alice online",
                )
                await edit_my_profile(alice, nickname="Ally", pronouns="she/her")
                await olive.click('button[title="Members"]')
                await open_members(olive)
                await olive.wait_js(
                    "[...document.querySelectorAll('#members .member.online')]"
                    ".some((e) => e.getAttribute('aria-label') === 'Ally')",
                    "nickname in the member list",
                )
                await olive.wait_text("#members .member .pronouns", "she/her")

                # Three attachments in one message.
                paths = []
                for i in range(3):
                    f = tmp_path / f"note{i}.txt"
                    f.write_text(f"note {i}")
                    paths.append(str(f))
                await attach_files(alice, paths, "three notes")
                sel = await message_selector(olive, "three notes")
                await olive.wait_js(
                    f"document.querySelectorAll('{sel} .file').length === 3", "three files in one message"
                )
                assert sorted(await olive.texts(f"{sel} .file .fname")) == ["note0.txt", "note1.txt", "note2.txt"]
                # And the sender's name in it is the nickname.
                assert "Ally" in (await olive.texts(f"{sel} .author"))[0]

                for app in (olive, alice):
                    app.assert_no_errors()
                    app.assert_local_only()
            finally:
                await olive.stop()
                await alice.stop()

        run(main(), 420)


def test_messaging_toggles_hide_controls(tmp_path):
    with run_relay() as relay, run_community(tmp_path, [relay]) as node:

        async def main():
            olive, alice = await two_members(tmp_path, node)
            try:
                await send_message(olive, "toggle target")
                target = await message_selector(alice, "toggle target")
                await alice.hover(target)
                assert await alice.exists(f"{target} .actions button", "Add reaction")
                assert await alice.exists('button[aria-label="Attach files"]')

                # The owner turns reactions and attachments off for the community.
                await open_settings(olive, "community")
                await olive.click(".settings nav .item", "Features")
                await olive.click(".settings section input[type=checkbox]", "Reactions")
                await olive.click(".settings section input[type=checkbox]", "Attachments")
                await olive.button("Save changes")
                await olive.wait_text(".toast", "Saved")
                await olive.click(".settings .close")

                await alice.wait_gone('button[aria-label="Attach files"]', timeout=30)
                target = await message_selector(alice, "toggle target")
                await alice.hover(target)
                assert not await alice.exists(f"{target} .actions button", "Add reaction")

                # The change is in the audit log.
                await open_settings(olive, "community")
                await olive.click(".settings nav .item", "Audit log")
                await olive.wait_text(".settings section td", "reactions off")
                await olive.click(".settings .close")
                for app in (olive, alice):
                    app.assert_no_errors()
            finally:
                await olive.stop()
                await alice.stop()

        run(main(), 300)


def test_purged_messages_leave_the_open_app(tmp_path):
    """Messages the server purged by retention disappear from a running
    app's channel (the creation times are moved back in the database
    instead of waiting a day)."""
    with run_relay() as relay, run_community(tmp_path, [relay]) as node:

        async def main():
            olive = await App(tmp_path / "olive", name="olive").start()
            try:
                await create_profile(olive)
                await claim(olive, node.claim_code, "Retention Town", "Olive")
                await create_channel(olive, "short")
                await open_channel(olive, "short")
                # The community update for the new channel redraws the composer.
                await olive.wait_js("st.states[st.cid].channels.some((c) => c.name === 'short')", "channel in state")
                await asyncio.sleep(1)
                for text in ("old one", "old two"):
                    await send_message(olive, text)
                    await olive.wait_text("#messages .mtext", text)
                channel = await olive.js("st.channel")
                # Keep #short's messages one day, set in the channel's Edit… dialog.
                await edit_channel(olive, "short", keep_messages="1")
                await olive.wait_text(".settings section .channel-row .desc", "retention: messages 1, files default")
                await close_settings(olive)
                await open_channel(olive, "general")

                node.stop()
                db = sqlite3.connect(node.data / "community.db")
                db.execute("UPDATE messages SET created_at = created_at - 2*86400000 WHERE channel_id = ?", (channel,))
                db.commit()
                db.close()
                node.start()  # the retention job runs at start
                # Once the server answers again, opening the channel shows
                # what it has: nothing.
                await olive.wait_js(
                    f"api('history', st.cid, {channel}, 0).then((r) => !r.offline)", "the server answering", 60
                )
                await open_channel(olive, "short")
                await asyncio.sleep(2)
                assert not await olive.exists("#messages .mtext"), await olive.texts("#messages .mtext")
                olive.assert_no_errors()
            finally:
                await olive.stop()

        run(main(), 300)
