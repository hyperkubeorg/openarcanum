"""Clean shutdown: the app leaves by returning from main,
not os._exit, after saving everything; a terminate signal closes the window
the normal way; bridge calls still in flight don't keep the process alive."""

import asyncio
import time
from pathlib import Path

import pytest
from conftest import PASS, Events, freeze, network, run, thaw
from uidriver import App
from uiflows import claim, create_profile, send_message

from openarcanum.core import Core
from openarcanum.paths import Paths


def log_of(home: Path) -> str:
    return (home / "log" / "client.log").read_text(encoding="utf-8")


@pytest.mark.gui
def test_terminate_saves_everything_and_exits(tmp_path):
    with network(tmp_path) as (_meta, _relay, community):
        node = community()

        async def main():
            home = tmp_path / "olive"
            app = await App(home, name="olive").start()
            await create_profile(app)
            await claim(app, node.claim_code, "Shutdown", "Olive")
            await send_message(app, "saved at shutdown")
            await app.wait_text("#messages .mtext", "saved at shutdown")
            # Within the cache's one-second save delay: only shutdown saves it.
            # Closed as the system does (SIGTERM; the sign-out messages on Windows).
            pid = app.terminate()
            took = await asyncio.to_thread(app.wait_exit, pid, 20)
            await app.stop()
            log = log_of(home)
            assert "exiting with status 0" in log, log[-2000:]
            assert "Traceback" not in log, log[-2000:]
            # The message is in the cache on disk: open it as the next start
            # would, with the server stopped.
            node.stop()
            core = Core(Paths.under(home), emit=Events())
            await core.unlock(PASS)
            cid = next(iter(core.vault.data["communities"]))
            texts = [m.get("text") for msgs in core.cache(cid).messages.values() for m in msgs]
            await core.shutdown()
            assert "saved at shutdown" in texts, texts
            return took

        took = run(main(), 300)
        assert took < 10, took


@pytest.mark.gui
def test_exit_is_prompt_with_a_bridge_call_in_flight(tmp_path):
    """A bridge call waiting on a server that stopped answering doesn't keep
    the process alive: the core is shut down, the call returns "closing",
    and its pywebview thread ends."""
    with network(tmp_path) as (_meta, _relay, community):
        node = community()

        async def main():
            home = tmp_path / "olive"
            app = await App(home, name="olive").start()
            await create_profile(app)
            await claim(app, node.claim_code, "Shutdown", "Olive")
            # The server freezes (still connected): the next request waits.
            freeze(node.proc.p.pid)
            try:
                await app.js(
                    "window.__inflight = api('admin', st.cid, 'create_channel', {name: 'slow', kind: 'text'}); 0"
                )
                await asyncio.sleep(1.0)
                pid = app.terminate()
                took = await asyncio.to_thread(app.wait_exit, pid, 20)
            finally:
                thaw(node.proc.p.pid)
            await app.stop()
            assert "exiting with status 0" in log_of(home)
            return took

        took = run(main(), 300)
        assert took < 10, took


def test_a_stuck_or_failing_step_doesnt_stop_the_rest(tmp_path):
    """Core.shutdown: a call that never finishes leaving and a failing step
    don't keep the others from running inside the app's 5 s budget, and the
    caches are saved again after the connections stop."""
    from conftest import new_core

    async def main():
        core, _ = await new_core(tmp_path / "home")
        order: list[str] = []

        async def stuck():
            order.append("voice")
            await asyncio.Event().wait()

        async def broken():
            order.append("devices")
            raise RuntimeError("boom")

        real_dms = core.dms.shutdown

        async def dms():
            order.append("dms")
            await real_dms()

        class Conn:
            async def stop(self):
                order.append("conn")

        class Cache:
            def save(self):
                order.append("save")

        core.voice.shutdown = stuck
        core.devices.shutdown = broken
        core.dms.shutdown = dms
        core.conns = {"a": Conn(), "b": Conn()}
        core.caches = {"a": Cache()}
        start = time.monotonic()
        await core.shutdown()
        took = time.monotonic() - start
        assert order == ["save", "voice", "devices", "dms", "conn", "conn", "save"], order
        assert took < 3, took

    run(main(), 30)
