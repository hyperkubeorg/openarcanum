package meta_test

import (
	"archive/tar"
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/meta"
	"github.com/hyperkubeorg/openarcanum/internal/meta/clusterset"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/version"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// A cluster test node: a fixed URL whose server can be swapped (a restore
// on the same address), cut off (a partition) or stopped (a dead node).
type clusterNode struct {
	*node
	dir     string
	cfg     meta.Config
	handler atomic.Pointer[http.Handler]
	// down refuses everything (a dead node); cut refuses peer sessions
	// only (a partition between nodes that clients can still reach).
	down atomic.Bool
	cut  atomic.Bool
	hs   *httptest.Server
}

func startClusterNode(t *testing.T, dir string, mutate func(*meta.Config)) *clusterNode {
	t.Helper()
	cn := &clusterNode{dir: dir}
	cn.hs = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cn.down.Load() || (cn.cut.Load() && r.URL.Path == "/v1/peer") {
			// A partition: the connection is cut.
			if hj, ok := w.(http.Hijacker); ok {
				if c, _, err := hj.Hijack(); err == nil {
					c.Close()
					return
				}
			}
			http.Error(w, "partitioned", http.StatusServiceUnavailable)
			return
		}
		(*cn.handler.Load()).ServeHTTP(w, r)
	}))
	t.Cleanup(cn.hs.Close)
	cn.cfg = meta.DefaultConfig()
	cn.cfg.DirectoryEnabled = true // the tests exercise the directory; it's off by default
	cn.cfg.DataDir = dir
	if mutate != nil {
		mutate(&cn.cfg)
	}
	cn.open(t)
	return cn
}

// partition cuts (or heals) this node off from its peers: open sessions
// end and new ones are refused, while clients still reach it.
func (cn *clusterNode) partition(on bool) {
	cn.cut.Store(on)
	if on {
		cn.srv.DropPeerSessions()
	}
}

// kill makes the node unreachable for everyone, as if it died.
func (cn *clusterNode) kill() {
	cn.down.Store(true)
	cn.srv.DropPeerSessions()
}

// trust makes a trust b (`meta trust add` on a's data directory).
func trust(t *testing.T, a, b *clusterNode) {
	t.Helper()
	adm, err := meta.OpenAdmin(context.Background(), a.dir, meta.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer adm.Close()
	if err := adm.TrustAdd(context.Background(), b.url, b.srv.NodeKey()); err != nil {
		t.Fatal(err)
	}
}

// pair starts two nodes that trust each other.
func pair(t *testing.T, mutate func(*meta.Config)) (*clusterNode, *clusterNode) {
	t.Helper()
	a := startClusterNode(t, t.TempDir(), mutate)
	b := startClusterNode(t, t.TempDir(), mutate)
	trust(t, a, b)
	trust(t, b, a)
	return a, b
}

func peerStatus(t *testing.T, n *clusterNode) map[string]meta.PeerStatus {
	t.Helper()
	ps, err := n.srv.PeerStatuses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]meta.PeerStatus{}
	for _, p := range ps {
		out[p.URL] = p
	}
	return out
}

// open (re)opens the node's server on its data directory.
func (cn *clusterNode) open(t *testing.T) {
	t.Helper()
	if cn.node != nil {
		cn.srv.Close()
	}
	log := testutil.Quiet()
	if os.Getenv("CLUSTER_TEST_LOG") != "" {
		log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})).With("node", cn.hs.URL)
	}
	srv, err := meta.Open(context.Background(), cn.cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	h := srv.Handler()
	cn.handler.Store(&h)
	cn.node = &node{srv: srv, url: cn.hs.URL}
}

