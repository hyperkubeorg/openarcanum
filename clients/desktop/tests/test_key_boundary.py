"""Keys never cross into the webview (docs/threat-model.md):
identity, vault, DM, channel and file keys stay in Python. Everything the UI
can see is either an event the core emits or the return value of a call the
UI makes; this runs a realistic session and checks both. The one key that
does enter JavaScript is the per-call media key (SFrame runs in the page):
the call tests check that it's the only one, in the "call" sessions only,
both from the events the core emits and from what a real page received."""

import asyncio
import base64
import json

import pytest
from conftest import PASS, Events, network, run, wait_until
from test_e2e_community import general
from test_e2e_dm import community_of, key_of, poll

from openarcanum import keys


def encodings(secret: bytes) -> set[str]:
    out = {
        secret.hex(),
        keys.encode(secret),
        base64.b64encode(secret).decode(),
        base64.b64encode(secret).decode().rstrip("="),
    }
    return {e for e in out if len(e) >= 16}


def secrets_of(core) -> set[bytes]:
    v = core.vault.data
    out: set[bytes] = set()
    for ident in v["identities"].values():
        if ident.get("seed"):
            out.add(keys.decode(ident["seed"]))
    for k in ("state_key", "cache_key", "dm_history_key"):
        if v.get(k):
            out.add(keys.decode(v[k]))
    for k in ("seal", "pickle"):
        out.add(keys.decode(v["olm"][k]))
    for rec in v["communities"].values():
        if rec.get("device", {}).get("static"):
            out.add(keys.decode(rec["device"]["static"]))
    for conn in core.conns.values():
        if conn.state is not None:
            for ch in conn.state.channels:
                if ch.key:
                    out.add(bytes(ch.key))
    for (_cid, _fid), k in core._file_keys.items():
        out.add(keys.decode(k))
    for cache in core.caches.values():
        for msgs in cache.messages.values():
            for m in msgs:
                for f in [*(m.get("files") or []), (m.get("preview") or {}).get("image") or {}]:
                    if f.get("key"):
                        out.add(keys.decode(f["key"]))
    for hist in core.dms.store.data.values():
        for m in hist:
            for f in m.get("files", []):
                out.add(base64.b64decode(f["key"]))
                if f.get("token"):
                    out.add(base64.b64decode(f["token"]))
    return out


def test_no_key_material_reaches_the_ui(tmp_path):
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            cid, [(olive, oev), (alice, aev)] = await community_of(tmp_path, meta, node, "Alice")
            gen = await general(olive, cid)
            await olive.send(cid, gen, "hello channel")
            cf = tmp_path / "community-file.txt"
            cf.write_bytes(b"community attachment")
            await olive.upload(cid, gen, str(cf), "a community file")
            gid = (await alice.dms.open_with_member(cid, await key_of(alice, cid, "Olive")))["id"]
            await poll(olive)
            await olive.dms.accept(gid)
            f = tmp_path / "doc.txt"
            f.write_bytes(b"attachment body")
            await olive.dms.send(gid, "a file", files=[str(f)])

            async def got():
                await poll(alice)
                return any(m.get("files") for m in alice.dms.history(gid))

            await wait_until(got, 20, 0.25)
            seen = []
            for core, ev in ((olive, oev), (alice, aev)):
                seen.append(ev.items)
                seen.append(
                    [
                        core.profile_status(),
                        core.list_communities(),
                        core.community_state(cid),
                        await core.members(cid),
                        await core.history(cid, gen),
                        core.get_settings(),
                        core.dms.list(),
                        core.dms.info(gid),
                        core.dms.history(gid),
                        core.dms.contacts(),
                        await core.devices.list(),
                        core.dms.safety(next(m["id"] for m in core.dms.info(gid)["members"] if not m["me"]), gid),
                    ]
                )
            ui = json.dumps(seen, default=str)
            for core in (olive, alice):
                for secret in secrets_of(core):
                    for enc in encodings(secret):
                        assert enc not in ui, f"key material reached the UI ({len(secret)}-byte secret)"
            for c in (olive, alice):
                await c.shutdown()

        run(main(), 120)


