package meta_test

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/meta"
	"github.com/hyperkubeorg/openarcanum/internal/store"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// After a restore from an older backup, mailbox ids don't go backwards: a
// client that kept running across the node's loss holds a position
// (after_id) past every id in the backup, up to the last id the lost node
// issued, and must still get every item dropped after the restore. The
// mailbox epoch changes too, so clients start their positions over.
func TestRestoredMailboxIDsPassLostNode(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	n := openNode(t, dir, nil)
	alice := blobToken(0xa1)
	for i := range 3 {
		n.drop(t, alice, []byte(fmt.Sprintf("before the backup %d", i)))
	}
	var first wire.MailboxItems
	n.post(t, "/v1/mailbox/fetch", &wire.MailboxFetch{Token: alice}, &first)
	res, err := meta.Backup(ctx, dir, filepath.Join(t.TempDir(), "b"), nil)
	if err != nil {
		t.Fatal(err)
	}
	// The node keeps working until it is lost; the client reads along.
	for i := range 5 {
		n.drop(t, alice, []byte(fmt.Sprintf("after the backup, lost with the node %d", i)))
	}
	items := n.fetchMail(t, alice)
	cursor := items[len(items)-1].Id

	newDir := filepath.Join(t.TempDir(), "new-node")
	if _, err := meta.Restore(ctx, res.Path, newDir); err != nil {
		t.Fatal(err)
	}
	r := openNode(t, newDir, nil)
	r.drop(t, alice, []byte("first on the restored node"))
	var got wire.MailboxItems
	if code := r.post(t, "/v1/mailbox/fetch", &wire.MailboxFetch{Token: alice, AfterId: cursor}, &got); code != 200 {
		t.Fatal(code)
	}
	if len(got.Items) != 1 || string(got.Items[0].Blob) != "first on the restored node" {
		t.Fatalf("a client positioned at %d gets %q after the restore", cursor, mailTexts(got.Items))
	}
	if got.Items[0].Id <= cursor+meta.MailboxIDGap-1000 {
		t.Fatalf("new id %d isn't past the gap after %d", got.Items[0].Id, cursor)
	}
	if got.Epoch == 0 || first.Epoch == 0 || got.Epoch == first.Epoch {
		t.Fatalf("mailbox epoch %d before, %d after the restore", first.Epoch, got.Epoch)
	}
	// The epoch stays put across restarts of one node.
	var again wire.MailboxItems
	n.post(t, "/v1/mailbox/fetch", &wire.MailboxFetch{Token: alice}, &again)
	if again.Epoch != first.Epoch {
		t.Fatal("epoch changed without a restore")
	}
}

// A restore that fails after every file checked out (here: the restored
// database doesn't open, because a migration step fails on it) leaves the
// data directory as it was, so the same command can be run again with a
// good backup.
func TestFailedRestoreLeavesNothingBehind(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	n := openNode(t, dir, nil)
	n.upload(t, randBytes(3000), blobToken(1))
	n.drop(t, blobToken(9), []byte("an item"))
	res, err := meta.Backup(ctx, dir, filepath.Join(t.TempDir(), "b"), nil)
	if err != nil {
		t.Fatal(err)
	}
	// A database one schema version back whose next step can't apply.
	work := t.TempDir()
	var dbBytes []byte
	rewriteTar(t, res.Path, func(h *tar.Header, data []byte) []byte {
		if h.Name == "meta.db" {
			dbBytes = data
		}
		return data
	}, nil)
	broken := filepath.Join(work, "meta.db")
	if err := os.WriteFile(broken, dbBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(broken)
	if err != nil {
		t.Fatal(err)
	}
	older := meta.SchemaVersion() - 1
	if _, err := db.Exec(`DROP TABLE relays`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = ` + itoa(older)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	dbBytes, _ = os.ReadFile(broken)
	sum := sha256.Sum256(dbBytes)
	bad := rewriteTar(t, res.Path, func(h *tar.Header, data []byte) []byte {
		switch h.Name {
		case "meta.db":
			return dbBytes
		case "manifest.json":
			var m meta.BackupManifest
			_ = json.Unmarshal(data, &m)
			m.Schema = older
			m.Database.Size = int64(len(dbBytes))
			m.Database.SHA256 = hex.EncodeToString(sum[:])
			data, _ = json.Marshal(m)
		}
		return data
	}, nil)

	target := filepath.Join(t.TempDir(), "node")
	if _, err := meta.Restore(ctx, bad, target); err == nil || !strings.Contains(err.Error(), "doesn't open") {
		t.Fatalf("restore of a database that can't migrate: %v", err)
	}
	entries, _ := os.ReadDir(target)
	for _, e := range entries {
		if e.Name() == "blobs" {
			if bs, _ := os.ReadDir(filepath.Join(target, e.Name())); len(bs) == 0 {
				continue
			}
		}
		t.Errorf("a failed restore left %s behind", e.Name())
	}
	if _, err := meta.Restore(ctx, res.Path, target); err != nil {
		t.Fatalf("restore again after a failed one: %v", err)
	}
	r := openNode(t, target, nil)
	if got := mailTexts(r.fetchMail(t, blobToken(9))); len(got) != 1 {
		t.Fatalf("restored mailbox: %q", got)
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
