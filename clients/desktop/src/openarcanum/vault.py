"""The encrypted local profile.

vault.bin holds every key, the joined communities and the user's settings,
encrypted with ChaCha20-Poly1305 under a key derived from the passphrase with
scrypt. It is only ever decrypted in memory. state.bin (UI state) and the
message cache are encrypted with random keys kept inside the vault.
"""

from __future__ import annotations

import base64
import json
import os
import secrets
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from openarcanum import crypto
from openarcanum.i18n import _
from openarcanum.paths import Paths, atomic_write
from openarcanum.settings import defaults

MAGIC = b"OARCVLT1"
STATE_MAGIC = b"OARCST01"
VERSION = 1


class VaultError(Exception):
    pass


class WrongPassphrase(VaultError):
    pass


class Backoff(VaultError):
    def __init__(self, seconds: float):
        super().__init__(_("Wait {seconds} s before trying again.").format(seconds=int(seconds + 0.999)))
        self.seconds = seconds


def b64(b: bytes) -> str:
    return base64.b64encode(b).decode()


def unb64(s: str) -> bytes:
    return base64.b64decode(s)


def _scrypt_params() -> crypto.ScryptParams:
    return crypto.DEFAULT_SCRYPT


def seal_with_passphrase(
    magic: bytes, passphrase: str, plaintext: bytes, params: crypto.ScryptParams | None = None
) -> bytes:
    params = params or _scrypt_params()
    salt = os.urandom(16)
    header = json.dumps({"v": VERSION, "kdf": "scrypt", **params.to_dict(), "salt": b64(salt)}).encode()
    key = crypto.scrypt(passphrase, salt, params)
    nonce = os.urandom(crypto.NONCE_LEN)
    ct = crypto.encrypt_with_nonce(key, nonce, plaintext, magic + header)
    return magic + len(header).to_bytes(4, "big") + header + nonce + ct


def _parse_sealed(magic: bytes, blob: bytes) -> tuple[dict, bytes, bytes, bytes]:
    if not blob.startswith(magic) or len(blob) < len(magic) + 4:
        raise VaultError(_("not an OpenArcanum file"))
    hlen = int.from_bytes(blob[len(magic) : len(magic) + 4], "big")
    if hlen > 4096:
        raise VaultError(_("damaged file header"))
    start = len(magic) + 4
    header = blob[start : start + hlen]
    try:
        h = json.loads(header)
    except json.JSONDecodeError as e:
        raise VaultError(_("damaged file header")) from e
    v = h.get("v")
    if isinstance(v, int) and not isinstance(v, bool) and v > VERSION:
        # A newer build wrote it; refuse rather than risk saving over fields
        # this version doesn't know.
        raise VaultError(_("This file was saved by a newer version of OpenArcanum. Update OpenArcanum to open it."))
    if v != VERSION or h.get("kdf") != "scrypt":
        raise VaultError(_("unsupported file version"))
    rest = blob[start + hlen :]
    return h, header, rest[: crypto.NONCE_LEN], rest[crypto.NONCE_LEN :]


def open_with_passphrase(magic: bytes, passphrase: str, blob: bytes) -> tuple[bytes, bytes, dict]:
    """Returns (plaintext, derived key, header)."""
    h, header, nonce, ct = _parse_sealed(magic, blob)
    try:
        params = crypto.ScryptParams.from_dict(h)
    except (KeyError, ValueError) as e:
        raise VaultError(_("damaged file header")) from e
    key = crypto.scrypt(passphrase, unb64(h["salt"]), params)
    try:
        return crypto.decrypt_with_nonce(key, nonce, ct, magic + header), key, h
    except crypto.DecryptError as e:
        raise WrongPassphrase(_("Wrong passphrase.")) from e


def new_vault_data(device_name: str) -> dict[str, Any]:
    settings = defaults("vault")
    settings["device_name"] = device_name
    return {
        "version": VERSION,
        "created": int(time.time()),
        "device_id": secrets.token_hex(8),
        "identities": {},  # id -> {seed, kind, created, label}
        "communities": {},  # community id (b64url owner key, or "pending-<x>") -> record
        "settings": settings,
        "state_key": b64(crypto.random_key()),
        "cache_key": b64(crypto.random_key()),
        "keys_revision": 0,
        "backup_revision": 0,
        "devices": {},  # device group (docs/protocol.md §10)
    }


