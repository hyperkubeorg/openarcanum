package relay_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/pion/webrtc/v4"
	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/callauth"
	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/pow"
	"github.com/hyperkubeorg/openarcanum/internal/relay"
	"github.com/hyperkubeorg/openarcanum/internal/sfu"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// rawConn speaks relay frames directly, for the call frames the Go relay
// client doesn't use.
type rawConn struct {
	t       *testing.T
	ws      *websocket.Conn
	backlog []*wire.RelayFrame // read but not matched yet
}

func rawDial(t *testing.T, url string) *rawConn {
	t.Helper()
	ws, _, err := websocket.Dial(ctx(t), url, nil)
	if err != nil {
		t.Fatal(err)
	}
	ws.SetReadLimit(1 << 22)
	t.Cleanup(func() { ws.CloseNow() })
	c := &rawConn{t: t, ws: ws}
	startRaw(t, ws)
	return c
}

// startRaw reads the relay's Hello on a hand-driven connection and sends
// Start (version 1, solving any proof-of-work), waiting for the Ack. It
// returns the Hello.
func startRaw(t testing.TB, ws *websocket.Conn) *wire.Hello {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, b, err := ws.Read(c)
	if err != nil {
		t.Fatal(err)
	}
	var f wire.RelayFrame
	if proto.Unmarshal(b, &f) != nil || f.GetHello() == nil {
		t.Fatal("no hello")
	}
	h := f.GetHello()
	counter, err := pow.Solve(c, h.Nonce, h.PowBits)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := proto.Marshal(&wire.RelayFrame{Kind: &wire.RelayFrame_Start{Start: &wire.Start{Version: 1, Pow: counter, Req: 1}}})
	if err := ws.Write(c, websocket.MessageBinary, st); err != nil {
		t.Fatal(err)
	}
	_, b, err = ws.Read(c)
	if err != nil {
		t.Fatal(err)
	}
	var r wire.RelayFrame
	if proto.Unmarshal(b, &r) != nil || r.GetAck().GetReq() != 1 {
		t.Fatalf("start not acknowledged: %v", &r)
	}
	return h
}

func (c *rawConn) send(f *wire.RelayFrame) {
	b, _ := proto.Marshal(f)
	if err := c.ws.Write(ctx(c.t), websocket.MessageBinary, b); err != nil {
		c.t.Fatal(err)
	}
}

// next returns the first frame, kept or newly read, that matches.
func (c *rawConn) next(match func(*wire.RelayFrame) bool) *wire.RelayFrame {
	c.t.Helper()
	for i, f := range c.backlog {
		if match(f) {
			c.backlog = append(c.backlog[:i], c.backlog[i+1:]...)
			return f
		}
	}
	for {
		_, b, err := c.ws.Read(ctx(c.t))
		if err != nil {
			c.t.Fatalf("read: %v", err)
		}
		f := &wire.RelayFrame{}
		if err := proto.Unmarshal(b, f); err != nil {
			c.t.Fatal(err)
		}
		if match(f) {
			return f
		}
		c.backlog = append(c.backlog, f)
	}
}

func startWithCalls(t *testing.T) string {
	media, err := sfu.New(sfu.Config{IncludeLoopback: true, Log: testutil.Quiet()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = media.Close() })
	srv, url := start(t, nil)
	srv.SetSFU(media)
	return url
}

type callServer struct {
	key   *identity.Key
	nonce []byte
}

func (s *callServer) grant(t *testing.T, name string, mut ...func(*wire.CallGrant)) (*wire.SignedCallGrant, []byte) {
	p := make([]byte, 16)
	rand.Read(p)
	g := &wire.CallGrant{
		CallId: callauth.CallID(s.key.Public, s.nonce), ServerKey: s.key.Public, CallNonce: s.nonce,
		Participant: p, Kid: 1, Audio: true, ExpiresAt: time.Now().Add(time.Minute).UnixMilli(),
		MaxParticipants: 10,
	}
	for _, f := range mut {
		f(g)
	}
	sg, err := callauth.SignGrant(s.key, g)
	if err != nil {
		t.Fatal(err)
	}
	return sg, p
}

func audioOffer(t *testing.T) (*webrtc.PeerConnection, string, map[string]string) {
	se := webrtc.SettingEngine{}
	se.SetIncludeLoopbackCandidate(true)
	pc, err := webrtc.NewAPI(webrtc.WithSettingEngine(se)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	tr, _ := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "audio", "me")
	tx, err := pc.AddTransceiverFromTrack(tr, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly})
	if err != nil {
		t.Fatal(err)
	}
	offer, _ := pc.CreateOffer(nil)
	gather := webrtc.GatheringCompletePromise(pc)
	_ = pc.SetLocalDescription(offer)
	<-gather
	return pc, pc.LocalDescription().SDP, map[string]string{tx.Mid(): sfu.KindAudio}
}

