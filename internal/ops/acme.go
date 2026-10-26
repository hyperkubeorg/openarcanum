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
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
)

// shortLivedProfile is the ACME profile (draft-ietf-acme-profiles) under
// which Let's Encrypt issues certificates for IP addresses (about six days).
const shortLivedProfile = "shortlived"

// acmeCerts is a node's ACME account and its pending tls-alpn-01 answers,
// shared by its issuers (one for its DNS names, one for its IP addresses).
type acmeCerts struct {
	client *acme.Client
	email  string
	dir    string // acme_cache_dir
	log    *slog.Logger

	accountMu  sync.Mutex
	registered bool

	tokenMu sync.RWMutex
	tokens  map[string]*tls.Certificate // by the server name the CA sends
}

func newACMECerts(directoryURL, email, dir string, log *slog.Logger) *acmeCerts {
	return &acmeCerts{
		client: &acme.Client{DirectoryURL: directoryURL, UserAgent: "openarcanum"},
		email:  email, dir: dir, log: log,
		tokens: map[string]*tls.Certificate{},
	}
}

// challengeCert is the tls-alpn-01 answer for serverName, if one is pending.
func (a *acmeCerts) challengeCert(serverName string) *tls.Certificate {
	a.tokenMu.RLock()
	defer a.tokenMu.RUnlock()
	return a.tokens[strings.ToLower(strings.TrimSuffix(serverName, "."))]
}

// account loads or makes the account key (kept in the cache directory) and
// registers it with the CA, agreeing to its terms of service.
func (a *acmeCerts) account(ctx context.Context) error {
	a.accountMu.Lock()
	defer a.accountMu.Unlock()
	if a.client.Key == nil {
		const name = "acme_account.key"
		var key *ecdsa.PrivateKey
		if data, err := a.read(name); err == nil {
			if b, _ := pem.Decode(data); b != nil && b.Type == "EC PRIVATE KEY" {
				key, _ = x509.ParseECPrivateKey(b.Bytes)
			}
		}
		if key == nil {
			k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				return err
			}
			der, err := x509.MarshalECPrivateKey(k)
			if err != nil {
				return err
			}
			if err := a.write(name, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})); err != nil {
				return err
			}
			key = k
		}
		a.client.Key = key
	}
	if a.registered {
		return nil
	}
	acct := &acme.Account{}
	if a.email != "" {
		acct.Contact = []string{"mailto:" + a.email}
	}
	if _, err := a.client.Register(ctx, acct, acme.AcceptTOS); err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) {
		return fmt.Errorf("ACME account: %w", err)
	}
	a.registered = true
	return nil
}

func (a *acmeCerts) read(name string) ([]byte, error) {
	return os.ReadFile(filepath.Join(a.dir, name))
}

// write replaces a cache file atomically, readable by the node's user only.
func (a *acmeCerts) write(name string, data []byte) error {
	f, err := os.CreateTemp(a.dir, "."+name+".*")
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(f.Name(), filepath.Join(a.dir, name))
	}
	if werr != nil {
		_ = os.Remove(f.Name())
	}
	return werr
}

// issuer gets and renews one certificate covering a set of identifiers:
// all of the node's DNS names, or all of its IP addresses (RFC 8738). It
// renews once two thirds of the certificate's lifetime have passed (day 60
// of a 90-day certificate; with two days left of a six-day one) and keeps
// the certificate in the cache directory, so a restart doesn't ask the CA
// again.
type issuer struct {
	a   *acmeCerts
	ids []string // canonical (tlsName)
	ip  bool
	// profile is the issuance profile to ask for when the CA offers it.
	profile string

	mu   sync.RWMutex
	cert *tls.Certificate
}

func newIssuer(a *acmeCerts, ids []string, ip bool) *issuer {
	i := &issuer{a: a, ids: ids, ip: ip}
	if ip {
		i.profile = shortLivedProfile
	}
	return i
}

