package community_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/relay"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/version"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

func httpGet(t *testing.T, url string, out proto.Message) int {
	t.Helper()
	resp, err := apiClient.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 200 && out != nil {
		if err := proto.Unmarshal(b, out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A public community publishes a signed listing and route; search finds it and
// the listing carries a working public join key.
func TestPublicListingAndRoute(t *testing.T) {
	m := testutil.StartMeta(t, nil)
	r := testutil.StartRelay(t)
	c := testutil.StartCommunity(t, func(cfg *community.Config) { cfg.Maintainers = []string{m.URL} }, r.URL)
	owner := testutil.NewMember(t, "Owner")
	ctx := testutil.Ctx(t)
	if err := owner.Connect(ctx, r.URL, c.ClaimCode.ServerKey); err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	ownerKey, _ := identity.Generate()
	if _, err := owner.Claim(ctx, ownerKey, c.ClaimCode, &wire.CommunitySettings{
		Name: "Woodworkers", Description: "Joinery and hand tools", Tags: []string{"wood", "tools"},
		Visibility: wire.Visibility_VISIBILITY_PUBLIC, JoinPolicy: wire.JoinPolicy_JOIN_POLICY_OPEN,
		MessageRetentionDays: 30, FileRetentionDays: 7, SearchEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	var res wire.SearchResponse
	waitFor(t, "listing", func() bool {
		res.Reset()
		return httpGet(t, m.URL+"/v1/search?q=joinery", &res) == 200 && len(res.Listings) == 1
	})
	var l wire.Listing
	proto.Unmarshal(res.Listings[0].Listing, &l)
	if l.Name != "Woodworkers" || !bytes.Equal(l.Community, ownerKey.Public) || len(l.Relays) != 1 || l.MemberRange != "1-10" {
		t.Fatalf("listing: %v", &l)
	}
	var route wire.SignedRoute
	if httpGet(t, m.URL+"/v1/routes/"+identity.Encode(c.Server.RouteID()), &route) != 200 {
		t.Fatal("no route published")
	}
	// Open policy: anyone who found it can join without a key.
	visitor := testutil.NewMember(t, "Visitor")
	if err := visitor.Connect(ctx, l.Relays[0], l.ServerKey); err != nil {
		t.Fatal(err)
	}
	defer visitor.Close()
	if jr, err := visitor.Join(ctx, ""); err != nil || jr.Status != wire.JoinStatus_JOIN_STATUS_ADMITTED {
		t.Fatalf("join from search: %v %v", jr, err)
	}
	// Going private withdraws the listing.
	s := cloneSettings(owner.State.Settings)
	s.Visibility = wire.Visibility_VISIBILITY_PRIVATE
	if _, err := owner.Admin(ctx, ownerKey.Public, &wire.AdminAction{Op: &wire.AdminAction_UpdateSettings{UpdateSettings: s}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "withdrawal", func() bool {
		res.Reset()
		return httpGet(t, m.URL+"/v1/search?q=joinery", &res) == 200 && len(res.Listings) == 0
	})
	// The route stays (private communities are still reachable by invite).
	if httpGet(t, m.URL+"/v1/routes/"+identity.Encode(c.Server.RouteID()), nil) != 200 {
		t.Fatal("route gone after going private")
	}
}

// In automatic mode the community picks relays from the registry.
func TestAutomaticRelaysFromRegistry(t *testing.T) {
	m := testutil.StartMeta(t, nil)
	relays := []*testutil.Relay{testutil.StartRelay(t), testutil.StartRelay(t), testutil.StartRelay(t)}
	for _, r := range relays {
		cfg := relay.DefaultConfig()
		cfg.RegistryURLs = []string{m.URL}
		cfg.PublicURL = r.URL
		hb, err := relay.NewHeartbeater(cfg, r.Server, testutil.Quiet())
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		go hb.Run(ctx)
	}
	var list wire.RelayList
	waitFor(t, "relay registry", func() bool {
		list.Reset()
		return httpGet(t, m.URL+"/v1/relays", &list) == 200 && len(list.Relays) == 3
	})
	c := testutil.StartCommunity(t, func(cfg *community.Config) {
		cfg.RelayMode = "automatic"
		cfg.Maintainers = []string{m.URL}
		cfg.RelayCount = 2
	})
	waitFor(t, "automatic attachment", func() bool { return len(c.Server.AttachedRelays()) == 2 })
}

// A community on two relays survives one of them dying: members fail over to
// the other and nothing stored is lost (docs/protocol.md §7.4).
func TestRelayFailover(t *testing.T) {
	r1, r2 := testutil.StartRelay(t), testutil.StartRelay(t)
	c := testutil.StartCommunity(t, nil, r1.URL, r2.URL)
	ctx := testutil.Ctx(t)
	owner := testutil.NewMember(t, "Owner")
	if err := owner.Connect(ctx, r1.URL, c.ClaimCode.ServerKey); err != nil {
		t.Fatal(err)
	}
	ownerKey, _ := identity.Generate()
	cr, err := owner.Claim(ctx, ownerKey, c.ClaimCode, nil)
	if err != nil {
		t.Fatal(err)
	}
	alice := testutil.NewMember(t, "Alice")
	if err := alice.Connect(ctx, r2.URL, c.ClaimCode.ServerKey); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.Join(ctx, cr.FirstInvite.Secret); err != nil {
		t.Fatal(err)
	}
	gen := alice.Channel("general")
	alice.Watch(ctx, gen, c.ClaimCode.ServerKey)
	// The server publishes on both relays, so members on either get messages.
	for i := 0; i < 3; i++ {
		owner.Post(ctx, gen.Id, fmt.Sprintf("before %d", i))
	}
	if got := alice.WaitReceived(gen.Id, 3, 5*time.Second); len(got) != 3 {
		t.Fatalf("cross-relay fan-out: %d", len(got))
	}
	// Relay 1 dies mid-chat. The owner reconnects through relay 2.
	r1.Stop()
	waitFor(t, "server to notice", func() bool { return len(c.Server.AttachedRelays()) == 1 })
	owner.Close()
	owner2 := testutil.NewMember(t, "Owner")
	owner2.Identity, owner2.Static = owner.Identity, owner.Static
	if err := owner2.Connect(ctx, r2.URL, c.ClaimCode.ServerKey); err != nil {
		t.Fatal(err)
	}
	defer owner2.Close()
	for i := 0; i < 2; i++ {
		if _, err := owner2.Post(ctx, gen.Id, fmt.Sprintf("after %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if got := alice.WaitReceived(gen.Id, 5, 5*time.Second); len(got) != 5 {
		t.Fatalf("after failover alice has %d live messages", len(got))
	}
	h, err := owner2.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_History{History: &wire.History{ChannelId: gen.Id}}})
	if err != nil || len(userMessages(h.GetMessages().Messages)) != 5 {
		t.Fatalf("stored messages after failover: %v %v", h, err)
	}
}

// Server key rotation: the owner certifies a new key; members keep working
// and see the new cert.
func TestServerKeyRotation(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	res, err := e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_PrepareKeyRotation{PrepareKeyRotation: &wire.PrepareKeyRotation{}}})
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := identity.IssueServerCert(e.ownerK, res.NewServerKey, 2, nil, nil)
	if _, err := e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_CommitKeyRotation{CommitKeyRotation: &wire.CommitKeyRotation{Cert: cert}}}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(e.comm.Server.ServerKey(), res.NewServerKey) {
		t.Fatal("server key not rotated")
	}
	// New sessions reach the server at its new key, which the owner cert covers.
	waitFor(t, "re-registration", func() bool {
		m := testutil.NewMember(t, "Owner")
		m.Identity, m.Static = e.owner.Identity, e.owner.Static
		defer m.Close()
		return m.Connect(ctx, e.relay.URL, res.NewServerKey) == nil
	})
	// An old-seq cert is refused for the next rotation.
	res2, _ := e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_PrepareKeyRotation{PrepareKeyRotation: &wire.PrepareKeyRotation{}}})
	stale, _ := identity.IssueServerCert(e.ownerK, res2.NewServerKey, 1, nil, nil)
	if _, err := e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_CommitKeyRotation{CommitKeyRotation: &wire.CommitKeyRotation{Cert: stale}}}); err == nil {
		t.Fatal("stale cert accepted")
	}
}

