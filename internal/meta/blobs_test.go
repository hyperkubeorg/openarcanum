package meta_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/config"
	"github.com/hyperkubeorg/openarcanum/internal/meta"
	"github.com/hyperkubeorg/openarcanum/internal/store"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

func openNode(t *testing.T, dir string, mutate func(*meta.Config)) *node {
	t.Helper()
	cfg := meta.DefaultConfig()
	cfg.DirectoryEnabled = true // the tests exercise the directory; it's off by default
	cfg.DataDir = dir
	if mutate != nil {
		mutate(&cfg)
	}
	srv, err := meta.Open(context.Background(), cfg, testutil.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { hs.Close(); srv.Close() })
	return &node{srv: srv, url: hs.URL}
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func blobToken(i byte) []byte { return bytes.Repeat([]byte{i}, 32) }

func tokenHashes(tokens ...[]byte) [][]byte {
	var out [][]byte
	for _, tk := range tokens {
		out = append(out, meta.BlobTokenHash(tk))
	}
	return out
}

func hashOf(data []byte) []byte {
	h := sha256.Sum256(data)
	return h[:]
}

func chunkOf(data []byte, i int) []byte {
	end := min((i+1)*meta.BlobChunkSize, len(data))
	return data[i*meta.BlobChunkSize : end]
}

func (n *node) createBlob(t *testing.T, hash []byte, size int, tokens ...[]byte) (*wire.BlobUploadStatus, int) {
	t.Helper()
	var st wire.BlobUploadStatus
	code := n.post(t, "/v1/blobs/create", &wire.BlobCreate{Hash: hash, Size: uint64(size), TokenHashes: tokenHashes(tokens...)}, &st)
	if code == 200 {
		if !bytes.Equal(st.Upload, uploadID(hash, size, tokens...)) {
			t.Fatalf("create returned upload id %x", st.Upload)
		}
		if n.uploads == nil {
			n.uploads = map[string][]byte{}
		}
		n.uploads[string(hash)] = st.Upload
	}
	return &st, code
}

// uploadID is the id of the upload of hash with these tokens.
func uploadID(hash []byte, size int, tokens ...[]byte) []byte {
	return meta.BlobUploadID(hash, uint64(size), tokenHashes(tokens...))
}

// sendChunk sends chunk i of the last upload created for hash.
func (n *node) sendChunk(t *testing.T, hash, data []byte, i int) (*wire.BlobUploadStatus, int) {
	t.Helper()
	var st wire.BlobUploadStatus
	code := n.post(t, "/v1/blobs/chunk", &wire.BlobChunk{Hash: hash, Upload: n.uploads[string(hash)], Index: uint32(i), Data: chunkOf(data, i)}, &st)
	return &st, code
}

// upload stores data in full and fails the test otherwise.
func (n *node) upload(t *testing.T, data []byte, tokens ...[]byte) []byte {
	t.Helper()
	hash := hashOf(data)
	st, code := n.createBlob(t, hash, len(data), tokens...)
	if code != 200 {
		t.Fatalf("create: %d", code)
	}
	for _, i := range st.Missing {
		if st, code = n.sendChunk(t, hash, data, int(i)); code != 200 {
			t.Fatalf("chunk %d: %d", i, code)
		}
	}
	if !st.Complete {
		t.Fatal("upload not complete after every chunk")
	}
	return hash
}

// download fetches every chunk; it returns the first non-200 status.
func (n *node) download(t *testing.T, hash, token []byte) ([]byte, int) {
	t.Helper()
	var out []byte
	for i := 0; ; i++ {
		var d wire.BlobChunkData
		if code := n.post(t, "/v1/blobs/fetch", &wire.BlobFetch{Hash: hash, Token: token, Index: uint32(i)}, &d); code != 200 {
			return nil, code
		}
		out = append(out, d.Data...)
		if uint64(len(out)) >= d.Size {
			return out, 200
		}
	}
}

func (n *node) ack(t *testing.T, hash, token []byte) int {
	t.Helper()
	return n.post(t, "/v1/blobs/ack", &wire.BlobAck{Hash: hash, Token: token}, nil)
}

func blobFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestBlobChunkedUploadFetchAndAck(t *testing.T) {
	dir := t.TempDir()
	n := openNode(t, dir, nil)
	data := randBytes(2*meta.BlobChunkSize + 12345)
	hash := hashOf(data)
	t1, t2, t3 := blobToken(1), blobToken(2), blobToken(3)

	st, code := n.createBlob(t, hash, len(data), t1, t2, t3)
	if code != 200 || st.Complete || len(st.Missing) != 3 || st.ChunkSize != meta.BlobChunkSize {
		t.Fatalf("create: %d %v", code, st)
	}
	// Out of order, with a repeated chunk.
	for _, i := range []int{2, 0, 0} {
		if st, code = n.sendChunk(t, hash, data, i); code != 200 || st.Complete {
			t.Fatalf("chunk %d: %d %v", i, code, st)
		}
	}
	if len(st.Missing) != 1 || st.Missing[0] != 1 {
		t.Fatalf("missing after chunks 2,0: %v", st.Missing)
	}
	// Fetching before the upload finishes is refused.
	if _, code := n.download(t, hash, t1); code != http.StatusConflict {
		t.Fatalf("fetch of unfinished upload: %d", code)
	}
	// Resuming (same create again) reports what is missing.
	if st, code = n.createBlob(t, hash, len(data), t1, t2, t3); code != 200 || len(st.Missing) != 1 {
		t.Fatalf("resume: %d %v", code, st)
	}
	if st, code = n.sendChunk(t, hash, data, 1); code != 200 || !st.Complete {
		t.Fatalf("last chunk: %d %v", code, st)
	}
	// A chunk after completion is accepted and ignored.
	if st, code = n.sendChunk(t, hash, data, 0); code != 200 || !st.Complete {
		t.Fatalf("chunk after completion: %d", code)
	}
	if files := blobFiles(t, dir); len(files) != 1 || files[0] != hex.EncodeToString(uploadID(hash, len(data), t1, t2, t3)) {
		t.Fatalf("blob files: %v", files)
	}

	for _, tk := range [][]byte{t1, t2, t3} {
		got, code := n.download(t, hash, tk)
		if code != 200 || !bytes.Equal(got, data) {
			t.Fatalf("download: %d, equal=%v", code, bytes.Equal(got, data))
		}
	}
	if _, code := n.download(t, hash, blobToken(9)); code != http.StatusNotFound {
		t.Fatalf("unregistered token: %d", code)
	}
	if _, code := n.download(t, hashOf([]byte("other")), t1); code != http.StatusNotFound {
		t.Fatalf("unknown blob: %d", code)
	}
	var d wire.BlobChunkData
	if code := n.post(t, "/v1/blobs/fetch", &wire.BlobFetch{Hash: hash, Token: t1, Index: 3}, &d); code != http.StatusBadRequest {
		t.Fatalf("index past end: %d", code)
	}

	// Releasing tokens: the blob stays until the last one.
	for _, tk := range [][]byte{t1, t1, blobToken(9)} {
		if n.ack(t, hash, tk) != 200 {
			t.Fatal("ack refused")
		}
	}
	if _, code := n.download(t, hash, t1); code != http.StatusNotFound {
		t.Fatalf("released token still fetches: %d", code)
	}
	if n.ack(t, hash, t2) != 200 {
		t.Fatal("ack 2")
	}
	if _, code := n.download(t, hash, t3); code != 200 {
		t.Fatalf("remaining token: %d", code)
	}
	if n.ack(t, hash, t3) != 200 {
		t.Fatal("ack 3")
	}
	if _, code := n.download(t, hash, t3); code != http.StatusNotFound {
		t.Fatalf("after every ack: %d", code)
	}
	if files := blobFiles(t, dir); len(files) != 0 {
		t.Fatalf("files left after every ack: %v", files)
	}
}

func TestBlobUploadRefusals(t *testing.T) {
	dir := t.TempDir()
	n := openNode(t, dir, func(c *meta.Config) { c.BlobMaxSize = 2 << 20 })
	tk := blobToken(1)

	// Oversize.
	if _, code := n.createBlob(t, hashOf(nil), 2<<20+1, tk); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize: %d", code)
	}
	// Token counts and shapes.
	many := make([][]byte, meta.MaxBlobTokens+1)
	for i := range many {
		many[i] = randBytes(32)
	}
	for name, ths := range map[string][][]byte{
		"none":      nil,
		"too many":  many,
		"duplicate": {meta.BlobTokenHash(tk), meta.BlobTokenHash(tk)},
		"short":     {[]byte("short")},
	} {
		if code := n.post(t, "/v1/blobs/create", &wire.BlobCreate{Hash: hashOf(nil), Size: 10, TokenHashes: ths}, nil); code != http.StatusBadRequest {
			t.Fatalf("%s tokens: %d", name, code)
		}
	}
	if code := n.post(t, "/v1/blobs/create", &wire.BlobCreate{Hash: []byte("x"), Size: 10, TokenHashes: tokenHashes(tk)}, nil); code != http.StatusBadRequest {
		t.Fatalf("bad hash: %d", code)
	}
	if code := n.postRaw(t, "/v1/blobs/create", []byte{0xff, 0xff}, nil); code != http.StatusBadRequest {
		t.Fatalf("garbage: %d", code)
	}
	if code := n.postRaw(t, "/v1/blobs/fetch", make([]byte, 4096), nil); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize fetch body: %d", code)
	}

	// Chunk shape checks.
	data := randBytes(meta.BlobChunkSize + 10)
	hash := hashOf(data)
	if _, code := n.createBlob(t, hash, len(data), tk); code != 200 {
		t.Fatal("create")
	}
	upload := n.uploads[string(hash)]
	if code := n.post(t, "/v1/blobs/chunk", &wire.BlobChunk{Hash: hash, Upload: upload, Index: 1, Data: []byte("short")}, nil); code != http.StatusBadRequest {
		t.Fatalf("wrong chunk length: %d", code)
	}
	if code := n.post(t, "/v1/blobs/chunk", &wire.BlobChunk{Hash: hash, Upload: upload, Index: 2, Data: []byte("x")}, nil); code != http.StatusBadRequest {
		t.Fatalf("chunk past end: %d", code)
	}
	if code := n.post(t, "/v1/blobs/chunk", &wire.BlobChunk{Hash: hash, Index: 0, Data: chunkOf(data, 0)}, nil); code != http.StatusBadRequest {
		t.Fatalf("chunk without an upload id: %d", code)
	}
	if code := n.post(t, "/v1/blobs/chunk", &wire.BlobChunk{Hash: hashOf([]byte("nope")), Upload: upload, Index: 0, Data: []byte("x")}, nil); code != http.StatusNotFound {
		t.Fatalf("chunk naming another blob: %d", code)
	}
	if code := n.post(t, "/v1/blobs/chunk", &wire.BlobChunk{Hash: hash, Upload: randBytes(32), Index: 0, Data: chunkOf(data, 0)}, nil); code != http.StatusNotFound {
		t.Fatalf("chunk for unknown upload: %d", code)
	}
	// The same hash with other tokens or another size is a separate upload.
	if st, code := n.createBlob(t, hash, len(data), blobToken(2)); code != 200 || bytes.Equal(st.Upload, upload) || len(st.Missing) != 2 {
		t.Fatalf("other tokens: %d %v", code, st)
	}
	if st, code := n.createBlob(t, hash, len(data)+1, tk); code != 200 || bytes.Equal(st.Upload, upload) {
		t.Fatalf("other size: %d %v", code, st)
	}

	// Wrong hash: refused once the last chunk arrives, and discarded.
	wrong := randBytes(100)
	claimed := hashOf([]byte("something else"))
	if _, code := n.createBlob(t, claimed, len(wrong), tk); code != 200 {
		t.Fatal("create for wrong hash")
	}
	if _, code := n.sendChunk(t, claimed, wrong, 0); code != http.StatusBadRequest {
		t.Fatalf("hash mismatch: %d", code)
	}
	for _, f := range blobFiles(t, dir) {
		if strings.HasPrefix(f, hex.EncodeToString(uploadID(claimed, len(wrong), tk))) {
			t.Fatalf("mismatched upload left %s", f)
		}
	}
	if _, code := n.sendChunk(t, claimed, wrong, 0); code != http.StatusNotFound {
		t.Fatalf("mismatched upload still open: %d", code)
	}
}

