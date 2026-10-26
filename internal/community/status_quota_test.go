package community_test

import (
	"crypto/rand"
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// One member can't evict the key drops waiting for another by sending
// drops to made-up device ids: drops for unregistered devices aren't
// stored at all.
func TestStatusKeyDropsOnlyForRegisteredDevices(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	inv := e.newInvite(nil)
	victim := e.member("Victim", inv.Secret)
	attacker := e.member("Attacker", inv.Secret)
	dev := []byte("victim-device-000000000000000000")
	if _, err := statusCall(ctx, victim, &wire.SetStatusDevice{Entry: statusEntry(t, victim.Identity, dev), Device: dev}); err != nil {
		t.Fatal(err)
	}
	if _, err := statusCall(ctx, e.owner, &wire.SendStatusKeys{Drops: []*wire.StatusKeyDrop{{Member: victim.Identity.Public, Device: dev, Envelope: []byte("legit")}}}); err != nil {
		t.Fatal(err)
	}
	var made [][]byte
	for i := 0; i < 42; i++ {
		var drops []*wire.StatusKeyDrop
		for j := 0; j < 500; j++ {
			d := make([]byte, 16)
			_, _ = rand.Read(d)
			if j == 0 {
				made = append(made, d)
			}
			drops = append(drops, &wire.StatusKeyDrop{Member: victim.Identity.Public, Device: d, Envelope: []byte("x")})
		}
		// One for the real device too: it replaces only the attacker's own.
		drops[1] = &wire.StatusKeyDrop{Member: victim.Identity.Public, Device: dev, Envelope: []byte("attacker")}
		if _, err := statusCall(ctx, attacker, &wire.SendStatusKeys{Drops: drops}); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	r, err := statusCall(ctx, victim, &wire.FetchStatusKeys{Device: dev})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(r.GetStatusKeys().Items); got != 2 {
		t.Fatalf("drops waiting for the victim's device: %d, want the owner's and the attacker's latest", got)
	}
	for _, d := range made[:3] {
		r, err := statusCall(ctx, victim, &wire.FetchStatusKeys{Device: d})
		if err != nil {
			t.Fatal(err)
		}
		if n := len(r.GetStatusKeys().Items); n != 0 {
			t.Fatalf("%d drops stored for an unregistered device", n)
		}
	}
	// Unregistering the device drops what waited for it.
	if _, err := statusCall(ctx, victim, &wire.SetStatusDevice{Device: dev}); err != nil {
		t.Fatal(err)
	}
	if _, err := statusCall(ctx, victim, &wire.SetStatusDevice{Entry: statusEntry(t, victim.Identity, dev), Device: dev}); err != nil {
		t.Fatal(err)
	}
	r, err = statusCall(ctx, victim, &wire.FetchStatusKeys{Device: dev})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(r.GetStatusKeys().Items); n != 0 {
		t.Fatalf("%d drops survived the device's removal", n)
	}
}
