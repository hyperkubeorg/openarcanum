package meta_test

import (
	"context"
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/meta"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// With the public directory off (directory_enabled false) a node refuses
// listings with its own status, answers search, snapshots and listings the
// same, says so at /v1/node, and keeps serving routes.
func TestDirectoryDisabledInConfig(t *testing.T) {
	n := startNode(t, func(c *meta.Config) { c.DirectoryEnabled = false })
	c := newCommunity(t)
	if code := n.post(t, "/v1/listings", c.listing("Hidden", "", nil, 1), nil); code != meta.DirectoryOff {
		t.Fatalf("listing post: %d", code)
	}
	for _, path := range []string{"/v1/search?q=hidden", "/v1/directory/snapshot", "/v1/listings/" + identity.Encode(c.owner.Public)} {
		if code := n.get(t, path, nil); code != meta.DirectoryOff {
			t.Fatalf("%s: %d", path, code)
		}
	}
	var info wire.NodeInfo
	if n.get(t, "/v1/node", &info); info.Directory {
		t.Fatal("/v1/node says the directory is on")
	}
	if code := n.post(t, "/v1/routes", c.signedRoute(1), nil); code != 200 {
		t.Fatalf("routes are affected: %d", code)
	}
	if n.get(t, "/v1/routes/"+identity.Encode(c.route), nil) != 200 {
		t.Fatal("route not served")
	}
}

// `meta directory disable` on a running node deletes the stored listings
// and switches it off at once; listings replicated from a peer that still
// lists don't come back, and the node passes none on; `enable` switches it
// back on.
func TestDirectoryDisabledAtRunTime(t *testing.T) {
	ctx := context.Background()
	peer := startClusterNode(t, t.TempDir(), nil)
	n := startClusterNode(t, t.TempDir(), func(c *meta.Config) { c.Peers = []string{peer.hs.URL} })
	c := newCommunity(t)
	for _, x := range []*clusterNode{peer, n} {
		if code := x.post(t, "/v1/listings", c.listing("Listed", "", nil, 1), nil); code != 200 {
			t.Fatal(code)
		}
	}
	adm, err := meta.OpenAdmin(ctx, n.dir, meta.Config{DirectoryEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	defer adm.Close()
	if removed, err := adm.DisableDirectory(ctx); err != nil || removed != 1 {
		t.Fatalf("disable: %d %v", removed, err)
	}
	if code := n.get(t, "/v1/search?q=listed", nil); code != meta.DirectoryOff {
		t.Fatalf("search after disable: %d", code)
	}
	n.srv.ResetReplicationCursors(ctx)
	n.srv.ReplicateOnce(ctx)
	if st, _ := adm.Directory(ctx); st.On {
		t.Fatal("directory still on")
	}
	ls, err := adm.Listings(ctx, "", 10)
	if err != nil || len(ls) != 0 {
		t.Fatalf("replication brought listings back: %v %v", ls, err)
	}
	var batch wire.ReplicationBatch
	if n.get(t, "/v1/replicate?cursor=0", &batch); len(batch.Listings) != 0 {
		t.Fatal("a node with the directory off passes listings on")
	}
	if err := adm.EnableDirectory(ctx); err != nil {
		t.Fatal(err)
	}
	if code := n.post(t, "/v1/listings", c.listing("Listed again", "", nil, 2), nil); code != 200 {
		t.Fatalf("listing after enable: %d", code)
	}
	// A node whose config has it off can't be switched on by the command.
	off, err := meta.OpenAdmin(ctx, n.dir, meta.Config{Listen: ":1"})
	if err != nil {
		t.Fatal(err)
	}
	defer off.Close()
	if err := off.EnableDirectory(ctx); err != meta.ErrInConfig {
		t.Fatalf("enable against the config: %v", err)
	}
}
