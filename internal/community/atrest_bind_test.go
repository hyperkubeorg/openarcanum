package community_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/store"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

func queryInt(t *testing.T, dir, q string, args ...any) int64 {
	t.Helper()
	db, err := store.Open(filepath.Join(dir, "community.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int64
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func joinKeyHash(secret string) []byte {
	h := sha256.Sum256([]byte("openarcanum/join-key/v1\x00" + secret))
	return h[:]
}

// Plaintext columns that decide access are bound to sealed data
// (atrest_bind.go). Someone who can write the database file but has no
// storage key edits them on disk; the server refuses and reports instead of
// silently letting the change take effect.
func TestTamperedPlaintextColumnsAreRefused(t *testing.T) {
	e := setup(t, workloadSettings())
	ctx := testutil.Ctx(t)
	revoked := e.newInvite(nil)
	if _, err := e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_RevokeJoinKey{RevokeJoinKey: &wire.RevokeJoinKey{Id: revoked.Id}}}); err != nil {
		t.Fatal(err)
	}
	once := e.newInvite(func(c *wire.CreateJoinKey) { c.MaxUses = 1 })
	e.member("First", once.Secret)
	open := e.newInvite(nil)
	w := runWorkload(t, e)
	gen := e.owner.Channel("general").Id
	e.owner.Close()
	e.comm.Stop()
	dir := e.comm.Dir
	pristine, err := os.ReadFile(filepath.Join(dir, "community.db"))
	if err != nil {
		t.Fatal(err)
	}
	restore := func() {
		t.Helper()
		for _, f := range []string{"community.db-wal", "community.db-shm"} {
			os.Remove(filepath.Join(dir, f))
		}
		if err := os.WriteFile(filepath.Join(dir, "community.db"), pristine, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// live runs f against the server restarted on the (tampered) database.
	live := func(f func(m *testutil.Member)) {
		t.Helper()
		e.comm.Restart(t, e.relay.URL)
		defer e.comm.Stop()
		m := testutil.NewMember(t, "Probe")
		if err := m.Connect(ctx, e.relay.URL, e.serverKey()); err != nil {
			t.Fatal(err)
		}
		defer m.Close()
		f(m)
	}
	asOwner := func(f func(m *testutil.Member)) {
		t.Helper()
		e.comm.Restart(t, e.relay.URL)
		defer e.comm.Stop()
		m := testutil.NewMember(t, "Owner")
		m.Identity, m.Static = e.owner.Identity, e.owner.Static
		if err := m.Connect(ctx, e.relay.URL, e.serverKey()); err != nil {
			t.Fatal(err)
		}
		defer m.Close()
		f(m)
	}

	// The untouched database admits with a valid key.
	live(func(m *testutil.Member) {
		if res, err := m.Join(ctx, open.Secret); err != nil || res.Status != wire.JoinStatus_JOIN_STATUS_ADMITTED {
			t.Fatalf("valid key on the untouched database: %v %v", res, err)
		}
	})

	for _, c := range []struct {
		name, sql string
		args      []any
		secret    string
	}{
		{"un-revoked join key", `UPDATE join_keys SET revoked = 0 WHERE id = ?`, []any{revoked.Id}, revoked.Secret},
		{"use count reset", `UPDATE join_keys SET uses = 0 WHERE id = ?`, []any{once.Id}, once.Secret},
		{"secret hash replaced", `UPDATE join_keys SET secret_hash = ? WHERE id = ?`, []any{joinKeyHash("attacker-chosen"), open.Id}, "attacker-chosen"},
	} {
		restore()
		execRaw(t, dir, c.sql, c.args...)
		live(func(m *testutil.Member) {
			res, err := m.Join(ctx, c.secret)
			if err == nil {
				t.Fatalf("%s: join answered %v instead of reporting the tampering", c.name, res)
			}
		})
	}

	// A key package moved to another device's row.
	restore()
	execRaw(t, dir, `UPDATE key_packages SET device = ?`, bytes.Repeat([]byte{7}, 32))
	asOwner(func(m *testutil.Member) {
		if _, err := m.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_GetContactCard{GetContactCard: &wire.GetContactCard{Member: w.Alice.Identity.Public}}}); err == nil {
			t.Fatal("key package under another device handed out")
		}
	})

	// Messages and a file moved into another channel.
	restore()
	fileID := queryInt(t, dir, `SELECT min(id) FROM files`)
	execRaw(t, dir, `UPDATE messages SET channel_id = ? WHERE channel_id = ?`, w.Heron, gen)
	execRaw(t, dir, `UPDATE files SET channel_id = ?`, w.Heron)
	srv := reopen(t, dir, e.relay.URL, testutil.Quiet())
	var te *community.TamperError
	if _, err := srv.Messages(context.Background(), w.Heron); !errors.As(err, &te) || te.Table != "messages" || te.Column != "channel_id" {
		t.Fatalf("messages moved to another channel: %v", err)
	}
	if _, err := srv.FileChannel(context.Background(), fileID); !errors.As(err, &te) || te.Table != "files" || te.Column != "channel_id" {
		t.Fatalf("file moved to another channel: %v", err)
	}
	srv.Close()

	// Restored, everything reads again.
	restore()
	srv = reopen(t, dir, e.relay.URL, testutil.Quiet())
	if ch, err := srv.FileChannel(context.Background(), fileID); err != nil || ch != gen {
		t.Fatalf("untouched file: %d %v", ch, err)
	}
	if msgs, err := srv.Messages(context.Background(), gen); err != nil || len(msgs) == 0 {
		t.Fatalf("untouched messages: %v %v", msgs, err)
	}
}

// Shutdown checkpoints the WAL into the database file and truncates it
// (docs/threat-model.md: a failed checkpoint is retried at shutdown).
func TestShutdownCheckpointsWAL(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	for i := 0; i < 20; i++ {
		if _, err := e.owner.Post(ctx, e.owner.Channel("general").Id, "words for the write-ahead log"); err != nil {
			t.Fatal(err)
		}
	}
	e.comm.Stop()
	if st, err := os.Stat(filepath.Join(e.comm.Dir, "community.db-wal")); err == nil && st.Size() != 0 {
		t.Fatalf("WAL holds %d bytes after shutdown", st.Size())
	}
}

// Schemas 7 and 8 bind the columns of a database written by an older schema:
// join keys, messages and files created under schema 3 read back and keep
// working after the migration, and tampering is caught on them too.
func TestMigrationV7BindsExistingRows(t *testing.T) {
	dir, _, _ := copyFixture(t)
	cfg := community.DefaultConfig(dir)
	if err := community.MigrateTo(context.Background(), cfg, 3); err != nil {
		t.Fatal(err)
	}
	if v := schemaVersion(t, dir); v != 3 {
		t.Fatalf("fixture at schema %d", v)
	}
	srv := reopen(t, dir, "ws://127.0.0.1:1/ws", testutil.Quiet())
	if v := schemaVersion(t, dir); v != community.LatestSchema() {
		t.Fatalf("schema after opening: %d", v)
	}
	ctx := context.Background()
	labels, _, err := srv.JoinKeyLabels(ctx)
	if err != nil || len(labels) == 0 {
		t.Fatalf("join keys after migration: %v %v", labels, err)
	}
	snap := srv.Snapshot()
	total := 0
	for _, id := range snap.Channels {
		msgs, err := srv.Messages(ctx, id)
		if err != nil {
			t.Fatalf("messages after migration: %v", err)
		}
		total += len(msgs)
	}
	if total == 0 {
		t.Fatal("no messages after migration")
	}
	fileID := queryInt(t, dir, `SELECT min(id) FROM files`)
	if _, err := srv.FileChannel(ctx, fileID); err != nil {
		t.Fatalf("file after migration: %v", err)
	}
	srv.Close()
	execRaw(t, dir, `UPDATE join_keys SET revoked = 1 - revoked`)
	srv = reopen(t, dir, "ws://127.0.0.1:1/ws", testutil.Quiet())
	var te *community.TamperError
	if _, _, err := srv.JoinKeyLabels(ctx); !errors.As(err, &te) || te.Table != "join_keys" {
		t.Fatalf("tampered migrated join key: %v", err)
	}
}

// The messaging columns (schema 5) are bound too: a posted file turned back
// into a staged one, and a pin moved to another channel, are reported.
func TestTamperedMessagingColumnsAreRefused(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	gen := e.owner.Channel("general").Id
	res, err := e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: "other"}}})
	if err != nil {
		t.Fatal(err)
	}
	other := res.Id
	f, err := stage(t, e.owner, gen, "notes.txt", "text/plain", []byte("bound file"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := call(t, e.owner, &wire.Post{ChannelId: gen, Text: "with a file", FileIds: []int64{f.FileId}})
	if err != nil {
		t.Fatal(err)
	}
	msg := r.GetMessage()
	if _, err := call(t, e.owner, &wire.Pin{MessageId: msg.Id}); err != nil {
		t.Fatal(err)
	}
	e.owner.Close()
	e.comm.Stop()
	dir := e.comm.Dir
	pristine, err := os.ReadFile(filepath.Join(dir, "community.db"))
	if err != nil {
		t.Fatal(err)
	}
	restore := func() {
		t.Helper()
		for _, f := range []string{"community.db-wal", "community.db-shm"} {
			os.Remove(filepath.Join(dir, f))
		}
		if err := os.WriteFile(filepath.Join(dir, "community.db"), pristine, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// A posted file made "staged" again (so its uploader could attach it
	// to another message) no longer reads.
	execRaw(t, dir, `UPDATE files SET message_id = 0 WHERE id = ?`, f.FileId)
	srv := reopen(t, dir, e.relay.URL, testutil.Quiet())
	var te *community.TamperError
	if _, err := srv.FileChannel(context.Background(), f.FileId); !errors.As(err, &te) || te.Table != "files" || te.Column != "message_id" {
		t.Fatalf("posted file made staged: %v", err)
	}
	srv.Close()

	// A pin moved to another channel isn't listed there.
	restore()
	execRaw(t, dir, `UPDATE pins SET channel_id = ?`, other)
	e.comm.Restart(t, e.relay.URL)
	owner := testutil.NewMember(t, "Owner")
	owner.Identity, owner.Static = e.owner.Identity, e.owner.Static
	if err := owner.Connect(ctx, e.relay.URL, e.serverKey()); err != nil {
		t.Fatal(err)
	}
	if r, err := call(t, owner, &wire.ListPins{ChannelId: other}); err == nil {
		t.Fatalf("pin moved to another channel listed there: %v", r.GetPins().GetMessages())
	}
	owner.Close()
	e.comm.Stop()

	// Restored, both read again.
	restore()
	srv = reopen(t, dir, e.relay.URL, testutil.Quiet())
	if ch, err := srv.FileChannel(context.Background(), f.FileId); err != nil || ch != gen {
		t.Fatalf("untouched file: %d %v", ch, err)
	}
	srv.Close()
}

// The report queue's plaintext columns (schema 6) are bound: a report
// closed, reopened or re-pointed on disk is reported when it's read.
func TestTamperedReportColumnsAreRefused(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	gen := e.owner.Channel("general").Id
	msg := post(t, e.owner, gen, "report me")
	if _, err := alice.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_ReportMessage{ReportMessage: &wire.ReportMessage{MessageId: msg.Id, Reason: "rude"}}}); err != nil {
		t.Fatal(err)
	}
	e.owner.Close()
	e.comm.Stop()
	dir := e.comm.Dir
	pristine, err := os.ReadFile(filepath.Join(dir, "community.db"))
	if err != nil {
		t.Fatal(err)
	}
	reports := func() error {
		t.Helper()
		e.comm.Restart(t, e.relay.URL)
		defer e.comm.Stop()
		owner := testutil.NewMember(t, "Owner")
		owner.Identity, owner.Static = e.owner.Identity, e.owner.Static
		if err := owner.Connect(ctx, e.relay.URL, e.serverKey()); err != nil {
			t.Fatal(err)
		}
		defer owner.Close()
		_, err := owner.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_ListReports{ListReports: &wire.ListReports{IncludeClosed: true}}})
		return err
	}
	for _, c := range []struct{ name, sql string }{
		{"status", `UPDATE reports SET status = 2`},
		{"month", `UPDATE reports SET month = month - 1`},
		{"dedupe", `UPDATE reports SET dedupe = NULL`},
	} {
		for _, f := range []string{"community.db-wal", "community.db-shm"} {
			os.Remove(filepath.Join(dir, f))
		}
		if err := os.WriteFile(filepath.Join(dir, "community.db"), pristine, 0o600); err != nil {
			t.Fatal(err)
		}
		execRaw(t, dir, c.sql)
		if err := reports(); err == nil {
			t.Fatalf("report with a tampered %s listed", c.name)
		}
	}
}
