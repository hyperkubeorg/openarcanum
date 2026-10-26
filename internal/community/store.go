package community

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/store"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// ---------------------------------------------------------------- in-memory model
//
// The records below are also the storage format: each row of roles,
// channels, members, devices, bans, join_keys and audit holds one record as
// JSON, sealed under the storage key (atrest.go). The JSON field names are
// part of the on-disk format.

type roleRec struct {
	ID       int64  `json:"-"`
	Name     string `json:"name"`
	Perms    uint64 `json:"permissions"`
	Position int32  `json:"position"`
	Color    uint32 `json:"color"`
	Builtin  bool   `json:"builtin"`
	// Shown separately in the member list.
	Hoist bool `json:"hoist,omitempty"`
	// Anyone may mention a mentionable role; other roles need Mention
	// @everyone/@here and All Roles (messaging.go).
	Mentionable bool `json:"mentionable,omitempty"`
}

type channelRec struct {
	ID        int64            `json:"-"`
	Name      string           `json:"name"`
	Kind      wire.ChannelKind `json:"kind"`
	Position  int32            `json:"position"`
	Topic     []byte           `json:"topic"`
	Key       []byte           `json:"key"`
	Epoch     uint32           `json:"epoch"`
	MsgRet    *uint32          `json:"msg_retention,omitempty"`
	FileRet   *uint32          `json:"file_retention,omitempty"`
	Overrides []Override       `json:"overrides,omitempty"`
	// Schema 4 (channels.go): the topic text, category, whether the
	// overrides are a synced copy of the category's, slowmode and the
	// channel-level feature settings.
	Description   string             `json:"description,omitempty"`
	CategoryID    int64              `json:"category_id,omitempty"`
	Synced        bool               `json:"synced,omitempty"`
	Slowmode      uint32             `json:"slowmode,omitempty"`
	Attachments   wire.ChannelToggle `json:"attachments,omitempty"`
	LinkPreviews  wire.ChannelToggle `json:"link_previews,omitempty"`
	Reactions     wire.ChannelToggle `json:"reactions,omitempty"`
	HistoryForNew wire.ChannelToggle `json:"history_for_new,omitempty"`
}

// categoryRec is a channel category (schema 4), sealed in categories.data.
type categoryRec struct {
	ID        int64      `json:"-"`
	Name      string     `json:"name"`
	Position  int32      `json:"position"`
	Overrides []Override `json:"overrides,omitempty"`
}

func (c *categoryRec) clone() *categoryRec {
	cp := *c
	cp.Overrides = append([]Override(nil), c.Overrides...)
	return &cp
}

type memberRec struct {
	Key       []byte            `json:"key"`
	Name      string            `json:"name"`
	HasAvatar bool              `json:"has_avatar"`
	AvatarVer uint32            `json:"avatar_version"`
	JoinedAt  int64             `json:"joined_at"`
	JoinKeyID int64             `json:"join_key_id"`
	Status    wire.MemberStatus `json:"status"`
	Roles     []int64           `json:"roles,omitempty"`
	// An owner invited this member to become an owner; OwnerAdd is the
	// owner key request their client then submitted (owners.go).
	// Server mute and deafen (Mute Members / Deafen Members), kept across calls.
	VoiceMuted    bool   `json:"voice_muted,omitempty"`
	VoiceDeafened bool   `json:"voice_deafened,omitempty"`
	OwnerInvited  bool   `json:"owner_invited,omitempty"`
	OwnerAdd      []byte `json:"owner_add,omitempty"`
	// The member asked to be removed from the owner set; their key stays in
	// the chain until another owner signs the removal, but the server no
	// longer treats them as an owner (owners.go opResignOwner).
	OwnerLeaving bool `json:"owner_leaving,omitempty"`
	// Per-community nickname and profile (messaging.go).
	Nick     string `json:"nick,omitempty"`
	About    string `json:"about,omitempty"`
	Pronouns string `json:"pronouns,omitempty"`
	// Unix ms until which the member is timed out (moderation.go).
	TimeoutUntil int64 `json:"timeout_until,omitempty"`
	// The member chose Invisible: their sessions don't count as online for
	// anyone else (status.go).
	Invisible bool `json:"invisible,omitempty"`
}

type deviceRec struct {
	DeviceKey []byte `json:"device_key"`
	Member    []byte `json:"member"`
	Name      string `json:"name"`
	FirstSeen int64  `json:"first_seen"`
	LastSeen  int64  `json:"last_seen"`
	Revoked   bool   `json:"revoked"`
}

