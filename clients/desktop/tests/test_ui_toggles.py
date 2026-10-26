"""Every community feature toggle and every channel-level setting, clicked
in the owner's window, with the effect checked in a member's window: each
control disappears while its toggle is off and comes back when it is turned on
again. Server-side refusals for each toggle are tested in Go
(internal/community: TestAttachmentToggles, TestLinkPreviews, ...) and end
to end (test_e2e_messaging.py, test_e2e_channels.py); here the point is
what members see.

Link previews need a page to fetch: the test serves one on 127.0.0.1 and
runs the community server as `previewcommunity` (the same server with the
fetcher allowed to reach loopback, a test command that is never released)
with `preview_schemes` allowing http."""

import asyncio
import http.server
import threading

import pytest
from conftest import new_core, run, run_community, run_relay
from test_e2e_community import general
from test_e2e_moderation import png
from test_ui_voice import create_voice_channel, join_voice, media_env, tone  # noqa: F401 (tone is a fixture)
from uidriver import App
from uiflows import (
    claim,
    close_settings,
    community_section,
    create_channel,
    create_profile,
    edit_channel,
    join,
    message_selector,
    open_channel,
    open_settings,
    react,
    send_message,
    set_feature,
    toggle_role_perms,
)

pytestmark = [pytest.mark.gui, pytest.mark.timeout(480)]

PAGE = (
    b'<html><head><meta property="og:title" content="Heron facts">'
    b'<meta property="og:description" content="All about herons."></head><body>herons</body></html>'
)


class _Page(http.server.BaseHTTPRequestHandler):
    hits = 0

    def do_GET(self):
        type(self).hits += 1
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(PAGE)))
        self.end_headers()
        self.wfile.write(PAGE)

    def log_message(self, *args):
        """Keep test output quiet."""


async def two_members(tmp_path, node, name: str, env: dict | None = None) -> tuple[App, App, str]:
    olive = await App(tmp_path / "olive", env=env, name="olive").start()
    alice = await App(tmp_path / "alice", env=env, name="alice").start()
    for app in (olive, alice):
        await create_profile(app)
    invite = await claim(olive, node.claim_code, name, "Olive")
    await join(alice, invite, "Alice")
    return olive, alice, invite


async def wait_action(app: App, msg_sel: str, action: str, present: bool, timeout: float = 30) -> None:
    """Wait until a message's hover actions include (or lack) `action`."""
    loop = asyncio.get_running_loop()
    deadline = loop.time() + timeout
    while True:
        await app.hover(msg_sel)
        if await app.exists(f"{msg_sel} .actions button", action) == present:
            return
        if loop.time() > deadline:
            raise AssertionError(f"{app.name}: {action!r} {'never appeared' if present else 'never went away'}")
        await asyncio.sleep(0.3)


def feature_js(name: str) -> str:
    return f"st.states[st.cid].settings.features.{name}"


def channel_feature_js(channel: str, name: str) -> str:
    return f"st.states[st.cid].channels.find((c) => c.name === {channel!r}).features.{name}"


async def no_preview(app: App, sel: str) -> None:
    """The message never gets a preview card (the fetch, if any, would take
    well under this)."""
    await asyncio.sleep(3)
    assert not await app.exists(f"{sel} .link-preview"), f"{app.name}: unexpected preview"


