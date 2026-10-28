package community

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// MaxActiveJoinKeys caps a community's invites that aren't revoked
// (docs/operations.md "Quotas"; expired keys count until revoked).
var MaxActiveJoinKeys = 1000

// MaxRoles caps a community's roles, @everyone included (docs/operations.md
// "Quotas").
const MaxRoles = 250

// ManualBackupInterval is the least time between two on-demand backups
// (ServerBackup).
const ManualBackupInterval = 10 * time.Minute

// maxActionSkew bounds how old (or how far in the future) a signed admin
// action may be. Nonces are remembered for twice this long.
const (
	maxActionSkew  = 5 * time.Minute
	maxActionNonce = 64
	maxActionBytes = 512 << 10 // the largest real action is a settings change of a few KB
)

// admin verifies and applies a signed admin action, then records it in the
// audit log (docs/protocol.md §4.4).
func (s *Server) admin(ctx context.Context, ss *sess, sa *wire.SignedAction) (*wire.ServerMessage, error) {
	return s.adminWith(ctx, ss, sa, nil)
}

// adminWith is admin with data that travels next to the signed action (an
// emoji image, whose hash the action carries; emoji.go).
func (s *Server) adminWith(ctx context.Context, ss *sess, sa *wire.SignedAction, attach []byte) (*wire.ServerMessage, error) {
	var a wire.AdminAction
	if err := proto.Unmarshal(sa.Action, &a); err != nil {
		return nil, errInvalid("malformed action")
	}
	if string(a.Actor) != string(ss.member) {
		return nil, errDenied("action is signed for another member")
	}
	if err := identity.Verify(a.Actor, identity.CtxAdminAction, sa.Action, sa.Signature); err != nil {
		return nil, errDenied("bad action signature")
	}
	now := s.now()
	if d := now.Sub(time.UnixMilli(a.At)); d > maxActionSkew || d < -maxActionSkew {
		return nil, errInvalid("action timestamp is too far from the server's clock")
	}
	if len(a.Nonce) < 16 || len(a.Nonce) > maxActionNonce {
		return nil, errInvalid("action nonce must be 16 to 64 bytes")
	}
	// The action is kept verbatim in the audit log; one that is mostly
	// padding (unknown fields, or a huge nonce) is nobody's real request.
	if len(sa.Action) > maxActionBytes || len(a.ProtoReflect().GetUnknown()) > 0 {
		return nil, errInvalid("action too large")
	}

	s.mu.Lock()
	if string(a.Community) != string(s.ownerKey) {
		s.mu.Unlock()
		return nil, errDenied("action is for another community")
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO action_nonces (nonce, at) VALUES (?, ?)`, a.Nonce, now.UnixMilli()); err != nil {
		s.mu.Unlock()
		return nil, errConflict("action was already applied")
	}
	actor := s.members[string(ss.member)]
	if actor == nil || actor.Status != wire.MemberStatus_MEMBER_STATUS_ACTIVE {
		s.mu.Unlock()
		return nil, errNotMember()
	}
	res := &adminOutcome{result: &wire.AdminResult{}, attach: attach}
	err := s.applyAction(ctx, actor, &a, res)
	if err != nil {
		s.bestEffort(ctx, `DELETE FROM action_nonces WHERE nonce = ?`, a.Nonce)
		s.mu.Unlock()
		return nil, err
	}
	if res.summary != "" {
		if err := s.writeAudit(ctx, &auditRec{At: now.UnixMilli(), Actor: ss.member, Summary: res.summary, Action: sa.Action, Signature: sa.Signature}); err != nil {
			s.log.Error("audit write failed", "err", err)
		}
	}
	s.mu.Unlock()

	// Before the sessions close: the block names their relay streams.
	if res.netBlock != nil {
		s.applyNetBlock(ctx, res.netBlock, res.result)
	}
	if res.netUnblock != nil {
		s.liftNetBlocks(ctx, res.netUnblock, "")
	}
	for _, r := range res.removed {
		s.afterRemovalNoKeep(r.key, r.status)
	}
	if res.broadcast || len(res.removed) > 0 {
		// The state carries the queue counts too.
		s.broadcastState()
	} else if res.modQueue {
		s.pushModQueue()
	}
	if res.admitted != nil {
		for _, k := range res.admitted {
			s.admitPending(k)
		}
	}
	if res.publish {
		s.requestPublish()
	}
	if res.relays {
		// Let the reply and the new state (with the relay list) reach members
		// before leaving the relays they're on.
		time.AfterFunc(2*time.Second, s.kickRelayManager)
	}
	if res.after != nil {
		res.after()
	}
	if res.create {
		code, err := s.host.Create(ctx)
		if err != nil {
			return nil, errConflict(err.Error())
		}
		res.result.ClaimCode = code
		s.mu.Lock()
		if err := s.writeAudit(ctx, &auditRec{At: now.UnixMilli(), Actor: ss.member, Summary: "started another community on this server", Action: sa.Action, Signature: sa.Signature}); err != nil {
			s.log.Error("audit write failed", "err", err)
		}
		s.mu.Unlock()
	}
	if res.backup {
		path, err := s.Backup(ctx)
		if err != nil {
			return nil, err
		}
		res.result.BackupPath = path
		s.mu.Lock()
		if err := s.writeAudit(ctx, &auditRec{At: now.UnixMilli(), Actor: ss.member, Summary: "made a server backup", Action: sa.Action, Signature: sa.Signature}); err != nil {
			s.log.Error("audit write failed", "err", err)
		}
		s.mu.Unlock()
	}
	// Roles, overrides and toggles may have changed what people in calls
	// may do.
	s.voiceRecheck()
	// Never the summary: it names members, channels, roles and join keys.
	s.log.Info("admin action", "action", actionName(&a), "actor", identity.Fingerprint(ss.member), "id", res.result.Id)
	return &wire.ServerMessage{Body: &wire.ServerMessage_AdminResult{AdminResult: res.result}}, nil
}

// actionName is the admin action's type (its oneof field name), which is
// what server logs record instead of the action's content.
func actionName(a *wire.AdminAction) string {
	m := a.ProtoReflect()
	if fd := m.WhichOneof(m.Descriptor().Oneofs().ByName("op")); fd != nil {
		return string(fd.Name())
	}
	return "unknown"
}

// writeAudit appends an audit entry, sealed under the storage key; callers
// hold s.mu.
func (s *Server) writeAudit(ctx context.Context, e *auditRec) error {
	id, err := s.nextID(ctx, s.db, "audit")
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO audit (id, data) VALUES (?, ?)`, id, s.sealJSON("audit", "data", rowID(id), e))
	return err
}

type removal struct {
	key    []byte
	status wire.MemberStatus
}

type adminOutcome struct {
	summary   string
	result    *wire.AdminResult
	broadcast bool
	publish   bool
	relays    bool
	removed   []removal
	admitted  [][]byte
	after     func()
	// attach is data sent next to the signed action (adminWith).
	attach []byte
	// backup asks adminWith for a server backup once s.mu is released.
	backup bool
	// create asks for a new community on this server's host, likewise.
	create bool
	// netBlock is a banned member whose network the dedicated relays are
	// to block, and netUnblock an unbanned one whose blocks are to be
	// lifted, once s.mu is released (netblock.go).
	netBlock   []byte
	netUnblock []byte
	// modQueue: the moderator queues' counts changed (reports.go).
	modQueue bool
}

