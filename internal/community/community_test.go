package community_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/session"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// posixModes: Windows has no POSIX mode bits (files there report 0666, or
// 0444 when read-only; privacy comes from the profile's ACLs), so mode
// assertions only hold elsewhere.
const posixModes = runtime.GOOS != "windows"

type env struct {
	t      *testing.T
	relay  *testutil.Relay
	comm   *testutil.Community
	owner  *testutil.Member
	ownerK *identity.Key
	invite *wire.JoinKey
}

func (e *env) serverKey() []byte { return e.comm.Server.ServerKey() }
func (e *env) id() []byte        { return e.ownerK.Public }

func setup(t *testing.T, settings *wire.CommunitySettings) *env {
	t.Helper()
	return setupWithLog(t, settings, testutil.Quiet())
}

// setupWithLog is setup with the community server logging to log.
func setupWithLog(t *testing.T, settings *wire.CommunitySettings, log *slog.Logger) *env {
	t.Helper()
	return setupWithConfig(t, settings, log, nil)
}

// setupWithConfig is setupWithLog with the server's node config changed by mutate.
func setupWithConfig(t *testing.T, settings *wire.CommunitySettings, log *slog.Logger, mutate func(*community.Config)) *env {
	t.Helper()
	r := testutil.StartRelay(t)
	c := testutil.StartCommunityWithLog(t, log, mutate, r.URL)
	return claimedOn(t, r, c, settings)
}

// claimedOn claims community c (reached through relay r) with a fresh owner.
func claimedOn(t *testing.T, r *testutil.Relay, c *testutil.Community, settings *wire.CommunitySettings) *env {
	t.Helper()
	owner := testutil.NewMember(t, "Owner")
	ctx := testutil.Ctx(t)
	if err := owner.Connect(ctx, r.URL, c.ClaimCode.ServerKey); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Close)
	if owner.Status.Claimed || owner.Status.Status != wire.MemberStatus_MEMBER_STATUS_UNKNOWN {
		t.Fatalf("fresh server status: %v", owner.Status)
	}
	ownerKey, _ := identity.Generate()
	if settings == nil {
		settings = &wire.CommunitySettings{Name: "Test", Visibility: wire.Visibility_VISIBILITY_PRIVATE, JoinPolicy: wire.JoinPolicy_JOIN_POLICY_INVITE_ONLY, MessageRetentionDays: 30, FileRetentionDays: 7, SearchEnabled: true}
	}
	res, err := owner.Claim(ctx, ownerKey, c.ClaimCode, settings)
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, relay: r, comm: c, owner: owner, ownerK: ownerKey, invite: res.FirstInvite}
}

func (e *env) member(name, joinKey string) *testutil.Member {
	e.t.Helper()
	m := testutil.NewMember(e.t, name)
	ctx := testutil.Ctx(e.t)
	if err := m.Connect(ctx, e.relay.URL, e.serverKey()); err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(m.Close)
	if joinKey != "" {
		res, err := m.Join(ctx, joinKey)
		if err != nil {
			e.t.Fatal(err)
		}
		if res.Status != wire.JoinStatus_JOIN_STATUS_ADMITTED {
			e.t.Fatalf("%s not admitted: %v %v", name, res.Status, res.Reason)
		}
	}
	return m
}

func (e *env) newInvite(mut func(*wire.CreateJoinKey)) *wire.JoinKey {
	e.t.Helper()
	c := &wire.CreateJoinKey{Label: "test"}
	if mut != nil {
		mut(c)
	}
	res, err := e.owner.Admin(testutil.Ctx(e.t), e.id(), &wire.AdminAction{Op: &wire.AdminAction_CreateJoinKey{CreateJoinKey: c}})
	if err != nil {
		e.t.Fatal(err)
	}
	return res.JoinKey
}

func TestClaimCreatesCommunity(t *testing.T) {
	e := setup(t, nil)
	st := e.owner.State
	if st == nil || st.Info.Name != "Test" || !st.Info.Claimed {
		t.Fatalf("bad state after claim: %v", st)
	}
	if e.owner.Channel("general") == nil {
		t.Fatal("no #general")
	}
	if e.invite == nil || e.invite.Secret == "" || e.invite.ExpiresAt == 0 {
		t.Fatal("no first invite with an expiry")
	}
	if d := time.Until(time.UnixMilli(e.invite.ExpiresAt)); d < 6*24*time.Hour || d > 7*24*time.Hour+time.Minute {
		t.Fatalf("first invite expires in %v, want 7 days", d)
	}
	if !st.Self.Owner {
		t.Fatal("claimer isn't marked as owner")
	}
	// A second claim fails.
	other := testutil.NewMember(t, "Other")
	ctx := testutil.Ctx(t)
	if err := other.Connect(ctx, e.relay.URL, e.serverKey()); err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	k, _ := identity.Generate()
	if _, err := other.Claim(ctx, k, e.comm.ClaimCode, nil); err == nil {
		t.Fatal("second claim accepted")
	}
}

