package meta_test

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/meta"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// syncAll runs peer rounds on every node until nothing changes (bounded).
func syncAll(t *testing.T, nodes ...*clusterNode) {
	t.Helper()
	ctx := context.Background()
	for range 4 {
		for _, n := range nodes {
			if !n.down.Load() {
				n.srv.PeerSyncOnce(ctx)
			}
		}
	}
}

func texts(items []*wire.MailboxItem) []string {
	out := mailTexts(items)
	sort.Strings(out)
	return out
}

func deleteMail(t *testing.T, n *clusterNode, token []byte, ids []int64, gids [][]byte) {
	t.Helper()
	if code := n.post(t, "/v1/mailbox/delete", &wire.MailboxDelete{Token: token, Ids: ids, Gids: gids}, nil); code != 200 {
		t.Fatalf("delete: %d", code)
	}
}

// A drop on one member reaches the other; a delete on the other removes it
// on the first, and stays deleted when the first pulls everything again.
func TestClusterMailboxBothWays(t *testing.T) {
	ctx := context.Background()
	a, b := pair(t, nil)
	alice := blobToken(0xa1)
	a.drop(t, alice, []byte("dropped on a"))
	b.drop(t, alice, []byte("dropped on b"))
	syncAll(t, a, b)
	for _, n := range []*clusterNode{a, b} {
		got := n.fetchMail(t, alice)
		if want := []string{"dropped on a", "dropped on b"}; !slices.Equal(texts(got), want) {
			t.Fatalf("%s holds %q", n.url, texts(got))
		}
		for _, it := range got {
			if !bytes.Equal(it.Gid, meta.MailboxGID(meta.InboxAddress(alice), it.Blob)) {
				t.Fatal("fetch doesn't carry the global id")
			}
		}
	}
	onB := b.fetchMail(t, alice)
	deleteMail(t, b, alice, []int64{onB[0].Id, onB[1].Id}, nil)
	syncAll(t, a, b)
	if got := a.fetchMail(t, alice); len(got) != 0 {
		t.Fatalf("a delete on b didn't reach a: %q", texts(got))
	}
	// a pulls b's whole log again: the deleted items don't come back.
	if _, err := a.srv.ResetPeerCursors(ctx); err != nil {
		t.Fatal(err)
	}
	syncAll(t, a, b)
	if got := a.fetchMail(t, alice); len(got) != 0 {
		t.Fatalf("deleted items came back on a full re-pull: %q", texts(got))
	}
	// Dropping a deleted message again (a late copy from its sender) doesn't
	// bring it back either.
	a.drop(t, alice, []byte("dropped on a"))
	if got := a.fetchMail(t, alice); len(got) != 0 {
		t.Fatal("a late copy of a deleted message came back")
	}
}

// Route-around: the sender reaches only a, the recipient's other device
// only b, and a and b can't reach each other. Writes on both sides, a
// delete on one; after the heal both hold exactly the items not deleted,
// the recipient sees each item once, and the deleted one never reappears.
func TestClusterRouteAroundPartitionHeal(t *testing.T) {
	a, b := pair(t, nil)
	bob := blobToken(0xb0)
	syncAll(t, a, b)
	a.partition(true)
	b.partition(true)
	a.drop(t, bob, []byte("X via a"))
	b.drop(t, bob, []byte("Y via b"))
	syncAll(t, a, b) // nothing moves
	if got := texts(a.fetchMail(t, bob)); !slices.Equal(got, []string{"X via a"}) {
		t.Fatalf("partitioned a holds %q", got)
	}

	// The recipient reads both members it can reach and merges by global
	// id, handling each item once; it then deletes X where it read it.
	handled := map[string]int{}
	var x *wire.MailboxItem
	for _, n := range []*clusterNode{a, b} {
		for _, it := range n.fetchMail(t, bob) {
			handled[string(it.Gid)]++
			if string(it.Blob) == "X via a" {
				x = it
			}
		}
	}
	deleteMail(t, a, bob, []int64{x.Id}, nil)

	a.partition(false)
	b.partition(false)
	syncAll(t, a, b)
	for _, n := range []*clusterNode{a, b} {
		got := n.fetchMail(t, bob)
		if !slices.Equal(texts(got), []string{"Y via b"}) {
			t.Fatalf("after the heal %s holds %q", n.url, texts(got))
		}
		for _, it := range got {
			handled[string(it.Gid)]++
		}
	}
	// Y was read on b before the heal and again on both after it, always
	// under one global id; X under one id, once.
	if len(handled) != 2 || handled[string(x.Gid)] != 1 {
		t.Fatalf("items by global id: %v", handled)
	}
	// More rounds change nothing: X stays deleted everywhere.
	syncAll(t, a, b)
	for _, n := range []*clusterNode{a, b} {
		if got := texts(n.fetchMail(t, bob)); !slices.Equal(got, []string{"Y via b"}) {
			t.Fatalf("%s holds %q", n.url, got)
		}
	}
}

