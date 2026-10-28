// Command site serves the project website, OpenArcanum.org: announcements,
// bug and security report forms, and the admin portal at /admin
// (docs/operations.md "Website").
//
//	site [-config site.json] [-v]                       serve
//	site admin add|passwd NAME [-config F | -data DIR]  create an admin / change a passphrase (prompts)
//	site admin remove|unlock NAME [-config F | -data DIR]
//	site admin totp NAME [-off] [-config F | -data DIR] turn two-factor sign-in on (or off)
//	site admin list [-config F | -data DIR]
//	site backup [-config F | -data DIR] [-out DIR] [-keep N]
//	site healthcheck URL                                exit 0 if URL answers 200
//
// Settings come from the JSON config file, then OPENARCANUM_SITE_* environment
// variables (internal/site/config.go).
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/hyperkubeorg/openarcanum/internal/config"
	"github.com/hyperkubeorg/openarcanum/internal/httpx"
	"github.com/hyperkubeorg/openarcanum/internal/ops"
	"github.com/hyperkubeorg/openarcanum/internal/site"
)

// EnvPrefix is the environment prefix of the site's settings.
const EnvPrefix = "OPENARCANUM_SITE_"

func main() {
	if len(os.Args) > 1 {
		var err error
		switch os.Args[1] {
		case "healthcheck":
			os.Exit(ops.Healthcheck(os.Args[2:], os.Stdout, os.Stderr))
		case "admin":
			err = adminCommand(context.Background(), os.Args[2:], os.Stdin, os.Stdout)
		case "backup":
			err = backupCommand(context.Background(), os.Args[2:], os.Stdout)
		default:
			if !strings.HasPrefix(os.Args[1], "-") {
				err = fmt.Errorf("unknown command %q (admin, backup, healthcheck, or flags to serve)", os.Args[1])
			} else {
				err = serve()
			}
		}
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "site:", err)
			os.Exit(1)
		}
		return
	}
	if err := serve(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "site:", err)
		os.Exit(1)
	}
}

func serve() error {
	cfgPath := flag.String("config", "", "path to the JSON config file")
	verbose := flag.Bool("v", false, "debug logging")
	flag.Parse()

	cfg := site.DefaultConfig()
	if err := config.Load(*cfgPath, EnvPrefix, &cfg); err != nil {
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
	access, closeAccess, err := accessLog(cfg)
	if err != nil {
		return fmt.Errorf("access_log: %w", err)
	}
	defer func() { _ = closeAccess() }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := site.OpenStore(ctx, cfg.DataDir, cfg.KeyFile(), true)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	if admins, err := st.Admins(ctx); err == nil && len(admins) == 0 {
		log.Warn("no admin accounts yet: create one with `site admin add NAME`")
	}
	if !cfg.Enabled() {
		log.Warn("tls_mode is off: admin sign-in cookies are Secure, so browsers only send them over https (or to localhost); put a TLS proxy in front or set tls_mode")
	}
	srv, err := site.New(cfg, st, log)
	if err != nil {
		return err
	}

	ln, err := ops.ListenPublic(ctx, cfg.Listen, cfg.TLSSettings, ops.TLSOptions{CacheDir: cfg.ACMECache(), HTTP2: true, Log: log})
	if err != nil {
		return err
	}
	hs := httpx.NewServer(srv.Handler(access), httpx.DefaultTimeouts(), log, cfg.LogClientIPs)
	servers := []*http.Server{hs}
	errc := make(chan error, 2)
	go func() { errc <- hs.Serve(ln) }()

	if cfg.HTTPListen != "" {
		pl, err := net.Listen("tcp", cfg.HTTPListen)
		if err != nil {
			return fmt.Errorf("http_listen: %w", err)
		}
		redirect := httpx.Wrap(httpx.RedirectHTTPS(cfg.PublicHost()), httpx.Options{AccessLog: access, LogClientIPs: cfg.LogClientIPs})
		rs := httpx.NewServer(redirect, httpx.DefaultTimeouts(), log, cfg.LogClientIPs)
		servers = append(servers, rs)
		go func() { errc <- rs.Serve(pl) }()
		log.Info("redirecting plain HTTP to https", "addr", pl.Addr().String())
	}
	if cfg.MetricsListen != "" {
		reg := ops.NewRegistry()
		token, err := cfg.MetricsToken()
		if err != nil {
			return err
		}
		if _, err := ops.ServeStatus(ctx, cfg.MetricsListen, ops.Status{Registry: reg, Live: st.Ping, Token: token}, log); err != nil {
			return err
		}
	}
	go srv.Run(ctx)
	log.Info("website listening", "addr", ln.Addr().String(), "tls", cfg.TLSMode, "log_client_ips", cfg.LogClientIPs)

	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(sctx)
	}
	return nil
}

