"""Linked devices (docs/protocol.md §10).

Linking: the new device gets a nameplate (a small number pointing at a fresh
relay topic) from a maintainer node and shows a code like
"42-purple-sausage-anchor". The existing device types it, claims the
nameplate (once), and both run SPAKE2 over the topic with the code as the
password (the Magic Wormhole design). Key confirmation means a wrong code
cancels the link before anything is sent. Once approved on the existing
device, keys, communities, settings and DM state travel encrypted
under the SPAKE2 key.

Sync: a user's devices form a group like a DM group (`dm.py`), whose only
member is the profile's sync identity and whose devices are that identity's
signed device list. Sync events are Megolm events in that group; adding or
removing a device rotates its keys. Settings: latest change wins.
Communities and keys: union.

History copy: after linking, the new device asks the device it linked from
for message history one chunk at a time, over Olm. Each chunk is saved before
the next is requested, and the position is kept in the vault, so the copy
runs in the background, uses bounded memory, and resumes after interruption.
"""

from __future__ import annotations

import asyncio
import contextlib
import json
import logging
import math
import secrets
import time
import uuid
from typing import TYPE_CHECKING, Any

from spake2 import SPAKE2_Symmetric
from spake2.spake2 import SPAKEError

from openarcanum import crypto, keys
from openarcanum import settings as settings_mod
from openarcanum.community import Offline, RpcError
from openarcanum.errors import UserError
from openarcanum.i18n import _
from openarcanum.keys import SigningKey, StaticKey
from openarcanum.meta import MetaClient, MetaError
from openarcanum.paths import platform_name
from openarcanum.relay import Relay, RelayError
from openarcanum.wire import community_pb2 as cp

if TYPE_CHECKING:
    from openarcanum.core import Core

log = logging.getLogger(__name__)

LINK_ID = b"openarcanum/link/v1"
LINK_TIMEOUT = 300.0
CHUNK = 4 << 20
# History copy: bytes of JSON per chunk (each is one Olm message), and how
# long the new device waits for a chunk before asking again.
HISTORY_CHUNK = 48 << 10
HISTORY_RETRY = 30.0

# 282 short, distinct words: three of them carry about 24 bits on top of the
# nameplate. A wrong guess burns the nameplate, so one guess per code.
WORDS = """
acorn adult agent alarm album alert alley amber anchor angle ankle apple april apron arena armor arrow atlas
attic audio autumn avocado badge bagel baker bamboo banana banjo barrel basil basket beacon beaver berry
bicycle bison blanket blossom bonnet border bottle bounce bracket branch breeze brick bridge bronze bubble
bucket buffalo bugle bundle butter button cabin cactus camel candle canoe canvas canyon carbon carpet carrot
castle cedar cello cereal chalk cherry chimney circus citrus clover cobalt coconut comet copper coral cotton
cradle crayon cricket crystal cupcake curtain cushion dagger daisy dancer delta desert diamond dinner dolphin
donkey dragon drawer dynamo eagle easel eclipse elbow ember engine falcon feather fennel ferry fiddle finch
flannel flute forest fossil fountain fox galaxy garden garlic gazelle geyser ginger glacier globe goblin
gondola gopher granite grape gravel guitar hammer harbor harvest hazel helmet heron hickory honey hornet husky
igloo island ivory jacket jaguar jasmine jelly jigsaw jungle kayak kettle kitten koala ladder lagoon lantern
laurel lemon lentil lilac linen lizard lobster locket lotus magnet mango maple marble meadow melon mermaid
meteor mitten monsoon mosaic muffin mural nectar needle nickel noodle nugget oasis oatmeal ocean olive onion
opal orbit orchard otter oyster paddle pancake panda papaya parrot peanut pebble pelican pencil pepper piano
pickle pigeon pillow pilot pine plum pocket pony poppy potato prairie pretzel puffin pumpkin purple puzzle
quartz quill rabbit radish raven rhubarb ribbon river robin rocket saddle saffron salmon sandal sausage scarf
seashell sequoia shadow sherbet silver sketch sparrow spinach squirrel stencil sunflower swallow tango teapot
thistle thunder tiger timber toffee tomato topaz tractor trumpet tulip tundra turnip turtle umbrella valley
velvet violet walnut walrus wander whistle willow window wizard wombat yogurt zebra zephyr zigzag
""".split()  # noqa: SIM905

assert len(set(WORDS)) == len(WORDS)


# The most pieces a link transfer may come in (CHUNK each).
MAX_TRANSFER_PARTS = 256


def make_code(number: int, n_words: int = 3) -> str:
    return "-".join([str(number), *(secrets.choice(WORDS) for _i in range(n_words))])


def parse_code(code: str) -> tuple[int, str]:
    parts = [p for p in code.strip().lower().replace(" ", "-").split("-") if p]
    if len(parts) != 4 or not parts[0].isdigit():
        raise UserError(_("Link codes look like 42-purple-sausage-anchor."))
    if any(w not in WORDS for w in parts[1:]):
        raise UserError(_("One of the words isn't in the link word list; check the spelling."))
    return int(parts[0]), "-".join(parts)


