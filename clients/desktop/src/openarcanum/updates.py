"""Auto-update from a signed release manifest (docs/release.md).

The client fetches one small JSON file, the release manifest, checks its
Ed25519 signature against the release keys built into this copy, and only
then reads it. A newer version's download for this platform goes to the
cache directory, bounded by the size the manifest states, and must match the
manifest's size and SHA-256 exactly. Nothing is installed until the user
says so in the UI ("Restart and install"); the install copies the verified
file again, hashing what it writes, so a file changed in the cache after the
check is refused.

The release keys and the manifest URL are set per build (docs/release.md
"The release key"). Without keys the updater reports that updates aren't
configured and never touches the network.
"""

from __future__ import annotations

import asyncio
import base64
import binascii
import contextlib
import hashlib
import hmac
import http.client
import json
import logging
import os
import platform
import re
import secrets
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from collections.abc import Callable, Sequence
from dataclasses import dataclass
from datetime import UTC, datetime
from pathlib import Path
from typing import Any
from urllib.parse import urlparse

from openarcanum import PRODUCT_NAME, __version__, keys
from openarcanum.errors import UserError
from openarcanum.i18n import N_, _
from openarcanum.paths import POSIX, atomic_write, ensure_dir

log = logging.getLogger(__name__)

# ---------------------------------------------------------------- release configuration

# Trusted release public keys (raw Ed25519, unpadded base64url, as printed by
# `clients/desktop/scripts/release_manifest.py keygen`). None until the release key is
# generated offline; the private half never enters this repository. List
# more than one while rotating keys (docs/release.md "Key rotation").
RELEASE_PUBLIC_KEYS: tuple[str, ...] | None = None
# Where the manifest lives: attached to the latest GitHub Release
# (docs/release.md).
MANIFEST_URL = "https://github.com/hyperkubeorg/openarcanum/releases/latest/download/manifest.json"

# Test-only override, read from the environment and nothing else (never from
# a setting, so neither a page script nor a user setting can change the keys).
# It is honored only when the URL is on this computer (loopback), so it can't
# point a copy at a remote host signing with someone else's key.
TEST_KEYS_ENV = "OPENARCANUM_UPDATE_TEST_KEYS"
TEST_URL_ENV = "OPENARCANUM_UPDATE_TEST_URL"

# ---------------------------------------------------------------- format

CTX = keys.CTX_RELEASE_MANIFEST
CHECK_INTERVAL = 24 * 3600
MAX_MANIFEST_BYTES = 64 * 1024
MAX_SIGNATURES = 8
MAX_ASSET_BYTES = 1024 * 1024 * 1024
HTTP_TIMEOUT = 30.0
# How long installing a verified update may take (copy and hash, or mount
# and copy the .dmg) before it is abandoned.
INSTALL_TIMEOUT = 600.0
CHUNK = 256 * 1024
USER_AGENT = f"{PRODUCT_NAME}-updater"

# Platform name -> the kind of asset it installs.
PLATFORM_KINDS = {
    "linux-x86_64": "appimage",
    "windows-x86_64": "exe",
    "macos-universal": "dmg",
}
EXTENSIONS = {"appimage": ".AppImage", "exe": ".exe", "dmg": ".dmg"}
# The Inno Setup flags for an unattended upgrade; the installer restarts the app.
WINDOWS_INSTALLER_FLAGS = ("/SILENT", "/SUPPRESSMSGBOXES", "/NORESTART")

