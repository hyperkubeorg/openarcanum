package relayclient

import (
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/relayid"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// fuzzRelayKey is the fake relay's identity: it proves it in Hello.
var fuzzRelayKey, _ = identity.Generate()

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

func joinFrames(frames ...*wire.RelayFrame) []byte {
	var b []byte
	for _, f := range frames {
		m, err := proto.Marshal(f)
		if err != nil {
			panic(err)
		}
		b = binary.BigEndian.AppendUint16(b, uint16(len(m)))
		b = append(b, m...)
	}
	return b
}

const (
	fuzzMaxChunk   = 64
	fuzzMaxMessage = 256
)

// fakeRelay accepts one WebSocket at a time, greets it (with maxMessage as
// its max_message), waits for the test to hand it frames, sends them, and
// hangs up.
type fakeRelay struct {
	url        string
	maxMessage uint64
	frames     chan [][]byte
}

func startFakeRelay(tb testing.TB) *fakeRelay {
	r := &fakeRelay{maxMessage: fuzzMaxMessage, frames: make(chan [][]byte)}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ws, err := websocket.Accept(w, req, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		ctx, cancel := context.WithTimeout(req.Context(), 10*time.Second)
		defer cancel()
		nonce := make([]byte, 32)
		hello, _ := proto.Marshal(&wire.RelayFrame{Kind: &wire.RelayFrame_Hello{Hello: &wire.Hello{
			Nonce: nonce, MaxChunk: fuzzMaxChunk, MaxMessage: r.maxMessage, Versions: []uint32{1},
			RelayKey: fuzzRelayKey.Public, RelaySignature: relayid.SignHello(fuzzRelayKey, "", nonce),
		}}})
		if ws.Write(ctx, websocket.MessageBinary, hello) != nil {
			return
		}
		if _, _, err := ws.Read(ctx); err != nil { // Start
			return
		}
		ack, _ := proto.Marshal(&wire.RelayFrame{Kind: &wire.RelayFrame_Ack{Ack: &wire.Ack{Req: 1}}})
		if ws.Write(ctx, websocket.MessageBinary, ack) != nil {
			return
		}
		// Drain what the client sends (subscribe, open, stream close).
		go func() {
			for {
				if _, _, err := ws.Read(ctx); err != nil {
					return
				}
			}
		}()
		var frames [][]byte
		select {
		case frames = <-r.frames:
		case <-ctx.Done():
			return
		}
		for _, f := range frames {
			if ws.Write(ctx, websocket.MessageBinary, f) != nil {
				return
			}
		}
		ws.Close(websocket.StatusNormalClosure, "")
	}))
	tb.Cleanup(hs.Close)
	r.url = "ws" + strings.TrimPrefix(hs.URL, "http")
	return r
}