func TestBlobExpiresAtTTL(t *testing.T) {
	dir := t.TempDir()
	n := openNode(t, dir, func(c *meta.Config) { c.BlobTTL = config.Duration{Duration: time.Hour} })
	t0 := time.UnixMilli(1_800_000_000_000)
	now := t0
	n.srv.SetClock(func() time.Time { return now })
	data := randBytes(5000)
	tk := blobToken(1)
	hash := n.upload(t, data, tk)

	now = t0.Add(time.Hour - time.Millisecond)
	n.srv.Expire(context.Background())
	if got, code := n.download(t, hash, tk); code != 200 || !bytes.Equal(got, data) {
		t.Fatalf("just before the TTL: %d", code)
	}
	now = t0.Add(time.Hour)
	if _, code := n.download(t, hash, tk); code != http.StatusNotFound {
		t.Fatalf("at the TTL, before collection: %d", code)
	}
	if len(blobFiles(t, dir)) != 1 {
		t.Fatal("file removed before collection")
	}
	n.srv.Expire(context.Background())
	if files := blobFiles(t, dir); len(files) != 0 {
		t.Fatalf("expired blob left files: %v", files)
	}
	// The same ciphertext can be uploaded again after expiry.
	n.upload(t, data, tk)
}

func TestBlobQuotaPerNetwork(t *testing.T) {
	n := openNode(t, t.TempDir(), func(c *meta.Config) {
		c.BlobMaxSize = 1 << 20
		c.BlobQuotaPerIP = 2 << 20
	})
	now := time.UnixMilli(1_800_000_000_000)
	n.srv.SetClock(func() time.Time { return now })
	for i := range 2 {
		if _, code := n.createBlob(t, hashOf([]byte{byte(i)}), 1<<20, blobToken(1)); code != 200 {
			t.Fatalf("upload %d within quota: %d", i, code)
		}
	}
	if _, code := n.createBlob(t, hashOf([]byte{9}), 1, blobToken(1)); code != http.StatusTooManyRequests {
		t.Fatalf("over quota: %d", code)
	}
	// Resuming an upload already charged is not charged again.
	if _, code := n.createBlob(t, hashOf([]byte{0}), 1<<20, blobToken(1)); code != 200 {
		t.Fatalf("resume over quota: %d", code)
	}
	now = now.Add(meta.BlobQuotaWindow)
	if _, code := n.createBlob(t, hashOf([]byte{9}), 1, blobToken(1)); code != 200 {
		t.Fatalf("after the window: %d", code)
	}
}

