"""DM file encryption: the age v1 payload (STREAM), checked against the C2SP
CCTV age test vectors (tests/vectors/age, see README there)."""

import hashlib
import io
import os
import zlib
from pathlib import Path

import pytest
from hypothesis import given, settings
from hypothesis import strategies as st
from test_fuzz_decoders import FUZZ

from openarcanum import filecrypt
from openarcanum.filecrypt import CHUNK, FileDecryptError

VECTORS = Path(__file__).parent / "vectors" / "age"
FILES = sorted(p for p in VECTORS.iterdir() if p.name not in ("SHA256SUMS", "CCTV-age-README.md", "README.md"))


def test_vector_files_are_intact():
    sums = {}
    for line in (VECTORS / "SHA256SUMS").read_text().splitlines():
        digest, name = line.split(maxsplit=1)
        sums[name.strip()] = digest
    assert len(FILES) == 37 and set(sums) >= {p.name for p in FILES}
    for p in FILES:
        assert hashlib.sha256(p.read_bytes()).hexdigest() == sums[p.name], p.name


def load(path: Path):
    raw = path.read_bytes()
    meta_raw, body = raw.split(b"\n\n", 1)
    meta = dict(line.split(": ", 1) for line in meta_raw.decode().splitlines())
    if meta.get("compressed") == "zlib":
        body = zlib.decompress(body)
    # The payload starts after the age header's MAC line ("--- <mac>\n").
    end = body.index(b"\n---")
    payload = body[body.index(b"\n", end + 1) + 1 :]
    return meta, bytes.fromhex(meta["file key"]), payload


@pytest.mark.parametrize("path", FILES, ids=[p.name for p in FILES])
def test_cctv_age_payload_vectors(path):
    meta, key, payload = load(path)
    released = []
    if meta["expect"] == "success":
        for chunk in filecrypt.decrypt_chunks(key, io.BytesIO(payload)):
            released.append(chunk)
    else:
        assert meta["expect"] == "payload failure"
        with pytest.raises(FileDecryptError):
            for chunk in filecrypt.decrypt_chunks(key, io.BytesIO(payload)):
                released.append(chunk)
    # Everything released, even before a failure, must match the expected hash.
    assert hashlib.sha256(b"".join(released)).hexdigest() == meta["payload"]


@pytest.mark.parametrize("size", [0, 1, CHUNK - 1, CHUNK, CHUNK + 1, 2 * CHUNK, 3 * CHUNK + 7])
def test_round_trip_at_chunk_boundaries(size):
    key = filecrypt.new_file_key()
    data = os.urandom(size)
    ct = filecrypt.encrypt_bytes(key, data)
    chunks = max(1, -(-size // CHUNK))
    assert len(ct) == filecrypt.NONCE_SIZE + size + chunks * filecrypt.TAG
    assert filecrypt.decrypt_bytes(key, ct) == data


def test_failures():
    key = filecrypt.new_file_key()
    data = os.urandom(CHUNK * 2 + 5)
    ct = filecrypt.encrypt_bytes(key, data)
    with pytest.raises(FileDecryptError):
        filecrypt.decrypt_bytes(filecrypt.new_file_key(), ct)
    for cut in (len(ct) - 1, filecrypt.NONCE_SIZE + filecrypt.ENC_CHUNK, filecrypt.NONCE_SIZE, 3):
        with pytest.raises(FileDecryptError):
            filecrypt.decrypt_bytes(key, ct[:cut])
    with pytest.raises(FileDecryptError):
        filecrypt.decrypt_bytes(key, ct + b"x")
    bad = bytearray(ct)
    bad[filecrypt.NONCE_SIZE + 10] ^= 1
    with pytest.raises(FileDecryptError):
        filecrypt.decrypt_bytes(key, bytes(bad))
    # Swapping two full chunks breaks the counter binding.
    a = filecrypt.NONCE_SIZE
    b = a + filecrypt.ENC_CHUNK
    swapped = ct[:a] + ct[b : b + filecrypt.ENC_CHUNK] + ct[a:b] + ct[b + filecrypt.ENC_CHUNK :]
    with pytest.raises(FileDecryptError):
        filecrypt.decrypt_bytes(key, swapped)
    with pytest.raises(ValueError):
        filecrypt.encrypt_bytes(b"short", b"x")


# At least 200 examples; the nightly fuzz profile (make fuzz-py) raises it.
@settings(FUZZ, max_examples=max(200, FUZZ.max_examples))
@given(st.binary(max_size=3 * CHUNK), st.binary(min_size=16, max_size=16))
def test_decrypt_never_crashes_on_garbage(blob, key):
    try:
        filecrypt.decrypt_bytes(key, blob)
    except FileDecryptError:
        pass
