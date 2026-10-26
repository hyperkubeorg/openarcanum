"""Hypothesis fuzzing of what reaches the DM code from other people and other
devices: `DMManager.on_blob` (mailbox and relay
blobs: envelopes, Olm and Megolm payloads, DM events), `DMFiles.on_chunk`
(live attachment chunks), the device-group sync events and history-copy
chunks `DeviceManager.apply_event` takes, and the community statuses a
server passes on (`Statuses._entry`, `_take_keys`: entries, key drops and
status plaintexts).

A real client core (its own profile, DM identity, Olm account and history
store) is the victim. The attacker is a real second party: its own identity
key, a signed device list the victim has accepted, an Olm session with the
victim and a Megolm session in a group they share, so fuzzed plaintexts are
encrypted and signed with real keys and reach the checks behind the
cryptography, as a malicious peer's messages would.

Properties: nothing raises out of the handlers; blobs that aren't
authentic leave the stored state exactly as it was; and whatever an
authentic peer sends leaves a state the app can still list, open, show and
save. Every example starts from the same stored state.
"""

from __future__ import annotations

import asyncio
import atexit
import copy
import functools
import hashlib
import json
import os
import shutil
import tempfile
from pathlib import Path

from conftest import new_core
from hypothesis import given
from hypothesis import strategies as st
from test_fuzz_decoders import FUZZ, fuzzed

from openarcanum import dm, groups, keys, statuses
from openarcanum.dmfiles import CHUNK_MAGIC, LIVE_CHUNK
from openarcanum.groups import Group
from openarcanum.keys import SigningKey
from openarcanum.olm import OlmStore
from openarcanum.wire import community_pb2 as cp
from openarcanum.wire import dm_pb2 as dmp
from openarcanum.wire import meta_pb2 as mp

A_LABEL = "dm:a"
GID = bytes(range(16))
SEED_MSG = bytes([0xEE]) * 16


