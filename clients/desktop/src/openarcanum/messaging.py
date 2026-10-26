"""Community messaging helpers: mention tokens, who a message mentions, and
unread and mention counts (docs/protocol.md §4.3). The feature toggles are
openarcanum.permissions (feature_on, channel_feature_on).

Pure functions over plain dicts; core.py wires them to the connection,
cache and UI.
"""

from __future__ import annotations

import re

# Code spans and fenced blocks: mentions inside them don't count (the server
# ignores them too).
_CODE = re.compile(r"```.*?```|`[^`\n]*`", re.S)

MAX_ATTACHMENTS = 10


def encode_mentions(text: str, members: list[dict], roles: list[dict]) -> str:
    """Turn typed `@Name` mentions into the tokens the server reads:
    `<@member key>` for members (nickname or display name) and `<@&id>` for
    roles. The longest matching name wins; text in code stays as typed;
    @everyone and @here are already tokens."""
    names: dict[str, str] = {}
    for r in roles:
        if r.get("name") and r["name"] != "@everyone":
            names.setdefault(r["name"].lower(), f"<@&{r['id']}>")
    for m in members:
        for n in (m.get("nickname"), m.get("display_name")):
            if n:
                names.setdefault(n.lower(), f"<@{m['key']}>")
    names.pop("everyone", None)
    names.pop("here", None)
    if not names or "@" not in text:
        return text
    pattern = re.compile(
        r"(?<![\w<@])@(" + "|".join(re.escape(n) for n in sorted(names, key=len, reverse=True)) + r")(?!\w)",
        re.I,
    )

    def plain(part: str) -> str:
        return pattern.sub(lambda mt: names[mt.group(1).lower()], part)

    out, last = [], 0
    for code in _CODE.finditer(text):
        out.append(plain(text[last : code.start()]))
        out.append(code.group(0))
        last = code.end()
    out.append(plain(text[last:]))
    return "".join(out)


def mentions_me(m: dict, my_key: str, my_roles: set[int], live: bool) -> bool:
    """Whether a message mentions this member: by key, a role they hold,
    @everyone, or @here when it arrives live (here means "whoever is online")."""
    if m.get("deleted") or m.get("author") == my_key:
        return False
    if my_key and my_key in m.get("mentions", []):
        return True
    if m.get("mention_everyone") or (live and m.get("mention_here")):
        return True
    return any(r in my_roles for r in m.get("mention_roles", []))


def channel_counts(msgs: list[dict], last_read: int, my_key: str) -> tuple[int, int]:
    """Unread messages and unread mentions after last_read."""
    unread = mentions = 0
    for m in reversed(msgs):
        if m["id"] <= last_read:
            break
        if m.get("deleted") or m.get("author") == my_key:
            continue
        unread += 1
        if m.get("mentions_me"):
            mentions += 1
    return unread, mentions
