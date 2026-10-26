package meta_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/meta"
	"github.com/hyperkubeorg/openarcanum/internal/store"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

func mailTexts(items []*wire.MailboxItem) []string {
	var out []string
	for _, it := range items {
		out = append(out, string(it.Blob))
	}
	return out
}

// A maintainer node's backup, taken while it serves, restores onto new
// hardware with every mailbox item, blob, listing and route it had when
// the backup ran (docs/operations.md "Backups").
func TestBackupRestoreEndToEnd(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	n := openNode(t, dir, func(c *meta.Config) { c.WritesPerMinute = 100000; c.MailboxDropsPerMin = 100000 })
	alice, bob := blobToken(0xa1), blobToken(0xb2)
	n.drop(t, alice, []byte("sealed item one"))
	n.drop(t, alice, []byte("sealed item two"))
	n.drop(t, bob, []byte("sealed item for bob"))
	data := randBytes(meta.BlobChunkSize + 1000) // two chunks
	hash := n.upload(t, data, blobToken(1), blobToken(2))
	small := randBytes(5000)
	smallHash := n.upload(t, small, blobToken(3))
	// An upload in progress (one of two chunks) is left out of the backup.
	partial := randBytes(meta.BlobChunkSize + 10)
	if st, code := n.createBlob(t, hashOf(partial), len(partial), blobToken(4)); code != 200 || len(st.Missing) != 2 {
		t.Fatalf("partial create: %d", code)
	}
	if _, code := n.sendChunk(t, hashOf(partial), partial, 0); code != 200 {
		t.Fatal("partial chunk")
	}
	c := newCommunity(t)
	if code := n.post(t, "/v1/listings", c.listing("Gecko Garden", "plants", []string{"plants"}, 1), nil); code != 200 {
		t.Fatalf("listing: %d", code)
	}
	if code := n.post(t, "/v1/routes", c.signedRoute(1), nil); code != 200 {
		t.Fatalf("route: %d", code)
	}

	// Writes keep arriving while the backup runs.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var during atomic.Int64
	wg.Add(1)
	go func() {
		defer wg.Done()
		carol := blobToken(0xc3)
		for {
			select {
			case <-stop:
				return
			default:
			}
			n.drop(t, carol, []byte("written during the backup"))
			during.Add(1)
		}
	}()
	out := filepath.Join(t.TempDir(), "backups")
	res, err := meta.Backup(ctx, dir, out, nil)
	close(stop)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if res.Blobs != 2 || res.Skipped != 0 || !strings.HasPrefix(filepath.Base(res.Path), meta.BackupPrefix) {
		t.Fatalf("backup: %+v", res)
	}
	if st, _ := os.Stat(res.Path); st.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode %v", st.Mode())
	}
	if during.Load() == 0 {
		t.Fatal("no writes happened during the backup")
	}
	// After the backup: one item read and deleted, one new.
	items := n.fetchMail(t, alice)
	n.post(t, "/v1/mailbox/delete", &wire.MailboxDelete{Token: alice, Ids: []int64{items[0].Id}}, nil)
	n.drop(t, alice, []byte("after the backup"))

	newDir := filepath.Join(t.TempDir(), "new-node")
	rres, err := meta.Restore(ctx, res.Path, newDir)
	if err != nil {
		t.Fatal(err)
	}
	if rres.Blobs != 2 || rres.Schema != res.Schema {
		t.Fatalf("restore: %+v", rres)
	}
	r := openNode(t, newDir, nil)
	if got := mailTexts(r.fetchMail(t, alice)); strings.Join(got, "|") != "sealed item one|sealed item two" {
		t.Fatalf("alice's mailbox after restore: %q", got)
	}
	if got := mailTexts(r.fetchMail(t, bob)); len(got) != 1 || got[0] != "sealed item for bob" {
		t.Fatalf("bob's mailbox after restore: %q", got)
	}
	for _, tk := range [][]byte{blobToken(1), blobToken(2)} {
		got, code := r.download(t, hash, tk)
		if code != 200 || !bytes.Equal(got, data) {
			t.Fatalf("blob after restore: %d", code)
		}
	}
	if got, code := r.download(t, smallHash, blobToken(3)); code != 200 || !bytes.Equal(got, small) {
		t.Fatalf("small blob after restore: %d", code)
	}
	// The interrupted upload starts over.
	if st, code := r.createBlob(t, hashOf(partial), len(partial), blobToken(4)); code != 200 || len(st.Missing) != 2 {
		t.Fatalf("partial upload after restore: %d missing %v", code, st.Missing)
	}
	var found wire.SearchResponse
	if code := r.get(t, "/v1/search?q=gecko", &found); code != 200 || len(found.Listings) != 1 {
		t.Fatalf("search after restore: %d %d", code, len(found.Listings))
	}
	if code := r.get(t, "/v1/routes/"+identity.Encode(c.route), nil); code != 200 {
		t.Fatalf("route after restore: %d", code)
	}
	// Mailbox ids keep growing past every id the backup held.
	before := r.fetchMail(t, alice)
	r.drop(t, alice, []byte("first on the new node"))
	after := r.fetchMail(t, alice)
	if after[len(after)-1].Id <= before[len(before)-1].Id {
		t.Fatal("restored node reused a mailbox id")
	}

	// A second backup, then keep 1.
	time.Sleep(5 * time.Millisecond)
	res2, err := meta.Backup(ctx, dir, out, nil)
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := meta.PruneBackups(out, 1)
	if err != nil || len(deleted) != 1 || deleted[0] != res.Path {
		t.Fatalf("prune: %v %v", deleted, err)
	}
	if _, err := os.Stat(res2.Path); err != nil {
		t.Fatal("newest backup pruned")
	}
}

