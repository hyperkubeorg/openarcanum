// SFrame (ui/js/sframe.js) against the RFC 9605 test vectors
// (tests/vectors/sframe, from the sframe-wg repository the RFC was built from).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { createHash } from 'node:crypto';

const require = createRequire(import.meta.url);
const SFrame = require('../../../shared/ui/js/sframe.js');
const dir = new URL('../vectors/sframe/', import.meta.url);
const raw = readFileSync(new URL('test-vectors.json', dir), 'utf8');
// KIDs and counters go up to 2^64 - 1: read them as BigInt from the source text.
const V = JSON.parse(raw, (k, v, ctx) => (k === 'kid' || k === 'ctr') ? BigInt(ctx.source) : v);
const hex = (s) => Uint8Array.from(Buffer.from(s, 'hex'));
const toHex = (b) => Buffer.from(b).toString('hex');

test('vector file is the one recorded', () => {
  const want = readFileSync(new URL('SHA256SUMS', dir), 'utf8').split(/\s+/)[0];
  assert.equal(createHash('sha256').update(raw).digest('hex'), want);
});

test('header encoding (all vectors)', () => {
  assert.ok(V.header.length > 200);
  for (const h of V.header) {
    assert.equal(toHex(SFrame.encodeHeader(h.kid, h.ctr)), h.encoded, `kid ${h.kid} ctr ${h.ctr}`);
    const d = SFrame.decodeHeader(hex(h.encoded));
    assert.equal(d.kid, h.kid);
    assert.equal(d.ctr, h.ctr);
    assert.equal(d.length, h.encoded.length / 2);
  }
  assert.throws(() => SFrame.decodeHeader(hex('80')), /truncated/);
  assert.throws(() => SFrame.decodeHeader(new Uint8Array(0)), /empty/);
});

test('AES-CTR + HMAC AEAD (suites 1-3)', async () => {
  assert.equal(V.aes_ctr_hmac.length, 3);
  for (const v of V.aes_ctr_hmac) {
    const sub = await SFrame.deriveSubkeys(v.cipher_suite, hex(v.key));
    assert.equal(toHex(sub.encKey), v.enc_key);
    assert.equal(toHex(sub.authKey), v.auth_key);
    const a = await SFrame.aead(v.cipher_suite, hex(v.key));
    assert.equal(toHex(await a.seal(hex(v.nonce), hex(v.aad), hex(v.pt))), v.ct);
    assert.equal(toHex(await a.open(hex(v.nonce), hex(v.aad), hex(v.ct))), v.pt);
    const bad = hex(v.ct);
    bad[0] ^= 1;
    await assert.rejects(a.open(hex(v.nonce), hex(v.aad), bad));
  }
});

test('SFrame encryption (suites 1-5)', async () => {
  assert.deepEqual(V.sframe.map((v) => v.cipher_suite), [1, 2, 3, 4, 5]);
  for (const v of V.sframe) {
    const ks = await SFrame.deriveKeySalt(v.cipher_suite, v.kid, hex(v.base_key));
    assert.equal(toHex(ks.keyLabel), v.sframe_key_label);
    assert.equal(toHex(ks.saltLabel), v.sframe_salt_label);
    assert.equal(toHex(ks.secret), v.sframe_secret);
    assert.equal(toHex(ks.key), v.sframe_key);
    assert.equal(toHex(ks.salt), v.sframe_salt);
    assert.equal(toHex(SFrame.nonceFor(ks.salt, v.ctr)), v.nonce);
    const k = await SFrame.keyContext(v.cipher_suite, v.kid, hex(v.base_key));
    assert.equal(toHex(await k.encrypt(v.ctr, hex(v.metadata), hex(v.pt))), v.ct, `suite ${v.cipher_suite}`);
    assert.equal(toHex(await k.decrypt(hex(v.ct), hex(v.metadata))), v.pt);
    await assert.rejects(k.decrypt(hex(v.ct), new Uint8Array(0)));  // metadata is authenticated
  }
});

test('call context: counters, per-sender keys, wrong key refused', async () => {
  const key = crypto.getRandomValues(new Uint8Array(16));
  const alice = new SFrame.Context();
  await alice.setSendKey(7, key);
  const bob = new SFrame.Context();
  await bob.setRecvKey(7, key);
  const f1 = await alice.protect(Uint8Array.of(1, 2, 3));
  const f2 = await alice.protect(Uint8Array.of(1, 2, 3));
  assert.notEqual(toHex(f1), toHex(f2));  // fresh nonce per frame
  assert.equal(SFrame.decodeHeader(f2).ctr, 1n);
  assert.deepEqual(await bob.unprotect(f2), Uint8Array.of(1, 2, 3));
  bob.dropRecvKey(7);
  await assert.rejects(bob.unprotect(f1), /unknown key id/);
  await bob.setRecvKey(7, crypto.getRandomValues(new Uint8Array(16)));
  await assert.rejects(bob.unprotect(f1));
  const tampered = Uint8Array.from(f1);
  tampered[tampered.length - 1] ^= 1;
  await bob.setRecvKey(7, key);
  await assert.rejects(bob.unprotect(tampered));
});

