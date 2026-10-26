"""UI strings in one place, end to end (docs/translations.md).

The app runs under the pseudo-locale (OPENARCANUM_LOCALE=x-pseudo): Python
builds a table from the catalog, ui/strings/en.json, that wraps every string
in ⟦…⟧, and hands it to the page. Clicking through the main screens, every
piece of visible text, every title, aria-label and placeholder, and every
option in a list must be inside ⟦…⟧ unless it is data: the test's own names
and messages (in Greek letters, so they can't be mistaken for UI text), what
the community server creates (the "general" channel, the Admin and @everyone
roles, the "First invite"), codes and keys shown for copying, URLs, the
product name, version and platform, and dates and times (formatted by the
system locale, not catalog strings). Errors raised in Python reach the page translated too.
So a translator's table changes the whole UI, and nothing visible bypasses
the catalog.
"""

import asyncio

import pytest
from conftest import PASS, network, run
from uidriver import App

pytestmark = pytest.mark.gui

ENV = {"OPENARCANUM_LOCALE": "x-pseudo"}


def P(text: str) -> str:
    return f"⟦{text}⟧"


# Visible text, attributes and options that are not inside ⟦…⟧ once the
# wrapped parts (innermost first, so nested strings work) are removed.
DATA = (
    r"\b[a-z]+://\S+|\bopenarcanum-[a-z]+:|\d{1,2}/\d{1,2}/\d{4},?\s*\d{1,2}:\d{2}(:\d{2})?\s*(AM|PM)?"
    r"|\b(OpenArcanum|Linux|Windows|macOS|general|Admin|First invite)\b|@everyone|\b\d+\.\d+\.\d+\b"
)

AUDIT = (
    r"""
(() => {
  const DATA = new RegExp("""
    + repr(DATA)
    + r""", 'g');
  const strip = (s) => { let prev; do { prev = s; s = s.replace(/⟦[^⟦⟧]*⟧/g, ' '); } while (s !== prev); return s; };
  const data = (s) => s.replace(DATA, ' ');
  const latin = (s) => /[A-Za-z]{2,}/.test(data(strip(s)));
  const vis = (e) => { const r = e.getBoundingClientRect(); const s = getComputedStyle(e);
    return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none'; };
  const exempt = (e) => e.closest('.mono, code, pre, .mtext, .time, .ts, time, script, style');
  const out = new Set();
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    const el = n.parentElement;
    if (!el || !latin(n.textContent.replace(/[⟦⟧]/g, ' ')) || exempt(el) || !vis(el)) continue;
    // Covered when an ancestor's text has it inside ⟦…⟧ (a translated
    // sentence may hold elements, such as a live countdown).
    const own = n.textContent.trim();
    let covered = false;
    for (let a = el, i = 0; a && a !== document.body && i < 4; a = a.parentElement, i++) {
      if (!strip(a.textContent).includes(own)) { covered = true; break; }
    }
    if (!covered) out.add('text: ' + own.slice(0, 120));
  }
  for (const el of document.querySelectorAll('[title], [aria-label], [placeholder], [alt]')) {
    if (exempt(el) || !(vis(el) || el.id === 'rail' || el.id === 'members')) continue;
    for (const a of ['title', 'aria-label', 'placeholder', 'alt']) {
      const v = el.getAttribute(a);
      if (v && latin(v)) out.add(a + ': ' + v.slice(0, 120));
    }
  }
  for (const o of document.querySelectorAll('select:not([data-devices]) option')) {
    if (vis(o.parentElement) && latin(o.textContent)) out.add('option: ' + o.textContent.trim());
  }
  return [...out];
})()
"""
)


async def unwrapped(app: App, where: str) -> list[str]:
    return [f"{where}: {x}" for x in await app.js(AUDIT)]


