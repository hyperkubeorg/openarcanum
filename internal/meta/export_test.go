package meta

import (
	"context"

	"github.com/hyperkubeorg/openarcanum/internal/store"
)

// ResetReplicationCursors makes the next ReplicateOnce pull everything from
// every peer again, as a newly added peer would (tests).
func (s *Server) ResetReplicationCursors(ctx context.Context) {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM counters WHERE k LIKE 'cursor:%'`); err != nil {
		panic(err)
	}
}

// ResetPeerCursors makes the next peer round pull every trusted peer's log
// from the start (tests).
func (s *Server) ResetPeerCursors(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE peer_cursors SET cursor = 0`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CopyRemoteBlobs runs the background blob copy now (tests).
func (s *Server) CopyRemoteBlobs(ctx context.Context) { s.copyRemoteBlobs(ctx) }

// SchemaVersion is the schema version this build uses (tests).
func SchemaVersion() int { return len(migrations) }

// Migrations are this build's schema steps (tests).
func Migrations() []store.Migration { return migrations }
