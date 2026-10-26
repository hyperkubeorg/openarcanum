"""Protocol version negotiation (docs/protocol.md §1 "Versions").

Mirrors internal/version: every side announces the versions it speaks, the
highest one both speak is used, and a build speaks its current version and
the previous one. With nothing in common, the side that notices says which
peer is out of date, so the UI can say "update OpenArcanum" or "this server is
outdated".
"""

from __future__ import annotations

from openarcanum.i18n import N_, _

CURRENT = 1
HEADER = "OpenArcanum-Protocol"
MAX_LISTED = 16


def supported_for(current: int) -> list[int]:
    """The policy: the current version and the previous one (none before 1)."""
    if current <= 1:
        return [1]
    return [current - 1, current]


SUPPORTED = supported_for(CURRENT)


# What can be out of date (the `what` of a mismatch), for the catalog.
PEERS = (N_("server"), N_("maintainer node"))


class VersionMismatch(Exception):
    """No protocol version in common. `peer_outdated` says which side must update."""

    def __init__(self, ours: list[int], theirs: list[int], peer_outdated: bool, what: str = "server"):
        self.ours, self.theirs, self.peer_outdated, self.what = list(ours), list(theirs), peer_outdated, what
        super().__init__(self.user_message())

    def user_message(self) -> str:
        what = _(self.what)
        if self.peer_outdated:
            return _(
                "This {what} is out of date (it supports {theirs}; this version of OpenArcanum needs {ours}). "
                "Whoever runs it needs to update it."
            ).format(what=what, theirs=fmt(self.theirs) or _("none"), ours=fmt(self.ours))
        return _(
            "Update OpenArcanum: this {what} needs version {theirs}, and this version of OpenArcanum supports {ours}."
        ).format(what=what, theirs=fmt(self.theirs), ours=fmt(self.ours))


def negotiate(ours: list[int], theirs: list[int], what: str = "server") -> int:
    """The highest version in both lists, or VersionMismatch."""
    clean = [v for v in theirs if v] if len(theirs) <= MAX_LISTED else []
    common = [v for v in clean if v in ours]
    if common:
        return max(common)
    raise VersionMismatch(ours, clean, peer_outdated=not clean or max(clean) < min(ours), what=what)


def fmt(vs: list[int]) -> str:
    return ",".join(str(v) for v in vs)


def parse(s: str) -> list[int]:
    """Reads fmt's output; anything malformed yields an empty list."""
    out: list[int] = []
    for part in s.split(","):
        part = part.strip()
        if not part:
            continue
        if not part.isdigit() or int(part) >= 1 << 32 or len(out) == MAX_LISTED:
            return []
        out.append(int(part))
    return out
