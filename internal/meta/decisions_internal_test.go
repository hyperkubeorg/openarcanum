package meta

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// A moderation decision passed on by cluster members is signed by the node
// where it was made: a member can't forge another node's decisions (here
// the root's, which this node follows), and a decision dated far in the
// future (which would block every later change) is refused.
func TestPeerDecisionsAreSigned(t *testing.T) {
	s := openTestNode(t, nil, nil)
	root, _ := identity.Generate()
	s.cfg.FollowDecisions = []string{identity.Encode(root.Public)}
	s.cfg.TrustedPeers = []string{"http://root.example#" + identity.Encode(root.Public)}
	ctx := context.Background()
	community, _ := identity.Generate()
	delisted := func() bool {
		gone, err := s.communityDelisted(ctx, s.db, community.Public)
		if err != nil {
			t.Fatal(err)
		}
		return gone
	}
	now := time.Now().UnixMilli()
	d := &wire.PeerDecision{Origin: root.Public, Kind: kindCommunity, Key: identity.Encode(community.Public), Action: actionDelist, At: now}

	// Claimed to be the root's, unsigned or signed by another member.
	member, _ := identity.Generate()
	forged := []*wire.PeerDecision{
		{Origin: d.Origin, Kind: d.Kind, Key: d.Key, Action: d.Action, At: d.At},
		{Origin: d.Origin, Kind: d.Kind, Key: d.Key, Action: d.Action, At: d.At, Signature: member.Sign(ctxPeerDecision, decisionBytes(d))},
	}
	for i, f := range forged {
		if applied, err := s.applyDecision(ctx, f); applied || err != nil || delisted() {
			t.Fatalf("forged decision %d: applied %v, %v", i, applied, err)
		}
	}
	// Signed by the root but dated near the end of time.
	future := &wire.PeerDecision{Origin: d.Origin, Kind: d.Kind, Key: d.Key, Action: d.Action, At: math.MaxInt64}
	future.Signature = root.Sign(ctxPeerDecision, decisionBytes(future))
	if applied, _ := s.applyDecision(ctx, future); applied || delisted() {
		t.Fatal("a decision from the far future was applied")
	}
	// The root's own, signed: applied.
	d.Signature = root.Sign(ctxPeerDecision, decisionBytes(d))
	if applied, err := s.applyDecision(ctx, d); !applied || err != nil || !delisted() {
		t.Fatalf("the root's signed decision: applied %v, %v", applied, err)
	}
	// It is passed on with its signature.
	e, err := s.decisionEntry(ctx, decisionKey(d.Origin, d.Kind, d.Key))
	if err != nil || e.GetDecision() == nil || verifyDecision(e.GetDecision()) != nil {
		t.Fatalf("passed-on decision: %v %v", e, err)
	}
}

// Only decisions of origins this node trusts now are kept and passed on:
// a signed decision from any other key is dropped, a decision's log entry
// is pruned like every other entry, and the decisions of an origin that
// lost its trust are dropped at the next expiry.
func TestPeerDecisionsNeedATrustedOrigin(t *testing.T) {
	s := openTestNode(t, nil, nil)
	ctx := context.Background()
	stranger, _ := identity.Generate()
	member, _ := identity.Generate()
	s.cfg.TrustedPeers = []string{"http://member.example#" + identity.Encode(member.Public)}
	community, _ := identity.Generate()
	now := time.Now().UnixMilli()
	decision := func(origin *identity.Key) *wire.PeerDecision {
		d := &wire.PeerDecision{Origin: origin.Public, Kind: kindCommunity, Key: identity.Encode(community.Public), Action: actionDelist, At: now}
		d.Signature = origin.Sign(ctxPeerDecision, decisionBytes(d))
		return d
	}
	held := func(origin *identity.Key) bool {
		e, err := s.decisionEntry(ctx, decisionKey(origin.Public, kindCommunity, identity.Encode(community.Public)))
		if err != nil {
			t.Fatal(err)
		}
		return e != nil
	}
	if applied, err := s.applyDecision(ctx, decision(stranger)); applied || err != nil || held(stranger) {
		t.Fatalf("a stranger's decision: applied %v, %v", applied, err)
	}
	if applied, err := s.applyDecision(ctx, decision(member)); !applied || err != nil || !held(member) {
		t.Fatalf("a member's decision: applied %v, %v", applied, err)
	}
	var at int64
	if err := s.db.QueryRowContext(ctx, `SELECT at FROM peer_log WHERE kind = ?`, logDecision).Scan(&at); err != nil {
		t.Fatal(err)
	}
	if at != time.UnixMilli(now).Add(decisionLogTTL).UnixMilli() {
		t.Fatalf("decision log entry pruned at %d", at)
	}
	s.cfg.TrustedPeers = nil
	s.Expire(ctx)
	if held(member) {
		t.Fatal("the decisions of a member no longer trusted are still passed on")
	}
}
