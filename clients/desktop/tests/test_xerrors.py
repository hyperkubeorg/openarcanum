"""An X11 protocol error doesn't end the app (xerrors.py): screen capture on
X11 can hit a window it can't read, and Xlib's default handler exits."""

import ctypes.util
import os
import subprocess
import sys

import pytest

pytestmark = pytest.mark.skipif(
    not os.environ.get("DISPLAY") or not ctypes.util.find_library("X11"), reason="needs an X11 display (Xvfb)"
)

# X_GetImage of a rectangle outside the root window: BadMatch, the error
# screen capture runs into.
CHILD = """
import ctypes, ctypes.util, sys
from openarcanum import xerrors
if sys.argv[1] == "install":
    assert xerrors.ignore_x_errors()
x = ctypes.CDLL(ctypes.util.find_library("X11"))
x.XOpenDisplay.restype = ctypes.c_void_p
x.XOpenDisplay.argtypes = (ctypes.c_char_p,)
x.XDefaultRootWindow.restype = ctypes.c_ulong
x.XDefaultRootWindow.argtypes = (ctypes.c_void_p,)
x.XGetImage.restype = ctypes.c_void_p
x.XGetImage.argtypes = (ctypes.c_void_p, ctypes.c_ulong, ctypes.c_int, ctypes.c_int, ctypes.c_uint,
                        ctypes.c_uint, ctypes.c_ulong, ctypes.c_int)
x.XSync.argtypes = (ctypes.c_void_p, ctypes.c_int)
d = x.XOpenDisplay(None)
assert d
x.XGetImage(d, x.XDefaultRootWindow(d), 100000, 100000, 10, 10, 0xFFFFFFFF, 2)
x.XSync(d, 0)
print("survived")
"""


def child(mode: str) -> subprocess.CompletedProcess:
    return subprocess.run([sys.executable, "-c", CHILD, mode], capture_output=True, text=True, timeout=30)


def test_x_error_is_logged_not_fatal():
    # The control: without the handler, Xlib exits on this error.
    bare = child("bare")
    assert bare.returncode != 0 and "X_GetImage" in bare.stderr, (bare.returncode, bare.stderr)
    r = child("install")
    assert r.returncode == 0, r.stderr
    assert r.stdout.strip() == "survived"
