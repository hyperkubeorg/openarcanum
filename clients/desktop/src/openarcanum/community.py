"""One joined community: relay connection, Noise session, requests, channel
topic subscriptions, failover and catch-up.

A CommunityConnection keeps itself connected while the community is active
or pending. It tries the preferred relays from settings first, then the relays in
the community's route; when all of them fail it asks the maintainer nodes for
a fresh route (which may carry a new server key certified by an owner).
"""

from __future__ import annotations

import asyncio
import contextlib
import itertools
import logging
import os
import random
import time
from collections.abc import Callable
from typing import TYPE_CHECKING, Any

from google.protobuf.message import DecodeError

from openarcanum import keys
from openarcanum.crypto import DecryptError, decrypt_with_nonce
from openarcanum.i18n import N_, _
from openarcanum.keys import SigningKey, StaticKey
from openarcanum.names import safe_name
from openarcanum.owners import OwnerSet, check_known, remember, verify_cert
from openarcanum.relay import Relay, RelayBlocked, RelayError, StreamClosed
from openarcanum.session import IdentityError, Session, SessionError, dial
from openarcanum.tasks import spawn
from openarcanum.wire import community_pb2 as cp
from openarcanum.wire import session_pb2 as sp

if TYPE_CHECKING:
    from openarcanum.core import Core

log = logging.getLogger(__name__)

TERMINAL = {
    cp.MEMBER_STATUS_REMOVED: "removed",
    cp.MEMBER_STATUS_BANNED: "banned",
    cp.MEMBER_STATUS_DEVICE_REVOKED: "device_revoked",
    cp.MEMBER_STATUS_DEVICE_LIMIT: "device_limit",
}

REFUSALS = {
    cp.REFUSAL_REASON_KEY_INVALID: N_("That invite isn't valid for this community."),
    cp.REFUSAL_REASON_KEY_REVOKED: N_("That invite has been revoked."),
    cp.REFUSAL_REASON_KEY_EXPIRED: N_("That invite has expired."),
    cp.REFUSAL_REASON_KEY_EXHAUSTED: N_("That invite has been used up."),
    cp.REFUSAL_REASON_BANNED: N_("You're banned from this community."),
    cp.REFUSAL_REASON_FULL: N_("This community is full."),
    cp.REFUSAL_REASON_INVITE_REQUIRED: N_("This community needs an invite to join."),
    cp.REFUSAL_REASON_DENIED: N_("Your request to join was declined."),
}


class RpcError(Exception):
    def __init__(self, code: int, message: str):
        super().__init__(message)
        self.code = code
        self.code_name = cp.RpcErrorCode.Name(code).removeprefix("RPC_ERROR_CODE_").lower() if code else "error"


class Offline(Exception):
    pass


# How often a client that can't reach a community asks the maintainers about
# it, and how often it tries a community they shut down.
STATUS_EVERY = 20.0
SHUT_DOWN_RECHECK = 300.0


def channel_aad(channel_id: int, epoch: int) -> bytes:
    return b"openarcanum/channel-event/v1" + channel_id.to_bytes(8, "big", signed=True) + epoch.to_bytes(4, "big")


def file_chunk_aad(file_id: int, idx: int) -> bytes:
    return b"openarcanum/file-chunk/v1" + file_id.to_bytes(8, "big", signed=True) + idx.to_bytes(4, "big")


def file_chunk_nonce(idx: int) -> bytes:
    return b"\x00" * 8 + idx.to_bytes(4, "big")


def file_dict(f: cp.FileRef) -> dict:
    return {
        "file_id": f.file_id,
        # Someone else chose it: one safe component, no bidi tricks (names.py).
        "name": safe_name(f.name),
        "size": f.size,
        "mime": f.mime,
        "key": keys.encode(f.key) if f.key else "",
        "chunk_size": f.chunk_size,
        "expired": not f.key,
    }


SYSTEM_KINDS = {cp.SYSTEM_EVENT_KIND_JOINED: "joined", cp.SYSTEM_EVENT_KIND_LEFT: "left"}
# Channels that take messages: text channels, and voice channels (their
# text chat, docs/protocol.md §4.3).
MESSAGE_KINDS = (cp.CHANNEL_KIND_TEXT, cp.CHANNEL_KIND_VOICE)


