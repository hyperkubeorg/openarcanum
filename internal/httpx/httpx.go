// Package httpx is the HTTP stack every public HTTP(S) server here shares:
// gorilla/mux routing, gorilla/handlers compression, and an access log in
// Apache combined format (gorilla/handlers CustomLoggingHandler) that keeps
// client addresses and secrets out of the logs.
//
// The privacy rules it enforces (docs/operations.md "Website" and "Logs"):
//
//   - The client address is written as "-" unless the node opts in with
//     log_client_ips. It is never forwarded anywhere: nothing here adds
//     X-Forwarded-For or any other header to anything.
//   - Behind a reverse proxy, a configured header (TrustedProxyHeader) may
//     name the client for rate limiting only (ClientIP); it is never logged
//     unless log_client_ips is on.
//   - Query strings are never logged, and a Redact hook replaces
//     token-bearing path segments before the path is written.
//   - The http.Server's own error log (TLS handshake errors and the like,
//     which name the peer) goes through the same address redaction.
//
// A server is built in three steps:
//
//	r := httpx.NewRouter()                // gorilla/mux
//	r.HandleFunc("/", home).Methods(http.MethodGet)
//	h := httpx.Wrap(r, httpx.Options{AccessLog: w, Redact: redactTokens})
//	hs := httpx.NewServer(h, httpx.DefaultTimeouts(), log, false)
//	hs.Serve(ln)                          // ln from ops.ListenPublic
//
// Wrap is Chain(h, ClientAddr, AccessLog, Compress) plus the options; nodes
// that need more middleware add it with Chain.
package httpx

import (
	"bytes"
	"context"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"

	"github.com/hyperkubeorg/openarcanum/internal/ops"
)

// Middleware wraps a handler.
type Middleware func(http.Handler) http.Handler

// Chain applies middleware so the first one listed is the outermost: it sees
// the request first and the response last.
func Chain(h http.Handler, mw ...Middleware) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

// NewRouter is a gorilla/mux router with the settings every server uses:
// paths are matched as sent (no cleaning redirects that would echo a
// request's path back in a Location header) and a trailing slash is not
// a different route.
func NewRouter() *mux.Router {
	r := mux.NewRouter()
	r.StrictSlash(false)
	r.SkipClean(true)
	return r
}

// Options configure Wrap.
type Options struct {
	// AccessLog receives one Apache combined-format line per request. nil
	// turns the access log off.
	AccessLog io.Writer
	// LogClientIPs writes the client address into the access log instead
	// of "-" (the node's log_client_ips; off by default).
	LogClientIPs bool
	// TrustedProxyHeader, when set, is the header a trusted reverse proxy
	// puts the client's address in ("X-Real-IP"). ClientIP returns it for
	// rate limiting; the TCP peer is used when the header is missing or
	// isn't an IP address. Of a comma-separated list, only the last entry
	// (the one the trusted proxy added) counts. Leave it empty when
	// clients connect directly: anyone can send the header.
	TrustedProxyHeader string
	// Redact rewrites a request path before it is logged, replacing
	// segments that carry tokens or ids ("/v1/blob/<token>" →
	// "/v1/blob/-"). The query string is never logged either way.
	Redact func(path string) string
	// NoCompress exempts responses from compression. Use it for responses
	// that mix secrets (CSRF tokens) with attacker-influenced text, which
	// compression would expose to length-based attacks (BREACH).
	NoCompress func(r *http.Request) bool
}

// Wrap puts the standard middleware around h: the client address for rate
// limiting, the access log and compression.
func Wrap(h http.Handler, o Options) http.Handler {
	mw := []Middleware{ClientAddr(o.TrustedProxyHeader)}
	if o.AccessLog != nil {
		mw = append(mw, AccessLog(o.AccessLog, o.LogClientIPs, o.Redact))
	}
	mw = append(mw, Compress(o.NoCompress))
	return Chain(h, mw...)
}

// ---------------------------------------------------------------- client address

type ctxKey int

const clientIPKey ctxKey = 0

// ClientAddr records the client's address in the request context, for
// ClientIP: the TCP peer, or the trusted proxy header's value when one is
// configured and holds an IP address. The header itself is removed from
// the request so no handler can pass it on.
func ClientAddr(trustedHeader string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			a := peerAddr(r)
			if trustedHeader != "" {
				v := r.Header.Get(trustedHeader)
				// A proxy that appends to a list (X-Forwarded-For) adds
				// the address it saw last; entries before it are whatever
				// the client sent, so only the last one is trusted.
				if i := strings.LastIndexByte(v, ','); i >= 0 {
					v = v[i+1:]
				}
				if p, err := netip.ParseAddr(strings.TrimSpace(v)); err == nil {
					a = p.Unmap()
				}
				r.Header.Del(trustedHeader)
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientIPKey, a)))
		})
	}
}

