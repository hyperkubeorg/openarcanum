"""User settings (docs/settings.md): defaults, rejection of bad input,
persistence, and the live effect of each setting."""

import asyncio
import re
import time

import pytest
from conftest import PASS, REPO, Events, new_core, run, run_community, run_relay, wait_until
from test_e2e_community import WIZARD, general

from openarcanum import settings as S
from openarcanum.core import Core, should_notify
from openarcanum.errors import UserError
from openarcanum.paths import Paths
from openarcanum.settings import Preferences

EXPECTED_DEFAULTS = {
    "theme": "dark",
    "text_size": "medium",
    "compact": False,
    "data_dir": "",
    "update_check": True,
    "close_to_tray": True,
    "auto_lock_minutes": 0,
    "default_identity": "separate",
    "notifications": True,
    "notify_mode": "all",
    "muted_communities": [],
    "muted_channels": [],
    "community_notify": {},
    "sounds": True,
    "sound_volume": 50,
    "sounds_off": [],
    "maintainers": [],
    "maintainer_cluster_nodes": True,
    "preferred_relays": [],
    "directory_local": False,
    "cache_limit_mb": 1024,
    "voice_input": "default",
    "voice_output": "default",
    "voice_mode": "vad",
    "ptt_key": "Space",
    "ptt_global": True,
    "remember_device": False,
    "voice_sensitivity": -40,
    "voice_output_volume": 100,
    "voice_local_mutes": [],
    "screen_share_audio": True,
    "video_input": "default",
    "voice_volumes": {},
    "dm_policy": "contacts",
    "status_contacts": "full",
    "status_contacts_scope": "all",
    "status_contacts_chosen": [],
    "status_communities": {},
    "status_idle_minutes": 10,
    "device_name": "",
}

BAD = {
    "theme": ["blue", 3],
    "text_size": ["huge"],
    "compact": ["yes", 1],
    "data_dir": ["relative/path", 5],
    "update_check": ["yes", 1, None],
    "close_to_tray": ["yes", 0, None],
    "auto_lock_minutes": [-1, 1441, "15", 2.5, True],
    "default_identity": ["both"],
    "notifications": ["on"],
    "notify_mode": ["some"],
    "muted_communities": ["x", [1]],
    "muted_channels": [[""]],
    "community_notify": [[], {"c": "some"}, {"": "all"}, {"c": True}],
    "sounds": ["on", 1, None],
    "sound_volume": [-1, 101, "loud", True, 2.5],
    "sounds_off": ["message", ["ring"], [1], {}],
    "maintainers": [
        ["ftp://x"],
        ["not a url"],
        [f"http://n{i}" for i in range(9)],
        ["https://n.example.invalid#not-a-key"],
    ],
    "maintainer_cluster_nodes": ["yes", 1, None],
    "preferred_relays": [["http://relay"], ["ws:/broken"]],
    "directory_local": ["yes", 1],
    "cache_limit_mb": [10, 1_000_000, "big"],
    "voice_input": [5],
    "voice_output": [None],
    "voice_mode": ["always", "Studio"],
    "ptt_key": ["Ctrl+Alt+Del", ""],
    "ptt_global": ["yes", 1],
    "remember_device": ["yes", 1],
    "voice_sensitivity": [-101, 1, "loud"],
    "voice_output_volume": [-1, 201, "loud", True],
    "voice_local_mutes": [{}, [""], [1]],
    "screen_share_audio": ["yes", 1],
    "video_input": [7],
    "voice_volumes": [[], {"k": 3}, {"k": True}, {"": 1}],
    "dm_policy": ["anyone"],
    "status_contacts": ["everyone", True],
    "status_contacts_scope": ["some"],
    "status_contacts_chosen": ["x", [""], [1]],
    "status_communities": [[], {"c": "text"}, {"": "full"}, {"c": True}],
    "status_idle_minutes": [-1, 241, "10", 2.5, True],
    "device_name": ["", "x" * 65, "bad\nname"],
}


