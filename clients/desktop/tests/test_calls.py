"""Calls through the webview media path and a relay's SFU, with Chromium's
fake devices: tests/webengine/callharness.py runs
the client's call code (ui/js/call.js, sframe.js) in real QtWebEngine pages
against a real relay. Each test runs one scenario and checks what the pages
observed."""

import json
import os
import socket
import subprocess
import sys
from pathlib import Path

import pytest
from conftest import ENGINE_TEST_FLAGS, binary, fake_media_flags, headless, run_relay

HARNESS = Path(__file__).parent / "webengine" / "callharness.py"

pytestmark = pytest.mark.gui


@pytest.fixture(scope="module")
def tone(tmp_path_factory):
    sys.path.insert(0, str(HARNESS.parent))
    import callharness

    path = tmp_path_factory.mktemp("media") / "tone.wav"
    callharness.tone_bursts(path)
    return path


def harness(plan: dict, tone: Path, timeout: float = 240, extra_flags: str = "") -> dict:
    flags = (
        f"{fake_media_flags(tone)} --use-fake-ui-for-media-stream "
        f"--auto-select-desktop-capture-source=Entire --autoplay-policy=no-user-gesture-required {extra_flags}"
    )
    cmd = headless([sys.executable, str(HARNESS), json.dumps(plan)])
    env = dict(os.environ, QTWEBENGINE_CHROMIUM_FLAGS=f"{flags} {ENGINE_TEST_FLAGS}")
    p = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout, env=env)
    line = next((ln for ln in reversed(p.stdout.splitlines()) if ln.startswith("{")), None)
    assert line, p.stderr[-3000:]
    out = json.loads(line)
    assert "error" not in out, out["error"]
    return out


def test_three_people_audio_video_screen_rekey_layers_and_moderation(tone):
    with run_relay() as relay:
        r = harness({"scenario": "basic", "relay": relay}, tone)
    # Everyone hears and sees everyone; c's screen reaches the others.
    for name, d in r["joined"].items():
        assert d["connection"] == "connected", name
        assert len(d["remote"]) == 2 and all(x["audio"] and x["camera"] for x in d["remote"]), (name, d["remote"])
        # Frames under keys the receiver doesn't have yet are only lost
        # around a join (the joiner gets only the new key).
        assert d["stats"]["failed"] < 0.05 * d["stats"]["decrypted"], (name, d["stats"])
    assert r["joined"]["c"]["stats"]["sentFrames"]["screen"] > 50
    assert any(x["screen"] for x in r["joined"]["a"]["remote"])
    # Speaking: a hears both others' tone bursts at close to their level.
    assert all(s["spoke"] and s["max"] > -20 for s in r["speaking"].values()), r["speaking"]
    # A key change mid-call loses nothing.
    for name, d in r["rekey"].items():
        assert d["decrypted"] > 100 and d["failed"] == 0, (name, d)
    # a shows b large and c small: c gets the low layer, b a higher one (the
    # top one when the bandwidth estimate allows), and what arrives is the
    # size of the layers asked for.
    chosen = {x["key"].split("/")[0]: x["rid"] for x in r["layers"]["debug"]}
    rank = {"l": 0, "m": 1, "h": 2}
    assert sorted(rank[x] for x in chosen.values()) in ([0, 1], [0, 2]), r["layers"]
    # The top layer is the camera's own size (1280x720 asked for; the fake
    # camera on some systems gives a larger one), the others its halves.
    sizes = {(v["w"], v["h"]) for v in r["layers"]["inbound"]}
    top = max(sizes, key=lambda wh: wh[0])
    assert top[0] >= 1280, sizes
    size = {"l": (320, 180), "m": (640, 360), "h": top}
    for rid in chosen.values():
        assert size[rid] in sizes, (rid, sizes)
    # Server mute: b goes silent at a, c doesn't.
    levels = sorted(s["max"] for s in r["muted"].values())
    assert levels[0] == -120 and levels[1] > -30, r["muted"]
    # c left: a and b see only each other.
    for name, d in r["after_leave"].items():
        assert len(d["remote"]) == 1, (name, d["remote"])
    # Server removal reaches b.
    assert any(e.startswith("b: ended: A moderator disconnected you") for e in r["events"]), r["events"]


