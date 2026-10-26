package meta

import (
	"context"
	"errors"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/ops"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// What travels in the peer log, entry by entry (docs/protocol.md §7.5), and
// the per-peer intake limits that bound what a compromised member can push.

// peerLimits counts what one peer sent in the current windows.
type peerLimits struct {
	minute, day        time.Time // window starts
	items, tombs       int
	bytes              uint64
	throttledUntil     time.Time
	throttledLoggedFor time.Time
}

// throttled reports whether pulls from peer are paused by its limits.
func (s *Server) throttled(peer []byte, now time.Time) bool {
	s.peers.mu.Lock()
	defer s.peers.mu.Unlock()
	l := s.peers.limits[string(peer)]
	return l != nil && now.Before(l.throttledUntil)
}

// charge counts one entry from peer and reports whether it fits the
// peer's limits; when it doesn't, the peer is throttled until the window
// that ran out ends.
func (s *Server) charge(peer []byte, item, tomb bool, size uint64, now time.Time) bool {
	s.peers.mu.Lock()
	defer s.peers.mu.Unlock()
	l := s.peers.limits[string(peer)]
	if l == nil {
		l = &peerLimits{minute: now, day: now}
		s.peers.limits[string(peer)] = l
	}
	if now.Sub(l.minute) >= time.Minute {
		l.minute, l.items, l.tombs = now, 0, 0
	}
	if now.Sub(l.day) >= 24*time.Hour {
		l.day, l.bytes = now, 0
	}
	switch {
	case item && l.items+1 > s.cfg.PeerItemsPerMinute,
		tomb && l.tombs+1 > s.cfg.PeerTombstonesPerMinute:
		l.throttledUntil = l.minute.Add(time.Minute)
	case l.bytes+size > s.cfg.PeerBytesPerDay:
		l.throttledUntil = l.day.Add(24 * time.Hour)
	default:
		if item {
			l.items++
		}
		if tomb {
			l.tombs++
		}
		l.bytes += size
		return true
	}
	if l.throttledLoggedFor != l.throttledUntil {
		l.throttledLoggedFor = l.throttledUntil
		s.log.Warn("peer throttled: it sent more than its limits allow (peer_items_per_minute, peer_tombstones_per_minute, peer_bytes_per_day); "+
			"pulls from it pause until the window ends", "peer", identity.Fingerprint(peer), "until", l.throttledUntil.UTC().Format(time.RFC3339))
	}
	return false
}

// peerEntry builds the entry a log row names, as it is now (nil: gone).
func (s *Server) peerEntry(ctx context.Context, kind string, key []byte) (*wire.PeerEntry, error) {
	switch kind {
	case logItem:
		return s.itemEntry(ctx, key)
	case logTombstone:
		return s.tombstoneEntry(ctx, key)
	}
	return s.peerEntryMore(ctx, kind, key)
}

// applyPeerBatch applies a peer's batch in order and returns the cursor to
// save and whether to stop pulling until the next round (the peer ran into its limits:
// the cursor is then the last entry applied, so nothing is skipped).
func (s *Server) applyPeerBatch(ctx context.Context, l *peerLink, b *wire.PeerBatch) (uint64, bool, error) {
	done := b.Cursor
	if len(b.Entries) > 0 {
		done = b.Entries[0].Mrev - 1
	}
	changed := false
	defer func() {
		if changed {
			// Members further away pull it from here.
			s.notifyPeers()
		}
	}()
	for _, e := range b.Entries {
		now := s.now()
		var err error
		var ok bool
		switch x := e.Entry.(type) {
		case *wire.PeerEntry_Item:
			if !s.charge(l.key, true, false, uint64(len(x.Item.Blob)), now) {
				s.recordThrottle(ctx, l.key)
				return done, true, nil
			}
			ok, err = s.applyItem(ctx, x.Item)
		case *wire.PeerEntry_Tombstone:
			if !s.charge(l.key, false, true, 0, now) {
				s.recordThrottle(ctx, l.key)
				return done, true, nil
			}
			ok, err = s.applyTombstone(ctx, x.Tombstone)
		default:
			ok, err = s.applyEntryMore(ctx, l, e)
			if errors.Is(err, errThrottled) {
				s.recordThrottle(ctx, l.key)
				return done, true, nil
			}
		}
		if err != nil {
			return done, true, err
		}
		changed = changed || ok
		done = e.Mrev
	}
	return b.Cursor, false, nil
}

// recordThrottle keeps the throttle in peer_cursors for `meta peers status`.
func (s *Server) recordThrottle(ctx context.Context, peer []byte) {
	s.peers.mu.Lock()
	until := s.peers.limits[string(peer)].throttledUntil
	s.peers.mu.Unlock()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO peer_cursors (peer_key, throttled_until) VALUES (?1, ?2)
ON CONFLICT (peer_key) DO UPDATE SET throttled_until = ?2`, peer, until.UnixMilli()); err != nil {
		s.log.Error("peer status", "err", err)
	}
}

// refused counts a replicated entry this node didn't store.
func (s *Server) refused(kind string) {
	if v := s.refusedVec.Load(); v != nil {
		v.With(kind).Inc()
	}
}

// registerPeerMetrics adds the cluster metrics.
func (s *Server) registerPeerMetrics(reg *ops.Registry) {
	s.refusedVec.Store(reg.CounterVec("openarcanum_meta_peer_refused_total",
		"Entries from cluster peers this node didn't store (invalid, expired, over a quota, store full).",
		ops.Labels{Names: []string{"kind"}, Values: [][]string{refusedKinds}}))
	peerQuery := func(q string) func() float64 {
		return func() float64 {
			var v float64
			if err := s.db.QueryRow(q, s.now().UnixMilli()).Scan(&v); err != nil {
				return 0
			}
			return v
		}
	}
	reg.GaugeFunc("openarcanum_meta_peer_lag", "The largest number of log entries this node is behind a cluster peer.",
		peerQuery(`SELECT coalesce(max(head - cursor), 0) FROM peer_cursors WHERE ?1 = ?1`))
	reg.GaugeFunc("openarcanum_meta_peer_throttled", "Cluster peers whose pulls are paused by their limits.",
		peerQuery(`SELECT count(*) FROM peer_cursors WHERE throttled_until > ?1`))
	reg.GaugeFunc("openarcanum_meta_peer_failing", "Cluster peers whose last pull failed.",
		peerQuery(`SELECT count(*) FROM peer_cursors WHERE last_error != '' AND ?1 = ?1`))
}

// peerEntryMore builds the entries of later kinds (nil: unknown or gone).
func (s *Server) peerEntryMore(ctx context.Context, kind string, key []byte) (*wire.PeerEntry, error) {
	switch kind {
	case logBlobMeta:
		return s.blobMetaEntry(ctx, key)
	case logBlobRelease:
		return s.blobReleaseEntry(ctx, key)
	case logDecision:
		return s.decisionEntry(ctx, key)
	case logCommunityTombstone:
		return s.communityTombstoneEntry(ctx, key)
	}
	return nil, nil
}

// applyEntryMore applies the entries of later kinds and reports whether
// anything changed.
func (s *Server) applyEntryMore(ctx context.Context, l *peerLink, e *wire.PeerEntry) (bool, error) {
	now := s.now()
	switch x := e.Entry.(type) {
	case *wire.PeerEntry_BlobMeta:
		if !s.charge(l.key, true, false, 0, now) {
			return false, errThrottled
		}
		return s.applyBlobMeta(ctx, x.BlobMeta)
	case *wire.PeerEntry_BlobRelease:
		if !s.charge(l.key, false, true, 0, now) {
			return false, errThrottled
		}
		return s.applyBlobRelease(ctx, x.BlobRelease)
	case *wire.PeerEntry_Decision:
		if !s.charge(l.key, true, false, 0, now) {
			return false, errThrottled
		}
		return s.applyDecision(ctx, x.Decision)
	case *wire.PeerEntry_CommunityTombstone:
		if !s.charge(l.key, false, true, 0, now) {
			return false, errThrottled
		}
		return s.applyCommunityTombstone(ctx, x.CommunityTombstone)
	}
	return false, nil
}

// errThrottled stops a batch at an entry over the peer's limits.
var errThrottled = errors.New("peer throttled")

// handlePeerMore answers the requests other than pulls (nil: unknown).
func (s *Server) handlePeerMore(ctx context.Context, peer []byte, m *wire.PeerMessage) *wire.PeerMessage {
	if r := m.GetBlobChunkRequest(); r != nil {
		return s.peerBlobChunk(ctx, r)
	}
	return nil
}

func (s *Server) nodeInfo(ctx context.Context) *wire.NodeInfo {
	return &wire.NodeInfo{NodeKey: s.id.key.Public, NoiseKey: s.id.noise.Public, Directory: s.directoryOn(ctx)}
}
