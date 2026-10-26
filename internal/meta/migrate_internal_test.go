package meta

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/store"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// seedAt fills a database that is at schema version v with one row of
// every kind that version has, in that version's table layout, and writes
// the blob file it names. It returns what a reader should find afterwards.
type seeded struct {
	token    []byte // mailbox token
	mailbox  map[int64]byte
	blob     []byte
	blobHash []byte
	blobTok  []byte
}

func seedAt(t *testing.T, ctx context.Context, db *sql.DB, dir string, v int) *seeded {
	t.Helper()
	now := time.Now().UnixMilli()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("schema %d: %s: %v", v, q, err)
		}
	}
	// Schema 1 tables (present in every version).
	exec(`INSERT INTO listings (id, community, signed, seq, withdrawn, rev, updated_at) VALUES (7, ?, ?, 3, 0, 11, ?)`, []byte("community-key-0123456789abcdef01"), []byte("signed-listing-bytes"), now)
	exec(`INSERT INTO listings_fts (rowid, name, description, tags) VALUES (7, 'Okapi Reading Room', 'quiet books', 'books')`)
	exec(`INSERT INTO listing_tags (listing_id, tag) VALUES (7, 'books')`)
	exec(`INSERT INTO routes (route_id, owner, signed, seq, rev, updated_at) VALUES (?, ?, ?, 2, 12, ?)`, bytes.Repeat([]byte{9}, 16), []byte("owner"), []byte("signed-route"), now)
	exec(`INSERT INTO relays (url, relay_key, signed, at, region, capacity, connections, rev) VALUES ('wss://relay.example.invalid/ws', ?, ?, ?, 'eu', 100, 5, 13)`, []byte("relay-key"), []byte("signed-hb"), now)
	exec(`INSERT INTO nameplates (number, topic, relay, expires_at, claimed) VALUES (42, ?, 'wss://relay.example.invalid/ws', ?, 0)`, bytes.Repeat([]byte{3}, 32), now+3600_000)
	exec(`INSERT INTO counters (k, v) VALUES ('rev', 13), ('peer:https://peer.example.invalid', 99)`)
	s := &seeded{token: bytes.Repeat([]byte{byte(v)}, 32), mailbox: map[int64]byte{}}
	for _, id := range []int64{4, 8} {
		if v >= schemaMailboxGID {
			exec(`INSERT INTO mailbox (id, inbox, blob, size, received_at, expires_at, gid) VALUES (?, ?, ?, 1, ?, ?, ?)`, id, InboxAddress(s.token), []byte{byte(id)}, now, now+3600_000,
				MailboxGID(InboxAddress(s.token), []byte{byte(id)}))
		} else {
			exec(`INSERT INTO mailbox (id, inbox, blob, size, received_at, expires_at) VALUES (?, ?, ?, 1, ?, ?)`, id, InboxAddress(s.token), []byte{byte(id)}, now, now+3600_000)
		}
		s.mailbox[id] = byte(id)
	}
	if v < 2 {
		return s
	}
	s.blob = bytes.Repeat([]byte(fmt.Sprintf("blob stored at schema %d ", v)), 500)
	sum := sha256.Sum256(s.blob)
	s.blobHash, s.blobTok = sum[:], bytes.Repeat([]byte{0xb0 + byte(v)}, 32)
	name := hex.EncodeToString(sum[:])
	if v == 2 {
		exec(`INSERT INTO blobs (hash, size, received, complete, created_at, touched_at, expires_at) VALUES (?, ?, ?, 1, ?, ?, ?)`, sum[:], len(s.blob), []byte{0xff}, now, now, now+3600_000)
		exec(`INSERT INTO blob_tokens (hash, token_hash) VALUES (?, ?)`, sum[:], BlobTokenHash(s.blobTok))
	} else {
		// Schema 3 on: rows keyed by upload id.
		upload := BlobUploadID(sum[:], uint64(len(s.blob)), [][]byte{BlobTokenHash(s.blobTok)})
		name = hex.EncodeToString(upload)
		exec(`INSERT INTO blobs (id, hash, size, received, complete, created_at, touched_at, expires_at) VALUES (?, ?, ?, ?, 1, ?, ?, ?)`, upload, sum[:], len(s.blob), []byte{0xff}, now, now, now+3600_000)
		exec(`INSERT INTO blob_tokens (upload, token_hash) VALUES (?, ?)`, upload, BlobTokenHash(s.blobTok))
	}
	if err := os.MkdirAll(filepath.Join(dir, "blobs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "blobs", name), s.blob, 0o600); err != nil {
		t.Fatal(err)
	}
	return s
}

