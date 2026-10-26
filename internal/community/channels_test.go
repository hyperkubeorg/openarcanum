package community_test

import (
	"bytes"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// act runs an admin action as m and returns its result or error.
func (e *env) act(m *testutil.Member, a *wire.AdminAction) (*wire.AdminResult, error) {
	e.t.Helper()
	return m.Admin(testutil.Ctx(e.t), e.id(), a)
}

// must runs an admin action as the owner and fails the test on error.
func (e *env) must(a *wire.AdminAction) *wire.AdminResult {
	e.t.Helper()
	r, err := e.act(e.owner, a)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

func wantCode(t *testing.T, what string, err error, code wire.RpcErrorCode) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), code.String()) {
		t.Fatalf("%s: got %v, want %s", what, err, code)
	}
}

func permDenied(t *testing.T, what string, err error) {
	t.Helper()
	wantCode(t, what, err, wire.RpcErrorCode_RPC_ERROR_CODE_PERMISSION_DENIED)
}

func disabled(t *testing.T, what string, err error) {
	t.Helper()
	wantCode(t, what, err, wire.RpcErrorCode_RPC_ERROR_CODE_DISABLED)
}

func createRole(name string, perms uint64) *wire.AdminAction {
	return &wire.AdminAction{Op: &wire.AdminAction_CreateRole{CreateRole: &wire.CreateRole{Name: name, Permissions: perms}}}
}

func assign(m *testutil.Member, ids ...int64) *wire.AdminAction {
	return &wire.AdminAction{Op: &wire.AdminAction_AssignRoles{AssignRoles: &wire.AssignRoles{Member: m.Identity.Public, RoleIds: ids}}}
}

func override(o *wire.SetChannelOverride) *wire.AdminAction {
	return &wire.AdminAction{Op: &wire.AdminAction_SetChannelOverride{SetChannelOverride: o}}
}

func editChannel(e *wire.EditChannel) *wire.AdminAction {
	return &wire.AdminAction{Op: &wire.AdminAction_EditChannel{EditChannel: e}}
}

func syncState(t *testing.T, m *testutil.Member) *wire.SyncState {
	t.Helper()
	if err := m.Sync(testutil.Ctx(t)); err != nil {
		t.Fatal(err)
	}
	return m.State
}

func channelIDs(st *wire.SyncState) []string {
	var out []string
	for _, c := range st.Channels {
		out = append(out, c.Name)
	}
	return out
}

func (e *env) everyoneID() int64 {
	e.t.Helper()
	for _, r := range syncState(e.t, e.owner).Roles {
		if r.Builtin && r.Position == 0 {
			return r.Id
		}
	}
	e.t.Fatal("no @everyone")
	return 0
}

