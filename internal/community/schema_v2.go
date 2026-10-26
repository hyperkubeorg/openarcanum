package community

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"
	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/store"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// migrations are the community database schema versions:
//
//  1. the original schema (schemaV1);
//  2. everything encrypted at rest (atrest.go): rows are sealed records,
//     public keys become lookup hashes, content moves to month keys;
//  3. owner sets (owners.go): co-owners, who held rank but no owner key,
//     become members invited to become owners (schema_v3.go);
//  4. channel categories and the perms.go permission layout (schema_v4.go);
//  5. messaging (schema_v5.go): files point at their message (up to 10
//     attachments per message), and pins get a table;
//  6. moderation and custom emoji (schema_v6.go): reports, AutoMod rules,
//     emoji;
//  7. plaintext columns that decide access are bound to sealed data
//     (atrest_bind.go, schema_v7.go);
//  8. message creation times are bound too (schema_v8.go);
//  9. network blocks on a dedicated relay (netblock.go): a sealed record per
//     block, found by the member's lookup hash;
//  10. statuses (status.go): each member's status devices and ciphertext,
//     and the key drops waiting for recipient devices, all sealed.
func (s *Server) migrations() []store.Migration {
	return []store.Migration{
		{SQL: schemaV1},
		{Go: s.migrateV2},
		{Go: s.migrateV3},
		{Go: s.migrateV4},
		{SQL: schemaV5Tables, Go: s.migrateV5},
		{SQL: schemaV6Tables, Go: s.migrateV6},
		{Go: s.migrateV7},
		{Go: s.migrateV8},
		{SQL: schemaV9Tables},
		{SQL: schemaV10Tables},
	}
}

// v2MemberRec is a member record as schema version 2 stored it, with the
// co-owner flag that version 3 converts.
type v2MemberRec struct {
	memberRec
	CoOwner bool `json:"co_owner,omitempty"`
}

// schemaV2Tables moves the v1 tables that held plaintext aside (as *_v1;
// renaming a parent also repoints its children's foreign keys) and creates
// their v2 replacements under the final names.
const schemaV2Tables = `
DROP INDEX members_join_key;
DROP INDEX devices_member;
DROP INDEX key_packages_member;
ALTER TABLE members RENAME TO members_v1;
ALTER TABLE devices RENAME TO devices_v1;
ALTER TABLE roles RENAME TO roles_v1;
ALTER TABLE channels RENAME TO channels_v1;
ALTER TABLE join_keys RENAME TO join_keys_v1;
ALTER TABLE bans RENAME TO bans_v1;
ALTER TABLE audit RENAME TO audit_v1;
ALTER TABLE contact_cards RENAME TO contact_cards_v1;
ALTER TABLE key_packages RENAME TO key_packages_v1;
CREATE TABLE month_keys (month INTEGER PRIMARY KEY, key_enc BLOB NOT NULL);
CREATE TABLE members (kid BLOB PRIMARY KEY, data BLOB NOT NULL, avatar BLOB);
CREATE TABLE devices (did BLOB PRIMARY KEY, member BLOB NOT NULL, data BLOB NOT NULL);
CREATE INDEX devices_member ON devices (member);
CREATE TABLE roles (id INTEGER PRIMARY KEY, data BLOB NOT NULL);
CREATE TABLE channels (id INTEGER PRIMARY KEY, data BLOB NOT NULL);
CREATE TABLE join_keys (
	id INTEGER PRIMARY KEY,
	secret_hash BLOB NOT NULL UNIQUE,
	uses INTEGER NOT NULL DEFAULT 0,
	revoked INTEGER NOT NULL DEFAULT 0,
	public INTEGER NOT NULL DEFAULT 0,
	data BLOB NOT NULL
);
CREATE TABLE bans (kid BLOB PRIMARY KEY, data BLOB NOT NULL);
CREATE TABLE audit (id INTEGER PRIMARY KEY, data BLOB NOT NULL);
CREATE TABLE contact_cards (member BLOB PRIMARY KEY, card BLOB NOT NULL);
CREATE TABLE key_packages (id INTEGER PRIMARY KEY, member BLOB NOT NULL, device BLOB NOT NULL, package BLOB NOT NULL);
CREATE INDEX key_packages_member ON key_packages (member, device, id);
ALTER TABLE messages ADD COLUMN month INTEGER NOT NULL DEFAULT 0;
ALTER TABLE files ADD COLUMN month INTEGER NOT NULL DEFAULT 0;
ALTER TABLE files ADD COLUMN meta BLOB;
`

