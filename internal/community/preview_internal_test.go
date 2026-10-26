package community

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/hyperkubeorg/openarcanum/internal/config"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

func TestPublicAddr(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.8.9.10", "::1", "10.1.2.3", "172.16.0.1", "172.31.255.255", "192.168.1.1",
		"169.254.169.254", "fe80::1", "fc00::1", "fd12:3456::1", "0.0.0.0", "::", "0.1.2.3",
		"100.64.0.1", "100.127.255.254", "192.0.0.8", "192.0.2.1", "198.18.0.1", "198.51.100.7", "203.0.113.9",
		"224.0.0.1", "239.255.255.250", "255.255.255.255", "240.0.0.1", "ff02::1", "ff01::1",
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", "::ffff:169.254.169.254", "64:ff9b::a00:1", "2002:a00:1::1",
		"2001:db8::1", "2001:0:4136:e378::1", "fec0::1", "100::1",
	}
	for _, a := range blocked {
		if publicAddr(netip.MustParseAddr(a), false) {
			t.Errorf("%s counts as public", a)
		}
	}
	for _, a := range []string{"1.1.1.1", "93.184.215.14", "8.8.8.8", "2606:4700::1111", "::ffff:1.1.1.1", "100.128.0.1"} {
		if !publicAddr(netip.MustParseAddr(a), false) {
			t.Errorf("%s counts as non-public", a)
		}
	}
	// The test allowance covers loopback only.
	if !publicAddr(netip.MustParseAddr("127.0.0.1"), true) || publicAddr(netip.MustParseAddr("10.0.0.1"), true) {
		t.Error("loopback allowance")
	}
}

func testFetcher(t *testing.T, allowLoopback bool) *previewFetcher {
	cfg := DefaultConfig(t.TempDir())
	cfg.PreviewTimeout = config.Duration{Duration: 2 * time.Second}
	cfg.PreviewSchemes = []string{"http", "https"}
	cfg.PreviewAllowLoopback = allowLoopback
	f := newPreviewFetcher(cfg)
	t.Cleanup(f.wait)
	return f
}

// Every address of a host is checked before connecting: names that resolve
// to loopback and literal private addresses are refused without a connection.
func TestFetcherRefusesNonPublicTargets(t *testing.T) {
	f := testFetcher(t, false)
	ctx := context.Background()
	for _, addr := range []string{"127.0.0.1:80", "localhost:80", "[::1]:443", "10.0.0.1:443", "169.254.169.254:80", "[fe80::1]:80", "0.0.0.0:80"} {
		if _, err := f.dial(ctx, "tcp", addr); !errors.Is(err, errPreviewBlocked) {
			t.Errorf("dial %s: %v", addr, err)
		}
	}
	// The socket-level check refuses too.
	if err := f.control("tcp", "192.168.0.10:443", nil); !errors.Is(err, errPreviewBlocked) {
		t.Errorf("control: %v", err)
	}
	if _, err := f.fetch(ctx, "http://127.0.0.1:9/page"); !errors.Is(err, errPreviewBlocked) {
		t.Errorf("fetch of loopback: %v", err)
	}
}

func TestFetcherURLChecks(t *testing.T) {
	cfg := DefaultConfig(t.TempDir())
	f := newPreviewFetcher(cfg)
	defer f.wait()
	for _, raw := range []string{"http://example.invalid/", "ftp://example.invalid/", "https://user:pw@example.invalid/", "file:///etc/passwd", "javascript:alert(1)"} {
		if _, err := f.fetch(context.Background(), raw); !errors.Is(err, errPreviewScheme) {
			t.Errorf("%s: %v", raw, err)
		}
	}
}

