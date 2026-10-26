"""Moderation and custom emoji on the client (docs/protocol.md §4.7-§4.10).

Reports, the moderator queue, AutoMod rules, the filtered audit log and
custom emoji. The server enforces every rule here; the client turns UI
requests into protos and server replies into plain dicts for the UI.
Emoji images reach the UI as data: URIs, never URLs.
"""

from __future__ import annotations

import asyncio
import base64
import hashlib
import re
from datetime import UTC, datetime
from pathlib import Path
from typing import TYPE_CHECKING, Any

from openarcanum import PRODUCT_NAME, keys
from openarcanum.community import Offline, RpcError, sign_action
from openarcanum.errors import UserError
from openarcanum.i18n import _
from openarcanum.wire import community_pb2 as cp

if TYPE_CHECKING:
    from openarcanum.core import Core

MAX_REPORT_REASON = 500
MAX_EMOJI_BYTES = 256 * 1024
EMOJI_NAME = re.compile(r"^[a-z0-9_]{2,32}$")
# How a message or reaction refers to a custom emoji.
EMOJI_REF = re.compile(r":([a-z0-9_]{2,32}):([0-9]{1,18})")

TRIGGERS = {
    "keyword": cp.AUTOMOD_TRIGGER_KEYWORD,
    "mention_spam": cp.AUTOMOD_TRIGGER_MENTION_SPAM,
    "links": cp.AUTOMOD_TRIGGER_LINKS,
}
TRIGGER_NAMES = {v: k for k, v in TRIGGERS.items()}

# Ban "delete message history" choices (seconds), as the server accepts them.
BAN_DELETE_WINDOWS = (0, 3600, 21600, 86400, 259200, 604800)
# How long a ban's network block on a dedicated relay lasts (seconds; 0 =
# until the ban is lifted; docs/protocol.md §4.7 "Network blocks").
NETWORK_BLOCK_DURATIONS = (0, 86400, 604800, 2592000)
MIN_TIMEOUT, MAX_TIMEOUT = 60, 28 * 24 * 3600


def _enum(enum, value: int, prefix: str) -> str:
    try:
        return enum.Name(value).removeprefix(prefix).lower()
    except ValueError:
        return "unspecified"


def report_dict(r: cp.Report) -> dict:
    return {
        "id": r.id,
        "kind": _enum(cp.ReportKind, r.kind, "REPORT_KIND_"),
        "status": _enum(cp.ReportStatus, r.status, "REPORT_STATUS_"),
        "created_at": r.created_at,
        "reporter": keys.encode(r.reporter) if r.reporter else "",
        "reporter_name": r.reporter_name,
        "message_id": r.message_id,
        "channel_id": r.channel_id,
        "author": keys.encode(r.author) if r.author else "",
        "author_name": r.author_name,
        "reason": r.reason,
        "rule_id": r.rule_id,
        "rule_name": r.rule_name,
        "automod_action": r.automod_action,
        "resolved_by_name": r.resolved_by_name,
        "resolved_at": r.resolved_at,
    }


def rule_dict(r: cp.AutoModRule) -> dict:
    return {
        "id": r.id,
        "name": r.name,
        "enabled": r.enabled,
        "trigger": TRIGGER_NAMES.get(r.trigger, "keyword"),
        "keywords": list(r.keywords),
        "patterns": list(r.patterns),
        "mention_limit": r.mention_limit,
        "allowed_domains": list(r.allowed_domains),
        "block": r.block,
        "alert": r.alert,
        "timeout_seconds": r.timeout_seconds,
        "exempt_role_ids": list(r.exempt_role_ids),
        "exempt_channel_ids": list(r.exempt_channel_ids),
    }


def _lines(v: Any) -> list[str]:
    if isinstance(v, str):
        return [x.strip() for x in re.split(r"[\n,]", v) if x.strip()]
    return [str(x).strip() for x in (v or []) if str(x).strip()]


def _patterns(v: Any) -> list[str]:
    # One regular expression per line (commas are legal inside patterns).
    if isinstance(v, str):
        return [x for x in (line.strip() for line in v.splitlines()) if x]
    return [str(x) for x in (v or []) if str(x).strip()]


