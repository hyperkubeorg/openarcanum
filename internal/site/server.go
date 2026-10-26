package site

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"

	"github.com/hyperkubeorg/openarcanum/internal/httpx"
)

// ProductName is the name the site shows.
const ProductName = "OpenArcanum"

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Server is the website.
type Server struct {
	cfg   Config
	store *Store
	log   *slog.Logger
	now   func() time.Time

	pages map[string]*template.Template

	pow          *powGate
	reportsPerIP *limiter
	reportsAll   *limiter
	loginsPerIP  *limiter
	loginQueue   chan struct{}
	signMu       sync.Mutex
	signingIn    map[string]bool
	hosts        map[string]bool
	tls          bool
}

// Sign-in limits, on top of the per-account lockout: attempts per IPv4
// address or IPv6 /64 in loginWindow, and sign-ins waiting for a hash at
// once (others are refused at once instead of queueing without bound).
const (
	loginAttemptsPerIP = 10
	loginWindow        = 15 * time.Minute
	loginQueueSize     = 16
)

// New builds the site on an open store.
func New(cfg Config, st *Store, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Server{
		cfg: cfg, store: st, log: log, now: time.Now,
		reportsPerIP: newLimiter(cfg.ReportsPerHour, time.Hour),
		reportsAll:   newLimiter(cfg.ReportsPerDay, 24*time.Hour),
		loginsPerIP:  newLimiter(loginAttemptsPerIP, loginWindow),
		loginQueue:   make(chan struct{}, loginQueueSize),
		signingIn:    map[string]bool{},
		tls:          cfg.Enabled(),
	}
	// The challenge key is derived from the storage key, so forms shown
	// before a restart still submit after it.
	m := hmac.New(sha256.New, st.key)
	m.Write([]byte("openarcanum/site-pow-key/v1"))
	s.pow = newPoWGate(m.Sum(nil), cfg.ReportPoWBits)
	if len(cfg.TLSDomains) > 0 {
		s.hosts = map[string]bool{}
		for _, d := range cfg.TLSDomains {
			s.hosts[strings.Trim(strings.ToLower(strings.TrimSuffix(d, ".")), "[]")] = true
		}
	}
	if err := s.parseTemplates(); err != nil {
		return nil, err
	}
	return s, nil
}

// Run purges expired sessions until ctx ends.
func (s *Server) Run(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		if err := s.store.PurgeSessions(ctx, s.cfg.SessionIdle.Duration); err != nil && ctx.Err() == nil {
			s.log.Error("purging sessions", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ---------------------------------------------------------------- handler

// Handler is the site's whole HTTP handler: the routes behind the admin
// gate, the same-origin check, the security headers and httpx's access log
// and compression. accessLog may be nil (no access log).
func (s *Server) Handler(accessLog io.Writer) http.Handler {
	r := httpx.NewRouter()
	get, post := []string{http.MethodGet, http.MethodHead}, []string{http.MethodPost}

	r.HandleFunc("/", s.home).Methods(get...)
	r.HandleFunc("/announcements/{id:[0-9]{1,18}}", s.announcement).Methods(get...)
	r.HandleFunc("/report/{kind:bug|security}", s.reportForm).Methods(get...)
	r.HandleFunc("/report/{kind:bug|security}", s.reportSubmit).Methods(post...)
	r.HandleFunc("/security", s.securityPolicy).Methods(get...)
	r.HandleFunc("/.well-known/security.txt", s.securityTxt).Methods(get...)
	r.HandleFunc("/robots.txt", robots).Methods(get...)
	r.HandleFunc("/theme", s.setTheme).Methods(post...)
	files := http.StripPrefix("/static/", http.FileServer(http.FS(mustSub(staticFS, "static"))))
	static := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") { // no directory listings
			s.renderError(w, r, http.StatusNotFound, "There's nothing here.")
			return
		}
		files.ServeHTTP(w, r)
	})
	r.PathPrefix("/static/").Handler(cacheFor(static, 24*time.Hour)).Methods(get...)
	r.Handle("/favicon.svg", cacheFor(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, staticFS, "static/brand.svg")
	}), 24*time.Hour)).Methods(get...)

	r.HandleFunc("/admin/login", s.loginForm).Methods(get...)
	r.HandleFunc("/admin/login", s.loginSubmit).Methods(post...)
	r.HandleFunc("/admin/logout", s.logout).Methods(post...)
	r.HandleFunc("/admin", s.adminHome).Methods(get...)
	r.HandleFunc("/admin/announcements", s.adminAnnouncements).Methods(get...)
	r.HandleFunc("/admin/announcements/new", s.adminAnnouncementForm).Methods(get...)
	r.HandleFunc("/admin/announcements/new", s.adminAnnouncementSave).Methods(post...)
	r.HandleFunc("/admin/announcements/{id:[0-9]{1,18}}", s.adminAnnouncementForm).Methods(get...)
	r.HandleFunc("/admin/announcements/{id:[0-9]{1,18}}", s.adminAnnouncementSave).Methods(post...)
	r.HandleFunc("/admin/announcements/{id:[0-9]{1,18}}/delete", s.adminAnnouncementDelete).Methods(post...)
	r.HandleFunc("/admin/reports", s.adminReports).Methods(get...)
	r.HandleFunc("/admin/reports/{id:[0-9]{1,18}}", s.adminReport).Methods(get...)
	r.HandleFunc("/admin/reports/{id:[0-9]{1,18}}", s.adminReportTriage).Methods(post...)
	r.HandleFunc("/admin/reports/{id:[0-9]{1,18}}/delete", s.adminReportDelete).Methods(post...)

	r.NotFoundHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.renderError(w, r, http.StatusNotFound, "There's nothing here.")
	})
	r.MethodNotAllowedHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.renderError(w, r, http.StatusMethodNotAllowed, "That doesn't work here.")
	})

	h := httpx.Chain(r, s.securityHeaders, s.checkHost, s.sameOrigin, s.adminGate)
	return httpx.Wrap(h, httpx.Options{
		AccessLog:          accessLog,
		LogClientIPs:       s.cfg.LogClientIPs,
		TrustedProxyHeader: s.cfg.TrustedProxyHeader,
		// Admin pages carry CSRF tokens next to text reporters wrote.
		NoCompress: isAdminPath,
	})
}

