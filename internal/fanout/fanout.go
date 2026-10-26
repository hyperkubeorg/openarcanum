// Package fanout signs and checks the tokens a relay requires before it
// copies one publish to a large number of subscribers (docs/protocol.md §2
// "Large fan-outs").
//
// A topic that may grow large commits to the Ed25519 key of the community
// server that hands it out:
//
//	topic = r || SHA-256("openarcanum/fanout-topic/v1" || 0x00 || issuer || r)[:16]
//
// with r 16 random bytes, the same domain-separated hash binding that
// callauth.CallID uses for calls. The issuer signs a short-lived token for one
// relay connection: bound to the relay's key, the relay URL the issuer
// dialled and that connection's Hello nonce (internal/relayid), so it can't
// be replayed on another connection or relay, nor obtained through a relay
// that presents another relay's nonce. The relay checks the signature
// once, keeps the issuer for the token's lifetime on that connection only,
// and lets the connection publish to large topics that commit to it. Nothing
// is stored per user.
package fanout

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/relayid"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

const topicContext = "openarcanum/fanout-topic/v1"

// TopicLen is the length of a committed topic.
const TopicLen = 32

// MaxTokenLifetime bounds how long a token may be valid; issuers renew well
// before it runs out.
const MaxTokenLifetime = time.Hour

// TokenLifetime is what the community server asks for; it renews at half.
const TokenLifetime = 30 * time.Minute

func commitment(issuer, r []byte) []byte {
	h := sha256.New()
	h.Write([]byte(topicContext))
	h.Write([]byte{0})
	h.Write(issuer)
	h.Write(r)
	return h.Sum(nil)[:TopicLen-16]
}

// NewTopic makes a fresh random topic committed to issuer.
func NewTopic(issuer []byte) []byte {
	r := make([]byte, 16)
	if _, err := rand.Read(r); err != nil {
		panic(err)
	}
	return append(r, commitment(issuer, r)...)
}

// Commits reports whether topic was made by NewTopic(issuer).
func Commits(topic, issuer []byte) bool {
	return len(topic) == TopicLen && len(issuer) == 32 && bytes.Equal(topic[16:], commitment(issuer, topic[:16]))
}

// Sign issues a token for the relay connection b names (the relay's key,
// the URL the issuer dialled, the connection's Hello nonce).
func Sign(k *identity.Key, b relayid.Binding, expires time.Time) (*wire.SignedFanoutToken, error) {
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(&wire.FanoutToken{
		Issuer: k.Public, RelayNonce: b.Nonce, ExpiresAt: expires.UnixMilli(), RelayKey: b.RelayKey, Url: b.URL,
	})
	if err != nil {
		return nil, err
	}
	return &wire.SignedFanoutToken{Token: raw, Signature: k.Sign(identity.CtxFanoutToken, raw)}, nil
}

// Verify checks a token for the relay connection local describes and
// returns its issuer and expiry.
func Verify(st *wire.SignedFanoutToken, local relayid.Local, now time.Time) ([]byte, time.Time, error) {
	if st == nil {
		return nil, time.Time{}, errors.New("no token")
	}
	t := &wire.FanoutToken{}
	if err := proto.Unmarshal(st.Token, t); err != nil || !identity.ValidPublic(t.Issuer) {
		return nil, time.Time{}, errors.New("malformed token")
	}
	if err := identity.Verify(t.Issuer, identity.CtxFanoutToken, st.Token, st.Signature); err != nil {
		return nil, time.Time{}, errors.New("token signature doesn't verify")
	}
	switch err := local.Check(t.RelayKey, t.Url, t.RelayNonce); {
	case errors.Is(err, relayid.ErrOtherConn):
		return nil, time.Time{}, errors.New("token is for another connection")
	case err != nil:
		return nil, time.Time{}, errors.New("token is for another relay: " + err.Error())
	}
	exp := time.UnixMilli(t.ExpiresAt)
	if !now.Before(exp) {
		return nil, time.Time{}, errors.New("token has expired")
	}
	if exp.After(now.Add(MaxTokenLifetime)) {
		return nil, time.Time{}, errors.New("token is valid for too long")
	}
	return t.Issuer, exp, nil
}
