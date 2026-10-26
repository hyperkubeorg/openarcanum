package community_test

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// copyV3Fixture copies the schema 3 database written by the build before
// schema 4 (testdata/v3/fixture.json says how).
func copyV3Fixture(t *testing.T) (string, [][]byte, int64) {
	t.Helper()
	dir := t.TempDir()
	for _, f := range []string{"community.db", "storage.key", "server.key", "noise.key"} {
		b, err := os.ReadFile(filepath.Join("testdata", "v3", f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var fx struct {
		PlaintextHex []string `json:"plaintext_hex"`
		Heron        int64    `json:"heron"`
	}
	b, err := os.ReadFile(filepath.Join("testdata", "v3", "fixture.json"))
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
	return dir, secrets, fx.Heron
}

// Schema 4 on a database the schema 3 build wrote: every role's
// permissions and every override move to the new layout, the categories
// table appears, settings get the toggle defaults, nothing is left in
// plaintext, and the new features work on the migrated data.
func TestMigrationV4FromV3Database(t *testing.T) {
	dir, secrets, heron := copyV3Fixture(t)
	if v := schemaVersion(t, dir); v != 3 {
		t.Fatalf("fixture is at schema %d, want 3", v)
	}
	srv := reopen(t, dir, "ws://127.0.0.1:1/ws", testutil.Quiet())
	perms := func(name string) uint64 {
		p, ok := srv.RolePerms(name)
		if !ok {
			t.Fatalf("role %q lost", name)
		}
		return p
	}
	if got := perms("@everyone"); got != community.DefaultEveryonePerms {
		t.Errorf("@everyone: %x, want the documented defaults %x", got, community.DefaultEveryonePerms)
	}
	if got := perms("Admin"); got != community.PermAdministrator {
		t.Errorf("Admin: %x", got)
	}
	if got := perms("Pelican Wranglers"); got != community.PermViewChannel|community.PermReadMessageHistory {
		t.Errorf("Pelican Wranglers: %x", got)
	}
	wantMod := community.PermManageMessages | community.PermCreateInvite | community.PermKick | community.PermBan |
		community.PermApproveMembers | community.PermViewAuditLog | community.PermConnectVoice | community.PermSpeak |
		community.PermUseVoiceActivity | community.PermVideo
	if got := perms("Gull Moderators"); got != wantMod {
		t.Errorf("Gull Moderators: %x, want %x", got, wantMod)
	}
	ovs, synced, cat := srv.ChannelOverrides(heron)
	if synced || cat != 0 || len(ovs) != 2 {
		t.Fatalf("heron overrides: %v synced=%v cat=%d", ovs, synced, cat)
	}
	for _, o := range ovs {
		switch {
		case o.RoleID == srv.Snapshot().Roles["Pelican Wranglers"]:
			if o.Allow != community.PermSendMessages || o.Deny != 0 {
				t.Errorf("pelican override: %+v", o)
			}
		case o.RoleID == srv.Snapshot().Roles["@everyone"]:
			if o.Deny != community.PermAttachFiles || o.Allow != 0 {
				t.Errorf("@everyone override: %+v", o)
			}
		default:
			t.Errorf("unexpected override %+v", o)
		}
	}
	set := srv.Snapshot().Settings
	if !set.GetVoiceEnabled() || set.GetLinkPreviewsEnabled() || !set.GetNewMembersSeeHistory() {
		t.Errorf("toggle defaults after migration: %v", set)
	}
	if _, ok := srv.Snapshot().Channels["otter-voice"]; !ok {
		t.Error("voice channel lost")
	}
	srv.Close()
	if v := schemaVersion(t, dir); v != community.LatestSchema() {
		t.Fatalf("schema after migration: %d", v)
	}
	assertNoPlaintext(t, secrets, dbFiles(dir)...)

	// Opening again doesn't migrate twice (bits would move again).
	srv = reopen(t, dir, "ws://127.0.0.1:1/ws", testutil.Quiet())
	if p, _ := srv.RolePerms("Gull Moderators"); p != wantMod {
		t.Fatalf("second open changed permissions: %x", p)
	}
	srv.Close()
}

// The categories table is sealed like the others: a category record copied
// into another row fails to open, and names never appear in the file.
func TestCategoriesAtRest(t *testing.T) {
	e := setup(t, nil)
	a := e.must(&wire.AdminAction{Op: &wire.AdminAction_CreateCategory{CreateCategory: &wire.CreateCategory{Name: "Albatross Lounge"}}}).Id
	b := e.must(&wire.AdminAction{Op: &wire.AdminAction_CreateCategory{CreateCategory: &wire.CreateCategory{Name: "Booby Corner"}}}).Id
	e.comm.Stop()
	assertNoPlaintext(t, [][]byte{[]byte("Albatross Lounge"), []byte("Booby Corner")}, dbFiles(e.comm.Dir)...)
	execRaw(t, e.comm.Dir, `UPDATE categories SET data = (SELECT data FROM categories WHERE id = ?) WHERE id = ?`, a, b)
	if _, err := openDir(e.comm.Dir, e.relay.URL, testutil.Quiet()); err == nil {
		t.Fatal("a category record moved to another row was accepted")
	}
}
