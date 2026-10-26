"""DM file attachments end to end (docs/protocol.md §9.7): real relay,
maintainer node (blob store) and community server binaries."""

import asyncio
import os
import re
import subprocess

import conftest
import pytest
from conftest import PASS, Events, binary, network, run, wait_until
from test_e2e_dm import community_of, key_of, poll

from openarcanum import dmfiles
from openarcanum.core import Core, UserError
from openarcanum.paths import Paths


def blob_count(meta: str) -> int:
    out = subprocess.run(
        [str(binary("meta")), "blobs", "stats", "-data", conftest.META_DIRS[meta]],
        capture_output=True,
        text=True,
        check=True,
    ).stdout
    stored = int(re.search(r"stored: (\d+) blobs", out).group(1))
    return stored + int(re.search(r"uploading: (\d+) blobs", out).group(1))


def png(w=640, h=480) -> bytes:
    from qtpy.QtCore import QBuffer, QIODevice
    from qtpy.QtGui import QImage

    img = QImage(w, h, QImage.Format_RGB32)
    img.fill(0x3366FF)
    buf = QBuffer()
    buf.open(QIODevice.WriteOnly)
    img.save(buf, "PNG")
    return bytes(buf.data())


async def direct(tmp_path, meta, node):
    cid, [(olive, _), (alice, _)] = await community_of(tmp_path, meta, node, "Alice")
    await olive.dms.add_contact(cid, await key_of(olive, cid, "Alice"))
    await alice.dms.add_contact(cid, await key_of(alice, cid, "Olive"))
    gid = (await olive.dms.open_with_member(cid, await key_of(olive, cid, "Alice")))["id"]

    async def active():
        await poll(alice, olive)
        return alice.dms.list() and alice.dms.info(gid)["status"] == "active"

    await wait_until(active, 20, 0.25)
    return cid, olive, alice, gid


def files_of(core, gid):
    return [f for m in core.dms.history(gid) for f in m.get("files", [])]


def test_live_transfer_stores_nothing(tmp_path, monkeypatch):
    monkeypatch.setattr(dmfiles, "LIVE_WAIT", 3.0)
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            _cid, olive, alice, gid = await direct(tmp_path, meta, node)
            data = os.urandom(1_300_000)
            src = tmp_path / "report.bin"
            src.write_bytes(data)
            await olive.dms.send(gid, "the report", files=[str(src)])

            async def arrived():
                await poll(alice)
                fs = files_of(alice, gid)
                return fs and fs[0]["state"] == "available"

            await wait_until(arrived, 20, 0.2)
            f = files_of(alice, gid)[0]
            assert (f["name"], f["size"], f["mime"]) == ("report.bin", len(data), "application/octet-stream")
            eid = next(m["id"] for m in alice.dms.history(gid) if m.get("files"))
            out = tmp_path / "saved.bin"
            await alice.dms.save_file(gid, eid, f["hash"], str(out))
            assert out.read_bytes() == data
            # Everyone had it live: nothing was stored on the maintainer.
            await asyncio.sleep(dmfiles.LIVE_WAIT + 1)
            assert blob_count(meta) == 0
            # Nothing readable left in the cache: only ciphertext.
            for p in (tmp_path / "alice" / "cache").rglob("*"):
                if p.is_file():
                    assert data[:4096] not in p.read_bytes()
            for c in (olive, alice):
                await c.shutdown()

        run(main(), 120)


