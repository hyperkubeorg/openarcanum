# SFrame test vectors (RFC 9605)

`test-vectors.json` from https://github.com/sframe-wg/sframe (the repository
RFC 9605 was produced from), directory `test-vectors/`, commit
`3d07d8ffb23795cd7acc188bd8910dce0c5aa253` (2025-09-18). It holds the header
encoding vectors, the AES-CTR + HMAC AEAD vectors (suites 1-3) and the full
SFrame vectors for all five RFC cipher suites. `tests/js/sframe.test.mjs`
runs all of them against `clients/shared/ui/js/sframe.js` and checks
`SHA256SUMS`.
