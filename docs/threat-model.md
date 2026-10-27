# Threat model

This extends `docs/architecture.md` §2 (what the system protects and what it
accepts by design) with one section per component: the asset, the adversary,
the controls and what they stop, and the residual risks we accept. A change
that adds a risk adds it here in the same change. The cryptography behind
each control is listed in `docs/crypto-inventory.md`.

**Trust zones** (`docs/architecture.md` §1): a community (its members and its server,
which can read everything in the community); a DM conversation (only its
participants); infrastructure (relays and maintainer nodes: no keys, only
ciphertext and connection metadata).

## Client device

**Asset.** The vault (all identity, owner and DM identity seeds, community
records, settings), Olm/Megolm state, DM history, the message and file cache,
logs.

**Adversary.** Someone with the device's disk (lost or stolen laptop,
backups of the home directory); malware running as the user.

| Control | What it stops |
|---|---|
| Vault sealed with scrypt (N=2^17) + ChaCha20-Poly1305; every profile has a passphrase; increasing backoff after wrong attempts | Reading keys from a copied disk without the passphrase; online guessing in the app |
| `state.bin`, cache, `dms.bin`, `olm.bin` sealed with keys kept only in the vault; atomic writes; `0600`/`0700` on POSIX | Reading history or DM keys from disk; torn writes |
| Lock clears identity and DM identity seeds from memory | A locked, unattended session being used to sign as the user |
| Logs never contain message content, keys, join keys or passphrases (tests check log output) | Leaks through logs and bug reports |
| **Generate** (new passphrases): six words from the vendored EFF large wordlist (digest-checked) drawn with Python's `secrets`, ≈ 77.5 bits; generated in Python, never in the page; nothing keeps it | Weak or reused passphrases chosen by people, which an offline guesser with a copied vault or backup file would find |
| Settings → Data & storage shows paths only; **Open folder** takes the id of one of the app's own locations and Python resolves it, so the page can't ask the OS to open an arbitrary path | Script in the page opening files or folders through the bridge |

**Residual risks.** Malware running as the user while the profile is
unlocked can read everything the client can (no OS can stop that). A
generated passphrase is shown in the clear until the user hides it, and
**Copy passphrase** puts it on the system clipboard, where clipboard
history tools and other apps can read it; the user is told to move it to a
password manager, which is the point of generating it. Lock keeps
the vault file key and device keys in memory so sessions keep running and
state that arrives can be saved; a memory dump of a locked
client still yields those.

**Staying signed in** (`keychain.py`; `docs/architecture.md` §16.1). A
profile created, restored or linked on a device stays signed in there by
default: the device keeps the vault's file key (never the passphrase) so
the next start opens without the passphrase. This is a deliberate trade of
the passphrase's protection *at rest on this device* for convenience: the
passphrase then guards backups, other devices and copies of the data folder,
not this computer. Where the key is kept decides who can use it:

- **The OS keychain** (Windows Credential Manager, macOS Keychain, Secret
  Service: GNOME Keyring, KWallet) where there is one. Only as strong as
  the OS account: anyone who can use that account, or malware running as
  it, can open the profile without the passphrase.
- **A key file** where there is no keychain (minimal Linux desktops,
  containers): `unlock-key` in the settings folder, mode 0600 in a 0700
  folder, as `{"v": 1, "key": <base64>}` with no further wrapping (there is
  no secret on the device to wrap it with that the attacker wouldn't also
  have). Anyone who can read the user's files (root, another administrator,
  a backup of the home folder, malware running as the user) can open the
  profile. Settings → Security says so in those words while it is in use. A
  file whose permissions were widened, or that another account owns, is
  refused and deleted. It is in the settings folder, not the data folder,
  so a copy of the data folder (synced, backed up or moved) stays locked
  with the passphrase.

What limits it: **Lock now** and the idle lock delete the kept key, so a
locked profile needs the passphrase to come back, also after a restart
(the passphrase keeps the key again); **Ask for my passphrase when OpenArcanum
starts** (Settings → Security; needs the passphrase) deletes it and re-keys
the vault file (same passphrase, new scrypt salt), so a copy of the key
taken earlier (by malware that read the keychain or the file, or from a
backup of either) no longer opens any later version of `vault.bin`; it can
still open copies of the file made while it was valid, so whatever those
held (identity seeds, the state key) must be treated as exposed if such a
copy may exist. Locking does not re-key, so a key copied before a lock
still opens the current file until the option is turned on or the
passphrase changes. Changing the passphrase re-keys the file and replaces
the kept key; Start over and a removed device delete it; a stale key is
refused and deleted, and the passphrase always works. Backups (`.fbak`)
and device linking never include it. Profiles from before this default
keep asking for the passphrase until the user accepts a one-time offer
after unlocking.

`preferences.toml` (config folder) is not encrypted, because the screens
before unlock need it: appearance, the data folder, whether this device
stays signed in (and whether the key is in the keychain or the key file),
the update-check switch, and this device's **maintainer node list**
(per device, not synced). Someone who can read the profile folders learns
which maintainer nodes this device uses (a self-hosted or test network
says something about its owner), though not what the user does with them.
Someone who can write there (malware running as the user) can point the
device at maintainer nodes they run. Those nodes still can't read or forge
anything: directory listings and routes are signed by community owners, DMs
and DM files are end-to-end encrypted, and link codes are protected by
SPAKE2. They can see the IP address, searches (unless the directory is
searched on the device) and mailbox timing, and withhold service. The lock
screens and Settings → Network show the list in use and where it comes from,
so such a change is visible. Such malware could do far more anyway (see
above).

**The tray icon** (`openarcanum/tray.py`). Closing the window leaves an unlocked
profile running in the system tray (on by default where a tray exists;
Settings → Appearance), so a user who thinks they quit may leave it open:
the first close says how to quit, auto-lock keeps running while the window
is hidden, and **Lock** is in the tray menu. The tray's tooltip shows counts
only (unread mentions, unread direct messages, items waiting for review),
never names or text, and nothing while locked; anyone who can see the
screen can see those counts, as with the window's badges. The counts come
from the page through one bridge call (`set_attention`), clamped to
non-negative integers; a page that lied could only change the dot and the
numbers. The tray menu's actions run the same core calls as the window
(lock, the Desktop notifications setting, quit through the normal shutdown).

## Webview (UI)

**Asset.** The keys above; the user's messages as displayed.

**Adversary.** Message content crafted to run script in the UI (a malicious
member, contact or server); a compromised or malicious remote resource.

