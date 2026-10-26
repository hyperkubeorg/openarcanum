package identity

import (
	"bytes"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// FuzzVerifyServerCert feeds the owner-certificate check arbitrary bytes, the
// way certificates arrive from servers, maintainers and peers. With sign set,
// the cert body is signed by a real owner key first, so the checks after the
// signature see fuzzed content too. Verification must never panic, and a
// cert it accepts must certify exactly the server key asked about, for the
// community asked about (here a founder-only owner set, so its signer is the
// founder), under a valid owner signature.
func FuzzVerifyServerCert(f *testing.F) {
	owner, _ := FromSeed(bytes.Repeat([]byte{7}, 32))
	server, _ := FromSeed(bytes.Repeat([]byte{8}, 32))
	good, err := IssueServerCert(owner, server.Public, 3, FounderSet(owner.Public), nil)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(good.Cert, good.Signature, []byte(server.Public), []byte(owner.Public), false)
	f.Add(good.Cert, good.Signature, []byte(server.Public), []byte(nil), false)
	f.Add(good.Cert, good.Signature, []byte(owner.Public), []byte(owner.Public), true)
	f.Add([]byte{0xff}, make([]byte, 64), []byte(server.Public), []byte(nil), true)
	f.Add([]byte{}, []byte{}, []byte{}, []byte{}, false)
	f.Fuzz(func(t *testing.T, cert, sig, serverKey, wantOwner []byte, sign bool) {
		if sign {
			sig = owner.Sign(CtxServerCert, cert)
		}
		if len(wantOwner) == 0 {
			wantOwner = nil
		}
		c, err := VerifyServerCert(&wire.SignedServerCert{Cert: cert, Signature: sig}, serverKey, wantOwner)
		if err != nil {
			if c != nil {
				t.Fatal("cert returned with an error")
			}
			return
		}
		if !bytes.Equal(c.ServerKey, serverKey) {
			t.Fatalf("accepted a cert for %x when asked about %x", c.ServerKey, serverKey)
		}
		if wantOwner != nil && !bytes.Equal(c.OwnerKey, wantOwner) {
			t.Fatal("accepted a cert from another owner")
		}
		if Verify(c.OwnerKey, CtxServerCert, cert, sig) != nil {
			t.Fatal("accepted a cert without a valid owner signature")
		}
		var again wire.ServerCert
		if proto.Unmarshal(cert, &again) != nil {
			t.Fatal("accepted a cert that does not parse")
		}
	})
}

// FuzzVerify checks the domain-separated signature check against arbitrary
// keys, contexts, messages and signatures: no panic, and only the signature
// the key makes over exactly (context, message) verifies.
func FuzzVerify(f *testing.F) {
	k, _ := FromSeed(bytes.Repeat([]byte{9}, 32))
	f.Add([]byte(k.Public), CtxRoute, []byte("m"), k.Sign(CtxRoute, []byte("m")))
	f.Add([]byte(k.Public), CtxRoute+"\x00m", []byte{}, k.Sign(CtxRoute, []byte("m")))
	f.Add([]byte{}, "", []byte{}, []byte{})
	f.Fuzz(func(t *testing.T, pub []byte, ctx string, msg, sig []byte) {
		if Verify(pub, ctx, msg, sig) != nil {
			return
		}
		if !ValidPublic(pub) || len(sig) != 64 {
			t.Fatal("malformed key or signature accepted")
		}
		if bytes.Equal(pub, k.Public) && !bytes.Equal(sig, k.Sign(ctx, msg)) {
			// Ed25519 signing is deterministic; accepting a different
			// signature for the same key and message would be malleability.
			t.Fatal("accepted a signature the key did not make")
		}
	})
}

// FuzzDecode parses pasted base64url (codes, and keys in URLs and configs).
func FuzzDecode(f *testing.F) {
	f.Add("")
	f.Add(Encode([]byte("hello")))
	f.Add(" aGVsbG8= \n")
	f.Add("a+b/c")
	f.Fuzz(func(t *testing.T, s string) {
		b, err := Decode(s)
		if err != nil {
			return
		}
		again, err := Decode(Encode(b))
		if err != nil || !bytes.Equal(again, b) {
			t.Fatalf("round trip of %q failed", s)
		}
	})
}