def call_secrets(core) -> set[bytes]:
    """What a call hands this client besides the per-call key: the relay
    grant and control (signed admission tokens) and the call nonce."""
    from openarcanum.wire import call_pb2

    call = core.voice.call
    if call is None:
        return set()
    s = call.session
    out = {s.grant.grant, s.grant.signature, s.control.signature}
    out.add(call_pb2.CallGrant.FromString(s.grant.grant).call_nonce)
    return {x for x in out if x}


def page_keys(events: list) -> list[str]:
    """Every call key the page was given (in "call" events' sessions)."""
    return [p["session"]["key"] for name, p in events if name == "call" and "session" in p]


def test_only_the_call_key_reaches_the_page_in_a_call(tmp_path):
    """A call: two people join a voice channel, one leaves (the key rotates
    on every join and leave). What the page is sent ("call" events and the
    UI's voice state) carries the current call key and nothing else secret:
    no grant, control, nonce, identity, vault, channel or DM key."""
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            cid, [(olive, oev), (alice, aev)] = await community_of(tmp_path, meta, node, "Alice")
            await general(olive, cid)
            vc = (await olive.admin(cid, "create_channel", {"name": "talk", "kind": "voice"}))["id"]
            await wait_until(lambda: any(c["id"] == vc for c in alice.community_state(cid)["channels"]), 15)
            seen_keys: set[bytes] = set()
            secrets: set[bytes] = set()
            ui = []
            await olive.voice.join(cid, vc)
            seen_keys.add(olive.voice.call.session.call_key)
            secrets |= call_secrets(olive)
            await alice.voice.join(cid, vc)
            secrets |= call_secrets(alice)
            seen_keys.add(alice.voice.call.session.call_key)
            # Olive is told about the new key when Alice joins.
            await wait_until(lambda: olive.voice.call.session.call_key == alice.voice.call.session.call_key, 15)
            secrets |= call_secrets(olive)
            for core in (olive, alice):
                ui.append([core.voice.state(), core.voice.rooms(cid)])
            await alice.voice.leave()
            await wait_until(lambda: olive.voice.call.session.call_key not in seen_keys, 15)
            seen_keys.add(olive.voice.call.session.call_key)
            secrets |= call_secrets(olive)
            ui.append([olive.voice.state(), olive.voice.rooms(cid)])
            await olive.voice.leave()

            events = [*oev.items, *aev.items]
            # The page got a key for each epoch it was in, and only call keys.
            given = {base64.b64decode(k) for k in page_keys(events)}
            assert given and given <= seen_keys, "a key other than a call key was given to the page"
            assert len(given) >= 2, "the key never rotated"
            dump = json.dumps([events, ui], default=str)
            for secret in secrets | secrets_of(olive) | secrets_of(alice):
                for enc in encodings(secret):
                    assert enc not in dump, f"non-call secret reached the page ({len(secret)} bytes)"
            # Outside "call" events (which carry the key), no call key at all.
            rest = json.dumps([[e for e in events if e[0] != "call"], ui], default=str)
            for k in seen_keys:
                for enc in encodings(k):
                    assert enc not in rest, "a call key left the call events"
            for c in (olive, alice):
                await c.shutdown()

        run(main(), 120)


# Installed in the page: records every event Python pushes and every result
# of a bridge call, as the page receives them.
RECORD = """
(() => {
  window.__seen = [];
  const keep = (x) => { try { window.__seen.push(JSON.stringify(x)); } catch (e) { window.__seen.push(String(x)); } };
  const deliver = window.__openarcanumEvent;
  window.__openarcanumEvent = (m) => { keep(m); return deliver(m); };
  const a = window.pywebview.api;
  for (const k of Object.keys(a)) {
    const f = a[k];
    if (typeof f !== 'function') continue;
    a[k] = async (...args) => { const r = await f(...args); keep(r); return r; };
  }
  return true;
})()
"""


@pytest.fixture(scope="module")
def tone(tmp_path_factory):
    """The fake microphone's input (tone bursts), as in test_ui_voice."""
    import sys
    from pathlib import Path

    sys.path.insert(0, str(Path(__file__).parent / "webengine"))
    import callharness

    path = tmp_path_factory.mktemp("media") / "tone.wav"
    callharness.tone_bursts(path)
    return path


