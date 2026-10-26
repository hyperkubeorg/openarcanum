package community_test

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/prototext"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/store"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
)

// copyDir copies a fixture directory (database and keys).
func copyFixtureDir(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range []string{"community.db", "storage.key", "server.key", "noise.key"} {
		b, err := os.ReadFile(filepath.Join("testdata", name, f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// digest is everything a server holds, decrypted, in a stable text form:
// settings, the roll, roles with permissions, categories, channels with
// overrides, every message (text, author, edits, deletions, attachments),
// join keys, bans, the audit log and the month keys.
func digest(t *testing.T, srv *community.Server) string {
	t.Helper()
	ctx := context.Background()
	snap := srv.Snapshot()
	var b strings.Builder
	settings, _ := prototext.MarshalOptions{}.Marshal(snap.Settings)
	fmt.Fprintf(&b, "owner %x\nsettings %s\n", snap.Owner, settings)
	section := func(name string, m map[string]int64, val func(k string, v int64) string) {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "%s %q %s\n", name, k, val(k, m[k]))
		}
	}
	section("member", snap.Members, func(_ string, st int64) string { return fmt.Sprint(st) })
	section("role", snap.Roles, func(k string, _ int64) string { p, _ := srv.RolePerms(k); return fmt.Sprintf("%x", p) })
	fmt.Fprintf(&b, "invited %q\ncategories %q\n", snap.OwnerInvited, srv.CategoryNames())
	section("channel", snap.Channels, func(_ string, id int64) string {
		ov, synced, cat := srv.ChannelOverrides(id)
		msgs, err := srv.Messages(ctx, id)
		if err != nil {
			t.Fatalf("messages of %d: %v", id, err)
		}
		var lines []string
		for _, m := range msgs {
			var files []string
			for _, f := range m.Files {
				ref := srv.FileRef(ctx, f.FileId)
				if ref != nil {
					files = append(files, fmt.Sprintf("%s/%s/%d/%s", ref.Name, ref.Mime, ref.Size, hex.EncodeToString(ref.Key)))
				}
			}
			lines = append(lines, fmt.Sprintf("{%d %x %q del=%v edited=%d created=%d files=%v}", m.Id, m.Author, m.Text, m.Deleted, m.EditedAt, m.CreatedAt, files))
		}
		return fmt.Sprintf("id=%d overrides=%v synced=%v category=%d messages=%v", id, ov, synced, cat, lines)
	})
	labels, public, err := srv.JoinKeyLabels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bans, err := srv.BanReasons(ctx)
	if err != nil {
		t.Fatal(err)
	}
	audit, err := srv.AuditSummaries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	months, err := srv.MonthKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(&b, "joinkeys %q public %q\nbans %q\naudit %q\nmonths %v\n", labels, public, bans, audit, months)
	return b.String()
}

// Every schema step keeps the data, whatever version a database starts
// from: each fixture written by an older build (schema 1, and schema 3
// before and after the messaging workload) is brought to every
// intermediate version as that release would have left it, then opened by
// this build. The result is the same decrypted state as migrating the
// fixture in one go, and that state holds the fixture's data.
func TestMigrationFromEveryVersionKeepsData(t *testing.T) {
	ctx := context.Background()
	latest := community.LatestSchema()
	for _, fx := range []struct {
		name string
		from int
		must []string // text the migrated state must contain
	}{
		{"v1", 1, []string{"Zephyr Okapi Society", "Alice Marmot", "the secret capybara message", "tapir-plans.txt", "Walrus invite label", "posting ibex spam"}},
		{"v3", 3, []string{"Gull Moderators", "otter-voice", "heron-lounge"}},
		{"v3msg", 3, []string{"general"}},
	} {
		t.Run(fx.name, func(t *testing.T) {
			if v := schemaVersion(t, copyFixtureDir(t, fx.name)); v != fx.from {
				t.Fatalf("fixture %s is at schema %d, want %d", fx.name, v, fx.from)
			}
			ref := reopen(t, copyFixtureDir(t, fx.name), "ws://127.0.0.1:1/ws", testutil.Quiet())
			want := digest(t, ref)
			ref.Close()
			for _, s := range fx.must {
				if !strings.Contains(want, s) {
					t.Errorf("migrated %s lacks %q", fx.name, s)
				}
			}
			for k := fx.from; k < latest; k++ {
				dir := copyFixtureDir(t, fx.name)
				if err := community.MigrateTo(ctx, community.DefaultConfig(dir), k); err != nil {
					t.Fatalf("to schema %d: %v", k, err)
				}
				if v := schemaVersion(t, dir); v != k {
					t.Fatalf("at %d, want %d", v, k)
				}
				srv := reopen(t, dir, "ws://127.0.0.1:1/ws", testutil.Quiet())
				got := digest(t, srv)
				srv.Close()
				if got != want {
					t.Errorf("from schema %d the data differs:\n got: %s\nwant: %s", k, got, want)
				}
				if v := schemaVersion(t, dir); v != latest {
					t.Errorf("from schema %d: ended at %d", k, v)
				}
			}
		})
	}
}

// A database from a newer release is refused with an error naming the
// file and saying what to do, and is left as it was.
func TestNewerSchemaRefused(t *testing.T) {
	dir, _, _ := copyFixture(t)
	srv := reopen(t, dir, "ws://127.0.0.1:1/ws", testutil.Quiet())
	srv.Close()
	newer := community.LatestSchema() + 1
	execRaw(t, dir, fmt.Sprintf(`PRAGMA user_version = %d`, newer))
	_, err := openDir(dir, "ws://127.0.0.1:1/ws", testutil.Quiet())
	if err == nil || !strings.Contains(err.Error(), "newer than this build supports") || !strings.Contains(err.Error(), "community.db") {
		t.Fatalf("err = %v", err)
	}
	db, err := store.Open(filepath.Join(dir, "community.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if v, _ := store.Version(context.Background(), db); v != newer {
		t.Fatalf("version changed to %d", v)
	}
}
