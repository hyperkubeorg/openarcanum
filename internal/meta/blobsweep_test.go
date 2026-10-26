package meta_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/meta"
)

// `meta blobs delete` and `purge-expired` run in another process, without
// the node's lock: a node finishing an upload at that moment can be left
// with a blob file whose row is gone, which the store's size (a sum over
// rows) doesn't count. The running node removes such files with its next
// expiry pass instead of only at its next start; files of live blobs stay.
func TestNodeSweepsStrayBlobFiles(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	n := openNode(t, dir, nil)
	data := randBytes(4000)
	hash := n.upload(t, data, blobToken(1))
	id := make([]byte, 32)
	_, _ = rand.Read(id)
	stray := filepath.Join(dir, "blobs", hex.EncodeToString(id))
	strayPart := stray + ".part"
	for _, p := range []string{stray, strayPart} {
		if err := os.WriteFile(p, bytes.Repeat([]byte{1}, 1<<20), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	n.srv.Expire(ctx)
	for _, p := range []string{stray, strayPart} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("stray blob file %s survived the expiry pass: %v", filepath.Base(p), err)
		}
	}
	if got, code := n.download(t, hash, blobToken(1)); code != 200 || !bytes.Equal(got, data) {
		t.Fatalf("live blob after the sweep: %d", code)
	}
	// The admin process itself never sweeps: a fresh upload's file exists
	// before its row, and only the node's lock covers that moment.
	adm, err := meta.OpenBlobAdmin(ctx, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer adm.Close()
	if err := os.WriteFile(stray, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := adm.PurgeExpired(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stray); err != nil {
		t.Fatal("the admin command swept files")
	}
}
