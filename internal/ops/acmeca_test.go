package ops

// testCA is a small in-process ACME CA (RFC 8555) for the TLS tests: it
// serves the directory, accounts, orders for "dns" and "ip" identifiers
// (with optional profiles), the tls-alpn-01 challenge (it dials the node,
// as a real CA would, and checks the acmeIdentifier extension and the
// name or address), finalization and certificate download. JWS signatures
// aren't checked: the client under test is golang.org/x/crypto/acme, and the
// test only cares that the node drives the protocol and serves what it gets.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/acme"
)

type testCA struct {
	t   *testing.T
	srv *httptest.Server
	// Target is the address the CA dials for every identifier's challenge.
	Target string
	// Profiles the directory advertises, and each one's certificate
	// lifetime (the default lifetime applies without a profile).
	Profiles map[string]time.Duration
	Lifetime time.Duration

	root    *x509.Certificate
	rootKey *ecdsa.PrivateKey

	mu       sync.Mutex
	accounts map[string]*ecdsa.PublicKey // by account URL
	orders   []*caOrder
	authzs   []*caAuthz
	certs    [][]byte // PEM chains
	// Ordered records each order's identifiers and profile.
	Ordered []string
	// Checked records the server names challenges were checked with.
	Checked []string
}

type caOrder struct {
	ids     []acme.AuthzID
	profile string
	authzs  []int
	status  string
	cert    int // index in certs, -1 until issued
}

type caAuthz struct {
	id      acme.AuthzID
	token   string
	status  string
	account string
}

func newTestCA(t *testing.T) *testCA {
	ca := &testCA{t: t, Lifetime: 90 * 24 * time.Hour, Profiles: map[string]time.Duration{}, accounts: map[string]*ecdsa.PublicKey{}}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "OpenArcanum test ACME root"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca.root, _ = x509.ParseCertificate(der)
	ca.rootKey = key
	ca.srv = httptest.NewServer(http.HandlerFunc(ca.handle))
	t.Cleanup(ca.srv.Close)
	return ca
}

// URL is the directory URL.
func (ca *testCA) URL() string { return ca.srv.URL + "/dir" }

// Roots trusts the CA's root.
func (ca *testCA) Roots() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.root)
	return p
}

func (ca *testCA) url(format string, a ...any) string { return ca.srv.URL + fmt.Sprintf(format, a...) }

func (ca *testCA) problem(w http.ResponseWriter, code int, typ, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "urn:ietf:params:acme:error:" + typ, "detail": detail})
}

