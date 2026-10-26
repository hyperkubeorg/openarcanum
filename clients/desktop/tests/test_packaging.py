"""The packaging inputs (clients/desktop/packaging/, docs/packaging.md) register the
openarcanum:// scheme on every OS, and the AppImage tools are only used when
their hashes match. The built artifacts themselves are tested in
test_bundle.py."""

import importlib.util
import io
import plistlib
import re
import struct
import sys
from pathlib import Path

import pytest

PACKAGING = Path(__file__).resolve().parents[1] / "packaging"


def load(name: str, path: Path):
    spec = importlib.util.spec_from_file_location(name, path)
    mod = importlib.util.module_from_spec(spec)
    sys.modules[name] = mod
    spec.loader.exec_module(mod)
    return mod


build_appimage = load("build_appimage", PACKAGING / "linux" / "build_appimage.py")
icons = load("icons", PACKAGING / "icons.py")


def test_desktop_file_registers_the_scheme():
    lines = (PACKAGING / "linux" / "openarcanum.desktop").read_text().splitlines()
    entry = dict(line.split("=", 1) for line in lines if "=" in line)
    assert entry["MimeType"].split(";")[0] == "x-scheme-handler/openarcanum"
    # %u passes the opened link as the argument openarcanum/app.py takes.
    assert entry["Exec"].split() == ["OpenArcanum", "--", "%u"]  # the link after --: it can never be an option
    assert entry["Icon"] == "openarcanum" and entry["StartupWMClass"] == "openarcanum"


def test_inno_setup_registers_the_scheme_and_restarts_after_upgrade():
    iss = (PACKAGING / "windows" / "openarcanum.iss").read_text()
    assert 'Subkey: "Software\\Classes\\openarcanum"; ValueType: string; ValueName: "URL Protocol"' in iss
    # `--` first: whatever the link holds (Windows substitutes %1 without
    # escaping) can't be read as an option.
    assert 'ValueData: """{app}\\{#AppExe}"" -- ""%1"""' in iss
    assert "uninsdeletekey" in iss
    assert "PrivilegesRequired=lowest" in iss
    assert "Check: SilentUpgrade" in iss
    # openarcanum/updates.py names the installer it downloads this way, and looks
    # for Inno's unins000.exe next to the app.
    assert "OutputBaseFilename={#AppName}-Setup-{#AppVersion}" in iss


def test_macos_spec_and_entitlements():
    spec = (PACKAGING / "openarcanum.spec").read_text()
    for key in (
        "NSMicrophoneUsageDescription",
        "NSCameraUsageDescription",
        "NSScreenCaptureUsageDescription",
        '"CFBundleURLSchemes": ["openarcanum"]',
    ):
        assert key in spec
    ent = plistlib.loads((PACKAGING / "macos" / "entitlements.plist").read_bytes())
    # No disable-library-validation: every Mach-O is re-signed with the app's
    # Team ID, so a dylib planted in the bundle won't load.
    build = (PACKAGING / "macos" / "build_dmg.sh").read_text()
    assert "disable-library-validation" in build and "TeamIdentifier" in build
    assert ent == {
        "com.apple.security.cs.allow-jit": True,
        "com.apple.security.cs.allow-unsigned-executable-memory": True,
        "com.apple.security.device.audio-input": True,
        "com.apple.security.device.camera": True,
    }


def test_appdir_layout(tmp_path):
    bundle = tmp_path / "OpenArcanum"
    (bundle / "_internal").mkdir(parents=True)
    (bundle / "OpenArcanum").write_text("exe")
    appdir = tmp_path / "AppDir"
    build_appimage.make_appdir(bundle, appdir)
    assert (appdir / "usr/lib/openarcanum/OpenArcanum").read_text() == "exe"
    assert (appdir / "AppRun").stat().st_mode & 0o111
    assert 'exec "$HERE/usr/lib/openarcanum/OpenArcanum" "$@"' in (appdir / "AppRun").read_text()
    assert (appdir / "openarcanum.desktop").read_text() == (PACKAGING / "linux" / "openarcanum.desktop").read_text()
    assert (appdir / "usr/share/applications/openarcanum.desktop").exists()
    assert (appdir / "openarcanum.png").exists() and (appdir / ".DirIcon").is_symlink()
    hicolor = appdir / "usr/share/icons/hicolor"
    for n in icons.HICOLOR_SIZES:
        assert icons.png_size((hicolor / f"{n}x{n}/apps/openarcanum.png").read_bytes()) == (n, n)
    assert (hicolor / "scalable/apps/openarcanum.svg").read_bytes() == icons.MASTER.read_bytes()


