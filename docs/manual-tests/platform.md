# Manual test: the app on each OS (platform integration)

CI runs the whole UI suite on Linux, Windows and macOS (`make check`,
`.github/workflows/ci.yml`), which clicks through the client flows
(`docs/architecture.md` §16). What a test runner can't judge is how OpenArcanum fits into a real
desktop: the OS keychain, native file dialogs, screen readers, sleep and
logout, high-DPI screens, and two different OSes talking to each other. This
script covers that. Run it on **Windows, macOS and at least one Linux
desktop** (note X11 or Wayland), with an installed release build, and fill
in the table at the end.

Other scripts cover the rest, so this one doesn't repeat them:

| Area | Script |
|---|---|
| Installing, first start, invite links from the OS, single instance with a link, auto-update, uninstall | `docs/manual-tests/installers.md` |
| Desktop notifications | `docs/manual-tests/notifications.md` |
| Microphone, camera, screen capture, push to talk in other apps (incl. macOS Input Monitoring, Wayland) | `docs/manual-tests/voice.md` |

You need:

- OpenArcanum installed from the release artifacts (`installers.md` §1);
- a community you can post in (the QA guide's local network,
  `docs/QA_GUIDE.md` §1, or any test community), and a second computer or
  person, **ideally on a different OS**, for sections 8 and 9;
- for section 6: Narrator (Windows, Ctrl+Win+Enter), VoiceOver (macOS,
  Cmd+F5) or Orca (Linux, Super+Alt+S on GNOME).

Write down the OS, version and (Linux) desktop and X11/Wayland first. The
profile folders are listed in `installers.md` ("Profile folder").

## 1. Profile, lock and passphrase on this OS

1. Create a profile (`docs/QA_GUIDE.md` session 1, steps 1-6).
   - [ ] The profile folders exist; on Linux and macOS the data folder is
     readable only by you (`ls -ld`: `drwx------`).
2. Settings → Security → **Lock after idle** = **1 minute**, then minimize
   OpenArcanum and use other apps for a minute.
   - [ ] Back in OpenArcanum, it's locked. Unlock; set it back to **Never**.
3. Lock (**Lock now**), type a wrong passphrase twice.
   - [ ] **Wait 1 s**, then a longer wait; the right passphrase then opens it.
4. Lock → **Forgot your passphrase?** → **Set this profile aside and start
   over…** → **Start over**.
   - [ ] The first-run screen appears, and the old profile is still on disk
     in the data folder, renamed (`vault.bin.set-aside-…`), not deleted.
   - Restore your backup (step 5 of section 2) or start over with a new
     profile for the rest of this script.

## 2. Staying signed in (OS keychain)

A profile created, restored or linked stays signed in on the device.

1. Settings → Security → **Signing in**.
   - [ ] **Ask for my passphrase when OpenArcanum starts** is off, and the text
     names this system's keychain (Windows, macOS, a Linux desktop with
     GNOME Keyring or KWallet). Without a keychain it says the key is kept
     in a file and shows a yellow note that anyone who can read your user's
     files can open the profile; note that, check
     `ls -l ~/.config/OpenArcanum/unlock-key` shows `-rw-------`, and skip to
     step 3.
   - [ ] Note any prompt from the OS when the profile was created (macOS
     Keychain, KWallet asking to open the wallet, GNOME asking to unlock the
     keyring) and that allowing it works.
   - [ ] The entry exists: Windows **Credential Manager → Windows
     Credentials** (generic credential "OpenArcanum profile key"); macOS
     **Keychain Access → login** ("OpenArcanum profile key"); Linux **Passwords
     and Keys** (Seahorse) or **KWalletManager** ("OpenArcanum profile key").