// accessLog opens access_log: stderr, off (nil), or a rotated file.
func accessLog(cfg site.Config) (io.Writer, func() error, error) {
	switch cfg.AccessLog {
	case "stderr":
		return os.Stderr, func() error { return nil }, nil
	case "off":
		return nil, func() error { return nil }, nil
	}
	f, err := ops.OpenRotating(cfg.AccessLog, cfg.LogMaxSize, cfg.LogMaxAge.Duration, cfg.LogKeep, os.Stderr)
	if err != nil {
		return nil, nil, err
	}
	return f, f.Close, nil
}

// ---------------------------------------------------------------- admin commands

// dataFlags are the flags every offline command takes to find the data.
type dataFlags struct {
	config, data *string
}

func newDataFlags(fs *flag.FlagSet) dataFlags {
	return dataFlags{
		config: fs.String("config", "", "the site's JSON config file (for data_dir and storage_key_file)"),
		data:   fs.String("data", "", "the site's data directory (overrides the config)"),
	}
}

func (d dataFlags) load() (site.Config, error) {
	cfg := site.DefaultConfig()
	if err := config.Load(*d.config, EnvPrefix, &cfg); err != nil {
		return cfg, err
	}
	if *d.data != "" {
		cfg.DataDir = *d.data
	}
	if cfg.DataDir == "" {
		return cfg, errors.New("say where the site's data is: -data DIR, -config FILE, or OPENARCANUM_SITE_DATA_DIR")
	}
	return cfg, nil
}

// parseNamed parses "NAME [flags]" or "[flags] NAME".
func parseNamed(fs *flag.FlagSet, args []string) (string, error) {
	var name string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if name == "" && fs.NArg() > 0 {
		name = fs.Arg(0)
	}
	if name == "" {
		return "", errors.New("an admin NAME is required")
	}
	return name, nil
}

const adminUsage = `usage: site admin add|passwd|remove|unlock|totp NAME [-config F | -data DIR]
       site admin list [-config F | -data DIR]`

