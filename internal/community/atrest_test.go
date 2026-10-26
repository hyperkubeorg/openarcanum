package community_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/store"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// assertNoPlaintext fails if any secret appears in any of the files (a
// missing file, such as an absent WAL, is skipped).
func assertNoPlaintext(t *testing.T, secrets [][]byte, paths ...string) {
	t.Helper()
	checked := 0
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, s := range secrets {
			if len(s) > 0 && bytes.Contains(b, s) {
				t.Errorf("%s contains plaintext %q", filepath.Base(p), printable(s))
			}
		}
	}
	if checked == 0 {
		t.Fatal("no database file to check")
	}
}

func printable(b []byte) string {
	for _, c := range b {
		if c < 0x20 || c > 0x7e {
			return "hex:" + hex.EncodeToString(b)
		}
	}
	return string(b)
}

func dbFiles(dir string) []string {
	p := filepath.Join(dir, "community.db")
	return []string{p, p + "-wal"}
}

// Everything a realistic workload stores is unreadable in the database file,
// its WAL and a decrypted backup snapshot: names, channel names, role names,
// labels, reasons, message text, file names and types, avatars, contact
// cards, key packages, join-key secrets, member and device public keys and
// channel keys.
func TestAtRestNoPlaintextInDatabase(t *testing.T) {
	e := setup(t, workloadSettings())
	w := runWorkload(t, e)
	ctx := testutil.Ctx(t)
	// While running (recent pages are in the WAL).
	assertNoPlaintext(t, w.Strings, dbFiles(e.comm.Dir)...)

	// The backup's database snapshot (after decrypting the backup itself).
	path, err := e.comm.Server.Backup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := os.ReadFile(filepath.Join(e.comm.Dir, "storage.key"))
	k, _ := hex.DecodeString(strings.TrimSpace(string(key)))
	enc, _ := os.ReadFile(path)
	snap, err := community.DecryptBackup(k, enc)
	if err != nil {
		t.Fatal(err)
	}
	snapPath := filepath.Join(t.TempDir(), "snapshot.db")
	if err := os.WriteFile(snapPath, snap, 0o600); err != nil {
		t.Fatal(err)
	}
	assertNoPlaintext(t, w.Strings, snapPath)

	// After a clean shutdown (WAL checkpointed into the main file).
	e.comm.Stop()
	assertNoPlaintext(t, w.Strings, dbFiles(e.comm.Dir)...)

	// And the data is all still there after a restart.
	srv := reopen(t, e.comm.Dir, e.relay.URL, testutil.Quiet())
	snapModel := srv.Snapshot()
	for _, n := range []string{"Owner", "Alice Marmot", "Mallory Narwhal"} {
		if _, ok := snapModel.Members[n]; !ok {
			t.Errorf("member %q missing after restart", n)
		}
	}
	if _, ok := snapModel.Channels["heron-lounge"]; !ok || snapModel.Roles["Pelican Wranglers"] == 0 {
		t.Errorf("channels/roles after restart: %v %v", snapModel.Channels, snapModel.Roles)
	}
	msgs, err := srv.Messages(ctx, w.Heron)
	if err != nil || len(msgs) != 1 || msgs[0].Text != "heron lounge chatter about quokkas" {
		t.Fatalf("heron history after restart: %v %v", msgs, err)
	}
}

