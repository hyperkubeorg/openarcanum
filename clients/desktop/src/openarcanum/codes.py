"""Strings people paste: invites, claim codes and move codes
(docs/protocol.md §5). Each is a prefix plus base64url of a protobuf."""

from __future__ import annotations

import re

from openarcanum import keys
from openarcanum.i18n import _
from openarcanum.wire import invite_pb2 as ip

INVITE_PREFIX = "openarcanum-invite:"
CLAIM_PREFIX = "openarcanum-claim:"
MOVE_PREFIX = "openarcanum-move:"
LINK_SCHEME = "openarcanum://invite/"
# The longest invite link the app accepts from the OS (a launch argument, a
# hand-off from a second launch, or a macOS URL-open event). Real invites are
# well under 2 KiB.
MAX_LINK = 16 * 1024
_LINK_PAYLOAD = re.compile(r"[A-Za-z0-9_-]+={0,2}")


class CodeError(ValueError):
    pass


def _decode(text: str, prefix: str, msg):
    text = re.sub(r"\s+", "", text or "")
    if text.startswith(LINK_SCHEME) and prefix == INVITE_PREFIX:
        text = INVITE_PREFIX + text[len(LINK_SCHEME) :]
    if not text.startswith(prefix):
        raise CodeError(_("That doesn't look like a {kind} code.").format(kind=prefix.split("-")[1].rstrip(":")))
    try:
        msg.ParseFromString(keys.decode(text[len(prefix) :]))
    except Exception as e:
        raise CodeError(_("The code is damaged or incomplete.")) from e
    return msg


def parse_invite(text: str) -> ip.Invite:
    inv = _decode(text, INVITE_PREFIX, ip.Invite())
    if len(inv.server_key) != 32 or len(inv.owner_key) != 32 or not inv.relays:
        raise CodeError(_("The invite is incomplete."))
    return inv


def encode_invite(inv: ip.Invite) -> str:
    return INVITE_PREFIX + keys.encode(inv.SerializeToString())


def invite_link(inv: ip.Invite) -> str:
    return LINK_SCHEME + keys.encode(inv.SerializeToString())


def link_from_os(text: str) -> str | None:
    """The invite link in what the OS handed the app when someone opened a
    `openarcanum://invite/...` link, in canonical form, or None when it isn't
    one. The scheme and host are case-insensitive (browsers and OSes may
    change their case) and a trailing slash is dropped. The result only ever
    prefills the Join dialog: joining still takes an explicit confirmation."""
    t = (text or "").strip()
    if len(t) > MAX_LINK:
        return None
    scheme, sep, rest = t.partition("://")
    host, _sep, payload = rest.partition("/")
    if not sep or scheme.lower() != "openarcanum" or host.lower() != "invite":
        return None
    payload = payload.rstrip("/")
    if not _LINK_PAYLOAD.fullmatch(payload):
        return None
    return LINK_SCHEME + payload


def parse_claim(text: str) -> ip.ClaimCode:
    c = _decode(text, CLAIM_PREFIX, ip.ClaimCode())
    if len(c.server_key) != 32 or not c.secret or not c.relays:
        raise CodeError(_("The claim code is incomplete."))
    return c


def parse_move(text: str) -> ip.MoveCode:
    c = _decode(text, MOVE_PREFIX, ip.MoveCode())
    if len(c.server_key) != 32 or not c.secret or not c.relays or len(c.owner_key) != 32:
        raise CodeError(_("The move code is incomplete."))
    return c


def detect(text: str) -> str:
    t = re.sub(r"\s+", "", text or "")
    if t.startswith((INVITE_PREFIX, LINK_SCHEME)):
        return "invite"
    if t.startswith(CLAIM_PREFIX):
        return "claim"
    if t.startswith(MOVE_PREFIX):
        return "move"
    return "unknown"