func adminCommand(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(adminUsage)
	}
	sub := args[0]
	fs := flag.NewFlagSet("site admin "+sub, flag.ContinueOnError)
	df := newDataFlags(fs)
	off := fs.Bool("off", false, "totp: turn two-factor sign-in off")
	var name string
	var err error
	if sub == "list" {
		err = fs.Parse(args[1:])
	} else {
		name, err = parseNamed(fs, args[1:])
	}
	if err != nil {
		return err
	}
	cfg, err := df.load()
	if err != nil {
		return err
	}
	st, err := site.OpenStore(ctx, cfg.DataDir, cfg.KeyFile(), true)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	pr := newPrompter(in, out)

	switch sub {
	case "add":
		if err := site.ValidAdminName(name); err != nil {
			return err
		}
		if _, err := st.AdminByName(ctx, name); err == nil {
			return fmt.Errorf("an admin named %q already exists (site admin passwd %s changes the passphrase)", name, name)
		}
		pass, err := pr.newPassphrase()
		if err != nil {
			return err
		}
		if err := st.AddAdmin(ctx, name, site.HashPassphrase(pass)); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "Admin %s added. Sign in at /admin. Turn on two-factor sign-in with: site admin totp %s\n", name, name)
	case "passwd":
		if _, err := st.AdminByName(ctx, name); err != nil {
			return fmt.Errorf("no admin named %q", name)
		}
		pass, err := pr.newPassphrase()
		if err != nil {
			return err
		}
		if err := st.SetPassphrase(ctx, name, site.HashPassphrase(pass)); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "Passphrase changed; %s's sessions were ended.\n", name)
	case "remove":
		if err := st.RemoveAdmin(ctx, name); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "Admin %s removed.\n", name)
	case "unlock":
		if err := st.Unlock(ctx, name); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "Admin %s unlocked.\n", name)
	case "totp":
		if _, err := st.AdminByName(ctx, name); err != nil {
			return fmt.Errorf("no admin named %q", name)
		}
		if *off {
			if err := st.SetTOTP(ctx, name, nil); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(out, "Two-factor sign-in is off for %s.\n", name)
			return nil
		}
		secret := site.NewTOTPSecret()
		_, _ = fmt.Fprintf(out, "Add this key to an authenticator app (time-based, 6 digits, 30 seconds):\n\n  %s\n\nor open this URI on the device:\n\n  %s\n\n",
			site.TOTPKeyText(secret), site.TOTPURI(site.ProductName, name, secret))
		code, err := pr.line("Enter the code the app shows now to confirm: ")
		if err != nil {
			return err
		}
		if !site.VerifyTOTPOnce(secret, code, time.Now()) {
			return errors.New("that code doesn't match; nothing was changed (check the device's clock, and run the command again)")
		}
		if err := st.SetTOTP(ctx, name, secret); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "Two-factor sign-in is on for %s; their sessions were ended.\n", name)
	case "list":
		admins, err := st.Admins(ctx)
		if err != nil {
			return err
		}
		if len(admins) == 0 {
			_, _ = fmt.Fprintln(out, "No admins. Add one with: site admin add NAME")
		}
		for _, a := range admins {
			state := ""
			if time.Now().Before(a.LockedUntil) {
				state = ", locked until " + a.LockedUntil.UTC().Format(time.RFC3339)
			}
			twoFA := "off"
			if a.TOTP {
				twoFA = "on"
			}
			last := "never"
			if !a.LastLogin.IsZero() {
				last = a.LastLogin.UTC().Format(time.RFC3339)
			}
			_, _ = fmt.Fprintf(out, "%s\ttwo-factor %s\tlast sign-in %s%s\n", a.Name, twoFA, last, state)
		}
	default:
		return errors.New(adminUsage)
	}
	return nil
}

// prompter reads passphrases without echo from a terminal, or lines from
// a pipe (scripts: `printf '%s\n' "$PASS" | site admin add NAME`).
type prompter struct {
	in  io.Reader
	out io.Writer
	br  *bufio.Reader
	tty bool
	fd  int
}

func newPrompter(in io.Reader, out io.Writer) *prompter {
	p := &prompter{in: in, out: out, br: bufio.NewReader(in)}
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		p.tty, p.fd = true, int(f.Fd())
	}
	return p
}

func (p *prompter) line(prompt string) (string, error) {
	_, _ = fmt.Fprint(p.out, prompt)
	s, err := p.br.ReadString('\n')
	if err != nil && (s == "" || !errors.Is(err, io.EOF)) {
		return "", errors.New("no input")
	}
	return strings.TrimRight(s, "\r\n"), nil
}

func (p *prompter) secret(prompt string) (string, error) {
	if !p.tty {
		return p.line(prompt)
	}
	_, _ = fmt.Fprint(p.out, prompt)
	b, err := term.ReadPassword(p.fd)
	_, _ = fmt.Fprintln(p.out)
	return string(b), err
}

func (p *prompter) newPassphrase() (string, error) {
	pass, err := p.secret(fmt.Sprintf("New passphrase (at least %d characters): ", site.MinPassphrase))
	if err != nil {
		return "", err
	}
	if err := site.CheckPassphraseStrength(pass); err != nil {
		return "", err
	}
	if p.tty {
		again, err := p.secret("Repeat it: ")
		if err != nil {
			return "", err
		}
		if again != pass {
			return "", errors.New("the passphrases differ")
		}
	}
	return pass, nil
}

// ---------------------------------------------------------------- backup

func backupCommand(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("site backup", flag.ContinueOnError)
	df := newDataFlags(fs)
	outDir := fs.String("out", "", "where to write the backup (default DATA/backups)")
	keep := fs.Int("keep", 14, "backups to keep in -out (0 keeps all)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := df.load()
	if err != nil {
		return err
	}
	if *outDir == "" {
		*outDir = filepath.Join(cfg.DataDir, "backups")
	}
	st, err := site.OpenStore(ctx, cfg.DataDir, cfg.KeyFile(), false)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	p, err := st.Backup(ctx, *outDir, *keep)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "%s\nReports in it are encrypted: keep a copy of %s apart from the backups to restore it.\n", p, cfg.KeyFile())
	return nil
}
