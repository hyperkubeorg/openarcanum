package identity

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Owner sets (docs/protocol.md §3.1). A community is identified by its
// founder's owner key. Every later change to the set of owner keys is an
// OwnerChange signed by a key already in the set, numbered (seq) and
// chained to the previous change by its SHA-256, so anyone who knows the
// founder key can verify the current set from the chain alone. This is key
// delegation with Ed25519 signatures, nothing more.

// Signature contexts for owner sets. Python mirrors these in keys.py.
const (
	CtxOwnerChange = "openarcanum/owner-change/v1"
	CtxOwnerKey    = "openarcanum/owner-key/v1"
)

// Limits that keep verification cheap for anyone handed a chain.
const (
	MaxOwners       = 16
	MaxOwnerChanges = 256
)

// OwnerSet is a verified owner set.
type OwnerSet struct {
	Community []byte
	// Owners in the order they were added; the founder first while they
	// remain.
	Owners [][]byte
	// Member is the member key each added owner key belongs to (from its
	// OwnerAdd); the founder's isn't in the chain.
	Member map[string][]byte
	// Seq is the number of changes; Heads[i] is the hash after i changes
	// (Heads[0] is empty).
	Seq   uint64
	Heads [][]byte
	// ever holds every key that was ever an owner; a removed key can't be
	// added again.
	ever map[string]bool
}

// FounderSet is the owner set of a community with no changes yet.
func FounderSet(community []byte) *OwnerSet {
	return &OwnerSet{
		Community: append([]byte(nil), community...),
		Owners:    [][]byte{append([]byte(nil), community...)},
		Member:    map[string][]byte{},
		Heads:     [][]byte{nil},
		ever:      map[string]bool{string(community): true},
	}
}

// Head is the hash of the last change (empty for the founder set).
func (o *OwnerSet) Head() []byte { return o.Heads[o.Seq] }

// Has reports whether key is a current owner.
func (o *OwnerSet) Has(key []byte) bool {
	for _, k := range o.Owners {
		if bytes.Equal(k, key) {
			return true
		}
	}
	return false
}

// Extends reports whether o is prev or a later state of the same chain: a
// client that saw prev accepts o only then, so a removed owner can't present
// an older or forked set.
func (o *OwnerSet) Extends(prev *OwnerSet) bool {
	return prev == nil || (bytes.Equal(o.Community, prev.Community) && o.Seq >= prev.Seq &&
		bytes.Equal(o.Heads[prev.Seq], prev.Head()))
}

// ChangeHash is the hash a later change's prev field carries.
func ChangeHash(change []byte) []byte {
	h := sha256.Sum256(change)
	return h[:]
}

// VerifyOwnerChain verifies a chain from the founder key and returns the
// resulting owner set.
func VerifyOwnerChain(community []byte, chain []*wire.SignedOwnerChange) (*OwnerSet, error) {
	if !ValidPublic(community) {
		return nil, errors.New("identity: malformed community id")
	}
	if len(chain) > MaxOwnerChanges {
		return nil, errors.New("identity: owner chain is too long")
	}
	set := FounderSet(community)
	for _, sc := range chain {
		if _, err := set.Apply(sc); err != nil {
			return nil, err
		}
	}
	return set, nil
}

// Apply verifies one change against the set and applies it.
func (o *OwnerSet) Apply(sc *wire.SignedOwnerChange) (*wire.OwnerChange, error) {
	if sc == nil {
		return nil, errors.New("identity: missing owner change")
	}
	var c wire.OwnerChange
	if err := proto.Unmarshal(sc.Change, &c); err != nil {
		return nil, fmt.Errorf("identity: malformed owner change: %w", err)
	}
	switch {
	case !bytes.Equal(c.Community, o.Community):
		return nil, errors.New("identity: owner change is for another community")
	case c.Seq != o.Seq+1:
		return nil, fmt.Errorf("identity: owner change %d out of order (expected %d)", c.Seq, o.Seq+1)
	case !bytes.Equal(c.Prev, o.Head()):
		return nil, errors.New("identity: owner change doesn't follow the previous one")
	case !o.Has(c.Signer):
		return nil, errors.New("identity: owner change is signed by a key that isn't an owner")
	}
	if err := Verify(c.Signer, CtxOwnerChange, sc.Change, sc.Signature); err != nil {
		return nil, err
	}
	switch op := c.Op.(type) {
	case *wire.OwnerChange_Add:
		req, err := VerifyOwnerAdd(op.Add, o.Community)
		if err != nil {
			return nil, err
		}
		if o.ever[string(req.OwnerKey)] {
			return nil, errors.New("identity: that key is or was already an owner")
		}
		for k, m := range o.Member {
			if bytes.Equal(m, req.Member) && o.Has([]byte(k)) {
				return nil, errors.New("identity: that member already holds an owner key")
			}
		}
		if len(o.Owners) >= MaxOwners {
			return nil, fmt.Errorf("identity: a community can have at most %d owners", MaxOwners)
		}
		o.Owners = append(o.Owners, append([]byte(nil), req.OwnerKey...))
		o.Member[string(req.OwnerKey)] = append([]byte(nil), req.Member...)
		o.ever[string(req.OwnerKey)] = true
	case *wire.OwnerChange_Remove:
		if !o.Has(op.Remove) {
			return nil, errors.New("identity: that key isn't an owner")
		}
		if len(o.Owners) == 1 {
			return nil, errors.New("identity: the last owner can't be removed")
		}
		kept := o.Owners[:0:0]
		for _, k := range o.Owners {
			if !bytes.Equal(k, op.Remove) {
				kept = append(kept, k)
			}
		}
		o.Owners = kept
	default:
		return nil, errors.New("identity: unknown owner change")
	}
	o.Seq++
	o.Heads = append(o.Heads, ChangeHash(sc.Change))
	return &c, nil
}

