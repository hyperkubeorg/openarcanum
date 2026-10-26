"""Staying signed in (keychain.py): after
creating, restoring or linking a profile, the device keeps the vault's key
(never the passphrase) so the next start opens without the passphrase. The
OS keychain when there is one, else a key file (0600) in the settings folder
(0700). "Ask for my passphrase when OpenArcanum starts" deletes it and re-keys
the vault; Lock now and an idle lock delete it until the passphrase is
entered again. Older profiles keep asking until a one-time offer is
accepted."""

import asyncio
import base64
import json
import os
import stat
import time
from pathlib import Path

import keyring
import pytest
from conftest import PASS, Events, missing_harness, run, wait_until
from keyring.backends import fail
from keyring_backend import FileKeyring

from openarcanum import keychain
from openarcanum.core import Core, UserError
from openarcanum.paths import POSIX, Paths
from openarcanum.settings import Preferences
from openarcanum.vault import Vault, WrongPassphrase


@pytest.fixture
def file_keyring(tmp_path, monkeypatch):
    """A stand-in keychain (conftest gives every other test none)."""
    monkeypatch.setenv("OPENARCANUM_TEST_KEYRING_FILE", str(tmp_path / "keyring.json"))
    old = keyring.get_keyring()
    keyring.set_keyring(FileKeyring())
    yield tmp_path / "keyring.json"
    keyring.set_keyring(old)


def stored(path) -> str:
    return path.read_text() if path.exists() else ""


def entry(paths: Paths) -> str | None:
    return keyring.get_password(keychain.SERVICE, keychain.account_name(paths))


def secrets_of(passphrase: str) -> tuple[str, ...]:
    return (passphrase, base64.b64encode(passphrase.encode()).decode())


def old_profile(home: Path, remember: bool = False) -> Paths:
    """A profile as a build from before staying signed in was the default
    left it: the vault, and preferences.toml with `remember_device` but
    neither the record of where a key is kept nor the one-time offer."""
    paths = Paths.under(home)
    paths.ensure()
    Vault.create(paths, PASS, "old desktop")
    paths.preferences.write_text(
        "# OpenArcanum preferences. Nothing here identifies you.\nformat = 1\n"
        f'theme = "dark"\nremember_device = {"true" if remember else "false"}\n'
    )
    return paths


# ---------------------------------------------------------------- new profiles


def test_a_new_profile_stays_signed_in_with_the_key_file(tmp_path):
    """No keychain (as in the dev containers): the key goes to the key file,
    0600 in the settings folder (0700), never in the data folder; the next
    start opens with it."""

    async def main():
        paths = Paths.under(tmp_path / "home")
        core = Core(paths, emit=Events())
        await core.create_profile(PASS)
        st = core.profile_status()["remember"]
        assert st == {"enabled": True, "stored": "file", "offer": ""}
        f = keychain.key_file(paths)
        assert f.parent == paths.config and f.exists()
        assert not any(p.name == keychain.KEY_FILE for p in paths.data.rglob("*"))
        if POSIX:
            assert stat.S_IMODE(f.stat().st_mode) == 0o600
            assert stat.S_IMODE(f.parent.stat().st_mode) == 0o700
        body = f.read_text()
        assert json.loads(body)["v"] == 1
        for s in secrets_of(PASS):
            assert s not in body  # the key, never the passphrase
        assert keychain.load(paths, "file") == core.vault.file_key()
        r = await core.remember_status()
        assert r["store"] == "file" and not r["available"] and "no keychain" in r["reason"]
        assert r["file"] == str(f)
        # Settings → Data & storage lists it.
        locs = {loc["id"]: loc for loc in core.storage_locations()}
        assert locs["unlock_key"]["path"] == str(f) and locs["unlock_key"]["exists"]
        await core.shutdown()

        again = Core(paths, emit=Events())
        assert again.profile_status()["state"] == "locked"
        assert again.profile_status()["remember"]["stored"] == "file"
        assert (await again.unlock_remembered())["state"] == "unlocked"
        await again.shutdown()

    run(main(), 60)