@dataclass
class Vault:
    """An unlocked (or locked-but-running) vault."""

    path: Path
    data: dict[str, Any]
    _key: bytes
    _header: bytes
    _salt_header: dict
    locked: bool = False

    # ---------------------------------------------------------------- lifecycle

    @classmethod
    def exists(cls, paths: Paths) -> bool:
        return paths.vault.exists()

    @classmethod
    def create(cls, paths: Paths, passphrase: str, device_name: str, data: dict | None = None) -> Vault:
        if paths.vault.exists():
            raise VaultError(_("A profile already exists here."))
        data = data or new_vault_data(device_name)
        blob = seal_with_passphrase(MAGIC, passphrase, json.dumps(data).encode())
        atomic_write(paths.vault, blob)
        v = cls.unlock(paths, passphrase, guard=None)
        return v

    @classmethod
    def unlock(cls, paths: Paths, passphrase: str, guard: UnlockGuard | None) -> Vault:
        if guard is not None:
            guard.check()
        try:
            blob = paths.vault.read_bytes()
        except FileNotFoundError as e:
            raise VaultError(_("No profile on this device.")) from e
        try:
            pt, key, h = open_with_passphrase(MAGIC, passphrase, blob)
        except WrongPassphrase:
            if guard is not None:
                guard.failed()
            raise
        if guard is not None:
            guard.succeeded()
        _magic, header, _nonce, _ct = _parse_sealed(MAGIC, blob)
        data = json.loads(pt)
        # Settings added in later versions get their defaults.
        for k, v in defaults("vault").items():
            data["settings"].setdefault(k, v)
        for k in ("devices", "contacts", "dms"):
            data.setdefault(k, {})
        return cls(path=paths.vault, data=data, _key=key, _header=header, _salt_header=h)

    @classmethod
    def unlock_with_key(cls, paths: Paths, key: bytes) -> Vault:
        """Open with the key itself (kept on this device for staying signed
        in: keychain.py); WrongPassphrase if it no longer fits (the passphrase
        was changed elsewhere, or the file was replaced)."""
        try:
            blob = paths.vault.read_bytes()
        except FileNotFoundError as e:
            raise VaultError(_("No profile on this device.")) from e
        h, header, nonce, ct = _parse_sealed(MAGIC, blob)
        try:
            pt = crypto.decrypt_with_nonce(key, nonce, ct, MAGIC + header)
        except crypto.DecryptError as e:
            raise WrongPassphrase(_("The key kept on this device no longer opens this profile.")) from e
        data = json.loads(pt)
        for k, v in defaults("vault").items():
            data["settings"].setdefault(k, v)
        for k in ("devices", "contacts", "dms"):
            data.setdefault(k, {})
        return cls(path=paths.vault, data=data, _key=key, _header=header, _salt_header=h)

    def file_key(self) -> bytes:
        """The key the vault file is encrypted under (kept for staying signed in)."""
        return self._key

    def _decrypt_file(self) -> dict:
        _magic, header, nonce, ct = _parse_sealed(MAGIC, self.path.read_bytes())
        return json.loads(crypto.decrypt_with_nonce(self._key, nonce, ct, MAGIC + header))

    def save(self) -> None:
        data = self.data
        if self.locked:
            # Seeds aren't in memory while locked: carry them over from disk
            # without keeping them.
            disk = self._decrypt_file()
            data = json.loads(json.dumps(self.data))
            for ident_id, ident in disk.get("identities", {}).items():
                if ident_id in data["identities"] and "seed" in ident:
                    data["identities"][ident_id]["seed"] = ident["seed"]
        nonce = os.urandom(crypto.NONCE_LEN)
        ct = crypto.encrypt_with_nonce(self._key, nonce, json.dumps(data).encode(), MAGIC + self._header)
        atomic_write(self.path, MAGIC + len(self._header).to_bytes(4, "big") + self._header + nonce + ct)

    def new_key_for(self, old: str, new: str) -> tuple[bytes, bytes, dict]:
        """The slow half of a passphrase change, safe in a worker thread:
        checks the old passphrase, derives the new key. Nothing is written;
        use_key then switches over and saves (on the thread that saves the
        vault, so no save in between can write under the old key)."""
        open_with_passphrase(MAGIC, old, self.path.read_bytes())  # raises WrongPassphrase
        params = _scrypt_params()
        salt = os.urandom(16)
        h = {"v": VERSION, "kdf": "scrypt", **params.to_dict(), "salt": b64(salt)}
        return crypto.scrypt(new, salt, params), json.dumps(h).encode(), h

    def use_key(self, key: bytes, header: bytes, h: dict) -> None:
        self._key, self._header, self._salt_header = key, header, h
        self.save()

    def change_passphrase(self, old: str, new: str) -> None:
        """Both halves at once (when nothing else saves concurrently)."""
        self.use_key(*self.new_key_for(old, new))

    def lock(self) -> None:
        """Clear identity keys from memory. Device keys and the
        file key stay, so open sessions keep running and state that arrives
        while locked can still be saved."""
        for ident in self.data["identities"].values():
            ident.pop("seed", None)
        self.locked = True

    def wipe(self) -> None:
        """Delete the vault file (a removed device wipes itself)."""
        try:
            self.path.unlink()
        except FileNotFoundError:
            pass

    # ---------------------------------------------------------------- helpers

    @property
    def settings(self) -> dict[str, Any]:
        return self.data["settings"]

    @property
    def state_key(self) -> bytes:
        return unb64(self.data["state_key"])

    @property
    def cache_key(self) -> bytes:
        return unb64(self.data["cache_key"])

    def keys_changed(self) -> None:
        """Mark the backup as out of date."""
        self.data["keys_revision"] = self.data.get("keys_revision", 0) + 1

    @property
    def backup_current(self) -> bool:
        return self.data.get("backup_revision", 0) >= self.data.get("keys_revision", 0)


