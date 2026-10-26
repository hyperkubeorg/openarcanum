// DOM helpers, modals, menus and toasts. Text always goes in through
// textContent, never innerHTML, so message content can't inject markup.
'use strict';

function $(sel, root = document) { return root.querySelector(sel); }
function $$(sel, root = document) { return Array.from(root.querySelectorAll(sel)); }

function h(tag, attrs, ...children) {
  const el = document.createElement(tag);
  if (attrs) {
    for (const [k, v] of Object.entries(attrs)) {
      if (v === undefined || v === null || v === false) continue;
      if (k === 'class') el.className = v;
      else if (k === 'text') el.textContent = v;
      else if (k === 'icon') el.appendChild(icon(v));
      else if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2), v);
      else if (k === 'value') el.value = v;
      else if (k === 'checked') el.checked = !!v;
      else if (k === 'style') setStyle(el, v);
      else el.setAttribute(k, v === true ? '' : v);
    }
  }
  append(el, children);
  if (attrs) accessible(el, attrs);
  return el;
}

// An interface icon (ui/js/icons.js, Lucide): an inline <svg> built with DOM
// calls, drawn in the text colour (currentColor) and sized by styles.css
// (.ic). Hidden from screen readers: what carries it keeps its own name
// (an icon button's title or aria-label). h()'s icon attribute puts one
// first in the element.
const SVG_NS = 'http://www.w3.org/2000/svg';
const ICON_ATTRS = { viewBox: '0 0 24 24', fill: 'none', stroke: 'currentColor', 'stroke-width': '2', 'stroke-linecap': 'round', 'stroke-linejoin': 'round', 'aria-hidden': 'true', focusable: 'false' };
function icon(name, cls = '') {
  const shapes = ICONS[name];
  if (!shapes) throw new Error(`unknown icon ${name}`); // i18n: not shown (a programming error, logged)
  const svg = document.createElementNS(SVG_NS, 'svg');
  for (const [k, v] of Object.entries(ICON_ATTRS)) svg.setAttribute(k, v);
  svg.setAttribute('class', cls ? 'ic ' + cls : 'ic');
  for (const [tag, attrs] of shapes) {
    const el = document.createElementNS(SVG_NS, tag);
    for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, v);
    svg.appendChild(el);
  }
  return svg;
}

// Styles go in through the CSSOM, property by property: the page's CSP
// (style-src 'self') refuses a style attribute, and every refusal is logged
// to the app's stderr. Custom properties (--name) need setProperty.
function setStyle(el, style) {
  if (typeof style !== 'object') throw new TypeError('h(): style must be an object, not a style attribute string'); // i18n: not shown (a programming error, logged)
  for (const [k, v] of Object.entries(style)) {
    if (k.startsWith('--')) el.style.setProperty(k, v);
    else el.style[k] = v;
  }
}

// Keyboard and screen reader basics for everything built with h(): an
// element that acts on click but isn't a control is focusable and works with
// Enter and Space, as a button; a button labelled only by its title (icon
// buttons) gets that title as its accessible name.
const NATIVE = new Set(['BUTTON', 'A', 'INPUT', 'SELECT', 'TEXTAREA', 'SUMMARY', 'VIDEO', 'AUDIO']);
function accessible(el, attrs) {
  if (attrs.onclick && !NATIVE.has(el.tagName)) {
    // A caller's own tabindex wins; Enter and Space work either way.
    if (!('tabindex' in attrs)) el.tabIndex = 0;
    if (!attrs.role) el.setAttribute('role', 'button');
    el.addEventListener('keydown', (e) => {
      if (e.target !== el || e.altKey || e.ctrlKey || e.metaKey) return;
      if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); el.click(); }
    });
  }
  if (attrs.title && !attrs['aria-label'] && (el.tagName === 'BUTTON' || el.getAttribute('role') === 'button')) {
    el.setAttribute('aria-label', attrs.title);
  }
}

