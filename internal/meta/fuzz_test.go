package meta_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// Fuzz targets for everything a maintainer node decodes from the network:
// the bodies of its write endpoints, the path and query of its read
// endpoints, and replication batches pulled from peers. They drive the real
// HTTP handler (no production code is bypassed). The invariant throughout:
// no panic, no 500 (malformed input is the client's error, never an internal
// one), and nothing that fails verification is ever stored.

func fuzzNode(f *testing.F, peers ...string) *meta.Server {
	cfg := meta.DefaultConfig()
	cfg.DirectoryEnabled = true // the tests exercise the directory; it's off by default
	cfg.DataDir = f.TempDir()
	cfg.Peers = peers
	cfg.ListingsPerHour, cfg.WritesPerMinute = 1<<30, 1<<30
	cfg.MailboxDropsPerMin, cfg.NameplatesPerMinute = 1<<30, 1<<30
	cfg.MailboxQuota, cfg.MailboxMaxItem = 1<<20, 64<<10
	srv, err := meta.Open(context.Background(), cfg, testutil.Quiet())
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { srv.Close() })
	return srv
}

func serve(t *testing.T, h http.Handler, method, target string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "http://meta.test/", bytes.NewReader(body))
	u, err := url.Parse(target)
	if err != nil {
		t.Fatalf("bad target %q: %v", target, err)
	}
	req.URL.Path, req.URL.RawPath, req.URL.RawQuery = u.Path, u.RawPath, u.RawQuery
	req.RequestURI = target
	version.SetRequest(req)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code >= 500 && w.Code != http.StatusInsufficientStorage {
		t.Fatalf("%s %s: %d %q", method, target, w.Code, w.Body.String())
	}
	return w
}

type signer struct {
	owner, server *identity.Key
	cert          *wire.SignedServerCert
	relay         *identity.Key
}

func newSigner(tb testing.TB) *signer {
	owner, _ := identity.Generate()
	server, _ := identity.Generate()
	relay, _ := identity.Generate()
	cert, err := identity.IssueServerCert(owner, server.Public, 1, nil, nil)
	if err != nil {
		tb.Fatal(err)
	}
	return &signer{owner: owner, server: server, cert: cert, relay: relay}
}

func (s *signer) listing(l []byte) *wire.SignedListing {
	return &wire.SignedListing{Listing: l, Signature: s.server.Sign(identity.CtxListing, l), Cert: s.cert}
}

func (s *signer) route(r []byte) *wire.SignedRoute {
	return &wire.SignedRoute{Route: r, Signature: s.server.Sign(identity.CtxRoute, r), Cert: s.cert}
}

func (s *signer) heartbeat(h []byte) *wire.SignedHeartbeat {
	return &wire.SignedHeartbeat{Heartbeat: h, Signature: s.relay.Sign(identity.CtxRelayHeartbeat, h)}
}

func mustMarshal(m proto.Message) []byte {
	b, err := proto.Marshal(m)
	if err != nil {
		panic(err)
	}
	return b
}

func (s *signer) validListing() []byte {
	return mustMarshal(&wire.Listing{
		Community: s.owner.Public, ServerKey: s.server.Public, RouteId: bytes.Repeat([]byte{1}, 16),
		Name: "Fuzz", Description: "d", Tags: []string{"a"}, Seq: 1, Relays: []string{"ws://r/ws"},
		JoinPolicy: wire.JoinPolicy_JOIN_POLICY_OPEN, PublishedAt: time.Now().UnixMilli(),
	})
}

func (s *signer) validRoute() []byte {
	return mustMarshal(&wire.Route{RouteId: bytes.Repeat([]byte{1}, 16), ServerKey: s.server.Public, Relays: []string{"ws://r/ws"}, Seq: 1,
		PublishedAt: time.Now().UnixMilli()})
}

func (s *signer) validHeartbeat() []byte {
	return mustMarshal(&wire.RelayHeartbeat{Url: "ws://relay.test/ws", Region: "eu", Capacity: 10, RelayKey: s.relay.Public, At: time.Now().UnixMilli()})
}

var writePaths = []string{
	"/v1/listings", "/v1/routes", "/v1/relays/heartbeat",
	"/v1/mailbox/drop", "/v1/mailbox/fetch", "/v1/mailbox/delete",
	"/v1/nameplates", "/v1/nameplates/claim",
}

