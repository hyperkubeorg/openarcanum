package community_test

import (
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// A timeout takes away everything but seeing, reading and listening: a
// timed-out moderator can't kick, ban or manage roles, and a timed-out
// member can't change their avatar, about text or pronouns.
func TestTimeoutRestrictsEverything(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	mod := e.member("Mod", e.invite.Secret)
	low := e.member("Low", e.invite.Secret)
	e.role("Moderators", community.PermKick|community.PermBan|community.PermManageRoles|community.PermTimeoutMembers, mod)
	e.mustAdmin(e.owner, timeoutOp(mod, 600))

	for name, op := range map[string]*wire.AdminAction{
		"kick":    {Op: &wire.AdminAction_Kick{Kick: &wire.Kick{Member: low.Identity.Public}}},
		"ban":     {Op: &wire.AdminAction_Ban{Ban: &wire.BanMember{Member: low.Identity.Public}}},
		"timeout": timeoutOp(low, 60),
		"role":    {Op: &wire.AdminAction_CreateRole{CreateRole: &wire.CreateRole{Name: "Sneaky"}}},
	} {
		_, err := e.adminAs(mod, op)
		wantErr(t, err, "permission")
		_ = name
	}
	st := fresh(t, mod)
	if st.Permissions&^(community.PermViewChannel|community.PermReadMessageHistory|community.PermConnectVoice) != 0 {
		t.Fatalf("timed-out permissions %b", st.Permissions)
	}

	_, err := mod.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_UpdateProfile{UpdateProfile: &wire.UpdateProfile{About: "new about text"}}})
	wantErr(t, err, "timed out")
	_, err = mod.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_SetProfile{SetProfile: &wire.SetProfile{Avatar: pngOf(t, 8, 8, "")}}})
	wantErr(t, err, "timed out")

	// Lifting the timeout gives it all back.
	e.mustAdmin(e.owner, timeoutOp(mod, 0))
	e.mustAdmin(mod, &wire.AdminAction{Op: &wire.AdminAction_Kick{Kick: &wire.Kick{Member: low.Identity.Public}}})
	if _, err := mod.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_UpdateProfile{UpdateProfile: &wire.UpdateProfile{About: "new about text"}}}); err != nil {
		t.Fatal(err)
	}
}
