package ops

// TLS on a node's public listener (docs/operations.md "TLS and browsers").
//
// tls_mode acme gets certificates from an ACME CA (Let's Encrypt by
// default) and renews them (acme.go), proving control with the tls-alpn-01
// challenge on the listener itself, so the node needs no second port. One
// certificate covers the node's DNS names and another its IP addresses,
// ordered with the CA's short-lived profile when it offers one (Let's
// Encrypt issues IP address certificates only that way). It uses
// golang.org/x/crypto/acme directly: autocert handles neither IP addresses
// nor profiles. tls_mode files serves a certificate from PEM files and picks
// up replaced files (cert-manager, certbot) without a restart.

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"runtime"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
)

// TLSOptions are what a node adds to its TLSSettings.
type TLSOptions struct {
	// CacheDir is where acme keeps its account key and certificates: the
	// node's acme_cache_dir, or its default.
	CacheDir string
	// HTTP2 offers h2 by ALPN (the maintainer node's HTTP API). The relay
	// leaves it off: browsers open WebSockets over HTTP/1.1.
	HTTP2 bool
	Log   *slog.Logger
}

// ListenPublic listens on addr and, unless tls_mode is off, serves TLS
// there. With acme, certificates are requested in the background (the
// listener is already up, so the CA's challenge connections reach it) until
// ctx ends; handshakes before the first certificate arrives fail.
func ListenPublic(ctx context.Context, addr string, s TLSSettings, o TLSOptions) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	tc, err := ServerTLS(ctx, s, o)
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	if tc == nil {
		return ln, nil
	}
	return tls.NewListener(ln, tc), nil
}

// ServerTLS is the public listener's TLS configuration, or nil when
// tls_mode is off. See ListenPublic.
func ServerTLS(ctx context.Context, s TLSSettings, o TLSOptions) (*tls.Config, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	protos := []string{"http/1.1"}
	if o.HTTP2 {
		protos = []string{"h2", "http/1.1"}
	}
	src := &certSource{}
	switch s.TLSMode {
	case TLSOff:
		return nil, nil
	case TLSFiles:
		fc, err := newFileCert(s.TLSCertFile, s.TLSKeyFile, o.Log)
		if err != nil {
			return nil, err
		}
		src.files = fc
	case TLSACME:
		if o.CacheDir == "" {
			return nil, errors.New("acme_cache_dir is required with tls_mode acme")
		}
		if err := privateDir(o.CacheDir); err != nil {
			return nil, fmt.Errorf("acme_cache_dir: %w", err)
		}
		var names, ips []string
		for _, d := range s.TLSDomains {
			n, _ := tlsName(d) // checked by Validate
			if _, err := netip.ParseAddr(n); err == nil {
				ips = append(ips, n)
			} else {
				names = append(names, n)
			}
		}
		src.acme = newACMECerts(s.ACMEDirectoryURL, s.ACMEEmail, o.CacheDir, o.Log)
		if len(names) > 0 {
			src.names = newIssuer(src.acme, names, false)
			go src.names.run(ctx)
		}
		if len(ips) > 0 {
			src.ips = newIssuer(src.acme, ips, true)
			go src.ips.run(ctx)
		}
		protos = append(protos, acme.ALPNProto)
	}
	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		NextProtos:     protos,
		GetCertificate: src.get,
	}, nil
}

// certSource picks the certificate for a handshake.
type certSource struct {
	files *fileCert
	acme  *acmeCerts
	names *issuer
	ips   *issuer
}

func (c *certSource) get(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if c.files != nil {
		return c.files.get()
	}
	if len(hello.SupportedProtos) == 1 && hello.SupportedProtos[0] == acme.ALPNProto {
		if cert := c.acme.challengeCert(hello.ServerName); cert != nil {
			return cert, nil
		}
		return nil, fmt.Errorf("no ACME challenge pending for %q", hello.ServerName)
	}
	// Clients send no server name when they dial an IP address.
	byIP := hello.ServerName == "" || net.ParseIP(hello.ServerName) != nil
	if c.ips != nil && (byIP || c.names == nil) {
		return c.ips.get()
	}
	return c.names.get()
}

// privateDir makes dir (and its parents) if needed and makes sure only the
// node's user can open it: it holds private keys.
func privateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	st, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	// Windows has no mode bits to check (Chmod only toggles read-only); a
	// directory there inherits its parent's ACL (private under the user's profile).
	if runtime.GOOS != "windows" && st.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("%s is open to other users (mode %o) and can't be made 0700: %w", dir, st.Mode().Perm(), err)
		}
	}
	return nil
}

// fileCert is a certificate and key from PEM files, read again when either
// file changes (checked at most every fileCertCheck).
type fileCert struct {
	certFile, keyFile string
	log               *slog.Logger

	mu        sync.Mutex
	cert      *tls.Certificate
	stamp     [2]time.Time
	checkedAt time.Time
}

var fileCertCheck = 10 * time.Second

func newFileCert(certFile, keyFile string, log *slog.Logger) (*fileCert, error) {
	f := &fileCert{certFile: certFile, keyFile: keyFile, log: log}
	stamp, err := f.stamps()
	if err != nil {
		return nil, err
	}
	if err := f.load(stamp); err != nil {
		return nil, err
	}
	return f, nil
}

func (f *fileCert) stamps() ([2]time.Time, error) {
	var out [2]time.Time
	for i, p := range []string{f.certFile, f.keyFile} {
		st, err := os.Stat(p)
		if err != nil {
			return out, err
		}
		out[i] = st.ModTime()
	}
	return out, nil
}

func (f *fileCert) load(stamp [2]time.Time) error {
	cert, err := tls.LoadX509KeyPair(f.certFile, f.keyFile)
	if err != nil {
		return fmt.Errorf("tls_cert_file / tls_key_file: %w", err)
	}
	f.cert, f.stamp, f.checkedAt = &cert, stamp, time.Now()
	return nil
}

func (f *fileCert) get() (*tls.Certificate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if time.Since(f.checkedAt) < fileCertCheck {
		return f.cert, nil
	}
	f.checkedAt = time.Now()
	stamp, err := f.stamps()
	if err != nil || stamp == f.stamp {
		return f.cert, nil
	}
	// A renewal tool may have written one file but not yet the other:
	// keep serving the old pair until the new one loads.
	if err := f.load(stamp); err != nil {
		f.log.Warn("tls certificate files changed but don't load; still serving the previous certificate", "err", err)
		return f.cert, nil
	}
	f.log.Info("tls certificate reloaded", "file", f.certFile)
	return f.cert, nil
}
