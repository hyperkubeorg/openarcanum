"""Direct messages and group DMs (docs/protocol.md §9).

A 1:1 conversation is a group with two members; there is one code path.

Each DM identity (an Ed25519 key, one per community by default) has a
signed list of devices, and each device has its own vodozemac Account per
identity (`olm.py`). Olm sessions between devices carry Megolm session keys
("room keys"); Megolm carries every event.

A group's member list, owner and settings are an owner-signed chain of
states (`groups.py`) that every member's client verifies.

A sender starts a new Megolm session whenever the set of recipient devices
changes (membership or any device list), after 100 messages, and after a
week. New members and devices only ever receive the current session, so
they can't read anything from before they joined.

Events go live through the group's relay topic, and into each recipient
device's mailbox inbox for this group (its first-contact inbox until it has
announced one). Typing indicators are live only.
"""

from __future__ import annotations

import asyncio
import base64
import contextlib
import hashlib
import json
import logging
import os
import time
from dataclasses import dataclass
from typing import TYPE_CHECKING

from google.protobuf.message import DecodeError
from websockets.exceptions import WebSocketException

from openarcanum import PRODUCT_NAME, crypto, groups, keys, olm
from openarcanum import settings as settings_mod
from openarcanum.community import Offline, RpcError
from openarcanum.dmfiles import CHUNK_MAGIC, DMFiles, safe_name
from openarcanum.errors import UserError
from openarcanum.groups import Group, GroupError
from openarcanum.i18n import _
from openarcanum.keys import SigningKey
from openarcanum.meta import MetaError, contact_card_address, inbox_address, mailbox_gid
from openarcanum.olm import OlmError, OlmStore
from openarcanum.paths import atomic_write
from openarcanum.relay import Relay, RelayError
from openarcanum.wire import community_pb2 as cp
from openarcanum.wire import dm_pb2 as dm

if TYPE_CHECKING:
    from openarcanum.core import Core

log = logging.getLogger(__name__)

MAGIC = b"FDM2"
MAX_TEXT = 4000
MAX_EMOJI = 32
# Inboxes are read this often (direct messages arrive within it).
POLL_SECONDS = 5
TYPING_SECONDS = 6
# Bounds on what other people can make this client keep.
MAX_REQUESTS = 100
REQUEST_LIFETIME = 30 * 24 * 3600
EARLY_BYTES = 16 << 20
EARLY_SECONDS = 600
# Mailbox pages read per node and inbox in one poll (a page is up to 200 items).
MAX_PAGES = 10
# Every reachable maintainer node is read this often (others: the healthiest
# member of each cluster; docs/protocol.md §7.5), and global ids of handled
# items are remembered this long (mailbox_ttl), at most this many.
FULL_POLL_SECONDS = 60
# Direct messages arrive as they're dropped: each of the most recently used
# inboxes is watched (one WebSocket per inbox, so watches link inboxes no
# more than separate fetches do; docs/protocol.md §7.1) and fetched when its
# node says "check now". The rest, and anything a watch misses, come with
# the poll. A watch that fails waits a little longer each time.
# Watching is off in this release: with watches, a linked device's community
# session dropped when it acted as an owner (tests/test_e2e_owners.py
# test_owner_key_reaches_linked_device), so the poll above carries
# everything. tests/test_e2e_dm_push.py turns watches on.
WATCH_MAX = 0
WATCH_RETRY_SECONDS = (2, 5, 15, 30, 60)
WATCH_SIGNAL = b"\x01"
HANDLED_GID_SECONDS = 14 * 24 * 3600
MAX_HANDLED_GIDS = 50_000
# A sender's clock can be ahead; their timestamps never count as later than this.
MAX_CLOCK_SKEW_MS = 5 * 60 * 1000
# Top up a device's one-time keys on its community card below this many.
OTK_LOW = 20
# Up to 10 attachments with thumbnails fit; the relay's chunk limit is 1 MiB.
MAX_EVENT = 1 << 20


def b64(b: bytes) -> str:
    return keys.encode(b)


def unb64(s: str) -> bytes:
    return keys.decode(s)


def safety_number(a: bytes, b: bytes) -> str:
    """A 60-digit number both people see for their pair of identity keys, read
    aloud or compared in person. It's a display encoding of public keys, not a
    secret: SHA-512 over the two keys in sorted order, 5 digits per 3 bytes."""
    lo, hi = sorted([a, b])
    h = hashlib.sha512(b"openarcanum/safety-number/v1\x00" + lo + hi).digest()
    groups_ = [int.from_bytes(h[i : i + 3]) % 100000 for i in range(0, 36, 3)][:12]
    return " ".join(f"{g:05d}" for g in groups_)


# ---------------------------------------------------------------- cards and device lists


def device_consent(identity: bytes, curve: bytes) -> bytes:
    return keys.CTX_DM_DEVICE.encode() + b"\x00" + identity + curve


def sign_device_list(key: SigningKey, lst: dm.DmDeviceList) -> dm.SignedDmDeviceList:
    raw = lst.SerializeToString(deterministic=True)
    return dm.SignedDmDeviceList(list=raw, signature=key.sign(keys.CTX_DM_DEVICE_LIST, raw))


def open_device_list(signed: dm.SignedDmDeviceList, identity: bytes) -> dm.DmDeviceList:
    if not keys.is_valid(identity, keys.CTX_DM_DEVICE_LIST, signed.list, signed.signature):
        raise UserError(_("A device list couldn't be verified."))
    try:
        lst = dm.DmDeviceList.FromString(signed.list)
    except DecodeError:
        raise UserError(_("A device list is damaged.")) from None
    if lst.identity != identity or len(lst.devices) > 20:
        raise UserError(_("A device list belongs to someone else."))
    for d in lst.devices:
        if len(d.curve25519) != 32 or len(d.ed25519) != 32:
            raise UserError(_("A device list has a damaged key."))
        if not OlmStore.verify(d.ed25519, device_consent(identity, d.curve25519), d.device_signature):
            raise UserError(_("A device list includes a device that didn't agree to it."))
    return lst


# Device list changes (docs/protocol.md §9.1 "Device list changes"). Every
# list after the first is approved by a device of the list it replaces, with
# that device's own Account key: the identity key alone (which every linked
# device holds, a removed one too) can't change the list.
LIST_APPROVAL_CTX = b"openarcanum/dm-device-list-approval/v1\x00"
DEVICE_ADD_CTX = b"openarcanum/dm-device-add/v1\x00"
MAX_REMOVED = 256


class ListNotApproved(Exception):
    """This device can't approve a change to one of my device lists."""


def list_approval_message(identity: bytes, raw_list: bytes) -> bytes:
    return LIST_APPROVAL_CTX + identity + hashlib.sha256(raw_list).digest()


def device_add_message(identity: bytes, device_id: bytes) -> bytes:
    return DEVICE_ADD_CTX + identity + device_id


def check_list_change(old: dm.DmDeviceList | None, signed: dm.SignedDmDeviceList, new: dm.DmDeviceList) -> str | None:
    """Why `new` (already verified by open_device_list) can't follow `old`,
    the list this client holds for the identity, or None if it can.

    - Removals are for good: `removed` only grows, and no listed device is
      in it (a removed device can't come back, with its old keys or new ones
      under the same device_id).
    - After the first list this client holds, a change is approved by a
      device of `old` that the change doesn't remove: an approval over the
      whole list, or, for a device adding itself, an add certificate for
      its device_id that a listed device issued."""
    removed = set(new.removed)
    if len(new.removed) > MAX_REMOVED or any(not 1 <= len(r) <= 32 for r in new.removed):
        return "malformed removals"
    if any(d.device_id in removed for d in new.devices):
        return "lists a removed device"
    if old is None:
        return None  # the first list this client sees (trust on first use)
    if not set(old.removed) <= removed:
        return "brings back a removed device"
    approvers = {d.ed25519: d for d in old.devices if d.device_id not in removed}
    approver = approvers.get(signed.approver)
    if approver is None:
        return "not approved by a device of the current list"
    if signed.added:
        # The listed devices stay as they are, keys and names included.
        old_devs = {d.device_id: d.SerializeToString(deterministic=True) for d in old.devices}
        new_devs = {d.device_id: d.SerializeToString(deterministic=True) for d in new.devices}
        if (
            set(new_devs) - set(old_devs) != {signed.added}
            or any(new_devs.get(i) != raw for i, raw in old_devs.items())
            or removed != set(old.removed)
        ):
            return "a device adding itself changed more than that"
        msg = device_add_message(new.identity, signed.added)
    else:
        msg = list_approval_message(new.identity, signed.list)
    if not OlmStore.verify(approver.ed25519, msg, signed.approval):
        return "the approval doesn't verify"
    return None


def list_replaces(old: dm.DmDeviceList | None, signed: dm.SignedDmDeviceList, new: dm.DmDeviceList) -> bool:
    """Whether a client holding `old` takes `new` instead: an approved change
    with a higher seq, or one that removes a device `old` still lists (a
    removed device's list, adopted before the removal arrived, never
    outlives it, whatever seq it claims)."""
    if check_list_change(old, signed, new) is not None:
        return False
    if old is None or new.seq > old.seq:
        return True
    removed = set(new.removed)
    return any(d.device_id in removed for d in old.devices)


def sign_card(key: SigningKey, card: dm.ContactCard) -> dm.SignedContactCard:
    raw = card.SerializeToString(deterministic=True)
    return dm.SignedContactCard(card=raw, signature=key.sign(keys.CTX_DM_CARD, raw))


@dataclass
class Card:
    card: dm.ContactCard
    devices: dm.DmDeviceList
    signed: dm.SignedContactCard


def open_card(signed: dm.SignedContactCard) -> Card:
    try:
        card = dm.ContactCard.FromString(signed.card)
    except Exception:
        raise UserError(_("A contact card is damaged.")) from None
    if len(card.identity) != 32 or not keys.is_valid(card.identity, keys.CTX_DM_CARD, signed.card, signed.signature):
        raise UserError(_("A contact card couldn't be verified."))
    devices = open_device_list(card.devices, card.identity)
    eds = {d.curve25519: d.ed25519 for d in devices.devices}
    for fk in card.fallback_keys:
        ed = eds.get(fk.curve25519)
        if ed is None or not OlmStore.verify(ed, olm.otk_message(fk.curve25519, fk.key_id, fk.key, True), fk.signature):
            raise UserError(_("A contact card has a damaged key."))
    return Card(card, devices, signed)


CONTACT_CODE_PREFIX = "openarcanum-contact:"
# A one-week code's fallback key is replaced 7 days after it was made at the
# latest (the previous one is kept until the next replacement): a first
# message from a card older than this might go to a key that's gone.
CODE_FIRST_CONTACT_SECONDS = 7 * 24 * 3600
# A contact card is at most 64 KiB (docs/protocol.md §9.2).
MAX_CONTACT_CODE = len(CONTACT_CODE_PREFIX) + (64 << 10) * 4 // 3 + 4


PERMANENT_CODE_PREFIX = "openarcanum-id:"
PERMANENT_CODE_VERSION = 1
MAX_CODE_NODES = 4
MAX_PERMANENT_CODE = len(PERMANENT_CODE_PREFIX) + 1024
# The permanent code's card is put on its nodes again at least this often
# (each node deletes a card not replaced for 60 days).
PERMANENT_REFRESH_SECONDS = 24 * 3600


@dataclass
class PermanentCode:
    """What a permanent contact code says: whose it is (the DM identity),
    where its card is kept (the address of the owner's token) and on which
    maintainer nodes."""

    identity: bytes
    address: bytes
    urls: list[str]


def encode_permanent_code(c: PermanentCode) -> str:
    """version (1) || identity (32) || address (32) || node URLs, one per
    line || the first 4 bytes of SHA-256 of what precedes (a damaged or
    cut-off code is refused rather than read as other URLs)."""
    raw = bytes([PERMANENT_CODE_VERSION]) + c.identity + c.address + "\n".join(c.urls).encode()
    return PERMANENT_CODE_PREFIX + keys.encode(raw + hashlib.sha256(raw).digest()[:4])


def parse_permanent_code(code: str) -> PermanentCode:
    code = "".join((code or "").split())
    bad = UserError(_("That permanent contact code is damaged. Copy it again."))
    if not code.startswith(PERMANENT_CODE_PREFIX) or len(code) > MAX_PERMANENT_CODE:
        raise bad
    try:
        raw = unb64(code[len(PERMANENT_CODE_PREFIX) :])
        raw, check = raw[:-4], raw[-4:]
        if len(raw) < 66 or hashlib.sha256(raw).digest()[:4] != check:
            raise ValueError("damaged")
        urls = raw[65:].decode().split("\n")
    except Exception:
        raise bad from None
    if len(raw) < 65 or raw[0] != PERMANENT_CODE_VERSION or not 1 <= len(urls) <= MAX_CODE_NODES:
        raise bad
    if not all(u.startswith("https://") and len(u) <= 200 for u in urls):
        raise bad
    return PermanentCode(raw[1:33], raw[33:65], urls)


def parse_contact_code(code: str) -> Card:
    """The verified card in a contact code. A card published in a community
    (bound to a membership there) isn't a contact code."""
    code = "".join((code or "").split())
    if not code.startswith(CONTACT_CODE_PREFIX) or len(code) > MAX_CONTACT_CODE:
        raise UserError(_("That isn't a contact code. It starts with {prefix}").format(prefix=CONTACT_CODE_PREFIX))
    try:
        signed = dm.SignedContactCard.FromString(unb64(code[len(CONTACT_CODE_PREFIX) :]))
    except Exception:
        raise UserError(_("That contact code is damaged. Copy it again.")) from None
    card = open_card(signed)
    if card.card.member or not card.card.inboxes:
        raise UserError(_("That isn't a contact code. It starts with {prefix}").format(prefix=CONTACT_CODE_PREFIX))
    return card


# The fields of a DM history entry and their types. This device writes
# them; the history copy from another of my devices (docs/protocol.md
# §10.3) brings entries
# that are checked against this before they're kept, so a damaged or
# mismatched entry can't break the conversation it lands in.
ENTRY_FIELDS: dict[str, type] = {
    "id": str,
    "at": int,
    "recv_at": int,
    "sender": str,
    "name": str,
    "text": str,
    "reply_to": str,
    "mentions_me": bool,
    "mine": bool,
    "reactions": dict,
    "pinned": bool,
    "files": list,
    "deleted": bool,
    "edited_at": int,
    "system": bool,
}
FILE_FIELDS: dict[str, type] = {
    "hash": str,
    "key": str,
    "name": str,
    "ciphertext_size": int,
    "mime": str,
    "size": int,
    "width": int,
    "height": int,
    "thumb": str,
    "state": str,
    "nodes": list,
    "token": str,
    "expires_at": int,
}


def _typed(d: dict, fields: dict[str, type]) -> bool:
    # bool is an int in Python: an int field must not be a bool, and the reverse.
    return all(k not in d or type(d[k]) is t for k, t in fields.items())


def valid_entry(m: object) -> bool:
    """True if `m` is a well-formed history entry (unknown extra fields, from
    a newer version, are allowed)."""
    if not isinstance(m, dict) or not _typed(m, ENTRY_FIELDS):
        return False
    if not m.get("id") or "at" not in m or "sender" not in m:
        return False
    reactions = m.get("reactions", {})
    if not all(
        isinstance(e, str) and isinstance(w, list) and all(isinstance(x, str) for x in w) for e, w in reactions.items()
    ):
        return False
    for f in m.get("files", []):
        if not isinstance(f, dict) or not _typed(f, FILE_FIELDS) or not {"hash", "name", "state"} <= set(f):
            return False
        if f["state"] not in ("available", "waiting", "expired") or not all(
            isinstance(n, str) for n in f.get("nodes", [])
        ):
            return False
    return True


def check_otk(otk: dm.OneTimeKey, devices: dm.DmDeviceList) -> bool:
    for d in devices.devices:
        if d.curve25519 == otk.curve25519:
            return OlmStore.verify(
                d.ed25519, olm.otk_message(otk.curve25519, otk.key_id, otk.key, otk.fallback), otk.signature
            )
    return False


class DMHistory:
    """DM history, encrypted in the data directory. It's the only copy (no
    server keeps DMs), so the cache limit doesn't prune it."""

    MAGIC = b"OARCDM02"

    def __init__(self, path, key: bytes):
        self.path = path
        self.key = key
        self.data: dict[str, list[dict]] = {}
        try:
            blob = path.read_bytes()
        except FileNotFoundError:
            return
        # The magic ends in the format version; a file from a newer version
        # is refused, never replaced.
        if blob[:6] == self.MAGIC[:6] and blob[6:8].isdigit() and blob[6:8] > self.MAGIC[6:8]:
            raise UserError(
                _(
                    "Your direct message history was saved by a newer version of OpenArcanum. "
                    "Update OpenArcanum to read it."
                )
            )
        try:
            self.data = json.loads(crypto.open_sealed(key, blob[len(self.MAGIC) :], self.MAGIC))
        except (crypto.DecryptError, json.JSONDecodeError) as e:
            raise UserError(_("Your direct message history file is damaged and can't be read.")) from e

    def save(self) -> None:
        atomic_write(self.path, self.MAGIC + crypto.seal(self.key, json.dumps(self.data).encode(), self.MAGIC))

    def find(self, gid: str, eid: str) -> dict | None:
        for m in self.data.get(gid, []):
            if m["id"] == eid:
                return m
        return None


