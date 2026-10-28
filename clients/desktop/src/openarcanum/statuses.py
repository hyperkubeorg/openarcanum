"""User statuses: a state, custom text and an expiry, end-to-end encrypted
to the audiences the user picks (docs/protocol.md §4.11 and §9.8;
docs/threat-model.md "Statuses").

Nothing here is new cryptography. Each **audience** is one vodozemac Megolm
outbound session on each of the user's devices, used as a sender key: a
status is one Megolm message for the whole audience, and the session key
goes to each recipient device over Olm. This is the DM design (dm.py) with
two kinds of audience:

- **A community** the user shares with. Each device of each member that can
  receive statuses there publishes a *status device* entry (its own
  vodozemac Account labelled ``status:<community>``, signed by the member
  key, so the community server can't substitute devices). Keys travel as
  Olm messages the server stores for each recipient device until it fetches
  them; the status itself is one opaque blob per member that the server
  stores and pushes to everyone. The server never holds a key.
- **Contacts** (all, or chosen ones). Keys go over the DM machinery (Olm with
  the DM identity the contact knows, into their devices' mailboxes); the
  status is one Megolm message dropped into each contact device's
  first-contact inbox. One audience per DM identity, so identities used in
  different communities stay unlinkable.

**Rotation.** A new session (and so new keys for everyone) whenever a
recipient device left the audience (someone left or was removed from the
community, removed from contacts or un-chosen, or a device was removed),
after 1000 messages and after 7 days. In a community the rotation waits for
the next status the user posts (nothing new is readable before then), so a
member leaving costs each sharer O(members) key messages once, not at once.
Devices that join the audience get the current session exported at its
newest message (``OlmStore.exported_key``): they can read the current status
and nothing earlier, with no new post.

**What servers see** (and the UI says): the community server sees who is
connected (it routes sessions), when a member's status ciphertext changes,
how big it is and which devices get keys; never the state or text.
Invisible is a flag the server must read to stop announcing the user's
presence, and the user's connection stays visible to the server.

Status text, keys and status contents are never logged.
"""

from __future__ import annotations

import asyncio
import contextlib
import datetime
import logging
import os
import time
import unicodedata
from typing import TYPE_CHECKING

from google.protobuf.message import DecodeError

from openarcanum import keys
from openarcanum.community import Offline, RpcError
from openarcanum.errors import UserError
from openarcanum.i18n import _
from openarcanum.meta import MetaError
from openarcanum.olm import OlmError, OlmStore, otk_message
from openarcanum.wire import community_pb2 as cp
from openarcanum.wire import dm_pb2 as dm

if TYPE_CHECKING:
    from openarcanum.core import Core

log = logging.getLogger(__name__)

MAGIC = b"FDM2"
STATES = ("online", "idle", "dnd", "invisible")
LEVELS = ("off", "state", "full")
CLEAR_AFTER = ("never", "30m", "1h", "4h", "today")
CLEAR_SECONDS = {"30m": 30 * 60, "1h": 3600, "4h": 4 * 3600}
MAX_TEXT = 128
MAX_EMOJI = 32
# Contacts have no server telling them who is connected: a status says how
# long it stays fresh, and it is posted again while the app runs.
REFRESH_SECONDS = 30 * 60
FRESH_SECONDS = 75 * 60
# Megolm limits for status sessions (statuses change often and cheaply; a
# rotation in a large community costs a key message per member device).
ROTATE_MESSAGES = 1000
ROTATE_SECONDS = 7 * 24 * 3600
MAX_CLOCK_SKEW_MS = 5 * 60 * 1000
SEND_CHUNK = 500
# Asking a member's devices again for a key that never arrived.
KEY_REQUEST_EVERY = 600
LOOP_SECONDS = 15

WIRE = {
    "online": dm.STATUS_STATE_ONLINE,
    "idle": dm.STATUS_STATE_IDLE,
    "dnd": dm.STATUS_STATE_DND,
    "invisible": dm.STATUS_STATE_OFFLINE,
}
FROM_WIRE = {
    dm.STATUS_STATE_ONLINE: "online",
    dm.STATUS_STATE_IDLE: "idle",
    dm.STATUS_STATE_DND: "dnd",
    dm.STATUS_STATE_OFFLINE: "offline",
}


def b64(b: bytes) -> str:
    return keys.encode(b)


def unb64(s: str) -> bytes:
    return keys.decode(s)


def clean_text(text: object) -> str:
    """Custom text: one line, at most MAX_TEXT characters, no control or
    bidirectional formatting characters."""
    if not isinstance(text, str):
        raise UserError(_("The status text must be text."))
    text = " ".join(text.split())
    if len(text) > MAX_TEXT:
        raise UserError(_("A status is at most {n} characters.").format(n=MAX_TEXT))
    if any(unicodedata.category(c) in ("Cc", "Cf") and c not in "‍️" for c in text):
        raise UserError(_("The status text contains characters that can't be shown."))
    return text


def clean_emoji(emoji: object) -> str:
    """An emoji for the status: empty, or 1 to 32 characters that aren't plain
    ASCII, spaces, control or bidirectional characters (joiners and
    variation selectors are fine), like a reaction."""
    if emoji in ("", None):
        return ""
    if not isinstance(emoji, str) or not 0 < len(emoji) <= MAX_EMOJI or emoji.isascii():
        raise UserError(_("Pick one emoji."))
    for c in emoji:
        if c.isspace() or (unicodedata.category(c) in ("Cc", "Cf") and c not in "‍️"):
            raise UserError(_("Pick one emoji."))
    return emoji


