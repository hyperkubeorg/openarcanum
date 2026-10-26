package site

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hyperkubeorg/openarcanum/internal/httpx"
)

// Admin sign-in (docs/operations.md "Website"):
//
//   - Accounts exist only through `site admin add` on the server; there is
//     no sign-up.
//   - The session cookie is 256 random bits, Secure, HttpOnly,
//     SameSite=Strict, Path=/admin, and lives server-side (only its SHA-256
//     is stored) with an absolute lifetime (session_ttl) and an idle
//     timeout (session_idle). Signing in always makes a new session and
//     ends the one the browser had.
//   - Every state-changing admin form carries the session's CSRF token,
//     checked in constant time, on top of the same-origin check; the
//     sign-in form carries a double-submit token in its own cookie.
//   - Failed sign-ins are limited per IPv4 address or IPv6 /64 (in memory)
//     and per account (in the database: locked after 5 failures in a row,
//     1 minute doubling to an hour).
const (
	sessionCookie = "site_admin"
	loginCookie   = "site_login"
)

type adminCtxKey struct{}

// adminCtx is the signed-in admin of a request.
type adminCtx struct {
	admin *Admin
	csrf  string
	token []byte // the session's token hash
}

func adminFrom(r *http.Request) *adminCtx {
	a, _ := r.Context().Value(adminCtxKey{}).(*adminCtx)
	return a
}

func (s *Server) cookie(name, value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: name, Value: value, Path: "/admin", MaxAge: maxAge, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode}
}

// session returns the request's valid session, or nil.
func (s *Server) session(r *http.Request) (*adminCtx, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || len(c.Value) != 43 {
		return nil, nil
	}
	th := tokenHash(c.Value)
	ss, err := s.store.sessionByHash(r.Context(), th)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	now := s.now()
	if !now.Before(ss.ExpiresAt) || now.Sub(ss.SeenAt) >= s.cfg.SessionIdle.Duration {
		_ = s.store.deleteSession(r.Context(), th)
		return nil, nil
	}
	a, err := s.store.AdminByID(r.Context(), ss.AdminID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if now.Sub(ss.SeenAt) >= time.Minute {
		if err := s.store.touchSession(r.Context(), th); err != nil {
			return nil, err
		}
	}
	return &adminCtx{admin: a, csrf: hex.EncodeToString(ss.CSRF), token: th}, nil
}

// adminGate makes every path under /admin, whatever the router does with
// it, need a valid session, except the sign-in page. Without one, the
// browser is sent to sign in. State-changing requests also need the
// session's CSRF token.
func (s *Server) adminGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isAdminPath(r) {
			next.ServeHTTP(w, r)
			return
		}
		a, err := s.session(r)
		if err != nil {
			s.internalError(w, r, "reading a session", err)
			return
		}
		if r.URL.Path == "/admin/login" {
			if a != nil && r.Method == http.MethodGet {
				http.Redirect(w, r, "/admin", http.StatusSeeOther)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if a == nil {
			http.SetCookie(w, s.cookie(sessionCookie, "", -1))
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
			if err := r.ParseForm(); err != nil {
				s.renderError(w, r, http.StatusRequestEntityTooLarge, "That form is too large.")
				return
			}
			if subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(a.csrf)) != 1 {
				s.renderError(w, r, http.StatusForbidden, "The form expired or didn't come from this site. Go back, reload the page and try again.")
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), adminCtxKey{}, a)))
	})
}

// ---------------------------------------------------------------- sign in and out

type loginPage struct {
	Token string
	Name  string
	Error string
}

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	s.renderLogin(w, r, http.StatusOK, "", "")
}

func (s *Server) renderLogin(w http.ResponseWriter, r *http.Request, code int, name, msg string) {
	tok := newToken()
	http.SetCookie(w, s.cookie(loginCookie, tok, 3600))
	s.render(w, r, code, "login.html", page{Title: "Sign in", Data: loginPage{Token: tok, Name: name, Error: msg}})
}

