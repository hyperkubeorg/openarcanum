package community

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/store"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// migrateV7 binds the plaintext columns that decide access to sealed data
// (atrest_bind.go): it adds join_keys.bound, reseals every message body and
// file's metadata with its channel id (and a file's metadata with its
// message id, schema 5), every pin with its channel id, and every report
// with its status and month (schema 6). Rows of a month whose key was
// already deleted can't be read by anyone and are left as they are.
func (s *Server) migrateV7(ctx context.Context, tx store.Tx) error {
	if err := s.checkStorageKey(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE join_keys ADD COLUMN bound BLOB`); err != nil {
		return err
	}
	type joinKey struct {
		id         int64
		secretHash []byte
		uses       uint32
		revoked    bool
		public     bool
	}
	var keys []joinKey
	if err := queryAll(ctx, tx, `SELECT id, secret_hash, uses, revoked, public FROM join_keys`, func(r *sql.Rows) error {
		var k joinKey
		if err := r.Scan(&k.id, &k.secretHash, &k.uses, &k.revoked, &k.public); err != nil {
			return err
		}
		keys = append(keys, k)
		return nil
	}); err != nil {
		return err
	}
	for _, k := range keys {
		bound := s.sealJoinKeyBinding(k.id, joinKeyBinding{SecretHash: k.secretHash, Uses: k.uses, Revoked: k.revoked, Public: k.public})
		if _, err := tx.ExecContext(ctx, `UPDATE join_keys SET bound = ? WHERE id = ?`, bound, k.id); err != nil {
			return err
		}
	}

	monthKeys := map[int][]byte{}
	if err := queryAll(ctx, tx, `SELECT month, key_enc FROM month_keys`, func(r *sql.Rows) error {
		var m int
		var enc []byte
		if err := r.Scan(&m, &enc); err != nil {
			return err
		}
		k, err := s.open("month_keys", "key_enc", monthRow(m), enc)
		if err != nil {
			return err
		}
		monthKeys[m] = k
		return nil
	}); err != nil {
		return err
	}

	type sealed struct {
		id, channelID int64
		messageID     int64 // files only
		month         int
		data          []byte
	}
	var files, messages []sealed
	scan := func(out *[]sealed) func(*sql.Rows) error {
		return func(r *sql.Rows) error {
			var x sealed
			if err := r.Scan(&x.id, &x.channelID, &x.month, &x.data); err != nil {
				return err
			}
			*out = append(*out, x)
			return nil
		}
	}
	if err := queryAll(ctx, tx, `SELECT id, channel_id, message_id, month, meta FROM files WHERE complete = 1 AND length(meta) > 0`, func(r *sql.Rows) error {
		var x sealed
		if err := r.Scan(&x.id, &x.channelID, &x.messageID, &x.month, &x.data); err != nil {
			return err
		}
		files = append(files, x)
		return nil
	}); err != nil {
		return err
	}
	if err := queryAll(ctx, tx, `SELECT id, channel_id, month, body FROM messages WHERE body IS NOT NULL`, scan(&messages)); err != nil {
		return err
	}
	skipped := 0
	for _, f := range files {
		k := monthKeys[f.month]
		if k == nil {
			skipped++
			continue
		}
		pt, err := openWith(k, "files", "meta", rowID(f.id), f.data)
		if err != nil {
			return err
		}
		var meta fileMeta
		if err := json.Unmarshal(pt, &meta); err != nil {
			return errors.Join(&TamperError{"files", "meta", rowID(f.id)}, err)
		}
		meta.ChannelID, meta.MessageID = f.channelID, f.messageID
		b, _ := json.Marshal(&meta)
		if _, err := tx.ExecContext(ctx, `UPDATE files SET meta = ? WHERE id = ?`, sealWith(k, "files", "meta", rowID(f.id), b), f.id); err != nil {
			return err
		}
	}
	for _, m := range messages {
		k := monthKeys[m.month]
		if k == nil {
			skipped++
			continue
		}
		pt, err := openWith(k, "messages", "body", rowID(m.id), m.data)
		if err != nil {
			return err
		}
		var body wire.Message
		if err := proto.Unmarshal(pt, &body); err != nil {
			return errors.Join(&TamperError{"messages", "body", rowID(m.id)}, err)
		}
		body.ChannelId = m.channelID
		b, _ := proto.Marshal(&body)
		if _, err := tx.ExecContext(ctx, `UPDATE messages SET body = ? WHERE id = ?`, sealWith(k, "messages", "body", rowID(m.id), b), m.id); err != nil {
			return err
		}
	}
	// Pins (schema 5) are sealed under the storage key; their channel goes
	// into the sealed record.
	type pin struct {
		messageID, channelID int64
		data                 []byte
	}
	var pins []pin
	if err := queryAll(ctx, tx, `SELECT message_id, channel_id, data FROM pins`, func(r *sql.Rows) error {
		var p pin
		if err := r.Scan(&p.messageID, &p.channelID, &p.data); err != nil {
			return err
		}
		pins = append(pins, p)
		return nil
	}); err != nil {
		return err
	}
	for _, p := range pins {
		var rec pinRec
		if err := s.openJSON("pins", "data", rowID(p.messageID), p.data, &rec); err != nil {
			return err
		}
		rec.ChannelID = p.channelID
		if _, err := tx.ExecContext(ctx, `UPDATE pins SET data = ? WHERE message_id = ?`, s.sealJSON("pins", "data", rowID(p.messageID), &rec), p.messageID); err != nil {
			return err
		}
	}
	// Reports (schema 6): their status and month go into the sealed record.
	type report struct {
		id            int64
		status, month int
		data          []byte
	}
	var reports []report
	if err := queryAll(ctx, tx, `SELECT id, status, month, data FROM reports`, func(r *sql.Rows) error {
		var x report
		if err := r.Scan(&x.id, &x.status, &x.month, &x.data); err != nil {
			return err
		}
		reports = append(reports, x)
		return nil
	}); err != nil {
		return err
	}
	for _, x := range reports {
		var rec reportRec
		if err := s.openJSON("reports", "data", rowID(x.id), x.data, &rec); err != nil {
			return err
		}
		rec.Status, rec.Month = wire.ReportStatus(x.status), x.month
		if _, err := tx.ExecContext(ctx, `UPDATE reports SET data = ? WHERE id = ?`, s.sealJSON("reports", "data", rowID(x.id), &rec), x.id); err != nil {
			return err
		}
	}
	if skipped > 0 && s.log != nil {
		s.log.Warn("schema 7: rows of months whose key was deleted left unconverted", "rows", skipped)
	}
	return nil
}

// checkStorageKey opens key_check, so a migration run with the wrong storage
// key fails as one and leaves the database at the previous version.
func (s *Server) checkStorageKey(ctx context.Context, q dbtx) error {
	var check []byte
	if err := q.QueryRowContext(ctx, `SELECT v FROM meta WHERE k = 'key_check'`).Scan(&check); err != nil {
		return fmt.Errorf("database has no storage key check: %w", err)
	}
	if pt, err := s.open("meta", "v", "key_check", check); err != nil || string(pt) != keyCheckPlaintext {
		return errWrongStorageKey
	}
	return nil
}

// queryAll runs a query and calls scan for each row, closing the rows before
// returning (so callers can write afterwards in the same transaction).
func queryAll(ctx context.Context, q dbtx, query string, scan func(*sql.Rows) error) error {
	rows, err := q.QueryContext(ctx, query)
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
