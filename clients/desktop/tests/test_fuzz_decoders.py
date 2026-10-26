"""Hypothesis fuzzing of the client's decoders of untrusted input. The Go
side has native fuzz targets for the same wire formats
(`scripts/fuzz-go --list`).

Each test feeds random bytes, and valid messages mutated byte by byte, into a
decoder and asserts it raises only its documented exception types (or
returns a value meeting its contract), never anything else and never hangs.
Where a signature guards the content, the test also signs the fuzzed bytes
with a real key so the checks behind the signature are fuzzed too: that is
what a malicious peer holding its own key can send.

Example counts are bounded (FUZZ below) so `make check` stays quick; the
nightly CI job raises them with HYPOTHESIS_PROFILE=nightly.
"""

from __future__ import annotations

import asyncio
import os

import pytest
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from google.protobuf.message import DecodeError
from hypothesis import HealthCheck, example, given, settings
from hypothesis import strategies as st

from openarcanum import codes, community, dm, groups, keys, meta
from openarcanum.codes import CodeError
from openarcanum.crypto import encrypt_with_nonce
from openarcanum.errors import UserError
from openarcanum.groups import Group, GroupError, sign_state
from openarcanum.keys import SigningKey
from openarcanum.relay import Relay, RelayError
from openarcanum.session import IdentityError
from openarcanum.wire import community_pb2 as cp
from openarcanum.wire import dm_pb2 as dmp
from openarcanum.wire import invite_pb2 as ip
from openarcanum.wire import meta_pb2 as mp
from openarcanum.wire import relay_pb2 as rp
from openarcanum.wire import session_pb2 as sp

settings.register_profile("fuzz-short", max_examples=150, deadline=None, suppress_health_check=[HealthCheck.too_slow])
settings.register_profile("nightly", max_examples=5000, deadline=None, suppress_health_check=[HealthCheck.too_slow])
FUZZ = settings(settings.get_profile(os.environ.get("HYPOTHESIS_PROFILE", "fuzz-short")))

OWNER, SERVER, PEER = (SigningKey.generate() for _ in range(3))


# ---------------------------------------------------------------- strategies


def _mutate(data: bytes, edits: list[tuple[int, int, int]]) -> bytes:
    b = bytearray(data)
    for op, pos, val in edits:
        if op == 0 and b:  # flip a byte
            b[pos % len(b)] ^= val or 1
        elif op == 1:  # insert
            b.insert(pos % (len(b) + 1), val)
        elif op == 2 and b:  # delete
            del b[pos % len(b)]
        elif op == 3:  # truncate
            del b[pos % (len(b) + 1) :]
    return bytes(b)


def fuzzed(*valid: bytes, max_size: int = 400):
    """Random bytes, or one of `valid` with a few byte-level edits."""
    edits = st.lists(st.tuples(st.integers(0, 3), st.integers(0, 1 << 16), st.integers(0, 255)), max_size=6)
    mutated = st.builds(_mutate, st.sampled_from(valid), edits) if valid else st.nothing()
    return st.one_of(st.binary(max_size=max_size), mutated)


def run(coro, timeout: float = 10.0):
    """Run a coroutine; exceeding the timeout (a hang) fails the example."""
    return asyncio.run(asyncio.wait_for(coro, timeout))


# ---------------------------------------------------------------- codes.py

VALID_INVITE = codes.encode_invite(
    ip.Invite(server_key=SERVER.public, owner_key=OWNER.public, relays=["ws://r/ws"], join_key="secret", name="C")
)
VALID_CLAIM = codes.CLAIM_PREFIX + keys.encode(
    ip.ClaimCode(server_key=SERVER.public, secret=b"s", relays=["ws://r/ws"]).SerializeToString()
)
VALID_MOVE = codes.MOVE_PREFIX + keys.encode(
    ip.MoveCode(server_key=SERVER.public, owner_key=OWNER.public, secret=b"s", relays=["ws://r/ws"]).SerializeToString()
)


