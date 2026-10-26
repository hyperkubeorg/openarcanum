"""Maintainer node client: directory search, routes, relay registry,
mailboxes, the DM blob store and link nameplates (docs/protocol.md §7, §11).

Everything a maintainer returns is checked here: listings and routes must be
signed by a server key that a key in the community's owner set certified
(owners.py), so a maintainer can drop entries but never alter them.
"""

from __future__ import annotations

import asyncio
import hashlib
import logging
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass, field

from google.protobuf.message import DecodeError, Message

from openarcanum import keys
from openarcanum import version as proto_version
from openarcanum.i18n import _
from openarcanum.nodes import NodeHealth, verify_set
from openarcanum.owners import OwnerSet, verify_cert
from openarcanum.wire import community_pb2 as cp
from openarcanum.wire import meta_pb2 as mp
from openarcanum.wire import session_pb2 as sp

log = logging.getLogger(__name__)
CONTENT_TYPE = "application/x-protobuf"


class MetaError(Exception):
    def __init__(self, message: str, status: int = 0):
        super().__init__(message)
        self.status = status


class MetaVersionError(MetaError):
    """No protocol version in common with a maintainer node (HTTP 426); the
    message says which side is outdated."""

    def __init__(self, mismatch: proto_version.VersionMismatch):
        super().__init__(mismatch.user_message(), 426)
        self.mismatch = mismatch


class MetaTooLarge(MetaError):
    """A maintainer node's reply was longer than the caller allowed."""


# The status of directory requests (listings, search, snapshots) to a node
# whose public directory is switched off (docs/protocol.md §7.2).
DIRECTORY_OFF = 501

# The most any one reply may hold (a mailbox page is at most 24 MiB plus
# framing; a blob chunk 1 MiB).
MAX_REPLY = 64 << 20


# Local directory search (docs/protocol.md §7.2): the most a snapshot
# download may hold, in listings and bytes, so a node can't exhaust memory.
MAX_SNAPSHOT_LISTINGS = 50_000
MAX_SNAPSHOT_BYTES = 64 << 20


def inbox_address(token: bytes) -> bytes:
    return hashlib.sha256(b"openarcanum/inbox/v1\x00" + token).digest()


def mailbox_gid(inbox: bytes, blob: bytes) -> bytes:
    """A mailbox item's global id, the same on every node of a maintainer
    cluster (docs/protocol.md §7.5). Computed here, never taken from a node."""
    return hashlib.sha256(b"openarcanum/mailbox-item/v1\x00" + inbox + blob).digest()


def blob_token_hash(token: bytes) -> bytes:
    """What a sender registers for a blob fetch token (docs/protocol.md §11)."""
    return hashlib.sha256(b"openarcanum/blob-token/v1\x00" + token).digest()


# Outside a maintainer cluster blobs aren't replicated, so a sender stores
# copies on this many nodes when it can (docs/protocol.md §11).
BLOB_COPIES = 2
MAX_BLOB_TOKENS = 200
# The largest blob a client will download (25 MiB file + encryption overhead).
MAX_BLOB_SIZE = 26 * 1024 * 1024


def _parse(msg: Message, raw: bytes, what: str) -> None:
    """Parse signed content; malformed bytes fail verification like a bad signature."""
    try:
        msg.ParseFromString(raw)
    except DecodeError as e:
        raise keys.BadSignature(f"malformed {what}") from e


def _reply(msg: Message, raw: bytes) -> Message:
    """Parse a maintainer node's reply; malformed bytes are that node's
    failure (MetaError), so callers move on to the next node."""
    try:
        msg.ParseFromString(raw)
    except DecodeError as e:
        raise MetaError(_("the maintainer node sent a damaged {kind}").format(kind=type(msg).__name__)) from e
    return msg


def verify_listing(sl: mp.SignedListing) -> mp.Listing:
    """Raises keys.BadSignature unless the listing verifies end to end."""
    lst = mp.Listing()
    _parse(lst, sl.listing, "listing")
    verify_cert(sl.cert, lst.server_key, lst.community)
    keys.verify(lst.server_key, keys.CTX_LISTING, sl.listing, sl.signature)
    return lst


