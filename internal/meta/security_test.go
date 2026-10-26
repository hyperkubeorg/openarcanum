package meta_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/meta"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Attacks on the maintainer node's public API (docs/threat-model.md
// "Maintainer nodes").

// A reaped community's route id stays bound to that community: another
// community's route for it, however fresh its re-registration time, is
// refused, and the real owners can still re-register.
func TestReapedRouteIDCantBeTakenOver(t *testing.T) {
	clk := &clock{time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	n := startClockedNode(t, clk, nil)
	victim := newCommunity(t)
	n.post(t, "/v1/routes", victim.routeAt(clk.t, 0), nil)
	clk.t = clk.t.Add(7*24*time.Hour + time.Minute)
	n.srv.Expire(context.Background())
	wantState(t, n, victim, wire.CommunityState_COMMUNITY_STATE_TERMINATED)

	attacker := newCommunity(t)
	attacker.route = victim.route
	if code := n.post(t, "/v1/routes", attacker.routeAt(clk.t, clk.t.UnixMilli()), nil); code != http.StatusForbidden {
		t.Fatalf("another community's route for a reaped route id: %d, want 403", code)
	}
	wantState(t, n, victim, wire.CommunityState_COMMUNITY_STATE_TERMINATED)
	clk.advance(time.Minute)
	if code := n.post(t, "/v1/routes", victim.routeAt(clk.t, clk.t.UnixMilli()), nil); code != 200 {
		t.Fatalf("the owners' re-registration: %d", code)
	}
	var sr wire.SignedRoute
	var rt wire.Route
	if n.get(t, "/v1/routes/"+identity.Encode(victim.route), &sr) != 200 || proto.Unmarshal(sr.Route, &rt) != nil {
		t.Fatal("route after re-registration")
	}
	if !bytes.Equal(rt.ServerKey, victim.server.Public) {
		t.Fatal("the served route isn't the owners'")
	}
}

// A heartbeat lists a relay URL only if the URL serves the heartbeat's key:
// another key can't take a relay's URL (and steer servers to itself), even
// before the real relay registered; the real relay's key is listed.
func TestRelayURLNeedsProofOfControl(t *testing.T) {
	r := testutil.StartRelay(t)
	real, _ := identity.Generate()
	evil, _ := identity.Generate()
	r.Server.SetRegistryKey(real.Public)
	n := startNode(t, nil) // allow_private_relays: the relay is on 127.0.0.1
	n.srv.SetRelayProof(nil)
	now := time.Now()
	squat := &wire.RelayHeartbeat{RelayKey: evil.Public, Url: r.URL, At: now.UnixMilli(), Capacity: 1, Connections: 0}
	if code := n.post(t, "/v1/relays/heartbeat", signHeartbeat(evil, squat), nil); code != http.StatusForbidden {
		t.Fatalf("heartbeat for another relay's URL: %d, want 403", code)
	}
	// The real relay, claiming more than it may: listed, numbers bounded.
	claim := &wire.RelayHeartbeat{RelayKey: real.Public, Url: r.URL, At: now.UnixMilli(), Capacity: 4_000_000_000, Connections: 4_000_000_001}
	if code := n.post(t, "/v1/relays/heartbeat", signHeartbeat(real, claim), nil); code != 200 {
		t.Fatalf("the relay's own heartbeat: %d", code)
	}
	var list wire.RelayList
	n.get(t, "/v1/relays", &list)
	if len(list.Relays) != 1 || list.Relays[0].Url != r.URL || list.Relays[0].Capacity > 1_000_000 || list.Relays[0].Connections > list.Relays[0].Capacity {
		t.Fatalf("registry: %v", list.Relays)
	}
	squat.At = now.Add(time.Second).UnixMilli()
	if code := n.post(t, "/v1/relays/heartbeat", signHeartbeat(evil, squat), nil); code == 200 {
		t.Fatal("another key took the URL over")
	}
	// A URL that serves nothing proves nothing.
	dead := &wire.RelayHeartbeat{RelayKey: evil.Public, Url: "ws://127.0.0.1:1/ws", At: now.UnixMilli()}
	if code := n.post(t, "/v1/relays/heartbeat", signHeartbeat(evil, dead), nil); code != http.StatusForbidden {
		t.Fatalf("heartbeat for a URL that serves nothing: %d, want 403", code)
	}
}

// Relay URLs on private, loopback or local-only hosts aren't listed (the
// community servers that pick registry relays would dial them).
func TestRelayRegistryRefusesPrivateHosts(t *testing.T) {
	n := startNode(t, func(c *meta.Config) { c.AllowPrivateRelays = false })
	k, _ := identity.Generate()
	for _, u := range []string{"ws://127.0.0.1:7000/ws", "ws://10.1.2.3/ws", "ws://169.254.169.254/ws", "ws://[::1]/ws", "ws://localhost/ws", "ws://redis:6379/ws", "wss://metadata.google.internal/ws"} {
		if code := n.post(t, "/v1/relays/heartbeat", heartbeat(k, u, time.Now()), nil); code != http.StatusBadRequest {
			t.Errorf("heartbeat for %s: %d, want 400", u, code)
		}
	}
	if code := n.post(t, "/v1/relays/heartbeat", heartbeat(k, "wss://relay.example.org/ws", time.Now()), nil); code != 200 {
		t.Fatalf("public relay: %d", code)
	}
	if code := n.post(t, "/v1/nameplates", &wire.NameplateRequest{Relay: "ws://127.0.0.1:7000/ws"}, nil); code != 200 {
		t.Fatalf("a link code's relay is the device's own choice: %d", code)
	}
	// Proof can be off only on a development node that allows private hosts.
	cfg := meta.DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.RelayProof = false
	if err := cfg.Validate(); err == nil {
		t.Fatal("relay_proof off accepted without allow_private_relays")
	}
	cfg.AllowPrivateRelays = true
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

// Link-code numbers are random, not the next free one, and one network
// can't sweep them: a third party guessing numbers doesn't find the live
// code, and its claims are rate limited.
func TestNameplatesCantBeSniped(t *testing.T) {
	clk := &clock{time.Now()}
	n := startClockedNode(t, clk, nil)
	var np wire.Nameplate
	if code := n.post(t, "/v1/nameplates", &wire.NameplateRequest{Relay: "ws://r.example/ws"}, &np); code != 200 {
		t.Fatal(code)
	}
	if np.Number < 100_000 || np.Number > 999_999 {
		t.Fatalf("nameplate number %d, want six random digits", np.Number)
	}
	sniped := false
	limited := 0
	for guess := uint32(1); guess <= 30; guess++ {
		var got wire.Nameplate
		switch n.post(t, "/v1/nameplates/claim", &wire.NameplateClaim{Number: guess}, &got) {
		case 200:
			sniped = true
		case http.StatusTooManyRequests:
			limited++
		}
	}
	if sniped || limited == 0 {
		t.Fatalf("guessed claims: sniped %v, rate limited %d of 30", sniped, limited)
	}
	clk.advance(time.Minute)
	var got wire.Nameplate
	if code := n.post(t, "/v1/nameplates/claim", &wire.NameplateClaim{Number: np.Number}, &got); code != 200 || !bytes.Equal(got.Topic, np.Topic) {
		t.Fatalf("the real claim: %d", code)
	}
	// Numbers aren't sequential.
	seen := map[uint32]bool{np.Number: true}
	sequential := true
	for range 5 {
		var next wire.Nameplate
		n.post(t, "/v1/nameplates", &wire.NameplateRequest{Relay: "ws://r.example/ws"}, &next)
		if seen[next.Number] || (!seen[next.Number-1] && !seen[next.Number+1]) {
			sequential = false
		}
		seen[next.Number] = true
	}
	if sequential {
		t.Fatal("nameplate numbers are sequential")
	}
}

func signHeartbeat(k *identity.Key, hb *wire.RelayHeartbeat) *wire.SignedHeartbeat {
	b, _ := proto.Marshal(hb)
	return &wire.SignedHeartbeat{Heartbeat: b, Signature: k.Sign(identity.CtxRelayHeartbeat, b)}
}

// A route or listing without a publish time (signed before published_at
// existed) is as stale as one signed long ago: an old copy can't be
// replayed to bring back a community reaped since.
func TestUndatedRecordsAreStale(t *testing.T) {
	n := startNode(t, nil)
	c := newCommunity(t)
	r, _ := proto.Marshal(&wire.Route{RouteId: c.route, ServerKey: c.server.Public, Relays: []string{"ws://r/ws"}, Seq: 1})
	if code := n.post(t, "/v1/routes", &wire.SignedRoute{Route: r, Signature: c.server.Sign(identity.CtxRoute, r), Cert: c.cert}, nil); code != http.StatusGone {
		t.Fatalf("undated route: %d, want 410", code)
	}
	l, _ := proto.Marshal(&wire.Listing{Community: c.owner.Public, ServerKey: c.server.Public, RouteId: c.route, Name: "Old", Seq: 1})
	if code := n.post(t, "/v1/listings", &wire.SignedListing{Listing: l, Signature: c.server.Sign(identity.CtxListing, l), Cert: c.cert}, nil); code != http.StatusGone {
		t.Fatalf("undated listing: %d, want 410", code)
	}
}

// pad is an unknown protobuf field of n bytes, to append to a message's
// encoding.
func pad(n int) []byte {
	b := protowire.AppendTag(nil, 999, protowire.BytesType)
	return protowire.AppendBytes(b, bytes.Repeat([]byte{'A'}, n))
}

// Listings, routes and heartbeats are bounded in size, stored without
// unknown fields, and their strings are bounded.
func TestOversizedRecordsRefused(t *testing.T) {
	n := startNode(t, nil)
	c := newCommunity(t)
	sl := c.listing("Padded", "", nil, 1)
	b, _ := proto.Marshal(sl)
	if code := n.postRaw(t, "/v1/listings", append(bytes.Clone(b), pad(20<<20)...), nil); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("20 MiB listing: %d, want 413", code)
	}
	// A little padding is accepted, and dropped: the node keeps and serves
	// only the fields it knows.
	if code := n.postRaw(t, "/v1/listings", append(bytes.Clone(b), pad(1000)...), nil); code != 200 {
		t.Fatalf("listing with an unknown field: %d", code)
	}
	var got wire.SignedListing
	if n.get(t, "/v1/listings/"+identity.Encode(c.owner.Public), &got) != 200 || len(got.ProtoReflect().GetUnknown()) != 0 || !proto.Equal(&got, sl) {
		t.Fatal("the stored listing kept the unknown field")
	}
	// A tag is a word, not ten kilobytes.
	if code := n.post(t, "/v1/listings", newCommunity(t).listing("Tags", "", []string{strings.Repeat("t", 10<<10)}, 1), nil); code != http.StatusBadRequest {
		t.Fatalf("10 KiB tag: %d, want 400", code)
	}

	pub := time.Now().UnixMilli()
	r, _ := proto.Marshal(&wire.Route{RouteId: bytes.Repeat([]byte{7}, 16), ServerKey: c.server.Public, Relays: []string{"ws://r/ws"}, Seq: 1, PublishedAt: pub})
	rb, _ := proto.Marshal(&wire.SignedRoute{Route: r, Signature: c.server.Sign(identity.CtxRoute, r), Cert: c.cert})
	if code := n.postRaw(t, "/v1/routes", append(rb, pad(20<<20)...), nil); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("20 MiB route: %d, want 413", code)
	}
	r, _ = proto.Marshal(&wire.Route{RouteId: bytes.Repeat([]byte{8}, 16), ServerKey: c.server.Public, Relays: []string{strings.Repeat("r", 100<<10)}, Seq: 1, PublishedAt: pub})
	if code := n.post(t, "/v1/routes", &wire.SignedRoute{Route: r, Signature: c.server.Sign(identity.CtxRoute, r), Cert: c.cert}, nil); code != http.StatusBadRequest {
		t.Fatalf("route with a 100 KiB relay: %d, want 400", code)
	}

	k, _ := identity.Generate()
	hb, _ := proto.Marshal(heartbeat(k, "ws://x/ws", time.Now()))
	if code := n.postRaw(t, "/v1/relays/heartbeat", append(hb, pad(1<<20)...), nil); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("1 MiB relay heartbeat: %d, want 413", code)
	}
	var rep wire.ReplicationBatch
	n.get(t, "/v1/replicate?cursor=0", &rep)
	if out, _ := proto.Marshal(&rep); len(out) > 64<<10 {
		t.Fatalf("replication batch is %d bytes", len(out))
	}
}

