# Manual test: installers, updates, invite links, uninstall

CI builds each installer and checks it automatically (`docs/packaging.md`):
the bundle and the AppImage start, the Windows installer installs, registers
`openarcanum://`, upgrades silently and uninstalls, the `.dmg` holds the app and
the app starts from it. CI can't check what a person sees: SmartScreen and
Gatekeeper, the OS asking which app opens a link, shortcuts, the update
prompts, what is left behind after uninstalling. This script does. Run it on
**Linux, Windows and macOS** for every release candidate, with the artifacts
attached to the release workflow's draft GitHub Release (`docs/release.md`
"Making a release"; testers' install steps in "Installing a build"), and
fill in the table at the end.

You need:

- the installer for the OS under test, for two versions: an **older** one
  (N-1) and the **new** one (N), with a signed `manifest.json` for N served
  from the release host the client is built to use (`docs/release.md`);
- an invite link for a community you can join: in any community where you
  may create invites, **Community Admin → Invites & join policy → Create
  invite…**, then copy the `openarcanum://invite/…` link;
- a browser, and a chat or mail app in which links are clickable.

Write down the OS and version first. "Profile folder" below means:

| OS | Profile folder (config, data, cache, logs) |
|---|---|
| Linux | `~/.config/OpenArcanum`, `~/.local/share/OpenArcanum`, `~/.cache/OpenArcanum`, `~/.local/state/OpenArcanum` (log) |
| Windows | `%LOCALAPPDATA%\OpenArcanum` |
| macOS | `~/Library/Application Support/OpenArcanum`, `~/Library/Caches/OpenArcanum`, `~/Library/Logs/OpenArcanum` |

## 1. Install (version N-1)

**Linux (AppImage)**

1. Download `openarcanum-<N-1>-linux-x86_64.AppImage`, then
   `chmod +x openarcanum-*-linux-x86_64.AppImage` (or Properties → Allow executing).
   - [ ] Double-clicking it (or running it from a terminal) opens the
     OpenArcanum window.
2. Register it with the desktop, either with AppImageLauncher / Gear Lever
   ("Integrate"), or by hand:

   ```sh
   app="$PWD/openarcanum-<N-1>-linux-x86_64.AppImage"
   "$app" --appimage-extract openarcanum.desktop
   mkdir -p ~/.local/share/applications
   sed "s|^Exec=.*|Exec=$app -- %u|" squashfs-root/openarcanum.desktop > ~/.local/share/applications/openarcanum.desktop
   rm -r squashfs-root
   xdg-mime default openarcanum.desktop x-scheme-handler/openarcanum
   ```

   - [ ] OpenArcanum appears in the application menu (with its icon when an
     integration tool installed it).
   - [ ] `xdg-mime query default x-scheme-handler/openarcanum` prints
     `openarcanum.desktop`.

**Windows**

