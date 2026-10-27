package community_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"image"
	"image/png"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// ---------------------------------------------------------------- helpers

func (e *env) adminAs(m *testutil.Member, a *wire.AdminAction) (*wire.AdminResult, error) {
	return m.Admin(testutil.Ctx(e.t), e.id(), a)
}

// An action that is mostly padding (a huge nonce, or unknown fields) isn't
// applied, and so never reaches the audit log that keeps actions verbatim.
func TestOversizedAdminActionRefused(t *testing.T) {
	e := setup(t, nil)
	owner := e.owner
	ctx := testutil.Ctx(t)
	send := func(a *wire.AdminAction, extra []byte) error {
		a.Community, a.Actor, a.At = e.id(), owner.Identity.Public, time.Now().UnixMilli()
		b, _ := proto.Marshal(a)
		b = append(b, extra...)
		_, err := owner.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_Admin{Admin: &wire.SignedAction{
			Action: b, Signature: owner.Identity.Sign(identity.CtxAdminAction, b),
		}}})
		return err
	}
	nick := func(n int) *wire.AdminAction {
		return &wire.AdminAction{Nonce: make([]byte, n), Op: &wire.AdminAction_SetNickname{SetNickname: &wire.SetNickname{Member: owner.Identity.Public, Nickname: "O"}}}
	}
	wantErr(t, send(nick(65), nil), "16 to 64")
	wantErr(t, send(nick(16), []byte{0xf8, 0x7f, 0x01}), "too large") // an unknown varint field (2047)
	big := nick(16)
	big.Op.(*wire.AdminAction_SetNickname).SetNickname.Nickname = strings.Repeat("x", 600<<10)
	wantErr(t, send(big, nil), "too large")
	if err := send(nick(64), nil); err != nil {
		t.Fatalf("a plain action: %v", err)
	}
}

func (e *env) mustAdmin(m *testutil.Member, a *wire.AdminAction) *wire.AdminResult {
	e.t.Helper()
	r, err := e.adminAs(m, a)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

// role creates a role with perms and gives it to the members.
func (e *env) role(name string, perms uint64, members ...*testutil.Member) int64 {
	e.t.Helper()
	id := e.mustAdmin(e.owner, &wire.AdminAction{Op: &wire.AdminAction_CreateRole{CreateRole: &wire.CreateRole{Name: name, Permissions: perms}}}).Id
	for _, m := range members {
		e.mustAdmin(e.owner, &wire.AdminAction{Op: &wire.AdminAction_AssignRoles{AssignRoles: &wire.AssignRoles{Member: m.Identity.Public, RoleIds: []int64{id}}}})
	}
	return id
}

// fresh fetches a member's current state without touching m.State, which
// the member's reader goroutine updates when other members' actions push.
func fresh(t *testing.T, m *testutil.Member) *wire.SyncState {
	t.Helper()
	r, err := m.Call(testutil.Ctx(t), &wire.ClientMessage{Body: &wire.ClientMessage_Sync{Sync: &wire.Sync{}}})
	if err != nil {
		t.Fatal(err)
	}
	return r.GetState()
}

// set changes the community settings from their current values.
func (e *env) set(mut func(*wire.CommunitySettings)) {
	e.t.Helper()
	s := cloneSettings(fresh(e.t, e.owner).Settings)
	mut(s)
	e.mustAdmin(e.owner, &wire.AdminAction{Op: &wire.AdminAction_UpdateSettings{UpdateSettings: s}})
}

func wantErr(t *testing.T, err error, substr string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), substr) {
		t.Fatalf("want an error containing %q, got %v", substr, err)
	}
}

func timeoutOp(m *testutil.Member, secs uint32) *wire.AdminAction {
	return &wire.AdminAction{Op: &wire.AdminAction_Timeout{Timeout: &wire.TimeoutMember{Member: m.Identity.Public, Seconds: secs, Reason: "cool off"}}}
}

func historyMsgs(t *testing.T, m *testutil.Member, ch int64) []*wire.Message {
	t.Helper()
	r, err := m.Call(testutil.Ctx(t), &wire.ClientMessage{Body: &wire.ClientMessage_History{History: &wire.History{ChannelId: ch, Limit: 200}}})
	if err != nil {
		t.Fatal(err)
	}
	return r.GetMessages().Messages
}

func search(t *testing.T, m *testutil.Member, q string) []*wire.Message {
	t.Helper()
	r, err := m.Call(testutil.Ctx(t), &wire.ClientMessage{Body: &wire.ClientMessage_Search{Search: &wire.SearchQuery{Query: q}}})
	if err != nil {
		t.Fatal(err)
	}
	return r.GetSearchResults().Messages
}

func members(t *testing.T, m *testutil.Member) map[string]*wire.Member {
	t.Helper()
	r, err := m.Call(testutil.Ctx(t), &wire.ClientMessage{Body: &wire.ClientMessage_ListMembers{ListMembers: &wire.ListMembers{}}})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*wire.Member{}
	for _, x := range r.GetMembers().Members {
		out[x.DisplayName] = x
	}
	return out
}

// fixedClock pins the server's clock at the returned pointer's value.
func fixedClock(e *env, at time.Time) *time.Time {
	t := at
	e.comm.Server.SetClock(func() time.Time { return t })
	return &t
}

// ---------------------------------------------------------------- kick, ban and hierarchy

