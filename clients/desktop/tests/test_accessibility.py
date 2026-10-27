"""Accessibility: every main view passes axe-core's
WCAG 2.1 A and AA rules (names for controls, contrast, structure) in the
light and dark themes, and the app works from the keyboard with visible
focus. axe-core is injected through DevTools for the test only
(tests/vendor/axe); the app never loads it."""

import hashlib
import sys
from pathlib import Path

import pytest
from conftest import ENGINE_TEST_FLAGS, fake_media_flags, network, run
from uidriver import App, UIError
from uiflows import (
    FAKE_SCREEN,
    claim,
    close_settings,
    community_member_menu,
    community_section,
    create_profile,
    join,
    message_action_on,
    open_members,
    open_settings,
    publish_card,
    send_message,
    set_maintainers_in_settings,
)

pytestmark = pytest.mark.gui

AXE = Path(__file__).parent / "vendor" / "axe" / "axe.min.js"


def axe_source() -> str:
    data = AXE.read_bytes()
    want = (AXE.parent / "SHA256SUMS").read_text().split()[0]
    assert hashlib.sha256(data).hexdigest() == want, "vendored axe-core changed"
    return data.decode()


async def audit(app: App, view: str) -> list[str]:
    """axe's WCAG 2.1 A/AA violations in this view, in both themes."""
    if not await app.js("typeof axe !== 'undefined'"):
        await app.js(axe_source() + "; 0")
    found = []
    before = await app.js("document.documentElement.dataset.theme || ''")
    for theme in ("light", "dark"):
        await app.js(f"document.documentElement.dataset.theme = {theme!r}; 0")
        res = await app.js(
            "axe.run(document, {runOnly: ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']})"
            ".then((r) => r.violations.map((v) => `${v.id} (${v.impact}): ${v.nodes.length}× `"
            " + v.nodes.slice(0, 3).map((n) => n.target.join(' ')).join(' | ')"
            " + ` (${(v.nodes[0].failureSummary || '').replace(/\\s+/g, ' ').slice(0, 200)})`))"
        )
        found += [f"{view} [{theme}] {v}" for v in res]
    await app.js(f"document.documentElement.dataset.theme = {before!r}; 0")
    return found


