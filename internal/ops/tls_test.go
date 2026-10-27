package ops

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestTLSSettingsValidate(t *testing.T) {
	ok := []TLSSettings{
		DefaultTLSSettings(),
		{TLSMode: TLSACME, TLSDomains: []string{"relay.example.invalid", "203.0.113.7", "2001:db8::1"}, ACMEDirectoryURL: LetsEncryptDirectory, ACMEEmail: "ops@example.invalid"},
		{TLSMode: TLSACME, TLSDomains: []string{"Relay.Example.Invalid."}, ACMEDirectoryURL: "http://127.0.0.1:14000/dir"},
		{TLSMode: TLSFiles, TLSCertFile: "/c", TLSKeyFile: "/k"},
		{TLSMode: TLSOff, TLSDomains: []string{"relay.example.invalid"}},
	}
	for _, s := range ok {
		if err := s.Validate(); err != nil {
			t.Errorf("%+v: %v", s, err)
		}
	}
	acme := func(f func(*TLSSettings)) TLSSettings {
		s := TLSSettings{TLSMode: TLSACME, TLSDomains: []string{"relay.example.invalid"}, ACMEDirectoryURL: LetsEncryptDirectory}
		f(&s)
		return s
	}
	bad := map[string]TLSSettings{
		"tls_mode":           {TLSMode: "on"},
		"needs tls_domains":  acme(func(s *TLSSettings) { s.TLSDomains = nil }),
		"wildcard":           acme(func(s *TLSSettings) { s.TLSDomains = []string{"*.example.invalid"} }),
		"single label":       acme(func(s *TLSSettings) { s.TLSDomains = []string{"localhost"} }),
		"zone":               acme(func(s *TLSSettings) { s.TLSDomains = []string{"fe80::1%eth0"} }),
		"url in domains":     acme(func(s *TLSSettings) { s.TLSDomains = []string{"https://relay.example.invalid"} }),
		"plain http CA":      acme(func(s *TLSSettings) { s.ACMEDirectoryURL = "http://ca.example.invalid/dir" }),
		"acme_email":         acme(func(s *TLSSettings) { s.ACMEEmail = "Ops <ops@example.invalid>" }),
		"files without key":  {TLSMode: TLSFiles, TLSCertFile: "/c"},
		"files outside mode": acme(func(s *TLSSettings) { s.TLSCertFile, s.TLSKeyFile = "/c", "/k" }),
	}
	for name, s := range bad {
		if err := s.Validate(); err == nil {
			t.Errorf("%s: accepted %+v", name, s)
		}
	}
	s := TLSSettings{TLSDomains: []string{"2001:DB8::1", "relay.example.invalid"}}
	if h := s.PublicHost(); h != "[2001:db8::1]" {
		t.Fatalf("PublicHost = %q", h)
	}
}

func TestArpaName(t *testing.T) {
	for in, want := range map[string]string{
		"192.0.2.10":         "10.2.0.192.in-addr.arpa",
		"::ffff:192.0.2.1":   "1.2.0.192.in-addr.arpa",
		"2001:db8::567:89ab": "b.a.9.8.7.6.5.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.ip6.arpa",
	} {
		if got := arpaName(netip.MustParseAddr(in)); got != want {
			t.Errorf("arpaName(%s) = %s, want %s", in, got, want)
		}
	}
}

// serveTLS runs an HTTPS server answering "ok" on a listener from
// ListenPublic.
func serveTLS(t *testing.T, ctx context.Context, s TLSSettings, o TLSOptions) string {
	t.Helper()
	ln, err := ListenPublic(ctx, "127.0.0.1:0", s, o)
	if err != nil {
		t.Fatal(err)
	}
	return serveOn(t, ln)
}