// Categories group channels; channels and categories have an order that
// reorder actions change, and deleting a category keeps its channels.
func TestCategoriesAndOrdering(t *testing.T) {
	e := setup(t, nil)
	alice := e.member("Alice", e.invite.Secret)
	ch := func(name string, cat int64) int64 {
		return e.must(&wire.AdminAction{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: name, CategoryId: cat}}}).Id
	}
	cat := func(name string) int64 {
		return e.must(&wire.AdminAction{Op: &wire.AdminAction_CreateCategory{CreateCategory: &wire.CreateCategory{Name: name}}}).Id
	}
	info := cat("Info")
	games := cat("Games")
	rules := ch("rules", info)
	news := ch("news", info)
	ch("chess", games)
	loose := ch("loose", 0)
	gen := e.owner.Channel("general").Id

	st := syncState(t, alice)
	if got := channelIDs(st); !slices.Equal(got, []string{"general", "loose", "rules", "news", "chess"}) {
		t.Fatalf("order: %v", got)
	}
	if len(st.Categories) != 2 || st.Categories[0].Name != "Info" || st.Categories[1].Name != "Games" {
		t.Fatalf("categories: %v", st.Categories)
	}
	// Move a channel into a category: it goes to the end of that category.
	e.must(editChannel(&wire.EditChannel{Id: loose, Name: "loose", Position: 0, CategoryId: ptr(games)}))
	e.must(&wire.AdminAction{Op: &wire.AdminAction_ReorderChannels{ReorderChannels: &wire.ReorderChannels{CategoryId: info, ChannelIds: []int64{news, rules}}}})
	e.must(&wire.AdminAction{Op: &wire.AdminAction_ReorderCategories{ReorderCategories: &wire.ReorderCategories{CategoryIds: []int64{games, info}}}})
	if got := channelIDs(syncState(t, alice)); !slices.Equal(got, []string{"general", "chess", "loose", "news", "rules"}) {
		t.Fatalf("order after moves: %v", got)
	}
	// Reorders must list exactly the category's channels.
	for _, bad := range [][]int64{{news}, {news, rules, gen}, {news, news}} {
		if _, err := e.act(e.owner, &wire.AdminAction{Op: &wire.AdminAction_ReorderChannels{ReorderChannels: &wire.ReorderChannels{CategoryId: info, ChannelIds: bad}}}); err == nil {
			t.Errorf("reorder with %v accepted", bad)
		}
	}
	if _, err := e.act(e.owner, &wire.AdminAction{Op: &wire.AdminAction_ReorderCategories{ReorderCategories: &wire.ReorderCategories{CategoryIds: []int64{info}}}}); err == nil {
		t.Error("partial category reorder accepted")
	}
	// Members without Manage Channels can't change any of it.
	permDenied(t, "member creates a category", func() error {
		_, err := e.act(alice, &wire.AdminAction{Op: &wire.AdminAction_CreateCategory{CreateCategory: &wire.CreateCategory{Name: "Mine"}}})
		return err
	}())
	permDenied(t, "member reorders", func() error {
		_, err := e.act(alice, &wire.AdminAction{Op: &wire.AdminAction_ReorderCategories{ReorderCategories: &wire.ReorderCategories{CategoryIds: []int64{info, games}}}})
		return err
	}())
	// Rename, then delete: the channels move to the uncategorized list.
	e.must(&wire.AdminAction{Op: &wire.AdminAction_EditCategory{EditCategory: &wire.EditCategory{Id: info, Name: "Read me"}}})
	e.must(&wire.AdminAction{Op: &wire.AdminAction_DeleteCategory{DeleteCategory: &wire.DeleteCategory{Id: info}}})
	st = syncState(t, alice)
	if got := channelIDs(st); !slices.Equal(got, []string{"general", "news", "rules", "chess", "loose"}) {
		t.Fatalf("order after deleting a category: %v", got)
	}
	if len(st.Categories) != 1 || st.Categories[0].Id != games {
		t.Fatalf("categories after delete: %v", st.Categories)
	}
	if _, err := e.act(e.owner, &wire.AdminAction{Op: &wire.AdminAction_CreateCategory{CreateCategory: &wire.CreateCategory{Name: "  "}}}); err == nil {
		t.Error("empty category name accepted")
	}
	audit, _ := e.comm.Server.AuditSummaries(testutil.Ctx(t))
	for _, want := range []string{"created category Info", "renamed category Info to Read me", "deleted category Read me", "reordered categories", "reordered channels in Info"} {
		if !slices.Contains(audit, want) {
			t.Errorf("audit lacks %q: %q", want, audit)
		}
	}
}

func ptr[T any](v T) *T { return &v }

