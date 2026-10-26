// Command callload runs synthetic call participants for scale tests
// (clients/desktop/tests/test_calls.py): each joins a relay's SFU with a grant, sends
// SFrame-encrypted audio (Opus silence frames, which real clients decode)
// and optionally simulcast video, answers the relay's renegotiation, and
// counts what it receives from everyone else, decrypting the audio.
//
//	callload plan.json   # prints one JSON result line when done
package main

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/pow"
	"github.com/hyperkubeorg/openarcanum/internal/testutil/sframe"
	"github.com/hyperkubeorg/openarcanum/internal/version"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

type plan struct {
	Relay   string `json:"relay"`
	CallKey string `json:"call_key"` // base64
	Epoch   uint64 `json:"epoch"`
	Seconds int    `json:"seconds"`
	// Everyone in the call (participant hex -> slot), to know whose key id is whose.
	Roster       map[string]uint64 `json:"roster"`
	Participants []struct {
		Grant string `json:"grant"` // base64 SignedCallGrant
		ID    string `json:"id"`    // participant hex
		Slot  uint64 `json:"slot"`
		Video bool   `json:"video"`
	} `json:"participants"`
}

type result struct {
	ID        string            `json:"id"`
	Joined    bool              `json:"joined"`
	Error     string            `json:"error,omitempty"`
	AudioFrom map[string]int64  `json:"audio_from"` // decrypted audio frames per sender
	VideoFrom map[string]int64  `json:"video_from"` // video packets per sender
	Failed    int64             `json:"failed"`
	Offers    int               `json:"offers"`
	Answers   int               `json:"answers"`
	Tracks    int               `json:"tracks"`
	RenegErr  string            `json:"reneg_err,omitempty"`
	Sent      map[string]uint64 `json:"sent"`
}

// opusSilence is a valid 20 ms Opus frame (CELT, fullband) of silence.
var opusSilence = []byte{0xf8, 0xff, 0xfe}

func main() {
	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	var p plan
	if err := json.Unmarshal(raw, &p); err != nil {
		panic(err)
	}
	callKey, _ := base64.StdEncoding.DecodeString(p.CallKey)
	keys := map[uint64]*sframe.Key{}
	for _, slot := range p.Roster {
		kid := p.Epoch<<8 | slot
		if k, err := sframe.NewKey(callKey, kid); err == nil {
			keys[kid] = k
		}
	}
	slots := map[string]uint64{}
	for id, slot := range p.Roster {
		slots[id] = slot
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(p.Seconds+60)*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	results := make([]*result, len(p.Participants))
	for i, pp := range p.Participants {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := &result{ID: pp.ID, AudioFrom: map[string]int64{}, VideoFrom: map[string]int64{}, Sent: map[string]uint64{}}
			results[i] = r
			c := &client{plan: &p, keys: keys, slots: slots, res: r, own: keys[p.Epoch<<8|pp.Slot]}
			if err := c.run(ctx, pp.Grant, pp.Video, time.Duration(p.Seconds)*time.Second); err != nil {
				r.Error = err.Error()
			}
		}(i)
		time.Sleep(50 * time.Millisecond) // don't join all at the same instant
	}
	wg.Wait()
	out, _ := json.Marshal(results)
	fmt.Println(string(out))
}

type client struct {
	plan  *plan
	keys  map[uint64]*sframe.Key
	slots map[string]uint64
	own   *sframe.Key
	res   *result
	ws    *websocket.Conn
	pc    *webrtc.PeerConnection
	wmu   sync.Mutex
	mu    sync.Mutex // the result
	neg   sync.Mutex // one renegotiation at a time
	ctr   atomic.Uint64
}

func (c *client) send(ctx context.Context, f *wire.RelayFrame) error {
	b, _ := proto.Marshal(f)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.ws.Write(ctx, websocket.MessageBinary, b)
}