// beginSignIn claims the one in-flight sign-in an account name may have.
func (s *Server) beginSignIn(name string) bool {
	k := strings.ToLower(name)
	s.signMu.Lock()
	defer s.signMu.Unlock()
	if s.signingIn[k] {
		return false
	}
	s.signingIn[k] = true
	return true
}

func (s *Server) endSignIn(name string) {
	s.signMu.Lock()
	delete(s.signingIn, strings.ToLower(name))
	s.signMu.Unlock()
}

const signInFailed = "Sign-in failed. Check the name, passphrase and code. After repeated failures, sign-in is paused for a while."

func (s *Server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		s.renderLogin(w, r, http.StatusBadRequest, "", signInFailed)
		return
	}
	c, err := r.Cookie(loginCookie)
	if err != nil || c.Value == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(r.PostForm.Get("token"))) != 1 {
		s.renderLogin(w, r, http.StatusForbidden, "", "The sign-in form expired. Please try again.")
		return
	}
	name := strings.TrimSpace(r.PostForm.Get("name"))
	if len(name) > 64 || !utf8.ValidString(name) {
		name = ""
	}
	// Every attempt counts against the network before any work is done, so
	// concurrent requests can't all pass the check (take is atomic).
	nk := httpx.NetworkKey(r)
	if !s.loginsPerIP.take(nk) {
		s.renderLogin(w, r, http.StatusTooManyRequests, name, signInFailed)
		return
	}
	// A bounded number of sign-ins wait for a hash at all ...
	select {
	case s.loginQueue <- struct{}{}:
		defer func() { <-s.loginQueue }()
	default:
		s.renderLogin(w, r, http.StatusServiceUnavailable, name, "Sign-in is busy. Try again in a moment.")
		return
	}
	// ... and one at a time per account, so each sees the lockout the one
	// before it left (no batch of guesses inside one lockout window).
	if !s.beginSignIn(name) {
		s.renderLogin(w, r, http.StatusTooManyRequests, name, signInFailed)
		return
	}
	defer s.endSignIn(name)
	a, err := s.store.authenticate(r.Context(), name, r.PostForm.Get("passphrase"), r.PostForm.Get("code"))
	if errors.Is(err, errSignIn) {
		s.log.Info("admin sign-in failed")
		s.renderLogin(w, r, http.StatusUnauthorized, name, signInFailed)
		return
	}
	if err != nil {
		s.internalError(w, r, "signing in", err)
		return
	}
	// A new session every time (no fixation); the old one ends.
	if old, err := r.Cookie(sessionCookie); err == nil {
		_ = s.store.deleteSession(r.Context(), tokenHash(old.Value))
	}
	tok := newToken()
	if err := s.store.createSession(r.Context(), tokenHash(tok), a.ID, randomBytes(32), s.cfg.SessionTTL.Duration); err != nil {
		s.internalError(w, r, "creating a session", err)
		return
	}
	s.log.Info("admin signed in", "admin", a.Name)
	http.SetCookie(w, s.cookie(loginCookie, "", -1))
	http.SetCookie(w, s.cookie(sessionCookie, tok, int(s.cfg.SessionTTL.Seconds())))
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if a := adminFrom(r); a != nil {
		if err := s.store.deleteSession(r.Context(), a.token); err != nil {
			s.internalError(w, r, "signing out", err)
			return
		}
	}
	http.SetCookie(w, s.cookie(sessionCookie, "", -1))
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

// ---------------------------------------------------------------- overview

type adminHomePage struct {
	Counts map[string]map[string]int
	Open   map[string]int
}

func (s *Server) adminHome(w http.ResponseWriter, r *http.Request) {
	counts, err := s.store.CountReports(r.Context())
	if err != nil {
		s.internalError(w, r, "counting reports", err)
		return
	}
	for _, k := range Kinds {
		if counts[k] == nil {
			counts[k] = map[string]int{}
		}
	}
	open := map[string]int{}
	for k, m := range counts {
		open[k] = m["new"] + m["triaged"] + m["in-progress"]
	}
	s.render(w, r, http.StatusOK, "admin.html", page{Title: "Admin", Section: "admin", Data: adminHomePage{Counts: counts, Open: open}})
}

