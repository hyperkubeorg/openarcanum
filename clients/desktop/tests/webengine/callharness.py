"""Multi-party call harness: N QtWebEngine pages run the client's call code
(ui/js/call.js, sframe.js) with Chromium's fake devices, through a real
relay's SFU. This process stands in for the community server: it signs
grants and controls and rotates the call key. Signaling goes through the
client's own relay connection code (openarcanum.relay).

    callharness.py '<json plan>'

Prints one JSON object with what each scenario observed; test_calls.py
asserts on it. Optionally runs the media through a lossy, delaying UDP proxy
(userspace, so it needs neither tc netem nor root).
"""

from __future__ import annotations

import asyncio
import base64
import hashlib
import json
import os
import random
import sys
import threading
import time
from pathlib import Path

from PySide6.QtCore import QObject, QUrl, Signal
from PySide6.QtWebEngineCore import QWebEnginePage, QWebEnginePermission, QWebEngineProfile, QWebEngineSettings
from PySide6.QtWidgets import QApplication

from openarcanum.keys import SigningKey
from openarcanum.relay import Relay
from openarcanum.tasks import spawn
from openarcanum.wire import call_pb2 as cw

CTX_GRANT = "openarcanum/call-grant/v1"
CTX_CONTROL = "openarcanum/call-control/v1"
PAGE = Path(__file__).with_name("callpage.html")


def call_id(server_key: bytes, nonce: bytes) -> bytes:
    return hashlib.sha256(b"openarcanum/call-id/v1\x00" + server_key + nonce).digest()[:16]


class Server:
    """The community server's part: grants, controls, keys."""

    def __init__(self) -> None:
        self.key = SigningKey.generate()
        self.nonce = os.urandom(16)
        self.id = call_id(self.key.public, self.nonce)
        self.epoch = 1
        self.call_key = os.urandom(32)
        self.seq = 0

    def grant(self, slot: int, name: str, video=True, screen=True, priority=False) -> tuple[cw.SignedCallGrant, bytes]:
        p = os.urandom(16)
        g = cw.CallGrant(
            call_id=self.id,
            server_key=self.key.public,
            call_nonce=self.nonce,
            participant=p,
            kid=slot,
            audio=True,
            video=video,
            screen=screen,
            priority=priority,
            expires_at=int(time.time() * 1000) + 5 * 60 * 1000,
            max_participants=60,
        ).SerializeToString()
        return cw.SignedCallGrant(grant=g, signature=self.key.sign(CTX_GRANT, g)), p

    def rekey(self) -> None:
        self.epoch += 1
        self.call_key = os.urandom(32)

    def control(self, muted=(), deafened=(), removed=()) -> cw.SignedCallControl:
        self.seq += 1
        c = cw.CallControl(
            call_id=self.id, seq=self.seq, muted=list(muted), deafened=list(deafened), removed=list(removed)
        ).SerializeToString()
        return cw.SignedCallControl(control=c, signature=self.key.sign(CTX_CONTROL, c))


class Proxy(asyncio.DatagramProtocol):
    """Forwards UDP between clients and the relay's media port, dropping and
    delaying packets in both directions."""

    def __init__(self, upstream: tuple[str, int], loss: float, delay_ms: float, jitter_ms: float):
        self.upstream = upstream
        self.loss, self.delay, self.jitter = loss, delay_ms / 1000, jitter_ms / 1000
        self.clients: dict[tuple, asyncio.DatagramTransport] = {}
        self.transport: asyncio.DatagramTransport | None = None
        self.passed = self.dropped = 0
        # What the relay sees (scenario "wire"): every RTP packet's header,
        # parsed from the datagrams (SRTP leaves the RTP header, header
        # extensions included, in the clear).
        self.record: list[dict] | None = None

    def note(self, data: bytes, client: tuple, up: bool) -> None:
        if self.record is not None:
            r = rtp_header(data)
            if r:
                self.record.append(r | {"t": time.monotonic(), "client": f"{client[0]}:{client[1]}", "up": up})

    def connection_made(self, transport) -> None:
        self.transport = transport

    def _later(self, send, data) -> None:
        if random.random() < self.loss:
            self.dropped += 1
            return
        self.passed += 1
        d = max(0.0, self.delay + random.uniform(-self.jitter, self.jitter))
        asyncio.get_running_loop().call_later(d, send, data)

    def datagram_received(self, data, addr) -> None:
        self.note(data, addr, True)
        up = self.clients.get(addr)
        if up is None:
            loop = asyncio.get_running_loop()
            proxy = self

            class Back(asyncio.DatagramProtocol):
                def datagram_received(self, d, _a) -> None:
                    proxy.note(d, addr, False)
                    proxy._later(lambda x: proxy.transport.sendto(x, addr), d)

            spawn(self._open(loop, Back, addr, data))
            return
        self._later(up.sendto, data)

    async def _open(self, loop, back, addr, first) -> None:
        if addr in self.clients:
            self._later(self.clients[addr].sendto, first)
            return
        t, _ = await loop.create_datagram_endpoint(back, remote_addr=self.upstream)
        self.clients[addr] = t
        self._later(t.sendto, first)


