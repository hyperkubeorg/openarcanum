"""A stand-in for the desktop's system tray (tests): owns
org.kde.StatusNotifierWatcher on the session bus, says a tray host is
running, and accepts the app's tray icon (StatusNotifierItem, the D-Bus tray
protocol of KDE, Xfce, Cinnamon, GNOME with the AppIndicator extension and
others; Qt uses it on Linux when a watcher is present). Xvfb alone has no
tray, so this is what lets the UI tests drive the real QSystemTrayIcon.

    python fake_sni.py READY_FILE [NOTIFICATIONS_FILE]

With NOTIFICATIONS_FILE it also stands in for the notification service
(org.freedesktop.Notifications), writing each notification as a JSON line
{"summary", "body"} there.

Once the names are owned it writes READY_FILE: JSON with the session bus
address, DISPLAY and XAUTHORITY, so a test can join the app's session:
talk to the registered tray item over D-Bus (its tooltip and icon, a click
with Activate, its menu through com.canonical.dbusmenu) and to its window
over X11 (tests/x11win.py)."""

import json
import os
import sys

from jeepney import HeaderFields, MessageType, new_error, new_method_return
from jeepney.bus_messages import message_bus
from jeepney.io.blocking import open_dbus_connection

WATCHER = "org.kde.StatusNotifierWatcher"
NOTIFICATIONS = "org.freedesktop.Notifications"
IN_S = '<arg type="s" direction="in"/>'
INTROSPECT = f"""<!DOCTYPE node PUBLIC "-//freedesktop//DTD D-BUS Object Introspection 1.0//EN"
"http://www.freedesktop.org/standards/dbus/1.0/introspect.dtd">
<node>
 <interface name="{WATCHER}">
  <method name="RegisterStatusNotifierItem">{IN_S}</method>
  <method name="RegisterStatusNotifierHost">{IN_S}</method>
  <property name="RegisteredStatusNotifierItems" type="as" access="read"/>
  <property name="IsStatusNotifierHostRegistered" type="b" access="read"/>
  <property name="ProtocolVersion" type="i" access="read"/>
  <signal name="StatusNotifierItemRegistered"><arg type="s"/></signal>
  <signal name="StatusNotifierItemUnregistered"><arg type="s"/></signal>
  <signal name="StatusNotifierHostRegistered"/>
 </interface>
 <interface name="org.freedesktop.DBus.Properties">
  <method name="Get">{IN_S}{IN_S}<arg type="v" direction="out"/></method>
  <method name="GetAll">{IN_S}<arg type="a{{sv}}" direction="out"/></method>
 </interface>
 <interface name="org.freedesktop.DBus.Introspectable">
  <method name="Introspect"><arg type="s" direction="out"/></method>
 </interface>
</node>
"""


def main() -> None:
    ready = sys.argv[1]
    notes = open(sys.argv[2], "a", encoding="utf-8") if len(sys.argv) > 2 else None  # noqa: SIM115
    conn = open_dbus_connection(bus="SESSION")
    for name in [WATCHER] + ([NOTIFICATIONS] if notes else []):
        if conn.send_and_get_reply(message_bus.RequestName(name)).body[0] != 1:
            sys.exit(f"couldn't own {name}")
    items: list[str] = []
    tmp = ready + ".tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        json.dump({k: os.environ.get(k, "") for k in ("DBUS_SESSION_BUS_ADDRESS", "DISPLAY", "XAUTHORITY")}, f)
    os.replace(tmp, ready)

    def props() -> dict:
        return {
            "RegisteredStatusNotifierItems": ("as", list(items)),
            "IsStatusNotifierHostRegistered": ("b", True),
            "ProtocolVersion": ("i", 0),
        }

    next_id = 1
    while True:
        try:
            msg = conn.receive()
        except OSError:  # the session bus went away with the test's session
            return
        if msg.header.message_type != MessageType.method_call:
            continue
        member = msg.header.fields.get(HeaderFields.member)
        if member == "Notify" and notes:
            _app, _replaces, _icon, summary, body = msg.body[:5]
            notes.write(json.dumps({"summary": summary, "body": body}) + "\n")
            notes.flush()
            conn.send(new_method_return(msg, "u", (next_id,)))
            next_id += 1
        elif member == "GetCapabilities":
            conn.send(new_method_return(msg, "as", (["body"],)))
        elif member == "GetServerInformation":
            conn.send(new_method_return(msg, "ssss", ("fake", "tests", "1", "1.2")))
        elif member == "Introspect":
            conn.send(new_method_return(msg, "s", (INTROSPECT,)))
        elif member == "RegisterStatusNotifierItem":
            # Qt passes its own bus name; the item is at /StatusNotifierItem.
            service = msg.body[0]
            items.append(service if service.startswith((":", "org.")) else msg.header.fields[HeaderFields.sender])
            conn.send(new_method_return(msg))
        elif member == "RegisterStatusNotifierHost":
            conn.send(new_method_return(msg))
        elif member == "Get":
            value = props().get(msg.body[1])
            if value is None:
                conn.send(new_error(msg, "org.freedesktop.DBus.Error.UnknownProperty", "s", (msg.body[1],)))
            else:
                conn.send(new_method_return(msg, "v", (value,)))
        elif member == "GetAll":
            conn.send(new_method_return(msg, "a{sv}", (props(),)))
        else:
            conn.send(new_error(msg, "org.freedesktop.DBus.Error.UnknownMethod", "s", (str(member),)))


if __name__ == "__main__":
    main()
