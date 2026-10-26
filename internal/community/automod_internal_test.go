package community

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/color/palette"
	"image/gif"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"
)

func TestKeywordMatching(t *testing.T) {
	cases := []struct {
		kw   string
		text string
		want bool
	}{
		{"cat", "the Cat sat", true},
		{"cat", "concatenate", false},
		{"cat", "cats", false},
		{"cat*", "cats", true},
		{"cat*", "concatenate", false},
		{"*cat", "bobcat!", true},
		{"*cat", "cats", false},
		{"*cat*", "concatenate", true},
		{"two words", "these two   WORDS here", true},
		{"two words", "two wordsmiths", false},
		{"two words*", "two wordsmiths", true},
		{"naïve", "so NAÏVE", true},
		{"кот", "мой кот", true},
	}
	for _, c := range cases {
		k, err := compileKeyword(c.kw)
		if err != nil {
			t.Fatal(err)
		}
		ws := words(c.text)
		norm := " " + strings.Join(ws, " ") + " "
		if got := keywordMatch(k, norm, ws); got != c.want {
			t.Errorf("%q in %q: %v, want %v", c.kw, c.text, got, c.want)
		}
	}
	for _, bad := range []string{"a*b", "***", "  ", "!!"} {
		if _, err := compileKeyword(bad); err == nil {
			t.Errorf("keyword %q accepted", bad)
		}
	}
}

func TestMentionsAndLinks(t *testing.T) {
	if n := mentionCount("@Alice @alice @bob, mail me at x@y.org @everyone @here."); n != 4 {
		t.Fatalf("mentions: %d", n)
	}
	got := linkHosts("go to https://Sub.Example.org/x, http://u:p@evil.test:8080/y www.Plain.net ftp://[::1]:21/ and not file.txt")
	want := []string{"sub.example.org", "evil.test", "www.plain.net", "[::1]"}
	if !slices.Equal(got, want) {
		t.Fatalf("hosts %q, want %q", got, want)
	}
	if !domainAllowed("docs.example.org", []string{"example.org"}) || domainAllowed("badexample.org", []string{"example.org"}) {
		t.Fatal("domain allowlist")
	}
}

// Patterns are refused at save time when they are too big, and the worst
// accepted rule set stays cheap on the longest message.
func TestPatternCost(t *testing.T) {
	if _, _, err := compilePattern(`(?:\p{L}{100}){10}`); err == nil || !strings.Contains(err.Error(), "too complex") {
		t.Fatalf("huge pattern: %v", err)
	}
	_, n, err := compilePattern(`fr[e3]{2}\s+mon[e3]y`)
	if err != nil || n > 100 {
		t.Fatalf("small pattern: %d %v", n, err)
	}
	// Fill the whole budget with the most expensive accepted patterns and
	// time a worst-case message.
	var set autoModSet
	total := 0
	for total < maxPatternInsts {
		re, n, err := compilePattern(`(?:[a-z]\d){1,60}[!?]`)
		if err != nil {
			t.Fatal(err)
		}
		if total+n > maxPatternInsts {
			break
		}
		total += n
		set.rules = append(set.rules, &compiledRule{rec: autoModRec{Enabled: true, Trigger: 1}})
		set.rules[len(set.rules)-1].patterns = append(set.rules[len(set.rules)-1].patterns, re)
	}
	text := strings.Repeat("a1", MaxMessageChars/2)
	if utf8.RuneCountInString(text) != MaxMessageChars {
		t.Fatal("bad text")
	}
	start := time.Now()
	for _, r := range set.rules {
		r.patterns[0].MatchString(text)
	}
	d := time.Since(start)
	t.Logf("%d patterns, %d instructions: worst-case pass %v", len(set.rules), total, d)
	if d > 2*time.Second {
		t.Fatalf("worst-case AutoMod pass took %v", d)
	}
}

