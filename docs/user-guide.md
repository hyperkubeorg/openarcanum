# OpenArcanum user guide

How to do things in the OpenArcanum client. Admin tasks (community settings,
roles, moderation) are marked **Admins**; the full admin guide is
`docs/admin-guide.md`. Server operators: see `docs/operations.md`.

Labels in **bold** are the exact text on screen. Buttons that show only an
icon are named by their tooltip (hover to see it): the padlock is **Lock
now**, the gear is **Settings**, the two speech bubbles at the top of the
left rail are **Direct messages**, the "i" in a circle is **Conversation
details**.

## Contents

- [Your profile: first run, unlock and backups](#your-profile-first-run-unlock-and-backups)
- [Communities: create, join, leave](#communities-create-join-leave)
- [Messages in a community](#messages-in-a-community)
- [Members and profiles](#members-and-profiles)
- [Your status](#your-status)
- [Direct messages and group DMs](#direct-messages-and-group-dms)
- [Files in direct messages](#files-in-direct-messages)
- [Safety numbers, key changes and blocking](#safety-numbers-key-changes-and-blocking)
- [Your devices](#your-devices)
- [Voice, video and screen share](#voice-video-and-screen-share)
- [Notifications](#notifications)
- [The tray icon, and closing the window](#the-tray-icon-and-closing-the-window)
- [Keyboard and screen readers](#keyboard-and-screen-readers)
- [Settings at a glance](#settings-at-a-glance)
- [Admins: voice moderation](#admins-voice-moderation)
- [Admins: messaging features](#admins-messaging-features)
- [Where your data is](#where-your-data-is)
- [Logs and crashes](#logs-and-crashes)
- [Updates](#updates)

## Your profile: first run, unlock and backups

There are no accounts. Your keys live in an encrypted **profile** on each of
your devices, opened with a passphrase that only that device knows. Nobody
can reset it for you, so read "Forgot your passphrase?" and "Back up your
keys" below before you need them.

### Create a profile

The first start shows **Welcome to OpenArcanum** with three choices:

- **Create a profile**: a new profile with new keys. Pick a
  **Passphrase**, type it again under **Repeat passphrase**, and check
  **Name this device** (shown in your device list; "Linux device",
  "Windows device" and so on by default). The passphrase needs at least 10
  characters and at least "Fair" on the meter under the field; whole
  sentences of ordinary words work well, or press **Generate** (see
  "Generate a passphrase"). **Create profile** opens the app.
- **Link to an existing device**: this computer joins a profile you
  already have on another device (see "Your devices").
- **Restore from backup**: sets the profile up from a backup file (see
  "Restore from a backup").

**Dark**, **Light** and **System** at the top of this screen (and of the
unlock, restore and link screens) switch the theme; the choice is saved at
once and is the same as Settings → **Appearance** → **Theme**. OpenArcanum
starts in **Dark**. **System** follows your desktop's light or dark setting
when the desktop tells apps about it (through the desktop settings portal,
which GNOME, KDE and most full desktops run); when it doesn't, for
example in a container that only shares the display, or under a bare
window manager, **System** shows the light theme.

### Generate a passphrase

Next to every new passphrase (creating a profile, restoring, linking a
device, **Change passphrase…** and **Export backup…**), **Generate** makes a
random one: six words, such as `glider unstuck pelican mumble tidy
sesame`. It appears in the field, readable (**Hide** covers it again,
**Show** shows any passphrase you typed), and the **Repeat** field is
filled in for you. **Copy passphrase** puts it on the clipboard: save it in
your password manager before you go on. There is no reset.

Why words: six words drawn at random from the EFF's 7,776-word list are
about 77 bits, beyond guessing even by someone who copied your encrypted
profile or backup file and tries passphrases offline, and they are easy to
read off one screen and type on another device when the password manager
isn't there. OpenArcanum generates them itself (in the app, with the operating
system's secure random numbers); nothing is sent anywhere. The word list is
"EFF's Long Wordlist" by the Electronic Frontier Foundation
(https://www.eff.org/dice), under CC BY 3.0 US.

### Unlock, lock and auto-lock

After you create, restore or link a profile, this device **stays signed
in**: the next time OpenArcanum starts it opens straight to your communities,
without the passphrase (see [Staying signed in](#staying-signed-in) for
what that means and how to turn it off). Otherwise, and after a lock, the
**Unlock** screen asks for the passphrase. After a wrong one the button
shows **Wait 1 s**, and each further wrong try waits longer.

- **Lock now** (the padlock, bottom left, or Settings → **Security** → **Lock
  now**) locks at once. Locking hides everything and removes your identity
  keys from memory. Messages keep arriving while locked; notifications then
  say only "New activity". A locked OpenArcanum needs your passphrase to come
  back, also if you quit and start it again; once you enter it, the device
  stays signed in again.
- **Settings → Security → Lock after idle** locks OpenArcanum when you
  haven't used it for a while: 1 minute to 4 hours, or **Never**
  (default **Never**, so OpenArcanum stays signed in). Moving the mouse or typing in the window counts
  as use. An idle lock is a lock like **Lock now**: afterwards your
  passphrase is needed again, also after a restart.
- **Settings → Security → Change passphrase…** asks for the current one
  and the new one twice (or **Generate**). It changes this device only:
  each device has its own passphrase.

### Forgot your passphrase?

On the unlock screen, **Forgot your passphrase?** explains the ways back.
There is no reset:

- **Link again from another device**, if one of your other devices is still
  unlocked.
- **Restore from a backup file**, if you have the file and its passphrase.
- **Start a new profile**: your old communities won't know the new keys, so
  their admins would have to re-admit you.

**Set this profile aside and start over…** moves the old profile aside
(it's renamed, not deleted, so a passphrase you remember later can still
open it) and returns to the first-run screen.

### Back up your keys

A backup is the only way to get your keys back if you lose every device.
**Settings → Security → Backup** says **Up to date** or **Out of date** and
when you last exported one.

**Export backup…** opens one dialog:

1. **Backup passphrase** and **Repeat backup passphrase**. Use a different
   passphrase from your profile's (the same rules apply: at least 10
   characters, at least "Fair"); **Generate** makes one. Keep it in your
   password manager, not next to the backup file.
2. **Save to**: the file, `openarcanum-backup.fbak` in your downloads
   folder by default (**Browse…** picks another place). Keep a copy
   somewhere other than this computer.
3. **Your passphrase**: your profile's own passphrase, to confirm it's you.
   A wrong one is refused and, like a wrong passphrase on the lock screen,
   makes the next try wait a little longer.

**Save backup** writes the file.

The backup holds your keys (including any owner keys) and your list of
communities, with how to reach each one. It holds **no messages**:
community messages download again from the servers (within each
community's retention), and direct-message history isn't in it at all
(only linking a device copies it).

The backup goes **Out of date** whenever your keys change: when you join a
community or become an owner, for example. OpenArcanum then shows "Your backup
is out of date." above the messages, with **Export backup**, and offers
**Back up your keys** after you join a community.

### Restore from a backup

On a device with no profile: **Restore from backup** → **Choose file…** (or
paste a recovery backup, `openarcanum-backup:…`, into the same field),
then the **Backup passphrase**, a **New passphrase for this device** and
**Name this device** → **Restore**. You get your keys and communities back,
and this device makes its own device keys. Messages come back from the
community servers while still within their retention. "Wrong backup
passphrase." means just that; nothing is changed.

### Staying signed in

A profile you create, restore or link on this device stays signed in
here: OpenArcanum keeps the key that opens your profile on this computer, so
it starts without asking for your passphrase. Only this device is
affected; your passphrase still works, and it still protects your backups,
your other devices and any copy of your data folder.

Where the key is kept, and who could use it:

- **In this computer's keychain** (Windows Credential Manager, the macOS
  Keychain, or your Linux desktop's keyring: GNOME Keyring, KWallet).
  Anyone who can use your account on this computer can open your profile.
- **In a file**, on a system with no keychain (some minimal Linux desktops,
  containers): `unlock-key` in OpenArcanum's settings folder, readable by your
  user account only. On such a system your profile opens without a
  passphrase for anyone who can read your user's files (another
  administrator, a backup of your home folder, malware running as you).
  Settings → **Security** says so while this is the case.

**Settings → Security → Ask for my passphrase when OpenArcanum starts** turns
this off. It asks for your passphrase, deletes the kept key and
re-encrypts your profile file, so a copy of the old key (say, from a
keychain or home-folder backup) no longer opens it. Turning it off again
(passphrase, **Stay signed in**) keeps a key again.

**Lock now** and **Lock after idle** delete the kept key too: after a lock,
the passphrase is needed, also after a restart, and entering it signs you
in again.

If the kept key can't be used (the keychain didn't answer, or the key no
longer fits because the profile file changed), OpenArcanum says so and asks
for the passphrase, which always works. If the key is in the keychain, the
unlock screen also offers **Unlock with this device's keychain** to try
again.

### Maintainer nodes

Maintainer nodes run the public directory, list relays, hold DM mailboxes
and pass device-link codes. OpenArcanum comes with a built-in list; you can
use your own (a self-hosted network, or a local test network).

You can change the list **before unlocking**, at the bottom of the
**Create your profile**, **Unlock**, **Restore from backup** and **Link to
an existing device** screens: the **Maintainer nodes** line shows the nodes
in use ("Maintainer nodes: 127.0.0.1:7100", or "3 maintainer nodes") and
where they come from (**your own list**, **from the launch option** or
**the defaults**). **Change…** opens the list:

- **Remove** takes a node out of the list.
- Type an address under **Add a node** and press **Add** (or Enter). It
  must start with `https://`; plain `http://` is accepted only for a node on
  this computer. Up to 8 nodes. An address that can't be used is refused
  under the field with the reason, for example "Use https:// (http:// only
  for this computer)."
- **Use the defaults** empties your own list: OpenArcanum then uses the nodes
  given with the `--maintainer` launch option, if any, else the built-in
  list.
- **Save** keeps the change and uses it at once; **Cancel** (or Escape)
  changes nothing.

**After unlocking**, the same line and **Change…** are in Settings →
Network.

**Without a public directory.** Maintainer nodes have no public
community list unless the people who run them turn one on, so usually you
join through an invite someone shares with you (a message, a website, a QR
code). When no node you use has a directory, **Add a community** doesn't
offer **Find a community**, and a community's **Visibility** (and the setup form) offers only private
communities, with the note "Public listing isn't available on your
maintainer network; share invites to let people join." Invites work the
same either way.

**Maintainer networks.** Some maintainer nodes belong to a network of
nodes, run by people who trust each other, that copy DM mailboxes between
them, so your mail still arrives when one of them is down. When you add
such a node, the editor says so under the field and shows the network's
members and a **fingerprint**: twelve characters to check with the people
who run it. **Use the network** keeps that fingerprint with the node (the
entry shows a "network …" pill) and OpenArcanum then uses every member; **Only
this node** uses just the node you typed. In Settings → Network each
network you use is listed with its members, a dot for each (green:
reachable; red: not reachable, OpenArcanum tries again later; grey: not tried
yet) and the number of its member list, which OpenArcanum keeps current by
itself. **Use this network's other nodes** (on by default) switches back to
the nodes in your list only.

The list is kept **per device**: it isn't copied to your other devices,
linking a device doesn't bring it over, and changing it on one device
leaves the others as they were. When you link a new device, set its list
first (on the link screen) if your other device uses its own nodes. The
list is saved unencrypted beside the appearance settings, so it's readable
before unlock; anyone who can read your OpenArcanum folders can see which
maintainer nodes this device uses (not what you do with them).

## Communities: create, join, leave

**Add a community** (the plus at the bottom of the left rail) opens a page
in the middle of the window (where the messages are; the **×** at its top
right or Escape closes it) with four ways in: **Join with an invite**,
**Find a community**, **Host on this computer** and **Host on your own
server**. The arrow keys move between them.

### Host a community on this computer

The simplest way to start a community: OpenArcanum runs its server for you, in
the background.

1. **Add a community** → **Host on this computer**.
2. Fill in **Set up your community** (below) and press **Create community**.
   OpenArcanum sets up a community server in your profile's data folder,
   starts it, and claims it for you: there's no code to copy.
3. **Your community is ready** shows the first invite. **Export backup…**:
   the backup holds your owner key.

The server runs whenever OpenArcanum runs: also while its window is closed to
the tray (see "The tray icon, and closing the window") and before you
unlock it. Members reach it through relays, so there's no port forwarding
and nothing to open on your router. If a relay it uses goes down, it moves
to another one by itself, and members' apps follow.

- **While this computer is off, or OpenArcanum isn't running, the community is
  offline.** Members see "Offline · last seen …" on its icon and above its
  messages; it comes back by itself when OpenArcanum starts again.
- **After 7 days offline, the maintainer nodes shut it down**: members see
  "This community was shut down", and it disappears from directory search.
  Your data isn't lost: start OpenArcanum and choose **Bring it back online**
  (the banner, or the community menu) to register it again; members
  reconnect by themselves.
- **The community menu** says **Hosted on this computer: Running** (or
  Stopped, Restarting…), with **Stop hosting** and **Start hosting**. A
  stopped community stays stopped when OpenArcanum starts again. If the server
  stops unexpectedly, OpenArcanum starts it again after a moment (waiting
  longer each time, up to a minute).
- **Quitting OpenArcanum** (the tray menu's **Quit OpenArcanum**, Ctrl+Q, or
  closing the window without a tray) first asks, because your hosted
  communities go offline until you open it again. Closing the window to the
  tray keeps them up.
- Its files: Settings → **Data & storage** → **Communities hosted on this
  computer** (the server's keys, database and backups; keep a copy
  elsewhere: if this folder is lost, so is the community) and **Hosted
  community logs**.

For a community that should always be up, host it on your own server.

### Host on your own server (claim a server)

A community can also run on a server that you (or someone you trust) keep
running, such as a small VPS or a Raspberry Pi
(`docs/admin-guide.md` "Installing a community server").

1. **Add a community** → **Host on your own server**.
2. **1. Copy the community server**: **Save the community server** saves the
   same program OpenArcanum uses, for this computer's system, where **Save to**
   says (**Browse…** picks the place). Copy it to the server. For another
   system, use the server release for it.
3. **2. Set it up and start it**: on the server, run the two commands shown
   (with **Copy**). `community init` prints a one-time **claim code**
   (`openarcanum-claim:…`); `community run` keeps the community up.
4. **3. Claim it**: paste the claim code into **Claim code**, **Next**, fill
   in **Set up your community** and press **Claim and create**.

Once you own a community on a server, that server can run more: **Add a
community** → **Host on your own server** → **On one of my servers**, pick
the community whose server should run the new one, fill in **Set up your
community** and press **Create community**. No claim code: the server makes
the community and it's yours at once. Each community on it has its own
members, invites, settings and backups; the server's operator sets how many
it runs (`max_communities`, 10 by default).

### Set up your community

Both ways of hosting ask for the same things:

1. **Set up your community**: **Name**, **Description**, **Tags**
   (comma-separated, for directory search), **Visibility** (**Private
   (invite links only)** or **Public (listed in the directory)**), **Join
   policy** (**Invite only**, **Request to join (approval queue)** or
   **Open (anyone who finds it can join, no invite needed)**; the line under
   them says who can find and join with that pair), **Keep messages
   (days)** (30) and **Keep files (days)** (7) (0 keeps them forever),
   **Largest file (MB)** (10, up to 25), and **Your display name**.
2. **Create community** (or **Claim and create**). **Your community is ready** shows a first invite
   (it works for 7 days; make more in Community Admin → **Invites & join
   policy**) in a box with **Copy invite**, and **Export backup…**: do it, because
   the backup holds your owner key.

A claim code works once: a second try says "this server has already been
claimed". Whoever claims the server owns the community. Until there's a
second owner, a banner above the messages warns that losing your owner key
would leave nobody able to rotate the server key or move the community;
**Add an owner** leads to the fix (`docs/admin-guide.md` "Owners").

### Join with an invite

An invite can arrive as a link, `openarcanum://invite/…`. Click it (in a chat,
mail or the browser, which may ask whether to open OpenArcanum) and OpenArcanum
opens with **Add a community → Join with an invite** already filled in; if it
was closed, unlock first and the page appears then. If OpenArcanum is already
running, its window comes to the front instead of a second one starting.
Nothing happens until you press **Continue** (which shows the community) and
**Join**, so a link alone never joins you anywhere. You can also paste the
link, or a `openarcanum-invite:…` code, into that page yourself.

On Windows and macOS the installer (or dragging OpenArcanum to Applications)
registers `openarcanum://` links. **On Linux an AppImage registers nothing by
itself.** Either let a desktop integration tool do it (AppImageLauncher,
Gear Lever or appimaged add OpenArcanum to your applications, links included),
or run these commands once in a terminal, in the folder that holds the
AppImage (use its real file name; run them again if you move it):

```sh
app="$PWD/OpenArcanum-x86_64.AppImage"
"$app" --appimage-extract openarcanum.desktop
mkdir -p ~/.local/share/applications
sed "s|^Exec=.*|Exec=$app -- %u|" squashfs-root/openarcanum.desktop > ~/.local/share/applications/openarcanum.desktop
rm -r squashfs-root
xdg-mime default openarcanum.desktop x-scheme-handler/openarcanum
```

Until then, copy the link and paste it into **Add a community → Join with an
invite**: it works the same.

### Find a community

Only when one of your maintainer nodes runs a public directory (they
don't by default). **+ → Find a community** searches it by name, tag
(`#bread`) or description. Private communities never appear there. Each
result says whether its server is **Online**, **Offline · last seen …** or
online with some of its relays down. Click a result to see it; if its join
policy is **Open**, the page says "This community is open: anyone who finds
it can join, no invite needed", and **Join** lets you straight in.

### When a community is offline or shut down

The maintainer nodes keep track of whether each community's server is
reporting in, and OpenArcanum shows it when it can't reach a community:

- **Offline · last seen 3 hours ago**: the server isn't running (its
  computer is off, or it was stopped). OpenArcanum keeps trying and reconnects
  by itself when it's back.
- **Its server reaches 1 of its 2 relays**: up, but some of its relays are
  down.
- **Offline: its server is up, but none of its own relays can be reached**:
  the community uses only relays its owners chose (pinned, or a relay of its
  own), so it doesn't move to others; it's back when they are.
- **Shut down**: its server reported nothing for over 7 days, so the
  maintainer nodes shut it down. OpenArcanum checks now and then whether its
  owner brought it back; **Leave community** removes it from your list.

When a relay goes down, a community whose server picks its relays
automatically moves to another one, and OpenArcanum follows it without you
doing anything.

### Make a community anyone can find and join (Admins)

Two settings decide it together:

| Visibility (Community Admin → **Visibility**) | Join policy (**Invites & join policy**) | Who gets in |
|---|---|---|
| **Public** | **Open** | Anyone who finds it in search joins straight away. No invite or key to share. |
| **Public** | **Request to join** | Anyone can find it and ask; an admin approves each request (**Approval queue**). |
| **Public** | **Invite only** | Listed in search, but joining needs an invite (or a **Public join key**, below). |
| **Private** | any | Not listed; only people with an invite link. |

The **Visibility** page shows what the current pair means under **Who can
find and join**, with **Change join policy…** (it opens **Invites & join
policy**) and, unless the community is already public and open, **Let
anyone find and join**, which sets both at once after asking. The **Join
policy** form has a **Visibility…** link back. A **Public join key** (an
invite marked public when you create it) is only needed to let anyone in
while the policy stays **Invite only**.

By default the search goes to this device's maintainer nodes (see
"Maintainer nodes"),
which see what you search for (they promise not to log it). To keep
searches on your device, turn on **Settings → Network → Search the
directory on this device**: OpenArcanum then downloads a copy of the whole
directory (about 1 KB per community, every entry checked against its
community's signature) and searches that copy. The Find pane says where
results come from and how old the copy is; it refreshes itself after an
hour, or press **Refresh copy**. If the directory is larger than OpenArcanum
keeps (50 000 communities or 64 MB), the pane says the copy is partial.

### What happens when you join

**Continue** (or clicking a search result) shows **Join** and the
community's name, with its member count, join policy, description and tags, and the notice
"Admins of this community can read everything you post here." Under
**Identity**, **A new identity (recommended)** means nobody can connect
this community to any other you're in; you can instead reuse an identity,
and the dialog lists which communities could then link you. Type your
**Display name in this community** and press **Join**.

- In an **Invite only** community you need an invite; without one the
  server refuses ("This community needs an invite to join.").
- With **Request to join**, you see "Pending approval": the community
  appears with "Pending approval: an admin has to let you in. This updates
  by itself." and opens once an admin approves you.
- A revoked or expired invite, a full community or a ban is refused with
  the reason in the dialog.

After joining, OpenArcanum reminds you that your backup is out of date (**Back
up your keys**, **Later** or **Export backup…**).

### Arrange your communities

Drag a community's icon up or down the left column to move it. Drop it on
another community to put both in a folder, or on a folder to add it. Click
a folder to open or close it. Right-clicking a community also offers **Move
up**, **Move down**, **New folder**, **Add to folder** and **Remove from
folder**; right-clicking a folder offers **Rename folder…**, **Move up**,
**Move down** and **Ungroup**. The arrangement is kept on this device.

### Leave a community, or delete its local data

- **Leave**: click the community name (or right-click its icon on the
  left) → **Leave community** (or Settings → **Communities** → **Leave…**). "You stop receiving messages and this
  device deletes its copy of the community." **Also delete my key for this
  community** is ticked: without the key you can't come back as the same
  member, only as a new one. Owners can't leave while they are owners
  (`docs/admin-guide.md` "Owners").
- **Delete local data** (Settings → **Communities**): deletes the messages
  and files from that community cached on this device. They download again
  from the server while still within its retention. You stay a member.
- **Mute community** (community menu, or right-click its icon) stops its
  notifications; see "Notifications".

### The community menu and the icon's right-click menu

Click the community's name at the top of the channel list, or right-click
its icon on the left (or focus the icon and press the **Menu** key or
**Shift+F10**):

- **Notifications**: **All messages**, **Mentions only** or **Nothing** for
  this community's desktop notifications (a check mark shows the current
  one); see "Notifications".
- **Mute community** / **Unmute community**, and **Mark all as read**
  (clears every channel's unread marks).
- **Invite people** (members who may create invites).
- **Community Admin**, **Reports**, **Approval queue** and **Audit log**
  (admins and moderators; each only with the permission it needs).
- **Search messages** (the community you're in: words of three letters or
  more also match as the start of a word, shorter ones only whole words;
  the first five words count), **Your profile here**
  (your name, nickname, pronouns and avatar in this community) and **Copy
  community ID**.
- **Leave community**, after a confirmation.

### Community Admin (admins and moderators)

Members with an admin or moderator permission (Manage community, channels,
roles or messages, Kick, Ban, Time out, Manage nicknames or emoji, View audit
log, Approve join requests; owners) see a red **Community Admin** button at
the top of the channel list. It opens the community's administration, on
the first section you can use, or straight on **Reports** or the **Approval
queue** when something waits there. **Community Admin** is also in the
community menu and the icon's right-click menu. Your own name, nickname and
avatar here are under **Your profile here**, not in Community Admin.

Community Admin opens in the middle of the window like Settings: titled
**Community Admin** and the community's name, with its sections down the
left (and that community's channels still in the channel list). Opened from
another community's icon, that community opens first. **Close** (top right)
or **Escape** goes back to the channel; clicking a channel, a community or
**Direct messages** leaves it too.

### Reports and join requests waiting (moderators)

When a member reports a message, AutoMod alerts the moderators, or someone
asks to join, a red square badge with the count leads you to it:

1. on the community's icon on the left (top left of the icon);
2. on the community's name at the top of the channel list, and on the red
   **Community Admin** button;
3. in the community menu, on **Community Admin**, **Reports** and
   **Approval queue**;
4. in Community Admin, on the **Moderation** and **Joining** headings and on
   **Reports** and **Approval queue**.

Reports and AutoMod alerts count for members with *Manage messages*, join
requests for those with *Approve join requests*; the server sends each count
only to members allowed to see that queue. The badges clear as reports are
resolved or dismissed and requests approved or denied. A new report or
AutoMod alert also gives a desktop notification, following the community's
notification settings (not when it's muted or set to **Nothing**).

### Removed, offline or blocked

- If you were removed, banned, or this device was removed, the community
  shows why, with **Dismiss**; in Settings → Communities it then offers
  **Remove from list**.
- When a community is offline, its banner and Settings → **Network** offer
  **Reconnect**. "The community's relay refuses connections from your
  network" means the community runs its own dedicated relay and it blocked
  the address you connect from: after a ban there, or because someone else
  on your network (the same router, mobile carrier, VPN) was banned with a
  network block. Only the community's moderators can lift it.

### When OpenArcanum or a server is out of date

OpenArcanum and the servers it talks to agree on a protocol version each
time they connect; each side speaks its own version and the one before.
If they have none in common you'll see one of:

- **"Update OpenArcanum: …"**: the relay, community server or maintainer
  node is newer than this app. Install the update (Settings → Updates).
- **"This relay / community server / maintainer node is outdated …"**:
  the server is too old for this app; its operator needs to update it.
  OpenArcanum keeps trying other relays meanwhile.

The message shows in the community's status line and in the Join dialog.

## Messages in a community

### Format a message

Type Markdown in the message box. Press **Enter** to send and
**Shift+Enter** for a new line.

| You type | You get |
|---|---|
| `**bold**` or `__bold__` | **bold** |
| `*italic*` or `_italic_` | *italic* |
| `~~strike~~` | ~~strike~~ |
| `` `code` `` | inline code |
| ```` ```python ```` … ```` ``` ```` on their own lines | a code block labelled "python" (no colouring) |
| `> quoted` | a block quote |
| `- item`, `* item` or `1. item` | a list |
| `# Title`, `## Title`, `### Title` | a small heading |
| `[label](https://example.org)` | "label (https://example.org)" |
| `||spoiler||` | hidden text; click it (or press Enter on it) to reveal |
| `\*` | a literal `*` (any of `` \ ` * _ ~ | [ ] ( ) < > # @ `` can be escaped) |

The same formatting works in direct messages.

**Links are never opened or loaded by the app.** Clicking one shows the
address and offers to copy it, so you can open it in your browser yourself.
Wrap a link in `<` and `>` (`<https://example.org>`) to keep the community
from showing a preview of it.

### Mention someone

Type `@` and the person's name (their nickname here, or display name):
`@Alice`. OpenArcanum turns it into a mention when you send. You can also
mention a role (`@Moderators`), `@everyone` or `@channel` (everyone who can
read the channel) or `@here` (those of them online right now). As you type, a list shows the members and roles whose name
starts with what you typed: the arrow keys and Enter (or Tab), or a click,
put the name in.

- A mention shows highlighted; a message that mentions you gets a yellow
  bar, a red **@ badge** on the channel and a number on the community icon.
- Don't want them? Right-click the community's icon: **Suppress @everyone,
  @here and @channel** and **Suppress role mentions** stop those from
  notifying you or counting as mentions there (mentions of you by name still
  do). **Mute community** or **Nothing** silences it entirely.
- Mentioning `@everyone`, `@here`, `@channel` or a role that isn't marked mentionable
  needs the *Mention @everyone, @here and all roles* permission. Without it
  the message is still sent, but nobody is notified.
- Mentions inside `` `code` `` don't count.

### Reply, and jump to the original

Hover a message and click **Reply**. The reply shows a quote of the
original; click the quote to jump back to it (it flashes). You can also jump
to messages from the pinned list, from search results and from the red @
badge next to a channel (it jumps to your first unread mention).

### Edit or delete

Hover your message: **Edit** or **Delete**. Edited messages show
"(edited)" (hover it for the time). Members with *Manage messages* can
delete anyone's message. Admins can turn editing or deleting your own
messages off for the community; the buttons then disappear.

### React

Hover a message and click **Add reaction** (a smiley with a plus), pick an
emoji or type one under "Other…". Click a reaction to add or take back
yours. A message can have up to 20 different reactions and 1000 in all.
Needs the *Add reactions* permission and reactions turned on for the community and channel.

The community's own **custom emoji** appear in the reaction menu and under
**Custom emoji** (the same smiley) next to the message box (needs *Use
custom emoji*, and custom emoji turned on for the community); they show as
images in messages.

### Report a message

Hover someone else's message and click **Report** (a flag), with an optional
reason (up to 500 characters). The community's moderators (members with
*Manage messages*) see the report, your name and the reason. You can
report a message once, at most 5 messages every 10 minutes, and have at
most 20 reports waiting for the moderators at a time.

### Pin, and see pinned messages

Members with *Manage messages* see **Pin** on hover (and **Unpin** on pinned
ones). Anyone can open the pinned list with **Pinned messages** (a pin) at
the top of the channel; click a pin to jump to it. Up to 50 pins per
channel.

### Send files

Click **Attach files** (the paperclip) next to the message box. Choose up
to **10 files** with **Browse…**, or type their paths, then **Send**.
Whatever you typed in the message box becomes the caption. Each file can be
up to the community's limit (10 MB unless the admins changed it, up to
25 MB); a community can allow images only. The community server stores
files encrypted at rest, but its admins can read them, as they can read
messages.

Images get a **Show** button that displays them inline; **Save…** downloads
any file.

### See who's typing

"Alice is typing…" appears under the messages while someone types in the
channel you're looking at. Admins can turn typing indicators off.

### Unread messages

- A channel with unread messages is shown in bold with a dot; a community
  with unread mentions shows the count on its icon (a round red badge, top
  right), and a white bar when it has unread messages.
- New messages never pop up inside the window: these badges are how
  OpenArcanum shows them. Desktop notifications are separate (see
  "Notifications").
- When you open a channel, a red **New** line marks where you stopped
  reading.
- Right-click a channel → **Mark as read** to clear it without opening it;
  right-click the community's icon → **Mark all as read** for every channel.

Read markers are kept on this device (in its encrypted state file).

### Link previews

If the community turned link previews on, a message with a link gets a card
with the page's title, description and image. **The community server fetches
it, not your device**, so the linked site never sees your address. Previews
are off by default.

## Members and profiles

### See who's here

Click **Members** (two people) at the top right. Online members are listed
under their highest role that's shown separately (or **Online**), the rest
under **Offline**, with role colours and pronouns. Click a member (or
right-click them) for their menu. The dot on each avatar is their status
(see "Your status"): green online, amber idle, red do not disturb, grey
offline; the custom text they share with you shows under their name. Hover
a row for the status in words (screen readers read it with the name).

### A person's menu

Right-click a person wherever they're shown: a row in the member list, a
message's author (name or avatar), an @mention in a message, the name in a
reply's quote, who reacted (right-click a reaction, then pick someone), a
1:1 conversation in the **Direct messages** list, a message's author in a
DM, someone in **Conversation details** or **Contacts**, or a row in
Community Admin → **Members**. From the keyboard, Tab to the name and press
the **Menu** key or **Shift+F10**. A click does what it always did (a
member-list row still opens the menu; a reply's quote still jumps to the
message). The menu holds what applies to that person:

- **View profile**: their profile card in that community.
- **Message**: your 1:1 conversation with them, or a new one. It goes the
  usual way: they need a contact card in that community, and their **Who
  can DM me** setting decides whether it arrives as a request.
- **Mention**: puts *@name* in the message box of the channel you're in
  (in a group DM, of that conversation).
- **Add as contact**, **Safety number** (in DMs), **Copy user ID** (their
  member key in a community, their DM identity key in DMs), and **Block** /
  **Unblock** once they're a contact or in a DM with you.
- For yourself: **Edit your profile here** (the same as **Your profile
  here** in the community menu).
- With the permission for it, in a community: **Change nickname…**
  (*Manage nicknames*), **Roles…** (*Manage roles*; only roles below your
  highest), **Time out…** / **Remove timeout** (*Time out members*),
  **Kick…** and **Ban…**, in red, each with the same dialog as in
  Community Admin. Owners can't be timed out, kicked or banned.
- In a group DM you own: **Make owner** and **Remove from group** (or
  **Cancel invitation**).

### Timeouts and new-member waits

If a moderator times you out, you can still read (and listen in voice) but
can't post, react, start typing, change your name, avatar, about text or
pronouns, speak, or use any moderator permissions you have until it ends;
the message box says when. Some communities make new members wait a while
after joining before they can post or react (*member screening*), or before
they can post links, files or mentions (*new-member restrictions*); the
message box (or the error you get) says when you can start. Members given a role skip the wait.

### Your profile in a community

Community menu (click the community name) → **Your profile here**:

- **Display name**: what you chose when you joined.
- **Nickname**: shown instead of your display name in this community.
  Needs *Change nickname*; moderators with *Manage nicknames* can change
  it too.
- **Pronouns** (up to 40 characters) and **About me** (up to 190), shown on
  your profile card.
- Avatar and contact card.

## Your status

Click your avatar and name at the bottom left (**Set your status**):

- **Online**, **Idle**, **Do not disturb** or **Invisible**. Idle is also
  set by itself after a while without input on that device (**Idle after**,
  10 minutes by default) and goes away when you use it again. Being in a call
  (a voice channel or a conversation's) counts as using it: you don't go idle,
  and auto-lock doesn't lock, while you're in one.
- Pointing at someone's status dot says their status and when they were
  last active: "Online - Active now", "Idle - Last active 12 minutes ago".
  That's only shown to the people you share your status with, and never
  while you're offline or invisible.
- Someone you also share a community with shows **Offline** as soon as
  that community sees them leave.
- **Do not disturb**: no desktop notifications. Unread marks, mention counts
  and the moderators' badges still count, so nothing is lost.
- **Invisible**: you appear offline to everyone, and you can still read and
  write. Your community servers still see that you're connected (they have
  to, to route your messages); they just don't tell anyone. What you do
  still shows: messages you send, being in a call, typing.
- **Custom text** (up to 128 characters) and an **Emoji**, and **Clear
  after**: **30 minutes**, **1 hour**, **4 hours**, **Today** (midnight) or
  **Don't clear**. **Clear custom text** removes it at once.
- **Who can see this** says who gets what; **Change who can see this** opens
  Settings → **Privacy & DMs** → **Status**.

Your status follows you to your other devices.

**Who sees it.** Your status is end-to-end encrypted: only the people you
share it with can read it. Not community servers, relays or maintainer
nodes; they pass it on without a key. In **Settings → Privacy & DMs**:

- **Your contacts see**: **Off**, **Online status only** or **Status and
  custom text** (the default), and **Share with**: **All contacts** (the
  default) or **Only the contacts I choose** (**Choose…**). Contacts get it
  through their DM mailboxes, so they need your contact card (and you
  theirs).
- **Status in each community**: **Off**, **Online status only** (the
  default) or **Status and custom text**, per community.

Why these defaults: whether you're around is what the member list has
always shown (the server knows who is connected anyway), so communities get
the state; what you're doing is more personal, so the custom text goes only
to people you chose as contacts until you share it more widely. Turning a
community **Off** removes your status there at once; taking someone off your
contacts (or your chosen list) sends them a "cleared" status and gives
everyone else a new key they never get.

**What servers still see.** A community server knows when you're connected
and when your status changes (not what it says), also while you're
Invisible. Maintainer nodes see that a contact's mailbox got something
about every 30 minutes while you're online. If you use separate identities
in two communities, sharing the same custom text in both could let someone
in both guess it's you.

**Contacts and "offline".** Contacts have no server to tell them you've
gone, so OpenArcanum says "offline" to them when it quits, and your status
counts as stale for them after about 75 minutes without a refresh.

## Direct messages and group DMs

Direct messages (DMs) are end-to-end encrypted between the people in the
conversation, on every one of their devices. No community server or
maintainer node can read them: they travel through relays when the other
person is online and wait, encrypted, in mailboxes on maintainer nodes when
they're offline. A 1:1 conversation is a group of two; a group DM holds up
to 10 people, you included.

**Direct messages** (two speech bubbles, top of the left rail) opens the
conversation list.
Its number counts unread DM messages. The list shows **Requests and
invitations** first, then **Conversations**, with tags such as
**request**, **invited**, **blocked**, **@** (you were mentioned) and the
unread count.

### Before you start: contact cards

People can message you through a community you share: your **contact card**
there is published, so a member can start a direct message with you, which
reaches you as a request. It holds only a DM key and an inbox address,
separate for each community. To turn it off for a community: community
menu → **Your profile here** → untick **Publish a contact card here** →
**Save** (or Settings → **Privacy & DMs** → **Contact cards**, one switch
per community). A community's admins can turn contact cards off for the
whole community; the option then disappears.

### Contact codes: DMs without a shared community

You don't need to be in any community to message people. **Direct
messages** opens on **Contacts**: everyone you can message, with
**Message**, **Safety number**, **Block** and **Remove** for each.

A **contact code** is how two people connect without a community. Yours
are one click away: the card button next to your name (bottom left), or
**Contacts** → **Your contact code**. There are two kinds:

- **Permanent code** (`openarcanum-id:`): keeps working until you
  **Revoke** it (or **Revoke and make a new one**). Give it to friends.
  Revoking stops new people from using it; people you already talk with
  aren't affected.
- **One-week code** (`openarcanum-contact:`): for someone who'll write
  soon; it starts a conversation for about a week, then expires by itself.

Both use a DM key of their own, not one from any community, so they don't
tell anyone which communities you're in. Once you've talked with someone,
you keep messaging each other whatever happens to the code.

To use someone's code: **Contacts** → **Add a contact code**, paste it,
**Add and message**. They see your first message as a request; accepting
it adds you to their contacts too. **New group DM** (the + at the top of
the list) can start a group with your contacts or with members of a
community; a group invitation reaches only people who have you as a
contact.

**Rename** (on Contacts, or **Rename…** in anyone's menu in your
conversations) gives a person a name of your own, seen only on your
devices; empty goes back to the name they set. Naming someone doesn't make
them a contact, and the name stays when you remove them from your contacts
or block them.

The name people see in direct messages and in your code is **Settings →
Identity → Your name** (it can be empty). A community you join starts with
that name, and you can use a different one there. Your device's name never
leaves your own devices.

### Start a conversation

- **1:1:** in a community, right-click the person (in **Members**, or
  their name on a message) → **Message**. The conversation opens with "End-to-end
  encrypted" in its header; type and press Enter.
- **Group:** **Direct messages** → **New group DM** (the plus): pick
  **People from** (your contacts, or a community's members), an optional
  **Group name** (up to 100 characters) and up to 9 people, then
  **Create**. "Group created. People join once they accept the
  invitation." **Search by name** filters a long list; the people you've
  picked stay at the top of it whatever you search for.

Everyone you pick needs a contact card in that community, and their **Who
can DM me** setting decides whether your message or invitation reaches
them.

New conversations, and messages from someone who was offline, can take up
to about 15 seconds to show. **Check for new messages** (circular arrows,
top of the list) checks at once.

### Who can DM you, requests and invitations

**Settings → Privacy & DMs → Who can DM me**:

- **Contacts only** (the default). Contacts are people you added
  yourself: member menu → **Add as contact**, or in a conversation
  **Conversation details** → **Options for …** (three dots) next to the
  person → **Add as contact**. Contacts can message you
  directly. A 1:1 message from anyone else arrives as a **request**: "Alice
  isn't in your contacts and wants to message you." with **Accept** and
  **Decline**. A group invitation from someone who isn't your contact is
  dropped without notice.
- **Members of communities we share**: also lets anyone who shares a
  community with you, and has a contact card published there, message you
  directly or invite you to a group.

Accepting a request doesn't make the person a contact. A request you don't
answer is dropped after 30 days, and at most 100 requests wait at once.
Group invitations always wait for you: "You were invited to "Weekend".
You'll only see messages sent after you join." with **Join** and
**Decline**.

**Direct messages** → **Contacts** (two people) lists your contacts, each
with **Safety number**, **Block** and **Remove**.

### Messages in a DM

DMs work like community messages (see "Messages in a community"), with
the same Markdown formatting:

- **Enter** sends, **Shift+Enter** starts a new line; messages hold up to
  4,000 characters.
- Hover a message: **Reply** (click the quote to jump back to the
  original), **Add reaction** (six quick emoji or **Other…**), **Edit** and
  **Delete** (your own messages only; delete removes it for everyone),
  **Pin** / **Unpin** (anyone in the conversation), **Copy text**. Pinned
  messages are listed in **Conversation details**; click one to jump to
  it.
- Edited messages show "(edited)".
- "Alice is typing…" shows under the messages while someone types.
- A red **New** line marks where you stopped reading.
- Mention someone in the conversation with `@` and their name; the
  conversation then shows an **@** tag in the list.

### Group details and owner controls

**Conversation details** (the "i" in a circle) shows the people in the
conversation, with tags such as **owner**, **invited**, **verified**, **key
changed** and **blocked**, and a menu for each person (three dots,
**Options for …**): **Safety number**, **Add as
contact**, **Block** / **Unblock**.

The person who creates a group is its **owner**. In **Group details** the
owner also has:

- **Set icon…** (an image, shrunk on your device and shared inside the
  encrypted group), **Remove icon** and **Rename**;
- **Add people**, up to 10 members in all (invited people count until they
  answer);
- in each person's menu (three dots), **Make owner** (you then can't change
  the group's settings any more) and **Remove from group** (or **Cancel
  invitation** for someone who hasn't joined): "They won't receive anything
  new.";
- **Settings**:
  - **Members can add people**: off means only the owner can invite.
    When it's on, every member sees **Add people**.
  - **Allow attachments**: off means nobody can send files in this
    group; the **Attach files** button disappears for everyone.

There's no server behind a group DM, so every member's app checks each
change: changes that don't come from the owner are ignored.

### Leave a conversation

- **Group:** **Conversation details** → **Leave group** → "Leave this group?
  You will stop receiving its messages, and its history is deleted from your
  devices." → **Leave**. The owner must first make someone else the owner
  ("You own this group. Make someone else the owner before leaving.").
- **1:1:** **Conversation details** → **Delete conversation** (or
  right-click the conversation in the list → **Delete conversation**)
  deletes it and its history from your devices. A group's right-click menu
  has **Leave group**.
- If an owner removes you, the conversation says "You were removed from
  this conversation. You can still read what you received before." with
  **Delete conversation**.

People who join a group later don't see messages from before they joined.

## Files in direct messages

Click the paperclip (**Attach files (up to 10, 25 MB each)**) next to the
message box. In **Attach files**, use **Browse…** or type a path and **Add**;
**Remove** takes one back off the list. **Send** sends them, with what you
typed in the message box as the caption. A path you typed opens in the
system's file dialog first, for you to confirm: OpenArcanum only reads and
saves files you chose there (the same goes for every **Save to** and image
path you type).

- Up to **10 files** per message, each up to **25 MB**. OpenArcanum refuses
  more: "You can send at most 10 files in one message.", "… is over the 25
  MB limit."
- Each file is encrypted on your device, with its own random key, before
  it leaves. The key travels only inside the encrypted message.
- In a group whose owner turned **Allow attachments** off there's no
  **Attach files** button ("The owner turned off attachments in this
  conversation.").

**How files reach people.** Devices that are online get the file straight
away through the relay. For devices that are offline, OpenArcanum stores the
encrypted file once on the maintainer nodes, which can't read it. Each copy
there is deleted as soon as every recipient device has fetched it, and
after **72 hours** at most (the maintainer's setting; 72 hours by default).

**Receiving.** Each file shows its name, size and type, and images a small
preview (made and encrypted by the sender). "not downloaded yet" means it
hasn't arrived on this device; it fetches by itself. **Save…** asks where
to save it: **Save to** (a full path or a folder; your Downloads folder
by default) or **Browse…**, then **Save** ("Saved readme.md."). Names from
other people are made safe before saving.

**Expired files.** If your devices were all offline for longer than the
file was kept (72 hours by default), the file shows **expired** and has no
**Save…**: ask the sender to send it again. A file that was tampered with
on the way isn't saved ("This file is damaged and can't be opened.").

## Safety numbers, key changes and blocking

### Check a safety number

Encryption protects you only if you're talking to the right person.
Compare safety numbers once per contact:

1. In the conversation: **Conversation details** → **Options for …** (three
   dots) next to the person → **Safety number** (or **Direct messages** →
   **Contacts** → **Safety number**).
2. **Safety number with Alice** shows 60 digits in 12 groups of 5.
   Compare it with Alice in person or on a call you trust: "If it matches
   on both screens, nobody is in the middle."
3. If it matches, **Mark as verified**. Alice then has a **verified** tag
   in conversation details and in your contacts. **Mark as not verified**
   takes it back.

You have a separate identity in each community (unless you chose to reuse
one), so the same two people can have a different safety number in each
community they share.

### When someone's key changes

If a person shows up with a different identity key from the one OpenArcanum
saw before, their conversations show a warning: "Alice's identity key
changed. This happens when they reinstall or reset their profile, or when
someone else is pretending to be them. Check their safety number before
trusting new messages." Their name gets a **key changed** tag, and a
**verified** mark is removed.

1. Ask Alice, outside OpenArcanum, whether she reset or reinstalled.
2. **Safety number** (on the warning) says "Their identity key changed
   since you last checked. Accept the new key first, then compare."
   **Accept new key** clears the warning; a contact moves to the new key.
3. Open **Safety number** again, compare the new number with her, and
   **Mark as verified** if it matches.

### Block someone

**Conversation details** → **Options for …** next to the person → **Block**
(or **Direct messages** → **Contacts** → **Block**). OpenArcanum then drops
their messages and invitations in every conversation; messages they send
while blocked aren't kept for later. They aren't told. A 1:1 conversation
with them says "You blocked this person. You won't receive their
messages." with **Unblock**, and you can't message them or start a
conversation with them until you unblock. Blocking keeps what you already
have: the conversation, its history and your name for them. Blocks, verified marks
and contacts are shared with your other devices.

## Your devices

Every device you link holds your keys and has its own passphrase. Use
linking, not a backup, to add a computer: linking also copies your direct
message history, which exists only on your devices.

### Link a new device

1. **On the new device**, on the first-run screen: **Link to an existing
   device**. Check **Name this device**. The code goes through this
   device's maintainer nodes, shown at the bottom: if your other device uses
   its own list, use **Change…** to enter the same nodes here first (see
   "Maintainer nodes"). **Get a code**.
2. It shows **Your link code**, for example `1-blanket-easel-swallow`, with
   a 5-minute countdown.
3. **On a device that's already set up and unlocked:** **Settings** →
   **Devices** → **Link a new device…**, type the code and your
   passphrase, **Continue**. A
   code works once and allows one guess: a wrong code cancels the link, and
   you start again with a new code. Never type a code someone else sent you.
4. **Approve new device** shows the new device's name and system and what
   to send:
   - **Communities to share**: all are ticked; untick any you want to keep
     off the new device.
   - **Copy direct message history** (on, with its size): your devices hold
     the only copy of your DMs.
   - **Copy downloaded community messages** (off): optional, because the
     new device can download them from the servers.

   **Approve and send**.
5. **On the new device:** **Almost done** says what arrived ("Received from
   Linux device: 1 communities, 3 conversations."). Choose this device's
   passphrase twice → **Finish**.

The new device gets your keys for the shared communities and for your
DMs, any owner keys, your settings and contacts. It makes its own device
key for each community and connects; a community may limit how many
devices one member has (5 by default). Your other devices show "New device
linked: Laptop". Copying history continues in the background, so keep the
old device online until the new one says "Message history copied from your
other device." From then on your devices keep each other up to date:
joining and leaving communities, settings, contacts, blocks and verified
marks.

### Rename or remove a device

**Settings → Devices** lists each device with when it was linked and last
seen; this one is marked **this device**.

- **Rename** changes the name in the list.
- **Remove** → "Remove Laptop?" → your passphrase → **Remove**: every community refuses that
  device from then on, it stops receiving DMs and updates from your other
  devices, and if it comes online it wipes its profile. If some
  communities can't be reached, OpenArcanum says which. You can't remove a
  device from itself; do it from another device.

A lost device is protected by its passphrase. If someone may know that
passphrase too, remove the device and also replace your identity in each
community (leave and rejoin with a new identity, and ask an admin to give
the new member your old roles with **Replace member…**).

## Voice, video and screen share

Calls happen in voice channels (a speaker icon in the channel list).
Everything you say and show is end-to-end encrypted: the relay that carries
the call only forwards encrypted media, and the call key changes whenever
someone joins or leaves. The relay can't tell when you talk either: your
voice goes out as packets of one size, 50 a second, whether you speak,
stay quiet or are muted. Up to 99 people fit in one call.

### Join and leave

Click a voice channel to join. Anyone who can see the channel sees who is
in it, listed under the channel, before joining. The voice bar at the
bottom of the channel list says where the call is: **Joining Lounge…**
while the server answers, **Connecting…** while the media connection comes
up, **● Voice connected** once it's up, and **Reconnecting…** if the
connection drops (the call comes back by itself, and if the relay carrying
it fails, it moves to another relay within seconds). Click the channel name
in the voice bar to show the call (the *stage*: a block per person, with
their camera or avatar, and one for each shared screen; see "Watch a camera
or a shared screen"). Everything you do in a call is in the voice bar:
**Mute**, **Deafen**, **Turn on camera**, **Share your screen**, **Priority
speaker** (if you have it), **Voice & video settings** (the gear) and
**Leave call** (a hung-up phone). Mute, deafen, the camera and the screen
each have a small arrow beside them for their options (see "Options in the
voice bar"). The stage's header has **Chat** for the channel's text chat.

**If a call can't connect**, you leave it (nobody sees you stuck in the
room), and the voice bar keeps the reason with **Try again**, **Copy
details** (to send to whoever runs the community) and **Dismiss**; the
same message shows as an error notice.

**Calls need UDP.** Media goes to the relay's UDP port; there's no TCP
fallback. On a network that blocks outgoing UDP (some workplaces, schools,
hotels and VPNs do), chat and DMs keep working but the call can't connect.
OpenArcanum notices within about 15 seconds and says so in plain words: "Can't
reach the call's media server at 203.0.113.7:7000. Calls need UDP network
traffic…", with the address a firewall has to let through. Try another
network (a phone hotspot, say) or turn the VPN off, or ask whoever runs the
network to allow UDP to that address; then **Try again**.

The first time you join, the system may ask for microphone access
(macOS: allow it, or later in System Settings → Privacy & Security →
Microphone).

### No microphone or camera

You can join without a microphone, or with OpenArcanum not allowed to use it:
you're in the call **listening** (you hear everyone; nothing you say goes
out). The voice bar says why, and that you can still hear everyone:

- **No microphone found.** Plug one in: the call starts using it by itself.
  Or click **Choose microphone** for the microphones there are, or open
  **Voice & video settings** (the gear in the voice bar), without leaving
  the call.
- **OpenArcanum isn't allowed to use the microphone.** Allow it in your
  system's privacy settings (macOS: System Settings → Privacy & Security →
  Microphone; Windows: Settings → Privacy & security → Microphone), then
  click **Try again**.
- **The microphone couldn't be opened. Another app may be using it.** Close
  the other app (or pick another microphone) and click **Try again**.

The stage's status line says the same. Unplugging your microphone during a
call leaves you listening and switches to another microphone if there is
one; a microphone you chose that isn't plugged in any more is replaced by
the system's default.

Without a camera, **Turn on camera** says "No camera found." and the call
goes on with your video off; unplugging the camera during a call turns your
video off and says so.

### Text chat in a voice channel

Every voice channel has its own text chat, like a text channel's: for links,
"brb", or talking to people in the call when you can't speak. Click **Open
chat** (a speech bubble) next to the voice channel (it shows when you point
at the channel, and always when there's something new), **Chat** in the
stage's header, or **Open chat** in the channel's right-click menu. The chat opens on
the right, where the member list is, with who's in the call at the top and
**Join voice** if you aren't; you don't have to be in the call to read or
write. Close it with **Close chat** (the X; the member list comes back).

New messages show as a dot (and @mentions as a count) on the voice channel
while its chat isn't open, and notify you like any channel (mute it from
its right-click menu). Everything else works as in a text channel: replies,
reactions, files, pins, search, reports, slowmode and the permissions
*Send messages*, *Read message history* and *Attach files*. With voice
channels turned off for the community their chats go too.

### Mute, deafen and the speaking ring

In the voice bar:

- **Mute** / **Unmute** (a microphone): nobody hears you; others see the
  muted icon.
- **Deafen** / **Undeafen** (headphones): you hear nobody, and deafening
  mutes you too. Undeafening leaves you muted: unmute when you're ready to
  talk. While you're deafened nobody's voice is even sent to you, so you
  don't see who is speaking either.

A green ring around someone's avatar (in the channel list and on the stage)
shows who is speaking. Icons next to a name show *Muted*, *Deafened*,
*Server muted* or *Server deafened* (in red), *Muted for you* (a crossed-out
speaker: see "Someone's volume, or mute them for you"), *Camera on* and
*Priority speaker* (a megaphone); **LIVE** (red) means they're sharing their
screen.

### Options in the voice bar

The small arrow beside an icon (or a right-click on the icon) opens its
options; with the keyboard, Tab to the arrow and press Enter (Escape closes
the menu):

- **Microphone** (beside Mute): **Voice activity sensitivity**, your
  microphones (the one in use is ticked), and **Input mode**: Voice
  activity, Studio or Push to talk.
- **Sound** (beside Deafen): **Output volume** (0-200%, this device only)
  and your speakers.
- **Camera**: turn it on or off, and your cameras.
- **Screen**: share or stop, and **Share sound** (see "Share your screen").

A choice made here is saved, as in Settings.

If you lack the *Speak* permission in that channel, or are timed out, you
join **listening only**: the voice bar says so and has no mute button.

### Devices

**Settings → Voice & video** (also **Voice & video settings** in the voice
bar, and at the bottom of each of its menus) lists your microphones, speakers and
cameras by name (**System default** follows the system's choice; a device
that isn't plugged in shows as "(not connected)"; with no microphone it says
so and still lists the rest). Changing one during a call switches to it
straight away, without rejoining. Check them before a call:

- **Test microphone** shows a live level meter: speak and watch it move.
- **Play test sound** plays a short chime through the chosen speaker.
- **Test camera** shows your camera's picture; **Stop camera** ends it.

**Output volume** sets how loud calls play on this device.

Echo cancellation, noise suppression and automatic gain control are on,
except in studio mode.

### Voice activity or push to talk

**Input mode** in Settings → Voice & video:

- **Voice activity** (the default) sends while you speak. Drag **Voice
  activity sensitivity** so the mark sits just below your speaking level
  and above your room's noise (watch the meter while you talk and while
  you're quiet): sound louder than the mark counts as speaking. The default
  is −40 dB, which ignores the steady noise of a fan or computer near the
  microphone.
- **Studio** sends any sound your microphone gives while you're unmuted,
  with no threshold and none of the processing above (no echo
  cancellation, noise suppression or automatic gain): for a microphone,
  interface or software that has its own noise gate and processing. When
  it gives silence (your gate is closed, or the microphone is muted on the
  hardware), nothing is sent and you aren't shown as talking. Use headphones, or your speakers come back
  through the microphone. Where a channel takes *Use voice activity* away,
  studio is push to talk too.
- **Push to talk** sends only while you hold the **Push-to-talk key**
  (click the box and press a key: Space, a letter, a digit, F1-F24, Shift,
  Ctrl, Alt, Caps Lock, Tab, an arrow or another named key; or press an
  extra mouse button, back or forward, on the box). The left, right and
  middle mouse buttons are never push to talk.
  The key does nothing while you're typing in a text box. Some communities
  take *Use voice activity* away in a channel; there you always use push to
  talk.

**Push to talk in other windows** (on by default) lets the key work while
another app is focused:

- **Windows:** works as is.
- **macOS:** allow OpenArcanum under System Settings → Privacy & Security →
  **Input Monitoring** (restart OpenArcanum if it doesn't take effect). Until
  then the key only works while OpenArcanum is focused.
- **Linux on X11:** works as is for keys; the extra mouse buttons work only
  while OpenArcanum is focused there.

If the key you chose can't be watched in other windows, Settings says so
under it; pick another key.

On Windows, a key pressed while an app running as administrator is focused
(some games and their launchers) isn't seen by other apps, OpenArcanum
included: Windows keeps those apart.
- **Linux on Wayland** (GNOME, KDE and most current desktops): Wayland
  doesn't let apps see keys pressed in other windows, so the key works only
  while OpenArcanum is focused. Settings says "This is a Wayland session…"
  under the switch and turns it off.

### Someone's volume, or mute them for you

Right-click a person in the call (in the channel list or on the stage), or
hover their name and click **Options for …** (three dots). The menu starts
with a volume slider for them (0-200%): you hear the change as you drag, and
it's kept when you let go. It changes only what you hear, on your devices.
While they share their screen there's a **Stream volume** slider too, for
its sound (for this call).

**Mute for me** mutes them on this device only: you don't hear them (their
voice isn't even sent to you), and nobody else is told or affected; it isn't
a server mute. It's kept on this device for later calls (*Muted for you*
next to their name) until you click **Mute for me** again. **Mute stream**
does the same for their screen's sound.

### Watch a camera or a shared screen

Someone's camera takes the place of their avatar on the stage, and a shared
screen is a block of its own marked **LIVE**. Neither is sent to you until
you click it (**Click to watch**): this saves your connection and theirs (if
nobody watches, their app doesn't send it at all). Clicking a video watches
it and brings it to the front, with everyone else in a strip below; click
it again, or press Escape, to put it back among the others; **Stop
watching** (the crossed-out eye on it) stops receiving it. On a watched
screen, **Mute stream** (the speaker) and **Stream volume** (the arrow
beside it) set its sound for you only. Your own camera and screen show their
preview.

**Pop out** (the monitor on a watched video) opens it in a window of its
own, which you can move to another screen and resize; the call goes on in
the main window, and the sound stays there too. Close the window, or stop
watching, to end it.

### Camera

Click **Turn on camera** in the voice bar (shown if you may use video in
that channel; macOS asks for camera access the first time); the arrow beside
it picks the camera. Your camera is sent at up to 1280×720, 30 frames a
second, in up to three sizes, only to people who watch it and only in the
sizes they need: each watcher's app picks the size that fits the block it
shows you in, and a smaller one when their connection starts losing packets.
While nobody watches, nothing is sent (the camera stays on and everyone sees
you have it on). Click **Turn off camera** to stop. You can share your
camera and your screen at the same time. Turning your camera off, or
stopping a screen share, asks first (**Turn off your camera?**, **Stop
sharing your screen?**), so a stray click doesn't cut you off; if the
screen share ends on its own (the shared window closed), OpenArcanum says
so.

### Share your screen

Click **Share your screen** (a monitor) in the voice bar (shown if you have
*Screen Share*, which is off for `@everyone` by default). OpenArcanum's own
picker lists your screens and windows (windows appear after a moment); pick
one, or **Cancel**. Stop with **Stop sharing**, or by closing the shared
window.

- **macOS:** the first time, allow OpenArcanum under System Settings → Privacy
  & Security → **Screen Recording**, then restart OpenArcanum if macOS asks.
- **Sound:** **Share sound** (in the picker, and in the arrow's menu beside
  the screen button; on by default, kept for next time) sends the shared
  screen's sound (a video playing, say) where the system allows capturing
  it, which is Windows for a whole screen (a single window's sound can't be
  captured there: OpenArcanum shares the window without it and says so); on
  macOS and Linux it depends on the system and version. Turned off while sharing, the sound stops at once; turned on, it
  comes with your next share. Viewers can mute it or change its volume for
  themselves.
- Like a camera, your screen is only sent while someone watches it (the
  **LIVE** badge shows everyone you're sharing).
- If the system can't capture what you picked, OpenArcanum says so and the call
  goes on.

### When a moderator acts

Moderators (see below) can act on you during a call:

- **Server mute:** the voice bar shows "server muted" and nobody hears you;
  your own mute button can't undo it. It stays, in later calls too, until a
  moderator unmutes you.
- **Server deafen:** you hear nobody (also until a moderator undeafens
  you); everyone, you included, sees the red *Server deafened* icon next to
  your name.
- **Move:** your app moves you to another voice channel by itself.
- **Disconnect:** your call ends with "A moderator disconnected you from
  voice." You can join again.
- **Priority speaker:** while someone who has switched on *Priority
  speaker* talks, everyone else is played at 30% (about a third) on every
  listener's device.

If you lose the right to connect (a role or override changes), the call
ends with "You can no longer connect to this voice channel."

### Calls in direct messages

In a conversation, **Start a voice call** (the phone) or **Start a video
call** (the camera) at the top. Everyone in the conversation hears it ring
("… is calling", with **Join**); while a call is on, the header shows
**Join call (n)**. While you're in it, the call shows at the top of the
conversation with its controls under it: mute, deafen, camera, screen share
and **Leave**; the messages stay below. Drag the divider under the call (or
focus it and use the up and down arrows) to give it more or less room; the
height is kept for your next calls. **Hide chat** gives the call the whole
conversation (**Show chat** brings the messages back), and **Videos only**
leaves just the cameras and screens on the stage, without the people who
have neither (**Show everyone** to undo).
It works like a voice channel, with the same end-to-end encryption. You're
in one call at a time: joining a conversation's call leaves a voice channel,
and joining a voice channel leaves the call.

The camera and screen buttons' menus (the small arrows) set the quality you
send: the camera's resolution (480p, 720p, 1080p) and, for a screen, sharp
text, balanced, or smooth motion. On someone's video, **Video quality** (or
the arrow on a shared screen) picks what you receive: automatic (it follows
the video's size on your screen and your connection), high, medium or low.

## Admins: voice moderation

A voice channel's text chat has the same settings as a text channel's
(Community Admin → Channels → edit the voice channel): slowmode,
message and file retention, and the attachments, link previews, reactions
and new-members-see-history settings; AutoMod rules apply to it and can
exempt it.

Right-click someone in a call (or **Options for …** next to their name) for
the actions your permissions allow: **Server mute** / **Server unmute**
(*Mute members*), **Server deafen** / **Server undeafen** (*Deafen
members*), **Move to…** another voice channel and **Disconnect** (*Move
members*). The community server enforces each of them at the relay, so a
modified client can't get round them, and they're in the audit log.

**Priority speaker** is a role permission (Roles → create or edit a role →
*Priority speaker*, then give the role to someone, or allow it in a
channel's overrides). It's off until that person switches it on in a call:
the megaphone button beside the camera button, there only for people who
have the permission. The megaphone icon by a name shows whose is on.

Voice channels, video and screen share can each be turned off for the whole
community under **Features**, and limited per role, channel or member with
the *Connect*, *Speak*, *Video*, *Screen share* and *Use voice activity*
permissions (docs/admin-guide.md "Feature toggles", "Permission defaults").

## Notifications

**Settings → Notifications:**

- **Desktop notifications** (on by default) turns them all on or off.
- **Notify me about**: **All messages** (default) or **Mentions only**
  (messages that mention you or @everyone). Direct messages follow the same
  choice.
- **Each community**: **Notify me about** per community: **Default (as
  above)**, **All messages**, **Mentions only** or **Nothing**. The same
  choice is in the community icon's right-click menu (**Notifications**).
  **Nothing** keeps the community's unread marks and badges but never
  notifies, not even for mentions or reports.
- **Muted**: a switch per community. A muted community never notifies, not
  even for mentions. The community menu has **Mute community** too.

To mute one channel, right-click it → **Mute channel** (a crossed-out bell
shows next to it; **Unmute channel** undoes it). Mentions still notify in
muted channels.

**While OpenArcanum is locked,** a notification says only "OpenArcanum" and "New
activity": no names, no message text.

**Do not disturb** (your status, see "Your status") turns desktop
notifications off for messages, DMs and reports until you change it or it
clears; the badges keep counting.

Notifications come from your system: the desktop's notification service on
Linux (GNOME, KDE, Xfce and others), the notification area on Windows, and
Notification Center on macOS (allow OpenArcanum when macOS asks). Inside the
window, new messages and reports never pop up: unread marks, mention counts
and the moderators' badges show them instead (see "Unread messages" and
"Reports and join requests waiting"). Other notices, such as a newly linked
device, still appear briefly at the bottom right.

Moderators also get a desktop notification for each new report or AutoMod
alert ("New report: Alice reported a message by Bob"), following the same
settings for that community.

### Sounds

The app plays short, quiet tones (each under a quarter of a second). They are
made by the app as it runs, not recorded: there are no sound files. **Settings
→ Notifications → Sounds**:

- **Play sounds** (on by default) turns every sound on or off.
- **Sound volume** (50 % by default). Sounds play through the speaker chosen
  under **Settings → Voice & video**.
- A switch for each sound, all on by default, each with a **Play** button to
  hear it:
  - **New message**: a message in a channel you aren't reading.
  - **Direct message**: in a conversation you aren't reading.
  - **Mention**: a message that mentions you, also in a muted channel.
  - **Someone joins your call** (two notes going up) and **Someone leaves
    your call** (two notes going down): the voice channel you're in.
  - **Mute and unmute**, **Deafen and undeafen**: a soft click pair when you
    change your own microphone or sound in a call.
  - **Call connected and disconnected**: when your call connects, and when
    you leave it or it ends.

Message sounds follow the same choices as notifications: a muted community,
**Nothing**, a muted channel (except for mentions) and **Mentions only** stay
quiet. They don't depend on the **Desktop notifications** switch, so you can
have sounds without pop-ups or the other way round. Nothing plays for the
channel or conversation on screen while the window has focus, and a burst of
messages (catching up after being offline, a busy channel) plays one sound.

**Do not disturb** and **Deafen** silence every sound except those of your
own call (mute, deafen, connected, disconnected), since those answer
something you just did. No sound plays until you have clicked or typed in the
window once after starting the app.

These settings belong to this device: each of your devices has its own.

## The tray icon, and closing the window

While OpenArcanum runs, its icon (the cracked violet speech bubble) sits in the
system tray: the notification area on Windows, the menu bar on macOS, and the
panel's tray on Linux desktops that have one (KDE, Xfce, Cinnamon, MATE,
GNOME with the AppIndicator extension, and others).

- **Click the icon** to show the window (it comes back if it was closed or
  minimized).
- **Its menu** (right-click; a click on macOS): **Open OpenArcanum**, **Mute
  notifications** (the same switch as Settings → Notifications → **Desktop
  notifications**, ticked while they're off; available while unlocked),
  **Lock** (locks at once, like **Lock now**), and **Quit OpenArcanum**.
- **A red dot** on the icon means something is waiting for you: unread
  mentions, unread direct messages, or (moderators) reports and join
  requests waiting for review: the same things the window's badges count.
  Point at the icon to see what ("2 unread mentions", "1 waiting for
  review"); once you've read them, the dot goes away. While OpenArcanum is
  locked the icon shows nothing waiting and its tooltip says **Locked**.

**Closing the window keeps OpenArcanum running in the tray**, so communities
you host on this computer stay up, and messages,
notifications and calls still reach you. The first time, a notification
says so. To quit, choose **Quit OpenArcanum** in the tray menu, press
**Ctrl+Q** (**Cmd+Q** on macOS, or **Quit** in the app menu), or log out:
OpenArcanum saves everything and closes as usual. To make closing the window
quit instead, turn off Settings → **Appearance** → **Keep running in the tray
when the window is closed** (on by default; this device only).

**Without a system tray** (plain GNOME, and many other Wayland desktops,
have none unless a tray extension is installed), there's no tray icon, the
setting is shown off and greyed out with that reason, and closing the
window quits OpenArcanum.

## Keyboard and screen readers

Everything works from the keyboard, with a visible focus outline:

- **Tab** and **Shift+Tab** move between controls; **Enter** or **Space**
  activates the focused one (channels, members, results, buttons).
- Tab to a message to show its actions (**Reply**, **Add reaction**,
  **Edit** …); Tab again to reach them.
- In the message box, **Enter** sends, **Shift+Enter** starts a new line,
  and **Escape** cancels a reply you started.
- Dialogs keep the focus inside them. **Escape** closes the top one and
  puts the focus back where it was.
- Settings and Community Admin open as pages in the middle of the window,
  with the focus on their title. With no dialog open over them, **Escape**
  closes them (as **Close** does) and puts the focus back on what opened
  them.
- Spoilers open with **Enter** or **Space**. In **Search**, **Enter**
  searches; Tab to a result and press **Enter** to jump to it.
- **Ctrl+Shift+I** opens the developer tools (for bug reports) when
  OpenArcanum was started with `--debug` (or with `OPENARCANUM_DEVTOOLS=1` in its
  environment); otherwise it does nothing, so nobody can talk you into
  pasting code there.

Menus that open with a right-click (a community's icon, a channel's **Mute
channel** and **Mark as read**, a message's or a person's menu) open from
the keyboard too: press the **Menu** key or **Shift+F10** on the focused
icon, channel, message or person (a message's author name and an @mention
take the focus with Tab). The focus moves to the first entry; **↑** and **↓** move,
**Enter** chooses, and **Escape** closes the menu and puts the focus back.

For screen readers, every button has a name (icon buttons use their
tooltip), settings are labelled and described, and new messages and the
"is typing" line are announced politely as they arrive. Both themes meet
WCAG 2.1 AA contrast, checked automatically. **Settings → Appearance**
changes the **Theme** (**Dark**, the default, **Light** or **Match the
system**; also on the screens before unlock), the
**Text size** (**Small** to **Larger**) and **Compact mode**. The
"jump to message" flash is replaced by a steady highlight when your system
asks for reduced motion.

## Settings at a glance

**Settings** (the gear, bottom left) opens in the middle of the window, in
place of the channel or conversation (and the member list): its sections
are listed down its left, the open one beside them, and the window's title
says which. The rail, the channel list, the voice bar and your name stay
where they are, and a call goes on. To go back, press **Close** (top right)
or **Escape**; clicking a channel, a community or **Direct messages** also
leaves Settings for it.

Every change saves at once (a green
**Saved** appears) and is checked first; a refused value shows why next to
the control. Each part of a section is a box of its own; its buttons sit in
the box's footer, under a line, so it's clear what they act on. Appearance,
the tray option, the data folder, update checks, maintainer nodes and
whether this device stays signed in are kept in an unencrypted settings
file, so the screens before unlock can use them; everything else is stored
in your encrypted profile.

Messages that something went wrong (red, bottom right) stay until you close
them with **Dismiss** (the X): clicking the text doesn't close it, you can
select the text, and **Copy** puts it on the clipboard for a bug report.
Other notices go away by themselves after a few seconds.

| Section | What's there |
|---|---|
| **Security** | **Change passphrase…**; **Ask for my passphrase when OpenArcanum starts** (off: this device stays signed in); **Lock after idle** and **Lock now**; **Backup** status and **Export backup…** |
| **Identity** | **Default when joining** (a new key for each community, or reuse an identity); your name and avatar in each community (**Edit…**) |
| **Communities** | Each community's status, **Delete local data**, **Leave…** (or **Remove from list**) |
| **Devices** | **Link a new device…**, and each device with **Rename** and **Remove** |
| **Privacy & DMs** | **Who can DM me**; contact cards per community; **Status**: who sees your status (contacts: off / online status only / status and custom text, all or chosen; each community: the same choice) and **Idle after** |
| **Appearance** | Theme, text size, compact mode (saved unencrypted so the lock screen can use them; they say nothing about you); **Keep running in the tray when the window is closed** (see "The tray icon, and closing the window") |
| **Notifications** | **Desktop notifications**, **Notify me about**, the level for each community, muted communities, **Sounds** (on or off, volume, each sound with a **Play** button) |
| **Voice & video** | Devices, input mode and push to talk; see "Voice, video and screen share" |
| **Network** | **Maintainer nodes** with **Change…** (directory, community routes, DM mailboxes, link codes; this device only, also changeable before unlock; see "Maintainer nodes"), **Search the directory on this device**, **Preferred relays**, and **Reconnect** per community |
| **Data & storage** | **Local cache size (MB)** (1024 by default) and **Clear cache**; **Data folder** (**Choose…** moves the files now, **Use default**); **Where OpenArcanum keeps things**: every folder and file the app writes, with its full path, **Copy** and **Open folder** (see "Where your data is"); **Reset all data…** (see "Reset all data") |
| **Updates** | Version, **Check for updates automatically**, **Check now** (see "Updates") |
| **About** | Version, system, where the data folder is, and the credits for the word list and the interface icons |

## Admins: messaging features

Community Admin → **Features** (the Content and Messaging groups; the
other toggles are in docs/admin-guide.md "Feature toggles"). Each switch
applies to the whole community; turning one off removes the feature everywhere, and the
server refuses it even from modified clients. Every change is in the audit
log.

| Feature | Default |
|---|---|
| Attachments (and *Allowed attachments*: all kinds or images only; size limit under **Files**, up to 25 MB) | on, all kinds, 10 MB |
| Link previews | off |
| Reactions | on |
| Editing own messages | on |
| Deleting own messages | on |
| Typing indicators | on |
| Join and leave messages (and the channel they go to) | on, first text channel |

**Per channel** (Channels → **Edit…**): attachments, link previews,
reactions and history for new members can be turned off for one channel. A feature that's off for the
community stays off everywhere.

**Permissions** decide who may use what a channel allows. The defaults for
`@everyone` (docs/admin-guide.md "Permission defaults") include sending
messages, attaching files, adding reactions, embedding links (link
previews) and changing your own nickname; mentioning @everyone, @here and
all roles, and managing others' nicknames are admin-only by default. Channel
overrides can allow or deny add reactions, embed links and mention
@everyone per role or member.

### Reset all data

Settings → **Data & storage** → **Reset all data…** starts over: in the
web app, everything OpenArcanum keeps in that browser is deleted; on the
desktop, the profile is set aside (not deleted), as with "Forgot your
passphrase?". It doesn't ask for your passphrase. Your other devices aren't
affected.

If you own a community, the dialog says which and warns that your owner
keys are in this profile. It makes a passphrase for a recovery backup (copy
it with **Copy passphrase**), and **Copy recovery backup** copies a backup
of all your keys, owner keys included, encrypted with it, as text: paste
both into your password manager. Reset waits until you tick **I saved the
recovery backup and its passphrase somewhere safe.** To recover, choose
**Restore from backup** on first run and paste both in.

## Where your data is

Settings → **Data & storage** → **Where OpenArcanum keeps things** lists every
folder and file OpenArcanum writes on this computer, each with its full path
(**Copy**), what it holds, and **Open folder** (or **Open its folder** for a
file), which shows it in your file manager where the system has one. A
file not written yet says **not created yet**.

| Group | Paths (Linux defaults; `--home DIR` puts them under `DIR/config`, `DIR/data`, `DIR/cache`, `DIR/log`) |
|---|---|
| Settings: readable before unlock | `~/.config/OpenArcanum/` with `preferences.toml` (appearance, window, data folder, update checks, maintainer nodes, whether this device stays signed in), `updates.json` (update checks) and, only on a system without a keychain while this device stays signed in, `unlock-key` (the key that opens your profile; readable by your user account only) |
| Profile and messages: encrypted | `~/.local/share/OpenArcanum/` with `vault.bin` (keys, communities, settings), `state.bin` (drafts, read markers), `dms.bin` (direct-message history), `olm.bin` (DM encryption sessions), `unlock-guard.json` (wrong-passphrase wait) and `engine-sandbox` (the browser engine check) |
| Communities hosted on this computer | `~/.local/share/OpenArcanum/hosted/<id>/`, one folder per community you host ("Host a community on this computer"): `server/` is its community server's data directory (server keys, the database, which the storage key `server/storage.key` beside it encrypts, and `server/backups/`), `host.json` whether it should run. The folder names are random; which community each serves is in your encrypted profile. Folders are readable only by you; anyone who can read them as you can read the community, as on any server |
| Cache: encrypted, safe to delete | `~/.cache/OpenArcanum/` with `communities/` (community messages and files), `dm-files/` (DM files) and `updates/` (a downloaded update) |
| Logs | `~/.local/state/OpenArcanum/log/` (platformdirs' log folder) with `client.log` (and `.1` to `.3`), `crash.log`, and `hosted/<id>.log` (each hosted community server's own log, rotated at 4 MB, ids and sizes only) |
| System keychain | The entry "OpenArcanum profile key" with this profile's account name, while this device stays signed in on a system with a keychain (not after a lock) |

On Windows these are under `%LOCALAPPDATA%\OpenArcanum`, on macOS under
`~/Library/Application Support/OpenArcanum`, `~/Library/Caches/OpenArcanum` and
`~/Library/Logs/OpenArcanum`; the page shows the real paths. If you moved the
data folder (**Data folder** → **Choose…**), the data and cache entries
show the new place.

To back up everything, copy the settings and data folders while OpenArcanum
is closed: the profile in them stays locked with your passphrase, except
that on a system without a keychain the settings folder also holds
`unlock-key` while this device stays signed in, and that file opens the
profile. Leave it out of the copy (or lock OpenArcanum first, which deletes
it). The hosted communities' folders aren't locked by the passphrase:
keep that copy safe. An
exported backup (Settings → Security) is the way to move your keys to a new
device; community messages download again from their servers.

## Logs and crashes

OpenArcanum keeps a log on your computer only, in its log folder (the crash
notice and Settings show where; with `--home DIR` it's `DIR/log`). Nothing
is ever sent anywhere: there is no telemetry and no crash reporting service.

- `client.log` records what the app did (connections, errors) by ids and
  sizes. It never contains message text, file names you didn't choose to
  log, keys or passphrases. When something fails, the log keeps where in the
  code it happened and the kind of error, but not the error's own text unless
  the app wrote that text itself, because such text can quote what it failed
  on.
- `crash.log` gets the program's stacks if OpenArcanum ever crashes outright.
- If OpenArcanum didn't close normally last time, it tells you at the next
  start, with the log folder's location, in case you want to look or share it
  yourself.

## Updates

Settings → **Updates** shows the version you're running and the update
status.

- **Check for updates automatically** (on by default): OpenArcanum checks at
  startup and every 24 hours. Each check contacts the update host, which
  sees your computer's IP address and the time (nothing else identifies
  you). Turn it off and OpenArcanum contacts the host only when you click
  **Check now**.
- When a newer version has been downloaded and checked against OpenArcanum's
  signed release manifest, OpenArcanum asks: **Restart and install**, **Later**
  (ask again at the next check) or **Skip this version** (automatic checks
  won't offer it again; **Check now** still finds it). Nothing is ever
  installed without your click. Installing restarts OpenArcanum; your messages
  and settings are kept.
- OpenArcanum refuses an update whose signature, version, expiry or download
  doesn't check out, and says why in Settings → Updates. "Expired" can also
  mean your computer's clock is wrong.
- A copy that can't replace itself (run from source or a distribution
  package, or an AppImage in a folder you can't write) says a new version is
  available; update it the way you installed it.
- A build without a release key (development builds) says "Updates aren't
  configured for this build." and never checks.

Details: docs/release.md.
