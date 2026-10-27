from __future__ import annotations

import asyncio

import js
from pyodide.ffi import create_proxy, to_js

from websockets.exceptions import ConnectionClosed

_CLOSED = object()


class ClientConnection:
    """A browser WebSocket with websockets' asyncio interface: send, recv,
    async iteration and close. Binary frames arrive as bytes, text as str.
    Pings are the browser's business, so ping_interval/ping_timeout do nothing."""

    def __init__(self, url: str, max_size: int | None):
        self._max_size = max_size
        self._queue: asyncio.Queue = asyncio.Queue()
        self._opened: asyncio.Future = asyncio.get_running_loop().create_future()
        self.close_code: int | None = None
        self.close_reason = ""
        self._ws = js.WebSocket.new(url)
        self._ws.binaryType = "arraybuffer"
        self._proxies = [
            create_proxy(self._on_open),
            create_proxy(self._on_message),
            create_proxy(self._on_close),
            create_proxy(self._on_error),
        ]
        self._ws.onopen, self._ws.onmessage, self._ws.onclose, self._ws.onerror = self._proxies

    def _on_open(self, _event) -> None:
        if not self._opened.done():
            self._opened.set_result(None)

    def _on_message(self, event) -> None:
        data = event.data
        msg = data if isinstance(data, str) else js.Uint8Array.new(data).to_bytes()
        if self._max_size is not None and len(msg) > self._max_size:
            self._ws.close(1009, "message too big")
            return
        self._queue.put_nowait(msg)

    def _on_close(self, event) -> None:
        self.close_code, self.close_reason = int(event.code), str(event.reason)
        if not self._opened.done():
            self._opened.set_exception(ConnectionClosed(self.close_code, self.close_reason))
        self._queue.put_nowait(_CLOSED)
        for p in self._proxies:
            p.destroy()
        self._proxies = []

    def _on_error(self, _event) -> None:
        """Always followed by a close event, which carries what there is to know."""

    def _closed_error(self) -> ConnectionClosed:
        return ConnectionClosed(self.close_code or 1006, self.close_reason)

    async def send(self, message: bytes | str) -> None:
        if self.close_code is not None or int(self._ws.readyState) != 1:
            raise self._closed_error()
        if isinstance(message, str):
            self._ws.send(message)
        else:
            self._ws.send(to_js(bytes(message)))

    async def recv(self):
        if self._queue.empty() and self.close_code is not None:
            raise self._closed_error()
        msg = await self._queue.get()
        if msg is _CLOSED:
            self._queue.put_nowait(_CLOSED)  # every later recv fails too
            raise self._closed_error()
        return msg

    def __aiter__(self):
        return self

    async def __anext__(self):
        try:
            return await self.recv()
        except ConnectionClosed as e:
            if e.code in (1000, 1001):
                raise StopAsyncIteration from None
            raise

    async def close(self, code: int = 1000, reason: str = "") -> None:
        if self.close_code is None and int(self._ws.readyState) < 2:
            self._ws.close(code, reason)


async def connect(
    url: str,
    max_size: int | None = 1 << 20,
    ping_interval: float | None = None,
    ping_timeout: float | None = None,
    open_timeout: float | None = 10,
    **_ignored,
) -> ClientConnection:
    conn = ClientConnection(url, max_size)
    try:
        await asyncio.wait_for(asyncio.shield(conn._opened), open_timeout)
    except BaseException:
        await conn.close()
        raise
    return conn
