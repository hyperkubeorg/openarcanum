"""Local directory search (docs/protocol.md §7.2).

With the "Search on this device" setting on, the client downloads the
directory snapshot from a maintainer node (every listing verified, the size
bounded by meta.MAX_SNAPSHOT_*), keeps it in memory only, and answers
searches itself, so no maintainer sees the search terms. The copy
is refreshed when it is older than REFRESH_AFTER.
"""

from __future__ import annotations

import asyncio
import re
import time

from openarcanum.meta import MetaClient

REFRESH_AFTER = 3600.0
RESULT_LIMIT = 25
_WORD = re.compile(r"[^\W_]+", re.UNICODE)


def _words(s: str) -> list[str]:
    return [w.casefold() for w in _WORD.findall(s)]


def matches(listing: dict, terms: list[str], tag: str) -> int:
    """A score above 0 when every term prefixes a word of the listing's
    name, description or tags (like the maintainer's prefix search) and the
    tag, if any, is one of its tags. Name hits count most."""
    if tag and tag.casefold() not in (t.casefold() for t in listing["tags"]):
        return 0
    if not terms:
        return 1
    name = _words(listing["name"])
    rest = _words(listing["description"]) + [w for t in listing["tags"] for w in _words(t)]
    score = 0
    for term in terms:
        if any(w.startswith(term) for w in name):
            score += 3
        elif any(w.startswith(term) for w in rest):
            score += 1
        else:
            return 0
    return score


def search(listings: list[dict], query: str, tag: str = "", limit: int = RESULT_LIMIT) -> list[dict]:
    terms = _words(query)[:8]
    tag = tag.strip()
    scored = [(matches(lst, terms, tag), lst) for lst in listings]
    hits = [(s, lst) for s, lst in scored if s > 0]
    hits.sort(key=lambda x: (-x[0], -x[1].get("published_at", 0), x[1]["name"]))
    return [lst for _, lst in hits[:limit]]


class LocalDirectory:
    """The downloaded copy of the directory."""

    def __init__(self, meta: MetaClient):
        self.meta = meta
        self.listings: list[dict] = []
        self.complete = True
        self.fetched_at = 0.0
        self._lock = asyncio.Lock()

    def stale(self) -> bool:
        return not self.fetched_at or time.monotonic() - self.fetched_at > REFRESH_AFTER

    async def refresh(self, force: bool = False) -> None:
        async with self._lock:
            if not force and not self.stale():
                return
            self.listings, self.complete = await self.meta.snapshot()
            self.fetched_at = time.monotonic()

    async def search(self, query: str, tag: str = "") -> list[dict]:
        await self.refresh()
        return search(self.listings, query, tag)

    def status(self) -> dict:
        return {
            "listings": len(self.listings),
            "complete": self.complete,
            "age_seconds": int(time.monotonic() - self.fetched_at) if self.fetched_at else None,
        }

    def forget(self) -> None:
        self.listings, self.fetched_at, self.complete = [], 0.0, True
