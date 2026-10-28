"""Voice and video calls in direct messages (docs/protocol.md §9.9).

A community call is run by its server: it hands out the call key, signs the
relay grants and keeps the participant list. A conversation has no server,
so its members do that themselves, over the conversation's own end-to-end
encrypted events (DmCall):

- whoever starts the call makes its signing key and media key and sends
  them in START; everyone in the conversation can join with them;
- each participant signs its own grant with the call's key: the relay only
  checks that the grant's call id commits to the key that signed it;
- JOIN, STATE (again every HEARTBEAT seconds) and LEAVE keep everyone's
  participant list; someone not heard from for GONE seconds has left.

What reaches voice.py is the same VoiceSession a community server sends, so
the call itself (relay signaling, the page's WebRTC and SFrame) is the same.
"""

from __future__ import annotations

import asyncio
import contextlib
import hashlib
import logging
import os
import time
from dataclasses import dataclass, field
from typing import TYPE_CHECKING

from cryptography.hazmat.primitives import hashes
from cryptography.hazmat.primitives.kdf.hkdf import HKDF

from openarcanum import keys
from openarcanum.errors import UserError
from openarcanum.i18n import _
from openarcanum.keys import SigningKey
from openarcanum.wire import call_pb2 as cw
from openarcanum.wire import community_pb2 as cp
from openarcanum.wire import dm_pb2 as dm

if TYPE_CHECKING:
    from openarcanum.core import Core

log = logging.getLogger(__name__)

HEARTBEAT = 20
GONE = 70
# A START that says it was sent longer ago than this (a mailbox delivers it
# late) doesn't ring. Generous: senders' clocks can be off by minutes.
RING_SECONDS = 600
# The relay accepts grants for at most 10 minutes (callauth.MaxGrantLifetime).
GRANT_SECONDS = 9 * 60
MAX_PARTICIPANTS = 25
K = dm.DmCallKind


def join_key(call_key: bytes, participant: bytes) -> tuple[int, bytes]:
    """One join's SFrame epoch and key, which everyone in the call derives
    from the call key: each join (a rejoin too) sends under a key of its own,
    so a participant's frame counter starting again never reuses a key and
    key id (its slot stays the same)."""
    epoch = int.from_bytes(hashlib.sha256(b"openarcanum/dm-call-epoch/v1\x00" + participant).digest()[:4], "big") | 1
    key = HKDF(
        algorithm=hashes.SHA256(), length=32, salt=None, info=b"openarcanum/dm-call-key/v1\x00" + participant
    ).derive(call_key)
    return epoch, key


def call_id(signing_pub: bytes, nonce: bytes) -> bytes:
    return hashlib.sha256(b"openarcanum/call-id/v1\x00" + signing_pub + nonce).digest()[:16]


@dataclass
class Part:
    member: bytes
    slot: int
    seen: float
    self_mute: bool = False
    self_deaf: bool = False
    camera: bool = False
    screen: bool = False


@dataclass
class DmCall:
    gid: str
    call_id: bytes
    nonce: bytes
    seed: bytes
    key: bytes
    relay: str
    started_at: float
    video: bool = False
    parts: dict[bytes, Part] = field(default_factory=dict)