func TestClaimRequiresSecret(t *testing.T) {
	r := testutil.StartRelay(t)
	c := testutil.StartCommunity(t, nil, r.URL)
	m := testutil.NewMember(t, "X")
	ctx := testutil.Ctx(t)
	if err := m.Connect(ctx, r.URL, c.ClaimCode.ServerKey); err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	bad := proto.Clone(c.ClaimCode).(*wire.ClaimCode)
	bad.Secret = []byte("nope")
	k, _ := identity.Generate()
	if _, err := m.Claim(ctx, k, bad, nil); err == nil || !strings.Contains(err.Error(), "claim code") {
		t.Fatalf("wrong secret: %v", err)
	}
}

// Two members chat through the relay: the server fans out once per message,
// signed and encrypted, and both decrypt it.
func TestTwoMembersChat(t *testing.T) {
	e := setup(t, nil)
	alice := e.member("Alice", e.invite.Secret)
	ctx := testutil.Ctx(t)
	gen := alice.Channel("general")
	if gen == nil || len(gen.Key) != 32 || len(gen.Topic) != 32 {
		t.Fatal("member didn't get the channel key and topic")
	}
	if err := alice.Watch(ctx, gen, e.serverKey()); err != nil {
		t.Fatal(err)
	}
	if err := e.owner.Watch(ctx, e.owner.Channel("general"), e.serverKey()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.owner.Post(ctx, gen.Id, "hello alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.Post(ctx, gen.Id, "hi owner"); err != nil {
		t.Fatal(err)
	}
	// Alice's own "joined" event can arrive after her watch starts (a slow
	// machine): only the two posts count.
	got := posts(alice.WaitReceived(gen.Id, 2, 5*time.Second))
	if len(got) < 2 {
		got = posts(alice.WaitReceived(gen.Id, 3, 5*time.Second))
	}
	if len(got) != 2 || got[0].Text != "hello alice" || got[1].Text != "hi owner" || got[0].AuthorName != "Owner" {
		t.Fatalf("alice received %v", got)
	}
	owner := posts(e.owner.WaitReceived(gen.Id, 2, 5*time.Second))
	if len(owner) < 2 {
		owner = posts(e.owner.WaitReceived(gen.Id, 3, 5*time.Second))
	}
	if len(owner) != 2 {
		t.Fatalf("owner received %d", len(owner))
	}
}

// posts leaves out system events (joins, leaves).
func posts(msgs []*wire.Message) []*wire.Message {
	var out []*wire.Message
	for _, m := range msgs {
		if m.GetSystem() == nil {
			out = append(out, m)
		}
	}
	return out
}

func TestEnvelopeRejectsForgery(t *testing.T) {
	e := setup(t, nil)
	gen := e.owner.Channel("general")
	impostor, _ := identity.Generate()
	ctx := testutil.Ctx(t)
	if err := e.owner.Watch(ctx, gen, impostor.Public); err != nil {
		t.Fatal(err)
	}
	e.owner.Post(ctx, gen.Id, "signed by the real server")
	if got := e.owner.WaitReceived(gen.Id, 1, 500*time.Millisecond); len(got) != 0 {
		t.Fatal("envelope verified under the wrong server key")
	}
}

func TestJoinKeyRefusals(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	now := time.Now()
	e.comm.Server.SetClock(func() time.Time { return now })

	revoked := e.newInvite(nil)
	if _, err := e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_RevokeJoinKey{RevokeJoinKey: &wire.RevokeJoinKey{Id: revoked.Id}}}); err != nil {
		t.Fatal(err)
	}
	expiring := e.newInvite(func(c *wire.CreateJoinKey) { c.ExpiresInSeconds = 60 })
	once := e.newInvite(func(c *wire.CreateJoinKey) { c.MaxUses = 1 })

	try := func(name, key string) *wire.JoinResult {
		m := e.member(name, "")
		r, err := m.Join(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := try("r", revoked.Secret); r.Status != wire.JoinStatus_JOIN_STATUS_REFUSED || r.Reason != wire.RefusalReason_REFUSAL_REASON_KEY_REVOKED {
		t.Fatalf("revoked key: %v", r)
	}
	if r := try("e1", expiring.Secret); r.Status != wire.JoinStatus_JOIN_STATUS_ADMITTED {
		t.Fatalf("fresh key refused: %v", r)
	}
	// Exactly at expiry the key is dead.
	now = now.Add(60 * time.Second)
	if r := try("e2", expiring.Secret); r.Status != wire.JoinStatus_JOIN_STATUS_REFUSED || r.Reason != wire.RefusalReason_REFUSAL_REASON_KEY_EXPIRED {
		t.Fatalf("expired key: %v", r)
	}
	if r := try("o1", once.Secret); r.Status != wire.JoinStatus_JOIN_STATUS_ADMITTED {
		t.Fatalf("single-use key refused: %v", r)
	}
	if r := try("o2", once.Secret); r.Reason != wire.RefusalReason_REFUSAL_REASON_KEY_EXHAUSTED {
		t.Fatalf("used-up key: %v", r)
	}
	if r := try("x", "made-up"); r.Reason != wire.RefusalReason_REFUSAL_REASON_KEY_INVALID {
		t.Fatalf("unknown key: %v", r)
	}
	if r := try("none", ""); r.Reason != wire.RefusalReason_REFUSAL_REASON_INVITE_REQUIRED {
		t.Fatalf("no key on an invite-only community: %v", r)
	}
}

func TestHistorySurvivesRestart(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	gen := e.owner.Channel("general")
	for i := 0; i < 5; i++ {
		if _, err := e.owner.Post(ctx, gen.Id, fmt.Sprintf("msg %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	e.owner.Close()
	e.comm.Restart(t, e.relay.URL)
	owner := testutil.NewMember(t, "Owner")
	owner.Identity, owner.Static = e.owner.Identity, e.owner.Static
	if err := owner.Connect(ctx, e.relay.URL, e.serverKey()); err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if owner.Status.Status != wire.MemberStatus_MEMBER_STATUS_ACTIVE {
		t.Fatalf("owner status after restart: %v", owner.Status)
	}
	r, err := owner.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_History{History: &wire.History{ChannelId: gen.Id, Limit: 3}}})
	if err != nil {
		t.Fatal(err)
	}
	msgs := r.GetMessages()
	if len(msgs.Messages) != 3 || !msgs.More || msgs.Messages[2].Text != "msg 4" || msgs.Messages[0].Text != "msg 2" {
		t.Fatalf("history after restart: %v", msgs)
	}
	r, err = owner.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_History{History: &wire.History{ChannelId: gen.Id, BeforeId: msgs.Messages[0].Id}}})
	if err != nil || len(r.GetMessages().Messages) != 2 {
		t.Fatalf("older page: %v %v", r, err)
	}
}

