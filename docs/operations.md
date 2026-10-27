# Running OpenArcanum nodes

All three node binaries take a JSON config file plus environment variable
overrides (`OPENARCANUM_RELAY_*`, `OPENARCANUM_COMMUNITY_*`, `OPENARCANUM_META_*`;
the suffix is the upper-cased JSON key). Unknown JSON keys are errors.
Durations are strings like `"30s"`. Build with `make build`; binaries land in
`bin/`. For a local network use `scripts/dev-up.sh`. Release builds (static
linux/amd64 and linux/arm64 binaries, container images, systemd units) and
installing them: docs/admin-guide.md "Installing the servers". The project
website (`site`, `OPENARCANUM_SITE_*`) follows the same conventions: "Website"
below.

Every node also has the shared logging and status settings (`log_format`,
`log_level`, `log_file`, `log_max_size`, `log_max_age`, `log_keep`,
`metrics_listen`), described in "Logs, health and metrics" below, and a
`healthcheck URL` subcommand that exits 0 when URL answers 200 (for
container health checks; the images have no shell or curl). Relays and
maintainer nodes can serve TLS themselves, with certificates they get and
renew from Let's Encrypt, for browser clients: "TLS and browsers" below.

## relay

`relay [-config relay.json] [-v]`: the stateless, keyless topic router.

