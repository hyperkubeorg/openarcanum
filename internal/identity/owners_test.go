package identity

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// chainFixture is a community with a founder and helpers to grow its chain.
type chainFixture struct {
	t       *testing.T
	founder *Key
	set     *OwnerSet
	chain   []*wire.SignedOwnerChange
}

func newChain(t *testing.T) *chainFixture {
	f, _ := Generate()
	return &chainFixture{t: t, founder: f, set: FounderSet(f.Public)}
}

func (c *chainFixture) apply(sc *wire.SignedOwnerChange) {
	c.t.Helper()
	if _, err := c.set.Apply(sc); err != nil {
		c.t.Fatal(err)
	}
	c.chain = append(c.chain, sc)
}

// add has signer sign a new owner key in for a fresh member.
func (c *chainFixture) add(signer *Key) (owner, member *Key) {
	c.t.Helper()
	owner, _ = Generate()
	member, _ = Generate()
	req, err := RequestOwnerKey(owner, member, c.founder.Public)
	if err != nil {
		c.t.Fatal(err)
	}
	sc, err := SignOwnerChange(signer, c.set, req, nil)
	if err != nil {
		c.t.Fatal(err)
	}
	c.apply(sc)
	return owner, member
}

func (c *chainFixture) remove(signer *Key, key []byte) {
	c.t.Helper()
	sc, err := SignOwnerChange(signer, c.set, nil, key)
	if err != nil {
		c.t.Fatal(err)
	}
	c.apply(sc)
}

func TestOwnerChainAddAndRemove(t *testing.T) {
	c := newChain(t)
	second, member := c.add(c.founder)
	third, _ := c.add(second) // any owner can add owners
	set, err := VerifyOwnerChain(c.founder.Public, c.chain)
	if err != nil {
		t.Fatal(err)
	}
	if set.Seq != 2 || len(set.Owners) != 3 || !set.Has(second.Public) || !set.Has(third.Public) {
		t.Fatalf("set after two adds: %+v", set)
	}
	if string(set.Member[string(second.Public)]) != string(member.Public) {
		t.Fatal("added owner not bound to its member")
	}
	// The second owner removes the founder; the community id stays.
	c.remove(second, c.founder.Public)
	set, err = VerifyOwnerChain(c.founder.Public, c.chain)
	if err != nil {
		t.Fatal(err)
	}
	if set.Has(c.founder.Public) || len(set.Owners) != 2 || string(set.Community) != string(c.founder.Public) {
		t.Fatalf("set after removing the founder: %+v", set)
	}
	// The removed founder can no longer sign changes.
	byFounder, _ := SignOwnerChange(c.founder, set, nil, third.Public)
	if _, err := set.Clone().Apply(byFounder); err == nil {
		t.Fatal("removed owner's change accepted")
	}
	// An owner can remove themselves while another remains, not the last.
	c.remove(third, third.Public)
	last, _ := SignOwnerChange(second, c.set, nil, second.Public)
	if _, err := c.set.Clone().Apply(last); err == nil || !strings.Contains(err.Error(), "last owner") {
		t.Fatalf("last owner removed: %v", err)
	}
	// A removed key can't come back.
	member2, _ := Generate()
	back, _ := RequestOwnerKey(third, member2, c.founder.Public)
	again, _ := SignOwnerChange(second, c.set, back, nil)
	if _, err := c.set.Clone().Apply(again); err == nil {
		t.Fatal("removed key added again")
	}
}

func TestOwnerChainRejectsBadChanges(t *testing.T) {
	c := newChain(t)
	second, _ := c.add(c.founder)
	stranger, _ := Generate()
	member, _ := Generate()
	newKey, _ := Generate()
	req, _ := RequestOwnerKey(newKey, member, c.founder.Public)

	check := func(name string, sc *wire.SignedOwnerChange) {
		t.Helper()
		if _, err := c.set.Clone().Apply(sc); err == nil {
			t.Errorf("%s accepted", name)
		}
		if _, err := VerifyOwnerChain(c.founder.Public, append(append([]*wire.SignedOwnerChange(nil), c.chain...), sc)); err == nil {
			t.Errorf("%s accepted in a chain", name)
		}
	}
	// Signed by a key that isn't an owner.
	bad, _ := SignOwnerChange(stranger, c.set, req, nil)
	check("non-owner signer", bad)
	// Forged signature: names an owner as signer, signed by someone else.
	forged, _ := SignOwnerChange(second, c.set, req, nil)
	forged.Signature = stranger.Sign(CtxOwnerChange, forged.Change)
	check("forged signature", forged)
	// Altered after signing.
	altered, _ := SignOwnerChange(second, c.set, req, nil)
	altered.Change[len(altered.Change)-1] ^= 1
	check("altered change", altered)
	// Wrong sequence number and wrong previous hash.
	for _, mut := range []func(*wire.OwnerChange){
		func(o *wire.OwnerChange) { o.Seq = 5 },
		func(o *wire.OwnerChange) { o.Prev = make([]byte, 32) },
		func(o *wire.OwnerChange) { o.Community = stranger.Public },
	} {
		o := &wire.OwnerChange{Community: c.founder.Public, Seq: c.set.Seq + 1, Prev: c.set.Head(), Signer: second.Public, Op: &wire.OwnerChange_Add{Add: req}}
		mut(o)
		b, _ := proto.Marshal(o)
		check("misplaced change", &wire.SignedOwnerChange{Change: b, Signature: second.Sign(CtxOwnerChange, b)})
	}
	// Owner key request without proof of possession, or not signed by the
	// member, or for another community.
	noProof := proto.Clone(req).(*wire.OwnerAdd)
	noProof.OwnerSig = member.Sign(CtxOwnerKey, noProof.Request)
	sc, _ := SignOwnerChange(second, c.set, noProof, nil)
	check("request without proof of possession", sc)
	noConsent := proto.Clone(req).(*wire.OwnerAdd)
	noConsent.MemberSig = stranger.Sign(CtxOwnerKey, noConsent.Request)
	sc, _ = SignOwnerChange(second, c.set, noConsent, nil)
	check("request without the member's signature", sc)
	elsewhere, _ := RequestOwnerKey(newKey, member, stranger.Public)
	sc, _ = SignOwnerChange(second, c.set, elsewhere, nil)
	check("request for another community", sc)
	// Removing a key that isn't an owner.
	sc, _ = SignOwnerChange(second, c.set, nil, stranger.Public)
	check("removing a non-owner", sc)
}