def test_offline_recipient_fetches_from_store(tmp_path, monkeypatch):
    monkeypatch.setattr(dmfiles, "LIVE_WAIT", 2.0)
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            _cid, olive, alice, gid = await direct(tmp_path, meta, node)
            await alice.shutdown()  # offline: gets nothing live
            data = png()
            src = tmp_path / "photo.png"
            src.write_bytes(data)
            other = tmp_path / "notes.txt"
            other.write_bytes(b"plain notes")
            await olive.dms.send(gid, "", files=[str(src), str(other)])
            await wait_until(lambda: blob_count(meta) == 2, 20, 0.3)

            alice2 = Core(Paths.under(tmp_path / "alice"), emit=Events())
            await alice2.unlock(PASS)

            async def fetched():
                await poll(alice2)
                fs = files_of(alice2, gid)
                return len(fs) == 2 and all(f["state"] == "available" for f in fs)

            await wait_until(fetched, 30, 0.3)
            photo = next(f for f in files_of(alice2, gid) if f["name"] == "photo.png")
            assert photo["mime"] == "image/png" and (photo["width"], photo["height"]) == (640, 480)
            assert photo["thumb"].startswith("data:image/jpeg;base64,")
            eid = next(m["id"] for m in alice2.dms.history(gid) if m.get("files"))
            out = tmp_path / "out"
            out.mkdir()
            await alice2.dms.save_file(gid, eid, photo["hash"], str(out))
            assert (out / "photo.png").read_bytes() == data
            # Every recipient device released its token: the blobs are gone.
            await wait_until(lambda: blob_count(meta) == 0, 20, 0.3)
            # The maintainer's database holds only ciphertext.
            for p in os.listdir(conftest.META_DIRS[meta]):
                fp = os.path.join(conftest.META_DIRS[meta], p)
                if os.path.isfile(fp):
                    with open(fp, "rb") as fh:
                        assert b"plain notes" not in fh.read()
            for c in (olive, alice2):
                await c.shutdown()

        run(main(), 120)


def test_file_sent_just_before_closing_is_stored_at_the_next_start(tmp_path, monkeypatch):
    monkeypatch.setattr(dmfiles, "LIVE_WAIT", 30.0)
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            _cid, olive, alice, gid = await direct(tmp_path, meta, node)
            await alice.shutdown()  # offline: gets nothing live
            src = tmp_path / "late.txt"
            src.write_bytes(b"sent on the way out")
            await olive.dms.send(gid, "bye", files=[str(src)])
            # Olive closes before the live wait is over: nothing stored yet.
            await olive.shutdown()
            assert blob_count(meta) == 0

            olive2 = Core(Paths.under(tmp_path / "owner"), emit=Events())
            await olive2.unlock(PASS)
            await wait_until(lambda: blob_count(meta) == 1, 20, 0.3)
            await wait_until(lambda: not olive2.vault.data["dm"].get("file_stores"), 20, 0.3)

            alice2 = Core(Paths.under(tmp_path / "alice"), emit=Events())
            await alice2.unlock(PASS)

            async def fetched():
                await poll(alice2)
                fs = files_of(alice2, gid)
                return len(fs) == 1 and fs[0]["state"] == "available"

            await wait_until(fetched, 30, 0.3)
            f = files_of(alice2, gid)[0]
            eid = next(m["id"] for m in alice2.dms.history(gid) if m.get("files"))
            out = tmp_path / "out"
            out.mkdir()
            await alice2.dms.save_file(gid, eid, f["hash"], str(out))
            assert (out / "late.txt").read_bytes() == b"sent on the way out"
            # Stored once: a third start has nothing left to store.
            await olive2.shutdown()
            olive3 = Core(Paths.under(tmp_path / "owner"), emit=Events())
            await olive3.unlock(PASS)
            await asyncio.sleep(1)
            assert not olive3.vault.data["dm"].get("file_stores")
            for c in (olive3, alice2):
                await c.shutdown()

        run(main(), 120)


def test_attachment_limits_and_group_setting(tmp_path):
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            _cid, olive, alice, gid = await direct(tmp_path, meta, node)
            small = tmp_path / "a.txt"
            small.write_bytes(b"x")
            with pytest.raises(UserError, match="at most 10"):
                await olive.dms.send(gid, "", files=[str(small)] * 11)
            big = tmp_path / "big.bin"
            with open(big, "wb") as f:
                f.truncate(25 * 1024 * 1024 + 1)
            with pytest.raises(UserError, match="25 MB"):
                await olive.dms.send(gid, "", files=[str(big)])
            with pytest.raises(UserError, match="isn't a file"):
                await olive.dms.send(gid, "", files=[str(tmp_path / "missing")])
            for c in (olive, alice):
                await c.shutdown()

        run(main(), 120)


