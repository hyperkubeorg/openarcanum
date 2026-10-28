package community

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Status reports to maintainer nodes, and coming back after being reaped
// (docs/protocol.md §7.4).
//
// While it runs, the server sends every maintainer a heartbeat signed by its
// server key every heartbeat_interval, and one marked stopping when it shuts
// down cleanly, so members see "Offline" at once instead of after the
// maintainers' offline window. A maintainer refuses a heartbeat for a route
// it doesn't hold (404) or that names another server key (409); the server
// then publishes its route again. A 410 means the maintainer reaped the
// community after its server reported nothing for longer than the
// maintainer's community_reap_after: the server records that, logs how to
// come back, and keeps trying (its members may still reach it), but the
// maintainers refuse it until the operator runs `community reregister`,
// which stamps every later route with the re-registration time.

const (
	metaReaped       = "reaped_at"       // when a maintainer first answered 410 (unix ms, 8 bytes)
	metaReregistered = "reregistered_at" // when the operator re-registered (unix ms, 8 bytes)
)

func msBytes(ms int64) []byte { return binary.BigEndian.AppendUint64(nil, uint64(ms)) }

func msFrom(b []byte) int64 {
	if len(b) != 8 {
		return 0
	}
	return int64(binary.BigEndian.Uint64(b))
}

// relayWanted is how many relays the server means to be attached to (in
// automatic mode, what the last choice from the registry came to), and
// whether it only uses relays it was given (pinned or a dedicated relay):
// those never move elsewhere when they're down.
func (s *Server) relayWanted() (int, bool) {
	s.mu.RLock()
	set := s.settings
	s.mu.RUnlock()
	if set.RelayMode == wire.RelayMode_RELAY_MODE_PINNED && len(set.PinnedRelays) > 0 {
		return len(set.PinnedRelays), true
	}
	if s.cfg.RelayMode == "pinned" {
		return len(s.cfg.Relays), true
	}
	// Automatic: what the registry let it choose (it can't be on more
	// relays than the registry lists).
	s.rmu.RLock()
	defer s.rmu.RUnlock()
	if s.relaysWanted > 0 {
		return s.relaysWanted, false
	}
	return s.cfg.RelayCount, false
}

// heartbeat builds this server's signed heartbeat now (nil before it's
// claimed or while it waits for an owner to adopt it).
func (s *Server) heartbeat(stopping bool) []byte {
	s.mu.RLock()
	key, cert, owner, route := s.key, s.cert, s.ownerKey, s.routeID
	s.mu.RUnlock()
	if owner == nil || cert == nil {
		return nil
	}
	if _, err := identity.VerifyServerCert(cert, key.Public, owner); err != nil {
		return nil
	}
	wanted, pinned := s.relayWanted()
	hb, _ := proto.Marshal(&wire.CommunityHeartbeat{
		RouteId: route, ServerKey: key.Public, At: s.now().UnixMilli(), Stopping: stopping,
		RelaysAttached: uint32(min(len(s.AttachedRelays()), 64)), RelaysWanted: uint32(min(wanted, 64)), Pinned: pinned,
	})
	signed, _ := proto.Marshal(&wire.SignedCommunityHeartbeat{Heartbeat: hb, Signature: key.Sign(identity.CtxCommunityHeartbeat, hb)})
	return signed
}

// SendHeartbeat reports to every maintainer node once (heartbeatLoop; and
// with stopping set, from Run as it shuts down).
func (s *Server) SendHeartbeat(ctx context.Context, stopping bool) error {
	if len(s.cfg.Maintainers) == 0 {
		return nil
	}
	body := s.heartbeat(stopping)
	if body == nil {
		return nil
	}
	var errs []error
	for _, m := range s.maintainerURLs(ctx) {
		err := s.http.post(ctx, strings.TrimRight(m, "/")+"/v1/communities/heartbeat", body)
		var se *statusError
		switch {
		case err == nil:
		case errors.As(err, &se) && (se.code == http.StatusNotFound || se.code == http.StatusConflict):
			// The node lacks the current route (new node, rotated key).
			s.requestPublish()
		case errors.As(err, &se) && se.code == http.StatusGone:
			// Reaped there: publishing the route (with any re-registration)
			// decides, and records it if the maintainer refuses that too.
			// Once recorded, only a re-registration publishes again.
			if s.Reaped(ctx) == 0 {
				s.requestPublish()
			}
		default:
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Server) heartbeatLoop(ctx context.Context) {
	t := time.NewTicker(s.cfg.HeartbeatInterval.Duration)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := s.SendHeartbeat(ctx, false); err != nil && ctx.Err() == nil {
			s.log.Debug("heartbeat to maintainers failed", "err", err)
		}
	}
}

// noteReaped records that a maintainer reaped this community (the first
// time) and says how to bring it back.
func (s *Server) noteReaped(ctx context.Context, maintainer string) {
	s.reapMu.Lock()
	defer s.reapMu.Unlock()
	if v, _ := s.getMeta(ctx, metaReaped); v != nil {
		reg, _ := s.getMeta(ctx, metaReregistered)
		if msFrom(reg) < msFrom(v) {
			s.log.Debug("maintainer still refuses this reaped community", "maintainer", maintainer)
			return
		}
	}
	if err := s.setMeta(ctx, s.db, metaReaped, msBytes(s.now().UnixMilli())); err != nil {
		s.log.Error("recording that the community was reaped", "err", err)
	}
	s.log.Error("a maintainer node shut this community down: its server reported nothing for longer than the "+
		"maintainer's community_reap_after (7 days by default). Members are told it was shut down. To bring it back, "+
		"run `community reregister` on this data directory", "maintainer", maintainer, "data", s.cfg.DataDir)
}

// Reaped reports when a maintainer last refused this community as reaped,
// unless the operator re-registered it since (0: not reaped).
func (s *Server) Reaped(ctx context.Context) int64 {
	v, _ := s.getMeta(ctx, metaReaped)
	reg, _ := s.getMeta(ctx, metaReregistered)
	if msFrom(reg) >= msFrom(v) {
		return 0
	}
	return msFrom(v)
}

// reregisteredAt is the operator's re-registration time (0 = never).
func (s *Server) reregisteredAt(ctx context.Context) int64 {
	v, _ := s.getMeta(ctx, metaReregistered)
	return msFrom(v)
}

// Reregister is `community reregister`: it stamps the data directory with
// the time now, which every later route carries, so maintainers that reaped
// the community accept it again (docs/protocol.md §7.4). It works while the
// server runs (the next publication picks it up).
func Reregister(ctx context.Context, cfg Config, now time.Time) error {
	cfg.Resolve()
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "server.key")); err != nil {
		return fmt.Errorf("%s is not a community server's data directory", cfg.DataDir)
	}
	storageKey, err := cfg.readStorageKey()
	if err != nil {
		return fmt.Errorf("storage key: %w", err)
	}
	s := &Server{cfg: cfg, storageKey: storageKey}
	if err := s.openDB(ctx); err != nil {
		return err
	}
	defer s.db.Close()
	return s.setMeta(ctx, s.db, metaReregistered, msBytes(now.UnixMilli()))
}
