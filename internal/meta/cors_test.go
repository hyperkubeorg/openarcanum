package meta_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/meta"
	"github.com/hyperkubeorg/openarcanum/internal/ops"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/version"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

const webOrigin = "https://app.example.invalid"

func preflight(t *testing.T, url, origin string, extra map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodOptions, url, nil)
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "content-type,openarcanum-protocol")
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	return res
}

// browserPost is a POST as the web client's page makes it.
func browserPost(t *testing.T, client *http.Client, url, origin string, msg proto.Message) *http.Response {
	t.Helper()
	body, _ := proto.Marshal(msg)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Origin", origin)
	req.Header.Set("Content-Type", "application/x-protobuf")
	version.SetRequest(req)
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	return res
}

// By default any web page may call the API: preflights are answered
// without touching the routes or the write rate limit, answers carry
// Access-Control-Allow-Origin: * and expose the protocol header, and
// credentials are never allowed (none exist: no cookies are set).
func TestCORSOpenByDefault(t *testing.T) {
	m := testutil.StartMeta(t, func(c *meta.Config) { c.WritesPerMinute = 1 })
	url := m.URL + "/v1/mailbox/fetch"
	for i := 0; i < 50; i++ {
		res := preflight(t, url, webOrigin, nil)
		h := res.Header
		if res.StatusCode != http.StatusNoContent || h.Get("Access-Control-Allow-Origin") != "*" ||
			!strings.Contains(h.Get("Access-Control-Allow-Methods"), "POST") ||
			!strings.Contains(h.Get("Access-Control-Allow-Headers"), version.Header) ||
			h.Get("Access-Control-Max-Age") == "" || h.Get("Access-Control-Allow-Credentials") != "" {
			t.Fatalf("preflight %d: %d %v", i, res.StatusCode, h)
		}
	}
	// Fifty preflights later the one write a minute is still there.
	res := browserPost(t, http.DefaultClient, url, webOrigin, &wire.MailboxFetch{Token: make([]byte, 32)})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("write after preflights: %d", res.StatusCode)
	}
	if res.Header.Get("Access-Control-Allow-Origin") != "*" || res.Header.Get("Access-Control-Expose-Headers") != version.Header ||
		res.Header.Get("Set-Cookie") != "" || res.Header.Get(version.Header) == "" {
		t.Fatalf("answer headers: %v", res.Header)
	}
	// Errors are readable by the page too (it learns it was rate limited).
	res = browserPost(t, http.DefaultClient, url, webOrigin, &wire.MailboxFetch{Token: make([]byte, 32)})
	if res.StatusCode != http.StatusTooManyRequests || res.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("rate-limited answer: %d %v", res.StatusCode, res.Header)
	}
	// GETs, and Chromium's private-network preflight.
	req, _ := http.NewRequest(http.MethodGet, m.URL+"/v1/relays", nil)
	req.Header.Set("Origin", webOrigin)
	version.SetRequest(req)
	get, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = get.Body.Close()
	if get.StatusCode != http.StatusOK || get.Header.Get("Access-Control-Allow-Origin") != "*" || get.Header.Get("Set-Cookie") != "" {
		t.Fatalf("GET: %d %v", get.StatusCode, get.Header)
	}
	pna := preflight(t, m.URL+"/v1/relays", webOrigin, map[string]string{"Access-Control-Request-Private-Network": "true"})
	if pna.Header.Get("Access-Control-Allow-Private-Network") != "true" {
		t.Fatalf("private network preflight: %v", pna.Header)
	}
	// Requests without an Origin (desktop clients, peers) are unchanged.
	req, _ = http.NewRequest(http.MethodGet, m.URL+"/v1/relays", nil)
	version.SetRequest(req)
	plain, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = plain.Body.Close()
	if plain.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("CORS headers without an Origin: %v", plain.Header)
	}
}

// cors_origins narrows the pages allowed; empty turns CORS off.
func TestCORSOriginsNarrowedOrOff(t *testing.T) {
	m := testutil.StartMeta(t, func(c *meta.Config) { c.CORSOrigins = []string{webOrigin} })
	url := m.URL + "/v1/mailbox/fetch"
	ok := preflight(t, url, webOrigin, nil)
	if ok.StatusCode != http.StatusNoContent || ok.Header.Get("Access-Control-Allow-Origin") != webOrigin ||
		!strings.Contains(strings.Join(ok.Header.Values("Vary"), ","), "Origin") {
		t.Fatalf("allowed origin: %d %v", ok.StatusCode, ok.Header)
	}
	if no := preflight(t, url, "https://evil.example.invalid", nil); no.StatusCode != http.StatusForbidden || no.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("other origin preflight: %d %v", no.StatusCode, no.Header)
	}
	res := browserPost(t, http.DefaultClient, url, "https://evil.example.invalid", &wire.MailboxFetch{Token: make([]byte, 32)})
	if res.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("other origin answer: %v", res.Header)
	}

	off := testutil.StartMeta(t, func(c *meta.Config) { c.CORSOrigins = nil })
	if res := preflight(t, off.URL+"/v1/mailbox/fetch", webOrigin, nil); res.Header.Get("Access-Control-Allow-Origin") != "" || res.StatusCode == http.StatusNoContent {
		t.Fatalf("CORS off: %d %v", res.StatusCode, res.Header)
	}

	for _, bad := range [][]string{{"https://app.example.invalid/"}, {"app.example.invalid"}, {"https://app.example.invalid/path"}, {"*", webOrigin}, {"ftp://app.example.invalid"}} {
		c := meta.DefaultConfig()
		c.DataDir = t.TempDir()
		c.CORSOrigins = bad
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "cors_origins") {
			t.Errorf("cors_origins %v: %v", bad, err)
		}
	}
}

// A maintainer node with TLS on serves the API (HTTP/2 offered) on a
// listener from ops.ListenPublic, as cmd/meta runs it, to a browser-like
// client on another origin.
func TestMetaOverTLS(t *testing.T) {
	certFile, keyFile, roots := testutil.SelfSignedTLS(t, t.TempDir(), "maintainer.example.invalid", "127.0.0.1")
	cfg := meta.DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.TLSMode, cfg.TLSCertFile, cfg.TLSKeyFile = ops.TLSFiles, certFile, keyFile
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.ACMECache() != cfg.DataDir+"/acme" {
		t.Fatalf("default acme cache %q", cfg.ACMECache())
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv, err := meta.Open(ctx, cfg, testutil.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ln, err := ops.ListenPublic(ctx, "127.0.0.1:0", cfg.TLSSettings, ops.TLSOptions{CacheDir: cfg.ACMECache(), HTTP2: true})
	if err != nil {
		t.Fatal(err)
	}
	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = hs.Serve(ln) }()
	defer hs.Close()

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}, ForceAttemptHTTP2: true}}
	base := "https://" + ln.Addr().String()
	res := browserPost(t, client, base+"/v1/mailbox/fetch", webOrigin, &wire.MailboxFetch{Token: make([]byte, 32)})
	if res.StatusCode != http.StatusOK || res.ProtoMajor != 2 || res.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("over TLS: %d %s %v", res.StatusCode, res.Proto, res.Header)
	}
	if _, err := http.Get(base + "/healthz"); err == nil {
		t.Fatal("an untrusted certificate was accepted")
	}
}
