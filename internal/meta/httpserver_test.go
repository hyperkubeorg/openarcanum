package meta_test

import (
	"bytes"
	"crypto/tls"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/meta"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestRedactAddrs(t *testing.T) {
	for in, want := range map[string]string{
		"http: TLS handshake error from 203.0.113.9:51234: EOF":          "http: TLS handshake error from [address]: EOF",
		"http: TLS handshake error from [2001:db8::1]:443: remote error": "http: TLS handshake error from [address]: remote error",
		"http: panic serving 10.0.0.1:80: boom":                          "http: panic serving [address]: boom",
		"accept from 2001:db8::7, retrying":                              "accept from [address], retrying",
		"http2: server: error reading preface from client 192.0.2.4:1":   "http2: server: error reading preface from client [address]",
		"no addresses here: 12:34":                                       "no addresses here: 12:34",
	} {
		if got := meta.RedactAddrs(in); got != want {
			t.Errorf("RedactAddrs(%q) = %q, want %q", in, got, want)
		}
	}
}

// The API server has timeouts, and its error log (a failed TLS handshake
// here) never records the client's address.
func TestHTTPServerNeverLogsClientAddresses(t *testing.T) {
	var buf lockedBuffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	hs := meta.NewHTTPServer(http.NotFoundHandler(), log)
	if hs.ReadHeaderTimeout == 0 || hs.ReadTimeout == 0 || hs.WriteTimeout == 0 || hs.IdleTimeout == 0 || hs.ErrorLog == nil {
		t.Fatalf("server without timeouts or error log: %+v", hs)
	}
	ts := httptest.NewUnstartedServer(hs.Handler)
	ts.Config = hs
	ts.StartTLS()
	defer ts.Close()
	// A client that doesn't trust the certificate: the server logs a
	// handshake error.
	c, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	tc := tls.Client(c, &tls.Config{ServerName: "example.invalid"})
	_ = tc.Handshake()
	tc.Close()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(buf.String(), "handshake") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	out := buf.String()
	if !strings.Contains(out, "handshake") {
		t.Fatalf("no handshake error logged: %q", out)
	}
	if strings.Contains(out, "127.0.0.1") {
		t.Fatalf("client address logged: %q", out)
	}
}
