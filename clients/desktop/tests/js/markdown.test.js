// Unit tests for the message formatter (clients/shared/ui/js/markdown.js).
// Run with `make test-js` (node --test; no npm dependencies). The DOM is a
// tiny fake that records what the renderer builds, so these tests also prove
// it only uses createElement/createTextNode/textContent.
'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const UI = path.join(__dirname, '..', '..', '..', 'shared', 'ui', 'js');
const md = require(path.join(UI, 'markdown.js'));

// ---------------------------------------------------------------- fake DOM

class FakeText {
  constructor(text) { this.nodeType = 3; this.text = String(text); }
  get textContent() { return this.text; }
}

class FakeElement {
  constructor(tag) {
    this.nodeType = 1;
    this.tagName = tag.toUpperCase();
    this.children = [];
    this.attrs = {};
    this.listeners = {};
    this.className = '';
    this.style = { setProperty(k, v) { this[k] = v; } };
    const self = this;
    this.classList = {
      contains: (c) => self.className.split(/\s+/).includes(c),
      add: (c) => { if (!self.classList.contains(c)) self.className = (self.className + ' ' + c).trim(); },
    };
  }
  appendChild(c) { this.children.push(c); return c; }
  set textContent(v) { this.children = [new FakeText(v)]; }
  get textContent() { return this.children.map((c) => c.textContent).join(''); }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  removeAttribute(k) { delete this.attrs[k]; }
  addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); }
  fire(type, ev = {}) { for (const fn of this.listeners[type] || []) fn(ev); }
  set innerHTML(_v) { throw new Error('innerHTML used'); }
  find(pred) {
    if (pred(this)) return this;
    for (const c of this.children) {
      if (c instanceof FakeElement) { const f = c.find(pred); if (f) return f; }
    }
    return null;
  }
  all(pred, out = []) {
    if (pred(this)) out.push(this);
    for (const c of this.children) if (c instanceof FakeElement) c.all(pred, out);
    return out;
  }
}

const doc = {
  createElement: (tag) => new FakeElement(tag),
  createTextNode: (t) => new FakeText(t),
};

const render = (text, ctx = {}) => md.renderText(text, ctx, doc);
const byTag = (root, tag) => root.all((e) => e.tagName === tag.toUpperCase());
const byClass = (root, cls) => root.all((e) => e.classList.contains(cls));

// ---------------------------------------------------------------- inline

test('bold, italic, strike and inline code', () => {
  const r = render('**bold** *it* _it2_ ~~gone~~ `x * y` __also bold__');
  assert.deepEqual(byTag(r, 'strong').map((e) => e.textContent), ['bold', 'also bold']);
  assert.deepEqual(byTag(r, 'em').map((e) => e.textContent), ['it', 'it2']);
  assert.deepEqual(byTag(r, 'del').map((e) => e.textContent), ['gone']);
  assert.deepEqual(byTag(r, 'code').map((e) => e.textContent), ['x * y']);
  assert.equal(r.textContent, 'bold it it2 gone x * y also bold');
});

test('nesting and unmatched markers', () => {
  const r = render('**bold *and italic***');
  assert.equal(byTag(r, 'strong').length, 1);
  assert.equal(r.textContent.replace(/\*/g, ''), 'bold and italic');
  assert.equal(render('2 * 3 * 4').textContent, '2 * 3 * 4');
  assert.equal(byTag(render('2 * 3 * 4'), 'em').length, 0);
  assert.equal(render('snake_case_name').textContent, 'snake_case_name');
  assert.equal(byTag(render('snake_case_name'), 'em').length, 0);
  assert.equal(render('** not bold **').textContent, '** not bold **');
  assert.equal(render('a `b').textContent, 'a `b');
});

test('double-backtick code can hold a backtick', () => {
  const r = render('``a ` b``');
  assert.deepEqual(byTag(r, 'code').map((e) => e.textContent), ['a ` b']);
});

test('backslash escapes', () => {
  const r = render('\\*not italic\\* \\`x\\` \\||no\\||');
  assert.equal(byTag(r, 'em').length, 0);
  assert.equal(byClass(r, 'spoiler').length, 0);
  assert.equal(r.textContent, '*not italic* `x` ||no||');
});