def test_a_new_profile_prefers_the_keychain(tmp_path, file_keyring):
    async def main():
        paths = Paths.under(tmp_path / "home")
        core = Core(paths, emit=Events())
        await core.create_profile(PASS)
        assert core.profile_status()["remember"]["stored"] == "keychain"
        assert entry(paths) and not keychain.key_file(paths).exists()
        for s in secrets_of(PASS):
            assert s not in stored(file_keyring)
        r = await core.remember_status()
        assert r == {
            "enabled": True,
            "stored": "keychain",
            "store": "keychain",
            "available": True,
            "reason": "",
            "file": str(keychain.key_file(paths)),
        }
        await core.shutdown()
        again = Core(paths, emit=Events())
        assert (await again.unlock_remembered())["state"] == "unlocked"
        await again.shutdown()

    run(main(), 60)


def test_a_restored_profile_stays_signed_in_and_backups_are_unchanged(tmp_path):
    async def main():
        core = Core(Paths.under(tmp_path / "a"), emit=Events())
        await core.create_profile(PASS)
        backup = tmp_path / "backup.fbak"
        await core.export_backup(str(backup), "backup passphrase here")
        key = core.vault.file_key()
        assert base64.b64encode(key) not in backup.read_bytes() and key not in backup.read_bytes()
        await core.shutdown()

        paths = Paths.under(tmp_path / "b")
        fresh = Core(paths, emit=Events())
        await fresh.restore_backup(str(backup), "backup passphrase here", PASS, "restored")
        assert fresh.profile_status()["remember"]["stored"] == "file"
        await fresh.shutdown()
        again = Core(paths, emit=Events())
        assert (await again.unlock_remembered())["state"] == "unlocked"
        await again.shutdown()

    run(main(), 60)


# ---------------------------------------------------------------- the option


def test_ask_for_my_passphrase_removes_the_key(tmp_path):
    """Turning the option on needs the passphrase, deletes the key and
    re-keys the profile file (a copy of the key stops working); the next
    start asks. Turning it off again keeps a key again."""

    async def main():
        paths = Paths.under(tmp_path / "home")
        core = Core(paths, emit=Events())
        await core.create_profile(PASS)
        copied = keychain.load(paths, "file")
        with pytest.raises(UserError, match="wrong"):
            await core.set_remember(False, "not the passphrase")
        assert keychain.key_file(paths).exists()  # a refused change keeps it
        # A wrong passphrase starts the unlock wait (as on the lock screen).
        core.guard.now = lambda: time.time() + 100
        r = await core.set_remember(False, PASS)
        assert not r["enabled"] and r["stored"] == ""
        assert not keychain.key_file(paths).exists()
        with pytest.raises(WrongPassphrase):
            Vault.unlock_with_key(paths, copied)
        Vault.unlock(paths, PASS, None)
        await core.shutdown()

        again = Core(paths, emit=Events())
        assert again.profile_status()["remember"] == {"enabled": False, "stored": "", "offer": ""}
        with pytest.raises(UserError, match="Enter your passphrase"):
            await again.unlock_remembered()
        await again.unlock(PASS)
        # Asked for, so no offer and nothing kept by the unlock.
        assert again.profile_status()["remember"] == {"enabled": False, "stored": "", "offer": ""}
        assert not keychain.key_file(paths).exists()
        with pytest.raises(UserError, match="wrong"):
            await again.set_remember(True, "not the passphrase")
        again.guard.now = lambda: time.time() + 100
        r = await again.set_remember(True, PASS)
        assert r["enabled"] and r["stored"] == "file" and keychain.key_file(paths).exists()
        await again.shutdown()
        third = Core(paths, emit=Events())
        assert (await third.unlock_remembered())["state"] == "unlocked"
        await third.shutdown()

    run(main(), 60)


