"""Auto-update (docs/release.md): the signed manifest,
every refusal, downloads, consent, and installing on each platform. The
Linux AppImage install runs for real against a temp file standing in for
$APPIMAGE; the Windows and macOS installers run on those systems."""

from __future__ import annotations

import asyncio
import base64
import hashlib
import itertools
import json
import os
import subprocess
import sys
import time
from pathlib import Path

import pytest
from conftest import run
from updateserver import UpdateServer, asset_for, fake_appimage, publish

from openarcanum import keys, updates
from openarcanum.updates import InstallTarget, UpdateConfig, UpdateError, Updater

REPO = Path(__file__).resolve().parents[3]
SCRIPT = REPO / "clients" / "desktop" / "scripts" / "release_manifest.py"
OLD = b"old appimage contents\n"
NEW = b"\x7fELF new appimage " + os.urandom(300_000)


@pytest.fixture
def key():
    return keys.SigningKey.generate()


@pytest.fixture
def server():
    with UpdateServer() as s:
        yield s


class Rig:
    """An updater for "1.0.0" on Linux, installed as an AppImage in tmp."""

    def __init__(self, tmp: Path, server: UpdateServer, trusted: list[bytes], **kw):
        self.tmp = tmp
        self.appimage = tmp / "app" / "OpenArcanum.AppImage"
        self.appimage.parent.mkdir(parents=True, exist_ok=True)
        self.appimage.write_bytes(OLD)
        self.appimage.chmod(0o755)
        self.events: list[dict] = []
        self.auto = True
        cfg = kw.pop("config", None) or UpdateConfig(tuple(trusted), server.url("/manifest.json"), True)
        self.u = Updater(
            tmp / "config" / "updates.json",
            cache_dir=lambda: tmp / "cache" / "updates",
            auto_check=lambda: self.auto,
            emit=lambda e, d: self.events.append(d),
            config=cfg,
            version=kw.pop("version", "1.0.0"),
            platform_name="linux-x86_64",
            target=kw.pop("target", (InstallTarget("appimage", self.appimage), "")),
            restart_args=["--home", str(tmp / "home")],
            **kw,
        )

    def check(self, manual: bool = True) -> dict:
        return run(self.u.check(manual))

    def install(self, version: str, token: str):
        return run(self.u.install(version, token))

    def refused(self, match: str) -> None:
        with pytest.raises(UpdateError, match=match):
            self.check()
        assert self.u.status()["state"] == "error"
        assert self.appimage.read_bytes() == OLD

    def downloads(self) -> list[str]:
        d = self.tmp / "cache" / "updates"
        return sorted(p.name for p in d.iterdir()) if d.exists() else []


# ---------------------------------------------------------------- versions and format