func TestBanDeletesRecentHistory(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	gen := e.owner.Channel("general").Id
	now := time.Now()
	clock := fixedClock(e, now.Add(-25*time.Hour))
	old, err := alice.Post(ctx, gen, "an old wallaby remark")
	if err != nil {
		t.Fatal(err)
	}
	*clock = now.Add(-23 * time.Hour)
	recent1, _ := alice.Post(ctx, gen, "a recent wallaby remark")
	*clock = now
	recent2, _ := alice.Post(ctx, gen, "another fresh wallaby remark")
	mine, _ := e.owner.Post(ctx, gen, "the owner's wallaby remark")

	// Only the listed windows are accepted.
	_, err = e.adminAs(e.owner, &wire.AdminAction{Op: &wire.AdminAction_Ban{Ban: &wire.BanMember{Member: alice.Identity.Public, DeleteMessageSeconds: 1234}}})
	wantErr(t, err, "message history can be deleted")
	res := e.mustAdmin(e.owner, &wire.AdminAction{Op: &wire.AdminAction_Ban{Ban: &wire.BanMember{Member: alice.Identity.Public, Reason: "spam", DeleteMessageSeconds: 86400}}})
	if res.Deleted != 2 {
		t.Fatalf("deleted %d, want 2", res.Deleted)
	}
	byID := map[int64]*wire.Message{}
	for _, m := range historyMsgs(t, e.owner, gen) {
		byID[m.Id] = m
	}
	if !byID[recent1.Id].Deleted || !byID[recent2.Id].Deleted || byID[recent1.Id].Text != "" {
		t.Fatal("recent messages weren't deleted")
	}
	if byID[old.Id].Deleted || byID[mine.Id].Deleted {
		t.Fatal("the ban deleted messages outside its window or by someone else")
	}
	// Gone from search too; the old one is still found.
	hits := search(t, e.owner, "wallaby")
	if len(hits) != 2 {
		t.Fatalf("search after ban: %d hits", len(hits))
	}
	for _, h := range hits {
		if h.Id == recent1.Id || h.Id == recent2.Id {
			t.Fatal("a deleted message is still searchable")
		}
	}
	r, _ := e.owner.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_AuditLog{AuditLog: &wire.AuditQuery{Action: "ban"}}})
	if s := r.GetAudit().Entries[0].Summary; !strings.Contains(s, "deleted 2 messages from the last 24 hours") {
		t.Fatalf("audit: %q", s)
	}
}

// The index of who posted what in the last week is rebuilt at startup, so
// a ban after a restart still finds the member's recent messages.
func TestBanHistoryAfterRestart(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	gen := e.owner.Channel("general").Id
	m1, _ := alice.Post(ctx, gen, "first")
	m2, _ := alice.Post(ctx, gen, "second")
	e.owner.Close()
	alice.Close()
	e.comm.Restart(t, e.relay.URL)
	owner := testutil.NewMember(t, "Owner")
	owner.Identity, owner.Static = e.owner.Identity, e.owner.Static
	if err := owner.Connect(ctx, e.relay.URL, e.serverKey()); err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	e.owner = owner
	if n := e.mustAdmin(owner, &wire.AdminAction{Op: &wire.AdminAction_Ban{Ban: &wire.BanMember{Member: alice.Identity.Public, DeleteMessageSeconds: 3600}}}).Deleted; n != 2 {
		t.Fatalf("deleted %d after restart", n)
	}
	for _, m := range historyMsgs(t, owner, gen) {
		if (m.Id == m1.Id || m.Id == m2.Id) && !m.Deleted {
			t.Fatal("a message survived the ban")
		}
	}
}

func TestKickBanTimeoutHierarchy(t *testing.T) {
	e := setup(t, nil)
	mod := e.member("Mod", e.invite.Secret)
	peer := e.member("Peer", e.invite.Secret)
	low := e.member("Low", e.invite.Secret)
	perms := community.PermKick | community.PermBan | community.PermTimeoutMembers
	e.role("Moderators", perms, mod, peer)
	e.role("Regulars", 0, low) // below Moderators (new roles go just above @everyone)
	for name, op := range map[string]*wire.AdminAction{
		"kick":    {Op: &wire.AdminAction_Kick{Kick: &wire.Kick{Member: peer.Identity.Public}}},
		"ban":     {Op: &wire.AdminAction_Ban{Ban: &wire.BanMember{Member: peer.Identity.Public}}},
		"timeout": timeoutOp(peer, 60),
	} {
		_, err := e.adminAs(mod, op)
		if err == nil || !strings.Contains(err.Error(), "below you") {
			t.Fatalf("%s of a peer: %v", name, err)
		}
	}
	for _, op := range []*wire.AdminAction{
		{Op: &wire.AdminAction_Kick{Kick: &wire.Kick{Member: e.owner.Identity.Public}}},
		{Op: &wire.AdminAction_Ban{Ban: &wire.BanMember{Member: e.owner.Identity.Public}}},
		timeoutOp(e.owner, 60),
	} {
		if _, err := e.adminAs(mod, op); err == nil {
			t.Fatal("a moderator acted on the owner")
		}
	}
	// Members without the permission are refused.
	_, err := e.adminAs(low, timeoutOp(mod, 60))
	wantErr(t, err, "permission")
	// Below them works.
	e.mustAdmin(mod, timeoutOp(low, 60))
	e.mustAdmin(mod, &wire.AdminAction{Op: &wire.AdminAction_Kick{Kick: &wire.Kick{Member: low.Identity.Public}}})
}

// ---------------------------------------------------------------- timeouts

