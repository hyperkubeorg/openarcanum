"""End-to-end encryption state for DMs and device sync (docs/protocol.md §9).

A thin, persistent layer over vodozemac. Nothing here designs cryptography:
Olm sessions (one per pair of devices) carry key material, Megolm sessions
(one outbound session per sender device per group) carry events, and every
ratchet step is saved before its output leaves the process, so a crash can
never make a device reuse ratchet state.

State lives in `data/olm.bin`, sealed with a key kept in the vault. vodozemac
objects are pickled with a second vault key.

Accounts are labelled: `dm:<identity>` for each DM identity (so device keys
are never shared between communities, keeping per-community identities
unlinkable) and `sync` for the device group.
"""

from __future__ import annotations

import base64
import contextlib
import json
import logging
import time
from dataclasses import dataclass
from pathlib import Path

import vodozemac as vz

from openarcanum import crypto, keys
from openarcanum.paths import atomic_write

log = logging.getLogger(__name__)

MAGIC = b"OARCOLM1"

# Megolm rotation (docs/protocol.md §9.5). The same limits Matrix clients use
# by default: a new outbound session after 100 messages or one week, and on
# every membership or device-list change.
ROTATE_MESSAGES = 100
ROTATE_SECONDS = 7 * 24 * 3600

# Olm sessions kept per peer device. A peer that lost its state and starts a
# new session doesn't strand messages still in flight on the old one.
SESSIONS_PER_PEER = 4
# Session ids remembered per peer, so a replayed pre-key message can't
# re-create a session that was already used (and has since been evicted).
KNOWN_SESSIONS_PER_PEER = 256
# Peers with Olm state per account; the least recently used go first. Anyone
# who can reach a first-contact inbox can make a new one, so it's bounded.
MAX_PEERS = 5000
# Copies of one Megolm session id kept from different sharing devices (inbound_for).
MAX_SHARERS = 16
# Inbound Megolm sessions kept per (group, sender device): the newest few,
# plus everything from the last 30 days (late and out-of-order events).
INBOUND_KEEP_NEWEST = 3
INBOUND_KEEP_SECONDS = 30 * 24 * 3600
ONE_TIME_KEYS = 50

CTX_OTK = b"openarcanum/dm-otk/v2\x00"


class OlmError(Exception):
    """A message couldn't be decrypted, or keys were missing or invalid."""


@dataclass
class Decrypted:
    plaintext: bytes
    session_id: str
    index: int
    group_id: bytes
    sender_curve: bytes
    sender_ed: bytes
    label: str


def otk_message(curve: bytes, key_id: str, key: bytes, fallback: bool) -> bytes:
    return CTX_OTK + curve + key_id.encode() + b"\x00" + key + (b"\x01" if fallback else b"\x00")


def _b64(b: bytes) -> str:
    return keys.encode(b)


