package meta_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/meta"
	"github.com/hyperkubeorg/openarcanum/internal/version"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

func searchNames(t *testing.T, n *node, q string) []string {
	t.Helper()
	var sr wire.SearchResponse
	if code := n.get(t, "/v1/search?q="+q, &sr); code != 200 {
		t.Fatalf("search: %d", code)
	}
	var out []string
	for _, sl := range sr.Listings {
		var l wire.Listing
		proto.Unmarshal(sl.Listing, &l)
		out = append(out, l.Name)
	}
	return out
}

// The admin commands work on the database of a running node: the node sees
// a delisting and a relay removal at once, and refuses the entries from then
// on, also when they come back by replication.
func TestAdminDelistWhileRunning(t *testing.T) {
	var dir string
	n := startNode(t, func(c *meta.Config) { dir = c.DataDir })
	c := newCommunity(t)
	if code := n.post(t, "/v1/listings", c.listing("Gardeners", "plants", nil, 1), nil); code != 200 {
		t.Fatal(code)
	}
	if code := n.post(t, "/v1/routes", c.signedRoute(1), nil); code != 200 {
		t.Fatal(code)
	}
	adm, err := meta.OpenAdmin(context.Background(), dir, meta.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer adm.Close()
	ls, err := adm.Listings(context.Background(), "garden", 10)
	if err != nil || len(ls) != 1 || ls[0].Community != identity.Encode(c.owner.Public) {
		t.Fatalf("listings %v %v", ls, err)
	}
	removed, err := adm.Delist(context.Background(), c.owner.Public, "spam")
	if err != nil || removed != 2 {
		t.Fatalf("delist removed %d: %v", removed, err)
	}
	if got := searchNames(t, n, "Gardeners"); len(got) != 0 {
		t.Fatalf("delisted community still found: %v", got)
	}
	if code := n.get(t, "/v1/routes/"+identity.Encode(c.route), nil); code != http.StatusNotFound {
		t.Fatalf("route of a delisted community still served: %d", code)
	}
	if code := n.post(t, "/v1/listings", c.listing("Gardeners", "plants", nil, 2), nil); code != http.StatusForbidden {
		t.Fatalf("re-post after delisting: %d", code)
	}
	if code := n.post(t, "/v1/routes", c.signedRoute(2), nil); code != http.StatusForbidden {
		t.Fatalf("route after delisting: %d", code)
	}
	// Replicated from a peer that still has it: refused too.
	peer := startNode(t, nil)
	if code := peer.post(t, "/v1/listings", c.listing("Gardeners", "plants", nil, 3), nil); code != 200 {
		t.Fatal(code)
	}
	follower := startNode(t, func(cfg *meta.Config) { dir = cfg.DataDir; cfg.Peers = []string{peer.url} })
	adm2, _ := meta.OpenAdmin(context.Background(), dir, meta.Config{})
	defer adm2.Close()
	if _, err := adm2.Delist(context.Background(), c.owner.Public, ""); err != nil {
		t.Fatal(err)
	}
	follower.srv.ReplicateOnce(context.Background())
	if got := searchNames(t, follower, "Gardeners"); len(got) != 0 {
		t.Fatalf("replication brought a delisted community back: %v", got)
	}

	ds, _ := adm.Decisions(context.Background())
	if len(ds) != 1 || ds[0].Reason != "spam" || ds[0].Kind != "community" {
		t.Fatalf("decisions %+v", ds)
	}
	if ok, err := adm.Relist(context.Background(), c.owner.Public); !ok || err != nil {
		t.Fatalf("relist: %v %v", ok, err)
	}
	if code := n.post(t, "/v1/listings", c.listing("Gardeners", "plants", nil, 4), nil); code != 200 {
		t.Fatalf("re-post after relisting: %d", code)
	}
	if ok, _ := adm.Relist(context.Background(), c.owner.Public); ok {
		t.Fatal("relisted twice")
	}
	if _, err := adm.Delist(context.Background(), []byte("short"), ""); err == nil {
		t.Fatal("delisted a malformed id")
	}
	if _, err := adm.Delist(context.Background(), c.owner.Public, string(bytes.Repeat([]byte("x"), meta.MaxReason+1))); err == nil {
		t.Fatal("accepted an over-long reason")
	}
}

func TestAdminConfigDecisionsAreProtected(t *testing.T) {
	c := newCommunity(t)
	var dir string
	startNode(t, func(cfg *meta.Config) { dir = cfg.DataDir })
	cfg := meta.Config{Delisted: []string{identity.Encode(c.owner.Public)}, DelistedRelays: []string{"ws://bad.example.invalid/ws"}}
	adm, err := meta.OpenAdmin(context.Background(), dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer adm.Close()
	if _, err := adm.Relist(context.Background(), c.owner.Public); !errors.Is(err, meta.ErrInConfig) {
		t.Fatalf("relisting a config entry: %v", err)
	}
	if _, err := adm.RestoreRelay(context.Background(), "ws://bad.example.invalid/ws"); !errors.Is(err, meta.ErrInConfig) {
		t.Fatalf("restoring a config relay: %v", err)
	}
	ds, _ := adm.Decisions(context.Background())
	if len(ds) != 2 || !ds[0].FromConfig || !ds[1].FromConfig {
		t.Fatalf("decisions %+v", ds)
	}
	if _, err := meta.OpenAdmin(context.Background(), t.TempDir(), meta.Config{}); err == nil {
		t.Fatal("opened a directory without a database")
	}
}

func TestAdminRemoveRelayWhileRunning(t *testing.T) {
	var dir string
	n := startNode(t, func(c *meta.Config) { dir = c.DataDir })
	k, _ := identity.Generate()
	url := "wss://relay-1.example.invalid/ws"
	if code := n.post(t, "/v1/relays/heartbeat", heartbeat(k, url, time.Now()), nil); code != 200 {
		t.Fatal(code)
	}
	adm, _ := meta.OpenAdmin(context.Background(), dir, meta.Config{})
	defer adm.Close()
	rs, _ := adm.Relays(context.Background())
	if len(rs) != 1 || rs[0].URL != url {
		t.Fatalf("relays %+v", rs)
	}
	if had, err := adm.RemoveRelay(context.Background(), url, "drops traffic"); !had || err != nil {
		t.Fatalf("remove: %v %v", had, err)
	}
	var rl wire.RelayList
	n.get(t, "/v1/relays", &rl)
	if len(rl.Relays) != 0 {
		t.Fatalf("removed relay still listed: %v", rl.Relays)
	}
	if code := n.post(t, "/v1/relays/heartbeat", heartbeat(k, url, time.Now()), nil); code != http.StatusForbidden {
		t.Fatalf("heartbeat after removal: %d", code)
	}
	if _, err := adm.RemoveRelay(context.Background(), "http://not-a-relay", ""); err == nil {
		t.Fatal("removed a non-relay URL")
	}
	if ok, _ := adm.RestoreRelay(context.Background(), url); !ok {
		t.Fatal("restore failed")
	}
	if code := n.post(t, "/v1/relays/heartbeat", heartbeat(k, url, time.Now()), nil); code != 200 {
		t.Fatalf("heartbeat after restore: %d", code)
	}
}

// ---------------------------------------------------------------- snapshot

func TestDirectorySnapshot(t *testing.T) {
	n := startNode(t, func(c *meta.Config) { c.ListingsPerHour = 10_000; c.WritesPerMinute = 10_000 })
	icon := bytes.Repeat([]byte{0x89}, 200<<10)
	var owners [][]byte
	for i := range meta.SnapshotPageListings + 20 {
		c := newCommunity(t)
		sl := c.listing(fmt.Sprintf("Community %d", i), "about things", []string{"t"}, 1)
		if i == 0 {
			sum := sha256.Sum256(icon)
			sl = c.listingWithIcon("With icon", icon, sum[:])
		}
		if code := n.post(t, "/v1/listings", sl, nil); code != 200 {
			t.Fatalf("listing %d: %d", i, code)
		}
		owners = append(owners, c.owner.Public)
	}
	// A withdrawn listing isn't in the snapshot.
	gone := newCommunity(t)
	n.post(t, "/v1/listings", gone.listing("Gone", "", nil, 1), nil)
	wl, _ := proto.Marshal(&wire.Listing{Community: gone.owner.Public, ServerKey: gone.server.Public, Seq: 2, Withdrawn: true, PublishedAt: time.Now().UnixMilli()})
	n.post(t, "/v1/listings", &wire.SignedListing{Listing: wl, Signature: gone.server.Sign(identity.CtxListing, wl), Cert: gone.cert}, nil)

	seen := map[string]bool{}
	var after uint64
	pages := 0
	for {
		var snap wire.DirectorySnapshot
		if code := n.get(t, fmt.Sprintf("/v1/directory/snapshot?after=%d", after), &snap); code != 200 {
			t.Fatalf("snapshot: %d", code)
		}
		pages++
		if snap.Total != uint64(len(owners)) {
			t.Fatalf("total %d, want %d", snap.Total, len(owners))
		}
		if len(snap.Listings) > meta.SnapshotPageListings || proto.Size(&snap) > meta.SnapshotPageBytes+64<<10 {
			t.Fatalf("page too big: %d listings, %d bytes", len(snap.Listings), proto.Size(&snap))
		}
		for _, sl := range snap.Listings {
			if len(sl.Icon) != 0 {
				t.Fatal("snapshot carries an icon")
			}
			var l wire.Listing
			proto.Unmarshal(sl.Listing, &l)
			// Each listing still verifies on its own: the signature covers
			// the icon's hash, not the icon.
			if identity.Verify(l.ServerKey, identity.CtxListing, sl.Listing, sl.Signature) != nil {
				t.Fatal("snapshot listing doesn't verify")
			}
			if seen[string(l.Community)] {
				t.Fatal("listing repeated across pages")
			}
			seen[string(l.Community)] = true
		}
		if !snap.More {
			break
		}
		after = snap.Next
	}
	if pages < 2 || len(seen) != len(owners) || seen[string(gone.owner.Public)] {
		t.Fatalf("%d pages, %d listings", pages, len(seen))
	}
	// The full listing still carries the icon.
	var full wire.SignedListing
	n.get(t, "/v1/listings/"+identity.Encode(owners[0]), &full)
	if !bytes.Equal(full.Icon, icon) {
		t.Fatal("full listing lost its icon")
	}
	if code := n.get(t, "/v1/directory/snapshot?after=junk", nil); code != http.StatusBadRequest {
		t.Fatalf("bad cursor: %d", code)
	}
}

func TestListingIconBoundByHash(t *testing.T) {
	n := startNode(t, nil)
	c := newCommunity(t)
	icon := []byte("png bytes")
	sum := sha256.Sum256(icon)
	sl := c.listingWithIcon("Icons", icon, sum[:])
	sl.Icon = []byte("swapped")
	if code := n.post(t, "/v1/listings", sl, nil); code != http.StatusForbidden {
		t.Fatalf("swapped icon: %d", code)
	}
	sl.Icon = nil
	if code := n.post(t, "/v1/listings", sl, nil); code != http.StatusForbidden {
		t.Fatalf("missing icon: %d", code)
	}
	sl.Icon = bytes.Repeat([]byte{1}, meta.MaxIconBytes+1)
	if code := n.post(t, "/v1/listings", sl, nil); code != http.StatusBadRequest {
		t.Fatalf("huge icon: %d", code)
	}
	sl.Icon = icon
	if code := n.post(t, "/v1/listings", sl, nil); code != 200 {
		t.Fatalf("good icon: %d", code)
	}
}

func (c *community) listingWithIcon(name string, icon, sum []byte) *wire.SignedListing {
	l, _ := proto.Marshal(&wire.Listing{
		Community: c.owner.Public, ServerKey: c.server.Public, RouteId: c.route, Name: name, Seq: 1,
		Relays: []string{"ws://relay.example/ws"}, IconSha256: sum, PublishedAt: time.Now().UnixMilli(),
	})
	return &wire.SignedListing{Listing: l, Signature: c.server.Sign(identity.CtxListing, l), Cert: c.cert, Icon: icon}
}

// ---------------------------------------------------------------- versions

func TestAPIVersionNegotiation(t *testing.T) {
	n := startNode(t, nil)
	// A node one version ahead still serves today's clients.
	n.srv.SetVersions(version.SupportedFor(2))
	var sr wire.SearchResponse
	if code := n.get(t, "/v1/search", &sr); code != 200 {
		t.Fatalf("v1 client on a v2 node: %d", code)
	}
	// No version, version 0 or only unknown versions: 426, with the node's
	// versions.
	for _, h := range []string{"", "0", "7"} {
		req, _ := http.NewRequest(http.MethodGet, n.url+"/v1/search", nil)
		if h != "" {
			req.Header.Set(version.Header, h)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUpgradeRequired || resp.Header.Get(version.Header) != "1,2" {
			t.Fatalf("header %q: %d %q", h, resp.StatusCode, resp.Header.Get(version.Header))
		}
	}
	// A node that moved on (speaks only 2 and 3) tells a v1 client to update.
	n.srv.SetVersions([]uint32{2, 3})
	resp, err := apiClient.Get(n.url + "/v1/search")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var me *version.MismatchError
	if err := version.CheckResponse(resp); !errors.As(err, &me) || me.PeerOutdated {
		t.Fatalf("old client: %v", err)
	}
	// Health checks need no version.
	r2, _ := http.Get(n.url + "/healthz")
	r2.Body.Close()
	if r2.StatusCode != 200 {
		t.Fatal("healthz refused")
	}
}
