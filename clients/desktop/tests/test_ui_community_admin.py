"""Community administration by clicks: categories, channel topic and
slowmode, a colored hoisted role with a permission, a per-member override,
the member list grouped by hoisted role, revoking an invite, and a feature
toggle that makes its feature disappear. Bob is a headless client acting
alongside the window (and checks what the server enforces). A second test
covers ordering, category sync and the role hierarchy in two windows."""

import asyncio

import pytest
from conftest import new_core, run, run_community, run_relay, wait_until
from test_e2e_community import general
from uidriver import App
from uiflows import (
    assign_role,
    claim,
    close_settings,
    community_section,
    create_category,
    create_channel,
    create_profile,
    create_role,
    edit_channel,
    join,
    member_override,
    message_selector,
    open_channel,
    send_message,
    set_feature,
    toggle_role_perms,
)

from openarcanum.core import UserError

pytestmark = [pytest.mark.gui, pytest.mark.timeout(460)]


def chan(core, cid, name):
    return next(c for c in core.community_state(cid)["channels"] if c["name"] == name)


def test_community_admin_ui_flow(tmp_path):
    with run_relay() as relay, run_community(tmp_path, [relay]) as node:

        async def main():
            app = await App(tmp_path / "owner", name="owner").start()
            bob = None
            try:
                await create_profile(app)
                invite = await claim(app, node.claim_code, "Admin Town", "Olive")
                bob, _ = await new_core(tmp_path / "bob")
                await bob.join({"invite": invite}, "new", "Bob")
                cid = next(iter(bob.conns))
                await general(bob, cid)
                bob_key = bob.community_state(cid)["self"]["key"]

                # A category, and #general moved into it with a topic and slowmode.
                await create_category(app, "Lounge")
                await edit_channel(app, "general", category="Lounge", topic="Say hi here", slowmode="30 seconds")
                await app.wait_text(".settings section .channel-row .desc", "slowmode 30s")
                await wait_until(lambda: chan(bob, cid, "general")["slowmode"] == 30, 20)
                assert chan(bob, cid, "general")["topic"] == "Say hi here"
                assert chan(bob, cid, "general")["category_id"] != 0
                gen = chan(bob, cid, "general")["id"]
                await bob.send(cid, gen, "first")
                with pytest.raises(UserError, match="slowmode"):
                    await bob.send(cid, gen, "second")

                # A colored role shown separately, with Kick, given to Bob.
                await create_role(app, "Hosts", color="#22aa44", hoist=True, perms=("kick",))
                await app.wait_text(".settings section .role-row .pill", "shown separately")
                await assign_role(app, "Bob", "Hosts")
                await wait_until(lambda: bob.community_summary(cid)["can"]["kick"], 20)

                # A per-member override: Bob can't send in #general.
                await member_override(app, "general", "Bob", bob_key, "send_messages", "deny")
                await wait_until(lambda: not chan(bob, cid, "general")["can"]["send_messages"], 20)

                # Revoke the first invite.
                await community_section(app, "Invites & join policy")
                await app.click(".settings section .invites button", "Revoke First invite")
                await app.wait_text(".modal h2", "Revoke this invite?")  # it asks first
                await app.click(".modal-foot button", "Revoke")
                await app.wait_text(".settings section .invites .pill", "revoked")
                await close_settings(app)

                # The sidebar groups #general under the category; the header shows the topic.
                await app.wait_text(".side-cat", "Lounge")
                assert await app.call(
                    "(id) => !!(document.querySelector('.side-cat').compareDocumentPosition("
                    "document.querySelector(`.chan[data-channel='${id}']`)) & Node.DOCUMENT_POSITION_FOLLOWING)",
                    gen,
                )
                await app.click(".chan .cname", "general")
                await app.wait_text(".main-head .channel-topic", "Say hi here")
                # The owner is exempt from slowmode, so no slowmode note.
                assert not await app.exists(".composer .slowmode")
                # Collapsing the category flips its state.
                await app.click(".side-cat")
                await app.wait_js(
                    "document.querySelector('.side-cat').getAttribute('aria-expanded') === 'false'", "collapsed"
                )
                await app.click(".side-cat")

                # The member list groups Bob under the hoisted role.
                await app.click('button[title="Members"]')
                await app.wait_text("#members .member-group", "Hosts · 1")
                assert await app.js(
                    "(() => { const g = document.querySelector('#members .member-group[data-role]');"
                    " const b = [...document.querySelectorAll('#members .member')]"
                    ".find((e) => e.textContent.includes('Bob'));"
                    " return !!(g.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING); })()"
                )

                # Turning search off makes it disappear (and the server refuses it).
                assert await app.exists('.main-head button[title="Search"]')
                await set_feature(app, "search", False)
                await close_settings(app)
                await app.wait_gone('.main-head button[title="Search"]')
                await wait_until(lambda: bob.community_summary(cid)["features"]["search"] is False, 20)
                with pytest.raises(UserError, match="turned off search"):
                    await bob.search_messages(cid, "first")
                # Voice off: the voice-channel buttons go away too.
                assert await app.exists('.side-group button[title="Create voice channel"]')
                await set_feature(app, "voice", False)
                await close_settings(app)
                await app.wait_gone('.side-group button[title="Create voice channel"]')
                app.assert_no_errors()
            finally:
                if bob is not None:
                    await bob.shutdown()
                await app.stop()

        run(main(), 240)


