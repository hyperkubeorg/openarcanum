package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/config"
	"github.com/hyperkubeorg/openarcanum/internal/identity"
)

// `community init`: create a server's data directory, write its
// config.json and print the one-time claim code. It runs from flags, or
// as a guided setup that asks where the server runs (home network or VPS)
// and explains what that means for the network (docs/admin-guide.md
// "Installing a community server").

const initUsage = `usage: community init -data DIR (-relay URL... | -automatic-relays -maintainer URL...) [flags]
       community init -interactive          guided setup (the default when run in a terminal without -data)

flags:
  -data DIR              data directory to create
  -relay URL             relay to attach to, ws:// or wss:// (repeatable)
  -automatic-relays      pick relays from the maintainers' registry instead
  -maintainer URL        maintainer node, http(s):// (repeatable)
  -storage-key PATH      where to keep the storage key (default
                         $OPENARCANUM_COMMUNITY_STORAGE_KEY_FILE, else DATA/storage.key)
  -install home|vps      where the server runs; only changes the advice printed
  -metrics-listen ADDR   turn on the health and metrics listener (e.g. 127.0.0.1:7201)
  -claim-file PATH       also write the claim code to PATH (owner-only); with
                         -if-needed it's printed again on later runs; the
                         server deletes it once it's claimed
  -if-needed             do nothing (exit 0) if DIR is already initialized,
                         for container start-up (Kubernetes init containers)
  -interactive           ask instead of reading flags`

// Default status listener address offered by the guided setup.
const defaultCommunityMetrics = "127.0.0.1:7201"

type initOptions struct {
	data, storageKey, install, metrics, claimFile string
	automatic, ifNeeded                           bool
	relays, maintainers                           []string
}

// initCommand runs `community init`. terminal says whether stdin is an
// interactive terminal, which makes the guided setup the default when no
// -data is given.
func initCommand(args []string, in io.Reader, out io.Writer, terminal bool) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var o initOptions
	var relays, maintainers multi
	fs.StringVar(&o.data, "data", "", "")
	fs.StringVar(&o.storageKey, "storage-key", "", "")
	fs.StringVar(&o.install, "install", "", "")
	fs.StringVar(&o.metrics, "metrics-listen", "", "")
	fs.BoolVar(&o.automatic, "automatic-relays", false, "")
	fs.StringVar(&o.claimFile, "claim-file", "", "")
	fs.BoolVar(&o.ifNeeded, "if-needed", false, "")
	interactive := fs.Bool("interactive", false, "")
	fs.Var(&relays, "relay", "")
	fs.Var(&maintainers, "maintainer", "")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, _ = fmt.Fprintln(out, initUsage)
			return nil
		}
		return fmt.Errorf("%w\n%s", err, initUsage)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q\n%s", fs.Arg(0), initUsage)
	}
	o.relays, o.maintainers = relays, maintainers
	if o.storageKey == "" {
		// The container image points this at its separate key volume.
		o.storageKey = os.Getenv("OPENARCANUM_COMMUNITY_STORAGE_KEY_FILE")
	}
	if *interactive || (o.data == "" && terminal) {
		w := &wizard{in: bufio.NewReader(in), out: out}
		var err error
		if o, err = w.run(o); err != nil {
			return err
		}
	} else if o.data == "" {
		return errors.New("-data is required (or run `community init -interactive` for the guided setup)\n" + initUsage)
	}
	if o.install != "" && o.install != "home" && o.install != "vps" {
		return fmt.Errorf("-install must be \"home\" or \"vps\", not %q", o.install)
	}
	cfg, err := o.config()
	if err != nil {
		return err
	}
	if o.ifNeeded && initialized(cfg.DataDir) {
		_, _ = fmt.Fprintf(out, "%s is already initialized; nothing to do.\n", cfg.DataDir)
		if o.claimFile != "" {
			if code, err := os.ReadFile(o.claimFile); err == nil {
				_, _ = fmt.Fprintln(out, "Claim code (the server hasn't been claimed yet):")
				_, _ = fmt.Fprintln(out, strings.TrimSpace(string(code)))
			}
		}
		return nil
	}
	res, err := community.Init(context.Background(), cfg)
	if err != nil {
		return err
	}
	cfgFile := filepath.Join(cfg.DataDir, "config.json")
	if err := config.Save(cfgFile, cfg); err != nil {
		return err
	}
	if o.claimFile != "" {
		if err := os.WriteFile(o.claimFile, []byte(res.ClaimCode+"\n"), 0o600); err != nil {
			return fmt.Errorf("claim file: %w", err)
		}
	}
	_, _ = fmt.Fprintf(out, "Server key:  %s\n", identity.Encode(res.ServerKey))
	// A dedicated relay's dedicated_server_key takes the hex form.
	_, _ = fmt.Fprintf(out, "  (hex, for a dedicated relay: %s)\n", hex.EncodeToString(res.ServerKey))
	_, _ = fmt.Fprintf(out, "Config:      %s\n", cfgFile)
	_, _ = fmt.Fprintf(out, "Storage key: %s\n", cfg.StorageKeyFile)
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintln(out, "Claim code (in the app: + → Host on your own server; it works once):")
	_, _ = fmt.Fprintln(out, res.ClaimCode)
	_, _ = fmt.Fprintln(out)
	printNextSteps(out, o, cfg)
	return nil
}

