package meta_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/meta"
)

// A blob uploaded to one member is served by the other, which fetches the
// chunks it is asked for from the first and then keeps them; an ack on the
// second ends the blob on both.
func TestClusterBlobOnDemandAndRelease(t *testing.T) {
	a, b := pair(t, nil)
	data := randBytes(2*meta.BlobChunkSize + 1234)
	alice, bob := blobToken(0xa1), blobToken(0xb0)
	hash := a.upload(t, data, alice, bob)
	syncAll(t, a, b)
	got, code := b.download(t, hash, bob)
	if code != 200 || !bytes.Equal(got, data) {
		t.Fatalf("download from the member without the content: %d", code)
	}
	// b now holds the content itself: a can go.
	a.kill()
	if got, code := b.download(t, hash, alice); code != 200 || !bytes.Equal(got, data) {
		t.Fatalf("b didn't keep the chunks it fetched: %d", code)
	}
	a.down.Store(false)

	// Bob's device acks on b, Alice's on a: the blob is gone on both.
	if b.ack(t, hash, bob) != 200 {
		t.Fatal("ack")
	}
	syncAll(t, a, b)
	if _, code := a.download(t, hash, bob); code != 404 {
		t.Fatalf("a released token still fetches on a: %d", code)
	}
	if a.ack(t, hash, alice) != 200 {
		t.Fatal("ack")
	}
	syncAll(t, a, b)
	for _, n := range []*clusterNode{a, b} {
		if _, code := n.download(t, hash, alice); code != 404 {
			t.Fatalf("%s still serves a blob every device released: %d", n.url, code)
		}
		if files := blobFiles(t, n.dir); len(files) != 0 {
			t.Fatalf("%s kept files %v", n.url, files)
		}
	}
}

// The background copy fills in the content on the other member, so the
// blob survives the member it was uploaded to.
func TestClusterBlobBackgroundCopySurvivesTheRoot(t *testing.T) {
	root, m2 := pair(t, nil)
	data := randBytes(meta.BlobChunkSize + 99)
	tok := blobToken(0xb1)
	hash := root.upload(t, data, tok)
	syncAll(t, root, m2)
	m2.srv.CopyRemoteBlobs(context.Background())
	root.kill()
	if got, code := m2.download(t, hash, tok); code != 200 || !bytes.Equal(got, data) {
		t.Fatalf("after the root died: %d", code)
	}
}

// A member whose blob store is full keeps the record only and serves the
// content from its peers.
func TestClusterBlobFullStoreKeepsRecordOnly(t *testing.T) {
	small := func(c *meta.Config) { c.BlobMaxSize, c.BlobStoreMax, c.BlobQuotaPerIP = 1<<20, 1<<20, 1<<30 }
	a, b := pair(t, small)
	b.upload(t, randBytes(1<<20-100), blobToken(0x01)) // b is full
	data := randBytes(5000)
	tok := blobToken(0xb2)
	hash := a.upload(t, data, tok)
	syncAll(t, a, b)
	b.srv.CopyRemoteBlobs(context.Background())
	if got, code := b.download(t, hash, tok); code != 200 || !bytes.Equal(got, data) {
		t.Fatalf("full member doesn't serve from its peer: %d", code)
	}
	if files := blobFiles(t, b.dir); len(files) != 1 {
		t.Fatalf("full member stored content: %v", files)
	}
	// With its peer gone, it has nothing to serve.
	a.kill()
	if _, code := b.download(t, hash, tok); code != 404 {
		t.Fatalf("serving without content or peer: %d", code)
	}
}
