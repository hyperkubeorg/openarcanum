"""Auto-update by clicks: the window's own startup check
finds a signed update on a local update host, downloads and verifies it, and
asks. "Later" and "Skip this version" leave the AppImage alone; "Restart and
install" replaces it and starts the new one with the same arguments. A
temp file stands in for $APPIMAGE: a shell script that records being run.

The test release key reaches the app only through the environment
(OPENARCANUM_UPDATE_TEST_*), honored for an update host on this computer."""

import asyncio
import sys
import time
from pathlib import Path

import pytest
from uidriver import App
from uiflows import create_profile, open_settings, unlock
from updateserver import UpdateServer, asset_for, fake_appimage, publish

from openarcanum import crashlog, keys, updates

pytestmark = [pytest.mark.gui, pytest.mark.timeout(240)]


async def wait_for(pred, what: str, timeout: float = 30.0) -> None:
    deadline = time.monotonic() + timeout
    while not pred():
        if time.monotonic() > deadline:
            raise AssertionError(f"timed out waiting for {what}")
        await asyncio.sleep(0.1)


# Running from source, only the AppImage path can install (Windows needs the
# installed copy, macOS the app bundle): test_updates.py runs those installs
# on their own OS.
@pytest.mark.skipif(not sys.platform.startswith("linux"), reason="the AppImage path is Linux's")
def test_update_flow_ui(tmp_path):
    key = keys.SigningKey.generate()
    appimage = tmp_path / "app" / "OpenArcanum.AppImage"
    appimage.parent.mkdir()
    old = fake_appimage(appimage, tmp_path / "old-started.txt", "old version")
    marker = tmp_path / "restarted.txt"
    home = tmp_path / "home"
    # The new version must not find the old run's crash marker.
    new = fake_appimage(None, marker, "new version", watch=home / "log" / crashlog.MARKER)
    with UpdateServer() as server:
        publish(server, [key], "9999.1.0", [asset_for(server, "/OpenArcanum-9999.1.0.AppImage", new)])
        env = {
            updates.TEST_KEYS_ENV: updates.b64u(key.public),
            updates.TEST_URL_ENV: server.url("/manifest.json"),
            "APPIMAGE": str(appimage),
            # As the AppImage runtime sets it: the mount the app runs from
            # (here, the interpreter's folder; the test source waives "frozen").
            "APPDIR": str(Path(sys.executable).resolve().parent),
        }

        async def main():
            # The restart keeps the options (test_app.py checks that it drops an
            # invite link the app was opened with).
            app = await App(home, env=env, name="updater", args=["--verbose"]).start()
            try:
                await create_profile(app)
                # The startup check found, downloaded and verified 9999.1.0: the prompt asks.
                await app.wait_text(".update-dialog h2", "Update ready", 60)
                await app.wait_text(".update-dialog", "Version 9999.1.0 is ready.")
                assert server.paths()[:2] == ["/manifest.json", "/OpenArcanum-9999.1.0.AppImage"]
                await app.button("Later")
                await app.wait_gone(".update-dialog")
                assert appimage.read_bytes() == old

                # Settings → Updates: version, the automatic-check toggle, status.
                await open_settings(app)
                await app.click(".settings nav .item", "Updates")
                await app.wait_text(".settings .update-status", "Version 9999.1.0 is ready to install.")
                await app.wait_text(".settings section", f"{updates.PRODUCT_NAME} {updates.__version__}")
                await app.wait_text(".settings section", "Using a test update source")
                # "Install…" reopens the prompt; closing it (Escape) is "Later".
                await app.click(".settings section button", "Install…")
                await app.wait_text(".update-dialog", "Version 9999.1.0 is ready.")
                await app.key("Escape")
                await app.wait_gone(".update-dialog")
                await app.wait_js("document.querySelector('.settings')", "settings still open")
                assert appimage.read_bytes() == old
                toggle = ".settings section .setting-row input[type=checkbox]"
                assert await app.js(f"document.querySelector('{toggle}').checked")
                await app.click(toggle)
                prefs = home / "config" / "preferences.toml"
                await wait_for(lambda: "update_check = false" in prefs.read_text(), "the setting saved")
                await app.click(toggle)
                await wait_for(lambda: "update_check = true" in prefs.read_text(), "the setting saved")

                # "Check now" asks again; "Skip this version" leaves it alone.
                await app.click(".settings section button", "Check now")
                await app.wait_text(".update-dialog", "Version 9999.1.0 is ready.")
                await app.button("Skip this version")
                await app.wait_gone(".update-dialog")
                await app.wait_text(".settings .update-status", "Version 9999.1.0 is available (skipped).")
                assert '"skipped": "9999.1.0"' in (home / "config" / "updates.json").read_text()
                assert appimage.read_bytes() == old

                # A page script can't install without the token Python issued
                # with the prompt (the page never had one for a skipped version).
                res = await app.js("window.pywebview.api.update_install('9999.1.0', 'guess')")
                assert not res["ok"] and appimage.read_bytes() == old

                # "Check now" still finds a skipped version; install it.
                await app.click(".settings section button", "Check now")
                await app.wait_text(".update-dialog", "Version 9999.1.0 is ready.")
                assert appimage.read_bytes() == old and not marker.exists()

                # Python refuses to install while the profile is locked, even
                # with the prompt's token (the prompt only shows when unlocked).
                token = (await app.js("window.pywebview.api.update_status()"))["value"]["token"]
                await app.js("lockNow(); 0")
                await app.wait_js("!document.querySelector('#lock').classList.contains('hidden')", "locked")
                res = await app.js(f"window.pywebview.api.update_install('9999.1.0', {token!r})")
                assert not res["ok"] and "Unlock" in res["error"], res
                assert appimage.read_bytes() == old
                # Unlocked again, the prompt the lock took away comes back.
                await unlock(app)
                await app.wait_text(".update-dialog", "Version 9999.1.0 is ready.")
                app.assert_no_errors()
                app.assert_local_only()
                await app.button("Restart and install")
                # The app shuts down normally, the AppImage now holds 9999.1.0,
                # and the new one starts with the same arguments.
                await wait_for(marker.exists, "the new version to start", 60)
                assert appimage.read_bytes() == new
                assert marker.read_text().splitlines() == [
                    "new version",
                    "--home",
                    str(home),
                    "--verbose",
                    "watched file absent",
                ]
                assert await asyncio.to_thread(app.proc.wait, 60) == 0
                assert sorted(p.name for p in appimage.parent.iterdir()) == ["OpenArcanum.AppImage"]
            finally:
                await app.stop()

        asyncio.run(main())


def test_updates_not_configured_ui(tmp_path, monkeypatch):
    """This repository's build has no release key: Settings → Updates says so
    and "Check now" is off; nothing is downloaded or installed."""
    for var in (updates.TEST_KEYS_ENV, updates.TEST_URL_ENV):
        monkeypatch.delenv(var, raising=False)
    appimage = tmp_path / "OpenArcanum.AppImage"
    old = fake_appimage(appimage, tmp_path / "started.txt", "old version")

    async def main():
        app = await App(tmp_path / "home", env={"APPIMAGE": str(appimage)}, name="unconfigured").start()
        try:
            await create_profile(app)
            await open_settings(app)
            await app.click(".settings nav .item", "Updates")
            await app.wait_text(".settings .update-status", "Updates aren't set up for this version.")
            assert await app.js(
                "[...document.querySelectorAll('.settings section button')]"
                ".find((b) => b.textContent === 'Check now').disabled"
            )
            assert not await app.exists(".update-dialog")
            assert not (tmp_path / "home" / "cache" / "updates").exists()
            assert appimage.read_bytes() == old
            app.assert_no_errors()
        finally:
            await app.stop()

    asyncio.run(main())
