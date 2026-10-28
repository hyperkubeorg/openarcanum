"""Messaging helpers: mention tokens, mention detection, unread counts
(openarcanum.messaging) and the messaging feature toggles (openarcanum.permissions)."""

import pytest

from openarcanum.core import settings_from_dict, settings_to_dict
from openarcanum.errors import UserError
from openarcanum.messaging import (
    channel_counts,
    encode_mentions,
    mentions_me,
)
from openarcanum.permissions import channel_features
from openarcanum.wire import community_pb2 as cp

ALICE = "A" * 43
BOB = "B" * 43
MEMBERS = [
    {"key": ALICE, "display_name": "Alice", "nickname": ""},
    {"key": BOB, "display_name": "Bob", "nickname": "Bobby Tables"},
]
ROLES = [{"id": 1, "name": "@everyone"}, {"id": 4, "name": "Mods"}]


def test_encode_mentions():
    assert encode_mentions("hi @Alice!", MEMBERS, ROLES) == f"hi <@{ALICE}>!"
    # Case-insensitive; nicknames with spaces; the longest name wins.
    assert encode_mentions("@bobby tables and @bob", MEMBERS, ROLES) == f"<@{BOB}> and <@{BOB}>"
    assert encode_mentions("ping @Mods", MEMBERS, ROLES) == "ping <@&4>"
    # Not inside a word, an e-mail address or code; @everyone/@here stay.
    assert encode_mentions("mail x@Alice.example", MEMBERS, ROLES) == "mail x@Alice.example"
    assert encode_mentions("`@Alice` and ```\n@Bob\n```", MEMBERS, ROLES) == "`@Alice` and ```\n@Bob\n```"
    assert encode_mentions("@everyone @here @Alicex", MEMBERS, ROLES) == "@everyone @here @Alicex"
    assert encode_mentions("no mentions", [], []) == "no mentions"
    # Tokens already written aren't touched.
    assert encode_mentions(f"<@{ALICE}>", MEMBERS, ROLES) == f"<@{ALICE}>"


def test_mentions_me():
    m = {"author": BOB, "mentions": [ALICE], "mention_roles": [], "mention_everyone": False, "mention_here": False}
    assert mentions_me(m, ALICE, set(), live=False)
    assert not mentions_me(m, BOB, set(), live=True)  # own message
    assert not mentions_me({**m, "deleted": True}, ALICE, set(), live=True)
    role = {**m, "mentions": [], "mention_roles": [4]}
    assert mentions_me(role, ALICE, {4}, live=False) and not mentions_me(role, ALICE, {5}, live=False)
    everyone = {**m, "mentions": [], "mention_everyone": True}
    assert mentions_me(everyone, ALICE, set(), live=False)
    # @here only reaches people online when it was sent.
    here = {**m, "mentions": [], "mention_here": True}
    assert mentions_me(here, ALICE, set(), live=True) and not mentions_me(here, ALICE, set(), live=False)
    channel = {**m, "mentions": [], "mention_channel": True}
    assert mentions_me(channel, ALICE, set(), live=False)
    # Suppressed in that community: @everyone, @here and @channel, or roles;
    # a mention by name always counts.
    for x in (everyone, here, channel):
        assert not mentions_me(x, ALICE, set(), live=True, suppress=frozenset({"everyone"}))
    assert not mentions_me(role, ALICE, {4}, live=False, suppress=frozenset({"roles"}))
    assert mentions_me(m, ALICE, set(), live=False, suppress=frozenset({"everyone", "roles"}))


def test_channel_counts():
    msgs = [
        {"id": 1, "author": BOB},
        {"id": 2, "author": BOB, "mentions_me": True},
        {"id": 3, "author": ALICE},
        {"id": 4, "author": BOB, "deleted": True},
        {"id": 5, "author": BOB, "mentions_me": True},
    ]
    assert channel_counts(msgs, 0, ALICE) == (3, 2)
    assert channel_counts(msgs, 2, ALICE) == (1, 1)
    assert channel_counts(msgs, 5, ALICE) == (0, 0)
    assert channel_counts([], 0, ALICE) == (0, 0)


def test_messaging_settings_round_trip():
    s = cp.CommunitySettings(name="C")
    d = settings_to_dict(s)
    f = d["features"]
    assert f["attachments"] and f["reactions"] and f["edit_own"] and f["delete_own"] and f["typing"]
    assert f["join_leave_messages"] and not f["link_previews"]
    assert d["attachment_kinds"] == "all" and d["join_leave_channel"] == 0
    changed = settings_from_dict(
        {
            "features": {"attachments": False, "link_previews": True, "typing": False},
            "attachment_kinds": "images_only",
            "join_leave_channel": 7,
        },
        s,
    )
    assert not changed.attachments_enabled and changed.link_previews_enabled and not changed.typing_enabled
    assert changed.attachment_kinds == cp.ATTACHMENT_KINDS_IMAGES_ONLY and changed.join_leave_channel == 7
    assert not changed.HasField("reactions_enabled") and not changed.HasField("edit_own_enabled")
    d = settings_to_dict(changed)
    assert d["attachment_kinds"] == "images_only" and d["features"]["reactions"] and d["join_leave_channel"] == 7
    for bad in (
        {"attachment_kinds": "videos"},
        {"features": {"nonsense": True}},
        {"features": {"typing": "on"}},
        {"join_leave_channel": "x"},
    ):
        with pytest.raises(UserError):
            settings_from_dict(bad, s)


def test_channel_features_precedence():
    on = cp.CommunitySettings(link_previews_enabled=True)
    assert channel_features(on, cp.Channel())["link_previews"]
    assert not channel_features(on, cp.Channel(link_previews=cp.CHANNEL_TOGGLE_OFF))["link_previews"]
    off = cp.CommunitySettings(reactions_enabled=False)
    # Community off wins over a channel that inherits.
    assert not channel_features(off, cp.Channel(reactions=cp.CHANNEL_TOGGLE_INHERIT))["reactions"]
    assert channel_features(cp.CommunitySettings(), cp.Channel())["attachments"]
    assert not channel_features(cp.CommunitySettings(), cp.Channel(attachments=cp.CHANNEL_TOGGLE_OFF))["attachments"]
