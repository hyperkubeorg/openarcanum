package community

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Owner sets (docs/protocol.md §4.5).
//
// The community id is the founder's owner key. The owner set starts as
// {founder} and changes only through owner changes signed by a current
// owner (identity/owners.go). The chain travels inside the server cert, and
// every change comes with a new cert for the current server key signed
// under the changed set, so the cert members and maintainers see always
// verifies against the current set.
//
// Adding an owner:
//  1. an owner invites a member (InviteOwner);
//  2. the member's client generates an owner key and submits a request
//     signed by that key and by the member's identity (SubmitOwnerKey); no
//     private key leaves the device;
//  3. an owner signs the change adding it, plus a new cert (ChangeOwners).
//
// Removing another owner is one ChangeOwners. Removing yourself is a
// ChangeOwners whose change removes your own key, without a cert (you can't
// sign a cert under a set you're no longer in): the server stops treating
// you as an owner at once and marks you leaving, and another owner's client
// then signs the removal and the new cert (protocol §4.5). The last owner
// can't be removed. Removal takes away everything ownership gave: the
// member keeps no roles (the founder's Admin role included).
// Owners hold every permission and outrank everyone, and only owners can
// change the set, rotate the server key or adopt a restored server.

// ownerKeyOf is the owner key a member holds in the current set, or nil
// (also for a member who asked to be removed). Callers hold s.mu.
func (s *Server) ownerKeyOf(member []byte) []byte {
	if m := s.members[string(member)]; m != nil && m.OwnerLeaving {
		return nil
	}
	return s.heldOwnerKey(member)
}

// heldOwnerKey is the owner key a member holds in the current set, leaving
// or not. Callers hold s.mu.
func (s *Server) heldOwnerKey(member []byte) []byte {
	if s.owners == nil || len(member) == 0 {
		return nil
	}
	if bytes.Equal(member, s.ownerMember) && s.owners.Has(s.ownerKey) {
		return s.ownerKey
	}
	for k, m := range s.owners.Member {
		if bytes.Equal(m, member) && s.owners.Has([]byte(k)) {
			return []byte(k)
		}
	}
	return nil
}

// memberOfOwner is the member key holding an owner key.
func (s *Server) memberOfOwner(key []byte) []byte {
	if bytes.Equal(key, s.ownerKey) {
		return s.ownerMember
	}
	if s.owners == nil {
		return nil
	}
	return s.owners.Member[string(key)]
}

// isOwner reports whether m holds a key in the current owner set.
func (s *Server) isOwner(m *memberRec) bool {
	return m != nil && s.ownerKeyOf(m.Key) != nil
}

func (s *Server) certSeq() uint64 {
	if s.cert == nil {
		return 0
	}
	var c wire.ServerCert
	if proto.Unmarshal(s.cert.Cert, &c) != nil {
		return 0
	}
	return c.Seq
}

func (s *Server) currentChain() []*wire.SignedOwnerChange {
	if s.cert == nil {
		return nil
	}
	return s.cert.OwnerChain
}

// ownerEntries lists the current owners for SyncState. Callers hold s.mu.
func (s *Server) ownerEntries() []*wire.OwnerEntry {
	if s.owners == nil {
		return nil
	}
	out := make([]*wire.OwnerEntry, 0, len(s.owners.Owners))
	for _, k := range s.owners.Owners {
		m := s.memberOfOwner(k)
		rec := s.members[string(m)]
		out = append(out, &wire.OwnerEntry{OwnerKey: k, Member: m, DisplayName: s.nameOf(m), Leaving: rec != nil && rec.OwnerLeaving})
	}
	return out
}

// pendingOwners lists invited members and their key requests (owners'
// view). Callers hold s.mu.
func (s *Server) pendingOwners() []*wire.PendingOwner {
	var out []*wire.PendingOwner
	for _, m := range s.members {
		if !m.OwnerInvited || m.Status != wire.MemberStatus_MEMBER_STATUS_ACTIVE || s.isOwner(m) {
			continue
		}
		p := &wire.PendingOwner{Member: m.Key, DisplayName: m.Name}
		if len(m.OwnerAdd) > 0 {
			p.Add = &wire.OwnerAdd{}
			if proto.Unmarshal(m.OwnerAdd, p.Add) != nil {
				p.Add = nil
			}
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DisplayName < out[j].DisplayName })
	return out
}

func (s *Server) needOwner(actor *memberRec) error {
	if !s.isOwner(actor) {
		return errDenied("only an owner can do that")
	}
	return nil
}

