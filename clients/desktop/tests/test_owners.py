"""Owner sets (owners.py): chained owner changes, certs from any owner, and
Go<->Python interop for the same structures."""

import json
import subprocess

import pytest
from conftest import binary

from openarcanum import keys, owners
from openarcanum.keys import BadSignature, SigningKey
from openarcanum.wire import session_pb2 as sp


class Chain:
    def __init__(self):
        self.founder = SigningKey.generate()
        self.set = owners.OwnerSet.founder(self.founder.public)
        self.chain: list[sp.SignedOwnerChange] = []

    @property
    def cid(self) -> bytes:
        return self.founder.public

    def apply(self, sc):
        self.set.apply(sc)
        self.chain.append(sc)

    def add(self, signer: SigningKey) -> tuple[SigningKey, SigningKey]:
        owner, member = SigningKey.generate(), SigningKey.generate()
        self.apply(owners.sign_change(signer, self.set, add=owners.request_owner_key(owner, member, self.cid)))
        return owner, member

    def remove(self, signer: SigningKey, key: bytes):
        self.apply(owners.sign_change(signer, self.set, remove=key))


def refused(s: owners.OwnerSet, sc) -> bool:
    try:
        s.clone().apply(sc)
    except BadSignature:
        return True
    return False


def test_add_remove_and_last_owner():
    c = Chain()
    second, member = c.add(c.founder)
    third, _ = c.add(second)
    s = owners.verify_chain(c.cid, c.chain)
    assert s.seq == 2 and s.owners == [c.cid, second.public, third.public]
    assert s.member[second.public] == member.public
    c.remove(second, c.cid)  # the founder is removed; the id stays
    s = owners.verify_chain(c.cid, c.chain)
    assert not s.has(c.cid) and s.community == c.cid
    assert refused(s, owners.sign_change(c.founder, s, remove=third.public))
    c.remove(third, third.public)  # removing yourself while another remains
    with pytest.raises(BadSignature, match="last owner"):
        c.set.clone().apply(owners.sign_change(second, c.set, remove=second.public))
    # A removed key can't come back.
    back = owners.request_owner_key(third, SigningKey.generate(), c.cid)
    assert refused(c.set, owners.sign_change(second, c.set, add=back))


def test_bad_changes_are_refused():
    c = Chain()
    second, _ = c.add(c.founder)
    stranger, member, new = SigningKey.generate(), SigningKey.generate(), SigningKey.generate()
    req = owners.request_owner_key(new, member, c.cid)
    assert refused(c.set, owners.sign_change(stranger, c.set, add=req))
    forged = owners.sign_change(second, c.set, add=req)
    forged.signature = stranger.sign(keys.CTX_OWNER_CHANGE, forged.change)
    assert refused(c.set, forged)
    no_proof = sp.OwnerAdd()
    no_proof.CopyFrom(req)
    no_proof.owner_sig = member.sign(keys.CTX_OWNER_KEY, req.request)
    assert refused(c.set, owners.sign_change(second, c.set, add=no_proof))
    no_consent = sp.OwnerAdd()
    no_consent.CopyFrom(req)
    no_consent.member_sig = stranger.sign(keys.CTX_OWNER_KEY, req.request)
    assert refused(c.set, owners.sign_change(second, c.set, add=no_consent))
    for field, value in (("seq", 7), ("prev", b"\x00" * 32), ("community", stranger.public)):
        oc = sp.OwnerChange(community=c.cid, seq=c.set.seq + 1, prev=c.set.head, signer=second.public)
        oc.add.CopyFrom(req)
        setattr(oc, field, value)
        raw = oc.SerializeToString()
        assert refused(c.set, sp.SignedOwnerChange(change=raw, signature=second.sign(keys.CTX_OWNER_CHANGE, raw)))
    with pytest.raises(BadSignature):
        owners.verify_chain(c.cid, [sp.SignedOwnerChange()] * (owners.MAX_OWNER_CHANGES + 1))