def test_ask_for_my_passphrase_with_the_keychain(tmp_path, file_keyring):
    async def main():
        paths = Paths.under(tmp_path / "home")
        core = Core(paths, emit=Events())
        await core.create_profile(PASS)
        copied = keychain.load(paths, "keychain")
        await core.set_remember(False, PASS)
        assert entry(paths) is None
        with pytest.raises(WrongPassphrase):
            Vault.unlock_with_key(paths, copied)
        await core.set_remember(True, PASS)
        assert entry(paths)
        await core.shutdown()

    run(main(), 60)


# ---------------------------------------------------------------- locking


def test_lock_now_and_idle_lock_remove_the_key(tmp_path, file_keyring):
    """Locked means the passphrase is needed, also after a restart: the key
    goes with the lock, and the passphrase keeps it again."""

    async def main():
        paths = Paths.under(tmp_path / "home")
        core = Core(paths, emit=Events())
        await core.create_profile(PASS)
        assert entry(paths)
        await core.lock()
        assert entry(paths) is None
        assert core.profile_status()["remember"] == {"enabled": True, "stored": "", "offer": ""}
        with pytest.raises(UserError, match="Enter your passphrase"):
            await core.unlock_remembered()
        await core.shutdown()

        again = Core(paths, emit=Events())
        assert again.profile_status()["remember"]["stored"] == ""
        await again.unlock(PASS)
        assert again.profile_status()["remember"]["stored"] == "keychain" and entry(paths)
        # The idle lock does the same.
        await again.set_setting("auto_lock_minutes", 1)
        again.last_activity = time.monotonic() - 61
        await wait_until(lambda: not again.unlocked, 10)
        await wait_until(lambda: entry(paths) is None, 5)
        await again.unlock(PASS)
        assert entry(paths)
        await again.shutdown()

    run(main(), 60)


def test_lock_removes_the_key_file(tmp_path):
    async def main():
        paths = Paths.under(tmp_path / "home")
        core = Core(paths, emit=Events())
        await core.create_profile(PASS)
        await core.lock()
        assert not keychain.key_file(paths).exists()
        await core.unlock(PASS)
        assert keychain.key_file(paths).exists()
        await core.shutdown()

    run(main(), 60)


# ---------------------------------------------------------------- existing profiles


def test_an_existing_profile_keeps_asking_until_it_opts_in(tmp_path):
    """A profile from before: nothing is stored by opening it; the offer is
    made once, after the passphrase; No is final; Yes keeps the key."""

    async def main():
        paths = old_profile(tmp_path / "home")
        core = Core(paths, emit=Events())
        assert core.profile_status()["remember"] == {"enabled": False, "stored": "", "offer": ""}
        await core.unlock(PASS)
        assert core.profile_status()["remember"] == {"enabled": False, "stored": "", "offer": "file"}
        assert not keychain.key_file(paths).exists()  # unchanged until it opts in
        await core.lock()  # a lock withdraws the offer
        assert core.profile_status()["remember"]["offer"] == ""
        await core.unlock(PASS)
        st = await core.answer_stay_signed_in(False)
        assert st["remember"] == {"enabled": False, "stored": "", "offer": ""}
        assert not keychain.key_file(paths).exists()
        with pytest.raises(UserError):
            await core.answer_stay_signed_in(True)  # answered once
        await core.shutdown()
        again = Core(paths, emit=Events())
        await again.unlock(PASS)
        assert again.profile_status()["remember"] == {"enabled": False, "stored": "", "offer": ""}
        await again.shutdown()

        # Another old profile says yes.
        paths = old_profile(tmp_path / "other")
        core = Core(paths, emit=Events())
        await core.unlock(PASS)
        st = await core.answer_stay_signed_in(True)
        assert st["remember"] == {"enabled": True, "stored": "file", "offer": ""}
        await core.shutdown()
        again = Core(paths, emit=Events())
        assert (await again.unlock_remembered())["state"] == "unlocked"
        assert again.profile_status()["remember"]["offer"] == ""
        await again.shutdown()

    run(main(), 60)