// schemaV2Drop removes the v1 tables and columns, children first so foreign
// keys never block a drop.
const schemaV2Drop = `
DROP TABLE member_roles;
DROP TABLE channel_overrides;
DROP TABLE join_key_roles;
DROP TABLE members_v1;
DROP TABLE devices_v1;
DROP TABLE roles_v1;
DROP TABLE channels_v1;
DROP TABLE join_keys_v1;
DROP TABLE bans_v1;
DROP TABLE audit_v1;
DROP TABLE contact_cards_v1;
DROP TABLE key_packages_v1;
ALTER TABLE messages DROP COLUMN author;
ALTER TABLE messages DROP COLUMN edited_at;
ALTER TABLE files DROP COLUMN uploader;
ALTER TABLE files DROP COLUMN name_enc;
ALTER TABLE files DROP COLUMN mime;
ALTER TABLE files DROP COLUMN size;
ALTER TABLE files DROP COLUMN key_enc;
ALTER TABLE files DROP COLUMN chunks;
CREATE INDEX messages_month ON messages (month);
CREATE INDEX files_month ON files (month);
`

// v2Migration carries state through migrateV2.
type v2Migration struct {
	s         *Server
	tx        store.Tx
	monthKeys map[int][]byte
}

// openLegacy opens a v1 storage-key ciphertext (nonce || sealed, raw AAD).
func (s *Server) openLegacy(aad string, ct []byte) ([]byte, error) {
	aead, _ := chacha20poly1305.NewX(s.storageKey)
	if len(ct) < aead.NonceSize() {
		return nil, errWrongStorageKey
	}
	pt, err := aead.Open(nil, ct[:aead.NonceSize()], ct[aead.NonceSize():], []byte(aad))
	if err != nil {
		return nil, errWrongStorageKey
	}
	return pt, nil
}

// monthSeal seals under a month key created inside the migration
// transaction (the server's cache is not touched until Open loads keys).
func (m *v2Migration) monthSeal(ctx context.Context, month int, table, column, row string, pt []byte) ([]byte, error) {
	k := m.monthKeys[month]
	if k == nil {
		k = randomBytes(chacha20poly1305.KeySize)
		if _, err := m.tx.ExecContext(ctx, `INSERT INTO month_keys (month, key_enc) VALUES (?, ?)`,
			month, m.s.seal("month_keys", "key_enc", monthRow(month), k)); err != nil {
			return nil, err
		}
		m.monthKeys[month] = k
	}
	return sealWith(k, table, column, row, pt), nil
}

func (s *Server) migrateV2(ctx context.Context, tx store.Tx) error {
	m := &v2Migration{s: s, tx: tx, monthKeys: map[int][]byte{}}
	if _, err := tx.ExecContext(ctx, schemaV2Tables); err != nil {
		return err
	}
	// Meta first: the lookup key is needed for every keyed row below.
	s.lookupKey = randomBytes(32)
	for _, step := range []func(context.Context) error{
		m.meta, m.members, m.devices, m.roles, m.channels, m.joinKeys,
		m.messages, m.files, m.bans, m.audit,
	} {
		// contact_cards and key_packages are not converted: they belong to
		// the retired DM v1 scheme, which no client can use. Clients publish
		// v2 cards (docs/protocol.md §9.2) when they next connect.
		if err := step(ctx); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, schemaV2Drop)
	return err
}