def test_receivers_enforce_allow_attachments(tmp_path):
    """The group owner turns attachments off; a member whose client ignores
    that still can't get files into anyone's history."""
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            cid, people = await community_of(tmp_path, meta, node, "Alice")
            (olive, _), (alice, _) = people
            await alice.dms.add_contact(cid, await key_of(alice, cid, "Olive"))
            gid = (await olive.dms.create_group(cid, [await key_of(olive, cid, "Alice")], "No files"))["id"]
            await poll(alice)
            await alice.dms.accept(gid)
            await poll(olive)
            await olive.dms.set_options(gid, allow_attachments=False)

            async def off():
                await poll(alice)
                return not alice.dms.info(gid)["allow_attachments"]

            await wait_until(off, 20, 0.25)
            f = tmp_path / "x.txt"
            f.write_bytes(b"x")
            with pytest.raises(UserError, match="turned off attachments"):
                await alice.dms.send(gid, "", files=[str(f)])
            # A modified client skips its own check: receivers still drop it.
            alice.dms.files.check = lambda *_a: None
            await alice.dms.send(gid, "with a file", files=[str(f)])

            async def got_text():
                await poll(olive)
                return any(m.get("text") == "with a file" for m in olive.dms.history(gid))

            await wait_until(got_text, 20, 0.25)
            assert files_of(olive, gid) == []
            for c in (olive, alice):
                await c.shutdown()

        run(main(), 120)


def test_expired_file(tmp_path, monkeypatch):
    monkeypatch.setattr(dmfiles, "LIVE_WAIT", 1.0)
    with network(tmp_path, blob_ttl="1m") as (meta, _relay, community):
        node = community()

        async def main():
            _cid, olive, alice, gid = await direct(tmp_path, meta, node)
            await alice.shutdown()
            src = tmp_path / "late.txt"
            src.write_bytes(b"too late")
            await olive.dms.send(gid, "", files=[str(src)])
            await wait_until(lambda: blob_count(meta) == 1, 20, 0.3)
            await asyncio.sleep(62)  # past the blob lifetime
            alice2 = Core(Paths.under(tmp_path / "alice"), emit=Events())
            await alice2.unlock(PASS)

            async def expired():
                await poll(alice2)
                fs = files_of(alice2, gid)
                return fs and fs[0]["state"] == "expired"

            await wait_until(expired, 30, 0.5)
            eid = next(m["id"] for m in alice2.dms.history(gid) if m.get("files"))
            with pytest.raises(UserError, match="expired"):
                await alice2.dms.save_file(gid, eid, files_of(alice2, gid)[0]["hash"], str(tmp_path / "o.txt"))
            for c in (olive, alice2):
                await c.shutdown()

        run(main(), 180)