class DMManager:
    def __init__(self, core: Core):
        self.core = core
        self.store: DMHistory | None = None
        self.olm: OlmStore | None = None
        self.relays: dict[str, Relay] = {}
        self.typing: dict[str, dict[str, float]] = {}
        self._poll: asyncio.Task | None = None
        # Mailbox watches running, by inbox token (_sync_watches).
        self._watches: dict[bytes, asyncio.Task] = {}
        self._locks: dict[str, asyncio.Lock] = {}
        self._subscribed: set[tuple[str, str]] = set()
        # Events whose room key hasn't arrived yet (it travels separately,
        # over Olm), retried when it does. Bounded. Entries are
        # (envelope, source, received, size); source is (node, token, item
        # id) for a mailbox item, which stays on its node until handled.
        self._waiting: dict[str, list[tuple]] = {}
        # Mailbox read position per (node, token) for this run; moved back
        # to fetch again what's still on a node unhandled (docs/protocol.md §7.1).
        self._cursor: dict[tuple[str, bytes], int] = {}
        # Each node's mailbox epoch as last seen: a node restored from a
        # backup has a new one, and its ids may have gone back, so every
        # read position on it starts over.
        self._epochs: dict[str, int] = {}
        # Mailbox items left on their node with nowhere to hold them, and
        # when: read again after EARLY_SECONDS.
        self._unheld: list[tuple[tuple[str, bytes, int], float]] = []
        # One mailbox read at a time (the poll loop and explicit polls).
        self._polling = asyncio.Lock()
        # Global ids (meta.mailbox_gid) of mailbox items handled, and when:
        # a maintainer cluster's members each hold a copy of an item, so the
        # same item read from another member is skipped before decryption
        # and deleted there too (docs/protocol.md §7.5). Bounded; kept for
        # as long as an item can live.
        self._handled_gids: dict[bytes, float] = {}
        # When every node was last read (the slow loop), and the node
        # health generation then: a failure since means read them all now.
        self._full_poll_at = 0.0
        self._full_poll_gen = -1
        # Room keys for a group we don't know yet (the invitation travels
        # separately and may arrive later), applied when it does. Bounded.
        self._early_keys: dict[str, list[tuple]] = {}
        # Events from someone still invited in this client's view of a group:
        # their acceptance lost a race with another change and is being
        # signed again. Retried when the group changes. Bounded.
        self._pending_events: dict[str, list[tuple]] = {}
        self.files = DMFiles(self)
        self._gcache: dict[str, tuple] = {}
        self._early_bytes = 0

    # ---------------------------------------------------------------- state

    @property
    def d(self) -> dict:
        v = self.core.vault
        assert v is not None
        return v.data.setdefault(
            "dm", {"ids": {}, "groups": {}, "contacts": {}, "peers": {}, "pins": {}, "blocked": []}
        )

    def _lock(self, gid: str) -> asyncio.Lock:
        return self._locks.setdefault(gid, asyncio.Lock())

    async def opened(self) -> None:
        v = self.core.vault
        assert v is not None
        if "olm" not in v.data:
            v.data["olm"] = {"seal": b64(crypto.random_key()), "pickle": b64(crypto.random_key())}
            v.data["dm_history_key"] = b64(crypto.random_key())
            self.core.save_vault()
        self.olm = OlmStore(
            self.core.paths.data / "olm.bin", unb64(v.data["olm"]["seal"]), unb64(v.data["olm"]["pickle"])
        )
        self.store = DMHistory(self.core.paths.data / "dms.bin", unb64(v.data["dm_history_key"]))
        await self.ensure_sync()
        self._poll = self.core.spawn(self._poll_loop())
        for gid in list(self.d["groups"]):
            self.core.spawn(self._subscribe(gid))
        for pub in list(self.d["ids"]):
            self.core.spawn(self._ensure_listed(pub))
        self.files.resume()

    async def shutdown(self) -> None:
        if self._poll is not None:
            self._poll.cancel()
        for task in self._watches.values():
            task.cancel()
        self._watches.clear()
        for r in self.relays.values():
            await r.close()
        self.relays.clear()
        self._subscribed.clear()

    # ---------------------------------------------------------------- my DM identities

    def _ident(self, cid: str, create: bool) -> str | None:
        """The DM identity (b64 public key) this profile uses in a community,
        or with no community (""), the one behind its contact code."""
        if not cid:
            pub = self.d.get("contact_identity")
            if pub and pub in self.d["ids"]:
                return pub
            if not create:
                return None
            pub = self._new_ident(_("Contact code"))
            self.d["contact_identity"] = pub
            self.core.save_vault()
            return pub
        rec = self.core.vault.data["communities"].get(cid)
        if rec is None:
            raise UserError(_("Unknown community."))
        pub = rec.get("dm_identity")
        if pub and pub in self.d["ids"]:
            return pub
        if not create:
            return None
        pub = self._new_ident(rec.get("name", ""))
        rec["dm_identity"] = pub
        self.core.save_vault()
        return pub

    def _new_ident(self, label: str) -> str:
        self.core.require_unlocked()
        k = SigningKey.generate()
        iid = f"dm-{b64(k.public)[:16]}"
        self.core.vault.data["identities"][iid] = {
            "seed": b64(k.seed),
            "kind": "dm",
            "created": int(time.time()),
            "label": label,
        }
        pub = b64(k.public)
        self.d["ids"][pub] = {"iid": iid, "list": "", "extras": {}, "inbox_token": b64(os.urandom(32))}
        self.core.vault.keys_changed()
        return pub

    def _signing(self, pub: str) -> SigningKey:
        ident = self.core.vault.data["identities"].get(self.d["ids"][pub]["iid"], {})
        if "seed" not in ident:
            raise UserError(_("Unlock your profile first."))
        return SigningKey(unb64(ident["seed"]))

    def _label(self, pub: str) -> str:
        return f"dm:{pub}"

    def _my_list(self, pub: str) -> dm.DmDeviceList | None:
        raw = self.d["ids"].get(pub, {}).get("list")
        if not raw:
            return None
        return dm.DmDeviceList.FromString(dm.SignedDmDeviceList.FromString(unb64(raw)).list)

    def _my_device(self, pub: str) -> dm.DmDevice:
        label = self._label(pub)
        curve = self.olm.curve(label)
        return dm.DmDevice(
            device_id=bytes.fromhex(self.core.vault.data["device_id"]),
            curve25519=curve,
            ed25519=self.olm.ed(label),
            # No name: the list goes out in contact cards and contact codes,
            # and device names stay between my own devices (devices.py).
            added_at=int(time.time()),
            device_signature=self.olm.sign(label, device_consent(unb64(pub), curve)),
        )

    async def _ensure_listed(self, pub: str) -> None:
        """Make sure this device is in the identity's signed device list, and
        its fallback key and inbox are known to the other devices."""
        if not self.core.unlocked:
            return
        label = self._label(pub)
        ent = self.d["ids"][pub]
        curve = self.olm.curve(label)
        kid, key = self.olm.fallback_key(label)
        fk = dm.OneTimeKey(
            curve25519=curve, key_id=kid, key=key, fallback=True, signature=self.olm.signed_otk(label, kid, key, True)
        )
        extra = {"fallback": b64(fk.SerializeToString()), "inbox": b64(inbox_address(unb64(ent["inbox_token"])))}
        changed = ent.setdefault("extras", {}).get(b64(curve)) != extra
        ent["extras"][b64(curve)] = extra
        lst = self._my_list(pub)
        if lst is None or curve not in {d.curve25519 for d in lst.devices}:
            new = dm.DmDeviceList(identity=unb64(pub), seq=(lst.seq + 1) if lst else 1, issued_at=int(time.time()))
            if lst is not None:
                new.devices.extend(lst.devices)
                new.removed.extend(lst.removed)
            new.devices.append(self._my_device(pub))
            try:
                await self._set_my_list(pub, new, lst)
            except ListNotApproved:
                # Not approved to join this identity's list (yet): another
                # of my devices certifies this one (_issue_certs).
                log.info("dm: this device isn't certified for one of the identities yet")
                return
            if lst is None:
                # A new identity: my other devices may join its list.
                others = self.core.vault.data.get("group", {}).get("devices", {})
                self._issue_certs(pub, [d for d in others if d != self._me_hex()])
            changed = True
        if changed:
            self.core.save_vault()
            await self.core.devices.broadcast("dm_identity", self._identity_sync(pub))
            await self._republish(pub)

    def _first_device(self) -> bool:
        """Whether this is the first of my devices (the lowest id)."""
        others = self.core.vault.data.get("group", {}).get("devices", {})
        return all(self._me_hex() <= d for d in others)

    def _me_hex(self) -> str:
        return self.core.vault.data["device_id"]

    async def _set_my_list(self, pub: str, lst: dm.DmDeviceList, prev: dm.DmDeviceList | None) -> None:
        """Sign and keep a new list for one of my identities. A change to an
        existing list is approved by this device (check_list_change): with
        its own Account key if it's in `prev`, else with the add certificate
        another of my devices issued for it."""
        signed = sign_device_list(self._signing(pub), lst)
        if prev is not None:
            label = self._label(pub)
            me = bytes.fromhex(self._me_hex())
            if any(d.device_id == me and d.ed25519 == self.olm.ed(label) for d in prev.devices):
                signed.approver = self.olm.ed(label)
                signed.approval = self.olm.sign(label, list_approval_message(unb64(pub), signed.list))
            else:
                cert = self.d["ids"][pub].get("certs", {}).get(self._me_hex())
                if not cert:
                    raise ListNotApproved
                signed.added = me
                signed.approver = unb64(cert["approver"])
                signed.approval = unb64(cert["cert"])
        if check_list_change(prev, signed, lst) is not None:
            raise ListNotApproved
        self.d["ids"][pub]["list"] = b64(signed.SerializeToString())
        self.core.save_vault()
        self._remember_list(unb64(pub), signed)

    def _issue_certs(self, pub: str, device_ids: list[str]) -> None:
        """Certify my other devices (device_id, hex) to add themselves to
        this identity's list; this device must be in it."""
        lst = self._my_list(pub)
        label = self._label(pub)
        if lst is None or not self.olm.has_account(label):
            return
        me = self.olm.ed(label)
        if me not in {d.ed25519 for d in lst.devices}:
            return
        removed = set(lst.removed)
        certs = self.d["ids"][pub].setdefault("certs", {})
        for did in device_ids:
            try:
                raw = bytes.fromhex(did)
            except ValueError:
                continue
            if not 1 <= len(raw) <= 32 or raw in removed:
                continue
            certs[did] = {"approver": b64(me), "cert": b64(self.olm.sign(label, device_add_message(unb64(pub), raw)))}
        self.core.save_vault()

    def certify_device(self, device_id: str) -> None:
        """A device being linked (devices.existing_approve): certify it for
        every identity whose list this device is in."""
        for pub in list(self.d["ids"]):
            self._issue_certs(pub, [device_id])

    def _identity_sync(self, pub: str) -> dict:
        ent = self.d["ids"][pub]
        iid = ent["iid"]
        return {
            "pub": pub,
            "identity": {iid: self.core.vault.data["identities"].get(iid, {})},
            "list": ent.get("list", ""),
            "extras": ent.get("extras", {}),
            "certs": ent.get("certs", {}),
        }

    def apply_identity_sync(self, data: dict) -> None:
        """Another of my devices changed an identity's device list or extras."""
        pub = data["pub"]
        for iid, ident in data.get("identity", {}).items():
            if ident.get("seed"):
                self.core.vault.data["identities"].setdefault(iid, ident)
        ent = self.d["ids"].setdefault(
            pub,
            {
                "iid": next(iter(data.get("identity", {})), ""),
                "list": "",
                "extras": {},
                "inbox_token": b64(os.urandom(32)),
            },
        )
        # Other devices' extras; never this device's own (theirs may be stale).
        here = b64(self.olm.curve(self._label(pub))) if self.olm.has_account(self._label(pub)) else ""
        for curve, extra in data.get("extras", {}).items():
            if curve != here and ent["extras"].get(curve) != extra:
                ent["extras"][curve] = extra
        for did, cert in data.get("certs", {}).items():
            if isinstance(cert, dict) and {"approver", "cert"} <= set(cert):
                ent.setdefault("certs", {}).setdefault(did, cert)
        list_changed = False
        if data.get("list"):
            try:
                theirs = dm.SignedDmDeviceList.FromString(unb64(data["list"]))
                lst = open_device_list(theirs, unb64(pub))
            except UserError, DecodeError, ValueError:
                return
            mine = self._my_list(pub)
            removed = set(self.d.get("removed_devices", {}).get(pub, []))
            if any(b64(d.curve25519) in removed for d in lst.devices):
                return
            if list_replaces(mine, theirs, lst):
                ent["list"] = data["list"]
                list_changed = True
            elif (
                mine is not None
                and lst.seq == mine.seq
                and theirs.SerializeToString() != unb64(ent["list"])
                and check_list_change(mine, theirs, lst) is None
            ):
                # Two of my devices changed the list at once: merge.
                self.core.spawn(self._merge_lists(pub, lst))
            elif mine is not None and theirs.SerializeToString() != unb64(ent["list"]) and lst.seq >= mine.seq:
                log.warning("dm: refused a device list change for one of my identities")
        self.core.save_vault()
        if list_changed:
            for g in self.d["groups"].values():
                if g["me"] == pub:
                    g["rekey"] = True
            lst = self._my_list(pub)
            if here and lst is not None and unb64(here) not in {d.curve25519 for d in lst.devices}:
                self.core.spawn(self._ensure_listed(pub))
            self.core.spawn(self._announce_card_everywhere(pub))

    async def _merge_lists(self, pub: str, other: dm.DmDeviceList) -> None:
        mine = self._my_list(pub)
        if mine is None or not self.core.unlocked:
            return
        removed = set(self.d.get("removed_devices", {}).get(pub, []))
        merged = dm.DmDeviceList(identity=unb64(pub), seq=max(mine.seq, other.seq) + 1, issued_at=int(time.time()))
        gone = list(dict.fromkeys([*mine.removed, *other.removed]))[-MAX_REMOVED:]
        merged.removed.extend(gone)
        seen = set()
        for d in [*mine.devices, *other.devices]:
            if d.curve25519 in seen or b64(d.curve25519) in removed or d.device_id in set(gone):
                continue
            seen.add(d.curve25519)
            merged.devices.append(d)
        try:
            await self._set_my_list(pub, merged, mine)
        except ListNotApproved:
            return
        await self.core.devices.broadcast("dm_identity", self._identity_sync(pub))
        await self._republish(pub)

    async def remove_device(self, device_id: str) -> None:
        """A linked device was removed (docs/protocol.md §10.4): drop it from
        every identity's device list. Every group then rotates, so it receives nothing new."""
        if self.core.vault is None:
            return
        try:
            raw_id = bytes.fromhex(device_id)
        except ValueError:
            return
        for pub in list(self.d["ids"]):
            lst = self._my_list(pub)
            self.d["ids"][pub].get("certs", {}).pop(device_id, None)
            if lst is None:
                continue
            keep = [d for d in lst.devices if d.device_id != raw_id]
            if len(keep) == len(lst.devices) and raw_id in set(lst.removed):
                continue
            # Recorded in the list for good, listed or not: a removed device
            # can't add itself back (it may hold an add certificate).
            gone = [b64(d.curve25519) for d in lst.devices if d.device_id == raw_id]
            self.d.setdefault("removed_devices", {}).setdefault(pub, []).extend(gone)
            for c in gone:
                self.d["ids"][pub].get("extras", {}).pop(c, None)
            new = dm.DmDeviceList(identity=unb64(pub), seq=lst.seq + 1, issued_at=int(time.time()), devices=keep)
            new.removed.extend([*(r for r in lst.removed if r != raw_id), raw_id][-MAX_REMOVED:])
            try:
                await self._set_my_list(pub, new, lst)
            except ListNotApproved:
                # This device isn't in that identity's list (the removed one
                # made it before this one joined), so it can't take the
                # removed device out; that device holds the identity's keys
                # anyway. Retire it: communities that used it get a new one.
                log.warning("dm: retired an identity the removed device made before this one joined it")
                self._retire_identity(pub)
                continue
            await self.core.devices.broadcast("dm_identity", self._identity_sync(pub))
            await self._republish(pub)
            await self._announce_card_everywhere(pub)

    def _retire_identity(self, pub: str) -> None:
        """Forget one of my DM identities (its keys are no longer only mine):
        conversations under it are left, its contact codes stop working, and
        communities that used it publish a new one."""
        ent = self.d["ids"].pop(pub, None)
        if ent is not None:
            self.core.vault.data["identities"].pop(ent.get("iid", ""), None)
            self.core.vault.keys_changed()
        republish = []
        for cid, rec in self.core.vault.data["communities"].items():
            if rec.get("dm_identity") == pub:
                rec.pop("dm_identity", None)
                if rec.get("publish_card", True) and rec.get("status") == "active":
                    republish.append(cid)
        if self.d.get("contact_identity") == pub:
            self.d.pop("contact_identity", None)
            self.d.pop("permanent_code", None)
        for g in self.d["groups"].values():
            if g.get("me") == pub and g.get("status") != "left":
                g["status"] = "left"
        self.core.save_vault()
        for cid in republish:
            self.core.spawn(self._publish_card_quietly(cid))
        self.core.emit("dm", {})

    async def _publish_card_quietly(self, cid: str) -> None:
        with contextlib.suppress(UserError):
            await self._publish_card(cid)

    def _build_card(self, pub: str, cid: str) -> tuple[dm.SignedContactCard, list[bytes]]:
        ent = self.d["ids"][pub]
        rec = self.core.vault.data["communities"].get(cid, {}) if cid else {}
        card = dm.ContactCard(
            identity=unb64(pub),
            member=self.core.identity_public(rec["identity"]) if rec.get("identity") else b"",
            name=rec.get("display_name", "") if cid else self.core.settings.get("display_name", ""),
            devices=dm.SignedDmDeviceList.FromString(unb64(ent["list"])),
            issued_at=int(time.time()),
        )
        lst = self._my_list(pub)
        curves = [d.curve25519 for d in lst.devices] if lst else []
        for c in curves:
            extra = ent.get("extras", {}).get(b64(c))
            if extra is None:
                continue
            card.fallback_keys.append(dm.OneTimeKey.FromString(unb64(extra["fallback"])))
            card.inboxes.append(dm.DeviceInbox(curve25519=c, inbox=unb64(extra["inbox"])))
        return sign_card(self._signing(pub), card), curves

    def _cards_for(self, pub: str) -> list[str]:
        return [
            cid
            for cid, rec in self.core.vault.data["communities"].items()
            if rec.get("dm_identity") == pub and rec.get("publish_card", True) and rec.get("status") == "active"
        ]

    async def _republish(self, pub: str) -> None:
        if pub == self.d.get("contact_identity") and self.d.get("permanent_code"):
            self.core.spawn(self._publish_permanent())
        for cid in self._cards_for(pub):
            with contextlib.suppress(UserError):
                await self._publish_card(cid)

    async def _publish_card(self, cid: str) -> None:
        pub = self._ident(cid, create=True)
        if not self._my_list(pub) or b64(self.olm.curve(self._label(pub))) not in self.d["ids"][pub].get("extras", {}):
            await self._ensure_listed(pub)
        signed, curves = self._build_card(pub, cid)
        label = self._label(pub)
        conn = self.core.conn(cid)
        try:
            await conn.call(publish_contact_card=cp.PublishContactCard(card=signed.SerializeToString(), devices=curves))
            res = (await conn.call(get_contact_card=cp.GetContactCard(member=self._member_key(cid)))).contact_card
            mine = self.olm.curve(label)
            left = next((c.left for c in res.left if c.device == mine), 0)
            log.info(
                "contact card published in %s: %d devices listed, %d one-time keys left for this device",
                cid[:8],
                len(curves),
                left,
            )
            if left < OTK_LOW:
                otks = self.olm.new_one_time_keys(label, olm.ONE_TIME_KEYS)
                await conn.call(
                    add_key_packages=cp.AddKeyPackages(
                        key_packages=[
                            cp.KeyPackage(
                                device=mine,
                                package=dm.OneTimeKey(
                                    curve25519=mine,
                                    key_id=kid,
                                    key=k,
                                    signature=self.olm.signed_otk(label, kid, k, False),
                                ).SerializeToString(),
                            )
                            for kid, k in otks
                        ]
                    )
                )
        except (RpcError, Offline) as e:
            raise UserError(str(e)) from None

    def _member_key(self, cid: str) -> bytes:
        rec = self.core.vault.data["communities"][cid]
        return self.core.identity_public(rec["identity"])

    # ---------------------------------------------------------------- contact cards

    async def set_contact_card(self, cid: str, enabled: bool) -> dict:
        v = self.core.require_unlocked()
        rec = v.data["communities"].get(cid)
        if rec is None:
            raise UserError(_("Unknown community."))
        rec["publish_card"] = enabled
        if enabled:
            await self._publish_card(cid)
        else:
            try:
                await self.core.conn(cid).call(publish_contact_card=cp.PublishContactCard(card=b""))
            except (RpcError, Offline) as e:
                raise UserError(str(e)) from None
            log.info("contact card withdrawn from %s", cid[:8])
        self.core.save_vault()
        self.core.community_changed(cid)
        return {"ok": True}

    async def card_status(self, cid: str) -> dict:
        """What the community's server holds as my contact card right now:
        whether one is published there and, if so, how many one-time keys
        this device has left (the owner's own fetch only learns counts)."""
        self.core.require_unlocked()
        rec = self.core.vault.data["communities"].get(cid)
        if rec is None:
            raise UserError(_("Unknown community."))
        conn = self.core.conn(cid)
        try:
            res = (await conn.call(get_contact_card=cp.GetContactCard(member=self._member_key(cid)))).contact_card
        except Offline as e:
            raise UserError(str(e)) from None
        except RpcError as e:
            return {"published": False, "wanted": bool(rec.get("publish_card", True)), "reason": str(e)}
        card = open_card(dm.SignedContactCard.FromString(res.card))
        pub = self._ident(cid, create=False)
        mine = self.olm.curve(self._label(pub)) if pub else b""
        return {
            "published": True,
            "wanted": bool(rec.get("publish_card", True)),
            "mine": pub is not None and card.card.identity == unb64(pub),
            "devices": len(card.devices.devices),
            "keys_left": next((c.left for c in res.left if c.device == mine), 0),
        }

    async def community_online(self, cid: str) -> None:
        rec = self.core.vault.data["communities"].get(cid, {}) if self.core.vault else {}
        if rec.get("publish_card", True) and self.core.unlocked:
            if self._ident(cid, create=False) is None and not self._first_device():
                # Cards are on by default, so every device of mine comes here
                # at once: one makes the community's DM identity (devices
                # making their own would each end up in a list of its own),
                # the others get it by sync and publish with it.
                return
            try:
                await self._publish_card(cid)
            except UserError as e:
                log.info("contact card not published: %s", e)

    async def _fetch_card(self, cid: str, member: bytes, claim: bool = True) -> tuple[Card, dict[bytes, dm.OneTimeKey]]:
        conn = self.core.conn(cid)
        try:
            res = (await conn.call(get_contact_card=cp.GetContactCard(member=member))).contact_card
        except (RpcError, Offline) as e:
            log.info("contact card of member %s in %s not fetched: %s", b64(member)[:8], cid[:8], e)
            raise UserError(str(e)) from None
        card = open_card(dm.SignedContactCard.FromString(res.card))
        if card.card.member != member:
            raise UserError(_("That contact card belongs to someone else."))
        otks: dict[bytes, dm.OneTimeKey] = {}
        for kp in res.key_packages if claim else []:
            with contextlib.suppress(Exception):
                otk = dm.OneTimeKey.FromString(kp.package)
                if kp.device == otk.curve25519 and not otk.fallback and check_otk(otk, card.devices):
                    otks[otk.curve25519] = otk
        self._remember_card(card, cid, member)
        return card, otks

    def _remember_list(self, identity: bytes, signed: dm.SignedDmDeviceList) -> bool:
        """Keep a peer's device list if it's newer than the one we have. True
        if the device set changed (groups with them must rotate)."""
        lst = open_device_list(signed, identity)
        peer = self.d["peers"].setdefault(b64(identity), {"seq": 0, "list": "", "fallback": {}, "inbox": {}})
        held = None
        if peer["list"]:
            held_signed = dm.SignedDmDeviceList.FromString(unb64(peer["list"]))
            if held_signed.SerializeToString(deterministic=True) == signed.SerializeToString(deterministic=True):
                return False
            held = dm.DmDeviceList.FromString(held_signed.list)
        if not list_replaces(held, signed, lst):
            if held is not None and lst.seq >= held.seq:
                log.warning("dm: refused a device list change that isn't approved by a listed device")
            return False
        old = {d.curve25519 for d in held.devices} if held is not None else set()
        peer["seq"] = lst.seq
        peer["list"] = b64(signed.SerializeToString())
        return old != {d.curve25519 for d in lst.devices}

    def _remember_card(self, card: Card, cid: str = "", member: bytes = b"") -> bool:
        ident = card.card.identity
        changed = self._remember_list(ident, card.card.devices)
        peer = self.d["peers"][b64(ident)]
        # A card older than the one held (a community server serving a
        # stale one) can't put back a fallback key or inbox it replaced; the
        # device list above has its own order (seq).
        if card.card.issued_at < peer.get("card_at", 0):
            log.warning("dm: ignored a contact card older than the one held")
        else:
            peer["card_at"] = card.card.issued_at
            peer["card"] = b64(card.signed.SerializeToString())
            for fk in card.card.fallback_keys:
                peer["fallback"][b64(fk.curve25519)] = b64(fk.SerializeToString())
            for ib in card.card.inboxes:
                peer["inbox"][b64(ib.curve25519)] = b64(ib.inbox)
            peer["name"] = card.card.name or peer.get("name", "")
        if cid and member:
            # Identity-key change warning: this member showed a different DM
            # identity before.
            pin_key = f"{cid}:{b64(member)}"
            pinned = self.d["pins"].get(pin_key)
            if pinned and pinned != b64(ident):
                # Flag it wherever this person shows up: as a contact, and as
                # a member of conversations (contact or not).
                c = self.d["contacts"].get(pinned)
                if c is not None:
                    c["key_changed"] = b64(ident)
                self.d["peers"].setdefault(pinned, {"seq": 0, "list": "", "fallback": {}, "inbox": {}})[
                    "key_changed"
                ] = b64(ident)
                if pinned in self._verified():
                    self._verified().remove(pinned)
                self.core.emit("dm_contacts", {})
            elif not pinned:
                self.d["pins"][pin_key] = b64(ident)
        self.core.save_vault()
        if changed:
            for gid, g in self.d["groups"].items():
                if b64(ident) in self._member_ids(gid):
                    g["rekey"] = True
        return changed

    def _devices_of(self, identity: str) -> list[dm.DmDevice]:
        if identity in self.d["ids"]:
            lst = self._my_list(identity)
            return list(lst.devices) if lst else []
        peer = self.d["peers"].get(identity)
        if not peer or not peer.get("list"):
            return []
        return list(dm.DmDeviceList.FromString(dm.SignedDmDeviceList.FromString(unb64(peer["list"])).list).devices)

    # ---------------------------------------------------------------- contacts, blocks, verification

    def contacts(self) -> list[dict]:
        out = []
        for ident, c in self.d["contacts"].items():
            out.append(
                {
                    "id": ident,
                    "name": self._contact_name(ident),
                    "nickname": self._nickname(ident),
                    "card_name": self.d["peers"].get(ident, {}).get("name", ""),
                    "community": c.get("community", ""),
                    "member": c.get("member", ""),
                    "verified": ident in self._verified(),
                    "key_changed": bool(c.get("key_changed")),
                    "blocked": ident in self.d["blocked"],
                }
            )
        out.sort(key=lambda c: c["name"].lower())
        return out

    def _nicknames(self) -> dict[str, str]:
        """My names for people, by identity: kept apart from contacts, so a
        removed or blocked contact keeps theirs, and naming someone doesn't
        add them."""
        return self.d.setdefault("nicknames", {})

    def _nickname(self, ident: str) -> str:
        # Nicknames used to live in the contact entry.
        return self._nicknames().get(ident) or self.d["contacts"].get(ident, {}).get("nickname", "")

    def _contact_name(self, ident: str) -> str:
        """How a contact is shown: my nickname for them, else the name on
        their newest card, else the name they had when added."""
        c = self.d["contacts"].get(ident, {})
        return self._nickname(ident) or self.d["peers"].get(ident, {}).get("name") or c.get("name") or _("Contact")

    async def rename_contact(self, ident: str, nickname: str) -> dict:
        """A name of my own for someone (empty: their own name again), a
        contact or anyone in my conversations. It stays on my devices:
        nobody else sees it, and it doesn't make them a contact."""
        self.core.require_unlocked()
        if (
            ident not in self.d["contacts"]
            and ident not in self.d["peers"]
            and self._contact_from_groups(ident) is None
        ):
            raise UserError(_("You don't talk with that person."))
        nickname = " ".join((nickname or "").split())
        if len(nickname) > 64:
            raise UserError(_("Names can be at most {max} characters.").format(max=64))
        names = self._nicknames()
        if nickname:
            names[ident] = nickname
        else:
            names.pop(ident, None)
        c = self.d["contacts"].get(ident)
        if c is not None and c.pop("nickname", None) is not None:
            await self.core.devices.broadcast("contact_added", {"id": ident, "contact": c})
        self.core.save_vault()
        await self.core.devices.broadcast("dm_nicknames", {"nicknames": dict(names)})
        self.core.emit("dm_contacts", {})
        self.core.emit("dm", {})
        return {"ok": True}

    def apply_nicknames_sync(self, names: dict) -> None:
        self.d["nicknames"] = {k: str(v)[:64] for k, v in list(names.items())[:10000] if isinstance(k, str) and v}
        self.core.save_vault()
        self.core.emit("dm_contacts", {})
        self.core.emit("dm", {})

    def _contact_from_groups(self, ident: str) -> dict | None:
        """A contact entry for someone in one of my conversations (their card
        came with it), or None."""
        if ident in self.d["ids"]:
            return None
        for gid in self.d["groups"]:
            st = self._group(gid).state
            for m in [*st.members, *st.invited]:
                if b64(m.identity) == ident:
                    return {
                        "name": m.name or self.d["peers"].get(ident, {}).get("name") or _("Contact"),
                        "community": m.community,
                        "member": b64(m.member) if m.member else "",
                        "added_at": int(time.time()),
                    }
        return None

    async def add_contact(self, cid: str, member: str) -> dict:
        card, _member = await self._fetch_card(cid, unb64(member), claim=False)
        ident = b64(card.card.identity)
        self.d["contacts"][ident] = {
            "name": card.card.name or "Contact",
            "community": cid,
            "member": member,
            "added_at": int(time.time()),
        }
        self.core.save_vault()
        await self.core.devices.broadcast("contact_added", {"id": ident, "contact": self.d["contacts"][ident]})
        self.core.emit("dm_contacts", {})
        return {"ok": True, "id": ident}

    # ---------------------------------------------------------------- contact codes

    async def contact_code(self, name: str = "") -> str:
        """My contact code: a contact card for an identity of its own, not
        tied to any community (so it doesn't link me to one), that someone
        can add to start a direct message with me. It carries this device's
        fallback key, which is replaced every 7 days, so a code works for
        about a week; a new one is made each time."""
        self.core.require_unlocked()
        if name:
            # The name in it is the profile's display name (settings).
            await self.core.set_setting("display_name", name)
        pub = self._ident("", create=True)
        await self._ensure_listed(pub)
        signed, _curves = self._build_card(pub, "")
        return CONTACT_CODE_PREFIX + b64(signed.SerializeToString())

    # ---------------------------------------------------------------- permanent contact codes

    def permanent_code_info(self) -> dict | None:
        p = self.d.get("permanent_code")
        if not p:
            return None
        pub = self.d.get("contact_identity", "")
        token = unb64(p["token"])
        code = encode_permanent_code(PermanentCode(unb64(pub), contact_card_address(token), p["urls"]))
        return {"code": code, "created": p.get("created", 0), "published_at": p.get("published_at", 0)}

    async def permanent_code(self) -> dict:
        """My permanent contact code, made the first time: it keeps working
        until I revoke it. Its card (the identity of my contact codes) is kept
        on my maintainer nodes, replaced whenever it changes, and the code
        says where; the nodes can't change it (it's signed)."""
        self.core.require_unlocked()
        if not self.d.get("permanent_code"):
            urls = [u for u in self.core.meta.urls if u.startswith("https://")][:MAX_CODE_NODES]
            if not urls:
                raise UserError(_("A permanent code needs a maintainer node with https."))
            self._ident("", create=True)
            self.d["permanent_code"] = {"token": b64(os.urandom(32)), "urls": urls, "created": int(time.time())}
            self.core.save_vault()
        await self._publish_permanent(required=True)
        return self.permanent_code_info()

    async def revoke_permanent_code(self) -> dict:
        """Delete the card from its nodes: the code stops working for anyone
        who hasn't used it yet. People I already talk with aren't affected."""
        self.core.require_unlocked()
        p = self.d.pop("permanent_code", None)
        self.core.save_vault()
        if p:
            token = unb64(p["token"])
            for url in p["urls"]:
                try:
                    await self.core.meta.card_delete(url, token)
                except MetaError as e:
                    log.warning("permanent code not deleted from %s: %s", url, e)
        return {"ok": True}

    async def _publish_permanent(self, required: bool = False) -> None:
        p = self.d.get("permanent_code")
        pub = self.d.get("contact_identity")
        if not p or not pub or pub not in self.d["ids"] or not self.core.unlocked:
            return
        await self._ensure_listed(pub)
        signed, _curves = self._build_card(pub, "")
        raw, token, done = signed.SerializeToString(), unb64(p["token"]), 0
        for url in p["urls"]:
            try:
                await self.core.meta.card_put(url, token, raw)
                done += 1
            except MetaError as e:
                log.warning("permanent code's card not put on %s: %s", url, e)
        if done:
            p["published_at"] = int(time.time())
            self.core.save_vault()
        elif required:
            raise UserError(_("Your maintainer nodes can't be reached right now. Try again in a moment."))

    async def _refresh_permanent(self) -> None:
        p = self.d.get("permanent_code")
        if p and time.time() - p.get("published_at", 0) >= PERMANENT_REFRESH_SECONDS:
            await self._publish_permanent()

    async def _fetch_code_card(self, ref: dict, expect: bytes) -> Card:
        try:
            raw = await self.core.meta.card_get(ref["urls"], unb64(ref["address"]))
        except MetaError as e:
            raise UserError(str(e)) from None
        if raw is None:
            raise UserError(_("That permanent contact code was revoked, or its maintainer nodes can't be reached."))
        try:
            card = open_card(dm.SignedContactCard.FromString(raw))
        except UserError:
            raise UserError(_("The card behind that permanent contact code couldn't be verified.")) from None
        if card.card.identity != expect or card.card.member or not card.card.inboxes:
            raise UserError(_("The card behind that permanent contact code couldn't be verified."))
        self._remember_card(card)
        return card

    async def add_contact_code(self, code: str) -> dict:
        """Add whoever made a contact code (one-week or permanent) to my
        contacts."""
        self.core.require_unlocked()
        ref = None
        if "".join((code or "").split()).startswith(PERMANENT_CODE_PREFIX):
            pc = parse_permanent_code(code)
            ref = {"address": b64(pc.address), "urls": pc.urls}
            if b64(pc.identity) in self.d["ids"]:
                raise UserError(_("That's your own contact code."))
            card = await self._fetch_code_card(ref, pc.identity)
        else:
            card = parse_contact_code(code)
        ident = b64(card.card.identity)
        if ident in self.d["ids"]:
            raise UserError(_("That's your own contact code."))
        if self._blocked(card.card.identity):
            raise UserError(_("You blocked this person. Unblock them first."))
        self._remember_card(card)
        known = self.d["contacts"].get(ident)
        self.d["contacts"][ident] = {
            "name": card.card.name or (known or {}).get("name") or "Contact",
            "community": (known or {}).get("community", ""),
            "member": (known or {}).get("member", ""),
            "added_at": (known or {}).get("added_at", int(time.time())),
        }
        if ref or (known or {}).get("code"):
            self.d["contacts"][ident]["code"] = ref or known["code"]
        self.core.save_vault()
        await self.core.devices.broadcast("contact_added", {"id": ident, "contact": self.d["contacts"][ident]})
        self.core.emit("dm_contacts", {})
        return {"ok": True, "id": ident}

    async def open_with_contact(self, ident: str) -> dict:
        """The 1:1 conversation with a contact, creating it if needed: from
        the card held for them (a contact code), or fetched again from the
        community they're a contact from."""
        self.core.require_unlocked()
        c = self.d["contacts"].get(ident)
        if c is None:
            raise UserError(_("That person isn't in your contacts."))
        if c.get("community") and c.get("member"):
            return await self.open_with_member(c["community"], c["member"])
        existing = self._direct_with(ident)
        if existing:
            return {"id": existing}
        card = await self._fresh_card(ident)
        if self._blocked(card.card.identity):
            raise UserError(_("You blocked this person. Unblock them first."))
        return await self._create("", [(card, "", b"", {})], name="", direct=True)

    async def _contact_invitee(self, ident: str) -> tuple[Card, str, bytes, dict]:
        """A contact to invite: their card fetched again from the community
        they're a contact from (while I'm in it), else the card I hold for
        them (a contact code)."""
        c = self.d["contacts"].get(ident)
        if c is None:
            raise UserError(_("That person isn't in your contacts."))
        if self._blocked(unb64(ident)):
            raise UserError(_("You blocked this person. Unblock them first."))
        cid, member = c.get("community", ""), c.get("member", "")
        rec = self.core.vault.data["communities"].get(cid, {}) if cid else {}
        if member and rec.get("status") == "active":
            card, otks = await self._fetch_card(cid, unb64(member))
            return card, cid, unb64(member), otks
        return await self._fresh_card(ident), "", b"", {}

    async def _fresh_card(self, ident: str) -> Card:
        """A contact's card to start a conversation with: fetched again
        when they gave me a permanent code (so it's current), else the one
        held (a one-week code: refused when too old)."""
        ref = self.d["contacts"].get(ident, {}).get("code")
        if ref:
            try:
                return await self._fetch_code_card(ref, unb64(ident))
            except UserError:
                # Revoked or unreachable: what's held still works while it's fresh.
                pass
        return self._held_card(ident)

    def _held_card(self, ident: str) -> Card:
        """The card held for a contact without a community: refused when
        it's too old to start a conversation with (a one-week code), since
        the first message would go to a key their devices no longer have.
        Cards come again with every conversation, so people I talk with
        stay current."""
        peer = self.d["peers"].get(ident, {})
        name = self.d["contacts"].get(ident, {}).get("name") or _("them")
        if not peer.get("card"):
            raise UserError(_("Ask {name} for a new contact code.").format(name=name))
        if time.time() - peer.get("card_at", 0) > CODE_FIRST_CONTACT_SECONDS:
            raise UserError(
                _("The contact code from {name} is more than a week old. Ask them for a new one.").format(name=name)
            )
        return open_card(dm.SignedContactCard.FromString(unb64(peer["card"])))

    def _direct_with(self, them: str) -> str | None:
        for gid, g in self.d["groups"].items():
            st = Group.from_list(g["chain"]).state
            if (
                st.direct
                and g.get("status") != "left"
                and them in [b64(m.identity) for m in [*st.members, *st.invited]]
            ):
                return gid
        return None

    async def remove_contact(self, ident: str) -> dict:
        """Their conversations, my nickname for them and any block stay."""
        c = self.d["contacts"].pop(ident, None)
        if c and c.get("nickname"):
            self._nicknames().setdefault(ident, c["nickname"])
            await self.core.devices.broadcast("dm_nicknames", {"nicknames": dict(self._nicknames())})
        self.core.save_vault()
        await self.core.devices.broadcast("contact_removed", {"id": ident})
        self.core.emit("dm_contacts", {})
        return {"ok": True}

    def _verified(self) -> list[str]:
        return self.d.setdefault("verified", [])

    def safety(self, ident: str, gid: str = "") -> dict:
        """The safety number between one of my identities and theirs: the one
        I use in a shared conversation, or in the community they're a contact from."""
        mine = ""
        if gid and gid in self.d["groups"]:
            mine = self.d["groups"][gid]["me"]
        elif ident in self.d["contacts"]:
            cid = self.d["contacts"][ident].get("community", "")
            if cid:
                mine = self.core.vault.data["communities"].get(cid, {}).get("dm_identity", "")
            else:
                mine = self.d.get("contact_identity", "")
        if not mine:
            raise UserError(_("Open a conversation with them first."))
        return {
            "number": safety_number(unb64(mine), unb64(ident)),
            "verified": ident in self._verified(),
            "key_changed": bool(self._key_changed(ident)),
        }

    async def set_verified(self, ident: str, verified: bool) -> dict:
        lst = self._verified()
        if verified and ident not in lst:
            lst.append(ident)
        elif not verified and ident in lst:
            lst.remove(ident)
        self.core.save_vault()
        await self.core.devices.broadcast("dm_verified", {"verified": list(lst)})
        self.core.emit("dm_contacts", {})
        return {"ok": True}

    def apply_verified_sync(self, verified: list[str]) -> None:
        self.d["verified"] = [v for v in verified if isinstance(v, str)][:10000]
        self.core.save_vault()
        self.core.emit("dm_contacts", {})

    def _key_changed(self, ident: str) -> str:
        c = self.d["contacts"].get(ident, {})
        return c.get("key_changed") or self.d["peers"].get(ident, {}).get("key_changed", "")

    async def accept_key_change(self, ident: str) -> dict:
        """Acknowledge someone's new identity key: a contact moves
        to the new key, unverified; for anyone else the warning is cleared and
        the new key is what's expected from now on."""
        new = self._key_changed(ident)
        if not new:
            raise UserError(_("There's no new key to accept."))
        peer = self.d["peers"].get(ident)
        if peer is not None:
            peer.pop("key_changed", None)
        for pin, val in list(self.d["pins"].items()):
            if val == ident:
                self.d["pins"][pin] = new
        c = self.d["contacts"].get(ident)
        if c is None:
            self.core.save_vault()
            self.core.emit("dm_contacts", {})
            return {"ok": True, "id": new}
        c.pop("key_changed", None)
        self.d["contacts"].pop(ident)
        self.d["contacts"][new] = c
        if c.get("community") and c.get("member"):
            self.d["pins"][f"{c['community']}:{c['member']}"] = new
        self.core.save_vault()
        await self.core.devices.broadcast("contact_removed", {"id": ident})
        await self.core.devices.broadcast("contact_added", {"id": new, "contact": c})
        self.core.emit("dm_contacts", {})
        return {"ok": True, "id": new}

    async def set_blocked(self, ident: str, blocked: bool) -> dict:
        lst = self.d["blocked"]
        if blocked and ident not in lst:
            lst.append(ident)
        elif not blocked and ident in lst:
            lst.remove(ident)
        self.core.save_vault()
        await self.core.devices.broadcast("dm_blocked", {"blocked": list(lst)})
        self.core.emit("dm_contacts", {})
        self.core.emit("dm", {})
        return {"ok": True}

    def apply_blocked_sync(self, blocked: list[str]) -> None:
        self.d["blocked"] = [b for b in blocked if isinstance(b, str)][:10000]
        self.core.save_vault()
        self.core.emit("dm_contacts", {})

    def _blocked(self, ident: bytes) -> bool:
        return b64(ident) in self.d["blocked"]

    # ---------------------------------------------------------------- groups: reading

    def _group(self, gid: str) -> Group:
        """The group's verified chain (cached while it doesn't change), with
        the states that arrived early."""
        g = self.d["groups"].get(gid)
        if g is None:
            raise UserError(_("Unknown conversation."))
        key = (len(g["chain"]), g["chain"][-1])
        cached = self._gcache.get(gid)
        if cached is None or cached[0] != key:
            cached = (key, Group.from_list(g["chain"]))
            self._gcache[gid] = cached
        base = cached[1]
        out = Group(chain=list(base.chain), state=dm.GroupState())
        out.state.CopyFrom(base.state)
        for raw in g.get("pending", []):
            with contextlib.suppress(ValueError, GroupError):
                st = dm.SignedGroupState.FromString(unb64(raw))
                out.pending.setdefault(dm.GroupState.FromString(st.state).seq, []).append(st)
        return out

    def _member_ids(self, gid: str) -> list[str]:
        g = self._group(gid)
        return [b64(m.identity) for m in g.state.members]

    def list(self) -> list[dict]:
        out = []
        for gid, g in self.d["groups"].items():
            if g.get("sync"):
                continue
            try:
                st = Group.from_list(g["chain"]).state
            except GroupError:
                continue
            me = g["me"]
            hist = self.store.data.get(gid, []) if self.store else []
            visible = [m for m in hist if not m.get("deleted")]
            others = [m for m in [*st.members, *st.invited] if b64(m.identity) != me]
            name = st.name or ", ".join(self._name_of(gid, m.identity) for m in others) or _("Just you")
            out.append(
                {
                    "id": gid,
                    "name": name,
                    "icon": ("data:image/jpeg;base64," + base64.b64encode(st.icon.thumbnail).decode())
                    if st.HasField("icon") and st.icon.thumbnail
                    else "",
                    "direct": st.direct,
                    # Who else is in it (identity keys): the UI finds a
                    # person's existing 1:1 conversation by these.
                    "peers": [b64(m.identity) for m in others],
                    "status": g.get("status", "active"),
                    "last": self._for_ui(self._named(gid, visible[-1])) if visible else None,
                    "unread": self._unread(gid),
                    "mentions": g.get("mentions", 0),
                    "blocked": st.direct and any(b64(m.identity) in self.d["blocked"] for m in others),
                }
            )
        out.sort(key=lambda d: -(d["last"]["at"] if d["last"] else 0))
        return out

    def _unread(self, gid: str) -> int:
        g = self.d["groups"][gid]
        hist = self.store.data.get(gid, []) if self.store else []
        last = g.get("read_at", 0)
        return sum(
            1
            for m in hist
            if not m.get("mine") and not m.get("deleted") and not m.get("system") and m.get("recv_at", m["at"]) > last
        )

    def info(self, gid: str) -> dict:
        g = self.d["groups"].get(gid)
        if g is None:
            raise UserError(_("Unknown conversation."))
        st = self._group(gid).state
        me = g["me"]

        def person(m: dm.GroupMember) -> dict:
            ident = b64(m.identity)
            return {
                "id": ident,
                "name": (m.name or _("You")) if ident == me else self._name_of(gid, m.identity),
                "me": ident == me,
                "owner": m.identity == st.owner,
                "contact": ident in self.d["contacts"],
                "nickname": "" if ident == me else self._nickname(ident),
                "verified": ident in self._verified(),
                "key_changed": bool(self._key_changed(ident)),
                "blocked": ident in self.d["blocked"],
                "community": m.community,
                "member": b64(m.member) if m.member else "",
            }

        return {
            "id": gid,
            "name": st.name,
            "icon": ("data:image/jpeg;base64," + base64.b64encode(st.icon.thumbnail).decode())
            if st.HasField("icon") and st.icon.thumbnail
            else "",
            "direct": st.direct,
            "status": g.get("status", "active"),
            "owner": b64(st.owner) == me,
            "members": [person(m) for m in st.members],
            "invited": [person(m) for m in st.invited],
            "members_can_add": st.members_can_add,
            "allow_attachments": st.allow_attachments,
            "can_add": not st.direct
            and g.get("status") == "active"
            and (b64(st.owner) == me or st.members_can_add)
            and len(st.members) + len(st.invited) < groups.MAX_MEMBERS,
            "max_members": groups.MAX_MEMBERS,
            "pins": [
                self._for_ui(self._named(gid, m))
                for m in (self.store.data.get(gid, []) if self.store else [])
                if m.get("pinned") and not m.get("deleted")
            ],
            "typing": [n for n, t in self.typing.get(gid, {}).items() if t > time.time()],
            "read_at": g.get("read_at", 0),
            "me": me,
        }

    def history(self, gid: str) -> list[dict]:
        """For the UI: never file keys or fetch tokens (keys stay in Python)."""
        if gid not in self.d["groups"]:
            raise UserError(_("Unknown conversation."))
        return [self._for_ui(self._named(gid, m)) for m in (self.store.data.get(gid, []) if self.store else [])]

    def _named(self, gid: str, m: dict | None) -> dict | None:
        """A message as shown: its author under the name they have now (my
        nickname for them, else their name here), so a rename reaches old
        messages too; the name it arrived with when neither is known."""
        if not m or m.get("mine") or m.get("system") or not m.get("sender"):
            return m
        ident = m["sender"]
        name = self._nickname(ident)
        if not name:
            with contextlib.suppress(Exception):
                name = self._name_of(gid, unb64(ident))
            if name in ("Someone", _("Someone")):
                name = m.get("name") or name
        return m if name == m.get("name") else dict(m, name=name)

    @staticmethod
    def _for_ui(m: dict) -> dict:
        if not m.get("files"):
            return dict(m)
        return dict(m, files=[{k: v for k, v in f.items() if k not in ("key", "token", "nodes")} for f in m["files"]])

    async def mark_read(self, gid: str) -> dict:
        g = self.d["groups"].get(gid)
        if g is None:
            raise UserError(_("Unknown conversation."))
        hist = self.store.data.get(gid, []) if self.store else []
        # Read up to now, by this device's clock (senders' clocks can't push
        # the read position into the future).
        at = max((m.get("recv_at", m["at"]) for m in hist), default=0)
        if at > g.get("read_at", 0) or g.get("mentions"):
            g["read_at"] = at
            g["mentions"] = 0
            self.core.save_vault()
            await self.core.devices.broadcast("dm_read", {"id": gid, "at": at})
            self.core.emit("dm", {"id": gid})
        return {"ok": True}

    def apply_read_sync(self, gid: str, at: int) -> None:
        g = self.d["groups"].get(gid)
        if g is not None and at > g.get("read_at", 0):
            g["read_at"] = at
            g["mentions"] = 0
            self.core.save_vault()
            self.core.emit("dm", {"id": gid})

    # ---------------------------------------------------------------- groups: creating and changing

    def _me_member(self, cid: str, pub: str, seq: int) -> dm.GroupMember:
        if not cid:
            name = self.core.settings.get("display_name", "") if pub == self.d.get("contact_identity") else ""
            return dm.GroupMember(identity=unb64(pub), name=name, joined_seq=seq)
        rec = self.core.vault.data["communities"][cid]
        return dm.GroupMember(
            identity=unb64(pub),
            name=rec.get("display_name", ""),
            community=cid,
            member=self._member_key(cid),
            joined_seq=seq,
        )

    def _dedicated_relays(self) -> set[str]:
        """Relays dedicated to one community, which carry no DM topics
        (docs/protocol.md §2 "Dedicated relays")."""
        return {u for rec in self.core.vault.data["communities"].values() for u in rec.get("dedicated_relays", [])}

    async def _pick_relay(self, cid: str) -> str:
        rec = self.core.vault.data["communities"].get(cid, {})
        dedicated = self._dedicated_relays()
        for url in rec.get("relays") or []:
            if url not in dedicated:
                return url
        # The community is on a dedicated relay: any other relay will do.
        return await self._sync_relay_url()

    async def open_with_member(self, cid: str, member: str) -> dict:
        """The 1:1 conversation with a member, creating it if needed."""
        self.core.require_unlocked()
        card, otks = await self._fetch_card(cid, unb64(member))
        them = b64(card.card.identity)
        if self._blocked(card.card.identity):
            raise UserError(_("You blocked this person. Unblock them first."))
        existing = self._direct_with(them)
        if existing:
            return {"id": existing}
        return await self._create(cid, [(card, cid, unb64(member), otks)], name="", direct=True)

    async def create_group(self, cid: str, members: list[str], name: str = "") -> dict:
        """A group DM with members of one community (more can be added later,
        including from other communities)."""
        self.core.require_unlocked()
        name = (name or "").strip()
        if len(name) > groups.MAX_NAME:
            raise UserError(_("Group names can be at most {max} characters.").format(max=groups.MAX_NAME))
        if not members:
            raise UserError(_("Pick at least one person."))
        if len(members) + 1 > groups.MAX_MEMBERS:
            raise UserError(_("A group DM can have at most {max} people.").format(max=groups.MAX_MEMBERS))
        if not cid:
            # From my contacts, with no community: the identity of my
            # contact code, unless they all came from the same community.
            cards = [await self._contact_invitee(m) for m in members]
            shared = {inv[1] for inv in cards}
            return await self._create(shared.pop() if len(shared) == 1 else "", cards, name=name, direct=False)
        cards = []
        for m in members:
            card, otks = await self._fetch_card(cid, unb64(m))
            cards.append((card, cid, unb64(m), otks))
        return await self._create(cid, cards, name=name, direct=False)

    async def _create(self, cid: str, invitees: list, name: str, direct: bool) -> dict:
        pub = self._ident(cid, create=True)
        await self._ensure_listed(pub)
        key = self._signing(pub)
        gid_b = os.urandom(16)
        st = dm.GroupState(
            group_id=gid_b,
            seq=1,
            owner=unb64(pub),
            members=[self._me_member(cid, pub, 1)],
            name=name,
            allow_attachments=True,
            direct=direct,
            relay=await self._pick_relay(cid),
            topic=os.urandom(32),
            at=int(time.time() * 1000),
        )
        g = Group.from_chain([groups.sign_state(key, st)])
        gid = gid_b.hex()
        self.d["groups"][gid] = {
            "chain": g.to_list(),
            "me": pub,
            "community": cid,
            "status": "active",
            "inbox_token": b64(os.urandom(32)),
            "created": int(time.time()),
        }
        self.core.save_vault()
        await self._subscribe(gid)
        await self._invite_cards(gid, invitees)
        await self.core.devices.broadcast("dm_group", self._group_sync(gid))
        self.core.emit("dm", {"id": gid})
        return {"id": gid}

    async def _invite_cards(self, gid: str, invitees: list) -> None:
        """Add invitees to the state, then send each of their devices the
        state chain and every member's card over Olm."""
        g = self._group(gid)
        pub = self.d["groups"][gid]["me"]
        nxt = g.successor()
        for card, cid, member, _otks in invitees:
            ident = card.card.identity
            if ident in [m.identity for m in [*nxt.members, *nxt.invited]]:
                continue
            nxt.invited.append(
                dm.GroupMember(identity=ident, name=card.card.name, community=cid, member=member, joined_seq=0)
            )
        nxt.at = int(time.time() * 1000)
        signed = groups.sign_state(self._signing(pub), nxt)
        try:
            g.apply(signed)
        except GroupError as e:
            raise UserError(str(e)) from None
        await self._commit_state(gid, g, signed)
        invite = dm.GroupInvite(chain=g.chain, cards=self._member_cards(gid))
        for card, _cid, _member, otks in invitees:
            for dev in card.devices.devices:
                await self._send_olm(
                    pub,
                    card.card.identity,
                    dev,
                    dm.OlmPayload(invite=invite),
                    otk=otks.get(dev.curve25519),
                    inbox=self._first_inbox(card.card.identity, dev.curve25519),
                )

    def _member_cards(self, gid: str) -> list[dm.SignedContactCard]:
        """Cards we know for everyone in the group, members and invitees
        (ours freshly signed), so each can reach the others' devices."""
        out = []
        g = self.d["groups"][gid]
        out.append(self._build_card(g["me"], g["community"])[0])
        st = self._group(gid).state
        for ident in [b64(m.identity) for m in [*st.members, *st.invited]]:
            if ident == g["me"]:
                continue
            raw = self.d["peers"].get(ident, {}).get("card")
            if raw:
                out.append(dm.SignedContactCard.FromString(unb64(raw)))
        return out

    async def invite(self, gid: str, cid: str, members: list[str]) -> dict:
        self.core.require_unlocked()
        info = self.info(gid)
        if not info["can_add"]:
            raise UserError(_("You can't add people to this conversation."))
        if len(info["members"]) + len(info["invited"]) + len(members) > groups.MAX_MEMBERS:
            raise UserError(_("A group DM can have at most {max} people.").format(max=groups.MAX_MEMBERS))
        if cid:
            cards = []
            for m in members:
                card, otks = await self._fetch_card(cid, unb64(m))
                cards.append((card, cid, unb64(m), otks))
        else:
            cards = [await self._contact_invitee(m) for m in members]
        async with self._lock(gid):
            await self._invite_cards(gid, cards)
        return {"ok": True}

    async def _change(self, gid: str, edit) -> None:
        """Sign and publish a change to the group state."""
        async with self._lock(gid):
            g = self._group(gid)
            pub = self.d["groups"][gid]["me"]
            nxt = g.successor()
            edit(nxt)
            nxt.at = int(time.time() * 1000)
            signed = groups.sign_state(self._signing(pub), nxt)
            try:
                g.apply(signed)
            except GroupError as e:
                raise UserError(str(e)) from None
            await self._commit_state(gid, g, signed)

    async def rename(self, gid: str, name: str) -> dict:
        name = (name or "").strip()
        if len(name) > groups.MAX_NAME:
            raise UserError(_("Group names can be at most {max} characters.").format(max=groups.MAX_NAME))
        await self._change(gid, lambda s: setattr(s, "name", name))
        return {"ok": True}

    async def set_icon(self, gid: str, path: str) -> dict:
        """The owner sets (or, with an empty path, removes) the group's icon:
        a small image made on this device, inside the signed group state."""
        from pathlib import Path

        from openarcanum.dmfiles import make_thumbnail

        if not path:
            await self._change(gid, lambda s: s.ClearField("icon"))
            return {"ok": True}
        f = Path(path).expanduser()
        if not f.is_file() or f.stat().st_size > 25 * 1024 * 1024:
            raise UserError(_("Pick an image file (up to 25 MB)."))
        thumb, w, h = await asyncio.to_thread(make_thumbnail, f.read_bytes(), 128)
        if not thumb:
            raise UserError(_("That file isn't an image this app can read."))

        def edit(s):
            s.icon.CopyFrom(dm.DmFileRef(thumbnail=thumb, mime="image/jpeg", width=w, height=h))

        await self._change(gid, edit)
        return {"ok": True}

    async def set_options(
        self, gid: str, members_can_add: bool | None = None, allow_attachments: bool | None = None
    ) -> dict:
        def edit(s):
            if members_can_add is not None:
                s.members_can_add = bool(members_can_add)
            if allow_attachments is not None:
                s.allow_attachments = bool(allow_attachments)

        await self._change(gid, edit)
        return {"ok": True}

    async def remove_member(self, gid: str, ident: str) -> dict:
        target = unb64(ident)

        def edit(s):
            keep = [m for m in s.members if m.identity != target]
            keep_inv = [m for m in s.invited if m.identity != target]
            del s.members[:], s.invited[:]
            s.members.extend(keep)
            s.invited.extend(keep_inv)

        await self._change(gid, edit)
        return {"ok": True}

    async def transfer(self, gid: str, ident: str) -> dict:
        target = unb64(ident)
        if ident not in self._member_ids(gid):
            raise UserError(_("Ownership can only go to a member."))
        await self._change(gid, lambda s: setattr(s, "owner", target))
        return {"ok": True}

    async def leave(self, gid: str) -> dict:
        g = self.d["groups"].get(gid)
        if g is None:
            raise UserError(_("Unknown conversation."))
        st = self._group(gid).state
        me = unb64(g["me"])
        if g.get("status") == "invited":
            return await self.decline(gid)
        if st.owner == me and len(st.members) > 1:
            if st.direct:
                other = next(m.identity for m in st.members if m.identity != me)

                def edit(s):
                    keep = [m for m in s.members if m.identity != me]
                    del s.members[:]
                    s.members.extend(keep)
                    s.owner = other

                await self._change(gid, edit)
            else:
                raise UserError(_("You own this group. Make someone else the owner before leaving."))
        elif len(st.members) > 1 or st.invited:

            def edit(s):
                keep = [m for m in s.members if m.identity != me]
                del s.members[:]
                s.members.extend(keep)

            await self._change(gid, edit)
        self._forget(gid)
        await self.core.devices.broadcast("dm_forget", {"id": gid})
        return {"ok": True}

    def _forget(self, gid: str) -> None:
        self.d["groups"].pop(gid, None)
        if self.store is not None:
            self.store.data.pop(gid, None)
            self.store.save()
        if self.olm is not None:
            self.olm.forget_group(bytes.fromhex(gid))
        self.core.save_vault()
        self.core.emit("dm", {"id": gid})

    async def accept(self, gid: str) -> dict:
        g = self.d["groups"].get(gid)
        if g is None or g.get("status") not in ("invited", "request"):
            raise UserError(_("There's no invitation to accept."))
        pub = g["me"]
        cid = g["community"]
        was_request = g.get("status") == "request"
        await self._ensure_listed(pub)
        group = self._group(gid)
        me = unb64(pub)

        def edit(s):
            keep = [m for m in s.invited if m.identity != me]
            del s.invited[:]
            s.invited.extend(keep)
            s.members.append(self._me_member(cid, pub, s.seq))

        g["status"] = "active"
        if not group.is_member(me):
            await self._change(gid, edit)
        self.core.save_vault()
        await self._subscribe(gid)
        self._fetch_waiting(gid)
        await self.send_event(gid, dm.DmEvent(card=self._build_card(pub, cid)[0]))
        await self._announce_inbox(gid)
        await self.core.devices.broadcast("dm_group", self._group_sync(gid))
        if was_request and group.state.direct:
            # Accepting someone's message request is agreeing to talk with
            # them: they become a contact (so they can also add me to groups).
            await self._add_requester(group.state, me)
        self.core.emit("dm", {"id": gid})
        return {"ok": True}

    async def _add_requester(self, st: dm.GroupState, me: bytes) -> None:
        other = next((m for m in [*st.members, *st.invited] if m.identity != me), None)
        if other is None:
            return
        ident = b64(other.identity)
        if ident in self.d["contacts"] or ident in self.d["ids"]:
            return
        self.d["contacts"][ident] = {
            "name": other.name or self.d["peers"].get(ident, {}).get("name") or "Contact",
            "community": other.community,
            "member": b64(other.member) if other.member else "",
            "added_at": int(time.time()),
        }
        self.core.save_vault()
        await self.core.devices.broadcast("contact_added", {"id": ident, "contact": self.d["contacts"][ident]})
        self.core.emit("dm_contacts", {})

    async def decline(self, gid: str) -> dict:
        g = self.d["groups"].get(gid)
        if g is None:
            raise UserError(_("Unknown conversation."))
        me = unb64(g["me"])
        if self._group(gid).is_invited(me):

            def edit(s):
                keep = [m for m in s.invited if m.identity != me]
                del s.invited[:]
                s.invited.extend(keep)

            with contextlib.suppress(UserError):
                await self._change(gid, edit)
        self._forget(gid)
        await self.core.devices.broadcast("dm_forget", {"id": gid})
        return {"ok": True}

    async def _commit_state(self, gid: str, g: Group, signed: dm.SignedGroupState) -> None:
        """Store a new head we made and tell the group. Anyone it removed is
        told over Olm: they're no longer sent group events."""
        old = self._group(gid).state
        self.d["groups"][gid]["chain"] = g.to_list()
        members = {m.identity for m in g.state.members}
        outside = {m.identity for m in [*old.members, *old.invited, *g.state.invited]} - members
        pub = self.d["groups"][gid]["me"]
        for ident in outside - {unb64(pub)}:
            for dev in self._devices_of(b64(ident)):
                await self._send_olm(pub, ident, dev, dm.OlmPayload(state_update=signed))
        self.d["groups"][gid]["rekey"] = True
        self.core.save_vault()
        await self.send_event(gid, dm.DmEvent(state=signed, state_tail=g.chain[-17:-1]))
        await self.core.devices.broadcast("dm_group", self._group_sync(gid))
        self._system_entry(gid, signed)
        self.core.emit("dm", {"id": gid})

    def _group_sync(self, gid: str) -> dict:
        g = self.d["groups"][gid]
        return {k: g[k] for k in ("chain", "me", "community", "status", "created") if k in g} | {"id": gid}

    def apply_group_sync(self, data: dict) -> None:
        """Another of my devices created, joined or changed a group."""
        gid = data["id"]
        mine = self.d["groups"].get(gid)
        try:
            theirs = Group.from_list(data["chain"])
        except GroupError:
            return
        if mine is None:
            if data["me"] not in self.d["ids"]:
                return
            self.d["groups"][gid] = {
                "chain": data["chain"],
                "me": data["me"],
                "community": data.get("community", ""),
                "status": data.get("status", "active"),
                "inbox_token": b64(os.urandom(32)),
                "created": data.get("created", int(time.time())),
                "rekey": True,
            }
            self.core.spawn(self._subscribe(gid))
            self.core.spawn(self._announce_inbox(gid))
        else:
            cur = Group.from_list(mine["chain"])
            for s in theirs.chain[1:]:
                with contextlib.suppress(GroupError):
                    if cur.apply(s):
                        mine["rekey"] = True
            mine["chain"] = cur.to_list()
            if data.get("status") == "active" and mine.get("status") in ("invited", "request"):
                # Accepted on another device: now this one may connect too.
                mine["status"] = "active"
                self.core.spawn(self._subscribe(gid))
                self._fetch_waiting(gid)
        self.core.save_vault()
        self.core.emit("dm", {"id": gid})

    def apply_forget_sync(self, gid: str) -> None:
        if gid in self.d["groups"]:
            self._forget(gid)

    # ---------------------------------------------------------------- sending

    async def send(self, gid: str, text: str, reply_to: str = "", files: list[str] | None = None) -> dict:
        g = self.d["groups"].get(gid)
        if g is None:
            raise UserError(_("Unknown conversation."))
        if g.get("status") != "active":
            raise UserError(_("Accept the conversation first."))
        st = self._group(gid).state
        if st.direct and any(b64(m.identity) in self.d["blocked"] for m in st.members if b64(m.identity) != g["me"]):
            raise UserError(_("You blocked this person. Unblock them to send messages."))
        text = (text or "").rstrip()
        files = list(files or [])
        if not text.strip() and not files:
            raise UserError(_("Message is empty."))
        if len(text) > MAX_TEXT:
            raise UserError(_("Messages can be at most {max} characters.").format(max=MAX_TEXT))
        if reply_to and self.store.find(gid, reply_to) is None:
            raise UserError(_("The message you're replying to is gone."))
        refs = await self.files.prepare(gid, files) if files else []
        mentions = [m.identity for m in st.members if m.name and f"@{m.name}" in text and b64(m.identity) != g["me"]]
        body = dm.DmText(
            text=text, reply_to=bytes.fromhex(reply_to) if reply_to else b"", mentions=mentions, files=refs
        )
        recipients = self._recipients(gid)
        entry = await self.send_event(gid, dm.DmEvent(text=body))
        await self.files.after_send(gid, refs, recipients)
        return self._for_ui(self._named(gid, entry)) if entry else {}

    async def edit(self, gid: str, eid: str, text: str) -> dict:
        m = self._own_message(gid, eid)
        text = (text or "").rstrip()
        if not text.strip() or len(text) > MAX_TEXT:
            raise UserError(_("Messages need 1 to {max} characters.").format(max=MAX_TEXT))
        if text == m["text"]:
            return {"ok": True}
        await self.send_event(gid, dm.DmEvent(edit=dm.DmEdit(target=bytes.fromhex(eid), text=text)))
        return {"ok": True}

    async def delete(self, gid: str, eid: str) -> dict:
        self._own_message(gid, eid)
        await self.send_event(gid, dm.DmEvent(delete=dm.DmDelete(target=bytes.fromhex(eid))))
        return {"ok": True}

    def _own_message(self, gid: str, eid: str) -> dict:
        m = self.store.find(gid, eid) if self.store else None
        if m is None or m.get("deleted"):
            raise UserError(_("That message is gone."))
        if not m.get("mine"):
            raise UserError(_("You can only change your own messages."))
        return m

    async def react(self, gid: str, eid: str, emoji: str, remove: bool = False) -> dict:
        emoji = (emoji or "").strip()
        if not emoji or len(emoji) > MAX_EMOJI:
            raise UserError(_("Pick an emoji."))
        m = self.store.find(gid, eid) if self.store else None
        if m is None or m.get("deleted"):
            raise UserError(_("That message is gone."))
        await self.send_event(
            gid, dm.DmEvent(reaction=dm.DmReaction(target=bytes.fromhex(eid), emoji=emoji, remove=remove))
        )
        return {"ok": True}

    async def pin(self, gid: str, eid: str, unpin: bool = False) -> dict:
        m = self.store.find(gid, eid) if self.store else None
        if m is None or m.get("deleted"):
            raise UserError(_("That message is gone."))
        await self.send_event(gid, dm.DmEvent(pin=dm.DmPin(target=bytes.fromhex(eid), unpin=unpin)))
        return {"ok": True}

    async def set_typing(self, gid: str, typing: bool) -> dict:
        g = self.d["groups"].get(gid)
        if g is None or g.get("status") != "active":
            return {"ok": False}
        if not self.core.settings.get("typing_indicators", True):
            return {"ok": False}
        await self.send_event(gid, dm.DmEvent(typing=dm.DmTyping(typing=typing)), live_only=True)
        return {"ok": True}

    def _recipients(self, gid: str) -> list[tuple[bytes, dm.DmDevice]]:
        """Every device of every member, plus my other devices; not this one.
        In a direct conversation the invited person is included too: a
        message request shows what was sent so they can decide."""
        g = self.d["groups"][gid]
        mine = self.olm.curve(self._label(g["me"]))
        st = self._group(gid).state
        idents = self._member_ids(gid) + ([b64(m.identity) for m in st.invited] if st.direct else [])
        out = []
        for ident in idents:
            for dev in self._devices_of(ident):
                if dev.curve25519 != mine:
                    out.append((unb64(ident), dev))
        return out

    async def send_event(self, gid: str, ev: dm.DmEvent, live_only: bool = False) -> dict | None:
        g = self.d["groups"][gid]
        pub = g["me"]
        label = self._label(pub)
        gid_b = bytes.fromhex(gid)
        ev.id = ev.id or os.urandom(16)
        ev.at = ev.at or int(time.time() * 1000)
        ev.group_id = gid_b
        ev.sender = unb64(pub)
        ev.sender_device = self.olm.curve(label)
        async with self._lock(f"send:{gid}"):
            recipients = self._recipients(gid)
            fp = b64(hashlib.sha256(b"".join(sorted(d.curve25519 for _key, d in recipients))).digest())
            if g.get("rekey") or g.get("devices_fp") != fp or self.olm.needs_rotation(label, gid_b):
                self.olm.rotate(label, gid_b)
                g["devices_fp"] = fp
                g["rekey"] = False
                self.core.save_vault()
            await self._share_room_key(gid, pub, recipients)
            sid, ct = self.olm.encrypt_megolm(label, gid_b, ev.SerializeToString())
        env = dm.Envelope(megolm=dm.MegolmEnvelope(sender_curve25519=ev.sender_device, session_id=sid, ciphertext=ct))
        blob = MAGIC + env.SerializeToString()
        entry = await self._handle_event(gid, ev, mine=True)
        await self._deliver(gid, blob, recipients, live_only)
        return entry

    async def _share_room_key(self, gid: str, pub: str, recipients: list[tuple[bytes, dm.DmDevice]]) -> None:
        label = self._label(pub)
        gid_b = bytes.fromhex(gid)
        shared = self.olm.shared_with(label, gid_b)
        todo = [(i, d) for i, d in recipients if b64(d.curve25519) not in shared]
        if not todo:
            return
        sid, skey = self.olm.current_key(label, gid_b)
        done = []
        for ident, dev in todo:
            payload = dm.OlmPayload(room_key=dm.RoomKey(group_id=gid_b, session_id=sid, session_key=skey))
            if await self._send_olm(pub, ident, dev, payload, inbox=self._device_inbox(gid, ident, dev.curve25519)):
                done.append(dev.curve25519)
        self.olm.mark_shared(label, gid_b, done)

    async def _send_olm(
        self,
        pub: str,
        ident: bytes,
        dev: dm.DmDevice,
        payload: dm.OlmPayload,
        otk: dm.OneTimeKey | None = None,
        inbox: bytes | None = None,
        live: str | None = None,
    ) -> bool:
        """Encrypt `payload` to one device and deliver it. False if there was
        no way to start a session (no one-time or fallback key known)."""
        label = self._label(pub)
        payload.sender_identity = unb64(pub)
        payload.sender_ed25519 = self.olm.ed(label)
        payload.recipient_identity = ident
        payload.recipient_ed25519 = dev.ed25519
        payload.at = int(time.time() * 1000)
        # My card goes along, so a device that hasn't seen this one yet can
        # verify it. Community-specific fields come from the identity's
        # community; the device group's identity has none.
        cid = next((c for c, r in self.core.vault.data["communities"].items() if r.get("dm_identity") == pub), "")
        if self.d["ids"].get(pub, {}).get("list"):
            payload.sender_card.CopyFrom(self._build_card(pub, cid)[0])
        key = None
        if not self.olm.has_session(label, dev.curve25519):
            if otk is None:
                otk = self._fallback_key(ident, dev.curve25519)
            if otk is None:
                log.info("no key to reach a device; it will get keys when it next publishes its card")
                return False
            key = otk.key
        try:
            mtype, ct = self.olm.encrypt_olm(label, dev.curve25519, payload.SerializeToString(), one_time_key=key)
        except OlmError as e:
            log.info("olm encrypt failed: %s", e)
            return False
        env = dm.Envelope(
            olm=dm.OlmEnvelope(
                sender_curve25519=self.olm.curve(label), recipient_curve25519=dev.curve25519, type=mtype, ciphertext=ct
            )
        )
        blob = MAGIC + env.SerializeToString()
        if live is not None:
            # Both devices are online (e.g. the history copy): the group's
            # relay topic, not a mailbox. A lost message is asked for again.
            st = self._group(live).state
            relay = await self._group_relay(live)
            if relay is not None:
                with contextlib.suppress(RelayError):
                    await relay.publish(st.topic, blob)
                    return True
        target = inbox or self._first_inbox(ident, dev.curve25519)
        if target is None:
            return False
        try:
            await self.core.meta.drop(target, blob)
        except MetaError as e:
            log.info("mailbox drop failed: %s", e)
            return False
        return True

    def _fallback_key(self, ident: bytes, curve: bytes) -> dm.OneTimeKey | None:
        """A device's published fallback key: from my own identity's extras
        for my devices, from their card for everyone else's."""
        if b64(ident) in self.d["ids"]:
            raw = self.d["ids"][b64(ident)].get("extras", {}).get(b64(curve), {}).get("fallback")
        else:
            raw = self.d["peers"].get(b64(ident), {}).get("fallback", {}).get(b64(curve))
        return dm.OneTimeKey.FromString(unb64(raw)) if raw else None

    def _first_inbox(self, ident: bytes, curve: bytes) -> bytes | None:
        if b64(ident) in self.d["ids"]:
            raw = self.d["ids"][b64(ident)].get("extras", {}).get(b64(curve), {}).get("inbox")
        else:
            raw = self.d["peers"].get(b64(ident), {}).get("inbox", {}).get(b64(curve))
        return unb64(raw) if raw else None

    def _device_inbox(self, gid: str, ident: bytes, curve: bytes) -> bytes | None:
        raw = self.d["groups"][gid].get("inboxes", {}).get(b64(curve))
        return unb64(raw) if raw else self._first_inbox(ident, curve)

    async def _deliver(self, gid: str, blob: bytes, recipients, live_only: bool) -> None:
        st = self._group(gid).state
        # A conversation not accepted yet: mailboxes only (on this device's
        # own maintainer nodes), never its relay (reachable()).
        relay = await self._group_relay(gid)
        if relay is not None:
            with contextlib.suppress(RelayError):
                await relay.publish(st.topic, blob)
        if live_only:
            return
        # Invitees get state changes too (so they see the group change), but
        # they have no room keys: only state events are useful to them, and
        # those also arrive over Olm in their invitation.
        failed = 0
        for ident, dev in recipients:
            inbox = self._device_inbox(gid, ident, dev.curve25519)
            if inbox is None:
                continue
            try:
                await self.core.meta.drop(inbox, blob)
            except MetaError as e:
                failed += 1
                log.info("mailbox drop failed: %s", e)
        if recipients and failed == len(recipients) and relay is None:
            raise UserError(_("Couldn't deliver the message: no server can be reached. Check your connection."))

    async def _announce_inbox(self, gid: str) -> None:
        g = self.d["groups"].get(gid)
        if g is None or g.get("status") != "active":
            return
        curve = self.olm.curve(self._label(g["me"]))
        inbox = inbox_address(unb64(g["inbox_token"]))
        g.setdefault("inboxes", {})[b64(curve)] = b64(inbox)
        self.core.save_vault()
        with contextlib.suppress(UserError):
            await self.send_event(gid, dm.DmEvent(inboxes=dm.DmInboxes(curve25519=curve, inbox=inbox)))

    async def _announce_card_everywhere(self, pub: str) -> None:
        for gid, g in list(self.d["groups"].items()):
            if g["me"] == pub and g.get("status") == "active":
                g["rekey"] = True
                with contextlib.suppress(UserError):
                    await self.send_event(gid, dm.DmEvent(card=self._build_card(pub, g["community"])[0]))

    # ---------------------------------------------------------------- receiving

    def reachable(self, gid: str) -> bool:
        """Whether this client may contact the nodes a conversation names (its
        relay, the nodes its attachments are stored on). Only once it's
        accepted: a message request or invitation was set up by someone
        else, possibly a stranger, and connecting to the relay or nodes they
        chose would tell them this device's address (docs/threat-model.md).
        Until then, only this device's own maintainer nodes are contacted
        (the mailbox)."""
        g = self.d["groups"].get(gid)
        return g is not None and g.get("status") == "active"

    async def _group_relay(self, gid: str) -> Relay | None:
        """The conversation's relay, or None while it isn't reachable."""
        if not self.reachable(gid):
            return None
        return await self._relay(self._group(gid).state.relay)

    async def _relay(self, url: str) -> Relay | None:
        if not url:
            return None
        r = self.relays.get(url)
        if r is not None and r.connected:
            return r
        try:
            r = await Relay(url).connect(timeout=10)
        except (RelayError, OSError, WebSocketException) as e:
            # Someone else may have chosen this relay (the group's creator):
            # whatever it answers, it's only unreachable.
            log.info("dm relay unreachable: %s", type(e).__name__)
            return None
        self.relays[url] = r
        return r

    async def _subscribe(self, gid: str) -> None:
        g = self.d["groups"].get(gid)
        if g is None:
            return
        # A conversation just made, joined or used: watch its inbox now.
        self._sync_watches()
        st = self._group(gid).state
        if (st.relay, st.topic.hex()) in self._subscribed:
            return
        relay = await self._group_relay(gid)
        if relay is None:
            return
        try:
            await relay.subscribe(st.topic, lambda _t, data: self.core.spawn(self.on_blob(data)))
            self._subscribed.add((st.relay, st.topic.hex()))
        except RelayError as e:
            log.info("dm subscribe failed: %s", e)

    async def _poll_loop(self) -> None:
        while True:
            try:
                self._sync_watches()
                await self.poll_once()
                self._sweep()
                if self.core.unlocked:
                    await self._refresh_permanent()
            except asyncio.CancelledError:
                raise
            except Exception:
                log.exception("mailbox poll failed")
            await asyncio.sleep(POLL_SECONDS)

    def _sweep(self) -> None:
        """Expire what other people can make this client hold: stalled file
        transfers, events still waiting for keys, old message requests, and
        superseded Megolm sessions."""
        self.files.sweep()
        now = time.time()
        for table in (self._waiting, self._early_keys, self._pending_events):
            for k in list(table):
                keep = [x for x in table[k] if now - x[-2] <= EARLY_SECONDS]
                self._early_bytes -= sum(x[-1] for x in table[k]) - sum(x[-1] for x in keep)
                if table is self._waiting:
                    # Still on its node: read it again later, from there.
                    for x in table[k]:
                        if x[1] is not None and now - x[-2] > EARLY_SECONDS:
                            self._read_again(x[1])
                if keep:
                    table[k] = keep
                else:
                    table.pop(k)
        due = [src for src, at in self._unheld if now - at > EARLY_SECONDS]
        self._unheld = [(src, at) for src, at in self._unheld if now - at <= EARLY_SECONDS]
        for src in due:
            self._read_again(src)
        if self.core.vault is not None:
            for gid, g in list(self.d["groups"].items()):
                if g.get("status") == "request" and now - g.get("created", now) > REQUEST_LIFETIME:
                    self._forget(gid)
            self.olm.prune_inbound()

    async def poll_once(self, full: bool | None = None) -> None:
        """Read every inbox. An item is deleted from its node once handled;
        one that can't be read yet (its room key is on a node that's down)
        stays there until it can (docs/protocol.md §7.1).

        Which nodes: every independent node, and the healthiest member of
        each maintainer cluster; and every reachable node every
        FULL_POLL_SECONDS, at once after a node failed, or with `full`
        (docs/protocol.md §7.5: a cluster isn't trusted to hold everything, since a sender may
        have reached only another member, and replication may be cut).
        Items are merged by global id, so one held by several members is
        handled once."""
        if self.core.vault is None:
            return
        async with self._polling:
            meta = self.core.meta
            if full is None:
                full = (
                    time.monotonic() - self._full_poll_at >= FULL_POLL_SECONDS
                    or meta.health.generation != self._full_poll_gen
                )
            if full:
                self._full_poll_at, self._full_poll_gen = time.monotonic(), meta.health.generation
            nodes = meta.poll_nodes(full)
            tokens = [unb64(e["inbox_token"]) for e in self.d["ids"].values()]
            tokens += [unb64(g["inbox_token"]) for g in self.d["groups"].values()]
            for token in tokens:
                for base in nodes:
                    await self._poll_node(base, token)
            self._prune_gids()

    def _watch_tokens(self) -> list[bytes]:
        """The inboxes to watch: my identities' first-contact inboxes, then
        the conversations used most recently, WATCH_MAX in all."""
        tokens = [unb64(e["inbox_token"]) for e in self.d["ids"].values()]

        def last(gid: str) -> float:
            hist = self.store.data.get(gid, []) if self.store else []
            return max([m.get("at", 0) for m in hist[-1:]] + [self.d["groups"][gid].get("created", 0) * 1000])

        live = [gid for gid, g in self.d["groups"].items() if g.get("status") != "left" and g.get("inbox_token")]
        for gid in sorted(live, key=last, reverse=True):
            tokens.append(unb64(self.d["groups"][gid]["inbox_token"]))
        return tokens[:WATCH_MAX]

    def _sync_watches(self) -> None:
        """Watch the inboxes _watch_tokens names, and only those."""
        if self.core.vault is None:
            return
        want = set(self._watch_tokens())
        for token in list(self._watches):
            if token not in want or self._watches[token].done():
                self._watches.pop(token).cancel()
        for token in want - set(self._watches):
            self._watches[token] = self.core.spawn(self._watch(token))

    async def _watch(self, token: bytes) -> None:
        """One inbox's watch: connected to the first node this client reads,
        fetching the inbox from every node it reads on each "check now"."""
        from websockets.asyncio.client import connect

        from openarcanum import version as proto_version

        failures = 0
        while True:
            nodes = self.core.meta.poll_nodes(False)
            ws = None
            if nodes:
                base = nodes[0]
                url = (
                    "ws"
                    + base.removeprefix("http")
                    + "/v1/mailbox/watch?v="
                    + proto_version.fmt(proto_version.SUPPORTED)
                )
                try:
                    ws = await connect(url, max_size=64, ping_interval=20, ping_timeout=20, open_timeout=15)
                    await ws.send(token)
                    failures = 0
                    async for msg in ws:
                        if msg == WATCH_SIGNAL:
                            await self._poll_token(token)
                except asyncio.CancelledError:
                    raise
                except Exception as e:
                    log.debug("mailbox watch ended: %s", type(e).__name__)
                finally:
                    if ws is not None:
                        with contextlib.suppress(Exception):
                            await ws.close()
            await asyncio.sleep(WATCH_RETRY_SECONDS[min(failures, len(WATCH_RETRY_SECONDS) - 1)])
            failures += 1

    async def _poll_token(self, token: bytes) -> None:
        """Fetch one inbox from the nodes this client reads, now."""
        if self.core.vault is None:
            return
        async with self._polling:
            for base in self.core.meta.poll_nodes(False):
                await self._poll_node(base, token)

    async def _poll_node(self, base: str, token: bytes) -> None:
        key = (base, token)
        for _page in range(MAX_PAGES):
            after = self._cursor.get(key, 0)
            try:
                page = await self.core.meta.fetch_page(base, token, after)
            except MetaError:
                return
            if self._epoch_changed(base, page.epoch) and after > 0:
                # The page was read from a position that no longer means
                # anything on this node: read again from the start.
                continue
            items = list(page.items)
            if not items:
                return
            done: list[int] = []
            gids: list[bytes] = []
            inbox = inbox_address(token)
            for it in items:
                self._cursor[key] = max(self._cursor.get(key, 0), it.id)
                # Computed here: a node can't make two items look like one.
                gid = mailbox_gid(inbox, bytes(it.blob))
                if gid in self._handled_gids:
                    done.append(it.id)  # handled from another member
                    continue
                if await self.on_blob(it.blob, (base, token, it.id)):
                    done.append(it.id)
                    gids.append(gid)
                    self._handled_gids[gid] = time.time()
            if done:
                with contextlib.suppress(MetaError):
                    await self.core.meta.delete(base, token, done)
            if gids:
                await self._delete_elsewhere(base, token, gids)

    async def _delete_elsewhere(self, base: str, token: bytes, gids: list[bytes]) -> None:
        """Items handled from `base` are deleted by global id on the other
        reachable members of its cluster, where they may not have been read
        (a member that's down gets the delete by replication)."""
        meta = self.core.meta
        for other in meta.cluster_of(base):
            if other != base and meta.health.usable(other):
                with contextlib.suppress(MetaError):
                    await meta.delete(other, token, [], gids)

    def _prune_gids(self) -> None:
        cutoff = time.time() - HANDLED_GID_SECONDS
        if len(self._handled_gids) > MAX_HANDLED_GIDS or any(at < cutoff for at in self._handled_gids.values()):
            keep = sorted(((at, g) for g, at in self._handled_gids.items() if at >= cutoff), reverse=True)
            self._handled_gids = {g: at for at, g in keep[:MAX_HANDLED_GIDS]}

    def _epoch_changed(self, base: str, epoch: int) -> bool:
        """Note a node's mailbox epoch; on a change (a restore from backup)
        every read position on that node starts over. True if it changed."""
        seen = self._epochs.get(base)
        self._epochs[base] = epoch
        if seen is None or seen == epoch:
            return False
        log.info("a maintainer node's mailboxes were restored; reading them again from the start")
        for k in [k for k in self._cursor if k[0] == base]:
            self._cursor[k] = 0
        return True

    def _read_again(self, source: tuple[str, bytes, int]) -> None:
        base, token, item = source
        key = (base, token)
        if key in self._cursor:
            self._cursor[key] = min(self._cursor[key], item - 1)

    async def on_blob(self, blob: bytes, source: tuple[str, bytes, int] | None = None) -> bool:
        """Handle one event, live (`source` None) or from a mailbox. False
        means it waits for its room key and must stay on its node."""
        if self.core.vault is None or self.olm is None:
            return False
        if blob.startswith(CHUNK_MAGIC):
            await self.files.on_chunk(blob)
            return True
        if not blob.startswith(MAGIC) or len(blob) > MAX_EVENT + 4096:
            return True
        try:
            env = dm.Envelope.FromString(blob[len(MAGIC) :])
        except Exception:
            log.info("dm: dropped a malformed envelope (%d bytes)", len(blob))
            return True
        try:
            if env.HasField("olm"):
                await self._on_olm(env.olm)
            elif env.HasField("megolm") and await self._on_megolm(env.megolm, source) is False:
                return False
        except (OlmError, GroupError, UserError, DecodeError) as e:
            log.info("dm: dropped an event: %s", e)
        return True

    def _label_for_curve(self, curve: bytes) -> str | None:
        for pub in self.d["ids"]:
            label = self._label(pub)
            if self.olm.has_account(label) and self.olm.curve(label) == curve:
                return label
        return None

    def _knows(self, ident: bytes) -> bool:
        """A contact, one of my identities, or someone in a group with me."""
        i = b64(ident)
        if i in self.d["contacts"] or i in self.d["ids"]:
            return True
        for gid in self.d["groups"]:
            with contextlib.suppress(GroupError, UserError):
                st = self._group(gid).state
                if ident in [m.identity for m in [*st.members, *st.invited]]:
                    return True
        return False

    def _device_known(self, ident: bytes, curve: bytes, ed: bytes) -> bool:
        return any(d.curve25519 == curve and d.ed25519 == ed for d in self._devices_of(b64(ident)))

    async def _on_olm(self, env: dm.OlmEnvelope) -> None:
        label = self._label_for_curve(env.recipient_curve25519)
        if label is None:
            return
        pub = label.removeprefix("dm:")
        pt = self.olm.decrypt_olm(label, env.sender_curve25519, env.type, env.ciphertext)
        p = dm.OlmPayload.FromString(pt)
        if p.recipient_identity != unb64(pub) or p.recipient_ed25519 != self.olm.ed(label):
            raise OlmError("payload addressed to another device")
        # Blocking drops someone's invitations and events, but not their keys:
        # their sessions keep rotating while blocked, and unblocking should
        # bring their new messages back.
        blocked = self._blocked(p.sender_identity)
        # Their card (to verify a device we haven't seen) is only kept for
        # people we already deal with; strangers' cards come with an
        # invitation, after the DM policy has had its say.
        if p.HasField("sender_card") and self._knows(p.sender_identity):
            with contextlib.suppress(UserError):
                card = open_card(p.sender_card)
                if card.card.identity == p.sender_identity:
                    self._remember_peer_card(card)
        kind = p.WhichOneof("body")
        if kind == "invite":
            if not blocked:
                await self._on_invite(pub, env.sender_curve25519, p)
            return
        if kind == "card":
            card = open_card(p.card)
            if card.card.identity == p.sender_identity and self._knows(p.sender_identity):
                self._remember_peer_card(card)
            return
        if kind == "room_key":
            # Checked against the sender's devices when it's used: a key for a
            # group we don't know yet waits for the invitation that brings the
            # sender's card.
            await self._on_room_key(pub, env.sender_curve25519, p)
            return
        if kind == "status_key":
            # A contact's status session key (statuses.py checks the sender).
            await self.core.statuses.on_dm_status_key(pub, env.sender_curve25519, p)
            return
        if not self._device_known(p.sender_identity, env.sender_curve25519, p.sender_ed25519):
            raise OlmError("message from a device that isn't in its owner's device list")
        if kind == "sync":
            if b64(p.sender_identity) != self.d.get("sync", {}).get("pub"):
                raise OlmError("sync event from outside my devices")
            await self.core.devices.apply_event(json.loads(p.sync))
        elif kind == "state_update":
            sgid = groups.open_state(p.state_update).group_id.hex()
            g = self.d["groups"].get(sgid)
            if g is not None and g["me"] == pub:
                await self._on_state(sgid, p.state_update)

    async def _on_room_key(self, pub: str, sender_curve: bytes, p: dm.OlmPayload) -> None:
        rk = p.room_key
        gid = rk.group_id.hex()
        g = self.d["groups"].get(gid)
        if g is None:
            self._hold_key(gid, pub, sender_curve, p)
            return
        if g["me"] != pub:
            return
        group = self._group(gid)
        # Invitees share their key just before their acceptance arrives;
        # their events are still checked against membership.
        known = self._device_known(p.sender_identity, sender_curve, p.sender_ed25519)
        in_group = group.is_member(p.sender_identity) or group.is_invited(p.sender_identity)
        if not (known and in_group):
            # A new member's key can arrive before the change that brings them
            # in (another member's event, read later from another mailbox).
            # It's held, bounded, and checked again when the group changes
            # (_retry_held_keys); used only once it passes these checks.
            if self._hold_key(gid, pub, sender_curve, p):
                return
            if not known:
                raise OlmError("room key from a device that isn't in its owner's device list")
            raise OlmError("room key from someone who isn't in the group")
        self.olm.add_inbound(
            self._label(pub), rk.session_id, rk.session_key, rk.group_id, sender_curve, p.sender_ed25519
        )
        handled: dict[tuple[str, bytes], list[int]] = {}
        for waiting, source, _at, wsize in self._waiting.pop(rk.session_id, []):
            self._early_bytes -= wsize
            with contextlib.suppress(OlmError, GroupError, UserError, DecodeError):
                await self._on_megolm(waiting)
            if source is not None:
                handled.setdefault(source[:2], []).append(source[2])
        for (base, token), ids in handled.items():
            with contextlib.suppress(MetaError):
                await self.core.meta.delete(base, token, ids)

    def _hold_key(self, gid: str, pub: str, sender_curve: bytes, p: dm.OlmPayload) -> bool:
        """Keep a room key that can't be checked yet (bounded; _sweep expires it)."""
        size = p.ByteSize()
        early = self._early_keys.setdefault(gid, [])
        if len(early) < 50 and len(self._early_keys) < 200 and self._early_bytes + size <= EARLY_BYTES:
            early.append((pub, sender_curve, p, time.time(), size))
            self._early_bytes += size
            return True
        if not early:
            self._early_keys.pop(gid, None)
        return False

    async def _retry_held_keys(self, gid: str) -> None:
        """The group is new or changed: check the room keys held for it again.
        A sender who is now in the group is someone this client deals with,
        so the card that came with their key is kept first."""
        for epub, ecurve, ep, _at, esize in self._early_keys.pop(gid, []):
            self._early_bytes -= esize
            if ep.HasField("sender_card") and self._knows(ep.sender_identity):
                with contextlib.suppress(UserError, DecodeError):
                    card = open_card(ep.sender_card)
                    if card.card.identity == ep.sender_identity:
                        self._remember_peer_card(card)
            with contextlib.suppress(OlmError, GroupError, UserError):
                await self._on_room_key(epub, ecurve, ep)

    def _hold_event(self, gid: str, ev: dm.DmEvent) -> bool:
        size = ev.ByteSize()
        held = self._pending_events.setdefault(gid, [])
        if len(held) < 50 and len(self._pending_events) < 200 and self._early_bytes + size <= EARLY_BYTES:
            held.append((ev, None, time.time(), size))
            self._early_bytes += size
            return True
        if not held:
            self._pending_events.pop(gid, None)
        return False

    async def _retry_pending_events(self, gid: str) -> None:
        """The group changed: handle again the events held from people who
        were still invited (dropped if they're neither invited nor members now)."""
        for ev, _source, _at, size in self._pending_events.pop(gid, []):
            self._early_bytes -= size
            with contextlib.suppress(OlmError, GroupError, UserError, DecodeError):
                await self._handle_event(gid, ev, mine=False)

    def _remember_peer_card(self, card: Card) -> None:
        if b64(card.card.identity) in self.d["ids"]:
            self._adopt_own_card(card)
            return
        self._remember_card(card)

    async def _on_invite(self, pub: str, sender_curve: bytes, p: dm.OlmPayload) -> None:
        try:
            g = Group.from_chain(list(p.invite.chain))
        except GroupError as e:
            raise OlmError(f"bad invitation: {e}") from None
        me = unb64(pub)
        st = g.state
        gid = st.group_id.hex()
        if not g.is_invited(me):
            return
        # The inviter is whoever signed the change that invited us.
        inviter = None
        for s in g.chain:
            if me in [m.identity for m in groups.open_state(s).invited]:
                inviter = s.signer
                break
        if inviter != p.sender_identity:
            raise OlmError("invitation sent by someone other than the inviter")
        cards = {}
        for sc in p.invite.cards:
            with contextlib.suppress(UserError):
                c = open_card(sc)
                if g.is_member(c.card.identity) or g.is_invited(c.card.identity):
                    cards[c.card.identity] = c
        sender_card = cards.get(p.sender_identity)
        if sender_card is None or not any(
            d.curve25519 == sender_curve and d.ed25519 == p.sender_ed25519 for d in sender_card.devices.devices
        ):
            raise OlmError("invitation from a device that isn't in the inviter's card")
        if gid in self.d["groups"]:
            # Already known (e.g. another of my devices accepted): take the state.
            mine = self.d["groups"][gid]
            cur = Group.from_list(mine["chain"])
            for s in g.chain[1:]:
                with contextlib.suppress(GroupError):
                    cur.apply(s)
            mine["chain"] = cur.to_list()
            self.core.save_vault()
            return
        cid = next((c for c, rec in self.core.vault.data["communities"].items() if rec.get("dm_identity") == pub), "")
        allowed = await self._policy_allows(p.sender_identity, cid, st)
        if st.direct:
            # Allowed senders' direct conversations are accepted right away
            # (below); others wait as a request.
            status = "invited" if allowed in ("contact", "member") else "request"
            if (
                status == "request"
                and sum(g.get("status") == "request" for g in self.d["groups"].values()) >= MAX_REQUESTS
            ):
                log.info("dm: dropped a message request: too many pending")
                return
        else:
            if allowed is None:
                log.info("dm: dropped a group invitation the DM policy doesn't allow")
                return
            status = "invited"
        for c in cards.values():
            self._remember_peer_card(c)
        self.d["groups"][gid] = {
            "chain": g.to_list(),
            "me": pub,
            "community": cid,
            "status": status,
            "inbox_token": b64(os.urandom(32)),
            "created": int(time.time()),
            "rekey": True,
        }
        self.core.save_vault()
        await self._retry_held_keys(gid)
        await self.core.devices.broadcast("dm_group", self._group_sync(gid))
        if st.direct and allowed in ("contact", "member"):
            await self.accept(gid)
        else:
            who = next((m.name for m in st.members if m.identity == p.sender_identity), "") or _("Someone")
            if st.direct:
                text = _("{name} wants to send you direct messages.").format(name=who)
            else:
                text = _("{name} invited you to a group DM.").format(name=who)
            self.core.emit("dm", {"id": gid, "request": True})
            self.core.emit("toast", {"text": text})

    async def _policy_allows(self, sender: bytes, cid: str, st: dm.GroupState) -> str | None:
        """ "contact", "member" (shares a community where their card is
        published, under the "members" policy), or None."""
        if b64(sender) in self.d["contacts"]:
            return "contact"
        if self.core.settings.get("dm_policy", "contacts") != "members":
            return None
        m = next((m for m in [*st.members] if m.identity == sender), None)
        if m is None or not m.community or m.community not in self.core.conns:
            return None
        try:
            card, _member = await self._fetch_card(m.community, m.member, claim=False)
        except UserError:
            return None
        return "member" if card.card.identity == sender else None

    async def _on_megolm(self, env: dm.MegolmEnvelope, source: tuple[str, bytes, int] | None = None) -> bool:
        """Returns False when its room key hasn't arrived: it's held (and,
        from a mailbox, left on its node)."""
        info = self.olm.inbound_for(env.session_id, env.sender_curve25519)
        if info is None and self.core.statuses.refused(env.session_id):
            # A status from someone whose status key was refused (statuses.py):
            # it will never be readable, so it isn't held.
            return None
        if info is None:
            size = env.ByteSize()
            waiting = self._waiting.get(env.session_id, [])
            if len(waiting) < 200 and len(self._waiting) < 500 and self._early_bytes + size <= EARLY_BYTES:
                self._waiting.setdefault(env.session_id, []).append((env, source, time.time(), size))
                self._early_bytes += size
            elif source is not None:
                self._unheld.append((source, time.time()))
            return False
        d = self.olm.decrypt_megolm(env.session_id, env.sender_curve25519, env.ciphertext)
        ev = dm.DmEvent.FromString(d.plaintext)
        if not self.olm.check_replay(d.session_id, d.index, ev.id, d.sender_curve):
            raise OlmError("replayed event")
        gid = d.group_id.hex()
        if ev.group_id != d.group_id or ev.sender_device != d.sender_curve:
            raise OlmError("event doesn't match its session")
        g = self.d["groups"].get(gid)
        if ev.HasField("status"):
            # A contact's status, from a status session (never a group's).
            if g is None:
                self.core.statuses.on_dm_status(d.label, d.sender_curve, d.sender_ed, ev)
            return
        if g is None:
            return
        if not self._device_known(ev.sender, d.sender_curve, d.sender_ed):
            raise OlmError("event from a device that isn't in its owner's device list")
        mine = ev.sender in [unb64(p) for p in self.d["ids"]]
        await self._handle_event(gid, ev, mine=mine)

    async def _drain_waiting(self, session_id: str, drop: bool = False) -> None:
        """A Megolm session key arrived outside a group (a contact's status
        session, statuses.py): handle the events that waited for it, or with
        `drop` discard them; either way their mailbox items are deleted."""
        handled: dict[tuple[str, bytes], list[int]] = {}
        for waiting, source, _at, wsize in self._waiting.pop(session_id, []):
            self._early_bytes -= wsize
            if not drop:
                with contextlib.suppress(OlmError, GroupError, UserError, DecodeError):
                    await self._on_megolm(waiting)
            if source is not None:
                handled.setdefault(source[:2], []).append(source[2])
        for (base, token), ids in handled.items():
            with contextlib.suppress(MetaError):
                await self.core.meta.delete(base, token, ids)

    async def _handle_event(self, gid: str, ev: dm.DmEvent, mine: bool) -> dict | None:
        g = self.d["groups"].get(gid)
        if g is None:
            return None
        kind = ev.WhichOneof("body")
        group = self._group(gid)
        sender = ev.sender
        if kind == "state":
            for earlier in list(ev.state_tail)[-16:]:
                with contextlib.suppress(GroupError):
                    await self._on_state(gid, earlier)
            await self._on_state(gid, ev.state)
            return None
        if not group.is_member(sender):
            if group.is_invited(sender) and not mine and self._hold_event(gid, ev):
                return None
            raise GroupError(_("event from someone who isn't a member"))
        if self._blocked(sender) and not mine:
            return None
        if kind == "sync":
            if g.get("sync") and mine and b64(sender) == self.d["sync"]["pub"]:
                await self.core.devices.apply_event(json.loads(ev.sync))
            return None
        if kind == "card":
            card = open_card(ev.card)
            if card.card.identity != sender:
                raise GroupError(_("a contact card for someone else"))
            if not mine:
                self._remember_peer_card(card)
            return None
        if kind == "inboxes":
            # Only for the device that sent it: nobody can redirect someone
            # else's mailbox.
            if ev.inboxes.curve25519 != ev.sender_device or len(ev.inboxes.inbox) != 32:
                raise GroupError(_("a delivery address for another device"))
            g.setdefault("inboxes", {})[b64(ev.inboxes.curve25519)] = b64(ev.inboxes.inbox)
            self.core.save_vault()
            return None
        if kind == "stored":
            self._on_stored(gid, ev.stored, sender)
            return None
        if kind == "received":
            self.files.on_received(ev.sender_device, list(ev.received.hashes))
            return None
        if kind == "call":
            self.core.dmcalls.on_event(gid, ev, mine)
            return None
        if kind == "typing":
            if not mine:
                name = self._name_of(gid, sender)
                self.typing.setdefault(gid, {})[name] = time.time() + TYPING_SECONDS if ev.typing.typing else 0
                self.core.emit("dm_typing", {"id": gid, "typing": self.info(gid)["typing"]})
            return None
        if self.store is None:
            return None
        hist = self.store.data.setdefault(gid, [])
        eid = ev.id.hex()
        if any(m["id"] == eid for m in hist):
            return None
        if kind == "text":
            # Every member enforces the group's attachment setting.
            refs = list(ev.text.files)[:10] if self._group(gid).state.allow_attachments else []
            if not refs and not ev.text.text:
                return None
            now_ms = int(time.time() * 1000)
            entry = {
                "id": eid,
                "at": ev.at if mine else min(ev.at, now_ms + MAX_CLOCK_SKEW_MS),
                "recv_at": now_ms,
                "sender": b64(sender),
                "name": self._name_of(gid, sender),
                "text": ev.text.text[:MAX_TEXT],
                "reply_to": ev.text.reply_to.hex(),
                "mentions_me": unb64(g["me"]) in list(ev.text.mentions),
                "mine": mine,
                "reactions": {},
                "pinned": False,
                "files": [self._file_entry(r, mine) for r in refs],
            }
            if not mine:
                self.files.expect(refs)
            hist.append(entry)
            hist.sort(key=lambda m: m["at"])
            self.store.save()
            if not mine:
                self.typing.get(gid, {}).pop(entry["name"], None)
                self._notify(gid, entry)
            self.core.emit("dm_message", {"conv": gid, "message": self._for_ui(self._named(gid, entry))})
            return entry
        target = (
            self.store.find(gid, getattr(ev, kind).target.hex())
            if kind in ("edit", "delete", "reaction", "pin")
            else None
        )
        if target is None:
            return None
        if kind == "edit" and target["sender"] == b64(sender) and not target.get("deleted"):
            target["text"] = ev.edit.text[:MAX_TEXT]
            target["edited_at"] = ev.at
        elif kind == "delete" and target["sender"] == b64(sender):
            target.update(deleted=True, text="", reactions={}, pinned=False)
        elif kind == "reaction" and not target.get("deleted"):
            who = target["reactions"].setdefault(ev.reaction.emoji[:MAX_EMOJI], [])
            if ev.reaction.remove and b64(sender) in who:
                who.remove(b64(sender))
            elif not ev.reaction.remove and b64(sender) not in who:
                who.append(b64(sender))
            target["reactions"] = {e: w for e, w in target["reactions"].items() if w}
        elif kind == "pin" and not target.get("deleted"):
            target["pinned"] = not ev.pin.unpin
        else:
            return None
        self.store.save()
        self.core.emit("dm_update", {"conv": gid, "message": self._for_ui(self._named(gid, target))})
        return target

    # ---------------------------------------------------------------- attachments

    def _file_entry(self, ref: dm.DmFileRef, mine: bool) -> dict:
        local = self.files.local(ref.hash) is not None
        return {
            "hash": base64.b64encode(ref.hash).decode(),
            "key": base64.b64encode(ref.key).decode(),
            "name": safe_name(ref.name),
            "ciphertext_size": ref.ciphertext_size,
            "mime": ref.mime[:100],
            "size": ref.size,
            "width": ref.width,
            "height": ref.height,
            "thumb": ("data:image/jpeg;base64," + base64.b64encode(ref.thumbnail).decode()) if ref.thumbnail else "",
            "state": "available" if local or mine else "waiting",
            "nodes": [],
            "token": "",
            "expires_at": 0,
        }

    def _files_with(self, digest: bytes):
        h = base64.b64encode(digest).decode()
        for gid, hist in self.store.data.items() if self.store else []:
            for m in hist:
                for f in m.get("files", []):
                    if f["hash"] == h:
                        yield gid, m, f

    def group_of_file(self, digest: bytes) -> str:
        return next((gid for gid, _m, _f in self._files_with(digest)), "")

    def file_available(self, digest: bytes) -> None:
        for gid, m, f in self._files_with(digest):
            f["state"] = "available"
            self.core.emit("dm_update", {"conv": gid, "message": self._for_ui(self._named(gid, m))})
        if self.store is not None:
            self.store.save()

    def file_expired(self, digest: bytes) -> None:
        for gid, m, f in self._files_with(digest):
            if f["state"] != "available":
                f["state"] = "expired"
                self.core.emit("dm_update", {"conv": gid, "message": self._for_ui(self._named(gid, m))})
        if self.store is not None:
            self.store.save()

    def _on_stored(self, gid: str, stored: dm.DmFileStored, sender: bytes) -> None:
        """Where a file can be fetched: only the person who sent the file may
        say, and only maintainer URLs this client would use itself."""
        mine = self.olm.curve(self._label(self.d["groups"][gid]["me"]))
        token = next((t.token for t in stored.tokens if t.curve25519 == mine), b"")
        try:
            nodes = settings_mod.validate("maintainers", list(stored.nodes)[:8])
        except settings_mod.SettingError:
            raise GroupError(_("the attachment is stored on servers that can't be used")) from None
        for fgid, m, f in self._files_with(stored.hash):
            if fgid != gid or m.get("sender") != b64(sender):
                continue
            f["nodes"] = nodes
            f["expires_at"] = stored.expires_at
            if token:
                f["token"] = base64.b64encode(token).decode()
            if f["state"] == "waiting" and token and self.reachable(gid):
                self.core.spawn(self.files.fetch(gid, m, f))
        if self.store is not None:
            self.store.save()

    def _fetch_waiting(self, gid: str) -> None:
        """Fetch the attachments a conversation's messages are waiting for
        (once it's accepted: _on_stored doesn't fetch before)."""
        if self.store is None or not self.reachable(gid):
            return
        for m in self.store.data.get(gid, []):
            for f in m.get("files", []):
                if f.get("state") == "waiting" and f.get("token") and f.get("nodes"):
                    self.core.spawn(self.files.fetch(gid, m, f))

    async def save_file(self, gid: str, eid: str, digest: str, dest: str) -> dict:
        m = self.store.find(gid, eid) if self.store else None
        f = next((f for f in (m or {}).get("files", []) if f["hash"] == digest), None)
        if f is None:
            raise UserError(_("That file is gone."))
        if f["state"] == "waiting" and not self.reachable(gid):
            raise UserError(_("Accept the request first: its files come from servers the sender chose."))
        if f["state"] == "waiting" and f.get("token"):
            await self.files.fetch(gid, m, f)
        if f["state"] == "waiting" and f.get("expires_at") and time.time() > f["expires_at"]:
            self.file_expired(base64.b64decode(f["hash"]))
        await self.files.save(f, dest)
        return {"ok": True, "path": dest}

    def _name_of(self, gid: str, ident: bytes) -> str:
        st = self._group(gid).state
        for m in [*st.members, *st.invited]:
            if m.identity == ident:
                if b64(ident) in self.d["contacts"]:
                    return self._contact_name(b64(ident))
                if nick := self._nickname(b64(ident)):
                    return nick
                return m.name or self.d["peers"].get(b64(ident), {}).get("name") or _("Someone")
        return "Someone"

    def _notify(self, gid: str, entry: dict) -> None:
        g = self.d["groups"][gid]
        if entry["mentions_me"]:
            g["mentions"] = g.get("mentions", 0) + 1
            self.core.save_vault()
        s = self.core.settings
        if not s.get("notifications", True) or g.get("status") != "active":
            return
        if self.core.statuses.dnd():
            # Do not disturb: badges still count, nothing pops up.
            return
        if s.get("notify_mode", "all") == "mentions" and not entry["mentions_me"]:
            return
        # While locked, nothing about who wrote or what (as for communities).
        title, body, place = (entry["name"] or _("Direct message"), entry["text"][:200], _("Direct messages"))
        if not self.core.unlocked:
            title, body, place = PRODUCT_NAME, _("New activity"), ""
        self.core.emit(
            "notify",
            {
                "title": title,
                "community_name": place,
                "body": body,
                "community": "",
                "channel": 0,
                "dm": gid,
            },
        )

    async def _on_state(self, gid: str, signed: dm.SignedGroupState) -> None:
        g = self.d["groups"][gid]
        group = self._group(gid)
        before = set(self._member_ids(gid))
        changed = group.apply(signed)
        g["pending"] = group.pending_list()
        if group.lost_leaves:
            # Kept with the group until the owner's client removes them.
            g["lost_leaves"] = sorted(set(g.get("lost_leaves", [])) | {b64(m) for m in group.lost_leaves})
        if not changed:
            self.core.save_vault()
            if group.lost_leaves:
                self.core.spawn(self._reconcile(gid))
            return
        g["chain"] = group.to_list()
        me = unb64(g["me"])
        if not group.is_member(me) and not group.is_invited(me) and g.get("status") == "active":
            # Removed from the group: keep the history, stop sending.
            g["status"] = "removed"
        if set(self._member_ids(gid)) != before:
            g["rekey"] = True
        self.core.save_vault()
        self._system_entry(gid, signed)
        self.core.emit("dm", {"id": gid})
        await self._retry_held_keys(gid)
        await self._retry_pending_events(gid)
        self.core.spawn(self._reconcile(gid))

    async def _reconcile(self, gid: str) -> None:
        """Re-apply a change that lost to a concurrent one (the group keeps
        one change per state): my acceptance, signed again on top of the
        winner; and, on the owner's client, a member's leaving, which they
        can't sign again (they dropped the group), so the owner removes them."""
        g = self.d["groups"].get(gid)
        if g is None or g.get("status") != "active":
            return
        group = self._group(gid)
        me = unb64(g["me"])
        lost = [unb64(m) for m in g.get("lost_leaves", [])]
        g.pop("lost_leaves", None)
        if group.state.owner != me:
            lost = []
        for gone in lost:
            if not group.is_member(gone) or gone == me:
                continue

            def remove(s, gone=gone):
                keep = [m for m in s.members if m.identity != gone]
                del s.members[:]
                s.members.extend(keep)

            try:
                await self._change(gid, remove)
            except UserError as e:
                log.info("dm: couldn't re-apply a member's leaving: %s", e)
        if group.is_invited(me) and not group.is_member(me):
            pub, cid = g["me"], g["community"]

            def edit(s):
                keep = [m for m in s.invited if m.identity != me]
                del s.invited[:]
                s.invited.extend(keep)
                s.members.append(self._me_member(cid, pub, s.seq))

            try:
                await self._change(gid, edit)
            except UserError as e:
                log.info("dm: couldn't re-apply acceptance: %s", e)

    def _system_entry(self, gid: str, signed: dm.SignedGroupState) -> None:
        """A line in the history for membership and settings changes."""
        if self.store is None:
            return
        st = groups.open_state(signed)
        hist = self.store.data.setdefault(gid, [])
        eid = groups.state_hash(signed).hex()[:32]
        if any(m["id"] == eid for m in hist) or st.seq == 1:
            return
        hist.append(
            {
                "id": eid,
                "at": st.at,
                "system": True,
                "sender": b64(signed.signer),
                "name": self._name_of(gid, signed.signer),
                "text": _("updated the conversation"),
                "mine": b64(signed.signer) in self.d["ids"],
                "reactions": {},
            }
        )
        hist.sort(key=lambda m: m["at"])
        self.store.save()

    # ---------------------------------------------------------------- device-group sync (docs/protocol.md §10.2)

    async def ensure_sync(self) -> None:
        """The profile's own device group: a group whose only member is the
        sync identity, whose devices are this profile's devices."""
        s = self.d.setdefault("sync", {})
        if s.get("gid") in self.d["groups"]:
            await self._ensure_listed(s["pub"])
            await self._sync_relay()
            return
        if not self.core.unlocked:
            return
        # A restored backup brings the old sync identity (keys.export_backup):
        # the new device group keeps it rather than leaving a key unused.
        idents = self.core.vault.data["identities"]
        iid = next((i for i, v in idents.items() if v.get("kind") == "sync" and "seed" in v), "")
        if iid:
            k = SigningKey(unb64(idents[iid]["seed"]))
            pub = b64(k.public)
        else:
            k = SigningKey.generate()
            pub = b64(k.public)
            iid = f"sync-{pub[:16]}"
            idents[iid] = {"seed": b64(k.seed), "kind": "sync", "created": int(time.time())}
        self.d["ids"][pub] = {"iid": iid, "list": "", "extras": {}, "inbox_token": b64(os.urandom(32))}
        gid_b = os.urandom(16)
        st = dm.GroupState(
            group_id=gid_b,
            seq=1,
            owner=k.public,
            members=[self._me_member("", pub, 1)],
            relay=await self._sync_relay_url(),
            topic=os.urandom(32),
            at=int(time.time() * 1000),
        )
        gid = gid_b.hex()
        self.d["groups"][gid] = {
            "chain": Group.from_chain([groups.sign_state(k, st)]).to_list(),
            "me": pub,
            "community": "",
            "status": "active",
            "inbox_token": b64(os.urandom(32)),
            "created": int(time.time()),
            "sync": True,
        }
        self.d["sync"] = {"pub": pub, "gid": gid}
        self.core.vault.keys_changed()
        self.core.save_vault()
        await self._ensure_listed(pub)
        await self._subscribe(gid)
        await self._announce_inbox(gid)

    async def _sync_relay_url(self) -> str:
        dedicated = self._dedicated_relays()
        with contextlib.suppress(MetaError):
            relays = sorted(r["url"] for r in await self.core.meta.relays() if r["url"] not in dedicated)
            if relays:
                return relays[0]
        for rec in self.core.vault.data["communities"].values():
            for url in rec.get("relays") or []:
                if url not in dedicated:
                    return url
        return ""

    async def _sync_relay(self) -> None:
        """A device group created before any relay was known gets one later."""
        gid = self.d["sync"]["gid"]
        if self._group(gid).state.relay or not self.core.unlocked:
            return
        url = await self._sync_relay_url()
        if url:
            await self._change(gid, lambda s: setattr(s, "relay", url))
            await self._subscribe(gid)

    def _other_devices(self) -> list[dm.DmDevice]:
        s = self.d.get("sync", {})
        if not s.get("pub"):
            return []
        mine = self.olm.curve(self._label(s["pub"]))
        return [d for d in self._devices_of(s["pub"]) if d.curve25519 != mine]

    async def send_sync(self, event: dict) -> None:
        if self.core.vault is None or not self.d.get("sync", {}).get("gid") or not self._other_devices():
            return
        await self.send_event(self.d["sync"]["gid"], dm.DmEvent(sync=json.dumps(event).encode()))

    async def send_sync_to(self, device_id: str, event: dict) -> None:
        """A sync event for one device only, over Olm."""
        s = self.d.get("sync", {})
        dev = next((d for d in self._other_devices() if d.device_id.hex() == device_id), None)
        if dev is None:
            raise UserError(_("That device isn't linked."))
        payload = dm.OlmPayload(sync=json.dumps(event).encode())
        if not await self._send_olm(s["pub"], unb64(s["pub"]), dev, payload, live=s["gid"]):
            raise UserError(_("Couldn't reach that device."))

    def sync_card(self) -> str:
        s = self.d["sync"]
        return b64(self._build_card(s["pub"], "")[0].SerializeToString())

    def adopt_sync_card(self, raw: str) -> None:
        if not raw:
            return
        with contextlib.suppress(UserError, ValueError):
            card = open_card(dm.SignedContactCard.FromString(unb64(raw)))
            if b64(card.card.identity) == self.d.get("sync", {}).get("pub"):
                self._adopt_own_card(card)

    def _adopt_own_card(self, card: Card) -> None:
        """Another of my devices published a newer list for one of my
        identities (e.g. it was just linked)."""
        pub = b64(card.card.identity)
        ent = self.d["ids"][pub]
        mine = self._my_list(pub)
        removed = set(self.d.get("removed_devices", {}).get(pub, []))
        if any(b64(d.curve25519) in removed for d in card.devices.devices):
            return
        if list_replaces(mine, card.card.devices, card.devices):
            ent["list"] = b64(card.card.devices.SerializeToString())
            for g in self.d["groups"].values():
                if g["me"] == pub:
                    g["rekey"] = True
        for fk in card.card.fallback_keys:
            inbox = next((i.inbox for i in card.card.inboxes if i.curve25519 == fk.curve25519), b"")
            ent.setdefault("extras", {}).setdefault(
                b64(fk.curve25519), {"fallback": b64(fk.SerializeToString()), "inbox": b64(inbox)}
            )
        self.core.save_vault()

    # ---------------------------------------------------------------- device linking hooks

    def export_for_link(self) -> dict:
        """Identity keys (their seeds travel with the other identities),
        contacts and group states for a new device. Olm and Megolm state is
        per device and never copied: the new device gets its own accounts and
        receives current keys from the other members."""
        ids = {
            pub: {
                "iid": e["iid"],
                "list": e.get("list", ""),
                "extras": e.get("extras", {}),
                "certs": e.get("certs", {}),
            }
            for pub, e in self.d["ids"].items()
        }
        return {
            "ids": ids,
            "groups": {
                gid: self._group_sync(gid) | {"sync": bool(g.get("sync")), "read_at": g.get("read_at", 0)}
                for gid, g in self.d["groups"].items()
            },
            "contacts": self.d["contacts"],
            "peers": self.d["peers"],
            "pins": self.d["pins"],
            "nicknames": self._nicknames(),
            "blocked": self.d["blocked"],
            "sync": self.d.get("sync", {}),
        }

    @staticmethod
    def import_from_link(data: dict) -> dict:
        """The `dm` section of a new device's vault, from export_for_link."""
        ids = {
            pub: {
                "iid": e["iid"],
                "list": e.get("list", ""),
                "extras": dict(e.get("extras", {})),
                "certs": dict(e.get("certs", {})),
                "inbox_token": b64(os.urandom(32)),
            }
            for pub, e in data.get("ids", {}).items()
        }
        grps = {
            gid: {
                "chain": gs["chain"],
                "me": gs["me"],
                "community": gs.get("community", ""),
                "status": gs.get("status", "active"),
                "inbox_token": b64(os.urandom(32)),
                "created": gs.get("created", int(time.time())),
                "rekey": True,
                # What was read on the linking device stays read here.
                "read_at": gs["read_at"] if isinstance(gs.get("read_at"), int) else 0,
                **({"sync": True} if gs.get("sync") else {}),
            }
            for gid, gs in data.get("groups", {}).items()
        }
        return {
            "ids": ids,
            "groups": grps,
            "contacts": dict(data.get("contacts", {})),
            "peers": dict(data.get("peers", {})),
            "pins": dict(data.get("pins", {})),
            "nicknames": dict(data.get("nicknames", {})),
            "blocked": list(data.get("blocked", [])),
            "sync": dict(data.get("sync", {})),
        }

    async def after_link(self) -> None:
        """On the new device, once its vault exists: join every identity's
        device list and announce this device in every group."""
        for pub in list(self.d["ids"]):
            await self._ensure_listed(pub)
        for gid, g in list(self.d["groups"].items()):
            await self._subscribe(gid)
            if g.get("status") == "active":
                await self._announce_inbox(gid)

    def history_export(self) -> dict:
        return dict(self.store.data) if self.store else {}

    def history_import(self, gid: str, messages: list[dict]) -> int:
        """Entries copied from another of my devices; malformed ones (see
        valid_entry) are dropped."""
        if self.store is None or not isinstance(gid, str):
            return 0
        have = {m["id"] for m in self.store.data.get(gid, [])}
        added = []
        for m in messages:
            if valid_entry(m) and m["id"] not in have:
                have.add(m["id"])
                added.append({"text": "", "reactions": {}, **m})
        if not added:
            return 0
        hist = self.store.data.setdefault(gid, [])
        hist.extend(added)
        hist.sort(key=lambda m: m["at"])
        self.store.save()
        return len(added)

    def history_size(self) -> int:
        if self.store is None:
            return 0
        return len(json.dumps(self.store.data))
