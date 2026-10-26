package relay_test

import (
	"context"
	"encoding/binary"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/relay"
	"github.com/hyperkubeorg/openarcanum/internal/relayclient"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// splitFrames cuts fuzz input into frames, each prefixed by a 2-byte
// big-endian length (truncated at the end of the input).
func splitFrames(data []byte) [][]byte {
	var out [][]byte
	for len(data) >= 2 {
		n := min(int(binary.BigEndian.Uint16(data)), len(data)-2)
		out = append(out, data[2:2+n])
		data = data[2+n:]
	}
	return out
}

func joinFrames(frames ...[]byte) []byte {
	var b []byte
	for _, f := range frames {
		b = binary.BigEndian.AppendUint16(b, uint16(len(f)))
		b = append(b, f...)
	}
	return b
}

func frame(f *wire.RelayFrame) []byte {
	b, err := proto.Marshal(f)
	if err != nil {
		panic(err)
	}
	return b
}

// FuzzRelayFrames feeds a relay connection arbitrary WebSocket frames: the
// relay's whole read path (frame decoding, publish reassembly limits,
// subscriptions, endpoint registration, stream routing) is reachable by any
// client. Whatever arrives, the relay must not panic, must not hang the
// connection, and must free everything the connection held once it closes.
// A subscriber and a registered endpoint stay connected for the whole run so
// fuzzed publishes and stream opens have somewhere to go.
func FuzzRelayFrames(f *testing.F) {
	endpoint, _ := identity.Generate()
	topic := []byte("fuzz-topic")

	f.Add(joinFrames(), false)
	f.Add(joinFrames([]byte{0xff, 0xff, 0xff}), false)
	f.Add(joinFrames(frame(&wire.RelayFrame{Kind: &wire.RelayFrame_Subscribe{Subscribe: &wire.Subscribe{Topic: topic, Req: 1}}})), true)
	f.Add(joinFrames(
		frame(&wire.RelayFrame{Kind: &wire.RelayFrame_Subscribe{Subscribe: &wire.Subscribe{Topic: topic, Req: 1}}}),
		frame(&wire.RelayFrame{Kind: &wire.RelayFrame_Publish{Publish: &wire.Publish{Topic: topic, MessageId: 7, Seq: 0, Data: []byte("a")}}}),
		frame(&wire.RelayFrame{Kind: &wire.RelayFrame_Publish{Publish: &wire.Publish{Topic: topic, MessageId: 7, Seq: 1, Final: true, Data: []byte("b")}}}),
		frame(&wire.RelayFrame{Kind: &wire.RelayFrame_Unsubscribe{Unsubscribe: &wire.Unsubscribe{Topic: topic, Req: 2}}}),
	), false)
	f.Add(joinFrames(
		frame(&wire.RelayFrame{Kind: &wire.RelayFrame_OpenStream{OpenStream: &wire.OpenStream{EndpointKey: endpoint.Public, StreamId: 1}}}),
		frame(&wire.RelayFrame{Kind: &wire.RelayFrame_StreamData{StreamData: &wire.StreamData{StreamId: 1, Data: []byte("hi")}}}),
		frame(&wire.RelayFrame{Kind: &wire.RelayFrame_StreamClose{StreamClose: &wire.StreamClose{StreamId: 1, Reason: "bye"}}}),
	), false)
	f.Add(joinFrames(
		frame(&wire.RelayFrame{Kind: &wire.RelayFrame_OpenStream{OpenStream: &wire.OpenStream{EndpointKey: endpoint.Public, StreamId: relay.InboundStreamBit | 3}}}),
		frame(&wire.RelayFrame{Kind: &wire.RelayFrame_StreamData{StreamData: &wire.StreamData{StreamId: relay.InboundStreamBit | 1, Data: []byte("x")}}}),
	), false)
	f.Add(joinFrames(frame(&wire.RelayFrame{Kind: &wire.RelayFrame_RegisterEndpoint{RegisterEndpoint: &wire.RegisterEndpoint{
		EndpointKey: endpoint.Public, Signature: make([]byte, 64), Req: 9,
	}}})), false)
	f.Add(joinFrames(
		frame(&wire.RelayFrame{Kind: &wire.RelayFrame_Ack{Ack: &wire.Ack{Req: 1}}}),
		frame(&wire.RelayFrame{Kind: &wire.RelayFrame_Deliver{Deliver: &wire.Deliver{Topic: topic, Data: []byte("x")}}}),
		frame(&wire.RelayFrame{Kind: &wire.RelayFrame_Hello{Hello: &wire.Hello{MaxChunk: 1}}}),
	), false)

	cfg := relay.DefaultConfig()
	cfg.Listen = "127.0.0.1:0"
	cfg.MaxChunk, cfg.MaxMessage, cfg.ByteBurst = 1024, 4096, 1<<20
	cfg.FramesPerSecond, cfg.FrameBurst = 1e6, 1e6
	cfg.MaxPartial, cfg.MaxSubscriptions, cfg.MaxStreams = 4, 8, 4
	if err := cfg.Validate(); err != nil {
		f.Fatal(err)
	}
	srv := relay.New(cfg, nil)
	hs := httptest.NewServer(srv.Handler())
	f.Cleanup(hs.Close)
	url := "ws" + strings.TrimPrefix(hs.URL, "http") + "/ws"

	ctx := context.Background()
	sub, err := relayclient.Dial(ctx, url)
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(sub.Close)
	if err := sub.Subscribe(ctx, topic, func(_, data []byte) {
		if uint64(len(data)) > cfg.MaxMessage {
			panic("relay delivered a message over max_message")
		}
	}); err != nil {
		f.Fatal(err)
	}
	ep, err := relayclient.Dial(ctx, url)
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(ep.Close)
	if err := ep.RegisterEndpoint(ctx, endpoint); err != nil {
		f.Fatal(err)
	}
	go func() {
		// Accept and drop inbound streams so the endpoint's backlog drains.
		for {
			s, err := ep.Accept(ctx)
			if err != nil {
				return
			}
			s.Close("fuzz")
		}
	}()
	base := srv.Connections()

	f.Fuzz(func(t *testing.T, data []byte, firstAsText bool) {
		tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		ws, _, err := websocket.Dial(tctx, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer ws.CloseNow()
		if h := startRaw(t, ws); len(h.Nonce) != 32 {
			t.Fatal("relay did not send a hello with a nonce")
		}
		// Read replies in the background so the relay never blocks on us.
		go func() {
			for {
				if _, _, err := ws.Read(tctx); err != nil {
					return
				}
			}
		}()
		for i, fr := range splitFrames(data) {
			typ := websocket.MessageBinary
			if i == 0 && firstAsText {
				typ = websocket.MessageText
			}
			if ws.Write(tctx, typ, fr) != nil {
				break // the relay hung up, which is allowed
			}
		}
		ws.Close(websocket.StatusNormalClosure, "")

		// The connection's state must be released.
		for srv.Connections() != base {
			if tctx.Err() != nil {
				t.Fatalf("connection not released: %d open, want %d", srv.Connections(), base)
			}
			time.Sleep(time.Millisecond)
		}
		select {
		case <-sub.Done():
			t.Fatal("fuzzed frames disconnected another client's subscription")
		case <-ep.Done():
			t.Fatal("fuzzed frames disconnected the endpoint")
		default:
		}
	})
}
