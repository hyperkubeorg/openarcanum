package community_test

import (
	"bytes"
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/fanout"
	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Owner sets: any owner signs server-key delegations and adds or removes
// owners; the server enforces every step.

func inviteOwner(member []byte, cancel bool) *wire.AdminAction {
	return &wire.AdminAction{Op: &wire.AdminAction_InviteOwner{InviteOwner: &wire.InviteOwner{Member: member, Cancel: cancel}}}
}

func submitOwnerKey(add *wire.OwnerAdd) *wire.AdminAction {
	return &wire.AdminAction{Op: &wire.AdminAction_SubmitOwnerKey{SubmitOwnerKey: &wire.SubmitOwnerKey{Add: add}}}
}

// verifiedState syncs m and verifies the pushed cert against the
// community id, as a client does.
func verifiedState(t *testing.T, m *testutil.Member, community []byte) *identity.VerifiedCert {
	t.Helper()
	if err := m.Sync(testutil.Ctx(t)); err != nil {
		t.Fatal(err)
	}
	v, err := identity.VerifyCert(m.State.Cert, m.State.Info.ServerKey, community)
	if err != nil {
		t.Fatalf("state cert doesn't verify: %v", err)
	}
	return v
}

// changeOwners builds the ChangeOwners action a client sends: the change
// signed by signer, and a new cert for the current server key under the
// changed set. If the change doesn't apply locally the cert is made under
// the current set, so the server's refusal is what's tested.
func changeOwners(t *testing.T, m *testutil.Member, community []byte, signer *identity.Key, add *wire.OwnerAdd, remove []byte) *wire.AdminAction {
	t.Helper()
	v := verifiedState(t, m, community)
	change, err := identity.SignOwnerChange(signer, v.Owners, add, remove)
	if err != nil {
		t.Fatal(err)
	}
	next := v.Owners.Clone()
	chain := m.State.Cert.OwnerChain
	if _, err := next.Apply(change); err == nil {
		chain = append(append([]*wire.SignedOwnerChange(nil), chain...), change)
	} else {
		next = v.Owners
	}
	cert, err := identity.IssueServerCert(signer, m.State.Info.ServerKey, v.Cert.Seq+1, next, chain)
	if err != nil {
		cert, _ = identity.IssueServerCert(signer, m.State.Info.ServerKey, v.Cert.Seq+1, identity.FounderSet(signer.Public), nil)
	}
	return &wire.AdminAction{Op: &wire.AdminAction_ChangeOwners{ChangeOwners: &wire.ChangeOwners{Change: change, Cert: cert}}}
}

// addOwner runs the whole flow: invite, the member's key request, and the
// signing owner's change. It returns the new owner key.
func addOwner(t *testing.T, e *env, by *testutil.Member, byKey *identity.Key, m *testutil.Member) *identity.Key {
	t.Helper()
	ctx := testutil.Ctx(t)
	if _, err := by.Admin(ctx, e.id(), inviteOwner(m.Identity.Public, false)); err != nil {
		t.Fatal(err)
	}
	key, _ := identity.Generate()
	add, _ := identity.RequestOwnerKey(key, m.Identity, e.id())
	if _, err := m.Admin(ctx, e.id(), submitOwnerKey(add)); err != nil {
		t.Fatal(err)
	}
	if _, err := by.Admin(ctx, e.id(), changeOwners(t, by, e.id(), byKey, add, nil)); err != nil {
		t.Fatal(err)
	}
	return key
}

func denied(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s was accepted", what)
	}
}

