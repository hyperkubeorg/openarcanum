"""Communities hosted on this computer (openarcanum/hosting.py), community status
on maintainer nodes, reaping, and automatic relay failover
(docs/protocol.md §7.4), end to end against the real relay, meta and
community binaries.

The hosted community servers are the real `community` program run by the
client's supervisor, so these tests also prove the Go-signed community
heartbeats verify in Python (meta.status_dict)."""

import asyncio
import os
import stat
import subprocess
import sys
import time

import pytest
from conftest import (
    CommunityNode,
    binary,
    kill_relay,
    network,
    new_core,
    run,
    run_meta,
    run_relay,
    wait_until,
)
from test_e2e_community import WIZARD, general, online

from openarcanum import hosting
from openarcanum.core import UserError

pytestmark = pytest.mark.timeout(300)


@pytest.fixture
def hosted_env(monkeypatch):
    """The client's supervisor runs the built server, reporting every second."""
    monkeypatch.setenv(hosting.BINARY_ENV, str(binary("community")))
    monkeypatch.setenv("OPENARCANUM_COMMUNITY_HEARTBEAT_INTERVAL", "1s")
    monkeypatch.setenv("OPENARCANUM_COMMUNITY_PUBLISH_INTERVAL", "2s")


def child_pids(core) -> list[int]:
    return [h.proc.pid for h in core.hosting.items.values() if h.proc is not None]


def alive(pid: int) -> bool:
    if sys.platform.startswith("linux"):
        try:
            with open(f"/proc/{pid}/status") as f:
                return "zombie" not in f.read().lower()
        except OSError:
            return False
    if os.name == "nt":
        # os.kill(pid, 0) would end the process here (TerminateProcess).
        import ctypes
        from ctypes import wintypes

        k32 = ctypes.WinDLL("kernel32", use_last_error=True)
        k32.OpenProcess.restype = wintypes.HANDLE
        k32.OpenProcess.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.DWORD]
        h = k32.OpenProcess(0x1000, False, pid)  # PROCESS_QUERY_LIMITED_INFORMATION
        if not h:
            return False
        try:
            code = wintypes.DWORD()
            k32.GetExitCodeProcess(h, ctypes.byref(code))
            return code.value == 259  # STILL_ACTIVE
        finally:
            k32.CloseHandle(h)
    try:
        os.kill(pid, 0)
    except OSError:
        return False
    return True


async def send_when_back(core, cid: str, ch: int, text: str, timeout: float = 60.0) -> None:
    """Send once the connection to the community is back (after its server
    restarted or moved)."""

    async def attempt():
        try:
            await core.send(cid, ch, text)
            return True
        except UserError:
            return False

    await wait_until(attempt, timeout, 0.5)


async def server_state(core, cid: str, want: str, timeout: float = 30.0) -> dict:
    """Wait until this client's view of the community's server says want."""

    async def check():
        conn = core.conns.get(cid)
        if conn is None:
            return None
        await conn._check_status(force=True)
        st = conn.server_status
        return st if st and st["state"] == want else None

    return await wait_until(check, timeout, 0.5)


def test_a_built_app_runs_only_its_bundled_server(tmp_path, monkeypatch):
    """Frozen (a built app), the community server comes only from inside the
    bundle: not the repository's bin/, not $OPENARCANUM_COMMUNITY_BIN."""
    elsewhere = tmp_path / "elsewhere" / hosting.binary_name()
    elsewhere.parent.mkdir()
    elsewhere.write_bytes(b"#!/bin/sh\n")
    bundle = tmp_path / "bundle"
    monkeypatch.setattr(sys, "frozen", True, raising=False)
    monkeypatch.setattr(sys, "_MEIPASS", str(bundle), raising=False)
    monkeypatch.setenv(hosting.BINARY_ENV, str(elsewhere))
    # Nothing in the bundle: nothing at all (the repository's bin/ may well exist here).
    assert hosting.community_binary() is None
    inside = bundle / "servers" / hosting.binary_name()
    inside.parent.mkdir(parents=True)
    inside.write_bytes(b"#!/bin/sh\n")
    assert hosting.community_binary() == inside
    # From source the override still works.
    monkeypatch.setattr(sys, "frozen", False)
    assert hosting.community_binary() == elsewhere


