// Command community runs an OpenArcanum community server.
//
//	community init    -data DIR [-relay URL]... [-maintainer URL]...   create a server, print a claim code
//	community init    -interactive                                     the same, as a guided setup (initcmd.go)
//	community run     -data DIR | -config FILE [-stop-with-stdin]      serve
//	community healthcheck URL                                          exit 0 if URL answers 200
//	community version                                                  print the release version
//	community backup  -data DIR                                        write an encrypted backup now
//	community restore -data NEWDIR -backup FILE -storage-key FILE [-relay URL]...
//	                                                                   restore on new hardware, print a move code
//	community reregister -data DIR | -config FILE                      bring back a community maintainers reaped
//
// Node settings live in DIR/config.json, with OPENARCANUM_COMMUNITY_* environment
// overrides (internal/community/config.go, docs/operations.md).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/config"
	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/ops"
	"github.com/hyperkubeorg/openarcanum/internal/version"
)

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "healthcheck":
		os.Exit(ops.Healthcheck(os.Args[2:], os.Stdout, os.Stderr))
	case "version":
		fmt.Println(version.String("community"))
		return
	case "init":
		err = initCommand(os.Args[2:], os.Stdin, os.Stdout, stdinIsTerminal())
	case "run":
		err = cmdRun(os.Args[2:])
	case "backup":
		err = cmdBackup(os.Args[2:])
	case "restore":
		err = cmdRestore(os.Args[2:])
	case "reregister":
		err = cmdReregister(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "community:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: community init|run|backup|restore|reregister|healthcheck|version [flags] (see -h on each)")
	os.Exit(2)
}

// stdinIsTerminal reports whether stdin is a character device (someone
// typing), not a pipe or file.
func stdinIsTerminal() bool {
	st, err := os.Stdin.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func logger(verbose bool) *slog.Logger {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

// loadConfig reads DIR/config.json (or -config) and applies env overrides.
func loadConfig(dataDir, path string) (community.Config, error) {
	if path == "" && dataDir != "" {
		path = filepath.Join(dataDir, "config.json")
	}
	cfg := community.DefaultConfig(dataDir)
	if err := config.Load(path, "OPENARCANUM_COMMUNITY_", &cfg); err != nil {
		return cfg, err
	}
	if cfg.DataDir == "" {
		cfg.DataDir = dataDir
	}
	if cfg.DataDir == "" && path != "" {
		cfg.DataDir = filepath.Dir(path)
	}
	cfg.Resolve()
	return cfg, cfg.Validate()
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	data := fs.String("data", "", "data directory")
	cfgPath := fs.String("config", "", "config file (default DATA/config.json)")
	verbose := fs.Bool("v", false, "debug logging")
	withStdin := fs.Bool("stop-with-stdin", false, "shut down cleanly when standard input closes (a supervising app ended)")
	fs.Parse(args)
	cfg, err := loadConfig(*data, *cfgPath)
	if err != nil {
		return err
	}
	log, closeLog, err := ops.NewLogger(cfg.Settings, *verbose, os.Stderr)
	if err != nil {
		return fmt.Errorf("log_file: %w", err)
	}
	defer func() { _ = closeLog() }()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *withStdin {
		// The desktop app runs hosted communities with a pipe on stdin: if
		// the app ends without stopping the server (a crash), the pipe
		// closes and the server stops cleanly instead of running orphaned.
		go func() {
			_, _ = io.Copy(io.Discard, os.Stdin)
			log.Info("standard input closed; shutting down")
			stop()
		}()
	}
	srv, err := community.Open(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer func() {
		if err := srv.Close(); err != nil {
			log.Error("closing database", "err", err)
		}
	}()
	if cfg.MetricsListen != "" {
		reg := ops.NewRegistry()
		srv.RegisterMetrics(reg)
		if _, err := ops.ServeStatus(ctx, cfg.MetricsListen, ops.Status{Registry: reg, Live: srv.Live, Ready: srv.Ready}, log); err != nil {
			return err
		}
	}
	log.Info("community server starting", "server", identity.Fingerprint(srv.ServerKey()), "claimed", srv.Claimed())
	return srv.Run(ctx)
}

// cmdReregister brings back a community that maintainer nodes reaped after
// its server reported nothing for longer than their community_reap_after
// (docs/protocol.md §7.4): every route from now on carries this moment, and
// maintainers that hold the community's tombstone accept it again. It works
// whether or not the server is running.
func cmdReregister(args []string) error {
	fs := flag.NewFlagSet("reregister", flag.ExitOnError)
	data := fs.String("data", "", "data directory")
	cfgPath := fs.String("config", "", "config file (default DATA/config.json)")
	fs.Parse(args)
	cfg, err := loadConfig(*data, *cfgPath)
	if err != nil {
		return err
	}
	if err := community.Reregister(context.Background(), cfg, time.Now()); err != nil {
		return err
	}
	fmt.Println("Re-registered. The server publishes its route with the next publication (start it if it isn't running);")
	fmt.Println("maintainer nodes then list the community again and members reconnect.")
	return nil
}

func cmdBackup(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	data := fs.String("data", "", "data directory")
	cfgPath := fs.String("config", "", "config file (default DATA/config.json)")
	fs.Parse(args)
	cfg, err := loadConfig(*data, *cfgPath)
	if err != nil {
		return err
	}
	ctx := context.Background()
	srv, err := community.Open(ctx, cfg, logger(false))
	if err != nil {
		return err
	}
	defer func() { _ = srv.Close() }()
	path, err := srv.Backup(ctx)
	if err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}

func cmdRestore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	data := fs.String("data", "", "new data directory")
	backup := fs.String("backup", "", "backup file (.fbk)")
	storageKey := fs.String("storage-key", "", "the community's storage key file")
	var relays, maintainers multi
	fs.Var(&relays, "relay", "relay URL (repeatable)")
	fs.Var(&maintainers, "maintainer", "maintainer node URL (repeatable)")
	fs.Parse(args)
	if *data == "" || *backup == "" || *storageKey == "" {
		return errors.New("-data, -backup and -storage-key are required")
	}
	cfg := community.DefaultConfig(*data)
	cfg.Relays = relays
	cfg.Maintainers = maintainers
	cfg.Resolve()
	if err := cfg.Validate(); err != nil {
		return err
	}
	res, err := community.Restore(context.Background(), cfg, *backup, *storageKey)
	if err != nil {
		return err
	}
	if err := config.Save(filepath.Join(*data, "config.json"), cfg); err != nil {
		return err
	}
	fmt.Printf("New server key: %s\n\n", identity.Encode(res.ServerKey))
	fmt.Println("Move code (any owner pastes it into Community settings → Ownership → Move to new hardware):")
	fmt.Println(res.MoveCode)
	return nil
}
