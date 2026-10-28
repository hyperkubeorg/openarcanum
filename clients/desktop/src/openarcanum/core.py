"""The client core: profile, communities, messages, settings.

Runs on the background asyncio thread. The UI talks to it through api.py;
it talks back through `emit(event, payload)` (bus.py). Nothing here touches
the webview or Qt.
"""

from __future__ import annotations

import asyncio
import base64
import contextlib
import hashlib
import json
import logging
import shutil
import time
from collections.abc import Callable
from pathlib import Path
from typing import Any

from openarcanum import PRODUCT_NAME, __version__, crashlog, hotkey, keychain, keys
from openarcanum import settings as settings_mod
from openarcanum.cache import CommunityCache, enforce_limit
from openarcanum.codes import CodeError, encode_invite, parse_claim, parse_invite, parse_move
from openarcanum.community import (
    MESSAGE_KINDS,
    REFUSALS,
    CommunityConnection,
    Offline,
    RpcError,
    Session2,
    file_chunk_aad,
    file_chunk_nonce,
    make_verifier,
    message_dict,
    update_dict,
)
from openarcanum.crypto import DecryptError, decrypt_with_nonce
from openarcanum.directory import LocalDirectory
from openarcanum.errors import UserError
from openarcanum.hosting import HostManager, binary_name, binary_platform, community_binary, copy_binary
from openarcanum.i18n import N_, _
from openarcanum.keys import SigningKey, StaticKey
from openarcanum.messaging import (
    MAX_ATTACHMENTS,
    channel_counts,
    encode_mentions,
    mentions_me,
)
from openarcanum.meta import MetaClient, MetaError, MetaVersionError
from openarcanum.nodes import REFRESH_SECONDS, Clusters, parse_entry
from openarcanum.owners import (
    MAX_OWNERS,
    OwnerSet,
    issue_cert,
    request_owner_key,
    sign_change,
    verify_chain,
    verify_owner_add,
)
from openarcanum.paths import ENGINE_SANDBOX, Paths, atomic_write, move_tree, platform_name, read_at_most
from openarcanum.permissions import (
    FEATURES,
    PERMISSIONS,
    catalog,
    channel_features,
    features,
    has,
    names,
)
from openarcanum.session import IdentityError, sign_static
from openarcanum.settings import Preferences, SettingError, check_new_passphrase
from openarcanum.updates import Updater
from openarcanum.vault import MAGIC as VAULT_MAGIC
from openarcanum.vault import Backoff, StateStore, UnlockGuard, Vault, VaultError, WrongPassphrase, open_with_passphrase
from openarcanum.wire import community_pb2 as cp
from openarcanum.wire import invite_pb2 as ip
from openarcanum.wire import session_pb2 as sp

log = logging.getLogger(__name__)

# How often the maintainer nodes are asked whether they serve the public
# directory (also after the list changes).
DIRECTORY_CHECK_SECONDS = 600

MAX_FILE = 25 * 1024 * 1024
MAX_BACKUP_BYTES = 4 * 1024 * 1024  # a backup holds keys and a community list: kilobytes
# state.bin (drafts, the last open channel and the like): at most this many
# keys, each value at most this big as JSON.
MAX_UI_STATE_KEYS = 256
MAX_UI_STATE_BYTES = 64 * 1024
# Time limit for each step of Core.shutdown (app.py allows 5 s for all of it).
SHUTDOWN_STEP_SECONDS = 1.0
# How long hosted servers get to stop cleanly at quit (they tell the
# maintainers they're going offline), and how long a new hosted server may
# take to be reachable for its claim.
HOSTING_STOP_SECONDS = 3.0
HOST_CLAIM_SECONDS = 45.0
NOTICE = N_("Admins of this community can read everything you post here.")


def _enum_name(enum, value: int, prefix: str) -> str:
    try:
        return enum.Name(value).removeprefix(prefix).lower()
    except ValueError:
        return "unspecified"


def _network_block(value: int) -> str:
    """A NetworkBlockStatus as "blocked", "pending", "skipped", or "" for none."""
    name = _enum_name(cp.NetworkBlockStatus, value, "NETWORK_BLOCK_STATUS_")
    return "" if name == "unspecified" else name


def settings_to_dict(s: cp.CommunitySettings) -> dict:
    return {
        "name": s.name,
        "description": s.description,
        "icon": base64.b64encode(s.icon).decode() if s.icon else "",
        "language": s.language,
        "tags": list(s.tags),
        "visibility": _enum_name(cp.Visibility, s.visibility, "VISIBILITY_"),
        "join_policy": _enum_name(cp.JoinPolicy, s.join_policy, "JOIN_POLICY_"),
        "audit_visibility": _enum_name(cp.AuditVisibility, s.audit_visibility, "AUDIT_VISIBILITY_"),
        "message_retention_days": s.message_retention_days,
        "file_retention_days": s.file_retention_days,
        "search_enabled": s.search_enabled,
        "max_file_size": s.max_file_size,
        "file_bandwidth_limit": s.file_bandwidth_limit,
        "relay_mode": _enum_name(cp.RelayMode, s.relay_mode, "RELAY_MODE_"),
        "pinned_relays": list(s.pinned_relays),
        "max_devices_per_member": s.max_devices_per_member,
        "allow_contact_cards": s.allow_contact_cards,
        "max_members": s.max_members,
        # Feature toggles by name (docs/protocol.md §4.6).
        "features": features(s),
        "attachment_kinds": "images_only" if s.attachment_kinds == cp.ATTACHMENT_KINDS_IMAGES_ONLY else "all",
        "join_leave_channel": s.join_leave_channel,
        "screening_minutes": s.screening_minutes,
        "max_custom_emoji": s.max_custom_emoji,
    }


def _enum_value(enum, prefix: str, name: str) -> int:
    try:
        return enum.Value(prefix + str(name).upper())
    except ValueError:
        raise UserError(_("unknown value {value}").format(value=repr(name))) from None


def settings_from_dict(d: dict, base: cp.CommunitySettings | None = None) -> cp.CommunitySettings:
    s = cp.CommunitySettings()
    if base is not None:
        s.CopyFrom(base)
    simple = {
        "name": str,
        "description": str,
        "language": str,
        "message_retention_days": int,
        "file_retention_days": int,
        "search_enabled": bool,
        "max_file_size": int,
        "file_bandwidth_limit": int,
        "max_devices_per_member": int,
        "allow_contact_cards": bool,
        "max_members": int,
        "screening_minutes": int,
        "max_custom_emoji": int,
    }
    for k, typ in simple.items():
        if k in d:
            v = d[k]
            if typ is int:
                if isinstance(v, bool) or not isinstance(v, int | float | str) or str(v).strip() == "":
                    raise UserError(_("{setting} must be a number").format(setting=k.replace("_", " ")))
                try:
                    v = int(v)
                except ValueError:
                    raise UserError(_("{setting} must be a number").format(setting=k.replace("_", " "))) from None
                if v < 0:
                    raise UserError(_("{setting} can't be negative").format(setting=k.replace("_", " ")))
            setattr(s, k, typ(v))
    if "tags" in d:
        tags = d["tags"]
        if isinstance(tags, str):
            tags = [t.strip() for t in tags.split(",") if t.strip()]
        del s.tags[:]
        s.tags.extend(tags)
    if "pinned_relays" in d:
        rel = d["pinned_relays"]
        if isinstance(rel, str):
            rel = [t.strip() for t in rel.replace("\n", ",").split(",") if t.strip()]
        del s.pinned_relays[:]
        s.pinned_relays.extend(rel)
    if "icon" in d:
        s.icon = base64.b64decode(d["icon"]) if d["icon"] else b""
    if "features" in d:
        fields = {n: field for n, field, _default, _ch in FEATURES}
        for name, on in dict(d["features"]).items():
            if name not in fields:
                raise UserError(_("unknown feature {feature}").format(feature=repr(name)))
            if not isinstance(on, bool):
                raise UserError(_("{feature} must be on or off").format(feature=name.replace("_", " ")))
            setattr(s, fields[name], on)
    if "attachment_kinds" in d:
        kinds = {"all": cp.ATTACHMENT_KINDS_ALL, "images_only": cp.ATTACHMENT_KINDS_IMAGES_ONLY}
        if d["attachment_kinds"] not in kinds:
            raise UserError(_("allowed attachments are all or images_only"))
        s.attachment_kinds = kinds[d["attachment_kinds"]]
    if "join_leave_channel" in d:
        try:
            s.join_leave_channel = int(d["join_leave_channel"] or 0)
        except TypeError, ValueError:
            raise UserError(_("pick a channel for join and leave messages")) from None
    for field, enum, prefix in (
        ("visibility", cp.Visibility, "VISIBILITY_"),
        ("join_policy", cp.JoinPolicy, "JOIN_POLICY_"),
        ("audit_visibility", cp.AuditVisibility, "AUDIT_VISIBILITY_"),
        ("relay_mode", cp.RelayMode, "RELAY_MODE_"),
    ):
        if field in d:
            setattr(s, field, _enum_value(enum, prefix, d[field]))
    return s


def _rpc_message(e: Exception) -> str:
    return str(e) or e.__class__.__name__


def _owner_error(msg: str) -> str:
    """Owner-set refusals (from owners.py or the server) in the UI's words."""
    if "last owner" in msg:
        return _("You can't remove the last owner. Add another owner first.")
    if "already an owner" in msg:
        return _("That key is or was already an owner.")
    if not msg:
        return _("The owner change was refused.")
    # owners.py's refusals are in the catalog (N_); the server's are shown as sent.
    shown = _(msg)
    return shown[:1].upper() + shown[1:]


