package meta

import (
	"context"
	"errors"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/ops"
)

// storeStatsMaxAge is how long one set of database counts serves scrapes,
// so frequent scrapes don't each scan the mailbox and blob tables.
const storeStatsMaxAge = 15 * time.Second

// storeStats are table counts for the metrics endpoint: sizes of the
// directory, registry, mailboxes and blob store, never their contents.
type storeStats struct {
	mu   sync.Mutex
	at   time.Time
	vals map[string]float64
}

// storeQueries count rows; a query with a window takes one argument, the
// cutoff (Unix milliseconds) that far back. Activity is counted from the
// network's own records (heartbeats, mailboxes), never per person or per
// community: "are people using it, and how much".
var storeQueries = []struct {
	name, help, query string
	window            time.Duration
}{
	{"openarcanum_meta_listings", "Directory listings (not withdrawn).", `SELECT count(*) FROM listings WHERE withdrawn = 0`, 0},
	{"openarcanum_meta_routes", "Private community routes.", `SELECT count(*) FROM routes`, 0},
	{"openarcanum_meta_relays", "Relays in the registry.", `SELECT count(*) FROM relays`, 0},
	{"openarcanum_meta_mailbox_items", "Items waiting in DM mailboxes.", `SELECT count(*) FROM mailbox`, 0},
	{"openarcanum_meta_mailbox_bytes", "Bytes waiting in DM mailboxes.", `SELECT coalesce(sum(size), 0) FROM mailbox`, 0},
	{"openarcanum_meta_nameplates", "Open device-link nameplates.", `SELECT count(*) FROM nameplates`, 0},
	{"openarcanum_meta_blobs_stored", "Complete DM blobs in the blob store.", `SELECT count(*) FROM blobs WHERE complete = 1 AND remote = 0`, 0},
	{"openarcanum_meta_blobs_stored_bytes", "Bytes of complete DM blobs.", `SELECT coalesce(sum(size), 0) FROM blobs WHERE complete = 1 AND remote = 0`, 0},
	{"openarcanum_meta_blobs_remote", "DM blob records from cluster peers whose content isn't (all) here yet.", `SELECT count(*) FROM blobs WHERE remote = 1`, 0},
	{"openarcanum_meta_blobs_uploading", "DM blob uploads in progress.", `SELECT count(*) FROM blobs WHERE complete = 0`, 0},
	{"openarcanum_meta_blobs_uploading_bytes", "Bytes reserved by DM blob uploads in progress.", `SELECT coalesce(sum(size), 0) FROM blobs WHERE complete = 0`, 0},
	// Activity: communities whose servers checked in (heartbeats come every
	// few minutes while a server runs), relays alive and how loaded they say
	// they are (from their heartbeats: the network-wide sum, for scaling),
	// and DM traffic waiting.
	{"openarcanum_meta_communities_active_1h", "Communities whose server sent a heartbeat in the last hour.", `SELECT count(*) FROM community_status WHERE at >= ?`, time.Hour},
	{"openarcanum_meta_communities_active_24h", "Communities whose server sent a heartbeat in the last 24 hours.", `SELECT count(*) FROM community_status WHERE at >= ?`, 24 * time.Hour},
	{"openarcanum_meta_communities_active_7d", "Communities whose server sent a heartbeat in the last 7 days.", `SELECT count(*) FROM community_status WHERE at >= ?`, 7 * 24 * time.Hour},
	{"openarcanum_meta_relays_alive", "Relays whose last heartbeat is under 2 minutes old (relay_ttl).", `SELECT count(*) FROM relays WHERE at >= ?`, 2 * time.Minute},
	{"openarcanum_meta_relay_connections", "Client connections across the relays alive, as their heartbeats report.", `SELECT coalesce(sum(connections), 0) FROM relays WHERE at >= ?`, 2 * time.Minute},
	{"openarcanum_meta_relay_capacity", "Connection capacity across the relays alive, as their heartbeats report.", `SELECT coalesce(sum(capacity), 0) FROM relays WHERE at >= ?`, 2 * time.Minute},
	{"openarcanum_meta_contact_cards", "Published contact cards (not expired).", `SELECT count(*) FROM contact_cards WHERE expires_at >= ?`, -1},
	{"openarcanum_meta_mailbox_items_24h", "DM mailbox items received in the last 24 hours (still waiting).", `SELECT count(*) FROM mailbox WHERE received_at >= ?`, 24 * time.Hour},
}

func (s *Server) storeValue(name string) float64 {
	st := &s.stats
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.vals == nil || s.now().Sub(st.at) >= storeStatsMaxAge || s.now().Before(st.at) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		vals := make(map[string]float64, len(storeQueries))
		for _, q := range storeQueries {
			var v float64
			var args []any
			switch {
			case q.window > 0:
				args = []any{s.now().Add(-q.window).UnixMilli()}
			case q.window < 0: // now: an expiry still ahead
				args = []any{s.now().UnixMilli()}
			}
			if err := s.db.QueryRowContext(ctx, q.query, args...).Scan(&v); err != nil {
				s.log.Warn("metrics: database count failed", "metric", q.name, "err", err)
				v = math.NaN()
			}
			vals[q.name] = v
		}
		st.vals, st.at = vals, s.now()
	}
	return st.vals[name]
}

// RegisterMetrics adds the node's metrics to reg and returns the HTTP API
// instrumented with per-route request counts and durations
// (docs/operations.md "Metrics").
func (s *Server) RegisterMetrics(reg *ops.Registry) http.Handler {
	for _, q := range storeQueries {
		reg.GaugeFunc(q.name, q.help, func() float64 { return s.storeValue(q.name) })
	}
	reg.GaugeFunc("openarcanum_meta_database_bytes", "Size of meta.db and its WAL on disk.", func() float64 {
		var n int64
		for _, f := range []string{"meta.db", "meta.db-wal"} {
			if st, err := os.Stat(filepath.Join(s.cfg.DataDir, f)); err == nil {
				n += st.Size()
			}
		}
		return float64(n)
	})
	reg.GaugeFunc("openarcanum_meta_mailbox_watches", "Open mailbox watches (one per inbox a client is listening on): how many people are connected for direct messages.", func() float64 {
		return float64(s.watchCount())
	})
	s.registerPeerMetrics(reg)
	mux := s.routes()
	return s.cors(ops.InstrumentRoutes(s.versioned(mux), mux, reg, "openarcanum_meta"))
}

// Live is the node's health check: its database answers.
func (s *Server) Live(ctx context.Context) error {
	var one int
	if err := s.db.QueryRowContext(ctx, `SELECT 1`).Scan(&one); err != nil {
		return errors.New("database unavailable")
	}
	return nil
}
