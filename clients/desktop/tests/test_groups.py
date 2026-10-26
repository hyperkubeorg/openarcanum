"""Group DM state rules: every member's client enforces them (docs/protocol.md §9.4)."""

import contextlib
import os

import pytest

from openarcanum import groups
from openarcanum.groups import Group, GroupError, sign_state
from openarcanum.keys import SigningKey
from openarcanum.wire import dm_pb2 as dm

OWNER, ALICE, BOB, MALLORY = (SigningKey.generate() for _ in range(4))


def member(k: SigningKey, seq=1, name="x"):
    return dm.GroupMember(identity=k.public, name=name, joined_seq=seq)


def genesis(direct=False, can_add=False):
    st = dm.GroupState(
        group_id=os.urandom(16),
        seq=1,
        owner=OWNER.public,
        members=[member(OWNER)],
        name="" if direct else "Plans",
        members_can_add=can_add,
        allow_attachments=True,
        direct=direct,
        relay="ws://relay/ws",
        topic=os.urandom(32),
    )
    return Group.from_chain([sign_state(OWNER, st)])


def change(g: Group, signer: SigningKey, edit) -> dm.SignedGroupState:
    nxt = g.successor()
    edit(nxt)
    return sign_state(signer, nxt)


def invite(g, who, by=OWNER):
    return change(g, by, lambda s: s.invited.append(member(who, 0)))


def accept(g, who):
    def edit(s):
        keep = [m for m in s.invited if m.identity != who.public]
        del s.invited[:]
        s.invited.extend(keep)
        s.members.append(member(who, s.seq))

    return change(g, who, edit)


def with_alice():
    g = genesis()
    assert g.apply(invite(g, ALICE))
    assert g.apply(accept(g, ALICE))
    return g


def test_genesis_rules():
    g = genesis()
    assert g.is_member(OWNER.public) and g.state.seq == 1
    st = g.successor()
    st.seq, st.prev = 1, b""
    with pytest.raises(GroupError):  # signed by someone other than the owner
        groups.check_genesis(sign_state(ALICE, st))
    st.members.append(member(ALICE))
    with pytest.raises(GroupError):  # starts with more than the owner as member
        groups.check_genesis(sign_state(OWNER, st))


def test_invite_accept_decline():
    g = with_alice()
    assert g.is_member(ALICE.public) and g.joined_seq(ALICE.public) == 3
    assert g.apply(invite(g, BOB))
    assert g.is_invited(BOB.public)
    # Bob declines.
    assert g.apply(change(g, BOB, lambda s: s.invited.pop()))
    assert not g.is_invited(BOB.public) and not g.is_member(BOB.public)


def test_invitee_cannot_do_more_than_accept():
    g = genesis()
    g.apply(invite(g, ALICE))

    def accept_and_rename(s):
        s.invited.pop()
        s.members.append(member(ALICE, s.seq))
        s.name = "mine now"

    with pytest.raises(GroupError):
        g.apply(change(g, ALICE, accept_and_rename))

    def accept_with_old_history(s):
        s.invited.pop()
        s.members.append(member(ALICE, 1))

    with pytest.raises(GroupError):
        g.apply(change(g, ALICE, accept_with_old_history))


def test_owner_cannot_add_members_directly():
    g = genesis()
    with pytest.raises(GroupError):
        g.apply(change(g, OWNER, lambda s: s.members.append(member(ALICE, s.seq))))


def test_non_owner_changes_refused():
    g = with_alice()
    for edit in (
        lambda s: setattr(s, "name", "hijacked"),
        lambda s: setattr(s, "owner", ALICE.public),
        lambda s: setattr(s, "allow_attachments", False),
        lambda s: setattr(s, "members_can_add", True),
        lambda s: s.invited.append(member(BOB, 0)),  # members_can_add is off
    ):
        with pytest.raises(GroupError):
            g.apply(change(g, ALICE, edit))
    # Not a member at all.
    with pytest.raises(GroupError):
        g.apply(change(g, MALLORY, lambda s: s.invited.append(member(MALLORY, 0))))


