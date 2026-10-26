package community_test

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Statuses (status.go, docs/protocol.md §4.11): the server stores and
// forwards opaque status devices, key drops and ciphertexts, honours the
// Invisible flag, and bounds sizes and rates.

func statusCall(ctx context.Context, m *testutil.Member, body any) (*wire.ServerMessage, error) {
	msg := &wire.ClientMessage{}
	switch b := body.(type) {
	case *wire.SetStatusDevice:
		msg.Body = &wire.ClientMessage_SetStatusDevice{SetStatusDevice: b}
	case *wire.SetStatus:
		msg.Body = &wire.ClientMessage_SetStatus{SetStatus: b}
	case *wire.ListStatuses:
		msg.Body = &wire.ClientMessage_ListStatuses{ListStatuses: b}
	case *wire.SendStatusKeys:
		msg.Body = &wire.ClientMessage_SendStatusKeys{SendStatusKeys: b}
	case *wire.FetchStatusKeys:
		msg.Body = &wire.ClientMessage_FetchStatusKeys{FetchStatusKeys: b}
	case *wire.RequestStatusKey:
		msg.Body = &wire.ClientMessage_RequestStatusKey{RequestStatusKey: b}
	case *wire.ListMembers:
		msg.Body = &wire.ClientMessage_ListMembers{ListMembers: b}
	default:
		panic(fmt.Sprintf("statusCall: %T", body))
	}
	return m.Call(ctx, msg)
}

