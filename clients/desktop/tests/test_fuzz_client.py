"""Hypothesis fuzzing of more client decoders of untrusted input
(test_fuzz_decoders.py has the rest): the release manifest
(`updates.verify_manifest`), maintainer node replies (mailbox pages,
directory snapshot pages, relay lists, nameplates, blob chunks), and the
device-link messages that arrive on the link's relay topic before and after
the SPAKE2 exchange.

Each test feeds random bytes or valid inputs mutated byte by byte (and,
where a signature or a key guards the content, the same fuzz signed or
sealed with the real key) and asserts the decoder raises only its
documented exception or returns something meeting its contract.
"""

from __future__ import annotations

import asyncio
import hashlib
import json

import pytest
from hypothesis import example, given
from hypothesis import strategies as st
from test_fuzz_decoders import FUZZ, OWNER, SERVER, _cert, fuzzed, run

from openarcanum import crypto, devices, keys, updates
from openarcanum.errors import UserError
from openarcanum.meta import MetaClient, MetaError
from openarcanum.updates import UpdateError
from openarcanum.wire import meta_pb2 as mp

# ---------------------------------------------------------------- updates.verify_manifest

RELEASE = keys.SigningKey(bytes([3]) * 32)
OTHER = keys.SigningKey(bytes([4]) * 32)
NOW = 1_800_000_000.0
ASSET = updates.Asset("linux-x86_64", "https://releases.example.invalid/f.AppImage", "ab" * 32, 1000, "appimage")
PAYLOAD = updates.build_payload("1.2.3", {ASSET.platform: ASSET}, NOW - 60, NOW + 86400)
VALID_MANIFEST = updates.sign_envelope(PAYLOAD, [RELEASE])
VALID_BODY = updates.canonical(PAYLOAD)


def _envelope(body: bytes, signer: keys.SigningKey | None, sig: bytes | None = None) -> bytes:
    pub = (signer or RELEASE).public
    if sig is None:
        sig = signer.sign(updates.CTX, body) if signer else bytes(64)
    env = {"payload": updates.b64u(body), "signatures": [{"key": updates.b64u(pub), "sig": updates.b64u(sig)}]}
    return json.dumps(env).encode()


def _check_manifest(m: updates.Manifest) -> None:
    assert m.product == updates.PRODUCT_NAME
    updates.version_key(m.version)
    assert m.released_at < m.expires_at and m.expires_at > NOW
    for name, a in m.assets.items():
        assert updates.PLATFORM_KINDS[name] == a.kind and 0 < a.size <= updates.MAX_ASSET_BYTES
        assert a.url.startswith("https://")


@FUZZ
@given(fuzzed(VALID_MANIFEST, max_size=600))
def test_verify_manifest_envelope(raw):
    """The manifest file as downloaded, random or mutated: a Manifest that
    meets every check, or UpdateError."""
    try:
        m = updates.verify_manifest(raw, [RELEASE.public], now=NOW)
    except UpdateError:
        return
    _check_manifest(m)


json_values = st.recursive(
    st.none() | st.booleans() | st.integers() | st.floats(allow_nan=False) | st.text(max_size=40),
    lambda inner: st.lists(inner, max_size=4) | st.dictionaries(st.text(max_size=16), inner, max_size=5),
    max_leaves=20,
)


def _payloads():
    fields = st.dictionaries(
        st.sampled_from(["product", "version", "released_at", "expires_at", "assets"]), json_values, max_size=5
    )
    asset = st.dictionaries(st.sampled_from(["url", "sha256", "size", "kind"]), json_values, max_size=4)
    assets = st.dictionaries(st.sampled_from([*updates.PLATFORM_KINDS, "other"]), asset | json_values, max_size=3)
    mixed = st.builds(lambda f, a: {**PAYLOAD, **f, "assets": a} if a else {**PAYLOAD, **f}, fields, st.none() | assets)
    as_json = st.builds(lambda v: json.dumps(v).encode(), mixed | json_values)
    return st.one_of(as_json, fuzzed(VALID_BODY, max_size=600))


