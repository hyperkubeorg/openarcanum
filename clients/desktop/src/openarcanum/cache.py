"""Encrypted local cache: message history and downloaded files.

Everything here can be fetched again from community servers, so it's safe to
delete. It follows each community's retention, and the cache size limit
(docs/settings.md) prunes the oldest material first.
"""

from __future__ import annotations

import json
import logging
import shutil
import time
from pathlib import Path

from openarcanum import crypto
from openarcanum.paths import atomic_write, ensure_dir

log = logging.getLogger(__name__)

PER_CHANNEL = 2000
MAGIC = b"OARCCC01"


class CommunityCache:
    def __init__(self, root: Path, key: bytes, community_id: str):
        self.root = root
        self.key = key
        self.cid = community_id
        self.messages: dict[int, list[dict]] = {}
        self.dirty = False
        self._load()

    @property
    def _file(self) -> Path:
        return self.root / "messages.bin"

    def _aad(self, what: str) -> bytes:
        return MAGIC + f"{self.cid}/{what}".encode()

    def _load(self) -> None:
        try:
            blob = self._file.read_bytes()
            data = json.loads(crypto.open_sealed(self.key, blob, self._aad("messages")))
            self.messages = {int(k): v for k, v in data.items()}
        except FileNotFoundError, crypto.DecryptError, json.JSONDecodeError, ValueError:
            self.messages = {}

    def save(self) -> None:
        if not self.dirty:
            return
        ensure_dir(self.root)
        atomic_write(self._file, crypto.seal(self.key, json.dumps(self.messages).encode(), self._aad("messages")))
        self.dirty = False

    # ---------------------------------------------------------------- messages

    def channel(self, channel_id: int) -> list[dict]:
        return self.messages.get(channel_id, [])

    def last_id(self, channel_id: int) -> int:
        msgs = self.messages.get(channel_id)
        return msgs[-1]["id"] if msgs else 0

    def upsert(self, msg: dict) -> bool:
        """Insert or replace by id, keeping order. Returns True if new."""
        msgs = self.messages.setdefault(msg["channel_id"], [])
        self.dirty = True
        for i in range(len(msgs) - 1, -1, -1):
            if msgs[i]["id"] == msg["id"]:
                msgs[i] = msg
                return False
            if msgs[i]["id"] < msg["id"]:
                msgs.insert(i + 1, msg)
                break
        else:
            msgs.insert(0, msg)
        if len(msgs) > PER_CHANNEL:
            del msgs[: len(msgs) - PER_CHANNEL]
        return True

    def merge(self, channel_id: int, batch: list[dict], newest: bool = False, more: bool = True) -> None:
        """Add a page of history. With `newest` (the server's latest page),
        cached messages in its range that the server no longer has, because
        they passed its retention, are dropped; with `more` false the page
        is everything the server has."""
        if newest:
            ids = {m["id"] for m in batch}
            oldest = min(ids) if ids else None
            msgs = self.messages.get(channel_id, [])
            keep = [m for m in msgs if m["id"] in ids or (more and oldest is not None and m["id"] < oldest)]
            if len(keep) != len(msgs):
                self.messages[channel_id] = keep
                self.dirty = True
        for m in batch:
            self.upsert(m)
        self.messages.setdefault(channel_id, [])

    def mark_deleted(self, channel_id: int, message_id: int) -> dict | None:
        for m in self.messages.get(channel_id, []):
            if m["id"] == message_id:
                m.update(deleted=True, text="", files=[], preview=None, reactions=[], pinned=False, mentions_me=False)
                self.dirty = True
                return m
        return None

    def find(self, channel_id: int, message_id: int) -> dict | None:
        for m in self.messages.get(channel_id, []):
            if m["id"] == message_id:
                return m
        return None

    def drop_channel(self, channel_id: int) -> None:
        if self.messages.pop(channel_id, None) is not None:
            self.dirty = True

    def prune(
        self, retention_days: dict[int, int], file_retention_days: dict[int, int], now_ms: int | None = None
    ) -> int:
        """Apply each channel's retention (0 = keep). Returns messages removed."""
        now_ms = now_ms or int(time.time() * 1000)
        removed = 0
        day = 86_400_000
        for ch, msgs in self.messages.items():
            days = retention_days.get(ch, 0)
            if days:
                cutoff = now_ms - days * day
                keep = [m for m in msgs if m["created_at"] > cutoff]
                removed += len(msgs) - len(keep)
                if len(keep) != len(msgs):
                    self.messages[ch] = keep
                    self.dirty = True
            fdays = file_retention_days.get(ch, 0)
            if fdays:
                cutoff = now_ms - fdays * day
                for m in self.messages[ch]:
                    if m["created_at"] > cutoff:
                        continue
                    for i, f in enumerate(m.get("files") or []):
                        if not f.get("expired"):
                            self.drop_file(f["file_id"])
                            m["files"][i] = {**f, "expired": True, "key": ""}
                            self.dirty = True
                    img = (m.get("preview") or {}).get("image")
                    if img:
                        self.drop_file(img["file_id"])
                        m["preview"] = {**m["preview"], "image": None}
                        self.dirty = True
        return removed

    def trim_oldest(self, fraction: float = 0.25) -> None:
        for ch, msgs in self.messages.items():
            n = int(len(msgs) * fraction)
            if n:
                self.messages[ch] = msgs[n:]
                self.dirty = True

    # ---------------------------------------------------------------- files

    def _file_path(self, file_id: int) -> Path:
        return self.root / "files" / f"{int(file_id)}.bin"

    def put_file(self, file_id: int, data: bytes) -> None:
        ensure_dir(self.root / "files")
        atomic_write(self._file_path(file_id), crypto.seal(self.key, data, self._aad(f"file/{file_id}")))

    def get_file(self, file_id: int) -> bytes | None:
        """The cached file, or None if it isn't cached. A cached copy that
        fails to decrypt (damaged or altered on disk) is reported and
        removed, then treated as not cached so it downloads again."""
        try:
            blob = self._file_path(file_id).read_bytes()
        except FileNotFoundError:
            return None
        try:
            return crypto.open_sealed(self.key, blob, self._aad(f"file/{file_id}"))
        except crypto.DecryptError:
            log.warning("cached file %d is damaged or was altered; removing it", file_id)
            self.drop_file(file_id)
            return None

    def drop_file(self, file_id: int) -> None:
        try:
            self._file_path(file_id).unlink()
        except FileNotFoundError:
            pass

    def drop_oldest_files(self) -> bool:
        files = sorted((self.root / "files").glob("*.bin"), key=lambda p: p.stat().st_mtime)
        if not files:
            return False
        for p in files[: max(1, len(files) // 4)]:
            p.unlink(missing_ok=True)
        return True

    # ---------------------------------------------------------------- whole cache

    def size(self) -> int:
        if not self.root.exists():
            return 0
        return sum(p.stat().st_size for p in self.root.rglob("*") if p.is_file())

    def wipe(self) -> None:
        self.messages = {}
        self.dirty = False
        shutil.rmtree(self.root, ignore_errors=True)


def enforce_limit(caches: list[CommunityCache], limit_bytes: int) -> None:
    """Prune the cache until it fits: downloaded files first, then old history."""
    for _ in range(50):
        total = sum(c.size() for c in caches)
        if total <= limit_bytes:
            return
        if any(c.drop_oldest_files() for c in caches):
            continue
        for c in caches:
            c.trim_oldest()
            c.save()