def test_a_translation_table_changes_the_whole_ui(tmp_path):
    with network(tmp_path) as (_meta, _relay, community):
        node = community()

        async def main():
            app = await App(tmp_path / "a", name="a", env=ENV).start()
            problems: list[str] = []
            try:
                # The first-run screen: index.html's own text and the lock screen.
                await app.wait_text("#lock button", P("Create a profile"))
                assert await app.js("document.querySelector('#rail').getAttribute('aria-label')") == P("Communities")
                problems += await unwrapped(app, "first run")

                await app.button(P("Create a profile"))
                await app.wait_js("document.querySelectorAll('#lock input[type=password]').length === 2", "fields")
                problems += await unwrapped(app, "create profile")
                await app.fill("#lock input[type=password]", PASS, nth=0)
                await app.fill("#lock input[type=password]", PASS, nth=1)
                await app.button(P("Create profile"))
                await app.wait_js("!document.querySelector('#app').classList.contains('hidden')", "main window", 40)
                problems += await unwrapped(app, "empty app")

                # Claim a server and create a community, through the dialogs.
                await app.click(".rail-add")
                await app.click('.add-way[data-tab="server"]')
                problems += await unwrapped(app, "add community page")
                await app.fill(".add-page textarea", node.claim_code)
                await app.button(P("Next"))
                await app.fill(f'.add-page input[placeholder="{P("Community name")}"]', "Ωμέγα")
                await app.fill(f'.add-page input[placeholder="{P("Your display name")}"]', "Ολίβια")
                problems += await unwrapped(app, "claim dialog")
                await app.button(P("Claim and create"))
                await app.wait_js("document.querySelector('.invite-code .code-text')", "claimed dialog", 40)
                problems += await unwrapped(app, "claimed dialog")
                await app.button(P("Export backup…"))
                await app.button(P("Cancel"))
                await app.wait_js("document.querySelector('#composer-input')", "composer", 30)
                await app.wait_gone("#modal-root .backdrop")

                # The community: channel list, messages, composer, members.
                await app.click("#composer-input")
                await app.type("γεια σου")
                await app.key("Enter")
                await app.wait_text("#messages .mtext", "γεια σου")
                assert (await app.js("document.querySelector('#composer-input').placeholder")).startswith("⟦")
                problems += await unwrapped(app, "community")

                # The message menu and a confirm dialog.
                await app.hover("#messages .msg", None, -1)
                problems += await unwrapped(app, "message actions")

                # Settings: every section of the user settings window.
                await app.click(f'.user-panel button[title="{P("Settings")}"]')
                await app.wait_js("document.querySelector('.settings')", "settings")
                for name in await app.texts(".settings nav .item"):
                    assert name.startswith("⟦"), name
                    await app.click(".settings nav .item", name)
                    await app.wait_js("document.querySelector('.settings section')", name)
                    await asyncio.sleep(0.5)  # the section fills in from Python
                    problems += await unwrapped(app, f"settings {name}")
                await app.click(".settings .close")
                await app.wait_gone(".settings")

                # Community Admin.
                await app.click(".side-head")
                problems += await unwrapped(app, "community menu")
                await app.click(".menu .mi", P("Community Admin"))
                await app.wait_js("document.querySelector('.settings')", "community settings")
                for name in await app.texts(".settings nav .item"):
                    if not name.startswith("⟦"):
                        continue  # a channel name in the list
                    await app.click(".settings nav .item", name)
                    await app.wait_js("document.querySelector('.settings section')", name)
                    await asyncio.sleep(0.5)
                    problems += await unwrapped(app, f"community settings {name}")
                await app.click(".settings .close")
                await app.wait_gone(".settings")

                # Direct messages.
                await app.click(f'.rail-item[title="{P("Direct messages")}"]')
                await app.wait_js("document.querySelector('.sidebar')", "direct messages")
                problems += await unwrapped(app, "direct messages")

                # An error raised in Python (vault.py) arrives translated.
                await app.js("lockNow(); 0")
                await app.wait_js("document.querySelector('#lock input[type=password]')", "lock screen", 30)
                problems += await unwrapped(app, "lock screen")
                await app.fill("#lock input[type=password]", "not the passphrase")
                await app.button(P("Unlock"))
                await app.wait_text("#lock", "⟦Wrong passphrase.")
                problems += await unwrapped(app, "wrong passphrase")

                # The audit isn't vacuous: text added without t() is reported.
                await app.js(
                    "document.body.appendChild(Object.assign(document.createElement('div'),"
                    " {textContent: 'Not translated', title: 'Nor this'})); 0"
                )
                probe = await app.js(AUDIT)
                assert "text: Not translated" in probe and "title: Nor this" in probe, probe

                assert problems == [], "\n".join(problems)
                app.assert_no_errors()
            finally:
                await app.stop()

        run(main(), timeout=240)
