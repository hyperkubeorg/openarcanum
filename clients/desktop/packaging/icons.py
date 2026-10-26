"""OpenArcanum's app icon: every PNG, the Windows .ico and the macOS .icns,
rendered from the SVG master (clients/shared/ui/brand/openarcanum.svg)
with the standard library only, so the output is the same on every OS and
build machine.

    python packaging/icons.py OUT_DIR      # build: .ico, .icns, Linux icon theme PNGs
    python packaging/icons.py --brand      # rewrite the PNGs the app itself ships
    python packaging/icons.py --check      # fail if those PNGs are stale

The master uses a small SVG subset, which is all an icon made of flat
shapes needs: one or more <linearGradient> (userSpaceOnUse, two or more
stops), <rect> (with rx), <polygon> and <circle>, filled with a colour or a
gradient (fill-opacity allowed). Anything else in it is an error rather than
silently dropped. Shapes are rasterised with exact horizontal coverage and
16 sub-scanlines per pixel, then composited in order.

The app ships (brand/, committed, checked by tests/test_packaging.py):
openarcanum-N.png for the window, the taskbar and the About page, and
openarcanum-alert-N.png, the tray icon with a red dot for something waiting
(openarcanum/tray.py). The dot is drawn here, not in the master: it is the
same for every icon.
"""

from __future__ import annotations

import math
import re
import struct
import sys
import xml.etree.ElementTree as ET
import zlib
from pathlib import Path

BRAND = Path(__file__).resolve().parents[2] / "shared" / "ui" / "brand"
MASTER = BRAND / "openarcanum.svg"
# What the app ships: the window/taskbar icon sizes, and the tray's.
APP_SIZES = (16, 24, 32, 48, 64, 128, 256)
ALERT_SIZES = (16, 22, 24, 32, 48, 64)
ICO_SIZES = (16, 24, 32, 48, 64, 128, 256)
# icns element types that hold a PNG of the given pixel size.
ICNS_TYPES = {16: b"icp4", 32: b"icp5", 64: b"icp6", 128: b"ic07", 256: b"ic08", 512: b"ic09", 1024: b"ic10"}
# The Linux icon theme (hicolor) sizes the AppImage installs.
HICOLOR_SIZES = (16, 24, 32, 48, 64, 128, 256, 512)
# The tray icon's "something is waiting" dot: a red disc with a ring in the
# tray's own colour cut out around it, top right, in master units.
ALERT_DOT = (204.0, 52.0, 44.0)
ALERT_RING = 14.0
ALERT_COLOR = (242, 63, 67)
SUBSAMPLES = 16
SVG_NS = "{http://www.w3.org/2000/svg}"


def png_size(data: bytes) -> tuple[int, int]:
    if data[:8] != b"\x89PNG\r\n\x1a\n" or data[12:16] != b"IHDR":
        raise ValueError("not a PNG")
    return struct.unpack(">II", data[16:24])


# ---------------------------------------------------------------- the SVG subset


def _num(el: ET.Element, name: str, default: float | None = None) -> float:
    v = el.get(name)
    if v is None:
        if default is None:
            raise ValueError(f"<{el.tag.removeprefix(SVG_NS)}> needs {name}")
        return default
    return float(v)


def _color(s: str) -> tuple[int, int, int]:
    m = re.fullmatch(r"#([0-9a-fA-F]{6})", s.strip())
    if not m:
        raise ValueError(f"colours are #rrggbb, not {s!r}")
    h = m.group(1)
    return int(h[0:2], 16), int(h[2:4], 16), int(h[4:6], 16)