def test_views_pass_axe_and_work_from_the_keyboard(tmp_path):
    with network(tmp_path) as (_meta, _relay, community):
        node = community()

        async def main():
            app = await App(tmp_path / "a", name="a").start()
            problems: list[str] = []
            try:
                await app.wait_text("#lock button", "Create a profile")
                problems += await audit(app, "first run")
                await app.button("Create a profile")
                await app.click("#lock .generate-btn")
                await app.wait_js(
                    "document.querySelector('#lock .generated-note:not(.hidden)')", "generated passphrase"
                )
                problems += await audit(app, "create profile, generated passphrase")
                await app.button("Back")
                await create_profile(app)
                await claim(app, node.claim_code, "Access", "Olive")
                await send_message(app, "hello there")
                await app.wait_text("#messages .mtext", "hello there")
                problems += await audit(app, "community")
                # Statuses (status.js): the picker, then the member list and
                # user panel showing a status with custom text.
                for attempt in range(3):
                    await app.click(".user-panel .me-btn")
                    try:
                        await app.wait_js("document.querySelector('.status-picker')", "the status picker", 5)
                        break
                    except UIError:
                        if attempt == 2:
                            raise
                problems += await audit(app, "status picker")
                await app.click('.status-choice[data-state="dnd"]')
                await app.fill(".status-picker input[type=text]", "Checking contrast")
                await app.button("Save")
                await app.wait_gone(".status-picker")
                await open_members(app)
                await app.wait_js(
                    "document.querySelector('#members .member[data-status=dnd]')", "a status in the member list"
                )
                problems += await audit(app, "member list with statuses")
                await open_settings(app)
                for section in (
                    "Security",
                    "Privacy & DMs",
                    "Appearance",
                    "Notifications",
                    "Voice & video",
                    "Network",
                    "Data & storage",
                ):
                    await app.click(".settings nav .item", section)
                    if section == "Notifications":
                        # Sounds: every switch and Play button has its own name.
                        await app.wait_text(".settings section .form-section h3", "Sounds")
                        names = await app.call(
                            "() => [...document.querySelectorAll('.sound-switch input, .sound-switch button')]"
                            ".map((e) => e.getAttribute('aria-label'))"
                        )
                        assert len(names) == 16 and all(names) and len(set(names)) == 16, names
                        volume = 'input[type=range][aria-label="Sound volume"]'
                        assert await app.js(f"!!document.querySelector('{volume}')"), "Sound volume"
                    problems += await audit(app, f"settings/{section}")
                await app.click(".settings .close")
                # Community Admin and a dialog.
                await app.click(".side-head")
                await app.click(".menu .mi", "Community Admin")
                problems += await audit(app, "community settings")
                for section in ("Visibility", "Invites & join policy", "Files"):
                    await app.click(".settings nav .item", section)
                    await app.wait_text(".settings section h2", section)
                    problems += await audit(app, f"community settings/{section}")
                await app.click(".settings .close")
                await app.click('button[aria-label="Create text channel"]')
                problems += await audit(app, "create channel dialog")
                await app.key("Escape")
                await app.wait_gone(".modal")
                # Add a community: a page in the center section with four
                # ways in, a tab list the arrow keys move through.
                await app.click(".rail-add")
                on_join = "document.activeElement.matches('.add-way.sel[data-tab=join]')"
                await app.wait_js(on_join, "focus on the first way")
                for way in ("join", "find", "host", "server"):
                    sel = f".add-way.sel[data-tab={way}][aria-selected=true]"
                    await app.wait_js(f"document.querySelector('{sel}')", way)
                    panel = f"#add-panel[aria-labelledby=add-way-{way}] .form-section"
                    await app.wait_js(f"document.querySelector('{panel}')", way)
                    problems += await audit(app, f"add a community/{way}")
                    await app.key("ArrowRight")
                await app.wait_js(on_join, "arrow keys wrap around")
                await app.key("Escape")
                await app.wait_gone(".add-page")
                await app.click('.rail-item[title="Direct messages"]')
                problems += await audit(app, "direct messages")
                await app.js("toastError(new Error('A test error')); 0")
                await app.wait_text("#toasts .toast.error", "A test error")
                problems += await audit(app, "error toast")
                await app.click("#toasts .toast.error button", "Dismiss")

                # Keyboard: Tab reaches the channel list with a visible focus
                # ring, Enter opens a channel, and a message's actions come
                # up when it has the focus.
                await app.click(".rail-item[aria-label]", None, 1)
                await app.wait_js("document.querySelector('.chan')", "channel list")
                await app.js("document.querySelector('.side-head').focus(); 0")
                for _ in range(15):
                    await app.key("Tab")
                    if await app.js("document.activeElement && document.activeElement.classList.contains('chan')"):
                        break
                assert await app.js("document.activeElement.classList.contains('chan')"), "Tab never reached a channel"
                outline = await app.js("getComputedStyle(document.activeElement).outlineStyle")
                assert outline not in ("none", ""), outline
                await app.key("Enter")
                await app.wait_js("document.querySelector('#composer-input')", "the channel opened")
                await app.js("document.querySelector('#messages .msg').focus(); 0")
                assert await app.js(
                    "getComputedStyle(document.querySelector('#messages .msg .actions')).display !== 'none'"
                ), "message actions hidden from the keyboard"

                # Right-click menus from the keyboard only. Shift+F10 on a
                # focused channel opens its menu with focus on the first
                # entry; the arrows move; Escape closes it and focus goes
                # back to the channel. The Menu key opens it again, and
                # Enter on "Mute channel" mutes the channel.
                await app.js("document.querySelector('.side-head').focus(); 0")
                for _ in range(15):
                    await app.key("Tab")
                    if await app.js("document.activeElement.matches('.chan:not(.voice)')"):
                        break
                on_chan = "document.activeElement.matches('.chan:not(.voice)')"
                assert await app.js(on_chan), "Tab never reached a channel"
                await app.key("F10", modifiers=8)  # Shift+F10
                await app.wait_js("document.querySelector('#ctx-menu[role=menu]')", "the channel menu")
                in_menu = "document.activeElement.getAttribute('role') === 'menuitem'"
                assert await app.js(in_menu), "focus didn't move into the menu"
                first = await app.js("document.activeElement.textContent")
                assert first == await app.js("document.querySelector('#ctx-menu .mi').textContent")
                await app.key("ArrowDown")
                assert await app.js("document.activeElement.textContent") != first, "ArrowDown didn't move"
                await app.key("ArrowUp")
                assert await app.js("document.activeElement.textContent") == first, "ArrowUp didn't move back"
                problems += await audit(app, "channel menu")
                await app.key("Escape")
                await app.wait_gone("#ctx-menu")
                assert await app.js(on_chan), "focus didn't go back to the channel"
                await app.key("ContextMenu")
                await app.wait_js("document.querySelector('#ctx-menu')", "the channel menu (Menu key)")
                for _ in range(6):
                    if await app.js("document.activeElement.textContent") == "Mute channel":
                        break
                    await app.key("ArrowDown")
                assert await app.js("document.activeElement.textContent") == "Mute channel"
                await app.key("Enter")
                await app.wait_gone("#ctx-menu")
                await app.wait_js(
                    "document.querySelector('.chan .muted-flag[role=img][aria-label=\"Muted\"] svg')",
                    "the channel's muted mark, named Muted",
                )
                await app.wait_js(on_chan, "focus back on the (redrawn) channel")
                # The message's reaction menu (Add reaction) works the same from the keyboard.
                await app.js("document.querySelector('#messages .msg').focus(); 0")
                seen = []
                for _ in range(8):
                    await app.key("Tab")
                    seen.append(await app.js("document.activeElement.outerHTML.slice(0, 100)"))
                    if await app.js("document.activeElement.getAttribute('aria-label') === 'Add reaction'"):
                        break
                assert await app.js("document.activeElement.getAttribute('aria-label') === 'Add reaction'"), seen
                await app.key("Enter")
                await app.wait_js(in_menu, "focus in the reaction menu")
                problems += await audit(app, "reaction menu")
                await app.key("Escape")
                await app.wait_gone("#ctx-menu")
                assert await app.js("document.activeElement.getAttribute('aria-label') === 'Add reaction'"), (
                    "focus didn't go back to the reaction button"
                )
                # A message's author: Tab from the message reaches the name,
                # and Shift+F10 opens the person's menu (people.js) with focus
                # on its first entry; Escape gives the focus back to the name.
                await app.js("document.querySelector('#messages .msg.first:not(.system)').focus(); 0")
                await app.key("Tab")
                on_author = "document.activeElement.matches('#messages .msg .author')"
                assert await app.js(on_author), "Tab didn't reach the author's name"
                await app.key("F10", modifiers=8)  # Shift+F10
                await app.wait_js(in_menu, "focus in the person's menu")
                assert await app.js("document.activeElement.textContent") == "View profile"
                problems += await audit(app, "person menu")
                await app.key("Escape")
                await app.wait_gone("#ctx-menu")
                assert await app.js(on_author), "focus didn't go back to the author's name"

                # Message search from the keyboard: Tab to a result, Enter
                # jumps to the message.
                await app.click('button[aria-label="Search"]')
                await app.wait_js("document.activeElement.type === 'search'", "focus in the search field")
                await app.type("hello")
                await app.key("Enter")
                await app.wait_js("document.querySelector('.modal .result')", "a search result")
                for _ in range(10):
                    await app.key("Tab")
                    if await app.js("document.activeElement.classList.contains('result')"):
                        break
                assert await app.js("document.activeElement.classList.contains('result')"), "Tab never reached a result"
                await app.key("Enter")
                await app.wait_gone(".modal")
                await app.wait_js("document.querySelector('#messages .msg.flash')", "the message it jumped to")

                # Dialogs on top of dialogs: focus goes in, Tab stays in the
                # top one, Escape closes only that one and focus goes back.
                await open_settings(app)
                await app.click(".settings nav .item", "Security")
                await app.click(".settings section button", "Change passphrase…")
                top = "[...document.querySelectorAll('#modal-root [role=dialog]')].pop()"
                await app.wait_js(f"{top}.contains(document.activeElement)", "focus in the new dialog")
                label = await app.js(f"document.getElementById({top}.getAttribute('aria-labelledby')).textContent")
                assert label == "Change passphrase", label
                for _ in range(12):
                    await app.key("Tab")
                    assert await app.js(f"{top}.contains(document.activeElement)"), "Tab left the dialog"
                await app.key("Tab", modifiers=8)  # Shift+Tab
                assert await app.js(f"{top}.contains(document.activeElement)"), "Shift+Tab left the dialog"
                await app.key("Escape")
                await app.wait_gone(".modal h2", "Change passphrase")
                assert await app.exists(".settings"), "Escape closed the settings under the dialog too"
                assert await app.js("document.activeElement.textContent.trim()") == "Change passphrase…"
                await app.key("Escape")
                await app.wait_gone(".settings")
                # Settings is a page, not a dialog: Escape put the channel
                # back and the focus on the button that opened it.
                await app.wait_js("document.querySelector('#composer-input')", "the channel back")
                assert await app.js("document.activeElement.matches('.user-panel button[aria-label=\"Settings\"]')"), (
                    "focus didn't go back to the Settings button"
                )
                assert await app.js("document.title") == "OpenArcanum"

                # Pages: Settings and Community Admin open in the centre of
                # the window, beside the channel list, with the focus on
                # their heading and the window's title saying where you are.
                await open_settings(app)
                page = "document.querySelector('#main > .page.settings')"
                assert await app.js(f"!!{page} && !document.querySelector('#modal-root .backdrop')"), "not a page"
                assert await app.js("document.activeElement.matches('#main .page-head h1')"), "focus not on the heading"
                assert await app.js("document.activeElement.textContent") == "Settings"
                assert (await app.js("document.title")).endswith("Settings · OpenArcanum")
                assert await app.js("getComputedStyle(document.querySelector('#members')).display === 'none'")
                current = await app.js("document.querySelector('.page-nav .item.sel').getAttribute('aria-current')")
                assert current == "page", current
                # The rest of the window still works: a channel leaves the page.
                await app.click(".chan .cname", "general")
                await app.wait_gone(".settings")
                await app.wait_js("document.querySelector('#composer-input')", "the channel")
                # Community Admin: its title names the community; Close
                # (at the top) leaves it, and the focus goes back to the
                # community menu that opened it.
                await open_settings(app, "community")
                assert await app.js("document.activeElement.textContent") == "Community Admin"
                assert await app.js("document.querySelector('.page-head .page-context').textContent") == "Access"
                assert "Community Admin · Access" in await app.js("document.title")
                problems += await audit(app, "community admin page")
                await app.click(".page-head .close")
                await app.wait_gone(".settings")
                await app.wait_js("document.activeElement.matches('.side-head')", "focus back on the community menu")
                # Direct messages leave a page too.
                await open_settings(app)
                await app.click('.rail-item[title="Direct messages"]')
                await app.wait_gone(".settings")
            finally:
                await app.stop()
            print("\n".join(problems))
            assert not problems, "\n".join(problems)

        run(main(), 300)


