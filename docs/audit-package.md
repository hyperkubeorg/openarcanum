# External security audit package

What an external security auditor needs, in reading order. It describes
the code in this repository at the commit the audit is given.

## 1. Scope

In scope: the desktop client (`clients/desktop/src/openarcanum/`, Python, and the
webview UI in `clients/shared/ui/`), the three servers (`cmd/relay`,
`cmd/community`, `cmd/meta` and `internal/`), the wire formats
(`proto/openarcanum/v1/`), and the build, release and update path (installers
in `clients/desktop/packaging/`, the signed release manifest, `.github/workflows/`).

Priorities, highest first:

1. **DMs and group DMs** (`dm.py`, `olm.py`, `groups.py`, `dmfiles.py`,
   `filecrypt.py`): vodozemac usage (session establishment, room-key
   distribution and acceptance rules, rotation triggers, replay handling,
   device-list verification), the group-state rules every client enforces
   (there is no server for a group DM), DM policy and requests,
   identity-key change detection, and attachment encryption (age v1
   payload).
2. **Calls** (`ui/js/call.js`, `ui/js/sframe.js`, `voice.py`,
   `internal/community/voice.go`, `internal/sfu`, `internal/callauth`):
   SFrame (RFC 9605) applied in the webview page through
   `createEncodedStreams`, with **WebCrypto** as its primitive provider (a
   known deviation, below); call keys made per epoch by the community
   server and rotated on every join, leave and relay move; key ids
   (`epoch << 8 | slot`) and nonce-reuse prevention; the **keyless SFU** on
   relays (Pion), which admits only on grants signed by the community
   server and enforces Speak, Video, Screen share, server mute and deafen
   from signed **call controls**; relay failover. The per-call key is the
   one key allowed into JavaScript.
3. **Device linking and sync** (`devices.py`, the sync parts of `dm.py`):
   SPAKE2 use, the transfer, history copy, removal.
4. **Sessions and owner keys** (`internal/session`, `session.py`,
   `_vendor/noise`, `internal/identity`, `owners.py`): Noise XX with
   identity binding, owner-set chains (multiple owners, any owner signs
   server certs), pinning, rollback and fork refusal, protocol version
   negotiation (`internal/version`).
5. **Community server** (`internal/community`): permission and feature
   toggle enforcement, channel key rotation, at-rest encryption and
   month-key purges, moderation and AutoMod, link-preview fetching (SSRF
   controls), custom emoji image checks, backups (age v1), move to new
   hardware.
6. **Auto-update** (`updates.py`, `clients/desktop/scripts/release_manifest.py`,
   `.github/workflows/release.yml`): manifest signature checked before
   parsing, downgrade and replay refusal, download hashes, install only with
   consent and while unlocked, the release key confined to one reviewed CI
   step.
7. **Maintainer node and relay** (`internal/meta`, `internal/relay`,
   `internal/pow`, `internal/fanout`): what they can learn; the **mailbox
   strategy** (write to every independent node and every maintainer
   cluster; replication only between members of one cluster; protocol.md
   §7.1, §7.5); the DM blob store and its fetch tokens; abuse controls:
   proof-of-work for new relay connections under load, signed **fan-out
   tokens** for large fan-outs, quotas, the optional chunk cache; directory
   snapshot verification.
