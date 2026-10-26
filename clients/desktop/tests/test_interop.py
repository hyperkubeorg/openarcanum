"""Go<->Python interop for identity signatures and Noise sessions."""

import asyncio
import json
import os
import subprocess

import pytest
from conftest import Proc, binary, run

from openarcanum import keys
from openarcanum.keys import StaticKey
from openarcanum.relay import Relay, StreamClosed
from openarcanum.session import IdentityError, SessionError, accept, dial, sign_static


def test_signature_interop():
    seed = os.urandom(32)
    msg = os.urandom(50)
    out = subprocess.run(
        [str(binary("interop")), "sign", "-seed", seed.hex(), "-ctx", keys.CTX_ROUTE, "-msg", msg.hex()],
        capture_output=True,
        text=True,
        check=True,
    )
    r = json.loads(out.stdout)
    k = keys.SigningKey(seed)
    assert bytes.fromhex(r["public"]) == k.public
    # Go's signature verifies in Python, and Python's equals Go's (Ed25519 is deterministic).
    keys.verify(k.public, keys.CTX_ROUTE, msg, bytes.fromhex(r["signature"]))
    assert k.sign(keys.CTX_ROUTE, msg).hex() == r["signature"]
    with pytest.raises(keys.BadSignature):
        keys.verify(k.public, keys.CTX_LISTING, msg, bytes.fromhex(r["signature"]))


def _identity():
    ident = keys.SigningKey.generate()
    static = StaticKey.generate()
    return ident, static, sign_static(ident, static.public)


def test_python_client_go_server(relay_url):
    server = Proc([str(binary("interop")), "serve", "-relay", relay_url])
    try:
        server_key = keys.decode(server.json_line()["server_key"])

        async def main():
            r = await Relay(relay_url).connect()
            ident, static, sig = _identity()
            stream = await r.open_stream(server_key)

            def check(si):
                assert si.server_key == server_key

            s = await dial(stream, static, ident.public, sig, "py-device", check)
            big = os.urandom(300_000)
            for m in (b"ping", big, b""):
                await s.send(m)
                assert await s.recv(10) == b"echo:" + m
            await r.close()
            return ident, static

        ident, static = run(main())
        ev = server.json_line()
        assert ev["event"] == "accepted"
        assert keys.decode(ev["identity"]) == ident.public
        assert keys.decode(ev["device"]) == static.public
        assert ev["name"] == "py-device"
    finally:
        server.stop()


def test_python_tampered_signature_rejected_by_go(relay_url):
    server = Proc([str(binary("interop")), "serve", "-relay", relay_url])
    try:
        server_key = keys.decode(server.json_line()["server_key"])

        async def main():
            r = await Relay(relay_url).connect()
            ident, static, sig = _identity()
            bad = bytes([sig[0] ^ 1]) + sig[1:]
            stream = await r.open_stream(server_key)
            s = await dial(stream, static, ident.public, bad, "py", lambda si: None)
            # The server drops the stream; nothing gets through.
            with pytest.raises((StreamClosed, SessionError)):
                await s.send(b"ping")
                await s.recv(5)
            await r.close()

        run(main())
        assert server.json_line() == {"event": "rejected", "reason": "identity"}
    finally:
        server.stop()


def test_python_rejects_unpinned_go_server(relay_url):
    server = Proc([str(binary("interop")), "serve", "-relay", relay_url])
    try:
        server_key = keys.decode(server.json_line()["server_key"])

        async def main():
            r = await Relay(relay_url).connect()
            ident, static, sig = _identity()
            stream = await r.open_stream(server_key)
            pinned = keys.SigningKey.generate().public

            def check(si):
                if si.server_key != pinned:
                    raise IdentityError("server key does not match the pinned key")

            with pytest.raises(IdentityError):
                await dial(stream, static, ident.public, sig, "py", check)
            await r.close()

        run(main())
    finally:
        server.stop()


def test_python_rejects_tampered_go_server_signature(relay_url):
    server = Proc([str(binary("interop")), "serve", "-relay", relay_url, "-tamper"])
    try:
        server_key = keys.decode(server.json_line()["server_key"])

        async def main():
            r = await Relay(relay_url).connect()
            ident, static, sig = _identity()
            stream = await r.open_stream(server_key)
            checked = []
            with pytest.raises(IdentityError):
                await dial(stream, static, ident.public, sig, "py", checked.append)
            # The proof is checked before the caller's pin check runs.
            assert checked == []
            await r.close()

        run(main())
    finally:
        server.stop()


def _python_server(relay_url, results: list):
    """Run a Python responder; returns (server_key, coroutine)."""
    ident = keys.SigningKey.generate()
    static = StaticKey.generate()

    async def serve(ready: asyncio.Event, sessions: int):
        r = await Relay(relay_url).connect()
        await r.register_endpoint(ident)
        ready.set()
        for _ in range(sessions):
            stream = await r.accept()
            try:
                s = await accept(stream, static, ident)
            except IdentityError:
                results.append("identity")
                await stream.close("handshake failed")
                continue
            results.append(s.client)
            for _ in range(2):
                m = await s.recv(10)
                await s.send(b"echo:" + m)
        await r.close()

    return ident.public, serve


def test_go_client_python_server(relay_url):
    results: list = []
    server_key, serve = _python_server(relay_url, results)

    async def main():
        ready = asyncio.Event()
        task = asyncio.create_task(serve(ready, 1))
        await ready.wait()
        proc = await asyncio.create_subprocess_exec(
            str(binary("interop")),
            "dial",
            "-relay",
            relay_url,
            "-server",
            keys.encode(server_key),
            stdout=asyncio.subprocess.PIPE,
        )
        out, _ = await proc.communicate()
        await task
        return proc.returncode, json.loads(out.decode().strip().splitlines()[-1])

    code, result = run(main())
    assert code == 0 and result["ok"] == "true"
    assert results[0].identity_key == keys.decode(result["identity"])
    assert results[0].device_name == "go-device"


def test_go_tampered_signature_rejected_by_python(relay_url):
    results: list = []
    server_key, serve = _python_server(relay_url, results)

    async def main():
        ready = asyncio.Event()
        task = asyncio.create_task(serve(ready, 1))
        await ready.wait()
        proc = await asyncio.create_subprocess_exec(
            str(binary("interop")),
            "dial",
            "-relay",
            relay_url,
            "-server",
            keys.encode(server_key),
            "-tamper",
            stdout=asyncio.subprocess.PIPE,
        )
        await proc.communicate()
        await task
        return proc.returncode

    assert run(main()) != 0
    assert results == ["identity"]
