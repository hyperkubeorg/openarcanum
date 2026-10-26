# Community admin guide

For people who run a community: roles, permissions, channels, invites,
feature toggles, moderation, voice moderation, custom emoji and owners. Everything here is in
**Community Admin** (the red **Community Admin** button at the top of the
channel list, also in the community menu and the community icon's
right-click menu), in a member's menu (click a name in the member list) or in a
message's actions (hover a message). Every change is a signed
admin action recorded in the audit log, and the server checks every
permission itself: a hidden button is never the only control.

## Roles

- **Order.** Roles are listed highest first. You can only edit, delete,
  move, assign or remove roles that are **strictly below your own highest
  role**, and only act on members below you. Owners outrank every role.
  Move a role with **Move up** / **Move down** in **Roles**.
- **Permissions.** You can only grant or take away permissions you have
  yourself. Permissions you don't hold are greyed out in the editor.
- **Options.** Each role has a **color** (shown on member names), **Display
  separately** (members are grouped under the role in the member list,
  under their highest such role) and **Allow anyone to @mention this role**.
- **@everyone** is every member's base role. It can't be deleted or renamed.
- **Limit.** A community has at most 250 roles, @everyone included.

## Permissions

| Group | Permissions |
|---|---|
| General | View channels, Manage channels, Manage roles, Manage community, View audit log, Create invite, Change nickname, Manage nicknames, Approve join requests |
| Moderation | Kick members, Ban members, Time out members |
| Text | Send messages, Embed links, Attach files, Add reactions, Use custom emoji, Mention @everyone, @here and all roles, Manage messages, Read message history, Manage emoji |
| Voice and video | Connect, Speak, Video, Screen share, Use voice activity, Priority speaker, Mute members, Deafen members, Move members |
| Administrator | Everything, and channel overrides don't apply |

Notes:
- **Speak.** A member without Speak joins voice channels listen-only: the
  server gives them nowhere to send audio.
- **Use voice activity.** Without it, a member's microphone only sends
  while they hold their push-to-talk key, whatever their input mode (their
  voice bar says "push to talk only"). Calls are end-to-end encrypted, so
  the server can't check how a member opened their microphone: the app
  enforces this one, as Discord's does. Whether someone can be heard at all
  is Speak (and server mute), which the server and relay enforce.
- **Read message history.** Without it, a member sees only messages that
  arrive while they are connected.
- **Manage community** also lets you see and revoke every invite; **Create
  invite** lets a member create invites and see and revoke their own.
  Invites that grant roles need **Manage roles**; public join keys need
  **Manage community**. The community's **Relays** setting (automatic or
  pinned, and the pinned list) is **owners only**: the relays see every
  member's IP address, so Manage community alone can't move the community
  onto other relays. Relay changes are in the audit log.

### Permission defaults

New communities give `@everyone` a privacy-preserving set:

| On for @everyone | Off for @everyone (grant to a role) |
|---|---|
| View channels, Send messages, Read message history, Add reactions, Attach files, Embed links, Use custom emoji, Change nickname, Connect, Speak, Use voice activity, Video | Create invite, Mention @everyone, @here and all roles, Screen share, every Manage permission, View audit log, Approve join requests, Kick members, Ban members, Time out members, Priority speaker, Mute/Deafen/Move members, Administrator |

So by default only admins invite people and mention everyone. The founder
also gets an **Admin** role with Administrator.

## Channels and categories

- **Channels** are text or voice. **Categories** group them; channels
  without a category are listed first. In **Channels**, use **New
  category**, **Rename…**, **Delete** (its channels stay, without a
  category) and **Move up** / **Move down** to order categories and the
  channels inside each.
- **Edit…** a channel to rename it, set its **topic** (shown in the channel
  header), **slowmode**, **category**, retention, and its channel-level
  feature settings. Editing, deleting or setting retention on a channel
  needs Manage channels **in that channel**: a channel you can't see is out
  of your reach.
