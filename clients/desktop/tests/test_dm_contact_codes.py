"""Contact codes: a contact card handed from person to person, for DMs
without a shared community (docs/protocol.md §9.2 "Contact codes")."""

from __future__ import annotations

import pytest
from test_dm_device_lists import Device, make_list

from openarcanum import dm, keys
from openarcanum.errors import UserError
from openarcanum.keys import SigningKey
from openarcanum.wire import dm_pb2 as dmp


def make_card(tmp_path, member: bytes = b"", inboxes: bool = True) -> tuple[SigningKey, dmp.SignedContactCard]:
    key = SigningKey.generate()
    y = Device(tmp_path, "y", key.public)
    curve = y.olm.curve(y.label)
    signed_list, _ = make_list(key, 1, [y])
    kid, fk = y.olm.fallback_key(y.label)
    card = dmp.ContactCard(
        identity=key.public,
        member=member,
        name="Rita",
        devices=signed_list,
        fallback_keys=[
            dmp.OneTimeKey(
                curve25519=curve, key_id=kid, key=fk, fallback=True, signature=y.olm.signed_otk(y.label, kid, fk, True)
            )
        ],
        inboxes=[dmp.DeviceInbox(curve25519=curve, inbox=b"i" * 32)] if inboxes else [],
        issued_at=1000,
    )
    return key, dm.sign_card(key, card)


def code_of(signed: dmp.SignedContactCard) -> str:
    return dm.CONTACT_CODE_PREFIX + keys.encode(signed.SerializeToString())


def test_a_contact_code_opens_to_its_card(tmp_path):
    key, signed = make_card(tmp_path)
    code = code_of(signed)
    card = dm.parse_contact_code(code)
    assert card.card.identity == key.public and card.card.name == "Rita"
    # Pasted with line breaks or spaces around it.
    assert dm.parse_contact_code(f"  {code[:40]}\n{code[40:]}  ").card.identity == key.public


@pytest.mark.parametrize(
    "mangle",
    [
        lambda c: "",
        lambda c: c[len(dm.CONTACT_CODE_PREFIX) :],
        lambda c: c.replace("openarcanum-contact:", "openarcanum-invite:"),
        lambda c: c[:-10],
        lambda c: c[:60] + ("A" if c[60] != "A" else "B") + c[61:],
        lambda c: dm.CONTACT_CODE_PREFIX + "A" * dm.MAX_CONTACT_CODE,
    ],
)
def test_a_damaged_code_is_refused(tmp_path, mangle):
    _key, signed = make_card(tmp_path)
    with pytest.raises(UserError):
        dm.parse_contact_code(mangle(code_of(signed)))


def test_a_community_card_or_one_without_an_inbox_isnt_a_code(tmp_path):
    (tmp_path / "a").mkdir()
    (tmp_path / "b").mkdir()
    _key, signed = make_card(tmp_path / "a", member=b"m" * 32)
    with pytest.raises(UserError):
        dm.parse_contact_code(code_of(signed))
    _key, signed = make_card(tmp_path / "b", inboxes=False)
    with pytest.raises(UserError):
        dm.parse_contact_code(code_of(signed))