func TestTimeout(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	bob := e.member("Bob", e.invite.Secret)
	gen := e.owner.Channel("general").Id
	before, _ := e.owner.Post(ctx, gen, "before the timeout")

	// Bounds.
	_, err := e.adminAs(e.owner, timeoutOp(alice, 59))
	wantErr(t, err, "60 seconds to 28 days")
	_, err = e.adminAs(e.owner, timeoutOp(alice, community.MaxTimeoutSeconds+1))
	wantErr(t, err, "60 seconds to 28 days")
	_, err = e.adminAs(e.owner, timeoutOp(e.owner, 60))
	wantErr(t, err, "yourself")

	start := time.Now()
	clock := fixedClock(e, start)
	e.mustAdmin(e.owner, timeoutOp(alice, 600))
	until := start.UnixMilli() + 600_000

	// Can't post, edit, attach, change nickname; can read and report.
	_, err = alice.Post(ctx, gen, "let me speak")
	wantErr(t, err, "timed out until")
	_, err = alice.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_UploadBegin{UploadBegin: &wire.UploadBegin{ChannelId: gen, Name: "x.txt", Size: 3}}})
	wantErr(t, err, "timed out until")
	_, err = alice.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_SetProfile{SetProfile: &wire.SetProfile{DisplayName: "Not Alice"}}})
	wantErr(t, err, "nickname")
	if len(historyMsgs(t, alice, gen)) == 0 {
		t.Fatal("a timed-out member can't read")
	}
	if _, err := alice.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_ReportMessage{ReportMessage: &wire.ReportMessage{MessageId: before.Id}}}); err != nil {
		t.Fatalf("a timed-out member can't report: %v", err)
	}

	// The member and moderators see the end time; other members don't.
	ast := fresh(t, alice)
	if ast.Self.TimeoutUntil != until {
		t.Fatalf("own timeout %d, want %d", ast.Self.TimeoutUntil, until)
	}
	var ch *wire.Channel
	for _, c := range ast.Channels {
		if c.Id == gen {
			ch = c
		}
	}
	if ch.Permissions&(community.PermSendMessages|community.PermSpeak) != 0 || ch.Permissions&community.PermViewChannel == 0 {
		t.Fatalf("timed-out channel permissions %b", ch.Permissions)
	}
	if got := members(t, e.owner)["Alice"].TimeoutUntil; got != until {
		t.Fatalf("moderator sees %d", got)
	}
	if got := members(t, bob)["Alice"].TimeoutUntil; got != 0 {
		t.Fatal("a member without Timeout Members sees the timeout")
	}

	// Boundary: still timed out 1 ms before the end, free at the end.
	*clock = time.UnixMilli(until - 1)
	_, err = alice.Post(ctx, gen, "too early")
	wantErr(t, err, "timed out")
	*clock = time.UnixMilli(until)
	if _, err := alice.Post(ctx, gen, "back again"); err != nil {
		t.Fatalf("after the timeout: %v", err)
	}

	// Lifting a timeout early.
	*clock = time.Now()
	e.mustAdmin(e.owner, timeoutOp(alice, 3600))
	e.mustAdmin(e.owner, timeoutOp(alice, 0))
	if _, err := alice.Post(ctx, gen, "lifted"); err != nil {
		t.Fatalf("after lifting: %v", err)
	}
	r, _ := e.owner.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_AuditLog{AuditLog: &wire.AuditQuery{Action: "timeout"}}})
	got := r.GetAudit().Entries
	if len(got) != 3 || !strings.Contains(got[0].Summary, "removed the timeout of Alice") ||
		!strings.Contains(got[2].Summary, "timed out Alice for 10 minutes: cool off") || string(got[0].Target) != string(alice.Identity.Public) {
		t.Fatalf("timeout audit: %v", got)
	}
}

// ---------------------------------------------------------------- bulk delete

func TestBulkDelete(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	gen := e.owner.Channel("general").Id
	other := e.mustAdmin(e.owner, &wire.AdminAction{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: "other"}}}).Id
	var ids []int64
	for i := 0; i < 5; i++ {
		m, err := alice.Post(ctx, gen, "bulk message")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.Id)
	}
	elsewhere, _ := e.owner.Post(ctx, other, "elsewhere")
	bulk := func(b *wire.BulkDelete) *wire.AdminAction {
		return &wire.AdminAction{Op: &wire.AdminAction_BulkDelete{BulkDelete: b}}
	}
	_, err := e.adminAs(alice, bulk(&wire.BulkDelete{ChannelId: gen, Last: 2}))
	wantErr(t, err, "can't delete")
	many := make([]int64, 101)
	_, err = e.adminAs(e.owner, bulk(&wire.BulkDelete{ChannelId: gen, MessageIds: many}))
	wantErr(t, err, "at most 100")
	_, err = e.adminAs(e.owner, bulk(&wire.BulkDelete{ChannelId: gen, Last: 101}))
	wantErr(t, err, "at most 100")
	_, err = e.adminAs(e.owner, bulk(&wire.BulkDelete{ChannelId: gen, MessageIds: []int64{ids[0], elsewhere.Id}}))
	wantErr(t, err, "isn't in #general")

	if n := e.mustAdmin(e.owner, bulk(&wire.BulkDelete{ChannelId: gen, Last: 3})).Deleted; n != 3 {
		t.Fatalf("deleted %d", n)
	}
	if n := e.mustAdmin(e.owner, bulk(&wire.BulkDelete{ChannelId: gen, MessageIds: []int64{ids[0], ids[4]}})).Deleted; n != 1 {
		t.Fatalf("second bulk delete removed %d (one was already gone)", n)
	}
	deleted := 0
	for _, m := range historyMsgs(t, e.owner, gen) {
		if m.Deleted {
			deleted++
		}
	}
	if deleted != 4 {
		t.Fatalf("%d deleted", deleted)
	}
	r, _ := e.owner.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_AuditLog{AuditLog: &wire.AuditQuery{Action: "bulk_delete"}}})
	if es := r.GetAudit().Entries; len(es) != 2 || es[1].Summary != "bulk deleted 3 messages in #general" {
		t.Fatalf("bulk delete audit: %v", es)
	}
}