// A removed member can't find (new topic) or decrypt (new key) anything sent
// after the removal, and the server refuses their next session.
func TestRemovedMemberLosesAccess(t *testing.T) {
	for _, ban := range []bool{false, true} {
		t.Run(fmt.Sprintf("ban=%v", ban), func(t *testing.T) {
			e := setup(t, nil)
			ctx := testutil.Ctx(t)
			mallory := e.member("Mallory", e.invite.Secret)
			old := mallory.Channel("general")
			if err := mallory.Watch(ctx, old, e.serverKey()); err != nil {
				t.Fatal(err)
			}
			var op *wire.AdminAction
			if ban {
				op = &wire.AdminAction{Op: &wire.AdminAction_Ban{Ban: &wire.BanMember{Member: mallory.Identity.Public, Reason: "spam"}}}
			} else {
				op = &wire.AdminAction{Op: &wire.AdminAction_Kick{Kick: &wire.Kick{Member: mallory.Identity.Public}}}
			}
			if _, err := e.owner.Admin(ctx, e.id(), op); err != nil {
				t.Fatal(err)
			}
			// Mallory's session is told and closed.
			want := wire.MemberStatus_MEMBER_STATUS_REMOVED
			if ban {
				want = wire.MemberStatus_MEMBER_STATUS_BANNED
			}
			gotStatus := false
			for ev := range mallory.Events {
				if sc := ev.GetStateChanged(); sc != nil && sc.Status == want {
					gotStatus = true
				}
			}
			if !gotStatus {
				t.Fatal("removed member's session wasn't told")
			}
			if err := e.owner.Sync(ctx); err != nil {
				t.Fatal(err)
			}
			cur := e.owner.Channel("general")
			if bytes.Equal(cur.Topic, old.Topic) || bytes.Equal(cur.Key, old.Key) || cur.KeyEpoch == old.KeyEpoch {
				t.Fatal("channel key and topic were not rotated")
			}
			// Mallory (still on the relay) watches the old topic and, worst case,
			// the new topic too, with the old key.
			stale := &wire.Channel{Id: cur.Id, Topic: cur.Topic, Key: old.Key, KeyEpoch: old.KeyEpoch}
			if err := mallory.Watch(ctx, stale, e.serverKey()); err != nil {
				t.Fatal(err)
			}
			if err := e.owner.Watch(ctx, cur, e.serverKey()); err != nil {
				t.Fatal(err)
			}
			if _, err := e.owner.Post(ctx, cur.Id, "after removal"); err != nil {
				t.Fatal(err)
			}
			if got := e.owner.WaitReceived(cur.Id, 1, 5*time.Second); len(got) != 1 {
				t.Fatal("remaining member didn't get the message")
			}
			if got := mallory.WaitReceived(old.Id, 1, 500*time.Millisecond); len(got) != 0 {
				t.Fatalf("removed member read %d new messages", len(got))
			}
			// Reconnecting is refused.
			again := testutil.NewMember(t, "Mallory")
			again.Identity = mallory.Identity
			if err := again.Connect(ctx, e.relay.URL, e.serverKey()); err != nil {
				t.Fatal(err)
			}
			defer again.Close()
			if again.Status.Status != want {
				t.Fatalf("reconnect status = %v, want %v", again.Status.Status, want)
			}
			if ban {
				fresh := testutil.NewMember(t, "Mallory")
				fresh.Identity = mallory.Identity
				fresh.Static, _ = session.GenerateStatic()
				if err := fresh.Connect(ctx, e.relay.URL, e.serverKey()); err != nil {
					t.Fatal(err)
				}
				defer fresh.Close()
				if fresh.Status.Status != wire.MemberStatus_MEMBER_STATUS_BANNED {
					t.Fatal("banned key got in on a new device")
				}
			}
		})
	}
}

