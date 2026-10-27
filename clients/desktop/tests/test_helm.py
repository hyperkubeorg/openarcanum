"""The Helm chart (deploy/helm/openarcanum, docs/kubernetes.md): it lints, its
values are checked by values.schema.json, and the kind values render what
the local stack relies on: one relay URL that clients and the community
server both use, the node's network with cluster DNS, the call media port,
the community server's start-up and hardened containers."""

import os
import re
import shutil
import subprocess
import sys
from pathlib import Path

import pytest
from conftest import missing_harness

ROOT = Path(__file__).resolve().parents[3]
CHART = ROOT / "deploy" / "helm" / "openarcanum"
KIND_VALUES = CHART / "values-kind.yaml"
HELM = os.environ.get("HELM") or shutil.which("helm") or "helm"


@pytest.fixture(scope="module", autouse=True)
def helm_present():
    if not os.access(HELM, os.X_OK):
        missing_harness("needs helm (on PATH, or named by HELM; docs/kubernetes.md)")


def helm(*args: str, check: bool = True) -> subprocess.CompletedProcess:
    return subprocess.run([HELM, *args], capture_output=True, text=True, check=check)


def render(*args: str) -> list[str]:
    """The rendered objects, as YAML documents."""
    out = helm("template", "openarcanum", str(CHART), "--namespace", "openarcanum", *args).stdout
    return [d for d in out.split("\n---\n") if re.search(r"^kind: ", d, re.M)]


def doc(docs: list[str], kind: str, name: str) -> str:
    for d in docs:
        if re.search(rf"^kind: {kind}$", d, re.M) and re.search(rf"^  name: {name}$", d, re.M):
            return d
    raise AssertionError(f"no {kind} {name}")


def env(d: str, name: str) -> str:
    m = re.search(rf"- name: {name}\n\s+value: \"?([^\"\n]*)\"?", d)
    assert m, f"{name} not set"
    return m.group(1)


def test_lints_with_the_kind_values_and_a_public_relay():
    for extra in (["-f", str(KIND_VALUES)], ["--set", "relay.publicURL=wss://relay.example.invalid/ws"]):
        r = helm("lint", "--strict", str(CHART), *extra, check=False)
        assert r.returncode == 0, r.stdout + r.stderr


def test_relay_url_is_required_and_values_are_checked():
    r = helm("template", "openarcanum", str(CHART), check=False)
    assert r.returncode != 0 and "relay.publicURL is required" in r.stderr
    for bad in ("relay.hostNetwork=maybe", "relay.publicURL=http://not-a-websocket", "logLevel=loud"):
        r = helm("template", "openarcanum", str(CHART), "-f", str(KIND_VALUES), "--set", bad, check=False)
        assert r.returncode != 0 and "schema" in r.stderr.lower(), (bad, r.stderr)
    # Extra settings must be OPENARCANUM_* variables.
    r = helm("template", "openarcanum", str(CHART), "-f", str(KIND_VALUES), "--set", "meta.env.PATH=/x", check=False)
    assert r.returncode != 0