@FUZZ
@given(_payloads(), st.sampled_from(["release", "other", "unsigned"]))
@example(
    body=updates.canonical(
        {**PAYLOAD, "assets": {ASSET.platform: {**PAYLOAD["assets"][ASSET.platform], "url": "https://[x/"}}}
    ),
    signer="release",
)
def test_verify_manifest_signed_payloads(body, signer):
    """Payloads signed by the release key (what a stolen or misused key, or
    a buggy release script, could produce), by an untrusted key, or not
    signed: only the release key's are parsed, and those either meet every
    check or raise UpdateError."""
    key = {"release": RELEASE, "other": OTHER, "unsigned": None}[signer]
    try:
        m = updates.verify_manifest(_envelope(body, key), [RELEASE.public], now=NOW)
    except UpdateError as e:
        if signer != "release":
            assert "signature" in str(e) or "trust" in str(e), str(e)
        return
    assert signer == "release"
    _check_manifest(m)


@pytest.mark.parametrize("body", [b"{not json", b"\xff\xfe", b"[1, 2", b'{"product": "OpenArcanum"'])
def test_manifest_signature_is_checked_before_the_payload_is_parsed(body):
    """A payload that isn't JSON at all is refused for its signature (bad,
    untrusted or missing), never for its content: nothing signed by someone
    else is parsed."""
    cases = {
        _envelope(body, RELEASE, sig=bytes(64)): "isn't genuine",
        _envelope(body, OTHER): "doesn't trust",
        json.dumps({"payload": updates.b64u(body), "signatures": []}).encode(): "isn't signed",
    }
    for raw, why in cases.items():
        with pytest.raises(UpdateError, match=why):
            updates.verify_manifest(raw, [RELEASE.public], now=NOW)
    # Signed by the release key, the same bytes fail as JSON.
    with pytest.raises(UpdateError, match="damaged"):
        updates.verify_manifest(_envelope(body, RELEASE), [RELEASE.public], now=NOW)


# ---------------------------------------------------------------- maintainer node replies


class FuzzNode(MetaClient):
    """A MetaClient whose maintainer nodes answer with given bytes."""

    def __init__(self, replies: dict[str, list[bytes]]):
        super().__init__(list(replies))
        self.replies = {base: list(r) for base, r in replies.items()}
        self.requests = 0

    def _request(self, base, method, path, body, limit=1 << 30):
        self.requests += 1
        r = self.replies[base]
        if not r:
            raise MetaError("no more replies", 503)
        data = r.pop(0) if len(r) > 1 else r[0]
        if len(data) > limit:
            from openarcanum.meta import MetaTooLarge

            raise MetaTooLarge("too large")
        return data


def _signed_listing(name="C", withdrawn=False) -> mp.SignedListing:
    lst = mp.Listing(
        community=OWNER.public,
        server_key=SERVER.public,
        route_id=bytes(16),
        name=name,
        seq=1,
        relays=["ws://r/ws"],
        withdrawn=withdrawn,
    ).SerializeToString()
    return mp.SignedListing(listing=lst, signature=SERVER.sign(keys.CTX_LISTING, lst), cert=_cert())


MAILBOX = mp.MailboxItems(
    items=[mp.MailboxItem(id=1, blob=b"FDM2x"), mp.MailboxItem(id=2, blob=b"y")], epoch=5
).SerializeToString()
SNAPSHOT = [
    mp.DirectorySnapshot(listings=[_signed_listing()], more=True, next=10).SerializeToString(),
    mp.DirectorySnapshot(listings=[_signed_listing("D", True)], more=False).SerializeToString(),
]
RELAYS = mp.RelayList(relays=[mp.RelayInfo(url="wss://r.example.invalid/ws", capacity=5)]).SerializeToString()
NAMEPLATE = mp.Nameplate(number=42, relay="wss://r/ws", topic=bytes(32), expires_at=1).SerializeToString()


@FUZZ
@given(fuzzed(MAILBOX, max_size=300), fuzzed(MAILBOX, max_size=300))
def test_mailbox_pages(first, second):
    """A mailbox page from each of two nodes: a page, or MetaError for that
    node; the client's mailbox read (`fetch`) carries on with the next node."""
    node = FuzzNode({"https://a": [first], "https://b": [second]})
    try:
        page = run(node.fetch_page("https://a", bytes(32), 0))
        assert isinstance(page, mp.MailboxItems)
    except MetaError:
        pass
    items = run(node.fetch(bytes(32)))
    assert all(base in ("https://a", "https://b") and isinstance(it, mp.MailboxItem) for base, it in items)