// A category's overrides are copied to the channels synced with it; editing
// a channel's own overrides unsyncs it; syncing copies them again.
func TestCategorySync(t *testing.T) {
	e := setup(t, nil)
	alice := e.member("Alice", e.invite.Secret)
	ev := e.everyoneID()
	staff := e.must(createRole("Staff", 0)).Id
	e.must(assign(alice, staff))
	cat := e.must(&wire.AdminAction{Op: &wire.AdminAction_CreateCategory{CreateCategory: &wire.CreateCategory{Name: "Staff only"}}}).Id
	a := e.must(&wire.AdminAction{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: "a", CategoryId: cat}}}).Id
	b := e.must(&wire.AdminAction{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: "b", CategoryId: cat}}}).Id
	bob := e.member("Bob", e.invite.Secret)
	if got := channelIDs(syncState(t, bob)); !slices.Contains(got, "a") {
		t.Fatalf("bob before the category override: %v", got)
	}
	keyBefore := e.channelKey(a)

	// Category: @everyone can't view, Staff can. Both synced channels follow.
	e.must(override(&wire.SetChannelOverride{CategoryId: cat, RoleId: ev, Deny: community.PermViewChannel}))
	e.must(override(&wire.SetChannelOverride{CategoryId: cat, RoleId: staff, Allow: community.PermViewChannel}))
	if got := channelIDs(syncState(t, bob)); slices.Contains(got, "a") || slices.Contains(got, "b") {
		t.Fatalf("bob still sees the staff channels: %v", got)
	}
	if got := channelIDs(syncState(t, alice)); !slices.Contains(got, "a") || !slices.Contains(got, "b") {
		t.Fatalf("staff can't see the staff channels: %v", got)
	}
	if bytes.Equal(keyBefore, e.channelKey(a)) {
		t.Fatal("channel key not rotated when bob lost access through the category")
	}
	// Bob sees no empty category either.
	if st := syncState(t, bob); len(st.Categories) != 0 {
		t.Fatalf("bob sees categories: %v", st.Categories)
	}
	// Editing b's own overrides unsyncs it: a category change no longer
	// reaches it.
	e.must(override(&wire.SetChannelOverride{ChannelId: b, RoleId: ev, Deny: community.PermViewChannel | community.PermSendMessages}))
	if _, synced, _ := e.comm.Server.ChannelOverrides(b); synced {
		t.Fatal("b still synced after editing its overrides")
	}
	e.must(override(&wire.SetChannelOverride{CategoryId: cat, RoleId: ev, Deny: 0}))
	if got := channelIDs(syncState(t, bob)); !slices.Contains(got, "a") || slices.Contains(got, "b") {
		t.Fatalf("after opening the category: %v", got)
	}
	// Syncing b copies the category's overrides again.
	e.must(editChannel(&wire.EditChannel{Id: b, Name: "b", Position: 1, SyncPermissions: true}))
	if ovs, synced, _ := e.comm.Server.ChannelOverrides(b); !synced || len(ovs) != 1 || ovs[0].RoleID != staff {
		t.Fatalf("b after sync: %v synced=%v", ovs, synced)
	}
	if got := channelIDs(syncState(t, bob)); !slices.Contains(got, "b") {
		t.Fatalf("bob after syncing b: %v", got)
	}
	// Moving a synced channel to another category takes its overrides;
	// moving it out keeps them but unsyncs.
	other := e.must(&wire.AdminAction{Op: &wire.AdminAction_CreateCategory{CreateCategory: &wire.CreateCategory{Name: "Open"}}}).Id
	e.must(editChannel(&wire.EditChannel{Id: a, Name: "a", CategoryId: ptr(other)}))
	if ovs, synced, catID := e.comm.Server.ChannelOverrides(a); !synced || len(ovs) != 0 || catID != other {
		t.Fatalf("a after the move: %v synced=%v cat=%d", ovs, synced, catID)
	}
	e.must(editChannel(&wire.EditChannel{Id: a, Name: "a", CategoryId: ptr(int64(0))}))
	if _, synced, catID := e.comm.Server.ChannelOverrides(a); synced || catID != 0 {
		t.Fatal("a still synced outside a category")
	}
	if _, err := e.act(e.owner, editChannel(&wire.EditChannel{Id: a, Name: "a", SyncPermissions: true})); err == nil {
		t.Fatal("synced a channel that has no category")
	}
}