func TestAddOwnerFlowIsEnforced(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	bob := e.member("Bob", e.invite.Secret)

	// One owner to start with: the founder.
	v := verifiedState(t, e.owner, e.id())
	if len(e.owner.State.Owners) != 1 || v.Owners.Seq != 0 || !e.owner.State.Self.Owner {
		t.Fatalf("fresh community owners: %v", e.owner.State.Owners)
	}
	// Non-owners can't invite, and nobody can submit a key uninvited.
	_, err := bob.Admin(ctx, e.id(), inviteOwner(alice.Identity.Public, false))
	denied(t, "invite by a non-owner", err)
	bobKey, _ := identity.Generate()
	bobAdd, _ := identity.RequestOwnerKey(bobKey, bob.Identity, e.id())
	_, err = bob.Admin(ctx, e.id(), submitOwnerKey(bobAdd))
	denied(t, "uninvited key request", err)

	if _, err := e.owner.Admin(ctx, e.id(), inviteOwner(alice.Identity.Public, false)); err != nil {
		t.Fatal(err)
	}
	if err := alice.Sync(ctx); err != nil || !alice.State.Self.OwnerInvited {
		t.Fatalf("alice not told about her invitation: %v", err)
	}
	aliceKey, _ := identity.Generate()
	// A request for someone else, or without proof of the key, is refused.
	_, err = alice.Admin(ctx, e.id(), submitOwnerKey(bobAdd))
	denied(t, "key request for another member", err)
	noProof, _ := identity.RequestOwnerKey(aliceKey, alice.Identity, e.id())
	noProof.OwnerSig = alice.Identity.Sign(identity.CtxOwnerKey, noProof.Request)
	_, err = alice.Admin(ctx, e.id(), submitOwnerKey(noProof))
	denied(t, "key request without proof of possession", err)
	aliceAdd, _ := identity.RequestOwnerKey(aliceKey, alice.Identity, e.id())
	if _, err := alice.Admin(ctx, e.id(), submitOwnerKey(aliceAdd)); err != nil {
		t.Fatal(err)
	}
	// Owners see the request; members don't.
	if err := e.owner.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if p := e.owner.State.PendingOwners; len(p) != 1 || !bytes.Equal(p[0].Member, alice.Identity.Public) || p[0].Add == nil {
		t.Fatalf("pending owners: %v", p)
	}
	if err := bob.Sync(ctx); err != nil || len(bob.State.PendingOwners) != 0 {
		t.Fatalf("a member sees pending owners: %v", bob.State.PendingOwners)
	}

	// Refused: a non-owner signing (with a key of their own), an owner
	// forging the signature, a change signed with a key other than the
	// actor's, a key that wasn't requested, and a cert from the wrong key.
	_, err = bob.Admin(ctx, e.id(), changeOwners(t, bob, e.id(), bobKey, aliceAdd, nil))
	denied(t, "owner change by a non-owner", err)
	forged := changeOwners(t, e.owner, e.id(), e.ownerK, aliceAdd, nil)
	forged.GetChangeOwners().Change.Signature = bobKey.Sign(identity.CtxOwnerChange, forged.GetChangeOwners().Change.Change)
	_, err = e.owner.Admin(ctx, e.id(), forged)
	denied(t, "forged owner change signature", err)
	_, err = e.owner.Admin(ctx, e.id(), changeOwners(t, e.owner, e.id(), e.ownerK, bobAdd, nil))
	denied(t, "adding a key nobody was invited for", err)
	badCert := changeOwners(t, e.owner, e.id(), e.ownerK, aliceAdd, nil)
	badCert.GetChangeOwners().Cert.Signature = bobKey.Sign(identity.CtxServerCert, badCert.GetChangeOwners().Cert.Cert)
	_, err = e.owner.Admin(ctx, e.id(), badCert)
	denied(t, "owner change with a forged cert", err)
	if verifiedState(t, e.owner, e.id()).Owners.Seq != 0 {
		t.Fatal("a refused change was applied")
	}

	if _, err := e.owner.Admin(ctx, e.id(), changeOwners(t, e.owner, e.id(), e.ownerK, aliceAdd, nil)); err != nil {
		t.Fatal(err)
	}
	v = verifiedState(t, alice, e.id())
	if !alice.State.Self.Owner || alice.State.Self.OwnerInvited || len(alice.State.Owners) != 2 || !v.Owners.Has(aliceKey.Public) {
		t.Fatalf("alice after being added: self %v owners %v", alice.State.Self, alice.State.Owners)
	}
	// A replayed change is refused (its seq is taken).
	_, err = e.owner.Admin(ctx, e.id(), changeOwners(t, e.owner, e.id(), e.ownerK, aliceAdd, nil))
	denied(t, "adding the same key twice", err)
	// Owners outrank everyone: Alice can't be kicked while she's an owner.
	_, err = e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_Kick{Kick: &wire.Kick{Member: alice.Identity.Public}}})
	denied(t, "kicking an owner", err)
	// The audit log names both steps.
	res, err := e.owner.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_AuditLog{AuditLog: &wire.AuditQuery{}}})
	if err != nil {
		t.Fatal(err)
	}
	var summaries []string
	for _, a := range res.GetAudit().Entries {
		summaries = append(summaries, a.Summary)
	}
	if !slices.ContainsFunc(summaries, func(s string) bool { return strings.HasPrefix(s, "added Alice as an owner") }) ||
		!slices.Contains(summaries, "invited Alice to become an owner") {
		t.Fatalf("audit: %q", summaries)
	}
}

