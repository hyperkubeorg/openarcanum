package community

import (
	"context"
	"database/sql"
	"errors"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/store"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Schema 5: messaging (docs/protocol.md §4.3).
//
// schemaV5Tables is the SQL part: a message carries up to 10 attachments, so files
// point at their message (0 = a staged upload not posted yet, or a file
// whose message is gone) instead of each message holding one file id; and
// pins get a table. files.message_id and pins.channel_id stay in plaintext
// like messages.channel_id: they link rows, the content stays sealed, and
// schema 7 binds both to sealed data (atrest_bind.go).
const schemaV5Tables = `
ALTER TABLE files ADD COLUMN message_id INTEGER NOT NULL DEFAULT 0;
UPDATE files SET message_id = (SELECT m.id FROM messages m WHERE m.file_id = files.id)
	WHERE id IN (SELECT file_id FROM messages WHERE file_id != 0);
CREATE INDEX files_message ON files (message_id);
CREATE TABLE pins (
	message_id INTEGER PRIMARY KEY REFERENCES messages (id) ON DELETE CASCADE,
	channel_id INTEGER NOT NULL,
	data BLOB NOT NULL
);
CREATE INDEX pins_channel ON pins (channel_id);
`

// migrateV5 moves each message's file id into its sealed body (the list of
// attachments) and drops messages.file_id. @everyone already holds the
// messaging permissions that are on by default (Add Reactions, Embed Links,
// Change Nickname): schema 4 granted them (MapV3RolePerms).
func (s *Server) migrateV5(ctx context.Context, tx store.Tx) error {
	if err := s.checkStorageKey(ctx, tx); err != nil {
		return err
	}
	monthKeys := map[int][]byte{}
	monthKey := func(m int) ([]byte, error) {
		if k, ok := monthKeys[m]; ok {
			return k, nil
		}
		var enc []byte
		err := tx.QueryRowContext(ctx, `SELECT key_enc FROM month_keys WHERE month = ?`, m).Scan(&enc)
		if errors.Is(err, sql.ErrNoRows) {
			monthKeys[m] = nil
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		k, err := s.open("month_keys", "key_enc", monthRow(m), enc)
		if err != nil {
			return nil, err
		}
		monthKeys[m] = k
		return k, nil
	}
	type row struct {
		id, fileID int64
		month      int
		body       []byte
	}
	var rows []row
	rs, err := tx.QueryContext(ctx, `SELECT id, file_id, month, body FROM messages WHERE file_id != 0 AND deleted = 0`)
	if err != nil {
		return err
	}
	for rs.Next() {
		var r row
		if err := rs.Scan(&r.id, &r.fileID, &r.month, &r.body); err != nil {
			rs.Close()
			return err
		}
		rows = append(rows, r)
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return err
	}
	for _, r := range rows {
		k, err := monthKey(r.month)
		if err != nil {
			return err
		}
		if k == nil {
			// The month is past retention; the purge job deletes the row.
			continue
		}
		pt, err := openWith(k, "messages", "body", rowID(r.id), r.body)
		if err != nil {
			return err
		}
		var body wire.Message
		if err := proto.Unmarshal(pt, &body); err != nil {
			return &TamperError{"messages", "body", rowID(r.id)}
		}
		body.Files = []*wire.FileRef{{FileId: r.fileID}}
		b, err := proto.Marshal(bodyOf(&body))
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE messages SET body = ? WHERE id = ?`, sealWith(k, "messages", "body", rowID(r.id), b), r.id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE messages DROP COLUMN file_id`); err != nil {
		return err
	}
	return nil
}