// Per-member overrides: a member deny beats a role allow, and a member allow
// opens a private channel to one person.
func TestMemberOverrides(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	bob := e.member("Bob", e.invite.Secret)
	ev := e.everyoneID()
	gen := e.owner.Channel("general").Id
	e.must(override(&wire.SetChannelOverride{ChannelId: gen, Member: alice.Identity.Public, Deny: community.PermSendMessages}))
	if _, err := alice.Post(ctx, gen, "hi"); err == nil {
		t.Fatal("alice posted despite her member deny")
	}
	if _, err := bob.Post(ctx, gen, "hi"); err != nil {
		t.Fatalf("bob: %v", err)
	}
	// Removing the override (allow = deny = 0) restores her.
	e.must(override(&wire.SetChannelOverride{ChannelId: gen, Member: alice.Identity.Public}))
	if _, err := alice.Post(ctx, gen, "back"); err != nil {
		t.Fatalf("alice after removing the override: %v", err)
	}
	secret := e.must(&wire.AdminAction{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: "secret"}}}).Id
	e.must(override(&wire.SetChannelOverride{ChannelId: secret, RoleId: ev, Deny: community.PermViewChannel}))
	e.must(override(&wire.SetChannelOverride{ChannelId: secret, Member: bob.Identity.Public, Allow: community.PermViewChannel}))
	if got := channelIDs(syncState(t, bob)); !slices.Contains(got, "secret") {
		t.Fatalf("bob can't see his channel: %v", got)
	}
	if got := channelIDs(syncState(t, alice)); slices.Contains(got, "secret") {
		t.Fatalf("alice sees bob's channel: %v", got)
	}
	// The owner sees member overrides in the state.
	for _, c := range syncState(t, e.owner).Channels {
		if c.Id == secret && (len(c.Overrides) != 2 || !bytes.Equal(c.Overrides[1].Member, bob.Identity.Public)) {
			t.Fatalf("overrides in state: %v", c.Overrides)
		}
	}
	// Exactly one target, and only channel bits.
	for _, bad := range []*wire.SetChannelOverride{
		{ChannelId: gen, RoleId: ev, Member: bob.Identity.Public, Deny: community.PermSendMessages},
		{ChannelId: gen, Deny: community.PermSendMessages},
		{ChannelId: gen, CategoryId: 1, RoleId: ev},
		{ChannelId: gen, Member: bob.Identity.Public, Allow: community.PermKick},
		{ChannelId: gen, Member: bob.Identity.Public, Allow: community.PermSendMessages, Deny: community.PermSendMessages},
		{ChannelId: gen, Member: []byte("not a member key")},
	} {
		if _, err := e.act(e.owner, override(bad)); err == nil {
			t.Errorf("override %v accepted", bad)
		}
	}
	audit, _ := e.comm.Server.AuditSummaries(ctx)
	if !slices.ContainsFunc(audit, func(s string) bool {
		return strings.HasPrefix(s, "set Bob (") && strings.HasSuffix(s, ") override on #secret")
	}) {
		t.Errorf("audit: %q", audit)
	}
}

