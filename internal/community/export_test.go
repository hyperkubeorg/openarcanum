package community

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"path/filepath"
	"sort"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/store"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// LatestSchema is the schema version this build migrates to.
func LatestSchema() int { return len((&Server{}).migrations()) }

// MigrateTo brings the database in cfg.DataDir to schema version n (not the
// latest), as an older build would have left it.
func MigrateTo(ctx context.Context, cfg Config, n int) error {
	cfg.Resolve()
	key, err := readHexFile(cfg.StorageKeyFile, 32)
	if err != nil {
		return err
	}
	s := &Server{cfg: cfg, storageKey: key}
	db, err := store.Open(filepath.Join(cfg.DataDir, "community.db"), "secure_delete(1)")
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = store.Apply(ctx, db, s.migrations()[:n])
	return err
}

// Test hooks for the external test package: read the server's decrypted
// view of its own database without a member session.

// MonthOf exposes the month numbering (YYYYMM, UTC).
var MonthOf = monthOf

// MonthEnd exposes the first millisecond after a month.
var MonthEnd = monthEnd

// ErrMonthKeyDeleted is returned for data whose month key is gone.
var ErrMonthKeyDeleted = errMonthKeyDeleted

// ErrWrongStorageKey is returned when the storage key can't open the database.
var ErrWrongStorageKey = errWrongStorageKey

// MonthKeys lists the months that still have a key.
func (s *Server) MonthKeys(ctx context.Context) ([]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT month FROM month_keys ORDER BY month`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var m int
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// StoredBody returns a message's month and sealed body as stored.
func (s *Server) StoredBody(ctx context.Context, id int64) (month int, body []byte, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT month, body FROM messages WHERE id = ?`, id).Scan(&month, &body)
	return
}

// OpenStoredBody decrypts a sealed body with the month key, as history does.
func (s *Server) OpenStoredBody(ctx context.Context, id int64, month int, body []byte) (*wire.Message, error) {
	return s.openSealedBody(ctx, storedMessage{id: id, month: month, body: body})
}

// Messages returns a channel's stored messages, oldest first, decrypted.
func (s *Server) Messages(ctx context.Context, channelID int64) ([]*wire.Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT `+msgCols+` FROM messages WHERE channel_id = ? ORDER BY id`, channelID)
	if err != nil {
		return nil, err
	}
	var raw []storedMessage
	for rows.Next() {
		r, err := scanStored(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		raw = append(raw, r)
	}
	rows.Close()
	var out []*wire.Message
	for _, r := range raw {
		m, err := s.scanMessage(ctx, r)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// Snapshot is the decrypted model: names of members, channels and roles.
type Snapshot struct {
	Members, Channels, Roles map[string]int64 // name -> id (members: status)
	Owner                    []byte
	OwnerInvited             []string // names of members invited to become owners
	Settings                 *wire.CommunitySettings
}

func (s *Server) Snapshot() *Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := &Snapshot{Members: map[string]int64{}, Channels: map[string]int64{}, Roles: map[string]int64{}, Owner: s.ownerKey, Settings: s.settings}
	for _, m := range s.members {
		out.Members[m.Name] = int64(m.Status)
		if m.OwnerInvited {
			out.OwnerInvited = append(out.OwnerInvited, m.Name)
		}
	}
	for _, c := range s.channels {
		out.Channels[c.Name] = c.ID
	}
	for _, r := range s.roles {
		out.Roles[r.Name] = r.ID
	}
	return out
}

// RolePerms returns the permissions of the role with this name.
func (s *Server) RolePerms(name string) (uint64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.roles {
		if r.Name == name {
			return r.Perms, true
		}
	}
	return 0, false
}

// ChannelOverrides returns a channel's overrides, whether they are synced
// with its category, and its category id.
func (s *Server) ChannelOverrides(id int64) ([]Override, bool, int64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c := s.channels[id]
	if c == nil {
		return nil, false, 0
	}
	return append([]Override(nil), c.Overrides...), c.Synced, c.CategoryID
}

// CategoryNames lists category names in order.
func (s *Server) CategoryNames() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for _, c := range s.sortedCategories() {
		out = append(out, c.Name)
	}
	return out
}

// JoinKeyLabels lists join key labels (newest first) and the active public
// join key secret.
func (s *Server) JoinKeyLabels(ctx context.Context) ([]string, string, error) {
	keys, err := s.queryJoinKeys(ctx, s.db, `ORDER BY id DESC`)
	if err != nil {
		return nil, "", err
	}
	var labels []string
	for _, k := range keys {
		labels = append(labels, k.Rec.Label)
	}
	return labels, s.publicJoinKey(ctx), nil
}

// BanReasons lists ban reasons.
func (s *Server) BanReasons(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kid, data FROM bans`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var kid, data []byte
		if err := rows.Scan(&kid, &data); err != nil {
			return nil, err
		}
		var b banRec
		if err := s.openJSON("bans", "data", rowKey(kid), data, &b); err != nil {
			return nil, err
		}
		out = append(out, b.DisplayName+": "+b.Reason)
	}
	sort.Strings(out)
	return out, rows.Err()
}

