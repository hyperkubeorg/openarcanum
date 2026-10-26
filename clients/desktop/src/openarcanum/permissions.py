"""Permission bits and feature toggles, mirrored from internal/community.

perms.go holds the bits and features.go the toggles; tests/test_permissions.py
checks this module against both files. The server enforces everything; the
client uses these only to hide what the member can't do.
"""

from __future__ import annotations

from openarcanum.i18n import N_, _

# Bits (Go perms.go).
VIEW_CHANNEL = 1 << 0
MANAGE_CHANNELS = 1 << 1
MANAGE_ROLES = 1 << 2
MANAGE_COMMUNITY = 1 << 3
VIEW_AUDIT_LOG = 1 << 4
CREATE_INVITE = 1 << 5
CHANGE_NICKNAME = 1 << 6
MANAGE_NICKNAMES = 1 << 7
APPROVE_MEMBERS = 1 << 8
KICK = 1 << 12
BAN = 1 << 13
TIMEOUT_MEMBERS = 1 << 14
SEND_MESSAGES = 1 << 20
EMBED_LINKS = 1 << 21
ATTACH_FILES = 1 << 22
ADD_REACTIONS = 1 << 23
USE_CUSTOM_EMOJI = 1 << 24
MENTION_EVERYONE = 1 << 25
MANAGE_MESSAGES = 1 << 26
READ_MESSAGE_HISTORY = 1 << 27
MANAGE_EMOJI = 1 << 28
CONNECT_VOICE = 1 << 32
SPEAK = 1 << 33
VIDEO = 1 << 34
SCREEN_SHARE = 1 << 35
USE_VOICE_ACTIVITY = 1 << 36
PRIORITY_SPEAKER = 1 << 37
MUTE_MEMBERS = 1 << 38
DEAFEN_MEMBERS = 1 << 39
MOVE_MEMBERS = 1 << 40
ADMINISTRATOR = 1 << 48

# (name, label, bit, group). Names are the Go PermissionNames; labels and groups
# are shown in the UI, translated in catalog().
CATALOG: list[tuple[str, str, int, str]] = [
    ("view_channel", N_("View channels"), VIEW_CHANNEL, N_("General")),
    ("manage_channels", N_("Manage channels"), MANAGE_CHANNELS, N_("General")),
    ("manage_roles", N_("Manage roles"), MANAGE_ROLES, N_("General")),
    ("manage_community", N_("Manage community"), MANAGE_COMMUNITY, N_("General")),
    ("view_audit_log", N_("View audit log"), VIEW_AUDIT_LOG, N_("General")),
    ("create_invite", N_("Create invite"), CREATE_INVITE, N_("General")),
    ("change_nickname", N_("Change nickname"), CHANGE_NICKNAME, N_("General")),
    ("manage_nicknames", N_("Manage nicknames"), MANAGE_NICKNAMES, N_("General")),
    ("approve_members", N_("Approve join requests"), APPROVE_MEMBERS, N_("General")),
    ("kick", N_("Kick members"), KICK, N_("Moderation")),
    ("ban", N_("Ban members"), BAN, N_("Moderation")),
    ("timeout_members", N_("Time out members"), TIMEOUT_MEMBERS, N_("Moderation")),
    ("send_messages", N_("Send messages"), SEND_MESSAGES, N_("Text")),
    ("embed_links", N_("Embed links"), EMBED_LINKS, N_("Text")),
    ("attach_files", N_("Attach files"), ATTACH_FILES, N_("Text")),
    ("add_reactions", N_("Add reactions"), ADD_REACTIONS, N_("Text")),
    ("use_custom_emoji", N_("Use custom emoji"), USE_CUSTOM_EMOJI, N_("Text")),
    ("mention_everyone", N_("Mention @everyone, @here and all roles"), MENTION_EVERYONE, N_("Text")),
    ("manage_messages", N_("Manage messages"), MANAGE_MESSAGES, N_("Text")),
    ("read_message_history", N_("Read message history"), READ_MESSAGE_HISTORY, N_("Text")),
    ("manage_emoji", N_("Manage emoji"), MANAGE_EMOJI, N_("Text")),
    ("connect_voice", N_("Connect"), CONNECT_VOICE, N_("Voice and video")),
    ("speak", N_("Speak"), SPEAK, N_("Voice and video")),
    ("video", N_("Video"), VIDEO, N_("Voice and video")),
    ("screen_share", N_("Screen share"), SCREEN_SHARE, N_("Voice and video")),
    ("use_voice_activity", N_("Use voice activity"), USE_VOICE_ACTIVITY, N_("Voice and video")),
    ("priority_speaker", N_("Priority speaker"), PRIORITY_SPEAKER, N_("Voice and video")),
    ("mute_members", N_("Mute members"), MUTE_MEMBERS, N_("Voice and video")),
    ("deafen_members", N_("Deafen members"), DEAFEN_MEMBERS, N_("Voice and video")),
    ("move_members", N_("Move members"), MOVE_MEMBERS, N_("Voice and video")),
    ("administrator", N_("Administrator (everything, bypasses channel overrides)"), ADMINISTRATOR, N_("Administrator")),
]

