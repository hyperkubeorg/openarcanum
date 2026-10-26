# Manual test: voice, video and screen share on real devices

The automated tests (`clients/desktop/tests/test_calls.py`, `test_ui_voice.py`) run
calls with Chromium's fake camera and microphone, under Xvfb. They can't
check real microphones, cameras, speakers, echo, screen pickers or the
operating systems' privacy prompts. This script does. Run it on **Linux,
Windows and macOS** before a release, and note the OS version, the result of
each step, the measured delay (section 8) and anything odd in the table at
the end.

You need two computers (or one computer and a second person), each with a
microphone, speakers or headphones, and a camera, and one community with a
voice channel. To test moderation, one of the two must be an owner or have
Mute Members, Deafen Members and Move Members, and the community needs a
second voice channel.

## 1. Devices and settings

1. Open **Settings → Voice & video**.
   - [ ] macOS: the first time, macOS asks for microphone access for
     OpenArcanum; allow it. (If you said no before: System Settings → Privacy &
     Security → Microphone.)
   - [ ] Your microphone, speaker and camera appear by name.
2. Click **Test microphone** and speak.
   - [ ] The meter moves with your voice and stays low when you're quiet.
   - [ ] **Voice activity sensitivity** starts at −40 dB. With a fan or a
     computer running near the microphone and you quiet, the meter stays
     below the red mark; drag the mark just below your speaking level if
     it isn't already.
3. Pick a different microphone (if you have one), test again.
   - [ ] The meter follows the newly chosen microphone.
4. Click **Play test sound**.
   - [ ] You hear a short two-note chime from the chosen speaker (pick
     another speaker and try again: it follows).
5. Click **Test camera**.
   - [ ] Your camera's picture appears; **Stop camera** turns the camera
     light off.
6. No microphone: unplug every microphone (or, on a desktop without one,
   just try it), then join a voice channel.
   - [ ] You join; the voice bar says "No microphone found. You can still
     hear everyone." and you hear the others.
   - [ ] Plug a microphone in: within a second or two the notice goes and
     the others hear you, without rejoining.
   - [ ] Unplug it again during the call: the notice comes back; you still
     hear everyone.
7. Refused microphone: turn OpenArcanum's microphone access off (macOS: System
   Settings → Privacy & Security → Microphone; Windows: Settings → Privacy
   & security → Microphone), join a call.
   - [ ] You join, listening; the bar says OpenArcanum isn't allowed to use
     the microphone. Allow it again, click **Try again**: the others hear
     you.

## 2. A call

1. Both people click the voice channel.
   - [ ] Each sees "Voice connected" and both names under the channel, and
     people who aren't in the call also see both names there.
2. Talk normally, with speakers (not headphones) on one side.
   - [ ] Both hear each other clearly; no echo of your own voice comes back
     (echo cancellation), steady background noise (a fan) is suppressed,
     and quiet and loud speakers end up at similar levels.
   - [ ] The speaking ring lights up around whoever talks.
   - [ ] Delay feels like a phone call (well under a second). Section 8
     measures it.
3. **Mute** (the microphone), then **Deafen** (the headphones).
   - [ ] Muted: the other side hears nothing, and sees the muted icon.
   - [ ] Deafened: you hear nothing and are muted too.
   - [ ] Nothing is under the call's tiles: every control is in the voice
     bar, with no line above it.
4. Right-click the other person (or **Options for …**) and drag the volume
   slider at the top of the menu to 30%, then 200%.
   - [ ] Their volume changes only on your side.
   - [ ] **Mute for me**: you stop hearing them, they hear you as before
     and see nothing different; "Muted for you" shows next to their name.
     Leave and rejoin: still muted for you. **Mute for me** again undoes it.
5. Switch microphone and speaker during the call, once in Settings and once
   from the arrows beside **Mute** and **Deafen** in the voice bar.
   - [ ] The call continues with the new devices without rejoining.
   - [ ] **Output volume** (the arrow beside **Deafen**) changes how loud
     the call plays, on your side only.

## 3. Talking modes