func (s *Server) afterRemovalNoKeep(member []byte, status wire.MemberStatus) {
	for _, ss := range s.sessionsFor(member) {
		s.endSessionOne(ss, status)
	}
}

// admitPending tells a pending member's open sessions they are in.
func (s *Server) admitPending(member []byte) {
	defer s.presenceChanged(member)
	for _, ss := range s.sessionsFor(member) {
		ss.setStatus(wire.MemberStatus_MEMBER_STATUS_ACTIVE)
		st := s.buildState(member)
		ss.send(&wire.ServerMessage{Body: &wire.ServerMessage_StateChanged{StateChanged: &wire.StateChanged{
			State: st, Status: wire.MemberStatus_MEMBER_STATUS_ACTIVE,
		}}})
	}
}

func (s *Server) need(actor *memberRec, perm uint64) error {
	if s.basePerms(actor)&perm != perm {
		return errDenied("you don't have permission to do that")
	}
	return nil
}

func (s *Server) nameOf(key []byte) string {
	if m := s.members[string(key)]; m != nil && m.Name != "" {
		return m.Name
	}
	return identity.Fingerprint(key)
}

// applyAction runs one op; callers hold s.mu.
func (s *Server) applyAction(ctx context.Context, actor *memberRec, a *wire.AdminAction, out *adminOutcome) error {
	switch op := a.Op.(type) {
	case *wire.AdminAction_UpdateSettings:
		return s.opSettings(ctx, actor, op.UpdateSettings, out)
	case *wire.AdminAction_CreateJoinKey:
		if err := s.need(actor, PermCreateInvite); err != nil {
			return err
		}
		if s.settings.Lockdown {
			return errDenied("raid mode is on: no new invites until it's turned off")
		}
		if op.CreateJoinKey.Public {
			if err := s.need(actor, PermManageCommunity); err != nil {
				return err
			}
		}
		// An invite that grants roles is role management.
		if len(op.CreateJoinKey.RoleIds) > 0 {
			if err := s.need(actor, PermManageRoles); err != nil {
				return err
			}
		}
		for _, r := range op.CreateJoinKey.RoleIds {
			if err := s.canGrantRole(actor, r); err != nil {
				return err
			}
		}
		jk, err := s.createJoinKey(ctx, s.db, actor.Key, op.CreateJoinKey, s.now().UnixMilli())
		if err != nil {
			return err
		}
		out.result.JoinKey = jk
		out.summary = fmt.Sprintf("created join key %q", jk.Label)
		out.publish = jk.Public
	case *wire.AdminAction_RevokeJoinKey:
		k, err := s.joinKeyByID(ctx, op.RevokeJoinKey.Id)
		if err != nil {
			return err
		}
		// Anyone who may create invites revokes their own; Manage
		// Community revokes any.
		if err := s.need(actor, PermManageCommunity); err != nil {
			if string(k.Rec.CreatedBy) != string(actor.Key) || s.need(actor, PermCreateInvite) != nil {
				return errDenied("you can only revoke invites you created")
			}
		}
		if err := s.revokeJoinKeys(ctx, s.db, `WHERE id = ?`, k.ID); err != nil {
			return err
		}
		out.summary = fmt.Sprintf("revoked join key %q", k.Rec.Label)
		out.publish = k.Public
	case *wire.AdminAction_CreateChannel:
		return s.opCreateChannel(ctx, actor, op.CreateChannel, out)
	case *wire.AdminAction_EditChannel:
		return s.opEditChannel(ctx, actor, op.EditChannel, out)
	case *wire.AdminAction_DeleteChannel:
		return s.opDeleteChannel(ctx, actor, op.DeleteChannel, out)
	case *wire.AdminAction_CreateCategory:
		return s.opCreateCategory(ctx, actor, op.CreateCategory, out)
	case *wire.AdminAction_EditCategory:
		return s.opEditCategory(ctx, actor, op.EditCategory, out)
	case *wire.AdminAction_DeleteCategory:
		return s.opDeleteCategory(ctx, actor, op.DeleteCategory, out)
	case *wire.AdminAction_ReorderCategories:
		return s.opReorderCategories(ctx, actor, op.ReorderCategories, out)
	case *wire.AdminAction_ReorderChannels:
		return s.opReorderChannels(ctx, actor, op.ReorderChannels, out)
	case *wire.AdminAction_CreateRole:
		return s.opCreateRole(ctx, actor, op.CreateRole, out)
	case *wire.AdminAction_EditRole:
		return s.opEditRole(ctx, actor, op.EditRole, out)
	case *wire.AdminAction_DeleteRole:
		return s.opDeleteRole(ctx, actor, op.DeleteRole, out)
	case *wire.AdminAction_ReorderRoles:
		return s.opReorderRoles(ctx, actor, op.ReorderRoles, out)
	case *wire.AdminAction_SetChannelOverride:
		return s.opOverride(ctx, actor, op.SetChannelOverride, out)
	case *wire.AdminAction_AssignRoles:
		return s.opAssignRoles(ctx, actor, op.AssignRoles, out)
	case *wire.AdminAction_VoiceMute:
		return s.opVoiceMute(ctx, actor, op.VoiceMute, out)
	case *wire.AdminAction_VoiceDeafen:
		return s.opVoiceDeafen(ctx, actor, op.VoiceDeafen, out)
	case *wire.AdminAction_VoiceMove:
		return s.opVoiceMove(ctx, actor, op.VoiceMove, out)
	case *wire.AdminAction_Kick:
		return s.opRemove(ctx, actor, op.Kick.Member, op.Kick.Reason, false, 0, out)
	case *wire.AdminAction_Ban:
		if op.Ban.BlockNetwork {
			if err := s.need(actor, PermBan); err != nil {
				return err
			}
			if err := s.checkNetBlock(op.Ban.BlockSeconds); err != nil {
				return err
			}
		}
		if err := s.opRemove(ctx, actor, op.Ban.Member, op.Ban.Reason, true, op.Ban.DeleteMessageSeconds, out); err != nil {
			return err
		}
		if op.Ban.BlockNetwork {
			if err := s.requestNetBlock(ctx, op.Ban.Member, actor.Key, op.Ban.BlockSeconds); err != nil {
				return err
			}
			out.netBlock = op.Ban.Member
			out.summary += " (network address block requested)"
		}
		return nil
	case *wire.AdminAction_Unban:
		if err := s.need(actor, PermBan); err != nil {
			return err
		}
		res, err := s.db.ExecContext(ctx, `DELETE FROM bans WHERE kid = ?`, s.kid(op.Unban.Member))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errNotFound("that key isn't banned")
		}
		out.summary = fmt.Sprintf("unbanned %s", identity.Fingerprint(op.Unban.Member))
		lift, err := s.releaseNetBlocks(ctx, op.Unban.Member)
		if err != nil {
			return err
		}
		if lift {
			out.netUnblock = op.Unban.Member
			out.summary += " and asked the relay to lift their network block"
		}
	case *wire.AdminAction_BulkRemove:
		return s.opBulkRemove(ctx, actor, op.BulkRemove, out)
	case *wire.AdminAction_Approve:
		return s.opDecide(ctx, actor, op.Approve.Member, true, out)
	case *wire.AdminAction_Deny:
		return s.opDecide(ctx, actor, op.Deny.Member, false, out)
	case *wire.AdminAction_RaidRevoke:
		return s.opRaidRevoke(ctx, actor, op.RaidRevoke, out)
	case *wire.AdminAction_ReplaceMember:
		return s.opReplace(ctx, actor, op.ReplaceMember, out)
	case *wire.AdminAction_InviteOwner:
		return s.opInviteOwner(ctx, actor, op.InviteOwner, out)
	case *wire.AdminAction_SubmitOwnerKey:
		return s.opSubmitOwnerKey(ctx, actor, op.SubmitOwnerKey, out)
	case *wire.AdminAction_ChangeOwners:
		return s.opChangeOwners(ctx, actor, op.ChangeOwners, out)
	case *wire.AdminAction_SetNickname:
		return s.opSetNickname(ctx, actor, op.SetNickname, out)
	case *wire.AdminAction_PrepareKeyRotation:
		if err := s.needOwner(actor); err != nil {
			return err
		}
		k, err := identity.Generate()
		if err != nil {
			return err
		}
		s.pendingKey = k
		out.result.NewServerKey = k.Public
	case *wire.AdminAction_CommitKeyRotation:
		return s.opCommitRotation(ctx, actor, op.CommitKeyRotation, out)
	case *wire.AdminAction_CreateCommunity:
		if err := s.needOwner(actor); err != nil {
			return err
		}
		if s.host == nil {
			return errDisabled("this server runs one community only")
		}
		out.create = true
	case *wire.AdminAction_ServerBackup:
		// Owners only, and not too often: each backup prunes the oldest
		// beyond backup_keep, so frequent on-demand backups would rotate
		// out every good one (and each is a full copy of the database).
		if err := s.needOwner(actor); err != nil {
			return err
		}
		now := s.now()
		if !s.lastManualBackup.IsZero() && now.Sub(s.lastManualBackup) < ManualBackupInterval {
			return errRateLimited(fmt.Sprintf("a backup was made in the last %d minutes; try again later", int(ManualBackupInterval/time.Minute)))
		}
		s.lastManualBackup = now
		// Taken after s.mu is released (adminWith), so the server keeps
		// serving while the snapshot is written.
		out.backup = true
	case *wire.AdminAction_Timeout:
		return s.opTimeout(ctx, actor, op.Timeout, out)
	case *wire.AdminAction_BulkDelete:
		return s.opBulkDelete(ctx, actor, op.BulkDelete, out)
	case *wire.AdminAction_SaveAutomodRule:
		return s.opSaveAutoModRule(ctx, actor, op.SaveAutomodRule, out)
	case *wire.AdminAction_DeleteAutomodRule:
		return s.opDeleteAutoModRule(ctx, actor, op.DeleteAutomodRule, out)
	case *wire.AdminAction_ResolveReport:
		return s.opResolveReport(ctx, actor, op.ResolveReport, out)
	case *wire.AdminAction_AddEmoji:
		return s.opAddEmoji(ctx, actor, op.AddEmoji, out)
	case *wire.AdminAction_RenameEmoji:
		return s.opRenameEmoji(ctx, actor, op.RenameEmoji, out)
	case *wire.AdminAction_DeleteEmoji:
		return s.opDeleteEmoji(ctx, actor, op.DeleteEmoji, out)
	default:
		return errInvalid("unknown admin action")
	}
	return nil
}