// Role hierarchy: a member manages only roles strictly below their highest
// role, only members below them, and only permissions they hold.
func TestRoleHierarchyRules(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	mod := e.member("Mod", e.invite.Secret)
	alice := e.member("Alice", e.invite.Secret)
	modPerms := community.PermManageRoles | community.PermManageChannels | community.PermKick
	low := e.must(createRole("Low", 0)).Id
	modRole := e.must(createRole("Mod", modPerms)).Id
	high := e.must(createRole("High", 0)).Id
	// Lowest first: Low, Mod, then every other role, High on top.
	order := []int64{low, modRole}
	for _, r := range syncState(t, e.owner).Roles {
		if !r.Builtin && !slices.Contains([]int64{low, modRole, high}, r.Id) {
			order = append(order, r.Id)
		}
	}
	order = append(order, high)
	e.must(&wire.AdminAction{Op: &wire.AdminAction_ReorderRoles{ReorderRoles: &wire.ReorderRoles{RoleIds: order}}})
	e.must(assign(mod, modRole))

	try := func(a *wire.AdminAction) error { _, err := e.act(mod, a); return err }
	edit := func(id int64, perms uint64) *wire.AdminAction {
		return &wire.AdminAction{Op: &wire.AdminAction_EditRole{EditRole: &wire.EditRole{Id: id, Permissions: perms}}}
	}
	// Roles at or above their own: no.
	permDenied(t, "edit own role", try(edit(modRole, modPerms)))
	permDenied(t, "edit higher role", try(edit(high, 0)))
	permDenied(t, "assign higher role", try(assign(alice, high)))
	permDenied(t, "assign own role", try(assign(alice, modRole)))
	permDenied(t, "delete higher role", try(&wire.AdminAction{Op: &wire.AdminAction_DeleteRole{DeleteRole: &wire.DeleteRole{Id: high}}}))
	// Permissions they lack: no, neither granting nor taking away.
	permDenied(t, "grant ban", try(edit(low, community.PermBan)))
	permDenied(t, "create with ban", try(createRole("Banners", community.PermBan)))
	e.must(edit(low, community.PermBan))
	permDenied(t, "take away ban", try(edit(low, 0)))
	// A lower role carrying a permission they lack is out of their hands
	// altogether: no edits, and no granting it (that would hand out Ban).
	permDenied(t, "edit a lower role carrying ban", try(edit(low, community.PermBan|community.PermKick)))
	permDenied(t, "assign a lower role carrying ban", try(assign(alice, low)))
	e.must(edit(low, 0))
	if err := try(edit(low, community.PermKick)); err != nil {
		t.Fatalf("adding a permission they hold to a lower role: %v", err)
	}
	// Below them: yes.
	if err := try(assign(alice, low)); err != nil {
		t.Fatalf("assign a lower role: %v", err)
	}
	// Options: color, display separately, mentionable; audited.
	if err := try(&wire.AdminAction{Op: &wire.AdminAction_EditRole{EditRole: &wire.EditRole{
		Id: low, Name: "Low", Permissions: community.PermKick, Color: 0x22aa44, Hoist: true, Mentionable: true,
	}}}); err != nil {
		t.Fatal(err)
	}
	for _, r := range syncState(t, alice).Roles {
		if r.Id == low && (!r.Hoist || !r.Mentionable || r.Color != 0x22aa44) {
			t.Fatalf("role options: %v", r)
		}
	}
	if _, err := e.act(e.owner, &wire.AdminAction{Op: &wire.AdminAction_CreateRole{CreateRole: &wire.CreateRole{Name: "Bad", Color: 0x1000000}}}); err == nil {
		t.Fatal("color above 0xFFFFFF accepted")
	}
	audit, _ := e.comm.Server.AuditSummaries(ctx)
	if !slices.Contains(audit, "edited role Low: color, display separately on, mentionable on") {
		t.Errorf("audit: %q", audit)
	}
	// Overrides: only roles below and members below, only bits they hold.
	gen := e.owner.Channel("general").Id
	permDenied(t, "override a higher role", try(override(&wire.SetChannelOverride{ChannelId: gen, RoleId: high, Deny: community.PermSendMessages})))
	permDenied(t, "override the owner", try(override(&wire.SetChannelOverride{ChannelId: gen, Member: e.owner.Identity.Public, Deny: community.PermSendMessages})))
	permDenied(t, "override with a bit they lack", try(override(&wire.SetChannelOverride{ChannelId: gen, RoleId: low, Allow: community.PermMentionEveryone})))
	if err := try(override(&wire.SetChannelOverride{ChannelId: gen, Member: alice.Identity.Public, Deny: community.PermSendMessages})); err != nil {
		t.Fatalf("mod overriding a member below: %v", err)
	}
	if err := try(override(&wire.SetChannelOverride{ChannelId: gen, RoleId: e.everyoneID(), Deny: community.PermSendMessages})); err != nil {
		t.Fatalf("mod overriding @everyone: %v", err)
	}
}