type banRec struct {
	Key         []byte `json:"key"`
	DisplayName string `json:"display_name"`
	Reason      string `json:"reason"`
	BannedBy    []byte `json:"banned_by"`
	At          int64  `json:"at"`
}

// joinKeyRec is the sealed part of a join key; uses, revoked and public stay
// in plaintext columns (docs/operations.md "Data at rest").
type joinKeyRec struct {
	Label       string  `json:"label"`
	Secret      string  `json:"secret,omitempty"` // public join keys only
	CreatedBy   []byte  `json:"created_by"`
	CreatedAt   int64   `json:"created_at"`
	ExpiresAt   int64   `json:"expires_at"`
	MaxUses     uint32  `json:"max_uses"`
	RequestOnly bool    `json:"request_only"`
	RoleIDs     []int64 `json:"role_ids,omitempty"`
}

type auditRec struct {
	At        int64  `json:"at"`
	Actor     []byte `json:"actor"`
	Summary   string `json:"summary"`
	Action    []byte `json:"action"`
	Signature []byte `json:"signature"`
	// Entries the server writes itself (AutoMod) have no signed action;
	// they name their op and target here instead (audit filters, admin.go).
	Op     string `json:"op,omitempty"`
	Target []byte `json:"target,omitempty"`
}

// fileMeta is a file's sealed metadata, under its month key.
type fileMeta struct {
	Name     string `json:"name"`
	Mime     string `json:"mime"`
	Size     uint64 `json:"size"`
	Key      []byte `json:"key"`
	Chunks   uint32 `json:"chunks"`
	Uploader []byte `json:"uploader"`
	// ChannelID binds the plaintext files.channel_id column: a file moved to
	// another channel on disk fails loadFileMeta.
	ChannelID int64 `json:"channel_id"`
	// MessageID binds files.message_id (0 = a staged upload not posted
	// yet): a posted file can't be made claimable again on disk.
	MessageID int64 `json:"message_id,omitempty"`
}

// pinRec is a pin's sealed record (pins table, storage key).
type pinRec struct {
	By []byte `json:"by"`
	At int64  `json:"at"`
	// ChannelID binds pins.channel_id (listPins checks it).
	ChannelID int64 `json:"channel_id"`
}

func (c *channelRec) clone() *channelRec {
	cp := *c
	cp.Overrides = append([]Override(nil), c.Overrides...)
	return &cp
}

// ---------------------------------------------------------------- keys on disk

func readHexFile(path string, n int) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	v, err := hex.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(v) != n {
		return nil, fmt.Errorf("%s: expected %d hex-encoded bytes", path, n)
	}
	return v, nil
}

func writeHexFile(path string, v []byte) error {
	return identity.WriteFileAtomic(path, []byte(hex.EncodeToString(v)+"\n"), 0o600)
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

// hashSecret is how join-key, claim and move secrets are stored: they are
// high-entropy random strings, so an unkeyed hash is enough to check them
// without keeping them.
func hashSecret(secret string) []byte {
	h := sha256.Sum256([]byte("openarcanum/join-key/v1\x00" + secret))
	return h[:]
}

// bestEffort runs a write whose failure shouldn't fail the request, and logs it.
func (s *Server) bestEffort(ctx context.Context, query string, args ...any) {
	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		s.log.Error("database write failed", "err", err)
	}
}

// count runs a SELECT count(*) query. Errors are returned so callers fail
// closed (a ban check that can't read the ban list must not admit).
func (s *Server) count(ctx context.Context, query string, args ...any) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&n)
	return n, err
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// dbtx is *sql.DB or *sql.Tx.
type dbtx interface {
	execer
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// ---------------------------------------------------------------- loading

// openDB opens the database, migrates it to the current schema and checks
// the storage key. Converting an older database rewrites every row, so it is
// followed by VACUUM and a WAL checkpoint, leaving no plaintext pages behind.
func (s *Server) openDB(ctx context.Context) error {
	db, err := store.Open(filepath.Join(s.cfg.DataDir, "community.db"), "secure_delete(1)")
	if err != nil {
		return err
	}
	s.db = db
	before, err := store.Version(ctx, db)
	if err != nil {
		db.Close()
		return err
	}
	applied, err := store.Apply(ctx, db, s.migrations())
	if err != nil {
		db.Close()
		return fmt.Errorf("%s: %w", filepath.Join(s.cfg.DataDir, "community.db"), err)
	}
	if err := s.loadKeys(ctx); err != nil {
		db.Close()
		return err
	}
	if before > 0 && applied > 0 {
		if err := s.compact(ctx); err != nil {
			db.Close()
			return err
		}
	} else if err := s.checkpoint(ctx); err != nil && s.log != nil {
		// Retries a checkpoint that failed before the last shutdown.
		s.log.Warn("WAL checkpoint at startup failed", "err", err)
	}
	return nil
}

// compact rewrites the database file and empties the WAL, so freed pages
// (which secure_delete zeroes) and old page versions don't linger.
func (s *Server) compact(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `VACUUM`); err != nil {
		return err
	}
	return s.checkpoint(ctx)
}