class UnlockGuard:
    """Each wrong passphrase makes the next attempt wait longer. Kept
    on disk so restarting the app doesn't reset it.

    unlock-guard.json records its format version in `v` (a file without it
    is version 1). The two fields mean the same in every version, so they're
    read whatever the version says."""

    VERSION = 1

    def __init__(self, path: Path, now=time.time):
        self.path = path
        self.now = now

    def _load(self) -> dict:
        try:
            st = json.loads(self.path.read_text())
        except FileNotFoundError, json.JSONDecodeError, OSError:
            st = None
        if not isinstance(st, dict):
            return {"failures": 0, "next": 0}
        return st

    def _write(self, failures: int, next_at: float) -> None:
        atomic_write(self.path, json.dumps({"v": self.VERSION, "failures": failures, "next": next_at}).encode())

    def remaining(self) -> float:
        return max(0.0, float(self._load().get("next", 0)) - self.now())

    def check(self) -> None:
        r = self.remaining()
        if r > 0:
            raise Backoff(r)

    @staticmethod
    def delay(failures: int) -> float:
        return 0.0 if failures <= 0 else float(min(2 ** (failures - 1), 60))

    def failed(self) -> None:
        failures = int(self._load().get("failures", 0)) + 1
        self._write(failures, self.now() + self.delay(failures))

    def succeeded(self) -> None:
        if self.path.exists():
            self._write(0, 0)


class StateStore:
    """state.bin: drafts, last-open channel and other UI state."""

    def __init__(self, path: Path, key: bytes):
        self.path = path
        self.key = key
        self.data: dict[str, Any] = {}
        try:
            blob = path.read_bytes()
            if blob.startswith(STATE_MAGIC):
                self.data = json.loads(crypto.open_sealed(key, blob[len(STATE_MAGIC) :], STATE_MAGIC))
        except FileNotFoundError, crypto.DecryptError, json.JSONDecodeError:
            self.data = {}

    def get(self, key: str, default: Any = None) -> Any:
        return self.data.get(key, default)

    def set(self, key: str, value: Any) -> None:
        if value is None:
            self.data.pop(key, None)
        else:
            self.data[key] = value
        self.save()

    def save(self) -> None:
        atomic_write(self.path, STATE_MAGIC + crypto.seal(self.key, json.dumps(self.data).encode(), STATE_MAGIC))