def test_content_toggles(tmp_path):
    """Attachments, link previews, reactions and custom emoji: the community
    toggles and the channel-level settings, a link preview card when
    previews are on, and reacting with a custom emoji from the reaction menu."""
    site = http.server.ThreadingHTTPServer(("127.0.0.1", 0), _Page)
    threading.Thread(target=site.serve_forever, daemon=True).start()
    url = f"http://127.0.0.1:{site.server_address[1]}/herons"
    with (
        run_relay() as relay,
        run_community(tmp_path, [relay], program="previewcommunity", preview_schemes="http,https") as node,
    ):

        async def main():
            olive, alice, _ = await two_members(tmp_path, node, "Toggle Town")
            try:
                # A custom emoji, uploaded by clicking.
                image = tmp_path / "parrot.png"
                image.write_bytes(png(64, 64))
                await community_section(olive, "Emoji")
                await olive.fill('input[aria-label="Emoji name"]', "party_parrot")
                await olive.fill('input[aria-label="Image path"]', str(image))
                await olive.button("Upload")
                await olive.wait_js("document.querySelector('.emoji-row[data-name=\"party_parrot\"]')", "emoji row")
                await close_settings(olive)

                await send_message(olive, "react to me")
                target = await message_selector(alice, "react to me")

                # Reacting with the custom emoji from the reaction menu.
                await alice.wait_js("document.querySelector('button[title=\"Custom emoji\"]')", "emoji picker", 30)
                await react(alice, target, ":party_parrot:")
                await olive.wait_js(
                    f"[...document.querySelectorAll('{target} .reaction img.emoji')]"
                    ".some((i) => i.alt === ':party_parrot:')",
                    "the custom emoji reaction on olive's client",
                )

                # Link previews: off by default, so a link gets no card and the
                # page is never fetched.
                await send_message(alice, f"before previews {url}")
                await no_preview(olive, await message_selector(olive, "before previews"))
                assert _Page.hits == 0
                await set_feature(olive, "link_previews", True)
                await alice.wait_js(feature_js("link_previews"), "previews on", 30)
                # Community Admin is a page in place of the channel: back to it.
                await close_settings(olive)
                await send_message(alice, f"with previews {url}")
                for app in (olive, alice):
                    sel = await message_selector(app, "with previews")
                    await app.wait_text(f"{sel} .link-preview .lp-title", "Heron facts", 30)
                    await app.wait_text(f"{sel} .link-preview", "All about herons.")
                assert _Page.hits >= 1
                # Off in #general (the channel setting): no card, no fetch.
                await edit_channel(olive, "general", toggles={"Link previews": False})
                await alice.wait_js(f"!{channel_feature_js('general', 'link_previews')}", "previews off here", 30)
                await close_settings(olive)
                hits = _Page.hits
                await send_message(alice, f"channel says no {url}")
                await no_preview(olive, await message_selector(olive, "channel says no"))
                assert _Page.hits == hits
                await edit_channel(olive, "general", toggles={"Link previews": True})
                await alice.wait_js(channel_feature_js("general", "link_previews"), "previews back here", 30)
                await close_settings(olive)
                await send_message(alice, f"previews again {url}")
                sel = await message_selector(olive, "previews again")
                await olive.wait_text(f"{sel} .link-preview .lp-title", "Heron facts", 30)

                # Attachments: the community toggle, then the channel setting.
                attach = 'button[aria-label="Attach files"]'
                assert await alice.exists(attach)
                await set_feature(olive, "attachments", False)
                await alice.wait_gone(attach, timeout=30)
                await set_feature(olive, "attachments", True)
                await alice.wait_js(f"document.querySelector('{attach}')", "📎 back", 30)
                await edit_channel(olive, "general", toggles={"Attachments": False})
                await alice.wait_gone(attach, timeout=30)
                await edit_channel(olive, "general", toggles={"Attachments": True})
                await alice.wait_js(f"document.querySelector('{attach}')", "📎 back in #general", 30)

                # Reactions: the community toggle, then the channel setting.
                await wait_action(alice, target, "Add reaction", True)
                await set_feature(olive, "reactions", False)
                await wait_action(alice, target, "Add reaction", False)
                # The existing reaction can't be clicked either.
                assert await alice.js(f"document.querySelector('{target} .reaction').disabled")
                await set_feature(olive, "reactions", True)
                await wait_action(alice, target, "Add reaction", True)
                await edit_channel(olive, "general", toggles={"Reactions": False})
                await wait_action(alice, target, "Add reaction", False)
                await edit_channel(olive, "general", toggles={"Reactions": True})
                await wait_action(alice, target, "Add reaction", True)

                # Custom emoji off: the picker and the reaction menu's custom
                # entries go; the owner's Emoji section says it's off.
                await set_feature(olive, "custom_emoji", False)
                await alice.wait_gone('button[title="Custom emoji"]', timeout=30)
                await alice.message_action(target, "Add reaction")
                items = await alice.texts(".menu .mi")
                assert "👍" in items and ":party_parrot:" not in items, items
                await alice.click(".main-head .title")
                await alice.wait_gone(".menu")
                await community_section(olive, "Emoji")
                await olive.wait_text(".settings section p", "Custom emoji are turned off")
                await set_feature(olive, "custom_emoji", True)
                await alice.wait_js("document.querySelector('button[title=\"Custom emoji\"]')", "picker back", 30)
                await alice.message_action(target, "Add reaction")
                await alice.wait_text(".menu .mi", ":party_parrot:")
                await alice.click(".main-head .title")

                # Every change is in the audit log.
                await community_section(olive, "Audit log")
                await olive.wait_js("document.querySelectorAll('.audit-row').length > 5", "audit rows")
                rows = " | ".join(await olive.texts(".audit-row td:nth-child(3)"))
                for want in (
                    "turned link previews on",
                    "turned attachments off",
                    "turned attachments on",
                    "turned reactions off",
                    "turned custom emoji off",
                    "edited #general",
                ):
                    assert want in rows, (want, rows)
                await close_settings(olive)
                for app in (olive, alice):
                    app.assert_no_errors()
                    app.assert_local_only()
            finally:
                await olive.stop()
                await alice.stop()

        try:
            run(main(), 460)
        finally:
            site.shutdown()


