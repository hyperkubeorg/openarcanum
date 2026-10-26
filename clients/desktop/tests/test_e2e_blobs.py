"""DM blob store end to end (docs/protocol.md §11) against real `meta` binaries."""

import hashlib
import os
import subprocess
import sys

import pytest
from conftest import META_DIRS, binary, run, run_meta

from openarcanum.meta import MetaClient, MetaError

# The maintainer node is Linux server software; its blob store isn't made
# to run on Windows.
pytestmark = pytest.mark.skipif(sys.platform == "win32", reason="the maintainer node runs on Linux")


def meta_blobs(url: str, *args: str) -> str:
    out = subprocess.run(
        [str(binary("meta")), "blobs", *args, "-data", META_DIRS[url]],
        capture_output=True,
        text=True,
        timeout=30,
        check=True,
    )
    return out.stdout


def test_blob_upload_download_ack_on_two_maintainers():
    with run_meta() as a, run_meta() as b, run_meta() as c:

        async def main():
            client = MetaClient([a, b, c])
            data = os.urandom((5 << 20) // 2 + 777)  # three chunks
            tokens = [os.urandom(32) for _ in range(3)]
            digest, nodes = await client.blob_upload_to(data, tokens)
            assert digest == hashlib.sha256(data).digest()
            assert nodes == [a, b], "two copies, on the first two nodes"
            # Uploading again resumes into the existing blobs.
            assert await client.blob_upload(data, tokens) == digest

            stats = meta_blobs(a, "stats")
            assert "stored: 1 blobs" in stats and digest.hex() in stats
            assert "stored: 0 blobs" in meta_blobs(c, "stats")

            for t in tokens:
                assert await client.blob_download(digest, t) == data
            # A node that has it serves it even if it is listed second.
            assert await client.blob_download(digest, tokens[0], [c, b]) == data
            with pytest.raises(MetaError) as e:
                await client.blob_download(digest, os.urandom(32))
            assert e.value.status == 404

            # Every device releases its token: the blob is gone everywhere.
            for t in tokens[:2]:
                assert await client.blob_ack(digest, t) == 3
            assert await client.blob_download(digest, tokens[2]) == data
            await client.blob_ack(digest, tokens[2])
            with pytest.raises(MetaError) as e:
                await client.blob_download(digest, tokens[2])
            assert e.value.status == 404
            assert "stored: 0 blobs" in meta_blobs(a, "stats")
            assert "stored: 0 blobs" in meta_blobs(b, "stats")

        run(main())


def test_blob_limits_and_admin_delete():
    with run_meta(blob_max_size=1 << 20, blob_quota_per_ip=3 << 20, blob_store_max=8 << 20) as m:

        async def main():
            client = MetaClient([m])
            token = os.urandom(32)
            with pytest.raises(MetaError) as e:
                await client.blob_upload(os.urandom((1 << 20) + 1), [token])
            assert e.value.status == 413
            with pytest.raises(ValueError):
                await client.blob_upload(b"x", [b"short"])
            with pytest.raises(ValueError):
                await client.blob_upload(b"x", [token] * 201)

            digests = [await client.blob_upload(os.urandom(1 << 20), [token]) for _ in range(3)]
            with pytest.raises(MetaError) as e:
                await client.blob_upload(os.urandom(10), [token])
            assert e.value.status == 429, "per-network quota"

            assert f"deleted {digests[0].hex()}" in meta_blobs(m, "delete", "-id", digests[0].hex())
            with pytest.raises(MetaError) as e:
                await client.blob_download(digests[0], token)
            assert e.value.status == 404
            assert await client.blob_download(digests[1], token)
            assert "deleted 0 blobs" in meta_blobs(m, "purge-expired")

        run(main())