2. Linux only: lock the keyring (Seahorse → right-click Login → Lock), then
   restart OpenArcanum.
   - [ ] The OS asks to unlock the keyring, or OpenArcanum says why it can't
     use it and asks for the passphrase, which works (**Unlock with this
     device's keychain** tries again).
3. Quit OpenArcanum and start it again.
   - [ ] It opens without asking for the passphrase.
4. Lock (**Lock now**), quit, start again.
   - [ ] It asks for the passphrase (the keychain entry or file is gone).
     Enter it; quit and start again: it opens without asking.
5. Change the passphrase (Settings → Security → **Change passphrase…**),
   restart.
   - [ ] It still opens without the passphrase.
6. Turn on **Ask for my passphrase when OpenArcanum starts** (passphrase, **Ask
   for it**).
   - [ ] The keychain entry (or file) is gone; the next start asks for the
     passphrase.
7. Export a backup (Settings → Security → **Export backup…**) for section 3.

## 3. Native file dialogs and file names

Use a folder whose name has a space and non-ASCII letters, such as
`Test files é`, holding a small image `photo é.png` and a text file.

1. **Export backup…** → **Browse…** next to **Save to**.
   - [ ] The OS's own save dialog (Windows Explorer, macOS Finder sheet,
     the GNOME/KDE dialog; note if it's Qt's generic one instead), with
     `openarcanum-backup.fbak` filled in. Save into the test folder: "Backup
     saved."
2. In a channel: **Attach files** → **Browse…**, select both files at once
   (Ctrl/Cmd-click).
   - [ ] Both are listed; **Send** posts them with the image's name intact.
3. In a DM: **Attach files** → **Browse…** → the same files → **Send**. On
   the other side, **Save…** → **Browse…**.
   - [ ] The save dialog opens; the file saves where you chose, under its
     name.
   - [ ] macOS: if macOS asks whether OpenArcanum may access Downloads,
     Documents or Desktop, allowing it lets the save finish (note the
     prompt).
4. Send a DM file named `CON.txt` (or `aux.png`) from Linux or macOS to the
   Windows computer, and save it there with **Save…** into a folder.
   - [ ] Windows saves it as `_CON.txt` (a safe name), not an error.
5. Settings → **Data & storage** → **Data folder** → **Choose…** → a new
   empty folder.
   - [ ] "Data moved."; the files are in the new folder; Settings → **About**
     shows it. Quit and restart: the profile opens from there. **Use
     default** moves it back.
6. On the first-run screen of a fresh profile (`--home` a new folder, or
   another OS account): **Restore from backup** → **Choose file…** → the
   backup from step 1.
   - [ ] The OS's open dialog; the restore works with the backup passphrase.

## 4. Crash notice and logs

1. Post a message with a word you'll recognise, such as `tangerine`.
2. Kill OpenArcanum: Windows Task Manager → **End task**; macOS Activity Monitor
   → **Force Quit**; Linux `pkill -9 -f OpenArcanum` (or `openarcanum`).
3. Start OpenArcanum again.
   - [ ] After unlocking, a notice says "OpenArcanum closed unexpectedly last
     time. Its log stays on this computer, in …; nothing was sent anywhere."
     with the log folder.
   - [ ] That folder holds `client.log` (and maybe `crash.log`). Searching it
     for `tangerine`, your passphrase or the community's name finds nothing.
4. Quit normally (**Quit OpenArcanum** in the tray menu, or Ctrl+Q) and start again.
   - [ ] No notice this time.
5. With OpenArcanum open and unlocked, log out of the OS (or shut down and start
   again).
   - [ ] The OS doesn't wait on OpenArcanum for long. After logging back in,
     OpenArcanum starts without the crash notice and the last message you
     posted is still there.

## 5. Window, screens and system settings

1. Resize the window, move it, quit and start again.
   - [ ] It comes back at the same size and place. It can't be made smaller
     than about 720×480.
2. Use a high-DPI screen or display scaling of 150% or 200% (and move the
   window between screens of different scaling, if you have two).
   - [ ] Text and icons are sharp and correctly sized; nothing is cut off.
3. Settings → **Appearance** → **Theme** = **Match the system**; switch the OS
   between light and dark mode.
   - [ ] OpenArcanum follows (note whether it needed a restart).
4. **Text size** = **Larger**.
   - [ ] Everything grows, the lock screen included after locking.
5. Copy text out of OpenArcanum (**Copy invite**, **Copy text** on a message,
   member menu → **Copy user ID**) and paste it into another app; copy
   text from another app into the message box.
   - [ ] Both directions work, with non-ASCII characters and emoji intact.