TYPING_SEEN = """
(ms) => new Promise((done) => {
  let seen = false;
  const t = setInterval(() => { seen = seen || !!document.querySelector('#typing').textContent; }, 50);
  setTimeout(() => { clearInterval(t); done(seen); }, ms);
})
"""


async def profile_has_card_option(app: App) -> bool:
    await app.click(".side-head")
    await app.click(".menu .mi", "Your profile here")
    await app.wait_js("document.querySelector('.modal input[aria-label=\"Pronouns\"]')", "profile dialog")
    has = await app.exists('.modal input[aria-label="Publish a contact card here"]')
    await app.button("Cancel")
    await app.wait_gone("#modal-root .backdrop")
    return has


async def privacy_lists(app: App, community: str) -> bool:
    await open_settings(app)
    await app.click(".settings nav .item", "Privacy & DMs")
    await app.wait_text(".settings section h3", "Contact cards")
    await asyncio.sleep(0.5)  # the list of communities is fetched as the section opens
    has = await app.exists(f'.settings section input[aria-label="{community}"]')
    await close_settings(app)
    return has


def test_messaging_toggles(tmp_path):
    """Editing and deleting own messages, typing indicators, search, member
    contact cards and join/leave messages, and deleting single messages by
    clicking."""
    with run_relay() as relay, run_community(tmp_path, [relay]) as node:

        async def main():
            olive, alice, invite = await two_members(tmp_path, node, "Habit Town")
            cores = []
            try:
                await send_message(alice, "alice wrote this")
                mine = await message_selector(alice, "alice wrote this")
                await wait_action(alice, mine, "Edit", True)
                await wait_action(alice, mine, "Delete", True)

                # Editing own messages off, then on.
                await set_feature(olive, "edit_own", False)
                await wait_action(alice, mine, "Edit", False)
                await wait_action(alice, mine, "Delete", True)
                await set_feature(olive, "edit_own", True)
                await wait_action(alice, mine, "Edit", True)

                # Deleting own messages off: Alice loses Delete; the owner
                # (Manage Messages) keeps it.
                await set_feature(olive, "delete_own", False)
                await wait_action(alice, mine, "Delete", False)
                await close_settings(olive)
                await wait_action(olive, mine, "Delete", True)
                await set_feature(olive, "delete_own", True)
                await close_settings(olive)
                await wait_action(alice, mine, "Delete", True)

                # Deleting a message by clicking: her own, then the owner
                # deletes one of hers; members can't delete others' messages.
                await alice.message_action(mine, "Delete")
                await alice.wait_text(".modal h2", "Delete message?")
                await alice.button("Delete")
                await olive.wait_text(f"{mine} .mtext", "Message deleted")
                await alice.wait_text(f"{mine} .mtext", "Message deleted")
                await send_message(alice, "moderate this one")
                theirs = await message_selector(olive, "moderate this one")
                await olive.message_action(theirs, "Delete")
                await olive.button("Delete")
                await alice.wait_text(f"{theirs} .mtext", "Message deleted")
                await send_message(olive, "the owner's words")
                owners = await message_selector(alice, "the owner's words")
                await wait_action(alice, owners, "Delete", False)
                await community_section(olive, "Audit log")
                await olive.wait_text(".audit-row td", "deleted a message by Alice in #general")
                await close_settings(olive)

                # Typing indicators: shown, then off (nothing is sent or
                # shown), then on again.
                await alice.click("#composer-input")
                await alice.type("thinking")
                await olive.wait_text("#typing", "Alice is typing")
                await set_feature(olive, "typing", False)
                await close_settings(olive)
                await alice.wait_js(f"!{feature_js('typing')}", "typing off", 30)
                await olive.wait_js("!document.querySelector('#typing').textContent", "indicator expired", 15)
                await alice.click("#composer-input")
                await alice.type(" some more")
                assert not await olive.call(TYPING_SEEN, 5000), "typing shown while turned off"
                await set_feature(olive, "typing", True)
                await close_settings(olive)
                await alice.wait_js(feature_js("typing"), "typing on", 30)
                await alice.click("#composer-input")
                await alice.type(" and again")
                await olive.wait_text("#typing", "Alice is typing", 15)

                # Search off and on: the button disappears and comes back.
                search = '.main-head button[title="Search"]'
                assert await alice.exists(search)
                await set_feature(olive, "search", False)
                await alice.wait_gone(search, timeout=30)
                await set_feature(olive, "search", True)
                await close_settings(olive)
                await alice.wait_js(f"document.querySelector('{search}')", "search back", 30)

                # Member contact cards: the option in the profile dialog and
                # in Privacy settings goes while they're off.
                assert await profile_has_card_option(alice)
                assert await privacy_lists(alice, "Habit Town")
                await set_feature(olive, "contact_cards", False)
                await close_settings(olive)
                await alice.wait_js(f"!{feature_js('contact_cards')}", "cards off", 30)
                assert not await profile_has_card_option(alice)
                assert not await privacy_lists(alice, "Habit Town")
                await set_feature(olive, "contact_cards", True)
                await close_settings(olive)
                await alice.wait_js(feature_js("contact_cards"), "cards on", 30)
                assert await profile_has_card_option(alice)
                assert await privacy_lists(alice, "Habit Town")

                # Join and leave messages: on by default, in the first text
                # channel; then in a channel picked in Features; none while off.
                async def newcomer(name: str):
                    core, _ = await new_core(tmp_path / name.lower())
                    cores.append(core)
                    await core.join({"invite": invite}, "new", name)
                    cid = next(iter(core.conns))
                    await general(core, cid)
                    return core, cid

                await newcomer("Bob")
                await olive.wait_text("#messages .msg.system", "Bob joined the community")
                await create_channel(olive, "lobby")
                await community_section(olive, "Features")
                await olive.select('.settings section select[aria-label="Join and leave messages channel"]', "#lobby")
                await olive.button("Save changes")
                await olive.wait_text(".toast", "Saved")
                await close_settings(olive)
                carol, cid = await newcomer("Carol")
                await open_channel(olive, "lobby")
                await olive.wait_text("#messages .msg.system", "Carol joined the community")
                await carol.leave(cid, True)
                await olive.wait_text("#messages .msg.system", "Carol left the community")
                await set_feature(olive, "join_leave_messages", False)
                await close_settings(olive)
                await newcomer("Dan")
                await asyncio.sleep(3)
                for channel in ("lobby", "general"):
                    await open_channel(olive, channel)
                    await asyncio.sleep(0.5)
                    assert not any("Dan" in t for t in await olive.texts("#messages .msg.system")), channel
                await set_feature(olive, "join_leave_messages", True)
                await close_settings(olive)
                await newcomer("Erin")
                await open_channel(olive, "lobby")
                await olive.wait_text("#messages .msg.system", "Erin joined the community")
                await community_section(olive, "Audit log")
                await olive.wait_text(".audit-row td", "changed the join and leave channel")
                await olive.wait_text(".audit-row td", "turned join and leave messages off")
                await close_settings(olive)
                for app in (olive, alice):
                    app.assert_no_errors()
            finally:
                for core in cores:
                    await core.shutdown()
                await olive.stop()
                await alice.stop()

        run(main(), 460)


