package community_test

import (
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

func assignOp(m []byte, roles ...int64) *wire.AdminAction {
	return &wire.AdminAction{Op: &wire.AdminAction_AssignRoles{AssignRoles: &wire.AssignRoles{Member: m, RoleIds: roles}}}
}

// A member holding Manage Roles can't hand out a role below theirs that
// carries a permission they don't hold (here Ban): not to themselves, not
// by invite. The role's position alone doesn't decide it.
func TestAssignRoleNeedsItsPermissions(t *testing.T) {
	e := setup(t, nil)
	mod := e.member("Mod", e.invite.Secret)
	victim := e.member("Victim", e.invite.Secret)
	modRole := e.role("Mod", community.PermManageRoles|community.PermKick|community.PermCreateInvite, mod)
	banRole := e.role("Banhammer", community.PermBan)
	kickRole := e.role("Bouncer", community.PermKick)
	ban := &wire.AdminAction{Op: &wire.AdminAction_Ban{Ban: &wire.BanMember{Member: victim.Identity.Public}}}

	_, err := e.adminAs(mod, assignOp(mod.Identity.Public, modRole, banRole))
	wantErr(t, err, "permissions you all have")
	_, err = e.adminAs(mod, assignOp(victim.Identity.Public, banRole))
	wantErr(t, err, "permissions you all have")
	_, err = e.adminAs(mod, &wire.AdminAction{Op: &wire.AdminAction_CreateJoinKey{CreateJoinKey: &wire.CreateJoinKey{Label: "x", RoleIds: []int64{banRole}}}})
	wantErr(t, err, "permissions you all have")
	if _, err := e.adminAs(mod, ban); err == nil {
		t.Fatal("the moderator banned someone after all")
	}
	// Editing, deleting or moving a role that carries what they lack is
	// refused too.
	_, err = e.adminAs(mod, &wire.AdminAction{Op: &wire.AdminAction_EditRole{EditRole: &wire.EditRole{Id: banRole, Name: "Renamed", Permissions: community.PermBan}}})
	wantErr(t, err, "permissions you all have")
	_, err = e.adminAs(mod, &wire.AdminAction{Op: &wire.AdminAction_DeleteRole{DeleteRole: &wire.DeleteRole{Id: banRole}}})
	wantErr(t, err, "permissions you all have")

	// A role whose permissions they hold is theirs to give.
	e.mustAdmin(mod, assignOp(victim.Identity.Public, kickRole))
	// The owner gives anything.
	e.mustAdmin(e.owner, assignOp(victim.Identity.Public, kickRole, banRole))
}

// An Administrator role created after the moderator's role sits below it,
// yet the moderator can neither take it nor act on its holders.
func TestAdministratorRoleBelowModerator(t *testing.T) {
	e := setup(t, nil)
	mod := e.member("Mod", e.invite.Secret)
	coadmin := e.member("Coadmin", e.invite.Secret)
	modRole := e.role("Moderator", community.PermManageRoles|community.PermKick|community.PermBan, mod)
	admins := e.role("Co-admins", community.PermAdministrator, coadmin)

	_, err := e.adminAs(mod, assignOp(mod.Identity.Public, modRole, admins))
	wantErr(t, err, "permissions you all have")
	if fresh(t, mod).Permissions&community.PermAdministrator != 0 {
		t.Fatal("the moderator became an administrator")
	}
	// Taking the role away from its holder, kicking or banning them: refused.
	_, err = e.adminAs(mod, assignOp(coadmin.Identity.Public))
	if err == nil {
		t.Fatal("the moderator stripped an administrator's role")
	}
	_, err = e.adminAs(mod, &wire.AdminAction{Op: &wire.AdminAction_Kick{Kick: &wire.Kick{Member: coadmin.Identity.Public}}})
	wantErr(t, err, "below you")
	_, err = e.adminAs(mod, &wire.AdminAction{Op: &wire.AdminAction_Ban{Ban: &wire.BanMember{Member: coadmin.Identity.Public}}})
	wantErr(t, err, "below you")
	_, err = e.adminAs(mod, &wire.AdminAction{Op: &wire.AdminAction_DeleteRole{DeleteRole: &wire.DeleteRole{Id: admins}}})
	wantErr(t, err, "permissions you all have")
}
