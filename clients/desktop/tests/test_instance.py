"""Invite links from the OS and the single-instance hand-off (openarcanum/instance.py)."""

import contextlib
import json
import os
import subprocess
import sys
import textwrap
import time

import pytest

from openarcanum import codes, instance, keys
from openarcanum.wire import invite_pb2 as ip

LINK = codes.invite_link(
    ip.Invite(server_key=bytes(range(32)), owner_key=bytes(range(32, 64)), relays=["ws://127.0.0.1:1/ws"])
)
PAYLOAD = LINK[len(codes.LINK_SCHEME) :]


def test_link_from_os_accepts_invite_links():
    assert codes.link_from_os(LINK) == LINK
    # Case of scheme and host, surrounding space, a trailing slash, padding.
    assert codes.link_from_os(f"  OPENARCANUM://Invite/{PAYLOAD}/\n") == LINK
    assert codes.link_from_os(LINK + "==") == LINK + "=="
    # What it returns parses as the same invite.
    assert codes.parse_invite(codes.link_from_os(LINK)).relays == ["ws://127.0.0.1:1/ws"]


@pytest.mark.parametrize(
    "text",
    [
        "",
        "openarcanum://invite/",
        "openarcanum://invite",
        "openarcanum:invite/" + PAYLOAD,
        "openarcanum://claim/" + PAYLOAD,
        "https://invite/" + PAYLOAD,
        "openarcanum-invite:" + PAYLOAD,
        "openarcanum://invite/" + PAYLOAD + "?x=1",
        "openarcanum://invite/" + PAYLOAD + "#frag",
        "openarcanum://invite/a/b",
        "openarcanum://invite/a%20b",
        "openarcanum://invite/" + "A" * codes.MAX_LINK,
        "--smoke",
    ],
)
def test_link_from_os_rejects_everything_else(text):
    assert codes.link_from_os(text) is None


def test_messages_round_trip_and_reject_junk():
    assert instance.decode(instance.encode(LINK).strip()) == (True, LINK)
    assert instance.decode(instance.encode(None).strip()) == (True, None)
    assert instance.decode(b"not json") == (False, None)
    assert instance.decode(b'{"link": 5}') == (False, None)
    assert instance.decode(b'{"link": "https://example.invalid/"}') == (False, None)
    assert instance.decode(b'{"link": null, "extra": 1}') == (False, None)
    assert instance.decode(b"[]") == (False, None)
    assert instance.decode(json.dumps({"link": "openarcanum://invite/" + "A" * 20000}).encode()) == (False, None)


def test_server_name_is_per_profile(tmp_path, monkeypatch):
    a = instance.server_name(tmp_path / "a" / "config")
    assert a == instance.server_name(tmp_path / "a" / "config")
    assert a != instance.server_name(tmp_path / "b" / "config")
    if sys.platform.startswith("linux"):
        private = tmp_path / "run"
        private.mkdir(mode=0o700)
        monkeypatch.setenv("XDG_RUNTIME_DIR", str(private))
        assert os.path.dirname(instance.server_name(tmp_path / "a")) == str(private)
        # A runtime directory others can enter is not used, and neither is
        # /tmp: the socket goes in the profile's own private config folder.
        private.chmod(0o755)
        config = tmp_path / "c"
        config.mkdir(mode=0o700)
        assert instance.server_name(config) == str(config / instance.SOCKET_FILE)
        monkeypatch.delenv("XDG_RUNTIME_DIR")
        assert instance.server_name(config) == str(config / instance.SOCKET_FILE)
        # Too long for a socket address: a bare name (checked by owner).
        deep = tmp_path / ("d" * 120)
        deep.mkdir(mode=0o700)
        assert "/" not in instance.server_name(deep)


@pytest.mark.skipif(os.name == "nt", reason="Unix sockets (the Windows pipe check is below)")
def test_a_name_held_by_another_user_gets_no_link(tmp_path, monkeypatch):
    """Squatting: another local user's program listening on the name (in a
    shared folder) is never handed a link, and doesn't make this launch exit
    or back off: it runs on its own. Another user is simulated by changing
    which user ID counts as ours, for the file owner and for the peer."""
    import socket
    import threading

    name = f"openarcanum-squat-test-{os.getpid()}"
    monkeypatch.setenv("TMPDIR", str(tmp_path))
    path = instance.socket_path(name)
    assert path == str(tmp_path / name)
    srv = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    srv.bind(path)
    srv.listen(4)
    got: list[bytes] = []

    def serve():
        while True:
            try:
                c, _ = srv.accept()
            except OSError:
                return
            with c:
                c.settimeout(2)
                with contextlib.suppress(OSError):
                    got.append(c.recv(1 << 16))

    threading.Thread(target=serve, daemon=True).start()
    try:
        # Ours (same user): the hand-off goes through.
        assert instance.probe(name) == instance.OURS
        assert instance.hand_off(name, LINK) is True
        # The socket file belongs to someone else.
        real = os.getuid()
        monkeypatch.setattr(instance, "_my_uid", lambda: real + 1)
        assert instance.probe(name) == instance.FOREIGN
        assert instance.hand_off(name, LINK) is False
        # The file looks like ours, but the process serving it isn't.
        monkeypatch.setattr(instance, "_my_uid", lambda: real)
        monkeypatch.setattr(instance, "_peer_uid", lambda sock: real + 1)
        assert instance.probe(name) == instance.FOREIGN
        assert instance.hand_off(name, LINK) is False
        time.sleep(0.2)
        assert [g for g in got if g] == [instance.encode(LINK)]  # only the first, our own
    finally:
        srv.close()


