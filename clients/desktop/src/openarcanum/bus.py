"""Python → JS events.

Nothing outside the one daemon sender thread ever calls `window.evaluate_js`,
which waits with no timeout for the Qt main loop to answer. Callers only
enqueue, so pywebview's (non-daemon) bridge threads never block on the GUI
loop, and a send stranded by shutdown can't keep the process alive.
"""

from __future__ import annotations

import json
import logging
import queue
import threading
from typing import Any

log = logging.getLogger(__name__)


def _json_default(o: Any):
    if isinstance(o, bytes):
        import base64

        return base64.b64encode(o).decode()
    if isinstance(o, set):
        return sorted(o)
    raise TypeError(f"not JSON serializable: {type(o).__name__}")


def payload(event: str, data: Any) -> str:
    body = json.dumps({"event": event, "data": data}, default=_json_default)
    # JSON is valid JS; escape the two line separators JSON allows but JS strings don't.
    body = body.replace("\u2028", "\\u2028").replace("\u2029", "\\u2029")
    return f"window.__openarcanumEvent && window.__openarcanumEvent({body})"


class Bus:
    def __init__(self):
        self._window = None
        self._ready = threading.Event()
        self._q: queue.SimpleQueue = queue.SimpleQueue()
        self._backlog: list[str] = []
        threading.Thread(target=self._sender, daemon=True, name="bus-sender").start()

    def set_window(self, window) -> None:
        self._window = window

    def mark_ready(self) -> None:
        """The page has loaded and defined window.__openarcanumEvent."""
        self._ready.set()

    def push(self, event: str, data: Any = None) -> None:
        try:
            self._q.put(payload(event, data))
        except TypeError, ValueError:
            log.exception("event %s not serializable", event)

    def _sender(self) -> None:
        while True:
            self._backlog.append(self._q.get())
            # Hold events until the page can receive them, keeping order.
            self._ready.wait()
            batch, self._backlog = self._backlog, []
            for item in batch:
                try:
                    self._window.evaluate_js(item)
                except Exception:
                    pass  # window died mid-send: the event is moot
