"""Group DM state and its rules (docs/protocol.md §9.4).

There is no server in a DM, so every rule about who may change a group is
enforced by every member's client: a state change is accepted only if it is
correctly signed, extends the state this client holds, and is a change its
signer is allowed to make. A hidden button is never the only control.
"""

from __future__ import annotations

import contextlib
import hashlib
from dataclasses import dataclass, field

from openarcanum import keys
from openarcanum.i18n import _
from openarcanum.wire import dm_pb2 as dm

# Up to 10 people in a group DM, members and invitees together.
MAX_MEMBERS = 10
MAX_NAME = 100
MAX_CHAIN = 2000
# How far back a better competing change may replace this client's states.
MAX_ROLLBACK = 64


class GroupError(Exception):
    """A group state change was refused."""


def state_hash(signed: dm.SignedGroupState) -> bytes:
    return hashlib.sha256(signed.SerializeToString(deterministic=True)).digest()


def sign_state(key: keys.SigningKey, state: dm.GroupState) -> dm.SignedGroupState:
    raw = state.SerializeToString(deterministic=True)
    return dm.SignedGroupState(state=raw, signer=key.public, signature=key.sign(keys.CTX_DM_GROUP_STATE, raw))


def open_state(signed: dm.SignedGroupState) -> dm.GroupState:
    if not keys.is_valid(signed.signer, keys.CTX_DM_GROUP_STATE, signed.state, signed.signature):
        raise GroupError(_("the change couldn't be verified"))
    st = dm.GroupState()
    try:
        st.ParseFromString(signed.state)
    except Exception as e:
        raise GroupError(_("damaged group details")) from e
    return st


def _ids(members) -> list[bytes]:
    return [m.identity for m in members]


def _entries(members) -> list[bytes]:
    return [m.SerializeToString(deterministic=True) for m in members]


def _invariants(st: dm.GroupState) -> None:
    members, invited = _ids(st.members), _ids(st.invited)
    if len(st.group_id) != 16:
        raise GroupError(_("damaged group details"))
    if len(set(members)) != len(members) or len(set(invited)) != len(invited):
        raise GroupError(_("duplicate member"))
    if set(members) & set(invited):
        raise GroupError(_("someone is both a member and invited"))
    if any(len(i) != 32 for i in members + invited) or len(st.owner) != 32:
        raise GroupError(_("a member's key is damaged"))
    if len(members) + len(invited) > MAX_MEMBERS:
        raise GroupError(_("a group can have at most {max} people").format(max=MAX_MEMBERS))
    if members and st.owner not in members:
        raise GroupError(_("the owner must be a member"))
    if len(st.name) > MAX_NAME or any(len(m.name) > 64 for m in [*st.members, *st.invited]):
        raise GroupError(_("name too long"))
    if len(st.topic) != 32:
        raise GroupError(_("damaged group details"))
    if st.relay and (len(st.relay) > 512 or not st.relay.startswith(("ws://", "wss://"))):
        raise GroupError(_("the group's relay must be a ws:// or wss:// address"))
    if st.HasField("icon") and (len(st.icon.thumbnail) > 48 * 1024 or st.icon.hash or st.icon.key):
        raise GroupError(_("a group icon is a small inline image"))
    if st.direct and (len(members) + len(invited) > 2 or st.name or st.members_can_add or st.HasField("icon")):
        raise GroupError(_("a direct conversation has exactly two people and no group settings"))


def _same_except(a: dm.GroupState, b: dm.GroupState, *fields: str) -> bool:
    """True if a and b differ at most in seq, prev, at and `fields`."""
    skip = {"seq", "prev", "at", *fields}
    for f in dm.GroupState.DESCRIPTOR.fields:
        if f.name in skip:
            continue
        if getattr(a, f.name) != getattr(b, f.name):
            return False
    return True


