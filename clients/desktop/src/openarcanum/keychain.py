"""Staying signed in: the vault's key kept on this device, so the profile
opens without the passphrase when OpenArcanum starts.

On by default for a profile created, restored or linked on this device;
"Ask for my passphrase when OpenArcanum starts" (Settings → Security) turns it
off. Older profiles keep asking until the person accepts a one-time offer.
What's stored is the key derived from the passphrase (the vault is encrypted
under it), never the passphrase.

Where it is kept:
- the operating system's keychain (Windows Credential Manager, the macOS
  Keychain, or the desktop's Secret Service: GNOME Keyring, KWallet), under
  the user's OS account;
- where there is none (minimal Linux desktops, containers), a key file in
  OpenArcanum's settings folder (`unlock-key`, mode 0600 in a 0700 folder):
  anyone who can read the user's files can then open the profile, and
  Settings → Security says so. It is in the settings folder rather than the
  data folder so a copy of the data folder (a backup, a synced or moved
  folder) stays locked with the passphrase.

Lock now, an idle lock, "Ask for my passphrase", Start over and a removed
device all delete it; the passphrase brings it back (unless asked not to).
"""

from __future__ import annotations

import base64
import hashlib
import json
import logging
import os
from pathlib import Path

import keyring
import keyring.errors
from keyring.backends import fail

from openarcanum import PRODUCT_NAME
from openarcanum.crypto import KEY_LEN
from openarcanum.i18n import _
from openarcanum.paths import POSIX, Paths, atomic_write

log = logging.getLogger(__name__)

SERVICE = f"{PRODUCT_NAME} profile key"
KEY_FILE = "unlock-key"
FILE_VERSION = 1
KEYCHAIN = "keychain"
FILE = "file"


class KeychainError(Exception):
    pass


def account_name(paths: Paths) -> str:
    """The keychain entry's account name. One entry per profile (--home
    profiles are separate); Settings → Data & storage shows it."""
    return hashlib.sha256(str(paths.vault.resolve()).encode()).hexdigest()[:32]


def key_file(paths: Paths) -> Path:
    """The fallback key file, used only where there is no keychain."""
    return paths.config / KEY_FILE


def available() -> tuple[bool, str]:
    """Whether this system has a keychain to use, and if not, why."""
    try:
        backend = keyring.get_keyring()
    except Exception as e:  # keyring raises plain exceptions from backends
        return False, _("the system keychain can't be reached ({error})").format(error=type(e).__name__)
    if isinstance(backend, fail.Keyring):
        return False, _("this system has no keychain OpenArcanum can use (on Linux: GNOME Keyring or KWallet)")
    return True, ""


def where_to_store() -> str:
    """KEYCHAIN where this system has one, else FILE."""
    return KEYCHAIN if available()[0] else FILE


def store(paths: Paths, key: bytes, where: str | None = None) -> str:
    """Keep the vault's key in `where` (default: the keychain if there is
    one, else the key file). Returns where it went."""
    where = where or where_to_store()
    if where == KEYCHAIN:
        store_keychain(paths, key)
    else:
        _write_file(paths, key)
    return where


def load(paths: Paths, where: str) -> bytes | None:
    """The stored key, or None if there is none."""
    return load_keychain(paths) if where == KEYCHAIN else _read_file(paths)


def delete(paths: Paths, where: str) -> None:
    """Remove the stored key: the key file always (wherever the key is now,
    none may be left behind), the keychain entry when `where` says so."""
    _delete_file(paths)
    if where == KEYCHAIN:
        delete_keychain(paths)


# ---------------------------------------------------------------- the OS keychain


def store_keychain(paths: Paths, key: bytes) -> None:
    try:
        keyring.set_password(SERVICE, account_name(paths), base64.b64encode(key).decode())
    except keyring.errors.KeyringError as e:
        raise KeychainError(
            _("Couldn't save to the system keychain: {error}.").format(error=type(e).__name__)
        ) from None


def load_keychain(paths: Paths) -> bytes | None:
    try:
        v = keyring.get_password(SERVICE, account_name(paths))
    except keyring.errors.KeyringError as e:
        log.info("keychain: couldn't read (%s)", type(e).__name__)
        raise KeychainError(_("Couldn't read the system keychain: {error}.").format(error=type(e).__name__)) from None
    if not v:
        return None
    try:
        return base64.b64decode(v, validate=True)
    except ValueError:
        return None


def delete_keychain(paths: Paths) -> None:
    try:
        keyring.delete_password(SERVICE, account_name(paths))
    except keyring.errors.PasswordDeleteError:
        pass
    except keyring.errors.KeyringError as e:
        log.info("keychain: couldn't delete (%s)", type(e).__name__)


# ---------------------------------------------------------------- the key file (no keychain)


def _write_file(paths: Paths, key: bytes) -> None:
    # atomic_write: 0600, and the folder 0700 (paths.ensure_dir).
    body = json.dumps({"v": FILE_VERSION, "key": base64.b64encode(key).decode()}).encode()
    try:
        atomic_write(key_file(paths), body, mode=0o600)
    except OSError as e:
        raise KeychainError(
            _("Couldn't save the key file: {error}.").format(error=e.strerror or type(e).__name__)
        ) from None


def _read_file(paths: Paths) -> bytes | None:
    path = key_file(paths)
    try:
        if POSIX:
            st = os.stat(path)
            if st.st_uid != os.getuid() or st.st_mode & 0o077:
                # Someone widened its permissions (or it isn't ours): it may
                # have been read, so it isn't used, and it goes.
                _delete_file(paths)
                raise KeychainError(
                    _("The stored key could be read by other accounts on this computer, so OpenArcanum deleted it.")
                )
        raw = path.read_bytes()
    except FileNotFoundError:
        return None
    except OSError as e:
        log.info("key file: couldn't read (%s)", type(e).__name__)
        raise KeychainError(
            _("Couldn't read the key file: {error}.").format(error=e.strerror or type(e).__name__)
        ) from None
    try:
        d = json.loads(raw)
        if not isinstance(d, dict) or d.get("v") != FILE_VERSION:
            # Another version's layout: counts as nothing stored (the
            # passphrase opens the profile and stores it again).
            return None
        key = base64.b64decode(d["key"], validate=True)
    except ValueError, KeyError, TypeError:
        return None
    return key if len(key) == KEY_LEN else None


def _delete_file(paths: Paths) -> None:
    try:
        key_file(paths).unlink()
    except FileNotFoundError:
        pass
    except OSError as e:
        log.warning("key file: couldn't delete (%s)", type(e).__name__)