def test_forged_signature_refused():
    g = with_alice()
    bad = change(g, OWNER, lambda s: setattr(s, "name", "x"))
    bad.signer = ALICE.public  # claims Alice signed it
    with pytest.raises(GroupError):
        g.apply(bad)
    bad2 = change(g, OWNER, lambda s: setattr(s, "name", "x"))
    bad2.state = bad2.state + b"\x00"
    with pytest.raises(GroupError):
        g.apply(bad2)


def test_members_can_add_allows_only_invites():
    g = genesis(can_add=True)
    g.apply(invite(g, ALICE))
    g.apply(accept(g, ALICE))
    assert g.apply(invite(g, BOB, by=ALICE))
    with pytest.raises(GroupError):  # but not removing anyone
        g.apply(change(g, ALICE, lambda s: s.invited.pop()))


def test_leave_and_owner_rules():
    g = with_alice()
    # The owner can't leave while others remain without transferring.
    with pytest.raises(GroupError):

        def owner_leaves(s):
            keep = [m for m in s.members if m.identity != OWNER.public]
            del s.members[:]
            s.members.extend(keep)

        g.apply(change(g, OWNER, owner_leaves))

    def transfer_and_leave(s):
        keep = [m for m in s.members if m.identity != OWNER.public]
        del s.members[:]
        s.members.extend(keep)
        s.owner = ALICE.public

    assert g.apply(change(g, OWNER, transfer_and_leave))
    assert g.state.owner == ALICE.public and not g.is_member(OWNER.public)
    # The new owner can rename; the old one can't.
    assert g.apply(change(g, ALICE, lambda s: setattr(s, "name", "Alice's")))


def test_member_leaves():
    g = with_alice()

    def alice_leaves(s):
        keep = [m for m in s.members if m.identity != ALICE.public]
        del s.members[:]
        s.members.extend(keep)

    assert g.apply(change(g, ALICE, alice_leaves))
    assert not g.is_member(ALICE.public)


def test_size_limit():
    g = genesis()
    people = [SigningKey.generate() for _ in range(groups.MAX_MEMBERS)]
    for p in people[: groups.MAX_MEMBERS - 1]:
        assert g.apply(invite(g, p))
    with pytest.raises(GroupError):
        g.apply(invite(g, people[-1]))


def test_direct_conversation_rules():
    g = genesis(direct=True)
    assert g.apply(invite(g, ALICE))
    with pytest.raises(GroupError):  # a third person
        g.apply(invite(g, BOB))
    with pytest.raises(GroupError):  # no names on a direct conversation
        g.apply(change(g, OWNER, lambda s: setattr(s, "name", "x")))
    with pytest.raises(GroupError):
        g.apply(change(g, OWNER, lambda s: setattr(s, "direct", False)))


def test_rollback_and_gaps():
    g = with_alice()
    head = g.state.seq
    old = g.chain[1]
    assert not g.apply(old)  # an older state changes nothing
    s1 = change(g, OWNER, lambda s: setattr(s, "name", "one"))
    tmp = Group.from_chain([*g.chain, s1])
    s2 = change(tmp, OWNER, lambda s: setattr(s, "name", "two"))
    assert not g.apply(s2)  # arrives first: held until its parent arrives
    assert g.state.seq == head
    assert g.apply(s1)
    assert g.state.name == "two" and g.state.seq == head + 2


def test_concurrent_changes_converge():
    g1 = with_alice()
    g2 = Group.from_list(g1.to_list())
    g1.apply(invite(g1, BOB))  # the owner invites Bob
    a = g1.chain[-1]
    # Meanwhile Alice leaves, based on the same parent.
    parent = Group.from_chain(g1.chain[:-1])

    def alice_leaves(s):
        keep = [m for m in s.members if m.identity != ALICE.public]
        del s.members[:]
        s.members.extend(keep)

    b = change(parent, ALICE, alice_leaves)
    g2.apply(b)
    g2.apply(a)
    g1.apply(b)
    # Both clients keep the owner's change.
    assert g1.chain[-1] == g2.chain[-1] == a