func TestInvitationCanBeCancelledOrDeclined(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	bob := e.member("Bob", e.invite.Secret)
	if _, err := e.owner.Admin(ctx, e.id(), inviteOwner(alice.Identity.Public, false)); err != nil {
		t.Fatal(err)
	}
	_, err := bob.Admin(ctx, e.id(), inviteOwner(alice.Identity.Public, true))
	denied(t, "cancelling someone else's invitation as a non-owner", err)
	if _, err := alice.Admin(ctx, e.id(), inviteOwner(alice.Identity.Public, true)); err != nil {
		t.Fatalf("declining: %v", err)
	}
	if err := alice.Sync(ctx); err != nil || alice.State.Self.OwnerInvited {
		t.Fatal("invitation still open after declining")
	}
	key, _ := identity.Generate()
	add, _ := identity.RequestOwnerKey(key, alice.Identity, e.id())
	_, err = alice.Admin(ctx, e.id(), submitOwnerKey(add))
	denied(t, "key request after declining", err)
	if _, err := e.owner.Admin(ctx, e.id(), inviteOwner(alice.Identity.Public, false)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.owner.Admin(ctx, e.id(), inviteOwner(alice.Identity.Public, true)); err != nil {
		t.Fatal(err)
	}
	if err := e.owner.Sync(ctx); err != nil || len(e.owner.State.PendingOwners) != 0 {
		t.Fatal("cancelled invitation still pending")
	}
}

// A second owner rotates the server key, removes the founder, and the
// founder's key is refused from then on; the last owner can't be removed.
func TestSecondOwnerRotatesAndRemovesFounder(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	bob := e.member("Bob", e.invite.Secret)
	aliceKey := addOwner(t, e, e.owner, e.ownerK, alice)

	prepare := &wire.AdminAction{Op: &wire.AdminAction_PrepareKeyRotation{PrepareKeyRotation: &wire.PrepareKeyRotation{}}}
	_, err := bob.Admin(ctx, e.id(), prepare)
	denied(t, "key rotation by a non-owner", err)

	// Alice rotates the server key with her owner key.
	res, err := alice.Admin(ctx, e.id(), prepare)
	if err != nil {
		t.Fatal(err)
	}
	v := verifiedState(t, alice, e.id())
	// Signed with a key other than her own: refused.
	wrong, _ := identity.IssueServerCert(e.ownerK, res.NewServerKey, v.Cert.Seq+1, v.Owners, alice.State.Cert.OwnerChain)
	_, err = alice.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_CommitKeyRotation{CommitKeyRotation: &wire.CommitKeyRotation{Cert: wrong}}})
	denied(t, "rotation cert signed with another owner's key", err)
	cert, err := identity.IssueServerCert(aliceKey, res.NewServerKey, v.Cert.Seq+1, v.Owners, alice.State.Cert.OwnerChain)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := alice.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_CommitKeyRotation{CommitKeyRotation: &wire.CommitKeyRotation{Cert: cert}}}); err != nil {
		t.Fatal(err)
	}
	// Clients keep connecting: Bob reconnects at the new key, which verifies
	// against the community id through the chain.
	waitFor(t, "re-registration", func() bool {
		m := testutil.NewMember(t, "Bob")
		m.Identity, m.Static = bob.Identity, bob.Static
		defer m.Close()
		if m.Connect(ctx, e.relay.URL, res.NewServerKey) != nil {
			return false
		}
		if err := m.Sync(ctx); err != nil {
			return false
		}
		_, err := identity.VerifyCert(m.State.Cert, res.NewServerKey, e.id())
		return err == nil
	})
	newOwner := func() *testutil.Member {
		m := testutil.NewMember(t, "Owner")
		m.Identity, m.Static = e.owner.Identity, e.owner.Static
		if err := m.Connect(ctx, e.relay.URL, res.NewServerKey); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(m.Close)
		return m
	}
	founder := newOwner()
	alice2 := testutil.NewMember(t, "Alice")
	alice2.Identity, alice2.Static = alice.Identity, alice.Static
	if err := alice2.Connect(ctx, e.relay.URL, res.NewServerKey); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(alice2.Close)
	// Channel topics moved to the new key (relays only let a topic's key
	// fan out to it when it's large, docs/protocol.md §2).
	if err := alice2.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	for _, ch := range alice2.State.Channels {
		if !fanout.Commits(ch.Topic, res.NewServerKey) {
			t.Fatalf("channel %d's topic doesn't commit to the new server key", ch.Id)
		}
	}

	// Alice removes the founder. The community id doesn't change.
	if _, err := alice2.Admin(ctx, e.id(), changeOwners(t, alice2, e.id(), aliceKey, nil, e.ownerK.Public)); err != nil {
		t.Fatal(err)
	}
	v = verifiedState(t, founder, e.id())
	if v.Owners.Has(e.ownerK.Public) || founder.State.Self.Owner || len(founder.State.Owners) != 1 {
		t.Fatalf("founder still an owner: %v", founder.State.Owners)
	}
	// The removed founder's ownership actions are refused by the server...
	_, err = founder.Admin(ctx, e.id(), prepare)
	denied(t, "key rotation by a removed owner", err)
	_, err = founder.Admin(ctx, e.id(), changeOwners(t, founder, e.id(), e.ownerK, nil, aliceKey.Public))
	denied(t, "owner change by a removed owner", err)
	_, err = founder.Admin(ctx, e.id(), inviteOwner(bob.Identity.Public, false))
	denied(t, "invitation by a removed owner", err)
	// The removed founder keeps no administrator powers either: claiming
	// gave them the Admin role, and removal takes it away.
	if len(founder.State.Self.RoleIds) != 0 {
		t.Fatalf("removed founder still holds roles %v", founder.State.Self.RoleIds)
	}
	_, err = founder.Admin(ctx, e.id(), kick(bob.Identity.Public))
	denied(t, "kick by a removed owner", err)
	_, err = founder.Admin(ctx, e.id(), assignRoles(bob.Identity.Public, 2))
	denied(t, "role change by a removed owner", err)
	renamed := proto.Clone(founder.State.Settings).(*wire.CommunitySettings)
	renamed.Name = "Taken over"
	_, err = founder.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_UpdateSettings{UpdateSettings: renamed}})
	denied(t, "settings change by a removed owner", err)
	// ...and a cert signed by the founder's key no longer verifies.
	byFounder, _ := identity.IssueServerCert(e.ownerK, res.NewServerKey, v.Cert.Seq+1, identity.FounderSet(e.id()), nil)
	byFounder.OwnerChain = founder.State.Cert.OwnerChain
	if _, err := identity.VerifyCert(byFounder, res.NewServerKey, e.id()); err == nil {
		t.Fatal("removed founder's cert verifies")
	}
	// The last owner can't remove themselves.
	_, err = alice2.Admin(ctx, e.id(), changeOwners(t, alice2, e.id(), aliceKey, nil, aliceKey.Public))
	if err == nil || !strings.Contains(err.Error(), "last owner") {
		t.Fatalf("last owner removed: %v", err)
	}
	// An owner can't leave the community.
	if _, err := alice2.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_Leave{Leave: &wire.Leave{}}}); err == nil {
		t.Fatal("an owner left")
	}
}