func (ca *testCA) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Replay-Nonce", fmt.Sprintf("n%d", time.Now().UnixNano()))
	if r.URL.Path == "/dir" {
		profiles := map[string]string{}
		for p := range ca.Profiles {
			profiles[p] = "https://ca.example.invalid/profiles/" + p
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"newNonce": ca.url("/nonce"), "newAccount": ca.url("/account"), "newOrder": ca.url("/order"),
			"revokeCert": ca.url("/revoke"), "keyChange": ca.url("/keychange"),
			"meta": map[string]any{"termsOfService": "https://ca.example.invalid/terms", "profiles": profiles},
		})
		return
	}
	if r.URL.Path == "/nonce" {
		return
	}
	var jws struct{ Protected, Payload string }
	if err := json.NewDecoder(r.Body).Decode(&jws); err != nil {
		ca.problem(w, 400, "malformed", "not a JWS")
		return
	}
	hb, _ := base64.RawURLEncoding.DecodeString(jws.Protected)
	payload, _ := base64.RawURLEncoding.DecodeString(jws.Payload)
	var hdr struct {
		JWK *struct{ Kty, Crv, X, Y string } `json:"jwk"`
		KID string                           `json:"kid"`
	}
	if err := json.Unmarshal(hb, &hdr); err != nil {
		ca.problem(w, 400, "malformed", "bad protected header")
		return
	}
	ca.mu.Lock()
	defer ca.mu.Unlock()
	var n int
	switch {
	case r.URL.Path == "/account":
		if hdr.JWK == nil || hdr.JWK.Kty != "EC" || hdr.JWK.Crv != "P-256" {
			ca.problem(w, 400, "badPublicKey", "test CA takes P-256 account keys")
			return
		}
		x, _ := base64.RawURLEncoding.DecodeString(hdr.JWK.X)
		y, _ := base64.RawURLEncoding.DecodeString(hdr.JWK.Y)
		pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
		if err != nil {
			ca.problem(w, 400, "badPublicKey", err.Error())
			return
		}
		for u, k := range ca.accounts {
			if k.Equal(pub) {
				w.Header().Set("Location", u)
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "valid"})
				return
			}
		}
		u := ca.url("/acct/%d", len(ca.accounts))
		ca.accounts[u] = pub
		w.Header().Set("Location", u)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "valid"})
	case ca.accounts[hdr.KID] == nil:
		ca.problem(w, 400, "accountDoesNotExist", "unknown account")
	case r.URL.Path == "/order":
		var req struct {
			Identifiers []struct{ Type, Value string }
			Profile     string
		}
		_ = json.Unmarshal(payload, &req)
		if _, ok := ca.Profiles[req.Profile]; req.Profile != "" && !ok {
			ca.problem(w, 400, "invalidProfile", "unknown profile")
			return
		}
		o := &caOrder{profile: req.Profile, status: "pending", cert: -1}
		var desc []string
		for _, id := range req.Identifiers {
			o.ids = append(o.ids, acme.AuthzID{Type: id.Type, Value: id.Value})
			ca.authzs = append(ca.authzs, &caAuthz{id: acme.AuthzID{Type: id.Type, Value: id.Value}, token: fmt.Sprintf("tok%d", len(ca.authzs)), status: "pending", account: hdr.KID})
			o.authzs = append(o.authzs, len(ca.authzs)-1)
			desc = append(desc, id.Type+":"+id.Value)
		}
		ca.Ordered = append(ca.Ordered, strings.Join(desc, ",")+" profile="+req.Profile)
		ca.orders = append(ca.orders, o)
		w.Header().Set("Location", ca.url("/orders/%d", len(ca.orders)-1))
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(ca.orderJSON(len(ca.orders) - 1))
	case scan(r.URL.Path, "/orders/%d", &n) && n < len(ca.orders):
		_ = json.NewEncoder(w).Encode(ca.orderJSON(n))
	case scan(r.URL.Path, "/authz/%d", &n) && n < len(ca.authzs):
		_ = json.NewEncoder(w).Encode(ca.authzJSON(n))
	case scan(r.URL.Path, "/chal/%d", &n) && n < len(ca.authzs):
		a := ca.authzs[n]
		if a.status == "pending" {
			a.status = "invalid"
			if err := ca.checkALPN(a); err != nil {
				ca.t.Logf("test CA: challenge for %s failed: %v", a.id.Value, err)
			} else {
				a.status = "valid"
			}
			ca.updateOrders()
		}
		_ = json.NewEncoder(w).Encode(ca.authzJSON(n)["challenges"].([]any)[0])
	case scan(r.URL.Path, "/finalize/%d", &n) && n < len(ca.orders):
		o := ca.orders[n]
		if o.status != "ready" {
			ca.problem(w, 403, "orderNotReady", "order is "+o.status)
			return
		}
		var req struct{ CSR string }
		_ = json.Unmarshal(payload, &req)
		der, _ := base64.RawURLEncoding.DecodeString(req.CSR)
		chain, err := ca.issue(o, der)
		if err != nil {
			ca.problem(w, 400, "badCSR", err.Error())
			return
		}
		ca.certs = append(ca.certs, chain)
		o.cert, o.status = len(ca.certs)-1, "valid"
		w.Header().Set("Location", ca.url("/orders/%d", n))
		_ = json.NewEncoder(w).Encode(ca.orderJSON(n))
	case scan(r.URL.Path, "/cert/%d", &n) && n < len(ca.certs):
		w.Header().Set("Content-Type", "application/pem-certificate-chain")
		_, _ = w.Write(ca.certs[n])
	default:
		ca.problem(w, 404, "malformed", "no such resource")
	}
}

func scan(path, format string, n *int) bool {
	_, err := fmt.Sscanf(path, format, n)
	return err == nil
}

func (ca *testCA) orderJSON(n int) map[string]any {
	o := ca.orders[n]
	var ids []map[string]string
	var authzs []string
	for _, id := range o.ids {
		ids = append(ids, map[string]string{"type": id.Type, "value": id.Value})
	}
	for _, a := range o.authzs {
		authzs = append(authzs, ca.url("/authz/%d", a))
	}
	out := map[string]any{"status": o.status, "identifiers": ids, "authorizations": authzs, "finalize": ca.url("/finalize/%d", n)}
	if o.cert >= 0 {
		out["certificate"] = ca.url("/cert/%d", o.cert)
	}
	return out
}

