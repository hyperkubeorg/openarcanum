# Settings

Every setting OpenArcanum has, where it is stored and its default. Tests keep
this page honest: `clients/desktop/tests/test_settings.py` checks that the
"User settings" table names every key in
`clients/desktop/src/openarcanum/settings.py` and no others, and
`internal/config/inventory_test.go` does the same for "Node settings" against
the relay, community server, maintainer node and website config structs. Add
or rename a setting and the row changes in the same commit.

A setting is finished when it has a control in the UI (or, for a node, a
config key and environment override), is saved, rejects bad input, starts at
the default listed here, takes effect without a restart where it can, and has
a test. Settings for features that don't exist yet are not shown.

There are three places settings live:

- **User settings** are kept by the desktop app in the OS app directories
  ([architecture.md §16.6](architecture.md#166-where-the-client-stores-data)),
  never in the webview. Settings the lock screens need, or that belong to one
  device only (appearance, data folder, staying signed in, update checks,
  maintainer nodes), are in `preferences.toml` in the config directory and
  can be read before unlock. Everything else is in the encrypted vault and
  is synced between linked devices.
- **Community settings** are stored on the community server. Only members
  with the right permission can change them, and every change goes into the
  audit log.
- **Node settings** come from a JSON config file, with an environment
  variable override for each key. [operations.md](operations.md) explains
  each one.

## User settings

| Area | Setting | Default |
|---|---|---|
| Security | Change passphrase | n/a |
| Security | Lock after idle (`auto_lock_minutes`, 0 = never, up to 1440) | Never |
| Security | Stay signed in on this device (`remember_device`; shown the other way round as "Ask for my passphrase when OpenArcanum starts" in Settings → Security, which asks for the passphrase to change it). Per device, in `preferences.toml`. The vault's key is kept in the OS keychain, or in `unlock-key` (mode 0600) in the config folder where there is no keychain, and Settings → Security then says anyone who can read the user's files can open the profile. Lock now and the idle lock delete the key until the passphrase is entered again | On for a profile created, restored or linked on this device; off for a `preferences.toml` without the key, until the user accepts the one-time offer after a passphrase unlock |
| Security | Export backup; backup status | n/a |
| Identity | Separate key per community, or reuse one identity (`default_identity`: the choice preselected when joining) | Separate key |
| Identity | Your name (`display_name`, up to 64 characters): shown in direct messages and contact codes; a community you join starts with it | None |
| Identity | Display name and avatar per community | None |
| Identity | Nickname, pronouns (up to 40 characters) and about text (up to 190) per community, stored on the community server (Your profile here) | None |
| Communities | Leave; delete local data | n/a |
| Devices | List devices (name, linked date, last seen); rename this device (`device_name`, set through Rename); remove; link a new device | The platform's name ("Linux device") |
| Appearance | Theme (`theme`: system, light, dark; also on the first-run, unlock, restore and link screens); text size (`text_size`: small, medium, large, larger); compact mode (`compact`). Kept in `preferences.toml`, readable before unlock | Dark; medium; off |
| Appearance | Keep running in the tray when the window is closed (`close_to_tray`, in `preferences.toml`, per device). Works only where the desktop has a system tray; without one the option is shown off and disabled with the reason, and closing the window quits (`openarcanum/tray.py`) | On |
| Notifications | Desktop notifications (`notifications`); per-community and per-channel mute (`muted_communities`, `muted_channels`); mentions only (`notify_mode`: all, mentions); per-community level (`community_notify`: all, mentions or nothing, set from the community icon's right-click menu or Settings → Notifications; a community not listed follows `notify_mode`); mentions suppressed per community (`suppress_mentions`: everyone for @everyone, @here and @channel, roles for role mentions, from the same menu: they neither notify nor count as mentions). Inside the window, new messages and reports show as badges only, never as pop-ups | On; none muted; all messages; every community follows the default |
| Notifications | Sounds (Settings → Notifications → Sounds): play sounds (`sounds`); volume (`sound_volume`, 0 to 100 %); sounds turned off one by one (`sounds_off`: new message, direct message, mention, someone joins or leaves your call, your mute, your deafen, call connected and disconnected). Synthesized in the page with Web Audio (`ui/js/sounds.js`) and played on the chosen speaker. They follow the mutes and notification levels above, not the desktop notifications switch; Do not disturb and deafen leave only the sounds of your own call actions | On; 50 %; none off |
| Network | Maintainer nodes (`maintainers`: https only, plain http only for this computer; up to 8). Per device, in `preferences.toml`, so they can be changed before unlock: Change… on the first-run, unlock, restore and link screens, and Settings → Network. Empty means the `--maintainer` launch option if given, else the built-in list | Built-in list |
| Network | An entry can name a maintainer cluster: `URL#KEY` pins the cluster's operator key and the app uses the members of its verified member list ([protocol.md §7.5](protocol.md)). Use the cluster's other nodes (`maintainer_cluster_nodes`, in `preferences.toml`) | On |
| Network | Preferred relays (`preferred_relays`, ws or wss; up to 8) | Automatic |
| Network | Search the directory on this device (`directory_local`): download the directory snapshot and search it locally, so maintainers don't see queries | Off |
| Data | Local cache size (`cache_limit_mb`, 64 MB to 100 GB); data folder location (`data_dir`, in `preferences.toml`; empty means the OS default); clear cache | 1 GB; OS default |
| Voice and video | Input and output devices (`voice_input`, `voice_output`; can be switched during a call, also from the voice bar's menus); talking mode (`voice_mode`: voice activity, push to talk, or studio, which sends any sound with no threshold and no processing, for people with their own gate and processing: silence or a muted microphone sends nothing); push-to-talk key (`ptt_key`); voice activity sensitivity (`voice_sensitivity`, -100 to 0 dB); output volume on this device (`voice_output_volume`, 0 to 200 %); camera (`video_input`); per-person volume (`voice_volumes`, from a participant's menu in a call); people muted on this device only (`voice_local_mutes`, "Mute for me" in a participant's menu); a screen share's sound (`screen_share_audio`); video sent (`camera_quality`: 480p, 720p or 1080p; `screen_quality`: text, sharp at 5 frames a second, balanced, or motion, 30 frames a second; also from the camera and screen menus during a call) | System devices; voice activity; Space; -40 dB; 100 %; system camera; 100 %; nobody muted; sound shared; 720p; balanced |
| Voice and video | Push to talk in other windows (`ptt_global`): the push-to-talk key also works while another app has focus, where the OS allows a global hotkey. Wayland gives apps no way to see keys pressed elsewhere, so it doesn't work there (`hotkey.py`) | On |
| Privacy | Who can DM me and add me to group DMs (`dm_policy`): members of shared communities, or contacts only | Contacts only |
| Privacy | Who sees my status, end-to-end encrypted ([protocol.md §4.11, §9.8](protocol.md)): contacts (`status_contacts`: off, the state, or the state and custom text; `status_contacts_scope`: all contacts, or only those in `status_contacts_chosen`) and each community (`status_communities`: off, the state, or the state and custom text; a community not listed gets the state). Settings → Privacy & DMs → Status, also reached from the status picker's "Who can see this" | Contacts see the state and custom text; every community sees the state only ([user-guide.md "Your status"](user-guide.md)) |
| Privacy | Publish a contact card, per community (members can then message you; it arrives as a request) | On |
| Privacy | Block list: blocked people can't DM you or add you to groups | Empty |
| Privacy | Contact verification: a safety number per contact, which you can mark verified | n/a |
| Status | Idle after no input on this device (`status_idle_minutes`, 0 = never, up to 240). The status itself (online, idle, do not disturb, invisible; custom text up to 128 characters and an emoji; clears after 30 minutes, 1 hour, 4 hours, today or never) is set from the user panel and synced between devices; it isn't a setting | 10 minutes |
| Direct messages | Group DM name, icon, "members can add people" and "allow attachments" (owner only); leave. Stored in the group's signed state (`groups.py`), not in the vault | Members can add: off; attachments: on |
| Updates | Check for updates automatically (`update_check`, in `preferences.toml`): at startup and every 24 hours, which shows the update host this computer's IP address. Check now; install only after confirmation (Restart and install, Later, Skip this version). See [release.md](release.md) | On |

## Community settings

Stored on the community server, changed from Community settings in the app.

| Area | Setting | Default |
|---|---|---|
| Overview | Name, description, icon, language | n/a |
| Overview | Tags | None |
| Visibility | Public (listed in the directory) or private | Private |
| Joining | Join policy: open, request, or invite-only | Invite-only |
| Joining | Maximum members (a full community refuses joins) | 0 (no limit) |
| Joining | Join keys: create, label, expiry, maximum uses, default role, revoke | One invite with a 7-day expiry |
| Joining | Invite list with creator, uses, maximum uses and expiry. Creating one needs Create Invite; members see and revoke their own invites, Manage Community sees and revokes all | Create Invite off for @everyone |
| Joining | Approval queue; raid revoke | n/a |
| Roles | Create, edit and order roles and their permissions | `@everyone`, `Admin` |
| Roles | Permission defaults for `@everyone`, chosen for privacy (for example Create Invite off; mentioning @everyone and @here for admins only) | See [admin-guide.md "Permission defaults"](admin-guide.md) |
| Roles | The full permission list in a grouped editor; members grant or take away only permissions they hold, and manage only roles below their highest | n/a |
| Roles | Colour, display separately (hoist), mentionable | No colour; off; off |
| Channels | Create, edit and delete text and voice channels | `#general` |
| Channels | Categories (create, rename, delete, order), channel order, topics, slowmode (0 seconds to 6 hours), category permission sync | None; slowmode off |
| Channels | Overrides per role and per member (allow, deny, inherit), on channels and categories | None |
| Channels | Per-channel attachments, link previews, reactions (inherit or off) and history for new members (inherit or off) | Inherit |
| Members | Assign roles; kick; ban; ban list; replace member | n/a |
| Members | Nicknames: your own with Change Nickname, other people's with Manage Nicknames; up to 32 characters; your own changes count toward the limit of 3 profile changes per member in any 6 seconds | None |
| Members | Maximum devices per member | 5 |
| Ownership | Several owners: any owner invites a member to become one, confirms the owner key their device sends, removes owners (never the last) and signs server keys. A warning shows in Ownership and above the chat while there is only one owner ([protocol.md §3.1, §4.5](protocol.md)) | The creator is the only owner |
| Ownership | Server key rotation by any owner; backup and export | n/a |
| Ownership | Move to new hardware, adopted by any owner | n/a |
| Features | Feature toggles: attachments (maximum size, kinds), link previews, reactions, custom emoji, voice, video, screen share, search, contact cards, edit own messages, delete own messages, typing indicators, new members see history, join and leave messages and the channel they go to. Community settings → Features. A feature off for the community is off everywhere; otherwise the channel setting, then permissions decide (`internal/community/features.go`) | All on except link previews; attachments of all kinds; join and leave messages in the first text channel |
| Moderation | Member screening: minimum membership age before posting, reacting and starting DMs (0 = off, up to 7 days; members with a role are exempt) | Off |
| Moderation | AutoMod rules (keywords and regular expressions, mention spam, links with an allowlist; block, alert or time out; exempt roles and channels), up to 20 rules | None |
| Moderation | Report queue (Manage Messages); timeouts (60 seconds to 28 days); deleting a banned member's recent messages (none, 1 hour, 6 hours, 24 hours, 3 days, 7 days); bulk delete (up to 100) | n/a |
| Moderation | Who can see the audit log: admins or everyone | Admins |
| Emoji | Custom emoji on or off (under Features) and how many (1 to 250, in the Emoji section); each image PNG, GIF or WebP, at most 256 KiB and 128×128 px | On; 50 |
| Retention | Message retention; file retention; per-channel overrides | 30 days; 7 days |
| Search | On or off (under Features) | On |
| Files | Maximum file size (up to 25 MB; also set in the setup wizard); bandwidth limits | 10 MB; none |
| Network | Relays: automatic or a pinned list | Automatic |
| DMs | Members may publish contact cards in this community (under Features) | On |

## Node settings

Each node reads a JSON config file and an environment variable per key
(`OPENARCANUM_RELAY_*`, `OPENARCANUM_COMMUNITY_*`, `OPENARCANUM_META_*`,
`OPENARCANUM_SITE_*`). Defaults are in brackets. [operations.md](operations.md)
describes each key, and the section named after a row has the details.

| Node | Settings |
|---|---|
| Relay | `listen` (:7000), `region` (none); sizes `max_chunk` (1 MiB), `max_message` (25 MiB); per-connection limits `frames_per_second` and `frame_burst` (1000 and 2000), `bytes_per_second` and `byte_burst` (16 and 32 MiB), `send_queue_bytes` (64 MiB), `max_subscriptions` (4096), `max_streams` (1024), `max_partial` (64), `max_connections` (10 000), `write_timeout` (15s), `ping_interval` (20s); relay registry `registry_urls` (none), `public_url` (required with `registry_urls`), `registry_key_file` (a fresh key each run), `heartbeat_interval` (30s). operations.md "relay" |
| Relay | Calls (the keyless SFU, protocol.md §12): `calls` (on), `media_udp_port` (7000), `media_listen_ip` (all addresses), `media_public_ips` (none), `media_loopback` (off), `max_calls` (1000), `max_call_peers` (5000), `max_call_peers_per_ip` (20 per IPv4 address or IPv6 /64), `call_peer_bitrate` (8 Mbit/s; at least 64 kbit/s), `call_servers` (any community server). operations.md "relay" |
| Relay | Proof-of-work: `pow_mode` (load), `pow_bits` (18), `pow_load_connections` (three quarters of `max_connections`), `pow_new_per_second` (200), `start_window` (30s); connections that haven't started: `max_unstarted` (a tenth of `max_connections`), `max_unstarted_per_ip` (128 per IPv4 address or IPv6 /64), `start_window_under_load` (10s); per network (IPv4 address or IPv6 /64): `max_connections_per_ip` (256), `max_subscriptions_per_ip` (65 536), `pow_ip_connections` (32: further connections from one network solve the proof-of-work); `max_subscriptions_total` (2 000 000); fan-out tokens: `fanout_threshold` (100 subscribers); chunk cache: `chunk_cache_bytes` (0 = off), `chunk_cache_ttl` (1m), `chunk_cache_topic_bytes` (4 MiB), `chunk_cache_max_message` (256 KiB), `chunk_cache_conn_bytes` (a quarter of the cache); connections that host a community server: `host_frames_per_stream` (50), `host_bytes_per_stream` (256 KiB/s), `host_frames_per_second` (50 000), `host_frame_burst` (100 000), `host_bytes_per_second` (128 MiB/s), `host_byte_burst` (256 MiB). operations.md "Abuse controls" |
| Relay | Dedicated relay (one community server; network blocks): `dedicated_server_key` (none; 64 hex characters) or `dedicated_server_key_file` (none; read again every 10 s), `block_file` (required when dedicated), `max_blocked_networks` (10 000). operations.md "Dedicated relays" |
| Relay | Maintainer clusters: `registry_urls` entries may be `URL#OPERATORKEY`; `maintainer_operator_key` (none) applies to entries without a key. operations.md "Maintainer clusters" |
| Relay | TLS for browser clients: `tls_mode` (off; acme or files), `tls_domains` (none; names or IP addresses, IP addresses under the CA's short-lived profile), `tls_cert_file` and `tls_key_file` (files mode), `acme_cache_dir` (required with acme), `acme_email` (none), `acme_directory_url` (Let's Encrypt); with TLS on, `public_url` defaults to `wss://<first of tls_domains>/ws`. operations.md "TLS and browsers" |
| Community server | `data_dir` (the `-data` directory), `storage_key_file` (`DATA/storage.key`), `claim_file` (none; written by `init -claim-file`, deleted once claimed), `server_public_key_file` (none; the server writes its public key there at start and after key rotations, for a dedicated relay to read), `maintainers` (none), relays `relay_mode` (pinned, or automatic), `relays` (none), `relay_count` (2); backups `backup_dir` (`DATA/backups`), `backup_interval` (24h; 0 = off), `backup_keep` (7); jobs `purge_interval` (1h), `publish_interval` (10m). operations.md "community" |
| Community server | Maintainer clusters: `maintainers` entries may be `URL#OPERATORKEY`; `maintainer_operator_key` (none) applies to entries without a key. The server publishes to every member of each cluster's verified member list. operations.md "Maintainer clusters" |
| Community server | Status reports to maintainer nodes: `heartbeat_interval` (1m; 1s to 10m). operations.md "Status and reaping". In automatic relay mode a relay that fails is replaced at once; there is no setting for it ("Relay failover") |
| Community server | Upload quotas: `upload_quota_per_member` (1 GiB per rolling 24 hours), `file_store_max` (20 GiB); sessions open at once `max_sessions_per_device` (4), `max_sessions_per_member` (20). operations.md "Quotas" |
| Community server | Communities this server runs at most: `max_communities` (10; owners start more from the app). operations.md "Several communities on one server" |
| Community server | Link-preview fetcher: `link_previews_disabled` (false), `preview_timeout` (5s), `preview_max_bytes` (1 MiB), `preview_image_max_bytes` (256 KiB), `preview_schemes` (https). operations.md "Link previews" |
| Community server | Storage-key rotation has no setting of its own: content keys cover one UTC calendar month and are deleted by the purge job (`purge_interval`) once the community's retention has passed. operations.md "Data at rest" |
| Maintainer node | `listen` (:7100), `data_dir` (required); the public directory `directory_enabled` (off: joining relies on invites shared elsewhere; `meta directory disable` switches a running node's directory off and `meta directory enable` lifts that); replication `peers` (none), `replication_interval` (30s); rate limits `listings_per_hour` (60 per community), `writes_per_minute` (600 per IPv4 address or IPv6 /64), `reads_per_minute` (1200 per IPv4 address or IPv6 /64, every GET but /healthz); delisting `delisted`, `delisted_relays` (none; also managed at run time with `meta directory` and `meta relays`, kept in the node's database); relay registry `relay_ttl` (2m). operations.md "meta (maintainer node)" |
| Maintainer node | Relay registry checks: `allow_private_relays` (false; for development only: accept relay URLs on private and loopback addresses and local names), `relay_proof` (true: list a relay URL only once it serves the heartbeat's key; false only with `allow_private_relays`). operations.md "Relay registry", protocol.md §7.3 |
| Maintainer node | DM mailboxes: `mailbox_ttl` (14 days), `mailbox_quota` (100 MiB per inbox), `mailbox_drops_per_minute` (120 per inbox); storage bounds `mailbox_store_max` (10 GiB for all mailboxes; 507 when full), `mailbox_bytes_per_ip` (1 GiB per rolling 24 hours per IPv4 address or IPv6 /64), `mailbox_max_item` (2 MiB). operations.md "Mailbox storage" |
| Maintainer node | DM blob store: `blob_ttl` (72h), `blob_max_size` (26 MiB), per-sender quota `blob_quota_per_ip` (250 MiB per rolling 24 hours per IPv4 address or IPv6 /64), per-network limits `blob_uploads_per_ip` (500) and `blob_tokens_per_ip` (10 000) per rolling 24 hours, `blob_store_max` (10 GiB), `blob_uploads_per_minute` (30) |
| Maintainer node | Device-link nameplates: `nameplate_ttl` (5m; 5s to 1h), `nameplates_per_minute` (10 per IPv4 address or IPv6 /64) |
| Maintainer node | Maintainer clusters (protocol.md §7.5): `cluster_operator_key` (none: not in a cluster), `maintainer_set_file` (none; also `meta set publish`), `node_key_dir` (`data_dir`), `trusted_peers` (none; `URL#NODEKEY`, also `meta trust add`), `trusted_interval` (5s); `follow_decisions` (none: moderation decisions of these members are applied too); per-peer intake limits `peer_bytes_per_day` (2 GiB), `peer_items_per_minute` (5000), `peer_tombstones_per_minute` (5000), background blob copy `peer_blob_bytes_per_hour` (2 GiB). operations.md "Maintainer clusters" |
| Maintainer node | Community status and reaping: `community_offline_after` (3m: no heartbeat for this long shows the community offline), `community_reap_after` (7 days: no signed report for this long ends it, dropping its route, listing and status), `community_tombstone_ttl` (30 days: how long clients are told it was shut down; coming back needs an explicit re-registration). operations.md "Community status and reaping", protocol.md §7.4 |
| Maintainer node | TLS for browser clients: `tls_mode` (off; acme or files), `tls_domains` (none), `tls_cert_file` and `tls_key_file` (files mode), `acme_cache_dir` (`DATA/acme`), `acme_email` (none), `acme_directory_url` (Let's Encrypt); CORS `cors_origins` (`["*"]`; listed origins only, or `[]` for off). operations.md "TLS and browsers" |
| Website | The project website (`cmd/site`): `listen` (:7300), `http_listen` (none; redirects to https, needs TLS), `data_dir` (required, mode 0700), `storage_key_file` (`DATA/storage.key`), `access_log` (stderr; off, or a file that is rotated), `log_client_ips` (false: clients are logged as "-"), `trusted_proxy_header` (none; used for rate limits only), report limits `reports_per_hour` (5 per IPv4 address or IPv6 /64), `reports_per_day` (500, for everyone together), `report_pow_bits` (18), landing-page links `download_url` and `source_url` (none: no links shown), the web client `webclient_dir` (none; the site image sets it: served at /app/), admin sessions `session_ttl` (12h), `session_idle` (1h). operations.md "Website" |
| Website | TLS: `tls_mode` (off; acme or files), `tls_domains` (required with acme and files; also the only host names answered, the first being the site's address that the others redirect to), `tls_cert_file` and `tls_key_file` (files mode), `acme_cache_dir` (`DATA/acme`), `acme_email` (none), `acme_directory_url` (Let's Encrypt); HSTS when TLS is on. operations.md "Website" |
| All nodes | Logs and the status listener (`internal/ops`): `log_format` (text, or json), `log_level` (info; debug, also `-v`), `log_file` (stderr), `log_max_size` (50 MiB, at least 1 MiB), `log_max_age` (24h; 0 = by size only), `log_keep` (7), `metrics_listen` (off; serves `/healthz`, `/readyz` and `/metrics`). operations.md "Logs, health and metrics" |

`community init` also takes `-install home|vps` (advice only) and
`-metrics-listen`, or asks for everything with `-interactive`.