// statusEntry is a status device entry signed by the member key, as the
// client makes it (status.py).
func statusEntry(t *testing.T, k *identity.Key, device []byte) []byte {
	t.Helper()
	d, err := proto.Marshal(&wire.StatusDevice{Member: k.Public, Curve25519: device, Ed25519: bytes.Repeat([]byte{7}, 32), IssuedAt: time.Now().UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	b, err := proto.Marshal(&wire.SignedStatusDevice{Device: d, Signature: k.Sign(identity.CtxStatusDevice, d)})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// waitBatch reads m's pushed events until pred accepts a presence batch.
func waitBatch(t *testing.T, m *testutil.Member, what string, pred func(*wire.PresenceBatch) bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-m.Events:
			if b := ev.GetPresenceBatch(); b != nil && pred(b) {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func statusOf(b *wire.PresenceBatch, member []byte) *wire.StatusEntry {
	for _, s := range b.Statuses {
		if bytes.Equal(s.Member, member) {
			return s
		}
	}
	return nil
}

func TestStatusStoredAndForwardedOpaquely(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	bob := e.member("Bob", e.invite.Secret)
	dev := []byte("alice-status-curve-0123456789abc")
	entry := statusEntry(t, alice.Identity, dev)
	// Not her entry, or not for this device, or not signed by her: refused.
	if _, err := statusCall(ctx, bob, &wire.SetStatusDevice{Entry: entry, Device: dev}); err == nil {
		t.Fatal("bob registered alice's entry")
	}
	if _, err := statusCall(ctx, alice, &wire.SetStatusDevice{Entry: entry, Device: []byte("other")}); err == nil {
		t.Fatal("entry stored under another device")
	}
	forged := statusEntry(t, bob.Identity, dev)
	var sd wire.SignedStatusDevice
	_ = proto.Unmarshal(forged, &sd)
	var d wire.StatusDevice
	_ = proto.Unmarshal(sd.Device, &d)
	d.Member = alice.Identity.Public
	sd.Device, _ = proto.Marshal(&d)
	forged, _ = proto.Marshal(&sd)
	if _, err := statusCall(ctx, alice, &wire.SetStatusDevice{Entry: forged, Device: dev}); err == nil {
		t.Fatal("entry with a bad signature stored")
	}
	if _, err := statusCall(ctx, alice, &wire.SetStatusDevice{Entry: entry, Device: dev}); err != nil {
		t.Fatal(err)
	}
	blob := append([]byte("FDM2"), []byte("opaque-megolm-ciphertext-quetzal")...)
	if _, err := statusCall(ctx, alice, &wire.SetStatus{Blob: blob}); err != nil {
		t.Fatal(err)
	}
	// Bob is pushed alice's entry exactly as stored, and lists it.
	waitBatch(t, bob, "alice's status", func(b *wire.PresenceBatch) bool {
		s := statusOf(b, alice.Identity.Public)
		return s != nil && bytes.Equal(s.Blob, blob) && len(s.Devices) == 1 && bytes.Equal(s.Devices[0], entry)
	})
	r, err := statusCall(ctx, bob, &wire.ListStatuses{})
	if err != nil {
		t.Fatal(err)
	}
	var found *wire.StatusEntry
	for _, s := range r.GetStatuses().Entries {
		if bytes.Equal(s.Member, alice.Identity.Public) {
			found = s
		}
	}
	if found == nil || !bytes.Equal(found.Blob, blob) || len(found.Devices) != 1 {
		t.Fatalf("listed: %v", r.GetStatuses())
	}
	// Re-registering the same entry (every connect) announces nothing.
	if _, err := statusCall(ctx, alice, &wire.SetStatusDevice{Entry: entry, Device: dev}); err != nil {
		t.Fatal(err)
	}
	// Key drops: pushed to the online recipient, fetched per device,
	// replaced per (sender, device), acknowledged only by the recipient.
	bobDev := []byte("bob-status-curve")
	if _, err := statusCall(ctx, bob, &wire.SetStatusDevice{Entry: statusEntry(t, bob.Identity, bobDev), Device: bobDev}); err != nil {
		t.Fatal(err)
	}
	env1, env2 := []byte("FDM2olm-drop-one-pangolin"), []byte("FDM2olm-drop-two-pangolin")
	if _, err := statusCall(ctx, alice, &wire.SendStatusKeys{Drops: []*wire.StatusKeyDrop{{Member: bob.Identity.Public, Device: bobDev, Envelope: env1}}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for pushed := false; !pushed; {
		select {
		case ev := <-bob.Events:
			if k := ev.GetStatusKeys(); k != nil && len(k.Items) == 1 && bytes.Equal(k.Items[0].Envelope, env1) && bytes.Equal(k.Items[0].Sender, alice.Identity.Public) {
				pushed = true
			}
		case <-deadline:
			t.Fatal("key drop not pushed")
		}
	}
	if _, err := statusCall(ctx, alice, &wire.SendStatusKeys{Drops: []*wire.StatusKeyDrop{{Member: bob.Identity.Public, Device: bobDev, Envelope: env2}}}); err != nil {
		t.Fatal(err)
	}
	fetch := func(m *testutil.Member, device []byte, ack ...uint64) *wire.StatusKeys {
		t.Helper()
		r, err := statusCall(ctx, m, &wire.FetchStatusKeys{Device: device, Ack: ack})
		if err != nil {
			t.Fatal(err)
		}
		return r.GetStatusKeys()
	}
	got := fetch(bob, bobDev)
	if len(got.Items) != 1 || !bytes.Equal(got.Items[0].Envelope, env2) {
		t.Fatalf("fetched: %v", got.Items)
	}
	if len(fetch(bob, []byte("another-device")).Items) != 0 {
		t.Fatal("another device got bob's drop")
	}
	// Alice can't acknowledge (delete) Bob's drop.
	fetch(alice, dev, got.Items[0].Id)
	if len(fetch(bob, bobDev).Items) != 1 {
		t.Fatal("someone else deleted bob's drop")
	}
	if len(fetch(bob, bobDev, got.Items[0].Id).Items) != 0 {
		t.Fatal("acknowledged drop still there")
	}

	// Nothing the server stores is readable: entries, ciphertexts and drops
	// are sealed, keys stored only as lookup hashes.
	if _, err := statusCall(ctx, alice, &wire.SendStatusKeys{Drops: []*wire.StatusKeyDrop{{Member: bob.Identity.Public, Device: bobDev, Envelope: env1}}}); err != nil {
		t.Fatal(err)
	}
	e.comm.Stop()
	assertNoPlaintext(t, [][]byte{blob[4:], entry, dev, bobDev, env1[4:], alice.Identity.Public}, dbFiles(e.comm.Dir)...)
}

func TestInvisibleSuppressesPresence(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	online := func(viewer *testutil.Member, of []byte) bool {
		t.Helper()
		r, err := statusCall(ctx, viewer, &wire.ListMembers{})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range r.GetMembers().Members {
			if bytes.Equal(m.Key, of) {
				return m.Online
			}
		}
		t.Fatal("not listed")
		return false
	}
	presence := func(b *wire.PresenceBatch, of []byte, want bool) bool {
		for _, p := range b.Updates {
			if bytes.Equal(p.Member, of) && p.Online == want {
				return true
			}
		}
		return false
	}
	waitBatch(t, e.owner, "alice online", func(b *wire.PresenceBatch) bool { return presence(b, alice.Identity.Public, true) })
	if _, err := statusCall(ctx, alice, &wire.SetStatus{Invisible: true, KeepBlob: true}); err != nil {
		t.Fatal(err)
	}
	waitBatch(t, e.owner, "alice appearing offline", func(b *wire.PresenceBatch) bool { return presence(b, alice.Identity.Public, false) })
	if online(e.owner, alice.Identity.Public) {
		t.Fatal("invisible alice listed online to the owner")
	}
	if !online(alice, alice.Identity.Public) {
		t.Fatal("alice doesn't see herself online")
	}
	r, err := statusCall(ctx, alice, &wire.ListStatuses{})
	if err != nil || !r.GetStatuses().Invisible {
		t.Fatalf("own invisible flag: %v %v", r, err)
	}
	// The flag is kept: reconnecting (another device) announces nothing.
	d2 := testutil.NewMember(t, "Alice")
	d2.Identity = alice.Identity
	alice.Close()
	time.Sleep(300 * time.Millisecond)
	if err := d2.Connect(ctx, e.relay.URL, e.serverKey()); err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	deadline := time.After(1500 * time.Millisecond)
	for done := false; !done; {
		select {
		case ev := <-e.owner.Events:
			if b := ev.GetPresenceBatch(); b != nil && presence(b, alice.Identity.Public, true) {
				t.Fatal("invisible alice announced online on reconnect")
			}
		case <-deadline:
			done = true
		}
	}
	if online(e.owner, alice.Identity.Public) {
		t.Fatal("invisible alice listed online after reconnecting")
	}
	// Visible again: online for everyone.
	if _, err := statusCall(ctx, d2, &wire.SetStatus{KeepBlob: true}); err != nil {
		t.Fatal(err)
	}
	waitBatch(t, e.owner, "alice visible", func(b *wire.PresenceBatch) bool { return presence(b, alice.Identity.Public, true) })
}

func TestStatusLimits(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	bob := e.member("Bob", e.invite.Secret)
	if _, err := statusCall(ctx, alice, &wire.SetStatus{Blob: make([]byte, 4<<10+1)}); err == nil {
		t.Fatal("oversized status accepted")
	}
	// At most 10 changes in 20 seconds.
	limited := false
	for i := 0; i < 12; i++ {
		if _, err := statusCall(ctx, alice, &wire.SetStatus{Blob: []byte{byte(i)}}); err != nil {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("status changes not rate limited")
	}
	// At most 10 status devices per member.
	for i := 0; i < 10; i++ {
		dev := []byte(fmt.Sprintf("device-%02d", i))
		if _, err := statusCall(ctx, bob, &wire.SetStatusDevice{Entry: statusEntry(t, bob.Identity, dev), Device: dev}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := statusCall(ctx, bob, &wire.SetStatusDevice{Entry: statusEntry(t, bob.Identity, []byte("eleventh")), Device: []byte("eleventh")}); err == nil {
		t.Fatal("an 11th status device accepted")
	}
	// Removing one makes room.
	if _, err := statusCall(ctx, bob, &wire.SetStatusDevice{Device: []byte("device-00")}); err != nil {
		t.Fatal(err)
	}
	if _, err := statusCall(ctx, bob, &wire.SetStatusDevice{Entry: statusEntry(t, bob.Identity, []byte("eleventh")), Device: []byte("eleventh")}); err != nil {
		t.Fatal(err)
	}
	// Key drops: at most 500 per request, each at most 4 KiB.
	many := make([]*wire.StatusKeyDrop, 501)
	for i := range many {
		many[i] = &wire.StatusKeyDrop{Member: alice.Identity.Public, Device: []byte("d"), Envelope: []byte("x")}
	}
	if _, err := statusCall(ctx, bob, &wire.SendStatusKeys{Drops: many}); err == nil {
		t.Fatal("501 drops accepted")
	}
	if _, err := statusCall(ctx, bob, &wire.SendStatusKeys{Drops: []*wire.StatusKeyDrop{{Member: alice.Identity.Public, Device: []byte("d"), Envelope: make([]byte, 4<<10+1)}}}); err == nil {
		t.Fatal("oversized drop accepted")
	}
	// Drops for someone who isn't a member are dropped.
	if _, err := statusCall(ctx, bob, &wire.SendStatusKeys{Drops: []*wire.StatusKeyDrop{{Member: []byte("not-a-member-key"), Device: []byte("d"), Envelope: []byte("x")}}}); err != nil {
		t.Fatal(err)
	}
	// Key requests are passed to the member asked, and limited.
	if _, err := statusCall(ctx, bob, &wire.RequestStatusKey{Member: alice.Identity.Public, Device: []byte("bob-dev")}); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for got := false; !got; {
		select {
		case ev := <-alice.Events:
			if r := ev.GetStatusKeyRequest(); r != nil && bytes.Equal(r.Member, bob.Identity.Public) && bytes.Equal(r.Device, []byte("bob-dev")) {
				got = true
			}
		case <-deadline:
			t.Fatal("key request not forwarded")
		}
	}
	limited = false
	for i := 0; i < 40; i++ {
		if _, err := statusCall(ctx, bob, &wire.RequestStatusKey{Member: alice.Identity.Public, Device: []byte("bob-dev")}); err != nil {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("key requests not rate limited")
	}
}

func TestStatusOfRemovedMemberForgotten(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	mallory := e.member("Mallory", e.invite.Secret)
	dev := []byte("mallory-status-device")
	if _, err := statusCall(ctx, mallory, &wire.SetStatusDevice{Entry: statusEntry(t, mallory.Identity, dev), Device: dev}); err != nil {
		t.Fatal(err)
	}
	if _, err := statusCall(ctx, alice, &wire.SendStatusKeys{Drops: []*wire.StatusKeyDrop{{Member: mallory.Identity.Public, Device: dev, Envelope: []byte("k")}}}); err != nil {
		t.Fatal(err)
	}
	waitBatch(t, alice, "mallory's device", func(b *wire.PresenceBatch) bool {
		s := statusOf(b, mallory.Identity.Public)
		return s != nil && len(s.Devices) == 1
	})
	if _, err := e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_Kick{Kick: &wire.Kick{Member: mallory.Identity.Public}}}); err != nil {
		t.Fatal(err)
	}
	// Members are told her entry is empty: senders stop sharing with her.
	waitBatch(t, alice, "mallory's entry emptied", func(b *wire.PresenceBatch) bool {
		s := statusOf(b, mallory.Identity.Public)
		return s != nil && len(s.Devices) == 0 && len(s.Blob) == 0
	})
	r, err := statusCall(ctx, alice, &wire.ListStatuses{})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range r.GetStatuses().Entries {
		if bytes.Equal(s.Member, mallory.Identity.Public) {
			t.Fatal("removed member still listed")
		}
	}
	// New drops for her are discarded.
	if _, err := statusCall(ctx, alice, &wire.SendStatusKeys{Drops: []*wire.StatusKeyDrop{{Member: mallory.Identity.Public, Device: dev, Envelope: []byte("k2")}}}); err != nil {
		t.Fatal(err)
	}
}

func TestStatusSurvivesRestart(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	dev := []byte("alice-dev")
	if _, err := statusCall(ctx, alice, &wire.SetStatusDevice{Entry: statusEntry(t, alice.Identity, dev), Device: dev}); err != nil {
		t.Fatal(err)
	}
	if _, err := statusCall(ctx, alice, &wire.SetStatus{Blob: []byte("FDM2blob"), Invisible: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := statusCall(ctx, e.owner, &wire.SendStatusKeys{Drops: []*wire.StatusKeyDrop{{Member: alice.Identity.Public, Device: dev, Envelope: []byte("FDM2drop")}}}); err != nil {
		t.Fatal(err)
	}
	alice.Close()
	e.comm.Restart(t, e.relay.URL)
	a2 := testutil.NewMember(t, "Alice")
	a2.Identity = alice.Identity
	if err := a2.Connect(ctx, e.relay.URL, e.serverKey()); err != nil {
		t.Fatal(err)
	}
	defer a2.Close()
	r, err := statusCall(ctx, a2, &wire.ListStatuses{})
	if err != nil {
		t.Fatal(err)
	}
	if !r.GetStatuses().Invisible || len(r.GetStatuses().Entries) != 1 || string(r.GetStatuses().Entries[0].Blob) != "FDM2blob" {
		t.Fatalf("after restart: %v", r.GetStatuses())
	}
	k, err := statusCall(ctx, a2, &wire.FetchStatusKeys{Device: dev})
	if err != nil || len(k.GetStatusKeys().Items) != 1 {
		t.Fatalf("drops after restart: %v %v", k, err)
	}
}
