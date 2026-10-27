"""The part of the websockets API the client uses, on the browser's WebSocket.

The desktop client uses the real websockets package. In the browser there are
no sockets, so relay.py's `connect`, `ClientConnection` and the exceptions it
catches come from here instead (clients/web/python is first on sys.path).
"""
