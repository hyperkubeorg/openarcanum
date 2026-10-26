"""OlmStore: vodozemac sessions, persistence, rotation and replay handling."""

import os

import pytest

from openarcanum import olm
from openarcanum.olm import OlmError, OlmStore


def store(tmp_path, name):
    return OlmStore(tmp_path / f"{name}.bin", os.urandom(32), os.urandom(32))


def reopen(s: OlmStore) -> OlmStore:
    return OlmStore(s.path, s.seal_key, s.pickle_key)


def start_session(a: OlmStore, b: OlmStore, plaintext=b"hi"):
    """a -> b using one of b's one-time keys."""
    (_kid, otk), *_ = b.new_one_time_keys("dm:b", 1)
    mtype, ct = a.encrypt_olm("dm:a", b.curve("dm:b"), plaintext, one_time_key=otk)
    return mtype, ct


def test_olm_round_trip_and_reply(tmp_path):
    a, b = store(tmp_path, "a"), store(tmp_path, "b")
    mtype, ct = start_session(a, b)
    assert mtype == 0  # pre-key
    assert b.decrypt_olm("dm:b", a.curve("dm:a"), mtype, ct) == b"hi"
    rtype, rct = b.encrypt_olm("dm:b", a.curve("dm:a"), b"back")
    assert a.decrypt_olm("dm:a", b.curve("dm:b"), rtype, rct) == b"back"
    # After a reply, a's messages are normal messages.
    mtype2, ct2 = a.encrypt_olm("dm:a", b.curve("dm:b"), b"again")
    assert mtype2 == 1
    assert b.decrypt_olm("dm:b", a.curve("dm:a"), mtype2, ct2) == b"again"


def test_olm_state_survives_restart(tmp_path):
    a, b = store(tmp_path, "a"), store(tmp_path, "b")
    mtype, ct = start_session(a, b)
    b.decrypt_olm("dm:b", a.curve("dm:a"), mtype, ct)
    a2, b2 = reopen(a), reopen(b)
    assert a2.curve("dm:a") == a.curve("dm:a")
    t, c = b2.encrypt_olm("dm:b", a2.curve("dm:a"), b"after restart")
    assert a2.decrypt_olm("dm:a", b2.curve("dm:b"), t, c) == b"after restart"


def test_olm_no_session_and_no_key_refused(tmp_path):
    a, b = store(tmp_path, "a"), store(tmp_path, "b")
    with pytest.raises(OlmError):
        a.encrypt_olm("dm:a", b.curve("dm:b"), b"x")


def test_olm_one_time_key_is_single_use(tmp_path):
    a, b, c = store(tmp_path, "a"), store(tmp_path, "b"), store(tmp_path, "c")
    (_kid, otk), *_ = b.new_one_time_keys("dm:b", 1)
    t1, ct1 = a.encrypt_olm("dm:a", b.curve("dm:b"), b"one", one_time_key=otk)
    t2, ct2 = c.encrypt_olm("dm:c", b.curve("dm:b"), b"two", one_time_key=otk)
    assert b.decrypt_olm("dm:b", a.curve("dm:a"), t1, ct1) == b"one"
    with pytest.raises(OlmError):
        b.decrypt_olm("dm:b", c.curve("dm:c"), t2, ct2)


def test_olm_tampered_and_misattributed_refused(tmp_path):
    a, b, c = store(tmp_path, "a"), store(tmp_path, "b"), store(tmp_path, "c")
    mtype, ct = start_session(a, b)
    bad = bytearray(ct)
    bad[-5] ^= 1
    with pytest.raises(OlmError):
        b.decrypt_olm("dm:b", a.curve("dm:a"), mtype, bytes(bad))
    # The same ciphertext claimed to come from another device doesn't open.
    with pytest.raises(OlmError):
        b.decrypt_olm("dm:b", c.curve("dm:c"), mtype, ct)


def test_corrupt_state_file_is_refused(tmp_path):
    a = store(tmp_path, "a")
    a.curve("dm:a")
    data = bytearray(a.path.read_bytes())
    data[-1] ^= 1
    a.path.write_bytes(bytes(data))
    with pytest.raises(OlmError):
        reopen(a)
    with pytest.raises(OlmError):
        OlmStore(a.path, os.urandom(32), a.pickle_key)


def test_signatures(tmp_path):
    a = store(tmp_path, "a")
    sig = a.sign("dm:a", b"message")
    assert OlmStore.verify(a.ed("dm:a"), b"message", sig)
    assert not OlmStore.verify(a.ed("dm:a"), b"other", sig)
    assert not OlmStore.verify(a.ed("dm:a"), b"message", b"\x00" * 64)
    kid, key = a.fallback_key("dm:a")
    s = a.signed_otk("dm:a", kid, key, True)
    assert OlmStore.verify(a.ed("dm:a"), olm.otk_message(a.curve("dm:a"), kid, key, True), s)
    assert not OlmStore.verify(a.ed("dm:a"), olm.otk_message(a.curve("dm:a"), kid, key, False), s)