class LinkSession:
    """One side of a link in progress."""

    def __init__(self, relay: Relay, topic: bytes):
        self.relay = relay
        self.topic = topic
        self.inbox: asyncio.Queue[dict] = asyncio.Queue()
        self.k_in = b""
        self.k_out = b""
        self.task: asyncio.Task | None = None
        self.state = "starting"
        self.peer: dict = {}
        self.transfer: dict | None = None

    async def open(self) -> None:
        await self.relay.subscribe(self.topic, self._on)

    def _on(self, _topic: bytes, data: bytes) -> None:
        with contextlib.suppress(ValueError, UnicodeDecodeError):
            msg = json.loads(data)
            if isinstance(msg, dict):
                self.inbox.put_nowait(msg)

    async def send(self, msg: dict) -> None:
        await self.relay.publish(self.topic, json.dumps(msg).encode())

    async def recv(self, kind: str, timeout: float = 120.0) -> dict:
        loop = asyncio.get_running_loop()
        deadline = loop.time() + timeout
        while True:
            remaining = deadline - loop.time()
            if remaining <= 0:
                raise UserError(_("The other device didn't answer in time. Start again with a new code."))
            msg = await asyncio.wait_for(self.inbox.get(), remaining)
            if msg.get("t") == "abort":
                raise UserError(msg.get("why") or _("The other device cancelled the link."))
            if msg.get("t") == kind:
                return msg

    def seal(self, obj: Any) -> str:
        return keys.encode(crypto.seal(self.k_out, json.dumps(obj).encode(), LINK_ID))

    def unseal(self, s: str) -> Any:
        return json.loads(crypto.open_sealed(self.k_in, keys.decode(s), LINK_ID))

    def set_keys(self, shared: bytes, i_am_new: bool) -> None:
        e2n = crypto.hkdf(shared, b"openarcanum/link/existing-to-new")
        n2e = crypto.hkdf(shared, b"openarcanum/link/new-to-existing")
        self.k_in, self.k_out = (e2n, n2e) if i_am_new else (n2e, e2n)

    async def close(self) -> None:
        if self.task is not None and self.task is not asyncio.current_task():
            self.task.cancel()
        await self.relay.close()


def _pake_finish(pake: SPAKE2_Symmetric, msg: dict) -> bytes:
    """The shared key from the other device's SPAKE2 message. Anyone on the
    link's relay topic can send one, so anything malformed fails the link
    with a reason instead of an internal error."""
    try:
        return pake.finish(keys.decode(msg["m"]))
    except KeyError, TypeError, AttributeError, ValueError, AssertionError, SPAKEError:
        raise UserError(_("The other device sent an unreadable link message. Start again with a new code.")) from None


def _transfer_part(sess: LinkSession, msg: dict) -> tuple[int, int, str] | None:
    """(index, count, text) of one sealed piece of the link transfer, or None
    for a message that isn't one from the other device (malformed, or sealed
    with another key: someone else on the topic), which is ignored."""
    i, n, box = msg.get("i"), msg.get("n"), msg.get("box")
    if any(type(x) is not int for x in (i, n)) or not 0 <= i < n <= MAX_TRANSFER_PARTS or not isinstance(box, str):
        return None
    try:
        text = sess.unseal(box)
    except crypto.DecryptError, ValueError, TypeError:
        return None
    return (i, n, text) if isinstance(text, str) else None


async def link_handshake_existing(sess: LinkSession, code: str) -> dict:
    """The existing device's half of the SPAKE2 exchange and the code check
    on an open link session. Returns what the new device says about itself;
    a wrong code or a malformed message raises UserError (after telling the
    other side, when the code was wrong)."""
    pake = SPAKE2_Symmetric(code.encode(), idSymmetric=LINK_ID)
    await sess.send({"t": "pake", "m": keys.encode(pake.start())})
    their = await sess.recv("pake", timeout=60)
    sess.set_keys(_pake_finish(pake, their), i_am_new=False)
    hello = await sess.recv("hello", timeout=60)
    try:
        peer = sess.unseal(hello["box"])
        if not isinstance(peer, dict):
            raise ValueError("not an object")
    except crypto.DecryptError, KeyError, TypeError, ValueError:
        await sess.send({"t": "abort", "why": _("The code didn't match. Start again with a new code.")})
        raise UserError(_("The code didn't match, so the link was cancelled and nothing was sent.")) from None
    await sess.send({"t": "confirm", "box": sess.seal("ok")})
    return peer