function append(el, children) {
  for (const c of children.flat(Infinity)) {
    if (c === null || c === undefined || c === false) continue;
    el.appendChild(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return el;
}

function clear(el) { while (el.firstChild) el.removeChild(el.firstChild); return el; }

// ---------------------------------------------------------------- formatting

function fmtTime(ms) {
  const d = new Date(ms);
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}
function fmtDate(ms) {
  const d = new Date(ms);
  const today = new Date();
  const yest = new Date(Date.now() - 86400000);
  if (d.toDateString() === today.toDateString()) return t('Today');
  if (d.toDateString() === yest.toDateString()) return t('Yesterday');
  return d.toLocaleDateString([], { year: 'numeric', month: 'long', day: 'numeric' });
}
function fmtDateTime(ms) { return ms ? new Date(ms).toLocaleString() : '-'; }
function fmtBytes(n) {
  if (n >= 1 << 30) return t('{size} GB', { size: (n / (1 << 30)).toFixed(1) });
  if (n >= 1 << 20) return t('{size} MB', { size: (n / (1 << 20)).toFixed(1) });
  if (n >= 1 << 10) return t('{size} KB', { size: (n / (1 << 10)).toFixed(0) });
  return t('{n} bytes', { n });
}
function hue(s) { let x = 0; for (const c of String(s)) x = (x * 31 + c.charCodeAt(0)) >>> 0; return x % 360; }
function initials(name) { return (String(name || '?').trim()[0] || '?').toUpperCase(); }

// A role's color for text, as h()'s style object (the --role-color custom
// property): styles.css shows it darker or lighter as the theme needs, so
// names stay at 4.5:1 or better whatever color the community picked (the
// default Admin gold included).
function roleColorStyle(color) {
  return /^#[0-9a-f]{6}$/i.test(color || '') ? { '--role-color': color } : null;
}

function avatarEl(name, key, cls = '') {
  return h('div', { class: 'av ' + cls, style: { background: `hsl(${hue(key || name)}, 45%, 45%)` }, title: name }, initials(name));
}

// ---------------------------------------------------------------- toasts

// Info toasts go by themselves (a click dismisses one and runs its action).
// Error toasts stay until closed: their text can be selected, Copy puts it
// on the clipboard, and only its close button closes one, so a stray click can't lose it.
const MAX_ERROR_TOASTS = 5;
function toast(text, opts = {}) {
  const sticky = !!opts.error;
  const body = h('div', { class: 'toast-body' }, opts.title ? h('b', { text: opts.title }) : null, h('span', { class: 'toast-text', text }));
  const box = h('div', { class: 'toast' + (sticky ? ' error' : ''), role: sticky ? 'alert' : 'status' }, body);
  const close = () => box.remove();
  if (sticky) {
    const full = opts.title ? `${opts.title}\n${text}` : String(text);
    box.appendChild(h('div', { class: 'toast-actions' },
      copyButton(full, t('Copy'), t('Copy the error')),
      h('button', { class: 'icon-btn toast-close', type: 'button', title: t('Dismiss'), icon: 'x', onclick: close })));
  } else {
    box.addEventListener('click', () => { close(); if (opts.onclick) opts.onclick(); });
    setTimeout(close, opts.ms || 4500);
  }
  const root = $('#toasts');
  root.appendChild(box);
  const errors = $$('.toast.error', root);
  for (const old of errors.slice(0, Math.max(0, errors.length - MAX_ERROR_TOASTS))) old.remove();
  return box;
}
function toastError(e) { return toast(e && e.message ? e.message : String(e), { error: true }); }

// ---------------------------------------------------------------- modals

// Open dialogs (modal() and its kin), topmost last. Only the top
// one answers Escape, Tab stays inside it, and closing it returns focus to
// what had it before.
const LAYERS = [];
const FOCUSABLE = 'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

function focusables(root) {
  return $$(FOCUSABLE, root).filter((e) => e.getClientRects().length > 0 && getComputedStyle(e).visibility !== 'hidden');
}

document.addEventListener('keydown', (e) => {
  // A dialog removed without closing (its page re-rendered) no longer counts.
  while (LAYERS.length && !LAYERS[LAYERS.length - 1].el.isConnected) LAYERS.pop();
  const top = LAYERS[LAYERS.length - 1];
  // A context menu open over a dialog handles its own keys (openMenu).
  if (!top || $('#ctx-menu')) return;
  if (e.key === 'Escape') {
    e.preventDefault(); e.stopImmediatePropagation();
    if (top.dismissable) top.close();
  } else if (e.key === 'Tab') {
    const els = focusables(top.el);
    if (!els.length) { e.preventDefault(); return; }
    const i = els.indexOf(document.activeElement);
    if (!top.el.contains(document.activeElement) || (e.shiftKey && i <= 0) || (!e.shiftKey && i === els.length - 1)) {
      e.preventDefault();
      (e.shiftKey ? els[els.length - 1] : els[0]).focus();
    }
  }
}, true);

// Register an open dialog; returns the function that unregisters it (and
// gives focus back). `el` is the dialog box.
function pushLayer(el, close, dismissable = true) {
  const layer = { el, close, dismissable, opener: document.activeElement };
  LAYERS.push(layer);
  return () => {
    const i = LAYERS.indexOf(layer);
    if (i < 0) return;
    LAYERS.splice(i, 1);
    const back = layer.opener;
    if (back && back.isConnected && typeof back.focus === 'function') back.focus();
  };
}

// Closes every open dialog, topmost first, and the open page: for an action
// that leaves for another view (Message from a person's menu opens their
// conversation).
function closeDialogs() {
  for (const layer of LAYERS.slice().reverse()) layer.close();
  leavePage();
}

// The first field, else the first control in the body, else any control.
function focusFirst(box, body) {
  const field = $('input:not([disabled]), textarea:not([disabled]), select:not([disabled])', box);
  const target = field || focusables(body)[0] || focusables(box)[0];
  if (target) target.focus();
}

let modalSeq = 0;
function modal({ title, body, actions = [], onclose, dismissable = true }) {
  const backdrop = h('div', { class: 'backdrop' });
  const box = h('div', { class: 'modal', role: 'dialog', 'aria-modal': 'true' });
  let pop = null;
  let closed = false;
  const close = () => {
    if (closed) return;
    closed = true;
    backdrop.remove();
    if (pop) pop();
    if (onclose) onclose();
  };
  if (title) {
    const id = `modal-title-${++modalSeq}`;
    box.setAttribute('aria-labelledby', id);
    box.appendChild(h('div', { class: 'modal-head' }, h('h2', { id, text: title }),
      dismissable ? h('button', { class: 'icon-btn', title: t('Close'), onclick: close, icon: 'x' }) : null));
  }
  const bodyEl = h('div', { class: 'modal-body' });
  append(bodyEl, [body]);
  box.appendChild(bodyEl);
  if (actions.length) {
    const foot = h('div', { class: 'modal-foot' });
    for (const a of actions) {
      foot.appendChild(h('button', {
        class: 'btn ' + (a.kind || ''), text: a.label,
        onclick: async (ev) => {
          const b = ev.currentTarget;
          b.disabled = true;
          try { const r = a.run ? await a.run(ev) : undefined; if (r !== false) close(); }
          catch (e) { toastError(e); }
          finally { b.disabled = false; }
        },
      }));
    }
    box.appendChild(foot);
  }
  backdrop.appendChild(box);
  backdrop.addEventListener('mousedown', (e) => { if (e.target === backdrop && dismissable) close(); });
  pop = pushLayer(box, close, dismissable);
  $('#modal-root').appendChild(backdrop);
  setTimeout(() => { if (!closed && !box.contains(document.activeElement)) focusFirst(box, bodyEl); }, 30);
  return { close, box, body: bodyEl };
}

function confirmModal(title, text, label = t('Confirm'), danger = true) {
  return new Promise((resolve) => {
    let done = false;
    const m = modal({
      title, body: h('p', { text }),
      actions: [
        { label: t('Cancel'), run: () => { done = true; resolve(false); } },
        { label, kind: danger ? 'danger' : 'primary', run: () => { done = true; resolve(true); } },
      ],
      onclose: () => { if (!done) resolve(false); },
    });
    return m;
  });
}

function promptModal(title, label, value = '', opts = {}) {
  return new Promise((resolve) => {
    let done = false;
    const input = h(opts.multiline ? 'textarea' : 'input', { type: opts.type || 'text', value, placeholder: opts.placeholder || '' });
    modal({
      title, body: h('label', { class: 'field' }, h('span', { text: label }), input, opts.hint ? h('div', { class: 'hint', text: opts.hint }) : null),
      actions: [
        { label: t('Cancel'), run: () => { done = true; resolve(null); } },
        { label: opts.ok || t('Save'), kind: 'primary', run: () => { done = true; resolve(input.value); } },
      ],
      onclose: () => { if (!done) resolve(null); },
    });
    input.addEventListener('keydown', (e) => { if (e.key === 'Enter' && !opts.multiline) { e.preventDefault(); $('.modal-foot .primary').click(); } });
  });
}

// ---------------------------------------------------------------- pages

// A page fills the centre of the window, in place of the channel or
// conversation and the member list, between the channel list and the
// window's edge: user Settings, Community Admin (docs/design.md, "Pages").
// It isn't a dialog: the rail, the channel list, the voice bar and the user
// panel stay usable beside it, and going anywhere from them (a channel, a
// community, direct messages) leaves it. One page at a time; showPage()
// replaces the open one.
//
// showPage({
//   title,     the heading at the top (h1), also the window's title
//   context,   optional: what it is about, shown after the title (a community's name)
//   icon,      optional: an icon before the title
//   nav,       optional: sections, [{ id, label, group, render(el, show), badge() }],
//              listed down the left (under their group headings, with a
//              count from badge() if any); the page opens on `initial`
//   initial,   optional: the section to open on (unknown ids open the first)
//   render,    without nav: render(el) fills the page (may be async)
//   onClose,   runs once when the page closes, whichever way it closes
//   cls,       optional: classes for the page element
// })
// returns { el, close(opts), show(id), current(), refreshBadges(), open() }.
//
// Close (the button at the top) and Escape (when no dialog or menu is open)
// call close(): the view underneath comes back and the focus goes back to
// what opened the page. Something that shows another view itself calls
// leavePage() first: the page goes, with no redraw and no focus move.
const PAGE = { cur: null };
const APP_TITLE = document.title;
let pageSeq = 0;

function pageOpen() { return !!PAGE.cur; }
function leavePage(quiet = true) { if (PAGE.cur) PAGE.cur.close({ quiet, focus: false }); }

function showPage({ title, context = '', icon: iconName = null, nav = null, initial = null, render: draw = null, onClose = null, cls = '' }) {
  // A page opened from another page gives the focus back to what opened
  // that one, and keeps whether the call's stage was showing under it.
  const prev = PAGE.cur;
  const active = document.activeElement;
  const opener = prev && prev.el.contains(active) ? prev.opener : focusTarget(active);
  const stage = prev ? prev.stage : !!st.stage;
  if (prev) prev.close({ quiet: true, focus: false });
  closeMenu();
  // The page takes the stage's place (voice.js redraws the stage only while
  // st.stage is set); the call goes on, and Close shows the stage again.
  st.stage = false;
  const n = ++pageSeq;
  const heading = h('h1', { id: `page-title-${n}`, class: 'title', tabindex: '-1', text: title });
  let closed = false;
  const closeBtn = h('button', { class: 'btn small close', type: 'button', title: t('Close (Esc)'), 'aria-label': t('Close'), icon: 'x', onclick: () => close() },
    h('span', { text: t('Close') }));
  const navEl = nav ? h('nav', { class: 'page-nav', 'aria-label': t('{page} sections', { page: title }) }) : null;
  const section = h('section', { class: 'page-section' });
  const el = h('div', { class: ('page ' + cls).trim() },
    h('div', { class: 'main-head page-head' },
      iconName ? h('span', { class: 'muted' }, icon(iconName)) : null,
      heading,
      context ? h('span', { class: 'page-context muted', text: context }) : null,
      h('div', { class: 'spacer' }), closeBtn),
    h('div', { class: 'page-body' + (nav ? ' with-nav' : '') }, navEl, section));
  const windowTitle = (part) => { document.title = [part, title, context, APP_TITLE].filter(Boolean).join(' · '); };

  const show = async (id) => {
    const s = nav.find((x) => x.id === id) || nav[0];
    for (const it of $$('.item', navEl)) {
      const on = it.dataset.id === s.id;
      it.classList.toggle('sel', on);
      if (on) it.setAttribute('aria-current', 'page'); else it.removeAttribute('aria-current');
    }
    clear(section);
    section.scrollTop = 0;
    const hid = `page-section-${n}`;
    section.setAttribute('aria-labelledby', hid);
    section.appendChild(h('h2', { id: hid, text: s.label }));
    // Sections re-render by clearing their own container, never the heading.
    const content = h('div');
    section.appendChild(content);
    windowTitle(s.label);
    try { await s.render(content, show); } catch (e) { content.appendChild(h('div', { class: 'error', text: e.message })); }
  };

  // A section can show a count (s.badge(): items waiting for review); its
  // group heading shows its sections' total, so the way to them is marked.
  const badges = [];
  const groupHolders = new Map();
  if (nav) {
    const descs = h('div', { hidden: true });
    let group = null;
    let groupEl = null;
    for (const s of nav) {
      if (s.group !== group) {
        group = s.group;
        groupEl = group ? h('div', { class: 'group' }, h('span', { text: group })) : null;
        if (groupEl) navEl.appendChild(groupEl);
      }
      const item = h('div', { class: 'item', 'data-id': s.id, 'aria-label': s.label, onclick: () => show(s.id) }, h('span', { class: 'spacer', text: s.label }));
      navEl.appendChild(item);
      if (!s.badge) continue;
      const holder = h('span', { class: 'nav-badge' });
      const desc = h('span', { id: `nav-count-${n}-${s.id}` });
      descs.appendChild(desc);
      item.setAttribute('aria-describedby', desc.id);
      item.appendChild(holder);
      badges.push({ s, group: groupEl, holder, desc });
    }
    navEl.appendChild(descs);
  }
  const refreshBadges = () => {
    const totals = new Map();
    for (const b of badges) {
      const count = b.s.badge() || 0;
      append(clear(b.holder), [modBadge(count)]);
      b.desc.textContent = count ? tn('{n} waiting for review', '{n} waiting for review', count) : '';
      if (b.group) totals.set(b.group, (totals.get(b.group) || 0) + count);
    }
    for (const [g, count] of totals) {
      if (!groupHolders.has(g)) { const holder = h('span', { class: 'nav-badge' }); g.appendChild(holder); groupHolders.set(g, holder); }
      append(clear(groupHolders.get(g)), [modBadge(count)]);
    }
  };

  const close = (o = {}) => {
    if (closed) return;
    closed = true;
    if (PAGE.cur === page) PAGE.cur = null;
    el.remove();
    $('#app').classList.remove('page-open');
    document.title = APP_TITLE;
    if (!o.quiet) {
      st.stage = stage;
      renderMain();
      renderMembers();
    }
    if (onClose) { try { onClose(); } catch (e) { console.error(e); } }
    if (o.focus === false) return;
    restoreFocus(opener);
    if (document.activeElement && document.activeElement !== document.body) return;
    const back = $('#composer-input') || $('#rail .rail-item.sel');
    if (back) back.focus();
  };

  const page = { el, opener, stage, close, show, refreshBadges, open: () => !closed,
    current: () => { const sel = navEl && $('.item.sel', navEl); return sel ? sel.dataset.id : null; } };
  PAGE.cur = page;
  $('#app').classList.add('page-open');
  clear($('#main')).appendChild(el);
  refreshBadges();
  heading.focus();
  if (nav) show(initial && nav.find((x) => x.id === initial) ? initial : nav[0].id);
  else {
    windowTitle('');
    if (draw) Promise.resolve().then(() => draw(section)).catch((e) => section.appendChild(h('div', { class: 'error', text: e.message })));
  }
  return page;
}

// Escape leaves the page, unless a dialog or a menu is open over it (they
// close first) or what has the focus used the key itself.
document.addEventListener('keydown', (e) => {
  if (e.key !== 'Escape' || e.defaultPrevented || !PAGE.cur) return;
  while (LAYERS.length && !LAYERS[LAYERS.length - 1].el.isConnected) LAYERS.pop();
  if (LAYERS.length || $('#ctx-menu')) return;
  e.preventDefault();
  PAGE.cur.close();
});

// ---------------------------------------------------------------- menus

// Focus back on what opened a menu. A control shown only while its row has
// the focus (a message's actions) is hidden once the focus left for the
// menu, so the row takes the focus first and the control after it.
// If the view was redrawn meanwhile (muting a channel redraws the list), the
// same control in the redrawn row: found by the row's id attribute and the
// control's name.
// A control outside any row (the Settings button in the user panel) is found
// again by its name in the same column.
const ROW_KEYS = ['data-id', 'data-channel', 'data-member', 'data-voice-member', 'data-dm', 'data-community'];
const COLUMNS = '#rail, #sidebar, #main, #members';
function focusTarget(el) {
  if (!el || el === document.body) return null;
  const row = el.closest(ROW_KEYS.map((k) => `[${k}]`).join(','));
  const key = row && ROW_KEYS.find((k) => row.hasAttribute(k));
  const column = el.closest(COLUMNS);
  return { el, key, value: key ? row.getAttribute(key) : null, self: row === el, label: el.getAttribute('aria-label'), column: column ? column.id : null };
}
function restoreFocus(target, opts = {}) {
  if (!target) return;
  let el = target.el;
  if (!el.isConnected && target.key) {
    const row = $(`[${target.key}="${CSS.escape(target.value)}"]`);
    el = row && (target.self ? row : $$('button, [tabindex]', row).find((x) => x.getAttribute('aria-label') === target.label));
  } else if (!el.isConnected && target.column && target.label) {
    el = $$('button, [tabindex]', document.getElementById(target.column)).find((x) => x.getAttribute('aria-label') === target.label);
  }
  if (!el || !el.isConnected) return;
  el.focus(opts);
  if (document.activeElement === el) return;
  const row = el.parentElement && el.parentElement.closest('[tabindex]');
  if (row) { row.focus(opts); el.focus(opts); }
}

// Redraws part of the page with fn; if the keyboard focus was in it, it
// lands on the same row's control again instead of falling to the page.
function keepingFocus(container, fn) {
  const was = document.activeElement;
  const target = container && was && was !== document.body && container.contains(was) ? focusTarget(was) : null;
  const r = fn();
  if (target && (!document.activeElement || document.activeElement === document.body)) restoreFocus(target, { preventScroll: true });
  return r;
}

// The moderator badge: a count of items waiting for review. The number is
// drawn by styles.css from data-count, so the text of what carries it (a
// menu entry, a settings section) stays its label; screen readers get the
// count from the description (countBadge) or the carrier's title.
function modBadge(n, cls = 'badge mod-badge') { // i18n: not shown (CSS classes)
  return n ? h('span', { class: cls, 'aria-hidden': 'true', 'data-count': String(Math.min(n, 99)) }) : null;
}

// A count that leads somewhere, with what it counts as the accessible
// description of what carries it (its name stays its label). Returns
// [badge element, hidden description element to put anywhere on the page].
let countSeq = 0;
function countBadge(n, cls = 'badge mod-badge') { // i18n: not shown (CSS classes)
  if (!n) return [null, null];
  const id = `count-desc-${++countSeq}`;
  return [modBadge(n, cls), h('span', { id, hidden: true, text: tn('{n} waiting for review', '{n} waiting for review', n) })];
}

// Menu entries: { label, run, danger, admin (red text, not destructive),
// badge (a count), checked (a choice in
// a group: true or false), toggle (with checked: an on/off entry of its
// own, such as "Mute for me") }, { separator: true }, { group: label, items }
// (choices under a heading), or { range: { label, value, min, max, step,
// format, input, change } } (a slider in the menu itself: input as it moves,
// change when let go). Falsy entries are skipped.
// The count's description goes in `holder`, outside the entry, so the
// entry's text stays its label.
function menuEntry(it, done, holder) {
  const radio = it.checked !== undefined;
  const [badge, desc] = countBadge(it.badge, 'badge mod-badge mi-badge'); // i18n: not shown (CSS classes)
  if (desc) holder.appendChild(desc);
  return h('div', { class: 'mi' + (it.danger ? ' danger' : '') + (it.admin ? ' admin' : '') + (radio && it.checked ? ' checked' : ''), role: radio ? (it.toggle ? 'menuitemcheckbox' : 'menuitemradio') : 'menuitem',
    'aria-checked': radio ? String(!!it.checked) : null, 'aria-label': it.label, 'aria-describedby': desc ? desc.id : null, onclick: () => done(it) },
  radio ? h('span', { class: 'mi-check', 'aria-hidden': 'true' }, it.checked ? icon('check') : null) : null,
  h('span', { class: 'mi-label', text: it.label }), badge);
}

function menuRange(r) {
  const out = h('span', { class: 'mi-range-value', text: r.format(r.value) });
  const input = h('input', { type: 'range', min: String(r.min), max: String(r.max), step: String(r.step), value: String(r.value), 'aria-label': r.label,
    'aria-valuetext': r.format(r.value),
    oninput: () => { const v = Number(input.value); out.textContent = r.format(v); input.setAttribute('aria-valuetext', r.format(v)); r.input(v); },
    onchange: () => r.change(Number(input.value)) });
  return h('div', { class: 'mi-range' },
    h('div', { class: 'mi-range-head', 'aria-hidden': 'true' }, h('span', { text: r.label }), out), input);
}

function openMenu(x, y, items) {
  closeMenu();
  const opener = focusTarget(document.activeElement);
  // A slider can't be a menu entry (ARIA menus hold only items), so a menu
  // with one is a popup: the sliders at the top, then the menu proper. So is
  // a menu with a note (a person's status, { note: text }) above its entries.
  const ranged = items.some((it) => it && (it.range || it.note));
  const head = items.find((it) => it && (it.range || it.note));
  const m = h('div', { class: 'menu', id: 'ctx-menu', role: ranged ? 'dialog' : 'menu', 'aria-label': ranged ? (head.range ? head.range.label : head.note) : null });
  const list = ranged ? h('div', { class: 'menu-list', role: 'menu' }) : m;
  const done = (it) => {
    // Focus goes back first, so a dialog the entry opens returns it there
    // too; again afterwards if the action redrew the view.
    closeMenu();
    restoreFocus(opener);
    Promise.resolve(it.run()).finally(() => { if (document.activeElement === document.body) restoreFocus(opener); });
  };
  const descs = h('div', { hidden: true });
  for (const it of items) {
    if (!it) continue;
    if (it.separator) { list.appendChild(h('div', { class: 'mi-sep', role: 'separator' })); continue; }
    if (it.range) { m.appendChild(menuRange(it.range)); continue; }
    if (it.note) {
      // A status note (status.js) leads with its dot.
      m.appendChild(h('div', { class: 'mi-note' }, it.dot ? h('span', { class: 'presence inline ' + it.dot, 'aria-hidden': 'true' }) : null, h('span', { text: it.note })));
      continue;
    }
    if (it.group) {
      list.appendChild(h('div', { class: 'mi-group', role: 'group', 'aria-label': it.group },
        h('div', { class: 'mi-head', 'aria-hidden': 'true', text: it.group }), ...it.items.filter(Boolean).map((x) => menuEntry(x, done, descs))));
      continue;
    }
    list.appendChild(menuEntry(it, done, descs));
  }
  if (ranged && list.firstChild) m.appendChild(list);
  if (descs.firstChild) m.appendChild(descs);
  // From the keyboard: focus starts on the first item, the arrows move,
  // Escape closes the menu (only the menu) and focus goes back.
  m.addEventListener('keydown', (e) => {
    const els = $$('.mi, .mi-range input', m);
    const i = els.indexOf(document.activeElement);
    if ((e.key === 'ArrowDown' || e.key === 'ArrowUp') && els.length) {
      e.preventDefault();
      els[(i + (e.key === 'ArrowDown' ? 1 : els.length - 1)) % els.length].focus();
    } else if (e.key === 'Escape' || e.key === 'Tab') {
      e.preventDefault(); e.stopPropagation();
      closeMenu();
      restoreFocus(opener);
    }
  });
  document.body.appendChild(m);
  // A click from the keyboard has no position: open under what had focus.
  if (!x && !y && opener) {
    const o = opener.el.getBoundingClientRect();
    x = o.left; y = o.bottom;
  }
  const r = m.getBoundingClientRect();
  m.style.left = Math.min(x, window.innerWidth - r.width - 8) + 'px';
  m.style.top = Math.min(y, window.innerHeight - r.height - 8) + 'px';
  const first = m.querySelector('.mi, .mi-range input');
  if (first) first.focus({ preventScroll: true });
  setTimeout(() => document.addEventListener('mousedown', closeMenuOutside, true), 0);
}
// The Menu key or Shift+F10 opens the focused item's right-click menu (a
// channel, a person in a call), as on every desktop.
document.addEventListener('keydown', (e) => {
  if (!(e.key === 'ContextMenu' || (e.key === 'F10' && e.shiftKey))) return;
  const el = document.activeElement;
  if (!el || el === document.body || el.closest('#ctx-menu') || ['INPUT', 'TEXTAREA'].includes(el.tagName) || el.isContentEditable) return;
  e.preventDefault();
  el.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true }));
});

