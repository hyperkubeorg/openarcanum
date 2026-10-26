#!/usr/bin/env python3
"""Create, sign and check release manifests for auto-update (docs/release.md).

    release_manifest.py keygen --out KEYFILE
    release_manifest.py sign --key KEYFILE [--key KEYFILE ...] --version V \\
        --asset PLATFORM=PATH=URL [--asset ...] --expires-days N --out manifest.json
    release_manifest.py verify (--pubkey KEY [--pubkey KEY ...] | --builtin) manifest.json

KEYFILE is an Ed25519 private key in unencrypted PKCS#8 PEM (the same as
`openssl genpkey -algorithm ed25519`), created with mode 0600. `keygen`
prints the public key (unpadded base64url), which goes into
`RELEASE_PUBLIC_KEYS` in clients/desktop/src/openarcanum/updates.py. `--key` may be
given twice while rotating keys: the manifest then carries both signatures.
`--pubkey` takes a public key or a file holding one; `--builtin` checks
against the keys built into this client (`RELEASE_PUBLIC_KEYS`), as installed
copies will: the release workflow runs it on every manifest it signs.

Run it with the client's environment: `uv run --project clients/desktop python
clients/desktop/scripts/release_manifest.py ...`. It uses the client's own manifest
code (openarcanum.updates), so what it signs is what the client verifies.
"""

from __future__ import annotations

import argparse
import hashlib
import os
import sys
import time
from pathlib import Path

try:
    from openarcanum import updates
except ImportError:  # run from a checkout without the client installed
    sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))
    from openarcanum import updates

from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

from openarcanum import keys


def load_key(path: Path) -> keys.SigningKey:
    try:
        priv = serialization.load_pem_private_key(path.read_bytes(), password=None)
    except (OSError, ValueError, TypeError) as e:
        raise SystemExit(f"{path}: not an unencrypted PKCS#8 PEM private key ({e})") from None
    if not isinstance(priv, Ed25519PrivateKey):
        raise SystemExit(f"{path}: not an Ed25519 key")
    seed = priv.private_bytes(serialization.Encoding.Raw, serialization.PrivateFormat.Raw, serialization.NoEncryption())
    return keys.SigningKey(seed)


