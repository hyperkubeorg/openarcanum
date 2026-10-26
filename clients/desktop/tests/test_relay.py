"""Python relay client against the real Go relay."""

import asyncio
import os

import pytest
from conftest import run

from openarcanum import relay as relay_mod
from openarcanum.keys import SigningKey
from openarcanum.relay import Relay, RelayError
from openarcanum.wire import relay_pb2 as rp


def test_publish_subscribe(relay_url):
    async def main():
        sub = await Relay(relay_url).connect()
        pub = await Relay(relay_url).connect()
        got: asyncio.Queue = asyncio.Queue()
        await sub.subscribe(b"t1", lambda topic, data: got.put_nowait((topic, data)))
        assert sub.hello.max_message <= relay_mod.MAX_MESSAGE
        big = os.urandom(3 * 1024 * 1024 + 17)  # several 1 MiB chunks
        await pub.publish(b"t1", b"hello")
        await pub.publish(b"t1", big)
        assert await asyncio.wait_for(got.get(), 10) == (b"t1", b"hello")
        assert (await asyncio.wait_for(got.get(), 10))[1] == big
        await sub.unsubscribe(b"t1")
        await pub.publish(b"t1", b"after")
        with pytest.raises(asyncio.TimeoutError):
            await asyncio.wait_for(got.get(), 0.5)
        await sub.close()
        await pub.close()

    run(main())