func reopen(t *testing.T, dir, relay string, log *slog.Logger) *community.Server {
	t.Helper()
	srv, err := openDir(dir, relay, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv
}

func openDir(dir, relay string, log *slog.Logger) (*community.Server, error) {
	cfg := community.DefaultConfig(dir)
	cfg.Relays = []string{relay}
	cfg.BackupInterval.Duration = 0
	return community.Open(context.Background(), cfg, log)
}

// copyFixture copies the schema 1 database in testdata/v1 (written by
// running runWorkload against a schema 1 build).
func copyFixture(t *testing.T) (string, [][]byte, string) {
	t.Helper()
	dir := t.TempDir()
	for _, f := range []string{"community.db", "storage.key", "server.key", "noise.key"} {
		b, err := os.ReadFile(filepath.Join("testdata", "v1", f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var fx struct {
		PlaintextHex []string `json:"plaintext_hex"`
		Alice        string   `json:"alice"`
	}
	b, err := os.ReadFile(filepath.Join("testdata", "v1", "fixture.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatal(err)
	}
	var secrets [][]byte
	for _, h := range fx.PlaintextHex {
		v, _ := hex.DecodeString(h)
		secrets = append(secrets, v)
	}
	return dir, secrets, fx.Alice
}

func schemaVersion(t *testing.T, dir string) int {
	t.Helper()
	db, err := store.Open(filepath.Join(dir, "community.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := store.Version(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// A schema 1 database (plaintext roll) is converted in place: every record survives, and no plaintext is left in
// the file.
func TestMigrationFromV1Database(t *testing.T) {
	dir, secrets, aliceHex := copyFixture(t)
	if v := schemaVersion(t, dir); v != 1 {
		t.Fatalf("fixture is at schema %d, want 1", v)
	}
	// The fixture really holds plaintext (so the check below means something).
	raw, _ := os.ReadFile(filepath.Join(dir, "community.db"))
	if !bytes.Contains(raw, []byte("Alice Marmot")) || !bytes.Contains(raw, []byte("Walrus invite label")) {
		t.Fatal("fixture doesn't contain the plaintext it should")
	}
	ctx := context.Background()
	srv, err := openDir(dir, "ws://127.0.0.1:1/ws", testutil.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	snap := srv.Snapshot()
	if snap.Settings.Name != "Zephyr Okapi Society" || len(snap.Owner) == 0 {
		t.Fatalf("settings/owner after migration: %v", snap.Settings)
	}
	for _, n := range []string{"Owner", "Alice Marmot", "Mallory Narwhal"} {
		if _, ok := snap.Members[n]; !ok {
			t.Errorf("member %q lost", n)
		}
	}
	if snap.Members["Mallory Narwhal"] != int64(wire.MemberStatus_MEMBER_STATUS_BANNED) {
		t.Errorf("mallory's status: %d", snap.Members["Mallory Narwhal"])
	}
	gen, heron := snap.Channels["general"], snap.Channels["heron-lounge"]
	if gen == 0 || heron == 0 || snap.Roles["Pelican Wranglers"] == 0 || snap.Roles["@everyone"] == 0 {
		t.Fatalf("channels %v roles %v", snap.Channels, snap.Roles)
	}
	msgs, err := srv.Messages(ctx, gen)
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	var file *wire.FileRef
	deleted := 0
	for _, m := range msgs {
		if len(m.Author) == 0 {
			t.Errorf("message %d lost its author", m.Id)
		}
		if m.Deleted {
			deleted++
			continue
		}
		texts = append(texts, m.Text)
		if m.Text == "alice says flamingo, edited" && m.EditedAt == 0 {
			t.Error("edit time lost")
		}
		if firstFile(m) != nil {
			file = firstFile(m)
		}
	}
	want := []string{"the secret capybara message", "alice says flamingo, edited", "caption about a gecko"}
	if !slices.Equal(texts, want) || deleted != 1 {
		t.Fatalf("general after migration: %q (%d deleted)", texts, deleted)
	}
	if file == nil || file.Name != "tapir-plans.txt" || file.Mime != "application/x-jackalope" || len(file.Key) != 32 {
		t.Fatalf("file ref after migration: %v", file)
	}
	if hm, err := srv.Messages(ctx, heron); err != nil || len(hm) != 1 || hm[0].Text != "heron lounge chatter about quokkas" {
		t.Fatalf("heron after migration: %v %v", hm, err)
	}
	labels, public, err := srv.JoinKeyLabels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(labels, []string{"Gannet public label", "Walrus invite label", "First invite"}) || public != string(secrets[21]) {
		t.Fatalf("join keys after migration: %q, public %q", labels, public)
	}
	if bans, err := srv.BanReasons(ctx); err != nil || !slices.Equal(bans, []string{"Mallory Narwhal: posting ibex spam"}) {
		t.Fatalf("bans after migration: %q %v", bans, err)
	}
	audit, err := srv.AuditSummaries(ctx)
	if err != nil || !slices.Contains(audit, "created role Pelican Wranglers") {
		t.Fatalf("audit after migration: %q %v", audit, err)
	}
	alice, _ := hex.DecodeString(aliceHex)
	// v1 contact cards belong to the retired DM v1 scheme and are dropped;
	// clients republish v2 cards when they reconnect.
	if card, _, err := srv.ContactCard(ctx, alice); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("v1 contact card survived migration: %q %v", card, err)
	}
	if av, err := srv.Avatar(ctx, alice); err != nil || !bytes.Contains(av, []byte("avatar-lemur-pixels")) {
		t.Fatalf("avatar after migration: %v", err)
	}
	if months, _ := srv.MonthKeys(ctx); len(months) == 0 {
		t.Fatal("no month keys after migrating messages")
	}
	srv.Close()
	if v := schemaVersion(t, dir); v != community.LatestSchema() {
		t.Fatalf("schema after migration: %d", v)
	}
	assertNoPlaintext(t, secrets, dbFiles(dir)...)

	// Opening again is a no-op migration and reads the same data.
	srv = reopen(t, dir, "ws://127.0.0.1:1/ws", testutil.Quiet())
	if _, ok := srv.Snapshot().Members["Alice Marmot"]; !ok {
		t.Fatal("second open lost the roll")
	}
}

// Migrating with the wrong storage key fails cleanly and leaves the old
// database untouched.
func TestMigrationWithWrongStorageKeyLeavesDatabase(t *testing.T) {
	dir, _, _ := copyFixture(t)
	writeKey(t, filepath.Join(dir, "storage.key"))
	_, err := openDir(dir, "ws://127.0.0.1:1/ws", testutil.Quiet())
	if !errors.Is(err, community.ErrWrongStorageKey) {
		t.Fatalf("err = %v", err)
	}
	if v := schemaVersion(t, dir); v != 1 {
		t.Fatalf("schema after failed migration: %d", v)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "community.db"))
	if !bytes.Contains(raw, []byte("Alice Marmot")) {
		t.Fatal("failed migration damaged the database")
	}
}

func TestWrongStorageKeyRefusesToStart(t *testing.T) {
	e := setup(t, nil)
	e.comm.Stop()
	writeKey(t, filepath.Join(e.comm.Dir, "storage.key"))
	_, err := openDir(e.comm.Dir, e.relay.URL, testutil.Quiet())
	if !errors.Is(err, community.ErrWrongStorageKey) || !strings.Contains(err.Error(), "storage key does not match") {
		t.Fatalf("err = %v", err)
	}
	// A truncated key file is refused too, before touching the database.
	if err := os.WriteFile(filepath.Join(e.comm.Dir, "storage.key"), []byte("abcd\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openDir(e.comm.Dir, e.relay.URL, testutil.Quiet()); err == nil || !strings.Contains(err.Error(), "storage key") {
		t.Fatalf("short key: %v", err)
	}
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func execRaw(t *testing.T, dir, q string, args ...any) {
	t.Helper()
	db, err := store.Open(filepath.Join(dir, "community.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

// Rows can't be swapped and bytes can't be flipped without the server
// noticing: a damaged roll refuses to load (naming the row), and a damaged
// message is reported in the log and to readers, never silently dropped.
func TestTamperedRowsAreReported(t *testing.T) {
	e := setup(t, workloadSettings())
	w := runWorkload(t, e)
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

	// Swapping two role records (each valid on its own) is caught.
	execRaw(t, dir, `UPDATE roles SET data = (SELECT data FROM roles WHERE id = 2) WHERE id = 1`)
	_, err = openDir(dir, e.relay.URL, testutil.Quiet())
	var te *community.TamperError
	if !errors.As(err, &te) || te.Table != "roles" || te.Row != "1" {
		t.Fatalf("swapped role: %v", err)
	}

	// Moving a channel's record to another channel's row is caught.
	restore()
	execRaw(t, dir, `UPDATE channels SET data = (SELECT data FROM channels WHERE id = ?) WHERE id = 1`, w.Heron)
	if _, err := openDir(dir, e.relay.URL, testutil.Quiet()); !errors.As(err, &te) || te.Table != "channels" {
		t.Fatalf("moved channel: %v", err)
	}

	// A flipped byte in a message body: the server starts, logs the damaged
	// message by id, and reading it fails rather than skipping it.
	restore()
	execRaw(t, dir, `UPDATE messages SET body = substr(body, 1, length(body) - 1) || x'00' WHERE id = (SELECT min(id) FROM messages)`)
	var logs syncBuffer
	srv := reopen(t, dir, e.relay.URL, slog.New(slog.NewTextHandler(&logs, nil)))
	if !strings.Contains(logs.String(), "stored message unreadable") {
		t.Fatalf("damaged message not reported at startup; log:\n%s", logs.String())
	}
	gen := srv.Snapshot().Channels["general"]
	if _, err := srv.Messages(context.Background(), gen); !errors.As(err, &te) || te.Table != "messages" {
		t.Fatalf("reading a damaged message: %v", err)
	}
}

// ---------------------------------------------------------------- month keys

var (
	jan15 = time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	feb10 = time.Date(2026, 2, 10, 12, 0, 0, 0, time.UTC)
	day   = 24 * time.Hour
)

// janLast is the last millisecond of January 2026.
func janLast() time.Time { return time.UnixMilli(community.MonthEnd(202601) - 1) }

func TestMonthOf(t *testing.T) {
	cases := []struct {
		at   time.Time
		want int
	}{
		{time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), 202601},
		{time.Date(2026, 1, 31, 23, 59, 59, 999e6, time.UTC), 202601},
		{time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), 202602},
		{time.Date(2025, 12, 31, 23, 59, 59, 0, time.UTC), 202512},
		// Months are UTC months, whatever the server's zone.
		{time.Date(2026, 3, 1, 0, 30, 0, 0, time.FixedZone("x", 3600)), 202602},
	}
	for _, c := range cases {
		if got := community.MonthOf(c.at.UnixMilli()); got != c.want {
			t.Errorf("MonthOf(%v) = %d, want %d", c.at, got, c.want)
		}
	}
	if community.MonthEnd(202612) != time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli() {
		t.Error("December rolls over to January")
	}
}

type monthEnv struct {
	*env
	clock          time.Time
	gen            *wire.Channel
	janMsg         *wire.Message
	janBody        []byte
	febMsg         *wire.Message
	beforeRotation string
}

// monthSetup posts a message and a file in January and a message in
// February, and takes a backup with both month keys.
func monthSetup(t *testing.T, beforeClock func(e *env)) *monthEnv {
	t.Helper()
	e := setup(t, nil) // 30 days messages, 7 days files
	if beforeClock != nil {
		beforeClock(e) // admin actions need the real clock
	}
	ctx := testutil.Ctx(t)
	me := &monthEnv{env: e, gen: e.owner.Channel("general")}
	srv := e.comm.Server
	srv.SetClock(func() time.Time { return me.clock })
	me.clock = jan15
	var err error
	if me.janMsg, err = e.owner.Post(ctx, me.gen.Id, "january words"); err != nil {
		t.Fatal(err)
	}
	if _, err := upload(t, e.owner, me.gen.Id, "jan.txt", []byte("january file")); err != nil {
		t.Fatal(err)
	}
	me.clock = feb10
	if me.febMsg, err = e.owner.Post(ctx, me.gen.Id, "february words"); err != nil {
		t.Fatal(err)
	}
	if months, _ := srv.MonthKeys(ctx); !slices.Equal(months, []int{202601, 202602}) {
		t.Fatalf("month keys: %v", months)
	}
	var month int
	if month, me.janBody, err = srv.StoredBody(ctx, me.janMsg.Id); err != nil || month != 202601 {
		t.Fatalf("january body: month %d, %v", month, err)
	}
	backup, err := srv.Backup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Later backups under the same fake clock reuse the file name.
	me.beforeRotation = filepath.Join(t.TempDir(), "before.fbk")
	if b, err := os.ReadFile(backup); err != nil || os.WriteFile(me.beforeRotation, b, 0o600) != nil {
		t.Fatal("copying the backup failed", err)
	}
	return me
}

func (me *monthEnv) purge(t *testing.T, at time.Time) []int {
	t.Helper()
	ctx := testutil.Ctx(t)
	if _, _, err := me.comm.Server.Purge(ctx, at); err != nil {
		t.Fatal(err)
	}
	months, err := me.comm.Server.MonthKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return months
}

// A month's key is deleted exactly when its last millisecond is past the
// retention; after that its data can't be decrypted even from a copy of the
// ciphertext, while newer months stay readable.
func TestMonthKeyRotation(t *testing.T) {
	me := monthSetup(t, nil)
	ctx := testutil.Ctx(t)
	srv := me.comm.Server

	// One millisecond early: the January rows are purged by retention, but
	// January's key stays (a message from its last millisecond would still be
	// within retention).
	if got := me.purge(t, janLast().Add(30*day-time.Millisecond)); !slices.Equal(got, []int{202601, 202602}) {
		t.Fatalf("keys 1 ms early: %v", got)
	}
	if _, err := srv.OpenStoredBody(ctx, me.janMsg.Id, 202601, me.janBody); err != nil {
		t.Fatalf("january ciphertext unreadable before its key expired: %v", err)
	}
	// Exactly at the boundary: January's key is gone.
	if got := me.purge(t, janLast().Add(30*day)); !slices.Equal(got, []int{202602}) {
		t.Fatalf("keys at the boundary: %v", got)
	}
	if _, err := srv.OpenStoredBody(ctx, me.janMsg.Id, 202601, me.janBody); !errors.Is(err, community.ErrMonthKeyDeleted) {
		t.Fatalf("january ciphertext after its key was deleted: %v", err)
	}
	// February is still within retention and readable.
	msgs, err := srv.Messages(ctx, me.gen.Id)
	if err != nil || len(msgs) != 1 || msgs[0].Text != "february words" {
		t.Fatalf("history after rotation: %v %v", msgs, err)
	}
	// The month key survives a restart only for February.
	// The deletion is durable (reopened without running the retention job,
	// which would use the real clock).
	me.comm.Stop()
	again := reopen(t, me.comm.Dir, me.relay.URL, testutil.Quiet())
	if months, _ := again.MonthKeys(ctx); !slices.Equal(months, []int{202602}) {
		t.Fatalf("keys after reopening: %v", months)
	}
	assertNoPlaintext(t, [][]byte{[]byte("january words"), []byte("february words")}, dbFiles(me.comm.Dir)...)
}

// The longest retention in effect decides: a channel that keeps messages
// for 90 days holds January's key until 90 days after January ends, and a
// channel that keeps anything forever holds every key.
func TestMonthKeyRespectsLongestRetention(t *testing.T) {
	ninety := uint32(90)
	var archive int64
	me := monthSetup(t, func(e *env) {
		res, err := e.owner.Admin(testutil.Ctx(t), e.id(), &wire.AdminAction{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: "archive"}}})
		if err != nil {
			t.Fatal(err)
		}
		archive = res.Id
		if _, err := e.owner.Admin(testutil.Ctx(t), e.id(), &wire.AdminAction{Op: &wire.AdminAction_EditChannel{EditChannel: &wire.EditChannel{
			Id: archive, Position: 1, MessageRetentionDays: &ninety,
		}}}); err != nil {
			t.Fatal(err)
		}
	})
	ctx := testutil.Ctx(t)
	me.clock = jan15
	if _, err := me.owner.Post(ctx, archive, "kept for ninety days"); err != nil {
		t.Fatal(err)
	}
	if got := me.purge(t, janLast().Add(30*day)); !slices.Equal(got, []int{202601, 202602}) {
		t.Fatalf("january key deleted under a 90-day channel: %v", got)
	}
	if msgs, _ := me.comm.Server.Messages(ctx, archive); len(msgs) != 1 {
		t.Fatalf("archive purged early: %v", msgs)
	}
	if got := me.purge(t, janLast().Add(90*day-time.Millisecond)); !slices.Equal(got, []int{202601, 202602}) {
		t.Fatalf("keys 1 ms before 90 days: %v", got)
	}
	if got := me.purge(t, janLast().Add(90*day)); !slices.Equal(got, []int{202602}) {
		t.Fatalf("keys at 90 days: %v", got)
	}
	if msgs, _ := me.comm.Server.Messages(ctx, archive); len(msgs) != 0 {
		t.Fatalf("archive kept january after its key went: %v", msgs)
	}
}

func TestMonthKeysKeptWhenRetentionIsForever(t *testing.T) {
	zero := uint32(0)
	me := monthSetup(t, func(e *env) {
		gen := e.owner.Channel("general")
		if _, err := e.owner.Admin(testutil.Ctx(t), e.id(), &wire.AdminAction{Op: &wire.AdminAction_EditChannel{EditChannel: &wire.EditChannel{
			Id: gen.Id, Position: gen.Position, MessageRetentionDays: &zero,
		}}}); err != nil {
			t.Fatal(err)
		}
	})
	if got := me.purge(t, janLast().Add(1000*day)); !slices.Equal(got, []int{202601, 202602}) {
		t.Fatalf("keys deleted although messages are kept forever: %v", got)
	}
	// January's message is still there and readable; its file (7-day file
	// retention) is not.
	msgs, err := me.comm.Server.Messages(testutil.Ctx(t), me.gen.Id)
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, m := range msgs {
		texts = append(texts, m.Text)
		if firstFile(m) != nil && firstFile(m).Name != "(expired file)" {
			t.Errorf("file past its retention still served: %v", firstFile(m))
		}
	}
	if !slices.Equal(texts, []string{"january words", "a file", "february words"}) {
		t.Fatalf("history kept forever: %q", texts)
	}
}

// Backups carry the month keys they were taken with: a backup taken after
// rotation restores without the expired month, and one taken before
// rotation restores with it until the retention job runs on the new
// machine.
func TestBackupRestoreAcrossMonthKeyRotation(t *testing.T) {
	me := monthSetup(t, nil)
	ctx := testutil.Ctx(t)
	if got := me.purge(t, janLast().Add(30*day)); !slices.Equal(got, []int{202602}) {
		t.Fatalf("rotation: %v", got)
	}
	after, err := me.comm.Server.Backup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	restoreTo := func(backup string) *community.Server {
		t.Helper()
		dir := t.TempDir()
		cfg := community.DefaultConfig(dir)
		cfg.Relays = []string{me.relay.URL}
		if _, err := community.Restore(ctx, cfg, backup, filepath.Join(me.comm.Dir, "storage.key")); err != nil {
			t.Fatal(err)
		}
		return reopen(t, dir, me.relay.URL, testutil.Quiet())
	}

	srv := restoreTo(after)
	if months, _ := srv.MonthKeys(ctx); !slices.Equal(months, []int{202602}) {
		t.Fatalf("restored month keys: %v", months)
	}
	msgs, err := srv.Messages(ctx, me.gen.Id)
	if err != nil || len(msgs) != 1 || msgs[0].Text != "february words" {
		t.Fatalf("restored history: %v %v", msgs, err)
	}

	old := restoreTo(me.beforeRotation)
	if months, _ := old.MonthKeys(ctx); !slices.Equal(months, []int{202601, 202602}) {
		t.Fatalf("old backup month keys: %v", months)
	}
	if _, _, err := old.Purge(ctx, janLast().Add(30*day)); err != nil {
		t.Fatal(err)
	}
	if months, _ := old.MonthKeys(ctx); !slices.Equal(months, []int{202602}) {
		t.Fatalf("old backup after purge: %v", months)
	}
	if _, err := old.OpenStoredBody(ctx, me.janMsg.Id, 202601, me.janBody); !errors.Is(err, community.ErrMonthKeyDeleted) {
		t.Fatalf("january readable after purging the restored backup: %v", err)
	}
}

// ---------------------------------------------------------------- logs

// Server logs never contain names, channel or role names, labels, reasons,
// message text or join keys, across a representative set of admin actions
// and messages (docs/operations.md "Logs, health and metrics").
func TestServerLogsContainNoContent(t *testing.T) {
	var logs syncBuffer
	e := setupWithLog(t, workloadSettings(), slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	w := runWorkload(t, e)
	ctx := testutil.Ctx(t)
	admin := func(a *wire.AdminAction) *wire.AdminResult {
		t.Helper()
		res, err := e.owner.Admin(ctx, e.id(), a)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	snap := e.comm.Server.Snapshot()
	heron, role := snap.Channels["heron-lounge"], snap.Roles["Pelican Wranglers"]
	admin(&wire.AdminAction{Op: &wire.AdminAction_EditChannel{EditChannel: &wire.EditChannel{Id: heron, Name: "egret-parlour", Position: 1}}})
	admin(&wire.AdminAction{Op: &wire.AdminAction_EditRole{EditRole: &wire.EditRole{Id: role, Name: "Cormorant Crew", Permissions: community.PermViewChannel}}})
	raid := admin(&wire.AdminAction{Op: &wire.AdminAction_CreateJoinKey{CreateJoinKey: &wire.CreateJoinKey{Label: "Raid lynx label"}}}).JoinKey
	bob := e.member("Bob Wombat", raid.Secret)
	if _, err := bob.Post(ctx, e.owner.Channel("general").Id, "bob talks about a sloth"); err != nil {
		t.Fatal(err)
	}
	carol := e.member("Carol Kestrel", e.invite.Secret)
	admin(&wire.AdminAction{Op: &wire.AdminAction_InviteOwner{InviteOwner: &wire.InviteOwner{Member: w.Alice.Identity.Public}}})
	admin(&wire.AdminAction{Op: &wire.AdminAction_Kick{Kick: &wire.Kick{Member: carol.Identity.Public, Reason: "kicked for marmoset noise"}}})
	if n := admin(&wire.AdminAction{Op: &wire.AdminAction_RaidRevoke{RaidRevoke: &wire.RaidRevoke{JoinKeyId: raid.Id, Hours: 1}}}).Removed; n != 1 {
		t.Fatalf("raid revoke removed %d", n)
	}
	s := proto2Settings(e)
	s.Name = "Renamed Ocelot Club"
	admin(&wire.AdminAction{Op: &wire.AdminAction_UpdateSettings{UpdateSettings: s}})
	admin(&wire.AdminAction{Op: &wire.AdminAction_DeleteRole{DeleteRole: &wire.DeleteRole{Id: role}}})
	admin(&wire.AdminAction{Op: &wire.AdminAction_DeleteChannel{DeleteChannel: &wire.DeleteChannel{Id: heron}}})
	admin(&wire.AdminAction{Op: &wire.AdminAction_ServerBackup{ServerBackup: &wire.ServerBackup{}}})
	e.owner.Post(ctx, e.owner.Channel("general").Id, "a final heartfelt sloth message")
	e.comm.Stop()

	out := logs.String()
	if !strings.Contains(out, "admin action") || !strings.Contains(out, "action=raid_revoke") {
		t.Fatalf("expected admin actions to be logged by type; log:\n%s", out)
	}
	secrets := append([][]byte(nil), w.Strings...)
	for _, s := range []string{"egret-parlour", "Cormorant Crew", "Raid lynx label", "Bob Wombat", "Carol Kestrel",
		"marmoset", "Renamed Ocelot Club", "sloth", "First invite", raid.Secret, e.invite.Secret} {
		secrets = append(secrets, []byte(s))
	}
	for _, s := range secrets {
		if len(s) == 0 {
			continue
		}
		forms := []string{string(s), hex.EncodeToString(s), base64.RawURLEncoding.EncodeToString(s), base64.StdEncoding.EncodeToString(s)}
		for _, f := range forms {
			if strings.Contains(out, f) {
				t.Errorf("log contains %q", printable(s))
			}
		}
	}
}

func proto2Settings(e *env) *wire.CommunitySettings {
	if err := e.owner.Sync(testutil.Ctx(e.t)); err != nil {
		e.t.Fatal(err)
	}
	return cloneSettings(e.owner.State.Settings)
}
