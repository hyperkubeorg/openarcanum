package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

type sample struct {
	Listen   string   `json:"listen" env:"LISTEN"`
	Max      uint64   `json:"max" env:"MAX"`
	Peers    []string `json:"peers" env:"PEERS"`
	Enabled  bool     `json:"enabled" env:"ENABLED"`
	Interval Duration `json:"interval" env:"INTERVAL"`
}

func TestFileThenEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(path, []byte(`{"listen":":1","max":5,"interval":"2s"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("X_MAX", "9")
	t.Setenv("X_PEERS", "a, b,,c")
	t.Setenv("X_ENABLED", "true")
	cfg := sample{Listen: ":0", Max: 1}
	if err := Load(path, "X_", &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":1" || cfg.Max != 9 || !cfg.Enabled || len(cfg.Peers) != 3 || cfg.Interval.Duration != 2*time.Second {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestRejectsUnknownKeysAndBadValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(path, []byte(`{"lisen":":1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var cfg sample
	if err := Load(path, "X_", &cfg); err == nil {
		t.Fatal("unknown key accepted")
	}
	t.Setenv("Y_MAX", "lots")
	if err := Load("", "Y_", &cfg); err == nil {
		t.Fatal("bad number accepted")
	}
}

type Shared struct {
	LogFile string `json:"log_file" env:"LOG_FILE"`
	Keep    int    `json:"keep" env:"KEEP"`
}

type withShared struct {
	Listen string `json:"listen" env:"LISTEN"`
	Shared
}

// Embedded settings (ops.Settings in every node config) read the same
// prefix from the environment, flatten into the JSON file, and their keys
// round-trip through Save.
func TestEmbeddedSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(path, []byte(`{"listen":":1","log_file":"/var/log/x.log","keep":3}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("Z_KEEP", "9")
	var cfg withShared
	if err := Load(path, "Z_", &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":1" || cfg.LogFile != "/var/log/x.log" || cfg.Keep != 9 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	out := filepath.Join(t.TempDir(), "saved.json")
	if err := Save(out, cfg); err != nil {
		t.Fatal(err)
	}
	var back withShared
	if err := Load(out, "NONE_", &back); err != nil || back != cfg {
		t.Fatalf("round trip: %+v %v", back, err)
	}
}
