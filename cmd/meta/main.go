// Command meta runs an OpenArcanum maintainer node: community directory and
// search, route lookup, relay registry, DM mailboxes and link nameplates.
//
//	meta [-config meta.json] [-v]
//	meta blobs stats|delete|purge-expired -data DIR [-id HEX]
//	meta backup -data DIR [-out DIR] [-keep N]     consistent online backup (database + blobs)
//	meta restore -data NEWDIR -backup FILE         restore a backup into an empty data directory
//	meta healthcheck URL                           exit 0 if URL answers 200 (container health checks)
//	meta version                                   print the release version (VERSION, set at link time)
//	meta directory list|delist|relist|decisions -data DIR [-id COMMUNITY] [-reason R]
//	meta relays list|remove|restore -data DIR [-url URL] [-reason R]
//	meta id -data DIR                              this node's node key (maintainer clusters)
//	meta trust add|remove|list -data DIR [URL] [NODEKEY]
//	meta operator keygen|sign-set ...               a maintainer cluster's signed member list
//	meta set publish -data DIR set.bin
//	meta init -replica-of ROOT_URL -data DIR [-url THIS_URL] [-from-backup FILE]
//	meta peers status -data DIR
//
// The admin commands (blobs, directory, relays) work on the node's data
// directory and are safe to run while the node is running (docs/operations.md
// "Maintainer admin commands").
//
// Settings come from the JSON config file, then OPENARCANUM_META_* environment
// variables (internal/meta/config.go, docs/operations.md).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/config"
	"github.com/hyperkubeorg/openarcanum/internal/meta"
	"github.com/hyperkubeorg/openarcanum/internal/ops"
	"github.com/hyperkubeorg/openarcanum/internal/version"
)

// subcommands run on a data directory instead of serving.
var subcommands = map[string]func(ctx context.Context, args []string, out io.Writer) error{
	"blobs":     blobsCommand,
	"backup":    backupCommand,
	"restore":   restoreCommand,
	"directory": directoryCommand,
	"relays":    relaysCommand,
	"id":        idCommand,
	"trust":     trustCommand,
	"operator":  operatorCommand,
	"set":       setCommand,
	"init":      initCommand,
	"peers":     peersCommand,
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(ops.Healthcheck(os.Args[2:], os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(version.String("meta"))
		return
	}
	if len(os.Args) > 1 && subcommands[os.Args[1]] != nil {
		if err := subcommands[os.Args[1]](context.Background(), os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "meta %s: %v\n", os.Args[1], err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "meta:", err)
		os.Exit(1)
	}
}

func run() error {
	cfgPath := flag.String("config", "", "path to the JSON config file")
	verbose := flag.Bool("v", false, "debug logging")
	flag.Parse()

	cfg := meta.DefaultConfig()
	if err := config.Load(*cfgPath, "OPENARCANUM_META_", &cfg); err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	log, closeLog, err := ops.NewLogger(cfg.Settings, *verbose, os.Stderr)
	if err != nil {
		return fmt.Errorf("log_file: %w", err)
	}
	defer func() { _ = closeLog() }()
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv, err := meta.Open(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer func() { _ = srv.Close() }()
	// TLS (tls_mode) for https:// straight from browsers; with acme the
	// certificates are obtained in the background on this listener.
	ops.WarnPlainListener(log, cfg.Listen, cfg.TLSSettings)
	ln, err := ops.ListenPublic(ctx, cfg.Listen, cfg.TLSSettings, ops.TLSOptions{CacheDir: cfg.ACMECache(), HTTP2: true, Log: log})
	if err != nil {
		return err
	}
	handler := srv.Handler()
	if cfg.MetricsListen != "" {
		reg := ops.NewRegistry()
		handler = srv.RegisterMetrics(reg)
		if _, err := ops.ServeStatus(ctx, cfg.MetricsListen, ops.Status{Registry: reg, Live: srv.Live}, log); err != nil {
			return err
		}
	}
	hs := meta.NewHTTPServer(handler, log)
	go srv.Run(ctx)
	log.Info("maintainer node listening", "addr", ln.Addr().String(), "peers", len(cfg.Peers), "tls", cfg.TLSMode, "cors_origins", cfg.CORSOrigins)
	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(ln) }()
	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return hs.Shutdown(sctx)
}