@pytest.fixture(scope="module")
def tone(tmp_path_factory):
    sys.path.insert(0, str(Path(__file__).parent / "webengine"))
    import callharness

    path = tmp_path_factory.mktemp("media") / "tone.wav"
    callharness.tone_bursts(path)
    return path


async def set_maintainer(app: App, url: str) -> None:
    await set_maintainers_in_settings(app, [url])


def test_call_menus_queues_prompts_and_group_dms_pass_axe(tmp_path, tone):
    """The rest of the app's surfaces: the call stage and voice controls,
    the voice and member context menus (opened from the keyboard too), the
    moderation queues with a report in them, the update prompt and the
    group-DM dialogs pass axe in both themes."""
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            env = {"QTWEBENGINE_CHROMIUM_FLAGS": (f"{fake_media_flags(tone)} {ENGINE_TEST_FLAGS}")}
            olive = await App(tmp_path / "olive", env=env, name="olive").start()
            alice = await App(tmp_path / "alice", env=env, name="alice").start()
            problems: list[str] = []
            try:
                for app in (olive, alice):
                    await create_profile(app)
                    await set_maintainer(app, meta)
                invite = await claim(olive, node.claim_code, "Access", "Olive")
                await join(alice, invite, "Alice")
                await publish_card(olive)
                await publish_card(alice)

                # A report in the moderation queue, and the approval queue.
                await send_message(olive, "report me")
                await message_action_on(alice, "report me", "Report")
                await alice.fill(".modal textarea", "testing the queue")
                await alice.button("Report")
                await alice.wait_text(".toast", "Reported")
                await community_section(olive, "Reports")
                await olive.wait_text(".settings .report-row", "testing the queue")
                problems += await audit(olive, "moderation queue (reports)")
                await community_section(olive, "Approval queue")
                problems += await audit(olive, "approval queue")
                await close_settings(olive)

                # A member's context menu, from the member list.
                await community_member_menu(olive, "Alice")
                await olive.wait_js("document.querySelector('#ctx-menu')", "member menu")
                problems += await audit(olive, "member menu")
                await olive.key("Escape")
                await olive.wait_gone("#ctx-menu")

                # In a call: the stage, the voice bar and its controls.
                await olive.click('button[aria-label="Create voice channel"]')
                await olive.fill(".modal input", "Lounge")
                await olive.button("Create")
                await olive.click(".chan.voice .cname", "Lounge")
                await olive.wait_text(".voice-bar", "Voice connected", 40)
                await olive.wait_js("document.querySelector('.stage .tile')", "the call stage")
                problems += await audit(olive, "call stage and voice controls")

                # The voice participant menu from the keyboard: Tab reaches
                # the row's options button (shown on focus), Enter opens the
                # menu with focus on its first item, the arrows move, Escape
                # closes it and focus goes back.
                focused = await olive.js(
                    # The row's join control (the row holds it and the Chat
                    # button side by side, so it isn't a control itself).
                    "(() => { const c = document.querySelector('.chan.voice .chan-join'); c.focus();"
                    " return document.activeElement === c; })()"
                )
                assert focused, "the voice channel row isn't focusable"
                opts = "document.activeElement.getAttribute('aria-label') === 'Options for Olive'"
                seen = []
                for _ in range(10):
                    await olive.key("Tab")
                    seen.append(await olive.js("document.activeElement.outerHTML.slice(0, 120)"))
                    if await olive.js(opts):
                        break
                assert await olive.js(opts), ("Tab never reached the voice row's options", seen)
                shown = "getComputedStyle(document.activeElement).opacity === '1'"
                assert await olive.js(shown), "the focused options button isn't shown"
                outline = await olive.js("getComputedStyle(document.activeElement).outlineStyle")
                assert outline not in ("none", ""), outline
                await olive.key("Enter")
                # Her own row: no volume or Mute for me, the moderator's
                # entries; focus starts on the first.
                await olive.wait_js("document.activeElement.matches('#ctx-menu .mi')", "focus in the menu")
                assert await olive.js("document.activeElement.getAttribute('role')") == "menuitem"
                assert "Mute for me" not in await olive.texts("#ctx-menu .mi")
                problems += await audit(olive, "voice participant menu")
                await olive.key("Escape")
                await olive.wait_gone("#ctx-menu")
                assert await olive.js(opts), "focus didn't go back to the options button"

                # The voice bar's menus, from the keyboard: an icon's arrow,
                # Enter. The sound menu starts on its volume slider; the
                # arrows go on to the devices; Escape goes back to the arrow.
                for label in ("Microphone options", "Sound options", "Camera options", "Screen share options"):
                    arrow = f"document.querySelector('.voice-bar button[aria-label=\"{label}\"]')"
                    await olive.js(f"{arrow}.focus()")
                    await olive.key("Enter")
                    await olive.wait_js("document.activeElement.closest('#ctx-menu')", f"focus in {label}")
                    if label == "Sound options":
                        assert await olive.js("document.activeElement.matches('#ctx-menu .mi-range input[type=range]')")
                        await olive.key("ArrowDown")
                        assert await olive.js("document.activeElement.getAttribute('role')") == "menuitemradio"
                    problems += await audit(olive, f"voice bar: {label}")
                    await olive.key("Escape")
                    await olive.wait_gone("#ctx-menu")
                    assert await olive.js(f"document.activeElement === {arrow}"), f"focus didn't go back from {label}"

                # Streams on the stage: Olive's camera and screen (the stand-in
                # capture Xvfb needs, uiflows.FAKE_SCREEN); Alice, in
                # the call, sees them as blocks to click to watch, marked Live,
                # then watched and in focus.
                await olive.js(FAKE_SCREEN)
                await olive.click('.voice-bar button[aria-label="Turn on camera"]')
                await olive.click('.voice-bar button[aria-label="Share your screen"]')
                await alice.click(".chan.voice .cname", "Lounge")
                await alice.wait_text(".voice-bar", "Voice connected", 40)
                await alice.wait_js(
                    "document.querySelector('.stage .video-tile.screen.unwatched .live-badge')", "olive's screen"
                )
                problems += await audit(alice, "call stage: streams to click")
                problems += await audit(olive, "call stage: own camera and screen, Live in the list")
                await alice.click(".stage .video-tile.screen .tile-main", "Watch Olive's screen")
                await alice.wait_js("document.querySelector('.stage.has-focus .video-tile.screen.watched')", "in focus")
                problems += await audit(alice, "call stage: a stream in focus")
                await alice.click('button[aria-label="Leave call"]')
                await alice.wait_gone(".voice-bar")
                await olive.click('.voice-bar button[aria-label="Stop sharing"]')
                await olive.click('.voice-bar button[aria-label="Turn off camera"]')

                # The update prompt, as the page shows it for the state
                # Python sends when a verified update is ready (the whole
                # flow runs in test_ui_updates.py).
                await olive.js(
                    "updateChanged({state: 'ready', prompt: true, version: '9.9.9', current: '1.0.0',"
                    " token: 'a11y', message: 'Version 9.9.9 is ready to install.'}); 0"
                )
                await olive.wait_text(".update-dialog h2", "Update ready")
                problems += await audit(olive, "update prompt")
                await olive.key("Escape")
                await olive.wait_gone(".update-dialog")
                await olive.click('button[aria-label="Leave call"]')
                await olive.wait_gone(".voice-bar")

                # Group DMs: the new-group picker and the group's details.
                await community_member_menu(alice, "Olive")
                await alice.click(".menu .mi", "Add as contact")
                await olive.click('.rail-item[title="Direct messages"]')
                await olive.wait_text(".side-head .name", "Direct messages")
                await olive.click('button[aria-label="New group DM"]')
                await olive.wait_text(".modal .pick-list label", "Alice")
                problems += await audit(olive, "new group DM dialog")
                await olive.click(".modal .pick-list input[type=checkbox]", "Alice")
                await olive.fill(".modal input[placeholder='Optional']", "Plans")
                await olive.button("Create")
                await olive.wait_text(".main-head .title", "Plans")
                problems += await audit(olive, "group DM")
                await olive.click('button[aria-label="Conversation details"]')
                await olive.wait_text(".modal h2", "Group details")
                problems += await audit(olive, "group DM details")
                await olive.click('.modal button[aria-label="Options for Alice"]')
                await olive.wait_js("document.querySelector('#ctx-menu')", "group member menu")
                problems += await audit(olive, "group member menu")
                await olive.key("Escape")
                await olive.wait_gone("#ctx-menu")
                assert await olive.exists(".modal h2", "Group details"), "Escape closed the dialog under the menu"
                await olive.button("Add people")
                await olive.wait_text(".modal h2", "Add people")
                problems += await audit(olive, "add people dialog")
                await olive.button("Cancel")
            finally:
                await olive.stop()
                await alice.stop()
            print("\n".join(problems))
            assert not problems, "\n".join(problems)

        run(main(), 400)