1. Run `openarcanum-<N-1>-windows-x86_64-setup.exe`.
   - [ ] Unsigned build: SmartScreen warns ("Windows protected your PC");
     **More info → Run anyway**. Signed build: the publisher name is shown and
     there is no SmartScreen block.
   - [ ] The installer asks for no administrator rights (default "Install for
     me only").
   - [ ] Offered: a desktop shortcut (unchecked by default), "Launch OpenArcanum"
     at the end.
2. Finish with "Launch OpenArcanum" ticked.
   - [ ] The window opens. A Start menu entry "OpenArcanum" exists.
   - [ ] The installer, the Start menu entry, the taskbar button and
     **Installed apps** show the OpenArcanum icon (violet tile, cracked white
     speech bubble), not a generic one.
   - [ ] **Settings → Apps → Installed apps** lists OpenArcanum with its version.

**macOS**

1. Open `openarcanum-<N-1>-macos-arm64.dmg`.
   - [ ] The window shows OpenArcanum and an Applications folder; drag OpenArcanum
     onto Applications.
2. Open OpenArcanum from Applications.
   - [ ] Signed and notarized build: macOS asks "OpenArcanum is an app
     downloaded from the Internet. Are you sure you want to open it?" and
     names the developer; **Open** starts it.
   - [ ] Unsigned build: macOS refuses ("cannot be opened" / "Apple could not
     verify"); right-click → Open (or System Settings → Privacy & Security →
     Open Anyway) starts it.
   - [ ] The app in Applications, the Dock and the app switcher show the
     OpenArcanum icon.
3. Eject the disk image.

## 2. First run

1. **Create a profile** with a passphrase, as on first run
   (`docs/QA_GUIDE.md`).
   - [ ] The main window appears; nothing else is asked.
2. Join a voice channel (any community) and leave it again.
   - [ ] macOS: macOS asks for the **microphone** with the text "OpenArcanum uses
     the microphone for voice calls you join."; allow it.
   - [ ] Turning the camera on asks for the **camera** ("…when you turn on
     video in a call"); sharing the screen asks for **Screen Recording**
     (macOS may require reopening the app after allowing it).
3. Close the window.
   - [ ] Where the desktop has a system tray (Windows, macOS, KDE…): the
     window hides and OpenArcanum stays in the tray, saying so once
     (`docs/manual-tests/tray.md` checks the tray in full). Choose **Quit
     OpenArcanum** in the tray icon's menu.
   - [ ] The app exits (no process left: Task Manager / Activity Monitor /
     `pgrep -f OpenArcanum`). Without a tray, closing the window does this.

## 3. Invite links open the app

1. With OpenArcanum **closed**, click the `openarcanum://invite/…` link in the chat
   or mail app (or paste it into the browser's address bar and press Enter).
   - [ ] The browser asks whether to open OpenArcanum (allow it; "always allow"
     is fine).
   - [ ] Windows/Linux: nothing else asks which app to use.
   - [ ] OpenArcanum starts and shows the unlock screen; after unlocking, the
     **Add a community → Join with an invite** dialog opens with the link
     already in the field.
   - [ ] Nothing was joined yet: **Continue** shows the community's preview,
     and **Join** is still needed. Close the dialog without joining.
2. With OpenArcanum **open** (unlocked, window behind another window or
   minimized), click the link again.
   - [ ] No second OpenArcanum window appears; the existing window comes to the
     front with the Join dialog prefilled.
3. Click a damaged link, for example `openarcanum://invite/%%%`.
   - [ ] OpenArcanum comes to the front (or starts) and no dialog opens.
4. Complete the join from the dialog: **Continue → Join**.
   - [ ] You are in the community, as when pasting the invite by hand.

## 4. Upgrade through auto-update (N-1 → N)

1. In version N-1, open **Settings → Updates** and check for updates.
   - [ ] It reports that version N is available, with its size, and asks
     before downloading or installing anything.
2. Accept.
   - [ ] The download completes; the app says it verified the update and
     asks to restart (or restarts on your confirmation).
   - [ ] The app closes and starts again by itself, on version N
     (**Settings → About** or **Settings → Updates** shows N).
   - Windows: there may be a brief installer progress window; no questions.
   - Linux: the AppImage file was replaced in place (same path, new
     contents; check the version in Settings).
   - macOS: OpenArcanum.app in Applications is version N
     (Finder → Get Info).
3. Unlock.
   - [ ] Your communities, messages, DMs and settings are all still there.
4. Check for updates again.
   - [ ] "You're up to date."
5. Click an invite link again (section 3, step 2).
   - [ ] It still opens the upgraded app with the dialog prefilled.

## 5. Uninstall

Before uninstalling, note the size of the profile folder.

**Linux**

1. Remove the integration (AppImageLauncher / Gear Lever "Remove", or delete
   `~/.local/share/applications/openarcanum.desktop`), then delete the AppImage.
   - [ ] OpenArcanum is gone from the application menu; clicking an invite link
     no longer offers OpenArcanum.
   - [ ] The profile folders (table above) are still there.

**Windows**

1. **Settings → Apps → Installed apps → OpenArcanum → Uninstall** (with OpenArcanum
   running, to check it is closed first).
   - [ ] The uninstaller closes OpenArcanum (or asks to) and finishes without
     errors.
   - [ ] `%LOCALAPPDATA%\Programs\OpenArcanum` is gone; the Start menu and desktop
     shortcuts are gone.
   - [ ] Clicking an invite link no longer opens OpenArcanum
     (`HKEY_CURRENT_USER\Software\Classes\openarcanum` is gone in regedit).
   - [ ] `%LOCALAPPDATA%\OpenArcanum` (the profile) is still there.

**macOS**

1. Quit OpenArcanum; drag **Applications → OpenArcanum** to the Trash and empty it.
   - [ ] Clicking an invite link no longer opens OpenArcanum (after a moment;
     Launch Services updates lazily).
   - [ ] The profile folders (table above) are still there.

**All OSes:** the profile is kept on purpose, so reinstalling keeps your
communities. To remove it too, delete the profile folders. (The key that
keeps the device signed in is in the OS keychain: Linux Secret Service or
KWallet, Windows Credential Manager, macOS Keychain, entry "OpenArcanum profile
key"; on a system without a keychain it is `unlock-key` in the config
folder.)

2. Reinstall version N and open it.
   - [ ] The unlock screen appears and your profile unlocks with its
     passphrase.

## Results

| Section | Linux (distro, version) | Windows (version) | macOS (version) |
|---|---|---|---|
| 1. Install | | | |
| 2. First run | | | |
| 3. Invite links | | | |
| 4. Upgrade | | | |
| 5. Uninstall | | | |
| Notes | | | |