def test_every_setting_has_a_default_and_rejects_bad_input():
    assert {s.key for s in S.SETTINGS} == set(EXPECTED_DEFAULTS)
    for s in S.SETTINGS:
        assert s.default == EXPECTED_DEFAULTS[s.key], s.key
        for bad in BAD[s.key]:
            with pytest.raises(S.SettingError):
                S.validate(s.key, bad)
    assert S.validate("auto_lock_minutes", 0) == 0  # never
    assert S.validate("maintainers", "https://a/, https://b") == ["https://a", "https://b"]
    # Plain http only for a node on this computer (a local test network).
    assert S.validate("maintainers", "http://127.0.0.1:7100/") == ["http://127.0.0.1:7100"]
    with pytest.raises(S.SettingError, match="https"):
        S.validate("maintainers", "http://maintainer.example.invalid")
    assert S.validate("ptt_key", "F8") == "F8"


def test_preferences_saved_unencrypted_and_damage_tolerant(tmp_path):
    p = S.Preferences(tmp_path / "preferences.toml")
    p.set("theme", "dark")
    p.set("compact", True)
    p.set_window(10, 20, 1000, 700)
    again = S.Preferences(tmp_path / "preferences.toml")
    assert again.values["theme"] == "dark" and again.values["compact"] is True
    assert again.window["window_width"] == 1000
    (tmp_path / "preferences.toml").write_text("theme = [broken")
    assert S.Preferences(tmp_path / "preferences.toml").values["theme"] == "dark"
    (tmp_path / "preferences.toml").write_text('theme = "purple"\n')
    assert S.Preferences(tmp_path / "preferences.toml").values["theme"] == "dark"
    # A theme saved before dark became the default is kept.
    (tmp_path / "preferences.toml").write_text('theme = "system"\n')
    assert S.Preferences(tmp_path / "preferences.toml").values["theme"] == "system"


def test_passphrase_strength():
    assert not S.passphrase_strength("short")["ok"]
    assert not S.passphrase_strength("aaaaaaaaaaaaaa")["ok"]
    assert S.passphrase_strength("correct horse battery staple")["ok"]
    with pytest.raises(UserError):
        S.check_new_passphrase("1234567890")


def test_vault_settings_persist_and_apply_live(tmp_path):
    async def main():
        core, ev = await new_core(tmp_path / "a")
        await core.set_setting("notify_mode", "mentions")
        # Sounds (sounds.js): kept per device, a duplicate dropped.
        assert (await core.set_setting("sounds_off", ["dm", "join", "dm"]))["value"] == ["dm", "join"]
        await core.set_setting("sound_volume", 20)
        assert {"sounds", "sound_volume", "sounds_off"} <= set(S.PER_DEVICE)
        await core.set_setting("maintainers", ["http://127.0.0.1:1"])
        assert core.meta.urls == ["http://127.0.0.1:1"]  # live
        await core.set_setting("theme", "light")
        assert ev.of("appearance")[-1]["theme"] == "light"
        with pytest.raises(UserError):
            await core.set_setting("cache_limit_mb", 1)
        with pytest.raises(UserError):
            await core.set_setting("nonsense", 1)
        await core.shutdown()
        again = Core(Paths.under(tmp_path / "a"))
        assert again.prefs.values["theme"] == "light"  # readable before unlock
        assert again.meta.urls == ["http://127.0.0.1:1"]  # a preference: in use before unlock
        await again.unlock(PASS)
        assert again.settings["notify_mode"] == "mentions"
        assert again.settings["sounds_off"] == ["dm", "join"] and again.settings["sound_volume"] == 20
        await again.shutdown()

    run(main())


