package meta

// Mailbox replication inside a maintainer cluster (docs/protocol.md §7.5):
// multi-writer, both directions. Every item has a global id computed from
// its content, the same on every node; the items a node holds converge to
// the union of every member's items minus every member's tombstones. Local
// ids stay a per-node arrival order (an item that arrives late from a peer
// gets a higher local id, so a reader's after_id never skips it).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/store"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// TombstoneSlack keeps a deleted item's tombstone past the item's own
// expiry, for clock skew between members: every copy has expired by then,
// so a deleted item can't come back.
const TombstoneSlack = 24 * time.Hour

// Peer log kinds.
const (
	logItem      = "i"
	logTombstone = "t"
)

// MailboxGID is an item's global id: SHA-256("openarcanum/mailbox-item/v1" ||
// 0x00 || inbox || blob). Inbox addresses are 32 bytes, so the
// concatenation is unambiguous. Olm and Megolm ciphertexts are unique per
// message, so the same bytes in the same inbox are the same message.
func MailboxGID(inbox, blob []byte) []byte {
	h := sha256.New()
	h.Write([]byte("openarcanum/mailbox-item/v1\x00"))
	h.Write(inbox)
	h.Write(blob)
	return h.Sum(nil)
}

// schemaMailboxGID is the schema version mailboxGlobalIDs brings a
// database to.
const schemaMailboxGID = 11

