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
	backupsOK      atomic.Int64
	backupsFailed  atomic.Int64
	purgeRuns      atomic.Int64
	purgeFailures  atomic.Int64
}

// RegisterMetrics adds one server's metrics to reg (a server opened on its
// own: tools, tests). A host registers its communities together
// (Host.RegisterMetrics).
func (s *Server) RegisterMetrics(reg *ops.Registry) {
	registerMetrics(reg, func() []*Server { return []*Server{s} })
}

// RegisterMetrics adds the host's metrics to reg (docs/operations.md
// "Metrics"): totals across every community it runs, and how many it runs.
// Nothing is per community, member, channel or message: the metrics say
// how busy the server is, not who uses it.
func (h *Host) RegisterMetrics(reg *ops.Registry) {
	reg.GaugeFunc("openarcanum_community_communities", "Communities this server runs.", func() float64 {
		return float64(len(h.Servers()))
	})
	registerMetrics(reg, func() []*Server {
		var out []*Server
		for _, s := range h.Servers() {
			out = append(out, s)
		}
		return out
	})
}

func registerMetrics(reg *ops.Registry, servers func() []*Server) {
	sum := func(f func(*Server) float64) func() float64 {
		return func() float64 {
			var n float64
			for _, s := range servers() {
				n += f(s)
			}
			return n
		}
	}
	reg.GaugeFunc("openarcanum_community_sessions", "Open member sessions (one per connected device).", sum(func(s *Server) float64 {
		s.smu.Lock()
		defer s.smu.Unlock()
		return float64(len(s.sessions))
	}))
	reg.GaugeFunc("openarcanum_community_members_online", "Members with at least one admitted session.", sum(func(s *Server) float64 {
		s.smu.Lock()
		defer s.smu.Unlock()
		online := map[string]bool{}
		for ss := range s.sessions {
			if ss.getStatus() == wire.MemberStatus_MEMBER_STATUS_ACTIVE {
				online[string(ss.member)] = true
			}
		}
		return float64(len(online))
	}))
	reg.GaugeFunc("openarcanum_community_members", "Members on the roll (all statuses).", sum(func(s *Server) float64 {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return float64(len(s.members))
	}))
	reg.GaugeFunc("openarcanum_community_channels", "Channels.", sum(func(s *Server) float64 {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return float64(len(s.channels))
	}))
	reg.GaugeFunc("openarcanum_community_call_participants", "People in voice channels.", sum(func(s *Server) float64 {
		s.voice.mu.Lock()
		defer s.voice.mu.Unlock()
		n := 0
		for _, cl := range s.voice.calls {
			n += len(cl.parts)
		}
		return float64(n)
	}))
	reg.GaugeFunc("openarcanum_community_relays_attached", "Relay attachments (one per community per relay).", sum(func(s *Server) float64 {
		return float64(len(s.AttachedRelays()))
	}))
	reg.GaugeFunc("openarcanum_community_unready", "Communities with no relay attached (members can't reach them).", sum(func(s *Server) float64 {
		if len(s.AttachedRelays()) == 0 {
			return 1
		}
		return 0
	}))
	reg.CounterFunc("openarcanum_community_messages_total", "Messages stored since start (including join and leave notices).", sum(func(s *Server) float64 {
		return float64(s.counters.messages.Load())
	}))
	reg.CounterFunc("openarcanum_community_backups_total", "Backups written since start.", sum(func(s *Server) float64 {
		return float64(s.counters.backupsOK.Load())
	}))
	reg.CounterFunc("openarcanum_community_backup_failures_total", "Backups that failed since start.", sum(func(s *Server) float64 {
		return float64(s.counters.backupsFailed.Load())
	}))
	reg.GaugeFunc("openarcanum_community_last_backup_timestamp_seconds", "When the newest backup was written (Unix time; 0 = none since start).", func() float64 {
		var newest int64
		for _, s := range servers() {
			newest = max(newest, s.counters.lastBackupUnix.Load())
		}
		return float64(newest)
	})
	reg.CounterFunc("openarcanum_community_purge_runs_total", "Retention job runs.", sum(func(s *Server) float64 {
		return float64(s.counters.purgeRuns.Load())
	}))
	reg.CounterFunc("openarcanum_community_purge_failures_total", "Retention job runs that failed.", sum(func(s *Server) float64 {
		return float64(s.counters.purgeFailures.Load())
	}))
	reg.GaugeFunc("openarcanum_community_database_bytes", "Size of the communities' databases and their WALs on disk.", sum(func(s *Server) float64 {
		var n int64
		for _, f := range []string{"community.db", "community.db-wal"} {
			if st, err := os.Stat(filepath.Join(s.cfg.DataDir, f)); err == nil {
				n += st.Size()
			}
		}
		return float64(n)
	}))
}

// countBackup records a backup's outcome.
func (s *Server) countBackup(err error) {
	if err != nil {
		s.counters.backupsFailed.Add(1)
		return
	}
	s.counters.lastBackupUnix.Store(s.now().Unix())
	s.counters.backupsOK.Add(1)
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