// query collects rows with scan, closing them before any writes.
func (m *v2Migration) query(ctx context.Context, q string, scan func(*sql.Rows) error) error {
	rows, err := m.tx.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (m *v2Migration) meta(ctx context.Context) error {
	type kv struct {
		k string
		v []byte
	}
	var all []kv
	if err := m.query(ctx, `SELECT k, v FROM meta`, func(r *sql.Rows) error {
		var e kv
		all = append(all, e)
		return r.Scan(&all[len(all)-1].k, &all[len(all)-1].v)
	}); err != nil {
		return err
	}
	for _, e := range all {
		if err := m.s.setMeta(ctx, m.tx, e.k, e.v); err != nil {
			return err
		}
	}
	if _, err := m.tx.ExecContext(ctx, `INSERT INTO meta (k, v) VALUES ('key_check', ?)`,
		m.s.seal("meta", "v", "key_check", []byte(keyCheckPlaintext))); err != nil {
		return err
	}
	return m.s.setMeta(ctx, m.tx, "lookup_key", m.s.lookupKey)
}

func (m *v2Migration) members(ctx context.Context) error {
	recs := map[string]*v2MemberRec{}
	avatars := map[string][]byte{}
	var order []string
	if err := m.query(ctx, `SELECT key, display_name, avatar, avatar_version, joined_at, join_key_id, status, co_owner FROM members_v1`, func(r *sql.Rows) error {
		rec := &v2MemberRec{}
		var avatar []byte
		var status int32
		var co int
		if err := r.Scan(&rec.Key, &rec.Name, &avatar, &rec.AvatarVer, &rec.JoinedAt, &rec.JoinKeyID, &status, &co); err != nil {
			return err
		}
		rec.Status, rec.CoOwner, rec.HasAvatar = wire.MemberStatus(status), co != 0, avatar != nil
		recs[string(rec.Key)] = rec
		avatars[string(rec.Key)] = avatar
		order = append(order, string(rec.Key))
		return nil
	}); err != nil {
		return err
	}
	if err := m.query(ctx, `SELECT member, role_id FROM member_roles ORDER BY role_id`, func(r *sql.Rows) error {
		var k []byte
		var id int64
		if err := r.Scan(&k, &id); err != nil {
			return err
		}
		if rec := recs[string(k)]; rec != nil {
			rec.Roles = append(rec.Roles, id)
		}
		return nil
	}); err != nil {
		return err
	}
	for _, k := range order {
		rec := recs[k]
		kid := m.s.kid(rec.Key)
		if _, err := m.tx.ExecContext(ctx, `INSERT INTO members (kid, data) VALUES (?, ?)`, kid, m.s.sealJSON("members", "data", rowKey(kid), rec)); err != nil {
			return err
		}
		if a := avatars[k]; a != nil {
			if _, err := m.tx.ExecContext(ctx, `UPDATE members SET avatar = ? WHERE kid = ?`, m.s.seal("members", "avatar", rowKey(kid), a), kid); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *v2Migration) devices(ctx context.Context) error {
	var recs []*deviceRec
	if err := m.query(ctx, `SELECT device_key, member, name, first_seen, last_seen, revoked FROM devices_v1`, func(r *sql.Rows) error {
		d := &deviceRec{}
		var revoked int
		if err := r.Scan(&d.DeviceKey, &d.Member, &d.Name, &d.FirstSeen, &d.LastSeen, &revoked); err != nil {
			return err
		}
		d.Revoked = revoked != 0
		recs = append(recs, d)
		return nil
	}); err != nil {
		return err
	}
	for _, d := range recs {
		if err := m.s.saveDevice(ctx, m.tx, d); err != nil {
			return err
		}
	}
	return nil
}

func (m *v2Migration) roles(ctx context.Context) error {
	var recs []*roleRec
	if err := m.query(ctx, `SELECT id, name, permissions, position, color, builtin FROM roles_v1`, func(r *sql.Rows) error {
		rec := &roleRec{}
		var builtin int
		if err := r.Scan(&rec.ID, &rec.Name, &rec.Perms, &rec.Position, &rec.Color, &builtin); err != nil {
			return err
		}
		rec.Builtin = builtin != 0
		recs = append(recs, rec)
		return nil
	}); err != nil {
		return err
	}
	for _, rec := range recs {
		if err := m.s.saveRole(ctx, m.tx, rec); err != nil {
			return err
		}
	}
	return nil
}

func (m *v2Migration) channels(ctx context.Context) error {
	recs := map[int64]*channelRec{}
	var order []int64
	if err := m.query(ctx, `SELECT id, name, kind, position, topic, key, key_epoch, msg_retention, file_retention FROM channels_v1`, func(r *sql.Rows) error {
		c := &channelRec{}
		var mr, fr sql.NullInt64
		var kind int32
		if err := r.Scan(&c.ID, &c.Name, &kind, &c.Position, &c.Topic, &c.Key, &c.Epoch, &mr, &fr); err != nil {
			return err
		}
		c.Kind = wire.ChannelKind(kind)
		if mr.Valid {
			v := uint32(mr.Int64)
			c.MsgRet = &v
		}
		if fr.Valid {
			v := uint32(fr.Int64)
			c.FileRet = &v
		}
		recs[c.ID] = c
		order = append(order, c.ID)
		return nil
	}); err != nil {
		return err
	}
	if err := m.query(ctx, `SELECT channel_id, role_id, allow, deny FROM channel_overrides`, func(r *sql.Rows) error {
		var cid int64
		var o Override
		if err := r.Scan(&cid, &o.RoleID, &o.Allow, &o.Deny); err != nil {
			return err
		}
		if c := recs[cid]; c != nil {
			c.Overrides = append(c.Overrides, o)
		}
		return nil
	}); err != nil {
		return err
	}
	for _, id := range order {
		if err := m.s.saveChannel(ctx, m.tx, recs[id]); err != nil {
			return err
		}
	}
	return nil
}

func (m *v2Migration) joinKeys(ctx context.Context) error {
	type row struct {
		id                    int64
		hash                  []byte
		uses, revoked, public int64
		rec                   joinKeyRec
		requestOnly           int
		secretEnc             []byte
	}
	var rows []*row
	if err := m.query(ctx, `SELECT id, label, secret_hash, secret_enc, created_by, created_at, expires_at, max_uses, uses, request_only, revoked, public FROM join_keys_v1`, func(r *sql.Rows) error {
		x := &row{}
		if err := r.Scan(&x.id, &x.rec.Label, &x.hash, &x.secretEnc, &x.rec.CreatedBy, &x.rec.CreatedAt, &x.rec.ExpiresAt,
			&x.rec.MaxUses, &x.uses, &x.requestOnly, &x.revoked, &x.public); err != nil {
			return err
		}
		x.rec.RequestOnly = x.requestOnly != 0
		rows = append(rows, x)
		return nil
	}); err != nil {
		return err
	}
	byID := map[int64]*row{}
	for _, x := range rows {
		byID[x.id] = x
		if x.secretEnc != nil {
			pt, err := m.s.openLegacy("public-join-key", x.secretEnc)
			if err != nil {
				return err
			}
			x.rec.Secret = string(pt)
		}
	}
	if err := m.query(ctx, `SELECT join_key_id, role_id FROM join_key_roles ORDER BY role_id`, func(r *sql.Rows) error {
		var id, role int64
		if err := r.Scan(&id, &role); err != nil {
			return err
		}
		if x := byID[id]; x != nil {
			x.rec.RoleIDs = append(x.rec.RoleIDs, role)
		}
		return nil
	}); err != nil {
		return err
	}
	for _, x := range rows {
		if _, err := m.tx.ExecContext(ctx, `INSERT INTO join_keys (id, secret_hash, uses, revoked, public, data) VALUES (?, ?, ?, ?, ?, ?)`,
			x.id, x.hash, x.uses, x.revoked, x.public, m.s.sealJSON("join_keys", "data", rowID(x.id), &x.rec)); err != nil {
			return err
		}
	}
	return nil
}

func (m *v2Migration) messages(ctx context.Context) error {
	type row struct {
		id, created, edited int64
		author, body        []byte
		deleted             int
	}
	var rows []row
	if err := m.query(ctx, `SELECT id, author, created_at, edited_at, deleted, body FROM messages`, func(r *sql.Rows) error {
		var x row
		if err := r.Scan(&x.id, &x.author, &x.created, &x.edited, &x.deleted, &x.body); err != nil {
			return err
		}
		rows = append(rows, x)
		return nil
	}); err != nil {
		return err
	}
	for _, x := range rows {
		msg := &wire.Message{Author: x.author, EditedAt: x.edited}
		if x.deleted == 0 && x.body != nil {
			pt, err := m.s.openLegacy(fmt.Sprintf("msg/%d", x.id), x.body)
			if err != nil {
				return err
			}
			var old wire.Message
			if err := proto.Unmarshal(pt, &old); err != nil {
				return err
			}
			msg.Text, msg.ReplyTo = old.Text, old.ReplyTo
		}
		b, _ := proto.Marshal(msg)
		month := monthOf(x.created)
		ct, err := m.monthSeal(ctx, month, "messages", "body", rowID(x.id), b)
		if err != nil {
			return err
		}
		if _, err := m.tx.ExecContext(ctx, `UPDATE messages SET body = ?, month = ? WHERE id = ?`, ct, month, x.id); err != nil {
			return err
		}
	}
	return nil
}

func (m *v2Migration) files(ctx context.Context) error {
	type row struct {
		id, created     int64
		nameEnc, keyEnc []byte
		meta            fileMeta
		complete        int
	}
	var rows []*row
	if err := m.query(ctx, `SELECT id, uploader, name_enc, mime, size, key_enc, chunks, created_at, complete FROM files`, func(r *sql.Rows) error {
		x := &row{}
		if err := r.Scan(&x.id, &x.meta.Uploader, &x.nameEnc, &x.meta.Mime, &x.meta.Size, &x.keyEnc, &x.meta.Chunks, &x.created, &x.complete); err != nil {
			return err
		}
		rows = append(rows, x)
		return nil
	}); err != nil {
		return err
	}
	for _, x := range rows {
		if x.complete != 0 {
			name, err := m.s.openLegacy(fmt.Sprintf("file/%d/name", x.id), x.nameEnc)
			if err != nil {
				return err
			}
			key, err := m.s.openLegacy(fmt.Sprintf("file/%d/key", x.id), x.keyEnc)
			if err != nil {
				return err
			}
			x.meta.Name, x.meta.Key = string(name), key
		}
		b, _ := json.Marshal(&x.meta)
		month := monthOf(x.created)
		ct, err := m.monthSeal(ctx, month, "files", "meta", rowID(x.id), b)
		if err != nil {
			return err
		}
		if _, err := m.tx.ExecContext(ctx, `UPDATE files SET meta = ?, month = ? WHERE id = ?`, ct, month, x.id); err != nil {
			return err
		}
	}
	return nil
}

func (m *v2Migration) bans(ctx context.Context) error {
	var recs []*banRec
	if err := m.query(ctx, `SELECT key, display_name, reason, banned_by, at FROM bans_v1`, func(r *sql.Rows) error {
		b := &banRec{}
		recs = append(recs, b)
		return r.Scan(&b.Key, &b.DisplayName, &b.Reason, &b.BannedBy, &b.At)
	}); err != nil {
		return err
	}
	for _, b := range recs {
		if err := m.s.saveBan(ctx, m.tx, b); err != nil {
			return err
		}
	}
	return nil
}

func (m *v2Migration) audit(ctx context.Context) error {
	type row struct {
		id  int64
		rec auditRec
	}
	var rows []*row
	if err := m.query(ctx, `SELECT id, at, actor, summary, action, signature FROM audit_v1`, func(r *sql.Rows) error {
		x := &row{}
		rows = append(rows, x)
		return r.Scan(&x.id, &x.rec.At, &x.rec.Actor, &x.rec.Summary, &x.rec.Action, &x.rec.Signature)
	}); err != nil {
		return err
	}
	for _, x := range rows {
		if _, err := m.tx.ExecContext(ctx, `INSERT INTO audit (id, data) VALUES (?, ?)`, x.id, m.s.sealJSON("audit", "data", rowID(x.id), &x.rec)); err != nil {
			return err
		}
	}
	return nil
}
