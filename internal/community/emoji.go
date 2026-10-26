package community

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"image/gif"
	"image/png"
	"regexp"
	"sort"
	"strconv"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Custom emoji (docs/protocol.md §4.10).
// Members with Manage Emoji upload PNG, GIF or WebP images (animated
// allowed) up to MaxEmojiBytes and MaxEmojiPixels square; the community's
// max_custom_emoji setting caps the count. Images reach clients over the
// session (GetEmoji), never as URLs. Messages and reactions refer to an
// emoji as ":name:id".

const (
	MaxEmojiBytes  = 256 << 10
	MaxEmojiPixels = 128
)

var emojiNameRE = regexp.MustCompile(`^[a-z0-9_]{2,32}$`)

// emojiRefRE finds ":name:id" references in text.
var emojiRefRE = regexp.MustCompile(`:([a-z0-9_]{2,32}):([0-9]{1,18})`)

// emojiRec is an emoji's sealed record (emoji.data); the image is sealed
// separately in emoji.image.
type emojiRec struct {
	ID        int64  `json:"-"`
	Name      string `json:"name"`
	Mime      string `json:"mime"`
	Animated  bool   `json:"animated"`
	Size      uint32 `json:"size"`
	Version   uint32 `json:"version"`
	SHA256    []byte `json:"sha256"`
	CreatedBy []byte `json:"created_by"`
	CreatedAt int64  `json:"created_at"`
}

func (e *emojiRec) proto() *wire.CustomEmoji {
	return &wire.CustomEmoji{Id: e.ID, Name: e.Name, Animated: e.Animated, Mime: e.Mime, Size: e.Size, Version: e.Version}
}

var errEmojiOff = errDisabled("custom emoji are turned off in this community")

// customEmoji looks up an emoji by id for anything that uses one (message
// text, reactions). It fails with a "turned off" DISABLED error when the
// community toggle is off and NOT_FOUND for an unknown id. Callers hold
// s.mu.
func (s *Server) customEmoji(id int64) (*emojiRec, error) {
	if !featureOn(s.settings, FeatureCustomEmoji) {
		return nil, errEmojiOff
	}
	e := s.emoji[id]
	if e == nil {
		return nil, errNotFound("no such custom emoji")
	}
	return e, nil
}