func TestApprovalQueue(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	req := e.newInvite(func(c *wire.CreateJoinKey) { c.RequestOnly = true })
	bob := e.member("Bob", "")
	r, err := bob.Join(ctx, req.Secret)
	if err != nil || r.Status != wire.JoinStatus_JOIN_STATUS_PENDING {
		t.Fatalf("request-only key: %v %v", r, err)
	}
	if _, err := bob.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_Sync{Sync: &wire.Sync{}}}); err == nil {
		t.Fatal("pending member could sync")
	}
	pr, err := e.owner.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_ListPending{ListPending: &wire.ListPending{}}})
	if err != nil || len(pr.GetMembers().Members) != 1 {
		t.Fatalf("pending list: %v %v", pr, err)
	}
	if _, err := e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_Approve{Approve: &wire.Approve{Member: bob.Identity.Public}}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-bob.Events:
			if sc := ev.GetStateChanged(); sc != nil && sc.Status == wire.MemberStatus_MEMBER_STATUS_ACTIVE {
				if sc.State == nil || len(sc.State.Channels) == 0 {
					t.Fatal("approval event without channels")
				}
				if err := bob.Sync(ctx); err != nil {
					t.Fatal(err)
				}
				return
			}
		case <-deadline:
			t.Fatal("approved member never told")
		}
	}
}

func TestRequestPolicyAndDeny(t *testing.T) {
	e := setup(t, &wire.CommunitySettings{Name: "Req", Visibility: wire.Visibility_VISIBILITY_PRIVATE, JoinPolicy: wire.JoinPolicy_JOIN_POLICY_REQUEST, MessageRetentionDays: 30, FileRetentionDays: 7})
	ctx := testutil.Ctx(t)
	carol := e.member("Carol", "")
	r, _ := carol.Join(ctx, "")
	if r.Status != wire.JoinStatus_JOIN_STATUS_PENDING {
		t.Fatalf("request policy: %v", r)
	}
	if _, err := e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_Deny{Deny: &wire.Deny{Member: carol.Identity.Public}}}); err != nil {
		t.Fatal(err)
	}
	for ev := range carol.Events {
		if sc := ev.GetStateChanged(); sc != nil && sc.Status == wire.MemberStatus_MEMBER_STATUS_REMOVED {
			return
		}
	}
	t.Fatal("denied member not told")
}