func TestOwnerSetExtends(t *testing.T) {
	c := newChain(t)
	second, _ := c.add(c.founder)
	old := c.set.Clone()
	c.add(second)
	if !c.set.Extends(old) || old.Extends(c.set) || !c.set.Extends(c.set) || !c.set.Extends(nil) {
		t.Fatal("Extends doesn't follow the chain")
	}
	// A fork at the same seq (the founder removing the second owner instead)
	// doesn't extend the set a client saw.
	fork := old.Clone()
	sc, _ := SignOwnerChange(c.founder, fork, nil, second.Public)
	if _, err := fork.Apply(sc); err != nil {
		t.Fatal(err)
	}
	if fork.Extends(c.set) || c.set.Extends(fork) {
		t.Fatal("forked owner set accepted as a continuation")
	}
}

func TestServerCertUnderOwnerSet(t *testing.T) {
	c := newChain(t)
	second, _ := c.add(c.founder)
	server, _ := Generate()
	// The added owner certifies a server key; it verifies against the
	// community id through the chain.
	sc, err := IssueServerCert(second, server.Public, 2, c.set, c.chain)
	if err != nil {
		t.Fatal(err)
	}
	v, err := VerifyCert(sc, server.Public, c.founder.Public)
	if err != nil {
		t.Fatalf("co-owner cert rejected: %v", err)
	}
	if v.Owners.Seq != 1 || string(v.Cert.OwnerKey) != string(second.Public) {
		t.Fatalf("verified cert: %+v", v.Cert)
	}
	// Without the chain, or with a chain that doesn't end where the cert
	// says, it doesn't verify.
	bare := proto.Clone(sc).(*wire.SignedServerCert)
	bare.OwnerChain = nil
	if _, err := VerifyCert(bare, server.Public, c.founder.Public); err == nil {
		t.Fatal("cert accepted without its owner chain")
	}
	// After the second owner is removed, a cert they sign (even claiming the
	// old set, with the newer chain attached) is refused.
	c.remove(c.founder, second.Public)
	stale := proto.Clone(sc).(*wire.SignedServerCert)
	stale.OwnerChain = c.chain
	if _, err := VerifyCert(stale, server.Public, c.founder.Public); err == nil {
		t.Fatal("cert from a removed owner accepted with the current chain")
	}
	if _, err := IssueServerCert(second, server.Public, 3, c.set, c.chain); err == nil {
		t.Fatal("removed owner issued a cert")
	}
	// A key that was never an owner can't certify, even with a valid chain.
	stranger, _ := Generate()
	body, _ := proto.Marshal(&wire.ServerCert{OwnerKey: stranger.Public, ServerKey: server.Public, Seq: 9, Community: c.founder.Public, OwnerSeq: c.set.Seq, OwnerHead: c.set.Head()})
	forged := &wire.SignedServerCert{Cert: body, Signature: stranger.Sign(CtxServerCert, body), OwnerChain: c.chain}
	if _, err := VerifyCert(forged, server.Public, c.founder.Public); err == nil {
		t.Fatal("cert from a non-owner accepted")
	}
	// A cert signed by a current owner but with a forged signature fails.
	body2, _ := proto.Marshal(&wire.ServerCert{OwnerKey: c.founder.Public, ServerKey: server.Public, Seq: 9, Community: c.founder.Public, OwnerSeq: c.set.Seq, OwnerHead: c.set.Head()})
	if _, err := VerifyCert(&wire.SignedServerCert{Cert: body2, Signature: stranger.Sign(CtxServerCert, body2), OwnerChain: c.chain}, server.Public, c.founder.Public); err == nil {
		t.Fatal("forged cert signature accepted")
	}
}

func TestOwnerChainLimits(t *testing.T) {
	c := newChain(t)
	for len(c.set.Owners) < MaxOwners {
		c.add(c.founder)
	}
	owner, _ := Generate()
	member, _ := Generate()
	req, _ := RequestOwnerKey(owner, member, c.founder.Public)
	sc, _ := SignOwnerChange(c.founder, c.set, req, nil)
	if _, err := c.set.Clone().Apply(sc); err == nil {
		t.Fatal("more than MaxOwners owners")
	}
	long := make([]*wire.SignedOwnerChange, MaxOwnerChanges+1)
	if _, err := VerifyOwnerChain(c.founder.Public, long); err == nil {
		t.Fatal("over-long chain accepted")
	}
}