// opInviteOwner invites a member to become an owner, or cancels the
// invitation (an owner, or the invited member declining).
func (s *Server) opInviteOwner(ctx context.Context, actor *memberRec, op *wire.InviteOwner, out *adminOutcome) error {
	m := s.members[string(op.Member)]
	declining := op.Cancel && m == actor
	if !declining {
		if err := s.needOwner(actor); err != nil {
			return err
		}
	}
	if m == nil || m.Status != wire.MemberStatus_MEMBER_STATUS_ACTIVE {
		return errNotFound("no such member")
	}
	if s.heldOwnerKey(m.Key) != nil {
		return errInvalid("that member is already an owner")
	}
	switch {
	case op.Cancel && !m.OwnerInvited:
		return errNotFound("that member hasn't been invited to become an owner")
	case op.Cancel:
		m.OwnerInvited, m.OwnerAdd = false, nil
		out.summary = fmt.Sprintf("cancelled the owner invitation of %s", s.nameOf(m.Key))
		if declining {
			out.summary = "declined to become an owner"
		}
	case m.OwnerInvited:
		return errConflict("that member has already been invited")
	default:
		if len(s.owners.Owners) >= identity.MaxOwners {
			return errInvalid(fmt.Sprintf("a community can have at most %d owners", identity.MaxOwners))
		}
		m.OwnerInvited = true
		out.summary = fmt.Sprintf("invited %s to become an owner", s.nameOf(m.Key))
	}
	if err := s.saveMember(ctx, s.db, m); err != nil {
		return err
	}
	out.broadcast = true
	return nil
}

// opSubmitOwnerKey records the invited member's owner key request, for an
// owner to sign into the set.
func (s *Server) opSubmitOwnerKey(ctx context.Context, actor *memberRec, op *wire.SubmitOwnerKey, out *adminOutcome) error {
	if !actor.OwnerInvited || s.heldOwnerKey(actor.Key) != nil {
		return errDenied("you haven't been invited to become an owner")
	}
	req, err := identity.VerifyOwnerAdd(op.Add, s.ownerKey)
	if err != nil {
		return errInvalid("the owner key request is invalid")
	}
	if !bytes.Equal(req.Member, actor.Key) {
		return errDenied("the owner key request is for another member")
	}
	if s.owners.Has(req.OwnerKey) || s.memberOfOwner(req.OwnerKey) != nil {
		return errInvalid("that key is or was already an owner")
	}
	b, _ := proto.Marshal(op.Add)
	actor.OwnerAdd = b
	if err := s.saveMember(ctx, s.db, actor); err != nil {
		return err
	}
	out.summary = fmt.Sprintf("submitted an owner key %s", identity.Fingerprint(req.OwnerKey))
	out.broadcast = true
	return nil
}

