"""Contact codes: a contact card handed from person to person, for DMs
without a shared community (docs/protocol.md §9.2 "Contact codes")."""

from __future__ import annotations

import asyncio

import pytest
from conftest import new_core
from test_dm_device_lists import Device, make_list

from openarcanum import dm, keys, meta
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


def test_a_permanent_code_round_trips_and_refuses_damage():
    pc = dm.PermanentCode(b"i" * 32, b"a" * 32, ["https://m1.example", "https://m2.example"])
    code = dm.encode_permanent_code(pc)
    assert code.startswith(dm.PERMANENT_CODE_PREFIX)
    assert dm.parse_permanent_code(f" {code[:30]}\n{code[30:]} ") == pc
    for bad in (
        "",
        code[:-8],
        dm.encode_permanent_code(dm.PermanentCode(b"i" * 32, b"a" * 32, [])),
        dm.encode_permanent_code(dm.PermanentCode(b"i" * 32, b"a" * 32, ["http://plain.example"])),
        dm.encode_permanent_code(dm.PermanentCode(b"i" * 32, b"a" * 32, ["https://m.example"] * 5)),
    ):
        with pytest.raises(UserError):
            dm.parse_permanent_code(bad)


class FakeNodes:
    """The maintainer nodes' contact cards, in memory."""

    def __init__(self):
        self.cards: dict[bytes, bytes] = {}

    def attach(self, core):
        core.meta.urls = ["https://m.example"]

        async def put(base, token, card):
            self.cards[meta.contact_card_address(token)] = card

        async def delete(base, token):
            self.cards.pop(meta.contact_card_address(token), None)

        async def get(urls, address):
            return self.cards.get(address)

        core.meta.card_put, core.meta.card_delete, core.meta.card_get = put, delete, get


def test_a_permanent_code_gives_the_current_card_until_it_is_revoked(tmp_path):
    async def go():
        nodes = FakeNodes()
        a, _ = await new_core(tmp_path / "a", "a", maintainers=[])
        b, _ = await new_core(tmp_path / "b", "b", maintainers=[])
        for c in (a, b):
            nodes.attach(c)
        try:
            await a.set_setting("display_name", "Ana")
            info = await a.dms.permanent_code()
            assert info == a.dms.permanent_code_info() and len(nodes.cards) == 1
            # The same code every time it's asked for.
            assert (await a.dms.permanent_code())["code"] == info["code"]
            added = await b.dms.add_contact_code(info["code"])
            contact = b.dms.d["contacts"][added["id"]]
            assert contact["name"] == "Ana" and contact["code"]["urls"] == ["https://m.example"]
            assert added["id"] == a.dms.d["contact_identity"]
            # A card replaced on the nodes is what the next reader gets.
            old = next(iter(nodes.cards.values()))
            await a.set_setting("display_name", "Ana B")
            await a.dms._publish_permanent()
            assert next(iter(nodes.cards.values())) != old
            card = await b.dms._fresh_card(added["id"])
            assert card.card.name == "Ana B"
            # Revoked: the code no longer adds anyone.
            await a.dms.revoke_permanent_code()
            assert not nodes.cards and a.dms.permanent_code_info() is None
            c, _ = await new_core(tmp_path / "c", "c", maintainers=[])
            nodes.attach(c)
            with pytest.raises(UserError):
                await c.dms.add_contact_code(info["code"])
            await c.shutdown()
            # A new code is a different one.
            assert (await a.dms.permanent_code())["code"] != info["code"]
        finally:
            await a.shutdown()
            await b.shutdown()

    asyncio.run(go())


def test_a_week_old_code_doesnt_start_a_conversation(tmp_path):
    async def go():
        a, _ = await new_core(tmp_path / "a", "a", maintainers=[])
        b, _ = await new_core(tmp_path / "b", "b", maintainers=[])
        try:
            added = await b.dms.add_contact_code(await a.dms.contact_code())
            b.dms.d["peers"][added["id"]]["card_at"] -= dm.CODE_FIRST_CONTACT_SECONDS + 60
            with pytest.raises(UserError, match="more than a week old"):
                await b.dms.open_with_contact(added["id"])
        finally:
            await a.shutdown()
            await b.shutdown()

    asyncio.run(go())