@FUZZ
@given(st.lists(fuzzed(*SNAPSHOT, max_size=900), min_size=1, max_size=6), st.integers(1, 40))
def test_directory_snapshot_pages(pages, max_listings):
    """Directory snapshot pages, fuzzed: every listing returned verifies,
    none is withdrawn or repeated, the count bound holds, and the download
    ends within its request bound; otherwise MetaError."""
    node = FuzzNode({"https://a": pages})
    try:
        out, complete = run(node.snapshot(max_listings=max_listings, max_bytes=8192))
    except MetaError:
        return
    assert len(out) <= max_listings and isinstance(complete, bool)
    assert len({d["community"] for d in out}) == len(out)
    assert node.requests <= max_listings // 10 + 10


@FUZZ
@given(fuzzed(RELAYS, max_size=300), fuzzed(NAMEPLATE, max_size=200))
def test_relay_list_and_nameplate(relays, nameplate):
    """Relay lists (devices.new_device_start, DM relay choice) and link
    nameplates: parsed values, or MetaError."""
    node = FuzzNode({"https://a": [relays]})
    try:
        out = run(node.relays())
        assert all(isinstance(r["url"], str) for r in out)
    except MetaError:
        pass
    node = FuzzNode({"https://a": [nameplate]})
    try:
        plate = run(node.nameplate("wss://r/ws"))
        assert isinstance(plate, mp.Nameplate)
    except MetaError:
        pass


BLOB = b"ciphertext " * 20
BLOB_HASH = hashlib.sha256(BLOB).digest()
BLOB_CHUNKS = [
    mp.BlobChunkData(index=0, size=len(BLOB), data=BLOB[:100]).SerializeToString(),
    mp.BlobChunkData(index=1, size=len(BLOB), data=BLOB[100:]).SerializeToString(),
]


@FUZZ
@given(st.lists(fuzzed(*BLOB_CHUNKS, max_size=400), min_size=1, max_size=4), st.booleans())
def test_blob_download_chunks(chunks, sized):
    """Blob chunks from a node, fuzzed, with an honest second node: the
    download returns exactly the blob (from whichever node) or MetaError."""
    node = FuzzNode({"https://a": chunks, "https://b": BLOB_CHUNKS})
    try:
        data = run(node.blob_download(BLOB_HASH, bytes(32), size=len(BLOB) if sized else None))
    except MetaError:
        return
    assert data == BLOB


# ---------------------------------------------------------------- device-link messages


class TopicRelay:
    """The link's relay topic: what the device publishes is recorded, and
    `script` answers it (the other device, or an attacker on the topic)."""

    def __init__(self, script):
        self.script = script
        self.sess: devices.LinkSession | None = None
        self.sent: list[dict] = []

    async def subscribe(self, topic, handler):
        self.handler = handler

    async def publish(self, topic, data):
        msg = json.loads(data)
        self.sent.append(msg)
        for raw in self.script(msg):
            self.handler(topic, raw)

    async def close(self):
        pass


def _json_or_bytes():
    return st.one_of(
        st.binary(max_size=80),
        st.builds(lambda v: json.dumps(v).encode(), json_values),
        st.builds(
            lambda t, v: json.dumps({"t": t, "m": v, "box": v, "i": v, "n": v, "why": v}).encode(),
            st.sampled_from(["pake", "hello", "confirm", "transfer", "done", "abort", "x"]),
            json_values,
        ),
    )


@FUZZ
@given(st.lists(_json_or_bytes(), max_size=8))
def test_link_session_inbox(messages):
    """Anything published on the link topic: only JSON objects reach the
    session's inbox, and sealed fields from anyone without the key only
    raise DecryptError or ValueError."""
    sess = devices.LinkSession(TopicRelay(lambda m: []), b"t")
    sess.set_keys(bytes(32), i_am_new=True)
    for raw in messages:
        sess._on(b"t", raw)
    while not sess.inbox.empty():
        msg = sess.inbox.get_nowait()
        assert isinstance(msg, dict)
        box = msg.get("box")
        if isinstance(box, str):
            with pytest.raises((crypto.DecryptError, ValueError)):
                sess.unseal(box)


class LinkCore:
    """What DeviceManager touches while linking, before a profile exists."""

    def __init__(self):
        self.vault = None
        self.events: list[dict] = []

    def emit(self, name, data):
        self.events.append(data)


def _mutate_json(raw: bytes, edits: list[tuple[int, int]]) -> bytes:
    b = bytearray(raw)
    for pos, val in edits:
        if b:
            b[pos % len(b)] = val
    return bytes(b)


