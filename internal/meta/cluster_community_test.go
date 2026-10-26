package meta_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// A community reaped by one member stays reaped on a member that was down
// during the reaping window and comes back still holding its route; one
// that its server re-registered in the meantime stays.
func TestClusterReapedCommunityStaysReaped(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	clkA, clkC := &clock{start}, &clock{start}
	a, c := pair(t, nil)
	a.srv.SetClock(clkA.now)
	c.srv.SetClock(clkC.now)
	dead, revived := newCommunity(t), newCommunity(t)
	revived.route = randBytes(16)
	for _, n := range []*clusterNode{a, c} {
		for _, com := range []*community{dead, revived} {
			if code := n.post(t, "/v1/routes", com.routeAt(start, 0), nil); code != 200 {
				t.Fatal(code)
			}
		}
	}
	syncAll(t, a, c)

	// c is down; eight days later a reaps both.
	c.kill()
	clkA.advance(8 * 24 * time.Hour)
	a.srv.Expire(ctx)
	wantState(t, a.node, dead, wire.CommunityState_COMMUNITY_STATE_TERMINATED)
	// revived's operator re-registers it, but only c (when back) hears.
	c.down.Store(false)
	clkC.advance(8*24*time.Hour - time.Hour)
	if code := c.post(t, "/v1/routes", revived.routeAt(clkC.t, clkC.t.UnixMilli()), nil); code != 200 {
		t.Fatalf("re-registration on c: %d", code)
	}
	syncAll(t, a, c)
	wantState(t, c.node, dead, wire.CommunityState_COMMUNITY_STATE_TERMINATED)
	if code := c.get(t, "/v1/routes/"+identity.Encode(dead.route), nil); code != http.StatusNotFound {
		t.Fatalf("c still serves the reaped community's route: %d", code)
	}
	if code := c.get(t, "/v1/routes/"+identity.Encode(revived.route), nil); code != 200 {
		t.Fatalf("a re-registered community was dropped by a member's tombstone: %d", code)
	}
}
