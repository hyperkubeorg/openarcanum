"""Owner sets: which keys may certify a community's server key
(docs/protocol.md §3.1), mirroring internal/identity/owners.go.

A community is identified by its founder's owner key. Every later change to
the set of owner keys is an ``OwnerChange`` signed by a key already in the
set, numbered and chained to the previous change by its SHA-256. The chain
travels inside every ``SignedServerCert``, and each cert names the owner set
it was signed under, so a client that pinned the community id verifies any
later cert from the chain alone. Only Ed25519 signatures are involved.
"""

from __future__ import annotations

import hashlib
import time
from dataclasses import dataclass, field

from openarcanum import keys
from openarcanum.i18n import N_
from openarcanum.keys import BadSignature, SigningKey
from openarcanum.wire import session_pb2 as sp

MAX_OWNERS = 16
MAX_OWNER_CHANGES = 256


def change_hash(change: bytes) -> bytes:
    return hashlib.sha256(change).digest()


@dataclass
class OwnerSet:
    """A verified owner set."""

    community: bytes
    owners: list[bytes]
    member: dict[bytes, bytes] = field(default_factory=dict)  # added owner key -> member key
    heads: list[bytes] = field(default_factory=lambda: [b""])
    ever: set[bytes] = field(default_factory=set)

    @classmethod
    def founder(cls, community: bytes) -> OwnerSet:
        return cls(community=bytes(community), owners=[bytes(community)], ever={bytes(community)})

    @property
    def seq(self) -> int:
        return len(self.heads) - 1

    @property
    def head(self) -> bytes:
        return self.heads[-1]

    def has(self, key: bytes) -> bool:
        return bytes(key) in self.owners

    def extends(self, seq: int, head: bytes) -> bool:
        """True if this set is the one a client saw at (seq, head), or later on
        the same chain."""
        return seq <= self.seq and self.heads[seq] == head

    def clone(self) -> OwnerSet:
        return OwnerSet(self.community, list(self.owners), dict(self.member), list(self.heads), set(self.ever))

    def apply(self, sc: sp.SignedOwnerChange) -> sp.OwnerChange:
        c = sp.OwnerChange()
        try:
            c.ParseFromString(sc.change)
        except Exception as e:
            raise BadSignature(N_("damaged owner change")) from e
        if c.community != self.community:
            raise BadSignature(N_("owner change is for another community"))
        if c.seq != self.seq + 1:
            raise BadSignature(N_("owner change out of order"))
        if c.prev != self.head:
            raise BadSignature(N_("owner change doesn't follow the previous one"))
        if not self.has(c.signer):
            raise BadSignature(N_("owner change is signed by a key that isn't an owner"))
        keys.verify(c.signer, keys.CTX_OWNER_CHANGE, sc.change, sc.signature)
        op = c.WhichOneof("op")
        if op == "add":
            req = verify_owner_add(c.add, self.community)
            if req.owner_key in self.ever:
                raise BadSignature(N_("that key is or was already an owner"))
            if any(m == req.member and self.has(k) for k, m in self.member.items()):
                raise BadSignature(N_("that member already holds an owner key"))
            if len(self.owners) >= MAX_OWNERS:
                raise BadSignature(f"a community can have at most {MAX_OWNERS} owners")
            self.owners.append(bytes(req.owner_key))
            self.member[bytes(req.owner_key)] = bytes(req.member)
            self.ever.add(bytes(req.owner_key))
        elif op == "remove":
            if not self.has(c.remove):
                raise BadSignature(N_("that key isn't an owner"))
            if len(self.owners) == 1:
                raise BadSignature(N_("the last owner can't be removed"))
            self.owners = [k for k in self.owners if k != c.remove]
        else:
            raise BadSignature(N_("unknown owner change"))
        self.heads.append(change_hash(sc.change))
        return c


def verify_chain(community: bytes, chain) -> OwnerSet:
    if len(community) != 32:
        raise BadSignature(N_("damaged community ID"))
    if len(chain) > MAX_OWNER_CHANGES:
        raise BadSignature(N_("the owner history is too long"))
    s = OwnerSet.founder(community)
    for sc in chain:
        s.apply(sc)
    return s


def request_owner_key(owner: SigningKey, member: SigningKey, community: bytes) -> sp.OwnerAdd:
    """The request a member's client submits for the owner key it generated."""
    req = sp.OwnerKeyRequest(community=community, owner_key=owner.public, member=member.public).SerializeToString()
    return sp.OwnerAdd(
        request=req, owner_sig=owner.sign(keys.CTX_OWNER_KEY, req), member_sig=member.sign(keys.CTX_OWNER_KEY, req)
    )


