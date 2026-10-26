"""The Python permission and toggle tables match the Go ones (perms.go,
features.go), and the client-side precedence mirror behaves like allowed()."""

import re
from pathlib import Path

import pytest

from openarcanum import permissions as P
from openarcanum.wire import community_pb2 as cp

GO = Path(__file__).resolve().parents[3] / "internal" / "community"


def go_bits() -> dict[str, int]:
    src = (GO / "perms.go").read_text()
    return {name: 1 << int(n) for name, n in re.findall(r"^\s+(Perm\w+)\s+uint64 = 1 << (\d+)", src, re.M)}


def go_mask(name: str) -> int:
    src = (GO / "perms.go").read_text()
    body = re.search(rf"{name} = (.*?)\n\n", src, re.S).group(1)
    bits = go_bits()
    return sum(bits[b] for b in re.findall(r"Perm\w+", body))


def test_permission_names_and_bits_match_go():
    src = (GO / "perms.go").read_text()
    bits = go_bits()
    pairs = re.findall(r'\{"(\w+)", (Perm\w+)\}', src)
    assert len(pairs) == len(P.CATALOG) == 31
    assert [n for n, _ in pairs] == [n for n, *_ in P.CATALOG], "same names in the same order"
    for name, const in pairs:
        assert P.PERMISSIONS[name] == bits[const], name
        assert getattr(P, name.upper()) == bits[const], name


def test_masks_match_go():
    assert go_mask("PermChannelMask") == P.CHANNEL_MASK
    assert go_mask("DefaultEveryonePerms") == P.DEFAULT_EVERYONE
    assert all(b < 1 << 53 for b in P.PERMISSIONS.values()), "exact in JavaScript numbers"


def test_privacy_preserving_defaults():
    on = set(P.names(P.DEFAULT_EVERYONE))
    off_by_default = ("create_invite", "mention_everyone", "manage_channels", "manage_roles", "manage_community")
    for off in (*off_by_default, "administrator"):
        assert off not in on
    for want in ("view_channel", "send_messages", "read_message_history", "connect_voice", "speak"):
        assert want in on


def test_names_round_trip():
    mask = P.SEND_MESSAGES | P.ADMINISTRATOR | P.MOVE_MEMBERS
    assert P.from_names(P.names(mask)) == mask
    with pytest.raises(ValueError):
        P.from_names(["fly"])
    assert P.has(P.ADMINISTRATOR, P.KICK) and not P.has(P.SEND_MESSAGES, P.KICK)


def test_features_match_go():
    src = (GO / "features.go").read_text()
    go = re.findall(
        r'Feature\w+: \{"(\w+)", "[^"]*", (true|false),\s*\n'
        r"\s*func\(s \*wire.CommunitySettings\) \*bool \{ return &?s\.(\w+) \}",
        src,
    )
    assert [(n, d == "true") for n, d, _f in go] == [(n, d) for n, _f, d, _c in P.FEATURES]
    camel = lambda s: "".join(w.capitalize() for w in s.split("_"))  # noqa: E731
    assert [f for _n, _d, f in go] == [camel(f) for _n, f, _d, _c in P.FEATURES]
    with_channel = re.findall(r'Feature\w+: \{"(\w+)",[^\n]*\n[^\n]*\n\s*func\(c \*channelRec\)', src)
    assert with_channel == [n for n, _f, _d, c in P.FEATURES if c]


def test_feature_precedence_mirror():
    s = cp.CommunitySettings()
    # Absent = default.
    assert P.feature_on(s, "voice") and not P.feature_on(s, "link_previews")
    s.reactions_enabled = False
    ch = cp.Channel()
    assert not P.channel_feature_on(s, ch, "reactions"), "community off wins"
    s.reactions_enabled = True
    assert P.channel_feature_on(s, ch, "reactions")
    ch.reactions = cp.CHANNEL_TOGGLE_OFF
    assert not P.channel_feature_on(s, ch, "reactions"), "channel off"
    assert P.channel_feature_on(s, ch, "attachments"), "other toggles unaffected"
    s.search_enabled = False
    assert P.features(s)["search"] is False
