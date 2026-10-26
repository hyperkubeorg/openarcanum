"""A removed device can't put itself back on a DM identity's device list
(docs/protocol.md §9.1 "Device list changes").

Every linked device holds the identity keys, a removed one too, so the
identity's signature alone can't decide what the list is: after the first
list, every change is approved by a device of the list it replaces (one the
change doesn't remove), and removals are recorded in the list for good.
Without that, a removed device could sign a newer list re-adding itself
(with its old keys or fresh ones) and its own devices and peers would take
it.
"""

from __future__ import annotations

import asyncio
import os

from conftest import new_core

from openarcanum import dm
from openarcanum.keys import SigningKey
from openarcanum.olm import OlmStore
from openarcanum.wire import dm_pb2 as dmp


class Device:
    """A device of an account: its own Olm account (its approval key)."""

    def __init__(self, tmp_path, name: str, identity: bytes):
        self.olm = OlmStore(tmp_path / f"{name}-olm.bin", os.urandom(32), os.urandom(32))
        self.label = f"dm:{name}"
        self.id = os.urandom(8)
        self.identity = identity
        self.ed = self.olm.ed(self.label)

    def entry(self) -> dmp.DmDevice:
        curve = self.olm.curve(self.label)
        return dmp.DmDevice(
            device_id=self.id,
            curve25519=curve,
            ed25519=self.ed,
            name="d",
            device_signature=self.olm.sign(self.label, dm.device_consent(self.identity, curve)),
        )

    def approve(self, signed: dmp.SignedDmDeviceList) -> dmp.SignedDmDeviceList:
        signed.approver = self.ed
        signed.approval = self.olm.sign(self.label, dm.list_approval_message(self.identity, signed.list))
        return signed

    def certify(self, device_id: bytes) -> bytes:
        return self.olm.sign(self.label, dm.device_add_message(self.identity, device_id))


def make_list(key: SigningKey, seq: int, devices, removed=()) -> tuple[dmp.SignedDmDeviceList, dmp.DmDeviceList]:
    lst = dmp.DmDeviceList(identity=key.public, seq=seq, devices=[d.entry() for d in devices], removed=list(removed))
    signed = dm.sign_device_list(key, lst)
    return signed, dm.open_device_list(signed, key.public)


def test_list_changes_need_a_listed_device(tmp_path):
    key = SigningKey.generate()
    y, x = Device(tmp_path, "y", key.public), Device(tmp_path, "x", key.public)
    s1, l1 = make_list(key, 1, [y, x])
    assert dm.check_list_change(None, s1, l1) is None  # the first list
    # Y removes X, for good.
    s2, l2 = make_list(key, 2, [y], removed=[x.id])
    assert dm.check_list_change(l1, s2, l2) is not None  # the identity's signature alone isn't enough
    y.approve(s2)
    assert dm.check_list_change(l1, s2, l2) is None and dm.list_replaces(l1, s2, l2)

    # X still holds the identity key. Re-adding itself, approved by itself:
    s3, l3 = make_list(key, 3, [y, x], removed=[x.id])
    assert dm.check_list_change(l2, x.approve(s3), l3) == "lists a removed device"
    s3, l3 = make_list(key, 3, [y, x])  # dropping the removal
    assert dm.check_list_change(l2, x.approve(s3), l3) == "brings back a removed device"
    # ... or a fresh device of its own (new keys, new device_id):
    z = Device(tmp_path, "z", key.public)
    s3, l3 = make_list(key, 3, [y, z], removed=[x.id])
    assert dm.check_list_change(l2, x.approve(s3), l3) is not None
    assert dm.check_list_change(l2, z.approve(s3), l3) is not None
    # ... with an add certificate X issued while it was still listed:
    s3, l3 = make_list(key, 3, [y, z], removed=[x.id])
    s3.added, s3.approver, s3.approval = z.id, x.ed, x.certify(z.id)
    assert dm.check_list_change(l2, s3, l3) is not None
    # ... or by removing Y (approved by a device the change removes):
    s3, l3 = make_list(key, 3, [x], removed=[y.id])
    assert dm.check_list_change(l2, y.approve(s3), l3) is not None
    assert not dm.list_replaces(l2, x.approve(s3), l3)


