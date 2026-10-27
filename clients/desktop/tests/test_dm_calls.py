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
