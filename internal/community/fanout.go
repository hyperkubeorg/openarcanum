package community

import (
	"context"
	"sort"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/fanout"
	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/relayclient"
)

// Channel topics commit to the server key (internal/fanout), so relays let
// this server, and only holders of its fan-out tokens, publish to them once
// they are large (docs/protocol.md §2 "Large fan-outs").

// newTopic makes a channel topic committed to the current server key.
// Callers hold s.mu (or own the server before Run).
func (s *Server) newTopic() []byte { return fanout.NewTopic(s.key.Public) }

// recommitTopics gives every channel whose topic doesn't commit to the
// current server key (made under a previous key, or before topics were
// committed) a new topic, key and epoch; members follow the new topic from
// their next state update. Callers hold s.mu.
func (s *Server) recommitTopics(ctx context.Context) error {
	var stale []int64
	for id, c := range s.channels {
		if !fanout.Commits(c.Topic, s.key.Public) {
			stale = append(stale, id)
		}
	}
	if len(stale) == 0 {
		return nil
	}
	sort.Slice(stale, func(i, j int) bool { return stale[i] < stale[j] })
	s.log.Info("channel topics moved to the current server key", "channels", len(stale))
	return s.rotateChannels(ctx, stale)
}

// authorizeFanout hands a relay connection a fan-out token for the current
// server key.
func (s *Server) authorizeFanout(ctx context.Context, c *relayclient.Client, key *identity.Key) error {
	return c.FanoutAuth(ctx, key, fanout.TokenLifetime)
}

// renewFanout keeps a relay connection's token fresh (renewed at half its
// lifetime) until the connection ends.
func (s *Server) renewFanout(ctx context.Context, c *relayclient.Client) {
	t := time.NewTicker(fanout.TokenLifetime / 2)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.Done():
			return
		case <-t.C:
			s.mu.RLock()
			key := s.key
			s.mu.RUnlock()
			if err := s.authorizeFanout(ctx, c, key); err != nil {
				s.log.Warn("fan-out token renewal failed", "relay", c.URL, "err", err)
			}
		}
	}
}