def test_chain_round_trip_and_tampered_chain():
    g = with_alice()
    again = Group.from_list(g.to_list())
    assert again.state == g.state
    chain = list(g.chain)
    chain[1], chain[2] = chain[2], chain[1]
    with pytest.raises(GroupError):
        Group.from_chain(chain)


def leaving(who):
    def edit(s):
        keep = [m for m in s.members if m.identity != who.public]
        del s.members[:]
        s.members.extend(keep)

    return edit


def test_forks_converge_even_after_building_on_the_loser():
    """With members-can-add on, Mallory signs two invites on
    the state before the owner removes her. Every client ends on the owner's
    chain, whatever order it saw the states in."""
    g = genesis(can_add=True)
    for who in (ALICE, MALLORY):
        g.apply(invite(g, who))
        g.apply(accept(g, who))
    base = Group.from_list(g.to_list())
    removal = change(base, OWNER, leaving(MALLORY))
    m1 = invite(base, BOB, by=MALLORY)
    after_m1 = Group.from_chain([*base.chain, m1])
    carol = SigningKey.generate()
    m2 = invite(after_m1, carol, by=MALLORY)
    orders = [(removal, m1, m2), (m1, m2, removal), (m1, removal, m2), (m2, m1, removal)]
    heads = set()
    for order in orders:
        c = Group.from_list(base.to_list())
        for s in order:
            with contextlib.suppress(GroupError):
                c.apply(s)
        assert not c.is_member(MALLORY.public), order
        assert not c.is_invited(BOB.public) and not c.is_invited(carol.public)
        heads.add(groups.state_hash(c.chain[-1]))
    assert len(heads) == 1


def test_non_owners_cannot_rewrite_other_entries():
    """Accepting, declining, leaving or inviting can't also rename other
    members or repoint their member keys."""
    g = genesis(can_add=True)
    g.apply(invite(g, ALICE))
    g.apply(invite(g, BOB))

    def accept_and_rename_owner(s):
        s.invited.pop(0)
        s.members.append(member(ALICE, s.seq))
        s.members[0].name = "Bank support"

    with pytest.raises(GroupError):
        g.apply(change(g, ALICE, accept_and_rename_owner))

    def decline_and_repoint_bob(s):
        s.invited.pop(0)
        s.invited[0].member = b"\x01" * 32

    with pytest.raises(GroupError):
        g.apply(change(g, ALICE, decline_and_repoint_bob))
    g.apply(accept(g, ALICE))

    def leave_and_rename(s):
        leaving(ALICE)(s)
        s.members[0].name = "not the owner's name"

    with pytest.raises(GroupError):
        g.apply(change(g, ALICE, leave_and_rename))

    def invite_and_rename_invitee(s):
        s.invited.append(member(MALLORY, 0))
        s.invited[0].name = "renamed"

    with pytest.raises(GroupError):
        g.apply(change(g, ALICE, invite_and_rename_invitee))
    # The plain versions still work.
    assert g.apply(change(g, ALICE, lambda s: s.invited.append(member(MALLORY, 0))))
    assert g.apply(change(g, ALICE, leaving(ALICE)))


def test_rollback_depth_is_bounded(monkeypatch):
    monkeypatch.setattr(groups, "MAX_ROLLBACK", 3)
    g = with_alice()
    base = Group.from_list(g.to_list())
    late = change(base, OWNER, lambda s: setattr(s, "name", "owner's late change"))
    for i in range(4):
        g.apply(change(g, OWNER, lambda s, i=i: setattr(s, "name", f"n{i}")))
    with pytest.raises(GroupError, match="newer change"):
        g.apply(late)


def test_pending_states_survive_a_reload():
    g = with_alice()
    s1 = change(g, OWNER, lambda s: setattr(s, "name", "one"))
    s2 = change(Group.from_chain([*g.chain, s1]), OWNER, lambda s: setattr(s, "name", "two"))
    g.apply(s2)  # early
    saved_chain, saved_pending = g.to_list(), g.pending_list()
    again = Group.from_list(saved_chain)
    again.add_pending(saved_pending)
    assert again.apply(s1)
    assert again.state.name == "two"


