"""The bundled QtWebEngine supports what the call media path needs, with
Chromium's fake media devices, which the automated call tests rely on."""

import json
import os
import subprocess
import sys
from pathlib import Path

import pytest
from conftest import ENGINE_TEST_FLAGS, headless

# Raw host addresses as ICE candidates: the engine hides them behind mDNS
# names by default, which the loopback connection on a runner without
# multicast can't resolve.
FLAGS = (
    "--use-fake-device-for-media-stream --use-fake-ui-for-media-stream --auto-select-desktop-capture-source=Entire"
    " --disable-features=WebRtcHideLocalIpsWithMdns"
)


@pytest.mark.gui
def test_engine_supports_the_call_media_path():
    probe = Path(__file__).parent / "webengine" / "probe.py"
    cmd = headless([sys.executable, str(probe)])
    env = dict(os.environ, QTWEBENGINE_CHROMIUM_FLAGS=f"{FLAGS} {ENGINE_TEST_FLAGS}")
    out = subprocess.run(cmd, capture_output=True, text=True, timeout=90, env=env).stdout
    line = next(ln for ln in reversed(out.splitlines()) if ln.startswith("{"))
    r = json.loads(line)
    assert not r.get("timeout") and not r.get("err"), r
    assert r["hasGUM"] and r["hasGDM"] and r["RTCPeerConnection"] == "function"
    audio = next(t for t in r["gum"] if t.startswith("audio:"))
    for setting in ("echoCancellation", "noiseSuppression", "autoGainControl"):
        assert f'"{setting}":true' in audio, setting
    video = next(t for t in r["gum"] if t.startswith("video:"))
    assert '"width":1280' in video and '"height":720' in video
    # SFrame goes in through encoded transforms on both ends.
    assert r["createEncodedStreams"] == "function" and r["sendEncoded"] == "ok" and r["recvEncoded"] == "ok"
    why = {k: r.get(k) for k in ("framesThroughTransform", "ice", "pair", "encoded", "statsErr")}
    assert r["framesThroughTransform"] > 50, why
    assert "audio/opus" in r["audioCodecs"] and "video/VP8" in r["codecs"]
    # Screen share, with system audio where the platform offers it.
    assert any(t.startswith("video:") for t in r["gdm"]), r.get("gdmErr")