func TestAdminActionsAreSignedAndAudited(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	// A member without permission is refused.
	if _, err := alice.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: "x"}}}); err == nil {
		t.Fatal("member created a channel")
	}
	if _, err := e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: "Random Stuff"}}}); err != nil {
		t.Fatal(err)
	}
	// Replayed or mis-signed actions are rejected.
	a := &wire.AdminAction{Community: e.id(), Actor: e.owner.Identity.Public, At: time.Now().UnixMilli(), Nonce: bytes.Repeat([]byte{7}, 16),
		Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: "dup"}}}
	sign := func(k *identity.Key) *wire.ClientMessage {
		b, _ := protoMarshal(a)
		return &wire.ClientMessage{Body: &wire.ClientMessage_Admin{Admin: &wire.SignedAction{Action: b, Signature: k.Sign(identity.CtxAdminAction, b)}}}
	}
	if _, err := e.owner.Call(ctx, sign(e.owner.Identity)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.owner.Call(ctx, sign(e.owner.Identity)); err == nil {
		t.Fatal("replayed action accepted")
	}
	a.Nonce = bytes.Repeat([]byte{8}, 16)
	if _, err := e.owner.Call(ctx, sign(alice.Identity)); err == nil {
		t.Fatal("action signed by someone else accepted")
	}
	a.Nonce = bytes.Repeat([]byte{9}, 16)
	a.At = time.Now().Add(-time.Hour).UnixMilli()
	if _, err := e.owner.Call(ctx, sign(e.owner.Identity)); err == nil {
		t.Fatal("stale action accepted")
	}

	r, err := e.owner.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_AuditLog{AuditLog: &wire.AuditQuery{}}})
	if err != nil {
		t.Fatal(err)
	}
	entries := r.GetAudit().Entries
	if len(entries) < 2 {
		t.Fatalf("audit log has %d entries", len(entries))
	}
	for _, en := range entries {
		if err := identity.Verify(en.Actor, identity.CtxAdminAction, en.Action.Action, en.Action.Signature); err != nil {
			t.Fatalf("audit entry %q has a bad signature", en.Summary)
		}
	}
	if !strings.Contains(entries[len(entries)-1].Summary, "random-stuff") {
		t.Fatalf("audit summary: %q", entries[len(entries)-1].Summary)
	}
	// Admins-only by default.
	if _, err := alice.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_AuditLog{AuditLog: &wire.AuditQuery{}}}); err == nil {
		t.Fatal("member read the admin-only audit log")
	}
}

func TestRolesAndChannelOverrides(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	bob := e.member("Bob", e.invite.Secret)
	res, err := e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: "staff"}}})
	if err != nil {
		t.Fatal(err)
	}
	staffCh := res.Id
	rr, err := e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_CreateRole{CreateRole: &wire.CreateRole{Name: "Staff", Permissions: community.PermKick}}})
	if err != nil {
		t.Fatal(err)
	}
	staffRole := rr.Id
	for _, o := range []*wire.SetChannelOverride{
		{ChannelId: staffCh, RoleId: 1, Deny: community.PermViewChannel},
		{ChannelId: staffCh, RoleId: staffRole, Allow: community.PermViewChannel},
	} {
		if _, err := e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_SetChannelOverride{SetChannelOverride: o}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_AssignRoles{AssignRoles: &wire.AssignRoles{Member: alice.Identity.Public, RoleIds: []int64{staffRole}}}}); err != nil {
		t.Fatal(err)
	}
	alice.Sync(ctx)
	bob.Sync(ctx)
	if alice.Channel("staff") == nil {
		t.Fatal("staff member can't see #staff")
	}
	if bob.Channel("staff") != nil {
		t.Fatal("non-staff member can see #staff")
	}
	if _, err := bob.Post(ctx, staffCh, "sneaky"); err == nil {
		t.Fatal("non-staff member posted in #staff")
	}
	// Alice (Staff, Kick) can kick Bob but not the owner, and can't grant Admin.
	if _, err := alice.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_AssignRoles{AssignRoles: &wire.AssignRoles{Member: bob.Identity.Public, RoleIds: []int64{2}}}}); err == nil {
		t.Fatal("staff granted a role above their own")
	}
	if _, err := alice.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_Kick{Kick: &wire.Kick{Member: e.owner.Identity.Public}}}); err == nil {
		t.Fatal("staff kicked the owner")
	}
	if _, err := alice.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_Kick{Kick: &wire.Kick{Member: bob.Identity.Public}}}); err != nil {
		t.Fatalf("staff couldn't kick a member: %v", err)
	}
	// Removing Alice's role rotates #staff (she loses access).
	before := e.channelKey(staffCh)
	if _, err := e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_AssignRoles{AssignRoles: &wire.AssignRoles{Member: alice.Identity.Public}}}); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(before, e.channelKey(staffCh)) {
		t.Fatal("#staff key not rotated when a reader lost access")
	}
}