PERMISSIONS: dict[str, int] = {name: bit for name, _label, bit, _group in CATALOG}
ALL = sum(PERMISSIONS.values())

# The bits a per-channel override can change (Go PermChannelMask).
CHANNEL_PERMISSIONS = [
    "view_channel",
    "send_messages",
    "embed_links",
    "attach_files",
    "add_reactions",
    "use_custom_emoji",
    "mention_everyone",
    "manage_messages",
    "read_message_history",
    "connect_voice",
    "speak",
    "video",
    "screen_share",
    "use_voice_activity",
    "priority_speaker",
    "mute_members",
    "deafen_members",
    "move_members",
]
CHANNEL_MASK = sum(PERMISSIONS[n] for n in CHANNEL_PERMISSIONS)

# The privacy-preserving @everyone defaults (Go DefaultEveryonePerms;
# docs/admin-guide.md "Permission defaults").
DEFAULT_EVERYONE = sum(
    PERMISSIONS[n]
    for n in (
        "view_channel",
        "send_messages",
        "read_message_history",
        "add_reactions",
        "attach_files",
        "embed_links",
        "use_custom_emoji",
        "change_nickname",
        "connect_voice",
        "speak",
        "use_voice_activity",
        "video",
    )
)


def has(perms: int, bit: int) -> bool:
    return perms & bit == bit or perms & ADMINISTRATOR == ADMINISTRATOR


def names(perms: int) -> list[str]:
    """The permission names set in a mask (for the UI, which can't do 64-bit math)."""
    return [n for n, bit in PERMISSIONS.items() if perms & bit]


def from_names(items) -> int:
    """A mask from permission names; unknown names are an error."""
    out = 0
    for n in items:
        if n not in PERMISSIONS:
            raise ValueError(f"unknown permission {n!r}")
        out |= PERMISSIONS[n]
    return out


def catalog() -> list[dict]:
    return [
        {"name": n, "label": _(label), "group": _(g), "channel": n in CHANNEL_PERMISSIONS}
        for n, label, _b, g in CATALOG
    ]


# ---------------------------------------------------------------- feature toggles

# (name, settings field, default, channel field or None). Names are the Go
# FeatureName values; precedence is community toggle, then channel setting,
# then permissions (docs/protocol.md §4.6).
FEATURES: list[tuple[str, str, bool, str | None]] = [
    ("attachments", "attachments_enabled", True, "attachments"),
    ("link_previews", "link_previews_enabled", False, "link_previews"),
    ("reactions", "reactions_enabled", True, "reactions"),
    ("custom_emoji", "custom_emoji_enabled", True, None),
    ("voice", "voice_enabled", True, None),
    ("video", "video_enabled", True, None),
    ("screen_share", "screen_share_enabled", True, None),
    ("search", "search_enabled", True, None),
    ("contact_cards", "allow_contact_cards", True, None),
    ("edit_own", "edit_own_enabled", True, None),
    ("delete_own", "delete_own_enabled", True, None),
    ("typing", "typing_enabled", True, None),
    ("history_for_new_members", "new_members_see_history", True, "history_for_new_members"),
    ("join_leave_messages", "join_leave_messages", True, None),
]
CHANNEL_TOGGLE_OFF = 1


def feature_on(settings, name: str) -> bool:
    """The community-level toggle (absent = its default)."""
    for n, field, default, _ch in FEATURES:
        if n == name:
            if settings is None:
                return default
            desc = settings.DESCRIPTOR.fields_by_name[field]
            if desc.has_presence and not settings.HasField(field):
                return default
            return bool(getattr(settings, field))
    raise KeyError(name)


def channel_feature_on(settings, channel, name: str) -> bool:
    """The community toggle, then the channel's inherit/off setting."""
    if not feature_on(settings, name):
        return False
    for n, _field, _default, ch_field in FEATURES:
        if n == name and ch_field and channel is not None:
            return getattr(channel, ch_field) != CHANNEL_TOGGLE_OFF
    return True


def features(settings) -> dict[str, bool]:
    return {n: feature_on(settings, n) for n, *_rest in FEATURES}


def channel_features(settings, channel) -> dict[str, bool]:
    return {n: channel_feature_on(settings, channel, n) for n, *_rest in FEATURES}