// Replays and reordering: a frame opens once; a
// frame older than the window is refused; frames out of order within it open.
test('call context: replayed frames are refused, reordered ones open once', async () => {
  const key = crypto.getRandomValues(new Uint8Array(16));
  const tx = new SFrame.Context();
  const rx = new SFrame.Context();
  const kid = (5n << 8n) | 3n;
  await tx.setSendKey(kid, key);
  await rx.setRecvKey(kid, key);
  const te = new TextEncoder();
  const td = new TextDecoder();
  const f1 = await tx.protect(te.encode('yes'));
  const f2 = await tx.protect(te.encode('no'));
  assert.equal(td.decode(await rx.unprotect(f2)), 'no');
  assert.equal(td.decode(await rx.unprotect(f1)), 'yes'); // late but inside the window
  await assert.rejects(rx.unprotect(f1), /replayed frame/);
  await assert.rejects(rx.unprotect(f2), /replayed frame/);
  // Setting the key again (as installRecvKeys does on every change) keeps
  // the window: old counters stay refused.
  await rx.setRecvKey(kid, key);
  await assert.rejects(rx.unprotect(f1), /replayed frame/);
  // A forged frame under a seen counter is refused, and one under a new
  // counter that fails authentication doesn't use the counter up.
  const f3 = await tx.protect(te.encode('three'));
  const forged = Uint8Array.from(f3);
  forged[forged.length - 1] ^= 1;
  await assert.rejects(rx.unprotect(forged));
  assert.equal(td.decode(await rx.unprotect(f3)), 'three');
  // Concurrent copies of one frame: only one opens.
  const f4 = await tx.protect(te.encode('four'));
  const both = await Promise.allSettled([rx.unprotect(f4), rx.unprotect(Uint8Array.from(f4))]);
  assert.deepEqual(both.map((r) => r.status).sort(), ['fulfilled', 'rejected']);
});

test('replay window: too old refused, gaps and jumps handled', () => {
  const w = new SFrame.ReplayWindow(64);
  assert.ok(w.mark(100n));
  assert.ok(!w.fresh(100n));
  assert.ok(w.mark(40n)); // 60 behind: inside
  assert.ok(!w.fresh(36n)); // 64 behind: outside
  assert.ok(!w.mark(36n));
  assert.ok(w.mark(101n) && w.mark(99n));
  assert.ok(w.mark(1000n)); // a jump past the window forgets everything older
  assert.ok(!w.fresh(101n) && !w.fresh(936n));
  assert.ok(w.fresh(937n) && w.mark(937n) && !w.fresh(937n));
  // Bits cleared when the window slides over them: 1000 + 64 reuses 1000's slot.
  assert.ok(w.mark(1064n) && w.fresh(1063n));
  assert.equal(SFrame.REPLAY_WINDOW >= 4096, true);
});

// A participant joining and leaving every few seconds gives every other
// page a key id per epoch; dropping a key frees its replay window (1 KiB
// each), so the context doesn't grow for the length of the call.
test('dropping a receive key frees its replay window', async () => {
  const key = crypto.getRandomValues(new Uint8Array(16));
  const rx = new SFrame.Context();
  for (let epoch = 1n; epoch <= 200n; epoch++) {
    const kid = (epoch << 8n) | 3n;
    await rx.setRecvKey(kid, key);
    assert.ok(rx.windows.has(kid));
    if (epoch > 2n) rx.dropRecvKey((epoch - 2n) << 8n | 3n);
  }
  assert.equal(rx.recv.size, 2);
  assert.equal(rx.windows.size, 2);
  assert.ok(!rx.windows.has((1n << 8n) | 3n));
});

test('setting the same key id and key again never repeats a nonce', async () => {
  const key = crypto.getRandomValues(new Uint8Array(16));
  const tx = new SFrame.Context();
  const kid = (5n << 8n) | 3n;
  await tx.setSendKey(kid, key);
  const g0 = await tx.protect(Uint8Array.of(1));
  await tx.setSendKey(kid, key);
  const g1 = await tx.protect(Uint8Array.of(1));
  assert.equal(SFrame.decodeHeader(g0).ctr, 0n);
  assert.equal(SFrame.decodeHeader(g1).ctr, 1n);
  // Another key id and back: each continues its own counter.
  await tx.setSendKey(kid + 1n, key);
  assert.equal(SFrame.decodeHeader(await tx.protect(Uint8Array.of(1))).ctr, 0n);
  await tx.setSendKey(kid, key);
  assert.equal(SFrame.decodeHeader(await tx.protect(Uint8Array.of(1))).ctr, 2n);
});