function closeMenuOutside(e) { const m = $('#ctx-menu'); if (m && !m.contains(e.target)) closeMenu(); }
function closeMenu() { const m = $('#ctx-menu'); if (m) m.remove(); document.removeEventListener('mousedown', closeMenuOutside, true); }

// ---------------------------------------------------------------- forms

function field(label, input, hint) {
  return h('label', { class: 'field' }, h('span', { text: label }), input, hint ? h('div', { class: 'hint', text: hint }) : null);
}
function selectEl(options, value) {
  const s = h('select');
  for (const [v, label] of options) s.appendChild(h('option', { value: v, text: label }));
  s.value = value;
  return s;
}
function checkbox(label, checked, hint) {
  const input = h('input', { type: 'checkbox', checked, 'aria-label': typeof label === 'string' ? label : null });
  const el = h('label', { class: 'check' }, input, h('div', null, label, hint ? h('div', { class: 'hint', text: hint }) : null));
  el.input = input;
  return el;
}
function copyButton(getText, label = t('Copy'), ariaLabel = null) {
  return h('button', { class: 'btn small copy-btn', type: 'button', text: label, 'aria-label': ariaLabel, onclick: async (e) => {
    const b = e.currentTarget;
    e.stopPropagation();
    await api('copy_text', typeof getText === 'function' ? getText() : getText);
    const old = b.textContent; b.textContent = t('Copied'); setTimeout(() => (b.textContent = old), 1200);
  } });
}

