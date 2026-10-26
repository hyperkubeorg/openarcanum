package meta

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Community status and reaping (docs/protocol.md §7.4).
//
// A community server reports to every maintainer node it publishes to: a
// heartbeat signed by its server key every heartbeat_interval, checked
// against the server key of the route stored for the same route id (so a
// heartbeat needs the route posted first), and replicated like routes. From
// the newest heartbeat and route the node tells clients whether the
// community is online, degraded (fewer relays than it wants), offline, or
// terminated.
//
// A community whose server has signed nothing (no heartbeat, no route) for
// longer than community_reap_after is terminated: Expire drops its route,
// listing and status and keeps a tombstone instead. Inside a maintainer
// cluster the tombstone also goes to the other members (community_repl.go).
// While the tombstone lasts (community_tombstone_ttl), the node refuses the
// community's listing and heartbeats, and its route unless the route carries
// a reregistered_at later than the last time the node saw the server: an
// explicit re-registration by the operator (`community reregister`), never a
// stale copy from another maintainer. Independently of tombstones, every
// node refuses a route, listing or heartbeat signed longer than
// community_reap_after ago, so replication can't bring a reaped community
// back from a peer that still holds an old copy.

// communityStatusSchema (schema version 9) adds the heartbeat store and
// the tombstones, and the signed publish times reaping reads (0 for rows
// stored before: their local receipt time stands in until the next
// publication).
const communityStatusSchema = `
ALTER TABLE routes ADD COLUMN published_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE listings ADD COLUMN published_at INTEGER NOT NULL DEFAULT 0;
CREATE TABLE community_status (
	route_id BLOB PRIMARY KEY,
	heartbeat BLOB NOT NULL,
	at INTEGER NOT NULL,
	rev INTEGER NOT NULL
);
CREATE TABLE community_tombstones (
	route_id BLOB PRIMARY KEY,
	community BLOB NOT NULL,
	last_seen INTEGER NOT NULL,
	reaped_at INTEGER NOT NULL
);
CREATE INDEX community_tombstones_community ON community_tombstones (community);
CREATE INDEX routes_owner ON routes (owner);
`

// The time a route or listing row was published: its signed published_at,
// or, for a row without one, when this node stored it.
const (
	routeSeenSQL   = `(CASE WHEN r.published_at > 0 THEN r.published_at ELSE r.updated_at END)`
	listingSeenSQL = `(CASE WHEN l.published_at > 0 THEN l.published_at ELSE l.updated_at END)`
)

// heartbeatAhead bounds how far a heartbeat's time may be ahead of this
// node's clock.
const heartbeatAhead = time.Minute

func verifyCommunityHeartbeat(sh *wire.SignedCommunityHeartbeat) (*wire.CommunityHeartbeat, error) {
	var hb wire.CommunityHeartbeat
	if err := proto.Unmarshal(sh.Heartbeat, &hb); err != nil {
		return nil, bad("malformed heartbeat")
	}
	if len(hb.RouteId) < 16 || len(hb.RouteId) > 32 || !identity.ValidPublic(hb.ServerKey) || hb.RelaysWanted > 64 || hb.RelaysAttached > 64 {
		return nil, bad("heartbeat fields are out of range")
	}
	if err := identity.Verify(hb.ServerKey, identity.CtxCommunityHeartbeat, sh.Heartbeat, sh.Signature); err != nil {
		return nil, forbidden("heartbeat signature is invalid")
	}
	return &hb, nil
}

func (s *Server) postCommunityHeartbeat(r *http.Request, body []byte) (proto.Message, error) {
	var sh wire.SignedCommunityHeartbeat
	if err := unmarshalKnown(body, &sh); err != nil {
		return nil, bad("malformed heartbeat")
	}
	hb, err := verifyCommunityHeartbeat(&sh)
	if err != nil {
		return nil, err
	}
	raw, err := encodeKnown(&sh, MaxHeartbeatBytes, "heartbeat")
	if err != nil {
		return nil, err
	}
	return nil, s.storeCommunityHeartbeat(r.Context(), hb, raw)
}

