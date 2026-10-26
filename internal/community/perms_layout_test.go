package community

import (
	"errors"
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Override precedence: @everyone override, then the member's role overrides
// (denies OR-ed, then allows OR-ed, so a role allow beats a role deny, as in Discord), then
// the member's own override; owners and administrators bypass overrides.
func TestOverridePrecedence(t *testing.T) {
	everyone := RoleInfo{ID: 1, Perms: DefaultEveryonePerms, Position: 0}
	roles := map[int64]RoleInfo{
		1: everyone,
		2: {ID: 2, Perms: PermAdministrator, Position: 30},
		3: {ID: 3, Perms: 0, Position: 10},
		4: {ID: 4, Perms: 0, Position: 5},
	}
	alice := []byte("alice-key-32-bytes-long-........")
	bob := []byte("bob-key-32-bytes-long-..........")
	both := Subject{Key: alice, RoleIDs: []int64{3, 4}}
	plain := Subject{Key: bob}
	admin := Subject{Key: bob, RoleIDs: []int64{2}}
	owner := Subject{Key: bob, Owner: true}

	cases := []struct {
		name string
		sub  Subject
		ovs  []Override
		has  uint64
		not  uint64
	}{
		{"role allow beats role deny", both, []Override{{RoleID: 3, Allow: PermSendMessages}, {RoleID: 4, Deny: PermSendMessages}}, PermViewChannel | PermSendMessages, 0},
		{"role allow beats @everyone deny", both, []Override{{RoleID: 1, Deny: PermSendMessages}, {RoleID: 3, Allow: PermSendMessages}}, PermSendMessages, 0},
		{"member allow beats role deny", both, []Override{{RoleID: 3, Deny: PermSendMessages}, {Member: alice, Allow: PermSendMessages}}, PermSendMessages, 0},
		{"member deny beats role allow", both, []Override{{RoleID: 1, Deny: PermSendMessages}, {RoleID: 3, Allow: PermSendMessages}, {Member: alice, Deny: PermSendMessages}}, PermViewChannel, PermSendMessages},
		{"member allow opens a private channel", both, []Override{{RoleID: 1, Deny: PermViewChannel}, {Member: alice, Allow: PermViewChannel}}, PermViewChannel | PermSendMessages, 0},
		{"someone else's member override", plain, []Override{{Member: alice, Deny: PermViewChannel}}, PermViewChannel, 0},
		{"member deny of view hides everything", both, []Override{{Member: alice, Deny: PermViewChannel}}, 0, PermChannelMask},
		{"member override can't grant community perms", both, []Override{{Member: alice, Allow: PermKick | PermManageRoles}}, PermViewChannel, PermKick | PermManageRoles},
		{"administrator bypasses denies", admin, []Override{{RoleID: 1, Deny: PermViewChannel}, {RoleID: 2, Deny: PermSendMessages}, {Member: bob, Deny: PermViewChannel}}, PermAll, 0},
		{"owner bypasses denies", owner, []Override{{RoleID: 1, Deny: PermViewChannel}, {Member: bob, Deny: PermViewChannel}}, PermAll, 0},
	}
	for _, c := range cases {
		got := ChannelPerms(c.sub, everyone, roles, c.ovs)
		if got&c.has != c.has || got&c.not != 0 {
			t.Errorf("%s: got %x", c.name, got)
		}
	}
}

// The permission layout: every bit named once, all of them exact integers
// in JavaScript (below 2^53), and the documented @everyone defaults.
func TestPermissionLayout(t *testing.T) {
	seen := map[uint64]string{}
	names := map[string]bool{}
	for _, p := range PermissionNames {
		if p.Bit == 0 || p.Bit&(p.Bit-1) != 0 || p.Bit >= 1<<53 {
			t.Errorf("%s: bad bit %x", p.Name, p.Bit)
		}
		if seen[p.Bit] != "" || names[p.Name] {
			t.Errorf("%s: duplicate", p.Name)
		}
		seen[p.Bit], names[p.Name] = p.Name, true
	}
	if len(PermissionNames) != 31 {
		t.Errorf("%d permissions, want 31", len(PermissionNames))
	}
	if PermChannelMask&^PermAll != 0 || DefaultEveryonePerms&^PermAll != 0 {
		t.Fatal("masks name undefined bits")
	}
	on := []uint64{PermViewChannel, PermSendMessages, PermReadMessageHistory, PermAddReactions, PermAttachFiles,
		PermEmbedLinks, PermUseCustomEmoji, PermChangeNickname, PermConnectVoice, PermSpeak, PermUseVoiceActivity, PermVideo}
	var want uint64
	for _, b := range on {
		want |= b
	}
	if DefaultEveryonePerms != want {
		t.Errorf("@everyone defaults %x, want %x", DefaultEveryonePerms, want)
	}
	for _, off := range []uint64{PermCreateInvite, PermMentionEveryone, PermManageChannels, PermManageRoles, PermManageCommunity,
		PermManageMessages, PermManageNicknames, PermManageEmoji, PermKick, PermBan, PermTimeoutMembers, PermAdministrator,
		PermScreenShare, PermPrioritySpeaker, PermMuteMembers, PermDeafenMembers, PermMoveMembers, PermViewAuditLog, PermApproveMembers} {
		if DefaultEveryonePerms&off != 0 {
			t.Errorf("@everyone has %s by default", seen[off])
		}
	}
}

// Schema 4 maps the earlier bits one to one and adds what split off them.
func TestMapV3Perms(t *testing.T) {
	oldEveryone := uint64(1<<0 | 1<<1 | 1<<2 | 1<<12 | 1<<13)
	if got := MapV3RolePerms(oldEveryone, true); got != DefaultEveryonePerms {
		t.Errorf("old @everyone defaults map to %x, want the new defaults %x", got, DefaultEveryonePerms)
	}
	if got := MapV3RolePerms(1<<30, false); got != PermAdministrator {
		t.Errorf("administrator: %x", got)
	}
	if got := MapV3RolePerms(1<<6|1<<7|1<<8|1<<9|1<<10|1<<11|1<<3|1<<4|1<<5, false); got !=
		PermCreateInvite|PermKick|PermBan|PermApproveMembers|PermViewAuditLog|PermManageCommunity|PermManageMessages|PermManageChannels|PermManageRoles {
		t.Errorf("management bits: %x", got)
	}
	// Overrides map bit for bit, without the split-off permissions.
	if got := MapV3Perms(1<<1 | 1<<0); got != PermSendMessages|PermViewChannel {
		t.Errorf("override bits: %x", got)
	}
	if got := MapV3Perms(1 << 20); got != 0 {
		t.Errorf("unknown bit kept: %x", got)
	}
}

// allowed() tells "turned off" (DISABLED) apart from "not permitted"
// (PERMISSION_DENIED), and applies the precedence community > channel >
// permissions.
func TestAllowedPrecedence(t *testing.T) {
	member := &memberRec{Key: []byte("member"), Status: wire.MemberStatus_MEMBER_STATUS_ACTIVE}
	s := &Server{
		settings:   DefaultSettings(),
		roles:      map[int64]*roleRec{1: {ID: 1, Perms: DefaultEveryonePerms, Builtin: true}},
		everyoneID: 1,
		members:    map[string]*memberRec{"member": member},
	}
	ch := &channelRec{ID: 1}
	code := func(err error) wire.RpcErrorCode {
		var re *rpcError
		if errors.As(err, &re) {
			return re.code
		}
		return wire.RpcErrorCode_RPC_ERROR_CODE_UNSPECIFIED
	}
	if err := s.allowed(FeatureReactions, ch, member, PermAddReactions); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	// Permissions decide when the feature is on.
	s.roles[1].Perms &^= PermAddReactions
	if c := code(s.allowed(FeatureReactions, ch, member, PermAddReactions)); c != wire.RpcErrorCode_RPC_ERROR_CODE_PERMISSION_DENIED {
		t.Errorf("without the permission: %v", c)
	}
	s.roles[1].Perms |= PermAddReactions
	// The channel can turn it off.
	ch.Reactions = wire.ChannelToggle_CHANNEL_TOGGLE_OFF
	if c := code(s.allowed(FeatureReactions, ch, member, PermAddReactions)); c != wire.RpcErrorCode_RPC_ERROR_CODE_DISABLED {
		t.Errorf("channel off: %v", c)
	}
	// Community off wins over a channel that inherits, and over permissions.
	ch.Reactions = wire.ChannelToggle_CHANNEL_TOGGLE_INHERIT
	off := false
	s.settings.ReactionsEnabled = &off
	s.roles[1].Perms |= PermAdministrator
	if c := code(s.allowed(FeatureReactions, ch, member, PermAddReactions)); c != wire.RpcErrorCode_RPC_ERROR_CODE_DISABLED {
		t.Errorf("community off: %v", c)
	}
	// Link previews default to off; a settings value without the toggle
	// reads as its default.
	if featureOn(&wire.CommunitySettings{}, FeatureLinkPreviews) || !featureOn(&wire.CommunitySettings{}, FeatureVoice) {
		t.Error("toggle defaults")
	}
	for f, fi := range features {
		if FeatureName(f) == "" || fi.label == "" {
			t.Errorf("feature %d has no name", f)
		}
	}
	if len(features) != len(featureOrder) {
		t.Error("featureOrder misses a feature")
	}
}