func TestParseOG(t *testing.T) {
	page := `<!DOCTYPE html><HTML><HEAD>
<!-- <meta property="og:title" content="in a comment"> -->
<script>var s = '<meta property="og:title" content="in a script">';</script>
<TITLE>Plain title</TITLE>
<meta name="description" content='meta description'>
<META PROPERTY="og:title" CONTENT="OG &quot;title&quot;" />
<meta content="described" property="og:description">
<meta property=og:site_name content=Site>
<meta property="og:image" content="https://img.example.invalid/a.png?x=1&amp;y=2">
<meta property="og:title" content="second title ignored">
</head><body><meta property="og:description" content="in the body"></body>`
	d := parseOG([]byte(page))
	if d.title != `OG &quot;title&quot;` || d.description != "described" || d.site != "Site" ||
		d.image != "https://img.example.invalid/a.png?x=1&y=2" || d.htmlTitle != "Plain title" || d.metaDescription != "meta description" {
		t.Fatalf("parsed: %+v", d)
	}
	if got := cleanText(d.title, 200); got != `OG "title"` {
		t.Fatalf("clean: %q", got)
	}
	if got := cleanText("a\x00b\n\n  c", 200); got != "a b c" {
		t.Fatalf("clean controls: %q", got)
	}
	if got := cleanText(strings.Repeat("é", 300), 200); utf8.RuneCountInString(got) != 200 {
		t.Fatalf("truncate: %d", utf8.RuneCountInString(got))
	}
	for _, broken := range []string{"<meta", "<meta property=\"og:title", "<title>never closed", "<!-- open", "<script>", "<meta content='x"} {
		parseOG([]byte(broken))
	}
}

func TestFirstLink(t *testing.T) {
	cases := map[string]string{
		"see https://a.example.invalid/x.":                     "https://a.example.invalid/x",
		"(https://a.example.invalid/y) and more":               "https://a.example.invalid/y",
		"<https://a.example.invalid/> then http://b.invalid/z": "http://b.invalid/z",
		"`https://code.invalid/` **https://bold.invalid/**":    "https://bold.invalid/",
		"||https://spoiler.invalid/|| nothing else":            "",
		"```\nhttps://block.invalid/\n```":                     "",
		"no links here":                                        "",
		"https://":                                             "",
		"[label](https://md.invalid/page)":                     "https://md.invalid/page",
	}
	for in, want := range cases {
		if got := firstLink(in); got != want {
			t.Errorf("firstLink(%q) = %q, want %q", in, got, want)
		}
	}
}

func FuzzParseOG(f *testing.F) {
	f.Add([]byte(`<head><meta property="og:title" content="x"><title>t</title></head>`))
	f.Add([]byte(`<meta property=og:image content=/a.png><!--`))
	f.Add([]byte(`<script><meta property="og:title" content="x"></script><meta`))
	f.Fuzz(func(t *testing.T, b []byte) {
		d := parseOG(b)
		for _, v := range []string{d.title, d.description, d.site, d.image, d.htmlTitle, d.metaDescription} {
			if len(v) > len(b) {
				t.Fatalf("value longer than the page: %q", v)
			}
			if c := cleanText(v, maxPreviewTitle); utf8.RuneCountInString(c) > maxPreviewTitle || !utf8.ValidString(c) {
				t.Fatalf("cleanText: %q", c)
			}
		}
		firstLink(string(b))
	})
}

// Mentionable roles can be mentioned by anyone; other roles, @everyone and
// @here need Mention @everyone/@here and All Roles.
func TestParseMentionsRoles(t *testing.T) {
	s := &Server{
		roles: map[int64]*roleRec{
			1: {ID: 1, Name: "@everyone", Perms: DefaultEveryonePerms, Builtin: true},
			2: {ID: 2, Name: "Helpers", Position: 1, Mentionable: true},
			3: {ID: 3, Name: "Mods", Position: 2},
		},
		everyoneID: 1,
		members:    map[string]*memberRec{},
		settings:   DefaultSettings(),
	}
	author := &memberRec{Key: []byte("author-key-0123456789abcdef01234"), Status: wire.MemberStatus_MEMBER_STATUS_ACTIVE}
	s.members[string(author.Key)] = author
	c := &channelRec{ID: 1, Name: "general"}
	got := s.parseMentions(author, c, "<@&2> <@&3> <@&1> <@&999> <@&2> @everyone")
	if len(got.roles) != 1 || got.roles[0] != 2 || got.everyone {
		t.Fatalf("plain member: %+v", got)
	}
	s.roles[1].Perms |= PermMentionEveryone
	got = s.parseMentions(author, c, "<@&2> <@&3> @here `<@&3>`")
	if len(got.roles) != 2 || !got.here {
		t.Fatalf("with Mention @everyone: %+v", got)
	}
}
