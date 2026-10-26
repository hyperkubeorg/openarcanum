package community_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

func publishCard(ctx context.Context, m *testutil.Member, card []byte, devices [][]byte, kps ...*wire.KeyPackage) error {
	_, err := m.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_PublishContactCard{
		PublishContactCard: &wire.PublishContactCard{Card: card, Devices: devices, KeyPackages: kps},
	}})
	return err
}

func getCard(ctx context.Context, m *testutil.Member, of []byte) (*wire.ContactCardResult, error) {
	r, err := m.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_GetContactCard{GetContactCard: &wire.GetContactCard{Member: of}}})
	if err != nil {
		return nil, err
	}
	return r.GetContactCard(), nil
}

func kp(device, pkg string) *wire.KeyPackage {
	return &wire.KeyPackage{Device: []byte(device), Package: []byte(pkg)}
}

func TestContactCardKeyPackagesPerDevice(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	bob := e.member("Bob", e.invite.Secret)
	if err := publishCard(ctx, alice, []byte("card"), [][]byte{[]byte("d1"), []byte("d2")},
		kp("d1", "a"), kp("d1", "b"), kp("d2", "c")); err != nil {
		t.Fatal(err)
	}
	// One package per device per request, oldest first, each handed out once.
	r, err := getCard(ctx, bob, alice.Identity.Public)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, p := range r.KeyPackages {
		got[string(p.Device)] = string(p.Package)
	}
	if !bytes.Equal(r.Card, []byte("card")) || got["d1"] != "a" || got["d2"] != "c" || len(got) != 2 {
		t.Fatalf("first fetch: %v", got)
	}
	r, _ = getCard(ctx, bob, alice.Identity.Public)
	if len(r.KeyPackages) != 1 || string(r.KeyPackages[0].Package) != "b" {
		t.Fatalf("second fetch: %v", r.KeyPackages)
	}
	r, _ = getCard(ctx, bob, alice.Identity.Public)
	if len(r.KeyPackages) != 0 || !bytes.Equal(r.Card, []byte("card")) {
		t.Fatalf("exhausted: %v", r.KeyPackages)
	}
	// Topping up; the owner asking about itself sees counts without burning keys.
	if _, err := alice.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_AddKeyPackages{
		AddKeyPackages: &wire.AddKeyPackages{KeyPackages: []*wire.KeyPackage{kp("d2", "e"), kp("d2", "f")}},
	}}); err != nil {
		t.Fatal(err)
	}
	r, _ = getCard(ctx, alice, alice.Identity.Public)
	if len(r.KeyPackages) != 0 || len(r.Left) != 1 || r.Left[0].Left != 2 {
		t.Fatalf("self view: %v %v", r.KeyPackages, r.Left)
	}
}

func TestContactCardRepublishDropsRemovedDevices(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	bob := e.member("Bob", e.invite.Secret)
	if err := publishCard(ctx, alice, []byte("v1"), [][]byte{[]byte("d1"), []byte("d2")}, kp("d1", "a"), kp("d2", "b")); err != nil {
		t.Fatal(err)
	}
	if err := publishCard(ctx, alice, []byte("v2"), [][]byte{[]byte("d1")}); err != nil {
		t.Fatal(err)
	}
	r, _ := getCard(ctx, bob, alice.Identity.Public)
	if string(r.Card) != "v2" || len(r.KeyPackages) != 1 || string(r.KeyPackages[0].Device) != "d1" {
		t.Fatalf("after removing d2: %s %v", r.Card, r.KeyPackages)
	}
}

func TestContactCardLimitsAndFailures(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	bob := e.member("Bob", e.invite.Secret)
	if _, err := getCard(ctx, bob, alice.Identity.Public); err == nil {
		t.Fatal("card that was never published")
	}
	if _, err := alice.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_AddKeyPackages{
		AddKeyPackages: &wire.AddKeyPackages{KeyPackages: []*wire.KeyPackage{kp("d1", "a")}},
	}}); err == nil {
		t.Fatal("key packages without a card")
	}
	if err := publishCard(ctx, alice, []byte("c"), nil); err == nil {
		t.Fatal("card without devices")
	}
	if err := publishCard(ctx, alice, []byte("c"), [][]byte{[]byte("d1")}, kp("other", "a")); err == nil {
		t.Fatal("key package for an unlisted device")
	}
	if err := publishCard(ctx, alice, make([]byte, 65<<10), [][]byte{[]byte("d1")}); err == nil {
		t.Fatal("oversized card")
	}
	if err := publishCard(ctx, alice, []byte("c"), [][]byte{[]byte("d1")}, kp("d1", string(make([]byte, 3<<10)))); err == nil {
		t.Fatal("oversized key package")
	}
	many := make([][]byte, 21)
	for i := range many {
		many[i] = []byte{byte(i)}
	}
	if err := publishCard(ctx, alice, []byte("c"), many); err == nil {
		t.Fatal("21 devices")
	}
	// Per-device cap: extra packages are dropped, not stored.
	pkgs := make([]*wire.KeyPackage, 150)
	for i := range pkgs {
		pkgs[i] = kp("d1", "k")
	}
	if err := publishCard(ctx, alice, []byte("c"), [][]byte{[]byte("d1")}, pkgs...); err != nil {
		t.Fatal(err)
	}
	r, _ := getCard(ctx, alice, alice.Identity.Public)
	if len(r.Left) != 1 || r.Left[0].Left != 100 {
		t.Fatalf("cap: %v", r.Left)
	}
	// Removing the card removes every package.
	if err := publishCard(ctx, alice, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := getCard(ctx, bob, alice.Identity.Public); err == nil {
		t.Fatal("removed card still served")
	}
	// The community toggle turns cards off on the server.
	e.update(t, func(s *wire.CommunitySettings) { s.AllowContactCards = false })
	if err := publishCard(ctx, alice, []byte("c"), [][]byte{[]byte("d1")}); err == nil {
		t.Fatal("card published while contact cards are off")
	}
	if _, err := getCard(ctx, bob, alice.Identity.Public); err == nil {
		t.Fatal("card served while contact cards are off")
	}
}