// FuzzClientFrames feeds a relay client whatever a relay could send after
// its hello: acks and errors for pending requests, chunked deliveries for
// subscribed and unknown topics, stream opens, data and closes for streams
// the client opened, accepts or never heard of. The client must not panic or
// hang its read loop, must only hand handlers complete messages within the
// relay's max_message for topics they subscribed to, and must shut down
// cleanly (every pending call returns) when the relay hangs up.
func FuzzClientFrames(f *testing.F) {
	topic := []byte("t")
	deliver := func(sender, id uint64, seq uint32, final bool, data string) *wire.RelayFrame {
		return &wire.RelayFrame{Kind: &wire.RelayFrame_Deliver{Deliver: &wire.Deliver{
			Topic: topic, Sender: sender, MessageId: id, Seq: seq, Final: final, Data: []byte(data),
		}}}
	}
	f.Add([]byte{})
	f.Add([]byte{0, 3, 0xff, 0xff, 0xff})
	f.Add(joinFrames(&wire.RelayFrame{Kind: &wire.RelayFrame_Ack{Ack: &wire.Ack{Req: 1}}}, deliver(1, 1, 0, true, "hello")))
	f.Add(joinFrames(deliver(1, 1, 0, false, "a"), deliver(1, 1, 1, false, "b"), deliver(1, 1, 2, true, "c"), deliver(1, 1, 0, true, "x")))
	f.Add(joinFrames(deliver(2, 9, 1, true, "out of order"), deliver(2, 9, 0, false, strings.Repeat("z", 200)), deliver(2, 9, 1, true, strings.Repeat("z", 100))))
	f.Add(joinFrames(&wire.RelayFrame{Kind: &wire.RelayFrame_Error{Error: &wire.Error{Req: 1, Code: wire.ErrorCode_ERROR_CODE_LIMIT, Message: "no"}}}))
	f.Add(joinFrames(
		&wire.RelayFrame{Kind: &wire.RelayFrame_StreamOpened{StreamOpened: &wire.StreamOpened{StreamId: 1}}},
		&wire.RelayFrame{Kind: &wire.RelayFrame_StreamData{StreamData: &wire.StreamData{StreamId: 1, Data: []byte("d")}}},
		&wire.RelayFrame{Kind: &wire.RelayFrame_StreamClose{StreamClose: &wire.StreamClose{StreamId: 1, Reason: "r"}}},
	))
	f.Add(joinFrames(
		&wire.RelayFrame{Kind: &wire.RelayFrame_StreamOpened{StreamOpened: &wire.StreamOpened{StreamId: 1}}},
		&wire.RelayFrame{Kind: &wire.RelayFrame_StreamOpened{StreamOpened: &wire.StreamOpened{StreamId: 1}}},
		&wire.RelayFrame{Kind: &wire.RelayFrame_StreamOpened{StreamOpened: &wire.StreamOpened{StreamId: 1}}},
		&wire.RelayFrame{Kind: &wire.RelayFrame_StreamClose{StreamClose: &wire.StreamClose{StreamId: 1}}},
	))
	f.Add(joinFrames(
		&wire.RelayFrame{Kind: &wire.RelayFrame_StreamOpened{StreamOpened: &wire.StreamOpened{StreamId: 0x80000001}}},
		&wire.RelayFrame{Kind: &wire.RelayFrame_StreamData{StreamData: &wire.StreamData{StreamId: 0x80000001, Data: []byte("in")}}},
		&wire.RelayFrame{Kind: &wire.RelayFrame_StreamOpened{StreamOpened: &wire.StreamOpened{StreamId: 0x80000001}}},
		&wire.RelayFrame{Kind: &wire.RelayFrame_Hello{Hello: &wire.Hello{MaxMessage: 1 << 40}}},
	))

	relay := startFakeRelay(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c, err := Dial(ctx, relay.url)
		if err != nil {
			// Thousands of short connections exhaust the loopback ports on
			// some systems (macOS keeps them in TIME_WAIT): not a finding.
			if strings.Contains(err.Error(), "assign requested address") {
				t.Skip("loopback ports exhausted")
			}
			t.Fatal(err)
		}
		defer c.Close()
		c.StreamBufferBytes = 1024

		var mu sync.Mutex
		var got [][]byte
		c.smu.Lock()
		c.handlers[string(topic)] = func(tp, msg []byte) {
			if string(tp) != string(topic) {
				t.Errorf("handler for %q got topic %q", topic, tp)
			}
			mu.Lock()
			got = append(got, msg)
			mu.Unlock()
		}
		c.smu.Unlock()

		// Calls in flight while the frames arrive: request id 1 and stream 1.
		var wg sync.WaitGroup
		wg.Add(3)
		go func() { defer wg.Done(); _ = c.Subscribe(ctx, []byte("other"), func(_, _ []byte) {}) }()
		go func() {
			defer wg.Done()
			if s, err := c.OpenStream(ctx, make([]byte, 32)); err == nil {
				_, _ = s.Recv(ctx)
			}
		}()
		go func() {
			defer wg.Done()
			if s, err := c.Accept(ctx); err == nil {
				_, _ = s.Recv(ctx)
			}
		}()

		select {
		case relay.frames <- splitFrames(data):
		case <-ctx.Done():
			t.Fatal("fake relay never took the frames")
		}
		select {
		case <-c.Done():
		case <-ctx.Done():
			t.Fatal("client did not notice the relay hanging up (read loop stuck)")
		}
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal("a pending call did not return after the connection closed")
		}
		mu.Lock()
		defer mu.Unlock()
		for _, m := range got {
			if len(m) > fuzzMaxMessage {
				t.Fatalf("delivered %d bytes, over max_message %d", len(m), fuzzMaxMessage)
			}
		}
	})
}
