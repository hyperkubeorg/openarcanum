package relay

import (
	"context"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/ops"
)

func dedicatedServer(t *testing.T, mutate func(*Config)) *Server {
	t.Helper()
	cfg := DefaultConfig()
	cfg.BlockFile = filepath.Join(t.TempDir(), "blocks.json")
	mutate(&cfg)
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	s := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if s.Err() != nil {
		t.Fatal(s.Err())
	}
	return s
}

// upgradeStatus is what the relay answers a WebSocket request from addr
// with: 403 for a blocked network, otherwise the upgrade is attempted (and
// fails on a recorder, which isn't a hijackable connection).
func upgradeStatus(s *Server, addr string) int {
	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.RemoteAddr = addr
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w.Code
}

func TestBlockCoversIPv6Slash64(t *testing.T) {
	k, _ := identity.Generate()
	s := dedicatedServer(t, func(c *Config) { c.DedicatedServerKey = hex.EncodeToString(k.Public) })
	now := time.Now()
	network := ops.NetworkKey("2001:db8:1:2::5")
	if err := s.blocks.add(&blockEntry{ID: make([]byte, 16), Networks: []string{network}, CreatedAt: now.UnixMilli()}, now); err != nil {
		t.Fatal(err)
	}
	for addr, blocked := range map[string]bool{
		"[2001:db8:1:2::5]:4000":            true,
		"[2001:db8:1:2:ffff:ffff:ffff:1]:1": true, // same /64
		"[2001:db8:1:3::5]:4000":            false,
		"198.51.100.7:4000":                 false,
	} {
		if got := upgradeStatus(s, addr) == http.StatusForbidden; got != blocked {
			t.Errorf("%s: blocked=%v, want %v", addr, got, blocked)
		}
	}
}

func TestBlockExpires(t *testing.T) {
	k, _ := identity.Generate()
	s := dedicatedServer(t, func(c *Config) { c.DedicatedServerKey = hex.EncodeToString(k.Public) })
	now := time.Now()
	if err := s.blocks.add(&blockEntry{ID: make([]byte, 16), Networks: []string{"198.51.100.7"}, CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(time.Minute).UnixMilli()}, now); err != nil {
		t.Fatal(err)
	}
	if !s.blocks.blocked("198.51.100.7", now.Add(59*time.Second)) {
		t.Fatal("not blocked before the end")
	}
	if s.blocks.blocked("198.51.100.7", now.Add(time.Minute)) {
		t.Fatal("still blocked after the end")
	}
	if s.blocks.count(now.Add(time.Minute)) != 0 {
		t.Fatal("expired block counted")
	}
}

func TestDedicatedKeyFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "server.pub")
	s := dedicatedServer(t, func(c *Config) { c.DedicatedServerKeyFile = file })
	if s.DedicatedKnown() || s.endpointAllowed(make([]byte, 32)) {
		t.Fatal("a relay waiting for its key file serves something")
	}
	old := dedicatedKeyPoll
	dedicatedKeyPoll = 20 * time.Millisecond
	t.Cleanup(func() { dedicatedKeyPoll = old })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.WatchDedicatedKey(ctx)

	k1, _ := identity.Generate()
	if err := os.WriteFile(file, []byte(hex.EncodeToString(k1.Public)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return s.endpointAllowed(k1.Public) })
	// A rotated key replaces it; the old one is no longer served.
	k2, _ := identity.Generate()
	if err := os.WriteFile(file, []byte(hex.EncodeToString(k2.Public)), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return s.endpointAllowed(k2.Public) })
	if s.endpointAllowed(k1.Public) {
		t.Fatal("the old key is still served")
	}
	// A file that isn't a key leaves the current one.
	if err := os.WriteFile(file, []byte("nonsense"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if !s.endpointAllowed(k2.Public) {
		t.Fatal("a bad key file dropped the key")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition never became true")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
