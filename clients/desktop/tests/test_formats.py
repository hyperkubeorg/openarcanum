"""Client on-disk formats and their migrations: each file is written here
by hand in its first released layout
(version 1, or the unversioned layout from before a file had a version
field) and loaded with the current code, so a format change that forgets its
migration fails. Files from a newer version are refused with a message, and
the precious ones (vault, backups, Olm store, DM history) are never
overwritten; disposable ones (caches, UI and probe state) are set aside.

When a format's version goes up, keep its version-1 test here (or move it
to a "previous version" fixture) and add one for the new version."""

import base64
import hashlib
import json
import os
import time

import pytest
from conftest import PASS, run
from cryptography.hazmat.primitives.ciphers.aead import ChaCha20Poly1305

from openarcanum import app, keys
from openarcanum.cache import CommunityCache
from openarcanum.dm import DMHistory
from openarcanum.errors import UserError
from openarcanum.olm import OlmError, OlmStore
from openarcanum.paths import Paths
from openarcanum.settings import Preferences
from openarcanum.updates import UpdateConfig, Updater
from openarcanum.vault import StateStore, UnlockGuard, Vault, VaultError

SCRYPT = {"n": 1 << 10, "r": 8, "p": 1}


def b64(b: bytes) -> str:
    return base64.b64encode(b).decode()


def seal_v1(key: bytes, plaintext: bytes, aad: bytes) -> bytes:
    """nonce || ChaCha20-Poly1305 ciphertext (the sealed-blob layout)."""
    nonce = os.urandom(12)
    return nonce + ChaCha20Poly1305(key).encrypt(nonce, plaintext, aad)


def passphrase_file_v1(magic: bytes, plaintext: bytes, v: int = 1) -> bytes:
    """MAGIC || header length (4, big endian) || JSON header || nonce || ct,
    keyed by scrypt(passphrase) and bound to MAGIC || header."""
    salt = os.urandom(16)
    header = json.dumps({"v": v, "kdf": "scrypt", **SCRYPT, "salt": b64(salt)}).encode()
    key = hashlib.scrypt(PASS.encode(), salt=salt, dklen=32, maxmem=256 << 20, **SCRYPT)
    nonce = os.urandom(12)
    ct = ChaCha20Poly1305(key).encrypt(nonce, plaintext, magic + header)
    return magic + len(header).to_bytes(4, "big") + header + nonce + ct


# The vault as version 1 first wrote it: before contacts, DMs and devices
# existed, with only the settings of that time (later ones get defaults).
VAULT_V1 = {
    "version": 1,
    "created": 1761264000,
    "device_id": "0011223344556677",
    "identities": {},
    "communities": {},
    "settings": {"auto_lock_minutes": 30, "device_name": "Old laptop"},
    "state_key": b64(bytes(32)),
    "cache_key": b64(bytes(range(32))),
    "keys_revision": 0,
    "backup_revision": 0,
}


def test_vault_version_1_loads_and_newer_is_refused(tmp_path):
    paths = Paths.under(tmp_path / "p")
    paths.ensure()
    paths.vault.write_bytes(passphrase_file_v1(b"OARCVLT1", json.dumps(VAULT_V1).encode()))
    v = Vault.unlock(paths, PASS, guard=None)
    assert v.data["settings"]["auto_lock_minutes"] == 30
    assert v.data["settings"]["notify_mode"] == "all"  # added later: default
    assert v.data["devices"] == {} and v.data["contacts"] == {} and v.data["dms"] == {}
    v.save()
    blob = paths.vault.read_bytes()
    hlen = int.from_bytes(blob[8:12], "big")
    assert blob[:8] == b"OARCVLT1" and json.loads(blob[12 : 12 + hlen])["v"] == 1

    newer = passphrase_file_v1(b"OARCVLT1", json.dumps(VAULT_V1).encode(), v=2)
    paths.vault.write_bytes(newer)
    with pytest.raises(VaultError, match="newer version"):
        Vault.unlock(paths, PASS, guard=None)
    assert paths.vault.read_bytes() == newer


