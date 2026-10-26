"""Maintainer clusters on the client (docs/protocol.md §7.5): entries that pin
a cluster's operator key, verified member lists, node health with backoff,
and the drop policy. The end-to-end flows with real nodes are in
test_e2e_mailbox_failover.py."""

import asyncio
import time

import pytest
from conftest import run

from openarcanum import keys
from openarcanum import settings as S
from openarcanum.core import Core
from openarcanum.meta import MetaClient, MetaError
from openarcanum.nodes import Clusters, NodeHealth, SetError, parse_entry, verify_set
from openarcanum.paths import Paths
from openarcanum.wire import meta_pb2 as mp


def sign_set(op: keys.SigningKey, seq: int, urls: list[str], *, issued=None, life=180 * 86400) -> bytes:
    now = int(time.time() * 1000) if issued is None else issued
    s = mp.MaintainerSet(operator_key=op.public, seq=seq, issued_at=now, expires_at=now + life * 1000)
    for i, u in enumerate(urls):
        s.nodes.add(url=u, node_key=keys.SigningKey.generate().public, role="root" if i == 0 else "member")
    body = s.SerializeToString()
    return mp.SignedMaintainerSet(set=body, signature=op.sign(keys.CTX_MAINTAINER_SET, body)).SerializeToString()


def test_maintainer_set_verification_and_pinning():
    op, other = keys.SigningKey.generate(), keys.SigningKey.generate()
    good = sign_set(op, 2, ["https://m1.example.invalid", "https://m2.example.invalid"])
    assert [n.url for n in verify_set(good, op.public).nodes] == [
        "https://m1.example.invalid",
        "https://m2.example.invalid",
    ]
    assert verify_set(good, None).operator_key == op.public  # trust on first use, shown to confirm
    with pytest.raises(SetError):
        verify_set(good, other.public)
    signed = mp.SignedMaintainerSet.FromString(good)
    tampered = mp.SignedMaintainerSet(set=signed.set + b"\x28\x01", signature=signed.signature)
    for bad in (
        tampered.SerializeToString(),
        sign_set(op, 1, ["https://m1.example.invalid"], issued=int(time.time() * 1000) - 10_000_000, life=1),
        sign_set(op, 1, []),
        sign_set(op, 1, ["ftp://m1.example.invalid"]),
        sign_set(op, 1, [f"https://m{i}.example.invalid" for i in range(9)]),
        b"\xff\xff",
    ):
        with pytest.raises(SetError):
            verify_set(bad, op.public)
    # The highest seq wins; an older one is ignored.
    c = Clusters()
    assert c.offer(good, op.public)
    assert not c.offer(sign_set(op, 1, ["https://old.example.invalid"]), op.public)
    assert c.sets[op.public].seq == 2
    assert c.offer(sign_set(op, 3, ["https://m3.example.invalid"]), op.public)
    with pytest.raises(SetError):
        c.offer(sign_set(other, 9, ["https://evil.example.invalid"]), op.public)