def alice_leaves(s):
    keep = [m for m in s.members if m.identity != ALICE.public]
    del s.members[:]
    s.members.extend(keep)


@pytest.mark.parametrize("leave_first", [True, False])
def test_a_leave_that_loses_a_fork_is_remembered(leave_first):
    """Whichever arrives first, a member's leaving that loses to the owner's
    concurrent change is noted, so the owner's client can remove them."""
    g = with_alice()
    rename = change(g, OWNER, lambda s: setattr(s, "name", "Renamed"))
    leave = change(g, ALICE, alice_leaves)
    for st in (leave, rename) if leave_first else (rename, leave):
        g.apply(st)
    assert g.chain[-1] == rename and g.is_member(ALICE.public)
    assert g.lost_leaves == {ALICE.public}


def test_only_self_removals_count_as_lost_leaves():
    g = with_alice()
    assert g.apply(invite(g, BOB))
    # The owner's rename and a different owner change don't count; nor does
    # the owner's own change losing (lower hash between two owner changes).
    a = change(g, OWNER, lambda s: setattr(s, "name", "A"))
    b = change(g, OWNER, lambda s: setattr(s, "name", "B"))
    g.apply(a)
    g.apply(b)
    assert g.lost_leaves == set()


def _mallory_fork():
    """Mallory accepts, the owner invites Bob and removes her; she then
    grinds another acceptance of
    her own at the same position with a lower hash. Returns the honest chain
    and her forked acceptance."""
    g = genesis()
    g.apply(invite(g, MALLORY))
    pre_accept = Group.from_list(g.to_list())

    def accept_at(at):
        def edit(s):
            del s.invited[:]
            s.members.append(member(MALLORY, s.seq))
            s.at = at

        return edit

    acc = change(g, MALLORY, accept_at(100))
    g.apply(acc)
    g.apply(invite(g, BOB))
    g.apply(change(g, OWNER, leaving(MALLORY)))
    assert not g.is_member(MALLORY.public) and g.is_invited(BOB.public)
    for at in range(101, 10_000):
        alt = change(pre_accept, MALLORY, accept_at(at))
        if groups.state_hash(alt) < groups.state_hash(acc):
            return g, acc, alt
    raise AssertionError("no lower hash found")


def test_a_removed_member_cant_undo_the_removal_with_a_fork():
    g, _acc, alt = _mallory_fork()
    head = g.chain[-1]
    assert not g.apply(alt)
    assert g.chain[-1] == head
    assert not g.is_member(MALLORY.public) and g.is_invited(BOB.public)
    # Building on her fork doesn't help either.
    on_fork = Group.from_chain([*g.chain[:2], alt])
    more = change(on_fork, MALLORY, lambda s: setattr(s, "at", 5))
    assert not g.apply(more)
    assert g.chain[-1] == head and not g.is_member(MALLORY.public)


def test_a_client_that_saw_the_fork_first_still_ends_on_the_owners_chain():
    """Whatever order the states arrive in (the fork first, the owner's
    states early, a reload in between), every client keeps the owner's chain."""
    g, _acc, alt = _mallory_fork()
    honest = list(g.chain)
    start = honest[:2]
    rest = honest[2:]  # acc, invite Bob, remove Mallory
    orders = [
        [alt, *rest],
        [alt, *reversed(rest)],
        [rest[2], alt, rest[1], rest[0]],
        [*rest, alt],
    ]
    for order in orders:
        c = Group.from_chain(start)
        for i, s in enumerate(order):
            with contextlib.suppress(GroupError):
                c.apply(s)
            if i == 1:  # a reload in the middle
                again = Group.from_list(c.to_list())
                again.add_pending(c.pending_list())
                c = again
        assert c.chain == honest, order
        assert not c.is_member(MALLORY.public)
