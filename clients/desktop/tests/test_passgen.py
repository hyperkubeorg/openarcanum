"""Generated passphrases (openarcanum/passgen.py) and where the app keeps its
files (Settings → Data & storage, Core.storage_locations)."""

import hashlib
import math
from pathlib import Path

import pytest
from conftest import new_core, run

from openarcanum import keychain, passgen
from openarcanum import settings as S
from openarcanum.errors import UserError


def test_wordlist_is_the_published_eff_list():
    data = passgen.WORDLIST.read_bytes()
    assert hashlib.sha256(data).hexdigest() == passgen.WORDLIST_SHA256
    words = passgen.wordlist()
    assert len(words) == len(set(words)) == 7776
    assert words[0] == "abacus" and words[-1] == "zoom"
    # Separated by spaces, a generated passphrase splits back into its words
    # unambiguously (four words have a hyphen).
    assert all(" " not in w and w == w.strip().lower() for w in words)
    vendored = (passgen.WORDLIST.parent / "VENDORED.md").read_text()
    assert passgen.WORDLIST_SHA256 in vendored and "CC BY 3.0" in vendored


def test_generated_passphrases_are_strong_random_words():
    words = set(passgen.wordlist())
    seen = set()
    for _ in range(200):
        p = passgen.generate()
        parts = p.split(passgen.SEPARATOR)
        assert len(parts) == passgen.WORDS == 6
        assert all(w in words for w in parts)
        # It passes the same check a typed passphrase must.
        S.check_new_passphrase(p)
        assert S.passphrase_strength(p)["score"] >= 3
        seen.add(p)
    assert len(seen) == 200
    assert passgen.bits() == pytest.approx(6 * math.log2(7776))
    assert passgen.bits() > 77
    for bad in (3, 13):
        with pytest.raises(ValueError):
            passgen.generate(bad)


def test_generator_uses_the_os_csprng(monkeypatch):
    """Words come from `secrets` (the OS CSPRNG), one draw per word."""
    calls = []

    def choice(seq):
        calls.append(len(seq))
        return seq[len(calls)]

    monkeypatch.setattr(passgen.secrets, "choice", choice)
    assert passgen.generate() == " ".join(passgen.wordlist()[1:7])
    assert calls == [7776] * 6


def test_a_damaged_wordlist_is_refused(tmp_path, monkeypatch):
    bad = tmp_path / "list.txt"
    bad.write_bytes(passgen.WORDLIST.read_bytes().replace(b"abacus", b"abacuz"))
    monkeypatch.setattr(passgen, "WORDLIST", bad)
    passgen.wordlist.cache_clear()
    try:
        with pytest.raises(RuntimeError):
            passgen.generate()
    finally:
        passgen.wordlist.cache_clear()


def test_storage_locations_list_every_path_with_full_paths(tmp_path):
    async def main():
        core, _ = await new_core(tmp_path / "home")
        locs = core.storage_locations()
        by_id = {loc["id"]: loc for loc in locs}
        home = (tmp_path / "home").resolve()
        expect = {
            "config": home / "config",
            "preferences": home / "config" / "preferences.toml",
            "updates": home / "config" / "updates.json",
            "data": home / "data",
            "vault": home / "data" / "vault.bin",
            "state": home / "data" / "state.bin",
            "dms": home / "data" / "dms.bin",
            "olm": home / "data" / "olm.bin",
            "guard": home / "data" / "unlock-guard.json",
            "sandbox": home / "data" / "engine-sandbox",
            "cache": home / "cache",
            "community_cache": home / "cache" / "communities",
            "dm_files": home / "cache" / "dm-files",
            "update_cache": home / "cache" / "updates",
            "logs": home / "log",
            "log": home / "log" / "client.log",
            "crash": home / "log" / "crash.log",
        }
        for lid, path in expect.items():
            assert by_id[lid]["path"] == str(path), lid
            assert Path(by_id[lid]["path"]).is_absolute()
            assert by_id[lid]["label"] and by_id[lid]["desc"], lid
        assert by_id["vault"]["exists"] and by_id["data"]["exists"]
        # The keychain entry by its service and account names.
        assert by_id["keychain"]["path"] == f"{keychain.SERVICE} / {keychain.account_name(core.paths)}"
        assert by_id["keychain"]["kind"] == "keychain"
        # Every id is unique.
        assert len(by_id) == len(locs)

        # Open folder shows only the app's own folders: a file's folder, a
        # folder itself, never the keychain or anything else.
        assert core.folder_of("vault") == str(home / "data")
        assert core.folder_of("cache") == str(home / "cache")
        for bad in ("keychain", "/etc", "../data", ""):
            with pytest.raises(UserError):
                core.folder_of(bad)
        # A folder that doesn't exist yet is said so.
        with pytest.raises(UserError):
            core.folder_of("update_cache")

        # Moving the data folder moves the listed data and cache paths too.
        moved = tmp_path / "elsewhere"
        await core.set_setting("data_dir", str(moved))
        by_id = {loc["id"]: loc for loc in core.storage_locations()}
        assert by_id["vault"]["path"] == str(moved.resolve() / "data" / "vault.bin")
        assert by_id["community_cache"]["path"] == str(moved.resolve() / "cache" / "communities")
        assert by_id["preferences"]["path"] == str(home / "config" / "preferences.toml")
        await core.shutdown()

    run(main(), 60)
