"""Community messaging end to end against the real relay and community
server binaries: mentions, unread and mention counts, reactions, pins,
typing, presence, nicknames and profiles, several attachments per message,
jump-to-message, feature toggles, and the link-preview fetcher refusing a
loopback address."""

import asyncio
import http.server
import threading

import pytest
from conftest import new_core, run, run_community, run_relay, wait_until
from test_e2e_community import WIZARD, general

from openarcanum import keys
from openarcanum.core import UserError
from openarcanum.wire import community_pb2 as cp


class _Counting(http.server.BaseHTTPRequestHandler):
    hits = 0

    def do_GET(self):
        type(self).hits += 1
        body = b'<html><head><meta property="og:title" content="should never be fetched"></head></html>'
        self.send_response(200)
        self.send_header("Content-Type", "text/html")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        """Keep test output quiet."""


def test_messaging_end_to_end(tmp_path):
    site = http.server.ThreadingHTTPServer(("127.0.0.1", 0), _Counting)
    threading.Thread(target=site.serve_forever, daemon=True).start()
    with run_relay() as relay, run_community(tmp_path, [relay], preview_schemes="http,https") as node:

        async def main():
            owner, oev = await new_core(tmp_path / "owner")
            res = await owner.claim(node.claim_code, WIZARD, "Olive")
            cid = res["community"]
            gen = await general(owner, cid)
            alice, aev = await new_core(tmp_path / "alice")
            await alice.join({"invite": res["invite"]}, "new", "Alice")
            await general(alice, cid)
            alice_key = alice.community_state(cid)["self"]["key"]
            await owner.members(cid)

            # A typed @Name becomes a mention token; Alice's client counts it.
            m = await owner.send(cid, gen, "hello @Alice, welcome")
            assert f"<@{alice_key}>" in m["text"] and m["mentions"] == [alice_key]
            got = await aev.wait(lambda n, p: n == "message" and p["message"]["id"] == m["id"])
            assert got["message"]["mentions_me"]
            await aev.wait(lambda n, p: n == "unread" and p["channel"] == gen and p["mentions"] == 1)
            st = alice.community_state(cid)
            assert st["mentions"] == 1 and next(c for c in st["channels"] if c["id"] == gen)["unread"] >= 1
            alice.mark_read(cid, gen)
            st = alice.community_state(cid)
            assert st["mentions"] == 0 and next(c for c in st["channels"] if c["id"] == gen)["unread"] == 0
            # @everyone from a member without the permission notifies nobody.
            plain = await alice.send(cid, gen, "@everyone look")
            assert not plain["mention_everyone"]

            # Reactions reach the other client as an update.
            await alice.react(cid, m["id"], "🎉")
            await oev.wait(lambda n, p: n == "message" and p["message"]["id"] == m["id"] and p["message"]["reactions"])
            assert (await alice.react(cid, m["id"], "🎉", remove=True))["reactions"] == []
            # Updates carry no text: one for a message this client doesn't
            # have is ignored (never cached as an empty message).
            unknown = cp.MessageUpdate(message_id=m["id"] + 10_000, channel_id=gen, pinned=True)
            assert alice.apply_update(cid, unknown) is None
            assert alice.cache(cid).find(gen, m["id"] + 10_000) is None
            assert alice.cache(cid).find(gen, m["id"])["text"] == m["text"]

            # A reply carries reply_to to the other client and in history; a
            # reply to a message that doesn't exist is refused.
            r = await alice.send(cid, gen, "replying to you", reply_to=m["id"])
            assert r["reply_to"] == m["id"]
            got = await oev.wait(lambda n, p: n == "message" and p["message"]["id"] == r["id"])
            assert got["message"]["reply_to"] == m["id"]
            with pytest.raises(UserError, match="replied to is gone"):
                await alice.send(cid, gen, "to nothing", reply_to=m["id"] + 10_000)
            hist = await owner.history(cid, gen)
            assert next(x for x in hist["messages"] if x["id"] == r["id"])["reply_to"] == m["id"]
            assert "to nothing" not in [x["text"] for x in hist["messages"]]

            # Deleting one message: the author deletes it, the other client
            # sees it deleted (no text); someone else's message needs Manage
            # Messages, which the owner has.
            gone = await alice.send(cid, gen, "delete me")
            await oev.wait(lambda n, p: n == "message" and p["message"]["id"] == gone["id"])
            await alice.delete_message(cid, gen, gone["id"])
            got = await oev.wait(
                lambda n, p: n == "message" and p["message"]["id"] == gone["id"] and p["message"]["deleted"]
            )
            assert got["message"]["text"] == ""
            with pytest.raises(UserError):
                await alice.delete_message(cid, gen, m["id"])
            theirs = await alice.send(cid, gen, "moderate me")
            await owner.delete_message(cid, gen, theirs["id"])
            await aev.wait(
                lambda n, p: n == "message" and p["message"]["id"] == theirs["id"] and p["message"]["deleted"]
            )
            hist = await alice.history(cid, gen)
            deleted = [x for x in hist["messages"] if x["id"] in (gone["id"], theirs["id"])]
            assert len(deleted) == 2 and all(x["deleted"] and x["text"] == "" for x in deleted)
            assert not next(x for x in hist["messages"] if x["id"] == m["id"])["deleted"]

            # Pins: Manage Messages only.
            with pytest.raises(UserError):
                await alice.pin(cid, m["id"])
            assert (await owner.pin(cid, m["id"]))["pinned"]
            assert [p["id"] for p in await alice.pins(cid, gen)] == [m["id"]]

            # Typing shows on the other client, with the channel.
            await alice.typing(cid, gen)
            ev = await oev.wait(lambda n, p: n == "typing")
            assert ev["channel"] == gen and ev["name"] == "Alice" and ev["member"] == alice_key

            # Nickname and profile, seen in the member list with online status.
            await alice.set_nickname(cid, alice_key, "Ally")
            await alice.update_profile(cid, "Likes herons.", "she/her")
            members = await owner.members(cid)
            me = next(x for x in members if x["key"] == alice_key)
            assert me["nickname"] == "Ally" and me["about"] == "Likes herons." and me["online"]
            assert (await alice.send(cid, gen, "named"))["author_name"] == "Ally"

            # Three attachments in one message; the owner saves each.
            paths = []
            for i in range(3):
                f = tmp_path / f"part{i}.txt"
                f.write_bytes(f"part {i}".encode())
                paths.append(str(f))
            sent = await alice.send(cid, gen, "three files", files=paths)
            assert [f["name"] for f in sent["files"]] == ["part0.txt", "part1.txt", "part2.txt"]
            got = await oev.wait(lambda n, p: n == "message" and p["message"]["id"] == sent["id"])
            for i, f in enumerate(got["message"]["files"]):
                out = tmp_path / f"saved{i}.txt"
                await owner.save_file(cid, f, str(out))
                assert out.read_bytes() == f"part {i}".encode()
            with pytest.raises(UserError, match="at most 10"):
                await alice.send(cid, gen, "", files=paths * 4)

            # Jump to an older message: the messages around it.
            around = await alice.history_around(cid, gen, m["id"])
            assert m["id"] in [x["id"] for x in around["messages"]]

            # Toggles: reactions off is refused as turned off.
            await owner.admin(cid, "update_settings", {"features": {"reactions": False}})
            await wait_until(lambda: not alice.community_state(cid)["settings"]["features"]["reactions"])
            with pytest.raises(UserError, match="turned off"):
                await alice.react(cid, m["id"], "👍")
            feats = next(c for c in alice.community_state(cid)["channels"] if c["id"] == gen)["features"]
            assert not feats["reactions"]
            # Member contact cards off: publishing one through the client is
            # refused as disabled, and so is fetching someone's.
            await alice.dms.set_contact_card(cid, True)
            await owner.admin(cid, "update_settings", {"features": {"contact_cards": False}})
            await wait_until(lambda: not alice.community_state(cid)["settings"]["features"]["contact_cards"])
            with pytest.raises(UserError, match="doesn't allow contact cards"):
                await alice.dms.set_contact_card(cid, True)
            with pytest.raises(UserError, match="doesn't allow contact cards"):
                await owner.dms._fetch_card(cid, keys.decode(alice_key), claim=False)
            await owner.admin(cid, "update_settings", {"features": {"contact_cards": True}})
            # Channel-level attachments off.
            await owner.admin(cid, "edit_channel", {"id": gen, "attachments": "off"})
            await wait_until(
                lambda: (
                    not next(c for c in alice.community_state(cid)["channels"] if c["id"] == gen)["features"][
                        "attachments"
                    ]
                )
            )
            with pytest.raises(UserError, match="turned off"):
                await alice.send(cid, gen, "", files=paths[:1])

            # Link previews on: a link to a loopback address is never fetched.
            await owner.admin(cid, "update_settings", {"features": {"link_previews": True}})
            port = site.server_address[1]
            linked = await alice.send(cid, gen, f"see http://127.0.0.1:{port}/page and http://localhost:{port}/")
            await asyncio.sleep(2.5)
            hist = await alice.history(cid, gen)
            assert next(x for x in hist["messages"] if x["id"] == linked["id"])["preview"] is None
            assert _Counting.hits == 0

            # Presence: Alice goes offline when her client stops.
            await alice.shutdown()
            ev = await oev.wait(lambda n, p: n == "presence" and p["member"] == alice_key and not p["online"])
            assert not next(x for x in await owner.members(cid) if x["key"] == alice_key)["online"]
            await owner.shutdown()

        try:
            run(main(), 150)
        finally:
            site.shutdown()
