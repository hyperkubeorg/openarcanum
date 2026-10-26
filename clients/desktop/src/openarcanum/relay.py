"""Relay connection over WebSocket (docs/protocol.md §2).

One `Relay` is one connection: topic publish/subscribe with chunking and
reassembly, plus byte streams to registered endpoints. Everything runs on the
client's asyncio loop.
"""

from __future__ import annotations

import asyncio
import contextlib
import ipaddress
import itertools
import logging
import posixpath
import time
from collections.abc import Awaitable, Callable
from urllib.parse import urlsplit

from google.protobuf.message import DecodeError
from websockets.asyncio.client import ClientConnection, connect
from websockets.exceptions import ConnectionClosed, InvalidStatus

from openarcanum import pow as relay_pow
from openarcanum.keys import CTX_RELAY_ENDPOINT, CTX_RELAY_HELLO, SigningKey, is_valid
from openarcanum.tasks import spawn
from openarcanum.version import SUPPORTED, VersionMismatch, negotiate
from openarcanum.wire import call_pb2 as cp_call
from openarcanum.wire import relay_pb2 as rp

log = logging.getLogger(__name__)

INBOUND_STREAM_BIT = 0x80000000
STREAM_BUFFER_BYTES = 64 << 20
# The largest message this client sends or reassembles, whatever the relay
# announces.
MAX_MESSAGE = 25 << 20
# Messages being reassembled (a relay allows a publisher max_partial in
# flight): at most this many in all and from one sender, holding at most
# PARTIAL_BYTES together; the oldest go first.
MAX_PARTIALS = 1024
MAX_PARTIALS_BY_SENDER = 64
PARTIAL_BYTES = 64 << 20

Handler = Callable[[bytes, bytes], Awaitable[None] | None]


# ---------------------------------------------------------------- relay identity
# docs/protocol.md §2 "Relay identity" (Go: internal/relayid). Kept apart
# from the connection code below.


def canonical_url(url: str) -> str | None:
    """A relay URL in its canonical form (internal/relayurl), or None when
    it isn't one this client can put in that form."""
    if len(url) > 256 or "#" in url or "?" in url or "%" in url:
        return None
    try:
        u = urlsplit(url)
        host = u.hostname
        port = u.port
    except ValueError:
        return None
    scheme = u.scheme.lower()
    if scheme not in ("ws", "wss") or not host or u.username is not None or u.password is not None:
        return None
    try:
        ip = ipaddress.ip_address(host)
    except ValueError:
        host = host.lower().rstrip(".")
        if not host or not all(c.isascii() and (c.isalnum() or c in "-_.") for c in host):
            return None
    else:
        if getattr(ip, "scope_id", None):
            return None
        if isinstance(ip, ipaddress.IPv6Address) and ip.ipv4_mapped is not None:
            ip = ip.ipv4_mapped
        host = f"[{ip}]" if ip.version == 6 else str(ip)
    if port is not None and (port, scheme) not in ((80, "ws"), (443, "wss")):
        host = f"{host}:{port}"
    path = u.path
    if path:
        path = posixpath.normpath("/" + path)
        path = "" if path == "/" else path.replace("//", "/")
    return f"{scheme}://{host}{path}"


# Keys of relays without a URL, and the URL each was first registered at.
_URLLESS_KEYS: dict[bytes, str] = {}


def binding(relay_key: bytes, url: str, nonce: bytes) -> bytes:
    """The signed form of a RelayBinding."""
    return rp.RelayBinding(relay_key=relay_key, url=url, nonce=nonce).SerializeToString(deterministic=True)


def verify_hello(hello: rp.Hello) -> None:
    """Refuse a Hello without a valid proof of the relay's key."""
    if hello.url and canonical_url(hello.url) != hello.url:
        raise RelayError("relay announced a malformed URL")
    if len(hello.relay_key) != 32 or not is_valid(
        bytes(hello.relay_key),
        CTX_RELAY_HELLO,
        binding(bytes(hello.relay_key), hello.url, bytes(hello.nonce)),
        bytes(hello.relay_signature),
    ):
        raise RelayError("relay sent no valid proof of its key")


