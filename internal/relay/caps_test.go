package relay_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/relay"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Each started connection can hold max_subscriptions subscriptions (about
// 480 bytes each), so without per-network caps one host could hold
// gigabytes of relay memory. Connections and subscriptions are capped per
// network (IPv4 address or IPv6 /64), and subscriptions in all.

func TestConnectionsPerNetworkCapped(t *testing.T) {
	srv, url := start(t, func(c *relay.Config) { c.MaxConnectionsPerIP = 3 })
	for i := range 3 {
		if _, err := clientFrom(t, url, "127.0.0.1"); err != nil {
			t.Fatalf("connection %d: %v", i, err)
		}
	}
	_, err := clientFrom(t, url, "127.0.0.1")
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("connection over max_connections_per_ip: %v", err)
	}
	if srv.Stats().NetworkRefused.Load() != 1 {
		t.Fatalf("network refusals = %d", srv.Stats().NetworkRefused.Load())
	}
	// Another network is unaffected.
	if _, err := clientFrom(t, url, "127.0.0.2"); err != nil {
		t.Fatalf("another network: %v", err)
	}
}

func TestConnectionPlaceGivenBack(t *testing.T) {
	srv, url := start(t, func(c *relay.Config) { c.MaxConnectionsPerIP = 1 })
	c, err := clientFrom(t, url, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	deadline := time.Now().Add(5 * time.Second)
	for srv.Connections() > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := clientFrom(t, url, "127.0.0.1"); err != nil {
		t.Fatalf("after closing the network's only connection: %v", err)
	}
}

func TestSubscriptionsPerNetworkCapped(t *testing.T) {
	_, url := start(t, func(c *relay.Config) {
		c.MaxSubscriptions = 10
		c.MaxSubscriptionsPerIP = 15
		c.MaxSubscriptionsTotal = 25
	})
	a1 := mustDialFrom(t, url, "127.0.0.1")
	a2 := mustDialFrom(t, url, "127.0.0.1")
	b := mustDialFrom(t, url, "127.0.0.2")
	c := mustDialFrom(t, url, "127.0.0.3")
	topic := func(i int) []byte { return []byte(fmt.Sprintf("topic-%04d", i)) }
	for i := range 10 {
		if err := a1.Subscribe(ctx(t), topic(i), func(_, _ []byte) {}); err != nil {
			t.Fatal(err)
		}
	}
	// Re-subscribing a topic it holds costs nothing.
	if err := a1.Subscribe(ctx(t), topic(0), func(_, _ []byte) {}); err != nil {
		t.Fatal(err)
	}
	for i := 10; i < 15; i++ {
		if err := a2.Subscribe(ctx(t), topic(i), func(_, _ []byte) {}); err != nil {
			t.Fatal(err)
		}
	}
	err := a2.Subscribe(ctx(t), topic(15), func(_, _ []byte) {})
	if err == nil || relayCode(t, err) != wire.ErrorCode_ERROR_CODE_LIMIT || !strings.Contains(err.Error(), "network") {
		t.Fatalf("subscription over max_subscriptions_per_ip: %v", err)
	}
	// Another network has its own allowance, up to the total.
	for i := range 10 {
		if err := b.Subscribe(ctx(t), topic(100+i), func(_, _ []byte) {}); err != nil {
			t.Fatal(err)
		}
	}
	err = c.Subscribe(ctx(t), topic(200), func(_, _ []byte) {})
	if err == nil || relayCode(t, err) != wire.ErrorCode_ERROR_CODE_LIMIT || !strings.Contains(err.Error(), "relay full") {
		t.Fatalf("subscription over max_subscriptions_total: %v", err)
	}
	// Unsubscribing and closing give subscriptions back.
	if err := a2.Unsubscribe(ctx(t), topic(10)); err != nil {
		t.Fatal(err)
	}
	if err := a2.Unsubscribe(ctx(t), topic(10)); err != nil {
		t.Fatal(err) // twice: counted once
	}
	if err := c.Subscribe(ctx(t), topic(200), func(_, _ []byte) {}); err != nil {
		t.Fatalf("after an unsubscribe: %v", err)
	}
	b.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := c.Subscribe(ctx(t), topic(201), func(_, _ []byte) {})
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after a connection with subscriptions closed: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// In pow_mode "load", a network that already holds pow_ip_connections
// connections pays the proof-of-work for more, though the relay isn't
// under load; other networks don't.
func TestPowForBusyNetwork(t *testing.T) {
	_, url := start(t, func(c *relay.Config) { c.PowIPConnections = 2; c.PowBits = 8 })
	for i := range 2 {
		ws, hello, err := dialFrom(ctx(t), url, "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ws.CloseNow() })
		if hello.PowBits != 0 {
			t.Fatalf("connection %d asked for a proof-of-work", i)
		}
	}
	ws, hello, err := dialFrom(ctx(t), url, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.CloseNow() })
	if hello.PowBits != 8 {
		t.Fatalf("third connection from one network: pow_bits %d, want 8", hello.PowBits)
	}
	ws2, hello, err := dialFrom(ctx(t), url, "127.0.0.2")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws2.CloseNow() })
	if hello.PowBits != 0 {
		t.Fatal("another network asked for a proof-of-work")
	}
	// The relay client solves it and starts.
	if _, err := clientFrom(t, url, "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
}
