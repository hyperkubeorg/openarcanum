# age payload test vectors (C2SP CCTV)

Source: https://github.com/C2SP/CCTV, directory `age/testdata`, commit
`50a8ecf2a220f4c8bdc4f085789b8e85c26829e7` (2026-09-25). Licence and format:
`CCTV-age-README.md` (copied from the same commit).

OpenArcanum uses only the age v1 *payload* (STREAM over ChaCha20-Poly1305) for DM
file attachments (`clients/desktop/src/openarcanum/filecrypt.py`); the file key travels
inside the end-to-end encrypted DM event, not in an age header. These are the
37 vectors from that directory that carry a `file key` header, are not
armored, and expect `success` or `payload failure`, i.e. every vector that
exercises the payload with a known file key. `tests/test_filecrypt.py` runs
all of them and checks `SHA256SUMS`.