def test_nothing_is_sent_that_nobody_takes(tone):
    """A camera nobody watches isn't encoded or sent; it is once someone
    watches, and stops again when they stop. A voice nobody can hear (the
    only listener muted it for themselves, or deafened) isn't sent either;
    muted for oneself, it isn't received or played. Measured on the frames
    the sender encrypts and sends and on what the listener receives."""
    with run_relay() as relay:
        r = harness({"scenario": "watchers", "relay": relay}, tone)
    # ~30 fps camera and 50 audio frames a second when sent; 3 s windows.
    unwatched = r["unwatched"]
    assert unwatched["camera"] == 0 and not unwatched["sending"]["camera"], unwatched
    assert unwatched["audio"] > 100 and unwatched["sending"]["audio"], unwatched
    assert all(v["frames"] == 0 for v in r["unwatched_b"]), r["unwatched_b"]
    watched = r["watched"]
    assert watched["camera"] > 10 and watched["sending"]["camera"], watched  # ~30 fps; slow runners manage less
    assert any(v["frames"] > 0 and v["w"] > 0 for v in r["watched_b"]), r["watched_b"]
    again = r["unwatched_again"]
    assert again["camera"] == 0 and not again["sending"]["camera"], again
    muted = r["muted_for_b"]
    assert muted["audio"] == 0 and not muted["sending"]["audio"], muted
    assert all(s["max"] == -120 and not s["spoke"] for s in r["muted_for_b_heard"].values()), r["muted_for_b_heard"]
    back = r["unmuted"]
    assert back["audio"] > 100 and back["sending"]["audio"], back
    # The receiver's state when nobody was heard (printed in full: pytest shortens assertion messages).
    for label in ("before_mute_heard", "before_mute_a", "unmuted_b", "unmuted_a", "unmuted_heard"):
        print(f"\n{label}:", json.dumps(r[label]))
    assert any(s["spoke"] for s in r["unmuted_heard"].values()), r["unmuted_heard"]
    assert r["deafened"]["audio"] == 0 and not r["deafened"]["sending"]["audio"], r["deafened"]
    assert r["undeafened"]["audio"] > 100, r["undeafened"]


# The harness's pages are hidden; without these the engine runs their
# timers about once a second (call.js's 50 ms voice gate included), unlike
# a visible window.
UNTHROTTLED = "--disable-background-timer-throttling --disable-renderer-backgrounding"


def _gate(levels: list[float], threshold: float, rate: int, hang_ms: int = 400) -> float:
    """How much of the time call.js's voice gate (level over the threshold,
    held open 400 ms) would be open over these levels (one per 512
    samples)."""
    hang = int(hang_ms / (512 / rate * 1000))
    left = opened = 0
    for lv in levels:
        if lv > threshold:
            left = hang
        elif left:
            left -= 1
        opened += lv > threshold or left > 0
    return opened / len(levels)


def _levels(tmp_path: Path, noise: float, speech: float | None, mode: str, threshold: int) -> dict:
    sys.path.insert(0, str(HARNESS.parent))
    import callharness

    bed = tmp_path / f"bed-{-noise}-{speech}.wav"
    callharness.noise_bed(bed, noise, speech)
    with run_relay() as relay:
        return harness(
            {"scenario": "levels", "relay": relay, "mode": mode, "threshold": threshold, "seconds": 6},
            bed,
            extra_flags=UNTHROTTLED,
        )