// Clone returns an independent copy.
func (o *OwnerSet) Clone() *OwnerSet {
	c := &OwnerSet{
		Community: o.Community, Seq: o.Seq,
		Owners: append([][]byte(nil), o.Owners...),
		Heads:  append([][]byte(nil), o.Heads...),
		Member: make(map[string][]byte, len(o.Member)),
		ever:   make(map[string]bool, len(o.ever)),
	}
	for k, v := range o.Member {
		c.Member[k] = v
	}
	for k, v := range o.ever {
		c.ever[k] = v
	}
	return c
}

// RequestOwnerKey builds the signed request a member's client submits for
// the owner key it generated.
func RequestOwnerKey(ownerKey, member *Key, community []byte) (*wire.OwnerAdd, error) {
	req, err := proto.Marshal(&wire.OwnerKeyRequest{Community: community, OwnerKey: ownerKey.Public, Member: member.Public})
	if err != nil {
		return nil, err
	}
	return &wire.OwnerAdd{Request: req, OwnerSig: ownerKey.Sign(CtxOwnerKey, req), MemberSig: member.Sign(CtxOwnerKey, req)}, nil
}

// VerifyOwnerAdd checks both signatures on an owner key request.
func VerifyOwnerAdd(a *wire.OwnerAdd, community []byte) (*wire.OwnerKeyRequest, error) {
	if a == nil {
		return nil, errors.New("identity: missing owner key request")
	}
	var r wire.OwnerKeyRequest
	if err := proto.Unmarshal(a.Request, &r); err != nil {
		return nil, fmt.Errorf("identity: malformed owner key request: %w", err)
	}
	if !bytes.Equal(r.Community, community) {
		return nil, errors.New("identity: owner key request is for another community")
	}
	if !ValidPublic(r.OwnerKey) || !ValidPublic(r.Member) || bytes.Equal(r.OwnerKey, r.Member) {
		return nil, errors.New("identity: owner key request has malformed keys")
	}
	if err := Verify(r.OwnerKey, CtxOwnerKey, a.Request, a.OwnerSig); err != nil {
		return nil, err
	}
	if err := Verify(r.Member, CtxOwnerKey, a.Request, a.MemberSig); err != nil {
		return nil, err
	}
	return &r, nil
}

// SignOwnerChange makes the next change to set, signed by signer (which
// must be an owner). Exactly one of add and remove is set.
func SignOwnerChange(signer *Key, set *OwnerSet, add *wire.OwnerAdd, remove []byte) (*wire.SignedOwnerChange, error) {
	c := &wire.OwnerChange{
		Community: set.Community, Seq: set.Seq + 1, Prev: set.Head(), Signer: signer.Public, At: time.Now().UnixMilli(),
	}
	switch {
	case add != nil && remove == nil:
		c.Op = &wire.OwnerChange_Add{Add: add}
	case add == nil && remove != nil:
		c.Op = &wire.OwnerChange_Remove{Remove: remove}
	default:
		return nil, errors.New("identity: an owner change adds or removes one key")
	}
	b, err := proto.Marshal(c)
	if err != nil {
		return nil, err
	}
	return &wire.SignedOwnerChange{Change: b, Signature: signer.Sign(CtxOwnerChange, b)}, nil
}