def test_old_voice_sensitivity_default_moves_to_the_new_one(tmp_path):
    """Voice activity's old default (-50 dBFS) opened on room noise
    (test_calls.py measures it). A profile still on it, never set by its
    user, moves to the new default when opened; one where the user chose
    -50 (here or on a linked device: the setting has a time) or anything
    else keeps it."""

    async def reopened(home):
        again = Core(Paths.under(home))
        await again.unlock(PASS)
        value = again.settings["voice_sensitivity"]
        await again.shutdown()
        return value

    async def main():
        assert S.VOICE_SENSITIVITY_DEFAULT == -40 and S.VOICE_SENSITIVITY_OLD_DEFAULT == -50
        # An old profile: the vault holds the old default, never chosen.
        core, _ = await new_core(tmp_path / "old")
        assert core.settings["voice_sensitivity"] == -40  # new profiles start on the new one
        core.vault.settings["voice_sensitivity"] = -50
        core.save_vault()
        await core.shutdown()
        assert await reopened(tmp_path / "old") == -40
        # Chosen by the user: kept.
        core, _ = await new_core(tmp_path / "chosen")
        await core.set_setting("voice_sensitivity", -50)
        await core.shutdown()
        assert await reopened(tmp_path / "chosen") == -50
        # Any other value: kept.
        core, _ = await new_core(tmp_path / "other")
        core.vault.settings["voice_sensitivity"] = -55
        core.save_vault()
        await core.shutdown()
        assert await reopened(tmp_path / "other") == -55

    run(main())


def test_maintainers_are_a_per_device_preference(tmp_path):
    """The maintainer nodes live in preferences.toml: readable and changeable
    before a profile exists and while locked, validated, applied at once, and
    never in the vault. The list in use: this device's own, else
    --maintainer, else the built-in list."""

    async def main():
        core = Core(Paths.under(tmp_path / "a"), emit=(ev := Events()))
        st = core.maintainers_status()
        assert st["own"] == [] and st["source"] == "builtin" and st["effective"] == list(S.BUILTIN_MAINTAINERS)
        assert core.profile_status()["maintainers"] == st and st["max"] == 8
        # Before a profile exists.
        st = await core.set_maintainers(["http://127.0.0.1:7100/", "https://m2.example.invalid"])
        assert st["own"] == ["http://127.0.0.1:7100", "https://m2.example.invalid"] and st["source"] == "own"
        assert core.meta.urls == st["own"] and ev.of("maintainers")[-1] == st
        text = (tmp_path / "a" / "config" / "preferences.toml").read_text()
        assert 'maintainers = ["http://127.0.0.1:7100", "https://m2.example.invalid"]' in text
        # Refusals change nothing.
        for bad in (["http://node.example.invalid"], ["ftp://x"], [f"https://n{i}" for i in range(9)], "x", [3]):
            with pytest.raises(UserError):
                await core.set_maintainers(bad)
        assert core.prefs.values["maintainers"] == st["own"] and core.meta.urls == st["own"]
        # The editor's Add, with the setting's own rules.
        assert core.check_new_maintainer(" https://n.example.invalid/ ", ["https://a"]) == [
            "https://a",
            "https://n.example.invalid",
        ]
        refusals = {
            "": "Enter the node's address",
            "http://node.example.invalid": r"Use https:// \(http:// only for this computer\)",
            "node.example.invalid": "Use https://",
            "https://a b": "one address at a time",
            "https://a/": "already in the list",
        }
        for url, message in refusals.items():
            with pytest.raises(UserError, match=message):
                core.check_new_maintainer(url, ["https://a"])
        with pytest.raises(UserError, match="at most 8"):
            core.check_new_maintainer("https://z", [f"https://n{i}" for i in range(8)])

        await core.create_profile(PASS, "desk")
        assert "maintainers" not in core.settings
        assert core.get_settings()["maintainers"]["own"] == st["own"]
        # While locked: saved and in use at once, so the next unlock uses it.
        await core.lock()
        st = await core.set_maintainers(["https://m3.example.invalid"])
        assert core.meta.urls == ["https://m3.example.invalid"]
        await core.unlock(PASS)
        assert core.meta.urls == ["https://m3.example.invalid"] and "maintainers" not in core.settings
        # "Use the defaults": the launch option, then the built-in list.
        try:
            S.set_launch_maintainers(["http://127.0.0.1:7200"])
            st = await core.set_maintainers([])
            assert st["source"] == "launch" and core.meta.urls == ["http://127.0.0.1:7200"]
            assert st["launch"] == ["http://127.0.0.1:7200"]
        finally:
            S.set_launch_maintainers([])
        assert Preferences(tmp_path / "a" / "config" / "preferences.toml").values["maintainers"] == []
        await core.shutdown()
        again = Core(Paths.under(tmp_path / "a"))
        assert again.maintainers_status()["source"] == "builtin"
        assert again.meta.urls == list(S.BUILTIN_MAINTAINERS)
        await again.shutdown()

    run(main())


