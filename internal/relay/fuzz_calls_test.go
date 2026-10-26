package relay_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/pion/webrtc/v4"
	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/callauth"
	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/relay"
	"github.com/hyperkubeorg/openarcanum/internal/sfu"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Bits of FuzzRelayCallFrames' flags argument.
const (
	fzSecondCall  = 1 << iota // join a call nobody else is in (else the anchor's)
	fzJoinControl             // bring a control with the join
	fzJunkControl             // ...unsigned bytes instead of a signed control
	fzUpdate                  // send a signed CallUpdate after the join
	fzMedia                   // the grant allows video and screen share too
	fzRemoveSelf              // the signed control removes the joiner
	fzJoinTwice               // a second join on the same connection
	fzExpired                 // the grant has expired
)

// fuzzOffer is a real WebRTC offer with the given send kinds, and the mids
// that carry them.
func fuzzOffer(tb testing.TB, kinds ...string) (*webrtc.PeerConnection, string, map[string]string) {
	tb.Helper()
	se := webrtc.SettingEngine{}
	se.SetIncludeLoopbackCandidate(true)
	pc, err := webrtc.NewAPI(webrtc.WithSettingEngine(se)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		tb.Fatal(err)
	}
	for _, k := range kinds {
		codec := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}
		if k == sfu.KindAudio || k == sfu.KindScreenAudio {
			codec = webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}
		}
		tr, err := webrtc.NewTrackLocalStaticRTP(codec, k, "fuzz")
		if err != nil {
			tb.Fatal(err)
		}
		if _, err := pc.AddTransceiverFromTrack(tr, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly}); err != nil {
			tb.Fatal(err)
		}
	}
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		tb.Fatal(err)
	}
	gather := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		tb.Fatal(err)
	}
	<-gather
	// Mids are assigned when the offer is set.
	mids := map[string]string{}
	for i, tx := range pc.GetTransceivers() {
		mids[tx.Mid()] = kinds[i]
	}
	return pc, pc.LocalDescription().SDP, mids
}

// idsFrom cuts b into 16-byte participant ids, leaving out skip.
func idsFrom(b, skip []byte) [][]byte {
	var out [][]byte
	for len(b) >= 16 {
		if !bytes.Equal(b[:16], skip) {
			out = append(out, b[:16:16])
		}
		b = b[16:]
	}
	return out
}

