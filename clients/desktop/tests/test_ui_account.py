"""Account and data controls, clicked through: change passphrase, lock and
unlock, export a backup, delete a community's local data, and leave a
community (the other member sees it)."""

import asyncio
import hashlib

import pytest
from conftest import PASS, network, run
from uidriver import App
from uiflows import claim, close_settings, create_profile, join, open_members, open_settings, send_message

from openarcanum import keychain, keys
from openarcanum.paths import Paths

pytestmark = pytest.mark.gui

NEW_PASS = "another horse battery staple"
BACKUP_PASS = "backup pass phrase one"


async def security(app: App) -> None:
    await open_settings(app)
    await app.click(".settings nav .item", "Security")
    await app.wait_text(".settings section h2", "Security")


async def wait_for(pred, what: str, timeout: float = 30.0) -> None:
    for _ in range(int(timeout / 0.25)):
        if pred():
            return
        await asyncio.sleep(0.25)
    raise AssertionError(f"timed out waiting for {what}")


def test_passphrase_backup_local_data_and_leaving(tmp_path):
    with network(tmp_path) as (_meta, _relay, community):
        node = community()

        async def main():
            olive = await App(tmp_path / "olive", name="olive").start()
            alice = await App(tmp_path / "alice", name="alice").start()
            try:
                for app in (olive, alice):
                    await create_profile(app)
                invite = await claim(olive, node.claim_code, "Account Town", "Olive")
                await join(alice, invite, "Alice")
                await send_message(olive, "kept on the server")
                await olive.wait_text("#messages .mtext", "kept on the server")
                cid = await olive.js("st.cid")

                # Change passphrase: mismatched new passphrases are refused in
                # the dialog, a wrong current one by the vault.
                await security(olive)
                await olive.button("Change passphrase…")
                fields = ".modal input[type=password]"
                await olive.fill(fields, PASS, nth=0)
                await olive.fill(fields, NEW_PASS, nth=1)
                await olive.fill(fields, NEW_PASS + " typo", nth=2)
                await olive.button("Change")
                await olive.wait_text(".modal .error", "don't match")
                await olive.fill(fields, "not my passphrase at all", nth=0)
                await olive.fill(fields, NEW_PASS, nth=2)
                await olive.button("Change")
                await olive.wait_text(".modal .error", "current passphrase is wrong")
                await asyncio.sleep(1.2)  # the wrong-passphrase backoff
                await olive.fill(fields, PASS, nth=0)
                await olive.button("Change")
                await olive.wait_text("#toasts .toast", "Passphrase changed.")
                await olive.wait_gone(".modal:not(.wide)")

                # Lock now: the old passphrase no longer opens the profile, the new one does.
                await olive.button("Lock now")
                await olive.wait_js("!document.querySelector('#lock').classList.contains('hidden')", "lock screen")
                await olive.fill("#lock input[type=password]", PASS)
                await olive.button("Unlock")
                await olive.wait_text("#lock .error", "Wrong passphrase")
                await olive.fill("#lock input[type=password]", NEW_PASS)
                await olive.button("Unlock")
                await olive.wait_js("!document.querySelector('#app').classList.contains('hidden')", "unlocked", 40)

                # Export a backup to a typed path; mismatched passphrases are refused.
                await security(olive)
                assert "Out of date" in await olive.texts(".settings section .pill")
                await olive.button("Export backup…")
                await olive.fill(".modal input[type=password]", BACKUP_PASS, nth=0)
                await olive.fill(".modal input[type=password]", BACKUP_PASS + "x", nth=1)
                out = tmp_path / "olive.fbak"
                await olive.fill('.modal input[aria-label="Save to"]', str(out))
                await olive.button("Save backup")
                await olive.wait_text(".modal .error", "don't match")
                await olive.fill(".modal input[type=password]", BACKUP_PASS, nth=1)
                # The profile's passphrase confirms it, checked in Python.
                await olive.button("Save backup")
                await olive.wait_text(".modal .error", "Enter your passphrase to confirm.")
                await olive.fill(".modal input[type=password]", "not my passphrase at all", nth=2)
                await olive.button("Save backup")
                await olive.wait_text(".modal .error", "That passphrase is wrong.")
                assert not out.exists()
                await asyncio.sleep(1.2)  # the wrong-passphrase backoff
                await olive.fill(".modal input[type=password]", NEW_PASS, nth=2)
                await olive.button("Save backup")
                await olive.wait_text("#toasts .toast", "Backup saved.")
                data = keys.import_backup(out.read_bytes(), BACKUP_PASS)
                assert [c["id"] for c in data["communities"]] == [cid]
                await close_settings(olive)
                await security(olive)
                await olive.wait_text(".settings section .pill", "Up to date")

                # Delete local data: this device's copy of the community goes,
                # and the history downloads again from the server.
                cache = tmp_path / "olive" / "cache" / "communities" / hashlib.sha256(cid.encode()).hexdigest()[:32]
                await wait_for(lambda: cache.exists() and any(cache.iterdir()), "the community cache on disk")
                await olive.click(".settings nav .item", "Communities")
                await olive.click(".settings section button", "Delete this device's copy of messages and files")
                await olive.wait_text(".modal h2", "Delete local data?")
                await olive.button("Delete")
                await olive.wait_text("#toasts .toast", "Local data deleted.")
                assert not cache.exists() or not any(cache.iterdir())
                await close_settings(olive)
                await olive.click(".chan .cname", "general")
                await olive.wait_text("#messages .mtext", "kept on the server", 40)

                # Alice leaves the community; Olive's member list loses her.
                await open_members(olive)
                await olive.wait_text("#members .member", "Alice")
                await alice.click(".side-head")
                await alice.click(".menu .mi", "Leave community")
                await alice.wait_text(".modal h2", "Leave Account Town?")
                await alice.button("Leave")
                await alice.wait_gone(".rail-item[title^='Account Town']")
                assert await alice.js("st.communities.filter((c) => c.status === 'active').length") == 0
                for _ in range(80):
                    if not any("Alice" in t for t in await olive.texts("#members .member")):
                        break
                    await asyncio.sleep(0.25)
                assert not any("Alice" in t for t in await olive.texts("#members .member"))
                for app in (olive, alice):
                    app.assert_no_errors()
            finally:
                await olive.stop()
                await alice.stop()

        run(main(), 300)


