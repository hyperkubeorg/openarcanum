package community_test

import (
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// One member's open reports are capped, so a few accounts can't fill the
// moderators' queue. Once moderators close one, the member may report again.
func TestOpenReportsCappedPerReporter(t *testing.T) {
	old := community.MaxOpenReportsPerReporter
	community.MaxOpenReportsPerReporter = 2
	t.Cleanup(func() { community.MaxOpenReportsPerReporter = old })
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	bob := e.member("Bob", e.invite.Secret)
	gen := e.owner.Channel("general").Id
	var ids []int64
	for i := 0; i < 4; i++ {
		m, err := bob.Post(ctx, gen, "rude thing")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.Id)
	}
	for _, id := range ids[:2] {
		if err := report(alice, id, ""); err != nil {
			t.Fatal(err)
		}
	}
	wantErr(t, report(alice, ids[2], ""), "2 reports waiting")
	// Others still report.
	if err := report(e.owner, ids[2], ""); err != nil {
		t.Fatal(err)
	}
	r, err := e.owner.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_ListReports{ListReports: &wire.ListReports{}}})
	if err != nil {
		t.Fatal(err)
	}
	var first int64
	for _, rep := range r.GetReports().Reports {
		if rep.ReporterName == "Alice" {
			first = rep.Id
		}
	}
	e.mustAdmin(e.owner, &wire.AdminAction{Op: &wire.AdminAction_ResolveReport{ResolveReport: &wire.ResolveReport{Id: first, Dismiss: true}}})
	if err := report(alice, ids[2], ""); err != nil {
		t.Fatalf("after a report was closed: %v", err)
	}
}