class RelayError(Exception):
    pass


class RelayClosed(RelayError):
    pass


class RelayVersionError(RelayError):
    """No protocol version in common with the relay; the message says which
    side is outdated ("update OpenArcanum" or "this relay is outdated")."""

    def __init__(self, mismatch: VersionMismatch):
        super().__init__(mismatch.user_message())
        self.mismatch = mismatch


class RelayRefused(RelayError):
    """The relay refused Start (version or proof-of-work)."""


class RelayBlocked(RelayRefused):
    """The relay refuses connections from this network: a relay dedicated
    to one community blocked the address after a ban (docs/protocol.md §2
    "Dedicated relays")."""


class StreamClosed(RelayError):
    pass


class Stream:
    """A message-preserving byte stream to (or from) an endpoint."""

    def __init__(self, relay: Relay, stream_id: int):
        self.relay = relay
        self.id = stream_id
        self._queue: asyncio.Queue[bytes | None] = asyncio.Queue()
        self._buffered = 0
        self.closed = False
        self.reason = ""

    @property
    def max_chunk(self) -> int:
        return self.relay.hello.max_chunk

    async def send(self, data: bytes) -> None:
        if self.closed:
            raise StreamClosed(self.reason or "closed")
        if len(data) > self.max_chunk:
            raise RelayError(f"stream chunk of {len(data)} bytes exceeds {self.max_chunk}")
        await self.relay._write(rp.RelayFrame(stream_data=rp.StreamData(stream_id=self.id, data=data)))

    async def recv(self, timeout: float | None = None) -> bytes:
        if self.closed and self._queue.empty():
            raise StreamClosed(self.reason or "closed")
        item = await asyncio.wait_for(self._queue.get(), timeout)
        if item is None:
            self._queue.put_nowait(None)
            raise StreamClosed(self.reason or "closed")
        self._buffered -= len(item)
        return item

    def _push(self, data: bytes) -> None:
        if self.closed:
            return
        if self._buffered + len(data) > STREAM_BUFFER_BYTES:
            spawn(self.close("receiver overflow"))
            return
        self._buffered += len(data)
        self._queue.put_nowait(data)

    def _remote_close(self, reason: str) -> None:
        if not self.closed:
            self.closed = True
            self.reason = reason
            self._queue.put_nowait(None)

    async def close(self, reason: str = "closed") -> None:
        if self.closed:
            return
        self._remote_close(reason)
        self.relay._streams.pop(self.id, None)
        with contextlib.suppress(RelayError, ConnectionClosed):
            await self.relay._write(rp.RelayFrame(stream_close=rp.StreamClose(stream_id=self.id, reason=reason)))