// checkpoint copies the WAL into the database and truncates it.
func (s *Server) checkpoint(ctx context.Context) error {
	var busy, logFrames, done int
	if err := s.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &done); err != nil {
		return err
	}
	if busy != 0 {
		return errors.New("WAL checkpoint was blocked by an open reader")
	}
	return nil
}

func (s *Server) loadModel(ctx context.Context) error {
	var err error
	if s.ownerKey, err = s.getMeta(ctx, "owner_key"); err != nil {
		return err
	}
	if s.ownerMember, err = s.getMeta(ctx, "owner_member"); err != nil {
		return err
	}
	if s.claimHash, err = s.getMeta(ctx, "claim_hash"); err != nil {
		return err
	}
	if s.moveHash, err = s.getMeta(ctx, "move_hash"); err != nil {
		return err
	}
	if s.routeID, err = s.getMeta(ctx, "route_id"); err != nil {
		return err
	}
	if len(s.routeID) == 0 {
		return errors.New("data directory has no route id (run `community init`)")
	}
	if b, err := s.getMeta(ctx, "cert"); err != nil {
		return err
	} else if b != nil {
		var c wire.SignedServerCert
		if err := proto.Unmarshal(b, &c); err != nil {
			return err
		}
		s.cert = &c
	}
	if s.ownerKey != nil {
		var chain []*wire.SignedOwnerChange
		if s.cert != nil {
			chain = s.cert.OwnerChain
		}
		if s.owners, err = identity.VerifyOwnerChain(s.ownerKey, chain); err != nil {
			return fmt.Errorf("stored owner chain: %w", err)
		}
	}
	s.settings = DefaultSettings()
	if b, err := s.getMeta(ctx, "settings"); err != nil {
		return err
	} else if b != nil {
		if err := proto.Unmarshal(b, s.settings); err != nil {
			return err
		}
	}
	fillFeatureDefaults(s.settings)

	s.roles = map[int64]*roleRec{}
	if err := s.loadRows(ctx, `SELECT id, data FROM roles`, func(id int64, data []byte) error {
		r := &roleRec{}
		if err := s.openJSON("roles", "data", rowID(id), data, r); err != nil {
			return err
		}
		r.ID = id
		s.roles[id] = r
		if r.Builtin && r.Position == 0 {
			s.everyoneID = id
		}
		return nil
	}); err != nil {
		return err
	}

	s.channels = map[int64]*channelRec{}
	if err := s.loadRows(ctx, `SELECT id, data FROM channels`, func(id int64, data []byte) error {
		c := &channelRec{}
		if err := s.openJSON("channels", "data", rowID(id), data, c); err != nil {
			return err
		}
		c.ID = id
		s.channels[id] = c
		return nil
	}); err != nil {
		return err
	}

	s.categories = map[int64]*categoryRec{}
	if err := s.loadRows(ctx, `SELECT id, data FROM categories`, func(id int64, data []byte) error {
		c := &categoryRec{}
		if err := s.openJSON("categories", "data", rowID(id), data, c); err != nil {
			return err
		}
		c.ID = id
		s.categories[id] = c
		return nil
	}); err != nil {
		return err
	}
	if err := s.loadEmoji(ctx); err != nil {
		return err
	}

	s.members = map[string]*memberRec{}
	rows, err := s.db.QueryContext(ctx, `SELECT kid, data FROM members`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var kid, data []byte
		if err := rows.Scan(&kid, &data); err != nil {
			return err
		}
		m := &memberRec{}
		if err := s.openJSON("members", "data", rowKey(kid), data, m); err != nil {
			return err
		}
		if !bytes.Equal(s.kid(m.Key), kid) {
			return &TamperError{"members", "kid", rowKey(kid)}
		}
		s.members[string(m.Key)] = m
	}
	return rows.Err()
}