async def sidebar_order(app: App, sel: str) -> list[str]:
    return [t.strip() for t in await app.texts(sel)]


async def can_send_in(app: App, channel: str) -> bool:
    await open_channel(app, channel)
    await app.wait_js("document.querySelector('#composer-input, .composer-box .muted')", "composer")
    return await app.exists("#composer-input")


def test_ordering_sync_and_role_hierarchy(tmp_path):
    """By clicks, seen in a member's window: channels and categories moved
    with ▲/▼; a category override that its synced channels follow, a
    channel given its own permissions and synced back with "Sync
    permissions with the category"; roles moved with ▲/▼, and a member with
    Manage Roles can't edit roles at or above their own; the role
    "Allow anyone to @mention this role" setting."""
    with run_relay() as relay, run_community(tmp_path, [relay]) as node:

        async def main():
            olive = await App(tmp_path / "olive", name="olive").start()
            alice = await App(tmp_path / "alice", name="alice").start()
            try:
                for app in (olive, alice):
                    await create_profile(app)
                invite = await claim(olive, node.claim_code, "Order Town", "Olive")
                await join(alice, invite, "Alice")
                for name in ("alpha", "beta", "gamma"):
                    await create_channel(olive, name)
                await alice.wait_text(".chan .cname", "gamma")
                assert await sidebar_order(alice, ".chan .cname") == ["general", "alpha", "beta", "gamma"]

                # Channels: gamma up one.
                await community_section(olive, "Channels")
                await olive.click('.settings section button[aria-label="Move gamma up"]')
                await alice.wait_js(
                    "[...document.querySelectorAll('.chan .cname')].map((e) => e.textContent).join() "
                    "=== 'general,alpha,gamma,beta'",
                    "gamma above beta",
                )

                # Categories: Upper and Lower, then Lower moved above Upper.
                await create_category(olive, "Upper")
                await create_category(olive, "Lower")
                await edit_channel(olive, "alpha", category="Upper")
                await edit_channel(olive, "beta", category="Lower")
                await alice.wait_js("document.querySelectorAll('.side-cat').length === 2", "two categories")
                assert await sidebar_order(alice, ".side-cat .spacer") == ["Upper", "Lower"]
                lower = await olive.call(
                    "() => [...document.querySelectorAll('.category-row')]"
                    ".find((e) => e.textContent.includes('LOWER')).dataset.category"
                )
                await olive.click(f'.category-row[data-category="{lower}"] button[title="Move category up"]')
                await alice.wait_js(
                    "[...document.querySelectorAll('.side-cat .spacer')].map((e) => e.textContent).join()"
                    " === 'Lower,Upper'",
                    "Lower above Upper",
                )

                # A category override: @everyone can't send in Upper. A channel
                # made in Upper starts synced and follows it; alpha, moved in
                # earlier, keeps its own permissions until it's synced.
                everyone = await olive.js("st.states[st.cid].roles.find((r) => r.builtin && r.position === 0).id")
                await community_section(olive, "Channels")
                await olive.click('.settings section button[aria-label="Permissions of Upper"]')
                await olive.wait_js("document.querySelector('.overrides')", "overrides dialog")
                deny = f'.override[data-target="r:{everyone}"] .tri[data-perm="send_messages"] button.deny'
                await olive.click(deny)
                await olive.button("Save")
                await olive.wait_gone(".overrides")
                upper = await olive.call(
                    "() => [...document.querySelectorAll('.category-row')]"
                    ".find((e) => e.textContent.includes('UPPER')).dataset.category"
                )
                await olive.click(f'.category-row[data-category="{upper}"] button', "New channel")
                await olive.fill(".modal input", "delta")
                await olive.button("Create")
                await olive.wait_text(".settings section .channel-row b", "delta")
                await alice.wait_text(".chan .cname", "delta")
                assert not await can_send_in(alice, "delta")
                assert await can_send_in(alice, "alpha")
                await olive.click('.settings section .channel-row button[aria-label="Edit alpha"]')
                await olive.wait_js("document.querySelector('.channel-dialog')", "channel dialog")
                await olive.button("Sync permissions with the category")
                await olive.wait_text(".toast", "Permissions synced.")
                await olive.button("Cancel")
                await olive.wait_gone(".channel-dialog")
                await alice.wait_js(
                    "!st.states[st.cid].channels.find((c) => c.name === 'alpha').can.send_messages", "alpha synced", 30
                )
                assert not await can_send_in(alice, "alpha")
                await community_section(olive, "Audit log")
                await olive.wait_text(".audit-row td", "reordered categories")
                await close_settings(olive)

                # Roles: three, Alice gets Mods (Manage Roles); Seniors moved up.
                await create_role(olive, "Helpers")
                await create_role(olive, "Mods", perms=("manage_roles",))
                await create_role(olive, "Seniors")
                order = await sidebar_order(olive, ".settings section .role-row b")
                await assign_role(olive, "Alice", "Mods")
                await community_section(olive, "Roles")
                seniors = order.index("Seniors")
                mods = order.index("Mods")
                # Move whichever of the two is lower above the other.
                low = "Seniors" if seniors > mods else "Mods"
                row = await olive.call(
                    "(n) => [...document.querySelectorAll('.role-row')]"
                    ".find((r) => r.querySelector('b').textContent === n).dataset.role",
                    low,
                )
                await olive.click(f'.role-row[data-role="{row}"] button[title="Move up"]')
                await olive.wait_js(
                    "(() => { const n = [...document.querySelectorAll('.settings section .role-row b')]"
                    ".map((e) => e.textContent); return n.indexOf('Seniors') < n.indexOf('Mods') === "
                    f"{'true' if low == 'Seniors' else 'false'}; }})()",
                    "role moved",
                )
                await close_settings(olive)
                # Alice (Mods) sees Roles; what's at or above Mods is disabled.
                await alice.wait_js("st.communities.find((c) => c.id === st.cid).can.manage_roles", "manage roles", 30)
                await community_section(alice, "Roles")
                names = await sidebar_order(alice, ".settings section .role-row b")
                for name in names:
                    disabled = await alice.js(
                        f"document.querySelector('.settings section button[aria-label=\"Edit {name}\"]').disabled"
                    )
                    at_or_above = names.index(name) <= names.index("Mods") and name != "@everyone"
                    assert disabled == at_or_above, (name, names)
                await close_settings(alice)

                # Mentionable: Olive holds Helpers. Alice (no Mention
                # @everyone) mentions it: no ping until the role allows it.
                await assign_role(olive, "Olive", "Helpers")
                await close_settings(olive)
                await open_channel(olive, "general")
                await open_channel(alice, "general")
                await alice.wait_js(
                    "st.states[st.cid].roles.some((r) => r.name === 'Helpers')", "helpers role on alice's side"
                )
                await send_message(alice, "@Helpers not yet")
                sel = await message_selector(olive, "not yet")
                await asyncio.sleep(1)
                assert not await olive.js(f"document.querySelector('{sel}').classList.contains('mentioned')")
                await toggle_role_perms(olive, "Helpers", (), mentionable=True)
                await olive.wait_text(".settings section .role-row .pill", "mentionable")
                await close_settings(olive)
                await send_message(alice, "@Helpers please look")
                sel = await message_selector(olive, "please look")
                await olive.wait_js(f"document.querySelector('{sel}').classList.contains('mentioned')", "role mention")
                for app in (olive, alice):
                    app.assert_no_errors()
            finally:
                await olive.stop()
                await alice.stop()

        run(main(), 400)