8. **Webview boundary and local storage** (`api.py`, `bus.py`, `guard.py`,
   `ui/`, `vault.py`, `keychain.py`): CSP (`'unsafe-eval'` only, for
   pywebview's bridge), no remote content, no webview storage, the rule that
   keys never enter JavaScript (except the per-call key), the vault and
   encrypted state files, and **staying signed in** (the vault key kept on
   the device after setup: the OS keychain, or a 0600 key file where there
   is none; on by default, "Ask for my passphrase when OpenArcanum starts"
   turns it off; a lock deletes it).
9. **Platform integration** (`instance.py`, `crashlog.py`, `notify.py`,
   installers): `openarcanum://invite/` links and single-instance hand-off
   (per-user names, no auto-join), crash logs without content,
   notifications that show nothing while locked, macOS hardened runtime
   entitlements.

### Known deviation

The project takes cryptographic primitives only from `cryptography` and the
Python standard library on the client and the Go standard library or
`x/crypto` on the servers. SFrame in the webview page is the exception: it
takes AES-GCM and HMAC (for RFC 9605's HKDF) from Chromium's WebCrypto, the
only cryptography inside the page. The reasoning, the alternatives and the
guards are in `docs/crypto-inventory.md` "Deviation: WebCrypto for SFrame"
and `docs/threat-model.md` "Calls". Please look at it specifically.

## 2. Documents

| Document | What it gives the auditor |
|---|---|
| `docs/architecture.md` | The design: trust zones, components, who sees what, the client's flows (first run, joining, creating, linking devices) and where it stores data |
| `docs/threat-model.md` | Per component: assets, adversaries, controls, residual risks (client, webview, relays, maintainers, DMs, attachments, devices, at rest, moderation, blob store, messaging, owners, permissions, calls, installers, updates, server operations) |
| `docs/crypto-inventory.md` | Every cryptographic use with library, construction, parameters and evidence, and the WebCrypto deviation |
| `docs/protocol.md` | Message flows and wire formats (with `proto/openarcanum/v1/*.proto` as the field-level source of truth): sessions and owner sets §3, community ops §4, maintainers and the mailbox strategy §7, moving a community §8, DMs §9, devices §10, blob store §11, calls §12 |
| `docs/voice-decision.md` | Why calls use WebRTC in the webview with SFrame and a keyless SFU; engine capability checks; latency measurements |
| `docs/release.md` | Release signing, the manifest format, key rotation, the update test source |
| `docs/packaging.md` | Installers, code signing, macOS entitlements |
| `docs/operations.md` | Node configuration, data at rest ("What stays in plaintext, and why"), abuse controls, quotas, backups, migrations, logs and metrics |
| `docs/settings.md` | Every user, community and node setting with its default |
| `docs/load-tests.md` | Capacity results, including proof-of-work cost under load |
| `docs/user-guide.md`, `docs/admin-guide.md` | What users and admins can do, as the UI presents it (useful to spot UI promises the code doesn't keep) |
| `docs/manual-tests/` | Step-by-step tests for what needs real devices: voice and video, notifications, the tray, installers, platform integration |

## 3. Evidence that ships with the code

| Area | Tests |
|---|---|
| Noise | `clients/desktop/tests/test_noise_vectors.py` (800 vectors), `internal/session/vectors_test.go` (408), `clients/desktop/tests/test_interop.py` (Go ↔ Python) |
| Owner keys and certs | `internal/identity/owners_test.go`, `internal/community/owners_test.go`, `internal/meta/meta_test.go` `TestRoutesAndListingsFollowOwnerSet`, `TestRemovedOwnerCannotFreezeEntries`; `clients/desktop/tests/test_owners.py`, `test_e2e_owners.py`, `test_ui_owners.py` |
| Protocol versions | `internal/version` tests, `internal/relay` `TestVersionNegotiation`, `internal/meta` `TestAPIVersionNegotiation`, `clients/desktop/tests/test_versions_abuse.py` |
| Olm/Megolm layer | `clients/desktop/tests/test_olm.py` (replay, bounds, rotation limits) |
| Group rules | `clients/desktop/tests/test_groups.py` (forged and non-owner changes, forks, rollback) |
| DMs end to end | `clients/desktop/tests/test_e2e_dm.py`, `test_e2e_dm_files.py`, `test_ui_dm.py`, `test_e2e_mailbox_failover.py`, `test_mailbox_epoch.py` |
| DM attachments (age payload) | `clients/desktop/tests/test_filecrypt.py` (C2SP CCTV vectors) |
| DM blob store | `internal/meta/blobs_test.go`, `blobs_limits_test.go` (existence can't be probed, uploads can't be held open), `clients/desktop/tests/test_e2e_blobs.py` |
| Devices | `clients/desktop/tests/test_e2e_devices.py` (wrong and expired codes, history copy resume, removal) |
| Calls: SFrame | RFC 9605 vectors, all five suites: `clients/desktop/tests/js/sframe.test.mjs` (the page's `sframe.js`, through `test_js.py`), `internal/testutil/sframe` (Go); interop of the real pages with the Go participants and wrong-key frames: `clients/desktop/tests/test_calls.py` |
| Calls: SFU, grants and controls | `internal/sfu/sfu_test.go` `TestGrantsAndControlsGateMedia`, `TestControlBroughtByTheFirstJoiner`, `TestServerDeafen`, `TestAdmission`, `TestRelayLimits`; `internal/relay` `TestCallsNeedAnSFU`, `TestCallOverRelayFrames`; `internal/community/voice_test.go` `TestCallJoinGrantsAndRekeys`, `TestVoicePermissionsRecheckedMidCall`, `TestCallMovesToAnotherRelay`, `TestTimeoutMakesCallListenOnly` |
| Calls: end to end | `clients/desktop/tests/test_calls.py` (50-person voice, 25-person video and screen share, rekey mid-call, layers, latency and loss), `test_ui_voice.py`, `test_webengine.py` (engine capabilities) |
| Community server | `internal/community` `perms_test.go`, `toggles_test.go`, `moderation_test.go`, `automod_*_test.go`, `history_gate_test.go`, `join_rotation_test.go`; link previews `TestLinkPreviewRefusesLoopback`, `TestLinkPreviewHTTPSOnlyByDefault`, `preview_redirect_internal_test.go`; emoji `TestEmojiImageHeaders` |
| At rest and backups | `internal/community/atrest_test.go` (no plaintext in DB, WAL or backup), `atrest_bind_test.go`, `backup_format_test.go` `TestBackupFormat` (age v1: round trip, wrong key, tamper, truncation, decryption with the age library), `backup_stream_test.go`; maintainer `TestBackupRestoreEndToEnd`, `TestRestoreRefusesBadBackups` |
| Migrations | `internal/community` `migrate_every_test.go`, `internal/meta` `TestMigrationFromEveryVersionKeepsData`, `TestNewerSchemaRefused` |
| Relay abuse controls | `internal/pow` `TestVectors` (mirrored in `test_versions_abuse.py`); `internal/relay` `TestProofOfWorkOnlyUnderLoad`, `TestProofOfWorkCantBeReplayed`, `TestLargeFanoutNeedsToken`, `TestFanoutTokenChecks`, `TestTopicCommitment`, `TestChunkCacheNeverHoldsStreams`, `TestPerConnectionCaps`; `internal/fanout` tests; `internal/community` `TestLargeChannelFanoutUsesToken` |
| Maintainer limits | `internal/meta` `TestMailboxStoreMax`, `TestMailboxBytesPerNetwork`, `TestPerIPLimitsGroupIPv6Slash64`, `TestBlobQuotaPerNetwork`, `TestListingSignatureChecks`, `TestDirectorySnapshot` |
| Auto-update | `clients/desktop/tests/test_updates.py` (signature, context, key rotation, downgrade, replay, expiry, http and redirects refused, hash mismatches, consent), `test_ui_updates.py`, `test_packaging.py::test_release_signing_key_is_confined_to_one_reviewed_step` |
| Staying signed in | `clients/desktop/tests/test_keychain.py` (on after create/restore, keychain preferred, key file 0600 in a 0700 folder and refused when readable by others, "Ask for my passphrase" deletes the key and re-keys the vault, Lock now and idle lock delete it, an existing profile unchanged until it accepts the offer, wrong passphrase, passphrase change, stale key forgotten, moved data folder, UI flows, real OS keychain on CI); `test_e2e_devices.py::test_link_second_device` (linked device signed in; removal deletes the key); `test_formats.py` (key file and older preferences) |
| Key boundary | `clients/desktop/tests/test_key_boundary.py` (no identity, vault, DM, channel or file keys or tokens cross the bridge) |
| Webview | `clients/desktop/tests/test_app.py` (storage guard, hardened engine flags, sandbox kept on where possible, debugging only on request), `test_ui_dm.py` `assert_local_only` (no remote content); the CSP is in `ui/index.html` (threat model "Webview") |
| Invite links, single instance | `clients/desktop/tests/test_instance.py`, `test_ui_invite_link.py` |
| Logs and crashes | `internal/community` `TestServerLogsContainNoContent`, `internal/relay` `TestRelayMetricsAndLogsCarryNoContent`, `internal/meta` `TestMetaMetricsAndLogsCarryNoContent`, `clients/desktop/tests/test_e2e_dm.py::test_client_logs_carry_no_content_or_keys`, `test_crashlog.py` |
| Notifications | `clients/desktop/tests/test_notifications.py` (nothing but "New activity" while locked; markup escaped) |
| Fuzzing | Go `Fuzz*` targets in `internal/*` (listed by `scripts/fuzz-go --list`; 5 s each in `make check`, 6 min each nightly in CI), Hypothesis tests in `clients/desktop/tests/test_fuzz_decoders.py` and `test_session_fuzz.py` |
| Dependencies | `make audit` (govulncheck, pip-audit), `uv.lock` pinned with hashes, `scripts/check-lock` |

Real-device behaviour (audio, cameras, notifications, installers, OS
integration) is covered by the manual test scripts in `docs/manual-tests/`.

## 4. Known limitations to look at closely

- **SFrame primitives from WebCrypto** (above): the page's engine, not
  `cryptography` or Go, does AES-GCM for calls.
- **Shared call key per epoch.** Sender keys are derived from one call key
  and the key id, so a malicious participant together with a malicious relay
  could forge frames under another participant's key id within the current
  call and epoch (threat model "Calls").
- **Use voice activity** is enforced only by each member's app; Speak is the
  relay-enforced control.
- All of a user's devices are equals: each holds the identity keys
  (`docs/architecture.md` §4). A stolen unlocked device is a full compromise
  until the user replaces their identities.
- Megolm gives no forward secrecy within a session (100 messages / 7 days).
- Group DMs have no server: every client enforces the group rules, and
  concurrent group-state changes resolve deterministically.
- The vendored `noiseprotocol` 0.3.1 has had no upstream release since 2020
  (guarded by vectors, interop tests and fuzzing).
- The community server is inside its community's trust zone by design: it
  reads everything its members post there.
- Mailboxes aren't replicated between independent maintainer nodes:
  senders write to every node they use, and a node that loses its disk loses
  what it held. Only members of one maintainer cluster replicate each other's
  items (protocol.md §7.1, §7.5).
- Staying signed in (the default after setup) lets anyone who can use the
  OS account open the profile; where there is no keychain, anyone who can
  read the user's files can (the key file). Settings → Security says which.
  Locking deletes the key but doesn't re-key the vault, so a key copied
  before a lock opens the profile file until "Ask for my passphrase" is
  turned on or the passphrase changes (threat-model.md "Client device").
- No release key is built in yet (`RELEASE_PUBLIC_KEYS` in `updates.py` is
  `None`), so the update path has only been exercised with test keys.

## 5. How to run everything

```
make check     # format, generated code, lint, markers, lock, tests, fuzz, UI, smoke
make audit     # govulncheck, pip-audit
```

`docs/QA_GUIDE.md` starts a local relay, maintainer node, community
server and several clients for hands-on testing.
