package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/meta"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/version"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

func post(t *testing.T, url string, m proto.Message) {
	t.Helper()
	b, _ := proto.Marshal(m)
	resp, err := apiClient.Post(url, "application/x-protobuf", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("%s: %d", url, resp.StatusCode)
	}
}

func uploadAt(t *testing.T, srv *meta.Server, url string, at time.Time, data []byte) string {
	t.Helper()
	srv.SetClock(func() time.Time { return at })
	h := sha256.Sum256(data)
	tokens := [][]byte{meta.BlobTokenHash(make([]byte, 32))}
	post(t, url+"/v1/blobs/create", &wire.BlobCreate{Hash: h[:], Size: uint64(len(data)), TokenHashes: tokens})
	upload := meta.BlobUploadID(h[:], uint64(len(data)), tokens)
	post(t, url+"/v1/blobs/chunk", &wire.BlobChunk{Hash: h[:], Upload: upload, Data: data})
	return hex.EncodeToString(h[:])
}

func runBlobs(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := blobsCommand(context.Background(), args, &out)
	return out.String(), err
}

func TestBlobsCommand(t *testing.T) {
	dir := t.TempDir()
	cfg := meta.DefaultConfig()
	cfg.DirectoryEnabled = true // the tests exercise the directory; it's off by default
	cfg.DataDir = dir
	srv, err := meta.Open(context.Background(), cfg, testutil.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	defer func() { hs.Close(); srv.Close() }()
	live := uploadAt(t, srv, hs.URL, time.Now(), []byte("live blob"))
	old := uploadAt(t, srv, hs.URL, time.Now().Add(-73*time.Hour), []byte("expired blob"))
	gone := uploadAt(t, srv, hs.URL, time.Now(), []byte("deleted blob"))

	out, err := runBlobs(t, "stats", "-data", dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"stored: 3 blobs", live, old, gone} {
		if !strings.Contains(out, want) {
			t.Fatalf("stats output lacks %q:\n%s", want, out)
		}
	}
	if out, err = runBlobs(t, "delete", "-data", dir, "-id", gone); err != nil || !strings.Contains(out, "deleted "+gone) {
		t.Fatalf("delete: %v %s", err, out)
	}
	if _, err = runBlobs(t, "delete", "-data", dir, "-id", gone); err == nil {
		t.Fatal("deleting a missing blob succeeded")
	}
	if out, err = runBlobs(t, "purge-expired", "-data", dir); err != nil || !strings.Contains(out, "deleted 1 blobs") {
		t.Fatalf("purge: %v %s", err, out)
	}
	out, _ = runBlobs(t, "stats", "-data", dir)
	if !strings.Contains(out, "stored: 1 blobs") || !strings.Contains(out, live) || strings.Contains(out, old) || strings.Contains(out, gone) {
		t.Fatalf("stats after delete and purge:\n%s", out)
	}

	for _, args := range [][]string{
		nil,
		{"stats"},
		{"bogus", "-data", dir},
		{"delete", "-data", dir},
		{"delete", "-data", dir, "-id", "nothex"},
		{"stats", "-data", t.TempDir()},
		{"stats", "-nope"},
	} {
		if _, err := runBlobs(t, args...); err == nil {
			t.Fatalf("%v succeeded", args)
		}
	}
}

// apiClient announces this build's protocol versions (the maintainer API
// refuses requests without them).
var apiClient = &http.Client{Transport: version.Transport{}}