// Records a peer can't take never stop replication: a legitimate route
// posted after a burst of padded ones still reaches the peer.
func TestReplicationNotStalledByOversizedRecords(t *testing.T) {
	a := startNode(t, nil)
	c := newCommunity(t)
	for i := range 11 {
		id := bytes.Repeat([]byte{byte(i + 10)}, 16)
		r, _ := proto.Marshal(&wire.Route{RouteId: id, ServerKey: c.server.Public, Relays: []string{"ws://r/ws"}, Seq: 1, PublishedAt: time.Now().UnixMilli()})
		rb, _ := proto.Marshal(&wire.SignedRoute{Route: r, Signature: c.server.Sign(identity.CtxRoute, r), Cert: c.cert})
		a.postRaw(t, "/v1/routes", append(rb, pad(26<<20)...), nil)
	}
	good := newCommunity(t)
	good.route = bytes.Repeat([]byte{9}, 16)
	if code := a.post(t, "/v1/routes", good.signedRoute(1), nil); code != 200 {
		t.Fatalf("legit route: %d", code)
	}
	b := startNode(t, func(cfg *meta.Config) { cfg.Peers = []string{a.url} })
	for range 3 {
		b.srv.ReplicateOnce(context.Background())
	}
	if code := b.get(t, "/v1/routes/"+identity.Encode(good.route), nil); code != 200 {
		t.Fatalf("legit route on the peer after 3 rounds: %d", code)
	}
}