def test_maintainer_editor_from_the_keyboard_passes_axe(tmp_path):
    """The maintainer nodes row and editor on the screens before unlock, with
    the keyboard only: Tab reaches Change…, Enter opens the dialog with focus
    in the address field and its name from its title; an invalid address is
    announced next to the field (aria-invalid, role=alert); Enter adds a node;
    a Remove button (named for its node) removes one and focus moves on; Tab
    stays in the dialog; Save closes it and focus goes back to Change…. The
    screens and the dialog (with and without an error) pass axe."""

    async def main():
        app = await App(tmp_path / "a", name="a").start()
        problems: list[str] = []
        focused = "document.activeElement"
        dialog = "[...document.querySelectorAll('#modal-root [role=dialog]')].pop()"
        try:
            await app.button("Create a profile")
            await app.wait_text("#lock .maintainer-row", "Maintainer nodes")
            problems += await audit(app, "create profile")
            await app.js("document.querySelector('#lock input[type=password]').focus(); 0")
            for _ in range(20):
                await app.key("Tab")
                if await app.js(f"{focused}.getAttribute('aria-label') === 'Change maintainer nodes'"):
                    break
            assert await app.js(f"{focused}.getAttribute('aria-label') === 'Change maintainer nodes'"), (
                "Tab never reached Change…"
            )
            outline = await app.js(f"getComputedStyle({focused}).outlineStyle")
            assert outline not in ("none", ""), outline
            await app.key("Enter")
            await app.wait_js(
                f"{dialog} && {dialog}.contains({focused}) && {focused}.tagName === 'INPUT'", "focus in the field"
            )
            name = await app.js(f"document.getElementById({dialog}.getAttribute('aria-labelledby')).textContent")
            assert name == "Maintainer nodes", name
            assert await app.js(f"{focused}.labels[0].textContent") == "Add a node"
            problems += await audit(app, "maintainer editor")

            await app.type("ftp://node.example.invalid")
            await app.key("Enter")
            await app.wait_text(".maintainer-error", "Use https://")
            assert await app.js(f"{focused}.getAttribute('aria-invalid') === 'true'")
            err_id = await app.js(f"{focused}.getAttribute('aria-describedby')")
            assert await app.js(f"document.getElementById({err_id!r}).getAttribute('role')") == "alert"
            problems += await audit(app, "maintainer editor with an error")

            await app.js(f"{focused}.select(); 0")
            await app.type("http://127.0.0.1:7100")
            await app.key("Enter")
            await app.wait_text(".maintainer-item .mono", "http://127.0.0.1:7100")
            await app.wait_text(".maintainer-editor [role=status]", "Added http://127.0.0.1:7100")
            assert await app.js(f"{focused}.getAttribute('aria-invalid')") is None

            # Shift+Tab back to the last Remove button, which is named for its node.
            await app.key("Tab", modifiers=8)
            assert await app.js(f"{focused}.getAttribute('aria-label')") == "Remove http://127.0.0.1:7100"
            await app.key("Tab", modifiers=8)
            first = await app.js(f"{focused}.getAttribute('aria-label')")
            assert first.startswith("Remove https://maintainer"), first
            await app.key("Enter")
            await app.wait_text(".maintainer-editor [role=status]", "Removed")
            assert await app.js(f"{dialog}.contains({focused}) && {focused}.textContent === 'Remove'"), (
                "focus didn't move to the next Remove button"
            )
            for _ in range(14):
                await app.key("Tab")
                assert await app.js(f"{dialog}.contains({focused})"), "Tab left the dialog"
            for _ in range(14):
                await app.key("Tab")
                if await app.js(f"{focused}.textContent === 'Save'"):
                    break
            await app.key("Enter")
            await app.wait_gone(".maintainer-editor")
            assert await app.js(f"{focused}.getAttribute('aria-label') === 'Change maintainer nodes'"), (
                "focus didn't go back to Change…"
            )
            await app.wait_text("#lock .maintainer-source", "(your own list)")
            problems += await audit(app, "create profile with own nodes")
            # Escape closes the editor without saving.
            await app.key("Enter")
            await app.wait_js(f"{dialog} && {dialog}.contains({focused})", "the editor again")
            await app.key("Escape")
            await app.wait_gone(".maintainer-editor")
            assert await app.js(f"{focused}.getAttribute('aria-label') === 'Change maintainer nodes'")
        finally:
            await app.stop()
        print("\n".join(problems))
        assert not problems, "\n".join(problems)

    run(main(), 120)
