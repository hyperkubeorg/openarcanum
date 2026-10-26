"""The interface's icons (ui/js/icons.js, vendored Lucide; icon() in dom.js):
the vendored files match their checksums and the generated table, every icon
the page names is vendored, and no emoji or symbol character stands in for an
icon. Emoji people type (messages, reactions) are data, not UI."""

import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
UI = ROOT / "clients" / "shared" / "ui"

# Pictographs and the symbol blocks that get used as makeshift icons (arrows,
# maths operators, technical, enclosed, geometric shapes, dingbats, misc symbols).
# The one exception is the rightwards arrow, which prose uses for a path
# through menus ("Settings, arrow, Network").
RANGES = [(0x2190, 0x2191), (0x2193, 0x21FF), (0x2200, 0x23FF), (0x2460, 0x27BF), (0x2900, 0x2BFF), (0x1F000, 0x1FAFF)]
SYMBOL = re.compile("[" + "".join(f"{chr(a)}-{chr(b)}" for a, b in RANGES) + "]")
# String literals in the page's code that may hold one: the quick reactions
# (emoji people send) and the stand-in for a spoiler in plain-text previews.
ALLOWED = {
    ("dm.js", "const QUICK_EMOJI = ['👍', '❤️', '😂', '🎉', '😮', '😢'];"),
    # Emoji a user can put in their status: content, like reactions.
    ("status.js", "const STATUS_EMOJI = ['💬', '🎧', '📅', '🏠', '🚗', '🍽️', '🤒', '🌴', '🎮', '💤'];"),
    ("markdown.js", "else if (n.t === 'spoiler') out.push('▒▒▒');"),
}
LITERAL = re.compile(r"""'(?:[^'\\\n]|\\.)*'|"(?:[^"\\\n]|\\.)*"|`(?:[^`\\]|\\.)*`""")


def test_vendored_icons_and_generated_table_are_current():
    r = subprocess.run(
        [sys.executable, "-I", str(ROOT / "scripts" / "ui-icons"), "--check"], capture_output=True, text=True
    )
    assert r.returncode == 0, r.stderr


def test_licence_ships_with_the_icons():
    lic = (UI / "vendor" / "lucide" / "LICENSE").read_text()
    assert lic.startswith("ISC License") and "Lucide Icons and Contributors" in lic
    assert "Lucide" in (UI / "vendor" / "lucide" / "VENDORED.md").read_text()


def test_no_symbol_characters_as_icons():
    found = []
    for p in sorted((UI / "js").glob("*.js")):
        for n, line in enumerate(p.read_text(encoding="utf-8").splitlines(), 1):
            code = line.split("//", 1)[0] if not line.lstrip().startswith("//") else ""
            if any(SYMBOL.search(lit) for lit in LITERAL.findall(code)) and (p.name, line.strip()) not in ALLOWED:
                found.append(f"{p.name}:{n}: {line.strip()}")
    html = (UI / "index.html").read_text(encoding="utf-8")
    found += [f"index.html: {m}" for m in SYMBOL.findall(html)]
    assert not found, "use icon() (ui/js/dom.js) instead:\n" + "\n".join(found)
