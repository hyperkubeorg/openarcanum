"""Zoom and pan on a stream (voice.js streamZoom), with real mouse input: the
wheel zooms in where the pointer is, a drag pans, a click doesn't refocus
while zoomed in; the minimap shows only while zoomed in and moves when
dragged; a double-click puts the whole picture back. The same tiles show
a conversation's call (test_ui_dm_call_layout.py zooms there too)."""

import pytest
from conftest import network, run
from test_ui_voice import create_voice_channel, join_voice, media_env, tone  # noqa: F401 (tone is a fixture)
from uidriver import App
from uiflows import claim, create_profile

pytestmark = pytest.mark.gui

TILE = "document.querySelector('.stage .video-tile[data-kind=camera]')"


def box(el: str) -> str:
    """JS for an element's centre and size: [x, y, width, height]."""
    return (
        f"(() => {{ const r = {el}.getBoundingClientRect();"
        " return [r.x + r.width / 2, r.y + r.height / 2, r.width, r.height]; })()"
    )


async def mouse(app: App, kind: str, x: float, y: float, **extra) -> None:
    await app.send("Input.dispatchMouseEvent", type=kind, x=x, y=y, **extra)


async def zoom_pan_minimap(app: App, tile: str) -> None:
    """The checks, on whichever tile `tile` (a JS expression) finds."""
    cx, cy, w, h = await app.js(box(tile))
    transform = f"{tile}.querySelector('video').style.transform"
    minimap = f"{tile}.querySelector('.zoom-map')"
    assert await app.js(f"getComputedStyle({minimap}).display") == "none"
    focused = await app.js(f"{tile}.classList.contains('focused')")
    # The wheel, up: zoomed in, the minimap shows.
    for _ in range(4):
        await mouse(app, "mouseWheel", cx, cy, deltaX=0, deltaY=-300)
    await app.wait_js(f"{tile}.classList.contains('zoomed')", "zoomed in")
    assert "scale(" in await app.js(transform)
    assert await app.js(f"getComputedStyle({minimap}).display") == "block"
    # A drag pans (and is no click: the focus stays as it was).
    before = await app.js(transform)
    await mouse(app, "mousePressed", cx, cy, button="left", buttons=1, clickCount=1)
    await mouse(app, "mouseMoved", cx - w / 6, cy - h / 6, button="left", buttons=1)
    await mouse(app, "mouseReleased", cx - w / 6, cy - h / 6, button="left", buttons=0, clickCount=1)
    assert await app.js(transform) != before
    assert await app.js(f"{tile}.classList.contains('focused')") == focused
    # The minimap moves when dragged.
    mx, my, _w, _h = await app.js(box(minimap))
    right = await app.js(f"{minimap}.style.right")
    await mouse(app, "mousePressed", mx, my, button="left", buttons=1, clickCount=1)
    await mouse(app, "mouseMoved", mx - 40, my - 20, button="left", buttons=1)
    await mouse(app, "mouseReleased", mx - 40, my - 20, button="left", buttons=0, clickCount=1)
    assert await app.js(f"{minimap}.style.right") != right
    # A double-click: the whole picture again, no minimap.
    await mouse(app, "mousePressed", cx, cy, button="left", buttons=1, clickCount=1)
    await mouse(app, "mouseReleased", cx, cy, button="left", buttons=0, clickCount=1)
    await mouse(app, "mousePressed", cx, cy, button="left", buttons=1, clickCount=2)
    await mouse(app, "mouseReleased", cx, cy, button="left", buttons=0, clickCount=2)
    await app.wait_js(f"!{tile}.classList.contains('zoomed')", "the whole picture again")
    assert await app.js(f"getComputedStyle({minimap}).display") == "none"
    assert await app.js(transform) == ""


def test_stream_zoom_pan_and_minimap(tmp_path, tone):  # noqa: F811
    with network(tmp_path) as (_meta, _relay, community):
        node = community()

        async def main():
            olive = await App(tmp_path / "olive", env=media_env(tone), name="olive").start()
            try:
                await create_profile(olive)
                await claim(olive, node.claim_code, "Zoom Town", "Olive")
                await create_voice_channel(olive, "Lounge")
                await join_voice(olive, "Lounge")
                await olive.click('.voice-bar button[aria-label="Turn on camera"]')
                await olive.wait_js(
                    f"{TILE} && {TILE}.querySelector('video') && {TILE}.querySelector('video').videoWidth > 0",
                    "her camera",
                    30,
                )
                await zoom_pan_minimap(olive, TILE)
                # Escape puts the whole picture back too.
                cx, cy, _w, _h = await olive.js(box(TILE))
                for _ in range(3):
                    await mouse(olive, "mouseWheel", cx, cy, deltaX=0, deltaY=-300)
                await olive.wait_js(f"{TILE}.classList.contains('zoomed')", "zoomed in")
                await olive.key("Escape")
                await olive.wait_js(f"!{TILE}.classList.contains('zoomed')", "Escape: the whole picture")
                olive.assert_no_errors()
            finally:
                await olive.stop()

        run(main(), 180)