class Harness:
    """A victim core and an attacker that shares a group DM with it."""

    def __init__(self) -> None:
        self.dir = Path(tempfile.mkdtemp(prefix="openarcanum-fuzz-dm-"))
        self.loop = asyncio.new_event_loop()
        atexit.register(self.close)
        self.run(self._setup())

    def close(self) -> None:
        if not self.loop.is_closed():
            self.loop.run_until_complete(self.core.shutdown())
            self.loop.close()
        shutil.rmtree(self.dir, ignore_errors=True)

    def run(self, coro, timeout: float = 20.0):
        """Run on the harness loop; exceeding the timeout (a hang) fails."""
        return self.loop.run_until_complete(asyncio.wait_for(coro, timeout))

    async def _setup(self) -> None:
        self.core, self.events = await new_core(self.dir / "victim", "victim", maintainers=[])
        # Offline: no maintainer nodes, so nothing in the background reaches out.
        self.core.meta.urls = []
        dms = self.dms = self.core.dms
        cid = "f" * 16
        self.core.vault.data["communities"][cid] = {"name": "Fuzz", "status": "left"}
        self.pub = dms._ident(cid, True)
        await dms._ensure_listed(self.pub)
        self.label = dms._label(self.pub)
        self.v_id = dm.unb64(self.pub)
        self.v_curve, self.v_ed = dms.olm.curve(self.label), dms.olm.ed(self.label)

        # The attacker: identity, device, signed device list and card.
        self.a = SigningKey(bytes([7]) * 32)
        self.a_olm = OlmStore(self.dir / "attacker-olm.bin", os.urandom(32), os.urandom(32))
        self.a_curve, self.a_ed = self.a_olm.curve(A_LABEL), self.a_olm.ed(A_LABEL)
        device = dmp.DmDevice(
            device_id=os.urandom(8),
            curve25519=self.a_curve,
            ed25519=self.a_ed,
            name="attacker",
            device_signature=self.a_olm.sign(A_LABEL, dm.device_consent(self.a.public, self.a_curve)),
        )
        devices = dm.sign_device_list(self.a, dmp.DmDeviceList(identity=self.a.public, seq=1, devices=[device]))
        self.card = dm.sign_card(self.a, dmp.ContactCard(identity=self.a.public, name="Attacker", devices=devices))
        dms._remember_card(dm.open_card(self.card))

        # A group the attacker owns and the victim joined (invited, then accepted).
        genesis = dmp.GroupState(
            group_id=GID,
            seq=1,
            owner=self.a.public,
            members=[dmp.GroupMember(identity=self.a.public, name="Attacker", joined_seq=1)],
            name="Fuzz",
            allow_attachments=True,
            topic=bytes(32),
        )
        g = Group.from_chain([groups.sign_state(self.a, genesis)])
        invite = g.successor()
        invite.invited.append(dmp.GroupMember(identity=self.v_id, name="Victim"))
        g.apply(groups.sign_state(self.a, invite))
        self.invite_chain = list(g.chain)
        accept = g.successor()
        del accept.invited[:]
        accept.members.append(dmp.GroupMember(identity=self.v_id, name="Victim", joined_seq=3))
        g.apply(groups.sign_state(dms._signing(self.pub), accept))
        self.group = g
        self.gid = GID.hex()
        dms.d["groups"][self.gid] = {
            "chain": g.to_list(),
            "me": self.pub,
            "community": cid,
            "status": "active",
            "inbox_token": dm.b64(os.urandom(32)),
            "created": 1,
            "rekey": False,
        }
        self.core.save_vault()

        # The attacker's Megolm session reaches the victim the real way: a
        # room key over an Olm session started from the victim's fallback key.
        self.fallback = dms.olm.fallback_key(self.label)[1]
        self.sid, skey = self.a_olm.rotate(A_LABEL, GID)
        await dms.on_blob(
            self.olm_blob(self.olm_header(room_key=dmp.RoomKey(group_id=GID, session_id=self.sid, session_key=skey)))
        )
        assert self.sid in dms.olm.inbound, "the room key wasn't accepted"
        # And one message from the attacker, for edits, reactions and replies to target.
        await dms.on_blob(self.megolm_blob(self.event_header(id=SEED_MSG, text=dmp.DmText(text="hello @Victim"))))
        assert dms.history(self.gid), "the seed message wasn't accepted"
        self.saved_dm = copy.deepcopy(self.core.vault.data["dm"])
        self.saved_history = copy.deepcopy(dms.store.data)
        # Authentic blobs already delivered once: replaying them must change nothing.
        self.replays = [
            self.olm_blob(self.olm_header(dummy=dmp.OlmDummy())),
            self.megolm_blob(self.event_header(id=SEED_MSG, text=dmp.DmText(text="hello again"))),
        ]
        for blob in self.replays:
            await dms.on_blob(blob)
        self.saved_dm = copy.deepcopy(self.core.vault.data["dm"])
        self.saved_history = copy.deepcopy(dms.store.data)

    # ---------------------------------------------------------------- what the attacker sends

    def olm_header(self, **body) -> dmp.OlmPayload:
        return dmp.OlmPayload(
            sender_identity=self.a.public,
            sender_ed25519=self.a_ed,
            recipient_identity=self.v_id,
            recipient_ed25519=self.v_ed,
            at=1,
            **body,
        )

    def olm_blob(self, payload: dmp.OlmPayload | bytes) -> bytes:
        pt = payload if isinstance(payload, bytes) else payload.SerializeToString()
        mtype, ct = self.a_olm.encrypt_olm(A_LABEL, self.v_curve, pt, one_time_key=self.fallback)
        env = dmp.Envelope(
            olm=dmp.OlmEnvelope(
                sender_curve25519=self.a_curve, recipient_curve25519=self.v_curve, type=mtype, ciphertext=ct
            )
        )
        return dm.MAGIC + env.SerializeToString()

    def event_header(self, **body) -> dmp.DmEvent:
        return dmp.DmEvent(
            id=body.pop("id", None) or os.urandom(16),
            at=1_700_000_000_000,
            group_id=GID,
            sender=self.a.public,
            sender_device=self.a_curve,
            **body,
        )

    def megolm_blob(self, event: dmp.DmEvent | bytes) -> bytes:
        pt = event if isinstance(event, bytes) else event.SerializeToString()
        sid, ct = self.a_olm.encrypt_megolm(A_LABEL, GID, pt)
        env = dmp.Envelope(megolm=dmp.MegolmEnvelope(sender_curve25519=self.a_curve, session_id=sid, ciphertext=ct))
        return dm.MAGIC + env.SerializeToString()

    # ---------------------------------------------------------------- state

    def reset(self) -> None:
        """Back to the stored state after setup (in memory and on disk)."""
        dms = self.dms
        self.core.vault.data["dm"] = copy.deepcopy(self.saved_dm)
        dms.store.data = copy.deepcopy(self.saved_history)
        for table in (dms._gcache, dms.typing, dms._waiting, dms._early_keys, dms.files._assembling):
            table.clear()
        dms._early_bytes = 0
        dms._unheld.clear()

    def stored(self) -> str:
        return json.dumps([self.core.vault.data["dm"], self.dms.store.data], sort_keys=True, default=repr)

    def check_usable(self) -> None:
        """Everything the UI asks for still works, and everything still saves."""
        dms = self.dms
        json.dumps(self.core.vault.data)
        json.dumps(dms.store.data)
        dms.list()
        dms.contacts()
        for gid in list(dms.d["groups"]):
            dms.info(gid)
            dms.history(gid)
        self.core.save_vault()
        dms.store.save()