def rule_from_dict(d: dict) -> cp.AutoModRule:
    trigger = TRIGGERS.get(str(d.get("trigger", "")))
    if trigger is None:
        raise UserError(_("Choose what the rule looks for: keywords, mention spam or links."))
    try:
        timeout = int(d.get("timeout_seconds") or 0)
        mention_limit = int(d.get("mention_limit") or 0)
    except TypeError, ValueError:
        raise UserError(_("Timeouts and mention limits are numbers.")) from None
    if timeout and not MIN_TIMEOUT <= timeout <= MAX_TIMEOUT:
        raise UserError(_("A timeout lasts from 60 seconds to 28 days."))
    return cp.AutoModRule(
        id=int(d.get("id") or 0),
        name=str(d.get("name", "")).strip(),
        enabled=bool(d.get("enabled", True)),
        trigger=trigger,
        keywords=_lines(d.get("keywords")),
        patterns=_patterns(d.get("patterns")),
        mention_limit=mention_limit,
        allowed_domains=_lines(d.get("allowed_domains")),
        block=bool(d.get("block")),
        alert=bool(d.get("alert")),
        timeout_seconds=timeout,
        exempt_role_ids=[int(x) for x in d.get("exempt_role_ids") or []],
        exempt_channel_ids=[int(x) for x in d.get("exempt_channel_ids") or []],
    )


def _day(v: Any, end: bool) -> int:
    """A YYYY-MM-DD date (UTC) as unix ms: its start, or the next day's start."""
    s = str(v or "").strip()
    if not s:
        return 0
    try:
        d = datetime.strptime(s, "%Y-%m-%d").replace(tzinfo=UTC)
    except ValueError:
        raise UserError(_("Dates look like 2026-10-24.")) from None
    ms = int(d.timestamp() * 1000)
    return ms + 86_400_000 if end else ms


def audit_query(params: dict) -> cp.AuditQuery:
    q = cp.AuditQuery(before_id=int(params.get("before_id") or 0), limit=int(params.get("limit") or 100))
    if params.get("action"):
        q.action = str(params["action"])
    for f in ("actor", "target"):
        if params.get(f):
            try:
                setattr(q, f, keys.decode(str(params[f])))
            except ValueError:
                raise UserError(_("That isn't a member key.")) from None
    q.since = _day(params.get("since"), end=False)
    q.until = _day(params.get("until"), end=True)
    if q.since and q.until and q.until <= q.since:
        raise UserError(_("The date range ends before it starts."))
    return q


def audit_entry_dict(e: cp.AuditEntry) -> dict:
    ok = (not e.system) and keys.is_valid(e.actor, keys.CTX_ADMIN_ACTION, e.action.action, e.action.signature)
    return {
        "id": e.id,
        "at": e.at,
        "actor": keys.encode(e.actor) if e.actor else "",
        "actor_name": e.actor_name,
        "summary": e.summary,
        "action": e.action_name,
        "target": keys.encode(e.target) if e.target else "",
        "target_name": e.target_name,
        "system": e.system,
        "signature_ok": ok,
    }


def emoji_dict(e: cp.CustomEmoji) -> dict:
    return {"id": e.id, "name": e.name, "animated": e.animated, "mime": e.mime, "size": e.size, "version": e.version}