// initialized reports whether dir already holds a server (its key).
func initialized(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "server.key"))
	return err == nil
}

func (o initOptions) config() (community.Config, error) {
	cfg := community.DefaultConfig(o.data)
	cfg.StorageKeyFile = o.storageKey
	cfg.Relays = o.relays
	cfg.Maintainers = o.maintainers
	if o.automatic {
		cfg.RelayMode = "automatic"
	}
	cfg.MetricsListen = o.metrics
	cfg.ClaimFile = o.claimFile
	cfg.Resolve()
	return cfg, cfg.Validate()
}

func printNextSteps(out io.Writer, o initOptions, cfg community.Config) {
	_, _ = fmt.Fprintln(out, "Next steps:")
	_, _ = fmt.Fprintf(out, "  1. Start it:  community run -data %s   (or the systemd unit / container image, docs/admin-guide.md)\n", cfg.DataDir)
	_, _ = fmt.Fprintln(out, "  2. Claim it in the app with the code above.")
	_, _ = fmt.Fprintf(out, "  3. Back up %s somewhere other than this disk now: backups (in %s) can't be read without it.\n", cfg.StorageKeyFile, cfg.BackupDir)
	switch o.install {
	case "home":
		_, _ = fmt.Fprintln(out, "  Network: no port forwarding and no DMZ. The server only connects out.")
	case "vps":
		_, _ = fmt.Fprintln(out, "  Network: no inbound port. Firewall: allow outgoing; deny incoming except SSH.")
	}
	if cfg.MetricsListen != "" {
		_, _ = fmt.Fprintf(out, "  Health: http://%s/healthz and /readyz; metrics at /metrics.\n", cfg.MetricsListen)
	}
}

// ---------------------------------------------------------------- guided setup

type wizard struct {
	in  *bufio.Reader
	out io.Writer
}

// errInputEnded means stdin closed before the setup finished.
var errInputEnded = errors.New("input ended before the setup finished; nothing was created")

func (w *wizard) say(format string, a ...any) { _, _ = fmt.Fprintf(w.out, format, a...) }

// ask prompts until check accepts the answer (an empty answer means def).
func (w *wizard) ask(prompt, def string, check func(string) error) (string, error) {
	for {
		if def != "" {
			w.say("%s [%s]: ", prompt, def)
		} else {
			w.say("%s: ", prompt)
		}
		line, err := w.in.ReadString('\n')
		if err != nil && (line == "" || !errors.Is(err, io.EOF)) {
			w.say("\n")
			return "", errInputEnded
		}
		ans := strings.TrimSpace(line)
		if ans == "" {
			ans = def
		}
		if cerr := check(ans); cerr != nil {
			w.say("  %v\n", cerr)
			if errors.Is(err, io.EOF) {
				return "", errInputEnded
			}
			continue
		}
		return ans, nil
	}
}

func (w *wizard) choose(prompt string, options []string, def string) (string, error) {
	return w.ask(prompt+" ("+strings.Join(options, "/")+")", def, func(s string) error {
		for _, o := range options {
			if strings.EqualFold(s, o) {
				return nil
			}
		}
		return fmt.Errorf("answer one of: %s", strings.Join(options, ", "))
	})
}

func (w *wizard) yes(prompt string, def bool) (bool, error) {
	d := "n"
	if def {
		d = "y"
	}
	a, err := w.choose(prompt, []string{"y", "n"}, d)
	return strings.EqualFold(a, "y"), err
}