func TestBlobUploadsPerMinute(t *testing.T) {
	n := openNode(t, t.TempDir(), func(c *meta.Config) { c.BlobUploadsPerMinute = 2 })
	for i := range 2 {
		if _, code := n.createBlob(t, hashOf([]byte{byte(i)}), 10, blobToken(1)); code != 200 {
			t.Fatalf("upload %d: %d", i, code)
		}
	}
	if _, code := n.createBlob(t, hashOf([]byte{7}), 10, blobToken(1)); code != http.StatusTooManyRequests {
		t.Fatalf("over rate: %d", code)
	}
}

func TestBlobStoreCap(t *testing.T) {
	n := openNode(t, t.TempDir(), func(c *meta.Config) {
		c.BlobMaxSize = 1 << 20
		c.BlobStoreMax = 1<<20 + 1000
	})
	a := randBytes(1 << 20)
	ha := n.upload(t, a, blobToken(1))
	if _, code := n.createBlob(t, hashOf([]byte("b")), 1001, blobToken(1)); code != http.StatusInsufficientStorage {
		t.Fatalf("store full: %d", code)
	}
	if _, code := n.createBlob(t, hashOf([]byte("b")), 1000, blobToken(1)); code != 200 {
		t.Fatalf("fits exactly: %d", code)
	}
	n.ack(t, ha, blobToken(1))
	if _, code := n.createBlob(t, hashOf([]byte("c")), 1<<20, blobToken(1)); code != 200 {
		t.Fatalf("space freed by ack: %d", code)
	}
}

