"""Click-through tests of everyday UI details: the theme switch before unlock
(dark by default), generated passphrases, error toasts that stay and copy,
clearly styled fields, code blocks with Copy, Settings → Data & storage,
settings forms with their own footers, and making a community anyone can
find and join from search."""

import asyncio
import re

import pytest
from conftest import network, run, run_community, run_relay
from uidriver import App
from uiflows import claim, close_settings, community_section, create_profile, open_settings

pytestmark = [pytest.mark.gui, pytest.mark.timeout(360)]

# Records what the page hands to copy_text (the real call still runs).
SPY_COPY = (
    "(() => { if (window.__copied) return 0; window.__copied = []; const orig = window.pywebview.api.copy_text;"
    " window.pywebview.api.copy_text = (text) => { window.__copied.push(text); return orig(text); }; return 0; })()"
)

# WCAG contrast of a field's border against the surface behind it.
FIELD_CONTRAST = """
(sel, focused) => {
  const rgb = (c) => (c.match(/[\\d.]+/g) || []).slice(0, 4).map(Number);
  const lum = ([r, g, b]) => {
    const f = (v) => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; };
    return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b); };
  const ratio = (a, b) => { const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p); return (x + 0.05) / (y + 0.05); };
  const el = document.querySelector(sel);
  if (focused) el.focus(); else el.blur();
  let back = el.parentElement;
  while (back && rgb(getComputedStyle(back).backgroundColor)[3] === 0) back = back.parentElement;
  const cs = getComputedStyle(el);
  const surface = rgb(getComputedStyle(back || document.body).backgroundColor);
  return { border: ratio(rgb(cs.borderTopColor), surface), width: cs.borderTopWidth,
    differs: cs.backgroundColor !== getComputedStyle(back).backgroundColor };
}
"""


async def theme(app: App) -> str:
    return await app.js("document.documentElement.dataset.theme")


