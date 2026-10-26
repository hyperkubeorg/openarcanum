package httpx

import (
	"bytes"
	"compress/gzip"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func serve(t *testing.T, h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

var combined = regexp.MustCompile(`^(\S+) - - \[[^\]]+\] "([^"]*)" (\d{3}) (\d+) "([^"]*)" "([^"]*)"\n$`)

// The access log is Apache combined format with "-" for the client, no
// query string, redacted paths and a cut-down Referer.
func TestAccessLogRedacts(t *testing.T) {
	var buf bytes.Buffer
	r := NewRouter()
	r.HandleFunc("/blob/{token}", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "hello") })
	redact := func(p string) string {
		if strings.HasPrefix(p, "/blob/") {
			return "/blob/-"
		}
		return p
	}
	h := Wrap(r, Options{AccessLog: &buf, Redact: redact})
	req := httptest.NewRequest("GET", "/blob/s3cr3t-token?key=hunter2&x=1", nil)
	req.RemoteAddr = "203.0.113.9:51234"
	req.Header.Set("Referer", "https://user:pw@example.invalid/reset/abc?token=zzz#frag")
	req.Header.Set("User-Agent", "test/1.0")
	if rec := serve(t, h, req); rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	line := buf.String()
	m := combined.FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("not combined format: %q", line)
	}
	if m[1] != "-" || m[2] != "GET /blob/-?- HTTP/1.1" || m[3] != "200" || m[4] != "5" || m[5] != "https://example.invalid/reset/abc" || m[6] != "test/1.0" {
		t.Fatalf("fields: %q", m[1:])
	}
	for _, secret := range []string{"203.0.113.9", "s3cr3t", "hunter2", "key=", "zzz", "user:pw", "frag"} {
		if strings.Contains(line, secret) {
			t.Fatalf("log line has %q: %q", secret, line)
		}
	}
}

// The client address appears only when the node opts in, and a trusted
// proxy header names it for rate limiting without being logged otherwise.
func TestClientIPOptInAndProxyHeader(t *testing.T) {
	var seen string
	var leaked bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = NetworkKey(r)
		leaked = r.Header.Get("X-Real-IP") != ""
	})
	var buf bytes.Buffer
	h := Wrap(inner, Options{AccessLog: &buf, TrustedProxyHeader: "X-Real-IP"})
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.2:4000"
	req.Header.Set("X-Real-IP", "2001:db8:1:2:3:4:5:6")
	serve(t, h, req)
	if leaked {
		t.Error("the trusted header reached the handler")
	}
	if seen != "2001:db8:1:2::/64" {
		t.Fatalf("NetworkKey = %q", seen)
	}
	if strings.Contains(buf.String(), "2001") || strings.Contains(buf.String(), "10.0.0.2") || !strings.HasPrefix(buf.String(), "- ") {
		t.Fatalf("log has an address: %q", buf.String())
	}

	// A client-supplied entry ahead of the proxy's own doesn't count.
	req = httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Real-IP", "192.0.2.66, 198.51.100.5")
	serve(t, h, req)
	if seen != "198.51.100.5" {
		t.Fatalf("list: NetworkKey = %q", seen)
	}

	buf.Reset()
	h = Wrap(inner, Options{AccessLog: &buf, LogClientIPs: true})
	req = httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "198.51.100.7:4000"
	req.Header.Set("X-Real-IP", "192.0.2.1") // not trusted: ignored
	serve(t, h, req)
	if !strings.HasPrefix(buf.String(), "198.51.100.7 - - [") || seen != "198.51.100.7" {
		t.Fatalf("opt-in log %q, key %q", buf.String(), seen)
	}
}

func TestCompressAndSkip(t *testing.T) {
	body := strings.Repeat("compressible ", 200)
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) })
	h := Wrap(inner, Options{NoCompress: func(r *http.Request) bool { return strings.HasPrefix(r.URL.Path, "/admin") }})
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := serve(t, h, req)
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("not compressed: %v", rec.Header())
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(zr); string(b) != body {
		t.Fatal("body differs")
	}
	req = httptest.NewRequest("GET", "/admin/x", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	if rec := serve(t, h, req); rec.Header().Get("Content-Encoding") != "" {
		t.Fatal("admin response was compressed")
	}
}

func TestRedactAddrs(t *testing.T) {
	cases := map[string]string{
		"http: TLS handshake error from 203.0.113.9:51234: EOF":              "http: TLS handshake error from -: EOF",
		"http: TLS handshake error from [2001:db8::1]:443: remote error":     "http: TLS handshake error from -: remote error",
		"accept from 2001:db8::7 failed":                                     "accept from - failed",
		"at 15:04:05 nothing happened":                                       "at 15:04:05 nothing happened",
		"http2: server: error reading preface from client 192.0.2.44:1: bad": "http2: server: error reading preface from client -: bad",
	}
	for in, want := range cases {
		if got := RedactAddrs(in); got != want {
			t.Errorf("RedactAddrs(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestServerErrorLogRedacts(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, nil))
	hs := NewServer(http.NotFoundHandler(), DefaultTimeouts(), l, false)
	if hs.ReadHeaderTimeout == 0 || hs.ReadTimeout == 0 || hs.WriteTimeout == 0 || hs.IdleTimeout == 0 {
		t.Fatalf("timeouts unset: %+v", hs)
	}
	hs.ErrorLog.Printf("http: TLS handshake error from %s: %s", "198.51.100.23:9999", "EOF")
	if strings.Contains(buf.String(), "198.51.100.23") || !strings.Contains(buf.String(), "from -") {
		t.Fatalf("error log: %q", buf.String())
	}
	buf.Reset()
	hs = NewServer(http.NotFoundHandler(), DefaultTimeouts(), l, true)
	hs.ErrorLog.Printf("http: TLS handshake error from %s: %s", "198.51.100.23:9999", "EOF")
	if !strings.Contains(buf.String(), "198.51.100.23") {
		t.Fatalf("opt-in error log: %q", buf.String())
	}
}

func TestRedirectHTTPS(t *testing.T) {
	h := RedirectHTTPS("openarcanum.example")
	rec := serve(t, h, httptest.NewRequest("GET", "http://x/report/bug?a=1", nil))
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "https://openarcanum.example/report/bug?a=1" {
		t.Fatalf("%d %q", rec.Code, rec.Header().Get("Location"))
	}
	rec = serve(t, RedirectHTTPS(""), httptest.NewRequest("GET", "http://site.example:80/", nil))
	if rec.Header().Get("Location") != "https://site.example/" {
		t.Fatalf("%q", rec.Header().Get("Location"))
	}
}

func TestChainOrder(t *testing.T) {
	var order []string
	mk := func(n string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { order = append(order, n); next.ServeHTTP(w, r) })
		}
	}
	serve(t, Chain(http.NotFoundHandler(), mk("a"), mk("b")), httptest.NewRequest("GET", "/", nil))
	if strings.Join(order, "") != "ab" {
		t.Fatal(order)
	}
}