def reactions_list(reactions: Any) -> list[dict]:
    return [{"emoji": r.emoji, "members": [keys.encode(k) for k in r.members]} for r in reactions]


def preview_dict(p: cp.LinkPreview) -> dict:
    return {
        "url": p.url,
        "title": p.title,
        "description": p.description,
        "site_name": p.site_name,
        "image": file_dict(p.image) if p.HasField("image") else None,
    }


def update_dict(u: cp.MessageUpdate) -> dict | None:
    """A MessageUpdate (reactions, pin state or link preview; never the
    message text) as the fields it changes, with the message's id and
    channel. None for a change this client doesn't know."""
    change = u.WhichOneof("change")
    d: dict[str, Any] = {"id": u.message_id, "channel_id": u.channel_id}
    if change == "reactions":
        d["reactions"] = reactions_list(u.reactions.reactions)
    elif change == "pinned":
        d["pinned"] = u.pinned
    elif change == "preview":
        d["preview"] = preview_dict(u.preview)
    else:
        return None
    return d


def message_dict(m: cp.Message) -> dict:
    d: dict[str, Any] = {
        "id": m.id,
        "channel_id": m.channel_id,
        "author": keys.encode(m.author),
        "author_name": m.author_name,
        "text": m.text,
        "created_at": m.created_at,
        "edited_at": m.edited_at,
        "deleted": m.deleted,
        "reply_to": m.reply_to,
        "files": [file_dict(f) for f in m.files],
        "mentions": [keys.encode(k) for k in m.mentions],
        "mention_roles": list(m.mention_roles),
        "mention_everyone": m.mention_everyone,
        "mention_here": m.mention_here,
        "reactions": reactions_list(m.reactions),
        "pinned": m.pinned,
        "preview": preview_dict(m.preview) if m.HasField("preview") else None,
        "system": None,
    }
    if m.HasField("system"):
        d["system"] = {"kind": SYSTEM_KINDS.get(m.system.kind, "unknown"), "member": keys.encode(m.system.member)}
    return d


class Session2:
    """A connected relay + Noise session + request plumbing. Used both by the
    long-lived CommunityConnection and for one-off sessions (preview, join,
    claim)."""

    def __init__(self, relay: Relay, session: Session, status: cp.SessionStatus, on_event: Callable | None = None):
        self.relay = relay
        self.session = session
        self.status = status
        self.server_identity: sp.ServerIdentity = session.server  # type: ignore[assignment]
        self._ids = itertools.count(1)
        self._pending: dict[int, asyncio.Future] = {}
        self._on_event = on_event
        self.closed = asyncio.Event()
        self._reader = asyncio.create_task(self._read_loop())

    @classmethod
    async def open(
        cls,
        relay_url: str,
        server_key: bytes,
        identity_pub: bytes,
        static: StaticKey,
        static_sig: bytes,
        device_name: str,
        verify_server: Callable[[sp.ServerIdentity], None],
        on_event: Callable | None = None,
        timeout: float = 20.0,
    ) -> Session2:
        relay = await Relay(relay_url).connect(timeout=timeout)
        try:
            stream = await relay.open_stream(server_key, timeout=timeout)
            sess = await dial(stream, static, identity_pub, static_sig, device_name, verify_server, timeout=timeout)
            first = cp.ServerMessage()
            first.ParseFromString(await sess.recv(timeout))
            if not first.HasField("status"):
                raise SessionError("server did not report a session status")
        except BaseException:
            await relay.close()
            raise
        return cls(relay, sess, first.status, on_event)

    async def close(self) -> None:
        self._reader.cancel()
        with contextlib.suppress(BaseException):
            await self._reader
        await self.relay.close()
        self.closed.set()

    async def _read_loop(self) -> None:
        try:
            while True:
                raw = await self.session.recv()
                m = cp.ServerMessage()
                try:
                    m.ParseFromString(raw)
                except DecodeError:
                    log.info("dropping a server message that doesn't parse (%d bytes)", len(raw))
                    continue
                if m.reply_to:
                    fut = self._pending.pop(m.reply_to, None)
                    if fut and not fut.done():
                        fut.set_result(m)
                elif self._on_event is not None:
                    try:
                        r = self._on_event(m)
                        if asyncio.iscoroutine(r):
                            await r
                    except Exception:
                        log.exception("event handler failed")
        except StreamClosed, SessionError, RelayError, asyncio.CancelledError:
            pass
        except Exception:
            log.exception("session reader failed")
        finally:
            self.closed.set()
            for fut in self._pending.values():
                if not fut.done():
                    fut.set_exception(Offline(_("connection lost")))
            self._pending.clear()

    async def call(self, timeout: float = 30.0, **body) -> cp.ServerMessage:
        if self.closed.is_set():
            raise Offline(_("not connected"))
        mid = next(self._ids)
        msg = cp.ClientMessage(id=mid, **body)
        fut = asyncio.get_running_loop().create_future()
        self._pending[mid] = fut
        try:
            await self.session.send(msg.SerializeToString())
            reply: cp.ServerMessage = await asyncio.wait_for(fut, timeout)
        except (StreamClosed, RelayError, SessionError) as e:
            raise Offline(_("connection lost")) from e
        except TimeoutError as e:
            raise Offline(_("the community server didn't answer in time")) from e
        finally:
            self._pending.pop(mid, None)
        if reply.HasField("error"):
            raise RpcError(reply.error.code, reply.error.message)
        return reply


