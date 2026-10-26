package meta

import (
	"context"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// NewHTTPServer is the http.Server for the node's API: bounded header,
// body and idle times (a client that trickles a request can't hold a
// connection forever), and the server's own error log (TLS handshake
// failures, panics, which net/http logs with the client's address) routed
// to log at debug level with every IP address removed: the node never
// records client addresses (docs/threat-model.md).
func NewHTTPServer(h http.Handler, logger *slog.Logger) *http.Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		// The largest request is a blob chunk or a mailbox drop of a few
		// MiB; the largest answer a mailbox page of 24 MiB.
		ReadTimeout:    2 * time.Minute,
		WriteTimeout:   5 * time.Minute,
		IdleTimeout:    2 * time.Minute,
		MaxHeaderBytes: 64 << 10,
		ErrorLog:       log.New(redactingWriter{logger}, "", 0),
	}
}

// redactingWriter logs net/http's error log lines without IP addresses.
type redactingWriter struct{ log *slog.Logger }

func (w redactingWriter) Write(p []byte) (int, error) {
	w.log.LogAttrs(context.Background(), slog.LevelDebug, "http server", slog.String("err", RedactAddrs(strings.TrimSpace(string(p)))))
	return len(p), nil
}

// RedactAddrs replaces every IP address (with or without a port) in s.
func RedactAddrs(s string) string {
	fields := strings.Fields(s)
	for i, f := range fields {
		core := strings.TrimRight(f, ":,;.)")
		core = strings.TrimLeft(core, "(")
		if isAddr(core) {
			fields[i] = strings.Replace(f, core, "[address]", 1)
		}
	}
	return strings.Join(fields, " ")
}

func isAddr(s string) bool {
	if _, err := netip.ParseAddr(strings.Trim(s, "[]")); err == nil {
		return true
	}
	host, _, err := net.SplitHostPort(s)
	if err != nil {
		return false
	}
	_, err = netip.ParseAddr(host)
	return err == nil
}