class OlmStore:
    def __init__(self, path: Path, seal_key: bytes, pickle_key: bytes):
        self.path = path
        self.seal_key = seal_key
        self.pickle_key = pickle_key
        self.accounts: dict[str, vz.Account] = {}
        # label -> peer curve (b64) -> pickled sessions, most recent first
        self.sessions: dict[str, dict[str, list[str]]] = {}
        # label -> group id (b64) -> {pickle, created, count, shared: [curve b64]}
        self.outbound: dict[str, dict[str, dict]] = {}
        # session id -> {pickle, group, sender_curve, sender_ed, label, seen: {index: event digest}}
        self.inbound: dict[str, dict] = {}
        # label -> peer curve (b64) -> session ids ever created with that peer
        self.known: dict[str, dict[str, list[str]]] = {}
        # Inside batch(): saves wait until the batch ends.
        self._batch = 0
        self._dirty = False
        self._load()

    # ---------------------------------------------------------------- persistence

    @contextlib.contextmanager
    def batch(self):
        """Save once at the end instead of after every ratchet step: for many
        Olm messages at once (a status key to every member of a community,
        statuses.py). The caller sends nothing produced inside the batch
        until it has ended, so state is still saved before its output leaves
        the process."""
        self._batch += 1
        try:
            yield self
        finally:
            self._batch -= 1
            if not self._batch and self._dirty:
                self.save()

    def _load(self) -> None:
        try:
            blob = self.path.read_bytes()
        except FileNotFoundError:
            return
        if not blob.startswith(MAGIC):
            # The magic's last character is the format version. A file from a
            # newer version is left alone rather than misread.
            if blob[: len(MAGIC) - 1] == MAGIC[:-1] and blob[len(MAGIC) - 1 : len(MAGIC)] > MAGIC[-1:]:
                raise OlmError("olm state file was saved by a newer version of OpenArcanum")
            raise OlmError("olm state file is not recognised")
        try:
            data = json.loads(crypto.open_sealed(self.seal_key, blob[len(MAGIC) :], MAGIC))
        except crypto.DecryptError as e:
            # The only copy of this device's DM keys: refuse rather than start
            # over silently, which would break every conversation.
            raise OlmError("olm state file failed to decrypt") from e
        for label, p in data.get("accounts", {}).items():
            self.accounts[label] = vz.Account.from_pickle(p, self.pickle_key)
        self.sessions = data.get("sessions", {})
        self.outbound = data.get("outbound", {})
        self.inbound = data.get("inbound", {})
        self.known = data.get("known", {})

    def save(self) -> None:
        if self._batch:
            self._dirty = True
            return
        self._dirty = False
        data = {
            "accounts": {label: a.pickle(self.pickle_key) for label, a in self.accounts.items()},
            "sessions": self.sessions,
            "outbound": self.outbound,
            "inbound": self.inbound,
            "known": self.known,
        }
        atomic_write(self.path, MAGIC + crypto.seal(self.seal_key, json.dumps(data).encode(), MAGIC))

    # ---------------------------------------------------------------- accounts

    def account(self, label: str) -> vz.Account:
        a = self.accounts.get(label)
        if a is None:
            a = self.accounts[label] = vz.Account()
            self.save()
        return a

    def has_account(self, label: str) -> bool:
        return label in self.accounts

    def drop_account(self, label: str) -> None:
        self.accounts.pop(label, None)
        self.sessions.pop(label, None)
        self.outbound.pop(label, None)
        self.inbound = {sid: s for sid, s in self.inbound.items() if s["label"] != label}
        self.save()

    def curve(self, label: str) -> bytes:
        return self.account(label).curve25519_key.to_bytes()

    def ed(self, label: str) -> bytes:
        return _from_std(self.account(label).ed25519_key.to_base64())

    def sign(self, label: str, message: bytes) -> bytes:
        return _from_std(self.account(label).sign(message).to_base64())

    @staticmethod
    def verify(ed: bytes, message: bytes, signature: bytes) -> bool:
        try:
            vz.Ed25519PublicKey.from_base64(_std_b64(ed)).verify_signature(
                message, vz.Ed25519Signature.from_base64(_std_b64(signature))
            )
            return True
        except vz.SignatureException, vz.KeyException, vz.DecodeException, ValueError:
            return False

    def new_one_time_keys(self, label: str, count: int = ONE_TIME_KEYS) -> list[tuple[str, bytes]]:
        """Fresh one-time keys to publish. vodozemac keeps the private halves
        until each is used by an inbound session."""
        a = self.account(label)
        a.generate_one_time_keys(count)
        out = [(kid, k.to_bytes()) for kid, k in a.one_time_keys.items()]
        a.mark_keys_as_published()
        self.save()
        return out

    def fallback_key(self, label: str, rotate: bool = False) -> tuple[str, bytes]:
        a = self.account(label)
        fk = a.fallback_key
        if rotate or not fk:
            a.generate_fallback_key()
            fk = a.fallback_key
            a.mark_keys_as_published()
            self.save()
        kid, k = next(iter(fk.items()))
        return kid, k.to_bytes()

    def signed_otk(self, label: str, key_id: str, key: bytes, fallback: bool) -> bytes:
        return self.sign(label, otk_message(self.curve(label), key_id, key, fallback))

    # ---------------------------------------------------------------- Olm

    def has_session(self, label: str, peer_curve: bytes) -> bool:
        return bool(self.sessions.get(label, {}).get(_b64(peer_curve)))

    def encrypt_olm(self, label: str, peer_curve: bytes, plaintext: bytes, one_time_key: bytes | None = None):
        """(type, ciphertext). Uses the newest session with the peer, or starts
        one from a claimed one-time key."""
        peers = self.sessions.setdefault(label, {})
        pickles = peers.get(_b64(peer_curve), [])
        if pickles:
            s = vz.Session.from_pickle(pickles[0], self.pickle_key)
        elif one_time_key is not None:
            s = self.account(label).create_outbound_session(
                vz.Curve25519PublicKey.from_bytes(peer_curve), vz.Curve25519PublicKey.from_bytes(one_time_key)
            )
        else:
            raise OlmError("no Olm session with that device and no one-time key to start one")
        msg = s.encrypt(plaintext)
        if not pickles:
            known = self.known.setdefault(label, {}).setdefault(_b64(peer_curve), [])
            known.append(s.session_id)
            del known[:-KNOWN_SESSIONS_PER_PEER]
        peers[_b64(peer_curve)] = [s.pickle(self.pickle_key), *pickles[1:]] if pickles else [s.pickle(self.pickle_key)]
        self._touch_peer(label, _b64(peer_curve))
        self.save()
        mtype, ct = msg.to_parts()
        return mtype, ct

    def _touch_peer(self, label: str, peer: str) -> None:
        """Mark a peer recently used; drop the least recently used beyond MAX_PEERS."""
        peers = self.sessions.setdefault(label, {})
        if peer in peers:
            peers[peer] = peers.pop(peer)
        while len(peers) > MAX_PEERS:
            old = next(iter(peers))
            peers.pop(old)
            self.known.get(label, {}).pop(old, None)

    def decrypt_olm(self, label: str, sender_curve: bytes, mtype: int, ciphertext: bytes) -> bytes:
        """Decrypt an Olm message from a peer device. A pre-key message for a
        session this device already has may only be decrypted by that session
        (so a replay fails there), and one for a session it had before is
        refused: replays can't create sessions or push out the real one."""
        if label not in self.accounts:
            raise OlmError("no account for that recipient")
        peer = _b64(sender_curve)
        peers = self.sessions.setdefault(label, {})
        pickles = peers.get(peer, [])
        # The type and ciphertext come off the network as they are: a
        # malformed message is refused like an undecryptable one.
        try:
            msg = vz.AnyOlmMessage.from_parts(mtype, ciphertext)
            pre = msg.to_pre_key() if mtype == 0 else None
        except (vz.DecodeException, ValueError, OverflowError) as e:
            raise OlmError("malformed Olm message") from e
        if pre is not None:
            for i, p in enumerate(pickles):
                s = vz.Session.from_pickle(p, self.pickle_key)
                if not s.session_matches(pre):
                    continue
                try:
                    pt = s.decrypt(msg)
                except vz.OlmDecryptionException as e:
                    raise OlmError("replayed or damaged pre-key message") from e
                return self._store_session(label, peer, pickles, i, s, pt)
            known = self.known.setdefault(label, {}).setdefault(peer, [])
            if pre.session_id() in known:
                raise OlmError("pre-key message for a session this device already used (replay)")
            try:
                s, pt = self.account(label).create_inbound_session(vz.Curve25519PublicKey.from_bytes(sender_curve), pre)
            except (vz.SessionCreationException, vz.KeyException, vz.DecodeException) as e:
                raise OlmError("couldn't start an Olm session from this message") from e
            known.append(s.session_id)
            del known[:-KNOWN_SESSIONS_PER_PEER]
            peers[peer] = [s.pickle(self.pickle_key), *pickles][:SESSIONS_PER_PEER]
            self._touch_peer(label, peer)
            self.save()
            return pt
        for i, p in enumerate(pickles):
            s = vz.Session.from_pickle(p, self.pickle_key)
            try:
                pt = s.decrypt(msg)
            except vz.OlmDecryptionException:
                continue
            return self._store_session(label, peer, pickles, i, s, pt)
        raise OlmError("no Olm session decrypts this message")

    def _store_session(self, label: str, peer: str, pickles: list[str], i: int, s: vz.Session, pt: bytes) -> bytes:
        pickles[i] = s.pickle(self.pickle_key)
        pickles.insert(0, pickles.pop(i))
        self.sessions[label][peer] = pickles
        self._touch_peer(label, peer)
        self.save()
        return pt

    # ---------------------------------------------------------------- Megolm (outbound)

    def outbound_info(self, label: str, group_id: bytes) -> dict | None:
        return self.outbound.get(label, {}).get(_b64(group_id))

    def needs_rotation(self, label: str, group_id: bytes, now: float | None = None) -> bool:
        info = self.outbound_info(label, group_id)
        if info is None:
            return True
        now = time.time() if now is None else now
        return info["count"] >= ROTATE_MESSAGES or now - info["created"] >= ROTATE_SECONDS

    def rotate(self, label: str, group_id: bytes) -> tuple[str, str]:
        """Start a new outbound session; returns (session_id, session_key).
        Our own inbound copy is stored so this device can read its own events
        when they come back from a mailbox or another device."""
        g = vz.GroupSession()
        sid, skey = g.session_id, g.session_key.to_base64()
        self.outbound.setdefault(label, {})[_b64(group_id)] = {
            "pickle": g.pickle(self.pickle_key),
            "created": time.time(),
            "count": 0,
            "shared": [],
        }
        self._add_inbound(sid, skey, group_id, self.curve(label), self.ed(label), label)
        self.save()
        return sid, skey

    def current_key(self, label: str, group_id: bytes) -> tuple[str, str]:
        """(session_id, session_key at the current index) of the outbound
        session, for devices that join it now: they can't read earlier events."""
        info = self.outbound_info(label, group_id)
        if info is None:
            raise OlmError("no outbound session")
        g = vz.GroupSession.from_pickle(info["pickle"], self.pickle_key)
        return g.session_id, g.session_key.to_base64()

    def exported_key(self, label: str, group_id: bytes) -> tuple[str, str]:
        """(session_id, key) of the outbound session exported at the index of
        its newest message (vodozemac InboundGroupSession.export_at, from this
        device's own inbound copy; the way Matrix forwards keys). A device
        given it reads that newest message and anything after, nothing
        before: used for statuses, where a new recipient needs the current
        status but nothing older."""
        info = self.outbound_info(label, group_id)
        if info is None:
            raise OlmError("no outbound session")
        g = vz.GroupSession.from_pickle(info["pickle"], self.pickle_key)
        own = self.inbound.get(g.session_id)
        if own is None:
            raise OlmError("no inbound copy of the outbound session")
        ig = vz.InboundGroupSession.from_pickle(own["pickle"], self.pickle_key)
        index = max(g.message_index - 1, ig.first_known_index)
        return g.session_id, ig.export_at(index).to_base64()

    def add_inbound_exported(
        self, label: str, session_id: str, exported: str, group_id: bytes, sender_curve: bytes, sender_ed: bytes
    ) -> bool:
        """Store an exported session key (exported_key) received over Olm
        from `sender_curve`. Like add_inbound: kept per (session id, sender
        device), and refused for a session id the same sender bound to another
        group. A later key for a known session that starts earlier than the
        one held replaces it (it reads more)."""
        try:
            ig = vz.InboundGroupSession.import_session(vz.ExportedSessionKey(exported))
        except (vz.SessionKeyDecodeException, vz.DecodeException, ValueError) as e:
            raise OlmError("bad session key") from e
        if ig.session_id != session_id:
            raise OlmError("session id doesn't match the key")
        old = self.inbound_for(session_id, sender_curve)
        if old is not None:
            if old["group"] != _b64(group_id):
                raise OlmError("session id already belongs to another group")
            held = vz.InboundGroupSession.from_pickle(old["pickle"], self.pickle_key)
            if held.first_known_index <= ig.first_known_index:
                return False
            old["pickle"] = ig.pickle(self.pickle_key)
            self.save()
            return True
        self.inbound[self._new_inbound_key(session_id, sender_curve)] = {
            "pickle": ig.pickle(self.pickle_key),
            "group": _b64(group_id),
            "sender_curve": _b64(sender_curve),
            "sender_ed": _b64(sender_ed),
            "label": label,
            "seen": {},
            "added": time.time(),
        }
        self.save()
        return True

    def shared_with(self, label: str, group_id: bytes) -> set[str]:
        info = self.outbound_info(label, group_id)
        return set(info["shared"]) if info else set()

    def mark_shared(self, label: str, group_id: bytes, curves: list[bytes]) -> None:
        info = self.outbound_info(label, group_id)
        if info is None:
            return
        info["shared"] = sorted(set(info["shared"]) | {_b64(c) for c in curves})
        self.save()

    def encrypt_megolm(self, label: str, group_id: bytes, plaintext: bytes) -> tuple[str, bytes]:
        info = self.outbound_info(label, group_id)
        if info is None:
            raise OlmError("no outbound session")
        g = vz.GroupSession.from_pickle(info["pickle"], self.pickle_key)
        msg = g.encrypt(plaintext)
        info["pickle"] = g.pickle(self.pickle_key)
        info["count"] += 1
        self.save()
        return g.session_id, msg.to_bytes()

    def drop_outbound(self, label: str, group_id: bytes) -> None:
        self.outbound.get(label, {}).pop(_b64(group_id), None)
        self.save()

    # ---------------------------------------------------------------- Megolm (inbound)

    # Inbound sessions are kept per (session id, sender device). Whoever shares
    # a session key over Olm is only its claimed owner: another member who was
    # given the key could share it first, and a first-wins binding to that
    # member would refuse the real sender's own share (a member silencing
    # another for someone). So each sharer's copy is kept apart; the copy
    # bound to the device an event comes from opens it, and every event names
    # its sending device inside the encryption (dm.py, statuses.py check it),
    # which only the session's creator can produce. The first copy of a
    # session id is stored under the id itself, others under "id|sender".

    def inbound_for(self, session_id: str, sender_curve: bytes) -> dict | None:
        """The inbound session `session_id` as shared by `sender_curve`."""
        c = _b64(sender_curve)
        info = self.inbound.get(session_id)
        if info is not None and info["sender_curve"] == c:
            return info
        return self.inbound.get(f"{session_id}|{c}")

    def _new_inbound_key(self, session_id: str, sender_curve: bytes) -> str:
        if session_id not in self.inbound:
            return session_id
        others = sum(1 for k in self.inbound if k.startswith(session_id + "|"))
        if others >= MAX_SHARERS:
            raise OlmError("too many devices shared this session")
        return f"{session_id}|{_b64(sender_curve)}"

    def _add_inbound(self, sid: str, skey: str, group_id: bytes, curve: bytes, ed: bytes, label: str) -> None:
        if self.inbound_for(sid, curve) is not None:
            return
        ig = vz.InboundGroupSession(vz.SessionKey(skey))
        self.inbound[self._new_inbound_key(sid, curve)] = {
            "pickle": ig.pickle(self.pickle_key),
            "group": _b64(group_id),
            "sender_curve": _b64(curve),
            "sender_ed": _b64(ed),
            "label": label,
            "seen": {},
            "added": time.time(),
        }

    def add_inbound(
        self, label: str, session_id: str, session_key: str, group_id: bytes, sender_curve: bytes, sender_ed: bytes
    ) -> bool:
        """Store a session key received over Olm from `sender_curve`, kept
        apart from any copy of the same session another device shared (see
        inbound_for). A session id this sender already bound to another group
        is refused."""
        try:
            ig = vz.InboundGroupSession(vz.SessionKey(session_key))
        except (vz.SessionKeyDecodeException, vz.DecodeException, ValueError) as e:
            raise OlmError("bad session key") from e
        if ig.session_id != session_id:
            raise OlmError("session id doesn't match the key")
        old = self.inbound_for(session_id, sender_curve)
        if old is not None:
            if old["group"] != _b64(group_id):
                raise OlmError("session id already belongs to another group")
            return False
        self._add_inbound(session_id, session_key, group_id, sender_curve, sender_ed, label)
        self.save()
        return True

    def decrypt_megolm(self, session_id: str, sender_curve: bytes, ciphertext: bytes) -> Decrypted:
        """Decrypt one event. Callers check `check_replay` with the event's id
        afterwards: the same index carrying a different event is a replay."""
        info = self.inbound_for(session_id, sender_curve)
        if info is None:
            raise OlmError(
                "unknown Megolm session" if not self.inbound.get(session_id) else "Megolm session from another device"
            )
        ig = vz.InboundGroupSession.from_pickle(info["pickle"], self.pickle_key)
        try:
            res = ig.decrypt(vz.MegolmMessage.from_bytes(ciphertext))
        except (vz.MegolmDecryptionException, vz.DecodeException, ValueError) as e:
            raise OlmError("Megolm decryption failed") from e
        return Decrypted(
            plaintext=res.plaintext,
            session_id=session_id,
            index=res.message_index,
            group_id=keys.decode(info["group"]),
            sender_curve=sender_curve,
            sender_ed=keys.decode(info["sender_ed"]),
            label=info["label"],
        )

    def check_replay(self, session_id: str, index: int, event_id: bytes, sender_curve: bytes | None = None) -> bool:
        """True if (session, index) is new or carries the same event as before
        (in the copy of the session shared by `sender_curve`, when given)."""
        info = self.inbound.get(session_id) if sender_curve is None else self.inbound_for(session_id, sender_curve)
        if info is None:
            return False
        seen = info.setdefault("seen", {})
        if len(seen) > 2000:
            # Bounded: the oldest indexes are forgotten, and everything at or
            # below the newest forgotten one is refused from then on (the
            # session still decrypts old indexes, so forgetting them would
            # let old events be replayed).
            old = sorted(seen, key=int)[:1000]
            info["floor"] = max(info.get("floor", -1), int(old[-1]))
            for k in old:
                seen.pop(k)
        prev = seen.get(str(index))
        if prev is None and index <= info.get("floor", -1):
            return False
        if prev is None:
            seen[str(index)] = _b64(event_id)
            self.save()
            return True
        return prev == _b64(event_id)

    def prune_inbound(self, now: float | None = None) -> int:
        """Drop inbound Megolm sessions that are old and superseded: keep the
        newest few per (group, sender device) and everything recent."""
        now = time.time() if now is None else now
        by: dict[tuple[str, str], list[tuple[float, str]]] = {}
        for sid, info in self.inbound.items():
            by.setdefault((info["group"], info["sender_curve"]), []).append((info.get("added", 0), sid))
        dropped = 0
        for items in by.values():
            items.sort(reverse=True)
            for added, sid in items[INBOUND_KEEP_NEWEST:]:
                if now - added > INBOUND_KEEP_SECONDS:
                    self.inbound.pop(sid, None)
                    dropped += 1
        if dropped:
            self.save()
        return dropped

    def forget_group(self, group_id: bytes) -> None:
        g = _b64(group_id)
        self.inbound = {sid: s for sid, s in self.inbound.items() if s["group"] != g}
        for per in self.outbound.values():
            per.pop(g, None)
        self.save()


def _std_b64(b: bytes) -> str:
    """vodozemac uses unpadded standard base64 for keys and signatures."""
    return base64.b64encode(b).decode().rstrip("=")


def _from_std(s: str) -> bytes:
    return base64.b64decode(s + "=" * (-len(s) % 4))
