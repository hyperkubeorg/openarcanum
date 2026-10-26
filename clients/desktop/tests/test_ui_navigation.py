"""Navigation and attention by clicks, in two real client windows (the owner
"Tester" and the member "Alice"):

- new messages show as badges, never as pop-ups;
- Community Admin, a red entry in the community menu, only for members
  with an admin or moderator permission (it appears when Alice is given
  one); no separate button in the channel list;
- the community icon's right-click menu (and the Menu key / Shift+F10):
  notification level, mute, mark all as read, Community Admin, Reports,
  copy the ID, leave (with a confirmation), by role;
- the moderator badge trail: Alice reports a message, and the owner follows
  the badges from the community's icon to the community menu, Community
  Admin, the Moderation group and Reports, to the report; resolving it
  clears every badge. A join request does the same for the Approval queue.
"""

import pytest
from conftest import new_core, run, run_community, run_relay
from test_accessibility import audit
from uidriver import App
from uiflows import (
    assign_role,
    claim,
    close_settings,
    community_section,
    create_channel,
    create_profile,
    create_role,
    join,
    message_action_on,
    open_channel,
    send_message,
)

pytestmark = [pytest.mark.gui, pytest.mark.timeout(400)]

RECORD_TOASTS = (
    "window.seenToasts = []; new MutationObserver(() => document.querySelectorAll('#toasts .toast')"
    ".forEach((t) => window.seenToasts.push(t.textContent)))"
    ".observe(document.querySelector('#toasts'), { childList: true }); 0"
)
RAIL = ".rail-item[data-community]"


def badge(sel: str, n: int) -> str:
    """A moderator badge showing n inside sel (JS expression)."""
    return f"!!document.querySelector('{sel} [data-count=\"{n}\"]')"


async def rail_menu(app: App) -> list[str]:
    """Right-click the community's icon; the menu's entries (their names)."""
    await app.right_click(RAIL)
    await app.wait_js("document.querySelector('#ctx-menu')", "the community icon's menu")
    return await app.call(
        "() => [...document.querySelectorAll('#ctx-menu .mi')].map((e) => e.getAttribute('aria-label'))"
    )


async def close_menu(app: App) -> None:
    await app.key("Escape")
    await app.wait_gone("#ctx-menu")