// loadRows scans (id, data) rows.
func (s *Server) loadRows(ctx context.Context, q string, f func(id int64, data []byte) error) error {
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var data []byte
		if err := rows.Scan(&id, &data); err != nil {
			return err
		}
		if err := f(id, data); err != nil {
			return err
		}
	}
	return rows.Err()
}

// ---------------------------------------------------------------- persistence

func (s *Server) saveMember(ctx context.Context, x execer, m *memberRec) error {
	kid := s.kid(m.Key)
	_, err := x.ExecContext(ctx, `INSERT INTO members (kid, data) VALUES (?, ?) ON CONFLICT (kid) DO UPDATE SET data = excluded.data`,
		kid, s.sealJSON("members", "data", rowKey(kid), m))
	return err
}

func (s *Server) saveRole(ctx context.Context, x execer, r *roleRec) error {
	_, err := x.ExecContext(ctx, `INSERT INTO roles (id, data) VALUES (?, ?) ON CONFLICT (id) DO UPDATE SET data = excluded.data`,
		r.ID, s.sealJSON("roles", "data", rowID(r.ID), r))
	return err
}

func (s *Server) saveChannel(ctx context.Context, x execer, c *channelRec) error {
	_, err := x.ExecContext(ctx, `INSERT INTO channels (id, data) VALUES (?, ?) ON CONFLICT (id) DO UPDATE SET data = excluded.data`,
		c.ID, s.sealJSON("channels", "data", rowID(c.ID), c))
	return err
}

func (s *Server) saveCategory(ctx context.Context, x execer, c *categoryRec) error {
	_, err := x.ExecContext(ctx, `INSERT INTO categories (id, data) VALUES (?, ?) ON CONFLICT (id) DO UPDATE SET data = excluded.data`,
		c.ID, s.sealJSON("categories", "data", rowID(c.ID), c))
	return err
}

func (s *Server) saveDevice(ctx context.Context, x execer, d *deviceRec) error {
	did := s.did(d.DeviceKey)
	_, err := x.ExecContext(ctx, `INSERT INTO devices (did, member, data) VALUES (?, ?, ?) ON CONFLICT (did) DO UPDATE SET member = excluded.member, data = excluded.data`,
		did, s.kid(d.Member), s.sealJSON("devices", "data", rowKey(did), d))
	return err
}

// loadDevice returns the device record for a device key, or nil.
func (s *Server) loadDevice(ctx context.Context, deviceKey []byte) (*deviceRec, error) {
	did := s.did(deviceKey)
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM devices WHERE did = ?`, did).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d := &deviceRec{}
	if err := s.openJSON("devices", "data", rowKey(did), data, d); err != nil {
		return nil, err
	}
	if !bytes.Equal(d.DeviceKey, deviceKey) {
		return nil, &TamperError{"devices", "did", rowKey(did)}
	}
	return d, nil
}

// devicesOf lists a member's devices, oldest first.
func (s *Server) devicesOf(ctx context.Context, member []byte) ([]*deviceRec, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT did, data FROM devices WHERE member = ?`, s.kid(member))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*deviceRec
	for rows.Next() {
		var did, data []byte
		if err := rows.Scan(&did, &data); err != nil {
			return nil, err
		}
		d := &deviceRec{}
		if err := s.openJSON("devices", "data", rowKey(did), data, d); err != nil {
			return nil, err
		}
		if !bytes.Equal(d.Member, member) {
			return nil, &TamperError{"devices", "member", rowKey(did)}
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FirstSeen < out[j].FirstSeen })
	return out, rows.Err()
}

func (s *Server) saveBan(ctx context.Context, x execer, b *banRec) error {
	kid := s.kid(b.Key)
	_, err := x.ExecContext(ctx, `INSERT INTO bans (kid, data) VALUES (?, ?) ON CONFLICT (kid) DO UPDATE SET data = excluded.data`,
		kid, s.sealJSON("bans", "data", rowKey(kid), b))
	return err
}