func (ca *testCA) authzJSON(n int) map[string]any {
	a := ca.authzs[n]
	return map[string]any{
		"status":     a.status,
		"identifier": map[string]string{"type": a.id.Type, "value": a.id.Value},
		"challenges": []any{map[string]string{"type": "tls-alpn-01", "url": ca.url("/chal/%d", n), "token": a.token, "status": a.status}},
	}
}

func (ca *testCA) updateOrders() {
	for _, o := range ca.orders {
		if o.status != "pending" {
			continue
		}
		ready := true
		for _, a := range o.authzs {
			switch ca.authzs[a].status {
			case "invalid":
				o.status = "invalid"
			case "pending":
				ready = false
			}
		}
		if ready && o.status == "pending" {
			o.status = "ready"
		}
	}
}

// checkALPN connects to Target the way a CA checks tls-alpn-01 (RFC 8737,
// and RFC 8738 §6 for IP addresses).
func (ca *testCA) checkALPN(a *caAuthz) error {
	sni := a.id.Value
	if a.id.Type == "ip" {
		addr, err := netip.ParseAddr(a.id.Value)
		if err != nil {
			return err
		}
		sni = arpaName(addr)
	}
	ca.Checked = append(ca.Checked, sni)
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", ca.Target, &tls.Config{
		ServerName: sni, NextProtos: []string{acme.ALPNProto}, InsecureSkipVerify: true,
	})
	if err != nil {
		return err
	}
	defer conn.Close()
	st := conn.ConnectionState()
	if st.NegotiatedProtocol != acme.ALPNProto || len(st.PeerCertificates) != 1 {
		return fmt.Errorf("negotiated %q with %d certificates", st.NegotiatedProtocol, len(st.PeerCertificates))
	}
	leaf := st.PeerCertificates[0]
	if a.id.Type == "ip" {
		if len(leaf.IPAddresses) != 1 || leaf.IPAddresses[0].String() != a.id.Value || len(leaf.DNSNames) != 0 {
			return fmt.Errorf("challenge certificate names %v %v", leaf.IPAddresses, leaf.DNSNames)
		}
	} else if !slices.Equal(leaf.DNSNames, []string{a.id.Value}) {
		return fmt.Errorf("challenge certificate names %v", leaf.DNSNames)
	}
	thumb, err := acme.JWKThumbprint(ca.accounts[a.account])
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(a.token + "." + thumb))
	want, _ := asn1.Marshal(sum[:])
	for _, e := range leaf.Extensions {
		if e.Id.Equal(asn1.ObjectIdentifier{1, 3, 6, 1, 5, 5, 7, 1, 31}) {
			if !e.Critical || string(e.Value) != string(want) {
				return fmt.Errorf("acmeIdentifier extension doesn't match")
			}
			return nil
		}
	}
	return fmt.Errorf("no acmeIdentifier extension")
}

// issue signs the CSR, which must name exactly the order's identifiers.
func (ca *testCA) issue(o *caOrder, der []byte) ([]byte, error) {
	csr, err := x509.ParseCertificateRequest(der)
	if err != nil {
		return nil, err
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, err
	}
	var want, got []string
	for _, id := range o.ids {
		want = append(want, id.Value)
	}
	got = append(got, csr.DNSNames...)
	for _, ip := range csr.IPAddresses {
		got = append(got, ip.String())
	}
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(want, got) {
		return nil, fmt.Errorf("CSR names %v, order %v", got, want)
	}
	life := ca.Lifetime
	if o.profile != "" {
		life = ca.Profiles[o.profile]
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano()),
		NotBefore:    now.Add(-time.Second),
		NotAfter:     now.Add(life),
		DNSNames:     csr.DNSNames,
		IPAddresses:  csr.IPAddresses,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leaf, err := x509.CreateCertificate(rand.Reader, tmpl, ca.root, csr.PublicKey, ca.rootKey)
	if err != nil {
		return nil, err
	}
	out := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf})
	return append(out, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.root.Raw})...), nil
}
