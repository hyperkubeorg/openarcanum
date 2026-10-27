"""UI strings in one place (docs/translations.md).

The translate functions (openarcanum/i18n.py; ui/js/i18n.js has tests in
tests/js/i18n.test.js), and scripts/strings: it extracts every marked string
into the catalog, catches variable mistakes and unwrapped user-visible
literals, checks translation files, and passes on the current tree.
test_ui_strings.py proves a translation table reaches the real UI.
"""

from __future__ import annotations

import importlib.machinery
import importlib.util
import json
import subprocess
import sys
import textwrap
from pathlib import Path

import pytest

from openarcanum import i18n

ROOT = Path(__file__).resolve().parents[3]
SCRIPT = ROOT / "scripts" / "strings"


def _load():
    loader = importlib.machinery.SourceFileLoader("strings_script", str(SCRIPT))
    spec = importlib.util.spec_from_loader("strings_script", loader)
    mod = importlib.util.module_from_spec(spec)
    sys.modules["strings_script"] = mod
    loader.exec_module(mod)
    return mod


ss = _load()


def js(src: str) -> tuple[list[str], list]:
    rep = ss.Report()
    ss.scan_js("ui/js/x.js", textwrap.dedent(src), rep)
    return rep.errors, rep.uses


def py(src: str) -> tuple[list[str], list]:
    rep = ss.Report()
    ss.scan_py("x.py", textwrap.dedent(src), rep)
    return rep.errors, rep.uses


# ---------------------------------------------------------------- openarcanum.i18n


@pytest.fixture
def restore_language():
    before = i18n.active()
    yield
    i18n.use(before)


def test_english_is_the_key_and_the_default(restore_language):
    i18n.use(i18n.ENGLISH)
    assert i18n._("Wrong passphrase.") == "Wrong passphrase."
    assert i18n.ngettext("{n} file", "{n} files", 1) == "{n} file"
    assert i18n.ngettext("{n} file", "{n} files", 0) == "{n} files"
    assert i18n.N_("Later") == "Later"


def test_a_translation_replaces_text_and_plural_forms(restore_language):
    tr = i18n.Translation(
        "ru",
        {"Wrong passphrase.": "Неверная парольная фраза.", "{n} file": ["{n} файл", "{n} файла", "{n} файлов"]},
        "east_slavic",
    )
    i18n.use(tr)
    assert i18n._("Wrong passphrase.") == "Неверная парольная фраза."
    assert i18n._("Not translated yet.") == "Not translated yet."
    forms = {n: i18n.ngettext("{n} file", "{n} files", n).format(n=n) for n in (1, 2, 5, 11, 21, 22, 25)}
    assert forms == {
        1: "1 файл",
        2: "2 файла",
        5: "5 файлов",
        11: "11 файлов",
        21: "21 файл",
        22: "22 файла",
        25: "25 файлов",
    }


@pytest.mark.parametrize(
    ("rule", "cases"),
    [
        ("one_other", {0: 1, 1: 0, 2: 1}),
        ("zero_one_other", {0: 0, 1: 0, 2: 1}),
        ("single", {0: 0, 1: 0, 7: 0}),
        ("east_slavic", {1: 0, 3: 1, 5: 2, 12: 2, 101: 0}),
        ("polish", {1: 0, 2: 1, 5: 2, 21: 2, 22: 1}),
    ],
)
def test_plural_rules(rule, cases):
    assert {n: i18n.PLURAL_RULES[rule](n) for n in cases} == cases


