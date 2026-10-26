"""A stand-in for the desktop's notification service (tests): owns
org.freedesktop.Notifications on the session bus and writes each Notify call
(summary and body) as a JSON line to the file named on the command line."""

import json
import sys

from jeepney import HeaderFields, MessageType, new_method_return
from jeepney.bus_messages import message_bus
from jeepney.io.blocking import open_dbus_connection

NAME = "org.freedesktop.Notifications"


def main() -> None:
    out = open(sys.argv[1], "a", encoding="utf-8")  # noqa: SIM115
    conn = open_dbus_connection(bus="SESSION")
    reply = conn.send_and_get_reply(message_bus.RequestName(NAME))
    if reply.body[0] != 1:
        sys.exit("couldn't own the notifications name")
    print("ready", flush=True)
    next_id = 1
    while True:
        msg = conn.receive()
        if msg.header.message_type != MessageType.method_call:
            continue
        member = msg.header.fields.get(HeaderFields.member)
        if member == "Notify":
            _app, _replaces, _icon, summary, body = msg.body[:5]
            out.write(json.dumps({"summary": summary, "body": body}) + "\n")
            out.flush()
            conn.send(new_method_return(msg, "u", (next_id,)))
            next_id += 1
        elif member == "GetCapabilities":
            conn.send(new_method_return(msg, "as", (["body"],)))
        elif member == "GetServerInformation":
            conn.send(new_method_return(msg, "ssss", ("fake", "tests", "1", "1.2")))


if __name__ == "__main__":
    main()
