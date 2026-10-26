package meta_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/meta"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Community status, reaping and tombstones (docs/protocol.md §7.4).

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }
func startClockedNode(t *testing.T, c *clock, mutate func(*meta.Config)) *node {
	t.Helper()
	n := startNode(t, mutate)
	n.srv.SetClock(c.now)
	return n
}

func (c *community) routeAt(at time.Time, reregistered int64) *wire.SignedRoute {
	r, _ := proto.Marshal(&wire.Route{RouteId: c.route, ServerKey: c.server.Public, Relays: []string{"ws://r/ws"},
		Seq: uint64(at.UnixMilli()), PublishedAt: at.UnixMilli(), ReregisteredAt: reregistered})
	return &wire.SignedRoute{Route: r, Signature: c.server.Sign(identity.CtxRoute, r), Cert: c.cert}
}

func (c *community) listingAt(at time.Time, name string) *wire.SignedListing {
	l, _ := proto.Marshal(&wire.Listing{Community: c.owner.Public, ServerKey: c.server.Public, RouteId: c.route, Name: name,
		Seq: uint64(at.UnixMilli()), PublishedAt: at.UnixMilli(), Relays: []string{"ws://r/ws"}})
	return &wire.SignedListing{Listing: l, Signature: c.server.Sign(identity.CtxListing, l), Cert: c.cert}
}

func (c *community) heartbeat(key *identity.Key, at time.Time, attached, wanted uint32, stopping bool) *wire.SignedCommunityHeartbeat {
	hb, _ := proto.Marshal(&wire.CommunityHeartbeat{RouteId: c.route, ServerKey: key.Public, At: at.UnixMilli(),
		RelaysAttached: attached, RelaysWanted: wanted, Stopping: stopping})
	return &wire.SignedCommunityHeartbeat{Heartbeat: hb, Signature: key.Sign(identity.CtxCommunityHeartbeat, hb)}
}

func (n *node) status(t *testing.T, c *community) (*wire.CommunityStatus, int) {
	t.Helper()
	var st wire.CommunityStatus
	code := n.get(t, "/v1/communities/"+identity.Encode(c.route)+"/status", &st)
	return &st, code
}

func wantState(t *testing.T, n *node, c *community, want wire.CommunityState) *wire.CommunityStatus {
	t.Helper()
	st, code := n.status(t, c)
	if code != 200 || st.State != want {
		t.Fatalf("status = %d %v, want %v", code, st.State, want)
	}
	return st
}