@functools.cache
def harness() -> Harness:
    return Harness()


def _olm_bodies(h: Harness) -> list[bytes]:
    """Serialized OlmPayload bodies, appended to a valid header (protobuf
    merges the fields, so a fuzzed tail can also override the header)."""
    sid, skey = h.a_olm.current_key(A_LABEL, GID)
    nxt = h.group.successor()
    nxt.name = "Renamed"
    bodies = [
        dmp.OlmPayload(room_key=dmp.RoomKey(group_id=GID, session_id=sid, session_key=skey)),
        dmp.OlmPayload(room_key=dmp.RoomKey(group_id=bytes(16), session_id="x", session_key="y")),
        dmp.OlmPayload(invite=dmp.GroupInvite(chain=h.invite_chain, cards=[h.card])),
        dmp.OlmPayload(card=h.card),
        dmp.OlmPayload(sender_card=h.card, dummy=dmp.OlmDummy()),
        dmp.OlmPayload(sync=json.dumps({"id": "e", "from": "x", "at": 1, "type": "setting", "data": {}}).encode()),
        dmp.OlmPayload(state_update=groups.sign_state(h.a, nxt)),
        # A contact's status session key (statuses.py).
        dmp.OlmPayload(status_key=dmp.StatusKey(audience=bytes(range(16, 32)), session_id=sid, session_key=skey)),
        dmp.OlmPayload(status_key=dmp.StatusKey(audience=GID, session_id="x", session_key="y")),
    ]
    return [b.SerializeToString() for b in bodies]


def _event_bodies(h: Harness) -> list[bytes]:
    nxt = h.group.successor()
    nxt.name = "Renamed"
    nxt.allow_attachments = False
    ref = dmp.DmFileRef(
        hash=hashlib.sha256(b"x").digest(),
        key=bytes(16),
        name="a.png",
        mime="image/png",
        size=1,
        ciphertext_size=200,
        width=1,
        height=1,
        thumbnail=b"\xff\xd8\xff",
    )
    bodies = [
        dmp.DmEvent(text=dmp.DmText(text="hi @Victim", reply_to=SEED_MSG, files=[ref], mentions=[h.v_id])),
        dmp.DmEvent(edit=dmp.DmEdit(target=SEED_MSG, text="edited")),
        dmp.DmEvent(delete=dmp.DmDelete(target=SEED_MSG)),
        dmp.DmEvent(reaction=dmp.DmReaction(target=SEED_MSG, emoji="👍")),
        dmp.DmEvent(pin=dmp.DmPin(target=SEED_MSG)),
        dmp.DmEvent(typing=dmp.DmTyping(typing=True)),
        dmp.DmEvent(state=groups.sign_state(h.a, nxt), state_tail=list(h.group.chain[-2:])),
        dmp.DmEvent(inboxes=dmp.DmInboxes(curve25519=h.a_curve, inbox=bytes(32))),
        dmp.DmEvent(card=h.card),
        dmp.DmEvent(sync=b'{"type": "setting"}'),
        dmp.DmEvent(
            stored=dmp.DmFileStored(
                hash=ref.hash,
                nodes=["https://n.example.invalid"],
                tokens=[dmp.DeviceToken(curve25519=h.v_curve, token=bytes(32))],
                expires_at=1,
            )
        ),
        dmp.DmEvent(received=dmp.DmFileReceived(hashes=[ref.hash])),
        # A status in a group's session is ignored (statuses.py).
        dmp.DmEvent(status=dmp.DmStatus(state=dmp.STATUS_STATE_DND, text="busy", emoji="🎧", expires_at=1)),
    ]
    return [b.SerializeToString() for b in bodies]