def test_backup_version_1_imports_and_newer_is_refused():
    payload = {"format": "openarcanum-backup", "version": 1, "created": 1761264000}
    payload |= {"identities": {}, "communities": [], "contacts": {}}
    data = keys.import_backup(passphrase_file_v1(b"OARCBAK1", json.dumps(payload).encode()), PASS)
    assert data["version"] == 1 and data["communities"] == []
    newer = passphrase_file_v1(b"OARCBAK1", json.dumps(payload | {"version": 2}).encode())
    with pytest.raises(VaultError, match="newer version"):
        keys.import_backup(newer, PASS)
    with pytest.raises(VaultError, match="newer version"):
        keys.import_backup(passphrase_file_v1(b"OARCBAK1", json.dumps(payload).encode(), v=2), PASS)


def test_state_file_version_1_loads(tmp_path):
    key = os.urandom(32)
    f = tmp_path / "state.bin"
    f.write_bytes(b"OARCST01" + seal_v1(key, json.dumps({"last_open": "c1"}).encode(), b"OARCST01"))
    s = StateStore(f, key)
    assert s.get("last_open") == "c1"
    s.set("draft", "hi")
    assert f.read_bytes()[:8] == b"OARCST01"
    # UI state is disposable: another version's file is set aside.
    f.write_bytes(b"OARCST02" + seal_v1(key, b"{}", b"OARCST02"))
    assert StateStore(f, key).data == {}


def test_message_cache_version_1_loads(tmp_path):
    key, cid = os.urandom(32), "community-1"
    msg = {"id": 7, "channel_id": 5, "text": "kept"}
    root = tmp_path / "cache"
    root.mkdir()
    (root / "messages.bin").write_bytes(
        seal_v1(key, json.dumps({"5": [msg]}).encode(), b"OARCCC01" + b"community-1/messages")
    )
    assert CommunityCache(root, key, cid).channel(5) == [msg]
    # A cache from another version fails authentication and is rebuilt
    # from the servers.
    (root / "messages.bin").write_bytes(seal_v1(key, b"{}", b"OARCCC02" + b"community-1/messages"))
    assert CommunityCache(root, key, cid).messages == {}


def test_dm_history_version_2_loads_and_newer_is_refused(tmp_path):
    key = os.urandom(32)
    f = tmp_path / "dms.bin"
    entry = {"id": "e1", "text": "hello", "at": 1}
    f.write_bytes(b"OARCDM02" + seal_v1(key, json.dumps({"g1": [entry]}).encode(), b"OARCDM02"))
    assert DMHistory(f, key).find("g1", "e1") == entry
    newer = b"OARCDM03" + seal_v1(key, b"{}", b"OARCDM03")
    f.write_bytes(newer)
    with pytest.raises(UserError, match="newer version"):
        DMHistory(f, key)
    assert f.read_bytes() == newer


def test_olm_store_version_1_loads_and_newer_is_refused(tmp_path):
    seal_key, pickle_key = os.urandom(32), os.urandom(32)
    f = tmp_path / "olm.bin"
    state = {"accounts": {}, "sessions": {"dm:a": {}}, "outbound": {}, "inbound": {}, "known": {}}
    f.write_bytes(b"OARCOLM1" + seal_v1(seal_key, json.dumps(state).encode(), b"OARCOLM1"))
    assert OlmStore(f, seal_key, pickle_key).sessions == {"dm:a": {}}
    newer = b"OARCOLM2" + seal_v1(seal_key, b"{}", b"OARCOLM2")
    f.write_bytes(newer)
    with pytest.raises(OlmError, match="newer version"):
        OlmStore(f, seal_key, pickle_key)
    assert f.read_bytes() == newer


def _profile_with_vault_maintainers(home, urls):
    """A profile in the older layout: the maintainer list inside the
    vault's settings."""
    from openarcanum.core import Core

    async def make():
        core = Core(Paths.under(home))
        await core.create_profile(PASS, "desk")
        await core.shutdown()

    run(make())
    paths = Paths.under(home)
    v = Vault.unlock(paths, PASS, guard=None)
    v.data["settings"]["maintainers"] = urls
    v.save()
    return paths