def test_host_on_this_computer_claims_serves_and_supervises(tmp_path, hosted_env):
    """Host on this computer: init into the profile's data folder, run the
    server, claim it with no code to copy; another member joins and chats;
    a crashed server is started again; Stop takes it offline (the
    maintainers say so at once), Start brings it back; quitting stops it."""
    with network(tmp_path, community_offline_after="10s") as (meta, _relay, _community):

        async def main():
            olive, _ev = await new_core(tmp_path / "olive", maintainers=[meta])
            r = await olive.host_community(dict(WIZARD, name="Home Hosted"), "Olive")
            cid = r["community"]
            assert r["invite"].startswith("openarcanum-invite:")
            assert r["hosted"]["state"] == "running"
            rec = olive.vault.data["communities"][cid]
            hid = rec["hosted"]
            folder = olive.paths.data / "hosted" / hid
            # The server's data in the profile's private data folder, 0700;
            # the claim code is gone once claimed.
            assert (folder / "server" / "server.key").is_file()
            if os.name == "posix":
                assert stat.S_IMODE((olive.paths.data / "hosted").stat().st_mode) == 0o700
                assert stat.S_IMODE((folder / "server").stat().st_mode) == 0o700
            await wait_until(lambda: not (folder / hosting.CLAIM_FILE).exists(), 15)
            listed = olive.hosting_list()
            assert [(x["id"], x["community"], x["name"], x["state"]) for x in listed] == [
                (hid, cid, "Home Hosted", "running")
            ]
            assert {loc["id"]: loc["path"] for loc in olive.storage_locations()}["hosted"] == str(
                olive.paths.data / "hosted"
            )
            assert olive.community_summary(cid)["hosted"]["state"] == "running"
            await general(olive, cid)

            # Another member joins through the maintainers' relay and chats.
            alice, _ = await new_core(tmp_path / "alice", maintainers=[meta])
            await alice.join({"invite": r["invite"]}, "new", "Alice")
            ch = await general(alice, cid)
            await alice.send(cid, ch, "hello from alice")
            await wait_until(lambda: any(m.get("text") == "hello from alice" for m in olive.cached_messages(cid, ch)))

            # The maintainers report it online; the heartbeat verifies here.
            st = await server_state(alice, cid, "online")
            assert st["state"] == "online" and st["relays_attached"] >= 1 and not st["pinned"]

            # The server crashes: the supervisor starts it again.
            [pid] = child_pids(olive)
            os.kill(pid, 9)
            await wait_until(lambda: olive.hosting.items[hid].restarts == 1, 20)
            await wait_until(lambda: olive.hosting.items[hid].state == "running" and child_pids(olive) != [pid], 20)
            await send_when_back(alice, cid, ch, "after the restart")
            await wait_until(lambda: "after the restart" in [m.get("text") for m in olive.cached_messages(cid, ch)], 40)

            # Stop: the server says it's going offline; members see it.
            await olive.hosting_stop(cid)
            assert child_pids(olive) == []
            assert olive.hosting_list()[0]["state"] == "stopped"
            off = await server_state(alice, cid, "offline")
            assert off["stopping"] and off["last_seen"] > 0
            await wait_until(lambda: alice.community_summary(cid)["server_status"] is not None, 30)
            # It stays stopped when the app starts again.
            assert '"enabled": false' in (folder / hosting.HOST_FILE).read_text()

            await olive.hosting_start(cid)
            await online(alice, cid)
            await server_state(alice, cid, "online")

            # Quitting stops the server cleanly (it says so to the maintainers).
            [pid] = child_pids(olive)
            await olive.shutdown()
            await wait_until(lambda: not alive(pid), 10)
            await server_state(alice, cid, "offline")
            await alice.shutdown()

        run(main(), 240)


def test_host_on_this_computer_needs_relays_and_maintainers(tmp_path, hosted_env):
    """Nothing is created when there's no maintainer node or no relay to
    host through, or when the form is incomplete."""
    with run_meta() as meta:

        async def main():
            core, _ = await new_core(tmp_path / "olive", maintainers=[meta])
            with pytest.raises(UserError, match="display name"):
                await core.host_community(dict(WIZARD), "")
            with pytest.raises(UserError, match="name"):
                await core.host_community(dict(WIZARD, name=""), "Olive")
            # This meta's registry is empty and no relay is preferred.
            with pytest.raises(UserError, match="No relay is available"):
                await core.host_community(dict(WIZARD), "Olive")
            assert not (core.paths.data / "hosted").exists() or not any((core.paths.data / "hosted").iterdir())
            await core.shutdown()

        run(main(), 60)