def test_an_existing_remember_on_this_device_keeps_working(tmp_path, file_keyring):
    """ "Remember on this device" turned on in a build from before: the key is
    in the keychain and preferences don't say where; it still opens."""

    async def main():
        paths = old_profile(tmp_path / "home", remember=True)
        v = Vault.unlock(paths, PASS, None)
        keychain.store_keychain(paths, v.file_key())
        core = Core(paths, emit=Events())
        assert core.profile_status()["remember"] == {"enabled": True, "stored": "keychain", "offer": ""}
        assert (await core.unlock_remembered())["state"] == "unlocked"
        await core.shutdown()

    run(main(), 60)


# ---------------------------------------------------------------- keeping it right


def test_a_key_file_others_can_read_is_refused_and_deleted(tmp_path):
    if not POSIX:
        pytest.skip("file modes are POSIX")

    async def main():
        paths = Paths.under(tmp_path / "home")
        core = Core(paths, emit=Events())
        await core.create_profile(PASS)
        await core.shutdown()
        os.chmod(keychain.key_file(paths), 0o644)
        again = Core(paths, emit=Events())
        with pytest.raises(UserError, match="other accounts"):
            await again.unlock_remembered()
        assert not keychain.key_file(paths).exists()
        assert again.profile_status()["remember"]["stored"] == ""
        await again.unlock(PASS)  # and the passphrase keeps a new one
        assert stat.S_IMODE(keychain.key_file(paths).stat().st_mode) == 0o600
        await again.shutdown()

    run(main(), 60)


def test_passphrase_change_and_a_stale_key(tmp_path, file_keyring):
    async def main():
        paths = Paths.under(tmp_path / "home")
        core = Core(paths, emit=Events())
        await core.create_profile(PASS)
        # A passphrase change keeps it working.
        await core.change_passphrase(PASS, PASS + " new")
        await core.shutdown()
        again = Core(paths, emit=Events())
        assert (await again.unlock_remembered())["state"] == "unlocked"
        await again.shutdown()

        # A key that no longer fits: refused and forgotten; the passphrase
        # opens the profile and keeps the right key again.
        keyring.set_password(keychain.SERVICE, keychain.account_name(paths), base64.b64encode(os.urandom(32)).decode())
        third = Core(paths, emit=Events())
        with pytest.raises(UserError, match="no longer opens"):
            await third.unlock_remembered()
        assert entry(paths) is None
        assert third.profile_status()["remember"] == {"enabled": True, "stored": "", "offer": ""}
        await third.unlock(PASS + " new")
        assert Vault.unlock_with_key(paths, keychain.load(paths, "keychain"))
        await third.shutdown()

    run(main(), 60)


def test_the_key_follows_a_keychain_appearing(tmp_path, monkeypatch):
    """Kept in the file while there was no keychain; once there is one, the
    next keep moves it there and the file goes."""

    async def main():
        paths = Paths.under(tmp_path / "home")
        core = Core(paths, emit=Events())
        await core.create_profile(PASS)
        assert keychain.key_file(paths).exists()
        monkeypatch.setenv("OPENARCANUM_TEST_KEYRING_FILE", str(tmp_path / "keyring.json"))
        keyring.set_keyring(FileKeyring())
        await core.change_passphrase(PASS, PASS + " new")
        assert core.profile_status()["remember"]["stored"] == "keychain"
        assert entry(paths) and not keychain.key_file(paths).exists()
        await core.shutdown()

    run(main(), 60)


