package community_test

import (
	"context"
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
)

// The moderation migration (schema 6) runs on a database the previous
// schema version left (here: the v1 fixture brought to version 5), and the
// later ones after it. Existing communities get the documented defaults of
// the new settings, @everyone has Use Custom Emoji like new communities
// (schema 4 granted it), and the new tables work.
func TestMigrationAddsModeration(t *testing.T) {
	dir, _, _ := copyFixture(t)
	const prev = 5 // the version before schema_v6.go
	if err := community.MigrateTo(context.Background(), community.DefaultConfig(dir), prev); err != nil {
		t.Fatal(err)
	}
	if v := schemaVersion(t, dir); v != prev {
		t.Fatalf("fixture at %d, want %d", v, prev)
	}
	srv := reopen(t, dir, "ws://127.0.0.1:1/ws", testutil.Quiet())
	snap := srv.Snapshot()
	if !snap.Settings.GetCustomEmojiEnabled() || snap.Settings.MaxCustomEmoji != community.DefaultMaxCustomEmoji || snap.Settings.ScreeningMinutes != 0 {
		t.Fatalf("settings after migration: emoji %v, limit %d, screening %d", snap.Settings.GetCustomEmojiEnabled(), snap.Settings.MaxCustomEmoji, snap.Settings.ScreeningMinutes)
	}
	if snap.Settings.Name != "Zephyr Okapi Society" {
		t.Fatalf("other settings lost: %q", snap.Settings.Name)
	}
	if p, _ := srv.RolePerms("@everyone"); p&community.PermUseCustomEmoji == 0 {
		t.Fatal("@everyone lacks Use Custom Emoji after migration")
	}
	if p, _ := srv.RolePerms("Pelican Wranglers"); p&community.PermUseCustomEmoji != 0 {
		t.Fatal("the migration changed a non-default role")
	}
	srv.Close()
	if v := schemaVersion(t, dir); v != community.LatestSchema() {
		t.Fatalf("schema %d after migration", v)
	}
	execRaw(t, dir, `INSERT INTO automod_rules (id, data) VALUES (1, x'00')`)
	execRaw(t, dir, `DELETE FROM automod_rules`)
	execRaw(t, dir, `INSERT INTO reports (id, status, month, data) VALUES (1, 1, 202601, x'00')`)
	execRaw(t, dir, `DELETE FROM reports`)
	execRaw(t, dir, `INSERT INTO emoji (id, data, image) VALUES (1, x'00', x'00')`)
	execRaw(t, dir, `DELETE FROM emoji`)
	// Opening again doesn't migrate again.
	srv = reopen(t, dir, "ws://127.0.0.1:1/ws", testutil.Quiet())
	if !srv.Snapshot().Settings.GetCustomEmojiEnabled() {
		t.Fatal("settings changed on reopen")
	}
}
