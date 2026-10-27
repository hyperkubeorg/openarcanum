class WebSocketException(Exception):
    pass


class ConnectionClosed(WebSocketException):
    def __init__(self, code: int = 1006, reason: str = ""):
        super().__init__(f"connection closed ({code}{': ' + reason if reason else ''})")
        self.code = code
        self.reason = reason


class _Response:
    def __init__(self, status_code: int):
        self.status_code = status_code


class InvalidStatus(WebSocketException):
    """Never raised here: a browser doesn't say why a WebSocket handshake
    failed, so a refused connection surfaces as ConnectionClosed."""

    def __init__(self, status_code: int = 0):
        super().__init__(f"HTTP {status_code}")
        self.response = _Response(status_code)
