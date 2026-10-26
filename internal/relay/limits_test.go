package relay_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/coder/websocket"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/relay"
	"github.com/hyperkubeorg/openarcanum/internal/relayclient"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// The per-connection caps: subscriptions, streams, messages in flight and
// total connections. The
// rate limits are covered by TestRateLimitSlowsSender and
// TestSlowConsumerDisconnected, the size caps by TestOverSizeMessageRefused.
func TestPerConnectionCaps(t *testing.T) {
	srv, url := start(t, func(c *relay.Config) {
		c.MaxSubscriptions = 3
		c.MaxStreams = 2
		c.MaxPartial = 2
		c.MaxConnections = 6
	})

	// Subscriptions: the fourth distinct topic is refused, re-subscribing
	// one already held is fine, and unsubscribing frees a slot.
	c := dial(t, url)
	for i := range 3 {
		if err := c.Subscribe(ctx(t), []byte(fmt.Sprintf("t%d", i)), func(_, _ []byte) {}); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Subscribe(ctx(t), []byte("t3"), func(_, _ []byte) {}); err == nil || !strings.Contains(err.Error(), "LIMIT") {
		t.Fatalf("fourth subscription: %v", err)
	}
	if err := c.Subscribe(ctx(t), []byte("t0"), func(_, _ []byte) {}); err != nil {
		t.Fatalf("re-subscribing: %v", err)
	}
	if err := c.Unsubscribe(ctx(t), []byte("t1")); err != nil {
		t.Fatal(err)
	}
	if err := c.Subscribe(ctx(t), []byte("t3"), func(_, _ []byte) {}); err != nil {
		t.Fatalf("subscribing after freeing a slot: %v", err)
	}

	// Streams: a client may hold max_streams open; the next is refused,
	// and closing one frees it.
	key, _ := identity.Generate()
	ep := dial(t, url)
	if err := ep.RegisterEndpoint(ctx(t), key); err != nil {
		t.Fatal(err)
	}
	cl := dial(t, url)
	var open []*relayclient.Stream
	for range 2 {
		s, err := cl.OpenStream(ctx(t), key.Public)
		if err != nil {
			t.Fatal(err)
		}
		open = append(open, s)
	}
	if _, err := cl.OpenStream(ctx(t), key.Public); err == nil || !strings.Contains(err.Error(), "stream limit") {
		t.Fatalf("third stream: %v", err)
	}
	open[0].Close("done")
	var err error
	for range 50 {
		var s *relayclient.Stream
		if s, err = cl.OpenStream(ctx(t), key.Public); err == nil {
			s.Close("done")
			break
		}
	}
	if err != nil {
		t.Fatalf("stream after closing one: %v", err)
	}

	// Messages in flight: a third unfinished message is refused with LIMIT.
	raw, _, err := websocket.Dial(ctx(t), url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.CloseNow()
	startRaw(t, raw)
	for id := uint64(1); id <= 3; id++ {
		writeRaw(t, raw, &wire.RelayFrame{Kind: &wire.RelayFrame_Publish{Publish: &wire.Publish{Topic: []byte("p"), MessageId: id, Data: []byte("x")}}})
	}
	if f := readRaw(t, raw); f.GetError().GetCode() != wire.ErrorCode_ERROR_CODE_LIMIT {
		t.Fatalf("third message in flight: %v", f)
	}

	// Connections: at max_connections the relay answers 503 before the
	// WebSocket upgrade.
	for srv.Connections() < 6 {
		dial(t, url)
	}
	resp, err := http.Get("http" + strings.TrimPrefix(url, "ws"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("connection over the cap: %d", resp.StatusCode)
	}
	if _, err := relayclient.Dial(ctx(t), url); err == nil {
		t.Fatal("connection over max_connections accepted")
	}
}