def test_default_voice_sensitivity_ignores_room_noise_and_opens_for_speech(tmp_path):
    """The voice activity default (settings.VOICE_SENSITIVITY_DEFAULT),
    measured through the engine's processing as calls use it: steady room
    noise at -45 dBFS (a fan or computer close to the microphone) never
    opens the gate, where the old default (-50) is open all the time;
    speech over that noise opens it (about half the time: the bed speaks
    half of each second)."""
    from openarcanum.settings import VOICE_SENSITIVITY_DEFAULT, VOICE_SENSITIVITY_OLD_DEFAULT

    noise = _levels(tmp_path, -45, None, "vad", VOICE_SENSITIVITY_DEFAULT)
    lv = noise["levels"]
    gate = noise["gate"]
    assert noise["constraints"]["noiseSuppression"] and noise["constraints"]["autoGainControl"], noise["constraints"]
    assert len(gate) > 60 and not any(g["open"] or g["tx"] for g in gate), gate[:20]
    opened_old = _gate(lv["levels"], VOICE_SENSITIVITY_OLD_DEFAULT, lv["rate"])
    opened_new = _gate(lv["levels"], VOICE_SENSITIVITY_DEFAULT, lv["rate"])
    median = sorted(lv["levels"])[len(lv["levels"]) // 2]
    print(
        f"\nroom noise at -45 dBFS: {median:.1f} dBFS after processing; gate open {opened_old:.0%}"
        f" at the old default, {opened_new:.0%} at the new"
    )
    assert opened_old > 0.9 and opened_new == 0, (opened_old, opened_new)

    speech = _levels(tmp_path, -45, -30, "vad", VOICE_SENSITIVITY_DEFAULT)
    open_share = sum(g["open"] for g in speech["gate"]) / len(speech["gate"])
    loud = sorted(speech["levels"]["levels"])[int(0.8 * len(speech["levels"]["levels"]))]
    print(f"speech at -30 dBFS over it: {loud:.1f} dBFS (80th percentile); gate open {open_share:.0%}")
    assert 0.4 < open_share < 0.98, open_share
    assert loud > VOICE_SENSITIVITY_DEFAULT + 10, loud


def test_studio_mode_sends_unprocessed_and_ungated(tmp_path):
    """Studio mode: the microphone is opened without echo cancellation,
    noise suppression or gain control, and is always open (unmuted), even
    on room noise far under any voice activity threshold."""
    r = _levels(tmp_path, -60, None, "studio", 0)
    c = r["constraints"]
    assert (c["echoCancellation"], c["noiseSuppression"], c["autoGainControl"]) == (False, False, False), c
    assert len(r["gate"]) > 60 and all(g["tx"] for g in r["gate"]), r["gate"][:20]


def test_frames_are_end_to_end_encrypted(tone):
    """A participant sending under a key nobody else has can't be heard: the
    media really is SFrame-encrypted, end to end."""
    with run_relay() as relay:
        r = harness({"scenario": "wrongkey", "relay": relay}, tone)
    w = r["wrongkey"]
    assert w["b_sent"] > 100 and w["decrypted"] == 0 and w["failed"] > 50, w


def _gaps(samples: list[dict], who: str, other: str, other_speaking: bool) -> list[float]:
    """How much quieter who's audio is played than received (dB) on the
    listening page, in the samples where who is audible and other is (or
    isn't) counted as speaking."""
    out = []
    for s in samples:
        r = s["remote"]
        if who in r and other in r and r[who]["pre"] > -40 and r[other]["speaking"] == other_speaking:
            out.append(r[who]["post"] - r[who]["pre"])
    return out


def _median(xs: list[float]) -> float:
    xs = sorted(xs)
    return xs[len(xs) // 2]


def test_priority_speaker_turns_others_down(tone):
    """Priority Speaker: while a (who has it)
    speaks, the listener plays b at 30% (about -10.5 dB, measured on the
    audio after the volume stage); a is never turned down; when a is muted
    or loses the role mid-call, b plays at full volume again."""
    with run_relay() as relay:
        r = harness({"scenario": "priority", "relay": relay}, tone)
    a, b = r["ids"]["a"], r["ids"]["b"]
    ducked = _gaps(r["with_priority"], b, a, True)
    assert len(ducked) >= 5, r["with_priority"][:5]
    assert -13 < _median(ducked) < -8, ducked
    # The volume call.js set on b while a speaks.
    gains = [s["remote"][b]["gain"] for s in r["with_priority"] if b in s["remote"] and s["remote"][a]["speaking"]]
    assert gains and abs(_median(gains) - 0.3) < 0.01, gains
    # a itself is never turned down, whoever speaks.
    own = [s["remote"][a]["post"] - s["remote"][a]["pre"] for s in r["with_priority"] if s["remote"][a]["pre"] > -40]
    assert own and abs(_median(own)) < 1.5, own
    # a muted: not speaking any more, b at full volume.
    muted = r["priority_muted"]
    assert not any(s["remote"][a]["speaking"] for s in muted[5:]), [s["remote"][a] for s in muted]
    full = _gaps(muted[5:], b, a, False)
    assert len(full) >= 5 and abs(_median(full)) < 1.5, full
    # Role taken away: a speaks and b isn't turned down.
    after = _gaps(r["without_priority"], b, a, True)
    print(
        f"\npriority: b played {_median(ducked):.1f} dB while a speaks ({len(ducked)} samples), "
        f"{_median(full):.1f} dB with a muted, {_median(after) if after else float('nan'):.1f} dB without the role"
    )
    assert len(after) >= 3, r["without_priority"][:5]  # a slow runner samples less often
    assert abs(_median(after)) < 1.5, after


def free_udp_port() -> int:
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


def latency_run(tone, loss=0.0, delay_ms=0.0, jitter_ms=0.0) -> dict:
    """Mouth-to-ear through a relay, optionally through a lossy, delaying
    UDP proxy between the clients and the relay's media port."""
    media = free_udp_port()
    plan = {"scenario": "latency", "bursts": 12}
    env = {}
    if loss or delay_ms or jitter_ms:
        # The relay advertises the proxy's address; the proxy forwards to it.
        env = {"media_udp_port": media, "media_listen_ip": "127.0.0.1", "media_public_ips": "127.0.0.3"}
        plan["proxy"] = {
            "listen": ["127.0.0.3", media],
            "upstream": ["127.0.0.1", media],
            "loss": loss,
            "delay_ms": delay_ms,
            "jitter_ms": jitter_ms,
        }
    with run_relay(**env) as relay:
        plan["relay"] = relay
        return harness(plan, tone)


def test_mouth_to_ear_latency_same_region(tone):
    r = latency_run(tone)["latency"]
    assert r["heard"] >= r["sent"] - 1, r
    ms = sorted(r["ms"])
    median = ms[len(ms) // 2]
    print(f"\nmouth-to-ear, local relay: median {median:.0f} ms, max {ms[-1]:.0f} ms ({ms})")
    assert median < 250, ms


def test_latency_and_loss_under_emulated_network(tone):
    """A same-region path through the relay: 10 ms ± 3 ms on each leg (client
    to relay, relay to client) and 2% loss each way. Speech still arrives
    (Opus FEC and concealment) within the 250 ms budget."""
    out = latency_run(tone, loss=0.02, delay_ms=10, jitter_ms=3)
    r = out["latency"]
    assert out["proxy"]["dropped"] > 0, out["proxy"]
    assert r["heard"] >= r["sent"] - 2, out
    ms = sorted(r["ms"])
    median = ms[len(ms) // 2]
    print(
        f"\nmouth-to-ear, 10±3 ms per leg, 2% loss: median {median:.0f} ms, max {ms[-1]:.0f} ms; proxy {out['proxy']}"
    )
    assert median < 250, ms


def test_latency_on_a_poor_network(tone):
    """Recorded, not a same-region path: 20 ms ± 5 ms per leg and 3% loss.
    The adaptive jitter buffer grows; speech keeps arriving."""
    out = latency_run(tone, loss=0.03, delay_ms=20, jitter_ms=5)
    r = out["latency"]
    assert r["heard"] >= r["sent"] - 2, out
    ms = sorted(r["ms"])
    median = ms[len(ms) // 2]
    print(
        f"\nmouth-to-ear, 20±5 ms per leg, 3% loss: median {median:.0f} ms, max {ms[-1]:.0f} ms; proxy {out['proxy']}"
    )
    assert median < 400, ms


def mixed(tone, tmp_path, **plan) -> dict:
    callload = binary("callload")
    # Every participant here comes from 127.0.0.1, past the default limit of
    # 20 call participants per address.
    with run_relay(max_call_peers_per_ip=200) as relay:
        out = harness(
            {"scenario": "mixed", "relay": relay, "callload": str(callload), "workdir": str(tmp_path), **plan},
            tone,
            600,
        )
    return out["mixed"]


def test_fifty_people_in_a_voice_call(tone, tmp_path):
    """50 participants: 6 real clients and 44
    synthetic ones (internal/testutil/cmd/callload: Pion, real SFrame under
    the same keys; a machine can't run 50 browser engines at once). Everyone
    receives and decrypts everyone else."""
    m = mixed(tone, tmp_path, real=6, synth=44, seconds=20)
    for name, p in m["pages"].items():
        assert p["connection"] == "connected", name
        assert p["audio_from"] == 49 and p["decrypted_from"] == 49 and p["failed"] == 0, (name, p)
    for s in m["synth"]:
        assert s["joined"] and not s["error"] and s["audio_from"] == 49 and s["failed"] == 0, s


def test_twenty_five_people_with_video_and_screen_share(tone, tmp_path):
    """25 participants all sending camera video (simulcast), one also
    sharing a screen: every real client receives every camera and the screen
    and decrypts everyone; every synthetic one receives all audio and video."""
    m = mixed(tone, tmp_path, real=4, synth=21, video_real=4, video_synth=21, screen_real=1, seconds=25)
    for name, p in m["pages"].items():
        assert p["connection"] == "connected", name
        assert p["audio_from"] == 24 and p["camera_from"] == 24 and p["decrypted_from"] == 24, (name, p)
        assert p["failed"] == 0, (name, p)
    assert [p["screen_from"] for p in m["pages"].values()] == [0, 1, 1, 1]
    for s in m["synth"]:
        assert s["joined"] and s["audio_from"] == 24 and s["video_from"] == 24 and s["failed"] == 0, s


def test_receiver_lowers_layers_when_packets_get_lost(tone):
    """Congestion control for layers: with a clean link a large tile gets
    the top layer; at 15% loss the receiver lowers its cap."""
    media = free_udp_port()
    env = {"media_udp_port": media, "media_listen_ip": "127.0.0.1", "media_public_ips": "127.0.0.3"}
    proxy = {
        "listen": ["127.0.0.3", media],
        "upstream": ["127.0.0.1", media],
        "loss": 0.0,
        "delay_ms": 0,
        "jitter_ms": 0,
    }
    with run_relay(**env) as relay:
        r = harness({"scenario": "lossy_layers", "relay": relay, "proxy": proxy}, tone)
    assert [x["rid"] for x in r["clean"]] == ["h"], r
    assert [x["rid"] for x in r["lossy"]] in (["m"], ["l"]), r


# (mode, what a sends, push to talk held) for each measured phase.
WIRE_PHASES = [
    ["studio", "speech", False],
    ["studio", "tone", False],
    ["studio", "noise", False],
    ["studio", "silence", False],
    ["ptt", "speech", True],
    ["ptt", "tone", True],
    ["ptt", "noise", True],
    ["ptt", "silence", True],
    ["ptt", "speech", False],
]


def test_relay_sees_no_voice_level_and_constant_voice_packets(tone):
    """What a relay (or anyone on the path) sees of a voice: no audio level
    header extension (RFC 6464) negotiated or on any packet, and Opus at a
    constant bitrate without DTX, so packet sizes don't follow what is said:
    speech, a tone, noise and silence give packets of one size, in studio
    mode (always sending) and with push to talk held. With push to talk
    released the gate sends silence: again packets of that size, as
    often. Measured on the RTP headers through a proxy in front of the
    relay's media port (SRTP leaves them in the clear)."""
    r = wire_run(tone)
    # (a) Nothing in any SDP exchanged with the relay asks for an audio
    # level, and every Opus format is constant bitrate without DTX.
    sdps = r["sdps"] + [r["local"], r["remote"]]
    assert sdps and not any("audio-level" in s for s in sdps), [s for s in sdps if "audio-level" in s][:1]
    for s in sdps:
        lines = s.splitlines()
        opus = {
            ln.split()[0][len("a=rtpmap:") :] for ln in lines if ln.lower().endswith(("opus/48000/2", "opus/48000"))
        }
        fmtps = [ln for ln in lines if ln.startswith("a=fmtp:") and ln.split()[0][len("a=fmtp:") :] in opus]
        assert opus and len(fmtps) >= 1, s
        for ln in fmtps:
            params = set(ln.split(" ", 1)[1].split(";"))
            assert {"cbr=1", "usedtx=0"} <= params, ln
    # ... and the only header extensions on audio packets either way are
    # the ones a negotiated (transport-wide congestion control, mid).
    allowed = {
        ln.split()[0].split(":")[1].split("/")[0]
        for ln in r["local"].splitlines()
        if ln.startswith("a=extmap:") and ("transport-wide-cc" in ln or "sdes:mid" in ln)
    }
    seen = {k.split(":")[0] for p in r["phases"] for g in p["groups"].values() for k in g["ext"]}
    assert seen <= allowed, (seen, allowed)
    # (b), (c) Packet sizes, per phase: one size, the same in every phase
    # (but for SFrame's counter growing a byte, and a few fixed-size
    # padding packets the engine's bandwidth probing sends).
    rows = []
    dominant = set()
    sizes = set()
    for p in r["phases"]:
        g = p["groups"].get("up/111")
        assert g, p
        top = max(int(k) for k, v in g["sizes"].items() if v == max(g["sizes"].values()))
        voice = sum(v for k, v in g["sizes"].items() if abs(int(k) - top) <= 1)
        rows.append(
            f"{p['mode']:6} {p['signal']:7} ptt={p['ptt']!s:5} {g['n']:4} packets {g['kbps']:5} kbps sizes {g['sizes']}"
        )
        # 50 packets a second, whatever is (or isn't) said.
        assert g["n"] >= 45 * p["seconds"], (p, g)
        assert voice >= 0.95 * g["n"], (p, g)
        dominant.add(top)
        sizes |= {int(k) for k in g["sizes"]}
    print("\nwhat the relay sees of a's voice:\n  " + "\n  ".join(rows))
    assert max(dominant) - min(dominant) <= 1, dominant
    assert len(sizes) <= 4, sizes
    # b received and decrypted a's voice throughout (frames under a key b
    # doesn't have yet are only lost around b's join).
    st = r["pages"]["b"]["stats"]
    assert st["decrypted"] > 500 and st["failed"] < 0.05 * st["decrypted"], st


def wire_run(tone) -> dict:
    """The wire scenario through a recording proxy in front of the relay's
    media port: what the relay (or anyone on the path) sees."""
    media = free_udp_port()
    env = {"media_udp_port": media, "media_listen_ip": "127.0.0.1", "media_public_ips": "127.0.0.3"}
    proxy = {"listen": ["127.0.0.3", media], "upstream": ["127.0.0.1", media]}
    with run_relay(**env) as relay:
        return harness(
            {"scenario": "wire", "relay": relay, "proxy": proxy, "phases": WIRE_PHASES, "seconds": 3},
            tone,
            extra_flags=UNTHROTTLED,
        )