// opChangeOwners applies an owner change signed by the acting owner, with a
// new cert for the current server key under the changed set.
func (s *Server) opChangeOwners(ctx context.Context, actor *memberRec, op *wire.ChangeOwners, out *adminOutcome) error {
	if err := s.needOwner(actor); err != nil {
		return err
	}
	if op.Change == nil {
		return errInvalid("an owner change needs the change and a new certificate")
	}
	mine := s.ownerKeyOf(actor.Key)
	var c wire.OwnerChange
	if err := proto.Unmarshal(op.Change.Change, &c); err != nil {
		return errInvalid("malformed owner change")
	}
	if !bytes.Equal(c.Signer, mine) {
		return errDenied("sign owner changes with your own owner key")
	}
	if rm, ok := c.Op.(*wire.OwnerChange_Remove); ok && bytes.Equal(rm.Remove, mine) {
		// Removing yourself: no cert (any attached one is ignored).
		return s.opResignOwner(ctx, actor, op.Change, out)
	}
	if op.Cert == nil {
		return errInvalid("an owner change needs the change and a new certificate")
	}
	next := s.owners.Clone()
	if _, err := next.Apply(op.Change); err != nil {
		return errInvalid("the owner change is invalid: " + err.Error())
	}
	var target, removed *memberRec
	switch o := c.Op.(type) {
	case *wire.OwnerChange_Add:
		req, _ := identity.VerifyOwnerAdd(o.Add, s.ownerKey)
		target = s.members[string(req.Member)]
		var asked wire.OwnerAdd
		if target == nil || target.Status != wire.MemberStatus_MEMBER_STATUS_ACTIVE || !target.OwnerInvited ||
			proto.Unmarshal(target.OwnerAdd, &asked) != nil || !bytes.Equal(asked.Request, o.Add.Request) {
			return errDenied("that member hasn't asked to become an owner with this key")
		}
		out.summary = fmt.Sprintf("added %s as an owner (key %s)", s.nameOf(target.Key), identity.Fingerprint(req.OwnerKey))
	case *wire.OwnerChange_Remove:
		who := s.memberOfOwner(o.Remove)
		removed = s.members[string(who)]
		out.summary = fmt.Sprintf("removed %s as an owner (key %s)", s.nameOf(who), identity.Fingerprint(o.Remove))
	}
	// The cert's chain is the server's chain plus this change, whatever the
	// client attached.
	cert := proto.Clone(op.Cert).(*wire.SignedServerCert)
	cert.OwnerChain = append(append([]*wire.SignedOwnerChange(nil), s.currentChain()...), op.Change)
	v, err := identity.VerifyCert(cert, s.key.Public, s.ownerKey)
	if err != nil {
		return errInvalid("the new certificate is invalid: " + err.Error())
	}
	if !bytes.Equal(v.Cert.OwnerKey, mine) {
		return errDenied("sign the new certificate with your own owner key")
	}
	if v.Cert.Seq <= s.certSeq() {
		return errInvalid("the new certificate must have a higher sequence number")
	}
	before := s.viewSets()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	b, _ := proto.Marshal(cert)
	if err := s.setMeta(ctx, tx, "cert", b); err != nil {
		return err
	}
	var added memberRec
	if target != nil {
		added = *target
		added.OwnerInvited, added.OwnerAdd = false, nil
		if err := s.saveMember(ctx, tx, &added); err != nil {
			return err
		}
	}
	// A removed owner keeps nothing ownership gave them: no roles (the
	// founder got Admin at claim), no pending leave.
	var demoted memberRec
	if removed != nil {
		demoted = *removed
		demoted.Roles, demoted.OwnerLeaving = nil, false
		if err := s.saveMember(ctx, tx, &demoted); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if target != nil {
		*target = added
	}
	if removed != nil {
		*removed = demoted
	}
	s.cert, s.owners = cert, v.Owners
	out.broadcast = true
	out.publish = true
	// Channels a removed owner could read only as an owner get new keys.
	return s.rotateChannels(ctx, lostAccess(before, s.viewSets()))
}

// opResignOwner handles an owner removing their own key. They can't sign a
// cert under the changed set, so the chain and cert stay as they are until
// another owner signs the removal (their client does it when it sees the
// leaving flag); the server stops treating the member as an owner now and
// drops their roles. Another owner who isn't leaving must remain.
func (s *Server) opResignOwner(ctx context.Context, actor *memberRec, change *wire.SignedOwnerChange, out *adminOutcome) error {
	next := s.owners.Clone()
	if _, err := next.Apply(change); err != nil {
		return errInvalid("the owner change is invalid: " + err.Error())
	}
	remaining := 0
	for _, k := range next.Owners {
		if m := s.memberOfOwner(k); m != nil && s.ownerKeyOf(m) != nil {
			remaining++
		}
	}
	if remaining == 0 {
		return errInvalid("the last owner can't be removed: another owner who isn't leaving must remain")
	}
	mine := s.ownerKeyOf(actor.Key)
	before := s.viewSets()
	leaving := *actor
	leaving.OwnerLeaving, leaving.Roles = true, nil
	if err := s.saveMember(ctx, s.db, &leaving); err != nil {
		return err
	}
	*actor = leaving
	out.summary = fmt.Sprintf("asked to be removed as an owner (key %s)", identity.Fingerprint(mine))
	out.broadcast = true
	return s.rotateChannels(ctx, lostAccess(before, s.viewSets()))
}

// opCommitRotation switches to the prepared server key once an owner
// certified it under the current owner set.
func (s *Server) opCommitRotation(ctx context.Context, actor *memberRec, c *wire.CommitKeyRotation, out *adminOutcome) error {
	if err := s.needOwner(actor); err != nil {
		return err
	}
	if s.pendingKey == nil {
		return errConflict("no key rotation in progress")
	}
	if c.Cert == nil {
		return errInvalid("the certificate for the new key is missing")
	}
	cert := proto.Clone(c.Cert).(*wire.SignedServerCert)
	cert.OwnerChain = s.currentChain()
	v, err := identity.VerifyCert(cert, s.pendingKey.Public, s.ownerKey)
	if err != nil {
		return errInvalid("the certificate for the new key is invalid")
	}
	if !bytes.Equal(v.Cert.OwnerKey, s.ownerKeyOf(actor.Key)) {
		return errDenied("sign the new certificate with your own owner key")
	}
	if v.Cert.Seq <= s.certSeq() {
		return errInvalid("the new certificate must have a higher sequence number")
	}
	if err := identity.WriteSeed(filepath.Join(s.cfg.DataDir, "server.key"), s.pendingKey); err != nil {
		return err
	}
	b, _ := proto.Marshal(cert)
	if err := s.setMeta(ctx, s.db, "cert", b); err != nil {
		return err
	}
	newKey := s.pendingKey
	s.key = newKey
	s.cert = cert
	s.pendingKey = nil
	// Channel topics commit to the server key; move them to the new one.
	// The key change already happened, so a failure here is logged and
	// retried at the next start (Open recommits too).
	if err := s.recommitTopics(ctx); err != nil {
		s.log.Error("moving channel topics to the new server key failed", "err", err)
	}
	out.result.NewServerKey = newKey.Public
	out.summary = fmt.Sprintf("rotated the server key to %s", identity.Fingerprint(newKey.Public))
	out.broadcast = true
	out.publish = true
	out.after = func() { s.reRegister(context.Background(), newKey) }
	return nil
}