def test_vault_maintainers_move_to_preferences(tmp_path):
    """The maintainer list lives in preferences.toml (docs/settings.md), not
    the vault: opening an older profile moves it, unless the preferences
    already name a list (they win); either way it leaves the vault."""
    from openarcanum.core import Core

    old = ["http://127.0.0.1:7100", "https://m2.example.invalid"]

    async def open_profile(paths):
        core = Core(paths)
        await core.unlock(PASS)
        status = core.maintainers_status()
        urls = list(core.meta.urls)
        await core.shutdown()
        return status, urls

    # Preferences name none: the vault's list moves there.
    paths = _profile_with_vault_maintainers(tmp_path / "a", old)
    status, urls = run(open_profile(paths))
    assert status["own"] == old and status["source"] == "own" and urls == old
    assert Preferences(paths.preferences).values["maintainers"] == old
    assert "maintainers" not in Vault.unlock(paths, PASS, guard=None).data["settings"]

    # Preferences already have a list: kept; the vault's copy is dropped.
    paths = _profile_with_vault_maintainers(tmp_path / "b", old)
    Preferences(paths.preferences).set("maintainers", ["https://mine.example.invalid"])
    status, urls = run(open_profile(paths))
    assert status["own"] == urls == ["https://mine.example.invalid"]
    assert "maintainers" not in Vault.unlock(paths, PASS, guard=None).data["settings"]

    # An empty list in the vault (the old default) just goes.
    paths = _profile_with_vault_maintainers(tmp_path / "c", [])
    status, _urls = run(open_profile(paths))
    assert status["own"] == [] and status["source"] == "builtin"
    assert "maintainers" not in Vault.unlock(paths, PASS, guard=None).data["settings"]


def test_preferences_with_a_list_load(tmp_path):
    """preferences.toml holds lists of strings (the maintainer nodes), written
    and read back; a bad list is ignored like any bad value."""
    f = tmp_path / "preferences.toml"
    p = Preferences(f)
    p.set("maintainers", ["https://a.example.invalid", 'https://b.example.invalid/"q"'])
    assert Preferences(f).values["maintainers"] == p.values["maintainers"]
    f.write_text('format = 1\nmaintainers = ["ftp://x"]\ntheme = "dark"\n')
    again = Preferences(f)
    assert again.values["maintainers"] == [] and again.values["theme"] == "dark"


def test_preferences_unversioned_and_newer_load(tmp_path):
    f = tmp_path / "preferences.toml"
    f.write_text('# OpenArcanum preferences. Nothing here identifies you.\ntheme = "dark"\nwindow_width = 900\n')
    p = Preferences(f)
    assert p.values["theme"] == "dark" and p.window["window_width"] == 900
    p.set("compact", True)
    assert "format = 1" in f.read_text().splitlines()
    # A newer format still yields the keys this build knows.
    f.write_text('format = 2\ntheme = "light"\nsomething_new = [1, 2]\n')
    assert Preferences(f).values["theme"] == "light"


def test_preferences_from_before_staying_signed_in(tmp_path):
    """Written before preferences recorded where the key is kept: "Remember
    on this device" on means the OS keychain; off means nothing is kept, and
    the one-time offer is still to come."""
    f = tmp_path / "preferences.toml"
    f.write_text("format = 1\nremember_device = true\n")
    p = Preferences(f)
    assert p.values["remember_device"] and p.unlock_key_in == "keychain"
    f.write_text("format = 1\nremember_device = false\n")
    p = Preferences(f)
    assert not p.values["remember_device"] and p.unlock_key_in == ""
    assert "stay_signed_in_offered" not in p.shown
    # Once recorded, the record wins (a lock deleted the key).
    f.write_text('format = 1\nremember_device = true\nunlock_key_in = ""\n')
    assert Preferences(f).unlock_key_in == ""
    f.write_text('format = 1\nremember_device = true\nunlock_key_in = "somewhere"\n')
    assert Preferences(f).unlock_key_in == ""


