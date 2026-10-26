package identity

import (
	"bytes"
	"errors"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// IssueServerCert signs a certificate from owner for serverKey under the
// owner set (nil: a founder with no owner changes yet). The set's chain
// travels with the cert so anyone holding the community id can verify it.
func IssueServerCert(owner *Key, serverKey []byte, seq uint64, set *OwnerSet, chain []*wire.SignedOwnerChange) (*wire.SignedServerCert, error) {
	if set == nil {
		set = FounderSet(owner.Public)
	}
	if !set.Has(owner.Public) {
		return nil, errors.New("identity: that key isn't an owner of the community")
	}
	body, err := proto.Marshal(&wire.ServerCert{
		OwnerKey:  owner.Public,
		ServerKey: serverKey,
		IssuedAt:  time.Now().Unix(),
		Seq:       seq,
		Community: set.Community,
		OwnerSeq:  set.Seq,
		OwnerHead: set.Head(),
	})
	if err != nil {
		return nil, err
	}
	return &wire.SignedServerCert{Cert: body, Signature: owner.Sign(CtxServerCert, body), OwnerChain: chain}, nil
}

// CertCommunity is the community a cert names (older certs: their signer).
func CertCommunity(c *wire.ServerCert) []byte {
	if len(c.Community) == 0 {
		return c.OwnerKey
	}
	return c.Community
}

// VerifiedCert is a server cert checked against its owner chain.
type VerifiedCert struct {
	Cert   *wire.ServerCert
	Owners *OwnerSet
}

// VerifyCert checks that sc certifies serverKey and is signed by a key in
// the owner set its chain proves, and that the chain ends exactly where the
// cert says. If community is non-nil the cert must be for that community.
func VerifyCert(sc *wire.SignedServerCert, serverKey, community []byte) (*VerifiedCert, error) {
	if sc == nil {
		return nil, errors.New("identity: missing server cert")
	}
	var c wire.ServerCert
	if err := proto.Unmarshal(sc.Cert, &c); err != nil {
		return nil, err
	}
	cid := CertCommunity(&c)
	if community != nil && !bytes.Equal(cid, community) {
		return nil, errors.New("identity: cert is for a different community")
	}
	set, err := VerifyOwnerChain(cid, sc.OwnerChain)
	if err != nil {
		return nil, err
	}
	if c.OwnerSeq != set.Seq || !bytes.Equal(c.OwnerHead, set.Head()) {
		return nil, errors.New("identity: cert doesn't match its owner chain")
	}
	if !set.Has(c.OwnerKey) {
		return nil, errors.New("identity: cert is signed by a key that isn't an owner")
	}
	if err := Verify(c.OwnerKey, CtxServerCert, sc.Cert, sc.Signature); err != nil {
		return nil, err
	}
	if !bytes.Equal(c.ServerKey, serverKey) {
		return nil, errors.New("identity: cert is for a different server key")
	}
	return &VerifiedCert{Cert: &c, Owners: set}, nil
}

// VerifyServerCert is VerifyCert returning only the cert.
func VerifyServerCert(sc *wire.SignedServerCert, serverKey, community []byte) (*wire.ServerCert, error) {
	v, err := VerifyCert(sc, serverKey, community)
	if err != nil {
		return nil, err
	}
	return v.Cert, nil
}