// ---------------------------------------------------------------- member screening

func TestMemberScreeningBoundary(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	if _, err := e.adminAs(e.owner, &wire.AdminAction{Op: &wire.AdminAction_UpdateSettings{UpdateSettings: func() *wire.CommunitySettings {
		s := cloneSettings(fresh(t, e.owner).Settings)
		s.ScreeningMinutes = community.MaxScreeningMinutes + 1
		return s
	}()}}); err == nil {
		t.Fatal("screening longer than 7 days accepted")
	}
	e.set(func(s *wire.CommunitySettings) { s.ScreeningMinutes = 10 })
	start := time.Now()
	clock := fixedClock(e, start)
	alice := e.member("Alice", e.invite.Secret)
	vetted := e.member("Vetted", e.invite.Secret)
	e.role("Vetted", 0, vetted)
	gen := e.owner.Channel("general").Id

	_, err := alice.Post(ctx, gen, "hello")
	wantErr(t, err, "New members can post from")
	_, err = alice.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_GetContactCard{GetContactCard: &wire.GetContactCard{Member: e.owner.Identity.Public}}})
	wantErr(t, err, "start direct messages from")
	if want := start.UnixMilli() + 600_000; fresh(t, alice).Self.CanPostAt != want {
		t.Fatalf("can_post_at %d, want %d", fresh(t, alice).Self.CanPostAt, want)
	}
	if _, err := vetted.Post(ctx, gen, "members with a role skip screening"); err != nil {
		t.Fatal(err)
	}
	*clock = start.Add(10*time.Minute - time.Millisecond)
	_, err = alice.Post(ctx, gen, "almost")
	wantErr(t, err, "member screening")
	*clock = start.Add(10 * time.Minute)
	if _, err := alice.Post(ctx, gen, "now I can"); err != nil {
		t.Fatalf("at the boundary: %v", err)
	}
	// Turning it off frees new members at once.
	*clock = time.Now()
	e.set(func(s *wire.CommunitySettings) { s.ScreeningMinutes = 60 })
	carol := e.member("Carol", e.invite.Secret)
	_, err = carol.Post(ctx, gen, "hi")
	wantErr(t, err, "member screening")
	e.set(func(s *wire.CommunitySettings) { s.ScreeningMinutes = 0 })
	if _, err := carol.Post(ctx, gen, "hi"); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------- AutoMod

func saveRule(e *env, r *wire.AutoModRule) (int64, error) {
	res, err := e.adminAs(e.owner, &wire.AdminAction{Op: &wire.AdminAction_SaveAutomodRule{SaveAutomodRule: &wire.SaveAutoModRule{Rule: r}}})
	if err != nil {
		return 0, err
	}
	return res.Id, nil
}

func TestAutoModRuleValidation(t *testing.T) {
	e := setup(t, nil)
	kw := wire.AutoModTrigger_AUTOMOD_TRIGGER_KEYWORD
	long := strings.Repeat("a", 261)
	for _, c := range []struct {
		rule *wire.AutoModRule
		want string
	}{
		{&wire.AutoModRule{Name: "", Trigger: kw, Keywords: []string{"x"}, Block: true}, "names"},
		{&wire.AutoModRule{Name: "n", Trigger: kw, Keywords: []string{"x"}}, "at least one action"},
		{&wire.AutoModRule{Name: "n", Block: true}, "choose what the rule looks for"},
		{&wire.AutoModRule{Name: "n", Trigger: kw, Block: true}, "needs keywords"},
		{&wire.AutoModRule{Name: "n", Trigger: kw, Keywords: []string{"a*b"}, Block: true}, "only allowed at the start or end"},
		{&wire.AutoModRule{Name: "n", Trigger: kw, Keywords: []string{strings.Repeat("k", 61)}, Block: true}, "at most 60"},
		{&wire.AutoModRule{Name: "n", Trigger: kw, Keywords: make([]string, 1001), Block: true}, "at most 1000 keywords"},
		{&wire.AutoModRule{Name: "n", Trigger: kw, Patterns: []string{"(unclosed"}, Block: true}, "missing closing"},
		{&wire.AutoModRule{Name: "n", Trigger: kw, Patterns: []string{long}, Block: true}, "longer than 260"},
		{&wire.AutoModRule{Name: "n", Trigger: kw, Patterns: []string{"x*"}, Block: true}, "matches every message"},
		{&wire.AutoModRule{Name: "n", Trigger: kw, Patterns: make([]string, 11), Block: true}, "at most 10 regular"},
		{&wire.AutoModRule{Name: "n", Trigger: kw, Patterns: []string{`(?:\p{L}{100}){10}`}, Block: true}, "too complex"},
		{&wire.AutoModRule{Name: "n", Trigger: kw, Keywords: []string{"x"}, TimeoutSeconds: 30}, "60 seconds"},
		{&wire.AutoModRule{Name: "n", Trigger: wire.AutoModTrigger_AUTOMOD_TRIGGER_MENTION_SPAM, Block: true}, "mention limit"},
		{&wire.AutoModRule{Name: "n", Trigger: wire.AutoModTrigger_AUTOMOD_TRIGGER_LINKS, AllowedDomains: []string{"not a domain"}, Block: true}, "isn't a domain"},
		{&wire.AutoModRule{Name: "n", Trigger: kw, Keywords: []string{"x"}, Block: true, ExemptRoleIds: []int64{999}}, "exempt role"},
	} {
		_, err := saveRule(e, c.rule)
		wantErr(t, err, c.want)
	}
	// The program-size budget covers all rules together.
	big := `[a-z][0-9][a-z][0-9][a-z][0-9][a-z][0-9][a-z][0-9][a-z][0-9][a-z]{1,40}`
	var saved int
	var err error
	for i := 0; i < 20 && err == nil; i++ {
		_, err = saveRule(e, &wire.AutoModRule{Name: "big", Trigger: kw, Patterns: []string{big, big + "x", big + "y", big + "z"}, Block: true})
		if err == nil {
			saved++
		}
	}
	wantErr(t, err, "too complex together")
	if saved == 0 {
		t.Fatal("the budget refused the first rule")
	}
}

