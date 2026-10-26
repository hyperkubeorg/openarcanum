package community

import (
	"strings"
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// FuzzEmojiImage feeds arbitrary bytes to the emoji image validator (an
// uploader controls them): no panic, and anything accepted is within the
// limits and of an allowed type.
func FuzzEmojiImage(f *testing.F) {
	f.Add([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x10\x00\x00\x00\x10\x08\x06\x00\x00\x00"))
	f.Add([]byte("GIF89a\x10\x00\x10\x00\x00\x00\x00"))
	f.Add([]byte("RIFF\x00\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00\x02\x00\x00\x00\x0f\x00\x00\x0f\x00\x00"))
	f.Add([]byte("RIFF\x00\x00\x00\x00WEBPVP8L\x05\x00\x00\x00\x2f\x0f\xc0\x03\x00"))
	f.Add([]byte("RIFF\x00\x00\x00\x00WEBPVP8 \x0a\x00\x00\x00\x00\x00\x00\x9d\x01\x2a\x10\x00\x10\x00"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		mime, _, err := ValidateEmojiImage(b)
		if err != nil {
			return
		}
		if len(b) > MaxEmojiBytes || (mime != "image/png" && mime != "image/gif" && mime != "image/webp") {
			t.Fatalf("accepted %d bytes as %q", len(b), mime)
		}
		_, w, h, _, _ := sniffEmoji(b)
		if w < 1 || h < 1 || w > MaxEmojiPixels || h > MaxEmojiPixels {
			t.Fatalf("accepted %dx%d", w, h)
		}
	})
}

// FuzzAutoModRule compiles arbitrary keywords and patterns as an admin
// would save them and runs them over arbitrary text: validation either
// refuses a rule or yields one whose matching never panics and stays
// within the size limits.
func FuzzAutoModRule(f *testing.F) {
	f.Add("bad*", `fr[e3]e\s+money`, "free money from a bad*actor @a @b https://x.example/y")
	f.Add("*ing two words", `(?:a|b){3,9}`, "jumping two words www.example.org")
	f.Add("", `\p{Greek}+`, "αβγ @everyone")
	s := &Server{settings: DefaultSettings(), roles: map[int64]*roleRec{}, channels: map[int64]*channelRec{}}
	f.Fuzz(func(t *testing.T, kw, pattern, text string) {
		for _, trig := range []wire.AutoModTrigger{wire.AutoModTrigger_AUTOMOD_TRIGGER_KEYWORD, wire.AutoModTrigger_AUTOMOD_TRIGGER_MENTION_SPAM, wire.AutoModTrigger_AUTOMOD_TRIGGER_LINKS} {
			cr, err := s.normalizeRule(&wire.AutoModRule{
				Name: "fuzz", Enabled: true, Trigger: trig, Keywords: strings.Split(kw, ","), Patterns: []string{pattern},
				MentionLimit: 2, AllowedDomains: strings.Fields(kw), Block: true,
			})
			if err != nil {
				continue
			}
			if cr.insts > maxPatternInstsOne*maxPatterns {
				t.Fatalf("rule over the size limit: %d", cr.insts)
			}
			ws := words(text)
			cr.match(text, " "+strings.Join(ws, " ")+" ", ws)
		}
		mentionCount(text)
		linkHosts(text)
	})
}