6. Emoji: post `👍 🎉 🇫🇷 👩‍👩‍👧`.
   - [ ] They show as colour emoji, not boxes.
7. Ctrl+Shift+I (Cmd+Shift+I on macOS), then again after starting the app
   with `--debug`.
   - [ ] Nothing opens the first time; the developer tools open with `--debug`.

## 6. Keyboard and screen reader

Turn the screen reader on.

1. Lock and unlock using only the keyboard.
   - [ ] The passphrase field is announced as "Passphrase", the **Unlock**
     button is announced, and an error is read out.
2. Tab through the main window to a channel, press Enter, Tab to a message.
   - [ ] Each control is announced by name (icon buttons by their tooltip,
     such as "Settings", "Lock now", "Members"); the focus outline is
     visible; the message's actions appear and can be reached with Tab.
3. Open Settings (Tab to **Settings**, Enter), move through sections, press
   Escape.
   - [ ] The dialog is announced as "Settings"; each setting is read with
     its label and description; Escape closes it and focus returns to
     **Settings**.
4. Have the other person post a message while the focus is in the message
   box.
   - [ ] The screen reader announces it without moving the focus.
5. Open a DM, a group DM's **Conversation details** and the safety number
   dialog from the keyboard.
   - [ ] Every button and checkbox is announced by name (including **Members
     can add people** and **Allow attachments**).

## 7. Sleep, network changes and long runs

1. With OpenArcanum unlocked and in a community, put the computer to sleep for
   5 minutes, then wake it.
   - [ ] Within a minute the community shows **Connected** again (hover its
     icon), without a restart; messages posted meanwhile appear.
   - [ ] Note whether OpenArcanum had locked itself (**Lock after idle** at 1
     minute while it slept).
2. Switch networks (Wi-Fi to another Wi-Fi, or unplug and replug the cable).
   - [ ] OpenArcanum reconnects by itself; Settings → **Network** → **Reconnect**
     isn't needed.
3. Have the other person send you a DM while this computer sleeps.
   - [ ] It arrives after waking (**Check for new messages** fetches at
     once).
4. Leave OpenArcanum running overnight.
   - [ ] Next morning it's responsive, memory use in Task Manager / Activity
     Monitor / `top` is reasonable (note it), and messages still arrive.

## 8. Linking devices across two OSes

Computer A has your profile; computer B (another OS) has none.

1. Link B to A (`docs/QA_GUIDE.md` session 9, steps 1-5), with **Copy direct
   message history** on.
   - [ ] B opens with your communities and DM history; A shows "New device
     linked: …"; B later says "Message history copied from your other
     device."
2. Post on B; join a new community on A.
   - [ ] Others see B's post as you; the new community appears on B by
     itself.
3. On A: Settings → **Devices** → **Remove** next to B.
   - [ ] B wipes its profile and shows "Welcome to OpenArcanum" within seconds
     (if online). Its profile folders no longer hold your vault.

## 9. Direct messages between two OSes

1. Start a DM between this computer and a person on another OS
   (`docs/QA_GUIDE.md` session 8, steps 1-8): a message each way, a reaction,
   an edit, a file each way.
   - [ ] Everything arrives both ways; images show previews; files save.
2. Compare safety numbers (**Conversation details** → **Options for …** →
   **Safety number**).
   - [ ] Both screens show the same 60 digits.
3. Quit OpenArcanum on the other computer, send a DM and a file, start it
   again.
   - [ ] Both arrive (through the mailbox and the blob store).

## Results

| Section | Windows (version) | macOS (version) | Linux (distro, desktop, X11/Wayland) |
|---|---|---|---|
| 1. Profile, lock, passphrase | | | |
| 2. Keychain | | | |
| 3. File dialogs and names | | | |
| 4. Crash notice, logout | | | |
| 5. Window, screens, clipboard | | | |
| 6. Keyboard, screen reader | | | |
| 7. Sleep, network, long run | | | |
| 8. Linking across OSes | | | |
| 9. DMs across OSes | | | |
| Notes | | | |

File anything that fails as in `docs/QA_GUIDE.md` §4 ("How to file a bug").
