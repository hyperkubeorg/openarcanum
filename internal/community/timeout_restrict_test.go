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

// A timeout and a server mute outlast leaving and joining again (as on
// Discord): a timed-out member can't shed them by leaving and rejoining
// with an invite on the same session.
func TestTimeoutAndMuteSurviveRejoin(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	waitAttached(t, e)
	vc := voiceChannel(e, "talk")
	gen := e.owner.Channel("general").Id
	low := e.member("Low", e.invite.Secret)
	e.mustAdmin(e.owner, timeoutOp(low, 600))
	e.mustAdmin(e.owner, &wire.AdminAction{Op: &wire.AdminAction_VoiceMute{VoiceMute: &wire.VoiceMute{Member: low.Identity.Public, Muted: true}}})

	if _, err := low.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_Leave{Leave: &wire.Leave{}}}); err != nil {
		t.Fatal(err)
	}
	// The session stays open a moment for the reply: join again on it.
	if r, err := low.Join(ctx, e.invite.Secret); err != nil || r.Status != wire.JoinStatus_JOIN_STATUS_ADMITTED {
		t.Fatalf("rejoin: %v %v", r, err)
	}
	for range low.Events {
	}
	again := testutil.NewMember(t, "Low")
	again.Identity, again.Static = low.Identity, low.Static
	if err := again.Connect(ctx, e.relay.URL, e.serverKey()); err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if again.Status.Status != wire.MemberStatus_MEMBER_STATUS_ACTIVE {
		t.Fatalf("after rejoining: %v", again.Status)
	}
	if st := fresh(t, again); st.Self.TimeoutUntil == 0 {
		t.Fatal("the timeout didn't survive leaving and rejoining")
	}
	_, err := again.Post(ctx, gen, "back")
	wantErr(t, err, "timed out")
	vs, err := voiceJoin(again, vc)
	if err != nil {
		t.Fatal(err)
	}
	if !vs.ServerMute {
		t.Fatal("the server mute didn't survive leaving and rejoining")
	}
}