func (e *env) channelKey(id int64) []byte {
	e.t.Helper()
	if err := e.owner.Sync(testutil.Ctx(e.t)); err != nil {
		e.t.Fatal(err)
	}
	for _, c := range e.owner.State.Channels {
		if c.Id == id {
			return c.Key
		}
	}
	return nil
}

func TestRaidRevokeAndReplaceMember(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	raidKey := e.newInvite(nil)
	var raiders []*testutil.Member
	for i := 0; i < 3; i++ {
		r := e.member(fmt.Sprintf("raider%d", i), raidKey.Secret)
		if err := publishCard(ctx, r, []byte("card"), [][]byte{[]byte("d1")}, kp("d1", "a")); err != nil {
			t.Fatal(err)
		}
		raiders = append(raiders, r)
	}
	legit := e.member("Legit", e.invite.Secret)
	res, err := e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_RaidRevoke{RaidRevoke: &wire.RaidRevoke{JoinKeyId: raidKey.Id, Hours: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed != 3 {
		t.Fatalf("raid revoke removed %d", res.Removed)
	}
	late := e.member("late", "")
	if r, _ := late.Join(ctx, raidKey.Secret); r.Reason != wire.RefusalReason_REFUSAL_REASON_KEY_REVOKED {
		t.Fatal("raid key still works")
	}
	ml, _ := e.owner.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_ListMembers{ListMembers: &wire.ListMembers{}}})
	if n := len(ml.GetMembers().Members); n != 2 {
		t.Fatalf("%d active members after raid revoke, want 2", n)
	}
	// As a kick: the raiders' sessions end, and their contact cards go.
	for _, r := range raiders {
		for range r.Events {
		}
		if _, _, err := e.comm.Server.ContactCard(ctx, r.Identity.Public); err == nil {
			t.Fatalf("%s's contact card survived the raid revoke", r.Name)
		}
	}

	// Replace member: Legit lost their key, rejoins as Legit2, roles move over.
	rr, _ := e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_CreateRole{CreateRole: &wire.CreateRole{Name: "Regular"}}})
	e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_AssignRoles{AssignRoles: &wire.AssignRoles{Member: legit.Identity.Public, RoleIds: []int64{rr.Id}}}})
	legit2 := e.member("Legit", e.newInvite(nil).Secret)
	if _, err := e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_ReplaceMember{ReplaceMember: &wire.ReplaceMember{OldKey: legit.Identity.Public, NewKey: legit2.Identity.Public}}}); err != nil {
		t.Fatal(err)
	}
	legit2.Sync(ctx)
	if len(legit2.State.Self.RoleIds) != 1 || legit2.State.Self.RoleIds[0] != rr.Id {
		t.Fatalf("roles not moved: %v", legit2.State.Self.RoleIds)
	}
}

func TestDevices(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	// Same identity, second device: admitted and listed.
	d2 := testutil.NewMember(t, "Alice")
	d2.Identity = alice.Identity
	if err := d2.Connect(ctx, e.relay.URL, e.serverKey()); err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	if d2.Status.Status != wire.MemberStatus_MEMBER_STATUS_ACTIVE {
		t.Fatalf("second device: %v", d2.Status)
	}
	r, err := alice.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_ListDevices{ListDevices: &wire.ListDevices{}}})
	if err != nil || len(r.GetDevices().Devices) != 2 {
		t.Fatalf("devices: %v %v", r, err)
	}
	// Revoke device 2 with a signed revocation; it's cut off and refused.
	rev, _ := protoMarshal(&wire.DeviceRevocation{Community: e.id(), IdentityKey: alice.Identity.Public, DeviceKey: d2.Static.Public, At: time.Now().UnixMilli()})
	if _, err := alice.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_RevokeDevice{RevokeDevice: &wire.SignedDeviceRevocation{
		Revocation: rev, Signature: alice.Identity.Sign(identity.CtxDeviceRevocation, rev),
	}}}); err != nil {
		t.Fatal(err)
	}
	for range d2.Events {
	}
	d2b := testutil.NewMember(t, "Alice")
	d2b.Identity, d2b.Static = alice.Identity, d2.Static
	if err := d2b.Connect(ctx, e.relay.URL, e.serverKey()); err != nil {
		t.Fatal(err)
	}
	defer d2b.Close()
	if d2b.Status.Status != wire.MemberStatus_MEMBER_STATUS_DEVICE_REVOKED {
		t.Fatalf("revoked device status: %v", d2b.Status)
	}
	// A forged revocation (signed by someone else) is refused.
	bob := e.member("Bob", e.invite.Secret)
	forged, _ := protoMarshal(&wire.DeviceRevocation{Community: e.id(), IdentityKey: alice.Identity.Public, DeviceKey: alice.Static.Public})
	if _, err := bob.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_RevokeDevice{RevokeDevice: &wire.SignedDeviceRevocation{
		Revocation: forged, Signature: bob.Identity.Sign(identity.CtxDeviceRevocation, forged),
	}}}); err == nil {
		t.Fatal("forged revocation accepted")
	}
}

