package community_test

import (
	"bytes"
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