# ---------------------------------------------------------------- dm.on_blob


def _unauthenticated_blobs():
    def build(h: Harness):
        envelopes = [
            dmp.Envelope(
                olm=dmp.OlmEnvelope(
                    sender_curve25519=h.a_curve, recipient_curve25519=h.v_curve, type=t, ciphertext=os.urandom(80)
                )
            ).SerializeToString()
            for t in (0, 1)
        ] + [
            dmp.Envelope(
                megolm=dmp.MegolmEnvelope(sender_curve25519=h.a_curve, session_id=h.sid, ciphertext=os.urandom(120))
            ).SerializeToString()
        ]
        replays = [b[len(dm.MAGIC) :] for b in h.replays]
        return st.one_of(
            st.builds(lambda b: dm.MAGIC + b, fuzzed(*envelopes, *replays, max_size=600)),
            st.builds(
                lambda b: CHUNK_MAGIC + b, fuzzed(dmp.DmFileChunk(index=0, total=1, data=b"x").SerializeToString())
            ),
            st.binary(max_size=200),
        )

    return st.deferred(lambda: build(harness()))


@FUZZ
@given(_unauthenticated_blobs())
def test_on_blob_unauthenticated(blob):
    """Random blobs, damaged envelopes, ciphertexts no session made and
    replays of delivered blobs: on_blob answers True or False, never raises,
    and the stored state doesn't change."""
    h = harness()
    h.reset()
    before = h.stored()
    assert h.run(h.dms.on_blob(blob)) in (True, False)
    assert h.stored() == before
    h.check_usable()


def _olm_plaintexts():
    return st.deferred(
        lambda: st.builds(
            lambda tail: harness().olm_header().SerializeToString() + tail,
            fuzzed(*_olm_bodies(harness()), max_size=600),
        )
    )


@FUZZ
@given(_olm_plaintexts())
def test_on_blob_authentic_olm_payloads(plaintext):
    """Olm payloads the attacker encrypts to the victim over a real session:
    room keys, invitations, cards, sync events and state updates, valid or
    fuzzed. Nothing raises, and the victim's state stays usable."""
    h = harness()
    h.reset()
    assert h.run(h.dms.on_blob(h.olm_blob(plaintext))) in (True, False)
    h.check_usable()


def _event_plaintexts():
    return st.deferred(
        lambda: st.builds(
            lambda tail: harness().event_header().SerializeToString() + tail,
            fuzzed(*_event_bodies(harness()), max_size=600),
        )
    )


@FUZZ
@given(_event_plaintexts())
def test_on_blob_authentic_megolm_events(plaintext):
    """DM events the attacker encrypts with its Megolm session in the shared
    group: messages with files, edits, deletes, reactions, pins, typing,
    state changes, inboxes, cards, sync, stored and received notices, valid
    or fuzzed. Nothing raises, and the victim's state stays usable."""
    h = harness()
    h.reset()
    assert h.run(h.dms.on_blob(h.megolm_blob(plaintext))) in (True, False)
    h.check_usable()
    for m in h.dms.history(h.gid):
        assert isinstance(m["id"], str) and isinstance(m["at"], int)


# ---------------------------------------------------------------- dmfiles.on_chunk