// FuzzRelayCallFrames fuzzes the call plane of a relay that carries calls:
// a join with a validly signed grant (so the relay gets past the signature
// to the grant's fields, the media kinds, the control the joiner brings and
// the SDP offer, all fuzzed), signed and unsigned moderator controls, then
// arbitrary relay frames (CallSignal answers, CallLayer, CallListen,
// CallLeave, CallUpdate and anything else) on the same connection. Grant
// and control decoding (callauth.VerifyGrant, VerifyControl, ControlCallID)
// and the SFU's offer, answer, layer and listen handling are all reachable
// by any client.
//
// Another participant (the anchor) stays in one call for the whole run, so
// fuzzed joins attach to a live call and fuzzed controls reach it. Whatever
// arrives: no panic, the connection is released when it closes, the anchor
// is never disconnected, and a call only the fuzzed client joined is gone
// once it leaves.
func FuzzRelayCallFrames(f *testing.F) {
	// A fixed server key and nonces, so seeds can name the calls.
	server, err := identity.FromSeed(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		f.Fatal(err)
	}
	anchorNonce := bytes.Repeat([]byte{1}, 16)
	otherNonce := bytes.Repeat([]byte{2}, 16)
	anchorCall := callauth.CallID(server.Public, anchorNonce)
	otherCall := callauth.CallID(server.Public, otherNonce)

	media, err := sfu.New(sfu.Config{IncludeLoopback: true, Log: testutil.Quiet()})
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { _ = media.Close() })
	cfg := relay.DefaultConfig()
	cfg.Listen = "127.0.0.1:0"
	cfg.FramesPerSecond, cfg.FrameBurst = 1e6, 1e6
	if err := cfg.Validate(); err != nil {
		f.Fatal(err)
	}
	srv := relay.New(cfg, nil)
	srv.SetSFU(media)
	hs := httptest.NewServer(srv.Handler())
	f.Cleanup(hs.Close)
	url := "ws" + strings.TrimPrefix(hs.URL, "http") + "/ws"
	ctx := context.Background()

	grant := func(tb testing.TB, nonce, participant []byte, flags uint8, kid uint32, maxp uint8) *wire.SignedCallGrant {
		g := &wire.CallGrant{
			CallId: callauth.CallID(server.Public, nonce), ServerKey: server.Public, CallNonce: nonce,
			Participant: participant, Kid: kid, Audio: true, Priority: kid&1 == 1,
			Video: flags&fzMedia != 0, Screen: flags&fzMedia != 0,
			ExpiresAt: time.Now().Add(time.Minute).UnixMilli(), MaxParticipants: uint32(maxp),
		}
		if flags&fzExpired != 0 {
			g.ExpiresAt = time.Now().Add(-time.Second).UnixMilli()
		}
		sg, err := callauth.SignGrant(server, g)
		if err != nil {
			tb.Fatal(err)
		}
		return sg
	}

	// The anchor: a real peer connection in the anchor call.
	anchorID := make([]byte, 16)
	rand.Read(anchorID)
	anchorPC, anchorOffer, anchorKinds := fuzzOffer(f, sfu.KindAudio)
	f.Cleanup(func() { _ = anchorPC.Close() })
	anchorWS, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		f.Fatal(err)
	}
	anchorWS.SetReadLimit(1 << 22)
	f.Cleanup(func() { anchorWS.CloseNow() })
	startRaw(f, anchorWS)
	join, _ := proto.Marshal(&wire.RelayFrame{Kind: &wire.RelayFrame_CallJoin{CallJoin: &wire.CallJoin{
		Grant: grant(f, anchorNonce, anchorID, 0, 1, 99), Offer: anchorOffer, Kinds: anchorKinds, Req: 1,
	}}})
	if err := anchorWS.Write(ctx, websocket.MessageBinary, join); err != nil {
		f.Fatal(err)
	}
	for {
		_, b, err := anchorWS.Read(ctx)
		if err != nil {
			f.Fatal(err)
		}
		fr := &wire.RelayFrame{}
		if proto.Unmarshal(b, fr) != nil {
			f.Fatal("anchor got a malformed frame")
		}
		if e := fr.GetError(); e != nil {
			f.Fatalf("anchor join refused: %v", e)
		}
		if a := fr.GetCallAnswer(); a != nil {
			if err := anchorPC.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: a.Answer}); err != nil {
				f.Fatal(err)
			}
			break
		}
	}
	// From here on the anchor only needs its frames read (and the relay's
	// renegotiation offers answered, so the anchor's peer stays usable).
	anchorGone := make(chan struct{})
	go func() {
		defer close(anchorGone)
		for {
			_, b, err := anchorWS.Read(ctx)
			if err != nil {
				return
			}
			fr := &wire.RelayFrame{}
			if proto.Unmarshal(b, fr) != nil {
				continue
			}
			if st := fr.GetCallState(); st != nil && st.Ended != "" {
				return
			}
			if s := fr.GetCallSignal(); s != nil && s.Type == "offer" {
				if anchorPC.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: s.Sdp}) != nil {
					continue
				}
				ans, err := anchorPC.CreateAnswer(nil)
				if err != nil || anchorPC.SetLocalDescription(ans) != nil {
					continue
				}
				out, _ := proto.Marshal(&wire.RelayFrame{Kind: &wire.RelayFrame_CallSignal{CallSignal: &wire.CallSignal{
					CallId: anchorCall, Type: "answer", Sdp: anchorPC.LocalDescription().SDP,
				}}})
				_ = anchorWS.Write(ctx, websocket.MessageBinary, out)
			}
		}
	}()
	baseConns := srv.Connections()
	if media.Calls() != 1 {
		f.Fatalf("%d calls after the anchor joined", media.Calls())
	}

	// Seeds: real offers for the kinds a client sends.
	pcA, offerA, kindsA := fuzzOffer(f, sfu.KindAudio)
	_ = pcA.Close()
	pcV, offerV, kindsV := fuzzOffer(f, sfu.KindAudio, sfu.KindCamera, sfu.KindScreen, sfu.KindScreenAudio)
	_ = pcV.Close()
	midA, midV := "", ""
	for m := range kindsA {
		midA = m
	}
	for m, k := range kindsV {
		if k == sfu.KindCamera {
			midV = m
		}
	}
	someID := bytes.Repeat([]byte{9}, 16)
	junkCtl, _ := proto.Marshal(&wire.CallControl{CallId: anchorCall, Seq: 1, Muted: [][]byte{someID}})
	f.Add(joinFrames(), offerA, midA, sfu.KindAudio, []byte{}, uint8(0))
	f.Add(joinFrames(), offerA, midA, sfu.KindAudio, someID, uint8(fzSecondCall|fzJoinControl|fzUpdate))
	f.Add(joinFrames(), offerA, midA, sfu.KindAudio, someID, uint8(fzJoinControl|fzRemoveSelf))
	f.Add(joinFrames(), offerA, midA, sfu.KindAudio, junkCtl, uint8(fzJoinControl|fzJunkControl))
	f.Add(joinFrames(), offerA, midA, sfu.KindAudio, []byte{}, uint8(fzExpired))
	f.Add(joinFrames(), offerA, midA, sfu.KindAudio, []byte{}, uint8(fzJoinTwice))
	f.Add(joinFrames(), offerV, midV, sfu.KindCamera, []byte{}, uint8(fzMedia|fzSecondCall))
	f.Add(joinFrames(), "v=0\r\n", "0", "video", []byte{}, uint8(0))
	f.Add(joinFrames(), "", "", "", []byte{}, uint8(0))
	f.Add(joinFrames(
		frame(&wire.RelayFrame{Kind: &wire.RelayFrame_CallSignal{CallSignal: &wire.CallSignal{CallId: anchorCall, Type: "answer", Sdp: offerA}}}),
		frame(&wire.RelayFrame{Kind: &wire.RelayFrame_CallSignal{CallSignal: &wire.CallSignal{CallId: anchorCall, Type: "offer", Sdp: "v=0"}}}),
		frame(&wire.RelayFrame{Kind: &wire.RelayFrame_CallLayer{CallLayer: &wire.CallLayer{CallId: anchorCall, Source: someID, Kind: sfu.KindCamera, Rid: "h"}}}),
		frame(&wire.RelayFrame{Kind: &wire.RelayFrame_CallLayer{CallLayer: &wire.CallLayer{CallId: anchorCall, Source: someID, Kind: sfu.KindCamera, Rid: "x"}}}),
		frame(&wire.RelayFrame{Kind: &wire.RelayFrame_CallUpdate{CallUpdate: &wire.CallUpdate{Control: &wire.SignedCallControl{Control: junkCtl, Signature: make([]byte, 64)}}}}),
		frame(&wire.RelayFrame{Kind: &wire.RelayFrame_CallUpdate{CallUpdate: &wire.CallUpdate{}}}),
		frame(&wire.RelayFrame{Kind: &wire.RelayFrame_CallLeave{CallLeave: &wire.CallLeave{CallId: anchorCall}}}),
		frame(&wire.RelayFrame{Kind: &wire.RelayFrame_CallLeave{CallLeave: &wire.CallLeave{CallId: otherCall}}}),
	), offerA, midA, sfu.KindAudio, someID, uint8(fzUpdate))
	f.Add(joinFrames(
		frame(&wire.RelayFrame{Kind: &wire.RelayFrame_CallListen{CallListen: &wire.CallListen{CallId: anchorCall, Deaf: true, NoAudio: [][]byte{someID}}}}),
		frame(&wire.RelayFrame{Kind: &wire.RelayFrame_CallListen{CallListen: &wire.CallListen{CallId: anchorCall, ScreenAudio: [][]byte{someID, {1}}}}}),
		frame(&wire.RelayFrame{Kind: &wire.RelayFrame_CallListen{CallListen: &wire.CallListen{CallId: otherCall}}}),
		frame(&wire.RelayFrame{Kind: &wire.RelayFrame_CallWatchers{CallWatchers: &wire.CallWatchers{CallId: anchorCall}}}),
	), offerA, midA, sfu.KindAudio, someID, uint8(fzMedia))
	f.Add(joinFrames(
		frame(&wire.RelayFrame{Kind: &wire.RelayFrame_CallJoin{CallJoin: &wire.CallJoin{Offer: offerA, Req: 5}}}),
		frame(&wire.RelayFrame{Kind: &wire.RelayFrame_CallJoin{CallJoin: &wire.CallJoin{Grant: &wire.SignedCallGrant{Grant: []byte{1, 2}}, Req: 6}}}),
	), offerV, midV, sfu.KindCamera, []byte{}, uint8(fzMedia))

	f.Fuzz(func(t *testing.T, data []byte, offer, mid, kind string, ctl []byte, flags uint8) {
		tctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		ws, _, err := websocket.Dial(tctx, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer ws.CloseNow()
		ws.SetReadLimit(1 << 22)
		startRaw(t, ws)

		// Read replies in the background; note when the join is answered.
		// (Or when the relay hangs up, which it may for an oversized frame.)
		answered := make(chan struct{})
		var once sync.Once
		go func() {
			defer once.Do(func() { close(answered) })
			for {
				_, b, err := ws.Read(tctx)
				if err != nil {
					return
				}
				fr := &wire.RelayFrame{}
				if proto.Unmarshal(b, fr) != nil {
					continue
				}
				if fr.GetCallAnswer().GetReq() == 100 || fr.GetError().GetReq() == 100 {
					once.Do(func() { close(answered) })
				}
			}
		}()
		send := func(fr *wire.RelayFrame) bool {
			b, err := proto.Marshal(fr)
			return err == nil && ws.Write(tctx, websocket.MessageBinary, b) == nil
		}

		nonce, callID := anchorNonce, anchorCall
		if flags&fzSecondCall != 0 {
			nonce, callID = otherNonce, otherCall
		}
		me := make([]byte, 16)
		rand.Read(me)
		var kid uint32
		var maxp uint8 = 99
		if len(ctl) > 0 {
			kid, maxp = uint32(ctl[0]), ctl[len(ctl)-1]
		}
		j := &wire.CallJoin{Grant: grant(t, nonce, me, flags, kid, maxp), Offer: offer, Kinds: map[string]string{mid: kind}, Req: 100}
		signed := func() *wire.SignedCallControl {
			c := &wire.CallControl{CallId: callID, Seq: uint64(len(ctl)), At: time.Now().UnixMilli()}
			for i, id := range idsFrom(ctl, anchorID) {
				switch i % 5 {
				case 0:
					c.Muted = append(c.Muted, id)
				case 1:
					c.Deafened = append(c.Deafened, id)
				case 2:
					c.NoVideo = append(c.NoVideo, id)
				case 3:
					c.NoScreen = append(c.NoScreen, id)
				default:
					c.Removed = append(c.Removed, id)
				}
			}
			if flags&fzRemoveSelf != 0 {
				c.Removed = append(c.Removed, me)
			}
			sc, err := callauth.SignControl(server, c)
			if err != nil {
				t.Fatal(err)
			}
			return sc
		}
		if flags&fzJoinControl != 0 {
			if flags&fzJunkControl != 0 {
				j.Control = &wire.SignedCallControl{Control: ctl, Signature: make([]byte, 64)}
			} else {
				j.Control = signed()
			}
		}
		if send(&wire.RelayFrame{Kind: &wire.RelayFrame_CallJoin{CallJoin: j}}) {
			if flags&fzJoinTwice != 0 {
				again := proto.Clone(j).(*wire.CallJoin)
				again.Req = 101
				send(&wire.RelayFrame{Kind: &wire.RelayFrame_CallJoin{CallJoin: again}})
			}
			select {
			case <-answered:
			case <-tctx.Done():
				t.Fatal("join neither answered nor refused")
			}
			if flags&fzUpdate != 0 {
				send(&wire.RelayFrame{Kind: &wire.RelayFrame_CallUpdate{CallUpdate: &wire.CallUpdate{Control: signed()}}})
			}
			for _, fr := range splitFrames(data) {
				if ws.Write(tctx, websocket.MessageBinary, fr) != nil {
					break // the relay hung up, which is allowed
				}
			}
		}
		ws.Close(websocket.StatusNormalClosure, "")

		// Everything the connection held is released: the connection, and
		// any call only this client was in.
		for srv.Connections() != baseConns || media.Calls() != 1 {
			if tctx.Err() != nil {
				t.Fatalf("not released: %d connections (want %d), %d calls (want 1)", srv.Connections(), baseConns, media.Calls())
			}
			time.Sleep(time.Millisecond)
		}
		select {
		case <-anchorGone:
			t.Fatal("fuzzed call frames disconnected another participant")
		default:
		}
	})
}
