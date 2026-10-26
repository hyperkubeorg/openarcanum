# Running the stack on Kubernetes

The Helm chart in `deploy/helm/openarcanum` runs the three servers: a
maintainer node (directory, relay registry, DM mailboxes and blob store), a
relay (WebSocket routing and the call SFU) and a community server. For
development there's a local kind cluster with Makefile targets, and
`scripts/dev-client` runs as many copies of the app as you like against it.

## The local cluster (kind)

### What you need on your computer

These run on your computer, not inside a container (kind needs the
host's container engine):

- **rootless podman**, with cgroups v2;
- **kind** 0.20 or later, **kubectl** and **helm** 3;
- a desktop session (X11 or Wayland) for the app copies; for calls with
  real devices, PipeWire (or PulseAudio) and a camera.

`scripts/kind.sh` tells you what's missing. It runs kind with
`KIND_EXPERIMENTAL_PROVIDER=podman`, and on a systemd host creates the
cluster in a user scope with cgroup delegation, as kind's rootless podman
instructions describe.

### Start it

In three terminals, from the project directory:

```bash
make kind-up                 # cluster, server images, chart; prints the claim code
make kind-proxy              # leave running: relay and maintainer node on 127.0.0.1
make client-image            # once (again after clients/desktop/uv.lock changes)
make client NAME=alice       # a copy of the app; another terminal: make client NAME=bob
```

1. In Alice's window, create a profile, then **+ → Host on your own server**
   and paste the claim code (`make kind-claim` shows it again).
2. Make an invite and paste it in Bob's window. The maintainer node runs
   without a public directory, as it does by default; to try directory
   search, add `OPENARCANUM_META_DIRECTORY_ENABLED: "true"` to `meta.env` in
   `values-kind.yaml`.
3. Calls work between the copies too: the relay's media port is mapped to
   127.0.0.1.

After changing server code, run `make kind-up` again: it rebuilds the
images under a new tag and upgrades the release, which restarts the pods.
Data (the community, the directory, mailboxes) is kept in the cluster's
volumes until `make kind-down`.

| Target | What it does |
|---|---|
| `make kind-up` | Creates the cluster `openarcanum` if needed, builds the three images from `deploy/container/Containerfile` with podman, loads them into kind, installs or upgrades the chart with `values-kind.yaml`, prints the claim code |
| `make kind-proxy` | Port-forwards the relay to `127.0.0.1:7000` and the maintainer node to `127.0.0.1:7100` (restarting a forward when its pod restarts); Ctrl+C stops it |
| `make kind-claim` | The community server's claim code |
| `make kind-status` | Pods, services and volumes |
| `make kind-down` | Deletes the cluster and everything in it |

### The app copies

```bash
make client-image                    # the app image (deploy/container/Containerfile.client)
make client NAME=alice [ARGS="--maintainer URL --fake-media --no-media"]
scripts/dev-client NAME [--maintainer URL] [--fake-media | --no-media] [-- APP ARGS]   # the same, with app options
scripts/dev-client --list
scripts/dev-client --stop NAME
scripts/dev-client --reset NAME      # deletes that copy's data (asks first)
```

Each copy keeps its profile, keys and settings in `.dev/clients/NAME` in
this project (git-ignored), so it survives restarts and copies don't share
anything. It runs in its own rootless podman container from the app image,
with your display (X11 or Wayland) and the host's network, which is how it
reaches `make kind-proxy` on 127.0.0.1.

The image holds the app's runtime only: Python, the dependencies locked in
`clients/desktop/uv.lock`, and the libraries Qt WebEngine needs. The app's code is
mounted read-only from `clients/desktop/src` (and its web UI from
`clients/shared/ui`) when a copy starts, so code changes
take effect at the next start without a rebuild. The interpreter is the
one `clients/desktop/.python-version` pins (the build checks it). After `clients/desktop/uv.lock`,
`clients/desktop/.python-version` or the Containerfile change, `make client` says to
run `make client-image` again.

New profiles use the maintainer node at `http://127.0.0.1:7100` (the
proxy). The app's first-run and unlock screens show the maintainer nodes in
use and let you change them (**Change…**); `--maintainer` only sets what a
profile without its own list uses. `-- APP ARGS` passes options to the app,
e.g. `-- --debug` for DevTools.

#### Voice and video in the copies

A copy gets this computer's sound and cameras, so calls between copies (or
with another computer) use real devices:

- **Sound**: the PulseAudio socket in `$XDG_RUNTIME_DIR/pulse/native`
  (PipeWire provides it with `pipewire-pulse`, the default on Arch) and
  PipeWire's own socket `$XDG_RUNTIME_DIR/pipewire-0` are mounted into the
  container's runtime folder, with `PULSE_SERVER` and
  `PIPEWIRE_RUNTIME_DIR` set for them; `PULSE_SERVER=unix:…` in your
  environment picks another socket, and a PulseAudio cookie
  (`~/.config/pulse/cookie` or `$PULSE_COOKIE`) goes along if there is one.
  The image has the clients: `libpulse`, and `pipewire-alsa` so ALSA reaches
  PipeWire when there's no PulseAudio socket.
- **Cameras**: every `/dev/video*` is passed through (`--device`). Your
  desktop session gives your user access to them; check with
  `v4l2-ctl --list-devices` or any camera app on the host.

The start line says what the copy got (e.g. `sound: PulseAudio and PipeWire;
cameras: /dev/video0`). The app's own media policy still applies: it asks for
the microphone and camera only in a call or a device test, as everywhere.
Run `make client-image` again after updating this project; `make client`
says when the image is out of date.

Two options change this:

- `--fake-media` (`make client NAME=alice ARGS="--fake-media"`): Chromium's
  fake devices instead of yours, a camera with a moving test pattern and a
  microphone that beeps (`--use-fake-device-for-media-stream` in
  `QTWEBENGINE_CHROMIUM_FLAGS`, the flag the call tests use). Several copies
  on one computer can then all be in a call with "their own" camera and
  microphone; sound still plays through your speakers. This is not
  `--use-fake-ui-for-media-stream` (which grants access without asking):
  the app drops that one, so its permission prompts and screen picker work
  as in a release.
- `--no-media`: no sound server, no cameras (a copy with neither).

### The app on your desktop

To check something in the app itself on this computer, without a container:

```bash
make desktop                         # from clients/desktop/src with uv; profile in .dev/desktop/me
make desktop NAME=bob ARGS="--debug" # another profile; ARGS go to the app
make desktop FAKE_MEDIA=1            # Chromium's fake camera and microphone
make desktop MAINTAINER=http://127.0.0.1:7100   # the default (kind proxy or dev-up.sh)
make desktop-appimage                # the AppImage (built with make appimage if missing)
make desktop-appimage REBUILD=1      # build it again first
```