def _code_texts():
    prefixes = st.sampled_from(
        [codes.INVITE_PREFIX, codes.CLAIM_PREFIX, codes.MOVE_PREFIX, codes.LINK_SCHEME, "openarcanum-", ""]
    )
    proto = fuzzed(*(keys.decode(c.split(":", 1)[1]) for c in (VALID_INVITE, VALID_CLAIM, VALID_MOVE)))
    encoded = st.builds(
        lambda p, b, ws: p + ws + keys.encode(b) + ws, prefixes, proto, st.sampled_from(["", " ", "\n"])
    )
    mangled = st.builds(
        lambda c, e: _mutate(c.encode(), e).decode("latin-1"),
        st.sampled_from([VALID_INVITE, VALID_CLAIM, VALID_MOVE]),
        st.lists(st.tuples(st.integers(0, 3), st.integers(0, 999), st.integers(32, 126)), max_size=4),
    )
    return st.one_of(st.text(max_size=200), encoded, mangled)


@FUZZ
@given(_code_texts())
def test_codes_raise_only_code_error(text):
    assert codes.detect(text) in ("invite", "claim", "move", "unknown")
    for parse, check in (
        (codes.parse_invite, lambda c: len(c.server_key) == 32 and len(c.owner_key) == 32 and c.relays),
        (codes.parse_claim, lambda c: len(c.server_key) == 32 and c.secret and c.relays),
        (codes.parse_move, lambda c: len(c.server_key) == 32 and len(c.owner_key) == 32 and c.secret and c.relays),
    ):
        try:
            parsed = parse(text)
        except CodeError:
            continue
        assert check(parsed)


def test_valid_codes_parse():
    assert codes.parse_invite(VALID_INVITE).join_key == "secret"
    assert codes.parse_invite(codes.invite_link(codes.parse_invite(VALID_INVITE))).name == "C"
    assert codes.parse_claim(VALID_CLAIM).secret == b"s"
    assert codes.parse_move(VALID_MOVE).owner_key == OWNER.public


# ---------------------------------------------------------------- relay.py


class FakeWS:
    """Stands in for the websockets connection: yields fixed messages, then ends."""

    def __init__(self, messages):
        self.messages = list(messages)
        self.sent: list[bytes] = []

    def __aiter__(self):
        return self

    async def __anext__(self):
        if not self.messages:
            raise StopAsyncIteration
        await asyncio.sleep(0)
        return self.messages.pop(0)

    async def send(self, data):
        self.sent.append(data)

    async def close(self):
        pass


def _frame(**kw) -> bytes:
    return rp.RelayFrame(**kw).SerializeToString()


RELAY_FRAMES = [
    _frame(ack=rp.Ack(req=1)),
    _frame(error=rp.Error(req=2, code=rp.ERROR_CODE_LIMIT, message="no")),
    _frame(deliver=rp.Deliver(topic=b"t", sender=1, message_id=1, seq=0, final=True, data=b"hi")),
    _frame(deliver=rp.Deliver(topic=b"t", sender=1, message_id=2, seq=0, final=False, data=b"a" * 40)),
    _frame(deliver=rp.Deliver(topic=b"t", sender=1, message_id=2, seq=1, final=True, data=b"b" * 40)),
    _frame(stream_opened=rp.StreamOpened(stream_id=1)),
    _frame(stream_opened=rp.StreamOpened(stream_id=0x80000001)),
    _frame(stream_data=rp.StreamData(stream_id=0x80000001, data=b"d")),
    _frame(stream_close=rp.StreamClose(stream_id=0x80000001, reason="r")),
    _frame(stream_close=rp.StreamClose(stream_id=1, reason="refused")),
    _frame(hello=rp.Hello(max_chunk=1, max_message=1)),
]


@FUZZ
@given(st.lists(st.one_of(fuzzed(*RELAY_FRAMES, max_size=120), st.text(max_size=20)), max_size=25))
def test_relay_read_loop_survives_any_frames(messages):
    """Relay._read_loop over arbitrary relay frames: handlers only see whole
    messages within max_message for their own topic, pending calls resolve
    or fail with RelayError, and every stream handed out ends when the
    connection does (nothing waits forever)."""
    got: list[tuple[bytes, bytes]] = []

    async def main():
        r = Relay("ws://fuzz")
        r.hello = rp.Hello(nonce=bytes(32), max_chunk=64, max_message=100)
        r._handlers[b"t"] = lambda topic, data: got.append((topic, data))
        r._ws = FakeWS(messages)
        loop = asyncio.get_running_loop()
        pending = loop.create_future()
        r._pending[1] = pending
        opening = loop.create_future()
        r._opening[1] = opening
        handed: list = []
        await r._read_loop()
        while not r._incoming.empty():
            handed.append(r._incoming.get_nowait())
        assert r.close_reason == "relay closed the connection"
        for fut in (pending, opening):
            if fut.done() and fut.exception() is not None:
                assert isinstance(fut.exception(), RelayError)
        for s in handed:
            assert s.closed, "a stream outlived its relay connection"
            with pytest.raises(RelayError):
                while True:
                    await s.recv(timeout=1)

    run(main())
    for topic, data in got:
        assert topic == b"t" and len(data) <= 100