def test_hostile_attachments_and_stored_events(tmp_path):
    """A member other than the sender can't redirect where a
    file is fetched from, file names can't escape the chosen folder, and a
    node can't make a device download more than the sender stated."""
    import base64

    from openarcanum import dmfiles as df
    from openarcanum.wire import dm_pb2

    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            cid, people = await community_of(tmp_path, meta, node, "Alice", "Bob")
            (olive, _), (alice, _), (bob, _) = people
            for c in (alice, bob):
                await c.dms.add_contact(cid, await key_of(c, cid, "Olive"))
            gid = (await olive.dms.create_group(cid, [await key_of(olive, cid, n) for n in ("Alice", "Bob")], "G"))[
                "id"
            ]
            for c in (alice, bob):
                await poll(c)
                await c.dms.accept(gid)
            await poll(olive, alice, bob)
            await bob.shutdown()  # offline: the file gets stored for Bob
            f = tmp_path / "..evil" / "x"
            f.parent.mkdir()
            f.write_bytes(b"payload")
            evil_name = tmp_path / "../../.bashrc"
            ref_src = tmp_path / "report.txt"
            ref_src.write_bytes(b"report")
            await olive.dms.send(gid, "files", files=[str(ref_src)])
            await wait_until(lambda: blob_count(meta) == 1, 20, 0.3)

            async def got():
                await poll(alice, olive)
                return files_of(alice, gid)

            await wait_until(got, 20, 0.25)
            entry = next(m for m in alice.dms.history(gid) if m.get("files"))
            fh = entry["files"][0]["hash"]
            # Modified clients skip their own checks when sending (simulated by
            # turning the local handler off); receivers must still refuse.
            alice.dms._on_stored = lambda *_a: None
            olive.dms._on_stored = lambda *_a: None
            # Alice (a member, not the sender) tries to redirect Bob's fetch.
            forged = dm_pb2.DmFileStored(
                hash=base64.b64decode(fh), nodes=["https://evil.example.invalid"], expires_at=1
            )
            await alice.dms.send_event(gid, dm_pb2.DmEvent(stored=forged))
            # A plain-http node is refused even from the sender.
            bad_nodes = dm_pb2.DmFileStored(hash=base64.b64decode(fh), nodes=["http://10.0.0.1:9/evil"])
            await olive.dms.send_event(gid, dm_pb2.DmEvent(stored=bad_nodes))
            bob2 = Core(Paths.under(tmp_path / "bob"), emit=Events())
            await bob2.unlock(PASS)

            async def fetched():
                await poll(bob2)
                fs = files_of(bob2, gid)
                return fs and fs[0]["state"] == "available"

            await wait_until(fetched, 30, 0.3)
            stored_f = next(f for m in bob2.dms.store.data[gid] for f in m.get("files", []))
            assert "10.0.0.1" not in str(stored_f["nodes"]) and "evil" not in str(stored_f["nodes"])
            # Hostile names are reduced to one safe component inside the folder.
            for hostile in ("../../.bashrc", "/etc/passwd", "a\\..\\b.txt", "CON"):
                assert "/" not in df.safe_name(hostile) and "\\" not in df.safe_name(hostile)
                assert not df.safe_name(hostile).startswith(".")
            stored_f["name"] = "../../escape.txt"
            out_dir = tmp_path / "downloads"
            out_dir.mkdir()
            await bob2.dms.files.save(stored_f, str(out_dir))
            assert (out_dir / "escape.txt").read_bytes() == b"report"
            assert not (tmp_path / "escape.txt").exists() and not evil_name.exists()
            for c in (olive, alice, bob2):
                await c.shutdown()

        run(main(), 180)


def test_download_size_must_match(tmp_path):
    with network(tmp_path) as (meta, _relay, _community):

        async def main():
            from openarcanum.meta import MetaClient, MetaError

            mc = MetaClient([meta])
            token = os.urandom(32)
            data = os.urandom(5000)
            digest, nodes = await mc.blob_upload_to(data, [token])
            assert await mc.blob_download(digest, token, nodes, size=len(data)) == data
            with pytest.raises(MetaError, match="different size"):
                await mc.blob_download(digest, token, nodes, size=len(data) - 1)

        run(main(), 60)


@pytest.mark.parametrize("offline", [False, True])
def test_file_in_a_first_contact_request_arrives_once_accepted(tmp_path, monkeypatch, offline):
    """A file sent with the first message to someone who doesn't have the
    sender as a contact (a message request) is listed in the request, and
    after accepting it downloads and saves; saving never hangs. Offline: the
    recipient is closed when it's sent, so it goes through the blob store."""
    monkeypatch.setattr(dmfiles, "LIVE_WAIT", 2.0)
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            cid, [(olive, _), (alice, _)] = await community_of(tmp_path, meta, node, "Alice")
            await alice.set_setting("dm_policy", "contacts")
            gid = (await olive.dms.open_with_member(cid, await key_of(olive, cid, "Alice")))["id"]
            data = b"first contact notes " * 50
            src = tmp_path / "notes.md"
            src.write_bytes(data)
            if offline:
                await alice.shutdown()
            await olive.dms.send(gid, "hello, here are the notes", files=[str(src)])
            if offline:
                await wait_until(lambda: blob_count(meta) == 1, 20, 0.3)
                alice = Core(Paths.under(tmp_path / "alice"), emit=Events())
                await alice.unlock(PASS)

            async def requested():
                await poll(alice)
                return any(c["id"] == gid and c["status"] == "request" for c in alice.dms.list())

            await wait_until(requested, 20, 0.25)
            await alice.dms.accept(gid)

            async def available():
                await poll(alice, olive)
                fs = files_of(alice, gid)
                return len(fs) == 1 and fs[0]["state"] == "available"

            await wait_until(available, 30, 0.3)
            f = files_of(alice, gid)[0]
            eid = next(m["id"] for m in alice.dms.history(gid) if m.get("files"))
            out = tmp_path / "out"
            out.mkdir()
            await asyncio.wait_for(alice.dms.save_file(gid, eid, f["hash"], str(out)), 15)
            assert (out / "notes.md").read_bytes() == data
            for c in (olive, alice):
                await c.shutdown()

        run(main(), 120)