func TestAutoModActions(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	mod := e.member("Mod", e.invite.Secret)
	trusted := e.member("Trusted", e.invite.Secret)
	e.role("Mods", community.PermManageMessages, mod)
	trustedRole := e.role("Trusted", 0, trusted)
	gen := e.owner.Channel("general").Id
	quiet := e.mustAdmin(e.owner, &wire.AdminAction{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: "anything-goes"}}}).Id
	kw := wire.AutoModTrigger_AUTOMOD_TRIGGER_KEYWORD

	if _, err := saveRule(e, &wire.AutoModRule{Name: "Words", Enabled: true, Trigger: kw, Keywords: []string{"badword", "spam*", "two words"},
		Patterns: []string{`fr[e3]{2}\s+mon[e3]y`}, Block: true, ExemptRoleIds: []int64{trustedRole}, ExemptChannelIds: []int64{quiet}}); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"a BADWORD here", "spammer!", "these two  words", "get FR33 money"} {
		_, err := alice.Post(ctx, gen, text)
		wantErr(t, err, "AutoMod blocked your message")
	}
	for _, text := range []string{"badwords is not the word", "antispam", "two", "free"} {
		if _, err := alice.Post(ctx, gen, text); err != nil {
			t.Fatalf("%q: %v", text, err)
		}
	}
	// Exempt channel and role.
	if _, err := alice.Post(ctx, quiet, "badword"); err != nil {
		t.Fatalf("exempt channel: %v", err)
	}
	if _, err := trusted.Post(ctx, gen, "badword"); err != nil {
		t.Fatalf("exempt role: %v", err)
	}
	// Edits are checked too; the original stays.
	ok, _ := alice.Post(ctx, gen, "harmless")
	_, err := alice.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_Edit{Edit: &wire.Edit{MessageId: ok.Id, Text: "now a badword"}}})
	wantErr(t, err, "AutoMod blocked")
	// File captions too.
	_, err = alice.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_UploadBegin{UploadBegin: &wire.UploadBegin{ChannelId: gen, Name: "f.txt", Size: 3, Text: "badword"}}})
	wantErr(t, err, "AutoMod blocked")

	// Mention spam, alert only: the message posts and moderators get an alert.
	if _, err := saveRule(e, &wire.AutoModRule{Name: "Mentions", Enabled: true, Trigger: wire.AutoModTrigger_AUTOMOD_TRIGGER_MENTION_SPAM, MentionLimit: 2, Alert: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.Post(ctx, gen, "@a @b @a"); err != nil {
		t.Fatal(err)
	}
	spam, err := alice.Post(ctx, gen, "@a @b @c hello")
	if err != nil {
		t.Fatalf("alert-only rule blocked: %v", err)
	}
	alert := waitAlert(t, mod)
	if alert.Kind != wire.ReportKind_REPORT_KIND_AUTOMOD || alert.MessageId != spam.Id || alert.RuleName != "Mentions" ||
		string(alert.Author) != string(alice.Identity.Public) || alert.AutomodAction != "alerted" {
		t.Fatalf("alert: %v", alert)
	}
	for quiet := time.After(300 * time.Millisecond); quiet != nil; {
		select {
		case ev := <-alice.Events:
			if ev.GetModAlert() != nil {
				t.Fatal("a member without Manage Messages got a moderator alert")
			}
		case <-quiet:
			quiet = nil
		}
	}

	// Links: block and time out, with an allowlist.
	if _, err := saveRule(e, &wire.AutoModRule{Name: "Links", Enabled: true, Trigger: wire.AutoModTrigger_AUTOMOD_TRIGGER_LINKS,
		AllowedDomains: []string{"example.org"}, Block: true, Alert: true, TimeoutSeconds: 600}); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.Post(ctx, gen, "see https://docs.example.org/page and www.example.org"); err != nil {
		t.Fatalf("allowed link: %v", err)
	}
	_, err = alice.Post(ctx, gen, "buy at http://user@evil.example.com:8080/x")
	wantErr(t, err, "links to a site")
	blocked := waitAlert(t, mod)
	if blocked.MessageId != 0 || !strings.Contains(blocked.AutomodAction, "blocked, timed out for 10 minutes") {
		t.Fatalf("blocked alert: %v", blocked)
	}
	_, err = alice.Post(ctx, gen, "anything")
	wantErr(t, err, "timed out until")
	r, _ := e.owner.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_AuditLog{AuditLog: &wire.AuditQuery{Action: "automod"}}})
	es := r.GetAudit().Entries
	if len(es) != 1 || !es[0].System || es[0].ActorName != "AutoMod" || string(es[0].Target) != string(alice.Identity.Public) ||
		!strings.Contains(es[0].Summary, "“Links” timed out Alice for 10 minutes") {
		t.Fatalf("system audit: %v", es)
	}
	// Disabled rules don't fire; rule changes are audited.
	list, _ := e.owner.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_ListAutomodRules{ListAutomodRules: &wire.ListAutoModRules{}}})
	rules := list.GetAutomodRules().Rules
	if len(rules) != 3 {
		t.Fatalf("%d rules", len(rules))
	}
	words := rules[0]
	words.Enabled = false
	if _, err := saveRule(e, words); err != nil {
		t.Fatal(err)
	}
	if _, err := trusted.Post(ctx, quiet, "badword"); err != nil {
		t.Fatal(err)
	}
	if _, err := mod.Post(ctx, gen, "badword is fine now"); err != nil {
		t.Fatalf("disabled rule fired: %v", err)
	}
	if _, err := e.adminAs(e.owner, &wire.AdminAction{Op: &wire.AdminAction_DeleteAutomodRule{DeleteAutomodRule: &wire.DeleteAutoModRule{Id: words.Id}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := mod.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_ListAutomodRules{ListAutomodRules: &wire.ListAutoModRules{}}}); err == nil {
		t.Fatal("a member without Manage Community listed the rules")
	}
	r, _ = e.owner.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_AuditLog{AuditLog: &wire.AuditQuery{Action: "save_automod_rule"}}})
	if n := len(r.GetAudit().Entries); n != 4 {
		t.Fatalf("%d rule-change audit entries", n)
	}
	if s := r.GetAudit().Entries[0].Summary; s != "edited AutoMod rule “Words” (keywords, disabled)" {
		t.Fatalf("audit: %q", s)
	}
}