class Gradient:
    def __init__(self, el: ET.Element):
        if el.get("gradientUnits") != "userSpaceOnUse":
            raise ValueError("gradients must use gradientUnits=userSpaceOnUse")
        self.p1 = (_num(el, "x1"), _num(el, "y1"))
        self.p2 = (_num(el, "x2"), _num(el, "y2"))
        self.stops = [(float(s.get("offset", "0")), _color(s.get("stop-color", ""))) for s in el]
        if len(self.stops) < 2:
            raise ValueError("a gradient needs two stops")

    def at(self, x: float, y: float) -> tuple[float, float, float]:
        (x1, y1), (x2, y2) = self.p1, self.p2
        dx, dy = x2 - x1, y2 - y1
        t = ((x - x1) * dx + (y - y1) * dy) / (dx * dx + dy * dy)
        t = min(max(t, 0.0), 1.0)
        prev = self.stops[0]
        for stop in self.stops[1:]:
            if t <= stop[0]:
                span = stop[0] - prev[0]
                f = 0.0 if span <= 0 else (t - prev[0]) / span
                return tuple(a + (b - a) * f for a, b in zip(prev[1], stop[1], strict=True))
            prev = stop
        return tuple(float(c) for c in self.stops[-1][1])


def _arc(cx: float, cy: float, r: float, a0: float, a1: float, steps: int) -> list[tuple[float, float]]:
    return [
        (cx + r * math.cos(a0 + (a1 - a0) * i / steps), cy + r * math.sin(a0 + (a1 - a0) * i / steps))
        for i in range(steps + 1)
    ]


def _circle(cx: float, cy: float, r: float) -> list[tuple[float, float]]:
    return _arc(cx, cy, r, 0.0, 2 * math.pi, 96)[:-1]


def _rect(x: float, y: float, w: float, h: float, rx: float) -> list[tuple[float, float]]:
    rx = min(rx, w / 2, h / 2)
    if rx <= 0:
        return [(x, y), (x + w, y), (x + w, y + h), (x, y + h)]
    q = math.pi / 2
    return (
        _arc(x + w - rx, y + rx, rx, -q, 0, 24)
        + _arc(x + w - rx, y + h - rx, rx, 0, q, 24)
        + _arc(x + rx, y + h - rx, rx, q, 2 * q, 24)
        + _arc(x + rx, y + rx, rx, 2 * q, 3 * q, 24)
    )


def load_master(path: Path = MASTER) -> tuple[float, list[tuple[list[tuple[float, float]], object, float]]]:
    """The master's viewBox size and its shapes, in order: (points, paint,
    opacity), paint being an (r, g, b) colour or a Gradient."""
    root = ET.fromstring(path.read_text(encoding="utf-8"))
    vb = [float(v) for v in root.get("viewBox", "").split()]
    if len(vb) != 4 or vb[0] or vb[1] or vb[2] != vb[3]:
        raise ValueError("the master needs a square viewBox from 0 0")
    grads: dict[str, Gradient] = {}
    shapes = []
    for el in root.iter():
        tag = el.tag.removeprefix(SVG_NS)
        if el is root or tag in ("defs", "stop"):
            continue
        if tag == "linearGradient":
            grads[el.get("id", "")] = Gradient(el)
            continue
        if tag == "rect":
            pts = _rect(_num(el, "x", 0), _num(el, "y", 0), _num(el, "width"), _num(el, "height"), _num(el, "rx", 0))
        elif tag == "polygon":
            nums = [float(v) for v in re.split(r"[\s,]+", el.get("points", "").strip()) if v]
            if len(nums) < 6 or len(nums) % 2:
                raise ValueError("a polygon needs three or more points")
            pts = list(zip(nums[0::2], nums[1::2], strict=True))
        elif tag == "circle":
            pts = _circle(_num(el, "cx"), _num(el, "cy"), _num(el, "r"))
        else:
            raise ValueError(f"<{tag}> is not in the subset icons.py renders")
        fill = el.get("fill", "")
        m = re.fullmatch(r"url\(#([\w-]+)\)", fill)
        paint = grads[m.group(1)] if m else _color(fill)
        shapes.append((pts, paint, float(el.get("fill-opacity", "1"))))
    return vb[2], shapes


# ---------------------------------------------------------------- rasterising


