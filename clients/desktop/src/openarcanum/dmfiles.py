"""DM file attachments (docs/protocol.md §9.7).

- Each file is encrypted on this device with a fresh random key (the age v1
  payload, `filecrypt.py`) into the cache; the key and the ciphertext hash
  travel only inside the Megolm-encrypted message.
- The ciphertext is first sent live, in chunks on the group's relay topic.
  Devices that are online assemble it, taking only chunks whose hash the
  encrypted message lists (any member can publish on the topic, so a forged
  chunk must not spoil the real one), check the whole hash and say so (a
  live-only `received` event).
- After a short wait, the sender uploads the ciphertext once to the blob
  store on maintainer nodes, with one fetch token per device that didn't
  confirm, and tells the group where it is (a `stored` event). If every
  device got it live, nothing is stored. Until that's done the file is
  listed in the vault, so if OpenArcanum closes first, the next start stores
  it for every recipient device.
- Devices that receive a `stored` event fetch the blob in the background,
  check its hash, keep the ciphertext in their cache and release their
  token; the maintainer deletes the blob when every token is released, or
  after its lifetime (72 h by default).
- Thumbnails are made on the sender's device and travel inside the
  encrypted message.
"""

from __future__ import annotations

import asyncio
import base64
import contextlib
import hashlib
import logging
import mimetypes
import os
import time
from pathlib import Path
from typing import TYPE_CHECKING

from google.protobuf.message import DecodeError

from openarcanum import filecrypt
from openarcanum.errors import UserError
from openarcanum.i18n import _
from openarcanum.meta import MetaError
from openarcanum.names import safe_name
from openarcanum.paths import atomic_write, ensure_dir
from openarcanum.relay import RelayError
from openarcanum.wire import dm_pb2 as dm

if TYPE_CHECKING:
    from openarcanum.dm import DMManager

log = logging.getLogger(__name__)

CHUNK_MAGIC = b"FDF1"
MAX_FILE = 25 * 1024 * 1024
MAX_FILES = 10
LIVE_CHUNK = 512 * 1024
# How long the sender waits for online devices to confirm a live copy
# before storing the file for the rest.
LIVE_WAIT = 6.0
THUMB_SIZE = 320
THUMB_MAX_BYTES = 48 * 1024
# The blob store's default lifetime; the maintainer enforces its own.
STORE_LIFETIME = 72 * 3600
# Assemblies in progress at once (bounded memory and disk).
MAX_ASSEMBLING = 20


def make_thumbnail(data: bytes, size: int = THUMB_SIZE) -> tuple[bytes, int, int]:
    """A small JPEG and the image size, or (b"", 0, 0) if it isn't an image
    or this client can't decode images (the web client has no Qt: the file
    goes without a preview)."""
    try:
        from qtpy.QtCore import QBuffer, QIODevice, Qt
        from qtpy.QtGui import QImage
    except ImportError:
        return b"", 0, 0

    img = QImage.fromData(data)
    if img.isNull():
        return b"", 0, 0
    w, h = img.width(), img.height()
    thumb = img.scaled(size, size, Qt.KeepAspectRatio, Qt.SmoothTransformation)
    for quality in (70, 50, 30):
        buf = QBuffer()
        buf.open(QIODevice.WriteOnly)
        thumb.save(buf, "JPEG", quality)
        out = bytes(buf.data())
        if len(out) <= THUMB_MAX_BYTES:
            return out, w, h
    return b"", w, h