func waitAlert(t *testing.T, m *testutil.Member) *wire.Report {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-m.Events:
			if a := ev.GetModAlert(); a != nil {
				return a.Report
			}
		case <-deadline:
			t.Fatal("no moderator alert")
		}
	}
}

// ---------------------------------------------------------------- reports

func report(m *testutil.Member, id int64, reason string) error {
	_, err := m.Call(context.Background(), &wire.ClientMessage{Body: &wire.ClientMessage_ReportMessage{ReportMessage: &wire.ReportMessage{MessageId: id, Reason: reason}}})
	return err
}

func TestReports(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	bob := e.member("Bob", e.invite.Secret)
	mod := e.member("Mod", e.invite.Secret)
	e.role("Mods", community.PermManageMessages, mod)
	gen := e.owner.Channel("general").Id
	var msgs []*wire.Message
	for i := 0; i < 7; i++ {
		m, _ := bob.Post(ctx, gen, "rude thing")
		msgs = append(msgs, m)
	}
	own, _ := alice.Post(ctx, gen, "mine")

	wantErr(t, report(alice, msgs[0].Id, strings.Repeat("r", 501)), "at most 500")
	wantErr(t, report(alice, own.Id, ""), "your own message")
	wantErr(t, report(alice, 999999, ""), "no such message")
	if err := report(alice, msgs[0].Id, "this is rude"); err != nil {
		t.Fatal(err)
	}
	if a := waitAlert(t, mod); a.Reason != "this is rude" || a.ReporterName != "Alice" || a.MessageId != msgs[0].Id {
		t.Fatalf("alert: %v", a)
	}
	wantErr(t, report(alice, msgs[0].Id, "again"), "already reported")
	for i := 1; i < 4; i++ {
		if err := report(alice, msgs[i].Id, ""); err != nil {
			t.Fatal(err)
		}
	}
	// The duplicate counted toward the rate limit: 5 per 10 minutes.
	wantErr(t, report(alice, msgs[5].Id, ""), "at most 5 messages every 10 minutes")
	if err := report(mod, msgs[5].Id, "mod reports too"); err != nil {
		t.Fatal(err)
	}

	list := func(m *testutil.Member, closed bool) ([]*wire.Report, error) {
		r, err := m.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_ListReports{ListReports: &wire.ListReports{IncludeClosed: closed}}})
		if err != nil {
			return nil, err
		}
		return r.GetReports().Reports, nil
	}
	if _, err := list(alice, false); err == nil {
		t.Fatal("a member read the report queue")
	}
	open, err := list(mod, false)
	if err != nil || len(open) != 5 {
		t.Fatalf("open reports: %d %v", len(open), err)
	}
	first := open[len(open)-1]
	if first.Reason != "this is rude" || first.ReporterName != "Alice" || first.AuthorName != "Bob" || first.ChannelId != gen {
		t.Fatalf("report: %v", first)
	}
	resolve := func(id int64, dismiss bool) error {
		_, err := e.adminAs(mod, &wire.AdminAction{Op: &wire.AdminAction_ResolveReport{ResolveReport: &wire.ResolveReport{Id: id, Dismiss: dismiss}}})
		return err
	}
	if err := resolve(first.Id, false); err != nil {
		t.Fatal(err)
	}
	wantErr(t, resolve(first.Id, true), "already closed")
	if err := resolve(open[0].Id, true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.adminAs(alice, &wire.AdminAction{Op: &wire.AdminAction_ResolveReport{ResolveReport: &wire.ResolveReport{Id: open[1].Id}}}); err == nil {
		t.Fatal("a member resolved a report")
	}
	open, _ = list(mod, false)
	all, _ := list(mod, true)
	if len(open) != 3 || len(all) != 5 {
		t.Fatalf("after resolving: %d open, %d total", len(open), len(all))
	}
	for _, r := range all {
		if r.Id == first.Id && (r.Status != wire.ReportStatus_REPORT_STATUS_RESOLVED || r.ResolvedByName != "Mod") {
			t.Fatalf("resolved report: %v", r)
		}
	}
	// Reports follow retention: they go with their month's key.
	if _, _, err := e.comm.Server.Purge(ctx, time.Now().AddDate(0, 3, 0)); err != nil {
		t.Fatal(err)
	}
	if all, _ = list(mod, true); len(all) != 0 {
		t.Fatalf("%d reports outlived retention", len(all))
	}
}

// ---------------------------------------------------------------- audit filters