def test_fallback_key_is_replaced_on_a_schedule_and_the_previous_one_still_works(tmp_path):
    """The published fallback key stays the same (cards and status device
    entries don't change on every start) until it's a week old; then a new
    one is made and the previous one keeps opening sessions until the one
    after it (a card up to a week old still works)."""
    a, b = store(tmp_path, "a"), store(tmp_path, "b")
    t0 = 1_700_000_000.0
    kid1, k1 = b.fallback_key("dm:b", now=t0)
    assert b.fallback_key("dm:b", now=t0 + 6 * 86400) == (kid1, k1)
    assert reopen(b).fallback_key("dm:b", now=t0 + 6 * 86400) == (kid1, k1)
    kid2, k2 = b.fallback_key("dm:b", now=t0 + 7 * 86400)
    assert kid2 != kid1 and k2 != k1
    # A session from the previous key (someone holding the older card).
    mtype, ct = a.encrypt_olm("dm:a", b.curve("dm:b"), b"older card", one_time_key=k1)
    assert b.decrypt_olm("dm:b", a.curve("dm:a"), mtype, ct) == b"older card"
    # The one after that forgets it; an explicit rotation counts as one.
    kid3, _k3 = b.fallback_key("dm:b", rotate=True, now=t0 + 8 * 86400)
    assert kid3 not in (kid1, kid2)
    c = store(tmp_path, "c")
    mtype, ct = c.encrypt_olm("dm:c", b.curve("dm:b"), b"stale card", one_time_key=k1)
    with pytest.raises(OlmError):
        b.decrypt_olm("dm:b", c.curve("dm:c"), mtype, ct)
    d = store(tmp_path, "d")
    mtype, ct = d.encrypt_olm("dm:d", b.curve("dm:b"), b"current card", one_time_key=k2)
    assert b.decrypt_olm("dm:b", d.curve("dm:d"), mtype, ct) == b"current card"


GROUP = b"g" * 16


def test_megolm_round_trip_and_late_joiner(tmp_path):
    a, b, c = store(tmp_path, "a"), store(tmp_path, "b"), store(tmp_path, "c")
    sid, skey = a.rotate("dm:a", GROUP)
    assert b.add_inbound("dm:b", sid, skey, GROUP, a.curve("dm:a"), a.ed("dm:a"))
    _, ct1 = a.encrypt_megolm("dm:a", GROUP, b"first")
    # c joins now: it gets the key at the current index and can't read "first".
    sid2, skey_now = a.current_key("dm:a", GROUP)
    assert sid2 == sid
    c.add_inbound("dm:c", sid, skey_now, GROUP, a.curve("dm:a"), a.ed("dm:a"))
    _, ct2 = a.encrypt_megolm("dm:a", GROUP, b"second")
    d = b.decrypt_megolm(sid, a.curve("dm:a"), ct1)
    assert (d.plaintext, d.index, d.group_id, d.label) == (b"first", 0, GROUP, "dm:b")
    assert c.decrypt_megolm(sid, a.curve("dm:a"), ct2).plaintext == b"second"
    with pytest.raises(OlmError):
        c.decrypt_megolm(sid, a.curve("dm:a"), ct1)
    # The sender can read its own events.
    assert a.decrypt_megolm(sid, a.curve("dm:a"), ct2).plaintext == b"second"


def test_megolm_wrong_sender_and_unknown_session(tmp_path):
    a, b = store(tmp_path, "a"), store(tmp_path, "b")
    sid, skey = a.rotate("dm:a", GROUP)
    b.add_inbound("dm:b", sid, skey, GROUP, a.curve("dm:a"), a.ed("dm:a"))
    _, ct = a.encrypt_megolm("dm:a", GROUP, b"x")
    with pytest.raises(OlmError):
        b.decrypt_megolm(sid, b"\x01" * 32, ct)
    with pytest.raises(OlmError):
        b.decrypt_megolm("nope", a.curve("dm:a"), ct)
    # A sender can't re-bind its session id to another group.
    with pytest.raises(OlmError):
        b.add_inbound("dm:b", sid, skey, b"h" * 16, a.curve("dm:a"), a.ed("dm:a"))
    with pytest.raises(OlmError):
        b.add_inbound("dm:b", "other-id", skey, GROUP, a.curve("dm:a"), a.ed("dm:a"))
    # Another device sharing the same session gets its own copy, which
    # doesn't touch the sender's: a's events still open only as a's.
    assert b.add_inbound("dm:b", sid, skey, GROUP, b"\x02" * 32, a.ed("dm:a"))
    assert b.decrypt_megolm(sid, a.curve("dm:a"), ct).sender_curve == a.curve("dm:a")
    with pytest.raises(OlmError):
        b.decrypt_megolm(sid, b"\x01" * 32, ct)


