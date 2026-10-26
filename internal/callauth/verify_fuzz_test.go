package callauth

import (
	"bytes"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Fuzz targets for the two decoders relays run on bytes a client hands
// them: a call grant (CallJoin) and a moderator control.
// Relay call-frame fuzzing lives with the relay (internal/relay).

var fuzzNow = time.UnixMilli(1_700_000_000_000)

func fuzzKey(t testing.TB, b byte) *identity.Key {
	k, err := identity.FromSeed(bytes.Repeat([]byte{b}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func fuzzGrant(t testing.TB, k *identity.Key) *wire.SignedCallGrant {
	nonce := bytes.Repeat([]byte{7}, 16)
	sg, err := SignGrant(k, &wire.CallGrant{
		CallId:      CallID(k.Public, nonce),
		ServerKey:   k.Public,
		CallNonce:   nonce,
		Participant: bytes.Repeat([]byte{9}, 16),
		Kid:         3,
		Audio:       true,
		Video:       true,
		ExpiresAt:   fuzzNow.Add(time.Minute).UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return sg
}

func fuzzControl(t testing.TB, k *identity.Key) *wire.SignedCallControl {
	sc, err := SignControl(k, &wire.CallControl{
		CallId:  CallID(k.Public, bytes.Repeat([]byte{7}, 16)),
		Seq:     4,
		Muted:   [][]byte{bytes.Repeat([]byte{9}, 16)},
		Removed: [][]byte{bytes.Repeat([]byte{8}, 16)},
		At:      fuzzNow.UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

// FuzzVerifyGrant: VerifyGrant never panics; the untouched signed grant
// verifies; changing its bytes or its signature (without the server's key)
// is always refused; and whatever the server's key signs is either refused
// or meets every documented check.
func FuzzVerifyGrant(f *testing.F) {
	k := fuzzKey(f, 1)
	good := fuzzGrant(f, k)
	f.Add(good.Grant, good.Signature, false)
	f.Add(good.Grant, good.Signature, true)
	f.Add(good.Grant[:len(good.Grant)-1], good.Signature, false)
	f.Add([]byte{}, []byte{}, false)
	f.Add([]byte{0x0a, 0xff}, good.Signature, true)
	f.Fuzz(func(t *testing.T, grant, sig []byte, resign bool) {
		k := fuzzKey(t, 1)
		good := fuzzGrant(t, k)
		if resign {
			sig = k.Sign(identity.CtxCallGrant, grant)
		}
		g, err := VerifyGrant(&wire.SignedCallGrant{Grant: grant, Signature: sig}, fuzzNow)
		untouched := bytes.Equal(grant, good.Grant) && bytes.Equal(sig, good.Signature)
		if untouched && err != nil {
			t.Fatalf("the valid grant was refused: %v", err)
		}
		if err != nil {
			if g != nil {
				t.Fatal("a refused grant was returned")
			}
			return
		}
		if !resign && !untouched {
			t.Fatalf("a modified grant verified without the server's key (grant %x sig %x)", grant, sig)
		}
		if len(g.ServerKey) != 32 || len(g.CallNonce) != 16 || len(g.Participant) != 16 {
			t.Fatalf("accepted a malformed grant: %v", g)
		}
		if !bytes.Equal(g.CallId, CallID(g.ServerKey, g.CallNonce)) {
			t.Fatal("accepted a grant whose call id doesn't commit to its key")
		}
		if fuzzNow.UnixMilli() >= g.ExpiresAt || g.ExpiresAt > fuzzNow.Add(MaxGrantLifetime).UnixMilli() {
			t.Fatalf("accepted a grant outside its lifetime: %d", g.ExpiresAt)
		}
		if identity.Verify(g.ServerKey, identity.CtxCallGrant, grant, sig) != nil {
			t.Fatal("accepted a grant its own server key didn't sign")
		}
	})
}

// FuzzVerifyControl: VerifyControl never panics; the untouched signed
// control verifies; changing its bytes or signature without the server's
// key is refused; and a control the server's key signed is either refused
// or within the list bounds. ControlCallID (run before the signature check)
// never panics either.
func FuzzVerifyControl(f *testing.F) {
	k := fuzzKey(f, 2)
	good := fuzzControl(f, k)
	f.Add(good.Control, good.Signature, false)
	f.Add(good.Control, good.Signature, true)
	f.Add(good.Control[1:], good.Signature, true)
	f.Add([]byte{}, []byte{}, false)
	f.Add([]byte{0x1a, 0x80, 0x80}, good.Signature, true)
	f.Fuzz(func(t *testing.T, control, sig []byte, resign bool) {
		k := fuzzKey(t, 2)
		good := fuzzControl(t, k)
		if resign {
			sig = k.Sign(identity.CtxCallControl, control)
		}
		sc := &wire.SignedCallControl{Control: control, Signature: sig}
		if id, err := ControlCallID(sc); err == nil && len(id) != 16 {
			t.Fatalf("ControlCallID returned a %d-byte id", len(id))
		}
		c, err := VerifyControl(sc, k.Public)
		untouched := bytes.Equal(control, good.Control) && bytes.Equal(sig, good.Signature)
		if untouched && err != nil {
			t.Fatalf("the valid control was refused: %v", err)
		}
		if err != nil {
			if c != nil {
				t.Fatal("a refused control was returned")
			}
			return
		}
		if !resign && !untouched {
			t.Fatalf("a modified control verified without the server's key (control %x sig %x)", control, sig)
		}
		for _, list := range [][][]byte{c.Muted, c.Deafened, c.Removed, c.NoVideo, c.NoScreen} {
			if len(list) > MaxControlList {
				t.Fatal("accepted an oversized control list")
			}
		}
		// Another server's key never verifies it.
		if _, err := VerifyControl(sc, fuzzKey(t, 3).Public); err == nil {
			t.Fatal("a control verified under another server's key")
		}
		var parsed wire.CallControl
		if proto.Unmarshal(control, &parsed) != nil {
			t.Fatal("accepted a control that doesn't parse")
		}
	})
}