def test_unlock_key_file_version_1_loads_and_others_are_ignored(tmp_path):
    """The key file kept where there is no keychain: version 1 opens the
    profile; another version counts as nothing kept (the passphrase opens it
    and writes a new one)."""
    from openarcanum import keychain

    paths = Paths.under(tmp_path / "home")
    paths.ensure()
    v = Vault.create(paths, PASS, "desk")
    key = keychain.key_file(paths)
    key.write_text(json.dumps({"v": 1, "key": b64(v.file_key())}))
    os.chmod(key, 0o600)
    assert Vault.unlock_with_key(paths, keychain.load(paths, "file"))
    key.write_text(json.dumps({"v": 2, "key": b64(v.file_key())}))
    assert keychain.load(paths, "file") is None
    key.write_text("not json")
    assert keychain.load(paths, "file") is None


def test_unlock_guard_unversioned_loads(tmp_path):
    f = tmp_path / "unlock-guard.json"
    f.write_text(json.dumps({"failures": 3, "next": time.time() + 30}))
    g = UnlockGuard(f)
    assert 25 < g.remaining() <= 30
    g.failed()
    st = json.loads(f.read_text())
    assert st["v"] == 1 and st["failures"] == 4
    f.write_text("[]")  # damaged: no backoff, no crash
    assert UnlockGuard(f).remaining() == 0


def test_update_state_unversioned_loads_and_newer_is_set_aside(tmp_path):
    f = tmp_path / "updates.json"
    f.write_text(json.dumps({"skipped": "1.2.0"}) + "\n")

    def updater():
        return Updater(
            f,
            cache_dir=lambda: tmp_path / "cache",
            auto_check=lambda: False,
            emit=lambda *_: None,
            config=UpdateConfig((), "", False),
            version="1.0.0",
            platform_name="linux-x86_64",
            target=(None, "test"),
        )

    u = updater()
    assert u.skipped == "1.2.0"
    u.skip("1.3.0")
    assert json.loads(f.read_text()) == {"skipped": "1.3.0", "v": 1}
    f.write_text(json.dumps({"skipped": "9.9.9", "v": 2}))
    assert updater().skipped == ""


def test_engine_sandbox_record_unversioned_loads(tmp_path, monkeypatch):
    monkeypatch.delenv("QTWEBENGINE_DISABLE_SANDBOX", raising=False)
    monkeypatch.setattr(app, "linux_sandbox_available", lambda: True)
    f = tmp_path / app.SANDBOX_STATE
    f.write_text("trying 1.0\n")  # a start that never reached the UI
    assert app.decide_sandbox(f, "1.0")[0] is False
    assert f.read_text() == "1 failed 1.0\n"
    f.write_text("something else entirely\n")
    assert app.decide_sandbox(f, "1.0") == (True, "")
    app.sandbox_started(f, "1.0")
    assert f.read_text() == "1 ok 1.0\n"


def test_host_file_version_1_and_unversioned_load(tmp_path):
    """hosted/<id>/host.json (openarcanum/hosting.py): version 1, the
    unversioned layout and a newer version are read the same way; a folder
    whose file can't be read counts as enabled; folders without a server key
    aren't hosted communities."""
    from openarcanum import hosting

    root = tmp_path / "hosted"
    layouts = {
        "v1off": '{"v": 1, "enabled": false}',
        "plain": '{"enabled": false}',
        "newer": '{"v": 9, "enabled": false, "later": 1}',
        "broken": "not json",
    }
    for name, text in layouts.items():
        (root / name / hosting.SERVER_DIR).mkdir(parents=True)
        (root / name / hosting.SERVER_DIR / "server.key").write_text("00" * 32)
        (root / name / hosting.HOST_FILE).write_text(text)
    (root / "empty").mkdir()
    hm = hosting.HostManager(lambda: root, lambda: tmp_path / "log", lambda hid: None)
    hm.load()
    assert {hid: h.enabled for hid, h in hm.items.items()} == {
        "v1off": False,
        "plain": False,
        "newer": False,
        "broken": True,
    }
