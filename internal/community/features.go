package community

import (
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Feature toggles (docs/admin-guide.md "Feature toggles"). Precedence:
//  1. community toggle off: the feature is unavailable everywhere;
//  2. community toggle on: the channel's setting applies (inherit or off),
//     for the features that have one;
//  3. the channel allows it: permissions decide who may use it.
//
// allowed() is the one server check; the client mirror is
// clients/desktop/src/openarcanum/permissions.py (feature_on, channel_feature_on).

// Feature names a community toggle.
type Feature int

const (
	FeatureAttachments Feature = iota + 1
	FeatureLinkPreviews
	FeatureReactions
	FeatureCustomEmoji
	FeatureVoice
	FeatureVideo
	FeatureScreenShare
	FeatureSearch
	FeatureContactCards
	FeatureEditOwn
	FeatureDeleteOwn
	FeatureTyping
	FeatureHistoryForNew
	FeatureJoinLeave
)

// featureInfo describes a toggle: its stable name (the client's key), what
// the audit log and errors call it, its default, its settings field, and
// the channel-level setting that can turn it off (nil if none).
type featureInfo struct {
	name    string
	label   string
	def     bool
	get     func(*wire.CommunitySettings) *bool
	channel func(*channelRec) wire.ChannelToggle
}

var features = map[Feature]featureInfo{
	FeatureAttachments: {"attachments", "attachments", true,
		func(s *wire.CommunitySettings) *bool { return s.AttachmentsEnabled },
		func(c *channelRec) wire.ChannelToggle { return c.Attachments }},
	FeatureLinkPreviews: {"link_previews", "link previews", false,
		func(s *wire.CommunitySettings) *bool { return s.LinkPreviewsEnabled },
		func(c *channelRec) wire.ChannelToggle { return c.LinkPreviews }},
	FeatureReactions: {"reactions", "reactions", true,
		func(s *wire.CommunitySettings) *bool { return s.ReactionsEnabled },
		func(c *channelRec) wire.ChannelToggle { return c.Reactions }},
	FeatureCustomEmoji: {"custom_emoji", "custom emoji", true,
		func(s *wire.CommunitySettings) *bool { return s.CustomEmojiEnabled }, nil},
	FeatureVoice: {"voice", "voice channels", true,
		func(s *wire.CommunitySettings) *bool { return s.VoiceEnabled }, nil},
	FeatureVideo: {"video", "video", true,
		func(s *wire.CommunitySettings) *bool { return s.VideoEnabled }, nil},
	FeatureScreenShare: {"screen_share", "screen share", true,
		func(s *wire.CommunitySettings) *bool { return s.ScreenShareEnabled }, nil},
	FeatureSearch: {"search", "search", true,
		func(s *wire.CommunitySettings) *bool { return &s.SearchEnabled }, nil},
	FeatureContactCards: {"contact_cards", "member contact cards", true,
		func(s *wire.CommunitySettings) *bool { return &s.AllowContactCards }, nil},
	FeatureEditOwn: {"edit_own", "editing own messages", true,
		func(s *wire.CommunitySettings) *bool { return s.EditOwnEnabled }, nil},
	FeatureDeleteOwn: {"delete_own", "deleting own messages", true,
		func(s *wire.CommunitySettings) *bool { return s.DeleteOwnEnabled }, nil},
	FeatureTyping: {"typing", "typing indicators", true,
		func(s *wire.CommunitySettings) *bool { return s.TypingEnabled }, nil},
	FeatureHistoryForNew: {"history_for_new_members", "new members see history", true,
		func(s *wire.CommunitySettings) *bool { return s.NewMembersSeeHistory },
		func(c *channelRec) wire.ChannelToggle { return c.HistoryForNew }},
	FeatureJoinLeave: {"join_leave_messages", "join and leave messages", true,
		func(s *wire.CommunitySettings) *bool { return s.JoinLeaveMessages }, nil},
}

// featureOrder is the order the audit log lists changed toggles in.
var featureOrder = []Feature{
	FeatureAttachments, FeatureLinkPreviews, FeatureReactions, FeatureCustomEmoji,
	FeatureVoice, FeatureVideo, FeatureScreenShare, FeatureSearch, FeatureContactCards,
	FeatureEditOwn, FeatureDeleteOwn, FeatureTyping, FeatureHistoryForNew, FeatureJoinLeave,
}

// FeatureName is a toggle's stable name (as the client and docs use it).
func FeatureName(f Feature) string { return features[f].name }

// featureOn reports the community-level toggle, applying its default when
// the settings don't carry it.
func featureOn(set *wire.CommunitySettings, f Feature) bool {
	fi, ok := features[f]
	if !ok || set == nil {
		return false
	}
	if v := fi.get(set); v != nil {
		return *v
	}
	return fi.def
}

// fillFeatureDefaults stores every optional toggle explicitly, so settings
// written before a toggle existed get its documented default.
func fillFeatureDefaults(s *wire.CommunitySettings) {
	set := func(p **bool, def bool) {
		if *p == nil {
			*p = proto.Bool(def)
		}
	}
	set(&s.AttachmentsEnabled, true)
	set(&s.LinkPreviewsEnabled, false)
	set(&s.ReactionsEnabled, true)
	set(&s.CustomEmojiEnabled, true)
	set(&s.VoiceEnabled, true)
	set(&s.VideoEnabled, true)
	set(&s.ScreenShareEnabled, true)
	set(&s.EditOwnEnabled, true)
	set(&s.DeleteOwnEnabled, true)
	set(&s.TypingEnabled, true)
	set(&s.NewMembersSeeHistory, true)
	set(&s.JoinLeaveMessages, true)
	if s.AttachmentKinds == wire.AttachmentKinds_ATTACHMENT_KINDS_UNSPECIFIED {
		s.AttachmentKinds = wire.AttachmentKinds_ATTACHMENT_KINDS_ALL
	}
}

// channelFeatureOn applies precedence steps 1 and 2: the community toggle,
// then the channel's setting (when the feature has one).
func channelFeatureOn(set *wire.CommunitySettings, f Feature, c *channelRec) bool {
	if !featureOn(set, f) {
		return false
	}
	if fi := features[f]; fi.channel != nil && c != nil && fi.channel(c) == wire.ChannelToggle_CHANNEL_TOGGLE_OFF {
		return false
	}
	return true
}

// allowed is the toggle check every feature goes through. It returns a
// DISABLED error (errDisabled) when the community or the channel has the
// feature turned off, and a PERMISSION_DENIED error (errDenied) when the
// feature is on but m lacks perm (in ch if given, else community-wide).
// perm 0 checks only the toggles. Callers hold s.mu (read or write).
func (s *Server) allowed(f Feature, ch *channelRec, m *memberRec, perm uint64) error {
	label := features[f].label
	if !featureOn(s.settings, f) {
		return errDisabled(fmt.Sprintf("this community has turned off %s", label))
	}
	if !channelFeatureOn(s.settings, f, ch) {
		return errDisabled(fmt.Sprintf("this channel has turned off %s", label))
	}
	if perm == 0 {
		return nil
	}
	var have uint64
	if ch != nil {
		have = s.channelPerms(m, ch)
	} else {
		have = s.basePerms(m)
	}
	if have&perm != perm {
		return errDenied("you don't have permission to do that")
	}
	return nil
}

// diffFeatures describes toggle changes for the audit log, one entry per
// changed toggle ("turned reactions off").
func diffFeatures(old, new *wire.CommunitySettings) []string {
	var out []string
	for _, f := range featureOrder {
		if a, b := featureOn(old, f), featureOn(new, f); a != b {
			state := "off"
			if b {
				state = "on"
			}
			out = append(out, fmt.Sprintf("turned %s %s", features[f].label, state))
		}
	}
	if old.JoinLeaveChannel != new.JoinLeaveChannel {
		out = append(out, "changed the join and leave channel")
	}
	if isAllKinds(old.AttachmentKinds) != isAllKinds(new.AttachmentKinds) {
		kinds := "all kinds"
		if new.AttachmentKinds == wire.AttachmentKinds_ATTACHMENT_KINDS_IMAGES_ONLY {
			kinds = "images only"
		}
		out = append(out, "allowed attachments: "+kinds)
	}
	return out
}

func isAllKinds(k wire.AttachmentKinds) bool {
	return k == wire.AttachmentKinds_ATTACHMENT_KINDS_UNSPECIFIED || k == wire.AttachmentKinds_ATTACHMENT_KINDS_ALL
}

// historyVisible reports whether m may read a message created at
// createdAt in c: with "new members see history" off (community or
// channel), only messages from the member's join time on. Owners and
// members who can manage messages in the channel always see everything.
// Callers hold s.mu.
func (s *Server) historyVisible(m *memberRec, c *channelRec, createdAt int64) bool {
	if m == nil || channelFeatureOn(s.settings, FeatureHistoryForNew, c) {
		return true
	}
	if s.channelPerms(m, c)&PermManageMessages != 0 {
		return true
	}
	return createdAt >= m.JoinedAt
}

// attachmentKindAllowed applies the community's allowed attachment kinds to
// a file: by its MIME type before the upload (data nil), by its content
// after. Callers hold s.mu.
func (s *Server) attachmentKindAllowed(mime string, data []byte) error {
	if s.settings.AttachmentKinds != wire.AttachmentKinds_ATTACHMENT_KINDS_IMAGES_ONLY {
		return nil
	}
	if (data != nil && !isImageMagic(data)) || (data == nil && !isImageMime(mime)) {
		return errDisabled("only images (PNG, JPEG, GIF or WebP) can be attached in this community")
	}
	return nil
}

func isImageMime(m string) bool {
	switch m {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	}
	return false
}

// previewsAllowed reports whether the server fetches link previews in c:
// the node's operator hasn't turned the fetcher off (link_previews_disabled)
// and the community and channel toggles allow them. Embed Links decides per
// author. Callers hold s.mu.
func (s *Server) previewsAllowed(c *channelRec) bool {
	return !s.cfg.LinkPreviewsDisabled && s.allowed(FeatureLinkPreviews, c, nil, 0) == nil
}

// checkFeatureSettings checks the toggle settings that need the model or
// the node config: the join and leave channel must be a text channel, and
// link previews can't be turned on when the operator turned the fetcher
// off. Callers hold s.mu.
func (s *Server) checkFeatureSettings(next *wire.CommunitySettings) error {
	if id := next.JoinLeaveChannel; id != 0 {
		c := s.channels[id]
		if c == nil || c.Kind != wire.ChannelKind_CHANNEL_KIND_TEXT {
			return errInvalid("join and leave messages need a text channel")
		}
	}
	if s.cfg.LinkPreviewsDisabled && featureOn(next, FeatureLinkPreviews) && !featureOn(s.settings, FeatureLinkPreviews) {
		return errInvalid("this server's operator has turned link previews off (link_previews_disabled)")
	}
	return nil
}

// joinLeaveChannel is where join and leave messages go: the configured
// channel, else the first text channel. Callers hold s.mu.
func (s *Server) joinLeaveChannel() *channelRec {
	if c := s.channels[s.settings.JoinLeaveChannel]; c != nil && c.Kind == wire.ChannelKind_CHANNEL_KIND_TEXT {
		return c
	}
	for _, c := range s.sortedChannels() {
		if c.Kind == wire.ChannelKind_CHANNEL_KIND_TEXT {
			return c
		}
	}
	return nil
}