def test_kind_values_render_one_relay_url_everywhere():
    docs = render("-f", str(KIND_VALUES), "--set", "image.tag=t1")
    assert sorted(re.search(r"^kind: (\w+)", d, re.M).group(1) for d in docs) == sorted(
        ["PersistentVolumeClaim", "Service", "Deployment", "Service", "Deployment", "StatefulSet"]
    )
    relay = doc(docs, "Deployment", "openarcanum-relay")
    community = doc(docs, "StatefulSet", "openarcanum-community")
    url = "ws://127.0.0.1:7000/ws"
    # The URL the relay registers is the one the community server attaches
    # to and puts in its claim code: clients use it too (make kind-proxy).
    assert env(relay, "OPENARCANUM_RELAY_PUBLIC_URL") == url
    assert env(community, "OPENARCANUM_COMMUNITY_RELAYS") == url
    assert f'- -relay\n            - "{url}"' in community
    meta_url = "http://openarcanum-meta.openarcanum.svc.cluster.local:7100"
    assert env(relay, "OPENARCANUM_RELAY_REGISTRY_URLS") == meta_url
    assert env(community, "OPENARCANUM_COMMUNITY_MAINTAINERS") == meta_url
    # Both on the node's network (so 127.0.0.1:7000 is the relay for the
    # community server), still resolving cluster names.
    for d in (relay, community):
        assert "hostNetwork: true" in d and "dnsPolicy: ClusterFirstWithHostNet" in d
    # Call media advertised at 127.0.0.1, where kind maps 7000/udp.
    assert env(relay, "OPENARCANUM_RELAY_MEDIA_PUBLIC_IPS") == "127.0.0.1"
    assert env(relay, "OPENARCANUM_RELAY_MEDIA_UDP_PORT") == "7000"
    assert "protocol: UDP" in relay
    kind_cfg = (ROOT / "deploy/kind/cluster.yaml").read_text(encoding="utf-8")
    for line in ("containerPort: 7000", "protocol: UDP", 'listenAddress: "127.0.0.1"'):
        assert line in kind_cfg, line
    for d in docs:
        if "image:" in d:
            assert ":t1" in d
    assert "image: localhost/openarcanum-meta:t1" in doc(docs, "Deployment", "openarcanum-meta")


def test_default_images_are_the_released_ones():
    """Without overrides the chart runs the GHCR images of its own release
    (appVersion, kept equal to VERSION by scripts/version)."""
    chart = (CHART / "Chart.yaml").read_text(encoding="utf-8")
    app = re.search(r'^appVersion: "([^"]+)"$', chart, re.M).group(1)
    assert app == (ROOT / "VERSION").read_text(encoding="utf-8").strip()
    docs = render("--set", "relay.publicURL=wss://relay.example.invalid/ws")
    images = {m for d in docs for m in re.findall(r"image: (\S+)", d)}
    repo = "ghcr.io/hyperkubeorg/openarcanum"
    assert images == {f"{repo}-relay:{app}", f"{repo}-maintainer:{app}", f"{repo}-community:{app}"}


def test_community_starts_once_and_keeps_its_key_apart():
    community = doc(render("-f", str(KIND_VALUES)), "StatefulSet", "openarcanum-community")
    init = community[community.index("initContainers:") : community.index("containers:\n        - name: community")]
    for arg in ("- init", "- -if-needed", "- -claim-file", "- /keys/claim.txt"):
        assert arg in init, arg
    assert "name: keys" in community and "mountPath: /keys" in community and "mountPath: /data" in community


def test_containers_are_hardened():
    for d in render("-f", str(KIND_VALUES)):
        if "containers:" not in d:
            continue
        assert "runAsNonRoot: true" in d and "runAsUser: 65532" in d
        n = len(re.findall(r"^\s+image: ", d, re.M))  # one per container
        assert d.count("allowPrivilegeEscalation: false") == n
        assert d.count("readOnlyRootFilesystem: true") == n
        assert d.count('drop: ["ALL"]') == n
        assert "privileged" not in d


def test_default_values_keep_the_node_network_off():
    docs = render("--set", "relay.publicURL=wss://relay.example.invalid/ws")
    for d in docs:
        assert "hostNetwork" not in d
    relay = doc(docs, "Deployment", "openarcanum-relay")
    assert env(relay, "OPENARCANUM_RELAY_PUBLIC_URL") == "wss://relay.example.invalid/ws"
    assert "OPENARCANUM_RELAY_POW_MODE" not in relay


def known_env() -> dict[str, set[str]]:
    """Each server's OPENARCANUM_* variables, from the env tags of its Go config
    (the servers ignore unknown ones, so a typo would do nothing)."""
    tags = lambda f: set(re.findall(r'env:"([A-Z0-9_]+)"', (ROOT / f).read_text(encoding="utf-8")))  # noqa: E731
    shared = tags("internal/ops/settings.go")
    return {
        "relay": {f"OPENARCANUM_RELAY_{t}" for t in tags("internal/relay/config.go") | shared},
        "meta": {f"OPENARCANUM_META_{t}" for t in tags("internal/meta/config.go") | shared},
        "community": {f"OPENARCANUM_COMMUNITY_{t}" for t in tags("internal/community/config.go") | shared},
        "site": {f"OPENARCANUM_SITE_{t}" for t in tags("internal/site/config.go") | shared},
    }


