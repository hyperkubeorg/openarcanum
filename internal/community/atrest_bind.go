package community

import (
	"bytes"
	"context"
)

// Binding plaintext columns to sealed data (docs/operations.md "Data at
// rest").
//
// A few columns stay in plaintext because SQL filters, orders or updates on
// them. Each one whose value decides who sees what is bound to its row's
// sealed data, and reads compare the two:
//
//   - messages.channel_id and messages.created_at: sealed inside the
//     message body (openBody; created_at decides what new members see and
//     what a ban's history deletion covers);
//   - files.channel_id and files.message_id: sealed inside the file
//     metadata (loadFileMeta; attachFile reseals it when a staged file is
//     posted);
//   - pins.channel_id: sealed inside the pin record (listPins, which also
//     checks the pinned message's own bound channel);
//   - reports.status and reports.month: sealed inside the report record,
//     and reports.dedupe recomputed from the sealed reporter and message
//     (loadReport, checkReportBinding);
//   - join_keys.secret_hash, uses, revoked, public: sealed again in
//     join_keys.bound (checkJoinKeyBinding);
//   - key_packages.device: the lookup hash of the device key sealed in the
//     package (getContactCard).
//
// A mismatch is a TamperError, so someone who can write the database file
// but lacks the storage key can't move a message or file into another
// channel, or un-revoke, reset or re-point a join key, without the server
// refusing and reporting it. What binding can't catch is replaying a whole
// older sealed value of the same row (or an older copy of the whole
// database): see docs/threat-model.md.

// joinKeyBinding is the sealed copy of a join key's plaintext columns.
type joinKeyBinding struct {
	SecretHash []byte `json:"secret_hash"`
	Uses       uint32 `json:"uses"`
	Revoked    bool   `json:"revoked"`
	Public     bool   `json:"public"`
}

func (s *Server) sealJoinKeyBinding(id int64, b joinKeyBinding) []byte {
	return s.sealJSON("join_keys", "bound", rowID(id), &b)
}

// checkJoinKeyBinding verifies a join key's plaintext columns against their
// sealed copy.
func (s *Server) checkJoinKeyBinding(k *joinKeyRow, bound []byte) error {
	var b joinKeyBinding
	if err := s.openJSON("join_keys", "bound", rowID(k.ID), bound, &b); err != nil {
		return err
	}
	if !bytes.Equal(b.SecretHash, k.SecretHash) || b.Uses != k.Uses || b.Revoked != k.Revoked || b.Public != k.Public {
		return &TamperError{"join_keys", "bound", rowID(k.ID)}
	}
	return nil
}

// writeJoinKeyState stores a join key's use count and revocation with a
// fresh binding.
func (s *Server) writeJoinKeyState(ctx context.Context, x execer, k *joinKeyRow) error {
	bound := s.sealJoinKeyBinding(k.ID, joinKeyBinding{SecretHash: k.SecretHash, Uses: k.Uses, Revoked: k.Revoked, Public: k.Public})
	_, err := x.ExecContext(ctx, `UPDATE join_keys SET uses = ?, revoked = ?, bound = ? WHERE id = ?`, k.Uses, boolInt(k.Revoked), bound, k.ID)
	return err
}

// revokeJoinKeys revokes the join keys a WHERE clause selects.
func (s *Server) revokeJoinKeys(ctx context.Context, x dbtx, where string, args ...any) error {
	keys, err := s.queryJoinKeys(ctx, x, where, args...)
	if err != nil {
		return err
	}
	for _, k := range keys {
		if k.Revoked {
			continue
		}
		k.Revoked = true
		if err := s.writeJoinKeyState(ctx, x, k); err != nil {
			return err
		}
	}
	return nil
}

// countJoinKeyUse records one more use of a join key.
func (s *Server) countJoinKeyUse(ctx context.Context, x dbtx, id int64) error {
	keys, err := s.queryJoinKeys(ctx, x, `WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return errNotFound("no such join key")
	}
	keys[0].Uses++
	return s.writeJoinKeyState(ctx, x, keys[0])
}