def test_start_over_and_a_moved_data_folder(tmp_path, file_keyring):
    async def main():
        paths = Paths.under(tmp_path / "home")
        core = Core(paths, emit=Events())
        await core.create_profile(PASS)
        # The keychain entry is named after the profile file's path: a moved
        # data folder keeps the key under the new name.
        await core.set_setting("data_dir", str(tmp_path / "elsewhere"))
        assert entry(core.paths) and entry(paths) is None
        await core.shutdown()
        again = Core(paths, emit=Events())
        assert (await again.unlock_remembered())["state"] == "unlocked"
        # A refused start-over changes nothing.
        with pytest.raises(UserError, match="Lock the profile first"):
            await again.reset_profile()
        assert again.profile_status()["remember"]["stored"] == "keychain" and entry(again.paths)
        await again.lock()
        await again.unlock(PASS)
        assert entry(again.paths)
        # Start over (locked): the key goes too.
        again.vault.lock()
        await again.reset_profile()
        assert entry(again.paths) is None
        assert again.profile_status()["remember"] == {"enabled": False, "stored": "", "offer": ""}
        await again.shutdown()

    run(main(), 60)


def test_no_keychain_status(tmp_path):
    """No keychain is no longer a reason to ask: the key file is used, and
    the status says so (Settings → Security shows the warning)."""
    old = keyring.get_keyring()
    keyring.set_keyring(fail.Keyring())
    try:

        async def main():
            core = Core(Paths.under(tmp_path / "home"), emit=Events())
            await core.create_profile(PASS)
            r = await core.remember_status()
            assert not r["available"] and "no keychain" in r["reason"]
            assert r["enabled"] and r["store"] == "file" and r["stored"] == "file"
            await core.shutdown()

        run(main(), 30)
    finally:
        keyring.set_keyring(old)


def test_preferences_record_where_the_key_is(tmp_path):
    p = Preferences(tmp_path / "preferences.toml")
    assert p.unlock_key_in == ""
    p.set_unlock_key_in("file")
    assert Preferences(tmp_path / "preferences.toml").unlock_key_in == "file"
    p.set_unlock_key_in("")
    assert Preferences(tmp_path / "preferences.toml").unlock_key_in == ""
    with pytest.raises(ValueError):
        p.set_unlock_key_in("somewhere")


NO_OS_KEYCHAIN = isinstance(keyring.get_keyring(), fail.Keyring)


@pytest.mark.os_keychain
def test_with_the_real_os_keychain(tmp_path):
    """Against the system's own keychain (Credential Manager, Keychain,
    Secret Service), where there is one (CI provides one on each OS)."""
    if NO_OS_KEYCHAIN:
        missing_harness("no OS keychain here")

    async def main():
        paths = Paths.under(tmp_path / "home")
        core = Core(paths, emit=Events())
        await core.create_profile(PASS)
        try:
            assert core.profile_status()["remember"]["stored"] == "keychain"
        finally:
            await core.shutdown()
        again = Core(paths, emit=Events())
        try:
            assert (await again.unlock_remembered())["state"] == "unlocked"
        finally:
            await again.set_remember(False, PASS)
            await again.shutdown()
        assert entry(paths) is None

    run(main(), 60)


