package community_test

import (
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// The community server keeps relay URLs (config and pinned-relay setting)
// in the canonical form the maintainer registry uses, and refuses URLs that
// aren't relay URLs.
func TestCommunityRelayURLsCanonical(t *testing.T) {
	cfg := community.DefaultConfig(t.TempDir())
	cfg.Relays = []string{"WSS://Relay.Example:443/ws/"}
	cfg.Resolve()
	if err := cfg.Validate(); err != nil || cfg.Relays[0] != "wss://relay.example/ws" {
		t.Fatalf("config relay %q, %v", cfg.Relays[0], err)
	}
	cfg.Relays = []string{"wss://relay.example/ws?x=1"}
	cfg.Resolve()
	if cfg.Validate() == nil {
		t.Fatal("relay URL with a query accepted in the config")
	}
	s := community.DefaultSettings()
	s.Name = "Test"
	s.RelayMode = wire.RelayMode_RELAY_MODE_PINNED
	s.PinnedRelays = []string{"ws://RELAY.example:80/ws/"}
	if err := community.NormalizeSettings(s); err != nil || s.PinnedRelays[0] != "ws://relay.example/ws" {
		t.Fatalf("pinned relay %q, %v", s.PinnedRelays[0], err)
	}
	s.PinnedRelays = []string{"wss://user@relay.example/ws"}
	if community.NormalizeSettings(s) == nil {
		t.Fatal("relay URL with user information accepted in the settings")
	}
}