def rtp_header(d: bytes) -> dict | None:
    """An SRTP packet's clear RTP header as anyone on the path (the relay
    included) reads it: payload type, marker, header extensions ({id:
    length}, one-byte or two-byte form) and the size of what follows the
    header (the encrypted payload and the SRTP tag). None for anything else
    (STUN, DTLS, RTCP)."""
    if len(d) < 12 or d[0] >> 6 != 2 or 72 <= (d[1] & 0x7F) <= 79:
        return None
    n = 12 + 4 * (d[0] & 0x0F)
    exts: dict[int, int] = {}
    if d[0] & 0x10:
        if len(d) < n + 4:
            return None
        profile = int.from_bytes(d[n : n + 2], "big")
        words = int.from_bytes(d[n + 2 : n + 4], "big")
        body = d[n + 4 : n + 4 + 4 * words]
        i = 0
        while i < len(body):
            if body[i] == 0:  # padding
                i += 1
            elif profile == 0xBEDE:
                eid, ln = body[i] >> 4, (body[i] & 0x0F) + 1
                if eid == 15:
                    break
                exts[eid] = ln
                i += 1 + ln
            elif profile & 0xFFF0 == 0x1000 and i + 1 < len(body):
                exts[body[i]] = body[i + 1]
                i += 2 + body[i + 1]
            else:
                exts[-profile] = 4 * words
                break
        n += 4 + 4 * words
    return {"pt": d[1] & 0x7F, "marker": bool(d[1] & 0x80), "ext": exts, "size": len(d) - n, "len": len(d)}


class Main(QObject):
    run = Signal(object)

    def __init__(self) -> None:
        super().__init__()
        self.run.connect(lambda fn: fn())


class Page:
    """One participant: a page, its relay connection and its grant."""

    def __init__(self, h: Harness, name: str, slot: int, opts: dict):
        self.h, self.name, self.slot, self.opts = h, name, slot, opts
        self.page: QWebEnginePage | None = None
        self.relay: Relay | None = None
        self.participant = b""
        self.grant: cw.SignedCallGrant | None = None
        self.n = 0
        self.closed = False
        # Priority Speaker (the server puts it in the grant and the roster).
        self.priority = bool(opts.get("priority"))
        # Every SDP exchanged with the relay, as the relay sees it.
        self.sdps: list[str] = []

    # -- JavaScript, from the asyncio thread

    def js(self, code: str) -> asyncio.Future:
        loop = asyncio.get_running_loop()
        fut = loop.create_future()

        def done(v) -> None:
            loop.call_soon_threadsafe(lambda: fut.done() or fut.set_result(v))

        self.h.main.run.emit(lambda: self.page.runJavaScript(code, 0, done))
        return fut

    async def js_async(self, expr: str, timeout: float = 30.0):
        self.n += 1
        key = f"r{self.n}"
        await self.js(
            f"window.__r = window.__r || {{}}; Promise.resolve().then(() => {expr})"
            f".then(v => {{ window.__r.{key} = JSON.stringify(v === undefined ? null : v); }})"
            f".catch(e => {{ window.__r.{key} = JSON.stringify({{error: String(e)}}); }}); 0"
        )
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            v = await self.js(f"window.__r && window.__r.{key}")
            if v:
                return json.loads(v)
            await asyncio.sleep(0.03)
        raise TimeoutError(expr)

    # -- signaling

    async def pump(self) -> None:
        while not self.closed:
            raw = await self.js("JSON.stringify(drainOutbox())")
            for msg in json.loads(raw or "[]"):
                spawn(self.on_signal(msg))
            await asyncio.sleep(0.03)

    async def on_signal(self, msg: dict) -> None:
        kind, args, sid = msg["kind"], msg["args"], msg["id"]
        try:
            value = None
            if kind == "join":
                join = cw.CallJoin(grant=self.grant, offer=args[0], kinds=args[1])
                self.sdps.append(args[0])
                value = await self.relay.call_join(join, self.on_frame)
                self.sdps.append(value)
            elif kind == "answer":
                self.sdps.append(args[0])
                await self.relay.call_signal(self.h.server.id, "answer", args[0])
            elif kind == "layer":
                await self.relay.call_layer(self.h.server.id, bytes.fromhex(args[0]), args[1], args[2])
            elif kind == "listen":
                st = args[0]
                await self.relay.call_listen(
                    self.h.server.id,
                    bool(st["deaf"]),
                    [bytes.fromhex(x) for x in st["noAudio"]],
                    [bytes.fromhex(x) for x in st["screenAudio"]],
                )
            elif kind == "failed":
                self.h.events.append(f"{self.name}: connection failed")
            await self.js(f"resolveSignal({sid}, {json.dumps(value)}, null)")
        except Exception as e:  # reported to the page, as the app does
            await self.js(f"resolveSignal({sid}, null, {json.dumps(str(e))})")

    def on_frame(self, f) -> None:
        if f.HasField("call_signal") and f.call_signal.type == "offer":
            self.sdps.append(f.call_signal.sdp)
            sdp = json.dumps(f.call_signal.sdp)
            spawn(self.js_async(f"call.onRemoteOffer({sdp})"))
        elif f.HasField("call_state") and f.call_state.ended:
            self.h.events.append(f"{self.name}: ended: {f.call_state.ended}")
        elif f.HasField("call_watchers") and not self.opts.get("ignoreWatchers"):
            # As voice.py passes them on.
            w = [{"kind": x.kind, "receivers": x.receivers, "layers": list(x.layers)} for x in f.call_watchers.watches]
            spawn(self.js_async(f"call.onWatchers({json.dumps(w)})"))

    def session(self) -> dict:
        return {
            "participant": self.participant.hex(),
            "slot": self.slot,
            "epoch": self.h.server.epoch,
            "key": base64.b64encode(self.h.server.call_key).decode(),
            "participants": self.h.roster(),
            "perms": self.opts.get("perms") or {"audio": True, "video": True, "screen": True, "vad": True},
            "mode": self.opts.get("mode") or "vad",
            "vadThreshold": self.opts["vadThreshold"] if self.opts.get("vadThreshold") is not None else -60,
            "screenAudio": True,
        }


