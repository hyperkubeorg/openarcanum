"""A maintainer node restored from an older backup may hand out mailbox ids
again that a running client already read past. The node's mailbox epoch
changes on a restore, and the client then starts its read positions on that
node over, so nothing new is skipped (docs/protocol.md §7.1)."""

import asyncio
from types import SimpleNamespace

from openarcanum.dm import DMManager
from openarcanum.wire import meta_pb2 as mp


class FakeNode:
    """One node's mailbox for one token: items (id, blob) and an epoch."""

    def __init__(self):
        self.epoch = 11
        self.items: list[tuple[int, bytes]] = []
        self.deleted: list[int] = []

    async def fetch_page(self, base, token, after_id=0):
        page = mp.MailboxItems(epoch=self.epoch)
        for i, blob in self.items:
            if i > after_id:
                page.items.add(id=i, blob=blob)
        return page

    async def delete(self, base, token, ids):
        self.deleted += ids
        self.items = [(i, b) for i, b in self.items if i not in ids]

    def cluster_of(self, base):
        return [base]  # an independent node


def manager(node):
    dm = DMManager.__new__(DMManager)
    dm.core = SimpleNamespace(meta=node)
    dm._cursor = {}
    dm._epochs = {}
    dm._handled_gids = {}
    got = []

    async def on_blob(blob, source=None):
        got.append(blob)
        return True

    dm.on_blob = on_blob
    return dm, got


def test_restored_node_ids_go_back():
    async def main():
        node = FakeNode()
        dm, got = manager(node)
        node.items = [(i, b"old %d" % i) for i in range(1, 9)]
        await dm._poll_node("http://m", b"t" * 32)
        assert len(got) == 8 and dm._cursor[("http://m", b"t" * 32)] == 8
        # The node is lost and restored from a backup taken at id 3: new
        # items get ids 4, 5 again, under a new epoch.
        node.epoch = 12
        node.items = [(4, b"new 4"), (5, b"new 5")]
        got.clear()
        await dm._poll_node("http://m", b"t" * 32)
        assert got == [b"new 4", b"new 5"]
        # Same epoch from now on: positions work as before.
        node.items = [(6, b"new 6")]
        got.clear()
        await dm._poll_node("http://m", b"t" * 32)
        assert got == [b"new 6"]

    asyncio.run(main())


def test_epoch_change_resets_every_token_on_that_node_only():
    dm, _ = manager(FakeNode())
    dm._cursor = {("a", b"1"): 5, ("a", b"2"): 7, ("b", b"1"): 9}
    assert not dm._epoch_changed("a", 1)  # first sight
    assert not dm._epoch_changed("a", 1)
    assert dm._epoch_changed("a", 2)
    assert dm._cursor == {("a", b"1"): 0, ("a", b"2"): 0, ("b", b"1"): 9}