def test_history_for_new_members(tmp_path):
    """New members see history, and its channel setting, as seen through the
    app by members who join afterwards."""
    with run_relay() as relay, run_community(tmp_path, [relay]) as node:

        async def main():
            olive = await App(tmp_path / "olive", name="olive").start()
            dave = erin = None
            try:
                await create_profile(olive)
                invite = await claim(olive, node.claim_code, "Memory Town", "Olive")
                await send_message(olive, "old news in general")
                await olive.wait_text("#messages .mtext", "old news in general")
                await create_channel(olive, "random")
                await open_channel(olive, "random")
                await olive.wait_js("st.states[st.cid].channels.some((c) => c.name === 'random')", "#random")
                await asyncio.sleep(1)
                await send_message(olive, "old news in random")
                await olive.wait_text("#messages .mtext", "old news in random")
                # #general: "New members see history" off in this channel only.
                await edit_channel(olive, "general", toggles={"New members see history": False})
                await close_settings(olive)

                dave = await App(tmp_path / "dave", name="dave").start()
                await create_profile(dave)
                await join(dave, invite, "Dave")
                await open_channel(dave, "random")
                await dave.wait_text("#messages .mtext", "old news in random")
                await open_channel(dave, "general")
                await asyncio.sleep(2)
                assert "old news in general" not in await dave.texts("#messages .mtext")
                await dave.stop()
                dave = None

                # The community toggle off: nobody new sees older messages anywhere.
                await set_feature(olive, "history_for_new_members", False)
                # The channel setting is only offered while the community toggle is on.
                await community_section(olive, "Channels")
                await olive.click(".settings section .channel-row button", "Edit random")
                await olive.wait_js("document.querySelector('.channel-dialog')", "channel dialog")
                assert not await olive.exists('.channel-dialog select[aria-label="New members see history"]')
                await olive.button("Cancel")
                await close_settings(olive)
                erin = await App(tmp_path / "erin", name="erin").start()
                await create_profile(erin)
                await join(erin, invite, "Erin")
                for channel in ("random", "general"):
                    await open_channel(erin, channel)
                    await asyncio.sleep(1.5)
                    texts = await erin.texts("#messages .mtext")
                    assert not any("old news" in t for t in texts), (channel, texts)
                # On again: Erin's #random has the older message.
                await set_feature(olive, "history_for_new_members", True)
                await close_settings(olive)
                await erin.wait_js(feature_js("history_for_new_members"), "history on", 30)
                await open_channel(erin, "general")
                await open_channel(erin, "random")
                await erin.wait_text("#messages .mtext", "old news in random")
                erin.assert_no_errors()
                olive.assert_no_errors()
            finally:
                for app in (olive, dave, erin):
                    if app is not None:
                        await app.stop()

        run(main(), 400)


