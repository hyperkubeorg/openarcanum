// Message formatting: a CommonMark subset plus spoilers and mentions, used by
// community channels and DMs alike (docs/user-guide.md "Formatting").
//
// Supported: **bold** / __bold__, *italic* / _italic_, ~~strike~~, `code`,
// fenced ``` code blocks with a language label (no highlighting), > block
// quotes, - / * / 1. lists, # ## ### headings (rendered small), [label](url)
// links and bare URLs (shown as text and URL, never loaded or opened),
// ||spoilers|| (hidden until clicked), mentions (<@member>, <@&role>,
// @everyone, @here, and in DMs plain @name), and backslash escapes.
//
// parseMarkdown() turns text into a small tree of plain objects; toDom()
// builds DOM nodes from it with createElement and textContent only. No
// message content is ever parsed as HTML (docs/threat-model.md "Webview").
'use strict';

(function (root) {
  // The page's translate function (i18n.js); the text as it is under Node's tests.
  const t = typeof root.t === 'function' ? root.t : (s) => s; // i18n: not shown (binds the page's t, or the identity under Node)
  const MAX_DEPTH = 6;
  const URL_RE = /^https?:\/\/[^\s<>"'`|]+/;
  const LINK_RE = /^\[([^\]\n]{1,300})\]\((https?:\/\/[^\s)]{1,2000})\)/;
  const MENTION_RE = /^<@(&?)([A-Za-z0-9_-]{1,64})>/;
  const ANGLE_URL_RE = /^<(https?:\/\/[^\s<>]{1,2000})>/;
  const FENCE_RE = /^ {0,3}```\s*([\w+#.-]{0,32})\s*$/;
  const FENCE_END_RE = /^ {0,3}```\s*$/;
  const ESCAPABLE = '\\`*_~|[]()<>#@-.!>';

  function isWord(c) { return !!c && /[\p{L}\p{N}_]/u.test(c); }

  // Trailing punctuation isn't part of a bare URL.
  function trimUrl(u) { return u.replace(/[.,;:!?)\]}*_~]+$/, ''); }

  // ---------------------------------------------------------------- inline

  function parseInline(s, ctx, depth) {
    const out = [];
    let text = '';
    const flush = () => { if (text) { out.push({ t: 'text', v: text }); text = ''; } };
    const push = (node) => { flush(); out.push(node); };
    const nested = (inner) => (depth >= MAX_DEPTH ? [{ t: 'text', v: inner }] : parseInline(inner, ctx, depth + 1));
    // A delimited span: closing delimiter found, non-empty inner text that
    // doesn't start or end with a space.
    const span = (i, delim) => {
      const end = s.indexOf(delim, i + delim.length);
      if (end < 0) return null;
      const inner = s.slice(i + delim.length, end);
      if (!inner || /^\s/.test(inner) || /\s$/.test(inner)) return null;
      return { inner, next: end + delim.length };
    };
    let i = 0;
    while (i < s.length) {
      const c = s[i];
      const rest = s.slice(i);
      if (c === '\\' && i + 1 < s.length && ESCAPABLE.includes(s[i + 1])) { text += s[i + 1]; i += 2; continue; }
      if (c === '`') {
        let n = 0;
        while (s[i + n] === '`') n++;
        const delim = '`'.repeat(n);
        let end = s.indexOf(delim, i + n);
        while (end >= 0 && s[end + n] === '`') end = s.indexOf(delim, end + n + 1);
        if (end > i + n) {
          let v = s.slice(i + n, end);
          if (v.length > 2 && v.startsWith(' ') && v.endsWith(' ')) v = v.slice(1, -1);
          push({ t: 'code', v });
          i = end + n;
          continue;
        }
        text += delim; i += n; continue;
      }
      if (rest.startsWith('||')) {
        const sp = span(i, '||');
        if (sp) { push({ t: 'spoiler', c: nested(sp.inner) }); i = sp.next; continue; }
      }
      if (rest.startsWith('**') || rest.startsWith('__')) {
        const delim = rest.slice(0, 2);
        const sp = span(i, delim);
        if (sp && (delim === '**' || (!isWord(s[i - 1]) && !isWord(s[sp.next])))) {
          push({ t: 'b', c: nested(sp.inner) }); i = sp.next; continue;
        }
      }
      if (rest.startsWith('~~')) {
        const sp = span(i, '~~');
        if (sp) { push({ t: 's', c: nested(sp.inner) }); i = sp.next; continue; }
      }
      if ((c === '*' || c === '_') && s[i + 1] !== c) {
        const sp = span(i, c);
        if (sp && s[sp.next] !== c && (c === '*' || (!isWord(s[i - 1]) && !isWord(s[sp.next])))) {
          push({ t: 'i', c: nested(sp.inner) }); i = sp.next; continue;
        }
      }
      if (c === '[') {
        const m = LINK_RE.exec(rest);
        if (m) { push({ t: 'link', label: m[1], url: m[2] }); i += m[0].length; continue; }
      }
      if (c === '<') {
        let m = MENTION_RE.exec(rest);
        if (m) {
          push(m[1] ? { t: 'mention', kind: 'role', v: m[2] } : { t: 'mention', kind: 'member', v: m[2] });
          i += m[0].length; continue;
        }
        m = ANGLE_URL_RE.exec(rest);
        if (m) { push({ t: 'url', v: m[1] }); i += m[0].length; continue; }
      }
      if ((c === 'h' || c === 'H') && !isWord(s[i - 1])) {
        const m = URL_RE.exec(rest);
        if (m) {
          const u = trimUrl(m[0]);
          if (u.length > 'https://'.length) { push({ t: 'url', v: u }); i += u.length; continue; }
        }
      }
      if (c === '@' && !isWord(s[i - 1])) {
        const m = /^@(everyone|here)(?![\p{L}\p{N}_])/u.exec(rest);
        if (m) { push({ t: 'mention', kind: m[1], v: m[1] }); i += m[0].length; continue; }
        if (ctx.plainMentions) {
          const pm = /^@[\p{L}\p{N}_.-]+/u.exec(rest);
          if (pm) { push({ t: 'mention', kind: 'name', v: pm[0].slice(1) }); i += pm[0].length; continue; }
        }
      }
      text += c;
      i++;
    }
    flush();
    return out;
  }

  // ---------------------------------------------------------------- blocks

  const BULLET_RE = /^ {0,3}[-*+]\s+(.*)$/;
  const ORDERED_RE = /^ {0,3}(\d{1,9})[.)]\s+(.*)$/;
  const HEADING_RE = /^ {0,3}(#{1,3})\s+(.+?)\s*#*\s*$/;
  const QUOTE_RE = /^ {0,3}>\s?(.*)$/;

  function startsBlock(line) {
    return FENCE_RE.test(line) || QUOTE_RE.test(line) || HEADING_RE.test(line) || BULLET_RE.test(line) || ORDERED_RE.test(line);
  }

  function parseBlocks(text, ctx, depth) {
    const lines = String(text).replace(/\r\n?/g, '\n').split('\n');
    const blocks = [];
    let i = 0;
    while (i < lines.length) {
      const line = lines[i];
      let m = FENCE_RE.exec(line);
      if (m) {
        const body = [];
        i++;
        while (i < lines.length && !FENCE_END_RE.test(lines[i])) body.push(lines[i++]);
        i++; // the closing fence (or the end)
        blocks.push({ t: 'codeblock', lang: m[1], v: body.join('\n') });
        continue;
      }
      if (QUOTE_RE.test(line) && depth < 2) {
        const inner = [];
        while (i < lines.length && (m = QUOTE_RE.exec(lines[i]))) { inner.push(m[1]); i++; }
        blocks.push({ t: 'quote', c: parseBlocks(inner.join('\n'), ctx, depth + 1) });
        continue;
      }
      m = HEADING_RE.exec(line);
      if (m) { blocks.push({ t: 'h', level: m[1].length, c: parseInline(m[2], ctx, 0) }); i++; continue; }
      if (BULLET_RE.test(line) || ORDERED_RE.test(line)) {
        const ordered = !BULLET_RE.test(line);
        const re = ordered ? ORDERED_RE : BULLET_RE;
        const items = [];
        const start = ordered ? Number(ORDERED_RE.exec(line)[1]) : 1;
        while (i < lines.length && (m = re.exec(lines[i]))) {
          items.push(parseInline(ordered ? m[2] : m[1], ctx, 0));
          i++;
        }
        blocks.push({ t: ordered ? 'ol' : 'ul', start, items });
        continue;
      }
      const para = [line];
      i++;
      while (i < lines.length && !startsBlock(lines[i])) para.push(lines[i++]);
      // Blank lines around blocks are spacing, not content.
      while (para.length && !para[para.length - 1].trim()) para.pop();
      while (para.length && !para[0].trim()) para.shift();
      if (para.length) blocks.push({ t: 'p', c: parseInline(para.join('\n'), ctx, 0) });
    }
    return blocks;
  }

  function parseMarkdown(text, ctx) { return parseBlocks(String(text || ''), ctx || {}, 0); }

  // ---------------------------------------------------------------- DOM

  function el(doc, tag, cls, text) {
    const e = doc.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined) e.textContent = text;
    return e;
  }

  function mentionLabel(node, ctx) {
    if (node.kind === 'member') {
      const name = ctx.memberName ? ctx.memberName(node.v) : '';
      return '@' + (name || t('unknown member'));
    }
    if (node.kind === 'role') {
      const name = ctx.roleName ? ctx.roleName(node.v) : '';
      return '@' + (name || t('deleted role'));
    }
    return '@' + node.v;
  }

  function linkEl(doc, ctx, url, label) {
    const e = el(doc, 'span', 'link', label || url);
    e.setAttribute('title', url);
    e.setAttribute('role', 'button');
    e.setAttribute('tabindex', '0');
    e.setAttribute('data-url', url);
    const act = () => { if (ctx.onLink) ctx.onLink(url); };
    e.addEventListener('click', act);
    e.addEventListener('keydown', (ev) => { if (ev.key === 'Enter') act(); });
    return e;
  }

  function inlineDom(nodes, ctx, doc, parent) {
    for (const n of nodes) {
      switch (n.t) {
        // ctx.text, if given, renders plain text runs (custom emoji); code
        // never goes through it.
        case 'text': if (ctx.text) ctx.text(parent, n.v); else parent.appendChild(doc.createTextNode(n.v)); break;
        case 'code': parent.appendChild(el(doc, 'code', 'inline-code', n.v)); break;
        case 'b': parent.appendChild(inlineDom(n.c, ctx, doc, el(doc, 'strong'))); break;
        case 'i': parent.appendChild(inlineDom(n.c, ctx, doc, el(doc, 'em'))); break;
        case 's': parent.appendChild(inlineDom(n.c, ctx, doc, el(doc, 'del'))); break;
        case 'spoiler': {
          const sp = el(doc, 'span', 'spoiler');
          sp.setAttribute('role', 'button');
          sp.setAttribute('tabindex', '0');
          sp.setAttribute('aria-label', t('Spoiler, activate to reveal'));
          sp.appendChild(inlineDom(n.c, ctx, doc, el(doc, 'span', 'spoiler-content')));
          const reveal = () => {
            if (sp.classList.contains('revealed')) return;
            sp.classList.add('revealed');
            sp.removeAttribute('aria-label');
            sp.removeAttribute('role');
          };
          sp.addEventListener('click', reveal);
          sp.addEventListener('keydown', (ev) => { if (ev.key === 'Enter' || ev.key === ' ') reveal(); });
          parent.appendChild(sp);
          break;
        }
        case 'link': {
          parent.appendChild(linkEl(doc, ctx, n.url, n.label));
          parent.appendChild(el(doc, 'span', 'link-url', ` (${n.url})`));
          break;
        }
        case 'url': parent.appendChild(linkEl(doc, ctx, n.v)); break;
        case 'mention': {
          let cls = 'mention';
          if ((n.kind === 'member' && ctx.me && n.v === ctx.me) || n.kind === 'everyone' || n.kind === 'here') cls += ' me';
          const e = el(doc, 'span', cls, mentionLabel(n, ctx));
          if (n.kind === 'role' && ctx.roleColor && typeof root.roleColorStyle === 'function') {
            // Only a #rrggbb color, checked where every role color is (dom.js).
            const style = root.roleColorStyle(ctx.roleColor(n.v));
            if (style) e.style.setProperty('--role-color', style['--role-color']);
          }
          // A mentioned member has a menu (ctx.memberMenu): a right-click,
          // or the Menu key once the mention has the focus.
          if (n.kind === 'member' && ctx.memberMenu) {
            e.setAttribute('tabindex', '0');
            e.addEventListener('contextmenu', (ev) => ctx.memberMenu(ev, n.v));
          }
          parent.appendChild(e);
          break;
        }
        default: break;
      }
    }
    return parent;
  }

  function blockDom(blocks, ctx, doc, parent) {
    for (const b of blocks) {
      switch (b.t) {
        case 'p': parent.appendChild(inlineDom(b.c, ctx, doc, el(doc, 'div', 'md-p'))); break;
        case 'h': parent.appendChild(inlineDom(b.c, ctx, doc, el(doc, 'div', `md-h md-h${b.level}`))); break;
        case 'quote': parent.appendChild(blockDom(b.c, ctx, doc, el(doc, 'blockquote', 'md-quote'))); break;
        case 'codeblock': {
          const box = el(doc, 'div', 'codeblock');
          if (b.lang) box.appendChild(el(doc, 'div', 'code-lang', b.lang));
          const pre = el(doc, 'pre');
          pre.appendChild(el(doc, 'code', null, b.v));
          box.appendChild(pre);
          parent.appendChild(box);
          break;
        }
        case 'ul':
        case 'ol': {
          const list = el(doc, b.t, 'md-list');
          if (b.t === 'ol' && b.start !== 1) list.setAttribute('start', String(b.start));
          for (const item of b.items) list.appendChild(inlineDom(item, ctx, doc, el(doc, 'li')));
          parent.appendChild(list);
          break;
        }
        default: break;
      }
    }
    return parent;
  }

  function toDom(blocks, ctx, doc) {
    return blockDom(blocks, ctx || {}, doc, el(doc, 'div', 'mtext md')); // i18n: not shown (class names)
  }

  // renderText is what the UI calls: message text in, a .mtext element out.
  function renderText(text, ctx, doc) {
    const d = doc || root.document;
    return toDom(parseMarkdown(text, ctx), ctx, d);
  }

  // Plain text of a message for previews (reply quotes, notifications):
  // formatting marks dropped, spoilers hidden.
  function plainText(text, ctx) {
    const out = [];
    const walk = (nodes) => {
      for (const n of nodes) {
        if (n.t === 'text' || n.t === 'code' || n.t === 'url') out.push(n.v);
        else if (n.t === 'spoiler') out.push('▒▒▒');
        else if (n.t === 'link') out.push(n.label);
        else if (n.t === 'mention') out.push(mentionLabel(n, ctx || {}));
        else if (n.c) walk(n.c);
      }
    };
    const walkBlocks = (blocks) => {
      for (const b of blocks) {
        if (b.t === 'codeblock') out.push(b.v);
        else if (b.t === 'ul' || b.t === 'ol') b.items.forEach((it) => { walk(it); out.push(' '); });
        else if (b.t === 'quote') walkBlocks(b.c);
        else walk(b.c);
        out.push(' ');
      }
    };
    walkBlocks(parseMarkdown(text, ctx));
    return out.join('').replace(/\s+/g, ' ').trim();
  }

  const api = { parseMarkdown, toDom, renderText, plainText };
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  else Object.assign(root, api);
})(typeof window !== 'undefined' ? window : globalThis);
