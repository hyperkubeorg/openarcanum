package community_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/meta"
	"github.com/hyperkubeorg/openarcanum/internal/meta/clusterset"
	"github.com/hyperkubeorg/openarcanum/internal/relay"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// startCluster runs two maintainer nodes of one cluster whose member list
// is published on the first only (as right after `meta set publish`).
func startCluster(t *testing.T) (m1, m2 *testutil.Meta, op *identity.Key) {
	t.Helper()
	op, _ = identity.Generate()
	var metas []*testutil.Meta
	var dirs []string
	for range 2 {
		cfg := meta.DefaultConfig()
		cfg.DirectoryEnabled = true // the tests exercise the directory; it's off by default
		cfg.DataDir = t.TempDir()
		cfg.ClusterOperatorKey = identity.Encode(op.Public)
		cfg.AllowPrivateRelays = true // test relays listen on 127.0.0.1
		srv, err := meta.Open(context.Background(), cfg, testutil.Quiet())
		if err != nil {
			t.Fatal(err)
		}
		hs := httptest.NewServer(srv.Handler())
		t.Cleanup(func() { hs.Close(); _ = srv.Close() })
		metas = append(metas, &testutil.Meta{URL: hs.URL, Server: srv})
		dirs = append(dirs, cfg.DataDir)
	}
	now := time.Now()
	set := &wire.MaintainerSet{Seq: 1, IssuedAt: now.UnixMilli(), ExpiresAt: now.Add(time.Hour).UnixMilli()}
	for i, m := range metas {
		role := clusterset.RoleMember
		if i == 0 {
			role = clusterset.RoleRoot
		}
		set.Nodes = append(set.Nodes, &wire.MaintainerNode{Url: m.URL, NodeKey: m.Server.NodeKey(), Role: role})
	}
	signed, err := clusterset.Sign(op, set)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := proto.Marshal(signed)
	adm, err := meta.OpenAdmin(context.Background(), dirs[0], meta.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer adm.Close()
	if _, err := adm.PublishSet(context.Background(), raw, op.Public); err != nil {
		t.Fatal(err)
	}
	return metas[0], metas[1], op
}

// A community server configured with one node of a maintainer cluster
// (URL#OPERATORKEY) publishes its route and heartbeats to every member of
// the cluster's verified member list; a relay registers with every member
// the same way.
func TestPublishesToEveryClusterMember(t *testing.T) {
	m1, m2, op := startCluster(t)
	entry := m1.URL + "#" + identity.Encode(op.Public)
	r := testutil.StartRelay(t)
	c := testutil.StartCommunity(t, func(cfg *community.Config) {
		cfg.Maintainers = []string{entry}
		cfg.HeartbeatInterval.Duration = time.Second
		cfg.PublishInterval.Duration = time.Second
	}, r.URL)
	claimedOn(t, r, c, nil)
	waitState(t, m2, c, wire.CommunityState_COMMUNITY_STATE_ONLINE)

	rcfg := relay.DefaultConfig()
	rcfg.RegistryURLs = []string{m1.URL}
	rcfg.MaintainerOperatorKey = identity.Encode(op.Public)
	rcfg.PublicURL = r.URL
	rcfg.HeartbeatInterval.Duration = time.Second
	if err := rcfg.Validate(); err != nil {
		t.Fatal(err)
	}
	hb, err := relay.NewHeartbeater(rcfg, r.Server, testutil.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go hb.Run(ctx)
	var list wire.RelayList
	waitFor(t, "relay registered on the other member", func() bool {
		list.Reset()
		return httpGet(t, m2.URL+"/v1/relays", &list) == 200 && len(list.Relays) == 1
	})
}

// A public community publishes its listing only to maintainer nodes whose
// directory is on; a node with it off still gets the route, and its refusal
// isn't an error.
func TestListingSkipsNodesWithoutDirectory(t *testing.T) {
	on := testutil.StartMeta(t, nil)
	off := testutil.StartMeta(t, func(c *meta.Config) { c.DirectoryEnabled = false })
	r := testutil.StartRelay(t)
	c := testutil.StartCommunity(t, func(cfg *community.Config) { cfg.Maintainers = []string{off.URL, on.URL} }, r.URL)
	owner := testutil.NewMember(t, "Owner")
	ctx := testutil.Ctx(t)
	if err := owner.Connect(ctx, r.URL, c.ClaimCode.ServerKey); err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	ownerKey, _ := identity.Generate()
	if _, err := owner.Claim(ctx, ownerKey, c.ClaimCode, &wire.CommunitySettings{
		Name: "Bell Ringers", Visibility: wire.Visibility_VISIBILITY_PUBLIC, JoinPolicy: wire.JoinPolicy_JOIN_POLICY_OPEN,
		MessageRetentionDays: 30, FileRetentionDays: 7,
	}); err != nil {
		t.Fatal(err)
	}
	var res wire.SearchResponse
	waitFor(t, "listing on the node with a directory", func() bool {
		res.Reset()
		return httpGet(t, on.URL+"/v1/search?q=bell", &res) == 200 && len(res.Listings) == 1
	})
	if httpGet(t, off.URL+"/v1/routes/"+identity.Encode(c.Server.RouteID()), nil) != 200 {
		t.Fatal("the route didn't reach the node without a directory")
	}
	if err := c.Server.PublishNow(ctx); err != nil {
		t.Fatalf("a node without a directory counts as a failure: %v", err)
	}
}

func TestMaintainerEntriesValidated(t *testing.T) {
	op, _ := identity.Generate()
	cfg := community.DefaultConfig(t.TempDir())
	cfg.RelayMode = "automatic"
	cfg.Maintainers = []string{"https://m.example.invalid#" + identity.Encode(op.Public)}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a URL#KEY entry: %v", err)
	}
	for _, bad := range []string{"https://m.example.invalid#short", "ftp://m.example.invalid"} {
		cfg.Maintainers = []string{bad}
		if cfg.Validate() == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	cfg.Maintainers = []string{"https://m.example.invalid"}
	cfg.MaintainerOperatorKey = "nope"
	if cfg.Validate() == nil {
		t.Error("a malformed maintainer_operator_key was accepted")
	}
}