func mustSub(f fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic(err)
	}
	return sub
}

func cacheFor(h http.Handler, d time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(d.Seconds())))
		h.ServeHTTP(w, r)
	})
}

func isAdminPath(r *http.Request) bool {
	return r.URL.Path == "/admin" || strings.HasPrefix(r.URL.Path, "/admin/")
}

// ContentSecurityPolicy is sent with every response: nothing loads from
// anywhere but the site, no inline script or style, no framing.
const ContentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; " +
	"connect-src 'self'; font-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// PermissionsPolicy turns off the browser features the site never uses.
const PermissionsPolicy = "accelerometer=(), camera=(), geolocation=(), gyroscope=(), magnetometer=(), microphone=(), " +
	"payment=(), usb=(), interest-cohort=(), browsing-topics=()"

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", ContentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", PermissionsPolicy)
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		if s.tls {
			h.Set("Strict-Transport-Security", "max-age=63072000")
		}
		if isAdminPath(r) || r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// checkHost answers only for the site's own names (tls_domains) when they
// are configured, so a rebound DNS name can't reach the admin portal.
func (s *Server) checkHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.hosts != nil {
			host := r.Host
			if h, _, err := net.SplitHostPort(host); err == nil {
				host = h
			}
			if !s.hosts[strings.Trim(strings.ToLower(strings.TrimSuffix(host, ".")), "[]")] {
				http.Error(w, "unknown host", http.StatusMisdirectedRequest)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin refuses state-changing requests a browser says came from
// another site: Sec-Fetch-Site when the browser sends it, else an Origin
// (other than "null", which no-referrer pages send) naming another host.
// The CSRF token is checked on top of this.
func (s *Server) sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !s.isSameOrigin(r) {
			s.renderError(w, r, http.StatusForbidden, "That request came from another site, so it was refused.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) isSameOrigin(r *http.Request) bool {
	if sfs := r.Header.Get("Sec-Fetch-Site"); sfs != "" {
		return sfs == "same-origin"
	}
	if o := r.Header.Get("Origin"); o != "" && o != "null" {
		u, err := url.Parse(o)
		return err == nil && strings.EqualFold(u.Host, r.Host)
	}
	return true
}

func robots(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "User-agent: *\nDisallow: /admin\n")
}

// ---------------------------------------------------------------- templates

// page is what every template gets.
type page struct {
	Title    string
	Theme    string
	Section  string
	Admin    *Admin
	CSRF     string
	Path     string
	Product  string
	Download string
	Source   string
	Data     any
}

var funcs = template.FuncMap{
	"md":   Markdown,
	"date": func(t time.Time) string { return t.UTC().Format("2 January 2006") },
	"datetime": func(t time.Time) string {
		if t.IsZero() {
			return "never"
		}
		return t.UTC().Format("2006-01-02 15:04 UTC")
	},
	"iso":        func(t time.Time) string { return t.UTC().Format(time.RFC3339) },
	"label":      label,
	"kindLabel":  func(k string) string { return map[string]string{"bug": "Bug", "security": "Security"}[k] },
	"severities": func() []string { return Severities },
	"statuses":   func() []string { return Statuses },
	"kinds":      func() []string { return Kinds },
	"userIDLen":  func() int { return UserIDLen },
}

// label is how a status or severity value is shown.
func label(v string) string {
	switch v {
	case "":
		return "Unrated"
	case "in-progress":
		return "In progress"
	case "wont-fix":
		return "Won't fix"
	}
	return strings.ToUpper(v[:1]) + v[1:]
}

func (s *Server) parseTemplates() error {
	names, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return err
	}
	s.pages = map[string]*template.Template{}
	for _, n := range names {
		base := path.Base(n)
		if base == "layout.html" {
			continue
		}
		t, err := template.New(base).Funcs(funcs).ParseFS(templateFS, "templates/layout.html", n)
		if err != nil {
			return fmt.Errorf("template %s: %w", base, err)
		}
		s.pages[base] = t
	}
	return nil
}

func (s *Server) theme(r *http.Request) string {
	if c, err := r.Cookie("theme"); err == nil && c.Value == "light" {
		return "light"
	}
	return "dark"
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, code int, name string, p page) {
	t := s.pages[name]
	if t == nil {
		panic("no template " + name)
	}
	p.Theme = s.theme(r)
	p.Path = r.URL.Path
	p.Product = ProductName
	p.Download, p.Source = s.cfg.DownloadURL, s.cfg.SourceURL
	if a := adminFrom(r); a != nil {
		p.Admin = a.admin
		p.CSRF = a.csrf
	}
	var buf strings.Builder
	if err := t.ExecuteTemplate(&buf, "layout", p); err != nil {
		s.log.Error("rendering a page", "template", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, buf.String())
	}
}

func (s *Server) renderError(w http.ResponseWriter, r *http.Request, code int, msg string) {
	s.render(w, r, code, "error.html", page{Title: http.StatusText(code), Data: msg})
}

func (s *Server) internalError(w http.ResponseWriter, r *http.Request, what string, err error) {
	s.log.Error(what, "err", err)
	s.renderError(w, r, http.StatusInternalServerError, "Something went wrong on our side. Please try again later.")
}

// ---------------------------------------------------------------- public pages

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	anns, err := s.store.Announcements(r.Context(), false, 50)
	if err != nil {
		s.internalError(w, r, "listing announcements", err)
		return
	}
	s.render(w, r, http.StatusOK, "home.html", page{Section: "home", Data: anns})
}

