package community

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A host runs its communities side by side; owners start more (Create) up
// to max_communities, and they come back on the next start.
func TestHostCreatesAndReopensCommunities(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig(dir)
	cfg.Relays = []string{"wss://relay.example.invalid/ws"}
	cfg.ClaimFile = filepath.Join(dir, "claim-code")
	cfg.MaxCommunities = 2
	first, err := InitHost(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first.ClaimCode, ClaimPrefix) {
		t.Fatalf("claim code %q", first.ClaimCode)
	}
	if _, err := InitHost(context.Background(), cfg); err == nil {
		t.Fatal("a second init of the same host was allowed")
	}

	ctx, cancel := context.WithCancel(context.Background())
	host, err := OpenHost(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- host.Run(ctx) }()
	var code string
	for deadline := time.Now().Add(5 * time.Second); ; {
		if code, err = host.Create(ctx); err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond) // Run hasn't started yet
	}
	if err != nil || !strings.HasPrefix(code, ClaimPrefix) || code == first.ClaimCode {
		t.Fatalf("create: %q %v", code, err)
	}
	servers := host.Servers()
	if len(servers) != 2 {
		t.Fatalf("%d communities", len(servers))
	}
	var keys [][]byte
	for _, s := range servers {
		keys = append(keys, s.storageKey)
		if s.host != host {
			t.Fatal("a community doesn't know its host")
		}
	}
	if bytes.Equal(keys[0], keys[1]) {
		t.Fatal("two communities share a storage key")
	}
	if _, err := host.Create(ctx); err == nil || !strings.Contains(err.Error(), "max_communities") {
		t.Fatalf("over the limit: %v", err)
	}
	cancel()
	<-done
	host.Close()

	again, err := OpenHost(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if n := len(again.Servers()); n != 2 {
		t.Fatalf("reopened with %d communities", n)
	}
	for id, s := range again.Servers() {
		if !bytes.Equal(s.storageKey, servers[id].storageKey) {
			t.Fatal("a community's storage key changed across restarts")
		}
	}
}