def test_a_member_cant_take_over_anothers_session(tmp_path):
    """A member given another sender's session key may share it to a victim
    before the real sender does. With first-wins binding of the session id,
    the real sender's share would be refused and the victim could no longer
    read them. Each sharer's copy is kept apart instead: the real sender's
    share is accepted and opens its events, and the member's copy opens
    events only as that member, whose plaintext names the real sending
    device (which dm.py and statuses.py refuse)."""
    alice, mallory, victim = store(tmp_path, "a"), store(tmp_path, "m"), store(tmp_path, "v")
    sid, skey = alice.rotate("dm:a", GROUP)
    assert mallory.add_inbound("dm:m", sid, skey, GROUP, alice.curve("dm:a"), alice.ed("dm:a"))
    _, ct = alice.encrypt_megolm("dm:a", GROUP, b"from alice")
    # Mallory shares Alice's session to the victim first, as her own.
    assert victim.add_inbound("dm:v", sid, skey, GROUP, mallory.curve("dm:m"), mallory.ed("dm:m"))
    # Alice's own share still goes in, and her events open as hers.
    assert victim.add_inbound("dm:v", sid, skey, GROUP, alice.curve("dm:a"), alice.ed("dm:a"))
    d = victim.decrypt_megolm(sid, alice.curve("dm:a"), ct)
    assert (d.plaintext, d.sender_curve, d.sender_ed) == (b"from alice", alice.curve("dm:a"), alice.ed("dm:a"))
    assert victim.check_replay(sid, d.index, b"ev", alice.curve("dm:a"))
    # Mallory's copy opens the same ciphertext only as hers, and replay state is per copy.
    dm_ = victim.decrypt_megolm(sid, mallory.curve("dm:m"), ct)
    assert dm_.sender_curve == mallory.curve("dm:m") != alice.curve("dm:a")
    assert victim.check_replay(sid, dm_.index, b"other", mallory.curve("dm:m"))
    assert not victim.check_replay(sid, d.index, b"other", alice.curve("dm:a"))
    # An event that waits for its key is looked up for its own sender.
    assert (
        victim.inbound_for(sid, alice.curve("dm:a"))["sender_curve"]
        != victim.inbound_for(sid, mallory.curve("dm:m"))["sender_curve"]
    )
    # The copies are bounded per session id.
    for i in range(olm.MAX_SHARERS - 1):
        victim.add_inbound("dm:v", sid, skey, GROUP, bytes([i + 10]) * 32, alice.ed("dm:a"))
    with pytest.raises(OlmError):
        victim.add_inbound("dm:v", sid, skey, GROUP, b"\xee" * 32, alice.ed("dm:a"))
    # Persisted and reloaded the same way.
    victim.save()
    again = OlmStore(victim.path, victim.seal_key, victim.pickle_key)
    assert again.decrypt_megolm(sid, alice.curve("dm:a"), ct).plaintext == b"from alice"


def test_megolm_replay_detection(tmp_path):
    a = store(tmp_path, "a")
    sid, _ = a.rotate("dm:a", GROUP)
    assert a.check_replay(sid, 0, b"event-1")
    assert a.check_replay(sid, 0, b"event-1")  # the same event again (live + mailbox)
    assert not a.check_replay(sid, 0, b"event-2")  # same index, different event
    assert not a.check_replay("unknown", 0, b"event-1")
    # The memory of seen indexes is bounded, but forgetting old ones doesn't
    # let them be replayed.
    for i in range(1, 2100):
        assert a.check_replay(sid, i, f"e{i}".encode())
    assert a.check_replay(sid, 2100, b"e2100")
    assert not a.check_replay(sid, 5, b"e5")  # forgotten, and refused
    assert not a.check_replay(sid, 5, b"other")
    assert a.check_replay(sid, 2099, b"e2099") and not a.check_replay(sid, 2099, b"x")


def test_rotation_limits(tmp_path, monkeypatch):
    a = store(tmp_path, "a")
    assert a.needs_rotation("dm:a", GROUP)
    a.rotate("dm:a", GROUP)
    assert not a.needs_rotation("dm:a", GROUP)
    for _ in range(olm.ROTATE_MESSAGES - 1):
        a.encrypt_megolm("dm:a", GROUP, b".")
    assert not a.needs_rotation("dm:a", GROUP)
    a.encrypt_megolm("dm:a", GROUP, b".")
    assert a.needs_rotation("dm:a", GROUP)
    a.rotate("dm:a", GROUP)
    created = a.outbound_info("dm:a", GROUP)["created"]
    assert not a.needs_rotation("dm:a", GROUP, now=created + olm.ROTATE_SECONDS - 1)
    assert a.needs_rotation("dm:a", GROUP, now=created + olm.ROTATE_SECONDS)


