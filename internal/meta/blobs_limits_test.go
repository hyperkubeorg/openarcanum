package meta_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/meta"
	"github.com/hyperkubeorg/openarcanum/internal/store"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

func hexOf(b []byte) string { return hex.EncodeToString(b) }

// rawPost returns the status and the exact reply body.
func (n *node) rawPost(t *testing.T, path string, m proto.Message) (int, []byte) {
	t.Helper()
	b, _ := proto.Marshal(m)
	resp, err := apiClient.Post(n.url+path, "application/x-protobuf", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

// Without a valid token, create and chunk must answer the same for
// a blob that exists and one that doesn't, or the sender (or a recipient)
// can tell when every device has fetched it.
func TestBlobExistenceCantBeProbed(t *testing.T) {
	n := openNode(t, t.TempDir(), nil)
	data := randBytes(meta.BlobChunkSize + 100)
	stored := n.upload(t, data, blobToken(1), blobToken(2))
	gone := n.upload(t, randBytes(len(data)), blobToken(1))
	n.ack(t, gone, blobToken(1))
	never := hashOf([]byte("never uploaded"))
	probe := blobToken(9)

	// create with tokens of the prober's own: a fresh upload every time.
	shape := func(h []byte) *wire.BlobUploadStatus {
		st, code := n.createBlob(t, h, len(data), probe)
		if code != 200 {
			t.Fatalf("probe create: %d", code)
		}
		return &wire.BlobUploadStatus{Size: st.Size, ChunkSize: st.ChunkSize, Missing: st.Missing, Complete: st.Complete}
	}
	want := shape(never)
	for name, h := range map[string][]byte{"stored": stored, "fetched by everyone": gone} {
		if got := shape(h); !proto.Equal(got, want) {
			t.Fatalf("create for a %s blob answers %v, for a missing one %v", name, got, want)
		}
	}
	// A recipient holding one token learns nothing either: naming the blob
	// with only its own token starts a separate upload, whether or not the
	// other recipients have fetched (and the blob is still there).
	if st, code := n.createBlob(t, stored, len(data), blobToken(2)); code != 200 || st.Complete || len(st.Missing) != 2 {
		t.Fatalf("recipient probe: %d %v", code, st)
	}

	// chunk without the upload id: the same status and body.
	chunk := func(h []byte) (int, []byte) {
		return n.rawPost(t, "/v1/blobs/chunk", &wire.BlobChunk{Hash: h, Upload: hashOf(h), Index: 0, Data: chunkOf(data, 0)})
	}
	wantCode, wantBody := chunk(never)
	for name, h := range map[string][]byte{"stored": stored, "fetched by everyone": gone} {
		if code, body := chunk(h); code != wantCode || !bytes.Equal(body, wantBody) {
			t.Fatalf("chunk for a %s blob: %d %q; for a missing one: %d %q", name, code, body, wantCode, wantBody)
		}
	}
	// fetch with a wrong token: the same too.
	fetch := func(h []byte) (int, []byte) {
		return n.rawPost(t, "/v1/blobs/fetch", &wire.BlobFetch{Hash: h, Token: probe})
	}
	wantCode, wantBody = fetch(never)
	if code, body := fetch(stored); code != wantCode || !bytes.Equal(body, wantBody) {
		t.Fatalf("fetch with a wrong token: %d %q vs %d %q", code, body, wantCode, wantBody)
	}
	// The real recipients still get the stored blob.
	if got, code := n.download(t, stored, blobToken(2)); code != 200 || !bytes.Equal(got, data) {
		t.Fatalf("recipient download: %d", code)
	}
}

// Uploads and token rows are limited per network per rolling 24 h, so tiny
// uploads with many tokens each can't grow meta.db without bound.
func TestBlobPerNetworkUploadAndTokenLimits(t *testing.T) {
	n := openNode(t, t.TempDir(), func(c *meta.Config) {
		c.BlobUploadsPerIP = 3
		c.BlobTokensPerIP = meta.MaxBlobTokens + 50
	})
	now := time.UnixMilli(1_800_000_000_000)
	n.srv.SetClock(func() time.Time { return now })
	many := make([][]byte, meta.MaxBlobTokens)
	for i := range many {
		many[i] = randBytes(32)
	}
	if _, code := n.createBlob(t, hashOf([]byte{1}), 1, many...); code != 200 {
		t.Fatalf("upload with the most tokens: %d", code)
	}
	if _, code := n.createBlob(t, hashOf([]byte{2}), 1, many[:51]...); code != http.StatusTooManyRequests {
		t.Fatalf("over the token limit: %d", code)
	}
	if _, code := n.createBlob(t, hashOf([]byte{2}), 1, many[:50]...); code != 200 {
		t.Fatalf("token limit exactly: %d", code)
	}
	if _, code := n.createBlob(t, hashOf([]byte{3}), 1, blobToken(1)); code != http.StatusTooManyRequests {
		t.Fatalf("over the token limit with one more: %d", code)
	}
	n.srv.SetClock(func() time.Time { return now.Add(meta.BlobQuotaWindow) })
	for i := range 3 {
		if _, code := n.createBlob(t, hashOf([]byte{byte(10 + i)}), 1, blobToken(1)); code != 200 {
			t.Fatalf("upload %d after the window: %d", i, code)
		}
	}
	if _, code := n.createBlob(t, hashOf([]byte{20}), 1, blobToken(1)); code != http.StatusTooManyRequests {
		t.Fatalf("over the upload count: %d", code)
	}
	// Resuming doesn't count again.
	if _, code := n.createBlob(t, hashOf([]byte{10}), 1, blobToken(1)); code != 200 {
		t.Fatalf("resume over the upload count: %d", code)
	}
}

// Re-sending a chunk the node already has doesn't keep an abandoned upload
// (and the size it reserves) alive, and no upload stays open longer than
// BlobMaxUploadTime however steadily it trickles in.
func TestBlobUploadCantBeHeldOpen(t *testing.T) {
	n := openNode(t, t.TempDir(), nil)
	t0 := time.UnixMilli(1_800_000_000_000)
	now := t0
	n.srv.SetClock(func() time.Time { return now })
	data := randBytes(2*meta.BlobChunkSize + 1)
	hash := hashOf(data)
	n.createBlob(t, hash, len(data), blobToken(1))
	n.sendChunk(t, hash, data, 0)
	for _, at := range []time.Duration{20 * time.Minute, 40 * time.Minute, 55 * time.Minute} {
		now = t0.Add(at)
		if _, code := n.sendChunk(t, hash, data, 0); code != 200 {
			t.Fatalf("resend: %d", code)
		}
	}
	now = t0.Add(meta.BlobStaleUpload)
	n.srv.Expire(context.Background())
	if _, code := n.sendChunk(t, hash, data, 1); code != http.StatusNotFound {
		t.Fatalf("upload kept alive by resending a chunk: %d", code)
	}

	// A new chunk every 50 minutes still ends at BlobMaxUploadTime.
	big := randBytes(10 * meta.BlobChunkSize)
	hb := hashOf(big)
	t1 := now
	n.createBlob(t, hb, len(big), blobToken(1))
	for i := 0; i < 8; i++ {
		now = t1.Add(time.Duration(i) * 50 * time.Minute)
		if _, code := n.sendChunk(t, hb, big, i); code != 200 {
			t.Fatalf("chunk %d: %d", i, code)
		}
	}
	now = t1.Add(meta.BlobMaxUploadTime)
	if _, code := n.sendChunk(t, hb, big, 8); code != http.StatusNotFound {
		t.Fatalf("chunk past the upload deadline: %d", code)
	}
	n.srv.Expire(context.Background())
	if st, code := n.createBlob(t, hb, len(big), blobToken(1)); code != 200 || len(st.Missing) != 10 {
		t.Fatalf("overdue upload not discarded: %d %v", code, st)
	}
}

// An upload whose last chunk was recorded but not finished (the client hung
// up, or the node stopped, between the steps) completes when the client
// resends a chunk or repeats create, whether or not the file was renamed.
func TestBlobCompletionIsResumable(t *testing.T) {
	dir := t.TempDir()
	n := openNode(t, dir, nil)
	db, err := store.Open(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tk := blobToken(1)
	// interrupted uploads data up to the point where every chunk is on disk
	// and recorded, and (if renamed) the file moved into place, but the
	// upload isn't marked complete.
	interrupted := func(renamed bool) []byte {
		data := randBytes(meta.BlobChunkSize + 500)
		hash := hashOf(data)
		n.createBlob(t, hash, len(data), tk)
		n.sendChunk(t, hash, data, 0)
		id := n.uploads[string(hash)]
		part := filepath.Join(dir, "blobs", hexOf(id)+".part")
		f, err := os.OpenFile(part, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteAt(chunkOf(data, 1), meta.BlobChunkSize); err != nil {
			t.Fatal(err)
		}
		f.Close()
		if _, err := db.Exec(`UPDATE blobs SET received = ? WHERE id = ?`, []byte{0x03}, id); err != nil {
			t.Fatal(err)
		}
		if renamed {
			if err := os.Rename(part, filepath.Join(dir, "blobs", hexOf(id))); err != nil {
				t.Fatal(err)
			}
		}
		return data
	}

	// Renamed: resending the last chunk finishes it.
	data := interrupted(true)
	if st, code := n.sendChunk(t, hashOf(data), data, 1); code != 200 || !st.Complete {
		t.Fatalf("resent last chunk after the rename: %d %v", code, st)
	}
	if got, code := n.download(t, hashOf(data), tk); code != 200 || !bytes.Equal(got, data) {
		t.Fatalf("download: %d", code)
	}
	// Not renamed: resuming with create finishes it.
	data = interrupted(false)
	if st, code := n.createBlob(t, hashOf(data), len(data), tk); code != 200 || !st.Complete {
		t.Fatalf("resume of a fully received upload: %d %v", code, st)
	}
	if got, code := n.download(t, hashOf(data), tk); code != 200 || !bytes.Equal(got, data) {
		t.Fatalf("download: %d", code)
	}
	// Renamed, resumed with create.
	data = interrupted(true)
	if st, code := n.createBlob(t, hashOf(data), len(data), tk); code != 200 || !st.Complete {
		t.Fatalf("resume after the rename: %d %v", code, st)
	}
}
