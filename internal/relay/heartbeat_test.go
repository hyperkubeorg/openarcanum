package relay_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/relay"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/version"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// The relay announces its public_url in canonical form (internal/relayurl),
// the form the registry stores and compares, and refuses a public_url that
// isn't a relay URL.
func TestHeartbeatSendsCanonicalURL(t *testing.T) {
	got := make(chan string, 1)
	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var sh wire.SignedHeartbeat
		var hb wire.RelayHeartbeat
		if proto.Unmarshal(b, &sh) == nil && proto.Unmarshal(sh.Heartbeat, &hb) == nil {
			select {
			case got <- hb.Url:
			default:
			}
		}
		w.Header().Set(version.Header, version.Format(version.Supported()[:1]))
		w.WriteHeader(http.StatusOK)
	}))
	defer reg.Close()
	cfg := relay.DefaultConfig()
	cfg.RegistryURLs = []string{reg.URL}
	cfg.PublicURL = "WSS://Relay.Example:443/ws/"
	cfg.HeartbeatInterval.Duration = time.Hour
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	srv := relay.New(cfg, nil)
	hb, err := relay.NewHeartbeater(cfg, srv, testutil.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	defer func() { cancel(); <-done }()
	go func() { defer close(done); hb.Run(ctx) }()
	select {
	case u := <-got:
		if u != "wss://relay.example/ws" {
			t.Fatalf("heartbeat URL %q", u)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no heartbeat")
	}
	for _, bad := range []string{"wss://relay.example/ws?x=1", "https://relay.example/ws", "wss://u:p@relay.example/ws"} {
		c := relay.DefaultConfig()
		c.RegistryURLs, c.PublicURL = []string{reg.URL}, bad
		if c.Validate() == nil {
			t.Errorf("public_url %q accepted", bad)
		}
	}
}
