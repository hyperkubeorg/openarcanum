"""Fire-and-forget tasks that are kept referenced until they finish."""

from __future__ import annotations

import asyncio
import logging

log = logging.getLogger(__name__)
_tasks: set[asyncio.Future] = set()


def _done(t: asyncio.Future) -> None:
    _tasks.discard(t)
    if not t.cancelled() and t.exception() is not None:
        log.error("background task failed", exc_info=t.exception())


def spawn(coro) -> asyncio.Future:
    t = asyncio.ensure_future(coro)
    _tasks.add(t)
    t.add_done_callback(_done)
    return t