| Control | What it stops |
|---|---|
| The UI never parses message content as HTML: text goes in through `textContent` only (`dom.js`) | Script injection from messages, names, file names |
| File names from others (DM and community attachments) are reduced in Python to one safe component before the page shows them or a save dialog suggests them: no path, no bidi or other format characters, no reserved Windows device names (`names.safe_name`, `test_safe_names.py`) | A name that writes outside the chosen folder or preselects another folder in the save dialog; `invoice\u202Efdp.exe` shown as `invoiceexe.pdf`; `CON .txt` that Windows can't save or opens a device |
| CSP: `default-src 'none'`; scripts only from the app's own files; no inline script; `connect-src 'none'`, `frame-src 'none'`; images only from the app or `data:` | Loading or exfiltrating to anything remote, even if script got in |
| No remote content is ever loaded: avatars, thumbnails and previews come from Python as `data:` URIs | IP leaks to linked sites, tracking pixels, remote exploits |
| Keys stay in Python: identity, vault, DM, channel and file keys and fetch tokens never cross the bridge (`test_key_boundary.py`). Only the per-call media keys (the newest few epochs) enter the page, for SFrame | Script in the page stealing long-term keys |
| The bridge exposes only `JsApi`'s own public methods: a guard replaces pywebview's name lookup (which follows dotted names through any attribute, private ones included) and refuses every other name, dotted names, names with a leading underscore and pywebview's internal calls before any attribute is looked up (`api.install_bridge_guard`, `test_bridge_security.py`) | Script in the page reaching Python objects behind the API (`_core.vault.data…`: the keys) |
| Paths come only from Python's own file dialogs: every bridge method that reads or writes a file (attachments, saving files, avatars and icons, emoji, backups, the community server copy, the data folder) accepts only a path the user chose in a native dialog this session (open, save and folder kept apart); a path typed in the page opens the dialog on it for the user to confirm (`api.PickedPaths`, `test_bridge_privilege.py`) | Script in the page reading any file (keys, documents) into a message, or writing anywhere (startup files, over the profile) |
| Privileged actions take the passphrase again, checked in Python against the profile file: exporting a backup, linking a device (before the code is even looked up) and removing one, changing the passphrase, and staying signed in or not. Every check counts toward the unlock backoff (`Core.confirm_passphrase`, `test_bridge_privilege.py`) | Script in the page exporting the keys under a passphrase it chose, linking a device of its own, removing the user's devices, or guessing the passphrase through these checks |
| The window never leaves the app's own page: every navigation (main frame or frame: links, script, forms, a dropped file or link) except to the app's own `file://` index is refused, the page can't open windows or the system browser (links open only through the app's link dialog, which copies them), the bridge is injected only into the app's own page, drag-and-drop is cancelled in the page and refused at Qt before the engine sees it, and downloads are off, every `file://` request outside the UI's folder is refused by the request interceptor (the page's own scripts, styles and images still load; `fetch` is already refused by `connect-src 'none'`), and WebRTC offers only the default route's address (`WebRTCPublicInterfacesOnly`) (`app.harden_webview`, `test_ui_webview_hardening.py`) | A page that isn't the app's getting the bridge; script or a drop moving the window to a local or remote page; links leaving the app without the user seeing where they go; script reading other local files; calls revealing every LAN or VPN interface address to the relay |
| Developer tools (Ctrl+Shift+I, the bridge's `open_devtools`) open only when the app was started with `--debug` or `OPENARCANUM_DEVTOOLS=1` | Script in the page opening DevTools; a user being talked into pasting code into a console (self-XSS) |
| Media capture: the app's own policy grants the microphone, camera and screen capture only to the app's own page and only while the app is in a call or testing devices in Settings; everything else (location, notifications, clipboard, …) is refused. Screens and windows are chosen in the app's own picker. pywebview's `--use-fake-ui-for-media-stream` (which grants all of this silently) is removed at startup | Capture the user didn't start (the app's own code only asks in calls and device tests), and any other page or origin asking. It is not a boundary against script already running in the page: that script can call the same bridge methods the UI does (as it can for everything else), which is why no script but the app's own may run there (rows above) |
| Chromium's renderer sandbox stays on: pywebview's `--no-sandbox` (added on some Linux distributions) is removed. Where Linux can't run the sandbox (unprivileged user namespaces off, or restricted by AppArmor without the app's profile), the app runs without it and logs that | Escaping the renderer after an engine exploit |
| Engine debugging stays off unless the app is started with `--debug` or `--allow-remote-debugging`: `QTWEBENGINE_REMOTE_DEBUGGING` and the flags `--remote-debugging-*`, `--remote-allow-origins`, `--disable-web-security`, `--allow-file-access-from-files`, `--allow-universal-access-from-files` and `--user-data-dir` in `QTWEBENGINE_CHROMIUM_FLAGS` are dropped at startup (logged by name) | A DevTools port inherited from the environment (a developer's shell profile, a launcher), through which any local program or user could attach to the unlocked page and drive the bridge; origin rules being lifted for the page. The UI tests start the app with `--allow-remote-debugging` to drive it |
| No webview storage at all (storage guard in `make check`); `private_mode=True` | Keys or history ending up in Chromium's profile |

**`'unsafe-eval'`.** The CSP has to allow it because pywebview's injected
bridge script (`webview/js/api.js`) creates each method of
`window.pywebview.api` with `new Function(params, body)`, which CSP treats as
eval; without it the bridge never loads and the UI can't talk to Python.
Inline scripts stay blocked, so eval is only reachable by code already running
in the page. Combined with the textContent-only rule, there is no path from
message content to script.

**Media permissions are per page lifetime.** QtWebEngine keeps a granted
(or refused) capture permission for the rest of the page's life and offers
no way to revoke it (verified: a second request is answered from the first
decision, whatever the profile's permission policy). So the app's policy
decides the page's *first* capture; after a first call the page could
capture again without being asked. Only the app's own code runs in the page
and it captures only in calls and device tests; the operating system's
microphone/camera indicators show any capture.

**WebRTC from the page.** Calls make the page open WebRTC connections (UDP)
to the call's relay, which the CSP doesn't govern. The relay URL comes from
the community server over the encrypted session; the page itself has no
network access otherwise (`connect-src 'none'`). ICE host candidates carry
only the default route's address (`WebRTCPublicInterfacesOnly`), not every
interface's.

## Relays

**Asset.** Packets in flight (channel events, DM envelopes, call media, link
and sync traffic), and the connection metadata.

**Adversary.** A relay operator; anyone on the network.

| Control | What it stops |
|---|---|
| Everything a relay carries is encrypted end to end (Noise sessions, channel AEAD + server signature, Olm/Megolm, SFrame, age-encrypted file chunks) | Reading or forging content |
| Endpoint registration is signed over a per-connection nonce | Hijacking a community's endpoint by knowing its route |
| Per-connection rate, bandwidth, subscription and stream limits; slow subscribers are dropped | One client exhausting the relay |
| Topics are 32 random bytes handed out over encrypted channels | Subscribing to a channel or DM without being told the topic |
| Proof-of-work (Hashcash with SHA-256, bound to the connection's random Hello nonce, accepted only within the start window) for new connections while the relay is under load | Cheap floods of connections that start; replaying one solution on many connections |
| Unstarted connections (accepted, no Start yet, so no proof-of-work done) hold separate slots: at most `max_unstarted_per_ip` per IPv4 address or IPv6 /64 (more refused at once) and `max_unstarted` in all, past which a newcomer takes the oldest unstarted connection's slot; the start window shrinks under load; an unstarted connection allocates about 9 KiB and runs no pinger | A flood of connections that never send Start (and so never pay the puzzle) filling `max_connections` or memory and locking real clients out |
| Chunk cache entries are charged their memory overhead (empty messages aren't cached), bounded per topic and per publishing connection, evicted in constant time; a connection's unfinished cacheable messages hold at most its cache share and are dropped after `chunk_cache_ttl` or when the message is refused | Tiny cacheable messages growing relay memory past `chunk_cache_bytes`; one publisher pushing out everyone else's cached messages |
| Clients reassemble a message's chunks under the topic its first chunk named (a chunk on another topic drops the message for good) and bound partly received messages: at most 1024, 64 per sender, 64 MiB in all (oldest dropped first), never more than 25 MiB per message whatever the relay announces | A relay moving a message's final chunk to another topic; a relay or publisher growing a client's or community server's memory with chunks that never finish |
| Fan-out tokens: a publish to a topic with more than `fanout_threshold` subscribers needs a short-lived Ed25519 token (`openarcanum/fanout-token/v2`) bound to the relay (key, URL) and the connection, from the key the topic commits to; the check runs on a message's first chunk and every later chunk must name the same topic | A member (or a removed member who still knows a topic) using the relay to amplify traffic to a whole channel |
| Relay identity: every Hello carries the relay's key, its URL and a signature over them and the connection nonce; endpoint registrations, fan-out tokens and moderation commands are signed over the relay key, the URL the server dialled and the nonce (`openarcanum/relay-endpoint/v2`, protocol §2 "Relay identity"); a relay accepts only bindings to itself; clients refuse a relay announcing another URL, a key other than a pinned one, and a URL-less relay's key at a second URL | A malicious relay that a community server attaches to presenting another relay's nonce, getting the server's endpoint registration and fan-out token signed, and replaying them on the other relay to receive members' sessions there (silent endpoint hijack). Residual: a relay without `public_url` checks no URL, so a server that has never talked to it can be tricked into a registration for it through another relay; public relays should set `public_url` |
| Endpoint hosts get a per-stream budget with a hard cap; resizing the budget as streams come and go never mints tokens | A community server stalling all its members on the per-connection limit, without letting one host take the relay |
| Version negotiation: the relay refuses every frame before Start; unknown or version-0 peers are refused | Old or foreign clients driving code paths the relay doesn't speak |

**Origin checks.** Relays accept WebSocket connections from any Origin
(`InsecureSkipVerify` in `internal/relay/relay.go`). Their clients are
desktop apps and servers, not web pages, and a relay holds no state or
credentials a cross-site page could abuse: anything a web page could do
through a visitor's browser, any program can do directly. Accepted.

**Chunk cache (optional, off by default).** A relay operator can keep
recent cacheable messages in memory for a configurable time so reconnecting
members catch up. Only messages the publisher marked cacheable are kept,
and community servers mark only channel envelopes, which are signed and
encrypted with the channel key; Noise streams and call media are never
cached, and nothing goes to disk. A replay goes only to a subscriber of the
topic (who could have received it live). Clients ask for one only when
re-subscribing after a reconnect, but that is client behaviour; the server
enforces history visibility: when a member is admitted to a channel whose
history is hidden from them ("New members see history" off for the
community or channel, no Manage Messages), and anything was published there
within the longest cache lifetime (1 h), the channel gets a new key, epoch
and topic before the member learns them (docs/protocol.md §6.2), so a
modified client can neither find nor decrypt envelopes from before it
joined. A member whose role denies Read Message History still holds the
key of a channel they can view (they receive its live events), so such a
client can replay up to `chunk_cache_ttl` of recent events after a
reconnect: events it would have received live had it stayed connected.
Accepted; operators who don't want even that leave the cache off. New risk,
accepted: while a message is cached
it can be delivered to a removed member whose client still knows the old
topic and key for up to `chunk_cache_ttl` after the removal; channel
rotation on removal (new topic, key and epoch) means everything published
after the removal goes to the new topic, so the exposure is limited to
events the member could already read before removal. Operators who don't
want this leave the cache off.

**Proof-of-work limits.** The puzzle costs an attacker CPU, not money; a
botnet pays it easily. It bounds a single machine's connection rate and
buys time; `max_connections`, the start window and per-connection limits
remain the hard bounds. Slow devices pay more (about 2^18 hashes by
default, well under a second on a laptop; `pow_bits` is capped at 24).

**Fan-out token limits.** A topic commits to a server key with a 128-bit
truncated SHA-256, and the topic's other 16 bytes are random, so topics
stay unguessable (128 bits) and binding them to a key is a second-preimage
problem. Small topics (group DMs, device sync) need no token, so a
participant can still flood a small topic up to its own rate limits.

**Residual risks.** A relay sees IP addresses, timing, sizes and which topics
a connection uses, and can drop or delay traffic (`docs/architecture.md` §10). In
calls it sees which participants send which kinds of media and the sizes
of video frames, but not who is speaking: voice carries no audio level and
is sent at a constant bitrate, one packet size whether speaking, silent or
muted ("Calls" below). Clients
fail over to other relays. The relay's Hello proves only the
relay's own key (nothing certifies it), so its settings (versions, PoW
difficulty) are the relay's word: a malicious relay can claim to need a
newer version or a hard puzzle, which only denies its own service (clients
cap the difficulty at 24 bits and use other relays).

## Dedicated relays and network blocks

An opt-in arrangement (docs/operations.md "Dedicated relays"; protocol §2
"Dedicated relays", §4.7 "Network blocks"): a relay serves one community
server only, and blocks the networks of members the community bans.

**Asset.** Members' IP addresses (which nobody on the community side should
learn); the ability to block a network (which only the community's
moderators should have); the community's availability.

**Adversary.** A member or outsider trying to get someone else blocked or
to learn an address; a banned member coming back; the relay operator; a
moderator abusing the feature; a stolen relay disk.

| Control | What it stops |
|---|---|
| The server names streams, never addresses; the relay maps them itself and returns an opaque random block id; audit entries carry the id | The community server, its operator, moderators and the audit log learning members' IP addresses |
| Commands are Ed25519-signed by the dedicated key under `openarcanum/relay-moderation/v2` (no other signed message uses it), carry the relay's key and URL and the relay connection's Hello nonce, expire within 5 minutes and are accepted once per id; only the connection hosting the dedicated key's endpoint may send them; non-dedicated relays refuse them | Forged, replayed, cross-relay or expired commands; another community (or a member who knows a stream id) blocking anyone; a captured command reused later |
| Stream ids are looked up among the sending connection's own inbound streams | Naming streams of another server's (or no) connection |
| The server enforces Ban Members and the documented durations before anything is sent, records the request sealed (`net_blocks`), and audits every block and lift | A member without Ban Members blocking someone through a modified client; silent blocks |
| A relay never blocks the network of the community server's own connection | A moderator (or a member on the server's LAN) taking the community offline by banning someone who shares its address |
| A dedicated relay hosts one endpoint, only topics committed to its key and only its calls, and doesn't register with maintainer nodes | The relay being used by others, and others' traffic being subject to this community's blocks |
| The block list is written atomically, mode 0600, read at start; an unreadable list stops the relay rather than dropping blocks; bounded by `max_blocked_networks` | Blocks silently lost on restart; a flood of bans growing the file without bound |
| Relays never log addresses (blocks are logged by id and counts) | Addresses leaking into logs |

**Residual risks, stated plainly.**

- **Collateral blocks.** A network is an IPv4 address or an IPv6 /64. Under
  carrier-grade NAT, on mobile networks, in offices, schools and shared
  housing, and at VPN and Tor exits, many people share one; all of them
  are blocked with the banned member. The ban dialog says so. Accepted:
  moderators choose a duration (1, 7 or 30 days, or until unbanned) and can
  lift a block by unbanning.
- **Easy to evade.** A VPN, Tor, mobile data, or a new address from the ISP
  gets around it. It raises the cost of returning; the ban on the member's
  key, invites and the approval queue remain the real controls.
- **Only this relay.** It covers connections through the dedicated relay,
  which is why the option exists only when every pinned relay is
  dedicated. A member who isn't connected at ban time is blocked only when
  they next connect through it (their banned session names their stream).
- **The relay operator holds addresses.** As with every relay, the operator
  sees addresses; a dedicated relay also keeps blocked ones on disk in
  `block_file`, linked to an opaque id the community holds. A community
  that runs its own dedicated relay therefore can, as operator, link a
  block id to an address by reading the file: the separation is between
  roles (moderators, the community server) and the relay host, and is
  only as strong as who controls that host. Documented in operations.md.
- **Address-based identification.** A blocked network is personal data
  where privacy law applies; the operator owns retention (the file is
  plain JSON, deleted with the relay's volume).
- **Proxies.** A relay behind a proxy that hides clients' addresses would
  block everyone at once; it never trusts forwarded-for headers, and the
  docs require it to see clients' own addresses.
- **Availability.** The community depends on one relay operator (pinned
  only); if that relay is down the community is unreachable until it
  returns or the owners re-pin. A key rotation needs the relay's key
  updated (automatic with the key file arrangement).
- **Fewer interchangeable routers.** The relay is no longer anonymous
  infrastructure for this community: its URL in the route identifies it as
  the community's. Accepted for an opt-in moderation feature.

## Maintainer nodes: directory, routes, mailboxes, nameplates

**Asset.** Public listings, private-community routes, the relay registry,
DM mailboxes (ciphertext), link nameplates.

**Adversary.** A maintainer operator; a network attacker; spammers.

| Control | What it stops |
|---|---|
| Listings and routes are signed by a server key certified by the community's owner set; clients verify them themselves | A maintainer altering listings or redirecting a community |
| Mailbox inbox addresses are SHA-256 of a secret token; only the token holder fetches or deletes; inboxes are per conversation and per device | Reading or deleting someone else's mail; linking conversations by address |
| Rate limits per network (IPv4 address or IPv6 /64) and per inbox; quotas per inbox, per network per day (`mailbox_bytes_per_ip`) and for all mailboxes together (`mailbox_store_max`, 507 when full) | Flooding inboxes (first-contact inboxes are the spam surface); one client filling the node's disk by dropping into fresh inboxes, or escaping per-IP limits by rotating addresses within its /64 |
| Limit tables are bounded (100 000 keys each), drop idle keys in constant time, and evict the least recently used key when full instead of scanning or refusing; limits keyed by client address are also capped per IPv6 /48 (16 networks' worth) | Many distinct addresses turning every request into a scan under the limiter lock, or filling the tables so every newcomer is refused; one site's 65 536 /64s each getting a full budget |
| Nameplates are single-use, short-lived, random six-digit numbers, and only point at a relay topic; claims are rate limited per network; the link itself is SPAKE2-protected | A maintainer joining or watching a device link; a third party claiming a live code before the new device (link DoS) by guessing the next number |
| Listings, routes and heartbeats are stored and passed on re-encoded with known fields only, within size bounds (640 KiB, 256 KiB, 2 KiB) and per-endpoint body limits; listing strings are bounded; replication batches end before 4 MiB and skip oversized entries | One post storing megabytes that every search, snapshot and replication repeats; a burst of padded records making a peer's batch unparseable so its replication stops for good |
| A reaped route id stays bound to its community in the tombstone; clients verify a route against the community they expect and ask the next node when it doesn't verify | Another community taking over a reaped community's route id (and the owners' re-registration then being refused); one lying node ending a client's route lookup |
| The relay registry lists a URL only after a plain GET of it returned the heartbeat's key (proof of control; the fetch: no redirects or proxy, public addresses only, 5 s, 256 bytes, rate limited; stalling relay URLs can't evict proven relays: proven URLs are accepted on their last proof while the check pool is busy, and unproven checks are 10 per minute per network); relay URLs on private, loopback or link-local addresses and local-only names are refused (`allow_private_relays` for development only); claimed capacity and connections are capped | Anyone registering another relay's URL under their key (409 for the real relay) or steering community servers to fake relays; the registry making community servers, or the node itself, dial into their own networks (blind SSRF) |
| The API server has read, write and idle timeouts, and net/http's own error log goes to the node's log at debug level with IP addresses removed | Slow-request connection exhaustion; client addresses ending up in logs through TLS handshake errors |
| A mail tombstone binds only the inbox whose token made it; tombstones for mail the node doesn't hold are limited per network | Someone who learned an item's gid deleting it in advance with their own token; one client filling the node and its cluster with tombstones for random gids |
| A route, listing or heartbeat without a publish time is stale | An old undated signed copy being replayed indefinitely |
| Directory snapshots carry each community's signed listing, verified one by one on the client; bounded in size on both sides; local search keeps queries on the device | A maintainer altering listings in a snapshot; learning what a user searches for (with local search on); a node exhausting a client's memory |
| A listing signs its icon's SHA-256; the icon must match | Swapping a community's icon |
| API version negotiation (`OpenArcanum-Protocol` header, TLS-protected); 426 when no version is in common | Silent misinterpretation between mismatched builds |
| Admin commands (`meta directory`, `meta relays`) act on the node's own database; decisions are checked inside the write transactions | A delisted community or removed relay coming back through replication or a race with a running node |
| Relay URLs are compared in canonical form everywhere (`internal/relayurl`); a removal covers the relay's whole host | A removed relay re-registering as `wss://RELAY.example:443/ws/`, another port or another path |
| Admin commands refuse a database at another schema version instead of migrating it; migration steps re-read the version under the write lock | A newer binary upgrading the schema under an older running node; two processes applying one data step twice |
| Community heartbeats are signed by the server key and accepted only for the route the node holds with that key; clients trust "last seen" and relay counts only from a heartbeat they verify against the server key they know (docs/protocol.md §7.4) | A maintainer or anyone else inventing a community's liveness, or reporting someone else's community online |
| Reaping uses the signed times the node holds; anything signed longer ago than `community_reap_after` is refused; a reaped community comes back only with a route whose signed `reregistered_at` is later than its last-seen time | A peer resurrecting a reaped community by replicating an old copy; a stale copy of a route re-registering by itself |

**Residual risks.** Maintainers see IPs and timing of drops and fetches and
search queries (they publish a no-logging policy; the code doesn't log
queries); with local search on they see only snapshot downloads. A
maintainer can omit listings from a snapshot or search results, or serve
an old snapshot, as it can with search; clients that use several
maintainers can compare. Mailbox contents are not replicated between
independent maintainers; senders drop to every configured maintainer (and
each maintainer cluster), and only the members of one maintainer cluster
replicate to each other ("Maintainer clusters" below); docs/protocol.md
§7.1 and §7.5 describe how delivery behaves when nodes fail. A maintainer can claim a
community is offline or terminated (absence of reports can't be proven) or
withhold its heartbeats; clients then show it as offline or shut down, but
keep trying its relays (a terminated one every few minutes), so a lying
maintainer can't cut members off a server they can still reach, and a
maintainer can terminate a community only on its own node.

Route ids are not secret: `GET /v1/replicate` is public and lists every
route, private communities' included, so anyone can learn which server key
and relays a private community uses (not its name, members or content;
joining still needs a join key). Relay capacity and connection counts are
the relays' own claims (capped); a proven relay operator can still run
many relays. Community servers should not rank registry relays by those
numbers alone (internal/community). A relay URL proven once can later
resolve elsewhere (DNS); the proof is repeated every 10 minutes. Between
proofs, the first proven key keeps the URL while fresh.

## Maintainer clusters

**Asset.** The private data members replicate to each other: mailbox items
(ciphertext), inbox addresses and timing, deletes, blob records and
content (ciphertext), moderation decisions, community tombstones; and the
cluster's member list.

**Adversary.** A compromised or misbehaving cluster member; a node outside
the cluster; a network attacker between members; whoever steals the
operator key.

| Control | What it stops |
|---|---|
| The member list is signed by the operator key (offline), pinned by nodes and clients, newest valid `seq` wins, expires (180 days by default) | Anyone adding a node to a cluster; a lost operator key working forever; an old list coming back |
| Node-to-node traffic is a Noise XX session (prologue `openarcanum/meta-peer/v1`) where each side proves its node key; a node accepts sessions only from node keys it trusts and dials only those, re-checking on every request | Nodes outside the cluster reading or injecting private data; a network attacker reading or altering it, also over plain http inside a cluster; a member removed from the list keeping its session |
| Global ids are computed by every receiver from the item itself; replicated items never get a later expiry than the receiver's own `mailbox_ttl`; per-peer limits on items, deletes and bytes throttle a member that pushes too much (metric, log, `meta peers status`) | A member choosing ids to shadow or collide with others' items; extending items' lives; flooding the other members |
| Tombstones delete only items of the inbox they name | A member deleting mail of inboxes it doesn't name |
| Handshakes on `/v1/peer` are bounded per client network (4) and in all (64) and end within 10 s | Anyone holding unauthenticated upgrades open to exhaust the node |
| Moderation decisions are signed by their origin's node key, kept and passed on only from origins the node trusts now (and pruned when that trust ends), refused when dated more than 10 minutes ahead, and applied only from origins in `follow_decisions`, never over the node's own; signed public data (listings, routes, heartbeats, the member list) is verified on arrival as before | A member delisting communities on nodes that don't follow it, or forging the root's decisions on nodes that do; a decision dated at the end of time blocking every later change; forging listings, routes, heartbeats or the member list |
| A member's community tombstone counts only when its `last_seen` is older than the receiver's own `community_reap_after` and nothing newer was seen there, and its community owns the route the receiver holds; for a route id the receiver doesn't hold it binds no community | A member dropping any community cluster-wide with a tombstone dated now or in the future; blocking another community's listing by naming it |
| Nameplates aren't replicated; members hand out disjoint numbers | Two members accepting a claim of the same code (a second guess) |
| Node keys are never in backups; a restored or seeded node gets new ones | A backup taken off a node being used to impersonate it |

**Residual risks.** A compromised member can:

| It can… | Why it's limited |
|---|---|
| Read mailbox and blob ciphertext, inbox addresses and timing of the whole cluster | No plaintext: everything is end-to-end encrypted. This is the cost of replication, accepted for nodes run by trusted parties |
| Inject junk items | Bounded by `peer_items_per_minute` and `peer_bytes_per_day`; recipients drop what doesn't decrypt |
| Serve a wrong chunk of a replicated blob | The receiver discards what it collected and keeps the record, fetching again from any member; clients verify the hash themselves |
| Hold 64 stalled upgrades of `/v1/peer` open from 16 or more networks | Delays trusted peers' reconnects (they back off); bounded memory |
| Forge tombstones, deleting unread mail in inboxes whose items it holds | A delete can't be proven (the inbox has no key, and sending the token would hand out read and delete rights). Bounded by `peer_tombstones_per_minute`, visible in metrics and `meta peers status`; then the operator removes it (new member list, or `meta trust remove`). Mail already read is unaffected: clients delete only what they handled |
| Withhold replication | Clients read every member and merge, so withheld items still arrive from the member that took them |

It **can't** forge listings, routes, heartbeats or the member list (signed
elsewhere) or change moderation on nodes that don't follow it. Removing it
is a new member list without it; its sessions end on every node at once,
and what it already received stays with it (ciphertext).

## Direct messages and group DMs

**Asset.** DM content, attachments, who talks to whom, group membership.

**Adversary.** Relays, maintainers and community servers (all outside the
DM trust zone); a member of a group acting against the others; someone who
compromises one device; someone impersonating a contact.

| Control | What it stops |
|---|---|
| vodozemac Olm/Megolm; Olm payloads bind sender and recipient keys | Anyone outside the conversation reading or forging messages; re-addressing an Olm message |
| Signed device lists (identity + each device's consent), seq pinning | A server or relay adding a device to someone's list; rolling a list back |
| A contact card older than the one held (by `issued_at`) doesn't replace the fallback key or inbox; a device adding itself with an add certificate can change nothing about the listed devices (keys and names byte-identical) | A community server serving a stale card to revert a rotated fallback key; a device swapping another device's keys on the way in |
| Megolm rotation on any recipient-device change, after 100 messages and 7 days | A removed member or device reading new messages; long-lived session keys |
| New members get only the current session key at its current index | New members reading history from before they joined |
| Group state is an owner-signed chain checked by every member's client (`groups.py`) | A member renaming, inviting without permission, taking ownership, or adding people directly |
| DM policy and block list on the recipient's devices; group invitations always need accepting | Strangers adding people to groups; harassment by blocked people |
| A conversation nobody accepted yet (a message request, an invitation) contacts only the user's own maintainer nodes: no connection to its relay, no fetch from its attachments' nodes (also after a restart, after linking and on devices that learn of it through sync) until the user accepts (`DMs.reachable`, `test_message_requests_contact_no_node_the_sender_chose`) | A stranger learning a user's IP address by sending a request that names the stranger's own relay or blob node |
| Safety numbers, key-change warnings with verification reset | Impersonation through a swapped DM identity going unnoticed |
| Megolm room keys are kept per (session id, sharing device): an event opens only with the copy shared by the device it comes from, and must name that device inside the encryption | A member sharing another member's session key on first (first-wins binding) so the victim refuses the real sender's key and can't read them |
| Replay check per (session, index) for Megolm; Olm pre-key replays only decrypt in their existing session and can't re-create an old one; duplicate ids dropped | Replaying old events or key messages, including to break a device pair by evicting its real session |
| Live attachment chunks are taken only if their hash is listed in the sender's encrypted message (`DmFileRef.live_chunks`) | A member publishing forged chunks on the group's topic to spoil (or end early) another member's live file transfer |
| Inbox announcements only for the sender's own device; attachment locations (`stored`) only from the file's sender, https only, exact size, fetched only once the conversation is accepted | A member redirecting someone else's mail or downloads, or making a device download an oversized blob |
| Bounds on what others can make a client keep: Olm peers, pending requests (100, 30 days), early-event buffers (16 MiB, 10 minutes), old Megolm sessions pruned | Memory and disk exhaustion from remote input |

**Residual risks.**
- **Metadata:** mailboxes and relays see sizes and timing; maintainers see
  that some inbox received mail. Contact cards reveal that a member has DMs
  enabled in a community.
- **Malicious group member:** can leak what they receive (as anywhere), can
  release other devices' attachment fetch tokens early (they then see
  "expired"; the file isn't readable by the maintainer either way), and, as a
  member, sees who else is in the group. They can't rewrite other members'
  entries, redirect anyone's inbox or downloads, or keep themselves in a group
  the owner removed them from by racing changes (forks resolve to the owner's
  chain on every client).
- **Compromised device:** holds the identity keys and the current sessions;
  it reads everything until the user removes it, and Megolm sessions it had
  stay valid for messages already sent. Removal rotates every group.
  Post-compromise security comes from Olm's ratchet for future key shares.
- **Trust on first use:** without comparing safety numbers, a malicious
  community server could hand out a fake card the first time someone
  contacts a member. The key-change warning catches a later swap; comparing
  safety numbers catches the first.
- **Concurrent group changes:** competing changes resolve the same way on
  every client that has seen them (the chain with the most owner-signed
  states, then owner first, then lower hash, with rollback up to 64 states;
  a non-owner's fork never replaces an owner-signed state, so a removed
  member can't undo their removal by re-signing an earlier change); a losing acceptance is re-applied automatically,
  other losing changes are not (their author sees the result and can redo
  them). A member who missed a change catches up from the next one, which carries
  the 16 states before it; invitations carry the whole chain.
- **Group relay:** the group creator chooses the relay URL members connect to
  for live delivery (a `ws://`/`wss://` URL); a hostile creator learns members'
  IP addresses, as any relay does.

## Statuses

**Asset.** A user's status: online / idle / do not disturb / invisible, the
custom text and emoji, when it clears; and who the user shares it with.

**Adversary.** Community servers, relays and maintainer nodes (they carry
statuses but are outside the status trust zone); members and contacts the
user stopped sharing with; strangers trying to push statuses at a user.

**Trust zones.** Unlike everything else in a community, a status is *not* in
the community server's zone: it is end-to-end encrypted to the audience the
user chose (docs/protocol.md §4.11, §9.8). The server's zone still holds who
is connected, because it routes their sessions.

| Control | What it stops |
|---|---|
| vodozemac Megolm per audience, keys over Olm with sender and recipient bound in the payload (the DM design) | Servers, relays and maintainers reading or forging a status; re-addressing a key message |
| Status devices signed by the member key (`openarcanum/status-device/v1`), checked by the server on the way in and by every client before sending a key | The community server adding or swapping a member's devices to receive keys meant for them |
| A new session when a device that had the key leaves the audience (member left or removed, contact removed or un-chosen, un-shared, device removed), after 1000 messages and 7 days; a removed contact is told "cleared" first | Someone no longer in the audience reading the next status |
| New recipient devices get the session exported at its newest message | A late joiner reading earlier statuses |
| Contacts' statuses only from people the client deals with (contact, or someone in a conversation with the user), not blocked, from a device in their signed list; refused sessions' statuses dropped at once | Strangers pushing statuses or filling a client's buffers |
| Newest status wins (`at`), at most 5 minutes in the future; text and emoji limits; one line, no control or bidirectional characters | Replaying an old status; spoofed or garbled text in lists |
| Server bounds: 4 KiB per status, 10 changes in 20 s, 10 devices per member, 500 key drops a request, 120 requests a minute, 250,000 waiting in all, 7-day expiry; drops only for the recipient's registered (member-signed) status devices, one per sender and device, never trimming another sender's (`TestStatusKeyDropsOnlyForRegisteredDevices`); everything sealed at rest | Storage and fan-out exhaustion through statuses; one member evicting the keys others sent someone. Residual: many members (sybils) together can still reach the 250,000 total, which refuses new drops until they expire or are fetched |
| Invisible kept in the member record and applied from a session's first moment; a reconnect re-sends the same signed device entry (nothing to announce); no key requests while Invisible | Members seeing an Invisible user come online |
| Status text, keys and contents never logged (`test_status_text_is_never_logged`) | Leaks through logs and crash reports |

**Residual risks (said plainly in the app's help).**
- **Connections are visible to the community server.** It routes the user's
  sessions, so it always knows when they are connected, also while
  **Invisible**: Invisible hides the user from other members, not from the
  server. It also sees when a member's status ciphertext changes, its size,
  and which member devices receive key messages from whom (in a community
  that's every member device anyway). The user-visible state, text and
  expiry stay encrypted. An idle change is a status change, so a server can
  tell roughly when a user stopped and resumed using a device.
- **The community server decides membership.** A server that turns hostile
  can admit a member it controls; that member then receives statuses like
  any member (the new member shows in the member list). E2E here protects
  against a server reading statuses, not against it adding a reader the
  user can see.
- **Maintainer nodes** see that a contact's inbox received an item (the
  status refresh every 30 minutes looks like mail) and its size.
- **Linkability through content.** Sharing the same custom text in two
  communities where the user has separate identities lets someone in both
  (or both servers together) guess they're the same person, from the
  timing of changes too. Keys are separate per community and per DM
  identity; the text is the user's choice.
- **Contacts' freshness.** Contacts have no server telling them who is
  connected: a status is shown until it goes stale (75 minutes) unless the
  app posts "offline" when it quits (it tries; a crash leaves it until
  stale). Invisible posts the same "offline".
- **Lazy rotation in communities.** After a member leaves, the new key is
  made when the user next posts; until then nothing new is readable, and the
  last status the leaver saw stays theirs to remember.
- **Old statuses after a restart.** A client keeps only the newest status it
  has seen while running; after a restart a server could hand it an older
  (genuine) status of a member until that member posts again.
- **Fallback keys.** Status devices are reached with their published
  fallback key (no one-time keys, so a large community can't exhaust them).
  Recipients never reply, so these Olm sessions never take a Diffie-Hellman
  ratchet step: someone who later steals a device's status account (or its
  fallback key) and has recorded the key messages to it could read the
  status keys in them. That gives them statuses, nothing else (the account
  holds no DM or community keys); rotation limits each key to one session.

## DM attachments

See the maintainer DM blob store section below for the server side.
Files are encrypted on the sender's device with a fresh key (age v1 payload);
keys and hashes travel only inside Megolm events; live chunks on relays are
ciphertext; thumbnails are made on the sender's device and travel inside the
encrypted event. The group's "allow attachments" setting is enforced by
every recipient, not just the sender. Residual: file sizes are visible to
relays and maintainers; recipients that were offline past the lifetime lose
the file ("expired"), by design.

## Linked devices and device sync

**Asset.** Every key in the profile (linking copies them); the user's
device list.

**Adversary.** Someone who tricks the user into linking their device;
someone who overhears the code; a relay or maintainer; a stolen linked
device.

| Control | What it stops |
|---|---|
| SPAKE2 with the typed code; one guess (the first well-formed PAKE message on the topic is the only one used; a wrong code burns the nameplate); messages on the topic that can't be the other device's are skipped (malformed PAKE messages, `abort` before the keys exist or unsealed afterwards, `confirm` or `done` that don't open; a plain `abort` is honoured only while the new device waits for the confirmation, as the code-mismatch report); 5-minute expiry; key confirmation before anything is sent; maintainer nodes bound failed claims node-wide (200 per minute), so a distributed sweep can't guess at live codes at scale (linking answers 429 while a sweep runs) | Guessing the code; a relay or maintainer joining the link; a third party on the topic cancelling a link or driving it to failure |
| The existing device must be unlocked and the user must approve after seeing the new device's name, with the warning "Never enter a code someone sent you" | Social-engineered linking (reduced, not eliminated) |
| Sync events are Megolm events in the user's own device group; the group rotates on link and removal | Anyone else reading or injecting sync events; a removed device receiving new ones |
| Removal revokes the device's key at every community and drops it from every device list; a revocation a community couldn't take at the time (offline, unreachable) is kept signed in the vault and sent whenever that community next connects, until it's accepted | A removed device connecting or receiving new DMs, including at a community that was down during the removal |
| A community that takes a revocation rotates the key and topic of every channel the member can see (`TestDeviceRevokeRotatesChannels`) | A removed device that kept the old channel keys reading new messages from the relays |

Device list changes after the first need the approval of a device still in
the list (its own Account key, which no other device holds), and removals
are recorded in the list for good (docs/protocol.md §9.1 "Device list
changes"): a removed device can't sign a list that re-adds itself, with its
old keys or fresh ones, and the user's devices and peers that hold the
removal refuse one; a peer that took such a list before the removal arrived
drops it when the removal arrives (`test_dm_device_lists.py`).

**Residual risks.** All devices are equals (`docs/architecture.md` §16.7): a stolen
device that is unlocked, or whose passphrase is known, holds the identity
keys and can act as the user (sign messages, group states, community
actions) until the user replaces those identities (Replace member, `docs/architecture.md` §16.5).
It can't put itself back on a DM device list once removed, but it can race
the removal: before a peer has seen the removal, it can show that peer a
list it approved itself while still listed, and if that list also removes
the user's remaining devices, the two lists exclude each other and each peer
keeps whichever it saw first until the identity is replaced. A peer that
first meets an identity takes its list on trust (as it takes the identity). A user who
types a code an attacker sent them gives the attacker a full copy of the
profile, which is why the warning is shown before approval.

## Communities hosted on a desktop

**Asset.** A community server run by the desktop app (`openarcanum/hosting.py`):
its server key, storage key, database and backups in the profile's data
folder.

**Adversary.** Someone with access to the computer or its disk; a crash of
the app; another program on the same account.

| Control | What it stops |
|---|---|
| The folders are 0700 and the key files and `community.db` (with its WAL) 0600 (POSIX); the database is encrypted at rest as on any server | Other accounts on the computer reading the community |
| The server runs as a child process with `-stop-with-stdin`, so it stops when the app ends, however it ends | An orphaned server left running with nobody watching it, or a second copy started next to it |
| The claim code goes through a 0600 file the server deletes once claimed; it is never logged | Someone else claiming the new community |
| Hosted servers' logs hold ids and sizes, like every server's | Content in logs |

**Residual risks.** The storage key sits next to the data (the server must
start before the profile is unlocked), so anyone who can read the folder
as the user can read the community, as with a server on a VPS; full-disk
encryption covers a stolen device. A desktop that's off takes the community
offline, and after `community_reap_after` the maintainers shut it down until
its owner brings it back.

## Community server data at rest

**Asset.** Everything a community server stores: the member roll (display
names, member and device public keys, roles, join history), channel names,
keys and topics, roles, bans and reasons, join keys and their labels, the
audit log, message text, files and their metadata, avatars, contact cards
and key packages, community settings, channel categories, channel topic
text, and per-member channel overrides (which name a member's public key).

**Adversary.** Someone who gets a copy of the data directory's database
(`community.db` and its `-wal` file) or of a `.fbk` backup, but not the
storage key: a stolen or discarded disk, a leaked backup, a hosting
provider's snapshot; or someone who can also write those files (while the
server is stopped, or a backup before it is restored). Out of scope here: an adversary who controls the
running server (it holds the keys and can read everything, by design,
`docs/architecture.md` §6), and one who has both the storage key and the files.

**Controls** (details: `docs/operations.md` "Data at rest";
code: `internal/community/atrest.go`).

| Control | What it stops |
|---|---|
| Every row's content sealed with XChaCha20-Poly1305 under the storage key or a month key, AAD = table/column/row | reading names, content, keys or settings from the file; moving a valid ciphertext into another row, column or table (fails authentication) |
| Member and device public keys stored only as HMAC lookup hashes under a sealed lookup key | linking the database to known people by their public keys |
| `key_check` value under the storage key, checked at startup and before every schema migration from version 3 on | a server started with the wrong key silently creating data under a new key; it refuses to start. (A version 1 database has no key check: converting it detects a wrong key only if it holds encrypted messages, files or a public join key; docs/operations.md "Schema migrations".) |
| Tampered rows reported (startup error naming table and row, or logged and returned as an error) | silent loss or substitution of sealed values (for plaintext columns, see the next row; for whole older copies of a row, see the residual risks) |
| Plaintext columns that decide access (message and file channel, message creation time, join-key secret hash, uses, revoked and public flags, key-package device) bound to the row's sealed data and compared on every read (`atrest_bind.go`, schemas 7 and 8) | someone who can write the file moving a message or file into another channel, or un-revoking, resetting or re-pointing a join key: the server refuses and reports the row instead |
| Month keys for message bodies and file metadata, deleted once the month is past the longest retention, with `secure_delete` and a WAL checkpoint | recovering purged content from a later copy of the database |
| Backups are age files whose key is wrapped under the storage key (crypto inventory #7) | reading a leaked backup without the key |
| Logs carry action types, numeric ids and key fingerprints only | content and names leaking through log collection |

**What an adversary with the files but not the key still learns.** Row
counts and ids; per-channel message counts and timestamps (by channel id,
not name); approximate message and file sizes; join-key use counts and
revoked/public flags; which months have keys; that two rows belong to the
same member (equal lookup hashes). Every column left in plaintext, with the
reason it can't be sealed and what editing it on disk would do:

| Table.column | Why it can't be sealed | Edited on disk without the key |
|---|---|---|
| every `id` (`roles`, `channels`, `categories`, `join_keys`, `messages`, `files`, `pins.message_id`, `reports`, `automod_rules`, `emoji`, `audit`, `key_packages`) and `file_chunks.file_id/idx` | row identity: bound into each row's AAD, used for paging, foreign keys and ordering. Opaque counters. | bound (AAD): the sealed values stop decrypting |
| `members.kid`, `devices.did`, `bans.kid`, `contact_cards.member`, `key_packages.member` | keyed lookup hashes (HMAC-SHA256 under the sealed lookup key), not public keys: needed to find a member's or device's row at connect time without decrypting the whole table. Without the storage key they can't be linked to a public key. | bound (AAD, and members/devices are checked against the sealed public key) |
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

**Residual risks.**

| Risk | Mitigation / note |
|---|---|
| Storage key kept on the same disk as the data (the default `DATA/storage.key`) | Documented: keep `storage_key_file` off the data disk and back it up separately. Anyone with both the disk and the key file reads everything. |
| A backup taken before a month key was deleted still holds that key | Bounded by backup pruning (`backup_keep` × `backup_interval`); restoring such a backup deletes the key again on the first retention run. |
| Retention set to "forever" in any channel keeps every month key | Expected: data kept forever is kept readable. Purges in other channels still delete rows (with `secure_delete`). |
| SQLite may leave fragments of old pages in free space or the WAL if a checkpoint can't complete | `secure_delete` zeroes freed content; a failed checkpoint is logged and retried after the next key deletion, at shutdown and at startup (explicit `wal_checkpoint(TRUNCATE)` in `Server.Close` and `openDB`); migration runs `VACUUM`. |
| Someone who can write the files puts back an older sealed copy of a row (for example a join key's `bound`, `revoked` and `uses` from before it was revoked or used up) or an older copy of the whole database | Not detected: binding stops edits of single columns, but every sealed value is valid on its own and the server keeps no version counter outside the disk. Treat write access to the data directory like access to the running server. |
| The server's in-memory search index holds plaintext | Memory only, never written to disk; rebuilt from the encrypted database at startup. |
| **Search denial of service**: a member sends searches built to be slow (many one-letter prefix terms over a long history took tens of seconds), stalling everyone, since posts update the index under the server's model lock | Queries are at most 256 bytes and 5 words; a word is a prefix match only from 3 characters; each search is cut off after 2 seconds (the SQLite statement is interrupted). Posts, edits and deletes only queue their index change, applied by whoever holds the index next, so a running search never holds up the model lock. Tests: `TestSearchDoesNotBlockIndexing`, `TestSearchQueryBounds`. |
| The server keeps an in-memory index of who posted which message in the last 7 days (so a ban's history deletion doesn't decrypt the whole week) | Memory only, never written to disk; rebuilt from the encrypted database at startup and pruned every minute. Authors stay sealed in the database. |

## Moderation: AutoMod, reports, timeouts, custom emoji

**Assets.** Members' ability to post (timeouts, screening, AutoMod blocks),
the moderators' report queue and their attention, the server's CPU while it
checks messages, and the custom emoji images members' clients render.

| Threat | Control |
|---|---|
| A moderator acting on someone at or above them (kick, ban, timeout), or on an owner | Server-side hierarchy check (`Outranks`): the actor's highest role must be strictly above the target's; owners and administrators can't be timed out; owners can't be kicked or banned; only owners and administrators act on an administrator, wherever their role sits. Tests: `TestKickBanTimeoutHierarchy`, `TestAdministratorRoleBelowModerator`. |
| An audit log visible to everyone publishing the AutoMod filter list (each rule's signed action holds its keywords and patterns) | Those entries carry the signed action only for members with View Audit Log. Test: `TestAuditHidesAutoModRuleContents`. |
| Memory exhaustion by unadmitted sessions: anyone who completes a handshake sending messages the session layer reassembles up to 32 MiB each | The community server meters each request as its fragments arrive and closes the session past 512 KiB before admission (8 MiB for members). Test: `TestPreAdmissionRequestSizeBounded`. |
| **Decompression bombs**: an avatar, community icon or link preview image of a few kilobytes declaring a huge canvas, which every member's client would try to decode | The server reads the canvas size from the header (PNG, JPEG, GIF, WebP) and refuses more than 8192 pixels a side or 16 megapixels (a preview keeps its text without the image). Test: `TestImagePixelLimits`. Attachments are files the client opens on request and aren't checked this way. |
| **One-time key draining**: a member fetching someone's contact card over and over to burn their one-time keys (forcing the fallback key) | Fetches that hand out packages are limited per member: 3 per card and 30 in all per hour; beyond that the card comes with counts only. Test: `TestContactCardFetchesCantDrainKeys`. Residual: many accounts together can still drain a member's keys, after which senders use the fallback key (as designed for exhaustion). |
| **Relay takeover by an insider**: a non-owner with Manage Community pins the community to a relay they run and so sees every member's IP address and traffic timing | Changing `relay_mode` or `pinned_relays` needs an owner; the change is audited and every member's client follows the new list from the state. Test: `TestRelaySettingsOwnerOnly`. Residual: members aren't told separately (no in-channel notice), and an owner can still choose an unencrypted `ws://` relay. |
| **Role escalation**: a moderator with Manage Roles grants themselves (or a friend, or an invite) a role below theirs that carries a permission they lack (Ban, Administrator) | Granting, removing, editing, deleting or moving a role needs every permission the role carries (`holdsAll`), on top of the position check; Administrator is never held by a non-administrator, so only owners and administrators handle such roles. Tests: `TestAssignRoleNeedsItsPermissions`, `TestAdministratorRoleBelowModerator`, `TestRoleHierarchyRules`. |
| An insider rotating out every good backup (each on-demand backup prunes the oldest beyond `backup_keep`) or loading the disk with full copies | `ServerBackup` is owners only and at most once per 10 minutes. Test: `TestServerBackupOwnerOnlyAndLimited`. |
| Reaction bloat: members piling reactions onto one message so its sealed body (re-sent to everyone on each change) grows without bound | At most 20 distinct emoji and 1000 reactions in all per message (`MaxReactors`). Test: `TestReactionsBoundedInAll`. |
| **Raids**: many new accounts joining at once (with a leaked invite or the public key) to spam, mention-bomb or post links and files | Raid mode (`lockdown`) sends every new join to the approval queue and stops new invites; new-member restrictions (`new_member_minutes`) keep links, files and mentions from accounts younger than N minutes in the community; bulk ban (`BulkRemove`) with history deletion clears up to 100 at once, all-or-nothing. Tests: `TestRaidModeLockdown`, `TestNewMemberRestrictions`, `TestBulkRemove`. Residual: the desktop app doesn't show these controls yet. |
| A timed-out moderator still kicking, banning or changing roles; a timed-out member changing their avatar or profile text | A timeout leaves only View Channel, Read Message History and Connect (`PermTimeoutMask`), and profile changes are refused while it lasts. A timeout and a server mute or deafen survive leaving and rejoining. Tests: `TestTimeoutRestrictsEverything`, `TestTimeoutAndMuteSurviveRejoin`. |
| An old invite granting a role that was deleted, after a new role (say, Administrator) took its id; AutoMod exemptions and emoji references repointed the same way | Role, channel, category, emoji and join key ids are never reused (schema 11, AUTOINCREMENT). Tests: `TestDeletedRoleIdIsNeverReused`, `TestMigrationV11KeepsIdsAndSequences`. |
| An audit log visible to everyone naming hidden channels and who was given what there | Entries about channels the viewer can't see are left out for members without View Audit Log. Test: `TestAuditHidesChannelsViewerCantSee`. |
| One device key or member opening sessions without bound (each costs a goroutine, a queue and a state build per broadcast); anyone fetching the icon through `GetInfo` at the full request rate | Sessions are capped per device key (4) and per member (20), the oldest being closed; pre-admission requests get a tenth of the rate and `GetInfo` 3 then 1/s per session; the open-report count behind every state build is cached. Tests: `TestSessionsCappedPerDeviceAndMember`, `TestGetInfoRateLimited`. |
| Fetching a file's chunks with View Channel alone, around Read Message History and the history gate | `FileGet` applies the checks of the message carrying the file. Test: `TestFileDownloadFollowsHistoryGate`. |
| A timed-out or screened member posting through another path (edit, upload caption, custom emoji, reactions, voice) | The restriction is applied to the member's effective permissions (`restrictedPerms` in `basePerms`/`channelPerms`), so every permission check sees it, plus explicit checks with a clear error on post, edit, upload begin and finish, reactions, typing, display-name and nickname changes and contact-card fetch. |
| **AutoMod regex denial of service**: an admin (or a stolen admin key) saves patterns that make every post expensive | Go RE2 (`regexp`) matches in time linear in the input, with no backtracking. Patterns are limited to 260 characters, 10 per rule and 20 rules; each compiles to at most 1000 instructions and all patterns together to at most 5000; a pattern matching the empty string is refused. Messages are at most 4000 characters, so the worst case is bounded: the whole 5000-instruction budget over a 4000-character message takes about 75 ms (`TestPatternCost`). Matching runs before the server takes its model lock, so a slow check never stalls other members. Keywords are matched by word lists and substring search, not regexes. |
| AutoMod as a content leak | Matching happens in memory on text the server already holds; nothing about the text is logged. Alerts and audit entries reference message ids and rule names, and moderators read a reported message through the normal permission-checked history. |
| **Report abuse**: a member floods the queue, or reports to harass | Per member: one report per message (keyed hash, unique), at most 5 reports per 10 minutes and 20 open at once (`TestOpenReportsCappedPerReporter`); the queue refuses new member reports at 5000 open ones (residual: 250 accounts together can still fill it); reasons are at most 500 characters. Reports show the reporter's name to moderators, so abuse is attributable; moderators can dismiss. Reports aren't anonymous to moderators, by design. |
| The moderator queue counts (`ModQueue`, the badges) telling non-moderators about reports or join requests | The server fills each count only for a member with that queue's permission (open reports with Manage Messages, join requests with Approve Members), in the state and in pushes, and sends a member with neither no `ModQueue` at all; losing the permission drops the counts on the next state. The counts are numbers only, no ids or names. Test: `TestModQueueCountsAreGatedAndLive`. |
| A report revealing a message to someone who can't read its channel | Reporting needs read access to the message; the queue shows ids, names and the reason, not the message text, and jumping to the message goes through history, which checks View Channel. |
| Malicious emoji images (parser bugs, oversized or remote content) | The server reads only image headers (PNG via `image/png` DecodeConfig, GIF via `image/gif` DecodeConfig, WebP chunk headers parsed by hand; fuzzed in `FuzzEmojiImage`), caps size at 256 KiB and 128×128 px, and accepts only PNG, GIF and WebP. Clients get images over the session and show them as `data:` URIs; the webview's CSP (`img-src 'self' data:`) never loads a URL. |
| Emoji uploads bloating the audit log or being swapped in transit | The signed action holds the image's SHA-256 and size; the server checks the image against them; the audit log stores only the action. Count is capped by `max_custom_emoji` (≤ 250). |
| A timed-out member still talking in a call they had already joined | The timeout removes Speak, Video and Screen Share from their effective permissions; `voice.go` rechecks every call participant after each admin action (and every 10 s, which covers a timeout ending), so they become listen-only with a new grant without audio or video. Test: `TestTimeoutMakesCallListenOnly`. |
| Someone with write access to the database closing, reopening or hiding reports, or clearing the one-report-per-message hash | `reports.status` and `reports.month` are sealed in the report record and `reports.dedupe` is recomputed from the sealed reporter and message; a mismatch is a TamperError when the report is read (`TestTamperedReportColumnsAreRefused`). As with every bound column, putting back a whole older sealed row isn't detected (see "Data at rest"). |
| Memory exhaustion through uploads (unfinished uploads are buffered in memory until `UploadFinish`) or room held in the file store by uploads never finished | At most 4 uploads and 50 MiB in flight per member and 512 MiB on the server; an unfinished upload is dropped after 3 idle minutes or 15 in all, giving back its room. Test: `TestUploadsInFlightBounded`. Residual: many members together can still keep the server's 512 MiB busy, delaying others' uploads (streaming uploads to sealed temporary storage would remove the memory bound altogether). |
| A toggled-off feature still usable | With custom emoji off, uploads, renames, image fetches and messages using an existing emoji are refused with a distinct DISABLED error; the client hides the picker and the list. |

**Residual risks.** A member who is timed out can still read and report; a
banned member can rejoin with a new key unless invites are controlled
(bans are per key). The report rate limit is in memory and resets when the
server restarts. AutoMod keyword matching is by letters and digits, so
obfuscated spellings (`k u m q u a t`, homoglyphs) get through unless a
regex covers them.

## Maintainer DM blob store

**Asset.** Encrypted DM attachments held on maintainer nodes for up to
`blob_ttl` (72 h), and the metadata around them: who uploaded, who fetched,
when, and how large.

**Adversary.** A maintainer operator (or someone who takes over a node or
copies its disk) who wants to read files or learn who sends files to whom;
and an anonymous network client who wants to exhaust a node's disk or fetch
files meant for others.

| Control | What it stops |
|---|---|
| Files encrypted on the client with a fresh key that travels only inside the encrypted DM message; the node stores ciphertext addressed by its SHA-256 | the node reading files; the node substituting a file (clients check the hash) |
| Fetch tokens: only `SHA-256("openarcanum/blob-token/v1" || 0 || token)` registered, one random token per recipient device, carried inside the encrypted message | the node learning recipients or linking fetches to identities; anyone without a token downloading a blob, even knowing its id |
| Uploads stored under an upload id over the blob id, size and every token hash, which only the sender knows; `create` from anyone else starts a separate upload and `chunk` needs the upload id, so both answer the same whether the blob exists or not; `ack` answers the same for registered and unregistered tokens; `fetch` answers 404 for unknown blob, wrong token and expired alike (tests: `internal/meta/blobs_limits_test.go` TestBlobExistenceCantBeProbed) | probing for which blobs exist, including a recipient (who knows the id and one token) or anyone else using the endpoints as a read receipt for when every device has fetched |
| Per-network rolling 24 h limits (IPv4 address or IPv6 /64) on bytes (`blob_quota_per_ip`), uploads (`blob_uploads_per_ip`) and fetch tokens (`blob_tokens_per_ip`), uploads-per-minute limit, `blob_max_size`, `blob_store_max` with 507 when full; unfinished uploads discarded an hour after their last new chunk or 6 hours after they started (resending a chunk doesn't count) | one client filling the disk, the store or the database (row count), or holding reserved space with an upload kept open |
| Client addresses used for quotas are kept in memory only; logs carry upload ids and sizes only | addresses and content leaking through the database, backups or log collection |

**Residual risks.** The node sees uploader and fetcher IP addresses and
times, ciphertext sizes (roughly the file size), and how many recipient
devices a blob has (the token count); a colluding node operator can
correlate an upload with later fetches of the same id by address. The
sender can still learn whether its own upload has been fetched by every
device (it can resume it), which tells it nothing it couldn't learn from the
recipients' replies. The per-network limits can be evaded by an attacker
with many addresses (the store cap still bounds the bytes, at the price of
refusing honest uploads until blobs expire; the row count is bounded only
per network and by `writes_per_minute`), and users behind one carrier-grade
NAT share them.
A restart forgets quota usage. Blobs aren't replicated, so a file survives
a node loss only if the sender reached a second node.

## Community messaging features

Formatting, reactions, pins, mentions, typing, presence, nicknames and
profiles, attachments and link previews. Message content already reaches the
community server (`docs/architecture.md` §6); these features add metadata and one outbound network path.

**Asset.** Message text and its formatting; who reacted with what; who is
online and when; who is typing; nicknames, pronouns and about text; links
members post; the community server's own network position.

**Adversary.** A malicious member (crafted text, links and emoji); a
malicious or compromised website a member links to; an observer of relays;
the community server's operator (trusted with content, as before).

| Control | What it stops |
|---|---|
| Formatting is parsed by `markdown.js` into a tree and built with `createElement`/`textContent`; no HTML is ever parsed from message text (a unit test also checks no UI file uses `innerHTML` and friends) | script or markup injection through Markdown, code blocks, spoilers, link labels, mention names, preview titles |
| Links are shown as text and URL, never as `<a>`; clicking offers to copy the address; clients never fetch link targets or preview images from the web | members' IP addresses reaching linked sites; tracking pixels; drive-by downloads |
| Reactions, mentions, the link preview and the join/leave event are part of the message body, sealed under the month key and fanned out inside the channel envelope; pins are sealed under the storage key | reading them from a stolen disk or from relays |
| Mentions are parsed by the server and gated by permission (Mention @everyone/@here and All Roles for @everyone, @here and non-mentionable roles); members who can't read a channel aren't mentioned in it | members mass-pinging the community; notifying people about channels they can't see |
| Typing and presence go only to members: typing inside the encrypted channel envelope on the channel topic, presence as pushes on members' own sessions | relays and non-members learning who is typing or online |
| Profile changes (display name, about and pronouns, and a member's own nickname) are limited to 3 per member in any 6 s, since each rebuilds every session's state; setting an unchanged nickname is a no-op | a member flooding every session with state rebuilds and the audit log with entries |
| Typing is rate limited per session and channel; reactions capped at 20 emoji per message, pins at 50 per channel, mentions at 100 members/20 roles, attachments at 10 per message, staged uploads expire after an hour and are capped at 40 per member in a channel (400 per channel), so one member can't block others' attachments | a member using these features to exhaust storage or flood other members |
| Every feature has a community toggle and (attachments, previews, reactions) a channel setting, enforced by the server with a distinct "turned off" error; changes are signed and in the audit log | a hidden button being the only control |

**Link-preview fetcher (SSRF).** With previews on, the community server makes
HTTP requests to URLs that members choose: the classic server-side request
forgery setup. A member could try to make the server reach its own admin
interfaces, the home LAN it runs in (a Raspberry Pi in a DMZ), or a cloud
metadata endpoint, and read the answer back through the preview.

| Control | What it stops |
|---|---|
| Off by default (community toggle); senders need Embed Links; operators can disable the fetcher for the whole node (`link_previews_disabled`) | exposure in communities that don't want previews |
| Only `https` by default (`preview_schemes`); no credentials in URLs | plain-HTTP targets and URL-embedded credentials |
| Every address a host resolves to must be public (loopback, RFC 1918, link-local incl. `169.254.169.254`, CGNAT, multicast, reserved, documentation, NAT64/6to4/Teredo refused); the connection goes to the checked address, and the socket address is checked again at connect time | requests to the LAN, the host itself or metadata services, including via DNS rebinding and via IPv6 forms of IPv4 addresses |
| Redirects at most 3, each re-checked; no environment proxy | redirect-based bypasses |
| 5 s timeout, 1 MiB read, `text/html` only, image ≤ 256 KiB and sniffed as PNG/JPEG/GIF/WebP, 4 concurrent fetches | resource exhaustion and using the server to download large or arbitrary content |
| Only `og:` title/description/site/image (trimmed, control characters dropped) are kept; the page itself is discarded | reading arbitrary response bodies back through the preview |
| The URL is never logged | content leaking through logs |

Residual: the fetcher reveals the server's IP address and the time a link
was posted to the linked site (by design: the server, not members, is
exposed; operators who don't want that keep `link_previews_disabled`). A
public site can serve a misleading title or image for a link; previews show
the URL next to the title. Tests: `TestPublicAddr`,
`TestFetcherRefusesNonPublicTargets`, `TestLinkPreviewRefusesLoopback`,
`TestLinkPreviews` (redirect limit, size limit, non-HTML) and the end-to-end
check in `test_e2e_messaging.py` that a loopback link is never contacted.

**Presence and typing metadata.** Every member learns when other members
connect and disconnect, and when they type in channels both can read. The
server already knows both. A member who sets their status to Invisible
appears offline to other members ("Statuses" above); typing can be turned
off per community. Presence is
announced only on transitions (first session opened, last one closed), so
multiple devices don't reveal more.

**Residual risks.**

| Risk | Mitigation / note |
|---|---|
| Custom emoji references (`:name:id`) are accepted by syntax; whether the id exists is checked by the custom-emoji feature | Rendering falls back to the `:name:` text, so an unknown reference shows as text |
| The server sees reactions, mentions and profiles in plaintext while running | By design (`docs/architecture.md` §6); the client says admins can read everything |

## Owner keys and the owner set

**Asset.** A community's owner keys: any key in the current owner set can
certify a server key (so clients and maintainers accept that server as the
community), rotate the server key, adopt a restored server, and add or
remove owners (docs/protocol.md §3.1, §4.5). Owner keys live in each owner's
encrypted vault, are copied to that member's other devices by device sync,
and are included in their backups. They never go to the server.

**Adversaries.** A removed owner; a compromised or malicious co-owner; a
malicious community server, relay or maintainer node; someone with one of a
member's devices.

**Controls** (code: `internal/identity/owners.go`, `internal/community/owners.go`,
`clients/desktop/src/openarcanum/owners.py`; tests: `internal/identity/owners_test.go`,
`internal/community/owners_test.go`, `internal/meta/meta_test.go`
TestRoutesAndListingsFollowOwnerSet and TestRemovedOwnerCannotFreezeEntries,
`clients/desktop/tests/test_owners.py`,
`clients/desktop/tests/test_e2e_owners.py`).

| Control | What it stops |
|---|---|
| Every owner change is signed by a key in the set before it, numbered, and chained by hash; certs name the exact set they were signed under | the server, a relay or a maintainer adding an owner, dropping a change, or reordering changes |
| `OwnerKeyRequest` signed by the new owner key and by the member's identity key | the server substituting its own key for the one a member's device generated, or binding an owner key to a member who didn't ask |
| Clients and maintainers keep the last verified `(owner_seq, owner_head)` and refuse sets that don't extend it | a removed owner presenting an older chain in which they were still an owner, or a forked chain |
| Each owner change carries a new cert under the changed set; certs from keys outside the current set are refused | a removed owner's certificate staying valid until the next rotation |
| Server checks on every owner action: actor is a member holding a current owner key, signs with that key, signatures valid | non-owners, removed owners and forged signatures changing the set, rotating the key or adopting a restored server (the client checks are not the only control) |
| Removal (or an owner asking to leave) takes away everything ownership gave: the member keeps no roles (the founder's Admin role included), channels they could read only as an owner get new keys, and an owner who asked to leave is treated as a member at once (tests: `internal/community/owners_test.go` TestSecondOwnerRotatesAndRemovesFounder, TestOwnerRemovesThemselves) | a removed owner still kicking, banning, changing roles or settings through a role they held as an owner |
| Maintainers order routes and listings by owner set before seq, and refuse a seq more than 24 h ahead of their clock | a removed owner freezing a route or listing at a node that hadn't seen the removal by posting a huge seq |
| The last owner (counting only owners who haven't asked to leave) can't be removed; a key that was ever an owner can't be added again | a community losing every owner by one mistaken change; a removed key returning silently |
| Single-owner warning in Ownership and above the chat | a community quietly depending on one device or backup |

**Residual risks.**

| Risk | Mitigation / note |
|---|---|
| A compromised or malicious co-owner can remove every other owner and keep the community: there is no quorum, every owner has full power | By design: any owner can add or remove owners. Add only people you trust as owners. Every change is signed and in the audit log and the owner chain; removed owners see "You're no longer an owner". There is no protocol-level recovery once the other owners are removed. |
| Any owner can certify a server they run and publish a route for it, moving the community there | Same power the founder always had; the audit log and the owner chain show who signed. |
| A client that has never seen a later owner set (a new joiner with an old invite, or a client offline since the removal) can accept a cert from a removed owner under an older chain | Maintainer nodes refuse routes and listings under sets older than the one they store, and connected clients update their known set from every pushed state. A brand-new maintainer that never saw the community can't tell. |
| A stolen, unlocked device (or a backup and its passphrase) exposes that member's owner key | The thief can act as that owner until another owner removes the key. A member's devices share one owner key, so another owner removes it and invites the member again; their client makes a new key. |
| An owner who removes themselves stays in the chain until another owner's client signs the removal (they can't sign a cert under a set without them) | The server stops treating them as an owner at once, but until another owner comes online their key can still sign certs that verify against the current chain. Removing yourself means trusting yourself until then; to cut a key off at once, another owner removes it. |
| The server can refuse to apply an owner change or keep presenting an old cert | It can't forge changes. Owners can move the community to other hardware (any owner can adopt a restored server). |

## Community permissions and feature toggles

**Asset.** Who may read, post, invite, manage and talk in a community, and
which features exist there (docs/admin-guide.md).

**Controls.** Every permission, hierarchy rule, slowmode and toggle is
checked by the community server (`perms.go`, `channels.go`,
`features.go`); the client only hides what the member can't do. Losing
read access to a channel through any role, override or category change
rotates its key and topic (§6.2 of docs/protocol.md). Every change is a
signed, audited admin action.

**Residual risks.**

| Risk | Mitigation / note |
|---|---|
| A member without Speak holds the call key | Their grant allows no audio and the relay forwards none of theirs; frames under another participant's key id on their track are dropped by receivers. |
| Slowmode timestamps are kept in memory | A server restart lets each member post once more before slowmode applies again. Uploads begun before a post are checked again when they finish (`TestUploadsInFlightUnderSlowmode`). |

## Calls

**Asset.** What people say and show in calls; who is in which call.

**Adversaries.** A relay operator; a malicious participant; someone who was
in the call before.

| Control | What it stops |
|---|---|
| Every encoded frame is SFrame-encrypted in the sender's client (RFC 9605, test vectors pass) under a call key only the community server and the participants hold; the relay's SFU forwards ciphertext (only the VP8 payload header is clear, and authenticated) | The relay or the network hearing or seeing a call |
| The call key changes on every join and leave and on relay moves; receivers keep only the newest four epochs, for frames still in flight at the change | Someone who left hearing what follows; a newcomer decrypting what came before |
| A participant's frames must carry the slot they held in that frame's epoch; receivers keep keys only for the current and previous epoch, for the participants of each | One participant's frames being played as another's (with an honest relay); someone who left being heard under an old key in a reused slot |
| Each key id has a replay window in the receiver (a counter opens once, and not at all once 8192 behind the highest seen; recorded only after the frame authenticates; the window is freed with its key, and the page refuses frames of epochs it no longer holds before the window is consulted), and a frame's kind of media (voice, camera, screen, screen sound) is bound into its SFrame metadata; a sender's counter for a key id continues if its key is set again | A relay or a participant replaying frames (repeating someone's words), moving a frame to another kind of the same sender (a screen's sound played as the voice), or a key set twice making a page reuse nonces |
| Reconnecting rotates the key, and a page never sends under a key id it used before with the same key (it remembers each key id with a hash of its key: a new call in an empty channel starts at the same key id under a new key, which is safe) | A relay forcing reconnects to make a client reuse SFrame (AES-GCM) nonces |
| A connection that never comes up (UDP to the relay blocked) is reported to the user and the member leaves the call; the page doesn't ask to reconnect, since every rejoin rotates everyone's key | A blocked network (or a relay that never answers on UDP) making a client rejoin in a loop and rekey the whole call each time |
| The call's signed control travels in every session and is handed to the relay with each join | A relay that starts a call (first join, after a move or restart) ignoring server mutes, deafens and removals |
| Relays admit only on a grant signed by the server key the call id commits to, valid ≤ 10 minutes, and refuse removed participants for as long as a grant could be used (a call keeps its newest 396 removals beyond the current control's list, the relay the newest 65536 across calls; control lists name 16-byte participant ids only) | Joining a call without the server's say-so, or coming back after removal; controls growing relay memory. Residual: a relay that restarted takes the first control it is brought; someone removed earlier can get back in with an unexpired grant until the next participant brings a newer control |
| Server mute, Speak, Video and Screen Share are enforced by the relay (signed controls), not only by the clients | A modified client sending what it isn't allowed to |
| No RFC 6464 audio level: the relay doesn't negotiate it, and the client strips it (with every RTP header extension but mid, rid and transport-wide congestion control) from every description it sets, its own and the relay's (`call.js` `hardenSdp`) | The relay (or the network) reading each packet's loudness and voice-activity bit in the clear: who speaks, and how loudly |
| Voice and screen sound are Opus at a constant 32 kbit/s without DTX, insisted on by the client whatever the relay says; a muted or gated (voice activity, push to talk) microphone sends encoded silence at the same size and rate | Recovering what is said, or the language, from packet sizes (with a variable bitrate they follow the speech, as published attacks exploit), and telling speech from silence. Measured through a proxy in front of the relay (`test_calls.py::test_relay_sees_no_voice_level_and_constant_voice_packets`): with a variable bitrate and the audio level extension, speech gave 26 to 37 different packet sizes in 3 s (33 to 163 bytes), silence 34 bytes, and every packet carried its level; with these settings speech, a tone, noise and silence, in studio mode, with push to talk held and released, all give one size (111 bytes; 110 before SFrame's counter passes 256), 50 a second, and no level. `internal/sfu` `TestVoiceNegotiatesNoAudioLevelAndConstantBitrate` checks the relay's side. Cost: 52.4 to 53 kbit/s of RTP per sending microphone (plus IP and UDP headers), where variable bitrate used about 44 to 48 while talking and 24 while silent |

**Residual risks.**

| Risk | Mitigation / note |
|---|---|
| Participants share one call key per epoch (sender keys are derived from it and the key id), so a malicious participant can forge frames under another participant's key id; with a malicious relay re-labelling them onto the victim's track, others would play them | Needs a malicious participant and relay together. Per-sender keys distributed by the server would close it; the forgeries are limited to the current call and epoch. |
| The relay sees each call's participants by random id and IP address, and when each kind of their media flows and how much | As with any SFU. Grants carry no names, so the relay doesn't learn who the participants are. Voice says nothing more: packets of one size, 50 a second, from join to leave, whether the participant speaks, is silent, is muted or has their gate closed (see the control above); it stops only when nobody receives it (everyone deafened or muted it for themselves, which the relay already knows from `CallListen`) or the participant has no microphone. A few fixed-size packets of the engine's bandwidth probing, and a byte more as SFrame's counter grows, are the only other sizes; under congestion the engine may lower the Opus bitrate, so sizes then follow the network, not the speech. |
| Video and screen share frames are variable bitrate (VP8): their sizes and rate follow what is shown (motion, scene changes, a still screen sending little), and each frame's VP8 payload header is in the clear (authenticated): keyframe or not, and on keyframes the resolution, plus the first partition's size | Inherent to forwarding encrypted VP8 by layer: the SFU must find keyframes, and constant-bitrate video would cost every layer's full rate all the time. The relay already knows which layers are sent and asked for. Not used for voice, which is where size-based attacks recover content. |
| The relay learns what each participant watches and listens to (`CallLayer`, `CallListen`: who is deafened, who muted whom for themselves, which screens are watched), by random participant id, and tells each sender how many receive its media (`CallWatchers`, counts only) | An SFU needs most of this to forward only what is wanted (the layers it already had); it saves everyone's bandwidth, and a sender stops sending what nobody takes. Ids are random per call and grants carry no names. Senders learn counts, never who; the server and other participants learn nothing (Mute for me isn't announced). A relay lying in `CallWatchers` can only make a sender stop sending, which it could do by dropping the media anyway. |
| A relay can drop or delay media, or refuse service | Clients report an unreachable relay and the server moves the call. |
| **Use Voice Activity is enforced by the client only.** Media is end-to-end encrypted, so neither the server nor the relay can tell whether a participant's audio went out because a key was held or because the voice detector opened; a modified client can use voice activity without the permission | The server works the permission out per channel and sends it in every voice session and update (`can_voice_activity`, docs/protocol.md §12.2); without it the official client sends audio only while the push-to-talk key is held (in the window, and system-wide when "Push to talk in other windows" is on), whatever the member's input mode, and the voice bar says "push to talk only" (`test_ui_voice.py::test_without_use_voice_activity_only_push_to_talk_sends`). Discord enforces it in its client too. Speak, server mute and the rest are enforced by the relay, so this permission only decides *how* a member who may speak opens their microphone, never *whether* they can be heard. |
| A voice channel's text chat is an ordinary channel's messages (docs/protocol.md §4.3): the same channel-key encryption, topic, rotation, permissions and at-rest sealing as a text channel; being in the call doesn't grant reading it | No new risk: who can read it is decided by View Channel and Read Message History, as for text channels. The call key never touches it. |
| Anyone can make a server key and sign grants for calls of their own, using a relay's SFU | Bounded per relay: calls, participants (in all calls and per address), each participant's bitrate (excess dropped, not fanned out), two calls per connection, control sizes; a call nobody is in or joining is dropped at once, so refused joins (a removed participant, a bad offer) can't pile up calls until `max_calls` is reached (`FuzzRelayCallFrames`, `TestRefusedJoinLeavesNoCall`); `call_servers` can restrict a relay to known community servers (docs/protocol.md §12.5). |
| SFrame's AES-GCM and HMAC come from Chromium's WebCrypto (`crypto.subtle` in the QtWebEngine page), not from `cryptography` or Go: a flaw in that engine's implementation, or a compromised renderer, would expose call media and the current call key. This is the one primitive provider outside `cryptography` and the Go standard library and `x/crypto` (`docs/crypto-inventory.md` "Deviation: WebCrypto for SFrame") | Inside the page WebCrypto is the only cryptography there is, and it is Chromium's BoringSSL; a JavaScript library would be worse and moving SFrame to Python would send every frame across the bridge. The page's `sframe.js` passes the RFC 9605 vectors, the real engine interoperates with the vector-checked Go implementation (`test_calls.py`), only the per-call key (current and previous epoch) is ever in the page, and Chromium's renderer sandbox stays on. Updates to OpenArcanum carry Chromium's fixes through the pinned PySide6. |


## Installers and invite links

**Asset.** The integrity of the installed app (everything else depends on it);
the user's consent to joining a community.

**Adversary.** Someone who tampers with a download or the build's tools; a web
page or message that opens a crafted `openarcanum://` link; another program or
user on the same computer.

| Control | What it stops |
|---|---|
| Release artifacts are built only by `.github/workflows/release.yml` from the tagged source; Windows installers (and the app and uninstaller in them) are Authenticode-signed with a timestamp, the macOS app and `.dmg` are Developer-ID signed with the hardened runtime and notarized, once the certificates are configured (`docs/packaging.md`). Updates are additionally checked against the signed manifest (`docs/release.md`, "Updates") | Installing a modified app |
| The AppImage tools (appimagetool, runtime) are pinned to a release and checked against SHA-256 values in the repository before use; a mismatch stops the build | A tampered build tool slipping code into the AppImage |
| The hardened runtime's exceptions are only JIT, unsigned executable memory (Chromium's V8) and microphone/camera; library validation stays on (every Mach-O is re-signed with the app's Team ID, checked by `build_dmg.sh`) | Code injection into the signed macOS app beyond what the engine needs, including a dylib planted inside a user-writable `/Applications/OpenArcanum.app` |
| A `openarcanum://invite/…` link only **prefills** the Join dialog; nothing is fetched or joined until the user presses Continue (a preview through the invite's relays, as for a pasted invite) and then Join. A locked profile holds the link until unlocked. Anything that isn't `openarcanum://invite/<base64url>` (16 KiB at most) is ignored (`codes.link_from_os`) | A web page or message joining the user to a community, or contacting a server, without the user's say-so |
| A second launch hands its link to the running app over a local socket that only the user can open (Qt `UserAccessOption`; on Linux in the user's private `$XDG_RUNTIME_DIR`, else in the profile's own 0700 config folder; on macOS in the per-user temporary folder; on Windows a named pipe), named after the user and the profile. Messages are one JSON line holding only a link (16 KiB at most), validated like a launch argument; anything else is dropped and logged without content (`openarcanum/instance.py`) | Another local program driving the app beyond prefilling the dialog; a crafted message crashing it |
| Before handing a link over, and before backing off as a second instance, the launch checks who serves the name: on Linux and macOS the socket file must be owned by this user and the peer's credentials (`SO_PEERCRED` / `LOCAL_PEERCRED`) must be this user's ID; on Windows the pipe server process's token user (`GetNamedPipeServerProcessId`) must be this user's SID. Otherwise nothing is sent and the app starts normally, without single-instance hand-off (logged) | Another local user who creates the name first (a shared Windows PC or terminal server, or a socket in `/tmp`) receiving invite links (and the join keys in them) or keeping the app from starting |
| The OS-registered launch commands pass the link after `--` (`openarcanum.iss`, `openarcanum.desktop`), so a link can't add options | A raw link with quotes and spaces (Windows substitutes `%1` unescaped) starting the app with `--home` on a remote share or with DevTools |
| Invite links are never logged (`test_ui_invite_link.py` checks the log) | Invite secrets (join keys) ending up in logs |
| The installers write only the app's folder, shortcuts and the scheme registration; uninstalling removes those and keeps the profile, whose files are encrypted (vault) | Uninstall leaving the program registered for links; data loss on uninstall |

## Updates

**Asset.** The code the user runs: whatever the updater installs runs with
the user's rights and holds every key once the profile is unlocked. Also the
fact that this computer runs OpenArcanum (the update host sees each check).

**Adversaries.** A compromised update host or mirror; a network attacker
between the client and the host; someone holding an old, validly signed
manifest; a page script (an XSS in the UI); someone who steals a release key.

| Control | What it stops |
|---|---|
| The manifest is signed with an Ed25519 release key (generated offline; kept offline, or as a secret of the GitHub Environment `release` behind required reviewers, visible only to the one signing step of the release workflow, whose actions are pinned by commit; the workflow then checks the manifest against the client's built-in keys: `docs/release.md` "The release key") over `openarcanum/release-manifest/v1 \|\| 0x00 \|\| payload`; the client checks the signature against the release keys built into it before parsing anything, and each download's size and SHA-256 come from the signed manifest (`docs/release.md`) | A compromised host, mirror or CDN, or a network attacker, serving a different manifest or different installers. The worst the host can do is withhold updates |
| `https://` only, for the manifest, downloads and every redirect (loopback `http://` only for the test source) | Network attackers reading or rewriting the traffic, and redirects to plain HTTP |
| `expires_at` is enforced; a manifest whose `released_at` is older than one already accepted on this device is refused | Freeze: replaying an old manifest to hide newer releases lasts at most until it expires (a manifest valid for more than 30 days is refused), and can't undo a newer manifest the device has seen |
| Only a version newer than the running one is installed (SemVer precedence) | Rollback to an older, vulnerable release, even one that was validly signed |
| `product` must match | A manifest signed by the same key for another product |
| Downloads are bounded by the signed `size`, and the installed copy is re-hashed as it is written (AppImage next to `$APPIMAGE`, Windows installer and macOS `.dmg` in a private temp folder) | Disk filling from an endless download; a file swapped in the cache between the check and the install |
| Nothing installs without the user choosing "Restart and install" in the prompt; Python refuses the install call while the profile is locked, and unless the verified download of that exact version is ready and the call carries the one-time token issued with the prompt; the page sends it only from a trusted click (`event.isTrusted`). A failed or timed-out install leaves nothing pending | Installing behind the user's back: from a background check, a stray call, or over the lock screen. The token and the click check don't stop script in the page (residual risks) |
| On Linux the install target is the AppImage this process runs from: a frozen build whose executable is inside `$APPDIR` (`detect_target`) | Overwriting another program's AppImage when OpenArcanum was started from one (the runtime exports `$APPIMAGE` to every child) |
| Release keys and the manifest URL are build constants; the test override is environment-only and honored only for an update host on this computer; no setting or page script can change them | Pointing a copy at an attacker's host and key through the UI or a synced setting |
| Several trusted keys at once; manifests can carry several signatures (`docs/release.md` "Key rotation") | Rotating the release key without breaking updates for clients that know only one key |
| A build without release keys (`RELEASE_PUBLIC_KEYS = None`, as in this repository until the release key exists) makes no update request at all | Leaking IP addresses to an update host for manifests the build could never accept |

**Residual risks.**

| Risk | Mitigation / note |
|---|---|
| Until the signing certificates are configured, installers are unsigned (SmartScreen and Gatekeeper warn), so users can't tell a genuine download from a modified one by its signature | Configure the Windows and Apple signing secrets before a public release (`docs/release.md` "What the repository owner configures"). Auto-updates are protected by the manifest signature either way |
| Another local user can still create the single-instance name first where names are shared (Windows pipes; `/tmp` on Linux only when neither `$XDG_RUNTIME_DIR` nor the config folder can hold the socket) | The owner check refuses it: no link is sent and the app starts normally, but without single-instance hand-off, so a later launch with a link opens a second window on the same profile until the squatter goes away. Needs a malicious local account; it learns nothing |
| Two launches within the same moment can both start (each sees no running instance), with the profile open in two processes | Only on simultaneous starts (a double click and a link at once); the user sees two windows and can close one |
| Any program can open a `openarcanum://` link, so a page can make the Join dialog appear repeatedly | Only a dialog; the user closes it. No network request happens before Continue |
| A stolen release key can sign a malicious update that clients accept (after the user's consent, which they would give) | Keep the key offline, or in the `release` environment with required reviewers so that only an approved run's signing step can read it. Stored in CI it is still online: a compromise of GitHub, of a reviewer's account, or of the pinned tools and locked dependencies that step runs could reach it. Ship a client without the leaked key immediately; clients that never update past it stay exposed. A threshold of keys (requiring two signatures) would need every release signed twice; not done, noted for the external audit. |
| Script running in the page (an XSS that got past the textContent-only rule and the CSP) can read the prompt's token (`update_status` returns it to the page) and call the install API while the profile is unlocked; consent is asked in the page, which such script controls | It can only install the release that is already downloaded and verified: newer, signed by a release key, matching size and hash. It can't choose what is installed. A native (Qt) confirmation outside the page would close this; not done, noted for the external audit. |
| The update host sees each check: this computer's IP address, the time, that it runs OpenArcanum (the request carries no identifier and no version) | Documented next to the setting; "Check for updates automatically" can be turned off, after which the app contacts the host only when the user clicks "Check now". |
| A wrong local clock: far ahead refuses valid manifests as expired; far behind accepts expired ones | The error message says to check the clock. Expiry bounds freeze attacks only as well as the clock does. |
| The update host can withhold updates, or serve the newest manifest only to some users | Equivalent to the host being down; the manifest's expiry makes a stale view visible as an error. |
| The installer itself (Inno Setup, the `.dmg`'s app bundle, the AppImage) is trusted once its hash matches; OS code signing is separate | Windows Authenticode and macOS notarization add the OS's own check when the certificates are provided. |
| An AppImage in a folder the user can't write (e.g. `/opt`) or a distribution package can't update itself | The app reports the update as available with the reason and downloads nothing. |

## Server operations: logs, metrics, backups, deployment

**Asset.** Node logs, the status listener (health and metrics), backups of
community servers and maintainer nodes, the server binaries and images, and
the hosts they run on.

**Adversary.** Someone who reads a node's logs or scrapes its metrics (an
operator's monitoring provider, a log aggregator, a host admin); someone who
obtains a backup; an attacker who compromises a server process and tries to
reach the rest of the host; someone tampering with a restore input.

| Control | What it stops |
|---|---|
| No node logs content, names, labels, codes, keys, tokens, inbox addresses, search queries or passphrases; members, devices and servers appear as 6-byte fingerprints; maintainer errors log the route pattern, not the path. Tests drive typical traffic (and, across real binaries, a community chat and a DM) and scan the logs for every secret in several encodings (docs/operations.md "Logs, health and metrics") | Logs (and whoever collects them) becoming a copy of conversations, membership or credentials |
| Metrics are counts and sizes; label values are declared in code and checked at runtime (a request value can't become a label); route labels are mux patterns | Metrics revealing who talks to whom, topics, inboxes or identities |
| The status listener is off by default, separate from the public listener, documented for loopback | Health and metrics being reachable from the internet by default |
| Log files are created 0600, rotated by size and age with a bounded number kept | Logs readable by other local users; unbounded log growth filling the disk |
| Maintainer backups: restore accepts only the entry names a backup has (no links, no `..`), checks every file against the manifest and the database against the files, opens the node in a staging directory, and moves files into place only at the end; never restores over an existing node, refuses newer schemas | A crafted or damaged backup writing outside the data directory, or leaving a half-restored node |
| Community backups are age files (published format, filippo.io/age: scrypt recipient over the storage key, STREAM payload) written and read in bounded memory without holding the server's lock; restore stages everything and never replaces an existing storage key; maintainer backups hold only what the data directory holds (ciphertext, signed records, metadata), 0600 | A backup copy revealing message content; a backup or restore exhausting memory or stalling the server; a restore destroying the key that opens the server's other backups |
| The community container keeps the storage key in its own `/keys` volume, apart from `/data` and its backups | A copied data volume carrying the key that opens its backups |
| A maintainer restore moves the mailbox id sequence 2^32 past the backup and changes the mailbox epoch; clients reset positions on an epoch change | Clients silently skipping new mail because ids went back to ones the lost node had issued |
| A database newer than the binary is refused untouched | An older binary misreading or corrupting a newer schema after a downgrade |
| systemd units: a dynamic unprivileged user, read-only system, no capabilities, no new privileges, private /tmp and devices, syscall filter, state only in `/var/lib/openarcanum-*` (0700); `systemd-analyze security` exposure 1.1 (backup job 0.4), held under 2.0 by `make deploy-check` | A compromised server process reading or changing the rest of the host |
| Images: static binaries on a distroless base (no shell, no package manager), uid 65532, base images pinned by digest | The same inside containers; a moved image tag swapping the base |

**Residual risks.**

| Risk | Mitigation / note |
|---|---|
| Logs still carry metadata: timestamps, fingerprints of members who join, leave or act as admins, relay and peer URLs, upload ids and sizes, error texts | Needed to operate the node; documented. Operators who don't want even this can keep `log_level` at info and short journald retention |
| The status listener has no authentication; bound to a public address it tells anyone the community's size and activity | Off by default; documented to keep on loopback or a private network |
| Maintainer backups are not encrypted by OpenArcanum; they reveal inbox addresses, sizes and times (not content) to whoever holds them | Same exposure as the data directory; operators encrypt backup storage with their usual tools (restic, encrypted disks) |
| A backup restored on a maintainer node brings back mailbox items and blobs that were deleted after it was taken | Items stay ciphertext for their recipients and keep their original expiry times, so anything past its lifetime is deleted on the first expiry pass; restore only the newest backup |
