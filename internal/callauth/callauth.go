// Package callauth signs and checks call grants and moderator controls
// (docs/protocol.md §12): the community server signs them, relays check them.
package callauth

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// CallID is the id of a call run by the community server with serverKey:
// SHA-256("openarcanum/call-id/v1" || 0x00 || server_key || call_nonce)[:16].
// A grant for a call must be signed by the key the id commits to.
func CallID(serverKey, nonce []byte) []byte {
	h := sha256.New()
	h.Write([]byte("openarcanum/call-id/v1\x00"))
	h.Write(serverKey)
	h.Write(nonce)
	return h.Sum(nil)[:16]
}

// VerifyGrant checks a grant's signature, that its call id commits to the
// signing key, and that it hasn't expired.
func VerifyGrant(sg *wire.SignedCallGrant, now time.Time) (*wire.CallGrant, error) {
	if sg == nil {
		return nil, errors.New("no grant")
	}
	g := &wire.CallGrant{}
	if err := proto.Unmarshal(sg.Grant, g); err != nil {
		return nil, errors.New("malformed grant")
	}
	if len(g.ServerKey) != 32 || len(g.CallNonce) != 16 || len(g.Participant) != 16 {
		return nil, errors.New("malformed grant")
	}
	if err := identity.Verify(g.ServerKey, identity.CtxCallGrant, sg.Grant, sg.Signature); err != nil {
		return nil, errors.New("grant signature doesn't verify")
	}
	if !bytes.Equal(g.CallId, CallID(g.ServerKey, g.CallNonce)) {
		return nil, errors.New("grant is for a call another server runs")
	}
	if now.UnixMilli() >= g.ExpiresAt {
		return nil, errors.New("grant has expired")
	}
	if g.ExpiresAt > now.Add(MaxGrantLifetime).UnixMilli() {
		return nil, errors.New("grant is valid for too long")
	}
	return g, nil
}

// MaxControlList bounds each list in a control (the community server lists
// at most 99 participants and 500 who left).
const MaxControlList = 1000

// MaxGrantLifetime bounds how long a grant can be used to join, so the relay
// only needs to remember removals that long.
const MaxGrantLifetime = 10 * time.Minute

// VerifyControl checks a control against the server key that runs the call.
func VerifyControl(sc *wire.SignedCallControl, serverKey []byte) (*wire.CallControl, error) {
	if sc == nil {
		return nil, errors.New("no control")
	}
	if err := identity.Verify(serverKey, identity.CtxCallControl, sc.Control, sc.Signature); err != nil {
		return nil, errors.New("control signature doesn't verify")
	}
	c := &wire.CallControl{}
	if err := proto.Unmarshal(sc.Control, c); err != nil {
		return nil, errors.New("malformed control")
	}
	for _, list := range [][][]byte{c.Muted, c.Deafened, c.Removed, c.NoVideo, c.NoScreen} {
		if len(list) > MaxControlList {
			return nil, errors.New("control lists too many participants")
		}
		for _, p := range list {
			if len(p) != 16 {
				return nil, errors.New("control names a malformed participant")
			}
		}
	}
	return c, nil
}

// SignGrant and SignControl are what the community server uses.
func SignGrant(k *identity.Key, g *wire.CallGrant) (*wire.SignedCallGrant, error) {
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(g)
	if err != nil {
		return nil, err
	}
	return &wire.SignedCallGrant{Grant: raw, Signature: k.Sign(identity.CtxCallGrant, raw)}, nil
}

func SignControl(k *identity.Key, c *wire.CallControl) (*wire.SignedCallControl, error) {
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(c)
	if err != nil {
		return nil, err
	}
	return &wire.SignedCallControl{Control: raw, Signature: k.Sign(identity.CtxCallControl, raw)}, nil
}

// ControlCallID reads which call a control is for, before its signature is
// checked (the call says which server key must have signed it).
func ControlCallID(sc *wire.SignedCallControl) ([]byte, error) {
	ctl := &wire.CallControl{}
	if sc == nil || proto.Unmarshal(sc.Control, ctl) != nil || len(ctl.CallId) != 16 {
		return nil, errors.New("malformed control")
	}
	return ctl.CallId, nil
}