// Every schema version this build has ever written migrates to the
// current one with all its data: from each earlier version, every later
// step runs and keeps listings (and their search index), routes, relays,
// nameplates, replication cursors, mailbox items (whose ids are never
// reused afterwards) and blobs (still fetchable with their token).
func TestMigrationFromEveryVersionKeepsData(t *testing.T) {
	ctx := context.Background()
	for v := 1; v < len(migrations); v++ {
		t.Run(fmt.Sprintf("from_v%d", v), func(t *testing.T) {
			dir := t.TempDir()
			db, err := store.Open(filepath.Join(dir, "meta.db"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Apply(ctx, db, migrations[:v]); err != nil {
				t.Fatal(err)
			}
			seed := seedAt(t, ctx, db, dir, v)
			db.Close()

			cfg := DefaultConfig()
			cfg.DirectoryEnabled = true // the tests exercise the directory; it's off by default
			cfg.DataDir = dir
			s, err := Open(ctx, cfg, slog.New(slog.DiscardHandler))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if got, _ := store.Version(ctx, s.db); got != len(migrations) {
				t.Fatalf("schema after open: %d", got)
			}
			var signed []byte
			var seq, rev int64
			if err := s.db.QueryRow(`SELECT signed, seq, rev FROM listings WHERE id = 7`).Scan(&signed, &seq, &rev); err != nil || string(signed) != "signed-listing-bytes" || seq != 3 || rev != 11 {
				t.Fatalf("listing: %q %d %d %v", signed, seq, rev, err)
			}
			var hit int64
			if err := s.db.QueryRow(`SELECT rowid FROM listings_fts WHERE listings_fts MATCH 'okapi'`).Scan(&hit); err != nil || hit != 7 {
				t.Fatalf("search index: %d %v", hit, err)
			}
			for q, want := range map[string]string{
				`SELECT tag FROM listing_tags WHERE listing_id = 7`:                    "books",
				`SELECT signed FROM routes`:                                            "signed-route",
				`SELECT region FROM relays`:                                            "eu",
				`SELECT relay FROM nameplates WHERE number = 42`:                       "wss://relay.example.invalid/ws",
				`SELECT v FROM counters WHERE k = 'peer:https://peer.example.invalid'`: "99",
			} {
				var got string
				if err := s.db.QueryRow(q).Scan(&got); err != nil || got != want {
					t.Errorf("%s = %q, %v; want %q", q, got, err, want)
				}
			}
			body, _ := proto.Marshal(&wire.MailboxFetch{Token: seed.token})
			res, err := s.mailboxFetch(httptest.NewRequest("POST", "/v1/mailbox/fetch", nil), body)
			if err != nil {
				t.Fatal(err)
			}
			items := res.(*wire.MailboxItems).Items
			if len(items) != len(seed.mailbox) {
				t.Fatalf("mailbox items: %v", items)
			}
			for _, it := range items {
				if seed.mailbox[it.Id] != it.Blob[0] {
					t.Fatalf("mailbox item %d changed", it.Id)
				}
			}
			// The newest id deleted, a new item gets a higher one.
			del, _ := proto.Marshal(&wire.MailboxDelete{Token: seed.token, Ids: []int64{8}})
			if _, err := s.mailboxDelete(httptest.NewRequest("POST", "/", nil), del); err != nil {
				t.Fatal(err)
			}
			drop, _ := proto.Marshal(&wire.MailboxDrop{Inbox: InboxAddress(seed.token), Blob: []byte{1}})
			if _, err := s.mailboxDrop(httptest.NewRequest("POST", "/", nil), drop); err != nil {
				t.Fatal(err)
			}
			var maxID int64
			if err := s.db.QueryRow(`SELECT max(id) FROM mailbox`).Scan(&maxID); err != nil || maxID <= 8 {
				t.Fatalf("new mailbox id %d reuses a deleted one (%v)", maxID, err)
			}
			if seed.blob != nil {
				fb, _ := proto.Marshal(&wire.BlobFetch{Hash: seed.blobHash, Token: seed.blobTok})
				res, err := s.blobFetch(httptest.NewRequest("POST", "/v1/blobs/fetch", nil), fb)
				if err != nil {
					t.Fatalf("blob after migration: %v", err)
				}
				if !bytes.Equal(res.(*wire.BlobChunkData).Data, seed.blob) {
					t.Fatal("blob changed")
				}
			}
		})
	}
}

// The schema step that gives mailbox items their global ids keeps the
// oldest of identical items (the same message dropped twice) and files
// every item under its content's id.
func TestMailboxGlobalIDMigration(t *testing.T) {
	if reflect.ValueOf(migrations[schemaMailboxGID-1].Go).Pointer() != reflect.ValueOf(mailboxGlobalIDs).Pointer() {
		t.Fatalf("schemaMailboxGID (%d) isn't the version mailboxGlobalIDs brings", schemaMailboxGID)
	}
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Apply(ctx, db, migrations[:schemaMailboxGID-1]); err != nil {
		t.Fatal(err)
	}
	inbox := InboxAddress(bytes.Repeat([]byte{3}, 32))
	now := time.Now().UnixMilli()
	for id, blob := range map[int64]string{1: "same", 2: "other", 3: "same"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO mailbox (id, inbox, blob, size, received_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
			id, inbox, []byte(blob), len(blob), now, now+3600_000); err != nil {
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
	rows, err := s.db.Query(`SELECT id, blob, gid FROM mailbox ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		var blob, gid []byte
		if err := rows.Scan(&id, &blob, &gid); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(gid, MailboxGID(inbox, blob)) {
			t.Fatalf("item %d has gid %x", id, gid)
		}
		ids = append(ids, id)
	}
	if fmt.Sprint(ids) != "[1 2]" {
		t.Fatalf("items after the migration: %v", ids)
	}
	if s.mailboxBytes != uint64(len("same")+len("other")) {
		t.Fatalf("mailbox bytes %d", s.mailboxBytes)
	}
}

// A database written by a newer release is refused with an error that
// says so, and is left untouched.
func TestNewerSchemaRefused(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.DirectoryEnabled = true // the tests exercise the directory; it's off by default
	cfg.DataDir = dir
	s, err := Open(ctx, cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	newer := len(migrations) + 1
	if _, err := s.db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, newer)); err != nil {
		t.Fatal(err)
	}
	s.Close()
	_, err = Open(ctx, cfg, slog.New(slog.DiscardHandler))
	if err == nil || !strings.Contains(err.Error(), "newer than this build supports") || !strings.Contains(err.Error(), "meta.db") {
		t.Fatalf("err = %v", err)
	}
	if _, err := OpenBlobAdmin(ctx, dir, nil); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("schema version %d", newer)) {
		t.Fatalf("blob admin err = %v", err)
	}
	db, _ := store.Open(filepath.Join(dir, "meta.db"))
	defer db.Close()
	if v, _ := store.Version(ctx, db); v != newer {
		t.Fatalf("version changed to %d", v)
	}
}
