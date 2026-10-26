package callauth

import (
	"bytes"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

func newKey(t testing.TB) *identity.Key {
	t.Helper()
	k, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func randBytes(t testing.TB, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// grantFor is a well-formed grant for a call run by k, valid for life.
func grantFor(t testing.TB, k *identity.Key, now time.Time, life time.Duration) *wire.CallGrant {
	t.Helper()
	nonce := randBytes(t, 16)
	return &wire.CallGrant{
		CallId:          CallID(k.Public, nonce),
		ServerKey:       append([]byte(nil), k.Public...),
		CallNonce:       nonce,
		Participant:     randBytes(t, 16),
		Kid:             3,
		Audio:           true,
		Priority:        true,
		ExpiresAt:       now.Add(life).UnixMilli(),
		MaxParticipants: 99,
	}
}

func sign(t testing.TB, k *identity.Key, g *wire.CallGrant) *wire.SignedCallGrant {
	t.Helper()
	sg, err := SignGrant(k, g)
	if err != nil {
		t.Fatal(err)
	}
	return sg
}

func wantErr(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("accepted; want %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q; want %q", err, want)
	}
}

func TestCallIDCommitsToKeyAndNonce(t *testing.T) {
	k1, k2 := newKey(t), newKey(t)
	nonce := randBytes(t, 16)
	id := CallID(k1.Public, nonce)
	if len(id) != 16 {
		t.Fatalf("call id is %d bytes", len(id))
	}
	if !bytes.Equal(id, CallID(k1.Public, nonce)) {
		t.Fatal("call id isn't deterministic")
	}
	if bytes.Equal(id, CallID(k2.Public, nonce)) {
		t.Fatal("another server key gives the same call id")
	}
	if bytes.Equal(id, CallID(k1.Public, randBytes(t, 16))) {
		t.Fatal("another nonce gives the same call id")
	}
}

func TestVerifyGrantAccepts(t *testing.T) {
	k := newKey(t)
	now := time.Now()
	g := grantFor(t, k, now, 5*time.Minute)
	got, err := VerifyGrant(sign(t, k, g), now)
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(got, g) {
		t.Fatalf("verified grant differs: %v vs %v", got, g)
	}
	// Exactly the longest allowed lifetime is still accepted.
	g = grantFor(t, k, now, MaxGrantLifetime)
	if _, err := VerifyGrant(sign(t, k, g), now); err != nil {
		t.Fatalf("grant valid for exactly MaxGrantLifetime refused: %v", err)
	}
}

func TestVerifyGrantLifetime(t *testing.T) {
	k := newKey(t)
	now := time.Now()

	g := grantFor(t, k, now, MaxGrantLifetime+time.Millisecond)
	_, err := VerifyGrant(sign(t, k, g), now)
	wantErr(t, err, "valid for too long")

	g = grantFor(t, k, now, time.Hour)
	_, err = VerifyGrant(sign(t, k, g), now)
	wantErr(t, err, "valid for too long")

	// Expiry is exclusive: at expires_at it is already expired.
	g = grantFor(t, k, now, time.Minute)
	sg := sign(t, k, g)
	if _, err := VerifyGrant(sg, now.Add(time.Minute-time.Millisecond)); err != nil {
		t.Fatalf("refused a millisecond before expiry: %v", err)
	}
	_, err = VerifyGrant(sg, time.UnixMilli(g.ExpiresAt))
	wantErr(t, err, "expired")
	_, err = VerifyGrant(sg, now.Add(time.Hour))
	wantErr(t, err, "expired")
}

func TestVerifyGrantSignature(t *testing.T) {
	k, other := newKey(t), newKey(t)
	now := time.Now()
	g := grantFor(t, k, now, time.Minute)
	sg := sign(t, k, g)

	// A flipped signature bit.
	bad := proto.Clone(sg).(*wire.SignedCallGrant)
	bad.Signature[0] ^= 1
	_, err := VerifyGrant(bad, now)
	wantErr(t, err, "signature")

	// A changed grant under the old signature (a member granting themselves
	// Priority Speaker or a longer life).
	g2 := proto.Clone(g).(*wire.CallGrant)
	g2.Video = true
	raw, err := proto.Marshal(g2)
	if err != nil {
		t.Fatal(err)
	}
	_, err = VerifyGrant(&wire.SignedCallGrant{Grant: raw, Signature: sg.Signature}, now)
	wantErr(t, err, "signature")

	// Signed by another key while naming k as the server.
	_, err = VerifyGrant(sign(t, other, g), now)
	wantErr(t, err, "signature")

	// Signed under the control context instead of the grant context.
	_, err = VerifyGrant(&wire.SignedCallGrant{Grant: sg.Grant, Signature: k.Sign(identity.CtxCallControl, sg.Grant)}, now)
	wantErr(t, err, "signature")

	// Missing or short signature.
	_, err = VerifyGrant(&wire.SignedCallGrant{Grant: sg.Grant}, now)
	wantErr(t, err, "signature")
}

func TestVerifyGrantWrongServerKey(t *testing.T) {
	k, other := newKey(t), newKey(t)
	now := time.Now()
	// A server signs a grant whose call id commits to another server's key:
	// it can't let members into a call it doesn't run.
	g := grantFor(t, k, now, time.Minute)
	g.CallId = CallID(other.Public, g.CallNonce)
	_, err := VerifyGrant(sign(t, k, g), now)
	wantErr(t, err, "another server")

	// Or the right key with another nonce than the id commits to.
	g = grantFor(t, k, now, time.Minute)
	g.CallNonce = randBytes(t, 16)
	_, err = VerifyGrant(sign(t, k, g), now)
	wantErr(t, err, "another server")
}

func TestVerifyGrantMalformed(t *testing.T) {
	k := newKey(t)
	now := time.Now()
	_, err := VerifyGrant(nil, now)
	wantErr(t, err, "no grant")
	_, err = VerifyGrant(&wire.SignedCallGrant{Grant: []byte{0xff, 0xff, 0xff}}, now)
	wantErr(t, err, "malformed")

	for name, edit := range map[string]func(*wire.CallGrant){
		"short server key":  func(g *wire.CallGrant) { g.ServerKey = g.ServerKey[:31] },
		"no server key":     func(g *wire.CallGrant) { g.ServerKey = nil },
		"short nonce":       func(g *wire.CallGrant) { g.CallNonce = g.CallNonce[:15] },
		"long participant":  func(g *wire.CallGrant) { g.Participant = append(g.Participant, 0) },
		"empty participant": func(g *wire.CallGrant) { g.Participant = nil },
	} {
		t.Run(name, func(t *testing.T) {
			g := grantFor(t, k, now, time.Minute)
			edit(g)
			_, err := VerifyGrant(sign(t, k, g), now)
			wantErr(t, err, "malformed")
		})
	}
}

func signControl(t testing.TB, k *identity.Key, c *wire.CallControl) *wire.SignedCallControl {
	t.Helper()
	sc, err := SignControl(k, c)
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

func ids(t testing.TB, n int) [][]byte {
	out := make([][]byte, n)
	for i := range out {
		out[i] = randBytes(t, 16)
	}
	return out
}

func TestVerifyControl(t *testing.T) {
	k, other := newKey(t), newKey(t)
	callID := CallID(k.Public, randBytes(t, 16))
	c := &wire.CallControl{CallId: callID, Seq: 7, Muted: ids(t, 2), Deafened: ids(t, 1), Removed: ids(t, 3), At: time.Now().UnixMilli()}
	sc := signControl(t, k, c)

	got, err := VerifyControl(sc, k.Public)
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(got, c) {
		t.Fatal("verified control differs")
	}
	id, err := ControlCallID(sc)
	if err != nil || !bytes.Equal(id, callID) {
		t.Fatalf("ControlCallID = %x, %v", id, err)
	}

	// Checked against another server's key.
	_, err = VerifyControl(sc, other.Public)
	wantErr(t, err, "signature")
	// Signed by another key.
	_, err = VerifyControl(signControl(t, other, c), k.Public)
	wantErr(t, err, "signature")
	// Tampered after signing (someone un-muting themselves).
	c2 := proto.Clone(c).(*wire.CallControl)
	c2.Muted = nil
	raw, err := proto.Marshal(c2)
	if err != nil {
		t.Fatal(err)
	}
	_, err = VerifyControl(&wire.SignedCallControl{Control: raw, Signature: sc.Signature}, k.Public)
	wantErr(t, err, "signature")
	// A grant signature can't stand in for a control signature.
	_, err = VerifyControl(&wire.SignedCallControl{Control: sc.Control, Signature: k.Sign(identity.CtxCallGrant, sc.Control)}, k.Public)
	wantErr(t, err, "signature")
	// A bad server key length.
	_, err = VerifyControl(sc, k.Public[:16])
	wantErr(t, err, "signature")
	_, err = VerifyControl(nil, k.Public)
	wantErr(t, err, "no control")

	// Signed but not a CallControl.
	junk := []byte{0xff, 0xff, 0xff}
	_, err = VerifyControl(&wire.SignedCallControl{Control: junk, Signature: k.Sign(identity.CtxCallControl, junk)}, k.Public)
	wantErr(t, err, "malformed")
}

func TestVerifyControlListCap(t *testing.T) {
	k := newKey(t)
	callID := CallID(k.Public, randBytes(t, 16))
	full := ids(t, MaxControlList)
	over := ids(t, MaxControlList+1)

	// Each list may hold exactly MaxControlList.
	all := &wire.CallControl{CallId: callID, Muted: full, Deafened: full, Removed: full, NoVideo: full, NoScreen: full}
	if _, err := VerifyControl(signControl(t, k, all), k.Public); err != nil {
		t.Fatalf("lists of exactly MaxControlList refused: %v", err)
	}
	for name, edit := range map[string]func(*wire.CallControl){
		"muted":     func(c *wire.CallControl) { c.Muted = over },
		"deafened":  func(c *wire.CallControl) { c.Deafened = over },
		"removed":   func(c *wire.CallControl) { c.Removed = over },
		"no_video":  func(c *wire.CallControl) { c.NoVideo = over },
		"no_screen": func(c *wire.CallControl) { c.NoScreen = over },
	} {
		t.Run(name, func(t *testing.T) {
			c := &wire.CallControl{CallId: callID}
			edit(c)
			_, err := VerifyControl(signControl(t, k, c), k.Public)
			wantErr(t, err, "too many")
		})
	}
}

// Each list names participants by their 16-byte id, nothing longer: the
// relay keeps removed ids for as long as a grant lives.
func TestVerifyControlParticipantLength(t *testing.T) {
	k := newKey(t)
	callID := CallID(k.Public, randBytes(t, 16))
	for name, edit := range map[string]func(*wire.CallControl, []byte){
		"muted":     func(c *wire.CallControl, p []byte) { c.Muted = [][]byte{p} },
		"deafened":  func(c *wire.CallControl, p []byte) { c.Deafened = [][]byte{p} },
		"removed":   func(c *wire.CallControl, p []byte) { c.Removed = [][]byte{p} },
		"no_video":  func(c *wire.CallControl, p []byte) { c.NoVideo = [][]byte{p} },
		"no_screen": func(c *wire.CallControl, p []byte) { c.NoScreen = [][]byte{p} },
	} {
		t.Run(name, func(t *testing.T) {
			for _, n := range []int{0, 15, 17, 1024} {
				c := &wire.CallControl{CallId: callID}
				edit(c, randBytes(t, n))
				_, err := VerifyControl(signControl(t, k, c), k.Public)
				wantErr(t, err, "malformed participant")
			}
			c := &wire.CallControl{CallId: callID}
			edit(c, randBytes(t, 16))
			if _, err := VerifyControl(signControl(t, k, c), k.Public); err != nil {
				t.Fatalf("16-byte id refused: %v", err)
			}
		})
	}
}

func TestControlCallIDMalformed(t *testing.T) {
	if _, err := ControlCallID(nil); err == nil {
		t.Fatal("nil control accepted")
	}
	if _, err := ControlCallID(&wire.SignedCallControl{Control: []byte{0xff}}); err == nil {
		t.Fatal("garbage accepted")
	}
	raw, err := proto.Marshal(&wire.CallControl{CallId: make([]byte, 15)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ControlCallID(&wire.SignedCallControl{Control: raw}); err == nil {
		t.Fatal("15-byte call id accepted")
	}
}