class DeviceManager:
    def __init__(self, core: Core):
        self.core = core
        self.link: LinkSession | None = None
        self._copy_task: asyncio.Task | None = None
        self._chunk_arrived = asyncio.Event()

    # ---------------------------------------------------------------- device table

    @property
    def group(self) -> dict:
        return self.core.vault.data["group"]

    @property
    def me(self) -> str:
        return self.core.vault.data["device_id"]

    def _ensure(self) -> None:
        v = self.core.vault
        assert v is not None
        changed = False
        if "group" not in v.data:
            v.data["group"] = {"devices": {}}
            changed = True
        devs = v.data["group"]["devices"]
        me = devs.get(self.me)
        entry = self._self_entry()
        if me != entry:
            devs[self.me] = {**(me or {}), **entry}
            changed = True
        if changed:
            self.core.save_vault()

    def _self_entry(self) -> dict:
        v = self.core.vault
        old = v.data.get("group", {}).get("devices", {}).get(self.me, {})
        return {
            "name": self.core.device_name,
            "platform": platform_name(),
            "linked_at": old.get("linked_at") or v.data.get("created", int(time.time())),
            "last_seen": int(time.time()),
            "community_keys": {
                cid: c["device"]["public"]
                for cid, c in v.data["communities"].items()
                if c.get("device", {}).get("public") and c.get("status") in ("active", "pending")
            },
        }

    async def opened(self) -> None:
        self._ensure()
        copy = self.core.vault.data.get("history_copy")
        if copy and not copy.get("done"):
            self._start_copy()

    def list(self):
        async def run():
            self._ensure()
            out = []
            for did, d in self.group["devices"].items():
                out.append(
                    {
                        "id": did,
                        "name": d.get("name", "Device"),
                        "platform": d.get("platform", ""),
                        "linked_at": d.get("linked_at", 0),
                        "last_seen": d.get("last_seen", 0),
                        "this": did == self.me,
                    }
                )
            out.sort(key=lambda d: (not d["this"], d["linked_at"]))
            return out

        return run()

    async def rename(self, device_id: str, name: str) -> dict:
        name = settings_mod.validate("device_name", name)
        if device_id == self.me:
            await self.core.set_setting("device_name", name)
            return {"ok": True}
        d = self.group["devices"].get(device_id)
        if d is None:
            raise UserError(_("Unknown device."))
        d["name"] = name
        self.core.save_vault()
        await self.broadcast("device_renamed", {"device": device_id, "name": name})
        return {"ok": True}

    async def renamed(self) -> None:
        if self.core.vault is None:
            return
        self._ensure()
        await self.broadcast("device_renamed", {"device": self.me, "name": self.core.device_name})

    # ---------------------------------------------------------------- events

    def _event(self, kind: str, data: dict) -> dict:
        return {"id": uuid.uuid4().hex, "from": self.me, "at": int(time.time() * 1000), "type": kind, "data": data}

    async def broadcast(self, kind: str, data: dict) -> None:
        """Send a sync event to every other linked device (live through the
        sync group's relay, and into each device's mailbox)."""
        if self.core.vault is None:
            return
        await self.core.dms.send_sync(self._event(kind, data))

    async def apply_event(self, event: dict) -> None:
        """A sync event from another of my devices (already authenticated as
        coming from a device in my own signed device list)."""
        v = self.core.vault
        if v is None or not isinstance(event, dict) or event.get("from") == self.me:
            return
        seen = v.data.setdefault("sync_seen", [])
        if event.get("id") in seen:
            return
        seen.append(event.get("id"))
        del seen[:-2000]
        src = self.group["devices"].get(event.get("from"))
        if src is not None:
            src["last_seen"] = max(src.get("last_seen", 0), int(event.get("at", 0)) // 1000)
        try:
            await self._apply(event)
        except Exception:
            log.exception("sync event %s failed", event.get("type"))
        self.core.save_vault()

    async def _apply(self, ev: dict) -> None:
        v = self.core.vault
        kind, data = ev["type"], ev["data"]
        if kind == "community_joined":
            cid = data["cid"]
            for iid, ident in data.get("identities", {}).items():
                v.data["identities"].setdefault(iid, ident)
            existing = v.data["communities"].get(cid)
            if existing and existing.get("status") in ("active", "pending"):
                return
            rec = dict(data["rec"])
            rec["device"] = self.core._new_device(rec["identity"])
            v.data["communities"][cid] = rec
            v.keys_changed()
            self.core.save_vault()
            self.core._start(cid)
            self.core.community_changed(cid)
            self.core.emit(
                "toast",
                {"text": _("Joined {name} from another device.").format(name=rec.get("name", _("a community")))},
            )
            await self.broadcast("device_keys", {"device": self.me, "keys": self._self_entry()["community_keys"]})
        elif kind == "owner_key":
            rec = v.data["communities"].get(data["cid"])
            if rec is not None and not rec.get("owner_identity") and data["iid"] != rec.get("owner_pending"):
                v.data["identities"].setdefault(data["iid"], data["identity"])
                rec["owner_pending"] = data["iid"]
                v.keys_changed()
                self.core.save_vault()
        elif kind == "community_left":
            if data["cid"] in v.data["communities"]:
                await self.core._forget(data["cid"], delete_key=data.get("delete_key", True))
        elif kind == "setting":
            times = v.data.setdefault("setting_times", {})
            if ev["at"] >= times.get(data["key"], 0):
                times[data["key"]] = ev["at"]
                self.core.apply_synced_setting(data["key"], data["value"])
        elif kind in ("device_linked", "device_info"):
            d = data["device"]
            self.group["devices"][d["id"]] = {k: d[k] for k in d if k != "id"}
            if kind == "device_linked":
                self.core.emit(
                    "notify",
                    {
                        "title": _("New device linked"),
                        "body": d.get("name") or _("Device"),
                        "community": "",
                        "community_name": _("Devices") if self.core.unlocked else "",
                        "channel": 0,
                    },
                )
            self.core.emit("devices", {})
        elif kind == "device_keys":
            d = self.group["devices"].setdefault(data["device"], {})
            d.setdefault("community_keys", {}).update(data["keys"])
        elif kind == "device_renamed":
            d = self.group["devices"].get(data["device"])
            if d is not None:
                d["name"] = data["name"]
            if data["device"] == self.me:
                # Renamed from another device: this device takes the name (its
                # own entry is rebuilt from the device_name setting).
                with contextlib.suppress(settings_mod.SettingError):
                    self.core.vault.settings["device_name"] = settings_mod.validate("device_name", data["name"])
                    self.core.save_vault()
                    self.core.emit("profile", self.core.profile_status())
            self.core.emit("devices", {})
        elif kind == "device_removed":
            if data["device"] == self.me:
                await self.core.wipe_device(_("This device was removed from your account on another device."))
                return
            self.group["devices"].pop(data["device"], None)
            self.core.emit("devices", {})
        elif kind == "dm_identity":
            self.core.dms.apply_identity_sync(data)
        elif kind == "dm_group":
            self.core.dms.apply_group_sync(data)
        elif kind == "dm_forget":
            self.core.dms.apply_forget_sync(data["id"])
        elif kind == "dm_read":
            self.core.dms.apply_read_sync(data["id"], int(data["at"]))
        elif kind == "dm_blocked":
            self.core.dms.apply_blocked_sync(data["blocked"])
        elif kind == "dm_verified":
            self.core.dms.apply_verified_sync(data["verified"])
        elif kind == "contact_added":
            self.core.dms.d["contacts"][data["id"]] = data["contact"]
            self.core.emit("dm_contacts", {})
        elif kind == "contact_removed":
            self.core.dms.d["contacts"].pop(data["id"], None)
            self.core.emit("dm_contacts", {})
        elif kind in ("status", "status_idle"):
            # My status, set on another device (statuses.py).
            self.core.statuses.apply_sync(kind, data)
        elif kind == "history_request":
            if data.get("to") == self.me:
                await self._send_history_chunk(ev["from"], data)
        elif kind == "history_chunk":
            if data.get("to") == self.me:
                self._on_history_chunk(data)

    # ---------------------------------------------------------------- hooks from the core

    async def community_joined(self, cid: str) -> None:
        v = self.core.vault
        rec = v.data["communities"].get(cid)
        if rec is None or rec.get("local_only"):
            return
        idents = {
            iid: v.data["identities"][iid]
            for iid in (rec.get("identity"), rec.get("owner_identity"), rec.get("owner_pending"))
            if iid and iid in v.data["identities"]
        }
        shared = {k: rec[k] for k in rec if k != "device"}
        await self.broadcast("community_joined", {"cid": cid, "rec": shared, "identities": idents})
        await self.broadcast("device_keys", {"device": self.me, "keys": self._self_entry()["community_keys"]})

    async def owner_key_made(self, cid: str, iid: str) -> None:
        """This device generated an owner key for a community (an owner
        invitation it accepted): the profile's other devices keep a copy, so the
        key isn't lost with this device."""
        v = self.core.vault
        ident = v.data["identities"].get(iid)
        if ident is None:
            return
        await self.broadcast("owner_key", {"cid": cid, "iid": iid, "identity": ident})

    async def community_left(self, cid: str) -> None:
        await self.broadcast("community_left", {"cid": cid})

    async def setting_changed(self, key: str, value: Any) -> None:
        s = settings_mod.BY_KEY.get(key)
        # Device name, voice devices and the like are per device (and preferences,
        # such as the maintainer nodes, aren't synced at all).
        if s is None or s.store != "vault" or key in settings_mod.PER_DEVICE:
            return
        self.core.vault.data.setdefault("setting_times", {})[key] = int(time.time() * 1000)
        # Kept with the value: an older value from another device must not
        # win after a restart, and a value the user chose must not look
        # like a default to a migration (core._migrate_voice_sensitivity).
        self.core.save_vault()
        await self.broadcast("setting", {"key": key, "value": value})

    # ---------------------------------------------------------------- removal

    async def remove(self, device_id: str) -> dict:
        v = self.core.require_unlocked()
        if device_id == self.me:
            raise UserError(_("You can't remove this device from itself. Remove it from another device."))
        target = self.group["devices"].get(device_id)
        if target is None:
            raise UserError(_("Unknown device."))
        # 1. Every community refuses that device's key from now on. Each
        # revocation is signed now and kept until its community accepts it:
        # one that can't be reached gets it when it next connects
        # (send_pending_revocations), so a removed device never stays
        # admitted somewhere.
        failures = []
        pending = v.data.setdefault("pending_revocations", [])
        for cid, dev_pub in target.get("community_keys", {}).items():
            rec = v.data["communities"].get(cid)
            if rec is None:
                continue
            ident = self.core.identity_key(rec["identity"])
            raw = cp.DeviceRevocation(
                community=keys.decode(rec["owner_key"]),
                identity_key=ident.public,
                device_key=keys.decode(dev_pub),
                at=int(time.time() * 1000),
            ).SerializeToString()
            signed = cp.SignedDeviceRevocation(revocation=raw, signature=ident.sign(keys.CTX_DEVICE_REVOCATION, raw))
            item = {"community": cid, "signed": keys.encode(signed.SerializeToString())}
            pending.append(item)
            self.core.save_vault()
            error = await self._send_revocation(cid, signed)
            if error is None and item in pending:
                pending.remove(item)
                self.core.save_vault()
            else:
                failures.append(
                    _("{community}: {error} (it's sent when the community is reachable)").format(
                        community=rec.get("name", cid[:8]), error=error
                    )
                )
        # 2. Tell the device (so it wipes itself if it's still around), then
        # drop it from every device list: every group rotates its keys, so it
        # receives nothing new, DMs or sync.
        await self.broadcast("device_removed", {"device": device_id})
        await self.core.dms.remove_device(device_id)
        self.group["devices"].pop(device_id, None)
        self.core.save_vault()
        self.core.emit("devices", {})
        return {"ok": True, "failures": failures}

    async def _send_revocation(self, cid: str, signed: cp.SignedDeviceRevocation) -> str | None:
        """Send one device revocation; the error text if it didn't go through."""
        conn = self.core.conns.get(cid)
        if conn is None or conn.status != "online":
            return _("not connected")
        try:
            await conn.call(revoke_device=signed)
        except (RpcError, Offline) as e:
            return str(e)
        return None

    async def send_pending_revocations(self, cid: str) -> None:
        """A community came online: send the revocations it hasn't got yet."""
        v = self.core.vault
        if v is None:
            return
        pending = v.data.get("pending_revocations", [])
        for item in [p for p in pending if p["community"] == cid]:
            if item not in pending:
                continue
            signed = cp.SignedDeviceRevocation.FromString(keys.decode(item["signed"]))
            error = await self._send_revocation(cid, signed)
            if error is None:
                pending.remove(item)
                self.core.save_vault()
            else:
                log.info("device revocation for a community still pending: %s", error)

    # ---------------------------------------------------------------- linking: new device

    async def new_device_start(self, device_name: str) -> dict:
        """Through this device's own maintainer nodes (preferences, changed on
        the link screen with Change…): maintainer lists are per device."""
        if self.core.vault is not None:
            raise UserError(_("This device already has a profile."))
        name = settings_mod.validate("device_name", device_name or f"{platform_name()} device")
        await self.cancel()
        meta = MetaClient(list(self.core.meta.urls))
        try:
            relays = [r["url"] for r in await meta.relays()]
        except MetaError as e:
            raise UserError(_("Can't reach a maintainer node: {error}").format(error=e)) from None
        if not relays:
            raise UserError(_("The maintainer nodes don't list any relays right now."))
        relay_url = sorted(relays)[0]
        try:
            plate = await meta.nameplate(relay_url)
            relay = await Relay(plate.relay).connect()
        except (MetaError, RelayError) as e:
            raise UserError(_("Couldn't start linking: {error}").format(error=e)) from None
        code = make_code(plate.number)
        sess = LinkSession(relay, bytes(plate.topic))
        await sess.open()
        sess.peer = {"name": name}
        self.link = sess
        sess.task = self.core.spawn(self._new_device_run(sess, code, name))
        return {"code": code, "expires_at": plate.expires_at}

    async def _new_device_run(self, sess: LinkSession, code: str, name: str) -> None:
        try:
            their = await sess.recv("pake", timeout=LINK_TIMEOUT)
            pake = SPAKE2_Symmetric(code.encode(), idSymmetric=LINK_ID)
            mine = pake.start()
            await sess.send({"t": "pake", "m": keys.encode(mine)})
            sess.set_keys(_pake_finish(pake, their), i_am_new=True)
            await sess.send({"t": "hello", "box": sess.seal({"name": name, "platform": platform_name()})})
            confirm = await sess.recv("confirm", timeout=60)
            try:
                if sess.unseal(confirm["box"]) != "ok":
                    raise crypto.DecryptError("bad confirm")
            except crypto.DecryptError, KeyError, TypeError, ValueError:
                raise UserError(_("The code didn't match. Nothing was sent; start again with a new code.")) from None
            sess.state = "waiting_approval"
            self.core.emit("link", {"stage": "waiting_approval"})
            parts: dict[int, str] = {}
            total = None
            while total is None or any(i not in parts for i in range(total)):
                part = _transfer_part(sess, await sess.recv("transfer", timeout=LINK_TIMEOUT))
                if part is not None:
                    i, total, text = part
                    parts[i] = text
            try:
                transfer = sess.unseal("".join(parts[i] for i in range(total)))
            except crypto.DecryptError, KeyError, ValueError:
                raise UserError(
                    _("The transfer from the other device was damaged. Start again with a new code.")
                ) from None
            if not isinstance(transfer, dict) or not isinstance(transfer.get("communities"), dict):
                raise UserError(_("The transfer from the other device was damaged. Start again with a new code."))
            sess.transfer = transfer
            sess.state = "received"
            self.core.emit(
                "link",
                {
                    "stage": "received",
                    "communities": [c.get("name", "") for c in transfer["communities"].values()],
                    # Conversations the user sees (not the devices' own sync group).
                    "dms": sum(
                        1
                        for g in transfer.get("dm", {}).get("groups", {}).values()
                        if isinstance(g, dict) and not g.get("sync")
                    ),
                    "from": transfer.get("from_name", ""),
                },
            )
        except asyncio.CancelledError:
            raise
        except (UserError, RelayError, crypto.DecryptError, TimeoutError) as e:
            sess.state = "failed"
            self.core.emit("link", {"stage": "failed", "error": str(e) or _("The link failed.")})
        except Exception:
            log.exception("link failed")
            sess.state = "failed"
            self.core.emit("link", {"stage": "failed", "error": _("The link failed.")})

    async def new_device_finish(self, passphrase: str) -> dict:
        sess = self.link
        if sess is None or sess.transfer is None:
            raise UserError(_("Nothing has been received from the other device yet."))
        from openarcanum.session import sign_static
        from openarcanum.vault import Vault, new_vault_data

        settings_mod.check_new_passphrase(passphrase)
        t = sess.transfer
        name = sess.peer.get("name") or f"{platform_name()} device"
        vd = new_vault_data(name)
        did = t.get("device_id")
        if isinstance(did, str) and len(did) == 16 and all(c in "0123456789abcdef" for c in did):
            # Certified by the other device for the DM device lists.
            vd["device_id"] = did
        vd["identities"] = t["identities"]
        for k, val in t.get("settings", {}).items():
            if (
                k in settings_mod.BY_KEY
                and settings_mod.BY_KEY[k].store == "vault"
                and k not in settings_mod.PER_DEVICE
            ):
                vd["settings"][k] = val
        vd["group"] = {"devices": t["group"]["devices"]}
        for cid, rec in t["communities"].items():
            rec = dict(rec)
            ident = vd["identities"][rec["identity"]]
            k = SigningKey(keys.decode(ident["seed"]))
            st = StaticKey.generate()
            rec["device"] = {
                "static": keys.encode(st.private),
                "public": keys.encode(st.public),
                "sig": keys.encode(sign_static(k, st.public)),
            }
            vd["communities"][cid] = rec
        vd["backup_revision"] = vd["keys_revision"] = 1
        copy = t.get("copy", {})
        if copy.get("dms") or copy.get("communities"):
            vd["history_copy"] = {
                "from": t["from_device"],
                "dms": bool(copy.get("dms")),
                "communities": list(copy.get("communities", [])),
                "cursor": None,
                "done": False,
                "received": 0,
            }
        vd["dm"] = self.core.dms.import_from_link(t.get("dm", {}))
        async with self.core._open_lock:
            if self.core.vault is not None:
                raise UserError(_("A profile is already open."))
            self.core.vault = await asyncio.to_thread(Vault.create, self.core.paths, passphrase, name, vd)
            await self.core._opened()
            await self.core._new_profile_signed_in()
        self._ensure()
        entry = {"id": self.me, **self.group["devices"][self.me]}
        await self.core.dms.after_link()
        await sess.send({"t": "done", "box": sess.seal({"entry": entry, "card": self.core.dms.sync_card()})})
        await self.broadcast("device_linked", {"device": entry})
        await sess.close()
        self.link = None
        return self.core.profile_status()

    # ---------------------------------------------------------------- linking: existing device

    async def existing_enter_code(self, code: str) -> dict:
        self.core.require_unlocked()
        number, code = parse_code(code)
        await self.cancel()
        plate = None
        last: Exception | None = None

        # In a maintainer cluster the code's last digit names the member that
        # handed it out (docs/protocol.md §7.5): that one first.
        def index_of(url: str) -> int | None:
            at = self.core.clusters.index_of(url)
            return at[1] if at else None

        for base in self.core.meta.nameplate_order(number, index_of):
            try:
                plate = await MetaClient([base]).claim_nameplate(number)
                break
            except MetaError as e:
                last = e
                if e.status == 410:
                    raise UserError(
                        _("That code has expired or was already used. Get a new code on the new device.")
                    ) from None
        if plate is None:
            raise UserError(
                _("That code isn't known (it may have expired).")
                if isinstance(last, MetaError) and last.status == 404
                else _("Can't reach a maintainer node: {error}").format(error=last)
            )
        try:
            relay = await Relay(plate.relay).connect()
        except RelayError as e:
            raise UserError(_("Can't reach the server used for linking: {error}").format(error=e)) from None
        sess = LinkSession(relay, bytes(plate.topic))
        self.link = sess
        try:
            await sess.open()
            peer = await link_handshake_existing(sess, code)
        except BaseException:
            await self.cancel()
            raise
        sess.peer = peer
        sess.state = "approve"
        v = self.core.vault
        communities = []
        for cid, rec in v.data["communities"].items():
            if rec.get("status") not in ("active", "pending"):
                continue
            size = sum(len(json.dumps(m)) for msgs in self.core.cache(cid).messages.values() for m in msgs)
            communities.append(
                {
                    "id": cid,
                    "name": rec.get("name", ""),
                    "local_only": bool(rec.get("local_only")),
                    "messages_size": size,
                }
            )
        return {
            "device_name": str(peer.get("name", ""))[:64],
            "platform": str(peer.get("platform", ""))[:32],
            "communities": communities,
            "dm_size": self.core.dms.history_size(),
            "warning": (
                _("Only continue if you are setting up your own device right now. Never enter a code someone sent you.")
            ),
        }

    async def existing_approve(self, choices: dict) -> dict:
        sess = self.link
        if sess is None or sess.state != "approve":
            raise UserError(_("No link is waiting for approval."))
        v = self.core.require_unlocked()
        chosen = [c for c in choices.get("communities", []) if c in v.data["communities"]]
        copy_dms = bool(choices.get("copy_dms", True))
        copy_messages = bool(choices.get("copy_messages", False))
        self._ensure()
        await self.core.dms.ensure_sync()
        idents = {}
        recs = {}
        for cid in chosen:
            rec = v.data["communities"][cid]
            recs[cid] = {k: rec[k] for k in rec if k != "device"}
            for iid in (rec.get("identity"), rec.get("owner_identity"), rec.get("owner_pending")):
                if iid and iid in v.data["identities"]:
                    idents[iid] = v.data["identities"][iid]
        # The new device's id, chosen here so this device can certify it to
        # join each DM identity's device list (dm.check_list_change).
        new_id = secrets.token_hex(8)
        self.core.dms.certify_device(new_id)
        dm_state = self.core.dms.export_for_link()
        for e in dm_state["ids"].values():
            if e["iid"] in v.data["identities"]:
                idents[e["iid"]] = v.data["identities"][e["iid"]]
        transfer = {
            "from_name": self.core.device_name,
            "from_device": self.me,
            "device_id": new_id,
            "identities": idents,
            "communities": recs,
            "settings": dict(v.settings),
            "group": {"devices": self.group["devices"]},
            "dm": dm_state,
            "copy": {"dms": copy_dms, "communities": chosen if copy_messages else []},
        }
        box = sess.seal(transfer)
        n = max(1, math.ceil(len(box) / CHUNK))
        for i in range(n):
            await sess.send({"t": "transfer", "i": i, "n": n, "box": sess.seal(box[i * CHUNK : (i + 1) * CHUNK])})
        try:
            done = await sess.recv("done", timeout=LINK_TIMEOUT)
            reply = sess.unseal(done["box"])
            entry = dict(reply["entry"])
        except (UserError, crypto.DecryptError, KeyError, TypeError) as e:
            await self.cancel()
            raise UserError(_("The new device didn't finish: {error}").format(error=e)) from None
        # The new device's sync card: from now on its sync events and keys verify.
        self.core.dms.adopt_sync_card(reply.get("card", ""))
        did = entry.pop("id")
        self.group["devices"][did] = entry
        self.core.save_vault()
        await self.broadcast("device_info", {"device": {"id": did, **entry}})
        self.core.emit(
            "notify",
            {
                "title": _("New device linked"),
                "body": entry.get("name") or _("Device"),
                "community": "",
                "community_name": _("Devices") if self.core.unlocked else "",
                "channel": 0,
            },
        )
        self.core.emit("devices", {})
        await sess.close()
        self.link = None
        return {"ok": True, "device": entry.get("name", "")}

    async def cancel(self) -> dict:
        sess, self.link = self.link, None
        if sess is not None:
            with contextlib.suppress(Exception):
                await sess.send({"t": "abort", "why": _("The other device cancelled the link.")})
            await sess.close()
        return {"ok": True}

    # ---------------------------------------------------------------- history copy (docs/protocol.md §10.3)

    def copy_status(self) -> dict:
        c = self.core.vault.data.get("history_copy") if self.core.vault else None
        if not c:
            return {"active": False, "done": False, "received": 0}
        return {"active": not c.get("done"), "done": bool(c.get("done")), "received": c.get("received", 0)}

    def _start_copy(self) -> None:
        if self._copy_task is None or self._copy_task.done():
            self._copy_task = self.core.spawn(self._copy_loop())

    async def _copy_loop(self) -> None:
        """New device: ask for the next chunk until done. A chunk that doesn't
        arrive in time is asked for again, from the last saved position."""
        while True:
            c = self.core.vault.data.get("history_copy") if self.core.vault else None
            if not c or c.get("done"):
                return
            self._chunk_arrived.clear()
            req = {"to": c["from"], "cursor": c["cursor"], "dms": c["dms"], "communities": c["communities"]}
            try:
                await self.core.dms.send_sync_to(c["from"], self._event("history_request", req))
            except UserError as e:
                log.info("history copy: request not sent: %s", e)
            with contextlib.suppress(TimeoutError):
                await asyncio.wait_for(self._chunk_arrived.wait(), HISTORY_RETRY)

    def _on_history_chunk(self, data: dict) -> None:
        c = self.core.vault.data.get("history_copy")
        if not c or c.get("done") or data.get("cursor") != c.get("cursor"):
            return  # a duplicate or stale answer
        added = 0
        touched: set[str] = set()
        for item in data.get("items", []):
            kind, key, msg = item.get("k"), item.get("id"), item.get("m")
            if not isinstance(msg, dict):
                continue
            if kind == "dm" and c["dms"]:
                added += self.core.dms.history_import(key, [msg])
            elif kind == "c" and key in c["communities"] and key in self.core.vault.data["communities"]:
                self.core.cache(key).upsert(msg)
                touched.add(key)
                added += 1
        for key in touched:
            self.core.cache(key).save()
        c["received"] = c.get("received", 0) + added
        c["cursor"] = data.get("next")
        c["done"] = data.get("next") is None
        self.core.save_vault()
        self._chunk_arrived.set()
        self.core.emit("history_copy", self.copy_status())
        if c["done"]:
            self.core.emit("toast", {"text": _("Message history copied from your other device.")})

    def _history_items(self, dms: bool, communities: list[str]):
        """Everything to copy, in a stable order, as (key, item). The key
        names the item itself, not its place in a list: DM messages by
        ["dm", gid, at, id], community messages by ["c", cid, channel, id].
        Keys only grow along the order, so "resume after this key" holds
        when messages are purged, evicted or re-sorted between chunks."""
        if dms:
            hist = self.core.dms.history_export()
            for gid in sorted(hist):
                keyed = [(["dm", gid, _num(m.get("at")), str(m.get("id", ""))], m) for m in hist[gid]]
                keyed.sort(key=lambda km: (km[0][2], km[0][3]))
                for key, m in keyed:
                    yield key, {"k": "dm", "id": gid, "m": m}
        for cid in sorted(communities):
            if cid not in self.core.vault.data["communities"]:
                continue
            keyed = [
                (["c", cid, _int(m.get("channel")), _int(m.get("id"))], m)
                for ms in self.core.cache(cid).messages.values()
                for m in ms
            ]
            keyed.sort(key=lambda km: (km[0][2], km[0][3]))
            for key, m in keyed:
                yield key, {"k": "c", "id": cid, "m": m}

    def _chunk_after(self, cursor: list | None, dms: bool, communities: list[str]) -> tuple[list[dict], list | None]:
        """Items strictly after `cursor` (the key of the last item already
        received) up to the chunk size, and the new cursor (None when done).
        The cursor needn't still exist: a purged or evicted item is passed
        over by its key. A malformed cursor restarts the copy; the new
        device ignores items it already has."""
        after = _cursor_order(cursor)
        items: list[dict] = []
        size = 0
        last = None
        for key, item in self._history_items(dms, communities):
            if after is not None and _cursor_order(key) <= after:
                continue
            n = len(json.dumps(item))
            if items and size + n > HISTORY_CHUNK:
                return items, last
            items.append(item)
            size += n
            last = key
        return items, None

    async def _send_history_chunk(self, to: str, req: dict) -> None:
        """Existing device: answer one request with one chunk."""
        items, nxt = self._chunk_after(req.get("cursor"), bool(req.get("dms")), list(req.get("communities", [])))
        chunk = {"to": to, "cursor": req.get("cursor"), "items": items, "next": nxt}
        await self.core.dms.send_sync_to(to, self._event("history_chunk", chunk))

    async def shutdown(self) -> None:
        if self._copy_task is not None:
            self._copy_task.cancel()
        await self.cancel()


def _num(v: Any) -> float:
    return v if isinstance(v, (int, float)) and not isinstance(v, bool) and math.isfinite(v) else 0


def _int(v: Any) -> int:
    return v if isinstance(v, int) and not isinstance(v, bool) else 0


def _cursor_order(cursor: Any) -> tuple | None:
    """A history-copy cursor as a comparable tuple (DM items before community
    items), or None for "from the start" (no cursor, or one that isn't a
    well-formed key, such as a positional cursor from an older version)."""
    if not isinstance(cursor, list) or len(cursor) != 4:
        return None
    kind, key, a, b = cursor
    if not isinstance(key, str):
        return None
    if kind == "dm" and isinstance(b, str) and _num(a) is a:
        return (0, key, a, b)
    if kind == "c" and _int(a) is a and _int(b) is b:
        return (1, key, a, b)
    return None
