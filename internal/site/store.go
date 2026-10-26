package site

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/atrest"
	"github.com/hyperkubeorg/openarcanum/internal/store"
)

// atRestContext separates the site's sealed values from every other use of
// the at-rest construction.
const atRestContext = "openarcanum/site-at-rest/v1"

// keyCheck is sealed under the storage key in meta; it tells a wrong key
// apart from damaged rows.
const keyCheck = "site storage key check v1"

// ErrWrongKey means storage_key_file doesn't open this database.
var ErrWrongKey = errors.New("the storage key does not match this database (check storage_key_file)")

// ErrNotFound is a missing row.
var ErrNotFound = errors.New("not found")

var migrations = []store.Migration{{SQL: `
CREATE TABLE meta (k TEXT PRIMARY KEY, v BLOB NOT NULL);
CREATE TABLE admins (
	id INTEGER PRIMARY KEY,
	name TEXT NOT NULL UNIQUE COLLATE NOCASE,
	pass_hash TEXT NOT NULL,
	totp_enc BLOB,
	totp_last INTEGER NOT NULL DEFAULT 0,
	failures INTEGER NOT NULL DEFAULT 0,
	locked_until INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	last_login INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE sessions (
	token_hash BLOB PRIMARY KEY,
	admin_id INTEGER NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
	csrf BLOB NOT NULL,
	created_at INTEGER NOT NULL,
	seen_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL
);
CREATE INDEX sessions_admin ON sessions(admin_id);
CREATE TABLE announcements (
	id INTEGER PRIMARY KEY,
	title TEXT NOT NULL,
	body TEXT NOT NULL,
	pinned INTEGER NOT NULL DEFAULT 0,
	published INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	published_at INTEGER NOT NULL DEFAULT 0,
	author INTEGER REFERENCES admins(id) ON DELETE SET NULL
);
CREATE TABLE reports (
	id INTEGER PRIMARY KEY,
	ref TEXT NOT NULL UNIQUE,
	kind TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'new',
	severity TEXT NOT NULL DEFAULT '',
	assignee INTEGER REFERENCES admins(id) ON DELETE SET NULL,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	content BLOB NOT NULL
);
CREATE INDEX reports_list ON reports(kind, status, created_at);
CREATE TABLE report_notes (
	id INTEGER PRIMARY KEY,
	report_id INTEGER NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
	author INTEGER REFERENCES admins(id) ON DELETE SET NULL,
	created_at INTEGER NOT NULL,
	body BLOB NOT NULL
);
CREATE INDEX report_notes_report ON report_notes(report_id, created_at);
`}}

// Store is the site's database: admins and their sessions, announcements,
// reports. Report contents, notes and second-factor secrets are sealed
// with the storage key; announcements are public and stored plain.
type Store struct {
	db  *sql.DB
	key []byte
	now func() time.Time
}

// DBFile is the database's name inside the data directory.
const DBFile = "site.db"

// OpenStore opens (creating and migrating) DATA/site.db with the key at
// keyFile (created on first use when create is set).
func OpenStore(ctx context.Context, dataDir, keyFile string, create bool) (*Store, error) {
	if err := privateDir(dataDir); err != nil {
		return nil, fmt.Errorf("data_dir: %w", err)
	}
	var key []byte
	var err error
	if create {
		key, err = atrest.LoadOrCreateKeyFile(keyFile)
	} else {
		key, err = atrest.ReadKeyFile(keyFile)
	}
	if err != nil {
		return nil, fmt.Errorf("storage_key_file: %w", err)
	}
	path := filepath.Join(dataDir, DBFile)
	if !create {
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("no site database in %s (start the site once first): %w", dataDir, err)
		}
	}
	db, err := store.Open(path, "secure_delete(1)")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
		db.Close()
		return nil, err
	}
	s := &Store{db: db, key: key, now: time.Now}
	if err := s.init(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) init(ctx context.Context) error {
	if _, err := store.Apply(ctx, s.db, migrations); err != nil {
		return err
	}
	var check []byte
	err := s.db.QueryRowContext(ctx, `SELECT v FROM meta WHERE k = 'key_check'`).Scan(&check)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = s.db.ExecContext(ctx, `INSERT INTO meta (k, v) VALUES ('key_check', ?) ON CONFLICT (k) DO NOTHING`,
			s.seal("meta", "v", "key_check", []byte(keyCheck)))
		return err
	}
	if err != nil {
		return err
	}
	if pt, err := s.open("meta", "v", "key_check", check); err != nil || string(pt) != keyCheck {
		return ErrWrongKey
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

// Ping checks the database answers (the health check).
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) seal(table, column, row string, pt []byte) []byte {
	return atrest.Seal(s.key, atRestContext, table, column, row, pt)
}

