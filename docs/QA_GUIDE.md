# OpenArcanum QA guide

This guide is for testing OpenArcanum by hand on a Linux machine: how to
start a complete local network (a relay, a maintainer node and a community
server) and three or four app windows, then fifteen scripted sessions with
the result to expect after every step, the known limitations, and how to
report a bug.

Steps marked **(real devices)** or **(real desktop)** can't be checked under
Xvfb or in a container: they need a microphone and camera, another OS, or a
desktop with a tray and a notification service.

## Contents

1. [Start up](#1-start-up)
2. [Scripted sessions](#2-scripted-sessions)
   1. [First run, unlock and backup](#session-1-first-run-unlock-and-backup)
   2. [Creating and claiming a community](#session-2-creating-and-claiming-a-community)
   3. [Invites and joining from search](#session-3-invites-and-joining-from-search)
   4. [Roles, permissions and every feature toggle](#session-4-roles-permissions-and-every-feature-toggle)
   5. [Moderation](#session-5-moderation)
   6. [Messaging features](#session-6-messaging-features)
   7. [Files](#session-7-files)
   8. [DMs, group DMs and DM files](#session-8-dms-group-dms-and-dm-files)
   9. [Device linking](#session-9-device-linking)
   10. [Voice, video and screen share](#session-10-voice-video-and-screen-share)
   11. [Retention](#session-11-retention)
   12. [The app icon, the interface icons and the tray](#session-12-the-app-icon-the-interface-icons-and-the-tray)
   13. [Hosting on this computer, offline and shut-down communities, relay failover](#session-13-hosting-on-this-computer-offline-and-shut-down-communities-relay-failover)
   14. [Statuses](#session-14-statuses)
   15. [A maintainer network: DMs with a node down or cut off](#session-15-a-maintainer-network-dms-with-a-node-down-or-cut-off)
3. [Known limitations](#3-known-limitations)
4. [How to file a bug](#4-how-to-file-a-bug)

### The cast

The sessions use the same people throughout. Each is an app window with its
own profile folder (`--home`):

| Who | Folder | Role |
|---|---|---|
| **Olive** | `.dev/client-a` | creates and owns the community |
| **Alice** | `.dev/client-b` | a member, later a moderator |
| **Bob** | `.dev/client-c` | an ordinary member |
| Olive's **laptop** | `.dev/client-a2` | a second device for Olive (session 9) |
| **Dana** | `.dev/client-d` | a newcomer (sessions 3 and 4) |

Passphrase for every profile: `correct horse battery staple`. Backup
passphrase: `backup pass phrase one`.

Sessions build on each other: run them in order, with the same network. Each
takes 10 to 30 minutes.

## 1. Start up

There are two ways to run the network. This section runs it directly on your
computer with `scripts/dev-up.sh` and opens each app window from source with
`make desktop`. The other runs it on a local Kubernetes cluster (kind) on your
computer, with each app window in its own container and its data in
`.dev/clients/NAME`:

```bash
make kind-up                 # cluster, images, chart; prints the claim code
make kind-proxy              # leave running
make client-image            # once
make client NAME=olive       # one terminal per person: alice, bob, carol…
```

`docs/kubernetes.md` has what you need for that and how it works. The
sessions below are the same either way; use the claim code `make kind-up`
prints, and the windows already point at the cluster's maintainer node. The
chart leaves the public directory off: for the directory steps, add
`OPENARCANUM_META_DIRECTORY_ENABLED: "true"` under `meta.env` in
`deploy/helm/openarcanum/values-kind.yaml` before `make kind-up`.
`make clean-dev` deletes every window's data; `make clean-all` also deletes
the cluster and the images.

### 1.1 What you need

- A Linux machine with a desktop session (X11 or Wayland) and this
  repository checked out.
- Go, uv and Qt WebEngine's system libraries: the README's "What to install"
  lists them (with the Arch and Debian/Ubuntu package lists). uv installs
  the right Python by itself.
- About 4 GB of free memory for four app windows.
- Real microphones, cameras (and, for the two-computer part, a second
  computer) only for the optional real-device parts of session 10.

### 1.2 Terminals

Open **five** terminals in the repository folder: one for the servers, one
per app window. All commands below are typed there.

### 1.3 Build (terminal 1)

```bash
make build
make client-sync
```

Expected: `make build` prints one `go build` line and leaves the programs in
`bin/` (`relay`, `community`, `meta` and `site`, plus test helpers);
`make client-sync` installs the app's Python packages.

Test files for the file sessions:

```bash
mkdir -p /tmp/qa
cp clients/shared/ui/brand/openarcanum-64.png /tmp/qa/icon.png
cp README.md /tmp/qa/readme.md
head -c 2097152 /dev/urandom > /tmp/qa/two-mb.bin
```

### 1.4 Start the network (terminal 1)

```bash
OPENARCANUM_META_DIRECTORY_ENABLED=true scripts/dev-up.sh reset
```

`reset` deletes `.dev/` first (every server and app profile from earlier
runs). `make dev-up` is the same without `reset` (it builds first). A
maintainer node serves the public directory only when `directory_enabled` is
on, and sessions 2, 3 and 15 use it, so keep
`OPENARCANUM_META_DIRECTORY_ENABLED=true` on every start below. Expected
output ends with:

```
claim code: openarcanum-claim:…
relay     ws://127.0.0.1:7000/ws   (log: .dev/logs/relay.log)
meta      http://127.0.0.1:7100    (log: .dev/logs/meta.log)
community data in .dev/community   (log: .dev/logs/community.log)
client:   cd clients/desktop && uv run python -m openarcanum --home ../../.dev/client-a --maintainer http://127.0.0.1:7100
Ctrl+C to stop.
```

Leave it running. The claim code is also in `.dev/claim.txt`. Ctrl+C stops
all three servers; `scripts/dev-up.sh` without `reset` (and with the same
environment variable) starts them again with their data.

- One `relay heartbeat failed … connection refused` warning at the top of
  `.dev/logs/relay.log` is normal (the relay starts a moment before the
  maintainer node and retries every 30 s).
- If `relay.log` says `address already in use`, something else holds port
  7000 (TCP or UDP) or 7100: stop it, or move the relay's media port with
  `OPENARCANUM_RELAY_MEDIA_UDP_PORT=7002 scripts/dev-up.sh`.

### 1.5 Start the app windows (terminals 2 to 5)

In each app terminal, once:

```bash
cd clients/desktop
```

then start one window per terminal:

```bash
uv run python -m openarcanum --home ../../.dev/client-a --maintainer http://127.0.0.1:7100   # Olive
uv run python -m openarcanum --home ../../.dev/client-b --maintainer http://127.0.0.1:7100   # Alice
uv run python -m openarcanum --home ../../.dev/client-c --maintainer http://127.0.0.1:7100   # Bob
```

(`make desktop NAME=olive` from the repository folder does the same with the
profile in `.dev/desktop/olive` instead; the sessions name the `.dev/client-*`
folders.)

- `--home` keeps that window's profile, logs and cache in its own folder, so
  several people can run on one machine.
- `--maintainer` points the app at the local maintainer node. Without it the
  app uses the built-in list (`https://maintainer-01.openarcanum.org`), the
  public network, not your local one. Instead of the flag you
  can also set the node in the window itself, before creating or unlocking
  the profile: **Change…** next to "Maintainer nodes" at the bottom of the
  Create your profile, Unlock, Restore from backup and Link to an existing
  device screens → remove the built-in nodes, add `http://127.0.0.1:7100`,
  **Save**. That list is kept for that `--home` (in
  `config/preferences.toml`) and wins over the flag; **Use the defaults**
  goes back to the flag or the built-in list. Session 1, steps 15 to 17,
  go through it.
- In a container with a small `/dev/shm`, also
  `export QTWEBENGINE_CHROMIUM_FLAGS=--disable-dev-shm-usage` first, or the
  browser engine runs out of shared memory with several windows.
- To stop a window, choose **Quit OpenArcanum** in its tray icon's menu or press
  **Ctrl+Q**. Where your desktop has a system tray, closing the window only
  hides it there (Session 12); without a tray, closing it quits. Started
  again with the same `--home`, it opens straight to the main window: a
  new profile stays signed in (without a keychain, through
  `config/unlock-key`). After **Lock now** it asks for the passphrase,
  also when started again; Settings → Security → **Ask for my passphrase
  when OpenArcanum starts** makes every start ask.
- The terminal shows some browser-engine noise when a window starts
  ("Sandboxing disabled by user.", Vulkan, GLX, "Failed to connect to the
  bus"): harmless.
- The window titles are all "OpenArcanum": the name at the bottom left of each
  window (next to **Lock now** and **Settings**) tells you whose it is.

### 1.6 Starting over

Close all windows, press Ctrl+C in terminal 1, and run
`OPENARCANUM_META_DIRECTORY_ENABLED=true scripts/dev-up.sh reset`: every
server and profile starts fresh. Use the new claim code.

With the kind setup: close the windows, then `make clean-dev` (deletes
`.dev/`, every copy's profile; it asks first) and `make kind-down`, or
`make clean-all` for both plus the images and build output. Then start
again from `make kind-up`.

## 2. Scripted sessions

Each step is an action and **→** what you should see. Labels in **bold** are
the exact text on screen. Buttons that are only an icon are named by their
tooltip (hover to see it): for example the padlock is **Lock now** and the
gear is **Settings**. Message actions (**Reply**, **Add reaction**, **Edit**,
**Pin**, **Delete**, **Select**, **Report**, **Copy text**) appear when you
hover a message.

Toasts (small notices at the bottom right) disappear after a few seconds.

### Session 1: First run, unlock and backup

Olive's window (`client-a`).

1. Start Olive's window.
   → A dark window: "**Welcome to OpenArcanum**", "There are no accounts. Your
   keys live in an encrypted profile on this device." and three buttons:
   **Create a profile**, **Link to an existing device**, **Restore from
   backup**. Top right of the card: **Dark** (highlighted), **Light**,
   **System**.
   1a. Click **Light**, then **Dark**.
   → The whole window turns light, then dark again, at once.
   `.dev/client-a/config/preferences.toml` says `theme = "dark"`.
2. Click **Create a profile**.
   → "**Create your profile**" with **Passphrase** (and **Show** and
   **Generate** beside it), **Repeat passphrase** and **Name this device**
   (filled in: "Linux device"). The **Dark/Light/System** switch is here
   too. Every field has a darker box with a clear border, and a blue ring
   when it has the focus.
   2a. Click **Generate**.
   → Six lowercase words appear in the field, readable; the meter says
   "**Very strong**"; **Repeat passphrase** is filled in; a box says "Save
   this passphrase in your password manager now. …" with **Copy
   passphrase** (click it: the button says **Copied**, and pasting
   elsewhere gives the six words). **Hide** turns the field back to dots.
   Click **Generate** again: different words. (The scripted session uses
   its own passphrase: the next steps type over these.)
3. Type `short` in both passphrase fields.
   → Under the first field: "**Very weak**" and "**Use at least 10
   characters.**"
4. Click **Create profile**.
   → Red text "**Use at least 10 characters.**"; nothing is created.
5. Type `correct horse battery staple` in the first field and something else
   in the second, then **Create profile**.
   → "**The passphrases don't match.**"
6. Type `correct horse battery staple` in both, then **Create profile**.
   → The meter said "**Strong**". The main window opens: "**No community
   selected**", "Join one with an invite, find a public one, or claim your
   own server." and **Add a community**. `.dev/client-a/config/unlock-key`
   now exists (`ls -l`: `-rw-------`): this device stays signed in, and
   without a keychain the key is kept in that file.
7. Click **Lock now** (the padlock, bottom left).
   → "**Unlock**", "Enter the passphrase for this device.", a passphrase
   field, **Unlock** and **Forgot your passphrase?** `config/unlock-key` is
   gone: a locked profile needs the passphrase, also after a restart.
8. Type a wrong passphrase and press Enter.
   → "**Wrong passphrase. Wait 1 s to try again.**" and the button shows
   **Wait 1 s** until it's usable again (each wrong try waits longer).
9. Type `correct horse battery staple`, press Enter.
   → The main window again.
10. Click **Settings** (the gear).
    → Settings opens in the middle of the window, in place of the channel
    (the rail and the channel list stay on the left), titled **Settings**
    with **Close** at the top right, with sections **Security, Identity, Communities, Devices,
    Privacy & DMs, Appearance, Notifications, Voice & video, Network, Data &
    storage, Updates, About**. Security shows **Passphrase**, **Signing in**
    with **Ask for my passphrase when OpenArcanum starts** off ("OpenArcanum opens
    without your passphrase on this device. This system has no keychain, so
    the key that opens your profile is kept in a file only your user account
    can read: …/config/unlock-key" and a yellow note "On this system your
    profile opens without a passphrase for anyone who can read your user's
    files …"; that is expected without GNOME Keyring or KWallet; with one it names
    the keychain and there is no yellow note), **Auto-lock** and **Backup**
    with a yellow **Out of date** and "Never exported".
    10a. Quit (**Ctrl+Q**) and start client-a again with the same command.
    → The main window opens without asking for the passphrase. Open
    **Settings** → **Security** again.
11. Under Backup click **Export backup…** (each part of Security is a box
    with its button in a footer under a line), type `backup pass phrase one`
    twice (there's a **Generate** here too), type `/tmp/qa/olive-backup.fbak` in **Save to** (or pick it with
    **Browse…**) and click **Save backup**.
    → Toast "**Backup saved.**" Close and reopen Settings: Backup shows a
    green **Up to date** and "Last exported …" with the time.
12. Click **Change passphrase…**, enter the current passphrase, then
    `another horse battery staple` twice, **Change**.
    → Toast "**Passphrase changed.**" Do it again to change it back to
    `correct horse battery staple`.
13. Set **Lock after idle** to **1 minute** (a green **Saved** appears),
    close Settings and don't touch the window for a minute.
    → The window locks itself, and `config/unlock-key` is gone as after
    **Lock now**. Unlock, and set it back to **Never** (the default for new
    profiles).
14. Lock (**Lock now**) and click **Forgot your passphrase?**
    → "**Forgot your passphrase?**" explains the three ways back (link from
    another device, restore a backup, start a new profile) and offers **Set
    this profile aside and start over…** (don't click it). Click **Back** and
    unlock.

15. Lock (**Lock now**).
    → At the bottom of the Unlock screen: "**Maintainer nodes:
    127.0.0.1:7100 (from the launch option)**" and **Change…** (the same line
    is on the Create your profile screen).
16. Click **Change…**.
    → A "**Maintainer nodes**" dialog: "The defaults: the nodes given with
    the --maintainer launch option.", `http://127.0.0.1:7100` with
    **Remove**, **Add a node** with **Add**, **Use the defaults**, **Cancel**
    and **Save**. Type `http://node.example.invalid` and press **Add**.
    → Red under the field: "**Use https:// (http:// only for this
    computer).**"; the list is unchanged. Type `http://localhost:7100` and
    press Enter.
    → It's added; the dialog says "Your own list, kept on this device only."
    Click **Remove** next to `http://127.0.0.1:7100`, then **Save**.
    → Toast "**Maintainer nodes saved.**"; the line reads "**Maintainer
    nodes: localhost:7100 (your own list)**".
17. Unlock, **Settings** → **Network**.
    → **Maintainer nodes** shows the same line. Click **Change…** → **Use
    the defaults** → **Save**.
    → "**Maintainer nodes: 127.0.0.1:7100 (from the launch option)**" again.
    (The list is per device: Alice's and Bob's windows still show theirs.)

18. **Settings** → **Data & storage**.
    → **Cache**, **Data folder**, then **Where OpenArcanum keeps things**:
    groups (Settings: readable before unlock; Profile and messages:
    encrypted; Cache: encrypted, safe to delete; Logs; System keychain;
    Window toolkit), each entry with a name, what it holds and its full
    path in a box, e.g. `…/.dev/client-a/data/vault.bin` (the full path) for
    **Profile**, `…/.dev/client-a/log/client.log` for **App log**,
    "OpenArcanum profile key / …" for **Keychain entry**, `…/config/unlock-key`
    for **Signed-in key** (no keychain here). A long path scrolls
    sideways in its box. **Copy** copies the path; **Open folder** opens the
    folder in a file manager if the system has one (without one: a red
    message "This system has no file manager OpenArcanum can open. …").
19. Press **Escape**.
    → Settings closes: the channel and its message box are back, and the
    focus is on the gear. Open **Settings** again and click **Direct
    messages** (the two speech bubbles).
    → Settings goes and Direct messages opens in its place. Click **New
    group DM** (the plus) next to the title.
    → A red message bottom right: "**Join a community first: people are
    found through communities.**" with **Copy** and **Dismiss** (an X).
    Wait 10 seconds and click its text: it stays. Select part of the text
    with the mouse; **Copy** puts the whole message on the clipboard.
    **Dismiss** closes it. (A
    blue-bordered notice such as "Backup saved." goes away by itself.)

**Restore from backup** is in session 2, step 13, once the backup has a
community in it.

### Session 2: Creating and claiming a community

Olive's window; the claim code from terminal 1 (or `.dev/claim.txt`).

1. Click **Add a community** (the plus in the left rail).
   → No dialog: the middle of the window (where the messages go) becomes the
   page "**Add a community**" with a line of explanation and four boxes
   side by side: **Join with an invite**, **Find a community**, **Host on
   this computer** and **Host on your own server**, each with a line under
   it; the first is selected. The left and right arrow keys move between
   them; the **×** at the top right (or Escape) closes the page.
2. Click **Host on your own server**.
   → Three boxes: **1. Copy the community server** ("The same program this
   app uses to host communities, for linux-amd64 …", **Save to** with a
   path in Downloads, **Browse…** and **Save the community server**);
   **2. Set it up and start it** with the `community init …` and
   `community run …` commands in a box that scrolls sideways (the page keeps
   its width) with **Copy**; **3. Claim it** with a **Claim code** field and
   **Next**.
3. Paste an invite or any other text and click **Next**.
   → "**That doesn't look like a claim code.**"
4. Paste the claim code (the whole `openarcanum-claim:…` line) and click
   **Next**.
   → The page shows "**Set up your community**": Name, Description, Tags, Visibility, Join
   policy (with "Not listed: only people with an invite link can find it,
   whatever the join policy." under them; it changes as you pick), Keep
   messages (days) 30, Keep files (days) 7, **Largest file (MB)** 10, Your
   display name.
5. Name `QA Town`, Description `A place to test OpenArcanum`, Tags `qa,
   testing`, Visibility **Public (listed in the directory)**, Join policy
   **Invite only**, Your display name `Olive`. Click **Claim and create**.
   → The page closes and "**Your community is ready**" opens with an invite (`openarcanum-invite:…` in a
   box that scrolls sideways, "it works for 7 days; make more in Community
   settings → Invites & join policy"), **Copy invite**, a notice about the owner key and the only
   button **Export backup…**
6. Click **Export backup…** and save a new backup as
   `/tmp/qa/olive-backup-2.fbak` (same backup passphrase).
   → Toast "**Backup saved.**" The community opens: **QA Town** (with a
   chevron) at the top, **CHANNELS** with **# general**, "**No messages yet.
   Say hello!**", the composer "Message #general", and a yellow banner "**If
   you lose your owner key, nobody can rotate the server key or move the
   community. Add a second owner.**" with **Add an owner**.
7. Hover the **Q** icon in the rail.
   → "**QA Town**" and "**Connected**".
8. Click **QA Town**.
   → The community menu: **NOTIFICATIONS** with **All messages** (with a
   check mark), **Mentions only, Nothing**, then **Mute community, Mark all
   as read, Invite people, Community Admin, Reports, Approval queue, Audit
   log, Search messages, Your profile here, Copy community ID, Leave
   community** (red). Press Escape. Right-clicking the **Q** icon in the
   rail opens the same menu. Under the community name there is a red
   **Community Admin** button.
9. Type `hello from Olive` in the composer and press Enter.
   → The message appears with Olive's name and the time.
10. Try the claim code again on Bob's window (start it, **Create a
    profile**, then **Add a community** → **Host on your own server** →
    paste → **Next** → fill in → **Claim and create**).
    → Red text "**this server has already been claimed**" in the box's
    footer (a claim code works once). Close the page (**×**). Bob's profile
    stays for session 3.

Adding a second owner (needs Alice, so do it after session 3):

11. Olive: click **Add an owner** in the banner.
    → Community Admin opens in the middle of the window, in place of the
    channel and the member list (the channel list stays on the left),
    titled **Community Admin** | **QA Town** with **Close** at the top
    right, at **Ownership**: Owners (Olive, **founder**,
    **you**), **Add an owner** with a member list and **Invite as owner**,
    **Server backup**, **Rotate server key**, **Move to new hardware**.
    Clicking **#general** in the channel list leaves it for the channel;
    reopen it from the banner.
12. Pick **Alice**, click **Invite as owner**.
    → Toast "**Invitation sent.**" Alice gets a banner "**You've been
    invited to become an owner of this community.**" with **Review**; she
    clicks it, then **Accept** → "**Owner key sent. An owner needs to
    confirm it.**" Olive's Ownership section shows Alice with "Their owner
    key arrived. Confirm to add it." and **Confirm** → "**Alice is now an
    owner.**" The single-owner banner is gone.

Restore (any time after step 6):

13. Start a fifth window with a new folder: `uv run python -m openarcanum
    --home ../../.dev/client-r --maintainer http://127.0.0.1:7100`. Click
    **Restore from backup**, type `/tmp/qa/olive-backup-2.fbak` in **Backup
    file** (or pick it with **Choose file…**), type a wrong backup passphrase and a new
    passphrase for this device, **Restore**.
    → "**Wrong backup passphrase.**"
14. Type `backup pass phrase one`, **Restore**.
    → The main window with **QA Town** (**Connected**) and its history (it comes
    back from the server). Close this window; it isn't used again.

### Session 3: Invites and joining from search

Olive, Alice (`client-b`), Bob (`client-c`). Alice's and Bob's windows need a
profile (**Create a profile** as in session 1) if they don't have one yet.

1. Olive: **QA Town** → **Invite people**.
   → Community Admin at "**Invites & join policy**": a **Join policy**
   box with **Join policy**, **Maximum members** (0 = no limit), "Listed in
   directory search, but joining needs an invite …" and **Visibility…**,
   and **Save changes** in the box's footer; below the box, **Invites**
   with **Create invite…** and a table (Label, Created by, Uses, Expires,
   Grants) listing "First invite" (**active**).
2. Click **Create invite…**.
   → "**Create invite**": Label, **Expires after** (1 hour … Never; 7 days
   chosen), **Max uses** (0 = unlimited), "Roles granted on joining",
   **Needs approval**, **Public join key**.
3. Label `for Alice`, Max uses `1`, **Create**.
   → The invite appears in the dialog with **Copy invite**; after **Close**,
   the table shows "for Alice", **active**, Uses "0 / 1".
4. Alice: **Add a community** → **Join with an invite** → paste the invite
   → **Continue**.
   → "**Join community**": QA Town, "1-10 members", **Invite only**, the
   description and tags, "Admins of this community can read everything you
   post here.", **Identity** with "A new identity (recommended)", and
   "Display name in this community".
5. Display name `Alice`, **Join**.
   → The community opens with Olive's message, then "**Back up your
   keys**" ("Your backup is out of date. …") with **Later** and **Export
   backup…** Click **Later**. A yellow "Your backup is out of date." banner
   stays at the top until she exports one.
6. Olive: look at #general.
   → "**→ Alice joined the community · 17:10**" (your time). In Invites &
   join policy the invite shows Uses "1 / 1".
7. Bob: **Add a community** → **Find a community**.
   → It searches right away: "Searches the maintainer nodes in Settings →
   Network. Private communities never appear here." and a result **QA
   Town**, "1-10 members", **Invite only**, the description, `#qa`
   `#testing`. Typing `QA` or `#qa` and Enter finds it too.
8. Click the result, display name `Bob`, **Join**.
   → The dialog already warned "This community needs an invite; you can
   still ask, but the server will refuse without one."; now it says "**This
   community needs an invite to join.**" Cancel.
9. Olive: Invites & join policy → **Join policy** = **Request to join
   (approval queue)** → **Save changes**.
   → Toast "**Saved.**"
10. Bob: **Add a community** → **Find a community** → the result now says
    **Request to join** → click it → `Bob` → **Join**.
    → A toast titled "**Pending approval**": "Request sent. You can read the
    community once an admin approves you." The community shows "Waiting for
    an admin to approve you." and a blue banner "Pending approval: an admin
    has to let you in. This updates by itself."
11. Olive (close Community Admin first if it's open).
    → A red square badge **1** on the **Q** rail icon, next to **QA Town**,
    and on the **Community Admin** button. **QA Town**'s menu shows it on
    **Community Admin** and **Approval queue**. Click the **Community
    Admin** button: it opens on **Approval queue** (badge **1** there and on
    the **JOINING** heading) with Bob, his key and "Asked …", **Approve**
    and **Deny**. Click **Approve**.
    → The badges go.
12. Bob: wait a few seconds.
    → He's in: #general with the history and "→ Bob joined the community".
13. Revoking: Olive creates another invite labelled `to revoke`, then clicks
    **Revoke** on its row.
    → The row shows **revoked**. Start Dana's window (`--home
    ../../.dev/client-d`), create a profile, **Add a community** → **Join with
    an invite** → paste it → **Continue** → `Dana` → **Join** → "**That
    invite has been revoked.**" Cancel; Dana is used again in session 4.

Anyone can find and join (a temporary window, Erin):

14. Olive: Community Admin → **Visibility**.
    → A box **Who can find and join**: "Anyone can find it in directory
    search and ask to join; an admin approves each request (Approval
    queue)." with the four combinations listed, **Change join policy…** and
    **Let anyone find and join**; below it the **Visibility** box with
    **Save changes**.
15. Click **Change join policy…**.
    → **Invites & join policy** opens. Click **Visibility…** to go back.
16. Click **Let anyone find and join** → **Make it public**.
    → Toast "**Saved.**"; the box now says "**Anyone can find it in
    directory search and join straight away, with no invite.**" and the
    button is gone.
17. Start a window `uv run python -m openarcanum --home ../../.dev/client-e
    --maintainer http://127.0.0.1:7100`, **Create a profile**, then **Add a
    community** → **Find a community**.
    → **QA Town** with **Open**. Click it.
    → "**Join community**" says "This community is open: anyone who finds
    it can join, no invite needed." Display name `Erin`, **Join**.
    → Erin is in #general at once (then **Later** on the backup reminder).
    Close Erin's window.
18. Olive: Invites & join policy → **Join policy** = **Request to join
    (approval queue)** → **Save changes** (as it was for the next sessions).

Optional: Settings → Network → **Search the directory on
this device**: the Find pane then says "Searching on this device, in a copy
of the directory (… communities, downloaded just now). Maintainers don't see
your searches." and offers **Refresh copy**.

### Session 4: Roles, permissions and every feature toggle

Olive, Alice, Bob; Dana for steps 21 to 23.

Roles and permissions:

1. Olive: Community Admin → **Roles**.
   → **Admin** and **@everyone** with their permissions, **Create role…**
2. **Create role…** → Name `Moderators`, Color `#3366ff`, tick **Display
   separately**, and under the permission groups (General, Moderation, Text,
   Voice and video, Administrator) tick **Kick members**, **Ban members**,
   **Time out members**, **Manage messages**, **View audit log** →
   **Create**.
   → The Roles list shows **Moderators** (blue, **shown separately**) with
   those permissions.
3. **Members** → **Roles…** next to Alice → tick **Moderators** → **Save**.
4. Close settings, click **Members** (two people, top right).
   → Alice listed in blue under her own heading, **Moderators** (1); Olive
   with **owner**.
5. Alice: **QA Town**.
   → Her menu now has **Community Admin**, **Audit log** and
   **Reports**, and the red **Community Admin** button appears above her
   channel list; her Community Admin has only **Invites & join policy,
   Members, Bans, Reports, Audit log** (no Roles: she lacks Manage roles).
   Bob's menu has none of these, and he has no Community Admin button.
6. Olive: in the channel list click **Create text channel** (the plus) →
   `staff` → **Create**. Then Community Admin → **Channels** →
   **Permissions…** next to staff.
   → "**Permissions in #staff**": a card per role (@everyone, Moderators,
   Admin) with **Deny** (an X), **Inherit** (a slash) and **Allow** (a check
   mark) for each channel permission, and a member picker with **Add member
   override**.
7. On **@everyone** click **Deny** next to **View channels**; on
   **Moderators** click **Allow** next to **View channels**; **Save**.
   → Alice still sees **# staff**; Bob's channel list no longer has it.
8. Channels → **New category** → `Projects` → **Create**; then **Edit…**
   next to staff → **Category** = Projects, **Topic** `Moderators only` →
   **Save**.
   → The sidebar shows **Projects** (with a chevron) with staff under it;
   opening #staff shows "Moderators only" next to its name.
9. Roles → **Edit…** next to @everyone → tick **Create invite** → **Save**.
   → Bob's community menu now has **Invite people** (by default only admins
   can invite).
10. Channels → **Edit…** next to general → **Slowmode** = **10 seconds** →
    **Save**.
    → Bob sees "**Slowmode is on: one message every 10 seconds.**" above
    the composer; a second message within 10 s is refused with
    "**slowmode is on: wait 10 more seconds**". Olive and Alice (Manage
    messages) aren't limited. Set it back to **Off**.

Feature toggles (Community Admin → **Features**; tick or untick, then
**Save changes** at the bottom → toast "Saved."). After each change look at
Bob's window (give it 2 seconds), then turn the feature back on:

11. **Attachments** off.
    → Bob's **Attach files** (the paperclip) next to the composer is gone.
12. **Allowed attachments** = **Images only (PNG, JPEG, GIF, WebP)**.
    → Bob's Attach files dialog says "… images only"; sending
    `/tmp/qa/readme.md` is refused: "**readme.md: only images (PNG, JPEG,
    GIF or WebP) can be attached in this community**". Set it back to **All
    kinds of files**.
13. **Reactions** off.
    → **Add reaction** disappears from message actions.
14. **Custom emoji** off.
    → Community Admin → **Emoji** says "**Custom emoji are turned off.
    Turn them on under Features.**"
15. **Voice channels** off.
    → (Create a voice channel first: **Create voice channel** (the speaker)
    next to CHANNELS → `Lounge`.) Lounge disappears for everyone and the
    **Create voice channel** button goes. Video and Screen share are checked
    in session 10.
16. **Search** off.
    → **Search** (the magnifying glass) at the top and **Search messages**
    in the community menu are gone.
17. **Member contact cards** off.
    → Bob's **Your profile here** dialog has no "Publish a contact card
    here" box.
18. **Editing own messages** off.
    → **Edit** is gone from Bob's own messages.
19. **Deleting own messages** off.
    → **Delete** is gone from Bob's own messages (Olive, with Manage
    messages, still has it).
20. **Typing indicators**: with it on, Bob typing in #general shows "**Bob
    is typing…**" under Olive's messages; off, nothing.
21. **Link previews** on (off by default). Bob posts a message with a link
    to any public web page you like.
    → If the community server can reach the internet, a card with the
    page's title (and description and image if it has them) appears under
    the message; the server fetched it, not the app. A link written as
    `<https://…>` gets no card. Turn it off again.
22. **New members see history** and **Join and leave messages** off. Make a
    new invite and join with Dana (**Add a community** → **Join with an
    invite** → `Dana` → **Join**).
    → Dana's #general says "No messages yet. Say hello!" (she sees only what
    is posted after she joined), and nobody sees "→ Dana joined the
    community".
23. Turn both back on.

Per channel (Channels → **Edit…** next to a channel → "Features in this
channel"):

24. general → **Reactions** = **Off in this channel** → **Save**.
    → No **Add reaction** in #general; other channels still have it. Set it
    back to **Community setting**. The dialog also has **Attachments**,
    **New members see history** (and **Link previews** while previews are
    on), and **Keep messages / Keep files (days)** (session 11).

Every change is listed under Community Admin → **Audit log** ("changed
turned search off", …).

### Session 5: Moderation

Olive (owner), Alice (Moderators), Bob. Everyone in #general.

1. Alice: **Members** → click **Bob** (a right-click opens the same menu).
   → **View profile, Message, Mention, Add as contact, Copy user ID**, a
   line, then **Time out…, Kick…, Ban…** in red. Escape. Right-click
   Bob's name on his message, his avatar, and (Olive) the row next to Bob
   in Community Admin → **Members**: the same menu (Community Admin's has no
   **Mention**; Olive's also has **Change nickname…** and **Roles…**).
   Bob right-clicking Alice sees only **View profile, Message, Mention, Add
   as contact, Copy user ID**; right-clicking himself, **View profile, Copy
   user ID, Edit your profile here**. Alice: Bob's menu → **Mention**.
   → "@Bob " is typed in the message box, with the focus there; send it.
   Right-click the **@Bob** in the sent message: Bob's menu again. From the
   keyboard: Tab to a message's author name, press **Shift+F10** (or the
   Menu key): the menu opens with the focus on **View profile**; Escape
   puts it back on the name.
2. **Time out…** → "**Time out Bob**" (Duration 60 seconds … 28 days,
   Reason) → **60 seconds**, reason `cool down` → **Time out**.
   → Toast "Bob is timed out." Bob's composer is replaced by "**You're timed
   out. You can post again at 17:15.**"; the member list shows **timed out**
   next to Bob for moderators.
3. Alice: Bob's menu → **Remove timeout**.
   → Bob's composer is back.
4. Bob: hover Olive's message → **Report** (the flag) → reason `testing
   reports` → **Report**.
   → A toast starting "**Reported.**" says the moderators will take a look. Olive
   and Alice (moderators) get no pop-up in the window; instead a red square
   badge **1** appears on the **Q** icon (top left), next to **QA Town**,
   and on the **Community Admin** button; their desktop shows a
   notification "**New report**: Bob reported a message by Olive". Bob sees
   no badge.
5. Olive: **QA Town**.
   → **Community Admin** and **Reports** carry the badge **1**. Click
   **Community Admin**: it opens on **Reports**, with **1** on the
   **MODERATION** heading and on **Reports**.
   → "Olive in #general", "Reported by Bob · …", "“testing reports”",
   **Jump to message**, **Resolve**, **Dismiss**. Click **Resolve** → toast
   "Resolved."; every badge goes, in Olive's and Alice's windows; tick
   **Show resolved and dismissed** → the row shows **resolved** and
   "resolved by Olive".
6. **AutoMod** → **New rule…** → Name `No spam`, Looks for **Keywords and
   patterns**, Keywords `spam*`, tick **Alert moderators** (**Block the
   message** is ticked) → **Save rule**.
   → Toast "Rule saved."; the rule shows **on**, "Keywords → block, alert".
7. Bob posts `buy spammy stuff`.
   → Bob: "**AutoMod blocked your message: it contains a blocked word (rule
   “No spam”).**"; the message isn't posted. Olive and Alice: the badge **1**
   again (icon, community name, Community Admin, Reports), and a desktop
   notification "**AutoMod alert**: Rule “No spam”: Bob, blocked".
8. Bob posts `message one`, `message two`, `message three`. Alice
   right-clicks **# general**.
   → **Mute channel, Mark as read, Select messages to delete, Delete recent
   messages…** Choose **Delete recent messages…**, How many `2`, **Delete**.
   → "**Deleted 2 message(s).**"; everyone sees two "Message deleted".
   (**Select messages to delete**, or **Select** on a message, lets you tick
   up to 100.)
9. Alice: hover Olive's message → **Pin**.
   → Toast "Pinned."; Bob's **Pinned messages** (the pin, top) lists it.
10. Olive: **Member screening** → **10 minutes** → **Save changes**. Make a
    new invite. Alice: Bob's menu → **Kick…** → reason `testing kick` →
    **Kick**.
    → Bob: red banner "**You were removed from this community.**" with
    **Dismiss**.
11. Bob: **Dismiss**, then **Add a community** → **Join with an invite**
    with the new invite → `Bob` → **Join**.
    → He's back, but his composer says "**New members can post from
    17:26.**" Olive: Member screening → **Off** → **Save changes** → Bob can
    post again.
12. Bob posts `bob says something`. Olive: Bob's menu → **Ban…**.
    → "**Ban Bob**": Reason, **Delete message history** (Don't delete any,
    Last hour, … Last 7 days). Choose **Last hour** → **Ban**.
    → "**Bob was banned. 1 message(s) deleted.**" (messages since he
    rejoined); Bob: "**You were banned from this community.**"
13. Olive: **Bans** → Bob with "No reason" and the time → **Unban** → "No
    bans."
14. **Audit log**.
    → Newest first: "unbanned …", "banned Bob and deleted 1 message from the
    last hour", "kicked Bob: testing kick", "changed member screening", …,
    each **valid** (or **server** for the server's own entries). Action =
    **Ban** → **Apply filters** shows only the ban.
15. Bob: **Dismiss**, rejoin with the invite (he's needed in the next
    sessions).

Not covered here: **Raid revoke…** on an invite (removes everyone who joined
with it in the last N hours) and **Replace member…** in Members.

#### Session 5b (optional): a dedicated relay

A second community on a relay dedicated to it (docs/operations.md
"Dedicated relays"). Keep `scripts/dev-up.sh` running (its maintainer node
is used). On this one computer every window connects from 127.0.0.1, the
same address as the community server, and a relay never blocks the
server's own address, so this session checks the dialog and the flow; the
block itself (another address refused) is checked by
`clients/desktop/tests/test_e2e_moderation.py::test_ban_blocks_network_on_dedicated_relay`
and `clients/desktop/tests/test_ui_netblock.py`, which connect from other loopback
addresses.

1. In a new terminal:

   ```bash
   mkdir -p .dev/ded
   bin/community init -data .dev/ded/community -relay ws://127.0.0.1:7010/ws -maintainer http://127.0.0.1:7100 | tee .dev/ded/claim.txt
   OPENARCANUM_COMMUNITY_SERVER_PUBLIC_KEY_FILE=.dev/ded/server.pub bin/community run -data .dev/ded/community &
   OPENARCANUM_RELAY_LISTEN=127.0.0.1:7010 OPENARCANUM_RELAY_MEDIA_UDP_PORT=7011 \
     OPENARCANUM_RELAY_DEDICATED_SERVER_KEY_FILE=.dev/ded/server.pub \
     OPENARCANUM_RELAY_BLOCK_FILE=.dev/ded/blocks.json bin/relay
   ```

   → The relay logs "dedicated relay serving its community server" within
   10 s, and the community server "attached to relay … dedicated=true".
2. Olive: **+ → Host on your own server** with the claim code from
   `.dev/ded/claim.txt`, community "Guarded". Bob joins it with its invite.
3. Olive: Bob's menu → **Ban…**.
   → Below **Delete message history**: **Also block their network address
   on our relay**, with a hint that shared addresses (home, office, mobile
   carrier, VPN or Tor exit) are blocked too and that a VPN gets around it.
   Ticking it shows **Block the address for** (1 day, 7 days, 30 days,
   Until they are unbanned).
4. Tick it, keep 7 days → **Ban**.
   → "**Bob was banned. Their address is the community server’s own
   network, so it wasn’t blocked.**" (everything here is 127.0.0.1); Bob:
   "**You were banned from this community.**" **Audit log**: "banned Bob
   (network address block requested)" and "couldn't block the network
   address of Bob: it is the community server's own". No address appears
   anywhere.
5. In the first community (the ordinary relay), Olive's **Ban…** dialog has
   no such option.
6. Stop the two processes (Ctrl+C, then `kill %1`); `rm -rf .dev/ded`
   removes them.

### Session 6: Messaging features

Olive and Alice in #general.

1. Alice posts this (one line):

   ```
   **bold** *italic* ~~strike~~ `code` ||secret|| and [label](https://example.invalid/page)
   ```

   → Olive sees bold, italic, struck-through and code text, a hidden
   spoiler (click it: "secret" shows), and "label (https://example.invalid/page)".
2. Olive clicks "label".
   → "**Link**": "OpenArcanum never opens links itself, …", the address,
   **Close** and **Copy link**. Nothing opens.
3. Alice types ```` ```python ````, Shift+Enter, `print('hi')`, Shift+Enter,
   ```` ``` ````, Enter.
   → A code block labelled "python".
4. Olive opens #staff. Alice posts `@Olive please look at this` in #general.
   → Olive's **# general** shows a red **@1**, and the **Q** rail icon a
   **1** (its tooltip: QA Town, Connected, 1 mention). Clicking **@1** jumps to the
   message, highlighted with a yellow bar.
5. Olive: hover it → **Reply** (the curved arrow).
   → "**Replying to Alice**" with **Cancel** above the composer. Post `on
   it` → Alice sees the quote "Alice: @Olive please look at this" (with an
   arrow) above it; clicking that quote jumps to the original (it
   flashes).
6. Olive: **Edit** (the pencil) on "on it" → `on it now` → **Save**.
   → Alice sees "on it now (edited)".
7. Olive posts `oops wrong channel`, then **Delete** (the trash can) →
   "**Delete message?**" "This deletes it for everyone." → **Delete**.
   → "Message deleted" for both.
8. Alice: **Add reaction** (the smiley with a plus) on "on it now" → 👍.
   → "👍 1"; Olive clicking it makes "👍 2", clicking again "👍 1".
9. Olive: Community Admin → **Emoji** → Name `party`, Image
   `/tmp/qa/icon.png` (or **Browse…**) → **Upload**.
   → "**Added :party:.**" and the emoji row (4 KB).
10. Alice: **Custom emoji** next to the composer → the emoji → type
    `party time` → Enter.
    → The message shows the image followed by "party time". Its **Add
    reaction** menu lists 👍 ❤️ 😂 🎉 😮 😢 **:party:** **Other…**; reacting with
    :party: shows the image with 1.
11. Olive opens #staff; Alice posts `unread one` in #general.
    → **# general** turns bold with a dot, and nothing pops up inside
    Olive's window (new messages show only as these marks; the desktop
    notification is separate). Right-click it → **Mark as read** → normal
    again. Alice posts `unread two`; Olive opens #general → a red **New**
    line just above "unread two".
11a. Olive opens #staff; Alice posts `unread three` in #general. Olive
    right-clicks the **Q** rail icon → **Mark all as read**.
    → **# general** is normal again. Right-click the icon again →
    **Mentions only** (under NOTIFICATIONS): Alice posts `quiet please` →
    no desktop notification for Olive; `@Olive loud` → one. Settings →
    **Notifications** → **Each community** shows QA Town at **Mentions
    only**; set it back to **Default (as above)**. Focus the icon with Tab
    and press **Shift+F10**: the same menu opens with the focus on the
    checked level; Escape puts the focus back on the icon.
11b. **(sounds; speakers or headphones on)** Olive: **Settings** →
    **Notifications** → **Sounds**.
    → **Play sounds** (on), **Sound volume** (50%), and a switch with a
    **Play** button for **New message**, **Direct message**, **Mention**,
    **Someone joins your call**, **Someone leaves your call**, **Mute and
    unmute**, **Deafen and undeafen**, **Call connected and disconnected**.
    Each **Play** gives a short, soft tone (two going up for joins, down for
    leaves, a quiet click pair for mute and deafen); none is harsh or long.
    Close Settings and open #staff. Alice posts `ding` in #general → one soft
    tone for Olive. Alice posts five lines quickly → one tone, not five.
    Olive opens #general; Alice posts `here` → no tone (Olive is reading
    it). Olive opens #staff; Alice posts `@Olive ping` → a brighter two-note
    tone (a mention). Settings → **Mention** off → Alice posts `@Olive
    again` → no tone; switch it back on. **Play sounds** off → nothing plays
    for anything; back on. Olive's status → **Do not disturb** → Alice's
    messages make no sound; back to **Online**.
12. Olive: **Search** (the magnifying glass) → `party` → Enter.
    → "#general · Alice · …" with the message; clicking it jumps there.
13. Alice: **QA Town** → **Your profile here** → Nickname `Ali`, Pronouns
    `she/her` → **Save**.
    → Olive's member list shows "Ali she/her"; clicking her → **View
    profile** → "Ali", "Display name: Alice", "she/her", Online, Moderators,
    "Member since …".
14. Alice posts `@everyone hello all`.
    → It's posted but nobody is notified (no yellow bar): mentioning
    @everyone needs *Mention @everyone, @here and all roles*, which only
    admins have by default.

### Session 7: Files

Olive and Alice in #general. Test files from section 1.3.

1. Alice: **Attach files** (the paperclip).
   → "**Attach files**": "No files chosen yet.", **Browse…**, "Or type a
   path" with **Add**, "Up to 10 files, 10 MB each. The community's server
   stores them (its admins can read them)." (10 MB: the new-community
   default, Community Admin → **Files**.)
2. Type `two files` in the composer, then **Attach files**, add
   `/tmp/qa/icon.png` and `/tmp/qa/readme.md` (type each path and press
   Enter, or **Browse…**) → **Send**.
   → One message "two files" with **icon.png** 4 KB (an image icon,
   **Save…**, **Show**) and **readme.md** 2 KB (a document icon,
   **Save…**).
3. Olive: **Show** on icon.png.
   → The image appears under it.
4. Olive: **Save…** on readme.md → save it under `/tmp/qa/saved-readme.md`.
   → Toast "**Saved readme.md**"; the file is identical
   (`cmp /tmp/qa/readme.md /tmp/qa/saved-readme.md` prints nothing).
5. Olive: Community Admin → **Files** → **Maximum file size (MB)** `1` →
   **Save changes**.
   → Alice's Attach files dialog now says "1 MB each"; sending
   `/tmp/qa/two-mb.bin` is refused: "**two-mb.bin is 2.0 MB; this community
   allows up to 1 MB per file.**" Set it back to 10.
6. Alice: add 11 paths in the Attach files dialog.
   → "**At most 10 files per message.**"
7. Olive: Channels → **Edit…** staff → **Attachments** = **Off in this
   channel** → **Save**.
   → No **Attach files** in #staff; #general still has it. Set it back.

### Session 8: DMs, group DMs and DM files

Olive, Alice, Bob. DMs never touch the community server: they're end-to-end
encrypted between devices and go through the relay (live) or the maintainer
node's mailboxes (when someone is offline). New conversations and messages
from someone offline can take up to about 15 seconds; **Check for new
messages** (circular arrows) at the top of the DM list fetches at once.

1. Everyone: **QA Town** → **Your profile here** → tick **Publish a
   contact card here** → **Save**. (DMs between members need a contact card
   in the shared community.)
2. Alice: right-click **Olive** (in **Members**, or her name on a
   message) → **Message**.
   → The DM view: "Olive", "**End-to-end encrypted**", **Conversation
   details** (an "i" in a circle), and the composer
   "Message Olive". Send `hello olive`.
3. Olive: click **Direct messages** (the two speech bubbles, top of the
   rail).
   → Within about 15 s: **REQUESTS AND INVITATIONS** with Alice
   (**request**); **Direct messages** shows **1**. Open it: banner "**Alice
   isn't in your contacts and wants to message you.**" with **Accept** and
   **Decline**.
4. **Accept**, reply `hi alice`.
   → Alice gets it at once. While one types, the other sees "**Alice is
   typing…**".
5. Replies, reactions, edits, pins and deletes work as in session 6 (only
   for your own messages for edit and delete). Pinned messages are listed
   under **Conversation details**.
6. Alice: **Conversation details** → **Options for Olive** (three dots) →
   **Safety number**.
   → "**Safety number with Olive**" with 12 groups of 5 digits. Olive's
   shows the same number. **Mark as verified** → a **verified** tag next to
   the name.
7. Alice: **Attach files** in the DM → add `/tmp/qa/icon.png` and
   `/tmp/qa/readme.md` → **Send**.
   → Olive: icon.png with a thumbnail and "4 KB · image/png", readme.md
   "2 KB · text/markdown" ("· not downloaded yet" until it has arrived),
   each with **Save…**
8. Olive: **Save…** on readme.md.
   → "**Save readme.md**" with **Save to** (your Downloads folder, or your
   home folder if there's none, e.g. `~/readme.md`;
   a full path or a folder) and **Browse…** Type `/tmp/qa/dm-readme.md` →
   **Save** → "**Saved readme.md.**"
9. Bob: **Members** → **Olive** → **Add as contact** → "**Added to
   contacts.**" Alice does the same. (DM settings default to **Contacts
   only** (Settings → **Privacy & DMs** → **Who can DM me**), and a group
   invitation from someone who isn't your contact is dropped without
   notice.)
10. Olive: **Direct messages** → **New group DM** (the plus).
    → "**New group DM**": **Community** (QA Town), **Group name**, "Up to 9
    people. …" and the members. Name `Weekend`, tick **Alice** and **Bob** →
    **Create**.
    → "**Group created. People join once they accept the invitation.**"
11. Bob (and Alice): **Direct messages**.
    → Within about 15 s, **Weekend** with **invited**; opening it: "**You were
    invited to "Weekend". You'll only see messages sent after you join.**"
    with **Join** and **Decline**. Both click **Join**.
12. Olive posts `welcome to the group`; Bob answers `thanks`.
    → Everyone sees both (members who joined later don't see earlier
    messages). The header says "3 members".
    Olive: right-click **Bob** on his message → **Mention**.
    → "@Bob " is typed in the group's message box. React to Bob's message
    with 👍 and right-click the reaction →
    "REACTED WITH 👍" and who; pick someone → their menu. Right-click
    **Alice** in the **Direct messages** list (the 1:1 from step 2): her
    menu with **View profile**, **Message**, **Safety number**, **Copy user
    ID** and **Block**; **Message** opens that conversation.
13. Olive: **Conversation details**.
    → "**Group details**": **Set icon…**, **Rename**, "MEMBERS (3 OF 10)"
    with **owner** next to Olive, **Add people**, **Settings** with
    **Members can add people** and **Allow attachments**. Untick **Allow
    attachments**.
    → Within about 15 s Bob's **Attach files** disappears. Tick it again,
    and wait until Bob's **Attach files** is back before the next step (see
    the open bugs in section 3).
14. Bob: **Conversation details** → **Leave group** → "Leave this group? You
    will stop receiving its messages, and its history is deleted from your
    devices." → **Leave**.
    → Weekend is gone from Bob's list; within about 15 s Olive's header says
    "2 members".
15. Olive: **Direct messages** → **Contacts** (two people).
    → The people she added herself (accepting a request doesn't add a
    contact), each with **Safety number**, **Block** and **Remove**.

A DM file for someone offline is in session 11 (it expires).

### Session 9: Device linking

Olive's window and a new window for her laptop.

1. Start the laptop window: `uv run python -m openarcanum --home
   ../../.dev/client-a2 --maintainer http://127.0.0.1:7100`. Click **Link to an
   existing device**.
   → "**Link to an existing device**": **Name this device**, **Back**, **Get
   a code**, "The code goes through this device's maintainer nodes. If your
   other device uses its own list, use the same nodes here." and
   "**Maintainer nodes: 127.0.0.1:7100 (from the launch option)**" with
   **Change…** (to use another node without the flag).
2. Name `Laptop`, **Get a code**.
   → "**Your link code**", a code like `1-blanket-easel-swallow`, "Expires
   in 4:59" counting down, "Waiting for the other device…"
3. Olive: **Settings** → **Devices** → **Link a new device…** → type the
   code → **Continue**.
   → Laptop: "Codes match. Approve the link on the other device." Olive:
   "**Approve new device**": Laptop / Linux, "Only continue if you are
   setting up your own device right now. Never enter a code someone sent
   you.", **Communities to share** (QA Town ticked), **Copy direct message
   history** (ticked), **Copy downloaded community messages**.
4. **Approve and send**.
   → Laptop: "**Almost done**", "Received from Linux device: 1 communities,
   … conversations." and two passphrase fields.
5. Laptop: passphrase twice → **Finish**.
   → The main window with QA Town, and under **Direct messages** Olive's
   conversations with their history.
6. Laptop: post `hello from my laptop` in #general.
   → Everyone sees it from Olive.
7. Olive: Settings → **Devices**.
   → "Linux device" (**this device**) and "Laptop" with linked and last-seen
   times, **Rename** and **Remove**.
8. **Remove** next to Laptop → "**Remove Laptop?**" → **Remove**.
   → "**Laptop removed.**" Within seconds the laptop window wipes its
   profile and shows "Welcome to OpenArcanum". Close it.

### Session 10: Voice, video and screen share

The scripted steps use the browser engine's stand-in devices: a microphone
that beeps and a camera that shows a moving test pattern, so two windows on
one computer don't fight over one microphone. That checks joining, the call
UI, permissions and moderation, not sound quality. Real devices are covered
after the steps.

1. Close Olive's and Alice's windows and start them again with stand-in
   devices. In their terminals:

   ```bash
   QTWEBENGINE_CHROMIUM_FLAGS="--use-fake-device-for-media-stream --disable-dev-shm-usage" \
     uv run python -m openarcanum --home ../../.dev/client-a --maintainer http://127.0.0.1:7100
   ```

   (and `client-b` for Alice). With the kind setup:

   ```bash
   make client NAME=olive ARGS="--fake-media"
   make client NAME=alice ARGS="--fake-media"
   ```

   → The terminal says "fake camera and microphone (--fake-media)" and
   "sound: PulseAudio…" (the beeps play through your speakers).
   Make sure **Voice channels** is on and a voice channel **Lounge** exists
   (session 4).
2. Alice: **Settings** → **Voice & video**.
   → **Microphone** (System default, Fake Default Audio Input, Fake Audio
   Input 1, 2), **Speaker**, **Output volume** (100%), **Camera**, **Input
   mode** (Voice activity / Studio / Push to talk), **Push-to-talk key**,
   **Push to talk in other windows**, **Voice activity sensitivity** (the
   mark at −40), **Share sound with your screen** (on), **Microphone check** with **Test
   microphone** (the meter moves with the beeps), **Speaker check** with
   **Play test sound**, **Camera check** with **Test camera** (a preview of
   the test pattern; **Stop camera**). Close Settings.
3. Olive: click **Lounge** (the speaker icon).
   → Above her name at the bottom left: "Joining Lounge…", then
   "Connecting…", then "**● Voice connected**", "Lounge", **Voice & video
   settings** (a gear) and **Leave call** (a hung-up phone), with no line
   above them; then **Mute**, **Deafen**, **Turn on camera** and **Share
   your screen**, each with a small arrow beside it, and **Turn on priority
   speaker** (a megaphone: admins may, but it's off until pressed). The
   main area shows the call with her tile, "Voice connected" next to the
   channel name and **Chat** (a speech bubble) at the top right; nothing
   under the tiles.
   Olive: the arrow beside **Mute** (or Tab to it and Enter).
   → **Voice activity sensitivity** (−40 dB), **Input device** (System
   default ticked, the fake inputs), **Input mode** (Voice activity ticked,
   Studio, Push to talk), **Voice & video settings…**. Escape closes it and
   the arrow has the focus again. **Studio** → the bar says "Lounge ·
   studio mode"; back to **Voice activity**. The arrow beside **Deafen** →
   **Output volume** and **Output device**; beside the camera → **Turn on
   camera** and **Camera**; beside the screen → **Share your screen** and
   **Share sound** (ticked).
4. Alice (not in the call yet).
   → Olive is listed under Lounge. Click **Lounge** → both connected, each
   sees two tiles.
5. Alice: **Mute** (the microphone).
   → Olive sees **Muted** (a crossed-out microphone) next to Alice under
   Lounge. **Unmute**.
   With sound on (session 6, step 11b): Olive heard "connected" (three
   notes up) when her call connected and two notes going up when Alice
   joined; Alice hears a soft click pair on **Mute** and on **Unmute**, and
   a lower pair on **Deafen** and **Undeafen**. When Alice leaves later,
   Olive hears two notes going down, and Alice hears "disconnected" (three
   notes down).
6. Alice: **Turn on camera** (the video camera).
   → On Olive's call view Alice's tile shows her avatar and "Click to
   watch" (her camera isn't sent until someone watches). Olive clicks it →
   Alice's video, large, the other tiles in a strip below. Click it again
   (or Escape) → back among the others; **Stop watching** (the crossed-out
   eye) → her avatar and "Click to watch" again. Alice: **Turn off camera**.
7. Alice has no **Share your screen**: **Screen share** is off for
   @everyone by default (give a role Screen share to allow it). Olive:
   **Share your screen** (the monitor).
   → "**Share your screen**" lists "Screen: Entire screen" (and windows).
   The picker also has **Share sound** (ticked). Under `--gui` on X11,
   choosing it shares your desktop: Olive has a red **LIVE** next to her
   name under Lounge, and Alice's stage has a block "LIVE", "Olive", "screen"
   with "Click to watch"; Alice clicks it → the screen, large, with **Mute
   stream** and **Stream volume** on it. On Wayland or without a capturable
   display the app says it couldn't capture the screen and the call goes
   on. **(real desktop: Xvfb can't be captured the same way)**
8. Olive: hover Alice under Lounge → **Options for Alice** (three dots).
   → a **Volume for Alice** slider at the top (drag it: 0-200%, kept when
   you let go), then **Mute for me**, then **Server mute, Server deafen,
   Move to…, Disconnect**. **Mute for me** → "Muted for you" (a crossed-out
   speaker) next to Alice, and Olive no longer hears her beeps; Alice sees
   nothing (it isn't a server mute). **Mute for me** again undoes it.
   **Server mute** → Alice's bar says "Lounge · **server muted**";
   **Server unmute** clears it.
9. Olive: Community Admin → **Features** → **Video** off → **Save
   changes**.
   → Alice's **Turn on camera** disappears (the relay drops video too).
   Back on. **Screen share** off → Olive's **Share your screen** disappears.
   Back on.
10. Olive: **Options for Alice** → **Disconnect**.
    → Alice: toast "**A moderator disconnected you from voice.**", her call
    ends; she can rejoin.
11. Olive: **Leave call**.
    → The voice bar is gone.
12. **Voice channel chat.** Olive: point at **Lounge** → **Open chat** (a
    speech bubble).
    → The right-hand panel shows "Lounge", "Nobody is in the call right
    now." (or who is), **Join voice**, **Close chat** (an X), and a
    composer "Message Lounge". Type "game in five" and press Enter.
    → Alice (chat not open): Lounge shows a dot and **Open chat**; a
    notification arrives. Alice: **Open chat** → the message is there, the
    dot goes. Alice replies; Olive sees it without reloading. Olive: **Join
    voice** in the panel → she's in the call and the panel stays; **Chat**
    at the top right of the stage closes and opens it. **Close chat** → the member list is
    back.
13. Olive: Community Admin → **Channels** → **Edit…** on Lounge.
    → Slowmode, Keep messages/files and the channel's feature settings (no
    Topic). Set **Slowmode** 30 seconds, **Save**. Alice posts twice in
    Lounge's chat → the second is refused ("slowmode is on: wait … more
    seconds"). Set it back to Off.
14. **No microphone.** Close Bob's window and start it *without* the
    stand-in devices (on a computer with a microphone, unplug or disable it
    first, so this is a real "no microphone"):

    ```bash
    uv run python -m openarcanum --home ../../.dev/client-c --maintainer http://127.0.0.1:7100
    ```

    Bob: click **Lounge**.
    → He joins: "● Voice connected" and, under it, "No microphone found.
    You can still hear everyone." with **Try again** and **Choose
    microphone**; the stage's status line says the same; Olive sees Bob in
    the call. **Turn on camera** → "No camera found." and the call goes on.
    **Choose microphone** → "System default" and "Voice & video settings…"
    only. **Leave call**.
15. **UDP blocked.** Close Bob's window and start it with WebRTC kept off
    UDP (as a firewall that drops UDP would):

    ```bash
    QTWEBENGINE_CHROMIUM_FLAGS="--use-fake-device-for-media-stream --disable-dev-shm-usage --force-webrtc-ip-handling-policy=disable_non_proxied_udp" \
      uv run python -m openarcanum --home ../../.dev/client-c --maintainer http://127.0.0.1:7100
    ```

    Bob: click **Lounge**.
    → "Connecting…" for up to 15 seconds, then a red voice bar and an error
    notice: "Can't reach the call's media server at 127.0.0.1:…. Calls need
    UDP network traffic, and something is blocking it: …", with **Try
    again**, **Copy details** and **Dismiss**. Bob isn't listed under
    Lounge any more. **Copy details** → "Copied." **Dismiss** → the bar
    goes. Restart Bob normally afterwards.

**Real devices on one computer (kind setup).** Start one copy
with your devices and one with stand-ins, so only one of them uses your
microphone:

```bash
make client NAME=olive               # your microphone, speakers and camera
make client NAME=alice ARGS="--fake-media"
```

→ Olive's terminal says "sound: PulseAudio…" (or "PipeWire") and
"cameras: /dev/video0" (your cameras). Repeat steps 2 to 6: in Olive's
**Voice & video** settings the **Microphone**, **Speaker** and **Camera**
lists show your devices by name, **Test microphone** moves the meter when
you speak, Alice hears you (use headphones: the beeps and your voice share
one room) and sees your camera. `make desktop NAME=olive` (the app natively
on your computer, data in `.dev/desktop/olive`) is the same test without a
container. If a list is empty, see `docs/kubernetes.md` "Troubleshooting".

**Real devices on two computers.** Sound quality, echo
cancellation, plugging a microphone in during a call, refusing microphone
access in the system's privacy settings, screen pickers, push to talk in other apps and each OS's
privacy prompts need two computers with an installed build (AppImage,
Windows installer or macOS `.dmg`; see `docs/packaging.md`;
`make desktop-appimage` builds and runs the AppImage) and a network both
can reach. Follow `docs/manual-tests/voice.md`; it lists what to expect on
Linux, Windows and macOS.

### Session 11: Retention

Retention is set in whole days (Community Admin → **Retention**, and per
channel), and the community server deletes what has passed it when it starts
and then every hour (`purge_interval`, docs/operations.md). Two ways to see
it work:

- **Overnight:** set a channel to keep messages 1 day, post, and look the
  next day (start the servers with `OPENARCANUM_COMMUNITY_PURGE_INTERVAL=1m`
  added to the `scripts/dev-up.sh` line to purge within a minute of the day passing instead of
  within the hour).
- **Now:** move the messages' stored creation time back two days in the
  server's database and restart the server, as below. The database keeps
  that time in plain text precisely so the purge can run in SQL.

DM files have their own lifetime on the maintainer node (`blob_ttl`, 72 h by
default, at least 1 minute), which steps 9 to 12 shorten to 2 minutes.

1. Olive: Community Admin → **Retention**.
   → **Keep messages (days)** 30 and **Keep files (days)** 7 ("0 = forever"),
   "Channels can set shorter or longer limits under Channels. Members'
   devices follow the same limits for what they cache."
2. Create a text channel `retention-test` (it must be the newest channel
   with messages), post `old news one` and `old news two` in it.
3. Channels → **Edit…** retention-test → **Keep messages (days)** `1`,
   **Keep files (days)** `1` → **Save**.
   → Its row says "retention: messages 1, files 1 days"; the Audit log has
   "edited #retention-test: message retention 1 days, file retention 1
   days".
4. Terminal 1: Ctrl+C (the windows stay open and try to reconnect).
5. Terminal 1:

   ```bash
   sqlite3 .dev/community/community.db \
     "UPDATE messages SET created_at = created_at - 2*86400000 WHERE channel_id = (SELECT MAX(channel_id) FROM messages); SELECT changes();"
   ```

   → It prints `2`.
6. Terminal 1 (no `reset`!):

   ```bash
   OPENARCANUM_META_DIRECTORY_ENABLED=true OPENARCANUM_META_BLOB_TTL=2m scripts/dev-up.sh
   ```

   → `.dev/logs/community.log` has two lines `level=ERROR msg="stored message
   unreadable; left out of the search index"` (expected: the server noticed
   the hand-edited rows, which is the tamper check working), then
   `msg="retention purge" messages=2 files=0`.
7. Olive: once **QA Town** is **Connected** again (click **Reconnect** in
   the banner if it says offline), open #general, then #retention-test.
   → "**No messages yet. Say hello!**": the purged messages are gone from
   the app and from this device's cache. #general still has its history.
8. Settings → **Communities** → **Delete local data** next to QA Town →
   **Delete**.
   → "**Local data deleted.**" Channels reload from the server.
9. Close Alice's window.
10. Olive: **Direct messages** → the conversation with Alice → **Attach
    files** → `/tmp/qa/icon.png` → **Send**. Wait 10 seconds (OpenArcanum
    stores the file for offline devices after 6 s).
    → `.dev/logs/meta.log`: `msg="blob stored"`.
11. Wait 3 minutes (2 minutes' lifetime, and the maintainer checks every
    minute).
    → `meta.log`: `msg="blob deleted" … reason="lifetime ended"`.
12. Start Alice's window, open the conversation with Olive.
    → The new message's file card says "**icon.png** 4 KB · image/png ·
    **expired**" with no **Save…** button.

The maintainer node also limits how long DM messages wait for an offline
device (`mailbox_ttl`, 14 days, at least 1 minute) the same way.

### Session 12: The app icon, the interface icons and the tray

Olive and Alice, both unlocked and in the community from Session 2. Each
window shows its own tray icon (several profiles run as several apps), so
point at an icon to see whose it is: the tooltip doesn't name the profile,
so start with only Olive's window open for steps 3-9.

Steps marked **(test tray)** are also automated by
`clients/desktop/tests/test_ui_tray.py` against a stand-in tray (the same
D-Bus protocol KDE, Xfce and GNOME's AppIndicator extension use); run them
on your own desktop's tray as well.

1. Look at Olive's window: its title bar, the taskbar or dock entry, and the
   lock screen (**Lock now** → the lock screen, then unlock).
   → The app icon everywhere: a violet rounded tile with a white speech
   bubble cracked into four glass shards, a small cyan chip flying off the
   top right. The lock screen and the "Starting…" screen show it above the
   title; Settings → **About** shows it next to the version.
2. Point at each button in the window: the rail, the channel list's headers,
   the channel header, a message's hover actions, the composer, the bottom
   left, the voice bar (join Lounge) and the call stage, Community Admin.
   Switch Settings → **Appearance** → **Theme** between Dark and Light.
   → Every icon is a thin line drawing in the text's colour (no coloured
   emoji); each button's tooltip names it; the icons follow the theme and
   turn the accent colour when a toggle is on (Mute, Camera, Members).
   Reactions and emoji people typed are still emoji.
3. **(test tray)** Look at the system tray (notification area, menu bar or
   panel).
   → OpenArcanum's icon is there. Pointing at it says "OpenArcanum" and "Nothing
   new". Right-click: **Open OpenArcanum**, **Mute notifications**, **Lock**,
   **Quit OpenArcanum**.
4. **(test tray)** Close Olive's window (its title bar's ✕).
   → The window goes away, the app keeps running (the tray icon stays), and
   a notification says "**OpenArcanum is still running**" and how to quit.
5. **(test tray)** Click the tray icon.
   → The window comes back as it was. Close it again: it hides, and no
   second notification appears. **Open OpenArcanum** in the menu brings it back.
6. **(test tray)** Olive: click **Direct messages**. Alice: post `hey
   @Olive` in #general.
   → Olive's tray icon gets a red dot; its tooltip says "**1 unread
   mention**".
7. **(test tray)** Olive: click the community in the rail (#general opens).
   → The dot goes away; the tooltip says "Nothing new" again.
8. **(test tray)** Tray menu → **Mute notifications**.
   → The entry is ticked, the tooltip adds "Notifications muted", and
   Settings → **Notifications** → **Desktop notifications** is off. Click
   it again to turn notifications back on.
9. **(test tray)** Tray menu → **Lock**.
   → The window shows the Unlock screen; the tooltip says "**Locked**";
   **Mute notifications** and **Lock** are greyed out. Unlock.
10. **(test tray)** Tray menu → **Quit OpenArcanum**.
    → The app exits within a few seconds; `.dev/client-a/log/client.log`
    ends with `exiting with status 0`. Start it again: everything posted
    before quitting is there.
11. **(test tray)** Settings → **Appearance** → turn off **Keep running in
    the tray when the window is closed** ("Saved"). Close the window.
    → The app quits (`exiting with status 0` in the log).
12. **(real desktop)** On a desktop without a system tray (GNOME without the
    AppIndicator extension): start Olive's window.
    → No tray icon. Settings → **Appearance** shows **Keep running in the
    tray when the window is closed** off and greyed out, saying this desktop
    has no system tray. Closing the window quits.
13. **(real desktop)** Windows and macOS: steps 3-11 with the notification
    area / menu bar icon, and **Ctrl+Q** (**Cmd+Q**, or OpenArcanum → **Quit**
    in the macOS menu bar) also quits; on macOS, clicking the Dock icon
    brings a closed window back. The installed app (Start menu, the .app,
    the AppImage in the application menu) shows the same icon
    (docs/manual-tests/installers.md).

### Session 13: Hosting on this computer, offline and shut-down communities, relay failover

Olive and Alice. The network from section 1 (its relay registers with the
maintainer node). From source the app runs `bin/community` (built by `make
build`; `scripts/desktop.sh` builds it too). Steps marked **(test)** are
also automated by `clients/desktop/tests/test_ui_hosting.py` and
`clients/desktop/tests/test_e2e_hosting.py`.

1. **(test)** Olive: **Add a community** → **Host on your own server** →
   **Save the community server** (keep the suggested path).
   → Toast "**Saved to …/community.**" The file is executable; `./community
   init -h` in a terminal prints its usage.
2. **(test)** Olive: **Host on this computer**.
   → A box "**Your computer serves the community**" (runs while the app
   runs, also in the tray; no port forwarding; offline while the computer is
   off, shut down after 7 days offline) and "**Set up your community**"
   with **Create community**.
3. **(test)** Name `Home Town`, Your display name `Olive`, **Create
   community**.
   → "Starting the community server and setting it up…", then "**Your
   community is ready**" with an invite and a line saying this computer
   serves it. No claim code was asked for. **Export backup…** → **Cancel**.
4. **(test)** Click **Home Town** at the top of the channel list.
   → The menu has a group "**HOSTED ON THIS COMPUTER: RUNNING**" with **Stop
   hosting**. Settings → **Data & storage** lists **Communities hosted on
   this computer** (`…/data/hosted`) and **Hosted community logs**.
5. **(test)** Close Olive's window to the tray. Alice: join `Home Town`
   with the invite and post `hi from outside`.
   → Alice joins and reads Olive's messages: the closed window's app keeps
   serving. Bring Olive's window back: Alice's message is there.
6. **(test)** Olive: community menu → **Stop hosting** → **Stop hosting**.
   → A yellow banner "**This computer hosts this community, and hosting is
   stopped: members can't reach it.**" with **Start hosting**. Within a few
   seconds the **H** icon's tooltip (Olive's and Alice's) says "**Offline ·
   last seen just now**", and Alice's banner says the same.
7. **(test)** Olive: **Start hosting**.
   → The banner goes; both windows reconnect by themselves.
8. **(test)** Olive: tray menu → **Quit OpenArcanum**.
   → The window comes up with "**Quit OpenArcanum?**": "This computer hosts 1
   community. It goes offline until you open OpenArcanum again.", **Cancel**
   and **Quit anyway** (red). **Cancel** keeps it running; **Quit OpenArcanum**
   again → **Quit anyway** exits, and Alice sees the community offline at
   once.
9. **(test)** In a terminal, kill the hosted server (`pkill -f
   'community run -data .*hosted'`) while Olive's app runs.
   → It is started again after a second (the menu shows **Restarting…**
   briefly); Alice reconnects by herself.
10. **(test, maintainer configured for minutes)** Terminal 1: Ctrl+C, then
    start the servers again with `OPENARCANUM_META_DIRECTORY_ENABLED=true
    OPENARCANUM_META_COMMUNITY_REAP_AFTER=2m
    OPENARCANUM_META_COMMUNITY_OFFLINE_AFTER=30s scripts/dev-up.sh`. Quit
    Olive's app and wait three minutes.
    → Alice: "**This community was shut down (its server has been offline
    for over 0 days).**" ("7 days" with the default setting), and **Home
    Town** is gone from directory search. Start Olive's app: its banner
    says the community was shut down, with **Bring it back online**; click
    it.
    → Within a minute both windows are connected again.
11. **(test)** Relay failover: start a second relay registered with the
    maintainer node (`OPENARCANUM_RELAY_LISTEN=127.0.0.1:7001
    OPENARCANUM_RELAY_MEDIA_UDP_PORT=7001
    OPENARCANUM_RELAY_REGISTRY_URLS=http://127.0.0.1:7100
    OPENARCANUM_RELAY_PUBLIC_URL=ws://127.0.0.1:7001/ws bin/relay`), host a
    community, then stop the relay it uses (the community menu → **Copy
    community ID** doesn't say which; `.dev/client-a/log/hosted/*.log`
    does: "attached to relay").
    → The community keeps working: its server moves to the other relay,
    and Olive's and Alice's apps follow within seconds without any action.
    A community claimed from a server whose relays are pinned shows
    "**Offline: its server is up, but none of its own relays can be
    reached**" instead, and never moves.

### Session 14: Statuses

Olive, Alice and Bob in QA Town, each with a contact card there (session 8,
step 1), and Olive and Alice in each other's contacts (Members → right-click
→ **Add as contact**, both ways), with the 1:1 DM from session 8. The same
steps are automated by `clients/desktop/tests/test_ui_status.py`, and the
end-to-end checks by `clients/desktop/tests/test_statuses.py`.

1. Olive: click her avatar and name at the bottom left.
   → "**Set your status**": **Online**, **Idle**, **Do not disturb**,
   **Invisible** (each with a coloured dot and a line saying what it does;
   Invisible's says the community servers still see that you're connected),
   **Custom text**, **Emoji**, **Clear after**, and **Who can see this**:
   "Your status: your contacts and members of 1 community. Custom text:
   your contacts." with **Change who can see this**.
2. Pick **Do not disturb**, type `Planting quokka saplings`, pick 🌴, **Clear
   after** → **1 hour** → **Save**.
   → Olive's bottom left: a red dot on her avatar and "🌴 Planting quokka
   saplings" under her name.
3. Alice: **Members**.
   → Olive's avatar has a red dot; no custom text under her name (a
   community gets the state only by default). Hovering the row says "Do not
   disturb". Right-click Olive → the menu starts with "Do not disturb".
4. Alice: **Direct messages** (press **Check for new messages** if needed).
   → Olive's row: a red dot and "🌴 Planting quokka saplings" under her name
   (contacts get the custom text).
5. Bob (not Olive's contact): **Members**.
   → Olive's red dot, no text. Bob has no DM row for Olive's status.
6. Alice: post `are you there?` in **#general**.
   → Olive gets no desktop notification (Do not disturb); QA Town and
   #general still show the unread mark.
7. Olive: Settings → **Privacy & DMs** → **Status**.
   → **Your contacts see** (**Status and custom text**), **Share with**
   (**All contacts**), **Chosen contacts** (**Choose…**), **Idle after**
   (**10 minutes**), and under **Status in each community**, QA Town with
   **Online status only**. The text says statuses are end-to-end encrypted
   and that a community server still sees when you are connected.
8. Set QA Town to **Status and custom text**.
   → Within a second Alice's and Bob's member lists show "🌴 Planting quokka
   saplings" under Olive's name.
9. Set QA Town to **Off**.
   → Alice's and Bob's member lists show Olive online (green), no text.
10. Olive: set her status to **Invisible** → **Save**.
    → Alice and Bob: Olive moves to **Offline** (grey) within a second;
    Olive still sees herself as connected and can post (her message
    arrives). Olive: quit and start the app again → she stays offline for
    the others. Set **Online** again → she's back for them.
11. Olive: set **Online** with custom text `back in 5`, **Clear after** →
    **30 minutes**. Leave the window alone for 10 minutes.
    → Her dot turns amber (**Idle**) for Alice; moving the mouse in Olive's
    window turns it green again. After 30 minutes the text is gone.
12. Olive: **Direct messages** → **Contacts** → **Remove** Alice, then set
    custom text `only for contacts`.
    → Within about 15 s Alice's DM row for Olive loses Olive's custom text
    (she was sent "cleared"), and `only for contacts` never appears there:
    Olive's contacts got a new key Alice doesn't have. Alice still sees
    Olive online through QA Town (shared as **Off** since step 9: a green
    dot, no text).

### Session 15: A maintainer network: DMs with a node down or cut off

Olive and Alice, with a DM between them (Session 8). A second maintainer
node joins the first as a replica (docs/operations.md "Maintainer
clusters"); steps marked **(test)** are also automated by
`clients/desktop/tests/test_ui_maintainers.py::test_maintainer_network_offer_and_status`
and `clients/desktop/tests/test_e2e_mailbox_failover.py`.

1. Terminal: set up the second node as a replica of the first and sign a
   member list for both:

   ```bash
   bin/meta operator keygen -out .dev/op.key            # prints the operator key
   bin/meta init -replica-of http://127.0.0.1:7100 -data .dev/meta2 -url http://127.0.0.1:7102
   # run the `meta trust add` line it prints, with -data on the first node's data folder
   bin/meta operator sign-set -key .dev/op.key -seq 1 \
     -node http://127.0.0.1:7100=$(bin/meta id -data .dev/meta | awk '/node key/ {print $3}'):root \
     -node http://127.0.0.1:7102=$(bin/meta id -data .dev/meta2 | awk '/node key/ {print $3}') -out .dev/set.bin
   bin/meta set publish -data .dev/meta .dev/set.bin
   OPENARCANUM_META_LISTEN=127.0.0.1:7102 bin/meta -config .dev/meta2/meta.json
   ```
   → The second node logs nothing alarming; `bin/meta peers status -data
   .dev/meta2` shows the first node with `lag 0` within seconds.
2. **(test)** Olive: Settings → **Network** → **Change…**, remove every
   node, add `http://127.0.0.1:7100`.
   → Under the field: "**127.0.0.1:7100 belongs to a maintainer network of 2
   nodes…**" with the key's fingerprint (compare it with the first 12
   characters `meta` prints for the operator key's fingerprint) and both
   nodes, the first marked (root). **Use the network** → the entry shows a
   "**network …**" pill. **Save**.
   → The Network section shows "**Network …**", "**2 nodes · updated from
   the network (list #1)**", both nodes with a dot (green: reachable; grey:
   not tried yet), and **Use this network's other nodes** on. The summary
   row says "**2 maintainer nodes**".
3. Alice: the same with `http://127.0.0.1:7102` (the replica): it offers
   the same network.
4. **(test)** Stop the first node (Ctrl+C in its terminal). Alice sends
   Olive a DM while Olive's window is closed to the tray; open it.
   → The message arrives (from the replica). In Olive's Network section
   the first node's dot turns red with "not reachable, trying again
   later" and the error.
5. Start the first node again; send another DM.
   → It arrives once. Within a few seconds `meta peers status` on both
   nodes shows `lag 0`, and neither node holds the delivered messages
   (`bin/meta blobs stats` is for files; mail is gone from both once read).
6. **(test)** Olive: switch **Use this network's other nodes** off.
   → The summary row says "**Maintainer nodes: 127.0.0.1:7100**"; switch
   it back on.
7. **(test)** Terminal: `bin/meta directory disable -data .dev/meta` and
   the same on `.dev/meta2`; restart Olive's app (it asks the nodes every
   10 minutes otherwise).
   → **Add a community** has no **Find a community**; **Host on this
   computer** offers only a private community, with "**Public listing
   isn't available on your maintainer network; share invites to let people
   join.**" Community Admin → **Visibility** shows the same note. Joining
   with an invite still works. `bin/meta directory enable -data …` on both
   brings it back.

## 3. Known limitations

Please don't report these as bugs.

**By design:**

- **Joining a group DM while its owner changes it.** If the owner changes
  the group (renames it, invites someone) in the same few seconds that you
  accept an invitation, the owner's change goes first and your acceptance
  is applied again after it. Messages others sent in between count as
  before you joined, so you don't get them (new members never see earlier
  messages).
- **Calls need UDP.** Voice, video and screen share send media to the
  relay's one UDP port (`media_udp_port`, 7000 by default); there's no
  TCP or TURN fallback. On a network that blocks outgoing UDP, a call never
  connects its media; chat and DMs, which use the relay's WebSocket, keep
  working.
- DM settings default to **Contacts only**: a 1:1 message from a
  non-contact arrives as a request, and a group invitation from a
  non-contact is dropped without notice. Accepting a request doesn't add a
  contact; **Add as contact** does.
- DMs and invitations from someone you haven't talked to recently can take
  up to about 15 seconds (mailbox polling); **Check for new messages**
  fetches at once.
- In a channel where you lack *Send messages*, the composer is replaced by
  "You can't send messages in this channel."
- Retention is in whole days; the purge runs at start and hourly.
- Features not built are listed in `docs/parity-gaps.md` (among them
  threads, forums, stage and announcement channels, polls, stickers, a GIF
  picker, drag-and-drop ordering (**Move up** and **Move down** instead),
  search filters such as `from:` and `has:`, forwarding and scheduled
  messages).

**Depends on the build:**

- **Unsigned installers.** A build made without the signing secrets has an
  unsigned Windows installer (SmartScreen warns) and an ad-hoc signed,
  unnotarized macOS `.dmg` (Gatekeeper blocks it). The macOS build is Apple
  silicon only (`docs/packaging.md`).
- **Updates.** A build without a release public key says "Updates aren't
  configured for this build." under Settings → **Updates** and never checks
  (`docs/release.md`).
- **Real devices and other OSes** have their own scripts in
  `docs/manual-tests/`: `voice.md` (voice, video, screen share and the
  mouth-to-ear delay), `notifications.md`, `tray.md`, `installers.md` and
  `platform.md`.

The built-in maintainer node is the public `maintainer-01.openarcanum.org`,
hence `--maintainer` for a local network.

**In a minimal environment** (a container, Xvfb or a bare window manager;
these work on a normal desktop):

- No desktop notifications (no notification service): you
  get the in-app toasts only. `client.log` says "notifications: no desktop
  notification service".
- No keychain: staying signed in keeps the key in `config/unlock-key`
  instead, and Settings → Security says plainly that anyone who can read
  your user's files can then open the profile.
- No sound, microphone or camera: use the stand-in devices of session 10.
- `openarcanum://invite/…` links can't be clicked from other apps: that needs an
  installed build registered with the desktop. Pasting the link into **Join
  with an invite** works.
- The browser engine runs without its sandbox inside a container
  (`client.log`: "the browser engine runs without its sandbox: turned off in
  the environment"); installed builds keep it on where the system allows.
- Native file dialogs (**Browse…**) are Qt's own
  dialog there, not your desktop's.

## 4. How to file a bug

File bugs as issues at https://github.com/hyperkubeorg/openarcanum/issues.
Report security problems privately instead, as the website's security page
describes.

**Where the logs are** (paths from the repository folder):

| What | Where |
|---|---|
| App, per window | `.dev/client-a/log/client.log` (Olive), `client-b` (Alice), `client-c` (Bob), `client-a2`, `client-d` |
| App crashes | `crash.log` next to `client.log` (the app also says at the next start that it closed unexpectedly) |
| Relay, maintainer node, community server | `.dev/logs/relay.log`, `meta.log`, `community.log` |
| The page's JavaScript errors | start the app with `--debug` (or `OPENARCANUM_DEVTOOLS=1`); then Ctrl+Shift+I opens the developer tools: the **Console** tab |

The logs never contain message text, names, keys, invite codes or
passphrases (members and devices appear as short fingerprints), so you can
attach them as they are. For more detail, start the app with `--verbose` and
the servers with `-v`.

**What to include:**

1. The session and step number from this guide (or what you did, click by
   click), and which window (Olive, Alice, …).
2. What you expected and what happened instead: the exact text on screen,
   and a screenshot if it's visual.
3. The time it happened (the logs are timestamped), and the relevant lines
   of the logs above from around that time, or the whole files.
4. `git rev-parse --short HEAD`, and the version from Settings → **About**.
5. Your desktop: X11 or Wayland, and the distribution.
6. Whether it happens again after closing and reopening the window, and
   after `scripts/dev-up.sh reset`.