// serveACME is serveTLS with ca checking challenges on the listener, as
// ListenPublic does it (listen first, then start the issuers). The issuers
// are stopped (and waited for) with stop, at the latest at cleanup: they
// write to the cache directory, which the test's TempDir removes last.
func serveACME(t *testing.T, ctx context.Context, ca *testCA, s TLSSettings, o TLSOptions) (addr string, stop func()) {
	t.Helper()
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ca.mu.Lock()
	ca.Target = raw.Addr().String()
	ca.mu.Unlock()
	tc, stop, err := ServerTLS(ctx, s, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return serveOn(t, tls.NewListener(raw, tc)), stop
}

func serveOn(t *testing.T, ln net.Listener) string {
	hs := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok\n") })}
	go func() { _ = hs.Serve(ln) }()
	t.Cleanup(func() { _ = hs.Close() })
	return ln.Addr().String()
}

// handshake connects to addr as a browser would for serverName ("" for an
// IP address: no SNI), verifying against roots, and returns the leaf.
func handshake(addr, serverName string, roots *x509.CertPool) (*x509.Certificate, error) {
	cfg := &tls.Config{RootCAs: roots, ServerName: serverName, NextProtos: []string{"http/1.1"}}
	if serverName == "" {
		host, _, _ := net.SplitHostPort(addr)
		cfg.ServerName = host // verified as an IP SAN; Go sends no SNI for it
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", addr, cfg)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return conn.ConnectionState().PeerCertificates[0], nil
}

func waitHandshake(t *testing.T, addr, serverName string, roots *x509.CertPool, accept func(*x509.Certificate) bool) *x509.Certificate {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		leaf, err := handshake(addr, serverName, roots)
		if err == nil && (accept == nil || accept(leaf)) {
			return leaf
		}
		if time.Now().After(deadline) {
			t.Fatalf("no acceptable certificate for %q at %s: %v", serverName, addr, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A node with tls_mode acme gets one certificate for its names and one,
// under the short-lived profile, for its IP address, answering tls-alpn-01
// on its own listener; keeps them in a private cache; and serves them
// again after a restart without asking the CA.
func TestACMECertificatesForNamesAndIPs(t *testing.T) {
	ca := newTestCA(t)
	ca.Profiles["shortlived"] = 160 * time.Hour
	ca.Profiles["classic"] = 90 * 24 * time.Hour
	cache := filepath.Join(t.TempDir(), "state", "acme")
	s := TLSSettings{
		TLSMode: TLSACME, TLSDomains: []string{"relay.example.invalid", "127.0.0.1"},
		ACMEDirectoryURL: ca.URL(), ACMEEmail: "ops@example.invalid",
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr, stop := serveACME(t, ctx, ca, s, TLSOptions{CacheDir: cache})

	byName := waitHandshake(t, addr, "relay.example.invalid", ca.Roots(), nil)
	byIP := waitHandshake(t, addr, "", ca.Roots(), nil)
	stop() // the issuers finish caching what they got
	if !slices.Equal(byName.DNSNames, []string{"relay.example.invalid"}) || len(byName.IPAddresses) != 0 {
		t.Fatalf("name certificate: %v %v", byName.DNSNames, byName.IPAddresses)
	}
	if len(byIP.IPAddresses) != 1 || !byIP.IPAddresses[0].Equal(net.ParseIP("127.0.0.1")) || len(byIP.DNSNames) != 0 {
		t.Fatalf("IP certificate: %v %v", byIP.DNSNames, byIP.IPAddresses)
	}
	if life := byIP.NotAfter.Sub(byIP.NotBefore); life > 7*24*time.Hour {
		t.Fatalf("IP certificate lives %v: not the short-lived profile", life)
	}
	ca.mu.Lock()
	ordered, checked := slices.Clone(ca.Ordered), slices.Clone(ca.Checked)
	ca.mu.Unlock()
	slices.Sort(ordered)
	if !slices.Equal(ordered, []string{"dns:relay.example.invalid profile=", "ip:127.0.0.1 profile=shortlived"}) {
		t.Fatalf("orders: %v", ordered)
	}
	if !slices.Contains(checked, "1.0.0.127.in-addr.arpa") || !slices.Contains(checked, "relay.example.invalid") {
		t.Fatalf("challenges checked for %v", checked)
	}

	// The cache: private directory, private files, keys never anywhere else.
	st, err := os.Stat(cache)
	if err != nil || (posixModes && st.Mode().Perm() != 0o700) {
		t.Fatalf("cache dir: %v %v", st.Mode(), err)
	}
	entries, _ := os.ReadDir(cache)
	if len(entries) != 3 { // account key, names, IPs
		t.Fatalf("cache holds %v", entries)
	}
	for _, e := range entries {
		info, _ := e.Info()
		if posixModes && info.Mode().Perm() != 0o600 {
			t.Fatalf("%s has mode %v", e.Name(), info.Mode())
		}
	}

	// A restart serves the cached certificates at once, with no new order.
	addr2 := serveTLS(t, ctx, s, TLSOptions{CacheDir: cache})
	again := waitHandshake(t, addr2, "relay.example.invalid", ca.Roots(), nil)
	if !again.Equal(byName) {
		t.Fatal("restart got a new certificate instead of the cached one")
	}
	ca.mu.Lock()
	defer ca.mu.Unlock()
	if len(ca.Ordered) != 2 {
		t.Fatalf("restart ordered again: %v", ca.Ordered)
	}
}

// Short-lived certificates are renewed before they expire.
func TestACMERenewsShortLivedCertificates(t *testing.T) {
	ca := newTestCA(t)
	ca.Profiles["shortlived"] = 3 * time.Second
	s := TLSSettings{TLSMode: TLSACME, TLSDomains: []string{"127.0.0.1"}, ACMEDirectoryURL: ca.URL()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr, _ := serveACME(t, ctx, ca, s, TLSOptions{CacheDir: t.TempDir()})
	first := waitHandshake(t, addr, "", ca.Roots(), nil)
	next := waitHandshake(t, addr, "", ca.Roots(), func(c *x509.Certificate) bool { return c.SerialNumber.Cmp(first.SerialNumber) != 0 })
	if !next.NotAfter.After(first.NotAfter) {
		t.Fatalf("renewed certificate ends %v, the first %v", next.NotAfter, first.NotAfter)
	}
}

// A CA without profiles still issues for IP addresses (no profile asked).
func TestACMEWithoutProfiles(t *testing.T) {
	ca := newTestCA(t)
	s := TLSSettings{TLSMode: TLSACME, TLSDomains: []string{"127.0.0.1"}, ACMEDirectoryURL: ca.URL()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr, _ := serveACME(t, ctx, ca, s, TLSOptions{CacheDir: t.TempDir()})
	waitHandshake(t, addr, "", ca.Roots(), nil)
	ca.mu.Lock()
	defer ca.mu.Unlock()
	if !slices.Equal(ca.Ordered, []string{"ip:127.0.0.1 profile="}) {
		t.Fatalf("orders: %v", ca.Ordered)
	}
}

func TestACMECacheDirIsMadePrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "acme")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s := TLSSettings{TLSMode: TLSACME, TLSDomains: []string{"relay.example.invalid"}, ACMEDirectoryURL: "http://127.0.0.1:1/dir"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, stop, err := ServerTLS(ctx, s, TLSOptions{CacheDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer stop() // before TempDir goes: the issuer writes the account key there
	if st, _ := os.Stat(dir); posixModes && st.Mode().Perm() != 0o700 {
		t.Fatalf("mode %v", st.Mode())
	}
	if _, _, err := ServerTLS(ctx, s, TLSOptions{}); err == nil || !strings.Contains(err.Error(), "acme_cache_dir") {
		t.Fatalf("no cache dir: %v", err)
	}
	if tc, _, err := ServerTLS(ctx, DefaultTLSSettings(), TLSOptions{}); tc != nil || err != nil {
		t.Fatalf("off: %v %v", tc, err)
	}
}

// writeCert writes a self-signed certificate for 127.0.0.1 and its key.
func writeCert(t *testing.T, certFile, keyFile string) *x509.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kder, _ := x509.MarshalECPrivateKey(key)
	_ = os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	_ = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), 0o600)
	c, _ := x509.ParseCertificate(der)
	return c
}

// tls_mode files serves the files' certificate and picks up replaced files;
// a half-written pair keeps the old certificate in service.
func TestTLSFilesReload(t *testing.T) {
	old := fileCertCheck
	fileCertCheck = 0
	t.Cleanup(func() { fileCertCheck = old })
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	first := writeCert(t, certFile, keyFile)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := serveTLS(t, ctx, TLSSettings{TLSMode: TLSFiles, TLSCertFile: certFile, TLSKeyFile: keyFile}, TLSOptions{})
	pool := func(c *x509.Certificate) *x509.CertPool { p := x509.NewCertPool(); p.AddCert(c); return p }
	if got := waitHandshake(t, addr, "", pool(first), nil); !got.Equal(first) {
		t.Fatal("not the files' certificate")
	}
	// Only the certificate replaced: it doesn't match the key, keep the old.
	keep, _ := os.ReadFile(keyFile)
	second := writeCert(t, certFile, keyFile)
	_ = os.WriteFile(keyFile, keep, 0o600)
	future := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(certFile, future, future)
	if got := waitHandshake(t, addr, "", pool(first), nil); !got.Equal(first) {
		t.Fatal("a mismatched pair replaced the certificate")
	}
	third := writeCert(t, certFile, keyFile)
	later := time.Now().Add(4 * time.Second)
	_ = os.Chtimes(certFile, later, later)
	_ = os.Chtimes(keyFile, later, later)
	if got := waitHandshake(t, addr, "", pool(third), nil); !got.Equal(third) || got.Equal(second) {
		t.Fatal("replaced files not picked up")
	}
}

// The images' fixed health check (http://127.0.0.1:PORT/healthz) keeps
// working when the node serves TLS on that port.
func TestHealthcheckFollowsTLS(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	writeCert(t, certFile, keyFile)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := serveTLS(t, ctx, TLSSettings{TLSMode: TLSFiles, TLSCertFile: certFile, TLSKeyFile: keyFile}, TLSOptions{})
	var out, errOut bytes.Buffer
	if rc := Healthcheck([]string{"http://" + addr + "/healthz"}, &out, &errOut); rc != 0 || out.String() != "200 ok\n" {
		t.Fatalf("rc=%d out=%q err=%q", rc, out.String(), errOut.String())
	}
	out.Reset()
	if rc := Healthcheck([]string{"https://" + addr + "/healthz"}, &out, &errOut); rc != 0 {
		t.Fatalf("https: rc=%d err=%q", rc, errOut.String())
	}
}