func TestDeviceLimit(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	set := e.owner.State.Settings
	set.MaxDevicesPerMember = 2
	if _, err := e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_UpdateSettings{UpdateSettings: set}}); err != nil {
		t.Fatal(err)
	}
	alice := e.member("Alice", e.invite.Secret)
	d2 := testutil.NewMember(t, "Alice")
	d2.Identity = alice.Identity
	d2.Connect(ctx, e.relay.URL, e.serverKey())
	defer d2.Close()
	d3 := testutil.NewMember(t, "Alice")
	d3.Identity = alice.Identity
	if err := d3.Connect(ctx, e.relay.URL, e.serverKey()); err != nil {
		t.Fatal(err)
	}
	defer d3.Close()
	if d2.Status.Status != wire.MemberStatus_MEMBER_STATUS_ACTIVE || d3.Status.Status != wire.MemberStatus_MEMBER_STATUS_DEVICE_LIMIT {
		t.Fatalf("device limit: d2=%v d3=%v", d2.Status.Status, d3.Status.Status)
	}
}

func TestSettingsValidationAndAudit(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	bad := []func(*wire.CommunitySettings){
		func(s *wire.CommunitySettings) { s.Name = "" },
		func(s *wire.CommunitySettings) { s.MaxFileSize = 26 << 20 },
		func(s *wire.CommunitySettings) { s.Tags = []string{"has space"} },
		func(s *wire.CommunitySettings) { s.MessageRetentionDays = 100000 },
		func(s *wire.CommunitySettings) {
			s.RelayMode = wire.RelayMode_RELAY_MODE_PINNED
			s.PinnedRelays = nil
		},
		func(s *wire.CommunitySettings) { s.Icon = []byte("not an image") },
		func(s *wire.CommunitySettings) { s.MaxDevicesPerMember = 0 },
	}
	for i, mut := range bad {
		s := cloneSettings(e.owner.State.Settings)
		mut(s)
		if _, err := e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_UpdateSettings{UpdateSettings: s}}); err == nil {
			t.Errorf("bad settings %d accepted", i)
		}
	}
	s := cloneSettings(e.owner.State.Settings)
	s.Description = "now with words"
	s.Tags = []string{"Go", "chat"}
	if _, err := e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_UpdateSettings{UpdateSettings: s}}); err != nil {
		t.Fatal(err)
	}
	e.owner.Sync(ctx)
	if got := e.owner.State.Settings; got.Description != "now with words" || strings.Join(got.Tags, ",") != "go,chat" {
		t.Fatalf("settings not saved/normalized: %v", got)
	}
}

func TestDefaults(t *testing.T) {
	d := community.DefaultSettings()
	if d.Visibility != wire.Visibility_VISIBILITY_PRIVATE || d.JoinPolicy != wire.JoinPolicy_JOIN_POLICY_INVITE_ONLY ||
		d.MessageRetentionDays != 30 || d.FileRetentionDays != 7 || !d.SearchEnabled || d.MaxFileSize != 10<<20 ||
		d.MaxDevicesPerMember != 5 || !d.AllowContactCards || d.AuditVisibility != wire.AuditVisibility_AUDIT_VISIBILITY_ADMINS ||
		d.RelayMode != wire.RelayMode_RELAY_MODE_AUTOMATIC || d.FileBandwidthLimit != 0 {
		t.Fatalf("defaults differ from docs/settings.md: %v", d)
	}
}