def test_voice_video_and_screen_share_toggles(tmp_path, tone):  # noqa: F811 (the fixture)
    """Voice channels, video and screen share turned off and on mid-call:
    the member's camera and screen buttons, and then the voice channels,
    disappear and come back."""
    with run_relay() as relay, run_community(tmp_path, [relay]) as node:

        async def main():
            olive, alice, _ = await two_members(tmp_path, node, "Call Town", env=media_env(tone))
            try:
                # Screen share is off for @everyone by default: grant it.
                await toggle_role_perms(olive, "@everyone", ("screen_share",))
                await close_settings(olive)
                await create_voice_channel(olive, "Lounge")
                await alice.wait_text(".chan.voice .cname", "Lounge")
                await join_voice(olive, "Lounge")
                await join_voice(alice, "Lounge")
                camera, share = 'button[aria-label="Turn on camera"]', 'button[aria-label="Share your screen"]'
                await alice.wait_js(f"document.querySelector('{share}')", "share button", 30)
                await alice.click(camera)
                await olive.wait_js("document.querySelector('.voice-user .flag[title=\"Camera on\"]')", "camera flag")

                # Video off: Alice's camera goes off and its button disappears.
                await set_feature(olive, "video", False)
                await alice.wait_gone('button[aria-label="Turn off camera"]', timeout=30)
                await alice.wait_gone(camera, timeout=30)
                await olive.wait_js("!document.querySelector('.voice-user .flag[title=\"Camera on\"]')", "flag gone")
                await set_feature(olive, "video", True)
                await alice.wait_js(f"document.querySelector('{camera}')", "camera button back", 40)

                # Screen share off and on.
                await set_feature(olive, "screen_share", False)
                await alice.wait_gone(share, timeout=30)
                await set_feature(olive, "screen_share", True)
                await alice.wait_js(f"document.querySelector('{share}')", "share button back", 40)

                # Voice channels off: the call ends and the channel disappears.
                await set_feature(olive, "voice", False)
                await alice.wait_gone(".voice-bar", timeout=30)
                await alice.wait_gone(".chan.voice", timeout=30)
                await set_feature(olive, "voice", True)
                await close_settings(olive)
                await alice.wait_text(".chan.voice .cname", "Lounge", 30)
                for app in (olive, alice):
                    app.assert_no_errors()
            finally:
                await olive.stop()
                await alice.stop()

        run(main(), 400)
