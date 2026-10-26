package community_test

import (
	"testing"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// waitQueue reads m's pushed events until a ModQueue that satisfies ok.
func waitQueue(t *testing.T, m *testutil.Member, what string, ok func(*wire.ModQueue) bool) *wire.ModQueue {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, open := <-m.Events:
			if !open {
				t.Fatalf("%s: session closed", what)
			}
			if q := ev.GetModQueue(); q != nil && ok(q) {
				return q
			}
			if sc := ev.GetStateChanged(); sc != nil && sc.State != nil && sc.State.ModQueue != nil && ok(sc.State.ModQueue) {
				return sc.State.ModQueue
			}
		case <-deadline:
			t.Fatalf("%s: no queue count", what)
		}
	}
}

// noQueue fails if m got any moderator queue counts.
func noQueue(t *testing.T, m *testutil.Member, who string) {
	t.Helper()
	for quiet := time.After(300 * time.Millisecond); quiet != nil; {
		select {
		case ev := <-m.Events:
			if ev.GetModQueue() != nil || ev.GetModAlert() != nil {
				t.Fatalf("%s got moderator queue news: %v", who, ev)
			}
			if sc := ev.GetStateChanged(); sc != nil && sc.State != nil && sc.State.ModQueue != nil {
				t.Fatalf("%s's state carries queue counts: %v", who, sc.State.ModQueue)
			}
		case <-quiet:
			quiet = nil
		}
	}
}

// What waits in the moderator queues reaches only those allowed to see each
// queue: open reports and AutoMod alerts with Manage Messages, join requests
// with Approve Members. The counts are in the state on connect, and pushed
// whenever they change (a report, a resolved report, a join request, a
// decision on one).
func TestModQueueCountsAreGatedAndLive(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	bob := e.member("Bob", e.invite.Secret)
	mod := e.member("Mod", e.invite.Secret)
	gate := e.member("Gate", e.invite.Secret)
	e.role("Mods", community.PermManageMessages, mod)
	e.role("Gatekeepers", community.PermApproveMembers, gate)
	gen := e.owner.Channel("general").Id

	// On connect (Sync): the owner sees both queues, empty; a plain member
	// gets no counts at all.
	if q := fresh(t, e.owner).ModQueue; q == nil || q.OpenReports != 0 || q.PendingMembers != 0 {
		t.Fatalf("owner's queue: %v", q)
	}
	if q := fresh(t, alice).ModQueue; q != nil {
		t.Fatalf("a member without moderator permissions got queue counts: %v", q)
	}
	if q := fresh(t, mod).ModQueue; q == nil {
		t.Fatal("a moderator's state has no queue counts")
	}

	// Bob reports Alice's message: moderators' counts go up live.
	msg, err := alice.Post(ctx, gen, "something rude")
	if err != nil {
		t.Fatal(err)
	}
	if err := report(bob, msg.Id, "rude"); err != nil {
		t.Fatal(err)
	}
	waitQueue(t, mod, "moderator after a report", func(q *wire.ModQueue) bool { return q.OpenReports == 1 })
	waitQueue(t, e.owner, "owner after a report", func(q *wire.ModQueue) bool { return q.OpenReports == 1 })
	// Approve Members alone doesn't show the report queue.
	if q := fresh(t, gate).ModQueue; q == nil || q.OpenReports != 0 {
		t.Fatalf("a gatekeeper saw the report count: %v", q)
	}
	if q := fresh(t, mod).ModQueue; q.OpenReports != 1 {
		t.Fatalf("moderator's state after a report: %v", q)
	}

	// A join request: gatekeepers' (and the owner's) pending count goes up;
	// a moderator without Approve Members doesn't see it.
	req := e.newInvite(func(c *wire.CreateJoinKey) { c.RequestOnly = true })
	carol := e.member("Carol", "")
	if r, err := carol.Join(ctx, req.Secret); err != nil || r.Status != wire.JoinStatus_JOIN_STATUS_PENDING {
		t.Fatalf("request: %v %v", r, err)
	}
	waitQueue(t, gate, "gatekeeper after a join request", func(q *wire.ModQueue) bool { return q.PendingMembers == 1 && q.OpenReports == 0 })
	waitQueue(t, e.owner, "owner after a join request", func(q *wire.ModQueue) bool { return q.PendingMembers == 1 && q.OpenReports == 1 })
	if q := fresh(t, mod).ModQueue; q.PendingMembers != 0 || q.OpenReports != 1 {
		t.Fatalf("moderator's state after a join request: %v", q)
	}

	// Resolving the report and approving the request clear the counts.
	list, err := mod.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_ListReports{ListReports: &wire.ListReports{}}})
	if err != nil || len(list.GetReports().Reports) != 1 {
		t.Fatalf("reports: %v %v", list, err)
	}
	e.mustAdmin(mod, &wire.AdminAction{Op: &wire.AdminAction_ResolveReport{ResolveReport: &wire.ResolveReport{Id: list.GetReports().Reports[0].Id}}})
	waitQueue(t, mod, "moderator after resolving", func(q *wire.ModQueue) bool { return q.OpenReports == 0 })
	e.mustAdmin(gate, &wire.AdminAction{Op: &wire.AdminAction_Approve{Approve: &wire.Approve{Member: carol.Identity.Public}}})
	waitQueue(t, gate, "gatekeeper after approving", func(q *wire.ModQueue) bool { return q.PendingMembers == 0 })
	if q := fresh(t, e.owner).ModQueue; q.OpenReports != 0 || q.PendingMembers != 0 {
		t.Fatalf("owner's queue after both: %v", q)
	}

	// Alice and Bob never got any of it.
	noQueue(t, alice, "Alice")
	noQueue(t, bob, "Bob")

	// Losing the permission loses the counts.
	e.mustAdmin(e.owner, &wire.AdminAction{Op: &wire.AdminAction_AssignRoles{AssignRoles: &wire.AssignRoles{Member: mod.Identity.Public}}})
	if q := fresh(t, mod).ModQueue; q != nil {
		t.Fatalf("a former moderator still gets queue counts: %v", q)
	}
}