@pytest.mark.skipif(os.name != "nt", reason="Windows named pipes")
def test_a_pipe_served_by_another_user_gets_no_link(tmp_path, monkeypatch):
    name = instance.server_name(tmp_path / "config")
    p, first = start_listener(name)
    try:
        assert first == {"listening": True}
        assert instance.probe(name) == instance.OURS  # the real SID check: our own server
        monkeypatch.setattr(instance._win, "current_sid", lambda: "S-1-5-21-0-0-0-4242")
        assert instance.probe(name) == instance.FOREIGN
        assert instance.hand_off(name, LINK) is False
    finally:
        stop(p)


def test_a_foreign_holder_doesnt_stop_the_app(tmp_path, monkeypatch):
    """The app's own end: with the name held by someone else, listen() says
    "go ahead" (run without single-instance hand-off) instead of "another
    instance is running" (which would make the app exit)."""
    script = textwrap.dedent(
        """
        import json, sys
        from qtpy.QtCore import QCoreApplication
        from openarcanum import instance

        app = QCoreApplication([])
        instance.probe = lambda name: instance.FOREIGN
        listener = instance.Listener(sys.argv[1], lambda link: None)
        print(json.dumps({"go": listener.listen(), "listening": listener.server.isListening()}))
        """
    )
    p = subprocess.run(
        [sys.executable, "-c", script, instance.server_name(tmp_path / "config")],
        capture_output=True,
        text=True,
        timeout=60,
        env=dict(os.environ, QT_QPA_PLATFORM="offscreen"),
    )
    assert p.returncode == 0, p.stderr[-2000:]
    assert json.loads(p.stdout.strip().splitlines()[-1]) == {"go": True, "listening": False}


# A running app's end, without the window: prints each link it is handed.
SERVER = textwrap.dedent(
    """
    import json, sys
    from qtpy.QtCore import QCoreApplication
    from openarcanum import instance

    app = QCoreApplication([])
    listener = instance.Listener(sys.argv[1], lambda link: print(json.dumps({"got": link}), flush=True))
    print(json.dumps({"listening": listener.listen()}), flush=True)
    app.exec()
    """
)


def start_listener(name: str) -> tuple[subprocess.Popen, dict]:
    p = subprocess.Popen(
        [sys.executable, "-c", SERVER, name], stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True
    )
    return p, json.loads(p.stdout.readline() or "{}")


def stop(p: subprocess.Popen) -> None:
    p.kill()
    p.wait(10)


def read_got(p: subprocess.Popen) -> object:
    return json.loads(p.stdout.readline())["got"]


def test_second_launch_hands_its_link_to_the_running_one(tmp_path):
    name = instance.server_name(tmp_path / "config")
    assert instance.hand_off(name, LINK) is False  # nothing running yet
    p, first = start_listener(name)
    try:
        assert first == {"listening": True}
        assert instance.hand_off(name, LINK) is True
        assert read_got(p) == LINK
        assert instance.hand_off(name, None) is True  # a plain launch: show the window
        assert read_got(p) is None

        # A second listener on the same profile sees the live one and backs off.
        p2, second = start_listener(name)
        stop(p2)
        assert second == {"listening": False}

        # Malformed and oversized messages are dropped; the listener keeps working.
        from qtpy.QtNetwork import QLocalSocket

        for junk in (b'{"link": "https://example.invalid/x"}\n', b"x" * (instance.MAX_MESSAGE + 10)):
            s = QLocalSocket()
            s.connectToServer(name)
            assert s.waitForConnected(2000)
            s.write(junk)
            s.waitForBytesWritten(2000)
            s.disconnectFromServer()
        assert instance.hand_off(name, LINK + "A") is True
        assert read_got(p) == LINK + "A"
    finally:
        stop(p)


@pytest.mark.skipif(os.name == "nt", reason="named pipes vanish with their process")
def test_a_crashed_instance_does_not_block_the_next(tmp_path):
    name = instance.server_name(tmp_path / "config")
    p, first = start_listener(name)
    assert first == {"listening": True}
    p.kill()  # no clean-up: the socket file stays behind
    p.wait(10)
    time.sleep(0.2)
    assert instance.hand_off(name, LINK) is False
    p2, second = start_listener(name)
    try:
        assert second == {"listening": True}
        assert instance.hand_off(name, LINK) is True
        assert read_got(p2) == LINK
    finally:
        stop(p2)


def test_url_open_events_reach_the_handler():
    """macOS sends an opened link to the application as a QFileOpenEvent; one
    sent before the handler is set is kept until it is."""
    script = textwrap.dedent(
        """
        import json, sys
        from qtpy.QtCore import QCoreApplication, QUrl
        from qtpy.QtGui import QFileOpenEvent
        from openarcanum import instance

        app = instance.create_application(["openarcanum"])
        got = []
        QCoreApplication.sendEvent(app, QFileOpenEvent(QUrl(sys.argv[1])))
        app.set_link_handler(got.append)
        QCoreApplication.sendEvent(app, QFileOpenEvent(QUrl("https://example.invalid/not-an-invite")))
        QCoreApplication.sendEvent(app, QFileOpenEvent(QUrl(sys.argv[1] + "B")))
        print(json.dumps(got))
        """
    )
    env = dict(os.environ, QT_QPA_PLATFORM="offscreen")
    p = subprocess.run([sys.executable, "-c", script, LINK], capture_output=True, text=True, timeout=60, env=env)
    assert p.returncode == 0, p.stderr[-2000:]
    assert json.loads(p.stdout.strip().splitlines()[-1]) == [LINK, LINK + "B"]


def test_payload_alphabet_matches_the_encoder():
    """Every invite the app makes passes link_from_os unchanged."""
    for n in range(1, 70):
        link = codes.LINK_SCHEME + keys.encode(os.urandom(n))
        assert codes.link_from_os(link) == link
