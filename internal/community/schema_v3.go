package community

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hyperkubeorg/openarcanum/internal/store"
)

// migrateV3 converts co-owners to owner invitations. A co-owner outranked
// everyone but held no owner key, so it can't become an owner by itself:
// its client must submit an owner key and a current owner must sign it into
// the owner set (owners.go). Until then the member keeps its roles.
func (s *Server) migrateV3(ctx context.Context, tx store.Tx) error {
	// Check the storage key first, so a wrong key reports as one and leaves
	// the database at version 2.
	var check []byte
	if err := tx.QueryRowContext(ctx, `SELECT v FROM meta WHERE k = 'key_check'`).Scan(&check); err != nil {
		return fmt.Errorf("database has no storage key check: %w", err)
	}
	if pt, err := s.open("meta", "v", "key_check", check); err != nil || string(pt) != keyCheckPlaintext {
		return errWrongStorageKey
	}
	type row struct{ kid, data []byte }
	var rows []row
	rs, err := tx.QueryContext(ctx, `SELECT kid, data FROM members`)
	if err != nil {
		return err
	}
	for rs.Next() {
		var r row
		if err := rs.Scan(&r.kid, &r.data); err != nil {
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
		pt, err := s.open("members", "data", rowKey(r.kid), r.data)
		if err != nil {
			return err
		}
		var old v2MemberRec
		if err := json.Unmarshal(pt, &old); err != nil {
			return errors.Join(&TamperError{"members", "data", rowKey(r.kid)}, err)
		}
		if !old.CoOwner {
			continue
		}
		m := old.memberRec
		m.OwnerInvited = true
		if _, err := tx.ExecContext(ctx, `UPDATE members SET data = ? WHERE kid = ?`, s.sealJSON("members", "data", rowKey(r.kid), &m), r.kid); err != nil {
			return err
		}
	}
	return nil
}
