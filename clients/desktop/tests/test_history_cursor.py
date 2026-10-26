"""The device-link history copy's cursor (docs/protocol.md §10.3).

The sender answers each request with the items strictly after the cursor,
the key of the last item the new device saved. The key names the item
(DM: group, time, id; community: community, channel, id), so purges,
cache evictions and re-sorts on the sending device between chunks can't
make the copy skip or repeat anything.
"""

import json
import random
from types import SimpleNamespace

from openarcanum import devices
from openarcanum.devices import DeviceManager


class FakeCache:
    def __init__(self):
        self.messages: dict[int, list[dict]] = {}


def sender(dm_hist: dict, caches: dict) -> DeviceManager:
    """A DeviceManager whose core holds only what the history copy reads."""
    core = SimpleNamespace(
        dms=SimpleNamespace(history_export=lambda: dict(dm_hist)),
        vault=SimpleNamespace(data={"communities": {cid: {} for cid in caches}}),
        cache=lambda cid: caches[cid],
    )
    dm = object.__new__(DeviceManager)
    dm.core = core
    return dm


def item_id(item: dict) -> tuple:
    m = item["m"]
    return (item["k"], item["id"], m.get("channel"), m["id"])


def build(n_dm: int = 120, n_c: int = 120):
    rng = random.Random(7)
    dm_hist = {
        gid: [{"id": f"{gid}-{i:04x}", "at": 1000 + i * 3, "text": "x" * rng.randrange(20, 200)} for i in range(n_dm)]
        for gid in ("g1", "g2")
    }
    caches = {}
    for cid in ("cA", "cB"):
        c = FakeCache()
        for ch in (1, 2):
            c.messages[ch] = [{"channel": ch, "id": i, "text": "y" * rng.randrange(20, 200)} for i in range(1, n_c)]
        caches[cid] = c
    return dm_hist, caches


def everything(dm_hist, caches) -> set:
    out = {("dm", g, None, m["id"]) for g, ms in dm_hist.items() for m in ms}
    out |= {("c", cid, m["channel"], m["id"]) for cid, c in caches.items() for ms in c.messages.values() for m in ms}
    return out


def test_copy_without_changes_sends_everything_once_in_bounded_chunks(monkeypatch):
    monkeypatch.setattr(devices, "HISTORY_CHUNK", 2048)
    dm_hist, caches = build()
    dev = sender(dm_hist, caches)
    got, cursor, chunks = [], None, 0
    while True:
        items, cursor = dev._chunk_after(cursor, True, sorted(caches))
        assert len(json.dumps(items)) <= devices.HISTORY_CHUNK + 512
        got += [item_id(i) for i in items]
        chunks += 1
        if cursor is None:
            break
        assert json.loads(json.dumps(cursor)) == cursor  # survives the vault and the wire
    assert chunks > 10
    assert len(got) == len(set(got))
    assert {(k, g, ch if k == "c" else None, i) for k, g, ch, i in got} == everything(dm_hist, caches)


def test_purge_evict_and_resort_between_chunks_skip_nothing(monkeypatch):
    """Interrupt the copy part way through each kind of history, change the
    sender's history behind and ahead of the cursor, resume: every item that
    still exists and wasn't received yet arrives exactly once."""
    monkeypatch.setattr(devices, "HISTORY_CHUNK", 2048)
    dm_hist, caches = build()
    original = everything(dm_hist, caches)
    dev = sender(dm_hist, caches)
    comms = sorted(caches)
    received: list[tuple] = []
    purged: set = set()
    rng = random.Random(11)

    def norm(t):
        k, g, ch, i = t
        return (k, g, ch if k == "c" else None, i)

    cursor = None
    step = 0
    while True:
        items, cursor = dev._chunk_after(cursor, True, comms)
        received += [norm(item_id(i)) for i in items]
        if cursor is None:
            break
        step += 1
        if step % 3:
            continue
        # The copy is interrupted here; meanwhile on the sending device:
        have = set(received)
        if cursor[0] == "dm":
            hist = dm_hist[cursor[1]]
            # retention purges the oldest messages (all already received)...
            old = [m for m in hist if ("dm", cursor[1], None, m["id"]) in have][:5]
            # ...including the very item the cursor names, and one not yet sent.
            last = next(m for m in hist if m["id"] == cursor[3])
            ahead = [m for m in hist if ("dm", cursor[1], None, m["id"]) not in have][:1]
            for m in [*old, last, *ahead]:
                if m in hist:
                    hist.remove(m)
                    purged.add(("dm", cursor[1], None, m["id"]))
            # ...and the stored list is re-sorted (history_import sorts by time
            # only, so equal times may come back in any order).
            rng.shuffle(hist)
        else:
            cache = caches[cursor[1]]
            # The cache evicts old messages of every channel, and the cursor's own.
            for ch, ms in cache.messages.items():
                evict = [m for m in ms if ("c", cursor[1], ch, m["id"]) in have][:4]
                if ch == cursor[2]:
                    evict += [m for m in ms if m["id"] == cursor[3] and m not in evict]
                for m in evict:
                    ms.remove(m)
                    purged.add(("c", cursor[1], ch, m["id"]))
                rng.shuffle(ms)
    assert len(received) == len(set(received)), "an item arrived twice"
    missing = original - purged - set(received)
    assert not missing, f"skipped {sorted(missing)[:5]}"


def test_bad_or_old_cursor_restarts_from_the_beginning():
    dm_hist, caches = build(5, 5)
    dev = sender(dm_hist, caches)
    first, _ = dev._chunk_after(None, True, sorted(caches))
    for bad in (
        ["dm", "g1", 3],
        ["dm", "g1", "x", "y"],
        ["c", "cA", 1.5, 2],
        ["c", "cA", True, 2],
        "nope",
        [1, 2, 3, 4],
    ):
        items, _ = dev._chunk_after(bad, True, sorted(caches))
        assert items == first


def test_cursor_after_the_last_item_ends_the_copy():
    dm_hist, caches = build(5, 5)
    dev = sender(dm_hist, caches)
    assert dev._chunk_after(["c", "zz", 0, 0], True, sorted(caches)) == ([], None)
