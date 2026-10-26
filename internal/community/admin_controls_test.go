package community_test

import (
	"strings"
	"testing"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Raid mode (lockdown): every new join waits for approval, invites
// included, and no invites can be created until it's off.
func TestRaidModeLockdown(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	inv := e.newInvite(nil)
	e.set(func(s *wire.CommunitySettings) { s.Lockdown = true })
	e.auditHas(t, "raid mode on")

	raider := e.member("Raider", "")
	r, err := raider.Join(ctx, inv.Secret)
	if err != nil || r.Status != wire.JoinStatus_JOIN_STATUS_PENDING {
		t.Fatalf("join with an invite in raid mode: %v %v", r, err)
	}
	_, err = e.adminAs(e.owner, &wire.AdminAction{Op: &wire.AdminAction_CreateJoinKey{CreateJoinKey: &wire.CreateJoinKey{Label: "x"}}})
	wantErr(t, err, "raid mode is on")

	// Off again: invites work as before (and those waiting still wait).
	e.set(func(s *wire.CommunitySettings) { s.Lockdown = false })
	e.member("Friend", e.newInvite(nil).Secret)
	if _, err := raider.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_Sync{Sync: &wire.Sync{}}}); err == nil {
		t.Fatal("a member who joined in raid mode was admitted when it ended")
	}
}

// New-member restrictions: for new_member_minutes after joining, a member
// without a role can't post links, mention anyone or attach files; plain
// messages are fine, and the restriction ends on time or with a role.
func TestNewMemberRestrictions(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	e.set(func(s *wire.CommunitySettings) { s.NewMemberMinutes = 10 })
	newbie := e.member("Newbie", e.invite.Secret)
	gen := e.owner.Channel("general").Id
	clock := fixedClock(e, time.Now())

	for _, text := range []string{"see https://spam.example/x", "hey @everyone", "hi <@" + itoa(1) + ">"} {
		_, err := newbie.Post(ctx, gen, text)
		wantErr(t, err, "New members can post links and mentions from")
	}
	_, err := newbie.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_UploadBegin{UploadBegin: &wire.UploadBegin{ChannelId: gen, Name: "x.txt", Size: 3}}})
	wantErr(t, err, "New members can attach files from")
	if _, err := newbie.Post(ctx, gen, "hello everyone, glad to be here"); err != nil {
		t.Fatalf("plain message: %v", err)
	}
	// A mention inside code is just text.
	if _, err := newbie.Post(ctx, gen, "`@everyone`"); err != nil {
		t.Fatalf("mention in code: %v", err)
	}

	// Over after the time.
	*clock = time.Now().Add(11 * time.Minute)
	if _, err := newbie.Post(ctx, gen, "now https://ok.example works"); err != nil {
		t.Fatalf("after the restriction: %v", err)
	}
	*clock = time.Now()
	if err := validMinutes(e); err != nil {
		t.Fatal(err)
	}
}

func validMinutes(e *env) error {
	s := cloneSettings(fresh(e.t, e.owner).Settings)
	s.NewMemberMinutes = community.MaxScreeningMinutes + 1
	_, err := e.adminAs(e.owner, &wire.AdminAction{Op: &wire.AdminAction_UpdateSettings{UpdateSettings: s}})
	if err == nil || !strings.Contains(err.Error(), "at most 7 days") {
		return err
	}
	return nil
}

// Bulk ban with history deletion: every selected member is checked before
// any is removed; then all are banned and their recent messages deleted,
// in one audited action.
func TestBulkRemove(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	gen := e.owner.Channel("general").Id
	mod := e.member("Mod", e.invite.Secret)
	peer := e.member("Peer", e.invite.Secret)
	e.role("Mods", community.PermBan|community.PermKick, mod, peer)
	var spammers []*testutil.Member
	var keys [][]byte
	for _, n := range []string{"S1", "S2", "S3"} {
		m := e.member(n, e.invite.Secret)
		if _, err := m.Post(ctx, gen, "buy cheap things"); err != nil {
			t.Fatal(err)
		}
		spammers = append(spammers, m)
		keys = append(keys, m.Identity.Public)
	}
	bulk := func(members [][]byte, ban bool, secs uint32) *wire.AdminAction {
		return &wire.AdminAction{Op: &wire.AdminAction_BulkRemove{BulkRemove: &wire.BulkRemove{Members: members, Ban: ban, Reason: "raid", DeleteMessageSeconds: secs}}}
	}
	// One target they don't outrank: nobody is removed.
	_, err := e.adminAs(mod, bulk(append(append([][]byte(nil), keys...), peer.Identity.Public), true, 3600))
	wantErr(t, err, "below you")
	if len(members(t, e.owner)) != 6 {
		t.Fatal("a refused bulk ban removed someone")
	}
	_, err = e.adminAs(mod, bulk(keys, false, 3600))
	wantErr(t, err, "only a ban deletes messages")

	r := e.mustAdmin(mod, bulk(keys, true, 3600))
	if r.Removed != 3 || r.Deleted != 3 {
		t.Fatalf("removed %d, deleted %d", r.Removed, r.Deleted)
	}
	if len(e.bans()) != 3 {
		t.Fatalf("bans: %d", len(e.bans()))
	}
	for _, m := range historyMsgs(t, e.owner, gen) {
		if strings.Contains(m.Text, "cheap things") && !m.Deleted {
			t.Fatal("a spammer's message survived")
		}
	}
	e.auditHas(t, "banned 3 members")
	_ = spammers
}
