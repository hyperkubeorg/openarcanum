package community

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/hyperkubeorg/openarcanum/internal/ops"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// opCounters are the server's event counts for the metrics endpoint. They
// count events, never who or what: no member, channel or message ids.
type opCounters struct {
	messages       atomic.Int64
	lastBackupUnix atomic.Int64
	purgeRuns      atomic.Int64
	purgeFailures  atomic.Int64
}

// RegisterMetrics adds the community server's metrics to reg
// (docs/operations.md "Metrics"): totals and gauges only. Membership size
// is a single number; nothing identifies a member, channel or message.
func (s *Server) RegisterMetrics(reg *ops.Registry) {
	reg.GaugeFunc("openarcanum_community_sessions", "Open member sessions (one per connected device).", func() float64 {
		s.smu.Lock()
		defer s.smu.Unlock()
		return float64(len(s.sessions))
	})
	reg.GaugeFunc("openarcanum_community_members_online", "Members with at least one admitted session.", func() float64 {
		s.smu.Lock()
		defer s.smu.Unlock()
		online := map[string]bool{}
		for ss := range s.sessions {
			if ss.getStatus() == wire.MemberStatus_MEMBER_STATUS_ACTIVE {
				online[string(ss.member)] = true
			}
		}
		return float64(len(online))
	})
	reg.GaugeFunc("openarcanum_community_members", "Members on the roll (all statuses).", func() float64 {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return float64(len(s.members))
	})
	reg.GaugeFunc("openarcanum_community_channels", "Channels.", func() float64 {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return float64(len(s.channels))
	})
	reg.GaugeFunc("openarcanum_community_relays_attached", "Relays this server is attached to.", func() float64 {
		return float64(len(s.AttachedRelays()))
	})
	reg.CounterFunc("openarcanum_community_messages_total", "Messages stored since start (including join and leave notices).", func() float64 {
		return float64(s.counters.messages.Load())
	})
	backups := reg.CounterVec("openarcanum_community_backups_total", "Backups written, by outcome.",
		ops.Labels{Names: []string{"outcome"}, Values: [][]string{{"ok", "failed"}}})
	backups.With("ok")
	backups.With("failed")
	s.backupMetric = backups
	reg.GaugeFunc("openarcanum_community_last_backup_timestamp_seconds", "When the last backup was written (Unix time; 0 = none since start).", func() float64 {
		return float64(s.counters.lastBackupUnix.Load())
	})
	reg.CounterFunc("openarcanum_community_purge_runs_total", "Retention job runs.", func() float64 {
		return float64(s.counters.purgeRuns.Load())
	})
	reg.CounterFunc("openarcanum_community_purge_failures_total", "Retention job runs that failed.", func() float64 {
		return float64(s.counters.purgeFailures.Load())
	})
	reg.GaugeFunc("openarcanum_community_database_bytes", "Size of community.db and its WAL on disk.", func() float64 {
		var n int64
		for _, f := range []string{"community.db", "community.db-wal"} {
			if st, err := os.Stat(filepath.Join(s.cfg.DataDir, f)); err == nil {
				n += st.Size()
			}
		}
		return float64(n)
	})
}

// countBackup records a backup's outcome.
func (s *Server) countBackup(err error) {
	if err != nil {
		if s.backupMetric != nil {
			s.backupMetric.With("failed").Inc()
		}
		return
	}
	s.counters.lastBackupUnix.Store(s.now().Unix())
	if s.backupMetric != nil {
		s.backupMetric.With("ok").Inc()
	}
}

// Live is the health check: the database answers.
func (s *Server) Live(ctx context.Context) error {
	var one int
	if err := s.db.QueryRowContext(ctx, `SELECT 1`).Scan(&one); err != nil {
		return errors.New("database unavailable")
	}
	return nil
}

// Ready is the readiness check: the server is live and attached to at
// least one relay, so members can reach it.
func (s *Server) Ready(ctx context.Context) error {
	if err := s.Live(ctx); err != nil {
		return err
	}
	if len(s.AttachedRelays()) == 0 {
		return errors.New("no relay attached")
	}
	return nil
}