def test_semver_precedence():
    order = ["0.9.9", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2"]
    order += ["1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0", "2.0.0", "10.0.0"]
    for a, b in itertools.pairwise(order):
        assert updates.is_newer(b, a), (a, b)
        assert not updates.is_newer(a, b), (a, b)
    assert not updates.is_newer("1.0.0+build.2", "1.0.0+build.1")
    for bad in ("1.0", "01.0.0", "1.0.0-", "v1.0.0", "1.0.0-01", "", "1" * 70 + ".0.0"):
        with pytest.raises(ValueError):
            updates.version_key(bad)


def test_roundtrip_and_canonical_payload(key):
    a = updates.Asset("linux-x86_64", "https://dl.example.invalid/F.AppImage", "ab" * 32, 10, "appimage")
    raw = updates.sign_envelope(updates.build_payload("2.0.0", {a.platform: a}, 1000, 2000), [key])
    env = json.loads(raw)
    body = updates.unb64u(env["payload"], "payload")
    assert body == updates.canonical(json.loads(body))
    # The signature covers the domain-separated context, NUL, then the payload.
    sig = updates.unb64u(env["signatures"][0]["sig"], "sig")
    from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey

    Ed25519PublicKey.from_public_bytes(key.public).verify(sig, b"openarcanum/release-manifest/v1\x00" + body)
    m = updates.verify_manifest(raw, [key.public], now=1500)
    assert (m.version, m.assets["linux-x86_64"].size, m.signed_by) == ("2.0.0", 10, keys.fingerprint(key.public))


def _env(raw: bytes) -> dict:
    return json.loads(raw)


def _resign(env: dict, payload: dict, key: keys.SigningKey) -> bytes:
    body = json.dumps(payload).encode()
    env = dict(env, payload=updates.b64u(body))
    env["signatures"] = [{"key": updates.b64u(key.public), "sig": updates.b64u(key.sign(updates.CTX, body))}]
    return json.dumps(env).encode()


def _good(key, **kw) -> bytes:
    a = updates.Asset("linux-x86_64", "https://dl.example.invalid/F.AppImage", "ab" * 32, 10, "appimage")
    return updates.sign_envelope(updates.build_payload(kw.pop("version", "2.0.0"), {a.platform: a}, 1000, 2000), [key])


def test_manifest_refusals(key):
    good = _good(key)
    other = keys.SigningKey.generate()
    verify = lambda raw, trusted=(key.public,), now=1500: updates.verify_manifest(raw, list(trusted), now=now)  # noqa: E731

    # Missing signature, empty signature list, unknown key, bad signature.
    env = _env(good)
    with pytest.raises(UpdateError, match="isn't signed"):
        verify(json.dumps({"payload": env["payload"]}).encode())
    with pytest.raises(UpdateError, match="isn't signed"):
        verify(json.dumps(dict(env, signatures=[])).encode())
    with pytest.raises(UpdateError, match="doesn't trust"):
        verify(_good(other))
    tampered = json.loads(updates.unb64u(env["payload"], "p"))
    tampered["assets"]["linux-x86_64"]["url"] = "https://evil.example.invalid/x"
    forged = dict(env, payload=updates.b64u(updates.canonical(tampered)))
    with pytest.raises(UpdateError, match="isn't genuine"):
        verify(json.dumps(forged).encode())
    # A signature made without the domain-separation context doesn't verify.
    body = updates.unb64u(env["payload"], "p")
    from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

    raw_sig = Ed25519PrivateKey.from_private_bytes(key.seed).sign(body)
    nocontext = dict(env, signatures=[{"key": updates.b64u(key.public), "sig": updates.b64u(raw_sig)}])
    with pytest.raises(UpdateError, match="isn't genuine"):
        verify(json.dumps(nocontext).encode())
    # Malformed envelopes.
    for raw in (b"not json", b"[]", json.dumps(dict(env, payload="***")).encode(), b"{" * 70000):
        with pytest.raises(UpdateError):
            verify(raw)
    with pytest.raises(UpdateError, match="damaged signature"):
        verify(json.dumps(dict(env, signatures=["x"])).encode())
    with pytest.raises(UpdateError, match="can't check updates"):
        verify(good, trusted=())
    # Wrong product, expiry (freeze), bad times, malformed assets, http URLs.
    payload = json.loads(updates.unb64u(env["payload"], "p"))
    with pytest.raises(UpdateError, match="not OpenArcanum"):
        verify(_resign(env, dict(payload, product="Other"), key))
    with pytest.raises(UpdateError, match="expired"):
        verify(good, now=2000)
    with pytest.raises(UpdateError, match="expired"):
        verify(good, now=10**10)
    with pytest.raises(UpdateError, match="expected time format"):
        verify(_resign(env, dict(payload, expires_at="tomorrow"), key))
    with pytest.raises(UpdateError, match="before its release date"):
        verify(_resign(env, dict(payload, expires_at=payload["released_at"]), key))
    # Valid for longer than the documented 30 days: refused (a replayed old
    # manifest could hide newer releases for that long), even while current.
    released = updates.parse_timestamp(payload["released_at"], "t")
    for days, ok in ((30, True), (31, False), (366, False)):
        long = _resign(env, dict(payload, expires_at=updates.timestamp(released + days * 86400)), key)
        if ok:
            assert verify(long, now=released + 60).version == "2.0.0"
        else:
            with pytest.raises(UpdateError, match="more than 30 days"):
                verify(long, now=released + 60)
    with pytest.raises(ValueError):
        updates.build_payload("2.0.0", {}, 1000, 1000 + 31 * 86400)
    with pytest.raises(UpdateError, match="damaged version number"):
        verify(_resign(env, dict(payload, version="2.0"), key))
    asset = payload["assets"]["linux-x86_64"]
    bad_assets = {
        "kind": dict(asset, kind="dmg"),
        "checksum": dict(asset, sha256="AB" * 32),
        "size": dict(asset, size=0),
        "doesn't start with https": dict(asset, url="http://dl.example.invalid/F.AppImage"),
    }
    for match, a in bad_assets.items():
        with pytest.raises(UpdateError, match=match):
            verify(_resign(env, dict(payload, assets={"linux-x86_64": a}), key))
    with pytest.raises(UpdateError, match="doesn't start with https"):
        verify(_resign(env, dict(payload, assets={"linux-x86_64": dict(asset, url="http://127.0.0.1/x")}), key))
    # Platforms this version doesn't know are skipped.
    m = verify(_resign(env, dict(payload, assets={**payload["assets"], "linux-riscv64": {"what": 1}}), key))
    assert list(m.assets) == ["linux-x86_64"]


def test_key_rotation(key):
    new = keys.SigningKey.generate()
    a = updates.Asset("linux-x86_64", "https://dl.example.invalid/F.AppImage", "ab" * 32, 10, "appimage")
    both = updates.sign_envelope(updates.build_payload("2.0.0", {a.platform: a}, 1000, 2000), [key, new])
    # A client that trusts only the old key, only the new one, or both, accepts it.
    for trusted in ([key.public], [new.public], [key.public, new.public]):
        assert updates.verify_manifest(both, trusted, now=1500).version == "2.0.0"
    # After the old key is retired, a manifest signed by it alone is refused.
    with pytest.raises(UpdateError, match="doesn't trust"):
        updates.verify_manifest(_good(key), [new.public], now=1500)


def test_config_and_test_override(key):
    assert not UpdateConfig.load({}).configured  # this build: no keys yet
    assert updates.RELEASE_PUBLIC_KEYS is None
    assert updates.MANIFEST_URL == "https://github.com/hyperkubeorg/openarcanum/releases/latest/download/manifest.json"
    assert not UpdateConfig((key.public,), "https://releases.example.invalid/m.json").configured
    assert UpdateConfig((key.public,), "https://releases.example.org/m.json").configured
    pub = updates.b64u(key.public)
    env = {updates.TEST_KEYS_ENV: pub, updates.TEST_URL_ENV: "http://127.0.0.1:9/m.json"}
    cfg = UpdateConfig.load(env)
    assert cfg.configured and cfg.test_override and cfg.allow_loopback_http and cfg.keys == (key.public,)
    # The override can't point at another computer, and needs both variables.
    for bad in ({**env, updates.TEST_URL_ENV: "https://updates.example.org/m.json"}, {updates.TEST_KEYS_ENV: pub}):
        assert not UpdateConfig.load(bad).configured
    assert not UpdateConfig.load({**env, updates.TEST_KEYS_ENV: "not-a-key"}).configured


def test_settings_cannot_change_update_keys():
    """Only the automatic-check toggle is a setting; keys and URL aren't."""
    from openarcanum import settings

    names = {s.key for s in settings.SETTINGS}
    assert "update_check" in names
    assert not {n for n in names if "update" in n} - {"update_check"}
    assert settings.BY_KEY["update_check"].store == "prefs" and settings.BY_KEY["update_check"].default is True


# ---------------------------------------------------------------- the updater


def test_unconfigured_build_makes_no_request(tmp_path, server, key):
    asset = asset_for(server, "/F.AppImage", NEW)
    publish(server, [key], "2.0.0", [asset])
    # No keys (the built-in state of this repository), even with a reachable URL.
    rig = Rig(tmp_path, server, [], config=UpdateConfig((), server.url("/manifest.json"), True))
    st = rig.check()
    assert st["state"] == "unconfigured" and st["message"] == "Updates aren't set up for this version."
    run(asyncio.wait_for(rig.u.run(), 5))  # the scheduler returns at once
    assert server.requests == []
    # The default configuration of this build: keys None.
    rig = Rig(tmp_path / "b", server, [], config=UpdateConfig.load({}))
    assert rig.check()["state"] == "unconfigured"
    assert server.requests == []


def test_happy_path_needs_consent(tmp_path, server, key):
    asset = asset_for(server, "/F.AppImage", NEW)
    publish(server, [key], "2.0.0", [asset])
    rig = Rig(tmp_path, server, [key.public])
    st = rig.check(manual=False)
    assert st["state"] == "ready" and st["version"] == "2.0.0" and st["prompt"]
    token = st["token"]
    assert rig.downloads() == ["OpenArcanum-2.0.0-linux-x86_64.AppImage"]
    # Downloaded and verified, but nothing installed: that needs the user.
    assert rig.appimage.read_bytes() == OLD and rig.u.pending is None
    states = list(dict.fromkeys(e["state"] for e in rig.events))
    assert states == ["idle", "checking", "downloading", "ready"]
    assert rig.events[-2]["done"] == rig.events[-2]["total"] == len(NEW)
    # The consent checks: wrong version, missing or wrong token.
    for v, t in (("2.0.1", token), ("2.0.0", ""), ("2.0.0", "x" * len(token)), ("1.0.0", token)):
        with pytest.raises(UpdateError):
            rig.install(v, t)
        assert rig.appimage.read_bytes() == OLD and rig.u.pending is None
    plan = rig.install("2.0.0", token)
    assert rig.appimage.read_bytes() == NEW
    if os.name == "posix":
        assert os.stat(rig.appimage).st_mode & 0o777 == 0o755
    assert plan.command == [str(rig.appimage), "--home", str(tmp_path / "home")]
    assert rig.u.pending is plan and rig.u.status()["state"] == "installing"
    # No temp files left beside the AppImage, and the token is spent.
    assert sorted(p.name for p in rig.appimage.parent.iterdir()) == ["OpenArcanum.AppImage"]
    with pytest.raises(UpdateError):
        rig.install("2.0.0", token)


@pytest.mark.skipif(os.name != "posix", reason="the stand-in AppImage is a shell script")
def test_restart_command_starts_new_appimage(tmp_path, server, key):
    """The chosen restart command really starts the replaced file."""
    marker = tmp_path / "started.txt"
    new = fake_appimage(None, marker, "new version")
    asset = asset_for(server, "/F.AppImage", new)
    publish(server, [key], "2.0.0", [asset])
    rig = Rig(tmp_path, server, [key.public])
    st = rig.check()
    plan = rig.install("2.0.0", st["token"])
    updates.launch(plan)
    deadline = time.monotonic() + 10
    while not marker.exists() and time.monotonic() < deadline:
        time.sleep(0.05)
    assert marker.read_text().split("\n")[:3] == ["new version", "--home", str(tmp_path / "home")]


def test_no_install_without_consent_over_time(tmp_path, server, key):
    """Automatic checks find, download and verify, then wait for the user."""
    asset = asset_for(server, "/F.AppImage", NEW)
    publish(server, [key], "2.0.0", [asset])
    rig = Rig(tmp_path, server, [key.public])

    async def scenario():
        task = asyncio.ensure_future(rig.u.run())
        for _ in range(1200):  # up to 60 s on a busy machine
            if rig.u.status()["state"] == "ready":
                break
            await asyncio.sleep(0.05)
        await asyncio.sleep(0.5)
        task.cancel()

    run(scenario())
    assert rig.u.status()["state"] == "ready"
    assert rig.appimage.read_bytes() == OLD and rig.u.pending is None
    # "Later" hides the prompt until the next check; nothing installed.
    v = rig.u.status()["version"]
    assert not rig.u.later(v)["prompt"]
    assert rig.appimage.read_bytes() == OLD
    assert rig.check()["prompt"]
    # "Skip this version": automatic checks don't offer it again.
    st = rig.u.skip(v)
    assert st["state"] == "skipped" and st["skipped"] == "2.0.0"
    with pytest.raises(UpdateError):
        rig.install(v, "anything")
    assert rig.check(manual=False)["state"] == "skipped"
    # "Check now" (manual) still finds it.
    assert rig.check(manual=True)["state"] == "ready"
    assert rig.appimage.read_bytes() == OLD


def test_automatic_checks_off_makes_no_request(tmp_path, server, key):
    publish(server, [key], "2.0.0", [asset_for(server, "/F.AppImage", NEW)])
    rig = Rig(tmp_path, server, [key.public])
    rig.auto = False

    async def scenario():
        task = asyncio.ensure_future(rig.u.run())
        await asyncio.sleep(0.5)
        assert server.requests == []
        # Turning it on checks at once.
        rig.auto = True
        rig.u.wake()
        for _ in range(1200):
            if server.requests:
                break
            await asyncio.sleep(0.05)
        task.cancel()

    run(scenario())
    assert server.paths()[0] == "/manifest.json"


def test_refuses_downgrade_and_same_version(tmp_path, server, key):
    asset = asset_for(server, "/F.AppImage", NEW)
    publish(server, [key], "0.9.0", [asset])
    rig = Rig(tmp_path, server, [key.public])
    rig.refused("isn't newer than 1.0.0")
    publish(server, [key], "1.0.0-rc.1", [asset], released=time.time() - 30)
    rig.refused("isn't newer than")
    publish(server, [key], "1.0.0", [asset], released=time.time() - 20)
    assert rig.check()["state"] == "up_to_date"
    assert "/F.AppImage" not in server.paths()


def test_refuses_bad_untrusted_wrong_product_expired(tmp_path, server, key):
    asset = asset_for(server, "/F.AppImage", NEW)
    rig = Rig(tmp_path, server, [key.public])
    publish(server, [keys.SigningKey.generate()], "2.0.0", [asset])
    rig.refused("doesn't trust")
    raw = publish(server, [key], "2.0.0", [asset])
    env = json.loads(raw)
    env["signatures"][0]["sig"] = updates.b64u(bytes(64))
    server.files["/manifest.json"] = json.dumps(env).encode()
    rig.refused("isn't genuine")
    server.files["/manifest.json"] = json.dumps({"payload": env["payload"]}).encode()
    rig.refused("isn't signed")
    publish(server, [key], "2.0.0", [asset], product="Other")
    rig.refused("not OpenArcanum")
    publish(server, [key], "2.0.0", [asset], released=time.time() - 10 * 86400, expires_in=86400)
    rig.refused("expired")
    assert "/F.AppImage" not in server.paths()


def test_refuses_replayed_older_manifest(tmp_path, server, key):
    asset = asset_for(server, "/F.AppImage", NEW)
    old = publish(server, [key], "2.0.0", [asset], released=time.time() - 3600)
    publish(server, [key], "2.0.1", [asset_for(server, "/G.AppImage", NEW + b"1")], released=time.time() - 60)
    rig = Rig(tmp_path, server, [key.public])
    assert rig.check()["version"] == "2.0.1"
    server.files["/manifest.json"] = old  # still within its expiry, but older than one already seen
    rig.refused("older update information")


def test_release_time_floor_cant_run_ahead_of_the_clock(tmp_path, server, key):
    """A manifest dated far in the future is used, but the remembered
    release time (the floor later manifests must reach) is capped at a day
    ahead of this clock, so such a manifest can't refuse every later one."""
    asset = asset_for(server, "/F.AppImage", NEW)
    far = time.time() + 20 * 86400
    publish(server, [key], "2.0.0", [asset], released=far, expires_in=86400)
    rig = Rig(tmp_path, server, [key.public])
    assert rig.check()["version"] == "2.0.0"
    assert rig.u._saved()["released_at"] <= time.time() + updates.RELEASE_SKEW_SECONDS + 1
    publish(server, [key], "2.0.1", [asset_for(server, "/G.AppImage", NEW + b"1")], released=time.time() - 60)
    rig.refused("older update information")  # still older than the floor
    publish(server, [key], "2.0.1", [asset_for(server, "/G.AppImage", NEW + b"1")], released=time.time() + 2 * 86400)
    assert rig.check()["version"] == "2.0.1"


def test_refuses_mismatched_downloads(tmp_path, server, key):
    rig = Rig(tmp_path, server, [key.public])
    good = asset_for(server, "/F.AppImage", NEW)
    # The host sends more bytes than the manifest says: stopped at the limit.
    server.files["/F.AppImage"] = NEW + b"extra"
    publish(server, [key], "2.0.0", [good])
    rig.refused("larger than expected")
    assert rig.downloads() == []
    # Same size, different bytes.
    server.files["/F.AppImage"] = bytes(len(NEW))
    rig.refused("doesn't match the official release")
    assert rig.downloads() == []
    # Fewer bytes than stated: kept for resuming, not used.
    server.files["/F.AppImage"] = NEW[:1000]
    rig.refused("stopped early")
    assert rig.downloads() == ["OpenArcanum-2.0.0-linux-x86_64.AppImage.part"]
    # The right file: resumes from what it has (a Range request) and verifies.
    server.files["/F.AppImage"] = NEW
    server.requests.clear()
    assert rig.check()["state"] == "ready"
    assert ("/F.AppImage", "bytes=1000-") in server.requests


def test_refuses_http_urls_and_redirects(tmp_path, server, key):
    rig = Rig(tmp_path, server, [key.public], config=UpdateConfig((key.public,), "http://releases.example.org/m.json"))
    rig.refused("doesn't start with https")
    # In production (no loopback allowance) a manifest naming loopback http is refused too.
    asset = asset_for(server, "/F.AppImage", NEW)
    publish(server, [key], "2.0.0", [asset])
    with pytest.raises(UpdateError, match="doesn't start with https"):
        updates.verify_manifest(server.files["/manifest.json"], [key.public])
    # A redirect from the update host to plain http elsewhere is refused.
    rig = Rig(tmp_path / "r", server, [key.public])
    server.redirects["/F.AppImage"] = "http://downloads.example.org/F.AppImage"
    rig.refused("redirect address")
    # A redirect to https would be followed (checked by URL only here).
    updates.check_url("https://downloads.example.org/F.AppImage", False, "redirect")


def test_download_resumes_after_interruption(tmp_path, server, key):
    asset = asset_for(server, "/F.AppImage", NEW)
    publish(server, [key], "2.0.0", [asset])
    rig = Rig(tmp_path, server, [key.public])
    server.cut_after["/F.AppImage"] = 100_000
    rig.refused("interrupted|stopped early")
    part = tmp_path / "cache" / "updates" / "OpenArcanum-2.0.0-linux-x86_64.AppImage.part"
    have = part.stat().st_size
    assert 0 < have < len(NEW)
    server.requests.clear()
    assert rig.check()["state"] == "ready"
    assert ("/F.AppImage", f"bytes={have}-") in server.requests


def test_download_restarts_without_range_support(tmp_path, server, key):
    asset = asset_for(server, "/F.AppImage", NEW)
    publish(server, [key], "2.0.0", [asset])
    rig = Rig(tmp_path, server, [key.public])
    server.cut_after["/F.AppImage"] = 50_000
    rig.refused("interrupted|stopped early")
    server.ranges = False
    assert rig.check()["state"] == "ready"
    st = rig.u.status()
    rig.install("2.0.0", st["token"])
    assert rig.appimage.read_bytes() == NEW


def test_install_runs_off_the_loop_and_a_timeout_leaves_nothing_pending(tmp_path, server, key, monkeypatch):
    """The copy runs in a worker: the loop keeps serving everything else
    meanwhile. One that takes too long is abandoned and nothing is left to
    launch at the next quit (no install without a fresh consent)."""
    publish(server, [key], "2.0.0", [asset_for(server, "/F.AppImage", NEW)])
    rig = Rig(tmp_path, server, [key.public])
    real = updates.install_appimage

    def slow(*a, **kw):
        time.sleep(0.6)
        return real(*a, **kw)

    monkeypatch.setattr(updates, "install_appimage", slow)

    async def install_while_ticking(token: str):
        ticks = 0

        async def tick():
            nonlocal ticks
            while True:
                await asyncio.sleep(0.05)
                ticks += 1

        t = asyncio.ensure_future(tick())
        try:
            return await rig.u.install("2.0.0", token), ticks
        finally:
            t.cancel()

    st = rig.check()
    plan, ticks = run(install_while_ticking(st["token"]))
    assert ticks >= 5 and rig.u.pending is plan and rig.appimage.read_bytes() == NEW

    rig = Rig(tmp_path / "again", server, [key.public])
    st = rig.check()
    monkeypatch.setattr(updates, "INSTALL_TIMEOUT", 0.1)
    with pytest.raises(UpdateError, match="took too long"):
        rig.install("2.0.0", st["token"])
    assert rig.u.pending is None and rig.u.status()["state"] == "error"
    with pytest.raises(UpdateError):  # the consent was spent
        rig.install("2.0.0", st["token"])


def test_install_refuses_file_changed_after_verification(tmp_path, server, key):
    asset = asset_for(server, "/F.AppImage", NEW)
    publish(server, [key], "2.0.0", [asset])
    rig = Rig(tmp_path, server, [key.public])
    st = rig.check()
    cached = tmp_path / "cache" / "updates" / "OpenArcanum-2.0.0-linux-x86_64.AppImage"
    cached.write_bytes(b"\x00" + NEW[1:])
    with pytest.raises(UpdateError, match="changed after it was checked"):
        rig.install("2.0.0", st["token"])
    assert rig.appimage.read_bytes() == OLD and rig.u.pending is None
    assert sorted(p.name for p in rig.appimage.parent.iterdir()) == ["OpenArcanum.AppImage"]


def test_copy_that_cannot_update_itself(tmp_path, server, key):
    publish(server, [key], "2.0.0", [asset_for(server, "/F.AppImage", NEW)])
    rig = Rig(tmp_path, server, [key.public], target=(None, "This copy isn't running from an AppImage."))
    st = rig.check()
    assert st["state"] == "available" and "isn't running from an AppImage" in st["message"]
    assert "/F.AppImage" not in server.paths()
    with pytest.raises(UpdateError, match="No verified update"):
        rig.install("2.0.0", "x")


def test_no_asset_for_this_platform(tmp_path, server, key):
    publish(server, [key], "2.0.0", [asset_for(server, "/F.dmg", NEW, "macos-universal")])
    Rig(tmp_path, server, [key.public]).refused("no download for this system")


def test_detect_target_on_linux(tmp_path):
    if not sys.platform.startswith("linux"):
        pytest.skip("Linux detection")
    f = tmp_path / "F.AppImage"
    f.write_bytes(b"x")
    appdir = tmp_path / "mnt"
    (appdir / "usr" / "bin").mkdir(parents=True)
    exe = str(appdir / "usr" / "bin" / "OpenArcanum")
    ours = {"APPIMAGE": str(f), "APPDIR": str(appdir)}
    assert updates.detect_target(ours, frozen=True, executable=exe)[0] == InstallTarget("appimage", f)
    for env in ({}, {"APPIMAGE": "relative/F.AppImage"}, {"APPIMAGE": str(tmp_path / "missing")}):
        target, reason = updates.detect_target(env, frozen=True, executable=exe)
        assert target is None and "AppImage" in reason
    # Inherited from a parent program that is an AppImage (an editor's
    # terminal): not this copy's, so never overwritten.
    for env, frozen, executable in (
        ({"APPIMAGE": str(f)}, True, exe),  # no APPDIR
        (ours, True, str(tmp_path / "elsewhere" / "OpenArcanum")),  # we don't run from that mount
        ({**ours, "APPDIR": "relative"}, True, exe),
        (ours, False, exe),  # `python -m openarcanum`, the onedir bundle
    ):
        target, reason = updates.detect_target(env, frozen=frozen, executable=executable)
        assert target is None and "isn't running from an AppImage" in reason, env
    # The test update source (tests run from source) waives only "frozen".
    assert updates.detect_target(ours, frozen=False, executable=exe, test_source=True)[0] is not None
    assert updates.detect_target({"APPIMAGE": str(f)}, frozen=False, executable=exe, test_source=True)[0] is None
    if os.geteuid() != 0:
        tmp_path.chmod(0o500)
        try:
            target, reason = updates.detect_target(ours, frozen=True, executable=exe)
            assert target is None and "isn't writable" in reason
        finally:
            tmp_path.chmod(0o700)


def test_checks_every_24_hours(tmp_path, server, key):
    publish(server, [key], "1.0.0", [asset_for(server, "/F.AppImage", NEW)])
    now = [time.time()]
    rig = Rig(tmp_path, server, [key.public], clock=lambda: now[0])

    async def until(n: int) -> None:
        for _ in range(1200):
            if server.paths().count("/manifest.json") >= n:
                return
            await asyncio.sleep(0.05)
        raise AssertionError(f"expected {n} checks, saw {server.paths()}")

    async def scenario():
        task = asyncio.ensure_future(rig.u.run())
        await until(1)  # at startup
        now[0] += updates.CHECK_INTERVAL - 60
        rig.u.wake()
        await asyncio.sleep(0.3)
        assert server.paths().count("/manifest.json") == 1  # not yet
        now[0] += 120
        rig.u.wake()
        await until(2)  # 24 hours later
        task.cancel()

    run(scenario())
    assert rig.u.status()["state"] == "up_to_date"


# ---------------------------------------------------------------- Windows and macOS installers


def tampered(data: bytes) -> bytes:
    """The same size with the first byte flipped (a fixed byte could equal
    the original: a zlib stream, as in a UDZO dmg, starts with "x")."""
    return bytes([data[0] ^ 1]) + data[1:]


def _asset(data: bytes, platform: str) -> updates.Asset:
    return updates.Asset(
        platform,
        "https://x.example.org/a",
        hashlib.sha256(data).hexdigest(),
        len(data),
        updates.PLATFORM_KINDS[platform],
    )


def test_windows_installer_plan(tmp_path):
    """Copies the verified installer to a private folder and runs it with
    Inno Setup's unattended flags (the copy step runs everywhere)."""
    data = Path(sys.executable).read_bytes()
    src = tmp_path / "setup.exe"
    src.write_bytes(data)
    plan = updates.install_windows(src, _asset(data, "windows-x86_64"), "2.0.0")
    exe = Path(plan.command[0])
    try:
        assert exe.read_bytes() == data and exe.name == "OpenArcanum-Setup-2.0.0.exe"
        assert plan.command[1:] == ["/SILENT", "/SUPPRESSMSGBOXES", "/NORESTART"]
    finally:
        import shutil

        shutil.rmtree(exe.parent, ignore_errors=True)
    src.write_bytes(tampered(data))
    with pytest.raises(UpdateError, match="changed after"):
        updates.install_windows(src, _asset(data, "windows-x86_64"), "2.0.0")


@pytest.mark.skipif(sys.platform != "win32", reason="runs a Windows program")
def test_windows_installer_runs(tmp_path):
    """On Windows: the plan's command really starts the installer (here a
    copy of `where.exe`, which accepts and ignores the flags' absence)."""
    where = Path(os.environ.get("SYSTEMROOT", r"C:\Windows")) / "System32" / "where.exe"
    data = where.read_bytes()
    src = tmp_path / "setup.exe"
    src.write_bytes(data)
    plan = updates.install_windows(src, _asset(data, "windows-x86_64"), "2.0.0")
    r = subprocess.run([plan.command[0], "/?"], capture_output=True, timeout=30)
    assert r.returncode == 0
    target, reason = updates.detect_target()
    assert target is None and "installer" in reason  # pytest isn't an installed copy


def test_swap_bundle(tmp_path):
    bundle = tmp_path / "OpenArcanum.app"
    (bundle / "Contents").mkdir(parents=True)
    (bundle / "Contents" / "v").write_text("old")
    staged = tmp_path / ".OpenArcanum.app.update-1"
    (staged / "Contents").mkdir(parents=True)
    (staged / "Contents" / "v").write_text("new")
    updates.swap_bundle(staged, bundle)
    assert (bundle / "Contents" / "v").read_text() == "new"
    assert sorted(p.name for p in tmp_path.iterdir()) == ["OpenArcanum.app"]
    # If moving the new one in fails, the old one goes back.
    with pytest.raises(OSError):
        updates.swap_bundle(tmp_path / "missing", bundle)
    assert (bundle / "Contents" / "v").read_text() == "new"
    assert sorted(p.name for p in tmp_path.iterdir()) == ["OpenArcanum.app"]


@pytest.mark.skipif(sys.platform != "darwin", reason="needs hdiutil")
def test_macos_dmg_install(tmp_path):
    """On macOS: build a real .dmg holding OpenArcanum.app, then install it over
    a running-style bundle in tmp."""
    src_dir = tmp_path / "src"
    (src_dir / "OpenArcanum.app" / "Contents").mkdir(parents=True)
    (src_dir / "OpenArcanum.app" / "Contents" / "v").write_text("new")
    dmg = tmp_path / "OpenArcanum.dmg"
    subprocess.run(
        ["hdiutil", "create", "-srcfolder", str(src_dir), "-volname", "OpenArcanum", "-format", "UDZO", str(dmg)],
        check=True,
        capture_output=True,
    )
    apps = tmp_path / "Applications"
    (apps / "OpenArcanum.app" / "Contents").mkdir(parents=True)
    (apps / "OpenArcanum.app" / "Contents" / "v").write_text("old")
    data = dmg.read_bytes()
    plan = updates.install_macos(dmg, _asset(data, "macos-universal"), apps / "OpenArcanum.app", ["--home", "/h"])
    assert (apps / "OpenArcanum.app" / "Contents" / "v").read_text() == "new"
    assert sorted(p.name for p in apps.iterdir()) == ["OpenArcanum.app"]
    assert plan.command == ["/usr/bin/open", "-n", str(apps / "OpenArcanum.app"), "--args", "--home", "/h"]
    # A dmg changed after verification is refused before mounting.
    dmg.write_bytes(tampered(data))
    assert hashlib.sha256(dmg.read_bytes()).hexdigest() != _asset(data, "macos-universal").sha256, "not tampered"
    with pytest.raises(UpdateError, match="changed after"):
        updates.install_macos(dmg, _asset(data, "macos-universal"), apps / "OpenArcanum.app", [])


def test_macos_install_refuses_wrong_bundle(tmp_path):
    """The .dmg must hold exactly the app being replaced (hdiutil faked, so
    this runs everywhere; test_macos_dmg_install runs the real tools)."""
    data = b"dmg bytes"
    src = tmp_path / "u.dmg"
    src.write_bytes(data)
    bundle = tmp_path / "Applications" / "OpenArcanum.app"
    bundle.mkdir(parents=True)
    calls = []

    def fake_run(cmd, **kw):
        calls.append(cmd[:2])
        if cmd[1] == "attach":
            (Path(cmd[cmd.index("-mountpoint") + 1]) / "Other.app").mkdir()
        return subprocess.CompletedProcess(cmd, 0)

    with pytest.raises(UpdateError, match=r"doesn't hold OpenArcanum\.app"):
        updates.install_macos(src, _asset(data, "macos-universal"), bundle, [], run=fake_run)
    assert ["/usr/bin/hdiutil", "detach"] in calls  # always unmounted
    assert sorted(p.name for p in bundle.parent.iterdir()) == ["OpenArcanum.app"]


# ---------------------------------------------------------------- the release tool


def tool(*args: str, cwd: Path | None = None) -> subprocess.CompletedProcess:
    return subprocess.run([sys.executable, str(SCRIPT), *args], capture_output=True, text=True, cwd=cwd, timeout=60)


def test_release_tool_keygen_sign_verify(tmp_path):
    keyfile = tmp_path / "release.pem"
    r = tool("keygen", "--out", str(keyfile))
    assert r.returncode == 0, r.stderr
    pub = r.stdout.strip()
    assert len(updates.public_key(pub)) == 32
    assert keyfile.read_bytes().startswith(b"-----BEGIN PRIVATE KEY-----")
    if os.name == "posix":
        assert keyfile.stat().st_mode & 0o777 == 0o600
    assert tool("keygen", "--out", str(keyfile)).returncode != 0  # never overwrites a key
    linux, win = tmp_path / "F.AppImage", tmp_path / "F-Setup.exe"
    linux.write_bytes(NEW)
    win.write_bytes(b"MZ installer")
    out = tmp_path / "manifest.json"
    r = tool(
        "sign", "--key", str(keyfile), "--version", "2.1.0",
        "--asset", f"linux-x86_64={linux}=https://dl.example.org/F.AppImage?x=1",
        "--asset", f"windows-x86_64={win}=https://dl.example.org/F-Setup.exe",
        "--expires-days", "30", "--out", str(out),
    )  # fmt: skip
    assert r.returncode == 0, r.stderr
    m = updates.verify_manifest(out.read_bytes(), [updates.public_key(pub)])
    assert m.version == "2.1.0" and m.expires_at - m.released_at == 30 * 86400
    a = m.assets["linux-x86_64"]
    assert (a.size, a.sha256, a.url, a.kind) == (
        len(NEW),
        hashlib.sha256(NEW).hexdigest(),
        "https://dl.example.org/F.AppImage?x=1",
        "appimage",
    )
    assert m.assets["windows-x86_64"].kind == "exe"
    r = tool("verify", f"--pubkey={pub}", str(out))
    assert r.returncode == 0 and "OK: OpenArcanum 2.1.0" in r.stdout
    # Another key, a tampered file, bad arguments.
    other = tmp_path / "other.pem"
    other_pub = tool("keygen", "--out", str(other)).stdout.strip()
    r = tool("verify", f"--pubkey={other_pub}", str(out))
    assert r.returncode == 1 and "doesn't trust" in r.stderr
    (tmp_path / "pub.txt").write_text(other_pub + "\n")
    assert tool("verify", "--pubkey", str(tmp_path / "pub.txt"), f"--pubkey={pub}", str(out)).returncode == 0
    env = json.loads(out.read_bytes())
    payload = json.loads(base64.urlsafe_b64decode(env["payload"] + "=" * (-len(env["payload"]) % 4)))
    payload["version"] = "9.0.0"
    env["payload"] = updates.b64u(updates.canonical(payload))
    out.write_text(json.dumps(env))
    assert tool("verify", f"--pubkey={pub}", str(out)).returncode == 1
    for bad in (
        ["--asset", f"linux-x86_64={linux}=http://dl.example.org/F"],
        ["--asset", f"linux-arm={linux}=https://dl.example.org/F"],
        ["--asset", f"linux-x86_64={tmp_path / 'missing'}=https://dl.example.org/F"],
        ["--asset", "nonsense"],
    ):
        r = tool("sign", "--key", str(keyfile), "--version", "2.1.0", *bad, "--expires-days", "30", "--out", str(out))
        assert r.returncode != 0
    r = tool("sign", "--key", str(keyfile), "--version", "2.1", "--asset", f"linux-x86_64={linux}=https://d.example.org/F",
             "--expires-days", "30", "--out", str(out))  # fmt: skip
    assert r.returncode != 0
    for days in ("0", "31", "366"):
        asset = f"linux-x86_64={linux}=https://d.example.org/F"
        r = tool(
            "sign",
            "--key",
            str(keyfile),
            "--version",
            "2.1.0",
            "--asset",
            asset,
            "--expires-days",
            days,
            "--out",
            str(out),
        )
        assert r.returncode != 0 and "between 1 and 30" in r.stderr, days
    # Two keys while rotating: both signatures are in the manifest.
    asset = f"linux-x86_64={linux}=https://d.example.org/F"
    r = tool("sign", "--key", str(keyfile), "--key", str(other), "--version", "2.1.0",
             "--asset", asset, "--expires-days", "7", "--out", str(out))  # fmt: skip
    assert r.returncode == 0
    assert len(json.loads(out.read_bytes())["signatures"]) == 2
    assert updates.verify_manifest(out.read_bytes(), [updates.public_key(other_pub)]).version == "2.1.0"


def test_release_tool_verifies_against_the_built_in_keys(tmp_path, monkeypatch, capsys):
    """`verify --builtin` checks a manifest as installed copies will: with
    the keys in RELEASE_PUBLIC_KEYS (release.yml runs it after signing)."""
    import importlib.util

    spec = importlib.util.spec_from_file_location("release_manifest", SCRIPT)
    tool_mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(tool_mod)
    signer, other = keys.SigningKey.generate(), keys.SigningKey.generate()
    a = updates.Asset("linux-x86_64", "https://dl.example.org/F.AppImage", "ab" * 32, 10, "appimage")
    now = time.time()
    out = tmp_path / "manifest.json"
    out.write_bytes(
        updates.sign_envelope(updates.build_payload("2.0.0", {a.platform: a}, now - 60, now + 86400), [signer])
    )
    monkeypatch.setattr(updates, "RELEASE_PUBLIC_KEYS", None)
    with pytest.raises(SystemExit, match="no built-in release keys"):
        tool_mod.main(["verify", "--builtin", str(out)])
    monkeypatch.setattr(updates, "RELEASE_PUBLIC_KEYS", (updates.b64u(other.public),))
    assert tool_mod.main(["verify", "--builtin", str(out)]) == 1  # signed by a key the client doesn't know
    monkeypatch.setattr(updates, "RELEASE_PUBLIC_KEYS", (updates.b64u(other.public), updates.b64u(signer.public)))
    assert tool_mod.main(["verify", "--builtin", str(out)]) == 0
    assert "OK: OpenArcanum 2.0.0" in capsys.readouterr().out
    with pytest.raises(SystemExit, match="--pubkey or --builtin"):
        tool_mod.main(["verify", str(out)])
