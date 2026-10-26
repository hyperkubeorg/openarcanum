package community_test

import (
	"context"
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
)

// Schema 11 rebuilds the roles, channels, categories, emoji and join key
// tables with AUTOINCREMENT ids: every row keeps its id, and each table's
// sequence starts past its largest id.
func TestMigrationV11KeepsIdsAndSequences(t *testing.T) {
	dir, _, _ := copyFixture(t)
	const prev = 10 // the version before schema_v11.go
	if err := community.MigrateTo(context.Background(), community.DefaultConfig(dir), prev); err != nil {
		t.Fatal(err)
	}
	before := reopen(t, dir, "ws://127.0.0.1:1/ws", testutil.Quiet())
	wantRoles, wantChannels := before.Snapshot().Roles, before.Snapshot().Channels
	before.Close()
	if v := schemaVersion(t, dir); v != community.LatestSchema() {
		t.Fatalf("schema %d after migration", v)
	}
	srv := reopen(t, dir, "ws://127.0.0.1:1/ws", testutil.Quiet())
	defer srv.Close()
	snap := srv.Snapshot()
	for name, id := range wantRoles {
		if snap.Roles[name] != id {
			t.Fatalf("role %q: id %d, was %d", name, snap.Roles[name], id)
		}
	}
	for name, id := range wantChannels {
		if snap.Channels[name] != id {
			t.Fatalf("channel %q: id %d, was %d", name, snap.Channels[name], id)
		}
	}
	for _, table := range []string{"roles", "channels", "join_keys"} {
		var seq, maxID int64
		if err := srv.DB().QueryRow(`SELECT seq FROM sqlite_sequence WHERE name = ?`, table).Scan(&seq); err != nil {
			t.Fatalf("%s has no sequence: %v", table, err)
		}
		if err := srv.DB().QueryRow(`SELECT max(id) FROM ` + table).Scan(&maxID); err != nil {
			t.Fatal(err)
		}
		if seq != maxID || maxID == 0 {
			t.Fatalf("%s: sequence %d, largest id %d", table, seq, maxID)
		}
	}
}
