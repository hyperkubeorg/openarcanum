package meta_test

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/meta"
	"github.com/hyperkubeorg/openarcanum/internal/store"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// A removed relay can't come back by spelling its URL differently: the
// registry stores and compares canonical URLs, and a removal covers the
// relay's whole host. Variants of one relay are one registry entry.
func TestRemovedRelayVariantsRefused(t *testing.T) {
	var dir string
	n := startNode(t, func(c *meta.Config) { dir = c.DataDir })
	ctx := context.Background()
	k, _ := identity.Generate()
	now := time.Now()
	if code := n.post(t, "/v1/relays/heartbeat", heartbeat(k, "WSS://Relay.Example:443/ws/", now), nil); code != 200 {
		t.Fatal(code)
	}
	if code := n.post(t, "/v1/relays/heartbeat", heartbeat(k, "wss://relay.example/ws", now.Add(time.Millisecond)), nil); code != 200 {
		t.Fatal(code)
	}
	var rl wire.RelayList
	n.get(t, "/v1/relays", &rl)
	if len(rl.Relays) != 1 || rl.Relays[0].Url != "wss://relay.example/ws" {
		t.Fatalf("registry: %v", rl.Relays)
	}
	adm, err := meta.OpenAdmin(ctx, dir, meta.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer adm.Close()
	if had, err := adm.RemoveRelay(ctx, " wss://RELAY.example/ws/ ", "abuse"); !had || err != nil {
		t.Fatalf("remove: %v %v", had, err)
	}
	for i, variant := range []string{
		"wss://relay.example/ws",
		"wss://relay.example/ws/",
		"wss://RELAY.example/ws",
		"wss://relay.example:443/ws",
		"wss://relay.example./ws",
		"wss://relay.example:8443/ws",
		"ws://relay.example/other",
	} {
		if code := n.post(t, "/v1/relays/heartbeat", heartbeat(k, variant, now.Add(time.Duration(i+2)*time.Millisecond)), nil); code != http.StatusForbidden {
			t.Errorf("heartbeat as %q after removal: %d", variant, code)
		}
	}
	// A query or fragment isn't a relay URL at all.
	for _, bad := range []string{"wss://relay.example/ws?x", "wss://relay.example/ws#x", "wss://u@relay.example/ws"} {
		if code := n.post(t, "/v1/relays/heartbeat", heartbeat(k, bad, now), nil); code != http.StatusBadRequest {
			t.Errorf("heartbeat as %q: %d", bad, code)
		}
	}
	n.get(t, "/v1/relays", &rl)
	if len(rl.Relays) != 0 {
		t.Fatalf("removed relay listed: %v", rl.Relays)
	}
	// Restoring by any spelling lifts the removal.
	if ok, err := adm.RestoreRelay(ctx, "wss://Relay.Example:443/ws"); !ok || err != nil {
		t.Fatalf("restore: %v %v", ok, err)
	}
	if code := n.post(t, "/v1/relays/heartbeat", heartbeat(k, "wss://relay.example/ws", now.Add(time.Second)), nil); code != 200 {
		t.Fatalf("heartbeat after restore: %d", code)
	}
	// Config removals are canonical too.
	m := startNode(t, func(c *meta.Config) { c.DelistedRelays = []string{"wss://RELAY.EXAMPLE:443/ws/"} })
	if code := m.post(t, "/v1/relays/heartbeat", heartbeat(k, "wss://relay.example:9000/ws", now), nil); code != http.StatusForbidden {
		t.Fatalf("heartbeat for a relay removed in the config: %d", code)
	}
}

// Nameplates store the canonical relay URL and refuse anything else.
func TestNameplateRelayCanonical(t *testing.T) {
	n := startNode(t, nil)
	var np wire.Nameplate
	if code := n.post(t, "/v1/nameplates", &wire.NameplateRequest{Relay: "WS://Relay.Example:80/ws/"}, &np); code != 200 || np.Relay != "ws://relay.example/ws" {
		t.Fatalf("nameplate: %d %q", code, np.Relay)
	}
	if code := n.post(t, "/v1/nameplates", &wire.NameplateRequest{Relay: "ws://relay.example/ws?x=1"}, nil); code != http.StatusBadRequest {
		t.Fatalf("relay URL with a query: %d", code)
	}
}

// Schema version 7 puts relay URLs stored by earlier releases in canonical
// form: registry rows (keeping the freshest of duplicates, dropping what
// isn't a relay URL) and removal decisions.
func TestRelayURLMigration(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Apply(ctx, db, meta.Migrations()[:6]); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	for _, r := range []struct {
		url string
		at  int64
	}{
		{"wss://relay.example/ws", now - 5000},
		{"WSS://Relay.Example:443/ws/", now},
		{"ws://other.example:80/ws", now},
		{"ws://bad.example/ws?x=1", now},
	} {
		if _, err := db.Exec(`INSERT INTO relays (url, relay_key, signed, at, region, capacity, connections, rev) VALUES (?, x'00', x'00', ?, '', 0, 0, 1)`, r.url, r.at); err != nil {
			t.Fatal(err)
		}
	}
	for _, k := range []string{"wss://Removed.Example/ws/", "wss://removed.example/ws", "not a url"} {
		if _, err := db.Exec(`INSERT INTO delisted (kind, key, reason, at) VALUES ('relay', ?, '', 1)`, k); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	cfg := meta.DefaultConfig()
	cfg.DirectoryEnabled = true // the tests exercise the directory; it's off by default
	cfg.DataDir = dir
	srv, err := meta.Open(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.Close()
	adm, err := meta.OpenAdmin(ctx, dir, meta.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer adm.Close()
	rs, err := adm.Relays(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, r := range rs {
		got[r.URL] = r.LastSeen.UnixMilli()
	}
	if len(got) != 2 || got["wss://relay.example/ws"] != now || got["ws://other.example/ws"] != now {
		t.Fatalf("registry after migration: %v", got)
	}
	ds, err := adm.Decisions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, d := range ds {
		keys[d.Key] = true
	}
	if len(keys) != 2 || !keys["wss://removed.example/ws"] || !keys["not a url"] {
		t.Fatalf("decisions after migration: %v", keys)
	}
}
