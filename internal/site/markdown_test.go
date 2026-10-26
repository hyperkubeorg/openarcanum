package site

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/pow"
)

func TestMarkdownRenders(t *testing.T) {
	cases := map[string]string{
		"Hello **bold** and *it* and _it_ and `x<y`":     "<p>Hello <strong>bold</strong> and <em>it</em> and <em>it</em> and <code>x&lt;y</code></p>\n",
		"# Title\n\nPara one\nstill one\n\nPara two":     "<h3>Title</h3>\n<p>Para one\nstill one</p>\n<p>Para two</p>\n",
		"- a\n- b\n\n1. one\n2. two":                     "<ul>\n<li>a</li>\n<li>b</li>\n</ul>\n<ol>\n<li>one</li>\n<li>two</li>\n</ol>\n",
		"> quoted *text*":                                "<blockquote>\n<p>quoted <em>text</em></p>\n</blockquote>\n",
		"```\n<b>code</b>\n```":                          "<pre><code>&lt;b&gt;code&lt;/b&gt;</code></pre>\n",
		"[site](https://openarcanum.example/a?b=1&c=2)":  `<p><a href="https://openarcanum.example/a?b=1&amp;c=2" rel="nofollow noopener noreferrer">site</a></p>` + "\n",
		"[local](/report/bug) <https://example.invalid>": `<p><a href="/report/bug" rel="nofollow noopener noreferrer">local</a> <a href="https://example.invalid" rel="nofollow noopener noreferrer">https://example.invalid</a></p>` + "\n",
		"line one  \nline two":                           "<p>line one<br>\nline two</p>\n",
		"---":                                            "<hr>\n",
		`\*not em\*`:                                     "<p>*not em*</p>\n",
	}
	for in, want := range cases {
		if got := string(Markdown(in)); got != want {
			t.Errorf("Markdown(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}

var (
	anyTag  = regexp.MustCompile(`<(/?)([a-zA-Z0-9]+)([^>]*)>`)
	allowed = map[string]bool{"p": true, "br": true, "h3": true, "h4": true, "h5": true, "strong": true, "em": true, "code": true,
		"pre": true, "ul": true, "ol": true, "li": true, "blockquote": true, "hr": true, "a": true}
	hrefAttr = regexp.MustCompile(`^ href="([^"]*)" rel="nofollow noopener noreferrer"$`)
)

// Injection attempts come out inert: only the allowed tags, no other
// attributes, and links only to safe schemes.
func TestMarkdownNeutralisesInjection(t *testing.T) {
	attacks := []string{
		`<script>alert(1)</script>`,
		`<img src=x onerror=alert(1)>`,
		`<a href="javascript:alert(1)">x</a>`,
		`[click](javascript:alert(1))`,
		`[click](JaVaScRiPt:alert(1))`,
		`[click](java&#x09;script:alert(1))`,
		"[click](java\tscript:alert(1))",
		`[click](data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==)`,
		`[click](vbscript:msgbox(1))`,
		`[click](//evil.example/x)`,
		`[click](/\evil.example)`,
		`[x](https://ok.example/" onmouseover="alert(1))`,
		`[x](https://ok.example/'onmouseover='alert(1)')`,
		`[<img src=x onerror=alert(1)>](https://ok.example)`,
		`<javascript:alert(1)>`,
		`<https://ok.example/"><script>alert(1)</script>>`,
		"**<svg/onload=alert(1)>**",
		"`</code><script>alert(1)</script>`",
		"```\n</code></pre><script>alert(1)</script>\n```",
		"# <iframe src=//evil>",
		"> <style>body{display:none}</style>",
		"- <form action=//evil><input name=x></form>",
		`[x](https://user:pass@ok.example/)`,
		"\x00<script>alert(1)</script>",
		`<!-- --><script>alert(1)</script>`,
		`[a](https://ok.example)](javascript:alert(1))`,
		strings.Repeat("[", 5000) + "x" + strings.Repeat("](javascript:alert(1))", 100),
		strings.Repeat("**", 3000) + "x",
		strings.Repeat("> ", 500) + "deep",
	}
	for _, a := range attacks {
		done := make(chan string, 1)
		go func() { done <- string(Markdown(a)) }()
		var out string
		select {
		case out = <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("Markdown(%.40q) is too slow", a)
		}
		for _, m := range anyTag.FindAllStringSubmatch(out, -1) {
			tag, attrs := strings.ToLower(m[2]), m[3]
			if !allowed[tag] {
				t.Errorf("%.60q produced <%s>: %q", a, tag, out)
				continue
			}
			if attrs == "" || m[1] == "/" {
				continue
			}
			hm := hrefAttr.FindStringSubmatch(attrs)
			if tag != "a" || hm == nil {
				t.Errorf("%.60q produced attributes %q on <%s>", a, attrs, tag)
				continue
			}
			if !safeTestHref(strings.ToLower(hm[1])) {
				t.Errorf("%.60q produced href %q", a, hm[1])
			}
		}
		// Text may say "javascript:" or "onerror=" (escaped, it is just
		// text); markup may not.
		low := strings.ToLower(out)
		for _, bad := range []string{"<script", "<img", "<svg", "<iframe", "<style", "<form", "<input", "<!--", `href="javascript`, `href="data`} {
			if strings.Contains(low, bad) {
				t.Errorf("%.60q left %q in %.200q", a, bad, out)
			}
		}
	}
}

func safeTestHref(href string) bool {
	if strings.Contains(href, "@") {
		return false
	}
	for _, p := range []string{"https://", "http://", "mailto:", "#"} {
		if strings.HasPrefix(href, p) {
			return true
		}
	}
	return strings.HasPrefix(href, "/") && !strings.HasPrefix(href, "//") && !strings.HasPrefix(href, "/\\")
}

func FuzzMarkdown(f *testing.F) {
	f.Add("**a** [b](https://c.example) `d` <e>")
	f.Add("> - [x](javascript:y)\n```\n<z>")
	f.Fuzz(func(t *testing.T, s string) {
		out := string(Markdown(s))
		for _, m := range anyTag.FindAllStringSubmatch(out, -1) {
			if !allowed[strings.ToLower(m[2])] {
				t.Fatalf("tag <%s> from %q", m[2], s)
			}
			if m[3] != "" && m[1] != "/" && (m[2] != "a" || !hrefAttr.MatchString(m[3])) {
				t.Fatalf("attributes %q from %q", m[3], s)
			}
		}
	})
}

// static/pow.js finds counters the server accepts (needs Node; skipped
// without it).
func TestPoWJavaScriptSolver(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	src, err := filepath.Abs("static/pow.js")
	if err != nil {
		t.Fatal(err)
	}
	g := newPoWGate([]byte("k"), 12)
	for i := range 3 {
		ch := g.challenge()
		harness := `
const fs = require('fs'), vm = require('vm');
const field = { value: '' };
const form = { dataset: { powContext: process.argv[2], powChallenge: process.argv[3], powBits: process.argv[4] },
  querySelector: (s) => (s.includes('pow_counter') ? field : null), addEventListener() {}, submit() {} };
global.document = { querySelector: () => form, getElementById: () => null };
vm.runInThisContext(fs.readFileSync(process.argv[5], 'utf8'));
const t = setInterval(() => { if (field.value) { console.log(field.value); clearInterval(t); } }, 5);
`
		hp := filepath.Join(t.TempDir(), "h.js")
		if err := os.WriteFile(hp, []byte(harness), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(node, hp, powContext, ch, "12", src)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("node: %v", err)
		}
		counter := strings.TrimSpace(string(out))
		if len(counter) != 16 {
			t.Fatalf("counter %q", counter)
		}
		if err := g.check(ch, counter); err != nil {
			t.Fatalf("round %d: the server rejected the JavaScript solution %s", i, counter)
		}
		// And it is what the Go solver finds first.
		want, _ := pow.SolveIn(context.Background(), powContext, []byte(ch), 12)
		if counter != hexString(want) {
			t.Fatalf("JS %s, Go %x", counter, want)
		}
	}
}

func TestLimiter(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	l := newLimiter(2, time.Minute)
	l.now = c.now
	first, second, third := l.take("a"), l.take("a"), l.take("a")
	if !first || !second || third || !l.take("b") {
		t.Fatal("take")
	}
	c.add(time.Minute)
	if !l.take("a") || l.full("b") {
		t.Fatal("window")
	}
}