@pytest.mark.gui
def test_only_the_call_key_reaches_the_real_page(tmp_path, tone):
    """The same in real windows: both people click into a voice call and one
    leaves. Everything the page received (pushed events and every bridge
    result, recorded in the page) is checked afterwards against each
    profile's keys: the only key in it is the per-call key, only in the
    "call" messages' sessions."""
    from test_e2e_community import online
    from test_ui_voice import create_voice_channel, join_voice, media_env
    from uidriver import App
    from uiflows import claim, create_profile, join

    from openarcanum.core import Core
    from openarcanum.paths import Paths

    with network(tmp_path) as (_meta, _relay, community):
        node = community()

        async def main():
            env = media_env(tone)
            olive = await App(tmp_path / "olive", env=env, name="olive").start()
            alice = await App(tmp_path / "alice", env=env, name="alice").start()
            seen = {}
            try:
                for app in (olive, alice):
                    assert await app.js(RECORD)
                    await create_profile(app)
                invite = await claim(olive, node.claim_code, "Key Town", "Olive")
                await join(alice, invite, "Alice")
                await create_voice_channel(olive, "Lounge")
                await alice.wait_text(".chan.voice .cname", "Lounge")
                await join_voice(olive, "Lounge")
                await join_voice(alice, "Lounge")
                await olive.wait_text(".voice-user .spacer", "Alice")
                await alice.click('button[aria-label="Leave call"]')
                await alice.wait_gone(".voice-bar")
                for _ in range(80):
                    if not any(
                        "Alice" in t
                        for t in await olive.texts(".voice-bar ~ * .voice-user .spacer, .voice-user .spacer")
                    ):
                        break
                    await asyncio.sleep(0.25)
                await olive.click('button[aria-label="Leave call"]')
                await olive.wait_gone(".voice-bar")
                for app in (olive, alice):
                    seen[app.name] = await app.js("window.__seen")
                    app.assert_no_errors()
            finally:
                await olive.stop()
                await alice.stop()

            keys_given: set[str] = set()
            for name, items in seen.items():
                msgs = [json.loads(x) for x in items if x.startswith("{")]
                calls = [m["data"] for m in msgs if isinstance(m, dict) and m.get("event") == "call"]
                sessions = [c["session"] for c in calls if "session" in c]
                assert sessions, f"{name}'s page never got a call session"
                assert any(x.startswith('{"ok":true') for x in items), f"{name}: no bridge results were recorded"
                keys_given |= {s["key"] for s in sessions}
                for s in sessions:
                    assert set(s) == {
                        "generation",
                        "participant",
                        "slot",
                        "epoch",
                        "key",
                        "participants",
                        "perms",
                        "serverMuted",
                        "serverDeafened",
                        "mode",
                        "vadThreshold",
                        "pttKey",
                        "mic",
                        "speaker",
                        "camera",
                        "volumes",
                        # Member keys muted on this device, an output level
                        # and a switch: public ids and preferences, no keys.
                        "localMutes",
                        "outputVolume",
                        "screenAudio",
                        # Video quality choices: preferences.
                        "cameraQuality",
                        "screenQuality",
                        # A DM call's per-join media keys (call keys, as
                        # "key"); none in a community call such as this.
                        "joinKeys",
                    }, sorted(s)
                    assert s["joinKeys"] is None
                    assert isinstance(s["cameraQuality"], str) and isinstance(s["screenQuality"], str)
                    assert isinstance(s["outputVolume"], float | int) and isinstance(s["screenAudio"], bool)
                    assert all(isinstance(m, str) for m in s["localMutes"])
            assert len(keys_given) >= 2, "the call key never rotated"
            assert all(len(base64.b64decode(k)) == 32 for k in keys_given)
            # Each profile's own keys (identity, vault, Olm, device, channel
            # keys once it's back online) never reached its page.
            for name, items in seen.items():
                core = Core(Paths.under(tmp_path / name), emit=Events())
                await core.unlock(PASS)
                cid = next(iter(core.vault.data["communities"]))
                await online(core, cid)
                await wait_until(lambda core=core, cid=cid: core.conns[cid].state is not None, 15)
                dump = "\n".join(items)
                for secret in secrets_of(core):
                    for enc in encodings(secret):
                        assert enc not in dump, f"{name}: key material reached the page ({len(secret)} bytes)"
                # Outside the call sessions, no call key either.
                rest = "\n".join(x for x in items if '"event": "call"' not in x and '"event":"call"' not in x)
                for k in keys_given:
                    assert k not in rest, f"{name}: a call key reached the page outside a call session"
                await core.shutdown()

        run(main(), 300)