func (s *Store) open(table, column, row string, ct []byte) ([]byte, error) {
	pt, err := atrest.Open(s.key, atRestContext, table, column, row, ct)
	if err != nil {
		return nil, fmt.Errorf("stored %s.%s for row %s failed to decrypt: the database was modified or damaged", table, column, row)
	}
	return pt, nil
}

func rowID(id int64) string { return strconv.FormatInt(id, 10) }

// privateDir makes dir if needed and makes it 0700.
func privateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	st, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if st.Mode().Perm() != 0o700 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("%s is open to other users (mode %o) and can't be made 0700: %w", dir, st.Mode().Perm(), err)
		}
	}
	return nil
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

func (s *Store) unix() int64 { return s.now().Unix() }

// ---------------------------------------------------------------- admins

// Admin is an admin account.
type Admin struct {
	ID          int64
	Name        string
	PassHash    string
	TOTP        bool
	Failures    int
	LockedUntil time.Time
	CreatedAt   time.Time
	LastLogin   time.Time
}

const adminCols = `id, name, pass_hash, totp_enc IS NOT NULL, failures, locked_until, created_at, last_login`

func scanAdmin(row interface{ Scan(...any) error }) (*Admin, error) {
	var a Admin
	var locked, created, last int64
	if err := row.Scan(&a.ID, &a.Name, &a.PassHash, &a.TOTP, &a.Failures, &locked, &created, &last); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	a.LockedUntil, a.CreatedAt = time.Unix(locked, 0), time.Unix(created, 0)
	if last > 0 {
		a.LastLogin = time.Unix(last, 0)
	}
	return &a, nil
}

// AdminByName finds an admin (names are case-insensitive).
func (s *Store) AdminByName(ctx context.Context, name string) (*Admin, error) {
	return scanAdmin(s.db.QueryRowContext(ctx, `SELECT `+adminCols+` FROM admins WHERE name = ?`, name))
}

// AdminByID finds an admin by id.
func (s *Store) AdminByID(ctx context.Context, id int64) (*Admin, error) {
	return scanAdmin(s.db.QueryRowContext(ctx, `SELECT `+adminCols+` FROM admins WHERE id = ?`, id))
}

// Admins lists every admin by name.
func (s *Store) Admins(ctx context.Context) ([]Admin, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+adminCols+` FROM admins ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Admin
	for rows.Next() {
		a, err := scanAdmin(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// ValidAdminName: 1-32 letters, digits, '.', '_' or '-'.
func ValidAdminName(name string) error {
	if len(name) < 1 || len(name) > 32 || strings.Trim(name, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-") != "" {
		return errors.New("an admin name is 1-32 letters, digits, '.', '_' or '-'")
	}
	return nil
}

// AddAdmin creates an admin with a passphrase hash (HashPassphrase).
func (s *Store) AddAdmin(ctx context.Context, name, passHash string) error {
	if err := ValidAdminName(name); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO admins (name, pass_hash, created_at) VALUES (?, ?, ?)`, name, passHash, s.unix())
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return fmt.Errorf("an admin named %q already exists", name)
	}
	return err
}