// ---------------------------------------------------------------- settings

func (s *Server) opSettings(ctx context.Context, actor *memberRec, next *wire.CommunitySettings, out *adminOutcome) error {
	if err := s.need(actor, PermManageCommunity); err != nil {
		return err
	}
	next = cloneSettings(next)
	if err := NormalizeSettings(next); err != nil {
		return errInvalid(err.Error())
	}
	if err := s.checkFeatureSettings(next); err != nil {
		return err
	}
	old := s.settings
	// Which relays the community runs on decides who sees every member's
	// IP address and traffic timing, so only owners change it (Manage
	// Community alone would let a moderator move everyone onto a relay
	// they run).
	if relaysChanged(old, next) {
		if err := s.needOwner(actor); err != nil {
			return errDenied("only an owner can change the community's relays")
		}
	}
	b, _ := proto.Marshal(next)
	if err := s.setMeta(ctx, s.db, "settings", b); err != nil {
		return err
	}
	s.settings = next
	s.files.setRate(next.FileBandwidthLimit)
	var after []func()
	if old.SearchEnabled != next.SearchEnabled {
		after = append(after, func() {
			if err := s.rebuildSearch(context.Background()); err != nil {
				s.log.Error("search rebuild", "err", err)
			}
		})
	}
	// Turning voice channels off ends every call.
	if featureOn(old, FeatureVoice) && !featureOn(next, FeatureVoice) {
		var voice []int64
		for id, c := range s.channels {
			if c.Kind == wire.ChannelKind_CHANNEL_KIND_VOICE {
				voice = append(voice, id)
			}
		}
		after = append(after, func() {
			for _, id := range voice {
				s.voiceEndChannel(id, "Voice channels were turned off in this community.")
			}
		})
	}
	out.after = func() {
		for _, f := range after {
			f()
		}
	}
	out.summary = diffSettings(old, next)
	out.broadcast = true
	out.publish = true
	out.relays = relaysChanged(old, next)
	return nil
}

// relaysChanged reports whether the relay mode or pinned list differ.
func relaysChanged(old, next *wire.CommunitySettings) bool {
	return old.RelayMode != next.RelayMode || strings.Join(old.PinnedRelays, ",") != strings.Join(next.PinnedRelays, ",")
}

// ---------------------------------------------------------------- join keys

// joinKeyRow is a join key as stored: plaintext counters and flags plus the
// sealed record.
type joinKeyRow struct {
	ID         int64
	Uses       uint32
	Revoked    bool
	Public     bool
	SecretHash []byte
	Rec        joinKeyRec
}

const joinKeyCols = `id, uses, revoked, public, data, secret_hash, bound`