// A replication batch is bounded in bytes, not only in entries: many
// listings with large icons come over several batches, and all arrive.
func TestReplicationBatchesByBytes(t *testing.T) {
	a := startNode(t, nil)
	icon := bytes.Repeat([]byte{0x89}, 200<<10)
	sum := sha256.Sum256(icon)
	const count = 30
	for i := range count {
		c := newCommunity(t)
		l, _ := proto.Marshal(&wire.Listing{Community: c.owner.Public, ServerKey: c.server.Public, RouteId: c.route,
			Name: fmt.Sprintf("Iconic %d", i), Seq: 1, IconSha256: sum[:], PublishedAt: time.Now().UnixMilli()})
		sl := &wire.SignedListing{Listing: l, Signature: c.server.Sign(identity.CtxListing, l), Cert: c.cert, Icon: icon}
		if code := a.post(t, "/v1/listings", sl, nil); code != 200 {
			t.Fatalf("listing %d: %d", i, code)
		}
	}
	var rep wire.ReplicationBatch
	a.get(t, "/v1/replicate?cursor=0", &rep)
	if out, _ := proto.Marshal(&rep); len(rep.Listings) == count || len(out) > 5<<20 {
		t.Fatalf("one batch carries %d listings in %d bytes", len(rep.Listings), len(out))
	}
	b := startNode(t, func(cfg *meta.Config) { cfg.Peers = []string{a.url} })
	for range 4 {
		b.srv.ReplicateOnce(context.Background())
	}
	var snap wire.DirectorySnapshot
	b.get(t, "/v1/directory/snapshot", &snap)
	if snap.Total != count {
		t.Fatalf("the peer lists %d of %d", snap.Total, count)
	}
}