// A delete by global id on a member that never served the item (the
// recipient read it elsewhere) takes effect there and travels on.
func TestClusterDeleteByGlobalID(t *testing.T) {
	a, b := pair(t, nil)
	bob := blobToken(0xb1)
	a.partition(true)
	b.partition(true)
	a.drop(t, bob, []byte("on a only"))
	got := a.fetchMail(t, bob)
	// The recipient deletes it by gid on b too, where it hasn't arrived.
	deleteMail(t, b, bob, nil, [][]byte{got[0].Gid})
	a.partition(false)
	b.partition(false)
	syncAll(t, a, b)
	for _, n := range []*clusterNode{a, b} {
		if got := n.fetchMail(t, bob); len(got) != 0 {
			t.Fatalf("%s still holds %q", n.url, texts(got))
		}
	}
}

// The same message dropped on both members (a sender writing to two) is
// one item on each after replication.
func TestClusterSameMessageStoredOnce(t *testing.T) {
	a, b := pair(t, nil)
	bob := blobToken(0xb2)
	a.partition(true)
	b.partition(true)
	a.drop(t, bob, []byte("sent to both"))
	b.drop(t, bob, []byte("sent to both"))
	a.drop(t, bob, []byte("sent to both")) // a retry
	a.partition(false)
	b.partition(false)
	syncAll(t, a, b)
	for _, n := range []*clusterNode{a, b} {
		if got := n.fetchMail(t, bob); len(got) != 1 {
			t.Fatalf("%s holds %d copies", n.url, len(got))
		}
	}
}

// A cluster that isn't fully connected converges: a trusts b, b trusts c,
// a and c don't know each other.
func TestClusterChainConverges(t *testing.T) {
	a := startClusterNode(t, t.TempDir(), nil)
	b := startClusterNode(t, t.TempDir(), nil)
	c := startClusterNode(t, t.TempDir(), nil)
	trust(t, a, b)
	trust(t, b, a)
	trust(t, b, c)
	trust(t, c, b)
	bob := blobToken(0xb3)
	a.drop(t, bob, []byte("from the far end"))
	syncAll(t, a, b, c)
	got := c.fetchMail(t, bob)
	if len(got) != 1 {
		t.Fatal("an item didn't travel two hops")
	}
	deleteMail(t, c, bob, []int64{got[0].Id}, nil)
	syncAll(t, a, b, c)
	if got := a.fetchMail(t, bob); len(got) != 0 {
		t.Fatalf("a delete didn't travel two hops back: a %+v, b %+v, c %+v", peerStatus(t, a), peerStatus(t, b), peerStatus(t, c))
	}
}

// An item that doesn't fit a member's quota is refused there only, and
// doesn't stall replication of what follows.
func TestClusterQuotaRefusalDoesNotStall(t *testing.T) {
	a := startClusterNode(t, t.TempDir(), nil)
	b := startClusterNode(t, t.TempDir(), func(c *meta.Config) { c.MailboxQuota = 1 << 20; c.MailboxMaxItem = 600 << 10 })
	trust(t, a, b)
	trust(t, b, a)
	big := blobToken(0xb4)
	small := blobToken(0xb5)
	for i := range 3 {
		a.drop(t, big, bytes.Repeat([]byte{byte(i)}, 500<<10))
	}
	a.drop(t, small, []byte("after the big ones"))
	syncAll(t, a, b)
	if got := b.fetchMail(t, big); len(got) != 2 {
		t.Fatalf("b holds %d of the inbox's items, quota fits 2", len(got))
	}
	if got := b.fetchMail(t, small); len(got) != 1 {
		t.Fatal("an item after a refused one never arrived")
	}
	if st := peerStatus(t, b)[a.url]; st.Lag() != 0 {
		t.Fatalf("b's cursor stalled: %+v", st)
	}
}

