package meta

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/relayurl"
	"github.com/hyperkubeorg/openarcanum/internal/version"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Heartbeats naming URLs whose host stalls (every fetch takes its whole
// timeout) can't push a proven relay out of the registry: a heartbeat for
// a URL whose last proof matched its key is accepted while every slot is
// busy, one client network gets a few fetches a minute for URLs it hasn't
// proven, and heartbeats for one URL share a fetch.
func TestRelayProofPoolSaturation(t *testing.T) {
	s := openTestNode(t, nil, nil)
	s.cfg.AllowPrivateRelays = true
	s.relayProofs = newRelayProofs(true)
	clk := time.Now()
	s.now = func() time.Time { return clk }
	real, _ := identity.Generate()
	// stop releases the stalled fetches; their heartbeats then finish.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	defer func() { close(stop); wg.Wait() }()
	var mu sync.Mutex
	stalled := map[string]int{}
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/real" {
			_, _ = fmt.Fprintln(w, identity.Encode(real.Public))
			return
		}
		mu.Lock()
		stalled[r.URL.Path]++
		mu.Unlock()
		select {
		case <-stop:
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer host.Close()
	base := "ws://" + strings.TrimPrefix(host.URL, "http://")
	heartbeat := func(k *identity.Key, path string, from string) int {
		hb, _ := proto.Marshal(&wire.RelayHeartbeat{RelayKey: k.Public, Url: base + path, At: clk.UnixMilli(), Capacity: 1})
		body, _ := proto.Marshal(&wire.SignedHeartbeat{Heartbeat: hb, Signature: k.Sign(identity.CtxRelayHeartbeat, hb)})
		req := httptest.NewRequest(http.MethodPost, "/v1/relays/heartbeat", bytes.NewReader(body))
		req.RemoteAddr = from
		req.Header.Set(version.Header, version.Format(version.Supported()))
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec.Code
	}
	if code := heartbeat(real, "/real", "192.0.2.1:1"); code != 200 {
		t.Fatalf("the relay's first heartbeat: %d", code)
	}
	fetchesStalled := func() int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, c := range stalled {
			n += c
		}
		return n
	}

	// Stalling URLs from a few networks take every slot; their heartbeats
	// wait for their fetch, so they run in the background.
	squatter, _ := identity.Generate()
	networks := (relayProofsAtOnce + relayProofsPerNet - 1) / relayProofsPerNet
	for i := range relayProofsAtOnce {
		wg.Add(1)
		go func() {
			defer wg.Done()
			heartbeat(squatter, fmt.Sprintf("/stall%d", i), fmt.Sprintf("198.51.100.%d:1", i%networks+1))
		}()
	}
	deadline := time.Now().Add(10 * time.Second)
	for fetchesStalled() < relayProofsAtOnce {
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d fetches started", fetchesStalled(), relayProofsAtOnce)
		}
		time.Sleep(time.Millisecond)
	}
	// Another network's unproven URL finds no slot; the proven relay, its
	// proof past relayProofTTL, is still accepted.
	if code := heartbeat(squatter, "/another", "203.0.113.9:1"); code != http.StatusTooManyRequests {
		t.Fatalf("unproven URL with every slot busy: %d, want 429", code)
	}
	url, _ := relayurl.Canonical(base + "/real")
	s.relayProofs.mu.Lock()
	e := s.relayProofs.m[url]
	e.at = e.at.Add(-relayProofTTL)
	s.relayProofs.m[url] = e
	s.relayProofs.mu.Unlock()
	clk = clk.Add(time.Second)
	if code := heartbeat(real, "/real", "192.0.2.1:1"); code != 200 {
		t.Fatalf("the proven relay with every slot busy: %d", code)
	}
	var at int64
	if err := s.db.QueryRow(`SELECT at FROM relays WHERE url = ?`, url).Scan(&at); err != nil || at != clk.UnixMilli() {
		t.Fatalf("the relay's heartbeat time: %d %v, want %d", at, err, clk.UnixMilli())
	}
	// One network has used its fetches for the minute: the next unproven
	// URL from it is refused before the slots are even looked at, and a
	// second heartbeat for a URL under way starts no second fetch.
	if code := heartbeat(squatter, "/more", "198.51.100.1:1"); code != http.StatusTooManyRequests {
		t.Fatalf("over the network's fetches: %d, want 429", code)
	}
	if n := fetchesStalled(); n != relayProofsAtOnce {
		t.Fatalf("%d fetches started, want %d", n, relayProofsAtOnce)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if code := heartbeat(squatter, "/stall0", "203.0.113.10:1"); code != http.StatusForbidden {
			t.Errorf("second heartbeat for a URL under way: %d, want 403 once its fetch failed", code)
		}
	}()
	close(stop)
	wg.Wait()
	for path, n := range stalled {
		if n != 1 {
			t.Errorf("%s fetched %d times", path, n)
		}
	}
	stop = make(chan struct{})
}
