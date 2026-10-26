# OpenArcanum desktop app

The OpenArcanum app for Linux, Windows and macOS. It is a Python program that
shows its window with [pywebview](https://pywebview.flowrl.com/) (Qt
WebEngine). Python does all the work that matters: it keeps your keys, talks
to the servers and encrypts everything. The window only draws the interface,
which is plain HTML, CSS and JavaScript in `../shared/ui/` (shared with the
web client, no build step). The page reaches Python through
`window.pywebview.api` and never stores anything in the browser engine.

How the pieces fit together is in `../../docs/architecture.md`, the wire
format in `../../docs/protocol.md`, and every setting in
`../../docs/settings.md`.

## Running it

You need [uv](https://docs.astral.sh/uv/). From the repository root,
`make desktop` runs the app from source (`make desktop NAME=alice` gives each
name its own profile). Or, from this folder:

```bash
uv sync
uv run python -m openarcanum                    # your real profile, in the usual app folders for your OS
uv run python -m openarcanum --home /tmp/alice  # keep everything in one folder
uv run python -m openarcanum --debug            # allow DevTools (Ctrl+Shift+I)
```

DevTools also open with `OPENARCANUM_DEVTOOLS=1`. A DevTools port set in the
environment (`QTWEBENGINE_REMOTE_DEBUGGING`, or `--remote-debugging-port` in
`QTWEBENGINE_CHROMIUM_FLAGS`) is ignored unless the app starts with `--debug`
or `--allow-remote-debugging`, because any program on the computer could use
that port to control the unlocked app.

## Tests

```bash
make test-py      # from the repository root; builds the servers first
```

or `uv run pytest` here once `make build` has put the servers in `../../bin`.
Tests that open windows are marked `gui`. Without a display (on Linux, Xvfb
and `dbus-run-session`) they are skipped, and with `CI=true` they fail
instead. The UI tests (`tests/uidriver.py`) start the app with
`--allow-remote-debugging` to drive it.

## Building the app

`make bundle` builds the app folder in `dist/` and `make appimage` packs it
into a Linux AppImage. The Windows installer and the macOS `.dmg`, and how
each is signed, are described in `../../docs/packaging.md`.

## Interface text

Text shown in the interface is always wrapped for translation: the page uses
`t()` / `tn()` (`ui/js/i18n.js`) and Python uses `_()` / `ngettext()`
(`openarcanum/i18n.py`). `make strings` rebuilds the English catalog,
`../shared/ui/strings/en.json`, and `make strings-check` (part of
`make check`) fails if the catalog is out of date, if placeholders don't
match, or if visible text isn't wrapped. Adding a language is described in
`../../docs/translations.md`.