func isErr(f *wire.RelayFrame) bool { return f.GetError() != nil }

func TestCallsNeedAnSFU(t *testing.T) {
	_, url := start(t, func(c *relay.Config) { c.Calls = false })
	c := rawDial(t, url)
	k, _ := identity.Generate()
	srv := &callServer{key: k, nonce: make([]byte, 16)}
	g, _ := srv.grant(t, "a")
	c.send(&wire.RelayFrame{Kind: &wire.RelayFrame_CallJoin{CallJoin: &wire.CallJoin{Grant: g, Req: 1}}})
	if e := c.next(isErr).GetError(); e.Code != wire.ErrorCode_ERROR_CODE_NO_CALLS {
		t.Fatalf("got %v", e)
	}
}

func TestCallOverRelayFrames(t *testing.T) {
	url := startWithCalls(t)
	k, _ := identity.Generate()
	srv := &callServer{key: k, nonce: make([]byte, 16)}
	rand.Read(srv.nonce)
	members := func(n int) func(*wire.RelayFrame) bool {
		return func(f *wire.RelayFrame) bool {
			return f.GetCallState() != nil && len(f.GetCallState().Members) == n
		}
	}

	alice := rawDial(t, url)
	apc, offer, kinds := audioOffer(t)
	ga, _ := srv.grant(t, "Alice")
	alice.send(&wire.RelayFrame{Kind: &wire.RelayFrame_CallJoin{CallJoin: &wire.CallJoin{Grant: ga, Offer: offer, Kinds: kinds, Req: 7}}})
	ans := alice.next(func(f *wire.RelayFrame) bool { return f.GetCallAnswer() != nil || isErr(f) })
	if ans.GetCallAnswer() == nil || ans.GetCallAnswer().Req != 7 {
		t.Fatalf("join: %v", ans)
	}
	if err := apc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: ans.GetCallAnswer().Answer}); err != nil {
		t.Fatal(err)
	}
	callID := ans.GetCallAnswer().CallId

	bob := rawDial(t, url)
	_, offer, kinds = audioOffer(t)
	gb, bobID := srv.grant(t, "Bob")
	bob.send(&wire.RelayFrame{Kind: &wire.RelayFrame_CallJoin{CallJoin: &wire.CallJoin{Grant: gb, Offer: offer, Kinds: kinds, Req: 1}}})
	bob.next(func(f *wire.RelayFrame) bool { return f.GetCallAnswer() != nil })
	bob.next(members(2))
	alice.next(members(2))

	// A control not signed by the call's server is refused.
	other, _ := identity.Generate()
	bad, _ := callauth.SignControl(other, &wire.CallControl{CallId: callID, Seq: 1})
	bob.send(&wire.RelayFrame{Kind: &wire.RelayFrame_CallUpdate{CallUpdate: &wire.CallUpdate{Control: bad}}})
	if e := bob.next(isErr).GetError(); e.Code != wire.ErrorCode_ERROR_CODE_CALL_REFUSED {
		t.Fatalf("got %v", e)
	}
	// Signals for a call this connection isn't in are refused.
	alice.send(&wire.RelayFrame{Kind: &wire.RelayFrame_CallSignal{CallSignal: &wire.CallSignal{CallId: []byte("nope"), Type: "answer"}}})
	alice.next(isErr)

	// Closing the connection leaves the call.
	alice.ws.Close(websocket.StatusNormalClosure, "")
	bob.next(members(1))

	// The server removes Bob: he's told, and disconnected from the call.
	ctl, _ := callauth.SignControl(k, &wire.CallControl{CallId: callID, Seq: 2, Removed: [][]byte{bobID}})
	bob.send(&wire.RelayFrame{Kind: &wire.RelayFrame_CallUpdate{CallUpdate: &wire.CallUpdate{Control: ctl}}})
	if st := bob.next(func(f *wire.RelayFrame) bool { return f.GetCallState().GetEnded() != "" }); st == nil {
		t.Fatal("no end notice")
	}
}

// watching returns a matcher for a CallWatchers frame in which kind has n
// receivers and these layers.
func watching(kind string, n uint32, layers ...string) func(*wire.RelayFrame) bool {
	return func(f *wire.RelayFrame) bool {
		for _, w := range f.GetCallWatchers().GetWatches() {
			if w.Kind == kind {
				return w.Receivers == n && fmt.Sprint(w.Layers) == fmt.Sprint(layers)
			}
		}
		return false
	}
}