Both run the app with `--home .dev/desktop/NAME` (default `me`), so its
profile, keys, settings and logs, and Qt's, fontconfig's and Mesa's caches,
stay in this project: `rm -rf .dev/desktop/NAME` or `make clean-dev`
removes them. The one thing kept elsewhere is the key for staying signed
in, which a new profile keeps in your desktop keychain (or in `unlock-key`
inside the profile's folder when there is no keychain). Turn on **Ask for my
passphrase when OpenArcanum starts** (Settings → Security) before deleting
the folder, and the keychain entry goes too. The app uses your sound,
microphone and cameras directly.
`scripts/desktop.sh --help` has the options; `-n` prints the command
without running it.

`make desktop` runs `uv run --project clients/desktop --locked python -m openarcanum …`.
It needs:

- **uv** (Arch: `sudo pacman -S uv`). uv installs the Python
  `clients/desktop/.python-version` names (3.14) by itself, and the dependencies into
  `clients/desktop/.venv`.
- **The system libraries Qt WebEngine loads** (PySide6's wheels bring Qt and
  Chromium, not these). On Arch:

  ```bash
  sudo pacman -S --needed nss nspr alsa-lib libpulse fontconfig freetype2 dbus mesa libglvnd \
      libxkbcommon libxkbcommon-x11 libxcomposite libxdamage libxrandr libxtst \
      libxkbfile libxshmfence libxcursor libxi \
      xcb-util xcb-util-cursor xcb-util-image xcb-util-keysyms xcb-util-renderutil xcb-util-wm
  ```

  (the list in `deploy/container/Containerfile.client`; a desktop install
  has most of them). The script checks them with `ldconfig` before
  starting and names the missing ones and their packages;
  `OPENARCANUM_SKIP_LIB_CHECK=1` skips the check.

`make desktop-appimage` builds `clients/desktop/dist/OpenArcanum-x86_64.AppImage` with
`make appimage` (uv and network access for the pinned AppImage tools;
`docs/packaging.md`) the first time and reuses it after that. The AppImage
mounts itself with FUSE (`fuse3`, or `fuse2`); without `/dev/fuse` or
`fusermount`, the script runs it with `APPIMAGE_EXTRACT_AND_RUN=1`
(the `--appimage-extract-and-run` mode: it unpacks to a temporary folder
each start).

### Cleaning up

| Target | Removes |
|---|---|
| `make clean` | Build output and caches: `bin/`, `dist/`, `clients/desktop/build`, `clients/desktop/dist` (bundle, AppImage), `load-results.jsonl`, pytest/ruff/Hypothesis caches, `__pycache__` |
| `make clean-dev` | All of `.dev/`: the app copies' and desktop profiles, and `scripts/dev-up.sh`'s server data. Asks first; refuses while an app copy or a `make desktop` run is using it. `FORCE=1` skips the question and stops those first |
| `make clean-images` | The podman images this project built: `localhost/openarcanum-*` (the app image and every `kind-up` server image tag) |
| `make clean-all` | `kind-down`, `clean-dev`, `clean-images`, `clean`, and `clients/desktop/.venv` |

Each target prints what it removes, does nothing when there's nothing left,
and never touches anything outside the project directory
(`scripts/clean.sh` refuses such paths, including through symlinks).

### How the networking fits together

```
your computer (127.0.0.1)                kind node (podman container)
  app copies (host network)
    ws://127.0.0.1:7000/ws ──┐
    http://127.0.0.1:7100 ───┤ make kind-proxy ──► relay (node network, :7000)
                             │   (port-forward)  ► maintainer node (pod, :7100)
    media 127.0.0.1:7000/udp ─── kind port mapping ► relay's SFU (:7000/udp)
                                                    community server (node network)
                                                      └► ws://127.0.0.1:7000/ws
```

The relay URL is the one thing that has to be the same everywhere: the
relay registers it with the maintainer node, and the community server both
attaches to it and puts it in its claim code and route, which is where
clients find it. The relay and the community server run on the node's
network (`hostNetwork`), so `ws://127.0.0.1:7000/ws` reaches the relay from
the community server just as it does from the app copies through the proxy.
Call media is UDP, which port-forwards can't carry, so kind maps
`127.0.0.1:7000/udp` straight to the relay, which advertises 127.0.0.1 as
its media address. Everything else stays inside the cluster: the servers
reach the maintainer node by its service name.

Because every client reaches the cluster from the proxy's one address, the
kind values raise the maintainer node's per-address write limits and turn
the relay's proof-of-work off (it would treat all clients as one).

### Troubleshooting

- **`kind create cluster` fails under rootless podman**: check cgroups v2
  (`stat -fc %T /sys/fs/cgroup` says `cgroup2fs`) and that your user
  session delegates cgroup controllers (kind's "Rootless" docs).
- **Port 7000 or 7100 in use**: something else listens there (e.g.
  `scripts/dev-up.sh`); stop it. The kind mapping needs 7000/udp, the proxy
  7000/tcp and 7100/tcp.
- **A copy says it can't reach a maintainer node**: is `make kind-proxy`
  running? `make kind-status` shows whether the pods are ready; their logs:
  `kubectl -n openarcanum logs deploy/openarcanum-relay` (or `openarcanum-meta`,
  `statefulset/openarcanum-community`).
- **No window**: the launcher needs `DISPLAY` or `WAYLAND_DISPLAY` in its
  environment.
- **No sound in a call**: the copy's start line says whether it found a
  sound socket. `pactl info` on the host should answer (PipeWire needs
  `pipewire-pulse` running for that: `systemctl --user status
  pipewire-pulse.socket`). Without one, ALSA through `pipewire-0` is used.
- **No camera**: `ls -l /dev/video*` on the host; your user needs access
  (the desktop session grants it). `--fake-media` tests calls without one.

## The chart on a real cluster

Turn on TLS for the relay and the maintainer node (`relay.tls`,
`meta.tls`, see "TLS for browsers" below), or set `relay.publicURL` to the
relay's public address (`wss://…`, with TLS terminated in front of port
7000), and give the relay a way to receive UDP
on `relay.mediaUDPPort`: `relay.hostNetwork: true` on a node with a public
address (and `relay.mediaPublicIPs` if it's behind NAT) is the simplest.
The community server needs no inbound port. `values.yaml` lists every value;
`values.schema.json` checks them. More server settings go in each server's
`env` map as `OPENARCANUM_*` variables (docs/operations.md).

- Images: by default the released ones,
  `ghcr.io/hyperkubeorg/openarcanum-{relay,maintainer,community}` at the
  chart's `appVersion` (the release the chart came with; `image.names`
  maps each server to its image). Pin another release with
  `--set image.tag=2026.10.0`; use your own registry with
  `image.repository` (and `image.names.meta=meta` for images named after
  the Containerfile targets, as `values-kind.yaml` does).

- Volumes: the maintainer node's database and blobs, and the community
  server's data and storage key (in separate volumes: backups in the data
  volume can't be read without the key volume). Back up the key volume
  somewhere else (docs/operations.md "Backups").
- Every container runs as 65532 with a read-only root filesystem, no
  privilege escalation and no capabilities.
- The community server's claim code is in its init container's log, and in
  `/keys/claim.txt` until the server is claimed (it deletes the file then).

`clients/desktop/tests/test_helm.py` lints the chart, checks the schema and checks
what the kind values render.

### TLS for browsers

The web client runs in a browser on an HTTPS page, so it can only reach
`wss://` relays and `https://` maintainer nodes. The servers get their own
certificates from Let's Encrypt (docs/operations.md "TLS and browsers");
the chart needs only the names:

```bash
helm install openarcanum deploy/helm/openarcanum \
  --set relay.tls.mode=acme --set relay.tls.domains={relay.example.invalid} \
  --set meta.tls.mode=acme --set meta.tls.domains={maintainer.example.invalid}
```

- The relay's URL becomes `wss://relay.example.invalid/ws` (no
  `relay.publicURL` needed), and the chart's relay and community server
  reach the maintainer node at `https://maintainer.example.invalid`: the
  certificate names the public domain, not the in-cluster Service.
