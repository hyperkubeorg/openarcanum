"""Linking a second device by clicking, in both windows: the new device
shows a code, the existing one types it, approves with the history-copy
choice, the new device finishes with its own passphrase and gets the DM
history in the background. Then the device is renamed and
removed from Settings → Devices, and the removed device wipes itself."""

import asyncio

import pytest
from conftest import PASS, network, run
from test_ui_dm import open_dms, two_people
from uidriver import App
from uiflows import close_settings, open_settings, replace_maintainers

pytestmark = [pytest.mark.gui, pytest.mark.timeout(420)]


async def devices_section(app: App) -> None:
    if not await app.exists(".settings"):
        await open_settings(app)
    await app.click(".settings nav .item", "Devices")
    await app.wait_text(".settings section h2", "Devices")


async def dm_with_history(olive: App, alice: App) -> str:
    """Precondition (not what this test is about): a DM between Alice and
    Olive with one message, set up through the app's API."""
    gid = await alice.js(
        "(async () => { const cid = st.cid; const ms = await api('members', cid);"
        " const o = ms.find((m) => m.display_name === 'Olive');"
        " const r = await api('dm_open', cid, o.key);"
        " await api('dm_send', r.id, 'history before linking', ''); return r.id; })()"
    )
    for _ in range(80):
        if any(c["id"] == gid for c in await olive.js("api('dm_poll').then(() => api('dm_list'))")):
            break
        await asyncio.sleep(0.25)
    await olive.js(f"api('dm_accept', {gid!r})")
    for _ in range(80):
        hist = await olive.js(f"api('dm_history', {gid!r})")
        if any(m.get("text") == "history before linking" for m in hist):
            return gid
        await asyncio.sleep(0.25)
    raise AssertionError("the DM never reached Olive")


def test_link_rename_and_remove_a_device_by_clicking(tmp_path):
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            olive, alice = await two_people(tmp_path, meta, node)
            laptop = await App(tmp_path / "laptop", name="laptop").start()
            try:
                await dm_with_history(olive, alice)

                # The new device: Link to an existing device → Get a code.
                await laptop.button("Link to an existing device")
                await laptop.wait_text("#lock h1", "Link to an existing device")
                await laptop.fill("#lock input[type=text]", "laptop", nth=0)
                # This device's maintainer nodes, changed on the link screen
                # itself; the code goes through them (the built-in names
                # don't resolve).
                await laptop.wait_text("#lock .maintainer-source", "the defaults")
                await replace_maintainers(laptop, [meta])
                await laptop.wait_text("#lock .maintainer-summary", f"Maintainer nodes: {meta.split('//')[1]}")
                await laptop.button("Get a code")
                await laptop.wait_js("document.querySelector('#lock .code-display')", "the link code", 40)
                code = (await laptop.texts("#lock .code-display"))[0]
                assert len(code.split("-")) == 4, code

                # The existing device: Settings → Devices → Link a new device….
                await devices_section(olive)
                assert len(await olive.texts(".settings section table.list tr")) == 2  # header + this device
                await olive.button("Link a new device…")
                await olive.fill(".modal input[type=text]", code)
                # The passphrase again, checked in Python: a wrong one (or
                # none) starts nothing.
                await olive.button("Continue")
                await olive.wait_text(".modal .error", "Enter your passphrase to confirm.")
                await olive.fill(".modal input[type=password]", "not the passphrase")
                await olive.button("Continue")
                await olive.wait_text(".modal .error", "That passphrase is wrong.", 30)
                assert not await olive.exists(".modal h2", "Approve new device")
                await asyncio.sleep(1.2)  # the wrong-passphrase backoff
                await olive.fill(".modal input[type=password]", PASS)
                await olive.button("Continue")
                await olive.wait_text(".modal h2", "Approve new device", 40)
                await olive.wait_text(".modal .card b", "laptop")
                await olive.wait_text(".modal .notice", "Never enter a code someone sent you")
                await laptop.wait_text("#lock .muted", "Approve the link on the other device")
                # DM history is copied by default; community messages are optional.
                assert await olive.js(
                    "[...document.querySelectorAll('.modal label')]"
                    ".find((l) => l.textContent.includes('Copy direct message history')).querySelector('input').checked"
                )
                await olive.click(".modal input[type=checkbox]", "Copy downloaded community messages")
                await olive.button("Approve and send")

                # The new device finishes with its own passphrase.
                await laptop.wait_text("#lock h1", "Almost done", 40)
                await laptop.wait_text("#lock p", "1 communities, 1 conversations")
                await laptop.fill("#lock input[type=password]", PASS, nth=0)
                await laptop.fill("#lock input[type=password]", PASS, nth=1)
                await laptop.button("Finish")
                await laptop.wait_js("!document.querySelector('#app').classList.contains('hidden')", "laptop app", 40)
                # The transfer is complete once the new device has finished.
                await olive.wait_text("#toasts .toast", "New device linked: laptop", 40)
                await laptop.wait_js("document.querySelector('.rail-item[data-community]')", "the shared community", 40)
                # The DM history arrives in the background.
                await laptop.wait_text("#toasts .toast", "Message history copied from your other device.", 60)
                await open_dms(laptop)
                await laptop.wait_text("button.chan[data-dm] .cname", "Alice")
                await laptop.click("button.chan[data-dm] .cname", "Alice")
                await laptop.wait_text("#dm-messages .mtext", "history before linking")

                # Both devices list each other; Olive renames the laptop and
                # the laptop's own list shows the new name.
                await close_settings(olive)
                await devices_section(olive)
                await olive.wait_text(".settings section table.list b", "laptop")
                await olive.click(".settings section tr:nth-child(3) button", "Rename")
                if "this device" in (await olive.texts(".settings section tr:nth-child(3)"))[0]:
                    raise AssertionError("expected the laptop in the second row")
                await olive.fill(".modal input", "work laptop")
                await olive.button("Save")
                await olive.wait_text(".settings section table.list b", "work laptop")
                await devices_section(laptop)
                for _ in range(80):
                    await laptop.js("api('dm_poll')")
                    await devices_section(laptop)
                    if "work laptop" in await laptop.texts(".settings section table.list b"):
                        break
                    await asyncio.sleep(0.25)
                assert "work laptop" in await laptop.texts(".settings section table.list b")

                # Olive removes it: it's told, wipes its profile and is back at
                # the first-run screen.
                await olive.click(".settings section button", "Remove")
                await olive.wait_text(".modal h2", "Remove work laptop?")
                await olive.fill(".modal input[type=password]", PASS)
                await olive.button("Remove")
                await olive.wait_text("#toasts .toast", "work laptop removed.", 40)
                await olive.wait_gone(".settings section table.list b", "work laptop")
                for _ in range(120):
                    if await laptop.exists("#lock button", "Link to an existing device"):
                        break
                    await laptop.js("api('dm_poll').catch(() => null)")
                    await asyncio.sleep(0.25)
                await laptop.wait_text("#lock h1", "Welcome to", 10)
                assert not (tmp_path / "laptop" / "data" / "vault.bin").exists()
                for app in (olive, alice, laptop):
                    app.assert_no_errors()
                    app.assert_local_only()
            finally:
                for app in (olive, alice, laptop):
                    await app.stop()

        run(main(), 360)
