package relay

import (
	"github.com/hyperkubeorg/openarcanum/internal/ops"
)

// RegisterMetrics adds the relay's metrics to reg (docs/operations.md
// "Metrics"). They are counts only: no topics, endpoint keys, addresses or
// payload sizes per client.
func (s *Server) RegisterMetrics(reg *ops.Registry) {
	reg.GaugeFunc("openarcanum_relay_connections", "Open client connections.", func() float64 {
		return float64(s.Connections())
	})
	reg.CounterFunc("openarcanum_relay_connections_accepted_total", "Connections accepted since start.", func() float64 {
		return float64(s.nextConn.Load())
	})
	reg.CounterFunc("openarcanum_relay_connections_refused_total", "Connections refused because max_connections was reached.", func() float64 {
		return float64(s.stats.Refused.Load())
	})
	reg.GaugeFunc("openarcanum_relay_connections_unstarted", "Connections that haven't sent Start yet.", func() float64 {
		return float64(s.Unstarted())
	})
	reg.CounterFunc("openarcanum_relay_unstarted_refused_total", "Connections refused because too many from their network (or in all) hadn't started.", func() float64 {
		return float64(s.stats.UnstartedRefused.Load())
	})
	reg.CounterFunc("openarcanum_relay_unstarted_evicted_total", "Unstarted connections closed to make room for a new one.", func() float64 {
		return float64(s.stats.UnstartedEvicted.Load())
	})
	reg.CounterFunc("openarcanum_relay_network_refused_total", "Connections refused because their network held max_connections_per_ip.", func() float64 {
		return float64(s.stats.NetworkRefused.Load())
	})
	reg.GaugeFunc("openarcanum_relay_subscriptions", "Subscriptions across all connections.", func() float64 {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return float64(s.subsTotal)
	})
	reg.GaugeFunc("openarcanum_relay_cache_bytes", "Bytes the chunk cache holds, bookkeeping overhead included.", func() float64 {
		return float64(s.CacheBytes())
	})
	reg.GaugeFunc("openarcanum_relay_topics", "Topics with at least one subscriber.", func() float64 {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return float64(len(s.topics))
	})
	reg.GaugeFunc("openarcanum_relay_endpoints", "Registered stream endpoints (community servers).", func() float64 {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return float64(len(s.endpoints))
	})
	reg.CounterFunc("openarcanum_relay_deliveries_total", "Published messages delivered to subscribers.", func() float64 {
		return float64(s.stats.Delivered.Load())
	})
	reg.CounterFunc("openarcanum_relay_slow_consumers_total", "Subscribers disconnected because their send queue overflowed.", func() float64 {
		return float64(s.stats.SlowConsumers.Load())
	})
	reg.GaugeFunc("openarcanum_relay_blocked_networks", "Networks a dedicated relay's block list holds (0 on other relays).", func() float64 {
		return float64(s.BlockedNetworks())
	})
	reg.CounterFunc("openarcanum_relay_blocked_refused_total", "Connections a dedicated relay refused because their network is blocked.", func() float64 {
		return float64(s.stats.BlockedRefused.Load())
	})
	reg.GaugeFunc("openarcanum_relay_calls", "Calls in progress on this relay's SFU (0 when calls are off).", func() float64 {
		s.mu.RLock()
		media := s.sfu
		s.mu.RUnlock()
		if media == nil {
			return 0
		}
		return float64(media.Calls())
	})
}
