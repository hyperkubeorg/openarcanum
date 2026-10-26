package community_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// With the audit log visible to everyone, an AutoMod rule's keywords and
// patterns (inside its signed action) reach only members with View Audit
// Log; everyone else sees the summary, which names the rule only.
func TestAuditHidesAutoModRuleContents(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	auditor := e.member("Auditor", e.invite.Secret)
	e.role("Auditors", community.PermViewAuditLog, auditor)
	e.set(func(s *wire.CommunitySettings) { s.AuditVisibility = wire.AuditVisibility_AUDIT_VISIBILITY_EVERYONE })
	if _, err := saveRule(e, &wire.AutoModRule{Name: "Words", Enabled: true, Trigger: wire.AutoModTrigger_AUTOMOD_TRIGGER_KEYWORD,
		Keywords: []string{"secretfilterword"}, Block: true}); err != nil {
		t.Fatal(err)
	}
	entries := func(m *testutil.Member) []*wire.AuditEntry {
		r, err := m.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_AuditLog{AuditLog: &wire.AuditQuery{}}})
		if err != nil {
			t.Fatal(err)
		}
		return r.GetAudit().Entries
	}
	leaks := func(m *testutil.Member) (found, ruleListed bool) {
		for _, en := range entries(m) {
			if en.ActionName == "save_automod_rule" {
				ruleListed = true
			}
			if en.Action != nil && bytes.Contains(en.Action.Action, []byte("secretfilterword")) {
				found = true
			}
		}
		return
	}
	if found, listed := leaks(alice); found || !listed {
		t.Fatalf("member: keywords visible %v, rule listed %v", found, listed)
	}
	if found, _ := leaks(auditor); !found {
		t.Fatal("an auditor didn't get the rule's signed action")
	}
}

// With the audit log open to everyone, entries about a channel the viewer
// can't see (its creation, edits, overrides, pins and deletions there) are
// left out for members without View Audit Log; those with it see all.
func TestAuditHidesChannelsViewerCantSee(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	auditor := e.member("Auditor", e.invite.Secret)
	e.role("Auditors", community.PermViewAuditLog, auditor)
	e.set(func(s *wire.CommunitySettings) { s.AuditVisibility = wire.AuditVisibility_AUDIT_VISIBILITY_EVERYONE })
	secret := e.must(&wire.AdminAction{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: "secret-plans"}}}).Id
	e.must(override(&wire.SetChannelOverride{ChannelId: secret, RoleId: e.everyoneID(), Deny: community.PermViewChannel}))
	e.must(editChannel(&wire.EditChannel{Id: secret, Name: "secret-plans", Description: ptr("Nobody else")}))
	m := post(t, e.owner, secret, "hidden")
	if _, err := call(t, e.owner, &wire.Pin{MessageId: m.Id}); err != nil {
		t.Fatal(err)
	}
	e.must(&wire.AdminAction{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: "open-plans"}}})
	about := func(m *testutil.Member, name string) (n int) {
		t.Helper()
		r, err := m.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_AuditLog{AuditLog: &wire.AuditQuery{}}})
		if err != nil {
			t.Fatal(err)
		}
		for _, en := range r.GetAudit().Entries {
			if strings.Contains(en.Summary, name) {
				n++
			}
		}
		return n
	}
	if n := about(alice, "secret-plans"); n != 0 {
		t.Fatalf("a member saw %d entries about a channel they can't see", n)
	}
	if n := about(alice, "open-plans"); n != 1 {
		t.Fatalf("a member saw %d entries about a channel they can see", n)
	}
	if n := about(auditor, "secret-plans"); n != 4 {
		t.Fatalf("an auditor saw %d entries about the hidden channel, want 4", n)
	}
}