def test_choosing_the_language(tmp_path):
    (tmp_path / "en.json").write_text(json.dumps({"strings": {"Later": {"used_in": ["ui/js/x.js"]}}}))
    (tmp_path / "de.json").write_text(json.dumps({"language": "de", "strings": {"Later": "Später"}}))
    (tmp_path / "xx.json").write_text("{not json")
    choose = lambda env: i18n.choose(env, tmp_path)  # noqa: E731
    assert choose({"OPENARCANUM_LOCALE": "de"}).gettext("Later") == "Später"
    assert choose({"OPENARCANUM_LOCALE": "de-AT"}).language == "de"
    assert choose({"LANG": "de_DE.UTF-8"}).language == "de"
    assert choose({"LC_ALL": "de_CH.UTF-8", "LANG": "fr_FR.UTF-8"}).language == "de"
    assert choose({"LANG": "fr_FR.UTF-8"}) is i18n.ENGLISH
    assert choose({"LANG": "C.UTF-8"}) is i18n.ENGLISH
    assert choose({"OPENARCANUM_LOCALE": "xx"}) is i18n.ENGLISH  # unreadable: English, not a crash
    assert choose({"OPENARCANUM_LOCALE": "../de"}) is i18n.ENGLISH  # not a path
    pseudo = choose({"OPENARCANUM_LOCALE": "x-pseudo"})
    assert pseudo.gettext("Later") == "⟦Later⟧"
    assert pseudo.for_page()["strings"]["Later"] == "⟦Later⟧"


def test_the_pseudo_locale_wraps_every_catalog_string():
    catalog = json.loads(i18n.CATALOG.read_text(encoding="utf-8"))["strings"]
    pseudo = i18n.pseudo_translation()
    for text, entry in catalog.items():
        if entry.get("plural"):
            assert pseudo.ngettext(text, entry["plural"], 2) == f"⟦{entry['plural']}⟧"
        else:
            assert pseudo.gettext(text) == f"⟦{text}⟧"


def test_python_messages_follow_the_language(restore_language):
    from openarcanum import codes

    i18n.use(i18n.Translation("de", {"The invite is incomplete.": "Die Einladung ist unvollständig."}))
    with pytest.raises(codes.CodeError, match=r"Die Einladung ist unvollständig\."):
        codes.parse_invite("openarcanum-invite:!!!")


# ---------------------------------------------------------------- scripts/strings: extraction


def test_extracts_js_calls_with_variables_and_plurals():
    errors, uses = js(
        """
        h('button', { text: t('Save') });
        toast(t('Joined {name}.', { name }));
        el.title = tn('{n} member', '{n} members', count);
        const LABELS = [N_('Off'), N_('On')];
        h('span', null, t(LABELS[0]));
        """
    )
    assert errors == []
    got = {(u.text, u.plural) for u in uses}
    assert got == {("Save", None), ("Joined {name}.", None), ("{n} member", "{n} members"), ("Off", None), ("On", None)}


def test_extracts_python_calls():
    errors, uses = py(
        """
        from openarcanum.i18n import N_, _, ngettext
        LABEL = N_("Empty")
        def f(name, n):
            raise UserError(_("Couldn't save {name}.").format(name=name))
        def g(n):
            return ngettext("{n} file", "{n} files", n).format(n=n)
        """
    )
    assert errors == []
    assert {(u.text, u.plural) for u in uses} == {
        ("Empty", None),
        ("Couldn't save {name}.", None),
        ("{n} file", "{n} files"),
    }


# ---------------------------------------------------------------- scripts/strings: mistakes


@pytest.mark.parametrize(
    ("src", "why"),
    [
        ("toast(t('Joined {name}.'));", "no values"),
        ("toast(t('Joined {name}.', { who }));", "variables don't match"),
        ("toast(t(`Joined ${name}.`));", "template literal"),
        ("toast(t('Joined ' + name));", "built with +"),
        ("x.title = tn('{n} item', '{n} things {k}', n);", "different variables"),
        ("x.title = tn('{n} item', n);", "tn() needs"),
    ],
)
def test_js_variable_mistakes(src, why):
    errors, _uses = js(src)
    assert any(why in e for e in errors), errors