def test_a_device_adds_itself_only_with_a_certificate(tmp_path):
    key = SigningKey.generate()
    y, w, v = (Device(tmp_path, n, key.public) for n in ("y", "w", "v"))
    _s1, l1 = make_list(key, 1, [y])
    s2, l2 = make_list(key, 2, [y, w])
    s2.added, s2.approver, s2.approval = w.id, y.ed, y.certify(w.id)
    assert dm.check_list_change(l1, s2, l2) is None
    # The certificate is for W's device_id only, and only for adding W.
    s2b, l2b = make_list(key, 2, [y, v])
    s2b.added, s2b.approver, s2b.approval = v.id, y.ed, y.certify(w.id)
    assert dm.check_list_change(l1, s2b, l2b) is not None
    s2c, l2c = make_list(key, 2, [y, w, v])
    s2c.added, s2c.approver, s2c.approval = w.id, y.ed, y.certify(w.id)
    assert dm.check_list_change(l1, s2c, l2c) is not None
    s2d, l2d = make_list(key, 2, [w])
    s2d.added, s2d.approver, s2d.approval = w.id, y.ed, y.certify(w.id)
    assert dm.check_list_change(l1, s2d, l2d) is not None
    # Nothing else changes: the listed devices keep their keys and names
    # (a device adding itself can't swap Y's keys for its own on the way).
    for change in ("curve25519", "ed25519", "name"):
        changed = y.entry()
        setattr(changed, change, "renamed" if change == "name" else b"\x07" * 32)
        l2e = dmp.DmDeviceList(identity=key.public, seq=2, devices=[changed, w.entry()])
        s2e = dm.sign_device_list(key, l2e)
        s2e.added, s2e.approver, s2e.approval = w.id, y.ed, y.certify(w.id)
        assert dm.check_list_change(l1, s2e, l2e) == "a device adding itself changed more than that", change


def test_peers_refuse_a_removed_devices_list_and_the_removal_wins(tmp_path):
    """The peer side (DMManager._remember_list, through cards): a peer that
    has the removal refuses the removed device's lists; a peer that took the
    removed device's list first (it claimed a higher seq) drops it when the
    removal arrives."""

    async def go():
        core, _ev = await new_core(tmp_path / "peer", "peer", maintainers=[])
        core.meta.urls = []
        try:
            dms = core.dms
            key = SigningKey.generate()
            y, x = Device(tmp_path, "y", key.public), Device(tmp_path, "x", key.public)
            s1, _ = make_list(key, 1, [y, x])
            s2, _ = make_list(key, 2, [y], removed=[x.id])
            y.approve(s2)
            evil, _ = make_list(key, 9, [y, x])
            x.approve(evil)

            def listed() -> set[bytes]:
                return {d.device_id for d in dms._devices_of(dm.b64(key.public))}

            dms._remember_list(key.public, s1)
            dms._remember_list(key.public, s2)
            assert listed() == {y.id}
            assert not dms._remember_list(key.public, evil)
            assert listed() == {y.id}
            # Unapproved (signed by the identity key only): refused too.
            bare, _ = make_list(key, 10, [y, x])
            assert not dms._remember_list(key.public, bare)
            assert listed() == {y.id}

            # Another peer saw the removed device's list before the removal.
            key2 = SigningKey.generate()
            y2, x2 = Device(tmp_path, "y2", key2.public), Device(tmp_path, "x2", key2.public)
            t1, _ = make_list(key2, 1, [y2, x2])
            t2, _ = make_list(key2, 2, [y2], removed=[x2.id])
            y2.approve(t2)
            evil2, _ = make_list(key2, 50, [y2, x2])
            x2.approve(evil2)
            dms._remember_list(key2.public, t1)
            dms._remember_list(key2.public, evil2)  # first seen, before the removal
            assert dms._remember_list(key2.public, t2)  # the removal wins despite its lower seq
            assert {d.device_id for d in dms._devices_of(dm.b64(key2.public))} == {y2.id}
            again, _ = make_list(key2, 60, [y2, x2])
            assert not dms._remember_list(key2.public, x2.approve(again))
        finally:
            await core.shutdown()

    asyncio.run(go())


