"""Translatable text (docs/translations.md).

Every user-visible string in the client goes through `_()` (Python) or
`t()` (the page's `ui/js/i18n.js`) with its English text as the key.
`scripts/strings` extracts them all into one catalog,
`ui/strings/en.json`; a translation is a file beside it,
`ui/strings/<language>.json`, mapping the English text to the translated
text. With no translation for the running locale every lookup returns the
English, unchanged.

The language is chosen once per run: `OPENARCANUM_LOCALE` if set, otherwise
the operating system's language, matched against the translation files
(`pt_BR`, then `pt`). `OPENARCANUM_LOCALE=x-pseudo` is the pseudo-locale used
to prove nothing visible bypasses the catalog: every catalog string comes
back wrapped in ⟦…⟧.

Variables are named, `{name}`, and filled with `str.format` by the caller:

    raise UserError(_("{name} is over the 25 MB limit.").format(name=f.name))
    ngettext("{n} file", "{n} files", n).format(n=n)

`N_()` only marks a string for the catalog (a table built at import time);
the code translates it with `_()` where it shows it.
"""

from __future__ import annotations

import json
import locale
import logging
import os
import re
import sys
from pathlib import Path

from openarcanum.paths import UI_DIR

log = logging.getLogger(__name__)

STRINGS_DIR = UI_DIR / "strings"
CATALOG = STRINGS_DIR / "en.json"
SOURCE_LANGUAGE = "en"
PSEUDO = "x-pseudo"
PSEUDO_OPEN, PSEUDO_CLOSE = "⟦", "⟧"


# Plural rules by name: n -> index into a translation's list of forms. A
# translation file names one in "plural_rule"; i18n.js has the same table.
def _rule_one_other(n: int) -> int:
    return 0 if n == 1 else 1


def _rule_zero_one_other(n: int) -> int:
    return 0 if n in (0, 1) else 1


def _rule_single(n: int) -> int:
    return 0


def _rule_east_slavic(n: int) -> int:
    if n % 10 == 1 and n % 100 != 11:
        return 0
    if 2 <= n % 10 <= 4 and not 12 <= n % 100 <= 14:
        return 1
    return 2


def _rule_polish(n: int) -> int:
    if n == 1:
        return 0
    if 2 <= n % 10 <= 4 and not 12 <= n % 100 <= 14:
        return 1
    return 2


PLURAL_RULES = {
    "one_other": _rule_one_other,  # English, German, Spanish, Italian, Dutch, ...
    "zero_one_other": _rule_zero_one_other,  # French, Brazilian Portuguese
    "single": _rule_single,  # Chinese, Japanese, Korean, Turkish, ...
    "east_slavic": _rule_east_slavic,  # Russian, Ukrainian, Serbian, Croatian
    "polish": _rule_polish,
}

_LOCALE_NAME = re.compile(r"^[A-Za-z]{2,3}([_-][A-Za-z0-9]{2,8})?$")


class Translation:
    """One language's table: English text -> translated text (or, for a
    plural entry, the list of its forms)."""

    def __init__(self, language: str, strings: dict, plural_rule: str = "one_other"):
        self.language = language
        self.strings = strings
        self.plural_rule = plural_rule if plural_rule in PLURAL_RULES else "one_other"

    def gettext(self, text: str) -> str:
        got = self.strings.get(text)
        if isinstance(got, list):
            got = got[0] if got else None
        return got if isinstance(got, str) and got else text

    def ngettext(self, singular: str, plural: str, n: int) -> str:
        got = self.strings.get(singular)
        if isinstance(got, list) and got:
            i = min(PLURAL_RULES[self.plural_rule](abs(int(n))), len(got) - 1)
            if isinstance(got[i], str) and got[i]:
                return got[i]
        return singular if n == 1 else plural

    def for_page(self) -> dict:
        """What the page's i18n.js takes (`setTranslation`)."""
        return {"language": self.language, "plural_rule": self.plural_rule, "strings": self.strings}


ENGLISH = Translation(SOURCE_LANGUAGE, {})


def pseudo_translation(catalog_path: Path = CATALOG) -> Translation:
    """Every catalog string wrapped in ⟦…⟧, variables kept."""
    try:
        catalog = json.loads(catalog_path.read_text(encoding="utf-8"))["strings"]
    except OSError, ValueError, KeyError:
        log.warning("pseudo-locale: the string catalog can't be read")
        return ENGLISH
    table: dict = {}
    for text, entry in catalog.items():
        wrap = f"{PSEUDO_OPEN}{text}{PSEUDO_CLOSE}"
        if entry.get("plural"):
            table[text] = [wrap, f"{PSEUDO_OPEN}{entry['plural']}{PSEUDO_CLOSE}"]
        else:
            table[text] = wrap
    return Translation(PSEUDO, table)


def load_translation(language: str, strings_dir: Path = STRINGS_DIR) -> Translation | None:
    """`<strings_dir>/<language>.json`, or None if there's no such file or it
    isn't a valid translation."""
    if not _LOCALE_NAME.match(language) or language == SOURCE_LANGUAGE:
        return None
    path = strings_dir / f"{language}.json"
    try:
        doc = json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError:
        return None
    except OSError, ValueError:
        log.warning("translation %s can't be read; using English", path.name)
        return None
    strings = doc.get("strings") if isinstance(doc, dict) else None
    if not isinstance(strings, dict):
        log.warning("translation %s has no strings table; using English", path.name)
        return None
    clean = {
        k: v
        for k, v in strings.items()
        if isinstance(k, str) and (isinstance(v, str) or (isinstance(v, list) and all(isinstance(x, str) for x in v)))
    }
    return Translation(language, clean, str(doc.get("plural_rule", "one_other")))


def system_languages(env: dict | None = None) -> list[str]:
    """The operating system's language preferences, most preferred first,
    as `ll_CC` / `ll` names."""
    names: list[str] = []
    for var in ("LC_ALL", "LC_MESSAGES", "LANG", "LANGUAGE"):
        value = (os.environ if env is None else env).get(var, "")
        if value:
            names.extend(value.split(":"))
            break
    if not names and sys.platform in ("win32", "darwin"):
        try:
            got = locale.getlocale()[0]
        except ValueError:
            got = None
        if got:
            names.append(got)
    out: list[str] = []
    for name in names:
        base = name.split(".")[0].split("@")[0].replace("-", "_")
        if base in ("", "C", "POSIX"):
            continue
        for cand in (base, base.split("_")[0]):
            if cand not in out:
                out.append(cand)
    return out


def choose(env: dict | None = None, strings_dir: Path = STRINGS_DIR) -> Translation:
    env = os.environ if env is None else env
    wanted = env.get("OPENARCANUM_LOCALE", "").strip()
    if wanted == PSEUDO:
        return pseudo_translation(strings_dir / "en.json")
    name = wanted.replace("-", "_")
    candidates = [name, name.split("_")[0]] if wanted else system_languages(env)
    for name in candidates:
        found = load_translation(name, strings_dir)
        if found:
            return found
    return ENGLISH


_active: Translation = choose()


def active() -> Translation:
    return _active


def use(translation: Translation) -> None:
    """Switch the running language (tests; the app chooses once at import)."""
    global _active
    _active = translation


def _(text: str) -> str:
    """The running language's text for this English string."""
    return _active.gettext(text)


def ngettext(singular: str, plural: str, n: int) -> str:
    """The form of this English string for `n` in the running language."""
    return _active.ngettext(singular, plural, n)


def N_(text: str) -> str:
    """Mark a string for the catalog without translating it here."""
    return text