func (c *client) run(ctx context.Context, grantB64 string, video bool, dur time.Duration) error {
	grantRaw, _ := base64.StdEncoding.DecodeString(grantB64)
	grant := &wire.SignedCallGrant{}
	if err := proto.Unmarshal(grantRaw, grant); err != nil {
		return err
	}
	g := &wire.CallGrant{}
	if err := proto.Unmarshal(grant.Grant, g); err != nil {
		return err
	}
	ws, _, err := websocket.Dial(ctx, c.plan.Relay, nil)
	if err != nil {
		return err
	}
	ws.SetReadLimit(1 << 22)
	c.ws = ws
	defer ws.CloseNow()
	if err := startRelay(ctx, ws); err != nil {
		return err
	}

	m := &webrtc.MediaEngine{}
	if err := m.RegisterDefaultCodecs(); err != nil {
		return err
	}
	for _, uri := range []string{"urn:ietf:params:rtp-hdrext:sdes:mid", "urn:ietf:params:rtp-hdrext:sdes:rtp-stream-id"} {
		_ = m.RegisterHeaderExtension(webrtc.RTPHeaderExtensionCapability{URI: uri}, webrtc.RTPCodecTypeVideo)
	}
	se := webrtc.SettingEngine{}
	se.SetIncludeLoopbackCandidate(true)
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	pc, err := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithSettingEngine(se)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return err
	}
	c.pc = pc
	defer pc.Close()
	audio, _ := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "audio", "me")
	atx, err := pc.AddTransceiverFromTrack(audio, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly})
	if err != nil {
		return err
	}
	kinds := map[string]string{}
	var layers []*webrtc.TrackLocalStaticRTP
	var vtx *webrtc.RTPTransceiver
	if video {
		for _, rid := range []string{"l", "m", "h"} {
			tr, _ := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}, "video", "me", webrtc.WithRTPStreamID(rid))
			layers = append(layers, tr)
			if vtx == nil {
				if vtx, err = pc.AddTransceiverFromTrack(tr, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly}); err != nil {
					return err
				}
			} else if err := vtx.Sender().AddEncoding(tr); err != nil {
				return err
			}
		}
	}
	pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		c.mu.Lock()
		c.res.Tracks++
		c.mu.Unlock()
		go c.receive(tr)
	})
	offer, _ := pc.CreateOffer(nil)
	gather := webrtc.GatheringCompletePromise(pc)
	_ = pc.SetLocalDescription(offer)
	<-gather
	kinds[atx.Mid()] = "audio"
	if vtx != nil {
		kinds[vtx.Mid()] = "camera"
	}
	if err := c.send(ctx, &wire.RelayFrame{Kind: &wire.RelayFrame_CallJoin{CallJoin: &wire.CallJoin{Grant: grant, Offer: pc.LocalDescription().SDP, Kinds: kinds, Req: 1}}}); err != nil {
		return err
	}
	// Frames: the answer, then the relay's renegotiation offers.
	answered := make(chan error, 1)
	// Relay offers wait until the join answer is applied.
	c.neg.Lock()
	go func() {
		for {
			_, b, err := ws.Read(ctx)
			if err != nil {
				return
			}
			f := &wire.RelayFrame{}
			if proto.Unmarshal(b, f) != nil {
				continue
			}
			switch {
			case f.GetCallAnswer() != nil:
				answered <- pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: f.GetCallAnswer().Answer})
			case f.GetError() != nil && f.GetError().Req == 1:
				answered <- fmt.Errorf("join refused: %s", f.GetError().Message)
			case f.GetCallSignal() != nil && f.GetCallSignal().Type == "offer":
				go c.renegotiate(ctx, g.CallId, f.GetCallSignal().Sdp)
			}
		}
	}()
	select {
	case err := <-answered:
		c.neg.Unlock()
		if err != nil {
			return err
		}
	case <-ctx.Done():
		c.neg.Unlock()
		return ctx.Err()
	}
	c.res.Joined = true
	// Video is paused until asked for: like a client showing everyone in
	// small tiles, ask for every other participant's low camera layer (the
	// relay keeps the request until that camera sends).
	for id := range c.plan.Roster {
		src, err := hex.DecodeString(id)
		if err != nil || id == c.res.ID {
			continue
		}
		layer := &wire.CallLayer{CallId: g.CallId, Source: src, Kind: "camera", Rid: "l"}
		if err := c.send(ctx, &wire.RelayFrame{Kind: &wire.RelayFrame_CallLayer{CallLayer: layer}}); err != nil {
			return err
		}
	}

	var midID, ridID uint8
	if vtx != nil {
		for _, e := range vtx.Sender().GetParameters().HeaderExtensions {
			switch e.URI {
			case "urn:ietf:params:rtp-hdrext:sdes:mid":
				midID = uint8(e.ID)
			case "urn:ietf:params:rtp-hdrext:sdes:rtp-stream-id":
				ridID = uint8(e.ID)
			}
		}
	}
	end := time.After(dur)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	var seq uint16
	var ts uint32
	for n := 0; ; n++ {
		select {
		case <-end:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
		seq++
		ts += 960
		frame := c.own.Seal(c.ctr.Add(1), sframe.Metadata(sframe.KindAudio, nil), opusSilence)
		_ = audio.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: ts, PayloadType: 111}, Payload: frame})
		c.mu.Lock()
		c.res.Sent["audio"]++
		c.mu.Unlock()
		if vtx != nil && n%4 == 0 { // ~12 fps
			for i, tr := range layers {
				rid := []string{"l", "m", "h"}[i]
				pkt := &rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: ts * 90 / 48, PayloadType: 96, Marker: true},
					Payload: c.vp8Frame(n%60 == 0)}
				_ = pkt.SetExtension(midID, []byte(vtx.Mid()))
				_ = pkt.SetExtension(ridID, []byte(rid))
				_ = tr.WriteRTP(pkt)
			}
			c.mu.Lock()
			c.res.Sent["video"]++
			c.mu.Unlock()
		}
	}
}

