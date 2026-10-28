package community

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// ---------------------------------------------------------------- errors

type rpcError struct {
	code wire.RpcErrorCode
	msg  string
}

func (e *rpcError) Error() string { return e.msg }

func errInvalid(msg string) error { return &rpcError{wire.RpcErrorCode_RPC_ERROR_CODE_INVALID, msg} }
func errDenied(msg string) error {
	return &rpcError{wire.RpcErrorCode_RPC_ERROR_CODE_PERMISSION_DENIED, msg}
}
func errNotFound(msg string) error { return &rpcError{wire.RpcErrorCode_RPC_ERROR_CODE_NOT_FOUND, msg} }
func errConflict(msg string) error { return &rpcError{wire.RpcErrorCode_RPC_ERROR_CODE_CONFLICT, msg} }
func errTooLarge(msg string) error { return &rpcError{wire.RpcErrorCode_RPC_ERROR_CODE_TOO_LARGE, msg} }
func errDisabled(msg string) error { return &rpcError{wire.RpcErrorCode_RPC_ERROR_CODE_DISABLED, msg} }
func errNotMember() error {
	return &rpcError{wire.RpcErrorCode_RPC_ERROR_CODE_NOT_MEMBER, "not a member"}
}
func errRateLimited(msg string) error {
	return &rpcError{wire.RpcErrorCode_RPC_ERROR_CODE_RATE_LIMITED, msg}
}

func errBody(err error) *wire.ServerMessage_Error {
	var re *rpcError
	if errors.As(err, &re) {
		return &wire.ServerMessage_Error{Error: &wire.RpcError{Code: re.code, Message: re.msg}}
	}
	return &wire.ServerMessage_Error{Error: &wire.RpcError{Code: wire.RpcErrorCode_RPC_ERROR_CODE_INTERNAL, Message: "internal error"}}
}

func okBody() *wire.ServerMessage {
	return &wire.ServerMessage{Body: &wire.ServerMessage_Ok{Ok: &wire.Ok{}}}
}

// ---------------------------------------------------------------- dispatch

func (s *Server) dispatch(ctx context.Context, ss *sess, m *wire.ClientMessage) *wire.ServerMessage {
	reply, err := s.route(ctx, ss, m)
	if err != nil {
		var re *rpcError
		if !errors.As(err, &re) {
			s.log.Error("request failed", "kind", fmt.Sprintf("%T", m.Body), "err", err)
		}
		return &wire.ServerMessage{Body: errBody(err)}
	}
	return reply
}

func (s *Server) route(ctx context.Context, ss *sess, m *wire.ClientMessage) (*wire.ServerMessage, error) {
	switch b := m.Body.(type) {
	case *wire.ClientMessage_GetInfo:
		return &wire.ServerMessage{Body: &wire.ServerMessage_Info{Info: s.info()}}, nil
	case *wire.ClientMessage_Claim:
		return s.claim(ctx, ss, b.Claim)
	case *wire.ClientMessage_AdoptServer:
		return s.adoptServer(ctx, ss, b.AdoptServer)
	case *wire.ClientMessage_Join:
		return s.join(ctx, ss, b.Join)
	case *wire.ClientMessage_CheckStatus:
		return s.checkStatus(ss), nil
	}
	if !s.Claimed() {
		return nil, errInvalid("this server has not been claimed yet")
	}
	// Everything below needs an admitted member.
	if ss.getStatus() != wire.MemberStatus_MEMBER_STATUS_ACTIVE {
		return nil, errNotMember()
	}
	switch b := m.Body.(type) {
	case *wire.ClientMessage_Sync:
		st := s.buildState(ss.member)
		if st == nil {
			return nil, errNotMember()
		}
		// Who's in voice follows the state (sent separately: it's the voice
		// state's, under its own lock).
		go s.sendVoiceRooms(ss)
		return &wire.ServerMessage{Body: &wire.ServerMessage_State{State: st}}, nil
	case *wire.ClientMessage_History:
		return s.history(ctx, ss, b.History)
	case *wire.ClientMessage_Post:
		return s.post(ctx, ss, b.Post)
	case *wire.ClientMessage_Edit:
		return s.edit(ctx, ss, b.Edit)
	case *wire.ClientMessage_Delete:
		return s.deleteMessage(ctx, ss, b.Delete)
	case *wire.ClientMessage_Leave:
		return s.leave(ctx, ss)
	case *wire.ClientMessage_SetProfile:
		return s.setProfile(ctx, ss, b.SetProfile)
	case *wire.ClientMessage_GetAvatar:
		return s.getAvatar(ctx, b.GetAvatar)
	case *wire.ClientMessage_Admin:
		return s.admin(ctx, ss, b.Admin)
	case *wire.ClientMessage_ListMembers:
		return s.listMembers(ss)
	case *wire.ClientMessage_ListJoinKeys:
		return s.listJoinKeys(ctx, ss)
	case *wire.ClientMessage_ListPending:
		return s.listPending(ss)
	case *wire.ClientMessage_ListBans:
		return s.listBans(ctx, ss)
	case *wire.ClientMessage_AuditLog:
		return s.auditLog(ctx, ss, b.AuditLog)
	case *wire.ClientMessage_GetInsights:
		return s.getInsights(ctx, ss, b.GetInsights)
	case *wire.ClientMessage_Search:
		return s.searchMessages(ctx, ss, b.Search)
	case *wire.ClientMessage_UploadBegin:
		return s.uploadBegin(ctx, ss, b.UploadBegin)
	case *wire.ClientMessage_UploadChunk:
		return s.uploadChunk(ctx, ss, b.UploadChunk)
	case *wire.ClientMessage_UploadFinish:
		return s.uploadFinish(ctx, ss, b.UploadFinish)
	case *wire.ClientMessage_FileGet:
		return s.fileGet(ctx, ss, b.FileGet)
	case *wire.ClientMessage_ListDevices:
		return s.listDevices(ctx, ss)
	case *wire.ClientMessage_RevokeDevice:
		return s.revokeDevice(ctx, ss, b.RevokeDevice)
	case *wire.ClientMessage_SetStatusDevice:
		return s.setStatusDevice(ctx, ss, b.SetStatusDevice)
	case *wire.ClientMessage_SetStatus:
		return s.setStatus(ctx, ss, b.SetStatus)
	case *wire.ClientMessage_ListStatuses:
		return s.listStatuses(ss, b.ListStatuses)
	case *wire.ClientMessage_SendStatusKeys:
		return s.sendStatusKeys(ctx, ss, b.SendStatusKeys)
	case *wire.ClientMessage_FetchStatusKeys:
		return s.fetchStatusKeys(ctx, ss, b.FetchStatusKeys)
	case *wire.ClientMessage_RequestStatusKey:
		return s.requestStatusKey(ss, b.RequestStatusKey)
	case *wire.ClientMessage_PublishContactCard:
		return s.publishContactCard(ctx, ss, b.PublishContactCard)
	case *wire.ClientMessage_AddKeyPackages:
		return s.addKeyPackages(ctx, ss, b.AddKeyPackages)
	case *wire.ClientMessage_GetContactCard:
		return s.getContactCard(ctx, ss, b.GetContactCard)
	case *wire.ClientMessage_VoiceJoin:
		return s.voiceJoin(ss, b.VoiceJoin)
	case *wire.ClientMessage_VoiceLeave:
		return s.voiceLeave(ss, b.VoiceLeave)
	case *wire.ClientMessage_VoiceState:
		return s.voiceSetState(ss, b.VoiceState)
	case *wire.ClientMessage_React:
		return s.react(ctx, ss, b.React)
	case *wire.ClientMessage_Pin:
		return s.pin(ctx, ss, b.Pin)
	case *wire.ClientMessage_ListPins:
		return s.listPins(ctx, ss, b.ListPins)
	case *wire.ClientMessage_Typing:
		return s.typing(ss, b.Typing)
	case *wire.ClientMessage_UpdateProfile:
		return s.updateProfile(ctx, ss, b.UpdateProfile)
	case *wire.ClientMessage_ReportMessage:
		return s.reportMessage(ctx, ss, b.ReportMessage)
	case *wire.ClientMessage_ListReports:
		return s.listReports(ctx, ss, b.ListReports)
	case *wire.ClientMessage_ListAutomodRules:
		return s.listAutoModRules(ss)
	case *wire.ClientMessage_GetEmoji:
		return s.getEmoji(ctx, b.GetEmoji)
	case *wire.ClientMessage_EmojiUpload:
		return s.emojiUpload(ctx, ss, b.EmojiUpload)
	}
	return nil, errInvalid("unknown request")
}