def test_every_variable_the_chart_sets_exists():
    known = known_env()
    for values in (
        ["-f", str(KIND_VALUES)],
        ["--set", "relay.publicURL=wss://relay.example.invalid/ws"],
        ["-f", str(KIND_VALUES), "--set", "relay.dedicated=true"],
        ["-f", str(KIND_VALUES), "--set", "site.enabled=true", "--set", "site.tls.mode=acme",
         "--set", "site.tls.domains={openarcanum.example}"],
    ):  # fmt: skip
        for d in render(*values):
            if "containers:" not in d:
                continue
            # Each server's container (a dedicated relay shares the
            # community server's pod).
            servers = [s for s in known if re.search(rf"^\s+- name: {s}$", d, re.M)]
            assert servers, d[:200]
            for server in servers:
                names = set(container_env(d, server))
                assert names, server
                unknown = names - known[server]
                assert not unknown, f"{server}: {sorted(unknown)}"


def test_dedicated_relay_runs_next_to_the_community_server():
    """relay.dedicated (docs/kubernetes.md "A dedicated relay"): the relay
    moves into the community server's pod, reads the key file the server
    writes to a volume they share, keeps its blocks on a volume claim, and
    registers with no maintainer node; the relay Service follows it."""
    url = "wss://relay.example.invalid/ws"
    docs = render("--set", f"relay.publicURL={url}", "--set", "relay.dedicated=true")
    kinds = [re.search(r"^kind: (\w+)", d, re.M).group(1) for d in docs]
    assert "Deployment" in kinds and not any(
        re.search(r"^  name: openarcanum-relay$", d, re.M) for d in docs if "kind: Deployment" in d
    )
    sts = doc(docs, "StatefulSet", "openarcanum-community")
    relay = container_env(sts, "relay")
    community = container_env(sts, "community")
    assert community["OPENARCANUM_COMMUNITY_SERVER_PUBLIC_KEY_FILE"] == "/serverkey/server.pub"
    assert relay["OPENARCANUM_RELAY_DEDICATED_SERVER_KEY_FILE"] == "/serverkey/server.pub"
    assert relay["OPENARCANUM_RELAY_BLOCK_FILE"] == "/relay-state/blocks.json"
    assert "OPENARCANUM_RELAY_REGISTRY_URLS" not in relay
    # The community is pinned to this relay only.
    assert community["OPENARCANUM_COMMUNITY_RELAYS"] == url
    assert "{name: serverkey, mountPath: /serverkey, readOnly: true}" in sts
    assert "{name: serverkey, mountPath: /serverkey}" in sts
    assert "name: relay-state" in sts and "storage: 64Mi" in sts
    svc = doc(docs, "Service", "openarcanum-relay")
    assert "app.kubernetes.io/component: community" in svc.split("selector:", 1)[1]
    # Off by default: the relay keeps its own Deployment and registers.
    plain = render("--set", f"relay.publicURL={url}")
    assert "OPENARCANUM_RELAY_DEDICATED" not in "".join(plain)
    assert "OPENARCANUM_RELAY_REGISTRY_URLS" in doc(plain, "Deployment", "openarcanum-relay")
    # It needs the chart's community server, pinned to it alone.
    for bad in (["--set", "community.enabled=false"], ["--set", "community.relays={wss://other.example.invalid/ws}"]):
        r = helm(
            "template", "openarcanum", str(CHART), "--set", f"relay.publicURL={url}", "--set", "relay.dedicated=true",
            *bad, check=False,
        )  # fmt: skip
        assert r.returncode != 0 and "relay.dedicated" in r.stderr, (bad, r.stderr)
    r = helm("lint", "--strict", str(CHART), "-f", str(KIND_VALUES), "--set", "relay.dedicated=true", check=False)
    assert r.returncode == 0, r.stdout + r.stderr


def container_env(d: str, container: str) -> dict[str, str]:
    """The env of one container in a rendered object."""
    block = d.split(f"- name: {container}\n", 1)[1]
    block = block.split("\n        - name: ", 1)[0]
    return dict(re.findall(r"- name: (OPENARCANUM_[A-Z0-9_]+)\n\s+value: \"?([^\"\n]*)\"?", block))