func kick(member []byte) *wire.AdminAction {
	return &wire.AdminAction{Op: &wire.AdminAction_Kick{Kick: &wire.Kick{Member: member}}}
}

func assignRoles(member []byte, roles ...int64) *wire.AdminAction {
	return &wire.AdminAction{Op: &wire.AdminAction_AssignRoles{AssignRoles: &wire.AssignRoles{Member: member, RoleIds: roles}}}
}

// An owner removes themselves while another owner remains (protocol §3.1,
// §4.5): the server stops treating them as an owner at once, another owner
// signs the removal into the chain, and the last owner still can't leave.
func TestOwnerRemovesThemselves(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	bob := e.member("Bob", e.invite.Secret)
	carol := e.member("Carol", e.invite.Secret)
	aliceKey := addOwner(t, e, e.owner, e.ownerK, alice)
	carolKey := addOwner(t, e, e.owner, e.ownerK, carol)

	// Alice removes her own key. She can't sign a cert under a set without
	// her, so the server takes the change without one.
	if _, err := alice.Admin(ctx, e.id(), changeOwners(t, alice, e.id(), aliceKey, nil, aliceKey.Public)); err != nil {
		t.Fatalf("self-removal refused: %v", err)
	}
	if err := alice.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if alice.State.Self.Owner || len(alice.State.Self.RoleIds) != 0 {
		t.Fatalf("alice after leaving: owner %v roles %v", alice.State.Self.Owner, alice.State.Self.RoleIds)
	}
	_, err := alice.Admin(ctx, e.id(), kick(bob.Identity.Public))
	denied(t, "kick by an owner who removed themselves", err)
	_, err = alice.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_PrepareKeyRotation{PrepareKeyRotation: &wire.PrepareKeyRotation{}}})
	denied(t, "key rotation by an owner who removed themselves", err)
	// The other owners see her leaving; her key is still in the chain until
	// one of them signs the removal.
	v := verifiedState(t, e.owner, e.id())
	if !v.Owners.Has(aliceKey.Public) {
		t.Fatal("alice's key left the chain without another owner's cert")
	}
	leaving := 0
	for _, o := range e.owner.State.Owners {
		if o.Leaving {
			if !bytes.Equal(o.OwnerKey, aliceKey.Public) {
				t.Fatalf("wrong owner marked leaving: %x", o.OwnerKey)
			}
			leaving++
		}
	}
	if leaving != 1 {
		t.Fatalf("owners: %v", e.owner.State.Owners)
	}
	// Carol leaves too; then the founder, the only owner not leaving, can't.
	if _, err := carol.Admin(ctx, e.id(), changeOwners(t, carol, e.id(), carolKey, nil, carolKey.Public)); err != nil {
		t.Fatal(err)
	}
	_, err = e.owner.Admin(ctx, e.id(), changeOwners(t, e.owner, e.id(), e.ownerK, nil, e.ownerK.Public))
	if err == nil || !strings.Contains(err.Error(), "last owner") {
		t.Fatalf("the last owner who isn't leaving removed themselves: %v", err)
	}
	// The founder signs both removals into the chain.
	for _, k := range []*identity.Key{aliceKey, carolKey} {
		if _, err := e.owner.Admin(ctx, e.id(), changeOwners(t, e.owner, e.id(), e.ownerK, nil, k.Public)); err != nil {
			t.Fatal(err)
		}
	}
	v = verifiedState(t, e.owner, e.id())
	if v.Owners.Has(aliceKey.Public) || v.Owners.Has(carolKey.Public) || len(e.owner.State.Owners) != 1 || e.owner.State.Owners[0].Leaving {
		t.Fatalf("owners after the removals: %v", e.owner.State.Owners)
	}
	// Alone again, the founder still can't remove themselves.
	_, err = e.owner.Admin(ctx, e.id(), changeOwners(t, e.owner, e.id(), e.ownerK, nil, e.ownerK.Public))
	if err == nil || !strings.Contains(err.Error(), "last owner") {
		t.Fatalf("last owner removed: %v", err)
	}
	// Alice, no longer an owner, can now leave the community.
	if _, err := alice.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_Leave{Leave: &wire.Leave{}}}); err != nil {
		t.Fatalf("former owner can't leave: %v", err)
	}
}

