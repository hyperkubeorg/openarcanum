"""Protocol versions, the relay's proof-of-work, its chunk cache, and the
directory snapshot (docs/protocol.md §1, §2, §7.2), against the real Go
binaries where they are involved."""

import asyncio
import contextlib
import hashlib
import http.server
import threading
import time

import pytest
from conftest import CommunityNode, new_core, run, run_meta, run_relay, wait_until
from test_e2e_community import WIZARD
from websockets.asyncio.server import serve

from openarcanum import directory, meta, pow, version
from openarcanum.core import UserError
from openarcanum.keys import CTX_RELAY_HELLO, SigningKey, StaticKey
from openarcanum.relay import Relay, RelayError, RelayRefused, RelayVersionError, binding
from openarcanum.session import SessionVersionError, accept, dial, sign_static
from openarcanum.wire import relay_pb2 as rp

# ---------------------------------------------------------------- negotiation


def test_policy_and_negotiation():
    assert version.SUPPORTED == [1]
    assert version.supported_for(2) == [1, 2]
    assert version.negotiate([1, 2], [1]) == 1  # a v2 server serves a v1 client
    assert version.negotiate([1], [1, 2]) == 1
    assert version.negotiate([1, 2], [2, 3]) == 2
    with pytest.raises(version.VersionMismatch) as e:
        version.negotiate([1, 2], [0])
    assert e.value.peer_outdated and "out of date" in str(e.value)
    with pytest.raises(version.VersionMismatch) as e:
        version.negotiate([1], [2, 3])
    assert not e.value.peer_outdated and str(e.value).startswith("Update OpenArcanum")
    with pytest.raises(version.VersionMismatch):
        version.negotiate([1], [1] * 17)  # over-long lists are refused
    assert version.parse(version.fmt([1, 2])) == [1, 2]
    assert version.parse("x") == [] and version.parse("1,-2") == [] and version.parse("99999999999") == []


class MemTransport:
    """An in-memory relay stream pair for handshakes."""

    def __init__(self):
        self.inbox: asyncio.Queue = asyncio.Queue()
        self.peer: MemTransport | None = None

    async def send(self, data):
        assert self.peer is not None
        self.peer.inbox.put_nowait(bytes(data))

    async def recv(self, timeout=None):
        return await asyncio.wait_for(self.inbox.get(), timeout)

    async def close(self, reason="closed"):
        pass


def _pair():
    a, b = MemTransport(), MemTransport()
    a.peer, b.peer = b, a
    return a, b


def _handshake(client_versions, server_versions):
    from openarcanum.keys import SigningKey

    async def main():
        a, b = _pair()
        sid, cid = SigningKey.generate(), SigningKey.generate()
        sst, cst = StaticKey.generate(), StaticKey.generate()
        srv = asyncio.create_task(accept(b, sst, sid, versions=server_versions, timeout=5))
        try:
            cs = await dial(
                a,
                cst,
                cid.public,
                sign_static(cid, cst.public),
                "d",
                lambda _si: None,
                timeout=5,
                versions=client_versions,
            )
        except Exception as e:
            with contextlib.suppress(Exception):
                await srv
            return e, None
        ss = await srv
        return cs, ss

    return run(main())


def test_session_versions():
    cs, ss = _handshake([1], [1, 2])
    assert cs.version == ss.version == 1
    cs, ss = _handshake([1, 2], [1, 2])
    assert cs.version == 2
    err, _ = _handshake([1], [2, 3])
    assert isinstance(err, SessionVersionError) and "Update OpenArcanum" in str(err)
    err, _ = _handshake([2, 3], [1])
    assert isinstance(err, SessionVersionError) and "out of date" in str(err)


# ---------------------------------------------------------------- relay versions against a stand-in relay


@contextlib.asynccontextmanager
async def fake_relay(versions, pow_bits=0, reply=None):
    """A relay stand-in that sends Hello with the given versions and answers
    Start with `reply` (default: Ack)."""
    starts = []

    # It proves a relay key, as every relay does (docs/protocol.md §2
    # "Relay identity").
    relay_key = SigningKey.generate()
    nonce = b"n" * 32

    async def handler(ws):
        hello = rp.RelayFrame(
            hello=rp.Hello(
                nonce=nonce,
                max_chunk=1024,
                max_message=4096,
                versions=versions,
                pow_bits=pow_bits,
                relay_key=relay_key.public,
                relay_signature=relay_key.sign(CTX_RELAY_HELLO, binding(relay_key.public, "", nonce)),
            )
        )
        await ws.send(hello.SerializeToString())
        with contextlib.suppress(Exception):
            raw = await ws.recv()
            f = rp.RelayFrame()
            f.ParseFromString(raw)
            starts.append(f.start)
            r = reply or rp.RelayFrame(ack=rp.Ack(req=f.start.req))
            await ws.send(r.SerializeToString())
            await ws.wait_closed()

    async with serve(handler, "127.0.0.1", 0) as server:
        port = server.sockets[0].getsockname()[1]
        yield f"ws://127.0.0.1:{port}/ws", starts


