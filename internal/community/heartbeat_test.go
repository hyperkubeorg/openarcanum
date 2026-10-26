package community_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/meta"
	"github.com/hyperkubeorg/openarcanum/internal/relay"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Status reports to maintainers, automatic relay failover and coming back
// after being reaped (docs/protocol.md §7.4).

// registered starts n relays that heartbeat into the registry of m.
func registered(t *testing.T, m *testutil.Meta, n int) []*testutil.Relay {
	t.Helper()
	var out []*testutil.Relay
	for i := 0; i < n; i++ {
		r := testutil.StartRelay(t)
		cfg := relay.DefaultConfig()
		cfg.RegistryURLs = []string{m.URL}
		cfg.PublicURL = r.URL
		cfg.HeartbeatInterval.Duration = time.Second
		hb, err := relay.NewHeartbeater(cfg, r.Server, testutil.Quiet())
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		go hb.Run(ctx)
		out = append(out, r)
	}
	var list wire.RelayList
	waitFor(t, "relay registry", func() bool {
		list.Reset()
		return httpGet(t, m.URL+"/v1/relays", &list) == 200 && len(list.Relays) == n
	})
	return out
}

func communityStatus(t *testing.T, m *testutil.Meta, c *testutil.Community) *wire.CommunityStatus {
	t.Helper()
	var st wire.CommunityStatus
	if httpGet(t, m.URL+"/v1/communities/"+identity.Encode(c.Server.RouteID())+"/status", &st) != 200 {
		return nil
	}
	return &st
}

func waitState(t *testing.T, m *testutil.Meta, c *testutil.Community, want wire.CommunityState) *wire.CommunityStatus {
	t.Helper()
	var st *wire.CommunityStatus
	waitFor(t, "community state "+want.String(), func() bool {
		st = communityStatus(t, m, c)
		return st != nil && st.State == want
	})
	return st
}

func reporting(m *testutil.Meta) func(*community.Config) {
	return func(cfg *community.Config) {
		cfg.Maintainers = []string{m.URL}
		cfg.HeartbeatInterval.Duration = time.Second
		cfg.PublishInterval.Duration = time.Second
	}
}

// A running server reports itself online, with its relays; a clean
// shutdown shows it offline at once.
func TestHeartbeatsReportStatus(t *testing.T) {
	m := testutil.StartMeta(t, nil)
	r := testutil.StartRelay(t)
	c := testutil.StartCommunity(t, reporting(m), r.URL)
	claimedOn(t, r, c, nil)
	st := waitState(t, m, c, wire.CommunityState_COMMUNITY_STATE_ONLINE)
	var hb wire.CommunityHeartbeat
	if err := proto.Unmarshal(st.Heartbeat.Heartbeat, &hb); err != nil {
		t.Fatal(err)
	}
	if hb.RelaysAttached != 1 || hb.RelaysWanted != 1 || !hb.Pinned || hb.Stopping {
		t.Fatalf("heartbeat = %v", &hb)
	}
	if identity.Verify(c.Server.ServerKey(), identity.CtxCommunityHeartbeat, st.Heartbeat.Heartbeat, st.Heartbeat.Signature) != nil {
		t.Fatal("heartbeat isn't signed by the server key")
	}
	c.Stop()
	st = waitState(t, m, c, wire.CommunityState_COMMUNITY_STATE_OFFLINE)
	proto.Unmarshal(st.Heartbeat.Heartbeat, &hb)
	if !hb.Stopping {
		t.Fatal("the last heartbeat isn't the stopping one")
	}
}

// Automatic mode: the relay the server is on dies mid-session; the server
// moves to another healthy relay from the registry, publishes the new route,
// and members who follow the route carry on.
func TestAutomaticRelayFailover(t *testing.T) {
	m := testutil.StartMeta(t, nil)
	rs := registered(t, m, 2)
	c := testutil.StartCommunity(t, func(cfg *community.Config) {
		reporting(m)(cfg)
		cfg.RelayMode = "automatic"
		cfg.RelayCount = 1
	}, rs[0].URL)
	// The configured relay is preferred while it's healthy.
	waitFor(t, "attached to the configured relay", func() bool { return slices.Equal(c.Server.AttachedRelays(), []string{rs[0].URL}) })
	e := claimedOn(t, rs[0], c, nil)
	ctx := testutil.Ctx(t)
	gen := e.owner.Channel("general")
	if _, err := e.owner.Post(ctx, gen.Id, "before"); err != nil {
		t.Fatal(err)
	}

	rs[0].Stop()
	waitFor(t, "failover to the other relay", func() bool { return slices.Equal(c.Server.AttachedRelays(), []string{rs[1].URL}) })
	// The route now names the new relay.
	waitFor(t, "route on the new relay", func() bool {
		var sr wire.SignedRoute
		var rt wire.Route
		return httpGet(t, m.URL+"/v1/routes/"+identity.Encode(c.Server.RouteID()), &sr) == 200 &&
			proto.Unmarshal(sr.Route, &rt) == nil && slices.Equal(rt.Relays, []string{rs[1].URL})
	})
	waitState(t, m, c, wire.CommunityState_COMMUNITY_STATE_ONLINE)
	// The member follows the route and carries on.
	owner2 := testutil.NewMember(t, "Owner")
	owner2.Identity, owner2.Static = e.owner.Identity, e.owner.Static
	if err := owner2.Connect(ctx, rs[1].URL, c.Server.ServerKey()); err != nil {
		t.Fatal(err)
	}
	defer owner2.Close()
	if _, err := owner2.Post(ctx, gen.Id, "after"); err != nil {
		t.Fatal(err)
	}
}