FILE = os.urandom(1000)
FILE_HASH = hashlib.sha256(FILE).digest()
GOOD_CHUNK = dmp.DmFileChunk(hash=FILE_HASH, index=0, total=1, data=FILE).SerializeToString()


@FUZZ
@given(st.lists(fuzzed(GOOD_CHUNK, max_size=1200), max_size=6), st.booleans())
def test_on_chunk(chunks, honest_last):
    """Live attachment chunks for an expected file, fuzzed, optionally
    followed by the real one: nothing raises, a part file never grows past
    the expected size, a file is only kept if its hash matches, and the real
    chunk is always taken whatever came before it."""
    h = harness()
    h.reset()
    files = h.dms.files
    kept = files.dir / FILE_HASH.hex()
    kept.unlink(missing_ok=True)
    files.expect([dmp.DmFileRef(hash=FILE_HASH, ciphertext_size=len(FILE), live_chunks=[FILE_HASH])])
    for c in [*chunks, *([GOOD_CHUNK] if honest_last else [])]:
        h.run(files.on_chunk(CHUNK_MAGIC + c))
    part = files.dir / f"{FILE_HASH.hex()}.part"
    if part.exists():
        assert part.stat().st_size <= LIVE_CHUNK
    for f in files.dir.iterdir():
        if not f.name.endswith(".part") and len(f.name) == 64:
            assert hashlib.sha256(f.read_bytes()).hexdigest() == f.name, "kept a file that fails its hash"
    if honest_last:
        assert kept.exists(), "a forged chunk kept the real one out"
    if kept.exists():
        kept.unlink()
    for f in files.dir.glob("*.part"):
        f.unlink()


def test_forged_live_chunks_dont_spoil_the_real_file():
    """Anyone on the group's relay topic can send chunks. Were the first
    chunk at an index to win (or total=1 to end the file), they could keep
    the real chunks out. Only chunks whose hash the sender's encrypted
    message lists are taken, so the real file still assembles."""
    h = harness()
    h.reset()
    files = h.dms.files
    data = os.urandom(LIVE_CHUNK * 2 + 777)
    digest = hashlib.sha256(data).digest()
    parts = [data[i : i + LIVE_CHUNK] for i in range(0, len(data), LIVE_CHUNK)]
    kept = files.dir / digest.hex()
    kept.unlink(missing_ok=True)
    ref = dmp.DmFileRef(hash=digest, ciphertext_size=len(data), live_chunks=[hashlib.sha256(p).digest() for p in parts])
    files.expect([ref])

    def chunk(index, total, payload):
        return CHUNK_MAGIC + dmp.DmFileChunk(hash=digest, index=index, total=total, data=payload).SerializeToString()

    # Forgeries first: a one-chunk "file", wrong data at every index, a wrong total.
    h.run(files.on_chunk(chunk(0, 1, b"x")))
    for i in range(3):
        h.run(files.on_chunk(chunk(i, 3, os.urandom(len(parts[i])))))
    h.run(files.on_chunk(chunk(0, 2, parts[0])))
    assert not kept.exists()
    for i, p in enumerate(parts):
        h.run(files.on_chunk(chunk(i, 3, p)))
    assert kept.exists() and kept.read_bytes() == data
    kept.unlink()
    # A file the message lists no chunk hashes for isn't assembled live (it's
    # fetched from the store instead).
    other = os.urandom(100)
    files.expect([dmp.DmFileRef(hash=hashlib.sha256(other).digest(), ciphertext_size=100)])
    assert hashlib.sha256(other).digest() not in files._assembling


# ---------------------------------------------------------------- device sync events and history chunks

KINDS = [
    "community_left",
    "owner_key",
    "setting",
    "device_linked",
    "device_info",
    "device_keys",
    "device_renamed",
    "device_removed",
    "dm_identity",
    "dm_group",
    "dm_forget",
    "dm_read",
    "dm_blocked",
    "dm_verified",
    "contact_added",
    "contact_removed",
    "history_request",
    "history_chunk",
    "nonsense",
]

