"""Crash logs stay local and never hold message content.
Uncaught errors anywhere are logged with their stack but without text that
could quote content; native crashes leave stacks in crash.log; the next
start notices a run that didn't end cleanly."""

import logging
import os
import subprocess
import sys
import textwrap

import pytest

from openarcanum import crashlog

SECRET = "the secret message text 7d3f"


def test_exception_text_is_withheld_unless_safe():
    def ei(e):
        try:
            raise e
        except BaseException as x:
            return (type(x), x, x.__traceback__)

    out = crashlog.format_exception(ei(ValueError(f"cannot parse {SECRET}")))
    assert SECRET not in out and "ValueError: (details withheld)" in out and "test_crashlog.py" in out
    assert "No such file" in crashlog.format_exception(ei(FileNotFoundError(2, "No such file", "/x")))
    from openarcanum.errors import UserError

    assert "Pick a channel" in crashlog.format_exception(ei(UserError("Pick a channel")))
    # Chained: both stacks, neither text.
    try:
        try:
            raise KeyError(SECRET)
        except KeyError as inner:
            raise RuntimeError(SECRET) from inner
    except RuntimeError as outer:
        out = crashlog.format_exception((RuntimeError, outer, outer.__traceback__))
    assert SECRET not in out and "KeyError" in out and "RuntimeError" in out


def test_log_records_use_the_safe_format(tmp_path):
    h = logging.FileHandler(tmp_path / "x.log")
    h.setFormatter(crashlog.SafeFormatter("%(message)s"))
    lg = logging.getLogger("test-crashlog")
    lg.addHandler(h)
    try:
        raise ValueError(SECRET)
    except ValueError:
        lg.exception("failed")
    h.close()
    text = (tmp_path / "x.log").read_text()
    assert "failed" in text and "ValueError" in text and SECRET not in text


SCRIPT = textwrap.dedent(
    """
    import faulthandler, logging, sys, threading
    from pathlib import Path
    from openarcanum import crashlog
    log_dir = Path(sys.argv[1])
    h = logging.FileHandler(log_dir / "client.log")
    h.setFormatter(crashlog.SafeFormatter("%(levelname)s %(message)s"))
    logging.getLogger().addHandler(h)
    crashlog.install(log_dir)
    print("CRASHED_BEFORE", crashlog.last_run_crashed, flush=True)
    def boom():
        raise ValueError(sys.argv[2])
    t = threading.Thread(target=boom, name="worker")
    t.start(); t.join()
    if sys.argv[3] == "segfault":
        faulthandler._sigsegv()
    crashlog.clean_exit(log_dir)
    """
)


def run_script(log_dir, mode):
    return subprocess.run([sys.executable, "-c", SCRIPT, str(log_dir), SECRET, mode], capture_output=True, text=True)


def test_uncaught_errors_and_native_crashes_are_logged_locally(tmp_path):
    first = run_script(tmp_path, "segfault")
    assert first.returncode != 0  # it really crashed
    assert "CRASHED_BEFORE False" in first.stdout
    log = (tmp_path / "client.log").read_text()
    assert "uncaught exception in thread worker" in log and "ValueError: (details withheld)" in log
    crash = (tmp_path / crashlog.CRASH_FILE).read_text()
    assert "Fatal Python error: Segmentation fault" in crash and "most recent call first" in crash
    for text in (log, crash):
        assert SECRET not in text
    # The next start knows; after a clean run, it doesn't any more.
    second = run_script(tmp_path, "clean")
    assert "CRASHED_BEFORE True" in second.stdout, second.stdout
    third = run_script(tmp_path, "clean")
    assert "CRASHED_BEFORE False" in third.stdout


@pytest.mark.gui
def test_crash_notice_and_page_errors_in_the_app(tmp_path):
    from conftest import run
    from uidriver import App

    home = tmp_path / "home"
    (home / "log").mkdir(parents=True)
    (home / "log" / crashlog.MARKER).write_text("1 1\n")  # as a crashed run leaves it

    async def main():
        app = await App(home, name="solo").start()
        try:
            await app.wait_js(
                "[...document.querySelectorAll('.toast')].some((t) => t.textContent.includes('closed unexpectedly'))",
                "the crash notice",
            )
            await app.js(f"setTimeout(() => {{ throw new TypeError({SECRET!r}); }}, 0); 0")
            await app.wait_js("true", "tick")
        finally:
            await app.stop()
        log = (home / "log" / "client.log").read_text()
        assert "page error TypeError at" in log and SECRET not in log, log[-1500:]
        assert not (home / "log" / crashlog.MARKER).exists()  # this run exited cleanly

    run(main(), 120)


def test_clean_exit_removes_only_its_own_marker(tmp_path):
    """After an update the new version may already have written its marker
    when the old one finishes: the old one leaves it alone."""
    (tmp_path / crashlog.MARKER).write_text(f"{os.getpid() + 1} 1\n")
    crashlog.clean_exit(tmp_path)
    assert (tmp_path / crashlog.MARKER).exists()
    (tmp_path / crashlog.MARKER).write_text(f"{os.getpid()} 1\n")
    crashlog.clean_exit(tmp_path)
    assert not (tmp_path / crashlog.MARKER).exists()