def init_args(d: str) -> list[str]:
    block = d.split("args:\n", 1)[1].split("volumeMounts:", 1)[0]
    return [a.strip().strip('"') for a in re.findall(r"^\s+- (.+)$", block, re.M)]


def test_servers_accept_what_the_kind_values_render(tmp_path):
    """The real binaries, started with the environment and arguments the
    chart renders for kind (ports and paths moved to this test's), accept
    them; a client claims the community through the one relay URL and
    posts; the claim file is gone once claimed."""
    from conftest import PASS, Events, Proc, binary, free_port, run, wait_port
    from test_e2e_community import WIZARD, general

    from openarcanum.core import Core
    from openarcanum.paths import Paths

    rp, mp = free_port(), free_port()
    relay_url = f"ws://127.0.0.1:{rp}/ws"
    meta_url = f"http://127.0.0.1:{mp}"
    docs = render(
        "-f", str(KIND_VALUES),
        "--set", f"relay.port={rp}", "--set", f"relay.mediaUDPPort={rp}",
        "--set", f"relay.publicURL={relay_url}", "--set", f"meta.port={mp}",
    )  # fmt: skip
    svc = f"http://openarcanum-meta.openarcanum.svc.cluster.local:{mp}"
    local = lambda env: {k: v.replace(svc, meta_url) for k, v in env.items()}  # noqa: E731

    meta_env = local(container_env(doc(docs, "Deployment", "openarcanum-meta"), "meta"))
    meta_env["OPENARCANUM_META_DATA_DIR"] = str(tmp_path / "meta")
    (tmp_path / "meta").mkdir()
    relay_env = local(container_env(doc(docs, "Deployment", "openarcanum-relay"), "relay"))
    relay_env["OPENARCANUM_RELAY_REGISTRY_KEY_FILE"] = str(tmp_path / "registry.key")
    sts = doc(docs, "StatefulSet", "openarcanum-community")
    data, keys = tmp_path / "data", tmp_path / "keys"
    keys.mkdir()
    # The image sets this; the chart relies on it.
    image_env = {"OPENARCANUM_COMMUNITY_STORAGE_KEY_FILE": str(keys / "storage.key")}
    args = [a.replace("/data", str(data)).replace("/keys", str(keys)).replace(svc, meta_url) for a in init_args(sts)]
    assert args[:2] == ["init", "-if-needed"]

    procs = []
    try:
        procs.append(Proc([str(binary("meta"))], env=meta_env))
        wait_port(mp)
        procs.append(Proc([str(binary("relay"))], env=relay_env))
        wait_port(rp)
        for _ in range(2):  # the init container runs on every start
            r = subprocess.run(
                [str(binary("community")), *args], capture_output=True, text=True, env={**os.environ, **image_env}
            )
            assert r.returncode == 0, r.stderr
        claim = (keys / "claim.txt").read_text(encoding="utf-8").strip()
        assert claim in r.stdout and "already initialized" in r.stdout
        community_env = {**image_env, **local(container_env(sts, "community"))}
        assert community_env["OPENARCANUM_COMMUNITY_RELAYS"] == relay_url
        procs.append(Proc([str(binary("community")), "run", "-data", str(data)], env=community_env))

        async def main():
            core = Core(Paths.under(tmp_path / "olive"), emit=Events())
            await core.create_profile(PASS, "olive")
            await core.set_setting("maintainers", [meta_url])
            cid = (await core.claim(claim, WIZARD, "Olive"))["community"]
            ch = await general(core, cid)
            await core.send(cid, ch, "hello from the kind values")
            texts = [m["text"] for m in (await core.history(cid, ch))["messages"]]
            assert "hello from the kind values" in texts
            await core.shutdown()

        run(main(), 90)
        assert not (keys / "claim.txt").exists()
    finally:
        for p in reversed(procs):
            p.stop()


# ---------------------------------------------------------------- maintainer clusters (docs/kubernetes.md)

