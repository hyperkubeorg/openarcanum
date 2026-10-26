# Calls: where media is captured and encoded

*Decision record, October 2026.*

## Decision

Voice, video and screen share run as WebRTC inside the client's webview
(QtWebEngine, so Chromium on every OS), with SFrame (RFC 9605) applied to
every encoded frame, through a keyless SFU on the relays. Python keeps the
control plane.

- **Media in the page** (`clients/shared/ui/js/call.js`): Chromium captures
  and processes audio (echo cancellation, noise suppression and gain control
  all on), encodes Opus (in-band FEC) and VP8, and handles jitter buffering
  and loss concealment (NetEq). Camera is 720p30 with three simulcast
  layers; screen share is a screen or window, with system audio where the OS
  gives it.
- **SFrame** (`clients/shared/ui/js/sframe.js`, RFC vectors in
  `clients/desktop/tests/js/sframe.test.mjs`) on every encoded frame through
  `createEncodedStreams`, because the bundled engine has no
  `RTCRtpScriptTransform`. Only the per-call key enters the page; identity,
  vault, DM and channel keys stay in Python.
- **Relays** run a keyless SFU (Pion, `internal/sfu`): one UDP port per
  relay, grants signed by the community server, simulcast layer selection
  per receiver, moderator controls enforced. See `docs/protocol.md` §12.
- **Python** (`clients/desktop/src/openarcanum/voice.py`) handles joining,
  the relay connection for signaling, keys, reconnecting and relay failover.

The deviation this brings (SFrame's AES-GCM and HMAC come from Chromium's
WebCrypto rather than from `cryptography` or Go) is described in
`docs/crypto-inventory.md` "Deviation: WebCrypto for SFrame".

## The alternative: media in Python

The first working calls captured and played audio in Python: PortAudio
(`sounddevice`) for devices, libopus through PyAV, SFrame in Python, and one
SFrame per 20 ms frame published to each participant's own relay topic. It
kept every key in Python and gave one implementation on every OS, and a
two-person call through the real relay and community server worked on Linux
(about 43 kbit/s per sender, well under a millisecond of CPU per frame).

It was replaced because it couldn't do what calls need:

| | WebRTC in the webview | Media in Python |
|---|---|---|
| Echo cancellation, noise suppression, gain control | Built into Chromium's capture | Not available from PortAudio; would need another native library |
| Video and screen share | Built in (capture, VP8, simulcast) | Would mean encoding video in Python |
| 50-person calls | One upstream per sender to an SFU | Full mesh of per-sender topics; doesn't scale |
| Jitter and loss | NetEq, Opus FEC | A fixed 60 ms buffer |
| Keys in the page | Only the per-call media key | None |
| Packaging | Nothing extra (QtWebEngine is already bundled) | PyAV (FFmpeg, tens of MB) and PortAudio |

The cost of the chosen path is the per-call key in JavaScript and
WebCrypto as SFrame's primitive provider; both are covered in
`docs/threat-model.md` "Calls".

## Engine checks

`clients/desktop/tests/test_webengine.py` runs
`clients/desktop/tests/webengine/probe.html` in a bare `QWebEngineView` with
Chromium's fake-device flags (`QTWEBENGINE_CHROMIUM_FLAGS`). On Linux with
PySide6 6.12.0 (QtWebEngine 6.12, Chromium 140):

| Check | Result |
|---|---|
| getUserMedia (audio and 720p video) | yes |
| echoCancellation, noiseSuppression, autoGainControl applied | yes (all three reported on) |
| getDisplayMedia (screen and system audio) | yes (screen, "Fake audio") |
| `RTCRtpScriptTransform` | no (not in Chromium 140) |
| `createEncodedStreams` (send and receive) | yes |
| Codecs | Opus; VP8, VP9, AV1 (no H.264 in these wheels) |
| Fake devices through `QTWEBENGINE_CHROMIUM_FLAGS` | yes |
| Permission and screen-pick hooks (`QWebEnginePage.permissionRequested`, `desktopMediaRequested`) | present |

The same test runs in CI on Windows and macOS. Real microphones, cameras,
screen pickers and the OS privacy prompts are covered by
`docs/manual-tests/voice.md`.

## Latency

Measured in October 2026 by `clients/desktop/tests/test_calls.py`: real
QtWebEngine pages through a real relay on one Linux machine, fake devices.
Mouth-to-ear here is a tone burst sent by one page until it reaches the
other's audio output, plus the output latency the engine reports; the
microphone's own capture latency isn't included.

| Path | Median | Max |
|---|---|---|
| Local relay | 106 to 110 ms | 115 ms |
| 10 ± 3 ms per leg, 2% loss each way (same region) | 118 to 128 ms | 153 ms |
| 20 ± 5 ms per leg, 3% loss each way | 140 to 155 ms | 177 ms |

Voice activity adds up to one 50 ms detector tick before the first frame
goes out. The budget is 250 ms mouth-to-ear. Loss and delay came from a
userspace UDP proxy between the clients and the relay.