def test_auto_lock_after_idle(tmp_path):
    async def main():
        core, ev = await new_core(tmp_path / "a")
        await core.set_setting("auto_lock_minutes", 1)
        core.last_activity = time.monotonic() - 61
        await wait_until(lambda: any(p["state"] == "locked" for p in ev.of("profile")), 10)
        assert not core.unlocked
        # Identity keys are gone from memory, but the vault on disk keeps them.
        assert all("seed" not in i for i in core.vault.data["identities"].values())
        await core.unlock(PASS)
        await core.set_setting("auto_lock_minutes", 0)
        core.last_activity = time.monotonic() - 10_000
        await asyncio.sleep(6)
        assert core.unlocked  # never
        await core.shutdown()

    run(main(), 60)


def test_change_passphrase(tmp_path):
    async def main():
        core, _ = await new_core(tmp_path / "a")
        with pytest.raises(UserError, match="current passphrase"):
            await core.change_passphrase("wrong wrong wrong", "another good passphrase")
        with pytest.raises(UserError):
            await core.change_passphrase(PASS, "weak")
        # The wrong passphrase above started the unlock wait.
        core.guard.now = lambda: time.time() + 100
        await core.change_passphrase(PASS, "another good passphrase")
        await core.shutdown()
        again = Core(Paths.under(tmp_path / "a"))
        with pytest.raises(UserError):
            await again.unlock(PASS)
        again.guard.now = lambda: time.time() + 100
        await again.unlock("another good passphrase")
        await again.shutdown()

    run(main())


def test_notification_policy():
    m = {"channel_id": 3, "text": "hello @olive", "author_name": "Al", "file": None}
    base = {"notifications": True, "notify_mode": "all", "muted_communities": [], "muted_channels": []}
    assert should_notify(base, "c", m, "Olive")[0] == "Al"
    assert should_notify(dict(base, notifications=False), "c", m, "Olive")[0] is None
    assert should_notify(dict(base, muted_communities=["c"]), "c", m, "Olive")[0] is None
    plain = dict(m, text="no mention")
    assert should_notify(dict(base, notify_mode="mentions"), "c", plain, "Olive")[0] is None
    assert should_notify(dict(base, notify_mode="mentions"), "c", m, "Olive")[0] == "Al"
    # Mentions break through a channel mute, but not a community mute.
    assert should_notify(dict(base, muted_channels=["c:3"]), "c", plain, "Olive")[0] is None
    assert should_notify(dict(base, muted_channels=["c:3"]), "c", m, "Olive")[0] == "Al"
    # Per community: its own level wins over the default, both ways; Nothing
    # silences mentions too; other communities keep the default.
    mentions_here = dict(base, community_notify={"c": "mentions"})
    assert should_notify(mentions_here, "c", plain, "Olive")[0] is None
    assert should_notify(mentions_here, "c", m, "Olive")[0] == "Al"
    assert should_notify(mentions_here, "d", plain, "Olive")[0] == "Al"
    all_here = dict(base, notify_mode="mentions", community_notify={"c": "all"})
    assert should_notify(all_here, "c", plain, "Olive")[0] == "Al"
    assert should_notify(all_here, "d", plain, "Olive")[0] is None
    nothing_here = dict(base, community_notify={"c": "nothing"})
    assert should_notify(nothing_here, "c", m, "Olive")[0] is None
    assert should_notify(nothing_here, "d", m, "Olive")[0] == "Al"