def coverage(pts: list[tuple[float, float]], size: int) -> dict[int, list[float]]:
    """Per pixel row, the fraction of each pixel the polygon covers (nonzero
    winding): exact across, SUBSAMPLES sub-scanlines down."""
    edges = []
    n = len(pts)
    for i in range(n):
        (x0, y0), (x1, y1) = pts[i], pts[(i + 1) % n]
        if y0 != y1:
            edges.append((x0, y0, x1, y1, 1 if y1 > y0 else -1))
    ys = [p[1] for p in pts]
    top = max(0, math.floor(min(ys)))
    bottom = min(size - 1, math.ceil(max(ys)))
    w = 1.0 / SUBSAMPLES
    rows: dict[int, list[float]] = {}
    for row in range(top, bottom + 1):
        acc = [0.0] * (size + 1)
        full = [0.0] * (size + 1)
        hit = False
        for s in range(SUBSAMPLES):
            y = row + (s + 0.5) * w
            xs = []
            for x0, y0, x1, y1, d in edges:
                if (y0 <= y < y1) or (y1 <= y < y0):
                    xs.append((x0 + (y - y0) * (x1 - x0) / (y1 - y0), d))
            if not xs:
                continue
            xs.sort()
            wind = 0
            for k, (x, d) in enumerate(xs[:-1]):
                wind += d
                if wind == 0:
                    continue
                a, b = max(x, 0.0), min(xs[k + 1][0], float(size))
                if b <= a:
                    continue
                hit = True
                ia, ib = int(a), int(b)
                if ia == ib:
                    acc[ia] += (b - a) * w
                    continue
                acc[ia] += (ia + 1 - a) * w
                full[ia + 1] += w
                full[ib] -= w
                if ib < size:
                    acc[ib] += (b - ib) * w
        if not hit:
            continue
        run = 0.0
        out = [0.0] * size
        for x in range(size):
            run += full[x]
            out[x] = min(1.0, acc[x] + run)
        rows[row] = out
    return rows


def render(size: int, alert: bool = False, master: Path = MASTER) -> bytes:
    """The icon at size x size pixels as straight-alpha RGBA bytes."""
    view, shapes = load_master(master)
    k = size / view
    px = [[0.0, 0.0, 0.0, 0.0] for _ in range(size * size)]  # premultiplied

    def paint(pts, color, opacity, erase=False):
        scaled = [(x * k, y * k) for x, y in pts]
        for row, cov in coverage(scaled, size).items():
            base = row * size
            for x, c in enumerate(cov):
                if c <= 0.0:
                    continue
                p = px[base + x]
                if erase:  # cut out: what's below loses alpha
                    keep = 1.0 - c
                    p[0] *= keep
                    p[1] *= keep
                    p[2] *= keep
                    p[3] *= keep
                    continue
                r, g, b = color.at((x + 0.5) / k, (row + 0.5) / k) if isinstance(color, Gradient) else color
                a = c * opacity
                inv = 1.0 - a
                p[0] = r * a + p[0] * inv
                p[1] = g * a + p[1] * inv
                p[2] = b * a + p[2] * inv
                p[3] = 255.0 * a + p[3] * inv

    for pts, color, opacity in shapes:
        paint(pts, color, opacity)
    if alert:
        cx, cy, r = ALERT_DOT
        paint(_circle(cx, cy, r + ALERT_RING), None, 1.0, erase=True)
        paint(_circle(cx, cy, r), ALERT_COLOR, 1.0)
    out = bytearray()
    for r, g, b, a in px:
        if a <= 0.0:
            out += b"\0\0\0\0"
            continue
        f = 255.0 / a
        out += bytes((_byte(r * f), _byte(g * f), _byte(b * f), _byte(a)))
    return bytes(out)


def _byte(v: float) -> int:
    return max(0, min(255, int(v + 0.5)))


def png(rgba: bytes, size: int) -> bytes:
    """An 8-bit RGBA PNG (no filtering; zlib level 9)."""
    stride = size * 4
    raw = b"".join(b"\0" + rgba[y * stride : (y + 1) * stride] for y in range(size))

    def chunk(kind: bytes, data: bytes) -> bytes:
        return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data))

    ihdr = struct.pack(">IIBBBBB", size, size, 8, 6, 0, 0, 0)
    return b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", ihdr) + chunk(b"IDAT", zlib.compress(raw, 9)) + chunk(b"IEND", b"")


