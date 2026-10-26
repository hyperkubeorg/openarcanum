package community_test

import (
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Manage Community doesn't let a non-owner move the community onto other
// relays (which would show them every member's IP address); other settings
// stay theirs to change, and owners change relays as before.
func TestRelaySettingsOwnerOnly(t *testing.T) {
	e := setup(t, nil)
	mod := e.member("Mod", e.invite.Secret)
	e.role("Managers", community.PermManageCommunity, mod)
	update := func(m func(*wire.CommunitySettings)) *wire.AdminAction {
		s := cloneSettings(fresh(t, e.owner).Settings)
		m(s)
		return &wire.AdminAction{Op: &wire.AdminAction_UpdateSettings{UpdateSettings: s}}
	}
	pin := func(s *wire.CommunitySettings) {
		s.RelayMode = wire.RelayMode_RELAY_MODE_PINNED
		s.PinnedRelays = []string{"ws://relay.attacker.example/ws"}
	}
	_, err := e.adminAs(mod, update(pin))
	wantErr(t, err, "only an owner can change the community's relays")
	if got := fresh(t, e.owner).Settings; got.RelayMode == wire.RelayMode_RELAY_MODE_PINNED {
		t.Fatalf("relays changed: %v", got.PinnedRelays)
	}
	e.mustAdmin(mod, update(func(s *wire.CommunitySettings) { s.Description = "still mine to edit" }))
	e.mustAdmin(e.owner, update(func(s *wire.CommunitySettings) {
		s.RelayMode = wire.RelayMode_RELAY_MODE_PINNED
		s.PinnedRelays = []string{e.relay.URL}
	}))
	e.auditHas(t, "relays")
}

// On-demand backups are for owners, and at most one per ten minutes: each
// prunes the oldest beyond backup_keep, so a moderator with Manage
// Community could otherwise rotate out every good backup in a minute.
func TestServerBackupOwnerOnlyAndLimited(t *testing.T) {
	e := setup(t, nil)
	mod := e.member("Mod", e.invite.Secret)
	e.role("Managers", community.PermManageCommunity, mod)
	backup := func() *wire.AdminAction {
		return &wire.AdminAction{Op: &wire.AdminAction_ServerBackup{ServerBackup: &wire.ServerBackup{}}}
	}
	_, err := e.adminAs(mod, backup())
	wantErr(t, err, "only an owner")
	if r := e.mustAdmin(e.owner, backup()); r.BackupPath == "" {
		t.Fatal("no backup path")
	}
	_, err = e.adminAs(e.owner, backup())
	wantErr(t, err, "try again later")
}