// A blob the node deletes while the backup runs (simulated: its file is
// gone, its row not yet) is left out, and a blob file damaged on disk is
// left out of the manifest; the restored node is consistent either way.
func TestBackupLeavesOutVanishedAndDamagedBlobs(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	n := openNode(t, dir, nil)
	keep := randBytes(3000)
	keepHash := n.upload(t, keep, blobToken(1))
	gone := randBytes(4000)
	n.upload(t, gone, blobToken(2))
	damaged := randBytes(5000)
	n.upload(t, damaged, blobToken(3))
	files := map[string]string{}
	for _, f := range blobFiles(t, dir) {
		b, _ := os.ReadFile(filepath.Join(dir, "blobs", f))
		files[string(hashOf(b))] = f
	}
	if err := os.Remove(filepath.Join(dir, "blobs", files[string(hashOf(gone))])); err != nil {
		t.Fatal(err)
	}
	bad := append([]byte(nil), damaged...)
	bad[10] ^= 1
	if err := os.WriteFile(filepath.Join(dir, "blobs", files[string(hashOf(damaged))]), bad, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := meta.Backup(ctx, dir, filepath.Join(dir, "backups"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Blobs != 1 || res.Skipped != 2 {
		t.Fatalf("backup: %+v", res)
	}
	newDir := t.TempDir()
	if _, err := meta.Restore(ctx, res.Path, newDir); err != nil {
		t.Fatal(err)
	}
	if got := blobFiles(t, newDir); len(got) != 1 {
		t.Fatalf("restored blob files: %v", got)
	}
	r := openNode(t, newDir, nil)
	if got, code := r.download(t, keepHash, blobToken(1)); code != 200 || !bytes.Equal(got, keep) {
		t.Fatalf("kept blob: %d", code)
	}
	if _, code := r.download(t, hashOf(damaged), blobToken(3)); code != 404 {
		t.Fatalf("damaged blob after restore: %d", code)
	}
}

// rewriteTar copies a backup, letting edit change or drop entries (return
// nil to drop) and add extra ones at the end.
func rewriteTar(t *testing.T, src string, edit func(h *tar.Header, data []byte) []byte, extra map[string][]byte) string {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	dst := filepath.Join(t.TempDir(), "edited.tar")
	out, _ := os.Create(dst)
	defer out.Close()
	tr, tw := tar.NewReader(in), tar.NewWriter(out)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(tr)
		if edit != nil {
			if data = edit(h, data); data == nil {
				continue
			}
		}
		h.Size = int64(len(data))
		_ = tw.WriteHeader(h)
		_, _ = tw.Write(data)
	}
	for name, data := range extra {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), Typeflag: tar.TypeReg})
		_, _ = tw.Write(data)
	}
	_ = tw.Close()
	return dst
}