def png_pixels(data: bytes) -> bytes:
    """The RGBA pixels of a PNG this module wrote (for comparing renders
    without depending on the zlib build's exact output)."""
    w, h = png_size(data)
    pos, idat = 8, b""
    while pos < len(data):
        n = struct.unpack(">I", data[pos : pos + 4])[0]
        kind = data[pos + 4 : pos + 8]
        if kind == b"IDAT":
            idat += data[pos + 8 : pos + 8 + n]
        pos += 12 + n
    raw = zlib.decompress(idat)
    stride = w * 4
    rows = [raw[y * (stride + 1) : (y + 1) * (stride + 1)] for y in range(h)]
    if any(r[0] != 0 for r in rows):
        raise ValueError("only unfiltered PNGs are read")
    return b"".join(r[1:] for r in rows)


def icon_png(size: int, alert: bool = False) -> bytes:
    return png(render(size, alert), size)


# ---------------------------------------------------------------- containers


def ico(pngs: list[bytes]) -> bytes:
    """PNG-compressed images (Windows Vista and later read these), one per
    size, smallest first."""
    count = len(pngs)
    header = struct.pack("<HHH", 0, 1, count)
    entries, offset = b"", 6 + 16 * count
    for data in pngs:
        w, h = png_size(data)
        if w > 256 or h > 256:
            raise ValueError("an .ico image is at most 256x256")
        entries += struct.pack("<BBBBHHII", w % 256, h % 256, 0, 0, 1, 32, len(data), offset)
        offset += len(data)
    return header + entries + b"".join(pngs)


def icns(pngs: list[bytes]) -> bytes:
    body = b""
    for data in pngs:
        w, h = png_size(data)
        if w != h or w not in ICNS_TYPES:
            raise ValueError(f"no icns slot for a {w}x{h} PNG")
        body += ICNS_TYPES[w] + struct.pack(">I", 8 + len(data)) + data
    return b"icns" + struct.pack(">I", 8 + len(body)) + body


# ---------------------------------------------------------------- outputs


def brand_files() -> dict[str, bytes]:
    """The PNGs the app ships next to the master, by file name."""
    out = {f"openarcanum-{n}.png": icon_png(n) for n in APP_SIZES}
    out |= {f"openarcanum-alert-{n}.png": icon_png(n, alert=True) for n in ALERT_SIZES}
    return out


def build(out: Path) -> None:
    """Packaging: openarcanum.ico, openarcanum.icns and hicolor/NxN/apps/openarcanum.png."""
    out.mkdir(parents=True, exist_ok=True)
    rendered = {n: icon_png(n) for n in sorted({*ICO_SIZES, *ICNS_TYPES, *HICOLOR_SIZES})}
    (out / "openarcanum.ico").write_bytes(ico([rendered[n] for n in ICO_SIZES]))
    (out / "openarcanum.icns").write_bytes(icns([rendered[n] for n in sorted(ICNS_TYPES)]))
    for n in HICOLOR_SIZES:
        d = out / "hicolor" / f"{n}x{n}" / "apps"
        d.mkdir(parents=True, exist_ok=True)
        (d / "openarcanum.png").write_bytes(rendered[n])


def write_brand() -> None:
    for name, data in brand_files().items():
        (BRAND / name).write_bytes(data)


def stale_brand() -> list[str]:
    """Shipped PNGs that are missing or don't match the master."""
    bad = []
    for name, data in brand_files().items():
        f = BRAND / name
        if not f.exists() or png_pixels(f.read_bytes()) != png_pixels(data):
            bad.append(name)
    return bad


def main(argv: list[str]) -> int:
    if argv == ["--brand"]:
        write_brand()
        return 0
    if argv == ["--check"]:
        bad = stale_brand()
        if bad:
            print(f"stale app icons (run: python packaging/icons.py --brand): {', '.join(bad)}", file=sys.stderr)
            return 1
        return 0
    if len(argv) == 1 and not argv[0].startswith("-"):
        build(Path(argv[0]))
        return 0
    print(__doc__, file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
