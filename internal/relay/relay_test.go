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
	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/relay"
	"github.com/hyperkubeorg/openarcanum/internal/relayclient"
	"github.com/hyperkubeorg/openarcanum/internal/relayid"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

func start(t *testing.T, mutate func(*relay.Config)) (*relay.Server, string) {
	t.Helper()
	cfg := relay.DefaultConfig()
	cfg.Listen = "127.0.0.1:0"
	if mutate != nil {
		mutate(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	srv := relay.New(cfg, testutil.Quiet())
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	url := "ws" + strings.TrimPrefix(hs.URL, "http") + "/ws"
	// Its public URL, as on a public relay (endpoint registrations and
	// fan-out tokens are bound to it).
	if err := srv.SetURL(url); err != nil {
		t.Fatal(err)
	}
	return srv, url
}

// binding is what c signs for its relay connection.
func binding(t testing.TB, c *relayclient.Client) relayid.Binding {
	t.Helper()
	b, err := c.Binding()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func dial(t *testing.T, url string) *relayclient.Client {
	t.Helper()
	c, err := relayclient.Dial(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return c
}

type inbox struct {
	mu   sync.Mutex
	msgs [][]byte
	ch   chan struct{}
}

func newInbox() *inbox { return &inbox{ch: make(chan struct{}, 1024)} }

func (b *inbox) handler(_, data []byte) {
	b.mu.Lock()
	b.msgs = append(b.msgs, data)
	b.mu.Unlock()
	b.ch <- struct{}{}
}

func (b *inbox) wait(t *testing.T, n int) [][]byte {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		b.mu.Lock()
		if len(b.msgs) >= n {
			out := append([][]byte(nil), b.msgs...)
			b.mu.Unlock()
			return out
		}
		b.mu.Unlock()
		select {
		case <-b.ch:
		case <-deadline:
			t.Fatalf("timed out waiting for %d messages", n)
		}
	}
}

func TestFanOut(t *testing.T) {
	_, url := start(t, nil)
	topic := []byte("topic-fanout")
	var boxes []*inbox
	for i := 0; i < 5; i++ {
		c := dial(t, url)
		b := newInbox()
		if err := c.Subscribe(ctx(t), topic, b.handler); err != nil {
			t.Fatal(err)
		}
		boxes = append(boxes, b)
	}
	other := dial(t, url)
	ob := newInbox()
	if err := other.Subscribe(ctx(t), []byte("other-topic"), ob.handler); err != nil {
		t.Fatal(err)
	}
	pub := dial(t, url)
	selfBox := newInbox()
	if err := pub.Subscribe(ctx(t), topic, selfBox.handler); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := pub.Publish(topic, []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, b := range boxes {
		got := b.wait(t, 3)
		for i, m := range got {
			if !bytes.Equal(m, []byte{byte(i)}) {
				t.Fatalf("message %d out of order or wrong: %v", i, m)
			}
		}
	}
	time.Sleep(100 * time.Millisecond)
	if len(ob.msgs) != 0 {
		t.Fatal("message leaked to another topic")
	}
	if len(selfBox.msgs) != 0 {
		t.Fatal("publisher received its own message")
	}
}

func TestChunkedMessageAtTheCap(t *testing.T) {
	_, url := start(t, func(c *relay.Config) {
		c.MaxChunk = 64 << 10
		c.MaxMessage = 1 << 20
		c.SendQueueBytes = 8 << 20
		c.ByteBurst = 8 << 20
	})
	topic := []byte("big")
	sub := dial(t, url)
	b := newInbox()
	if err := sub.Subscribe(ctx(t), topic, b.handler); err != nil {
		t.Fatal(err)
	}
	pub := dial(t, url)
	payload := make([]byte, 1<<20)
	rand.Read(payload)
	if err := pub.Publish(topic, payload); err != nil {
		t.Fatal(err)
	}
	got := b.wait(t, 1)
	if !bytes.Equal(got[0], payload) {
		t.Fatal("reassembled payload differs")
	}
}

// A message over max_message is refused and never delivered, even when the
// sender bypasses the client-side check.
func TestOverSizeMessageRefused(t *testing.T) {
	_, url := start(t, func(c *relay.Config) {
		c.MaxChunk = 64 << 10
		c.MaxMessage = 256 << 10
	})
	topic := []byte("cap")
	sub := dial(t, url)
	b := newInbox()
	if err := sub.Subscribe(ctx(t), topic, b.handler); err != nil {
		t.Fatal(err)
	}
	pub := dial(t, url)
	if err := pub.Publish(topic, make([]byte, 256<<10+1)); err == nil {
		t.Fatal("client allowed an over-size publish")
	}

	// Speak the protocol by hand to push chunks past the cap.
	ws, _, err := websocket.Dial(ctx(t), url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	startRaw(t, ws)
	chunk := make([]byte, 64<<10)
	for seq := uint32(0); seq < 6; seq++ {
		f, _ := proto.Marshal(&wire.RelayFrame{Kind: &wire.RelayFrame_Publish{Publish: &wire.Publish{
			Topic: topic, MessageId: 7, Seq: seq, Final: seq == 5, Data: chunk,
		}}})
		if err := ws.Write(ctx(t), websocket.MessageBinary, f); err != nil {
			t.Fatal(err)
		}
	}
	_, data, err := ws.Read(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	var f wire.RelayFrame
	if err := proto.Unmarshal(data, &f); err != nil || f.GetError().GetCode() != wire.ErrorCode_ERROR_CODE_TOO_LARGE {
		t.Fatalf("expected TOO_LARGE error, got %v", &f)
	}
	// A normal message afterwards still goes through, and nothing partial was
	// completed for the subscriber.
	if err := pub.Publish(topic, []byte("ok")); err != nil {
		t.Fatal(err)
	}
	got := b.wait(t, 1)
	if len(got) != 1 || string(got[0]) != "ok" {
		t.Fatalf("unexpected deliveries: %d", len(got))
	}
}

func TestEndpointStreams(t *testing.T) {
	_, url := start(t, nil)
	key, _ := identity.Generate()
	ep := dial(t, url)
	if err := ep.RegisterEndpoint(ctx(t), key); err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			s, err := ep.Accept(context.Background())
			if err != nil {
				return
			}
			go func() {
				for {
					m, err := s.Recv(context.Background())
					if err != nil {
						return
					}
					s.Send(append([]byte("echo:"), m...))
				}
			}()
		}
	}()

	for i := 0; i < 3; i++ {
		cl := dial(t, url)
		s, err := cl.OpenStream(ctx(t), key.Public)
		if err != nil {
			t.Fatal(err)
		}
		msg := []byte{'m', byte('0' + i)}
		if err := s.Send(msg); err != nil {
			t.Fatal(err)
		}
		got, err := s.Recv(ctx(t))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "echo:"+string(msg) {
			t.Fatalf("got %q", got)
		}
		s.Close("done")
	}

	// Unknown endpoints are refused.
	cl := dial(t, url)
	other, _ := identity.Generate()
	if _, err := cl.OpenStream(ctx(t), other.Public); err == nil {
		t.Fatal("stream to unregistered endpoint opened")
	}
}

func TestEndpointRegistrationNeedsSignature(t *testing.T) {
	_, url := start(t, nil)
	key, _ := identity.Generate()
	ws, _, err := websocket.Dial(ctx(t), url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	startRaw(t, ws)
	// Signed over the wrong nonce: a replayed registration.
	f, _ := proto.Marshal(&wire.RelayFrame{Kind: &wire.RelayFrame_RegisterEndpoint{RegisterEndpoint: &wire.RegisterEndpoint{
		EndpointKey: key.Public, Signature: key.Sign(identity.CtxRelayEndpoint, []byte("old nonce")), Req: 1,
	}}})
	if err := ws.Write(ctx(t), websocket.MessageBinary, f); err != nil {
		t.Fatal(err)
	}
	_, data, err := ws.Read(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	var r wire.RelayFrame
	proto.Unmarshal(data, &r)
	if r.GetError().GetCode() != wire.ErrorCode_ERROR_CODE_BAD_SIGNATURE {
		t.Fatalf("expected BAD_SIGNATURE, got %v", &r)
	}
}

func TestEndpointMoveClosesOldStreams(t *testing.T) {
	_, url := start(t, nil)
	key, _ := identity.Generate()
	ep1 := dial(t, url)
	if err := ep1.RegisterEndpoint(ctx(t), key); err != nil {
		t.Fatal(err)
	}
	cl := dial(t, url)
	s, err := cl.OpenStream(ctx(t), key.Public)
	if err != nil {
		t.Fatal(err)
	}
	ep2 := dial(t, url)
	if err := ep2.RegisterEndpoint(ctx(t), key); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Recv(ctx(t)); err == nil {
		t.Fatal("stream to old endpoint survived the move")
	}
	if _, err := cl.OpenStream(ctx(t), key.Public); err != nil {
		t.Fatalf("stream to moved endpoint failed: %v", err)
	}
	if _, err := ep2.Accept(ctx(t)); err != nil {
		t.Fatal(err)
	}
}

func TestEndpointDisconnectClosesStreams(t *testing.T) {
	_, url := start(t, nil)
	key, _ := identity.Generate()
	ep := dial(t, url)
	if err := ep.RegisterEndpoint(ctx(t), key); err != nil {
		t.Fatal(err)
	}
	cl := dial(t, url)
	s, err := cl.OpenStream(ctx(t), key.Public)
	if err != nil {
		t.Fatal(err)
	}
	ep.Close()
	if _, err := s.Recv(ctx(t)); err == nil {
		t.Fatal("stream survived endpoint disconnect")
	}
}

// A subscriber that stops reading is disconnected instead of buffered forever,
// and other subscribers keep receiving.
func TestSlowConsumerDisconnected(t *testing.T) {
	srv, url := start(t, func(c *relay.Config) {
		c.MaxChunk = 256 << 10
		c.MaxMessage = 1 << 20
		c.SendQueueBytes = 2 << 20
		c.ByteBurst = 64 << 20
		c.BytesPerSecond = 1 << 30
	})
	topic := []byte("slow")

	slow, _, err := websocket.Dial(ctx(t), url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer slow.CloseNow()
	startRaw(t, slow)
	f, _ := proto.Marshal(&wire.RelayFrame{Kind: &wire.RelayFrame_Subscribe{Subscribe: &wire.Subscribe{Topic: topic, Req: 1}}})
	if err := slow.Write(ctx(t), websocket.MessageBinary, f); err != nil {
		t.Fatal(err)
	}
	if _, _, err := slow.Read(ctx(t)); err != nil { // ack
		t.Fatal(err)
	}
	// From here on `slow` never reads.

	fast := dial(t, url)
	fb := newInbox()
	if err := fast.Subscribe(ctx(t), topic, fb.handler); err != nil {
		t.Fatal(err)
	}

	pub := dial(t, url)
	payload := make([]byte, 256<<10)
	const n = 400 // 100 MiB total, far past socket buffers and the queue
	go func() {
		for i := 0; i < n; i++ {
			if pub.Publish(topic, payload) != nil {
				return
			}
		}
	}()
	fb.wait(t, n)
	deadline := time.Now().Add(10 * time.Second)
	for srv.Stats().SlowConsumers.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("slow consumer was never disconnected")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Senders over their byte rate are slowed down, not cut off.
func TestRateLimitSlowsSender(t *testing.T) {
	_, url := start(t, func(c *relay.Config) {
		c.MaxChunk = 64 << 10
		c.MaxMessage = 64 << 10
		c.SendQueueBytes = 1 << 20
		c.BytesPerSecond = 512 << 10
		c.ByteBurst = 64 << 10
	})
	topic := []byte("rate")
	sub := dial(t, url)
	b := newInbox()
	if err := sub.Subscribe(ctx(t), topic, b.handler); err != nil {
		t.Fatal(err)
	}
	pub := dial(t, url)
	start := time.Now()
	for i := 0; i < 16; i++ { // 1 MiB at 512 KiB/s
		if err := pub.Publish(topic, make([]byte, 64<<10)); err != nil {
			t.Fatal(err)
		}
	}
	b.wait(t, 16)
	if el := time.Since(start); el < 1500*time.Millisecond {
		t.Fatalf("1 MiB at 512 KiB/s took only %v", el)
	}
}

func TestConfigValidation(t *testing.T) {
	cfg := relay.DefaultConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
	bad := cfg
	bad.MaxMessage = 26 << 20
	if bad.Validate() == nil {
		t.Fatal("max_message over 25 MiB accepted")
	}
	bad = cfg
	bad.MaxChunk = 10
	if bad.Validate() == nil {
		t.Fatal("tiny max_chunk accepted")
	}
	bad = cfg
	bad.RegistryURLs = []string{"http://x"}
	if bad.Validate() == nil {
		t.Fatal("registry without public_url accepted")
	}
}