@FUZZ
@given(st.one_of(fuzzed(_frame(hello=rp.Hello(nonce=bytes(32), max_chunk=10, max_message=10))), st.text(max_size=20)))
def test_relay_connect_rejects_bad_hello(first):
    """Relay.connect against a server whose first message is arbitrary: it
    connects (a hello arrived) or raises RelayError, nothing else."""
    from websockets.asyncio.server import serve

    async def main():
        async def handler(ws):
            await ws.send(first)
            await ws.wait_closed()

        async with serve(handler, "127.0.0.1", 0) as server:
            port = server.sockets[0].getsockname()[1]
            r = Relay(f"ws://127.0.0.1:{port}")
            try:
                await r.connect(timeout=5)
            except RelayError:
                return
            assert r.hello is not None
            await r.close()

    run(main())


# ---------------------------------------------------------------- meta.py


def _cert(owner=OWNER, server=SERVER) -> sp.SignedServerCert:
    body = sp.ServerCert(owner_key=owner.public, server_key=server.public, issued_at=1, seq=1).SerializeToString()
    return sp.SignedServerCert(cert=body, signature=owner.sign(keys.CTX_SERVER_CERT, body))


VALID_LISTING = mp.Listing(
    community=OWNER.public, server_key=SERVER.public, route_id=bytes(16), name="C", seq=1, relays=["ws://r/ws"]
).SerializeToString()
VALID_ROUTE = mp.Route(route_id=bytes(16), server_key=SERVER.public, relays=["ws://r/ws"], seq=1).SerializeToString()


@FUZZ
@given(fuzzed(VALID_LISTING), fuzzed(_cert().cert), st.booleans())
def test_verify_listing(listing, cert_body, sign):
    """A directory result either verifies end to end or raises BadSignature."""
    sl = mp.SignedListing(
        listing=listing,
        signature=SERVER.sign(keys.CTX_LISTING, listing) if sign else bytes(64),
        cert=sp.SignedServerCert(cert=cert_body, signature=OWNER.sign(keys.CTX_SERVER_CERT, cert_body)),
    )
    try:
        lst = meta.verify_listing(sl)
    except keys.BadSignature:
        return
    cert = sp.ServerCert.FromString(cert_body)
    assert cert.server_key == lst.server_key and cert.owner_key == lst.community
    keys.verify(lst.server_key, keys.CTX_LISTING, listing, sl.signature)


@FUZZ
@given(fuzzed(VALID_ROUTE), fuzzed(_cert().cert), st.booleans(), st.booleans())
def test_verify_route(route, cert_body, sign, pin_owner):
    """A route either verifies (for the pinned owner, if any) or raises BadSignature."""
    sr = mp.SignedRoute(
        route=route,
        signature=SERVER.sign(keys.CTX_ROUTE, route) if sign else bytes(64),
        cert=sp.SignedServerCert(cert=cert_body, signature=OWNER.sign(keys.CTX_SERVER_CERT, cert_body)),
    )
    try:
        rt, cert, _owners = meta.verify_route(sr, OWNER.public if pin_owner else None)
    except keys.BadSignature:
        return
    assert cert.server_key == rt.server_key
    if pin_owner:
        assert cert.owner_key == OWNER.public


# ---------------------------------------------------------------- community.py


def _server_identity(cert: sp.SignedServerCert | None, server=SERVER) -> bytes:
    si = sp.ServerIdentity(server_key=server.public)
    if cert is not None:
        si.cert.CopyFrom(cert)
    return si.SerializeToString()


@FUZZ
@given(fuzzed(_server_identity(_cert()), _server_identity(_cert(server=PEER), server=PEER), _server_identity(None)))
def test_server_identity_verifier(raw):
    """The pinning check on the server's handshake identity accepts the
    pinned key or an owner-certified new one, and otherwise raises
    IdentityError, nothing else."""
    try:
        si = sp.ServerIdentity.FromString(raw)
    except DecodeError:
        return  # session.dial rejects unparseable handshake payloads first
    new_keys = []
    rec = {"server_key": keys.encode(SERVER.public), "owner_key": keys.encode(OWNER.public), "cert_seq": 1}
    verify = community.make_verifier(rec, lambda key, seq: new_keys.append((key, seq)))
    try:
        verify(si)
    except IdentityError:
        assert not new_keys
        return
    if si.server_key != SERVER.public:
        assert new_keys and new_keys[0][0] == si.server_key


