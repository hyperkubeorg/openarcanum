package meta

// Community tombstones inside a maintainer cluster (docs/protocol.md
// §7.4, §7.5). Every member reaps by the signed times it holds, which
// replication keeps the same everywhere; but a member that was down while
// the others reaped a community still holds its route, and would serve it
// as alive. Members therefore pass their tombstones to each other: the
// receiver drops the community too, unless it has seen the server sign
// something after the tombstone's last_seen (the server came back, or the
// route re-registered it), and keeps the union of tombstones (the later
// last_seen and reaped_at per route id).

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

const logCommunityTombstone = "k"

func (s *Server) communityTombstoneEntry(ctx context.Context, routeID []byte) (*wire.PeerEntry, error) {
	ts := &wire.PeerCommunityTombstone{RouteId: routeID}
	err := s.db.QueryRowContext(ctx, `SELECT community, last_seen, reaped_at FROM community_tombstones WHERE route_id = ?`, routeID).
		Scan(&ts.Community, &ts.LastSeen, &ts.ReapedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &wire.PeerEntry{Entry: &wire.PeerEntry_CommunityTombstone{CommunityTombstone: ts}}, nil
}

// applyCommunityTombstone merges a member's tombstone and drops the
// community here unless this node saw it alive after last_seen. A member
// can claim any last_seen, so the tombstone counts only where this node's
// own rule agrees: last_seen older than this node's community_reap_after
// (a tombstone dated now or in the future drops nothing), and the
// community must be the one this node's route for the id belongs to. For a
// route id this node doesn't hold, the tombstone is kept without a
// community, so it can't block that community's listing or re-registration
// here.
func (s *Server) applyCommunityTombstone(ctx context.Context, ts *wire.PeerCommunityTombstone) (bool, error) {
	now := s.now()
	switch {
	case len(ts.RouteId) < 16 || len(ts.RouteId) > 32 || (len(ts.Community) > 0 && !identity.ValidPublic(ts.Community)),
		ts.LastSeen <= 0 || ts.ReapedAt < ts.LastSeen || ts.ReapedAt > now.Add(MaxSeqAhead).UnixMilli():
		s.refused(refusedInvalid)
		return false, nil
	case ts.ReapedAt <= now.Add(-s.cfg.CommunityTombstoneTTL.Duration).UnixMilli():
		return false, nil // forgotten here already
	case ts.LastSeen >= now.Add(-s.cfg.CommunityReapAfter.Duration).UnixMilli():
		return false, nil // not dead by this node's rule (yet)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := beginWrite(ctx, s.db)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var lastSeen, reapedAt int64
	err = tx.QueryRowContext(ctx, `SELECT last_seen, reaped_at FROM community_tombstones WHERE route_id = ?`, ts.RouteId).Scan(&lastSeen, &reapedAt)
	switch {
	case err == nil:
		if ts.LastSeen <= lastSeen && ts.ReapedAt <= reapedAt {
			return false, nil
		}
	case !errors.Is(err, sql.ErrNoRows):
		return false, err
	}
	// Seen alive here after the member last saw it: the member reaped too
	// early for this node (it missed the later reports); keep it.
	alive, err := s.seenAfter(ctx, tx, ts.RouteId, ts.LastSeen)
	if err != nil || alive {
		return false, err
	}
	var owner []byte
	err = tx.QueryRowContext(ctx, `SELECT owner FROM routes WHERE route_id = ?`, ts.RouteId).Scan(&owner)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	community := []byte{}
	if owner != nil {
		if !bytes.Equal(owner, ts.Community) {
			s.refused(refusedInvalid)
			return false, nil
		}
		community = owner
		if _, err := deleteCommunity(ctx, tx, owner); err != nil {
			return false, err
		}
	} else if err := tx.QueryRowContext(ctx, `SELECT community FROM community_tombstones WHERE route_id = ?`, ts.RouteId).Scan(&community); err != nil &&
		!errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	for _, q := range []string{`DELETE FROM routes WHERE route_id = ?`, `DELETE FROM community_status WHERE route_id = ?`} {
		if _, err := tx.ExecContext(ctx, q, ts.RouteId); err != nil {
			return false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO community_tombstones (route_id, community, last_seen, reaped_at) VALUES (?, ?, ?, ?)
ON CONFLICT (route_id) DO UPDATE SET community = excluded.community, last_seen = max(last_seen, excluded.last_seen), reaped_at = max(reaped_at, excluded.reaped_at)`,
		ts.RouteId, community, ts.LastSeen, ts.ReapedAt); err != nil {
		return false, err
	}
	if err := logPeer(ctx, tx, logCommunityTombstone, ts.RouteId, time.UnixMilli(ts.ReapedAt).Add(s.cfg.CommunityTombstoneTTL.Duration).UnixMilli()); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	if owner != nil {
		s.log.Info("community reaped by a cluster member; dropped here too", "route", shortID(ts.RouteId))
	}
	return true, nil
}

// seenAfter reports whether this node holds a sign of life from route
// id's server later than t: its route's publish time or re-registration,
// or a heartbeat.
func (s *Server) seenAfter(ctx context.Context, tx *sql.Tx, routeID []byte, t int64) (bool, error) {
	var signed []byte
	var seen int64
	err := tx.QueryRowContext(ctx, `SELECT r.signed, max(`+routeSeenSQL+`, coalesce(c.at, 0)) FROM routes r
LEFT JOIN community_status c ON c.route_id = r.route_id WHERE r.route_id = ?`, routeID).Scan(&signed, &seen)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if seen > t {
		return true, nil
	}
	var sr wire.SignedRoute
	var rt wire.Route
	if proto.Unmarshal(signed, &sr) == nil && proto.Unmarshal(sr.Route, &rt) == nil && rt.ReregisteredAt > t {
		return true, nil
	}
	return false, nil
}