KIND_SH = ROOT / "scripts" / "kind.sh"
OP_KEY = "A" * 43
ROOT_KEY, M2_KEY = "R" * 43, "M" * 43


def cluster_values(tmp_path) -> Path:
    """What `KIND_META_CLUSTER=1 make kind-up` installs on top of the kind
    values (scripts/kind.sh cluster-values: the same function it uses)."""
    out = subprocess.run(
        ["bash", str(KIND_SH), "cluster-values", ROOT_KEY, M2_KEY, OP_KEY], capture_output=True, text=True, check=True
    ).stdout
    p = tmp_path / "cluster-values.yaml"
    p.write_text(out)
    return p


@pytest.mark.skipif(sys.platform == "win32", reason="the kind script runs on Linux and macOS")
def test_kind_script_parses():
    r = subprocess.run(["bash", "-n", str(KIND_SH)], capture_output=True, text=True)
    assert r.returncode == 0, r.stderr
    make = (ROOT / "Makefile").read_text(encoding="utf-8")
    for target, cmd in (("kind-partition", "partition"), ("kind-heal", "heal"), ("kind-failover", "failover")):
        assert re.search(rf"^{target}:.*\n\tscripts/kind.sh {cmd}$", make, re.M), target
        assert re.search(rf"^\s+{cmd}\) cmd_{cmd} ;;$", KIND_SH.read_text(encoding="utf-8"), re.M), cmd


@pytest.mark.skipif(sys.platform == "win32", reason="the kind script runs on Linux and macOS")
def test_meta_nodes_render_a_cluster(tmp_path):
    """meta.nodes: each node its own volume claim, Service and Deployment,
    with its key Secret, the cluster's operator key and signed member list,
    and its own settings; the relay and the community server reach every
    node; the kind cluster mode renders the two nodes trusting each other
    through their Services."""
    docs = render("-f", str(KIND_VALUES), "-f", str(cluster_values(tmp_path)))
    for node, other_key, other in (("root", M2_KEY, "m2"), ("m2", ROOT_KEY, "root")):
        name = f"openarcanum-meta-{node}"
        doc(docs, "PersistentVolumeClaim", name)
        svc = doc(docs, "Service", name)
        assert f"meta-node: {node}" in svc
        dep = doc(docs, "Deployment", name)
        e = container_env(dep, "meta")
        assert e["OPENARCANUM_META_CLUSTER_OPERATOR_KEY"] == OP_KEY
        assert e["OPENARCANUM_META_MAINTAINER_SET_FILE"] == "/maintainer-set/set.bin"
        assert e["OPENARCANUM_META_NODE_KEY_DIR"] == "/node-key"
        assert (
            e["OPENARCANUM_META_TRUSTED_PEERS"]
            == f"http://openarcanum-meta-{other}.openarcanum.svc.cluster.local:7100#{other_key}"
        )
        assert e["OPENARCANUM_META_WRITES_PER_MINUTE"] == "60000"  # meta.env still applies
        assert f"secretName: openarcanum-meta-{node}-key" in dep and "secretName: openarcanum-meta-set" in dep
        assert "{name: node-key, mountPath: /node-key, readOnly: true}" in dep
        assert f"claimName: {name}" in dep and "replicas: 1" in dep
    assert (
        container_env(doc(docs, "Deployment", "openarcanum-meta-m2"), "meta")["OPENARCANUM_META_FOLLOW_DECISIONS"]
        == ROOT_KEY
    )
    assert not any(re.search(r"^  name: openarcanum-meta$", d, re.M) for d in docs)
    urls = "http://openarcanum-meta-root.openarcanum.svc.cluster.local:7100,http://openarcanum-meta-m2.openarcanum.svc.cluster.local:7100"
    assert env(doc(docs, "Deployment", "openarcanum-relay"), "OPENARCANUM_RELAY_REGISTRY_URLS") == urls
    assert env(doc(docs, "StatefulSet", "openarcanum-community"), "OPENARCANUM_COMMUNITY_MAINTAINERS") == urls
    known = known_env()["meta"]
    for d in docs:
        if re.search(r"^\s+- name: meta$", d, re.M):
            assert not set(container_env(d, "meta")) - known
    r = helm("lint", "--strict", str(CHART), "-f", str(KIND_VALUES), "-f", str(cluster_values(tmp_path)), check=False)
    assert r.returncode == 0, r.stdout + r.stderr


