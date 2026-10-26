package fanout

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/relayid"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// testRelayKey and testRelayURL name the relay the tests' tokens are for.
var testRelayKey = bytes.Repeat([]byte{9}, 32)

const testRelayURL = "wss://relay.example/ws"

func bind(nonce []byte) relayid.Binding {
	return relayid.Binding{RelayKey: testRelayKey, URL: testRelayURL, Nonce: nonce}
}

func local(nonce []byte) relayid.Local {
	return relayid.Local{Key: testRelayKey, URL: testRelayURL, Nonce: nonce}
}

// A token is bound to the relay's key and URL as well as the connection's
// nonce: a malicious relay that presents another relay's nonce gets a
// token naming itself, which the other relay refuses.
func TestTokenWrongRelay(t *testing.T) {
	k := key(t)
	nonce := bytes.Repeat([]byte{1}, 32)
	exp := time.Now().Add(time.Minute)
	otherKey := bytes.Repeat([]byte{8}, 32)
	cases := map[string]relayid.Binding{
		"another relay's key":   {RelayKey: otherKey, URL: testRelayURL, Nonce: nonce},
		"another relay's URL":   {RelayKey: testRelayKey, URL: "wss://evil.example/ws", Nonce: nonce},
		"no relay key":          {URL: testRelayURL, Nonce: nonce},
		"no URL":                {RelayKey: testRelayKey, Nonce: nonce},
		"a non-canonical URL":   {RelayKey: testRelayKey, URL: "wss://RELAY.example:443/ws/", Nonce: nonce},
		"the v1 form (no bind)": {Nonce: nonce},
	}
	for name, b := range cases {
		st, _ := Sign(k, b, exp)
		if _, _, err := Verify(st, local(nonce), time.Now()); err == nil || !strings.Contains(err.Error(), "another relay") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A relay that has no configured URL takes any canonical URL, but still
	// only its own key.
	urlless := relayid.Local{Key: testRelayKey, Nonce: nonce}
	st, _ := Sign(k, relayid.Binding{RelayKey: testRelayKey, URL: "ws://10.0.0.1:7000/ws", Nonce: nonce}, exp)
	if _, _, err := Verify(st, urlless, time.Now()); err != nil {
		t.Fatalf("relay without a URL: %v", err)
	}
	st, _ = Sign(k, cases["another relay's key"], exp)
	if _, _, err := Verify(st, urlless, time.Now()); err == nil {
		t.Fatal("relay without a URL accepted another relay's key")
	}
}

func key(t *testing.T) *identity.Key {
	t.Helper()
	k, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestTopicCommitment(t *testing.T) {
	a, b := key(t), key(t)
	topic := NewTopic(a.Public)
	if len(topic) != TopicLen {
		t.Fatalf("topic length %d", len(topic))
	}
	if !Commits(topic, a.Public) {
		t.Fatal("topic doesn't commit to its issuer")
	}
	if Commits(topic, b.Public) {
		t.Fatal("topic commits to another key")
	}
	if bytes.Equal(topic, NewTopic(a.Public)) {
		t.Fatal("two topics for one issuer are equal")
	}
	for i := range topic {
		bad := bytes.Clone(topic)
		bad[i] ^= 1
		if Commits(bad, a.Public) {
			t.Fatalf("topic with byte %d flipped still commits", i)
		}
	}
	for _, bad := range [][]byte{nil, topic[:31], append(bytes.Clone(topic), 0)} {
		if Commits(bad, a.Public) {
			t.Fatalf("a %d-byte topic commits", len(bad))
		}
	}
	if Commits(topic, a.Public[:31]) {
		t.Fatal("a short issuer key matched")
	}
}

func TestTokenVerifies(t *testing.T) {
	k := key(t)
	nonce := bytes.Repeat([]byte{7}, 32)
	now := time.Now()
	exp := now.Add(TokenLifetime)
	st, err := Sign(k, bind(nonce), exp)
	if err != nil {
		t.Fatal(err)
	}
	issuer, got, err := Verify(st, local(nonce), now)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(issuer, k.Public) || got.UnixMilli() != exp.UnixMilli() {
		t.Fatalf("issuer %x expiry %v", issuer, got)
	}
}

// The expiry boundary: valid strictly before expires_at, refused at it and
// after; a lifetime over MaxTokenLifetime is refused, exactly the maximum
// is accepted.
func TestTokenExpiryBoundaries(t *testing.T) {
	k := key(t)
	nonce := bytes.Repeat([]byte{1}, 32)
	now := time.UnixMilli(time.Now().UnixMilli())
	exp := now.Add(time.Minute)
	st, _ := Sign(k, bind(nonce), exp)
	if _, _, err := Verify(st, local(nonce), exp.Add(-time.Millisecond)); err != nil {
		t.Fatalf("a millisecond before expiry: %v", err)
	}
	for _, at := range []time.Time{exp, exp.Add(time.Millisecond), exp.Add(time.Hour)} {
		if _, _, err := Verify(st, local(nonce), at); err == nil || !strings.Contains(err.Error(), "expired") {
			t.Fatalf("at %v after expiry: %v", at.Sub(exp), err)
		}
	}
	max, _ := Sign(k, bind(nonce), now.Add(MaxTokenLifetime))
	if _, _, err := Verify(max, local(nonce), now); err != nil {
		t.Fatalf("token valid for exactly the maximum: %v", err)
	}
	long, _ := Sign(k, bind(nonce), now.Add(MaxTokenLifetime+time.Millisecond))
	if _, _, err := Verify(long, local(nonce), now); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("token valid for longer than the maximum: %v", err)
	}
}

// A token is bound to the relay connection's nonce: on another connection
// (another nonce, or none) it is refused.
func TestTokenWrongNonce(t *testing.T) {
	k := key(t)
	nonce := bytes.Repeat([]byte{1}, 32)
	st, _ := Sign(k, bind(nonce), time.Now().Add(time.Minute))
	other := bytes.Clone(nonce)
	other[31] ^= 1
	for _, n := range [][]byte{other, nil, nonce[:31]} {
		if _, _, err := Verify(st, local(n), time.Now()); err == nil || !strings.Contains(err.Error(), "another connection") {
			t.Fatalf("nonce %x: %v", n, err)
		}
	}
}

// Malformed and tampered tokens are refused.
func TestTokenMalformed(t *testing.T) {
	k := key(t)
	nonce := bytes.Repeat([]byte{1}, 32)
	now := time.Now()
	st, _ := Sign(k, bind(nonce), now.Add(time.Minute))
	if _, _, err := Verify(nil, local(nonce), now); err == nil {
		t.Fatal("nil token accepted")
	}
	cases := map[string]*wire.SignedFanoutToken{
		"garbage":       {Token: []byte{0xff, 0xff, 0xff}, Signature: st.Signature},
		"empty":         {},
		"no signature":  {Token: st.Token},
		"bad signature": {Token: st.Token, Signature: bytes.Repeat([]byte{1}, 64)},
	}
	flipped := bytes.Clone(st.Token)
	flipped[len(flipped)-1] ^= 1
	cases["tampered token"] = &wire.SignedFanoutToken{Token: flipped, Signature: st.Signature}
	// A token naming an issuer that didn't sign it.
	other := key(t)
	raw, _ := proto.Marshal(&wire.FanoutToken{Issuer: other.Public, RelayNonce: nonce, ExpiresAt: now.Add(time.Minute).UnixMilli()})
	cases["wrong issuer"] = &wire.SignedFanoutToken{Token: raw, Signature: k.Sign(identity.CtxFanoutToken, raw)}
	// Signed under another context (a different signature purpose).
	cases["wrong context"] = &wire.SignedFanoutToken{Token: st.Token, Signature: k.Sign(identity.CtxRelayEndpoint, st.Token)}
	// An issuer key of the wrong length.
	short, _ := proto.Marshal(&wire.FanoutToken{Issuer: k.Public[:16], RelayNonce: nonce, ExpiresAt: now.Add(time.Minute).UnixMilli()})
	cases["short issuer"] = &wire.SignedFanoutToken{Token: short, Signature: k.Sign(identity.CtxFanoutToken, short)}
	for name, c := range cases {
		if _, _, err := Verify(c, local(nonce), now); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func FuzzVerify(f *testing.F) {
	k, _ := identity.Generate()
	nonce := bytes.Repeat([]byte{1}, 32)
	st, _ := Sign(k, bind(nonce), time.Now().Add(time.Minute))
	f.Add(st.Token, st.Signature)
	f.Fuzz(func(t *testing.T, token, sig []byte) {
		issuer, _, err := Verify(&wire.SignedFanoutToken{Token: token, Signature: sig}, local(nonce), time.Now())
		if err == nil && !identity.ValidPublic(issuer) {
			t.Fatal("accepted a token with an invalid issuer")
		}
	})
}
