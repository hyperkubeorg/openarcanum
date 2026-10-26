package community_test

import (
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

func createCategory(name string) *wire.AdminAction {
	return &wire.AdminAction{Op: &wire.AdminAction_CreateCategory{CreateCategory: &wire.CreateCategory{Name: name}}}
}

func createChannelIn(name string, category int64) *wire.AdminAction {
	return &wire.AdminAction{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: name, CategoryId: category}}}
}

// everyoneDenied reports whether channel id still has its @everyone View
// Channel deny.
func (e *env) everyoneDenied(t *testing.T, id int64) bool {
	t.Helper()
	ovs, _, _ := e.comm.Server.ChannelOverrides(id)
	for _, o := range ovs {
		if o.Member == nil && o.RoleID == e.everyoneID() && o.Deny&community.PermViewChannel != 0 {
			return true
		}
	}
	return false
}

// Manage Channels alone can't change a channel's overrides through syncing
// or moving a synced channel: those go through SetChannelOverride's checks.
func TestSyncAndMoveNeedOverrideRights(t *testing.T) {
	e := setup(t, nil)
	mod := e.member("Mod", e.invite.Secret)
	manager := e.must(createRole("Channel manager", community.PermManageChannels)).Id
	e.must(assign(mod, manager))
	ev := e.everyoneID()
	private := e.must(createCategory("Private")).Id
	lobby := e.must(createCategory("Lobby")).Id
	e.must(override(&wire.SetChannelOverride{CategoryId: private, RoleId: ev, Deny: community.PermViewChannel}))
	// The mod may see the private channels (so Manage Channels reaches them).
	e.must(override(&wire.SetChannelOverride{CategoryId: private, Member: mod.Identity.Public, Allow: community.PermViewChannel}))
	staff := e.must(createChannelIn("staff", private)).Id
	if !e.everyoneDenied(t, staff) {
		t.Fatal("setup: #staff isn't private")
	}

	// Moving the synced #staff into the empty Lobby would drop the deny.
	_, err := e.act(mod, editChannel(&wire.EditChannel{Id: staff, Name: "staff", CategoryId: ptr(lobby)}))
	permDenied(t, "moving a synced channel without Manage Roles", err)
	// Unsynced in Lobby, then syncing with Lobby would drop the deny.
	e.must(editChannel(&wire.EditChannel{Id: staff, Name: "staff", CategoryId: ptr(int64(0))}))
	e.must(editChannel(&wire.EditChannel{Id: staff, Name: "staff", CategoryId: ptr(lobby)}))
	if _, synced, cat := e.comm.Server.ChannelOverrides(staff); synced || cat != lobby || !e.everyoneDenied(t, staff) {
		t.Fatalf("setup: #staff synced=%v cat=%d", synced, cat)
	}
	_, err = e.act(mod, editChannel(&wire.EditChannel{Id: staff, Name: "staff", SyncPermissions: true}))
	permDenied(t, "syncing without Manage Roles", err)
	if !e.everyoneDenied(t, staff) {
		t.Fatal("#staff lost its @everyone deny")
	}
	// Edits that don't touch overrides still work for the mod.
	if _, err := e.act(mod, editChannel(&wire.EditChannel{Id: staff, Name: "staff-room"})); err != nil {
		t.Fatalf("rename: %v", err)
	}
	// With Manage Roles too, syncing may drop the @everyone deny, but not
	// the mod's own member override (only members below them).
	e.must(assign(mod, manager, e.must(createRole("Role manager", community.PermManageRoles)).Id))
	_, err = e.act(mod, editChannel(&wire.EditChannel{Id: staff, Name: "staff-room", SyncPermissions: true}))
	permDenied(t, "syncing away an override of a member not below you", err)
	e.must(override(&wire.SetChannelOverride{CategoryId: lobby, Member: mod.Identity.Public, Allow: community.PermViewChannel}))
	if _, err := e.act(mod, editChannel(&wire.EditChannel{Id: staff, Name: "staff-room", SyncPermissions: true})); err != nil {
		t.Fatalf("sync with Manage Roles: %v", err)
	}
	if e.everyoneDenied(t, staff) {
		t.Fatal("sync didn't apply")
	}
}

