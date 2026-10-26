package meta_test

import (
	"bytes"
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/meta"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/version"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

type node struct {
	srv *meta.Server
	url string
	// uploads maps a blob id to the upload id of the last create for it.
	uploads map[string][]byte
}

func startNode(t *testing.T, mutate func(*meta.Config)) *node {
	t.Helper()
	cfg := meta.DefaultConfig()
	cfg.DirectoryEnabled = true // the tests exercise the directory; it's off by default
	cfg.DataDir = t.TempDir()
	// Relay URLs in these tests are names like ws://a/ws that point
	// nowhere: no public-address rule and no proof of control (the tests
	// of those turn them back on).
	cfg.AllowPrivateRelays = true
	if mutate != nil {
		mutate(&cfg)
	}
	srv, err := meta.Open(context.Background(), cfg, testutil.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	srv.SetRelayProof(func(context.Context, string, []byte) error { return nil })
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { hs.Close(); srv.Close() })
	return &node{srv: srv, url: hs.URL}
}

func (n *node) post(t *testing.T, path string, m proto.Message, out proto.Message) int {
	t.Helper()
	b, _ := proto.Marshal(m)
	return n.postRaw(t, path, b, out)
}

func (n *node) postRaw(t *testing.T, path string, b []byte, out proto.Message) int {
	t.Helper()
	resp, err := apiClient.Post(n.url+path, "application/x-protobuf", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode == 200 {
		if err := proto.Unmarshal(body, out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode
}

func (n *node) get(t *testing.T, path string, out proto.Message) int {
	t.Helper()
	resp, err := apiClient.Get(n.url + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode == 200 {
		if err := proto.Unmarshal(body, out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode
}

type community struct {
	owner, server *identity.Key
	cert          *wire.SignedServerCert
	route         []byte
}

func newCommunity(t *testing.T) *community {
	owner, _ := identity.Generate()
	server, _ := identity.Generate()
	cert, err := identity.IssueServerCert(owner, server.Public, 1, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &community{owner: owner, server: server, cert: cert, route: bytes.Repeat([]byte{1}, 16)}
}

func (c *community) listing(name, desc string, tags []string, seq uint64) *wire.SignedListing {
	l, _ := proto.Marshal(&wire.Listing{
		Community: c.owner.Public, ServerKey: c.server.Public, RouteId: c.route, Name: name, Description: desc,
		Tags: tags, Seq: seq, Relays: []string{"ws://relay.example/ws"}, JoinPolicy: wire.JoinPolicy_JOIN_POLICY_OPEN,
		PublishedAt: time.Now().UnixMilli(),
	})
	return &wire.SignedListing{Listing: l, Signature: c.server.Sign(identity.CtxListing, l), Cert: c.cert}
}

func (c *community) signedRoute(seq uint64) *wire.SignedRoute {
	r, _ := proto.Marshal(&wire.Route{RouteId: c.route, ServerKey: c.server.Public, Relays: []string{"ws://r/ws"}, Seq: seq, PublishedAt: time.Now().UnixMilli()})
	return &wire.SignedRoute{Route: r, Signature: c.server.Sign(identity.CtxRoute, r), Cert: c.cert}
}

func TestListingSignatureChecks(t *testing.T) {
	n := startNode(t, nil)
	c := newCommunity(t)
	good := c.listing("Rust Hobbyists", "We talk about rust", []string{"rust", "metal"}, 1)
	if code := n.post(t, "/v1/listings", good, nil); code != 200 {
		t.Fatalf("valid listing: %d", code)
	}
	// Altered after signing.
	altered := proto.Clone(good).(*wire.SignedListing)
	var l wire.Listing
	proto.Unmarshal(altered.Listing, &l)
	l.Name = "Totally Different"
	l.Seq = 2
	altered.Listing, _ = proto.Marshal(&l)
	if code := n.post(t, "/v1/listings", altered, nil); code != http.StatusForbidden {
		t.Fatalf("altered listing: %d", code)
	}
	// Unsigned.
	unsigned := proto.Clone(good).(*wire.SignedListing)
	unsigned.Signature = nil
	if code := n.post(t, "/v1/listings", unsigned, nil); code != http.StatusForbidden {
		t.Fatalf("unsigned listing: %d", code)
	}
	// Signed by a server key the owner never certified.
	rogue, _ := identity.Generate()
	l2, _ := proto.Marshal(&wire.Listing{Community: c.owner.Public, ServerKey: rogue.Public, Name: "Hijack", Seq: 9})
	if code := n.post(t, "/v1/listings", &wire.SignedListing{Listing: l2, Signature: rogue.Sign(identity.CtxListing, l2), Cert: c.cert}, nil); code != http.StatusForbidden {
		t.Fatalf("uncertified server key: %d", code)
	}
	// Replayed older listing.
	if code := n.post(t, "/v1/listings", good, nil); code != http.StatusConflict {
		t.Fatalf("replayed listing: %d", code)
	}
	var got wire.SignedListing
	if n.get(t, "/v1/listings/"+identity.Encode(c.owner.Public), &got) != 200 || !proto.Equal(&got, good) {
		t.Fatal("stored listing differs from what was signed")
	}
}

func TestSearch(t *testing.T) {
	n := startNode(t, nil)
	add := func(name, desc string, tags ...string) *community {
		c := newCommunity(t)
		if code := n.post(t, "/v1/listings", c.listing(name, desc, tags, 1), nil); code != 200 {
			t.Fatalf("listing %s: %d", name, code)
		}
		return c
	}
	add("Rust Hobbyists", "Metalworking and restoration", "metal")
	add("Rustaceans", "The Rust programming language", "programming", "rust")
	add("Knitting Circle", "Yarn, needles, patterns", "crafts")
	search := func(q string) []string {
		var out wire.SearchResponse
		if n.get(t, "/v1/search?"+q, &out) != 200 {
			t.Fatalf("search %q failed", q)
		}
		var names []string
		for _, sl := range out.Listings {
			var l wire.Listing
			proto.Unmarshal(sl.Listing, &l)
			// Results carry the original signatures.
			if err := identity.Verify(l.ServerKey, identity.CtxListing, sl.Listing, sl.Signature); err != nil {
				t.Fatal("search result signature doesn't verify")
			}
			names = append(names, l.Name)
		}
		return names
	}
	if got := search("q=rust"); len(got) != 2 {
		t.Fatalf("q=rust: %v", got)
	}
	if got := search("q=programming"); len(got) != 1 || got[0] != "Rustaceans" {
		t.Fatalf("q=programming (description): %v", got)
	}
	if got := search("tag=crafts"); len(got) != 1 || got[0] != "Knitting Circle" {
		t.Fatalf("tag=crafts: %v", got)
	}
	if got := search("q=rust&tag=metal"); len(got) != 1 || got[0] != "Rust Hobbyists" {
		t.Fatalf("q+tag: %v", got)
	}
	if got := search("q=knit"); len(got) != 1 {
		t.Fatalf("prefix: %v", got)
	}
	if got := search("q=" + `%22%29+OR+%2A`); len(got) != 0 {
		t.Fatalf("injection matched: %v", got)
	}
}

func TestWithdrawAndDelist(t *testing.T) {
	c := newCommunity(t)
	n := startNode(t, func(cfg *meta.Config) {})
	n.post(t, "/v1/listings", c.listing("Gone Soon", "", nil, 1), nil)
	l, _ := proto.Marshal(&wire.Listing{Community: c.owner.Public, ServerKey: c.server.Public, Seq: 2, Withdrawn: true, PublishedAt: time.Now().UnixMilli()})
	if code := n.post(t, "/v1/listings", &wire.SignedListing{Listing: l, Signature: c.server.Sign(identity.CtxListing, l), Cert: c.cert}, nil); code != 200 {
		t.Fatalf("withdrawal: %d", code)
	}
	var out wire.SearchResponse
	n.get(t, "/v1/search?q=gone", &out)
	if len(out.Listings) != 0 {
		t.Fatal("withdrawn listing still searchable")
	}
	d := startNode(t, func(cfg *meta.Config) { cfg.Delisted = []string{identity.Encode(c.owner.Public)} })
	if code := d.post(t, "/v1/listings", c.listing("Banned", "", nil, 3), nil); code != http.StatusForbidden {
		t.Fatalf("delisted community accepted: %d", code)
	}
}

func TestRoutes(t *testing.T) {
	n := startNode(t, nil)
	c := newCommunity(t)
	if code := n.post(t, "/v1/routes", c.signedRoute(1), nil); code != 200 {
		t.Fatalf("route: %d", code)
	}
	// Someone else can't take over the route id.
	other := newCommunity(t)
	other.route = c.route
	if code := n.post(t, "/v1/routes", other.signedRoute(5), nil); code != http.StatusForbidden {
		t.Fatalf("route hijack: %d", code)
	}
	// The owner moves the community to a new server key: accepted.
	newServer, _ := identity.Generate()
	cert2, _ := identity.IssueServerCert(c.owner, newServer.Public, 2, nil, nil)
	r, _ := proto.Marshal(&wire.Route{RouteId: c.route, ServerKey: newServer.Public, Relays: []string{"ws://new/ws"}, Seq: 2, PublishedAt: time.Now().UnixMilli()})
	if code := n.post(t, "/v1/routes", &wire.SignedRoute{Route: r, Signature: newServer.Sign(identity.CtxRoute, r), Cert: cert2}, nil); code != 200 {
		t.Fatalf("moved route: %d", code)
	}
	var got wire.SignedRoute
	if n.get(t, "/v1/routes/"+identity.Encode(c.route), &got) != 200 {
		t.Fatal("route lookup failed")
	}
	var rt wire.Route
	proto.Unmarshal(got.Route, &rt)
	if !bytes.Equal(rt.ServerKey, newServer.Public) {
		t.Fatal("route not updated")
	}
	if n.get(t, "/v1/routes/"+identity.Encode(bytes.Repeat([]byte{9}, 16)), nil) != 404 {
		t.Fatal("unknown route found")
	}
}

func heartbeat(k *identity.Key, url string, at time.Time) *wire.SignedHeartbeat {
	b, _ := proto.Marshal(&wire.RelayHeartbeat{Url: url, Region: "eu", Capacity: 100, RelayKey: k.Public, At: at.UnixMilli()})
	return &wire.SignedHeartbeat{Heartbeat: b, Signature: k.Sign(identity.CtxRelayHeartbeat, b)}
}

func TestRelayRegistry(t *testing.T) {
	now := time.Now()
	n := startNode(t, nil)
	n.srv.SetClock(func() time.Time { return now })
	k1, _ := identity.Generate()
	k2, _ := identity.Generate()
	if code := n.post(t, "/v1/relays/heartbeat", heartbeat(k1, "ws://a/ws", now), nil); code != 200 {
		t.Fatalf("heartbeat: %d", code)
	}
	if code := n.post(t, "/v1/relays/heartbeat", heartbeat(k2, "ws://a/ws", now), nil); code != http.StatusConflict {
		t.Fatalf("URL taken over by another key: %d", code)
	}
	n.post(t, "/v1/relays/heartbeat", heartbeat(k2, "ws://b/ws", now), nil)
	var list wire.RelayList
	n.get(t, "/v1/relays", &list)
	if len(list.Relays) != 2 {
		t.Fatalf("relays: %v", list.Relays)
	}
	now = now.Add(3 * time.Minute) // past the TTL
	list.Reset()
	n.get(t, "/v1/relays", &list)
	if len(list.Relays) != 0 {
		t.Fatal("stale relays still listed")
	}
	if code := n.post(t, "/v1/relays/heartbeat", heartbeat(k1, "ws://a/ws", now.Add(-10*time.Minute)), nil); code != http.StatusBadRequest {
		t.Fatalf("stale heartbeat accepted: %d", code)
	}
}

func TestMailbox(t *testing.T) {
	n := startNode(t, func(c *meta.Config) { c.MailboxQuota = 1 << 20; c.MailboxMaxItem = 600 << 10 })
	token := bytes.Repeat([]byte{4}, 32)
	inbox := meta.InboxAddress(token)
	for i := 0; i < 2; i++ {
		if code := n.post(t, "/v1/mailbox/drop", &wire.MailboxDrop{Inbox: inbox, Blob: bytes.Repeat([]byte{byte(i)}, 400<<10)}, nil); code != 200 {
			t.Fatalf("drop %d: %d", i, code)
		}
	}
	// (Different bytes: the same bytes again are the same message, which
	// the node already holds.)
	if code := n.post(t, "/v1/mailbox/drop", &wire.MailboxDrop{Inbox: inbox, Blob: bytes.Repeat([]byte{9}, 400<<10)}, nil); code != http.StatusInsufficientStorage {
		t.Fatalf("over quota: %d", code)
	}
	var items wire.MailboxItems
	if code := n.post(t, "/v1/mailbox/fetch", &wire.MailboxFetch{Token: token}, &items); code != 200 || len(items.Items) != 2 {
		t.Fatalf("fetch: %d %d", code, len(items.Items))
	}
	// The wrong token sees nothing.
	var none wire.MailboxItems
	n.post(t, "/v1/mailbox/fetch", &wire.MailboxFetch{Token: bytes.Repeat([]byte{5}, 32)}, &none)
	if len(none.Items) != 0 {
		t.Fatal("other token read the inbox")
	}
	n.post(t, "/v1/mailbox/delete", &wire.MailboxDelete{Token: token, Ids: []int64{items.Items[0].Id}}, nil)
	items.Reset()
	n.post(t, "/v1/mailbox/fetch", &wire.MailboxFetch{Token: token}, &items)
	if len(items.Items) != 1 {
		t.Fatalf("after delete: %d", len(items.Items))
	}
}

// A reader's after_id must never skip new mail: ids of deleted items don't
// come back, even when the newest item was deleted.
func TestMailboxIDsNeverReused(t *testing.T) {
	n := startNode(t, nil)
	token := bytes.Repeat([]byte{6}, 32)
	inbox := meta.InboxAddress(token)
	fetch := func(after int64) []*wire.MailboxItem {
		var items wire.MailboxItems
		if code := n.post(t, "/v1/mailbox/fetch", &wire.MailboxFetch{Token: token, AfterId: after}, &items); code != 200 {
			t.Fatalf("fetch: %d", code)
		}
		return items.Items
	}
	var last int64
	for round := 0; round < 3; round++ {
		if code := n.post(t, "/v1/mailbox/drop", &wire.MailboxDrop{Inbox: inbox, Blob: []byte{byte(round)}}, nil); code != 200 {
			t.Fatalf("drop: %d", code)
		}
		got := fetch(last)
		if len(got) != 1 || got[0].Blob[0] != byte(round) {
			t.Fatalf("round %d: reading after id %d found %d items", round, last, len(got))
		}
		last = got[0].Id
		n.post(t, "/v1/mailbox/delete", &wire.MailboxDelete{Token: token, Ids: []int64{last}}, nil)
	}
}

func TestNameplates(t *testing.T) {
	now := time.Now().Truncate(time.Millisecond)
	n := startNode(t, nil)
	n.srv.SetClock(func() time.Time { return now })
	var np wire.Nameplate
	if code := n.post(t, "/v1/nameplates", &wire.NameplateRequest{Relay: "ws://r/ws"}, &np); code != 200 || np.Number < 100_000 || np.Number > 999_999 || len(np.Topic) != 32 {
		t.Fatalf("create: %d %v", code, &np)
	}
	if d := time.UnixMilli(np.ExpiresAt).Sub(now); d != 5*time.Minute {
		t.Fatalf("lifetime %v, want 5m", d)
	}
	var claimed wire.Nameplate
	if code := n.post(t, "/v1/nameplates/claim", &wire.NameplateClaim{Number: np.Number}, &claimed); code != 200 || !bytes.Equal(claimed.Topic, np.Topic) {
		t.Fatalf("claim: %d", code)
	}
	if code := n.post(t, "/v1/nameplates/claim", &wire.NameplateClaim{Number: np.Number}, nil); code != http.StatusGone {
		t.Fatalf("second claim: %d", code)
	}
	var np2 wire.Nameplate
	n.post(t, "/v1/nameplates", &wire.NameplateRequest{Relay: "ws://r/ws"}, &np2)
	now = now.Add(5 * time.Minute)
	if code := n.post(t, "/v1/nameplates/claim", &wire.NameplateClaim{Number: np2.Number}, nil); code != http.StatusGone {
		t.Fatalf("expired claim: %d", code)
	}
	if code := n.post(t, "/v1/nameplates/claim", &wire.NameplateClaim{Number: 777}, nil); code != http.StatusNotFound {
		t.Fatalf("unknown nameplate: %d", code)
	}
}

func TestReplication(t *testing.T) {
	a := startNode(t, nil)
	b := startNode(t, func(c *meta.Config) { c.Peers = []string{a.url} })
	c := newCommunity(t)
	a.post(t, "/v1/listings", c.listing("Replicated", "copied between maintainers", nil, 1), nil)
	a.post(t, "/v1/routes", c.signedRoute(1), nil)
	k, _ := identity.Generate()
	a.post(t, "/v1/relays/heartbeat", heartbeat(k, "ws://x/ws", time.Now()), nil)
	b.srv.ReplicateOnce(context.Background())
	var out wire.SearchResponse
	b.get(t, "/v1/search?q=replicated", &out)
	if len(out.Listings) != 1 {
		t.Fatal("listing not replicated")
	}
	if b.get(t, "/v1/routes/"+identity.Encode(c.route), nil) != 200 {
		t.Fatal("route not replicated")
	}
	var list wire.RelayList
	b.get(t, "/v1/relays", &list)
	if len(list.Relays) != 1 {
		t.Fatal("relay not replicated")
	}
	// An update on A flows to B on the next round.
	a.post(t, "/v1/listings", c.listing("Replicated Again", "", nil, 2), nil)
	b.srv.ReplicateOnce(context.Background())
	out.Reset()
	b.get(t, "/v1/search?q=again", &out)
	if len(out.Listings) != 1 {
		t.Fatal("update not replicated")
	}
}

func TestConfigValidation(t *testing.T) {
	cfg := meta.DefaultConfig()
	cfg.DirectoryEnabled = true // the tests exercise the directory; it's off by default
	cfg.DataDir = "x"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if cfg.NameplateTTL.Duration != 5*time.Minute || cfg.MailboxTTL.Duration != 14*24*time.Hour || cfg.MailboxQuota != 100<<20 {
		t.Fatal("defaults differ from docs/settings.md")
	}
	bad := cfg
	bad.Peers = []string{"ftp://x"}
	if bad.Validate() == nil {
		t.Fatal("bad peer accepted")
	}
	bad = cfg
	bad.NameplateTTL.Duration = time.Second
	if bad.Validate() == nil || !strings.Contains(bad.Validate().Error(), "nameplate") {
		t.Fatal("tiny nameplate TTL accepted")
	}
}

// Routes and listings under owner sets: an added owner's cert is accepted
// for the same community; a removed owner's, or an older owner set, is not.
func TestRoutesAndListingsFollowOwnerSet(t *testing.T) {
	n := startNode(t, nil)
	c := newCommunity(t)
	if code := n.post(t, "/v1/routes", c.signedRoute(1), nil); code != 200 {
		t.Fatalf("route: %d", code)
	}
	if code := n.post(t, "/v1/listings", c.listing("Owners", "", nil, 1), nil); code != 200 {
		t.Fatalf("listing: %d", code)
	}
	set := identity.FounderSet(c.owner.Public)
	second, _ := identity.Generate()
	member, _ := identity.Generate()
	req, _ := identity.RequestOwnerKey(second, member, c.owner.Public)
	add, _ := identity.SignOwnerChange(c.owner, set, req, nil)
	if _, err := set.Apply(add); err != nil {
		t.Fatal(err)
	}
	chain := []*wire.SignedOwnerChange{add}
	// The added owner certifies the server key: accepted for the route the
	// founder registered, and for the listing.
	c.cert, _ = identity.IssueServerCert(second, c.server.Public, 2, set, chain)
	if code := n.post(t, "/v1/routes", c.signedRoute(2), nil); code != 200 {
		t.Fatalf("route certified by an added owner: %d", code)
	}
	if code := n.post(t, "/v1/listings", c.listing("Owners", "", nil, 2), nil); code != 200 {
		t.Fatalf("listing certified by an added owner: %d", code)
	}
	// Going back to the founder-only set (as if the second owner never
	// existed) is refused once the newer set is stored.
	c.cert, _ = identity.IssueServerCert(c.owner, c.server.Public, 3, nil, nil)
	if code := n.post(t, "/v1/routes", c.signedRoute(3), nil); code != http.StatusForbidden {
		t.Fatalf("route under an older owner set: %d", code)
	}
	if code := n.post(t, "/v1/listings", c.listing("Owners", "", nil, 3), nil); code != http.StatusForbidden {
		t.Fatalf("listing under an older owner set: %d", code)
	}
	// The second owner removes the founder; the founder's cert is refused
	// even with the current chain attached.
	rm, _ := identity.SignOwnerChange(second, set, nil, c.owner.Public)
	if _, err := set.Apply(rm); err != nil {
		t.Fatal(err)
	}
	chain = append(chain, rm)
	byFounder, _ := identity.IssueServerCert(c.owner, c.server.Public, 4, nil, nil)
	byFounder.OwnerChain = chain
	c.cert = byFounder
	if code := n.post(t, "/v1/routes", c.signedRoute(4), nil); code != http.StatusForbidden {
		t.Fatalf("route certified by a removed owner: %d", code)
	}
	c.cert, _ = identity.IssueServerCert(second, c.server.Public, 5, set, chain)
	if code := n.post(t, "/v1/routes", c.signedRoute(5), nil); code != 200 {
		t.Fatalf("route after removing the founder: %d", code)
	}
}

// A removed owner posts a route and a listing with a huge seq to a node that
// hasn't seen the removal. Seqs far ahead of the clock are refused, and an
// entry under the later owner set replaces one under an earlier set
// whatever its seq, so nothing can be frozen.
func TestRemovedOwnerCannotFreezeEntries(t *testing.T) {
	n := startNode(t, nil)
	c := newCommunity(t)
	set := identity.FounderSet(c.owner.Public)
	second, _ := identity.Generate()
	member, _ := identity.Generate()
	req, _ := identity.RequestOwnerKey(second, member, c.owner.Public)
	add, _ := identity.SignOwnerChange(c.owner, set, req, nil)
	if _, err := set.Apply(add); err != nil {
		t.Fatal(err)
	}
	chain := []*wire.SignedOwnerChange{add}
	// The founder, about to be removed, still signs under the set with both
	// owners; this node never sees the removal before the attack.
	c.cert, _ = identity.IssueServerCert(c.owner, c.server.Public, 2, set, chain)
	if code := n.post(t, "/v1/routes", c.signedRoute(math.MaxUint64-1), nil); code != http.StatusBadRequest {
		t.Fatalf("route with a seq near the maximum: %d", code)
	}
	if code := n.post(t, "/v1/listings", c.listing("Frozen", "", nil, math.MaxUint64-1), nil); code != http.StatusBadRequest {
		t.Fatalf("listing with a seq near the maximum: %d", code)
	}
	ahead := uint64(time.Now().Add(meta.MaxSeqAhead - time.Hour).UnixMilli())
	if code := n.post(t, "/v1/routes", c.signedRoute(ahead), nil); code != 200 {
		t.Fatalf("route with a seq inside the bound: %d", code)
	}
	if code := n.post(t, "/v1/listings", c.listing("Frozen", "", nil, ahead), nil); code != 200 {
		t.Fatalf("listing with a seq inside the bound: %d", code)
	}
	// The second owner removes the founder and publishes with an honest seq,
	// lower than the founder's: the later owner set wins.
	rm, _ := identity.SignOwnerChange(second, set, nil, c.owner.Public)
	if _, err := set.Apply(rm); err != nil {
		t.Fatal(err)
	}
	chain = append(chain, rm)
	c.cert, _ = identity.IssueServerCert(second, c.server.Public, 3, set, chain)
	now := uint64(time.Now().UnixMilli())
	if code := n.post(t, "/v1/routes", c.signedRoute(now), nil); code != 200 {
		t.Fatalf("route under the later owner set with a lower seq: %d", code)
	}
	if code := n.post(t, "/v1/listings", c.listing("Current", "", nil, now), nil); code != 200 {
		t.Fatalf("listing under the later owner set with a lower seq: %d", code)
	}
	var sr wire.SignedRoute
	n.get(t, "/v1/routes/"+identity.Encode(c.route), &sr)
	var rt wire.Route
	if err := proto.Unmarshal(sr.Route, &rt); err != nil || rt.Seq != now {
		t.Fatalf("stored route seq %d, want %d", rt.Seq, now)
	}
	// Within one owner set the seq still has to increase.
	if code := n.post(t, "/v1/routes", c.signedRoute(now), nil); code != http.StatusConflict {
		t.Fatalf("replayed route: %d", code)
	}
	if code := n.post(t, "/v1/listings", c.listing("Current", "", nil, now-1), nil); code != http.StatusConflict {
		t.Fatalf("older listing in the same set: %d", code)
	}
}

// apiClient announces this build's protocol versions (the maintainer API
// refuses requests without them).
var apiClient = &http.Client{Transport: version.Transport{}}
