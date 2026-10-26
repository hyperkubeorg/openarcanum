// Package ops is what every node binary shares for running in production:
// logging (structured, optionally to a rotated file), Prometheus metrics,
// the status listener (/healthz, /readyz, /metrics) and the `healthcheck`
// command used by container health checks (docs/operations.md
// "Logs, health and metrics"), and TLS on the public listener, with
// certificates from ACME or files (tls.go; docs/operations.md "TLS and
// browsers").
package ops

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/mail"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/config"
)

// Settings are the logging and monitoring settings every node has. Each
// node's Config embeds them, so the keys are the same in every config file
// and environment prefix (OPENARCANUM_RELAY_LOG_FILE, OPENARCANUM_META_LOG_FILE, …).
type Settings struct {
	// LogFormat is "text" (logfmt-style key=value) or "json".
	LogFormat string `json:"log_format" env:"LOG_FORMAT"`
	// LogLevel is "info" or "debug" (-v also selects debug).
	LogLevel string `json:"log_level" env:"LOG_LEVEL"`
	// LogFile, when set, is where logs go instead of stderr. The file is
	// rotated in-process (logfile.go): when it reaches LogMaxSize bytes or
	// is LogMaxAge old, and LogKeep rotated files are kept.
	LogFile    string          `json:"log_file" env:"LOG_FILE"`
	LogMaxSize int64           `json:"log_max_size" env:"LOG_MAX_SIZE"`
	LogMaxAge  config.Duration `json:"log_max_age" env:"LOG_MAX_AGE"`
	LogKeep    int             `json:"log_keep" env:"LOG_KEEP"`
	// MetricsListen is the address of the status listener (/healthz,
	// /readyz, /metrics). Empty turns it off. It is separate from the
	// node's public listener so it can stay on loopback or a private
	// network.
	MetricsListen string `json:"metrics_listen" env:"METRICS_LISTEN"`
}

// DefaultSettings are the documented defaults: text logs at info level to
// stderr (journald or the container runtime collects and rotates them),
// and no status listener.
func DefaultSettings() Settings {
	return Settings{
		LogFormat:  "text",
		LogLevel:   "info",
		LogMaxSize: 50 << 20,
		LogMaxAge:  config.Duration{Duration: 24 * time.Hour},
		LogKeep:    7,
	}
}

// Validate rejects settings a node can't run with.
func (s *Settings) Validate() error {
	switch {
	case s.LogFormat != "text" && s.LogFormat != "json":
		return fmt.Errorf("log_format must be \"text\" or \"json\", not %q", s.LogFormat)
	case s.LogLevel != "info" && s.LogLevel != "debug":
		return fmt.Errorf("log_level must be \"info\" or \"debug\", not %q", s.LogLevel)
	case s.LogMaxSize < 1<<20:
		return errors.New("log_max_size must be at least 1048576 (1 MiB)")
	case s.LogMaxAge.Duration < 0 || (s.LogMaxAge.Duration > 0 && s.LogMaxAge.Duration < time.Minute):
		return errors.New("log_max_age must be 0 (rotate by size only) or at least 1m")
	case s.LogKeep < 1 || s.LogKeep > 1000:
		return errors.New("log_keep must be between 1 and 1000")
	}
	if s.MetricsListen != "" {
		host, port, err := net.SplitHostPort(s.MetricsListen)
		if err != nil || port == "" {
			return fmt.Errorf("metrics_listen %q must be host:port (for example 127.0.0.1:7101)", s.MetricsListen)
		}
		if host != "" && net.ParseIP(host) == nil && host != "localhost" {
			return fmt.Errorf("metrics_listen %q: the host must be an IP address or localhost", s.MetricsListen)
		}
	}
	return nil
}

// ExposedAddr reports whether a listen address (host:port, as listen and
// metrics_listen are written) accepts connections from other machines:
// the host is empty, unspecified (0.0.0.0, ::) or not a loopback address.
func ExposedAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	return host == "" || !loopbackHost(host)
}

// WarnPlainListener logs a warning when the public listener at addr
// (listen) serves other machines without TLS: tls_mode is off and the
// address isn't on loopback. The nodes default to that for tests and
// local networks; in production browsers need wss:// and https://
// (docs/operations.md "TLS and browsers").
func WarnPlainListener(log *slog.Logger, addr string, s TLSSettings) {
	if log == nil || s.Enabled() || !ExposedAddr(addr) {
		return
	}
	log.Warn("tls_mode is off and listen is not on loopback: other machines reach this node over plain http:// and ws://; set tls_mode (acme or files) or put a TLS proxy in front", "addr", addr)
}

