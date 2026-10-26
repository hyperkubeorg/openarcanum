"""A local update host for the auto-update tests: serves a signed release
manifest and downloads over loopback HTTP, with byte ranges, redirects and
interrupted transfers on demand. Records every request."""

from __future__ import annotations

import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

from openarcanum import keys, updates


class UpdateServer:
    def __init__(self):
        self.files: dict[str, bytes] = {}
        self.redirects: dict[str, str] = {}
        # Path -> bytes to send before dropping the connection (once).
        self.cut_after: dict[str, int] = {}
        self.ranges = True
        self.requests: list[tuple[str, str]] = []  # (path, Range header)
        server = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *a):
                pass

            def do_GET(self):
                rng = self.headers.get("Range", "")
                server.requests.append((self.path, rng))
                if self.path in server.redirects:
                    self.send_response(302)
                    self.send_header("Location", server.redirects[self.path])
                    self.send_header("Content-Length", "0")
                    self.end_headers()
                    return
                data = server.files.get(self.path)
                if data is None:
                    self.send_error(404)
                    return
                start = 0
                if rng and server.ranges and rng.startswith("bytes=") and rng.endswith("-"):
                    start = int(rng[6:-1])
                    if start >= len(data):
                        self.send_error(416)
                        return
                    self.send_response(206)
                    self.send_header("Content-Range", f"bytes {start}-{len(data) - 1}/{len(data)}")
                else:
                    self.send_response(200)
                body = data[start:]
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                cut = server.cut_after.pop(self.path, None)
                if cut is not None:
                    self.wfile.write(body[:cut])
                    self.wfile.flush()
                    self.close_connection = True
                    return
                self.wfile.write(body)

        self.httpd = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.port = self.httpd.server_address[1]
        self.thread = threading.Thread(target=self.httpd.serve_forever, daemon=True)

    def url(self, path: str) -> str:
        return f"http://127.0.0.1:{self.port}{path}"

    def __enter__(self) -> UpdateServer:
        self.thread.start()
        return self

    def __exit__(self, *exc) -> None:
        self.httpd.shutdown()
        self.httpd.server_close()

    def paths(self) -> list[str]:
        return [p for p, _ in self.requests]


def asset_for(server: UpdateServer, path: str, data: bytes, platform: str = "linux-x86_64") -> updates.Asset:
    """Serve `data` at `path` and describe it as the manifest would."""
    import hashlib

    server.files[path] = data
    return updates.Asset(
        platform, server.url(path), hashlib.sha256(data).hexdigest(), len(data), updates.PLATFORM_KINDS[platform]
    )


def publish(
    server: UpdateServer,
    signers: list[keys.SigningKey],
    version: str,
    assets: list[updates.Asset],
    released: float | None = None,
    expires_in: float = 30 * 86400,
    product: str | None = None,
    path: str = "/manifest.json",
) -> bytes:
    released = time.time() - 60 if released is None else released
    payload = updates.build_payload(
        version, {a.platform: a for a in assets}, released, released + expires_in, product or updates.PRODUCT_NAME
    )
    raw = updates.sign_envelope(payload, signers)
    server.files[path] = raw
    return raw


def fake_appimage(path: Path, marker: Path, label: str, watch: Path | None = None) -> bytes:
    """A shell script standing in for an AppImage: when started, it writes
    its label and arguments to `marker`, then (with `watch`) whether that
    file existed as it started."""
    data = f'#!/bin/sh\nprintf "%s\\n" "{label}" "$@" > "{marker}"\n'
    if watch is not None:
        found = 'echo "watched file present"; else echo "watched file absent"'
        data += f'if [ -e "{watch}" ]; then {found}; fi >> "{marker}"\n'
    data = data.encode()
    if path is not None:
        path.write_bytes(data)
        path.chmod(0o755)
    return data