class Harness:
    def __init__(self, plan: dict):
        self.plan = plan
        self.server = Server()
        self.main = Main()
        self.pages: list[Page] = []
        self.events: list[str] = []
        self.out: dict = {}
        self.profile: QWebEngineProfile | None = None
        self.proxy: Proxy | None = None

    def roster(self) -> list[dict]:
        return [
            {
                "participant": p.participant.hex(),
                "member": p.name,
                "name": p.name,
                "slot": p.slot,
                "priority": p.priority,
            }
            for p in self.pages
            if not p.closed and p.participant
        ]

    # Pages are created on the main thread.
    def make_page(self, p: Page) -> None:
        page = QWebEnginePage(self.profile)
        # As the app's page (app.harden_webview): ICE offers the default
        # route's address only, which must still reach a relay on loopback.
        page.settings().setAttribute(QWebEngineSettings.WebAttribute.WebRTCPublicInterfacesOnly, True)
        page.permissionRequested.connect(lambda perm: perm.grant())
        page.desktopMediaRequested.connect(lambda req: req.selectScreen(req.screensModel().index(0, 0)))
        page.load(QUrl.fromLocalFile(str(PAGE)))
        p.page = page

    async def add(self, name: str, opts: dict) -> Page:
        p = Page(self, name, len(self.pages) + 1, opts)
        p.grant, p.participant = self.server.grant(p.slot, name, priority=p.priority)
        self.pages.append(p)
        ready = asyncio.get_running_loop().create_future()

        def make() -> None:
            self.make_page(p)
            p.page.loadFinished.connect(lambda ok: asyncio.run_coroutine_threadsafe(self._set(ready, ok), self.loop))

        self.main.run.emit(make)
        assert await ready, "page didn't load"
        p.relay = await Relay(self.plan["relay"]).connect()
        spawn(p.pump())
        # As the server does: everyone already in learns of the newcomer and
        # gets the new key, the newcomer gets its session.
        await self.update_all(rekey=True, skip=p)
        r = await p.js_async(f"startCall({json.dumps(p.session())}, {json.dumps(opts)})", 40)
        assert r == "ok", (name, r, await p.js("JSON.stringify(log)"))
        return p

    @staticmethod
    async def _set(fut, v) -> None:
        if not fut.done():
            fut.set_result(v)

    async def update_all(self, rekey: bool, skip: Page | None = None) -> None:
        if rekey:
            self.server.rekey()
        roster = self.roster()
        for p in self.pages:
            if p.closed or p is skip:
                continue
            upd = {"participants": roster}
            if rekey:
                upd.update(epoch=self.server.epoch, key=base64.b64encode(self.server.call_key).decode(), slot=p.slot)
            await p.js_async(f"call.update({json.dumps(upd)})")

    async def leave(self, p: Page) -> None:
        await p.js("call.close(); 0")
        await p.relay.call_leave(self.server.id)
        await p.relay.close()
        p.closed = True
        await self.update_all(rekey=True)

    async def debug(self, p: Page) -> dict:
        return json.loads(await p.js("JSON.stringify(call.debug())"))

    async def speaking_over(self, p: Page, seconds: float, until_spoken: bool = False) -> dict:
        """Loudest level and whether each remote participant spoke, as heard
        on p over a few seconds (the fake microphone beeps about once a
        second). With until_spoken, stop as soon as someone is heard."""
        seen: dict = {}
        end = time.monotonic() + seconds
        while time.monotonic() < end:
            for r in (await self.debug(p))["remote"]:
                s = seen.setdefault(r["id"], {"max": -120.0, "spoke": False})
                s["max"] = max(s["max"], r["level"])
                s["spoke"] = s["spoke"] or r["speaking"]
            if until_spoken and any(s["spoke"] for s in seen.values()):
                break
            await asyncio.sleep(0.1)
        return seen

    async def watch(self, p: Page, q: Page, kind: str, width: int) -> None:
        """p shows q's video of kind in a tile width pixels wide."""
        await p.js(f"call.watch({json.dumps(q.participant.hex())}, {json.dumps(kind)}, {width}); 0")

    async def sent(self, p: Page, seconds: float) -> dict:
        """Frames p encoded and sent per kind over a few seconds."""
        before = (await self.debug(p))["stats"]["sentFrames"]
        await asyncio.sleep(seconds)
        after = await self.debug(p)
        return {k: after["stats"]["sentFrames"][k] - before[k] for k in before} | {"sending": after["sending"]}

    async def by_name(self) -> dict:
        return {p.name: await self.debug(p) for p in self.pages if not p.closed}

    # ---------------------------------------------------------------- scenarios

    async def basic(self) -> None:
        """Three people with audio and camera: everyone hears and sees
        everyone, keys rotate on join and leave without breaking media,
        receivers pick simulcast layers, and the server can mute."""
        a = await self.add("a", {"camera": True})
        b = await self.add("b", {"camera": True})
        c = await self.add("c", {"camera": True, "screen": True})
        # Everyone shows everyone's camera in a mid-sized tile, and a also
        # watches c's screen (video is only sent to those who watch it).
        for p in (a, b, c):
            for q in (a, b, c):
                if q is not p:
                    await self.watch(p, q, "camera", 300)
        await self.watch(a, c, "screen", 300)
        await asyncio.sleep(6)
        self.out["joined"] = await self.by_name()
        await a.js(f"call.unwatch({json.dumps(c.participant.hex())}, 'screen'); 0")
        self.out["speaking"] = await self.speaking_over(a, 4)
        # A key change mid-call: media keeps flowing.
        before = await self.by_name()
        await self.update_all(rekey=True)
        await asyncio.sleep(3)
        after = await self.by_name()
        self.out["rekey"] = {
            n: {
                "decrypted": after[n]["stats"]["decrypted"] - before[n]["stats"]["decrypted"],
                "failed": after[n]["stats"]["failed"] - before[n]["stats"]["failed"],
            }
            for n in after
        }
        # Layers: a shows b large and c small.
        await a.js(f"call.watch({json.dumps(b.participant.hex())}, 'camera', 1000); 0")
        await a.js(f"call.watch({json.dumps(c.participant.hex())}, 'camera', 100); 0")
        await asyncio.sleep(7)
        self.out["layers"] = {"debug": (await self.debug(a))["layers"], "inbound": await a.js_async("inboundVideo()")}
        # The server mutes b; a stops hearing b.
        await a.relay._write(_update(self.server.control(muted=[b.participant])))
        await asyncio.sleep(1)
        self.out["muted"] = await self.speaking_over(a, 4)
        # c leaves; a and b go on, without c.
        await self.leave(c)
        await asyncio.sleep(3)
        self.out["after_leave"] = await self.by_name()
        # The server removes b: told, and disconnected.
        await a.relay._write(_update(self.server.control(muted=[b.participant], removed=[b.participant])))
        await asyncio.sleep(2)

    async def played_over(self, p: Page, seconds: float) -> list[dict]:
        """Samples of how each remote participant sounds on p (received and
        played levels, the volume call.js set, speaking), every 100 ms."""
        out = []
        end = time.monotonic() + seconds
        while time.monotonic() < end:
            out.append(json.loads(await p.js("JSON.stringify(playedLevels())")))
            await asyncio.sleep(0.1)
        return out

    async def priority(self) -> None:
        """Priority Speaker: a has it, b doesn't, c listens. On c, b is
        turned down while a speaks; a never is. Then a self-mutes (b comes
        back up), and a loses the role mid-call (b no longer ducks)."""
        a = await self.add("a", {"priority": True})
        b = await self.add("b", {})
        c = await self.add("c", {})
        await asyncio.sleep(5)
        ids = {"a": a.participant.hex(), "b": b.participant.hex()}
        self.out["ids"] = ids
        self.out["with_priority"] = await self.played_over(c, 6)
        # a mutes itself: nobody is ducked once a stops being heard.
        await a.js_async("call.setSelfMute(true)")
        await asyncio.sleep(1.5)
        self.out["priority_muted"] = await self.played_over(c, 4)
        await a.js_async("call.setSelfMute(false)")
        # The server takes Priority Speaker away (a new roster, as a
        # session update after the role change).
        a.priority = False
        await self.update_all(rekey=False)
        await asyncio.sleep(1.5)
        self.out["without_priority"] = await self.played_over(c, 6)

    async def lossy_layers(self) -> None:
        """a shows b's camera large: the top layer while the link is clean;
        when 15% of packets start getting lost, a lowers its cap and asks for
        a lower layer."""
        a = await self.add("a", {})
        b = await self.add("b", {"camera": True})
        await a.js(f"call.watch({json.dumps(b.participant.hex())}, 'camera', 1000); 0")
        await asyncio.sleep(8)
        self.out["clean"] = (await self.debug(a))["layers"]
        self.proxy.loss = 0.15
        await asyncio.sleep(10)
        self.out["lossy"] = (await self.debug(a))["layers"]

    async def watchers(self) -> None:
        """Nothing is sent that nobody takes. a has its camera on; b
        decides what it watches and hears. Frames a encodes and sends are
        counted (they pass the SFrame transform), and what b receives."""
        a = await self.add("a", {"camera": True})
        b = await self.add("b", {})
        await asyncio.sleep(4)
        aid = json.dumps(a.participant.hex())
        out = self.out
        # Nobody watches a's camera: not encoded, not sent; a's voice is.
        out["unwatched"] = await self.sent(a, 3)
        out["unwatched_b"] = await b.js_async("inboundVideo()")
        # b watches it: sent, and b gets pictures.
        await self.watch(b, a, "camera", 640)
        await asyncio.sleep(3)
        out["watched"] = await self.sent(a, 3)
        out["watched_b"] = await b.js_async("inboundVideo()")
        # b stops watching: a stops again.
        await b.js(f"call.unwatch({aid}, 'camera'); 0")
        await asyncio.sleep(2)
        out["unwatched_again"] = await self.sent(a, 3)
        # b mutes a for itself: a's voice isn't played or received at b, and
        # a (heard by nobody else) stops sending it.
        await b.js("call.setLocalMute('a', true); 0")
        await asyncio.sleep(2)
        out["muted_for_b"] = await self.sent(a, 3)
        out["muted_for_b_heard"] = await self.speaking_over(b, 3)
        await b.js("call.setLocalMute('a', false); 0")
        await asyncio.sleep(2)
        out["unmuted"] = await self.sent(a, 3)
        # Forwarding resumes once the relay has b's new listening state, which
        # takes longer on a loaded machine.
        out["unmuted_heard"] = await self.speaking_over(b, 15, until_spoken=True)
        # b deafens: a stops sending its voice.
        await b.js_async("call.setSelfDeaf(true)")
        await asyncio.sleep(2)
        out["deafened"] = await self.sent(a, 3)
        await b.js_async("call.setSelfDeaf(false)")
        await asyncio.sleep(2)
        out["undeafened"] = await self.sent(a, 3)

    async def levels(self) -> None:
        """The microphone's level as call.js measures it (after the
        engine's processing, or raw in studio mode), and how often voice
        activity opens, sampled every 50 ms. The plan's mode and
        vadThreshold go into the session."""
        a = await self.add("a", {"mode": self.plan.get("mode", "vad"), "vadThreshold": self.plan.get("threshold")})
        await asyncio.sleep(2)  # the engine's noise suppression and gain settle
        # The level is taken on the audio itself, every 512 samples (call.js's
        # analyser window); the gate is call.js's own, sampled every 50 ms
        # (the plan runs the engine without background timer throttling, as
        # a visible window's timers run).
        ms = int(self.plan.get("seconds", 8) * 1000)
        levels, gate = await asyncio.gather(a.js_async(f"micLevels({ms})", 60), a.js_async(f"gateStates({ms})", 60))
        self.out["levels"] = levels
        self.out["gate"] = gate
        self.out["constraints"] = await a.js_async("call.mic ? call.mic.getSettings() : null")
        self.out["sent_audio"] = (await self.debug(a))["sending"]

    async def wrongkey(self) -> None:
        """b sends under a key nobody else has: nothing b sends can be played."""
        a = await self.add("a", {})
        b = await self.add("b", {})
        await asyncio.sleep(1)
        bad = base64.b64encode(os.urandom(32)).decode()
        await b.js_async(f"call.update({json.dumps({'epoch': self.server.epoch + 100, 'key': bad, 'slot': b.slot})})")
        await asyncio.sleep(2)
        before = await self.debug(a)
        await asyncio.sleep(4)
        after = await self.debug(a)
        self.out["wrongkey"] = {
            "decrypted": after["stats"]["decrypted"] - before["stats"]["decrypted"],
            "failed": after["stats"]["failed"] - before["stats"]["failed"],
            "b_sent": (await self.debug(b))["stats"]["encrypted"],
        }
        del a

    async def latency(self) -> None:
        """Mouth-to-ear: bursts from a's tone 'microphone', onsets at b's
        speaker (plus the output latency the engine reports)."""
        a = await self.add("a", {"tone": True})
        b = await self.add("b", {"listen": True})
        await asyncio.sleep(3)
        sent = []
        trace = []
        for _ in range(int(self.plan.get("bursts", 10))):
            sent.append(json.loads(await a.js("JSON.stringify(burst())")))
            await asyncio.sleep(0.12)
            da, db = await self.debug(a), await self.debug(b)
            trace.append(
                {
                    "a_tx": da["transmitting"],
                    "a_level": round(da["myLevel"]),
                    "b_hears": [round(r["level"]) for r in db["remote"]],
                }
            )
            await asyncio.sleep(0.88)
        self.out["trace"] = trace
        self.out["a_audio"] = json.loads(
            await a.js(
                "JSON.stringify({tone: window.toneCtx && toneCtx.state, ctx: call.ctx.state,"
                " mic: call.mic && call.mic.readyState, muted: call.mic && call.mic.muted,"
                " sent: call.local.audio && call.local.audio.readyState})"
            )
        )
        await asyncio.sleep(1)
        heard = json.loads(await b.js("JSON.stringify(onsets)"))
        lat = []
        for t in sent:
            after = [h for h in heard if h >= t]
            if after and after[0] - t < 1000:
                lat.append(after[0] - t)
        self.out["latency"] = {"sent": len(sent), "heard": len(lat), "ms": lat}
        self.out["pages"] = await self.by_name()
        if self.proxy:
            self.out["proxy"] = {"passed": self.proxy.passed, "dropped": self.proxy.dropped}

    async def scale(self) -> None:
        """N people, the first V with camera: everyone receives everyone."""
        n, v = int(self.plan["n"]), int(self.plan.get("video", 0))
        for i in range(n):
            await self.add(f"p{i}", {"camera": i < v, "smallCamera": True, "synthMic": i >= 12})
        # Receivers show many small tiles: everyone asks for the low layer.
        for p in self.pages:
            for q in self.pages[:v]:
                if q is not p:
                    await p.js(f"call.watch({json.dumps(q.participant.hex())}, 'camera', 100); 0")
        await asyncio.sleep(float(self.plan.get("settle", 10)))
        summary = {}
        for p in self.pages:
            d = await self.debug(p)
            summary[p.name] = {
                "audio_from": sum(1 for r in d["remote"] if r["audio"]),
                "camera_from": sum(1 for r in d["remote"] if r["camera"]),
                "decrypted": d["stats"]["decrypted"],
                "failed": d["stats"]["failed"],
                "why": d["stats"]["why"],
                "epochs": d["epochs"],
                "connection": d["connection"],
            }
        self.out["scale"] = summary

    async def mixed(self) -> None:
        """A big call: a few real pages plus synthetic participants
        (cmd/callload, Pion, real SFrame under the same keys) through one
        relay. One key epoch for the whole run (rekeying is tested above).
        Every page must receive and decrypt every other participant, and
        every synthetic participant every page's audio."""
        real, synth = int(self.plan["real"]), int(self.plan["synth"])
        vreal, vsynth = int(self.plan.get("video_real", 0)), int(self.plan.get("video_synth", 0))
        seconds = int(self.plan.get("seconds", 25))
        self.server.rekey()
        for i in range(real):
            p = Page(
                self,
                f"r{i}",
                i + 1,
                {
                    "camera": i < vreal,
                    "screen": i < int(self.plan.get("screen_real", 0)),
                    "smallCamera": True,
                    "synthMic": i >= 12,
                },
            )
            p.grant, p.participant = self.server.grant(p.slot, p.name)
            self.pages.append(p)
        synths = []
        for j in range(synth):
            slot = real + j + 1
            g, pid = self.server.grant(slot, f"s{j}")
            synths.append(
                {
                    "grant": base64.b64encode(g.SerializeToString()).decode(),
                    "id": pid.hex(),
                    "slot": slot,
                    "video": j < vsynth,
                }
            )
        roster = {p.participant.hex(): p.slot for p in self.pages} | {s["id"]: s["slot"] for s in synths}
        plan = {
            "relay": self.plan["relay"],
            "call_key": base64.b64encode(self.server.call_key).decode(),
            "epoch": self.server.epoch,
            "seconds": seconds + 20,
            "roster": roster,
            "participants": synths,
        }
        planfile = Path(self.plan["workdir"]) / "callload.json"
        planfile.write_text(json.dumps(plan))
        load = await asyncio.create_subprocess_exec(
            self.plan["callload"], str(planfile), stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.DEVNULL
        )
        full_roster = self.roster() + [
            {"participant": s["id"], "member": s["id"], "name": s["id"][:6], "slot": s["slot"], "priority": False}
            for s in synths
        ]
        for p in self.pages:
            ready = asyncio.get_running_loop().create_future()

            def make(p=p, ready=ready) -> None:
                self.make_page(p)
                p.page.loadFinished.connect(
                    lambda ok: asyncio.run_coroutine_threadsafe(self._set(ready, ok), self.loop)
                )

            self.main.run.emit(make)
            assert await ready, "page didn't load"
            p.relay = await Relay(self.plan["relay"]).connect()
            spawn(p.pump())
            sess = p.session() | {"participants": full_roster}
            r = await p.js_async(f"startCall({json.dumps(sess)}, {json.dumps(p.opts)})", 60)
            assert r == "ok", (p.name, r)
        # Receivers show many small tiles: everyone asks for the low layer.
        video_ids = [p.participant.hex() for p in self.pages[:vreal]] + [s["id"] for s in synths[:vsynth]]
        for p in self.pages:
            for vid in video_ids:
                if vid != p.participant.hex():
                    await p.js(f"call.watch({json.dumps(vid)}, 'camera', 100); 0")
            # And everyone watches the shared screens.
            for q in self.pages[: int(self.plan.get("screen_real", 0))]:
                if q is not p:
                    await self.watch(p, q, "screen", 300)
        await asyncio.sleep(seconds)
        pages = {}
        for p in self.pages:
            d = await self.debug(p)
            others = [x for x in roster if x != p.participant.hex()]
            pages[p.name] = {
                "connection": d["connection"],
                "audio_from": sum(1 for r in d["remote"] if r["audio"]),
                "camera_from": sum(1 for r in d["remote"] if r["camera"]),
                "screen_from": sum(1 for r in d["remote"] if r["screen"]),
                "decrypted_from": sum(1 for x in others if d["stats"]["from"].get(x, 0) > 0),
                "failed": d["stats"]["failed"],
                "why": d["stats"]["why"],
            }
        out, _ = await load.communicate()
        synth_res = json.loads(out.decode().strip().splitlines()[-1])
        self.out["mixed"] = {
            "participants": real + synth,
            "pages": pages,
            "synth": [
                {
                    "joined": s["joined"],
                    "error": s.get("error", ""),
                    "audio_from": len([k for k, v in s["audio_from"].items() if v > 0]),
                    "video_from": len([k for k, v in s["video_from"].items() if v > 0]),
                    "failed": s["failed"],
                    "offers": s.get("offers", 0),
                    "answers": s.get("answers", 0),
                    "tracks": s.get("tracks", 0),
                    "reneg_err": s.get("reneg_err", ""),
                }
                for s in synth_res
            ],
        }

    async def wire(self) -> None:
        """What the relay sees of a's voice: every RTP header on the path
        between the clients and the relay (through the proxy), while a
        sends speech, a tone, noise or silence (generated in the page, so
        each phase is known), in studio mode, with push to talk held and
        with it released. b only listens (it sends no audio). Also every
        SDP a exchanged with the relay."""
        self.proxy.record = []
        a = await self.add("a", {"mode": "studio", "signal": True})
        b = await self.add("b", {"perms": {"audio": False, "video": True, "screen": True, "vad": True}})
        await asyncio.sleep(3)
        seconds = float(self.plan.get("seconds", 3))
        phases = []
        for mode, signal, ptt in self.plan["phases"]:
            await a.js(f"setSignal({json.dumps(signal)}, {json.dumps(mode)}, {json.dumps(ptt)}); 0")
            await asyncio.sleep(0.8)  # the encoder settles on the new sound
            start = len(self.proxy.record)
            t0 = time.monotonic()
            await asyncio.sleep(seconds)
            took = time.monotonic() - t0
            groups: dict = {}
            for r in self.proxy.record[start:]:
                key = f"{'up' if r['up'] else 'down'}/{r['pt']}"
                g = groups.setdefault(key, {"n": 0, "bytes": 0, "sizes": {}, "ext": {}, "markers": 0})
                g["n"] += 1
                g["bytes"] += r["len"]
                g["sizes"][r["size"]] = g["sizes"].get(r["size"], 0) + 1
                for k, v in r["ext"].items():
                    g["ext"][f"{k}:{v}"] = g["ext"].get(f"{k}:{v}", 0) + 1
                g["markers"] += r["marker"]
            for g in groups.values():
                g["kbps"] = round(g["bytes"] * 8 / took / 1000, 1)
            phases.append({"mode": mode, "signal": signal, "ptt": ptt, "seconds": round(took, 2), "groups": groups})
        self.out["phases"] = phases
        self.out["sdps"] = a.sdps + b.sdps
        self.out["local"] = await a.js("call.pc.localDescription.sdp")
        self.out["remote"] = await a.js("call.pc.remoteDescription.sdp")
        self.out["heard"] = await self.speaking_over(b, 1)
        self.out["pages"] = await self.by_name()

    # ---------------------------------------------------------------- running

    async def run_async(self) -> None:
        self.loop = asyncio.get_running_loop()
        px = self.plan.get("proxy")
        if px:
            self.proxy = Proxy(tuple(px["upstream"]), px.get("loss", 0), px.get("delay_ms", 0), px.get("jitter_ms", 0))
            await self.loop.create_datagram_endpoint(lambda: self.proxy, local_addr=tuple(px["listen"]))
        try:
            await getattr(self, self.plan["scenario"])()
        except Exception as e:
            import traceback

            self.out["error"] = f"{e!r}\n{traceback.format_exc()}"
        self.out["events"] = self.events
        print(json.dumps(self.out), flush=True)
        self.main.run.emit(QApplication.quit)