// Slowmode: one message per window per member; members who can manage
// messages or channels are exempt; Send Messages is still required.
func TestSlowmode(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	gen := e.owner.Channel("general").Id
	if _, err := e.act(e.owner, editChannel(&wire.EditChannel{Id: gen, Name: "general", SlowmodeSeconds: ptr(uint32(6*3600 + 1))})); err == nil {
		t.Fatal("slowmode over 6 hours accepted")
	}
	e.must(editChannel(&wire.EditChannel{Id: gen, Name: "general", SlowmodeSeconds: ptr(uint32(60))}))
	if c := syncState(t, alice).Channels[0]; c.SlowmodeSeconds != 60 {
		t.Fatalf("slowmode in state: %d", c.SlowmodeSeconds)
	}
	now := time.Now()
	e.comm.Server.SetClock(func() time.Time { return now })
	if _, err := alice.Post(ctx, gen, "one"); err != nil {
		t.Fatal(err)
	}
	_, err := alice.Post(ctx, gen, "two")
	wantCode(t, "second post within the window", err, wire.RpcErrorCode_RPC_ERROR_CODE_RATE_LIMITED)
	_, err = alice.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_UploadBegin{UploadBegin: &wire.UploadBegin{ChannelId: gen, Name: "f.txt", Size: 3}}})
	wantCode(t, "upload within the window", err, wire.RpcErrorCode_RPC_ERROR_CODE_RATE_LIMITED)
	// The owner (and anyone with Manage Messages) is exempt.
	for i := 0; i < 3; i++ {
		if _, err := e.owner.Post(ctx, gen, "owner"); err != nil {
			t.Fatalf("owner under slowmode: %v", err)
		}
	}
	mods := e.must(createRole("Mods", community.PermManageMessages)).Id
	bob := e.member("Bob", e.invite.Secret)
	e.must(assign(bob, mods))
	for i := 0; i < 2; i++ {
		if _, err := bob.Post(ctx, gen, "mod"); err != nil {
			t.Fatalf("manage-messages member under slowmode: %v", err)
		}
	}
	now = now.Add(61 * time.Second)
	if _, err := alice.Post(ctx, gen, "three"); err != nil {
		t.Fatalf("after the window: %v", err)
	}
	// Slowmode off.
	e.must(editChannel(&wire.EditChannel{Id: gen, Name: "general", SlowmodeSeconds: ptr(uint32(0))}))
	if _, err := alice.Post(ctx, gen, "four"); err != nil {
		t.Fatalf("slowmode off: %v", err)
	}
	// Topic text.
	e.must(editChannel(&wire.EditChannel{Id: gen, Name: "general", Description: ptr("Be nice")}))
	if c := syncState(t, alice).Channels[0]; c.Description != "Be nice" {
		t.Fatalf("topic: %q", c.Description)
	}
	if _, err := e.act(e.owner, editChannel(&wire.EditChannel{Id: gen, Name: "general", Description: ptr(strings.Repeat("x", 1025))})); err == nil {
		t.Fatal("topic over 1024 characters accepted")
	}
	audit, _ := e.comm.Server.AuditSummaries(ctx)
	if !slices.Contains(audit, "edited #general: slowmode 60 seconds") || !slices.Contains(audit, "edited #general: topic changed") {
		t.Errorf("audit: %q", audit)
	}
}

