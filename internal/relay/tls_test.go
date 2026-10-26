package relay_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/ops"
	"github.com/hyperkubeorg/openarcanum/internal/relay"
	"github.com/hyperkubeorg/openarcanum/internal/relayclient"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/version"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// browserTransport makes the requests a browser page on another origin
// makes: an Origin header, no other custom headers.
type browserTransport struct {
	origin string
	base   http.RoundTripper
}

func (b browserTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Origin", b.origin)
	r.Header.Set("User-Agent", "Mozilla/5.0 (test)")
	return b.base.RoundTrip(r)
}

// startTLSRelay runs a relay the way cmd/relay does with tls_mode files,
// on a listener from ops.ListenPublic.
func startTLSRelay(t *testing.T) (url string, roots *x509.CertPool) {
	t.Helper()
	certFile, keyFile, roots := testutil.SelfSignedTLS(t, t.TempDir(), "relay.example.invalid", "127.0.0.1")
	cfg := relay.DefaultConfig()
	cfg.Listen = "127.0.0.1:0"
	cfg.TLSMode, cfg.TLSCertFile, cfg.TLSKeyFile = ops.TLSFiles, certFile, keyFile
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	srv := relay.New(cfg, testutil.Quiet())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ln, err := ops.ListenPublic(ctx, cfg.Listen, cfg.TLSSettings, ops.TLSOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = hs.Serve(ln) }()
	t.Cleanup(func() { srv.CloseAll(); _ = hs.Close() })
	return "wss://" + ln.Addr().String() + "/ws", roots
}

// A browser page served from another origin (the web client's site) can
// use a relay over wss:// directly: the relay accepts any Origin, needs no
// subprotocol or custom header (the version is negotiated in band), and
// serves HTTP/1.1, which browsers open WebSockets over.
func TestBrowserConnectsOverTLSFromAnotherOrigin(t *testing.T) {
	url, roots := startTLSRelay(t)
	tlsCfg := &tls.Config{RootCAs: roots, NextProtos: []string{"h2", "http/1.1"}}
	browser := &http.Client{Transport: browserTransport{
		origin: "https://app.example.invalid",
		base:   &http.Transport{TLSClientConfig: tlsCfg, ForceAttemptHTTP2: true},
	}}
	sub, err := relayclient.DialHTTP(ctx(t), url, browser)
	if err != nil {
		t.Fatalf("browser-like client over wss: %v", err)
	}
	defer sub.Close()
	box := newInbox()
	if err := sub.Subscribe(ctx(t), []byte("tls-topic"), box.handler); err != nil {
		t.Fatal(err)
	}
	// A second page, from yet another origin, publishes.
	other := &http.Client{Transport: browserTransport{origin: "https://other.example.invalid", base: &http.Transport{TLSClientConfig: tlsCfg}}}
	pub, err := relayclient.DialHTTP(ctx(t), url, other)
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	if err := pub.Publish([]byte("tls-topic"), []byte("over tls")); err != nil {
		t.Fatal(err)
	}
	if got := box.wait(t, 1); string(got[0]) != "over tls" {
		t.Fatalf("got %q", got[0])
	}

	// The relay offers only HTTP/1.1 by ALPN, so a browser that would
	// prefer h2 still gets a connection it can upgrade to a WebSocket.
	addr := strings.TrimSuffix(strings.TrimPrefix(url, "wss://"), "/ws")
	conn, err := tls.Dial("tcp", addr, &tls.Config{RootCAs: roots, ServerName: "relay.example.invalid", NextProtos: []string{"h2", "http/1.1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if p := conn.ConnectionState().NegotiatedProtocol; p != "http/1.1" {
		t.Fatalf("negotiated %q", p)
	}

	// Without a trusted certificate there is no connection (no silent
	// fallback to something unverified).
	if _, err := relayclient.DialHTTP(ctx(t), url, &http.Client{}); err == nil {
		t.Fatal("connected without trusting the certificate")
	}
}

// With TLS on and tls_domains set, the relay registers wss://<first>/ws
// unless public_url says otherwise; a ws:// public_url is refused; acme
// needs a cache directory.
func TestTLSRelayAnnouncesWSS(t *testing.T) {
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
	cfg.TLSMode, cfg.TLSDomains, cfg.ACMECacheDir = ops.TLSACME, []string{"Relay.Example.Invalid", "203.0.113.9"}, t.TempDir()
	cfg.HeartbeatInterval.Duration = time.Hour
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if u := cfg.AnnouncedURL(); u != "wss://relay.example.invalid/ws" {
		t.Fatalf("AnnouncedURL = %q", u)
	}
	hb, err := relay.NewHeartbeater(cfg, relay.New(cfg, nil), testutil.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	hctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	defer func() { cancel(); <-done }()
	go func() { defer close(done); hb.Run(hctx) }()
	select {
	case u := <-got:
		if u != "wss://relay.example.invalid/ws" {
			t.Fatalf("heartbeat URL %q", u)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no heartbeat")
	}

	ip := cfg
	ip.TLSDomains = []string{"2001:db8::7"}
	if u := ip.AnnouncedURL(); u != "wss://[2001:db8::7]/ws" {
		t.Fatalf("IP AnnouncedURL = %q", u)
	}
	explicit := cfg
	explicit.PublicURL = "wss://relay.example.invalid:8443/ws"
	if err := explicit.Validate(); err != nil || explicit.AnnouncedURL() != explicit.PublicURL {
		t.Fatalf("explicit public_url: %v %q", err, explicit.AnnouncedURL())
	}
	for name, mut := range map[string]func(*relay.Config){
		"ws public_url": func(c *relay.Config) { c.PublicURL = "ws://relay.example.invalid/ws" },
		"no cache dir":  func(c *relay.Config) { c.ACMECacheDir = "" },
		"no domains":    func(c *relay.Config) { c.TLSDomains = nil },
		"files, no names": func(c *relay.Config) {
			c.TLSMode, c.TLSDomains, c.TLSCertFile, c.TLSKeyFile = ops.TLSFiles, nil, "/c", "/k"
		},
	} {
		c := cfg
		mut(&c)
		if c.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// With TLS off, registry_urls needs public_url.
	off := relay.DefaultConfig()
	off.RegistryURLs, off.TLSDomains = []string{reg.URL}, []string{"relay.example.invalid"}
	if err := off.Validate(); err == nil || !strings.Contains(err.Error(), "public_url") {
		t.Fatalf("TLS off without public_url: %v", err)
	}
}
