package community_test

import (
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

func openReports(t *testing.T, m *testutil.Member) []*wire.Report {
	t.Helper()
	r, err := m.Call(testutil.Ctx(t), &wire.ClientMessage{Body: &wire.ClientMessage_ListReports{ListReports: &wire.ListReports{}}})
	if err != nil {
		t.Fatal(err)
	}
	return r.GetReports().Reports
}

func alertOnMentions(t *testing.T, e *env) {
	t.Helper()
	if _, err := saveRule(e, &wire.AutoModRule{Name: "Mentions", Enabled: true, Trigger: wire.AutoModTrigger_AUTOMOD_TRIGGER_MENTION_SPAM, MentionLimit: 2, Alert: true}); err != nil {
		t.Fatal(err)
	}
}

// AutoMod alerts are rate limited per author: one member tripping a rule
// over and over doesn't flood the queue (their messages are still handled),
// and others' alerts still arrive.
func TestAutoModAlertsRateLimitedPerAuthor(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	bob := e.member("Bob", e.invite.Secret)
	alertOnMentions(t, e)
	gen := e.owner.Channel("general").Id
	for i := 0; i < 8; i++ {
		if _, err := alice.Post(ctx, gen, "@a @b @c again"); err != nil {
			t.Fatalf("post %d: %v", i, err)
		}
	}
	if n := len(openReports(t, e.owner)); n != 5 {
		t.Fatalf("%d alert reports for one author, want 5", n)
	}
	if _, err := bob.Post(ctx, gen, "@a @b @c hi"); err != nil {
		t.Fatal(err)
	}
	reps := openReports(t, e.owner)
	if len(reps) != 6 || string(reps[0].Author) != string(bob.Identity.Public) {
		t.Fatalf("another author's alert: %d reports", len(reps))
	}
}

// AutoMod alerts respect the queue's open-report cap, like member reports.
func TestAutoModAlertsRespectOpenReportCap(t *testing.T) {
	t.Cleanup(community.SetMaxOpenReports(3))
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	bob := e.member("Bob", e.invite.Secret)
	alertOnMentions(t, e)
	gen := e.owner.Channel("general").Id
	for _, m := range []*testutil.Member{alice, alice, bob, bob} {
		if _, err := m.Post(ctx, gen, "@a @b @c spam"); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(openReports(t, e.owner)); n != 3 {
		t.Fatalf("%d open reports with a cap of 3", n)
	}
}