def test_relay_version_errors_say_who_is_outdated():
    async def main():
        async with fake_relay([2, 3]) as (url, starts):
            with pytest.raises(RelayVersionError) as e:
                await Relay(url).connect(timeout=5)
            assert str(e.value).startswith("Update OpenArcanum") and not starts
        async with fake_relay([]) as (url, starts):
            with pytest.raises(RelayVersionError) as e:
                await Relay(url).connect(timeout=5)
            assert "relay is out of date" in str(e.value)
        # A relay one version ahead serves this client at version 1.
        async with fake_relay([1, 2]) as (url, starts):
            r = await Relay(url).connect(timeout=5)
            assert r.version == 1 and starts[0].version == 1
            await r.close()
        # A refusal of Start is reported, not swallowed.
        refuse = rp.RelayFrame(
            error=rp.Error(req=1, code=rp.ERROR_CODE_POW_REQUIRED, message="proof-of-work missing or wrong")
        )
        async with fake_relay([1], reply=refuse) as (url, _):
            with pytest.raises(RelayRefused, match="proof-of-work"):
                await Relay(url).connect(timeout=5)

    run(main())


# ---------------------------------------------------------------- proof-of-work


def test_pow_vectors_match_go():
    challenge = bytes(range(32))
    d = pow.digest(challenge, (42).to_bytes(8, "big"))
    assert d.hex() == "bc3b5acf788a7e5f93eb19cd9fc853a43500432f875027b4fef417087335645a"
    assert pow.solve(challenge, 12) == (5386).to_bytes(8, "big")
    assert pow.verify(challenge, (5386).to_bytes(8, "big"), 12)
    assert pow.leading_zero_bits(b"\x00\x40") == 9
    assert not pow.verify(challenge, b"", 1) and pow.verify(challenge, b"", 0)
    with pytest.raises(pow.PowError):
        pow.solve(challenge, pow.MAX_BITS + 1)
    with pytest.raises(pow.PowError):
        pow.solve(challenge, pow.MAX_BITS, deadline=time.monotonic())
    cancel = threading.Event()
    cancel.set()
    with pytest.raises(pow.PowError):
        pow.solve(challenge, pow.MAX_BITS, cancel=cancel)


def test_pow_solved_off_the_event_loop():
    """A relay under load (pow_mode always here) asks for proof-of-work; the
    client solves it in a worker thread, so the event loop (and with it the
    UI bridge) keeps running meanwhile."""
    with run_relay(pow_mode="always", pow_bits=18) as url:

        async def main():
            ticks = 0
            stop = asyncio.Event()

            async def ticker():
                nonlocal ticks
                while not stop.is_set():
                    ticks += 1
                    await asyncio.sleep(0.01)

            t = asyncio.create_task(ticker())
            started = time.monotonic()
            a = await Relay(url).connect(timeout=30)
            elapsed = time.monotonic() - started
            stop.set()
            await t
            assert a.hello.pow_bits == 18
            # The loop ticked through the solve (at least a third of the
            # 10 ms ticks it could have).
            assert ticks >= max(1, int(elapsed / 0.01) // 3), (ticks, elapsed)
            b = await Relay(url).connect(timeout=30)
            got: asyncio.Queue = asyncio.Queue()
            await b.subscribe(b"t", lambda _t, d: got.put_nowait(d))
            await a.publish(b"t", b"after proof-of-work")
            assert await asyncio.wait_for(got.get(), 5) == b"after proof-of-work"
            await a.close()
            await b.close()

        run(main())


# ---------------------------------------------------------------- chunk cache


def test_chunk_cache_replay_with_real_relay():
    with run_relay(chunk_cache_bytes=8 << 20) as url:

        async def main():
            pub = await Relay(url).connect()
            assert pub.cache_ttl_ms == 60_000
            await pub.publish(b"topic", b"ciphertext-1", cacheable=True)
            await pub.publish(b"topic", b"not cacheable")
            await asyncio.sleep(0.2)
            sub = await Relay(url).connect()
            live: asyncio.Queue = asyncio.Queue()
            old: asyncio.Queue = asyncio.Queue()
            await sub.subscribe(
                b"topic",
                lambda _t, d: live.put_nowait(d),
                replay_ms=sub.cache_ttl_ms,
                on_replay=lambda _t, d: old.put_nowait(d),
            )
            assert await asyncio.wait_for(old.get(), 5) == b"ciphertext-1"
            await asyncio.sleep(0.2)
            assert old.empty() and live.empty()
            await pub.publish(b"topic", b"live")
            assert await asyncio.wait_for(live.get(), 5) == b"live"
            await pub.close()
            await sub.close()

        run(main())


def test_no_chunk_cache_by_default(relay_url):
    async def main():
        r = await Relay(relay_url).connect()
        assert r.cache_ttl_ms == 0
        await r.close()

    run(main())


# ---------------------------------------------------------------- maintainer API versions


class _OldNode(http.server.BaseHTTPRequestHandler):
    supported = "2,3"

    def do_GET(self):
        self.send_response(426)
        self.send_header(version.HEADER, self.supported)
        self.end_headers()
        self.wfile.write(b"this maintainer node speaks protocol 2,3; update OpenArcanum")

    def log_message(self, *a):
        pass


@contextlib.contextmanager
def http_node(supported):
    handler = type("H", (_OldNode,), {"supported": supported})
    srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), handler)
    th = threading.Thread(target=srv.serve_forever, daemon=True)
    th.start()
    try:
        yield f"http://127.0.0.1:{srv.server_address[1]}"
    finally:
        srv.shutdown()