// Moving to new hardware: restore from backup, the owner adopts the new
// server, and it publishes the route under the same route id.
func TestMoveToNewHardware(t *testing.T) {
	m := testutil.StartMeta(t, nil)
	r := testutil.StartRelay(t)
	c := testutil.StartCommunity(t, func(cfg *community.Config) { cfg.Maintainers = []string{m.URL} }, r.URL)
	ctx := testutil.Ctx(t)
	owner := testutil.NewMember(t, "Owner")
	owner.Connect(ctx, r.URL, c.ClaimCode.ServerKey)
	ownerKey, _ := identity.Generate()
	if _, err := owner.Claim(ctx, ownerKey, c.ClaimCode, nil); err != nil {
		t.Fatal(err)
	}
	gen := owner.Channel("general")
	owner.Post(ctx, gen.Id, "carried over")
	backup, err := c.Server.Backup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first route", func() bool { return httpGet(t, m.URL+"/v1/routes/"+identity.Encode(c.Server.RouteID()), nil) == 200 })
	owner.Close()
	c.Stop()

	dir := t.TempDir()
	cfg := community.DefaultConfig(dir)
	cfg.Relays = []string{r.URL}
	cfg.Maintainers = []string{m.URL}
	cfg.BackupInterval.Duration = 0
	mv, err := community.Restore(ctx, cfg, backup, c.Dir+"/storage.key")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := community.Open(ctx, cfg, testutil.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	rctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = srv.Run(rctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done; srv.Close() })
	waitFor(t, "restored server attach", func() bool { return len(srv.AttachedRelays()) == 1 })

	// Before adoption, clients that pinned the owner can't be fooled: the
	// restored server's cert doesn't cover its new key.
	var code wire.MoveCode
	b, _ := identity.Decode(mv.MoveCode[len(community.MovePrefix):])
	proto.Unmarshal(b, &code)
	adopter := testutil.NewMember(t, "Owner")
	adopter.Identity, adopter.Static = owner.Identity, owner.Static
	if err := adopter.Connect(ctx, r.URL, code.ServerKey); err != nil {
		t.Fatal(err)
	}
	defer adopter.Close()
	cert, _ := identity.IssueServerCert(ownerKey, code.ServerKey, 2, nil, nil)
	if _, err := adopter.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_AdoptServer{AdoptServer: &wire.AdoptServer{Secret: code.Secret, Cert: cert}}}); err != nil {
		t.Fatal(err)
	}
	// The route now points at the new key, certified by the same owner.
	waitFor(t, "moved route", func() bool {
		var sr wire.SignedRoute
		if httpGet(t, m.URL+"/v1/routes/"+identity.Encode(code.RouteId), &sr) != 200 {
			return false
		}
		var rt wire.Route
		proto.Unmarshal(sr.Route, &rt)
		_, err := identity.VerifyServerCert(sr.Cert, rt.ServerKey, ownerKey.Public)
		return bytes.Equal(rt.ServerKey, code.ServerKey) && err == nil
	})
	// History came along.
	m2 := testutil.NewMember(t, "Owner")
	m2.Identity, m2.Static = owner.Identity, owner.Static
	if err := m2.Connect(ctx, r.URL, code.ServerKey); err != nil {
		t.Fatal(err)
	}
	defer m2.Close()
	h, err := m2.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_History{History: &wire.History{ChannelId: gen.Id}}})
	if err != nil || len(h.GetMessages().Messages) != 1 || h.GetMessages().Messages[0].Text != "carried over" {
		t.Fatalf("history on new hardware: %v %v", h, err)
	}
}

// apiClient announces this build's protocol versions (the maintainer API
// refuses requests without them).
var apiClient = &http.Client{Transport: version.Transport{}}
