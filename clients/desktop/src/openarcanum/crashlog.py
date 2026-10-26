"""Crash logs: local only, no telemetry, never message content.

Everything goes to the log folder on this computer and nowhere else:

- uncaught exceptions on the main thread, other threads and the core's event
  loop are logged with their stack;
- a native crash (a fault in Qt, Chromium or a library) leaves the Python
  stacks of every thread in `crash.log` (faulthandler);
- a run that didn't end cleanly is noticed at the next start, and the app
  then says where the log is.

Logs never carry message content, keys or passphrases. An exception's text
can quote what it failed on (a parse error repeating its input), so only the
exception's type and stack are logged, with the text kept only for errors
whose wording the app writes itself, and for system errors (file paths,
"connection refused").
"""

from __future__ import annotations

import asyncio
import faulthandler
import logging
import os
import sys
import threading
import time
import traceback
from pathlib import Path

log = logging.getLogger("openarcanum")

# Errors whose message is safe to log: written by this app (never quoting
# user data), or by the system about files and connections.
SAFE_MESSAGE_TYPES: tuple[type[BaseException], ...] = (OSError, TimeoutError, asyncio.TimeoutError)
SAFE_MESSAGE_MODULES = ("openarcanum.",)

MARKER = "running"
CRASH_FILE = "crash.log"

# Set at startup: the last run didn't exit cleanly.
last_run_crashed = False
_fault_file = None


def _safe_text(e: BaseException) -> str:
    t = type(e)
    if isinstance(e, SAFE_MESSAGE_TYPES) or t.__module__.startswith(SAFE_MESSAGE_MODULES):
        return str(e)
    return "(details withheld)"


def format_exception(ei) -> str:
    """A traceback with frames (file, line, function, source line) and each
    exception's type; the text only for safe types. Chained exceptions too."""
    _, exc, tb = ei
    parts: list[str] = []
    seen: set[int] = set()
    while exc is not None and id(exc) not in seen:
        seen.add(id(exc))
        frames = "".join(traceback.format_tb(tb if not parts else exc.__traceback__))
        parts.append(f"Traceback (most recent call last):\n{frames}{type(exc).__qualname__}: {_safe_text(exc)}")
        nxt = exc.__cause__ or (None if exc.__suppress_context__ else exc.__context__)
        exc, tb = nxt, None
    return "\n\nWhile handling the above:\n".join(reversed(parts))


class SafeFormatter(logging.Formatter):
    def formatException(self, ei) -> str:
        return format_exception(ei)


def install(log_dir: Path) -> None:
    """Hooks for uncaught exceptions and native crashes; notes an unclean
    previous run. Call once, after logging is set up."""
    global last_run_crashed, _fault_file
    marker = log_dir / MARKER
    if marker.exists():
        last_run_crashed = True
        log.warning("the previous run didn't exit cleanly (log folder: %s)", log_dir)
    marker.write_text(f"{os.getpid()} {int(time.time())}\n")
    os.chmod(marker, 0o600)

    crash = log_dir / CRASH_FILE
    _fault_file = open(crash, "a", encoding="utf-8")  # noqa: SIM115 (kept open for faulthandler)
    os.chmod(crash, 0o600)
    faulthandler.enable(file=_fault_file, all_threads=True)

    def excepthook(t, e, tb) -> None:
        log.critical("uncaught exception", exc_info=(t, e, tb))

    def thread_hook(args) -> None:
        if args.exc_type is SystemExit:
            return
        name = args.thread.name if args.thread else "?"
        log.critical(
            "uncaught exception in thread %s", name, exc_info=(args.exc_type, args.exc_value, args.exc_traceback)
        )

    sys.excepthook = excepthook
    threading.excepthook = thread_hook


def loop_exception_handler(loop: asyncio.AbstractEventLoop, context: dict) -> None:
    """For the core's event loop: errors nobody awaited."""
    e = context.get("exception")
    what = context.get("message", "error in the event loop")
    if e is not None:
        log.error("%s", what, exc_info=(type(e), e, e.__traceback__))
    else:
        log.error("%s", what)


def clean_exit(log_dir: Path) -> None:
    """The run ended normally: no crash notice next time. Only this
    process's own marker is removed (another run of the profile, such as the
    version an update just started, may have written its own)."""
    marker = log_dir / MARKER
    try:
        if marker.read_text().split()[:1] == [str(os.getpid())]:
            marker.unlink()
    except FileNotFoundError:
        pass
    except OSError:
        log.warning("couldn't remove the crash marker")
    if _fault_file is not None:
        faulthandler.disable()