json_values = st.recursive(
    st.none() | st.booleans() | st.integers(-(2**63), 2**63) | st.floats(allow_nan=False) | st.text(max_size=12),
    lambda inner: st.lists(inner, max_size=4) | st.dictionaries(st.text(max_size=8), inner, max_size=4),
    max_leaves=12,
)
FIELDS = [
    "cid",
    "iid",
    "identity",
    "key",
    "value",
    "device",
    "keys",
    "name",
    "id",
    "at",
    "blocked",
    "verified",
    "contact",
    "to",
    "cursor",
    "items",
    "next",
    "delete_key",
    "pub",
    "gid",
    "chain",
]


def _sync_events():
    data = st.dictionaries(st.sampled_from(FIELDS), json_values, max_size=6)
    item = st.fixed_dictionaries(
        {
            "k": st.sampled_from(["dm", "c", "x"]),
            # The shared group's id too, so imported entries land in a conversation the UI shows.
            "id": st.just(GID.hex()) | json_values,
            "m": json_values
            | st.dictionaries(
                st.sampled_from(["id", "at", "sender", "text", "files", "reactions", "deleted", "reply_to"]),
                st.text(max_size=8) | json_values,
            ),
        }
    )
    chunk = st.fixed_dictionaries(
        {"to": st.just("ME"), "cursor": st.none(), "items": st.lists(item, max_size=5), "next": json_values}
    )
    return (
        st.fixed_dictionaries(
            {
                "id": st.text(max_size=8),
                "from": st.text(max_size=8),
                "at": st.one_of(st.integers(0, 2**62), json_values),
                "type": st.sampled_from(KINDS),
                "data": st.one_of(data, chunk, json_values),
            }
        )
        | json_values
    )


@FUZZ
@given(_sync_events(), st.booleans())
def test_device_sync_events(event, copying):
    """Sync events from another of my devices (after authentication) and
    history-copy chunks, valid-shaped with fuzzed values or not dicts at
    all: apply_event never raises and leaves a state that's still usable."""
    h = harness()
    h.reset()
    v = h.core.vault.data
    me = h.core.devices.me
    saved = {k: copy.deepcopy(v.get(k)) for k in ("group", "settings", "history_copy", "sync_seen", "setting_times")}
    if isinstance(event, dict) and isinstance(event.get("data"), dict):
        data = event["data"]
        if data.get("to") == "ME":
            data["to"] = me
        if event.get("type") == "device_removed" and data.get("device") == me:
            return  # wipes this device: tested in test_e2e_devices
    if copying:
        v["history_copy"] = {"from": "other", "dms": True, "communities": [], "cursor": None, "done": False}
    try:
        h.run(h.core.devices.apply_event(event))
        h.check_usable()
        h.run(h.core.devices.list())
        h.core.devices.copy_status()
    finally:
        for k, val in saved.items():
            if val is None:
                v.pop(k, None)
            else:
                v[k] = val


# ---------------------------------------------------------------- the mailbox read


class Nodes:
    """Stands in for the maintainer nodes' HTTP replies to the mailbox read."""

    def __init__(self, replies: dict[str, bytes]):
        self.replies = replies
        self.deleted: list[tuple[str, list[int]]] = []

    def __call__(self, base, method, path, body, limit=1 << 30):
        if path == "/v1/mailbox/delete":
            self.deleted.append((base, list(mp.MailboxDelete.FromString(body).ids)))
            return b""
        assert path == "/v1/mailbox/fetch", path
        return self.replies[base]


@FUZZ
@given(fuzzed(b"", max_size=300))
def test_mailbox_read_survives_a_bad_node(bad_page):
    """One maintainer node answers the mailbox read with garbage; the next
    node's message is still read, and deleted from that node."""
    h = harness()
    h.reset()
    good = mp.MailboxItems(
        items=[mp.MailboxItem(id=7, blob=h.megolm_blob(h.event_header(text=dmp.DmText(text="from node b"))))],
        epoch=1,
    ).SerializeToString()
    nodes = Nodes({"https://a.invalid": bad_page, "https://b.invalid": good})
    meta = h.core.meta
    meta.urls, meta._request = list(nodes.replies), nodes
    h.dms._cursor.clear()
    h.dms._epochs.clear()
    try:
        h.run(h.dms.poll_once())
    finally:
        meta.urls = []
        del meta._request
    assert any(m["text"] == "from node b" for m in h.dms.history(h.gid))
    assert ("https://b.invalid", [7]) in nodes.deleted
    h.check_usable()


