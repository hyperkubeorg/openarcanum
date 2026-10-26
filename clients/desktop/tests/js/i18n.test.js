// Unit tests for the page's translate functions (clients/shared/ui/js/i18n.js).
// Run with `make test-js`. test_ui_strings.py checks the same table reaches
// the real window.
'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const path = require('node:path');

const i18n = require(path.join(__dirname, '..', '..', '..', 'shared', 'ui', 'js', 'i18n.js'));
const { t, tn, N_, setTranslation } = i18n;

function english() { setTranslation({ language: 'en', strings: {} }); }

test('English: the key is the text, placeholders filled', () => {
  english();
  assert.equal(t('Save'), 'Save');
  assert.equal(t('Delete {name}?', { name: 'general' }), 'Delete general?');
  assert.equal(t('{a} and {b}', { a: 1 }), '1 and {b}');
  assert.equal(t('Costs {n} {n}', { n: 0 }), 'Costs 0 0');
  assert.equal(t('A {name}', { name: '{evil}' }), 'A {evil}');
  assert.equal(tn('{n} member', '{n} members', 1), '1 member');
  assert.equal(tn('{n} member', '{n} members', 3), '3 members');
  assert.equal(tn('{n} file in {room}', '{n} files in {room}', 2, { room: 'x' }), '2 files in x');
  assert.equal(N_('Later'), 'Later');
});

test('a translation replaces text and picks plural forms by its rule', () => {
  setTranslation({
    language: 'pl',
    plural_rule: 'polish',
    strings: { Save: 'Zapisz', '{n} file': ['{n} plik', '{n} pliki', '{n} plików'], 'Hi {name}': '' },
  });
  assert.equal(t('Save'), 'Zapisz');
  assert.equal(t('Not translated'), 'Not translated');
  assert.equal(t('Hi {name}', { name: 'Ola' }), 'Hi Ola'); // empty: English
  assert.deepEqual([1, 2, 5, 22, 25].map((n) => tn('{n} file', '{n} files', n)),
    ['1 plik', '2 pliki', '5 plików', '22 pliki', '25 plików']);
  english();
});

test('plural rules match openarcanum/i18n.py', () => {
  const R = i18n.PLURAL_RULES;
  assert.deepEqual([0, 1, 2].map(R.one_other), [1, 0, 1]);
  assert.deepEqual([0, 1, 2].map(R.zero_one_other), [0, 0, 1]);
  assert.deepEqual([0, 1, 7].map(R.single), [0, 0, 0]);
  assert.deepEqual([1, 3, 5, 12, 101].map(R.east_slavic), [0, 1, 2, 2, 0]);
  assert.deepEqual([1, 2, 5, 21, 22].map(R.polish), [0, 1, 2, 2, 1]);
});

test('a bad table is ignored, an unknown rule falls back', () => {
  english();
  setTranslation(null);
  setTranslation({ strings: null });
  assert.equal(t('Save'), 'Save');
  setTranslation({ plural_rule: 'nope', strings: { '{n} file': ['{n} Datei', '{n} Dateien'] } });
  assert.equal(tn('{n} file', '{n} files', 2), '2 Dateien');
  english();
});