// SetPassphrase replaces an admin's passphrase hash, clears any lockout
// and ends their sessions.
func (s *Store) SetPassphrase(ctx context.Context, name, passHash string) error {
	return s.updateAdmin(ctx, name, `UPDATE admins SET pass_hash = ?, failures = 0, locked_until = 0 WHERE name = ?`, passHash, name)
}

// RemoveAdmin deletes an admin and their sessions.
func (s *Store) RemoveAdmin(ctx context.Context, name string) error {
	return s.updateAdmin(ctx, name, `DELETE FROM admins WHERE name = ?`, name)
}

// Unlock clears an admin's failed sign-ins and lockout.
func (s *Store) Unlock(ctx context.Context, name string) error {
	return s.updateAdmin(ctx, name, `UPDATE admins SET failures = 0, locked_until = 0 WHERE name = ?`, name)
}

// SetTOTP stores (or with nil removes) an admin's TOTP secret and ends
// their sessions.
func (s *Store) SetTOTP(ctx context.Context, name string, secret []byte) error {
	a, err := s.AdminByName(ctx, name)
	if err != nil {
		return err
	}
	var enc []byte
	if secret != nil {
		enc = s.seal("admins", "totp_enc", rowID(a.ID), secret)
	}
	return s.updateAdmin(ctx, name, `UPDATE admins SET totp_enc = ?, totp_last = 0 WHERE name = ?`, enc, name)
}

func (s *Store) updateAdmin(ctx context.Context, name, query string, args ...any) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM admins WHERE name = ?`, name).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no admin named %q", name)
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE admin_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return err
	}
	return tx.Commit()
}

// totpSecret returns an admin's TOTP secret and the last time step used,
// or nil when TOTP is off.
func (s *Store) totpSecret(ctx context.Context, id int64) ([]byte, int64, error) {
	var enc []byte
	var last int64
	if err := s.db.QueryRowContext(ctx, `SELECT totp_enc, totp_last FROM admins WHERE id = ?`, id).Scan(&enc, &last); err != nil {
		return nil, 0, err
	}
	if enc == nil {
		return nil, 0, nil
	}
	sec, err := s.open("admins", "totp_enc", rowID(id), enc)
	return sec, last, err
}

// useTOTPStep records step as used if it is newer than the last one, so a
// code can't be replayed; it reports whether it was.
func (s *Store) useTOTPStep(ctx context.Context, id, step int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE admins SET totp_last = ? WHERE id = ? AND totp_last < ?`, step, id, step)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// recordFailure counts a failed sign-in and returns the new lockout end.
func (s *Store) recordFailure(ctx context.Context, id int64, lockFor func(failures int) time.Duration) error {
	var failures int
	if err := s.db.QueryRowContext(ctx, `UPDATE admins SET failures = failures + 1 WHERE id = ? RETURNING failures`, id).Scan(&failures); err != nil {
		return err
	}
	if d := lockFor(failures); d > 0 {
		_, err := s.db.ExecContext(ctx, `UPDATE admins SET locked_until = ? WHERE id = ?`, s.now().Add(d).Unix(), id)
		return err
	}
	return nil
}

func (s *Store) recordLogin(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE admins SET failures = 0, locked_until = 0, last_login = ? WHERE id = ?`, s.unix(), id)
	return err
}

// ---------------------------------------------------------------- sessions

type session struct {
	AdminID   int64
	CSRF      []byte
	SeenAt    time.Time
	ExpiresAt time.Time
}

func (s *Store) createSession(ctx context.Context, tokenHash []byte, adminID int64, csrf []byte, ttl time.Duration) error {
	now := s.unix()
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions (token_hash, admin_id, csrf, created_at, seen_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
		tokenHash, adminID, csrf, now, now, now+int64(ttl/time.Second))
	return err
}

func (s *Store) sessionByHash(ctx context.Context, tokenHash []byte) (*session, error) {
	var ss session
	var seen, exp int64
	err := s.db.QueryRowContext(ctx, `SELECT admin_id, csrf, seen_at, expires_at FROM sessions WHERE token_hash = ?`, tokenHash).
		Scan(&ss.AdminID, &ss.CSRF, &seen, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	ss.SeenAt, ss.ExpiresAt = time.Unix(seen, 0), time.Unix(exp, 0)
	return &ss, nil
}

func (s *Store) touchSession(ctx context.Context, tokenHash []byte) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET seen_at = ? WHERE token_hash = ?`, s.unix(), tokenHash)
	return err
}