def test_theme_passphrases_toasts_fields_and_storage(tmp_path):
    with run_relay() as relay, run_community(tmp_path, [relay]) as node:

        async def main():
            home = tmp_path / "a"
            app = await App(home, name="a").start()
            try:
                # Dark by default, with a theme switch on the first screen.
                await app.wait_text("#lock button", "Create a profile")
                assert await theme(app) == "dark"
                assert (
                    await app.js(
                        "document.querySelector('.theme-switch button[data-theme=dark]').getAttribute('aria-pressed')"
                    )
                    == "true"
                )
                await app.click(".theme-switch button", "Light")
                await app.wait_js("document.documentElement.dataset.theme === 'light'", "light theme")
                prefs = home / "config" / "preferences.toml"
                assert 'theme = "light"' in prefs.read_text()
                # The switch is on the other screens before unlock too.
                await app.button("Create a profile")
                await app.wait_js("document.querySelector('#lock .theme-switch')", "switch on Create")
                assert (
                    await app.js(
                        "document.querySelector('.theme-switch button[data-theme=light]').getAttribute('aria-pressed')"
                    )
                    == "true"
                )
                await app.click(".theme-switch button", "Dark")
                await app.wait_js("document.documentElement.dataset.theme === 'dark'", "dark theme")
                assert 'theme = "dark"' in prefs.read_text()

                # Fields look like fields: own background, a 3:1 border, both themes.
                for name in ("dark", "light"):
                    await app.js(f"document.documentElement.dataset.theme = {name!r}; 0")
                    for focused in (False, True):
                        c = await app.call(FIELD_CONTRAST, "#lock input[type=password]", focused)
                        # A 3:1 border, and a background of its own or a 4:1 one.
                        assert c["width"] == "1px" and c["border"] >= 3, (name, focused, c)
                        assert c["differs"] or c["border"] >= 4, (name, focused, c)
                await app.js("document.documentElement.dataset.theme = 'dark'; 0")

                # Generate: six words from Python, shown in the field, the
                # repeat field filled, Copy, and the password-manager reminder.
                await app.js(SPY_COPY)
                await app.click("#lock .generate-btn")
                await app.wait_js("document.querySelector('#lock .generated-note:not(.hidden)')", "generated note")
                generated = await app.js("document.querySelector('#lock .passphrase-field input').value")
                assert re.fullmatch(r"[a-z-]+( [a-z-]+){5}", generated), generated
                assert await app.js("document.querySelector('#lock .passphrase-field input').type") == "text"
                assert await app.js("document.querySelectorAll('#lock input[type=password]')[0].value") == generated
                await app.wait_text("#lock .generated-note", "password manager")
                # The button says Copied for a moment: record its labels as
                # they change (a poll can miss the moment on a busy machine).
                await app.js(
                    "(() => { const b = document.querySelector('#lock .generated-note button'); window.__labels = [];"
                    " new MutationObserver(() => window.__labels.push(b.textContent))"
                    ".observe(b, { childList: true, characterData: true, subtree: true }); return 0; })()"
                )
                await app.button("Copy passphrase")
                await app.wait_js("window.__labels.includes('Copied')", "the Copied label")
                assert await app.js("window.__copied") == [generated]
                await app.wait_text("#lock .passphrase-field .hint", "trong")  # Strong or Very strong
                # Hide and Show again.
                await app.click("#lock .reveal-btn")
                assert await app.js("document.querySelector('#lock .passphrase-field input').type") == "password"
                await app.click("#lock .reveal-btn")
                assert await app.js("document.querySelector('#lock .passphrase-field input').type") == "text"
                # A second Generate gives a different one.
                await app.click("#lock .generate-btn")
                await app.wait_js(
                    f"document.querySelector('#lock .passphrase-field input').value !== {generated!r}", "new passphrase"
                )
                generated = await app.js("document.querySelector('#lock .passphrase-field input').value")
                await app.button("Create profile")
                await app.wait_js("!document.querySelector('#app').classList.contains('hidden')", "main window", 40)

                # The generated passphrase is the profile's passphrase.
                await app.click('.user-panel button[title="Lock now"]')
                await app.wait_js("document.querySelector('#lock input[type=password]')", "lock screen")
                assert await app.js("!!document.querySelector('#lock .theme-switch')")
                await app.fill("#lock input[type=password]", generated)
                await app.button("Unlock")
                await app.wait_js("!document.querySelector('#app').classList.contains('hidden')", "unlocked", 40)

                # An error toast stays, keeps its text through a click, copies, and closes with its close button.
                await app.js(SPY_COPY)
                await app.click('.rail-item[title="Direct messages"]')
                await app.click('button[aria-label="New group DM"]')
                await app.wait_text(".modal h2", "New group DM")
                await app.click(".modal-foot button", "Create")  # nobody picked
                joined = "Pick at least one person."
                await app.wait_text("#toasts .toast.error", joined)
                # That toast (other errors may stack with it): its text, then
                # its own buttons, by its position among the error toasts.
                await app.click("#toasts .toast.error .toast-text", joined)
                await asyncio.sleep(6)
                errors = "[...document.querySelectorAll('#toasts .toast.error')]"
                nth = await app.js(f"{errors}.findIndex((t) => t.textContent.includes({joined!r}))")
                assert nth >= 0, "the error toast went away by itself or on a click"
                that = f"{errors}[{nth}]"
                assert await app.js(f"getComputedStyle({that}.querySelector('.toast-text')).userSelect") != "none"
                assert await app.js(f"{that}.getAttribute('role')") == "alert"
                await app.click("#toasts .toast.error button", "Copy the error", nth=nth)
                assert joined in (await app.js("window.__copied"))[-1]
                await app.click("#toasts .toast.error button", "Dismiss", nth=nth)
                await app.wait_gone("#toasts .toast.error .toast-text", joined)
                await app.button("Cancel")
                await app.wait_gone(".modal")

                # Settings → Data & storage: every path, full and copyable.
                await open_settings(app)
                await app.click(".settings nav .item", "Data & storage")
                await app.wait_js("document.querySelector('.storage-row[data-location=vault]')", "storage list")
                paths = await app.call(
                    "() => Object.fromEntries([...document.querySelectorAll('.storage-row')]"
                    ".map((r) => [r.dataset.location, r.querySelector('.code-text').textContent]))"
                )
                root = home.resolve()
                assert paths["vault"] == str(root / "data" / "vault.bin")
                assert paths["preferences"] == str(root / "config" / "preferences.toml")
                assert paths["logs"] == str(root / "log") and paths["crash"] == str(root / "log" / "crash.log")
                assert paths["dm_files"] == str(root / "cache" / "dm-files")
                assert paths["update_cache"] == str(root / "cache" / "updates")
                assert paths["keychain"].startswith("OpenArcanum profile key / ")
                assert {"config", "data", "state", "dms", "olm", "cache", "community_cache", "log"} <= set(paths)
                await app.click(".storage-row[data-location=vault] button", "Copy the path of Profile")
                assert (await app.js("window.__copied"))[-1] == str(root / "data" / "vault.bin")
                assert await app.exists(".storage-row[data-location=data] button", "Open the folder of Data folder")
                assert not await app.exists(
                    ".storage-row[data-location=keychain] button", "Open the folder of Keychain entry"
                )
                # Info toasts still go by themselves.
                await app.click(".settings section .form-foot button", "Clear cache")
                await app.wait_text(".modal h2", "Clear the cache?")
                await app.click(".modal-foot button", "Clear cache")
                await app.wait_text("#toasts .toast", "Cache cleared.")
                await app.wait_gone("#toasts .toast", None, 10)

                # Security: each part is its own form with its actions in its footer.
                await app.click(".settings nav .item", "Security")
                await app.wait_text(".settings section .form-section h3", "Backup")
                assert await app.js(
                    "[...document.querySelectorAll('.settings section .form-section')]"
                    ".find((s) => s.querySelector('h3').textContent === 'Backup')"
                    ".querySelector('.form-foot button').textContent === 'Export backup…'"
                )

                # Export backup with a generated passphrase.
                await app.button("Export backup…")
                await app.wait_text(".modal h2", "Export backup")
                await app.click(".modal .generate-btn")
                await app.wait_text(".modal .generated-note", "apart from the backup file")
                assert await app.js(
                    "document.querySelector('.modal .passphrase-field input').value"
                    " === document.querySelectorAll('.modal input[type=password]')[0].value"
                )
                await app.fill('.modal input[aria-label="Save to"]', str(tmp_path / "backup.fbak"))
                await app.fill(".modal input[autocomplete=current-password]", generated)
                await app.button("Save backup")
                await app.wait_text("#toasts .toast", "Backup saved.")
                assert (tmp_path / "backup.fbak").exists()

                # Change passphrase to a generated one, then unlock with it.
                await app.button("Change passphrase…")
                await app.fill(".modal input[autocomplete=current-password]", generated)
                await app.click(".modal .generate-btn")
                await app.wait_js("document.querySelector('.modal .generated-note:not(.hidden)')", "generated note")
                newer = await app.js("document.querySelector('.modal .passphrase-field input').value")
                assert newer != generated and len(newer.split(" ")) == 6
                await app.button("Change")
                await app.wait_text("#toasts .toast", "Passphrase changed.")
                await close_settings(app)
                await app.click('.user-panel button[title="Lock now"]')
                await app.wait_js("document.querySelector('#lock input[type=password]')", "lock screen")
                await app.fill("#lock input[type=password]", newer)
                await app.button("Unlock")
                await app.wait_js("!document.querySelector('#app').classList.contains('hidden')", "unlocked", 40)

                # The add-community page fills the center section; the claim
                # commands sit in a code block that scrolls sideways inside
                # its section's box.
                await app.click(".rail-add")
                await app.click('.add-way[data-tab="server"]')
                await app.wait_js("document.querySelector('.claim-commands .code-text')", "claim commands")
                box = await app.call(
                    "() => { const p = document.querySelector('.claim-commands .code-text');"
                    " const cs = getComputedStyle(p);"
                    " const m = p.closest('.form-section').getBoundingClientRect();"
                    " const main = document.querySelector('#main').getBoundingClientRect();"
                    " const page = document.querySelector('#main .add-page').getBoundingClientRect();"
                    " return { ws: cs.whiteSpace, ox: cs.overflowX, fits: p.getBoundingClientRect().right <= m.right,"
                    " width: m.width, tab: p.tabIndex,"
                    " fills: page.width === main.width && !document.querySelector('.modal') }; }"
                )
                assert (
                    box["ws"] == "pre"
                    and box["ox"] == "auto"
                    and box["fits"]
                    and box["width"] <= 780
                    and box["tab"] == 0
                    and box["fills"]
                ), box
                await app.click(".claim-commands button", "Copy the commands")
                assert (await app.js("window.__copied"))[-1].startswith("community init -data ./my-community")
                await app.key("Escape")
                await app.wait_gone(".add-page")

                # The claim wizard: the file size starts at 10 MB; the first
                # invite in a code block.
                await app.click(".rail-add")
                await app.click('.add-way[data-tab="server"]')
                if await app.js("!!document.querySelector('.add-page input[name=host-where]')"):
                    await app.click('.add-page input[name=host-where][value="new"]')
                await app.fill(".add-page textarea", node.claim_code)
                await app.button("Next")
                await app.wait_text(".add-page .form-section h3", "Set up your community")
                assert (
                    await app.js(
                        "[...document.querySelectorAll('.add-page label.field')]"
                        ".find((l) => l.textContent.includes('Largest file (MB)'))"
                        ".querySelector('input').value"
                    )
                    == "10"
                )
                for focused in (False, True):
                    c = await app.call(FIELD_CONTRAST, '.add-page input[placeholder="Community name"]', focused)
                    assert c["border"] >= 3 and c["differs"], (focused, c)
                await app.fill('.add-page input[placeholder="Community name"]', "UX Town")
                await app.fill('.add-page input[placeholder="Your display name"]', "Olive")
                await app.button("Claim and create")
                await app.wait_js("document.querySelector('.invite-code .code-text')", "claimed dialog", 40)
                invite = await app.js("document.querySelector('.invite-code .code-text').textContent")
                assert invite.startswith("openarcanum-invite:")
                await app.click(".invite-code button", "Copy invite")
                assert (await app.js("window.__copied"))[-1] == invite
                await app.button("Export backup…")
                await app.button("Cancel")
                await app.wait_gone("#modal-root .backdrop")

                # Community settings → Files: the new community's 10 MB, in a
                # form whose Save sits in its own footer.
                await community_section(app, "Files")
                assert (
                    await app.js("document.querySelector('.settings section .form-section input[type=number]').value")
                    == "10"
                )
                assert await app.exists(".settings section .form-section .form-foot button", "Save changes")
                await close_settings(app)
                app.assert_no_errors()
            finally:
                await app.stop()

        run(main(), 300)


