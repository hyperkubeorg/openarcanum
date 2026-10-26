# Cryptography inventory

Every place OpenArcanum uses cryptography and what it uses. Part of the
external-audit package (`docs/audit-package.md`).

The project's rule for providers: vodozemac for DMs, Noise for sessions,
`spake2` for device linking, SFrame per RFC 9605 for call media, and
primitives only from `cryptography` and the Python standard library on the
client (both backed by OpenSSL) and the Go standard library or
`golang.org/x/crypto` on the servers.

**One known deviation:** SFrame (#17) runs in the webview page, so its
AES-GCM and HMAC come from Chromium's WebCrypto (`crypto.subtle`), which is
not one of those providers. Why there's no alternative and what guards it
are under "Deviation: WebCrypto for SFrame" below.

**Rule:** no construction of our own. Each row is either a library's
protocol, a published spec implemented with its official test vectors, or a
single primitive used in the standard way (an AEAD seal with a random nonce
and associated data that names what is sealed).

## Libraries

| Library | Version | Language | Used for |
|---|---|---|---|
| `vodozemac` (PyPI `vodozemac`) | 0.10.0 (pinned, hashes in `uv.lock`) | Python bindings to the Rust crate | Olm, Megolm (DMs, device sync, statuses) |
| `noiseprotocol` | 0.3.1, vendored in `clients/desktop/src/openarcanum/_vendor/noise/` (VENDORED.md) | Python | Noise XX sessions (client) |
| `flynn/noise` | per `go.mod` | Go | Noise XX sessions (servers) |
| `spake2` | 0.9 (pinned) | Python | device-link PAKE |
| `cryptography` | per `uv.lock` | Python (OpenSSL) | Ed25519, X25519, ChaCha20-Poly1305, AES-GCM, HKDF-SHA256 |
| Python standard library (`hashlib`, `hmac`, `secrets`) | the bundled CPython | Python (OpenSSL) | scrypt (vault and backups), SHA-256, SHA-512, constant-time comparison, randomness |
| Go `crypto/*`, `golang.org/x/crypto` | per `go.mod` | Go | Ed25519, ChaCha20-Poly1305, XChaCha20-Poly1305, HMAC-SHA256, SHA-256 |
| `filippo.io/age` | v1.3.2 (per `go.mod`) | Go | community server backups (the age v1 format, C2SP age spec) |
| Chromium WebCrypto (`crypto.subtle`, BoringSSL underneath) | the Chromium inside the pinned PySide6/QtWebEngine (6.12.0: Chromium 140) | JavaScript, in the webview page | SFrame only (#17): AES-GCM (`AES_128_GCM_SHA256_128`), and HMAC-SHA256 for RFC 9605's HKDF. **Outside the provider rule: the known deviation below** |

## Uses

| # | Purpose | Construction | Parameters | Evidence | Code |
|---|---|---|---|---|---|
| 1 | Identity, owner, server, DM identity and sync identity keys | Ed25519 signatures over `context \|\| 0x00 \|\| message` (domain separation; contexts in protocol.md §1) | 32-byte keys | Go/Python interop tests (`test_interop.py`) | `internal/identity`, `clients/desktop/src/openarcanum/keys.py` |
| 2 | Client ↔ community server sessions | Noise `XX_25519_ChaChaPoly_SHA256`; the handshake payload carries the Ed25519 identity and its signature over the Noise static key | per Noise spec | 800 Python + 408 Go Noise vectors; Go↔Python interop, tamper tests | `internal/session`, `clients/desktop/src/openarcanum/session.py` |
| 3 | Server certs and owner sets | Ed25519 signature chains (protocol.md §3.1) | | `internal/identity/owners_test.go`, `test_owners.py` | `internal/identity`, `clients/desktop/src/openarcanum/owners.py` |
| 4 | Channel events (community → members) | ChaCha20-Poly1305, random 96-bit nonce, AAD = `"openarcanum/channel-event/v1" \|\| channel id \|\| key epoch`; the envelope is then Ed25519-signed by the server key | per-channel 256-bit key, rotated on access loss | community tests; `FuzzClientMessage` | `internal/community/server.go`, `clients/desktop/src/openarcanum/community.py` |
| 5 | Community files at rest | The community server seals each 1 MiB chunk with ChaCha20-Poly1305 under a per-file random key: nonce = chunk index (unique per key), AAD = `"openarcanum/file-chunk/v1" \|\| file id \|\| index`. The key and chunk count live in the file's metadata, itself sealed at rest (#6). Chunks reach members inside their Noise session (#2); the server is inside the community's trust zone (architecture §2), so truncation by it isn't a threat this protects against, and the client still checks the assembled size against the file reference | per-file 256-bit key | `internal/community/files_retention_test.go`, `test_e2e_community.py` | `internal/community/files.go`, `clients/desktop/src/openarcanum/core.py` |
| 6 | Community server data at rest | XChaCha20-Poly1305, random 192-bit nonce, AAD = `"openarcanum/at-rest/v1" 0x00 table 0x00 column 0x00 row`; monthly content keys wrapped by the storage key; HMAC-SHA256 lookup hashes for public keys | storage key 256-bit | `atrest_test.go` (no plaintext in DB/WAL/backup, tampered rows reported) | `internal/community/atrest.go` |
| 7 | Community server backups | An age v1 file made by `filippo.io/age` (age-encryption.org/v1, C2SP spec): one `scrypt` recipient stanza wrapping the random file key, whose passphrase is the hex storage key; header MAC; payload = age STREAM (ChaCha20-Poly1305, 64 KiB chunks, counter nonce with last-chunk flag, payload key HKDF-SHA256 from the file key and a 16-byte nonce). Streamed in bounded memory; readable by the stock `age` tool given the key | storage key (256-bit, as the scrypt passphrase); scrypt work factor 2^10 (the passphrase is a random key, so work adds nothing), restore accepts at most 2^18 | `TestBackupFormat` (round trip, wrong key, tamper, truncation, decryption with the age library directly), `TestBackupStreamsAndKeepsServing`, `TestBackupRestoreAcrossMonthKeyRotation` | `internal/community/jobs.go`, `move.go` |
| 8 | Client vault (`vault.bin`), backups the user exports | scrypt → ChaCha20-Poly1305, random nonce, AAD = magic \|\| header (KDF parameters, salt) | scrypt N=2^17, r=8, p=1, 16-byte salt | `test_e2e_community.py` (wrong passphrase, backoff) | `clients/desktop/src/openarcanum/vault.py`, `keys.py` |
| 9 | Client state, cache, DM history, Olm state files | ChaCha20-Poly1305, random nonce, AAD = file magic (+ file id for cached files), keys from the vault | 256-bit keys | `test_olm.py` (corrupt state refused), cache tests | `vault.py`, `cache.py`, `dm.py`, `olm.py` |
| 10 | DMs and group DMs | vodozemac Olm (key distribution, sender/recipient keys bound in the payload as in Matrix `m.olm.v1`) and Megolm (events); rotation on recipient-set change, 100 messages, 7 days | | `test_olm.py`, `test_groups.py`, `test_e2e_dm.py` | `clients/desktop/src/openarcanum/olm.py`, `dm.py`, `groups.py` |
| 11 | DM device lists, contact cards, group states | Ed25519 signatures by the DM identity; device consent and one-time keys signed by the device's vodozemac ed25519 key | | `test_fuzz_decoders.py`, `test_groups.py` | `dm.py`, `groups.py` |
| 12 | DM attachments | age v1 payload (STREAM: ChaCha20-Poly1305 in 64 KiB chunks, nonce = 11-byte counter \|\| last flag; payload key = HKDF-SHA256(16-byte file key, salt = 16-byte nonce, info "payload")) | fresh random file key per file | all 37 applicable C2SP CCTV age vectors (`tests/vectors/age`) | `clients/desktop/src/openarcanum/filecrypt.py` |
| 13 | DM attachment fetch tokens | 32 random bytes; maintainers store SHA-256(`"openarcanum/blob-token/v1" 0x00 \|\| token`) | | `internal/meta/blobs_test.go` | `internal/meta/blobs.go`, `meta.py` |
| 14 | Mailbox inbox addresses | SHA-256(`"openarcanum/inbox/v1" 0x00 \|\| token`), token 32 random bytes | | meta tests | `internal/meta`, `meta.py` |
| 14b | Mailbox item global ids (maintainer clusters) | SHA-256(`"openarcanum/mailbox-item/v1" 0x00 \|\| inbox \|\| blob`); the inbox is 32 bytes, so the input is unambiguous. Computed by every node and client itself, never taken from a peer. Not a secret, an identifier | | `internal/meta` `FuzzPeerBatch`, `TestClusterSameMessageStoredOnce`, `TestMailboxGlobalIDMigration`; `test_e2e_mailbox_failover.py` | `internal/meta/mailbox_repl.go`, `meta.py`, `dm.py` |
| 15 | Device linking | SPAKE2 (symmetric, `idSymmetric = "openarcanum/link/v1"`, password = the typed code); session keys HKDF-SHA256(K, direction label); messages ChaCha20-Poly1305 with random nonces; key confirmation before anything is sent. The Magic Wormhole design | code: nameplate + 3 words (≈24 bits), one guess, 5 minutes | `test_e2e_devices.py` (wrong code cancels, expired code refused) | `clients/desktop/src/openarcanum/devices.py` |
| 16 | Safety numbers | Display encoding of public keys: SHA-512 over both identity keys (sorted) with a domain label (`openarcanum/safety-number/v1`), 12 groups of 5 digits (protocol.md §9.2). Not a secret, not a key | | `test_ui_dm.py` (both sides match) | `dm.py` |
| 17 | Calls (voice, video, screen share) | SFrame (RFC 9605) on every encoded frame, applied in the webview through `createEncodedStreams`; cipher suite `AES_128_GCM_SHA256_128`; key id = `epoch << 8 \| slot`, so each sender's key and salt are derived (RFC 9605 §4.4.2) from the per-epoch call key and its own key id. The community server makes a random 32-byte call key per epoch and changes it on every join, leave, rejoin and relay move; relays never get it (protocol.md §12.1). Outside SFrame the relay reads only RTP headers (mid, rid and congestion control extensions; never the audio level) and, authenticated, the VP8 payload header; voice is Opus at a constant bitrate without DTX, so with SFrame's fixed overhead every voice packet has the same size (protocol.md §12.1 "What stays in the clear") | 256-bit call key per epoch | RFC 9605 test vectors, all five cipher suites: `clients/desktop/tests/js/sframe.test.mjs` (the page's `sframe.js`, run under Node's WebCrypto by `test_js.py`), `internal/testutil/sframe` (the Go test participants, Go stdlib crypto). The page's own engine is checked by interop: in `test_calls.py` the real pages and the vector-checked Go participants decrypt each other's frames (`test_fifty_people_in_a_voice_call`, `test_twenty_five_people_with_video_and_screen_share`: 0 failures), and frames under a wrong key don't play (`test_frames_are_end_to_end_encrypted`). Primitives: Chromium WebCrypto (deviation below) | `clients/shared/ui/js/sframe.js`, `call.js`; `internal/community/voice.go` |
| 18 | Randomness | `os.urandom` and `secrets` in Python, Go `crypto/rand` | | | |
| 19 | Release manifest (auto-update) | Ed25519 signature (`cryptography`) over `openarcanum/release-manifest/v1 \|\| 0x00 \|\| payload`, the canonical JSON bytes, verified before they are parsed; each download's SHA-256 compared with `hmac.compare_digest` | Release keys: Ed25519, PKCS#8 PEM, kept offline; several trusted keys during rotation | `test_updates.py` (signature, context, rotation, every refusal) | `clients/desktop/src/openarcanum/updates.py`, `keys.py` (context), `clients/desktop/scripts/release_manifest.py`; `docs/release.md` |
| 20 | Relay proof-of-work | Hashcash (A. Back, 2002) with SHA-256: `SHA-256("openarcanum/relay-pow/v1" 0x00 \|\| Hello.nonce \|\| counter)` with `pow_bits` (≤ 24) leading zero bits; the challenge is the relay's fresh 32-byte per-connection nonce. Not a secret, a cost | | `internal/pow` vectors, mirrored in `clients/desktop/tests/test_versions_abuse.py`; `internal/relay` `TestProofOfWork*` | `internal/pow`, `clients/desktop/src/openarcanum/pow.py` |
| 21 | Fan-out tokens and committed topics | Ed25519 (Go `crypto/ed25519`) over `openarcanum/fanout-token/v2 \|\| 0x00 \|\| FanoutToken{issuer, relay_nonce, expires_at, relay_key, url}` (≤ 1 h), bound to the relay connection as row 28; topics `r \|\| SHA-256("openarcanum/fanout-topic/v1" 0x00 \|\| server_key \|\| r)[:16]`, `r` 16 random bytes (same binding as call ids) | community server key | `internal/fanout` `TestTokenWrongRelay`; `internal/relay` `TestLargeFanoutNeedsToken`, `TestFanoutTokenChecks`, `TestTopicCommitment`, `TestCrossRelayHijack*`; `internal/community` `TestLargeChannelFanoutUsesToken` | `internal/fanout`, `internal/community/fanout.go` |
| 22 | Listing icons | SHA-256 of the icon inside the signed listing; the icon next to it must match | | `internal/meta` `TestListingIconBoundByHash`, `test_listing_icon_bound_to_hash` | `internal/meta/meta.go`, `meta.py` |
| 23 | Dedicated relay moderation commands | Ed25519 (Go `crypto/ed25519`) over `openarcanum/relay-moderation/v2 \|\| 0x00 \|\| ModerationCommand{server_key, relay_nonce, command_id, expires_at, block \| unblock, relay_key, url}`, bound to the relay connection as row 28, expiring within 5 min, one use per 16-byte random command id; block ids are 16 random bytes (`crypto/rand`). No new construction: the fan-out token pattern (row 21) with its own context | community server key | `internal/relaymod` `TestSignVerify`, `TestCommandBoundToRelay`, `FuzzVerify`; `internal/relay` `TestModerationCommandChecks`, `FuzzModerationFrames` | `internal/relaymod`, `internal/relay/dedicated.go`, `internal/community/netblock.go` |
| 24 | Community heartbeats (status reports to maintainers) | Ed25519 (Go `crypto/ed25519`) over `openarcanum/community-heartbeat/v1 \|\| 0x00 \|\| CommunityHeartbeat{route_id, server_key, at, stopping, relay counts}`; maintainers accept it only for the server key of the route they hold, clients verify it (`cryptography`) against the server key they know. No new construction: the route signature (row 1) with its own context | community server key | `internal/meta` `TestCommunityHeartbeatStates`, `FuzzMetaCommunityHeartbeat`; `internal/community` `TestHeartbeatsReportStatus`; Go-signed, Python-verified in `test_e2e_hosting.py` | `internal/community/heartbeat.go`, `internal/meta/status.go`, `clients/desktop/src/openarcanum/meta.py` |
| 25 | User statuses (state, custom text, expiry) to contacts and communities | vodozemac Megolm, one outbound session per audience per device (the DM design, #10, with the audience as the group): one Megolm message per status per audience; session keys over vodozemac Olm (`OlmPayload.status_key`, sender and recipient keys bound in the payload), exported at the newest message index with `InboundGroupSession.export_at` (Matrix's key-forwarding format) so a device joining the audience reads the current status only. Community audiences: each receiving device has its own Olm account (`status:<community>`) whose `StatusDevice` entry (curve25519, ed25519, signed fallback key) is Ed25519-signed by the member key over `openarcanum/status-device/v1 \|\| 0x00 \|\| StatusDevice` (#1); the community server stores and forwards entries, Olm key drops and Megolm blobs, sealed at rest (#6). Contacts: keys over DM Olm sessions, statuses into mailbox inboxes. New session when a recipient device leaves the audience, after 1000 messages, after 7 days. No new construction | | `test_statuses.py` (round trip; what the community server stores and forwards and what the maintainer node stores hold no plaintext; a member who left and a removed contact can't read the next status; un-share; expiry; Invisible; exported key reads only the newest status), `internal/community` `TestStatusStoredAndForwardedOpaquely`, `TestStatusOfRemovedMemberForgotten` | `clients/desktop/src/openarcanum/statuses.py`, `olm.py` (`exported_key`, `add_inbound_exported`, `batch`), `dm.py`; `internal/community/status.go` |
| 26 | Maintainer sets (a maintainer cluster's member list) | Ed25519 (Go `crypto/ed25519`; clients `cryptography`) over `openarcanum/maintainer-set/v1 \|\| 0x00 \|\| MaintainerSet{operator_key, seq, issued_at, expires_at, nodes}`; nodes and clients pin the operator key, keep the highest valid seq and refuse expired sets (180 days by default, 2 years at most). No new construction: the route signature (row 1) with its own context | cluster operator key (offline) | `internal/meta/clusterset` `TestVerify`; `internal/meta` `TestMaintainerSetTrust` | `internal/meta/clusterset`, `internal/meta/cluster.go`, `cmd/meta/cluster.go` |
| 27 | Maintainer peer sessions (node to node) | The Noise XX session of row 2 (`internal/session`, Noise_XX_25519_ChaChaPoly_SHA256) over a WebSocket, with its own prologue `openarcanum/meta-peer/v1` so it can't be taken for a community session; each side signs its X25519 static key with its Ed25519 node key (`openarcanum/noise-static/v1`), and the responder refuses node keys it doesn't trust. No new construction | node key (Ed25519, `DATA/node.key`), Noise static (X25519, `DATA/node.noise`); never in backups | `internal/session` `TestPrologueSeparatesUses`, `TestVerifyClientRefuses`; `internal/meta` `TestPeerSessionNeedsTrustBothWays`, `TestPeerSessionChecksTheNodeKey` | `internal/meta/peer.go` |
| 28 | Relay identity and connection binding | Ed25519 (Go `crypto/ed25519`; clients `cryptography`). The relay proves its key per connection: `Hello.relay_signature` over `openarcanum/relay-hello/v1 \|\| 0x00 \|\| RelayBinding{relay_key, url, nonce}` (deterministic protobuf). Endpoint registrations (`openarcanum/relay-endpoint/v2`, over the same `RelayBinding`), fan-out tokens (row 21) and moderation commands (row 23) name the relay key, the canonical URL the client dialled and the connection's 32-byte random nonce; the relay accepts only its own key, its own URL (any canonical URL when it has no `public_url`) and the connection's nonce; clients refuse a Hello without a valid proof, a relay announcing another URL than the one dialled, a key other than one pinned for the URL, and (per process) a URL-less relay's key at a second URL. Replaces the v1 contexts, which signed the nonce alone and let a relay relay another relay's nonce (docs/protocol.md §2 "Relay identity"). No new construction: Ed25519 with the §1 domain separation under new versioned contexts | relay key (`registry_key_file`, or fresh per run) | `internal/relay` `TestCrossRelayHijackCopiedHello`, `TestCrossRelayHijackOwnKey`, `TestCrossRelayHijackRelayWithoutURL`, `TestHelloProofRequired`, `TestEndpointRegistrationBoundToRelay`; `internal/relayclient` `TestKeyBook`; `clients/desktop/tests/test_relay.py::test_endpoint_registration_bound_to_relay_identity` | `internal/relayid`, `internal/relay/relay.go`, `internal/relayclient` (`keys.go`), `clients/desktop/src/openarcanum/relay.py` |
| 29 | Maintainer moderation decisions passed between cluster members | Ed25519 (Go `crypto/ed25519`) over `openarcanum/peer-decision/v1 \|\| 0x00 \|\| origin \|\| kind \|\| 0x00 \|\| key \|\| 0x00 \|\| action \|\| 0x00 \|\| at` (8 bytes big-endian); members accept only decisions that verify for their origin and aren't dated more than 10 min ahead. No new construction: the route signature (row 1) with its own context | node key (Ed25519, `DATA/node.key`) | `internal/meta` `TestPeerDecisionsAreSigned` | `internal/meta/decisions.go` |
| 30 | Website data at rest (reports, internal notes, admins' TOTP secrets) | Row 6's construction, shared code (`internal/atrest`): XChaCha20-Poly1305, random 192-bit nonce, AAD = `"openarcanum/site-at-rest/v1" 0x00 table 0x00 column 0x00 row`; a sealed key check refuses a wrong key at start | storage key 256-bit (`storage_key_file`) | `internal/site` `TestReportsEncryptedAtRest` (no plaintext in the data directory, wrong key refused), `TestTOTP`; `internal/atrest` tests | `internal/site/store.go`, `internal/atrest` |
| 31 | Website admin passphrases | Argon2id (`golang.org/x/crypto/argon2`, RFC 9106) in PHC string format; constant-time comparison | m = 64 MiB, t = 3, p = 4, 16-byte salt, 32-byte hash (RFC 9106 §4 second recommendation); at most 2 at a time | `TestPassphraseHashing`, `TestLockoutAndIPLimit` | `internal/site/auth.go` |
| 32 | Website admin sessions and second factor | Session tokens: 32 random bytes, stored as SHA-256(`"openarcanum/site-session/v1" 0x00 \|\| token`); CSRF tokens 32 random bytes per session. TOTP: RFC 6238 with its defaults (HMAC-SHA1 from Go `crypto/hmac`/`crypto/sha1`, 30 s, 6 digits), ±1 step, each step accepted once | 160-bit TOTP secrets (sealed, #30) | `TestSignInSessionCookie`, `TestAdminFormsNeedCSRF`, `TestTOTP` (RFC 6238 Appendix B vectors, replay) | `internal/site/auth.go`, `admin.go` |
| 33 | Website report-form proof of work | Row 20's Hashcash under its own context: `SHA-256("openarcanum/site-pow/v1" 0x00 \|\| challenge \|\| counter)`; challenge = issue time \|\| 16 random bytes \|\| HMAC-SHA256(key derived from the storage key, …)[:16], valid 1 h, accepted once. The browser's search uses a SHA-256 written out in `static/pow.js` (no secret involved; the server checks every result with Go's SHA-256) | `report_pow_bits` (18, ≤ 24) | `TestPoWJavaScriptSolver` (JS and Go find the same counter), `TestReportValidation`, `internal/pow` `TestContextsAreSeparate` | `internal/site/limits.go`, `static/pow.js`, `internal/pow` |

## Deviation: WebCrypto for SFrame

Calls run WebRTC in the QtWebEngine page, with SFrame (RFC 9605) applied to
each encoded frame through `createEncodedStreams` inside the page
(`docs/voice-decision.md`). The per-call media key is the only key allowed
into JavaScript. Inside the page, the only cryptography available is the
browser engine's WebCrypto, so `ui/js/sframe.js` takes its primitives from
`crypto.subtle`:

- **AES-GCM** (`subtle.encrypt`/`decrypt`) seals each frame
  (`AES_128_GCM_SHA256_128`, the suite calls use).
- **HMAC-SHA256** (`subtle.sign`) implements RFC 9605's HKDF (RFC 5869
  extract and expand, written out in `sframe.js` so the RFC's intermediate
  values can be checked against the vectors).
- The AES-CTR + HMAC suites (1 to 3) and `AES_256_GCM_SHA512_128` are present
  only so every RFC vector can be checked; calls never use them.

The SFrame framing (header, key and salt derivation, nonce) is the RFC's,
checked against the official vectors.

**Why there's no alternative within the provider rule.**

- *A JavaScript crypto library in the page* (a pure-JS AES-GCM) would also
  be outside the rule, and worse: no constant-time guarantees, slower on
  every frame, and one more dependency to vendor and audit, where WebCrypto
  is Chromium's BoringSSL.
- *Moving SFrame out of the page* into Python (`cryptography`) or Go
  would put every encoded media frame through Python: the page would hand
  each outgoing frame across the pywebview bridge and wait for it to come
  back sealed, and do the same for every incoming frame from every other
  participant (up to 49 in a voice call, 24 cameras in a video call). The
  bridge is a JSON call path, not built for media rates; the round trips
  would add latency against the 250 ms mouth-to-ear target, and the
  decrypted media would then sit in Python as well as in the page. It
  would keep the media key out of JavaScript, but that key is the one
  allowed there anyway.
- *Sending media unencrypted to the relay* would give up end-to-end
  encryption of call media, and relays never get keys.

**What guards it.** The JS code passes every RFC 9605 vector; the page's
real engine interoperates with the Go implementation, which passes the
same vectors (evidence in #17); the key that reaches WebCrypto is only the
per-call media key (current and previous epoch), and the threat model
records the trust in Chromium's WebCrypto ("Calls", residual risks).
Auditors should look at this specifically.

## What JavaScript never holds

Identity, vault, DM, status, channel and file keys (community and DM) and fetch
tokens never reach the webview (`clients/desktop/tests/test_key_boundary.py`).
Only the per-call media key (current and previous epoch) does, for SFrame in
the webview media path (`docs/voice-decision.md`), and it is used there only
through WebCrypto (the deviation above).
