package main

import (
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/config"
	"github.com/hyperkubeorg/openarcanum/internal/relay"
)

// The example config the systemd unit points at (/etc/openarcanum/relay.json,
// docs/admin-guide.md) loads and validates.
func TestExampleConfig(t *testing.T) {
	cfg := relay.DefaultConfig()
	if err := config.Load("../../deploy/config/relay.json", "OPENARCANUM_TEST_UNSET_", &cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.MetricsListen != "127.0.0.1:7001" || cfg.LogFormat != "json" || len(cfg.RegistryURLs) != 1 {
		t.Fatalf("example config: %+v", cfg)
	}
}
