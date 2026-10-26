# Vendored: noiseprotocol 0.3.1

The Python Noise Protocol Framework implementation used by `openarcanum.session`
(`Noise_XX_25519_ChaChaPoly_SHA256`). Why this library and
version: docs/protocol.md §3 "Noise implementation".

## Upstream

| | |
|---|---|
| Project | https://github.com/plizonczyk/noiseprotocol |
| Version | 0.3.1 (PyPI release 2020-03-03; the last release) |
| Tag commit | `v0.3.1` = `73375448c55af85df0230841af868b7f31942f0a` |
| Source | PyPI sdist `noiseprotocol-0.3.1.tar.gz`, https://files.pythonhosted.org/packages/76/17/fcf8a90dcf36fe00b475e395f34d92f42c41379c77b25a16066f63002f95/noiseprotocol-0.3.1.tar.gz |
| sdist sha256 | `b092a871b60f6a8f07f17950dc9f7098c8fe7d715b049bd4c24ee3752b90d645` (matches PyPI's published digest) |
| License | MIT, `LICENSE` in this directory (the sdist ships no license file; taken from the repository at the tag commit, sha256 `a293aa2800fa06051f7234159271f6dd1cdf98d6a6356da6d953fe06f93c66d3`) |
| Runtime dependency | `cryptography` (already a client dependency) |

## Local changes

Everything else is byte-for-byte the sdist's `noise/` directory (including
`state.py`'s CRLF line endings).

1. **Imports made relative.** Every `from noise.<module> import ...` became
   `from .<module>`, `from ..<module>` or `from ...<module>` according to
   the file's depth, so the package works as `openarcanum._vendor.noise` and
   cannot pick up a separately installed `noise` package. No other line
   changed.
2. **Removed `noise/backends/experimental/`.** It was an empty subclass of
   the default backend that nothing imports.

Nothing was needed for current `cryptography` (50.x): the code uses only
`X25519`/`X448`, `ChaCha20Poly1305`/`AESGCM`, `hashes` and `HMAC`, and the
`backend=` arguments it passes are still accepted (ignored). No warnings are
raised on import or use.

Suites other than ours (X448, AESGCM, SHA512, BLAKE2s/b, the other patterns
and PSK modifiers) are kept: they are table entries over the same
`CipherState`/`SymmetricState`/`HandshakeState` code, unreachable from
OpenArcanum (the protocol name is a constant in `session.py`), and running their
test vectors exercises that shared code more broadly.

## Lint and format

`clients/desktop/pyproject.toml` excludes `src/openarcanum/_vendor` from ruff (lint and
format) so this copy stays diffable against upstream. Correctness is covered
by `clients/desktop/tests/test_noise_vectors.py` (the cacophony and snow test vectors)
and the Go<->Python interop tests in `clients/desktop/tests/test_interop.py`.

## Upstream markers

The repository's no-markers rule (no `TODO`/`FIXME`/stubs in non-test code)
is exempted for this directory only for these upstream lines, left unchanged
to keep the copy faithful:

| File:line | Text | Why it is harmless here |
|---|---|---|
| `noise_protocol.py:12` | `TODO: Document` (class docstring of `NoiseProtocol`) | Documentation note only |
| `functions/patterns.py:8` | `TODO document` (class docstring of `Pattern`) | Documentation note only |
| `functions/patterns.py:55` | `raise NotImplementedError  # TODO implement` for the `fallback` pattern modifier | Only reachable with a protocol name containing `fallback`; OpenArcanum's protocol name is the constant `Noise_XX_25519_ChaChaPoly_SHA256`. Raising is the safe behavior |
| `state.py:342`, `state.py:411` | `raise NotImplementedError('Pattern token: ...')` | Defensive `else` for an unknown token; patterns are fixed tables, so unreachable |
| `state.py:421,423,427,429` | `# Maybe explicitly handle exception ...`, `# Maybe subclassed exception` | Comments on error paths for missing pre-message keys; XX has no pre-messages |
| `functions/cipher.py`, `functions/dh.py`, `functions/hash.py`, `functions/keypair.py`, `backends/default/ciphers.py:17` | `raise NotImplementedError` | Bodies of `@abc.abstractmethod`s; every concrete backend class overrides them |

## Updating

There is no newer upstream release. Any change to this directory must keep
`tests/test_noise_vectors.py` and `tests/test_interop.py` green and be listed
under "Local changes" above.