def listing_icon(sl: mp.SignedListing, lst: mp.Listing) -> bytes:
    """The icon next to a verified listing, if it matches the hash the listing
    signs (snapshots carry none; a mismatching one is dropped)."""
    if sl.icon and lst.icon_sha256 and hashlib.sha256(sl.icon).digest() == lst.icon_sha256:
        return bytes(sl.icon)
    return b""


def verify_route(sr: mp.SignedRoute, community: bytes | None) -> tuple[mp.Route, sp.ServerCert, OwnerSet]:
    """Raises keys.BadSignature unless the route verifies (for community, if given)."""
    rt = mp.Route()
    _parse(rt, sr.route, "route")
    cert, owner_set = verify_cert(sr.cert, rt.server_key, community)
    keys.verify(rt.server_key, keys.CTX_ROUTE, sr.route, sr.signature)
    return rt, cert, owner_set


STATES = {
    mp.COMMUNITY_STATE_ONLINE: "online",
    mp.COMMUNITY_STATE_DEGRADED: "degraded",
    mp.COMMUNITY_STATE_OFFLINE: "offline",
    mp.COMMUNITY_STATE_TERMINATED: "terminated",
}


def status_dict(st: mp.CommunityStatus, server_key: bytes | None) -> dict | None:
    """A maintainer's CommunityStatus (docs/protocol.md §7.4) for the UI, or
    None when there's nothing to say. The heartbeat must be signed by
    server_key (the key this client knows for the community) and name the
    same route; otherwise only a "terminated" verdict is kept (a maintainer
    can withhold reports, never invent a server's). `last_seen` and the relay
    counts come from the verified heartbeat."""
    state = STATES.get(st.state, "")
    reap_days = round(st.reap_after_seconds / 86400) if st.reap_after_seconds else 7
    if state == "terminated":
        return {
            "state": state,
            "last_seen": st.last_seen,
            "reaped_at": st.reaped_at,
            "reap_after_days": reap_days,
        }
    if not state or not st.HasField("heartbeat") or not server_key:
        return None
    hb = mp.CommunityHeartbeat()
    try:
        hb.ParseFromString(st.heartbeat.heartbeat)
        keys.verify(server_key, keys.CTX_COMMUNITY_HEARTBEAT, st.heartbeat.heartbeat, st.heartbeat.signature)
    except DecodeError, keys.BadSignature:
        log.info("dropping a community status whose heartbeat doesn't verify")
        return None
    if hb.route_id != st.route_id or hb.server_key != server_key:
        return None
    return {
        "state": state,
        "last_seen": max(hb.at, 0),
        "stopping": hb.stopping,
        "relays_attached": hb.relays_attached,
        "relays_wanted": hb.relays_wanted,
        "pinned": hb.pinned,
        "reap_after_days": reap_days,
    }


def listing_dict(lst: mp.Listing, icon: bytes = b"") -> dict:
    return {
        "community": keys.encode(lst.community),
        "server_key": keys.encode(lst.server_key),
        "route_id": keys.encode(lst.route_id),
        "name": lst.name,
        "description": lst.description,
        "tags": list(lst.tags),
        "language": lst.language,
        "member_range": lst.member_range,
        "join_policy": cp.JoinPolicy.Name(lst.join_policy).removeprefix("JOIN_POLICY_").lower(),
        "has_public_key": bool(lst.public_join_key),
        "public_join_key": lst.public_join_key,
        "relays": list(lst.relays),
        "icon": keys.encode(icon) if icon else "",
        "published_at": lst.published_at,
    }