def test_failed_claim_leaves_nothing_behind(tmp_path, hosted_env, monkeypatch):
    """A preferred relay nobody runs: the server can't be reached for its
    claim, and the half-made hosted community is removed again."""
    monkeypatch.setattr("openarcanum.core.HOST_CLAIM_SECONDS", 2.0)
    with run_meta() as meta:

        async def main():
            core, _ = await new_core(tmp_path / "olive", maintainers=[meta])
            await core.set_setting("preferred_relays", ["ws://127.0.0.1:1/ws"])
            with pytest.raises(UserError, match="reach"):
                await core.host_community(dict(WIZARD), "Olive")
            assert core.hosting.items == {}
            assert list((core.paths.data / "hosted").iterdir()) == []
            assert core.vault.data["communities"] == {}
            await core.shutdown()

        run(main(), 90)


def test_hosted_server_stops_when_its_app_goes_away(tmp_path):
    """`community run -stop-with-stdin`: when the supervising app ends (its
    end of the pipe closes, as when it crashes), the server shuts down
    cleanly by itself."""
    with run_relay() as relay:
        node = CommunityNode(tmp_path / "c", [relay])
        p = subprocess.Popen(
            [str(binary("community")), "run", "-data", str(node.data), "-stop-with-stdin"],
            stdin=subprocess.PIPE,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.PIPE,
        )
        try:
            time.sleep(1.0)
            assert p.poll() is None
            p.stdin.close()
            assert p.wait(15) == 0
            assert b"standard input closed" in p.stderr.read()
        finally:
            if p.poll() is None:
                p.kill()


def test_copy_the_community_server(tmp_path, hosted_env):
    """Host on your own server → Copy the community server: the bundled
    program, saved executable, to a file or into a folder."""

    async def main():
        out = await hosting.copy_binary(str(tmp_path / "community-copy"))
        assert out == str(tmp_path / "community-copy")
        into = await hosting.copy_binary(str(tmp_path))
        assert into == str(tmp_path / hosting.binary_name())
        for f in (out, into):
            if os.name == "posix":
                assert os.access(f, os.X_OK)
            data = str(tmp_path / ("data-" + os.path.basename(f)))
            r = subprocess.run(
                [f, "init", "-data", data, "-relay", "ws://relay.example.invalid/ws"], capture_output=True, text=True
            )
            assert r.returncode == 0 and "openarcanum-claim:" in r.stdout, r.stderr
        with pytest.raises(UserError, match="folder doesn't exist"):
            await hosting.copy_binary(str(tmp_path / "no" / "such" / "dir" / "x"))

    run(main(), 30)


# ---------------------------------------------------------------- relay failover


def test_relay_killed_mid_session_server_and_client_move(tmp_path):
    """Automatic relays: the relay the server and the member are on dies
    mid-session. The server moves to the other healthy relay from the
    registry and publishes its new route; the member's app follows it
    without anyone doing anything."""
    with (
        run_meta(writes_per_minute="60000") as meta,
        run_relay(registry_urls=meta, public_url="{url}", heartbeat_interval="1s") as r1,
        run_relay(registry_urls=meta, public_url="{url}", heartbeat_interval="1s") as r2,
    ):
        node = CommunityNode(tmp_path / "c", [r1], [meta], automatic=True)
        node.start(relay_count="1", heartbeat_interval="1s")
        try:

            async def main():
                owner, _ = await new_core(tmp_path / "owner", maintainers=[meta])
                await wait_until(lambda: _two_relays(owner), 15)
                r = await owner.claim(node.claim_code, WIZARD, "Olive", retry_for=20)
                cid = r["community"]
                ch = await general(owner, cid)
                assert owner.conns[cid].relay_url == r1
                await owner.send(cid, ch, "before")

                kill_relay(r1)
                await wait_until(lambda: owner.conns[cid].status == "online" and owner.conns[cid].relay_url == r2, 60)
                assert owner.vault.data["communities"][cid]["relays"] == [r2]
                await owner.send(cid, ch, "after the failover")
                hist = await owner.history(cid, ch)
                assert [m["text"] for m in hist["messages"] if m["text"] in ("before", "after the failover")] == [
                    "before",
                    "after the failover",
                ]
                await owner.shutdown()

            run(main(), 150)
        finally:
            node.stop()