// ---------------------------------------------------------------- announcements

func (s *Server) adminAnnouncements(w http.ResponseWriter, r *http.Request) {
	anns, err := s.store.Announcements(r.Context(), true, 500)
	if err != nil {
		s.internalError(w, r, "listing announcements", err)
		return
	}
	s.render(w, r, http.StatusOK, "admin_announcements.html", page{Title: "Announcements", Section: "announcements", Data: struct {
		List  []Announcement
		Saved string
	}{anns, r.URL.Query().Get("done")}})
}

type annPage struct {
	Ann    Announcement
	Done   string
	Errors map[string]string
}

const (
	maxAnnTitle = 200
	maxAnnBody  = 50_000
)

func (s *Server) adminAnnouncementForm(w http.ResponseWriter, r *http.Request) {
	p := annPage{Done: r.URL.Query().Get("done")}
	if id := pathID(r); id != 0 {
		a, err := s.store.Announcement(r.Context(), id)
		if errors.Is(err, ErrNotFound) {
			s.renderError(w, r, http.StatusNotFound, "There's no such announcement.")
			return
		}
		if err != nil {
			s.internalError(w, r, "reading an announcement", err)
			return
		}
		p.Ann = *a
	}
	s.render(w, r, http.StatusOK, "admin_announcement.html", page{Title: "Announcement", Section: "announcements", Data: p})
}

func (s *Server) adminAnnouncementSave(w http.ResponseWriter, r *http.Request) {
	a := Announcement{ID: pathID(r)}
	errs := map[string]string{}
	var ok bool
	if a.Title, ok = cleanText(r.PostForm.Get("title"), true); !ok || a.Title == "" || utf8.RuneCountInString(a.Title) > maxAnnTitle {
		errs["title"] = "A title of up to 200 characters is required."
	}
	if a.Body, ok = cleanText(r.PostForm.Get("body"), false); !ok || a.Body == "" || utf8.RuneCountInString(a.Body) > maxAnnBody {
		errs["body"] = "A text of up to 50,000 characters is required."
	}
	a.Pinned = r.PostForm.Get("pinned") == "on"
	a.Published = r.PostForm.Get("status") == "published"
	if len(errs) > 0 {
		s.render(w, r, http.StatusUnprocessableEntity, "admin_announcement.html", page{Title: "Announcement", Section: "announcements", Data: annPage{Ann: a, Errors: errs}})
		return
	}
	id, err := s.store.SaveAnnouncement(r.Context(), &a, adminFrom(r).admin.ID)
	if errors.Is(err, ErrNotFound) {
		s.renderError(w, r, http.StatusNotFound, "There's no such announcement.")
		return
	}
	if err != nil {
		s.internalError(w, r, "saving an announcement", err)
		return
	}
	http.Redirect(w, r, "/admin/announcements/"+strconv.FormatInt(id, 10)+"?done=saved", http.StatusSeeOther)
}

func (s *Server) adminAnnouncementDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteAnnouncement(r.Context(), pathID(r)); err != nil {
		s.internalError(w, r, "deleting an announcement", err)
		return
	}
	http.Redirect(w, r, "/admin/announcements?done=deleted", http.StatusSeeOther)
}

// ---------------------------------------------------------------- reports

type reportsPage struct {
	List     []Report
	Filter   ReportFilter
	Page     int
	Prev     string
	Next     string
	Admins   []Admin
	Done     string
	Filtered bool
}

const reportsPerPage = 50