def _update(ctl: cw.SignedCallControl):
    from openarcanum.wire import relay_pb2 as rp

    return rp.RelayFrame(call_update=cw.CallUpdate(control=ctl))


def tone_bursts(path: Path, seconds: int = 10) -> None:
    """A 'microphone' for Chromium's fake device: 0.5 s of 440 Hz at half
    scale, then 0.5 s of silence, repeated."""
    import math
    import struct
    import wave

    rate = 48000
    frames = bytearray()
    for i in range(rate * seconds):
        on = (i // (rate // 2)) % 2 == 0
        v = int(16000 * math.sin(2 * math.pi * 440 * i / rate)) if on else 0
        frames += struct.pack("<h", v)
    with wave.open(str(path), "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(rate)
        w.writeframes(bytes(frames))


def noise_bed(path: Path, noise_dbfs: float, speech_dbfs: float | None = None, seconds: int = 10) -> None:
    """A 'microphone' in a noisy room: steady background noise (white noise
    through a gentle low-pass, like a fan or a computer, at noise_dbfs RMS)
    and, if speech_dbfs is given, a voice-like sound over it half of each
    second (a 180 Hz tone with harmonics, at speech_dbfs RMS while on)."""
    import math
    import struct
    import wave

    rate = 48000
    rng = random.Random(7)
    raw = []
    lp = 0.0
    for _ in range(rate * seconds):
        lp += 0.3 * (rng.uniform(-1, 1) - lp)
        raw.append(lp)
    rms = math.sqrt(sum(x * x for x in raw) / len(raw))
    noise_gain = 10 ** (noise_dbfs / 20) / rms
    # Harmonics of 180 Hz at falling levels: RMS of the sum, for scaling.
    harmonics = [(1, 1.0), (2, 0.6), (3, 0.4), (4, 0.25), (5, 0.15)]
    voice_rms = math.sqrt(sum(a * a for _h, a in harmonics) / 2)
    voice_gain = 10 ** (speech_dbfs / 20) / voice_rms if speech_dbfs is not None else 0.0
    frames = bytearray()
    for i, n in enumerate(raw):
        v = n * noise_gain
        if voice_gain and (i // (rate // 2)) % 2 == 0:
            t = i / rate
            v += voice_gain * sum(a * math.sin(2 * math.pi * 180 * h * t) for h, a in harmonics)
        frames += struct.pack("<h", max(-32767, min(32767, int(v * 32767))))
    with wave.open(str(path), "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(rate)
        w.writeframes(bytes(frames))


def main() -> None:
    plan = json.loads(sys.argv[1])
    app = QApplication(sys.argv[:1])
    h = Harness(plan)
    h.profile = QWebEngineProfile()  # off the record
    _ = QWebEnginePermission  # (the grant API the app uses too)
    t = threading.Thread(target=lambda: asyncio.run(h.run_async()), daemon=True)
    t.start()
    app.exec()


if __name__ == "__main__":
    main()
