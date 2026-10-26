package community

import (
	"context"
	"database/sql"
	"errors"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/store"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Schema 6: the moderation and custom emoji tables.
//
//   - reports: the moderator queue (member reports and AutoMod alerts).
//     status and month stay in plaintext (the queue is filtered by status;
//     month is the month key the reason is sealed under, as for messages);
//     dedupe is a keyed lookup hash of (reporter, message), so a member
//     can't report the same message twice; data is the sealed record and
//     reason the reporter's text, sealed under the month key.
//   - automod_rules: one sealed rule record per row.
//   - emoji: the sealed emoji record and its sealed image.
const schemaV6Tables = `
CREATE TABLE reports (
	id INTEGER PRIMARY KEY,
	status INTEGER NOT NULL,
	month INTEGER NOT NULL,
	dedupe BLOB UNIQUE,
	data BLOB NOT NULL,
	reason BLOB
);
CREATE INDEX reports_status ON reports (status, id);
CREATE INDEX reports_month ON reports (month);
CREATE TABLE automod_rules (id INTEGER PRIMARY KEY, data BLOB NOT NULL);
CREATE TABLE emoji (id INTEGER PRIMARY KEY, data BLOB NOT NULL, image BLOB NOT NULL);
`

// migrateV6 gives existing communities the documented custom emoji limit
// (50). The custom emoji toggle (custom_emoji_enabled) gets its default
// when settings are loaded (fillFeatureDefaults), and @everyone already
// holds Use Custom Emoji where schema 4 granted it (MapV3RolePerms: Send
// Messages brings it), so neither is written here.
func (s *Server) migrateV6(ctx context.Context, tx store.Tx) error {
	if err := s.checkStorageKey(ctx, tx); err != nil {
		return err
	}
	var enc []byte
	err := tx.QueryRowContext(ctx, `SELECT v FROM meta WHERE k = 'settings'`).Scan(&enc)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Not claimed yet: the claim starts from DefaultSettings.
		return nil
	case err != nil:
		return err
	}
	b, err := s.open("meta", "v", "settings", enc)
	if err != nil {
		return err
	}
	var set wire.CommunitySettings
	if err := proto.Unmarshal(b, &set); err != nil {
		return &TamperError{"meta", "v", "settings"}
	}
	set.MaxCustomEmoji = DefaultMaxCustomEmoji
	nb, _ := proto.Marshal(&set)
	return s.setMeta(ctx, tx, "settings", nb)
}
