package identity

import (
	"path/filepath"
	"testing"
)

func TestSignVerify(t *testing.T) {
	k, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte("hello")
	sig := k.Sign(CtxRoute, msg)
	if err := Verify(k.Public, CtxRoute, msg, sig); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	if Verify(k.Public, CtxListing, msg, sig) == nil {
		t.Fatal("signature accepted under a different context")
	}
	if Verify(k.Public, CtxRoute, []byte("hellO"), sig) == nil {
		t.Fatal("signature accepted for altered message")
	}
	tampered := append([]byte(nil), sig...)
	tampered[0] ^= 1
	if Verify(k.Public, CtxRoute, msg, tampered) == nil {
		t.Fatal("tampered signature accepted")
	}
	if Verify(k.Public[:5], CtxRoute, msg, sig) == nil {
		t.Fatal("short key accepted")
	}
}

func TestSeedRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k.seed")
	a, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Public.Equal(b.Public) {
		t.Fatal("reloaded key differs")
	}
}

func TestServerCert(t *testing.T) {
	owner, _ := Generate()
	server, _ := Generate()
	other, _ := Generate()
	sc, err := IssueServerCert(owner, server.Public, 1, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyServerCert(sc, server.Public, owner.Public); err != nil {
		t.Fatalf("valid cert rejected: %v", err)
	}
	if _, err := VerifyServerCert(sc, other.Public, nil); err == nil {
		t.Fatal("cert accepted for wrong server key")
	}
	if _, err := VerifyServerCert(sc, server.Public, other.Public); err == nil {
		t.Fatal("cert accepted for wrong owner")
	}
	sc.Cert[len(sc.Cert)-1] ^= 1
	if _, err := VerifyServerCert(sc, server.Public, nil); err == nil {
		t.Fatal("altered cert accepted")
	}
}

func TestEncodeDecode(t *testing.T) {
	b := []byte{0, 1, 2, 250, 251, 252}
	got, err := Decode(Encode(b) + "==")
	if err != nil || string(got) != string(b) {
		t.Fatalf("round trip failed: %v %v", got, err)
	}
}