// mailboxGlobalIDs (schema version 11) gives every stored item its global id,
// keeping the oldest of identical items, and adds the tombstones.
func mailboxGlobalIDs(ctx context.Context, tx store.Tx) error {
	for _, q := range []string{
		`ALTER TABLE mailbox ADD COLUMN gid BLOB NOT NULL DEFAULT x''`,
		`ALTER TABLE mailbox ADD COLUMN origin_received_at INTEGER NOT NULL DEFAULT 0`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, inbox, blob FROM mailbox ORDER BY id`)
	if err != nil {
		return err
	}
	type idGID struct {
		id  int64
		gid []byte
	}
	var keep []idGID
	var dups []int64
	seen := map[string]bool{}
	for rows.Next() {
		var id int64
		var inbox, blob []byte
		if err := rows.Scan(&id, &inbox, &blob); err != nil {
			rows.Close()
			return err
		}
		g := MailboxGID(inbox, blob)
		if seen[string(g)] {
			dups = append(dups, id)
			continue
		}
		seen[string(g)] = true
		keep = append(keep, idGID{id, g})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range dups {
		if _, err := tx.ExecContext(ctx, `DELETE FROM mailbox WHERE id = ?`, id); err != nil {
			return err
		}
	}
	for _, k := range keep {
		if _, err := tx.ExecContext(ctx, `UPDATE mailbox SET gid = ?, origin_received_at = received_at WHERE id = ?`, k.gid, k.id); err != nil {
			return err
		}
	}
	for _, q := range []string{
		`CREATE UNIQUE INDEX mailbox_gid ON mailbox (gid)`,
		`CREATE TABLE mailbox_tombstones (
			gid BLOB PRIMARY KEY,
			inbox BLOB NOT NULL,
			expires_at INTEGER NOT NULL
		)`,
		`CREATE INDEX mailbox_tombstones_expiry ON mailbox_tombstones (expires_at)`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// mailboxHas reports whether gid is held, or tombstoned by inbox's own
// token (within tx). A tombstone made with another token doesn't count:
// gid is computed from the item's inbox, so only that inbox's holder can
// delete it, and anyone else who learned the gid could otherwise
// pre-delete it.
func mailboxHas(ctx context.Context, q querier, gid, inbox []byte) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM mailbox WHERE gid = ?1 UNION ALL SELECT 1 FROM mailbox_tombstones WHERE gid = ?1 AND inbox = ?2 LIMIT 1`,
		gid, inbox).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// tombstoneMail deletes the item gid of inbox, if held, and records its
// tombstone until expires (clamped to what any copy can live), logging it
// for the peers. It reports whether the tombstone is new and the bytes
// freed. Callers hold s.mu, inside tx, and take the bytes freed off
// mailboxBytes once tx commits.
func (s *Server) tombstoneMail(ctx context.Context, tx *sql.Tx, gid, inbox []byte, expires int64) (bool, uint64, error) {
	now := s.now()
	expires = min(expires, now.Add(s.cfg.MailboxTTL.Duration+TombstoneSlack).UnixMilli())
	if expires <= now.UnixMilli() {
		return false, 0, nil
	}
	// One tombstone per gid. One made with another inbox's token gives way
	// to this inbox's only when this inbox holds the item (which proves the
	// gid is this inbox's: it is computed from the inbox); otherwise it
	// stays, and binds nothing for this inbox (mailboxHas).
	var other []byte
	err := tx.QueryRowContext(ctx, `SELECT inbox FROM mailbox_tombstones WHERE gid = ?`, gid).Scan(&other)
	switch {
	case err == nil && bytes.Equal(other, inbox):
		return false, 0, nil
	case err == nil:
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM mailbox WHERE gid = ? AND inbox = ?`, gid, inbox).Scan(&n); errors.Is(err, sql.ErrNoRows) {
			return false, 0, nil
		} else if err != nil {
			return false, 0, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM mailbox_tombstones WHERE gid = ?`, gid); err != nil {
			return false, 0, err
		}
	case !errors.Is(err, sql.ErrNoRows):
		return false, 0, err
	}
	rows, err := tx.QueryContext(ctx, `DELETE FROM mailbox WHERE gid = ? AND inbox = ? RETURNING size`, gid, inbox)
	if err != nil {
		return false, 0, err
	}
	var freed uint64
	for rows.Next() {
		var sz uint64
		if err := rows.Scan(&sz); err != nil {
			rows.Close()
			return false, 0, err
		}
		freed += sz
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO mailbox_tombstones (gid, inbox, expires_at) VALUES (?, ?, ?)`, gid, inbox, expires); err != nil {
		return false, 0, err
	}
	if err := logPeer(ctx, tx, logTombstone, gid, expires); err != nil {
		return false, 0, err
	}
	return true, freed, nil
}

// itemEntry is the log entry for an item still held.
func (s *Server) itemEntry(ctx context.Context, gid []byte) (*wire.PeerEntry, error) {
	it := &wire.PeerMailboxItem{}
	err := s.db.QueryRowContext(ctx, `SELECT inbox, blob, origin_received_at, expires_at FROM mailbox WHERE gid = ?`, gid).
		Scan(&it.Inbox, &it.Blob, &it.ReceivedAt, &it.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &wire.PeerEntry{Entry: &wire.PeerEntry_Item{Item: it}}, nil
}

// tombstoneEntry is the log entry for a tombstone still kept.
func (s *Server) tombstoneEntry(ctx context.Context, gid []byte) (*wire.PeerEntry, error) {
	ts := &wire.PeerMailboxTombstone{Gid: gid}
	err := s.db.QueryRowContext(ctx, `SELECT inbox, expires_at FROM mailbox_tombstones WHERE gid = ?`, gid).Scan(&ts.Inbox, &ts.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &wire.PeerEntry{Entry: &wire.PeerEntry_Tombstone{Tombstone: ts}}, nil
}

// Why a replicated entry was refused (openarcanum_meta_peer_refused_total).
const (
	refusedInvalid = "invalid"
	refusedExpired = "expired"
	refusedQuota   = "quota"
	refusedFull    = "store_full"
)

var refusedKinds = []string{refusedInvalid, refusedExpired, refusedQuota, refusedFull}

// applyItem stores a peer's item unless it is already held or tombstoned.
// An item that doesn't fit this node's quotas is refused here only (the
// cursor moves on; clients read the other members). It keeps its first
// expiry, never later than this node's own mailbox_ttl from now.
func (s *Server) applyItem(ctx context.Context, it *wire.PeerMailboxItem) (bool, error) {
	if len(it.Inbox) != 32 || len(it.Blob) == 0 || uint64(len(it.Blob)) > s.cfg.MailboxMaxItem {
		s.refused(refusedInvalid)
		return false, nil
	}
	now := s.now()
	expires := min(it.ExpiresAt, now.Add(s.cfg.MailboxTTL.Duration).UnixMilli())
	if expires <= now.UnixMilli() {
		s.refused(refusedExpired)
		return false, nil
	}
	gid := MailboxGID(it.Inbox, it.Blob)
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := beginWrite(ctx, s.db)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if has, err := mailboxHas(ctx, tx, gid, it.Inbox); err != nil || has {
		return false, err
	}
	var used sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT sum(size) FROM mailbox WHERE inbox = ?`, it.Inbox).Scan(&used); err != nil {
		return false, err
	}
	size := uint64(len(it.Blob))
	if uint64(used.Int64)+size > s.cfg.MailboxQuota {
		s.refused(refusedQuota)
		return false, nil
	}
	if s.mailboxBytes+size > s.cfg.MailboxStoreMax {
		s.refused(refusedFull)
		return false, nil
	}
	received := min(it.ReceivedAt, now.UnixMilli())
	if _, err := tx.ExecContext(ctx, `INSERT INTO mailbox (inbox, blob, size, received_at, expires_at, gid, origin_received_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		it.Inbox, it.Blob, size, now.UnixMilli(), expires, gid, received); err != nil {
		return false, err
	}
	if err := logPeer(ctx, tx, logItem, gid, expires); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	s.mailboxBytes += size
	return true, nil
}

// applyTombstone deletes a peer's deleted item here and keeps its
// tombstone (logged again, so members further away get it too).
func (s *Server) applyTombstone(ctx context.Context, ts *wire.PeerMailboxTombstone) (bool, error) {
	if len(ts.Gid) != sha256.Size || len(ts.Inbox) != 32 {
		s.refused(refusedInvalid)
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := beginWrite(ctx, s.db)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	added, freed, err := s.tombstoneMail(ctx, tx, ts.Gid, ts.Inbox, ts.ExpiresAt)
	if err != nil || !added {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	s.mailboxBytes -= min(freed, s.mailboxBytes)
	return true, nil
}
