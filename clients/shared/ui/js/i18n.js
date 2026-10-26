// Translatable text (docs/translations.md). Every user-visible string in the
// page goes through t() or tn() with its English text as the key;
// scripts/strings extracts them into the one catalog, ui/strings/en.json.
// Python picks the language and hands its table over at startup
// (setTranslation, from app.js boot); with no table every lookup returns the
// English. Loaded before every other script.
'use strict';

const I18N = {
  language: 'en',
  rule: 'one_other',
  strings: {},
};

// Plural rules by name: n -> index into a translation's list of forms.
// openarcanum/i18n.py has the same table.
const PLURAL_RULES = {
  one_other: (n) => (n === 1 ? 0 : 1),
  zero_one_other: (n) => (n === 0 || n === 1 ? 0 : 1),
  single: () => 0,
  east_slavic: (n) => (n % 10 === 1 && n % 100 !== 11 ? 0
    : n % 10 >= 2 && n % 10 <= 4 && !(n % 100 >= 12 && n % 100 <= 14) ? 1 : 2),
  polish: (n) => (n === 1 ? 0
    : n % 10 >= 2 && n % 10 <= 4 && !(n % 100 >= 12 && n % 100 <= 14) ? 1 : 2),
};

function setTranslation(tr) {
  if (!tr || typeof tr.strings !== 'object' || tr.strings === null) return;
  I18N.language = String(tr.language || 'en');
  I18N.rule = PLURAL_RULES[tr.plural_rule] ? tr.plural_rule : 'one_other';
  I18N.strings = tr.strings;
  if (typeof document !== 'undefined') document.documentElement.lang = I18N.language;
}

// Fill {name} variables from vars; a name vars doesn't have stays as it is.
function fillVars(text, vars) {
  if (!vars) return text;
  return text.replace(/\{([A-Za-z_][A-Za-z0-9_]*)\}/g, (m, k) => (
    Object.prototype.hasOwnProperty.call(vars, k) && vars[k] !== undefined && vars[k] !== null ? String(vars[k]) : m));
}

// The running language's text for an English string, variables filled.
function t(text, vars) {
  const got = Object.prototype.hasOwnProperty.call(I18N.strings, text) ? I18N.strings[text] : null;
  const s = typeof got === 'string' && got ? got : Array.isArray(got) && got[0] ? got[0] : text;
  return fillVars(s, vars);
}

// The form for n: tn('{n} file', '{n} files', n). {n} is filled from n.
function tn(singular, plural, n, vars) {
  const got = Object.prototype.hasOwnProperty.call(I18N.strings, singular) ? I18N.strings[singular] : null;
  let s = n === 1 ? singular : plural;
  if (Array.isArray(got) && got.length) {
    const i = Math.min(PLURAL_RULES[I18N.rule](Math.abs(Math.trunc(n))), got.length - 1);
    if (typeof got[i] === 'string' && got[i]) s = got[i];
  }
  return fillVars(s, Object.assign({ n }, vars || {}));
}

// Marks a string for the catalog without translating it (tables built when
// the script loads, before the language is known); the code that shows it
// calls t() on it.
function N_(text) { return text; }

// Text in index.html: data-i18n="English" sets the element's text,
// data-i18n-attr="aria-label" translates that attribute instead.
function translatePage(root = document) {
  for (const el of root.querySelectorAll('[data-i18n]')) {
    const attr = el.getAttribute('data-i18n-attr');
    const text = el.getAttribute('data-i18n');
    if (attr) el.setAttribute(attr, t(text)); else el.textContent = t(text);
  }
}

if (typeof module !== 'undefined') module.exports = { t, tn, N_, setTranslation, I18N, PLURAL_RULES, fillVars };
