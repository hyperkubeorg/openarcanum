# Manual test: the app icon and the system tray on each OS

`clients/desktop/tests/test_ui_tray.py` drives the real tray icon on Linux under Xvfb
against a stand-in tray (the StatusNotifierItem D-Bus protocol), and checks
the window's icon over X11. This script checks real desktops. Run it on
**Windows, macOS and at least two Linux desktops: one with a tray (KDE,
Xfce or Cinnamon) and plain GNOME without the AppIndicator extension**, and
note the OS, desktop and result of each step at the end.

You need the installed app (installer, `.dmg` or AppImage) in a community,
and a second person or device that can post in it.

1. Start OpenArcanum.
   - [ ] The window, the taskbar / dock and the application menu show the
     OpenArcanum icon (a violet tile with a cracked white speech bubble), crisp
     at the small sizes too; the lock screen shows it above "Unlock".
   - [ ] **With a tray:** the same icon is in the notification area (Windows;
     it may be under the ^ overflow), the menu bar (macOS) or the panel's
     tray (Linux). Pointing at it says "OpenArcanum" and, once unlocked,
     "Nothing new".
   - [ ] **GNOME without a tray extension:** no tray icon. Settings →
     Appearance → **Keep running in the tray when the window is closed** is
     off and greyed out, saying the desktop has no system tray.
2. **With a tray:** close the window (its ✕).
   - [ ] The window goes away and OpenArcanum keeps running (the tray icon
     stays); a notification says "OpenArcanum is still running" and how to
     quit. Closing it again later doesn't repeat the notification.
   - [ ] **Without a tray:** OpenArcanum quits.
3. Click the tray icon (macOS: choose **Open OpenArcanum** in its menu, or click
   the Dock icon).
   - [ ] The window comes back where it was.
4. Close the window again. Have the other person mention you (@your name).
   - [ ] The tray icon gets a red dot; pointing at it says "1 unread
     mention". (Windows may show the dot only after the icon redraws; the
     tooltip is immediate.)
5. Open the window and read the message.
   - [ ] The dot goes away; the tooltip says "Nothing new".
6. Tray menu → **Mute notifications**; have them post again.
   - [ ] No desktop notification; Settings → Notifications → Desktop
     notifications is off. Choose **Mute notifications** again to undo.
7. Tray menu → **Lock**.
   - [ ] The window (if shown) shows the Unlock screen; the tooltip says
     "Locked"; Mute and Lock are greyed out in the menu.
8. Unlock, then quit three ways (start OpenArcanum again after each):
   - [ ] Tray menu → **Quit OpenArcanum**: the icon disappears and the process
     ends (Task Manager / Activity Monitor / `ps`).
   - [ ] **Ctrl+Q** (macOS **Cmd+Q**, or OpenArcanum → Quit in the menu bar).
   - [ ] Log out of the desktop with OpenArcanum running (window hidden): the
     logout isn't held up, and after logging in again OpenArcanum starts with
     its profile intact (unlock: what you posted before is there).
9. Settings → Appearance → turn off **Keep running in the tray when the
   window is closed**, then close the window.
   - [ ] OpenArcanum quits.

Results (OS, desktop, step, pass or what happened):