def test_cluster_entries_expand_to_members(tmp_path):
    """URL#KEY entries are a setting value; with a verified set cached in
    preferences, the nodes in use are the cluster's members (also locked and
    after a restart), and the toggle limits it to the configured node."""
    op = keys.SigningKey.generate()
    entry = "https://m1.example.invalid#" + keys.encode(op.public)
    assert parse_entry(entry) == ("https://m1.example.invalid", op.public)
    assert S.validate("maintainers", [entry.replace("#", "/#"), "https://plain.example.invalid"]) == [
        entry,
        "https://plain.example.invalid",
    ]
    with pytest.raises(S.SettingError):
        S.validate("maintainers", ["https://m1.example.invalid#short"])
    with pytest.raises(S.SettingError):
        S.validate("maintainer_cluster_nodes", "yes")

    async def main():
        core = Core(Paths.under(tmp_path / "a"))
        raw = sign_set(op, 1, ["https://m1.example.invalid", "https://m2.example.invalid"])
        await core.set_maintainers([entry, "https://plain.example.invalid"])
        core.clusters.offer(raw, op.public)
        core.prefs.set_maintainer_sets(core.clusters.cached())
        core._apply_maintainers()
        assert core.meta.urls == [
            "https://m1.example.invalid",
            "https://m2.example.invalid",
            "https://plain.example.invalid",
        ]
        assert core.meta.node_groups() == [
            ["https://m1.example.invalid", "https://m2.example.invalid"],
            ["https://plain.example.invalid"],
        ]
        st = core.maintainers_status()
        (cl,) = st["clusters"]
        assert cl["fingerprint"] == keys.fingerprint(op.public) and cl["seq"] == 1 and len(cl["members"]) == 2
        assert set(st["health"]) == set(core.meta.urls) and st["use_cluster"] is True
        await core.set_setting("maintainer_cluster_nodes", False)
        assert core.meta.urls == ["https://m1.example.invalid", "https://plain.example.invalid"]
        await core.set_setting("maintainer_cluster_nodes", True)
        await core.shutdown()
        # After a restart the cached set is checked again and used.
        again = Core(Paths.under(tmp_path / "a"))
        assert again.meta.urls[:2] == ["https://m1.example.invalid", "https://m2.example.invalid"]
        # Dropping the entry drops its cached set.
        await again.set_maintainers(["https://plain.example.invalid"])
        assert again.prefs.maintainer_sets == [] and again.meta.urls == ["https://plain.example.invalid"]
        await again.shutdown()

    run(main())


def test_node_health_backoff_order():
    now = [1000.0]
    h = NodeHealth(clock=lambda: now[0])
    urls = ["https://a.invalid", "https://b.invalid", "https://c.invalid"]
    assert h.order(urls) == urls
    h.failed("https://a.invalid", "down")
    assert h.order(urls) == ["https://b.invalid", "https://c.invalid", "https://a.invalid"]
    assert h.status("https://a.invalid")["state"] == "backoff"
    first_wait = h.nodes["https://a.invalid"].next_try - now[0]
    h.failed("https://a.invalid", "down")
    assert h.nodes["https://a.invalid"].next_try - now[0] > first_wait  # exponential
    for _ in range(20):
        h.failed("https://a.invalid", "down")
    assert h.nodes["https://a.invalid"].next_try - now[0] <= 600 * 1.25  # capped at 10 minutes
    now[0] += 1000
    assert h.usable("https://a.invalid")
    h.ok("https://a.invalid", 0.05)
    assert h.order(urls) == urls and h.status("https://a.invalid")["state"] == "ok"


def test_drop_policy(monkeypatch):
    """A drop goes to the two healthiest members of a cluster (they
    replicate), to every independent node, and on to the next member when
    both fail; it fails only if no node took it."""
    calls: list[str] = []
    down: set[str] = set()

    def fake(self, base, method, path, body, limit=0):
        calls.append(base)
        if base in down:
            self.health.failed(base, "down")
            raise MetaError("maintainer node unreachable: down")
        self.health.ok(base, 0.01)
        return b""

    monkeypatch.setattr(MetaClient, "_request", fake)
    cluster = ["https://m1.invalid", "https://m2.invalid", "https://m3.invalid"]
    m = MetaClient([*cluster, "https://solo.invalid"], groups=[cluster])

    async def main():
        assert await m.drop(b"i" * 32, b"x") == 3
        assert sorted(calls) == ["https://m1.invalid", "https://m2.invalid", "https://solo.invalid"]
        calls.clear()
        down.update({"https://m1.invalid", "https://m2.invalid"})
        assert await m.drop(b"i" * 32, b"y") == 2  # m3 and solo
        assert "https://m3.invalid" in calls
        calls.clear()
        # The failed members now come last.
        assert await m.drop(b"i" * 32, b"z") == 2
        assert calls[0] in ("https://m3.invalid", "https://solo.invalid")
        down.update({"https://m3.invalid", "https://solo.invalid"})
        with pytest.raises(MetaError):
            await m.drop(b"i" * 32, b"w")
        # Reading: the healthiest member of the cluster plus independent
        # nodes, or every node not backing off.
        down.clear()
        for u in m.urls:
            m.health.ok(u, 0.01)
        assert m.poll_nodes(False) == ["https://m1.invalid", "https://solo.invalid"]
        assert m.poll_nodes(True) == m.urls
        m.health.failed("https://m1.invalid", "down")
        assert m.poll_nodes(False) == ["https://m2.invalid", "https://solo.invalid"]
        # Blobs: where it was stored, then the rest of that cluster.
        assert m.blob_nodes(["https://m2.invalid"]) == [
            "https://m2.invalid",
            "https://m1.invalid",
            "https://m3.invalid",
        ]
        assert m.blob_nodes(["https://solo.invalid"]) == ["https://solo.invalid"]

    asyncio.run(main())


