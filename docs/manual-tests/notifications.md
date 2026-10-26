# Manual test: desktop notifications on each OS

`clients/desktop/tests/test_notifications.py` checks the Linux path automatically
against a stand-in notification service. This script checks real desktops.
Run it on **Windows, macOS and at least one Linux desktop (GNOME and KDE if
you can)**, and note the OS, desktop and result of each step at the end.

You need OpenArcanum on this computer in a community, and a second person or
device that can post in it.

1. In Settings → Notifications, make sure desktop notifications are on.
2. Switch to another app (OpenArcanum not focused). Have the other person post
   "hello from the test" in a channel you're in.
   - [ ] A notification appears with their name and the channel, and the text.
   - [ ] **Windows:** a toast near the clock (the OpenArcanum icon is in the
     notification area).
   - [ ] **macOS:** the first time, macOS asks whether OpenArcanum may send
     notifications; allow it. The notification shows in Notification Center.
     (If you said no: System Settings → Notifications → OpenArcanum.)
   - [ ] **Linux:** the desktop's own notification (GNOME's banner, KDE's
     popup), also on Wayland, without a tray icon.
3. Lock OpenArcanum (**Lock now**) and have them post again.
   - [ ] The notification says only "New activity", with no name or text.
4. Settings → Notifications → "Mentions only"; they post without mentioning
   you, then with @your name.
   - [ ] Only the mention notifies.
5. Mute the channel (right-click → Mute channel); they post again.
   - [ ] Nothing; a mention still notifies.
6. Turn notifications back on ("All messages"). Have them send you a
   **direct message**.
   - [ ] A notification with their name and the text.
   - [ ] Locked (**Lock now**): only "OpenArcanum" / "New activity", no name or
     text.
   - [ ] With "Mentions only": a direct message without a mention doesn't
     notify.
7. Have them post `<b>bold</b> & <a href="https://example.org">link</a>`.
   - [ ] The notification shows those characters as typed: no bold text and
     no clickable link (KDE Plasma renders markup, so check there if you can).
8. Turn desktop notifications off.
   - [ ] Nothing at all.

## Results

| Step | Windows (version) | macOS (version) | Linux (distro, desktop, X11/Wayland) |
|---|---|---|---|
| 2 Shown |  |  |  |
| 3 Locked |  |  |  |
| 4 Mentions only |  |  |  |
| 5 Muted channel |  |  |  |
| 6 Direct messages (shown, locked, mentions only) |  |  |  |
| 7 Markup shown as text |  |  |  |
| 8 Off |  |  |  |