class FakeCache:
    def __init__(self):
        self.messages: dict[int, list] = {1: [], 99: []}

    def drop_channel(self, chid):
        self.messages.pop(chid, None)

    def last_id(self, chid):
        return 0


class FakeCore:
    """The parts of Core a CommunityConnection touches while handling frames."""

    def __init__(self, cid: str):
        self.vault = type("V", (), {})()
        self.vault.data = {
            "communities": {
                cid: {
                    "server_key": keys.encode(SERVER.public),
                    "owner_key": keys.encode(OWNER.public),
                    "cert_seq": 1,
                    "relays": ["ws://r/ws"],
                    "status": "pending",
                }
            }
        }
        self._cache = FakeCache()
        self.events: list = []

    def cache(self, cid):
        return self._cache

    def save_vault(self):
        self.events.append("save")

    def spawn(self, coro):
        # An envelope under a newer key epoch asks for fresh state (a Sync
        # this offline harness can't answer): record the request.
        coro.close()
        self.events.append("refresh")

    def owner_keys_changed(self, cid, owner_set, state):
        # The real core moves owner keys around; here only parsing is under test.
        return False

    def complete_owner_leaves(self, cid, owner_set, state):
        pass

    def community_changed(self, cid):
        self.events.append("changed")

    def emit(self, *a):
        self.events.append(a)

    def channel_event(self, cid, ev, channel_id=0):
        assert isinstance(ev, cp.ChannelEvent)
        # The real core turns messages into dicts for the cache and the UI.
        kind = ev.WhichOneof("kind")
        if kind in ("message", "edited"):
            d = community.message_dict(getattr(ev, kind))
            assert isinstance(d["files"], list) and isinstance(d["reactions"], list)
        elif kind == "update":
            u = community.update_dict(ev.update)
            assert u is None or ("text" not in u and {"id", "channel_id"} <= set(u))
        self.events.append(ev)

    def presence(self, cid, member, online):
        self.events.append(("presence", member, online))

    async def community_ended(self, cid, name):
        self.events.append(("ended", name))

    async def after_connect(self, cid):
        self.events.append("after_connect")

    async def voice_update(self, cid, session):
        self.events.append(("voice", session))


CHANNEL_KEY = bytes(range(32))


def _state_changed(status=cp.MEMBER_STATUS_ACTIVE) -> bytes:
    state = cp.SyncState(
        info=cp.CommunityInfo(name="C", server_key=PEER.public),
        channels=[cp.Channel(id=1, name="g", kind=cp.CHANNEL_KIND_TEXT, topic=bytes(32), key=CHANNEL_KEY, key_epoch=1)],
        cert=_cert(server=PEER),
    )
    return cp.ServerMessage(state_changed=cp.StateChanged(status=status, state=state)).SerializeToString()


SERVER_EVENTS = [
    _state_changed(),
    _state_changed(cp.MEMBER_STATUS_BANNED),
    cp.ServerMessage(voice_update=cp.VoiceUpdate()).SerializeToString(),
    cp.ServerMessage(reply_to=3, ok=cp.Ok()).SerializeToString(),
    cp.ServerMessage(presence=cp.PresenceUpdate(member=bytes(32), online=True)).SerializeToString(),
]


@FUZZ
@given(fuzzed(*SERVER_EVENTS, max_size=600))
def test_pushed_server_events(raw):
    """A pushed ServerMessage (state change, voice update) goes through the
    same parse-then-dispatch as Session2's read loop and is either applied or
    dropped; the handler never raises."""

    async def main():
        cid = "c" * 16
        conn = community.CommunityConnection(FakeCore(cid), cid)
        conn.status = "pending"
        m = cp.ServerMessage()
        try:
            m.ParseFromString(raw)
        except DecodeError:
            return  # Session2._read_loop skips unparseable messages
        if m.reply_to:
            return
        await conn._on_event(m)
        rec = conn.rec
        # The pinned server key only moves to an owner-certified key.
        if rec["server_key"] != keys.encode(SERVER.public):
            si = sp.ServerIdentity(server_key=keys.decode(rec["server_key"]), cert=m.state_changed.state.cert)
            community.make_verifier({"server_key": keys.encode(SERVER.public), "owner_key": rec["owner_key"]})(si)

    run(main())