// TLSSettings are the public listener's TLS settings, shared by the nodes
// that serve the public directly (the relay and the maintainer node; tls.go,
// docs/operations.md "TLS and browsers"). Browser clients on an HTTPS page
// can only reach wss:// and https:// endpoints, so production nodes serve
// TLS themselves or sit behind a proxy that does.
type TLSSettings struct {
	// TLSMode is "off" (plain HTTP and ws://), "acme" (certificates from an
	// ACME CA such as Let's Encrypt, obtained and renewed automatically) or
	// "files" (a certificate and key read from tls_cert_file and
	// tls_key_file, reloaded when they change).
	TLSMode string `json:"tls_mode" env:"TLS_MODE"`
	// TLSDomains are the names (and public IP addresses) the node is reached
	// at. With acme, the node requests certificates for them; the first one
	// also makes a relay's default public_url (wss://NAME/ws).
	TLSDomains []string `json:"tls_domains" env:"TLS_DOMAINS"`
	// TLSCertFile (PEM chain) and TLSKeyFile (PEM key) are used with files.
	TLSCertFile string `json:"tls_cert_file" env:"TLS_CERT_FILE"`
	TLSKeyFile  string `json:"tls_key_file" env:"TLS_KEY_FILE"`
	// ACMECacheDir keeps the ACME account key and the certificates across
	// restarts (mode 0700). Without it every start would ask the CA for new
	// certificates and soon run into its rate limits.
	ACMECacheDir string `json:"acme_cache_dir" env:"ACME_CACHE_DIR"`
	// ACMEEmail is the optional contact address given to the CA.
	ACMEEmail string `json:"acme_email" env:"ACME_EMAIL"`
	// ACMEDirectoryURL is the CA's ACME directory (Let's Encrypt production
	// by default).
	ACMEDirectoryURL string `json:"acme_directory_url" env:"ACME_DIRECTORY_URL"`
}

// TLS modes.
const (
	TLSOff   = "off"
	TLSACME  = "acme"
	TLSFiles = "files"
)

// LetsEncryptDirectory is acme_directory_url's default.
const LetsEncryptDirectory = "https://acme-v02.api.letsencrypt.org/directory"

// DefaultTLSSettings are the documented defaults: TLS off (tests and local
// networks; docs/operations.md recommends acme in production), Let's
// Encrypt as the CA.
func DefaultTLSSettings() TLSSettings {
	return TLSSettings{TLSMode: TLSOff, ACMEDirectoryURL: LetsEncryptDirectory}
}

// Enabled reports whether the public listener serves TLS.
func (s *TLSSettings) Enabled() bool { return s.TLSMode == TLSACME || s.TLSMode == TLSFiles }

// Validate rejects TLS settings the node can't run with. The ACME cache
// directory is checked by each node, which knows its default.
func (s *TLSSettings) Validate() error {
	switch s.TLSMode {
	case TLSOff, TLSACME, TLSFiles:
	default:
		return fmt.Errorf("tls_mode must be \"off\", \"acme\" or \"files\", not %q", s.TLSMode)
	}
	for _, d := range s.TLSDomains {
		if _, err := tlsName(d); err != nil {
			return fmt.Errorf("tls_domains entry %q: %w", d, err)
		}
	}
	files := s.TLSCertFile != "" || s.TLSKeyFile != ""
	switch {
	case s.TLSMode == TLSACME && len(s.TLSDomains) == 0:
		return errors.New("tls_mode acme needs tls_domains: the names (or public IP addresses) the node is reached at")
	case s.TLSMode == TLSFiles && (s.TLSCertFile == "" || s.TLSKeyFile == ""):
		return errors.New("tls_mode files needs tls_cert_file and tls_key_file")
	case s.TLSMode != TLSFiles && files:
		return errors.New("tls_cert_file and tls_key_file are only used with tls_mode files")
	}
	if s.ACMEEmail != "" {
		if a, err := mail.ParseAddress(s.ACMEEmail); err != nil || a.Address != s.ACMEEmail {
			return fmt.Errorf("acme_email %q must be a plain e-mail address", s.ACMEEmail)
		}
	}
	if s.TLSMode == TLSACME {
		u, err := url.Parse(s.ACMEDirectoryURL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && (u.Scheme != "http" || !loopbackHost(u.Hostname()))) {
			return fmt.Errorf("acme_directory_url %q must be an https:// URL (http:// only on loopback, for a local test CA)", s.ACMEDirectoryURL)
		}
	}
	return nil
}

// PublicHost is the first of tls_domains as a URL host (IPv6 in brackets),
// or "" without one.
func (s *TLSSettings) PublicHost() string {
	if len(s.TLSDomains) == 0 {
		return ""
	}
	n, err := tlsName(s.TLSDomains[0])
	if err != nil {
		return ""
	}
	if strings.Contains(n, ":") {
		return "[" + n + "]"
	}
	return n
}

// tlsName is a tls_domains entry in canonical form: a lower-case DNS name
// without a trailing dot, or an IP address in its standard text form.
// Wildcards aren't accepted: the ACME challenge the node answers
// (tls-alpn-01) can't prove them.
func tlsName(s string) (string, error) {
	if a, err := netip.ParseAddr(s); err == nil {
		if a.Zone() != "" {
			return "", errors.New("an IPv6 zone is not allowed")
		}
		return a.Unmap().String(), nil
	}
	n := strings.TrimSuffix(strings.ToLower(s), ".")
	if n == "" || len(n) > 253 || !strings.Contains(n, ".") {
		return "", errors.New("must be a fully qualified DNS name or an IP address")
	}
	for _, label := range strings.Split(n, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("must be a fully qualified DNS name or an IP address")
		}
		for _, c := range label {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return "", errors.New("must be an ASCII DNS name (the xn-- form for others; no wildcards) or an IP address")
			}
		}
	}
	return n, nil
}

// loopbackHost: localhost or a loopback address. An unspecified address
// (0.0.0.0, ::) means every interface, so it isn't one.
func loopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	a, err := netip.ParseAddr(h)
	return err == nil && a.IsLoopback()
}