// Moving to new hardware with a cert from an owner who was added after the
// backup was taken: the client's chain extends the backup's.
func TestMoveSignedByAddedOwner(t *testing.T) {
	m := testutil.StartMeta(t, nil)
	r := testutil.StartRelay(t)
	c := testutil.StartCommunity(t, func(cfg *community.Config) { cfg.Maintainers = []string{m.URL} }, r.URL)
	ctx := testutil.Ctx(t)
	owner := testutil.NewMember(t, "Owner")
	if err := owner.Connect(ctx, r.URL, c.ClaimCode.ServerKey); err != nil {
		t.Fatal(err)
	}
	ownerKey, _ := identity.Generate()
	res, err := owner.Claim(ctx, ownerKey, c.ClaimCode, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, relay: r, comm: c, owner: owner, ownerK: ownerKey, invite: res.FirstInvite}
	alice := e.member("Alice", res.FirstInvite.Secret)
	backup, err := c.Server.Backup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	aliceKey := addOwner(t, e, owner, ownerKey, alice)
	chain := verifiedState(t, alice, e.id())
	owner.Close()
	alice.Close()
	c.Stop()

	dir := t.TempDir()
	cfg := community.DefaultConfig(dir)
	cfg.Relays = []string{r.URL}
	cfg.Maintainers = []string{m.URL}
	cfg.BackupInterval.Duration = 0
	mv, err := community.Restore(ctx, cfg, backup, filepath.Join(c.Dir, "storage.key"))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := community.Open(ctx, cfg, testutil.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	rctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = srv.Run(rctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done; srv.Close() })
	waitFor(t, "restored server attach", func() bool { return len(srv.AttachedRelays()) == 1 })

	var code wire.MoveCode
	b, _ := identity.Decode(mv.MoveCode[len(community.MovePrefix):])
	if err := proto.Unmarshal(b, &code); err != nil {
		t.Fatal(err)
	}
	adopter := testutil.NewMember(t, "Alice")
	adopter.Identity, adopter.Static = alice.Identity, alice.Static
	if err := adopter.Connect(ctx, r.URL, code.ServerKey); err != nil {
		t.Fatal(err)
	}
	defer adopter.Close()
	adopt := func(cert *wire.SignedServerCert) error {
		_, err := adopter.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_AdoptServer{AdoptServer: &wire.AdoptServer{Secret: code.Secret, Cert: cert}}})
		return err
	}
	// A key that isn't an owner can't adopt it; neither can Alice's key
	// without the chain that made her an owner.
	stranger, _ := identity.Generate()
	bad, _ := identity.IssueServerCert(stranger, code.ServerKey, chain.Cert.Seq+1, identity.FounderSet(stranger.Public), nil)
	denied(t, "adoption by a non-owner", adopt(bad))
	noChain, _ := identity.IssueServerCert(aliceKey, code.ServerKey, chain.Cert.Seq+1, chain.Owners, nil)
	denied(t, "adoption without the owner chain", adopt(noChain))
	cert, err := identity.IssueServerCert(aliceKey, code.ServerKey, chain.Cert.Seq+1, chain.Owners, alice.State.Cert.OwnerChain)
	if err != nil {
		t.Fatal(err)
	}
	if err := adopt(cert); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "moved route", func() bool {
		var sr wire.SignedRoute
		if httpGet(t, m.URL+"/v1/routes/"+identity.Encode(code.RouteId), &sr) != 200 {
			return false
		}
		var rt wire.Route
		if proto.Unmarshal(sr.Route, &rt) != nil {
			return false
		}
		v, err := identity.VerifyCert(sr.Cert, rt.ServerKey, ownerKey.Public)
		return err == nil && bytes.Equal(rt.ServerKey, code.ServerKey) && bytes.Equal(v.Cert.OwnerKey, aliceKey.Public)
	})
	// Alice is an owner on the new hardware too.
	if err := adopter.Sync(ctx); err != nil || !adopter.State.Self.Owner || len(adopter.State.Owners) != 2 {
		t.Fatalf("owners after the move: %v %v", adopter.State.GetOwners(), err)
	}
}

// Schema 3 turns co-owners (rank without an owner key) into invitations to
// become owners, from a version 1 and from a version 2 database.
func TestMigrationCoOwnersBecomeInvitations(t *testing.T) {
	for _, from := range []int{1, 2} {
		dir, _, _ := copyFixture(t)
		execRaw(t, dir, `UPDATE members SET co_owner = 1 WHERE display_name = 'Alice Marmot'`)
		cfg := community.DefaultConfig(dir)
		if from == 2 {
			if err := community.MigrateTo(context.Background(), cfg, 2); err != nil {
				t.Fatal(err)
			}
			if v := schemaVersion(t, dir); v != 2 {
				t.Fatalf("fixture at %d, want 2", v)
			}
		}
		srv := reopen(t, dir, "ws://127.0.0.1:1/ws", testutil.Quiet())
		if got := srv.Snapshot().OwnerInvited; !slices.Equal(got, []string{"Alice Marmot"}) {
			t.Fatalf("from v%d: invited %q", from, got)
		}
		srv.Close()
		if v := schemaVersion(t, dir); v != community.LatestSchema() {
			t.Fatalf("from v%d: schema %d", from, v)
		}
	}
}
