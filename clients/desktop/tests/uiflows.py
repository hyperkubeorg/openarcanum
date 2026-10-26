"""Reusable click-through steps for UI tests (see uidriver.py)."""

from __future__ import annotations

from conftest import PASS
from uidriver import App


async def create_profile(app: App) -> None:
    await app.button("Create a profile")
    await app.wait_js("document.querySelectorAll('#lock input[type=password]').length === 2", "passphrase fields")
    await app.fill("#lock input[type=password]", PASS, nth=0)
    await app.fill("#lock input[type=password]", PASS, nth=1)
    await app.button("Create profile")
    await app.wait_js("!document.querySelector('#app').classList.contains('hidden')", "main window", 40)


async def unlock(app: App) -> None:
    await app.fill("#lock input[type=password]", PASS)
    await app.button("Unlock")
    await app.wait_js("!document.querySelector('#app').classList.contains('hidden')", "unlocked", 40)


async def ask_for_passphrase_at_start(app: App) -> None:
    """Settings → Security → "Ask for my passphrase when OpenArcanum starts":
    a new profile stays signed in, so a test that restarts the app and wants
    the unlock screen turns this on first."""
    box = 'input[aria-label="Ask for my passphrase when OpenArcanum starts"]'
    await open_settings(app)
    await app.click(".settings nav .item", "Security")
    await app.click(box)
    await app.fill('.modal input[aria-label="Passphrase"]', PASS)
    await app.button("Ask for it")
    await app.wait_gone(".modal h2", "Ask for your passphrase")
    await app.wait_js(f"document.querySelector('{box}').checked", "asks at start")
    await app.click(".settings .close")
    await app.wait_gone(".settings")


async def replace_maintainers(app: App, urls: list[str]) -> None:
    """Maintainer nodes → Change… (on a lock screen or in Settings → Network):
    remove every node in the list, add `urls`, Save."""
    await app.button("Change maintainer nodes")
    await app.wait_text(".modal h2", "Maintainer nodes")
    while await app.exists(".maintainer-item button"):
        await app.click(".maintainer-item button")
    for url in urls:
        await app.fill(".maintainer-editor input", url)
        await app.button("Add")
        await app.wait_text(".maintainer-item .mono", url.rstrip("/"))
    await app.button("Save")
    await app.wait_gone(".maintainer-editor")
    await app.wait_text(".maintainer-source", "your own list")


async def set_maintainers_in_settings(app: App, urls: list[str]) -> None:
    """Settings → Network → Maintainer nodes → Change…."""
    await open_settings(app)
    await app.click(".settings nav .item", "Network")
    await replace_maintainers(app, urls)
    await close_settings(app)


async def claim(app: App, claim_code: str, community: str, display_name: str) -> str:
    """Claim a server and create a community. Returns the first invite."""
    await app.click(".rail-add")
    await app.click('.add-way[data-tab="server"]')
    await app.fill(".add-page textarea", claim_code)
    await app.button("Next")
    await app.fill('.add-page input[placeholder="Community name"]', community)
    await app.fill('.add-page input[placeholder="Your display name"]', display_name)
    await app.button("Claim and create")
    await app.wait_js(
        "[...document.querySelectorAll('.invite-code .code-text')]"
        ".some((e) => e.textContent.startsWith('openarcanum-invite:'))",
        "claimed dialog",
        40,
    )
    invite = await app.js(
        "[...document.querySelectorAll('.invite-code .code-text')].map((e) => e.textContent)"
        ".find((t) => t.startsWith('openarcanum-invite:'))"
    )
    await app.button("Export backup…")
    await app.button("Cancel")
    await app.wait_js("document.querySelector('#composer-input')", "composer", 30)
    await app.wait_gone("#modal-root .backdrop")
    return invite


async def join(app: App, invite: str, display_name: str) -> None:
    await app.click(".rail-add")
    await app.click('.add-way[data-tab="join"]')
    await app.fill(".add-page textarea", invite)
    await app.button("Continue")
    await app.fill('.add-page input[placeholder="How others will see you here"]', display_name)
    await app.button("Join")
    await app.wait_js("document.querySelector('#composer-input')", "joined community", 40)
    # The key backup reminder after joining.
    await app.wait_text(".modal h2", "Back up your keys")
    await app.button("Later")
    await app.wait_gone("#modal-root .backdrop")


async def send_message(app: App, text: str, input_sel: str = "#composer-input") -> None:
    await app.click(input_sel)
    await app.type(text)
    await app.key("Enter")


# Settings and Community Admin are pages in the centre of the window
# (showPage in dom.js), not dialogs: `.settings` is the page, its sections'
# list is `.settings nav .item`, the open section `.settings section` (its
# heading an h2), and Close is at the top (`.page-head .close`).
SETTINGS_PAGE = "document.querySelector('#main > .page.settings')"


async def open_settings(app: App, which: str = "user") -> None:
    if which == "user":
        await app.click('.user-panel button[title="Settings"]')
    else:
        await app.click(".side-head")
        await app.click(".menu .mi", "Community Admin")
    await app.wait_js(SETTINGS_PAGE, "settings")
    await app.wait_js("document.querySelector('.settings section h2')", "a settings section")