def test_data_folder_moves_live(tmp_path):
    async def main():
        core, _ = await new_core(tmp_path / "a")
        target = tmp_path / "elsewhere"
        await core.set_setting("data_dir", str(target))
        assert (target / "data" / "vault.bin").exists()
        assert not (tmp_path / "a" / "data" / "vault.bin").exists()
        core.save_vault()  # keeps working from the new place
        await core.shutdown()
        again = Core(Paths.under(tmp_path / "a"))  # config dir remembers the move
        assert again.profile_status()["state"] == "locked"
        await again.unlock(PASS)
        await again.set_setting("data_dir", "")
        assert (tmp_path / "a" / "data" / "vault.bin").exists()
        await again.shutdown()

    run(main())


def test_cache_limit_and_clear(tmp_path):
    with run_relay() as relay, run_community(tmp_path, [relay]) as node:

        async def main():
            core, _ = await new_core(tmp_path / "a")
            cid = (await core.claim(node.claim_code, WIZARD, "Olive"))["community"]
            gen = await general(core, cid)
            for i in range(30):
                await core.send(cid, gen, f"message {i} " + "x" * 3000)
            core.cache(cid).save()
            core.cache(cid).put_file(1, b"y" * (2 << 20))
            from openarcanum.cache import enforce_limit

            enforce_limit([core.cache(cid)], 64 * 1024)
            assert core.cache(cid).get_file(1) is None  # files go first
            assert core.cache(cid).size() <= 64 * 1024 or len(core.cache(cid).channel(gen)) < 30
            await core.clear_cache()
            assert core.cache(cid).size() == 0
            # History comes back from the server.
            assert len((await core.history(cid, gen))["messages"]) == 30
            await core.shutdown()

        run(main(), 90)


def test_preferred_relays_tried_first(tmp_path):
    with run_relay() as r1, run_relay() as r2:
        from conftest import CommunityNode

        node = CommunityNode(tmp_path / "c", [r1, r2])
        node.start()
        try:

            async def main():
                core, _ = await new_core(tmp_path / "a")
                await core.set_setting("preferred_relays", [r2])
                cid = (await core.claim(node.claim_code, WIZARD, "Olive"))["community"]
                await general(core, cid)
                assert core.conns[cid]._relay_order()[0] in (r2, core.conns[cid].relay_url)
                core.conns[cid].relay_url = ""
                assert core.conns[cid]._relay_order()[0] == r2
                await core.shutdown()

            run(main(), 60)
        finally:
            node.stop()


def test_default_identity_and_display_name(tmp_path):
    with run_relay() as relay, run_community(tmp_path, [relay]) as node:

        async def main():
            owner, _ = await new_core(tmp_path / "o")
            res = await owner.claim(node.claim_code, WIZARD, "Olive")
            cid = res["community"]
            await general(owner, cid)
            alice, _ = await new_core(tmp_path / "a")
            await alice.set_setting("default_identity", "reuse")
            pv = await alice.preview({"invite": res["invite"]})
            assert pv["default_identity"] == "reuse"
            await alice.join({"invite": res["invite"]}, "new", "Alice")
            await general(alice, cid)
            await alice.set_display_name(cid, "Alice B")
            members = await owner.members(cid)
            assert "Alice B" in [m["display_name"] for m in members]
            with pytest.raises(UserError):
                await alice.set_display_name(cid, "")
            # Avatars: an image is accepted, other files are refused.
            png = tmp_path / "a.png"
            import shutil
            from pathlib import Path

            shutil.copy(Path(__file__).parents[2] / "shared/ui/brand/openarcanum-64.png", png)
            await alice.set_avatar(cid, str(png))
            txt = tmp_path / "a.txt"
            txt.write_text("not an image")
            with pytest.raises(UserError):
                await alice.set_avatar(cid, str(txt))
            for c in (owner, alice):
                await c.shutdown()

        run(main(), 60)


def test_events_are_json_serializable():
    from openarcanum.bus import payload

    payload("x", {"a": b"bytes", "b": {1, 2}})
    Events()