@pytest.mark.gui
def test_stay_signed_in_in_the_ui(tmp_path):
    """No keychain (the dev containers, a minimal desktop): create a profile,
    restart, and it opens without the passphrase; Settings → Security says
    plainly who could open it. Lock now brings the passphrase back, also
    after a restart. "Ask for my passphrase" makes every start ask."""
    from uidriver import App
    from uiflows import create_profile, open_settings, unlock

    opened = "!document.querySelector('#app').classList.contains('hidden')"
    asks = "document.querySelector('#lock input[type=password]')"

    async def main():
        home = tmp_path / "home"
        key_file = keychain.key_file(Paths.under(home))
        app = await App(home, name="a").start()
        try:
            await create_profile(app)
            assert key_file.exists()
        finally:
            await app.stop()
        app = await App(home, name="b").start()
        try:
            await app.wait_js(opened, "opened without the passphrase", 40)
            await open_settings(app)
            await app.click(".settings nav .item", "Security")
            await app.wait_text(
                ".settings .key-file-notice", "without a passphrase for anyone who can read your user's files"
            )
            assert not await app.js(
                "document.querySelector('input[aria-label=\"Ask for my passphrase when OpenArcanum starts\"]').checked"
            )
            await app.click(".settings .close")
            # Locked by hand: the passphrase, and no other way in.
            await app.js("lockNow(); 0")
            await app.wait_js(asks, "lock screen")
            assert not await app.exists("#lock button", "Unlock with this device's keychain")
            assert not key_file.exists()
        finally:
            await app.stop()
        app = await App(home, name="c").start()
        try:
            await app.wait_js(asks, "asks after a lock and a restart", 40)
            await unlock(app)
            assert key_file.exists()
            # Ask for my passphrase when OpenArcanum starts.
            await open_settings(app)
            await app.click(".settings nav .item", "Security")
            await app.click('input[aria-label="Ask for my passphrase when OpenArcanum starts"]')
            await app.fill('.modal input[aria-label="Passphrase"]', PASS)
            await app.button("Ask for it")
            await app.wait_gone(".modal h2", "Ask for your passphrase")
            await app.wait_js(
                "document.querySelector('input[aria-label=\"Ask for my passphrase when OpenArcanum starts\"]').checked",
                "on",
            )
            assert not await app.exists(".settings .key-file-notice")
            assert not key_file.exists()
        finally:
            await app.stop()
        app = await App(home, name="d").start()
        try:
            await app.wait_js(asks, "asks at start", 40)
            await asyncio.sleep(1)
            assert not await app.js(opened)
        finally:
            await app.stop()

    run(main(), 240)


@pytest.mark.gui
def test_stay_signed_in_with_a_keychain_in_the_ui(tmp_path):
    """With a keychain: the key is kept there (no warning about files); the
    next start opens without the passphrase even when the keychain is slow
    to answer; turning the option on and off again asks for the passphrase."""
    from uidriver import App
    from uiflows import create_profile, open_settings

    env = {
        "PYTHON_KEYRING_BACKEND": "keyring_backend.FileKeyring",
        "OPENARCANUM_TEST_KEYRING_FILE": str(tmp_path / "keyring.json"),
        "PYTHONPATH": os.pathsep.join([str(Path(__file__).parent), os.environ.get("PYTHONPATH", "")]),
    }
    box = 'input[aria-label="Ask for my passphrase when OpenArcanum starts"]'

    async def main():
        home = tmp_path / "home"
        app = await App(home, env=env, name="a").start()
        try:
            await create_profile(app)
            assert "OpenArcanum profile key" in (tmp_path / "keyring.json").read_text()
            assert not keychain.key_file(Paths.under(home)).exists()
            await open_settings(app)
            await app.click(".settings nav .item", "Security")
            await app.wait_text(".settings .sign-in-section .desc", "kept in this computer's keychain")
            assert not await app.exists(".settings .key-file-notice")
            # On, then off again: each asks for the passphrase.
            await app.click(box)
            await app.fill('.modal input[aria-label="Passphrase"]', PASS)
            await app.button("Ask for it")
            await app.wait_gone(".modal h2", "Ask for your passphrase")
            await app.wait_js(f"document.querySelector('{box}').checked", "on")
            assert "OpenArcanum profile key" not in (tmp_path / "keyring.json").read_text()
            await app.click(box)
            await app.fill('.modal input[aria-label="Passphrase"]', PASS)
            await app.button("Stay signed in")
            await app.wait_gone(".modal h2", "Stay signed in on this device")
            await app.wait_js(f"!document.querySelector('{box}').checked", "off")
            assert "OpenArcanum profile key" in (tmp_path / "keyring.json").read_text()
            await app.click(".settings .close")
        finally:
            await app.stop()
        app = await App(home, env={**env, "OPENARCANUM_TEST_KEYRING_DELAY": "2"}, name="b").start()
        try:
            await app.wait_js(
                "!document.querySelector('#app').classList.contains('hidden')", "unlocked without the passphrase", 40
            )
        finally:
            await app.stop()

    run(main(), 180)


