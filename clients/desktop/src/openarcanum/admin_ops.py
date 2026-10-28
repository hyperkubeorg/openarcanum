"""Turn UI requests into admin action protos (docs/protocol.md §4.4).

The server validates everything again; checks here only give faster, clearer
errors.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Any

from openarcanum import keys
from openarcanum.i18n import _
from openarcanum.moderation import (
    BAN_DELETE_WINDOWS,
    EMOJI_NAME,
    MAX_TIMEOUT,
    MIN_TIMEOUT,
    NETWORK_BLOCK_DURATIONS,
    rule_from_dict,
)
from openarcanum.permissions import ALL, CHANNEL_MASK, from_names
from openarcanum.wire import community_pb2 as cp

if TYPE_CHECKING:
    from openarcanum.community import CommunityConnection


class OpError(ValueError):
    pass


def _int(params: dict, key: str, lo: int = 0, hi: int = 1 << 62, default: int | None = None) -> int:
    v = params.get(key, default)
    if v is None or v == "":
        if default is not None:
            return default
        raise OpError(_("{setting} is required").format(setting=key.replace("_", " ")))
    try:
        n = int(v)
    except TypeError, ValueError:
        raise OpError(_("{setting} must be a number").format(setting=key.replace("_", " "))) from None
    if not lo <= n <= hi:
        raise OpError(_("{setting} must be between {lo} and {hi}").format(setting=key.replace("_", " "), lo=lo, hi=hi))
    return n


def _key(params: dict, key: str = "member") -> bytes:
    try:
        b = keys.decode(str(params[key]))
    except KeyError, ValueError:
        raise OpError(_("a member key is required")) from None
    if len(b) != 32:
        raise OpError(_("that isn't a member key"))
    return b


def _color(v: Any) -> int:
    if v in (None, "", 0):
        return 0
    s = str(v).lstrip("#")
    try:
        c = int(s, 16)
    except ValueError:
        raise OpError(_("colors look like #3366ff")) from None
    if c > 0xFFFFFF:
        raise OpError(_("colors look like #3366ff"))
    return c


def _perms(p: dict, key: str, mask: int) -> int:
    """A permission mask given as a list of names (what the UI sends) or a number."""
    v = p.get(key, 0)
    if isinstance(v, list | tuple):
        try:
            n = from_names(v)
        except ValueError as e:
            raise OpError(str(e)) from None
    else:
        n = _int(p, key, 0, ALL, default=0)
    if n & ~mask:
        raise OpError(_("those permissions can't be set here"))
    return n


def _toggle(v: Any) -> int:
    if v in ("inherit", None, ""):
        return cp.CHANNEL_TOGGLE_INHERIT
    if v == "off":
        return cp.CHANNEL_TOGGLE_OFF
    raise OpError(_("channel settings are inherit or off"))


def _ids(p: dict, key: str) -> list[int]:
    try:
        return [int(x) for x in p.get(key, [])]
    except TypeError, ValueError:
        raise OpError(_("{setting} must be a list of ids").format(setting=key.replace("_", " "))) from None


def _optional_days(v: Any):
    if v is None or v == "" or v == "default":
        return None
    n = int(v)
    if not 0 <= n <= 36500:
        raise OpError(_("retention must be between 0 (forever) and 36500 days"))
    return n


def build_op(op: str, p: dict, conn: CommunityConnection) -> tuple[str, Any]:
    from openarcanum.core import settings_from_dict

    if op == "update_settings":
        base = conn.state.settings if conn.state else None
        return "update_settings", settings_from_dict(p, base)
    if op == "create_invite":
        days = float(p.get("expires_days", 7) or 0)
        if days < 0 or days > 3650:
            raise OpError(_("expiry must be between 0 (never) and 3650 days"))
        return "create_join_key", cp.CreateJoinKey(
            label=str(p.get("label", ""))[:64],
            expires_in_seconds=int(days * 86400),
            max_uses=_int(p, "max_uses", 0, 1_000_000, default=0),
            role_ids=[int(r) for r in p.get("role_ids", [])],
            request_only=bool(p.get("request_only")),
            public=bool(p.get("public")),
        )
    if op == "revoke_invite":
        return "revoke_join_key", cp.RevokeJoinKey(id=_int(p, "id", 1))
    if op == "create_channel":
        kind = cp.CHANNEL_KIND_VOICE if p.get("kind") == "voice" else cp.CHANNEL_KIND_TEXT
        name = str(p.get("name", "")).strip()
        if not name:
            raise OpError(_("give the channel a name"))
        return "create_channel", cp.CreateChannel(
            name=name,
            kind=kind,
            category_id=_int(p, "category_id", 0, default=0),
            description=str(p.get("topic", "")),
        )
    if op == "edit_channel":
        ch = conn.channel(_int(p, "id", 1))
        if ch is None:
            raise OpError(_("no such channel"))
        e = cp.EditChannel(
            id=ch.id, name=str(p.get("name", ch.name)), position=_int(p, "position", -100000, 100000, ch.position)
        )
        if p.get("clear_retention"):
            e.clear_retention = True
        mr, fr = _optional_days(p.get("message_retention_days")), _optional_days(p.get("file_retention_days"))
        if mr is not None:
            e.message_retention_days = mr
        if fr is not None:
            e.file_retention_days = fr
        if "message_retention_days" in p and mr is None and "file_retention_days" in p and fr is None:
            e.clear_retention = True
        if "topic" in p:
            e.description = str(p["topic"] or "")
        if "category_id" in p:
            e.category_id = _int(p, "category_id", 0, default=0)
        if "slowmode" in p:
            e.slowmode_seconds = _int(p, "slowmode", 0, 21600, default=0)
        if p.get("sync_permissions"):
            e.sync_permissions = True
        for name in ("attachments", "link_previews", "reactions", "history_for_new_members"):
            if name in p:
                setattr(e, name, _toggle(p[name]))
        return "edit_channel", e
    if op == "delete_channel":
        return "delete_channel", cp.DeleteChannel(id=_int(p, "id", 1))
    if op == "reorder_channels":
        return "reorder_channels", cp.ReorderChannels(
            category_id=_int(p, "category_id", 0, default=0), channel_ids=_ids(p, "channel_ids")
        )
    if op == "create_category":
        name = str(p.get("name", "")).strip()
        if not name:
            raise OpError(_("give the category a name"))
        return "create_category", cp.CreateCategory(name=name)
    if op == "rename_category":
        return "edit_category", cp.EditCategory(id=_int(p, "id", 1), name=str(p.get("name", "")).strip())
    if op == "delete_category":
        return "delete_category", cp.DeleteCategory(id=_int(p, "id", 1))
    if op == "reorder_categories":
        return "reorder_categories", cp.ReorderCategories(category_ids=_ids(p, "category_ids"))
    if op == "create_role":
        return "create_role", cp.CreateRole(
            name=str(p.get("name", "")).strip(),
            permissions=_perms(p, "permissions", ALL),
            color=_color(p.get("color")),
            hoist=bool(p.get("hoist")),
            mentionable=bool(p.get("mentionable")),
        )
    if op == "edit_role":
        return "edit_role", cp.EditRole(
            id=_int(p, "id", 1),
            name=str(p.get("name", "")).strip(),
            permissions=_perms(p, "permissions", ALL),
            color=_color(p.get("color")),
            hoist=bool(p.get("hoist")),
            mentionable=bool(p.get("mentionable")),
        )
    if op == "delete_role":
        return "delete_role", cp.DeleteRole(id=_int(p, "id", 1))
    if op == "reorder_roles":
        return "reorder_roles", cp.ReorderRoles(role_ids=[int(r) for r in p.get("role_ids", [])])
    if op == "set_override":
        allow, deny = _perms(p, "allow", CHANNEL_MASK), _perms(p, "deny", CHANNEL_MASK)
        if allow & deny:
            raise OpError(_("a permission can't be both allowed and denied"))
        o = cp.SetChannelOverride(
            channel_id=_int(p, "channel_id", 0, default=0),
            category_id=_int(p, "category_id", 0, default=0),
            allow=allow,
            deny=deny,
        )
        if (o.channel_id == 0) == (o.category_id == 0):
            raise OpError(_("pick a channel or a category"))
        if p.get("member"):
            o.member = _key(p)
        else:
            o.role_id = _int(p, "role_id", 1)
        return "set_channel_override", o
    if op == "assign_roles":
        return "assign_roles", cp.AssignRoles(member=_key(p), role_ids=[int(r) for r in p.get("role_ids", [])])
    if op == "voice_mute":
        return "voice_mute", cp.VoiceMute(member=_key(p), muted=bool(p.get("muted", True)))
    if op == "voice_deafen":
        return "voice_deafen", cp.VoiceDeafen(member=_key(p), deafened=bool(p.get("deafened", True)))
    if op == "voice_move":
        return "voice_move", cp.VoiceMove(member=_key(p), channel_id=_int(p, "channel_id", 0, default=0))
    if op == "kick":
        return "kick", cp.Kick(member=_key(p), reason=str(p.get("reason", ""))[:512])
    if op == "ban":
        window = _int(p, "delete_message_seconds", 0, 604800, default=0)
        if window not in BAN_DELETE_WINDOWS:
            raise OpError(_("delete messages from the last hour, 6 hours, 24 hours, 3 days or 7 days"))
        block = bool(p.get("block_network"))
        block_secs = _int(p, "block_seconds", 0, 2592000, default=0) if block else 0
        if block_secs not in NETWORK_BLOCK_DURATIONS:
            raise OpError(_("a network block lasts 1 day, 7 days, 30 days or until the ban is lifted"))
        return "ban", cp.BanMember(
            member=_key(p),
            reason=str(p.get("reason", ""))[:512],
            delete_message_seconds=window,
            block_network=block,
            block_seconds=block_secs,
        )
    if op == "timeout":
        secs = _int(p, "seconds", 0, MAX_TIMEOUT)
        if secs and secs < MIN_TIMEOUT:
            raise OpError(_("a timeout lasts from 60 seconds to 28 days"))
        return "timeout", cp.TimeoutMember(member=_key(p), seconds=secs, reason=str(p.get("reason", ""))[:512])
    if op == "bulk_delete":
        ids = [int(x) for x in p.get("message_ids") or []]
        last = _int(p, "last", 0, 100, default=0)
        if len(ids) > 100:
            raise OpError(_("a bulk delete removes at most 100 messages"))
        if not ids and not last:
            raise OpError(_("choose messages to delete"))
        return "bulk_delete", cp.BulkDelete(channel_id=_int(p, "channel_id", 1), message_ids=ids, last=last)
    if op == "save_automod_rule":
        return "save_automod_rule", cp.SaveAutoModRule(rule=rule_from_dict(p))
    if op == "delete_automod_rule":
        return "delete_automod_rule", cp.DeleteAutoModRule(id=_int(p, "id", 1))
    if op == "resolve_report":
        return "resolve_report", cp.ResolveReport(id=_int(p, "id", 1), dismiss=bool(p.get("dismiss")))
    if op == "rename_emoji":
        name = str(p.get("name", "")).strip().strip(":")
        if not EMOJI_NAME.match(name):
            raise OpError(_("emoji names are 2 to 32 lowercase letters, digits or underscores"))
        return "rename_emoji", cp.RenameEmoji(id=_int(p, "id", 1), name=name)
    if op == "delete_emoji":
        return "delete_emoji", cp.DeleteEmoji(id=_int(p, "id", 1))
    if op == "unban":
        return "unban", cp.Unban(member=_key(p))
    if op == "approve":
        return "approve", cp.Approve(member=_key(p))
    if op == "deny":
        return "deny", cp.Deny(member=_key(p))
    if op == "raid_revoke":
        return "raid_revoke", cp.RaidRevoke(join_key_id=_int(p, "join_key_id", 1), hours=_int(p, "hours", 1, 720))
    if op == "replace_member":
        return "replace_member", cp.ReplaceMember(old_key=_key(p, "old_key"), new_key=_key(p, "new_key"))
    if op == "server_backup":
        return "server_backup", cp.ServerBackup()
    if op == "create_community":
        return "create_community", cp.CreateCommunity()
    if op == "set_nickname":
        nick = str(p.get("nickname", "")).strip()
        if len(nick) > 32:
            raise OpError(_("nicknames are at most 32 characters"))
        return "set_nickname", cp.SetNickname(member=_key(p), nickname=nick)
    raise OpError(_("unknown action {action}").format(action=repr(op)))