func (i *issuer) cacheName() string {
	kind := "names"
	if i.ip {
		kind = "ips"
	}
	return kind + "+" + strings.ReplaceAll(strings.Join(i.ids, "+"), ":", "_") + ".pem"
}

// get is the current certificate.
func (i *issuer) get() (*tls.Certificate, error) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if i.cert == nil {
		return nil, fmt.Errorf("the certificate for %s isn't issued yet", strings.Join(i.ids, ", "))
	}
	return i.cert, nil
}

// run keeps the certificate fresh until ctx ends, retrying failures after a
// minute, then twice as long each time up to an hour.
func (i *issuer) run(ctx context.Context) {
	i.loadCached()
	backoff := time.Minute
	for {
		next := i.untilRenewal()
		if next <= 0 {
			err := i.issue(ctx)
			if err == nil {
				backoff = time.Minute
				continue
			}
			if ctx.Err() != nil {
				return
			}
			i.a.log.Warn("tls certificate not obtained", "for", i.ids, "err", err, "retry_in", backoff.String())
			next, backoff = backoff, min(2*backoff, time.Hour)
		}
		// Wake at least hourly: timers don't advance while a machine sleeps.
		select {
		case <-ctx.Done():
			return
		case <-time.After(min(next, time.Hour)):
		}
	}
}

// untilRenewal is how long until the certificate is due (0 or less: now).
func (i *issuer) untilRenewal() time.Duration {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if i.cert == nil {
		return 0
	}
	l := i.cert.Leaf
	return time.Until(l.NotBefore.Add(l.NotAfter.Sub(l.NotBefore) * 2 / 3))
}

func (i *issuer) loadCached() {
	data, err := i.a.read(i.cacheName())
	if err != nil {
		return
	}
	cert, err := tls.X509KeyPair(data, data)
	if err != nil || cert.Leaf == nil || time.Now().After(cert.Leaf.NotAfter) || !i.covers(cert.Leaf) {
		return
	}
	i.mu.Lock()
	i.cert = &cert
	i.mu.Unlock()
	i.a.log.Info("tls certificate loaded", "for", i.ids, "not_after", cert.Leaf.NotAfter.UTC().Format(time.RFC3339))
}

func (i *issuer) covers(leaf *x509.Certificate) bool {
	for _, id := range i.ids {
		if leaf.VerifyHostname(id) != nil {
			return false
		}
	}
	return true
}

// issue orders a new certificate, answers the challenges and installs it.
func (i *issuer) issue(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	c := i.a.client
	if err := i.a.account(ctx); err != nil {
		return err
	}
	dir, err := c.Discover(ctx)
	if err != nil {
		return err
	}
	var opts []acme.OrderOption
	if _, ok := dir.Profiles[acme.ProfileName(i.profile)]; ok && i.profile != "" {
		opts = append(opts, acme.WithOrderIssuanceProfile(acme.ProfileName(i.profile)))
	}
	ids := acme.DomainIDs(i.ids...)
	if i.ip {
		ids = acme.IPIDs(i.ids...)
	}
	o, err := c.AuthorizeOrder(ctx, ids, opts...)
	if err != nil {
		return err
	}
	for _, u := range o.AuthzURLs {
		if err := i.authorize(ctx, u); err != nil {
			return err
		}
	}
	if o, err = c.WaitOrder(ctx, o.URI); err != nil {
		return err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	tmpl := &x509.CertificateRequest{}
	for _, id := range i.ids {
		if i.ip {
			tmpl.IPAddresses = append(tmpl.IPAddresses, net.ParseIP(id))
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, id)
		}
	}
	if !i.ip {
		tmpl.Subject.CommonName = i.ids[0]
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		return err
	}
	chain, _, err := c.CreateCertFromOrder(ctx, o, csr, true)
	if err != nil {
		return err
	}
	if len(chain) == 0 {
		return errors.New("the CA returned no certificate")
	}
	leaf, err := x509.ParseCertificate(chain[0])
	if err != nil {
		return err
	}
	if pub, ok := leaf.PublicKey.(*ecdsa.PublicKey); !ok || !pub.Equal(&key.PublicKey) {
		return errors.New("the CA's certificate is not for the requested key")
	}
	if !i.covers(leaf) || time.Now().After(leaf.NotAfter) {
		return errors.New("the CA's certificate doesn't cover the requested names or has expired")
	}
	cert := &tls.Certificate{Certificate: chain, PrivateKey: key, Leaf: leaf}
	i.mu.Lock()
	i.cert = cert
	i.mu.Unlock()
	i.a.log.Info("tls certificate issued", "for", i.ids, "not_after", leaf.NotAfter.UTC().Format(time.RFC3339))
	if err := i.a.write(i.cacheName(), encodeCert(cert)); err != nil {
		i.a.log.Warn("tls certificate not cached", "err", err)
	}
	return nil
}

