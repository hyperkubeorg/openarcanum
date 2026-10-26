// Package relayid binds what clients sign for a relay connection to the
// relay's identity (docs/protocol.md §2 "Relay identity").
//
// Every relay has an Ed25519 key (its registry key). Its Hello carries the
// key, the canonical URL the relay is configured to be reached at (empty
// when it has none) and a proof for this connection:
//
//	relay_signature = Sign(relay_key, "openarcanum/relay-hello/v1" || RelayBinding{relay_key, url, nonce})
//
// Endpoint registrations, fan-out tokens and moderation commands are then
// signed over (or carry) a RelayBinding of the relay's key, the canonical
// URL the client dialled and the connection's nonce. A relay accepts one
// only when the key is its own, the nonce is the connection's, and the URL
// is its own (or, for a relay with no configured URL, any canonical relay
// URL). Signing only the nonce let a malicious relay present another
// relay's nonce in its own Hello and replay what it got signed there.
//
// No new construction: Ed25519 with the §1 domain separation, under new
// versioned contexts.
package relayid

import (
	"bytes"
	"errors"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/relayurl"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Binding is what signatures for one relay connection are bound to.
type Binding struct {
	RelayKey []byte
	URL      string
	Nonce    []byte
}

// Bytes is the binding's signed form: RelayBinding serialized
// deterministically.
func (b Binding) Bytes() []byte {
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(&wire.RelayBinding{
		RelayKey: b.RelayKey, Url: b.URL, Nonce: b.Nonce,
	})
	if err != nil {
		panic(err) // three scalar fields can't fail to marshal
	}
	return raw
}

// SignHello is the relay's proof of its key for the connection with nonce;
// url is the relay's own canonical URL, or empty.
func SignHello(k *identity.Key, url string, nonce []byte) []byte {
	return k.Sign(identity.CtxRelayHello, Binding{RelayKey: k.Public, URL: url, Nonce: nonce}.Bytes())
}

// Errors from VerifyHello and Local.Check.
var (
	ErrNoProof   = errors.New("relay sent no proof of its key in Hello")
	ErrBadProof  = errors.New("relay's proof of its key doesn't verify")
	ErrOtherKey  = errors.New("signed for another relay (relay key)")
	ErrOtherConn = errors.New("signed for another relay connection (nonce)")
	ErrOtherURL  = errors.New("signed for another relay URL")
	ErrBadURL    = errors.New("relay URL in the binding isn't canonical")
)

// VerifyHello checks a Hello's proof: a valid relay key, a URL that is
// empty or canonical, and the relay's signature over its binding.
func VerifyHello(h *wire.Hello) error {
	if h == nil || len(h.RelayKey) == 0 || len(h.RelaySignature) == 0 {
		return ErrNoProof
	}
	if !identity.ValidPublic(h.RelayKey) || len(h.Nonce) == 0 {
		return ErrBadProof
	}
	if h.Url != "" && !canonical(h.Url) {
		return ErrBadURL
	}
	b := Binding{RelayKey: h.RelayKey, URL: h.Url, Nonce: h.Nonce}
	if identity.Verify(h.RelayKey, identity.CtxRelayHello, b.Bytes(), h.RelaySignature) != nil {
		return ErrBadProof
	}
	return nil
}

// Local is a relay's view of one of its connections: its key, its own
// canonical URL ("" when it has none) and the connection's nonce.
type Local struct {
	Key   []byte
	URL   string
	Nonce []byte
}

// Check accepts a binding presented on this connection: the relay's own
// key, this connection's nonce, and the relay's URL (a relay without one
// takes any canonical relay URL: it can't tell which names reach it).
func (l Local) Check(relayKey []byte, url string, nonce []byte) error {
	switch {
	case len(l.Key) == 0 || !bytes.Equal(relayKey, l.Key):
		return ErrOtherKey
	case len(l.Nonce) == 0 || !bytes.Equal(nonce, l.Nonce):
		return ErrOtherConn
	case !canonical(url):
		return ErrBadURL
	case l.URL != "" && url != l.URL:
		return ErrOtherURL
	}
	return nil
}

// Binding is the binding a client on this connection signs, with url the
// URL it dialled.
func (l Local) Binding(url string) Binding {
	return Binding{RelayKey: l.Key, URL: url, Nonce: l.Nonce}
}

func canonical(url string) bool {
	c, err := relayurl.Canonical(url)
	return err == nil && c == url
}