def test_directory_switched_off_on_every_node_hides_it(tmp_path):
    """Public listing switched off (directory_enabled false): the app learns
    it from each node (GET /v1/node), searches only through nodes that list,
    and reports the directory unavailable once no node in use offers it."""
    from conftest import run_meta

    with (
        run_meta(writes_per_minute="60000", directory_enabled="false") as off1,
        run_meta(writes_per_minute="60000", directory_enabled="false") as off2,
        run_meta(writes_per_minute="60000") as on,
    ):

        async def main():
            core = Core(Paths.under(tmp_path / "a"))
            await core.set_maintainers([off1, on])
            await core.refresh_directory()
            st = core.maintainers_status()
            assert st["directory"] is True
            assert core.meta.directory_urls() == [on]
            assert await core.meta.search("anything") == []  # answered by the node that lists
            await core.set_maintainers([off1, off2])
            await core.refresh_directory()
            assert core.maintainers_status()["directory"] is False
            with pytest.raises(MetaError) as e:
                await core.meta.search("anything")
            assert e.value.status == 501
            # A node that hasn't said yet counts as listing until it does.
            fresh = MetaClient([off1, off2])
            assert fresh.directory_available()
            with pytest.raises(MetaError):
                await fresh.search("x")
            assert not fresh.directory_available() and fresh.health.status(off1)["state"] == "ok"
            await core.shutdown()

        run(main())


def test_route_that_does_not_verify_sends_to_the_next_node(monkeypatch):
    """A node serving another community's route under a route id (a hijacked
    or lying node) doesn't end the lookup: the client asks the next node, and
    fails only when no node has a route that verifies."""
    from openarcanum import owners

    def route_of(founder: keys.SigningKey, route_id: bytes) -> bytes:
        server = keys.SigningKey.generate()
        cert = owners.issue_cert(founder, server.public, 1, owners.OwnerSet.founder(founder.public), [])
        raw = mp.Route(route_id=route_id, server_key=server.public, relays=["wss://r.example.invalid/ws"], seq=1)
        body = raw.SerializeToString()
        return mp.SignedRoute(route=body, signature=server.sign(keys.CTX_ROUTE, body), cert=cert).SerializeToString()

    rid = b"r" * 16
    owner, attacker = keys.SigningKey.generate(), keys.SigningKey.generate()
    served = {"https://m1.invalid": route_of(attacker, rid), "https://m2.invalid": route_of(owner, rid)}
    asked: list[str] = []

    def fake(self, base, method, path, body, limit=0):
        asked.append(base)
        self.health.ok(base, 0.01)
        return served[base]

    monkeypatch.setattr(MetaClient, "_request", fake)
    m = MetaClient(list(served))

    async def main():
        rt, _cert, owner_set = await m.route(rid, owner.public)
        assert asked == ["https://m1.invalid", "https://m2.invalid"]
        assert owner_set.community == owner.public and rt.route_id == rid
        served["https://m2.invalid"] = served["https://m1.invalid"]
        with pytest.raises(keys.BadSignature):
            await m.route(rid, owner.public)

    asyncio.run(main())
