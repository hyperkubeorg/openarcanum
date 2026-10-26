"""Python relay client against the real Go relay."""

import asyncio
import os

import pytest
from conftest import run

from openarcanum.keys import SigningKey
from openarcanum.relay import Relay, RelayError


def test_publish_subscribe(relay_url):
    async def main():
        sub = await Relay(relay_url).connect()
        pub = await Relay(relay_url).connect()
        got: asyncio.Queue = asyncio.Queue()
        await sub.subscribe(b"t1", lambda topic, data: got.put_nowait((topic, data)))
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
