"""Failures that are reported and recovered from rather than ignored:
damaged cache files, channel events under a key epoch this client hasn't
got yet."""

import asyncio
import logging
import os

from test_fuzz_decoders import SERVER, FakeCore

from openarcanum import community, crypto, keys
from openarcanum.cache import CommunityCache
from openarcanum.wire import community_pb2 as cp


def test_damaged_cached_file_is_reported_and_removed(tmp_path, caplog):
    cache = CommunityCache(tmp_path, os.urandom(32), "c")
    cache.put_file(7, b"file body")
    assert cache.get_file(7) == b"file body"
    path = tmp_path / "files" / "7.bin"
    blob = bytearray(path.read_bytes())
    blob[-1] ^= 1
    path.write_bytes(bytes(blob))
    with caplog.at_level(logging.WARNING):
        assert cache.get_file(7) is None
    assert "damaged or was altered" in caplog.text
    assert not path.exists()  # downloads again next time
    # Missing is just missing (no warning).
    caplog.clear()
    assert cache.get_file(8) is None
    assert not caplog.text


def test_newer_key_epoch_triggers_a_refresh():
    async def main():
        cid = "c" * 16
        core = FakeCore(cid)
        spawned = []
        core.spawn = lambda coro: spawned.append(coro) or coro.close()
        conn = community.CommunityConnection(core, cid)
        conn.server_keys = [SERVER.public]
        key = crypto.random_key()
        conn.channel_keys = {5: {1: key}}

        def signed_event(epoch: int) -> bytes:
            nonce = os.urandom(12)
            ct = crypto.encrypt_with_nonce(os.urandom(32), nonce, b"x", community.channel_aad(5, epoch))
            env = cp.ChannelEnvelope(channel_id=5, key_epoch=epoch, nonce=nonce, ciphertext=ct).SerializeToString()
            return cp.SignedEnvelope(
                envelope=env, signature=SERVER.sign(keys.CTX_CHANNEL_ENVELOPE, env)
            ).SerializeToString()

        conn._on_topic(b"t", signed_event(2))
        assert len(spawned) == 1  # asked the server for current keys
        conn._on_topic(b"t", signed_event(2))
        assert len(spawned) == 1  # at most every 10 s
        conn._keys_refreshed -= 11
        conn._on_topic(b"t", signed_event(3))
        assert len(spawned) == 2
        # An older epoch than any we hold isn't a reason to refresh.
        conn.channel_keys = {5: {4: key}}
        conn._keys_refreshed -= 11
        conn._on_topic(b"t", signed_event(1))
        assert len(spawned) == 2

    asyncio.run(main())