// ---------------------------------------------------------------- views

func (s *Server) info() *wire.CommunityInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	set := s.settings
	return &wire.CommunityInfo{
		OwnerKey:    s.ownerKey,
		ServerKey:   s.key.Public,
		Name:        set.Name,
		Description: set.Description,
		MemberRange: s.memberRange(),
		JoinPolicy:  set.JoinPolicy,
		Visibility:  set.Visibility,
		Icon:        set.Icon,
		Tags:        set.Tags,
		Language:    set.Language,
		Claimed:     s.ownerKey != nil,
	}
}

// memberProto is m as the roll shows it. Timeouts are shown only to the
// member and to those who can time members out (memberProtoFor).
func (s *Server) memberProto(m *memberRec) *wire.Member {
	return &wire.Member{
		Key:           m.Key,
		DisplayName:   m.Name,
		HasAvatar:     m.HasAvatar,
		RoleIds:       append([]int64(nil), m.Roles...),
		JoinedAt:      m.JoinedAt,
		JoinKeyId:     m.JoinKeyID,
		Status:        m.Status,
		Owner:         s.isOwner(m),
		OwnerInvited:  m.OwnerInvited && !s.isOwner(m),
		AvatarVersion: m.AvatarVer,
		Nickname:      m.Nick,
		About:         m.About,
		Pronouns:      m.Pronouns,
		// An invisible member is shown offline to everyone but themself
		// (memberProtoFor).
		Online: m.Status == wire.MemberStatus_MEMBER_STATUS_ACTIVE && !m.Invisible && s.isOnline(m.Key),
	}
}

// memberProtoFor is m as `viewer` sees it: with its timeout for the member
// themself and for moderators, and with its screening time for the member
// themself. Callers hold s.mu.
func (s *Server) memberProtoFor(m, viewer *memberRec) *wire.Member {
	p := s.memberProto(m)
	self := viewer != nil && string(viewer.Key) == string(m.Key)
	if s.timedOut(m) && (self || s.basePerms(viewer)&PermTimeoutMembers != 0) {
		p.TimeoutUntil = m.TimeoutUntil
	}
	if self {
		p.CanPostAt = s.screenedUntil(m)
		p.Online = m.Status == wire.MemberStatus_MEMBER_STATUS_ACTIVE && s.isOnline(m.Key)
	}
	return p
}

// sortedChannels is the sidebar order: uncategorized channels first, then
// each category's channels, categories and channels by position.
func (s *Server) sortedChannels() []*channelRec {
	out := s.channelsIn(0)
	for _, cat := range s.sortedCategories() {
		out = append(out, s.channelsIn(cat.ID)...)
	}
	return out
}

func overrideProtos(list []Override) []*wire.ChannelOverride {
	var out []*wire.ChannelOverride
	for _, o := range list {
		out = append(out, &wire.ChannelOverride{RoleId: o.RoleID, Allow: o.Allow, Deny: o.Deny, Member: o.Member})
	}
	return out
}

func (s *Server) sortedRoles() []*roleRec {
	out := make([]*roleRec, 0, len(s.roles))
	for _, r := range s.roles {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Position < out[j].Position })
	return out
}

// buildState is the member's view: channels they can read (with keys and
// topics), roles, their own roll entry and the community settings.
func (s *Server) buildState(member []byte) *wire.SyncState {
	info := s.info()
	s.mu.RLock()
	defer s.mu.RUnlock()
	m := s.members[string(member)]
	if m == nil || m.Status != wire.MemberStatus_MEMBER_STATUS_ACTIVE {
		return nil
	}
	base := s.basePerms(m)
	st := &wire.SyncState{
		Info:        info,
		Self:        s.memberProtoFor(m, m),
		Settings:    cloneSettings(s.settings),
		Permissions: base,
		Cert:        s.cert,
		// Bans can then ask for a network block (netblock.go).
		DedicatedRelay: s.dedicatedRelayActiveLocked(),
		// What waits in the queues this member may see (reports.go).
		ModQueue: s.modQueueLocked(context.Background(), m),
	}
	for _, r := range s.sortedRoles() {
		st.Roles = append(st.Roles, &wire.Role{Id: r.ID, Name: r.Name, Permissions: r.Perms, Position: r.Position, Color: r.Color,
			Builtin: r.Builtin, Hoist: r.Hoist, Mentionable: r.Mentionable})
	}
	showOverrides := base&(PermManageChannels|PermManageRoles) != 0
	manage := base&PermManageChannels != 0
	used := map[int64]bool{}
	for _, c := range s.sortedChannels() {
		p := s.channelPerms(m, c)
		if p&PermViewChannel == 0 {
			continue
		}
		used[c.CategoryID] = true
		ch := &wire.Channel{
			Id: c.ID, Name: c.Name, Kind: c.Kind, Position: c.Position,
			Topic: c.Topic, Key: c.Key, KeyEpoch: c.Epoch, Permissions: p,
			MessageRetentionDays: c.MsgRet, FileRetentionDays: c.FileRet,
			Description: c.Description, CategoryId: c.CategoryID, PermissionsSynced: c.Synced, SlowmodeSeconds: c.Slowmode,
			Attachments: c.Attachments, LinkPreviews: c.LinkPreviews, Reactions: c.Reactions, HistoryForNewMembers: c.HistoryForNew,
		}
		if showOverrides {
			ch.Overrides = overrideProtos(c.Overrides)
		}
		st.Channels = append(st.Channels, ch)
	}
	// Categories with a channel the member can see (all of them for
	// members who manage channels).
	for _, cat := range s.sortedCategories() {
		if !manage && !used[cat.ID] {
			continue
		}
		pc := &wire.Category{Id: cat.ID, Name: cat.Name, Position: cat.Position}
		if showOverrides {
			pc.Overrides = overrideProtos(cat.Overrides)
		}
		st.Categories = append(st.Categories, pc)
	}
	st.Owners = s.ownerEntries()
	if s.isOwner(m) {
		st.PendingOwners = s.pendingOwners()
	}
	st.Emoji = s.emojiList()
	return st
}

func (s *Server) checkStatus(ss *sess) *wire.ServerMessage {
	s.mu.RLock()
	m := s.members[string(ss.member)]
	st := wire.MemberStatus_MEMBER_STATUS_UNKNOWN
	if m != nil {
		st = m.Status
	}
	s.mu.RUnlock()
	return &wire.ServerMessage{Body: &wire.ServerMessage_Status{Status: &wire.SessionStatus{Status: st, Claimed: s.Claimed()}}}
}

// ---------------------------------------------------------------- claim

