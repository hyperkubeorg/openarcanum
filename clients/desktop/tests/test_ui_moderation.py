"""Moderation and custom emoji by clicks, in two real client windows (the
owner "Tester" and the member "Alice") with Bob as a headless client:
upload a custom emoji, add an AutoMod keyword rule and see Alice's message
blocked, report a message and resolve it from the queue, time Alice out and
see her composer locked, ban Bob with "delete the last 24 hours", and filter
the audit log."""

import pytest
from conftest import new_core, run, run_community, run_relay, wait_until
from test_e2e_community import general
from test_e2e_moderation import png
from uidriver import App
from uiflows import (
    claim,
    close_settings,
    community_section,
    create_profile,
    join,
    member_action,
    message_action_on,
    message_selector,
    send_message,
)

pytestmark = [
    pytest.mark.gui,
    # Two windows and a headless client through eight flows.
    pytest.mark.timeout(400),
]


def test_moderation_ui_flow(tmp_path):
    with run_relay() as relay, run_community(tmp_path, [relay]) as node:

        async def main():
            image = tmp_path / "parrot.png"
            image.write_bytes(png(64, 64))
            owner = await App(tmp_path / "owner", name="owner").start()
            alice = await App(tmp_path / "alice", name="alice").start()
            bob = None
            try:
                await create_profile(owner)
                invite = await claim(owner, node.claim_code, "Mod Town", "Tester")
                await create_profile(alice)
                await join(alice, invite, "Alice")
                bob, _ = await new_core(tmp_path / "bob")
                await bob.join({"invite": invite}, "new", "Bob")
                cid = next(iter(bob.conns))
                gen = await general(bob, cid)
                await bob.send(cid, gen, "bob says buy cheap stuff")
                await owner.wait_text("#messages .mtext", "bob says buy cheap stuff")

                # Upload a custom emoji (typed path) and see it in the list.
                await community_section(owner, "Emoji")
                await owner.fill('input[aria-label="Emoji name"]', "party_parrot")
                await owner.fill('input[aria-label="Image path"]', str(image))
                await owner.button("Upload")
                await owner.wait_js(
                    "(document.querySelector('.emoji-row[data-name=\"party_parrot\"] img') || {}).src"
                    "?.startsWith('data:image/png;base64,')",
                    "uploaded emoji in the list",
                )

                # An AutoMod keyword rule blocks Alice's message, with the reason.
                await community_section(owner, "AutoMod")
                await owner.button("New rule…")
                await owner.fill('.modal input[aria-label="Rule name"]', "No kumquats")
                await owner.fill('.modal textarea[aria-label="Keywords"]', "kumquat")
                await owner.button("Save rule")
                await owner.wait_text(".settings .rule-row", "No kumquats")
                await send_message(alice, "I sell KUMQUAT juice")
                await alice.wait_text(".toast.error", "AutoMod blocked your message")
                assert "I sell KUMQUAT juice" not in await alice.texts("#messages .mtext")

                # Alice reports Bob's message; the owner resolves it from the queue.
                await message_action_on(alice, "bob says buy cheap stuff", "Report")
                await alice.fill(".modal textarea", "this is spam")
                await alice.button("Report")
                await alice.wait_text(".toast", "Reported")
                await community_section(owner, "Reports")
                await owner.wait_text(".settings .report-row", "this is spam")
                await owner.wait_text(".settings .report-row", "Reported by Alice")
                await owner.click(".settings .report-row button", "Resolve")
                await owner.wait_text(".settings section p", "Nothing to review")
                await owner.click(".settings .close")
                await owner.wait_gone(".settings")

                # Time Alice out: her composer locks and says when it opens.
                await member_action(owner, "Alice", "Time out…")
                await owner.select('.modal select[aria-label="Duration"]', "1 hour")
                await owner.button("Time out")
                await owner.wait_text("#members .member", "timed out")
                await alice.wait_text(".composer-locked", "You're timed out. You can post again at", 30)
                assert not await alice.exists("#composer-input")

                # Ban Bob and delete his last 24 hours of messages.
                await member_action(owner, "Bob", "Ban…")
                # An ordinary relay can't block addresses: no such option
                # (test_ui_netblock.py has the dedicated relay's).
                await owner.wait_js("!!document.querySelector('.modal select')", "ban dialog")
                assert not await owner.exists(".modal .ban-block-network")
                await owner.select('.modal select[aria-label="Delete message history"]', "Last 24 hours")
                await owner.button("Ban")
                await owner.wait_text("#messages .msg.deleted .mtext", "Message deleted")
                assert "bob says buy cheap stuff" not in await owner.texts("#messages .mtext")
                await alice.wait_text("#messages .msg.deleted .mtext", "Message deleted")

                # Filter the audit log on the server: only the ban.
                await community_section(owner, "Audit log")
                await owner.wait_js("document.querySelectorAll('.audit-row').length >= 5", "audit rows")
                await owner.select('.settings select[aria-label="Action"]', "Ban")
                await owner.button("Apply filters")
                await owner.wait_js("document.querySelectorAll('.audit-row').length === 1", "one filtered row")
                assert (
                    "banned Bob and deleted 1 message from the last 24 hours"
                    in (await owner.texts(".audit-row td:nth-child(3)"))[0]
                )
                await owner.select('.settings select[aria-label="Action"]', "Timeout")
                await owner.select('.settings select[aria-label="Done to"]', "Alice")
                await owner.button("Apply filters")
                await owner.wait_js("document.querySelectorAll('.audit-row').length === 1", "timeout row")
                assert "timed out Alice for 1 hour" in (await owner.texts(".audit-row td:nth-child(3)"))[0]
                await owner.fill('.settings input[aria-label="From"]', "2001-01-01")
                await owner.fill('.settings input[aria-label="To"]', "2001-01-02")
                await owner.button("Apply filters")
                await owner.wait_text(".audit-empty", "Nothing matches")

                await wait_until(lambda: True)
                owner.assert_no_errors()
                alice.assert_no_errors()
                owner.assert_local_only()
            finally:
                await owner.stop()
                await alice.stop()
                if bob is not None:
                    await bob.shutdown()

        run(main(), 400)


