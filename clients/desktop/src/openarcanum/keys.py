"""Keypairs, signatures and key encodings.

Ed25519 identity keys sign everything; X25519 keys are Noise statics (device
keys) and DM keys. Signatures cover context || 0x00 || message, mirroring
internal/identity in Go.
"""

from __future__ import annotations

import base64
import hashlib
from dataclasses import dataclass

from cryptography.exceptions import InvalidSignature
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey, Ed25519PublicKey
from cryptography.hazmat.primitives.asymmetric.x25519 import X25519PrivateKey, X25519PublicKey

from openarcanum.i18n import _

# Endpoint registration over a RelayBinding (relay key, URL, connection
# nonce), and the relay's proof of its key in Hello (docs/protocol.md §2
# "Relay identity"). The endpoint context is v2 because v1 signed the nonce
# alone, which didn't bind the relay or its URL.
CTX_RELAY_ENDPOINT = "openarcanum/relay-endpoint/v2"
CTX_RELAY_HELLO = "openarcanum/relay-hello/v1"
CTX_NOISE_STATIC = "openarcanum/noise-static/v1"
CTX_SERVER_CERT = "openarcanum/server-cert/v1"
CTX_CHANNEL_ENVELOPE = "openarcanum/channel-envelope/v1"
CTX_ADMIN_ACTION = "openarcanum/admin-action/v1"
CTX_DEVICE_REVOCATION = "openarcanum/device-revocation/v1"
CTX_ROUTE = "openarcanum/route/v1"
CTX_LISTING = "openarcanum/listing/v1"
CTX_RELAY_HEARTBEAT = "openarcanum/relay-heartbeat/v1"
CTX_COMMUNITY_HEARTBEAT = "openarcanum/community-heartbeat/v1"
CTX_MAINTAINER_SET = "openarcanum/maintainer-set/v1"
CTX_DM_CARD = "openarcanum/dm-card/v2"
CTX_DM_DEVICE_LIST = "openarcanum/dm-device-list/v2"
CTX_DM_DEVICE = "openarcanum/dm-device/v2"
CTX_DM_GROUP_STATE = "openarcanum/dm-group-state/v2"
CTX_OWNER_CHANGE = "openarcanum/owner-change/v1"
CTX_OWNER_KEY = "openarcanum/owner-key/v1"
CTX_RELEASE_MANIFEST = "openarcanum/release-manifest/v1"
# A device able to receive statuses in a community, vouched for by the member
# key (statuses.py, docs/protocol.md §9.8).
CTX_STATUS_DEVICE = "openarcanum/status-device/v1"
# A relay grant for a call (callauth.CtxCallGrant): signed by a community
# server, or for a DM call by the call's own key (dmcalls.py).
CTX_CALL_GRANT = "openarcanum/call-grant/v1"

_RAW = serialization.Encoding.Raw
_RAW_PUB = serialization.PublicFormat.Raw
_RAW_PRIV = serialization.PrivateFormat.Raw
_NOENC = serialization.NoEncryption()


class BadSignature(Exception):
    pass


def _signed_bytes(ctx: str, msg: bytes) -> bytes:
    return ctx.encode() + b"\x00" + msg


@dataclass(frozen=True)
class SigningKey:
    """An Ed25519 keypair."""

    seed: bytes

    @classmethod
    def generate(cls) -> SigningKey:
        return cls(Ed25519PrivateKey.generate().private_bytes(_RAW, _RAW_PRIV, _NOENC))

    @property
    def _priv(self) -> Ed25519PrivateKey:
        return Ed25519PrivateKey.from_private_bytes(self.seed)

    @property
    def public(self) -> bytes:
        return self._priv.public_key().public_bytes(_RAW, _RAW_PUB)

    def sign(self, ctx: str, msg: bytes) -> bytes:
        return self._priv.sign(_signed_bytes(ctx, msg))

    def __repr__(self) -> str:  # never print key material
        return f"SigningKey({fingerprint(self.public)})"


def verify(public: bytes, ctx: str, msg: bytes, sig: bytes) -> None:
    if len(public) != 32 or len(sig) != 64:
        raise BadSignature("malformed key or signature")
    try:
        Ed25519PublicKey.from_public_bytes(public).verify(sig, _signed_bytes(ctx, msg))
    except InvalidSignature as e:
        raise BadSignature("bad signature") from e


