"""Generated passphrases: the Generate button next to new-passphrase fields
(first run, restore, linking, Change passphrase, Export backup).

Six words drawn with `secrets` from the EFF large wordlist (7776 words,
vendored in `_vendor/eff_wordlist/`, CC BY 3.0 US), separated by spaces:
6 × log2(7776) ≈ 77.5 bits, beyond guessing even offline against a stolen
vault or backup file, and still easy to read out and type when a password
manager isn't to hand. Words rather than random characters because people
copy them by hand from one device to another; the reasons are in
docs/user-guide.md "Generate a passphrase".

Generated here, never in the page: Python's `secrets` reads the operating
system's CSPRNG, and the page stays free of key material.
"""

from __future__ import annotations

import functools
import hashlib
import math
import secrets
from pathlib import Path

WORDLIST = Path(__file__).parent / "_vendor" / "eff_wordlist" / "eff_large_wordlist.txt"
WORDLIST_SHA256 = "addd35536511597a02fa0a9ff1e5284677b8883b83e986e43f15a3db996b903e"
WORDLIST_SIZE = 7776
WORDS = 6
SEPARATOR = " "  # four words have a hyphen (t-shirt), so not "-"


@functools.cache
def wordlist() -> tuple[str, ...]:
    """The vendored list's words, checked against its published digest."""
    data = WORDLIST.read_bytes()
    if hashlib.sha256(data).hexdigest() != WORDLIST_SHA256:
        raise RuntimeError(f"{WORDLIST} doesn't match the EFF wordlist's sha256")
    words = tuple(line.split("\t")[1] for line in data.decode("ascii").splitlines())
    if len(words) != WORDLIST_SIZE or len(set(words)) != WORDLIST_SIZE:
        raise RuntimeError(f"{WORDLIST} doesn't hold {WORDLIST_SIZE} distinct words")
    return words


def bits(words: int = WORDS) -> float:
    """The entropy of a generated passphrase of `words` words."""
    return words * math.log2(WORDLIST_SIZE)


def generate(words: int = WORDS) -> str:
    """A new random passphrase of `words` words."""
    if not 4 <= words <= 12:
        raise ValueError("a generated passphrase has 4 to 12 words")
    wl = wordlist()
    return SEPARATOR.join(secrets.choice(wl) for _ in range(words))