// authorize answers one authorization's tls-alpn-01 challenge on the
// node's own listener.
func (i *issuer) authorize(ctx context.Context, u string) error {
	c := i.a.client
	z, err := c.GetAuthorization(ctx, u)
	if err != nil {
		return err
	}
	switch z.Status {
	case acme.StatusValid:
		return nil
	case acme.StatusPending:
	default:
		return fmt.Errorf("authorization for %s is %s", z.Identifier.Value, z.Status)
	}
	var chal *acme.Challenge
	for _, ch := range z.Challenges {
		if ch.Type == "tls-alpn-01" {
			chal = ch
		}
	}
	if chal == nil {
		return fmt.Errorf("the CA offers no tls-alpn-01 challenge for %s", z.Identifier.Value)
	}
	id := z.Identifier.Value
	name := strings.ToLower(id)
	if i.ip {
		addr, err := netip.ParseAddr(id)
		if err != nil {
			return fmt.Errorf("the CA asks about %q, not an IP address", id)
		}
		id, name = addr.String(), arpaName(addr)
	}
	cert, err := c.TLSALPN01ChallengeCert(chal.Token, id)
	if err != nil {
		return err
	}
	i.a.tokenMu.Lock()
	i.a.tokens[name] = &cert
	i.a.tokenMu.Unlock()
	defer func() {
		i.a.tokenMu.Lock()
		delete(i.a.tokens, name)
		i.a.tokenMu.Unlock()
	}()
	if _, err := c.Accept(ctx, chal); err != nil {
		return err
	}
	_, err = c.WaitAuthorization(ctx, z.URI)
	return err
}

// arpaName is the reverse-DNS name a CA sends as the server name when it
// checks a tls-alpn-01 challenge for an IP address (RFC 8738 §6).
func arpaName(a netip.Addr) string {
	a = a.Unmap()
	var b strings.Builder
	s := a.AsSlice()
	if a.Is4() {
		for j := len(s) - 1; j >= 0; j-- {
			fmt.Fprintf(&b, "%d.", s[j])
		}
		b.WriteString("in-addr.arpa")
		return b.String()
	}
	const hex = "0123456789abcdef"
	for j := len(s) - 1; j >= 0; j-- {
		b.WriteByte(hex[s[j]&0xf])
		b.WriteByte('.')
		b.WriteByte(hex[s[j]>>4])
		b.WriteByte('.')
	}
	b.WriteString("ip6.arpa")
	return b.String()
}

// encodeCert is the cached form: the key, then the chain, as PEM.
func encodeCert(c *tls.Certificate) []byte {
	var buf bytes.Buffer
	if der, err := x509.MarshalECPrivateKey(c.PrivateKey.(*ecdsa.PrivateKey)); err == nil {
		_ = pem.Encode(&buf, &pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	}
	for _, d := range c.Certificate {
		_ = pem.Encode(&buf, &pem.Block{Type: "CERTIFICATE", Bytes: d})
	}
	return buf.Bytes()
}