@dataclass
class MetaClient:
    urls: list[str]
    timeout: float = 10.0
    # The nodes grouped by maintainer cluster (each cluster's members
    # together; an independent node alone). Empty: each URL is its own group.
    groups: list[list[str]] = field(default_factory=list)
    # Per-node success and backoff (nodes.py), shared by every request.
    health: NodeHealth = field(default_factory=NodeHealth)
    # Whether each node serves the public directory, as last learned (GET
    # /v1/node, or a 501 "directory disabled"); a node not in it is assumed
    # to (docs/protocol.md §7.2).
    directory: dict[str, bool] = field(default_factory=dict)

    def directory_urls(self) -> list[str]:
        """The nodes to search and list through: those not known to have
        their directory off."""
        return [u for u in self.urls if self.directory.get(u) is not False]

    def directory_available(self) -> bool:
        """False once every node whose state is known has its directory off:
        the app then hides finding and listing communities."""
        known = [self.directory[u] for u in self.urls if u in self.directory]
        return not known or any(known)

    def node_groups(self) -> list[list[str]]:
        groups = [[u for u in g if u in self.urls] for g in self.groups]
        groups = [g for g in groups if g]
        grouped = {u for g in groups for u in g}
        return groups + [[u] for u in self.urls if u not in grouped]

    def cluster_of(self, url: str) -> list[str]:
        """The members of url's cluster that this client uses (url alone if
        it isn't in one)."""
        return next((g for g in self.node_groups() if url in g), [url])

    def poll_nodes(self, full: bool) -> list[str]:
        """Where to read mailboxes: the healthiest member of each cluster
        and every independent node (the fast loop), or every node not
        backing off (full: a cluster isn't trusted to be complete, since a
        sender may have reached only another member). A cluster whose nodes
        all back off is still read at its soonest."""
        out: list[str] = []
        for g in self.node_groups():
            order = self.health.order(g)
            usable = [u for u in order if self.health.usable(u)]
            out.extend((usable if full else usable[:1]) or order[:1])
        return out

    def _request(self, base: str, method: str, path: str, body: bytes | None, limit: int = MAX_REPLY) -> bytes:
        """One request; a reply longer than `limit` bytes raises
        MetaTooLarge without reading more than limit + 1 bytes of it."""
        req = urllib.request.Request(base.rstrip("/") + path, data=body, method=method)
        if body is not None:
            req.add_header("Content-Type", CONTENT_TYPE)
        # Every request announces the protocol versions this build speaks
        # (docs/protocol.md §7); the node answers with its choice, or 426.
        req.add_header(proto_version.HEADER, proto_version.fmt(proto_version.SUPPORTED))
        started = time.monotonic()
        try:
            data = self._send(req, limit)
        except MetaError as e:
            # A node that answered (4xx, or 501 for a directory that is off)
            # is up; one that didn't, or failed (5xx), backs off.
            if (e.status == 0 or e.status >= 500) and e.status != DIRECTORY_OFF and not isinstance(e, MetaTooLarge):
                self.health.failed(base, str(e))
            else:
                self.health.ok(base, time.monotonic() - started)
            raise
        self.health.ok(base, time.monotonic() - started)
        return data

    def _send(self, req: urllib.request.Request, limit: int) -> bytes:
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as resp:
                chosen = proto_version.parse(resp.headers.get(proto_version.HEADER, ""))
                if len(chosen) == 1 and chosen[0] not in proto_version.SUPPORTED:
                    raise MetaError(
                        _("the maintainer node needs a different version of OpenArcanum ({version})").format(
                            version=chosen[0]
                        )
                    )
                data = resp.read(limit + 1)
                if len(data) > limit:
                    raise MetaTooLarge(f"maintainer node's reply is larger than {limit} bytes")
                return data
        except urllib.error.HTTPError as e:
            if e.code == 426:
                theirs = proto_version.parse(e.headers.get(proto_version.HEADER, ""))
                try:
                    proto_version.negotiate(proto_version.SUPPORTED, theirs, what="maintainer node")
                    mismatch = proto_version.VersionMismatch(proto_version.SUPPORTED, theirs, False, "maintainer node")
                except proto_version.VersionMismatch as m:
                    mismatch = m
                raise MetaVersionError(mismatch) from None
            msg = e.read(512).decode(errors="replace").strip() or e.reason
            raise MetaError(msg, e.code) from None
        except (urllib.error.URLError, OSError, TimeoutError) as e:
            raise MetaError(
                _("can't reach the maintainer node: {reason}").format(reason=getattr(e, "reason", e))
            ) from None

    async def _first(
        self,
        method: str,
        path: str,
        body: bytes | None = None,
        out: Message | None = None,
        urls: list[str] | None = None,
        check=None,
    ):
        """Ask maintainers in order until one answers. HTTP errors other than
        5xx are final (the node answered). With check, a reply it rejects
        (keys.BadSignature) counts as that node's failure: the next is asked."""
        if not self.urls:
            raise MetaError(_("no maintainer nodes are configured"))
        if urls is not None and not urls:
            raise MetaError(_("Public listing isn't available on your maintainer nodes."), DIRECTORY_OFF)
        last: MetaError | None = None
        bad: keys.BadSignature | None = None
        for base in self.health.order(list(self.urls if urls is None else urls)):
            try:
                raw = await asyncio.to_thread(self._request, base, method, path, body)
            except MetaError as e:
                last = e
                if e.status == DIRECTORY_OFF:
                    self.directory[base] = False
                if 400 <= e.status < 500 and e.status != 426:
                    raise
                continue
            if out is not None:
                reply = _reply(out, raw)
                if check is not None:
                    try:
                        check(reply)
                    except keys.BadSignature as e:
                        bad = e
                        continue
                return reply
            return raw
        if bad is not None:
            raise bad
        assert last is not None
        raise last

    async def _all(self, path: str, body: bytes) -> int:
        """Send a mailbox item to every cluster and independent node
        (docs/protocol.md §7.5): within a cluster, to its two healthiest
        members in parallel (members replicate it to each other; the same
        bytes are one item everywhere), then to the others one by one until
        one takes it. Returns how many nodes took it; fails only if none did."""

        async def to_group(group: list[str]) -> tuple[int, BaseException | None]:
            order = self.health.order(group)
            usable = [b for b in order if self.health.usable(b)]
            first = usable[:2]
            rest = [b for b in order if b not in first]
            results = await asyncio.gather(
                *(asyncio.to_thread(self._request, b, "POST", path, body) for b in first), return_exceptions=True
            )
            ok = sum(1 for r in results if not isinstance(r, BaseException))
            last = next((r for r in results if isinstance(r, BaseException)), None)
            for b in rest:
                if ok:
                    break
                try:
                    await asyncio.to_thread(self._request, b, "POST", path, body)
                    ok += 1
                except MetaError as e:
                    last = e
            return ok, last

        results = await asyncio.gather(*(to_group(g) for g in self.node_groups()))
        ok = sum(n for n, _e in results)
        if ok == 0 and results:
            err = next((e for _n, e in results if e is not None), None) or MetaError(_("no maintainer node took it"))
            raise err if isinstance(err, MetaError) else MetaError(str(err))
        return ok

    # ---------------------------------------------------------------- directory

    async def search(self, query: str = "", tag: str = "", limit: int = 25) -> list[dict]:
        qs = urllib.parse.urlencode({k: v for k, v in (("q", query), ("tag", tag), ("limit", limit)) if v})
        resp = await self._first("GET", f"/v1/search?{qs}", out=mp.SearchResponse(), urls=self.directory_urls())
        statuses = {bytes(st.route_id): st for st in resp.statuses}
        out = []
        for sl in resp.listings:
            try:
                lst = verify_listing(sl)
                d = listing_dict(lst, listing_icon(sl, lst))
            except keys.BadSignature, Exception:
                log.warning("dropping a directory result that doesn't verify")
                continue
            st = statuses.get(bytes(lst.route_id))
            d["status"] = status_dict(st, lst.server_key) if st is not None else None
            out.append(d)
        return out

    async def snapshot(
        self, max_listings: int = MAX_SNAPSHOT_LISTINGS, max_bytes: int = MAX_SNAPSHOT_BYTES
    ) -> tuple[list[dict], bool]:
        """Download the directory for local search from the first node that
        answers, page by page (docs/protocol.md §7.2). Every listing is
        verified like a search result; ones that don't verify are dropped.
        Returns (listings, complete): the download stops at max_listings or
        max_bytes, and complete is False when it did."""
        if not self.urls:
            raise MetaError(_("no maintainer nodes are configured"))
        urls = self.directory_urls()
        if not urls:
            raise MetaError(_("Public listing isn't available on your maintainer nodes."), DIRECTORY_OFF)
        last: MetaError | None = None
        for base in urls:
            try:
                return await asyncio.to_thread(self._snapshot_from, base, max_listings, max_bytes)
            except MetaError as e:
                last = e
                if e.status == DIRECTORY_OFF:
                    self.directory[base] = False
        assert last is not None
        raise last

    def _snapshot_from(self, base: str, max_listings: int, max_bytes: int) -> tuple[list[dict], bool]:
        out: list[dict] = []
        seen: set[bytes] = set()
        after, total_bytes = 0, 0
        # Pages hold up to 500 listings; a node sending tiny or empty pages
        # still ends within this many requests.
        for _page in range(max_listings // 10 + 10):
            # Each page may use only what's left of the size bound, so a node
            # sending huge pages can't make the download larger than it.
            try:
                raw = self._request(
                    base, "GET", f"/v1/directory/snapshot?after={after}", None, limit=max_bytes - total_bytes
                )
            except MetaTooLarge:
                return out, False
            total_bytes += len(raw)
            page = mp.DirectorySnapshot()
            try:
                page.ParseFromString(raw)
            except DecodeError as e:
                raise MetaError(_("the maintainer node sent a damaged directory")) from e
            for sl in page.listings:
                try:
                    lst = verify_listing(sl)
                except keys.BadSignature, Exception:
                    log.warning("dropping a snapshot listing that doesn't verify")
                    continue
                if lst.withdrawn or lst.community in seen:
                    continue
                seen.add(lst.community)
                out.append(listing_dict(lst))
                if len(out) >= max_listings:
                    return out, False
            if not page.more:
                return out, True
            if total_bytes >= max_bytes or page.next <= after:
                # Over the size bound, or a node that doesn't advance.
                return out, False
            after = page.next
        return out, False

    async def route(self, route_id: bytes, community: bytes | None) -> tuple[mp.Route, sp.ServerCert, OwnerSet] | None:
        try:
            # A route that doesn't verify for this community (a node serving
            # someone else's route under its id) sends us to the next node.
            sr = await self._first(
                "GET",
                f"/v1/routes/{keys.encode(route_id)}",
                out=mp.SignedRoute(),
                check=lambda sr: verify_route(sr, community),
            )
        except MetaError as e:
            if e.status == 404:
                return None
            raise
        return verify_route(sr, community)

    async def status(self, route_id: bytes, server_key: bytes | None) -> dict | None:
        """Whether a community's server is reporting (docs/protocol.md §7.4),
        from the first maintainer that knows it; None if none does."""
        try:
            st = await self._first("GET", f"/v1/communities/{keys.encode(route_id)}/status", out=mp.CommunityStatus())
        except MetaError as e:
            if e.status == 404:
                return None
            raise
        return status_dict(st, server_key)

    async def relays(self) -> list[dict]:
        rl = await self._first("GET", "/v1/relays", out=mp.RelayList())
        return [
            {
                "url": r.url,
                "region": r.region,
                "capacity": r.capacity,
                "connections": r.connections,
                "last_seen": r.last_seen,
            }
            for r in rl.relays
        ]

    # ---------------------------------------------------------------- mailboxes

    async def drop(self, inbox: bytes, blob: bytes) -> int:
        return await self._all("/v1/mailbox/drop", mp.MailboxDrop(inbox=inbox, blob=blob).SerializeToString())

    async def fetch(self, token: bytes, after_id: int = 0) -> list[tuple[str, mp.MailboxItem]]:
        """Fetch from every maintainer; items are tagged with their node."""
        out = []
        for base in self.urls:
            try:
                out.extend((base, it) for it in await self.fetch_from(base, token, after_id))
            except MetaError:
                continue
        return out

    async def fetch_from(self, base: str, token: bytes, after_id: int = 0) -> list[mp.MailboxItem]:
        """One page (oldest first) of one node's items after `after_id`."""
        return list((await self.fetch_page(base, token, after_id)).items)

    async def fetch_page(self, base: str, token: bytes, after_id: int = 0) -> mp.MailboxItems:
        """One page of one node's items after `after_id`, with the node's
        mailbox epoch (it changes when the node is restored from a backup,
        and read positions then start over; docs/protocol.md §7.1)."""
        body = mp.MailboxFetch(token=token, after_id=after_id).SerializeToString()
        raw = await asyncio.to_thread(self._request, base, "POST", "/v1/mailbox/fetch", body)
        return _reply(mp.MailboxItems(), raw)

    async def delete(self, base: str, token: bytes, ids: list[int], gids: list[bytes] | None = None) -> None:
        """Delete items from one node: by its own ids, and by global id for
        items read on another member of its cluster (§7.5)."""
        body = mp.MailboxDelete(token=token, ids=ids, gids=gids or []).SerializeToString()
        try:
            await asyncio.to_thread(self._request, base, "POST", "/v1/mailbox/delete", body)
        except MetaError:
            pass

    # ---------------------------------------------------------------- DM blob store

    def _blob_upload_one(self, base: str, data: bytes, digest: bytes, token_hashes: list[bytes]) -> None:
        """Create or resume the upload on one node and send what it lacks."""
        create = mp.BlobCreate(hash=digest, size=len(data), token_hashes=token_hashes)
        st = _reply(mp.BlobUploadStatus(), self._request(base, "POST", "/v1/blobs/create", create.SerializeToString()))
        size = st.chunk_size
        if not st.complete and (size <= 0 or len(st.upload) != 32):
            raise MetaError(_("the maintainer node sent a damaged reply"))
        upload = bytes(st.upload)
        for i in list(st.missing):
            chunk = mp.BlobChunk(hash=digest, upload=upload, index=i, data=data[i * size : (i + 1) * size])
            st = _reply(
                mp.BlobUploadStatus(), self._request(base, "POST", "/v1/blobs/chunk", chunk.SerializeToString())
            )
        if not st.complete:
            raise MetaError(_("the maintainer node didn't finish the upload"))

    async def blob_upload_to(
        self, data: bytes, tokens: list[bytes], copies: int = BLOB_COPIES
    ) -> tuple[bytes, list[str]]:
        """Upload ciphertext to up to `copies` maintainers, in order. Returns
        the blob hash and the nodes that hold it. `tokens` are the random
        32-byte fetch tokens, one per recipient device."""
        if not data:
            raise ValueError("nothing to upload")
        if not 1 <= len(tokens) <= MAX_BLOB_TOKENS or any(len(t) != 32 for t in tokens):
            raise ValueError(f"need 1 to {MAX_BLOB_TOKENS} fetch tokens of 32 bytes")
        if len(set(tokens)) != len(tokens):
            raise ValueError("fetch tokens must be distinct")
        digest = hashlib.sha256(data).digest()
        token_hashes = [blob_token_hash(t) for t in tokens]
        stored: list[str] = []
        last: MetaError | None = None
        for base in self.health.order(list(self.urls)):
            if len(stored) >= copies:
                break
            try:
                await asyncio.to_thread(self._blob_upload_one, base, data, digest, token_hashes)
            except MetaError as e:
                last = e
                continue
            stored.append(base)
        if not stored:
            raise last or MetaError(_("no maintainer nodes are configured"))
        return digest, self.blob_nodes(stored)

    def blob_nodes(self, nodes: list[str]) -> list[str]:
        """Where a blob stored on `nodes` can be fetched: those nodes, then
        the other members of their maintainer clusters (they hold its record
        and fetch its content from each other; §7.5). At most 8."""
        out = list(dict.fromkeys(nodes))
        for n in nodes:
            out.extend(u for u in self.cluster_of(n) if u not in out)
        return out[:8]

    async def blob_upload(self, data: bytes, tokens: list[bytes]) -> bytes:
        """Upload ciphertext (to two maintainers when available); returns its hash."""
        digest, _stored = await self.blob_upload_to(data, tokens)
        return digest

    def _blob_download_one(self, base: str, digest: bytes, token: bytes, size: int | None) -> bytes:
        parts: list[bytes] = []
        got = 0
        index = 0
        while True:
            body = mp.BlobFetch(hash=digest, token=token, index=index).SerializeToString()
            d = _reply(mp.BlobChunkData(), self._request(base, "POST", "/v1/blobs/fetch", body))
            # The size is known from the (encrypted) message: a node can't make
            # this device download more than that.
            if size is not None and d.size != size:
                raise MetaError(_("the maintainer node reports a different size for this file"))
            if d.index != index or not d.data or got + len(d.data) > d.size or d.size > MAX_BLOB_SIZE:
                raise MetaError(_("the maintainer node sent damaged data"))
            parts.append(d.data)
            got += len(d.data)
            if got == d.size:
                break
            index += 1
        data = b"".join(parts)
        if hashlib.sha256(data).digest() != digest:
            raise MetaError(_("the downloaded file is damaged"))
        return data

    async def blob_download(
        self, digest: bytes, token: bytes, urls: list[str] | None = None, size: int | None = None
    ) -> bytes:
        """Download and hash-check a blob from the first node that has it
        (`size`: the ciphertext size the sender stated, if known). Raises
        MetaError with status 404 when no node has it (expired)."""
        last: MetaError | None = None
        for base in urls or self.urls:
            try:
                return await asyncio.to_thread(self._blob_download_one, base, digest, token, size)
            except MetaError as e:
                last = e
        if last is None:
            raise MetaError(_("no maintainer nodes are configured"))
        raise last

    async def blob_ack(self, digest: bytes, token: bytes, urls: list[str] | None = None) -> int:
        """Release this device's fetch token on every node, once the file is
        saved. Returns how many nodes answered."""
        body = mp.BlobAck(hash=digest, token=token).SerializeToString()
        results = await asyncio.gather(
            *(asyncio.to_thread(self._request, b, "POST", "/v1/blobs/ack", body) for b in urls or self.urls),
            return_exceptions=True,
        )
        return sum(1 for r in results if not isinstance(r, BaseException))

    # ---------------------------------------------------------------- nameplates

    def nameplate_order(self, number: int, index_of) -> list[str]:
        """Where to claim link code `number`: in a maintainer cluster each
        member hands out numbers ending in its position in the set, so that
        member first (index_of(url) gives a node's position, or None)."""
        first = [u for u in self.urls if index_of(u) == number % 10]
        return first + [u for u in self.urls if u not in first]

    async def node_info(self, base: str) -> mp.NodeInfo:
        """A node's identity and whether it serves the directory (GET
        /v1/node), noted for directory_urls."""
        raw = await asyncio.to_thread(self._request, base, "GET", "/v1/node", None, 64 << 10)
        info = _reply(mp.NodeInfo(), raw)
        self.directory[base] = info.directory
        return info

    async def fetch_set(self, base: str, operator: bytes | None) -> tuple[mp.MaintainerSet, bytes]:
        """A node's maintainer set (GET /v1/maintainers), verified for the
        pinned operator key (or any, for trust on first use)."""
        path = "/v1/maintainers" + (f"?operator={keys.encode(operator)}" if operator else "")
        raw = await asyncio.to_thread(self._request, base, "GET", path, None, 64 << 10)
        try:
            return verify_set(raw, operator), raw
        except ValueError as e:
            raise MetaError(_("the node's network list couldn't be verified")) from e

    async def nameplate(self, relay: str) -> mp.Nameplate:
        return await self._first(
            "POST", "/v1/nameplates", mp.NameplateRequest(relay=relay).SerializeToString(), out=mp.Nameplate()
        )

    async def claim_nameplate(self, number: int) -> mp.Nameplate:
        return await self._first(
            "POST", "/v1/nameplates/claim", mp.NameplateClaim(number=number).SerializeToString(), out=mp.Nameplate()
        )
