"""Noise test vectors (cacophony and snow) against the vendored noiseprotocol.

Every vector whose suite the vendored library implements is run: handshake
messages and transport ciphertexts must match byte for byte, and the
handshake hash must match where the vector gives one. Vector files and their
sources: tests/vectors/noise/README.md. The Go side runs the same files in
internal/session/vectors_test.go.
"""

from __future__ import annotations

import hashlib
import json
import re
from pathlib import Path

import pytest
from cryptography.exceptions import InvalidTag

from openarcanum._vendor.noise.connection import Keypair, NoiseConnection
from openarcanum._vendor.noise.exceptions import NoiseInvalidMessage
from openarcanum.session import PROTOCOL_NAME

VECTORS = Path(__file__).resolve().parent / "vectors" / "noise"
FILES = ("cacophony.txt", "snow.txt")

# What the vendored library implements: the fundamental patterns (one-way and
# interactive), any psk modifiers, 25519/448, ChaChaPoly/AESGCM and four hashes.
SUPPORTED = re.compile(
    r"^Noise_(N|K|X|NN|KN|NK|KK|NX|KX|XN|IN|XK|IK|XX|IX)(psk\d(\+psk\d)*)?"
    r"_(25519|448)_(ChaChaPoly|AESGCM)_(SHA256|SHA512|BLAKE2s|BLAKE2b)$"
)
# What it does not: the deferred patterns (NK1, X1X, I1K1, ...) of Noise spec §18.
DEFERRED = re.compile(r"^Noise_[NKXI]1?[NKX]?1?_")


def _load(name: str) -> list[dict]:
    return json.loads((VECTORS / name).read_text(encoding="utf-8"))["vectors"]


ALL = [(f, i, v) for f in FILES for i, v in enumerate(_load(f))]
RUN = [(f, i, v) for f, i, v in ALL if SUPPORTED.match(v["protocol_name"])]


def _ids(cases):
    return [f"{f}:{i}:{v['protocol_name']}" for f, i, v in cases]


def test_vector_files_match_checksums():
    sums = {}
    for line in (VECTORS / "SHA256SUMS").read_text(encoding="utf-8").splitlines():
        digest, name = line.split()
        sums[name] = digest
    assert set(sums) == set(FILES)
    for name in FILES:
        assert hashlib.sha256((VECTORS / name).read_bytes()).hexdigest() == sums[name], name


def test_every_vector_is_run_or_deferred():
    skipped = {v["protocol_name"] for _, _, v in ALL if not SUPPORTED.match(v["protocol_name"])}
    assert all(DEFERRED.match(n) and re.search(r"\d", n.split("_")[1]) for n in skipped), skipped
    assert len(RUN) == 800  # 576 cacophony + 224 snow
    ours = [v for _, _, v in RUN if v["protocol_name"].encode() == PROTOCOL_NAME]
    assert len(ours) == 2  # one per file


def _connection(v: dict, role: str) -> NoiseConnection:
    n = NoiseConnection.from_name(v["protocol_name"].encode())
    if role == "init":
        n.set_as_initiator()
    else:
        n.set_as_responder()
    n.set_prologue(bytes.fromhex(v.get(f"{role}_prologue", "")))
    if v.get(f"{role}_static"):
        n.set_keypair_from_private_bytes(Keypair.STATIC, bytes.fromhex(v[f"{role}_static"]))
    if v.get(f"{role}_ephemeral"):
        n.set_keypair_from_private_bytes(Keypair.EPHEMERAL, bytes.fromhex(v[f"{role}_ephemeral"]))
    if v.get(f"{role}_remote_static"):
        n.set_keypair_from_public_bytes(Keypair.REMOTE_STATIC, bytes.fromhex(v[f"{role}_remote_static"]))
    psks = v.get(f"{role}_psks") or []
    if psks:
        n.set_psks(psks=[bytes.fromhex(p) for p in psks])
    n.start_handshake()
    return n


def _pair(v: dict) -> tuple[NoiseConnection, NoiseConnection, list[tuple[bool, bytes, bytes]]]:
    """Both ends of a vector and its messages as (initiator_sends, payload, ciphertext)."""
    init, resp = _connection(v, "init"), _connection(v, "resp")
    one_way = init.noise_protocol.pattern.one_way
    msgs = [
        (one_way or i % 2 == 0, bytes.fromhex(m["payload"]), bytes.fromhex(m["ciphertext"]))
        for i, m in enumerate(v["messages"])
    ]
    return init, resp, msgs


def _send(sender: NoiseConnection, payload: bytes) -> bytes:
    return sender.encrypt(payload) if sender.handshake_finished else bytes(sender.write_message(payload))


def _receive(receiver: NoiseConnection, ct: bytes) -> bytes:
    return receiver.decrypt(ct) if receiver.handshake_finished else bytes(receiver.read_message(ct))


# Vectors fix the ephemeral keys, which makes the library warn (correctly: it
# must never happen outside tests).
@pytest.mark.filterwarnings("ignore:One of ephemeral keypairs is already set")
@pytest.mark.parametrize(("name", "index", "v"), RUN, ids=_ids(RUN))
def test_vector(name, index, v):
    init, resp, msgs = _pair(v)
    for init_sends, payload, want in msgs:
        sender, receiver = (init, resp) if init_sends else (resp, init)
        ct = _send(sender, payload)
        assert ct == want
        assert _receive(receiver, ct) == payload
    assert init.handshake_finished and resp.handshake_finished
    if "handshake_hash" in v:
        want_hash = bytes.fromhex(v["handshake_hash"])
        assert init.get_handshake_hash() == want_hash
        assert resp.get_handshake_hash() == want_hash


OURS = [(f, i, v) for f, i, v in RUN if v["protocol_name"].encode() == PROTOCOL_NAME]


@pytest.mark.filterwarnings("ignore:One of ephemeral keypairs is already set")
@pytest.mark.parametrize(("name", "index", "v"), OURS, ids=_ids(OURS))
def test_vector_tampering_rejected(name, index, v):
    """For OpenArcanum's suite, flipping the first or last byte of any message after the first is rejected."""
    n = len(v["messages"])
    for target in range(1, n):
        for pos in (0, -1):
            init, resp, msgs = _pair(v)
            for k, (init_sends, payload, want) in enumerate(msgs):
                sender, receiver = (init, resp) if init_sends else (resp, init)
                ct = _send(sender, payload)
                assert ct == want
                if k < target:
                    _receive(receiver, ct)
                    continue
                bad = bytearray(ct)
                bad[pos] ^= 0x01
                with pytest.raises((InvalidTag, NoiseInvalidMessage)):
                    _receive(receiver, bytes(bad))
                break