// Invites: creating one needs Create Invite (off for @everyone by default);
// such members list and revoke their own; Manage Community sees and revokes
// all, with creator, uses, max uses and expiry.
func TestInviteGating(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	bob := e.member("Bob", e.invite.Secret)
	invite := func(m *testutil.Member, c *wire.CreateJoinKey) (*wire.AdminResult, error) {
		return e.act(m, &wire.AdminAction{Op: &wire.AdminAction_CreateJoinKey{CreateJoinKey: c}})
	}
	list := func(m *testutil.Member) ([]*wire.JoinKey, error) {
		r, err := m.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_ListJoinKeys{ListJoinKeys: &wire.ListJoinKeys{}}})
		return r.GetJoinKeys().GetKeys(), err
	}
	_, err := invite(alice, &wire.CreateJoinKey{Label: "alice"})
	permDenied(t, "default member creates an invite", err)
	_, err = list(alice)
	permDenied(t, "default member lists invites", err)

	ev := e.everyoneID()
	e.must(&wire.AdminAction{Op: &wire.AdminAction_EditRole{EditRole: &wire.EditRole{Id: ev, Permissions: community.DefaultEveryonePerms | community.PermCreateInvite}}})
	mine, err := invite(alice, &wire.CreateJoinKey{Label: "alice's", MaxUses: 5, ExpiresInSeconds: 3600})
	if err != nil {
		t.Fatal(err)
	}
	bobs, err := invite(bob, &wire.CreateJoinKey{Label: "bob's"})
	if err != nil {
		t.Fatal(err)
	}
	// Role-granting and public invites need more.
	role := e.must(createRole("Guests", 0)).Id
	_, err = invite(alice, &wire.CreateJoinKey{Label: "with a role", RoleIds: []int64{role}})
	permDenied(t, "role-granting invite without Manage Roles", err)
	_, err = invite(alice, &wire.CreateJoinKey{Label: "public", Public: true})
	permDenied(t, "public invite without Manage Community", err)

	keys, err := list(alice)
	if err != nil || len(keys) != 1 || keys[0].Label != "alice's" || keys[0].MaxUses != 5 || keys[0].ExpiresAt == 0 || keys[0].CreatedByName != "Alice" {
		t.Fatalf("alice's list: %v %v", keys, err)
	}
	_, err = e.act(alice, &wire.AdminAction{Op: &wire.AdminAction_RevokeJoinKey{RevokeJoinKey: &wire.RevokeJoinKey{Id: bobs.JoinKey.Id}}})
	permDenied(t, "revoking someone else's invite", err)
	if _, err := e.act(alice, &wire.AdminAction{Op: &wire.AdminAction_RevokeJoinKey{RevokeJoinKey: &wire.RevokeJoinKey{Id: mine.JoinKey.Id}}}); err != nil {
		t.Fatalf("revoking her own: %v", err)
	}
	// The owner (Manage Community) sees all four and revokes any.
	all, err := list(e.owner)
	if err != nil || len(all) != 3 {
		t.Fatalf("owner's list: %v %v", all, err)
	}
	creators := map[string]bool{}
	for _, k := range all {
		creators[k.CreatedByName] = true
		if k.Id == mine.JoinKey.Id && !k.Revoked {
			t.Error("revoked invite listed as active")
		}
	}
	if !creators["Alice"] || !creators["Bob"] || !creators["Owner"] {
		t.Fatalf("creators: %v", creators)
	}
	if _, err := e.act(e.owner, &wire.AdminAction{Op: &wire.AdminAction_RevokeJoinKey{RevokeJoinKey: &wire.RevokeJoinKey{Id: bobs.JoinKey.Id}}}); err != nil {
		t.Fatal(err)
	}
	carol := e.member("Carol", "")
	if r, err := carol.Join(ctx, bobs.JoinKey.Secret); err != nil || r.Reason != wire.RefusalReason_REFUSAL_REASON_KEY_REVOKED {
		t.Fatalf("joining with a revoked invite: %v %v", r, err)
	}
}