async def walk_settings(app: App, label: str) -> None:
    """Open every section of the open settings window and check none shows an error."""
    names = await app.texts(".settings nav .item")
    for name in names:
        await app.click(".settings nav .item", name)
        await app.wait_js("document.querySelector('.settings section')", f"{label} → {name}")
        errors = await app.call(
            "() => [...document.querySelectorAll('.settings section .error')]"
            ".map((e) => e.textContent.trim()).filter(Boolean)"
        )
        assert not errors, f"{label} → {name}: {errors}"
    await close_settings(app)


async def community_member_menu(app: App, name: str) -> None:
    """Open the member list (if closed) and a member's menu."""
    if await app.js("document.querySelector('#members').classList.contains('hidden')"):
        await app.click('button[title="Members"]')
    await app.click("#members .member", name)


# ---------------------------------------------------------------- community administration


async def community_section(app: App, label: str) -> None:
    """Open Community Admin (if closed) at one section."""
    if not await app.exists(".settings"):
        await open_settings(app, "community")
    await app.click(".settings nav .item", label)
    await app.wait_text(".settings section h2", label)


async def close_settings(app: App) -> None:
    """Close (at the top of the page): the page goes and the view under it
    (a channel, a conversation, or the empty state) comes back."""
    await app.click(".settings .page-head .close")
    await app.wait_gone(".settings")
    await app.wait_js(
        "!document.querySelector('#main .page') && !!document.querySelector('#main').firstElementChild", "the view back"
    )


async def create_category(app: App, name: str) -> None:
    await community_section(app, "Channels")
    await app.click(".settings section button", "New category")
    await app.fill(".modal input", name)
    await app.button("Create")
    await app.wait_text(".settings section .category-row b", name.upper())


async def edit_channel(
    app: App,
    channel: str,
    category: str | None = None,
    topic: str | None = None,
    slowmode: str | None = None,
    toggles: dict[str, bool] | None = None,
    keep_messages: str | None = None,
) -> None:
    """Channels → Edit…: optionally move to a category, set the topic and
    slowmode, the channel-level feature settings (by label: True follows the
    community setting, False is "Off in this channel") and how long messages
    are kept ("" for the community default)."""
    await community_section(app, "Channels")
    await app.click(".settings section .channel-row button", f"Edit {channel}")
    await app.wait_js("document.querySelector('.channel-dialog')", "channel dialog")
    if category is not None:
        await app.select('select[aria-label="Category"]', category)
    if topic is not None:
        await app.fill('textarea[aria-label="Channel topic"]', topic)
    if slowmode is not None:
        await app.select('select[aria-label="Slowmode"]', slowmode)
    for label, on in (toggles or {}).items():
        await app.select(
            f'.channel-dialog select[aria-label="{label}"]', "Community setting" if on else "Off in this channel"
        )
    if keep_messages is not None:
        await app.fill('.channel-dialog input[aria-label="Keep messages (days)"]', keep_messages)
    await app.button("Save")
    await app.wait_gone(".channel-dialog")


async def create_role(app: App, name: str, color: str = "", hoist: bool = False, perms: tuple[str, ...] = ()) -> None:
    """Roles → Create role…, with a color, "display separately" and permissions (by name)."""
    await community_section(app, "Roles")
    await app.click(".settings section button", "Create role…")
    await app.wait_js("document.querySelector('.role-dialog')", "role dialog")
    await app.fill('input[aria-label="Role name"]', name)
    if color:
        await app.fill('input[aria-label="Role color"]', color)
    if hoist:
        await app.click(".role-dialog input[type=checkbox]", "Display separately")
    for p in perms:
        await app.click(f'.role-dialog input[data-perm="{p}"]')
    await app.button("Create")
    await app.wait_text(".settings section .role-row b", name)


async def toggle_role_perms(app: App, role: str, perms: tuple[str, ...], mentionable: bool | None = None) -> None:
    """Roles → Edit… on one role: click each named permission's box (turning
    it on or off) and optionally set "Allow anyone to @mention this role"."""
    await community_section(app, "Roles")
    await app.click(".settings section .role-row button", f"Edit {role}")
    await app.wait_js("document.querySelector('.role-dialog')", "role dialog")
    for p in perms:
        await app.click(f'.role-dialog input[data-perm="{p}"]')
    if mentionable is not None:
        sel = '.role-dialog input[aria-label="Allow anyone to @mention this role"]'
        if await app.js(f"document.querySelector('{sel}').checked") != mentionable:
            await app.click(sel)
    await app.button("Save")
    await app.wait_gone(".role-dialog")


async def assign_role(app: App, member: str, role: str) -> None:
    """Members → Roles… for one member: tick a role and save."""
    await community_section(app, "Members")
    await app.click(".settings section .member-admin-row button", f"Roles of {member}")
    await app.click(".modal input[type=checkbox]", role)
    await app.button("Save")
    await app.wait_gone(".role-assign")