// Commands, codes, invites and paths: kept on their lines (never wrapped,
// so a copied command is what was shown) and scrolled sideways inside the
// block instead of widening the dialog, with Copy beside them. The text is
// focusable so the keyboard can scroll it too.
function codeBlock(text, opts = {}) {
  const pre = h('pre', { class: 'code-text', tabindex: '0', text });
  return h('div', { class: 'code-block' + (opts.cls ? ' ' + opts.cls : '') }, pre,
    h('div', { class: 'code-actions' }, copyButton(text, opts.copyLabel || t('Copy'), opts.copyAria || null), ...(opts.actions || [])));
}

// A settings form: its fields, then its own footer (a divider, any error on
// the left and the actions on the right), boxed apart from what follows, so
// a Save button plainly belongs to the fields above it.
function formSection(title, body, foot) {
  return h('div', { class: 'form-section' }, title ? h('h3', { text: title }) : null,
    h('div', { class: 'stack form-body' }, ...[body].flat()), foot || null);
}
function formFoot(...children) {
  return h('div', { class: 'form-foot' }, ...children);
}

// A row of actions above a list (Create role…, New channel), the primary first.
function toolbar(...children) {
  return h('div', { class: 'toolbar' }, ...children);
}

// What a section is for, in a line or two above it.
function sectionIntro(text) {
  return h('p', { class: 'section-intro', text });
}

