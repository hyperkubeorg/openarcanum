# Noise test vectors

The Noise community's JSON test vectors, unmodified. Both the Python client
(`clients/desktop/tests/test_noise_vectors.py`, vendored noiseprotocol) and the Go
servers (`internal/session/vectors_test.go`, `flynn/noise`) run them from this
directory. `SHA256SUMS` pins the bytes; both tests check it.

| File | Source | Commit |
|---|---|---|
| `cacophony.txt` | https://github.com/centromere/cacophony/blob/8ee9d41e34a1a596cfa3ab12aa4069ff87dc1247/vectors/cacophony.txt (Haskell implementation; the vectors also used by noise-c and snow) | `8ee9d41e34a1a596cfa3ab12aa4069ff87dc1247` (file last changed in `18b7348c54fd61fcd0c220298883de0d09c8364d`) |
| `snow.txt` | https://github.com/mcginty/snow/blob/8ac60f51cfe3e010c84f0a454cc575ad9204fa12/tests/vectors/snow.txt (Rust implementation; adds multi-PSK vectors) | `8ac60f51cfe3e010c84f0a454cc575ad9204fa12` (file last changed in `d00b360cc61a7fe519ce7539974dca4f36c4654a`) |

Fetched 2026-10-24 from `raw.githubusercontent.com` at those commits.

What runs:

- **Python:** every vector whose pattern the vendored library implements:
  the 15 fundamental patterns with any PSK modifiers, 25519/448,
  ChaChaPoly/AESGCM, SHA256/SHA512/BLAKE2s/BLAKE2b: 576 cacophony + 224 snow
  = 800 vectors. The rest are the deferred patterns (`NK1`, `X1X`, ...),
  which it does not implement; a test asserts nothing else is skipped.
- **Go:** every vector `flynn/noise` can express: 25519 only, the 15
  fundamental patterns, at most one PSK: 288 cacophony + 120 snow = 408
  vectors (X448 and snow's multi-PSK vectors are outside its API).

Both include `Noise_XX_25519_ChaChaPoly_SHA256` (OpenArcanum's suite) from each
file.

`noise-c`'s `tests/vector/noise-c-basic.txt` is not included: it uses the
pre-revision-32 `NoisePSK_` naming that neither library parses.