@pytest.mark.parametrize(
    ("src", "why"),
    [
        ('raise UserError(_(f"Bad {x}"))', "f-string"),
        ('raise UserError(_("Bad {x}"))', "no .format"),
        ('raise UserError(_("Bad {x}").format(y=1))', "variables don't match"),
        ('raise UserError(_("Bad {}").format(1))', "named variables"),
        ('raise UserError(_("Bad {x}").format(1))', "keyword arguments"),
        ('n = ngettext("{n} file", "{n} files {k}", 2).format(n=2, k=1)', "different variables"),
    ],
)
def test_python_variable_mistakes(src, why):
    errors, _uses = py("from openarcanum.i18n import _, ngettext\n" + src)
    assert any(why in e for e in errors), errors


def test_plain_and_plural_use_of_one_text_conflict():
    rep = ss.Report()
    ss.scan_js("ui/js/x.js", "a.title = t('{n} file', { n }); b.title = tn('{n} file', '{n} files', n);", rep)
    ss.build_catalog(rep)
    assert any("both as a plain string" in e for e in rep.errors), rep.errors


# ---------------------------------------------------------------- scripts/strings: the lint


@pytest.mark.parametrize(
    "src",
    [
        "h('button', { class: 'btn', text: 'Save' });",
        "h('button', { title: 'Close' });",
        "h('input', { placeholder: 'Search' });",
        "h('div', { 'aria-label': 'Members' });",
        "h('p', null, 'or');",
        "el.textContent = 'Copied';",
        "el.setAttribute('aria-label', 'Spoiler');",
        "toast('Saved');",
        'throw new Error("You can\'t share your screen in this call.");',
        "const msg = 'Something went wrong, try again.';",
        "return 'Online';",
        "openMenu(x, y, [{ label: 'Delete', run }]);",
        "el.title = `${n} mention${n === 1 ? '' : 's'}`;",
        "label = who.replace(/^me$/, 'joined the community');",
        "h('button', { title: 'Delete' });",
    ],
)
def test_js_lint_catches_unwrapped_text(src):
    errors, _uses = js(src)
    assert any("unwrapped user-visible text" in e for e in errors), (src, errors)


@pytest.mark.parametrize(
    "src",
    [
        "h('div', { class: 'modal wide', role: 'dialog', id: 'x' });",
        "api('community_state', cid);",
        "on('message', (m) => render(m));",
        "if (e.key === 'Enter' || e.key === 'Escape') close();",
        "switch (s) { case 'Online': break; }",
        "$('#lock input[type=password]');",
        "el.classList.add('has-unread');",
        "console.error('event handler failed', e);",
        "h('span', { text: '✕' });",
        "h('span', { text: t('Save') });",
        "x = enc.encode('SFrame 1.0 Secret key '); // i18n: not shown (RFC 9605 label)",
    ],
)
def test_js_lint_leaves_non_text_alone(src):
    errors, _uses = js(src)
    assert errors == [], (src, errors)


def test_js_lint_looks_inside_event_handlers():
    errors, _uses = js("on('toast', (m) => { toast('Saved'); });")
    assert any("'Saved'" in e for e in errors), errors


def test_shadowing_the_translate_functions_is_refused():
    errors, _uses = js("on('toast', (t) => toast(t.text));")
    assert any("shadows the translate function" in e for e in errors), errors
    errors, _uses = py("from openarcanum.i18n import _\nfor _ in range(3):\n    print(_('x'))\n")
    assert any("binds `_`" in e for e in errors), errors


def test_an_opt_out_needs_a_reason():
    errors, _uses = js("toast('Saved'); // i18n: not shown ()")
    assert errors


@pytest.mark.parametrize(
    "src",
    [
        'raise UserError("Wrong passphrase.")',
        'raise UserError(f"{name} is over the 25 MB limit.")',
        'raise CodeError("The code is damaged or incomplete.")',
        'self.core.emit("toast", {"text": "Locked after being idle."})',
        'return {"ok": False, "error": "Unknown choice."}',
    ],
)
def test_python_lint_catches_unwrapped_text(src):
    errors, _uses = py(src)
    assert any("unwrapped user-visible text" in e for e in errors), (src, errors)


