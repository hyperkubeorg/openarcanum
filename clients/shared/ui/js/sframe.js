// SFrame (RFC 9605): end-to-end encryption of media frames, applied in the
// webview through encoded transforms (docs/protocol.md §12). WebCrypto
// only. All five RFC cipher suites are implemented so the RFC test vectors
// can be checked in full (tests/js/sframe.test.mjs); calls use
// AES_128_GCM_SHA256_128. Only per-call media keys ever reach this code.
'use strict';

const SFrame = (() => {
  const subtle = globalThis.crypto.subtle;
  const enc = new TextEncoder();

  // RFC 9605 §4.5 cipher suites.
  const SUITES = {
    1: { name: 'AES_128_CTR_HMAC_SHA256_80', hash: 'SHA-256', nh: 32, nk: 48, nka: 16, nn: 12, nt: 10, ctr: true },
    2: { name: 'AES_128_CTR_HMAC_SHA256_64', hash: 'SHA-256', nh: 32, nk: 48, nka: 16, nn: 12, nt: 8, ctr: true },
    3: { name: 'AES_128_CTR_HMAC_SHA256_32', hash: 'SHA-256', nh: 32, nk: 48, nka: 16, nn: 12, nt: 4, ctr: true },
    4: { name: 'AES_128_GCM_SHA256_128', hash: 'SHA-256', nh: 32, nk: 16, nn: 12, nt: 16, ctr: false },
    5: { name: 'AES_256_GCM_SHA512_128', hash: 'SHA-512', nh: 64, nk: 32, nn: 12, nt: 16, ctr: false },
  };
  const DEFAULT_SUITE = 4;

  function concat(...parts) {
    const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
    let o = 0;
    for (const p of parts) { out.set(p, o); o += p.length; }
    return out;
  }

  // I2OSP for values up to 2^64 - 1 (BigInt).
  function i2osp(value, len) {
    let v = BigInt(value);
    const out = new Uint8Array(len);
    for (let i = len - 1; i >= 0; i--) { out[i] = Number(v & 0xffn); v >>= 8n; }
    if (v !== 0n) throw new RangeError('value too large'); // i18n: not shown (SFrame error: the frame is dropped and counted)
    return out;
  }

  function minBytes(value) {
    let v = BigInt(value);
    if (v < 0n) throw new RangeError('negative');
    const bytes = [];
    do { bytes.unshift(Number(v & 0xffn)); v >>= 8n; } while (v > 0n);
    return Uint8Array.from(bytes);
  }

  // §4.3: config byte X|K(3)|Y|C(3), then KID and CTR when they don't fit.
  function encodeHeader(kid, ctr) {
    kid = BigInt(kid); ctr = BigInt(ctr);
    let config = 0;
    const parts = [];
    if (kid < 8n) config |= Number(kid) << 4;
    else { const b = minBytes(kid); config |= 0x80 | ((b.length - 1) << 4); parts.push(b); }
    if (ctr < 8n) config |= Number(ctr);
    else { const b = minBytes(ctr); config |= 0x08 | (b.length - 1); parts.push(b); }
    return concat(Uint8Array.of(config), ...parts);
  }

  function decodeHeader(buf) {
    if (!buf.length) throw new Error('empty frame'); // i18n: not shown (SFrame error: the frame is dropped and counted)
    const c = buf[0];
    let off = 1;
    const read = (n) => {
      if (off + n > buf.length) throw new Error('truncated header'); // i18n: not shown (SFrame error: the frame is dropped and counted)
      let v = 0n;
      for (let i = 0; i < n; i++) v = (v << 8n) | BigInt(buf[off + i]);
      off += n;
      return v;
    };
    const kid = (c & 0x80) ? read(((c >> 4) & 0x07) + 1) : BigInt((c >> 4) & 0x07);
    const ctr = (c & 0x08) ? read((c & 0x07) + 1) : BigInt(c & 0x07);
    return { kid, ctr, header: buf.subarray(0, off), length: off };
  }

  async function hkdfExtract(hash, salt, ikm, hlen) {
    // HMAC with an empty salt is HMAC with HashLen zero bytes (RFC 5869).
    const key = await subtle.importKey('raw', salt.length ? salt : new Uint8Array(hlen), { name: 'HMAC', hash }, false, ['sign']);
    return new Uint8Array(await subtle.sign('HMAC', key, ikm));
  }

  async function hkdfExpand(hash, prk, info, len) {
    const key = await subtle.importKey('raw', prk, { name: 'HMAC', hash }, false, ['sign']);
    const out = [];
    let block = new Uint8Array(0);
    let n = 0;
    for (let i = 1; n < len; i++) {
      block = new Uint8Array(await subtle.sign('HMAC', key, concat(block, info, Uint8Array.of(i))));
      out.push(block);
      n += block.length;
    }
    return concat(...out).subarray(0, len);
  }

  // §4.4.2
  async function deriveKeySalt(suiteId, kid, baseKey) {
    const s = SUITES[suiteId];
    const secret = await hkdfExtract(s.hash, new Uint8Array(0), baseKey, s.nh);
    const ctx = concat(i2osp(kid, 8), i2osp(suiteId, 2));
    const keyLabel = concat(enc.encode('SFrame 1.0 Secret key '), ctx); // i18n: not shown (RFC 9605 key label)
    const saltLabel = concat(enc.encode('SFrame 1.0 Secret salt '), ctx); // i18n: not shown (RFC 9605 salt label)
    return {
      secret,
      keyLabel,
      saltLabel,
      key: await hkdfExpand(s.hash, secret, keyLabel, s.nk),
      salt: await hkdfExpand(s.hash, secret, saltLabel, s.nn),
    };
  }

  // §4.5.1: AES-CTR + HMAC sub-keys are the two halves of the SFrame key.
  async function deriveSubkeys(suiteId, sframeKey) {
    const s = SUITES[suiteId];
    return { encKey: sframeKey.slice(0, s.nka), authKey: sframeKey.slice(s.nka, s.nka + s.nh) };
  }

  async function ctrTag(s, authKey, nonce, aad, ct) {
    const data = concat(i2osp(aad.length, 8), i2osp(ct.length, 8), i2osp(s.nt, 8), nonce, aad, ct);
    const k = await subtle.importKey('raw', authKey, { name: 'HMAC', hash: s.hash }, false, ['sign']);
    return new Uint8Array(await subtle.sign('HMAC', k, data)).subarray(0, s.nt);
  }

  function equal(a, b) {
    if (a.length !== b.length) return false;
    let d = 0;
    for (let i = 0; i < a.length; i++) d |= a[i] ^ b[i];
    return d === 0;
  }

  // The AEAD for a suite, keyed once per (key, suite).
  async function aead(suiteId, sframeKey) {
    const s = SUITES[suiteId];
    if (!s) throw new Error('unsupported cipher suite'); // i18n: not shown (SFrame error: the frame is dropped and counted)
    if (!s.ctr) {
      const k = await subtle.importKey('raw', sframeKey, 'AES-GCM', false, ['encrypt', 'decrypt']);
      return {
        seal: async (nonce, aad, pt) => new Uint8Array(await subtle.encrypt({ name: 'AES-GCM', iv: nonce, additionalData: aad, tagLength: 128 }, k, pt)),
        open: async (nonce, aad, ct) => new Uint8Array(await subtle.decrypt({ name: 'AES-GCM', iv: nonce, additionalData: aad, tagLength: 128 }, k, ct)),
      };
    }
    const { encKey, authKey } = await deriveSubkeys(suiteId, sframeKey);
    const k = await subtle.importKey('raw', encKey, 'AES-CTR', false, ['encrypt', 'decrypt']);
    const counter = (nonce) => concat(nonce, new Uint8Array(4));
    return {
      seal: async (nonce, aad, pt) => {
        const ct = new Uint8Array(await subtle.encrypt({ name: 'AES-CTR', counter: counter(nonce), length: 32 }, k, pt));
        return concat(ct, await ctrTag(s, authKey, nonce, aad, ct));
      },
      open: async (nonce, aad, data) => {
        if (data.length < s.nt) throw new Error('frame too short'); // i18n: not shown (SFrame error: the frame is dropped and counted)
        const ct = data.subarray(0, data.length - s.nt);
        if (!equal(await ctrTag(s, authKey, nonce, aad, ct), data.subarray(data.length - s.nt))) throw new Error('authentication failed'); // i18n: not shown (SFrame error: the frame is dropped and counted)
        return new Uint8Array(await subtle.decrypt({ name: 'AES-CTR', counter: counter(nonce), length: 32 }, k, ct));
      },
    };
  }

  function nonceFor(salt, ctr) {
    const c = i2osp(ctr, salt.length);
    return salt.map((b, i) => b ^ c[i]);
  }

  // §4.4.3 / §4.4.4 for one key: encrypt(ctr, metadata, pt) and decrypt(frame, metadata).
  async function keyContext(suiteId, kid, baseKey) {
    const { key, salt } = await deriveKeySalt(suiteId, kid, baseKey);
    const a = await aead(suiteId, key);
    return {
      kid: BigInt(kid),
      async encrypt(ctr, metadata, pt) {
        const header = encodeHeader(kid, ctr);
        return concat(header, await a.seal(nonceFor(salt, ctr), concat(header, metadata), pt));
      },
      async decrypt(frame, metadata) {
        const h = decodeHeader(frame);
        if (h.kid !== BigInt(kid)) throw new Error('wrong key id'); // i18n: not shown (SFrame error: the frame is dropped and counted)
        return a.open(nonceFor(salt, h.ctr), concat(h.header, metadata), frame.subarray(h.length));
      },
    };
  }

  // Replay protection for one receive KID (RFC 9605 §7.5 leaves it to the
  // application): a sliding window over the counter. A frame is accepted
  // once, and only if its counter is above the highest seen minus the
  // window. The window is wide because one sender's counter runs across all
  // of its media (voice, camera layers, screen), which arrive on separate
  // streams and so out of order with each other.
  const REPLAY_WINDOW = 8192;

  class ReplayWindow {
    constructor(size = REPLAY_WINDOW) { this.size = BigInt(size); this.bits = new Uint8Array(size / 8); this.top = -1n; }

    bit(ctr) { const i = Number(ctr % this.size); return [i >> 3, 1 << (i & 7)]; }

    // Whether ctr could still be accepted (not seen, not too old).
    fresh(ctr) {
      if (ctr > this.top) return true;
      if (this.top - ctr >= this.size) return false;
      const [byte, mask] = this.bit(ctr);
      return (this.bits[byte] & mask) === 0;
    }

    // Record ctr as seen; false if it was seen (or fell out of the window)
    // meanwhile.
    mark(ctr) {
      if (!this.fresh(ctr)) return false;
      if (ctr > this.top) {
        if (this.top < 0n || ctr - this.top >= this.size) this.bits.fill(0);
        else for (let c = this.top + 1n; c < ctr; c++) { const [b, m] = this.bit(c); this.bits[b] &= ~m; }
        this.top = ctr;
      }
      const [byte, mask] = this.bit(ctr);
      this.bits[byte] |= mask;
      return true;
    }
  }

  // A call: this device sends under one KID with a running counter; frames
  // from others are opened with the key for their KID. Keys change on every
  // join and leave (the community server rotates the call key).
  class Context {
    constructor(suiteId = DEFAULT_SUITE) {
      this.suite = suiteId; this.send = null; this.recv = new Map(); this.ctr = 0n;
      // The next counter per send KID used in this context: setting a KID's
      // key again continues its counter, so a (KID, key) pair never repeats a
      // nonce however often the key is set.
      this.sendCtr = new Map();
      this.windows = new Map();
    }

    async setSendKey(kid, baseKey) {
      const k = BigInt(kid);
      const next = await keyContext(this.suite, k, baseKey);
      this.send = next;
      this.ctr = this.sendCtr.get(k) || 0n;
    }

    async setRecvKey(kid, baseKey) {
      const k = BigInt(kid);
      this.recv.set(k, await keyContext(this.suite, k, baseKey));
      if (!this.windows.has(k)) this.windows.set(k, new ReplayWindow());
    }

    // The KID's replay window stays: the same key set again must not reopen
    // old counters.
    dropRecvKey(kid) { this.recv.delete(BigInt(kid)); }

    async protect(pt, metadata = new Uint8Array(0)) {
      if (!this.send) throw new Error('no send key'); // i18n: not shown (SFrame error: the frame is dropped and counted)
      const ctr = this.ctr;
      this.ctr += 1n;
      this.sendCtr.set(this.send.kid, this.ctr);
      return this.send.encrypt(ctr, metadata, pt);
    }

    async unprotect(frame, metadata = new Uint8Array(0)) {
      const { kid, ctr } = decodeHeader(frame);
      const k = this.recv.get(kid);
      if (!k) throw new Error('unknown key id'); // i18n: not shown (SFrame error: the frame is dropped and counted)
      const win = this.windows.get(kid);
      if (win && !win.fresh(ctr)) throw new Error('replayed frame'); // i18n: not shown (SFrame error: the frame is dropped and counted)
      const pt = await k.decrypt(frame, metadata);
      // Marked only once authenticated, so forged frames can't use up counters.
      if (win && !win.mark(ctr)) throw new Error('replayed frame'); // i18n: not shown (SFrame error: the frame is dropped and counted)
      return pt;
    }
  }

  return { SUITES, DEFAULT_SUITE, REPLAY_WINDOW, encodeHeader, decodeHeader, deriveKeySalt, deriveSubkeys, aead, keyContext, nonceFor, ReplayWindow, Context };
})();

if (typeof module !== 'undefined') module.exports = SFrame;
