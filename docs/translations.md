# Translations

Every user-visible string in the OpenArcanum client is kept in one place, the catalog
`clients/shared/ui/strings/en.json`, so the app can be translated without touching the code.
The app ships in English only; this page is for developers and translators.

## How strings are marked

The English text is the key (gettext style). Code never shows a literal; it passes the English
through a translate function:

| Where | Plain | With values | Plural | Table built at load time |
|---|---|---|---|---|
| Page (`ui/js/*.js`) | `t('Save')` | `t('Delete {name}?', { name })` | `tn('{n} member', '{n} members', n)` | `N_('Off')`, shown with `t(label)` |
| `ui/index.html` | `data-i18n="Starting…"` (text) | | | |
| | `data-i18n="Members" data-i18n-attr="aria-label"` (attribute) | | | |
| Python (`openarcanum/*.py`) | `_("Wrong passphrase.")` | `_("{name} is over the 25 MB limit.").format(name=n)` | `ngettext("{n} file", "{n} files", n).format(n=n)` | `N_("Empty")`, shown with `_(label)` |

Rules (`scripts/strings --check` enforces the first four):

- Variables are named, `{name}`, and the values passed are exactly the names the text uses. No
  template literals with `${…}` or f-strings inside a translate call, no positional `{}`.
- A whole sentence is one string. Don't build text from pieces with `+`: word order differs between
  languages.
- In JavaScript, `t`, `tn` and `N_` are never used as variable names; in a Python module that uses
  `_()`, `_` is never a throwaway variable.
- A literal that looks like text but isn't shown carries a comment `i18n: not shown (<why>)` on its
  line.
- Not marked: log messages (never user-visible), names and messages people type, the product name,
  codes and keys shown for copying, and text that comes from a server.

## The catalog

`scripts/strings` (or `make strings`) extracts every marked string from `ui/js/*.js`,
`ui/index.html` and the Python package and rewrites `en.json`:

```json
{
 "about": "…",
 "strings": {
  "Delete {name}?": {"vars": ["name"], "used_in": ["ui/js/chat.js"]},
  "{n} member": {"plural": "{n} members", "vars": ["n"], "used_in": ["ui/js/app.js"]},
  "Wrong passphrase.": {"used_in": ["vault.py"]}
 }
}
```

Entries are sorted by their English text. `used_in` lists the files that use the string: page
files relative to `clients/shared/`, Python files relative to `clients/desktop/src/openarcanum/`. The catalog is generated: never edit it by hand, and
commit it with the code change that changed a string.

`make check` runs `scripts/strings --check` (`make strings-check`). It fails when:

- the catalog is out of date;
- a call's variables don't match its text, or a plural's two forms use different variables;
- the same English text is used both as a plain string and as a plural;
- a likely user-visible literal isn't wrapped. In JavaScript this means literals given to `text:`,
  `title:`, `placeholder:`, `label:`, `aria-label`, `.textContent`, `toast()`, `confirmModal()`,
  `h()` children and the like, plus any prose-like literal elsewhere. In Python it means the
  messages of exceptions the UI shows, and the `"text"`, `"title"`, `"body"`, `"error"`… values of
  dicts sent to the page. The full rules are in the script's docstring;
- a translation file is malformed or drops or adds a variable. Keys a translation still has
  but the catalog no longer has are listed as stale warnings and don't fail the check.

## Adding a language

1. `scripts/strings --new de` creates `clients/shared/ui/strings/de.json` with every catalog
   string and an empty translation. Run it again later to add strings that are new since then;
   existing translations are kept.
2. Fill in the translations. Keep every `{variable}` exactly as it is (its position may change).
   An empty string means "not translated yet": the English is shown.

   ```json
   {
    "language": "de",
    "plural_rule": "one_other",
    "strings": {
     "Delete {name}?": "{name} löschen?",
     "{n} member": ["{n} Mitglied", "{n} Mitglieder"],
     "Wrong passphrase.": "Falsche Passphrase."
    }
   }
   ```

3. Plurals are a list of forms, ordered by the language's `plural_rule`:

   | `plural_rule` | Forms | Languages |
   |---|---|---|
   | `one_other` | n = 1; everything else | English, German, Dutch, Spanish, Italian, Swedish… |
   | `zero_one_other` | n = 0 or 1; everything else | French, Brazilian Portuguese |
   | `single` | one form | Chinese, Japanese, Korean, Turkish… |
   | `east_slavic` | 1, 21, 31…; 2-4, 22-24…; everything else | Russian, Ukrainian, Serbian, Croatian |
   | `polish` | 1; 2-4, 22-24…; everything else | Polish |

   A language with other rules needs a new entry in `PLURAL_RULES` in both
   `clients/desktop/src/openarcanum/i18n.py` and `clients/shared/ui/js/i18n.js`, plus the rule's name in
   `scripts/strings`.
4. Run `make strings-check`, then start the app with `OPENARCANUM_LOCALE=de` to see it.

## Which language the app uses

The language is chosen once at startup, in `openarcanum/i18n.py`: `OPENARCANUM_LOCALE` if it is set,
otherwise the operating system's language (`LC_ALL`, `LC_MESSAGES`, `LANG`, `LANGUAGE`; the
system locale on Windows and macOS), matched against the files in `ui/strings/` (`pt_BR`, then
`pt`). With no match the app is in English. Python translates its own messages (errors, toasts,
notifications) and gives the page its table through `api.ui_strings` before anything is drawn;
`i18n.js` then translates the page. There is no language setting in the app: it would be a dead
toggle while only English ships.

## The pseudo-locale

`OPENARCANUM_LOCALE=x-pseudo` builds a table from the catalog that wraps every string in ⟦…⟧.
Anything on screen without the brackets bypasses the catalog. `clients/desktop/tests/test_ui_strings.py`
clicks through the main screens under it (first run, the community, every settings section,
community settings, direct messages, a Python error on the lock screen). It fails on any visible
text, title, aria-label, placeholder or list option outside the brackets, apart from the test's
own data, codes and times of day.