func (s *Server) adminReports(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := ReportFilter{Kind: q.Get("kind"), Status: q.Get("status"), Severity: q.Get("severity"), Limit: reportsPerPage + 1}
	if !oneOf(f.Kind, Kinds) {
		f.Kind = ""
	}
	if !oneOf(f.Status, Statuses) {
		f.Status = ""
	}
	if !oneOf(f.Severity, Severities) {
		f.Severity = ""
	}
	pg, _ := strconv.Atoi(q.Get("page"))
	pg = max(1, min(pg, 10_000))
	f.Offset = (pg - 1) * reportsPerPage
	list, err := s.store.Reports(r.Context(), f)
	if err != nil {
		s.internalError(w, r, "listing reports", err)
		return
	}
	p := reportsPage{Filter: f, Page: pg, Done: q.Get("done"), Filtered: f.Kind != "" || f.Status != "" || f.Severity != ""}
	link := func(n int) string {
		v := url.Values{}
		for k, val := range map[string]string{"kind": f.Kind, "status": f.Status, "severity": f.Severity} {
			if val != "" {
				v.Set(k, val)
			}
		}
		v.Set("page", strconv.Itoa(n))
		return "/admin/reports?" + v.Encode()
	}
	if len(list) > reportsPerPage {
		list = list[:reportsPerPage]
		p.Next = link(pg + 1)
	}
	if pg > 1 {
		p.Prev = link(pg - 1)
	}
	p.List = list
	s.render(w, r, http.StatusOK, "admin_reports.html", page{Title: "Reports", Section: "reports", Data: p})
}

type reportDetail struct {
	Report *Report
	Notes  []Note
	Admins []Admin
	Done   string
	Error  string
}

func (s *Server) adminReport(w http.ResponseWriter, r *http.Request) {
	s.renderReport(w, r, http.StatusOK, pathID(r), r.URL.Query().Get("done"), "")
}

func (s *Server) renderReport(w http.ResponseWriter, r *http.Request, code int, id int64, done, msg string) {
	rep, err := s.store.Report(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		s.renderError(w, r, http.StatusNotFound, "There's no such report.")
		return
	}
	if err != nil {
		s.internalError(w, r, "reading a report", err)
		return
	}
	notes, err := s.store.Notes(r.Context(), id)
	if err != nil {
		s.internalError(w, r, "reading notes", err)
		return
	}
	admins, err := s.store.Admins(r.Context())
	if err != nil {
		s.internalError(w, r, "listing admins", err)
		return
	}
	s.render(w, r, code, "admin_report.html", page{Title: "Report " + rep.Ref, Section: "reports",
		Data: reportDetail{Report: rep, Notes: notes, Admins: admins, Done: done, Error: msg}})
}

const maxNote = 20_000

func (s *Server) adminReportTriage(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	t := Triage{Status: r.PostForm.Get("status"), Severity: r.PostForm.Get("severity")}
	t.Assignee, _ = strconv.ParseInt(r.PostForm.Get("assignee"), 10, 64)
	note, ok := cleanText(r.PostForm.Get("note"), false)
	if !ok || utf8.RuneCountInString(note) > maxNote {
		s.renderReport(w, r, http.StatusUnprocessableEntity, id, "", "The note is too long or contains characters that aren't allowed.")
		return
	}
	t.Note = note
	err := s.store.TriageReport(r.Context(), id, adminFrom(r).admin.ID, t)
	switch {
	case errors.Is(err, ErrNotFound):
		s.renderError(w, r, http.StatusNotFound, "There's no such report.")
	case errors.Is(err, errTriageValue):
		s.renderReport(w, r, http.StatusUnprocessableEntity, id, "", "Choose a status, severity and assignee from the lists.")
	case err != nil:
		s.internalError(w, r, "triaging a report", err)
	default:
		http.Redirect(w, r, "/admin/reports/"+strconv.FormatInt(id, 10)+"?done=saved", http.StatusSeeOther)
	}
}

func (s *Server) adminReportDelete(w http.ResponseWriter, r *http.Request) {
	if r.PostForm.Get("confirm") != "on" {
		s.renderReport(w, r, http.StatusUnprocessableEntity, pathID(r), "", "Tick the box to confirm deleting this report.")
		return
	}
	if err := s.store.DeleteReport(r.Context(), pathID(r)); err != nil {
		s.internalError(w, r, "deleting a report", err)
		return
	}
	http.Redirect(w, r, "/admin/reports?done=deleted", http.StatusSeeOther)
}
