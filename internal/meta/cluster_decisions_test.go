package meta_test

import (
	"context"
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/meta"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

func admin(t *testing.T, n *clusterNode) *meta.Admin {
	t.Helper()
	a, err := meta.OpenAdmin(context.Background(), n.dir, meta.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

func listed(t *testing.T, n *clusterNode, c *community) bool {
	t.Helper()
	return n.get(t, "/v1/listings/"+identity.Encode(c.owner.Public), nil) == 200
}

// A member applies the moderation decisions of the members it follows,
// never over its own; decisions from members it doesn't follow are passed
// on but not applied.
func TestClusterDecisionsFollowed(t *testing.T) {
	ctx := context.Background()
	root, m2 := pair(t, nil)
	m2.cfg.FollowDecisions = []string{identity.Encode(root.srv.NodeKey())}
	m2.open(t)
	other := startClusterNode(t, t.TempDir(), nil) // trusted by m2, not followed
	trust(t, m2, other)
	trust(t, other, m2)

	x, w, v := newCommunity(t), newCommunity(t), newCommunity(t)
	for _, c := range []*community{x, w, v} {
		for _, n := range []*clusterNode{root, m2} {
			if code := n.post(t, "/v1/listings", c.listing("Listed", "", nil, 1), nil); code != 200 {
				t.Fatal(code)
			}
		}
	}
	// The root delists x: m2 follows.
	if _, err := admin(t, root).Delist(ctx, x.owner.Public, "spam"); err != nil {
		t.Fatal(err)
	}
	syncAll(t, root, m2, other)
	if listed(t, m2, x) || m2.post(t, "/v1/listings", x.listing("Back", "", nil, 2), nil) != 403 {
		t.Fatal("a followed delisting wasn't applied")
	}
	ds, err := admin(t, m2).Decisions(ctx)
	if err != nil || len(ds) != 1 || ds[0].Source != identity.Encode(root.srv.NodeKey()) || ds[0].Reason != "" {
		t.Fatalf("m2's decisions: %+v %v (the reason stays on the root)", ds, err)
	}

	// m2 delists w itself; the root's later delist and relist of w don't
	// lift m2's own decision.
	if _, err := admin(t, m2).Delist(ctx, w.owner.Public, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := admin(t, root).Delist(ctx, w.owner.Public, ""); err != nil {
		t.Fatal(err)
	}
	syncAll(t, root, m2, other)
	if _, err := admin(t, root).Relist(ctx, w.owner.Public); err != nil {
		t.Fatal(err)
	}
	syncAll(t, root, m2, other)
	if m2.post(t, "/v1/listings", w.listing("Back", "", nil, 2), nil) != 403 {
		t.Fatal("a followed relist lifted m2's own delisting")
	}

	// The root relists x: m2 follows.
	if _, err := admin(t, root).Relist(ctx, x.owner.Public); err != nil {
		t.Fatal(err)
	}
	syncAll(t, root, m2, other)
	if m2.post(t, "/v1/listings", x.listing("Back", "", nil, 3), nil) != 200 {
		t.Fatal("a followed relist wasn't applied")
	}

	// A member m2 doesn't follow delists v: passed on to the root, applied
	// nowhere else.
	if _, err := admin(t, other).Delist(ctx, v.owner.Public, ""); err != nil {
		t.Fatal(err)
	}
	syncAll(t, root, m2, other)
	if !listed(t, m2, v) || !listed(t, root, v) {
		t.Fatal("a decision from a member nobody follows was applied")
	}
	if listed(t, other, v) {
		t.Fatal("the deciding member still lists v")
	}
}

// A node whose keys live in node_key_dir records its admin decisions under
// that key, so the members that follow it apply them.
func TestClusterDecisionsWithNodeKeyDir(t *testing.T) {
	ctx := context.Background()
	keys := t.TempDir()
	if _, _, err := meta.NodeKeys(keys); err != nil {
		t.Fatal(err)
	}
	root := startClusterNode(t, t.TempDir(), func(c *meta.Config) { c.NodeKeyDir = keys })
	m2 := startClusterNode(t, t.TempDir(), nil)
	trust(t, root, m2)
	trust(t, m2, root)
	m2.cfg.FollowDecisions = []string{identity.Encode(root.srv.NodeKey())}
	m2.open(t)
	x := newCommunity(t)
	adm, err := meta.OpenAdmin(ctx, root.dir, meta.Config{NodeKeyDir: keys})
	if err != nil {
		t.Fatal(err)
	}
	defer adm.Close()
	if _, err := adm.Delist(ctx, x.owner.Public, ""); err != nil {
		t.Fatal(err)
	}
	syncAll(t, root, m2)
	if m2.post(t, "/v1/listings", x.listing("Back", "", nil, 1), nil) != 403 {
		t.Fatal("a decision made under node_key_dir wasn't followed")
	}
}

// Members of a maintainer set hand out link codes whose last digit is
// their position in the set, so two members never give out the same code.
func TestClusterNameplatesByIndex(t *testing.T) {
	op, _ := identity.Generate()
	opKey := identity.Encode(op.Public)
	a := startClusterNode(t, t.TempDir(), func(c *meta.Config) { c.ClusterOperatorKey = opKey })
	b := startClusterNode(t, t.TempDir(), func(c *meta.Config) { c.ClusterOperatorKey = opKey })
	set := signSet(t, op, 1, a, b)
	for _, n := range []*clusterNode{a, b} {
		if err := publishSet(t, n, set); err != nil {
			t.Fatal(err)
		}
	}
	numbers := func(n *clusterNode) []uint32 {
		var out []uint32
		for range 3 {
			var np wire.Nameplate
			if code := n.post(t, "/v1/nameplates", &wire.NameplateRequest{Relay: "wss://relay.example.invalid/ws"}, &np); code != 200 {
				t.Fatal(code)
			}
			out = append(out, np.Number)
		}
		return out
	}
	// Random six-digit numbers (TestNameplatesCantBeSniped), each ending
	// in the member's position.
	for i, n := range []*clusterNode{a, b} {
		got := numbers(n)
		for _, num := range got {
			if num%10 != uint32(i) || num < 100_000 || num > 999_999 || got[0] == got[1] || got[1] == got[2] {
				t.Fatalf("member %d's codes: %v", i, got)
			}
		}
	}
}