// The claim wizard can set the largest file size; without one a new
// community starts at the 10 MB default, and the 25 MB limit holds.
func TestClaimFileSize(t *testing.T) {
	if got := syncState(t, setup(t, nil).owner).Settings.MaxFileSize; got != community.DefaultMaxFileSize {
		t.Fatalf("default max file size %d, want %d", got, community.DefaultMaxFileSize)
	}
	wizard := func(size uint64) *wire.CommunitySettings {
		return &wire.CommunitySettings{Name: "Files", Visibility: wire.Visibility_VISIBILITY_PRIVATE,
			JoinPolicy: wire.JoinPolicy_JOIN_POLICY_INVITE_ONLY, MessageRetentionDays: 30, FileRetentionDays: 7, MaxFileSize: size}
	}
	if got := syncState(t, setup(t, wizard(25<<20)).owner).Settings.MaxFileSize; got != 25<<20 {
		t.Fatalf("wizard's max file size not used: %d", got)
	}
	r := testutil.StartRelay(t)
	c := testutil.StartCommunity(t, nil, r.URL)
	owner := testutil.NewMember(t, "Owner")
	ctx := testutil.Ctx(t)
	if err := owner.Connect(ctx, r.URL, c.ClaimCode.ServerKey); err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	k, _ := identity.Generate()
	if _, err := owner.Claim(ctx, k, c.ClaimCode, wizard(26<<20)); err == nil || !strings.Contains(err.Error(), "max file size") {
		t.Fatalf("a claim over the 25 MB limit: %v", err)
	}
}

func sum(b []byte) []byte { h := sha256.Sum256(b); return h[:] }

var _ = context.Background

// The claim code's file (`init -claim-file`, for containers) goes away when
// the server is claimed, and a server that starts claimed removes it too.
func TestClaimRemovesClaimFile(t *testing.T) {
	claimFile := filepath.Join(t.TempDir(), "claim.txt")
	if err := os.WriteFile(claimFile, []byte("openarcanum-claim:x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := setupWithConfig(t, nil, testutil.Quiet(), func(c *community.Config) { c.ClaimFile = claimFile })
	if _, err := os.Stat(claimFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("claim file still there after claiming: %v", err)
	}
	// Written again (say, restored from a copy): the next start removes it.
	if err := os.WriteFile(claimFile, []byte("openarcanum-claim:x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := community.DefaultConfig(e.comm.Dir)
	cfg.Relays = []string{e.relay.URL}
	cfg.ClaimFile = claimFile
	e.comm.Stop()
	srv, err := community.Open(testutil.Ctx(t), cfg, testutil.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	if _, err := os.Stat(claimFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("claim file still there after a claimed server started: %v", err)
	}
}

// Sessions are capped per device key and per member: a newer session past
// the cap closes the oldest (a device reconnecting while its old sessions
// linger isn't refused), and the others carry on.
func TestSessionsCappedPerDeviceAndMember(t *testing.T) {
	e := setupWithConfig(t, nil, testutil.Quiet(), func(c *community.Config) {
		c.MaxSessionsPerDevice, c.MaxSessionsPerMember = 2, 3
	})
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	connect := func(sameDevice bool) *testutil.Member {
		t.Helper()
		m := testutil.NewMember(t, "Alice")
		m.Identity = alice.Identity
		if sameDevice {
			m.Static = alice.Static
		}
		if err := m.Connect(ctx, e.relay.URL, e.serverKey()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(m.Close)
		if m.Status.Status != wire.MemberStatus_MEMBER_STATUS_ACTIVE {
			t.Fatalf("session status %v", m.Status.Status)
		}
		return m
	}
	closed := func(m *testutil.Member) bool {
		select {
		case _, open := <-m.Events:
			for open {
				_, open = <-m.Events
			}
			return true
		case <-time.After(5 * time.Second):
			return false
		}
	}
	// Same device key: the third session closes the first.
	second := connect(true)
	third := connect(true)
	if !closed(alice) {
		t.Fatal("the oldest session of the device wasn't closed")
	}
	if err := second.Sync(ctx); err != nil || third.Sync(ctx) != nil {
		t.Fatalf("a newer session of the device was closed: %v", err)
	}
	// Other devices, up to the member's cap: the fourth session closes
	// the oldest.
	other := connect(false)
	more := connect(false)
	if !closed(second) {
		t.Fatal("the member's oldest session wasn't closed")
	}
	for _, m := range []*testutil.Member{third, other, more} {
		if err := m.Sync(ctx); err != nil {
			t.Fatalf("a session under the member's cap was closed: %v", err)
		}
	}
}

// The database (and so its WAL) is readable by its owner only.
func TestInitCreatesPrivateDatabase(t *testing.T) {
	dir := t.TempDir()
	cfg := community.DefaultConfig(dir)
	cfg.Relays = []string{"ws://relay.invalid/ws"}
	if _, err := community.Init(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(dir, "community.db"))
	if err != nil {
		t.Fatal(err)
	}
	if m := st.Mode().Perm(); posixModes && m != 0o600 {
		t.Fatalf("community.db has mode %o", m)
	}
}
