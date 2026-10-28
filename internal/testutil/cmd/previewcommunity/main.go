// Command previewcommunity runs a community server whose link-preview
// fetcher may reach loopback addresses, for UI tests that need a preview
// card from a page the test serves on 127.0.0.1
// (clients/desktop/tests/test_ui_toggles.py).
//
//	previewcommunity -data DIR   # a data directory made by `community init`
//
// It is the `community run` command with Config.PreviewAllowLoopback set.
// That field has no config key or environment variable, so the community
// binary that operators deploy can never turn it on; this test command is
// built into bin/ by `make build` but never released (`make
// release-servers` builds only ./cmd/relay, ./cmd/community and ./cmd/meta).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/config"
	"github.com/hyperkubeorg/openarcanum/internal/ops"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "previewcommunity:", err)
		os.Exit(1)
	}
}

func run() error {
	data := flag.String("data", "", "data directory")
	flag.Parse()
	if *data == "" {
		return fmt.Errorf("-data is required")
	}
	cfg := community.DefaultConfig(*data)
	if err := config.Load(filepath.Join(*data, "config.json"), "OPENARCANUM_COMMUNITY_", &cfg); err != nil {
		return err
	}
	if cfg.DataDir == "" {
		cfg.DataDir = *data
	}
	cfg.Resolve()
	if err := cfg.Validate(); err != nil {
		return err
	}
	cfg.PreviewAllowLoopback = true
	log, closeLog, err := ops.NewLogger(cfg.Settings, false, os.Stderr)
	if err != nil {
		return err
	}
	defer func() { _ = closeLog() }()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// A data directory `community init` made: every community in it, as
	// `community run` runs them.
	host, err := community.OpenHost(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer func() { _ = host.Close() }()
	return host.Run(ctx)
}