test('markup in messages stays text', () => {
  const evil = '<img src=x onerror=alert(1)><script>alert(2)</script>&amp;';
  const r = render(evil);
  assert.equal(r.textContent, evil);
  assert.equal(r.all((e) => ['IMG', 'SCRIPT'].includes(e.tagName)).length, 0);
});

// ---------------------------------------------------------------- spoilers

test('spoilers are hidden until clicked', () => {
  const r = render('the butler ||did it|| ok');
  const sp = byClass(r, 'spoiler')[0];
  assert.ok(sp);
  assert.equal(sp.textContent, 'did it');
  assert.ok(!sp.classList.contains('revealed'));
  assert.equal(sp.attrs.role, 'button');
  assert.equal(sp.attrs.tabindex, '0');
  sp.fire('click');
  assert.ok(sp.classList.contains('revealed'));
  const kb = byClass(render('||key||'), 'spoiler')[0];
  kb.fire('keydown', { key: 'Enter' });
  assert.ok(kb.classList.contains('revealed'));
  // Formatting inside a spoiler.
  assert.equal(byTag(render('||**x**||'), 'strong').length, 1);
});

// ---------------------------------------------------------------- links

test('links show text and URL and never load', () => {
  const opened = [];
  const r = render('see [the docs](https://docs.example.invalid/a) and https://b.example.invalid/x.', { onLink: (u) => opened.push(u) });
  const links = byClass(r, 'link');
  assert.deepEqual(links.map((e) => e.attrs['data-url']), ['https://docs.example.invalid/a', 'https://b.example.invalid/x']);
  assert.equal(links[0].textContent, 'the docs');
  assert.equal(byClass(r, 'link-url')[0].textContent, ' (https://docs.example.invalid/a)');
  assert.ok(r.textContent.endsWith('https://b.example.invalid/x.'));
  // Nothing becomes an <a>, an <img> or anything with a src/href.
  assert.equal(r.all((e) => e.tagName === 'A' || e.tagName === 'IMG' || e.attrs.href || e.attrs.src).length, 0);
  links[1].fire('click');
  assert.deepEqual(opened, ['https://b.example.invalid/x']);
  // javascript: and other schemes are plain text.
  assert.equal(byClass(render('[x](javascript:alert(1)) javascript:alert(1)'), 'link').length, 0);
  // <url> suppresses previews but is still shown as a link.
  assert.equal(byClass(render('<https://c.example.invalid/>'), 'link')[0].attrs['data-url'], 'https://c.example.invalid/');
});

// ---------------------------------------------------------------- mentions

test('mentions', () => {
  const key = 'A'.repeat(43);
  const ctx = {
    me: key,
    memberName: (k) => (k === key ? 'Ally' : ''),
    roleName: (id) => (id === '4' ? 'Mods' : ''),
    roleColor: (id) => (id === '4' ? '#ff0000' : ''),
  };
  const r = render(`hi <@${key}> and <@${'B'.repeat(43)}> <@&4> <@&9> @everyone @here mail@example.invalid`, ctx);
  const m = byClass(r, 'mention');
  assert.deepEqual(m.map((e) => e.textContent), ['@Ally', '@unknown member', '@Mods', '@deleted role', '@everyone', '@here']);
  assert.ok(m[0].classList.contains('me') && !m[1].classList.contains('me'));
  // The role's color, which styles.css keeps legible in each theme.
  assert.equal(m[2].style['--role-color'], '#ff0000');
  // Plain @name only where the context asks (DMs).
  assert.equal(byClass(render('@olive hi'), 'mention').length, 0);
  assert.deepEqual(byClass(render('@olive hi', { plainMentions: true }), 'mention').map((e) => e.textContent), ['@olive']);
  // Not inside code.
  assert.equal(byClass(render('`@everyone`'), 'mention').length, 0);
});

test('plain text runs go through ctx.text (custom emoji), code never does', () => {
  const seen = [];
  const ctx = {
    text: (parent, v) => {
      seen.push(v);
      for (const part of v.split(/(:[a-z_]+:\d+)/)) {
        if (/^:[a-z_]+:\d+$/.test(part)) {
          const img = doc.createElement('img');
          img.setAttribute('alt', part);
          parent.appendChild(img);
        } else if (part) parent.appendChild(doc.createTextNode(part));
      }
    },
  };
  const r = render('hi :party:7 **:party:7** `:party:7`\n```\n:party:7\n```', ctx);
  assert.equal(byTag(r, 'img').length, 2);
  assert.ok(seen.every((v) => !v.includes('`')));
  assert.equal(byTag(r, 'code').map((e) => e.textContent).join('|'), ':party:7|:party:7');
  // Without the hook, plain text.
  assert.equal(byTag(render('hi :party:7'), 'img').length, 0);
});