def test_anyone_can_find_and_join_from_search(tmp_path):
    with network(tmp_path) as (meta, _relay, community):
        node = community()

        async def main():
            owner = await App(tmp_path / "owner", name="owner", args=["--maintainer", meta]).start()
            seeker = None
            try:
                await create_profile(owner)
                await claim(owner, node.claim_code, "Open Bakery", "Olive")

                # Visibility explains who can find and join, and links to the join policy.
                await community_section(owner, "Visibility")
                await owner.wait_text(".settings section .reach-now", "only people with an invite link")
                await owner.button("Change join policy…")
                await owner.wait_text(".settings section h2", "Invites & join policy")
                # Save changes belongs to the join policy form, apart from the invites below.
                assert await owner.js(
                    "(() => { const f = document.querySelector('.settings section .form-section');"
                    " const save = [...f.querySelectorAll('.form-foot button')]"
                    ".find((b) => b.textContent === 'Save changes');"
                    " const invites = [...document.querySelectorAll('.settings section h3')]"
                    ".find((h) => h.textContent === 'Invites');"
                    " return !!save && !!invites && !f.contains(invites); })()"
                )
                await owner.select(
                    ".settings section .form-section select", "Open (anyone who finds it can join, no invite needed)"
                )
                await owner.wait_text(".settings section .reach-now", "only people with an invite link")
                await owner.button("Save changes")
                await owner.wait_text("#toasts .toast", "Saved")
                await owner.button("Visibility…")
                await owner.wait_text(".settings section h2", "Visibility")
                await owner.select(".settings section select", "Public: listed and searchable in the directory")
                await owner.wait_text(".settings section .reach-preview", "join straight away")
                await owner.click(".settings section .form-foot button", "Save changes")
                await owner.wait_text(
                    ".settings section .reach-now", "Anyone can find it in directory search and join straight away"
                )
                # Already fully public: no one-click button.
                assert not await owner.exists(".settings section button", "Let anyone find and join")
                await close_settings(owner)

                # Someone else finds it in search and joins with no invite or key.
                seeker = await App(tmp_path / "seeker", name="seeker", args=["--maintainer", meta]).start()
                await create_profile(seeker)
                await seeker.click(".rail-add")
                await seeker.click('.add-way[data-tab="find"]')
                for _ in range(40):
                    await seeker.fill(".add-page input[type=search]", "Open Bakery")
                    await seeker.button("Search")
                    await asyncio.sleep(0.5)
                    if await seeker.exists(".add-page .result b", "Open Bakery"):
                        break
                await seeker.click(".add-page .result b", "Open Bakery")
                await seeker.wait_text(".add-page .open-join", "no invite needed")
                await seeker.fill('.add-page input[placeholder="How others will see you here"]', "Sam")
                await seeker.button("Join")
                await seeker.wait_js("document.querySelector('#composer-input')", "joined from search", 40)
                await seeker.wait_text(".modal h2", "Back up your keys")
                await seeker.button("Later")
                await seeker.wait_text(".side-head .name", "Open Bakery")
                seeker.assert_no_errors()

                # The one-click button: back to private, then "Let anyone find and join".
                await community_section(owner, "Visibility")
                await owner.select(".settings section select", "Private: reachable only through invite links")
                await owner.click(".settings section .form-foot button", "Save changes")
                await owner.wait_text(".settings section .reach-now", "Not listed")
                await owner.button("Let anyone find and join")
                await owner.button("Make it public")
                await owner.wait_text(".settings section .reach-now", "Anyone can find it in directory search")
                owner.assert_no_errors()
            finally:
                await owner.stop()
                if seeker is not None:
                    await seeker.stop()

        run(main(), 300)