class Relay:
    def __init__(self, url: str, versions: list[int] | None = None):
        self.url = url
        self.versions = list(versions or SUPPORTED)
        self.version = 0
        self.hello: rp.Hello | None = None
        self._ws: ClientConnection | None = None
        self._reader: asyncio.Task | None = None
        self._write_lock = asyncio.Lock()
        self._req = itertools.count(1)
        self._msg = itertools.count(1)
        self._stream_ids = itertools.count(1)
        self._pending: dict[int, asyncio.Future] = {}
        self._handlers: dict[bytes, Handler] = {}
        # (sender, message id) -> [next seq, data so far, started at, topic]
        self._partial: dict[tuple[int, int], list] = {}
        self._partial_by: dict[int, int] = {}
        self._partial_bytes = 0
        self._streams: dict[int, Stream] = {}
        self._opening: dict[int, asyncio.Future] = {}
        self._incoming: asyncio.Queue[Stream] = asyncio.Queue()
        self.closed = asyncio.Event()
        self.close_reason = ""
        self._on_close: list[Callable[[Relay], None]] = []
        # Call frames from the relay's SFU, by call id (docs/protocol.md §12).
        self._call_handlers: dict[bytes, Callable[[rp.RelayFrame], None]] = {}
        # Handlers for messages replayed from the relay's chunk cache.
        self._replay_handlers: dict[bytes, Handler] = {}

    def __repr__(self) -> str:
        return f"Relay({self.url})"

    @property
    def connected(self) -> bool:
        return self._ws is not None and not self.closed.is_set()

    def on_close(self, cb: Callable[[Relay], None]) -> None:
        self._on_close.append(cb)

    async def connect(self, timeout: float = 15.0) -> Relay:
        try:
            self._ws = await asyncio.wait_for(
                connect(self.url, max_size=64 << 20, ping_interval=20, ping_timeout=20, open_timeout=timeout),
                timeout,
            )
            first = await asyncio.wait_for(self._ws.recv(), timeout)
        except InvalidStatus as e:
            # The relay answered the upgrade with an HTTP error: 403 is a
            # blocked network, 429/503 a busy relay.
            if e.response.status_code == 403:
                raise RelayBlocked("the relay refuses connections from this network") from e
            raise RelayError(f"cannot connect to relay: HTTP {e.response.status_code}") from e
        except (OSError, TimeoutError, ConnectionClosed) as e:
            if self._ws is not None:
                await self._ws.close()
            raise RelayError(f"cannot connect to relay: {e}") from e
        frame = rp.RelayFrame()
        try:
            if not isinstance(first, bytes):
                raise RelayError("relay sent a text frame")
            try:
                frame.ParseFromString(first)
            except DecodeError as e:
                raise RelayError("relay sent a malformed hello") from e
            if not frame.HasField("hello"):
                raise RelayError("relay did not send hello")
            verify_hello(frame.hello)
            self.hello = frame.hello
            self.hello.max_message = min(self.hello.max_message, MAX_MESSAGE)
            await self._start(frame.hello, timeout)
        except RelayError:
            await self._ws.close()
            raise
        except (OSError, TimeoutError, ConnectionClosed) as e:
            await self._ws.close()
            raise RelayError(f"cannot connect to relay: {e}") from e
        self._reader = asyncio.create_task(self._read_loop(), name=f"relay-reader {self.url}")
        return self

    async def _start(self, hello: rp.Hello, timeout: float) -> None:
        """Choose a protocol version, solve the proof-of-work if the relay
        asks for one (in a worker thread, so neither the UI nor the event
        loop stalls), send Start and wait for its Ack (docs/protocol.md §2)."""
        try:
            self.version = negotiate(self.versions, list(hello.versions), what="relay")
        except VersionMismatch as e:
            raise RelayVersionError(e) from None
        window = (hello.start_window_ms or 30_000) / 1000
        try:
            counter = await asyncio.to_thread(
                relay_pow.solve, bytes(hello.nonce), hello.pow_bits, time.monotonic() + window
            )
        except relay_pow.PowError as e:
            raise RelayError(f"cannot connect to relay: {e}") from None
        assert self._ws is not None
        start = rp.RelayFrame(start=rp.Start(version=self.version, pow=counter, req=1))
        await self._ws.send(start.SerializeToString())
        raw = await asyncio.wait_for(self._ws.recv(), timeout)
        reply = rp.RelayFrame()
        try:
            if not isinstance(raw, bytes):
                raise DecodeError("text frame")
            reply.ParseFromString(raw)
        except DecodeError as e:
            raise RelayError("relay sent a malformed reply to Start") from e
        if reply.HasField("error"):
            raise RelayRefused(f"the relay refused the connection: {reply.error.message}")
        if not reply.HasField("ack") or reply.ack.req != 1:
            raise RelayError("relay did not acknowledge Start")

    async def close(self, reason: str = "closed") -> None:
        await self._shutdown(reason)
        if self._reader is not None and self._reader is not asyncio.current_task():
            self._reader.cancel()
            with contextlib.suppress(asyncio.CancelledError, Exception):
                await self._reader

    async def _shutdown(self, reason: str) -> None:
        if self.closed.is_set():
            return
        self.close_reason = reason
        self.closed.set()
        for fut in list(self._pending.values()) + list(self._opening.values()):
            if not fut.done():
                fut.set_exception(RelayClosed(reason))
        for s in list(self._streams.values()):
            s._remote_close("relay connection closed")
        self._streams.clear()
        if self._ws is not None:
            with contextlib.suppress(Exception):
                await self._ws.close()
        for cb in self._on_close:
            try:
                cb(self)
            except Exception:
                log.exception("relay close callback failed")

    async def _write(self, frame: rp.RelayFrame) -> None:
        if self.closed.is_set() or self._ws is None:
            raise RelayClosed(self.close_reason or "not connected")
        data = frame.SerializeToString()
        async with self._write_lock:
            try:
                await self._ws.send(data)
            except ConnectionClosed as e:
                await self._shutdown("connection lost")
                raise RelayClosed("connection lost") from e

    async def _request(self, frame: rp.RelayFrame, req: int, timeout: float = 15.0) -> None:
        fut = asyncio.get_running_loop().create_future()
        self._pending[req] = fut
        try:
            await self._write(frame)
            result: rp.RelayFrame = await asyncio.wait_for(fut, timeout)
        finally:
            self._pending.pop(req, None)
        if result.HasField("error"):
            raise RelayError(f"relay: {result.error.message}")

    async def call_join(
        self, join: cp_call.CallJoin, on_frame: Callable[[rp.RelayFrame], None], timeout: float = 20.0
    ) -> str:
        """Joins a call through this relay's SFU; returns the SDP answer.
        Later call_signal, call_state and call_watchers frames for the call go
        to on_frame."""
        grant = cp_call.CallGrant.FromString(join.grant.grant)
        self._call_handlers[grant.call_id] = on_frame
        req = next(self._req)
        join.req = req
        fut = asyncio.get_running_loop().create_future()
        self._pending[req] = fut
        try:
            await self._write(rp.RelayFrame(call_join=join))
            result: rp.RelayFrame = await asyncio.wait_for(fut, timeout)
        except BaseException:
            self._call_handlers.pop(grant.call_id, None)
            raise
        finally:
            self._pending.pop(req, None)
        if result.HasField("error"):
            self._call_handlers.pop(grant.call_id, None)
            raise RelayError(f"relay: {result.error.message}")
        return result.call_answer.answer

    async def call_signal(self, call_id: bytes, kind: str, sdp: str) -> None:
        await self._write(rp.RelayFrame(call_signal=cp_call.CallSignal(call_id=call_id, type=kind, sdp=sdp)))

    async def call_layer(self, call_id: bytes, source: bytes, kind: str, rid: str) -> None:
        await self._write(
            rp.RelayFrame(call_layer=cp_call.CallLayer(call_id=call_id, source=source, kind=kind, rid=rid))
        )

    async def call_listen(self, call_id: bytes, deaf: bool, no_audio: list[bytes], screen_audio: list[bytes]) -> None:
        await self._write(
            rp.RelayFrame(
                call_listen=cp_call.CallListen(call_id=call_id, deaf=deaf, no_audio=no_audio, screen_audio=screen_audio)
            )
        )

    async def call_leave(self, call_id: bytes) -> None:
        self._call_handlers.pop(call_id, None)
        with contextlib.suppress(RelayError):
            await self._write(rp.RelayFrame(call_leave=cp_call.CallLeave(call_id=call_id)))

    @property
    def cache_ttl_ms(self) -> int:
        """How far back the relay's chunk cache reaches (0 = no cache)."""
        return self.hello.cache_ttl_ms if self.hello is not None else 0

    async def subscribe(
        self, topic: bytes, handler: Handler, replay_ms: int = 0, on_replay: Handler | None = None
    ) -> None:
        """Deliver messages on topic to handler. With replay_ms, also ask the
        relay's chunk cache for cacheable messages from that far back; those
        go to on_replay (default: handler)."""
        self._handlers[topic] = handler
        if on_replay is not None:
            self._replay_handlers[topic] = on_replay
        req = next(self._req)
        await self._request(rp.RelayFrame(subscribe=rp.Subscribe(topic=topic, req=req, replay_ms=replay_ms)), req)

    async def unsubscribe(self, topic: bytes) -> None:
        self._handlers.pop(topic, None)
        self._replay_handlers.pop(topic, None)
        req = next(self._req)
        await self._request(rp.RelayFrame(unsubscribe=rp.Unsubscribe(topic=topic, req=req)), req)

    def subscribed(self) -> list[bytes]:
        return list(self._handlers)

    async def publish(self, topic: bytes, data: bytes, cacheable: bool = False) -> None:
        """Send data to topic. `cacheable` marks an end-to-end encrypted
        payload the relay may keep in its chunk cache."""
        assert self.hello is not None
        if len(data) > self.hello.max_message:
            raise RelayError(f"message of {len(data)} bytes exceeds relay limit {self.hello.max_message}")
        mid = next(self._msg)
        chunk = self.hello.max_chunk
        seq = 0
        off = 0
        while True:
            end = min(off + chunk, len(data))
            final = end == len(data)
            await self._write(
                rp.RelayFrame(
                    publish=rp.Publish(
                        topic=topic, message_id=mid, seq=seq, final=final, data=data[off:end], cacheable=cacheable
                    )
                )
            )
            if final:
                return
            off = end
            seq += 1

    async def register_endpoint(self, key: SigningKey) -> None:
        """Register as the endpoint for key, bound to this relay's key, the
        URL it was dialled at and this connection (a relay that knows its
        URL must be dialled at it)."""
        assert self.hello is not None
        url = canonical_url(self.url)
        if url is None:
            raise RelayError("relay URL isn't a canonical relay URL")
        if self.hello.url and self.hello.url != url:
            raise RelayError(f"relay is {self.hello.url}, dialled as {url}: dial it at its own URL")
        if not self.hello.url:
            # A relay without a URL can't check it: its key is accepted at
            # one URL only in this process (Go: relayclient.KeyBook).
            seen = _URLLESS_KEYS.setdefault(bytes(self.hello.relay_key), url)
            if seen != url:
                raise RelayError("this relay key was first seen at another URL")
            if len(_URLLESS_KEYS) > 4096:
                _URLLESS_KEYS.clear()
        req = next(self._req)
        sig = key.sign(CTX_RELAY_ENDPOINT, binding(bytes(self.hello.relay_key), url, bytes(self.hello.nonce)))
        await self._request(
            rp.RelayFrame(
                register_endpoint=rp.RegisterEndpoint(endpoint_key=key.public, signature=sig, url=url, req=req)
            ),
            req,
        )

    async def open_stream(self, endpoint_key: bytes, timeout: float = 15.0) -> Stream:
        sid = next(self._stream_ids) & 0x7FFFFFFF
        stream = Stream(self, sid)
        self._streams[sid] = stream
        fut = asyncio.get_running_loop().create_future()
        self._opening[sid] = fut
        try:
            await self._write(rp.RelayFrame(open_stream=rp.OpenStream(endpoint_key=endpoint_key, stream_id=sid)))
            await asyncio.wait_for(fut, timeout)
        except BaseException:
            self._streams.pop(sid, None)
            raise
        finally:
            self._opening.pop(sid, None)
        return stream

    async def accept(self) -> Stream:
        return await self._incoming.get()

    async def _read_loop(self) -> None:
        assert self._ws is not None
        try:
            async for msg in self._ws:
                if not isinstance(msg, bytes):
                    continue
                frame = rp.RelayFrame()
                try:
                    frame.ParseFromString(msg)
                except DecodeError:
                    log.info("dropping a relay frame that doesn't parse (%d bytes)", len(msg))
                    continue
                await self._dispatch(frame)
            await self._shutdown("relay closed the connection")
        except ConnectionClosed:
            await self._shutdown("connection lost")
        except asyncio.CancelledError:
            await self._shutdown("closed")
            raise
        except Exception:
            log.exception("relay reader failed")
            await self._shutdown("reader failed")

    async def _dispatch(self, f: rp.RelayFrame) -> None:
        kind = f.WhichOneof("kind")
        if kind == "ack":
            self._resolve(f.ack.req, f)
        elif kind == "error":
            if f.error.req:
                self._resolve(f.error.req, f)
            else:
                log.debug("relay error: %s", f.error.message)
        elif kind == "deliver":
            await self._deliver(f.deliver)
        elif kind == "call_answer":
            self._resolve(f.call_answer.req, f)
        elif kind in ("call_signal", "call_state", "call_watchers"):
            h = self._call_handlers.get(getattr(f, kind).call_id)
            if h is not None:
                h(f)
        elif kind == "stream_opened":
            sid = f.stream_opened.stream_id
            if sid & INBOUND_STREAM_BIT:
                if sid in self._streams or self.closed.is_set():
                    # A relay must not reuse a live stream id; replacing the
                    # stream would orphan the one already handed to accept().
                    return
                s = Stream(self, sid)
                self._streams[sid] = s
                self._incoming.put_nowait(s)
            else:
                fut = self._opening.get(sid)
                if fut and not fut.done():
                    fut.set_result(None)
        elif kind == "stream_data":
            s = self._streams.get(f.stream_data.stream_id)
            if s is not None:
                s._push(f.stream_data.data)
        elif kind == "stream_close":
            sid = f.stream_close.stream_id
            s = self._streams.pop(sid, None)
            fut = self._opening.get(sid)
            if fut and not fut.done():
                fut.set_exception(RelayError(f"stream refused: {f.stream_close.reason}"))
            if s is not None:
                s._remote_close(f.stream_close.reason)

    def _drop_partial(self, key: tuple[int, int]) -> None:
        p = self._partial.pop(key, None)
        if p is None:
            return
        self._partial_bytes -= len(p[1])
        sender = key[0]
        self._partial_by[sender] -= 1
        if self._partial_by[sender] <= 0:
            del self._partial_by[sender]

    def _oldest_partial(self, other_than: tuple[int, int]) -> tuple[int, int]:
        return min((k for k in self._partial if k != other_than), key=lambda k: self._partial[k][2])

    def _resolve(self, req: int, f: rp.RelayFrame) -> None:
        fut = self._pending.get(req)
        if fut and not fut.done():
            fut.set_result(f)

    async def _deliver(self, d: rp.Deliver) -> None:
        key = (d.sender, d.message_id)
        p = self._partial.get(key)
        if p is None:
            if d.seq != 0 or self._partial_by.get(d.sender, 0) >= MAX_PARTIALS_BY_SENDER:
                return
            if len(self._partial) >= MAX_PARTIALS:
                cutoff = time.monotonic() - 120
                for k in [k for k, v in self._partial.items() if v[2] < cutoff]:
                    self._drop_partial(k)
            while len(self._partial) >= MAX_PARTIALS:
                self._drop_partial(self._oldest_partial(key))
            p = [0, bytearray(), time.monotonic(), bytes(d.topic)]
            self._partial[key] = p
            self._partial_by[d.sender] = self._partial_by.get(d.sender, 0) + 1
        assert self.hello is not None
        # Every chunk belongs to the topic the first one named.
        if d.seq != p[0] or d.topic != p[3] or len(p[1]) + len(d.data) > self.hello.max_message:
            self._drop_partial(key)
            return
        while self._partial_bytes + len(d.data) > PARTIAL_BYTES and len(self._partial) > 1:
            self._drop_partial(self._oldest_partial(key))
        if self._partial_bytes + len(d.data) > PARTIAL_BYTES:
            self._drop_partial(key)
            return
        p[0] += 1
        p[1] += d.data
        self._partial_bytes += len(d.data)
        if not d.final:
            return
        self._drop_partial(key)
        topic = p[3]
        handler = self._handlers.get(topic)
        if d.replayed:
            handler = self._replay_handlers.get(topic, handler)
        if handler is None:
            return
        try:
            result = handler(topic, bytes(p[1]))
            if asyncio.iscoroutine(result):
                spawn(result)
        except Exception:
            log.exception("topic handler failed")
