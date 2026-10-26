"""Noise XX sessions to community servers (docs/protocol.md §3).

Mirrors internal/session in Go: Noise_XX_25519_ChaChaPoly_SHA256, prologue
"openarcanum/noise/v1", identity payloads in messages 2 and 3, and 65000-byte
fragments carrying a one-byte "more" flag.
"""

from __future__ import annotations

import asyncio
from collections.abc import Callable
from typing import Protocol

from openarcanum._vendor.noise.connection import Keypair, NoiseConnection
from openarcanum.i18n import _
from openarcanum.keys import CTX_NOISE_STATIC, BadSignature, SigningKey, StaticKey, verify
from openarcanum.version import SUPPORTED, VersionMismatch, negotiate
from openarcanum.wire import session_pb2 as sp

PROTOCOL_NAME = b"Noise_XX_25519_ChaChaPoly_SHA256"
PROLOGUE = b"openarcanum/noise/v1"
FRAGMENT_SIZE = 65000
MAX_MESSAGE = 32 << 20
FLAG_MORE = 0x01


class SessionError(Exception):
    pass


class IdentityError(SessionError):
    """A handshake identity proof failed."""


class SessionVersionError(SessionError):
    """No protocol version in common with the community server; the message
    says which side is outdated."""

    def __init__(self, mismatch: VersionMismatch):
        super().__init__(mismatch.user_message())
        self.mismatch = mismatch


class Transport(Protocol):
    async def send(self, data: bytes) -> None: ...
    async def recv(self, timeout: float | None = None) -> bytes: ...
    async def close(self, reason: str = "closed") -> None: ...


def sign_static(identity: SigningKey, static_public: bytes) -> bytes:
    return identity.sign(CTX_NOISE_STATIC, static_public)


def _new(static: StaticKey, initiator: bool) -> NoiseConnection:
    n = NoiseConnection.from_name(PROTOCOL_NAME)
    if initiator:
        n.set_as_initiator()
    else:
        n.set_as_responder()
    n.set_prologue(PROLOGUE)
    n.set_keypair_from_private_bytes(Keypair.STATIC, static.private)
    n.start_handshake()
    # noiseprotocol deletes the handshake state (and with it the peer's static
    # key) when the handshake completes; keep a copy first.
    proto = n.noise_protocol
    done = proto.handshake_done

    def capture_then_finish():
        rs = getattr(proto.handshake_state, "rs", None)
        if rs is not None and getattr(rs, "public_bytes", None):
            n.openarcanum_peer_static = bytes(rs.public_bytes)
        done()

    proto.handshake_done = capture_then_finish
    return n


def _peer_static(n: NoiseConnection) -> bytes:
    captured = getattr(n, "openarcanum_peer_static", None)
    if captured:
        return captured
    hs = getattr(n.noise_protocol, "handshake_state", None)
    rs = getattr(hs, "rs", None)
    if rs is None or not getattr(rs, "public_bytes", None):
        raise SessionError("peer static key missing")
    return bytes(rs.public_bytes)


class Session:
    def __init__(self, transport: Transport, noise: NoiseConnection, peer_static: bytes):
        self.transport = transport
        self._noise = noise
        self.peer_static = peer_static
        self.server: sp.ServerIdentity | None = None
        self.client: sp.ClientIdentity | None = None
        # The protocol version both sides chose (docs/protocol.md §1 "Versions").
        self.version = 0
        self._send_lock = asyncio.Lock()

    async def send(self, msg: bytes) -> None:
        if len(msg) > MAX_MESSAGE:
            raise SessionError(f"message of {len(msg)} bytes exceeds {MAX_MESSAGE}")
        # The lock keeps one message's fragments contiguous and ciphertexts in
        # nonce order.
        async with self._send_lock:
            off = 0
            while True:
                end = min(off + FRAGMENT_SIZE, len(msg))
                flag = FLAG_MORE if end < len(msg) else 0
                await self.transport.send(self._noise.encrypt(bytes([flag]) + msg[off:end]))
                if not flag:
                    return
                off = end

    async def recv(self, timeout: float | None = None) -> bytes:
        parts = bytearray()
        while True:
            ct = await self.transport.recv(timeout)
            try:
                pt = self._noise.decrypt(bytes(ct))
            except Exception as e:
                await self.transport.close("decrypt failed")
                raise SessionError("decrypt failed") from e
            if not pt:
                raise SessionError("empty fragment")
            if len(parts) + len(pt) - 1 > MAX_MESSAGE:
                await self.transport.close("message too large")
                raise SessionError("message too large")
            parts += pt[1:]
            if not pt[0] & FLAG_MORE:
                return bytes(parts)

    async def close(self, reason: str = "closed") -> None:
        await self.transport.close(reason)


