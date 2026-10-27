package sfu_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/callauth"
	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/sfu"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

func newSFU(t *testing.T) *sfu.SFU {
	t.Helper()
	s, err := sfu.New(sfu.Config{IncludeLoopback: true, Log: testutil.Quiet()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

type server struct {
	key   *identity.Key
	nonce []byte
	id    []byte
}

func newServer(t *testing.T) *server {
	k, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	n := make([]byte, 16)
	rand.Read(n)
	return &server{key: k, nonce: n, id: callauth.CallID(k.Public, n)}
}

func (s *server) grant(t *testing.T, name string, kid uint32, mut func(*wire.CallGrant)) (*wire.SignedCallGrant, []byte) {
	t.Helper()
	p := make([]byte, 16)
	rand.Read(p)
	g := &wire.CallGrant{
		CallId: s.id, ServerKey: s.key.Public, CallNonce: s.nonce, Participant: p, Kid: kid,
		Audio: true, Video: true, Screen: true, ExpiresAt: time.Now().Add(time.Minute).UnixMilli(),
		MaxParticipants: 50,
	}
	if mut != nil {
		mut(g)
	}
	sg, err := callauth.SignGrant(s.key, g)
	if err != nil {
		t.Fatal(err)
	}
	return sg, p
}

// client is a Pion peer standing in for a participant's webview.
type client struct {
	t     *testing.T
	pc    *webrtc.PeerConnection
	peer  *sfu.Peer
	audio *webrtc.TrackLocalStaticRTP
	video map[string]*webrtc.TrackLocalStaticRTP
	// The control this client brings when it joins (from its session).
	control *wire.SignedCallControl
	// The header extensions a browser adds to simulcast packets itself.
	videoSender  *webrtc.RTPSender
	midID, ridID uint8
	// The address it joins from ("127.0.0.1" when empty).
	ip string
	// Set when the test is over: a renegotiation still in flight then fails
	// on the closed connection, which is no failure of the test.
	closing atomic.Bool

	mu       sync.Mutex
	offers   []string // the relay's renegotiation offers
	states   []*wire.CallState
	watchers []*wire.CallWatchers
	got      map[string]chan *rtp.Packet // "<source>/<kind>" -> packets
}

func (c *client) CallWatchers(w *wire.CallWatchers) {
	c.mu.Lock()
	c.watchers = append(c.watchers, w)
	c.mu.Unlock()
}

// watch is one kind's entry in the newest CallWatchers this client got.
func (c *client) watch(kind string) *wire.CallWatch {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.watchers) == 0 {
		return nil
	}
	for _, w := range c.watchers[len(c.watchers)-1].Watches {
		if w.Kind == kind {
			return w
		}
	}
	return nil
}

// waitWatch waits until this client's newest CallWatchers says kind has
// n receivers wanting these layers.
func (c *client) waitWatch(t *testing.T, kind string, n uint32, layers ...string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		w := c.watch(kind)
		if w != nil && w.Receivers == n && strings.Join(w.Layers, ",") == strings.Join(layers, ",") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s watchers: got %v, want %d receivers, layers %v", kind, w, n, layers)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (c *client) CallSignal(s *wire.CallSignal) {
	c.mu.Lock()
	c.offers = append(c.offers, s.Sdp)
	c.mu.Unlock()
	fail := func(format string, args ...any) {
		if !c.closing.Load() {
			c.t.Errorf(format, args...)
		}
	}
	go func() {
		if err := c.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: s.Sdp}); err != nil {
			fail("renegotiation offer: %v", err)
			return
		}
		ans, err := c.pc.CreateAnswer(nil)
		if err != nil {
			fail("answer: %v", err)
			return
		}
		gather := webrtc.GatheringCompletePromise(c.pc)
		_ = c.pc.SetLocalDescription(ans)
		<-gather
		if err := c.peer.Signal(&wire.CallSignal{Type: "answer", Sdp: c.pc.LocalDescription().SDP}); err != nil {
			fail("signal answer: %v", err)
		}
	}()
}

func (c *client) CallState(s *wire.CallState) {
	c.mu.Lock()
	c.states = append(c.states, s)
	c.mu.Unlock()
}

func (c *client) lastState() *wire.CallState {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.states) == 0 {
		return nil
	}
	return c.states[len(c.states)-1]
}

func (c *client) stream(key string) chan *rtp.Packet {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := c.got[key]
	if ch == nil {
		ch = make(chan *rtp.Packet, 4096)
		c.got[key] = ch
	}
	return ch
}

func newClient(t *testing.T, sendVideo bool) *client {
	t.Helper()
	m := &webrtc.MediaEngine{}
	if err := m.RegisterDefaultCodecs(); err != nil {
		t.Fatal(err)
	}
	for _, uri := range []string{"urn:ietf:params:rtp-hdrext:sdes:mid", "urn:ietf:params:rtp-hdrext:sdes:rtp-stream-id"} {
		_ = m.RegisterHeaderExtension(webrtc.RTPHeaderExtensionCapability{URI: uri}, webrtc.RTPCodecTypeVideo)
	}
	// Offered for audio as a browser does (the relay must not take them).
	for _, uri := range []string{"urn:ietf:params:rtp-hdrext:sdes:mid", sdpAudioLevel, sdpCSRCAudioLevel} {
		_ = m.RegisterHeaderExtension(webrtc.RTPHeaderExtensionCapability{URI: uri}, webrtc.RTPCodecTypeAudio)
	}
	se := webrtc.SettingEngine{}
	se.SetIncludeLoopbackCandidate(true)
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	api := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithSettingEngine(se))
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	c := &client{t: t, pc: pc, got: map[string]chan *rtp.Packet{}, video: map[string]*webrtc.TrackLocalStaticRTP{}}
	t.Cleanup(func() {
		c.closing.Store(true)
		_ = pc.Close()
	})
	c.audio, _ = webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "audio", "me")
	if _, err := pc.AddTransceiverFromTrack(c.audio, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly}); err != nil {
		t.Fatal(err)
	}
	if sendVideo {
		var sender *webrtc.RTPSender
		for _, rid := range []string{"h", "m", "l"} {
			tr, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}, "video", "me", webrtc.WithRTPStreamID(rid))
			if err != nil {
				t.Fatal(err)
			}
			c.video[rid] = tr
			if sender == nil {
				tx, err := pc.AddTransceiverFromTrack(tr, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly})
				if err != nil {
					t.Fatal(err)
				}
				sender = tx.Sender()
				c.videoSender = sender
			} else if err := sender.AddEncoding(tr); err != nil {
				t.Fatal(err)
			}
		}
	}
	pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		ch := c.stream(tr.StreamID() + "/" + tr.ID())
		go func() {
			for {
				p, _, err := tr.ReadRTP()
				if err != nil {
					return
				}
				select {
				case ch <- p:
				default:
				}
			}
		}()
	})
	return c
}