def test_a_stale_card_cant_put_back_an_old_fallback_key_or_inbox(tmp_path):
    """A contact card older than the one held (a community server serving
    a stale copy) doesn't replace the fallback key and inbox the newer card
    brought, so senders don't go back to a key the device has forgotten."""

    async def go():
        core, _ev = await new_core(tmp_path / "peer", "peer", maintainers=[])
        core.meta.urls = []
        try:
            dms = core.dms
            key = SigningKey.generate()
            y = Device(tmp_path, "y", key.public)
            curve = y.olm.curve(y.label)
            s1, _ = make_list(key, 1, [y])

            def card(issued_at: int, kid: str, fk: bytes, inbox: bytes) -> dm.Card:
                otk = dmp.OneTimeKey(
                    curve25519=curve,
                    key_id=kid,
                    key=fk,
                    fallback=True,
                    signature=y.olm.signed_otk(y.label, kid, fk, True),
                )
                c = dmp.ContactCard(
                    identity=key.public,
                    name="Y",
                    devices=s1,
                    fallback_keys=[otk],
                    inboxes=[dmp.DeviceInbox(curve25519=curve, inbox=inbox)],
                    issued_at=issued_at,
                )
                return dm.open_card(dm.sign_card(key, c))

            def held() -> tuple[bytes, bytes | None]:
                return dms._fallback_key(key.public, curve).key, dms._first_inbox(key.public, curve)

            kid1, fk1 = y.olm.fallback_key(y.label)
            kid2, fk2 = y.olm.fallback_key(y.label, rotate=True)
            older = card(1000, kid1, fk1, b"i" * 32)
            newer = card(2000, kid2, fk2, b"j" * 32)
            dms._remember_card(older)
            assert held() == (fk1, b"i" * 32)
            dms._remember_card(newer)
            assert held() == (fk2, b"j" * 32)
            dms._remember_card(older)  # served again later: ignored
            assert held() == (fk2, b"j" * 32)
            assert dms.d["peers"][dm.b64(key.public)]["card"] == dm.b64(newer.signed.SerializeToString())
            dms._remember_card(newer)  # the same card again is fine
            assert held() == (fk2, b"j" * 32)
        finally:
            await core.shutdown()

    asyncio.run(go())


def test_my_devices_refuse_a_removed_device_putting_itself_back(tmp_path):
    """The own-device side: a removed device holding the identity seed sends
    a newer list (device-group sync, or its card) re-adding itself."""

    async def go():
        core, _ev = await new_core(tmp_path / "me", "me", maintainers=[])
        core.meta.urls = []
        try:
            dms = core.dms
            cid = "c" * 16
            core.vault.data["communities"][cid] = {"name": "C", "status": "left"}
            pub = dms._ident(cid, True)
            await dms._ensure_listed(pub)
            ident = dm.unb64(pub)
            key = dms._signing(pub)  # what every linked device holds
            # Another of my devices, X, linked (certified, adds itself) ...
            x = Device(tmp_path, "x", ident)
            dms._issue_certs(pub, [x.id.hex()])
            cert = dms.d["ids"][pub]["certs"][x.id.hex()]
            mine = dms._my_list(pub)
            lst = dmp.DmDeviceList(identity=ident, seq=mine.seq + 1, devices=[*mine.devices, x.entry()])
            signed = dm.sign_device_list(key, lst)
            signed.added, signed.approver, signed.approval = x.id, dm.unb64(cert["approver"]), dm.unb64(cert["cert"])
            dms.apply_identity_sync({"pub": pub, "list": dm.b64(signed.SerializeToString())})
            assert x.id in {d.device_id for d in dms._my_list(pub).devices}
            # ... and is removed.
            await dms.remove_device(x.id.hex())
            after = dms._my_list(pub)
            assert x.id not in {d.device_id for d in after.devices} and x.id in set(after.removed)
            assert x.id.hex() not in dms.d["ids"][pub].get("certs", {})

            def attempt(devices, removed, approve_with, seq_add=1, added=b"", cert_from=None):
                cur = dms._my_list(pub)
                lst = dmp.DmDeviceList(identity=ident, seq=cur.seq + seq_add, devices=devices, removed=removed)
                s = dm.sign_device_list(key, lst)
                if added:
                    s.added, s.approver, s.approval = added, cert_from[0], cert_from[1]
                else:
                    approve_with.approve(s)
                dms.apply_identity_sync({"pub": pub, "list": dm.b64(s.SerializeToString())})
                return dms._my_list(pub)

            here = list(after.devices)
            # Its old keys, with or without the removal record.
            assert x.id not in {d.device_id for d in attempt([*here, x.entry()], list(after.removed), x).devices}
            assert x.id not in {d.device_id for d in attempt([*here, x.entry()], [], x).devices}
            # A fresh device of its own, approved by itself or by its old certificate.
            z = Device(tmp_path, "z", ident)
            assert z.id not in {d.device_id for d in attempt([*here, z.entry()], list(after.removed), z).devices}
            got = attempt([*here, z.entry()], list(after.removed), None, added=z.id, cert_from=(x.ed, x.certify(z.id)))
            assert z.id not in {d.device_id for d in got.devices}
            # Through its card (adopt_sync_card) as well.
            lst = dmp.DmDeviceList(identity=ident, seq=after.seq + 5, devices=[*here, x.entry()])
            card = dmp.ContactCard(identity=ident, devices=x.approve(dm.sign_device_list(key, lst)))
            dms._adopt_own_card(dm.open_card(dm.sign_card(key, card)))
            assert dms._my_list(pub).SerializeToString() == after.SerializeToString()
        finally:
            await core.shutdown()

    asyncio.run(go())
