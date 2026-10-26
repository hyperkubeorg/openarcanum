package community

import "bytes"

// Permission bits (docs/admin-guide.md "Permissions"), grouped with room to
// grow. Mirrored in clients/desktop/src/openarcanum/permissions.py
// (TestPermissionTable and clients/desktop/tests/test_permissions.py keep
// the two in sync). Schema 4 moved the earlier bits to this layout
// (schema_v4.go).
const (
	// General.
	PermViewChannel     uint64 = 1 << 0
	PermManageChannels  uint64 = 1 << 1
	PermManageRoles     uint64 = 1 << 2
	PermManageCommunity uint64 = 1 << 3
	PermViewAuditLog    uint64 = 1 << 4
	PermCreateInvite    uint64 = 1 << 5
	PermChangeNickname  uint64 = 1 << 6
	PermManageNicknames uint64 = 1 << 7
	PermApproveMembers  uint64 = 1 << 8

	// Moderation.
	PermKick           uint64 = 1 << 12
	PermBan            uint64 = 1 << 13
	PermTimeoutMembers uint64 = 1 << 14

	// Text.
	PermSendMessages       uint64 = 1 << 20
	PermEmbedLinks         uint64 = 1 << 21
	PermAttachFiles        uint64 = 1 << 22
	PermAddReactions       uint64 = 1 << 23
	PermUseCustomEmoji     uint64 = 1 << 24
	PermMentionEveryone    uint64 = 1 << 25 // @everyone, @here and every role
	PermManageMessages     uint64 = 1 << 26
	PermReadMessageHistory uint64 = 1 << 27
	PermManageEmoji        uint64 = 1 << 28

	// Voice and video.
	PermConnectVoice     uint64 = 1 << 32
	PermSpeak            uint64 = 1 << 33
	PermVideo            uint64 = 1 << 34
	PermScreenShare      uint64 = 1 << 35
	PermUseVoiceActivity uint64 = 1 << 36
	PermPrioritySpeaker  uint64 = 1 << 37
	PermMuteMembers      uint64 = 1 << 38
	PermDeafenMembers    uint64 = 1 << 39
	PermMoveMembers      uint64 = 1 << 40

	// Everything, and bypasses channel overrides.
	PermAdministrator uint64 = 1 << 48
)

// PermissionNames lists every bit with its stable name (the Python
// mirror's keys), in display order.
var PermissionNames = []struct {
	Name string
	Bit  uint64
}{
	{"view_channel", PermViewChannel},
	{"manage_channels", PermManageChannels},
	{"manage_roles", PermManageRoles},
	{"manage_community", PermManageCommunity},
	{"view_audit_log", PermViewAuditLog},
	{"create_invite", PermCreateInvite},
	{"change_nickname", PermChangeNickname},
	{"manage_nicknames", PermManageNicknames},
	{"approve_members", PermApproveMembers},
	{"kick", PermKick},
	{"ban", PermBan},
	{"timeout_members", PermTimeoutMembers},
	{"send_messages", PermSendMessages},
	{"embed_links", PermEmbedLinks},
	{"attach_files", PermAttachFiles},
	{"add_reactions", PermAddReactions},
	{"use_custom_emoji", PermUseCustomEmoji},
	{"mention_everyone", PermMentionEveryone},
	{"manage_messages", PermManageMessages},
	{"read_message_history", PermReadMessageHistory},
	{"manage_emoji", PermManageEmoji},
	{"connect_voice", PermConnectVoice},
	{"speak", PermSpeak},
	{"video", PermVideo},
	{"screen_share", PermScreenShare},
	{"use_voice_activity", PermUseVoiceActivity},
	{"priority_speaker", PermPrioritySpeaker},
	{"mute_members", PermMuteMembers},
	{"deafen_members", PermDeafenMembers},
	{"move_members", PermMoveMembers},
	{"administrator", PermAdministrator},
}

// PermAll is every defined bit.
var PermAll = func() uint64 {
	var all uint64
	for _, p := range PermissionNames {
		all |= p.Bit
	}
	return all
}()

const (
	// PermChannelMask is the bits a per-channel override may change: the
	// text and voice permissions plus View Channel. Community-level
	// permissions (managing roles, kicking, …) never change per channel.
	PermChannelMask = PermViewChannel | PermSendMessages | PermEmbedLinks | PermAttachFiles |
		PermAddReactions | PermUseCustomEmoji | PermMentionEveryone | PermManageMessages |
		PermReadMessageHistory | PermConnectVoice | PermSpeak | PermVideo | PermScreenShare |
		PermUseVoiceActivity | PermPrioritySpeaker | PermMuteMembers | PermDeafenMembers |
		PermMoveMembers

	// DefaultEveryonePerms are the privacy-preserving @everyone defaults
	// (docs/admin-guide.md "Permission defaults"): members read, write,
	// react and talk; inviting, mentioning everyone, screen sharing and
	// every management permission are off.
	DefaultEveryonePerms = PermViewChannel | PermSendMessages | PermReadMessageHistory |
		PermAddReactions | PermAttachFiles | PermEmbedLinks | PermUseCustomEmoji |
		PermChangeNickname | PermConnectVoice | PermSpeak | PermUseVoiceActivity | PermVideo

	// PermNewMemberMask is what new-member restrictions hold back
	// (settings.new_member_minutes): attaching files, link previews and
	// mentioning everyone or roles. Links and member mentions in text are
	// refused separately (newMemberTextError).
	PermNewMemberMask = PermAttachFiles | PermEmbedLinks | PermMentionEveryone
	// PermScreeningMask is what member screening holds back until the
	// minimum membership age: posting, attaching, reacting and custom emoji.
	PermScreeningMask = PermSendMessages | PermAttachFiles | PermAddReactions | PermUseCustomEmoji
)