def test_shared_tracking_and_forget(tmp_path):
    a = store(tmp_path, "a")
    a.rotate("dm:a", GROUP)
    a.mark_shared("dm:a", GROUP, [b"\x01" * 32])
    assert len(a.shared_with("dm:a", GROUP)) == 1
    a2 = reopen(a)
    assert len(a2.shared_with("dm:a", GROUP)) == 1
    a2.rotate("dm:a", GROUP)
    assert a2.shared_with("dm:a", GROUP) == set()
    a2.forget_group(GROUP)
    assert a2.outbound_info("dm:a", GROUP) is None
    assert not a2.inbound


def test_drop_account(tmp_path):
    a, b = store(tmp_path, "a"), store(tmp_path, "b")
    mtype, ct = start_session(a, b)
    b.decrypt_olm("dm:b", a.curve("dm:a"), mtype, ct)
    b.rotate("dm:b", GROUP)
    b.drop_account("dm:b")
    assert not b.has_account("dm:b")
    assert not b.inbound
    with pytest.raises(OlmError):
        b.decrypt_olm("dm:b", a.curve("dm:a"), mtype, ct)


def test_replayed_prekey_messages_are_refused_and_harmless(tmp_path):
    """A replayed pre-key message (fallback key path) is refused: it doesn't
    create a fresh session or push out the real one."""
    a, b = store(tmp_path, "a"), store(tmp_path, "b")
    _kid, fallback = b.fallback_key("dm:b")
    mtype, ct = a.encrypt_olm("dm:a", b.curve("dm:b"), b"room key", one_time_key=fallback)
    assert mtype == 0
    assert b.decrypt_olm("dm:b", a.curve("dm:a"), mtype, ct) == b"room key"
    for _ in range(5):
        with pytest.raises(OlmError):
            b.decrypt_olm("dm:b", a.curve("dm:a"), mtype, ct)
    # The real session is intact: a's next messages still decrypt.
    t2, c2 = a.encrypt_olm("dm:a", b.curve("dm:b"), b"second")
    assert b.decrypt_olm("dm:b", a.curve("dm:a"), t2, c2) == b"second"
    # Even after the session is evicted from b's short list, the replay can't
    # re-create it.
    b.sessions["dm:b"][olm._b64(a.curve("dm:a"))] = []
    with pytest.raises(OlmError, match="replay"):
        b.decrypt_olm("dm:b", a.curve("dm:a"), mtype, ct)
    # Survives a restart.
    with pytest.raises(OlmError):
        reopen(b).decrypt_olm("dm:b", a.curve("dm:a"), mtype, ct)


def test_replayed_normal_message_refused(tmp_path):
    a, b = store(tmp_path, "a"), store(tmp_path, "b")
    mtype, ct = start_session(a, b)
    b.decrypt_olm("dm:b", a.curve("dm:a"), mtype, ct)
    rt, rc = b.encrypt_olm("dm:b", a.curve("dm:a"), b"reply")
    a.decrypt_olm("dm:a", b.curve("dm:b"), rt, rc)
    nt, nc = a.encrypt_olm("dm:a", b.curve("dm:b"), b"normal")
    assert nt == 1
    assert b.decrypt_olm("dm:b", a.curve("dm:a"), nt, nc) == b"normal"
    with pytest.raises(OlmError):
        b.decrypt_olm("dm:b", a.curve("dm:a"), nt, nc)


def test_peer_state_is_bounded(tmp_path, monkeypatch):
    monkeypatch.setattr(olm, "MAX_PEERS", 3)
    b = store(tmp_path, "b")
    senders = [store(tmp_path, f"s{i}") for i in range(5)]
    for i, s in enumerate(senders):
        (_kid, otk), *_ = b.new_one_time_keys("dm:b", 1)
        t, c = s.encrypt_olm("dm:s", b.curve("dm:b"), b"hi", one_time_key=otk)
        b.decrypt_olm("dm:b", s.curve("dm:s"), t, c)
        assert len(b.sessions["dm:b"]) == min(i + 1, 3)
    # The most recent peers are the ones kept.
    assert set(b.sessions["dm:b"]) == {olm._b64(s.curve("dm:s")) for s in senders[2:]}


def test_old_inbound_sessions_are_pruned(tmp_path):
    a = store(tmp_path, "a")
    for _ in range(6):
        a.rotate("dm:a", GROUP)
    assert len(a.inbound) == 6
    assert a.prune_inbound() == 0  # all recent
    for info in a.inbound.values():
        info["added"] -= olm.INBOUND_KEEP_SECONDS + 1
    assert a.prune_inbound() == 6 - olm.INBOUND_KEEP_NEWEST
    assert len(a.inbound) == olm.INBOUND_KEEP_NEWEST