def expiry_for(clear_after: str, now: float | None = None) -> int:
    """Unix ms when a status set now clears itself (0 = never). "today" is
    the end of the local day."""
    now = time.time() if now is None else now
    if clear_after not in CLEAR_AFTER:
        raise UserError(_("Pick when the status clears."))
    if clear_after == "never":
        return 0
    if clear_after == "today":
        local = datetime.datetime.fromtimestamp(now).astimezone()
        end = (local + datetime.timedelta(days=1)).replace(hour=0, minute=0, second=0, microsecond=0)
        return int(end.timestamp() * 1000)
    return int((now + CLEAR_SECONDS[clear_after]) * 1000)


def received_view(st: dm.DmStatus, at: int) -> dict:
    """A received status as the UI gets it."""
    return {
        "state": FROM_WIRE.get(st.state, "online"),
        "text": st.text,
        "emoji": st.emoji,
        "expires_at": st.expires_at,
        "fresh_until": st.fresh_until,
        "at": at,
    }


def valid_received(st: dm.DmStatus) -> bool:
    if st.state not in FROM_WIRE and st.state != dm.STATUS_STATE_CLEARED:
        return False
    try:
        return clean_text(st.text) == st.text and clean_emoji(st.emoji) == st.emoji
    except UserError:
        return False


def open_status_device(raw: bytes, member: bytes) -> dm.StatusDevice | None:
    """A status device entry, if it's signed by `member` and its fallback key
    by the device itself; else None."""
    try:
        signed = dm.SignedStatusDevice.FromString(raw)
        d = dm.StatusDevice.FromString(signed.device)
    except DecodeError:
        return None
    if d.member != member or not keys.is_valid(member, keys.CTX_STATUS_DEVICE, signed.device, signed.signature):
        return None
    fk = d.fallback_key
    if fk.curve25519 != d.curve25519 or not fk.fallback:
        return None
    if not OlmStore.verify(d.ed25519, otk_message(fk.curve25519, fk.key_id, fk.key, True), fk.signature):
        return None
    return d