// FuzzMetaWrite posts arbitrary bodies to every write endpoint.
func FuzzMetaWrite(f *testing.F) {
	s := newSigner(f)
	token := bytes.Repeat([]byte{4}, 32)
	seeds := [][]byte{
		mustMarshal(s.listing(s.validListing())),
		mustMarshal(s.route(s.validRoute())),
		mustMarshal(s.heartbeat(s.validHeartbeat())),
		mustMarshal(&wire.MailboxDrop{Inbox: meta.InboxAddress(token), Blob: []byte("mail")}),
		mustMarshal(&wire.MailboxFetch{Token: token}),
		mustMarshal(&wire.MailboxDelete{Token: token, Ids: []int64{1, 2, -1}}),
		mustMarshal(&wire.NameplateRequest{Relay: "ws://r/ws"}),
		mustMarshal(&wire.NameplateClaim{Number: 1}),
	}
	for i, b := range seeds {
		f.Add(uint8(i), b)
	}
	f.Add(uint8(0), []byte{0xff, 0xff})
	f.Add(uint8(3), []byte{})
	h := fuzzNode(f).Handler()
	f.Fuzz(func(t *testing.T, which uint8, body []byte) {
		serve(t, h, http.MethodPost, writePaths[int(which)%len(writePaths)], body)
	})
}

// FuzzMetaSigned signs arbitrary listing, route and heartbeat bytes with
// valid keys, so the checks after the signature (field ranges, sequence
// numbers, storage, search indexing) see fuzzed content. A malicious
// community or relay operator can produce exactly these.
func FuzzMetaSigned(f *testing.F) {
	s := newSigner(f)
	f.Add(uint8(0), s.validListing())
	f.Add(uint8(1), s.validRoute())
	f.Add(uint8(2), s.validHeartbeat())
	f.Add(uint8(0), mustMarshal(&wire.Listing{Community: s.owner.Public, ServerKey: s.server.Public, Withdrawn: true, Seq: 9}))
	f.Add(uint8(0), mustMarshal(&wire.Listing{Community: s.owner.Public, ServerKey: s.server.Public, Name: strings.Repeat("n", 401)}))
	f.Add(uint8(2), mustMarshal(&wire.RelayHeartbeat{Url: "http://not-ws", RelayKey: s.relay.Public}))
	h := fuzzNode(f).Handler()
	f.Fuzz(func(t *testing.T, which uint8, inner []byte) {
		var path string
		var body []byte
		switch which % 3 {
		case 0:
			path, body = "/v1/listings", mustMarshal(s.listing(inner))
		case 1:
			path, body = "/v1/routes", mustMarshal(s.route(inner))
		default:
			path, body = "/v1/relays/heartbeat", mustMarshal(s.heartbeat(inner))
		}
		serve(t, h, http.MethodPost, path, body)
		// Whatever was accepted is served back intact.
		serve(t, h, http.MethodGet, "/v1/listings/"+identity.Encode(s.owner.Public), nil)
		serve(t, h, http.MethodGet, "/v1/search?q=fuzz", nil)
		serve(t, h, http.MethodGet, "/v1/relays", nil)
	})
}

// FuzzMetaRead requests every read endpoint with arbitrary path ids and
// query values.
func FuzzMetaRead(f *testing.F) {
	s := newSigner(f)
	f.Add(uint8(0), identity.Encode(s.owner.Public))
	f.Add(uint8(1), identity.Encode(bytes.Repeat([]byte{1}, 16)))
	f.Add(uint8(2), "garden tags:local \"quoted\" OR * NEAR(a b) -x")
	f.Add(uint8(3), "eu")
	f.Add(uint8(4), "-1")
	f.Add(uint8(4), "99999999999999999999")
	f.Add(uint8(0), "%%%")
	srv := fuzzNode(f)
	h := srv.Handler()
	// Something to find.
	signerTB := &testing.T{}
	_ = signerTB
	f.Fuzz(func(t *testing.T, which uint8, arg string) {
		var target string
		switch which % 5 {
		case 0:
			target = "/v1/listings/" + url.PathEscape(arg)
		case 1:
			target = "/v1/routes/" + url.PathEscape(arg)
		case 2:
			target = "/v1/search?q=" + url.QueryEscape(arg)
		case 3:
			target = "/v1/relays?region=" + url.QueryEscape(arg)
		default:
			target = "/v1/replicate?cursor=" + url.QueryEscape(arg)
		}
		serve(t, h, http.MethodGet, target, nil)
	})
}

