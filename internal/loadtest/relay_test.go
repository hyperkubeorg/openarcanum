//go:build load

package loadtest

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/relayclient"
)

// TestRelay: 2,000 concurrent connections on a relay pinned to
// LOAD_SERVER_CPUS, held for LOAD_DURATION. Every connection
// subscribes to one of LOAD_CONNECTIONS/20 topics and one connection per
// topic publishes a 1 KiB message every second, so the relay fans out
// 19 deliveries per message while the rest of each connection is idle
// (pings only), as most members' connections are.
func TestRelay(t *testing.T) {
	conns := envInt("LOAD_CONNECTIONS", 2000)
	duration := envDuration("LOAD_DURATION", 2*time.Minute)
	const perTopic = 20
	topics := conns / perTopic

	port := freePort(t)
	srv := startServer(t, []string{fmt.Sprintf("OPENARCANUM_RELAY_LISTEN=127.0.0.1:%d", port)}, serverBinary(t, "relay"))
	waitPort(t, port)
	url := fmt.Sprintf("ws://127.0.0.1:%d/ws", port)

	ctx, cancel := context.WithTimeout(context.Background(), duration+30*time.Minute)
	defer cancel()
	topicIDs := make([][]byte, topics)
	for i := range topicIDs {
		topicIDs[i] = make([]byte, 32)
		_, _ = rand.Read(topicIDs[i])
	}

	var delivery latencies
	var delivered atomic.Int64
	clients := make([]*relayclient.Client, conns)
	var connectLat latencies
	var failed atomic.Int64
	var firstErr atomic.Value
	sem := make(chan struct{}, 100)
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < conns; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			t0 := time.Now()
			c, err := relayclient.Dial(ctx, url)
			if err == nil {
				err = c.Subscribe(ctx, topicIDs[i%topics], func(_, data []byte) {
					delivered.Add(1)
					if len(data) >= 8 {
						sentAt := time.Unix(0, int64(binary.BigEndian.Uint64(data)))
						delivery.add(time.Since(sentAt))
					}
				})
			}
			if err != nil {
				failed.Add(1)
				firstErr.CompareAndSwap(nil, err.Error())
				return
			}
			connectLat.add(time.Since(t0))
			clients[i] = c
		}(i)
	}
	wg.Wait()
	if n := failed.Load(); n > 0 {
		t.Fatalf("%d of %d connections failed (first: %v)", n, conns, firstErr.Load())
	}
	connectTime := time.Since(start)
	t.Logf("%d connections in %v", conns, connectTime.Round(time.Millisecond))

	// Connection i publishes to topic i for the first `topics` connections.
	cpu0, wall0 := srv.cpuSeconds(t), time.Now()
	var published atomic.Int64
	var pubErr atomic.Int64
	tick := time.NewTicker(time.Second)
	for deadline := time.Now().Add(duration); time.Now().Before(deadline); {
		<-tick.C
		for i := 0; i < topics; i++ {
			msg := make([]byte, 1024)
			binary.BigEndian.PutUint64(msg, uint64(time.Now().UnixNano()))
			if err := clients[i].Publish(topicIDs[i], msg); err != nil {
				pubErr.Add(1)
				continue
			}
			published.Add(1)
		}
	}
	tick.Stop()
	elapsed := time.Since(wall0)
	cpu := srv.cpuSeconds(t) - cpu0
	time.Sleep(3 * time.Second) // the last deliveries
	closed := 0
	for _, c := range clients {
		select {
		case <-c.Done():
			closed++
		default:
		}
	}
	rss, peak := srv.memory(t)
	for _, c := range clients {
		c.Close()
	}

	want := published.Load() * (perTopic - 1)
	report(t, "relay", map[string]any{
		"connections": conns, "topics": topics, "duration_s": duration.Seconds(), "server_cpus": srv.cpus,
		"connect_seconds":  connectTime.Seconds(),
		"connect":          connectLat.summary(),
		"published":        published.Load(),
		"publish_errors":   pubErr.Load(),
		"delivered":        delivered.Load(),
		"expected":         want,
		"delivery":         delivery.summary(),
		"dropped_conns":    closed,
		"server_cpu_cores": cpu / elapsed.Seconds(),
		"server_rss_mib":   rss,
		"server_peak_mib":  peak,
	})
	if closed > 0 {
		t.Errorf("%d connections closed during the run", closed)
	}
	if e := pubErr.Load(); e > 0 {
		t.Errorf("%d publishes failed", e)
	}
	if got := delivered.Load(); got < want {
		t.Errorf("%d deliveries, want %d", got, want)
	}
	if s := delivery.summary(); s.P95 > 250 {
		t.Errorf("delivery p95 %.0f ms (over 250 ms)", s.P95)
	}
}
