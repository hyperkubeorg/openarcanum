package community

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/relayurl"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Limits on community settings (docs/settings.md).
const (
	MaxFileSizeLimit = 25 << 20
	// New communities start at 10 MB; an admin can raise it up to the limit.
	DefaultMaxFileSize = 10 << 20
	MaxIconBytes       = 256 << 10
	MaxAvatarBytes     = 256 << 10
	MaxRetentionDays   = 36500
	MaxTags            = 10
	MaxMessageChars    = 4000

	// Member screening is at most 7 days.
	MaxScreeningMinutes = 7 * 24 * 60
	// Custom emoji count limit range and default.
	DefaultMaxCustomEmoji = 50
	MaxCustomEmojiLimit   = 250
)

// DefaultSettings are the documented defaults: private, invite-only, 30-day
// message and 7-day file retention, search on, 10 MB files, 5 devices,
// and the feature toggle defaults (features.go).
func DefaultSettings() *wire.CommunitySettings {
	s := &wire.CommunitySettings{
		Name:                 "New community",
		Visibility:           wire.Visibility_VISIBILITY_PRIVATE,
		JoinPolicy:           wire.JoinPolicy_JOIN_POLICY_INVITE_ONLY,
		AuditVisibility:      wire.AuditVisibility_AUDIT_VISIBILITY_ADMINS,
		MessageRetentionDays: 30,
		FileRetentionDays:    7,
		SearchEnabled:        true,
		MaxFileSize:          DefaultMaxFileSize,
		RelayMode:            wire.RelayMode_RELAY_MODE_AUTOMATIC,
		MaxDevicesPerMember:  5,
		AllowContactCards:    true,
		MaxCustomEmoji:       DefaultMaxCustomEmoji,
	}
	fillFeatureDefaults(s)
	return s
}

var tagRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,29}$`)

// NormalizeSettings validates s in place (trimming, lowercasing tags).
func NormalizeSettings(s *wire.CommunitySettings) error {
	s.Name = strings.TrimSpace(s.Name)
	s.Description = strings.TrimSpace(s.Description)
	s.Language = strings.TrimSpace(s.Language)
	switch n := utf8.RuneCountInString(s.Name); {
	case n == 0:
		return errors.New("name is required")
	case n > 100:
		return errors.New("name may be at most 100 characters")
	}
	if utf8.RuneCountInString(s.Description) > 1000 {
		return errors.New("description may be at most 1000 characters")
	}
	if len(s.Language) > 16 {
		return errors.New("language must be a short language tag")
	}
	if len(s.Icon) > MaxIconBytes {
		return fmt.Errorf("icon may be at most %d KB", MaxIconBytes>>10)
	}
	if len(s.Icon) > 0 && !IsImage(s.Icon) {
		return errors.New("icon must be a PNG, JPEG, GIF or WebP image of at most 16 megapixels")
	}
	seen := map[string]bool{}
	var tags []string
	for _, t := range s.Tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] {
			continue
		}
		if !tagRE.MatchString(t) {
			return fmt.Errorf("tag %q: use letters, digits and dashes, up to 30 characters", t)
		}
		seen[t] = true
		tags = append(tags, t)
	}
	if len(tags) > MaxTags {
		return fmt.Errorf("at most %d tags", MaxTags)
	}
	s.Tags = tags
	if _, ok := wire.Visibility_name[int32(s.Visibility)]; !ok || s.Visibility == wire.Visibility_VISIBILITY_UNSPECIFIED {
		return errors.New("visibility must be public or private")
	}
	if _, ok := wire.JoinPolicy_name[int32(s.JoinPolicy)]; !ok || s.JoinPolicy == wire.JoinPolicy_JOIN_POLICY_UNSPECIFIED {
		return errors.New("join policy must be open, request or invite-only")
	}
	if _, ok := wire.AuditVisibility_name[int32(s.AuditVisibility)]; !ok || s.AuditVisibility == wire.AuditVisibility_AUDIT_VISIBILITY_UNSPECIFIED {
		return errors.New("audit log visibility must be admins or everyone")
	}
	if s.MessageRetentionDays > MaxRetentionDays || s.FileRetentionDays > MaxRetentionDays {
		return fmt.Errorf("retention may be at most %d days (0 keeps forever)", MaxRetentionDays)
	}
	if s.MaxFileSize < 1024 || s.MaxFileSize > MaxFileSizeLimit {
		return errors.New("max file size must be between 1 KB and 25 MB")
	}
	if s.FileBandwidthLimit != 0 && s.FileBandwidthLimit < 16<<10 {
		return errors.New("file bandwidth limit must be 0 (none) or at least 16 KB/s")
	}
	switch s.RelayMode {
	case wire.RelayMode_RELAY_MODE_AUTOMATIC:
	case wire.RelayMode_RELAY_MODE_PINNED:
		if len(s.PinnedRelays) == 0 {
			return errors.New("a pinned relay list needs at least one relay")
		}
	default:
		return errors.New("relays must be automatic or pinned")
	}
	if len(s.PinnedRelays) > 8 {
		return errors.New("at most 8 pinned relays")
	}
	for i, r := range s.PinnedRelays {
		c, err := relayurl.Canonical(r)
		if err != nil {
			return fmt.Errorf("relay %q isn't a ws:// or wss:// relay URL: %w", r, err)
		}
		s.PinnedRelays[i] = c
	}
	if s.MaxDevicesPerMember < 1 || s.MaxDevicesPerMember > 50 {
		return errors.New("maximum devices per member must be between 1 and 50")
	}
	if s.MaxMembers > 1_000_000 {
		return errors.New("maximum members is too large")
	}
	if s.ScreeningMinutes > MaxScreeningMinutes {
		return errors.New("member screening may be at most 7 days (0 turns it off)")
	}
	if s.NewMemberMinutes > MaxScreeningMinutes {
		return errors.New("new-member restrictions may last at most 7 days (0 turns them off)")
	}
	if s.MaxCustomEmoji < 1 || s.MaxCustomEmoji > MaxCustomEmojiLimit {
		return fmt.Errorf("the custom emoji limit must be between 1 and %d", MaxCustomEmojiLimit)
	}
	if _, ok := wire.AttachmentKinds_name[int32(s.AttachmentKinds)]; !ok {
		return errors.New("allowed attachments must be all kinds or images only")
	}
	if s.JoinLeaveChannel < 0 {
		return errors.New("no such channel for join and leave messages")
	}
	fillFeatureDefaults(s)
	return nil
}

// IsImage sniffs the formats accepted for icons and avatars.
func IsImage(b []byte) bool {
	return isImageMagic(b) && ImageSizeOK(b)
}

// Images the server stores and pushes to every member's client to decode
// (avatars, the community icon, link preview images) are limited in pixels
// as well as bytes, read from the header without decoding: a few kilobytes
// of PNG can declare a 50000 × 50000 canvas that takes gigabytes to decode.
const (
	MaxImageSide   = 8192
	MaxImagePixels = 4096 * 4096
)

// ImageSizeOK reports whether b's header names a canvas within MaxImageSide
// and MaxImagePixels; an unreadable header fails.
func ImageSizeOK(b []byte) bool {
	w, h, ok := imageDims(b)
	return ok && w > 0 && h > 0 && w <= MaxImageSide && h <= MaxImageSide && w*h <= MaxImagePixels
}

// imageDims reads a PNG, JPEG, GIF or WebP canvas size from its header.
func imageDims(b []byte) (w, h int, ok bool) {
	r := bytes.NewReader(b)
	var cfg image.Config
	var err error
	switch {
	case len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n":
		cfg, err = png.DecodeConfig(r)
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		cfg, err = jpeg.DecodeConfig(r)
	case len(b) >= 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a"):
		cfg, err = gif.DecodeConfig(r)
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		_, w, h, _, err := sniffWebP(b)
		return w, h, err == nil
	default:
		return 0, 0, false
	}
	return cfg.Width, cfg.Height, err == nil
}

// isImageMagic reports whether b starts like a PNG, JPEG, GIF or WebP.
func isImageMagic(b []byte) bool {
	switch {
	case len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n":
		return true
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return true
	case len(b) >= 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a"):
		return true
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return true
	}
	return false
}

// diffSettings describes what changed, for the audit log.
func diffSettings(old, new *wire.CommunitySettings) string {
	var parts []string
	add := func(name string, changed bool) {
		if changed {
			parts = append(parts, name)
		}
	}
	add("name", old.Name != new.Name)
	add("description", old.Description != new.Description)
	add("icon", string(old.Icon) != string(new.Icon))
	add("language", old.Language != new.Language)
	add("tags", strings.Join(old.Tags, ",") != strings.Join(new.Tags, ","))
	add("visibility", old.Visibility != new.Visibility)
	add("join policy", old.JoinPolicy != new.JoinPolicy)
	add("audit visibility", old.AuditVisibility != new.AuditVisibility)
	add("message retention", old.MessageRetentionDays != new.MessageRetentionDays)
	add("file retention", old.FileRetentionDays != new.FileRetentionDays)
	add("search", old.SearchEnabled != new.SearchEnabled)
	add("max file size", old.MaxFileSize != new.MaxFileSize)
	add("file bandwidth", old.FileBandwidthLimit != new.FileBandwidthLimit)
	add("relays", old.RelayMode != new.RelayMode || strings.Join(old.PinnedRelays, ",") != strings.Join(new.PinnedRelays, ","))
	add("max devices", old.MaxDevicesPerMember != new.MaxDevicesPerMember)
	add("contact cards", old.AllowContactCards != new.AllowContactCards)
	add("max members", old.MaxMembers != new.MaxMembers)
	// Search and contact cards are toggles too; they are named with the
	// other toggles below.
	parts = slices.DeleteFunc(parts, func(p string) bool { return p == "search" || p == "contact cards" })
	parts = append(parts, diffFeatures(old, new)...)
	add("member screening", old.ScreeningMinutes != new.ScreeningMinutes)
	add("new-member restrictions", old.NewMemberMinutes != new.NewMemberMinutes)
	if old.Lockdown != new.Lockdown {
		parts = append(parts, "raid mode "+onOff(new.Lockdown))
	}
	add("custom emoji limit", old.MaxCustomEmoji != new.MaxCustomEmoji)
	if len(parts) == 0 {
		return "updated settings (no changes)"
	}
	return "changed " + strings.Join(parts, ", ")
}

func cloneSettings(s *wire.CommunitySettings) *wire.CommunitySettings {
	return proto.Clone(s).(*wire.CommunitySettings)
}
