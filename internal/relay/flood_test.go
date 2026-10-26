package relay_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/pow"
	"github.com/hyperkubeorg/openarcanum/internal/relay"
	"github.com/hyperkubeorg/openarcanum/internal/version"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// dialFrom opens a WebSocket to url from the local address ip (any
// 127.0.0.0/8 address reaches the loopback listener on Linux) and reads
// Hello, without starting.
func dialFrom(ctx context.Context, url, ip string) (*websocket.Conn, *wire.Hello, error) {
	d := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(ip)}}
	hc := &http.Client{Transport: &http.Transport{DialContext: d.DialContext}}
	ws, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPClient: hc})
	if err != nil {
		return nil, nil, err
	}
	_, b, err := ws.Read(ctx)
	if err != nil {
		ws.CloseNow()
		return nil, nil, err
	}
	var f wire.RelayFrame
	if err := proto.Unmarshal(b, &f); err != nil || f.GetHello() == nil {
		ws.CloseNow()
		return nil, nil, fmt.Errorf("no hello: %v", err)
	}
	return ws, f.GetHello(), nil
}

// startClient dials from ip and completes Start, solving any proof-of-work.
func startClient(t *testing.T, url, ip string) *websocket.Conn {
	t.Helper()
	ws, hello, err := dialFrom(ctx(t), url, ip)
	if err != nil {
		t.Fatalf("a client from %s couldn't connect: %v", ip, err)
	}
	t.Cleanup(func() { ws.CloseNow() })
	counter, err := pow.Solve(ctx(t), hello.Nonce, hello.PowBits)
	if err != nil {
		t.Fatal(err)
	}
	writeRaw(t, ws, startFrame(version.Current, counter))
	if f := readRaw(t, ws); f.GetAck() == nil {
		t.Fatalf("start from %s: %v", ip, f)
	}
	return ws
}

