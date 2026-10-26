// Unit tests for h()'s styles and icons (clients/shared/ui/js/dom.js). Run with
// `make test-js`. The page's CSP (style-src 'self') refuses style
// attributes, and QtWebEngine logs every refusal to the app's stderr, so
// styles must go in through the CSSOM: a style attribute is silently
// dropped, and enough refusals fill the app's stderr until the window stops
// answering.
'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const UI = path.join(__dirname, '..', '..', '..', 'shared', 'ui', 'js');

// ---------------------------------------------------------------- fake DOM

class FakeNode {}

class FakeStyle {
  constructor() { this.props = {}; }
  setProperty(k, v) { this.props[k] = String(v); }
}

class FakeElement extends FakeNode {
  constructor(tag, ns = null) {
    super();
    this.namespaceURI = ns;
    this.tagName = ns ? tag : tag.toUpperCase();
    this.attrs = {};
    this.children = [];
    this.style = new FakeStyle();
  }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
  appendChild(c) { this.children.push(c); return c; }
  addEventListener() {}
}

function loadDom() {
  const ctx = vm.createContext({
    Node: FakeNode,
    document: {
      createElement: (tag) => new FakeElement(tag),
      createElementNS: (ns, tag) => new FakeElement(tag, ns),
      createTextNode: (s) => s,
      addEventListener() {},
    },
  });
  vm.runInContext(fs.readFileSync(path.join(UI, 'icons.js'), 'utf8'), ctx);
  vm.runInContext(fs.readFileSync(path.join(UI, 'dom.js'), 'utf8'), ctx);
  return ctx;
}

test('a role color goes in as the --role-color custom property, not a style attribute', () => {
  const { h, roleColorStyle } = loadDom();
  const el = h('span', { class: 'mname', text: 'Tester', style: roleColorStyle('#e0a030') });
  assert.equal(el.style.props['--role-color'], '#e0a030');
  assert.equal(el.getAttribute('style'), null);
});

test('no role color, no style', () => {
  const { h, roleColorStyle } = loadDom();
  for (const color of [null, '', 'red', '#e0a03', 'url(x)']) {
    assert.equal(roleColorStyle(color), null);
    const el = h('span', { style: roleColorStyle(color) });
    assert.deepEqual(el.style.props, {});
    assert.equal(el.getAttribute('style'), null);
  }
});

test('ordinary style properties are set on the element style', () => {
  const { h } = loadDom();
  const el = h('div', { style: { padding: '10px', marginTop: '6px' } });
  assert.equal(el.style.padding, '10px');
  assert.equal(el.style.marginTop, '6px');
  assert.equal(el.getAttribute('style'), null);
});

test('a style attribute string is refused', () => {
  const { h } = loadDom();
  assert.throws(() => h('div', { style: '--role-color: #e0a030' }), { name: 'TypeError' });
});

// ---------------------------------------------------------------- icons

const SVG = 'http://www.w3.org/2000/svg';

test('an icon is an inline SVG in the text colour, hidden from screen readers', () => {
  const { icon } = loadDom();
  const svg = icon('bell-off');
  assert.equal(svg.namespaceURI, SVG);
  assert.equal(svg.tagName, 'svg');
  assert.equal(svg.getAttribute('viewBox'), '0 0 24 24');
  assert.equal(svg.getAttribute('stroke'), 'currentColor');
  assert.equal(svg.getAttribute('fill'), 'none');
  assert.equal(svg.getAttribute('aria-hidden'), 'true');
  assert.equal(svg.getAttribute('focusable'), 'false');
  assert.equal(svg.getAttribute('class'), 'ic');
  assert.equal(svg.getAttribute('style'), null);
  // Lucide's bell-off: four paths, all in the SVG namespace, geometry only.
  assert.equal(svg.children.length, 4);
  for (const c of svg.children) {
    assert.equal(c.namespaceURI, SVG);
    assert.equal(c.tagName, 'path');
    assert.deepEqual(Object.keys(c.attrs), ['d']);
  }
  assert.equal(icon('x', 'inline').getAttribute('class'), 'ic inline');
});

test("h()'s icon attribute puts the icon first and keeps the button's name", () => {
  const { h } = loadDom();
  const b = h('button', { class: 'icon-btn', title: 'Close', icon: 'x' }, 'after');
  assert.equal(b.children[0].tagName, 'svg');
  assert.equal(b.children[1], 'after');
  assert.equal(b.getAttribute('aria-label'), 'Close');
  assert.equal(b.getAttribute('icon'), null);
});

test('an unknown icon is a programming error', () => {
  const { icon } = loadDom();
  assert.throws(() => icon('no-such-icon'), /unknown icon/);
});
