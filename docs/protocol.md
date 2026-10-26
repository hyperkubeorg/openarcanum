# OpenArcanum protocol

Message flows, kept in sync with `proto/openarcanum/v1/*.proto`
(the schemas are the source of truth for field layouts; this file explains how
they're used). Section numbers here are what code comments cite.

## 1. Conventions

- **Keys.** Ed25519 for signatures (identity, owner, server, DM signing keys);
  X25519 for Noise statics (device keys), DM keys and key packages.
- **Signatures** always cover `context || 0x00 || message`. Contexts:

  | Context | Signs |
  |---|---|
  | `openarcanum/relay-hello/v1` | a serialized `RelayBinding{relay_key, url, nonce}` (a relay's proof of its key for one connection, §2 "Relay identity") |
  | `openarcanum/relay-endpoint/v2` | a serialized `RelayBinding{relay_key, url, nonce}` (endpoint registration, §2 "Relay identity"; v1 signed `Hello.nonce` alone and is refused) |
  | `openarcanum/noise-static/v1` | a Noise static public key (handshake identity proof) |
  | `openarcanum/server-cert/v1` | a serialized `ServerCert` (an owner key → server key, §3.1) |
  | `openarcanum/owner-change/v1` | a serialized `OwnerChange` (a current owner → the next owner set, §3.1) |
  | `openarcanum/owner-key/v1` | a serialized `OwnerKeyRequest` (by the new owner key and by the requesting member, §3.1) |
  | `openarcanum/channel-envelope/v1` | a serialized `ChannelEnvelope` (server → channel topic) |
  | `openarcanum/admin-action/v1` | a serialized `AdminAction` (member → server, kept in the audit log) |
  | `openarcanum/device-revocation/v1` | a serialized `DeviceRevocation` |
  | `openarcanum/route/v1`, `openarcanum/listing/v1` | serialized `Route` / `Listing` (server key) |
  | `openarcanum/relay-heartbeat/v1` | a serialized `RelayHeartbeat` (relay registry key) |
  | `openarcanum/community-heartbeat/v1` | a serialized `CommunityHeartbeat` (server key, §7.4) |
  | `openarcanum/maintainer-set/v1` | a serialized `MaintainerSet` (a maintainer cluster's operator key, §7.5) |
  | `openarcanum/peer-decision/v1` | a moderation decision passed between maintainer cluster members (the origin's node key, §7.5) |
  | `openarcanum/release-manifest/v1` | the canonical JSON payload of a release manifest (release key; `docs/release.md`) |
  | `openarcanum/dm-card/v2` | a serialized `ContactCard` (DM identity, §9.2) |
  | `openarcanum/dm-device-list/v2` | a serialized `DmDeviceList` (DM identity, §9.1) |
  | `openarcanum/dm-device/v2` | `identity \|\| curve25519` (a device's own consent to a list, signed by its vodozemac account; §9.1) |
  | `openarcanum/dm-otk/v2` | `curve25519 \|\| key_id \|\| 0x00 \|\| key \|\| fallback` (a device's one-time or fallback key, vodozemac account; §9.2) |
  | `openarcanum/dm-group-state/v2` | a serialized `GroupState` (§9.4) |
  | `openarcanum/fanout-token/v2` | a serialized `FanoutToken` (a community server key → one relay connection: relay key, URL and nonce, §2 "Large fan-outs"; v1 is refused) |
  | `openarcanum/relay-moderation/v2` | a serialized `ModerationCommand` (a community server key → the relay dedicated to it: relay key, URL and one connection, §2 "Dedicated relays"; v1 is refused) |
  | `openarcanum/status-device/v1` | a serialized `StatusDevice` (a member key → one of its devices that can receive statuses in that community, §9.8) |
  | `openarcanum/dm-device-list-approval/v1` | `identity \|\| SHA-256(list)` (a listed device approving a new device list, its vodozemac account; §9.1) |
  | `openarcanum/dm-device-add/v1` | `identity \|\| device_id` (a listed device's add certificate for a new device, vodozemac account; §9.1) |
  | `openarcanum/call-grant/v1` | a serialized `CallGrant` (server key, §12.2) |
  | `openarcanum/call-control/v1` | a serialized `CallControl` (server key, §12.3) |

  Domain-separated hashes (not signatures): `openarcanum/relay-pow/v1`
  (relay proof-of-work, §2), `openarcanum/fanout-topic/v1` (topics committed
  to a server key, §2), `openarcanum/call-id/v1` (§12), `openarcanum/inbox/v1`
  (§7), `openarcanum/mailbox-item/v1` (a mailbox item's global id, §7.5),
  `openarcanum/blob-token/v1` and `openarcanum/blob-upload/v1` (§11).

- Signed protobufs travel as raw bytes next to their signature, so nobody has
  to re-serialize to verify.
- Encodings in strings: unpadded base64url. Times: Unix milliseconds unless a
  field says seconds.

### Versions

Every protocol negotiates its version explicitly (`internal/version`,
`clients/desktop/src/openarcanum/version.py`):

- Each side announces **every version it speaks**; the **highest version
  both speak** is used. A build speaks its current version and the one
  before it ("current and previous"), so a node one release ahead keeps
  serving clients one release behind, and the other way round.
- **Version 1 is current.** There is no version before it, so today every
  build speaks exactly {1}; a build of version 2 will speak {1, 2}. Version
  0 is "unset" on the wire and never valid. Lists longer than 16 entries
  are refused.
- **No version in common:** the side that notices compares the lists. If
  every version the peer speaks is older than ours, the peer is outdated
  ("this relay / community server / maintainer node is outdated"),
  otherwise we are ("Update OpenArcanum"). Clients show that sentence in the
  community status and in the join dialog.
- Where it is negotiated: the relay WebSocket (`Hello.versions` →
  `Start.version`, §2), Noise sessions with community servers
  (`ClientHello.versions` → `ServerIdentity.version`, §3), and the
  maintainer HTTP API (the `OpenArcanum-Protocol` header, §7). The URL prefix
  `/v1/` and the `…/v1` signature contexts name formats, not the
  negotiated version; a future version that changes a format gives it a
  new context or path, and the negotiated version says which the peers use.
- Tests prove the mechanism with a stand-in "version 2" side
  (`version.SupportedFor(2)` = {1, 2}): it serves version-1 peers and
  refuses 0 and unknown versions (`internal/relay` `TestVersionNegotiation`,
  `internal/session` `TestSessionVersionNegotiation`, `internal/meta`
  `TestAPIVersionNegotiation`, `clients/desktop/tests/test_versions_abuse.py`).

## 2. Relay (`relay.proto`)

One WebSocket per connection at `/ws`; every binary message is one
`RelayFrame`. The relay keeps nothing on disk, knows nothing about payloads,
and has no accounts.

1. The relay sends `Hello{nonce, max_chunk, max_message, region, versions,
   pow_bits, start_window_ms, cache_ttl_ms, fanout_threshold, dedicated_to,
   relay_key, url, relay_signature}` first (the last three: "Relay
   identity" below; clients refuse a Hello without a valid proof).
   **Starting.** The client picks the highest version in `versions` it
   speaks (§1 "Versions"; none → it disconnects and reports which side is
   outdated) and sends `Start{version, pow, req}`. Until Start is accepted
   the relay takes no other frame (anything else gets
   `Error{UNSUPPORTED_VERSION}` and the connection is closed); a wrong
   version gets `UNSUPPORTED_VERSION`, a missing or wrong proof-of-work
   `POW_REQUIRED`, and no Start within `start_window_ms` closes the
   connection. Success is `Ack{req}`. The window is shorter while the relay
   is under load (`Hello.start_window_ms` says which applies). Before
   Start a connection holds one of a bounded number of unstarted slots,
   per client network (IPv4 address or IPv6 /64) and in all; the relay
   refuses the WebSocket upgrade with HTTP 429 when the client's network
   holds too many, and when all slots are taken it closes the oldest
   unstarted connection (WebSocket status 1013) to admit the new one
   (docs/operations.md "Abuse controls").
   **Proof-of-work** (only while the relay is under load: open connections
   at `pow_load_connections` or new connections in the last second at
   `pow_new_per_second`; or always, if the operator says so): Hashcash
   (A. Back, "Hashcash — A Denial of Service Counter-Measure", 2002,
   http://www.hashcash.org/papers/hashcash.pdf) with SHA-256 in place of
   SHA-1. The client finds an 8-byte big-endian counter with
   `SHA-256("openarcanum/relay-pow/v1" || 0x00 || Hello.nonce || counter)`
   starting with `pow_bits` zero bits (at most 24). The challenge is the
   relay's fresh random per-connection nonce, so a solution only works on
   its own connection (no replay), and only within the start window
   (expiry). Clients solve it in a worker thread. Test vector: challenge
   bytes 0..31, counter 42 → digest `16b6ad67…4f1c66b5`; the first counter
   with 12 zero bits is 3226 (`internal/pow`, `clients/desktop/tests`).
2. **Pub/sub.** `Subscribe{topic, req}` / `Unsubscribe` are acked
   (`Ack{req}` or `Error{req}`). `Publish{topic, message_id, seq, final, data}`
   sends one chunk; a message is the chunks with one `message_id`, in `seq`
   order, ending with `final`. The relay forwards each chunk as `Deliver`
   (adding a relay-local `sender` number) to every subscriber except the
   publisher. Every chunk of a message names the topic its first chunk named
   (a chunk for another topic gets `Error{BAD_FRAME}`). It enforces
   `max_chunk` per chunk and `max_message` (≤ 25 MiB) per message; an
   over-size message gets `Error{TOO_LARGE}`. Once a chunk of a message is
   refused, its remaining chunks are dropped whatever topic they name. Clients
   cap `max_message` at 25 MiB whatever a relay announces, and bound what
   unfinished messages may hold (64 MiB, 1024 messages, 64 per sender).
   Knowing a topic is the permission: topics are
   random 32-byte values.
3. **Endpoints and streams.** A community server proves it holds its key:
   `RegisterEndpoint{endpoint_key, signature, url}`, the signature with
   context `openarcanum/relay-endpoint/v2` over `RelayBinding{Hello.relay_key,
   url, Hello.nonce}` and `url` the canonical URL it dialled ("Relay identity"
   below; a binding for another relay key, URL or connection, or a v1
   signature over the nonce alone, gets `BAD_SIGNATURE`). A newer valid
   registration replaces an older one (and closes streams to it). A connection
   hosts one endpoint: registering another key on it (key rotation) drops the
   previous one. A client sends `OpenStream{endpoint_key, stream_id}` (its own
   id, high bit clear); the relay answers `StreamOpened` to the client and
   sends the endpoint `StreamOpened` with a relay-chosen id (high bit set).
   `StreamData` is forwarded one-to-one and preserves message boundaries;
   `StreamClose` ends it either way. If either side disconnects, the other
   gets `StreamClose`.
4. **Limits.** Per connection: frames/s and bytes/s token buckets (a sender
   over its rate is paused, not cut off), and a send queue cap: a subscriber
   that can't keep up is disconnected ("slow consumer") rather than buffered
   without limit. Plus caps on subscriptions, streams, in-flight messages and
   total connections. A connection hosting a registered endpoint gets the
   plain budget plus a per-inbound-stream allowance, capped
   (docs/operations.md "Abuse controls"), because it carries every
   member's session.
   **Large fan-outs.** A `Publish` that starts a message on a topic with
   more than `fanout_threshold` other subscribers is refused
   (`Error{FANOUT_TOKEN_REQUIRED}`, the message's chunks dropped) unless the
   connection holds a live fan-out token from a key the topic commits to.
   Topics that can grow large (channel topics) are made as
   `r || SHA-256("openarcanum/fanout-topic/v1" || 0x00 || server_key || r)[:16]`
   with `r` 16 random bytes, so anyone can check which key a topic belongs
   to without learning anything else. A token is
   `SignedFanoutToken{token = FanoutToken{issuer, relay_nonce, expires_at, relay_key, url}, signature}`,
   signed with context `openarcanum/fanout-token/v2` by `issuer`, bound to the
   relay's key and URL and the `Hello.nonce` of the connection it is sent
   on, as an endpoint registration ("Relay identity"; `FanoutAuth{token,
   req}`; a token for another relay or connection, expired, valid for more
   than an hour or not verifying gets `BAD_TOKEN`). The relay keeps at most 8 issuers per
   connection until they expire and stores nothing per user. Community
   servers send a 30-minute token on every relay connection and renew it
   every 15 minutes and after a key rotation; their channel topics are
   re-made when the server key changes.
   **Chunk cache (optional).** `Publish.cacheable` marks an end-to-end
   encrypted message the relay may keep (community servers mark channel
   envelopes). With a cache configured (`Hello.cache_ttl_ms` > 0) the relay
   keeps whole, non-empty cacheable messages in memory for that long,
   bounded in total, per topic, per publishing connection and per message
   (each message charged its bookkeeping overhead too), and
   `Subscribe{replay_ms}` delivers the cached
   messages on that topic from the last `replay_ms` (at most the TTL) as
   `Deliver{replayed: true}` after the Ack. Streams and call media are never
   cached. Clients ask for a replay only when re-subscribing a topic after
   a reconnect, never on a first subscription; replayed typing indicators
   are ignored. That is client behaviour; what enforces "new members don't
   see history" against a modified client is the server: a member admitted
   while history is hidden from them gets channel keys and topics made
   after they joined (§6.2), so cached envelopes from before are on a topic
   they don't know, under a key they don't have.
5. **Registry.** With `registry_urls` set, the relay POSTs a
   `SignedHeartbeat` to each maintainer every `heartbeat_interval`, with
   its `public_url` in canonical form (§7.3), signed with its relay key.

   **Relay identity.** Every relay has an Ed25519 key: its registry key
   (`registry_key_file`, or a fresh one per run). `Hello` carries it in
   `relay_key`, the relay's own canonical URL in `url` (`public_url`, or
   the `wss://` default with TLS on; empty when it has none) and
   `relay_signature = Sign(relay_key, "openarcanum/relay-hello/v1" ||
   RelayBinding{relay_key, url, nonce})`, where `RelayBinding` is serialized
   deterministically (fields in order). Clients refuse a Hello whose proof
   is missing or doesn't verify, or whose `url` isn't canonical.
   Everything a client signs for one relay connection (endpoint
   registrations, fan-out tokens, moderation commands) is bound to
   `RelayBinding{Hello.relay_key, U, Hello.nonce}`, with `U` the canonical
   URL (§7.3) the client dialled. Before signing, the client checks:
   - a non-empty `Hello.url` equals `U` (a relay is registered at the URL
     it announces; dialled under another name it is refused with "dial it
     at its own URL");
   - a relay key pinned for `U` matches (`relayclient.Keys.Pin`);
   - a relay with an empty `url` has its key accepted at one URL only per
     process (trust on first use, `relayclient.KeyBook`; the Python client
     does the same).

   The relay accepts a binding only for its own key, this connection's
   nonce, and its own URL (a relay without `public_url` takes any
   canonical URL: it can't tell which names reach it). Signing the nonce
   alone (the v1 contexts) let a malicious relay that a server attaches to
   present another relay's nonce as its own, and replay the registration
   and token it got signed on that other relay: members' streams to the
   community then reached the attacker (`internal/relay`
   `TestCrossRelayHijackCopiedHello`, `TestCrossRelayHijackOwnKey`,
   `TestCrossRelayHijackRelayWithoutURL`). Public relays should set
   `public_url` (registry relays must); a relay without one logs a warning
   at start.
6. **Dedicated relays** (optional; docs/operations.md "Dedicated relays").
   A relay can be configured for one community server
   (`dedicated_server_key`, or `dedicated_server_key_file`). Such a relay:
   - names that key in `Hello.dedicated_to` (an ordinary relay leaves it
     empty);
   - accepts `RegisterEndpoint` and `OpenStream` only for that key
     (`Error{NOT_DEDICATED}`; a refused stream gets `StreamClose{reason:
     "not_dedicated"}`), `Subscribe` and `Publish` only on topics committed
     to it (`fanout.Commits`, as for "Large fan-outs": channel topics), and
     `CallJoin` only with a grant signed by it (`CALL_REFUSED`). DM, device
     sync and device-link topics are refused, so clients never choose a
     relay that says it's dedicated for those (they remember such relays
     per community), and a dedicated relay doesn't register with
     maintainer nodes;
   - while it doesn't know its key yet (a key file not written yet), serves
     nothing (every registration and topic is refused) and leaves
     `dedicated_to` empty.

   **Moderation commands.** The server sends
   `RelayModeration{command = SignedModerationCommand{command, signature}, req}`
   on its own endpoint connection; the relay answers
   `ModerationResult{req, …}` or `Error{req}`. `command` is a serialized
   `ModerationCommand{server_key, relay_nonce, command_id, expires_at,
   block | unblock, relay_key, url}` signed by `server_key` with context
   `openarcanum/relay-moderation/v2` (Ed25519 with the §1 domain separation,
   as fan-out tokens and call controls; code `internal/relaymod`). The
   relay accepts it only if all of these hold, in this order:
   1. the relay is dedicated (else `NOT_DEDICATED`), and the connection
      sending it hosts the dedicated key's endpoint (else `NOT_DEDICATED`);
   2. it parses, has a 16-byte `command_id`, and `server_key` is the
      dedicated key (else `BAD_COMMAND`, or `NOT_DEDICATED` for another
      key);
   3. the signature verifies (`BAD_COMMAND`);
   4. `relay_key`, `url` and `relay_nonce` are this relay's key, its URL
      and this connection's `Hello.nonce` ("Relay identity"): bound to this
      relay and this connection, so it can't be replayed on another relay
      or a later connection (`BAD_COMMAND`);
   5. `expires_at` (Unix ms) is in the future and at most 5 minutes away
      (`BAD_COMMAND`; servers use 1 minute);
   6. a block names 1 to 32 stream ids and lasts at most 400 days; an
      unblock names a 16-byte block id (`BAD_COMMAND`);
   7. its `command_id` hasn't been seen on this connection; ids are
      remembered until they expire, at most 1024 at once (`BAD_COMMAND` for
      a replay, `LIMIT` when full).

   `NetworkBlock{stream_ids, seconds}` names inbound streams of the
   sending connection (the relay-chosen ids with the high bit set, §2.3).
   For each stream that exists the relay takes the client connection's
   network, as for the unstarted limits: the IPv4 address, or the IPv6
   address masked to its /64. Networks equal to the server's own
   connection's are skipped (counted in `skipped`, never blocked). No
   known stream at all is `BAD_COMMAND`. The rest become one block with a
   fresh random 16-byte id, lasting `seconds` (0 = until lifted), saved to
   the relay's `block_file` before the answer (a full list is `LIMIT`).
   Every connection from those networks is then closed (after its queued
   frames, at most a second), and new WebSocket upgrades from them get HTTP
   403 until the block ends. The answer is
   `ModerationResult{block_id, networks, closed, skipped}` (`block_id`
   empty when everything was skipped). `NetworkUnblock{block_id}` removes
   that block: `ModerationResult{not_found}` says whether it was there.
   Nothing in a result names an address.

## 3. Sessions (`session.proto`)

`Noise_XX_25519_ChaChaPoly_SHA256`, prologue `openarcanum/noise/v1`, run over a
relay stream.

| Message | Direction | Payload |
|---|---|---|
| 1 | client → server | `ClientHello{versions}` (clear) |
| 2 | server → client | `ServerIdentity{server_key, static_sig, cert?, version, versions}` |
| 3 | client → server | `ClientIdentity{identity_key, static_sig, device_name}` |

- `static_sig` signs the sender's Noise static key with its Ed25519 key. The
  client's static key is its **device key** for that community; the
  signature is computed once and stored, so sessions can reconnect while the
  profile is locked.
- The client accepts the server if `server_key` is the key it pinned, or if
  `cert` is a `ServerCert` for that server key, signed by a key in the
  community's current owner set (verified from the pinned community id
  through the cert's owner chain, §3.1), with a sequence number at least as
  high as the last one seen (rotation, new hardware). Either way, a cert whose
  owner set is older than, or forked from, the one the client last saw is
  refused.
- Transport: each Noise message carries one byte of flags (bit 0 = more
  fragments follow) and up to 65 000 bytes of an application message;
  messages reassemble up to 32 MiB. A community server takes much less.
  The session reassembles at most 1 MiB per message from a peer that isn't
  an active member yet (a claim with its icon or a join request fits;
  `session.PreAdmissionMaxMessage`), and the server meters requests
  fragment by fragment: more than 512 KiB from a session that isn't an
  admitted member, or more than 8 MiB from a member, closes the session.
- The first transport message from the server is
  `ServerMessage{status: SessionStatus}` (§4.1).
- **Versions** (§1). The client lists every version it speaks in
  `ClientHello.versions` (the older single `version` field is read as a
  one-entry list). The server answers with the highest common one in
  `ServerIdentity.version` and its own list in `versions`; with none in
  common it sends `version = 0` and closes the stream after message 2,
  and the client reports which side is outdated. A version the client
  didn't offer is refused. Both payloads are part of the Noise transcript,
  so a man in the middle can't change the lists without breaking the
  handshake.

### 3.1 Owner sets and server certs

A community's id is its founder's owner key; it never changes (invites,
routes and listings carry it). The keys allowed to certify server keys form
the **owner set**, which starts as `{founder}`. This is key delegation with
Ed25519 signatures only; code: `internal/identity/owners.go`,
`clients/desktop/src/openarcanum/owners.py`.

- **Owner changes.** `OwnerChange{community, seq, prev, signer, at, add |
  remove}` is signed by `signer` (context `openarcanum/owner-change/v1`) into a
  `SignedOwnerChange`. `seq` is 1 for the first change and then +1;
  `prev` is SHA-256 of the previous change's bytes (empty for seq 1). A change
  is valid only if `signer` is in the set before it.
  - `add` is an `OwnerAdd{request, owner_sig, member_sig}`: `request` is a
    serialized `OwnerKeyRequest{community, owner_key, member}`, signed
    (context `openarcanum/owner-key/v1`) by the new owner key (proof that its
    holder made the request) and by the member's identity key (the member
    asked for it). A key that is or was ever an owner can't be added; a
    member holds at most one owner key; a set has at most 16 owners.
  - `remove` names a key in the set. The last owner can't be removed. Any
    owner can remove any other owner, or itself while another owner (who
    hasn't also asked to leave) remains; removing yourself takes a second
    owner's cert (§4.5).
- **The chain.** Verifying the changes in order from the founder key gives
  the current set, its `seq` and its head (hash of the last change). Chains
  are at most 256 changes.
- **Certs.** `ServerCert{owner_key, server_key, issued_at, seq, community,
  owner_seq, owner_head}` is signed by `owner_key`. `SignedServerCert`
  carries `owner_chain`, every change since the founder. A cert is valid for
  a community when its chain verifies from the community id, ends exactly at
  `(owner_seq, owner_head)`, and `owner_key` is in the resulting set. Certs
  from before owner sets (no `community`) mean `community = owner_key` with an
  empty chain.
- **Every owner change comes with a new cert** for the current server key,
  signed by an owner under the changed set (§4.5): the acting owner, or for
  an owner removing themselves, another owner. So the cert the server
  presents always verifies against the current set, and a removed owner's
  signature stops being accepted at once.
- **Rollback and forks.** Clients keep the `(owner_seq, owner_head)` and
  chain they last verified for each community, and refuse any cert, route or
  pushed state whose set doesn't extend it (same head at the known seq).
  Maintainer nodes do the same against the route or listing they store (§7).

### Noise implementation

| | Go (servers, Go tools) | Python (client) |
|---|---|---|
| Library | `github.com/flynn/noise` v1.1.0 | `noiseprotocol` 0.3.1, vendored as `openarcanum._vendor.noise` |
| Test vectors | `internal/session/vectors_test.go`: 408 vectors (every 25519, single-PSK vector) | `clients/desktop/tests/test_noise_vectors.py`: 800 vectors (every vector of a pattern the library implements) |
| Fuzzing | `FuzzRecv`, `FuzzAccept` in `internal/session/fuzz_test.go` | Hypothesis, `clients/desktop/tests/test_session_fuzz.py` |

- **Why vendored.** `noiseprotocol` has had no release since 2020 and no
  maintained, reviewed Python replacement exists, so the last release is
  kept in-tree, unchanged except for relative imports and one removed
  empty backend. `clients/desktop/src/openarcanum/_vendor/noise/VENDORED.md` records the
  upstream commit, the sdist's sha256 and every local change.
- **Vectors.** Both sides run the Noise community vectors from cacophony and
  snow, checked in once under `clients/desktop/tests/vectors/noise/` (pinned by
  upstream commit and `SHA256SUMS`). Each run asserts every handshake message
  and transport ciphertext byte for byte, and the handshake hash where the
  vector has one. Both include `Noise_XX_25519_ChaChaPoly_SHA256` from each
  file.
- **Interop.** `clients/desktop/tests/test_interop.py` runs Python↔Go handshakes and
  transport in both directions, and checks that each side rejects the
  other's tampered static-key proof.

## 4. Community requests (`community.proto`)

`ClientMessage{id, body}` / `ServerMessage{reply_to, body}`. Requests are
answered in order per session; `reply_to = 0` marks a pushed event.

### 4.1 Session status

On every handshake the server checks the roll: `BANNED` (key banned),
`DEVICE_REVOKED` (this device key was revoked, or belongs to another member),
`UNKNOWN` (not on the roll), `ACTIVE`, `PENDING` (approval queue), `REMOVED`
(left/kicked/denied/replaced), or `DEVICE_LIMIT` (a new device over the
community's per-member limit). Terminal statuses are sent and the session is
closed. New device keys of active members are recorded on first connection. A
device key may have `max_sessions_per_device` (4) sessions open and a member
`max_sessions_per_member` (20); a newer session past either closes the oldest.
A member who leaves and joins again keeps a running timeout and any server
mute or deafen.

### 4.2 Claim, join, sync

- `GetInfo` → `CommunityInfo` (works for anyone; used for the join preview,
  with a throwaway key). Each session may ask 3 times at once and then once a
  second (`RATE_LIMITED` beyond that); before admission a session gets a tenth
  of a member's request rate.
- `Claim{secret, cert, settings, display_name}` makes the session's identity
  the owner member and `cert.owner_key` the community id. The server creates
  `@everyone`, `Admin`, `#general` and a 7-day invite. Wizard fields
  (name, description, tags, language, icon, visibility, join policy,
  retention, search, and `max_file_size` when not 0) are applied on top of
  the documented defaults (files: 10 MB unless the wizard says otherwise).
- `Join{join_key, display_name}` → `JoinResult{ADMITTED | PENDING | REFUSED,
  reason}`; refusal reasons: invalid, revoked, expired, used up, banned,
  full, invite required, denied. Request-only keys and the `request` policy
  give `PENDING`; the `open` policy admits without a key.
- `Sync` → `SyncState`: info, own roll entry, roles (with `hoist` and
  `mentionable`), every channel the member can read **with its key, key
  epoch and topic** (plus its `description` (the topic text), `category_id`,
  `permissions_synced`, `slowmode_seconds` and channel toggles), the
  categories (`Category{id, name, position, overrides}`) that hold a channel
  the member can read (all of them for members with Manage Channels),
  community settings,
  effective permissions, the server cert (with its owner chain), the current
  owners (`OwnerEntry{owner_key, member, display_name}`), the member's own
  `owner_invited` flag and, for owners, the pending owner invitations and
  key requests (`PendingOwner`).
- Pushed `StateChanged{state?, status?}`: whenever channels, roles, keys or
  settings change, each active session gets its own fresh state; a status
  change (approved, removed, banned) comes with it.

### 4.3 Messages

`History{channel_id, before_id | after_id, limit}`, `Post{channel_id, text,
reply_to, file_ids}`, `Edit` (own), `DeleteMessage` (own, or
`manage_messages`). The reply carries the stored `Message`, and the server
fans out a `ChannelEvent` (§6). Text is at most 4000 characters; a message
needs text or at least one file.

**Voice channel chat.** Text channels and voice channels take messages; a
voice channel's messages are its text chat (as in Discord). Everything in
this section applies to both alike: `Post`, `UploadBegin`, `Typing`,
`History`, `Search`, reactions, pins, reports, bulk delete, slowmode, the
channel's feature settings, retention, the history gate and AutoMod, with
the same permissions (View Channel, Send Messages, Read Message History,
Attach Files…). A voice channel has its own topic and key from creation
(§6), rotated like a text channel's, so its chat reaches exactly the members
who can see it; nothing new is encrypted or signed. Being in the call
doesn't matter, and Connect and Speak don't affect the chat. With voice
channels turned off for the community (§4.6), `Post`, `UploadBegin` and
`Typing` in a voice channel are refused with `RPC_ERROR_CODE_DISABLED`;
`History` still works and moderators can still bulk delete. The join and
leave channel must still be a text channel.

**Formatting** is in the text, not the protocol: clients render a CommonMark
subset (bold, italic, strike, inline code, fenced code blocks with a language
label, block quotes, lists, small headings, `[label](url)` links), `||spoilers||`
and mention tokens. Links are shown as text and URL; clients never fetch or
open them (link previews come from the server, below).

**Mentions.** Mention tokens in the text: `<@MEMBER>` (the member's identity
key, unpadded base64url), `<@&ROLE>` (a role id), `@everyone` and `@here`;
tokens inside code spans and code blocks don't count. Clients turn a typed
`@Name` (nickname or display name, or a role name) into a token before
posting. The server parses the tokens and stores, sealed with the message,
the mentions the author may make: `mentions` (members who can read the
channel; at most 100), `mention_roles` (a role that is *mentionable*, or any
role with Mention @everyone/@here and All Roles; at most 20),
`mention_everyone` and `mention_here` (Mention @everyone/@here and All
Roles). Mentions the author isn't allowed stay in the text but aren't
marked, so they notify nobody (the message is still sent, as in Discord).
Clients count a message as mentioning them when their key, a role they hold
or `@everyone` is marked, or `@here` is marked and the message arrived live.

**Attachments.** Up to 10 files per message (`MaxAttachments`). A client
uploads each file as a staged upload (`UploadBegin{…, staged: true}`, §6.3),
which answers `StagedFile{file}` instead of posting, then posts once with
`Post.file_ids`. The server checks every id is a complete staged upload by
the same member in the same channel; a staged upload nobody posts is deleted
after an hour. A member may hold 40 unposted uploads in a channel, and a
channel 400 (`RATE_LIMITED` beyond). `Message.files` carries each file's
`FileRef` (with its key).
The one-step upload (`staged: false`) still posts a message with one file.

**Reactions.** `React{message_id, emoji, remove}` adds or takes back the
caller's reaction. The reply is a `MessageUpdate{message_id, channel_id,
reactions}` (the message's reactions, never its text), and when something
changed members get the same as `ChannelEvent.update`. Reacting (and taking
a reaction back) needs the same access as reading the message in `History`:
View Channel and Read Message History in the channel, and with "new members
see history" off, a message from the member's join time on (a message the
member may not read is `NOT_FOUND`). `emoji` is either a Unicode emoji (1 to 32 characters,
not plain ASCII, no spaces, control characters or bidirectional marks,
embeddings, overrides or isolates; joiners and variation selectors are
fine) or a **custom emoji
reference** `:name:id`, where `name` is 2 to 32 of `[A-Za-z0-9_]`, `id` the custom
emoji's decimal id (`^:[A-Za-z0-9_]{2,32}:[1-9][0-9]{0,18}$`). Clients show
a custom emoji from their own lookup of the community's emoji by id, falling
back to `:name:`. A message has at most 20 different emoji and 1000
reactions in all (members times emoji; `CONFLICT` beyond, as the reactions
travel in the sealed body re-sent on every change). Adding needs Add
Reactions in the channel; taking your own back doesn't.

**Pins.** `Pin{message_id, unpin}` needs Manage Messages in the channel and
the access to read the message (as for reactions); at most 50 pins per
channel. The reply, and the `ChannelEvent.update` members get, is a
`MessageUpdate{message_id, channel_id, pinned}`. Pinning and unpinning are
recorded in the audit log (who, whose message, which channel; never the
text). `ListPins{channel_id}` → `PinList` (newest pin first) needs Read
Message History and leaves out pins the member may not read (from before
they joined, with "new members see history" off). `Message.pinned` marks
pinned messages; deleting a message unpins it.

**Message updates.** `MessageUpdate{message_id, channel_id, reactions |
pinned | preview}` is a change to a message that isn't an edit. It never
carries the message's text: everyone holding the channel key receives it,
including members who may not read that message's history. Clients apply it
to a message they already have and ignore it otherwise.

**Typing.** `Typing{channel_id}` (needs Send Messages) makes the server
publish `ChannelEvent.typing{member, name}` to the channel topic. Nothing is
stored. One session may send one per channel every 2 s (`RATE_LIMITED`
otherwise); posting a message resets that. Clients show "… is typing" for 6 s.

**Presence.** A member is online while at least one of their sessions is
open. Changes are batched: every 500 ms the server sends each admitted
session one `PresenceBatch{updates}` with every member whose state changed
since the last batch, once each with their latest state (`PresenceUpdate{member, online}`).
A burst of joins or reconnects therefore costs one message per session per
interval, not one per change per session (docs/load-tests.md).
`Member.online` in `MemberList` is the current state. A member who chose
**Invisible** (`SetStatus.invisible`, §4.11) counts as offline for everyone
else from the first moment of every session (the flag is kept in their
member record): no `PresenceUpdate` says they're online and `Member.online`
is false, except in their own `MemberList` and `SyncState.self`. The server
still sees their connection; that can't be hidden from it.

**Nicknames and profiles.** `Member.nickname` (≤ 32 characters) is shown
instead of `display_name` in this community, including as `author_name`.
It's set with the signed admin action `SetNickname{member, nickname}`: your
own with Change Nickname, someone else's with Manage Nicknames and a higher
role. `UpdateProfile{about, pronouns}` sets the caller's profile (≤ 190 and
≤ 40 characters; `about` may contain line breaks). Both appear in
`MemberList` and `SyncState.self`. Each accepted `SetProfile` or
`UpdateProfile` sends every session a new state, so a member may make at
most 3 of them in any 6 seconds (one every 2 s on average; `RATE_LIMITED`
beyond). Avatars (`SetProfile.avatar`), the community icon and link preview
images must be PNG, JPEG, GIF or WebP whose header declares at most 8192
pixels a side and 16,777,216 (4096 × 4096) in all; the server reads only
the header, never decodes them.

**Link previews.** When the community turned previews on (and the channel
didn't turn them off) and the author has Embed Links in the channel, the
server fetches the first `http(s)` link of a new or edited message (outside
code, outside spoilers, and not written as `<https://…>`) with the limits
in docs/operations.md "Link previews". It stores `LinkPreview{url, title,
description, site_name, image}` sealed with the message (the `og:image`, if
any, is fetched by the server and stored as a file of the message, served
with `FileGet` like attachments) and publishes `ChannelEvent.update` with
the preview (a preview is fetched right after the post or edit that added
the link).
Clients never contact the linked site.

**Join and leave messages.** When on, joining (admission, approval) and
leaving post a message with `system{kind: JOINED | LEFT, member}` and empty
text in the configured channel (`CommunitySettings.join_leave_channel`, 0 =
the first text channel). Kicks and bans don't post one. System messages can't
be edited.

**Feature toggles** (§4.6: the `CommunitySettings` toggles and the
channel's `attachments`, `link_previews` and `reactions` settings): a
turned-off feature is refused with `RPC_ERROR_CODE_DISABLED` ("turned off"),
whether the community or the channel turned it off, before permissions are
checked: attachments (and `attachment_kinds`: all or images only), link
previews, reactions, editing own messages, deleting own messages (Manage
Messages still deletes), typing indicators, join and leave messages.

- `History` and `Search` need View Channel **and** Read Message History in the
  channel. `FileGet` applies the same checks (and the history gate) as the
  message that carries the file; an attachment not posted yet is readable by
  its uploader only. With "new members see history" off (community or channel,
  §4.6) they return only messages created at or after the member's join
  time, unless the member can manage messages there.
- `Post`, `UploadBegin` and `UploadFinish` in a channel with
  `slowmode_seconds > 0` are refused with `RATE_LIMITED` until that many
  seconds have passed since the member's last message there; members with
  Manage Messages or Manage Channels in the channel are exempt.
- `SetProfile` with a new display name needs Change Nickname.
- `VoiceJoin` needs the voice toggle on, View Channel and Connect. A
  member without Speak joins listen-only: their grant allows no audio
  (the relay forwards none) and others see them as `listen_only` (§12).

### 4.4 Admin actions

`SignedAction{action, signature}` wraps `AdminAction{community, actor, at,
nonce, op}`. The server requires `actor` = the session identity, a valid
signature, `community` = its owner key, `|at - now| ≤ 5 min`, and an unseen
nonce; then checks permissions (role hierarchy: you act only on roles and
members below your highest role; owners outrank everyone).
Every applied action is stored verbatim in the audit log, so anyone allowed
to read the log can verify who signed what.

Ops: settings (including feature toggles, §4.6), join keys (create/revoke;
label, expiry, max uses, roles, request-only, public), channels
(create/edit/delete; topic text, category, slowmode, retention overrides,
channel toggles, sync with the category), categories (`CreateCategory`,
`EditCategory`, `DeleteCategory`, `ReorderCategories`), channel order
within a category (`ReorderChannels`), roles (create/edit/delete/reorder;
color, `hoist`, `mentionable`), overrides (`SetChannelOverride` on a
channel or a category, for a role or one `member`), role assignment, kick,
ban/unban, approve/deny, raid revoke, replace member, owner set (invite,
submit owner key, change owners; §4.5), server-key rotation (prepare → an
owner signs the cert → commit), server backup, nicknames (`SetNickname`,
§4.3), and the moderation and emoji ops of §4.7 to §4.10 (timeout, bulk
delete, AutoMod rules, closing reports, custom emoji). The messaging
toggles are ordinary settings updates (§4.6) and channel edits (`EditChannel` attachments, link_previews, reactions); the
audit summary names each toggle changed.

Rules the server applies beyond the permission each op needs:
- **Hierarchy.** Roles strictly below the actor's highest role (owners
  outrank all) may be edited, deleted, moved, assigned or removed, and only
  members below the actor may be acted on. A role's permissions may change
  only in bits the actor holds (granting or taking away). Besides position,
  assigning or removing a role (`AssignRoles`, and `CreateJoinKey` with
  `role_ids`), editing, deleting or moving it needs the actor to hold every
  permission the role carries, so a role with Administrator is handled by
  owners and administrators only. Only owners and administrators act on a
  member holding a role with Administrator.
- **Overrides** need Manage Channels and Manage Roles; the target role must
  be below the actor (or @everyone), the target member below the actor, and
  every changed bit one the actor holds. Only channel bits (§4.6) are
  allowed. Setting a channel's own override unsyncs it from its category;
  setting a category's override rewrites its synced channels.
- **Invites.** `CreateJoinKey` needs Create Invite, plus Manage Roles when
  it grants roles and Manage Community when public.
- **Relays.** An `UpdateSettings` that changes `relay_mode` or
  `pinned_relays` needs an owner (`PERMISSION_DENIED` otherwise, even with
  Manage Community); the audit entry names "relays". `ListJoinKeys` returns
  every key (with `created_by_name`) to Manage Community holders and the
  caller's own keys to Create Invite holders; `RevokeJoinKey` works on your
  own keys, or any with Manage Community. Raid revoke needs Manage
  Community and Kick.
- **Ordering.** `ReorderCategories` must list every category, and
  `ReorderChannels` every channel of that category (0 = uncategorized), top
  first; `DeleteCategory` moves its channels to the end of the
  uncategorized list, unsynced.

`AuditQuery{before_id, limit, action, actor, target, since, until}` →
`AuditList{entries, more, next_before_id}`. The filters combine with AND:
`action` is an op name (the `AdminAction` oneof field, e.g. `ban`, or
`automod` for entries the server wrote itself), `actor` and `target` are
member keys (the target of `kick`, `ban`, `unban`, `approve`, `deny`,
`assign_roles`, `timeout`, `invite_owner`, `set_nickname`, `voice_mute`,
`voice_deafen`, `voice_move`, a member's `set_channel_override` and the old
key of `replace_member`), `since`/`until` are unix ms (inclusive/exclusive). The
server decrypts at most 2000 rows per request (the log is sealed at rest);
when it stops early, `more` is set and `next_before_id` continues the scan.
Each `AuditEntry` carries `action_name`, `target`, `target_name`, and
`system = true` (with no signed action) for AutoMod's own entries. With
`audit_visibility` EVERYONE, a `save_automod_rule` entry carries its signed
`action` (which holds the rule's keywords and patterns) only for members
with View Audit Log; others get the summary, which names the rule only.
Entries about a channel or category a member can't see (its creation, edits,
overrides, pins, deletions and bulk deletes there) are left out for members
without View Audit Log.

### 4.5 Owner set changes

An **owner** is a member holding a key in the current owner set (§3.1); the
server keeps which member holds which key (the founder's member is recorded
at claim, later ones come from `OwnerAdd.request.member`). Owners hold every
permission, outrank everyone, can't be kicked, banned or replaced, and can't
leave until they are removed from the set or have asked to be (step 4).

1. `InviteOwner{member}`: an owner invites an active member. The member's
   `SyncState.self.owner_invited` becomes true. `InviteOwner{member, cancel}`
   withdraws it (an owner, or the member declining).
2. `SubmitOwnerKey{add}`: the invited member's client generates an owner key
   on that device and sends the `OwnerAdd` for it. Only the public key leaves
   the device. The server checks both signatures and that the request names
   this member and this community, and shows it to owners in
   `pending_owners`.
3. `ChangeOwners{change, cert}`: an owner signs the next `OwnerChange` with
   their own owner key (an `add` of a submitted request, or a `remove`) and a
   new `ServerCert` for the current server key, with a higher `seq`, under the
   changed set. The server appends the change to its chain (it ignores any
   chain the client attached), verifies the change and the cert, and stores
   both. Then it pushes the new state and republishes its route and listing.
4. **Removing yourself:** `ChangeOwners{change}` whose change removes the
   actor's own key, signed by it, with no cert (the actor can't sign one
   under a set without them; an attached cert is ignored). The server checks
   the change against the current set and that another owner who isn't
   leaving remains, then at once stops treating the member as an owner
   (no owner rights, no roles) and marks them `OwnerEntry.leaving`. The
   chain and cert don't change yet. Any other owner's client that sees a
   `leaving` entry signs its own `remove` of that key with a new cert (step
   3); until one does, the key is still in the chain.

**Removal takes everything ownership gave.** When a key is removed (step 3)
or its holder asks to leave (step 4), the member keeps no roles (the
founder is given the Admin role at claim) and every channel they could read
only as an owner gets a new key (§6.2). An owner can then give the member
roles again like anyone else's.

The server refuses each step from a member who isn't an owner (or, for step
2, isn't invited), from a removed or leaving owner, with a change or cert
signed by a key other than the acting member's own owner key, and with any
bad signature.
`PrepareKeyRotation`/`CommitKeyRotation` and `AdoptServer` (§8) accept a cert
from any current owner, verified against the server's chain. The
`owner_key` device-sync event (§10.2) copies a new owner key to the member's
other devices; backups include owner keys.

### 4.6 Permissions and feature toggles

Permission bits (`internal/community/perms.go`, mirrored in
`clients/desktop/src/openarcanum/permissions.py`):

| Bits | Permissions |
|---|---|
| 0-8 | View Channel, Manage Channels, Manage Roles, Manage Community, View Audit Log, Create Invite, Change Nickname, Manage Nicknames, Approve Members |
| 12-14 | Kick, Ban, Timeout Members |
| 20-28 | Send Messages, Embed Links, Attach Files, Add Reactions, Use Custom Emoji, Mention Everyone, Manage Messages, Read Message History, Manage Emoji |
| 32-40 | Connect, Speak, Video, Screen Share, Use Voice Activity, Priority Speaker, Mute Members, Deafen Members, Move Members |
| 48 | Administrator |

Overrides may change View Channel and the text and voice bits only. A
member's channel permissions: base (@everyone | roles; owners and
Administrator get everything and skip the rest), then the @everyone
override (deny, allow), then the member's role overrides (OR of
denies applied, then OR of allows, as in Discord), then the member's own
override (deny, allow);
without View Channel, nothing. Schema 4 moved the earlier bit layout to
this one (`schema_v4.go`).

Feature toggles are `CommunitySettings` fields 20-32 (`optional bool`,
absent = the documented default; the server stores them all) plus
`search_enabled` and `allow_contact_cards`, with `join_leave_channel` (33)
for where join and leave messages go; channel toggles are
`Channel`/`EditChannel` `attachments`, `link_previews`, `reactions` and
`history_for_new_members` (`CHANNEL_TOGGLE_INHERIT` or `_OFF`).
Precedence: community off → the server refuses with `DISABLED` everywhere;
on → the channel's setting applies (off → `DISABLED` in that channel);
allowed → permissions decide (`PERMISSION_DENIED`). Every change is an
audited `UpdateSettings` or `EditChannel`.

### 4.7 Moderation: kick, ban, timeouts, screening, bulk delete

The permissions are §4.6's Timeout Members, Kick, Ban, Manage Messages,
Manage Emoji and Use Custom Emoji (a channel bit; on for `@everyone` by
default).

- **Kick / ban** (`Kick`, `BanMember{member, reason, delete_message_seconds}`):
  Kick or Ban permission, and the actor must outrank the target (highest role
  strictly above; owners can't be targeted). `delete_message_seconds` is one
  of 0, 3600, 21600, 86400, 259200, 604800; the server then deletes every
  message the member posted in that window, in every channel: the body is
  resealed with only its author (as a single delete), attachments, link
  preview images, pins and search entries go, and each channel gets one `ChannelEvent.deleted_many`.
  `AdminResult.deleted` counts them.
- **Network blocks** (`BanMember{…, block_network, block_seconds}`;
  code `internal/community/netblock.go`). Only on a community whose relays
  are pinned (community setting, or the node config) and all said in their
  `Hello` that they're dedicated to this server (§2 "Dedicated relays");
  `SyncState.dedicated_relay` tells clients so. Otherwise the whole ban is
  refused ("isn't on a dedicated relay"). The actor needs Ban (checked
  before anything else), `block_seconds` is 86400, 604800, 2592000 or 0
  (until the ban is lifted). The server records the request (sealed
  `net_blocks` row, schema 9). After the ban is applied, and before the
  member's sessions end, it pushes `StateChanged{status: BANNED}` to them,
  then sends each dedicated relay the member is connected through a
  `NetworkBlock` naming their session streams there, and records the
  returned block id (never an address; the server never learns one).
  `AdminResult.network_block` is `BLOCKED`; `PENDING` when the member
  wasn't connected (or a relay didn't answer): the next session the banned
  member opens through a dedicated relay is refused as banned and its
  stream is blocked then; `SKIPPED` when the member's network was the
  server's own. Audit entries (op `network_block`) name the member and the
  block id. `Unban` drops pending requests and lifts held blocks with
  `NetworkUnblock` by id (op `network_unblock`); a relay that can't be
  reached is asked again whenever the server attaches to it. `ListBans`
  reports each ban's `network_block` and `network_block_until`.
- **Timeout** (`TimeoutMember{member, seconds, reason}`): Timeout Members,
  hierarchy as above, never owners or administrators; `seconds` is 60 to
  2419200 (28 days), 0 lifts it. Until `Member.timeout_until` (unix ms) the
  member's effective permissions are only View Channel, Read Message
  History and Connect (`PermTimeoutMask` takes away every other bit, the
  management and moderation ones included, so a timed-out moderator can't
  kick, ban or change roles or settings), so `Post`, `Edit`, uploads,
  `React`, `Typing` and custom emoji are refused with "You're timed out
  until … UTC", and so are a `SetProfile` that changes the display name or
  avatar, an `UpdateProfile` that changes the about text or pronouns, and a
  `SetNickname` for themselves. In a call they become listen-only with no camera or screen
  (`voice.go` rechecks call permissions after every admin action and every
  10 s). Reading, listening, reporting and deleting their own
  messages still work. `timeout_until` is sent only to the member and to
  members with Timeout Members; the server pushes fresh state when a
  timeout ends.
- **Member screening** (`CommunitySettings.screening_minutes`, 0 = off, at
  most 10080): until `joined_at + screening_minutes`, a member without any
  role (and not an owner) can't post, react, attach, use custom emoji or
  fetch other members' contact cards (start DMs); the error names the time,
  and `SyncState.self.can_post_at` carries it.
- **New-member restrictions** (`CommunitySettings.new_member_minutes`, 0 =
  off, at most 10080): until `joined_at + new_member_minutes`, a member
  without any role (and not an owner) loses Attach Files, Embed Links and
  Mention Everyone (`PermNewMemberMask`), `UploadBegin` is refused, and a
  `Post`, `Edit` or file caption with a link (`http://`, `https://`) or any
  mention (`<@…>`, `<@&…>`, `@everyone`, `@here`, outside code) is refused;
  the errors name the time.
- **Raid mode** (`CommunitySettings.lockdown`, Manage Community like any
  setting, audited as "raid mode on/off"): while on, every `Join` (with an
  invite or public join key too) is `PENDING` until approved, and
  `CreateJoinKey` is refused for everyone. Turning it off admits nobody.
- **Bulk kick / ban** (`BulkRemove{members, ban, reason,
  delete_message_seconds}`): 1 to 100 members, each checked as for `Kick` or
  `BanMember` (permission, hierarchy, not an owner, not yourself) before any
  is removed, so either all go or none; a ban may delete their messages from
  the same windows as `BanMember`. One audit entry ("banned 3 members: …");
  `AdminResult.removed` and `deleted` count them.
- **Bulk delete** (`BulkDelete{channel_id, message_ids | last}`): Manage
  Messages in that channel; at most 100 messages, either listed (all in that
  channel) or the channel's `last` newest. One audit entry, one
  `deleted_many` event.

### 4.8 AutoMod

`SaveAutoModRule{rule}` (id 0 creates), `DeleteAutoModRule{id}` and
`ListAutoModRules` → `AutoModRuleList` need Manage Community. A rule has a
name, `enabled`, one trigger, actions and exemptions:

| Trigger | Fields | Fires when |
|---|---|---|
| `KEYWORD` | `keywords` (≤ 1000, ≤ 60 chars), `patterns` (≤ 10 RE2 regexes, ≤ 260 chars, case-insensitive) | a keyword matches a whole word or phrase (`word*` a word prefix, `*word` a suffix, `*word*` anywhere), or a pattern matches |
| `MENTION_SPAM` | `mention_limit` (1 to 50) | more distinct mentions than the limit: mention tokens (`<@member>`, `<@&role>`, §4.3) and typed `@names`, outside code (`@everyone` and `@here` count) |
| `LINKS` | `allowed_domains` (≤ 100) | a `scheme://host` or `www.host` link whose host isn't an allowed domain or a subdomain of one (empty list: every link) |

Actions: `block` (the post, edit or caption is refused with "AutoMod blocked
your message: <why> (rule “<name>”)"), `alert` (a report of kind
`AUTOMOD` in the queue, pushed as `ModAlert`; it references the message id,
or 0 when the message was blocked and never stored; at most 5 alert reports
per message author per 10 minutes, and none while 5000 reports are open;
the message is still blocked or timed out), `timeout_seconds`
(the sender is timed out; a system audit entry records it). At least one
action is required. Exempt roles and channels skip the rule; nobody else is
exempt (owners and admins included). Patterns are validated when saved:
syntax, length, at most 1000 compiled instructions each and 5000 across all
rules, and a pattern that matches the empty string is refused. Rules apply
to `Post`, `Edit` and file captions (`UploadBegin`, re-checked at
`UploadFinish`), after the permission checks (a member without Send
Messages or Attach Files gets the permission error, and AutoMod doesn't
act). The server never logs message text; alerts carry ids.

### 4.9 Reports

`ReportMessage{message_id, reason}`: any member who can read the message
(the same access as `History`: Read Message History, and the join-time rule
when new members don't see history), not their own; `reason` at most 500 characters; one report per member per
message; at most 5 reports per member per 10 minutes (in memory) and 20
open at once per member (`RATE_LIMITED` until moderators close some); the
queue refuses new member reports at 5000 open ones. `ListReports{include_closed,
before_id, limit}` → `ReportList` and `ResolveReport{id, dismiss}` (an
audited admin action) need Manage Messages, the queue's permission. A
`Report` shows the reporter, the message (id and channel, for jump-to), its
author, the reason or the AutoMod rule and action, and who closed it.
Members with Manage Messages get a pushed `ModAlert{report}` for every new
report and AutoMod alert. Moderators read the message itself through
`History`, with their normal permissions.

`ModQueue{open_reports, pending_members}` says how much waits in the
moderator queues, so clients can badge the way to it. Each count is filled
only for a member allowed to see that queue: `open_reports` (open member
reports and AutoMod alerts together) with Manage Messages,
`pending_members` (the approval queue) with Approve Members; the other is
0, and a member with neither permission gets no `ModQueue` at all. It is in
`SyncState.mod_queue` (so on connect and in every `StateChanged`), and the
server pushes a fresh `ModQueue` to every eligible session whenever a count
changes: a new report or alert, a resolved or dismissed report, a join
request, an approval, and a retention purge that deleted a month's
reports. Denials, kicks and bans of pending members change the roll and
arrive in `StateChanged`.

### 4.10 Custom emoji

`SyncState.emoji` lists `CustomEmoji{id, name, animated, mime, size,
version}` (empty while the community's custom emoji toggle,
`custom_emoji_enabled`, is off).
`GetEmoji{id}` → `EmojiImage{id, image, mime, version}`: images travel over
the session, never as URLs. Uploading is `EmojiUpload{action, image}`: the
signed `AdminAction` holds `AddEmoji{name, sha256, size}` and the image goes
next to it, so the audit log keeps the hash, not the image. Manage Emoji is
required; names are `[a-z0-9_]{2,32}` and unique; images are PNG, GIF or
WebP (animated allowed), at most 256 KiB and 128×128 px as read from the
image header; the community holds at most `max_custom_emoji` (1 to 250,
default 50). `RenameEmoji` and `DeleteEmoji` need Manage Emoji too.
Messages and reactions refer to an emoji as `:name:id`; using one needs Use
Custom Emoji in the channel, and a reaction with a reference to an emoji
the community doesn't have is refused (`NOT_FOUND`). With the toggle off, uploads, renames,
`GetEmoji` and messages using an existing emoji are refused with
`RPC_ERROR_CODE_DISABLED` "custom emoji are turned off in this community";
deleting still works.


### 4.11 Statuses

A member's status (state, custom text, expiry) is end-to-end encrypted by
their client (§9.8); the server only stores and forwards opaque bytes and
reads one flag. Requests (all need an admitted member):

- `SetStatusDevice{entry, device}`: register (or replace) one of the
  caller's devices that can receive statuses here. `entry` is a serialized
  `SignedStatusDevice` (dm.proto); the server checks it parses, names the
  caller and `device`, and is signed by the caller's member key (clients
  check the same before trusting it), then stores it opaquely. Empty `entry`
  removes the device. At most 10 per member; an unchanged entry (sent on
  every connect) changes nothing and announces nothing. Revoking a session
  device (§10.4) removes the entries it registered.
- `SetStatus{blob, invisible, keep_blob}`: `blob` is the caller's status for
  this community's audience, `FDM2 || Envelope` holding a Megolm message
  (≤ 4 KiB; empty = not shared here). `invisible` is the Invisible flag
  (§4.3 "Presence"); `keep_blob` changes only the flag. At most 10 in any
  20 s per member (`RATE_LIMITED`).
- `ListStatuses{after}` → `StatusList{entries, invisible, more}`: every
  admitted member's `StatusEntry{member, devices, blob}`, 500 (or 4 MiB) a
  page in member-key order, and the caller's own flag.
- `SendStatusKeys{drops}`: up to 500 `StatusKeyDrop{member, device,
  envelope}` (`FDM2 || Envelope` holding an Olm message, ≤ 4 KiB each) for
  recipient devices; a new drop from the same sender for the same device
  replaces the waiting one. Drops for non-members, and for any device that
  isn't one of the recipient's registered status devices (`SetStatusDevice`),
  are discarded; removing a status device deletes the drops waiting for it.
  So a recipient holds at most one drop per sender and registered device,
  and no sender's drops push out another's. 120 requests a minute per
  member; 250,000 waiting in all (refused beyond), each for 7 days.
  Recipients online get theirs pushed at once as `StatusKeys`.
- `FetchStatusKeys{ack, device}` → `StatusKeys{items, more}`: the drops
  waiting for one of the caller's devices (up to 500), after deleting those
  in `ack` (only the caller's own).
- `RequestStatusKey{member, device}`: "my device can't read this member's
  status", pushed to that member's sessions as `StatusKeyRequest{member,
  device}` (30 a minute per member). Clients don't send it while Invisible.

Pushed: each `PresenceBatch` also carries `statuses`, the current
`StatusEntry` of every member whose devices or blob changed since the last
batch (an emptied entry for a member who left or was removed: their devices,
blob and waiting drops are deleted). At rest the server keeps everything
sealed (`statuses`, `status_keys`, schema 10; docs/threat-model.md
"Statuses"), keyed by lookup hashes.

## 5. Pasted codes (`invite.proto`)

| Prefix | Message | Printed by |
|---|---|---|
| `openarcanum-invite:` (also `openarcanum://invite/`) | `Invite{route_id, owner_key, server_key, relays, join_key, name}` | the client, after creating a join key |
| `openarcanum-claim:` | `ClaimCode{server_key, route_id, relays, secret}` | `community init` |
| `openarcanum-move:` | `MoveCode{server_key, route_id, relays, secret, owner_key}` | `community restore` |

The body is unpadded base64url of the serialized message.

## 6. Channel fan-out, keys, files

### 6.1 Envelopes

For each event the server builds `ChannelEvent{message | edited | deleted |
typing | update}` (`update`: a `MessageUpdate` for reactions, pin state or link
preview changed, without the message text;
`typing`: live only, never stored),
encrypts it with ChaCha20-Poly1305 under the channel key with
`aad = "openarcanum/channel-event/v1" || channel_id (8 BE) || key_epoch (4 BE)`
into a `ChannelEnvelope`, signs that with the server key into a
`SignedEnvelope`, and publishes it **once** to the channel topic on every
relay it's attached to. Clients drop anything that isn't signed by the
server key they accept, or that they can't decrypt.

### 6.2 Rotation

When anyone loses read access to a channel (kick, ban, leave, role change,
override, role edit or deletion, raid revoke, replace member), that channel
gets a new key, key epoch **and** topic, pushed to the remaining readers
through `StateChanged`. Clients keep the last two epochs for in-flight
messages.

The same happens when a member is admitted (join, or approval of a
request) to a channel they can read but whose history is hidden from them
("New members see history" off for the community or the channel, and no
Manage Messages there), if anything was published to the channel within
the last hour (the longest a relay chunk cache keeps a message): the new
member only ever learns the new key and topic, so envelopes from before
they joined that a relay still caches can't be fetched or read with what
they hold. The members already in get the new state through
`StateChanged`.

### 6.3 Files

`UploadBegin{channel_id, name, size, mime, text, staged}` is refused up front over the
community's limit (≤ 25 MB; 10 MB by default), when attachments are turned off (community or
channel), or, with images only allowed, for a non-image type (the content is
sniffed again at `UploadFinish`: PNG, JPEG, GIF or WebP). `UploadChunk{upload_id, offset, data}` (1 MiB)
and `UploadFinish{upload_id, sha256}`. Unfinished uploads are held in the
server's memory, so a member may have at most 4 and 50 MB (announced
sizes) in flight, the server 512 MiB in all (`RATE_LIMITED` "the server is
busy with other uploads" beyond), and an unfinished upload is dropped 3
minutes after its last chunk or 15 minutes after it began. The server encrypts each 1 MiB chunk
with a random per-file key (`nonce = 0^8 || chunk index (4 BE)`,
`aad = "openarcanum/file-chunk/v1" || file_id (8 BE) || index (4 BE)`) and posts
a message whose `FileRef` carries that key. `FileGet{file_id, chunk}` returns
the stored ciphertext, so clients decrypt it themselves; downloads follow the
community's bandwidth limit.

### 6.4 At rest, retention, search

Server storage is not part of the wire protocol; this summarizes
`docs/operations.md` "Data at rest". Every stored row is sealed
(XChaCha20-Poly1305, random nonce, AAD binds each value to its table, column
and row): the member roll, roles, channels and their keys, join keys, bans,
devices, avatars, the audit log, contact cards, key packages and settings
under the storage key, as are categories (schema 4) and pins (`pins.data`:
who pinned, when; schema 5); message bodies (text, attachments, mentions,
reactions, link preview, system event) and file metadata (including each
file's chunk key) under a per-month key. Public keys are stored only as
keyed lookup hashes. The few plaintext columns that decide access (a
message's or file's channel, the message a file belongs to, a pin's
channel, a join key's secret hash, use count and revocation, a key
package's device) are bound to the row's sealed data and checked on every
read. SQLite runs with `secure_delete`. A purge job deletes
messages and files at or past their retention (channel override, else
community setting; 0 keeps forever), then deletes the key of every month
that is entirely past the longest retention in effect.
Search is an in-memory FTS5 index rebuilt from the encrypted rows at
startup; results are filtered by the searcher's read permission. A
`SearchQuery.query` is at most 256 bytes (longer is refused); the server
uses its first 5 words, each at most 64 bytes, and matches a word as a
prefix only from 3 characters (shorter words match whole words). A search
runs for at most 2 seconds and then fails with `RATE_LIMITED` ("that search
took too long"). Posting, editing and deleting never wait for a search: the
index changes are queued and applied before the next search runs.

## 7. Maintainer nodes (`meta.proto`, HTTP + protobuf)

| Endpoint | Body → reply |
|---|---|
| `POST /v1/listings` | `SignedListing` (signed by the server key, with a cert for it from a key in the community's owner set, §3.1). Rejected if unsigned, altered, uncertified, under an owner set that doesn't extend the stored listing's (403), not newer than the stored one (409, see "Ordering" below), with a seq more than 24 h ahead of the node's clock (400), or delisted. `withdrawn` removes it. |
| `GET /v1/listings/{community}` | `SignedListing` |
| `GET /v1/search?q=&tag=&limit=` | `SearchResponse`: signed listings, so clients verify them, and the `CommunityStatus` of each listed community the node has one for (§7.4); queries are never logged |
| `GET /v1/directory/snapshot?after=` | `DirectorySnapshot{listings, next, more, total, generated_at}`: one page of every listed community's signed listing, without icons (§7.2) |
| `POST /v1/routes`, `GET /v1/routes/{route_id}` | `SignedRoute`. The first community to register a route id keeps it; updates need a cert from an owner of the same community, an owner set that extends the stored route's, and to be newer (see "Ordering" below); a seq more than 24 h ahead of the node's clock is refused. A reaped route id stays bound to its community: only that community can re-register it (403 for any other, §7.4). Clients verify a route against the community they expect and ask the next node when it doesn't verify |
| `POST /v1/communities/heartbeat` | `SignedCommunityHeartbeat` from a community server (§7.4): checked against the server key of the route stored for its route id (404 without a route, 409 for another key, 403 forged or delisted), at most 1 min ahead of the node's clock (400); 410 once the community was reaped |
| `GET /v1/communities/{route_id}/status` | `CommunityStatus{route_id, state, last_seen, reaped_at, heartbeat, reap_after_seconds}` (§7.4); 404 if the node knows nothing of it |
| `POST /v1/relays/heartbeat`, `GET /v1/relays` | `SignedHeartbeat` / `RelayList` (fresh within `relay_ttl`); the URL is put in canonical form (§7.3; 400 if it isn't a relay URL), which is what the registry stores, compares and lists; a URL is listed only once it serves the heartbeat's key (proof of control, §7.3; 403 otherwise) and is on a public address (400; `allow_private_relays` on development nodes); a URL stays bound to its first proven key while fresh (409 for another); 403 if the operator removed the URL's host. Claimed `capacity` is capped at 1 000 000 and `connections` at `capacity` |
| `POST /v1/mailbox/drop` | `MailboxDrop{inbox, blob}`: no credentials; per-inbox rate limit and quota (507 "inbox is full"), the node's total mailbox storage (507), and a byte budget per client network per day (429) |
| `POST /v1/mailbox/fetch`, `/delete` | `MailboxFetch{token, after_id}` → `MailboxItems{items, used_bytes, quota_bytes, epoch}` / `MailboxDelete{token, ids, gids}`. The inbox address is `SHA-256("openarcanum/inbox/v1" || 0x00 || token)`, so only the token holder can read or delete. Each item carries its global id `gid` = `SHA-256("openarcanum/mailbox-item/v1" || 0x00 || inbox || blob)`, the same on every node of a maintainer cluster; `gids` deletes items read on another member (§7.5). The same bytes dropped twice into one inbox are one item |
| `POST /v1/nameplates` | `NameplateRequest{relay}` → `Nameplate{number, topic, relay, expires_at}` (a random unused six-digit number, in a maintainer cluster ending in the member's position; 5-minute lifetime; `relay` in canonical form, §7.3) |
| `POST /v1/nameplates/claim` | `NameplateClaim{number}` → `Nameplate`, **once**; expired → 410, used → 410, unknown → 404; claims are rate limited per client network like creates (429); failed claims are also bounded node-wide (200 per minute), and while that bound is reached every claim answers 429 |
| `GET /v1/replicate?cursor=` | `ReplicationBatch` of listings, routes, relay heartbeats, community heartbeats and maintainer sets changed after the cursor, in revision order, with the node's replication `epoch`; a batch ends before 4 MiB of entries (at least one entry) and skips entries over their size bound. Peers pull and re-verify everything. Route ids are not secret: this public endpoint lists every route, private communities' included (a route names a server key and relays, never content or join keys) |
| `GET /v1/node` | `NodeInfo{node_key, noise_key, directory}`: the node's identity for maintainer clusters (§7.5), and whether it serves the public directory (§7.2) |
| `GET /v1/maintainers?operator=` | `SignedMaintainerSet`: the newest member list of the node's cluster (or of `operator`); 404 if it holds none (§7.5) |
| `GET /v1/peer` | WebSocket upgrade carrying a Noise XX peer session between cluster members (§7.5); refused unless the caller's node key is trusted; at most 4 handshakes under way per client network and 64 in all (429), each within 10 s |

**Versions.** Every `/v1/` request carries `OpenArcanum-Protocol: <versions>`
(comma-separated, all the client speaks); the node answers with
`OpenArcanum-Protocol: <chosen>`. With no version in common (or no header)
the node answers `426 Upgrade Required` with its own list in the header and
a sentence saying which side is outdated; clients turn it into "Update
OpenArcanum" or "this maintainer node is outdated" (§1). `/healthz` needs no
header. Community servers, relays (heartbeats) and peers (replication)
send the header too.

**Sizes and unknown fields.** A node parses listings, routes and heartbeats
keeping only the fields it knows and stores, serves and passes on that
re-encoding, so unknown fields never travel. Each kind has a size bound,
which is also the request body limit of its endpoint: a listing 640 KiB
(icon included), a route 256 KiB, a relay or community heartbeat 2 KiB
(413 above). Listing strings are bounded: name 400 bytes, description
4000, at most 10 tags of 64 bytes, language and member range 64, public
join key 512, at most 16 relay URLs (§7.3 bounds each); a route has 1 to 16
relay URLs. Other write endpoints take at most 4 KiB (64 KiB for a mailbox
delete, `mailbox_max_item` plus 4 KiB for a drop). A route, listing or
heartbeat without a publish time (`published_at`/`at` = 0) is stale (410).

**Icons.** A listing signs the SHA-256 of the community icon
(`Listing.icon_sha256`); the icon itself travels next to the signed bytes
in `SignedListing.icon` (at most 256 KiB) and must hash to it (403
otherwise). Search results and `GET /v1/listings/{id}` carry it; snapshots
leave it out.

**Ordering.** A listing or route replaces the stored one when it is under a
later owner set (higher `owner_seq`, extending the stored chain), whatever
the seqs, or under the same set with a higher `seq`. Servers use the publish
time in unix milliseconds as `seq`, and a node refuses a `seq` more than
24 h past its own clock (`MaxSeqAhead`). So a removed owner who posts with a
huge `seq` to a node that hasn't seen the removal yet can't freeze the
entry there: the current owners' next post, under the later set, replaces
it.

**Replication.** A node pulls each peer's batch and stores what verifies.
If an entry fails to store for a reason other than a routine refusal (a
database error), the node keeps its cursor for that peer and pulls the same
batch again next round; refused pulls, unreadable replies and batches that
don't parse are logged (never their content). The cursor is a position in
the peer's revision sequence, which goes back when the peer is restored
from a backup; every batch carries the peer's replication `epoch`, a random
value the restore changes, and a puller that sees another epoch than the
one its cursor came from starts that peer over from cursor 0 (what it
already holds is refused as routine duplicates).

### 7.1 Mailbox strategy

Mailboxes hold what can't be delivered live: DM and device-group messages
for offline devices, first-contact messages, room keys and file locations.

- **Write to every cluster, read every node.** A client drops each item into
  every maintainer cluster and independent node it uses (its configured
  list, the built-in list by default), in parallel: an independent node
  gets its own copy, a cluster its two healthiest members (§7.5: members
  replicate to each other). The drop succeeds if at least one node
  accepted it. A receiving device reads every independent node and the
  healthiest member of each cluster on every poll, and every reachable
  member each minute and at once after a node failed (a cluster isn't
  trusted to hold everything: a sender may have reached only another
  member, and replication may be cut). It reads page by page from its own
  position on each node (item ids on a node only grow and are never reused,
  so `after_id` never skips new mail; after a node is restored from a
  backup its `epoch` changes and the client starts its position on that
  node over), handles each item once (by its global id, which the client
  computes itself, before decryption; then by the encrypted envelope's own
  ids, §9.3), and deletes it from the node it came from once handled, and
  by global id from the other reachable members of that node's cluster.
- **Nothing is deleted unread.** An event whose room key hasn't arrived yet
  (the key went to a node that's down now) stays on its node. The client
  holds it in memory for up to 10 minutes and reads it again from the node
  after that, or after a restart, until the key arrives; then it's shown and
  deleted.
- **Replication only inside a maintainer cluster.** Independent nodes don't
  copy mailboxes to each other. The members of one maintainer cluster
  (nodes run by parties who trust each other, defined by a signed member
  list, §7.5) replicate items and deletes both ways, so a sender that
  reaches only one member and a recipient that reaches only another still
  meet, and the root node is no single point of failure. The cost: every
  member sees which inboxes receive mail and when, as the node the sender
  used does. Inbox addresses are hashes of a token only the recipient
  holds, so nodes can't link an inbox to a person.
- **Storage.** Items are kept in the node's database (they survive
  restarts) until fetched and deleted, or for `mailbox_ttl` (14 days), within
  a per-inbox quota (`mailbox_quota`, 100 MiB; items up to
  `mailbox_max_item`, 2 MiB) and drop rate (`mailbox_drops_per_minute`).
  Inbox addresses are free to pick, so the node also bounds all mailboxes
  together (`mailbox_store_max`, 507 when full) and what one client network
  (IPv4 address or IPv6 /64) drops per rolling 24 hours
  (`mailbox_bytes_per_ip`, 429).
- **Restores.** Item ids are positions, so they must not go back when a
  node is restored from a backup taken earlier: the restore moves the id
  sequence 2^32 past the backup's highest id (past any id the lost node
  could have issued since the backup) and gives the node a new random
  `MailboxItems.epoch`. A client that sees a node's epoch change resets
  every position on that node to 0 and reads it again; items it handled
  before are recognised as duplicates.
- **When a node fails.** Down at send time: the other nodes take the copy
  (and the sender only fails if every node refused). Down at fetch time: the
  receiver reads the others and tries it again on the next poll; items it
  already handled from elsewhere are recognised and deleted when it comes
  back. A message readable only with a key that's on the node that's down
  waits on the node it was read from until that node is back. Lost its disk: its copies are gone, the other nodes' copies remain.
  An item is lost only if every node the sender used loses it before the
  receiver fetches it, or it outlives the TTL (a device offline for more
  than 14 days). That message doesn't reach that device; if it was a room
  key, the device can't read that session's messages (they wait on their
  nodes until the TTL), and reads the group again from the sender's next
  session (a new one after 100 messages, a week, or a change of devices;
  §9.3).
- **Different node lists.** Sender and receiver need at least one node (or
  one cluster) in common; everyone on the default list shares all of them.
  A user who replaces the list entirely should keep at least one widely used
  node.

The DM blob store (`/v1/blobs/create`, `/chunk`, `/fetch`, `/ack`) is
described in §11.

### 7.2 Directory snapshot (local search)

**A node without a directory.** An operator can switch a node's public
directory off (`directory_enabled`, `meta directory disable`). Its listing
posts, search, snapshots and `GET /v1/listings/{id}` then answer `501`
with "directory disabled"; it deletes the listings it held, serves none in
`/v1/replicate` and stores none it pulls. Routes are unaffected, so
invites and members keep working. `NodeInfo.directory` (and a maintainer
set's `no_directory`) tell clients: they search and download snapshots only
through nodes that list, and when no node they use has a directory they
hide finding communities and the public listing options ("Public listing
isn't available on your maintainer network; share invites to let people
join"). A community server that gets 501 for its listing sends that node
none for an hour, without counting it as a failure.

Clients can search a local copy of the directory instead of asking a
maintainer (docs/architecture.md §11). With the user setting "Search the
directory on this device" on, the client downloads
`GET /v1/directory/snapshot?after=N` page by page from the first
maintainer that answers and searches the copy itself, so no maintainer
sees its queries (only that it downloaded the directory).

- **Pages.** Listings in a stable order (the node's row id), at most 500
  listings or 4 MiB per page; `next` is the cursor for the following page
  and `more` says whether one follows. Withdrawn and delisted communities
  aren't included.
- **Verification.** There is no signature over the whole snapshot (no
  maintainer key exists to sign it, and a maintainer could always leave
  entries out); instead every listing is verified like a search result:
  the server key's signature, certified through the community's owner
  chain (§3.1). Listings that don't verify are dropped. A maintainer can
  omit or withhold listings, as with search, but can't alter or invent
  them.
- **Bounds.** The client stops at 50 000 listings or 64 MiB, and after at
  most `50 000 / 10 + 10` pages, and marks such a copy partial in the UI.
  The copy lives in memory only and is refreshed when an hour old, or on
  "Refresh copy".
- **Search** matches every query word as a prefix of a word in the name,
  description or tags (name matches rank first), like the node's own
  search.

### 7.3 Relay URLs

A relay URL is compared in one canonical form wherever it is accepted,
stored or compared (heartbeats, the registry, admin removals and
`delisted_relays`, nameplates, a relay's own `public_url`, a community server's `relays` and pinned relays;
`internal/relayurl`):

- scheme `ws` or `wss`, in lower case;
- the host in lower case without a trailing dot; an IP address in its
  standard text form (IPv6 in brackets, IPv4-mapped addresses as IPv4);
  host names in ASCII (IDNs in their `xn--` form);
- the port only when it isn't the scheme's default (80 for `ws`, 443 for
  `wss`);
- the path cleaned: no empty, `.` or `..` segments, no trailing slash; the
  root is the empty path. Path letters keep their case.

A URL with user information, a query, a fragment, percent-encoding or
characters outside letters, digits and `-._~/!$&'()*+,;=:@` in its path
is not a relay URL and is refused. So `WSS://Relay.Example:443/ws/` and
`wss://relay.example/ws` are the same relay. A maintainer's removal of a
relay covers every URL on its host (any port, any path).

**Public addresses.** The registry refuses relay URLs whose host is a
loopback, private, link-local, CGNAT, multicast, NAT64/6to4/Teredo or
other non-public address, or a name that can only be local (`localhost`
and subdomains, `.local`, `.internal`, `.lan`, `.localdomain`,
`.home.arpa`, single-label names, a numeric last label): community servers
dial registry relays, so such a URL would reach into their networks.
Development nodes accept them with `allow_private_relays`
(`relayurl.CheckPublic`; `relayurl.Dialer` checks the address actually
dialled after DNS resolution).

**Proof of control.** A heartbeat names a URL and is signed by a relay
key, but anyone can sign a heartbeat naming any URL. A maintainer lists a
URL for a key only after a plain `GET` of the URL's http(s) form (`ws://`
→ `http://`, `wss://` → `https://`, same host, port and path), without a
WebSocket upgrade, returned that key:

    GET https://relay.example/ws          (no Upgrade header)
    200 text/plain
    <relay registry key, base64url>\n

A relay that registers (`registry_urls` set) answers so on its `/ws`
path; requests with an upgrade, other methods, and relays that don't
register get the WebSocket endpoint as before. The maintainer's fetch
follows no redirects and uses no proxy, connects to public addresses only
(unless `allow_private_relays`), and reads at most 256 bytes within 5 s;
at most 16 run at once, one per URL at a time (heartbeats for a URL being
checked wait for that check), one per URL per 30 s, and 10 per minute per
client network (IPv4 address or IPv6 /64) for URLs that network hasn't proven;
a result is kept for 10 minutes. While every slot is busy, a heartbeat for a
URL whose last proof matched its key is accepted on that proof, so URLs whose
hosts stall can't push proven relays out of the registry. Heartbeats arriving
by replication never wait for a fetch:
one for a URL not yet proven here is refused routinely and checked in the
background, so the relay's next heartbeat is accepted. The first proven
key keeps the URL while its heartbeats are fresh (`relay_ttl`).
`relay_proof: false` turns the check off, and only together with
`allow_private_relays` (for a local kind cluster, where the node can't
reach the relay's 127.0.0.1 URL).

### 7.4 Community status, reaping and relay failover

**Heartbeats.** A community server sends every maintainer node it
publishes to a `SignedCommunityHeartbeat` every `heartbeat_interval` (1 min
by default): `CommunityHeartbeat{route_id, server_key, at, stopping,
relays_attached, relays_wanted, pinned}`, signed as
`Sign(server_key, "openarcanum/community-heartbeat/v1" || heartbeat)` (the same
Ed25519 signature with a context string as every other signed object, §3).
`relays_wanted` is its pinned list's length or its `relay_count`; `pinned`
says it never moves to other relays. On a clean shutdown it sends one more
with `stopping` set. The node accepts a heartbeat only for a route it holds
whose `server_key` matches (so heartbeats can't be forged for someone else's
community and need no cert of their own); on 404 or 409 the server publishes
its route again. Heartbeats are stored per route id (the newest wins) and
replicated after routes.

**Status.** `GET /v1/communities/{route_id}/status` and search results carry
`CommunityStatus`:

| `state` | When |
|---|---|
| `ONLINE` | the newest heartbeat is within the node's `community_offline_after` (3 min by default), not stopping, with every relay it wants attached |
| `DEGRADED` | as online, but attached to fewer relays than it wants |
| `OFFLINE` | no heartbeat within `community_offline_after`, a stopping heartbeat, or none of its relays attached |
| `TERMINATED` | reaped (below); only the tombstone is left |
| `UNSPECIFIED` | a route but no heartbeat yet |

`last_seen` is the newest time the server signed (heartbeat `at` or route
`published_at`); `heartbeat` is the newest signed heartbeat, which clients
verify against the server key they know before trusting `last_seen` and the
relay counts (a maintainer can withhold reports, never invent them).
`reap_after_seconds` is the node's `community_reap_after`, for the
"shut down after N days" wording.

**Reaping.** A community whose server signed nothing for longer than
`community_reap_after` (7 days by default) is terminated by each node's
expiry pass: its route, listing and status are deleted and a tombstone
`{route_id, community, last_seen, reaped_at}` is kept for
`community_tombstone_ttl` (30 days). Each node decides from the signed
times it holds (replicated heartbeats keep them the same), so tombstones are
not part of the public replication. Members of one maintainer cluster also
pass their tombstones to each other (§7.5): a member that was down while
the others reaped a community drops it too, unless it saw the server sign
something after the tombstone's `last_seen` (or the route re-registered
it). Members of a cluster should use the same `community_reap_after`. Every node refuses (410) a route, listing or heartbeat
signed longer than `community_reap_after` ago, so a peer still holding an
old copy can't bring it back by replication.

**Coming back.** While the tombstone lasts, the node answers 410 to the
community's listing and heartbeats, and to its routes unless
`Route.reregistered_at` is later than the tombstone's `last_seen`. The
server records the 410 (the operator sees it in the log) and keeps serving
members that can still reach it. The operator re-registers explicitly
(`community reregister`; the desktop app's "Bring it back online"), which
stamps `reregistered_at` = now on every route from then on; the first such
route deletes the tombstone, and the listing and heartbeats are accepted
again. The tombstone keeps the community the route id belonged to, and
only a route certified for that community (through its owner chain) can
re-register it; another community's route for the id is refused (403),
however fresh its `reregistered_at`. Clients learn it from the status, which turns from `TERMINATED` back
to `ONLINE`, and reconnect through the route. After the tombstone expires
the node no longer remembers the community, and its next route registers it
like a new one.

**Relay failover.** In automatic mode a server replaces a relay that
refuses or drops its connection with another healthy relay from the
registry at once (one that just failed is used only when nothing else is
left) and publishes its route with the new relays. A client whose relays
fail looks the route up again after every failed round and reconnects
through the relays it names; while it can't connect it asks for the status
and shows "Offline" (with when the server was last seen), "Degraded", or
"Shut down". A community on pinned or dedicated relays never moves: its
heartbeats report `pinned` with the attached count, and clients say its own
relays are unreachable instead.

### 7.5 Maintainer clusters and replication

A **maintainer cluster** is a small set of nodes (at most 8) run by parties
who trust each other, so any member can stand in for any other: when the
root node is down or unreachable, clients use the others with no action,
and nothing is lost when a sender and a recipient reach different members.

**Member list.** `SignedMaintainerSet{set, signature}`, where `set` is a
serialized `MaintainerSet{operator_key, seq, issued_at, expires_at,
nodes[]}` and each `MaintainerNode{url, node_key, role, region,
no_directory}`, signed `Sign(operator_key, "openarcanum/maintainer-set/v1" ||
set)`. The operator key is the cluster's identity and can stay offline
(`meta operator keygen`, `sign-set`). Verifiers pin it and keep the valid
set with the highest `seq`; a set is refused when `expires_at` has passed
(180 days by default, 2 years at most), when it is issued more than 24 h
ahead of the verifier's clock, or when its 1 to 8 members don't have
distinct http(s) URLs and node keys. `role` (`root` or `member`) only
orders the nodes. Served at `GET /v1/maintainers[?operator=KEY]` and
replicated publicly (§7 "Replication", `ReplicationBatch.maintainer_sets`;
a node keeps only its own `cluster_operator_key`'s sets). Removing a
member is a new set without it (`seq+1`); every node and client drops it
at its next refresh. Rotating the operator key means pinning the new key
everywhere (no rotation chain yet).

**Node identity and trust.** Each node has an Ed25519 node key
(`DATA/node.key`) and an X25519 Noise static key (`DATA/node.noise`),
never in backups (`GET /v1/node` → `NodeInfo{node_key, noise_key}`). A
node trusts exactly the node keys in the newest valid set for its
`cluster_operator_key`, plus its config's `trusted_peers` and `meta trust
add` (`URL#NODEKEY`; their URLs take precedence over the set's). It
exchanges private data only with keys it trusts, in both directions.

**Peer sessions.** `GET /v1/peer` upgrades to a WebSocket carrying the
Noise XX session of §3 (`internal/session`) with the prologue
`openarcanum/meta-peer/v1`; each side signs its static key with its node key
(`openarcanum/noise-static/v1`). The responder refuses the session (close
reason "this node doesn't trust your node key") unless the initiator's
node key is trusted, and re-checks trust on every request; the initiator
checks that the responder has the node key it expects. Messages are
`PeerMessage{id, pull | batch | poke | error | blob_chunk_request |
blob_chunk}`; a request's reply carries its id, and id 0 is a one-way
notice (`PeerPoke`: "my log grew, pull now").

**The peer log.** Every node keeps a private log of changes, separate from
the public revisions: `PeerPull{cursor, epoch}` → `PeerBatch{epoch,
cursor, more, head, entries[]}`, at most 1000 entries or 8 MiB; while
`more` is set the puller pulls again at once, otherwise every
`trusted_interval` (5 s) or when poked. Each entry is what the log row
names as it is now (gone ones are skipped): `PeerMailboxItem{inbox, blob,
received_at, expires_at}`, `PeerMailboxTombstone{gid, inbox, expires_at}`,
`PeerBlobMeta{upload_id, hash, size, token_hashes, expires_at}`,
`PeerBlobRelease{upload_id, token_hash, expires_at}`,
`PeerDecision{origin, kind, key, action, at, signature}` and
`PeerCommunityTombstone{route_id, community, last_seen, reaped_at}`. The
log includes what a node received from its peers, so a cluster that isn't
fully connected (A to B to C) converges. Cursors are kept per peer key; the log
has an epoch that a restore from backup changes, and a pull from another
epoch starts at 0 (what arrives again is recognised and skipped). Entries
are pruned when what they name has expired everywhere.

**Mailboxes: both directions.** An item's global id is `gid =
SHA-256("openarcanum/mailbox-item/v1" || 0x00 || inbox || blob)`, computed by
every node (and client) itself, never taken from a peer; the same bytes in
the same inbox are the same message (Olm and Megolm ciphertexts are unique
per message), so a message dropped on two members is one item. What a
member holds converges to the union of every member's items minus every
member's deletes:

- An item whose gid is held, or tombstoned by its own inbox, is skipped
  (also a client's drop: a late copy of a deleted message doesn't come
  back). A tombstone made with another inbox's token binds nothing for
  this inbox, so someone who learned a gid can't delete the item in
  advance; it gives way to the item's own inbox's delete.
- A replicated item keeps its first expiry, never later than the
  receiver's own `mailbox_ttl` from now; one already expired is refused.
  Its local id is the receiver's next one, so a reader's `after_id` never
  skips it.
- A delete (`MailboxDelete.ids` on that node's ids, or `.gids` for items
  read on another member) leaves a tombstone until the item's expiry plus
  24 h (as long as any copy can live, for a gid the node never held), which
  deletes the item wherever it arrives. Tombstones for gids the node
  doesn't hold are limited to 500 per client network per hour (the rest of
  such a request is skipped). A tombstone only deletes items of
  the inbox it names (the token holder's), and it can't be proven: see the
  threat model for what a compromised member can do. The longest partition
  that heals cleanly is `mailbox_ttl` (14 days): after that every copy has
  expired anyway.
- An item that doesn't fit the receiver's quotas is refused there only
  (`openarcanum_meta_peer_refused_total{kind}`) and the cursor moves on;
  clients read the other members. Per-network budgets apply to client
  drops only.
- Per-peer limits bound what one member can push: `peer_items_per_minute`,
  `peer_tombstones_per_minute` (5000 each) and `peer_bytes_per_day` (2
  GiB). Above one, the node stops pulling from that peer until the window
  ends (`openarcanum_meta_peer_throttled`); nothing is skipped.

**Blobs.** A completed upload's record and every released token go into the
peer log, so any member serves a blob and an ack on any member ends it
everywhere (a remembered release can't be revived by a late record). A
member with the record but not the content fetches the chunks a client asks
for from a member that has them (`PeerBlobChunkRequest`, 10 s each), keeps
them, and copies the rest in the background within
`peer_blob_bytes_per_hour`, so the blob outlives the node it was uploaded to.
A member whose `blob_store_max` is reached keeps the record only and serves
through. A member whose collected chunks don't hash to the blob id discards
them and keeps the record, so the content is fetched again from the members.
Senders list the rest of the cluster as fetch nodes (§11).

**Moderation decisions.** Delistings and relay removals made with `meta
directory`/`meta relays` carry the deciding node's key as origin and pass
through every member that trusts the origin (its maintainer set,
`trusted_peers` or `meta trust`); decisions of other keys are dropped, a
decision stays in the peer log for 90 days (the decision itself is kept), and
the decisions of an origin that loses its trust are dropped at the next
expiry; a member applies those of the origins in its
`follow_decisions` (a replica set up with `meta init --replica-of` follows
the root), newest per origin and key first, never over its own decisions,
and a followed relist lifts only that origin's delisting. Reasons don't
leave the node. Each decision is signed by its origin's node key:
`signature = Sign(origin, "openarcanum/peer-decision/v1" || 0x00 || origin ||
kind || 0x00 || key || 0x00 || action || 0x00 || at)` (`at` as 8 bytes
big-endian), so a member can't forge another member's decisions; members
refuse (and don't pass on) decisions that don't verify or are dated more
than 10 minutes past their clock. Unsigned decisions aren't accepted
from other members.

**Community tombstones.** A member that reaps a community passes its
tombstone on; a member that was down during the reaping window drops the
community too unless it saw its server sign something later (§7.4). A
member checks a tombstone against its own rule: its `last_seen` must be
older than the receiver's `community_reap_after` (a tombstone dated now
drops nothing), and its `community` the owner of the route the receiver
holds for the id. For a route id the receiver doesn't hold, the tombstone
is kept without a community, so it can't block any community's listing or
re-registration there.

**Nameplates** aren't replicated (a code must be claimed once, on one
node): in a cluster each member hands out only random numbers whose last
digit is its position in the set, and a new device asks that member first.

**Clients.** A maintainer entry `URL#OPERATORKEY` pins a cluster: the client
fetches the set from any member, keeps it only if it verifies against that
key with a `seq` at least the cached one, caches it (usable locked and
offline), refreshes it every 6 h and soon after a node failed, and uses
every member ("Use this network's other nodes", on by default). Adding a
plain URL that serves a set offers its cluster, showing the operator key's
fingerprint for the user to confirm before it is pinned. Each node's health
(consecutive failures, backoff from 30 s to 10 min with jitter) orders
requests: healthy nodes first, nodes backing off only as a last resort.
Community servers and relays expand `URL#OPERATORKEY` entries (or
`maintainer_operator_key`) the same way and publish to every member.

**Failure cases.** Root down: clients back off from it and use the others;
when it returns the members pull from each other and converge, and clients
skip what they handled by gid. Root gone for good: sign a set without it,
naming another member `root`. Sender reaches only the root, recipient only
m2: root→m2 replication delivers within seconds; with that link cut too,
the item arrives when either path heals. Sender reaches only m2, recipient
reads the root: the recipient's minute-by-minute read of every member gets
it from m2. Partition with writes and deletes on both sides: union minus
tombstones, each item handled once per device. A restored member: new
epochs, its peers pull it from 0, and it gets back from them what it lost.

## 8. Moving a community

1. `community restore -data NEW -backup FILE -storage-key PATH` decrypts the
   backup (encrypted with the storage key), generates a new server key and
   prints a move code. Until adopted, its cert doesn't cover its key, so
   clients won't accept it and it publishes no route.
2. Any owner pastes the move code; their client signs `ServerCert(seq + 1)`
   for the new key with their own owner key, attaches the owner chain it
   knows, and sends `AdoptServer{secret, cert}` to it. The restored server
   accepts the cert if it verifies and its chain extends the backup's (so an
   owner added after the backup was taken can adopt it).
3. The new server publishes its route under the same route id. Clients
   whose relays fail look up the route, accept the new key because a key in
   the community's owner set certified it, and reconnect.

Server-key rotation on the same hardware uses the same cert check via
`PrepareKeyRotation` / `CommitKeyRotation` (any owner); the new cert is pushed
to members in `SyncState.cert`. `ServerBackup` (an on-demand backup,
`AdminResult.backup_path`) is owners only and at most one per 10 minutes
(`RATE_LIMITED`), since every backup prunes the oldest beyond the server's
`backup_keep`.

## 9. Direct messages and group DMs (`dm.proto`)

End-to-end encrypted with **vodozemac** (Olm and Megolm, the Matrix
double-ratchet implementations). Nothing here is a new construction: the
usage follows the Matrix end-to-end encryption design. Servers, relays and
maintainers only carry `SignedContactCard`, `OneTimeKey` and `Envelope`
bytes. A 1:1 DM is a group with two members (`GroupState.direct`); there is
one code path. Code: `clients/desktop/src/openarcanum/dm.py`, `olm.py`, `groups.py`,
`dmfiles.py`, `filecrypt.py`.

### 9.1 Identities, devices and device lists

- A **DM identity** is an Ed25519 key, one per community by default (so a
  person's DM identities in two communities can't be linked). Its seed lives
  in the vault with the other identities (Lock clears it) and travels to
  linked devices.
- Each device has its own **vodozemac Account per DM identity** (labelled
  `dm:<identity>`), i.e. an Olm curve25519 identity key and an ed25519 signing
  key. Accounts are never shared between identities or copied between
  devices. Olm/Megolm state lives in `data/olm.bin`, sealed with a vault key;
  every ratchet step is saved before its output is sent.
- `DmDeviceList{identity, seq, issued_at, devices}` is signed by the identity
  (`openarcanum/dm-device-list/v2`). Each `DmDevice` carries the device's own
  signature over `identity || curve25519` (`openarcanum/dm-device/v2`), so a list
  can't claim a device that didn't consent. `seq` only increases: clients pin
  the highest seq per identity and ignore lower ones (rollback). At most 20
  devices.
- When two of the user's devices change a list concurrently (same seq), the
  next device to notice merges both and signs `seq + 1`. Removed devices are
  remembered so a merge never re-adds them.

**Device list changes.** Every linked device holds the identity key, a
removed one too, so the identity's signature alone can't decide the list:
otherwise a removed device could sign `seq + 1` re-adding itself, with its
old keys or fresh ones, and peers and the user's own devices would take it.
So:

- `DmDeviceList.removed` lists the `device_id`s removed from the identity,
  for good: a list never drops one, and never lists a device whose
  `device_id` is in it. Removal (§10.4) records the device in every
  identity's list, listed there or not.
- After the first list a client holds for an identity (trust on first use,
  as for the identity itself), it takes a new list only if a device of the
  list it holds, one the new list doesn't remove, approved it with its
  Account key (`SignedDmDeviceList.approver`, `approval`): either over the
  whole list (`openarcanum/dm-device-list-approval/v1` || 0x00 || identity ||
  SHA-256(list)), or, for a device adding itself (`added` = its device_id,
    and every other `DmDevice` entry byte-identical to the held list's, `removed`
  unchanged), an **add certificate** a listed device issued
  for that device_id (`openarcanum/dm-device-add/v1` || 0x00 || identity ||
  device_id). Certificates are issued by the existing device when linking
  (it chooses the new device's id, §10.1) and by the device that creates an
  identity, for the user's other devices; a removed device's certificates
  are deleted and its id is in `removed`, so it can't use one.
- Which list wins: an approved list with a higher `seq`, or an approved list
  that removes a device the held list still lists (whatever `seq` the held
  list claims), so a removed device's list taken before the removal arrived
  doesn't outlive it. Unapproved changes are refused (and logged, without
  keys).
- A removed device holds none of the remaining devices' Account keys and
  isn't in the list any more, so it can't approve a change that re-adds it
  or adds a fresh device of its own; residual risks in the threat model.

### 9.2 Contact cards and one-time keys

- A `ContactCard{identity, member, name, devices, fallback_keys, inboxes,
  issued_at}` is signed by the identity (`openarcanum/dm-card/v2`) and published
  per community (off by default; the community can turn cards off for
  everyone, enforced by the server). `member` binds the card to the
  publisher's membership, so it can't be replayed under someone else's.
- `fallback_keys` holds one signed vodozemac fallback key per device, replaced
  every 7 days (the previous key still starts sessions until the next
  replacement, so a card up to a week old works); a client keeps a peer's card
  only if its `issued_at` is not below the card it holds (a server can't
  revert the fallback key or inbox with a stale card), while device lists
  follow their own `seq` rule;
  `inboxes` one first-contact mailbox address per device. Each device shares
  its fallback key and inbox with the user's other devices (§10.2), so any of
  them can publish the full card.
- **One-time keys** (`OneTimeKey`, signed by the device: `openarcanum/dm-otk/v2`)
  are uploaded per device (`AddKeyPackages` with `KeyPackage{device,
  package}`); the server hands out one per device per `GetContactCard` and
  deletes it (`ContactCardResult.key_packages`; `left` reports counts, and the
  card's owner asking about itself only sees counts). A member is handed
  packages by at most 3 fetches of one card and 30 fetches in all per hour;
  beyond that the card comes with counts only, as when none are left, so
  nobody can drain another member's one-time keys. A device tops up below
  20. Republishing a card with `devices` drops the packages of devices no
  longer listed. Limits: 64 KiB card, 2 KiB per package, 100 packages per
  device, 20 devices.
- A sender starts an Olm session with a claimed one-time key, or with the
  card's fallback key when none is left (as in Matrix).
- **Key-change warning:** a client pins the DM identity it first saw for each
  `(community, member)`. A different identity later marks the contact
  "key changed" and clears its verification until the user accepts the new key.
- **Safety number:** 60 digits = for i in 0..11, `int(h[3i:3i+3]) mod 100000`
  as 5 digits, with `h = SHA-512("openarcanum/safety-number/v1" || 0x00 || lo ||
  hi)` over the two identity keys in sorted order. It is a display encoding of
  public keys for people to compare, not a secret. Verification is recorded
  per identity and synced between the user's devices.

### 9.3 Transport

- **Envelope** (after the magic `FDM2`): either `OlmEnvelope{sender_curve25519,
  recipient_curve25519, type, ciphertext}` (type 0 = pre-key, 1 = normal), or
  `MegolmEnvelope{sender_curve25519, session_id, ciphertext}`. At most 1 MiB.
- **Olm payloads** (`OlmPayload`) carry the sender and recipient identity and
  ed25519 keys inside the ciphertext (Matrix's `m.olm.v1` binding), so a message
  can't be presented as from or for another device, plus the sender's current
  card (so a device that joined later can verify the sender's device first;
  kept only for contacts, the user's own identities and people in a group with
  the user). Bodies: `room_key`, `invite`, `card`, `sync` (§10), `state_update`.
- **Olm replay:** a pre-key message for a session the device already has is
  decrypted only by that session (a replay fails there); one for a session it
  had before (session ids are remembered, 256 per peer) is refused. So a
  replayed message can't create sessions, push out the real one, or re-run its
  payload. Normal-message replays fail in the ratchet. Peers with Olm state are
  bounded (5,000 per account, least recently used dropped).
- **Megolm events** (`DmEvent{id, at, group_id, sender, sender_device, body}`)
  are checked against the session they decrypted with: the session's sender
  device must be in the sender's current device list, `group_id` must match
  the session's group, and the same message index carrying a different event
  is refused (replay). Duplicates (the same event live and from a mailbox) are
  dropped by id.
- **Delivery:** every event is published on the group's relay topic
  (`GroupState.relay`, `topic`) and dropped into each recipient device's
  mailbox inbox for that group (announced with a `DmInboxes` event; until then
  its first-contact inbox from its card). Typing indicators are live only.
  Olm messages go to mailboxes (or, between one user's own devices, live on the
  device group's topic, §10.2).
- **Ordering:** an event that arrives before its room key waits until the key
  arrives; a room key for a group that isn't known yet (the invitation
  travels separately) waits until the invitation arrives, and its sender's
  device is checked then. Both buffers are bounded (16 MiB together, 10
  minutes).
- **Inboxes:** a `DmInboxes` event is accepted only for the device that sent
  it, so nobody can redirect another device's mailbox.

### 9.4 Groups

`GroupState{group_id, seq, prev, owner, members, invited, name, icon,
members_can_add, allow_attachments, direct, relay, topic, at}`, signed by its
signer (`SignedGroupState`, `openarcanum/dm-group-state/v2`). States form a chain:
`seq` = previous + 1 and `prev` = SHA-256 of the previous `SignedGroupState`.
**Every member's client** checks every change (`groups.py`):

- The first state has seq 1, the owner as its only member, and is signed by
  the owner.
- At most **10 people** (members + invited). No duplicates; the owner is a
  member. Names ≤ 100 characters. The relay is a `ws://`/`wss://` URL; the
  icon is an inline JPEG of at most 48 KiB made on the owner's device.
- **The owner** may rename, set the icon, toggle `members_can_add` and
  `allow_attachments`, invite, remove anyone, and transfer ownership. Nobody
  becomes a member except by accepting. The owner can't leave while others
  remain (transfer first); in a direct conversation either person can leave.
- **A member** may leave (only their own removal), or, with
  `members_can_add`, invite (only appending to `invited`).
- **An invitee** may accept (move themselves to `members` with `joined_seq` =
  the new seq) or decline (remove themselves from `invited`), nothing else.
- For everyone but the owner, every other member and invitee entry must stay
  byte-for-byte the same (names, communities, member keys, join points).
- **Direct** conversations: exactly two people, no name, icon or
  `members_can_add`; `direct` never changes.
- **Concurrent changes and forks:** of every valid chain a client knows,
  it keeps the one with the most **owner-signed states** (a state signed by
  the owner of the state it extends: the group's owner epochs); between
  chains with as many, the one whose first differing state is better (the
  owner's change, else the lower hash). Only the owner can add an
  owner-signed state, so no fork a non-owner makes can replace a chain with
  an owner-signed state after the fork point: a removed member can't
  re-sign an earlier change of their own (say, their acceptance, ground to a
  lower hash) to roll back the owner's removal. A competing chain that wins
  replaces the client's states from the fork point and drops everything it
  built on the loser (up to 64 positions back); losing states are kept
  aside (up to 64, persisted with the group) so a branch that arrives in
  pieces, in any order, is judged the same way, and clients that have seen
  the same states hold the same chain. A
  client whose acceptance lost signs it again on the winning state. A
  member who left drops the group at once, so a leaving that lost can't be
  signed again by them: the owner's client, seeing it lose, removes that
  member on top of the winning state. States
  that arrive before their parent are kept with the group (persisted) until
  it arrives.
- Changes reach members as a Megolm `state` event carrying the up to 16
  states before it (`state_tail`, so a member who missed one catches up), and
  invitees and removed people (who don't get group events) as an Olm
  `state_update`.

**Inviting** sends each invitee device an Olm `GroupInvite{chain, cards}`: the
whole state chain (proving who invited them) and the cards of everyone in the
group. **DM policy** (decided on the invitee's devices): a direct conversation
from a contact (or, under "members of shared communities", from someone whose
card is published in a community both share) opens immediately; from anyone
else it's a request the person accepts or declines, and it shows what was
sent. Group invitations always need accepting; from someone the policy doesn't
allow they are dropped. Blocked people's invitations and events are dropped.
Until the person accepts (a request or an invitation), their devices contact
nothing the conversation names: not its relay (no subscription, at start-up,
after linking or when another device syncs the conversation; nothing is
published there either) and not the nodes its attachments are stored on (no
fetch, and saving such a file asks them to accept first). What a request
shows arrives through their own inbox on their own maintainer nodes, and
replies (a decline) go out that way. Accepting, on any of their devices,
subscribes to the relay and fetches the attachments that wait.

### 9.5 Megolm sessions and rotation

Each sender device has one outbound Megolm session per group and shares its
key (`RoomKey{group_id, session_id, session_key}`) over Olm with every device
of every member (and, in a direct conversation, of the invited person), plus
the sender's own other devices. Recipients accept a room key only from someone
who is a member or invited. A room key is kept for the device that shared it
(session id and sender device): a member who was given another sender's key
and shares it on first gets a copy of their own, which never displaces the
real sender's, since an event opens only with the copy of the device it comes
from and names its sending device inside the encryption (up to 16 copies per
session id). A sender starts a **new session**:

- when the set of recipient devices changes: a member joins, leaves or is
  removed, or any member's device list changes (a device is linked or removed);
- after **100 messages**;
- after **7 days**.

(The same limits Matrix clients use.) A device that joins later only receives
the current session key at its current index, so **nobody can read events from
before they joined**; history moves between a user's own devices only through
the link-time copy (§10.3).

### 9.6 Events

`text{text ≤ 4000, reply_to, files ≤ 10, mentions}`, `edit` and `delete` (the
sender's own messages only, checked by every client), `reaction{emoji ≤ 32,
remove}`, `pin{unpin}`, `typing` (live only, shown for 6 s), `state`,
`inboxes`, `card`, `sync` (device group only), `stored`, `received` (§9.7).
Unread counts and read positions are per user and travel only between the
user's own devices (`dm_read`, §10.2). They use each device's receive time:
an event's own `at` comes from the sender's clock, is shown as sent time, and
is never taken as more than 5 minutes in the future.

### 9.7 Attachments

- Up to **10 files per message**, **25 MB each**. The group's
  `allow_attachments` is enforced by the sender and by every recipient
  (files in an event are dropped when it's off).
- Each file is encrypted on the device with a fresh 16-byte key using the
  **age v1 payload** format (STREAM: ChaCha20-Poly1305, 64 KiB chunks, payload
  key = HKDF-SHA256(file key, salt = 16-byte nonce, "payload")), checked
  against the C2SP CCTV age vectors. The ciphertext's SHA-256 is its address.
  `DmFileRef{hash, key, name, mime, size, ciphertext_size, width, height,
  thumbnail, live_chunks}` travels inside the Megolm `text` event; the
  thumbnail (JPEG, ≤ 320 px, ≤ 48 KiB) is made on the sender's device, and
  `live_chunks` lists the SHA-256 of each 512 KiB live chunk.
- **Live first:** the sender publishes the ciphertext in 512 KiB
  `DmFileChunk{hash, index, total, data}` frames (magic `FDF1`) on the group
  topic. Online devices assemble chunks for files they were told about,
  taking a chunk only if its index and total fit the file's size and its
  hash is the one `live_chunks` lists (any member can publish on the topic:
  a forged chunk is ignored and the real one still taken; a file without
  `live_chunks` isn't assembled live), check the whole hash, keep the
  ciphertext in their cache and send a live `received{hashes}` event.
- **Store for the rest:** after 6 s the sender uploads the ciphertext once to
  maintainer blob stores (§11), registering one random fetch token per device
  that didn't confirm, and sends `stored{hash, nodes, tokens, expires_at}`.
  If the sender's app closes before that, its next start stores the file for
  every recipient device (the pending upload is kept in the sender's vault).
  Recipients accept `stored` only from the member who sent the file, and only
  with node URLs they'd use themselves (`https://`, or `http://` for this
  computer); a download must be exactly the ciphertext size stated in the
  file reference.
- File names from others (DM and community attachments alike) are reduced to
  one safe path component before they are shown or saved (no separators,
  control, bidi or other format characters, or reserved characters; not
  hidden; no trailing dots or spaces; not a Windows device name, with any
  extension: `CON .txt`, `nul.tar.gz`; `names.safe_name`).
  If every device confirmed, nothing is stored. Devices fetch in the
  background, check the hash, and release their token; the blob is deleted
  when every token is released or after its lifetime (72 h). After that,
  devices that never fetched it show "expired".
- Tokens are visible to every member (they're in a group event): a member
  could release someone else's token early. That only deletes the blob early
  for that person (they see "expired"); it's in the threat model.

### 9.8 Statuses

A status is a `DmStatus{state, text, emoji, expires_at, fresh_until}`
(state: online, idle, do not disturb, offline; text ≤ 128 characters on one
line, no control or bidirectional formatting characters; emoji ≤ 32
characters; `expires_at` Unix ms or 0) sent as the body of a `DmEvent`
(`status`, field 24) in a **status audience's** Megolm session. Nothing here
is new: it is the group design of §9.5 with the audience as the group and no
group state. Code: `clients/desktop/src/openarcanum/statuses.py`.

**Audiences.** Each of the user's devices keeps one outbound Megolm session
per audience, keyed by a random 16-byte audience id (`DmEvent.group_id`,
`StatusKey.audience`), never the id of a DM group:

- **A community** (per community: off, the state, or the state and custom
  text; default the state). Each device of each member that can receive
  statuses there has a vodozemac Account of its own, labelled
  `status:<community>` (no keys shared with DM identities), and publishes a
  `SignedStatusDevice`: `StatusDevice{member, curve25519, ed25519,
  fallback_key, issued_at}` signed by the **member key**
  (`openarcanum/status-device/v1`), its fallback key signed by the device
  (`openarcanum/dm-otk/v2`). A server can't add or swap a member's status
  devices without their member key. Keys and statuses go through the
  community server (§4.11).
- **Contacts** (off, the state, or the state and custom text; all contacts
  or chosen ones; default the state and text, all). One audience per DM
  identity of the user, made of the contacts that know that identity (a
  contact's community picks it), so identities stay unlinkable. Keys go over
  the DM machinery (§9.3) as an Olm `status_key`; each status is dropped into
  each recipient device's first-contact inbox. Contacts have no presence:
  `fresh_until` (75 minutes) says when the status goes stale, and it is
  posted again every 30 minutes while the app runs; quitting posts
  `offline`, and so does Invisible (indistinguishable).

**Keys.** `StatusKey{audience, session_id, session_key}` goes over Olm
(`OlmPayload.status_key`, field 16) to each recipient device, with the usual
binding of sender and recipient identity and ed25519 keys inside the
payload (community: the member keys and the status accounts' keys; contacts:
the DM identities). `session_key` is the session **exported at the index of
its newest message** (`InboundGroupSession.export_at`, as Matrix forwards
keys): a device that joins an audience reads the current status and nothing
before, with no new post. Community key drops are encrypted in one batch
(one save of the Olm state before anything is sent) and sent 500 a request.
A recipient takes a community key only from a device in the sender's
member-signed entry (checked before decrypting); a contact's only from
someone it deals with (a contact or someone in a conversation with it, not
blocked), from a device in their signed list. Statuses whose key was refused
are dropped, not held.

**Rotation.** A new session when a device that had the key is no longer in
the audience (someone left or was removed from the community, removed from
contacts or un-chosen, un-shared, or a device removed), after 1000 messages
and after 7 days. In a community the rotation happens when the user next
posts (nothing new is readable before then), so a member leaving costs each
sharer one key message per member device, once, at their next status. A
contact taken off the audience is first sent `cleared` (`STATUS_STATE_CLEARED`)
on the old session, then everyone else gets the new one. Un-sharing a
community deletes the blob on the server (`SetStatus{blob: ""}`) and forgets
the session.

**Checks on receipt.** The Megolm sender key must be one of the member's (or
contact's) devices; the event's sender, audience and device must match the
session; `at` no more than 5 minutes ahead, and not older than the status
already shown (a replayed old status is ignored); text and emoji within the
limits. A status in a DM group's session is ignored, and so is any other
event in a status session.

**Costs.** Posting a status: one Megolm message per audience (one blob on
each shared community server; one mailbox drop per contact device). Key
delivery: O(recipient devices) only when a session starts (rotation) and
one message per device that joins; idle and other changes reuse the
session.

**Idle and Do not disturb** are local: a device shows idle after
`status_idle_minutes` without input (default 10) and back on input, and
tells the user's other devices (`status_idle`), any of which still in use
posts online again. Do not disturb suppresses desktop notifications
(badges still count). The status itself travels between the user's devices
as a `status` sync event (§10.2; the newest wins).

## 10. Devices

### 10.1 Linking

1. New device: `POST /v1/nameplates` with a registered relay; shows
   `<number>-<word>-<word>-<word>` (282-word list) and subscribes to the
   nameplate topic.
2. Existing device (unlocked): claims the nameplate (once), subscribes, and
   both run SPAKE2 (symmetric, `idSymmetric = "openarcanum/link/v1"`, password =
   the code) by exchanging `{"t":"pake"}` messages: the Magic Wormhole design.
3. Keys: `HKDF(K, "openarcanum/link/existing-to-new")` and `…/new-to-existing`
   (ChaCha20-Poly1305). The new device sends `hello` (its name) encrypted; if
   the existing device can't decrypt it the codes differ: it sends a plain
   `abort` and nothing else (the only plain abort either side honours, and
   only while the new device waits for `confirm`). It confirms with an
   encrypted `confirm`. Anything else on the topic that can't be the other
   device's is skipped: `abort` before the keys exist or not sealed under them
   afterwards (a cancel after the keys exist is `{"t":"abort","box":…}`), PAKE
   messages not shaped like one (the first well-formed one is the only one
   used), `confirm` or `done` that don't open.
4. After the user approves (choosing communities, whether to copy DM history
   and cached community messages), `transfer` carries identities (community,
   DM and sync identity seeds), community records (routes, pinned keys, no
   device keys), settings, the device table, and the DM state (identities'
   device lists, contacts, group chains, peers' cards), encrypted and in 4 MiB
   chunks. **No Olm or Megolm state is copied.**
5. The new device sets its own passphrase, makes a device key per community
   (signed by that community's identity key), creates its own vodozemac
   accounts, adds itself to every identity's device list (§9.1, with the add
   certificates the existing device issued for the device_id it chose and
   sent in `transfer`), and replies
   `done` with its device entry and its device-group card. Every group with
   the user then rotates (§9.5), so the new device receives new events but
   nothing from before it was linked except what the history copy brings.

### 10.2 Device-group sync

A user's devices form a group like a DM group: its only member is the
profile's **sync identity** (an Ed25519 key that never leaves the user's
devices or appears on any card), and its devices are that identity's signed
device list. Sync events `{id, from, at, type, data}` (JSON, documented here)
are Megolm `sync` events in that group; the group rotates when a device is
linked or removed. One-device messages (the history copy) are Olm `sync`
payloads sent live on the group's relay topic, falling back to mailboxes.
Types: `community_joined` (record + identity keys), `community_left`,
`setting` (latest timestamp wins; device name and audio devices aren't
synced), `owner_key` (an owner key this member generated for a community,
§4.5), `device_linked`, `device_info`, `device_keys`, `device_renamed`,
`device_removed`, `dm_identity` (an identity's device list and every device's
fallback key and inbox), `dm_group` (a group's chain and status), `dm_forget`,
`dm_read`, `dm_blocked`, `dm_verified`, `contact_added`, `contact_removed`,
`history_request`, `history_chunk`, `status` (the user's status: state,
text, emoji, expiry; newest wins), `status_idle` (a device went idle, §9.8).

### 10.3 History copy

After linking, the new device asks the device it linked from for history
(DMs by default, cached community messages if chosen) with
`history_request{to, cursor, dms, communities}`; the answer is one
`history_chunk{to, cursor, items, next}` of items up to 48 KiB (or one larger
item, such as a message with thumbnails), over Olm. Items go in a fixed order
(DM groups by id, each by message time then id; then the chosen communities
by id, each by channel then message id), and `cursor`/`next` is the key of
the last item sent: `["dm", gid, at, id]` or `["c", cid, channel, id]`. The
answer holds the items strictly after that key, so the cursor never depends
on a list position: messages the existing device purges (retention), evicts
(cache limit) or re-sorts between chunks can't make the copy skip or repeat
anything, and the item the cursor names needn't still exist. A malformed
cursor restarts the copy from the beginning. Messages that reach the existing
device after the copy began aren't part of it: the new device is already a
recipient of everything new. The new device saves each
chunk (duplicates are ignored) and the new cursor in its vault before asking
for the next, and asks again after 30 s without an answer, so the copy runs
in the background, sends one chunk at a time, and resumes after a restart. `next = null` ends it. The existing device has to be
online for the copy to progress.

### 10.4 Removal

The removing device sends each community a `SignedDeviceRevocation` for the
removed device's key there, sends `device_removed` to the device group (the
removed device wipes its profile if it receives it), and drops the device
from every identity's device list (signing `seq + 1`, approved by the
removing device, with the device's id added to `removed` for good, §9.1).
Every DM group and the device group rotate, so the removed device
receives nothing new; every community refuses its keys and rotates the key
and topic of every channel the member can see (as when a member is
removed), so the removed device can't read new channel messages from the
relays; the member's other devices get the new keys in the next state. A
removed device
still holds the identity keys it had (all devices are equals,
docs/architecture.md §4); see the threat model.

## 11. DM blob store (`meta.proto`, maintainer nodes)

Files in DMs (up to 10 per message, 25 MB each, §9.7) are encrypted on
the client and the ciphertext is uploaded once to a blob store on maintainer nodes. The node is
keyless: it sees ciphertext, sizes, times and client IP addresses, never
file keys, names or recipients. Code: `internal/meta/blobs.go`; client:
`clients/desktop/src/openarcanum/meta.py` (`blob_upload`, `blob_download`, `blob_ack`).

**Ids.** A blob's id is `SHA-256(ciphertext)` (32 bytes; hex in the admin
CLI). The file key and the id travel inside the encrypted DM message.

**Upload ids.** The node stores each upload under its **upload id**,
`SHA-256("openarcanum/blob-upload/v1" || 0x00 || blob id || size (8 bytes,
big-endian) || the token hashes sorted bytewise)`, returned as
`BlobUploadStatus.upload` and required in every `BlobChunk`. Only the sender
knows every token hash, so only the sender can resume an upload or send it
chunks. Anyone else who names the same blob id (a recipient knows the id and
its own token) gets a separate upload of their own. So `create` and `chunk`
answer exactly the same whether or not a blob with that id is stored, or has
already been fetched by every device: they can't serve as a read receipt.
Several uploads of the same blob id can exist; `fetch` serves the finished
one that registered the presented token.

**Fetch tokens.** The sender makes one random 32-byte token per recipient
device (at most 200: 10 members × 20 devices) and registers only
`SHA-256("openarcanum/blob-token/v1" || 0x00 || token)` with the upload. Each
token travels to its device inside the encrypted message. The node can check
a token without storing it and can't tell which device, or whose, presented
it. A device downloads with its token and **releases** it with an explicit
ack once the file is decrypted and saved. A completed download doesn't count
as an ack, because the node can't tell a download that the device actually
kept from one that was cut off or failed to decrypt.

**Deletion.** A blob is deleted when every registered token has been released
or when `blob_ttl` (72 h) has passed since its upload started, whichever
comes first. From `created + blob_ttl` on (inclusive) it is never served, even
before the once-a-minute collector removes it. An unfinished upload is
discarded when it has received no new chunk (one the node didn't have yet)
for an hour, or 6 hours after it started, whichever comes first; resending
a chunk the node already has doesn't keep it alive.

**Requests.** All are `POST` with a protobuf body; errors are plain-text
bodies with the status shown. Every request counts against
`writes_per_minute` per client IP.

| Endpoint | Body → reply | Errors |
|---|---|---|
| `/v1/blobs/create` (≤ 64 KiB) | `BlobCreate{hash, size, token_hashes}` → `BlobUploadStatus{hash, size, chunk_size, missing, complete, expires_at, upload}` | 400 malformed (hash not 32 bytes, size 0, not 1 to 200 distinct 32-byte token hashes); 413 `size` over `blob_max_size`; 429 `blob_uploads_per_minute`, `blob_quota_per_ip`, `blob_uploads_per_ip` or `blob_tokens_per_ip` exceeded; 507 store full (`blob_store_max`) |
| `/v1/blobs/chunk` (≤ 1 MiB + 1 KiB) | `BlobChunk{hash, index, data, upload}` → `BlobUploadStatus` | 400 malformed (including no 32-byte `upload`), index past the end, or wrong length (every chunk is `chunk_size` = 1 MiB except the last); 400 when the last chunk lands and the data doesn't hash to `hash` (the upload is discarded); 404 unknown, expired, overdue or discarded upload, or an upload id that isn't for `hash` (the same answer for all) |
| `/v1/blobs/fetch` (≤ 1 KiB) | `BlobFetch{hash, token, index}` → `BlobChunkData{size, chunk_size, index, data, expires_at}` | 400 malformed or index past the end; 404 unknown blob, unregistered or released token, or expired (the same answer for all); 409 upload not finished |
| `/v1/blobs/ack` (≤ 1 KiB) | `BlobAck{hash, token}` → `Ok` | 400 malformed. Always `Ok` otherwise, whether or not the token was registered, so it can't probe for blobs |

**Uploading.** `create` is idempotent: repeating it with the same size and
token hashes (a resume after a dropped connection or a restart of either
side) returns the current status without charging quota again, and
`missing` lists the chunks still needed. Chunks may arrive in any order and
any number of times; a chunk the node already has, or one for a finished
blob, is ignored. The node writes each chunk in place and fsyncs it before
recording it, checks the SHA-256 when the last chunk arrives, then renames
the file into place and marks it complete. The blob can be fetched from
then on. Completion is idempotent: if the request carrying the last chunk
was cut off after the chunk was recorded (before or after the rename),
repeating `create` or resending any chunk finishes it. A client hanging up
doesn't abort a request halfway.

**Downloading.** Fetch chunk 0, then 1, … until `size` bytes have arrived;
check the SHA-256 against the id before decrypting.

**Quotas** (all maintainer settings, docs/operations.md):

- `blob_max_size` (26 MiB): 25 MiB of plaintext (the DM file limit) plus
  the chunked AEAD's overhead, with margin.
- `blob_quota_per_ip` (250 MiB per rolling 24 h) is the "per-sender" quota.
  There are no accounts and the sender is deliberately anonymous to the
  node, so the only sender attribute it has is the network address. The
  quota is charged when an upload is created (the declared size), so
  abandoned uploads count too. IPv6 addresses are grouped by /64, since one
  subscriber normally holds a whole /64; IPv4 addresses count singly (a
  carrier-grade NAT can make neighbours share a quota, which is why the
  default allows ten maximum-size files per day per address). Usage is kept
  in memory only, so client addresses never reach the disk; a node restart
  forgets it.
- `blob_uploads_per_minute` (30) per address (IPv6 /64): new uploads.
- `blob_uploads_per_ip` (500) and `blob_tokens_per_ip` (10 000) per address
  (IPv6 /64) per rolling 24 h: uploads and registered fetch tokens. Each is
  a database row, so these bound how much `meta.db` one network can grow
  with tiny uploads (1-byte blobs with 200 tokens each would otherwise be
  limited only by the per-minute rate). 10 000 tokens is 50 uploads to the
  largest group a day. Memory only, like the byte quota.
- `blob_store_max` (10 GiB): the sum of all stored and in-progress blob
  sizes. When full, new uploads get 507 and the client tries another node.

**Several maintainers.** Blobs aren't replicated between independent
maintainers (a 25 MiB file copied to every node would multiply storage for
no privacy gain). The sender uploads to two maintainers when it has two
(`BLOB_COPIES`), healthiest first, and recipients try the nodes in order
until one serves the blob; acks go to every node. Inside a maintainer
cluster the record and releases replicate at once and the content follows
on demand (§7.5): the sender's `DmFileStored.nodes` lists the nodes it
uploaded to and then the rest of their cluster (at most 8), and a recipient
also tries the other members of those nodes' clusters it knows. If a node dies, the copy on the other node
still serves; if a node loses its disk, its copies are gone and the other
copy remains. A node restarted after a crash reconciles its files with
`meta.db`: stray files are removed, records whose file is gone are dropped,
and an upload that was renamed into place but not yet recorded is
re-verified and finished.

## 12. Calls (`call.proto`, voice in `community.proto`)

Voice, video and screen share in voice channels. Media is WebRTC between
each participant's client and the call's relay; the relay's selective
forwarding unit (SFU, `internal/sfu`, Pion) forwards it to the other
participants without keys. Every encoded frame is end-to-end encrypted with
SFrame (RFC 9605) in the client before it leaves.

### 12.1 Keys and key ids

The community server makes a random 32-byte call key per *epoch*; the epoch
(and the key) change on every join and leave, and when the call moves to
another relay. Each participant has a *slot* (1..255, the lowest free one). A
sender's SFrame key id is `epoch << 8 | slot`, and RFC 9605 §4.4.2 derives the
key and salt from the call key **and the key id**, so every sender has its own
key and nonce space under one call key. Cipher suite: `AES_128_GCM_SHA256_128`
(4). Receivers keep the current and previous epoch; a sender switches to a new
key 250 ms after receiving it, so everyone has it first. A frame on a
participant's track must carry that participant's slot, or it is dropped (the
shared key alone wouldn't stop one participant sending under another's key
id). Video frames keep the VP8 payload header in the clear (10 bytes of a
keyframe, 3 otherwise), so the SFU can find keyframes. Every frame's SFrame
metadata (authenticated, not encrypted) is one byte naming its kind of media
(1 audio, 2 camera, 3 screen, 4 screen-audio: the relay's track ids) followed
by those clear bytes (none for audio), so a frame can't be moved to another
kind of the same sender's media. A sender's counter runs across all of its
kinds and never restarts for a key id within a call, however often its key is
set. Receivers keep a replay window per key id (8192 counters, a bitmap behind
the highest counter seen): a frame opens once, a frame further behind than the
window is dropped, and a counter is recorded only after its frame
authenticates. The window is freed with its key when the key's epoch is
dropped; a dropped key id is never set again.

**What stays in the clear.** The relay ends each participant's SRTP, so it
reads the RTP headers (which SRTP leaves in the clear on the network too)
and the sizes and timing of the SFrame ciphertexts. The media format is
chosen so these say as little as possible:

- RTP header extensions: only `urn:ietf:params:rtp-hdrext:sdes:mid`,
  `…:sdes:rtp-stream-id`, `…:sdes:repaired-rtp-stream-id` (bundling and
  simulcast) and `draft-holmer-rmcat-transport-wide-cc-extensions-01`
  (congestion control: a sequence number). **Never the RFC 6464 audio
  level** (`ssrc-audio-level`, nor `csrc-audio-level`), which the engine
  offers and which would put each packet's loudness and a voice-activity
  bit in the clear. Who is speaking is worked out by each receiving client
  from the decrypted audio.
- Voice and screen sound: Opus in 20 ms frames at a **constant bitrate
  without DTX** (`cbr=1;usedtx=0;maxaveragebitrate=32000`), so every packet
  of a sender is the same size whether it carries speech, music, noise or
  silence. A microphone that is muted, or whose voice activity or
  push-to-talk gate is closed, sends encoded silence: packets of the same
  size, as often (50 a second). SFrame adds a fixed overhead per frame (16
  bytes of tag and a header that grows by a byte as the frame counter
  passes 256 and 65536, and with the key id), so sizes stay constant after
  encryption.

The relay's SFU negotiates exactly this (`internal/sfu`: no audio level
registered, and its SDPs carry `cbr=1;usedtx=0`), and the client enforces it
on every description it sets, its own and the relay's (`call.js`
`hardenSdp`), so a relay can't ask for more. Video frame sizes still follow
what is shown (VP8 at a variable bitrate, per simulcast layer).

### 12.2 Joining

`VoiceJoin{channel_id}` (§4.3 permissions) returns a `VoiceSession`: the
call key and epoch, the participants, the relay (a ws URL whose relay
advertised `Hello.calls`), this participant's random 16-byte id and slot,
what they may do (`listen_only`, `can_video`, `can_screen`,
`can_voice_activity`, `priority`), server mute and deafen, and a grant:

    SignedCallGrant{grant: CallGrant, signature: Ed25519(server key,
      "openarcanum/call-grant/v1" || grant)}
    CallGrant{call_id, server_key, call_nonce, participant, kid = slot,
      audio = Speak, video, screen, priority, expires_at (≤ 10 min),
      max_participants (99)}
    call_id = SHA-256("openarcanum/call-id/v1" || 0x00 || server_key || call_nonce)[:16]

Grants carry no names: the relay knows participants only by their random
ids. The session also carries the call's current signed control (§12.3).

`can_voice_activity` (Use Voice Activity in that channel) is enforced by
the client alone: frames are end-to-end encrypted, so the relay can't tell
push-to-talk audio from voice-activity audio, and the grant has no field
for it. A client whose session says false sends audio only while its
push-to-talk key is held, whatever its input mode; a later `VoiceUpdate`
can turn it on or off mid-call (docs/threat-model.md "Calls").

The client sends `CallJoin{grant, offer, kinds, control}` to that relay
(`kinds` maps each send transceiver's mid to `audio`, `camera`, `screen` or
`screen-audio`, each at most once; clients create all four up front and
start media with `replaceTrack`). The relay checks the signature, that the
call id commits to the signing key, the expiry, the participant limit and
that the participant wasn't removed; applies the control the client
brought (signed by the same key, for this call, if newer than what it has),
so a relay starting the call enforces mutes and removals from the first
participant on; and answers with `CallAnswer`. Afterwards only
the relay offers (`CallSignal{type: "offer"}`, when others' tracks come and
go); the client answers. Tracks the grant doesn't allow are not forwarded.
Remote tracks carry the sender's participant id (hex) as stream id and the
kind as track id.

Every `VoiceUpdate` carries a fresh grant and the current control.
Reconnecting to the relay (`VoiceJoin{rejoin: true}`) gives the same
participant id and slot **and a new key**: a reconnecting client starts its
SFrame counters at zero again, so the old key must never be used again
(clients also refuse to send under a key id they have used before). A
session may join or rejoin five times at once, then once every 3 s (each
join rotates everyone's key). After a community reconnect (a new session
isn't in the call any more) the client rejoins, or joins afresh.

A permission the relay enforces from the grant (Speak, Video, Screen Share)
that is *gained* mid-call needs a new grant at the relay: the client
rejoins when its session shows one. Permissions *lost* mid-call take effect
at once through the control.

### 12.3 During a call

- `VoiceState{self_mute, self_deaf, camera, screen}`: shown to everyone.
- `VoiceRooms` (server push, after `Sync` and on every change): who is in
  each voice channel the member can see (View Channel), without participant
  ids or keys.
- Simulcast: camera and screen are sent in three layers (`l` ¼, `m` ½,
  `h` full; 150/500/1500 kbit/s). `CallLayer{source, kind, rid}` asks the
  relay for one layer of one source (`""` pauses it); the client picks by
  the size its tile shows, capped by how its link copes: video loss over 5%
  lowers the cap one layer, and 10 s under 1% raises it again. The relay switches layers at keyframes and rewrites
  sequence numbers and timestamps. Video is **paused until asked for**:
  clients ask only for the videos their user clicked to watch. A request
  may come before the source sends that video, or before its join reaches
  the relay (the server lists participants first); the relay keeps it (at
  most 198 per receiver, dropping requests for absent participants first)
  and applies it when the video arrives.
- Listening: `CallListen{deaf, no_audio, screen_audio}` (client → relay)
  states the whole audio choice of one receiver, replacing the last one:
  nothing while deafened; not the participants in `no_audio` (muted on that
  device only, "Mute for me"); screen audio only from the participants in
  `screen_audio` (screens watched there and not muted). Until the first
  one: every microphone and no screen audio. The relay forwards nothing
  else to that receiver. Each receiver has its own outgoing track per audio
  source, written only while it listens, so pausing never takes a track off
  a sender (which would break that receiver's later renegotiations and lose
  the stream id naming whose audio it is); server deafen uses the same
  path.
- Watchers: `CallWatchers{watches: [CallWatch{kind, receivers, layers}]}`
  (relay → client, on the connection that joined with that participant's
  grant, and only about its own media) says, for each of `audio`,
  `camera`, `screen` and `screen-audio`, how many other participants the
  relay would forward it to now (what they asked for, within the sender's
  grant and the control: a server-muted participant has no audio
  receivers) and which simulcast layers they want. Sent after the join and
  on every change, in order. A client encodes and sends only kinds with
  receivers, and only those layers (`RTCRtpSender` encodings `active`);
  the track itself stays (the camera's preview, the voice detector). A
  client that never gets `CallWatchers` (an older relay) sends everything.
  A relay could lie and say nobody watches: that is no more than the
  dropping it can always do.
- Moderators (signed admin actions, §4.4): `VoiceMute`, `VoiceDeafen`
  (stored on the member, kept across calls) and `VoiceMove` (to another
  voice channel, or 0 to disconnect). The server sends the relay a
  `SignedCallControl` (`"openarcanum/call-control/v1"`, same server key; the
  highest `seq` wins; at most 1000 entries per list, each a 16-byte
  participant id) listing who is muted
  (server mute, or no Speak), deafened, without Video (`no_video`) or
  Screen Share (`no_screen`), and removed (the newest 500 who left or were
  removed, for as long as their grants could still be used). A mute (or no
  Speak) silences screen audio too, since the relay can't tell what an
  audio track really carries; screen audio needs both Speak and Screen
  Share. The server re-checks participants' permissions
  after every admin action and every 10 s (timeouts ending, roles changing),
  and ends the call for anyone who lost Connect.
- A moved or disconnected member gets `VoiceSession{ended, moved_to}`; the
  client joins `moved_to`.

### 12.4 Relay failure

A client that loses the relay reconnects (with `rejoin`). If the relay stays
unreachable for 8 s it sends `VoiceJoin{failed_relay}` (which, if the call
doesn't move, rekeys like a rejoin). The server moves the
call to another relay that carries calls when it lost its own connection to
the call's relay, or when at least half the participants reported it in the
last 30 s (at most one move per 10 s); everyone gets a new epoch, the new
relay and fresh grants, and reconnects.

A media connection that never comes up (ICE fails, or isn't connected 15 s
after the relay's answer) is not reported: the relay answered over its
WebSocket, so it's the UDP path that's blocked, and every rejoin would
rotate everyone's key for nothing. The client tells the user (with the
relay's media address from the answer's candidates) and leaves the call
(`VoiceLeave`).

### 12.5 Relay limits

Anyone can make a server key and sign grants for a call of their own, so a
relay bounds what calls can take (relay settings, docs/operations.md):
`max_calls` (1000), `max_call_peers` participants in all calls (5000),
`max_call_peers_per_ip` (20), `call_peer_bitrate` per participant (8 Mbit/s;
media beyond it is dropped, not forwarded), and a connection is in at most
two calls. `call_servers` (optional) restricts the relay to calls run by
the listed community server keys.