func (s *Store) deleteSession(ctx context.Context, tokenHash []byte) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}

// PurgeSessions deletes expired and idle sessions.
func (s *Store) PurgeSessions(ctx context.Context, idle time.Duration) error {
	now := s.now()
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ? OR seen_at <= ?`, now.Unix(), now.Add(-idle).Unix())
	return err
}

// ---------------------------------------------------------------- announcements

// Announcement is a post on the landing page. Body is Markdown.
type Announcement struct {
	ID          int64
	Title       string
	Body        string
	Pinned      bool
	Published   bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
	PublishedAt time.Time
	Author      string
}

const annCols = `a.id, a.title, a.body, a.pinned, a.published, a.created_at, a.updated_at, a.published_at, COALESCE(ad.name, '')`

func scanAnn(row interface{ Scan(...any) error }) (*Announcement, error) {
	var a Announcement
	var c, u, p int64
	if err := row.Scan(&a.ID, &a.Title, &a.Body, &a.Pinned, &a.Published, &c, &u, &p, &a.Author); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	a.CreatedAt, a.UpdatedAt = time.Unix(c, 0), time.Unix(u, 0)
	if p > 0 {
		a.PublishedAt = time.Unix(p, 0)
	}
	return &a, nil
}

// Announcements lists announcements, pinned first, then newest; only
// published ones unless all.
func (s *Store) Announcements(ctx context.Context, all bool, limit int) ([]Announcement, error) {
	q := `SELECT ` + annCols + ` FROM announcements a LEFT JOIN admins ad ON ad.id = a.author`
	if !all {
		q += ` WHERE a.published = 1`
	}
	q += ` ORDER BY a.pinned DESC, CASE WHEN a.published_at > 0 THEN a.published_at ELSE a.created_at END DESC, a.id DESC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Announcement
	for rows.Next() {
		a, err := scanAnn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// Announcement finds one by id.
func (s *Store) Announcement(ctx context.Context, id int64) (*Announcement, error) {
	return scanAnn(s.db.QueryRowContext(ctx, `SELECT `+annCols+` FROM announcements a LEFT JOIN admins ad ON ad.id = a.author WHERE a.id = ?`, id))
}

// SaveAnnouncement creates (ID 0) or updates an announcement and returns
// its id. Publishing for the first time sets PublishedAt.
func (s *Store) SaveAnnouncement(ctx context.Context, a *Announcement, author int64) (int64, error) {
	now := s.unix()
	if a.ID == 0 {
		pub := int64(0)
		if a.Published {
			pub = now
		}
		res, err := s.db.ExecContext(ctx, `INSERT INTO announcements (title, body, pinned, published, created_at, updated_at, published_at, author)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, a.Title, a.Body, a.Pinned, a.Published, now, now, pub, author)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	res, err := s.db.ExecContext(ctx, `UPDATE announcements SET title = ?, body = ?, pinned = ?, published = ?, updated_at = ?,
		published_at = CASE WHEN ? AND published_at = 0 THEN ? ELSE published_at END WHERE id = ?`,
		a.Title, a.Body, a.Pinned, a.Published, now, a.Published, now, a.ID)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrNotFound
	}
	return a.ID, nil
}

func (s *Store) DeleteAnnouncement(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM announcements WHERE id = ?`, id)
	return err
}

// ---------------------------------------------------------------- reports

// Report kinds, statuses and severities.
var (
	Kinds      = []string{"bug", "security"}
	Statuses   = []string{"new", "triaged", "in-progress", "fixed", "wont-fix", "duplicate"}
	Severities = []string{"", "low", "medium", "high", "critical"}
)

func oneOf(v string, set []string) bool {
	for _, s := range set {
		if v == s {
			return true
		}
	}
	return false
}

// ReportContent is what the reporter sent; it is sealed at rest.
type ReportContent struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Steps       string `json:"steps,omitempty"`
	Version     string `json:"version,omitempty"`
	UserID      string `json:"user_id"`
	Community   string `json:"community"`
	Name        string `json:"name,omitempty"`
	Email       string `json:"email,omitempty"`
	Severity    string `json:"severity,omitempty"`
}

// Report is a report with its triage state.
type Report struct {
	ID        int64
	Ref       string
	Kind      string
	Status    string
	Severity  string
	Assignee  int64
	AssigneeN string
	CreatedAt time.Time
	UpdatedAt time.Time
	Content   ReportContent
}

// Note is an internal note on a report (also the record of triage changes).
type Note struct {
	Author    string
	CreatedAt time.Time
	Body      string
}

var refEncoding = base32.NewEncoding("ABCDEFGHJKLMNPQRSTUVWXYZ23456789").WithPadding(base32.NoPadding)

// AddReport stores a new report and returns its reference.
func (s *Store) AddReport(ctx context.Context, kind string, c ReportContent) (string, error) {
	if !oneOf(kind, Kinds) {
		return "", fmt.Errorf("unknown kind %q", kind)
	}
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	ref := refEncoding.EncodeToString(randomBytes(5))
	now := s.unix()
	var id int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO reports (ref, kind, created_at, updated_at, content) VALUES (?, ?, ?, ?, x'') RETURNING id`,
		ref, kind, now, now).Scan(&id); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE reports SET content = ? WHERE id = ?`, s.seal("reports", "content", rowID(id), b), id); err != nil {
		return "", err
	}
	return ref, tx.Commit()
}

// ReportFilter selects reports; empty fields match everything.
type ReportFilter struct {
	Kind, Status, Severity string
	Limit, Offset          int
}

// Reports lists reports, newest first.
func (s *Store) Reports(ctx context.Context, f ReportFilter) ([]Report, error) {
	q := `SELECT r.id, r.ref, r.kind, r.status, r.severity, COALESCE(r.assignee, 0), COALESCE(a.name, ''), r.created_at, r.updated_at, r.content
		FROM reports r LEFT JOIN admins a ON a.id = r.assignee WHERE 1 = 1`
	var args []any
	for col, v := range map[string]string{"r.kind": f.Kind, "r.status": f.Status, "r.severity": f.Severity} {
		if v != "" {
			q += ` AND ` + col + ` = ?`
			args = append(args, v)
		}
	}
	if f.Limit <= 0 {
		f.Limit = 50
	}
	q += ` ORDER BY r.created_at DESC, r.id DESC LIMIT ? OFFSET ?`
	args = append(args, f.Limit, f.Offset)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Report
	for rows.Next() {
		r, err := s.scanReport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *Store) scanReport(row interface{ Scan(...any) error }) (*Report, error) {
	var r Report
	var c, u int64
	var enc []byte
	if err := row.Scan(&r.ID, &r.Ref, &r.Kind, &r.Status, &r.Severity, &r.Assignee, &r.AssigneeN, &c, &u, &enc); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.CreatedAt, r.UpdatedAt = time.Unix(c, 0), time.Unix(u, 0)
	pt, err := s.open("reports", "content", rowID(r.ID), enc)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(pt, &r.Content); err != nil {
		return nil, err
	}
	return &r, nil
}

// Report finds one report by id.
func (s *Store) Report(ctx context.Context, id int64) (*Report, error) {
	return s.scanReport(s.db.QueryRowContext(ctx, `SELECT r.id, r.ref, r.kind, r.status, r.severity, COALESCE(r.assignee, 0), COALESCE(a.name, ''), r.created_at, r.updated_at, r.content
		FROM reports r LEFT JOIN admins a ON a.id = r.assignee WHERE r.id = ?`, id))
}

// CountReports counts reports per kind and status (the admin overview).
func (s *Store) CountReports(ctx context.Context) (map[string]map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, status, COUNT(*) FROM reports GROUP BY kind, status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]int{}
	for rows.Next() {
		var k, st string
		var n int
		if err := rows.Scan(&k, &st, &n); err != nil {
			return nil, err
		}
		if out[k] == nil {
			out[k] = map[string]int{}
		}
		out[k][st] = n
	}
	return out, rows.Err()
}

// Triage is a change to a report's state, with an optional note.
type Triage struct {
	Status, Severity string
	Assignee         int64
	Note             string
}

// errTriageValue is a status, severity or assignee that doesn't exist.
var errTriageValue = errors.New("unknown status, severity or assignee")

// TriageReport applies t by admin `by`, recording each change and the
// note as notes, in one transaction.
func (s *Store) TriageReport(ctx context.Context, id, by int64, t Triage) error {
	if !oneOf(t.Status, Statuses) || !oneOf(t.Severity, Severities) {
		return errTriageValue
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status, severity, assigneeName string
	var assignee int64
	err = tx.QueryRowContext(ctx, `SELECT r.status, r.severity, COALESCE(r.assignee, 0), COALESCE(a.name, '') FROM reports r
		LEFT JOIN admins a ON a.id = r.assignee WHERE r.id = ?`, id).Scan(&status, &severity, &assignee, &assigneeName)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	var changes []string
	if t.Status != status {
		changes = append(changes, fmt.Sprintf("Status: %s → %s", status, t.Status))
	}
	if t.Severity != severity {
		changes = append(changes, fmt.Sprintf("Severity: %s → %s", orNone(severity), orNone(t.Severity)))
	}
	var newName string
	if t.Assignee != 0 {
		if err := tx.QueryRowContext(ctx, `SELECT name FROM admins WHERE id = ?`, t.Assignee).Scan(&newName); err != nil {
			return errTriageValue
		}
	}
	if t.Assignee != assignee {
		changes = append(changes, fmt.Sprintf("Assignee: %s → %s", orNone(assigneeName), orNone(newName)))
	}
	if len(changes) == 0 && strings.TrimSpace(t.Note) == "" {
		return nil
	}
	var assigneeArg any
	if t.Assignee != 0 {
		assigneeArg = t.Assignee
	}
	now := s.unix()
	if _, err := tx.ExecContext(ctx, `UPDATE reports SET status = ?, severity = ?, assignee = ?, updated_at = ? WHERE id = ?`,
		t.Status, t.Severity, assigneeArg, now, id); err != nil {
		return err
	}
	body := strings.Join(changes, "\n")
	if n := strings.TrimSpace(t.Note); n != "" {
		if body != "" {
			body += "\n\n"
		}
		body += n
	}
	var nid int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO report_notes (report_id, author, created_at, body) VALUES (?, ?, ?, x'') RETURNING id`,
		id, by, now).Scan(&nid); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE report_notes SET body = ? WHERE id = ?`, s.seal("report_notes", "body", rowID(nid), []byte(body)), nid); err != nil {
		return err
	}
	return tx.Commit()
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// Notes lists a report's notes, oldest first.
func (s *Store) Notes(ctx context.Context, reportID int64) ([]Note, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT n.id, COALESCE(a.name, '(removed admin)'), n.created_at, n.body FROM report_notes n
		LEFT JOIN admins a ON a.id = n.author WHERE n.report_id = ? ORDER BY n.created_at, n.id`, reportID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Note
	for rows.Next() {
		var n Note
		var id, c int64
		var enc []byte
		if err := rows.Scan(&id, &n.Author, &c, &enc); err != nil {
			return nil, err
		}
		pt, err := s.open("report_notes", "body", rowID(id), enc)
		if err != nil {
			return nil, err
		}
		n.CreatedAt, n.Body = time.Unix(c, 0), string(pt)
		out = append(out, n)
	}
	return out, rows.Err()
}

// DeleteReport deletes a report and its notes (spam).
func (s *Store) DeleteReport(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM reports WHERE id = ?`, id)
	return err
}