func (c *client) join(t *testing.T, s *sfu.SFU, sg *wire.SignedCallGrant) error {
	t.Helper()
	offer, err := c.pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gather := webrtc.GatheringCompletePromise(c.pc)
	if err := c.pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	<-gather
	kinds := map[string]string{}
	for _, tx := range c.pc.GetTransceivers() {
		if tx.Kind() == webrtc.RTPCodecTypeAudio {
			kinds[tx.Mid()] = sfu.KindAudio
		} else {
			kinds[tx.Mid()] = sfu.KindCamera
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ip := c.ip
	if ip == "" {
		ip = "127.0.0.1"
	}
	peer, answer, err := s.Join(ctx, &wire.CallJoin{Grant: sg, Offer: c.pc.LocalDescription().SDP, Kinds: kinds, Control: c.control}, c, ip)
	if err != nil {
		return err
	}
	c.peer = peer
	if c.videoSender != nil {
		for _, e := range c.videoSender.GetParameters().HeaderExtensions {
			switch e.URI {
			case "urn:ietf:params:rtp-hdrext:sdes:mid":
				c.midID = uint8(e.ID)
			case "urn:ietf:params:rtp-hdrext:sdes:rtp-stream-id":
				c.ridID = uint8(e.ID)
			}
		}
	}
	return c.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer})
}

// pump sends packets on a track until stop is closed. VP8 payloads carry a
// keyframe every 15 frames (the payload header bit the SFU reads in the clear).
func pump(tr *webrtc.TrackLocalStaticRTP, vp8 bool, marker byte, stop <-chan struct{}, ext ...func(*rtp.Packet)) {
	seq := uint16(1)
	ts := uint32(0)
	for i := 0; ; i++ {
		select {
		case <-stop:
			return
		case <-time.After(20 * time.Millisecond):
		}
		payload := []byte{marker, byte(i)}
		if vp8 {
			key := byte(1)
			if i%15 == 0 {
				key = 0
			}
			payload = []byte{0x10, key, marker, byte(i)}
		}
		pkt := &rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: ts, PayloadType: 111}, Payload: payload}
		for _, f := range ext {
			f(pkt)
		}
		_ = tr.WriteRTP(pkt)
		seq++
		ts += 960
	}
}