// list asks for URLs, comma-separated or one per line, ending with an
// empty line.
func (w *wizard) list(prompt string, check func(string) error, min int) ([]string, error) {
	var out []string
	w.say("%s\n  (one per line or comma-separated; an empty line ends the list)\n", prompt)
	for {
		line, err := w.ask("  URL", "", func(s string) error {
			for _, u := range splitList(s) {
				if err := check(u); err != nil {
					return err
				}
			}
			if s == "" && len(out) < min {
				return fmt.Errorf("enter at least %d", min)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if line == "" {
			return out, nil
		}
		out = append(out, splitList(line)...)
	}
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func checkMaintainer(s string) error {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%q is not an http(s):// URL", s)
	}
	return nil
}

func defaultDataDir() string {
	if os.Geteuid() == 0 {
		return "/var/lib/openarcanum-community"
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "share", "openarcanum-community")
	}
	return "openarcanum-community"
}

const homeAdvice = `
At home, behind your router:
  The community server never accepts incoming connections. It connects out
  to relays (WebSocket) and to maintainer nodes (HTTPS), and members reach it
  through those relays. So:
    - you do NOT need port forwarding for it;
    - do NOT put this machine in your router's DMZ ("exposed host"): that
      sends every unsolicited connection from the internet to it and gains
      nothing here. Keep it behind the router's firewall like any other device;
    - it keeps working when your home address changes.
  Only if you also run your own relay at home does that relay need ports:
  forward TCP 7000 (or 443 behind a TLS proxy) and UDP 7000 (call media) to
  the relay machine, those ports only, never a DMZ.
`

const vpsAdvice = `
On a VPS or a server with a public address:
  The community server never accepts incoming connections: it connects out
  to relays and maintainer nodes. Open no inbound port for it. A firewall that
  allows outgoing traffic and denies incoming traffic except SSH is right.
  If a relay runs on the same machine, open only its ports (TCP 7000 or 443
  behind a TLS proxy, and UDP 7000 for call media).
  Keep the storage key off the VPS disk image if your provider snapshots it.
`

// run asks for everything flags didn't give and returns the options.
func (w *wizard) run(o initOptions) (initOptions, error) {
	w.say("OpenArcanum community server setup\n\nThis creates a data directory, a storage key and config.json, then prints the\nclaim code you paste into the app. Press Enter to accept a [default].\n\n")
	var err error
	if o.install == "" {
		w.say("Where does this server run?\n  home  a computer on your home network, behind a router (a Raspberry Pi, a NAS, a spare PC)\n  vps   a VPS or dedicated server with a public address\n")
		if o.install, err = w.choose("Install type", []string{"home", "vps"}, "home"); err != nil {
			return o, err
		}
		o.install = strings.ToLower(o.install)
	}
	if o.install == "home" {
		w.say("%s\n", homeAdvice)
	} else {
		w.say("%s\n", vpsAdvice)
	}
	if o.data == "" {
		if o.data, err = w.ask("Data directory", defaultDataDir(), func(s string) error {
			if _, err := os.Stat(filepath.Join(s, "server.key")); err == nil {
				return fmt.Errorf("%s already holds a server; choose another directory", s)
			}
			return nil
		}); err != nil {
			return o, err
		}
	}
	if len(o.relays) == 0 && !o.automatic {
		w.say("\nRelays carry your members' connections to this server.\n  auto    pick relays from the maintainer nodes' registry (and switch when one fails)\n  pinned  use relays you name (your own, or ones you trust)\n")
		mode, err := w.choose("Relays", []string{"auto", "pinned"}, "auto")
		if err != nil {
			return o, err
		}
		if strings.EqualFold(mode, "auto") {
			o.automatic = true
		} else if o.relays, err = w.list("Relay URLs (ws:// or wss://…/ws)", community.ValidRelayURL, 1); err != nil {
			return o, err
		}
	}
	if len(o.maintainers) == 0 {
		min := 0
		prompt := "Maintainer node URLs, where the community's route and directory listing are published (recommended; empty to skip)"
		if o.automatic {
			min = 1
			prompt = "Maintainer node URLs (https://…; needed to find relays automatically)"
		}
		if o.maintainers, err = w.list(prompt, checkMaintainer, min); err != nil {
			return o, err
		}
	}
	if o.storageKey == "" {
		w.say("\nThe storage key encrypts everything the server stores. Backups are useless\nwithout it, and anyone holding both can read them: keep a copy somewhere\nother than this disk (a USB stick, a password manager).\n")
		if o.storageKey, err = w.ask("Storage key file", filepath.Join(o.data, "storage.key"), func(s string) error {
			if !filepath.IsAbs(s) {
				return errors.New("use an absolute path")
			}
			return nil
		}); err != nil {
			return o, err
		}
	}
	if o.metrics == "" {
		w.say("\nA health and metrics listener lets a monitor or container health check see\nthat the server works (/healthz, /readyz, Prometheus /metrics; counts only,\nno content or member ids). On 127.0.0.1 only this machine can reach it.\n")
		on, err := w.yes("Turn it on at "+defaultCommunityMetrics, false)
		if err != nil {
			return o, err
		}
		if on {
			o.metrics = defaultCommunityMetrics
		}
	}
	if _, err := o.config(); err != nil {
		return o, err
	}
	w.say("\nSummary\n  install:      %s\n  data:         %s\n  storage key:  %s\n", o.install, o.data, o.storageKey)
	if o.automatic {
		w.say("  relays:       automatic, from the maintainers\n")
	} else {
		w.say("  relays:       %s\n", strings.Join(o.relays, ", "))
	}
	maint := strings.Join(o.maintainers, ", ")
	if maint == "" {
		maint = "none (the community won't be in the directory; members join by invite)"
	}
	w.say("  maintainers:  %s\n", maint)
	status := "off"
	if o.metrics != "" {
		status = o.metrics
	}
	w.say("  health/metrics: %s\n\n", status)
	ok, err := w.yes("Create it", true)
	if err != nil {
		return o, err
	}
	if !ok {
		return o, errors.New("setup cancelled; nothing was created")
	}
	w.say("\n")
	return o, nil
}