async def dial(
    transport: Transport,
    static: StaticKey,
    identity_key: bytes,
    static_sig: bytes,
    device_name: str,
    verify_server: Callable[[sp.ServerIdentity], None],
    timeout: float = 20.0,
    versions: list[int] | None = None,
) -> Session:
    """Initiator side. verify_server raises to reject the server. The client
    offers every version it speaks; the server answers with its choice."""
    ours = list(versions or SUPPORTED)
    n = _new(static, initiator=True)
    await transport.send(bytes(n.write_message(sp.ClientHello(versions=ours).SerializeToString())))
    m2 = await transport.recv(timeout)
    try:
        p2 = bytes(n.read_message(bytes(m2)))
    except Exception as e:
        raise SessionError(f"message 2: {e}") from e
    server = sp.ServerIdentity()
    server.ParseFromString(p2)
    if server.version == 0 or server.version not in ours:
        try:
            negotiate(ours, list(server.versions), what="community server")
        except VersionMismatch as e:
            raise SessionVersionError(e) from None
        raise SessionError(f"the server chose protocol version {server.version}, which this build didn't offer")
    peer = _peer_static(n)
    try:
        verify(server.server_key, CTX_NOISE_STATIC, peer, server.static_sig)
    except BadSignature as e:
        raise IdentityError(_("couldn't confirm that the server is genuine")) from e
    verify_server(server)
    p3 = sp.ClientIdentity(identity_key=identity_key, static_sig=static_sig, device_name=device_name)
    await transport.send(bytes(n.write_message(p3.SerializeToString())))
    if not n.handshake_finished:
        raise SessionError("handshake did not finish")
    s = Session(transport, n, peer)
    s.server = server
    s.version = server.version
    return s


async def accept(
    transport: Transport,
    static: StaticKey,
    identity: SigningKey,
    cert: sp.SignedServerCert | None = None,
    timeout: float = 20.0,
    versions: list[int] | None = None,
) -> Session:
    """Responder side. Production responders are Go; this exists for interop tests."""
    ours = list(versions or SUPPORTED)
    n = _new(static, initiator=False)
    m1 = await transport.recv(timeout)
    hello = sp.ClientHello()
    hello.ParseFromString(bytes(n.read_message(bytes(m1))))
    offered = list(hello.versions) or ([hello.version] if hello.version else [])
    chosen, mismatch = 0, None
    try:
        chosen = negotiate(ours, offered, what="client")
    except VersionMismatch as e:
        mismatch = e
    payload = sp.ServerIdentity(
        server_key=identity.public,
        static_sig=sign_static(identity, static.public),
        version=chosen,
        versions=ours,
    )
    if cert is not None:
        payload.cert.CopyFrom(cert)
    await transport.send(bytes(n.write_message(payload.SerializeToString())))
    if mismatch is not None:
        raise SessionVersionError(mismatch)
    m3 = await transport.recv(timeout)
    try:
        p3 = bytes(n.read_message(bytes(m3)))
    except Exception as e:
        raise SessionError(f"message 3: {e}") from e
    client = sp.ClientIdentity()
    client.ParseFromString(p3)
    peer = _peer_static(n)
    try:
        verify(client.identity_key, CTX_NOISE_STATIC, peer, client.static_sig)
    except BadSignature as e:
        raise IdentityError(_("the server couldn't confirm who you are")) from e
    s = Session(transport, n, peer)
    s.client = client
    s.version = chosen
    return s