func pathID(r *http.Request) int64 {
	var id int64
	_, _ = fmt.Sscan(mux.Vars(r)["id"], &id)
	return id
}

func (s *Server) announcement(w http.ResponseWriter, r *http.Request) {
	a, err := s.store.Announcement(r.Context(), pathID(r))
	if errors.Is(err, ErrNotFound) || (err == nil && !a.Published) {
		s.renderError(w, r, http.StatusNotFound, "There's no such announcement.")
		return
	}
	if err != nil {
		s.internalError(w, r, "reading an announcement", err)
		return
	}
	s.render(w, r, http.StatusOK, "announcement.html", page{Title: a.Title, Data: a})
}

func (s *Server) securityPolicy(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "security.html", page{Title: "Security disclosure policy", Section: "security"})
}

// securityTxt is RFC 9116's security.txt, pointing at the security form.
func (s *Server) securityTxt(w http.ResponseWriter, r *http.Request) {
	base := "https://" + r.Host
	if h := s.cfg.PublicHost(); h != "" {
		base = "https://" + h
	} else {
		// Built from the request's Host: no shared cache may keep it.
		w.Header().Set("Cache-Control", "no-store")
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = fmt.Fprintf(w, "Contact: %s/report/security\nPolicy: %s/security\nPreferred-Languages: en\nCanonical: %s/.well-known/security.txt\nExpires: %s\n",
		base, base, base, s.now().UTC().Add(180*24*time.Hour).Truncate(24*time.Hour).Format(time.RFC3339))
}

func (s *Server) setTheme(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	theme := "light"
	if r.PostFormValue("theme") == "dark" {
		theme = "dark"
	}
	http.SetCookie(w, &http.Cookie{Name: "theme", Value: theme, Path: "/", MaxAge: 365 * 24 * 3600, Secure: s.tls, SameSite: http.SameSiteLaxMode})
	back := r.PostFormValue("back")
	if !localPath(back) {
		back = "/"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// localPath accepts only a path on this site: it starts with one "/", has
// no scheme or host however a browser reads it (browsers drop tabs and
// newlines and treat "\" as "/"), and no control characters at all.
func localPath(p string) bool {
	if len(p) > 2048 || !strings.HasPrefix(p, "/") {
		return false
	}
	for _, c := range p {
		if c < 0x20 || c == 0x7f || c == '\\' {
			return false
		}
	}
	u, err := url.Parse(p)
	return err == nil && u.Scheme == "" && u.Host == "" && u.User == nil && !strings.HasPrefix(p, "//")
}

// ---------------------------------------------------------------- reports

type reportPage struct {
	Form             *reportForm
	Challenge        string
	Bits             int
	PoWContext       string
	ContactCommunity string
	Ref              string
}

func (s *Server) reportPage(f *reportForm) reportPage {
	return reportPage{Form: f, Challenge: s.pow.challenge(), Bits: s.cfg.ReportPoWBits, PoWContext: powContext,
		ContactCommunity: s.cfg.ContactCommunity}
}

func reportTitle(kind string) string {
	if kind == "security" {
		return "Report a security issue"
	}
	return "Report a bug"
}

func (s *Server) reportForm(w http.ResponseWriter, r *http.Request) {
	kind := mux.Vars(r)["kind"]
	f := &reportForm{Kind: kind, Community: s.cfg.ContactCommunity}
	s.render(w, r, http.StatusOK, "report.html", page{Title: reportTitle(kind), Section: "report-" + kind, Data: s.reportPage(f)})
}

func (s *Server) reportSubmit(w http.ResponseWriter, r *http.Request) {
	kind := mux.Vars(r)["kind"]
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusRequestEntityTooLarge, "That report is too large. Please shorten it.")
		return
	}
	nk := httpx.NetworkKey(r)
	if s.reportsPerIP.full(nk) {
		s.renderError(w, r, http.StatusTooManyRequests, "You've sent several reports in the last hour. Please wait a while before sending another.")
		return
	}
	f := parseReport(kind, r.PostForm)
	if err := s.pow.check(r.PostForm.Get("pow_challenge"), r.PostForm.Get("pow_counter")); err != nil {
		f.fail("form", err.Error())
	}
	if len(f.Errors) > 0 {
		s.render(w, r, http.StatusUnprocessableEntity, "report.html", page{Title: reportTitle(kind), Section: "report-" + kind, Data: s.reportPage(f)})
		return
	}
	// Counted atomically here (concurrent posts can't all pass the check
	// above); invalid forms don't use up the allowance.
	if !s.reportsPerIP.take(nk) {
		s.renderError(w, r, http.StatusTooManyRequests, "You've sent several reports in the last hour. Please wait a while before sending another.")
		return
	}
	sent := reportPage{Form: f}
	// The honeypot: a field hidden from people. A bot that fills it is
	// told its report arrived; nothing is stored.
	if r.PostForm.Get("website") != "" {
		sent.Ref = refEncoding.EncodeToString(randomBytes(5))
		s.render(w, r, http.StatusOK, "report_sent.html", page{Title: "Report received", Data: sent})
		return
	}
	if !s.reportsAll.take("all") {
		// A flood, or reports_per_day too low: the admins should know.
		s.log.Warn("reports_per_day reached: reports are refused until the oldest of today's fall out of the window")
		s.renderError(w, r, http.StatusServiceUnavailable, "We're receiving too many reports right now. Please try again tomorrow.")
		return
	}
	ref, err := s.store.AddReport(r.Context(), kind, f.content())
	if err != nil {
		s.internalError(w, r, "storing a report", err)
		return
	}
	s.log.Info("report received", "kind", kind)
	sent.Ref = ref
	s.render(w, r, http.StatusOK, "report_sent.html", page{Title: "Report received", Data: sent})
}