class DmCalls:
    def __init__(self, core: Core):
        self.core = core
        self.calls: dict[str, DmCall] = {}
        # (gid, participant, slot) while this device is in a call.
        self.mine: tuple[str, bytes, int] | None = None
        self.state = cp.VoiceState()
        self._beat: asyncio.Task | None = None

    @property
    def dms(self):
        return self.core.dms

    # ---------------------------------------------------------------- for the UI

    def info(self, gid: str) -> dict | None:
        c = self.calls.get(gid)
        if c is None:
            return None
        return {
            "id": gid,
            "video": c.video,
            "in_call": self.mine is not None and self.mine[0] == gid,
            "participants": [self.dms._name_of(gid, p.member) for p in c.parts.values()],
        }

    def all(self) -> list[dict]:
        return [i for i in (self.info(g) for g in list(self.calls)) if i]

    def _changed(self, gid: str) -> None:
        self.core.emit("dm_call", self.info(gid) or {"id": gid, "ended": True})
        if self.mine is not None and self.mine[0] == gid:
            self.core.spawn(self.core.voice.dm_update(gid))

    # ---------------------------------------------------------------- joining and leaving

    async def join(self, gid: str, video: bool = False) -> cp.VoiceSession:
        """Join the conversation's call, starting one if there is none."""
        g = self.dms.d["groups"].get(gid)
        if g is None or g.get("status") != "active":
            raise UserError(_("Unknown conversation."))
        self._prune()
        c = self.calls.get(gid)
        start = c is None or not c.parts
        if start:
            seed, nonce = os.urandom(32), os.urandom(16)
            relay = self.dms._group(gid).state.relay or await self.dms._sync_relay_url()
            c = DmCall(
                gid, call_id(SigningKey(seed).public, nonce), nonce, seed, os.urandom(32), relay, time.time(), video
            )
            self.calls[gid] = c
        elif len(c.parts) >= MAX_PARTICIPANTS:
            raise UserError(_("This call is full."))
        me = self._me(gid)
        pid = os.urandom(16)
        self.mine = (gid, pid, self._slot(gid))
        self.state = cp.VoiceState()
        c.parts[pid] = Part(me, self.mine[2], time.time())
        ev = self._event(K.DM_CALL_KIND_START if start else K.DM_CALL_KIND_JOIN, c)
        await self.dms.send_event(gid, dm.DmEvent(call=ev))
        self._start_beat()
        self._changed(gid)
        return self.session(gid)

    async def leave(self) -> None:
        mine, self.mine = self.mine, None
        if self._beat is not None:
            self._beat.cancel()
            self._beat = None
        if mine is None:
            return
        gid, pid, _slot = mine
        c = self.calls.get(gid)
        if c is None:
            return
        c.parts.pop(pid, None)
        with contextlib.suppress(UserError):
            await self.dms.send_event(
                gid, dm.DmEvent(call=dm.DmCall(kind=K.DM_CALL_KIND_LEAVE, call_id=c.call_id, participant=pid))
            )
        if not c.parts:
            self.calls.pop(gid, None)
        self._changed(gid)

    async def set_state(self, st: cp.VoiceState) -> None:
        self.state = st
        if self.mine is None:
            return
        gid, pid, _slot = self.mine
        c = self.calls.get(gid)
        if c is not None and pid in c.parts:
            p = c.parts[pid]
            p.self_mute, p.self_deaf, p.camera, p.screen = st.self_mute, st.self_deaf, st.camera, st.screen
            await self._send_state(c)

    def session(self, gid: str) -> cp.VoiceSession:
        """This device's VoiceSession, with a fresh grant (voice.py asks again
        to reconnect)."""
        if self.mine is None or self.mine[0] != gid or gid not in self.calls:
            raise UserError(_("That call is over."))
        c = self.calls[gid]
        _gid, pid, slot = self.mine
        signer = SigningKey(c.seed)
        grant = cw.CallGrant(
            call_id=c.call_id,
            server_key=signer.public,
            call_nonce=c.nonce,
            participant=pid,
            kid=slot,
            audio=True,
            video=True,
            screen=True,
            expires_at=int((time.time() + GRANT_SECONDS) * 1000),
            max_participants=MAX_PARTICIPANTS,
        )
        raw = grant.SerializeToString(deterministic=True)
        parts = sorted(c.parts.items(), key=lambda kv: kv[1].slot)
        return cp.VoiceSession(
            call_key=c.key,
            key_epoch=1,
            relay=c.relay,
            grant=cw.SignedCallGrant(grant=raw, signature=signer.sign(keys.CTX_CALL_GRANT, raw)),
            participant=pid,
            slot=slot,
            can_video=True,
            can_screen=True,
            can_voice_activity=True,
            participants=[
                cp.VoiceParticipant(
                    member=p.member,
                    display_name=self.dms._name_of(gid, p.member),
                    participant=ppid,
                    slot=p.slot,
                    self_mute=p.self_mute,
                    self_deaf=p.self_deaf,
                    camera=p.camera,
                    screen=p.screen,
                )
                for ppid, p in parts
            ],
        )

    def join_keys(self, gid: str) -> list[dict]:
        """For the page: every participant's join key, epoch and slot."""
        c = self.calls.get(gid)
        if c is None:
            return []
        out = []
        for pid, p in c.parts.items():
            epoch, key = join_key(c.key, pid)
            out.append({"participant": pid.hex(), "epoch": epoch, "key": key, "slot": p.slot})
        return out

    # ---------------------------------------------------------------- events

    def on_event(self, gid: str, ev: dm.DmEvent, mine: bool) -> None:
        e = ev.call
        if mine and ev.sender_device == self._my_curve(gid):
            return
        if len(e.call_id) != 16 or (e.participant and len(e.participant) != 16):
            return
        now = time.time()
        c = self.calls.get(gid)
        if e.kind == K.DM_CALL_KIND_START:
            if len(e.signing_seed) != 32 or len(e.call_key) != 32 or len(e.call_nonce) != 16:
                return
            if call_id(SigningKey(e.signing_seed).public, e.call_nonce) != e.call_id or not e.relay.startswith(
                "wss://"
            ):
                return
            if c is None or c.call_id != e.call_id:
                if c is not None and c.parts and c.call_id < e.call_id:
                    # Two calls started at once: everyone keeps the one with
                    # the lower id (no clocks involved, so all sides agree).
                    return
                moving = c is not None and self.mine is not None and self.mine[0] == gid
                c = DmCall(gid, e.call_id, e.call_nonce, e.signing_seed, e.call_key, e.relay, now, e.video)
                self.calls[gid] = c
                if moving:
                    # I'm in the call that lost: move to this one.
                    self.core.spawn(self.core.voice.join_dm(gid))
                elif not mine and abs(now - ev.at / 1000) < RING_SECONDS and (self.mine is None or self.mine[0] != gid):
                    name = self.dms._name_of(gid, ev.sender)
                    self.core.emit("dm_call", {**(self.info(gid) or {}), "ringing": True, "from": name})
                    self.core.emit("notify", {"title": name, "body": _("is calling you")})
        if c is None or c.call_id != e.call_id:
            return
        if e.kind == K.DM_CALL_KIND_LEAVE:
            c.parts.pop(e.participant, None)
        elif e.participant:
            # Liveness is when it arrives, never the sender's clock (which can
            # be off): someone who stops sending STATE is gone after GONE.
            p = c.parts.get(e.participant)
            if p is None:
                if len(c.parts) >= MAX_PARTICIPANTS or not 1 <= e.slot <= 255:
                    return
                p = c.parts[e.participant] = Part(ev.sender, e.slot, now)
            elif p.member != ev.sender:
                return
            p.seen = now
            p.self_mute, p.self_deaf, p.camera, p.screen = e.self_mute, e.self_deaf, e.camera, e.screen
        if not c.parts and (self.mine is None or self.mine[0] != gid):
            self.calls.pop(gid, None)
        self._changed(gid)

    # ---------------------------------------------------------------- helpers

    def _event(self, kind: int, c: DmCall) -> dm.DmCall:
        assert self.mine is not None
        _gid, pid, slot = self.mine
        ev = dm.DmCall(
            kind=kind,
            call_id=c.call_id,
            participant=pid,
            slot=slot,
            self_mute=self.state.self_mute,
            self_deaf=self.state.self_deaf,
            camera=self.state.camera,
            screen=self.state.screen,
        )
        if kind == K.DM_CALL_KIND_START:
            ev.call_nonce, ev.signing_seed, ev.call_key, ev.relay, ev.video = c.nonce, c.seed, c.key, c.relay, c.video
        return ev

    async def _send_state(self, c: DmCall) -> None:
        with contextlib.suppress(UserError):
            await self.dms.send_event(c.gid, dm.DmEvent(call=self._event(K.DM_CALL_KIND_STATE, c)), live_only=True)

    def _start_beat(self) -> None:
        if self._beat is None or self._beat.done():
            self._beat = self.core.spawn(self._heartbeat())

    async def _heartbeat(self) -> None:
        while self.mine is not None:
            await asyncio.sleep(HEARTBEAT)
            mine = self.mine
            if mine is None:
                return
            c = self.calls.get(mine[0])
            if c is not None:
                c.parts.get(mine[1], Part(b"", 0, 0)).seen = time.time()
                await self._send_state(c)
            self._prune()

    def _prune(self) -> None:
        """Drop participants not heard from (they left without saying so)."""
        now = time.time()
        mine = self.mine[1] if self.mine else None
        for gid, c in list(self.calls.items()):
            gone = [pid for pid, p in c.parts.items() if pid != mine and now - p.seen > GONE]
            for pid in gone:
                c.parts.pop(pid)
            if not c.parts and (self.mine is None or self.mine[0] != gid):
                self.calls.pop(gid)
            if gone:
                self._changed(gid)

    def _me(self, gid: str) -> bytes:
        return keys.decode(self.dms.d["groups"][gid]["me"])

    def _my_curve(self, gid: str) -> bytes:
        return self.dms.olm.curve(self.dms._label(self.dms.d["groups"][gid]["me"]))

    def _slot(self, gid: str) -> int:
        """1..255, the same for this device every time and different from
        every other device in the conversation (SFrame key ids must not
        collide): its place among the members' devices."""
        mine = self._my_curve(gid)
        curves = sorted(
            {d.curve25519 for ident in self.dms._member_ids(gid) for d in self.dms._devices_of(ident)} | {mine}
        )
        return min(255, curves.index(mine) + 1)