def test_chunks_reassemble_on_the_first_chunks_topic_within_bounds(monkeypatch):
    """The chunks of one message (sender, message id) belong to the topic
    its first chunk named: a chunk on another topic drops the message, which
    stays dropped, so a final chunk can't deliver it to a topic it wasn't
    published on. Partly received messages are bounded: per sender, in all
    and in bytes, the oldest going first."""
    monkeypatch.setattr(relay_mod, "PARTIAL_BYTES", 10)

    async def main():
        r = Relay("wss://relay.example.invalid")
        r.hello = rp.Hello(max_chunk=1 << 20, max_message=25 << 20)
        got: list[tuple[bytes, bytes]] = []
        r._handlers[b"a"] = lambda topic, data: got.append((topic, data))
        r._handlers[b"b"] = lambda topic, data: got.append((topic, data))

        async def chunk(topic, sender, mid, seq, data, final=False):
            await r._deliver(rp.Deliver(topic=topic, sender=sender, message_id=mid, seq=seq, data=data, final=final))

        await chunk(b"a", 1, 1, 0, b"x")
        await chunk(b"b", 1, 1, 1, b"y")  # another topic: dropped
        await chunk(b"b", 1, 1, 2, b"z", final=True)  # and stays dropped
        await chunk(b"a", 1, 1, 2, b"z", final=True)
        assert got == [] and r._partial == {} and r._partial_by == {} and r._partial_bytes == 0
        await chunk(b"a", 1, 2, 0, b"x")
        await chunk(b"a", 1, 2, 1, b"y", final=True)
        assert got == [(b"a", b"xy")]
        # Bytes: a chunk that doesn't fit beside older partial messages
        # pushes the oldest out; one that can't fit alone is dropped itself.
        await chunk(b"a", 1, 3, 0, b"123456")
        await chunk(b"a", 1, 4, 0, b"123")
        await chunk(b"a", 1, 4, 1, b"456")
        assert set(r._partial) == {(1, 4)} and r._partial_bytes == 6
        await chunk(b"a", 1, 3, 1, b"78", final=True)  # the dropped one's final chunk: nothing
        await chunk(b"a", 1, 4, 2, b"7890", final=True)
        assert got[-1] == (b"a", b"1234567890") and r._partial == {}
        await chunk(b"a", 1, 5, 0, b"x" * 11)
        assert r._partial == {} and r._partial_bytes == 0
        # Count: per sender, then in all (the oldest message goes).
        monkeypatch.setattr(relay_mod, "PARTIAL_BYTES", 1 << 20)
        for i in range(relay_mod.MAX_PARTIALS_BY_SENDER + 1):
            await chunk(b"a", 2, i, 0, b"p")
        assert len(r._partial) == relay_mod.MAX_PARTIALS_BY_SENDER and (2, 64) not in r._partial
        await chunk(b"a", 2, 64, 1, b"q", final=True)
        assert got[-1] != (b"a", b"pq")
        senders = range(3, 2 + relay_mod.MAX_PARTIALS // relay_mod.MAX_PARTIALS_BY_SENDER)
        for s in senders:
            for i in range(relay_mod.MAX_PARTIALS_BY_SENDER):
                await chunk(b"a", s, i, 0, b"p")
        assert len(r._partial) == relay_mod.MAX_PARTIALS and (2, 0) in r._partial
        await chunk(b"a", 99, 0, 0, b"p")
        assert len(r._partial) == relay_mod.MAX_PARTIALS and (2, 0) not in r._partial and (2, 1) in r._partial
        assert r._partial_by[2] == relay_mod.MAX_PARTIALS_BY_SENDER - 1
        await chunk(b"a", 2, 1, 1, b"q", final=True)
        assert got[-1] == (b"a", b"pq") and len(r._partial) == relay_mod.MAX_PARTIALS - 1

    run(main())


def test_size_cap(relay_url):
    async def main():
        pub = await Relay(relay_url).connect()
        with pytest.raises(RelayError):
            await pub.publish(b"t", b"x" * (25 * 1024 * 1024 + 1))
        await pub.close()

    run(main())


def test_endpoint_stream(relay_url):
    async def main():
        key = SigningKey.generate()
        ep = await Relay(relay_url).connect()
        await ep.register_endpoint(key)

        async def echo():
            s = await ep.accept()
            while True:
                m = await s.recv()
                await s.send(b"echo:" + m)

        task = asyncio.create_task(echo())
        cl = await Relay(relay_url).connect()
        s = await cl.open_stream(key.public)
        await s.send(b"hi")
        assert await s.recv(5) == b"echo:hi"
        with pytest.raises(RelayError):
            await cl.open_stream(SigningKey.generate().public)
        task.cancel()
        await cl.close()
        await ep.close()

    run(main())


def _fake_relay(hello_of):
    """A relay that sends hello_of(its own url) and acknowledges every
    request, recording what clients sign; returns (server, url, frames)."""
    from websockets.asyncio.server import serve

    from openarcanum.wire import relay_pb2 as rp

    frames: list = []
    box: dict = {}

    async def handler(ws):
        await ws.send(rp.RelayFrame(hello=hello_of(box["url"])).SerializeToString())
        async for raw in ws:
            f = rp.RelayFrame()
            f.ParseFromString(raw)
            kind = f.WhichOneof("kind")
            req = getattr(f, kind).req
            if kind != "start":
                frames.append(f)
            await ws.send(rp.RelayFrame(ack=rp.Ack(req=req)).SerializeToString())

    async def start():
        server = await serve(handler, "127.0.0.1", 0)
        port = server.sockets[0].getsockname()[1]
        box["url"] = f"ws://127.0.0.1:{port}/ws"
        return server, box["url"], frames

    return start()


def test_endpoint_registration_bound_to_relay_identity():
    """docs/protocol.md §2 "Relay identity": a relay that copies
    another relay's Hello gets no registration signed, and one that proves
    its own key gets one the real relay refuses."""
    from conftest import run_relay

    from openarcanum.relay import binding, verify_hello
    from openarcanum.wire import relay_pb2 as rp

    async def main(real_url):
        key = SigningKey.generate()
        real = await Relay(real_url).connect()
        assert real.hello.url == real_url
        verify_hello(real.hello)
        await real.register_endpoint(key)

        # Dialled at another name for the same relay: refused before signing.
        alias = await Relay(real_url.replace("127.0.0.1", "localhost")).connect()
        with pytest.raises(RelayError, match="own URL"):
            await alias.register_endpoint(key)
        await alias.close()

        # A fake relay sending the real relay's Hello as its own.
        server, fake_url, frames = await _fake_relay(lambda _url: real.hello)
        victim = await Relay(fake_url).connect()
        with pytest.raises(RelayError, match="own URL"):
            await victim.register_endpoint(key)
        assert frames == []
        await victim.close()
        server.close()

        # A fake relay proving its own key over the real relay's nonce: the
        # registration it gets is for itself, and the real relay refuses it.
        own = SigningKey.generate()

        def own_hello(url):
            h = rp.Hello()
            h.CopyFrom(real.hello)
            h.relay_key, h.url = own.public, url
            h.relay_signature = own.sign("openarcanum/relay-hello/v1", binding(own.public, url, bytes(h.nonce)))
            return h

        server, fake_url, frames = await _fake_relay(own_hello)
        victim = await Relay(fake_url).connect()
        await victim.register_endpoint(key)
        assert len(frames) == 1
        reg = frames[0].register_endpoint
        await victim.close()
        server.close()
        attacker = await Relay(real_url).connect()
        reg.req = 77
        with pytest.raises(RelayError, match=r"signature|another"):
            await attacker._request(rp.RelayFrame(register_endpoint=reg), 77)
        await attacker.close()

        # A Hello without a valid proof is refused.
        def tampered(url):
            h = rp.Hello()
            h.CopyFrom(real.hello)
            h.url = url
            return h

        server, fake_url, _ = await _fake_relay(tampered)
        with pytest.raises(RelayError, match="proof"):
            await Relay(fake_url).connect()
        server.close()
        await real.close()

    with run_relay(public_url="{url}") as url:
        run(main(url))