1. Settings → Voice & video → Input mode **Push to talk**, key `V` (click the
   box and press V).
   - [ ] With the OpenArcanum window focused, you're only heard while you hold V.
   - [ ] With another app focused, holding V still sends (Windows, X11;
     macOS after allowing OpenArcanum under Privacy & Security → Input
     Monitoring). On Wayland it only works while OpenArcanum is focused (the
     system doesn't let apps see keys pressed elsewhere). Turning off
     "Push to talk in other windows" limits it to OpenArcanum's window.
   - [ ] Linux on Wayland (GNOME, KDE): Settings shows "This is a Wayland
     session…" under "Push to talk in other windows" and that switch is
     off. On an X11 session nothing is shown and the switch works.
2. Back to **Voice activity**: you're heard when speaking above the mark,
   and a fan or computer noise near the microphone alone doesn't open it.
3. **Studio** (from the arrow beside **Mute**), with headphones on, on a
   microphone with its own noise gate or processing if you have one.
   - [ ] You're heard all the time while unmuted, even very quietly; the
     other side hears your microphone as it is (no noise suppression: a
     fan is audible; no gain control: quiet stays quiet).
   - [ ] Mute still mutes. Back to **Voice activity** afterwards.

## 4. Video

1. Turn on the camera (**Turn on camera**).
   - [ ] macOS asks for camera access the first time.
   - [ ] The other side sees your avatar with "Click to watch" in your
     tile; clicking it shows your video, smooth at normal motion, large,
     with the other tiles in a strip below; clicking again or Escape puts
     it back; **Stop watching** returns to "Click to watch".
   - [ ] While nobody watches, your upload drops to audio only (the
     system's network monitor: well under 100 kbit/s), and goes up when
     someone watches.
2. On the receiving side, make the window small, then large.
   - [ ] The picture gets sharper when the tile is large (the client asks
     the relay for a higher layer) and the call doesn't stutter when small.
3. Switch camera in Settings while it's on.
   - [ ] The video switches to the new camera.

## 5. Screen share

1. Click **Share your screen**. OpenArcanum's own picker lists screens and
   windows.
   - [ ] macOS: the first time, macOS asks for Screen Recording permission
     (System Settings → Privacy & Security → Screen Recording); allow and
     restart OpenArcanum if asked.
   - [ ] Windows appear in the list after a moment.
2. Share a window with a video playing, with **Share sound** ticked in the
   picker, and your camera on as well.
   - [ ] A red **LIVE** shows next to your name in the channel list on both
     sides, and the other side's stage has a block for your screen ("Click
     to watch") as well as your camera.
   - [ ] Watching it, the other side sees the window, sharp enough to read
     text.
   - [ ] System audio: on Windows the other side hears the video's sound;
     on Linux and macOS whether it's offered depends on the system (note it).
     The watcher's **Mute stream** (on the block) silences it for them only;
     **Stream volume** changes it.
   - [ ] Untick **Share sound** in the arrow's menu beside the screen
     button: the sound stops for the watcher at once.
3. Stop sharing from the button, and once by closing the shared window.
   - [ ] Sharing stops both ways; the call goes on.

## 6. Moderation (owner side)

1. Right-click the other person → **Server mute**.
   - [ ] They see "server muted" and can't unmute themselves; others can't
     hear them. **Server unmute** restores it.
2. **Server deafen**, then undeafen.
   - [ ] They hear nothing while deafened.
3. **Move to…** the second voice channel.
   - [ ] Their client moves to that channel by itself.
4. **Disconnect**.
   - [ ] Their call ends with a message; they can rejoin.
5. Give someone **Priority Speaker** (a role with it). In the call they
   now have a megaphone button beside the camera, not pressed.
   - [ ] Before they press it, nobody gets quieter when they talk.
   - [ ] After they press it, others get quieter while they speak, and a
     megaphone shows by their name.

## 7. Network trouble

1. During a call, turn Wi-Fi off for 5 seconds, then on.
   - [ ] The call reconnects by itself within a few seconds.
2. (If you run your own relays) stop the relay carrying the call.
   - [ ] Within about 10 to 20 seconds the call continues on another relay.
3. On a network that blocks outgoing UDP (or with a firewall rule dropping
   UDP to the relay's media port), join a call.
   - [ ] Within about 15 seconds the voice bar turns red: "Can't reach the
     call's media server at <address>. Calls need UDP network traffic…",
     with **Try again**, **Copy details** and **Dismiss**. You're not
     listed in the channel any more. Chat keeps working.

## 8. Mouth-to-ear delay (measured)

The target is **250 ms or less** from the talker's mouth to the listener's
ear through a relay in the same region. The automated tests measure a median
of about 118 to 128 ms on an emulated same-region path
(`docs/voice-decision.md` "Latency"), without the
microphone's own capture delay; this measures the whole path with real
devices.

You need two computers in the same call through a relay in your region
(for example the relay you'd use in production, not one across the
world), a phone (or any recorder) and a free audio editor (Audacity).

1. Set up so the delay is recorded once and nothing loops:
   - Computer **A** (talker): unmuted, input mode **Voice activity** with
     **Voice activity sensitivity** dragged all the way left, so A sends
     all the time (otherwise the start of each clap could be cut while
     voice activity opens). If claps still come through cut or not at all,
     use **Push to talk** instead with the key held down the whole time (a
     heavy book on it works). Turn A's speaker volume to zero (or use
     headphones nobody wears).
   - Computer **B** (listener): **Mute** so B sends nothing back, speaker
     volume at a normal level, no headphones.
   - Put A's microphone and B's speaker about 1 m apart, and the phone
     between them, about the same distance from both (sound takes about
     3 ms per metre, so a few centimetres don't matter).
2. Start recording on the phone. Clap once, sharply, right next to A's
   microphone. Wait 2 seconds. Clap again. Do 10 claps, then stop recording.
3. Open the recording in Audacity and zoom in on the first clap. There are
   two spikes: the clap itself, then the same clap coming out of B's
   speaker. Click at the start of the first spike and shift-click at the
   start of the second; the selection length (bottom toolbar, set to
   "hh:mm:ss + milliseconds") is the mouth-to-ear delay. Note it for each
   clap.
   - [ ] The **median** of the 10 delays is **250 ms or less**. Write the
     median, the largest value, the relay's location and whether the
     computers were on Wi-Fi or cable in the Results table.
4. If it's over 250 ms, repeat with both computers on a cable and note both
   results: Wi-Fi often adds 20 to 50 ms.

(One computer is enough if you don't have two: run two copies of OpenArcanum
with different `--home` folders, one in each role above, using that
computer's microphone and speaker; on the talking copy set the listening
copy's volume (the slider in its right-click menu) to 0%. Later, quieter repeats of a clap are the sound
going round again through the microphone; only the first one counts. The
delay includes the same steps, but both
copies share one sound device, which can hide device-specific delays, so
note that you used one computer.)

## 9. What the relay sees of a voice (optional, needs Wireshark)

The relay must not be able to tell when someone talks or what they say
from the packets (docs/protocol.md §12.1 "What stays in the clear"). The
automated test checks this with generated sounds; this checks it with a
real microphone and voice.

1. On the talking computer, start Wireshark capturing on the network
   interface, filter `udp`, and join a call with one other person
   listening. Find the relay's media address in the call's connection
   details (or the UDP conversation with the most packets), then
   right-click a packet → **Decode As…** → **RTP**, and filter
   `rtp && ip.dst == <relay address>`.
2. **Telephony → RTP → RTP Streams**: the microphone's stream (payload type
   111) is about 50 packets a second.
3. Talk for 10 seconds, stay silent for 10, mute for 10, then switch to
   **Push to talk** and leave the key up for 10. Add a column for
   `frame.len` (or **Statistics → Packet Lengths** with the filter
   `rtp.p_type == 111 && ip.dst == <relay address>`).
   - [ ] Every voice packet has the same length through all four (one
     byte more after the first few seconds is SFrame's counter growing; a
     packet of another fixed length now and then is the engine probing
     bandwidth), and the rate stays about 50 a second.
   - [ ] In a packet's details, the RTP header extensions show only small
     ids for congestion control and mid: no "audio level"
     (`ssrc-audio-level`) element.
4. Repeat in **Studio** mode: the same lengths while talking and silent.

## Results

| Step | Linux (distro, version) | Windows (version) | macOS (version) |
|---|---|---|---|
| 1 Devices |  |  |  |
| 2 Call |  |  |  |
| 3 Talking modes |  |  |  |
| 4 Video |  |  |  |
| 5 Screen share |  |  |  |
| 6 Moderation |  |  |  |
| 7 Network |  |  |  |
| 8 Mouth-to-ear delay: median / max ms, relay location, Wi-Fi or cable (pass: median ≤ 250 ms) |  |  |  |
| 9 What the relay sees: voice packet length(s) talking / silent / muted / gate closed |  |  |  |
