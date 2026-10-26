package community_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Profile changes rebuild every session's state, so they are limited per
// member: a burst of ProfileChangeBurst, then one every 2 s on average.
// The limit is per member and covers SetProfile and UpdateProfile alike.
func TestProfileChangesRateLimited(t *testing.T) {
	e := setup(t, nil)
	alice := e.member("Alice", e.invite.Secret)
	bob := e.member("Bob", e.invite.Secret)
	now := time.Now()
	e.comm.Server.SetClock(func() time.Time { return now })
	rateLimited := func(what string, err error) {
		t.Helper()
		wantCode(t, what, err, wire.RpcErrorCode_RPC_ERROR_CODE_RATE_LIMITED)
	}
	setName := func(name string) error {
		_, err := alice.Call(t.Context(), &wire.ClientMessage{Body: &wire.ClientMessage_SetProfile{SetProfile: &wire.SetProfile{DisplayName: name}}})
		return err
	}
	for i := 0; i < community.ProfileChangeBurst; i++ {
		if _, err := call(t, alice, &wire.UpdateProfile{About: fmt.Sprint("about ", i)}); err != nil {
			t.Fatalf("change %d: %v", i, err)
		}
	}
	_, err := call(t, alice, &wire.UpdateProfile{About: "one too many"})
	rateLimited("UpdateProfile over the limit", err)
	rateLimited("SetProfile over the limit", setName("Alicia"))
	// Someone else isn't affected.
	if _, err := call(t, bob, &wire.UpdateProfile{About: "bob"}); err != nil {
		t.Fatalf("bob: %v", err)
	}
	// The window slides: still refused until the burst is a window old.
	now = now.Add(community.ProfileChangeWindow - time.Millisecond)
	_, err = call(t, alice, &wire.UpdateProfile{About: "later"})
	rateLimited("inside the window", err)
	now = now.Add(time.Millisecond)
	if err := setName("Alicia"); err != nil {
		t.Fatalf("after the window: %v", err)
	}
}

// Changing your own nickname rebuilds every session's state too, and Change
// Nickname is an @everyone default, so it shares the same per-member limit.
// Setting the nickname you already have changes nothing (no audit entry) and
// isn't counted; a moderator setting someone else's nickname isn't limited.
func TestOwnNicknameChangesRateLimited(t *testing.T) {
	e := setup(t, nil)
	alice := e.member("Alice", e.invite.Secret)
	now := time.Now()
	e.comm.Server.SetClock(func() time.Time { return now })
	setNick := func(actor *testutil.Member, n string) error {
		_, err := e.adminAs(actor, &wire.AdminAction{Op: &wire.AdminAction_SetNickname{SetNickname: &wire.SetNickname{Member: alice.Identity.Public, Nickname: n}}})
		return err
	}
	for i := 0; i < community.ProfileChangeBurst; i++ {
		if err := setNick(alice, fmt.Sprint("Ally ", i)); err != nil {
			t.Fatalf("change %d: %v", i, err)
		}
	}
	wantCode(t, "own nickname over the limit", setNick(alice, "one too many"), wire.RpcErrorCode_RPC_ERROR_CODE_RATE_LIMITED)
	// It's the same budget as the other profile changes.
	_, err := call(t, alice, &wire.UpdateProfile{About: "after nicknames"})
	wantCode(t, "UpdateProfile after nickname changes", err, wire.RpcErrorCode_RPC_ERROR_CODE_RATE_LIMITED)
	// Unchanged: accepted, nothing recorded.
	last := fmt.Sprint("Ally ", community.ProfileChangeBurst-1)
	if err := setNick(alice, last); err != nil {
		t.Fatalf("unchanged nickname: %v", err)
	}
	summaries, err := e.comm.Server.AuditSummaries(testutil.Ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, s := range summaries {
		if strings.Contains(s, fmt.Sprintf("changed their nickname to %q", last)) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("audit entries for %q: %d, want 1 (the unchanged repeat adds none)", last, n)
	}
	// The owner (Manage Nicknames) sets Alice's nickname: Alice's budget doesn't apply.
	if err := setNick(e.owner, "Set by owner"); err != nil {
		t.Fatalf("owner setting Alice's nickname: %v", err)
	}
	e.auditHas(t, `changed Alice's nickname to "Set by owner"`)
	now = now.Add(community.ProfileChangeWindow)
	if err := setNick(alice, "Ally again"); err != nil {
		t.Fatalf("after the window: %v", err)
	}
}
