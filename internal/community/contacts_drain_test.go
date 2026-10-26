package community_test

import (
	"fmt"
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// One member can't drain another's one-time key packages by fetching their
// card over and over: past a few fetches per card (and 30 in all per hour)
// the card comes with counts only.
func TestContactCardFetchesCantDrainKeys(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	mallory := e.member("Mallory", e.invite.Secret)
	bob := e.member("Bob", e.invite.Secret)
	var kps []*wire.KeyPackage
	for i := 0; i < 20; i++ {
		kps = append(kps, kp("d1", fmt.Sprintf("pkg-%02d", i)))
	}
	if err := publishCard(ctx, alice, []byte("card"), [][]byte{[]byte("d1")}, kps...); err != nil {
		t.Fatal(err)
	}
	handed := 0
	for i := 0; i < 10; i++ {
		r, err := getCard(ctx, mallory, alice.Identity.Public)
		if err != nil {
			t.Fatal(err)
		}
		if string(r.Card) != "card" {
			t.Fatalf("card: %q", r.Card)
		}
		handed += len(r.KeyPackages)
	}
	if handed != 3 {
		t.Fatalf("mallory was handed %d packages, want 3", handed)
	}
	// Someone else still gets one.
	r, err := getCard(ctx, bob, alice.Identity.Public)
	if err != nil || len(r.KeyPackages) != 1 {
		t.Fatalf("bob: %v %v", r, err)
	}
}