// vp8Frame is one RTP payload: the VP8 payload descriptor, the frame's VP8
// header in the clear (10 bytes for a keyframe: tag, start code, size; 3
// otherwise), then the SFrame of the rest with the camera's kind tag and that
// header as metadata, as clients do.
func (c *client) vp8Frame(key bool) []byte {
	body := make([]byte, 200)
	var clear []byte
	if key {
		tag := uint32(len(body))<<5 | 1<<4 // show_frame, version 0, keyframe (P=0)
		clear = []byte{byte(tag), byte(tag >> 8), byte(tag >> 16), 0x9d, 0x01, 0x2a}
		clear = binary.LittleEndian.AppendUint16(clear, 320)
		clear = binary.LittleEndian.AppendUint16(clear, 180)
	} else {
		tag := uint32(len(body))<<5 | 1<<4 | 1
		clear = []byte{byte(tag), byte(tag >> 8), byte(tag >> 16)}
	}
	sealed := c.own.Seal(c.ctr.Add(1), sframe.Metadata(sframe.KindCamera, clear), body)
	out := append([]byte{0x10}, clear...)
	return append(out, sealed...)
}

func (c *client) renegotiate(ctx context.Context, callID []byte, sdp string) {
	c.neg.Lock()
	defer c.neg.Unlock()
	c.mu.Lock()
	c.res.Offers++
	c.mu.Unlock()
	fail := func(err error) {
		c.mu.Lock()
		c.res.RenegErr = err.Error()
		c.mu.Unlock()
	}
	if err := c.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdp}); err != nil {
		fail(err)
		return
	}
	ans, err := c.pc.CreateAnswer(nil)
	if err != nil {
		fail(err)
		return
	}
	gather := webrtc.GatheringCompletePromise(c.pc)
	_ = c.pc.SetLocalDescription(ans)
	<-gather
	if err := c.send(ctx, &wire.RelayFrame{Kind: &wire.RelayFrame_CallSignal{CallSignal: &wire.CallSignal{CallId: callID, Type: "answer", Sdp: c.pc.LocalDescription().SDP}}}); err != nil {
		fail(err)
		return
	}
	c.mu.Lock()
	c.res.Answers++
	c.mu.Unlock()
}

// receive counts a remote track's media: audio frames are decrypted (and must
// carry the sender's slot), video packets counted.
func (c *client) receive(tr *webrtc.TrackRemote) {
	src := tr.StreamID()
	kind := tr.ID()
	if _, err := hex.DecodeString(src); err != nil {
		return
	}
	for {
		p, _, err := tr.ReadRTP()
		if err != nil {
			return
		}
		if len(p.Payload) == 0 {
			continue // padding (bandwidth probes)
		}
		if kind != "audio" {
			c.mu.Lock()
			c.res.VideoFrom[src]++
			c.mu.Unlock()
			continue
		}
		kid, _, _, err := sframe.ParseHeader(p.Payload)
		ok := false
		if err == nil && kid&0xff == c.slots[src] {
			if k := c.keys[kid]; k != nil {
				if _, err := k.Open(p.Payload, sframe.Metadata(sframe.KindAudio, nil)); err == nil {
					ok = true
				}
			}
		}
		c.mu.Lock()
		if ok {
			c.res.AudioFrom[src]++
		} else {
			c.res.Failed++
		}
		c.mu.Unlock()
	}
}

// startRelay reads the relay's Hello and sends Start (docs/protocol.md §2),
// solving the proof-of-work if the relay asks for one.
func startRelay(ctx context.Context, ws *websocket.Conn) error {
	_, b, err := ws.Read(ctx)
	if err != nil {
		return err
	}
	var f wire.RelayFrame
	if proto.Unmarshal(b, &f) != nil || f.GetHello() == nil {
		return errors.New("relay did not send hello")
	}
	h := f.GetHello()
	v, err := version.Negotiate(version.Supported(), h.Versions)
	if err != nil {
		return err
	}
	counter, err := pow.Solve(ctx, h.Nonce, h.PowBits)
	if err != nil {
		return err
	}
	st, _ := proto.Marshal(&wire.RelayFrame{Kind: &wire.RelayFrame_Start{Start: &wire.Start{Version: v, Pow: counter, Req: 1}}})
	if err := ws.Write(ctx, websocket.MessageBinary, st); err != nil {
		return err
	}
	if _, b, err = ws.Read(ctx); err != nil {
		return err
	}
	var r wire.RelayFrame
	if proto.Unmarshal(b, &r) != nil || r.GetAck().GetReq() != 1 {
		return fmt.Errorf("relay refused start: %v", r.GetError().GetMessage())
	}
	return nil
}
