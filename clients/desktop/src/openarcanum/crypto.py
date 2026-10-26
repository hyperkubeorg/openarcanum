"""Symmetric crypto helpers: AEAD, scrypt, HKDF. All from `cryptography`."""

from __future__ import annotations

import hashlib
import os
import struct
from dataclasses import dataclass

from cryptography.exceptions import InvalidTag
from cryptography.hazmat.primitives import hashes
from cryptography.hazmat.primitives.ciphers.aead import ChaCha20Poly1305
from cryptography.hazmat.primitives.kdf.hkdf import HKDF

KEY_LEN = 32
NONCE_LEN = 12


class DecryptError(Exception):
    """Authentication failed: wrong key, or the data was altered."""


def random_key() -> bytes:
    return os.urandom(KEY_LEN)


def seal(key: bytes, plaintext: bytes, aad: bytes = b"") -> bytes:
    """nonce || ciphertext, with a random nonce."""
    nonce = os.urandom(NONCE_LEN)
    return nonce + ChaCha20Poly1305(key).encrypt(nonce, plaintext, aad)


def open_sealed(key: bytes, blob: bytes, aad: bytes = b"") -> bytes:
    if len(blob) < NONCE_LEN + 16:
        raise DecryptError("ciphertext too short")
    try:
        return ChaCha20Poly1305(key).decrypt(blob[:NONCE_LEN], blob[NONCE_LEN:], aad)
    except InvalidTag as e:
        raise DecryptError("authentication failed") from e


def encrypt_with_nonce(key: bytes, nonce: bytes, plaintext: bytes, aad: bytes = b"") -> bytes:
    return ChaCha20Poly1305(key).encrypt(nonce, plaintext, aad)


def decrypt_with_nonce(key: bytes, nonce: bytes, ciphertext: bytes, aad: bytes = b"") -> bytes:
    """Raises DecryptError for anything that doesn't authenticate, including a
    nonce of the wrong length (nonces often arrive with the ciphertext)."""
    if len(nonce) != NONCE_LEN:
        raise DecryptError("bad nonce length")
    try:
        return ChaCha20Poly1305(key).decrypt(nonce, ciphertext, aad)
    except InvalidTag as e:
        raise DecryptError("authentication failed") from e


def counter_nonce(counter: int) -> bytes:
    return b"\x00" * 4 + struct.pack(">Q", counter)


def hkdf(secret: bytes, info: bytes, length: int = KEY_LEN, salt: bytes | None = None) -> bytes:
    return HKDF(algorithm=hashes.SHA256(), length=length, salt=salt, info=info).derive(secret)


@dataclass(frozen=True)
class ScryptParams:
    n: int = 1 << 17
    r: int = 8
    p: int = 1

    def to_dict(self) -> dict:
        return {"n": self.n, "r": self.r, "p": self.p}

    @classmethod
    def from_dict(cls, d: dict) -> ScryptParams:
        n, r, p = int(d["n"]), int(d["r"]), int(d["p"])
        # Refuse absurd parameters from a tampered file rather than hanging.
        if not (1 << 10 <= n <= 1 << 22) or n & (n - 1) or not (1 <= r <= 32) or not (1 <= p <= 16):
            raise ValueError("unsupported scrypt parameters")
        return cls(n, r, p)


# Tests and fast paths can lower the cost; production files record their own.
DEFAULT_SCRYPT = ScryptParams()


def scrypt(passphrase: str, salt: bytes, params: ScryptParams) -> bytes:
    return hashlib.scrypt(
        passphrase.encode("utf-8"),
        salt=salt,
        n=params.n,
        r=params.r,
        p=params.p,
        maxmem=256 * 1024 * 1024,
        dklen=KEY_LEN,
    )