// Manage Channels doesn't reach channels the member can't see: no edits,
// no retention, no deletion.
func TestManageChannelsNeedsViewChannel(t *testing.T) {
	e := setup(t, nil)
	gen := e.owner.Channel("general").Id
	mod := e.member("Mod", e.invite.Secret)
	e.must(assign(mod, e.must(createRole("Channel manager", community.PermManageChannels)).Id))
	hidden := e.must(&wire.AdminAction{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: "hidden"}}}).Id
	e.must(override(&wire.SetChannelOverride{ChannelId: hidden, RoleId: e.everyoneID(), Deny: community.PermViewChannel}))
	days := uint32(1)
	for what, a := range map[string]*wire.AdminAction{
		"edit":      editChannel(&wire.EditChannel{Id: hidden, Name: "renamed", Position: 1}),
		"retention": editChannel(&wire.EditChannel{Id: hidden, Name: "hidden", Position: 1, MessageRetentionDays: &days}),
		"delete":    {Op: &wire.AdminAction_DeleteChannel{DeleteChannel: &wire.DeleteChannel{Id: hidden}}},
	} {
		_, err := e.act(mod, a)
		permDenied(t, what+" a hidden channel", err)
	}
	if _, _, cat := e.comm.Server.ChannelOverrides(hidden); cat != 0 || !e.everyoneDenied(t, hidden) {
		t.Fatal("hidden channel changed")
	}
	// The same edit on a channel they see works.
	if _, err := e.act(mod, editChannel(&wire.EditChannel{Id: gen, Name: "general", MessageRetentionDays: &days})); err != nil {
		t.Fatalf("retention on a visible channel: %v", err)
	}
}

// SetChannelOverride uses the actor's permissions in that channel: someone
// denied a channel can't let @everyone in, and someone denied a permission
// in a channel can't grant it there.
func TestOverrideUsesChannelPermissions(t *testing.T) {
	e := setup(t, nil)
	gen := e.owner.Channel("general").Id
	mod := e.member("Mod", e.invite.Secret)
	e.must(assign(mod, e.must(createRole("Manager", community.PermManageChannels|community.PermManageRoles|community.PermManageMessages)).Id))
	ev := e.everyoneID()
	hidden := e.must(&wire.AdminAction{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: "hidden"}}}).Id
	e.must(override(&wire.SetChannelOverride{ChannelId: hidden, RoleId: ev, Deny: community.PermViewChannel}))
	_, err := e.act(mod, override(&wire.SetChannelOverride{ChannelId: hidden, RoleId: ev}))
	permDenied(t, "opening a channel the actor is denied", err)
	_, err = e.act(mod, override(&wire.SetChannelOverride{ChannelId: hidden, Member: mod.Identity.Public, Allow: community.PermViewChannel}))
	permDenied(t, "letting yourself into a hidden channel", err)
	if !e.everyoneDenied(t, hidden) {
		t.Fatal("hidden channel opened")
	}
	// Category: the same, through the category's overrides.
	cat := e.must(createCategory("Private")).Id
	e.must(override(&wire.SetChannelOverride{CategoryId: cat, RoleId: ev, Deny: community.PermViewChannel}))
	_, err = e.act(mod, override(&wire.SetChannelOverride{CategoryId: cat, RoleId: ev}))
	permDenied(t, "opening a category the actor is denied", err)

	// Denied Manage Messages in #general: can't grant it there, can elsewhere.
	other := e.must(&wire.AdminAction{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: "other"}}}).Id
	e.must(override(&wire.SetChannelOverride{ChannelId: gen, Member: mod.Identity.Public, Deny: community.PermManageMessages}))
	_, err = e.act(mod, override(&wire.SetChannelOverride{ChannelId: gen, RoleId: ev, Allow: community.PermManageMessages}))
	permDenied(t, "granting a permission denied to you in the channel", err)
	if _, err := e.act(mod, override(&wire.SetChannelOverride{ChannelId: other, RoleId: ev, Allow: community.PermManageMessages})); err != nil {
		t.Fatalf("granting a held permission: %v", err)
	}
}
