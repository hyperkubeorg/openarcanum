"""A voice channel's text chat end to end (docs/protocol.md §4.3): two
clients against the real relay and community server binaries. Messages in
a voice channel arrive live on its topic, count as unread, come back in
history and after being offline, and follow the same permissions as a text
channel; with voice channels turned off they can't be posted."""

import pytest
from conftest import new_core, run, run_community, run_relay, wait_until
from test_e2e_community import WIZARD, general

from openarcanum.core import UserError


def chan(core, cid, name):
    return next(c for c in core.community_state(cid)["channels"] if c["name"] == name)


def test_voice_channel_text_chat(tmp_path):
    with run_relay() as relay, run_community(tmp_path, [relay]) as node:

        async def main():
            owner, oev = await new_core(tmp_path / "owner")
            res = await owner.claim(node.claim_code, WIZARD, "Olive")
            cid = res["community"]
            await general(owner, cid)
            alice, aev = await new_core(tmp_path / "alice")
            await alice.join({"invite": res["invite"]}, "new", "Alice")
            await general(alice, cid)
            vc = (await owner.admin(cid, "create_channel", {"name": "Lounge", "kind": "voice"}))["id"]
            await wait_until(lambda: any(c["id"] == vc for c in alice.community_state(cid)["channels"]), 15)
            assert chan(alice, cid, "Lounge")["kind"] == "voice"

            # Live both ways through the voice channel's topic; unread counts
            # for the channel and the community; history.
            await owner.send(cid, vc, "anyone around for a game?")
            got = await aev.wait(lambda n, p: n == "message" and p["message"]["text"] == "anyone around for a game?")
            assert got["message"]["channel_id"] == vc
            await wait_until(lambda: chan(alice, cid, "Lounge")["unread"] == 1, 15)
            assert alice.community_summary(cid)["unread"] >= 1
            await aev.wait(lambda n, p: n == "notify" and p["body"] == "anyone around for a game?")
            await alice.send(cid, vc, "me, give me five minutes")
            await oev.wait(lambda n, p: n == "message" and p["message"]["text"] == "me, give me five minutes")
            alice.mark_read(cid, vc)
            assert chan(alice, cid, "Lounge")["unread"] == 0
            hist = await alice.history(cid, vc)
            assert [m["text"] for m in hist["messages"] if not m.get("system")] == [
                "anyone around for a game?",
                "me, give me five minutes",
            ]

            # Messages sent while Alice is away arrive when she's back.
            await alice.shutdown()
            await owner.send(cid, vc, "starting now")
            alice2, _ev2 = await new_core_reopen(tmp_path / "alice")
            await general(alice2, cid)
            await wait_until(lambda: any(m["text"] == "starting now" for m in alice2.cached_messages(cid, vc)), 20)
            assert chan(alice2, cid, "Lounge")["unread"] == 1

            # Send Messages denied in the voice channel: refused.
            akey = alice2.community_state(cid)["self"]["key"]
            await owner.admin(cid, "set_override", {"channel_id": vc, "member": akey, "deny": ["send_messages"]})
            await wait_until(lambda: not chan(alice2, cid, "Lounge")["can"]["send_messages"], 15)
            with pytest.raises(UserError):
                await alice2.send(cid, vc, "let me in")
            await owner.admin(cid, "set_override", {"channel_id": vc, "member": akey, "deny": []})

            # Voice channels off: their chat is off too.
            await owner.admin(cid, "update_settings", {"features": {"voice": False}})
            await wait_until(lambda: alice2.community_summary(cid)["features"]["voice"] is False, 15)
            with pytest.raises(UserError, match="turned off voice channels"):
                await owner.send(cid, vc, "hello?")
            # A hidden voice channel's chat doesn't count as unread.
            assert alice2.community_summary(cid)["unread"] == 0
            for c in (owner, alice2):
                await c.shutdown()

        run(main(), 120)


async def new_core_reopen(home):
    """The same profile again, unlocked (a restart)."""
    from conftest import PASS, Events

    from openarcanum.core import Core
    from openarcanum.paths import Paths

    ev = Events()
    core = Core(Paths.under(home), emit=ev)
    await core.unlock(PASS)
    return core, ev
