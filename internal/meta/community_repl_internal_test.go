package meta

import (
	"bytes"
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// A member's community tombstone drops a community here only if, by this
// node's own rule, the community is dead: its last sign of life older than
// community_reap_after and nothing newer seen here. A tombstone dated now
// (or in the future) from a misbehaving member drops nothing, and one
// naming a community this node can't tie to the route id doesn't block
// that community.
func TestForgedCommunityTombstonesRefused(t *testing.T) {
	s := openTestNode(t, nil, nil)
	ctx := context.Background()
	sr, id := testRoute(t)
	rt, cert, err := verifyRoute(sr)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := proto.Marshal(sr)
	if err := s.storeRoute(ctx, rt, cert, raw); err != nil {
		t.Fatal(err)
	}
	community := cert.Owners.Community
	held := func() bool {
		var n int
		return s.db.QueryRowContext(ctx, `SELECT 1 FROM routes WHERE route_id = ?`, id).Scan(&n) == nil
	}
	now := s.now()
	for i, ts := range []*wire.PeerCommunityTombstone{
		// "Last seen" after anything this node saw.
		{RouteId: id, Community: community, LastSeen: now.Add(30 * time.Second).UnixMilli(), ReapedAt: now.Add(time.Minute).UnixMilli()},
		{RouteId: id, Community: community, LastSeen: now.UnixMilli(), ReapedAt: now.UnixMilli()},
		// Reaped "tomorrow".
		{RouteId: id, Community: community, LastSeen: now.Add(-8 * 24 * time.Hour).UnixMilli(), ReapedAt: now.Add(time.Hour).UnixMilli()},
	} {
		if applied, err := s.applyCommunityTombstone(ctx, ts); applied || err != nil || !held() {
			t.Fatalf("forged tombstone %d: applied %v, %v; route held %v", i, applied, err, held())
		}
	}
	// A tombstone for a route id this node never held, naming some other
	// community: kept, but it doesn't bind that community here.
	victim, _ := identity.Generate()
	other := bytes.Repeat([]byte{9}, 16)
	ts := &wire.PeerCommunityTombstone{RouteId: other, Community: victim.Public, LastSeen: now.Add(-8 * 24 * time.Hour).UnixMilli(), ReapedAt: now.UnixMilli()}
	if _, err := s.applyCommunityTombstone(ctx, ts); err != nil {
		t.Fatal(err)
	}
	if dead, err := tombstoned(ctx, s.db, `community`, victim.Public); dead || err != nil {
		t.Fatalf("an unverified tombstone blocks a community: %v %v", dead, err)
	}
	// Nor does it retire the route id: a heartbeat for it is unknown (not
	// 410), and the route can be posted as new.
	if dead, err := tombstoned(ctx, s.db, `route_id`, other); dead || err != nil {
		t.Fatalf("an unbound tombstone retires a route: %v %v", dead, err)
	}
	tx, _ := s.db.BeginTx(ctx, nil)
	if err := s.routeTombstone(ctx, tx, &wire.Route{RouteId: other}, victim.Public); err != nil {
		t.Fatalf("posting the route anew: %v", err)
	}
	_ = tx.Rollback()
	// A real one: the community's last sign of life is older than the
	// reap window here too.
	s.SetClock(func() time.Time { return now.Add(8 * 24 * time.Hour) })
	real := &wire.PeerCommunityTombstone{RouteId: id, Community: community, LastSeen: now.UnixMilli(), ReapedAt: now.Add(8 * 24 * time.Hour).UnixMilli()}
	if applied, err := s.applyCommunityTombstone(ctx, real); !applied || err != nil || held() {
		t.Fatalf("a real tombstone: applied %v, %v; route held %v", applied, err, held())
	}
}