// FuzzMetaReplication serves arbitrary replication batches from a fake peer.
// The node must store only entries that verify: afterwards everything it
// would replicate onwards is checked again here.
func FuzzMetaReplication(f *testing.F) {
	s := newSigner(f)
	f.Add(mustMarshal(&wire.ReplicationBatch{
		Cursor:     3,
		Listings:   []*wire.SignedListing{s.listing(s.validListing())},
		Routes:     []*wire.SignedRoute{s.route(s.validRoute())},
		Heartbeats: []*wire.SignedHeartbeat{s.heartbeat(s.validHeartbeat())},
	}))
	bad := s.listing(s.validListing())
	bad.Signature[0] ^= 1
	f.Add(mustMarshal(&wire.ReplicationBatch{Cursor: 1 << 62, Listings: []*wire.SignedListing{bad}}))
	f.Add([]byte{0x08})

	var batch []byte
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-protobuf")
		_, _ = w.Write(batch)
	}))
	f.Cleanup(peer.Close)
	srv := fuzzNode(f, peer.URL)
	h := srv.Handler()

	f.Fuzz(func(t *testing.T, data []byte) {
		batch = data
		srv.ReplicateOnce(context.Background())
		w := serve(t, h, http.MethodGet, "/v1/replicate?cursor=0", nil)
		var out wire.ReplicationBatch
		if err := proto.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("own replication batch: %v", err)
		}
		for _, sl := range out.Listings {
			var l wire.Listing
			if proto.Unmarshal(sl.Listing, &l) != nil {
				t.Fatal("stored listing does not parse")
			}
			if _, err := identity.VerifyServerCert(sl.Cert, l.ServerKey, l.Community); err != nil {
				t.Fatalf("stored listing with a bad cert: %v", err)
			}
			if identity.Verify(l.ServerKey, identity.CtxListing, sl.Listing, sl.Signature) != nil {
				t.Fatal("stored listing with a bad signature")
			}
		}
		for _, sr := range out.Routes {
			var r wire.Route
			if proto.Unmarshal(sr.Route, &r) != nil {
				t.Fatal("stored route does not parse")
			}
			if _, err := identity.VerifyServerCert(sr.Cert, r.ServerKey, nil); err != nil {
				t.Fatalf("stored route with a bad cert: %v", err)
			}
			if identity.Verify(r.ServerKey, identity.CtxRoute, sr.Route, sr.Signature) != nil {
				t.Fatal("stored route with a bad signature")
			}
		}
		for _, sh := range out.Heartbeats {
			var hb wire.RelayHeartbeat
			if proto.Unmarshal(sh.Heartbeat, &hb) != nil {
				t.Fatal("stored heartbeat does not parse")
			}
			if identity.Verify(hb.RelayKey, identity.CtxRelayHeartbeat, sh.Heartbeat, sh.Signature) != nil {
				t.Fatal("stored heartbeat with a bad signature")
			}
		}
		for _, sh := range out.CommunityHeartbeats {
			var hb wire.CommunityHeartbeat
			if proto.Unmarshal(sh.Heartbeat, &hb) != nil {
				t.Fatal("stored community heartbeat does not parse")
			}
			if identity.Verify(hb.ServerKey, identity.CtxCommunityHeartbeat, sh.Heartbeat, sh.Signature) != nil {
				t.Fatal("stored community heartbeat with a bad signature")
			}
		}
	})
}

// FuzzMetaCommunityHeartbeat signs arbitrary community heartbeat bytes with
// the server key of a stored route, so everything after the signature check
// (field ranges, times, the route's key, storage, the status endpoint) sees
// fuzzed content, as a community server's operator could send it.
func FuzzMetaCommunityHeartbeat(f *testing.F) {
	s := newSigner(f)
	routeID := bytes.Repeat([]byte{1}, 16)
	f.Add(mustMarshal(&wire.CommunityHeartbeat{RouteId: routeID, ServerKey: s.server.Public, At: time.Now().UnixMilli(), RelaysAttached: 1, RelaysWanted: 2}))
	f.Add(mustMarshal(&wire.CommunityHeartbeat{RouteId: routeID, ServerKey: s.server.Public, At: -1, Stopping: true}))
	f.Add(mustMarshal(&wire.CommunityHeartbeat{RouteId: routeID, ServerKey: s.relay.Public, At: 1 << 62, RelaysWanted: 1 << 31}))
	f.Add([]byte{0xff})
	h := fuzzNode(f).Handler()
	route := mustMarshal(&wire.Route{RouteId: routeID, ServerKey: s.server.Public, Relays: []string{"ws://r/ws"}, Seq: 1, PublishedAt: time.Now().UnixMilli()})
	f.Fuzz(func(t *testing.T, inner []byte) {
		serve(t, h, http.MethodPost, "/v1/routes", mustMarshal(s.route(route)))
		body := mustMarshal(&wire.SignedCommunityHeartbeat{Heartbeat: inner, Signature: s.server.Sign(identity.CtxCommunityHeartbeat, inner)})
		serve(t, h, http.MethodPost, "/v1/communities/heartbeat", body)
		w := serve(t, h, http.MethodGet, "/v1/communities/"+identity.Encode(routeID)+"/status", nil)
		var st wire.CommunityStatus
		if w.Code == http.StatusOK && proto.Unmarshal(w.Body.Bytes(), &st) != nil {
			t.Fatal("status does not parse")
		}
	})
}
