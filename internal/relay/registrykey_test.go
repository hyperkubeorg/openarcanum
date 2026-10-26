package relay_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/relay"
	"github.com/hyperkubeorg/openarcanum/internal/relayclient"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
)

// A relay that registers serves its registry key on a plain GET of its URL
// (proof of control for the maintainer's registry, docs/protocol.md §7.3);
// WebSocket clients on the same URL are unaffected.
func TestRelayServesRegistryKey(t *testing.T) {
	r := testutil.StartRelay(t)
	plain := "http" + strings.TrimPrefix(r.URL, "ws")
	cfg := relay.DefaultConfig()
	cfg.RegistryURLs = []string{"http://127.0.0.1:1"}
	cfg.PublicURL = r.URL
	cfg.HeartbeatInterval.Duration = time.Hour
	// The relay signs heartbeats with its own identity key (loaded at start).
	key := r.Server.Key()
	if _, err := relay.NewHeartbeater(cfg, r.Server, testutil.Quiet()); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(plain)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	k, err := identity.Decode(strings.TrimSpace(string(body)))
	if resp.StatusCode != 200 || err != nil || !identity.ValidPublic(k) || identity.Encode(k) != identity.Encode(key.Public) {
		t.Fatalf("GET %s: %d %q", plain, resp.StatusCode, body)
	}
	c, err := relayclient.Dial(t.Context(), r.URL)
	if err != nil {
		t.Fatalf("WebSocket on the same URL: %v", err)
	}
	c.Close()
}