def test_restore_from_backup_and_start_over(tmp_path):
    """A backup restores the profile on a new device (a wrong backup
    passphrase is refused), and "Forgot your passphrase?" sets the old
    profile aside and starts fresh."""
    with network(tmp_path) as (_meta, _relay, community):
        node = community()

        async def main():
            olive = await App(tmp_path / "olive", name="olive").start()
            restored = None
            try:
                await create_profile(olive)
                await claim(olive, node.claim_code, "Restore Town", "Olive")
                await send_message(olive, "from before the backup")
                await olive.wait_text("#messages .mtext", "from before the backup")
                out = tmp_path / "olive.fbak"
                await security(olive)
                await olive.button("Export backup…")
                await olive.fill(".modal input[type=password]", BACKUP_PASS, nth=0)
                await olive.fill(".modal input[type=password]", BACKUP_PASS, nth=1)
                await olive.fill(".modal input[type=password]", PASS, nth=2)
                await olive.fill('.modal input[aria-label="Save to"]', str(out))
                await olive.button("Save backup")
                await olive.wait_text("#toasts .toast", "Backup saved.")
                await close_settings(olive)

                # A new device restores it: wrong passphrase first, then the right one.
                restored = await App(tmp_path / "restored", name="restored").start()
                await restored.button("Restore from backup")
                await restored.wait_text("#lock h1", "Restore from backup")
                await restored.fill('#lock input[aria-label="Backup file"]', str(out))
                await restored.fill("#lock input[type=password]", "not the backup passphrase", nth=0)
                await restored.fill("#lock input[type=password]", NEW_PASS, nth=1)
                await restored.button("Restore")
                await restored.wait_text("#lock .error", "Wrong backup passphrase.")
                await restored.fill("#lock input[type=password]", BACKUP_PASS, nth=0)
                await restored.button("Restore")
                await restored.wait_js("!document.querySelector('#app').classList.contains('hidden')", "restored", 40)
                await restored.wait_js("document.querySelector('.rail-item[data-community]')", "the community", 40)
                await restored.click(".rail-item[data-community]")
                await restored.wait_text("#messages .mtext", "from before the backup", 40)
                # A restored profile stays signed in on its new device.
                assert keychain.key_file(Paths.under(tmp_path / "restored")).exists()
                restored.assert_no_errors()

                # Forgot the passphrase on the first device: start over. The
                # old profile is set aside, not deleted.
                await olive.click('.user-panel button[title="Lock now"]')
                await olive.wait_js("!document.querySelector('#lock').classList.contains('hidden')", "lock screen")
                await olive.button("Forgot your passphrase?")
                await olive.wait_text("#lock h1", "Forgot your passphrase?")
                await olive.button("Set this profile aside and start over…")
                await olive.wait_text(".modal h2", "Start over?")
                await olive.button("Start over")
                await olive.wait_text("#lock h1", "Welcome to", 20)
                data = tmp_path / "olive" / "data"
                assert not (data / "vault.bin").exists()
                assert [f.name for f in data.iterdir() if f.name.startswith("vault.bin.set-aside-")]
                assert not keychain.key_file(Paths.under(tmp_path / "olive")).exists()
                olive.assert_no_errors()
            finally:
                await olive.stop()
                if restored is not None:
                    await restored.stop()

        run(main(), 300)


def test_auto_lock_after_idle(tmp_path):
    """With "Lock after idle" set to 1 minute in Settings → Security, an
    untouched window locks itself."""

    async def main():
        app = await App(tmp_path / "olive", name="olive").start()
        try:
            await create_profile(app)
            await security(app)
            await app.select('.settings section select[aria-label="Lock after idle"]', "1 minute")
            await app.wait_text(".settings section .saved", "Saved")
            await close_settings(app)
            # Nothing touched for a minute (the check runs every 5 s).
            await app.wait_js("!document.querySelector('#lock').classList.contains('hidden')", "the idle lock", 90)
            await app.wait_text("#lock h1", "Unlock")
            assert await app.js("document.querySelector('#app').classList.contains('hidden')")
            # The key that kept it signed in went with the lock: a restart
            # asks for the passphrase too.
            assert not keychain.key_file(Paths.under(tmp_path / "olive")).exists()
            app.assert_no_errors()
        finally:
            await app.stop()

    run(main(), 180)
