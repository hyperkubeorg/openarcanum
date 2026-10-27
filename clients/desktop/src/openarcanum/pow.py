"""The relay's proof-of-work for new connections under load (docs/protocol.md
§2 "Starting"; internal/pow).

Hashcash (A. Back, "Hashcash - A Denial of Service Counter-Measure", 2002)
with SHA-256: find an 8-byte big-endian counter such that
SHA-256("openarcanum/relay-pow/v1" || 0x00 || challenge || counter) starts with
`bits` zero bits. The challenge is the relay's per-connection Hello nonce.
Solving is CPU work, so callers run `solve` in a worker thread, never on the
UI thread or the event loop.
"""

from __future__ import annotations

import hashlib
import threading
import time

CONTEXT = b"openarcanum/relay-pow/v1"
MAX_BITS = 24


class PowError(Exception):
    pass


def digest(challenge: bytes, counter: bytes) -> bytes:
    return hashlib.sha256(CONTEXT + b"\x00" + challenge + counter).digest()


def leading_zero_bits(h: bytes) -> int:
    n = 0
    for b in h:
        if b:
            return n + 8 - b.bit_length()
        n += 8
    return n


def verify(challenge: bytes, counter: bytes, bits: int) -> bool:
    if bits == 0:
        return True
    if bits > MAX_BITS or not 0 < len(counter) <= 16:
        return False
    return leading_zero_bits(digest(challenge, counter)) >= bits


def solve(challenge: bytes, bits: int, deadline: float | None = None, cancel: threading.Event | None = None) -> bytes:
    """Search for a counter. Raises PowError past `deadline` (time.monotonic())
    or when `cancel` is set, and for a difficulty above MAX_BITS."""
    if bits > MAX_BITS:
        raise PowError(f"the relay asks for a {bits}-bit proof-of-work (at most {MAX_BITS})")
    if bits == 0:
        return bytes(8)
    prefix = hashlib.sha256(CONTEXT + b"\x00" + challenge)
    full, rest = divmod(bits, 8)
    mask = 0xFF << (8 - rest) & 0xFF
    i = 0
    while True:
        if i & 0xFFF == 0:
            if (deadline is not None and time.monotonic() > deadline) or (cancel is not None and cancel.is_set()):
                raise PowError("proof-of-work not finished in time")
            # The loop holds the interpreter lock between hashes, and merely
            # offering it (sleep(0)) isn't enough on every system: a short
            # real sleep lets the event loop behind the UI run. It adds about
            # a quarter to the solve time.
            time.sleep(0.001)
        counter = i.to_bytes(8, "big")
        h = prefix.copy()
        h.update(counter)
        d = h.digest()
        if not any(d[:full]) and (rest == 0 or not d[full] & mask):
            return counter
        i += 1
