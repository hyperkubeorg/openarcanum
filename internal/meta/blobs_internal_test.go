package meta

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/store"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Schema 3 (blobs keyed by upload id) keeps blobs stored under schema 2:
// their upload id is their blob id, so their files keep their names.
func TestBlobSchemaMigrationKeepsStoredBlobs(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	db, err := store.Open(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Apply(ctx, db, migrations[:2]); err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("old blob"), 300)
	sum := sha256.Sum256(data)
	token := bytes.Repeat([]byte{4}, 32)
	now := time.Now().UnixMilli()
	if _, err := db.Exec(`INSERT INTO blobs (hash, size, received, complete, created_at, touched_at, expires_at) VALUES (?, ?, ?, 1, ?, ?, ?)`,
		sum[:], len(data), []byte{0xff}, now, now, now+3600_000); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO blob_tokens (hash, token_hash) VALUES (?, ?)`, sum[:], BlobTokenHash(token)); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := os.MkdirAll(filepath.Join(dir, "blobs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "blobs", hex.EncodeToString(sum[:])), data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.DirectoryEnabled = true // the tests exercise the directory; it's off by default
	cfg.DataDir = dir
	s, err := Open(ctx, cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	body, _ := proto.Marshal(&wire.BlobFetch{Hash: sum[:], Token: token})
	res, err := s.blobFetch(httptest.NewRequest("POST", "/v1/blobs/fetch", nil), body)
	if err != nil {
		t.Fatalf("fetch after migration: %v", err)
	}
	if got := res.(*wire.BlobChunkData).Data; !bytes.Equal(got, data) {
		t.Fatal("migrated blob changed")
	}
}

// A client that hangs up while sending the last chunk (its request context
// is cancelled) still leaves a finished upload, not a half-recorded one.
func TestBlobLastChunkSurvivesDisconnect(t *testing.T) {
	s := openTestNode(t, nil, slog.New(slog.DiscardHandler))
	data := bytes.Repeat([]byte("openarcanum"), 1000)
	sum := sha256.Sum256(data)
	th := BlobTokenHash(bytes.Repeat([]byte{1}, 32))
	body, _ := proto.Marshal(&wire.BlobCreate{Hash: sum[:], Size: uint64(len(data)), TokenHashes: [][]byte{th}})
	res, err := s.blobCreate(httptest.NewRequest("POST", "/v1/blobs/create", nil), body)
	if err != nil {
		t.Fatal(err)
	}
	st := res.(*wire.BlobUploadStatus)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest("POST", "/v1/blobs/chunk", nil).WithContext(ctx)
	body, _ = proto.Marshal(&wire.BlobChunk{Hash: sum[:], Upload: st.Upload, Index: 0, Data: data})
	if _, err := s.blobChunk(req, body); err != nil {
		t.Fatalf("last chunk with the client gone: %v", err)
	}
	row, err := s.blobs.get(context.Background(), st.Upload)
	if err != nil || row == nil || !row.complete {
		t.Fatalf("upload after the disconnect: %+v %v", row, err)
	}
}

// The migration to never-reused mailbox ids keeps what's waiting, and new
// items get ids above the old ones.
func TestMailboxMigrationKeepsItems(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	db, err := store.Open(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Apply(ctx, db, migrations[:3]); err != nil {
		t.Fatal(err)
	}
	token := bytes.Repeat([]byte{7}, 32)
	now := time.Now().UnixMilli()
	for _, id := range []int64{5, 9} {
		if _, err := db.Exec(`INSERT INTO mailbox (id, inbox, blob, size, received_at, expires_at) VALUES (?, ?, ?, 1, ?, ?)`,
			id, InboxAddress(token), []byte{byte(id)}, now, now+3600_000); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	cfg := DefaultConfig()
	cfg.DirectoryEnabled = true // the tests exercise the directory; it's off by default
	cfg.DataDir = dir
	s, err := Open(ctx, cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	fetch := func() []*wire.MailboxItem {
		body, _ := proto.Marshal(&wire.MailboxFetch{Token: token})
		res, err := s.mailboxFetch(httptest.NewRequest("POST", "/v1/mailbox/fetch", nil), body)
		if err != nil {
			t.Fatal(err)
		}
		return res.(*wire.MailboxItems).Items
	}
	got := fetch()
	if len(got) != 2 || got[0].Id != 5 || got[1].Id != 9 || got[1].Blob[0] != 9 {
		t.Fatalf("after migration: %v", got)
	}
	// Delete the newest, add one: its id is above 9.
	del, _ := proto.Marshal(&wire.MailboxDelete{Token: token, Ids: []int64{9}})
	if _, err := s.mailboxDelete(httptest.NewRequest("POST", "/v1/mailbox/delete", nil), del); err != nil {
		t.Fatal(err)
	}
	drop, _ := proto.Marshal(&wire.MailboxDrop{Inbox: InboxAddress(token), Blob: []byte{1}})
	if _, err := s.mailboxDrop(httptest.NewRequest("POST", "/v1/mailbox/drop", nil), drop); err != nil {
		t.Fatal(err)
	}
	if got := fetch(); len(got) != 2 || got[1].Id <= 9 {
		t.Fatalf("new item reused an id: %v", got)
	}
}

// Searching by tag and browsing use their indexes instead of scanning and
// sorting every listing (load test: p95 218 ms at 10,000 listings without).
func TestSearchUsesIndexes(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DirectoryEnabled = true // the tests exercise the directory; it's off by default
	cfg.DataDir = t.TempDir()
	s, err := Open(context.Background(), cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, c := range []struct{ sql string }{{searchTagSQL}, {searchRecentSQL}} {
		rows, err := s.db.Query("EXPLAIN QUERY PLAN "+c.sql, "x", 25)
		if c.sql == searchRecentSQL {
			rows, err = s.db.Query("EXPLAIN QUERY PLAN "+c.sql, 25)
		}
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		rows.Close()
		joined := strings.Join(plan, "; ")
		// The planner may walk either new index (it depends on statistics);
		// what must never happen is sorting every listing.
		uses := strings.Contains(joined, "listing_tags_tag") || strings.Contains(joined, "listings_recent")
		if !uses || strings.Contains(joined, "TEMP B-TREE") {
			t.Errorf("plan for %q: %s", c.sql, joined)
		}
	}
}