def test_harness_reaches_the_handlers():
    """The fuzz targets above reach past the cryptography: the attacker's
    valid events and payloads take effect."""
    h = harness()
    h.reset()
    before = len(h.dms.history(h.gid))
    h.run(h.dms.on_blob(h.megolm_blob(h.event_header(text=dmp.DmText(text="plain")))))
    assert len(h.dms.history(h.gid)) == before + 1
    h.run(h.dms.on_blob(h.megolm_blob(h.event_header(typing=dmp.DmTyping(typing=True)))))
    assert h.dms.info(h.gid)["typing"] == ["Attacker"]
    nxt = h.group.successor()
    nxt.name = "Renamed"
    h.run(h.dms.on_blob(h.megolm_blob(h.event_header(state=groups.sign_state(h.a, nxt)))))
    assert h.dms.info(h.gid)["name"] == "Renamed"
    h.reset()
    h.run(h.dms.on_blob(h.olm_blob(h.olm_header(state_update=groups.sign_state(h.a, nxt)))))
    assert h.dms.info(h.gid)["name"] == "Renamed"
    h.reset()


# ---------------------------------------------------------------- community statuses (statuses.py)

S_CID = "s" * 16


class StatusHarness:
    """The victim in a community where the attacker is a member with a status
    device: an entry signed by the attacker's member key, an Olm session to
    the victim's status account and a Megolm session for its audience, so
    fuzzed entries, key drops and status plaintexts reach the checks behind
    the cryptography."""

    def __init__(self) -> None:
        h = harness()
        core = h.core
        iid = core._new_identity("member", "status fuzz")
        core.vault.data["communities"][S_CID] = {"name": "S", "status": "left", "identity": iid}
        self.s = s = core.statuses
        self.label = label = f"status:{S_CID}"
        self.member = SigningKey(bytes([9]) * 32)
        self.a_olm = OlmStore(h.dir / "attacker-status.bin", os.urandom(32), os.urandom(32))
        kid, key = self.a_olm.fallback_key(label)
        self.a_curve = self.a_olm.curve(label)
        fk = dmp.OneTimeKey(
            curve25519=self.a_curve,
            key_id=kid,
            key=key,
            fallback=True,
            signature=self.a_olm.signed_otk(label, kid, key, True),
        )
        dev = dmp.StatusDevice(
            member=self.member.public, curve25519=self.a_curve, ed25519=self.a_olm.ed(label), fallback_key=fk
        )
        raw = dev.SerializeToString()
        signed = dmp.SignedStatusDevice(device=raw, signature=self.member.sign(keys.CTX_STATUS_DEVICE, raw))
        self.entry = signed.SerializeToString()
        s._entry(S_CID, cp.StatusEntry(member=self.member.public, devices=[self.entry]))
        assert s.devices[S_CID][dm.b64(self.member.public)], "the attacker's status device wasn't accepted"
        self.v_curve = s.olm.curve(label)
        self.v_fallback = s.olm.fallback_key(label)[1]
        self.victim_member = core.identity_public(iid)
        self.aud = bytes(range(32, 48))
        self.a_olm.rotate(label, self.aud)
        sid, exported = self.a_olm.exported_key(label, self.aud)
        good = self.header()
        good.status_key.CopyFrom(dmp.StatusKey(audience=self.aud, session_id=sid, session_key=exported))
        s._take_keys(S_CID, [self.key_item(good.SerializeToString(), 1)])
        assert sid in s.olm.inbound, "the attacker's status key wasn't accepted"

    def header(self) -> dmp.OlmPayload:
        return dmp.OlmPayload(
            sender_identity=self.member.public,
            sender_ed25519=self.a_olm.ed(self.label),
            recipient_identity=self.victim_member,
            recipient_ed25519=self.s.olm.ed(self.label),
            at=1,
        )

    def key_item(self, payload: bytes, item_id: int) -> cp.StatusKeyItem:
        mtype, ct = self.a_olm.encrypt_olm(self.label, self.v_curve, payload, one_time_key=self.v_fallback)
        env = dmp.Envelope(
            olm=dmp.OlmEnvelope(
                sender_curve25519=self.a_curve, recipient_curve25519=self.v_curve, type=mtype, ciphertext=ct
            )
        )
        return cp.StatusKeyItem(
            id=item_id,
            sender=self.member.public,
            device=self.v_curve,
            envelope=statuses.MAGIC + env.SerializeToString(),
        )

    def event(self, **body) -> dmp.DmEvent:
        return dmp.DmEvent(
            id=os.urandom(16),
            at=1_700_000_000_000,
            group_id=self.aud,
            sender=self.member.public,
            sender_device=self.a_curve,
            **body,
        )

    def blob(self, plaintext: bytes) -> bytes:
        sid, ct = self.a_olm.encrypt_megolm(self.label, self.aud, plaintext)
        env = dmp.Envelope(megolm=dmp.MegolmEnvelope(sender_curve25519=self.a_curve, session_id=sid, ciphertext=ct))
        return statuses.MAGIC + env.SerializeToString()

    def usable(self) -> None:
        json.dumps(self.s.view())