// ---------------------------------------------------------------- blocks

test('code blocks with a language label', () => {
  const r = render('before\n```python\nprint("**not bold**")\n<b>x</b>\n```\nafter');
  const box = byClass(r, 'codeblock')[0];
  assert.equal(byClass(box, 'code-lang')[0].textContent, 'python');
  assert.equal(byTag(box, 'code')[0].textContent, 'print("**not bold**")\n<b>x</b>');
  assert.equal(byTag(r, 'strong').length, 0);
  assert.deepEqual(byClass(r, 'md-p').map((e) => e.textContent), ['before', 'after']);
  // Unclosed fences run to the end; no language means no label.
  const open = render('```\nline 1\nline 2');
  assert.equal(byTag(open, 'code')[0].textContent, 'line 1\nline 2');
  assert.equal(byClass(open, 'code-lang').length, 0);
});

test('quotes, lists and headings', () => {
  const r = render('> quoted **text**\n> more\n- one\n- two\n3. three\n4. four\n# Title\n## Sub\nplain');
  const q = byTag(r, 'blockquote')[0];
  assert.equal(q.textContent, 'quoted text\nmore');
  assert.equal(byTag(q, 'strong').length, 1);
  assert.deepEqual(byTag(byTag(r, 'ul')[0], 'li').map((e) => e.textContent), ['one', 'two']);
  const ol = byTag(r, 'ol')[0];
  assert.equal(ol.attrs.start, '3');
  assert.deepEqual(byTag(ol, 'li').map((e) => e.textContent), ['three', 'four']);
  assert.deepEqual(byClass(r, 'md-h').map((e) => [e.className, e.textContent]), [['md-h md-h1', 'Title'], ['md-h md-h2', 'Sub']]);
  assert.equal(byClass(r, 'md-p').pop().textContent, 'plain');
  // A line of text keeps its line breaks.
  assert.equal(render('a\nb\n\nc').textContent, 'a\nb\n\nc');
  // Four #s isn't a heading.
  assert.equal(byClass(render('#### no'), 'md-h').length, 0);
});

test('plain text for previews', () => {
  assert.equal(md.plainText('**hi** ||secret|| [docs](https://x.invalid/) `c`\n> q\n- a\n- b'), 'hi ▒▒▒ docs c q a b');
});

// ---------------------------------------------------------------- robustness

test('random and pathological input never throws and stays fast', () => {
  const alphabet = ['*', '_', '~', '`', '|', '>', '#', '-', '[', ']', '(', ')', '<', '@', '&', '\\', '\n', ' ', 'a', 'https://x.invalid/', '1. '];
  let seed = 42;
  const rand = (n) => { seed = (seed * 1103515245 + 12345) & 0x7fffffff; return seed % n; };
  for (let i = 0; i < 3000; i++) {
    let s = '';
    const len = rand(60);
    for (let j = 0; j < len; j++) s += alphabet[rand(alphabet.length)];
    const r = render(s);
    assert.ok(r instanceof FakeElement);
  }
  const start = Date.now();
  for (const s of ['*'.repeat(4000), '`'.repeat(4000), '*a'.repeat(2000), '||'.repeat(2000), '['.repeat(4000), '> '.repeat(2000), '~~'.repeat(2000), '_a'.repeat(2000)]) {
    render(s);
  }
  assert.ok(Date.now() - start < 3000, `took ${Date.now() - start} ms`);
});

test('the UI never parses HTML from strings', () => {
  for (const f of fs.readdirSync(UI)) {
    // Code only: comments may name what's forbidden.
    const src = fs.readFileSync(path.join(UI, f), 'utf8').split('\n').filter((l) => !l.trim().startsWith('//')).join('\n');
    for (const bad of ['innerHTML', 'outerHTML', 'insertAdjacentHTML', 'document.write', 'DOMParser', 'createContextualFragment']) {
      assert.ok(!src.includes(bad), `${f} uses ${bad}`);
    }
  }
});