func (s *Server) claim(ctx context.Context, ss *sess, c *wire.Claim) (*wire.ServerMessage, error) {
	s.mu.Lock()
	if s.ownerKey != nil {
		s.mu.Unlock()
		return nil, errConflict("this server has already been claimed")
	}
	if !checkSecret(s.claimHash, c.Secret) {
		s.mu.Unlock()
		return nil, errDenied("wrong claim code")
	}
	v, err := identity.VerifyCert(c.Cert, s.key.Public, nil)
	if err != nil || v.Owners.Seq != 0 || !bytes.Equal(v.Cert.OwnerKey, identity.CertCommunity(v.Cert)) {
		s.mu.Unlock()
		return nil, errInvalid("the owner's certificate for this server is invalid")
	}
	cert := v.Cert
	if string(cert.OwnerKey) == string(ss.member) {
		s.mu.Unlock()
		return nil, errInvalid("the owner key must differ from the owner's member key")
	}
	// The setup wizard sets these; everything else starts from the
	// documented defaults (docs/settings.md).
	settings := DefaultSettings()
	if w := c.Settings; w != nil {
		settings.Name, settings.Description, settings.Tags = w.Name, w.Description, w.Tags
		settings.Language, settings.Icon = w.Language, w.Icon
		if w.Visibility != wire.Visibility_VISIBILITY_UNSPECIFIED {
			settings.Visibility = w.Visibility
		}
		if w.JoinPolicy != wire.JoinPolicy_JOIN_POLICY_UNSPECIFIED {
			settings.JoinPolicy = w.JoinPolicy
		}
		settings.MessageRetentionDays, settings.FileRetentionDays = w.MessageRetentionDays, w.FileRetentionDays
		settings.SearchEnabled = w.SearchEnabled
		if w.MaxFileSize != 0 {
			settings.MaxFileSize = w.MaxFileSize
		}
	}
	if err := NormalizeSettings(settings); err != nil {
		s.mu.Unlock()
		return nil, errInvalid(err.Error())
	}
	name := strings.TrimSpace(c.DisplayName)
	if err := validDisplayName(name); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	defer tx.Rollback()
	everyone := &roleRec{ID: 1, Name: "@everyone", Perms: DefaultEveryonePerms, Position: 0, Builtin: true}
	admin := &roleRec{ID: 2, Name: "Admin", Perms: PermAdministrator, Position: 1, Color: 0xE0A030}
	general := &channelRec{ID: 1, Name: "general", Kind: wire.ChannelKind_CHANNEL_KIND_TEXT, Topic: s.newTopic(), Key: randomBytes(32), Epoch: 1}
	owner := &memberRec{Key: ss.member, Name: name, JoinedAt: now, Status: wire.MemberStatus_MEMBER_STATUS_ACTIVE, Roles: []int64{admin.ID}}
	for _, step := range []func() error{
		func() error { return s.saveRole(ctx, tx, everyone) },
		func() error { return s.saveRole(ctx, tx, admin) },
		func() error { return s.saveChannel(ctx, tx, general) },
		func() error { return s.saveMember(ctx, tx, owner) },
		func() error { return s.setMeta(ctx, tx, "owner_key", cert.OwnerKey) },
		func() error { return s.setMeta(ctx, tx, "owner_member", ss.member) },
		func() error { b, _ := proto.Marshal(c.Cert); return s.setMeta(ctx, tx, "cert", b) },
		func() error { b, _ := proto.Marshal(settings); return s.setMeta(ctx, tx, "settings", b) },
		func() error { _, err := tx.ExecContext(ctx, `DELETE FROM meta WHERE k = 'claim_hash'`); return err },
	} {
		if err := step(); err != nil {
			s.mu.Unlock()
			return nil, err
		}
	}
	invite, err := s.createJoinKey(ctx, tx, ss.member, &wire.CreateJoinKey{Label: "First invite", ExpiresInSeconds: 7 * 24 * 3600}, now)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	s.ownerKey = cert.OwnerKey
	s.ownerMember = append([]byte(nil), ss.member...)
	s.owners = v.Owners
	s.cert = c.Cert
	s.claimHash = nil
	s.settings = settings
	s.roles = map[int64]*roleRec{1: everyone, 2: admin}
	s.everyoneID = 1
	s.channels = map[int64]*channelRec{1: general}
	s.members = map[string]*memberRec{string(ss.member): owner}
	s.files.setRate(settings.FileBandwidthLimit)
	if err := s.recordDevice(ctx, ss.member, ss.device, ss.name); err != nil {
		s.log.Warn("record owner device", "err", err)
	}
	s.mu.Unlock()

	ss.setStatus(wire.MemberStatus_MEMBER_STATUS_ACTIVE)
	s.smu.Lock()
	s.sessions[ss] = struct{}{}
	s.smu.Unlock()
	s.log.Info("community claimed", "owner", identity.Fingerprint(cert.OwnerKey))
	s.removeClaimFile()
	s.requestPublish()
	return &wire.ServerMessage{Body: &wire.ServerMessage_ClaimResult{ClaimResult: &wire.ClaimResult{
		State: s.buildState(ss.member), FirstInvite: invite,
	}}}, nil
}

func validDisplayName(name string) error {
	n := utf8.RuneCountInString(name)
	if n < 1 || n > 64 {
		return errInvalid("display name must be 1 to 64 characters")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return errInvalid("display name contains control characters")
		}
	}
	return nil
}

// ---------------------------------------------------------------- join

func refuse(reason wire.RefusalReason) *wire.ServerMessage {
	return &wire.ServerMessage{Body: &wire.ServerMessage_JoinResult{JoinResult: &wire.JoinResult{
		Status: wire.JoinStatus_JOIN_STATUS_REFUSED, Reason: reason,
	}}}
}