class Core:
    def __init__(self, paths: Paths, emit: Callable[[str, dict], None] | None = None):
        self.base_paths = paths
        self.prefs = Preferences(paths.preferences)
        self.paths = paths.with_data_location(Path(d) if (d := self.prefs.values.get("data_dir")) else None)
        # The chosen folder is only used while it holds the data: a drive
        # that isn't plugged in must not get an empty profile created on
        # its mount point. The setting stays; the folder may come back.
        self._data_dir_missing = ""
        if d and not self.paths.data.is_dir():
            log.warning("the data folder %s isn't available; using the default location", d)
            self._data_dir_missing = str(d)
            self.paths = paths
        self.paths.ensure()
        self.emit: Callable[[str, dict], None] = emit or (lambda e, p: None)
        # Whether the page may capture media (the window's policy, app.py);
        # on while in a call.
        self.media_access: Callable[[bool], None] = lambda allowed: None
        self.vault: Vault | None = None
        self.state: StateStore | None = None
        self.conns: dict[str, CommunityConnection] = {}
        self.caches: dict[str, CommunityCache] = {}
        self.guard = UnlockGuard(self.paths.unlock_guard)
        self.last_activity = time.monotonic()
        self._tasks: set[asyncio.Task] = set()
        self._profile_tasks: list[asyncio.Task] = []  # the open profile's own loops
        # Communities where this client is signing another owner's removal.
        self._completing_leaves: set[str] = set()
        self._avatar_cache: dict[tuple[str, str, int], str] = {}
        self._save_task: asyncio.Task | None = None
        # Serialises opening the profile (create, unlock, keychain unlock,
        # reset): two at once would open it twice (two sets of background
        # loops and subscriptions, and saves through two Vault objects).
        self._open_lock = asyncio.Lock()
        # The one-time offer to stay signed in (profiles from before it was
        # the default): where the key would be kept ("keychain" or "file")
        # while the offer stands, else "". Made after a passphrase unlock.
        self._stay_offer = ""
        # (community, file id) -> file key, for files the UI has shown.
        self._file_keys: dict[tuple[str, int], str] = {}
        # Last member list per community (mention tokens, presence).
        self._members: dict[str, list[dict]] = {}
        # The maintainer nodes are this device's preference, so they apply
        # before unlock too (first run, unlock, restore, linking).
        # Entries that name a maintainer cluster (URL#KEY) use the cluster's
        # members too, from its verified member list (nodes.py), cached in
        # preferences so it works locked and offline.
        self.clusters = Clusters.load(
            self.prefs.maintainer_sets, settings_mod.effective_maintainers(self.prefs.values["maintainers"])
        )
        # Due at once: the monotonic clock starts at boot, so zero would put
        # the first check up to a refresh interval away on a fresh system.
        self._clusters_checked = time.monotonic() - REFRESH_SECONDS
        self._clusters_generation = -1
        self._directory_checked = time.monotonic() - DIRECTORY_CHECK_SECONDS
        self.meta = MetaClient([])
        self._apply_maintainers()
        self.directory = LocalDirectory(self.meta)
        # Communities hosted on this computer (hosting.py): server processes
        # this app runs, also while locked or closed to the tray.
        self.hosting = HostManager(lambda: self.paths.data / "hosted", lambda: self.paths.log, self._hosting_changed)
        from openarcanum.devices import DeviceManager
        from openarcanum.dm import DMManager
        from openarcanum.dmcalls import DmCalls
        from openarcanum.moderation import Moderation
        from openarcanum.statuses import Statuses
        from openarcanum.voice import VoiceManager

        self.devices = DeviceManager(self)
        self.dms = DMManager(self)
        self.voice = VoiceManager(self)
        self.dmcalls = DmCalls(self)
        self.moderation = Moderation(self)
        # User statuses, end-to-end encrypted (statuses.py).
        self.statuses = Statuses(self)
        # Auto-update (docs/release.md): runs whether or not the profile is
        # unlocked; its state file sits beside the preferences.
        self.updates = Updater(
            paths.config / "updates.json",
            cache_dir=lambda: self.paths.cache / "updates",
            auto_check=lambda: bool(self.prefs.values.get("update_check", True)),
            emit=lambda event, data: self.emit(event, data),
        )

    # ---------------------------------------------------------------- basics

    @property
    def unlocked(self) -> bool:
        return self.vault is not None and not self.vault.locked

    @property
    def settings(self) -> dict:
        return self.vault.settings if self.vault else settings_mod.defaults("vault")

    @property
    def device_name(self) -> str:
        return self.settings.get("device_name") or _("{platform} device").format(platform=platform_name())

    def require_unlocked(self) -> Vault:
        if self.vault is None or self.vault.locked:
            raise UserError(_("Unlock your profile first."))
        return self.vault

    def spawn(self, coro) -> asyncio.Task:
        t = asyncio.ensure_future(coro)
        self._tasks.add(t)
        t.add_done_callback(self._tasks.discard)
        return t

    def save_vault(self) -> None:
        if self.vault is not None:
            self.vault.save()

    def profile_state(self) -> str:
        """The profile's state: "no_profile", "locked" or "unlocked"."""
        if self.unlocked:
            return "unlocked"
        return "locked" if self.vault is not None or Vault.exists(self.paths) else "no_profile"

    def profile_status(self) -> dict:
        return {
            "state": self.profile_state(),
            "product": PRODUCT_NAME,
            "version": __version__,
            "platform": platform_name(),
            "appearance": {k: self.prefs.values[k] for k in ("theme", "text_size", "compact")},
            "backoff": self.guard.remaining(),
            "backup_current": self.vault.backup_current if self.vault else True,
            "device_name": self.device_name if self.vault else "",
            "display_name": self.settings.get("display_name", "") if self.vault else "",
            "notice": _(NOTICE),
            # The previous run crashed; the UI says where the log is.
            "crashed_last_time": crashlog.last_run_crashed,
            "log_dir": str(self.paths.log),
            "remember": self._remember_status(),
            "maintainers": self.maintainers_status(),
        }

    # ---------------------------------------------------------------- maintainer nodes

    def maintainers_status(self) -> dict:
        """This device's maintainer list and what's in use; works locked and
        before a profile exists. `clusters`: each entry that names a
        maintainer cluster, with its verified members; `health`: each node in
        use (ok, backing off with the last error, or not tried yet)."""
        own = list(self.prefs.values["maintainers"])
        entries = settings_mod.effective_maintainers(own)
        return {
            "own": own,
            "effective": list(self.meta.urls),
            "source": settings_mod.maintainers_source(own),
            "launch": settings_mod.launch_maintainers(),
            "builtin": list(settings_mod.BUILTIN_MAINTAINERS),
            "max": settings_mod.MAX_MAINTAINERS,
            "use_cluster": bool(self.prefs.values["maintainer_cluster_nodes"]),
            "clusters": self.clusters.describe(entries),
            "health": {u: self.meta.health.status(u) for u in self.meta.urls},
            # False once every maintainer node in use has its public
            # directory switched off: the app hides finding and listing
            # communities (docs/protocol.md §7.2).
            "directory": self.meta.directory_available(),
        }

    async def refresh_directory(self) -> bool:
        """Ask every node in use whether it serves the public directory
        (GET /v1/node); a node that doesn't answer keeps what was known.
        Nodes of a pinned cluster start from what its member list says.
        True if whether the directory is available changed."""
        before = self.meta.directory_available()
        for s in self.clusters.sets.values():
            for n in s.nodes:
                self.meta.directory.setdefault(n.url, not n.no_directory)
        urls = list(self.meta.urls)
        for attempt in range(3):  # a node that is just starting answers soon
            results = await asyncio.gather(*(self.meta.node_info(u) for u in urls), return_exceptions=True)
            failed = [(u, r) for u, r in zip(urls, results, strict=True) if isinstance(r, Exception)]
            for u, r in failed:
                log.info("directory check of %s: %s", u, r)
            urls = [u for u, _r in failed]
            if not urls or attempt == 2:
                break
            await asyncio.sleep(2)
        self._directory_checked = time.monotonic()
        if self.meta.directory_available() != before:
            self.emit("maintainers", self.maintainers_status())
            return True
        return False

    def _apply_maintainers(self) -> None:
        entries = settings_mod.effective_maintainers(self.prefs.values["maintainers"])
        urls, groups = self.clusters.expand(entries, bool(self.prefs.values["maintainer_cluster_nodes"]))
        self.meta.urls, self.meta.groups = urls, groups

    async def refresh_clusters(self, force: bool = False) -> bool:
        """Fetch the member list of every pinned cluster again from its
        members and keep it if it verifies with a seq at least the held
        one: every REFRESH_SECONDS, and soon after a node failed (the list
        may have changed). True if anything changed."""
        now = time.monotonic()
        failed = self.meta.health.generation != self._clusters_generation
        due = (
            force or now - self._clusters_checked >= REFRESH_SECONDS or (failed and now - self._clusters_checked >= 60)
        )
        if not due:
            return False
        self._clusters_checked, self._clusters_generation = now, self.meta.health.generation
        changed = False
        for e in settings_mod.effective_maintainers(self.prefs.values["maintainers"]):
            url, op = parse_entry(e)
            if op is None:
                continue
            have = self.clusters.sets.get(op)
            candidates = [url] + [n.url for n in (have.nodes if have else []) if n.url != url]
            for base in self.meta.health.order(candidates):
                try:
                    _s, raw = await self.meta.fetch_set(base, op)
                    changed = self.clusters.offer(raw, op) or changed
                    break
                except (MetaError, ValueError) as err:
                    log.info("maintainer network list from %s: %s", base, err)
        if changed:
            self.prefs.set_maintainer_sets(self.clusters.cached())
            self._apply_maintainers()
            self.directory.forget()
            self.emit("maintainers", self.maintainers_status())
        return changed

    async def _clusters_loop(self) -> None:
        while True:
            try:
                await self.refresh_clusters()
                if time.monotonic() - self._directory_checked >= DIRECTORY_CHECK_SECONDS:
                    await self.refresh_directory()
            except asyncio.CancelledError:
                raise
            except Exception:
                log.exception("maintainer network refresh failed")
            await asyncio.sleep(30)

    def start_clusters(self) -> None:
        """Keep pinned maintainer networks' member lists current (runs
        locked too: the lists are public and kept in preferences)."""
        self.spawn(self._clusters_loop())

    async def maintainer_cluster_offer(self, url: str) -> dict | None:
        """For the maintainer editor's Add: whether the node at `url` is in a
        maintainer cluster, and its operator key's fingerprint and members,
        for the user to confirm before the key is pinned (trust on first
        use). None if it isn't (or doesn't say)."""
        try:
            (clean,) = settings_mod.validate("maintainers", [url])
        except SettingError as e:
            raise UserError(str(e)) from None
        base, op = parse_entry(clean)
        try:
            s, _raw = await MetaClient([base], health=self.meta.health).fetch_set(base, op)
        except MetaError:
            return None
        return {
            "entry": base + "#" + keys.encode(s.operator_key),
            "operator": keys.encode(s.operator_key),
            "fingerprint": keys.fingerprint(s.operator_key),
            "seq": s.seq,
            "members": [{"url": n.url, "role": n.role} for n in s.nodes],
        }

    def check_new_maintainer(self, url: str, current: list[str]) -> list[str]:
        """The editor's Add: the list with `url` added, or the reason it can't be."""
        try:
            return settings_mod.check_new_maintainer(url, current)
        except SettingError as e:
            raise UserError(str(e)) from None

    async def set_maintainers(self, urls: list[str]) -> dict:
        """Save this device's maintainer list (empty: the --maintainer option
        or the built-in list). Applies at once, also while locked, so the next
        unlock, search, link or DM fetch uses it."""
        await self.set_setting("maintainers", urls)
        return self.maintainers_status()

    def _maintainers_changed(self) -> None:
        entries = settings_mod.effective_maintainers(self.prefs.values["maintainers"])
        pinned = {op for _u, op in map(parse_entry, entries) if op is not None}
        for op in [op for op in self.clusters.sets if op not in pinned]:
            self.clusters.sets.pop(op)
            self.clusters.raw.pop(op, None)
        self.prefs.set_maintainer_sets(self.clusters.cached())
        self._apply_maintainers()
        self.directory.forget()
        self.emit("maintainers", self.maintainers_status())
        if any(op not in self.clusters.sets for op in pinned):
            self.spawn(self.refresh_clusters(force=True))
        self.spawn(self.refresh_directory())

    def _migrate_vault_settings(self) -> None:
        """Profiles from before the maintainer list moved to preferences.toml
        have it in the vault. It moves to this device's preferences, unless
        they already name one, and leaves the vault."""
        assert self.vault is not None
        self._migrate_voice_sensitivity()
        if "maintainers" not in self.vault.settings:
            return
        old = self.vault.settings.pop("maintainers")
        if old and not self.prefs.values["maintainers"]:
            try:
                self.prefs.set("maintainers", old)
            except SettingError:
                log.warning("the profile's old maintainer list was not valid; it was dropped")
        self.save_vault()
        self._maintainers_changed()

    def _migrate_voice_sensitivity(self) -> None:
        """Voice activity's old default (-50 dBFS) opened on ordinary room
        noise. Profiles still on it, never set by hand (on any of their
        devices: a set or synced value has a time), move to the new one."""
        assert self.vault is not None
        st = self.vault.settings
        if st.get("voice_sensitivity") != settings_mod.VOICE_SENSITIVITY_OLD_DEFAULT:
            return
        if "voice_sensitivity" in self.vault.data.get("setting_times", {}):
            return
        st["voice_sensitivity"] = settings_mod.VOICE_SENSITIVITY_DEFAULT
        self.save_vault()

    def _remember_status(self) -> dict:
        # Only what preferences say: finding out whether a keychain exists
        # can take long (on Linux it may wait for D-Bus to try starting one),
        # so that's asked for separately (remember_status), off the UI's path.
        # enabled: stay signed in on this device; stored: where the key that
        # opens the profile at start is now ("" after a lock); offer: the
        # one-time offer to stay signed in is open (where the key would go).
        return {
            "enabled": bool(self.prefs.values.get("remember_device")),
            "stored": self.prefs.unlock_key_in,
            "offer": self._stay_offer if self.unlocked else "",
        }

    async def remember_status(self) -> dict:
        """Settings → Security: staying signed in, and where the key is (or
        would be) kept: the OS keychain, or the key file where there is none."""
        ok, why = await asyncio.to_thread(keychain.available)
        st = self._remember_status()
        return {
            "enabled": st["enabled"],
            "stored": st["stored"],
            "store": st["stored"] or (keychain.KEYCHAIN if ok else keychain.FILE),
            "available": ok,
            "reason": why,
            "file": str(keychain.key_file(self.paths)),
        }

    async def _keep_unlock_key(self, v: Vault, toast: bool = True) -> None:
        """Stay signed in: keep the vault's key on this device (the keychain,
        or the key file where there is none). If it can't be kept, staying
        signed in turns off (the passphrase is asked for at start) and the
        reason is shown as a toast, or raised as a UserError (toast=False)."""
        old = self.prefs.unlock_key_in
        try:
            where = await asyncio.to_thread(keychain.store, self.paths, v.file_key())
        except keychain.KeychainError as e:
            log.info("stay signed in: the key couldn't be kept")
            if old:
                # Whatever is kept from before may no longer fit.
                await self._forget_unlock_key()
            self.prefs.set("remember_device", False)
            if not toast:
                raise UserError(str(e)) from None
            self.emit(
                "toast",
                {
                    "text": _(
                        "{error} OpenArcanum will ask for your passphrase when it starts. "
                        "You can try again in Settings → Security."
                    ).format(error=e)
                },
            )
            return
        if old and old != where:
            # The keychain appeared or went away since: nothing stays behind.
            await asyncio.to_thread(keychain.delete, self.paths, old)
        self.prefs.set_unlock_key_in(where)

    async def _forget_unlock_key(self) -> None:
        """Delete the kept key (from wherever it is); the passphrase is
        needed at the next start."""
        await asyncio.to_thread(keychain.delete, self.paths, self.prefs.unlock_key_in)
        self.prefs.set_unlock_key_in("")

    async def _new_profile_signed_in(self) -> None:
        """After creating, restoring or linking a profile: stay signed in on
        this device by default (Settings → Security → "Ask for my passphrase
        when OpenArcanum starts" turns it off)."""
        assert self.vault is not None
        self.prefs.mark_shown("stay_signed_in_offered")
        self.prefs.set("remember_device", True)
        await self._keep_unlock_key(self.vault)

    async def _after_passphrase_unlock(self, v: Vault) -> None:
        """The passphrase opened the profile: keep the key again if this
        device stays signed in (a lock deleted it), or make the one-time
        offer to a profile from before staying signed in was the default."""
        if self.prefs.values.get("remember_device"):
            if not self.prefs.unlock_key_in:
                await self._keep_unlock_key(v)
        elif "stay_signed_in_offered" not in self.prefs.shown:
            self._stay_offer = await asyncio.to_thread(keychain.where_to_store)

    async def answer_stay_signed_in(self, accept: bool) -> dict:
        """The one-time offer's answer. Yes keeps the key of the profile just
        opened with the passphrase; either answer is final (Settings →
        Security can change it later)."""
        v = self.require_unlocked()
        if not self._stay_offer:
            raise UserError(_("Change this in Settings → Security."))
        self._stay_offer = ""
        self.prefs.mark_shown("stay_signed_in_offered")
        if accept:
            self.prefs.set("remember_device", True)
            await self._keep_unlock_key(v)
        self.emit("profile", self.profile_status())
        return self.profile_status()

    # ---------------------------------------------------------------- profile

    async def create_profile(self, passphrase: str, device_name: str = "") -> dict:
        check_new_passphrase(passphrase)
        name = settings_mod.validate(
            "device_name", device_name or _("{platform} device").format(platform=platform_name())
        )
        async with self._open_lock:
            if self.vault is not None:
                raise UserError(_("A profile is already open."))
            self.vault = await asyncio.to_thread(Vault.create, self.paths, passphrase, name)
            await self._opened()
            await self._new_profile_signed_in()
            return self.profile_status()

    async def unlock(self, passphrase: str) -> dict:
        if not passphrase:
            raise UserError(_("Enter your passphrase."))
        async with self._open_lock:
            if self.unlocked:
                # Another unlock (a second click, or the keychain) got there first.
                return self.profile_status()
            try:
                v = await asyncio.to_thread(Vault.unlock, self.paths, passphrase, self.guard)
            except Backoff as e:
                raise UserError(str(e)) from None
            except WrongPassphrase:
                raise UserError(
                    _("Wrong passphrase. Wait {seconds} s to try again.").format(
                        seconds=int(self.guard.remaining() + 0.999)
                    )
                ) from None
            await self._open_vault(v)
            await self._after_passphrase_unlock(v)
            return self.profile_status()

    async def _open_vault(self, v: Vault) -> dict:
        """Install an unlocked vault (callers hold _open_lock)."""
        if self.vault is not None and self.vault.locked:
            # Already running (locked): swap in the full data, keep sessions.
            self.vault = v
            self._migrate_vault_settings()
            self.last_activity = time.monotonic()
            self.emit("profile", self.profile_status())
            return self.profile_status()
        self.vault = v
        await self._opened()
        return self.profile_status()

    async def unlock_remembered(self) -> dict:
        """Stay signed in: open with the key kept on this device (the
        keychain or the key file)."""
        async with self._open_lock:
            if self.unlocked:
                return self.profile_status()
            where = self.prefs.unlock_key_in
            if not where:
                raise UserError(_("Enter your passphrase."))
            try:
                key = await asyncio.to_thread(keychain.load, self.paths, where)
            except keychain.KeychainError as e:
                if where == keychain.FILE:
                    # The file was refused (and deleted) or can't be read.
                    self.prefs.set_unlock_key_in("")
                raise UserError(_("{error} Enter your passphrase.").format(error=e)) from None
            if key is None:
                self.prefs.set_unlock_key_in("")
                raise UserError(_("No key is kept on this device any more. Enter your passphrase."))
            try:
                v = await asyncio.to_thread(Vault.unlock_with_key, self.paths, key)
            except WrongPassphrase:
                # The profile file was re-keyed (or replaced) without this
                # device's key being updated: forget the stale key. The
                # passphrase opens it and, staying signed in, keeps the new one.
                await self._forget_unlock_key()
                raise UserError(
                    _(
                        "The key kept on this device no longer opens this profile (was the passphrase changed?). "
                        "Enter your passphrase."
                    )
                ) from None
            return await self._open_vault(v)

    def _guard_check(self) -> None:
        """Refuse while the wrong-passphrase backoff runs (every passphrase
        check counts, not only unlocking: the page can't guess through them)."""
        try:
            self.guard.check()
        except Backoff as e:
            raise UserError(str(e)) from None

    def _wrong_passphrase(self, text: str) -> UserError:
        self.guard.failed()
        return UserError(
            _("{error} Wait {seconds} s to try again.").format(error=text, seconds=int(self.guard.remaining() + 0.999))
        )

    async def confirm_passphrase(self, passphrase: str) -> None:
        """Check the passphrase typed again for a privileged action (export
        a backup, link or remove a device). Checked here
        against the profile file, with the unlock backoff; the page never
        decides (docs/threat-model.md, "The page and the bridge")."""
        self.require_unlocked()
        if not passphrase:
            raise UserError(_("Enter your passphrase to confirm."))
        # One check at a time, so each counts toward the backoff before the next starts.
        async with self._open_lock:
            self._guard_check()
            try:
                await asyncio.to_thread(open_with_passphrase, VAULT_MAGIC, passphrase, self.paths.vault.read_bytes())
            except WrongPassphrase:
                raise self._wrong_passphrase(_("That passphrase is wrong.")) from None
            self.guard.succeeded()

    async def set_remember(self, enabled: bool, passphrase: str = "") -> dict:
        """Stay signed in on this device, or not ("Ask for my passphrase when
        OpenArcanum starts"); the passphrase confirms it's you either way.
        On keeps the key in the keychain, or the key file where there is
        none. Off deletes it and re-keys the vault (same passphrase, new
        salt), so a copy of the key taken earlier (malware that read the
        keychain or the file, a backup of either) doesn't open the profile
        file from now on."""
        v = self.require_unlocked()
        async with self._open_lock:
            self._guard_check()
            if enabled:
                try:
                    await asyncio.to_thread(
                        open_with_passphrase, VAULT_MAGIC, passphrase, self.paths.vault.read_bytes()
                    )
                except WrongPassphrase:
                    raise self._wrong_passphrase(_("That passphrase is wrong.")) from None
                self.guard.succeeded()
                self.prefs.set("remember_device", True)
                await self._keep_unlock_key(v, toast=False)
            else:
                try:
                    # scrypt in a worker; the switch and save here, on the loop
                    # that does every other save (as change_passphrase).
                    key, header, h = await asyncio.to_thread(v.new_key_for, passphrase, passphrase)
                except WrongPassphrase:
                    raise self._wrong_passphrase(_("That passphrase is wrong.")) from None
                self.guard.succeeded()
                v.use_key(key, header, h)
                await self._forget_unlock_key()
                self.prefs.set("remember_device", False)
            self._stay_offer = ""
        self.prefs.mark_shown("stay_signed_in_offered")
        self.emit("profile", self.profile_status())
        return await self.remember_status()

    async def _opened(self) -> None:
        assert self.vault is not None
        self.state = StateStore(self.paths.state, self.vault.state_key)
        self._migrate_vault_settings()
        self.last_activity = time.monotonic()
        for cid, rec in self.vault.data["communities"].items():
            if rec.get("status") in ("active", "pending"):
                self._start(cid)
        self._profile_tasks = [self.spawn(self._idle_watch()), self.spawn(self._cache_janitor())]
        await self.dms.opened()
        await self.devices.opened()
        await self.statuses.opened()
        self.emit("profile", self.profile_status())
        if self._data_dir_missing:
            text = _("The data folder {folder} isn't available; this device's default location is used.").format(
                folder=self._data_dir_missing
            )
            self.emit("toast", {"text": text, "error": True})

    async def _close_profile(self) -> None:
        """Stop what the open profile runs (its community connections, the
        DM, device and status loops, its own tasks), before the profile is
        set aside or deleted: a new one starts its own."""
        for conn in list(self.conns.values()):
            with contextlib.suppress(Exception):
                await conn.stop()
        self.conns.clear()
        for part in (self.dms, self.devices, self.statuses):
            with contextlib.suppress(Exception):
                await part.shutdown()
        for t in self._profile_tasks:
            t.cancel()
        self._profile_tasks = []

    async def lock(self) -> dict:
        """Lock now, or an idle lock. Locked means the passphrase is needed
        to come back, also after a restart: a key kept for staying signed in
        is deleted here, and the passphrase keeps it again."""
        if self.vault is not None and not self.vault.locked:
            self.vault.lock()
            self._stay_offer = ""
            if self.prefs.unlock_key_in:
                await self._forget_unlock_key()
            self.emit("profile", self.profile_status())
        return self.profile_status()

    async def change_passphrase(self, old: str, new: str) -> dict:
        v = self.require_unlocked()
        check_new_passphrase(new)
        async with self._open_lock:
            self._guard_check()
            try:
                # scrypt in a worker; the switch and save here, on the loop that
                # does every other save (a save in between would write the file
                # under the old key).
                key, header, h = await asyncio.to_thread(v.new_key_for, old, new)
            except WrongPassphrase:
                raise self._wrong_passphrase(_("The current passphrase is wrong.")) from None
            self.guard.succeeded()
            v.use_key(key, header, h)
        if self.prefs.unlock_key_in:
            # The file's key changed with the passphrase.
            await self._keep_unlock_key(v)
        return {"ok": True}

    async def reset_profile(self) -> dict:
        """Forgot passphrase → start over. The old vault is moved aside,
        never deleted, so a remembered passphrase can still recover it."""
        async with self._open_lock:
            # Checked before anything changes: a refused reset leaves staying
            # signed in as it was.
            if self.unlocked:
                raise UserError(_("Lock the profile first."))
            await self._forget_unlock_key()
            self.prefs.set("remember_device", False)
            stamp = time.strftime("%Y%m%d-%H%M%S")
            for f in (self.paths.vault, self.paths.state, self.paths.data / "dms.bin", self.paths.data / "olm.bin"):
                if f.exists():
                    f.rename(f.with_name(f"{f.name}.set-aside-{stamp}"))
            await self._close_profile()
            self.caches.clear()
            shutil.rmtree(self.paths.cache, ignore_errors=True)
            self.paths.ensure()
            self.vault = None
            self.state = None
            self.guard.succeeded()
            return self.profile_status()

    async def wipe_device(self, reason: str) -> None:
        """This device was removed from the account: stop everything and
        delete the profile, state, DM history and cache."""
        await self._close_profile()
        for c in list(self.caches.values()):
            c.wipe()
        self.caches.clear()
        with contextlib.suppress(Exception):
            await self.voice.shutdown()
        await self._forget_unlock_key()
        self.prefs.set("remember_device", False)
        for f in (self.paths.vault, self.paths.state, self.paths.data / "dms.bin", self.paths.data / "olm.bin"):
            with contextlib.suppress(FileNotFoundError):
                f.unlink()
        shutil.rmtree(self.paths.cache, ignore_errors=True)
        self.paths.ensure()
        self.vault = None
        self.state = None
        self.emit("toast", {"text": reason})
        self.emit("profile", self.profile_status())

    def activity(self) -> None:
        self.last_activity = time.monotonic()
        self.statuses.activity()

    async def _idle_watch(self) -> None:
        while True:
            await asyncio.sleep(5)
            if not self.unlocked:
                continue
            minutes = self.settings.get("auto_lock_minutes", 0)
            if minutes and time.monotonic() - self.last_activity >= minutes * 60:
                await self.lock()
                self.emit("toast", {"text": _("Locked after being idle.")})

    # ---------------------------------------------------------------- backups

    async def export_backup(self, path: str, passphrase: str) -> dict:
        v = self.require_unlocked()
        check_new_passphrase(passphrase)
        blob = await asyncio.to_thread(keys.export_backup, v.data, passphrase)
        target = Path(path).expanduser()
        if target.is_dir():
            target = target / "openarcanum-backup.fbak"
        try:
            # Readable by the user only: it holds every key (encrypted).
            await asyncio.to_thread(atomic_write, target, blob)
        except OSError as e:
            raise UserError(_("Can't save the backup there: {reason}").format(reason=e.strerror)) from None
        v.data["backup_revision"] = v.data.get("keys_revision", 0)
        v.data["backup_exported_at"] = int(time.time())
        self.save_vault()
        self.emit("profile", self.profile_status())
        return {"ok": True, "path": str(target)}

    def backup_status(self) -> dict:
        v = self.require_unlocked()
        return {
            "current": v.backup_current,
            "exported_at": v.data.get("backup_exported_at", 0),
            "communities": sum(1 for c in v.data["communities"].values() if c.get("status") in ("active", "pending")),
        }

    async def restore_backup(
        self, path: str, backup_passphrase: str, new_passphrase: str, device_name: str = ""
    ) -> dict:
        if Vault.exists(self.paths):
            raise UserError(_("A profile already exists on this device."))
        check_new_passphrase(new_passphrase)
        try:
            blob = await asyncio.to_thread(read_at_most, Path(path).expanduser(), MAX_BACKUP_BYTES)
        except OSError as e:
            raise UserError(_("Can't read the backup file: {reason}").format(reason=e.strerror)) from None
        if blob is None:
            raise UserError(_("That isn't a backup file (it's far too large)."))
        try:
            data = await asyncio.to_thread(keys.import_backup, blob, backup_passphrase)
        except WrongPassphrase:
            raise UserError(_("Wrong backup passphrase.")) from None
        except VaultError as e:
            raise UserError(str(e)) from None
        from openarcanum.vault import new_vault_data

        name = settings_mod.validate(
            "device_name", device_name or _("{platform} device").format(platform=platform_name())
        )
        vd = new_vault_data(name)
        vd["identities"] = data["identities"]
        vd["contacts"] = data.get("contacts", {})
        for c in data["communities"]:
            rec = dict(c)
            # A restored device makes its own device keys.
            rec["device"] = self._new_device(rec["identity"], vd)
            rec["status"] = "active" if rec.get("status") != "pending" else "pending"
            vd["communities"][rec["id"]] = rec
        vd["backup_revision"] = vd["keys_revision"] = 1
        async with self._open_lock:
            if self.vault is not None:
                raise UserError(_("A profile is already open."))
            self.vault = await asyncio.to_thread(Vault.create, self.paths, new_passphrase, name, vd)
            await self._opened()
            await self._new_profile_signed_in()
            return self.profile_status()

    # ---------------------------------------------------------------- identities

    def _new_identity(self, kind: str, label: str = "") -> str:
        v = self.require_unlocked()
        k = SigningKey.generate()
        iid = keys.fingerprint(k.public)
        v.data["identities"][iid] = {
            "seed": keys.encode(k.seed),
            "public": keys.encode(k.public),
            "kind": kind,
            "created": int(time.time()),
            "label": label,
        }
        return iid

    def identity_key(self, iid: str) -> SigningKey:
        v = self.require_unlocked()
        ident = v.data["identities"].get(iid)
        if not ident or "seed" not in ident:
            raise UserError(_("That identity's key isn't available."))
        return SigningKey(keys.decode(ident["seed"]))

    def identity_public(self, iid: str) -> bytes:
        assert self.vault is not None
        ident = self.vault.data["identities"][iid]
        if "public" in ident:
            return keys.decode(ident["public"])
        return SigningKey(keys.decode(ident["seed"])).public

    def _new_device(self, iid: str, vault_data: dict | None = None) -> dict:
        data = vault_data or self.require_unlocked().data
        ident = data["identities"][iid]
        k = SigningKey(keys.decode(ident["seed"]))
        ident.setdefault("public", keys.encode(k.public))
        st = StaticKey.generate()
        return {
            "static": keys.encode(st.private),
            "public": keys.encode(st.public),
            "sig": keys.encode(sign_static(k, st.public)),
        }

    def identity_choices(self) -> list[dict]:
        """Existing member identities and which communities would be able to
        link you if you reused one."""
        v = self.require_unlocked()
        out = []
        for iid, ident in v.data["identities"].items():
            if ident.get("kind") != "member":
                continue
            used = [
                c.get("name", "?")
                for c in v.data["communities"].values()
                if c.get("identity") == iid and c.get("status") in ("active", "pending")
            ]
            out.append(
                {"id": iid, "label": ident.get("label") or _("Identity {id}").format(id=iid[:6]), "communities": used}
            )
        return out

    # ---------------------------------------------------------------- communities

    def _start(self, cid: str) -> None:
        conn = self.conns.get(cid)
        if conn is None:
            conn = self.conns[cid] = CommunityConnection(self, cid)
        conn.start()

    def cache(self, cid: str) -> CommunityCache:
        c = self.caches.get(cid)
        if c is None:
            assert self.vault is not None
            c = self.caches[cid] = CommunityCache(
                self.paths.community_cache(hashlib.sha256(cid.encode()).hexdigest()[:32]), self.vault.cache_key, cid
            )
        return c

    def _schedule_cache_save(self) -> None:
        if self._save_task is None or self._save_task.done():

            async def later():
                await asyncio.sleep(1.0)
                for c in list(self.caches.values()):
                    with contextlib.suppress(OSError):
                        c.save()

            self._save_task = self.spawn(later())

    def community_changed(self, cid: str) -> None:
        self.emit("community", self.community_summary(cid))

    def community_summary(self, cid: str) -> dict:
        v = self.vault
        assert v is not None
        rec = v.data["communities"].get(cid, {})
        conn = self.conns.get(cid)
        status = rec.get("status", "removed")
        ended = status not in ("active", "pending")
        live = conn.status if conn else ("offline" if not ended else status)
        st = conn.state if conn else None
        perms = st.permissions if st else 0
        feats = features(st.settings if st else None)
        unread = mentions = 0
        if st is not None and self.state is not None:
            for ch in st.channels:
                # A voice channel's chat counts while voice channels are on
                # (they're hidden otherwise).
                if ch.kind in MESSAGE_KINDS and (ch.kind == cp.CHANNEL_KIND_TEXT or feats.get("voice", True)):
                    u, n = self._counts(cid, ch.id)
                    unread += u
                    mentions += n
        return {
            "id": cid,
            "name": rec.get("name", "Community"),
            "status": status,
            "connection": live,
            # Removed, banned or revoked: why, also while the ended
            # connection is still around (the banner shows this text).
            "error": rec.get("removed_reason", "") if ended else (conn.error if conn else ""),
            "relay": conn.relay_url if conn else "",
            "display_name": rec.get("display_name", ""),
            "is_owner": bool(rec.get("owner_identity")),
            "permissions": perms,
            "can": {name: has(perms, bit) for name, bit in PERMISSIONS.items()},
            # Community toggles: off means hidden everywhere (the server refuses it too).
            "features": feats,
            "icon": base64.b64encode(st.settings.icon).decode() if st and st.settings.icon else "",
            "muted": cid in self.settings.get("muted_communities", []),
            # This community's own notification level ("" = the default).
            "notify": self.settings.get("community_notify", {}).get(cid, ""),
            "notify_level": community_notify_level(self.settings, cid),
            # What waits in the moderator queues this member may see
            # (docs/protocol.md §4.9): the badges that lead to it.
            "mod_queue": _mod_queue(st, perms),
            "publish_card": bool(rec.get("publish_card", True)),
            "identity": rec.get("identity", ""),
            "unread": unread,
            "mentions": mentions,
            # On relays dedicated to this community: bans can block network
            # addresses there (docs/protocol.md §2 "Dedicated relays").
            "dedicated_relay": bool(st and st.dedicated_relay),
            # What the maintainer nodes report about its server while this
            # device can't reach it (offline since, degraded, shut down;
            # meta.status_dict).
            "server_status": conn.server_status if conn else None,
            # Hosted on this computer (hosting.py): its server's state.
            "hosted": self._hosted_status(rec),
        }

    def list_communities(self) -> list[dict]:
        v = self.require_unlocked()
        order = self.state.get("community_order", []) if self.state else []
        ids = sorted(v.data["communities"], key=lambda c: (order.index(c) if c in order else 1_000_000, c))
        return [self.community_summary(c) for c in ids]

    def conn(self, cid: str) -> CommunityConnection:
        self.require_unlocked()
        c = self.conns.get(cid)
        if c is None:
            raise UserError(_("You aren't connected to that community."))
        return c

    def community_state(self, cid: str) -> dict:
        conn = self.conn(cid)
        st = conn.state
        summary = self.community_summary(cid)
        if st is None:
            return {**summary, "channels": [], "roles": [], "settings": None, "self": None}
        roles = [
            {
                "id": r.id,
                "name": r.name,
                "permissions": r.permissions,
                "perms": names(r.permissions),
                "position": r.position,
                "color": f"#{r.color:06x}" if r.color else "",
                "builtin": r.builtin,
                "hoist": r.hoist,
                "mentionable": r.mentionable,
            }
            for r in st.roles
        ]
        channels = []
        muted = set(self.settings.get("muted_channels", []))
        for ch in st.channels:
            channels.append(
                {
                    "id": ch.id,
                    "name": ch.name,
                    "kind": _enum_name(cp.ChannelKind, ch.kind, "CHANNEL_KIND_"),
                    "position": ch.position,
                    "permissions": ch.permissions,
                    "can": {name: has(ch.permissions, bit) for name, bit in PERMISSIONS.items()},
                    # Community toggles, then the channel's own (docs/protocol.md §4.6).
                    "features": channel_features(st.settings, ch),
                    "message_retention_days": ch.message_retention_days
                    if ch.HasField("message_retention_days")
                    else None,
                    "file_retention_days": ch.file_retention_days if ch.HasField("file_retention_days") else None,
                    "overrides": [_override_dict(o) for o in ch.overrides],
                    "muted": f"{cid}:{ch.id}" in muted,
                    "topic": ch.description,
                    "category_id": ch.category_id,
                    "synced": ch.permissions_synced,
                    "slowmode": ch.slowmode_seconds,
                    "settings": {
                        "attachments": _toggle_name(ch.attachments),
                        "link_previews": _toggle_name(ch.link_previews),
                        "reactions": _toggle_name(ch.reactions),
                        "history_for_new_members": _toggle_name(ch.history_for_new_members),
                    },
                }
            )
            u, n = self._counts(cid, ch.id)
            channels[-1].update(unread=u, mentions=n, last_read=self._last_read(cid, ch.id))
            channels[-1]["features"]["images_only"] = st.settings.attachment_kinds == cp.ATTACHMENT_KINDS_IMAGES_ONLY
        categories = [
            {
                "id": c.id,
                "name": c.name,
                "position": c.position,
                "overrides": [_override_dict(o) for o in c.overrides],
            }
            for c in st.categories
        ]
        me = st.self
        return {
            **summary,
            "channels": channels,
            "categories": categories,
            "roles": roles,
            "voice_rooms": self.voice.rooms(cid),
            "permission_catalog": catalog(),
            "settings": settings_to_dict(st.settings),
            "self": {
                "key": keys.encode(me.key),
                "display_name": me.display_name,
                "nickname": me.nickname,
                "about": me.about,
                "pronouns": me.pronouns,
                "role_ids": list(me.role_ids),
                "owner": me.owner,
                "owner_invited": me.owner_invited,
                "has_avatar": me.has_avatar,
                "timeout_until": me.timeout_until,
                "can_post_at": me.can_post_at,
            },
            "emoji": self.moderation.emoji_list(cid),
            **self._ownership(cid, conn),
            "info": {"member_range": st.info.member_range, "server_key": keys.encode(st.info.server_key)},
            "backup_current": self.vault.backup_current if self.vault else True,
        }

    # ---------------------------------------------------------------- join

    async def _temp_session(
        self,
        relays: list[str],
        server_key: bytes,
        owner_key: bytes | None,
        identity: SigningKey,
        static: StaticKey,
        retry_for: float = 0.0,
    ) -> Session2:
        """A one-off session through the first relay that works. retry_for:
        keep trying the relays for that many seconds (a server that is only
        starting, hosting.py)."""
        deadline = time.monotonic() + retry_for
        while True:
            try:
                return await self._temp_session_once(relays, server_key, owner_key, identity, static)
            except UserError:
                if time.monotonic() >= deadline:
                    raise
            await asyncio.sleep(0.5)

    async def _temp_session_once(
        self, relays: list[str], server_key: bytes, owner_key: bytes | None, identity: SigningKey, static: StaticKey
    ) -> Session2:
        rec = {"server_key": keys.encode(server_key), "owner_key": keys.encode(owner_key) if owner_key else ""}
        errors = []
        version_error = ""
        for url in relays:
            try:
                return await Session2.open(
                    url,
                    server_key,
                    identity.public,
                    static,
                    sign_static(identity, static.public),
                    self.device_name,
                    make_verifier(rec),
                )
            except IdentityError as e:
                raise UserError(_("This doesn't look like the right server: {error}").format(error=e)) from None
            except Exception as e:
                errors.append(str(e))
                if getattr(e, "mismatch", None) is not None:
                    version_error = str(e)
        if version_error:
            # No protocol version in common: say who needs updating.
            raise UserError(version_error)
        raise UserError(_("Couldn't reach the community. {error}").format(error=errors[-1] if errors else ""))

    def _target_from(self, source: dict) -> dict:
        """Normalise an invite string or a directory listing into a join target."""
        if source.get("invite"):
            try:
                inv = parse_invite(source["invite"])
            except CodeError as e:
                raise UserError(str(e)) from None
            return {
                "relays": list(inv.relays),
                "server_key": inv.server_key,
                "owner_key": inv.owner_key,
                "route_id": inv.route_id,
                "join_key": inv.join_key,
                "name": inv.name,
            }
        if source.get("listing"):
            lst = source["listing"]
            return {
                "relays": list(lst["relays"]),
                "server_key": keys.decode(lst["server_key"]),
                "owner_key": keys.decode(lst["community"]),
                "route_id": keys.decode(lst["route_id"]),
                "join_key": lst.get("public_join_key", ""),
                "name": lst.get("name", ""),
            }
        raise UserError(_("Paste an invite or pick a community from search."))

    async def preview(self, source: dict) -> dict:
        """Look the community up and show what joining means, before choosing
        an identity. Uses a throwaway key."""
        self.require_unlocked()
        t = self._target_from(source)
        cid = keys.encode(t["owner_key"])
        existing = self.vault.data["communities"].get(cid) if self.vault else None
        # Refresh relays from the route if the invite's are stale.
        relays = t["relays"]
        if t["route_id"]:
            with contextlib.suppress(Exception):
                found = await self.meta.route(t["route_id"], t["owner_key"])
                if found:
                    route = found[0]
                    relays = list(dict.fromkeys(list(route.relays) + relays))
                    if route.server_key != t["server_key"]:
                        t["server_key"] = route.server_key
        s = await self._temp_session(
            relays, t["server_key"], t["owner_key"], SigningKey.generate(), StaticKey.generate()
        )
        try:
            info = (await s.call(get_info=cp.GetInfo())).info
        finally:
            await s.close()
        if info.owner_key and info.owner_key != t["owner_key"]:
            raise UserError(_("The server answered for a different community."))
        return {
            "community": cid,
            "name": info.name,
            "description": info.description,
            "member_range": info.member_range,
            "join_policy": _enum_name(cp.JoinPolicy, info.join_policy, "JOIN_POLICY_"),
            "tags": list(info.tags),
            "icon": base64.b64encode(info.icon).decode() if info.icon else "",
            "notice": _(NOTICE),
            "has_key": bool(t["join_key"]),
            "already": bool(existing and existing.get("status") in ("active", "pending")),
            "identities": self.identity_choices(),
            "default_identity": self.settings.get("default_identity", "separate"),
        }

    async def join(self, source: dict, identity: str, display_name: str) -> dict:
        v = self.require_unlocked()
        name = (display_name or "").strip()
        if not 1 <= len(name) <= 64:
            raise UserError(_("Choose a display name (1 to 64 characters)."))
        t = self._target_from(source)
        cid = keys.encode(t["owner_key"])
        prev = v.data["communities"].get(cid)
        if prev and prev.get("status") in ("active", "pending"):
            raise UserError(_("You're already in this community."))
        created_identity = False
        if identity in ("", "new", None):
            iid = self._new_identity("member", label=t.get("name", ""))
            created_identity = True
        else:
            if identity not in v.data["identities"]:
                raise UserError(_("Unknown identity."))
            iid = identity
        device = self._new_device(iid)
        relays = t["relays"]
        if t["route_id"]:
            with contextlib.suppress(Exception):
                found = await self.meta.route(t["route_id"], t["owner_key"])
                if found:
                    relays = list(dict.fromkeys(list(found[0].relays) + relays))
                    t["server_key"] = found[0].server_key
        try:
            s = await self._temp_session(
                relays,
                t["server_key"],
                t["owner_key"],
                self.identity_key(iid),
                StaticKey(keys.decode(device["static"])),
            )
            try:
                res = (await s.call(join=cp.Join(join_key=t["join_key"], display_name=name))).join_result
            finally:
                await s.close()
        except (RpcError, Offline) as e:
            if created_identity:
                v.data["identities"].pop(iid, None)
            raise UserError(_rpc_message(e)) from None
        except BaseException:
            if created_identity:
                v.data["identities"].pop(iid, None)
            raise
        if res.status == cp.JOIN_STATUS_REFUSED:
            if created_identity:
                v.data["identities"].pop(iid, None)
            reason = REFUSALS.get(res.reason)
            return {"status": "refused", "reason": _(reason) if reason else _("The community refused the request.")}
        status = "active" if res.status == cp.JOIN_STATUS_ADMITTED else "pending"
        v.data["communities"][cid] = {
            "id": cid,
            "name": res.state.info.name if res.HasField("state") else t.get("name", ""),
            "server_key": keys.encode(t["server_key"]),
            "owner_key": cid,
            "route_id": keys.encode(t["route_id"]),
            "relays": relays,
            "identity": iid,
            "owner_identity": None,
            "display_name": name,
            "status": status,
            "device": device,
            "joined_at": int(time.time()),
            "cert_seq": 0,
        }
        v.keys_changed()
        self.save_vault()
        self._start(cid)
        self.community_changed(cid)
        await self.devices.community_joined(cid)
        return {"status": "admitted" if status == "active" else "pending", "community": cid}

    # ---------------------------------------------------------------- claim

    async def claim(self, claim_code: str, settings: dict, display_name: str, retry_for: float = 0.0) -> dict:
        v = self.require_unlocked()
        try:
            code = parse_claim(claim_code)
        except CodeError as e:
            raise UserError(str(e)) from None
        name = (display_name or "").strip()
        if not 1 <= len(name) <= 64:
            raise UserError(_("Choose a display name (1 to 64 characters)."))
        wizard = settings_from_dict(settings or {})
        if not wizard.name.strip():
            raise UserError(_("Give the community a name."))
        if not wizard.visibility:
            wizard.visibility = cp.VISIBILITY_PRIVATE
        if not wizard.join_policy:
            wizard.join_policy = cp.JOIN_POLICY_INVITE_ONLY
        wizard.search_enabled = settings.get("search_enabled", True) if settings else True
        owner_iid = self._new_identity("owner", label=wizard.name)
        member_iid = self._new_identity("member", label=wizard.name)
        owner = self.identity_key(owner_iid)
        cert_body = sp.ServerCert(owner_key=owner.public, server_key=code.server_key, issued_at=int(time.time()), seq=1)
        raw = cert_body.SerializeToString()
        cert = sp.SignedServerCert(cert=raw, signature=owner.sign(keys.CTX_SERVER_CERT, raw))
        device = self._new_device(member_iid)
        try:
            s = await self._temp_session(
                list(code.relays),
                code.server_key,
                None,
                self.identity_key(member_iid),
                StaticKey(keys.decode(device["static"])),
                retry_for=retry_for,
            )
            try:
                res = (
                    await s.call(claim=cp.Claim(secret=code.secret, cert=cert, settings=wizard, display_name=name))
                ).claim_result
            finally:
                await s.close()
        except BaseException as e:
            v.data["identities"].pop(owner_iid, None)
            v.data["identities"].pop(member_iid, None)
            if isinstance(e, RpcError | Offline):
                raise UserError(_rpc_message(e)) from None
            raise
        cid = keys.encode(owner.public)
        v.data["communities"][cid] = {
            "id": cid,
            "name": wizard.name,
            "server_key": keys.encode(code.server_key),
            "owner_key": cid,
            "route_id": keys.encode(code.route_id),
            "relays": list(code.relays),
            "identity": member_iid,
            "owner_identity": owner_iid,
            "display_name": name,
            "status": "active",
            "device": device,
            "joined_at": int(time.time()),
            "cert_seq": 1,
        }
        v.keys_changed()
        self.save_vault()
        self._start(cid)
        invite = self._invite_string(cid, res.first_invite.secret)
        self.community_changed(cid)
        await self.devices.community_joined(cid)
        return {"community": cid, "invite": invite}

    def _invite_string(self, cid: str, secret: str) -> str:
        rec = self.vault.data["communities"][cid]
        return encode_invite(
            ip.Invite(
                route_id=keys.decode(rec["route_id"]),
                owner_key=keys.decode(rec["owner_key"]),
                server_key=keys.decode(rec["server_key"]),
                relays=rec.get("relays", []),
                join_key=secret,
                name=rec.get("name", ""),
            )
        )

    # ---------------------------------------------------------------- hosting on this computer (hosting.py)

    def start_hosting(self) -> None:
        """Start the communities hosted on this computer (app start; they
        don't need the profile unlocked). Runs on the loop."""
        self.hosting.start_all()

    def _hosted_status(self, rec: dict) -> dict | None:
        hid = rec.get("hosted")
        if not hid or hid not in self.hosting.items:
            return None
        return self.hosting.status(hid)

    def _hosted_cid(self, hid: str) -> str:
        if self.vault is None or self.vault.locked:
            return ""
        for cid, rec in self.vault.data["communities"].items():
            if rec.get("hosted") == hid:
                return cid
        return ""

    def _hosting_changed(self, hid: str) -> None:
        cid = self._hosted_cid(hid)
        self.emit("hosting", {"id": hid, "community": cid})
        if cid:
            self.community_changed(cid)

    def hosting_list(self) -> list[dict]:
        """Every community hosted on this computer, with its server's state
        (for Settings; the community menu shows the same per community).
        Before unlock the names are unknown."""
        self.hosting.load()
        out = []
        for hid in sorted(self.hosting.items):
            cid = self._hosted_cid(hid)
            rec = self.vault.data["communities"].get(cid, {}) if cid and self.vault else {}
            conn = self.conns.get(cid)
            out.append(
                {
                    **self.hosting.status(hid),
                    "community": cid,
                    "name": rec.get("name", ""),
                    "server_status": conn.server_status if conn else None,
                }
            )
        return out

    def community_binary_info(self) -> dict:
        """The bundled community server program, for "Copy the community
        server" (Host on your own server)."""
        program = community_binary()
        return {
            "available": program is not None,
            "name": binary_name(),
            "platform": binary_platform(),
            "size": program.stat().st_size if program else 0,
        }

    async def save_community_binary(self, path: str) -> str:
        if not str(path or "").strip():
            raise UserError(_("Choose where to save the file."))
        return await copy_binary(str(path).strip())

    async def _hosting_relays(self) -> list[str]:
        """Relays a new hosted community starts on: the preferred relays,
        then the least loaded ones in the maintainers' registry. The server
        then keeps healthy relays from the registry by itself (automatic
        mode, with failover)."""
        out = list(self.settings.get("preferred_relays", []))
        try:
            registry = await self.meta.relays()
        except MetaError as e:
            log.info("hosting: relay registry unavailable: %s", e)
            registry = []
        registry.sort(key=lambda r: r["connections"] / max(r["capacity"], 1))
        out += [r["url"] for r in registry]
        return list(dict.fromkeys(out))[:4]

    async def host_community(self, settings: dict, display_name: str) -> dict:
        """Create a community served from this computer: `community init`
        into this profile's data folder, run the
        server, and claim it here, with no code to copy."""
        self.require_unlocked()
        name = (display_name or "").strip()
        if not 1 <= len(name) <= 64:
            raise UserError(_("Choose a display name (1 to 64 characters)."))
        if not str((settings or {}).get("name", "")).strip():
            raise UserError(_("Give the community a name."))
        if not self.meta.urls:
            raise UserError(_("Hosting needs at least one maintainer node (Settings → Network)."))
        relays = await self._hosting_relays()
        if not relays:
            raise UserError(
                _(
                    "No relay is available to host through. Your maintainer nodes list none, and you haven't "
                    "added any of your own (Settings → Network)."
                )
            )
        h = await self.hosting.create(list(self.meta.urls), relays)
        claimed = False
        try:
            await self.hosting.start(h.hid)
            res = await self.claim(self.hosting.claim_code(h.hid), settings, display_name, retry_for=HOST_CLAIM_SECONDS)
            claimed = True
        finally:
            if not claimed:
                await self.hosting.remove(h.hid)
        rec = self.vault.data["communities"][res["community"]] if self.vault else {}
        rec["hosted"] = h.hid
        self.save_vault()
        self.community_changed(res["community"])
        log.info("hosting: %s serves a new community", h.hid)
        return {**res, "hosted": self.hosting.status(h.hid)}

    def _hosted_id(self, cid: str) -> str:
        v = self.require_unlocked()
        hid = v.data["communities"].get(cid, {}).get("hosted", "")
        if not hid or hid not in self.hosting.items:
            raise UserError(_("That community isn't hosted on this computer."))
        return hid

    async def hosting_start(self, cid: str) -> dict:
        hid = self._hosted_id(cid)
        await self.hosting.start(hid)
        return self.hosting.status(hid)

    async def hosting_stop(self, cid: str) -> dict:
        hid = self._hosted_id(cid)
        await self.hosting.stop(hid)
        return self.hosting.status(hid)

    async def hosting_reregister(self, cid: str) -> dict:
        """Bring back a hosted community the maintainers shut down while it
        was offline for too long (docs/protocol.md §7.4)."""
        hid = self._hosted_id(cid)
        await self.hosting.reregister(hid)
        conn = self.conns.get(cid)
        if conn is not None:
            conn.reconnect_now()
        return self.hosting.status(hid)

    def hosting_running(self) -> int:
        """How many hosted servers run now (the quit warning; any thread)."""
        return self.hosting.running

    # ---------------------------------------------------------------- leaving and removal

    async def leave(self, cid: str, delete_key: bool = True) -> dict:
        v = self.require_unlocked()
        rec = v.data["communities"].get(cid)
        if rec is None:
            raise UserError(_("Unknown community."))
        conn = self.conns.get(cid)
        if conn is not None and conn.status == "online":
            try:
                await conn.call(leave=cp.Leave())
            except RpcError as e:
                raise UserError(str(e)) from None
            except Offline:
                pass
        await self._forget(cid, delete_key=delete_key)
        await self.devices.community_left(cid)
        return {"ok": True}

    async def _forget(self, cid: str, delete_key: bool) -> None:
        v = self.vault
        assert v is not None
        conn = self.conns.pop(cid, None)
        if conn is not None:
            await conn.stop()
        self.cache(cid).wipe()
        self.caches.pop(cid, None)
        await self.voice.left_community(cid)
        self.statuses.community_left(cid)
        rec = v.data["communities"].pop(cid, None)
        if rec and delete_key:
            for iid in (rec.get("identity"), rec.get("owner_identity"), rec.get("owner_pending")):
                if iid and not any(
                    iid in (c.get("identity"), c.get("owner_identity"), c.get("owner_pending"))
                    for c in v.data["communities"].values()
                ):
                    v.data["identities"].pop(iid, None)
        v.keys_changed()
        self.save_vault()
        self.emit("community_removed", {"id": cid})

    async def community_ended(self, cid: str, status: str) -> None:
        """The server says we're removed/banned or this device is revoked:
        stop reconnecting and delete the local copy."""
        v = self.vault
        if v is None:
            return
        rec = v.data["communities"].get(cid)
        if rec is None:
            return
        rec["status"] = status
        rec["removed_reason"] = {
            "removed": _("You were removed from this community."),
            "banned": _("You were banned from this community."),
            "device_revoked": _("This device was removed from your account in this community."),
            "device_limit": _("This community allows no more devices for your account."),
        }.get(status, _("You no longer have access."))
        if status != "device_limit":
            self.cache(cid).wipe()
        self.save_vault()
        await self.voice.left_community(cid)
        self.community_changed(cid)
        self.emit(
            "toast",
            {
                "text": _("{community}: {reason}").format(
                    community=rec.get("name", _("A community")), reason=rec["removed_reason"]
                )
            },
        )

    async def dismiss(self, cid: str) -> dict:
        v = self.require_unlocked()
        rec = v.data["communities"].get(cid)
        if rec and rec.get("status") not in ("active", "pending"):
            await self._forget(cid, delete_key=True)
        return {"ok": True}

    async def retry(self, cid: str) -> dict:
        v = self.require_unlocked()
        rec = v.data["communities"].get(cid)
        if rec is None:
            raise UserError(_("Unknown community."))
        if rec.get("status") == "device_limit":
            rec["status"] = "active"
            self.save_vault()
        self._start(cid)
        self.conns[cid].reconnect_now()
        return {"ok": True}

    async def after_connect(self, cid: str) -> None:
        # In the background: this can run inside the session's event handler,
        # which must not wait for a reply on the same session.
        self.spawn(self._refresh_members(cid))
        self.spawn(self.voice.reconnected(cid))
        self.spawn(self.devices.send_pending_revocations(cid))
        self.spawn(self.statuses.community_connected(cid))
        # Publishing the contact card asks the server: never awaited here.
        self.spawn(self.dms.community_online(cid))

    async def _refresh_members(self, cid: str) -> None:
        """The member list (mention tokens, presence); best effort."""
        with contextlib.suppress(UserError):
            await self.members(cid)

    # ---------------------------------------------------------------- messages

    def _ui(self, cid: str, m: dict) -> dict:
        """A message as the UI gets it: file keys (attachments and the link
        preview image) stay here, looked up by file id when a file is saved
        or shown; they never enter the webview."""

        def strip(f: dict) -> dict:
            if f.get("key"):
                self._file_keys[(cid, f["file_id"])] = f["key"]
            return {k: v for k, v in f.items() if k != "key"}

        out = {**m, "files": [strip(f) for f in m.get("files") or []]}
        p = m.get("preview")
        if p and p.get("image"):
            out["preview"] = {**p, "image": strip(p["image"])}
        return out

    def _me(self, cid: str) -> tuple[str, set[int]]:
        conn = self.conns.get(cid)
        if conn is None or conn.state is None:
            return "", set()
        return keys.encode(conn.state.self.key), set(conn.state.self.role_ids)

    def _mark_mentions(self, cid: str, m: dict, live: bool) -> dict:
        my_key, my_roles = self._me(cid)
        old = self.cache(cid).find(m["channel_id"], m["id"])
        m["mentions_me"] = mentions_me(m, my_key, my_roles, live) or bool(
            old and old.get("mentions_me") and not m.get("deleted")
        )
        return m

    def cache_message(self, cid: str, m: dict, notify: bool = True) -> None:
        self._mark_mentions(cid, m, live=notify)
        cache = self.cache(cid)
        new = cache.upsert(m)
        self._schedule_cache_save()
        if m["author"] == self._me(cid)[0] and m["id"] > self._last_read(cid, m["channel_id"]):
            # What you post you've read.
            self._set_read(cid, m["channel_id"], m["id"])
        self.emit("message", {"community": cid, "message": self._ui(cid, m)})
        self._emit_unread(cid, m["channel_id"])
        if new and notify:
            self._maybe_notify(cid, m)

    def apply_update(self, cid: str, u: cp.MessageUpdate) -> dict | None:
        """Apply a reactions, pin or link preview update to a message this
        client already has; ignore it otherwise (updates carry no text, so
        an unknown message stays unknown). Returns the message for the UI."""
        change = update_dict(u)
        if change is None:
            return None
        old = self.cache(cid).find(change["channel_id"], change["id"])
        if old is None or old.get("deleted"):
            return None
        m = {**old, **change}
        self.cache_message(cid, m, notify=False)
        return self._ui(cid, m)

    def channel_event(self, cid: str, ev: cp.ChannelEvent, channel_id: int = 0) -> None:
        kind = ev.WhichOneof("kind")
        if kind == "message":
            self.cache_message(cid, message_dict(ev.message))
        elif kind == "edited":
            self.cache_message(cid, message_dict(ev.edited), notify=False)
        elif kind == "update":
            self.apply_update(cid, ev.update)
        elif kind == "typing":
            member = keys.encode(ev.typing.member)
            if member != self._me(cid)[0]:
                self.emit("typing", {"community": cid, "channel": channel_id, "member": member, "name": ev.typing.name})
        elif kind in ("deleted", "deleted_many"):
            ids = [ev.deleted] if kind == "deleted" else list(ev.deleted_many.ids)
            for chid in list(self.cache(cid).messages):
                hit = False
                for mid in ids:
                    m = self.cache(cid).mark_deleted(chid, mid)
                    if m:
                        hit = True
                        self.emit("message", {"community": cid, "message": self._ui(cid, m)})
                if hit:
                    self._schedule_cache_save()
                    self._emit_unread(cid, chid)

    # ---------------------------------------------------------------- unread markers and mention badges

    def _read_markers(self, cid: str) -> dict:
        if self.state is None:
            return {}
        return self.state.get("read_markers", {}).get(cid, {})

    def _last_read(self, cid: str, channel_id: int) -> int:
        return int(self._read_markers(cid).get(str(channel_id), 0))

    def _set_read(self, cid: str, channel_id: int, message_id: int) -> None:
        if self.state is None:
            return
        all_markers = dict(self.state.get("read_markers", {}))
        mine = dict(all_markers.get(cid, {}))
        mine[str(channel_id)] = int(message_id)
        all_markers[cid] = mine
        self.state.set("read_markers", all_markers)

    def _counts(self, cid: str, channel_id: int) -> tuple[int, int]:
        return channel_counts(self.cache(cid).channel(channel_id), self._last_read(cid, channel_id), self._me(cid)[0])

    def _emit_unread(self, cid: str, channel_id: int) -> None:
        unread, mentions = self._counts(cid, channel_id)
        summary = self.community_summary(cid)
        self.emit(
            "unread",
            {
                "community": cid,
                "channel": channel_id,
                "unread": unread,
                "mentions": mentions,
                "last_read": self._last_read(cid, channel_id),
                "community_unread": summary["unread"],
                "community_mentions": summary["mentions"],
            },
        )

    def mark_read(self, cid: str, channel_id: int, message_id: int = 0) -> dict:
        """Mark a channel seen up to message_id (0 = its newest cached
        message)."""
        self.require_unlocked()
        msgs = self.cache(cid).channel(channel_id)
        upto = int(message_id) or (msgs[-1]["id"] if msgs else 0)
        if upto > self._last_read(cid, channel_id):
            self._set_read(cid, channel_id, upto)
        self._emit_unread(cid, channel_id)
        return {"ok": True, "last_read": self._last_read(cid, channel_id)}

    def _maybe_notify(self, cid: str, m: dict) -> None:
        rec = self.vault.data["communities"].get(cid, {}) if self.vault else {}
        me = self.conns.get(cid)
        my_key = keys.encode(me.state.self.key) if me and me.state else ""
        if m["author"] == my_key or m.get("deleted"):
            return
        title, body = should_notify(self.settings, cid, m, rec.get("display_name", ""))
        if title is None or self.statuses.dnd():
            # Do not disturb: unread marks and mention counts still count.
            return
        if not self.unlocked:
            # While locked, only that something happened: no sender, text,
            # channel or community. The community id stays so the page
            # shows a badge rather than a toast (app.js).
            self.emit(
                "notify",
                {
                    "title": PRODUCT_NAME,
                    "community_name": "",
                    "body": _("New activity"),
                    "community": cid,
                    "channel": 0,
                },
            )
            return
        chan = me.channel(m["channel_id"]) if me else None
        self.emit(
            "notify",
            {
                "title": f"{title} · #{chan.name}" if chan else title,
                "community_name": rec.get("name", ""),
                "body": body,
                "community": cid,
                "channel": m["channel_id"],
            },
        )

    async def history(self, cid: str, channel_id: int, before_id: int = 0) -> dict:
        conn = self.conn(cid)
        cache = self.cache(cid)
        try:
            reply = await conn.call(
                history=cp.History(channel_id=channel_id, before_id=before_id, limit=50), timeout=15
            )
            msgs = [self._mark_mentions(cid, message_dict(m), live=False) for m in reply.messages.messages]
            cache.merge(channel_id, msgs, newest=not before_id, more=reply.messages.more)
            self._schedule_cache_save()
            ui = [self._ui(cid, m) for m in msgs]
            return {"messages": ui, "more": reply.messages.more, "offline": False}
        except (Offline, RpcError) as e:
            msgs = cache.channel(channel_id)
            msgs = [m for m in msgs if m["id"] < before_id][-50:] if before_id else msgs[-50:]
            return {"messages": [self._ui(cid, m) for m in msgs], "more": False, "offline": True, "error": str(e)}

    def cached_messages(self, cid: str, channel_id: int) -> list[dict]:
        return [self._ui(cid, m) for m in self.cache(cid).channel(channel_id)[-100:]]

    async def send(
        self, cid: str, channel_id: int, text: str, reply_to: int = 0, files: list[str] | None = None, progress=None
    ) -> dict:
        """Post a message with up to 10 attachments (uploaded first, then
        attached to the one message). Typed @Name mentions become tokens."""
        conn = self.conn(cid)
        files = list(files or [])
        if len(files) > MAX_ATTACHMENTS:
            raise UserError(_("You can attach at most {n} files to one message.").format(n=MAX_ATTACHMENTS))
        if not text.strip() and not files:
            raise UserError(_("Type a message or attach a file."))
        file_ids = [await self._stage(cid, conn, channel_id, path, progress) for path in files]
        text = await self._encode_mentions(cid, conn, text)
        try:
            reply = await conn.call(
                post=cp.Post(channel_id=channel_id, text=text, reply_to=reply_to, file_ids=file_ids)
            )
        except (RpcError, Offline) as e:
            raise UserError(str(e)) from None
        m = message_dict(reply.message)
        self.cache_message(cid, m, notify=False)
        return self._ui(cid, m)

    async def _encode_mentions(self, cid: str, conn: CommunityConnection, text: str) -> str:
        """Typed @Name mentions to tokens, against a fresh member list (someone
        may have joined or changed their nickname since the last one)."""
        if "@" not in text:
            return text
        with contextlib.suppress(UserError):
            await self.members(cid)
        roles = [{"id": r.id, "name": r.name} for r in conn.state.roles] if conn.state else []
        return encode_mentions(text, self._members.get(cid, []), roles)

    async def react(self, cid: str, message_id: int, emoji: str, remove: bool = False) -> dict | None:
        try:
            reply = await self.conn(cid).call(react=cp.React(message_id=message_id, emoji=emoji, remove=remove))
        except (RpcError, Offline) as e:
            raise UserError(str(e)) from None
        # The reply is the message's reactions, not the message.
        return self.apply_update(cid, reply.message_update)

    async def pin(self, cid: str, message_id: int, unpin: bool = False) -> dict | None:
        try:
            reply = await self.conn(cid).call(pin=cp.Pin(message_id=message_id, unpin=unpin))
        except (RpcError, Offline) as e:
            raise UserError(str(e)) from None
        return self.apply_update(cid, reply.message_update)

    async def pins(self, cid: str, channel_id: int) -> list[dict]:
        try:
            reply = await self.conn(cid).call(list_pins=cp.ListPins(channel_id=channel_id))
        except (RpcError, Offline) as e:
            raise UserError(str(e)) from None
        return [self._ui(cid, message_dict(m)) for m in reply.pins.messages]

    async def typing(self, cid: str, channel_id: int) -> dict:
        """Tell the channel this member is typing. Best effort: the server
        rate-limits, and a turned-off indicator is not an error for the UI."""
        try:
            await self.conn(cid).call(typing=cp.Typing(channel_id=channel_id), timeout=10)
        except (RpcError, Offline) as e:
            return {"ok": False, "reason": str(e)}
        return {"ok": True}

    async def history_around(self, cid: str, channel_id: int, message_id: int) -> dict:
        """Messages around one message (jump to message): up to 25 before
        and 25 after it, merged into the cache."""
        conn = self.conn(cid)
        try:
            before = await conn.call(
                history=cp.History(channel_id=channel_id, before_id=message_id + 1, limit=25), timeout=15
            )
            after = await conn.call(
                history=cp.History(channel_id=channel_id, after_id=message_id, limit=25), timeout=15
            )
        except (RpcError, Offline) as e:
            raise UserError(str(e)) from None
        msgs = [
            self._mark_mentions(cid, message_dict(m), live=False)
            for m in [*before.messages.messages, *after.messages.messages]
        ]
        self.cache(cid).merge(channel_id, msgs)
        self._schedule_cache_save()
        if not any(m["id"] == message_id for m in msgs):
            raise UserError(_("That message is gone."))
        return {"messages": [self._ui(cid, m) for m in msgs], "more_before": before.messages.more}

    async def edit(self, cid: str, message_id: int, text: str) -> dict:
        conn = self.conn(cid)
        text = await self._encode_mentions(cid, conn, text)
        try:
            reply = await conn.call(edit=cp.Edit(message_id=message_id, text=text))
        except (RpcError, Offline) as e:
            raise UserError(str(e)) from None
        m = message_dict(reply.message)
        self.cache_message(cid, m, notify=False)
        return self._ui(cid, m)

    async def delete_message(self, cid: str, channel_id: int, message_id: int) -> dict:
        try:
            await self.conn(cid).call(delete=cp.DeleteMessage(message_id=message_id))
        except (RpcError, Offline) as e:
            raise UserError(str(e)) from None
        m = self.cache(cid).mark_deleted(channel_id, message_id)
        if m:
            self.emit("message", {"community": cid, "message": self._ui(cid, m)})
        return {"ok": True}

    async def search_messages(self, cid: str, query: str, channel_id: int = 0) -> list[dict]:
        try:
            reply = await self.conn(cid).call(search=cp.SearchQuery(query=query, channel_id=channel_id, limit=50))
        except (RpcError, Offline) as e:
            raise UserError(str(e)) from None
        return [self._ui(cid, message_dict(m)) for m in reply.search_results.messages]

    async def _stage(self, cid: str, conn: CommunityConnection, channel_id: int, path: str, progress=None) -> int:
        """Upload one file as a staged attachment; returns its file id."""
        p = Path(path).expanduser()
        try:
            size = p.stat().st_size
        except OSError as e:
            raise UserError(_("Can't read {name}: {reason}").format(name=p.name, reason=e.strerror)) from None
        limit = min((conn.state.settings.max_file_size if conn.state else 0) or MAX_FILE, MAX_FILE)
        if size > limit:
            mb = 1048576
            raise UserError(
                _("{name} is {size} MB; this community allows up to {limit} MB per file.").format(
                    name=p.name, size=f"{size / mb:.1f}", limit=f"{limit / mb:.0f}"
                )
            )
        if size == 0:
            raise UserError(_("{name} is empty.").format(name=p.name))
        import mimetypes

        mime = mimetypes.guess_type(p.name)[0] or "application/octet-stream"
        try:
            started = (
                await conn.call(
                    upload_begin=cp.UploadBegin(channel_id=channel_id, name=p.name, size=size, mime=mime, staged=True)
                )
            ).upload_started
            digest = hashlib.sha256()
            sent = 0
            with p.open("rb") as f:
                while chunk := await asyncio.to_thread(f.read, started.chunk_size):
                    digest.update(chunk)
                    await conn.call(
                        upload_chunk=cp.UploadChunk(upload_id=started.upload_id, offset=sent, data=chunk), timeout=120
                    )
                    sent += len(chunk)
                    if progress:
                        progress(sent, size)
                    self.emit(
                        "upload", {"community": cid, "channel": channel_id, "name": p.name, "sent": sent, "size": size}
                    )
            reply = await conn.call(
                upload_finish=cp.UploadFinish(upload_id=started.upload_id, sha256=digest.digest()), timeout=120
            )
        except (RpcError, Offline) as e:
            raise UserError(f"{p.name}: {e}") from None
        return reply.staged_file.file.file_id

    # ---------------------------------------------------------------- files

    async def upload(self, cid: str, channel_id: int, path: str, text: str = "", progress=None) -> dict:
        """One file with an optional caption (a message with one attachment)."""
        return await self.send(cid, channel_id, text, files=[path], progress=progress)

    async def fetch_file(self, cid: str, file: dict) -> bytes:
        cache = self.cache(cid)
        data = cache.get_file(file["file_id"])
        if data is not None:
            return data
        key_b64 = self._file_keys.get((cid, file["file_id"]), "")
        if file.get("expired") or not key_b64:
            raise UserError(_("That file has expired."))
        conn = self.conn(cid)
        key = keys.decode(key_b64)
        parts = []
        idx = 0
        while True:
            try:
                fc = (await conn.call(file_get=cp.FileGet(file_id=file["file_id"], chunk=idx), timeout=120)).file_chunk
            except (RpcError, Offline) as e:
                raise UserError(str(e)) from None
            try:
                parts.append(
                    decrypt_with_nonce(key, file_chunk_nonce(idx), fc.data, file_chunk_aad(file["file_id"], idx))
                )
            except DecryptError:
                raise UserError(_("The file couldn't be opened. It may be damaged.")) from None
            idx += 1
            if idx >= fc.chunks:
                break
        data = b"".join(parts)
        if "size" in file and len(data) != int(file["size"]):
            raise UserError(_("The file arrived incomplete; try again."))
        cache.put_file(file["file_id"], data)
        return data

    async def save_file(self, cid: str, file: dict, path: str) -> dict:
        data = await self.fetch_file(cid, file)
        target = Path(path).expanduser()

        def write() -> None:
            # Whole or absent (the user chose the folder: their umask applies).
            tmp = target.with_name(f".{target.name}.part")
            try:
                tmp.write_bytes(data)
                tmp.replace(target)
            except OSError:
                tmp.unlink(missing_ok=True)
                raise

        await asyncio.to_thread(write)
        return {"ok": True, "path": str(target)}

    async def file_preview(self, cid: str, file: dict) -> str:
        """A data: URI for inline image previews (images up to 8 MB). The
        type comes from the bytes, not the sender: only PNG, JPEG, GIF and
        WebP are shown (an SVG would be a document in an <img>)."""
        if not str(file.get("mime", "")).startswith("image/") or file.get("size", 0) > 8 << 20:
            raise UserError(_("No preview for this file."))
        data = await self.fetch_file(cid, file)
        mime = image_mime(data)
        if not mime:
            raise UserError(_("No preview for this file."))
        return f"data:{mime};base64," + base64.b64encode(data).decode()

    # ---------------------------------------------------------------- profile in a community

    async def set_display_name(self, cid: str, name: str) -> dict:
        name = (name or "").strip()
        if not 1 <= len(name) <= 64:
            raise UserError(_("Display names are 1 to 64 characters."))
        try:
            await self.conn(cid).call(set_profile=cp.SetProfile(display_name=name))
        except (RpcError, Offline) as e:
            raise UserError(str(e)) from None
        self.vault.data["communities"][cid]["display_name"] = name
        self.save_vault()
        return {"ok": True}

    async def set_avatar(self, cid: str, path: str | None) -> dict:
        if path:
            try:
                data = await asyncio.to_thread(read_at_most, Path(path).expanduser(), 256 * 1024)
            except OSError as e:
                raise UserError(_("Can't read that image: {reason}").format(reason=e.strerror)) from None
            if data is None:
                raise UserError(_("Avatars can be at most 256 KB."))
            msg = cp.SetProfile(avatar=data)
        else:
            msg = cp.SetProfile(clear_avatar=True)
        try:
            await self.conn(cid).call(set_profile=msg)
        except (RpcError, Offline) as e:
            raise UserError(str(e)) from None
        return {"ok": True}

    async def avatar(self, cid: str, member: str, version: int) -> str:
        key = (cid, member, version)
        if key in self._avatar_cache:
            return self._avatar_cache[key]
        try:
            res = (await self.conn(cid).call(get_avatar=cp.GetAvatar(member=keys.decode(member)))).avatar
        except RpcError, Offline:
            return ""
        mime = image_mime(res.avatar) or "image/png"
        uri = f"data:{mime};base64," + base64.b64encode(res.avatar).decode()
        self._avatar_cache[key] = uri
        return uri

    async def members(self, cid: str) -> list[dict]:
        try:
            reply = await self.conn(cid).call(list_members=cp.ListMembers())
        except (RpcError, Offline) as e:
            raise UserError(str(e)) from None
        self._members[cid] = [_member_dict(m) for m in reply.members.members]
        self.statuses.presence_changed()
        return self._members[cid]

    def presence(self, cid: str, member: str, online: bool) -> None:
        for m in self._members.get(cid, []):
            if m["key"] == member:
                m["online"] = online
        self.emit("presence", {"community": cid, "member": member, "online": online})
        self.statuses.presence_changed()

    async def set_nickname(self, cid: str, member: str, nickname: str) -> dict:
        res = await self.admin(cid, "set_nickname", {"member": member, "nickname": nickname})
        with contextlib.suppress(UserError):
            await self.members(cid)
        return res

    async def update_profile(self, cid: str, about: str, pronouns: str) -> dict:
        about, pronouns = (about or "").strip(), (pronouns or "").strip()
        if len(about) > 190:
            raise UserError(_("About is at most 190 characters."))
        if len(pronouns) > 40:
            raise UserError(_("Pronouns are at most 40 characters."))
        try:
            await self.conn(cid).call(update_profile=cp.UpdateProfile(about=about, pronouns=pronouns))
        except (RpcError, Offline) as e:
            raise UserError(str(e)) from None
        return {"ok": True}

    # ---------------------------------------------------------------- admin

    async def admin(self, cid: str, op: str, params: dict) -> dict:
        from openarcanum.admin_ops import OpError, build_op

        conn = self.conn(cid)
        try:
            field, msg = build_op(op, params, conn)
        except OpError as e:
            raise UserError(str(e)) from None
        if field in ("prepare_key_rotation", "commit_key_rotation"):
            raise UserError(_("Use Change server key in Community settings."))
        try:
            res = await conn.admin(field, msg)
        except (RpcError, Offline) as e:
            raise UserError(str(e)) from None
        out: dict[str, Any] = {
            "ok": True,
            "id": res.id,
            "removed": res.removed,
            "deleted": res.deleted,
            "backup_path": res.backup_path,
            # A ban's network block: "blocked", "pending", "skipped" or "".
            "network_block": _network_block(res.network_block),
        }
        if res.HasField("join_key") and res.join_key.secret:
            out["join_key"] = _join_key_dict(res.join_key)
            out["invite"] = self._invite_string(cid, res.join_key.secret)
        return out

    async def admin_query(self, cid: str, what: str, params: dict | None = None) -> Any:
        conn = self.conn(cid)
        params = params or {}
        try:
            if what == "join_keys":
                reply = await conn.call(list_join_keys=cp.ListJoinKeys())
                out = []
                for k in reply.join_keys.keys:
                    d = _join_key_dict(k)
                    if k.secret:
                        d["invite"] = self._invite_string(cid, k.secret)
                    out.append(d)
                return out
            if what == "pending":
                return [_member_dict(m) for m in (await conn.call(list_pending=cp.ListPending())).members.members]
            if what == "bans":
                return [
                    {
                        "key": keys.encode(b.key),
                        "display_name": b.display_name,
                        "reason": b.reason,
                        "at": b.at,
                        "network_block": _network_block(b.network_block),
                        "network_block_until": b.network_block_until,
                    }
                    for b in (await conn.call(list_bans=cp.ListBans())).bans.bans
                ]
            if what == "audit":
                return (await self.moderation.audit(cid, params))["entries"]
            if what == "audit_page":
                return await self.moderation.audit(cid, params)
            if what == "reports":
                return await self.moderation.reports(cid, bool(params.get("include_closed")))
            if what == "automod_rules":
                return await self.moderation.automod_rules(cid)
        except (RpcError, Offline) as e:
            raise UserError(str(e)) from None
        raise UserError(_("unknown query"))

    # ---------------------------------------------------------------- ownership (docs/protocol.md §4.5)

    def _ownership(self, cid: str, conn: CommunityConnection) -> dict:
        """Owners (checked against the verified owner chain), invitations and
        key requests waiting for an owner, for the Ownership settings."""
        st = conn.state
        assert st is not None
        verified = conn.owner_set
        me = st.self.key
        owners = [
            {
                "owner_key": keys.encode(o.owner_key),
                "member": keys.encode(o.member) if o.member else "",
                "display_name": o.display_name or keys.fingerprint(o.owner_key),
                "me": bool(o.member) and o.member == me,
                "founder": keys.encode(o.owner_key) == cid,
                "leaving": o.leaving,
            }
            for o in st.owners
            if verified is not None and verified.has(o.owner_key)
        ]
        pending = [
            {"member": keys.encode(p.member), "display_name": p.display_name, "submitted": p.HasField("add")}
            for p in st.pending_owners
        ]
        return {"owners": owners, "pending_owners": pending, "single_owner": len(owners) == 1}

    def owner_keys_changed(self, cid: str, owner_set: OwnerSet, state: cp.SyncState) -> bool:
        """Follow the verified owner set: a submitted owner key becomes this
        member's owner key once an owner signed it in; a removed key (or the
        key of a cancelled request) is deleted, since it can never sign for
        this community again. Returns True if the record changed."""
        v = self.vault
        rec = v.data["communities"].get(cid) if v else None
        if rec is None:
            return False
        changed = False
        name = rec.get("name", _("the community"))
        pending = rec.get("owner_pending")
        if pending and pending in v.data["identities"]:
            pub = self.identity_public(pending)
            if owner_set.has(pub) and not rec.get("owner_identity"):
                rec["owner_identity"] = rec.pop("owner_pending")
                self.emit("toast", {"text": _("You're now an owner of {community}.").format(community=name)})
                changed = True
            elif pub in owner_set.ever or not state.self.owner_invited:
                rec.pop("owner_pending")
                self._drop_identity(pending)
                changed = True
        current = rec.get("owner_identity")
        if current and current in v.data["identities"]:
            pub = self.identity_public(current)
            if pub in owner_set.ever and not owner_set.has(pub):
                rec["owner_identity"] = None
                self._drop_identity(current)
                self.emit("toast", {"text": _("You're no longer an owner of {community}.").format(community=name)})
                changed = True
        if changed:
            v.keys_changed()
            self.community_changed(cid)
        return changed

    def _drop_identity(self, iid: str) -> None:
        v = self.vault
        assert v is not None
        if not any(
            iid in (c.get("identity"), c.get("owner_identity"), c.get("owner_pending"))
            for c in v.data["communities"].values()
        ):
            v.data["identities"].pop(iid, None)

    def _owner_context(self, cid: str) -> tuple[CommunityConnection, dict, SigningKey, OwnerSet, list]:
        conn = self.conn(cid)
        rec = self.vault.data["communities"][cid]
        if not rec.get("owner_identity"):
            raise UserError(_("Only an owner can do that."))
        if conn.state is None or conn.owner_set is None:
            raise UserError(_("You're offline from this community right now."))
        owner = self.identity_key(rec["owner_identity"])
        if not conn.owner_set.has(owner.public):
            raise UserError(_("Your owner key isn't in this community's owner list any more."))
        return conn, rec, owner, conn.owner_set, list(conn.state.cert.owner_chain)

    def _next_cert_seq(self, conn: CommunityConnection, rec: dict) -> int:
        seq = rec.get("cert_seq", 1)
        if conn.state is not None and conn.state.HasField("cert"):
            c = sp.ServerCert()
            c.ParseFromString(conn.state.cert.cert)
            seq = max(seq, c.seq)
        return seq + 1

    async def _change_owners(self, cid: str, add: sp.OwnerAdd | None = None, remove: bytes | None = None) -> None:
        conn, rec, owner, owner_set, chain = self._owner_context(cid)
        change = sign_change(owner, owner_set, add=add, remove=remove)
        nxt = owner_set.clone()
        try:
            nxt.apply(change)
        except keys.BadSignature as e:
            raise UserError(_owner_error(str(e))) from None
        if remove is not None and remove == owner.public:
            # Removing yourself: you can't sign a cert under a set without
            # you. The server stops treating you as an owner and another
            # owner's client signs the removal (complete_owner_leaves).
            msg = cp.ChangeOwners(change=change)
        else:
            cert = issue_cert(owner, conn.state.info.server_key, self._next_cert_seq(conn, rec), nxt, [*chain, change])
            msg = cp.ChangeOwners(change=change, cert=cert)
        try:
            await conn.admin("change_owners", msg)
        except (RpcError, Offline) as e:
            raise UserError(_owner_error(str(e))) from None
        await conn.reload_state()

    async def invite_owner(self, cid: str, member: str) -> dict:
        """Ask a member to become an owner. Their client makes its own owner
        key; an owner then confirms it (confirm_owner)."""
        conn, _rec, _key, owner_set, _signer = self._owner_context(cid)
        if len(owner_set.owners) >= MAX_OWNERS:
            raise UserError(_("A community can have at most {n} owners.").format(n=MAX_OWNERS))
        try:
            await conn.admin("invite_owner", cp.InviteOwner(member=keys.decode(member)))
        except (RpcError, Offline) as e:
            raise UserError(str(e)) from None
        await conn.reload_state()
        return {"ok": True}

    async def cancel_owner_invite(self, cid: str, member: str) -> dict:
        conn = self.conn(cid)
        try:
            await conn.admin("invite_owner", cp.InviteOwner(member=keys.decode(member), cancel=True))
        except (RpcError, Offline) as e:
            raise UserError(str(e)) from None
        await conn.reload_state()
        return {"ok": True}

    async def accept_owner_invite(self, cid: str) -> dict:
        """Become an owner: generate an owner key on this device and send only
        its public half, signed by it and by this member's key. It counts
        once an owner signs it into the owner set."""
        conn = self.conn(cid)
        v = self.require_unlocked()
        rec = v.data["communities"][cid]
        if rec.get("owner_identity"):
            raise UserError(_("You're already an owner of this community."))
        if conn.state is None or not conn.state.self.owner_invited:
            raise UserError(_("Nobody has invited you to become an owner."))
        stale = rec.pop("owner_pending", None)
        if stale:
            self._drop_identity(stale)
        iid = self._new_identity("owner", label=rec.get("name", ""))
        add = request_owner_key(self.identity_key(iid), self.identity_key(rec["identity"]), keys.decode(cid))
        rec["owner_pending"] = iid
        try:
            await conn.admin("submit_owner_key", cp.SubmitOwnerKey(add=add))
        except (RpcError, Offline) as e:
            rec.pop("owner_pending", None)
            v.data["identities"].pop(iid, None)
            raise UserError(str(e)) from None
        v.keys_changed()
        self.save_vault()
        await self.devices.owner_key_made(cid, iid)
        await conn.reload_state()
        return {"ok": True}

    async def decline_owner_invite(self, cid: str) -> dict:
        conn = self.conn(cid)
        if conn.state is None:
            raise UserError(_("You're offline from this community right now."))
        return await self.cancel_owner_invite(cid, keys.encode(conn.state.self.key))

    async def confirm_owner(self, cid: str, member: str) -> dict:
        """Sign a member's submitted owner key into the owner set."""
        conn, _rec, _key, _owners, _signer = self._owner_context(cid)
        want = keys.decode(member)
        p = next((p for p in conn.state.pending_owners if p.member == want and p.HasField("add")), None)
        if p is None:
            raise UserError(_("That member hasn't sent an owner key yet."))
        try:
            req = verify_owner_add(p.add, keys.decode(cid))
        except keys.BadSignature:
            raise UserError(_("That member's owner key request couldn't be verified.")) from None
        if req.member != want:
            raise UserError(_("That owner key request is for another member."))
        await self._change_owners(cid, add=p.add)
        return {"ok": True}

    async def remove_owner(self, cid: str, owner_key: str) -> dict:
        """Remove an owner key: another owner's, or your own while another
        owner remains."""
        await self._change_owners(cid, remove=keys.decode(owner_key))
        return {"ok": True}

    def complete_owner_leaves(self, cid: str, owner_set: OwnerSet, state: cp.SyncState) -> None:
        """Another owner removed themselves (docs/protocol.md §4.5): as an
        owner, sign their removal and the new cert, so their key leaves the
        chain. Several owners may race; the server takes the first."""
        rec = self.vault.data["communities"].get(cid) if self.vault else None
        if rec is None or not rec.get("owner_identity") or not state.self.owner:
            return
        leaving = [
            bytes(o.owner_key)
            for o in state.owners
            if o.leaving and o.member != state.self.key and owner_set.has(o.owner_key)
        ]
        if not leaving or cid in self._completing_leaves:
            return
        self._completing_leaves.add(cid)

        async def complete() -> None:
            try:
                for k in leaving:
                    try:
                        await self._change_owners(cid, remove=k)
                    except UserError as e:
                        log.info("couldn't sign an owner's removal in %s: %s", cid[:8], e)
            finally:
                self._completing_leaves.discard(cid)

        self.spawn(complete())

    async def rotate_server_key(self, cid: str) -> dict:
        conn, rec, owner, owner_set, chain = self._owner_context(cid)
        try:
            res = await conn.admin("prepare_key_rotation", cp.PrepareKeyRotation())
            seq = self._next_cert_seq(conn, rec)
            cert = issue_cert(owner, res.new_server_key, seq, owner_set, chain)
            await conn.admin("commit_key_rotation", cp.CommitKeyRotation(cert=cert))
        except (RpcError, Offline) as e:
            raise UserError(str(e)) from None
        rec["server_key"] = keys.encode(res.new_server_key)
        rec["cert_seq"] = seq
        conn.server_keys.append(res.new_server_key)
        self.save_vault()
        return {"ok": True, "server_key": rec["server_key"]}

    async def adopt_server(self, cid: str, move_code: str) -> dict:
        """Move to new hardware: certify the restored server's new key
        with this member's owner key, under the owner set this client knows
        (which may be newer than the backup's)."""
        v = self.require_unlocked()
        rec = v.data["communities"].get(cid)
        if not rec or not rec.get("owner_identity"):
            raise UserError(_("Only an owner can move a community."))
        try:
            code = parse_move(move_code)
        except CodeError as e:
            raise UserError(str(e)) from None
        if keys.encode(code.owner_key) != cid:
            raise UserError(_("That move code is for a different community."))
        owner = self.identity_key(rec["owner_identity"])
        chain = []
        for raw in rec.get("owner_chain", []):
            sc = sp.SignedOwnerChange()
            sc.ParseFromString(keys.decode(raw))
            chain.append(sc)
        try:
            owner_set = verify_chain(keys.decode(cid), chain)
        except keys.BadSignature:
            raise UserError(_("This device's copy of the owner list couldn't be verified.")) from None
        if not owner_set.has(owner.public):
            raise UserError(_("Your owner key isn't in this community's owner list any more."))
        seq = rec.get("cert_seq", 1) + 1
        cert = issue_cert(owner, code.server_key, seq, owner_set, chain)
        dev = rec["device"]
        s = await self._temp_session(
            list(code.relays),
            code.server_key,
            None,
            self.identity_key(rec["identity"]),
            StaticKey(keys.decode(dev["static"])),
        )
        try:
            await s.call(adopt_server=cp.AdoptServer(secret=code.secret, cert=cert))
        except (RpcError, Offline) as e:
            raise UserError(str(e)) from None
        finally:
            await s.close()
        rec["server_key"] = keys.encode(code.server_key)
        rec["relays"] = list(dict.fromkeys(list(code.relays) + rec.get("relays", [])))
        rec["cert_seq"] = seq
        self.save_vault()
        if cid in self.conns:
            self.conns[cid].reconnect_now()
        return {"ok": True}

    async def invite_for(self, cid: str, join_key_id: int) -> str:
        for k in await self.admin_query(cid, "join_keys"):
            if k["id"] == join_key_id and k.get("invite"):
                return k["invite"]
        raise UserError(_("That invite's secret is only shown when it's created. Create a new invite."))

    # ---------------------------------------------------------------- directory

    async def directory_search(self, query: str, tag: str = "") -> list[dict]:
        """Search the directory: on the maintainer nodes, or, with the
        directory_local setting on, in a downloaded copy (directory.py)."""
        try:
            if self.vault is not None and self.vault.settings.get("directory_local", False):
                return await self.directory.search(query, tag)
            return await self.meta.search(query, tag)
        except MetaVersionError as e:
            raise UserError(str(e)) from None
        except MetaError as e:
            raise UserError(_("Directory search failed: {error}").format(error=e)) from None

    def directory_status(self) -> dict:
        """What the search pane says about where results come from."""
        local = bool(self.vault is not None and self.vault.settings.get("directory_local", False))
        return {"local": local, **self.directory.status()}

    async def directory_refresh(self) -> dict:
        try:
            await self.directory.refresh(force=True)
        except MetaError as e:
            raise UserError(_("Downloading the directory failed: {error}").format(error=e)) from None
        return self.directory_status()

    # ---------------------------------------------------------------- settings

    def get_settings(self) -> dict:
        v = self.require_unlocked()
        return {
            "prefs": dict(self.prefs.values),
            "vault": dict(v.settings),
            "maintainers": self.maintainers_status(),
            "data_dir_default": str(
                self.base_paths.data.parent if self.base_paths.data.name == "data" else self.base_paths.data
            ),
            "data_dir_effective": str(self.paths.data),
            "cache_bytes": sum(self.cache(c).size() for c in v.data["communities"]),
            "backup": self.backup_status(),
            # Why "Push to talk in other windows" can't work here (Wayland,
            # no X11 display), or None.
            "ptt_global_unavailable": hotkey.unavailable_reason(),
        }

    def storage_locations(self) -> list[dict]:
        """Settings → Data & storage: every folder and file this app keeps,
        with its full path, so people can see what to back up (or wipe).
        Grouped by folder; `kind` is "folder", "file" or "keychain"."""
        p, base = self.paths, self.base_paths
        stored = self.prefs.unlock_key_in

        def loc(lid: str, group: str, kind: str, path: Path | str, label: str, desc: str) -> dict:
            exists = kind == "keychain" or Path(path).exists()
            return {
                "id": lid,
                "group": group,
                "kind": kind,
                "path": str(path),
                "label": label,
                "desc": desc,
                "exists": exists,
            }

        out = [
            loc(
                "config",
                "config",
                "folder",
                base.config,
                _("Settings folder"),
                _(
                    "Not encrypted, and nothing in it identifies you: it is read before you unlock. On a system "
                    "without a keychain it also holds the key that keeps you signed in."
                ),
            ),
            loc(
                "preferences",
                "config",
                "file",
                base.preferences,
                _("Preferences"),
                _(
                    "Appearance, window size, data folder, update checks, maintainer nodes, "
                    "whether this device keeps you signed in."
                ),
            ),
            loc(
                "unlock_key",
                "config",
                "file",
                keychain.key_file(base),
                _("Signed-in key"),
                _(
                    "Only on a system without a keychain, while OpenArcanum keeps you signed in: the key that opens "
                    "your profile, readable by your user account only. Anyone who can read your files can open "
                    "your profile with it."
                ),
            ),
            loc(
                "updates",
                "config",
                "file",
                base.config / "updates.json",
                _("Update checks"),
                _("When updates were last checked, and versions you chose to skip or postpone."),
            ),
            loc(
                "data",
                "data",
                "folder",
                p.data,
                _("Data folder"),
                _("Your encrypted profile. A copy of this folder stays locked with your passphrase."),
            ),
            loc(
                "vault",
                "data",
                "file",
                p.vault,
                _("Profile"),
                _("Keys, communities and settings, encrypted with your passphrase."),
            ),
            loc(
                "state",
                "data",
                "file",
                p.state,
                _("App state"),
                _("Drafts, the last open channel and read markers. Encrypted."),
            ),
            loc(
                "dms",
                "data",
                "file",
                p.data / "dms.bin",
                _("Direct messages"),
                _("Direct-message history: the only copy is on your devices. Encrypted."),
            ),
            loc(
                "olm",
                "data",
                "file",
                p.data / "olm.bin",
                _("Direct-message encryption"),
                _("Encryption state for direct messages. Encrypted."),
            ),
            loc(
                "guard",
                "data",
                "file",
                p.unlock_guard,
                _("Wrong-passphrase counter"),
                _("How long the unlock screen makes you wait after wrong passphrases."),
            ),
            loc(
                "sandbox",
                "data",
                "file",
                base.data / ENGINE_SANDBOX,
                _("Window safety check"),
                _("Whether the app's window can run in its protected mode on this system."),
            ),
            loc(
                "hosted",
                "data",
                "folder",
                p.data / "hosted",
                _("Communities hosted on this computer"),
                _(
                    "Each one's community server: its keys, its database (encrypted with the storage key kept "
                    "beside it) and its backups. Keep a copy somewhere else: if this folder is lost, so is the "
                    "community."
                ),
            ),
            loc(
                "cache",
                "cache",
                "folder",
                p.cache,
                _("Cache folder"),
                _("Safe to delete: it downloads again. Encrypted."),
            ),
            loc(
                "community_cache",
                "cache",
                "folder",
                p.cache / "communities",
                _("Community messages and files"),
                _("Kept only as long as each community keeps messages, up to the cache size above."),
            ),
            loc(
                "dm_files",
                "cache",
                "folder",
                p.cache / "dm-files",
                _("Direct-message files"),
                _("Files sent and received in direct messages, encrypted."),
            ),
            loc(
                "update_cache",
                "cache",
                "folder",
                p.cache / "updates",
                _("Downloaded updates"),
                _("An update waiting to be installed."),
            ),
            loc(
                "logs",
                "logs",
                "folder",
                p.log,
                _("Logs folder"),
                _("Not encrypted. Logs hold IDs and sizes only, never messages, keys or passphrases."),
            ),
            loc(
                "log",
                "logs",
                "file",
                p.log_file,
                _("App log"),
                _("Starts a new file at 1 MB; up to three older copies (client.log.1 to .3) are kept next to it."),
            ),
            loc(
                "hosted_logs",
                "logs",
                "folder",
                p.log / "hosted",
                _("Hosted community logs"),
                _("The logs of the community servers this computer hosts: IDs and sizes, never messages or keys."),
            ),
            loc(
                "crash",
                "logs",
                "file",
                p.log / crashlog.CRASH_FILE,
                _("Crash log"),
                _("Details of what the app was doing if it crashes. Never sent anywhere."),
            ),
            loc(
                "keychain",
                "keychain",
                "keychain",
                f"{keychain.SERVICE} / {keychain.account_name(p)}",
                _("Keychain entry"),
                _("OpenArcanum keeps you signed in: your profile's key is in this computer's keychain under this name.")
                if stored == keychain.KEYCHAIN
                else _(
                    "Used while OpenArcanum keeps you signed in on a system with a keychain "
                    "(Settings → Security). Nothing is stored there now."
                ),
            ),
        ]
        toolkit = base.config.parent / ".toolkit"
        if base.config.name == "config" and toolkit.is_dir():
            out.append(
                loc(
                    "toolkit",
                    "toolkit",
                    "folder",
                    toolkit,
                    _("Toolkit files"),
                    _("Started with --home: the window toolkit's caches (fonts, graphics) stay here too."),
                )
            )
        return out

    def folder_of(self, location: str) -> str:
        """The folder to show for one of storage_locations' ids: the folder
        itself, or the folder a file is in."""
        for loc in self.storage_locations():
            if loc["id"] == location and loc["kind"] != "keychain":
                path = Path(loc["path"])
                folder = path if loc["kind"] == "folder" else path.parent
                if not folder.is_dir():
                    raise UserError(_("That folder doesn't exist yet."))
                return str(folder)
        raise UserError(_("Unknown location."))

    async def set_setting(self, key: str, value: Any) -> dict:
        s = settings_mod.BY_KEY.get(key)
        if s is None:
            raise UserError(_("Unknown setting {key}.").format(key=key))
        if key == "remember_device":
            raise UserError(_("Turn this on or off under Settings → Security."))
        try:
            clean = settings_mod.validate(key, value)
        except SettingError as e:
            raise UserError(str(e)) from None
        if s.store == "prefs":
            if key == "data_dir":
                await self._move_data(clean)
            self.prefs.set(key, clean)
            if key == "update_check":
                self.updates.wake()
            elif key in ("maintainers", "maintainer_cluster_nodes"):
                self._maintainers_changed()
            self.emit("appearance", {k: self.prefs.values[k] for k in ("theme", "text_size", "compact")})
            return {"ok": True, "value": clean}
        v = self.require_unlocked()
        v.settings[key] = clean
        self.save_vault()
        # Live effects.
        if key == "directory_local" and not clean:
            self.directory.forget()
        elif key == "cache_limit_mb":
            await self._enforce_cache()
        elif key == "device_name":
            await self.devices.renamed()
        elif key == "display_name":
            self.emit("profile", self.profile_status())
        elif key in (
            "voice_input",
            "voice_output",
            "video_input",
            "voice_mode",
            "ptt_key",
            "ptt_global",
            "voice_sensitivity",
            "voice_volumes",
            "voice_output_volume",
            "voice_local_mutes",
            "screen_share_audio",
            "camera_quality",
            "screen_quality",
        ):
            self.voice.settings_changed()
        if key in ("muted_communities", "muted_channels", "community_notify"):
            for cid in v.data["communities"]:
                self.community_changed(cid)
        await self.devices.setting_changed(key, clean)
        if key in settings_mod.STATUS_AUDIENCE_KEYS:
            # Who sees the status changed: post again (un-shared audiences
            # clear theirs; leavers trigger a new key).
            self.spawn(self.statuses.audience_changed())
        return {"ok": True, "value": clean}

    def apply_synced_setting(self, key: str, value: Any) -> None:
        if (
            self.vault is None
            or key not in settings_mod.BY_KEY
            or settings_mod.BY_KEY[key].store != "vault"
            or key in settings_mod.PER_DEVICE
        ):
            return
        try:
            self.vault.settings[key] = settings_mod.validate(key, value)
        except SettingError:
            return
        self.save_vault()
        if key in settings_mod.STATUS_AUDIENCE_KEYS:
            self.spawn(self.statuses.audience_changed())

    async def set_community_notify(self, cid: str, level: str) -> dict:
        """One community's notification level: all, mentions, nothing, or ""
        to follow the default (notify_mode)."""
        v = self.require_unlocked()
        if cid not in v.data["communities"]:
            raise UserError(_("You aren't in that community."))
        levels = dict(v.settings.get("community_notify", {}))
        if level:
            levels[cid] = level
        else:
            levels.pop(cid, None)
        return await self.set_setting("community_notify", levels)

    async def set_status_community(self, cid: str, level: str) -> dict:
        """Who in one community sees my status: off, state or full (statuses.py)."""
        v = self.require_unlocked()
        if cid not in v.data["communities"]:
            raise UserError(_("You aren't in that community."))
        levels = dict(v.settings.get("status_communities", {}))
        levels[cid] = level
        return await self.set_setting("status_communities", levels)

    async def remove_contact(self, ident: str) -> dict:
        """Remove a contact; they stop getting my status (a new status key
        for the rest, "cleared" for them)."""
        res = await self.dms.remove_contact(ident)
        self.statuses.contact_removed(ident)
        self.spawn(self.statuses.audience_changed())
        return res

    def mark_community_read(self, cid: str) -> dict:
        """Everything this device has from a community is read (the rail's
        Mark all as read)."""
        self.require_unlocked()
        st = self.conn(cid).state
        for ch in st.channels if st is not None else []:
            msgs = self.cache(cid).channel(ch.id)
            if msgs and msgs[-1]["id"] > self._last_read(cid, ch.id):
                self._set_read(cid, ch.id, msgs[-1]["id"])
                self._emit_unread(cid, ch.id)
        self.community_changed(cid)
        return {"ok": True}

    async def toggle_mute(self, cid: str, channel_id: int | None = None) -> dict:
        v = self.require_unlocked()
        if channel_id is None:
            lst = list(v.settings.get("muted_communities", []))
            item = cid
            key = "muted_communities"
        else:
            lst = list(v.settings.get("muted_channels", []))
            item = f"{cid}:{channel_id}"
            key = "muted_channels"
        if item in lst:
            lst.remove(item)
        else:
            lst.append(item)
        return await self.set_setting(key, lst)

    async def _move_data(self, folder: str) -> None:
        """Move data and cache to a new folder, live."""
        new = self.base_paths.with_data_location(Path(folder) if folder else None)
        if new.data == self.paths.data:
            return
        if new.data.is_dir() and any(new.data.iterdir()):
            raise UserError(_("That folder already holds OpenArcanum data. Choose an empty folder."))
        for c in self.caches.values():
            c.save()
        # Hosted servers keep their data in the data folder: stop them for
        # the move and start them again from the new place.
        hosted = [hid for hid, h in self.hosting.items.items() if h.enabled]
        await self.hosting.stop_all(grace=HOSTING_STOP_SECONDS)
        await asyncio.to_thread(move_tree, self.paths.data, new.data)
        await asyncio.to_thread(move_tree, self.paths.cache, new.cache)
        old, self.paths = self.paths, new
        if self.prefs.unlock_key_in == keychain.KEYCHAIN and self.vault is not None:
            # The keychain entry is named after the profile file's path:
            # keep the key under the new name, so the next start still opens.
            try:
                await asyncio.to_thread(keychain.store_keychain, new, self.vault.file_key())
            except keychain.KeychainError:
                log.info("stay signed in: the key couldn't be kept under the moved profile's name")
                self.prefs.set_unlock_key_in("")
            await asyncio.to_thread(keychain.delete_keychain, old)
        self.hosting.reload()
        for hid in hosted:
            if hid in self.hosting.items:
                await self.hosting.start(hid)
        self.paths.ensure()
        self.guard = UnlockGuard(self.paths.unlock_guard)
        if self.vault is not None:
            self.vault.path = self.paths.vault
            if self.state is not None:
                self.state.path = self.paths.state
        for cid, c in list(self.caches.items()):
            c.root = self.paths.community_cache(hashlib.sha256(cid.encode()).hexdigest()[:32])

    async def clear_community_cache(self, cid: str) -> dict:
        """Delete this device's copy of one community's messages and files."""
        self.require_unlocked()
        self.cache(cid).wipe()
        self.caches.pop(cid, None)
        return {"ok": True}

    async def clear_cache(self) -> dict:
        v = self.require_unlocked()
        for cid in list(v.data["communities"]):
            self.cache(cid).wipe()
        self.caches.clear()
        return {"ok": True}

    async def _enforce_cache(self) -> None:
        if self.vault is None:
            return
        limit = int(self.settings.get("cache_limit_mb", 1024)) * 1024 * 1024
        caches = [self.cache(c) for c in self.vault.data["communities"]]
        await asyncio.to_thread(enforce_limit, caches, limit)

    async def _cache_janitor(self) -> None:
        """Apply each community's retention to the local cache."""
        while True:
            await asyncio.sleep(1)
            try:
                for cid, conn in list(self.conns.items()):
                    st = conn.state
                    if st is None:
                        continue
                    ret, fret = {}, {}
                    for ch in st.channels:
                        ret[ch.id] = (
                            ch.message_retention_days
                            if ch.HasField("message_retention_days")
                            else st.settings.message_retention_days
                        )
                        fret[ch.id] = (
                            ch.file_retention_days
                            if ch.HasField("file_retention_days")
                            else st.settings.file_retention_days
                        )
                    if self.cache(cid).prune(ret, fret):
                        self._schedule_cache_save()
                await self._enforce_cache()
            except Exception:
                log.exception("cache janitor")
            await asyncio.sleep(600)

    # ---------------------------------------------------------------- UI state (state.bin)

    def ui_state_get(self, key: str) -> Any:
        self.require_unlocked()
        return self.state.get(key) if self.state else None

    def ui_state_set(self, key: str, value: Any) -> None:
        self.require_unlocked()
        if not isinstance(key, str) or len(key) > 200:
            raise UserError(_("unknown saved state"))
        if self.state is None:
            return
        if value is not None:
            # state.bin is small (drafts, the last open channel): the page
            # can't grow it without bound, in keys or in one value.
            try:
                size = len(json.dumps(value))
            except TypeError, ValueError:
                raise UserError(_("unknown saved state")) from None
            if size > MAX_UI_STATE_BYTES or (key not in self.state.data and len(self.state.data) >= MAX_UI_STATE_KEYS):
                raise UserError(_("unknown saved state"))
        self.state.set(key, value)

    # ---------------------------------------------------------------- voice passthrough

    def gui_media_access(self, allowed: bool) -> None:
        self.media_access(allowed)

    async def voice_update(self, cid: str, session: cp.VoiceSession) -> None:
        await self.voice.server_update(cid, session)

    # ---------------------------------------------------------------- updates (docs/release.md)

    def start_updates(self) -> None:
        """Startup check, then one every 24 h while automatic checks are on."""
        self.spawn(self.updates.run())

    async def update_check(self) -> dict:
        """ "Check now": runs in the background; progress arrives as `update` events."""

        async def run() -> None:
            with contextlib.suppress(UserError):
                await self.updates.check(manual=True)

        self.spawn(run())
        await asyncio.sleep(0)
        return self.updates.status()

    async def update_install(self, version: str, token: str) -> str:
        """ "Restart and install": only while the profile is unlocked (the
        prompt only shows over the unlocked app; this holds for any caller).
        The page can still reach this call, so consent rests on the prompt
        in the page (docs/threat-model.md, "Updates")."""
        self.require_unlocked()
        return (await self.updates.install(version, token)).description

    # ---------------------------------------------------------------- shutdown

    def _save_caches(self) -> None:
        for c in list(self.caches.values()):
            try:
                c.save()
            except Exception:
                log.exception("shutdown: saving a community cache")

    async def shutdown(self) -> None:
        """Save everything and stop. Each step has its own time limit and
        its failures are logged, so one stuck step (a call leaving through a
        frozen relay) can't use up the app's whole shutdown budget
        (app.py: 5 s) or skip the steps after it. Caches are saved first and
        again last, for whatever arrived while connections stopped."""

        async def step(what: str, aw) -> None:
            try:
                await asyncio.wait_for(aw, SHUTDOWN_STEP_SECONDS)
            except TimeoutError:
                log.warning("shutdown: %s took longer than %.1f s; went on without it", what, SHUTDOWN_STEP_SECONDS)
            except Exception:
                log.exception("shutdown: %s failed", what)

        self._save_caches()
        # Hosted servers stop alongside the other steps (closing their
        # standard input starts their clean shutdown at once).
        hosting = asyncio.ensure_future(self.hosting.stop_all(grace=HOSTING_STOP_SECONDS))
        started = time.monotonic()
        await step("leaving the call", self.voice.shutdown())
        await step("device linking", self.devices.shutdown())
        await step("statuses", self.statuses.shutdown())
        await step("direct messages", self.dms.shutdown())
        conns = list(self.conns.values())
        await step("community connections", asyncio.gather(*(c.stop() for c in conns), return_exceptions=True))
        try:
            await asyncio.wait_for(hosting, max(0.2, HOSTING_STOP_SECONDS + 0.5 - (time.monotonic() - started)))
        except TimeoutError:
            log.warning("shutdown: hosted community servers were still stopping")
        except Exception:
            log.exception("shutdown: stopping hosted community servers failed")
        self._save_caches()
        for t in list(self._tasks):
            t.cancel()