func waitUnstarted(t *testing.T, srv *relay.Server, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for srv.Unstarted() != want {
		if time.Now().After(deadline) {
			t.Fatalf("relay holds %d unstarted connections, want %d", srv.Unstarted(), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Connections that never send Start can't lock real clients out (a flood
// that takes a slot per connection, held for the start window, with no
// proof-of-work before load):
//   - one network (an IPv4 address here) holds at most max_unstarted_per_ip
//     unstarted connections, and its next ones are refused at once;
//   - a client from another address still connects and starts;
//   - all networks together hold at most max_unstarted; past that a
//     newcomer takes the oldest unstarted connection's slot, so the flood
//     can't keep a real client out after the cap either;
//   - started connections hold no unstarted slot.
func TestUnstartedFloodCantExhaustSlots(t *testing.T) {
	srv, url := start(t, func(c *relay.Config) {
		c.MaxConnections = 40
		c.MaxUnstarted = 8
		c.MaxUnstartedPerIP = 3
		c.PowMode = "off"
	})
	var flood []*websocket.Conn
	t.Cleanup(func() {
		for _, ws := range flood {
			ws.CloseNow()
		}
	})
	for i := range 20 {
		ws, _, err := dialFrom(ctx(t), url, "127.0.0.2")
		switch {
		case i < 3 && err != nil:
			t.Fatalf("unstarted connection %d refused: %v", i, err)
		case i >= 3 && err == nil:
			flood = append(flood, ws)
			t.Fatalf("unstarted connection %d from one address accepted", i)
		case err == nil:
			flood = append(flood, ws)
		}
	}
	waitUnstarted(t, srv, 3)
	if srv.Stats().UnstartedRefused.Load() != 17 {
		t.Fatalf("refusals counted: %d", srv.Stats().UnstartedRefused.Load())
	}
	// Another address: in, and it starts (then holds no unstarted slot).
	startClient(t, url, "127.0.0.3")
	waitUnstarted(t, srv, 3)

	// Five more networks fill max_unstarted.
	for i := range 5 {
		ws, _, err := dialFrom(ctx(t), url, fmt.Sprintf("127.0.1.%d", i+1))
		if err != nil {
			t.Fatalf("network %d: %v", i, err)
		}
		flood = append(flood, ws)
	}
	waitUnstarted(t, srv, 8)
	// A real client from yet another network still gets in: the oldest
	// unstarted connection gives up its slot and is closed.
	startClient(t, url, "127.0.2.1")
	if srv.Stats().UnstartedEvicted.Load() != 1 {
		t.Fatalf("evictions: %d", srv.Stats().UnstartedEvicted.Load())
	}
	if _, _, err := flood[0].Read(ctx(t)); err == nil {
		t.Fatal("the oldest unstarted connection wasn't closed")
	}
	waitUnstarted(t, srv, 7)
	// Many real clients in a row, with the flood still holding its slots.
	for i := range 10 {
		startClient(t, url, fmt.Sprintf("127.0.3.%d", i+1))
	}
	if srv.Connections() > 40 {
		t.Fatalf("%d connections, over max_connections", srv.Connections())
	}
}

// An unstarted connection allocates little: the send queue grows on demand
// instead of a buffer for 8192 frames (about 192 KiB) at accept time.
func TestUnstartedConnectionIsCheap(t *testing.T) {
	_, url := start(t, func(c *relay.Config) { c.MaxUnstartedPerIP = 1000; c.MaxUnstarted = 1000 })
	heap := func() uint64 {
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return m.HeapAlloc
	}
	var conns []*websocket.Conn
	t.Cleanup(func() {
		for _, ws := range conns {
			ws.CloseNow()
		}
	})
	before := heap()
	const n = 100
	for range n {
		ws, _, err := dialFrom(ctx(t), url, "127.0.5.1")
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, ws)
	}
	// Both ends of each connection live in this process.
	per := (int64(heap()) - int64(before)) / n
	t.Logf("%d bytes of heap per unstarted connection (both ends)", per)
	if per > 64<<10 {
		t.Fatalf("%d bytes of heap per unstarted connection (both ends)", per)
	}
}

// Under load the start window is short, so slots held by connections that
// never start turn over fast; and an unstarted connection runs only its
// writer besides the handler, not a pinger or a timer goroutine.
func TestStartWindowShortUnderLoad(t *testing.T) {
	srv, url := start(t, func(c *relay.Config) {
		c.PowMode = "always"
		c.PowBits = 1
		c.StartWindow.Duration = time.Minute
		c.StartWindowUnderLoad.Duration = time.Second
	})
	time.Sleep(50 * time.Millisecond)
	before := runtime.NumGoroutine()
	var conns []*websocket.Conn
	t.Cleanup(func() {
		for _, ws := range conns {
			ws.CloseNow()
		}
	})
	for range 20 {
		ws, h, err := dialFrom(ctx(t), url, "127.0.4.1")
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, ws)
		if h.StartWindowMs != 1000 {
			t.Fatalf("start window under load: %d ms", h.StartWindowMs)
		}
	}
	// Per connection: the relay's handler and writer, the HTTP server's
	// connection goroutine, and this test's client side (websocket and
	// HTTP transport); never a pinger or start-timer goroutine.
	if per := float64(runtime.NumGoroutine()-before) / 20; per > 5.5 {
		t.Fatalf("%.1f goroutines per unstarted connection", per)
	}
	waitUnstarted(t, srv, 0)
	if srv.Stats().StartTimeouts.Load() != 20 {
		t.Fatalf("start timeouts: %d", srv.Stats().StartTimeouts.Load())
	}
}

// The new limits are validated like the rest of the config.
func TestUnstartedAndCacheConfigBounds(t *testing.T) {
	for name, mut := range map[string]func(*relay.Config){
		"max_unstarted below 0":           func(c *relay.Config) { c.MaxUnstarted = -1 },
		"max_unstarted over max":          func(c *relay.Config) { c.MaxUnstarted = c.MaxConnections + 1 },
		"max_unstarted_per_ip 0":          func(c *relay.Config) { c.MaxUnstartedPerIP = 0 },
		"start_window_under_load 0":       func(c *relay.Config) { c.StartWindowUnderLoad.Duration = 0 },
		"start_window_under_load too big": func(c *relay.Config) { c.StartWindowUnderLoad.Duration = time.Hour },
		"chunk_cache_conn_bytes over cache": func(c *relay.Config) {
			c.ChunkCacheBytes, c.ChunkCacheTopicBytes, c.ChunkCacheConnBytes = 1<<20, 1<<20, 2<<20
		},
	} {
		c := relay.DefaultConfig()
		mut(&c)
		if c.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