// stale refuses something signed at `at` (unix ms) longer than
// community_reap_after ago. An unknown time (0, from servers before
// published_at existed) is stale too: such a copy could otherwise be
// replayed forever, bringing back a community reaped long ago.
func (s *Server) stale(at int64, what string) error {
	if at <= 0 || at < s.now().Add(-s.cfg.CommunityReapAfter.Duration).UnixMilli() {
		return gone("this " + what + " is older than this node's community_reap_after")
	}
	return nil
}

// terminatedMsg is the refusal a reaped community's server gets; the server
// recognises a 410 on its route or heartbeat as having been reaped.
func (s *Server) terminatedMsg() error {
	return gone(fmt.Sprintf("terminated: this community's server reported nothing for over %s, so this maintainer shut it down; "+
		"its operator can bring it back with `community reregister`", s.cfg.CommunityReapAfter.Duration))
}

func (s *Server) storeCommunityHeartbeat(ctx context.Context, hb *wire.CommunityHeartbeat, raw []byte) error {
	if hb.At > s.now().Add(heartbeatAhead).UnixMilli() {
		return bad("heartbeat is from the future")
	}
	if err := s.stale(hb.At, "heartbeat"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner, signed []byte
	err = tx.QueryRowContext(ctx, `SELECT owner, signed FROM routes WHERE route_id = ?`, hb.RouteId).Scan(&owner, &signed)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if dead, err := tombstoned(ctx, tx, `route_id`, hb.RouteId); err != nil {
			return err
		} else if dead {
			return s.terminatedMsg()
		}
		return notFound("no route for this community on this node; post the route first")
	case err != nil:
		return err
	}
	var sr wire.SignedRoute
	var rt wire.Route
	if proto.Unmarshal(signed, &sr) != nil || proto.Unmarshal(sr.Route, &rt) != nil {
		return notFound("no usable route for this community on this node; post the route again")
	}
	if !bytes.Equal(rt.ServerKey, hb.ServerKey) {
		return conflict("the stored route names another server key; post the current route first")
	}
	if delisted, err := s.communityDelisted(ctx, tx, owner); err != nil {
		return err
	} else if delisted {
		return forbidden("this community has been delisted by this maintainer")
	}
	var at int64
	err = tx.QueryRowContext(ctx, `SELECT at FROM community_status WHERE route_id = ?`, hb.RouteId).Scan(&at)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && hb.At <= at {
		return nil // a copy, or older than what's stored
	}
	rev, err := s.nextRev(ctx, tx)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO community_status (route_id, heartbeat, at, rev) VALUES (?, ?, ?, ?)
ON CONFLICT (route_id) DO UPDATE SET heartbeat = excluded.heartbeat, at = excluded.at, rev = excluded.rev`,
		hb.RouteId, raw, hb.At, rev); err != nil {
		return err
	}
	return tx.Commit()
}

// tombstoned reports whether a tombstone exists with column = key.
func tombstoned(ctx context.Context, q querier, column string, key []byte) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM community_tombstones WHERE `+column+` = ? LIMIT 1`, key).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// routeTombstone checks a route against the tombstone for its id (callers
// hold s.mu, inside tx): refused unless it re-registers the community after
// the last time this node saw the server, in which case the tombstone goes.
// The tombstone keeps the community the route id belonged to, and only that
// community (a route certified through its owner chain) may re-register it:
// any other community could otherwise take over a reaped route id, and the
// real owners' re-registration would then be refused as another
// community's route. A tombstone without a community (one from a cluster
// member for a route this node never held, community_repl.go) binds nothing.
func (s *Server) routeTombstone(ctx context.Context, tx *sql.Tx, rt *wire.Route, community []byte) error {
	var lastSeen int64
	var owner []byte
	err := tx.QueryRowContext(ctx, `SELECT last_seen, community FROM community_tombstones WHERE route_id = ?`, rt.RouteId).Scan(&lastSeen, &owner)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(owner) > 0 && !bytes.Equal(owner, community) {
		return forbidden("this route id belongs to another community")
	}
	if rt.ReregisteredAt <= lastSeen {
		return s.terminatedMsg()
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM community_tombstones WHERE route_id = ?`, rt.RouteId)
	if err == nil {
		s.log.Info("reaped community re-registered", "route", shortID(rt.RouteId))
	}
	return err
}

// ---------------------------------------------------------------- status for clients

func (s *Server) getCommunityStatus(w http.ResponseWriter, r *http.Request) {
	id, err := identity.Decode(r.PathValue("id"))
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	st, err := s.communityStatus(r.Context(), id)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if st == nil {
		http.Error(w, "unknown community", http.StatusNotFound)
		return
	}
	writeProto(w, st)
}

// communityStatus is what this node knows about route id's community (nil:
// nothing).
func (s *Server) communityStatus(ctx context.Context, routeID []byte) (*wire.CommunityStatus, error) {
	out := &wire.CommunityStatus{RouteId: routeID, ReapAfterSeconds: uint64(s.cfg.CommunityReapAfter.Duration / time.Second)}
	var routeSeen int64
	err := s.db.QueryRowContext(ctx, `SELECT `+routeSeenSQL+` FROM routes r WHERE r.route_id = ?`, routeID).Scan(&routeSeen)
	if errors.Is(err, sql.ErrNoRows) {
		err = s.db.QueryRowContext(ctx, `SELECT last_seen, reaped_at FROM community_tombstones WHERE route_id = ?`, routeID).Scan(&out.LastSeen, &out.ReapedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		out.State = wire.CommunityState_COMMUNITY_STATE_TERMINATED
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	out.LastSeen = routeSeen
	var raw []byte
	err = s.db.QueryRowContext(ctx, `SELECT heartbeat FROM community_status WHERE route_id = ?`, routeID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil // no report yet: UNSPECIFIED
	}
	if err != nil {
		return nil, err
	}
	var sh wire.SignedCommunityHeartbeat
	var hb wire.CommunityHeartbeat
	if unmarshalKnown(raw, &sh) != nil || proto.Unmarshal(sh.Heartbeat, &hb) != nil {
		return out, nil
	}
	out.Heartbeat = &sh
	out.LastSeen = max(out.LastSeen, hb.At)
	out.State = heartbeatState(&hb, s.now(), s.cfg.CommunityOfflineAfter.Duration)
	return out, nil
}

// heartbeatState is a community's state from its newest heartbeat.
func heartbeatState(hb *wire.CommunityHeartbeat, now time.Time, offlineAfter time.Duration) wire.CommunityState {
	switch {
	case hb.Stopping, now.Sub(time.UnixMilli(hb.At)) > offlineAfter, hb.RelaysAttached == 0:
		return wire.CommunityState_COMMUNITY_STATE_OFFLINE
	case hb.RelaysAttached < hb.RelaysWanted:
		return wire.CommunityState_COMMUNITY_STATE_DEGRADED
	default:
		return wire.CommunityState_COMMUNITY_STATE_ONLINE
	}
}

// listingStatuses adds the status of each listed community that has one
// (search results).
func (s *Server) listingStatuses(ctx context.Context, listings []*wire.SignedListing) []*wire.CommunityStatus {
	var out []*wire.CommunityStatus
	seen := map[string]bool{}
	for _, sl := range listings {
		var l wire.Listing
		if proto.Unmarshal(sl.Listing, &l) != nil || len(l.RouteId) == 0 || seen[string(l.RouteId)] {
			continue
		}
		seen[string(l.RouteId)] = true
		if st, err := s.communityStatus(ctx, l.RouteId); err == nil && st != nil {
			out = append(out, st)
		}
	}
	return out
}

// ---------------------------------------------------------------- reaping

// reap terminates every community whose server signed nothing for longer
// than community_reap_after, drops listings left without a route that are
// as old, and forgets tombstones past community_tombstone_ttl. Expire runs
// it. It returns how many communities it terminated.
func (s *Server) reap(ctx context.Context) (int, error) {
	now := s.now()
	cutoff := now.Add(-s.cfg.CommunityReapAfter.Duration).UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	type dead struct {
		route, owner []byte
		seen         int64
	}
	rows, err := tx.QueryContext(ctx, `SELECT r.route_id, r.owner, max(`+routeSeenSQL+`, coalesce(c.at, 0)) AS seen
FROM routes r LEFT JOIN community_status c ON c.route_id = r.route_id WHERE seen < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	var reaped []dead
	for rows.Next() {
		var d dead
		if err := rows.Scan(&d.route, &d.owner, &d.seen); err != nil {
			rows.Close()
			return 0, err
		}
		reaped = append(reaped, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, d := range reaped {
		if _, err := deleteCommunity(ctx, tx, d.owner); err != nil {
			return 0, err
		}
		for _, q := range []string{`DELETE FROM routes WHERE route_id = ?`, `DELETE FROM community_status WHERE route_id = ?`} {
			if _, err := tx.ExecContext(ctx, q, d.route); err != nil {
				return 0, err
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO community_tombstones (route_id, community, last_seen, reaped_at) VALUES (?, ?, ?, ?)
ON CONFLICT (route_id) DO UPDATE SET community = excluded.community, last_seen = excluded.last_seen, reaped_at = excluded.reaped_at`,
			d.route, d.owner, d.seen, now.UnixMilli()); err != nil {
			return 0, err
		}
		// Cluster members get the tombstone (community_repl.go).
		if err := logPeer(ctx, tx, logCommunityTombstone, d.route, now.Add(s.cfg.CommunityTombstoneTTL.Duration).UnixMilli()); err != nil {
			return 0, err
		}
	}
	// Listings whose community has no route here, as old as a reaped one.
	lrows, err := tx.QueryContext(ctx, `SELECT l.community FROM listings l
WHERE `+listingSeenSQL+` < ? AND NOT EXISTS (SELECT 1 FROM routes r WHERE r.owner = l.community)`, cutoff)
	if err != nil {
		return 0, err
	}
	var orphans [][]byte
	for lrows.Next() {
		var c []byte
		if err := lrows.Scan(&c); err != nil {
			lrows.Close()
			return 0, err
		}
		orphans = append(orphans, c)
	}
	lrows.Close()
	for _, c := range orphans {
		if _, err := deleteCommunity(ctx, tx, c); err != nil {
			return 0, err
		}
	}
	// Status rows left without a route (a delisted community), as old.
	if _, err := tx.ExecContext(ctx, `DELETE FROM community_status WHERE at < ? AND route_id NOT IN (SELECT route_id FROM routes)`, cutoff); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM community_tombstones WHERE reaped_at < ?`,
		now.Add(-s.cfg.CommunityTombstoneTTL.Duration).UnixMilli()); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if len(reaped) > 0 {
		s.notifyPeers()
	}
	for _, d := range reaped {
		s.log.Info("community reaped: its server reported nothing for longer than community_reap_after",
			"route", shortID(d.route), "last_seen", time.UnixMilli(d.seen).UTC().Format(time.RFC3339))
	}
	return len(reaped), nil
}

// shortID is the start of an id for logs.
func shortID(id []byte) string {
	e := identity.Encode(id)
	if len(e) > 8 {
		return e[:8]
	}
	return e
}

// expireEvery is how often Run calls Expire: every minute, or more often
// when the community windows are shorter than a few minutes (tests).
func (c *Config) expireEvery() time.Duration {
	return max(time.Second, min(time.Minute, c.CommunityOfflineAfter.Duration/4, c.CommunityReapAfter.Duration/4))
}