_SEMVER = re.compile(
    r"(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)"
    r"(?:-((?:0|[1-9]\d*|\d*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9]\d*|\d*[A-Za-z-][0-9A-Za-z-]*))*))?"
    r"(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?"
)
_TIMESTAMP = re.compile(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z")
_HEX64 = re.compile(r"[0-9a-f]{64}")
_B64URL = re.compile(r"[A-Za-z0-9_-]*")


class UpdateError(UserError):
    """A refused manifest, download or install; the message is shown to the user."""


# ---------------------------------------------------------------- versions


def version_key(v: str) -> tuple:
    """Semantic Versioning 2.0.0 precedence (build metadata is ignored)."""
    if not isinstance(v, str) or len(v) > 64:
        raise ValueError("not a version")
    m = _SEMVER.fullmatch(v)
    if not m:
        raise ValueError(f"{v!r} is not a semantic version")
    major, minor, patch, pre = int(m[1]), int(m[2]), int(m[3]), m[4]
    if pre is None:
        return (major, minor, patch, (1,))
    ids = tuple((0, int(p), "") if p.isdigit() else (1, 0, p) for p in pre.split("."))
    return (major, minor, patch, (0, ids))


def is_newer(candidate: str, running: str) -> bool:
    return version_key(candidate) > version_key(running)


# ---------------------------------------------------------------- encodings


def b64u(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode()


def unb64u(s: Any, what: str) -> bytes:
    """Strict unpadded base64url (no stray characters, canonical padding bits)."""
    if not isinstance(s, str) or not _B64URL.fullmatch(s) or len(s) % 4 == 1:
        raise UpdateError(_("The update information has a damaged {what}.").format(what=_(what)))
    try:
        out = base64.urlsafe_b64decode(s + "=" * (-len(s) % 4))
    except binascii.Error:
        raise UpdateError(_("The update information has a damaged {what}.").format(what=_(what))) from None
    if b64u(out) != s:
        raise UpdateError(_("The update information has a damaged {what}.").format(what=_(what)))
    return out


def public_key(text: str) -> bytes:
    """A release public key as printed by `keygen` (unpadded base64url)."""
    try:
        raw = unb64u(text.strip(), N_("key"))
    except UpdateError:
        raise ValueError("a release public key is 32 bytes of unpadded base64url") from None
    if len(raw) != 32:
        raise ValueError("a release public key is 32 bytes of unpadded base64url")
    return raw


def key_id(pub: bytes) -> str:
    return keys.fingerprint(pub)


def canonical(payload: dict) -> bytes:
    """The bytes that are signed: sorted keys, no spaces, ASCII only."""
    return json.dumps(payload, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode()


def timestamp(t: float) -> str:
    return datetime.fromtimestamp(int(t), UTC).strftime("%Y-%m-%dT%H:%M:%SZ")


def parse_timestamp(s: Any, what: str) -> float:
    if not isinstance(s, str) or not _TIMESTAMP.fullmatch(s):
        raise UpdateError(_("The update information's {what} isn't in the expected time format.").format(what=_(what)))
    try:
        return datetime.strptime(s, "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=UTC).timestamp()
    except ValueError:
        raise UpdateError(_("The update information's {what} isn't a valid time.").format(what=_(what))) from None


# ---------------------------------------------------------------- URLs


def _loopback(host: str | None) -> bool:
    return (host or "") in ("127.0.0.1", "localhost", "::1")


def check_url(url: Any, allow_loopback_http: bool, what: str = N_("download")) -> str:
    """https:// only; plain http:// only to this computer, and only when the
    manifest itself came from this computer (tests)."""
    if not isinstance(url, str) or len(url) > 2048:
        raise UpdateError(_("The update information has a damaged {what} address.").format(what=_(what)))
    try:
        p = urlparse(url)
        host = p.hostname
    except ValueError:  # e.g. an unclosed IPv6 literal
        raise UpdateError(_("The {what} address {url} is damaged.").format(what=_(what), url=repr(url))) from None
    if not p.netloc or not host:
        raise UpdateError(_("The {what} address {url} is damaged.").format(what=_(what), url=repr(url)))
    if p.scheme == "https":
        return url
    if p.scheme == "http" and allow_loopback_http and _loopback(host):
        return url
    raise UpdateError(
        _("The {what} address {url} doesn't start with https://, so it wasn't used.").format(
            what=_(what), url=repr(url)
        )
    )


# ---------------------------------------------------------------- manifest


@dataclass(frozen=True)
class Asset:
    platform: str
    url: str
    sha256: str
    size: int
    kind: str


@dataclass(frozen=True)
class Manifest:
    product: str
    version: str
    released_at: float
    expires_at: float
    assets: dict[str, Asset]
    signed_by: str  # key id of the trusted key that verified it


def sign_envelope(payload: dict, signers: Sequence[keys.SigningKey]) -> bytes:
    """The manifest file: the canonical payload and one signature per key."""
    if not signers:
        raise ValueError("at least one signing key is needed")
    body = canonical(payload)
    env = {
        "payload": b64u(body),
        "signatures": [{"key": b64u(k.public), "sig": b64u(k.sign(CTX, body))} for k in signers],
    }
    return (json.dumps(env, indent=2) + "\n").encode()


# The longest a release manifest may be valid (expires_at - released_at):
# replaying an old manifest hides newer releases for at most this long
# (docs/release.md). The signing tool refuses longer, and so does the client.
MAX_MANIFEST_DAYS = 30


def build_payload(
    version: str, assets: dict[str, Asset], released_at: float, expires_at: float, product: str = PRODUCT_NAME
) -> dict:
    version_key(version)
    if expires_at <= released_at:
        raise ValueError("the manifest must expire after it is released")
    if expires_at - released_at > MAX_MANIFEST_DAYS * 86400:
        raise ValueError(f"the manifest may be valid for at most {MAX_MANIFEST_DAYS} days")
    return {
        "product": product,
        "version": version,
        "released_at": timestamp(released_at),
        "expires_at": timestamp(expires_at),
        "assets": {
            a.platform: {"url": a.url, "sha256": a.sha256, "size": a.size, "kind": a.kind}
            for a in sorted(assets.values(), key=lambda a: a.platform)
        },
    }


def _verify_signatures(env: dict, trusted: Sequence[bytes]) -> tuple[bytes, str]:
    sigs = env.get("signatures")
    if not isinstance(sigs, list) or not sigs:
        raise UpdateError(_("The update information isn't signed, so it wasn't used."))
    if len(sigs) > MAX_SIGNATURES:
        raise UpdateError(_("The update information has too many signatures."))
    body = unb64u(env.get("payload"), N_("payload"))
    trusted_set = set(trusted)
    saw_trusted = False
    for s in sigs:
        if not isinstance(s, dict):
            raise UpdateError(_("The update information has a damaged signature."))
        pub = unb64u(s.get("key"), N_("signing key"))
        sig = unb64u(s.get("sig"), N_("signature"))
        if pub not in trusted_set:
            continue
        saw_trusted = True
        if keys.is_valid(pub, CTX, body, sig):
            return body, key_id(pub)
    if saw_trusted:
        raise UpdateError(_("The update information's signature isn't genuine, so it wasn't used."))
    raise UpdateError(_("The update information is signed by a key this version doesn't trust, so it wasn't used."))


def _asset(platform_name: str, a: Any, allow_loopback_http: bool) -> Asset:
    if not isinstance(a, dict):
        raise UpdateError(
            _("The update information's {platform_name} entry is damaged.").format(platform_name=platform_name)
        )
    kind = a.get("kind")
    if kind != PLATFORM_KINDS[platform_name]:
        raise UpdateError(
            _("The update information's {platform_name} download is the wrong kind ({kind}).").format(
                platform_name=platform_name, kind=repr(kind)
            )
        )
    sha = a.get("sha256")
    if not isinstance(sha, str) or not _HEX64.fullmatch(sha):
        raise UpdateError(
            _("The update information's {platform_name} checksum is damaged.").format(platform_name=platform_name)
        )
    size = a.get("size")
    if isinstance(size, bool) or not isinstance(size, int) or not 0 < size <= MAX_ASSET_BYTES:
        raise UpdateError(
            _("The update information's {platform_name} size is out of range.").format(platform_name=platform_name)
        )
    url = check_url(a.get("url"), allow_loopback_http)
    return Asset(platform_name, url, sha, size, kind)


def verify_manifest(
    raw: bytes, trusted: Sequence[bytes], now: float | None = None, allow_loopback_http: bool = False
) -> Manifest:
    """Check the signature first, then the content. Raises UpdateError."""
    if len(raw) > MAX_MANIFEST_BYTES:
        raise UpdateError(_("The update information is too large."))
    try:
        env = json.loads(raw)
    except ValueError, UnicodeDecodeError:
        raise UpdateError(_("The update information is damaged.")) from None
    if not isinstance(env, dict):
        raise UpdateError(_("The update information is damaged."))
    if not trusted:
        raise UpdateError(_("This version can't check updates."))
    body, signer = _verify_signatures(env, trusted)
    # Only signed bytes are parsed from here on.
    try:
        p = json.loads(body)
    except ValueError, UnicodeDecodeError:
        raise UpdateError(_("The signed update information is damaged.")) from None
    if not isinstance(p, dict):
        raise UpdateError(_("The signed update information is damaged."))
    if p.get("product") != PRODUCT_NAME:
        raise UpdateError(
            _("The update information is for {product}, not {name}.").format(
                product=repr(str(p.get("product"))[:40]), name=PRODUCT_NAME
            )
        )
    version = p.get("version")
    try:
        version_key(version)
    except ValueError:
        raise UpdateError(_("The update information has a damaged version number.")) from None
    released = parse_timestamp(p.get("released_at"), N_("release time"))
    expires = parse_timestamp(p.get("expires_at"), N_("expiry time"))
    if expires <= released:
        raise UpdateError(_("The update information is damaged: it expires before its release date."))
    if expires - released > MAX_MANIFEST_DAYS * 86400:
        raise UpdateError(
            _("The update information claims to be valid for more than {days} days, so it wasn't used.").format(
                days=MAX_MANIFEST_DAYS
            )
        )
    now = time.time() if now is None else now
    if now >= expires:
        raise UpdateError(
            _(
                "The update information expired on {expires}, so it wasn't used. "
                "If your clock is right, the update server is out of date."
            ).format(expires=timestamp(expires))
        )
    raw_assets = p.get("assets")
    if not isinstance(raw_assets, dict):
        raise UpdateError(_("The update information lists no downloads."))
    # Platforms this version doesn't know are skipped, so later manifests can
    # add them; a known platform's entry must be well formed.
    assets = {name: _asset(name, a, allow_loopback_http) for name, a in raw_assets.items() if name in PLATFORM_KINDS}
    return Manifest(PRODUCT_NAME, version, released, expires, assets, signer)


# ---------------------------------------------------------------- configuration


@dataclass(frozen=True)
class UpdateConfig:
    keys: tuple[bytes, ...]
    url: str
    allow_loopback_http: bool = False
    test_override: bool = False

    @property
    def configured(self) -> bool:
        if not self.keys:
            return False
        host = (urlparse(self.url).hostname or "").lower()
        return not (host == "invalid" or host.endswith(".invalid"))

    @classmethod
    def load(cls, environ: dict | None = None) -> UpdateConfig:
        env = os.environ if environ is None else environ
        test_keys, test_url = env.get(TEST_KEYS_ENV, "").strip(), env.get(TEST_URL_ENV, "").strip()
        if test_keys and test_url:
            p = urlparse(test_url)
            if p.scheme in ("http", "https") and _loopback(p.hostname):
                try:
                    ks = tuple(public_key(k) for k in test_keys.split(",") if k.strip())
                except ValueError:
                    log.warning("ignoring %s: not a list of release public keys", TEST_KEYS_ENV)
                else:
                    log.warning("updates: using the test update source on this computer (%s)", TEST_URL_ENV)
                    return cls(ks, test_url, allow_loopback_http=True, test_override=True)
            else:
                log.warning("ignoring %s: the test update source must be on this computer", TEST_URL_ENV)
        built_in = tuple(public_key(k) for k in RELEASE_PUBLIC_KEYS or ())
        return cls(built_in, MANIFEST_URL)


# ---------------------------------------------------------------- HTTP


class _SafeRedirects(urllib.request.HTTPRedirectHandler):
    """Follow redirects only to URLs the manifest itself could name."""

    def __init__(self, allow_loopback_http: bool):
        super().__init__()
        self.allow = allow_loopback_http

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        check_url(newurl, self.allow, N_("redirect"))
        return super().redirect_request(req, fp, code, msg, headers, newurl)


def _opener(allow_loopback_http: bool) -> urllib.request.OpenerDirector:
    return urllib.request.build_opener(_SafeRedirects(allow_loopback_http))


def _request(url: str, headers: dict | None = None) -> urllib.request.Request:
    return urllib.request.Request(url, headers={"User-Agent": USER_AGENT, **(headers or {})})


def fetch_manifest(url: str, allow_loopback_http: bool) -> bytes:
    check_url(url, allow_loopback_http, N_("update information"))
    try:
        with _opener(allow_loopback_http).open(_request(url), timeout=HTTP_TIMEOUT) as r:
            data = r.read(MAX_MANIFEST_BYTES + 1)
    except urllib.error.HTTPError as e:
        raise UpdateError(
            _("The update server answered {code} for the update information.").format(code=e.code)
        ) from None
    except (urllib.error.URLError, http.client.HTTPException, OSError, TimeoutError) as e:
        raise UpdateError(
            _("Couldn't reach the update server: {error}").format(error=getattr(e, "reason", e))
        ) from None
    if len(data) > MAX_MANIFEST_BYTES:
        raise UpdateError(_("The update information is too large."))
    return data


def sha256_file(path: Path) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as f:
        while chunk := f.read(CHUNK):
            h.update(chunk)
    return h.hexdigest()


def download(
    asset: Asset,
    dest: Path,
    allow_loopback_http: bool,
    progress: Callable[[int, int], None] | None = None,
) -> Path:
    """Download `asset` to `dest`, resuming a partial `dest.part` when the
    server supports ranges and starting over when it doesn't. Never writes
    more than asset.size bytes; the result matches size and SHA-256 or is
    deleted."""
    check_url(asset.url, allow_loopback_http)
    part = dest.with_name(dest.name + ".part")
    have = part.stat().st_size if part.exists() else 0
    if have > asset.size:
        part.unlink()
        have = 0
    headers = {"Range": f"bytes={have}-"} if 0 < have < asset.size else {}
    if have < asset.size:
        try:
            with _opener(allow_loopback_http).open(_request(asset.url, headers), timeout=HTTP_TIMEOUT) as r:
                status = r.status
                if headers and status == 206 and _range_start(r.headers.get("Content-Range")) == have:
                    mode = "ab"
                else:
                    mode, have = "wb", 0
                with open(part, mode) as f:
                    while True:
                        chunk = r.read(CHUNK)
                        if not chunk:
                            break
                        if have + len(chunk) > asset.size:
                            f.close()
                            part.unlink()
                            raise UpdateError(_("The download is larger than expected, so it wasn't used."))
                        f.write(chunk)
                        have += len(chunk)
                        if progress:
                            progress(have, asset.size)
                    f.flush()
                    os.fsync(f.fileno())
        except urllib.error.HTTPError as e:
            if e.code == 416:
                # The server says the part file is already past the end: start over.
                part.unlink(missing_ok=True)
            raise UpdateError(_("The update server answered {code} for the download.").format(code=e.code)) from None
        except (urllib.error.URLError, http.client.HTTPException, OSError, TimeoutError) as e:
            # The partial file stays for the next attempt to resume.
            raise UpdateError(
                _("The download was interrupted: {error}").format(error=getattr(e, "reason", e))
            ) from None
    if have != asset.size:
        raise UpdateError(
            _("The download stopped early ({have} of {size} bytes). It will continue at the next check.").format(
                have=have, size=asset.size
            )
        )
    if sha256_file(part) != asset.sha256:
        part.unlink()
        raise UpdateError(_("The download doesn't match the official release, so it wasn't used."))
    os.replace(part, dest)
    return dest


def _range_start(content_range: str | None) -> int:
    m = re.fullmatch(r"bytes (\d+)-\d+/(\d+|\*)", (content_range or "").strip())
    return int(m[1]) if m else -1


def copy_verified(src: Path, dst_fd: int, asset: Asset) -> None:
    """Copy src into an open file, hashing exactly what is written; raise
    unless it matches the manifest. The copy is what gets installed, so a
    change to the cached file after its check can't slip through."""
    h = hashlib.sha256()
    n = 0
    with open(src, "rb") as f_in, os.fdopen(dst_fd, "wb", closefd=True) as f_out:
        while chunk := f_in.read(CHUNK):
            n += len(chunk)
            if n > asset.size:
                break
            h.update(chunk)
            f_out.write(chunk)
        f_out.flush()
        os.fsync(f_out.fileno())
    if n != asset.size or not hmac.compare_digest(h.hexdigest(), asset.sha256):
        raise UpdateError(_("The downloaded update changed after it was checked, so it wasn't installed."))


# ---------------------------------------------------------------- installing


@dataclass(frozen=True)
class InstallTarget:
    """What this running copy can replace."""

    kind: str  # "appimage", "inno" or "app-bundle"
    path: Path  # the AppImage file, the install folder, or the .app bundle


@dataclass
class InstallPlan:
    """What to start once the app has shut down."""

    command: list[str]
    description: str


def current_platform() -> str | None:
    machine = platform.machine().lower()
    if sys.platform.startswith("linux") and machine in ("x86_64", "amd64"):
        return "linux-x86_64"
    if sys.platform == "win32" and machine in ("amd64", "x86_64"):
        return "windows-x86_64"
    if sys.platform == "darwin":
        return "macos-universal"
    return None


def detect_target(
    environ: dict | None = None,
    *,
    frozen: bool | None = None,
    executable: str | None = None,
    test_source: bool = False,
) -> tuple[InstallTarget | None, str]:
    """How this copy was installed, or why it can't update itself.

    On Linux the AppImage runtime names the file in $APPIMAGE and its mount
    in $APPDIR, but it exports both to every child process too: a copy
    started from a terminal or editor that is itself an AppImage inherits
    that program's. So the file is only taken as this copy's when this is
    the bundled app (frozen) and its own executable lies inside $APPDIR.
    With the test update source (OPENARCANUM_UPDATE_TEST_*, a host on this
    computer) the frozen check is waived so tests can run from source;
    $APPDIR must still contain the interpreter."""
    env = os.environ if environ is None else environ
    frozen = bool(getattr(sys, "frozen", False)) if frozen is None else frozen
    exe = Path(executable or sys.executable).resolve()
    if sys.platform.startswith("linux"):
        not_appimage = _("This copy isn't running from an AppImage, so it can't update itself.")
        appimage, appdir = env.get("APPIMAGE", ""), env.get("APPDIR", "")
        if not (appimage and Path(appimage).is_absolute() and Path(appimage).is_file()):
            return None, not_appimage
        if not (appdir and Path(appdir).is_absolute() and exe.is_relative_to(Path(appdir).resolve())):
            # An inherited $APPIMAGE: another program's, not ours.
            return None, not_appimage
        if not (frozen or test_source):
            return None, not_appimage
        if not os.access(Path(appimage).parent, os.W_OK):
            return None, _("The AppImage's folder isn't writable, so this copy can't update itself.")
        return InstallTarget("appimage", Path(appimage)), ""
    if sys.platform == "win32":
        folder = exe.parent
        if frozen and (folder / "unins000.exe").exists():
            return InstallTarget("inno", folder), ""
        return None, _("This copy wasn't installed with the installer, so it can't update itself.")
    if sys.platform == "darwin":
        for parent in exe.parents:
            if parent.suffix == ".app":
                if frozen:
                    return InstallTarget("app-bundle", parent), ""
                break
        return None, _("This copy isn't running from an app bundle, so it can't update itself.")
    return None, _("This system can't update itself.")


def install_appimage(src: Path, asset: Asset, target: Path, args: Sequence[str]) -> InstallPlan:
    """Replace the AppImage atomically: write the verified copy next to it,
    fsync, rename over it, fsync the folder. The running process keeps the
    old file open, so it is unaffected until it restarts."""
    folder = target.parent
    fd, tmp = tempfile.mkstemp(prefix=f".{target.name}.update-", dir=folder)
    try:
        copy_verified(src, fd, asset)
        os.chmod(tmp, (target.stat().st_mode & 0o777) | 0o755)
        os.replace(tmp, target)
    except BaseException:
        Path(tmp).unlink(missing_ok=True)
        raise
    if POSIX:
        dfd = os.open(folder, os.O_RDONLY)
        try:
            os.fsync(dfd)
        finally:
            os.close(dfd)
    return InstallPlan([str(target), *args], "restart the new AppImage")


def install_windows(src: Path, asset: Asset, version: str) -> InstallPlan:
    """Copy the verified installer into a new private folder (hashing what is
    written) and run it once the app has exited; Inno Setup upgrades in place
    and restarts the app."""
    folder = Path(tempfile.mkdtemp(prefix=f"{PRODUCT_NAME.lower()}-update-"))
    exe = folder / f"{PRODUCT_NAME}-Setup-{version}.exe"
    fd = os.open(exe, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_BINARY", 0), 0o700)
    try:
        copy_verified(src, fd, asset)
    except BaseException:
        shutil.rmtree(folder, ignore_errors=True)
        raise
    return InstallPlan([str(exe), *WINDOWS_INSTALLER_FLAGS], "run the installer")


def swap_bundle(staged: Path, bundle: Path) -> None:
    """Replace an .app bundle: move the old one aside, the new one in, then
    delete the old one. If the second rename fails the old one goes back."""
    old = bundle.with_name(f".{bundle.name}.old-{secrets.token_hex(4)}")
    os.rename(bundle, old)
    try:
        os.rename(staged, bundle)
    except BaseException:
        os.rename(old, bundle)
        raise
    shutil.rmtree(old, ignore_errors=True)


def install_macos(
    src: Path,
    asset: Asset,
    bundle: Path,
    args: Sequence[str],
    run: Callable[..., subprocess.CompletedProcess] = subprocess.run,
) -> InstallPlan:
    """Mount the verified .dmg read-only, copy its .app next to the running
    bundle, swap the two, and relaunch."""
    work = Path(tempfile.mkdtemp(prefix=f"{PRODUCT_NAME.lower()}-update-"))
    try:
        dmg = work / "update.dmg"
        fd = os.open(dmg, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        copy_verified(src, fd, asset)
        mnt = work / "mnt"
        mnt.mkdir()
        run(
            ["/usr/bin/hdiutil", "attach", "-readonly", "-nobrowse", "-noautoopen", "-mountpoint", str(mnt), str(dmg)],
            check=True,
            capture_output=True,
        )
        staged = bundle.with_name(f".{bundle.name}.update-{secrets.token_hex(4)}")
        try:
            apps = [p for p in mnt.iterdir() if p.suffix == ".app" and p.is_dir() and not p.is_symlink()]
            if len(apps) != 1 or apps[0].name != bundle.name:
                raise UpdateError(_("The update disk image doesn't hold {name}.").format(name=bundle.name))
            run(["/usr/bin/ditto", str(apps[0]), str(staged)], check=True, capture_output=True)
        except BaseException:
            shutil.rmtree(staged, ignore_errors=True)
            raise
        finally:
            run(["/usr/bin/hdiutil", "detach", str(mnt), "-force"], check=False, capture_output=True)
        try:
            swap_bundle(staged, bundle)
        except BaseException:
            shutil.rmtree(staged, ignore_errors=True)
            raise
    except subprocess.CalledProcessError as e:
        raise UpdateError(
            _("Installing the update failed ({name} exited with {returncode}).").format(
                name=Path(e.cmd[0]).name, returncode=e.returncode
            )
        ) from None
    finally:
        shutil.rmtree(work, ignore_errors=True)
    return InstallPlan(["/usr/bin/open", "-n", str(bundle), "--args", *args], "relaunch the new app")


def launch(plan: InstallPlan) -> None:
    """Start the plan's command detached from this process (called as the
    app exits)."""
    kw: dict[str, Any] = {
        "stdin": subprocess.DEVNULL,
        "stdout": subprocess.DEVNULL,
        "stderr": subprocess.DEVNULL,
        "close_fds": True,
    }
    if sys.platform == "win32":
        kw["creationflags"] = subprocess.DETACHED_PROCESS | subprocess.CREATE_NEW_PROCESS_GROUP
    else:
        kw["start_new_session"] = True
    log.info("updates: %s", plan.description)
    subprocess.Popen(plan.command, **kw)


# ---------------------------------------------------------------- the updater


class Updater:
    """Checks, downloads and (with the user's consent) installs updates.
    Runs on the core's asyncio loop; network and file work go to threads."""

    def __init__(
        self,
        state_file: Path,
        cache_dir: Callable[[], Path],
        auto_check: Callable[[], bool],
        emit: Callable[[str, dict], None],
        config: UpdateConfig | None = None,
        version: str = __version__,
        platform_name: str | None = None,
        target: tuple[InstallTarget | None, str] | None = None,
        restart_args: Sequence[str] = (),
        clock: Callable[[], float] = time.time,
    ):
        self.state_file = state_file
        self.cache_dir = cache_dir
        self.auto_check = auto_check
        self.emit = emit
        self.config = config if config is not None else UpdateConfig.load()
        self.version = version
        self.platform = platform_name if platform_name is not None else current_platform()
        self.target, self.target_reason = (
            target if target is not None else detect_target(test_source=self.config.test_override)
        )
        self.restart_args = list(restart_args)
        self.clock = clock
        self.pending: InstallPlan | None = None
        self._lock = asyncio.Lock()
        self._wake = asyncio.Event()
        self._file: Path | None = None
        self._asset: Asset | None = None
        self._token = ""
        self._snoozed = ""
        self._last_check = 0.0
        self._status: dict[str, Any] = {}
        self._set("idle" if self.config.configured else "unconfigured")

    # -- state

    # The state file's format version; files without one are version 1. It
    # only remembers choices like a skipped version, so a file in any other
    # format is ignored (its choices asked again) rather than misread.
    STATE_VERSION = 1

    def _saved(self) -> dict:
        try:
            d = json.loads(self.state_file.read_text(encoding="utf-8"))
        except OSError, ValueError:
            return {}
        if not isinstance(d, dict) or d.get("v", 1) != self.STATE_VERSION:
            return {}
        return d

    def _save(self, **changes: Any) -> None:
        d = self._saved()
        d.update(changes, v=self.STATE_VERSION)
        atomic_write(self.state_file, (json.dumps(d, sort_keys=True) + "\n").encode())

    @property
    def skipped(self) -> str:
        v = self._saved().get("skipped", "")
        return v if isinstance(v, str) else ""

    def _set(self, state: str, message: str = "", **extra: Any) -> None:
        self._status = {"state": state, "message": message, **extra}
        self.emit("update", self.status())

    def status(self) -> dict:
        s = dict(self._status)
        s.update(
            current=self.version,
            configured=self.config.configured,
            test_source=self.config.test_override,
            auto=bool(self.auto_check()),
            checked_at=self._last_check,
            skipped=self.skipped,
        )
        if s["state"] == "unconfigured":
            s["message"] = _("Updates aren't set up for this version.")
        s["prompt"] = s["state"] == "ready" and s.get("version") != self._snoozed
        if s["state"] != "ready":
            s.pop("token", None)
        return s

    # -- scheduling

    async def run(self) -> None:
        """Check at startup and every CHECK_INTERVAL while automatic checks
        are on. Does nothing at all for a build without update keys."""
        if not self.config.configured:
            return
        while True:
            wait = 3600.0
            if self.auto_check():
                due = self._last_check + CHECK_INTERVAL - self.clock() if self._last_check else 0.0
                if due <= 0:
                    try:
                        await self.check(manual=False)
                    except UpdateError:
                        pass
                    except Exception:
                        log.exception("update check")
                    continue
                wait = max(1.0, min(due, wait))
            self._wake.clear()
            with contextlib.suppress(TimeoutError):
                await asyncio.wait_for(self._wake.wait(), timeout=wait)

    def wake(self) -> None:
        """The automatic-check setting changed."""
        self._wake.set()

    # -- checking

    async def check(self, manual: bool = True) -> dict:
        if not self.config.configured:
            self._set("unconfigured")
            return self.status()
        if self._lock.locked():
            return self.status()
        async with self._lock:
            self._last_check = self.clock()
            self._snoozed = ""
            self._set("checking", _("Checking for updates…"))
            try:
                await self._check(manual)
            except UpdateError as e:
                log.warning("update check refused: %s", e)
                self._set("error", str(e))
                raise
        return self.status()

    async def _check(self, manual: bool) -> None:
        cfg = self.config
        raw = await asyncio.to_thread(fetch_manifest, cfg.url, cfg.allow_loopback_http)
        m = verify_manifest(raw, cfg.keys, now=self.clock(), allow_loopback_http=cfg.allow_loopback_http)
        last = self._saved().get("released_at", 0)
        if isinstance(last, int | float) and m.released_at < last:
            raise UpdateError(
                _("The update server sent older update information than it did before, so it wasn't used.")
            )
        self._save(released_at=m.released_at)
        if not is_newer(m.version, self.version):
            if m.version == self.version:
                self._set("up_to_date", f"{PRODUCT_NAME} {self.version} is the latest version.")
                return
            raise UpdateError(
                _("The update offers {version}, which isn't newer than {current}, so it wasn't used.").format(
                    version=m.version, current=self.version
                )
            )
        if m.version == self.skipped and not manual:
            self._set("skipped", f"Version {m.version} is available (skipped).", version=m.version)
            return
        asset = m.assets.get(self.platform or "")
        if asset is None:
            raise UpdateError(_("Version {version} has no download for this system.").format(version=m.version))
        if self.target is None:
            self._set("available", f"Version {m.version} is available. {self.target_reason}", version=m.version)
            return
        self._set("downloading", f"Downloading {m.version}…", version=m.version, done=0, total=asset.size)
        path = await asyncio.to_thread(self._fetch, m.version, asset)
        self._file, self._asset = path, asset
        self._token = secrets.token_urlsafe(24)
        self._set("ready", f"Version {m.version} is ready to install.", version=m.version, token=self._token)

    def _fetch(self, version: str, asset: Asset) -> Path:
        folder = self.cache_dir()
        ensure_dir(folder)
        dest = folder / f"{PRODUCT_NAME}-{version}-{asset.platform}{EXTENSIONS[asset.kind]}"
        # Downloads of other versions are dead weight now.
        for old in folder.iterdir():
            if old.name not in (dest.name, dest.name + ".part"):
                with contextlib.suppress(OSError):
                    old.unlink()
        if dest.exists():
            if dest.stat().st_size == asset.size and sha256_file(dest) == asset.sha256:
                return dest
            dest.unlink()
        last = [0.0]

        def progress(done: int, total: int) -> None:
            now = time.monotonic()
            if now - last[0] >= 0.5 or done == total:
                last[0] = now
                self._status.update(done=done, total=total)
                self.emit("update", self.status())

        return download(asset, dest, self.config.allow_loopback_http, progress)

    # -- the user's answers

    def later(self, version: str) -> dict:
        """Don't ask again until the next check."""
        self._snoozed = str(version)
        self.emit("update", self.status())
        return self.status()

    def skip(self, version: str) -> dict:
        """Don't offer this version again (automatic checks); "Check now" still finds it."""
        try:
            version_key(version)
        except ValueError:
            raise UpdateError(_("That isn't a version.")) from None
        self._save(skipped=version)
        if self._status.get("version") == version and self._status["state"] in ("ready", "available"):
            self._set("skipped", f"Version {version} is available (skipped).", version=version)
        return self.status()

    async def install(self, version: str, token: str) -> InstallPlan:
        """The user chose "Restart and install" for `version`. Refuses unless
        that exact version was downloaded and verified, and `token` is the
        one issued with it. The copy (hashing up to the size cap, and on
        macOS mounting the image) runs in a worker thread so the app keeps
        working; it holds the check lock so no check swaps the file. If it
        fails or takes longer than INSTALL_TIMEOUT nothing is left pending:
        the update never launches without a fresh "Restart and install".
        The caller then shuts the app down and launches self.pending."""
        async with self._lock:
            s = self._status
            if s.get("state") != "ready" or self._file is None or self._asset is None or self.target is None:
                raise UpdateError(_("No verified update is ready to install."))
            if s.get("version") != version:
                raise UpdateError(_("Version {version} isn't the update that's ready.").format(version=version))
            if not isinstance(token, str) or not token or not hmac.compare_digest(token, self._token):
                raise UpdateError(_("Install updates from the update message."))
            src, asset, target = self._file, self._asset, self.target
            self._token = ""
            self.pending = None
            self._set("installing", f"Installing {version}…", version=version)
            try:
                plan = await asyncio.wait_for(
                    asyncio.to_thread(self._install_files, src, asset, target, version), INSTALL_TIMEOUT
                )
            except UpdateError as e:
                self._set("error", str(e))
                raise
            except TimeoutError:
                msg = _("Installing the update took too long; it wasn't started. Check for updates to try again.")
                self._set("error", msg)
                raise UpdateError(msg) from None
            except OSError as e:
                self._set("error", _("Installing the update failed: {reason}").format(reason=e.strerror or e))
                raise UpdateError(_("Installing the update failed: {reason}").format(reason=e.strerror or e)) from None
            self.pending = plan
            log.info("updates: installed %s (%s); restarting", version, target.kind)
            return plan

    def _install_files(self, src: Path, asset: Asset, target: InstallTarget, version: str) -> InstallPlan:
        if target.kind == "appimage":
            return install_appimage(src, asset, target.path, self.restart_args)
        if target.kind == "inno":
            return install_windows(src, asset, version)
        if target.kind == "app-bundle":
            return install_macos(src, asset, target.path, self.restart_args)
        raise UpdateError(_("This copy can't update itself."))