func TestAuditFilters(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	bob := e.member("Bob", e.invite.Secret)
	mod := e.member("Mod", e.invite.Secret)
	e.role("Mods", community.PermKick|community.PermTimeoutMembers|community.PermViewAuditLog, mod)
	time.Sleep(5 * time.Millisecond)
	start := time.Now()
	e.mustAdmin(mod, timeoutOp(alice, 60))
	e.mustAdmin(mod, timeoutOp(bob, 60))
	e.mustAdmin(e.owner, timeoutOp(bob, 120))
	mid := time.Now()
	time.Sleep(5 * time.Millisecond)
	e.mustAdmin(mod, &wire.AdminAction{Op: &wire.AdminAction_Kick{Kick: &wire.Kick{Member: alice.Identity.Public}}})
	for i := 0; i < 5; i++ {
		e.mustAdmin(e.owner, &wire.AdminAction{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: "c" + string(rune('a'+i))}}})
	}
	q := func(aq *wire.AuditQuery) *wire.AuditList {
		t.Helper()
		r, err := mod.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_AuditLog{AuditLog: aq}})
		if err != nil {
			t.Fatal(err)
		}
		return r.GetAudit()
	}
	if n := len(q(&wire.AuditQuery{Action: "timeout"}).Entries); n != 3 {
		t.Fatalf("by action: %d", n)
	}
	if es := q(&wire.AuditQuery{Actor: mod.Identity.Public}).Entries; len(es) != 3 || es[0].ActionName != "kick" {
		t.Fatalf("by actor: %v", es)
	}
	if es := q(&wire.AuditQuery{Target: bob.Identity.Public}).Entries; len(es) != 2 || es[0].TargetName != "Bob" {
		t.Fatalf("by target: %v", es)
	}
	if n := len(q(&wire.AuditQuery{Action: "timeout", Actor: mod.Identity.Public, Target: bob.Identity.Public}).Entries); n != 1 {
		t.Fatalf("combined: %d", n)
	}
	if n := len(q(&wire.AuditQuery{Since: start.UnixMilli() - 1, Until: mid.UnixMilli() + 1}).Entries); n != 3 {
		t.Fatalf("date range: %d", n)
	}
	if n := len(q(&wire.AuditQuery{Since: mid.UnixMilli() + 1}).Entries); n != 6 {
		t.Fatalf("since: %d", n)
	}
	if _, err := mod.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_AuditLog{AuditLog: &wire.AuditQuery{Since: 10, Until: 5}}}); err == nil {
		t.Fatal("an inverted range was accepted")
	}
	// Pagination: pages continue where the last ended.
	p1 := q(&wire.AuditQuery{Limit: 4})
	if len(p1.Entries) != 4 || !p1.More {
		t.Fatalf("page 1: %d more=%v", len(p1.Entries), p1.More)
	}
	p2 := q(&wire.AuditQuery{Limit: 100, BeforeId: p1.NextBeforeId})
	if p2.More || len(p1.Entries)+len(p2.Entries) != len(q(&wire.AuditQuery{}).Entries) || p2.Entries[0].Id >= p1.Entries[3].Id {
		t.Fatalf("page 2: %d more=%v", len(p2.Entries), p2.More)
	}
}

// ---------------------------------------------------------------- quotas

func TestInviteQuota(t *testing.T) {
	old := community.MaxActiveJoinKeys
	community.MaxActiveJoinKeys = 3
	t.Cleanup(func() { community.MaxActiveJoinKeys = old })
	e := setup(t, nil) // the first invite counts
	a := e.newInvite(nil)
	e.newInvite(nil)
	_, err := e.adminAs(e.owner, &wire.AdminAction{Op: &wire.AdminAction_CreateJoinKey{CreateJoinKey: &wire.CreateJoinKey{Label: "fourth"}}})
	wantErr(t, err, "at most 3 invites")
	e.mustAdmin(e.owner, &wire.AdminAction{Op: &wire.AdminAction_RevokeJoinKey{RevokeJoinKey: &wire.RevokeJoinKey{Id: a.Id}}})
	e.newInvite(nil)
}

// ---------------------------------------------------------------- custom emoji

func pngOf(t *testing.T, w, h int, extra string) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return append(b.Bytes(), extra...)
}

func uploadEmoji(m *testutil.Member, community []byte, name string, img []byte) (*wire.AdminResult, error) {
	sum := sha256.Sum256(img)
	a := &wire.AdminAction{Community: community, Actor: m.Identity.Public, At: time.Now().UnixMilli(), Nonce: make([]byte, 16),
		Op: &wire.AdminAction_AddEmoji{AddEmoji: &wire.AddEmoji{Name: name, Sha256: sum[:], Size: uint32(len(img))}}}
	rand.Read(a.Nonce)
	b, _ := proto.Marshal(a)
	r, err := m.Call(context.Background(), &wire.ClientMessage{Body: &wire.ClientMessage_EmojiUpload{EmojiUpload: &wire.EmojiUpload{
		Action: &wire.SignedAction{Action: b, Signature: m.Identity.Sign(identity.CtxAdminAction, b)}, Image: img,
	}}})
	if err != nil {
		return nil, err
	}
	return r.GetAdminResult(), nil
}