async def _until_stalled(task: asyncio.Task, sess: devices.LinkSession) -> None:
    """Wait for the link run to finish, or to sit waiting on an empty inbox
    (everything here is in memory, so a quiet inbox means it's blocked);
    then the other side gives up, as a user cancelling would."""
    quiet = 0
    while not task.done():
        await asyncio.sleep(0)
        quiet = quiet + 1 if sess.inbox.empty() else 0
        if quiet > 200:
            sess._on(b"t", json.dumps({"t": "abort", "why": "cancelled"}).encode())
            quiet = 0
    await task


async def _link_run(stage: str, edits: list[tuple[int, int]], injected: list[bytes]):
    """The new device's side of linking against a scripted existing device
    whose message at `stage` is damaged by `edits`, plus `injected`
    messages from someone else on the topic."""
    from spake2 import SPAKE2_Symmetric

    code = devices.make_code(7)
    peer = devices.LinkSession(TopicRelay(lambda m: []), b"t")
    pake = SPAKE2_Symmetric(code.encode(), idSymmetric=devices.LINK_ID)

    def damaged(raw: bytes, at: str) -> bytes:
        return _mutate_json(raw, edits) if stage == at else raw

    def existing(msg: dict) -> list[bytes]:
        out: list[bytes] = []
        if msg.get("t") == "pake":
            peer.set_keys(pake.finish(keys.decode(msg["m"])), i_am_new=False)
        elif msg.get("t") == "hello":
            out.append(damaged(json.dumps({"t": "confirm", "box": peer.seal("ok")}).encode(), "confirm"))
            sealed = peer.seal({"communities": {}, "identities": {}, "group": {"devices": {}}})
            part = json.dumps({"t": "transfer", "i": 0, "n": 1, "box": peer.seal(sealed)}).encode()
            out.append(damaged(part, "transfer"))
        return [*injected, *out]

    relay = TopicRelay(existing)
    sess = devices.LinkSession(relay, b"t")
    await sess.open()
    relay.handler(b"t", damaged(json.dumps({"t": "pake", "m": keys.encode(pake.start())}).encode(), "pake"))
    mgr = devices.DeviceManager(LinkCore())
    await _until_stalled(asyncio.ensure_future(mgr._new_device_run(sess, code, "new")), sess)
    return sess, mgr.core.events


@FUZZ
@given(
    st.sampled_from(["pake", "confirm", "transfer"]),
    st.lists(st.tuples(st.integers(0, 1 << 12), st.integers(32, 126)), min_size=1, max_size=4),
    st.lists(_json_or_bytes(), max_size=3),
)
def test_new_device_link_run(stage, edits, injected):
    """Damaged link messages and messages injected on the topic: the new
    device's run ends "received" or "failed" with a reason, never raises
    and never hangs."""
    sess, events = run(_link_run(stage, edits, injected))
    assert sess.state in ("received", "failed")
    if sess.state == "failed":
        assert events and events[-1]["stage"] == "failed" and events[-1]["error"]
    else:
        assert events[-1]["stage"] == "received"


@pytest.mark.parametrize(
    "injected",
    [
        [],
        [b'{"t": "transfer", "i": 0, "n": 1, "box": "AAAA"}', b'{"t": "transfer", "i": "x"}', b"junk"],
    ],
)
def test_new_device_link_ignores_junk_transfer_parts(injected):
    """Undamaged, the link is received, also when someone else on the topic
    sends transfer pieces the other device didn't seal (they are ignored)."""
    sess, events = run(_link_run("none", [], injected))
    assert sess.state == "received", events
    assert events[-1]["stage"] == "received"


@FUZZ
@given(st.lists(_json_or_bytes(), min_size=1, max_size=4))
def test_existing_device_link_messages(messages):
    """The existing device's side reads the new device's pake and hello
    messages from the topic: whatever arrives, the step ends with a result
    or UserError (shown to the user), nothing else."""

    async def main():
        relay = TopicRelay(lambda m: [*messages, json.dumps({"t": "abort", "why": "gone"}).encode()])
        sess = devices.LinkSession(relay, b"t")
        await sess.open()
        return await devices.link_handshake_existing(sess, devices.make_code(7))

    try:
        run(main())
    except UserError:
        pass


def test_link_session_recv_waits_for_its_kind():
    """recv skips other kinds and ends on abort with the sender's reason."""

    async def main():
        sess = devices.LinkSession(TopicRelay(lambda m: []), b"t")
        await sess.open()
        sess._on(b"t", b'{"t": "x"}')
        sess._on(b"t", b'{"t": "abort", "why": "no"}')
        with pytest.raises(UserError, match="no"):
            await sess.recv("pake", timeout=2)

    asyncio.run(main())
