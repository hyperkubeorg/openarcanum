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

var storeQueries = []struct{ name, help, query string }{
	{"openarcanum_meta_listings", "Directory listings (not withdrawn).", `SELECT count(*) FROM listings WHERE withdrawn = 0`},
	{"openarcanum_meta_routes", "Private community routes.", `SELECT count(*) FROM routes`},
	{"openarcanum_meta_relays", "Relays in the registry.", `SELECT count(*) FROM relays`},
	{"openarcanum_meta_mailbox_items", "Items waiting in DM mailboxes.", `SELECT count(*) FROM mailbox`},
	{"openarcanum_meta_mailbox_bytes", "Bytes waiting in DM mailboxes.", `SELECT coalesce(sum(size), 0) FROM mailbox`},
	{"openarcanum_meta_nameplates", "Open device-link nameplates.", `SELECT count(*) FROM nameplates`},
	{"openarcanum_meta_blobs_stored", "Complete DM blobs in the blob store.", `SELECT count(*) FROM blobs WHERE complete = 1 AND remote = 0`},
	{"openarcanum_meta_blobs_stored_bytes", "Bytes of complete DM blobs.", `SELECT coalesce(sum(size), 0) FROM blobs WHERE complete = 1 AND remote = 0`},
	{"openarcanum_meta_blobs_remote", "DM blob records from cluster peers whose content isn't (all) here yet.", `SELECT count(*) FROM blobs WHERE remote = 1`},
	{"openarcanum_meta_blobs_uploading", "DM blob uploads in progress.", `SELECT count(*) FROM blobs WHERE complete = 0`},
	{"openarcanum_meta_blobs_uploading_bytes", "Bytes reserved by DM blob uploads in progress.", `SELECT coalesce(sum(size), 0) FROM blobs WHERE complete = 0`},
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
			if err := s.db.QueryRowContext(ctx, q.query).Scan(&v); err != nil {
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