func TestBlobAbandonedUploadCollected(t *testing.T) {
	dir := t.TempDir()
	n := openNode(t, dir, nil)
	t0 := time.UnixMilli(1_800_000_000_000)
	now := t0
	n.srv.SetClock(func() time.Time { return now })
	stale := randBytes(meta.BlobChunkSize + 1)
	active := randBytes(meta.BlobChunkSize + 2)
	n.createBlob(t, hashOf(stale), len(stale), blobToken(1))
	n.createBlob(t, hashOf(active), len(active), blobToken(1))
	n.sendChunk(t, hashOf(stale), stale, 0)
	now = t0.Add(meta.BlobStaleUpload / 2)
	n.sendChunk(t, hashOf(active), active, 0)
	now = t0.Add(meta.BlobStaleUpload)
	n.srv.Expire(context.Background())
	files := blobFiles(t, dir)
	if len(files) != 1 || files[0] != hex.EncodeToString(uploadID(hashOf(active), len(active), blobToken(1)))+".part" {
		t.Fatalf("after collection: %v", files)
	}
	if _, code := n.sendChunk(t, hashOf(stale), stale, 1); code != http.StatusNotFound {
		t.Fatalf("abandoned upload still open: %d", code)
	}
	if st, code := n.sendChunk(t, hashOf(active), active, 1); code != 200 || !st.Complete {
		t.Fatalf("active upload: %d", code)
	}
}