def cmd_keygen(args: argparse.Namespace) -> int:
    out = Path(args.out)
    priv = Ed25519PrivateKey.generate()
    pem = priv.private_bytes(
        serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8, serialization.NoEncryption()
    )
    try:
        fd = os.open(out, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    except FileExistsError:
        raise SystemExit(f"{out} exists; refusing to overwrite a key") from None
    with os.fdopen(fd, "wb") as f:
        f.write(pem)
    pub = priv.public_key().public_bytes(serialization.Encoding.Raw, serialization.PublicFormat.Raw)
    print(updates.b64u(pub))
    print(f"key id {updates.key_id(pub)}; private key written to {out}", file=sys.stderr)
    return 0


def parse_asset(spec: str) -> tuple[str, Path, str]:
    try:
        platform, rest = spec.split("=", 1)
        path, url = rest.split("=", 1)
    except ValueError:
        raise SystemExit(f"--asset {spec!r}: expected PLATFORM=PATH=URL") from None
    if platform not in updates.PLATFORM_KINDS:
        raise SystemExit(f"--asset: unknown platform {platform!r} (one of {', '.join(updates.PLATFORM_KINDS)})")
    try:
        updates.check_url(url, allow_loopback_http=False)
    except updates.UpdateError as e:
        raise SystemExit(f"--asset {platform}: {e}") from None
    return platform, Path(path), url


def file_digest(path: Path) -> tuple[str, int]:
    h = hashlib.sha256()
    n = 0
    try:
        with open(path, "rb") as f:
            while chunk := f.read(1 << 20):
                h.update(chunk)
                n += len(chunk)
    except OSError as e:
        raise SystemExit(f"{path}: {e.strerror}") from None
    return h.hexdigest(), n


def cmd_sign(args: argparse.Namespace) -> int:
    try:
        updates.version_key(args.version)
    except ValueError as e:
        raise SystemExit(f"--version: {e}") from None
    if not 1 <= args.expires_days <= updates.MAX_MANIFEST_DAYS:
        raise SystemExit(f"--expires-days must be between 1 and {updates.MAX_MANIFEST_DAYS}")
    signers = [load_key(Path(k)) for k in args.key]
    assets: dict[str, updates.Asset] = {}
    for spec in args.asset:
        platform, path, url = parse_asset(spec)
        if platform in assets:
            raise SystemExit(f"--asset: {platform} given twice")
        sha, size = file_digest(path)
        if not 0 < size <= updates.MAX_ASSET_BYTES:
            raise SystemExit(f"{path}: size {size} is out of range")
        assets[platform] = updates.Asset(platform, url, sha, size, updates.PLATFORM_KINDS[platform])
    now = time.time()
    payload = updates.build_payload(args.version, assets, now, now + args.expires_days * 86400)
    data = updates.sign_envelope(payload, signers)
    Path(args.out).write_bytes(data)
    ids = ", ".join(updates.key_id(k.public) for k in signers)
    print(f"signed {args.version} ({', '.join(sorted(assets))}) with {ids}")
    print(f"expires {payload['expires_at']}; wrote {args.out}")
    return 0


def read_pubkey(text: str) -> bytes:
    p = Path(text)
    if p.is_file():
        text = p.read_text(encoding="utf-8").strip()
    try:
        return updates.public_key(text)
    except ValueError as e:
        raise SystemExit(f"--pubkey: {e}") from None


def cmd_verify(args: argparse.Namespace) -> int:
    trusted = [read_pubkey(k) for k in args.pubkey or ()]
    if args.builtin:
        if not updates.RELEASE_PUBLIC_KEYS:
            raise SystemExit("--builtin: this client has no built-in release keys (RELEASE_PUBLIC_KEYS in updates.py)")
        trusted += [updates.public_key(k) for k in updates.RELEASE_PUBLIC_KEYS]
    if not trusted:
        raise SystemExit("verify: give --pubkey or --builtin")
    try:
        raw = Path(args.manifest).read_bytes()
    except OSError as e:
        raise SystemExit(f"{args.manifest}: {e.strerror}") from None
    try:
        m = updates.verify_manifest(raw, trusted)
    except updates.UpdateError as e:
        print(f"REFUSED: {e}", file=sys.stderr)
        return 1
    print(f"OK: {m.product} {m.version}, signed by {m.signed_by}")
    print(f"released {updates.timestamp(m.released_at)}, expires {updates.timestamp(m.expires_at)}")
    for a in m.assets.values():
        print(f"  {a.platform}: {a.size} bytes sha256 {a.sha256} {a.url}")
    return 0


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog="release_manifest.py", description=__doc__.split("\n\n")[0])
    sub = p.add_subparsers(dest="cmd", required=True)
    k = sub.add_parser("keygen", help="create a release signing key and print its public key")
    k.add_argument("--out", required=True, metavar="KEYFILE")
    s = sub.add_parser("sign", help="build and sign a manifest")
    s.add_argument("--key", required=True, action="append", metavar="KEYFILE")
    s.add_argument("--version", required=True)
    s.add_argument("--asset", required=True, action="append", metavar="PLATFORM=PATH=URL")
    s.add_argument("--expires-days", required=True, type=int, metavar="N")
    s.add_argument("--out", required=True, metavar="FILE")
    v = sub.add_parser("verify", help="check a manifest as the client does")
    v.add_argument("--pubkey", action="append", metavar="KEY")
    v.add_argument("--builtin", action="store_true", help="trust the keys built into this client")
    v.add_argument("manifest")
    args = p.parse_args(argv)
    return {"keygen": cmd_keygen, "sign": cmd_sign, "verify": cmd_verify}[args.cmd](args)


if __name__ == "__main__":
    sys.exit(main())