def _envelope(channel_id: int, epoch: int, pt: bytes) -> bytes:
    nonce = bytes(12)
    ct = encrypt_with_nonce(CHANNEL_KEY, nonce, pt, community.channel_aad(channel_id, epoch))
    return cp.ChannelEnvelope(channel_id=channel_id, key_epoch=epoch, nonce=nonce, ciphertext=ct).SerializeToString()


VALID_EVENT = cp.ChannelEvent(message=cp.Message(id=5, channel_id=1, text="hi")).SerializeToString()
RICH_EVENT = cp.ChannelEvent(
    edited=cp.Message(
        id=6,
        channel_id=1,
        text="<@AAAA> @everyone https://example.invalid/",
        files=[cp.FileRef(file_id=1, name="a.png", size=3, mime="image/png", key=bytes(32))],
        mentions=[bytes(32)],
        mention_roles=[2],
        mention_everyone=True,
        reactions=[cp.Reaction(emoji=":blob:1", members=[bytes(32)])],
        pinned=True,
        preview=cp.LinkPreview(url="https://example.invalid/", title="t", image=cp.FileRef(file_id=2, key=bytes(32))),
        system=cp.SystemEvent(kind=cp.SYSTEM_EVENT_KIND_JOINED, member=bytes(32)),
    )
).SerializeToString()
TYPING_EVENT = cp.ChannelEvent(typing=cp.TypingEvent(member=bytes(32), name="A")).SerializeToString()
UPDATE_EVENTS = [
    cp.ChannelEvent(
        update=cp.MessageUpdate(
            message_id=6,
            channel_id=1,
            reactions=cp.ReactionList(reactions=[cp.Reaction(emoji=":blob:1", members=[bytes(32)])]),
        )
    ).SerializeToString(),
    cp.ChannelEvent(update=cp.MessageUpdate(message_id=6, channel_id=1, pinned=False)).SerializeToString(),
    cp.ChannelEvent(
        update=cp.MessageUpdate(
            message_id=6,
            channel_id=1,
            preview=cp.LinkPreview(
                url="https://example.invalid/", title="t", image=cp.FileRef(file_id=2, key=bytes(32))
            ),
        )
    ).SerializeToString(),
]


@FUZZ
@given(
    st.sampled_from(["raw", "signed", "sealed"]),
    fuzzed(_envelope(1, 1, VALID_EVENT), VALID_EVENT, RICH_EVENT, TYPING_EVENT, *UPDATE_EVENTS, max_size=400),
)
@example(mode="signed", data=cp.ChannelEnvelope(channel_id=1, key_epoch=1).SerializeToString())  # empty nonce
def test_channel_topic_events(mode, data):
    """Channel events from a relay topic: unsigned junk, junk signed by the
    server, and junk the server encrypted and signed. _on_topic drops what
    doesn't verify, decrypt or parse, and never raises."""
    cid = "c" * 16
    core = FakeCore(cid)
    conn = community.CommunityConnection(core, cid)
    conn.server_keys = [SERVER.public]
    conn.channel_keys = {1: {1: CHANNEL_KEY}}
    if mode == "raw":
        payload = data
    else:
        env = data if mode == "signed" else _envelope(1, 1, data)
        payload = cp.SignedEnvelope(
            envelope=env, signature=SERVER.sign(keys.CTX_CHANNEL_ENVELOPE, env)
        ).SerializeToString()
    conn._on_topic(b"t", payload)
    if mode == "raw":
        assert not [e for e in core.events if isinstance(e, cp.ChannelEvent)]


# ---------------------------------------------------------------- dm.py


def _device(identity: SigningKey) -> dmp.DmDevice:
    """A device entry whose ed25519 key consents to the list, as dm.py checks."""
    dev = Ed25519PrivateKey.generate()
    curve = os.urandom(32)
    return dmp.DmDevice(
        device_id=os.urandom(8),
        curve25519=curve,
        ed25519=dev.public_key().public_bytes_raw(),
        device_signature=dev.sign(dm.device_consent(identity.public, curve)),
    )


def _device_list(identity: SigningKey) -> bytes:
    return dmp.DmDeviceList(identity=identity.public, seq=1, devices=[_device(identity)]).SerializeToString(
        deterministic=True
    )