@functools.cache
def status_harness() -> StatusHarness:
    return StatusHarness()


@FUZZ
@given(fuzzed(b"", max_size=400), st.lists(fuzzed(b"", max_size=300), max_size=4))
def test_status_entries_from_the_server(blob, devices):
    """Whatever a community server pushes as a member's status entry
    (devices and blob) is taken without raising; only signed devices count."""
    t = status_harness()
    t.s._entry(S_CID, cp.StatusEntry(member=os.urandom(32), devices=devices, blob=blob))
    t.s._entry(S_CID, cp.StatusEntry(member=t.member.public, devices=[t.entry, *devices], blob=blob))
    assert len(t.s.devices[S_CID][dm.b64(t.member.public)]) == 1
    t.usable()


@FUZZ
@given(
    fuzzed(
        dmp.OlmPayload(
            status_key=dmp.StatusKey(audience=bytes(16), session_id="x", session_key="y")
        ).SerializeToString(),
        max_size=500,
    )
)
def test_status_key_drops(tail):
    """Status key drops the attacker encrypts to the victim's status account
    over a real Olm session, valid or fuzzed: nothing raises, and the drop is
    acknowledged."""
    t = status_harness()
    item = t.key_item(t.header().SerializeToString() + tail, 9)
    assert 9 in t.s._take_keys(S_CID, [item])
    t.usable()


@FUZZ
@given(
    fuzzed(dmp.DmEvent(status=dmp.DmStatus(state=dmp.STATUS_STATE_IDLE, text="x")).SerializeToString(), max_size=500)
)
def test_status_plaintexts(tail):
    """Status events in the attacker's real Megolm session, valid or fuzzed:
    nothing raises, and what's shown is within the limits."""
    t = status_harness()
    plaintext = t.event().SerializeToString() + tail
    t.s._entry(S_CID, cp.StatusEntry(member=t.member.public, devices=[t.entry], blob=t.blob(plaintext)))
    r = t.s.received.get(S_CID, {}).get(dm.b64(t.member.public))
    if r is not None:
        assert len(r["text"]) <= 128 and len(r["emoji"]) <= 32
        assert r["state"] in ("online", "idle", "dnd", "offline")
    t.usable()


def test_status_harness_reaches_the_checks():
    t = status_harness()
    t.s.received.get(S_CID, {}).clear()
    good = t.event(status=dmp.DmStatus(state=dmp.STATUS_STATE_DND, text="busy", emoji="🎧"))
    t.s._entry(S_CID, cp.StatusEntry(member=t.member.public, devices=[t.entry], blob=t.blob(good.SerializeToString())))
    assert t.s.received[S_CID][dm.b64(t.member.public)]["text"] == "busy"