func peerAddr(r *http.Request) netip.Addr {
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		return ap.Addr().Unmap()
	}
	a, _ := netip.ParseAddr(ops.RemoteIP(r))
	return a.Unmap()
}

// ClientIP is the client's address as ClientAddr found it (the TCP peer
// when the middleware isn't installed). Use it for rate limiting only;
// never log or forward it.
func ClientIP(r *http.Request) netip.Addr {
	if a, ok := r.Context().Value(clientIPKey).(netip.Addr); ok {
		return a
	}
	return peerAddr(r)
}

// NetworkKey is the rate-limit key for a client: its IPv4 address, or the
// /64 of an IPv6 address (ops.NetworkKey).
func NetworkKey(r *http.Request) string {
	a := ClientIP(r)
	if !a.IsValid() {
		return "unknown"
	}
	return ops.NetworkKey(a.String())
}

// ---------------------------------------------------------------- access log

// AccessLog writes one Apache combined-format line per request to out
// through gorilla/handlers' CustomLoggingHandler:
//
//	HOST - - [02/Jan/2006:15:04:05 -0700] "GET /path HTTP/1.1" 200 1234 "REFERER" "UA"
//
// HOST is "-", or the client address with logClientIPs; the path
// has no query string ("?-" marks that there was one) and goes through
// redact; the Referer is cut to its origin and path the same way.
func AccessLog(out io.Writer, logClientIPs bool, redact func(string) string) Middleware {
	// Requests finish concurrently; each line is one locked Write.
	out = &lockedWriter{w: out}
	return func(next http.Handler) http.Handler {
		return handlers.CustomLoggingHandler(out, next, CombinedFormatter(logClientIPs, redact))
	}
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// CombinedFormatter is the LogFormatter AccessLog uses.
func CombinedFormatter(logClientIPs bool, redact func(string) string) handlers.LogFormatter {
	return func(w io.Writer, p handlers.LogFormatterParams) {
		host := "-"
		if logClientIPs {
			if a := ClientIP(p.Request); a.IsValid() {
				host = a.String()
			}
		}
		path := p.URL.EscapedPath()
		if p.Request.Method == http.MethodConnect || path == "" {
			path = "/"
		}
		if redact != nil {
			path = redact(path)
		}
		if p.URL.RawQuery != "" || p.URL.ForceQuery {
			path += "?-"
		}
		referer := "-"
		if ref := p.Request.Referer(); ref != "" {
			referer = RedactURL(ref, redact)
		}
		ua := p.Request.UserAgent()
		if ua == "" {
			ua = "-"
		}
		buf := make([]byte, 0, 160)
		buf = append(buf, host...)
		buf = append(buf, " - - ["...)
		buf = p.TimeStamp.AppendFormat(buf, "02/Jan/2006:15:04:05 -0700")
		buf = append(buf, "] "...)
		buf = strconv.AppendQuote(buf, p.Request.Method+" "+path+" "+p.Request.Proto)
		buf = append(buf, ' ')
		buf = strconv.AppendInt(buf, int64(p.StatusCode), 10)
		buf = append(buf, ' ')
		buf = strconv.AppendInt(buf, int64(p.Size), 10)
		buf = append(buf, ' ')
		buf = strconv.AppendQuote(buf, referer)
		buf = append(buf, ' ')
		buf = strconv.AppendQuote(buf, ua)
		buf = append(buf, '\n')
		_, _ = w.Write(buf)
	}
}

// RedactURL is a URL for the log: scheme, host and redacted path, no user
// info, query or fragment. Anything that doesn't parse is "-".
func RedactURL(raw string, redact func(string) string) string {
	i := strings.Index(raw, "://")
	if i <= 0 {
		return "-"
	}
	rest := raw[i+3:]
	if j := strings.IndexAny(rest, "?#"); j >= 0 {
		rest = rest[:j]
	}
	host, path := rest, "/"
	if j := strings.IndexByte(rest, '/'); j >= 0 {
		host, path = rest[:j], rest[j:]
	}
	if at := strings.LastIndexByte(host, '@'); at >= 0 {
		host = host[at+1:]
	}
	if redact != nil {
		path = redact(path)
	}
	return raw[:i] + "://" + host + path
}

// ---------------------------------------------------------------- compression

// Compress is gorilla/handlers' CompressHandler (gzip or deflate, by the
// client's Accept-Encoding), skipped for requests skip matches.
func Compress(skip func(*http.Request) bool) Middleware {
	return func(next http.Handler) http.Handler {
		c := handlers.CompressHandler(next)
		if skip == nil {
			return c
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if skip(r) {
				next.ServeHTTP(w, r)
				return
			}
			c.ServeHTTP(w, r)
		})
	}
}