def sign_action(identity: SigningKey, community: bytes, op_field: str, op: Any) -> cp.SignedAction:
    action = cp.AdminAction(
        community=community, actor=identity.public, at=int(time.time() * 1000), nonce=os.urandom(16), **{op_field: op}
    )
    raw = action.SerializeToString()
    return cp.SignedAction(action=raw, signature=identity.sign(keys.CTX_ADMIN_ACTION, raw))


def make_verifier(
    rec: dict, on_new_key: Callable[[bytes, int], None] | None = None
) -> Callable[[sp.ServerIdentity], None]:
    """Pin the server: either the key we know, or a new key that a key in the
    community's current owner set certified (key rotation, new hardware). The
    owner set must be the one this client last saw, or a later one."""
    pinned = keys.decode(rec["server_key"])
    community = keys.decode(rec["owner_key"]) if rec.get("owner_key") else None

    def verify(si: sp.ServerIdentity) -> None:
        if si.server_key == pinned:
            if community is not None and si.HasField("cert"):
                try:
                    _cert, owner_set = verify_cert(si.cert, si.server_key, community)
                    check_known(owner_set, rec)
                except keys.BadSignature as e:
                    raise IdentityError(_("the server's ownership proof isn't from an owner of this community")) from e
            return
        if community is None or not si.HasField("cert"):
            raise IdentityError(_("the server's key doesn't match the one this community had before"))
        try:
            cert, owner_set = verify_cert(si.cert, si.server_key, community)
            check_known(owner_set, rec)
        except keys.BadSignature as e:
            raise IdentityError(_("the server's new key isn't vouched for by an owner of this community")) from e
        if cert.seq < rec.get("cert_seq", 0):
            raise IdentityError(_("the server's ownership proof is out of date"))
        if on_new_key is not None:
            on_new_key(si.server_key, cert.seq)

    return verify