@pytest.mark.skipif(sys.platform == "win32", reason="the kind script runs on Linux and macOS")
def test_meta_nodes_values_are_checked(tmp_path):
    base = ["template", "openarcanum", str(CHART), "-f", str(KIND_VALUES)]
    for bad in (
        ["--set", "meta.nodes[0].name=Root"],
        ["--set", "meta.nodes[0].name=root", "--set", "meta.nodes[0].role=boss"],
        ["--set", "meta.cluster.operatorKey=short"],
        ["--set", "meta.nodes[0].name=root", "--set", "meta.nodes[0].env.PATH=/x"],
    ):
        r = helm(*base, *bad, check=False)
        assert r.returncode != 0 and "schema" in r.stderr.lower(), (bad, r.stderr)
    r = helm(*base, "--set", "meta.nodes[0].name=a", "--set", "meta.nodes[1].name=a", check=False)
    assert r.returncode != 0 and "used twice" in r.stderr
    # Without nodes: one node, named as before.
    docs = render("-f", str(KIND_VALUES))
    dep = doc(docs, "Deployment", "openarcanum-meta")
    assert "meta-node" not in dep and "OPENARCANUM_META_NODE_KEY_DIR" not in dep


# ---------------------------------------------------------------- TLS for browsers (docs/kubernetes.md)


def test_tls_acme_needs_only_domains():
    """relay.tls / meta.tls mode acme (docs/kubernetes.md "TLS for
    browsers"): the domains are all it takes. The relay's URL becomes
    wss://<domain>/ws, the chart's servers reach the maintainer node at
    https://<domain>, the Services listen on 443, the probes use HTTPS, the
    relay keeps its certificates on a volume claim; the kind values stay
    plain."""
    docs = render(
        "--set", "relay.tls.mode=acme", "--set", "relay.tls.domains={relay.example.invalid}",
        "--set", "meta.tls.mode=acme", "--set", "meta.tls.domains={maintainer.example.invalid}",
    )  # fmt: skip
    relay = container_env(doc(docs, "Deployment", "openarcanum-relay"), "relay")
    assert relay["OPENARCANUM_RELAY_PUBLIC_URL"] == "wss://relay.example.invalid/ws"
    assert (
        relay["OPENARCANUM_RELAY_TLS_MODE"] == "acme"
        and relay["OPENARCANUM_RELAY_TLS_DOMAINS"] == "relay.example.invalid"
    )
    assert relay["OPENARCANUM_RELAY_ACME_CACHE_DIR"] == "/acme/cache"
    assert relay["OPENARCANUM_RELAY_REGISTRY_URLS"] == "https://maintainer.example.invalid"
    meta = container_env(doc(docs, "Deployment", "openarcanum-meta"), "meta")
    assert meta["OPENARCANUM_META_TLS_MODE"] == "acme" and "OPENARCANUM_META_ACME_CACHE_DIR" not in meta  # DATA/acme
    community = container_env(doc(docs, "StatefulSet", "openarcanum-community"), "community")
    assert community["OPENARCANUM_COMMUNITY_RELAYS"] == "wss://relay.example.invalid/ws"
    assert community["OPENARCANUM_COMMUNITY_MAINTAINERS"] == "https://maintainer.example.invalid"
    for kind, name in (("Service", "openarcanum-relay"), ("Service", "openarcanum-meta")):
        assert "port: 443" in doc(docs, kind, name)
    for name in ("openarcanum-relay", "openarcanum-meta"):
        assert doc(docs, "Deployment", name).count("scheme: HTTPS") == 2
    doc(docs, "PersistentVolumeClaim", "openarcanum-relay-acme")
    assert "claimName: openarcanum-relay-acme" in doc(docs, "Deployment", "openarcanum-relay")
    known = known_env()
    for server, d in (
        ("relay", doc(docs, "Deployment", "openarcanum-relay")),
        ("meta", doc(docs, "Deployment", "openarcanum-meta")),
    ):
        assert not set(container_env(d, server)) - known[server]
    r = helm(
        "lint", "--strict", str(CHART), "--set", "relay.tls.mode=acme", "--set", "relay.tls.domains={203.0.113.7}",
        check=False,
    )  # fmt: skip
    assert r.returncode == 0, r.stdout + r.stderr
    # The kind values (and the defaults) stay plain.
    for d in render("-f", str(KIND_VALUES)):
        assert "TLS_MODE" not in d and "scheme: HTTPS" not in d and "port: 443" not in d