def test_pinned_relay_down_shows_offline_and_never_moves(tmp_path):
    """A community pinned to its relay doesn't move to the public relay in
    the registry when its own dies: members see it offline, with the reason
    (its own relays are unreachable)."""
    with (
        run_meta(writes_per_minute="60000") as meta,
        run_relay(registry_urls=meta, public_url="{url}", heartbeat_interval="1s") as r1,
        run_relay(registry_urls=meta, public_url="{url}", heartbeat_interval="1s") as r2,
    ):
        node = CommunityNode(tmp_path / "c", [r1], [meta])
        node.start(heartbeat_interval="1s")
        try:

            async def main():
                owner, _ = await new_core(tmp_path / "owner", maintainers=[meta])
                cid = (await owner.claim(node.claim_code, WIZARD, "Olive"))["community"]
                await general(owner, cid)
                st = await server_state(owner, cid, "online")
                assert st["pinned"] and st["relays_attached"] == st["relays_wanted"] == 1
                kill_relay(r1)
                st = await server_state(owner, cid, "offline", 40)
                assert st["pinned"] and st["relays_attached"] == 0 and not st["stopping"]
                await wait_until(lambda: owner.conns[cid].status == "offline", 40)
                await server_state(owner, cid, "offline")
                summary = owner.community_summary(cid)
                assert summary["connection"] == "offline" and summary["server_status"]["pinned"]
                await asyncio.sleep(3)
                assert owner.conns[cid].status != "online"
                assert r2 not in owner.vault.data["communities"][cid]["relays"]
                await owner.shutdown()

            run(main(), 120)
        finally:
            node.stop()


# ---------------------------------------------------------------- reaping


def test_reaped_community_is_shown_shut_down_until_reregistered(tmp_path):
    """A server offline for longer than the maintainers' community_reap_after
    is shut down: route and listing go, members are told "shut down". Its
    server coming back is refused until `community reregister`; then
    members reconnect by themselves."""
    with (
        run_meta(writes_per_minute="60000", community_offline_after="10s", community_reap_after="10s") as meta,
        run_relay(registry_urls=meta, public_url="{url}", heartbeat_interval="1s") as relay,
    ):
        node = CommunityNode(tmp_path / "c", [relay], [meta])
        node.start(heartbeat_interval="1s")
        try:

            async def main():
                owner, _ = await new_core(tmp_path / "owner", maintainers=[meta])
                cid = (await owner.claim(node.claim_code, dict(WIZARD, visibility="public"), "Olive"))["community"]
                await general(owner, cid)
                await wait_until(lambda: owner.directory_search("Test Town"), 20)
                node.stop()
                st = await server_state(owner, cid, "terminated", 60)
                assert st["reap_after_days"] == 0 and st["last_seen"] > 0
                await wait_until(lambda: owner.conns[cid].status == "shut_down", 60)
                assert owner.community_summary(cid)["connection"] == "shut_down"
                assert await owner.directory_search("Test Town") == []

                # Back without re-registering: still refused.
                node.start(heartbeat_interval="1s")
                await asyncio.sleep(5)
                assert (await owner.meta.status(*_ids(owner, cid)))["state"] == "terminated"
                node.stop()

                # `community reregister`, then it runs again.
                rereg = [str(binary("community")), "reregister", "-data", str(node.data)]
                subprocess.run(rereg, check=True, capture_output=True)
                node.start(heartbeat_interval="1s")
                owner.conns[cid].reconnect_now()
                await wait_until(lambda: owner.conns[cid].status == "online", 90)
                await server_state(owner, cid, "online")
                await owner.shutdown()

            run(main(), 240)
        finally:
            node.stop()


def _ids(core, cid):
    from openarcanum import keys

    rec = core.vault.data["communities"][cid]
    return keys.decode(rec["route_id"]), keys.decode(rec["server_key"])


async def _two_relays(core) -> bool:
    return len(await core.meta.relays()) == 2