def test_badges_admin_button_rail_menu_and_report_trail(tmp_path):
    with run_relay() as relay, run_community(tmp_path, [relay]) as node:

        async def main():
            owner = await App(tmp_path / "owner", name="owner").start()
            alice = await App(tmp_path / "alice", name="alice").start()
            cores = []
            problems: list[str] = []
            try:
                await create_profile(owner)
                invite = await claim(owner, node.claim_code, "Badge Town", "Tester")
                await create_profile(alice)
                await join(alice, invite, "Alice")

                # Community Admin: a red entry in the owner's community menu,
                # not a button in the channel list; Alice, with no admin
                # permission, doesn't have it.
                await owner.click(".side-head")
                await owner.find('#ctx-menu .mi.admin[aria-label="Community Admin"]')
                # Its text is the theme's red (a probe resolves --danger-text).
                red = await owner.call(
                    "() => { const p = document.createElement('span'); p.className = 'danger-text';"
                    " document.body.appendChild(p); const c = getComputedStyle(p).color; p.remove();"
                    " return [getComputedStyle(document.querySelector('#ctx-menu .mi.admin')).color, c]; }"
                )
                assert red[0] == red[1], red
                await close_menu(owner)
                assert not await owner.exists(".side .admin-btn, .side button", "Community Admin")
                items = await rail_menu(alice)
                for want in (
                    "All messages",
                    "Mentions only",
                    "Nothing",
                    "Mute community",
                    "Mark all as read",
                    "Copy community ID",
                    "Leave community",
                ):
                    assert want in items, (want, items)
                for admin_only in ("Community Admin", "Reports", "Approval queue", "Invite people"):
                    assert admin_only not in items, (admin_only, items)
                await close_menu(alice)
                items = await rail_menu(owner)
                for want in ("Community Admin", "Reports", "Approval queue", "Invite people", "Leave community"):
                    assert want in items, (want, items)
                await close_menu(owner)

                # New messages: badges, no pop-up. The owner is in another
                # channel; Alice writes in #general.
                await create_channel(owner, "random")
                await owner.wait_text(".main-head .title", "random")
                await owner.js(RECORD_TOASTS)
                await send_message(alice, "hello from alice")
                await alice.wait_text("#messages .mtext", "hello from alice")
                await owner.wait_js(
                    "document.querySelector('.chan[data-channel] .unread-dot')", "the unread dot on #general"
                )
                assert await owner.js("window.seenToasts") == [], "a pop-up for a new message"

                # Mark all as read from the icon's menu clears it.
                await rail_menu(owner)
                await owner.click("#ctx-menu .mi", "Mark all as read")
                await owner.wait_gone(".chan .unread-dot")

                # The notification level from the keyboard: Shift+F10 on the
                # focused icon, the arrows to "Mentions only", Enter.
                await owner.js(f"document.querySelector('{RAIL}').focus(); 0")
                await owner.key("F10", modifiers=8)
                await owner.wait_js("document.querySelector('#ctx-menu[role=menu]')", "the menu from the keyboard")
                assert await owner.js("document.activeElement.getAttribute('aria-checked')") == "true"
                assert await owner.js("document.activeElement.getAttribute('aria-label')") == "All messages"
                problems += await audit(owner, "community icon menu")
                await owner.key("ArrowDown")
                assert await owner.js("document.activeElement.getAttribute('aria-label')") == "Mentions only"
                await owner.key("Enter")
                await owner.wait_gone("#ctx-menu")
                await owner.wait_js("st.communities[0].notify_level === 'mentions'", "the level saved")
                assert await owner.js(f"document.activeElement === document.querySelector('{RAIL}')"), "focus not back"
                await rail_menu(owner)
                assert (
                    await owner.js(
                        "document.querySelector('#ctx-menu .mi[aria-label=\"Mentions only\"]')"
                        ".getAttribute('aria-checked')"
                    )
                    == "true"
                )
                await close_menu(owner)
                # Settings → Notifications shows it per community.
                await owner.click('.user-panel button[title="Settings"]')
                await owner.click(".settings nav .item", "Notifications")
                await owner.wait_js(
                    "document.querySelector('.settings select[data-community]')", "the per-community level"
                )
                assert await owner.js("document.querySelector('.settings select[data-community]').value") == "mentions"
                await owner.select(".settings select[data-community]", "Default (as above)")
                await owner.wait_js("st.communities[0].notify === ''", "back to the default")
                await close_settings(owner)

                # Copy community ID.
                await rail_menu(owner)
                await owner.click("#ctx-menu .mi", "Copy community ID")
                await owner.wait_text("#toasts .toast", "Community ID copied.")

                # Alice reports the owner's message: the badge trail.
                await open_channel(owner, "general")
                await send_message(owner, "a message to report")
                await alice.wait_text("#messages .mtext", "a message to report")
                await message_action_on(alice, "a message to report", "Report")
                await alice.fill(".modal textarea", "please check this")
                await alice.button("Report")
                await alice.wait_text(".toast", "Reported")
                # 1. the community's icon, 2. the community menu's header,
                # 3. the community menu's Community Admin entry (4).
                await owner.wait_js(badge(RAIL, 1), "the moderator badge on the icon")
                assert "1 waiting for review" in await owner.js(f"document.querySelector('{RAIL}').title")
                await owner.wait_js(badge(".side-head", 1), "the badge on the community menu's header")
                assert await alice.js("!document.querySelector('.mod-badge')"), "a member sees the moderator badge"
                # 4. the community menu's Community Admin and Reports entries.
                # A menu shows the counts as of when it opened, and under load
                # a sidebar redraw can swallow the click: open it until the
                # count is there (the header's badge above is already shown).
                await owner.wait_js(
                    "document.querySelector('#ctx-menu') || document.querySelector('.side-head').click(), "
                    + badge('#ctx-menu .mi[aria-label="Community Admin"]', 1),
                    "the badge on Community Admin",
                )
                assert (
                    await owner.js(
                        "document.getElementById(document.querySelector('#ctx-menu .mi.admin')"
                        ".getAttribute('aria-describedby')).textContent"
                    )
                    == "1 waiting for review"
                )
                assert await owner.js(badge('#ctx-menu .mi[aria-label="Reports"]', 1))
                assert not await owner.js(badge('#ctx-menu .mi[aria-label="Approval queue"]', 1))
                problems += await audit(owner, "community menu with the moderator badge")
                await owner.click("#ctx-menu .mi", "Community Admin")
                # 5. Community Admin opens where the report waits, with the
                # badge on Reports and on the Moderation group.
                await owner.wait_text(".settings section h2", "Reports")
                assert await owner.js(badge('.settings nav .item[data-id="reports"]', 1))
                assert await owner.js(
                    "!![...document.querySelectorAll('.settings nav .group')]"
                    ".find((g) => g.textContent.startsWith('Moderation'))"
                    ".querySelector('[data-count=\"1\"]')"
                )
                await owner.wait_text(".settings .report-row", "please check this")
                problems += await audit(owner, "Community Admin with the moderator badge")
                # Resolving clears every badge.
                await owner.click(".settings .report-row button", "Resolve")
                await owner.wait_text(".settings section p", "Nothing to review")
                await owner.wait_js("!document.querySelector('.settings nav .mod-badge')", "nav badges cleared")
                await close_settings(owner)
                await owner.wait_js("!document.querySelector('.mod-badge')", "every badge cleared")

                # Community Admin also lands on the queue: a join
                # request (a headless client with a "needs approval" invite).
                await community_section(owner, "Invites & join policy")
                await owner.button("Create invite…")
                await owner.fill('.modal input[placeholder="e.g. for the newsletter"]', "ask first")
                await owner.click('.modal input[aria-label="Needs approval"]')
                await owner.button("Create")
                await owner.wait_js("document.querySelector('.modal .invite-code .code-text')", "the new invite")
                asked = await owner.js("document.querySelector('.modal .invite-code .code-text').textContent")
                await owner.button("Close")
                await close_settings(owner)
                pia, _ = await new_core(tmp_path / "pia")
                cores.append(pia)
                assert (await pia.join({"invite": asked}, "new", "Pia"))["status"] == "pending"
                await owner.wait_js(badge(RAIL, 1), "the badge for the join request")
                await owner.click(".side-head")
                await owner.click("#ctx-menu .mi", "Community Admin")
                await owner.wait_text(".settings section h2", "Approval queue")
                assert await owner.js(badge('.settings nav .item[data-id="pending"]', 1))
                await owner.wait_text(".settings section .setting-row", "Pia")
                await owner.button("Approve")
                await owner.wait_text(".settings section p", "Nobody is waiting")
                await close_settings(owner)
                await owner.wait_js("!document.querySelector('.mod-badge')", "the join badge cleared")

                # Alice is given Manage Messages: Community Admin appears for
                # her, with Reports and without the owner's sections.
                await create_role(owner, "Mods", perms=("manage_messages",))
                await assign_role(owner, "Alice", "Mods")
                await close_settings(owner)
                await alice.wait_js(
                    "(document.querySelector('.side-head')?.click(), !!document.querySelector('#ctx-menu .mi.admin'))",
                    "Community Admin in Alice's community menu (opened until it appears)",
                    30,
                )
                await close_menu(alice)
                items = await rail_menu(alice)
                assert "Community Admin" in items and "Reports" in items and "Approval queue" not in items, items
                await alice.click("#ctx-menu .mi", "Community Admin")
                await alice.wait_text(".settings section h2", "Reports")
                assert await alice.texts(".settings nav .item") == ["Reports"]
                await close_settings(alice)

                # Leave from the icon's menu asks first; Cancel keeps her in.
                await rail_menu(alice)
                await alice.click("#ctx-menu .mi", "Leave community")
                await alice.wait_text(".modal h2", "Leave Badge Town?")
                await alice.button("Cancel")
                await alice.wait_gone(".modal")
                assert await alice.exists(RAIL)
                await rail_menu(alice)
                await alice.click("#ctx-menu .mi", "Leave community")
                await alice.wait_text(".modal h2", "Leave Badge Town?")
                await alice.click(".modal-foot button", "Leave")
                await alice.wait_gone(RAIL, None, 30)

                assert not problems, "\n".join(problems)
                owner.assert_no_errors()
                alice.assert_no_errors()
            finally:
                for core in cores:
                    await core.shutdown()
                await owner.stop()
                await alice.stop()

        run(main(), 400)