def _member_dict(m: cp.Member) -> dict:
    return {
        "key": keys.encode(m.key),
        "display_name": m.display_name,
        "role_ids": list(m.role_ids),
        "joined_at": m.joined_at,
        "owner": m.owner,
        "owner_invited": m.owner_invited,
        "has_avatar": m.has_avatar,
        "avatar_version": m.avatar_version,
        "join_key_id": m.join_key_id,
        "status": _enum_name(cp.MemberStatus, m.status, "MEMBER_STATUS_"),
        "nickname": m.nickname,
        "about": m.about,
        "pronouns": m.pronouns,
        "online": m.online,
        "timeout_until": m.timeout_until,
    }


def _join_key_dict(k: cp.JoinKey) -> dict:
    return {
        "id": k.id,
        "label": k.label,
        "created_at": k.created_at,
        "expires_at": k.expires_at,
        "max_uses": k.max_uses,
        "uses": k.uses,
        "role_ids": list(k.role_ids),
        "request_only": k.request_only,
        "revoked": k.revoked,
        "public": k.public,
        "created_by": keys.encode(k.created_by),
        "created_by_name": k.created_by_name or keys.fingerprint(k.created_by),
    }


def _override_dict(o: cp.ChannelOverride) -> dict:
    """An override with its permissions by name (the UI can't do 64-bit math)."""
    return {
        "role_id": o.role_id,
        "member": keys.encode(o.member) if o.member else "",
        "allow": names(o.allow),
        "deny": names(o.deny),
    }