- **Slowmode** makes each member wait between messages. The dialog offers
  5 seconds to 6 hours; the server accepts any whole number of seconds up
  to 6 hours (21600). Send messages is still required; members who can manage messages
  or manage channels in that channel are exempt.
- **Voice channels have a text chat** (opened with **Open chat** beside the
  channel or **Chat** on the call stage). It is moderated like a text
  channel: Send messages, Read message history and Attach files apply, and
  **Edit…** on a voice channel offers slowmode, retention and the feature
  settings (no topic). AutoMod checks it and can exempt it; reports, pins
  and **Delete recent messages…** work there. Turning voice channels off for
  the community turns their chats off too (history stays).
- **Limits.** A community has at most 500 channels (text and voice
  together) and 50 categories; each channel or category has at most 100
  permission overrides.

### Channel permissions (overrides)

**Permissions…** on a channel or a category sets, for any role or any single
member, each channel permission to **Deny** (an X), **Inherit** (a slash) or
**Allow** (a check mark). The member's permissions in a channel are worked
out in this order:

1. their roles' permissions (@everyone plus every role they have);
2. the @everyone override;
3. the overrides of their roles: all role denies are applied, then all role
   allows, so if one of their roles denies something and another allows it,
   the allow wins (as in Discord);
4. their own member override (deny, then allow), which beats everything
   above.

Owners and members with Administrator bypass overrides. Without View
channels a member can't see the channel at all. Taking someone's access
away rotates the channel's key and topic, so they can't read anything new.
Setting overrides needs Manage channels and Manage roles **in that channel**
(or, for a category, under the category's overrides), so someone who can't
see a channel can't open it to anyone. You can only set overrides for roles
below your highest role (and @everyone) and members below you, and only for
permissions you have in that channel.

### Category sync

A channel created in a category, or moved into one while synced, copies
the category's overrides and stays **synced**: changing the category's
overrides changes its synced channels too. Editing a channel's own
overrides unsyncs it; **Sync permissions with the category** in its
**Edit…** dialog copies them again. Moving a channel out of its category
keeps its overrides but unsyncs it. Syncing, and moving a synced channel
into another category, replace the channel's overrides, so they follow the
rules for setting overrides above for every override they add, remove or
change: Manage channels alone can't do it.

## Invites

**Invites & join policy** lists invites with label, creator, uses (and max
uses), expiry, the roles they grant and whether they are active, expired or
revoked. **Revoke** stops one working immediately; **Raid revoke…** also
removes everyone who joined with it recently. Only **Create invite**
holders can create invites (it's off for @everyone by default).
A community has at most 1000 invites that aren't revoked (expired ones
count until you revoke them).

### Who can find and join

**Visibility** (listed in the directory or not) and the **Join policy**
(**Invite only**, **Request to join**, **Open**) work together:

- **Public + Open**: anyone who finds the community in directory search
  joins at once, with no invite or key. The server admits a join with no
  key only under this policy.
- **Public + Request to join**: anyone can find it and ask; each request
  waits in the **Approval queue**.
- **Public + Invite only**: it's listed, but joining needs an invite, or the
  **Public join key** if you made one (an invite created with **Public join
  key** ticked is published in the listing).
- **Private**: not listed, whatever the policy; only invite links reach it.

The **Visibility** page says what the current pair means (**Who can find
and join**), links to the join policy (**Change join policy…**) and offers
**Let anyone find and join**, which makes the community public and open in
one step after asking. The join policy form links back (**Visibility…**).
Both are changes to the community's settings: they need Manage community,
and the audit log records them.

## Feature toggles

**Features** turns community features on and off. The rule is always the
same:

1. **Off in Features**: the feature is unavailable everywhere. The server
   refuses it and the app hides it.
2. **On**: each channel's own setting applies (**Community setting** or
   **Off in this channel**) for attachments, link previews, reactions and
   new members seeing history.
3. **Allowed in the channel**: permissions decide who may use it.

