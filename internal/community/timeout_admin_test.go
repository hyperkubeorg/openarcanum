package community_test

import (
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
)

// A timeout doesn't hold back an administrator (as in Discord): a member
// who becomes one while timed out can post again, and is restricted again
// if the role goes before the timeout ends.
func TestTimeoutIgnoredForAdministrators(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	gen := e.owner.Channel("general").Id
	e.mustAdmin(e.owner, timeoutOp(alice, 3600))
	_, err := alice.Post(ctx, gen, "muted")
	wantErr(t, err, "timed out until")
	e.role("Admins", community.PermAdministrator, alice)
	if _, err := alice.Post(ctx, gen, "an admin now"); err != nil {
		t.Fatalf("administrator still timed out: %v", err)
	}
	if st := fresh(t, alice); st.Self.TimeoutUntil != 0 {
		t.Fatalf("administrator shown as timed out until %d", st.Self.TimeoutUntil)
	}
	e.must(assign(alice))
	_, err = alice.Post(ctx, gen, "not an admin any more")
	wantErr(t, err, "timed out until")
}