def live_total(ciphertext_size: int) -> int:
    """How many live chunks carry a ciphertext of this size."""
    return max(1, -(-ciphertext_size // LIVE_CHUNK))


class DMFiles:
    def __init__(self, dms: DMManager):
        self.dms = dms
        self.core = dms.core
        # hash -> curves of devices that confirmed a live copy
        self._live_got: dict[bytes, set[bytes]] = {}
        # hash -> live assembly state (see expect)
        self._assembling: dict[bytes, dict] = {}
        self._fetching: set[bytes] = set()
        # Pending-store entries (ids) with a store task running in this run.
        self._storing: set[str] = set()

    @property
    def dir(self) -> Path:
        d = self.core.paths.cache / "dm-files"
        ensure_dir(d)
        return d

    def local(self, digest: bytes) -> Path | None:
        p = self.dir / digest.hex()
        return p if p.exists() else None

    # ---------------------------------------------------------------- sending

    def check(self, gid: str, paths: list[str]) -> None:
        st = self.dms._group(gid).state
        if not st.allow_attachments:
            raise UserError(_("The owner turned off attachments in this conversation."))
        if len(paths) > MAX_FILES:
            raise UserError(_("You can send at most {max} files in one message.").format(max=MAX_FILES))
        for p in paths:
            f = Path(p).expanduser()
            if not f.is_file():
                raise UserError(_("{name} isn't a file.").format(name=f.name or p))
            if f.stat().st_size > MAX_FILE:
                raise UserError(_("{name} is over the 25 MB limit.").format(name=f.name))

    def _encrypt(self, path: Path) -> dm.DmFileRef:
        """Encrypt one file into the cache, streaming (one chunk in memory)."""
        plain_size = path.stat().st_size
        if plain_size > MAX_FILE:
            raise UserError(_("{name} is over the 25 MB limit.").format(name=path.name))
        key = filecrypt.new_file_key()
        tmp = self.dir / f".enc-{os.urandom(8).hex()}"
        with open(path, "rb") as src, open(tmp, "wb") as out:
            size = filecrypt.encrypt_stream(key, src, out)
            out.flush()
            os.fsync(out.fileno())
        h = hashlib.sha256()
        chunks = []
        with open(tmp, "rb") as f:
            for block in iter(lambda: f.read(LIVE_CHUNK), b""):
                h.update(block)
                chunks.append(hashlib.sha256(block).digest())
        digest = h.digest()
        os.replace(tmp, self.dir / digest.hex())
        mime = mimetypes.guess_type(path.name)[0] or "application/octet-stream"
        thumb, w, ht = make_thumbnail(path.read_bytes()) if mime.startswith("image/") else (b"", 0, 0)
        return dm.DmFileRef(
            hash=digest,
            key=key,
            name=path.name[:255],
            mime=mime[:100],
            size=plain_size,
            ciphertext_size=size,
            width=w,
            height=ht,
            thumbnail=thumb,
            live_chunks=chunks or [hashlib.sha256(b"").digest()],
        )

    async def prepare(self, gid: str, paths: list[str]) -> list[dm.DmFileRef]:
        self.check(gid, paths)
        refs = []
        for p in paths:
            refs.append(await asyncio.to_thread(self._encrypt, Path(p).expanduser()))
        return refs

    async def after_send(self, gid: str, refs: list[dm.DmFileRef], recipients) -> None:
        """Send the ciphertext live, then store it for whoever didn't get it."""
        if not refs:
            return
        pending = self._remember(gid, refs, recipients)
        for ref in refs:
            self._live_got.setdefault(ref.hash, set())
            await self._send_live(gid, ref)
        self.core.spawn(self._store_for_the_rest(gid, refs, recipients, pending))

    # Files sent but not yet stored for offline devices are listed in the
    # vault until they are, so a file sent just before OpenArcanum closes (within
    # LIVE_WAIT) still reaches people who were offline: the next start stores
    # it (resume). Each entry: id, gid, the refs still owed, the recipients.

    def _pending(self) -> list[dict]:
        return self.dms.d.setdefault("file_stores", [])

    def _remember(self, gid: str, refs: list[dm.DmFileRef], recipients) -> str:
        pid = os.urandom(8).hex()
        self._pending().append(
            {
                "id": pid,
                "gid": gid,
                "at": int(time.time()),
                "refs": [base64.b64encode(r.SerializeToString()).decode() for r in refs],
                "devices": [
                    [base64.b64encode(ident).decode(), base64.b64encode(d.SerializeToString()).decode()]
                    for ident, d in recipients
                ],
            }
        )
        self.core.save_vault()
        return pid

    def _settled(self, pid: str, digest: bytes | None = None) -> None:
        """One file of a pending entry was dealt with (digest), or all of it."""
        entries = self._pending()
        for e in list(entries):
            if e.get("id") != pid:
                continue
            if digest is not None:
                e["refs"] = [r for r in e["refs"] if dm.DmFileRef.FromString(base64.b64decode(r)).hash != digest]
            if digest is None or not e["refs"]:
                entries.remove(e)
        self.core.save_vault()

    def resume(self) -> None:
        """Store files an earlier run sent but closed before storing."""
        now = time.time()
        for e in list(self._pending()):
            pid = e.get("id", "")
            if pid in self._storing:
                continue
            try:
                if now - e["at"] > STORE_LIFETIME or e["gid"] not in self.dms.d["groups"]:
                    raise ValueError("stale")
                refs = [dm.DmFileRef.FromString(base64.b64decode(r)) for r in e["refs"]]
                recipients = [
                    (base64.b64decode(i), dm.DmDevice.FromString(base64.b64decode(d))) for i, d in e["devices"]
                ]
            except KeyError, TypeError, ValueError, DecodeError:
                self._settled(pid)
                continue
            log.info("storing %d attachment(s) an earlier run sent", len(refs))
            self.core.spawn(self._store_for_the_rest(e["gid"], refs, recipients, pid, wait=0.0))

    async def _send_live(self, gid: str, ref: dm.DmFileRef) -> None:
        st = self.dms._group(gid).state
        relay = await self.dms._group_relay(gid)
        path = self.local(ref.hash)
        if relay is None or path is None:
            return
        total = live_total(ref.ciphertext_size)
        try:
            with open(path, "rb") as f:
                for i in range(total):
                    chunk = dm.DmFileChunk(hash=ref.hash, index=i, total=total, data=f.read(LIVE_CHUNK))
                    await relay.publish(st.topic, CHUNK_MAGIC + chunk.SerializeToString())
        except (RelayError, OSError) as e:
            log.info("live file send stopped: %s", e)

    async def _store_for_the_rest(
        self, gid: str, refs: list[dm.DmFileRef], recipients, pid: str, wait: float | None = None
    ) -> None:
        self._storing.add(pid)
        try:
            await asyncio.sleep(LIVE_WAIT if wait is None else wait)
            for ref in refs:
                await self._store_one(gid, ref, recipients, pid)
        finally:
            self._storing.discard(pid)

    async def _store_one(self, gid: str, ref: dm.DmFileRef, recipients, pid: str) -> None:
        got = self._live_got.pop(ref.hash, set())
        rest = [(ident, d) for ident, d in recipients if d.curve25519 not in got]
        if not rest:
            log.info("attachment delivered live to every device; not stored")
            self._settled(pid, ref.hash)
            return
        path = self.local(ref.hash)
        if path is None:
            self._settled(pid, ref.hash)
            return
        tokens = [dm.DeviceToken(curve25519=d.curve25519, token=os.urandom(32)) for _key, d in rest]
        try:
            digest, nodes = await self.core.meta.blob_upload_to(path.read_bytes(), [t.token for t in tokens])
        except MetaError as e:
            # Stays pending: the next start tries again.
            log.info("attachment upload failed: %s", e)
            self.core.emit(
                "toast",
                {"text": _("Couldn't store {name} for people who are offline: {error}").format(name=ref.name, error=e)},
            )
            return
        if digest != ref.hash:
            log.error("attachment hash changed on upload")
            self._settled(pid, ref.hash)
            return
        stored = dm.DmFileStored(
            hash=ref.hash, nodes=nodes, tokens=tokens, expires_at=int(time.time()) + STORE_LIFETIME
        )
        with contextlib.suppress(UserError):
            await self.dms.send_event(gid, dm.DmEvent(stored=stored))
        self._settled(pid, ref.hash)

    def on_received(self, sender_device: bytes, hashes: list[bytes]) -> None:
        for h in hashes:
            if h in self._live_got:
                self._live_got[h].add(sender_device)

    # ---------------------------------------------------------------- receiving

    def expect(self, refs) -> None:
        """A message with attachments arrived: accept live chunks for them."""
        for ref in refs:
            if self.local(ref.hash) or ref.hash in self._assembling:
                continue
            if len(self._assembling) >= MAX_ASSEMBLING or ref.ciphertext_size > MAX_FILE + 64 * 1024:
                continue
            total = live_total(ref.ciphertext_size)
            if len(ref.live_chunks) != total or any(len(h) != 32 for h in ref.live_chunks):
                # No (or wrong) chunk hashes: fetched from the store instead.
                continue
            self._assembling[ref.hash] = {
                "got": set(),
                "total": total,
                "chunks": list(ref.live_chunks),
                "path": self.dir / f"{ref.hash.hex()}.part",
                "size": ref.ciphertext_size,
                "at": time.time(),
            }

    async def on_chunk(self, blob: bytes) -> None:
        try:
            c = dm.DmFileChunk.FromString(blob[len(CHUNK_MAGIC) :])
        except Exception:
            return
        a = self._assembling.get(c.hash)
        if a is None or c.total != a["total"] or c.index >= a["total"] or c.index in a["got"]:
            return
        if hashlib.sha256(c.data).digest() != a["chunks"][c.index]:
            # Not the sender's chunk (published by someone else on the topic,
            # or damaged): ignored, and the real one is still taken.
            return
        mode = "r+b" if a["path"].exists() else "wb"
        with open(a["path"], mode) as f:
            f.seek(c.index * LIVE_CHUNK)
            f.write(c.data)
        a["got"].add(c.index)
        if len(a["got"]) < a["total"]:
            return
        self._assembling.pop(c.hash, None)
        data = a["path"].read_bytes()
        if hashlib.sha256(data).digest() != c.hash:
            a["path"].unlink(missing_ok=True)
            log.info("live attachment failed its hash check; will fetch it from the store")
            return
        os.replace(a["path"], self.dir / c.hash.hex())
        self.dms.file_available(c.hash)
        gid = self.dms.group_of_file(c.hash)
        if gid:
            with contextlib.suppress(UserError):
                await self.dms.send_event(gid, dm.DmEvent(received=dm.DmFileReceived(hashes=[c.hash])), live_only=True)

    async def fetch(self, gid: str, entry: dict, f: dict) -> None:
        """Download a stored attachment (in the background), check it, keep
        it, and release this device's token."""
        digest = base64.b64decode(f["hash"])
        if self.local(digest) or digest in self._fetching or not f.get("token"):
            return
        if not self.dms.reachable(gid):
            # A request nobody accepted: the sender chose these nodes.
            return
        self._fetching.add(digest)
        # The nodes the sender named, then the other members of their
        # maintainer clusters that this device knows: any member serves a
        # blob stored on one of them (docs/protocol.md §7.5), and an ack on
        # any member releases it everywhere.
        nodes = self.core.meta.blob_nodes(f["nodes"]) if f.get("nodes") else None
        try:
            data = await self.core.meta.blob_download(
                digest, base64.b64decode(f["token"]), nodes, size=f.get("ciphertext_size") or None
            )
            atomic_write(self.dir / digest.hex(), data)
            with contextlib.suppress(MetaError):
                await self.core.meta.blob_ack(digest, base64.b64decode(f["token"]), nodes)
            self.dms.file_available(digest)
        except MetaError as e:
            if e.status == 404:
                self.dms.file_expired(digest)
            else:
                log.info("attachment fetch failed: %s", e)
        finally:
            self._fetching.discard(digest)

    async def save(self, f: dict, dest: str) -> None:
        digest = base64.b64decode(f["hash"])
        src = self.local(digest)
        if src is None:
            raise UserError(
                _("This file has expired.") if f.get("state") == "expired" else _("This file hasn't arrived yet.")
            )
        out = Path(dest).expanduser()
        if out.is_dir():
            out = out / safe_name(f["name"])

        def run():
            tmp = out.with_name(f".{out.name}.part")
            try:
                with open(src, "rb") as inp, open(tmp, "wb") as w:
                    for chunk in filecrypt.decrypt_chunks(base64.b64decode(f["key"]), inp):
                        w.write(chunk)
                os.replace(tmp, out)
            except filecrypt.FileDecryptError:
                tmp.unlink(missing_ok=True)
                raise UserError(_("This file is damaged and can't be opened.")) from None
            except OSError as e:
                tmp.unlink(missing_ok=True)
                raise UserError(_("Couldn't save the file: {reason}").format(reason=e.strerror or e)) from None

        await asyncio.to_thread(run)

    def sweep(self) -> None:
        """Drop assemblies that stalled (the sender went away mid-transfer)."""
        now = time.time()
        for h, a in list(self._assembling.items()):
            if now - a["at"] > 600:
                self._assembling.pop(h, None)
                a["path"].unlink(missing_ok=True)