class Statuses:
    """This profile's status, its audiences, and everyone else's statuses."""

    def __init__(self, core: Core):
        self.core = core
        # community -> member (b64) -> verified status devices
        self.devices: dict[str, dict[str, list[dm.StatusDevice]]] = {}
        # community -> member (b64) -> the member's current status blob
        self.blobs: dict[str, dict[str, bytes]] = {}
        # community -> member (b64) -> received status (received_view)
        self.received: dict[str, dict[str, dict]] = {}
        # community -> member (b64) -> (session id, when asked)
        self._asked: dict[tuple[str, str], tuple[str, float]] = {}
        # Idle: no input on this device for status_idle_minutes.
        self._idle = False
        self._loop_task: asyncio.Task | None = None
        self._last_refresh = 0.0
        self._publishing: dict[str, asyncio.Lock] = {}
        # Status session ids whose key was refused (bounded).
        self._refused: dict[str, float] = {}
        self._emit_handle: asyncio.TimerHandle | None = None

    # ---------------------------------------------------------------- state

    @property
    def d(self) -> dict:
        v = self.core.vault
        assert v is not None
        return v.data.setdefault("status", {"mine": {}, "aud": {}, "recv": {}})

    @property
    def olm(self) -> OlmStore:
        o = self.core.dms.olm
        if o is None:
            raise UserError(_("Unlock your profile first."))
        return o

    def mine(self) -> dict:
        m = self.d.setdefault("mine", {})
        return {
            "state": m.get("state", "online"),
            "text": m.get("text", ""),
            "emoji": m.get("emoji", ""),
            "expires_at": int(m.get("expires_at", 0)),
            "clear_after": m.get("clear_after", "never"),
            "at": int(m.get("at", 0)),
        }

    def effective(self) -> str:
        """online, idle, dnd or invisible: what this device shows now."""
        if self.core.vault is None:
            return "online"
        st = self.mine()["state"]
        if st == "online" and self._idle:
            return "idle"
        return st

    def dnd(self) -> bool:
        return self.core.vault is not None and self.effective() == "dnd"

    def view(self) -> dict:
        """Everything the UI shows: my status, members' statuses per
        community, and per DM identity the freshest status known for that
        person (``people``: from their contacts audience, or from a community
        we share where they're online now)."""
        if self.core.vault is None:
            return {"mine": {**self.mine(), "effective": "online", "idle": False}, "communities": {}, "people": {}}
        people = {ident: {**r, "source": "contacts"} for ident, r in self.d.get("recv", {}).items()}
        for ident, cid, member in self._known_people():
            r = self.received.get(cid, {}).get(member)
            online = self._online(cid, member)
            if not online:
                continue
            cand = {**(r or {"state": "online", "text": "", "emoji": "", "expires_at": 0, "at": 0}), "source": cid}
            have = people.get(ident)
            if (
                have is None
                or cand.get("at", 0) >= have.get("at", 0)
                or have.get("fresh_until", 0) < time.time() * 1000
            ):
                people[ident] = cand
        return {
            "mine": {**self.mine(), "effective": self.effective(), "idle": self._idle},
            "communities": {cid: dict(m) for cid, m in self.received.items()},
            "people": people,
            "audience": self.audience_summary(),
        }

    def _online(self, cid: str, member: str) -> bool:
        return any(m.get("key") == member and m.get("online") for m in self.core._members.get(cid, []))

    def _known_people(self) -> list[tuple[str, str, str]]:
        """(DM identity, community, member key) for the people in my DM
        conversations and contacts, where the community is one I'm in."""
        dms = self.core.dms
        mine = self.core.vault.data["communities"]
        out = []
        for ident, c in dms.d.get("contacts", {}).items():
            if c.get("community") in mine and c.get("member"):
                out.append((ident, c["community"], c["member"]))
        if dms.store is not None:
            for gid in list(dms.d.get("groups", {})):
                with contextlib.suppress(Exception):
                    for m in dms._group(gid).state.members:
                        if m.community in mine and m.member:
                            out.append((b64(m.identity), m.community, b64(m.member)))
        return out

    def presence_changed(self) -> None:
        """Someone came or went in a community: the people view follows."""
        if self.core.vault is not None:
            self._emit()

    def audience_summary(self) -> dict:
        s = self.core.settings
        communities = {}
        for cid, rec in (self.core.vault.data["communities"] if self.core.vault else {}).items():
            if rec.get("status") == "active":
                communities[cid] = self.level(cid)
        return {
            "contacts": s.get("status_contacts", "full"),
            "contacts_scope": s.get("status_contacts_scope", "all"),
            "contacts_chosen": len(s.get("status_contacts_chosen", [])),
            "communities": communities,
        }

    def level(self, cid: str) -> str:
        return self.core.settings.get("status_communities", {}).get(cid, "state")

    def _emit(self) -> None:
        """Tell the UI, at most every 300 ms (presence batches and key drops
        come in bursts)."""
        if self._emit_handle is not None:
            return
        try:
            loop = asyncio.get_running_loop()
        except RuntimeError:
            self.core.emit("statuses", self.view())
            return

        def fire() -> None:
            self._emit_handle = None
            self.core.emit("statuses", self.view())

        self._emit_handle = loop.call_later(0.3, fire)

    # ---------------------------------------------------------------- lifecycle

    async def opened(self) -> None:
        self._expire_mine()
        if self._loop_task is None or self._loop_task.done():
            self._loop_task = self.core.spawn(self._loop())
        self._emit()

    async def shutdown(self) -> None:
        """Contacts have no server to tell them the app quit: say "offline"
        to them (best effort). Indistinguishable from Invisible."""
        if self._loop_task is not None:
            self._loop_task.cancel()
        if self.core.vault is None or self.core.dms.olm is None:
            return
        with contextlib.suppress(Exception):
            await asyncio.wait_for(self._publish_contacts(force_state="invisible"), 2.0)

    async def _loop(self) -> None:
        while True:
            await asyncio.sleep(LOOP_SECONDS)
            try:
                await self.tick()
            except asyncio.CancelledError:
                raise
            except Exception:
                log.exception("status upkeep failed")

    async def tick(self, now: float | None = None) -> None:
        """Every few seconds: idle after no input, expiry, the contacts'
        refresh, and audiences that changed underneath (a contact removed
        on another device)."""
        if self.core.vault is None:
            return
        now = time.time() if now is None else now
        minutes = self.core.settings.get("status_idle_minutes", 10)
        idle = bool(minutes) and time.monotonic() - self.core.last_activity >= minutes * 60
        changed = idle != self._idle
        self._idle = idle
        if self._expire_mine(now):
            changed = True
        if changed:
            if idle:
                with contextlib.suppress(Exception):
                    await self.core.devices.broadcast("status_idle", {})
            await self.publish()
            return
        if now - self._last_refresh >= REFRESH_SECONDS or self._contacts_shrank():
            await self._publish_contacts()

    def activity(self) -> None:
        """Input on this device: back from idle at once."""
        if self._idle and self.core.vault is not None:
            self._idle = False
            self.core.spawn(self.publish())

    def _expire_mine(self, now: float | None = None) -> bool:
        if self.core.vault is None:
            return False
        m = self.d.setdefault("mine", {})
        exp = int(m.get("expires_at", 0))
        if exp and (time.time() if now is None else now) * 1000 >= exp:
            self.d["mine"] = {"state": "online", "at": int(time.time() * 1000)}
            self.core.save_vault()
            return True
        return False

    # ---------------------------------------------------------------- setting my status

    async def set_status(self, state: str, text: str = "", emoji: str = "", clear_after: str = "never") -> dict:
        self.core.require_unlocked()
        if state not in STATES:
            raise UserError(_("Pick a status."))
        mine = {
            "state": state,
            "text": clean_text(text),
            "emoji": clean_emoji(emoji),
            "expires_at": expiry_for(clear_after),
            "clear_after": clear_after,
            "at": int(time.time() * 1000),
        }
        self.d["mine"] = mine
        self.core.save_vault()
        self._emit()
        await self.core.devices.broadcast("status", {"mine": mine})
        await self.publish()
        return self.view()

    async def clear_text(self) -> dict:
        m = self.mine()
        return await self.set_status(m["state"], "", "", "never")

    def apply_sync(self, kind: str, data: dict) -> None:
        """My status set on another of my devices (the latest wins)."""
        if self.core.vault is None or not isinstance(data, dict):
            return
        if kind == "status_idle":
            # Another device went idle; if this one is in use, it says so.
            if not self._idle and self.mine()["state"] == "online":
                self.core.spawn(self.publish())
            return
        mine = data.get("mine")
        if not isinstance(mine, dict):
            return
        try:
            clean = {
                "state": mine["state"] if mine.get("state") in STATES else "online",
                "text": clean_text(mine.get("text", "")),
                "emoji": clean_emoji(mine.get("emoji", "")),
                "expires_at": int(mine.get("expires_at", 0)),
                "clear_after": mine.get("clear_after", "never") if mine.get("clear_after") in CLEAR_AFTER else "never",
                "at": int(mine.get("at", 0)),
            }
        except UserError, TypeError, ValueError:
            return
        if clean["at"] < self.mine()["at"]:
            return
        self.d["mine"] = clean
        self.core.save_vault()
        self._emit()

    async def audience_changed(self) -> None:
        """A status setting changed (here or on another device): post again to
        every audience (un-shared ones clear theirs)."""
        await self.publish()

    # ---------------------------------------------------------------- posting

    def _plaintext(self, sender: bytes, device: bytes, audience: bytes, level: str, force_state: str = "") -> bytes:
        m = self.mine()
        state = force_state or self.effective()
        st = dm.DmStatus(state=WIRE[state], expires_at=m["expires_at"])
        if level == "off":
            st = dm.DmStatus(state=dm.STATUS_STATE_CLEARED)
        elif level == "full" and state != "invisible":
            st.text, st.emoji = m["text"], m["emoji"]
        st.fresh_until = int((time.time() + FRESH_SECONDS) * 1000)
        ev = dm.DmEvent(
            id=os.urandom(16),
            at=int(time.time() * 1000),
            group_id=audience,
            sender=sender,
            sender_device=device,
            status=st,
        )
        return ev.SerializeToString()

    def _audience(self, key: str) -> bytes:
        aud = self.d.setdefault("aud", {})
        if key not in aud:
            aud[key] = {"id": b64(os.urandom(16))}
            self.core.save_vault()
        return unb64(aud[key]["id"])

    def _needs_rotation(self, label: str, gid: bytes, current: set[str]) -> bool:
        info = self.olm.outbound_info(label, gid)
        if info is None:
            return True
        if info["count"] >= ROTATE_MESSAGES or time.time() - info["created"] >= ROTATE_SECONDS:
            return True
        # Someone who had the key isn't in the audience any more.
        return bool(set(info["shared"]) - current)

    async def publish(self) -> None:
        """Post the current status to every audience."""
        if self.core.vault is None or self.core.dms.olm is None:
            return
        for cid, conn in list(self.core.conns.items()):
            if conn.status == "online":
                with contextlib.suppress(UserError, RpcError, Offline, OlmError):
                    await self.publish_community(cid)
        await self._publish_contacts()

    # -- communities

    def _member(self, cid: str) -> tuple[bytes, keys.SigningKey | None]:
        rec = self.core.vault.data["communities"][cid]
        iid = rec["identity"]
        pub = self.core.identity_public(iid)
        try:
            return pub, self.core.identity_key(iid)
        except UserError:
            return pub, None

    def _label(self, cid: str) -> str:
        return f"status:{cid}"

    def _conn(self, cid: str):
        """The community's connection, also while the profile is locked
        (sessions keep running then, and so do statuses)."""
        c = self.core.conns.get(cid)
        if c is None or c.status != "online":
            raise Offline(_("not connected"))
        return c

    async def register_device(self, cid: str) -> None:
        """Publish this device's status device entry in a community, signed
        by the member key, on every connect (a server restored from a backup
        may not have it). The same signed bytes each time, so the server has
        nothing to announce unless the keys changed: a reconnect doesn't tell
        members anything (it matters while Invisible). Signing a new entry
        needs the profile unlocked."""
        member, signer = self._member(cid)
        label = self._label(cid)
        olm = self.olm
        curve = olm.curve(label)
        kid, key = olm.fallback_key(label)
        reg = self.d.setdefault("registered", {})
        mark = b64(curve) + ":" + kid
        saved = reg.get(cid)
        if isinstance(saved, dict) and saved.get("mark") == mark:
            entry = unb64(saved["entry"])
        else:
            if signer is None:
                return
            fk = dm.OneTimeKey(
                curve25519=curve, key_id=kid, key=key, fallback=True, signature=olm.signed_otk(label, kid, key, True)
            )
            dev = dm.StatusDevice(
                member=member,
                curve25519=curve,
                ed25519=olm.ed(label),
                fallback_key=fk,
                issued_at=int(time.time() * 1000),
            )
            raw = dev.SerializeToString()
            entry = dm.SignedStatusDevice(
                device=raw, signature=signer.sign(keys.CTX_STATUS_DEVICE, raw)
            ).SerializeToString()
            reg[cid] = {"mark": mark, "entry": b64(entry)}
            self.core.save_vault()
        await self._conn(cid).call(set_status_device=cp.SetStatusDevice(entry=entry, device=curve))

    async def community_connected(self, cid: str) -> None:
        """After (re)connecting: register this device, read everyone's
        statuses and the keys waiting for this device, then post mine."""
        if self.core.vault is None or self.core.dms.olm is None:
            return
        try:
            await self.register_device(cid)
            await self._load_community(cid)
            await self.fetch_keys(cid)
            await self.publish_community(cid)
        except (UserError, RpcError, Offline, OlmError) as e:
            log.info("statuses in a community: %s", type(e).__name__)
        self._emit()

    async def _load_community(self, cid: str) -> None:
        conn = self._conn(cid)
        after = b""
        self.devices[cid], self.blobs[cid] = {}, {}
        while True:
            reply = await conn.call(list_statuses=cp.ListStatuses(after=after), timeout=30)
            for e in reply.statuses.entries:
                self._entry(cid, e)
            if not reply.statuses.more or not reply.statuses.entries:
                break
            after = reply.statuses.entries[-1].member

    def _entry(self, cid: str, e: cp.StatusEntry) -> bool:
        """Take in one member's entry. True if their set of devices changed."""
        m = b64(e.member)
        devs = [d for d in (open_status_device(raw, e.member) for raw in e.devices[:10]) if d is not None]
        before = {d.curve25519 for d in self.devices.get(cid, {}).get(m, [])}
        self.devices.setdefault(cid, {})[m] = devs
        if not devs:
            self.devices[cid].pop(m, None)
        if e.blob:
            self.blobs.setdefault(cid, {})[m] = e.blob
        else:
            self.blobs.get(cid, {}).pop(m, None)
            self.received.get(cid, {}).pop(m, None)
        self._decrypt_blob(cid, m)
        return before != {d.curve25519 for d in devs}

    def on_entries(self, cid: str, entries) -> None:
        """Status entries pushed with the presence batch."""
        if self.core.vault is None or self.core.dms.olm is None:
            return
        grew = False
        for e in entries:
            before = {d.curve25519 for d in self.devices.get(cid, {}).get(b64(e.member), [])}
            if self._entry(cid, e):
                after = {d.curve25519 for d in self.devices.get(cid, {}).get(b64(e.member), [])}
                grew = grew or bool(after - before)
        if grew:
            # New devices get the current key now; leavers are handled by
            # the rotation before the next post.
            self.core.spawn(self._share_new(cid))
        self._emit()

    def _recipients(self, cid: str) -> list[tuple[bytes, dm.StatusDevice]]:
        member, _signer = self._member(cid)
        out = []
        for m, devs in self.devices.get(cid, {}).items():
            mb = unb64(m)
            if mb == member:
                continue
            for d in devs:
                out.append((mb, d))
        return out

    def _lock(self, key: str) -> asyncio.Lock:
        return self._publishing.setdefault(key, asyncio.Lock())

    async def publish_community(self, cid: str) -> None:
        if self.core.vault is None:
            return
        conn = self._conn(cid)
        invisible = self.effective() == "invisible"
        level = self.level(cid)
        label = self._label(cid)
        gid = self._audience(f"c:{cid}")
        async with self._lock(f"c:{cid}"):
            if level == "off":
                # Not shared: the server drops the blob (members stop seeing
                # it at once) and the session is forgotten, so sharing again
                # starts a new one.
                self.olm.drop_outbound(label, gid)
                await conn.call(set_status=cp.SetStatus(blob=b"", invisible=invisible))
                return
            if invisible:
                # Appear offline: the server stops announcing presence; the
                # stored status stays as it was, so nothing about it changes.
                await conn.call(set_status=cp.SetStatus(invisible=True, keep_blob=True))
                return
            recipients = self._recipients(cid)
            current = {b64(d.curve25519) for _m, d in recipients}
            if self._needs_rotation(label, gid, current):
                self.olm.rotate(label, gid)
            await self._send_keys(cid, recipients, only_new=True)
            member, _signer = self._member(cid)
            curve = self.olm.curve(label)
            sid, ct = self.olm.encrypt_megolm(label, gid, self._plaintext(member, curve, gid, level))
            env = dm.Envelope(megolm=dm.MegolmEnvelope(sender_curve25519=curve, session_id=sid, ciphertext=ct))
            await conn.call(set_status=cp.SetStatus(blob=MAGIC + env.SerializeToString(), invisible=False))

    async def _share_new(self, cid: str) -> None:
        if self.level(cid) == "off":
            return
        label, gid = self._label(cid), self._audience(f"c:{cid}")
        async with self._lock(f"c:{cid}"):
            if self.olm.outbound_info(label, gid) is None:
                return
            with contextlib.suppress(UserError, RpcError, Offline, OlmError):
                await self._send_keys(cid, self._recipients(cid), only_new=True)

    async def _send_keys(self, cid: str, targets: list[tuple[bytes, dm.StatusDevice]], only_new: bool) -> None:
        """The audience's current key, exported at its newest message, to each
        target device: Olm-encrypted here, stored by the server for that device."""
        label, gid = self._label(cid), self._audience(f"c:{cid}")
        olm = self.olm
        if only_new:
            shared = olm.shared_with(label, gid)
            targets = [(m, d) for m, d in targets if b64(d.curve25519) not in shared]
        if not targets:
            return
        member, _signer = self._member(cid)
        sid, key = olm.exported_key(label, gid)
        mine_curve, mine_ed = olm.curve(label), olm.ed(label)
        drops = []
        # One save for the whole batch, before anything is sent.
        with olm.batch():
            for m, d in targets:
                p = dm.OlmPayload(
                    sender_identity=member,
                    sender_ed25519=mine_ed,
                    recipient_identity=m,
                    recipient_ed25519=d.ed25519,
                    at=int(time.time() * 1000),
                    status_key=dm.StatusKey(audience=gid, session_id=sid, session_key=key),
                )
                otk = None if olm.has_session(label, d.curve25519) else d.fallback_key.key
                try:
                    mtype, ct = olm.encrypt_olm(label, d.curve25519, p.SerializeToString(), one_time_key=otk)
                except OlmError:
                    continue
                env = dm.Envelope(
                    olm=dm.OlmEnvelope(
                        sender_curve25519=mine_curve, recipient_curve25519=d.curve25519, type=mtype, ciphertext=ct
                    )
                )
                drops.append(cp.StatusKeyDrop(member=m, device=d.curve25519, envelope=MAGIC + env.SerializeToString()))
        conn = self._conn(cid)
        for i in range(0, len(drops), SEND_CHUNK):
            await conn.call(send_status_keys=cp.SendStatusKeys(drops=drops[i : i + SEND_CHUNK]), timeout=60)
        olm.mark_shared(label, gid, [d.device for d in drops])

    async def on_key_request(self, cid: str, req: cp.StatusKeyRequest) -> None:
        """A member's device can't read my status: send it the current key
        again, if it is one of theirs and they're in my audience."""
        if self.core.vault is None or self.level(cid) == "off" or self.effective() == "invisible":
            return
        devs = self.devices.get(cid, {}).get(b64(req.member), [])
        targets = [(req.member, d) for d in devs if d.curve25519 == req.device]
        if not targets:
            return
        async with self._lock(f"c:{cid}"):
            if self.olm.outbound_info(self._label(cid), self._audience(f"c:{cid}")) is None:
                return
            with contextlib.suppress(UserError, RpcError, Offline, OlmError):
                await self._send_keys(cid, targets, only_new=False)

    # -- receiving in a community

    async def fetch_keys(self, cid: str) -> None:
        conn = self._conn(cid)
        curve = self.olm.curve(self._label(cid))
        ack: list[int] = []
        for _round in range(50):
            reply = await conn.call(fetch_status_keys=cp.FetchStatusKeys(ack=ack, device=curve), timeout=30)
            ack = self._take_keys(cid, reply.status_keys.items)
            if not reply.status_keys.more:
                break
        if ack:
            await conn.call(fetch_status_keys=cp.FetchStatusKeys(ack=ack, device=curve), timeout=30)

    def on_keys(self, cid: str, items) -> None:
        """Key drops pushed by the server: handle mine, then acknowledge."""
        if self.core.vault is None or self.core.dms.olm is None:
            return
        ack = self._take_keys(cid, items)
        if ack:
            conn = self.core.conns.get(cid)
            curve = self.olm.curve(self._label(cid))

            async def send_ack() -> None:
                with contextlib.suppress(RpcError, Offline):
                    await conn.call(fetch_status_keys=cp.FetchStatusKeys(ack=ack, device=curve), timeout=30)

            if conn is not None:
                self.core.spawn(send_ack())
        self._emit()

    def _take_keys(self, cid: str, items) -> list[int]:
        """Decrypt the key drops for this device; the ids to acknowledge.
        A drop from a device not (yet) known to be the sender's is left on
        the server, undecrypted, for a later fetch."""
        label = self._label(cid)
        olm = self.olm
        mine = olm.curve(label)
        member, _signer = self._member(cid)
        ack, touched = [], set()
        with olm.batch():
            for it in items:
                if it.device != mine:
                    continue
                try:
                    env = dm.Envelope.FromString(it.envelope[len(MAGIC) :]) if it.envelope.startswith(MAGIC) else None
                except DecodeError:
                    env = None
                if env is None or not env.HasField("olm") or env.olm.recipient_curve25519 != mine:
                    ack.append(it.id)
                    continue
                sender_devs = {d.curve25519: d for d in self.devices.get(cid, {}).get(b64(it.sender), [])}
                dev = sender_devs.get(env.olm.sender_curve25519)
                if dev is None:
                    continue
                ack.append(it.id)
                try:
                    p = dm.OlmPayload.FromString(
                        olm.decrypt_olm(label, env.olm.sender_curve25519, env.olm.type, env.olm.ciphertext)
                    )
                except OlmError, DecodeError:
                    log.info("statuses: a key message didn't decrypt")
                    continue
                if (
                    p.WhichOneof("body") != "status_key"
                    or p.recipient_identity != member
                    or p.recipient_ed25519 != olm.ed(label)
                    or p.sender_identity != it.sender
                    or p.sender_ed25519 != dev.ed25519
                ):
                    log.info("statuses: a key message wasn't bound to its sender and this device")
                    continue
                k = p.status_key
                try:
                    olm.add_inbound_exported(
                        label, k.session_id, k.session_key, k.audience, dev.curve25519, dev.ed25519
                    )
                except OlmError:
                    log.info("statuses: refused a status key")
                    continue
                touched.add(b64(it.sender))
        for m in touched:
            self._decrypt_blob(cid, m)
        return ack

    def _decrypt_blob(self, cid: str, m: str) -> None:
        blob = self.blobs.get(cid, {}).get(m)
        if not blob:
            return
        try:
            env = dm.Envelope.FromString(blob[len(MAGIC) :]) if blob.startswith(MAGIC) else None
        except DecodeError:
            env = None
        if env is None or not env.HasField("megolm"):
            return
        meg = env.megolm
        devs = {d.curve25519 for d in self.devices.get(cid, {}).get(m, [])}
        if meg.sender_curve25519 not in devs:
            return
        info = self.olm.inbound_for(meg.session_id, meg.sender_curve25519)
        if info is None or info.get("label") != self._label(cid):
            self._ask_for_key(cid, m, meg.session_id)
            return
        try:
            d = self.olm.decrypt_megolm(meg.session_id, meg.sender_curve25519, meg.ciphertext)
            ev = dm.DmEvent.FromString(d.plaintext)
        except OlmError, DecodeError:
            return
        if (
            not ev.HasField("status")
            or ev.sender != unb64(m)
            or ev.group_id != d.group_id
            or ev.sender_device != meg.sender_curve25519
            or ev.at > time.time() * 1000 + MAX_CLOCK_SKEW_MS
            or not valid_received(ev.status)
        ):
            return
        prev = self.received.get(cid, {}).get(m)
        if prev is not None and prev["at"] > ev.at:
            # An older status (replayed): keep the newer one.
            return
        if ev.status.state == dm.STATUS_STATE_CLEARED:
            self.received.get(cid, {}).pop(m, None)
            return
        self.received.setdefault(cid, {})[m] = received_view(ev.status, ev.at)

    def _ask_for_key(self, cid: str, m: str, session_id: str) -> None:
        """No key for a member's status after the waiting ones were read: ask
        their devices (not while Invisible: asking shows the server and them
        that this device is here)."""
        if self.effective() == "invisible":
            return
        last = self._asked.get((cid, m))
        if last is not None and last[0] == session_id and time.time() - last[1] < KEY_REQUEST_EVERY:
            return
        self._asked[(cid, m)] = (session_id, time.time())
        conn = self.core.conns.get(cid)
        if conn is None or conn.status != "online":
            return
        curve = self.olm.curve(self._label(cid))

        async def ask() -> None:
            # Keys already waiting on the server come first.
            await asyncio.sleep(5)
            if self.olm.inbound.get(session_id) is not None:
                return
            with contextlib.suppress(RpcError, Offline):
                await conn.call(request_status_key=cp.RequestStatusKey(member=unb64(m), device=curve))

        self.core.spawn(ask())

    def community_left(self, cid: str) -> None:
        for table in (self.devices, self.blobs, self.received):
            table.pop(cid, None)
        if self.core.vault is not None:
            self.d.setdefault("registered", {}).pop(cid, None)
            self.d.setdefault("aud", {}).pop(f"c:{cid}", None)
            if self.core.dms.olm is not None:
                self.core.dms.olm.drop_account(self._label(cid))
            self.core.save_vault()
        self._emit()

    # -- contacts

    def _contact_targets(self) -> dict[str, list[tuple[bytes, dm.DmDevice]]]:
        """My DM identity -> the devices of the contacts who should see my
        status (all, or the chosen ones), grouped by the identity they know
        me by."""
        s = self.core.settings
        dms = self.core.dms
        if s.get("status_contacts", "full") == "off":
            return {}
        chosen = set(s.get("status_contacts_chosen", []))
        everyone = s.get("status_contacts_scope", "all") == "all"
        out: dict[str, list[tuple[bytes, dm.DmDevice]]] = {}
        communities = self.core.vault.data["communities"]
        for ident, c in dms.d["contacts"].items():
            if not everyone and ident not in chosen:
                continue
            if ident in dms.d["blocked"]:
                continue
            # The identity they know me by: our 1:1 conversation's, else the
            # one of the community they're a contact from, else (added with a
            # contact code, no community) my contact-code identity.
            gid = dms._direct_with(ident)
            pub = (
                (dms.d["groups"][gid]["me"] if gid else "")
                or communities.get(c.get("community", ""), {}).get("dm_identity", "")
                or ("" if c.get("community") else dms.d.get("contact_identity", ""))
            )
            if not pub or pub not in dms.d["ids"]:
                continue
            for dev in dms._devices_of(ident):
                out.setdefault(pub, []).append((unb64(ident), dev))
        return out

    def _contacts_shrank(self) -> bool:
        if self.core.vault is None or self.core.dms.olm is None:
            return False
        targets = self._contact_targets()
        for key, a in self.d.get("aud", {}).items():
            if not key.startswith("k:"):
                continue
            pub = key[2:]
            info = self.olm.outbound_info(f"dm:{pub}", unb64(a["id"]))
            if info is None:
                continue
            current = {b64(d.curve25519) for _i, d in targets.get(pub, [])}
            if set(info["shared"]) - current:
                return True
        return False

    async def _publish_contacts(self, force_state: str = "") -> None:
        if self.core.vault is None or self.core.dms.olm is None:
            return
        self._last_refresh = time.time()
        dms = self.core.dms
        targets = self._contact_targets()
        level = self.core.settings.get("status_contacts", "full")
        state = force_state or self.effective()
        keys_seen = {f"k:{pub}" for pub in targets}
        # Audiences no longer shared with anyone: tell those who had the key
        # it's cleared, then forget the session.
        for key, a in list(self.d.get("aud", {}).items()):
            if key.startswith("k:") and key not in keys_seen:
                await self._clear_contacts(key[2:], unb64(a["id"]), [])
        for pub, recipients in targets.items():
            gid = self._audience(f"k:{pub}")
            label = f"dm:{pub}"
            async with self._lock(f"k:{pub}"):
                current = {b64(d.curve25519) for _i, d in recipients}
                info = self.olm.outbound_info(label, gid)
                if info is not None and set(info["shared"]) - current:
                    # Someone was removed: they get "cleared" on the old
                    # session, and everyone else a new one.
                    await self._clear_contacts(pub, gid, recipients)
                if self._needs_rotation(label, gid, current):
                    self.olm.rotate(label, gid)
                sid, key = self.olm.exported_key(label, gid)
                shared = self.olm.shared_with(label, gid)
                done = []
                for ident, dev in recipients:
                    if b64(dev.curve25519) in shared:
                        continue
                    payload = dm.OlmPayload(status_key=dm.StatusKey(audience=gid, session_id=sid, session_key=key))
                    if await dms._send_olm(pub, ident, dev, payload):
                        done.append(dev.curve25519)
                self.olm.mark_shared(label, gid, done)
                curve = self.olm.curve(label)
                sid, ct = self.olm.encrypt_megolm(
                    label, gid, self._plaintext(unb64(pub), curve, gid, level, force_state=state)
                )
                env = dm.Envelope(megolm=dm.MegolmEnvelope(sender_curve25519=curve, session_id=sid, ciphertext=ct))
                await self._drop(pub, MAGIC + env.SerializeToString(), recipients)

    async def _clear_contacts(self, pub: str, gid: bytes, keep: list[tuple[bytes, dm.DmDevice]]) -> None:
        """Post "cleared" on the old session to the devices that had it but
        aren't in `keep`, then forget the session."""
        label = f"dm:{pub}"
        info = self.olm.outbound_info(label, gid)
        if info is None:
            if not keep:
                self.d.get("aud", {}).pop(f"k:{pub}", None)
            return
        kept = {b64(d.curve25519) for _i, d in keep}
        gone = set(info["shared"]) - kept
        dms = self.core.dms
        devices = []
        for ident in list(dms.d["contacts"]) + list(dms.d["peers"]):
            for dev in dms._devices_of(ident):
                if b64(dev.curve25519) in gone:
                    devices.append((unb64(ident), dev))
        if devices:
            curve = self.olm.curve(label)
            sid, ct = self.olm.encrypt_megolm(label, gid, self._plaintext(unb64(pub), curve, gid, "off"))
            env = dm.Envelope(megolm=dm.MegolmEnvelope(sender_curve25519=curve, session_id=sid, ciphertext=ct))
            await self._drop(pub, MAGIC + env.SerializeToString(), devices)
        self.olm.drop_outbound(label, gid)
        if not keep:
            self.d.get("aud", {}).pop(f"k:{pub}", None)
            self.core.save_vault()

    async def _drop(self, pub: str, blob: bytes, recipients) -> None:
        dms = self.core.dms
        for ident, dev in recipients:
            inbox = dms._first_inbox(ident, dev.curve25519)
            if inbox is None:
                continue
            try:
                await self.core.meta.drop(inbox, blob)
            except MetaError:
                log.info("statuses: a mailbox drop failed")

    # -- receiving from contacts (dm.py hands these over)

    def refused(self, session_id: str) -> bool:
        return session_id in self._refused

    async def _refuse(self, session_id: str) -> None:
        """A status key this client won't take (from a stranger or a blocked
        person): its statuses are dropped instead of waiting for a key."""
        self._refused[session_id] = time.time()
        while len(self._refused) > 1000:
            self._refused.pop(next(iter(self._refused)))
        await self.core.dms._drain_waiting(session_id, drop=True)

    async def on_dm_status_key(self, pub: str, sender_curve: bytes, p: dm.OlmPayload) -> None:
        """A status session key over Olm from someone I know through DMs.
        Statuses are taken only from people this client deals with (a
        contact, or someone in a conversation with me), not blocked, from a
        device in their signed device list."""
        dms = self.core.dms
        k = p.status_key
        known = dms._knows(p.sender_identity) and not dms._blocked(p.sender_identity)
        if not known or not dms._device_known(p.sender_identity, sender_curve, p.sender_ed25519):
            await self._refuse(k.session_id)
            return
        if len(k.audience) != 16 or k.audience.hex() in dms.d["groups"]:
            await self._refuse(k.session_id)
            return
        try:
            self.olm.add_inbound_exported(
                f"dm:{pub}", k.session_id, k.session_key, k.audience, sender_curve, p.sender_ed25519
            )
        except OlmError:
            log.info("statuses: refused a contact's status key")
            return
        await dms._drain_waiting(k.session_id)

    def on_dm_status(self, label: str, sender_curve: bytes, sender_ed: bytes, ev: dm.DmEvent) -> None:
        """A contact's status, decrypted by dm.py from their status session."""
        dms = self.core.dms
        if not label.startswith("dm:") or ev.group_id.hex() in dms.d["groups"]:
            return
        if (
            not dms._knows(ev.sender)
            or dms._blocked(ev.sender)
            or not dms._device_known(ev.sender, sender_curve, sender_ed)
        ):
            return
        if ev.at > time.time() * 1000 + MAX_CLOCK_SKEW_MS or not valid_received(ev.status):
            return
        recv = self.d.setdefault("recv", {})
        ident = b64(ev.sender)
        prev = recv.get(ident)
        if prev is not None and prev.get("at", 0) > ev.at:
            return
        if ev.status.state == dm.STATUS_STATE_CLEARED:
            recv.pop(ident, None)
        else:
            recv[ident] = received_view(ev.status, ev.at)
            # Bounded: the newest 5000 people.
            if len(recv) > 5000:
                for old in sorted(recv, key=lambda k: recv[k].get("at", 0))[: len(recv) - 5000]:
                    recv.pop(old, None)
        self.core.save_vault()
        self._emit()

    def contact_removed(self, ident: str) -> None:
        if self.core.vault is not None:
            self.d.setdefault("recv", {}).pop(ident, None)