// queryJoinKeys runs a SELECT of joinKeyCols and decrypts every row.
func (s *Server) queryJoinKeys(ctx context.Context, x dbtx, where string, args ...any) ([]*joinKeyRow, error) {
	rows, err := x.QueryContext(ctx, `SELECT `+joinKeyCols+` FROM join_keys `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*joinKeyRow
	for rows.Next() {
		k := &joinKeyRow{}
		var revoked, public int
		var data, bound []byte
		if err := rows.Scan(&k.ID, &k.Uses, &revoked, &public, &data, &k.SecretHash, &bound); err != nil {
			return nil, err
		}
		k.Revoked, k.Public = revoked != 0, public != 0
		if err := s.openJSON("join_keys", "data", rowID(k.ID), data, &k.Rec); err != nil {
			return nil, err
		}
		if err := s.checkJoinKeyBinding(k, bound); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Server) joinKeyByID(ctx context.Context, id int64) (*joinKeyRow, error) {
	keys, err := s.queryJoinKeys(ctx, s.db, `WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, errNotFound("no such join key")
	}
	return keys[0], nil
}

func (s *Server) createJoinKey(ctx context.Context, x dbtx, creator []byte, c *wire.CreateJoinKey, now int64) (*wire.JoinKey, error) {
	label := strings.TrimSpace(c.Label)
	if utf8.RuneCountInString(label) > 64 {
		return nil, errInvalid("label may be at most 64 characters")
	}
	if c.ExpiresInSeconds > 10*365*24*3600 {
		return nil, errInvalid("expiry is too far away")
	}
	if c.MaxUses > 1_000_000 {
		return nil, errInvalid("max uses is too large")
	}
	var active int
	if err := x.QueryRowContext(ctx, `SELECT count(*) FROM join_keys WHERE revoked = 0`).Scan(&active); err != nil {
		return nil, err
	}
	if active >= MaxActiveJoinKeys {
		return nil, errConflict(fmt.Sprintf("a community has at most %d invites that aren't revoked; revoke some first", MaxActiveJoinKeys))
	}
	secret := identity.Encode(randomBytes(18))
	var expires int64
	if c.ExpiresInSeconds > 0 {
		expires = now + int64(c.ExpiresInSeconds)*1000
	}
	rec := joinKeyRec{Label: label, CreatedBy: creator, CreatedAt: now, ExpiresAt: expires, MaxUses: c.MaxUses, RequestOnly: c.RequestOnly}
	seen := map[int64]bool{}
	for _, r := range c.RoleIds {
		if !seen[r] {
			seen[r] = true
			rec.RoleIDs = append(rec.RoleIDs, r)
		}
	}
	if c.Public {
		// Public keys are republished in the listing, so keep them recoverable.
		rec.Secret = secret
		if err := s.revokeJoinKeys(ctx, x, `WHERE public = 1 AND revoked = 0`); err != nil {
			return nil, err
		}
	}
	id, err := s.nextID(ctx, x, "join_keys")
	if err != nil {
		return nil, err
	}
	bound := s.sealJoinKeyBinding(id, joinKeyBinding{SecretHash: hashSecret(secret), Public: c.Public})
	if _, err := x.ExecContext(ctx, `INSERT INTO join_keys (id, secret_hash, public, data, bound) VALUES (?, ?, ?, ?, ?)`,
		id, hashSecret(secret), boolInt(c.Public), s.sealJSON("join_keys", "data", rowID(id), &rec), bound); err != nil {
		return nil, err
	}
	return &wire.JoinKey{
		Id: id, Label: label, Secret: secret, CreatedBy: creator, CreatedAt: now, ExpiresAt: expires,
		MaxUses: c.MaxUses, RoleIds: rec.RoleIDs, RequestOnly: c.RequestOnly, Public: c.Public,
	}, nil
}

// listJoinKeys lists every invite to members with Manage Community, and
// their own invites to members with Create Invite.
func (s *Server) listJoinKeys(ctx context.Context, ss *sess) (*wire.ServerMessage, error) {
	s.mu.RLock()
	perms := s.basePerms(s.members[string(ss.member)])
	s.mu.RUnlock()
	all := perms&PermManageCommunity != 0
	if !all && perms&PermCreateInvite == 0 {
		return nil, errDenied("you can't create or manage invites")
	}
	keys, err := s.queryJoinKeys(ctx, s.db, `ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	out := &wire.JoinKeyList{}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, k := range keys {
		if !all && string(k.Rec.CreatedBy) != string(ss.member) {
			continue
		}
		jk := &wire.JoinKey{
			Id: k.ID, Label: k.Rec.Label, CreatedBy: k.Rec.CreatedBy, CreatedAt: k.Rec.CreatedAt, ExpiresAt: k.Rec.ExpiresAt,
			MaxUses: k.Rec.MaxUses, Uses: k.Uses, RequestOnly: k.Rec.RequestOnly,
			Revoked: k.Revoked, Public: k.Public, CreatedByName: s.nameOf(k.Rec.CreatedBy),
		}
		// Deleted roles stay in the sealed record; show only live ones.
		for _, r := range k.Rec.RoleIDs {
			if s.roles[r] != nil {
				jk.RoleIds = append(jk.RoleIds, r)
			}
		}
		if k.Public && !k.Revoked {
			jk.Secret = k.Rec.Secret
		}
		out.Keys = append(out.Keys, jk)
	}
	return &wire.ServerMessage{Body: &wire.ServerMessage_JoinKeys{JoinKeys: out}}, nil
}

// publicJoinKey returns the active public join key secret, if any.
func (s *Server) publicJoinKey(ctx context.Context) string {
	keys, err := s.queryJoinKeys(ctx, s.db, `WHERE public = 1 AND revoked = 0 ORDER BY id DESC LIMIT 1`)
	if err != nil {
		s.log.Error("reading the public join key failed", "err", err)
		return ""
	}
	if len(keys) == 0 {
		return ""
	}
	k := keys[0]
	now := s.now().UnixMilli()
	if (k.Rec.ExpiresAt != 0 && now >= k.Rec.ExpiresAt) || (k.Rec.MaxUses != 0 && k.Uses >= k.Rec.MaxUses) {
		return ""
	}
	return k.Rec.Secret
}

// ---------------------------------------------------------------- roles

func (s *Server) canGrantRole(actor *memberRec, roleID int64) error {
	r := s.roles[roleID]
	if r == nil {
		return errNotFound("no such role")
	}
	if r.ID == s.everyoneID {
		return errInvalid("everyone already has @everyone")
	}
	_, roles := s.roleInfos()
	if !s.subject(actor).Owner && r.Position >= TopPosition(s.subject(actor), roles) {
		return errDenied("you can only grant roles below your highest role")
	}
	// Position alone isn't enough: a role below yours may still carry
	// permissions you don't have (Ban, Administrator), and granting it to
	// yourself or a friend would hand them out.
	return s.holdsAll(actor, r.Perms, "grant or take away a role")
}

// holdsAll refuses a non-owner who lacks any of perms. An Administrator
// role can only be handled by owners and administrators, since
// basePerms of anyone else never includes that bit.
func (s *Server) holdsAll(actor *memberRec, perms uint64, what string) error {
	if s.subject(actor).Owner {
		return nil
	}
	if perms&^s.basePerms(actor) != 0 {
		return errDenied(fmt.Sprintf("you can only %s whose permissions you all have yourself", what))
	}
	return nil
}

// canChangePerms checks that the actor holds every permission they add to
// or remove from a role (owners and administrators hold all).
func (s *Server) canChangePerms(actor *memberRec, old, next uint64) error {
	if s.subject(actor).Owner {
		return nil
	}
	if (old^next)&^s.basePerms(actor) != 0 {
		return errDenied("you can't grant or take away permissions you don't have")
	}
	return nil
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func validRoleName(n string) (string, error) {
	n = strings.TrimSpace(n)
	if n == "" || utf8.RuneCountInString(n) > 32 || strings.HasPrefix(n, "@") {
		return "", errInvalid("role names are 1 to 32 characters and can't start with @")
	}
	return n, nil
}

func (s *Server) opCreateRole(ctx context.Context, actor *memberRec, c *wire.CreateRole, out *adminOutcome) error {
	if err := s.need(actor, PermManageRoles); err != nil {
		return err
	}
	name, err := validRoleName(c.Name)
	if err != nil {
		return err
	}
	if c.Permissions&^PermAll != 0 {
		return errInvalid("unknown permission bits")
	}
	if err := s.canChangePerms(actor, 0, c.Permissions); err != nil {
		return err
	}
	if c.Color > 0xFFFFFF {
		return errInvalid("color must be an RGB value")
	}
	if len(s.roles) >= MaxRoles {
		return errConflict(fmt.Sprintf("a community has at most %d roles, @everyone included; delete one first", MaxRoles))
	}
	id, err := s.nextID(ctx, s.db, "roles")
	if err != nil {
		return err
	}
	// New roles go just above @everyone; reorder to move them.
	for _, r := range s.roles {
		if r.Position >= 1 {
			r.Position++
			if err := s.saveRole(ctx, s.db, r); err != nil {
				return err
			}
		}
	}
	r := &roleRec{ID: id, Name: name, Perms: c.Permissions, Position: 1, Color: c.Color, Hoist: c.Hoist, Mentionable: c.Mentionable}
	if err := s.saveRole(ctx, s.db, r); err != nil {
		return err
	}
	s.roles[id] = r
	out.result.Id = id
	out.summary = fmt.Sprintf("created role %s", name)
	out.broadcast = true
	return nil
}

func (s *Server) opEditRole(ctx context.Context, actor *memberRec, e *wire.EditRole, out *adminOutcome) error {
	if err := s.need(actor, PermManageRoles); err != nil {
		return err
	}
	r := s.roles[e.Id]
	if r == nil {
		return errNotFound("no such role")
	}
	_, roles := s.roleInfos()
	if !s.subject(actor).Owner && r.Position >= TopPosition(s.subject(actor), roles) {
		return errDenied("you can only edit roles below your highest role")
	}
	if err := s.holdsAll(actor, r.Perms, "edit a role"); err != nil {
		return err
	}
	if e.Permissions&^PermAll != 0 {
		return errInvalid("unknown permission bits")
	}
	if err := s.canChangePerms(actor, r.Perms, e.Permissions); err != nil {
		return err
	}
	if e.Color > 0xFFFFFF {
		return errInvalid("color must be an RGB value")
	}
	before := s.viewSets()
	next := *r
	if r.ID != s.everyoneID && e.Name != "" {
		name, err := validRoleName(e.Name)
		if err != nil {
			return err
		}
		next.Name = name
	}
	var changes []string
	if next.Name != r.Name {
		changes = append(changes, "renamed from "+r.Name)
	}
	if e.Permissions != r.Perms {
		changes = append(changes, "permissions")
	}
	if e.Color != r.Color {
		changes = append(changes, "color")
	}
	if r.ID != s.everyoneID {
		if e.Hoist != r.Hoist {
			changes = append(changes, fmt.Sprintf("display separately %s", onOff(e.Hoist)))
		}
		if e.Mentionable != r.Mentionable {
			changes = append(changes, fmt.Sprintf("mentionable %s", onOff(e.Mentionable)))
		}
		next.Hoist, next.Mentionable = e.Hoist, e.Mentionable
	}
	next.Perms = e.Permissions
	next.Color = e.Color
	if err := s.saveRole(ctx, s.db, &next); err != nil {
		return err
	}
	s.roles[r.ID] = &next
	if err := s.rotateChannels(ctx, lostAccess(before, s.viewSets())); err != nil {
		return err
	}
	if len(changes) == 0 {
		changes = append(changes, "no changes")
	}
	out.summary = fmt.Sprintf("edited role %s: %s", next.Name, strings.Join(changes, ", "))
	out.broadcast = true
	return nil
}

func (s *Server) opDeleteRole(ctx context.Context, actor *memberRec, d *wire.DeleteRole, out *adminOutcome) error {
	if err := s.need(actor, PermManageRoles); err != nil {
		return err
	}
	r := s.roles[d.Id]
	if r == nil {
		return errNotFound("no such role")
	}
	if r.ID == s.everyoneID {
		return errInvalid("@everyone can't be deleted")
	}
	_, roles := s.roleInfos()
	if !s.subject(actor).Owner && r.Position >= TopPosition(s.subject(actor), roles) {
		return errDenied("you can only delete roles below your highest role")
	}
	if err := s.holdsAll(actor, r.Perms, "delete a role"); err != nil {
		return err
	}
	before := s.viewSets()
	// Members' role lists and channel overrides live in their sealed
	// records, so drop the role from each record that has it.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM roles WHERE id = ?`, r.ID); err != nil {
		return err
	}
	members := map[*memberRec][]int64{}
	for _, m := range s.members {
		if next := without(m.Roles, r.ID); len(next) != len(m.Roles) {
			members[m] = next
			cp := *m
			cp.Roles = next
			if err := s.saveMember(ctx, tx, &cp); err != nil {
				return err
			}
		}
	}
	channels := map[int64]*channelRec{}
	for id, c := range s.channels {
		next := c.clone()
		var kept []Override
		for _, o := range next.Overrides {
			if o.RoleID != r.ID {
				kept = append(kept, o)
			}
		}
		if len(kept) != len(next.Overrides) {
			next.Overrides = kept
			if err := s.saveChannel(ctx, tx, next); err != nil {
				return err
			}
			channels[id] = next
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	delete(s.roles, r.ID)
	for m, roles := range members {
		m.Roles = roles
	}
	for id, c := range channels {
		s.channels[id] = c
	}
	if err := s.rotateChannels(ctx, lostAccess(before, s.viewSets())); err != nil {
		return err
	}
	out.summary = fmt.Sprintf("deleted role %s", r.Name)
	out.broadcast = true
	return nil
}

func without(ids []int64, id int64) []int64 {
	out := ids[:0:0]
	for _, x := range ids {
		if x != id {
			out = append(out, x)
		}
	}
	return out
}

func (s *Server) opReorderRoles(ctx context.Context, actor *memberRec, ro *wire.ReorderRoles, out *adminOutcome) error {
	if err := s.need(actor, PermManageRoles); err != nil {
		return err
	}
	if len(ro.RoleIds) != len(s.roles)-1 {
		return errInvalid("list every role except @everyone, lowest first")
	}
	seen := map[int64]bool{}
	for _, id := range ro.RoleIds {
		if s.roles[id] == nil || id == s.everyoneID || seen[id] {
			return errInvalid("role list has unknown or repeated roles")
		}
		seen[id] = true
	}
	_, roles := s.roleInfos()
	top := TopPosition(s.subject(actor), roles)
	for i, id := range ro.RoleIds {
		r := s.roles[id]
		newPos := int32(i + 1)
		if !s.subject(actor).Owner && r.Position != newPos && (r.Position >= top || newPos >= top) {
			return errDenied("you can only reorder roles below your highest role")
		}
		if r.Position != newPos {
			if err := s.holdsAll(actor, r.Perms, "move a role"); err != nil {
				return err
			}
		}
	}
	for i, id := range ro.RoleIds {
		r := s.roles[id]
		r.Position = int32(i + 1)
		if err := s.saveRole(ctx, s.db, r); err != nil {
			return err
		}
	}
	out.summary = "reordered roles"
	out.broadcast = true
	return nil
}

func (s *Server) opAssignRoles(ctx context.Context, actor *memberRec, a *wire.AssignRoles, out *adminOutcome) error {
	if err := s.need(actor, PermManageRoles); err != nil {
		return err
	}
	m := s.members[string(a.Member)]
	if m == nil || m.Status != wire.MemberStatus_MEMBER_STATUS_ACTIVE {
		return errNotFound("no such member")
	}
	_, roles := s.roleInfos()
	if string(m.Key) != string(actor.Key) && !Outranks(s.subject(actor), s.subject(m), roles) {
		return errDenied("you can only change roles of members below you")
	}
	want := map[int64]bool{}
	for _, id := range a.RoleIds {
		if id == s.everyoneID {
			continue
		}
		want[id] = true
	}
	had := map[int64]bool{}
	for _, id := range m.Roles {
		had[id] = true
	}
	// Every role added or removed must be one the actor may grant.
	for id := range want {
		if !had[id] {
			if err := s.canGrantRole(actor, id); err != nil {
				return err
			}
		}
	}
	for id := range had {
		if !want[id] {
			if err := s.canGrantRole(actor, id); err != nil {
				return err
			}
		}
	}
	before := s.viewSets()
	var next []int64
	for id := range want {
		next = append(next, id)
	}
	m.Roles = next
	if err := s.saveMember(ctx, s.db, m); err != nil {
		return err
	}
	if err := s.rotateChannels(ctx, lostAccess(before, s.viewSets())); err != nil {
		return err
	}
	var names []string
	for _, id := range next {
		names = append(names, s.roles[id].Name)
	}
	out.summary = fmt.Sprintf("set roles of %s to [%s]", s.nameOf(m.Key), strings.Join(names, ", "))
	out.broadcast = true
	return nil
}

// ---------------------------------------------------------------- moderation

func (s *Server) opRemove(ctx context.Context, actor *memberRec, target []byte, reason string, ban bool, deleteSecs uint32, out *adminOutcome) error {
	perm, verb := PermKick, "kicked"
	if ban {
		perm, verb = PermBan, "banned"
	}
	if err := s.need(actor, perm); err != nil {
		return err
	}
	window, ok := banDeleteWindows[deleteSecs]
	if !ok {
		return errInvalid("message history can be deleted for the last hour, 6 hours, 24 hours, 3 days or 7 days")
	}
	if utf8.RuneCountInString(reason) > 512 {
		return errInvalid("reason may be at most 512 characters")
	}
	if string(target) == string(actor.Key) {
		return errInvalid("you can't remove yourself; leave instead")
	}
	m := s.members[string(target)]
	_, roles := s.roleInfos()
	if m != nil && !Outranks(s.subject(actor), s.subject(m), roles) {
		return errDenied("you can only remove members below you")
	}
	if s.isOwner(m) {
		return errDenied("owners can't be kicked or banned; remove them as an owner first")
	}
	if !ban && (m == nil || (m.Status != wire.MemberStatus_MEMBER_STATUS_ACTIVE && m.Status != wire.MemberStatus_MEMBER_STATUS_PENDING)) {
		return errNotFound("no such member")
	}
	if !identity.ValidPublic(target) {
		return errInvalid("not a member key")
	}
	name := s.nameOf(target)
	status := wire.MemberStatus_MEMBER_STATUS_REMOVED
	if ban {
		status = wire.MemberStatus_MEMBER_STATUS_BANNED
		b, err := s.loadBan(ctx, target)
		if err != nil {
			return err
		}
		if b == nil {
			b = &banRec{Key: target, DisplayName: name, BannedBy: actor.Key, At: s.now().UnixMilli()}
		}
		// Banning again only updates the reason.
		b.Reason = reason
		if err := s.saveBan(ctx, s.db, b); err != nil {
			return err
		}
	}
	if m != nil {
		if err := s.removeMember(ctx, m, status); err != nil {
			return err
		}
	}
	var purged int
	if deleteSecs > 0 {
		n, err := s.purgeAuthor(ctx, target, s.now().UnixMilli()-int64(deleteSecs)*1000)
		if err != nil {
			return err
		}
		purged = n
		out.result.Deleted = uint32(n)
	}
	out.removed = append(out.removed, removal{target, status})
	out.summary = fmt.Sprintf("%s %s", verb, name)
	if deleteSecs > 0 {
		out.summary += fmt.Sprintf(" and deleted %s from %s", plural(purged, "message"), window)
	}
	if reason != "" {
		out.summary += ": " + reason
	}
	out.publish = true
	return nil
}

// MaxBulkRemove is how many members one BulkRemove may kick or ban.
const MaxBulkRemove = 100

// opBulkRemove kicks or bans several members. Every one is checked first
// (the same rules as Kick and BanMember), so either all go or none.
func (s *Server) opBulkRemove(ctx context.Context, actor *memberRec, b *wire.BulkRemove, out *adminOutcome) error {
	if len(b.Members) == 0 || len(b.Members) > MaxBulkRemove {
		return errInvalid(fmt.Sprintf("select 1 to %d members", MaxBulkRemove))
	}
	if !b.Ban && b.DeleteMessageSeconds != 0 {
		return errInvalid("only a ban deletes messages")
	}
	_, roles := s.roleInfos()
	seen := map[string]bool{}
	for _, k := range b.Members {
		if seen[string(k)] {
			return errInvalid("a member is listed twice")
		}
		seen[string(k)] = true
		if string(k) == string(actor.Key) {
			return errInvalid("you can't remove yourself; leave instead")
		}
		if !identity.ValidPublic(k) {
			return errInvalid("not a member key")
		}
		m := s.members[string(k)]
		if s.isOwner(m) {
			return errDenied("owners can't be kicked or banned; remove them as an owner first")
		}
		if m != nil && !Outranks(s.subject(actor), s.subject(m), roles) {
			return errDenied(fmt.Sprintf("you can only remove members below you (%s isn't)", s.nameOf(k)))
		}
		if !b.Ban && (m == nil || (m.Status != wire.MemberStatus_MEMBER_STATUS_ACTIVE && m.Status != wire.MemberStatus_MEMBER_STATUS_PENDING)) {
			return errNotFound("no such member")
		}
	}
	var names []string
	var deleted uint32
	for _, k := range b.Members {
		one := &adminOutcome{result: &wire.AdminResult{}}
		if err := s.opRemove(ctx, actor, k, b.Reason, b.Ban, b.DeleteMessageSeconds, one); err != nil {
			return err
		}
		names = append(names, s.nameOf(k))
		deleted += one.result.Deleted
		out.removed = append(out.removed, one.removed...)
	}
	verb := "kicked"
	if b.Ban {
		verb = "banned"
	}
	out.summary = fmt.Sprintf("%s %s: %s", verb, plural(len(names), "member"), strings.Join(names, ", "))
	if b.DeleteMessageSeconds > 0 {
		out.summary += fmt.Sprintf(" and deleted %s from %s", plural(int(deleted), "message"), banDeleteWindows[b.DeleteMessageSeconds])
	}
	if b.Reason != "" {
		out.summary += " (" + b.Reason + ")"
	}
	out.result.Removed = uint32(len(names))
	out.result.Deleted = deleted
	out.publish = true
	return nil
}

func (s *Server) opDecide(ctx context.Context, actor *memberRec, target []byte, approve bool, out *adminOutcome) error {
	if err := s.need(actor, PermApproveMembers); err != nil {
		return err
	}
	m := s.members[string(target)]
	if m == nil || m.Status != wire.MemberStatus_MEMBER_STATUS_PENDING {
		return errNotFound("no pending request from that member")
	}
	now := s.now().UnixMilli()
	if approve {
		m.Status = wire.MemberStatus_MEMBER_STATUS_ACTIVE
		m.JoinedAt = now
		if err := s.saveMember(ctx, s.db, m); err != nil {
			return err
		}
		rotated, err := s.rotateForNewMember(ctx, m)
		if err != nil {
			return err
		}
		out.broadcast = out.broadcast || rotated
		out.admitted = append(out.admitted, target)
		s.postSystem(ctx, wire.SystemEventKind_SYSTEM_EVENT_KIND_JOINED, target)
		out.summary = fmt.Sprintf("approved %s", m.Name)
		out.publish = true
		out.modQueue = true
		return nil
	}
	m.Status = wire.MemberStatus_MEMBER_STATUS_REMOVED
	if err := s.saveMember(ctx, s.db, m); err != nil {
		return err
	}
	out.removed = append(out.removed, removal{target, wire.MemberStatus_MEMBER_STATUS_REMOVED})
	out.summary = fmt.Sprintf("denied %s", m.Name)
	return nil
}

func (s *Server) opRaidRevoke(ctx context.Context, actor *memberRec, r *wire.RaidRevoke, out *adminOutcome) error {
	if err := s.need(actor, PermManageCommunity|PermKick); err != nil {
		return err
	}
	if r.Hours == 0 || r.Hours > 24*30 {
		return errInvalid("hours must be between 1 and 720")
	}
	k, err := s.joinKeyByID(ctx, r.JoinKeyId)
	if err != nil {
		return err
	}
	label := k.Rec.Label
	if err := s.revokeJoinKeys(ctx, s.db, `WHERE id = ?`, r.JoinKeyId); err != nil {
		return err
	}
	cutoff := s.now().Add(-time.Duration(r.Hours) * time.Hour).UnixMilli()
	_, roles := s.roleInfos()
	var n uint32
	for _, m := range s.members {
		if m.JoinKeyID != r.JoinKeyId || m.JoinedAt < cutoff || s.isOwner(m) {
			continue
		}
		if m.Status != wire.MemberStatus_MEMBER_STATUS_ACTIVE && m.Status != wire.MemberStatus_MEMBER_STATUS_PENDING {
			continue
		}
		if !Outranks(s.subject(actor), s.subject(m), roles) {
			continue
		}
		// As a kick: their channels rotate and what the server kept for
		// them goes.
		if err := s.removeMember(ctx, m, wire.MemberStatus_MEMBER_STATUS_REMOVED); err != nil {
			return err
		}
		out.removed = append(out.removed, removal{m.Key, wire.MemberStatus_MEMBER_STATUS_REMOVED})
		n++
	}
	out.result.Removed = n
	out.summary = fmt.Sprintf("raid revoke: revoked join key %q and removed %d members who joined with it in the last %d hours", label, n, r.Hours)
	out.publish = true
	return nil
}

// opReplace moves the old key's roles to the new key, for a member who lost
// every device and rejoined. The new key must already be a member.
func (s *Server) opReplace(ctx context.Context, actor *memberRec, r *wire.ReplaceMember, out *adminOutcome) error {
	if err := s.need(actor, PermManageRoles|PermKick); err != nil {
		return err
	}
	oldM, newM := s.members[string(r.OldKey)], s.members[string(r.NewKey)]
	if oldM == nil || newM == nil || newM.Status != wire.MemberStatus_MEMBER_STATUS_ACTIVE {
		return errNotFound("both keys must be on the member roll, and the new key active")
	}
	if string(r.OldKey) == string(r.NewKey) {
		return errInvalid("the keys are the same")
	}
	if s.isOwner(oldM) {
		return errDenied("an owner can't be replaced this way; remove them as an owner first")
	}
	_, roles := s.roleInfos()
	if !Outranks(s.subject(actor), s.subject(oldM), roles) {
		return errDenied("you can only replace members below you")
	}
	for _, id := range oldM.Roles {
		if err := s.canGrantRole(actor, id); err != nil {
			return err
		}
	}
	set := map[int64]bool{}
	for _, id := range append(append([]int64(nil), newM.Roles...), oldM.Roles...) {
		set[id] = true
	}
	newM.Roles = nil
	for id := range set {
		newM.Roles = append(newM.Roles, id)
	}
	if oldM.JoinedAt < newM.JoinedAt {
		newM.JoinedAt = oldM.JoinedAt
	}
	if err := s.saveMember(ctx, s.db, newM); err != nil {
		return err
	}
	if oldM.Status == wire.MemberStatus_MEMBER_STATUS_ACTIVE || oldM.Status == wire.MemberStatus_MEMBER_STATUS_PENDING {
		if err := s.removeMember(ctx, oldM, wire.MemberStatus_MEMBER_STATUS_REMOVED); err != nil {
			return err
		}
		out.removed = append(out.removed, removal{oldM.Key, wire.MemberStatus_MEMBER_STATUS_REMOVED})
	}
	out.summary = fmt.Sprintf("replaced %s (%s) with %s (%s)", oldM.Name, identity.Fingerprint(oldM.Key), newM.Name, identity.Fingerprint(newM.Key))
	out.broadcast = true
	return nil
}

// ---------------------------------------------------------------- audit log

// auditScanBudget bounds how many sealed audit rows one query decrypts.
// Filters run on decrypted records (actor, target and op are sealed), so a
// filter that matches little returns a partial page with more = true and a
// cursor to continue from (docs/operations.md "Data at rest").
const auditScanBudget = 2000

// auditView is a decrypted audit row with what the filters need, and the
// channel or category the entry concerns (auditVisible): by id, or by
// name for the one that created it.
type auditView struct {
	id     int64
	rec    auditRec
	op     string
	target []byte
	system bool

	channel, category         int64
	channelName, categoryName string
}

func (s *Server) auditView(id int64, data []byte) (*auditView, error) {
	v := &auditView{id: id}
	if err := s.openJSON("audit", "data", rowID(id), data, &v.rec); err != nil {
		return nil, err
	}
	if len(v.rec.Action) == 0 {
		v.system, v.op, v.target, v.channel = true, v.rec.Op, v.rec.Target, v.rec.Channel
		return v, nil
	}
	var a wire.AdminAction
	if err := proto.Unmarshal(v.rec.Action, &a); err != nil {
		return nil, &TamperError{"audit", "data", rowID(id)}
	}
	v.op, v.target = actionName(&a), targetOf(&a)
	switch op := a.Op.(type) {
	case *wire.AdminAction_CreateChannel:
		kind := op.CreateChannel.Kind
		if kind == wire.ChannelKind_CHANNEL_KIND_UNSPECIFIED {
			kind = wire.ChannelKind_CHANNEL_KIND_TEXT
		}
		v.channelName, _ = channelName(kind, op.CreateChannel.Name)
	case *wire.AdminAction_EditChannel:
		v.channel = op.EditChannel.Id
	case *wire.AdminAction_DeleteChannel:
		v.channel = op.DeleteChannel.Id
	case *wire.AdminAction_SetChannelOverride:
		v.channel, v.category = op.SetChannelOverride.ChannelId, op.SetChannelOverride.CategoryId
	case *wire.AdminAction_BulkDelete:
		v.channel = op.BulkDelete.ChannelId
	case *wire.AdminAction_VoiceMove:
		v.channel = op.VoiceMove.ChannelId
	case *wire.AdminAction_CreateCategory:
		v.categoryName, _ = validCategoryName(op.CreateCategory.Name)
	case *wire.AdminAction_EditCategory:
		v.category = op.EditCategory.Id
	case *wire.AdminAction_DeleteCategory:
		v.category = op.DeleteCategory.Id
	case *wire.AdminAction_ReorderChannels:
		v.category = op.ReorderChannels.CategoryId
	}
	return v, nil
}

// auditVisible reports whether an entry may be shown to a member who sees
// the log only because it is open to everyone: entries about a channel
// (or category) they can't see are left out, since the summary names it
// and the action says who was given what there. A channel that no longer
// exists counts as one they can't see. Callers hold s.mu.
func (s *Server) auditVisible(viewer *memberRec, v *auditView) bool {
	sees := func(c *channelRec) bool { return s.channelPerms(viewer, c)&PermViewChannel != 0 }
	switch {
	case v.channel != 0:
		c := s.channels[v.channel]
		return c != nil && sees(c)
	case v.channelName != "":
		for _, c := range s.channels {
			if c.Name == v.channelName && sees(c) {
				return true
			}
		}
		return false
	case v.category != 0:
		for _, c := range s.channelsIn(v.category) {
			if sees(c) {
				return true
			}
		}
		return false
	case v.categoryName != "":
		for _, cat := range s.categories {
			if cat.Name != v.categoryName {
				continue
			}
			for _, c := range s.channelsIn(cat.ID) {
				if sees(c) {
					return true
				}
			}
		}
		return false
	}
	return true
}

func (q *auditFilter) match(v *auditView) bool {
	switch {
	case q.action != "" && v.op != q.action:
		return false
	case len(q.actor) > 0 && string(v.rec.Actor) != string(q.actor):
		return false
	case len(q.target) > 0 && string(v.target) != string(q.target):
		return false
	case q.since > 0 && v.rec.At < q.since:
		return false
	case q.until > 0 && v.rec.At >= q.until:
		return false
	}
	return true
}

type auditFilter struct {
	action        string
	actor, target []byte
	since, until  int64
}

func (s *Server) auditLog(ctx context.Context, ss *sess, q *wire.AuditQuery) (*wire.ServerMessage, error) {
	s.mu.RLock()
	privileged := s.basePerms(s.members[string(ss.member)])&PermViewAuditLog != 0
	allowed := s.settings.AuditVisibility == wire.AuditVisibility_AUDIT_VISIBILITY_EVERYONE || privileged
	s.mu.RUnlock()
	if !allowed {
		return nil, errDenied("the audit log is only visible to admins")
	}
	if len(q.Action) > 64 || len(q.Actor) > 64 || len(q.Target) > 64 {
		return nil, errInvalid("bad audit filter")
	}
	if q.Since > 0 && q.Until > 0 && q.Until <= q.Since {
		return nil, errInvalid("the end of the date range is before its start")
	}
	f := &auditFilter{action: q.Action, actor: q.Actor, target: q.Target, since: q.Since, until: q.Until}
	limit := int(q.Limit)
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	before := q.BeforeId
	if before <= 0 {
		before = 1 << 62
	}
	out := &wire.AuditList{}
	scanned := 0
	cursor := before
	done := false
	for !done && len(out.Entries) < limit && scanned < auditScanBudget {
		batch := min(200, auditScanBudget-scanned)
		rows, err := s.db.QueryContext(ctx, `SELECT id, data FROM audit WHERE id < ? ORDER BY id DESC LIMIT ?`, cursor, batch)
		if err != nil {
			return nil, err
		}
		var views []*auditView
		n := 0
		for rows.Next() {
			var id int64
			var data []byte
			if err := rows.Scan(&id, &data); err != nil {
				rows.Close()
				return nil, err
			}
			n++
			v, err := s.auditView(id, data)
			if err != nil {
				rows.Close()
				return nil, err
			}
			views = append(views, v)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if n < batch {
			done = true
		}
		s.mu.RLock()
		viewer := s.members[string(ss.member)]
		for i, v := range views {
			scanned++
			cursor = v.id
			// Rows are written in time order, so once one is older than
			// `since`, every later row is too.
			if f.since > 0 && v.rec.At < f.since {
				done = true
				break
			}
			if !f.match(v) || (!privileged && !s.auditVisible(viewer, v)) {
				continue
			}
			e := &wire.AuditEntry{
				Id: v.id, At: v.rec.At, Actor: v.rec.Actor, Summary: v.rec.Summary,
				ActionName: v.op, Target: v.target, System: v.system,
			}
			switch {
			case v.system && len(v.rec.Actor) == 0:
				e.ActorName = "AutoMod"
			case v.system:
				// A moderator's message action, recorded by the server.
				e.ActorName = s.nameOf(v.rec.Actor)
			default:
				e.ActorName = s.nameOf(v.rec.Actor)
				// An AutoMod rule's signed action holds its keywords and
				// patterns: with the log open to everyone, only members
				// with View Audit Log get it (others see the summary,
				// which names the rule only), or the filter list would be
				// public and easy to get around.
				if privileged || v.op != "save_automod_rule" {
					e.Action = &wire.SignedAction{Action: v.rec.Action, Signature: v.rec.Signature}
				}
			}
			if len(v.target) > 0 {
				e.TargetName = s.nameOf(v.target)
			}
			out.Entries = append(out.Entries, e)
			if len(out.Entries) == limit {
				// Rows after this one in the batch weren't looked at.
				if i < len(views)-1 {
					done = false
				}
				break
			}
		}
		s.mu.RUnlock()
	}
	if !done {
		out.More = true
		out.NextBeforeId = cursor
	}
	return &wire.ServerMessage{Body: &wire.ServerMessage_Audit{Audit: out}}, nil
}
