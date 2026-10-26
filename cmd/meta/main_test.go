package main

import (
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/config"
	"github.com/hyperkubeorg/openarcanum/internal/meta"
)

// The example config the systemd unit points at (/etc/openarcanum/meta.json,
// docs/admin-guide.md) loads and validates.
func TestExampleConfig(t *testing.T) {
	cfg := meta.DefaultConfig()
	cfg.DirectoryEnabled = true // the tests exercise the directory; it's off by default
	if err := config.Load("../../deploy/config/meta.json", "OPENARCANUM_TEST_UNSET_", &cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.MetricsListen != "127.0.0.1:7101" || cfg.DataDir != "/var/lib/openarcanum-meta" || len(cfg.Peers) != 1 {
		t.Fatalf("example config: %+v", cfg)
	}
}