@pytest.mark.parametrize(
    "src",
    [
        'log.warning("couldn\'t reach %s", host)',
        "raise UserError(str(e)) from None",
        'raise ValueError("bad nonce length")',
        'raise UserError(_("Wrong passphrase."))',
        'return {"state": "locked", "error": None}',
    ],
)
def test_python_lint_leaves_non_text_alone(src):
    errors, _uses = py("from openarcanum.i18n import _\n" + src)
    assert errors == [], (src, errors)


def test_html_lint():
    rep = ss.Report()
    ss.scan_html(
        "ui/index.html", '<body>\n<div class="muted">Starting…</div>\n<nav aria-label="Communities"></nav>\n', rep
    )
    assert len(rep.errors) == 2, rep.errors
    rep = ss.Report()
    ss.scan_html(
        "ui/index.html",
        '<body>\n<div data-i18n="Starting…">Starting…</div>\n'
        '<nav aria-label="Communities" data-i18n="Communities" data-i18n-attr="aria-label"></nav>\n',
        rep,
    )
    assert rep.errors == []
    assert {u.text for u in rep.uses} == {"Starting…", "Communities"}


# ---------------------------------------------------------------- translation files


def test_translation_files_are_checked(tmp_path, monkeypatch):
    monkeypatch.setattr(ss, "STRINGS", tmp_path)
    catalog = {
        "strings": {
            "Joined {name}.": {"vars": ["name"], "used_in": ["ui/js/x.js"]},
            "{n} file": {"plural": "{n} files", "vars": ["n"], "used_in": ["x.py"]},
            "Later": {"used_in": ["ui/js/x.js"]},
        }
    }
    (tmp_path / "de.json").write_text(
        json.dumps(
            {
                "plural_rule": "one_other",
                "strings": {
                    "Joined {name}.": "{name} beigetreten.",
                    "{n} file": ["eine Datei", "{n} Dateien"],
                    "Later": "",
                    "Gone": "Weg",
                },
            }
        )
    )
    rep = ss.Report()
    stale = ss.check_translations(catalog, rep)
    assert rep.errors == []
    assert len(stale) == 1 and "Gone" in stale[0]

    (tmp_path / "fr.json").write_text(
        json.dumps(
            {
                "plural_rule": "nope",
                "strings": {"Joined {name}.": "Rejoint {nom}.", "{n} file": "fichier", "Later": ["Plus tard"]},
            }
        )
    )
    rep = ss.Report()
    ss.check_translations(catalog, rep)
    text = "\n".join(rep.errors)
    assert "unknown plural_rule" in text
    assert "variables ['nom'] don't match ['name']" in text
    assert "is a plural" in text
    assert "is not a plural" in text


def test_new_translation_lists_every_string(tmp_path, monkeypatch):
    monkeypatch.setattr(ss, "STRINGS", tmp_path)
    catalog = {"strings": {"Later": {}, "{n} file": {"plural": "{n} files"}}}
    path = ss.new_translation("de", catalog)
    doc = json.loads(path.read_text(encoding="utf-8"))
    assert doc["strings"] == {"Later": "", "{n} file": ["", ""]}
    doc["strings"]["Later"] = "Später"
    path.write_text(json.dumps(doc), encoding="utf-8")
    catalog["strings"]["Cancel"] = {}
    doc = json.loads(ss.new_translation("de", catalog).read_text(encoding="utf-8"))
    assert doc["strings"] == {"Cancel": "", "Later": "Später", "{n} file": ["", ""]}


# ---------------------------------------------------------------- the tree


def test_the_catalog_is_current_and_the_tree_is_clean():
    out = subprocess.run([sys.executable, str(SCRIPT), "--check"], capture_output=True, text=True)
    assert out.returncode == 0, out.stderr[-4000:]


def test_every_ui_script_loads_after_i18n():
    html = (ROOT / "clients" / "shared" / "ui" / "index.html").read_text(encoding="utf-8")
    scripts = [line.split('"')[1] for line in html.splitlines() if line.startswith("<script src=")]
    assert scripts[0] == "js/i18n.js"
    assert set(scripts) == {f"js/{p.name}" for p in (ROOT / "clients" / "shared" / "ui" / "js").glob("*.js")}
