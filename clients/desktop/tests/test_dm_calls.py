"""Calls in direct messages (dmcalls.py, docs/protocol.md §9.9): the grant a
participant signs for itself must be what a relay accepts (callauth.go)."""

from __future__ import annotations

import time

from openarcanum import dmcalls, keys
from openarcanum.keys import SigningKey
from openarcanum.wire import call_pb2 as cw


def test_call_id_commits_to_the_signing_key():
    seed, nonce = b"s" * 32, b"n" * 16
    pub = SigningKey(seed).public
    cid = dmcalls.call_id(pub, nonce)
    assert len(cid) == 16
    assert cid != dmcalls.call_id(SigningKey(b"t" * 32).public, nonce)
    assert cid != dmcalls.call_id(pub, b"m" * 16)


class FakeDms:
    def __init__(self):
        self.d = {"groups": {"g": {"me": keys.encode(b"m" * 32), "status": "active"}}}

    def _name_of(self, gid, member):
        return "Me"


def test_a_participants_own_grant_verifies_like_a_servers():
    calls = dmcalls.DmCalls.__new__(dmcalls.DmCalls)
    calls.core = type("C", (), {"dms": FakeDms()})()
    seed, nonce = b"s" * 32, b"n" * 16
    signer = SigningKey(seed)
    c = dmcalls.DmCall("g", dmcalls.call_id(signer.public, nonce), nonce, seed, b"k" * 32, "wss://r/ws", time.time())
    calls.calls = {"g": c}
    calls.mine = ("g", b"p" * 16, 3)
    c.parts[b"p" * 16] = dmcalls.Part(b"m" * 32, 3, time.time())
    vs = calls.session("g")
    g = cw.CallGrant.FromString(vs.grant.grant)
    # What callauth.VerifyGrant checks: the signature by server_key, the
    # call id committing to it, and an expiry within 10 minutes.
    assert keys.is_valid(g.server_key, keys.CTX_CALL_GRANT, vs.grant.grant, vs.grant.signature)
    assert g.call_id == dmcalls.call_id(g.server_key, g.call_nonce)
    assert time.time() * 1000 < g.expires_at <= (time.time() + 600) * 1000
    assert g.participant == b"p" * 16 and g.kid == 3 and g.audio and g.video and g.screen
    assert vs.call_key == b"k" * 32 and vs.slot == 3 and [p.slot for p in vs.participants] == [3]


def test_a_reconnect_is_a_new_join_with_its_own_key():
    """Reconnecting to the relay comes back as a new participant, so a new
    epoch and key (the page never sends twice under one key): everyone gets
    the old one's LEAVE and the new JOIN, with the same slot and state."""
    import asyncio

    from openarcanum.wire import dm_pb2 as dm

    sent = []

    class Dms(FakeDms):
        async def send_event(self, gid, ev, live_only=False):
            sent.append(ev.call)

    calls = dmcalls.DmCalls.__new__(dmcalls.DmCalls)
    voice = type("V", (), {"dm_update": lambda self, gid: None})()
    calls.core = type("C", (), {"dms": Dms(), "emit": lambda *a: None, "spawn": lambda *a: None, "voice": voice})()
    calls._me = lambda gid: b"m" * 32
    calls.state = dmcalls.cp.VoiceState(self_mute=True)
    seed, nonce = b"s" * 32, b"n" * 16
    c = dmcalls.DmCall(
        "g", dmcalls.call_id(SigningKey(seed).public, nonce), nonce, seed, b"k" * 32, "wss://r/ws", time.time()
    )
    calls.calls = {"g": c}
    old = b"p" * 16
    calls.mine = ("g", old, 3)
    c.parts[old] = dmcalls.Part(b"m" * 32, 3, time.time(), self_mute=True)
    c.parts[b"q" * 16] = dmcalls.Part(b"o" * 32, 1, time.time())

    vs = asyncio.run(calls.rejoin("g"))
    new = vs.participant
    assert new != old and calls.mine == ("g", new, 3) and vs.slot == 3
    assert set(c.parts) == {new, b"q" * 16} and c.parts[new].self_mute
    assert dmcalls.join_key(c.key, new) != dmcalls.join_key(c.key, old)
    assert [(e.kind, e.participant) for e in sent] == [
        (dmcalls.K.DM_CALL_KIND_LEAVE, old),
        (dmcalls.K.DM_CALL_KIND_JOIN, new),
    ]
    assert sent[1].slot == 3 and sent[1].self_mute
    assert isinstance(sent[0], dm.DmCall)