def check_genesis(signed: dm.SignedGroupState) -> dm.GroupState:
    st = open_state(signed)
    if st.seq != 1 or st.prev:
        raise GroupError(_("damaged group history"))
    if signed.signer != st.owner or _ids(st.members) != [st.owner]:
        raise GroupError(_("a group starts with its owner as the only member"))
    _invariants(st)
    return st


def check_change(prev_signed: dm.SignedGroupState, prev: dm.GroupState, signed: dm.SignedGroupState) -> dm.GroupState:
    """Validate `signed` as the successor of `prev`. Returns the new state."""
    st = open_state(signed)
    if st.group_id != prev.group_id or st.direct != prev.direct:
        raise GroupError(_("different group"))
    if st.seq != prev.seq + 1 or st.prev != state_hash(prev_signed):
        raise GroupError(_("the change doesn't follow the group's current state"))
    _invariants(st)
    signer = signed.signer
    pm, pi = _ids(prev.members), _ids(prev.invited)
    nm = _ids(st.members)
    if signer == prev.owner:
        # The owner may rename, set the icon, add, remove, toggle settings and
        # transfer ownership. A new member only joins by accepting.
        if set(nm) - set(pm):
            raise GroupError(_("people join by accepting their invitation"))
        if not nm:
            raise GroupError(_("the owner can't leave a group with other people in it without transferring it"))
        return st
    # Everyone else may only touch their own entry: every other member and
    # invitee entry must stay exactly as it was (names, communities, member
    # keys, join points), not just the list of identities.
    pme, pie = _entries(prev.members), _entries(prev.invited)
    nme, nie = _entries(st.members), _entries(st.invited)
    if signer in pi:
        # The invitee accepts (moves to members) or declines (removed).
        if not _same_except(prev, st, "members", "invited"):
            raise GroupError(_("an invitee can only accept or decline"))
        if nie != [e for e, i in zip(pie, pi, strict=True) if i != signer]:
            raise GroupError(_("an invitee can only accept or decline"))
        if nme == pme:
            return st  # declined
        new = [m for m in st.members if m.identity == signer]
        if len(nme) != len(pme) + 1 or nme[:-1] != pme or not new or st.members[-1].identity != signer:
            raise GroupError(_("an invitee can only add themselves"))
        if new[0].joined_seq != st.seq:
            raise GroupError(_("damaged invitation"))
        return st
    if signer not in pm:
        raise GroupError(_("not a member"))
    if (
        nme == [e for e, i in zip(pme, pm, strict=True) if i != signer]
        and nie == pie
        and _same_except(prev, st, "members")
    ):
        return st  # left
    if prev.members_can_add and nme == pme and _same_except(prev, st, "invited"):
        if nie[: len(pie)] != pie or len(nie) == len(pie):
            raise GroupError(_("members can only invite people"))
        return st
    raise GroupError(_("only the owner can make that change"))


def better(a: dm.SignedGroupState, b: dm.SignedGroupState, owner: bytes) -> bool:
    """Two valid successors of the same state: every client keeps the same
    one. The owner's change wins; otherwise the lower hash."""
    ao, bo = a.signer == owner, b.signer == owner
    if ao != bo:
        return ao
    return state_hash(a) < state_hash(b)


def owner_signed(chain: list[dm.SignedGroupState], start: int) -> int:
    """How many of chain[start:] were signed by the owner of the state each
    one extends (the group's owner-signed epochs in that stretch)."""
    n = 0
    for i in range(max(start, 0), len(chain)):
        if i == 0:
            n += 1
            continue
        if chain[i].signer == dm.GroupState.FromString(chain[i - 1].state).owner:
            n += 1
    return n


# Competing states kept to judge forks that arrive in pieces.
MAX_ALT = 64


