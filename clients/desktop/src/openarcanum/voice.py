"""Calls in voice channels (docs/protocol.md §12).

Media runs in the webview (ui/js/call.js): Chromium captures, processes and
encodes it, every frame is SFrame-encrypted there, and it goes over WebRTC to
the call's relay, whose SFU forwards it without keys. This module is the
call's control plane:

- joins and leaves through the community server, which hands out the call
  key, the relay and a grant for it;
- carries the page's signaling to the relay (the page has no network access
  of its own) and the relay's back;
- passes the page what it needs: the per-call key (the only key that ever
  enters JavaScript), the participants and what this member may do;
- reconnects to the relay, and reports it to the server when it can't be
  reached so the call can move to another.
"""

from __future__ import annotations

import asyncio
import base64
import contextlib
import logging
import time
from typing import TYPE_CHECKING

from openarcanum import keys
from openarcanum.community import Offline, RpcError
from openarcanum.errors import UserError
from openarcanum.hotkey import PushToTalk
from openarcanum.i18n import _
from openarcanum.relay import Relay, RelayError
from openarcanum.settings import VOICE_SENSITIVITY_DEFAULT
from openarcanum.wire import call_pb2 as cw
from openarcanum.wire import community_pb2 as cp
from openarcanum.wire import relay_pb2 as rp

if TYPE_CHECKING:
    from openarcanum.core import Core

log = logging.getLogger(__name__)

KINDS = ("audio", "camera", "screen", "screen-audio")
LAYERS = ("", "l", "m", "h")
MAX_SDP = 64 * 1024
# The most participant ids in one listening state (the relay's own bound).
MAX_LISTEN = 198
# How long the relay may be unreachable before the server is told.
RELAY_RETRY_SECONDS = 8.0


class Call:
    """One call this device is in."""

    def __init__(self, cid: str, session: cp.VoiceSession, gid: str = ""):
        self.cid = cid
        # A call in a direct message (dmcalls.py) instead of a community.
        self.gid = gid
        self.session = session
        self.relay: Relay | None = None
        self.relay_url = ""
        self.state = cp.VoiceState(channel_id=session.channel_id)
        self.generation = 0  # bumped whenever the page must reconnect
        # Rejoins in flight (_rejoin). Meanwhile server updates are only
        # kept, not sent to the page: the page reconnects with the newest
        # session when the rejoin answers, and must never have sent under
        # that session's key id before (call.js refuses to reuse one).
        self.rejoining = 0

    @property
    def channel(self) -> int:
        return self.session.channel_id

    @property
    def call_id(self) -> bytes:
        return cw.CallGrant.FromString(self.session.grant.grant).call_id


def _listen_args(args: list) -> tuple[bool, list[bytes], list[bytes]]:
    """The page's listening state (call.js listenState): deafened, the
    participants it muted for itself, the screens whose sound it wants."""
    st = args[0] if args and isinstance(args[0], dict) else None
    if st is None:
        raise UserError(_("Something went wrong with the call."))

    def ids(key: str) -> list[bytes]:
        raw = st.get(key) or []
        if not isinstance(raw, list) or len(raw) > MAX_LISTEN:
            raise UserError(_("Something went wrong with the call."))
        out = []
        for x in raw:
            try:
                b = bytes.fromhex(str(x))
            except ValueError:
                raise UserError(_("Something went wrong with the call.")) from None
            if len(b) != 16:
                raise UserError(_("Something went wrong with the call."))
            out.append(b)
        return out

    return bool(st.get("deaf")), ids("noAudio"), ids("screenAudio")


def _watches(w: cw.CallWatchers) -> list[dict] | None:
    """The relay's CallWatchers for the page, or None if it isn't sane."""
    out = []
    for x in w.watches[: len(KINDS)]:
        if x.kind not in KINDS:
            return None
        out.append(
            {"kind": x.kind, "receivers": int(x.receivers), "layers": [r for r in x.layers[:3] if r in LAYERS and r]}
        )
    return out