func TestCommunityHeartbeatStates(t *testing.T) {
	clk := &clock{time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	n := startClockedNode(t, clk, nil)
	c := newCommunity(t)
	if _, code := n.status(t, c); code != http.StatusNotFound {
		t.Fatalf("unknown community: %d", code)
	}
	// A heartbeat needs the route first.
	if code := n.post(t, "/v1/communities/heartbeat", c.heartbeat(c.server, clk.t, 2, 2, false), nil); code != http.StatusNotFound {
		t.Fatalf("heartbeat before the route: %d", code)
	}
	if code := n.post(t, "/v1/routes", c.routeAt(clk.t, 0), nil); code != 200 {
		t.Fatalf("route: %d", code)
	}
	// Route but no heartbeat yet: no state, last seen from the route.
	if st := wantState(t, n, c, wire.CommunityState_COMMUNITY_STATE_UNSPECIFIED); st.LastSeen != clk.t.UnixMilli() {
		t.Fatalf("last seen = %d", st.LastSeen)
	}
	if code := n.post(t, "/v1/communities/heartbeat", c.heartbeat(c.server, clk.t, 2, 2, false), nil); code != 200 {
		t.Fatalf("heartbeat: %d", code)
	}
	st := wantState(t, n, c, wire.CommunityState_COMMUNITY_STATE_ONLINE)
	if st.Heartbeat == nil || st.ReapAfterSeconds != uint64(7*24*3600) {
		t.Fatalf("status = %v", st)
	}
	var hb wire.CommunityHeartbeat
	if err := proto.Unmarshal(st.Heartbeat.Heartbeat, &hb); err != nil || identity.Verify(c.server.Public, identity.CtxCommunityHeartbeat, st.Heartbeat.Heartbeat, st.Heartbeat.Signature) != nil {
		t.Fatal("the served heartbeat doesn't verify against the server key")
	}

	// Wrong signer, another key than the route's, a forged signature, the future.
	other, _ := identity.Generate()
	if code := n.post(t, "/v1/communities/heartbeat", c.heartbeat(other, clk.t.Add(time.Second), 2, 2, false), nil); code != http.StatusConflict {
		t.Fatalf("heartbeat from another server key: %d", code)
	}
	forged := c.heartbeat(c.server, clk.t.Add(time.Second), 2, 2, true)
	forged.Signature[0] ^= 1
	if code := n.post(t, "/v1/communities/heartbeat", forged, nil); code != http.StatusForbidden {
		t.Fatalf("forged heartbeat: %d", code)
	}
	if code := n.post(t, "/v1/communities/heartbeat", c.heartbeat(c.server, clk.t.Add(time.Hour), 2, 2, false), nil); code != http.StatusBadRequest {
		t.Fatalf("heartbeat from the future: %d", code)
	}

	// One of two relays: degraded; none: offline (relays down).
	clk.advance(time.Second)
	n.post(t, "/v1/communities/heartbeat", c.heartbeat(c.server, clk.t, 1, 2, false), nil)
	wantState(t, n, c, wire.CommunityState_COMMUNITY_STATE_DEGRADED)
	clk.advance(time.Second)
	n.post(t, "/v1/communities/heartbeat", c.heartbeat(c.server, clk.t, 0, 2, false), nil)
	wantState(t, n, c, wire.CommunityState_COMMUNITY_STATE_OFFLINE)
	// An older heartbeat doesn't replace a newer one.
	n.post(t, "/v1/communities/heartbeat", c.heartbeat(c.server, clk.t.Add(-time.Minute), 2, 2, false), nil)
	wantState(t, n, c, wire.CommunityState_COMMUNITY_STATE_OFFLINE)
	clk.advance(time.Second)
	n.post(t, "/v1/communities/heartbeat", c.heartbeat(c.server, clk.t, 2, 2, false), nil)
	wantState(t, n, c, wire.CommunityState_COMMUNITY_STATE_ONLINE)

	// Silent past community_offline_after: offline, with the last time seen.
	clk.advance(3*time.Minute + time.Second)
	st = wantState(t, n, c, wire.CommunityState_COMMUNITY_STATE_OFFLINE)
	if st.LastSeen != clk.t.Add(-3*time.Minute-time.Second).UnixMilli() {
		t.Fatalf("last seen = %v", time.UnixMilli(st.LastSeen))
	}
	// A clean shutdown is offline at once.
	n.post(t, "/v1/communities/heartbeat", c.heartbeat(c.server, clk.t, 2, 2, false), nil)
	wantState(t, n, c, wire.CommunityState_COMMUNITY_STATE_ONLINE)
	clk.advance(time.Second)
	n.post(t, "/v1/communities/heartbeat", c.heartbeat(c.server, clk.t, 2, 2, true), nil)
	wantState(t, n, c, wire.CommunityState_COMMUNITY_STATE_OFFLINE)
}

func TestSearchCarriesCommunityStatus(t *testing.T) {
	clk := &clock{time.Now()}
	n := startClockedNode(t, clk, nil)
	c := newCommunity(t)
	n.post(t, "/v1/routes", c.routeAt(clk.t, 0), nil)
	if code := n.post(t, "/v1/listings", c.listingAt(clk.t, "Night Owls"), nil); code != 200 {
		t.Fatalf("listing: %d", code)
	}
	n.post(t, "/v1/communities/heartbeat", c.heartbeat(c.server, clk.t, 1, 1, false), nil)
	var res wire.SearchResponse
	if code := n.get(t, "/v1/search?q=owls", &res); code != 200 || len(res.Listings) != 1 || len(res.Statuses) != 1 {
		t.Fatalf("search: %d, %d listings, %d statuses", code, len(res.Listings), len(res.Statuses))
	}
	if st := res.Statuses[0]; string(st.RouteId) != string(c.route) || st.State != wire.CommunityState_COMMUNITY_STATE_ONLINE {
		t.Fatalf("status in search = %v", st)
	}
	clk.advance(10 * time.Minute)
	n.get(t, "/v1/search?q=owls", &res)
	if res.Statuses[0].State != wire.CommunityState_COMMUNITY_STATE_OFFLINE {
		t.Fatalf("status after silence = %v", res.Statuses[0].State)
	}
}

func TestReapingTerminatesAndNeedsReregistration(t *testing.T) {
	clk := &clock{time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	n := startClockedNode(t, clk, nil)
	c := newCommunity(t)
	start := clk.t
	n.post(t, "/v1/routes", c.routeAt(start, 0), nil)
	n.post(t, "/v1/listings", c.listingAt(start, "Gardeners"), nil)
	clk.advance(time.Hour)
	lastSeen := clk.t
	n.post(t, "/v1/communities/heartbeat", c.heartbeat(c.server, lastSeen, 1, 1, false), nil)

	// Just under seven days of silence: offline, still held.
	clk.t = lastSeen.Add(7*24*time.Hour - time.Minute)
	n.srv.Expire(context.Background())
	wantState(t, n, c, wire.CommunityState_COMMUNITY_STATE_OFFLINE)
	if code := n.get(t, "/v1/routes/"+identity.Encode(c.route), nil); code != 200 {
		t.Fatalf("route before the reap window: %d", code)
	}

	// Past it: terminated. Route, listing and search results are gone; the
	// status says when it was last seen.
	clk.t = lastSeen.Add(7*24*time.Hour + time.Minute)
	n.srv.Expire(context.Background())
	st := wantState(t, n, c, wire.CommunityState_COMMUNITY_STATE_TERMINATED)
	if st.LastSeen != lastSeen.UnixMilli() || st.ReapedAt != clk.t.UnixMilli() || st.Heartbeat != nil {
		t.Fatalf("tombstone status = %v", st)
	}
	if code := n.get(t, "/v1/routes/"+identity.Encode(c.route), nil); code != http.StatusNotFound {
		t.Fatalf("route after reaping: %d", code)
	}
	if code := n.get(t, "/v1/listings/"+identity.Encode(c.owner.Public), nil); code != http.StatusNotFound {
		t.Fatalf("listing after reaping: %d", code)
	}
	var res wire.SearchResponse
	if n.get(t, "/v1/search?q=gardeners", &res); len(res.Listings) != 0 {
		t.Fatalf("search still finds a reaped community")
	}

	// The old route and listing (stale), a fresh route without
	// re-registration, a heartbeat and a listing: all refused as terminated.
	for what, code := range map[string]int{
		"old route":   n.post(t, "/v1/routes", c.routeAt(start, 0), nil),
		"fresh route": n.post(t, "/v1/routes", c.routeAt(clk.t, 0), nil),
		"heartbeat":   n.post(t, "/v1/communities/heartbeat", c.heartbeat(c.server, clk.t, 1, 1, false), nil),
		"listing":     n.post(t, "/v1/listings", c.listingAt(clk.t, "Gardeners"), nil),
		// A re-registration from before the server went silent is a replay.
		"old reregistration": n.post(t, "/v1/routes", c.routeAt(clk.t, lastSeen.Add(-time.Hour).UnixMilli()), nil),
	} {
		if code != http.StatusGone {
			t.Errorf("%s after reaping: %d, want 410", what, code)
		}
	}
	wantState(t, n, c, wire.CommunityState_COMMUNITY_STATE_TERMINATED)

	// The operator re-registers: the route carries a re-registration time
	// after the server was last seen. Everything works again.
	clk.advance(time.Minute)
	if code := n.post(t, "/v1/routes", c.routeAt(clk.t, clk.t.UnixMilli()), nil); code != 200 {
		t.Fatalf("re-registered route: %d", code)
	}
	if code := n.post(t, "/v1/listings", c.listingAt(clk.t, "Gardeners"), nil); code != 200 {
		t.Fatalf("listing after re-registration: %d", code)
	}
	n.post(t, "/v1/communities/heartbeat", c.heartbeat(c.server, clk.t, 1, 1, false), nil)
	wantState(t, n, c, wire.CommunityState_COMMUNITY_STATE_ONLINE)
	// Later routes keep the same re-registration time and are accepted.
	clk.advance(time.Minute)
	if code := n.post(t, "/v1/routes", c.routeAt(clk.t, clk.t.Add(-time.Minute).UnixMilli()), nil); code != 200 {
		t.Fatalf("next route: %d", code)
	}
}

func TestTombstoneExpires(t *testing.T) {
	clk := &clock{time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	n := startClockedNode(t, clk, func(cfg *meta.Config) {
		cfg.CommunityReapAfter.Duration = time.Hour
		cfg.CommunityTombstoneTTL.Duration = 2 * time.Hour
	})
	c := newCommunity(t)
	n.post(t, "/v1/routes", c.routeAt(clk.t, 0), nil)
	clk.advance(time.Hour + time.Second)
	n.srv.Expire(context.Background())
	wantState(t, n, c, wire.CommunityState_COMMUNITY_STATE_TERMINATED)
	clk.advance(2*time.Hour + time.Second)
	n.srv.Expire(context.Background())
	if _, code := n.status(t, c); code != http.StatusNotFound {
		t.Fatalf("status after the tombstone expired: %d", code)
	}
	// With no record left, a fresh route registers like a new community.
	if code := n.post(t, "/v1/routes", c.routeAt(clk.t, 0), nil); code != 200 {
		t.Fatalf("fresh route after the tombstone expired: %d", code)
	}
}

// Heartbeats replicate like routes; a peer that still holds a reaped
// community's old route can't bring it back.
func TestReplicationOfStatusAndNoResurrection(t *testing.T) {
	clk := &clock{time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	// The peer keeps communities far longer (another operator's choice).
	peer := startClockedNode(t, clk, func(cfg *meta.Config) { cfg.CommunityReapAfter.Duration = 300 * 24 * time.Hour })
	follower := startClockedNode(t, clk, func(cfg *meta.Config) { cfg.Peers = []string{peer.url} })
	c := newCommunity(t)
	peer.post(t, "/v1/routes", c.routeAt(clk.t, 0), nil)
	peer.post(t, "/v1/listings", c.listingAt(clk.t, "Choir"), nil)
	peer.post(t, "/v1/communities/heartbeat", c.heartbeat(c.server, clk.t, 1, 1, false), nil)
	follower.srv.ReplicateOnce(context.Background())
	if st := wantState(t, follower, c, wire.CommunityState_COMMUNITY_STATE_ONLINE); st.Heartbeat == nil {
		t.Fatal("the heartbeat didn't replicate")
	}

	// Eight days of silence: the follower reaps; the peer still holds it.
	clk.advance(8 * 24 * time.Hour)
	follower.srv.Expire(context.Background())
	peer.srv.Expire(context.Background())
	wantState(t, follower, c, wire.CommunityState_COMMUNITY_STATE_TERMINATED)
	wantState(t, peer, c, wire.CommunityState_COMMUNITY_STATE_OFFLINE)

	// The follower pulls everything from the peer again (as a new peer
	// would): the old route, listing and heartbeat are refused as stale.
	follower.srv.ResetReplicationCursors(context.Background())
	follower.srv.ReplicateOnce(context.Background())
	wantState(t, follower, c, wire.CommunityState_COMMUNITY_STATE_TERMINATED)
	if code := follower.get(t, "/v1/routes/"+identity.Encode(c.route), nil); code != http.StatusNotFound {
		t.Fatalf("a stale route came back through replication: %d", code)
	}
	var res wire.SearchResponse
	if follower.get(t, "/v1/search?q=choir", &res); len(res.Listings) != 0 {
		t.Fatal("a stale listing came back through replication")
	}

	// A fresh heartbeat posted to the peer (a server that never learnt it
	// was reaped there) doesn't revive it on the follower either.
	peer.post(t, "/v1/communities/heartbeat", c.heartbeat(c.server, clk.t, 1, 1, false), nil)
	follower.srv.ReplicateOnce(context.Background())
	wantState(t, follower, c, wire.CommunityState_COMMUNITY_STATE_TERMINATED)
}

func TestCommunityStatusConfig(t *testing.T) {
	for name, mutate := range map[string]func(*meta.Config){
		"offline window too short": func(c *meta.Config) { c.CommunityOfflineAfter.Duration = time.Second },
		"reap before offline":      func(c *meta.Config) { c.CommunityReapAfter.Duration = time.Minute },
		"reap over a year":         func(c *meta.Config) { c.CommunityReapAfter.Duration = 400 * 24 * time.Hour },
		"tombstone too short":      func(c *meta.Config) { c.CommunityTombstoneTTL.Duration = time.Second },
	} {
		cfg := meta.DefaultConfig()
		cfg.DirectoryEnabled = true // the tests exercise the directory; it's off by default
		cfg.DataDir = t.TempDir()
		mutate(&cfg)
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "community_") {
			t.Errorf("%s: %v", name, err)
		}
	}
	cfg := meta.DefaultConfig()
	cfg.DirectoryEnabled = true // the tests exercise the directory; it's off by default
	if cfg.CommunityReapAfter.Duration != 7*24*time.Hour || cfg.CommunityOfflineAfter.Duration != 3*time.Minute || cfg.CommunityTombstoneTTL.Duration != 30*24*time.Hour {
		t.Fatalf("defaults = %v %v %v", cfg.CommunityOfflineAfter, cfg.CommunityReapAfter, cfg.CommunityTombstoneTTL)
	}
}
