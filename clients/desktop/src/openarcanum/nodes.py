"""Maintainer clusters and node health (docs/protocol.md §7.5).

A maintainer cluster is a small set of nodes run by parties who trust each
other; they replicate DM mailboxes, blob records and deletes among
themselves, so any member can stand in for any other. Its member list is a
`SignedMaintainerSet` signed by the cluster's operator key. A configured
maintainer entry is `URL` or `URL#OPERATORKEY`; with a key, this client
fetches the set from any member, keeps it only if it verifies against that
pinned key with a seq at least the one it has, and uses every member.

Node health: per node, consecutive successes and failures, with an
exponential backoff (30 s to 10 min, with jitter) after failures, so a
node that is down is tried last, and requests go to the healthy ones.
"""

from __future__ import annotations

import base64
import binascii
import logging
import random
import time
from dataclasses import dataclass, field

from google.protobuf.message import DecodeError

from openarcanum import keys
from openarcanum.wire import meta_pb2 as mp

log = logging.getLogger(__name__)

MAX_NODES = 8
# A set issued this far ahead of this device's clock is refused.
MAX_AHEAD_MS = 24 * 3600 * 1000
MAX_LIFETIME_MS = 2 * 365 * 24 * 3600 * 1000
# How often pinned sets are fetched again (and at once after a failure).
REFRESH_SECONDS = 6 * 3600
BACKOFF_MIN = 30.0
BACKOFF_MAX = 600.0


class SetError(ValueError):
    """A maintainer set that doesn't verify."""


def parse_entry(entry: str) -> tuple[str, bytes | None]:
    """`URL` or `URL#OPERATORKEY` -> (url, operator key or None). The URL is
    returned as written (settings.py validates it)."""
    url, sep, key = entry.partition("#")
    if not sep:
        return url, None
    try:
        op = keys.decode(key)
    except binascii.Error, ValueError:
        raise ValueError("not an operator key") from None
    if len(op) != 32:
        raise ValueError("not an operator key")
    return url, op


def verify_set(
    raw: bytes | mp.SignedMaintainerSet, operator: bytes | None, now_ms: int | None = None
) -> mp.MaintainerSet:
    """A signed set's content if it verifies: signed by `operator` (any key
    when None: trust on first use, shown to the user to confirm), not
    expired, at most 8 members with distinct http(s) URLs and node keys."""
    signed = raw if isinstance(raw, mp.SignedMaintainerSet) else mp.SignedMaintainerSet()
    if not isinstance(raw, mp.SignedMaintainerSet):
        try:
            signed.ParseFromString(raw)
        except DecodeError as e:
            raise SetError("malformed maintainer set") from e
    s = mp.MaintainerSet()
    try:
        s.ParseFromString(signed.set)
    except DecodeError as e:
        raise SetError("malformed maintainer set") from e
    if len(s.operator_key) != 32 or (operator is not None and s.operator_key != operator):
        raise SetError("maintainer set for another operator key")
    try:
        keys.verify(s.operator_key, keys.CTX_MAINTAINER_SET, signed.set, signed.signature)
    except keys.BadSignature as e:
        raise SetError("maintainer set signature doesn't verify") from e
    now = int(time.time() * 1000) if now_ms is None else now_ms
    if s.issued_at > now + MAX_AHEAD_MS or s.expires_at <= now:
        raise SetError("maintainer set is expired or from the future")
    if s.expires_at <= s.issued_at or s.expires_at - s.issued_at > MAX_LIFETIME_MS:
        raise SetError("maintainer set lifetime out of range")
    if not 1 <= len(s.nodes) <= MAX_NODES:
        raise SetError("maintainer set must name 1 to 8 nodes")
    urls, node_keys = set(), set()
    for n in s.nodes:
        if not n.url.startswith(("https://", "http://")) or n.url.endswith("/") or "#" in n.url:
            raise SetError("maintainer set names a bad URL")
        if len(n.node_key) != 32 or n.url in urls or n.node_key in node_keys or n.role not in ("root", "member"):
            raise SetError("maintainer set members must be distinct and valid")
        urls.add(n.url)
        node_keys.add(n.node_key)
    return s