def test_cert_from_any_owner_and_rollback():
    c = Chain()
    second, _ = c.add(c.founder)
    server = SigningKey.generate()
    cert = owners.issue_cert(second, server.public, 2, c.set, c.chain)
    got, s = owners.verify_cert(cert, server.public, c.cid)
    assert got.owner_key == second.public and s.seq == 1
    bare = sp.SignedServerCert()
    bare.CopyFrom(cert)
    del bare.owner_chain[:]
    with pytest.raises(BadSignature):
        owners.verify_cert(bare, server.public, c.cid)
    with pytest.raises(BadSignature):
        owners.verify_cert(cert, server.public, SigningKey.generate().public)
    # A client that saw seq 1 refuses the founder-only set (rollback) and a
    # fork at seq 1.
    rec: dict = {}
    assert owners.remember(s, rec, c.chain) and rec["owner_seq"] == 1
    old = owners.issue_cert(c.founder, server.public, 3, owners.OwnerSet.founder(c.cid), [])
    _, old_set = owners.verify_cert(old, server.public, c.cid)
    with pytest.raises(BadSignature):
        owners.check_known(old_set, rec)
    fork = owners.OwnerSet.founder(c.cid)
    fork.apply(
        owners.sign_change(
            c.founder, fork, add=owners.request_owner_key(SigningKey.generate(), SigningKey.generate(), c.cid)
        )
    )
    with pytest.raises(BadSignature):
        owners.check_known(fork, rec)
    owners.check_known(s, rec)
    # Once the second owner is removed, their cert is refused even with the
    # newer chain attached.
    c.remove(c.founder, second.public)
    stale = sp.SignedServerCert()
    stale.CopyFrom(cert)
    del stale.owner_chain[:]
    stale.owner_chain.extend(c.chain)
    with pytest.raises(BadSignature):
        owners.verify_cert(stale, server.public, c.cid)


def _interop(*args) -> dict:
    out = subprocess.run([str(binary("interop")), *args], capture_output=True, text=True)
    return json.loads(out.stdout.strip().splitlines()[-1])


def test_go_chain_verifies_in_python():
    r = _interop("owner-cert")
    cert = sp.SignedServerCert()
    cert.ParseFromString(bytes.fromhex(r["cert"]))
    got, s = owners.verify_cert(cert, bytes.fromhex(r["server"]), bytes.fromhex(r["community"]))
    assert got.owner_key == bytes.fromhex(r["owner"]) and s.owners == [bytes.fromhex(r["owner"])]
    assert s.seq == 2
    # The founder, removed in Go's chain, can't certify in Python.
    founder = SigningKey(bytes.fromhex(r["founder_seed"]))
    forged = owners.issue_cert(founder, bytes.fromhex(r["server"]), 9, owners.OwnerSet.founder(founder.public), [])
    forged.owner_chain.extend(cert.owner_chain)
    with pytest.raises(BadSignature):
        owners.verify_cert(forged, bytes.fromhex(r["server"]), founder.public)


def test_python_chain_verifies_in_go():
    c = Chain()
    second, _ = c.add(c.founder)
    c.remove(second, c.cid)
    server = SigningKey.generate()
    cert = owners.issue_cert(second, server.public, 2, c.set, c.chain)
    r = _interop(
        "verify-cert",
        "-cert",
        cert.SerializeToString().hex(),
        "-server",
        server.public.hex(),
        "-community",
        c.cid.hex(),
    )
    assert r == {"owners": [second.public.hex()], "seq": 2, "head": c.set.head.hex(), "signer": second.public.hex()}
    # Go refuses a cert from the removed founder and a forged change.
    bad = owners.issue_cert(c.founder, server.public, 3, owners.OwnerSet.founder(c.cid), [])
    bad.owner_chain.extend(c.chain)
    assert "error" in _interop(
        "verify-cert", "-cert", bad.SerializeToString().hex(), "-server", server.public.hex(), "-community", c.cid.hex()
    )
    forged = sp.SignedServerCert()
    forged.CopyFrom(cert)
    forged.owner_chain[1].signature = SigningKey.generate().sign(keys.CTX_OWNER_CHANGE, forged.owner_chain[1].change)
    assert "error" in _interop(
        "verify-cert",
        "-cert",
        forged.SerializeToString().hex(),
        "-server",
        server.public.hex(),
        "-community",
        c.cid.hex(),
    )
