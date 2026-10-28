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
	"sort"
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
	host, err := community.OpenHost(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer func() {
		if err := host.Close(); err != nil {
			log.Error("closing database", "err", err)
		}
	}()
	servers := host.Servers()
	if cfg.MetricsListen != "" {
		// Health covers every community; the metrics are the first one's.
		reg := ops.NewRegistry()
		host.First().RegisterMetrics(reg)
		if _, err := ops.ServeStatus(ctx, cfg.MetricsListen, ops.Status{Registry: reg, Live: host.Live, Ready: host.Ready}, log); err != nil {
			return err
		}
	}
	for _, id := range sortedIDs(servers) {
		srv := servers[id]
		log.Info("community server starting", "community", id, "server", identity.Fingerprint(srv.ServerKey()), "claimed", srv.Claimed())
	}
	return host.Run(ctx)
}

func sortedIDs(m map[string]*community.Server) []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// communityConfig is the config of one community on the host in dataDir:
// the one named, or the only one.
func communityConfig(dataDir, cfgPath, id string) (community.Config, error) {
	host, err := loadConfig(dataDir, cfgPath)
	if err != nil {
		return host, err
	}
	if id == "" {
		m, _ := filepath.Glob(filepath.Join(host.DataDir, "communities", "*", "server.key"))
		if len(m) != 1 {
			return host, errors.New("this server runs several communities: name one with -community ID (the directories in DATA/communities)")
		}
		id = filepath.Base(filepath.Dir(m[0]))
	}
	return community.CommunityConfig(host, id)
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
	id := fs.String("community", "", "which community (a directory in DATA/communities; needed when there are several)")
	fs.Parse(args)
	cfg, err := communityConfig(*data, *cfgPath, *id)
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
	id := fs.String("community", "", "which community (a directory in DATA/communities; needed when there are several)")
	fs.Parse(args)
	cfg, err := communityConfig(*data, *cfgPath, *id)
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
	data := fs.String("data", "", "the host's data directory (a new one is created)")
	backup := fs.String("backup", "", "backup file (.fbk)")
	storageKey := fs.String("storage-key", "", "the storage key file of the server the backup is from")
	from := fs.String("community", "", "which community on that server the backup is of (default: from the backup's path, DATA/communities/ID/backups/…)")
	var relays, maintainers multi
	fs.Var(&relays, "relay", "relay URL (repeatable; a new host only)")
	fs.Var(&maintainers, "maintainer", "maintainer node URL (repeatable; a new host only)")
	fs.Parse(args)
	if *data == "" || *backup == "" || *storageKey == "" {
		return errors.New("-data, -backup and -storage-key are required")
	}
	// Onto an existing host (its config), or a new one.
	cfgFile := filepath.Join(*data, "config.json")
	host, err := loadConfig(*data, "")
	if _, statErr := os.Stat(cfgFile); statErr != nil {
		host = community.DefaultConfig(*data)
		host.Relays = relays
		host.Maintainers = maintainers
		host.Resolve()
		err = host.Validate()
	}
	if err != nil {
		return err
	}
	if _, err := os.Stat(host.StorageKeyFile); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(host.StorageKeyFile), 0o700); err != nil {
			return err
		}
		if err := community.WriteStorageKey(host.StorageKeyFile); err != nil {
			return err
		}
	}
	// The restored community keeps the storage key its backup was made
	// with, in a key file of its own next to the host's, and its id when
	// that's free here.
	if *from == "" {
		if dir := filepath.Dir(filepath.Dir(*backup)); filepath.Base(filepath.Dir(dir)) == "communities" {
			*from = filepath.Base(dir)
		}
	}
	id := *from
	if _, err := os.Stat(filepath.Join(host.DataDir, "communities", id)); id == "" || err == nil {
		id = community.NewCommunityID()
	}
	cfg, err := community.CommunityConfig(host, id)
	if err != nil {
		return err
	}
	cfg.StorageKey = nil
	var res *community.MoveResult
	if *from != "" {
		res, err = community.RestoreHosted(context.Background(), cfg, *backup, *storageKey, *from)
	} else {
		// A backup from a server that ran one community on its own.
		res, err = community.Restore(context.Background(), cfg, *backup, *storageKey)
	}
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(cfgFile); statErr != nil {
		if err := config.Save(cfgFile, host); err != nil {
			return err
		}
	}
	fmt.Printf("Community: %s\n", id)
	fmt.Printf("New server key: %s\n\n", identity.Encode(res.ServerKey))
	fmt.Println("Move code (any owner pastes it into Community settings → Ownership → Move to new hardware):")
	fmt.Println(res.MoveCode)
	return nil
}