@dataclass
class Group:
    """The validated chain of a group's states, as one client holds it."""

    chain: list[dm.SignedGroupState] = field(default_factory=list)
    state: dm.GroupState | None = None
    # Successors that arrived before the state they extend.
    pending: dict[int, list[dm.SignedGroupState]] = field(default_factory=dict)
    # Members whose leaving lost to a concurrent change (take_lost_leaves).
    lost_leaves: set[bytes] = field(default_factory=set)
    # States off this client's chain (lost forks, or built on one), by hash:
    # a later state can make their branch the better one.
    alt: dict[bytes, dm.SignedGroupState] = field(default_factory=dict)

    @classmethod
    def from_chain(cls, chain: list[dm.SignedGroupState]) -> Group:
        if not chain or len(chain) > MAX_CHAIN:
            raise GroupError(_("damaged group history"))
        g = cls()
        g.state = check_genesis(chain[0])
        g.chain = [chain[0]]
        for s in chain[1:]:
            g.state = check_change(g.chain[-1], g.state, s)
            g.chain.append(s)
        return g

    def apply(self, signed: dm.SignedGroupState) -> bool:
        """Apply a state that arrived in an event. True if the head changed.
        Invalid states are refused with GroupError.

        Convergence (docs/protocol.md §9.4 "Forks"): of every chain this
        client knows, it keeps the one with the most owner-signed states
        (the owner's epochs); between chains with as many, the one whose
        first differing state is better (`better`: the owner's change, else
        the lower hash). A non-owner can never add an owner-signed state, so
        no fork they make can replace a chain that holds an owner-signed
        state after the fork point: an owner's removal can't be rolled back
        by re-signing an earlier change of one's own. Everything built on a
        losing branch is dropped, so every client that has seen the same
        states holds the same chain."""
        assert self.state is not None
        try:
            st0 = dm.GroupState.FromString(signed.state)
        except Exception as e:
            raise GroupError(_("damaged group details")) from e
        seq = st0.seq
        head = self.state.seq
        if seq > head + 1:
            if len(self.pending) < 64 and len(self.pending.get(seq, [])) < 8:
                self.pending.setdefault(seq, []).append(signed)
            return False
        if seq == head + 1 and st0.prev == state_hash(self.chain[-1]):
            self.state = check_change(self.chain[-1], self.state, signed)
            self.chain.append(signed)
            if len(self.chain) > MAX_CHAIN:
                raise GroupError(_("group history is too long"))
            self._extend_from_alt()
            return True
        if seq < 2:
            return False
        if seq <= head:
            held = self.chain[seq - 1]
            if signed.SerializeToString(deterministic=True) == held.SerializeToString(deterministic=True):
                return False
        return self._fork(signed)

    def _remember(self, *states: dm.SignedGroupState) -> None:
        for s in states:
            if len(self.alt) >= MAX_ALT:
                return
            self.alt.setdefault(state_hash(s), s)

    def _fork(self, signed: dm.SignedGroupState) -> bool:
        """A state that doesn't extend this client's head: find where its
        branch leaves this chain (through competing states seen before) and
        keep whichever chain is better."""
        path = [signed]
        mine = {state_hash(s): i for i, s in enumerate(self.chain)}
        prev = dm.GroupState.FromString(signed.state).prev
        while prev not in mine:
            earlier = self.alt.get(prev)
            if earlier is None or len(path) > MAX_ROLLBACK:
                # Its parent isn't known (yet): keep it for when it is.
                self._remember(signed)
                return False
            path.insert(0, earlier)
            prev = dm.GroupState.FromString(earlier.state).prev
        f = mine[prev] + 1  # index of the first state that differs
        if self.state.seq - (f + 1) >= MAX_ROLLBACK:
            raise GroupError(_("a newer change already replaced this one"))
        parent = Group.from_chain(self.chain[:f])
        try:
            cand = Group.from_chain([*parent.chain, *path])
        except GroupError:
            if len(path) == 1:
                raise
            return False
        if f == len(self.chain):
            wins = True  # it extends this chain after all
        else:
            mine_n, theirs_n = owner_signed(self.chain, f), owner_signed(cand.chain, f)
            wins = theirs_n > mine_n or (theirs_n == mine_n and better(path[0], self.chain[f], parent.state.owner))
        if not wins:
            if f < len(self.chain):
                self._note_lost_leave(parent.state, path[0], open_state(path[0]))
            self._remember(*path)
            # States seen earlier that build on this branch may make it win.
            tip = state_hash(path[-1])
            for child in self._children(tip):
                with contextlib.suppress(GroupError):
                    if self._fork(child):
                        return True
            return False
        if f < len(self.chain):
            held = self.chain[f]
            self._note_lost_leave(parent.state, held, open_state(held))
            self._remember(*self.chain[f:])
        for s in path:
            self.alt.pop(state_hash(s), None)
        self.chain = cand.chain
        self.state = cand.state
        self._extend_from_alt()
        return True

    def _children(self, parent_hash: bytes) -> list[dm.SignedGroupState]:
        out = []
        for s in [*self.alt.values(), *(x for xs in self.pending.values() for x in xs)]:
            with contextlib.suppress(Exception):
                if dm.GroupState.FromString(s.state).prev == parent_hash:
                    out.append(s)
        return out

    def _extend_from_alt(self) -> None:
        """Extend the head with states kept aside or early that build on it."""
        progressed = True
        while progressed:
            progressed = False
            self._apply_pending()
            for child in self._children(state_hash(self.chain[-1])):
                self.alt.pop(state_hash(child), None)
                with contextlib.suppress(GroupError):
                    if self.apply(child):
                        progressed = True
                        break

    def _note_lost_leave(self, parent: dm.GroupState, loser: dm.SignedGroupState, st: dm.GroupState) -> None:
        """Remember a member who left in a state that lost a fork: they
        dropped the group when they left, so they can't sign it again; the
        owner's client removes them on top of the winner instead."""
        was = {m.identity for m in parent.members}
        now = {m.identity for m in st.members}
        if loser.signer != parent.owner and loser.signer in was and was - now == {loser.signer}:
            self.lost_leaves.add(loser.signer)

    def _apply_pending(self) -> None:
        nxt = self.state.seq + 1
        while nxt in self.pending:
            progressed = False
            for cand in self.pending.pop(nxt):
                try:
                    if self.apply(cand):
                        progressed = True
                        break
                except GroupError:
                    continue
            if not progressed:
                return
            nxt = self.state.seq + 1

    def pending_list(self) -> list[str]:
        """States not on the chain (early ones, and competing ones), kept
        with the group so a reload judges later forks the same way."""
        out = [s for ss in self.pending.values() for s in ss] + list(self.alt.values())
        return [keys.encode(s.SerializeToString(deterministic=True)) for s in out]

    def add_pending(self, items: list[str]) -> None:
        for raw in items:
            try:
                self.apply(dm.SignedGroupState.FromString(keys.decode(raw)))
            except GroupError, ValueError:
                continue

    def successor(self) -> dm.GroupState:
        """A copy of the head with seq/prev set for the next change."""
        assert self.state is not None
        nxt = dm.GroupState()
        nxt.CopyFrom(self.state)
        nxt.seq = self.state.seq + 1
        nxt.prev = state_hash(self.chain[-1])
        return nxt

    def is_member(self, identity: bytes) -> bool:
        return self.state is not None and identity in _ids(self.state.members)

    def is_invited(self, identity: bytes) -> bool:
        return self.state is not None and identity in _ids(self.state.invited)

    def joined_seq(self, identity: bytes) -> int:
        for m in self.state.members if self.state else []:
            if m.identity == identity:
                return m.joined_seq
        return 0

    def to_list(self) -> list[str]:
        return [keys.encode(s.SerializeToString(deterministic=True)) for s in self.chain]

    @classmethod
    def from_list(cls, items: list[str]) -> Group:
        return cls.from_chain([dm.SignedGroupState.FromString(keys.decode(i)) for i in items])