class CommunityConnection:
    def __init__(self, core: Core, cid: str):
        self.core = core
        self.cid = cid
        self.s: Session2 | None = None
        self.relay_url = ""
        self.state: cp.SyncState | None = None
        # Who's in each voice channel this member can see (server push).
        self.voice_rooms = cp.VoiceRooms()
        self.status = "connecting"
        self.error = ""
        self.channel_keys: dict[int, dict[int, bytes]] = {}
        self._keys_refreshed = 0.0
        self.topics: dict[bytes, int] = {}
        # Topics this connection subscribed to before: re-subscribing after a
        # reconnect asks the relay's chunk cache for what was missed. A first
        # subscription never replays (a new member doesn't get events from
        # before it could read the channel).
        self._subscribed_before: set[bytes] = set()
        self.server_keys: list[bytes] = []
        self.owner_set: OwnerSet | None = None
        # What the maintainer nodes say about the server while this client
        # can't reach it (meta.status_dict; docs/protocol.md §7.4).
        self.server_status: dict | None = None
        self._status_checked = 0.0
        self._task: asyncio.Task | None = None
        self._stop = asyncio.Event()
        self._wake = asyncio.Event()
        self.connected = asyncio.Event()

    @property
    def rec(self) -> dict:
        return self.core.vault.data["communities"][self.cid]

    # ---------------------------------------------------------------- lifecycle

    def start(self) -> None:
        if self._task is None or self._task.done():
            self._stop.clear()
            self._task = asyncio.create_task(self._run(), name=f"community {self.cid[:8]}")

    async def stop(self) -> None:
        self._stop.set()
        self._wake.set()
        if self.s is not None:
            await self.s.close()
        if self._task is not None:
            self._task.cancel()
            with contextlib.suppress(BaseException):
                await self._task
        self.connected.clear()

    def reconnect_now(self) -> None:
        self._wake.set()
        if self.s is not None:
            spawn(self.s.close())

    def _set_status(self, status: str, error: str = "") -> None:
        if status != self.status or error != self.error:
            self.status, self.error = status, error
            self.core.community_changed(self.cid)

    def _relay_order(self) -> list[str]:
        relays = list(self.rec.get("relays") or [])
        random.shuffle(relays)
        preferred = [r for r in self.core.settings.get("preferred_relays", []) if r in relays]
        # Prefer the relay we were on, then preferred ones, then the rest.
        order = [r for r in [self.relay_url] if r in relays] + preferred + relays
        return list(dict.fromkeys(order))

    async def _run(self) -> None:
        backoff = 0.5
        failures = 0
        while not self._stop.is_set():
            ok = False
            version_error = ""
            blocked = False
            for url in self._relay_order():
                if self._stop.is_set():
                    return
                try:
                    await self._connect(url)
                    ok = True
                except (RelayError, SessionError, Offline, OSError, TimeoutError) as e:
                    log.info("community %s: relay %s failed: %s", self.cid[:8], url, e)
                    if isinstance(e, IdentityError):
                        self._set_status("error", str(e))
                    if getattr(e, "mismatch", None) is not None:
                        # No protocol version in common (docs/protocol.md
                        # §1): say who needs updating, keep trying.
                        version_error = str(e)
                    blocked = blocked or isinstance(e, RelayBlocked)
                    continue
                except Exception:
                    log.exception("community %s: connect failed", self.cid[:8])
                    continue
                break
            if ok and self.s is not None:
                backoff, failures = 0.5, 0
                if self.server_status is not None:
                    self.server_status = None
                await self._serve()
                if self.status in TERMINAL.values():
                    return
            else:
                failures += 1
                # Relay failover (docs/protocol.md §7.4): the server may have
                # moved to other relays and published a new route.
                await self._refresh_route()
                await self._check_status(force=failures == 1)
                if self.server_status and self.server_status["state"] == "terminated":
                    self._set_status("shut_down", _("This community was shut down."))
                elif self.status not in ("error",):
                    reason = _("Can't reach the community right now.")
                    if blocked:
                        reason = _("The community refuses connections from your network.")
                    self._set_status("offline", version_error or reason)
            if self._stop.is_set():
                return
            self._wake.clear()
            wait = backoff + random.random() * backoff / 2
            if self.status == "shut_down":
                # Shut down by the maintainers: look again now and then, in
                # case its operator brings it back.
                wait = SHUT_DOWN_RECHECK
            with contextlib.suppress(TimeoutError):
                await asyncio.wait_for(self._wake.wait(), wait)
            backoff = min(backoff * 2, 30.0)

    async def _check_status(self, force: bool = False) -> None:
        """Ask the maintainer nodes whether the server is reporting (at most
        every STATUS_EVERY seconds), so the community can be shown as
        offline since a time, degraded, or shut down."""
        rec = self.rec
        now = time.monotonic()
        if not rec.get("route_id") or (not force and now - self._status_checked < STATUS_EVERY):
            return
        self._status_checked = now
        try:
            st = await self.core.meta.status(keys.decode(rec["route_id"]), keys.decode(rec["server_key"]))
        except Exception as e:
            log.info("community %s: status lookup failed: %s", self.cid[:8], e)
            return
        if st != self.server_status:
            self.server_status = st
            self.core.community_changed(self.cid)

    async def _refresh_route(self) -> None:
        rec = self.rec
        if not rec.get("route_id") or not rec.get("owner_key"):
            return
        try:
            found = await self.core.meta.route(keys.decode(rec["route_id"]), keys.decode(rec["owner_key"]))
        except Exception as e:
            log.info("route lookup failed: %s", e)
            return
        if found is None:
            return
        route, cert, owner_set = found
        try:
            check_known(owner_set, rec)
        except keys.BadSignature:
            log.warning("community %s: ignoring a route under an older owner set", self.cid[:8])
            return
        changed = False
        if list(route.relays) != rec.get("relays"):
            rec["relays"] = list(route.relays)
            changed = True
        if route.server_key != keys.decode(rec["server_key"]) and cert.seq >= rec.get("cert_seq", 0):
            rec["server_key"] = keys.encode(route.server_key)
            rec["cert_seq"] = cert.seq
            changed = True
        if changed:
            self.core.save_vault()

    def _accept_new_key(self, key: bytes, seq: int) -> None:
        rec = self.rec
        rec["server_key"] = keys.encode(key)
        rec["cert_seq"] = seq
        self.core.save_vault()

    async def _connect(self, url: str) -> None:
        rec = self.rec
        dev = rec["device"]
        s = await Session2.open(
            url,
            keys.decode(rec["server_key"]),
            self.core.identity_public(rec["identity"]),
            StaticKey(keys.decode(dev["static"])),
            keys.decode(dev["sig"]),
            self.core.device_name,
            make_verifier(rec, self._accept_new_key),
            on_event=self._on_event,
        )
        self.s, self.relay_url = s, url
        self.server_keys = [s.server_identity.server_key]
        # A relay dedicated to this community carries nothing else
        # (docs/protocol.md §2 "Dedicated relays"): remember it, so DM groups
        # and device sync don't pick it.
        hello = s.relay.hello
        dedicated = set(rec.get("dedicated_relays", []))
        if hello is not None and hello.dedicated_to:
            dedicated.add(url)
        else:
            dedicated.discard(url)
        if sorted(dedicated) != rec.get("dedicated_relays", []):
            rec["dedicated_relays"] = sorted(dedicated)
            self.core.save_vault()

    async def _serve(self) -> None:
        assert self.s is not None
        s = self.s
        try:
            st = s.status.status
            if st in TERMINAL:
                await self._terminal(st)
                return
            if st == cp.MEMBER_STATUS_UNKNOWN:
                # We were on the roll, now we aren't: removed (or denied).
                await self._terminal(cp.MEMBER_STATUS_REMOVED)
                return
            if st == cp.MEMBER_STATUS_PENDING:
                if self.rec.get("status") != "pending":
                    self.rec["status"] = "pending"
                    self.core.save_vault()
                self._set_status("pending")
            else:
                await self._went_active()
            self.connected.set()
            await s.closed.wait()
        finally:
            self.connected.clear()
            with contextlib.suppress(Exception):
                await s.close()
            if self.s is s:
                self.s = None
            self.topics.clear()

    async def _went_active(self) -> None:
        assert self.s is not None
        if self.rec.get("status") != "active":
            self.rec["status"] = "active"
            self.core.save_vault()
        reply = await self.s.call(sync=cp.Sync())
        await self._apply_state(reply.state)
        self._set_status("online")
        await self._catch_up()
        await self.core.after_connect(self.cid)

    def _refresh_keys(self) -> None:
        now = time.monotonic()
        if now - self._keys_refreshed < 10:
            return
        self._keys_refreshed = now
        self.core.spawn(self._reload_quietly())

    async def _reload_quietly(self) -> None:
        try:
            await self.reload_state()
        except (RpcError, Offline) as e:
            log.info("couldn't refresh channel keys: %s", e)

    async def reload_state(self) -> None:
        """Fetch and apply a fresh SyncState (after this client's own change)."""
        reply = await self.call(sync=cp.Sync())
        await self._apply_state(reply.state)

    async def _terminal(self, st: int) -> None:
        name = TERMINAL.get(st, "removed")
        self._stop.set()
        self._set_status(name)
        await self.core.community_ended(self.cid, name)

    async def _on_event(self, m: cp.ServerMessage) -> None:
        kind = m.WhichOneof("body")
        if kind == "state_changed":
            sc = m.state_changed
            if sc.status in TERMINAL:
                await self._terminal(sc.status)
                return
            if sc.status == cp.MEMBER_STATUS_ACTIVE and self.status == "pending":
                if sc.HasField("state"):
                    await self._apply_state(sc.state)
                self.rec["status"] = "active"
                self.core.save_vault()
                self._set_status("online")
                await self.core.after_connect(self.cid)
                self.core.emit(
                    "toast",
                    {
                        "text": _("You've been admitted to {community}.").format(
                            community=self.rec.get("name", _("the community"))
                        )
                    },
                )
                return
            if sc.HasField("state"):
                await self._apply_state(sc.state)
        elif kind == "voice_update":
            await self.core.voice_update(self.cid, m.voice_update.session)
        elif kind == "voice_rooms":
            self.voice_rooms = m.voice_rooms
            self.core.emit("voice_rooms", {"community": self.cid, "rooms": self.core.voice.rooms(self.cid)})
        elif kind == "presence_batch":
            for p in m.presence_batch.updates:
                self.core.presence(self.cid, keys.encode(p.member), p.online)
            if m.presence_batch.statuses:
                self.core.statuses.on_entries(self.cid, m.presence_batch.statuses)
        elif kind == "status_keys":
            # Status keys for this member's devices (statuses.py).
            self.core.statuses.on_keys(self.cid, m.status_keys.items)
        elif kind == "status_key_request":
            self.core.spawn(self.core.statuses.on_key_request(self.cid, m.status_key_request))
        elif kind == "mod_alert":
            self.core.moderation.mod_alert(self.cid, m.mod_alert)
        elif kind == "mod_queue":
            # The moderator queues' counts changed (docs/protocol.md §4.9).
            if self.state is not None:
                self.state.mod_queue.CopyFrom(m.mod_queue)
                self.core.community_changed(self.cid)

    # ---------------------------------------------------------------- state & topics

    async def _apply_state(self, state: cp.SyncState) -> None:
        self.state = state
        rec = self.rec
        changed = False
        if state.info.name and rec.get("name") != state.info.name:
            rec["name"] = state.info.name
            changed = True
        # A pinned relay list in the community settings is where the server
        # will be; remember it so reconnects find it.
        if state.settings.relay_mode == cp.RELAY_MODE_PINNED and state.settings.pinned_relays:
            merged = list(dict.fromkeys([*state.settings.pinned_relays, *rec.get("relays", [])]))
            if merged != rec.get("relays"):
                rec["relays"] = merged
                changed = True
        if state.self.display_name and rec.get("display_name") != state.self.display_name:
            rec["display_name"] = state.self.display_name
            changed = True
        # A pushed cert can announce a rotated server key or a changed owner
        # set.
        self.owner_set = None
        if state.HasField("cert") and rec.get("owner_key"):
            try:
                cert, owner_set = verify_cert(state.cert, state.info.server_key, keys.decode(rec["owner_key"]))
                check_known(owner_set, rec)
                self.owner_set = owner_set
                changed = remember(owner_set, rec, state.cert.owner_chain) or changed
                if cert.seq > rec.get("cert_seq", 0):
                    rec["cert_seq"] = cert.seq
                    changed = True
                changed = self.core.owner_keys_changed(self.cid, owner_set, state) or changed
                self.core.complete_owner_leaves(self.cid, owner_set, state)
                if keys.encode(state.info.server_key) != rec["server_key"] and cert.seq >= rec.get("cert_seq", 0):
                    rec["server_key"] = keys.encode(state.info.server_key)
                    rec["cert_seq"] = cert.seq
                    if state.info.server_key not in self.server_keys:
                        self.server_keys.append(state.info.server_key)
                    changed = True
            except keys.BadSignature:
                log.warning("community %s pushed a cert that doesn't verify", self.cid[:8])
        if changed:
            self.core.save_vault()

        wanted: dict[bytes, int] = {}
        for ch in state.channels:
            if ch.key:
                self.channel_keys.setdefault(ch.id, {})[ch.key_epoch] = bytes(ch.key)
                # Keep only the newest two epochs for in-flight messages.
                epochs = sorted(self.channel_keys[ch.id])
                for old in epochs[:-2]:
                    del self.channel_keys[ch.id][old]
            if ch.topic and ch.kind in MESSAGE_KINDS:
                wanted[bytes(ch.topic)] = ch.id
        if self.s is not None:
            relay = self.s.relay
            for topic in [t for t in self.topics if t not in wanted]:
                with contextlib.suppress(RelayError):
                    await relay.unsubscribe(topic)
                self.topics.pop(topic, None)
            for topic, chid in wanted.items():
                if topic not in self.topics:
                    replay = relay.cache_ttl_ms if topic in self._subscribed_before else 0
                    try:
                        await relay.subscribe(topic, self._on_topic, replay_ms=replay, on_replay=self._on_replayed)
                        self.topics[topic] = chid
                        self._subscribed_before.add(topic)
                    except RelayError as e:
                        log.info("subscribe failed: %s", e)
        visible = {ch.id for ch in state.channels}
        cache = self.core.cache(self.cid)
        for chid in list(cache.messages):
            if chid not in visible:
                cache.drop_channel(chid)
        self.core.community_changed(self.cid)

    def _on_replayed(self, topic: bytes, data: bytes) -> None:
        """A channel event from the relay's chunk cache: handled like a live
        one (messages and updates are idempotent), except typing, which is
        stale by now."""
        self._on_topic(topic, data, replayed=True)

    def _on_topic(self, topic: bytes, data: bytes, replayed: bool = False) -> None:
        se = cp.SignedEnvelope()
        try:
            se.ParseFromString(data)
        except Exception:
            return
        if not any(keys.is_valid(k, keys.CTX_CHANNEL_ENVELOPE, se.envelope, se.signature) for k in self.server_keys):
            log.debug("dropping a channel event that isn't signed by the server")
            return
        env = cp.ChannelEnvelope()
        try:
            env.ParseFromString(se.envelope)
        except DecodeError:
            log.warning("dropping a server-signed channel envelope that doesn't parse")
            return
        key = self.channel_keys.get(env.channel_id, {}).get(env.key_epoch)
        if key is None:
            known = self.channel_keys.get(env.channel_id)
            if known and env.key_epoch > max(known):
                # A newer key than we have (the push of the new keys was
                # missed): fetch the current state, at most every 10 s.
                self._refresh_keys()
            else:
                log.debug("dropping a channel event for a key this member doesn't have")
            return
        try:
            pt = decrypt_with_nonce(key, env.nonce, env.ciphertext, channel_aad(env.channel_id, env.key_epoch))
        except DecryptError:
            log.warning("dropping a server-signed channel event that doesn't decrypt (channel %d)", env.channel_id)
            return
        ev = cp.ChannelEvent()
        try:
            ev.ParseFromString(pt)
        except DecodeError:
            log.warning("dropping a channel event that doesn't parse")
            return
        if replayed and ev.WhichOneof("kind") == "typing":
            return
        self.core.channel_event(self.cid, ev, env.channel_id)

    async def _catch_up(self) -> None:
        """Fetch what we missed while offline, for every cached channel."""
        cache = self.core.cache(self.cid)
        assert self.state is not None
        for ch in self.state.channels:
            if ch.kind not in MESSAGE_KINDS:
                continue
            after = cache.last_id(ch.id)
            if not after:
                continue
            for _attempt in range(20):
                try:
                    reply = await self.call(history=cp.History(channel_id=ch.id, after_id=after, limit=200))
                except Offline, RpcError:
                    return
                msgs = [message_dict(m) for m in reply.messages.messages]
                for m in msgs:
                    self.core.cache_message(self.cid, m, notify=False)
                if not reply.messages.more or not msgs:
                    break
                after = msgs[-1]["id"]

    # ---------------------------------------------------------------- requests

    async def call(self, timeout: float = 30.0, **body) -> cp.ServerMessage:
        if self.s is None or self.status not in ("online", "pending"):
            raise Offline(_("You're offline from this community right now."))
        return await self.s.call(timeout=timeout, **body)

    async def admin(self, op_field: str, op: Any) -> cp.AdminResult:
        identity = self.core.identity_key(self.rec["identity"])
        signed = sign_action(identity, keys.decode(self.rec["owner_key"]), op_field, op)
        return (await self.call(admin=signed)).admin_result

    def channel(self, channel_id: int) -> cp.Channel | None:
        if self.state is None:
            return None
        for ch in self.state.channels:
            if ch.id == channel_id:
                return ch
        return None