def _gained(old: cp.VoiceSession, new: cp.VoiceSession) -> bool:
    """A permission the relay enforces from the join's grant was gained."""
    return (
        (old.listen_only and not new.listen_only)
        or (new.can_video and not old.can_video)
        or (new.can_screen and not old.can_screen)
    )


class VoiceManager:
    def __init__(self, core: Core):
        self.core = core
        self.call: Call | None = None
        self._lock = asyncio.Lock()
        self.ptt = PushToTalk(self._ptt_changed)
        self.global_ptt = False

    # ---------------------------------------------------------------- joining

    async def join(self, cid: str, channel_id: int) -> dict:
        async with self._lock:
            if self.call is not None:
                await self._leave_locked()
            conn = self.core.conn(cid)
            try:
                vs = (await conn.call(voice_join=cp.VoiceJoin(channel_id=channel_id))).voice_session
            except (RpcError, Offline) as e:
                raise UserError(str(e)) from None
            self.call = Call(cid, vs)
            self.core.gui_media_access(True)
            self._page({"type": "start", "session": self._js_session(self.call)})
            self._watch_ptt()
            self._emit_state()
            return self.state()

    async def join_dm(self, gid: str, video: bool = False) -> dict:
        """Join (or start) the call in a conversation; one call at a time,
        so a voice channel is left first."""
        async with self._lock:
            if self.call is not None:
                await self._leave_locked()
            session = await self.core.dmcalls.join(gid, video)
            self.call = Call("", session, gid)
            self.core.gui_media_access(True)
            self._page({"type": "start", "session": self._js_session(self.call)})
            self._watch_ptt()
            self._emit_state()
            return self.state()

    async def dm_update(self, gid: str) -> None:
        """Who is in the DM call changed (dmcalls.py)."""
        call = self.call
        if call is None or call.gid != gid or call.rejoining:
            return
        try:
            call.session = self.core.dmcalls.session(gid)
        except UserError:
            return
        self._page({"type": "update", "generation": call.generation, "session": self._js_session(call)})
        self._emit_state()

    async def leave(self) -> dict:
        async with self._lock:
            await self._leave_locked()
        return {"ok": True}

    async def _leave_locked(self, tell_server: bool = True) -> None:
        call, self.call = self.call, None
        if call is None:
            return
        self.ptt.stop()
        self.global_ptt = False
        self._page({"type": "stop"})
        self.core.gui_media_access(False)
        if call.relay is not None:
            with contextlib.suppress(RelayError):
                await call.relay.call_leave(call.call_id)
            await call.relay.close()
        if call.gid:
            await self.core.dmcalls.leave()
        elif tell_server:
            conn = self.core.conns.get(call.cid)
            if conn is not None:
                with contextlib.suppress(RpcError, Offline):
                    await conn.call(voice_leave=cp.VoiceLeave(channel_id=call.channel))
        self._emit_state()

    async def left_community(self, cid: str) -> None:
        if self.call is not None and self.call.cid == cid:
            async with self._lock:
                await self._leave_locked(tell_server=False)

    async def shutdown(self) -> None:
        async with self._lock:
            await self._leave_locked()

    # ---------------------------------------------------------------- the page's signaling

    async def signal(self, generation: int, kind: str, args: list) -> object:
        """A request from call.js (see Call.signal there)."""
        call = self.call
        if call is None or generation != call.generation:
            raise UserError(_("That call is over."))
        if kind == "join":
            offer, kinds = str(args[0]), dict(args[1])
            if len(offer) > MAX_SDP or len(kinds) > 8 or any(k not in KINDS for k in kinds.values()):
                raise UserError(_("Something went wrong setting up the call."))
            return await self._connect(call, offer, {str(k): str(v) for k, v in kinds.items()})
        relay = call.relay
        if relay is None:
            raise UserError(_("Not connected to the call."))
        try:
            if kind == "answer":
                sdp = str(args[0])
                if len(sdp) > MAX_SDP:
                    raise UserError(_("Something went wrong setting up the call."))
                await relay.call_signal(call.call_id, "answer", sdp)
            elif kind == "layer":
                source, media, rid = bytes.fromhex(str(args[0])), str(args[1]), str(args[2])
                if len(source) != 16 or media not in ("camera", "screen") or rid not in LAYERS:
                    raise UserError(_("Couldn't change the video quality."))
                await relay.call_layer(call.call_id, source, media, rid)
            elif kind == "state":
                st = dict(args[0])
                call.state = cp.VoiceState(
                    channel_id=call.channel,
                    self_mute=bool(st.get("self_mute")),
                    self_deaf=bool(st.get("self_deaf")),
                    camera=bool(st.get("camera")),
                    screen=bool(st.get("screen")),
                    priority=bool(st.get("priority")),
                )
                if call.gid:
                    await self.core.dmcalls.set_state(call.state)
                else:
                    with contextlib.suppress(RpcError, Offline):
                        await self.core.conn(call.cid).call(voice_state=call.state)
                self._emit_state()
            elif kind == "listen":
                deaf, no_audio, screen_audio = _listen_args(args)
                await relay.call_listen(call.call_id, deaf, no_audio, screen_audio)
            elif kind == "failed":
                self.core.spawn(self._relay_failed(call))
            else:
                raise UserError(_("Unknown call request."))
        except RelayError:
            self.core.spawn(self._relay_failed(call))
            raise UserError(_("Lost the connection to the call's server.")) from None
        return None

    async def _connect(self, call: Call, offer: str, kinds: dict) -> str:
        """Joins the relay's SFU with the page's offer; returns the answer."""
        if call.relay is None or not call.relay.connected or call.relay_url != call.session.relay:
            if call.relay is not None:
                await call.relay.close()
            try:
                call.relay = await Relay(call.session.relay).connect()
            except (RelayError, OSError) as e:
                self.core.spawn(self._relay_failed(call))
                raise UserError(_("Couldn't reach the call's server: {error}").format(error=e)) from None
            call.relay_url = call.session.relay
            relay = call.relay
            relay.on_close(lambda r: self._relay_closed(call, r))
        join = cw.CallJoin(grant=call.session.grant, offer=offer, kinds=kinds)
        if call.session.HasField("control"):
            # The relay may be starting this call: it enforces the server's
            # current control (mutes, removals) from this join on.
            join.control.CopyFrom(call.session.control)
        try:
            return await call.relay.call_join(join, lambda f: self._from_relay(call, f))
        except RelayError as e:
            raise UserError(_("The call's server refused the call: {error}").format(error=e)) from None

    def _from_relay(self, call: Call, f: rp.RelayFrame) -> None:
        if call is not self.call:
            return
        if f.HasField("call_signal") and f.call_signal.type == "offer" and len(f.call_signal.sdp) <= MAX_SDP:
            self._page({"type": "offer", "generation": call.generation, "sdp": f.call_signal.sdp})
        elif f.HasField("call_watchers"):
            # Who receives this device's media: call.js sends only what
            # someone takes.
            watches = _watches(f.call_watchers)
            if watches is not None:
                self._page({"type": "watchers", "generation": call.generation, "watches": watches})
        elif f.HasField("call_state") and f.call_state.ended:
            # The server says why (VoiceUpdate); this is the relay's side of it.
            log.info("call: the relay ended the call for this device")

    def _relay_closed(self, call: Call, relay: Relay) -> None:
        if call is self.call and call.relay is relay:
            self.core.spawn(self._relay_failed(call))

    async def _relay_failed(self, call: Call) -> None:
        """Reconnect to the relay; if it stays unreachable, tell the server,
        which may move the call (docs/protocol.md §12.4)."""
        if call is not self.call:
            return
        if call.gid:
            # No server to move the call: reconnect to its relay with a
            # fresh grant for as long as it takes, or until left.
            await asyncio.sleep(1)
            if call is self.call:
                await self._rejoin(call, cp.VoiceJoin(rejoin=True))
            return
        deadline = time.monotonic() + RELAY_RETRY_SECONDS
        while time.monotonic() < deadline and call is self.call:
            try:
                r = await Relay(call.session.relay).connect(timeout=3)
                await r.close()
                await self._rejoin(call, cp.VoiceJoin(channel_id=call.channel, rejoin=True))
                return
            except RelayError, OSError:
                await asyncio.sleep(1)
        if call is self.call:
            await self._rejoin(call, cp.VoiceJoin(channel_id=call.channel, failed_relay=call.session.relay))

    @staticmethod
    def _stale(call: Call, session: cp.VoiceSession) -> bool:
        """A session older than the one the call has. A join's reply and the
        server's pushes can cross (both people report a dead relay at once:
        the move rekeys, then the other report rekeys again), and installing
        the older one would leave the two sides on different keys."""
        return call.session is not None and session.key_epoch < call.session.key_epoch

    async def _rejoin(self, call: Call, req: cp.VoiceJoin) -> None:
        call.rejoining += 1
        try:
            try:
                if call.gid:
                    vs = await self.core.dmcalls.rejoin(call.gid)
                else:
                    vs = (await self.core.conn(call.cid).call(voice_join=req)).voice_session
            except (RpcError, Offline, UserError) as e:
                log.info("call: couldn't rejoin: %s", e)
                return
            if call is not self.call:
                return
            # Another participant's join or rejoin may have rotated the key
            # again while this one was in flight: that update (with its own
            # fresh grant) is newer than the reply.
            if not self._stale(call, vs):
                call.session = vs
            await self._restart_page(call)
        finally:
            call.rejoining -= 1

    async def reconnected(self, cid: str) -> None:
        """The community connection came back. A new session isn't in the
        call any more (the server dropped the old one's place), so join
        again: as the same participant if the server still has it, else
        afresh. The page reconnects either way."""
        call = self.call
        if call is None or call.cid != cid:
            return
        conn = self.core.conn(cid)
        try:
            try:
                vs = (await conn.call(voice_join=cp.VoiceJoin(channel_id=call.channel, rejoin=True))).voice_session
            except RpcError:
                vs = (await conn.call(voice_join=cp.VoiceJoin(channel_id=call.channel))).voice_session
        except (RpcError, Offline) as e:
            log.info("call: couldn't rejoin after reconnecting: %s", e)
            async with self._lock:
                if self.call is call:
                    await self._leave_locked(tell_server=False)
            self.core.emit("toast", {"text": _("Left the call: {error}").format(error=e)})
            return
        if call is self.call and not self._stale(call, vs):
            call.session = vs
            call.state = cp.VoiceState(channel_id=call.channel)
            await self._restart_page(call)

    async def _restart_page(self, call: Call) -> None:
        """The page builds a new connection (to the same or another relay)."""
        if call.relay is not None:
            with contextlib.suppress(RelayError):
                await call.relay.call_leave(call.call_id)
            await call.relay.close()
            call.relay = None
        call.generation += 1
        self._page({"type": "start", "session": self._js_session(call)})
        self._emit_state()

    # ---------------------------------------------------------------- from the server

    async def server_update(self, cid: str, session: cp.VoiceSession) -> None:
        call = self.call
        if call is None or call.cid != cid or call.channel != session.channel_id:
            return
        if session.ended or not session.call_key:
            async with self._lock:
                if self.call is call:
                    await self._leave_locked(tell_server=False)
            self.core.emit("toast", {"text": session.ended or _("The voice call ended.")})
            if session.moved_to:
                # Not here: this runs in the connection's read loop, which the
                # join's request would wait on.
                self.core.spawn(self._follow_move(cid, session.moved_to))
            return
        if self._stale(call, session):
            return
        if call.rejoining:
            # The page restarts with the newest session once the rejoin answers.
            if session.key_epoch >= call.session.key_epoch:
                call.session = session
            return
        moved = session.relay != call.session.relay
        old = call.session
        call.session = session
        if moved:
            await self._restart_page(call)
            return
        if _gained(old, session):
            # The relay checks what to forward against the grant from the
            # join; a newly allowed Speak, Video or Screen Share needs a new
            # one, so reconnect (the server rotates the key for that).
            self.core.spawn(self._rejoin(call, cp.VoiceJoin(channel_id=call.channel, rejoin=True)))
            return
        self._page({"type": "update", "generation": call.generation, "session": self._js_session(call)})
        self._watch_ptt()
        self._emit_state()

    async def _follow_move(self, cid: str, channel_id: int) -> None:
        try:
            await self.join(cid, channel_id)
        except UserError as e:
            self.core.emit("toast", {"text": _("Couldn't join the channel you were moved to: {error}").format(error=e)})

    # ---------------------------------------------------------------- moderation

    async def moderate(self, cid: str, member: str, action: str, value) -> dict:
        if action == "mute":
            return await self.core.admin(cid, "voice_mute", {"member": member, "muted": bool(value)})
        if action == "deafen":
            return await self.core.admin(cid, "voice_deafen", {"member": member, "deafened": bool(value)})
        if action == "move":
            return await self.core.admin(cid, "voice_move", {"member": member, "channel_id": int(value)})
        if action == "disconnect":
            return await self.core.admin(cid, "voice_move", {"member": member, "channel_id": 0})
        raise UserError(_("Unknown action."))

    async def set_volume(self, member: str, volume: float) -> dict:
        """Per-user volume on this account's devices (0-200%)."""
        keys.decode(member)
        v = max(0.0, min(2.0, float(volume)))
        vols = dict(self.core.settings.get("voice_volumes", {}))
        if abs(v - 1.0) < 0.01:
            vols.pop(member, None)
        else:
            vols[member] = round(v, 2)
        await self.core.set_setting("voice_volumes", vols)
        return {"ok": True}

    async def set_local_mute(self, member: str, muted: bool) -> dict:
        """Mute a member for this device only ("Mute for me"): never played
        here, and the relay stops sending their voice here. Nobody else
        is told; it isn't a server mute."""
        keys.decode(member)
        mutes = [m for m in self.core.settings.get("voice_local_mutes", []) if m != member]
        if muted:
            mutes.append(member)
        await self.core.set_setting("voice_local_mutes", mutes)
        return {"ok": True}

    # ---------------------------------------------------------------- what the page and the UI see

    def _js_session(self, call: Call) -> dict:
        """The page's view of the call. The call key is the one secret in it:
        per-call media keys are the only keys that ever enter JavaScript."""
        s = call.session
        st = self.core.settings
        return {
            "generation": call.generation,
            "participant": s.participant.hex(),
            "slot": s.slot,
            "epoch": s.key_epoch,
            "key": base64.b64encode(s.call_key).decode(),
            "participants": [self._participant(p) for p in s.participants],
            "perms": {
                "audio": not s.listen_only,
                "video": s.can_video,
                "screen": s.can_screen,
                "vad": s.can_voice_activity,
                "priority": s.priority,
            },
            "serverMuted": s.server_mute,
            "serverDeafened": s.server_deaf,
            "mode": st.get("voice_mode", "vad"),
            "vadThreshold": st.get("voice_sensitivity", VOICE_SENSITIVITY_DEFAULT),
            "pttKey": st.get("ptt_key", "Space"),
            "mic": st.get("voice_input", "default"),
            "speaker": st.get("voice_output", "default"),
            "camera": st.get("video_input", "default"),
            "volumes": dict(st.get("voice_volumes", {})),
            "localMutes": list(st.get("voice_local_mutes", [])),
            "outputVolume": st.get("voice_output_volume", 100) / 100,
            "screenAudio": bool(st.get("screen_share_audio", True)),
            "cameraQuality": st.get("camera_quality", "720p"),
            "screenQuality": st.get("screen_quality", "balanced"),
            # A DM call: a key per join (dmcalls.join_key) instead of one per epoch.
            "joinKeys": [
                {**k, "key": base64.b64encode(k["key"]).decode()} for k in self.core.dmcalls.join_keys(call.gid)
            ]
            if call.gid
            else None,
        }

    @staticmethod
    def _participant(p: cp.VoiceParticipant) -> dict:
        return {
            "participant": p.participant.hex(),
            "member": keys.encode(p.member),
            "name": p.display_name,
            "slot": p.slot,
            "listenOnly": p.listen_only,
            "selfMute": p.self_mute,
            "selfDeaf": p.self_deaf,
            "serverMute": p.server_mute,
            "serverDeaf": p.server_deaf,
            "camera": p.camera,
            "screen": p.screen,
            "priority": p.priority,
        }

    def rooms(self, cid: str) -> list[dict]:
        """Who is in each voice channel of a community (for the channel list)."""
        conn = self.core.conns.get(cid)
        if conn is None:
            return []
        return [
            {"channel": r.channel_id, "participants": [self._participant(p) for p in r.participants]}
            for r in conn.voice_rooms.rooms
        ]

    def state(self) -> dict:
        """The call for the UI's lists (no keys)."""
        call = self.call
        if call is None:
            return {"active": False}
        s = call.session
        return {
            "active": True,
            "community": call.cid,
            "dm": call.gid,
            "channel": call.channel,
            "participant": s.participant.hex(),
            "participants": [self._participant(p) for p in s.participants],
            "listen_only": s.listen_only,
            "server_mute": s.server_mute,
            "server_deaf": s.server_deaf,
            "self_mute": call.state.self_mute,
            "self_deaf": call.state.self_deaf,
            "camera": call.state.camera,
            "screen": call.state.screen,
            "priority": call.state.priority,
            "can_video": s.can_video,
            "can_priority": s.priority,
            "can_screen": s.can_screen,
            "can_vad": s.can_voice_activity,
            "mode": self.core.settings.get("voice_mode", "vad"),
            "global_ptt": self.global_ptt,
            "relay": s.relay,
            "epoch": s.key_epoch,
        }

    # ---------------------------------------------------------------- push to talk in other windows

    def _watch_ptt(self) -> None:
        """While in a call in push-to-talk mode, watch the key system-wide
        (hotkey.py); the page handles it while OpenArcanum is focused anyway.
        A call without Use Voice Activity is push to talk whatever the
        setting says (call.js transmitting())."""
        st = self.core.settings
        call = self.call
        ptt = call is not None and (st.get("voice_mode", "vad") == "ptt" or not call.session.can_voice_activity)
        if not ptt or not st.get("ptt_global", True):
            self.ptt.stop()
            self.global_ptt = False
            return
        key = st.get("ptt_key", "Space")
        if self.ptt.running and self.ptt.key == key:
            return
        loop = asyncio.get_running_loop()
        self._loop = loop
        self.global_ptt = self.ptt.start(key)

    def _ptt_changed(self, down: bool) -> None:
        # From the watcher's thread.
        loop = getattr(self, "_loop", None)
        if loop is None:
            return

        def send() -> None:
            if self.call is not None:
                self._page({"type": "ptt", "generation": self.call.generation, "down": down})

        loop.call_soon_threadsafe(send)

    def settings_changed(self) -> None:
        self._watch_ptt()
        if self.call is not None:
            self._page({"type": "settings", "generation": self.call.generation, "session": self._js_session(self.call)})

    def _page(self, msg: dict) -> None:
        self.core.emit("call", msg)

    def _emit_state(self) -> None:
        self.core.emit("voice", self.state())