def is_valid(public: bytes, ctx: str, msg: bytes, sig: bytes) -> bool:
    try:
        verify(public, ctx, msg, sig)
        return True
    except BadSignature:
        return False


@dataclass(frozen=True)
class StaticKey:
    """An X25519 keypair (Noise static / device key, DM keys)."""

    private: bytes

    @classmethod
    def generate(cls) -> StaticKey:
        return cls(X25519PrivateKey.generate().private_bytes(_RAW, _RAW_PRIV, _NOENC))

    @property
    def public(self) -> bytes:
        return X25519PrivateKey.from_private_bytes(self.private).public_key().public_bytes(_RAW, _RAW_PUB)

    def exchange(self, peer_public: bytes) -> bytes:
        return X25519PrivateKey.from_private_bytes(self.private).exchange(
            X25519PublicKey.from_public_bytes(peer_public)
        )

    def __repr__(self) -> str:
        return f"StaticKey({fingerprint(self.public)})"


def encode(b: bytes) -> str:
    """Unpadded base64url, the form used in pasted strings."""
    return base64.urlsafe_b64encode(b).rstrip(b"=").decode()


def decode(s: str) -> bytes:
    s = s.strip().rstrip("=")
    return base64.urlsafe_b64decode(s + "=" * (-len(s) % 4))


def fingerprint(public: bytes) -> str:
    """Short, stable label for a key; safe to log and show."""
    return hashlib.sha256(public).hexdigest()[:12]


def hexkey(b: bytes) -> str:
    return b.hex()


# ---------------------------------------------------------------- backups

BACKUP_MAGIC = b"OARCBAK1"
# The payload's own format version; the sealed header carries the vault
# header version as well.
BACKUP_VERSION = 1


def export_backup(vault_data: dict, passphrase: str) -> bytes:
    """An encrypted backup: every identity key (community, DM, contact and
    the device group's sync identity) and the list of joined communities
    (routes and pinned keys), but no message history and no device keys (a
    restored device makes its own)."""
    import json
    import time

    from openarcanum.vault import seal_with_passphrase

    communities = []
    for cid, c in vault_data["communities"].items():
        if c.get("status") in ("removed", "left"):
            continue
        communities.append(
            {
                k: c.get(k)
                for k in (
                    "id",
                    "name",
                    "server_key",
                    "owner_key",
                    "route_id",
                    "relays",
                    "identity",
                    "owner_identity",
                    "owner_pending",
                    "display_name",
                    "status",
                    "cert_seq",
                    "owner_seq",
                    "owner_head",
                    "owner_chain",
                )
            }
            | {"id": cid}
        )
    used = {c.get(k) for c in communities for k in ("identity", "owner_identity", "owner_pending")}
    identities = {
        i: {
            "seed": v["seed"],
            "kind": v.get("kind", "member"),
            "created": v.get("created", 0),
            "label": v.get("label", ""),
        }
        for i, v in vault_data["identities"].items()
        if "seed" in v and (i in used or v.get("kind") in ("dm", "contact", "sync"))
    }
    payload = {
        "format": "openarcanum-backup",
        "version": BACKUP_VERSION,
        "created": int(time.time()),
        "identities": identities,
        "communities": communities,
        "contacts": vault_data.get("contacts", {}),
    }
    return seal_with_passphrase(BACKUP_MAGIC, passphrase, json.dumps(payload).encode())


def import_backup(blob: bytes, passphrase: str) -> dict:
    import json

    from openarcanum.vault import VaultError, open_with_passphrase

    pt, _header, _key = open_with_passphrase(BACKUP_MAGIC, passphrase, blob)
    data = json.loads(pt)
    if not isinstance(data, dict) or data.get("format") != "openarcanum-backup":
        raise VaultError(_("Not an OpenArcanum backup."))
    if data.get("version") != BACKUP_VERSION:
        newer = isinstance(data.get("version"), int) and data["version"] > BACKUP_VERSION
        raise VaultError(
            _("This backup was made by a newer version of OpenArcanum. Update OpenArcanum to restore it.")
            if newer
            else _("This backup's format isn't supported.")
        )
    return data
