"""The client side of call permissions (voice.py): a permission the relay
enforces from the join's grant, once gained mid-call, makes the client
reconnect for a new grant; losing one doesn't (the relay's control applies
at once)."""

import asyncio

import pytest

from openarcanum import keys
from openarcanum.errors import UserError
from openarcanum.voice import Call, VoiceManager, _gained
from openarcanum.wire import call_pb2 as cw
from openarcanum.wire import community_pb2 as cp
from openarcanum.wire import relay_pb2 as rp


def session(**kw) -> cp.VoiceSession:
    return cp.VoiceSession(channel_id=1, **kw)


def test_gaining_speak_video_or_screen_needs_a_new_grant():
    base = session(listen_only=True)
    assert _gained(base, session(listen_only=False))
    assert _gained(session(), session(can_video=True))
    assert _gained(session(can_video=True), session(can_video=True, can_screen=True))


def test_losing_or_keeping_permissions_does_not():
    full = session(can_video=True, can_screen=True)
    assert not _gained(full, full)
    assert not _gained(full, session(listen_only=True))
    assert not _gained(full, session())


class FakeConn:
    """A community connection whose join reply is held until released."""

    def __init__(self, reply: cp.VoiceSession):
        self.reply = reply
        self.release = asyncio.Event()

    async def call(self, **_kw):
        await self.release.wait()
        return cp.ServerMessage(voice_session=self.reply)


class FakeCore:
    def __init__(self, conn: FakeConn):
        self.conns = {"c": conn}
        self.settings: dict = {}
        self.events: list = []

    def conn(self, cid):
        return self.conns[cid]

    def emit(self, name, data):
        self.events.append((name, data))

    def spawn(self, coro):
        return asyncio.ensure_future(coro)


def keyed(epoch: int, relay: str) -> cp.VoiceSession:
    return session(key_epoch=epoch, relay=relay, call_key=bytes([epoch]) * 32)


def test_an_older_session_never_replaces_a_newer_one():
    """Both people report a dead relay at once: the move rekeys (epoch 4),
    then the other report rekeys again (epoch 5). The server's push of 5
    can arrive before the reply to this device's own report, which carries
    4; installing that reply would leave the two sides on different keys."""

    async def main():
        conn = FakeConn(keyed(4, "ws://r2/ws"))
        vm = VoiceManager(FakeCore(conn))
        vm.call = Call("c", keyed(3, "ws://r1/ws"))
        report = asyncio.ensure_future(vm._rejoin(vm.call, cp.VoiceJoin(channel_id=1, failed_relay="ws://r1/ws")))
        await asyncio.sleep(0)
        await vm.server_update("c", keyed(4, "ws://r2/ws"))
        await vm.server_update("c", keyed(5, "ws://r2/ws"))
        conn.release.set()
        await report
        assert vm.call.session.key_epoch == 5
        # A late push of an older epoch is ignored too.
        await vm.server_update("c", keyed(4, "ws://r2/ws"))
        assert vm.call.session.key_epoch == 5
        pages = [d for n, d in vm.core.events if n == "call" and "session" in d]
        assert pages[-1]["session"]["epoch"] == 5

    asyncio.run(main())


class FakeRelay:
    def __init__(self):
        self.listens: list = []

    async def call_listen(self, call_id, deaf, no_audio, screen_audio):
        self.listens.append((call_id, deaf, no_audio, screen_audio))


def test_listening_state_goes_to_the_relay_checked():
    """The page's listening state (deafened, muted for me, screens heard)
    reaches the relay as participant ids; anything malformed is refused
    before it gets there."""

    async def main():
        vm = VoiceManager(FakeCore(FakeConn(session())))
        grant = cw.CallGrant(call_id=b"c" * 16)
        vm.call = Call("c", session(grant=cw.SignedCallGrant(grant=grant.SerializeToString())))
        vm.call.relay = FakeRelay()
        a, b = "aa" * 16, "bb" * 16
        await vm.signal(0, "listen", [{"deaf": True, "noAudio": [a], "screenAudio": [b]}])
        assert vm.call.relay.listens == [(b"c" * 16, True, [bytes.fromhex(a)], [bytes.fromhex(b)])]
        for bad in ([], ["x"], [{"noAudio": ["zz"]}], [{"noAudio": ["aa"]}], [{"screenAudio": [a] * 199}]):
            with pytest.raises(UserError):
                await vm.signal(0, "listen", bad)
        assert len(vm.call.relay.listens) == 1

    asyncio.run(main())


def test_relay_watchers_reach_the_page():
    """CallWatchers from the relay go to the page (call.js sends only what
    someone receives), for the current connection; a bad kind is dropped."""
    vm = VoiceManager(FakeCore(FakeConn(session())))
    vm.call = Call("c", session())
    vm.call.generation = 3
    w = cw.CallWatchers(
        watches=[cw.CallWatch(kind="audio", receivers=2), cw.CallWatch(kind="camera", receivers=1, layers=["l", "h"])]
    )
    vm._from_relay(vm.call, rp.RelayFrame(call_watchers=w))
    assert vm.core.events[-1] == (
        "call",
        {
            "type": "watchers",
            "generation": 3,
            "watches": [
                {"kind": "audio", "receivers": 2, "layers": []},
                {"kind": "camera", "receivers": 1, "layers": ["l", "h"]},
            ],
        },
    )
    n = len(vm.core.events)
    vm._from_relay(vm.call, rp.RelayFrame(call_watchers=cw.CallWatchers(watches=[cw.CallWatch(kind="x")])))
    vm._from_relay(Call("c", session()), rp.RelayFrame(call_watchers=w))  # not the current call
    assert len(vm.core.events) == n


def test_local_mute_is_a_setting_on_this_device(tmp_path):
    """Mute for me: kept in this device's settings, never synced."""
    from openarcanum import settings as S

    assert "voice_local_mutes" in S.PER_DEVICE and "voice_output_volume" in S.PER_DEVICE

    class Core(FakeCore):
        async def set_setting(self, key, value):
            self.settings[key] = S.validate(key, value)

    vm = VoiceManager(Core(FakeConn(session())))
    key = keys.encode(bytes(32))
    asyncio.run(vm.set_local_mute(key, True))
    asyncio.run(vm.set_local_mute(key, True))
    assert vm.core.settings["voice_local_mutes"] == [key]
    asyncio.run(vm.set_local_mute(key, False))
    assert vm.core.settings["voice_local_mutes"] == []