VALID_LIST = _device_list(PEER)
VALID_CARD = dmp.ContactCard(
    identity=PEER.public,
    name="p",
    devices=dmp.SignedDmDeviceList(list=VALID_LIST, signature=PEER.sign(keys.CTX_DM_DEVICE_LIST, VALID_LIST)),
).SerializeToString(deterministic=True)


@FUZZ
@given(fuzzed(VALID_LIST), st.booleans())
def test_open_device_list(raw, sign):
    """Unsigned device lists, and garbage the identity signed itself, raise
    UserError only (signed garbage may parse; it must not crash)."""
    sig = PEER.sign(keys.CTX_DM_DEVICE_LIST, raw) if sign else bytes(64)
    try:
        lst = dm.open_device_list(dmp.SignedDmDeviceList(list=raw, signature=sig), PEER.public)
    except UserError:
        return
    assert sign, f"accepted an unsigned device list: {lst}"


def test_open_device_list_signed_garbage():
    raw = b"\x00"
    signed = dmp.SignedDmDeviceList(list=raw, signature=PEER.sign(keys.CTX_DM_DEVICE_LIST, raw))
    with pytest.raises(UserError):
        dm.open_device_list(signed, PEER.public)


@FUZZ
@given(fuzzed(VALID_CARD, max_size=600), st.booleans())
def test_open_card(raw, sign):
    sig = PEER.sign(keys.CTX_DM_CARD, raw) if sign else bytes(64)
    try:
        card = dm.open_card(dmp.SignedContactCard(card=raw, signature=sig))
    except UserError:
        return
    assert card.card.identity == card.devices.identity


@FUZZ
@given(fuzzed(dmp.OneTimeKey(curve25519=bytes(32), key_id="k", key=bytes(32), signature=bytes(64)).SerializeToString()))
def test_check_otk(raw):
    try:
        otk = dmp.OneTimeKey.FromString(raw)
    except DecodeError:
        return  # parsed by the caller from a ContactCardResult
    devices = dmp.DmDeviceList.FromString(VALID_LIST)
    assert dm.check_otk(otk, devices) in (True, False)


# ---------------------------------------------------------------- groups.py


def _genesis_state() -> dmp.GroupState:
    return dmp.GroupState(
        group_id=bytes(16),
        seq=1,
        owner=OWNER.public,
        members=[dmp.GroupMember(identity=OWNER.public, name="o", joined_seq=1)],
        name="G",
        relay="ws://r/ws",
        topic=bytes(32),
    )


VALID_GENESIS = _genesis_state().SerializeToString(deterministic=True)


@FUZZ
@given(fuzzed(VALID_GENESIS), st.sampled_from(["owner", "peer", "unsigned"]))
def test_open_state(raw, signer):
    if signer == "unsigned":
        signed = dmp.SignedGroupState(state=raw, signer=OWNER.public, signature=bytes(64))
    else:
        k = OWNER if signer == "owner" else PEER
        signed = dmp.SignedGroupState(state=raw, signer=k.public, signature=k.sign(keys.CTX_DM_GROUP_STATE, raw))
    try:
        st_ = groups.open_state(signed)
    except GroupError:
        return
    assert signer != "unsigned"
    assert st_ == dmp.GroupState.FromString(raw)


def _chain_seeds() -> list[bytes]:
    g = Group.from_chain([sign_state(OWNER, _genesis_state())])
    nxt = g.successor()
    nxt.name = "Renamed"
    return [s.SerializeToString() for s in [g.chain[0], sign_state(OWNER, nxt)]]


@FUZZ
@given(st.lists(st.tuples(fuzzed(*_chain_seeds(), max_size=300), st.booleans()), min_size=0, max_size=6))
def test_group_from_chain(items):
    """A group history from the network: each entry is fuzzed bytes, either
    taken as a SignedGroupState as is or re-signed by the owner. Building the
    group raises only GroupError, and a group that builds has a valid head."""
    chain = []
    for raw, resign in items:
        try:
            s = dmp.SignedGroupState.FromString(raw)
        except DecodeError:
            continue  # the caller parses the list before from_chain
        if resign:
            s = dmp.SignedGroupState(
                state=s.state, signer=OWNER.public, signature=OWNER.sign(keys.CTX_DM_GROUP_STATE, s.state)
            )
        chain.append(s)
    try:
        g = Group.from_chain(chain)
    except GroupError:
        return
    assert g.state is not None and g.state.seq == len(g.chain)