def test_tls_files_and_checks():
    """mode files mounts a kubernetes.io/tls Secret (also into a dedicated
    relay next to the community server); an IPv6 domain gets brackets in
    the URL; acme without domains and files without a Secret are refused."""
    docs = render(
        "--set", "relay.tls.mode=files", "--set", "relay.tls.secretName=relay-tls",
        "--set", "relay.tls.domains={2001:db8::7}", "--set", "relay.dedicated=true",
        "--set", "meta.tls.mode=files", "--set", "meta.tls.secretName=meta-tls",
        "--set", "meta.tls.domains={maintainer.example.invalid}",
    )  # fmt: skip
    sts = doc(docs, "StatefulSet", "openarcanum-community")
    relay = container_env(sts, "relay")
    assert relay["OPENARCANUM_RELAY_PUBLIC_URL"] == "wss://[2001:db8::7]/ws"
    assert (
        relay["OPENARCANUM_RELAY_TLS_CERT_FILE"] == "/tls/tls.crt"
        and relay["OPENARCANUM_RELAY_TLS_KEY_FILE"] == "/tls/tls.key"
    )
    assert "secretName: relay-tls" in sts and "{name: relay-tls, mountPath: /tls, readOnly: true}" in sts
    meta = doc(docs, "Deployment", "openarcanum-meta")
    assert "secretName: meta-tls" in meta and container_env(meta, "meta")["OPENARCANUM_META_TLS_MODE"] == "files"
    url = ["--set", "relay.publicURL=wss://relay.example.invalid/ws"]
    for bad, msg in (
        (["--set", "relay.tls.mode=acme"], "relay.tls.domains"),
        (["--set", "relay.tls.mode=files", *url], "relay.tls.secretName"),
        (["--set", "meta.tls.mode=acme", *url], "meta.tls.domains"),
        (["--set", "relay.tls.mode=on", *url], "schema"),
        (["--set", "relay.tls.domains={https://relay.example.invalid}", *url], "schema"),
    ):
        r = helm("template", "openarcanum", str(CHART), *bad, check=False)
        assert r.returncode != 0 and msg in r.stderr, (bad, r.stderr)


def test_site_is_off_by_default_and_keeps_its_key_apart():
    """The website (docs/operations.md "Website") renders only when enabled:
    its database and its key on separate claims, probes through the binary
    (the status listener is on loopback), TLS settings passed through."""
    url = ["--set", "relay.publicURL=wss://relay.example.invalid/ws"]
    assert not any("component: site" in d for d in render(*url))
    site = ["--set", "site.enabled=true", "--set", "site.tls.mode=acme"]
    docs = render(*url, *site, "--set", "site.tls.domains={openarcanum.example}")
    dep = doc(docs, "Deployment", "openarcanum-site")
    e = container_env(dep, "site")
    assert e["OPENARCANUM_SITE_DATA_DIR"] == "/data" and e["OPENARCANUM_SITE_STORAGE_KEY_FILE"] == "/keys/storage.key"
    assert e["OPENARCANUM_SITE_TLS_MODE"] == "acme" and e["OPENARCANUM_SITE_TLS_DOMAINS"] == "openarcanum.example"
    assert "claimName: openarcanum-site\n" in dep and "claimName: openarcanum-site-keys" in dep
    assert '"/usr/local/bin/site", "healthcheck"' in dep
    assert "port: 443" in doc(docs, "Service", "openarcanum-site")
    r = helm("template", "openarcanum", str(CHART), *url, *site, check=False)
    assert r.returncode != 0 and "site.tls.domains" in r.stderr
