"""Native notifications, automated on Linux: the app
talks to the desktop's notification service on the session bus (a stand-in
records what arrives). While locked, notifications never carry content."""

import json
import shlex
import shutil
import subprocess
import sys
import textwrap
from pathlib import Path
from types import SimpleNamespace

import pytest
from conftest import Events, missing_harness, network, new_core, run, wait_until
from test_e2e_community import general
from uidriver import App
from uiflows import claim, create_profile

from openarcanum import PRODUCT_NAME, keys
from openarcanum.core import Core
from openarcanum.paths import Paths

# The freedesktop service on the session bus is Linux's; Windows and macOS
# deliver through the system tray / Notification Center, checked by hand.
LINUX_BUS = [
    pytest.mark.skipif(not sys.platform.startswith("linux"), reason="Linux desktop bus test"),
    pytest.mark.gui,
]

SECRET = "lunch at noon, the usual place"


@LINUX_BUS[0]
@LINUX_BUS[1]
def test_desktop_notifications_and_locked_privacy(tmp_path):
    seen = tmp_path / "notifications.jsonl"
    helper = [sys.executable, str(Path(__file__).with_name("fake_notifyd.py")), str(seen)]

    def notes() -> list[dict]:
        return [json.loads(ln) for ln in seen.read_text().splitlines()] if seen.exists() else []

    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            olive = await App(tmp_path / "olive", name="olive", session_helper=helper).start()
            try:
                await create_profile(olive)
                invite = await claim(olive, node.claim_code, "Notify Town", "Olive")
                alice = Core(Paths.under(tmp_path / "alice"), emit=Events())
                await alice.create_profile("alice passphrase 1234")
                await alice.set_setting("maintainers", [meta])
                cid = (await alice.join({"invite": invite}, "new", "Alice"))["community"]
                ch = await general(alice, cid)
                # Olive looks at another view, so the message notifies.
                await olive.js("st.view = 'dms'; render(); 0")
                await alice.send(cid, ch, SECRET)
                await wait_until(lambda: any(SECRET in n["body"] for n in notes()), 30, 0.2)
                n = next(n for n in notes() if SECRET in n["body"])
                assert "Alice" in n["summary"], n
                # In the window it's a badge, never a pop-up: the community's
                # icon marks the unread message; nothing appears in #toasts.
                await olive.wait_js(
                    "document.querySelector('.rail-item[data-community].has-unread')", "the unread mark on the icon"
                )
                assert await olive.js("document.querySelectorAll('#toasts .toast').length") == 0
                # Locked: only that something happened.
                await olive.js("lockNow(); 0")
                await olive.wait_js("!document.querySelector('#lock').classList.contains('hidden')", "locked")
                await alice.send(cid, ch, SECRET + " again")
                await wait_until(lambda: any(n["body"] == "New activity" for n in notes()), 30, 0.2)
                assert not any((SECRET + " again") in n["body"] for n in notes())
                # Nor does the page show anything over the lock screen.
                assert "Notify Town" not in await olive.js("document.querySelector('#toasts').textContent")
                assert await olive.js("document.querySelectorAll('#toasts .toast').length") == 0
                await alice.shutdown()
            finally:
                await olive.stop()

        run(main(), 180)


def test_dm_notifications_follow_the_mode_and_hide_everything_while_locked(tmp_path):
    """DMs follow "Notify me about" (all / mentions only) and, while locked,
    say only that something happened: no sender or group name, no text."""

    async def main():
        core, ev = await new_core(tmp_path / "olive")
        gid = "g1"
        core.dms.d["groups"][gid] = {"status": "active"}

        def note(mentions: bool) -> dict | None:
            before = len(ev.of("notify"))
            core.dms._notify(gid, {"mentions_me": mentions, "name": "Alice", "text": SECRET})
            after = ev.of("notify")
            return after[-1] if len(after) > before else None

        n = note(False)
        assert n["title"] == "Alice" and n["body"] == SECRET
        assert n["community_name"] == "Direct messages" and n["dm"] == gid
        await core.set_setting("notify_mode", "mentions")
        assert note(False) is None
        assert note(True)["body"] == SECRET
        await core.set_setting("notify_mode", "all")
        await core.lock()
        n = note(False)
        assert (n["title"], n["body"]) == (PRODUCT_NAME, "New activity")
        assert "Alice" not in json.dumps(n) and SECRET not in json.dumps(n)
        # Not even that it's a DM: the in-window notice shows no place.
        assert n["community_name"] == ""
        await core.shutdown()

    run(main(), 60)


def test_channel_notifications_hide_everything_while_locked(tmp_path):
    """A channel message's notification names the sender and the channel;
    while locked it says only that something happened: no sender, text,
    channel or community name."""

    async def main():
        core, ev = await new_core(tmp_path / "olive")
        cid = "c1"
        core.vault.data["communities"][cid] = {"name": "Notify Town", "display_name": "Olive", "status": "active"}
        chan = SimpleNamespace(name="general")
        me = SimpleNamespace(state=SimpleNamespace(self=SimpleNamespace(key=b"\x01" * 32)), channel=lambda _id: chan)
        core.conns[cid] = me
        m = {"author": keys.encode(b"\x02" * 32), "author_name": "Alice", "channel_id": 5, "text": SECRET}

        def note() -> dict:
            before = len(ev.of("notify"))
            core._maybe_notify(cid, m)
            after = ev.of("notify")
            assert len(after) > before
            return after[-1]

        n = note()
        assert (n["title"], n["body"], n["community_name"]) == ("Alice · #general", SECRET, "Notify Town")
        await core.lock()
        n = note()
        assert (n["title"], n["body"], n["community_name"]) == (PRODUCT_NAME, "New activity", "")
        for word in ("Alice", "general", "Notify Town", SECRET):
            assert word not in json.dumps(n)
        del core.conns[cid]
        await core.shutdown()

    run(main(), 60)


@pytest.mark.skipif(not sys.platform.startswith("linux"), reason="the freedesktop service is Linux's")
def test_linux_notification_bodies_are_escaped(tmp_path):
    """Message text and the summary (a display name someone else chose) are
    escaped for services that render markup: a member's <a href> or <img>
    arrives as text, and & or < can't break it."""
    if not shutil.which("dbus-run-session"):
        missing_harness("needs dbus-run-session")
    seen = tmp_path / "seen.jsonl"
    sender = textwrap.dedent(
        """
        import sys, time
        from openarcanum.notify import LinuxNotifier
        n = LinuxNotifier()
        n.send('Alice <a href="https://evil.example">&</a>', sys.argv[1])
        for _ in range(100):
            if n.working is not None:
                break
            time.sleep(0.05)
        sys.exit(0 if n.working else 1)
        """
    )
    body = '<a href="https://evil.example/login">re-verify</a> & <img src="x"> 1 < 2'
    script = (
        f"{shlex.join([sys.executable, str(Path(__file__).with_name('fake_notifyd.py')), str(seen)])} &"
        " sleep 1;"
        f" exec {shlex.join([sys.executable, '-c', sender, body])}"
    )
    p = subprocess.run(["dbus-run-session", "--", "sh", "-c", script], capture_output=True, text=True, timeout=60)
    assert p.returncode == 0, p.stderr[-2000:]
    [n] = [json.loads(ln) for ln in seen.read_text().splitlines()]
    assert n["summary"] == 'Alice &lt;a href="https://evil.example"&gt;&amp;&lt;/a&gt;'
    assert (
        n["body"] == '&lt;a href="https://evil.example/login"&gt;re-verify&lt;/a&gt; &amp; &lt;img src="x"&gt; 1 &lt; 2'
    )
