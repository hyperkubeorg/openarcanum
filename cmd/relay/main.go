// Command relay runs an OpenArcanum relay: a stateless, keyless topic router.
//
//	relay [-config relay.json] [-v]
//	relay healthcheck URL        exit 0 if URL answers 200 (container health checks)
//	relay version                print the release version (VERSION, set at link time)
//
// Settings come from the JSON config file, then OPENARCANUM_RELAY_* environment
// variables (see internal/relay/config.go and docs/operations.md).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/config"
	"github.com/hyperkubeorg/openarcanum/internal/ops"
	"github.com/hyperkubeorg/openarcanum/internal/relay"
	"github.com/hyperkubeorg/openarcanum/internal/sfu"
	"github.com/hyperkubeorg/openarcanum/internal/version"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(ops.Healthcheck(os.Args[2:], os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(version.String("relay"))
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "relay:", err)
		os.Exit(1)
	}
}

func run() error {
	cfgPath := flag.String("config", "", "path to the JSON config file")
	verbose := flag.Bool("v", false, "debug logging")
	flag.Parse()

	cfg := relay.DefaultConfig()
	if err := config.Load(*cfgPath, "OPENARCANUM_RELAY_", &cfg); err != nil {
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

	srv := relay.New(cfg, log)
	if err := srv.Err(); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// TLS (tls_mode) for wss:// straight from browsers; with acme the
	// certificates are obtained in the background on this listener.
	ops.WarnPlainListener(log, cfg.Listen, cfg.TLSSettings)
	ln, err := ops.ListenPublic(ctx, cfg.Listen, cfg.TLSSettings, ops.TLSOptions{CacheDir: cfg.ACMECacheDir, Log: log})
	if err != nil {
		return err
	}
	if cfg.Calls {
		media, err := sfu.New(sfu.Config{
			UDPPort: cfg.MediaUDPPort, ListenIP: cfg.MediaListenIP, PublicIPs: cfg.MediaPublicIPs,
			IncludeLoopback: cfg.MediaLoopback, MaxCalls: cfg.MaxCalls, Log: log,
			MaxPeers: cfg.MaxCallPeers, MaxPeersPerIP: cfg.MaxCallPeersPerIP, PeerBitrate: cfg.CallPeerBitrate,
			AllowedServers: cfg.CallServerKeys(),
		})
		if err != nil {
			return err
		}
		defer media.Close()
		srv.SetSFU(media)
		log.Info("relay media", "udp", media.Addr().String())
	}
	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}

	if cfg.MetricsListen != "" {
		reg := ops.NewRegistry()
		srv.RegisterMetrics(reg)
		if _, err := ops.ServeStatus(ctx, cfg.MetricsListen, ops.Status{Registry: reg}, log); err != nil {
			return err
		}
	}

	if cfg.Dedicated() {
		go srv.WatchDedicatedKey(ctx)
		log.Info("relay dedicated to one community server", "key_known", srv.DedicatedKnown())
	}

	if len(cfg.RegistryURLs) > 0 {
		hb, err := relay.NewHeartbeater(cfg, srv, log)
		if err != nil {
			return err
		}
		go hb.Run(ctx)
	}

	log.Info("relay listening", "addr", ln.Addr().String(), "region", cfg.Region, "tls", cfg.TLSMode, "public_url", cfg.AnnouncedURL())
	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(ln) }()
	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = hs.Shutdown(shutdownCtx)
	srv.CloseAll()
	return nil
}
