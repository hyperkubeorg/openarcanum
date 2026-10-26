"""Desktop notifications on each OS.

- Linux: the freedesktop notification service on the session bus
  (`org.freedesktop.Notifications`), which every desktop provides (GNOME,
  KDE, Xfce, and Wayland sessions, where a tray icon usually doesn't exist).
- Windows and macOS: Qt's tray icon message (a toast on Windows; the
  Notification Center on macOS, once notifications are allowed).

What's shown is decided by the core (`should_notify`, and nothing but "New
activity" while locked); this only delivers it. On Linux the summary and
the body are escaped: services that parse markup would otherwise render tags
from other people's names and messages (links, images) in a notification
that comes from OpenArcanum. Delivery never blocks the
UI: Linux calls go through a worker thread.
"""

from __future__ import annotations

import html
import logging
import queue
import sys
import threading

log = logging.getLogger(__name__)

APP_NAME = "OpenArcanum"
TIMEOUT_MS = 6000


class LinuxNotifier:
    """Sends notifications over D-Bus from one worker thread."""

    def __init__(self, desktop_entry: str = "openarcanum"):
        self.desktop_entry = desktop_entry
        self._q: queue.SimpleQueue = queue.SimpleQueue()
        self._ok: bool | None = None
        threading.Thread(target=self._run, daemon=True, name="notifications").start()

    def send(self, title: str, body: str) -> None:
        self._q.put((title, body))

    @property
    def working(self) -> bool | None:
        """None until the first send was tried."""
        return self._ok

    def _run(self) -> None:
        from jeepney import DBusAddress, new_method_call
        from jeepney.io.blocking import open_dbus_connection

        conn = None
        target = DBusAddress(
            "/org/freedesktop/Notifications",
            bus_name="org.freedesktop.Notifications",
            interface="org.freedesktop.Notifications",
        )
        while True:
            title, body = self._q.get()
            # Both are escaped. The body may be markup by the spec, and the
            # summary (a display name someone else chose) is plain text by
            # the spec, but some services parse it as markup too (dunst with
            # markup=full): unescaped, a name could carry links or images.
            title = html.escape(title, quote=False)
            body = html.escape(body, quote=False)
            try:
                if conn is None:
                    conn = open_dbus_connection(bus="SESSION")
                msg = new_method_call(
                    target,
                    "Notify",
                    "susssasa{sv}i",
                    (APP_NAME, 0, "", title, body, [], {"desktop-entry": ("s", self.desktop_entry)}, TIMEOUT_MS),
                )
                reply = conn.send_and_get_reply(msg, timeout=5)
                if reply.header.message_type.name == "error":
                    raise OSError(reply.body[0] if reply.body else "error reply")
                self._ok = True
            except Exception as e:  # no session bus, no notification service, ...
                if self._ok is not False:
                    log.warning("notifications: no desktop notification service (%s)", type(e).__name__)
                self._ok = False
                if conn is not None:
                    conn.close()
                    conn = None


def make_notifier():
    """The Linux notifier, or None where Qt's tray message is used."""
    if sys.platform.startswith("linux"):
        return LinuxNotifier()
    return None