func TestRestoreRefusesBadBackups(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	n := openNode(t, dir, nil)
	n.upload(t, randBytes(3000), blobToken(1))
	n.drop(t, blobToken(9), []byte("an item"))
	res, err := meta.Backup(ctx, dir, filepath.Join(dir, "backups"), nil)
	if err != nil {
		t.Fatal(err)
	}
	flipBlob := func(h *tar.Header, data []byte) []byte {
		if strings.HasPrefix(h.Name, "blobs/") {
			data[0] ^= 1
		}
		return data
	}
	newerSchema := func(h *tar.Header, data []byte) []byte {
		if h.Name == "manifest.json" {
			var m map[string]any
			_ = json.Unmarshal(data, &m)
			m["schema"] = 99
			data, _ = json.Marshal(m)
		}
		return data
	}
	dropDB := func(h *tar.Header, data []byte) []byte {
		if h.Name == "meta.db" {
			return nil
		}
		return data
	}
	dropManifest := func(h *tar.Header, data []byte) []byte {
		if h.Name == "manifest.json" {
			return nil
		}
		return data
	}
	truncated := filepath.Join(t.TempDir(), "short.tar")
	whole, _ := os.ReadFile(res.Path)
	_ = os.WriteFile(truncated, whole[:len(whole)/2], 0o600)
	notTar := filepath.Join(t.TempDir(), "x.tar")
	_ = os.WriteFile(notTar, []byte("this is not a backup"), 0o600)
	cases := map[string]string{
		rewriteTar(t, res.Path, flipBlob, nil):                                        "damaged",
		rewriteTar(t, res.Path, newerSchema, nil):                                     "newer than this build",
		rewriteTar(t, res.Path, dropDB, nil):                                          "missing meta.db",
		rewriteTar(t, res.Path, dropManifest, nil):                                    "manifest.json is missing",
		rewriteTar(t, res.Path, nil, map[string][]byte{"../escape": []byte("x")}):     "not part of a maintainer backup",
		rewriteTar(t, res.Path, nil, map[string][]byte{"blobs/../../x": []byte("x")}): "not part of a maintainer backup",
		rewriteTar(t, res.Path, nil, map[string][]byte{"meta.db": []byte("again")}):   "twice",
		truncated: "truncated",
		notTar:    "not a maintainer backup",
	}
	for path, want := range cases {
		target := filepath.Join(t.TempDir(), "node")
		_, err := meta.Restore(ctx, path, target)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", filepath.Base(path), err, want)
			continue
		}
		if _, err := os.Stat(filepath.Join(target, "meta.db")); err == nil {
			t.Errorf("%s: a refused restore left a database", want)
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(target), "escape")); err == nil {
			t.Error("an entry escaped the data directory")
		}
	}
	// Never over an existing node.
	if _, err := meta.Restore(ctx, res.Path, dir); err == nil || !strings.Contains(err.Error(), "already has a database") {
		t.Fatalf("restore over a node: %v", err)
	}
}

// The backup is taken with the node's own release: a database at another
// schema version is refused rather than migrated under a running node.
func TestBackupRefusesOtherSchema(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	n := openNode(t, dir, nil)
	n.drop(t, blobToken(9), []byte("an item"))
	db, err := store.Open(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	v, _ := store.Version(ctx, db)
	if _, err := db.Exec(`PRAGMA user_version = 1`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := meta.Backup(ctx, dir, t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "the node's own release") {
		t.Fatalf("err = %v (schema was %d)", err, v)
	}
	if _, err := meta.Backup(ctx, t.TempDir(), t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "no maintainer database") {
		t.Fatalf("empty dir: %v", err)
	}
}
