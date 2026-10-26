package community

import "testing"

// The permission matrix: base permissions, per-channel overrides and role
// hierarchy.
func TestPermissionMatrix(t *testing.T) {
	everyone := RoleInfo{ID: 1, Perms: DefaultEveryonePerms, Position: 0}
	roles := map[int64]RoleInfo{
		1: everyone,
		2: {ID: 2, Perms: PermAdministrator, Position: 30},
		3: {ID: 3, Perms: PermKick | PermManageMessages, Position: 20},
		4: {ID: 4, Perms: 0, Position: 10}, // "staff": only used in overrides
		5: {ID: 5, Perms: PermBan, Position: 5},
	}
	member := Subject{}
	mod := Subject{RoleIDs: []int64{3}}
	staff := Subject{RoleIDs: []int64{4}}
	admin := Subject{RoleIDs: []int64{2}}
	owner := Subject{Owner: true}
	modStaff := Subject{RoleIDs: []int64{3, 4}}

	private := []Override{{RoleID: 1, Deny: PermViewChannel}, {RoleID: 4, Allow: PermViewChannel}}
	readOnly := []Override{{RoleID: 1, Deny: PermSendMessages}, {RoleID: 3, Allow: PermSendMessages}}
	staffMuted := []Override{{RoleID: 4, Deny: PermSendMessages}}
	everyoneAllowsKick := []Override{{RoleID: 1, Allow: PermKick | PermViewChannel}}

	type tc struct {
		name      string
		sub       Subject
		overrides []Override
		want      uint64 // bits that must be set
		notWant   uint64 // bits that must be clear
	}
	cases := []tc{
		{"member default channel", member, nil, PermViewChannel | PermSendMessages | PermAttachFiles, PermManageMessages | PermKick},
		{"member private channel", member, private, 0, PermAll},
		{"staff private channel", staff, private, PermViewChannel | PermSendMessages, 0},
		{"mod private channel", mod, private, 0, PermAll},
		{"mod+staff private channel", modStaff, private, PermViewChannel | PermManageMessages, 0},
		{"admin private channel", admin, private, PermAll, 0},
		{"owner private channel", owner, private, PermAll, 0},
		{"member read-only", member, readOnly, PermViewChannel, PermSendMessages},
		{"mod read-only", mod, readOnly, PermViewChannel | PermSendMessages, 0},
		{"staff muted", staff, staffMuted, PermViewChannel, PermSendMessages},
		{"member muted role not held", member, staffMuted, PermSendMessages, 0},
		{"override can't grant community perms", member, everyoneAllowsKick, PermViewChannel, PermKick},
	}
	for _, c := range cases {
		got := ChannelPerms(c.sub, everyone, roles, c.overrides)
		if got&c.want != c.want {
			t.Errorf("%s: got %b, missing %b", c.name, got, c.want&^got)
		}
		if got&c.notWant != 0 {
			t.Errorf("%s: got %b, unexpected %b", c.name, got, got&c.notWant)
		}
	}

	base := []struct {
		name string
		sub  Subject
		want uint64
		not  uint64
	}{
		{"member", member, PermViewChannel, PermKick | PermBan | PermManageRoles},
		{"mod", mod, PermKick | PermManageMessages, PermBan},
		{"admin", admin, PermAll, 0},
		{"owner", owner, PermAll, 0},
	}
	for _, b := range base {
		got := BasePerms(b.sub, everyone, roles)
		if got&b.want != b.want || got&b.not != 0 {
			t.Errorf("base %s: got %b", b.name, got)
		}
	}

	ranks := []struct {
		actor, target Subject
		want          bool
	}{
		{admin, mod, true},
		{mod, admin, false},
		{mod, mod, false},
		{mod, member, true},
		{member, member, false},
		{admin, owner, false},
		{owner, admin, true},
		{Subject{RoleIDs: []int64{5}}, member, true},
		{Subject{RoleIDs: []int64{5}}, staff, false},
	}
	for i, r := range ranks {
		if got := Outranks(r.actor, r.target, roles); got != r.want {
			t.Errorf("rank case %d: got %v want %v", i, got, r.want)
		}
	}
}
