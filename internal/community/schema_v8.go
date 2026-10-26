package community

import (
	"context"
	"database/sql"
	"errors"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/store"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// migrateV8 binds messages.created_at (atrest_bind.go): it decides history
// visibility for new members and the ban history-deletion window, so every
// message body is resealed with its creation time, which openBody checks.
// Rows of a month whose key was already deleted can't be read by anyone and
// are left as they are.
func (s *Server) migrateV8(ctx context.Context, tx store.Tx) error {
	if err := s.checkStorageKey(ctx, tx); err != nil {
		return err
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
	type row struct {
		id, created int64
		month       int
		body        []byte
	}
	var rows []row
	if err := queryAll(ctx, tx, `SELECT id, created_at, month, body FROM messages WHERE body IS NOT NULL`, func(r *sql.Rows) error {
		var x row
		if err := r.Scan(&x.id, &x.created, &x.month, &x.body); err != nil {
			return err
		}
		rows = append(rows, x)
		return nil
	}); err != nil {
		return err
	}
	skipped := 0
	for _, m := range rows {
		k := monthKeys[m.month]
		if k == nil {
			skipped++
			continue
		}
		pt, err := openWith(k, "messages", "body", rowID(m.id), m.body)
		if err != nil {
			return err
		}
		var body wire.Message
		if err := proto.Unmarshal(pt, &body); err != nil {
			return errors.Join(&TamperError{"messages", "body", rowID(m.id)}, err)
		}
		body.CreatedAt = m.created
		b, _ := proto.Marshal(&body)
		if _, err := tx.ExecContext(ctx, `UPDATE messages SET body = ? WHERE id = ?`, sealWith(k, "messages", "body", rowID(m.id), b), m.id); err != nil {
			return err
		}
	}
	if skipped > 0 && s.log != nil {
		s.log.Warn("schema 8: messages of months whose key was deleted left unconverted", "rows", skipped)
	}
	return nil
}
