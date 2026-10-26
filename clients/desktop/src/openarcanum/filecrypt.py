"""File encryption for DM attachments (docs/protocol.md §9.7).

The age v1 payload format (age-encryption.org/v1, "Payload"): STREAM over
ChaCha20-Poly1305 in 64 KiB chunks, with the payload key derived from a
16-byte random file key by HKDF-SHA256. Only the payload part of age is used
(no age header or recipients): the file key travels inside the encrypted DM
event instead. Primitives come from `cryptography`; the construction is the
published age spec, checked against the C2SP CCTV age test vectors
(tests/vectors/age, tests/test_filecrypt.py).

Ciphertext layout: nonce (16 random bytes) || chunks. Every chunk is
ChaCha20-Poly1305(key, nonce = 11-byte big-endian counter || last-flag,
chunk). Non-final chunks hold exactly 64 KiB of plaintext; the final one
holds 0-64 KiB and may be empty only if it is the only chunk.
"""

from __future__ import annotations

import os
from collections.abc import Iterator
from typing import BinaryIO

from cryptography.exceptions import InvalidTag
from cryptography.hazmat.primitives import hashes
from cryptography.hazmat.primitives.ciphers.aead import ChaCha20Poly1305
from cryptography.hazmat.primitives.kdf.hkdf import HKDF

FILE_KEY_SIZE = 16
NONCE_SIZE = 16
CHUNK = 64 * 1024
TAG = 16
ENC_CHUNK = CHUNK + TAG


class FileDecryptError(Exception):
    """The file's ciphertext is damaged, truncated, extended or keyed wrong."""


def new_file_key() -> bytes:
    return os.urandom(FILE_KEY_SIZE)


def _payload_key(file_key: bytes, nonce: bytes) -> ChaCha20Poly1305:
    if len(file_key) != FILE_KEY_SIZE:
        raise ValueError("file key must be 16 bytes")
    key = HKDF(algorithm=hashes.SHA256(), length=32, salt=nonce, info=b"payload").derive(file_key)
    return ChaCha20Poly1305(key)


def _chunk_nonce(counter: int, last: bool) -> bytes:
    if counter >= 1 << 88:
        raise FileDecryptError("too many chunks")
    return counter.to_bytes(11, "big") + (b"\x01" if last else b"\x00")


def encrypt_stream(file_key: bytes, src: BinaryIO, dst: BinaryIO, nonce: bytes | None = None) -> int:
    """Encrypt everything readable from `src` into `dst`, one chunk in memory
    at a time. Returns the ciphertext size."""
    nonce = os.urandom(NONCE_SIZE) if nonce is None else nonce
    aead = _payload_key(file_key, nonce)
    dst.write(nonce)
    written = NONCE_SIZE
    counter = 0
    chunk = src.read(CHUNK)
    while True:
        nxt = src.read(CHUNK) if len(chunk) == CHUNK else b""
        last = not nxt
        ct = aead.encrypt(_chunk_nonce(counter, last), chunk, None)
        dst.write(ct)
        written += len(ct)
        if last:
            return written
        chunk = nxt
        counter += 1


def decrypt_chunks(file_key: bytes, src: BinaryIO) -> Iterator[bytes]:
    """Yield plaintext chunk by chunk. Raises FileDecryptError as soon as a
    chunk fails; chunks yielded before that were authentic."""
    nonce = src.read(NONCE_SIZE)
    if len(nonce) != NONCE_SIZE:
        raise FileDecryptError("missing nonce")
    aead = _payload_key(file_key, nonce)
    counter = 0
    buf = src.read(ENC_CHUNK)
    if not buf:
        raise FileDecryptError("no chunks")
    while True:
        nxt = src.read(ENC_CHUNK) if len(buf) == ENC_CHUNK else b""
        if len(buf) < TAG:
            raise FileDecryptError("truncated chunk")
        last = not nxt
        try:
            pt = aead.decrypt(_chunk_nonce(counter, last), buf, None)
        except InvalidTag:
            # A full chunk might carry the other flag: a final chunk followed
            # by more data, or a non-final chunk at the end of the file. Its
            # plaintext is authentic either way and is released (as the age
            # spec requires) before the stream is rejected.
            if len(buf) != ENC_CHUNK:
                raise FileDecryptError("bad tag") from None
            try:
                pt = aead.decrypt(_chunk_nonce(counter, not last), buf, None)
            except InvalidTag:
                raise FileDecryptError("bad tag") from None
            yield pt
            raise FileDecryptError("data after the final chunk" if not last else "missing final chunk")
        if last and not pt and counter > 0:
            raise FileDecryptError("empty final chunk after data")
        yield pt
        if last:
            return
        buf = nxt
        counter += 1


def encrypt_bytes(file_key: bytes, data: bytes) -> bytes:
    import io

    out = io.BytesIO()
    encrypt_stream(file_key, io.BytesIO(data), out)
    return out.getvalue()


def decrypt_bytes(file_key: bytes, data: bytes) -> bytes:
    import io

    return b"".join(decrypt_chunks(file_key, io.BytesIO(data)))