@dataclass
class Clusters:
    """The verified maintainer sets this device pinned, by operator key."""

    sets: dict[bytes, mp.MaintainerSet] = field(default_factory=dict)
    raw: dict[bytes, bytes] = field(default_factory=dict)

    @classmethod
    def load(cls, cached: list[str], entries: list[str]) -> Clusters:
        """From preferences: each cached set is checked again against the
        operator key an entry pins (sets for keys no entry names are
        dropped)."""
        c = cls()
        pinned = {op for _u, op in (parse_entry(e) for e in entries) if op is not None}
        for item in cached:
            try:
                raw = base64.urlsafe_b64decode(item + "=" * (-len(item) % 4))
                signed = mp.SignedMaintainerSet.FromString(raw)
                s = mp.MaintainerSet.FromString(signed.set)
                if s.operator_key in pinned:
                    c.offer(raw, s.operator_key)
            except (SetError, DecodeError, ValueError, binascii.Error) as e:
                log.info("dropping a cached maintainer set: %s", e)
        return c

    def cached(self) -> list[str]:
        """For preferences."""
        return [base64.urlsafe_b64encode(r).decode().rstrip("=") for r in self.raw.values()]

    def offer(self, raw: bytes, operator: bytes) -> bool:
        """Keep a set if it verifies for `operator` and isn't older than the
        one held. True if it changed what's held."""
        s = verify_set(raw, operator)
        have = self.sets.get(operator)
        if have is not None and s.seq < have.seq:
            return False
        changed = have is None or s.SerializeToString() != have.SerializeToString()
        self.sets[operator] = s
        self.raw[operator] = raw
        return changed

    def expand(self, entries: list[str], members: bool = True) -> tuple[list[str], list[list[str]]]:
        """The nodes to use, and their groups: each cluster's members in set
        order (its configured URL first when the set isn't known, or with
        `members` off only that URL), then plain URLs, each its own group."""
        urls: list[str] = []
        groups: list[list[str]] = []
        for e in entries:
            try:
                url, op = parse_entry(e)
            except ValueError:
                continue
            s = self.sets.get(op) if op is not None else None
            group = [url]
            if s is not None and members:
                group = [n.url for n in s.nodes]
                if url not in group:
                    group.append(url)
            group = [u for u in group if u not in urls][:MAX_NODES]
            if group:
                urls.extend(group)
                groups.append(group)
        return urls, groups

    def describe(self, entries: list[str]) -> list[dict]:
        """For the UI: each pinned cluster with its members."""
        out = []
        for e in entries:
            try:
                url, op = parse_entry(e)
            except ValueError:
                continue
            if op is None:
                continue
            s = self.sets.get(op)
            out.append(
                {
                    "entry": e,
                    "url": url,
                    "operator": keys.encode(op),
                    "fingerprint": keys.fingerprint(op),
                    "seq": s.seq if s else 0,
                    "members": [
                        {"url": n.url, "role": n.role, "region": n.region, "directory": not n.no_directory}
                        for n in (s.nodes if s else [])
                    ],
                }
            )
        return out

    def index_of(self, url: str) -> tuple[bytes, int] | None:
        for op, s in self.sets.items():
            for i, n in enumerate(s.nodes):
                if n.url == url:
                    return op, i
        return None


@dataclass
class NodeState:
    ok_streak: int = 0
    failures: int = 0
    next_try: float = 0.0
    rtt: float = 0.0
    last_error: str = ""


class NodeHealth:
    """Per-node success/failure with exponential backoff."""

    def __init__(self, clock=time.monotonic):
        self.clock = clock
        self.nodes: dict[str, NodeState] = {}
        # Bumped on every failure: the DM poll reads every node at once
        # after one (a node it relied on may hold nothing more).
        self.generation = 0

    def _state(self, url: str) -> NodeState:
        return self.nodes.setdefault(url, NodeState())

    def ok(self, url: str, rtt: float) -> None:
        st = self._state(url)
        st.ok_streak += 1
        st.failures = 0
        st.next_try = 0.0
        st.rtt = rtt if not st.rtt else 0.7 * st.rtt + 0.3 * rtt
        st.last_error = ""

    def failed(self, url: str, error: str) -> None:
        st = self._state(url)
        st.ok_streak = 0
        st.failures += 1
        wait = min(BACKOFF_MAX, BACKOFF_MIN * 2 ** (st.failures - 1))
        st.next_try = self.clock() + wait * (1 + random.uniform(0, 0.25))
        st.last_error = error[:200]
        self.generation += 1

    def usable(self, url: str) -> bool:
        st = self.nodes.get(url)
        return st is None or self.clock() >= st.next_try

    def order(self, urls: list[str]) -> list[str]:
        """Usable nodes in their configured order, then nodes in backoff,
        soonest retry first (a last resort)."""
        good = [u for u in urls if self.usable(u)]
        bad = sorted((u for u in urls if not self.usable(u)), key=lambda u: self.nodes[u].next_try)
        return good + bad

    def status(self, url: str) -> dict:
        st = self.nodes.get(url)
        if st is None:
            return {"state": "unknown", "error": "", "rtt_ms": 0}
        state = "ok" if st.failures == 0 and st.ok_streak else ("backoff" if not self.usable(url) else "retry")
        return {"state": state, "error": st.last_error, "rtt_ms": round(st.rtt * 1000)}
