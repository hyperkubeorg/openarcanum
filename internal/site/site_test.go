package site

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/pow"
)

func TestMain(m *testing.M) {
	// Cheap Argon2 for tests; the real parameters are checked in
	// TestPassphraseHashing.
	argonParams.time, argonParams.memory, argonParams.threads = 1, 64, 1
	os.Exit(m.Run())
}

const testPass = "correct horse battery staple"

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type testSite struct {
	t     *testing.T
	srv   *Server
	st    *Store
	h     http.Handler
	dir   string
	clock *clock
	log   *bytes.Buffer
}

func newSite(t *testing.T, edit ...func(*Config)) *testSite {
	t.Helper()
	cfg := DefaultConfig()
	cfg.DataDir = filepath.Join(t.TempDir(), "data")
	cfg.ReportPoWBits = 8
	for _, f := range edit {
		f(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	st, err := OpenStore(context.Background(), cfg.DataDir, cfg.KeyFile(), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	c := &clock{t: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)}
	st.now = c.now
	srv, err := New(cfg, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.now = c.now
	srv.pow.now = c.now
	for _, l := range []*limiter{srv.reportsAll, srv.reportsPerIP, srv.loginsPerIP} {
		l.now = c.now
	}
	if err := st.AddAdmin(context.Background(), "alice", HashPassphrase(testPass)); err != nil {
		t.Fatal(err)
	}
	ts := &testSite{t: t, srv: srv, st: st, dir: cfg.DataDir, clock: c, log: &bytes.Buffer{}}
	ts.h = srv.Handler(ts.log)
	return ts
}

// req is one request. form != nil makes it a POST; same-origin browser
// headers are added unless cross is set.
type req struct {
	method, path string
	form         url.Values
	cookies      []*http.Cookie
	ip           string
	header       map[string]string
}

func (ts *testSite) do(r req) *httptest.ResponseRecorder {
	ts.t.Helper()
	method := r.method
	var body io.Reader
	if method == "" {
		method = http.MethodGet
		if r.form != nil {
			method = http.MethodPost
		}
	}
	if r.form != nil {
		body = strings.NewReader(r.form.Encode())
	}
	hr := httptest.NewRequest(method, "https://openarcanum.example"+r.path, body)
	if r.form != nil {
		hr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		hr.Header.Set("Sec-Fetch-Site", "same-origin")
	}
	for k, v := range r.header {
		hr.Header.Set(k, v)
	}
	if r.ip != "" {
		hr.RemoteAddr = net.JoinHostPort(r.ip, "40000")
	}
	for _, c := range r.cookies {
		hr.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	ts.h.ServeHTTP(rec, hr)
	return rec
}

func cookieNamed(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

var hiddenRE = func(name string) *regexp.Regexp {
	return regexp.MustCompile(`name="` + name + `" value="([^"]*)"`)
}

func field(t *testing.T, body, name string) string {
	t.Helper()
	m := hiddenRE(name).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no %s field in page", name)
	}
	return m[1]
}

// login signs alice in and returns her session cookie and CSRF token.
func (ts *testSite) login(code string) (*http.Cookie, string) {
	ts.t.Helper()
	rec := ts.do(req{path: "/admin/login"})
	lc := cookieNamed(rec, loginCookie)
	tok := field(ts.t, rec.Body.String(), "token")
	rec = ts.do(req{path: "/admin/login", form: url.Values{"token": {tok}, "name": {"alice"}, "passphrase": {testPass}, "code": {code}}, cookies: []*http.Cookie{lc}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin" {
		ts.t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	sc := cookieNamed(rec, sessionCookie)
	if sc == nil {
		ts.t.Fatal("no session cookie")
	}
	rec = ts.do(req{path: "/admin/reports", cookies: []*http.Cookie{sc}})
	if rec.Code != 200 {
		ts.t.Fatalf("after login: %d", rec.Code)
	}
	return sc, field(ts.t, rec.Body.String(), "csrf")
}

func (ts *testSite) tryLogin(name, pass, ip string) int {
	rec := ts.do(req{path: "/admin/login", ip: ip})
	lc := cookieNamed(rec, loginCookie)
	tok := field(ts.t, rec.Body.String(), "token")
	return ts.do(req{path: "/admin/login", ip: ip, form: url.Values{"token": {tok}, "name": {name}, "passphrase": {pass}}, cookies: []*http.Cookie{lc}}).Code
}

// ---------------------------------------------------------------- headers and pages

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	ts := newSite(t)
	for _, p := range []string{"/", "/report/bug", "/report/security", "/security", "/admin/login", "/static/site.css", "/nope", "/admin/reports", "/.well-known/security.txt"} {
		rec := ts.do(req{path: p})
		h := rec.Header()
		if h.Get("Content-Security-Policy") != ContentSecurityPolicy ||
			h.Get("X-Content-Type-Options") != "nosniff" ||
			h.Get("Referrer-Policy") != "no-referrer" ||
			h.Get("Permissions-Policy") != PermissionsPolicy ||
			h.Get("X-Frame-Options") != "DENY" {
			t.Errorf("%s: headers %v", p, h)
		}
		if strings.HasPrefix(p, "/admin") && !strings.Contains(h.Get("Cache-Control"), "no-store") {
			t.Errorf("%s: admin page cacheable", p)
		}
		if h.Get("Strict-Transport-Security") != "" {
			t.Errorf("%s: HSTS without TLS", p)
		}
	}
	for _, d := range []string{"default-src 'self'", "frame-ancestors 'none'", "object-src 'none'", "base-uri 'none'"} {
		if !strings.Contains(ContentSecurityPolicy, d) {
			t.Errorf("CSP lacks %q", d)
		}
	}
	if strings.Contains(ContentSecurityPolicy, "unsafe") {
		t.Error("CSP allows unsafe-*")
	}
	if rec := ts.do(req{path: "/static/"}); rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "pow.js") {
		t.Errorf("static directory listing: %d", rec.Code)
	}
	if rec := ts.do(req{path: "/static/pow.js"}); rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/javascript") {
		t.Errorf("pow.js: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	ts.srv.tls = true
	if rec := ts.do(req{path: "/"}); !strings.HasPrefix(rec.Header().Get("Strict-Transport-Security"), "max-age=") {
		t.Error("no HSTS with TLS")
	}
}

var (
	scriptTag  = regexp.MustCompile(`<script[^>]*>`)
	remoteRef  = regexp.MustCompile(`(?i)(src|href|action)="(https?:)?//`)
	inlineAttr = regexp.MustCompile(`(?i)\s(style|on[a-z]+)=`)
)

// Pages load nothing from elsewhere and have no inline script or style.
func TestPagesAreSelfContained(t *testing.T) {
	ts := newSite(t)
	sc, csrf := ts.login("")
	ts.do(req{path: "/admin/announcements/new", cookies: []*http.Cookie{sc}, form: url.Values{"csrf": {csrf}, "title": {"Hello"}, "body": {"Text with [a link](https://example.invalid/x)."}, "status": {"published"}}})
	for _, p := range []string{"/", "/report/bug", "/report/security", "/security", "/admin/login", "/nope", "/announcements/1"} {
		body := ts.do(req{path: p}).Body.String()
		for _, tag := range scriptTag.FindAllString(body, -1) {
			if !strings.Contains(tag, `src="/static/`) {
				t.Errorf("%s: inline or remote script %q", p, tag)
			}
		}
		if strings.Contains(body, "<style") || inlineAttr.MatchString(body) {
			t.Errorf("%s: inline style or event handler", p)
		}
		// The only absolute link allowed is the announcement's own.
		for _, m := range remoteRef.FindAllString(body, -1) {
			if !strings.Contains(body, `href="https://example.invalid/x"`) || !strings.HasPrefix(m, "href") {
				t.Errorf("%s: remote reference %q", p, m)
			}
		}
	}
	for _, p := range []string{"/admin", "/admin/reports", "/admin/announcements", "/admin/announcements/1"} {
		body := ts.do(req{path: p, cookies: []*http.Cookie{sc}}).Body.String()
		if strings.Contains(body, "<style") || inlineAttr.MatchString(body) {
			t.Errorf("%s: inline style or event handler", p)
		}
	}
}

func TestLandingPageLinksAndAnnouncements(t *testing.T) {
	ts := newSite(t, func(c *Config) { c.DownloadURL = "https://dl.example.invalid/oa" })
	body := ts.do(req{path: "/"}).Body.String()
	for _, want := range []string{"OpenArcanum", `href="/report/bug"`, `href="/report/security"`, `href="/security"`, `href="https://dl.example.invalid/oa"`, "No announcements yet."} {
		if !strings.Contains(body, want) {
			t.Errorf("landing page lacks %q", want)
		}
	}
	if strings.Contains(body, "Source code</a>") {
		t.Error("source link without source_url")
	}
	ctx := context.Background()
	for _, a := range []Announcement{
		{Title: "Old news", Body: "a", Published: true},
		{Title: "Pinned news", Body: "b", Published: true, Pinned: true},
		{Title: "Secret draft", Body: "c"},
	} {
		if _, err := ts.st.SaveAnnouncement(ctx, &a, 1); err != nil {
			t.Fatal(err)
		}
		ts.clock.add(time.Hour)
	}
	if _, err := ts.st.SaveAnnouncement(ctx, &Announcement{Title: "Newest", Body: "d", Published: true}, 1); err != nil {
		t.Fatal(err)
	}
	body = ts.do(req{path: "/"}).Body.String()
	pi, ni, oi := strings.Index(body, "Pinned news"), strings.Index(body, "Newest"), strings.Index(body, "Old news")
	if pi < 0 || ni < 0 || oi < 0 || pi > ni || ni > oi {
		t.Fatalf("order pinned %d newest %d old %d", pi, ni, oi)
	}
	if strings.Contains(body, "Secret draft") {
		t.Fatal("draft shown")
	}
	if ts.do(req{path: "/announcements/3"}).Code != http.StatusNotFound {
		t.Fatal("draft reachable by id")
	}
}

func TestWebClient(t *testing.T) {
	if body := newSite(t).do(req{path: "/"}).Body.String(); strings.Contains(body, `href="/app/"`) {
		t.Error("web app link without webclient_dir")
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "pyodide"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"index.html": "<!doctype html>web", "pyodide/pyodide.asm.wasm": "\x00asm", "worker.js": "x"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ts := newSite(t, func(c *Config) { c.WebClientDir = dir })
	if body := ts.do(req{path: "/"}).Body.String(); !strings.Contains(body, `href="/app/"`) {
		t.Error("landing page lacks the web app link")
	}
	if rec := ts.do(req{path: "/"}); rec.Header().Get("Content-Security-Policy") != ContentSecurityPolicy {
		t.Error("the site's own pages lost their policy")
	}
	rec := ts.do(req{path: "/app/"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "web") {
		t.Fatalf("/app/: %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != WebClientCSP {
		t.Errorf("/app/ policy %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("/app/ cache %q", got)
	}
	rec = ts.do(req{path: "/app/pyodide/pyodide.asm.wasm"})
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/wasm" || !strings.Contains(rec.Header().Get("Cache-Control"), "max-age") {
		t.Errorf("wasm: %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Cache-Control"))
	}
	for _, p := range []string{"/app/pyodide/", "/app/missing.js", "/app/../site.json"} {
		if code := ts.do(req{path: p}).Code; code != http.StatusNotFound && code != http.StatusMovedPermanently {
			t.Errorf("%s: %d", p, code)
		}
	}
	if code := ts.do(req{path: "/app"}).Code; code != http.StatusMovedPermanently {
		t.Errorf("/app: %d", code)
	}
}

// ---------------------------------------------------------------- admin authorization

var adminRoutes = []struct{ method, path string }{
	{"GET", "/admin"},
	{"GET", "/admin/"},
	{"GET", "/admin/reports"},
	{"GET", "/admin/reports/1"},
	{"POST", "/admin/reports/1"},
	{"POST", "/admin/reports/1/delete"},
	{"GET", "/admin/announcements"},
	{"GET", "/admin/announcements/new"},
	{"POST", "/admin/announcements/new"},
	{"GET", "/admin/announcements/1"},
	{"POST", "/admin/announcements/1"},
	{"POST", "/admin/announcements/1/delete"},
	{"POST", "/admin/logout"},
	{"GET", "/admin/no-such-page"},
	{"GET", "/%61dmin/reports"},
}

// Every admin route, and anything else under /admin, sends a browser
// without a valid session to the sign-in page and does nothing.
func TestAdminRoutesNeedSignIn(t *testing.T) {
	ts := newSite(t)
	ctx := context.Background()
	if _, err := ts.st.SaveAnnouncement(ctx, &Announcement{Title: "Keep me", Body: "x", Published: true}, 1); err != nil {
		t.Fatal(err)
	}
	ref, err := ts.st.AddReport(ctx, "bug", ReportContent{Title: "A bug", Description: strings.Repeat("d", 30), UserID: strings.Repeat("A", 43), Community: "c"})
	if err != nil {
		t.Fatal(err)
	}
	bogus := &http.Cookie{Name: sessionCookie, Value: newToken()}
	for _, cookies := range [][]*http.Cookie{nil, {bogus}} {
		for _, rt := range adminRoutes {
			r := req{method: rt.method, path: rt.path, cookies: cookies}
			if rt.method == "POST" {
				r.form = url.Values{"csrf": {"x"}, "title": {"Pwned"}, "body": {"pwned"}, "status": {"fixed"}, "confirm": {"on"}}
			}
			rec := ts.do(r)
			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/login" {
				t.Errorf("%s %s (cookie %v): %d %s", rt.method, rt.path, cookies != nil, rec.Code, rec.Header().Get("Location"))
			}
		}
	}
	a, err := ts.st.Announcement(ctx, 1)
	if err != nil || a.Title != "Keep me" {
		t.Fatalf("announcement changed: %+v %v", a, err)
	}
	list, _ := ts.st.Reports(ctx, ReportFilter{})
	if len(list) != 1 || list[0].Ref != ref || list[0].Status != "new" {
		t.Fatalf("report changed: %+v", list)
	}
}

// ---------------------------------------------------------------- sign-in, sessions, CSRF

func TestSignInSessionCookie(t *testing.T) {
	ts := newSite(t)
	rec := ts.do(req{path: "/admin/login"})
	lc := cookieNamed(rec, loginCookie)
	if lc == nil || !lc.Secure || !lc.HttpOnly || lc.SameSite != http.SameSiteStrictMode || lc.Path != "/admin" {
		t.Fatalf("login cookie %+v", lc)
	}
	sc, _ := ts.login("")
	rec = ts.do(req{path: "/admin/login"})
	lc = cookieNamed(rec, loginCookie)
	rec = ts.do(req{path: "/admin/login", cookies: []*http.Cookie{lc, sc}, form: url.Values{"token": {field(t, rec.Body.String(), "token")}, "name": {"alice"}, "passphrase": {testPass}}})
	sc2 := cookieNamed(rec, sessionCookie)
	if sc2 == nil || !sc2.Secure || !sc2.HttpOnly || sc2.SameSite != http.SameSiteStrictMode || sc2.Path != "/admin" || len(sc2.Value) != 43 {
		t.Fatalf("session cookie %+v", sc2)
	}
	if sc2.Value == sc.Value {
		t.Fatal("session not rotated at sign-in")
	}
	if ts.do(req{path: "/admin", cookies: []*http.Cookie{sc}}).Code != http.StatusSeeOther {
		t.Fatal("the session from before signing in again still works")
	}
	if ts.do(req{path: "/admin", cookies: []*http.Cookie{sc2}}).Code != 200 {
		t.Fatal("new session doesn't work")
	}
	// Only a hash of the token is stored.
	db, _ := os.ReadFile(filepath.Join(ts.dir, DBFile))
	wal, _ := os.ReadFile(filepath.Join(ts.dir, DBFile+"-wal"))
	if bytes.Contains(append(db, wal...), []byte(sc2.Value)) {
		t.Fatal("session token stored in the clear")
	}
}

func TestSignInNeedsFormToken(t *testing.T) {
	ts := newSite(t)
	rec := ts.do(req{path: "/admin/login", form: url.Values{"token": {"forged"}, "name": {"alice"}, "passphrase": {testPass}}, cookies: []*http.Cookie{{Name: loginCookie, Value: "other"}}})
	if rec.Code != http.StatusForbidden || cookieNamed(rec, sessionCookie) != nil {
		t.Fatalf("%d", rec.Code)
	}
	// Cross-site posts are refused before anything else.
	rec = ts.do(req{path: "/admin/login", form: url.Values{}, header: map[string]string{"Sec-Fetch-Site": "cross-site"}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site: %d", rec.Code)
	}
	rec = ts.do(req{path: "/admin/login", form: url.Values{}, header: map[string]string{"Sec-Fetch-Site": "", "Origin": "https://evil.example"}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("foreign origin: %d", rec.Code)
	}
}

func TestLockoutAndIPLimit(t *testing.T) {
	ts := newSite(t)
	// Wrong passphrases from different networks: the account locks.
	for i := range lockAfter {
		if code := ts.tryLogin("alice", "wrong passphrase!", "198.51.100."+string(rune('1'+i))); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, code)
		}
	}
	if code := ts.tryLogin("alice", testPass, "203.0.113.50"); code != http.StatusUnauthorized {
		t.Fatalf("locked account accepted the right passphrase: %d", code)
	}
	a, _ := ts.st.AdminByName(context.Background(), "alice")
	if !a.LockedUntil.After(ts.clock.now()) {
		t.Fatal("not locked")
	}
	// Unknown names fail the same way.
	if code := ts.tryLogin("mallory", testPass, "203.0.113.51"); code != http.StatusUnauthorized {
		t.Fatalf("unknown name: %d", code)
	}
	ts.clock.add(2 * time.Minute)
	if code := ts.tryLogin("alice", testPass, "203.0.113.52"); code != http.StatusSeeOther {
		t.Fatalf("after the lockout: %d", code)
	}
	// The backoff grows with failures after the threshold.
	if lockFor(lockAfter) != time.Minute || lockFor(lockAfter+1) != 2*time.Minute || lockFor(100) != lockMax || lockFor(lockAfter-1) != 0 {
		t.Fatal("backoff")
	}

	// One network: after loginAttemptsPerIP failures it is refused, even
	// with the right passphrase, until the window passes.
	ts = newSite(t)
	if err := ts.st.AddAdmin(context.Background(), "bob", HashPassphrase(testPass)); err != nil {
		t.Fatal(err)
	}
	for i := range loginAttemptsPerIP {
		name := "nobody"
		if i%2 == 0 {
			name = "bob"
		}
		ts.tryLogin(name, "wrong passphrase!", fmt.Sprintf("2001:db8:1:1::%x", i+1))
		ts.st.Unlock(context.Background(), "bob")
	}
	if code := ts.tryLogin("alice", testPass, "2001:db8:1:1::ffff"); code != http.StatusTooManyRequests {
		t.Fatalf("same /64 after %d failures: %d", loginAttemptsPerIP, code)
	}
	if code := ts.tryLogin("alice", testPass, "2001:db8:1:2::1"); code != http.StatusSeeOther {
		t.Fatalf("other network: %d", code)
	}
	ts.clock.add(loginWindow)
	if code := ts.tryLogin("alice", testPass, "2001:db8:1:1::ffff"); code != http.StatusSeeOther {
		t.Fatalf("after the window: %d", code)
	}
}

// A batch of concurrent guesses can't all run against the same unlocked
// account: sign-ins for one account run one at a time, so the lockout set
// by the fifth failure stops the rest.
func TestConcurrentSignInsCantOutrunLockout(t *testing.T) {
	ts := newSite(t)
	const n = 20
	type form struct {
		lc  *http.Cookie
		tok string
	}
	forms := make([]form, n)
	for i := range forms {
		rec := ts.do(req{path: "/admin/login"})
		forms[i] = form{cookieNamed(rec, loginCookie), field(t, rec.Body.String(), "token")}
	}
	var wg sync.WaitGroup
	for i, f := range forms {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ts.do(req{path: "/admin/login", ip: fmt.Sprintf("198.51.100.%d", i+1), cookies: []*http.Cookie{f.lc},
				form: url.Values{"token": {f.tok}, "name": {"alice"}, "passphrase": {fmt.Sprintf("wrong guess %d", i)}}})
		}()
	}
	wg.Wait()
	a, err := ts.st.AdminByName(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if a.Failures > lockAfter {
		t.Fatalf("%d guesses were checked; the lockout should have stopped them after %d", a.Failures, lockAfter)
	}
}

func TestLocalPath(t *testing.T) {
	for _, ok := range []string{"/", "/report/bug", "/announcements/3"} {
		if !localPath(ok) {
			t.Errorf("refused %q", ok)
		}
	}
	for _, bad := range []string{"", "x", "//evil.example", "/\\evil.example", "/\t/evil.example", "/\n/evil.example", "https://evil.example", "/\x7f"} {
		if localPath(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestAdminFormsNeedCSRF(t *testing.T) {
	ts := newSite(t)
	sc, csrf := ts.login("")
	post := func(form url.Values, h map[string]string) int {
		return ts.do(req{path: "/admin/announcements/new", cookies: []*http.Cookie{sc}, form: form, header: h}).Code
	}
	ann := func(token string) url.Values {
		return url.Values{"csrf": {token}, "title": {"T"}, "body": {"B"}, "status": {"published"}}
	}
	if c := post(ann(""), nil); c != http.StatusForbidden {
		t.Errorf("no token: %d", c)
	}
	if c := post(ann(strings.Repeat("0", len(csrf))), nil); c != http.StatusForbidden {
		t.Errorf("wrong token: %d", c)
	}
	if c := post(ann(csrf), map[string]string{"Sec-Fetch-Site": "same-site"}); c != http.StatusForbidden {
		t.Errorf("same-site (another subdomain): %d", c)
	}
	if c := post(ann(csrf), map[string]string{"Sec-Fetch-Site": "", "Origin": "https://evil.example"}); c != http.StatusForbidden {
		t.Errorf("foreign Origin: %d", c)
	}
	if anns, _ := ts.st.Announcements(context.Background(), true, 10); len(anns) != 0 {
		t.Fatal("a refused form saved something")
	}
	// Browsers send one of the two headers with every POST.
	if c := post(ann(csrf), map[string]string{"Sec-Fetch-Site": ""}); c != http.StatusForbidden {
		t.Errorf("neither header: %d", c)
	}
	// no-referrer pages send Origin: null; the token still decides.
	if c := post(ann(csrf), map[string]string{"Sec-Fetch-Site": "", "Origin": "null"}); c != http.StatusSeeOther {
		t.Errorf("right token: %d", c)
	}
	// Another admin's token doesn't work in this session.
	if err := ts.st.AddAdmin(context.Background(), "carol", HashPassphrase(testPass)); err != nil {
		t.Fatal(err)
	}
	if c := post(ann("deadbeef"), nil); c != http.StatusForbidden {
		t.Errorf("foreign token: %d", c)
	}
}

func TestSessionExpiry(t *testing.T) {
	ts := newSite(t)
	sc, _ := ts.login("")
	ts.clock.add(ts.srv.cfg.SessionIdle.Duration - time.Minute)
	if ts.do(req{path: "/admin", cookies: []*http.Cookie{sc}}).Code != 200 {
		t.Fatal("expired before the idle timeout")
	}
	ts.clock.add(ts.srv.cfg.SessionIdle.Duration)
	if rec := ts.do(req{path: "/admin", cookies: []*http.Cookie{sc}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("idle session still works: %d", rec.Code)
	}
	// Active all along: still ends at session_ttl.
	sc, _ = ts.login("")
	for el := time.Duration(0); el < ts.srv.cfg.SessionTTL.Duration-time.Minute; el += 30 * time.Minute {
		if ts.do(req{path: "/admin", cookies: []*http.Cookie{sc}}).Code != 200 {
			t.Fatalf("ended early at %v", el)
		}
		ts.clock.add(30 * time.Minute)
	}
	ts.clock.add(time.Minute)
	if ts.do(req{path: "/admin", cookies: []*http.Cookie{sc}}).Code != http.StatusSeeOther {
		t.Fatal("session outlived session_ttl")
	}
	// Signing out ends it at once.
	sc, csrf := ts.login("")
	ts.do(req{path: "/admin/logout", cookies: []*http.Cookie{sc}, form: url.Values{"csrf": {csrf}}})
	if ts.do(req{path: "/admin", cookies: []*http.Cookie{sc}}).Code != http.StatusSeeOther {
		t.Fatal("session works after signing out")
	}
	// Removing the admin ends their sessions.
	sc, _ = ts.login("")
	if err := ts.st.SetPassphrase(context.Background(), "alice", HashPassphrase(testPass)); err != nil {
		t.Fatal(err)
	}
	if ts.do(req{path: "/admin", cookies: []*http.Cookie{sc}}).Code != http.StatusSeeOther {
		t.Fatal("session survived a passphrase change")
	}
}

func TestTOTP(t *testing.T) {
	// RFC 6238 Appendix B (SHA-1), last 6 digits.
	secret := []byte("12345678901234567890")
	for ts, want := range map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924", 2000000000: "279037"} {
		if got := TOTPCode(secret, time.Unix(ts, 0)); got != want {
			t.Errorf("T=%d: %s, want %s", ts, got, want)
		}
	}

	ts := newSite(t)
	sec := NewTOTPSecret()
	if err := ts.st.SetTOTP(context.Background(), "alice", sec); err != nil {
		t.Fatal(err)
	}
	if code := ts.tryLogin("alice", testPass, "192.0.2.10"); code != http.StatusUnauthorized {
		t.Fatalf("no code: %d", code)
	}
	ts.st.Unlock(context.Background(), "alice")
	code := TOTPCode(sec, ts.clock.now())
	ts.login(code)
	// The same code doesn't work twice.
	rec := ts.do(req{path: "/admin/login"})
	rec = ts.do(req{path: "/admin/login", cookies: []*http.Cookie{cookieNamed(rec, loginCookie)},
		form: url.Values{"token": {field(t, rec.Body.String(), "token")}, "name": {"alice"}, "passphrase": {testPass}, "code": {code}}})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("replayed code: %d", rec.Code)
	}
	ts.clock.add(30 * time.Second)
	ts.login(TOTPCode(sec, ts.clock.now()))
	if !strings.HasPrefix(TOTPURI("OpenArcanum", "alice", sec), "otpauth://totp/OpenArcanum:alice?") {
		t.Fatal(TOTPURI("OpenArcanum", "alice", sec))
	}
	// The secret is sealed at rest.
	db, _ := os.ReadFile(filepath.Join(ts.dir, DBFile))
	wal, _ := os.ReadFile(filepath.Join(ts.dir, DBFile+"-wal"))
	if bytes.Contains(append(db, wal...), sec) {
		t.Fatal("TOTP secret stored in the clear")
	}
}

func TestPassphraseHashing(t *testing.T) {
	saved := argonParams
	argonParams.time, argonParams.memory, argonParams.threads = 3, 64*1024, 4
	h := HashPassphrase("a long enough passphrase")
	argonParams = saved
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Fatalf("hash %q", h)
	}
	if !verifyPassphrase(h, "a long enough passphrase") || verifyPassphrase(h, "a long enough passphrasE") {
		t.Fatal("verify")
	}
	if h1, h2 := HashPassphrase("x"), HashPassphrase("x"); h1 == h2 {
		t.Fatal("no salt")
	}
	for _, bad := range []string{"", "$argon2i$v=19$m=65536,t=3,p=4$AAAA$AAAA", "$argon2id$v=19$m=999999999,t=3,p=4$AAAA$AAAA"} {
		if verifyPassphrase(bad, "x") {
			t.Errorf("accepted %q", bad)
		}
	}
	if CheckPassphraseStrength("short") == nil || CheckPassphraseStrength("twelve chars") != nil {
		t.Fatal("strength")
	}
}

// ---------------------------------------------------------------- reports

const goodUserID = "u7fKqGm0bE3x1Y9cWJp2LhT8sNdA4vRz6oQiXeBgMkc"

func solvedForm(t *testing.T, ts *testSite, kind string, v url.Values) url.Values {
	t.Helper()
	body := ts.do(req{path: "/report/" + kind}).Body.String()
	ch := field(t, body, "pow_challenge")
	counter, err := pow.SolveIn(context.Background(), powContext, []byte(ch), uint32(ts.srv.cfg.ReportPoWBits))
	if err != nil {
		t.Fatal(err)
	}
	out := url.Values{"pow_challenge": {ch}, "pow_counter": {hexString(counter)}}
	for k, vs := range v {
		out[k] = vs
	}
	return out
}

func hexString(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, 2*len(b))
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&15])
	}
	return string(out)
}

func goodReport() url.Values {
	return url.Values{
		"title":       {"Crash when opening settings"},
		"description": {"The app closes when I open Settings → Network. Every time."},
		"user_id":     {goodUserID},
		"community":   {"OpenArcanum Beta"},
		"email":       {"tester@example.invalid"},
	}
}

func TestValidUserID(t *testing.T) {
	if !ValidUserID(goodUserID) {
		t.Fatal("rejected a good id")
	}
	for _, bad := range []string{"", goodUserID[:42], goodUserID + "A", strings.Replace(goodUserID, "u", "+", 1),
		goodUserID[:42] + "d", // non-canonical: trailing bits set
		strings.Repeat("A", 42) + "=", "<script>alert(1)</script>xxxxxxxxxxxxxxxxxxx"} {
		if ValidUserID(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestReportSubmission(t *testing.T) {
	ts := newSite(t)
	form := solvedForm(t, ts, "security", goodReport())
	form.Set("severity", "high")
	rec := ts.do(req{path: "/report/security", form: form})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "your report arrived") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	list, err := ts.st.Reports(context.Background(), ReportFilter{})
	if err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
	r := list[0]
	if r.Kind != "security" || r.Status != "new" || r.Content.UserID != goodUserID || r.Content.Severity != "high" || !strings.Contains(rec.Body.String(), r.Ref) {
		t.Fatalf("%+v", r)
	}
	// The challenge works once.
	if rec := ts.do(req{path: "/report/security", form: form}); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "anti-spam") {
		t.Fatalf("replayed challenge: %d", rec.Code)
	}
	// A form that fails validation doesn't use its challenge up: the
	// corrected form goes through with the same solution (so solving
	// challenges for invalid forms can't fill the table of used ones).
	form = solvedForm(t, ts, "bug", goodReport())
	form.Set("user_id", "not-an-id")
	if rec := ts.do(req{path: "/report/bug", form: form}); rec.Code != http.StatusUnprocessableEntity || strings.Contains(rec.Body.String(), "didn't pass") {
		t.Fatalf("invalid form: %d", rec.Code)
	}
	form.Set("user_id", goodUserID)
	if rec := ts.do(req{path: "/report/bug", form: form}); rec.Code != 200 || !strings.Contains(rec.Body.String(), "your report arrived") {
		t.Fatalf("corrected form: %d %s", rec.Code, rec.Body.String())
	}
}

func TestReportValidation(t *testing.T) {
	ts := newSite(t)
	cases := map[string]func(v url.Values){
		"user_id":     func(v url.Values) { v.Set("user_id", "not-an-id") },
		"user_id ":    func(v url.Values) { v.Del("user_id") },
		"community":   func(v url.Values) { v.Del("community") },
		"title":       func(v url.Values) { v.Set("title", "line\nbreak") },
		"title ":      func(v url.Values) { v.Set("title", strings.Repeat("x", maxTitle+1)) },
		"description": func(v url.Values) { v.Set("description", "too short") },
		"email":       func(v url.Values) { v.Set("email", "Mallory <m@example.invalid>") },
		"steps":       func(v url.Values) { v.Set("steps", "bidi \u202e override") },
	}
	for name, f := range cases {
		v := goodReport()
		f(v)
		rec := ts.do(req{path: "/report/bug", form: solvedForm(t, ts, "bug", v)})
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `id="`+strings.TrimSpace(name)) {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	// Without the proof of work.
	v := goodReport()
	v.Set("pow_challenge", ts.srv.pow.challenge())
	v.Set("pow_counter", "00")
	if rec := ts.do(req{path: "/report/bug", form: v}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("no PoW: %d", rec.Code)
	}
	// A forged challenge.
	v.Set("pow_challenge", strings.Repeat("A", 54))
	if rec := ts.do(req{path: "/report/bug", form: v}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("forged challenge: %d", rec.Code)
	}
	// An expired one.
	v = solvedForm(t, ts, "bug", goodReport())
	ts.clock.add(powTTL + time.Second)
	if rec := ts.do(req{path: "/report/bug", form: v}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("expired challenge: %d", rec.Code)
	}
	// Too large.
	v = goodReport()
	v.Set("description", strings.Repeat("x", maxFormBytes))
	if rec := ts.do(req{path: "/report/bug", form: v}); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("too large: %d", rec.Code)
	}
	// Cross-site.
	if rec := ts.do(req{path: "/report/bug", form: solvedForm(t, ts, "bug", goodReport()), header: map[string]string{"Sec-Fetch-Site": "cross-site"}}); rec.Code != http.StatusForbidden {
		t.Errorf("cross-site: %d", rec.Code)
	}
	if list, _ := ts.st.Reports(context.Background(), ReportFilter{}); len(list) != 0 {
		t.Fatalf("stored %d invalid reports", len(list))
	}
}

func TestReportHoneypotAndRateLimit(t *testing.T) {
	ts := newSite(t, func(c *Config) { c.ReportsPerHour = 2; c.ReportsPerDay = 3 })
	v := goodReport()
	v.Set("website", "http://spam.example")
	if rec := ts.do(req{path: "/report/bug", form: solvedForm(t, ts, "bug", v), ip: "192.0.2.200"}); rec.Code != 200 {
		t.Fatalf("honeypot: %d", rec.Code)
	}
	if list, _ := ts.st.Reports(context.Background(), ReportFilter{}); len(list) != 0 {
		t.Fatal("honeypot report stored")
	}
	ip := "198.51.100.77"
	for i := range 2 {
		if rec := ts.do(req{path: "/report/bug", form: solvedForm(t, ts, "bug", goodReport()), ip: ip}); rec.Code != 200 {
			t.Fatalf("report %d: %d", i, rec.Code)
		}
	}
	if rec := ts.do(req{path: "/report/bug", form: solvedForm(t, ts, "bug", goodReport()), ip: ip}); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over the hourly limit: %d", rec.Code)
	}
	ts.clock.add(time.Hour)
	if rec := ts.do(req{path: "/report/bug", form: solvedForm(t, ts, "bug", goodReport()), ip: ip}); rec.Code != 200 {
		t.Fatalf("after an hour: %d", rec.Code)
	}
	// The daily limit for everyone together.
	if rec := ts.do(req{path: "/report/bug", form: solvedForm(t, ts, "bug", goodReport()), ip: "203.0.113.9"}); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("over the daily limit: %d", rec.Code)
	}
}

// Report contents and notes never reach the database file in the clear.
func TestReportsEncryptedAtRest(t *testing.T) {
	ts := newSite(t)
	markers := []string{"ZEBRA-TITLE-MARKER", "QUOKKA-DESCRIPTION-MARKER", "WOMBAT-STEPS", "narwhal@example.invalid", "AXOLOTL-COMMUNITY", "PANGOLIN-NOTE"}
	v := goodReport()
	v.Set("title", markers[0])
	v.Set("description", markers[1]+" with enough text to pass")
	v.Set("steps", markers[2])
	v.Set("email", markers[3])
	v.Set("community", markers[4])
	if rec := ts.do(req{path: "/report/security", form: solvedForm(t, ts, "security", v)}); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if err := ts.st.TriageReport(context.Background(), 1, 1, Triage{Status: "triaged", Severity: "low", Note: markers[5]}); err != nil {
		t.Fatal(err)
	}
	ts.st.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	var all []byte
	entries, _ := os.ReadDir(ts.dir)
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(ts.dir, e.Name()))
		all = append(all, b...)
	}
	for _, m := range append(markers, goodUserID) {
		if bytes.Contains(all, []byte(m)) {
			t.Errorf("%q is in the data directory in the clear", m)
		}
	}
	// And reading it back works, through the key.
	r, err := ts.st.Report(context.Background(), 1)
	if err != nil || r.Content.Title != markers[0] {
		t.Fatal(r, err)
	}
	notes, err := ts.st.Notes(context.Background(), 1)
	if err != nil || len(notes) != 1 || !strings.Contains(notes[0].Body, markers[5]) || !strings.Contains(notes[0].Body, "Status: new → triaged") {
		t.Fatal(notes, err)
	}
	// A wrong key is refused.
	ts.st.Close()
	if err := os.WriteFile(filepath.Join(ts.dir, "storage.key"), []byte(strings.Repeat("ab", 32)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(context.Background(), ts.dir, filepath.Join(ts.dir, "storage.key"), false); err != ErrWrongKey {
		t.Fatalf("wrong key: %v", err)
	}
	if st, _ := os.Stat(ts.dir); st.Mode().Perm() != 0o700 {
		t.Fatalf("data dir mode %o", st.Mode().Perm())
	}
}

// What reporters send is shown escaped in the admin portal.
func TestAdminShowsReportsEscaped(t *testing.T) {
	ts := newSite(t)
	v := goodReport()
	v.Set("title", `<script>alert(1)</script>`)
	v.Set("description", `<img src=x onerror=alert(1)> and "quotes" & more text`)
	v.Set("community", `"><svg onload=alert(1)>`)
	if rec := ts.do(req{path: "/report/bug", form: solvedForm(t, ts, "bug", v)}); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	sc, csrf := ts.login("")
	for _, p := range []string{"/admin/reports", "/admin/reports/1"} {
		body := ts.do(req{path: p, cookies: []*http.Cookie{sc}}).Body.String()
		for _, raw := range []string{"<script>alert", "<img src=x", "<svg onload"} {
			if strings.Contains(body, raw) {
				t.Errorf("%s: %q unescaped", p, raw)
			}
		}
		if !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
			t.Errorf("%s: title not shown escaped", p)
		}
	}
	body := ts.do(req{path: "/admin/reports/1", cookies: []*http.Cookie{sc}}).Body.String()
	if !strings.Contains(body, `value="`+goodUserID+`"`) || !strings.Contains(body, `data-copy="reporter-id"`) {
		t.Fatal("user ID and its copy button missing")
	}
	// Triage: status, severity, assignee and a note.
	rec := ts.do(req{path: "/admin/reports/1", cookies: []*http.Cookie{sc}, form: url.Values{"csrf": {csrf}, "status": {"in-progress"}, "severity": {"critical"}, "assignee": {"1"}, "note": {"<b>looking</b>"}}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("triage: %d %s", rec.Code, rec.Body.String())
	}
	r, _ := ts.st.Report(context.Background(), 1)
	if r.Status != "in-progress" || r.Severity != "critical" || r.AssigneeN != "alice" {
		t.Fatalf("%+v", r)
	}
	body = ts.do(req{path: "/admin/reports/1", cookies: []*http.Cookie{sc}}).Body.String()
	if !strings.Contains(body, "&lt;b&gt;looking&lt;/b&gt;") || !strings.Contains(body, "Severity: none → critical") {
		t.Fatal("note not shown (escaped)")
	}
	if rec := ts.do(req{path: "/admin/reports/1", cookies: []*http.Cookie{sc}, form: url.Values{"csrf": {csrf}, "status": {"pwned"}, "severity": {""}}}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad status: %d", rec.Code)
	}
	// Filters.
	body = ts.do(req{path: "/admin/reports?kind=security", cookies: []*http.Cookie{sc}}).Body.String()
	if strings.Contains(body, "/admin/reports/1\"") {
		t.Fatal("kind filter")
	}
	body = ts.do(req{path: "/admin/reports?status=in-progress&severity=critical", cookies: []*http.Cookie{sc}}).Body.String()
	if !strings.Contains(body, "/admin/reports/1\"") {
		t.Fatal("status filter")
	}
	// Delete needs the confirmation box.
	if rec := ts.do(req{path: "/admin/reports/1/delete", cookies: []*http.Cookie{sc}, form: url.Values{"csrf": {csrf}}}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unconfirmed delete: %d", rec.Code)
	}
	if rec := ts.do(req{path: "/admin/reports/1/delete", cookies: []*http.Cookie{sc}, form: url.Values{"csrf": {csrf}, "confirm": {"on"}}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("delete: %d", rec.Code)
	}
	if _, err := ts.st.Report(context.Background(), 1); err != ErrNotFound {
		t.Fatal("not deleted")
	}
}

func TestAnnouncementCRUD(t *testing.T) {
	ts := newSite(t)
	sc, csrf := ts.login("")
	save := func(path string, v url.Values) *httptest.ResponseRecorder {
		v.Set("csrf", csrf)
		return ts.do(req{path: path, cookies: []*http.Cookie{sc}, form: v})
	}
	rec := save("/admin/announcements/new", url.Values{"title": {"Beta opens"}, "body": {"**Soon.** <script>alert(1)</script>"}, "status": {"draft"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/announcements/1?done=saved" {
		t.Fatalf("%d %s", rec.Code, rec.Header().Get("Location"))
	}
	if strings.Contains(ts.do(req{path: "/"}).Body.String(), "Beta opens") {
		t.Fatal("draft on the landing page")
	}
	save("/admin/announcements/1", url.Values{"title": {"Beta opens"}, "body": {"**Soon.** <script>alert(1)</script>"}, "status": {"published"}, "pinned": {"on"}})
	home := ts.do(req{path: "/"}).Body.String()
	if !strings.Contains(home, "<strong>Soon.</strong> &lt;script&gt;alert(1)&lt;/script&gt;") || !strings.Contains(home, "Pinned") {
		t.Fatalf("published announcement: %s", home)
	}
	a, _ := ts.st.Announcement(context.Background(), 1)
	if !a.Pinned || !a.Published || a.PublishedAt.IsZero() || a.Author != "alice" {
		t.Fatalf("%+v", a)
	}
	if rec := save("/admin/announcements/new", url.Values{"title": {""}, "body": {"x"}}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty title: %d", rec.Code)
	}
	if rec := save("/admin/announcements/99", url.Values{"title": {"x"}, "body": {"x"}}); rec.Code != http.StatusNotFound {
		t.Fatalf("missing: %d", rec.Code)
	}
	if rec := save("/admin/announcements/1/delete", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("delete: %d", rec.Code)
	}
	if strings.Contains(ts.do(req{path: "/"}).Body.String(), "Beta opens") {
		t.Fatal("deleted announcement shown")
	}
}

// ---------------------------------------------------------------- logs

// The access log has no client address, query string or report content;
// the error log no client address.
func TestAccessLogPrivacy(t *testing.T) {
	ts := newSite(t)
	v := solvedForm(t, ts, "bug", goodReport())
	ts.log.Reset()
	ts.do(req{path: "/report/bug?token=s3cret", form: v, ip: "203.0.113.99"})
	ts.do(req{path: "/?q=private", ip: "2001:db8::99"})
	ts.do(req{path: "/admin/reports/1234567"})
	line := ts.log.String()
	if strings.Count(line, "\n") != 3 {
		t.Fatalf("log: %q", line)
	}
	for _, leak := range []string{"203.0.113.99", "2001:db8", "s3cret", "private", "Crash when", goodUserID, "tester@", "1234567"} {
		if strings.Contains(line, leak) {
			t.Errorf("log has %q: %q", leak, line)
		}
	}
	if !strings.HasPrefix(line, `- - - [`) || !strings.Contains(line, `"POST /report/bug?- HTTP/1.1" 200`) || !strings.Contains(line, `"GET /admin/reports/- HTTP/1.1"`) {
		t.Fatalf("log format: %q", line)
	}

	ts = newSite(t, func(c *Config) { c.LogClientIPs = true })
	ts.do(req{path: "/", ip: "203.0.113.99"})
	if !strings.HasPrefix(ts.log.String(), "203.0.113.99 - - [") {
		t.Fatalf("opt-in log: %q", ts.log.String())
	}
}

func TestHostCheck(t *testing.T) {
	ts := newSite(t, func(c *Config) { c.TLSDomains = []string{"openarcanum.example"} })
	if ts.do(req{path: "/"}).Code != 200 {
		t.Fatal("own host refused")
	}
	hr := httptest.NewRequest("GET", "http://rebound.example/admin/login", nil)
	rec := httptest.NewRecorder()
	ts.h.ServeHTTP(rec, hr)
	if rec.Code != http.StatusMisdirectedRequest {
		t.Fatalf("other host: %d", rec.Code)
	}

	// The other names of the certificate (www) redirect to the first one.
	ts = newSite(t, func(c *Config) { c.TLSDomains = []string{"openarcanum.example", "www.openarcanum.example"} })
	hr = httptest.NewRequest("GET", "http://www.openarcanum.example/security?x=1", nil)
	rec = httptest.NewRecorder()
	ts.h.ServeHTTP(rec, hr)
	if rec.Code != http.StatusPermanentRedirect || rec.Header().Get("Location") != "https://openarcanum.example/security?x=1" {
		t.Fatalf("www: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	hr = httptest.NewRequest("POST", "http://www.openarcanum.example/report/bug", nil)
	rec = httptest.NewRecorder()
	ts.h.ServeHTTP(rec, hr)
	if rec.Code != http.StatusMisdirectedRequest {
		t.Fatalf("www post: %d", rec.Code)
	}
	if ts.do(req{path: "/"}).Code != 200 {
		t.Fatal("first name refused")
	}
}

func TestSecurityTxtAndTheme(t *testing.T) {
	ts := newSite(t)
	body := ts.do(req{path: "/.well-known/security.txt"}).Body.String()
	if !strings.Contains(body, "Contact: https://openarcanum.example/report/security") || !strings.Contains(body, "Expires: 2027-") {
		t.Fatal(body)
	}
	rec := ts.do(req{path: "/theme", form: url.Values{"theme": {"light"}, "back": {"//evil.example/"}}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" || cookieNamed(rec, "theme").Value != "light" {
		t.Fatalf("theme: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if !strings.Contains(ts.do(req{path: "/", cookies: []*http.Cookie{{Name: "theme", Value: "light"}}}).Body.String(), `data-theme="light"`) {
		t.Fatal("light theme not applied")
	}
	if !strings.Contains(ts.do(req{path: "/", cookies: []*http.Cookie{{Name: "theme", Value: `"><script>`}}}).Body.String(), `data-theme="dark"`) {
		t.Fatal("bad theme cookie")
	}
}

func TestBackup(t *testing.T) {
	ts := newSite(t)
	if _, err := ts.st.AddReport(context.Background(), "bug", ReportContent{Title: "x"}); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "backups")
	var last string
	for range 3 {
		p, err := ts.st.Backup(context.Background(), out, 2)
		if err != nil {
			t.Fatal(err)
		}
		last = p
		time.Sleep(5 * time.Millisecond)
	}
	entries, _ := os.ReadDir(out)
	if len(entries) != 2 {
		t.Fatalf("%d backups kept", len(entries))
	}
	st, _ := os.Stat(last)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode %o", st.Mode().Perm())
	}
	// The backup opens with the same key in a fresh data directory.
	dir := filepath.Join(t.TempDir(), "restored")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(last)
	if err := os.WriteFile(filepath.Join(dir, DBFile), b, 0o600); err != nil {
		t.Fatal(err)
	}
	st2, err := OpenStore(context.Background(), dir, filepath.Join(ts.dir, "storage.key"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if r, err := st2.Report(context.Background(), 1); err != nil || r.Content.Title != "x" {
		t.Fatal(r, err)
	}
}

func TestConfigValidate(t *testing.T) {
	base := func() Config { c := DefaultConfig(); c.DataDir = "/d"; return c }
	if c := base(); c.Validate() != nil {
		t.Fatal(c.Validate())
	}
	bad := map[string]func(*Config){
		"data_dir":             func(c *Config) { c.DataDir = "" },
		"listen":               func(c *Config) { c.Listen = "nope" },
		"http_listen":          func(c *Config) { c.HTTPListen = ":80" },
		"access_log":           func(c *Config) { c.AccessLog = "" },
		"trusted_proxy_header": func(c *Config) { c.TrustedProxyHeader = "X-Real-IP: 1" },
		"reports_per_hour":     func(c *Config) { c.ReportsPerHour = 0 },
		"reports_per_day":      func(c *Config) { c.ReportsPerDay = 0 },
		"report_pow_bits":      func(c *Config) { c.ReportPoWBits = 30 },
		"contact_community":    func(c *Config) { c.ContactCommunity = strings.Repeat("c", 201) },
		"download_url":         func(c *Config) { c.DownloadURL = "javascript:alert(1)" },
		"source_url":           func(c *Config) { c.SourceURL = "http://example.invalid" },
		"session_ttl":          func(c *Config) { c.SessionTTL.Duration = time.Second },
		"session_idle":         func(c *Config) { c.SessionIdle.Duration = 24 * time.Hour },
		"tls_domains":          func(c *Config) { c.TLSMode, c.TLSCertFile, c.TLSKeyFile = "files", "/c", "/k" },
	}
	for key, f := range bad {
		c := base()
		f(&c)
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("%s: %v", key, err)
		}
	}
	c := base()
	c.TLSMode, c.TLSDomains, c.HTTPListen = "acme", []string{"openarcanum.example"}, ":80"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.ACMECache() != "/d/acme" || c.KeyFile() != "/d/storage.key" {
		t.Fatal(c.ACMECache(), c.KeyFile())
	}
}