// A list with nothing in it: one muted line where the rows would be.
function emptyNote(text) {
  return h('p', { class: 'empty-note', text });
}

// A passphrase field with the strength meter, Show/Hide and, with
// opts.generate, Generate: Python makes a random passphrase (passgen.py),
// shown in the field with Copy and a reminder to keep it in a password
// manager; opts.repeat (the "repeat" input) is filled in too.
let passSeq = 0;
function passphraseField(label, opts = {}) {
  const id = `passphrase-${++passSeq}`;
  const input = h('input', { type: 'password', id, autocomplete: 'new-password', spellcheck: 'false' });
  const bar = h('i');
  const msg = h('div', { class: 'hint' });
  const note = h('div', { class: 'notice generated-note hidden', role: 'status' });
  let timer = null;
  const meter = () => {
    clearTimeout(timer);
    timer = setTimeout(async () => {
      const s = await api('passphrase_strength', input.value);
      const colors = ['#d83c3e', '#e67e22', '#f0b232', '#23a55a', '#1e8e4f'];
      bar.style.width = (20 + s.score * 20) + '%';
      bar.style.background = colors[s.score];
      msg.textContent = s.label + (s.message ? ': ' + s.message : '');
    }, 120);
  };
  input.addEventListener('input', meter);
  const show = h('button', { class: 'btn small reveal-btn', type: 'button', text: t('Show'), 'aria-controls': id });
  const reveal = (on) => {
    input.type = on ? 'text' : 'password';
    show.textContent = on ? t('Hide') : t('Show');
  };
  show.addEventListener('click', () => reveal(input.type === 'password'));
  const generate = opts.generate ? h('button', { class: 'btn small generate-btn', type: 'button', text: t('Generate'),
    title: t('Make a strong random passphrase'), onclick: async (e) => {
      const b = e.currentTarget;
      b.disabled = true;
      try {
        const r = await api('generate_passphrase');
        input.value = r.passphrase;
        if (opts.repeat) opts.repeat.value = r.passphrase;
        reveal(true);
        meter();
        clear(note).appendChild(h('div', { class: 'stack' },
          h('div', { text: opts.saveNote || t('Save this passphrase in your password manager now. There is no reset: without it, nobody can open this profile.') }),
          h('div', { class: 'small', text: t('Six random words, about {bits} bits: strong enough even if someone copies the encrypted file.', { bits: r.bits }) }),
          h('div', null, copyButton(() => input.value, t('Copy passphrase')))));
        note.classList.remove('hidden');
        input.focus();
      } catch (err) { toastError(err); } finally { b.disabled = false; }
    } }) : null;
  const el = h('div', { class: 'field passphrase-field' }, h('label', { for: id, text: label }),
    h('div', { class: 'row' }, input, show, generate), h('div', { class: 'meter' }, bar), msg, note);
  el.input = input;
  return el;
}
