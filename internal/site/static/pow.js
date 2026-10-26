// The report forms' anti-spam check: Hashcash proof of work (internal/pow,
// the same scheme as the relay's, under the site's own context string).
// Find an 8-byte counter such that
//   SHA-256(context || 0x00 || challenge || counter)
// starts with `bits` zero bits; the server checks it with one hash. It runs
// while the reporter types, in small slices so the page stays responsive.
// SHA-256 is written out here (FIPS 180-4) because WebCrypto's digest is
// asynchronous per call and missing on plain-HTTP pages.
'use strict';

(function () {
  const K = new Uint32Array([
    0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
    0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
    0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
    0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
    0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
    0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
    0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
    0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
  ]);
  const W = new Uint32Array(64);
  const H = new Uint32Array(8);

  // sha256Words hashes the padded message `blocks` (a Uint32Array of whole
  // 64-byte blocks, big-endian words) into H.
  function sha256Words(blocks) {
    H.set([0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19]);
    for (let off = 0; off < blocks.length; off += 16) {
      for (let t = 0; t < 16; t++) W[t] = blocks[off + t];
      for (let t = 16; t < 64; t++) {
        const a = W[t - 15], b = W[t - 2];
        const s0 = ((a >>> 7) | (a << 25)) ^ ((a >>> 18) | (a << 14)) ^ (a >>> 3);
        const s1 = ((b >>> 17) | (b << 15)) ^ ((b >>> 19) | (b << 13)) ^ (b >>> 10);
        W[t] = (W[t - 16] + s0 + W[t - 7] + s1) | 0;
      }
      let a = H[0], b = H[1], c = H[2], d = H[3], e = H[4], f = H[5], g = H[6], h = H[7];
      for (let t = 0; t < 64; t++) {
        const S1 = ((e >>> 6) | (e << 26)) ^ ((e >>> 11) | (e << 21)) ^ ((e >>> 25) | (e << 7));
        const ch = (e & f) ^ (~e & g);
        const t1 = (h + S1 + ch + K[t] + W[t]) | 0;
        const S0 = ((a >>> 2) | (a << 30)) ^ ((a >>> 13) | (a << 19)) ^ ((a >>> 22) | (a << 10));
        const maj = (a & b) ^ (a & c) ^ (b & c);
        const t2 = (S0 + maj) | 0;
        h = g; g = f; f = e; e = (d + t1) | 0; d = c; c = b; b = a; a = (t1 + t2) | 0;
      }
      H[0] += a; H[1] += b; H[2] += c; H[3] += d; H[4] += e; H[5] += f; H[6] += g; H[7] += h;
    }
  }

  // pad lays out message bytes as padded big-endian words.
  function pad(bytes) {
    const n = Math.ceil((bytes.length + 9) / 64) * 16;
    const words = new Uint32Array(n);
    for (let i = 0; i < bytes.length; i++) words[i >> 2] |= bytes[i] << (24 - (i % 4) * 8);
    words[bytes.length >> 2] |= 0x80 << (24 - (bytes.length % 4) * 8);
    words[n - 1] = bytes.length * 8;
    return words;
  }

  function leadingZeroBits() {
    let n = 0;
    for (let i = 0; i < 8; i++) {
      if (H[i] === 0) { n += 32; continue; }
      return n + Math.clz32(H[i]);
    }
    return n;
  }

  // solve searches counters from 0 up, `slice` attempts per tick.
  function solve(context, challenge, bits, done) {
    const enc = new TextEncoder();
    const prefix = [...enc.encode(context), 0, ...enc.encode(challenge)];
    const msg = new Uint8Array(prefix.length + 8);
    msg.set(prefix);
    const words = pad(msg);
    // The counter is the last 8 message bytes; rewrite just those.
    const at = prefix.length;
    let i = 0;
    const slice = 20000;
    function setCounter(v) {
      const hi = Math.floor(v / 0x100000000), lo = v >>> 0;
      const bytes = [hi >>> 24, (hi >>> 16) & 255, (hi >>> 8) & 255, hi & 255, lo >>> 24, (lo >>> 16) & 255, (lo >>> 8) & 255, lo & 255];
      for (let k = 0; k < 8; k++) {
        const p = at + k, w = p >> 2, sh = 24 - (p % 4) * 8;
        words[w] = (words[w] & ~(0xff << sh)) | (bytes[k] << sh);
      }
      return bytes;
    }
    function tick() {
      for (let end = i + slice; i < end; i++) {
        const bytes = setCounter(i);
        sha256Words(words);
        if (leadingZeroBits() >= bits) {
          done(bytes.map((b) => b.toString(16).padStart(2, '0')).join(''));
          return;
        }
      }
      setTimeout(tick, 0);
    }
    tick();
  }

  const form = document.querySelector('form[data-pow-challenge]');
  if (!form) return;
  const status = document.getElementById('pow-status');
  const button = form.querySelector('button[type="submit"]');
  const field = form.querySelector('input[name="pow_counter"]');
  let solved = false, submitWhenSolved = false;
  const say = (text) => { if (status) status.textContent = text; };

  say('Running the anti-spam check…');
  solve(form.dataset.powContext, form.dataset.powChallenge, Number(form.dataset.powBits) || 0, (hex) => {
    field.value = hex;
    solved = true;
    say('Anti-spam check done.');
    if (submitWhenSolved) form.submit();
  });
  form.addEventListener('submit', (ev) => {
    if (solved) return;
    ev.preventDefault();
    submitWhenSolved = true;
    if (button) button.disabled = true;
    say('Finishing the anti-spam check; your report is sent as soon as it is done…');
  });
}());