def test_appdir_needs_a_bundle(tmp_path):
    with pytest.raises(SystemExit):
        build_appimage.make_appdir(tmp_path / "missing", tmp_path / "AppDir")


def test_tools_with_the_wrong_hash_are_refused(tmp_path, monkeypatch):
    monkeypatch.setattr(build_appimage.urllib.request, "urlopen", lambda url, timeout: io.BytesIO(b"tampered"))
    with pytest.raises(SystemExit, match="SHA-256 mismatch"):
        build_appimage.fetch_tool(tmp_path, "runtime-x86_64")
    assert list(tmp_path.iterdir()) == []
    # A cached file that doesn't match is fetched again (and refused again).
    (tmp_path / "runtime-x86_64").write_bytes(b"old")
    with pytest.raises(SystemExit):
        build_appimage.fetch_tool(tmp_path, "runtime-x86_64")


def test_tools_are_pinned():
    for url, digest in build_appimage.TOOLS.values():
        assert "/continuous/" not in url and "/latest/" not in url
        assert len(digest) == 64 and int(digest, 16) >= 0


def test_icons_ico_and_icns_hold_every_size(tmp_path):
    icons.build(tmp_path)
    ico = (tmp_path / "openarcanum.ico").read_bytes()
    reserved, kind, count = struct.unpack("<HHH", ico[:6])
    assert (reserved, kind, count) == (0, 1, len(icons.ICO_SIZES))
    for i, n in enumerate(icons.ICO_SIZES):
        w, h, *_rest, size, offset = struct.unpack("<BBBBHHII", ico[6 + 16 * i : 22 + 16 * i])
        assert (w, h) == (n % 256, n % 256)
        assert icons.png_size(ico[offset : offset + size]) == (n, n)
    icns = (tmp_path / "openarcanum.icns").read_bytes()
    assert icns[:4] == b"icns" and struct.unpack(">I", icns[4:8])[0] == len(icns)
    pos, kinds = 8, []
    while pos < len(icns):
        kind, length = icns[pos : pos + 4], struct.unpack(">I", icns[pos + 4 : pos + 8])[0]
        kinds.append(kind)
        n = icons.png_size(icns[pos + 8 : pos + length])[0]
        assert icons.ICNS_TYPES[n] == kind
        pos += length
    assert kinds == [icons.ICNS_TYPES[n] for n in sorted(icons.ICNS_TYPES)]
    for n in icons.HICOLOR_SIZES:
        assert icons.png_size((tmp_path / "hicolor" / f"{n}x{n}" / "apps" / "openarcanum.png").read_bytes()) == (n, n)
    with pytest.raises(ValueError):
        icons.icns([b"not a png at all, clearly"])
    with pytest.raises(ValueError):
        icons.ico([icons.icon_png(512)])


def test_shipped_app_icons_match_the_svg_master():
    """brand/*.png are rendered from brand/openarcanum.svg (python
    packaging/icons.py --brand); the same pixels on every machine."""
    assert icons.stale_brand() == []