| Toggle | Default | Notes |
|---|---|---|
| Attachments | on | Maximum size under **Files** (10 MB for new communities, up to 25 MB; the claim wizard's **Largest file (MB)** sets it at the start); allowed kinds: all, or images only |
| Link previews | off | The community server fetches the first link's preview, so linked sites never see members' addresses; senders need Embed links. A server operator can turn the fetcher off for the whole node (`link_previews_disabled`), and then it can't be turned on |
| Reactions | on | Needs Add reactions |
| Custom emoji | on | Off: nobody can upload or use them, and they are hidden (the emoji themselves are kept). The limit and the emoji are under **Emoji** |
| Voice channels | on | Off hides every voice channel, ends calls, and refuses creating or joining one |
| Video | on | Off: nobody's call grant includes video, and the call relay drops video from everyone in the call; turning it off during a call stops video in it |
| Screen share | on | Off: nobody's call grant includes screen share, and the call relay drops it; turning it off during a call stops screen sharing in it |
| Search | on | Off refuses every search |
| Member contact cards | on | Off stops members publishing DM keys here |
| Editing own messages | on | |
| Deleting own messages | on | Members with Manage messages can always delete |
| Typing indicators | on | Nothing is stored |
| New members see history | on | Off: members see only messages posted after they joined (owners and members with Manage messages see everything); a channel can also turn this off for itself |
| Join and leave messages | on | Posted in the channel chosen under **Join and leave messages go to** (default: the first text channel); kicks and bans don't post one |

Each change is in the audit log ("turned search off", "reactions off in
this channel").

## Moderation

### Permissions involved

| Permission | Lets a member | `@everyone` default |
|---|---|---|
| Kick members | remove a member (they can rejoin with an invite) | off |
| Ban members | remove a member's key for good, optionally deleting their recent messages and, on a dedicated relay, blocking their network address | off |
| Time out members | stop a member posting for a while | off |
| Manage messages | delete anyone's messages, pin, bulk delete, see and close reports | off |
| Manage community | AutoMod rules, member screening, the custom emoji limit (the custom emoji toggle is under **Features**) | off |
| View audit log | read the audit log (unless it is visible to everyone); with the log visible to everyone, only these members see AutoMod rules' keywords and patterns in it | off |
| Manage emoji | upload, rename and delete custom emoji | off |
| Use custom emoji | use the community's emoji in messages and reactions (per channel) | on |

You can only act on members whose highest role is **below** your own
highest role. Owners outrank everyone and can't be kicked, banned or timed
out; administrators can't be timed out, and only owners and other
administrators can act on an administrator, wherever their role sits.

You can only grant, take away, edit, delete or move a role if you hold
**every** permission it carries yourself, even when it sits below your
highest role. A moderator with Manage roles can't give anyone (themselves
included) a lower role that carries Ban or Administrator unless they have
it too, and an invite can only grant such roles under the same rule.

### Kick and ban

Member menu → **Kick…** or **Ban…** (also under Community Admin →
Members). A ban asks for a reason and whether to **delete message
history**: none, the last hour, 6 hours, 24 hours, 3 days or 7 days. The
server deletes every message the member posted in that period, in every
channel, for everyone (and from search). Banned keys are listed under
**Bans**, where you can unban them. Anyone can make a new key, so bans stop
casual abuse; invites you can revoke and the approval queue are the real
gate.

### Blocking a network address (dedicated relay)

An advanced option for communities that run on a **dedicated relay**: a
relay set up for your community only (its operator configures it with your
server's key; docs/operations.md "Dedicated relays"), with your community
pinned to it alone (Community Admin → Relays → pinned, that relay as the
only entry). Then everyone connects through it, and the ban dialog shows
**Also block their network address on our relay**, with how long: 1 day,
7 days, 30 days, or until they are unbanned. Only members with Ban Members
see it, and the server checks that permission again.

- **Nobody on your side sees the address.** The server asks the relay to
  block whatever address the member's connection comes from; the relay
  keeps the address and gives back an opaque block id. The audit log says
  "blocked the network address of NAME … (block ID)". The relay's operator
  can see addresses, as with any relay.
- **Not connected right now?** The block waits: the next time the banned
  member tries to connect through the relay, the server refuses them and
  the relay blocks the address they came from. The Bans list says
  "waiting for them to connect" until then, and "blocked until …"
  afterwards.
- **Unban lifts it.** The relay drops the block; if it was unreachable, the
  server asks again when it reconnects.
- **Shared addresses are blocked too.** Everyone behind the same home or
  office router, school, mobile carrier (carrier-grade NAT), VPN or Tor
  exit is blocked with them. Prefer short durations.
- **Easy to get around.** A VPN, Tor, mobile data or a new address from
  their provider gets them past it. It's a speed bump on top of the ban,
  invites and the approval queue, not a replacement.
- The relay never blocks the address your community server connects from
  (you'd see "their address is the community server's own network").

### Timeouts

Member menu → **Time out…**, pick 60 seconds to 28 days. A timed-out
member can still read, listen and report messages but can't post, edit,
attach files, use custom emoji, react, start typing or change their
nickname, avatar, about text or pronouns, and in voice they can't speak,
turn on their camera or share their screen (a call they're in turns
listen-only within seconds). A timeout takes away **every** permission
other than seeing channels, reading history and joining calls, so a
timed-out moderator can't kick, ban, time out, manage roles or change
settings until it ends. They see "You're timed out. You can post again at 14:32"
in place of the message box. Moderators (Time out members) see a
"timed out" tag in the member list; other members don't. **Remove timeout**
ends it early.

### Bulk delete

In a channel's menu (right-click the channel): **Delete recent messages…**
deletes the newest 1 to 100 messages; **Select messages to delete** (or
**Select** on a message) lets you tick up to 100 and delete them at once.
Either way it is one audit entry. Needs Manage messages in that channel.

### Replace member

Someone lost their keys and rejoined with a new identity? Community Admin
→ **Members** → **Replace member…** (needs Manage roles and Kick): paste the
**Old key** (from the audit log or member list) and pick the **New key
(current member)**. The old key's roles move over and the old key is
removed. Ask the person to prove who they are some other way first: anyone
can claim to be someone who lost their keys.

### Approval queue and raid revoke

**Approval queue** lists people waiting to join (invites marked
"needs approval", or the "request to join" policy). **Raid revoke** (on an
invite) revokes it and removes everyone who joined with it in the last N
hours.

### Member screening

Community Admin → Moderation → **Member screening**: a minimum
membership age from 10 minutes to 7 days (default off). Until it passes, a
new member can read but can't post, react, use custom emoji or start direct
messages with members; they see when they can. Members you've given any
role skip it.

### New-member restrictions

The `new_member_minutes` setting (0 to 7 days, default off): for that long
after joining, a member without a role can post plain messages but no
links, no mentions (of members, roles, @everyone or @here) and no files.
It works with or without member screening (screening first holds back
posting entirely). Members you've given any role skip it.

### Raid mode

The `lockdown` setting (Manage community): while it's on, **everyone** who
joins waits in the **Approval queue**, even with an invite or the public
join key, and nobody can create invites. Turn it on as soon as a raid
starts, remove the raiders (bulk ban below, or **Raid revoke** on the
invite they used), then turn it off; people still waiting stay in the
queue until you approve or deny them.

### Bulk kick and ban

`BulkRemove` kicks or bans up to 100 selected members in one action, and a
ban can delete their recent messages (the same windows as a single ban).
Every selected member is checked first (you must outrank each one, and
owners can't be removed), so if one can't be removed, nobody is. It's one
entry in the audit log.

These three are in the server and the protocol; until the desktop app
shows them, the setting fields and the action are what a client sends.

## Voice moderation

In a voice channel, the people in the call are listed under the channel and
on the call screen. Right-click a person or use **Options for …** (three
dots):

| Action | Permission | What happens |
|---|---|---|
| **Server mute** / **Server unmute** | Mute members | The call relay stops forwarding their audio (enforced by the relay, not only their app); others see the muted icon in red |
| **Server deafen** / **Server undeafen** | Deafen members | The call relay stops forwarding the call's audio to them; others see the deafened icon in red |
| **Move to…** | Move members | Moves them to another voice channel |
| **Disconnect** | Move members (as in Discord) | Removes them from the call; their old call grant stops working at the relay, and they can join again like anyone allowed to Connect |

You can only act on members below your highest role, as with any
moderation. Every action is in the audit log.

- **Priority speaker** (permission, off for @everyone): while such a member
  speaks, everyone else in the call is turned down to about a third in each
  listener's app. They show a megaphone icon.
- **Speak**, **Video** and **Screen share** are enforced by the call relay
  through the grant the community server signs: a member without them can
  join but can't send that kind of media, whatever their app does.
- **Use voice activity** is the exception: without it a member's app makes
  them use push to talk, but the server can't tell how an app decides when
  to send audio, so a modified app could ignore it (Discord enforces it in
  the app too). Use **Speak** to control who may talk at all.
- Timed-out members stay in calls listen-only (see "Timeouts").

## AutoMod

Community Admin → Moderation → **AutoMod** → **New rule…**. Each rule
looks for one thing:

- **Keywords and patterns.** Keywords match whole words or phrases,
  ignoring case (`spam` doesn't match "spammer"); `spam*` matches words
  starting with spam, `*spam` words ending with it, `*spam*` anywhere.
  Up to 1000 keywords. Regular expressions (RE2 syntax, case-insensitive,
  one per line, up to 10 of 260 characters each) are checked when you save:
  broken ones, ones that match every message and overly complex ones are
  refused with the reason.
- **Mention spam.** More than N different @mentions (members and roles)
  in one message (@everyone and @here count).
- **Links.** Any `https://…` or `www.…` link whose site isn't on the
  allowed list (subdomains included). An empty list blocks every link.

Then what it does (one or more): **block the message** (the sender sees
"AutoMod blocked your message" and the rule's name), **alert moderators**
(an entry in the report queue and a notice to everyone with Manage
messages), **time out the sender** for a chosen time. Exempt roles and
channels skip a rule. Nobody else is exempt: test a rule from your own
account. Rules apply to new messages, edits and file captions. Turning a
rule off keeps it for later.

## Reports

Any member can report a message (**Report** on the message) with an optional
reason of up to 500 characters. A member can report each message once and at
most 5 messages every 10 minutes. Members with **Manage messages** see the
queue under Community Admin → Moderation → **Reports** (also in the
community menu). Open reports and AutoMod alerts are counted on a red square
badge that leads there: on the community's icon, on the community name and
the **Community Admin** button, on **Community Admin** and **Reports** in
the community menu, and on the **Moderation** heading and **Reports** in
Community Admin (join requests do the same for members with **Approve join
requests**, ending at the **Approval queue**). The server sends these counts
only to members allowed to see each queue, live and when they connect. Each
new report or alert also gives a desktop notification that follows the
moderator's notification settings for the community. The queue shows who
reported, whose message in which channel, the reason, or for AutoMod alerts
the rule and what it did. **Jump to message** opens it in its channel;
**Resolve** or **Dismiss** closes the report (both are audited). "Show
resolved and dismissed" shows closed ones with who closed them. Reports
follow the community's retention.

## Audit log

Community Admin → Moderation → **Audit log** lists every admin action,
newest first, with who did it and whether its signature checks out
("server" marks entries the server records itself: AutoMod's own actions,
such as timeouts, and moderators deleting someone else's message or
pinning and unpinning one: who did it, whose message and which channel,
never the message's text). Filter by
**action**, **done by**, **done to** and a **date range** (YYYY-MM-DD,
inclusive, UTC), then **Apply filters**; **Load more** continues further
back. The filtering happens on the server. Who can read the log (admins
with View audit log, or every member) is set at the top of the section.

## Custom emoji

Community Admin → Content → **Emoji**.

- The **Custom emoji** toggle (on by default) is under **Features**. Off:
  nobody can upload or use them, and they are hidden.
- **Most custom emoji**: 1 to 250 (default 50).
- **Upload**: a name (2 to 32 of a to z, 0 to 9 and _, unique) and an image:
  type its path or use **Browse…**. PNG, GIF or WebP, animated is fine, up
  to 256 KB and 128×128 pixels.
- **Rename…** and **Delete** per emoji (Manage emoji).
- Uploading past **Most custom emoji** is refused with "this community
  already has N custom emoji, its limit"; delete one or raise the limit.

Members insert them with **Custom emoji** next to the message box and react
with them from a message's reaction menu (Use custom emoji); a message shows
`:name:id` as the image.

## Owners

A community's **owners** hold **owner keys**. The founder (whoever claimed
the server) is the first owner; the community's id is the founder's key and
never changes. Owner keys certify the community server's key, which is how
members' apps know they're talking to the real server. Owners have every
permission, outrank every role, can't be kicked, banned or timed out, and
can't leave the community while they are owners.

Each owner key stays on its owner's devices: it's made on the device that
accepted ownership, copied to that person's linked devices, and included in
their backups. **Keep your backup current**: it holds your owner key.

Everything is in Community Admin → **Ownership** (owners, members with
Manage community, and members invited to become an owner see it).

### Why you want a second owner

Any owner can **rotate the server key**, **move the community to new
hardware**, and **add or remove owners**. If the only owner key is lost,
the community keeps running, but nobody can do any of those again: if the
server's machine dies or its key leaks, members would have to start over in
a new community. So until a second owner exists, owners see a yellow
warning in Ownership and above the messages: "If you lose your owner key,
nobody can rotate the server key or move the community. Add a second
owner." with **Add an owner**. A community can have up to 16 owners.

### Add an owner

1. An owner: Ownership → **Add an owner**, pick an active member, **Invite
   as owner** ("Invitation sent."). **Cancel** withdraws the invitation.
2. The member sees "You've been invited to become an owner of this
   community." with **Review**; in Ownership, **Accept** (or **Decline**).
   Their device makes a new owner key and sends only its public half:
   "Owner key sent. An owner needs to confirm it."
3. Any owner: the pending row says "Their owner key arrived. Confirm to add
   it." → **Confirm** ("Alice is now an owner."). Their app signs the change
   and a new server certificate; members' apps check both.

### Remove an owner

Ownership lists every owner with their key, **founder** and **you** tags.

- **Remove** next to another owner: "Their owner key stops working for this
  community. They stay a member." The key can't sign anything for the
  community again and can never be added back (inviting the member again
  makes a new key).
- **Remove me**: "Your owner key stops working for this community. Another
  owner stays in charge." You lose owner rights at once; another owner's
  app completes the removal the next time it connects.
- The last owner can't be removed (**Remove** is disabled), so a
  community always has an owner, and a sole owner can't leave.
- A removed owner keeps no roles (the founder's **Admin** role included)
  and loses channels they could read only as an owner. Give them roles
  again like anyone else's if they should keep some.

### Rotate the server key

Ownership → **Rotate server key** → **Rotate…** → "Members reconnect to the
new key. Old invites keep working through the route." The server gets a new
key, certified by your owner key, and members' apps accept it on their own.
Do this if the server's key may have leaked (a stolen disk or backup
together with its storage key, for example).

### Move to new hardware

The server's operator and an owner do this together (operator details:
docs/operations.md "Moving to new hardware"):

1. Operator: copy the newest backup (`.fbk`) and the storage key file to the
   new machine and run `community restore -data DIR -backup FILE
   -storage-key KEY` (`KEY` is the path of the key file). It prints a
   **move code**. Start it with `community run`.
2. Any owner: Ownership → **Move to new hardware** → **Paste move code…**,
   paste it, **Adopt server**: "The new server is now this community.
   Members follow automatically." An owner added after the backup was taken
   can do this too.
3. Operator: stop the old server.

Until an owner adopts it, the restored server's key isn't certified, so
members' apps don't accept it and it publishes nothing. **Server backup** →
**Back up now** (owners only, at most once every 10 minutes, since each
backup prunes the oldest beyond `backup_keep`) writes an encrypted backup on the server
at any time; the server also backs up daily.

## Installing the servers

For whoever runs the machines: a community server (one per community), and
optionally your own relay or maintainer node. Reference for every setting:
docs/operations.md.

### Getting the binaries

Each release on https://github.com/hyperkubeorg/openarcanum/releases has
them ready: `openarcanum-servers-<version>-linux-amd64.tar.gz` and
`-linux-arm64.tar.gz`, checked against the release's `SHA256SUMS`
(docs/release.md "Verifying a download"). `relay version` (and `meta
version`, `community version`) prints the release a binary is.

From source, `make release-servers` builds static binaries (no C library needed) for
64-bit Linux on PCs and servers (`dist/servers/linux-amd64/`) and on ARM,
including a Raspberry Pi 3/4/5 with a 64-bit OS (`dist/servers/linux-arm64/`):
`relay`, `community` and `meta`. Copy the ones you need to
`/usr/local/bin/` on the target machine. Check a binary with
`file community` ("statically linked", "x86-64" or "ARM aarch64").

### Installing a community server

The community server only connects out, to relays and maintainer nodes.
It needs no open port, so:

- **At home**, behind your router: no port forwarding, and do **not** put
  the machine in the router's DMZ ("exposed host"), which would send every
  unsolicited connection from the internet to it for no benefit. A dynamic
  home address doesn't matter.
- **On a VPS**: allow outgoing traffic, deny incoming except SSH.
- Only a relay you run yourself needs ports: TCP 7000 (or 443 behind a TLS
  proxy) and UDP 7000 for call media; forward exactly those to the relay.

Run the guided setup, which explains the same and asks for the rest:

```
community init -interactive
```

It asks where the server runs (home or VPS), the data directory, relays
(picked automatically from maintainer nodes, or ones you name), maintainer
nodes (where the community's route and listing are published), where to
keep the storage key, and whether to turn on a local health and metrics
endpoint. It then prints the claim code: in the app, **+ (Add a community)
→ Host on your own server** and paste it under **3. Claim it**. That page
also saves the community server program itself (**1. Copy the community
server**), for the system the app runs on. The same with flags, for scripts:

```
community init -data /var/lib/openarcanum-community -relay wss://relay-01.openarcanum.org/ws \
    -maintainer https://maintainer-01.openarcanum.org -install vps
```

**Back up the storage key** (`storage.key`, or wherever you put it) away
from the machine right away: the server's backups can't be read without it.

### Running the servers with systemd

`deploy/systemd/` has a unit per server, each running as its own throwaway
system user (`DynamicUser`) with a read-only view of the system, no
capabilities and its data in `/var/lib/openarcanum-*` (mode 0700):

```
install -m 0755 community /usr/local/bin/
cp deploy/systemd/openarcanum-community.service /etc/systemd/system/
systemctl daemon-reload
systemd-run --pty --wait -p DynamicUser=yes -p User=openarcanum-community \
    -p StateDirectory=openarcanum-community \
    /usr/local/bin/community init -interactive -data /var/lib/openarcanum-community
systemctl enable --now openarcanum-community
journalctl -u openarcanum-community -f
```

(`systemd-run` creates the server as the service's own user, in its state
directory.) To keep the storage key off the data disk, move it to
`/etc/openarcanum/credentials/community-storage.key` and install
`deploy/systemd/openarcanum-community-storage-key.conf` as a drop-in: systemd
then hands the key to the service as a credential.

Relay and maintainer node: copy `deploy/config/relay.json` or `meta.json` to
`/etc/openarcanum/`, replace the `example.invalid` names with yours, install
`openarcanum-relay.service` or `openarcanum-meta.service`, and for a
maintainer node also `openarcanum-meta-backup.service` and `.timer` (daily
backup into `/var/lib/openarcanum-meta/backups`; copy them off the machine, or
let `openarcanum-meta-backup-sync.service` do it once its remote is
configured).

journald keeps and rotates the logs (`journalctl -u openarcanum-meta`). Health:
`curl http://127.0.0.1:7101/healthz` when `metrics_listen` is set (the
example configs set it on loopback).

### Container images

Each release publishes one image per server on GitHub's container
registry, for amd64 and arm64, signed with cosign (docs/release.md
"Container images"):

| Server | Image |
|---|---|
| Relay | `ghcr.io/hyperkubeorg/openarcanum-relay` |
| Maintainer node | `ghcr.io/hyperkubeorg/openarcanum-maintainer` |
| Community server | `ghcr.io/hyperkubeorg/openarcanum-community` |

Tags: the release (`2026.10.0`, the safest to pin), the month's newest
release (`2026.10`), `latest` (the newest final release; betas never move
it) and `sha-<commit>`. Each runs as an unprivileged user (uid 65532) on a
minimal base with no shell. To build one yourself instead,
`docker buildx build -f deploy/container/Containerfile --target community
-t openarcanum-community .` (targets `relay`, `meta`, `community`).

```
docker pull ghcr.io/hyperkubeorg/openarcanum-community:2026.10.0
docker tag ghcr.io/hyperkubeorg/openarcanum-community:2026.10.0 openarcanum-community
docker volume create openarcanum-community
docker volume create openarcanum-community-keys
docker run --rm -it -v openarcanum-community:/data -v openarcanum-community-keys:/keys \
    openarcanum-community init -interactive -data /data
docker run -d --name community --restart unless-stopped \
    -v openarcanum-community:/data -v openarcanum-community-keys:/keys:ro openarcanum-community
```

The community image keeps the storage key in its own volume, `/keys`
(`OPENARCANUM_COMMUNITY_STORAGE_KEY_FILE=/keys/storage.key`), apart from the
database and the backups in `/data`: a copy of the data volume, or of its
`backups/`, then doesn't carry the key that opens them. Copy the key off
the machine once after `init` (for example `docker run --rm -v
openarcanum-community-keys:/keys busybox cat /keys/storage.key`, into your
password manager). Instead of a volume, the key can be supplied as a
secret file mounted at `/keys/storage.key` (Docker or Podman secrets,
Kubernetes secrets); the server only reads it.

The community image runs `community run -data /data` with its health
endpoint on `127.0.0.1:7201` inside the container (`docker ps` shows
"healthy"). The relay image listens on 7000 (publish `-p 7000:7000/tcp -p
7000:7000/udp`), the maintainer image on 7100 with its data in `/data`.
Configure them with `OPENARCANUM_*` environment variables
(`-e OPENARCANUM_RELAY_PUBLIC_URL=wss://relay.example.invalid/ws`). Limit the
container log size with your runtime (`--log-opt max-size=50m`).

### Upgrading and backups

Take a backup, replace the binary or image, restart. A node refuses a
database written by a newer release (with a message saying so), so going
back means restoring the backup taken before the upgrade. Maintainer admin
commands (`meta directory`, `meta relays`, `meta blobs`, `meta backup`)
never upgrade a database: between installing a new release and restarting
the node they refuse to run ("run this command with the node's own
release"), so restart the node first. Community servers back themselves up
daily without pausing (`community backup` on demand); a backup is an age
file the stock `age` tool can open with the storage key as passphrase.
`community restore` never overwrites an existing storage key file and
leaves the target directory untouched if it fails. Maintainer nodes back
up with `meta backup` (the timer, or by hand) and restore with `meta
restore` onto a new machine; running clients pick up a restored node's
mailboxes on their own. Details: docs/operations.md "Backups".

### Removing a relay from your maintainer node's registry

`meta relays remove -data DIR -url URL` drops the relay and refuses its
heartbeats from then on. The removal covers the relay's whole host, in any
spelling (`wss://RELAY.example:443/ws/` is the same relay as
`wss://relay.example/ws`) and on any port or path; `meta relays restore`
with any URL on that host lifts it. A relay under a different host name is
a different relay.