func waitPacket(t *testing.T, ch chan *rtp.Packet, want func(*rtp.Packet) bool, what string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case p := <-ch:
			if want(p) {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func TestAudioForwardedAndServerMute(t *testing.T) {
	s := newSFU(t)
	srv := newServer(t)
	alice, bob := newClient(t, false), newClient(t, false)
	ga, pa := srv.grant(t, "Alice", 1, nil)
	gb, _ := srv.grant(t, "Bob", 2, nil)
	if err := alice.join(t, s, ga); err != nil {
		t.Fatal(err)
	}
	if err := bob.join(t, s, gb); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	defer close(stop)
	go pump(alice.audio, false, 0xA1, stop)
	ch := bob.stream(hex.EncodeToString(pa) + "/audio")
	waitPacket(t, ch, func(p *rtp.Packet) bool { return len(p.Payload) == 2 && p.Payload[0] == 0xA1 }, "Alice's audio at Bob")
	// The call state lists both, with their key ids and names.
	if st := bob.lastState(); st == nil || len(st.Members) != 2 {
		t.Fatalf("call state: %v", st)
	}
	// Server mute: Alice's audio stops reaching Bob.
	ctl, _ := callauth.SignControl(srv.key, &wire.CallControl{CallId: srv.id, Seq: 1, Muted: [][]byte{pa}})
	if err := s.Update(ctl); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	for len(ch) > 0 {
		<-ch
	}
	select {
	case <-ch:
		t.Fatal("muted audio was forwarded")
	case <-time.After(500 * time.Millisecond):
	}
	// A control signed by another key is refused.
	other, _ := identity.Generate()
	bad, _ := callauth.SignControl(other, &wire.CallControl{CallId: srv.id, Seq: 2})
	if err := s.Update(bad); err == nil {
		t.Fatal("control from another key accepted")
	}
}

func TestSimulcastLayerSelection(t *testing.T) {
	s := newSFU(t)
	srv := newServer(t)
	alice, bob := newClient(t, true), newClient(t, false)
	ga, pa := srv.grant(t, "Alice", 1, nil)
	gb, _ := srv.grant(t, "Bob", 2, nil)
	if err := alice.join(t, s, ga); err != nil {
		t.Fatal(err)
	}
	if err := bob.join(t, s, gb); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	defer close(stop)
	markers := map[string]byte{"h": 0xF3, "m": 0xF2, "l": 0xF1}
	mid := ""
	for _, tx := range alice.pc.GetTransceivers() {
		if tx.Kind() == webrtc.RTPCodecTypeVideo {
			mid = tx.Mid()
		}
	}
	for rid, tr := range alice.video {
		go pump(tr, true, markers[rid], stop, func(p *rtp.Packet) {
			p.PayloadType = 96
			_ = p.SetExtension(alice.midID, []byte(mid))
			_ = p.SetExtension(alice.ridID, []byte(rid))
		})
	}
	ch := bob.stream(hex.EncodeToString(pa) + "/camera")
	layer := func(rid string) func(*rtp.Packet) bool {
		return func(p *rtp.Packet) bool { return len(p.Payload) == 4 && p.Payload[2] == markers[rid] }
	}
	// Nothing until Bob asks: receivers ask only for the videos their user
	// chose to watch.
	silent(t, ch, time.Second, "video nobody asked for")
	for _, rid := range []string{"m", "h", "l"} {
		if err := bob.peer.Layer(&wire.CallLayer{Source: pa, Kind: "camera", Rid: rid}); err != nil {
			t.Fatal(err)
		}
		waitPacket(t, ch, layer(rid), "layer "+rid)
	}
	if err := bob.peer.Layer(&wire.CallLayer{Source: pa, Kind: "camera", Rid: "x"}); err == nil {
		t.Fatal("unknown layer accepted")
	}
	// Stopping pauses it again.
	if err := bob.peer.Layer(&wire.CallLayer{Source: pa, Kind: "camera", Rid: ""}); err != nil {
		t.Fatal(err)
	}
	silent(t, ch, time.Second, "video after the receiver stopped watching")
	// Never one's own video, only participant ids, and only camera or
	// screen.
	if err := alice.peer.Layer(&wire.CallLayer{Source: pa, Kind: "camera", Rid: "m"}); err == nil {
		t.Fatal("layer of one's own video accepted")
	}
	if err := bob.peer.Layer(&wire.CallLayer{Source: []byte{1, 2}, Kind: "camera", Rid: "m"}); err == nil {
		t.Fatal("layer of a malformed id accepted")
	}
	if err := bob.peer.Layer(&wire.CallLayer{Source: pa, Kind: "audio", Rid: "m"}); err == nil {
		t.Fatal("layer of audio accepted")
	}
	// Requests for absent participants are kept only so far (when the list
	// is full they are dropped): what Bob asked of Alice, who is here, stays.
	if err := bob.peer.Layer(&wire.CallLayer{Source: pa, Kind: "camera", Rid: "m"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 500; i++ {
		id := make([]byte, 16)
		rand.Read(id)
		_ = bob.peer.Layer(&wire.CallLayer{Source: id, Kind: "camera", Rid: "l"})
	}
	alice.waitWatch(t, sfu.KindCamera, 1, "m")
	waitPacket(t, ch, layer("m"), "Alice's layer after many requests for absent participants")
}

// A layer asked of someone who isn't at the relay yet (the server lists
// people before their join reaches it) counts once they join.
func TestLayerAskedBeforeTheSourceJoins(t *testing.T) {
	s := newSFU(t)
	srv := newServer(t)
	ga, pa := srv.grant(t, "a", 1, nil)
	gb, _ := srv.grant(t, "b", 2, nil)
	alice, bob := newClient(t, true), newClient(t, false)
	if err := bob.join(t, s, gb); err != nil {
		t.Fatal(err)
	}
	if err := bob.peer.Layer(&wire.CallLayer{Source: pa, Kind: sfu.KindCamera, Rid: "m"}); err != nil {
		t.Fatal(err)
	}
	if err := alice.join(t, s, ga); err != nil {
		t.Fatal(err)
	}
	alice.waitWatch(t, sfu.KindCamera, 1, "m")
	stop := make(chan struct{})
	defer close(stop)
	sendVideo(alice, map[string]byte{"h": 0xF3, "m": 0xF2, "l": 0xF1}, stop)
	waitPacket(t, bob.stream(hex.EncodeToString(pa)+"/camera"), func(p *rtp.Packet) bool {
		return len(p.Payload) == 4 && p.Payload[2] == 0xF2
	}, "the layer asked for before Alice joined")
}

// sendVideo pumps a client's simulcast layers, each marked, until stop.
func sendVideo(c *client, markers map[string]byte, stop <-chan struct{}) {
	mid := ""
	for _, tx := range c.pc.GetTransceivers() {
		if tx.Kind() == webrtc.RTPCodecTypeVideo {
			mid = tx.Mid()
		}
	}
	for rid, tr := range c.video {
		go pump(tr, true, markers[rid], stop, func(p *rtp.Packet) {
			p.PayloadType = 96
			_ = p.SetExtension(c.midID, []byte(mid))
			_ = p.SetExtension(c.ridID, []byte(rid))
		})
	}
}

const (
	sdpAudioLevel     = "urn:ietf:params:rtp-hdrext:ssrc-audio-level"
	sdpCSRCAudioLevel = "urn:ietf:params:rtp-hdrext:csrc-audio-level"
)

// checkVoiceSDP fails if an SDP from the relay negotiates an audio level
// header extension, or Opus without constant bitrate and with DTX.
func checkVoiceSDP(t *testing.T, what, s string) {
	t.Helper()
	if strings.Contains(s, "audio-level") {
		t.Errorf("%s negotiates an audio level:\n%s", what, s)
	}
	fmtps := 0
	for _, line := range strings.Split(s, "\r\n") {
		if strings.HasPrefix(line, "a=fmtp:111 ") {
			fmtps++
			p := ";" + strings.TrimPrefix(line, "a=fmtp:111 ") + ";"
			if !strings.Contains(p, ";cbr=1;") || !strings.Contains(p, ";usedtx=0;") {
				t.Errorf("%s: Opus not at a constant bitrate without DTX: %s", what, line)
			}
		}
	}
	if fmtps == 0 {
		t.Errorf("%s has no Opus format:\n%s", what, s)
	}
}

// The relay negotiates no audio level (RFC 6464: a loudness value in the
// clear on every packet) though participants offer it, in its answers and
// in its own offers, and asks for Opus at a constant bitrate without DTX
// (packet sizes that follow speech give away what is said). A level a
// sender puts on its packets anyway isn't forwarded.
func TestVoiceNegotiatesNoAudioLevelAndConstantBitrate(t *testing.T) {
	s := newSFU(t)
	srv := newServer(t)
	ga, pa := srv.grant(t, "a", 1, nil)
	gb, _ := srv.grant(t, "b", 2, nil)
	alice, bob := newClient(t, false), newClient(t, false)
	if err := alice.join(t, s, ga); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(alice.pc.LocalDescription().SDP, sdpAudioLevel) {
		t.Fatal("the test client didn't offer an audio level")
	}
	checkVoiceSDP(t, "the answer to a", alice.pc.RemoteDescription().SDP)
	if err := bob.join(t, s, gb); err != nil {
		t.Fatal(err)
	}
	checkVoiceSDP(t, "the answer to b", bob.pc.RemoteDescription().SDP)
	stop := make(chan struct{})
	defer close(stop)
	// RFC 6464's one-byte form: voice activity bit and -30 dBov.
	go pump(alice.audio, false, 0xA1, stop, func(p *rtp.Packet) { _ = p.SetExtension(1, []byte{0x80 | 30}) })
	// b's voice arriving makes the relay offer it to a.
	go pump(bob.audio, false, 0xB1, stop)
	ch := bob.stream(hex.EncodeToString(pa) + "/audio")
	for i := 0; i < 20; i++ {
		waitPacket(t, ch, func(p *rtp.Packet) bool { return len(p.Payload) == 2 && p.Payload[0] == 0xA1 }, "a's audio at b")
	}
	for len(ch) > 0 {
		if p := <-ch; p.Extension || len(p.Extensions) > 0 {
			t.Fatalf("forwarded with header extensions: %+v", p.Header)
		}
	}
	// The relay offers a the voice b brought.
	deadline := time.Now().Add(10 * time.Second)
	for {
		alice.mu.Lock()
		offers := append([]string(nil), alice.offers...)
		alice.mu.Unlock()
		if len(offers) > 0 {
			for _, o := range offers {
				checkVoiceSDP(t, "an offer from the relay", o)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no offer from the relay")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestVoiceFormat(t *testing.T) {
	in := "v=0\r\nm=audio 9 UDP/TLS/RTP/SAVPF 111 63 0\r\na=rtpmap:111 opus/48000/2\r\n" +
		"a=fmtp:111 minptime=10;cbr=0;useinbandfec=1;usedtx=1\r\na=rtpmap:63 red/48000/2\r\na=fmtp:63 111/111\r\n" +
		"a=rtpmap:0 PCMU/8000\r\nm=audio 9 UDP/TLS/RTP/SAVPF 109\r\na=rtpmap:109 OPUS/48000/2\r\n"
	want := "v=0\r\nm=audio 9 UDP/TLS/RTP/SAVPF 111 63 0\r\na=rtpmap:111 opus/48000/2\r\n" +
		"a=fmtp:111 minptime=10;useinbandfec=1;cbr=1;usedtx=0\r\na=rtpmap:63 red/48000/2\r\na=fmtp:63 111/111\r\n" +
		"a=rtpmap:0 PCMU/8000\r\nm=audio 9 UDP/TLS/RTP/SAVPF 109\r\na=rtpmap:109 OPUS/48000/2\r\na=fmtp:109 cbr=1;usedtx=0\r\n"
	if got := sfu.VoiceFormat(in); got != want {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
}

// Audio a receiver doesn't listen to (deafened before the source sent any)
// is paused on its own track, not by taking the track off the sender: the
// receiver's later renegotiations still work (here: a camera it watches
// arrives), and when it listens again the audio arrives on a track that
// still says whose it is.
func TestPausedAudioKeepsNegotiationAndIdentity(t *testing.T) {
	s := newSFU(t)
	srv := newServer(t)
	ga, pa := srv.grant(t, "a", 1, nil)
	gb, _ := srv.grant(t, "b", 2, nil)
	alice, bob := newClient(t, true), newClient(t, false)
	if err := alice.join(t, s, ga); err != nil {
		t.Fatal(err)
	}
	if err := bob.join(t, s, gb); err != nil {
		t.Fatal(err)
	}
	if err := bob.peer.Listen(&wire.CallListen{Deaf: true}); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	defer close(stop)
	go pump(alice.audio, false, 0xA1, stop)
	audio := bob.stream(hex.EncodeToString(pa) + "/audio")
	silent(t, audio, time.Second, "audio to someone deafened")
	if err := bob.peer.Layer(&wire.CallLayer{Source: pa, Kind: sfu.KindCamera, Rid: "m"}); err != nil {
		t.Fatal(err)
	}
	sendVideo(alice, map[string]byte{"h": 0xF3, "m": 0xF2, "l": 0xF1}, stop)
	waitPacket(t, bob.stream(hex.EncodeToString(pa)+"/camera"), func(p *rtp.Packet) bool { return true }, "a camera after paused audio")
	if err := bob.peer.Listen(&wire.CallListen{}); err != nil {
		t.Fatal(err)
	}
	waitPacket(t, audio, func(p *rtp.Packet) bool { return len(p.Payload) == 2 && p.Payload[0] == 0xA1 }, "Alice's audio, as hers, once Bob listens")
}

// A sender learns who receives each kind of its media: video by who asked
// for which layer (also before it sends any), audio by who listens (not
// deafened, not muted for themselves; screen audio only for those who ask
// for it), within what its grant and the control allow. It is told on
// every change, and only about its own media.
func TestWatchersFollowLayersAndListening(t *testing.T) {
	s := newSFU(t)
	srv := newServer(t)
	ga, pa := srv.grant(t, "a", 1, nil)
	gb, pb := srv.grant(t, "b", 2, nil)
	gc, _ := srv.grant(t, "c", 3, nil)
	alice, bob, carol := newClient(t, true), newClient(t, false), newClient(t, false)
	if err := alice.join(t, s, ga); err != nil {
		t.Fatal(err)
	}
	// Alone: nobody receives anything.
	for _, kind := range []string{sfu.KindAudio, sfu.KindCamera, sfu.KindScreen, sfu.KindScreenAudio} {
		alice.waitWatch(t, kind, 0)
	}
	if err := bob.join(t, s, gb); err != nil {
		t.Fatal(err)
	}
	if err := carol.join(t, s, gc); err != nil {
		t.Fatal(err)
	}
	// Both hear her; nobody watches her camera or her screen's audio.
	alice.waitWatch(t, sfu.KindAudio, 2)
	alice.waitWatch(t, sfu.KindCamera, 0)
	alice.waitWatch(t, sfu.KindScreenAudio, 0)
	bob.waitWatch(t, sfu.KindAudio, 2)

	// Watching her camera, before she sends it: counted, with the layers.
	if err := bob.peer.Layer(&wire.CallLayer{Source: pa, Kind: sfu.KindCamera, Rid: "h"}); err != nil {
		t.Fatal(err)
	}
	alice.waitWatch(t, sfu.KindCamera, 1, "h")
	if err := carol.peer.Layer(&wire.CallLayer{Source: pa, Kind: sfu.KindCamera, Rid: "l"}); err != nil {
		t.Fatal(err)
	}
	alice.waitWatch(t, sfu.KindCamera, 2, "l", "h")
	// Bob's request was kept: once she sends, he gets the layer he asked for.
	stop := make(chan struct{})
	defer close(stop)
	markers := map[string]byte{"h": 0xF3, "m": 0xF2, "l": 0xF1}
	sendVideo(alice, markers, stop)
	waitPacket(t, bob.stream(hex.EncodeToString(pa)+"/camera"), func(p *rtp.Packet) bool {
		return len(p.Payload) == 4 && p.Payload[2] == markers["h"]
	}, "the layer Bob asked for before Alice sent")
	if err := bob.peer.Layer(&wire.CallLayer{Source: pa, Kind: sfu.KindCamera, Rid: ""}); err != nil {
		t.Fatal(err)
	}
	alice.waitWatch(t, sfu.KindCamera, 1, "l")

	// Listening: Bob deafens himself, Carol mutes Alice for herself, and the
	// relay stops forwarding her audio to them.
	go pump(alice.audio, false, 0xA1, stop)
	toCarol := carol.stream(hex.EncodeToString(pa) + "/audio")
	waitPacket(t, toCarol, func(p *rtp.Packet) bool { return true }, "Alice's audio at Carol")
	if err := bob.peer.Listen(&wire.CallListen{Deaf: true}); err != nil {
		t.Fatal(err)
	}
	alice.waitWatch(t, sfu.KindAudio, 1)
	if err := carol.peer.Listen(&wire.CallListen{NoAudio: [][]byte{pa}}); err != nil {
		t.Fatal(err)
	}
	alice.waitWatch(t, sfu.KindAudio, 0)
	silent(t, toCarol, time.Second, "audio muted for herself")
	silent(t, bob.stream(hex.EncodeToString(pa)+"/audio"), time.Second, "audio to someone deafened")
	// Bob undeafens: he counts for Alice again, and for Carol (whom nobody
	// muted) both count.
	if err := bob.peer.Listen(&wire.CallListen{}); err != nil {
		t.Fatal(err)
	}
	alice.waitWatch(t, sfu.KindAudio, 1)
	carol.waitWatch(t, sfu.KindAudio, 2)
	if err := carol.peer.Listen(&wire.CallListen{}); err != nil {
		t.Fatal(err)
	}
	alice.waitWatch(t, sfu.KindAudio, 2)
	waitPacket(t, toCarol, func(p *rtp.Packet) bool { return true }, "Alice's audio at Carol again")

	// Screen audio only for those who ask (they watch her screen, sound on).
	if err := carol.peer.Listen(&wire.CallListen{ScreenAudio: [][]byte{pa}}); err != nil {
		t.Fatal(err)
	}
	alice.waitWatch(t, sfu.KindScreenAudio, 1)

	// Server mute: nobody can hear her, so nobody receives her audio (or
	// her screen's); server deafen of Bob: he receives nobody's.
	ctl, _ := callauth.SignControl(srv.key, &wire.CallControl{CallId: srv.id, Seq: 1, Muted: [][]byte{pa}, Deafened: [][]byte{pb}})
	if err := s.Update(ctl); err != nil {
		t.Fatal(err)
	}
	alice.waitWatch(t, sfu.KindAudio, 0)
	alice.waitWatch(t, sfu.KindScreenAudio, 0)
	carol.waitWatch(t, sfu.KindAudio, 1)
	// The camera taken away (no_video): nobody receives it.
	ctl, _ = callauth.SignControl(srv.key, &wire.CallControl{CallId: srv.id, Seq: 2, NoVideo: [][]byte{pa}})
	if err := s.Update(ctl); err != nil {
		t.Fatal(err)
	}
	alice.waitWatch(t, sfu.KindCamera, 0)
	alice.waitWatch(t, sfu.KindAudio, 2)
	// Carol leaves: what she received isn't counted any more.
	carol.peer.Leave()
	alice.waitWatch(t, sfu.KindAudio, 1)
	alice.waitWatch(t, sfu.KindScreenAudio, 0)

	// Oversized listen lists are refused.
	big := make([][]byte, 1000)
	for i := range big {
		big[i] = make([]byte, 16)
	}
	if err := bob.peer.Listen(&wire.CallListen{NoAudio: big}); err == nil {
		t.Fatal("listen list of 1000 accepted")
	}
}

func TestAdmission(t *testing.T) {
	s := newSFU(t)
	srv := newServer(t)
	c := newClient(t, false)
	// Expired.
	sg, _ := srv.grant(t, "x", 1, func(g *wire.CallGrant) { g.ExpiresAt = time.Now().Add(-time.Second).UnixMilli() })
	if err := c.join(t, s, sg); err == nil {
		t.Fatal("expired grant accepted")
	}
	// A call id that doesn't commit to the signing key.
	other := newServer(t)
	sg, _ = srv.grant(t, "x", 1, func(g *wire.CallGrant) { g.CallId = other.id })
	if err := newClient(t, false).join(t, s, sg); err == nil {
		t.Fatal("grant for another server's call accepted")
	}
	// Forged signature.
	sg, _ = srv.grant(t, "x", 1, nil)
	sg.Signature[0] ^= 1
	if err := newClient(t, false).join(t, s, sg); err == nil {
		t.Fatal("forged grant accepted")
	}
	// Full call.
	g1, _ := srv.grant(t, "a", 1, func(g *wire.CallGrant) { g.MaxParticipants = 1 })
	if err := newClient(t, false).join(t, s, g1); err != nil {
		t.Fatal(err)
	}
	g2, _ := srv.grant(t, "b", 2, func(g *wire.CallGrant) { g.MaxParticipants = 1 })
	if err := newClient(t, false).join(t, s, g2); err == nil {
		t.Fatal("joined a full call")
	}
	// Removed participants can't come back with another grant for the same id.
	srv2 := newServer(t)
	gr, pr := srv2.grant(t, "r", 1, nil)
	cr := newClient(t, false)
	if err := cr.join(t, s, gr); err != nil {
		t.Fatal(err)
	}
	ctl, _ := callauth.SignControl(srv2.key, &wire.CallControl{CallId: srv2.id, Seq: 1, Removed: [][]byte{pr}})
	if err := s.Update(ctl); err != nil {
		t.Fatal(err)
	}
	again := &wire.CallGrant{}
	_ = proto.Unmarshal(gr.Grant, again)
	sg2, _ := callauth.SignGrant(srv2.key, again)
	if err := newClient(t, false).join(t, s, sg2); err == nil {
		t.Fatal("removed participant rejoined")
	}
}

// silent fails if any packet matching want arrives on ch within d (after
// draining what was already queued).
func silent(t *testing.T, ch chan *rtp.Packet, d time.Duration, what string) {
	t.Helper()
	time.Sleep(300 * time.Millisecond)
	for len(ch) > 0 {
		<-ch
	}
	select {
	case <-ch:
		t.Fatalf("%s: packets still arrive", what)
	case <-time.After(d):
	}
}

// The first participant brings the call's control: a relay that never got
// the server's update (new call, after a move) still enforces server mute.
func TestControlBroughtByTheFirstJoiner(t *testing.T) {
	s := newSFU(t)
	srv := newServer(t)
	ga, pa := srv.grant(t, "a", 1, nil)
	gb, _ := srv.grant(t, "b", 2, nil)
	alice, bob := newClient(t, false), newClient(t, false)
	alice.control, _ = callauth.SignControl(srv.key, &wire.CallControl{CallId: srv.id, Seq: 7, Muted: [][]byte{pa}})
	if err := alice.join(t, s, ga); err != nil {
		t.Fatal(err)
	}
	if err := bob.join(t, s, gb); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	defer close(stop)
	go pump(alice.audio, false, 0xA1, stop)
	silent(t, bob.stream(hex.EncodeToString(pa)+"/audio"), time.Second, "server-muted audio")
	// A control signed by another key, or for another call, is refused at join.
	other, _ := identity.Generate()
	carol := newClient(t, false)
	carol.control, _ = callauth.SignControl(other, &wire.CallControl{CallId: srv.id, Seq: 9})
	gc, _ := srv.grant(t, "c", 3, nil)
	if err := carol.join(t, s, gc); err == nil {
		t.Fatal("forged control accepted at join")
	}
}

// Server deafen: the deafened participant gets no audio.
func TestServerDeafen(t *testing.T) {
	s := newSFU(t)
	srv := newServer(t)
	ga, pa := srv.grant(t, "a", 1, nil)
	gb, pb := srv.grant(t, "b", 2, nil)
	alice, bob := newClient(t, false), newClient(t, false)
	if err := alice.join(t, s, ga); err != nil {
		t.Fatal(err)
	}
	if err := bob.join(t, s, gb); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	defer close(stop)
	go pump(alice.audio, false, 0xA1, stop)
	ch := bob.stream(hex.EncodeToString(pa) + "/audio")
	waitPacket(t, ch, func(p *rtp.Packet) bool { return true }, "audio before deafen")
	ctl, _ := callauth.SignControl(srv.key, &wire.CallControl{CallId: srv.id, Seq: 1, Deafened: [][]byte{pb}})
	if err := s.Update(ctl); err != nil {
		t.Fatal(err)
	}
	silent(t, ch, time.Second, "audio to a deafened participant")
}

// What a grant doesn't allow, or a control takes away, isn't forwarded:
// audio without Speak, camera with no_video, screen audio when muted.
func TestGrantsAndControlsGateMedia(t *testing.T) {
	s := newSFU(t)
	srv := newServer(t)
	ga, pa := srv.grant(t, "a", 1, func(g *wire.CallGrant) { g.Audio = false })
	gb, pb := srv.grant(t, "b", 2, nil)
	gc, _ := srv.grant(t, "c", 3, nil)
	alice, bob, carol := newClient(t, false), newClient(t, true), newClient(t, false)
	for c, g := range map[*client]*wire.SignedCallGrant{alice: ga, bob: gb, carol: gc} {
		if err := c.join(t, s, g); err != nil {
			t.Fatal(err)
		}
	}
	stop := make(chan struct{})
	defer close(stop)
	go pump(alice.audio, false, 0xA1, stop)
	mid := ""
	for _, tx := range bob.pc.GetTransceivers() {
		if tx.Kind() == webrtc.RTPCodecTypeVideo {
			mid = tx.Mid()
		}
	}
	for rid, tr := range bob.video {
		go pump(tr, true, 0xB0, stop, func(p *rtp.Packet) {
			p.PayloadType = 96
			_ = p.SetExtension(bob.midID, []byte(mid))
			_ = p.SetExtension(bob.ridID, []byte(rid))
		})
	}
	silent(t, carol.stream(hex.EncodeToString(pa)+"/audio"), time.Second, "audio from a grant without Speak")
	cam := carol.stream(hex.EncodeToString(pb) + "/camera")
	if err := carol.peer.Layer(&wire.CallLayer{Source: pb, Kind: sfu.KindCamera, Rid: "m"}); err != nil {
		t.Fatal(err)
	}
	waitPacket(t, cam, func(p *rtp.Packet) bool { return true }, "bob's camera")
	ctl, _ := callauth.SignControl(srv.key, &wire.CallControl{CallId: srv.id, Seq: 1, NoVideo: [][]byte{pb}})
	if err := s.Update(ctl); err != nil {
		t.Fatal(err)
	}
	silent(t, cam, time.Second, "camera after no_video")
}

// One address can only hold so many places; kinds can't repeat; controls
// can't be huge.
func TestRelayLimits(t *testing.T) {
	s, err := sfu.New(sfu.Config{IncludeLoopback: true, MaxPeersPerIP: 2, Log: testutil.Quiet()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	srv := newServer(t)
	for i := 1; i <= 2; i++ {
		g, _ := srv.grant(t, "x", uint32(i), nil)
		if err := newClient(t, false).join(t, s, g); err != nil {
			t.Fatal(err)
		}
	}
	g3, _ := srv.grant(t, "x", 3, nil)
	if err := newClient(t, false).join(t, s, g3); err == nil {
		t.Fatal("third participant from one address admitted")
	}
	// The limit counts the IPv6 /64, so other addresses of the same /64
	// don't get places of their own.
	for i, ip := range []string{"2001:db8::1", "2001:db8::2"} {
		g, _ := srv.grant(t, "x", uint32(10+i), nil)
		c := newClient(t, false)
		c.ip = ip
		if err := c.join(t, s, g); err != nil {
			t.Fatalf("participant from %s: %v", ip, err)
		}
	}
	g12, _ := srv.grant(t, "x", 12, nil)
	same := newClient(t, false)
	same.ip = "2001:db8::ffff:1"
	if err := same.join(t, s, g12); err == nil {
		t.Fatal("third participant from one IPv6 /64 admitted")
	}
	g13, _ := srv.grant(t, "x", 13, nil)
	other := newClient(t, false)
	other.ip = "2001:db8:0:1::1"
	if err := other.join(t, s, g13); err != nil {
		t.Fatalf("participant from another /64: %v", err)
	}
	// Repeated kinds.
	g4, _ := srv.grant(t, "x", 4, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := s.Join(ctx, &wire.CallJoin{Grant: g4, Kinds: map[string]string{"0": "audio", "1": "audio"}}, newClient(t, false), "10.0.0.9"); err == nil {
		t.Fatal("repeated kind accepted")
	}
	// Oversized control lists.
	big := make([][]byte, 1001)
	for i := range big {
		big[i] = make([]byte, 16)
	}
	ctl, _ := callauth.SignControl(srv.key, &wire.CallControl{CallId: srv.id, Seq: 2, Removed: big})
	if err := s.Update(ctl); err == nil {
		t.Fatal("control with 1001 removals accepted")
	}
}

// Controls listing many removed participants don't grow the relay's
// memory without bound: a call keeps the newest removals, the relay the
// newest across its calls, and a control naming anything but participant
// ids is refused.
func TestRemovalsBounded(t *testing.T) {
	s := newSFU(t)
	srv := newServer(t)
	sg, _ := srv.grant(t, "a", 1, nil)
	c := newClient(t, false)
	if err := c.join(t, s, sg); err != nil {
		t.Fatal(err)
	}
	ids := func(n, size int) [][]byte {
		out := make([][]byte, n)
		for i := range out {
			out[i] = make([]byte, size)
			rand.Read(out[i])
		}
		return out
	}
	ctl, _ := callauth.SignControl(srv.key, &wire.CallControl{CallId: srv.id, Seq: 1, Removed: ids(1, 1024)})
	if err := s.Update(ctl); err == nil {
		t.Fatal("control with a kilobyte removed id accepted")
	}
	const updates = 100
	for i := 2; i <= updates+1; i++ {
		ctl, _ := callauth.SignControl(srv.key, &wire.CallControl{CallId: srv.id, Seq: uint64(i), Removed: ids(callauth.MaxControlList, 16)})
		if err := s.Update(ctl); err != nil {
			t.Fatal(err)
		}
	}
	all, queued, inCall := s.Removals(srv.id)
	if inCall > sfu.MaxRemoved {
		t.Fatalf("call remembers %d removals, bound %d", inCall, sfu.MaxRemoved)
	}
	if all > sfu.MaxRemembered || queued > sfu.MaxRemembered {
		t.Fatalf("relay remembers %d removals (%d queued), bound %d", all, queued, sfu.MaxRemembered)
	}
	if all == 0 || inCall == 0 {
		t.Fatal("removals forgotten altogether")
	}
	// The newest are the ones kept: the last control's removals are still
	// refused.
	last := &wire.CallControl{}
	_ = proto.Unmarshal(ctl.Control, last)
	g, _ := srv.grant(t, "r", 2, func(g *wire.CallGrant) { g.Participant = last.Removed[0] })
	if err := newClient(t, false).join(t, s, g); err == nil {
		t.Fatal("participant removed by the latest control joined")
	}
}

// A relay that lost its state (restarted) takes the first control it is
// brought, stale or not: someone removed before can get back in with a
// grant that hasn't expired. The next newer control, brought by any other
// joiner, puts them out again.
func TestStaleControlReplacedByANewerOne(t *testing.T) {
	s := newSFU(t)
	srv := newServer(t)
	gx, px := srv.grant(t, "x", 1, nil)
	gy, _ := srv.grant(t, "y", 2, nil)
	x, y := newClient(t, false), newClient(t, false)
	x.control, _ = callauth.SignControl(srv.key, &wire.CallControl{CallId: srv.id, Seq: 3})
	if err := x.join(t, s, gx); err != nil {
		t.Fatal(err)
	}
	y.control, _ = callauth.SignControl(srv.key, &wire.CallControl{CallId: srv.id, Seq: 5, Removed: [][]byte{px}})
	if err := y.join(t, s, gy); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for st := x.lastState(); st == nil || st.Ended == ""; st = x.lastState() {
		if time.Now().After(deadline) {
			t.Fatal("participant removed by a newer control still in the call")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Nor can x come back on the stale control, or on none.
	sg, _ := callauth.SignGrant(srv.key, mustGrant(t, gx))
	again := newClient(t, false)
	again.control = x.control
	if err := again.join(t, s, sg); err == nil {
		t.Fatal("removed participant rejoined on a stale control")
	}
	if err := newClient(t, false).join(t, s, sg); err == nil {
		t.Fatal("removed participant rejoined")
	}
}

// mustGrant is the grant inside a signed one.
func mustGrant(t *testing.T, sg *wire.SignedCallGrant) *wire.CallGrant {
	t.Helper()
	g := &wire.CallGrant{}
	if err := proto.Unmarshal(sg.Grant, g); err != nil {
		t.Fatal(err)
	}
	return g
}

// What the WebRTC stack logs reaches the relay's log at debug level without
// the peer addresses ICE names (docs/operations.md "Logs").
func TestPionLogsCarryNoAddresses(t *testing.T) {
	var buf lockedBuffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	l := sfu.NewPionLogs(log).NewLogger("ice")
	l.Warnf("ping error from %s to %s", "203.0.113.7:51234", "[2001:db8::1]:3478")
	l.Error("conn 198.51.100.4 timed out")
	l.Info("candidate 192.0.2.9:1")
	got := buf.String()
	for _, addr := range []string{"203.0.113.7", "2001:db8::1", "198.51.100.4", "192.0.2.9"} {
		if strings.Contains(got, addr) {
			t.Errorf("log names %s:\n%s", addr, got)
		}
	}
	if !strings.Contains(got, "ping error") || !strings.Contains(got, "timed out") || strings.Contains(got, "candidate") {
		t.Fatalf("warnings and errors at debug, info dropped:\n%s", got)
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// A refused join leaves no call behind: anyone can sign grants for calls of
// their own, so a call kept after its only join failed (removed by the
// control the joiner brought, a full call, a bad offer) would hold one of
// the relay's MaxCalls places for good.
func TestRefusedJoinLeavesNoCall(t *testing.T) {
	s, err := sfu.New(sfu.Config{IncludeLoopback: true, MaxCalls: 2, Log: testutil.Quiet()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	waitCalls := func(want int, what string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for s.Calls() != want {
			if time.Now().After(deadline) {
				t.Fatalf("%s: %d calls, want %d", what, s.Calls(), want)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	for i := 0; i < 3; i++ {
		// Removed by the control it brought (refused before any peer
		// connection exists).
		srv := newServer(t)
		g, p := srv.grant(t, "x", 1, nil)
		c := newClient(t, false)
		c.control, _ = callauth.SignControl(srv.key, &wire.CallControl{CallId: srv.id, Seq: 1, Removed: [][]byte{p}})
		if err := c.join(t, s, g); err == nil {
			t.Fatal("participant removed by its own control joined")
		}
		waitCalls(0, "after a removed join")
		// A bad offer (refused after the peer connection was made).
		srv = newServer(t)
		g, _ = srv.grant(t, "x", 1, nil)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _, err := s.Join(ctx, &wire.CallJoin{Grant: g, Offer: "v=0\r\n", Kinds: map[string]string{"0": sfu.KindAudio}}, newClient(t, false), "127.0.0.1")
		cancel()
		if err == nil {
			t.Fatal("bad offer accepted")
		}
		waitCalls(0, "after a bad offer")
	}
	// The relay still has room for its two calls, and a call whose last
	// participant leaves is gone too.
	var peers []*sfu.Peer
	for i := 0; i < 2; i++ {
		srv := newServer(t)
		g, _ := srv.grant(t, "x", 1, nil)
		c := newClient(t, false)
		if err := c.join(t, s, g); err != nil {
			t.Fatalf("call %d refused after refused joins: %v", i+1, err)
		}
		peers = append(peers, c.peer)
	}
	waitCalls(2, "two live calls")
	for _, p := range peers {
		p.Leave()
	}
	waitCalls(0, "after everyone left")
}

// A participant joining again (a connection that dropped without a leave,
// or a rejoin before the relay noticed) replaces its earlier connection.
func TestRejoinReplacesTheEarlierConnection(t *testing.T) {
	s := newSFU(t)
	srv := newServer(t)
	g, _ := srv.grant(t, "a", 1, nil)
	first, second := newClient(t, true), newClient(t, true)
	if err := first.join(t, s, g); err != nil {
		t.Fatal(err)
	}
	if err := second.join(t, s, g); err != nil {
		t.Fatalf("joining again: %v", err)
	}
	if s.Calls() != 1 {
		t.Fatalf("%d calls", s.Calls())
	}
	first.peer.Leave() // long gone from the call: changes nothing
	second.peer.Leave()
	deadline := time.Now().Add(5 * time.Second)
	for s.Calls() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d calls after everyone left", s.Calls())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Asking for what one already gets sends nobody a CallWatchers; a burst of
// changes makes a few rounds, not one per change; and a control naming a
// thousand participants still applies to the ones in the call.
func TestWatchersRoundsFollowChanges(t *testing.T) {
	s := newSFU(t)
	srv := newServer(t)
	ga, pa := srv.grant(t, "a", 1, nil)
	gb, pb := srv.grant(t, "b", 2, nil)
	alice, bob := newClient(t, true), newClient(t, false)
	if err := alice.join(t, s, ga); err != nil {
		t.Fatal(err)
	}
	if err := bob.join(t, s, gb); err != nil {
		t.Fatal(err)
	}
	alice.waitWatch(t, sfu.KindAudio, 1)
	rounds := func() int {
		alice.mu.Lock()
		defer alice.mu.Unlock()
		return len(alice.watchers)
	}
	before := rounds()
	for range 50 {
		if err := bob.peer.Listen(&wire.CallListen{}); err != nil {
			t.Fatal(err)
		}
		if err := bob.peer.Layer(&wire.CallLayer{Source: pa, Kind: sfu.KindCamera, Rid: ""}); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(300 * time.Millisecond)
	if n := rounds(); n != before {
		t.Fatalf("%d CallWatchers for requests that changed nothing", n-before)
	}
	for i := range 100 {
		if err := bob.peer.Listen(&wire.CallListen{Deaf: i%2 == 0}); err != nil {
			t.Fatal(err)
		}
	}
	alice.waitWatch(t, sfu.KindAudio, 1)
	time.Sleep(300 * time.Millisecond)
	if n := rounds() - before; n > 10 {
		t.Fatalf("%d CallWatchers for a burst of 100 changes", n)
	}
	deafened := make([][]byte, 0, 1000)
	for i := range 999 {
		other := make([]byte, 16)
		other[0], other[1] = byte(i>>8), byte(i)
		deafened = append(deafened, other)
	}
	deafened = append(deafened, pb)
	ctl, _ := callauth.SignControl(srv.key, &wire.CallControl{CallId: srv.id, Seq: 1, Deafened: deafened})
	if err := s.Update(ctl); err != nil {
		t.Fatal(err)
	}
	alice.waitWatch(t, sfu.KindAudio, 0)
}