func TestBlobRestartAndCrashRecovery(t *testing.T) {
	dir := t.TempDir()
	cfg := meta.DefaultConfig()
	cfg.DirectoryEnabled = true // the tests exercise the directory; it's off by default
	cfg.DataDir = dir
	srv, err := meta.Open(context.Background(), cfg, testutil.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	n := &node{srv: srv, url: hs.URL}
	tk := blobToken(1)
	done := randBytes(3000)
	renamed := randBytes(4000)
	lost := randBytes(5000)
	partial := randBytes(meta.BlobChunkSize + 3)
	hDone := n.upload(t, done, tk)
	hRenamed := n.upload(t, renamed, tk)
	hLost := n.upload(t, lost, tk)
	n.createBlob(t, hashOf(partial), len(partial), tk)
	n.sendChunk(t, hashOf(partial), partial, 1)
	hs.Close()
	srv.Close()

	// Simulate crashes: a rename that never reached the database, a file
	// lost from disk, and stray files without records.
	db, err := store.Open(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE blobs SET complete = 0 WHERE hash = ?`, hRenamed); err != nil {
		t.Fatal(err)
	}
	db.Close()
	bdir := filepath.Join(dir, "blobs")
	os.Remove(filepath.Join(bdir, hex.EncodeToString(uploadID(hLost, len(lost), tk))))
	stray := hex.EncodeToString(hashOf([]byte("stray")))
	for _, name := range []string{stray, stray + ".part", "junk.tmp"} {
		if err := os.WriteFile(filepath.Join(bdir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	n = openNode(t, dir, nil)
	for _, c := range []struct {
		hash, data []byte
	}{{hDone, done}, {hRenamed, renamed}} {
		if got, code := n.download(t, c.hash, tk); code != 200 || !bytes.Equal(got, c.data) {
			t.Fatalf("after restart: %d", code)
		}
	}
	if _, code := n.download(t, hLost, tk); code != http.StatusNotFound {
		t.Fatalf("blob whose file was lost: %d", code)
	}
	st, code := n.createBlob(t, hashOf(partial), len(partial), tk)
	if code != 200 || len(st.Missing) != 1 || st.Missing[0] != 0 {
		t.Fatalf("resume after restart: %d %v", code, st)
	}
	if st, _ = n.sendChunk(t, hashOf(partial), partial, 0); !st.Complete {
		t.Fatal("resumed upload did not complete")
	}
	files := strings.Join(blobFiles(t, dir), " ")
	if strings.Contains(files, stray) || strings.Contains(files, "junk") {
		t.Fatalf("stray files survived: %s", files)
	}
}

func TestBlobConfig(t *testing.T) {
	cfg := meta.DefaultConfig()
	cfg.DirectoryEnabled = true // the tests exercise the directory; it's off by default
	cfg.DataDir = "x"
	if cfg.BlobTTL.Duration != 72*time.Hour || cfg.BlobMaxSize != 26<<20 || cfg.BlobQuotaPerIP != 250<<20 ||
		cfg.BlobStoreMax != 10<<30 || cfg.BlobUploadsPerMinute != 30 || cfg.BlobUploadsPerIP != 500 || cfg.BlobTokensPerIP != 10_000 {
		t.Fatal("blob defaults differ from docs/settings.md")
	}
	for name, mutate := range map[string]func(*meta.Config){
		"ttl too short":  func(c *meta.Config) { c.BlobTTL.Duration = time.Second },
		"ttl too long":   func(c *meta.Config) { c.BlobTTL.Duration = 31 * 24 * time.Hour },
		"tiny max size":  func(c *meta.Config) { c.BlobMaxSize = 1000 },
		"quota < max":    func(c *meta.Config) { c.BlobQuotaPerIP = 1 << 20 },
		"store < max":    func(c *meta.Config) { c.BlobStoreMax = 1 << 20 },
		"zero rate":      func(c *meta.Config) { c.BlobUploadsPerMinute = 0 },
		"huge max size":  func(c *meta.Config) { c.BlobMaxSize = 2 << 30; c.BlobQuotaPerIP = 4 << 30; c.BlobStoreMax = 4 << 30 },
		"negative rate":  func(c *meta.Config) { c.BlobUploadsPerMinute = -1 },
		"zero store max": func(c *meta.Config) { c.BlobStoreMax = 0 },
		"zero uploads":   func(c *meta.Config) { c.BlobUploadsPerIP = 0 },
		"few tokens":     func(c *meta.Config) { c.BlobTokensPerIP = meta.MaxBlobTokens - 1 },
	} {
		bad := cfg
		mutate(&bad)
		if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "blob") {
			t.Fatalf("%s accepted (%v)", name, err)
		}
	}
	t.Setenv("OPENARCANUM_META_BLOB_TTL", "24h")
	t.Setenv("OPENARCANUM_META_BLOB_MAX_SIZE", "2097152")
	t.Setenv("OPENARCANUM_META_BLOB_QUOTA_PER_IP", "4194304")
	t.Setenv("OPENARCANUM_META_BLOB_STORE_MAX", "8388608")
	t.Setenv("OPENARCANUM_META_BLOB_UPLOADS_PER_MINUTE", "5")
	t.Setenv("OPENARCANUM_META_BLOB_UPLOADS_PER_IP", "7")
	t.Setenv("OPENARCANUM_META_BLOB_TOKENS_PER_IP", "300")
	got := meta.DefaultConfig()
	if err := config.Load("", "OPENARCANUM_META_", &got); err != nil {
		t.Fatal(err)
	}
	if got.BlobTTL.Duration != 24*time.Hour || got.BlobMaxSize != 2<<20 || got.BlobQuotaPerIP != 4<<20 ||
		got.BlobStoreMax != 8<<20 || got.BlobUploadsPerMinute != 5 || got.BlobUploadsPerIP != 7 || got.BlobTokensPerIP != 300 {
		t.Fatalf("env overrides not applied: %+v", got)
	}
	// The overridden limits take effect.
	got.DataDir = t.TempDir()
	n := openNode(t, got.DataDir, func(c *meta.Config) { *c = got })
	if _, code := n.createBlob(t, hashOf(nil), 2<<20+1, blobToken(1)); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("env max size not enforced: %d", code)
	}
}

func TestBlobAdmin(t *testing.T) {
	dir := t.TempDir()
	n := openNode(t, dir, nil)
	t0 := time.UnixMilli(1_800_000_000_000)
	n.srv.SetClock(func() time.Time { return t0 })
	ha := n.upload(t, randBytes(3000), blobToken(1), blobToken(2))
	hb := n.upload(t, randBytes(4000), blobToken(1))
	n.createBlob(t, hashOf([]byte("partial")), 10, blobToken(1))

	ctx := context.Background()
	adm, err := meta.OpenBlobAdmin(ctx, dir, testutil.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	defer adm.Close()
	st, err := adm.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Stored != 2 || st.StoredBytes != 7000 || st.Uploading != 1 || st.UploadingBytes != 10 || len(st.Blobs) != 3 || st.Blobs[0].Tokens == 0 {
		t.Fatalf("stats: %+v", st)
	}
	if ok, err := adm.Delete(ctx, hex.EncodeToString(ha)); !ok || err != nil {
		t.Fatalf("delete: %v %v", ok, err)
	}
	if ok, _ := adm.Delete(ctx, hex.EncodeToString(ha)); ok {
		t.Fatal("deleted twice")
	}
	if _, err := adm.Delete(ctx, "zz"); err == nil {
		t.Fatal("bad id accepted")
	}
	if _, code := n.download(t, ha, blobToken(1)); code != http.StatusNotFound {
		t.Fatalf("deleted blob still served: %d", code)
	}
	adm.SetClock(func() time.Time { return t0.Add(72 * time.Hour) })
	if purged, err := adm.PurgeExpired(ctx); err != nil || purged != 2 {
		t.Fatalf("purge: %d %v", purged, err)
	}
	if _, code := n.download(t, hb, blobToken(1)); code != http.StatusNotFound {
		t.Fatalf("purged blob still served: %d", code)
	}
	if files := blobFiles(t, dir); len(files) != 0 {
		t.Fatalf("files after purge: %v", files)
	}
	if _, err := meta.OpenBlobAdmin(ctx, t.TempDir(), nil); err == nil {
		t.Fatal("admin opened a directory without a database")
	}
}