def test_message_requests_contact_no_node_the_sender_chose(tmp_path, monkeypatch):
    """A stranger's message request must not make the recipient connect to
    a relay or fetch attachments from nodes the stranger named, which would
    leak the recipient's IP address to the stranger. Until the request is
    accepted, only the recipient's own maintainer nodes are contacted (the
    mailbox), also when another device syncs the conversation; accepting
    uses the conversation's relay and the file's nodes."""
    monkeypatch.setattr(dmfiles, "LIVE_WAIT", 1.0)
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            # The stranger's relay and blob node: anything that connects is
            # recorded (and gets nothing).
            seen: list[int] = []

            async def record(_reader, writer):
                seen.append(1)
                writer.close()

            server = await asyncio.start_server(record, "127.0.0.1", 0)
            port = server.sockets[0].getsockname()[1]
            evil_relay, evil_node = f"ws://127.0.0.1:{port}/ws", f"http://127.0.0.1:{port}"

            cid, [(olive, _), (alice, _)] = await community_of(tmp_path, meta, node, "Alice")
            # Alice (a stranger to Olive) puts her conversation on her own
            # relay and names her own node for the files; her client never
            # connects there itself, so anything recorded came from Olive's.
            alice.dms._pick_relay = lambda _cid: asyncio.sleep(0, evil_relay)
            real_relay = alice.dms._relay
            alice.dms._relay = lambda url: asyncio.sleep(0) if url == evil_relay else real_relay(url)
            real_upload = alice.meta.blob_upload_to

            async def upload(data, tokens, *args, **kwargs):
                digest, _nodes = await real_upload(data, tokens, *args, **kwargs)
                return digest, [evil_node]

            alice.meta.blob_upload_to = upload
            gid = (await alice.dms.open_with_member(cid, await key_of(alice, cid, "Olive")))["id"]
            await poll(olive)
            assert olive.dms.info(gid)["status"] == "request"
            # Olive is offline when the file comes: it's stored for her.
            await olive.shutdown()
            f = tmp_path / "hello.txt"
            f.write_bytes(b"hello")
            await alice.dms.send(gid, "have a look", files=[str(f)])
            await wait_until(lambda: blob_count(meta) == 1, 20, 0.3)

            # Olive starts again: the request is still there, and its text
            # and file arrive through her own maintainer node.
            olive = Core(Paths.under(tmp_path / "owner"), emit=Events())
            await olive.unlock(PASS)

            async def arrived():
                await poll(olive)
                fs = [x for m in olive.dms.store.data.get(gid, []) for x in m.get("files", [])]
                return fs and fs[0].get("token") and fs[0].get("nodes")

            await wait_until(arrived, 30, 0.3)
            await asyncio.sleep(1.5)
            assert olive.dms.info(gid)["status"] == "request"
            [entry] = [m for m in olive.dms.history(gid) if m.get("files")]
            assert entry["text"] == "have a look" and entry["files"][0]["state"] == "waiting"
            # Another of her devices telling her about the conversation (sync).
            data = olive.dms._group_sync(gid)
            kept = olive.dms.d["groups"].pop(gid)
            olive.dms.apply_group_sync(data)
            await asyncio.sleep(1.0)
            olive.dms.d["groups"][gid] = kept
            assert seen == [], "contacted the stranger's relay or node before accepting"
            # Saving the file would fetch it from the stranger's node.
            with pytest.raises(UserError, match="Accept"):
                await olive.dms.save_file(gid, entry["id"], entry["files"][0]["hash"], str(tmp_path / "out"))
            assert seen == []

            # Accepting: the conversation's relay and the file's nodes are used.
            await olive.dms.accept(gid)
            await wait_until(lambda: seen, 20, 0.2)
            server.close()
            for c in (olive, alice):
                await c.shutdown()

        run(main(), 180)