// loadBan returns the ban record for a member key, or nil.
func (s *Server) loadBan(ctx context.Context, member []byte) (*banRec, error) {
	kid := s.kid(member)
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM bans WHERE kid = ?`, kid).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	b := &banRec{}
	if err := s.openJSON("bans", "data", rowKey(kid), data, b); err != nil {
		return nil, err
	}
	return b, nil
}

// banned reports whether a member key is on the ban list.
func (s *Server) banned(ctx context.Context, member []byte) (bool, error) {
	n, err := s.count(ctx, `SELECT count(*) FROM bans WHERE kid = ?`, s.kid(member))
	return n > 0, err
}

// Contact cards and key packages are stored opaquely, sealed under the
// storage key and bound to the member's lookup hash. Key packages of one
// member are interchangeable one-time packages, so binding them to the
// member (not the row id) is enough.
func (s *Server) sealCard(kid, card []byte) []byte {
	return s.seal("contact_cards", "card", rowKey(kid), card)
}

func (s *Server) openCard(kid, ct []byte) ([]byte, error) {
	return s.open("contact_cards", "card", rowKey(kid), ct)
}

func (s *Server) sealKeyPackage(kid, pkg []byte) []byte {
	return s.seal("key_packages", "package", rowKey(kid), pkg)
}

func (s *Server) openKeyPackage(kid, ct []byte) ([]byte, error) {
	return s.open("key_packages", "package", rowKey(kid), ct)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// nextID returns max(id)+1 for a table; callers hold s.mu.
func (s *Server) nextID(ctx context.Context, x interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, table string) (int64, error) {
	var id sql.NullInt64
	if err := x.QueryRowContext(ctx, "SELECT max(id) FROM "+table).Scan(&id); err != nil {
		return 0, err
	}
	return id.Int64 + 1, nil
}

// ---------------------------------------------------------------- helpers on the model

func (s *Server) subject(m *memberRec) Subject {
	if m == nil {
		return Subject{}
	}
	return Subject{Owner: s.isOwner(m), Key: m.Key, RoleIDs: m.Roles}
}

func (s *Server) roleInfos() (RoleInfo, map[int64]RoleInfo) {
	out := make(map[int64]RoleInfo, len(s.roles))
	for id, r := range s.roles {
		out[id] = RoleInfo{ID: id, Perms: r.Perms, Position: r.Position}
	}
	return out[s.everyoneID], out
}

func (s *Server) basePerms(m *memberRec) uint64 {
	if m == nil || m.Status != wire.MemberStatus_MEMBER_STATUS_ACTIVE {
		return 0
	}
	ev, roles := s.roleInfos()
	return BasePerms(s.subject(m), ev, roles) &^ s.restrictedPerms(m)
}

func (s *Server) channelPerms(m *memberRec, c *channelRec) uint64 {
	if m == nil || m.Status != wire.MemberStatus_MEMBER_STATUS_ACTIVE || c == nil {
		return 0
	}
	ev, roles := s.roleInfos()
	return ChannelPerms(s.subject(m), ev, roles, c.Overrides) &^ s.restrictedPerms(m)
}

// viewSets records which channels each active member can read, so changes
// that remove access can rotate exactly the affected channels.
func (s *Server) viewSets() map[int64]map[string]bool {
	out := map[int64]map[string]bool{}
	for id, c := range s.channels {
		set := map[string]bool{}
		for k, m := range s.members {
			if s.channelPerms(m, c)&PermViewChannel != 0 {
				set[k] = true
			}
		}
		out[id] = set
	}
	return out
}

// lostAccess lists channels where some member who could read before can't now.
func lostAccess(before, after map[int64]map[string]bool) []int64 {
	var ids []int64
	for id, set := range before {
		now := after[id]
		if now == nil {
			continue
		}
		for k := range set {
			if !now[k] {
				ids = append(ids, id)
				break
			}
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func (s *Server) memberRange() string {
	n := 0
	for _, m := range s.members {
		if m.Status == wire.MemberStatus_MEMBER_STATUS_ACTIVE {
			n++
		}
	}
	return MemberRange(n)
}

// MemberRange buckets a member count so listings don't reveal exact sizes.
func MemberRange(n int) string {
	switch {
	case n <= 10:
		return "1-10"
	case n <= 50:
		return "11-50"
	case n <= 200:
		return "51-200"
	case n <= 1000:
		return "201-1000"
	case n <= 5000:
		return "1001-5000"
	default:
		return "5000+"
	}
}

func be64(v int64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(v))
	return b
}

func be32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}