def test_maintainer_version_errors():
    async def main():
        with http_node("2,3") as url:
            with pytest.raises(meta.MetaVersionError) as e:
                await meta.MetaClient([url]).search("x")
            assert str(e.value).startswith("Update OpenArcanum")
        with http_node("0") as url:
            with pytest.raises(meta.MetaVersionError) as e:
                await meta.MetaClient([url]).search("x")
            assert "maintainer node is out of date" in str(e.value)

    run(main())


def test_real_maintainer_negotiates(tmp_path):
    with run_meta() as url:

        async def main():
            # This build's client talks to this build's node.
            assert await meta.MetaClient([url]).search("") == []

        run(main())
    # And the node refuses a client that announces nothing.
    import urllib.error
    import urllib.request

    with run_meta() as url:
        with pytest.raises(urllib.error.HTTPError) as e:
            urllib.request.urlopen(url + "/v1/search", timeout=5)
        assert e.value.code == 426 and e.value.headers[version.HEADER] == "1"


# ---------------------------------------------------------------- directory snapshot and local search


def test_local_search_matching():
    ls = [
        {"name": "Sourdough Bakers", "description": "wild yeast", "tags": ["bread"], "published_at": 2},
        {"name": "Rust", "description": "about baking bread in rust ovens", "tags": ["metal"], "published_at": 3},
        {"name": "Knitting", "description": "", "tags": ["wool"], "published_at": 1},
    ]
    assert [x["name"] for x in directory.search(ls, "bak")] == ["Sourdough Bakers", "Rust"]  # name hits first
    assert [x["name"] for x in directory.search(ls, "bread", tag="metal")] == ["Rust"]
    assert directory.search(ls, "bread yeast")[0]["name"] == "Sourdough Bakers"
    assert directory.search(ls, "nothing") == []
    assert [x["name"] for x in directory.search(ls, "")] == ["Rust", "Sourdough Bakers", "Knitting"]


def test_snapshot_bounds_and_verification(tmp_path):
    """A node whose snapshot never ends, or is huge, can't make the client
    download without bound; listings that don't verify are dropped."""
    from openarcanum.wire import meta_pb2 as mp

    junk = mp.SignedListing(listing=b"\x22\x04name", signature=b"x" * 64)
    page = mp.DirectorySnapshot(listings=[junk] * 3, more=True, next=1, total=10**9)

    class Endless(http.server.BaseHTTPRequestHandler):
        n = 0

        def do_GET(self):
            type(self).n += 1
            page.next = type(self).n
            body = page.SerializeToString()
            self.send_response(200)
            self.send_header(version.HEADER, "1")
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *a):
            pass

    srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Endless)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    try:
        listings, complete = run(
            meta.MetaClient([f"http://127.0.0.1:{srv.server_address[1]}"]).snapshot(max_listings=100)
        )
        assert listings == [] and not complete
        assert Endless.n <= 100 // 10 + 10
    finally:
        srv.shutdown()