class Moderation:
    def __init__(self, core: Core):
        self.core = core
        # (community, emoji id, version) -> data: URI
        self._emoji: dict[tuple[str, int, int], str] = {}

    async def _call(self, cid: str, **body) -> cp.ServerMessage:
        try:
            return await self.core.conn(cid).call(**body)
        except (RpcError, Offline) as e:
            raise UserError(str(e)) from None

    # ---------------------------------------------------------------- reports

    async def report_message(self, cid: str, message_id: int, reason: str = "") -> dict:
        reason = (reason or "").strip()
        if len(reason) > MAX_REPORT_REASON:
            raise UserError(_("A reason can be at most {max} characters.").format(max=MAX_REPORT_REASON))
        await self._call(cid, report_message=cp.ReportMessage(message_id=int(message_id), reason=reason))
        return {"ok": True}

    async def reports(self, cid: str, include_closed: bool = False) -> list[dict]:
        r = await self._call(cid, list_reports=cp.ListReports(include_closed=bool(include_closed), limit=100))
        return [report_dict(x) for x in r.reports.reports]

    def mod_alert(self, cid: str, alert: cp.ModAlert) -> None:
        """A new report or AutoMod alert, pushed to moderators. The page
        badges the way to it (the server's ModQueue counts); the desktop
        notification follows the notification settings for the community."""
        rep = report_dict(alert.report)
        self.core.emit("mod_alert", {"community": cid, "report": rep})
        from openarcanum.core import community_notifies

        if not community_notifies(self.core.settings, cid) or self.core.statuses.dnd():
            # Muted, or Do not disturb: the moderator badge still counts.
            return
        rec = self.core.vault.data["communities"].get(cid, {}) if self.core.vault else {}
        if rep["kind"] == "automod":
            title = _("AutoMod alert")
            body = _("Rule “{rule}”: {author}, {action}").format(
                rule=rep["rule_name"], author=rep["author_name"], action=rep["automod_action"]
            )
        else:
            title = _("New report")
            body = _("{reporter} reported a message by {author}").format(
                reporter=rep["reporter_name"], author=rep["author_name"]
            )
        place = rec.get("name", "")
        if not self.core.unlocked:
            # While locked, not even which community.
            title, body, place = PRODUCT_NAME, _("New activity"), ""
        self.core.emit(
            "notify",
            {
                "title": title,
                "community_name": place,
                "body": body,
                "community": cid,
                "channel": rep["channel_id"],
                "report": rep["id"],
            },
        )

    # ---------------------------------------------------------------- AutoMod and audit

    async def automod_rules(self, cid: str) -> list[dict]:
        r = await self._call(cid, list_automod_rules=cp.ListAutoModRules())
        return [rule_dict(x) for x in r.automod_rules.rules]

    async def audit(self, cid: str, params: dict) -> dict:
        r = await self._call(cid, audit_log=audit_query(params))
        return {
            "entries": [audit_entry_dict(e) for e in r.audit.entries],
            "more": r.audit.more,
            "next_before_id": r.audit.next_before_id,
        }

    # ---------------------------------------------------------------- custom emoji

    async def emoji_upload(self, cid: str, name: str, path: str) -> dict:
        name = (name or "").strip().strip(":")
        if not EMOJI_NAME.match(name):
            raise UserError(_("Emoji names are 2 to 32 lowercase letters, digits or underscores."))
        try:
            data = await asyncio.to_thread(Path(path).expanduser().read_bytes)
        except OSError as e:
            raise UserError(_("Can't read that image: {reason}").format(reason=e.strerror)) from None
        if len(data) > MAX_EMOJI_BYTES:
            raise UserError(_("Emoji images can be at most 256 KB."))
        conn = self.core.conn(cid)
        identity = self.core.identity_key(conn.rec["identity"])
        op = cp.AddEmoji(name=name, sha256=hashlib.sha256(data).digest(), size=len(data))
        signed = sign_action(identity, keys.decode(conn.rec["owner_key"]), "add_emoji", op)
        r = await self._call(cid, emoji_upload=cp.EmojiUpload(action=signed, image=data))
        await conn.reload_state()
        return {"ok": True, "id": r.admin_result.id}

    def emoji_list(self, cid: str) -> list[dict]:
        st = self.core.conn(cid).state
        return [emoji_dict(e) for e in st.emoji] if st else []

    async def emoji_image(self, cid: str, emoji_id: int) -> str:
        st = self.core.conn(cid).state
        known = next((e for e in (st.emoji if st else []) if e.id == int(emoji_id)), None)
        if known is None:
            return ""
        key = (cid, known.id, known.version)
        if key not in self._emoji:
            r = await self._call(cid, get_emoji=cp.GetEmoji(id=known.id))
            img = r.emoji_image
            if img.mime not in ("image/png", "image/gif", "image/webp"):
                return ""
            self._emoji[key] = f"data:{img.mime};base64," + base64.b64encode(img.image).decode()
        return self._emoji[key]