def _mod_queue(st: cp.SyncState | None, perms: int) -> dict:
    """Open reports and join requests from the server's counts, each only
    with the permission for its queue."""
    q = st.mod_queue if st is not None and st.HasField("mod_queue") else None
    return {
        "reports": q.open_reports if q is not None and has(perms, PERMISSIONS["manage_messages"]) else 0,
        "pending": q.pending_members if q is not None and has(perms, PERMISSIONS["approve_members"]) else 0,
    }


def _toggle_name(v: int) -> str:
    return "off" if v == cp.CHANNEL_TOGGLE_OFF else "inherit"


def is_mention(text: str, display_name: str) -> bool:
    t = text.lower()
    return "@everyone" in t or (bool(display_name) and f"@{display_name.lower()}" in t)


def community_notify_level(settings: dict, cid: str) -> str:
    """A community's notification level: its own (the rail menu), else the
    default (notify_mode)."""
    return settings.get("community_notify", {}).get(cid) or settings.get("notify_mode", "all")


def image_mime(data: bytes) -> str:
    """The type of a PNG, JPEG, GIF or WebP image from its first bytes, or
    "" for anything else."""
    if data[:8] == b"\x89PNG\r\n\x1a\n":
        return "image/png"
    if data[:3] == b"\xff\xd8\xff":
        return "image/jpeg"
    if data[:4] == b"GIF8":
        return "image/gif"
    if data[:4] == b"RIFF" and data[8:12] == b"WEBP":
        return "image/webp"
    return ""


def community_notifies(settings: dict, cid: str) -> bool:
    """Whether anything from this community may notify at all: desktop
    notifications on, the community not muted and not set to Nothing."""
    return (
        bool(settings.get("notifications", True))
        and cid not in settings.get("muted_communities", [])
        and community_notify_level(settings, cid) != "nothing"
    )


def should_notify(settings: dict, cid: str, m: dict, my_name: str) -> tuple[str | None, str]:
    """Notification policy: on/off, per-community and
    per-channel mute, all messages, mentions only or nothing (per community,
    else the default). Mentions break through channel mutes but not
    community mutes or Nothing."""
    if not community_notifies(settings, cid):
        return None, ""
    mention = m["mentions_me"] if "mentions_me" in m else is_mention(m.get("text", ""), my_name)
    if f"{cid}:{m['channel_id']}" in settings.get("muted_channels", []) and not mention:
        return None, ""
    if community_notify_level(settings, cid) == "mentions" and not mention:
        return None, ""
    files = m.get("files") or []
    body = m.get("text") or (_("Sent a file: {name}").format(name=files[0]["name"]) if files else "")
    return m.get("author_name") or _("New message"), body[:200]