// The public replication cursor survives a peer's restore from a backup:
// the restored peer's revision counter goes back to the backup's, and its
// new entries carry revisions the puller has already passed. The batch's
// epoch changes, so the puller starts over and gets them.
func TestReplicationCursorFollowsPeerRestore(t *testing.T) {
	ctx := context.Background()
	a := startClusterNode(t, t.TempDir(), nil)
	b := startNode(t, func(c *meta.Config) { c.Peers = []string{a.url} })

	first := newCommunity(t)
	a.post(t, "/v1/routes", first.signedRoute(1), nil)
	backup, err := meta.Backup(ctx, a.dir, filepath.Join(t.TempDir(), "b"), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Written after the backup, pulled by B, then lost with A.
	for range 3 {
		c := newCommunity(t)
		c.route = randBytes(16)
		a.post(t, "/v1/routes", c.signedRoute(1), nil)
	}
	b.srv.ReplicateOnce(ctx)

	// A is restored from the backup, on the same address.
	newDir := filepath.Join(t.TempDir(), "restored")
	if _, err := meta.Restore(ctx, backup.Path, newDir); err != nil {
		t.Fatal(err)
	}
	a.cfg.DataDir = newDir
	a.open(t)
	late := newCommunity(t)
	late.route = randBytes(16)
	if code := a.post(t, "/v1/routes", late.signedRoute(1), nil); code != 200 {
		t.Fatal(code)
	}
	b.srv.ReplicateOnce(ctx)
	if b.get(t, "/v1/routes/"+identity.Encode(late.route), nil) != 200 {
		t.Fatal("a route written on the restored peer never reached the puller")
	}
	var batch wire.ReplicationBatch
	if a.get(t, "/v1/replicate?cursor=0", &batch); batch.Epoch == 0 {
		t.Fatal("replication batches carry no epoch")
	}
}

// Two nodes that trust each other open a peer session and pull each
// other's log; the node keys are stable across restarts and served at
// GET /v1/node.
func TestPeerSessionRoundTrip(t *testing.T) {
	ctx := context.Background()
	a, b := pair(t, nil)
	var info wire.NodeInfo
	if a.get(t, "/v1/node", &info) != 200 || !bytes.Equal(info.NodeKey, a.srv.NodeKey()) || len(info.NoiseKey) != 32 {
		t.Fatal("GET /v1/node doesn't serve the node key")
	}
	a.srv.PeerSyncOnce(ctx)
	b.srv.PeerSyncOnce(ctx)
	for _, n := range []*clusterNode{a, b} {
		for url, st := range peerStatus(t, n) {
			if st.LastError != "" || st.LastOK.IsZero() || st.Epoch == 0 {
				t.Fatalf("pull from %s: %+v", url, st)
			}
		}
	}
	key := a.srv.NodeKey()
	a.open(t)
	if !bytes.Equal(a.srv.NodeKey(), key) {
		t.Fatal("the node key changed across a restart")
	}
}

// A node refuses a session from a key it doesn't trust, so trust that goes
// one way only moves nothing.
func TestPeerSessionNeedsTrustBothWays(t *testing.T) {
	ctx := context.Background()
	a := startClusterNode(t, t.TempDir(), nil)
	b := startClusterNode(t, t.TempDir(), nil)
	trust(t, a, b) // a trusts b; b doesn't trust a
	a.srv.PeerSyncOnce(ctx)
	st := peerStatus(t, a)[b.url]
	if !strings.Contains(st.LastError, "doesn't trust") || !st.LastOK.IsZero() {
		t.Fatalf("one-way trust: %+v", st)
	}
	// b never dials a: it trusts nobody.
	b.srv.PeerSyncOnce(ctx)
	if len(peerStatus(t, b)) != 0 {
		t.Fatal("a node pulled from a peer it doesn't trust")
	}
	trust(t, b, a)
	a.srv.PeerSyncOnce(ctx)
	if st := peerStatus(t, a)[b.url]; st.LastError != "" || st.LastOK.IsZero() {
		t.Fatalf("after mutual trust: %+v", st)
	}
}

// A node that answers at a trusted URL with another node key is refused
// (a moved or impersonated node).
func TestPeerSessionChecksTheNodeKey(t *testing.T) {
	ctx := context.Background()
	a := startClusterNode(t, t.TempDir(), nil)
	b := startClusterNode(t, t.TempDir(), nil)
	other, _ := identity.Generate()
	adm, err := meta.OpenAdmin(ctx, a.dir, meta.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := adm.TrustAdd(ctx, b.url, other.Public); err != nil {
		t.Fatal(err)
	}
	adm.Close()
	trust(t, b, a)
	a.srv.PeerSyncOnce(ctx)
	if st := peerStatus(t, a)[b.url]; !strings.Contains(st.LastError, "another node key") {
		t.Fatalf("impersonated peer: %+v", st)
	}
}

// Backups never carry the node's keys: a node restored or seeded from a
// backup gets keys of its own.
func TestBackupLeavesNodeKeysOut(t *testing.T) {
	ctx := context.Background()
	a := startClusterNode(t, t.TempDir(), nil)
	res, err := meta.Backup(ctx, a.dir, filepath.Join(t.TempDir(), "b"), nil)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		if strings.Contains(h.Name, "node.") {
			t.Fatalf("backup holds %s", h.Name)
		}
	}
	dir := filepath.Join(t.TempDir(), "restored")
	if _, err := meta.Restore(ctx, res.Path, dir); err != nil {
		t.Fatal(err)
	}
	key, _, err := meta.NodeKeys(dir)
	if err != nil || bytes.Equal(key, a.srv.NodeKey()) {
		t.Fatalf("restored node reuses the node key (%v)", err)
	}
}

// signSet signs a maintainer set naming the nodes (the first as root).
func signSet(t *testing.T, op *identity.Key, seq uint64, nodes ...*clusterNode) *wire.SignedMaintainerSet {
	t.Helper()
	set := &wire.MaintainerSet{Seq: seq, IssuedAt: time.Now().UnixMilli(), ExpiresAt: time.Now().Add(clusterset.DefaultLifetime).UnixMilli()}
	for i, n := range nodes {
		role := clusterset.RoleMember
		if i == 0 {
			role = clusterset.RoleRoot
		}
		set.Nodes = append(set.Nodes, &wire.MaintainerNode{Url: n.url, NodeKey: n.srv.NodeKey(), Role: role})
	}
	signed, err := clusterset.Sign(op, set)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func publishSet(t *testing.T, n *clusterNode, s *wire.SignedMaintainerSet) error {
	t.Helper()
	raw, _ := proto.Marshal(s)
	adm, err := meta.OpenAdmin(context.Background(), n.dir, meta.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer adm.Close()
	op, _ := identity.Decode(n.cfg.ClusterOperatorKey)
	_, err = adm.PublishSet(context.Background(), raw, op)
	return err
}

// The members of a cluster's newest maintainer set trust each other with
// no `meta trust`; the set reaches every member by replication and is
// served to clients; a set with a lower seq, for another operator key, or
// expired is refused; a member dropped from the set loses its sessions.
func TestMaintainerSetTrust(t *testing.T) {
	ctx := context.Background()
	op, _ := identity.Generate()
	opKey := identity.Encode(op.Public)
	a := startClusterNode(t, t.TempDir(), func(c *meta.Config) { c.ClusterOperatorKey = opKey })
	b := startClusterNode(t, t.TempDir(), func(c *meta.Config) { c.ClusterOperatorKey = opKey; c.Peers = []string{a.hs.URL} })
	outsider := startClusterNode(t, t.TempDir(), func(c *meta.Config) { c.ClusterOperatorKey = opKey; c.Peers = []string{a.hs.URL} })
	if err := publishSet(t, a, signSet(t, op, 2, a, b)); err != nil {
		t.Fatal(err)
	}
	b.srv.ReplicateOnce(ctx)
	outsider.srv.ReplicateOnce(ctx)
	var got wire.SignedMaintainerSet
	if b.get(t, "/v1/maintainers", &got) != 200 {
		t.Fatal("the set didn't replicate to the other member")
	}
	if set, err := clusterset.Verify(&got, op.Public, time.Now()); err != nil || set.Seq != 2 {
		t.Fatalf("served set: %v", err)
	}
	a.srv.PeerSyncOnce(ctx)
	b.srv.PeerSyncOnce(ctx)
	if st := peerStatus(t, a)[b.url]; st.LastError != "" || st.LastOK.IsZero() {
		t.Fatalf("set members don't trust each other: %+v", st)
	}
	// A node outside the set trusts the members, but they don't trust it.
	outsider.srv.PeerSyncOnce(ctx)
	if st := peerStatus(t, outsider)[a.url]; !strings.Contains(st.LastError, "doesn't trust") {
		t.Fatalf("a member trusted a node outside the set: %+v", st)
	}

	other, _ := identity.Generate()
	for name, s := range map[string]*wire.SignedMaintainerSet{
		"lower seq":      signSet(t, op, 1, a),
		"same seq":       signSet(t, op, 2, a),
		"other operator": signSet(t, other, 9, a),
	} {
		if err := publishSet(t, a, s); err == nil {
			t.Errorf("%s: published", name)
		}
	}
	expired := &wire.MaintainerSet{Seq: 9, IssuedAt: time.Now().Add(-48 * time.Hour).UnixMilli(), ExpiresAt: time.Now().Add(-time.Hour).UnixMilli(),
		Nodes: []*wire.MaintainerNode{{Url: a.url, NodeKey: a.srv.NodeKey(), Role: clusterset.RoleRoot}}}
	es, _ := clusterset.Sign(op, expired)
	if err := publishSet(t, a, es); err == nil {
		t.Error("an expired set was published")
	}

	// b is dropped from the cluster.
	if err := publishSet(t, a, signSet(t, op, 3, a)); err != nil {
		t.Fatal(err)
	}
	b.srv.PeerSyncOnce(ctx)
	if st := peerStatus(t, b)[a.url]; !strings.Contains(st.LastError, "doesn't trust") {
		t.Fatalf("a node dropped from the set still pulls: %+v", st)
	}
}

// maintainer_set_file is published when the node starts.
func TestMaintainerSetFile(t *testing.T) {
	op, _ := identity.Generate()
	opKey := identity.Encode(op.Public)
	a := startClusterNode(t, t.TempDir(), func(c *meta.Config) { c.ClusterOperatorKey = opKey })
	raw, _ := proto.Marshal(signSet(t, op, 1, a))
	path := filepath.Join(t.TempDir(), "set.bin")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	a.cfg.MaintainerSetFile = path
	a.open(t)
	if a.get(t, "/v1/maintainers", nil) != 200 {
		t.Fatal("maintainer_set_file wasn't published at start")
	}
	a.open(t) // the same set again is fine
}

// node_key_dir: keys made beforehand (a mounted Secret) are used as they
// are, and a node without them there refuses to start rather than make new
// ones its cluster doesn't know.
func TestNodeKeyDir(t *testing.T) {
	keys := t.TempDir()
	want, _, err := meta.NodeKeys(keys)
	if err != nil {
		t.Fatal(err)
	}
	n := startClusterNode(t, t.TempDir(), func(c *meta.Config) { c.NodeKeyDir = keys })
	if !bytes.Equal(n.srv.NodeKey(), want) {
		t.Fatal("node_key_dir's key wasn't used")
	}
	cfg := meta.DefaultConfig()
	cfg.DirectoryEnabled = true // the tests exercise the directory; it's off by default
	cfg.DataDir = t.TempDir()
	cfg.NodeKeyDir = t.TempDir()
	if _, err := meta.Open(context.Background(), cfg, nil); err == nil || !strings.Contains(err.Error(), "node_key_dir") {
		t.Fatalf("an empty node_key_dir: %v", err)
	}
}

// A member is reached at the URL in trusted_peers even when the maintainer
// set names another one (clients use the set's; nodes an internal one).
func TestTrustedPeerURLOverridesTheSet(t *testing.T) {
	ctx := context.Background()
	op, _ := identity.Generate()
	opKey := identity.Encode(op.Public)
	a := startClusterNode(t, t.TempDir(), func(c *meta.Config) { c.ClusterOperatorKey = opKey })
	b := startClusterNode(t, t.TempDir(), func(c *meta.Config) { c.ClusterOperatorKey = opKey })
	set := signSet(t, op, 1, a, b)
	// The set names an address the nodes can't use.
	var inner wire.MaintainerSet
	_ = proto.Unmarshal(set.Set, &inner)
	inner.Nodes[1].Url = "http://127.0.0.1:1"
	set, _ = clusterset.Sign(op, &inner)
	for _, n := range []*clusterNode{a, b} {
		if err := publishSet(t, n, set); err != nil {
			t.Fatal(err)
		}
	}
	a.srv.PeerSyncOnce(ctx)
	if st := peerStatus(t, a)["http://127.0.0.1:1"]; st.LastError == "" {
		t.Fatalf("the set's unusable address worked: %+v", st)
	}
	a.cfg.TrustedPeers = []string{b.url + "#" + identity.Encode(b.srv.NodeKey())}
	a.open(t)
	a.srv.PeerSyncOnce(ctx)
	if st := peerStatus(t, a)[b.url]; st.LastError != "" || st.LastOK.IsZero() {
		t.Fatalf("the trusted_peers address wasn't used: %+v", st)
	}
}

// Handshakes on /v1/peer run before the caller's key is known, so only a
// few may be under way per client network: stalled upgrades beyond that
// are refused at once, and a trusted peer gets through once one ends.
func TestPeerHandshakesBoundedPerNetwork(t *testing.T) {
	ctx := context.Background()
	a, b := pair(t, nil)
	h := http.Header{}
	h.Set(version.Header, version.Format(version.Supported()))
	var stalled []*websocket.Conn
	defer func() {
		for _, c := range stalled {
			_ = c.CloseNow()
		}
	}()
	for range 4 { // peerHandshakesPerNet
		c, _, err := websocket.Dial(ctx, a.url+"/v1/peer", &websocket.DialOptions{HTTPHeader: h})
		if err != nil {
			t.Fatal(err)
		}
		stalled = append(stalled, c)
	}
	_, resp, err := websocket.Dial(ctx, a.url+"/v1/peer", &websocket.DialOptions{HTTPHeader: h})
	if err == nil || resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("a fifth handshake from one network: %v %v", err, resp)
	}
	b.srv.PeerSyncOnce(ctx)
	if st := peerStatus(t, b)[a.url]; st.LastError == "" {
		t.Fatal("b's pull went through while the handshakes were held")
	}
	_ = stalled[0].CloseNow()
	deadline := time.Now().Add(5 * time.Second)
	for {
		b.srv.PeerSyncOnce(ctx)
		if st := peerStatus(t, b)[a.url]; st.LastError == "" && !st.LastOK.IsZero() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("b's pull after a handshake ended: %+v", peerStatus(t, b)[a.url])
		}
		time.Sleep(10 * time.Millisecond)
	}
}