- The Services listen on 443 (to the servers' own ports). Expose them as
  `LoadBalancer` Services or forward port 443 of each name's address to
  them: the CA checks each name on port 443 (tls-alpn-01; no port 80).
  With `relay.hostNetwork: true` the relay listens on the node's
  `relay.port`, so forward the node's 443 to it (the containers have no
  capability to bind 443 themselves).
- The relay keeps its ACME account and certificates on a small volume claim
  (`<release>-relay-acme`, `relay.tls.storage`); a dedicated relay keeps
  them on its block-list volume; maintainer nodes keep them on their data
  volume (`/data/acme`).
- `relay.tls.domains` may hold the relay's public IP address instead of a
  name (a six-day certificate, renewed by the relay; docs/operations.md).
- In a maintainer cluster, give each node its own name with
  `meta.nodes[].tlsDomains` (`meta.tls.domains` is the default).
- `mode: files` with `secretName` uses a `kubernetes.io/tls` Secret instead
  (cert-manager's, for example); the servers pick up a renewed Secret
  without a restart.
- The probes switch to HTTPS; the maintainer node answers CORS for any
  origin by default (`OPENARCANUM_META_CORS_ORIGINS` in `meta.env` narrows
  it).

The kind values stay plain (`ws://`, `http://`): on kind the chart's own
servers would have to trust a local certificate too, and the images trust
only the public CAs. Try TLS locally with the binaries instead, in files
mode with a certificate for `127.0.0.1` from mkcert (which browsers then
trust) or a self-signed one (docs/operations.md "Testing TLS locally").

### The website

The chart can also run the project website (docs/operations.md
"Website"), off by default: `--set site.enabled=true`, with
`site.tls.mode=acme` and `site.tls.domains={openarcanum.org}` when its
Service is reachable on port 443 (or `off` behind a TLS-terminating
ingress, then with `OPENARCANUM_SITE_TRUSTED_PROXY_HEADER` in `site.env` so
rate limits see clients rather than the ingress). Its database and the key
that seals reports get separate claims. Create the first admin in the pod:
`kubectl exec -it deploy/<release>-site -- site admin add NAME`.

## A maintainer cluster

`meta.nodes` runs several maintainer nodes that replicate DM mailboxes,
blob records, followed moderation decisions and community tombstones
between them (docs/operations.md "Maintainer clusters"), so any of them
can stand in for another. Each entry is one node with its own volume
claim, Service and Deployment, `<release>-meta-<name>` (one replica each:
a node is one SQLite database; more nodes are more entries):

```yaml
meta:
  nodes:
    - name: root
      role: root
      nodeKeySecret: openarcanum-meta-root-key   # node.key and node.noise
    - name: m2
      nodeKeySecret: openarcanum-meta-m2-key
      env:
        OPENARCANUM_META_FOLLOW_DECISIONS: "<the root's node key>"
  cluster:
    operatorKey: "<base64url operator key>"   # `meta operator keygen`
    setSecret: openarcanum-meta-set              # set.bin, `meta operator sign-set`
```

- Make each node's keys with `meta id -data DIR` (it prints the node key)
  and store `node.key` and `node.noise` in the node's Secret; the node reads
  them from there (`node_key_dir`) and never makes new ones. Without a
  Secret the node makes its keys on its volume at first start (`meta id`
  in the pod prints them).
- Sign the member list with the operator key, naming each node's URL as
  clients reach it, and store `set.bin` in `cluster.setSecret`; every node
  publishes it at start (a higher `-seq` replaces it). Keep the operator key
  off the cluster.
- The nodes reach each other at the URLs in the member list. If those are
  public addresses the pods can't use, give each node
  `OPENARCANUM_META_TRUSTED_PEERS` with the others' Service URLs
  (`http://<release>-meta-<name>.<namespace>.svc.cluster.local:7100#<node
  key>`): those take precedence.
- The relay and the community server publish to every node's Service by
  default (`relay.registryURLs` and `community.maintainers` empty).
- For real high availability the nodes must not share a failure: run one
  release per cluster or region with the same `meta.cluster` values and
  each region's nodes in `meta.nodes`, and list every member in the signed
  set. Members in other releases are reached at their public URLs.

### The kind cluster mode

`KIND_META_CLUSTER=1 make kind-up` installs two nodes, `meta-root` and
`meta-m2`: `scripts/kind.sh` makes their keys and an operator key with
`meta` inside its image (kept in `.dev/kind-meta-cluster/`, reused on the
next `kind-up`), signs a member list naming `http://127.0.0.1:7100` (root)
and `http://127.0.0.1:7102` (m2), creates the Secrets and installs with
two `meta.nodes` that trust each other through their Services. `make
kind-proxy` forwards m2 to 127.0.0.1:7102 too. In the app, add
`http://127.0.0.1:7100` and accept its network (the script prints the
`URL#KEY` form too). Then:

- `make kind-partition`: a NetworkPolicy cuts the nodes off from each
  other (clients, the relay and the community server still reach both;
  it needs a network plugin that enforces NetworkPolicy, as kindnet does
  from kind 0.24). DMs sent through one node reach a recipient reading the
  other on its next full read (within a minute).
- `make kind-heal`: removes the policy and starts the root if it was
  stopped; the nodes catch up within seconds (`kubectl exec deploy/openarcanum-meta-m2
  -- meta peers status -data /data`).
- `make kind-failover`: scales the root to 0; apps use m2 without any
  action.

`clients/desktop/tests/test_helm.py` renders the cluster mode with the values
`scripts/kind.sh cluster-values` writes.

## A dedicated relay

`relay.dedicated: true` (off by default) runs the chart's relay **dedicated
to the chart's community server**, so moderators can also block a banned
member's network address on it (docs/operations.md "Dedicated relays" says
what that does and what it can't do; docs/protocol.md §2 "Dedicated
relays" how).

The relay has to know the community server's public key, which doesn't
exist until the server's init container has created the server. The chart
solves that without any Kubernetes API access:

- The relay runs as a second container **in the community server's pod**
  (the relay Deployment isn't rendered, and the relay Service selects the
  community pod instead, so `relay.publicURL`, `make kind-proxy` and the
  claim code stay the same).
- The community server writes its public key to `/serverkey/server.pub`
  (`OPENARCANUM_COMMUNITY_SERVER_PUBLIC_KEY_FILE`) on an `emptyDir` the two
  containers share; the relay reads it from there, read-only
  (`OPENARCANUM_RELAY_DEDICATED_SERVER_KEY_FILE`), re-reading it every 10 s.
  Until the file exists the relay serves nothing, and the community server
  retries attaching, so their start order doesn't matter. After a server
  key rotation the server rewrites the file and the relay follows. The
  relay never sees the server's private key.
- The relay keeps its network blocks in `/relay-state/blocks.json` on its
  own volume claim (`relay-state`, `relay.blockStorage.size`, 64Mi), so
  they survive restarts and upgrades. The file holds IP addresses: it is
  deleted with the claim (`kubectl delete pvc relay-state-openarcanum-community-0`).
- The relay registers with no maintainer node (`registry_urls` is left
  out): nobody else can use it. The community stays pinned to it alone
  (`community.relays` must stay empty; the chart refuses otherwise), so
  every member connects through it. Device linking and live DM delivery
  use relays from the maintainers' registry, so a cluster whose only relay
  is dedicated has neither (DMs still arrive through mailboxes).

What the relay sees as a client's address is what decides what a block
covers. Behind a load balancer that hides clients' addresses (SNAT), every
client looks the same and blocks would hit everyone: expose the relay with
`relay.hostNetwork: true` on a node with a public address, or a
LoadBalancer Service with `externalTrafficPolicy: Local`. The relay never
trusts forwarded-for headers. **In the kind cluster** every client arrives
through `make kind-proxy` from 127.0.0.1, the same address the community
server uses, so the relay never blocks anything there (the moderator sees
"their address is the community server's own network"): use kind to try
the dialog and the flow, and a real cluster for the effect.

```bash
helm upgrade --install openarcanum deploy/helm/openarcanum -n openarcanum \
  --set relay.publicURL=wss://relay.example.invalid/ws --set relay.dedicated=true
```