// ---------------------------------------------------------------- server

// Timeouts bound a client's use of a connection.
type Timeouts struct {
	// ReadHeader is how long a client has to send the request headers
	// (slowloris).
	ReadHeader time.Duration
	// Read is how long it has for the whole request, body included.
	Read time.Duration
	// Write is how long the handler and the response may take.
	Write time.Duration
	// Idle is how long a keep-alive connection waits for the next request.
	Idle time.Duration
}

// DefaultTimeouts suit pages and form posts. Servers with long-lived
// responses (WebSockets, large uploads) set their own.
func DefaultTimeouts() Timeouts {
	return Timeouts{ReadHeader: 10 * time.Second, Read: 30 * time.Second, Write: 60 * time.Second, Idle: 120 * time.Second}
}

// NewServer is an http.Server for h with the timeouts set and the server's
// error log routed through ErrorLog (client addresses redacted unless
// logClientIPs). MaxHeaderBytes is 64 KiB.
func NewServer(h http.Handler, t Timeouts, l *slog.Logger, logClientIPs bool) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: t.ReadHeader,
		ReadTimeout:       t.Read,
		WriteTimeout:      t.Write,
		IdleTimeout:       t.Idle,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          ErrorLog(l, logClientIPs),
	}
}

// ErrorLog is a *log.Logger for http.Server.ErrorLog: each line becomes a
// warning on l with IP addresses (and their ports) replaced by "-", unless
// logClientIPs. Go's server writes lines such as
// "http: TLS handshake error from 203.0.113.9:51234: EOF".
func ErrorLog(l *slog.Logger, logClientIPs bool) *log.Logger {
	if l == nil {
		l = slog.New(slog.DiscardHandler)
	}
	return log.New(&errorWriter{log: l, keepIPs: logClientIPs}, "", 0)
}

type errorWriter struct {
	log     *slog.Logger
	keepIPs bool
}

func (e *errorWriter) Write(p []byte) (int, error) {
	msg := string(bytes.TrimRight(p, "\n"))
	if !e.keepIPs {
		msg = RedactAddrs(msg)
	}
	e.log.Warn("http server", "msg", msg)
	return len(p), nil
}

// Candidates for addresses, each checked by parsing: IPv6 in brackets
// (with a port: Go's form for IPv6 peers), an IPv4 dotted quad (with a
// port), and bare IPv6.
var addrRE = regexp.MustCompile(`\[[0-9A-Za-z:.%]+\](?::\d{1,5})?|(?:\d{1,3}\.){3}\d{1,3}(?::\d{1,5})?|[0-9A-Fa-f]{0,4}(?::[0-9A-Fa-f]{0,4}){2,7}`)

// RedactAddrs replaces every IP address (with its port) in s by "-".
func RedactAddrs(s string) string {
	return addrRE.ReplaceAllStringFunc(s, func(m string) string {
		if isAddr(m) {
			return "-"
		}
		return m
	})
}

func isAddr(m string) bool {
	if _, err := netip.ParseAddrPort(m); err == nil {
		return true
	}
	if host, _, err := net.SplitHostPort(m); err == nil {
		m = host
	}
	_, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(m, "["), "]"))
	return err == nil
}

// ---------------------------------------------------------------- redirect

// RedirectHTTPS answers every request with a permanent redirect to the same
// path on https://host (host as given, or the request's Host when empty),
// without the query string's secrets being logged by anything here. Use it
// on a plain-HTTP listener (:80) next to a TLS one.
func RedirectHTTPS(host string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := host
		if h == "" {
			h = r.Host
			if hh, _, err := net.SplitHostPort(h); err == nil {
				h = hh
				if strings.Contains(h, ":") {
					h = "[" + h + "]"
				}
			}
		}
		if h == "" || strings.ContainsAny(h, "/\\@ \t\r\n") {
			http.Error(w, "bad host", http.StatusBadRequest)
			return
		}
		target := "https://" + h + r.URL.EscapedPath()
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		w.Header().Set("Connection", "close")
		http.Redirect(w, r, target, http.StatusMovedPermanently)
	})
}
