"""Hypothesis fuzzing of the bytes a session reads from the relay (session.py).

Session.recv decrypts and reassembles relay frames; dial parses the server's
handshake message 2. Whatever arrives, they must either return the message a
reference model expects or raise SessionError, never another exception. The
Go side has the matching native fuzz targets in internal/session/fuzz_test.go.
"""

from __future__ import annotations

import asyncio
from unittest import mock

import pytest
from hypothesis import HealthCheck, given, settings
from hypothesis import strategies as st

from openarcanum import keys, session
from openarcanum.keys import StaticKey
from openarcanum.session import FLAG_MORE, Session, SessionError, _new, _peer_static, dial, sign_static

FUZZ = settings(max_examples=300, deadline=None, suppress_health_check=[HealthCheck.too_slow])


class Exhausted(Exception):
    """The fake relay stream has no more frames."""


class Frames:
    """A Transport that replays fixed frames and records what is sent."""

    def __init__(self, frames: list[bytes]):
        self.frames = list(frames)
        self.sent: list[bytes] = []
        self.closed: str | None = None

    async def send(self, data: bytes) -> None:
        self.sent.append(bytes(data))

    async def recv(self, timeout: float | None = None) -> bytes:
        if not self.frames:
            raise Exhausted
        return self.frames.pop(0)

    async def close(self, reason: str = "closed") -> None:
        self.closed = reason


def _established():
    """A completed XX handshake: (initiator, responder) NoiseConnections."""
    i = _new(StaticKey.generate(), initiator=True)
    r = _new(StaticKey.generate(), initiator=False)
    r.read_message(bytes(i.write_message(b"")))
    i.read_message(bytes(r.write_message(b"")))
    r.read_message(bytes(i.write_message(b"")))
    assert i.handshake_finished and r.handshake_finished
    return i, r


def _model(fragments: list[bytes], cap: int):
    """Expected outcome of one recv over these plaintext fragments.

    Returns ("message", bytes), ("error", closes_transport) or ("exhausted", None).
    """
    parts = b""
    for pt in fragments:
        if not pt:
            return "error", False
        if len(parts) + len(pt) - 1 > cap:
            return "error", True
        parts += pt[1:]
        if not pt[0] & FLAG_MORE:
            return "message", parts
    return "exhausted", None


fragment = st.one_of(
    st.just(b""),
    st.builds(lambda flag, body: bytes([flag]) + body, st.integers(0, 255), st.binary(max_size=80)),
)


@FUZZ
@given(fragments=st.lists(fragment, max_size=12), cap=st.integers(0, 400))
def test_recv_reassembles_authentic_fragments(fragments, cap):
    i, r = _established()
    transport = Frames([i.encrypt(pt) for pt in fragments])
    s = Session(transport, r, b"")
    kind, want = _model(fragments, cap)
    with mock.patch.object(session, "MAX_MESSAGE", cap):
        if kind == "message":
            assert asyncio.run(s.recv()) == want
            assert transport.closed is None
        elif kind == "error":
            with pytest.raises(SessionError):
                asyncio.run(s.recv())
            assert (transport.closed is not None) == want
        else:
            with pytest.raises(Exhausted):
                asyncio.run(s.recv())


@FUZZ
@given(frames=st.lists(st.binary(max_size=200), min_size=1, max_size=4))
def test_recv_rejects_unauthenticated_frames(frames):
    _, r = _established()
    transport = Frames(frames)
    s = Session(transport, r, b"")
    with pytest.raises(SessionError, match="decrypt failed"):
        asyncio.run(s.recv())
    assert transport.closed == "decrypt failed"


@FUZZ
@given(body=st.binary(max_size=60), index=st.integers(0, 10_000), bit=st.integers(0, 7))
def test_recv_rejects_modified_frames(body, index, bit):
    i, r = _established()
    ct = bytearray(i.encrypt(b"\x00" + body))
    ct[index % len(ct)] ^= 1 << bit
    transport = Frames([bytes(ct)])
    with pytest.raises(SessionError, match="decrypt failed"):
        asyncio.run(Session(transport, r, b"").recv())


@FUZZ
@given(m2=st.binary(max_size=300))
def test_dial_rejects_arbitrary_message_2(m2):
    ident = keys.SigningKey.generate()
    static = StaticKey.generate()
    transport = Frames([m2])
    with pytest.raises(SessionError):
        asyncio.run(dial(transport, static, ident.public, sign_static(ident, static.public), "fuzz", lambda si: None))
    # Only message 1 went out; nothing was sent after the bad message 2.
    assert len(transport.sent) == 1


def test_peer_static_survives_handshake_completion():
    """The responder's view of the peer key outlives the library's handshake state."""
    i, r = _established()
    assert _peer_static(r) == bytes(i.noise_protocol.keypairs["s"].public_bytes)