def verify_owner_add(a: sp.OwnerAdd, community: bytes) -> sp.OwnerKeyRequest:
    r = sp.OwnerKeyRequest()
    try:
        r.ParseFromString(a.request)
    except Exception as e:
        raise BadSignature(N_("damaged owner key request")) from e
    if r.community != community:
        raise BadSignature(N_("owner key request is for another community"))
    if len(r.owner_key) != 32 or len(r.member) != 32 or r.owner_key == r.member:
        raise BadSignature(N_("the owner key request has damaged keys"))
    keys.verify(r.owner_key, keys.CTX_OWNER_KEY, a.request, a.owner_sig)
    keys.verify(r.member, keys.CTX_OWNER_KEY, a.request, a.member_sig)
    return r


def sign_change(
    signer: SigningKey, s: OwnerSet, add: sp.OwnerAdd | None = None, remove: bytes | None = None
) -> sp.SignedOwnerChange:
    c = sp.OwnerChange(
        community=s.community, seq=s.seq + 1, prev=s.head, signer=signer.public, at=int(time.time() * 1000)
    )
    if (add is None) == (remove is None):
        raise ValueError("an owner change adds or removes one key")
    if add is not None:
        c.add.CopyFrom(add)
    else:
        c.remove = remove
    raw = c.SerializeToString()
    return sp.SignedOwnerChange(change=raw, signature=signer.sign(keys.CTX_OWNER_CHANGE, raw))


def issue_cert(owner: SigningKey, server_key: bytes, seq: int, s: OwnerSet, chain) -> sp.SignedServerCert:
    if not s.has(owner.public):
        raise BadSignature(N_("that key isn't an owner of the community"))
    raw = sp.ServerCert(
        owner_key=owner.public,
        server_key=server_key,
        issued_at=int(time.time()),
        seq=seq,
        community=s.community,
        owner_seq=s.seq,
        owner_head=s.head,
    ).SerializeToString()
    return sp.SignedServerCert(cert=raw, signature=owner.sign(keys.CTX_SERVER_CERT, raw), owner_chain=list(chain))


def cert_community(c: sp.ServerCert) -> bytes:
    return c.community or c.owner_key


def verify_cert(
    signed: sp.SignedServerCert, server_key: bytes, community: bytes | None
) -> tuple[sp.ServerCert, OwnerSet]:
    """Check that ``signed`` certifies ``server_key`` and is signed by a key in
    the owner set its chain proves, ending exactly where the cert says."""
    c = sp.ServerCert()
    try:
        c.ParseFromString(signed.cert)
    except Exception as e:
        raise BadSignature(N_("the server's ownership proof is damaged")) from e
    cid = cert_community(c)
    if community is not None and cid != community:
        raise BadSignature(N_("the server's ownership proof is for another community"))
    s = verify_chain(cid, signed.owner_chain)
    if c.owner_seq != s.seq or c.owner_head != s.head:
        raise BadSignature(N_("the server's ownership proof doesn't match the owner list"))
    if not s.has(c.owner_key):
        raise BadSignature(N_("the server's ownership proof isn't from an owner"))
    keys.verify(c.owner_key, keys.CTX_SERVER_CERT, signed.cert, signed.signature)
    if c.server_key != server_key:
        raise BadSignature(N_("the server's ownership proof is for another server"))
    return c, s


def check_known(s: OwnerSet, rec: dict) -> None:
    """Refuse an owner set older than, or forked from, the one this client
    last saw for the community (stored in its record)."""
    seq = rec.get("owner_seq", 0)
    head = keys.decode(rec["owner_head"]) if rec.get("owner_head") else b""
    if not s.extends(seq, head):
        raise BadSignature(
            N_("the community's owner list is older than, or differs from, the one this device already has")
        )


def remember(s: OwnerSet, rec: dict, chain) -> bool:
    """Record a newer verified owner set and its chain (kept so this client
    can certify a server on new hardware even when the old one is gone).
    True if the record changed."""
    if s.seq > rec.get("owner_seq", 0) or ("owner_chain" not in rec and s.seq == 0):
        rec["owner_seq"] = s.seq
        rec["owner_head"] = keys.encode(s.head)
        rec["owner_chain"] = [keys.encode(sc.SerializeToString()) for sc in chain]
        return True
    return False