async def member_override(app: App, channel: str, member: str, member_key: str, perm: str, mode: str) -> None:
    """Channels → Permissions…: add an override for one member and set one
    permission to deny, inherit or allow."""
    await community_section(app, "Channels")
    await app.click(".settings section .channel-row button", f"Permissions of {channel}")
    await app.wait_js("document.querySelector('.overrides')", "overrides dialog")
    await app.select('select[aria-label="Member to add"]', member)
    await app.button("Add member override")
    target = f'.override[data-target="m:{member_key}"] .tri[data-perm="{perm}"] button.{mode}'
    await app.click(target)
    await app.wait_js(f"document.querySelector('{target}').classList.contains('sel')", "override selected")
    await app.button("Save")
    await app.wait_gone(".overrides")


async def set_feature(app: App, feature: str, on: bool) -> None:
    """Features: turn one community toggle on or off and save."""
    await community_section(app, "Features")
    sel = f'.settings section input[data-feature="{feature}"]'
    if await app.js(f"document.querySelector('{sel}').checked") != on:
        await app.click(sel)
    await app.button("Save changes")
    await app.wait_js(f"document.querySelector('{sel}').checked === {str(on).lower()}", "toggle saved")


# ---------------------------------------------------------------- messaging (test_ui_messaging.py)


async def message_selector(app: App, text: str, timeout: float = 30.0) -> str:
    """A selector for the (last) channel message whose own text (not a reply
    quote) contains `text`."""
    await app.wait_text("#messages .msg .mtext", text, timeout)
    mid = await app.call(
        "(t) => { const m = [...document.querySelectorAll('#messages .msg')]"
        ".filter((e) => [...e.querySelectorAll('.mtext')].some((x) => x.textContent.includes(t))).pop();"
        " return m ? m.dataset.id : null; }",
        text,
    )
    return f'#messages .msg[data-id="{mid}"]'


async def open_channel(app: App, name: str) -> None:
    await app.click(".chan .cname", name)
    await app.wait_js(
        f"(document.querySelector('.main-head .title') || {{}}).textContent === {name!r}",
        f"#{name} open",
    )


async def create_channel(app: App, name: str) -> None:
    await app.click('.side-group button[title="Create text channel"]')
    await app.fill(".modal input", name)
    await app.button("Create")
    await app.wait_text(".chan .cname", name)


async def react(app: App, msg_sel: str, emoji: str) -> None:
    await app.message_action(msg_sel, "Add reaction")
    await app.click(".menu .mi", emoji)


async def attach_files(app: App, paths: list[str], caption: str = "") -> None:
    """Send one message with several files through the Attach files dialog,
    typing the paths (native file dialogs can't be driven)."""
    if caption:
        await app.click("#composer-input")
        await app.type(caption)
    await app.click('button[aria-label="Attach files"]')
    for p in paths:
        await app.click('.modal input[aria-label="File path"]')
        await app.type(p)
        await app.key("Enter")
        await app.wait_text(".modal .mono", p)
    await app.button("Send")
    await app.wait_gone("#modal-root .backdrop")


async def edit_my_profile(app: App, nickname: str | None = None, pronouns: str | None = None) -> None:
    await app.click(".side-head")
    await app.click(".menu .mi", "Your profile here")
    if nickname is not None:
        await app.fill('.modal input[aria-label="Nickname"]', nickname)
    if pronouns is not None:
        await app.fill('.modal input[aria-label="Pronouns"]', pronouns)
    await app.button("Save")
    await app.wait_gone("#modal-root .backdrop")


async def open_members(app: App) -> None:
    if await app.js("document.querySelector('#members').classList.contains('hidden')"):
        await app.click('button[title="Members"]')
    await app.wait_js("document.querySelector('#members .member')", "member list")


async def member_action(app: App, name: str, action: str) -> None:
    """Pick an action ("Time out…", "Ban…", ...) from a member's menu."""
    await community_member_menu(app, name)
    await app.click(".menu .mi", action)


async def message_index(app: App, text: str) -> int:
    """Index of the newest message in the open channel whose text contains `text`."""
    idx = await app.call(
        "(t) => { const all = [...document.querySelectorAll('#messages .msg')];"
        " for (let i = all.length - 1; i >= 0; i--) {"
        " const x = all[i].querySelector('.mtext'); if (x && x.textContent.includes(t)) return i; } return -1; }",
        text,
    )
    if idx < 0:
        raise AssertionError(f"{app.name}: no message containing {text!r}")
    return idx


# A screen share under Xvfb, where the system can't capture the screen: the
# page's capture returns the fake camera instead (a stand-in device, like
# the fake microphone), with sound if asked for.
FAKE_SCREEN = (
    "navigator.mediaDevices.getDisplayMedia = (c) => navigator.mediaDevices.getUserMedia("
    "{ video: true, audio: !!(c && c.audio) }); 0"
)


async def message_action_on(app: App, text: str, action: str) -> None:
    """Hover the message containing `text` and click one of its actions."""
    await app.wait_text("#messages .mtext", text)
    await app.message_action("#messages .msg", action, await message_index(app, text))