def test_snapshot_page_reads_only_what_the_bound_allows(monkeypatch):
    """One huge page can't take the download past max_bytes: each page may
    use only what's left of the bound, and the client stops reading a reply
    once it is over (it doesn't read a whole 64 MiB page first)."""
    from openarcanum.wire import meta_pb2 as mp

    junk = mp.SignedListing(listing=b"\x22\x04name", signature=b"x" * 64)
    small = mp.DirectorySnapshot(listings=[junk], more=True, next=1).SerializeToString()
    huge = mp.DirectorySnapshot(listings=[junk] * 50_000, more=True, next=2).SerializeToString()
    assert len(huge) > 3 << 20

    class Pages(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            body = small if "after=0" in self.path else huge
            self.send_response(200)
            self.send_header(version.HEADER, "1")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            with contextlib.suppress(OSError):
                self.wfile.write(body)

        def log_message(self, *a):
            pass

    read = []
    real_urlopen = meta.urllib.request.urlopen

    def counting_urlopen(*a, **kw):
        resp = real_urlopen(*a, **kw)
        real_read = resp.read

        def read_counted(n=-1):
            data = real_read(n)
            read.append(len(data))
            return data

        resp.read = read_counted
        return resp

    monkeypatch.setattr(meta.urllib.request, "urlopen", counting_urlopen)
    srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Pages)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    try:
        bound = 1 << 20
        listings, complete = run(
            meta.MetaClient([f"http://127.0.0.1:{srv.server_address[1]}"]).snapshot(max_bytes=bound)
        )
        assert listings == [] and not complete
        assert sum(read) <= bound + 1, f"read {sum(read)} bytes for a {bound}-byte bound"
    finally:
        srv.shutdown()


def test_local_directory_search_end_to_end(tmp_path):
    """With "Search the directory on this device" on, search results come
    from the downloaded snapshot (verified listings, no icon), and the
    maintainer only sees snapshot downloads, never the query."""
    with (
        run_meta() as meta_url,
        run_relay(registry_urls=meta_url, heartbeat_interval="1s", public_url="{url}") as relay,
    ):
        node = CommunityNode(tmp_path / "c", [relay], [meta_url])
        node.start()
        try:

            async def main():
                owner, _ = await new_core(tmp_path / "owner", maintainers=[meta_url])
                wiz = dict(WIZARD, name="Sourdough Bakers", description="Wild yeast", tags="bread", visibility="public")
                await owner.claim(node.claim_code, wiz, "Olive")
                seeker, _ = await new_core(tmp_path / "seeker", maintainers=[meta_url])
                await wait_until(lambda: seeker.directory_search("sourdough"), timeout=20)
                await seeker.set_setting("directory_local", True)
                st = seeker.directory_status()
                assert st["local"] and st["age_seconds"] is None
                hits = await seeker.directory_search("sour")
                assert [h["name"] for h in hits] == ["Sourdough Bakers"] and hits[0]["icon"] == ""
                assert await seeker.directory_search("knitting") == []
                st = seeker.directory_status()
                assert st["listings"] == 1 and st["complete"] and st["age_seconds"] is not None
                # The copy is what's searched: a maintainer that's gone
                # doesn't matter until the copy is refreshed.
                seeker.meta.urls = ["http://127.0.0.1:9"]
                assert len(await seeker.directory_search("bakers")) == 1
                with pytest.raises(UserError, match="Downloading the directory failed"):
                    await seeker.directory_refresh()
                await seeker.set_setting("directory_local", False)
                assert seeker.directory_status() == {
                    "local": False,
                    "listings": 0,
                    "complete": True,
                    "age_seconds": None,
                }
                await owner.shutdown()
                await seeker.shutdown()

            run(main(), 90)
        finally:
            node.stop()


def test_listing_icon_bound_to_hash():
    from openarcanum.wire import meta_pb2 as mp

    lst = mp.Listing(icon_sha256=hashlib.sha256(b"png").digest())
    assert meta.listing_icon(mp.SignedListing(icon=b"png"), lst) == b"png"
    assert meta.listing_icon(mp.SignedListing(icon=b"swapped"), lst) == b""
    assert meta.listing_icon(mp.SignedListing(), lst) == b""


def test_relay_error_types_are_relay_errors():
    assert issubclass(RelayVersionError, RelayError) and issubclass(RelayRefused, RelayError)