// Watcher signalling over relay frames: each participant's own connection
// gets CallWatchers about its own media only (call id set), and follows
// the others' CallLayer and CallListen; CallListen for a call the
// connection isn't in is refused.
func TestCallWatchersAndListenOverRelayFrames(t *testing.T) {
	url := startWithCalls(t)
	k, _ := identity.Generate()
	srv := &callServer{key: k, nonce: make([]byte, 16)}
	rand.Read(srv.nonce)
	video := func(g *wire.CallGrant) { g.Video = true }

	alice := rawDial(t, url)
	apc, offer, kinds := audioOffer(t)
	ga, aliceID := srv.grant(t, "Alice", video)
	alice.send(&wire.RelayFrame{Kind: &wire.RelayFrame_CallJoin{CallJoin: &wire.CallJoin{Grant: ga, Offer: offer, Kinds: kinds, Req: 1}}})
	ans := alice.next(func(f *wire.RelayFrame) bool { return f.GetCallAnswer() != nil || isErr(f) })
	if ans.GetCallAnswer() == nil {
		t.Fatalf("join: %v", ans)
	}
	if err := apc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: ans.GetCallAnswer().Answer}); err != nil {
		t.Fatal(err)
	}
	callID := ans.GetCallAnswer().CallId
	// Alone: nobody hears her.
	if w := alice.next(watching(sfu.KindAudio, 0)); !bytes.Equal(w.GetCallWatchers().CallId, callID) {
		t.Fatalf("watchers without the call id: %v", w)
	}

	bob := rawDial(t, url)
	_, offer, kinds = audioOffer(t)
	gb, bobID := srv.grant(t, "Bob", video)
	bob.send(&wire.RelayFrame{Kind: &wire.RelayFrame_CallJoin{CallJoin: &wire.CallJoin{Grant: gb, Offer: offer, Kinds: kinds, Req: 1}}})
	bob.next(func(f *wire.RelayFrame) bool { return f.GetCallAnswer() != nil })
	alice.next(watching(sfu.KindAudio, 1))
	bob.next(watching(sfu.KindAudio, 1))

	// Bob watches Alice's camera: only Alice is told.
	bob.send(&wire.RelayFrame{Kind: &wire.RelayFrame_CallLayer{CallLayer: &wire.CallLayer{CallId: callID, Source: aliceID, Kind: sfu.KindCamera, Rid: "m"}}})
	alice.next(watching(sfu.KindCamera, 1, "m"))
	// Bob deafens himself: nobody hears Alice now; then he mutes her only.
	bob.send(&wire.RelayFrame{Kind: &wire.RelayFrame_CallListen{CallListen: &wire.CallListen{CallId: callID, Deaf: true}}})
	alice.next(watching(sfu.KindAudio, 0))
	bob.send(&wire.RelayFrame{Kind: &wire.RelayFrame_CallListen{CallListen: &wire.CallListen{CallId: callID}}})
	alice.next(watching(sfu.KindAudio, 1))
	bob.send(&wire.RelayFrame{Kind: &wire.RelayFrame_CallListen{CallListen: &wire.CallListen{CallId: callID, NoAudio: [][]byte{aliceID}}}})
	alice.next(watching(sfu.KindAudio, 0))
	// Alice mutes Bob for herself: Bob is told nobody hears him, and was
	// never told about Alice's camera (only about his own media).
	alice.send(&wire.RelayFrame{Kind: &wire.RelayFrame_CallListen{CallListen: &wire.CallListen{CallId: callID, NoAudio: [][]byte{bobID}}}})
	bob.next(watching(sfu.KindAudio, 0))
	for _, f := range bob.backlog {
		if w := f.GetCallWatchers(); w != nil {
			for _, x := range w.Watches {
				if x.Kind == sfu.KindCamera && x.Receivers != 0 {
					t.Fatalf("Bob was told about Alice's camera: %v", w)
				}
			}
		}
	}

	// Not in that call: refused.
	bob.send(&wire.RelayFrame{Kind: &wire.RelayFrame_CallListen{CallListen: &wire.CallListen{CallId: []byte("nope"), Deaf: true}}})
	if e := bob.next(isErr).GetError(); e.Code != wire.ErrorCode_ERROR_CODE_CALL_REFUSED {
		t.Fatalf("got %v", e)
	}
	// A client can't send CallWatchers.
	bob.send(&wire.RelayFrame{Kind: &wire.RelayFrame_CallWatchers{CallWatchers: &wire.CallWatchers{CallId: callID}}})
	if e := bob.next(isErr).GetError(); e.Code != wire.ErrorCode_ERROR_CODE_BAD_FRAME {
		t.Fatalf("got %v", e)
	}
}