// The helpers other server code calls (reactions, voice, typing):
// useCustomEmoji, customEmoji, timedOut and the permissions a timeout
// removes.
func TestModerationHelpers(t *testing.T) {
	now := time.Now()
	s := &Server{
		settings:   DefaultSettings(),
		emoji:      map[int64]*emojiRec{7: {ID: 7, Name: "party"}},
		roles:      map[int64]*roleRec{1: {ID: 1, Perms: DefaultEveryonePerms, Builtin: true}},
		everyoneID: 1,
		now:        func() time.Time { return now },
	}
	m := &memberRec{Key: []byte("m"), Status: 2}
	c := &channelRec{ID: 1}
	if e, err := s.useCustomEmoji(m, c, ":party:7"); err != nil || e.ID != 7 {
		t.Fatalf("use: %v", err)
	}
	for ref, want := range map[string]string{":party:8": "no such", ":other:7": "no such", "party": ":name:id"} {
		if _, err := s.useCustomEmoji(m, c, ref); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%q: %v", ref, err)
		}
	}
	m.TimeoutUntil = now.Add(time.Minute).UnixMilli()
	if !s.timedOut(m) {
		t.Fatal("not timed out")
	}
	if _, err := s.useCustomEmoji(m, c, ":party:7"); err == nil {
		t.Fatal("a timed-out member used a custom emoji")
	}
	if p := s.channelPerms(m, c); p&PermTimeoutMask != 0 || p&PermViewChannel == 0 {
		t.Fatalf("timed-out channel perms %b", p)
	}
	m.TimeoutUntil = now.UnixMilli()
	if s.timedOut(m) {
		t.Fatal("still timed out at the end time")
	}
	s.settings.CustomEmojiEnabled = proto.Bool(false)
	if _, err := s.customEmoji(7); err == nil || !strings.Contains(err.Error(), "turned off") {
		t.Fatalf("toggle off: %v", err)
	}
}

func TestEmojiImageHeaders(t *testing.T) {
	// GIF, animated.
	pal := image.NewPaletted(image.Rect(0, 0, 32, 20), palette.Plan9)
	pal.Set(1, 1, color.White)
	var g bytes.Buffer
	if err := gif.EncodeAll(&g, &gif.GIF{Image: []*image.Paletted{pal, pal}, Delay: []int{10, 10}, LoopCount: 0}); err != nil {
		t.Fatal(err)
	}
	mime, w, h, anim, err := sniffEmoji(g.Bytes())
	if err != nil || mime != "image/gif" || w != 32 || h != 20 || !anim {
		t.Fatalf("gif: %s %dx%d %v %v", mime, w, h, anim, err)
	}
	// WebP: VP8X (animated), VP8L and VP8 headers.
	webp := func(chunk string, payload []byte) []byte {
		b := append([]byte("RIFF\x00\x00\x00\x00WEBP"+chunk), make([]byte, 4)...)
		binary.LittleEndian.PutUint32(b[16:20], uint32(len(payload)))
		return append(b, payload...)
	}
	vp8x := []byte{0x02, 0, 0, 0, 99, 0, 0, 63, 0, 0}
	if mime, w, h, anim, err := sniffEmoji(webp("VP8X", vp8x)); err != nil || mime != "image/webp" || w != 100 || h != 64 || !anim {
		t.Fatalf("vp8x: %dx%d %v %v", w, h, anim, err)
	}
	// The 24-bit fields are value + 1: 0x0001ff + 1 = 512 wide (the
	// carry out of the low byte must not be OR-ed away), 0x01007f + 1 tall.
	crafted := []byte{0, 0, 0, 0, 0xff, 0x01, 0x00, 0x7f, 0x00, 0x01}
	if _, w, h, _, err := sniffEmoji(webp("VP8X", crafted)); err != nil || w != 512 || h != 0x010080 {
		t.Fatalf("vp8x 24-bit sizes: %dx%d %v", w, h, err)
	}
	if _, _, err := ValidateEmojiImage(webp("VP8X", crafted)); err == nil {
		t.Fatal("512 px wide webp accepted")
	}
	v := uint32(127) | uint32(200)<<14
	vp8l := []byte{0x2f, byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24), 0, 0, 0, 0, 0}
	if _, w, h, _, err := sniffEmoji(webp("VP8L", vp8l)); err != nil || w != 128 || h != 201 {
		t.Fatalf("vp8l: %dx%d %v", w, h, err)
	}
	vp8 := []byte{0, 0, 0, 0x9d, 0x01, 0x2a, 16, 0, 16, 0}
	if _, w, h, _, err := sniffEmoji(webp("VP8 ", vp8)); err != nil || w != 16 || h != 16 {
		t.Fatalf("vp8: %dx%d %v", w, h, err)
	}
	if _, _, err := ValidateEmojiImage(webp("VP8L", vp8l)); err == nil || !strings.Contains(err.Error(), "201") {
		t.Fatalf("201 px tall webp: %v", err)
	}
	for _, bad := range [][]byte{webp("JUNK", vp8), webp("VP8 ", []byte{1, 2, 3}), []byte("RIFF....WEBPVP8X")} {
		if _, _, err := ValidateEmojiImage(bad); err == nil {
			t.Fatalf("bad webp %q accepted", bad)
		}
	}
	for ref, want := range map[string]bool{":party:12": true, ":Party:12": false, ":p:1": false, ":party:": false, "x:party:12": false} {
		if _, _, ok := ParseEmojiRef(ref); ok != want {
			t.Errorf("ParseEmojiRef(%q) = %v", ref, ok)
		}
	}
}