// PermTimeoutMask is what a timeout takes away (moderation.go): every
// permission except seeing channels, reading their history and joining
// calls to listen and watch. So a timed-out moderator can't kick, ban,
// manage roles or settings until the timeout ends, and a timed-out member
// adds nothing to a conversation, in text or in calls. Typing, nickname and
// profile changes are refused separately (participationError, setProfile,
// updateProfile, opSetNickname).
var PermTimeoutMask = PermAll &^ (PermViewChannel | PermReadMessageHistory | PermConnectVoice)

// RoleInfo is what permission math needs about a role.
type RoleInfo struct {
	ID       int64
	Perms    uint64
	Position int32
}

// Override is a per-channel (or per-category) allow/deny pair for a role,
// or, when Member is set, for one member. The JSON names are part of the
// sealed channel and category records.
type Override struct {
	RoleID      int64
	Allow, Deny uint64
	Member      []byte `json:",omitempty"`
}

// Subject is a member as far as permissions go.
type Subject struct {
	Owner   bool // a member holding a key in the owner set
	Key     []byte
	RoleIDs []int64
}

// BasePerms is the member's community-level permissions: @everyone plus the
// union of their roles. Owners and administrators get everything.
func BasePerms(sub Subject, everyone RoleInfo, roles map[int64]RoleInfo) uint64 {
	if sub.Owner {
		return PermAll
	}
	p := everyone.Perms
	for _, id := range sub.RoleIDs {
		if r, ok := roles[id]; ok {
			p |= r.Perms
		}
	}
	if p&PermAdministrator != 0 {
		return PermAll
	}
	return p
}

// ChannelPerms applies a channel's overrides on top of the base
// permissions, in this order:
//  1. the @everyone override: its denies, then its allows;
//  2. the overrides of the member's roles: the denies of all of them are
//     OR-ed and applied, then the OR-ed allows, so an allow on one of a
//     member's roles beats a deny on another (as in Discord;
//     docs/admin-guide.md);
//  3. the member's own override: its denies, then its allows.
//
// Owners and administrators bypass overrides. Without View Channel a member
// has no permissions in the channel at all.
func ChannelPerms(sub Subject, everyone RoleInfo, roles map[int64]RoleInfo, overrides []Override) uint64 {
	base := BasePerms(sub, everyone, roles)
	if base&PermAdministrator != 0 {
		return PermAll
	}
	p := base
	held := make(map[int64]bool, len(sub.RoleIDs))
	for _, id := range sub.RoleIDs {
		held[id] = true
	}
	for _, o := range overrides {
		if o.Member == nil && o.RoleID == everyone.ID {
			p &^= o.Deny & PermChannelMask
			p |= o.Allow & PermChannelMask
		}
	}
	var allow, deny uint64
	for _, o := range overrides {
		if o.Member == nil && o.RoleID != everyone.ID && held[o.RoleID] {
			allow |= o.Allow
			deny |= o.Deny
		}
	}
	// As in Discord: all role denies, then all role allows, so between two
	// of a member's roles the allow wins.
	p &^= deny & PermChannelMask
	p |= allow & PermChannelMask
	if sub.Key != nil {
		for _, o := range overrides {
			if o.Member != nil && bytes.Equal(o.Member, sub.Key) {
				p &^= o.Deny & PermChannelMask
				p |= o.Allow & PermChannelMask
			}
		}
	}
	if p&PermViewChannel == 0 {
		return 0
	}
	return p
}

// TopPosition is the highest role position a member holds (0 = @everyone only).
// Owners outrank every role.
func TopPosition(sub Subject, roles map[int64]RoleInfo) int32 {
	if sub.Owner {
		return 1 << 30
	}
	var top int32
	for _, id := range sub.RoleIDs {
		if r, ok := roles[id]; ok && r.Position > top {
			top = r.Position
		}
	}
	return top
}

// Outranks reports whether actor may act on target (kick, ban, change roles).
// Besides position, only an owner or administrator acts on an
// administrator, wherever the administrator's role sits in the list.
func Outranks(actor, target Subject, roles map[int64]RoleInfo) bool {
	if target.Owner && !actor.Owner {
		return false
	}
	if !actor.Owner && holdsAdmin(target, roles) && !holdsAdmin(actor, roles) {
		return false
	}
	return TopPosition(actor, roles) > TopPosition(target, roles)
}

// holdsAdmin reports whether one of sub's roles carries Administrator.
func holdsAdmin(sub Subject, roles map[int64]RoleInfo) bool {
	for _, id := range sub.RoleIDs {
		if r, ok := roles[id]; ok && r.Perms&PermAdministrator != 0 {
			return true
		}
	}
	return false
}