| Key | Default | Meaning |
|---|---|---|
| `listen` | `:7000` | address for `/ws` and `/healthz` |
| `region` | `""` | advertised in `Hello` and the registry |
| `max_chunk` | 1 MiB | largest single frame payload |
| `max_message` | 25 MiB | largest published message (protocol ceiling) |
| `frames_per_second` / `frame_burst` | 1000 / 2000 | per-connection frame rate (senders over it are slowed) |
| `bytes_per_second` / `byte_burst` | 16 MiB / 32 MiB | per-connection bandwidth |
| `send_queue_bytes` | 64 MiB | a subscriber whose queue exceeds this is disconnected |
| `max_subscriptions`, `max_streams`, `max_partial`, `max_connections` | 4096, 1024, 64, 10000 | caps |
| `write_timeout`, `ping_interval` | 15s, 20s | |
| `registry_urls` | `[]` | maintainer nodes to send heartbeats to; an entry `URL#OPERATORKEY` names a maintainer cluster: heartbeats go to every member of its verified member list (refreshed every 6 h) |
| `maintainer_operator_key` | none | the operator key for `registry_urls` entries without one (base64url) |
| `public_url` | none | required with `registry_urls`: the `ws(s)://…/ws` URL clients use; announced in canonical form (see "Relay URLs" under meta) |
| `registry_key_file` | none (fresh key per run) | the relay's identity key: heartbeats are signed with it, and every Hello proves it (docs/protocol.md §2 "Relay identity"); a fresh key per run is fine, the relay checks its own `public_url` in what servers sign |
| `heartbeat_interval` | 30s | |
| `calls` | `true` | carry calls (the SFU, docs/protocol.md §12); advertised in `Hello` |
| `media_udp_port` | 7000 | the one UDP port for all call media (open it in the firewall) |
| `media_listen_ip` | all addresses | bind media to one address |
| `media_public_ips` | `[]` | behind NAT: the public addresses clients should send media to |
| `media_loopback` | `false` | offer loopback addresses (tests, single-machine setups) |
| `max_calls` | 1000 | calls at once |
| `max_call_peers`, `max_call_peers_per_ip` | 5000, 20 | participants in all calls, and from one network (IPv4 address or IPv6 /64) |
| `call_peer_bitrate` | 8000000 | bits per second one participant may send; more is dropped, not forwarded |
| `call_servers` | `[]` (any) | only carry calls run by these community server keys (hex) |
| `pow_mode` | `load` | proof-of-work for new connections: `load` (only while under load), `always`, `off` |
| `pow_bits` | 18 (1 to 24) | difficulty: leading zero bits of the SHA-256 (about 2^18 hashes, well under a second on a laptop) |
| `pow_load_connections` | 0 (= ¾ of `max_connections`) | open connections at which the relay counts as under load |
| `pow_new_per_second` | 200 | new connections in the last second at which the relay counts as under load |
| `start_window` | 30s (1s to 5m) | how long a new connection has to send Start (and its proof-of-work) |
| `start_window_under_load` | 10s (1s to 5m) | the start window while under load or with half the unstarted slots taken (never shorter than a slow device needs for the puzzle: 2^`pow_bits` hashes at 50 000/s, so 5 s at 18 bits; never longer than `start_window`) |
| `max_unstarted` | 0 (= a tenth of `max_connections`) | connections that haven't sent Start yet, in all; past it a new connection takes the oldest unstarted one's slot |
| `max_unstarted_per_ip` | 128 | unstarted connections from one network (IPv4 address or IPv6 /64); more are refused with 429 |
| `max_connections_per_ip` | 256 | connections from one network, started or not; more are refused with 429 |
| `max_subscriptions_per_ip` | 65536 | subscriptions across one network's connections; more get `LIMIT` |
| `max_subscriptions_total` | 2000000 | subscriptions across all connections (each costs a few hundred bytes); more get `LIMIT` |
| `pow_ip_connections` | 32 | in `pow_mode` `load`, a new connection from a network that already holds this many must solve the proof-of-work even when the relay isn't under load |
| `fanout_threshold` | 100 | publishing to a topic with more subscribers than this needs a fan-out token; 0 = never |
| `chunk_cache_bytes` | 0 (off) | memory for the optional chunk cache |
| `chunk_cache_ttl` | 1m (1s to 1h) | how long a cached message is kept |
| `chunk_cache_topic_bytes` | 4 MiB | most one topic may hold in the cache |
| `chunk_cache_max_message` | 256 KiB | larger messages aren't cached |
| `chunk_cache_conn_bytes` | 0 (= a quarter of `chunk_cache_bytes`) | most one publishing connection's messages may hold in the cache (its oldest go first) |
| `host_frames_per_stream`, `host_bytes_per_stream` | 50, 256 KiB | extra frames/s and bytes/s per open inbound stream for a connection hosting an endpoint (streams from other networks than the host's, at most 4 per network) |
| `host_frames_per_second`, `host_frame_burst` | 50000, 100000 | cap on an endpoint host's frame budget |
| `host_bytes_per_second`, `host_byte_burst` | 128 MiB, 256 MiB | cap on an endpoint host's byte budget |
| `dedicated_server_key` | none | make this a **dedicated relay** for one community server: its public key, 64 hex characters (see "Dedicated relays") |
| `dedicated_server_key_file` | none | the same key read from a file (hex, as the community server's `server_public_key_file` writes it), re-read every 10 s; until the file exists the relay serves nothing. Set this or `dedicated_server_key`, not both |
| `block_file` | none (required when dedicated) | where a dedicated relay keeps its network blocks (JSON, mode 0600; the only file a relay writes). It holds IP addresses |
| `max_blocked_networks` | 10000 | networks a dedicated relay's block list may hold; past it a new block is refused (`LIMIT`) |
| `tls_mode`, `tls_domains`, `tls_cert_file`, `tls_key_file`, `acme_email`, `acme_directory_url` | `off`, `[]`, none, none, none, Let's Encrypt | TLS on `listen` (`wss://`), see "TLS and browsers"; with TLS on and `tls_domains` set, `public_url` defaults to `wss://<first of tls_domains>/ws` |
| `acme_cache_dir` | none (required with `tls_mode` `acme`) | where ACME keeps its account key and certificates across restarts (made mode 0700); the relay has no other state |

Serve TLS (`wss://`) with `tls_mode` (see "TLS and browsers": `acme` gets
and renews certificates by itself), or put any reverse proxy that passes
WebSockets in front. Call media doesn't go through either: clients send it
to `media_udp_port` directly (the WebSocket only carries signaling; WebRTC
media is encrypted with DTLS-SRTP and needs no certificate). Behind a
proxy, every client appears to come from the proxy's address, so raise
`max_call_peers_per_ip` (or put the relay directly on the internet, with
`tls_mode`).

### Abuse controls

All of these are per relay and need no accounts (docs/protocol.md §2).

- **Per-connection limits** (`frames_per_second`, `bytes_per_second` and
  their bursts, `send_queue_bytes`, `max_subscriptions`, `max_streams`,
  `max_partial`, `max_connections`): a sender over its rate is slowed
  down, a subscriber that can't keep up is disconnected, the caps refuse
  with an Error. Regression tests: `internal/relay` `TestRateLimitSlowsSender`,
  `TestSlowConsumerDisconnected`, `TestOverSizeMessageRefused`,
  `TestPerConnectionCaps`.
- **Endpoint hosts.** A community server reaches all of its members
  through one relay connection (every member's session is a stream on
  it), so that connection's budget grows with what it carries: the plain
  budget plus `host_frames_per_stream` frames/s and `host_bytes_per_stream`
  bytes/s per open inbound stream from another network than its own, at most four per network (bursts grow by twice that; so a client can't raise its own budget by opening streams to itself, and a dedicated relay gives it only to its server), capped at
  `host_frames_per_second`/`host_frame_burst` and
  `host_bytes_per_second`/`host_byte_burst`. Plain clients keep the plain
  budget even with streams open. The numbers: a member session averages a
  few frames a second (messages, presence, typing) and a reconnect storm
  costs about 3 to 5 frames per member for the handshake and sync, so 50
  frames/s per stream leaves an order of magnitude of headroom; at the
  load-test target of 500 members online that's 25 000 frames/s, half the
  50 000 cap, which itself stays below what one relay core forwards, so
  one host can't take the relay. 256 KiB/s per stream covers members
  uploading files at once; 128 MiB/s caps a host at about a gigabit link.
- **Unstarted connections.** A connection holds a slot of
  `max_connections` from the moment it is accepted, before any
  proof-of-work, so connections that never send Start are bounded on
  their own: at most `max_unstarted_per_ip` per network (an IPv4 address
  or IPv6 /64; more are refused with 429 at once) and `max_unstarted` in
  all. When every unstarted slot is taken, a new connection takes the
  slot of the oldest unstarted one, which is closed, so a flood only
  delays real clients by the time they need to send Start instead of
  locking them out. Under load the start window shrinks to
  `start_window_under_load`. Before Start a connection costs little (no
  pinger, a send queue that grows only when used; about 9 KiB of heap per
  side). Behind a reverse proxy every client has the proxy's address:
  raise `max_unstarted_per_ip` to `max_unstarted` there. Metrics:
  `openarcanum_relay_connections_unstarted`,
  `openarcanum_relay_unstarted_refused_total`,
  `openarcanum_relay_unstarted_evicted_total`. Regression tests:
  `internal/relay` `TestUnstartedFloodCantExhaustSlots`,
  `TestStartWindowShortUnderLoad`, `TestUnstartedConnectionIsCheap`.
- **Proof-of-work under load.** In `pow_mode` `load` a new connection
  must solve a Hashcash-style SHA-256 puzzle of `pow_bits` bits (bound to
  its own random Hello nonce, so no solution works twice) when the relay
  has `pow_load_connections` open connections or saw
  `pow_new_per_second` new ones in the last second, and for any new
  connection from a network that already holds `pow_ip_connections` open
  connections. Clients solve it in
  a worker thread; a connection that hasn't started within
  `start_window` is closed. Raise `pow_bits` by one to double the cost of
  each new connection; each extra bit also doubles what slow clients
  (phones, Raspberry Pis) spend, so stay at 20 or below unless under
  attack. Use `always` while under a sustained connection flood.
- **Connections and subscriptions per network.** Started connections
  are capped per network too (`max_connections_per_ip`, refused with 429
  at the upgrade), and so are subscriptions: per connection
  (`max_subscriptions`), per network across its connections
  (`max_subscriptions_per_ip`) and in all (`max_subscriptions_total`),
  answered with `LIMIT`. A subscription costs a few hundred bytes of relay
  memory, so without these one host could hold gigabytes with connections
  that start. Behind a reverse proxy every client has the proxy's address:
  raise the per-network limits there. Metrics:
  `openarcanum_relay_network_refused_total`, `openarcanum_relay_subscriptions`.
  Regression tests: `internal/relay` `TestConnectionsPerNetworkCapped`,
  `TestSubscriptionsPerNetworkCapped`, `TestPowForBusyNetwork`.
- **Fan-out tokens.** A publish to a topic with more than
  `fanout_threshold` subscribers is copied only for a connection holding
  a token from the key the topic commits to (community servers issue them
  for themselves). A member who knows a large channel's topic can't use
  the relay to amplify traffic to everyone in it. The check runs on every
  chunk, and a message that sends no chunk for `start_window` is
  forgotten (a late chunk is refused), so a message begun while a topic
  was small can't finish once it has grown. Group DMs (≤ 10 members,
  a few devices each) stay below the default threshold.
- **Chunk cache (optional, off by default).** With `chunk_cache_bytes`
  set, the relay keeps, in memory only, recent messages whose publisher
  marked them cacheable (channel envelopes: signed and end-to-end
  encrypted), for `chunk_cache_ttl`, so members that reconnect get what
  they missed without asking the community server. Byte streams (the
  Noise sessions) and call media are never cached; nothing is written to
  disk. Every message is charged what it costs in memory, not only its
  payload: 256 bytes for the entry, 256 per chunk and the topic name, so a
  flood of tiny messages fills the budget like large ones do (empty
  messages aren't cached at all), and the cache's heap stays within
  `chunk_cache_bytes` plus Go's own overhead. The oldest messages go first;
  one topic holds at most `chunk_cache_topic_bytes` and one publishing
  connection at most `chunk_cache_conn_bytes` (its own oldest go first; its
  unfinished cacheable messages may hold that much again, within
  `chunk_cache_bytes` together with the cache and every other connection's
  unfinished messages, and are dropped after `chunk_cache_ttl` even while
  the connection is idle),
  so one publisher can't push everyone else's messages out. Evicting is
  constant time whatever the number of entries. Only subscribers who know
  the topic (and so could have received the message live) can ask for a
  replay; see docs/threat-model.md for how channel keys rotate when a
  member joins with history hidden. `openarcanum_relay_cache_bytes` reports
  the charged size. A cached message is serialized for replay once, when
  cached, and every subscriber's send queue shares those bytes, so a
  replay costs a subscriber a few slice headers rather than a copy of the
  topic's cache (regression test `internal/relay`
  `TestReplayFramesSharedBySubscribers`).

### Dedicated relays

A relay can serve a single community server. Its owners then pin their
community to that relay alone (Community Admin → Relays), and moderators
can ask it to block a banned member's network address
(docs/admin-guide.md "Blocking a network address (dedicated relay)"; the
protocol is docs/protocol.md §2 "Dedicated relays").

```json
{
  "listen": ":443",
  "tls_mode": "acme",
  "tls_domains": ["relay.example.invalid"],
  "acme_cache_dir": "/var/lib/openarcanum-relay/acme",
  "dedicated_server_key_file": "/run/openarcanum/server.pub",
  "block_file": "/var/lib/openarcanum-relay/blocks.json"
}
```

Give it the server's public key with `dedicated_server_key` (64 hex
characters) or `dedicated_server_key_file`. The file form suits a relay
next to its community server: set the server's `server_public_key_file` to
the same path, and the server writes its key there at start and after
every key rotation. The relay reads the file every 10 seconds; until it
exists the relay serves nothing, so the start order doesn't matter.

A dedicated relay:

- hosts only that server's endpoint, carries only topics that commit to its
  key (channel topics) and only calls it granted. Device linking, live DM
  delivery and device sync need an ordinary relay from the registry;
- doesn't register with maintainer nodes (`registry_urls` must be empty);
- keeps its blocks in `block_file` (JSON, mode 0600, written atomically).
  This file is the one place a relay keeps IP addresses; they never appear
  in its log. A block covers an IPv4 address or an IPv6 /64, lasts at most
  400 days or until lifted, and is refused once `max_blocked_networks`
  networks are blocked. A connection from a blocked network gets 403 at the
  WebSocket upgrade. A relay that can't read its block file at start
  answers 503 to everyone and exits rather than run without its blocks.

The relay never blocks the network the community server connects from.
What it sees as a client's address decides what a block covers: behind a
proxy or a load balancer that hides client addresses every client looks the
same, so expose a dedicated relay directly. Metrics:
`openarcanum_relay_blocked_networks`, `openarcanum_relay_blocked_refused_total`.

## community

```
community init -data DIR -relay wss://relay.example.invalid/ws [-relay …] [-maintainer https://…] [-storage-key PATH]
               [-automatic-relays] [-install home|vps] [-metrics-listen ADDR]
community init -interactive        # guided setup (the default in a terminal without -data)
community run -data DIR            # or -config FILE
community backup -data DIR         # prints the backup path
community restore -data NEWDIR -backup FILE -storage-key PATH [-relay …] [-maintainer …]
community reregister -data DIR     # bring back a community maintainers reaped (see "Status and reaping")
community healthcheck URL
```

`init` creates the data directory (server key, Noise key, storage key,
database), writes `DIR/config.json` and prints the one-time claim code and
next steps. The guided setup asks where the server runs (`home` behind a
router, or `vps`) and explains what that means for the network: the
community server never accepts connections, so it needs **no port
forwarding and no DMZ** at home and no inbound firewall port on a VPS (only
a relay you run yourself needs ports). It then asks for the data directory,
relays (automatic from the maintainers, or pinned URLs), maintainer nodes,
where to keep the storage key, and whether to turn on the loopback status
listener; invalid answers are explained and asked again, and nothing is
created until the summary is confirmed (or if input ends early). `-install`
only changes the advice printed. `DIR/config.json`:

| Key | Default | Meaning |
|---|---|---|
| `data_dir` | the `-data` dir | database, server key, Noise key |
| `storage_key_file` | `DATA/storage.key` | key for content at rest; keep it off the data disk if you can, and back it up separately: backups are useless without it |
| `claim_file` | none | where `community init -claim-file` wrote the claim code (for containers, docs/kubernetes.md); the server deletes it once claimed |
| `server_public_key_file` | none | the server writes its public key (hex) here at start and after every key rotation, for a relay dedicated to it (`dedicated_server_key_file`, "Dedicated relays") |
| `maintainers` | `[]` | where routes and listings are published, and the relay registry; an entry `URL#OPERATORKEY` names a maintainer cluster: the server publishes to every member of its verified member list (refreshed every 6 h; "Maintainer clusters") |
| `maintainer_operator_key` | none | the operator key for `maintainers` entries without one (base64url) |
| `relay_mode` | `pinned` | `pinned` uses `relays`; `automatic` picks `relay_count` relays from the registry and moves to other healthy ones when they go down (see "Relay failover") |
| `relays` | `[]` | `pinned`: the relays; `automatic`: preferred while they are healthy (and used when the registry can't be reached) |
| `relay_count` | 2 | |
| `backup_dir` | `DATA/backups` | encrypted snapshots (`.fbk`) |
| `backup_interval` | 24h (0 = off) | |
| `backup_keep` | 7 | |
| `purge_interval` | 1h | retention job |
| `publish_interval` | 10m | route/listing refresh |
| `heartbeat_interval` | 1m (1s to 10m) | how often the server tells maintainer nodes it is up (see "Status and reaping"); keep it well under their `community_offline_after` |
| `link_previews_disabled` | `false` | turn the link-preview fetcher off entirely; communities then can't turn previews on (see "Link previews") |
| `preview_timeout` | 5s (500ms to 30s) | time limit for each page or image fetch, redirects included |
| `preview_max_bytes` | 1048576 (4 KiB to 8 MiB) | how much of a page is read; tags after that are ignored |
| `preview_image_max_bytes` | 262144 (0 to 1 MiB; 0 = no images) | largest `og:image` kept; larger images are left out of the preview |
| `preview_schemes` | `["https"]` | link schemes that get previews: `https`, optionally `http` |
| `upload_quota_per_member` | 1073741824 (1 GiB; at least 25 MiB) | bytes one member may upload in any rolling 24 hours (see "Quotas") |
| `file_store_max` | 21474836480 (20 GiB; at least 25 MiB) | encrypted bytes of files the community stores in all (see "Quotas") |
| `max_sessions_per_device` | 4 (at least 1) | sessions one device key may have open at once; a newer one past the cap closes the oldest (see "Quotas") |
| `max_sessions_per_member` | 20 (at least 1) | sessions one member may have open at once across their devices; a newer one past the cap closes the oldest (see "Quotas") |

Every key has a `OPENARCANUM_COMMUNITY_<KEY>` environment override (lists are
comma-separated, e.g. `OPENARCANUM_COMMUNITY_PREVIEW_SCHEMES=https,http`).

The community's own relay setting (Community Admin → Relays, pinned list)
overrides the node config. The server listens on no port: it only connects
out to relays.

### Relay failover

In `automatic` mode the server stays on `relay_count` relays from the
maintainers' registry, preferring relays it is connected to, then the
`relays` you configured, then the least loaded. When a relay refuses the
connection or drops it, the server picks another healthy relay from the
registry at once (a relay that failed stays behind healthy ones for two
minutes), and publishes its route with the new relays; members' apps
follow the route without anyone doing anything (docs/protocol.md §7.4).

In `pinned` mode, and when the community pinned its relays (Community
Admin → Relays) or uses a relay dedicated to it, the server never moves to
other relays: it keeps retrying its own, and its heartbeats tell the
maintainers how many of them it is attached to, so members see the
community as degraded (some down) or offline (all down) instead of being
moved somewhere its owners didn't choose.

### Status and reaping

While it runs, the server sends every maintainer node a heartbeat signed
by its server key every `heartbeat_interval`, and one more when it stops
cleanly; members' apps show the community as **Offline** (with when it was
last seen) when the maintainers have heard nothing for their
`community_offline_after` (3 minutes by default), and right away after a
clean stop. A server that has reported nothing for longer than the
maintainers' `community_reap_after` (7 days by default) is **terminated**:
the maintainers drop its route and directory listing, and members' apps say
"This community was shut down". If the server comes back after that, the
maintainers refuse it (the server logs it, and keeps serving members that
can still reach it); to bring the community back, run

```
community reregister -data DIR
```

(the server may be running). Every route the server publishes from then on
carries that moment, which the maintainers accept as an explicit
re-registration; the community is listed again and members' apps reconnect
by themselves. A server that was offline for longer than the maintainers
keep tombstones (`community_tombstone_ttl`, 30 days by default) simply
registers again with its next publication.

### Link previews

Link previews are off until a community turns them on (Community Admin →
Features). Then, for each new or edited message whose author has
Embed Links, the **community server** (never a member's client) fetches
the first link and stores a title, description, site name and image with the
message. Linked sites therefore see the server's address and the
`OpenArcanumLinkPreview/1` user agent, never a member's address. Code:
`internal/community/preview.go`.

What the fetcher does and refuses (SSRF protection, docs/threat-model.md):

- only the schemes in `preview_schemes` (`https` by default); no user names
  or passwords in URLs; URLs up to 2048 characters;
- it resolves the host and refuses it if **any** of its addresses is not
  public: loopback, private (RFC 1918, `fc00::/7`), link-local
  (`169.254.0.0/16`, where cloud metadata endpoints live, and `fe80::/10`), unspecified,
  multicast, carrier-grade NAT (`100.64.0.0/10`), documentation and
  benchmarking ranges, `0.0.0.0/8`, `240.0.0.0/4`, NAT64, 6to4 and Teredo;
  it then connects only to the checked address (no second lookup), and checks
  the connected socket's address again;
- at most 3 redirects, each one checked the same way; no proxy from the
  environment; no cookies;
- `preview_timeout` per request (5 s), reads at most `preview_max_bytes`
  (1 MiB), parses only `text/html` (`og:title`, `og:description`,
  `og:site_name`, `og:image`, falling back to `<title>` and the meta
  description); titles and descriptions are trimmed to 200 and 500
  characters;
- the `og:image` is fetched under the same rules, kept only if it's a PNG,
  JPEG, GIF or WebP of at most `preview_image_max_bytes` (256 KiB), and
  stored encrypted as a file of the message (file retention applies);
- at most 4 fetches run at once; links beyond that get no preview.

Failures are logged at debug level by message id and a reason
(`address not allowed`, `timeout`, …), never the URL. To stop all outbound
fetches regardless of what communities choose, set
`link_previews_disabled: true`. A server behind an egress firewall can also
allow only outbound HTTPS.

**Moving to new hardware:** copy the newest `.fbk` and the storage key file
to the new machine, run `community restore` (`-storage-key` is the path of
that file, not the key itself), start it with `community run`,
then the owner pastes the printed move code in the client (Community
settings → Ownership → Move to new hardware). Stop the old server.

### Search index

Message search uses a full-text index that lives **only in memory**, so no
plaintext index ever reaches the disk. The index database is opened with
`temp_store = MEMORY`, so its sorts and merges never spill to temporary files
either. The server rebuilds it from the
encrypted messages each time it starts, and again whenever a community turns
search on (Community Admin → Features); with search off there is no index
and no rebuild. The rebuild decrypts one stored message at a time and streams
it into the index (`internal/community/search.go`), so its own working
memory stays small and flat; what grows with the history is the index itself
and the start-up time. New, edited and deleted messages update the index as
they happen.

Measured on an AMD Ryzen 9 5950X (`go test -run '^$' -bench SearchRebuild
./internal/community/`, messages of 12-24 words, docs/load-tests.md "Search
index rebuild"):

| Messages kept | Rebuild time | Index memory (resident growth) | Rebuild working memory (Go heap peak) |
|---|---|---|---|
| 10,000 | 0.09 s | about 4.5 MiB | 2.3 MiB |
| 50,000 | 0.5 s | about 13 MiB | 2.3 MiB |
| 200,000 | 2.1 s | about 50 MiB | 2.3 MiB |

So budget roughly **10 s of start-up and 250 MiB of RAM per million kept
messages** (more for long messages; a Raspberry Pi is several times slower
to rebuild). The server isn't connected to its relays until the rebuild
finishes. Message retention (`message_retention_days`) bounds both; turning
search off removes the index and its memory.

### Data at rest

Everything the community server stores in `community.db` is encrypted,
except the columns listed below. A stolen disk (or database file, WAL or
backup) without the storage key reveals no names, no content and no member
or device public keys. The server can still read everything while it runs:
this protects the disk, not the data from the server's operator
(architecture §6).

**Format.** Every sealed value is a 24-byte random nonce followed by
XChaCha20-Poly1305 ciphertext (Go `x/crypto`). The associated data is
`openarcanum/at-rest/v1` 0x00 *table* 0x00 *column* 0x00 *row*, where *row* is
the row's integer id, or the row's lookup hash in hex for tables keyed by
one. A value copied into another row, column or table fails to decrypt.
Records (members, roles, channels, devices, bans, network blocks, join keys, audit entries,
file metadata, pins) are JSON inside the seal; message bodies are protobuf
`Message` (author, text, reply-to, edit time, attachment ids, mentions,
reactions with the members who reacted, link preview, join/leave event). Code: `internal/community/atrest.go`.

**Keys.**

| Key | Where | Seals |
|---|---|---|
| Storage key (32 random bytes, hex) | `storage_key_file`, outside the database | the member roll (including timeouts), roles, channels (names, channel keys, topics, topic text, overrides including per-member ones, retention, slowmode, channel toggles), categories (names, overrides), join keys (labels, creators, expiry, roles, public secrets), bans (names, reasons), network blocks on a dedicated relay (member, moderator, relay, block id, duration), devices (keys, names), avatars, audit log, contact cards, key packages, report records (reporter, message, author, rule, who closed it, status), AutoMod rules (names, keywords, patterns, domains), custom emoji (names and images), pins (who, when), every `meta` value (settings, certificate, owner keys, claim/move hashes), and the two keys below |
| Month keys (32 random bytes each) | `month_keys`, one row per UTC calendar month, sealed by the storage key | message bodies, file metadata (name, type, size, uploader and the file's own chunk key) and report reasons created in that month; file chunks are encrypted with the per-file key |
| Lookup key (32 random bytes) | `meta`, sealed by the storage key | nothing: it keys HMAC-SHA256 over member and device public keys, which is what rows are stored and looked up under |

A meta row `key_check` holds a fixed string sealed under the storage key. At
startup the server opens it first: with the wrong key it refuses to start
with "the storage key does not match this database". A row that fails to
decrypt is never skipped: a damaged roll, role or channel row stops the
server with an error naming the table and row id; a damaged message is
logged by id at startup (and left out of search) and reading it returns an
error.

**Monthly key rotation and retention.** Content is sealed under the key of
the UTC month it was created in. The retention job (`purge_interval`, hourly)
first deletes rows past their retention, then works out the
longest retention in effect: the maximum, over every channel, of its
message and file retention (channel overrides included; channels without
one use the community default). If any of those is "forever" (0), no month
key is ever deleted. Otherwise every month whose last millisecond is at or
past that retention has its remaining messages and files deleted and its key
deleted, in one transaction, followed by a WAL checkpoint (`TRUNCATE`).
`secure_delete` is on for every connection, so deleted rows and keys are
zeroed in the file. Example: with 30-day messages and 7-day files,
January's key is deleted at 23:59:59.999 on 2 March (UTC, non-leap year) and
January's data is unrecoverable from then on, even from a copy of the
database taken earlier, unless that copy also holds the key (see backups).
The period is fixed at one calendar month; it is not a setting.

**Backups.** A `.fbk` backup is a database snapshot (with its sealed month
keys) encrypted under the storage key (an age file; see "Backups" below for the format). Restoring needs the
storage key, and works with any set of rotated keys: months whose keys were
deleted before the backup are simply absent. A backup taken before a month
key was deleted still holds that key, so expired data stays recoverable
from that backup until it is pruned (`backup_keep` × `backup_interval`,
7 days by default). A server restored from such a backup deletes the key
again on its first retention run. Keep the storage key separate from the
backups; anyone with both can read everything in them.

**What stays in plaintext, and why.** Each column below is either
**bound** (a copy of its value is sealed with the row, and every read
compares the two, so editing it on disk is refused and reported as a
damaged row) or harmless to change (the last column says what an edit could
do). Code: `internal/community/atrest_bind.go`.

| Table.column | Why it can't be sealed | Edited on disk without the key |
|---|---|---|
| every `id` (`roles`, `channels`, `categories`, `join_keys`, `messages`, `files`, `pins.message_id`, `reports`, `automod_rules`, `emoji`, `audit`, `key_packages`) and `file_chunks.file_id/idx` | row identity: bound into each row's AAD, used for paging, foreign keys and ordering. Opaque counters. | bound (AAD): the sealed values stop decrypting |
| `members.kid`, `devices.did`, `bans.kid`, `net_blocks.kid`, `contact_cards.member`, `key_packages.member` | keyed lookup hashes (HMAC-SHA256 under the sealed lookup key), not public keys: needed to find a member's or device's row at connect time without decrypting the whole table. Without the storage key they can't be linked to a public key. | bound (AAD, and members/devices are checked against the sealed public key) |
| `devices.member` | lookup hash of the device's member, for listing a member's devices | bound: the sealed device record names its member; a device listed under another member is refused |
| `key_packages.device` | lookup hash of the DM device a one-time key belongs to: packages are counted and handed out one per device in SQL | bound: the sealed package holds the device key; a package under another device's hash is refused |
| `messages.channel_id`, `files.channel_id` | history is fetched per channel and retention is applied per channel. Reveals which channel id (not name) a row belongs to. | bound: the channel id is sealed in the message body and the file metadata; a row moved to another channel is refused |
| `messages.created_at` | retention purges by age in SQL (`created_at <= cutoff`); history and search for new members filter on it when "new members see history" is off; a ban's history deletion picks messages by it. Reveals posting times. | bound (schema 8): the creation time is sealed in the message body, so a message made to look newer (shown to members who joined after it) or older is refused when read. Purging by age still runs on the plaintext value: an edit can make a message purge earlier or later than its retention (the month key still bounds how long it stays readable) |
| `files.created_at` | retention purges by age in SQL (`created_at <= cutoff`), and unposted uploads expire by it. Reveals upload times. | changes when the row is purged by age (earlier or later); the month key still bounds how long it stays readable |
| `messages.month`, `files.month` | selects the month key; derivable from `created_at`. | the row stops decrypting |
| `messages.deleted`, `files.complete` | flags the server filters on (search rebuild, incomplete uploads). | `deleted`: a deleted message's body was already replaced by its author alone, so nothing comes back; `complete`: a file shows as missing |
| `files.message_id` | links a file to the message that carries it (0 = a staged upload not posted yet): attachment purge, staged-upload expiry and checking that a posted file is staged. Reveals which files belong to one message. | bound: the message id is sealed in the file metadata (resealed when the file is posted); a posted file made staged again, or moved to another message, is refused |
| `pins.channel_id` | pins are listed per channel and removed with their message (foreign key on `pins.message_id`, the row's id); who pinned and when are sealed. Reveals which message ids are pinned. | bound: the channel id is sealed in the pin record, and the pinned message's own (bound) channel is checked too; a pin moved to another channel is refused |
| `join_keys.secret_hash` | join keys are looked up by the hash of the secret a joiner presents; the secrets are 18 random bytes, so the hash can't be reversed. | bound (`join_keys.bound`) |
| `join_keys.uses`, `join_keys.revoked`, `join_keys.public` | used and updated in SQL (use counting, revoking the previous public key). Counters and flags, no names. | bound (`join_keys.bound`, rewritten with every use or revocation): an un-revoked or reset key is refused |
| `reports.status`, `reports.month` | the moderator queue lists open reports in SQL (open, resolved, dismissed); month selects the month key the reason is sealed under and lets the retention job delete a month's reports with its key. Reveals how many reports are open, not what they say or who made them. | bound: both are sealed in the report record; a report closed, reopened or moved to another month on disk is refused when read |
| `reports.dedupe` | keyed lookup hash (HMAC-SHA256 under the lookup key) of reporter and message id, unique, so a member can't report one message twice. Can't be linked to a member without the storage key. | bound: recomputed from the sealed reporter and message when read; a changed or cleared hash is refused |
| `month_keys.month` | which month a sealed key belongs to; needed to find and delete expired keys. | bound (AAD) |
| `action_nonces.nonce`, `action_nonces.at` | replay protection for signed admin actions; random nonces, pruned after 10 minutes. | deleting nonces could allow replaying an admin action signed in the last 5 minutes (`at` is checked against the clock) |
| `meta.k` | the names of the meta values (`settings`, `cert`, …); the values are sealed. | bound (AAD) |
| sizes of sealed values and of `file_chunks.data` | ciphertext length follows plaintext length (about 1 MiB per file chunk), so approximate message and file sizes are visible. | n/a |

Binding stops edits of single columns. It doesn't stop someone with write
access putting back an **older sealed copy of the same row** (for example a
join key's `bound` and `revoked` from before it was revoked) or an older
copy of the whole database: nothing on the server is outside the disk to
compare a version against. See docs/threat-model.md.

**Reports and retention.** A report's reason is sealed under the month key
of the day it was made, and `deleteMonth` deletes the month's reports with
its messages, so reports never outlive the longest retention in effect.

**Filtering the audit log.** The audit log is sealed row by row, including
who acted, on whom and what they did, so the server filters it after
decrypting: it reads rows newest first and decrypts at most 2000 per
request, returning a cursor (`next_before_id`) when it stops early. We
chose this over keyed lookup hashes for actor and target because the log
is small next to the message store (one row per admin action), filters are
used rarely and only by admins, and extra hash columns would reveal which
entries share an actor or target to anyone holding the database without
the storage key. Date filters stop the scan at the first row older than
`since` (rows are written in time order). A 100 000-row log is scanned in
50 requests of a few milliseconds each.

**Logs.** The community server never logs names, channel or role names,
join-key labels or secrets, reasons or message content. Admin actions are
logged as `action=<type> actor=<fingerprint> id=<numeric id>`; members and
devices appear only as fingerprints (the first 6 bytes of SHA-256 of the
public key). The audit log, which does name things, is in the encrypted
database and visible to admins in the client.

**Schema migrations.** `PRAGMA user_version` holds the schema version;
`internal/store` applies each missing step in its own transaction and
refuses a database newer than the binary. Version 2 converts a version 1
(plaintext) database in place on first start, then runs `VACUUM` and a WAL
checkpoint so no plaintext pages remain. Converting needs the same storage
key the old server used. A version 1 database has no key check, so the key
can only be checked against the data it encrypted (message bodies, file
names and keys, the public join key's secret): if any of these exist and
the key is different, conversion fails and leaves the database at version 1;
a version 1 database with none of them is converted under whatever key is
given. Every later step (3 onward) first opens `key_check` and fails with
"the storage key does not match this database" before changing anything.
Version 3 turns co-owners into owner invitations.
Version 4 adds the sealed `categories` table and moves every role's
permissions and every channel override to the current permission layout
(docs/admin-guide.md "Permission defaults" describes the mapping).
Version 5 (messaging) moves each message's attachment id from
`messages.file_id` into its sealed body, links files to messages
(`files.message_id`) and adds the `pins` table; @everyone already holds the
messaging permissions that are on by default (Add Reactions, Embed Links,
Change Nickname) from version 4.
Version 6 (moderation and custom emoji, `schema_v6.go`) creates the
`reports`, `automod_rules` and `emoji` tables and sets `max_custom_emoji =
50` in existing communities' settings; the custom emoji toggle gets its
default (on) like every toggle, and @everyone already has Use Custom Emoji
from version 4, matching what new communities start with.
Version 7 binds the plaintext columns above: it adds `join_keys.bound`,
reseals every message body and file's metadata with its channel id (and a
file's metadata with its message id), every pin with its channel id, and
every report with its status and month (rows of a month whose key was already deleted are unreadable anyway and are
left as they are).
Version 8 (`schema_v8.go`) binds `messages.created_at`: it reseals every
message body with its creation time (again leaving rows of a deleted month
key as they are).
Version 9 adds the sealed network-block records of a dedicated relay
(`netblock.go`), found by the member's lookup hash.
Version 10 adds the tables for statuses (`status.go`): each member's status
devices and status ciphertext, and the key drops waiting for recipient
devices, all sealed.

Version 11 (`schema_v11.go`) rebuilds `roles`, `channels`, `categories`,
`emoji` and `join_keys` with AUTOINCREMENT ids, so a deleted role, channel,
category, emoji or join key's id is never given to a new one (join keys and
AutoMod rules keep role and channel ids in their sealed records).

**WAL checkpoints.** Besides the checkpoint after each month-key deletion,
the server runs `wal_checkpoint(TRUNCATE)` at shutdown and again at startup,
so a checkpoint that failed (an open reader) is retried; failures are
logged.

### Quotas

Every collection a member or admin can grow has a cap; the server enforces
each one and refuses the request with the message shown. Four are node settings (above); the custom emoji count and the file size are community
settings; the rest are fixed in the code (`internal/community`).

| Limit | Default | Set by | What the member sees |
|---|---|---|---|
| Uploads per member | 1 GiB per rolling 24 h | `upload_quota_per_member` | "you've uploaded X in the last 24 hours; this community allows Y per member per day" |
| File storage, whole community | 20 GiB | `file_store_max` | "this community's file storage is full; try again after older files expire" |
| Sessions per device key / per member | 4 / 20 | `max_sessions_per_device`, `max_sessions_per_member` | the oldest session is closed ("replaced by a newer session"); nothing is refused |
| GetInfo per session | 3 at once, then 1 per second | fixed | "too many requests for the community's info; try again in a moment" |
| File size | 25 MB | community setting (Max file size, 1 KB to 25 MB; 10 MB for new communities) | "files may be at most X in this community" |
| Uploads in progress | 4 per member | fixed | "too many uploads in progress" |
| Unsent (staged) attachments | 40 per member per channel, 400 per channel (`MaxStagedPerMember`, `MaxStagedPerChannel`) | fixed | "you have N unsent attachments in this channel; …" / "too many unsent attachments in this channel; …" |
| Attachments per message | 10 (`MaxAttachments`) | fixed | "a message can carry at most 10 files" |
| Different reactions per message | 20 (`MaxReactionEmoji`) | fixed | "a message can have at most 20 different reactions" |
| Pins | 50 per channel (`MaxPinsPerChannel`) | fixed | "a channel can have at most 50 pinned messages" |
| Custom emoji | 50 (1 to 250) | community setting (Most custom emoji) | "this community already has N custom emoji, its limit" |
| Custom emoji image | 256 KB, 128×128 px (`MaxEmojiBytes`, `MaxEmojiPixels`) | fixed | "emoji images may be at most 256 KB" / "… at most 128×128 pixels …" |
| Roles | 250, @everyone included (`MaxRoles`) | fixed | "a community has at most 250 roles, @everyone included; delete one first" |
| Channels | 500, text and voice (`MaxChannels`) | fixed | "a community has at most 500 channels; delete one first" |
| Categories | 50 (`MaxCategories`) | fixed | "a community has at most 50 categories; delete one first" |
| Permission overrides | 100 per channel or category | fixed | "a channel or category has at most 100 permission overrides" |
| Invites not revoked | 1000, expired ones included (`MaxActiveJoinKeys`) | fixed | "a community has at most 1000 invites that aren't revoked; revoke some first" |
| AutoMod rules | 20 | fixed | "a community has at most 20 AutoMod rules" |
| Reports | 5 per member per 10 minutes | fixed | refused as rate limited |
| Members | 0 = no limit, up to 1,000,000 | community setting (Max members) | the join is refused as full |

The upload quota is charged when an upload is accepted (when it begins), so
abandoned uploads count too. It is kept in memory only: a restart forgets
it, and usage never reaches the disk. The storage total counts the
encrypted chunks in `file_chunks` (the sizes in file metadata are sealed),
plus the uploads in progress; it includes unsent attachments and link
preview images. The server keeps a running total, adds each stored file to
it and recounts (`SUM(length(data))`) after each retention purge and before
refusing an upload, so deleted and expired files free their room at once. A
link preview whose image doesn't fit is kept without the image. To free
space, shorten file retention (Community Admin, or per channel) or raise
`file_store_max`.

## meta (maintainer node)

`meta [-config meta.json] [-v]`

```
meta blobs stats -data DIR           # stored and in-progress blobs, bytes, tokens, expiry
meta blobs delete -data DIR -id HEX  # delete one blob by its id (hex SHA-256)
meta blobs purge-expired -data DIR   # run the expiry pass now
meta backup -data DIR [-out DIR] [-keep N]   # consistent online backup (see "Backups")
meta restore -data NEWDIR -backup FILE       # restore into an empty data directory
meta id -data DIR                            # this node's node key (maintainer clusters)
meta trust add|remove|list -data DIR [URL] [NODEKEY]   # trusted cluster members
meta operator keygen -out op.key             # a maintainer cluster's operator key (keep offline)
meta operator sign-set -key op.key -seq N -node URL=NODEKEY[:root][:nodirectory] ... -out set.bin
meta set publish -data DIR [-config FILE] set.bin       # store the signed member list
meta init -replica-of ROOT_URL -data DIR [-url THIS_URL] [-from-backup FILE]   # set up a replica
meta peers status -data DIR                  # each cluster peer: lag, last success, errors, throttling
meta healthcheck URL
```

The `blobs` commands work on the data directory directly and are safe to
run while the node is running (a blob deleted underneath it just answers
"not found"; a blob file left without its record by a delete racing an
upload's last chunk is removed by the node's next expiry pass, within a
minute).

Every admin command (`blobs`, `directory`, `relays`, and `backup`) refuses
a database at another schema version than its own build instead of
migrating it: a package upgrade that replaces the binary before the node
restarts must not upgrade the schema under the older node that is still
running. Run the commands with the node's own release, or restart the node
on the new release first. Only the node itself migrates its database.

### Maintainer admin commands

```
meta directory list -data DIR [-q TEXT] [-limit N]        # listings stored on this node
meta directory delist -data DIR -id COMMUNITY [-reason R] # remove a community's listing and routes; refuse them from now on
meta directory relist -data DIR -id COMMUNITY             # lift a delisting (its next publication lists it again)
meta directory decisions -data DIR [-config FILE]         # every delisting and relay removal, with reasons
meta directory disable -data DIR                          # switch the public directory off (deletes every listing)
meta directory enable -data DIR [-config FILE]            # switch it on again (refused if the config has it off)
meta directory status -data DIR [-config FILE]            # whether it is on
meta relays list -data DIR                                # the relay registry as stored
meta relays remove -data DIR -url URL [-reason R]         # drop a relay (its whole host); refuse its heartbeats
meta relays restore -data DIR -url URL                    # lift the removals of that host
```

Decisions made with these commands live in `meta.db` (table `delisted`),
next to the `delisted` and `delisted_relays` config lists; the node checks
both inside every write that stores a listing, route or heartbeat, and
when it serves the relay list, so the commands take effect at once on a
running node, also against copies arriving by replication. Entries from
the config file are shown by `decisions -config FILE` and can only be
lifted by editing the file. Decisions are per node: maintainers outside
your cluster aren't told. Inside a maintainer cluster, each decision is
passed to the other members, and a member applies it when the deciding
node is in its `follow_decisions` (a replica set up with `meta init
-replica-of` follows the root); its own decisions always win over
followed ones, and `decisions` shows where a followed one came from. A
reason (at most 500 bytes) is kept for your records; it never leaves the
node.

**Relay URLs.** The registry stores, compares and serves relay URLs in one
canonical form (`internal/relayurl`, docs/protocol.md §7.3): scheme and host
in lower case, no trailing dot, no default port (80 for `ws`, 443 for
`wss`), the path cleaned (no trailing slash, no empty, `.` or `..`
segments); a URL with user information, a query, a fragment or
percent-encoding isn't a relay URL and is refused. Heartbeats, `relays
remove`/`restore`, `delisted_relays` and nameplates all go through it, and
schema step 7 put stored entries in that form. A removal covers every relay
URL on the removed URL's host, whatever the port or path, so a removed relay
can't come back as `wss://RELAY.example:443/ws/` or on another port; a relay
under a new host name is a new relay (removals are a moderation tool, not an
identity system: relay keys are self-made).

Directory snapshots (`GET /v1/directory/snapshot`, docs/protocol.md §7.2)
are served from the same tables, so a delisted community disappears from
them too. Each page holds at most 500 listings and 4 MiB.

**The directory is off by default.** A node runs a public community list
only with `directory_enabled: true` (Helm: `meta.env.OPENARCANUM_META_DIRECTORY_ENABLED: "true"`);
otherwise communities are joined through invites shared another way.
**Switching it off.** When the community list has to go (for example, to
stop being compelled to censor it), `meta directory disable` on the
running node (or `directory_enabled: false`) removes every listing
and refuses new ones with 501 "directory disabled"; search, snapshots and
single listings answer the same, and the node passes no listings to its
peers and takes none from them. Routes are unaffected: existing members
and invites keep working, and people join through invites shared another
way. Community servers stop sending listings to such a node (without
logging errors), and apps hide **Find a community** and the public
listing options once no node they use has a directory (`GET /v1/node`
says). `meta directory enable` switches it back on; communities list again
with their next publication (within `publish_interval`).

| Key | Default | Meaning |
|---|---|---|
| `listen` | `:7100` | HTTP API |
| `data_dir` | required | `meta.db`, and DM blobs under `blobs/` |
| `peers` | `[]` | other maintainers to pull listings, routes and relays from |
| `replication_interval` | 30s | |
| `trusted_peers` | `[]` | maintainer cluster members (`URL#NODEKEY`) this node exchanges private data with, besides the maintainer set and `meta trust add` (see "Maintainer clusters") |
| `cluster_operator_key` | none | the maintainer cluster's operator key (base64url, `meta operator keygen`): the node trusts the members of the newest valid maintainer set signed by it and serves that set at `/v1/maintainers` |
| `node_key_dir` | `data_dir` | where `node.key` and `node.noise` are read from, when they were made beforehand (a mounted Kubernetes Secret, `meta id -data DIR`); never created there |
| `maintainer_set_file` | none | a signed maintainer set (`meta operator sign-set`) stored at start if newer than the one held; needs `cluster_operator_key` |
| `peer_bytes_per_day` | 2 GiB (≥ `mailbox_max_item`) | replicated bytes one trusted peer may push per rolling day; above it the node stops pulling from that peer until the window ends |
| `peer_items_per_minute` | 5000 | replicated mailbox items (and blob records) one trusted peer may push per minute |
| `peer_tombstones_per_minute` | 5000 | replicated deletes one trusted peer may push per minute (bounds what a compromised member can delete) |
| `follow_decisions` | `[]` | node keys of the cluster members whose moderation decisions (delistings, relay removals) this node applies too; its own decisions always win (`meta init -replica-of` sets the root); only decisions of trusted members are kept and passed on, and they stay in the peer log 90 days |
| `peer_blob_bytes_per_hour` | 2 GiB | DM blob content copied from trusted peers in the background per hour (chunks a client asks for are fetched at once) |
| `trusted_interval` | 5s (100ms to 1h) | how often the node pulls each trusted node's log (at once when a connected peer reports a change) |
| `directory_enabled` | false | the public directory (off by default: communities are joined through invites shared another way; set true to run a directory) (listings, search, snapshots). Off, the node lists no communities: listing posts, search, snapshots and listings answer 501 "directory disabled", listings neither replicate out nor in, stored ones are deleted; routes are unaffected (also `meta directory disable`) |
| `listings_per_hour` | 60 | accepted listing updates per community |
| `writes_per_minute` | 600 | per client IPv4 address or IPv6 /64, all write endpoints |
| `reads_per_minute` | 1200 | per client IPv4 address or IPv6 /64, every GET but `/healthz` (searches, snapshots and replication batches cost the most) |
| `delisted` | `[]` | community ids (base64url owner keys) refused and removed |
| `delisted_relays` | `[]` | relay URLs removed from the registry (each removes its whole host; must be valid relay URLs) |
| `relay_ttl` | 2m | registry entries without a heartbeat expire |
| `allow_private_relays` | false | development only (`scripts/dev-up.sh`, kind, the tests): accept relay URLs on loopback, private and link-local addresses and local-only names in the registry, and let the proof-of-control fetch reach them. Off, they are refused (community servers dial registry relays) |
| `relay_proof` | true | a relay URL is listed only after a plain GET of it returned the heartbeat's key (proof of control, docs/protocol.md §7.3; checks: 16 at once, one per URL at a time, 10 per minute per client network for unproven URLs). `false` only together with `allow_private_relays` (kind: the node's pod can't reach the relay's 127.0.0.1 URL) |
| `community_offline_after` | 3m (10s to 24h) | a community whose server sent no heartbeat for this long shows as offline |
| `community_reap_after` | 168h, 7 days (≥ `community_offline_after`, ≤ 365 days) | a community whose server signed nothing (heartbeat or route) for longer is terminated: its route, listing and status are dropped (see "Community status and reaping") |
| `community_tombstone_ttl` | 720h, 30 days (1m to 365 days) | how long a terminated community's tombstone is kept: clients are told it was shut down, and its server must re-register explicitly |
| `mailbox_ttl` | 14 days | DM mailbox time limit |
| `mailbox_quota` | 100 MiB | per inbox |
| `mailbox_max_item` | 2 MiB (1 KiB to `mailbox_quota`) | largest item (a DM event is at most 1 MiB; files go to the blob store) |
| `mailbox_drops_per_minute` | 120 | per inbox (first-contact inboxes are the spam surface) |
| `mailbox_store_max` | 10 GiB (≥ `mailbox_max_item`) | all mailboxes together; drops get 507 when full |
| `mailbox_bytes_per_ip` | 1 GiB (≥ `mailbox_max_item`) | bytes dropped per rolling 24 h per client IPv4 address or IPv6 /64, whatever the inbox; memory only |
| `nameplate_ttl` | 5m (5s to 1h) | device-link code lifetime |
| `nameplates_per_minute` | 10 | link codes made, and claims tried, per client IPv4 address or IPv6 /64 (each separately; codes are random six-digit numbers); failed claims are also limited to 200 per minute for the whole node |
| `blob_ttl` | 72h (1m to 30 days) | DM file lifetime from upload; deleted earlier once every recipient device releases it |
| `blob_max_size` | 26 MiB (1 MiB to 1 GiB) | largest blob: 25 MiB file plus encryption overhead |
| `blob_quota_per_ip` | 250 MiB (≥ `blob_max_size`) | bytes uploaded per rolling 24 h per client IPv4 address or IPv6 /64; kept in memory only |
| `blob_uploads_per_ip` | 500 (≥ 1) | new uploads per rolling 24 h per client IPv4 address or IPv6 /64; bounds the database rows tiny uploads can create; memory only |
| `blob_tokens_per_ip` | 10000 (≥ 200) | fetch tokens (one database row each) registered per rolling 24 h per client IPv4 address or IPv6 /64; memory only |
| `blob_store_max` | 10 GiB (≥ `blob_max_size`) | total blob store size; new uploads get 507 when full |
| `blob_uploads_per_minute` | 30 | new uploads per client IPv4 address or IPv6 /64 |
| `tls_mode`, `tls_domains`, `tls_cert_file`, `tls_key_file`, `acme_email`, `acme_directory_url` | `off`, `[]`, none, none, none, Let's Encrypt | TLS on `listen` (`https://`, HTTP/2 offered), see "TLS and browsers" |
| `acme_cache_dir` | `DATA/acme` | where ACME keeps its account key and certificates (made mode 0700); include it in backups or let the node ask the CA again after a restore |
| `cors_origins` | `["*"]` | web pages allowed to call the API from a browser (CORS): `"*"` for any, or origins such as `https://app.example.invalid`; `[]` turns CORS off. See "TLS and browsers" |

Search queries are never logged. Nothing here is content: listings and
routes are signed by communities, mailboxes and blobs hold ciphertext.

**Community status and reaping.** Community servers post signed heartbeats
(`POST /v1/communities/heartbeat`, docs/protocol.md §7.4), which the node
checks against the server key of the route it holds and replicates to peers
like routes; `GET /v1/communities/{route id}/status` and search results say
whether each community is online, degraded, offline or terminated. Every
expiry pass (each minute; more often when `community_offline_after` or
`community_reap_after` is under four minutes, for tests) terminates the
communities whose server has signed nothing for longer than
`community_reap_after`: their route, listing (with its search rows) and
status are deleted and a tombstone (route id, community id, when it was
last seen, when it was reaped) is kept for `community_tombstone_ttl`. Each
node decides this from the signed times it holds, so tombstones aren't
replicated to `peers` (members of a maintainer cluster do pass them on, so a
member that was down during the reaping drops the community too; give
cluster members the same `community_reap_after`); a route, listing or heartbeat signed longer than
`community_reap_after` ago is refused on every node, so a peer that still
holds an old copy can't bring a reaped community back. While the tombstone
lasts, the node answers the community's route, listing and heartbeats with
410 unless the route carries a `reregistered_at` later than the last time
the server was seen (`community reregister`). The log says when a community
is reaped and when one re-registers (route id prefix and time only).

**Per-client limits.** Every limit keyed by a client address keys on its
network: an IPv4 address, or the /64 of an IPv6 address (one host usually
owns a whole /64). The limit tables live in memory only and are bounded:
each holds at most 100 000 networks (or communities, or inboxes), drops
networks whose window has passed as it is used (constant time, no scan),
and when full of networks still within their window forgets the least
recently used one (refusing newcomers instead let one client with many
addresses lock everyone out). The limits keyed by client address (writes,
mailbox bytes, link codes, tombstones for mail not held) are also capped
per IPv6 /48 at 16 networks' worth, so the 65 536 /64s of one site share
a bounded budget. A mailbox delete may leave tombstones for at most 500
items the node doesn't hold per network per hour. Behind a reverse proxy
every client has the proxy's address; forward clients directly to the node
or raise these limits.

**Request and record sizes.** Each write endpoint has its own body limit
(a listing 640 KiB, a route 256 KiB, heartbeats 2 KiB, a mailbox drop
`mailbox_max_item` plus 4 KiB, small requests 4 KiB; 413 above), records
are stored re-encoded without fields the node doesn't know, and a
replication batch stays under 4 MiB (docs/protocol.md §7). The API server
times out slow requests (header 10 s, request 2 min, reply 5 min, idle 2
min), and net/http's own error log (failed TLS handshakes and the like) goes
to the node's log at debug level with every IP address removed.

**Relay registry.** A heartbeat lists its URL only once the URL proves it
serves the heartbeat's key: the node GETs the URL's http(s) form (no
redirects, no proxy, public addresses only, 5 s, 256 bytes; a relay that
registers answers with its registry key, docs/protocol.md §7.3). So a relay
must be reachable from the maintainer node at its `public_url`, which it
is when clients can reach it. URLs on private or loopback addresses and
local-only names are refused unless `allow_private_relays`. Capacity and
connections are the relay's own claims; the node caps them.

**Mailbox storage.** Inbox addresses are free to pick, so the per-inbox
quota alone doesn't bound the disk: `mailbox_store_max` bounds all
mailboxes together (the node keeps the total in memory, counted once at
startup, and refuses drops with 507 when full while every other write keeps
working) and `mailbox_bytes_per_ip` bounds what one network drops per day.
Size the disk for `mailbox_store_max` plus `blob_store_max` plus the
directory. Items stay until fetched and deleted or `mailbox_ttl`.

**DM blob store.** Blobs are files in `DATA/blobs/` named by their hex
upload id (`.part` while uploading; the upload id is a hash over the
blob's SHA-256, its size and its fetch-token hashes, docs/protocol.md §11);
`meta.db` holds their SHA-256, size, chunk bitmap, times and fetch-token
hashes. `meta blobs stats` and `delete` use the blob's SHA-256. They are files rather than database
rows because each is up to 26 MiB and lives at most `blob_ttl`: chunks are
written in place, nothing large goes through the WAL, and a deleted file
frees its space at once. Size the disk for `blob_store_max`. Logs carry upload
ids and sizes only, never tokens or content. Blobs aren't replicated to
`peers`: clients store each file on two maintainers, and a lost copy only
means recipients fetch the other one (docs/protocol.md §11). Inside a
maintainer cluster, a completed upload's record and every released token
reach the other members at once, any member serves the blob (fetching the
chunks a client asks for from a member that has them), and the rest of the
content is copied in the background within `peer_blob_bytes_per_hour`, every
`trusted_interval` and apart from the pulls (a member that doesn't answer
for chunks goes into the same backoff as one whose pull failed, so it costs
one timeout per backoff and never holds up mail from the others); a
member whose store is full keeps the record only and serves from its peers
(see "Maintainer clusters"). `meta backup`
includes them anyway, so a node restored from a backup still serves the
files (and mailbox items) that were waiting on it.

### Maintainer clusters

A maintainer cluster is up to 8 nodes, run by parties who trust each other,
that replicate DM mailboxes (items and deletes, both ways), DM blob records
(content on demand and in the background), followed moderation decisions
and community tombstones among themselves (docs/protocol.md §7.5). Any
member can then stand in for another: when one is down or unreachable,
apps, community servers and relays use the others, and a message a sender
dropped on one member reaches a recipient reading another. Members see
which inboxes receive mail and when, across the whole cluster: only
cluster with nodes whose operators you trust with that.

**Set up a primary and a replica.** On a computer that stays offline, make
the cluster's operator key once:

```bash
meta operator keygen -out op.key      # prints the operator (public) key; keep op.key offline
```

On the replica's machine:

```bash
meta init -replica-of https://m1.example.invalid -data /var/lib/openarcanum-meta \
  -url https://m2.example.invalid
```

It reads the root's node key (`GET /v1/node`), writes
`/var/lib/openarcanum-meta/meta.json` (trusting the root, following its
moderation decisions, and pinning its cluster's operator key if the root
has a member list), makes this node's keys, and prints what to run on the
root. Then either trust the replica on the root directly:

```bash
meta trust add -data /var/lib/openarcanum-meta https://m2.example.invalid <the replica's node key>
```

or (better: clients learn the cluster from it) sign a member list naming
both, in preference order, and publish it on any member:

```bash
meta operator sign-set -key op.key -seq 1 -out set.bin \
  -node https://m1.example.invalid=<root node key>:root \
  -node https://m2.example.invalid=<replica node key>
meta set publish -data /var/lib/openarcanum-meta set.bin   # on the root; it replicates
```

with `cluster_operator_key` set to the operator key in every member's
config (or `maintainer_set_file: set.bin`, published at start). Start the
replica (`meta -config /var/lib/openarcanum-meta/meta.json`): its first round
pulls everything from the root. For a large root, `meta init ...
-from-backup FILE` seeds the replica from the root's backup first (the
replica still gets keys and epochs of its own). `meta id -data DIR` prints
a node's key at any time.

**Clients.** Users add `https://m1.example.invalid#<operator key>`, or
just the URL: the app shows the operator key's fingerprint (the first 12
hex characters of its SHA-256, as `meta` prints it) and the members, and
pins the key once the user accepts. Community servers and relays take the
same `URL#KEY` entries in `maintainers` and `registry_urls` (or
`maintainer_operator_key`) and publish to every member.

**Add or remove a member.** Sign a new list with `-seq` one higher (with
or without the node) and publish it on any member; nodes act on it within
seconds and apps within 6 hours (at once after a failure). A removed member
loses its sessions at once and can't pull anything new; what it already
has stays with it (ciphertext). A member trusted with `meta trust add` is
removed with `meta trust remove -data DIR NODEKEY`.

**Root gone for good.** Sign a list without it, naming another member
`root` (the role only sets the order clients try nodes in).

**Keys.** A node key lives in `DATA/node.key` and `DATA/node.noise` (or
`node_key_dir`), never in backups: a node restored from a backup onto new
hardware has new keys, so re-sign the list (or `meta trust add` again) with
its new key. A list expires (180 days by default, `-valid`): re-sign before
then, or the members stop trusting each other. A new operator key needs
`cluster_operator_key` changed on every member and the new `URL#KEY` given
to users.

**Watching it.** `meta peers status -data DIR` lists each peer: how far
this node is behind (`lag`), its last success and error, its log epoch and
whether it is throttled. Metrics: `openarcanum_meta_peer_lag` (the largest
lag), `openarcanum_meta_peer_failing`, `openarcanum_meta_peer_throttled` (peers
paused by their limits: alert on it, it may be a compromised member),
`openarcanum_meta_peer_refused_total{kind}` (replicated entries not stored:
invalid, expired, over a quota, store full) and `openarcanum_meta_blobs_remote`.
Give every member the same `mailbox_ttl` and `community_reap_after`.

**Limits and failure.** A partition heals cleanly for up to `mailbox_ttl`
(14 days). A member over a peer's limit (`peer_items_per_minute`,
`peer_tombstones_per_minute`, `peer_bytes_per_day`) is paused until the
window ends; nothing is skipped. The other cases (root down, route-around,
split brain, restore) are in docs/protocol.md §7.5; the threat model has
what a compromised member can and can't do.

## TLS and browsers

A web client served over HTTPS can only open `wss://` and `https://`
connections, and only to servers that allow its origin. Relays and
maintainer nodes do both by themselves: they get and renew their own
certificates, relays accept WebSockets from any origin, and maintainer
nodes answer CORS for any origin by default. Community servers need
nothing: they only connect out, to relays and maintainer nodes.

**The zero-effort setup** (recommended for every public node; `tls_mode`
stays `off` by default only so tests, local networks and nodes behind a
TLS proxy keep working unchanged):

```json
{
  "listen": ":443",
  "tls_mode": "acme",
  "tls_domains": ["relay.example.invalid"],
  "acme_cache_dir": "/var/lib/openarcanum-relay/acme",
  "registry_urls": ["https://maintainer.example.invalid"]
}
```

```json
{
  "listen": ":443",
  "data_dir": "/var/lib/openarcanum-meta",
  "tls_mode": "acme",
  "tls_domains": ["maintainer.example.invalid"]
}
```

or the same as environment variables (`OPENARCANUM_RELAY_TLS_MODE=acme`,
`OPENARCANUM_RELAY_TLS_DOMAINS=relay.example.invalid`, …). What it needs:

- the names in `tls_domains` resolve to the node (DNS A/AAAA records);
- TCP port 443 on those addresses reaches `listen`. The CA proves control
  with the tls-alpn-01 challenge on that port, so no second port (no port
  80) is needed. Either listen on `:443` (the process needs
  `CAP_NET_BIND_SERVICE`; with the systemd units, a drop-in with
  `AmbientCapabilities=CAP_NET_BIND_SERVICE`,
  `CapabilityBoundingSet=CAP_NET_BIND_SERVICE` and `PrivateUsers=no`), or
  forward 443 to the listen port (a firewall redirect, a Kubernetes
  Service, docs/kubernetes.md "TLS for browsers");
- a relay: `acme_cache_dir` (the systemd unit sets it to
  `/var/lib/openarcanum-relay/acme`). A maintainer node uses `DATA/acme`.

With TLS on, a relay's `public_url` defaults to `wss://<first of
tls_domains>/ws`, so the registry and community routes get the `wss://`
URL without another setting (set `public_url` yourself for another port
or path; a `ws://` one is refused while TLS is on). The relay's UDP media
port is unchanged: WebRTC media is encrypted with DTLS-SRTP and needs no
certificate. The status listener (`metrics_listen`) stays plain HTTP on
loopback, and `healthcheck http://127.0.0.1:PORT/healthz` (the images'
health check) asks again over HTTPS when the port serves TLS, so the
container health checks need no change.

**What the node does.** At start it orders the certificates (one for its
DNS names; one for its IP addresses, see below), answers the CA's
challenges on its own listener and logs `tls certificate issued` (or
`tls certificate not obtained` with the CA's reason, retried after a
minute, then twice as long each time up to an hour). Until the first
certificate arrives, TLS handshakes fail. It renews when two thirds of a
certificate's lifetime have passed (day 60 of Let's Encrypt's 90 days) and
keeps the account key and the certificates in `acme_cache_dir` (mode 0700,
files 0600), so a restart serves them at once without asking the CA again
(Let's Encrypt limits duplicate certificates to a few per week). Private
keys are never logged. `acme_email` is the optional contact address given
to the CA. To try a setup without touching Let's Encrypt's production
limits, point `acme_directory_url` at its staging CA
(`https://acme-staging-v02.api.letsencrypt.org/directory`; browsers don't
trust its certificates).

**IP addresses instead of names.** `tls_domains` may hold the node's public
IP addresses (`["203.0.113.7"]`, or names and addresses together). Let's
Encrypt issues IP address certificates only under its `shortlived` profile
(about six days); the node asks for that profile whenever the CA offers it,
renews with about two days to spare, and serves that certificate to
clients that connect by address (they send no server name). A relay's
default `public_url` is then `wss://203.0.113.7/ws`. A CA that offers no
profiles gets the order without one. The address must be the one clients
and the CA reach (not a private address behind NAT).

**Certificates from files.** `tls_mode: "files"` with `tls_cert_file` (PEM
chain) and `tls_key_file` (PEM key) serves a certificate made elsewhere
(cert-manager, certbot, an internal CA). The node reads them again within
10 seconds of a change; a half-written pair (certificate and key that
don't match) keeps the previous one in service until both are replaced.

**Behind a TLS proxy.** Keep `tls_mode: "off"`, set the relay's
`public_url` to the proxy's `wss://` URL, and let the proxy pass
WebSockets (relays) and the `Origin`/CORS headers (maintainer nodes)
through unchanged.

**CORS on maintainer nodes.** With `cors_origins: ["*"]` (the default),
any web page may call the HTTP API: the node answers preflights
(`OPTIONS` with `Access-Control-Request-Method`) with 204, allowing `GET`
and `POST` with the `Content-Type` and `OpenArcanum-Protocol` headers, for
browsers to cache for a day (Chromium caps it at two hours), and also
answers Chromium's private-network preflight (a public page calling a node
on a home network). Answers carry `Access-Control-Allow-Origin: *` and
expose `OpenArcanum-Protocol`. Preflights are answered before anything else:
no database work, and they take nothing from the write rate limit. List
origins (`["https://app.example.invalid"]`) to allow only those pages
(other origins' preflights get 403; the answer then varies by `Origin`),
or set `[]` (or `OPENARCANUM_META_CORS_ORIGINS=`) to turn CORS off. Requests
without an `Origin` header (desktop clients, community servers, relays,
cluster peers) are unaffected either way. Relays need no setting: they
accept WebSockets from any origin, take no subprotocol or custom header
(the protocol version is negotiated inside the connection) and offer
HTTP/1.1, which browsers open WebSockets over.

**Threat model notes.**

- *CORS `*` is safe because nothing is ambient.* The API sets no cookies,
  uses no HTTP authentication and trusts no source address or client
  certificate; every write carries its own proof in the body (a signature,
  a capability token, a mailbox token) and every read answers public or
  capability-gated data. `Access-Control-Allow-Credentials` is never sent,
  so browsers attach no credentials to these requests anyway. A page on
  another site can therefore do nothing a script on its author's own
  machine couldn't; the most it can do is spend its visitor's per-network
  rate limits, which bound any client. Narrow `cors_origins` only to keep
  other web clients away, not for security.
- *TLS ends at the node, content stays end-to-end encrypted.* The relay
  still forwards only ciphertext frames and the maintainer node still
  stores only signed records and ciphertext; TLS adds protection of the
  metadata (topics, inbox tokens, who talks to which node) against
  observers on the path, and lets browsers authenticate the node's name or
  address. It doesn't change what the node's operator can see.
- *The ACME keys are the node's TLS identity.* Whoever copies
  `acme_cache_dir` can impersonate the node's TLS endpoint (and so observe
  the metadata above for clients they can intercept), but still can't read
  or forge content. Keep it like the node's other keys.
- *Mixed content.* An HTTPS page can't use `ws://` relays or `http://`
  maintainer nodes: a web client can only use the registry's `wss://`
  entries, so relays without TLS stay usable by desktop clients only.

**Testing TLS locally.** Use `tls_mode: "files"` with a certificate for
`127.0.0.1`: a self-signed one (`openssl req -x509 -newkey ec -pkeyopt
ec_paramgen_curve:P-256 -nodes -days 30 -subj /CN=openarcanum-test -addext
subjectAltName=IP:127.0.0.1 -keyout tls.key -out tls.crt`, then trust it
in the browser), or one from mkcert (`mkcert -install` once, then `mkcert
127.0.0.1`), which browsers trust without prompting. Point the relay's
`public_url` at `wss://127.0.0.1:PORT/ws` and the client at
`https://127.0.0.1:PORT` for the maintainer node. The other servers
(a community server attaching to the relay, a relay sending heartbeats)
verify the certificate too: start them with `SSL_CERT_FILE` naming the
self-signed certificate (or mkcert's root, `$(mkcert -CAROOT)/rootCA.pem`):
Go programs on Linux then trust that file instead of the system's roots.

## Website

`site` (`cmd/site`, `internal/site`) serves the project website,
OpenArcanum.org: a landing page with announcements, the bug and security
report forms, the security disclosure policy (`/security`, and
`/.well-known/security.txt`), and the admin portal at `/admin`, where admins
post announcements and triage reports. It is server-rendered HTML; the only
JavaScript is the report forms' proof of work, the theme switch and copy
buttons, all served by the site itself. Nothing loads from anywhere else (no
CDN, web fonts, analytics or captcha).

### Setting it up

```sh
make build                                    # bin/site
bin/site admin add alice -data /var/lib/openarcanum-site    # prompts for a passphrase (twice, no echo)
bin/site admin totp alice -data /var/lib/openarcanum-site   # strongly recommended: two-factor sign-in
OPENARCANUM_SITE_DATA_DIR=/var/lib/openarcanum-site \
OPENARCANUM_SITE_LISTEN=:443 OPENARCANUM_SITE_HTTP_LISTEN=:80 \
OPENARCANUM_SITE_TLS_MODE=acme OPENARCANUM_SITE_TLS_DOMAINS=openarcanum.org \
OPENARCANUM_SITE_ACME_EMAIL=ops@openarcanum.org \
bin/site
```

Then sign in at `https://openarcanum.org/admin`. There is no sign-up: admin
accounts exist only through these commands, run on the server against the
site's data directory (`-data DIR`, or `-config FILE` /
`OPENARCANUM_SITE_DATA_DIR`), safe while the site runs:

| Command | Does |
|---|---|
| `site admin add NAME` | creates an admin (1-32 letters, digits, `.`, `_`, `-`); asks for a passphrase of at least 12 characters, read without echo from a terminal or as one line from a pipe (`printf '%s\n' "$PASS" \| site admin add NAME`) |
| `site admin passwd NAME` | sets a new passphrase, clears a lockout and ends the admin's sessions |
| `site admin totp NAME` | turns on two-factor sign-in: prints a key and an `otpauth://` URI for an authenticator app (TOTP, 6 digits, 30 s) and turns it on once you type a current code; `-off` turns it off. Ends the admin's sessions |
| `site admin unlock NAME` | clears a sign-in lockout |
| `site admin remove NAME` | deletes the admin and their sessions (their notes stay, shown as "(removed admin)") |
| `site admin list` | names, two-factor on/off, last sign-in, lockouts |
| `site backup [-out DIR] [-keep N]` | a consistent copy of the database (`site-backup-<UTC time>.db`, 0600), safe while the site runs; default `DATA/backups`, keeping 14 |

In the container image (`deploy/container/Containerfile` target `site`;
HTTPS on 7300, publish it as 443): `docker run --rm -it -v oa-site:/data -v
oa-site-keys:/keys openarcanum-site admin add alice`, then run it with `-p
443:7300 -p 80:7380 -e OPENARCANUM_SITE_HTTP_LISTEN=:7380 -e
OPENARCANUM_SITE_TLS_MODE=acme -e OPENARCANUM_SITE_TLS_DOMAINS=openarcanum.org`.

### TLS

The site uses the same TLS settings as the relay and maintainer node
("TLS and browsers" above): `tls_mode` `acme` gets and renews certificates
from Let's Encrypt with the tls-alpn-01 challenge on the HTTPS listener
itself (port 443 must reach it), `files` serves `tls_cert_file` /
`tls_key_file` (reloaded when they change), `off` is plain HTTP (local
tests, or a TLS-terminating proxy in front). With TLS on, every response
carries `Strict-Transport-Security: max-age=63072000` (two years, without
`includeSubDomains`, so other hosts under the domain aren't affected), and
`http_listen` (`:80`) answers everything with a redirect to https. With TLS on
`tls_domains` is required (also with `files`), and the site answers only
requests for those names (others get 421), so a rebound DNS name can't reach
the admin portal and the https redirect and security.txt never name a host
from the request; behind a TLS-terminating proxy (`tls_mode` off) set
`tls_domains` too.
Admin cookies are `Secure`: browsers only send them over https (or to
`localhost`), so with `tls_mode` `off` sign in through a TLS proxy or on
localhost.

### Settings

`OPENARCANUM_SITE_<KEY>` or the JSON config (`-config`); also the shared
logging keys ("Logs, health and metrics") and the TLS keys above
(`acme_cache_dir` defaults to `DATA/acme`).

| Key | Default | Meaning |
|---|---|---|
| `listen` | `:7300` | the public listener (HTTPS unless `tls_mode` is off) |
| `http_listen` | none | a plain-HTTP listener that redirects to https (needs TLS on) |
| `data_dir` | required | `site.db` (SQLite) and, by default, the key; created and kept at mode 0700 |
| `storage_key_file` | `DATA/storage.key` | the 32-byte key that seals reports, notes and two-factor secrets at rest; created on first start (0600). The container image keeps it in `/keys`, apart from `/data` and its backups |
| `access_log` | `stderr` | the Apache combined-format access log: `stderr`, `off`, or a file (rotated like `log_file`) |
| `log_client_ips` | `false` | write client addresses into the access and error logs instead of `-` |
| `trusted_proxy_header` | none | behind a reverse proxy: the header carrying the client's address (`X-Real-IP`), used for rate limits only and never logged (unless `log_client_ips`); of a comma-separated list only the last entry, the one the proxy added, counts. Leave empty when clients connect directly, since anyone can send it |
| `reports_per_hour` | 5 (1 to 1000) | reports per IPv4 address or IPv6 /64 in a sliding hour |
| `reports_per_day` | 500 | reports from everyone together per day (bounds storage under a distributed flood; once reached, reports are refused and a warning is logged, so a flood can block real reports for the rest of the day: raise it, or `report_pow_bits`, if that happens) |
| `report_pow_bits` | 18 (0 to 24) | the forms' proof-of-work difficulty: about 2^18 SHA-256 hashes in the browser, well under a second on a laptop |
| `contact_community` | none | the community reporters are asked to be in, so the team can message them; shown on the forms and filled in |
| `download_url`, `source_url` | none | https links shown on the landing page (Download, Source code) once set; empty shows none |
| `webclient_dir` | none (the site image: `/usr/share/openarcanum/webclient`) | the built web client (`clients/web/build.py`), served at /app/ with its own Content-Security-Policy and an "Open in your browser" link on the landing page; empty: no web client |
| `session_ttl` | `12h` (5m to 720h) | an admin sign-in's longest life |
| `session_idle` | `1h` (≥ 1m, ≤ `session_ttl`) | sign-out after this long without a request |

### Reports and how to reach reporters

Reports are plain text (no uploads). Reporters must give their **user ID**,
which the desktop app copies with right-click on their name → **Copy user
ID** (a 32-byte public key, 43 characters of unpadded base64url; the form
checks it is one, in canonical form), and a **community** you share with
them, where they publish a contact card: that is what starting a direct
message needs. E-mail is optional. In the admin portal each report shows
the user ID with a copy button; find them in that community's member list
and **Message** them.

Triage: kind (bug, security), status (new, triaged, in progress, fixed,
won't fix, duplicate), severity (unrated, low, medium, high, critical),
assignee (an admin), internal notes. Every change of status, severity or
assignee is recorded as a note with its time and admin. Security reports
are marked in red in the list and on their page. Reports can be deleted
(spam), after a confirmation.

### Security measures

- **Admin sign-in.** Passphrases are hashed with Argon2id (64 MiB, 3 passes,
  4 lanes, 16-byte salt; RFC 9106's second recommended setting), at most two
  hashes at a time; an unknown name costs the same hash. Every failure gives
  the same answer. Five failures in a row lock the account for 1 minute,
  doubling per further failure up to an hour (kept in the database; `site
  admin unlock` clears it); ten sign-in attempts from one IPv4 address or IPv6 /64
  in 15 minutes pause sign-in from there. Sign-ins for one account run one
  at a time (so a burst of concurrent guesses meets the lockout the first
  ones set), and at most 16 wait for a hash at once. Optional TOTP (RFC 6238, ±1
  step, each code usable once).
- **Sessions.** 256 random bits in a cookie `Secure; HttpOnly;
  SameSite=Strict; Path=/admin`, stored server-side only as a SHA-256, new
  at every sign-in (the previous one is deleted), ended by
  `session_idle`/`session_ttl`, sign-out, a passphrase or two-factor change,
  or removing the admin. Everything under `/admin` except the sign-in page
  needs a session, checked before routing.
- **CSRF.** Every state-changing admin form carries the session's token
  (compared in constant time); the sign-in form a double-submit token. On
  top, any POST a browser marks as cross-site (`Sec-Fetch-Site`, or an
  `Origin` naming another host) is refused.
- **Public forms.** Size limits (128 KiB per post, per-field limits), field
  checks (no control or bidirectional-override characters, a valid user ID
  and e-mail), the per-network and global limits above, a Hashcash proof
  of work (`internal/pow` under the site's own context; challenges are
  signed, expire after an hour and work once), and a honeypot field (a bot
  that fills it is thanked and nothing is stored).
- **Headers.** `Content-Security-Policy: default-src 'self'; script-src
  'self'; style-src 'self'; img-src 'self'; connect-src 'self'; font-src
  'self'; object-src 'none'; base-uri 'none'; form-action 'self';
  frame-ancestors 'none'` (no inline script or style anywhere),
  `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, a
  `Permissions-Policy` turning off camera, microphone, geolocation and the
  rest, `X-Frame-Options: DENY`, `Cross-Origin-Opener-Policy` and
  `-Resource-Policy: same-origin`, `Cache-Control: no-store` on admin pages.
  Admin pages aren't compressed (they mix CSRF tokens with reporter text:
  BREACH).
- **Announcements** are Markdown rendered on the server by a small renderer
  that escapes everything it writes: HTML in the source shows as text, and
  links go only to https, http, mailto or this site (`rel="nofollow
  noopener noreferrer"`).
- **Storage.** SQLite (`modernc.org/sqlite`, `secure_delete` on) in the 0700
  data directory. Report contents, notes and TOTP secrets are sealed with
  XChaCha20-Poly1305 bound to their table, column and row
  (`internal/atrest`, the community server's construction, context
  `openarcanum/site-at-rest/v1`); a wrong key is refused at start. Kind,
  status, severity and times are stored in the clear for filtering.
- **Logs.** The access log (`internal/httpx`, gorilla/handlers'
  `CustomLoggingHandler`, Apache combined format) writes the client as `-`,
  never a query string (`?-` marks one) or a Referer's query; the
  server's error log (TLS handshake errors) has addresses replaced by `-`; ids
  in `/admin/…` paths are logged as `-`.
  Report contents, passphrases, tokens and codes are never logged; sign-ins
  are logged with the admin's name, failures without one.

### Backups and restore

`site backup` (cron or a systemd timer; it is safe while the site runs)
copies the database. A backup's reports stay sealed: restoring needs the
storage key too, so keep a copy of `storage_key_file` somewhere other than
the backups. To restore, stop the site, put the backup in place as
`DATA/site.db` (0600), make sure `storage_key_file` is the key it was made
with, and start the site (a wrong key is refused at start).

### Page text

The disclosure policy (`internal/site/templates/security.html`: response
times, 90-day disclosure, safe-harbour wording, scope) and the landing
page's description (`templates/home.html`, `templates/layout.html`) are
built into the binary. Changing them means a new build.

## Backups

**Community server.** `community backup -data DIR` (or the scheduled job,
`backup_interval`/`backup_keep`, or Community Admin → Backup now) writes
an encrypted `.fbk` snapshot (see "Data at rest" for the format and what it
holds). It is taken without stopping the server: the snapshot is a
`VACUUM INTO` copy read while the server keeps serving, and it is encrypted
as it streams to the backup file, so memory use doesn't grow with the
database (a 48 MiB database raises the heap by about 1.5 MiB, backup or
restore; `TestBackupStreamsAndKeepsServing`). Backups run one at a time.
The disk needs room for one plaintext snapshot (removed when the backup is
written) besides the backups kept.

`community restore` brings it up on new hardware under a new server key and
prints the move code ("Moving to new hardware" above). It builds everything
in a staging directory inside NEWDIR (decrypting as it reads, the new keys,
opening and checking the database) and moves the files into place only at
the end, so a restore that fails (wrong key, damaged backup, an unclaimed
server's backup) leaves NEWDIR as it was and can be run again. It never
replaces an existing storage key: if `storage_key_file` already holds a
different key, the restore is refused. Keep the storage key separately;
without it a backup can't be read. Tested by `TestMoveToNewHardware`,
`TestBackupRestoreAcrossMonthKeyRotation`,
`TestRestoreIsStagedAndKeepsStorageKey` and the backup format tests.

**Backup format.** A `.fbk` file is an age v1
file (age-encryption.org/v1): the payload is age's STREAM encryption
(ChaCha20-Poly1305 in 64 KiB chunks) and the file key is wrapped with age's
scrypt recipient, whose passphrase is the storage key in hex. So the stock
`age` tool can decrypt a backup by hand: `age -d -o snapshot.db
community-….fbk` and paste the 64 hex characters of the storage key file
when asked for the passphrase. The passphrase is a random 256-bit key, so
the scrypt work factor is set low (2^10); it adds nothing to a key that
can't be guessed. Files in the older whole-file formats (`FRAGBK1`,
`FRAGBK2`) are refused with a message saying so.

**Maintainer node.** `meta backup -data DIR` writes
`DIR/backups/meta-<UTC time>.tar` (or into `-out`), keeping the newest `-keep`
(7); the systemd timer `openarcanum-meta-backup.timer` runs it daily, and
`openarcanum-meta-backup-sync.service` copies the backups to another machine
after each run once `/etc/openarcanum/backup-remote.env` exists (see the
unit).
It is safe while the node runs:

- `meta.db` is copied with `VACUUM INTO`, one consistent state of every
  table: listings and the search index, routes, the relay registry,
  mailboxes (with their never-reused ids), nameplates, replication
  cursors and the blob records;
- each complete blob file named in that snapshot is then streamed into the
  archive and hashed; a blob the node deleted meanwhile (expired or
  released by every recipient) is dropped from the snapshot, as is a file
  that doesn't hash to its id; uploads still in progress are left out (their
  senders upload again);
- `manifest.json` lists every file with its size and SHA-256.

The backup is not encrypted again: it holds what the data directory holds
(ciphertext, signed public records, inbox addresses, sizes, times). Store
and copy it with the same care (mode 0600). `meta backup` refuses a
database at another schema version than its own build: run it with the
node's release.

`meta restore -data NEWDIR -backup FILE` restores into a directory without
a database or blob files. It accepts only the entries a backup has (no
links, nothing outside the directory), checks every file against the
manifest and the database against the files, refuses a backup from a newer
release, opens the restored node once in a staging directory inside NEWDIR
(migrating a backup from an older release), and only then moves the blobs
and, last, `meta.db` into place: a restore that fails leaves NEWDIR as it
was. Then start the node on NEWDIR. Mailbox items and blobs past their
lifetime are deleted on its first expiry pass.

Mailbox ids are clients' read positions, so a restore must not hand out
again ids the lost node issued after the backup was taken: the restore
moves the mailbox id sequence 2^32 past the backup's highest id, and
changes the node's mailbox epoch (`MailboxItems.epoch`), which makes
running clients start their positions on this node over. Tested end to
end in `internal/meta/backup_test.go` and `restore_test.go` (mailbox items,
blobs fetched with their tokens, listings, routes, writes during the
backup, vanished and damaged blobs, damaged, truncated, traversing and
newer backups, ids after a restore, a restore failing part way).

**Relays** keep nothing worth backing up (the registry key, if any, is
recreated and re-registered).

## Schema migrations and upgrades

Each database records its schema version in `PRAGMA user_version`
(`internal/store`). On start a node applies every missing step, each in its
own transaction, so a failed step leaves the previous version. Community
server steps are described under "Data at rest"; maintainer node steps are
the blob store (2, 3), never-reused mailbox ids (4), search indexes (5),
admin decisions (6), canonical relay URLs (7), community status and
tombstones (8), and maintainer clusters: peers (9), member lists (10),
global mailbox ids (11), blob replication (12) and replicated decisions
(13, 14). Each step takes the
database's write lock and re-reads the version under it, so two processes
starting at once never both run a step. Relays have no database.

A database written by a **newer** release is refused, untouched:

```
community: /var/lib/openarcanum-community/community.db: database schema version 12 is newer than
this build supports (10): it was written by a newer release; run that release or a newer one
(going back to an older release needs a backup taken before the upgrade)
```

To upgrade: take a backup, replace the binary (or image), restart. To go
back after an upgrade, restore the backup taken before it with the old
release. Tests: every community fixture written by an older build is
brought to each intermediate version and must reach the same decrypted
state as a one-step migration (`TestMigrationFromEveryVersionKeepsData`);
the maintainer database is seeded at every earlier version in that
version's layout and must keep every row (`internal/meta`
`TestMigrationFromEveryVersionKeepsData`); newer databases are refused
(`TestNewerSchemaRefused` in both).

## Logs, health and metrics

Shared by all three nodes (`internal/ops`; keys in the node's config file,
or `OPENARCANUM_<NODE>_<KEY>`):

| Key | Default | Meaning |
|---|---|---|
| `log_format` | `text` | `text` (key=value) or `json` (one JSON object per line: `time`, `level`, `msg`, then attributes) |
| `log_level` | `info` | `info` or `debug` (`-v` also selects debug) |
| `log_file` | none (stderr) | write logs to this file instead, rotated in-process |
| `log_max_size` | 52428800 (50 MiB; ≥ 1 MiB) | rotate before a write would pass this size |
| `log_max_age` | `24h` (0 = by size only; else ≥ 1m) | rotate once the file is this old; at start, a non-empty file older than this is rotated first |
| `log_keep` | 7 (1 to 1000) | rotated files kept (`<log_file>.<UTC time>`); older ones are deleted |
| `metrics_listen` | none (off) | address of the status listener: `/healthz`, `/readyz`, `/metrics` |

**Log rotation: what to use.** Under systemd or a container runtime, leave
`log_file` unset: logs go to stderr, and journald (`SystemMaxUse=`,
`MaxRetentionSec=` in `journald.conf`) or the runtime's log driver (for
Docker, `--log-opt max-size=50m --log-opt max-file=7`) rotate them. For a
binary run any other way (a Raspberry Pi started from a shell or
`@reboot`, a minimal container with a file volume), set `log_file`: the
node rotates the file itself by size and age. We chose in-process rotation
over shipping a logrotate config because it needs nothing from the host
(no `copytruncate` race losing lines, no SIGHUP reopen), it never splits a
record across files, and it is tested in Go (`internal/ops`:
`TestRotationBySize`, `TestRotationByAge`, `TestRotationFailureKeepsLogging`)
and end to end against the relay binary (`clients/desktop/tests/test_e2e_ops.py`).
Files are created with mode 0600; if rotating fails (a full or read-only
directory) logging continues in the current file and one notice goes to
stderr.

**What logs never contain.** No node logs message content, names (members,
channels, roles, communities), join-key labels or secrets, invite or claim
codes, reasons, search queries, keys, tokens, inbox addresses or
passphrases, nor clients' IP addresses (relays included, at debug level
too: connection errors are logged by kind, the WebRTC stack's warnings with
addresses redacted; a dedicated relay's `block_file` is the one place
addresses are kept, never its log).
Members, devices, servers and relay endpoints appear only as
fingerprints (first 6 bytes of SHA-256 of the public key); blobs by upload
id and size; maintainer request errors by route pattern (`POST
/v1/mailbox/drop`), never the path (which carries ids). Operator-chosen
values do appear: relay and peer URLs, listen addresses, file paths.
Tests that drive typical traffic and scan the logs: `internal/relay`
`TestRelayMetricsAndLogsCarryNoContent`, `internal/meta`
`TestMetaMetricsAndLogsCarryNoContent`, `internal/community`
`TestServerLogsContainNoContent`, and across real binaries with a
community chat and a DM, `clients/desktop/tests/test_e2e_ops.py`
`test_logs_and_metrics_carry_no_content`.

**Health.** The status listener answers `GET /healthz` with 200 `ok` while the
node works (meta and community: the database answers) and `GET /readyz` with
200 when it can serve (community: at least one relay attached; relay and meta:
same as healthz); otherwise 503 with a one-line reason (`unhealthy: no relay
attached`). Relays and maintainer nodes also answer `/healthz` on their public
listener (meta's checks the database). The community server has no other
listener, so its health endpoint exists only when `metrics_listen` is set (the
container image sets `127.0.0.1:7201`). A node warns at start when
`metrics_listen` is not a loopback address, and the relay and maintainer node
when `listen` serves other machines with `tls_mode` off.

**Metrics.** `GET /metrics` on the status listener serves the Prometheus
text exposition format 0.0.4, written by `internal/ops` (no client
library: the format is small and stable, and the checker in
`internal/ops/expocheck.go` validates every node's real output in tests).
Metrics are counts and sizes only: label values are declared in code (route
patterns, status codes, outcomes), never taken from requests, and no metric
names a member, community, channel, topic, inbox or blob.

| Node | Metrics |
|---|---|
| all | `process_start_time_seconds`, `process_open_fds` (Linux), `go_goroutines`, `go_memstats_heap_alloc_bytes`, `go_memstats_sys_bytes` |
| relay | `openarcanum_relay_connections`, `openarcanum_relay_connections_accepted_total`, `openarcanum_relay_connections_refused_total` (over `max_connections`), `openarcanum_relay_topics`, `openarcanum_relay_endpoints`, `openarcanum_relay_deliveries_total`, `openarcanum_relay_slow_consumers_total`, `openarcanum_relay_calls`, `openarcanum_relay_network_refused_total` (over `max_connections_per_ip`), `openarcanum_relay_subscriptions`, `openarcanum_relay_blocked_networks` and `openarcanum_relay_blocked_refused_total` (dedicated relays) |
| meta | `openarcanum_meta_http_requests_total{route,code}`, `openarcanum_meta_http_request_duration_seconds{route}` (histogram), `openarcanum_meta_listings`, `openarcanum_meta_routes`, `openarcanum_meta_relays`, `openarcanum_meta_mailbox_items`, `openarcanum_meta_mailbox_bytes`, `openarcanum_meta_nameplates`, `openarcanum_meta_blobs_stored`, `openarcanum_meta_blobs_stored_bytes`, `openarcanum_meta_blobs_uploading`, `openarcanum_meta_blobs_uploading_bytes` (table counts cached for 15 s), `openarcanum_meta_database_bytes` |
| community | `openarcanum_community_sessions`, `openarcanum_community_members_online`, `openarcanum_community_members`, `openarcanum_community_channels`, `openarcanum_community_relays_attached`, `openarcanum_community_messages_total`, `openarcanum_community_backups_total{outcome}`, `openarcanum_community_last_backup_timestamp_seconds`, `openarcanum_community_purge_runs_total`, `openarcanum_community_purge_failures_total`, `openarcanum_community_database_bytes` |

**Exposure.** The status listener is off by default and has no
authentication. Keep it on loopback (`127.0.0.1:7001` relay, `7101` meta,
`7201` community are the documented choices) and let a local Prometheus
scrape it, or bind it to a private interface behind a firewall. Even
public, it reveals only the sizes above (how many members, sessions,
mailbox bytes), which an operator may still want private.

## Names and defaults built into a release

- **Maintainer node in the desktop app.** New profiles use
  `https://maintainer-01.openarcanum.org` (`DEFAULT_MAINTAINERS` in
  `clients/desktop/src/openarcanum/settings.py`). A private network starts
  the app with `--maintainer URL`, or sets Settings → Network → Maintainer
  nodes.
- **No built-in network for the servers.** `community init` takes
  `-maintainer` and `-relay` flags, or asks for them in the guided setup;
  a relay's `registry_urls` is empty until you set it.
- **Container images.** Releases publish
  `ghcr.io/hyperkubeorg/openarcanum-relay`, `-maintainer`, `-community` and
  `-site`, built from the targets `relay`, `meta`, `community` and `site` of
  `deploy/container/Containerfile`. The Go build image and the distroless
  runtime image are pinned by digest (`GO_IMAGE`, `RUNTIME_IMAGE`); refresh
  the digests when either gets a security update.
- **Environment variables.** `OPENARCANUM_RELAY_*`,
  `OPENARCANUM_COMMUNITY_*`, `OPENARCANUM_META_*` and `OPENARCANUM_SITE_*`,
  each followed by the upper-cased config key.
- **Strings that travel in codes and links.** Invite, claim and move codes
  start with `openarcanum-invite:`, `openarcanum-claim:` and
  `openarcanum-move:`; invite links use `openarcanum://invite/`; the wire
  schemas are the protobuf package `openarcanum.v1`; signatures use
  `openarcanum/…` context labels. Changing any of these breaks codes, links
  and signatures made by earlier releases.
- **The website.** `site` starts with `tls_mode` off on `:7300`. A public
  deployment sets `tls_domains`, `tls_mode` `acme`, `acme_email` and
  `http_listen` ("Website" above), and usually `download_url`,
  `source_url` and `contact_community`, which are empty by default (the
  landing page then shows no links and the report forms ask reporters to
  name a community).
