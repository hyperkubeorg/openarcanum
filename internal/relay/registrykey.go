package relay

import (
	"net/http"
	"strings"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
)

// Proof of control for the relay registry (docs/protocol.md §7.3): a
// maintainer node lists a relay URL only after a plain GET of that URL
// (http:// for ws://, https:// for wss://) returned the key that signs the
// URL's heartbeats. Anyone can post a heartbeat naming any URL; only the
// operator of the relay at that URL can make the URL serve its key.

// SetRegistryKey sets the registry key the relay serves (the Heartbeater
// does, with the key it signs heartbeats with).
func (s *Server) SetRegistryKey(pub []byte) {
	k := append([]byte(nil), pub...)
	s.registryKey.Store(&k)
}

// serveRelayURL serves the relay URL: a plain GET (no WebSocket upgrade)
// to a relay that registers gets its registry key as base64url text;
// everything else goes to serveWS.
func (s *Server) serveRelayURL(w http.ResponseWriter, r *http.Request) {
	k := s.registryKey.Load()
	if k == nil || r.Method != http.MethodGet || strings.Contains(strings.ToLower(r.Header.Get("Upgrade")), "websocket") {
		s.serveWS(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(identity.Encode(*k) + "\n"))
}