// AuditSummaries lists audit summaries, oldest first.
func (s *Server) AuditSummaries(ctx context.Context) ([]string, error) {
	var out []string
	err := s.loadRows(ctx, `SELECT id, data FROM audit ORDER BY id`, func(id int64, data []byte) error {
		var e auditRec
		if err := s.openJSON("audit", "data", rowID(id), data, &e); err != nil {
			return err
		}
		out = append(out, e.Summary)
		return nil
	})
	return out, err
}

// FileChannel reads a file's metadata as downloads do and returns its
// channel.
func (s *Server) FileChannel(ctx context.Context, id int64) (int64, error) {
	ch, _, err := s.loadFileMeta(ctx, id)
	return ch, err
}

// FileRef builds a message's file reference.
func (s *Server) FileRef(ctx context.Context, id int64) *wire.FileRef {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fileRef(ctx, id)
}

// ContactCard returns a member's decrypted contact card and key package count.
func (s *Server) ContactCard(ctx context.Context, member []byte) ([]byte, int, error) {
	var sealed []byte
	if err := s.db.QueryRowContext(ctx, `SELECT card FROM contact_cards WHERE member = ?`, s.kid(member)).Scan(&sealed); err != nil {
		return nil, 0, err
	}
	card, err := s.openCard(s.kid(member), sealed)
	if err != nil {
		return nil, 0, err
	}
	n, err := s.count(ctx, `SELECT count(*) FROM key_packages WHERE member = ?`, s.kid(member))
	return card, n, err
}

// Avatar returns a member's decrypted avatar.
func (s *Server) Avatar(ctx context.Context, member []byte) ([]byte, error) {
	r, err := s.getAvatar(ctx, &wire.GetAvatar{Member: member})
	if err != nil {
		return nil, err
	}
	return r.GetAvatar().Avatar, nil
}

// BackupDir is where the server writes backups.
func (s *Server) BackupDir() string { return s.cfg.BackupDir }

// EncryptBackup encrypts plain as a backup for format tests.
func EncryptBackup(key, plain []byte) []byte {
	var b bytes.Buffer
	if err := encryptBackup(key, &b, bytes.NewReader(plain)); err != nil {
		panic(err)
	}
	return b.Bytes()
}

// DecryptBackup reads a whole backup into memory (tests).
func DecryptBackup(key, data []byte) ([]byte, error) {
	r, err := OpenBackup(key, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

// SetBackupHook runs f after each backup's snapshot is taken (tests).
func (s *Server) SetBackupHook(f func()) { s.backupHook = f }

// TryLockServer reports whether the server's main lock is free (tests).
func (s *Server) TryLockServer() bool {
	if !s.mu.TryLock() {
		return false
	}
	s.mu.Unlock()
	return true
}

// DB is the server's database (tests).
func (s *Server) DB() *sql.DB { return s.db }

// ExpireStaged deletes staged uploads older than StagedFileTTL at now.
func (s *Server) ExpireStaged(ctx context.Context, now time.Time) (int, error) {
	return s.expireStaged(ctx, now)
}

// StagedCount counts files not attached to any message.
func (s *Server) StagedCount(ctx context.Context) (int, error) {
	return s.count(ctx, `SELECT count(*) FROM files WHERE message_id = 0`)
}

// SetMaxOpenReports lowers the open-report cap until the test ends.
func SetMaxOpenReports(n int) (restore func()) {
	old := maxOpenReports
	maxOpenReports = n
	return func() { maxOpenReports = old }
}

// OnlineMembers lists the members with an open session.
func (s *Server) OnlineMembers() [][]byte {
	var out [][]byte
	for _, ss := range s.allSessions() {
		out = append(out, ss.member)
	}
	return out
}