async def side_menu_items(app: App) -> list[str]:
    await app.click(".side-head")
    await app.wait_js("document.querySelector('.menu')", "community menu")
    items = await app.texts(".menu .mi")
    await app.click(".main-head .title")
    await app.wait_gone(".menu")
    return items


def test_more_moderation_by_clicks(tmp_path):
    """Bulk delete (selection mode and "Delete recent messages…"), kick,
    another member's nickname, member screening, AutoMod alert and timeout
    rules made in the dialog, an invite that needs approval made with
    Create invite…, the approval queue (approve and deny) and raid revoke.
    Headless members join and post alongside the two windows."""
    with run_relay() as relay, run_community(tmp_path, [relay]) as node:

        async def main():
            owner = await App(tmp_path / "owner", name="owner").start()
            alice = await App(tmp_path / "alice", name="alice").start()
            cores = {}
            try:
                await create_profile(owner)
                invite = await claim(owner, node.claim_code, "Queue Town", "Tester")
                await create_profile(alice)
                await join(alice, invite, "Alice")

                async def headless(name: str, key: str):
                    core, ev = await new_core(tmp_path / name.lower())
                    cores[name] = core
                    r = await core.join({"invite": key}, "new", name)
                    return core, r, ev

                bob, _, _ = await headless("Bob", invite)
                cid = next(iter(bob.conns))
                gen = await general(bob, cid)

                # A member without Create Invite: no "Invite people", no settings.
                items = await side_menu_items(alice)
                assert "Invite people" not in items and "Community Admin" not in items, items

                # Bulk delete, selecting messages: two of Bob's four.
                for text in ("spam one", "spam two", "spam three", "keep me"):
                    await bob.send(cid, gen, text)
                await owner.wait_text("#messages .mtext", "keep me")
                one = await message_selector(owner, "spam one")
                two = await message_selector(owner, "spam two")
                await owner.message_action(one, "Select")
                await owner.wait_text(".select-bar", "1 selected")
                await owner.click(f"{two} .select-box")
                await owner.wait_text(".select-bar", "2 selected")
                await owner.button("Delete selected")
                await owner.wait_text(".modal h2", "Delete messages?")
                await owner.button("Delete")
                await owner.wait_text(".toast", "Deleted 2 message(s)")
                for sel in (one, two):
                    await alice.wait_text(f"{sel} .mtext", "Message deleted")
                assert "spam three" in await alice.texts("#messages .mtext")
                # "Delete recent messages…" from the channel's menu: the last two.
                await owner.right_click(".chan .cname", "general")
                await owner.click(".menu .mi", "Delete recent messages…")
                await owner.fill('.modal input[aria-label="How many"]', "2")
                await owner.button("Delete")
                await owner.wait_text(".toast", "Deleted 2 message(s)")
                for text in ("spam three", "keep me"):
                    await alice.wait_js(
                        f"![...document.querySelectorAll('#messages .mtext')].some((e) => e.textContent === {text!r})",
                        f"{text} deleted",
                    )
                await community_section(owner, "Audit log")
                await owner.select('.settings select[aria-label="Action"]', "Bulk delete")
                await owner.button("Apply filters")
                await owner.wait_js("document.querySelectorAll('.audit-row').length === 2", "two bulk deletes")
                await close_settings(owner)

                # Kick Bob from the member list.
                await member_action(owner, "Bob", "Kick…")
                await owner.wait_text(".modal h2", "Kick Bob")
                await owner.fill(".modal input", "testing kicks")
                await owner.button("Kick")
                await owner.wait_text(".toast", "Bob was kicked")
                await wait_until(lambda: bob.vault.data["communities"][cid]["status"] == "removed", 15)
                await owner.wait_js(
                    "![...document.querySelectorAll('#members .member')].some((e) => e.textContent.includes('Bob'))",
                    "Bob gone from the member list",
                )

                # Another member's nickname (Manage Nicknames).
                await member_action(owner, "Alice", "Change nickname…")
                await owner.fill(".modal input", "Ally")
                await owner.button("Save")
                await owner.wait_text(".toast", "Nickname saved")
                await alice.wait_js("st.states[st.cid].self.nickname === 'Ally'", "nickname on alice's side", 20)

                # Member screening: Alice joined minutes ago and has no role,
                # so her composer locks until she's been a member 10 minutes.
                await community_section(owner, "Member screening")
                await owner.select('.settings select[aria-label="Minimum membership age"]', "10 minutes")
                await owner.button("Save changes")
                await owner.wait_text(".toast", "Saved")
                await alice.wait_text(".composer-locked", "New members can post from", 30)
                assert not await alice.exists("#composer-input")
                await owner.select('.settings select[aria-label="Minimum membership age"]', "Off")
                await owner.button("Save changes")
                await alice.wait_js("document.querySelector('#composer-input')", "composer back", 30)

                # AutoMod alert: the message goes through, moderators get an alert.
                await community_section(owner, "AutoMod")
                await owner.button("New rule…")
                await owner.fill('.modal input[aria-label="Rule name"]', "Watch herring")
                await owner.fill('.modal textarea[aria-label="Keywords"]', "herring")
                await owner.click('.modal input[aria-label="Block the message"]')
                await owner.click('.modal input[aria-label="Alert moderators"]')
                await owner.button("Save rule")
                await owner.wait_text(".settings .rule-row", "Keywords → alert")
                # The live alert shows as the moderator badge, here on the
                # open window's Reports section and the community's icon.
                await send_message(alice, "a red herring")
                await alice.wait_text("#messages .mtext", "a red herring")
                await owner.wait_js(
                    'document.querySelector(\'.settings nav .item[data-id="reports"] .mod-badge[data-count="1"]\')',
                    "the alert's badge on Reports",
                )
                await owner.wait_js("document.querySelector('.rail-item[data-community] .mod-badge')", "the rail badge")
                await community_section(owner, "Reports")
                await owner.wait_text(".settings .report-row", "AutoMod rule “Watch herring” (alerted)")

                # AutoMod timeout: blocked, and Alice is timed out.
                await community_section(owner, "AutoMod")
                await owner.button("New rule…")
                await owner.fill('.modal input[aria-label="Rule name"]', "No sardines")
                await owner.fill('.modal textarea[aria-label="Keywords"]', "sardine")
                await owner.select('.modal select[aria-label="Time out the sender"]', "60 seconds")
                await owner.button("Save rule")
                await owner.wait_text(".settings .rule-row", "Keywords → block, time out")
                await send_message(alice, "sardine sale")
                await alice.wait_text(".toast.error", "AutoMod blocked your message")
                await alice.wait_text(".composer-locked", "You're timed out", 30)
                await close_settings(owner)
                # The alerted message went through (Community Admin was open
                # over the channel until now).
                await owner.wait_text("#messages .mtext", "a red herring")
                await member_action(owner, "Ally", "Remove timeout")
                await alice.wait_js("document.querySelector('#composer-input')", "composer back after timeout", 30)

                # Create invite… with "Needs approval"; requests land in the
                # approval queue: approve one, deny another.
                await community_section(owner, "Invites & join policy")
                await owner.button("Create invite…")
                await owner.fill('.modal input[placeholder="e.g. for the newsletter"]', "ask first")
                await owner.click('.modal input[aria-label="Needs approval"]')
                await owner.button("Create")
                await owner.wait_js(
                    "[...document.querySelectorAll('.modal .invite-code .code-text')]"
                    ".some((e) => e.textContent.startsWith('openarcanum-invite:'))",
                    "the new invite",
                )
                asked = await owner.js(
                    "[...document.querySelectorAll('.modal .invite-code .code-text')].map((e) => e.textContent)"
                    ".find((t) => t.startsWith('openarcanum-invite:'))"
                )
                await owner.button("Close")
                await owner.wait_text(".settings section .invites", "needs approval")
                pia, r, pev = await headless("Pia", asked)
                assert r["status"] == "pending"
                quinn, r, _ = await headless("Quinn", asked)
                assert r["status"] == "pending"
                await community_section(owner, "Approval queue")
                await owner.wait_text(".settings section .setting-row", "Quinn")
                await owner.click(".settings section .setting-row button", "Approve")  # the first: Pia
                await pev.wait(lambda n, p: n == "toast" and "admitted" in p["text"], 20)
                await general(pia, cid)
                await owner.wait_js(
                    "!document.querySelector('.settings section').textContent.includes('Pia')", "Pia left the queue"
                )
                await owner.button("Deny")
                await owner.wait_text(".settings section p", "Nobody is waiting")
                await wait_until(lambda: quinn.vault.data["communities"][cid]["status"] != "active", 20)

                # Raid revoke on that invite: Pia (who joined with it) is removed.
                await community_section(owner, "Invites & join policy")
                row = await owner.call(
                    "() => [...document.querySelectorAll('.invites tr[data-invite]')]"
                    ".find((r) => r.textContent.includes('ask first')).dataset.invite"
                )
                await owner.click(f'.invites tr[data-invite="{row}"] button', "Raid revoke…")
                await owner.wait_text(".modal h2", "Raid revoke")
                await owner.button("Revoke and remove")
                await owner.wait_text(".toast", "Revoked; removed 1 member(s).")
                await wait_until(lambda: pia.vault.data["communities"][cid]["status"] == "removed", 20)
                await owner.wait_text(f'.invites tr[data-invite="{row}"] .pill', "revoked")
                await close_settings(owner)
                for app in (owner, alice):
                    app.assert_no_errors()
            finally:
                for core in cores.values():
                    await core.shutdown()
                await owner.stop()
                await alice.stop()

        run(main(), 420)
