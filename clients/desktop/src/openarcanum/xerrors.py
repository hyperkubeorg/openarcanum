"""Survive X11 protocol errors instead of exiting.

Xlib's default error handler prints "X Error of failed request" and exits
the process. The browser engine's screen capture uses Xlib in our process
and can trip over a window it can't read (an X_GetImage BadMatch on a window
with a different depth, or one that closed mid-capture), so listing windows
for the screen picker could quit the whole app. With this handler installed
such an error is logged and the capture of that window just fails.

Only Xlib callers are affected: Qt's own X11 code uses xcb, which reports
errors through its own path.
"""

from __future__ import annotations

import ctypes
import ctypes.util
import logging
import os

log = logging.getLogger("openarcanum")


class _XErrorEvent(ctypes.Structure):
    _fields_ = (
        ("type", ctypes.c_int),
        ("display", ctypes.c_void_p),
        ("resourceid", ctypes.c_ulong),
        ("serial", ctypes.c_ulong),
        ("error_code", ctypes.c_ubyte),
        ("request_code", ctypes.c_ubyte),
        ("minor_code", ctypes.c_ubyte),
    )


_HANDLER_TYPE = ctypes.CFUNCTYPE(ctypes.c_int, ctypes.c_void_p, ctypes.POINTER(_XErrorEvent))


def _log_error(_display, event) -> int:
    e = event.contents
    log.warning("X11 error ignored (error %d, request %d.%d)", e.error_code, e.request_code, e.minor_code)
    return 0


# Kept for the life of the process: Xlib calls it through a raw pointer.
_handler = _HANDLER_TYPE(_log_error)
_installed = False


def ignore_x_errors() -> bool:
    """Install the logging handler (once). False where there's no X11
    display or no Xlib, where there's nothing to do."""
    global _installed
    if _installed:
        return True
    if not os.environ.get("DISPLAY"):
        return False
    name = ctypes.util.find_library("X11")
    if not name:
        return False
    try:
        xlib = ctypes.CDLL(name)
    except OSError:
        return False
    xlib.XSetErrorHandler.restype = ctypes.c_void_p
    xlib.XSetErrorHandler.argtypes = (_HANDLER_TYPE,)
    xlib.XSetErrorHandler(_handler)
    _installed = True
    log.info("X11 errors are logged, not fatal")
    return True