func (s *Server) join(ctx context.Context, ss *sess, j *wire.Join) (*wire.ServerMessage, error) {
	if !s.Claimed() {
		return nil, errInvalid("this server has not been claimed yet")
	}
	name := strings.TrimSpace(j.DisplayName)
	if err := validDisplayName(name); err != nil {
		return nil, err
	}
	s.mu.Lock()
	banned, err := s.banned(ctx, ss.member)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if banned {
		s.mu.Unlock()
		return refuse(wire.RefusalReason_REFUSAL_REASON_BANNED), nil
	}
	existing := s.members[string(ss.member)]
	if existing != nil && existing.Status == wire.MemberStatus_MEMBER_STATUS_ACTIVE {
		s.mu.Unlock()
		return &wire.ServerMessage{Body: &wire.ServerMessage_JoinResult{JoinResult: &wire.JoinResult{
			Status: wire.JoinStatus_JOIN_STATUS_ADMITTED, State: s.buildState(ss.member),
		}}}, nil
	}
	if existing != nil && existing.Status == wire.MemberStatus_MEMBER_STATUS_PENDING {
		s.mu.Unlock()
		return &wire.ServerMessage{Body: &wire.ServerMessage_JoinResult{JoinResult: &wire.JoinResult{Status: wire.JoinStatus_JOIN_STATUS_PENDING}}}, nil
	}
	if max := s.settings.MaxMembers; max > 0 {
		n := 0
		for _, m := range s.members {
			if m.Status == wire.MemberStatus_MEMBER_STATUS_ACTIVE || m.Status == wire.MemberStatus_MEMBER_STATUS_PENDING {
				n++
			}
		}
		if uint32(n) >= max {
			s.mu.Unlock()
			return refuse(wire.RefusalReason_REFUSAL_REASON_FULL), nil
		}
	}
	now := s.now().UnixMilli()
	var keyID int64
	var roles []int64
	pending := false
	if secret := strings.TrimSpace(j.JoinKey); secret != "" {
		keys, err := s.queryJoinKeys(ctx, s.db, `WHERE secret_hash = ?`, hashSecret(secret))
		if err != nil {
			s.mu.Unlock()
			return nil, err
		}
		if len(keys) == 0 {
			s.mu.Unlock()
			return refuse(wire.RefusalReason_REFUSAL_REASON_KEY_INVALID), nil
		}
		k := keys[0]
		switch {
		case k.Revoked:
			s.mu.Unlock()
			return refuse(wire.RefusalReason_REFUSAL_REASON_KEY_REVOKED), nil
		case k.Rec.ExpiresAt != 0 && now >= k.Rec.ExpiresAt:
			s.mu.Unlock()
			return refuse(wire.RefusalReason_REFUSAL_REASON_KEY_EXPIRED), nil
		case k.Rec.MaxUses != 0 && k.Uses >= k.Rec.MaxUses:
			s.mu.Unlock()
			return refuse(wire.RefusalReason_REFUSAL_REASON_KEY_EXHAUSTED), nil
		}
		keyID = k.ID
		pending = k.Rec.RequestOnly
		for _, r := range k.Rec.RoleIDs {
			if s.roles[r] != nil {
				roles = append(roles, r)
			}
		}
	} else {
		switch s.settings.JoinPolicy {
		case wire.JoinPolicy_JOIN_POLICY_OPEN:
		case wire.JoinPolicy_JOIN_POLICY_REQUEST:
			pending = true
		default:
			s.mu.Unlock()
			return refuse(wire.RefusalReason_REFUSAL_REASON_INVITE_REQUIRED), nil
		}
	}
	// Raid mode: everyone new waits for approval, invite or not.
	if s.settings.Lockdown {
		pending = true
	}
	m := &memberRec{Key: ss.member, Name: name, JoinedAt: now, JoinKeyID: keyID, Status: wire.MemberStatus_MEMBER_STATUS_ACTIVE, Roles: roles}
	if existing != nil {
		m.HasAvatar, m.AvatarVer = existing.HasAvatar, existing.AvatarVer
		// A timeout and a server mute or deafen outlast leaving and
		// rejoining, as on Discord.
		if existing.TimeoutUntil > now {
			m.TimeoutUntil = existing.TimeoutUntil
		}
		m.VoiceMuted, m.VoiceDeafened = existing.VoiceMuted, existing.VoiceDeafened
	}
	if pending {
		m.Status = wire.MemberStatus_MEMBER_STATUS_PENDING
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	defer tx.Rollback()
	if err := s.saveMember(ctx, tx, m); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if keyID != 0 {
		if err := s.countJoinKeyUse(ctx, tx, keyID); err != nil {
			s.mu.Unlock()
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	s.members[string(ss.member)] = m
	devErr := s.recordDevice(ctx, ss.member, ss.device, ss.name)
	rotated := false
	if !pending {
		if rotated, err = s.rotateForNewMember(ctx, m); err != nil {
			s.log.Error("channel rotation for a new member failed", "err", err)
		}
		s.postSystem(ctx, wire.SystemEventKind_SYSTEM_EVENT_KIND_JOINED, ss.member)
	}
	s.mu.Unlock()
	if rotated {
		s.broadcastState()
	}
	if devErr != nil && !errors.Is(devErr, errDeviceLimit) {
		s.log.Warn("record device on join", "err", devErr)
	}
	ss.setStatus(m.Status)
	if !pending {
		s.presenceChanged(ss.member)
		s.insightJoined()
		s.insightSeen(ss.member)
	}
	s.log.Info("member joined", "member", identity.Fingerprint(ss.member), "pending", pending, "join_key", keyID)
	s.requestPublish()
	if pending {
		// Members who approve requests see the approval queue grow.
		s.pushModQueue()
		return &wire.ServerMessage{Body: &wire.ServerMessage_JoinResult{JoinResult: &wire.JoinResult{Status: wire.JoinStatus_JOIN_STATUS_PENDING}}}, nil
	}
	return &wire.ServerMessage{Body: &wire.ServerMessage_JoinResult{JoinResult: &wire.JoinResult{
		Status: wire.JoinStatus_JOIN_STATUS_ADMITTED, State: s.buildState(ss.member),
	}}}, nil
}

// ---------------------------------------------------------------- messages

// storedMessage is a messages row. The body (author, text, reply-to, edit
// time, attachments, mentions, reactions, link preview, system event) is
// sealed under the key of the month the message was created in.
type storedMessage struct {
	id, channelID, created int64
	deleted                bool
	month                  int
	body                   []byte
}

const msgCols = `id, channel_id, created_at, deleted, month, body`

func scanStored(sc interface{ Scan(...any) error }) (storedMessage, error) {
	var r storedMessage
	var deleted int
	err := sc.Scan(&r.id, &r.channelID, &r.created, &deleted, &r.month, &r.body)
	r.deleted = deleted != 0
	return r, err
}

// bodyOf is what a message stores in its sealed body: files and the preview
// image by id only (their keys live in the files table). The channel id and
// creation time are sealed too, binding the plaintext channel_id and
// created_at columns (openBody checks them).
func bodyOf(m *wire.Message) *wire.Message {
	b := &wire.Message{
		ChannelId: m.ChannelId, CreatedAt: m.CreatedAt, Author: m.Author, Text: m.Text, ReplyTo: m.ReplyTo, EditedAt: m.EditedAt,
		Mentions: m.Mentions, MentionRoles: m.MentionRoles, MentionEveryone: m.MentionEveryone, MentionHere: m.MentionHere,
		MentionChannel: m.MentionChannel, Reactions: m.Reactions, System: m.System,
	}
	for _, f := range m.Files {
		b.Files = append(b.Files, &wire.FileRef{FileId: f.FileId})
	}
	if p := m.Preview; p != nil {
		b.Preview = &wire.LinkPreview{Url: p.Url, Title: p.Title, Description: p.Description, SiteName: p.SiteName}
		if p.Image != nil {
			b.Preview.Image = &wire.FileRef{FileId: p.Image.FileId}
		}
	}
	return b
}

// sealBody seals a message body under its month key. Only a new message may
// create the month key; edits and deletions reuse the existing one.
func (s *Server) sealBody(ctx context.Context, id int64, month int, body *wire.Message, create bool) ([]byte, error) {
	b, err := proto.Marshal(bodyOf(body))
	if err != nil {
		return nil, err
	}
	k, err := s.monthKey(ctx, month, create)
	if err != nil {
		return nil, err
	}
	return sealWith(k, "messages", "body", rowID(id), b), nil
}

// openBody decrypts a stored body and checks that the row's plaintext
// channel_id and created_at are the ones sealed with it: a row moved to
// another channel, or made to look newer or older (which decides what new
// members see and what a ban deletes), is reported, not shown.
func (s *Server) openBody(ctx context.Context, r storedMessage) (*wire.Message, error) {
	m, err := s.openSealedBody(ctx, r)
	if err != nil {
		return nil, err
	}
	if m.ChannelId != r.channelID {
		return nil, &TamperError{"messages", "channel_id", rowID(r.id)}
	}
	if m.CreatedAt != r.created {
		return nil, &TamperError{"messages", "created_at", rowID(r.id)}
	}
	return m, nil
}

func (s *Server) openSealedBody(ctx context.Context, r storedMessage) (*wire.Message, error) {
	pt, err := s.openMonth(ctx, r.month, "messages", "body", rowID(r.id), r.body)
	if err != nil {
		return nil, err
	}
	var m wire.Message
	if err := proto.Unmarshal(pt, &m); err != nil {
		return nil, &TamperError{"messages", "body", rowID(r.id)}
	}
	return &m, nil
}

// scanMessage decrypts one stored message with its file refs; callers hold
// s.mu for reading. A body that fails to decrypt is an error, never a
// silently missing message.
func (s *Server) scanMessage(ctx context.Context, r storedMessage) (*wire.Message, error) {
	body, err := s.openBody(ctx, r)
	if err != nil {
		return nil, err
	}
	return s.messageView(ctx, r, body), nil
}

// messageView is the message as members get it: the stored body plus the
// author's shown name, pin state and full file refs. Callers hold s.mu.
func (s *Server) messageView(ctx context.Context, r storedMessage, body *wire.Message) *wire.Message {
	m := &wire.Message{Id: r.id, ChannelId: r.channelID, Author: body.Author, CreatedAt: r.created, EditedAt: body.EditedAt, Deleted: r.deleted}
	m.AuthorName = s.shownName(body.Author)
	if r.deleted {
		return m
	}
	m.Text, m.ReplyTo, m.System = body.Text, body.ReplyTo, body.System
	m.Mentions, m.MentionRoles, m.MentionEveryone, m.MentionHere = body.Mentions, body.MentionRoles, body.MentionEveryone, body.MentionHere
	m.MentionChannel = body.MentionChannel
	m.Reactions = body.Reactions
	m.Pinned = s.pinned[r.id]
	for _, f := range body.Files {
		m.Files = append(m.Files, s.fileRef(ctx, f.FileId))
	}
	if p := body.Preview; p != nil {
		m.Preview = &wire.LinkPreview{Url: p.Url, Title: p.Title, Description: p.Description, SiteName: p.SiteName}
		if p.Image != nil {
			if ref := s.fileRef(ctx, p.Image.FileId); len(ref.Key) > 0 {
				m.Preview.Image = ref
			}
		}
	}
	return m
}

// shownName is a member's nickname, else their display name; callers hold s.mu.
func (s *Server) shownName(key []byte) string {
	if mr := s.members[string(key)]; mr != nil {
		if mr.Nick != "" {
			return mr.Nick
		}
		return mr.Name
	}
	return ""
}

func (s *Server) readableChannel(ss *sess, id int64, need uint64) (*channelRec, error) {
	c := s.channels[id]
	if c == nil {
		return nil, errNotFound("no such channel")
	}
	m := s.members[string(ss.member)]
	if s.channelPerms(m, c)&need != need {
		return nil, errDenied("you don't have permission in this channel")
	}
	return c, nil
}

func (s *Server) history(ctx context.Context, ss *sess, h *wire.History) (*wire.ServerMessage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, err := s.readableChannel(ss, h.ChannelId, PermViewChannel)
	if err != nil {
		return nil, err
	}
	m := s.members[string(ss.member)]
	if s.channelPerms(m, c)&PermReadMessageHistory == 0 {
		return nil, errDenied("you can't read the message history of this channel")
	}
	// "New members see history" off: nothing from before the member joined.
	var since int64
	if !s.historyVisible(m, c, 0) {
		since = m.JoinedAt
	}
	limit := int(h.Limit)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var rows *sql.Rows
	switch {
	case h.AfterId > 0:
		rows, err = s.db.QueryContext(ctx, `SELECT `+msgCols+` FROM messages WHERE channel_id = ? AND created_at >= ? AND id > ? ORDER BY id ASC LIMIT ?`, h.ChannelId, since, h.AfterId, limit+1)
	case h.BeforeId > 0:
		rows, err = s.db.QueryContext(ctx, `SELECT `+msgCols+` FROM messages WHERE channel_id = ? AND created_at >= ? AND id < ? ORDER BY id DESC LIMIT ?`, h.ChannelId, since, h.BeforeId, limit+1)
	default:
		rows, err = s.db.QueryContext(ctx, `SELECT `+msgCols+` FROM messages WHERE channel_id = ? AND created_at >= ? ORDER BY id DESC LIMIT ?`, h.ChannelId, since, limit+1)
	}
	if err != nil {
		return nil, err
	}
	var raw []storedMessage
	for rows.Next() {
		r, err := scanStored(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		raw = append(raw, r)
	}
	rows.Close()
	out := &wire.MessageList{}
	if len(raw) > limit {
		out.More = true
		raw = raw[:limit]
	}
	for _, r := range raw {
		m, err := s.scanMessage(ctx, r)
		if err != nil {
			return nil, err
		}
		out.Messages = append(out.Messages, m)
	}
	// Always oldest first.
	if h.AfterId == 0 {
		for i, j := 0, len(out.Messages)-1; i < j; i, j = i+1, j-1 {
			out.Messages[i], out.Messages[j] = out.Messages[j], out.Messages[i]
		}
	}
	return &wire.ServerMessage{Body: &wire.ServerMessage_Messages{Messages: out}}, nil
}

func validText(text string) (string, error) {
	text, err := validOptionalText(text)
	if err == nil && text == "" {
		return "", errInvalid("message is empty")
	}
	return text, err
}

// validOptionalText allows an empty text (a message that only carries files).
func validOptionalText(text string) (string, error) {
	text = strings.TrimRight(text, " \t\r\n")
	if strings.TrimSpace(text) == "" {
		return "", nil
	}
	if utf8.RuneCountInString(text) > MaxMessageChars {
		return "", errTooLarge(fmt.Sprintf("messages may be at most %d characters", MaxMessageChars))
	}
	return text, nil
}

// insertMessage stores a message (body: author, text, reply-to, mentions,
// system event) with its attachments and fans it out; callers hold s.mu.
func (s *Server) insertMessage(ctx context.Context, c *channelRec, body *wire.Message, fileIDs []int64) (*wire.Message, error) {
	now := s.now().UnixMilli()
	month := monthOf(now)
	// Create the month key before the transaction (atrest.go monthKey).
	if _, err := s.monthKey(ctx, month, true); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO messages (channel_id, created_at, month) VALUES (?, ?, ?)`, c.ID, now, month)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	body.ChannelId, body.CreatedAt = c.ID, now
	for _, fid := range fileIDs {
		if err := s.attachFile(ctx, tx, fid, id); err != nil {
			return nil, err
		}
		body.Files = append(body.Files, &wire.FileRef{FileId: fid})
	}
	sealed, err := s.sealBody(ctx, id, month, body, false)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE messages SET body = ? WHERE id = ?`, sealed, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.counters.messages.Add(1)
	s.insightPost(body.Author, body.System != nil)
	if body.System == nil {
		s.recordPost(c, body.Author, now)
	}
	// Ban history deletion finds the messages a member posted through this
	// index (moderation.go); join and leave notices aren't theirs to delete.
	if body.System == nil {
		s.recent.add(body.Author, id, now)
	}
	m := s.messageView(ctx, storedMessage{id: id, channelID: c.ID, created: now, month: month}, body)
	s.indexMessage(id, c.ID, m.Text, m.Files)
	s.publishEvent(c, &wire.ChannelEvent{Kind: &wire.ChannelEvent_Message{Message: m}})
	return m, nil
}

// gateText applies what every new or edited text must pass: a timeout or
// member screening, custom emoji rules and AutoMod. hits come from
// autoModCheck, run before taking s.mu. It returns the AutoMod outcome
// for after the message is stored, or the error to refuse it with (and
// what to run after unlocking, for a blocked message's alerts).
// Callers hold s.mu for writing.
func (s *Server) gateText(ctx context.Context, m *memberRec, c *channelRec, text string, hits []autoModHit) (*autoModOutcome, []func(), error) {
	if err := s.participationError(m); err != nil {
		return nil, nil, err
	}
	if err := s.newMemberTextError(m, text); err != nil {
		return nil, nil, err
	}
	if err := s.checkEmojiRefs(m, c, text); err != nil {
		return nil, nil, err
	}
	o := s.autoModFilter(hits, m, c.ID)
	if o != nil && o.blocked != nil {
		return nil, s.autoModAct(ctx, o, m, c.ID, 0), o.blocked
	}
	return o, nil, nil
}

// preGate checks, under a read lock, that the member may write in the
// channel at all (not timed out or screened, and holding need there) before
// AutoMod's matching looks at their text outside s.mu. The full checks run
// again under the write lock. Callers don't hold s.mu.
func (s *Server) preGate(ss *sess, channelID int64, need uint64) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.participationError(s.members[string(ss.member)]); err != nil {
		return err
	}
	_, err := s.readableChannel(ss, channelID, need)
	return err
}

// unlockThen releases s.mu and runs what was deferred until then.
func (s *Server) unlockThen(after *[]func()) {
	s.mu.Unlock()
	for _, f := range *after {
		f()
	}
}

func (s *Server) post(ctx context.Context, ss *sess, p *wire.Post) (*wire.ServerMessage, error) {
	text, err := validOptionalText(p.Text)
	if err != nil {
		return nil, err
	}
	if text == "" && len(p.FileIds) == 0 {
		return nil, errInvalid("message is empty")
	}
	if err := s.preGate(ss, p.ChannelId, PermViewChannel|PermSendMessages); err != nil {
		return nil, err
	}
	hits := s.autoModCheck(text)
	s.mu.Lock()
	var after []func()
	defer s.unlockThen(&after)
	me := s.members[string(ss.member)]
	if err := s.participationError(me); err != nil {
		return nil, err
	}
	c, err := s.readableChannel(ss, p.ChannelId, PermViewChannel|PermSendMessages)
	if err != nil {
		return nil, err
	}
	if err := s.messagesAllowed(c); err != nil {
		return nil, err
	}
	if err := s.checkSlowmode(c, me); err != nil {
		return nil, err
	}
	if p.ReplyTo != 0 {
		n, err := s.count(ctx, `SELECT count(*) FROM messages WHERE id = ? AND channel_id = ?`, p.ReplyTo, c.ID)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, errNotFound("the message you replied to is gone")
		}
	}
	files, err := s.claimStaged(ctx, ss, c, p.FileIds)
	if err != nil {
		return nil, err
	}
	// AutoMod last: it acts (alerts, timeouts) only on a post that every
	// other check allows.
	o, blockedAfter, err := s.gateText(ctx, me, c, text, hits)
	if err != nil {
		after = blockedAfter
		return nil, err
	}
	body := &wire.Message{Author: ss.member, Text: text, ReplyTo: p.ReplyTo}
	s.parseMentions(me, c, text).apply(body)
	m, err := s.insertMessage(ctx, c, body, files)
	if err != nil {
		return nil, err
	}
	// Posting ends typing: the next keystroke may announce typing again.
	ss.mu.Lock()
	delete(ss.typing, c.ID)
	ss.mu.Unlock()
	s.schedulePreview(me, c, m)
	after = s.autoModAct(ctx, o, me, c.ID, m.Id)
	return &wire.ServerMessage{Body: &wire.ServerMessage_Message{Message: m}}, nil
}

// loadForUpdate reads and decrypts one message; callers hold s.mu.
func (s *Server) loadForUpdate(ctx context.Context, id int64) (storedMessage, *wire.Message, error) {
	r, err := scanStored(s.db.QueryRowContext(ctx, `SELECT `+msgCols+` FROM messages WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, nil, errNotFound("no such message")
	}
	if err != nil {
		return r, nil, err
	}
	body, err := s.openBody(ctx, r)
	return r, body, err
}

// rewriteBody reseals a changed body; callers hold s.mu.
func (s *Server) rewriteBody(ctx context.Context, r storedMessage, body *wire.Message) error {
	sealed, err := s.sealBody(ctx, r.id, r.month, body, false)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE messages SET body = ? WHERE id = ?`, sealed, r.id)
	return err
}

func (s *Server) edit(ctx context.Context, ss *sess, e *wire.Edit) (*wire.ServerMessage, error) {
	text, err := validOptionalText(e.Text)
	if err != nil {
		return nil, err
	}
	var channelID int64
	if err := s.db.QueryRowContext(ctx, `SELECT channel_id FROM messages WHERE id = ?`, e.MessageId).Scan(&channelID); err != nil {
		return nil, errNotFound("no such message")
	}
	if err := s.preGate(ss, channelID, PermViewChannel|PermSendMessages); err != nil {
		return nil, err
	}
	hits := s.autoModCheck(text)
	s.mu.Lock()
	var after []func()
	defer s.unlockThen(&after)
	r, old, err := s.loadForUpdate(ctx, e.MessageId)
	if err != nil {
		return nil, err
	}
	if r.deleted {
		return nil, errNotFound("message was deleted")
	}
	if string(old.Author) != string(ss.member) || old.System != nil {
		return nil, errDenied("you can only edit your own messages")
	}
	me := s.members[string(ss.member)]
	if err := s.participationError(me); err != nil {
		return nil, err
	}
	c, err := s.readableChannel(ss, r.channelID, PermViewChannel|PermSendMessages)
	if err != nil {
		return nil, err
	}
	if err := s.allowed(FeatureEditOwn, c, nil, 0); err != nil {
		return nil, err
	}
	if text == "" && len(old.Files) == 0 {
		return nil, errInvalid("message is empty")
	}
	o, blockedAfter, err := s.gateText(ctx, me, c, text, hits)
	if err != nil {
		after = blockedAfter
		return nil, err
	}
	body := proto.Clone(old).(*wire.Message)
	body.Text, body.EditedAt = text, s.now().UnixMilli()
	body.Mentions, body.MentionRoles, body.MentionEveryone, body.MentionHere = nil, nil, false, false
	body.MentionChannel = false
	author := s.members[string(ss.member)]
	s.parseMentions(author, c, text).apply(body)
	// A preview follows the link: drop it when the first link changed.
	refetch := body.Preview == nil || firstLink(text) != body.Preview.Url
	if refetch && body.Preview != nil {
		if img := body.Preview.Image; img != nil {
			s.bestEffort(ctx, `DELETE FROM files WHERE id = ?`, img.FileId)
		}
		body.Preview = nil
	}
	if err := s.rewriteBody(ctx, r, body); err != nil {
		return nil, err
	}
	m := s.messageView(ctx, r, body)
	s.indexMessage(m.Id, r.channelID, text, m.Files)
	s.publishEvent(c, &wire.ChannelEvent{Kind: &wire.ChannelEvent_Edited{Edited: m}})
	if refetch {
		s.schedulePreview(author, c, m)
	}
	after = s.autoModAct(ctx, o, me, c.ID, m.Id)
	return &wire.ServerMessage{Body: &wire.ServerMessage_Message{Message: m}}, nil
}

func (s *Server) deleteMessage(ctx context.Context, ss *sess, d *wire.DeleteMessage) (*wire.ServerMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, old, err := s.loadForUpdate(ctx, d.MessageId)
	if err != nil {
		return nil, err
	}
	if r.deleted {
		return okBody(), nil
	}
	c, err := s.readableChannel(ss, r.channelID, PermViewChannel)
	if err != nil {
		return nil, err
	}
	manage := s.channelPerms(s.members[string(ss.member)], c)&PermManageMessages != 0
	if !manage {
		if string(old.Author) != string(ss.member) || old.System != nil {
			return nil, errDenied("you can only delete your own messages")
		}
		if err := s.allowed(FeatureDeleteOwn, c, nil, 0); err != nil {
			return nil, err
		}
	}
	// A deleted message keeps only its author.
	body, err := s.sealBody(ctx, r.id, r.month, &wire.Message{ChannelId: r.channelID, CreatedAt: r.created, Author: old.Author}, false)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE messages SET deleted = 1, body = ? WHERE id = ?`, body, r.id); err != nil {
		return nil, err
	}
	s.bestEffort(ctx, `DELETE FROM files WHERE message_id = ?`, r.id)
	s.bestEffort(ctx, `DELETE FROM pins WHERE message_id = ?`, r.id)
	delete(s.pinned, r.id)
	s.unindexMessage(d.MessageId)
	if string(old.Author) != string(ss.member) {
		// A moderator deleting someone else's message (or a join/leave
		// notice): who, whose and where, never the text.
		s.recordAudit(ctx, ss.member, "delete_message", old.Author, c.ID, fmt.Sprintf("deleted a message by %s in #%s", s.nameOf(old.Author), c.Name))
	}
	s.publishEvent(c, &wire.ChannelEvent{Kind: &wire.ChannelEvent_Deleted{Deleted: d.MessageId}})
	return okBody(), nil
}

// ---------------------------------------------------------------- membership

func (s *Server) leave(ctx context.Context, ss *sess) (*wire.ServerMessage, error) {
	s.mu.Lock()
	m := s.members[string(ss.member)]
	if s.isOwner(m) {
		s.mu.Unlock()
		return nil, errConflict("an owner can't leave; remove yourself as an owner first (another owner must remain)")
	}
	if m == nil {
		s.mu.Unlock()
		return okBody(), nil
	}
	if err := s.removeMember(ctx, m, wire.MemberStatus_MEMBER_STATUS_REMOVED); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	s.postSystem(ctx, wire.SystemEventKind_SYSTEM_EVENT_KIND_LEFT, ss.member)
	s.mu.Unlock()
	s.log.Info("member left", "member", identity.Fingerprint(ss.member))
	s.afterRemoval(ss.member, wire.MemberStatus_MEMBER_STATUS_REMOVED, ss)
	return okBody(), nil
}

// removeMember marks a member removed or banned and rotates every channel
// they could read (new key and topic). Callers hold s.mu.
func (s *Server) removeMember(ctx context.Context, m *memberRec, status wire.MemberStatus) error {
	if m.Status == wire.MemberStatus_MEMBER_STATUS_ACTIVE {
		s.insightLeft()
	}
	before := s.viewSets()
	m.Status = status
	m.Roles = nil
	m.OwnerInvited, m.OwnerAdd = false, nil
	if err := s.saveMember(ctx, s.db, m); err != nil {
		return err
	}
	s.bestEffort(ctx, `DELETE FROM contact_cards WHERE member = ?`, s.kid(m.Key))
	s.bestEffort(ctx, `DELETE FROM key_packages WHERE member = ?`, s.kid(m.Key))
	s.statusMemberGone(ctx, m.Key)
	return s.rotateChannels(ctx, lostAccess(before, s.viewSets()))
}

// afterRemoval runs outside s.mu: end the member's sessions (except `keep`,
// which gets its reply first) and push everyone the rotated keys.
func (s *Server) afterRemoval(member []byte, status wire.MemberStatus, keep *sess) {
	for _, ss := range s.sessionsFor(member) {
		if ss == keep {
			// The reply to Leave is queued after this returns; close once it's out.
			ss.setStatus(status)
			go func() {
				time.Sleep(500 * time.Millisecond)
				ss.close("left")
			}()
			continue
		}
		s.endSessionOne(ss, status)
	}
	s.broadcastState()
	s.requestPublish()
}

func (s *Server) endSessionOne(ss *sess, status wire.MemberStatus) {
	ss.setStatus(status)
	ss.send(&wire.ServerMessage{Body: &wire.ServerMessage_StateChanged{StateChanged: &wire.StateChanged{Status: status}}})
	s.voiceDisconnect(ss)
	ss.close("removed")
}

// MaxRelayCacheTTL is the longest a relay's chunk cache may keep a message
// (relay config chunk_cache_ttl is at most an hour).
const MaxRelayCacheTTL = time.Hour

// rotateForNewMember rotates the channels a member who was just admitted
// can read but whose history is hidden from them ("New members see
// history" off for the community or channel, and no Manage Messages
// there), if an event was published to the channel within
// MaxRelayCacheTTL. A relay's chunk cache replays recent channel envelopes
// to any subscriber that asks, and the member's client would hold the key
// that decrypts them; after the rotation the member only learns the new
// topic and key, so nothing published before they joined reaches them
// through a cache. It reports whether any channel rotated (the other
// members then need the new state). Callers hold s.mu.
func (s *Server) rotateForNewMember(ctx context.Context, m *memberRec) (bool, error) {
	var ids []int64
	for id, c := range s.channels {
		if channelFeatureOn(s.settings, FeatureHistoryForNew, c) {
			continue
		}
		perms := s.channelPerms(m, c)
		if perms&PermViewChannel == 0 || perms&PermManageMessages != 0 {
			continue
		}
		s.lpmu.Lock()
		last := s.lastPublish[id]
		s.lpmu.Unlock()
		if last.Before(s.startedAt) {
			last = s.startedAt
		}
		if s.now().Sub(last) >= MaxRelayCacheTTL {
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return false, nil
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return true, s.rotateChannels(ctx, ids)
}

// visibleChannels lists the channels m can see, in id order. Callers hold
// s.mu.
func (s *Server) visibleChannels(m *memberRec) []int64 {
	var ids []int64
	for id, c := range s.channels {
		if s.channelPerms(m, c)&PermViewChannel != 0 {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// rotateChannels gives channels a new key, key epoch and topic, so removed
// readers can neither decrypt nor find new messages. Callers hold s.mu.
func (s *Server) rotateChannels(ctx context.Context, ids []int64) error {
	for _, id := range ids {
		c := s.channels[id]
		if c == nil {
			continue
		}
		next := c.clone()
		next.Key = randomBytes(32)
		next.Topic = s.newTopic()
		next.Epoch++
		if err := s.saveChannel(ctx, s.db, next); err != nil {
			return err
		}
		s.channels[id] = next
		s.log.Info("channel key rotated", "channel", id, "epoch", next.Epoch)
	}
	return nil
}

func (s *Server) setProfile(ctx context.Context, ss *sess, p *wire.SetProfile) (*wire.ServerMessage, error) {
	name := strings.TrimSpace(p.DisplayName)
	if name != "" {
		if err := validDisplayName(name); err != nil {
			return nil, err
		}
	}
	if len(p.Avatar) > 0 {
		if len(p.Avatar) > MaxAvatarBytes {
			return nil, errTooLarge(fmt.Sprintf("avatars may be at most %d KB", MaxAvatarBytes>>10))
		}
		if !IsImage(p.Avatar) {
			return nil, errInvalid("avatar must be a PNG, JPEG, GIF or WebP image of at most 16 megapixels")
		}
	}
	s.mu.Lock()
	m := s.members[string(ss.member)]
	if m == nil {
		s.mu.Unlock()
		return nil, errNotMember()
	}
	if name != "" && name != m.Name && s.timedOut(m) {
		s.mu.Unlock()
		return nil, errDenied(fmt.Sprintf("You're timed out until %s and can't change your nickname.", fmtUTC(m.TimeoutUntil)))
	}
	if name != "" && name != m.Name && s.basePerms(m)&PermChangeNickname == 0 {
		s.mu.Unlock()
		return nil, errDenied("you can't change your name in this community")
	}
	if (p.ClearAvatar || len(p.Avatar) > 0) && s.timedOut(m) {
		s.mu.Unlock()
		return nil, errDenied(fmt.Sprintf("You're timed out until %s and can't change your avatar.", fmtUTC(m.TimeoutUntil)))
	}
	if err := s.profileRateError(ss.member); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	next := *m
	if name != "" {
		next.Name = name
	}
	kid := s.kid(m.Key)
	var avatar []byte
	setAvatar := p.ClearAvatar || len(p.Avatar) > 0
	if setAvatar {
		next.HasAvatar = len(p.Avatar) > 0 && !p.ClearAvatar
		next.AvatarVer++
		if next.HasAvatar {
			avatar = s.seal("members", "avatar", rowKey(kid), p.Avatar)
		}
	}
	if err := s.saveProfile(ctx, &next, setAvatar, avatar); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	*m = next
	s.mu.Unlock()
	s.broadcastState()
	return okBody(), nil
}

func (s *Server) getAvatar(ctx context.Context, g *wire.GetAvatar) (*wire.ServerMessage, error) {
	s.mu.RLock()
	m := s.members[string(g.Member)]
	s.mu.RUnlock()
	if m == nil || !m.HasAvatar {
		return nil, errNotFound("no avatar")
	}
	kid := s.kid(g.Member)
	var enc []byte
	if err := s.db.QueryRowContext(ctx, `SELECT avatar FROM members WHERE kid = ?`, kid).Scan(&enc); err != nil || enc == nil {
		return nil, errNotFound("no avatar")
	}
	avatar, err := s.open("members", "avatar", rowKey(kid), enc)
	if err != nil {
		return nil, err
	}
	return &wire.ServerMessage{Body: &wire.ServerMessage_Avatar{Avatar: &wire.AvatarResult{Member: g.Member, Avatar: avatar, Version: m.AvatarVer}}}, nil
}

// saveProfile writes a member record and, if setAvatar, its sealed avatar
// (nil clears it) in one transaction.
func (s *Server) saveProfile(ctx context.Context, m *memberRec, setAvatar bool, avatar []byte) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.saveMember(ctx, tx, m); err != nil {
		return err
	}
	if setAvatar {
		if _, err := tx.ExecContext(ctx, `UPDATE members SET avatar = ? WHERE kid = ?`, avatar, s.kid(m.Key)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Server) listMembers(ss *sess) (*wire.ServerMessage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := &wire.MemberList{}
	viewer := s.members[string(ss.member)]
	for _, m := range s.members {
		if m.Status == wire.MemberStatus_MEMBER_STATUS_ACTIVE {
			out.Members = append(out.Members, s.memberProtoFor(m, viewer))
		}
	}
	sort.Slice(out.Members, func(i, j int) bool {
		return strings.ToLower(out.Members[i].DisplayName) < strings.ToLower(out.Members[j].DisplayName)
	})
	return &wire.ServerMessage{Body: &wire.ServerMessage_Members{Members: out}}, nil
}

func (s *Server) listPending(ss *sess) (*wire.ServerMessage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.basePerms(s.members[string(ss.member)])&PermApproveMembers == 0 {
		return nil, errDenied("you can't see the approval queue")
	}
	out := &wire.MemberList{}
	for _, m := range s.members {
		if m.Status == wire.MemberStatus_MEMBER_STATUS_PENDING {
			out.Members = append(out.Members, s.memberProto(m))
		}
	}
	sort.Slice(out.Members, func(i, j int) bool { return out.Members[i].JoinedAt < out.Members[j].JoinedAt })
	return &wire.ServerMessage{Body: &wire.ServerMessage_Members{Members: out}}, nil
}

func (s *Server) listBans(ctx context.Context, ss *sess) (*wire.ServerMessage, error) {
	s.mu.RLock()
	allowed := s.basePerms(s.members[string(ss.member)])&PermBan != 0
	s.mu.RUnlock()
	if !allowed {
		return nil, errDenied("you can't see the ban list")
	}
	blocks, err := s.loadNetBlocks(ctx, s.db, nil)
	if err != nil {
		return nil, err
	}
	blocksOf := map[string][]netBlockRow{}
	for _, b := range blocks {
		blocksOf[string(b.rec.Member)] = append(blocksOf[string(b.rec.Member)], b)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT kid, data FROM bans`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &wire.BanList{}
	for rows.Next() {
		var kid, data []byte
		if err := rows.Scan(&kid, &data); err != nil {
			return nil, err
		}
		var b banRec
		if err := s.openJSON("bans", "data", rowKey(kid), data, &b); err != nil {
			return nil, err
		}
		st, until := netBlockStatus(blocksOf[string(b.Key)])
		out.Bans = append(out.Bans, &wire.Ban{Key: b.Key, DisplayName: b.DisplayName, Reason: b.Reason, BannedBy: b.BannedBy, At: b.At,
			NetworkBlock: st, NetworkBlockUntil: until})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out.Bans, func(i, j int) bool { return out.Bans[i].At > out.Bans[j].At })
	return &wire.ServerMessage{Body: &wire.ServerMessage_Bans{Bans: out}}, nil
}

// ---------------------------------------------------------------- devices

func (s *Server) listDevices(ctx context.Context, ss *sess) (*wire.ServerMessage, error) {
	devs, err := s.devicesOf(ctx, ss.member)
	if err != nil {
		return nil, err
	}
	out := &wire.DeviceList{}
	for _, d := range devs {
		out.Devices = append(out.Devices, &wire.Device{DeviceKey: d.DeviceKey, Name: d.Name, FirstSeen: d.FirstSeen, LastSeen: d.LastSeen, Revoked: d.Revoked})
	}
	return &wire.ServerMessage{Body: &wire.ServerMessage_Devices{Devices: out}}, nil
}

// revokeDevice refuses a device key from now on (docs/protocol.md §10.4).
// The revocation must be signed by the member's identity key.
// maxDeviceRecords bounds a member's device rows (connected or revoked
// unseen): all of them are read when the member connects.
const maxDeviceRecords = 64

func (s *Server) revokeDevice(ctx context.Context, ss *sess, sr *wire.SignedDeviceRevocation) (*wire.ServerMessage, error) {
	var r wire.DeviceRevocation
	if err := proto.Unmarshal(sr.Revocation, &r); err != nil {
		return nil, errInvalid("malformed revocation")
	}
	if err := identity.Verify(r.IdentityKey, identity.CtxDeviceRevocation, sr.Revocation, sr.Signature); err != nil {
		return nil, errDenied("bad revocation signature")
	}
	s.mu.RLock()
	owner := s.ownerKey
	s.mu.RUnlock()
	if string(r.IdentityKey) != string(ss.member) || string(r.Community) != string(owner) {
		return nil, errDenied("revocation is for another member or community")
	}
	// Device records are read, changed and resealed, so hold s.mu like the
	// other device writers (admitSession, recordDevice).
	s.mu.Lock()
	d, err := s.loadDevice(ctx, r.DeviceKey)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if d != nil && string(d.Member) != string(ss.member) {
		s.mu.Unlock()
		return nil, errDenied("that device belongs to another member")
	}
	// A device that connected holds the keys and topics of every channel
	// its member can see; like a member's removal, its revocation rotates
	// them so it can't read anything new from the relays.
	rotate := d != nil && !d.Revoked
	if d == nil {
		// Revoking a device that never connected keeps it out for good;
		// a member's record of them stays small (every row is read when
		// the member connects).
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM devices WHERE member = ?`, s.kid(ss.member)).Scan(&n); err != nil {
			s.mu.Unlock()
			return nil, err
		}
		if n >= maxDeviceRecords {
			s.mu.Unlock()
			return nil, errRateLimited(fmt.Sprintf("you have %d devices on record here; revoke ones that connected, not ones that never did", n))
		}
		d = &deviceRec{DeviceKey: r.DeviceKey, Member: ss.member, FirstSeen: r.At, LastSeen: r.At}
	}
	d.Revoked = true
	err = s.saveDevice(ctx, s.db, d)
	if err == nil && rotate {
		err = s.rotateChannels(ctx, s.visibleChannels(s.members[string(ss.member)]))
	}
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if rotate {
		defer s.broadcastState()
	}
	for _, other := range s.sessionsFor(ss.member) {
		if string(other.device) == string(r.DeviceKey) {
			s.endSessionOne(other, wire.MemberStatus_MEMBER_STATUS_DEVICE_REVOKED)
		}
	}
	s.statusDeviceRevoked(ctx, ss.member, r.DeviceKey)
	s.log.Info("device revoked", "member", identity.Fingerprint(ss.member), "device", identity.Fingerprint(r.DeviceKey))
	return okBody(), nil
}