def test_app_icon_render():
    """Opaque inside the tile, transparent outside it; the alert variant adds
    the red dot top right with a transparent ring around it."""
    n = 64
    plain, alert = icons.render(n), icons.render(n, alert=True)

    def px(img, x, y):
        i = (y * n + x) * 4
        return tuple(img[i : i + 4])

    assert px(plain, 0, 0)[3] == 0 and px(plain, n // 2, n - 8)[3] == 255
    assert px(plain, 8, n // 2)[3] == 255  # the tile, left of the bubble
    cx, cy, _r = (v * n / 256 for v in icons.ALERT_DOT)
    r, g, b, a = px(alert, int(cx), int(cy))
    assert a == 255 and (r, g, b) == icons.ALERT_COLOR
    assert px(plain, int(cx), int(cy)) != px(alert, int(cx), int(cy))
    # Away from the dot the two are the same picture.
    assert px(plain, 10, n - 12) == px(alert, 10, n - 12)


def test_master_subset_is_enforced(tmp_path):
    bad = tmp_path / "bad.svg"
    bad.write_text('<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><path d="M0 0"/></svg>')
    with pytest.raises(ValueError, match="path"):
        icons.render(16, master=bad)


def test_release_signing_key_is_confined_to_one_reviewed_step():
    """release.yml: the manifest job runs in the "release" environment (its
    secret, behind required reviewers); RELEASE_SIGNING_KEY is set on the one
    signing step only; every third-party action is pinned by commit; the
    manifest is then checked against the client's built-in keys."""
    wf = (Path(__file__).resolve().parents[3] / ".github" / "workflows" / "release.yml").read_text()
    lines = wf.splitlines()
    for ln in lines:
        m = re.match(r"\s*-?\s*uses:\s*(\S+)", ln)
        if m and not m.group(1).startswith("./"):
            assert re.fullmatch(r"[\w.-]+/[\w.-]+@[0-9a-f]{40}", m.group(1)), ln
    secret = [i for i, ln in enumerate(lines) if "secrets.RELEASE_SIGNING_KEY" in ln]
    assert len(secret) == 1
    i = secret[0]
    # Inside a step's env: (10 spaces deep), right under that step's "env:".
    assert lines[i].startswith(" " * 10 + "RELEASE_SIGNING_KEY:")
    assert lines[i - 1].strip() == "env:" and lines[i - 2].strip().startswith("- name: Sign")
    job = wf[wf.index("\n  manifest:\n") :]
    assert "\n    environment: release\n" in job
    sign, check = job.index("release_manifest.py sign"), job.index("release_manifest.py verify --builtin")
    assert sign < check
    assert "uv run --frozen --no-sync" in job[job.index("- name: Sign") : sign]


WORKFLOWS = Path(__file__).resolve().parents[3] / ".github" / "workflows"


def test_release_workflows_pin_actions_and_only_draft():
    """release.yml and containers.yml: every third-party action pinned by
    commit (with its version in a comment); the release is only ever a
    draft; images are signed by digest; the site image is built only when
    the Containerfile has its target."""
    for name in ("release.yml", "containers.yml"):
        for ln in (WORKFLOWS / name).read_text().splitlines():
            m = re.match(r"\s*-?\s*uses:\s*(\S+)(.*)", ln)
            if m and not m.group(1).startswith("./"):
                assert re.fullmatch(r"[\w.-]+/[\w.-]+@[0-9a-f]{40}", m.group(1)), ln
                assert re.fullmatch(r" # v\d+\.\d+\.\d+", m.group(2)), ln
    rel = (WORKFLOWS / "release.yml").read_text()
    assert "gh release create" in rel and "--draft" in rel and "gh release edit" in rel
    assert "--verify-tag" in rel and "publish" not in rel.split("gh release create")[1].split("\n")[0]
    assert "scripts/version info --tag" in rel
    con = (WORKFLOWS / "containers.yml").read_text()
    assert "linux/amd64,linux/arm64" in con and "provenance: mode=max" in con and "sbom: true" in con
    assert 'cosign sign --yes "${IMAGE}@${DIGEST}"' in con
    assert "target: site" in con and "steps.target.outputs.present == 'true'" in con
    for image, target in (("relay", "relay"), ("maintainer", "meta"), ("community", "community")):
        assert f"image: {image}\n            target: {target}\n" in con


licenses = load("licenses", PACKAGING / "licenses.py")


def test_licences_collected_for_the_bundle(tmp_path):
    """packaging/licenses.py (make bundle-licenses) lists every runtime
    distribution with its licence, copies the files its wheel carries, and
    points the Qt wheels (which carry none) at LICENSES/."""
    req = tmp_path / "req.txt"
    req.write_text(
        "# uv export\ncryptography==50.0.2\n    # via openarcanum\npyside6==6.12.0\n"
        "no-such-dist==1.0 ; sys_platform == 'win32'\n"
    )
    names = licenses.runtime_names(req)
    assert names == ["cryptography", "no-such-dist", "pyside6"]
    index: list[str] = []
    licenses.collect_python(tmp_path / "out", names, index)
    by_name = {line.split("  ")[1]: line for line in index}
    assert "no-such-dist" not in by_name
    assert "python/cryptography/" in by_name["cryptography"]
    assert any((tmp_path / "out" / "python" / "cryptography").rglob("LICENSE*"))
    assert "LGPL-3.0-only" in by_name["PySide6"] and "LICENSES/LGPL-3.0-only.txt" in by_name["PySide6"]
    assert "CPython" in by_name
    for text in ("LGPL-3.0-only.txt", "GPL-3.0-only.txt", "CC-BY-3.0-US.txt"):
        assert (licenses.REPO / "LICENSES" / text).is_file()
    agpl = (licenses.REPO / "LICENSE").read_text()
    assert "GNU AFFERO GENERAL PUBLIC LICENSE" in agpl and "Version 3, 19 November 2007" in agpl