def test_launch_maintainers_and_builtin_placeholders():
    """--maintainer replaces the built-in list for one run; this device's own
    list (preferences) wins. The built-in list is the project's own maintainer node."""
    assert S.DEFAULT_MAINTAINERS == ("https://maintainer-01.openarcanum.org",)
    # The suite runs against a stand-in list (conftest), never the public node.
    assert S.BUILTIN_MAINTAINERS == ("https://maintainer.example.invalid",)
    assert S._builtin_maintainers({}) == S.DEFAULT_MAINTAINERS
    assert S._builtin_maintainers({S.TEST_BUILTIN_ENV: "https://a.example.invalid, https://b.example.invalid"}) == (
        "https://a.example.invalid",
        "https://b.example.invalid",
    )
    try:
        assert S.effective_maintainers([]) == list(S.BUILTIN_MAINTAINERS)
        S.set_launch_maintainers(["http://127.0.0.1:7100/"])
        assert S.effective_maintainers([]) == ["http://127.0.0.1:7100"]
        assert S.effective_maintainers(["https://mine.example.invalid"]) == ["https://mine.example.invalid"]
        with pytest.raises(S.SettingError):
            S.set_launch_maintainers(["http://maintainer.example.invalid"])
    finally:
        S.set_launch_maintainers([])


def test_save_folder_is_downloads_or_home(tmp_path, monkeypatch):
    """Save… in a DM suggests the Downloads folder, and the home folder on
    systems without one (saving to a folder that doesn't exist fails)."""
    from openarcanum import paths

    monkeypatch.setenv("HOME", str(tmp_path))
    monkeypatch.setattr(paths.platformdirs, "user_downloads_dir", lambda: str(tmp_path / "Downloads"))
    assert paths.save_folder() == tmp_path
    (tmp_path / "Downloads").mkdir()
    assert paths.save_folder() == tmp_path / "Downloads"


# Settings changed through their own API call rather than set_setting: the
# call the UI makes for each.
SET_BY_API = {
    "remember_device": "set_remember",
    "maintainers": "set_maintainers",
    "voice_volumes": "voice_volume",
    "voice_local_mutes": "voice_local_mute",
    "device_name": "rename_device",
    "muted_communities": "toggle_mute",
    "muted_channels": "toggle_mute",
    "community_notify": "set_community_notify",
    "status_communities": "set_status_community",
}


def _doc_section(heading: str) -> str:
    doc = (REPO / "docs/settings.md").read_text(encoding="utf-8")
    start = doc.index(f"\n{heading}\n")
    end = doc.index("\n## ", start + len(heading))
    return doc[start:end]


def test_settings_doc_matches_settings():
    """docs/settings.md "User settings" names every client setting by its key, and only keys that
    exist (the node half: internal/config TestSettingsInventoryMatchesNodeConfigs)."""
    section = _doc_section("## User settings").splitlines()
    rows = [ln for ln in section if ln.startswith("| ") and not ln.startswith("| Area ")]
    listed = {k for ln in rows for k in re.findall(r"`([a-z][a-z0-9_]*)`", ln)}
    keys = {s.key for s in S.SETTINGS}
    assert not keys - listed, f"settings missing from docs/settings.md: {sorted(keys - listed)}"
    assert not listed - keys, f"docs/settings.md names keys settings.py doesn't have: {sorted(listed - keys)}"
    # Settings kept in preferences.toml (readable before unlock, per device)
    # say so in their row.
    for s in S.SETTINGS:
        if s.store == "prefs":
            row = next(ln for ln in rows if f"`{s.key}`" in ln)
            assert "preferences.toml" in row, f"docs/settings.md: {s.key} is in preferences.toml; its row must say so"


def test_every_setting_has_ui():
    """The settings rule: each setting can be changed in the UI (none is only
    reachable by editing files), through set_setting or its own call."""
    js = "\n".join(p.read_text(encoding="utf-8") for p in (REPO / "clients/shared/ui/js").glob("*.js"))
    for s in S.SETTINGS:
        if s.key in SET_BY_API:
            assert f"api('{SET_BY_API[s.key]}'" in js, s.key
        else:
            assert re.search(rf"['\"]{s.key}['\"]", js), f"{s.key} has no control in the UI"