@pytest.mark.gui
def test_an_existing_profile_is_offered_once_in_the_ui(tmp_path):
    """A profile from before staying signed in was the default asks for the
    passphrase as it did; after it, a one-line choice. Stay signed in: the
    next start opens without the passphrase."""
    from uidriver import App
    from uiflows import unlock

    home = tmp_path / "home"
    paths = old_profile(home)

    async def main():
        app = await App(home, name="a").start()
        try:
            await app.wait_js("document.querySelector('#lock input[type=password]')", "asks as before", 40)
            await unlock(app)
            await app.wait_text(".modal h2", "Stay signed in on this device?")
            await app.wait_text(".modal .stay-signed-in-offer", "no keychain")
            assert not keychain.key_file(paths).exists()
            await app.button("Stay signed in")
            await app.wait_gone(".modal h2", "Stay signed in on this device?")
            await wait_until(lambda: keychain.key_file(paths).exists(), 10)
        finally:
            await app.stop()
        app = await App(home, name="b").start()
        try:
            await app.wait_js(
                "!document.querySelector('#app').classList.contains('hidden')", "opened without the passphrase", 40
            )
            assert not await app.exists(".modal h2", "Stay signed in on this device?")
        finally:
            await app.stop()

    run(main(), 180)


def test_passphrase_change_while_the_vault_keeps_saving(tmp_path):
    """Saves on the event loop during a passphrase change don't write the
    file under the old key after the change wrote it under the new one,
    which would leave a profile neither passphrase opens."""

    async def main():
        paths = Paths.under(tmp_path / "home")
        core = Core(paths, emit=Events())
        await core.create_profile(PASS)
        stop = asyncio.Event()

        async def keep_saving():
            while not stop.is_set():
                core.vault.data["settings"]["notify_mode"] = "all"
                core.save_vault()
                await asyncio.sleep(0.001)

        saver = asyncio.ensure_future(keep_saving())
        await core.change_passphrase(PASS, PASS + " new")
        await asyncio.sleep(0.05)
        stop.set()
        await saver
        await core.shutdown()
        Vault.unlock(paths, PASS + " new", None)  # opens with the new one
        # And the kept key was updated with it.
        assert Vault.unlock_with_key(paths, keychain.load(paths, "file"))

    run(main(), 60)


def counting_opens(core: Core) -> list:
    opened = []
    orig = core._opened

    async def counting():
        opened.append(1)
        await orig()

    core._opened = counting
    return opened


def test_concurrent_unlocks_open_the_profile_once(tmp_path, file_keyring):
    """A double click, or the kept key and the passphrase at once, open the
    profile once: one set of background loops, one Vault object."""

    async def main():
        paths = Paths.under(tmp_path / "home")
        core = Core(paths, emit=Events())
        await core.create_profile(PASS)
        await core.shutdown()

        again = Core(paths, emit=Events())
        opened = counting_opens(again)
        results = await asyncio.gather(again.unlock(PASS), again.unlock(PASS), again.unlock_remembered())
        assert [r["state"] for r in results] == ["unlocked"] * 3
        assert len(opened) == 1
        first = again.vault
        # Once open, a further unlock changes nothing.
        await again.unlock(PASS)
        await again.unlock_remembered()
        assert again.vault is first and len(opened) == 1
        # Locked, then two unlocks at once: one swaps the data back in.
        await again.lock()
        await asyncio.gather(again.unlock(PASS), again.unlock(PASS))
        assert again.unlocked and len(opened) == 1
        with pytest.raises(UserError, match="already open"):
            await again.create_profile(PASS)
        await again.shutdown()

    run(main(), 60)