// ParseEmojiRef splits a ":name:id" reference (a reaction's emoji field).
func ParseEmojiRef(ref string) (name string, id int64, ok bool) {
	m := emojiRefRE.FindStringSubmatch(ref)
	if m == nil || m[0] != ref {
		return "", 0, false
	}
	id, err := strconv.ParseInt(m[2], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return m[1], id, true
}

// useCustomEmoji checks that member m may use emoji ref (":name:id") in
// channel c: the toggle is on, the emoji exists under that name and m has
// Use Custom Emoji there. Reactions call this. Callers hold s.mu.
func (s *Server) useCustomEmoji(m *memberRec, c *channelRec, ref string) (*emojiRec, error) {
	name, id, ok := ParseEmojiRef(ref)
	if !ok {
		return nil, errInvalid("custom emoji are written :name:id")
	}
	e, err := s.customEmoji(id)
	if err != nil {
		return nil, err
	}
	if e.Name != name {
		return nil, errNotFound("no such custom emoji")
	}
	if s.channelPerms(m, c)&PermUseCustomEmoji == 0 {
		return nil, errDenied("you can't use custom emoji in this channel")
	}
	return e, nil
}

// checkEmojiRefs enforces the toggle and Use Custom Emoji on the custom
// emoji a message's text uses. Text that only looks like a reference (no
// emoji with that id and name) is left alone. Callers hold s.mu.
func (s *Server) checkEmojiRefs(m *memberRec, c *channelRec, text string) error {
	for _, ref := range emojiRefRE.FindAllStringSubmatch(text, -1) {
		id, err := strconv.ParseInt(ref[2], 10, 64)
		if err != nil {
			continue
		}
		e := s.emoji[id]
		if e == nil || e.Name != ref[1] {
			continue
		}
		if !featureOn(s.settings, FeatureCustomEmoji) {
			return errEmojiOff
		}
		if s.channelPerms(m, c)&PermUseCustomEmoji == 0 {
			return errDenied("you can't use custom emoji in this channel")
		}
	}
	return nil
}

// sniffEmoji validates an emoji image from its header: format, size in
// pixels and whether it's animated.
func sniffEmoji(b []byte) (mime string, w, h int, animated bool, err error) {
	switch {
	case len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n":
		cfg, e := png.DecodeConfig(bytes.NewReader(b))
		if e != nil {
			return "", 0, 0, false, fmt.Errorf("that PNG is damaged")
		}
		// APNG: an acTL chunk before the image data.
		idat := bytes.Index(b, []byte("IDAT"))
		actl := bytes.Index(b, []byte("acTL"))
		return "image/png", cfg.Width, cfg.Height, actl > 0 && (idat < 0 || actl < idat), nil
	case len(b) >= 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a"):
		cfg, e := gif.DecodeConfig(bytes.NewReader(b))
		if e != nil {
			return "", 0, 0, false, fmt.Errorf("that GIF is damaged")
		}
		return "image/gif", cfg.Width, cfg.Height, bytes.Contains(b, []byte("NETSCAPE2.0")), nil
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return sniffWebP(b)
	}
	return "", 0, 0, false, fmt.Errorf("emoji must be PNG, GIF or WebP images")
}

// sniffWebP reads the canvas size from a WebP's first chunk (VP8X, VP8L or
// VP8; see the WebP container spec).
func sniffWebP(b []byte) (string, int, int, bool, error) {
	bad := fmt.Errorf("that WebP is damaged")
	if len(b) < 30 {
		return "", 0, 0, false, bad
	}
	chunk := string(b[12:16])
	p := b[20:]
	switch chunk {
	case "VP8X":
		if len(p) < 10 {
			return "", 0, 0, false, bad
		}
		w := 1 + (int(p[4]) | int(p[5])<<8 | int(p[6])<<16)
		h := 1 + (int(p[7]) | int(p[8])<<8 | int(p[9])<<16)
		return "image/webp", w, h, p[0]&0x02 != 0, nil
	case "VP8L":
		if len(p) < 5 || p[0] != 0x2f {
			return "", 0, 0, false, bad
		}
		v := binary.LittleEndian.Uint32(p[1:5])
		return "image/webp", int(v&0x3fff) + 1, int((v>>14)&0x3fff) + 1, false, nil
	case "VP8 ":
		if len(p) < 10 || p[3] != 0x9d || p[4] != 0x01 || p[5] != 0x2a {
			return "", 0, 0, false, bad
		}
		w := int(binary.LittleEndian.Uint16(p[6:8]) & 0x3fff)
		h := int(binary.LittleEndian.Uint16(p[8:10]) & 0x3fff)
		return "image/webp", w, h, false, nil
	}
	return "", 0, 0, false, bad
}

// ValidateEmojiImage applies the upload limits to an image.
func ValidateEmojiImage(b []byte) (mime string, animated bool, err error) {
	if len(b) == 0 {
		return "", false, errInvalid("no image")
	}
	if len(b) > MaxEmojiBytes {
		return "", false, errTooLarge(fmt.Sprintf("emoji images may be at most %d KB", MaxEmojiBytes>>10))
	}
	mime, w, h, animated, e := sniffEmoji(b)
	if e != nil {
		return "", false, errInvalid(e.Error())
	}
	if w < 1 || h < 1 || w > MaxEmojiPixels || h > MaxEmojiPixels {
		return "", false, errInvalid(fmt.Sprintf("emoji images may be at most %d×%d pixels (this one is %d×%d)", MaxEmojiPixels, MaxEmojiPixels, w, h))
	}
	return mime, animated, nil
}

func (s *Server) loadEmoji(ctx context.Context) error {
	s.emoji = map[int64]*emojiRec{}
	return s.loadRows(ctx, `SELECT id, data FROM emoji`, func(id int64, data []byte) error {
		e := &emojiRec{}
		if err := s.openJSON("emoji", "data", rowID(id), data, e); err != nil {
			return err
		}
		e.ID = id
		s.emoji[id] = e
		return nil
	})
}

// emojiList is the SyncState list: empty while the toggle is off. Callers
// hold s.mu.
func (s *Server) emojiList() []*wire.CustomEmoji {
	if !featureOn(s.settings, FeatureCustomEmoji) {
		return nil
	}
	out := make([]*wire.CustomEmoji, 0, len(s.emoji))
	for _, e := range s.emoji {
		out = append(out, e.proto())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *Server) emojiNameFree(name string, except int64) error {
	if !emojiNameRE.MatchString(name) {
		return errInvalid("emoji names are 2 to 32 lowercase letters, digits or underscores")
	}
	for id, e := range s.emoji {
		if e.Name == name && id != except {
			return errConflict(fmt.Sprintf("there is already an emoji called :%s:", name))
		}
	}
	return nil
}

// emojiUpload is an AddEmoji admin action with its image next to it.
func (s *Server) emojiUpload(ctx context.Context, ss *sess, u *wire.EmojiUpload) (*wire.ServerMessage, error) {
	if u.Action == nil {
		return nil, errInvalid("no signed action")
	}
	var a wire.AdminAction
	if err := proto.Unmarshal(u.Action.Action, &a); err != nil {
		return nil, errInvalid("malformed action")
	}
	add := a.GetAddEmoji()
	if add == nil {
		return nil, errInvalid("an emoji upload carries an add_emoji action")
	}
	sum := sha256.Sum256(u.Image)
	if !bytes.Equal(sum[:], add.Sha256) || int(add.Size) != len(u.Image) {
		return nil, errInvalid("the image doesn't match the signed action")
	}
	return s.adminWith(ctx, ss, u.Action, u.Image)
}

func (s *Server) opAddEmoji(ctx context.Context, actor *memberRec, a *wire.AddEmoji, out *adminOutcome) error {
	if err := s.need(actor, PermManageEmoji); err != nil {
		return err
	}
	if !featureOn(s.settings, FeatureCustomEmoji) {
		return errEmojiOff
	}
	if out.attach == nil {
		return errInvalid("emoji images are uploaded with an emoji upload request")
	}
	if err := s.emojiNameFree(a.Name, 0); err != nil {
		return err
	}
	if uint32(len(s.emoji)) >= s.settings.MaxCustomEmoji {
		return errConflict(fmt.Sprintf("this community already has %d custom emoji, its limit", len(s.emoji)))
	}
	mime, animated, err := ValidateEmojiImage(out.attach)
	if err != nil {
		return err
	}
	id, err := s.nextID(ctx, s.db, "emoji")
	if err != nil {
		return err
	}
	e := &emojiRec{
		ID: id, Name: a.Name, Mime: mime, Animated: animated, Size: uint32(len(out.attach)), Version: 1,
		SHA256: a.Sha256, CreatedBy: actor.Key, CreatedAt: s.now().UnixMilli(),
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO emoji (id, data, image) VALUES (?, ?, ?)`, id,
		s.sealJSON("emoji", "data", rowID(id), e), s.seal("emoji", "image", rowID(id), out.attach)); err != nil {
		return err
	}
	s.emoji[id] = e
	out.result.Id = id
	out.summary = fmt.Sprintf("added custom emoji :%s:", a.Name)
	out.broadcast = true
	return nil
}

func (s *Server) opRenameEmoji(ctx context.Context, actor *memberRec, r *wire.RenameEmoji, out *adminOutcome) error {
	if err := s.need(actor, PermManageEmoji); err != nil {
		return err
	}
	if !featureOn(s.settings, FeatureCustomEmoji) {
		return errEmojiOff
	}
	e := s.emoji[r.Id]
	if e == nil {
		return errNotFound("no such custom emoji")
	}
	if err := s.emojiNameFree(r.Name, r.Id); err != nil {
		return err
	}
	next := *e
	next.Name = r.Name
	if _, err := s.db.ExecContext(ctx, `UPDATE emoji SET data = ? WHERE id = ?`, s.sealJSON("emoji", "data", rowID(e.ID), &next), e.ID); err != nil {
		return err
	}
	s.emoji[e.ID] = &next
	out.summary = fmt.Sprintf("renamed custom emoji :%s: to :%s:", e.Name, next.Name)
	out.broadcast = true
	return nil
}

// opDeleteEmoji works with the toggle off too, so admins can clean up.
func (s *Server) opDeleteEmoji(ctx context.Context, actor *memberRec, d *wire.DeleteEmoji, out *adminOutcome) error {
	if err := s.need(actor, PermManageEmoji); err != nil {
		return err
	}
	e := s.emoji[d.Id]
	if e == nil {
		return errNotFound("no such custom emoji")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM emoji WHERE id = ?`, e.ID); err != nil {
		return err
	}
	delete(s.emoji, e.ID)
	out.summary = fmt.Sprintf("deleted custom emoji :%s:", e.Name)
	out.broadcast = true
	return nil
}

func (s *Server) getEmoji(ctx context.Context, g *wire.GetEmoji) (*wire.ServerMessage, error) {
	s.mu.RLock()
	e, err := s.customEmoji(g.Id)
	var rec emojiRec
	if e != nil {
		rec = *e
	}
	s.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	var enc []byte
	if err := s.db.QueryRowContext(ctx, `SELECT image FROM emoji WHERE id = ?`, g.Id).Scan(&enc); err != nil {
		return nil, errNotFound("no such custom emoji")
	}
	img, err := s.open("emoji", "image", rowID(g.Id), enc)
	if err != nil {
		return nil, err
	}
	return &wire.ServerMessage{Body: &wire.ServerMessage_EmojiImage{EmojiImage: &wire.EmojiImage{Id: g.Id, Image: img, Mime: rec.Mime, Version: rec.Version}}}, nil
}