// A member that pushes more than its limits is throttled: the rest waits
// for the next window, and nothing is skipped.
func TestClusterPeerThrottled(t *testing.T) {
	clock := &clock{t: time.Now()}
	a := startClusterNode(t, t.TempDir(), nil)
	b := startClusterNode(t, t.TempDir(), func(c *meta.Config) { c.PeerItemsPerMinute = 3 })
	b.srv.SetClock(clock.now)
	trust(t, a, b)
	trust(t, b, a)
	bob := blobToken(0xb6)
	for i := range 7 {
		a.drop(t, bob, []byte(fmt.Sprintf("item %d", i)))
	}
	syncAll(t, b)
	if got := b.fetchMail(t, bob); len(got) != 3 {
		t.Fatalf("b took %d items in a minute, limit 3", len(got))
	}
	st := peerStatus(t, b)[a.url]
	if st.ThrottledUntil.IsZero() || st.Lag() == 0 {
		t.Fatalf("throttle not recorded: %+v", st)
	}
	clock.advance(61 * time.Second)
	syncAll(t, b)
	clock.advance(61 * time.Second)
	syncAll(t, b)
	if got := b.fetchMail(t, bob); len(got) != 7 {
		t.Fatalf("after the windows b holds %d of 7", len(got))
	}
}

// A member restored from a backup gets back, from the other member, the
// items it lost (those after the backup), and its peers re-pull its log
// from the start (new epoch) without duplicates.
func TestClusterRestoredMemberResyncs(t *testing.T) {
	ctx := context.Background()
	a, b := pair(t, nil)
	bob := blobToken(0xb7)
	a.drop(t, bob, []byte("before the backup"))
	syncAll(t, a, b)
	backup, err := meta.Backup(ctx, a.dir, filepath.Join(t.TempDir(), "b"), nil)
	if err != nil {
		t.Fatal(err)
	}
	a.drop(t, bob, []byte("after the backup"))
	syncAll(t, a, b)

	// a is lost and restored on new hardware: new keys, so b must trust
	// it again (as the operator would re-sign the set).
	dir := filepath.Join(t.TempDir(), "restored")
	if _, err := meta.Restore(ctx, backup.Path, dir); err != nil {
		t.Fatal(err)
	}
	a.dir, a.cfg.DataDir = dir, dir
	a.open(t)
	trust(t, b, a)
	a.drop(t, bob, []byte("on the restored node"))
	syncAll(t, a, b)
	for _, n := range []*clusterNode{a, b} {
		want := []string{"after the backup", "before the backup", "on the restored node"}
		if got := texts(n.fetchMail(t, bob)); !slices.Equal(got, want) {
			t.Fatalf("%s holds %q", n.url, got)
		}
	}
}

// Kill the root: everything it had is served by the other member, which
// takes new mail and deletes on its own; when the root comes back, they
// converge.
func TestClusterKillTheRoot(t *testing.T) {
	root, m2 := pair(t, nil)
	bob := blobToken(0xb8)
	root.drop(t, bob, []byte("before the root died"))
	syncAll(t, root, m2)
	root.kill()
	got := m2.fetchMail(t, bob)
	if len(got) != 1 {
		t.Fatal("the surviving member doesn't serve the root's mail")
	}
	m2.drop(t, bob, []byte("while the root is dead"))
	deleteMail(t, m2, bob, []int64{got[0].Id}, nil)
	syncAll(t, root, m2)
	if got := texts(m2.fetchMail(t, bob)); !slices.Equal(got, []string{"while the root is dead"}) {
		t.Fatalf("m2 holds %q", got)
	}
	root.down.Store(false)
	syncAll(t, root, m2)
	if got := texts(root.fetchMail(t, bob)); !slices.Equal(got, []string{"while the root is dead"}) {
		t.Fatalf("the root came back holding %q", got)
	}
}