func TestCustomEmoji(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	keeper := e.member("Keeper", e.invite.Secret)
	e.role("Emoji keepers", community.PermManageEmoji, keeper)
	gen := e.owner.Channel("general").Id
	img := pngOf(t, 64, 64, "")

	_, err := uploadEmoji(alice, e.id(), "party", img)
	wantErr(t, err, "permission")
	for _, c := range []struct {
		name string
		img  []byte
		want string
	}{
		{"x", img, "2 to 32 lowercase"},
		{"Party", img, "2 to 32 lowercase"},
		{"party", pngOf(t, 129, 64, ""), "at most 128×128"},
		{"party", pngOf(t, 64, 64, strings.Repeat("z", community.MaxEmojiBytes)), "at most 256 KB"},
		{"party", []byte("\xff\xd8\xff\xe0 a jpeg"), "PNG, GIF or WebP"},
		{"party", []byte("\x89PNG\r\n\x1a\nbroken"), "damaged"},
	} {
		_, err := uploadEmoji(keeper, e.id(), c.name, c.img)
		wantErr(t, err, c.want)
	}
	// The signed hash must match the image sent.
	sum := sha256.Sum256(img)
	a := &wire.AdminAction{Community: e.id(), Actor: keeper.Identity.Public, At: time.Now().UnixMilli(), Nonce: []byte("0123456789abcdef"),
		Op: &wire.AdminAction_AddEmoji{AddEmoji: &wire.AddEmoji{Name: "swap", Sha256: sum[:], Size: uint32(len(img))}}}
	b, _ := proto.Marshal(a)
	_, err = keeper.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_EmojiUpload{EmojiUpload: &wire.EmojiUpload{
		Action: &wire.SignedAction{Action: b, Signature: keeper.Identity.Sign(identity.CtxAdminAction, b)}, Image: pngOf(t, 32, 32, ""),
	}}})
	wantErr(t, err, "doesn't match")
	// And an add_emoji without its image is refused.
	_, err = e.adminAs(keeper, &wire.AdminAction{Op: &wire.AdminAction_AddEmoji{AddEmoji: &wire.AddEmoji{Name: "bare", Sha256: sum[:], Size: 1}}})
	wantErr(t, err, "emoji upload request")

	res, err := uploadEmoji(keeper, e.id(), "party", img)
	if err != nil {
		t.Fatal(err)
	}
	_, err = uploadEmoji(keeper, e.id(), "party", img)
	wantErr(t, err, "already an emoji called :party:")
	if em := fresh(t, alice).Emoji; len(em) != 1 || em[0].Name != "party" || em[0].Mime != "image/png" {
		t.Fatalf("state emoji: %v", em)
	}
	got, err := alice.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_GetEmoji{GetEmoji: &wire.GetEmoji{Id: res.Id}}})
	if err != nil || !bytes.Equal(got.GetEmojiImage().Image, img) {
		t.Fatalf("get emoji: %v", err)
	}
	// Using it in a message: Use Custom Emoji is on for @everyone by default.
	ref := ":party:" + itoa(res.Id)
	if _, err := alice.Post(ctx, gen, "yay "+ref); err != nil {
		t.Fatal(err)
	}
	// Without the permission, a real emoji is refused; look-alike text isn't.
	ev := fresh(t, e.owner).Roles[0]
	e.mustAdmin(e.owner, &wire.AdminAction{Op: &wire.AdminAction_EditRole{EditRole: &wire.EditRole{Id: ev.Id, Permissions: ev.Permissions &^ community.PermUseCustomEmoji}}})
	_, err = alice.Post(ctx, gen, "yay "+ref)
	wantErr(t, err, "can't use custom emoji")
	if _, err := alice.Post(ctx, gen, "meet at 10:30:45 or :nope:12"); err != nil {
		t.Fatalf("look-alike text: %v", err)
	}

	// The count limit.
	e.set(func(s *wire.CommunitySettings) { s.MaxCustomEmoji = 2 })
	if _, err := uploadEmoji(keeper, e.id(), "wave", pngOf(t, 16, 16, "")); err != nil {
		t.Fatal(err)
	}
	_, err = uploadEmoji(keeper, e.id(), "third", img)
	wantErr(t, err, "its limit")
	e.set(func(s *wire.CommunitySettings) { s.MaxCustomEmoji = community.DefaultMaxCustomEmoji })

	// Rename and delete.
	if _, err := e.adminAs(keeper, &wire.AdminAction{Op: &wire.AdminAction_RenameEmoji{RenameEmoji: &wire.RenameEmoji{Id: res.Id, Name: "wave"}}}); err == nil {
		t.Fatal("renamed onto an existing name")
	}
	e.mustAdmin(keeper, &wire.AdminAction{Op: &wire.AdminAction_RenameEmoji{RenameEmoji: &wire.RenameEmoji{Id: res.Id, Name: "fiesta"}}})

	// The toggle: off means unavailable everywhere, with a distinct error.
	e.set(func(s *wire.CommunitySettings) { s.CustomEmojiEnabled = proto.Bool(false) })
	_, err = uploadEmoji(keeper, e.id(), "more", img)
	wantErr(t, err, "RPC_ERROR_CODE_DISABLED: custom emoji are turned off")
	_, err = alice.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_GetEmoji{GetEmoji: &wire.GetEmoji{Id: res.Id}}})
	wantErr(t, err, "turned off")
	_, err = e.owner.Post(ctx, gen, ":fiesta:"+itoa(res.Id))
	wantErr(t, err, "turned off")
	_, err = e.adminAs(keeper, &wire.AdminAction{Op: &wire.AdminAction_RenameEmoji{RenameEmoji: &wire.RenameEmoji{Id: res.Id, Name: "again"}}})
	wantErr(t, err, "turned off")
	if em := fresh(t, alice).Emoji; len(em) != 0 {
		t.Fatalf("state lists emoji while off: %v", em)
	}
	// Deleting still works, for clean-up.
	e.mustAdmin(keeper, &wire.AdminAction{Op: &wire.AdminAction_DeleteEmoji{DeleteEmoji: &wire.DeleteEmoji{Id: res.Id}}})
	r, _ := e.owner.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_AuditLog{AuditLog: &wire.AuditQuery{Action: "update_settings"}}})
	if s := r.GetAudit().Entries[0].Summary; s != "changed turned custom emoji off" {
		t.Fatalf("toggle audit: %q", s)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