// A community pinned to its relays never moves to public relays when they
// die: it reports itself offline (no relay attached) instead.
func TestPinnedRelaysDoNotFailOver(t *testing.T) {
	m := testutil.StartMeta(t, nil)
	rs := registered(t, m, 2)
	c := testutil.StartCommunity(t, reporting(m), rs[0].URL)
	claimedOn(t, rs[0], c, nil)
	waitState(t, m, c, wire.CommunityState_COMMUNITY_STATE_ONLINE)
	rs[0].Stop()
	st := waitState(t, m, c, wire.CommunityState_COMMUNITY_STATE_OFFLINE)
	var hb wire.CommunityHeartbeat
	proto.Unmarshal(st.Heartbeat.Heartbeat, &hb)
	if hb.RelaysAttached != 0 || hb.RelaysWanted != 1 || !hb.Pinned || hb.Stopping {
		t.Fatalf("heartbeat while its pinned relay is down = %v", &hb)
	}
	time.Sleep(2 * time.Second)
	if got := c.Server.AttachedRelays(); len(got) != 0 {
		t.Fatalf("a pinned community moved to %v", got)
	}
}

// Reaped by the maintainers while it was off: on its return it learns so
// and keeps being refused until the operator re-registers it; then it is
// online again.
func TestReapedServerComesBackOnlyWhenReregistered(t *testing.T) {
	m := testutil.StartMeta(t, func(cfg *meta.Config) {
		cfg.CommunityOfflineAfter.Duration = 10 * time.Second
		cfg.CommunityReapAfter.Duration = 10 * time.Second
	})
	r := testutil.StartRelay(t)
	mutate := reporting(m)
	c := testutil.StartCommunity(t, mutate, r.URL)
	claimedOn(t, r, c, nil)
	waitState(t, m, c, wire.CommunityState_COMMUNITY_STATE_ONLINE)
	c.Stop()

	// Eleven seconds of silence, seen from the maintainer's clock (nothing
	// talks to it meanwhile).
	m.Server.SetClock(func() time.Time { return time.Now().Add(11 * time.Second) })
	m.Server.Expire(context.Background())
	m.Server.SetClock(time.Now)
	waitState(t, m, c, wire.CommunityState_COMMUNITY_STATE_TERMINATED)

	cfg := community.DefaultConfig(c.Dir)
	cfg.Relays = []string{r.URL}
	cfg.BackupInterval.Duration = 0
	mutate(&cfg)
	srv := runServer(t, cfg)
	ctx := testutil.Ctx(t)
	waitFor(t, "the server to learn it was reaped", func() bool { return srv.Reaped(ctx) != 0 })
	if st := communityStatus(t, m, c); st == nil || st.State != wire.CommunityState_COMMUNITY_STATE_TERMINATED {
		t.Fatalf("a reaped community came back without re-registering: %v", st)
	}

	if err := community.Reregister(ctx, cfg, time.Now()); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, c, wire.CommunityState_COMMUNITY_STATE_ONLINE)
	waitFor(t, "the reaped mark to clear", func() bool { return srv.Reaped(ctx) == 0 })
}

// runServer runs a community server from cfg until the test ends.
func runServer(t *testing.T, cfg community.Config) *community.Server {
	t.Helper()
	srv, err := community.Open(context.Background(), cfg, testutil.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = srv.Run(ctx)
		_ = srv.Close()
		close(done)
	}()
	t.Cleanup(func() { cancel(); <-done })
	return srv
}

func TestHeartbeatIntervalConfig(t *testing.T) {
	for _, d := range []time.Duration{0, time.Millisecond, 11 * time.Minute} {
		cfg := community.DefaultConfig(t.TempDir())
		cfg.Relays = []string{"ws://relay.example.invalid/ws"}
		cfg.HeartbeatInterval.Duration = d
		if err := cfg.Validate(); err == nil {
			t.Errorf("heartbeat_interval %v accepted", d)
		}
	}
	if community.DefaultConfig("x").HeartbeatInterval.Duration != time.Minute {
		t.Fatal("heartbeat_interval default isn't 1m")
	}
}
