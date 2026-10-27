// Package site is the project website (OpenArcanum.org): a landing page with
// announcements, public bug and security report forms, and an admin portal
// (/admin) for announcements and report triage. It is served by cmd/site
// (docs/operations.md "Website").
//
// Everything is rendered on the server with html/template; the only scripts
// are the report forms' proof-of-work solver, the theme switch and the copy
// buttons, all served from the site itself under a strict Content Security
// Policy. Reports are sealed at rest (internal/atrest) with a key file kept
// next to, not inside, the database.
package site

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/config"
	"github.com/hyperkubeorg/openarcanum/internal/ops"
	"github.com/hyperkubeorg/openarcanum/internal/pow"
)

// Config is the site's config file and OPENARCANUM_SITE_* environment.
type Config struct {
	// Listen is the public listener (HTTPS unless tls_mode is off).
	Listen string `json:"listen" env:"LISTEN"`
	// HTTPListen, when set, is a plain-HTTP listener (":80") that redirects
	// everything to https://. Only with TLS on.
	HTTPListen string `json:"http_listen" env:"HTTP_LISTEN"`
	// DataDir holds site.db (mode 0700); required.
	DataDir string `json:"data_dir" env:"DATA_DIR"`
	// StorageKeyFile is the key that seals reports and second factors at
	// rest (created on first start). Default DATA/storage.key; keep a copy
	// apart from the backups.
	StorageKeyFile string `json:"storage_key_file" env:"STORAGE_KEY_FILE"`
	// AccessLog is where the Apache combined-format access log goes:
	// "stderr" (default), "off", or a file path (rotated like log_file).
	AccessLog string `json:"access_log" env:"ACCESS_LOG"`
	// LogClientIPs writes client addresses into the access and error logs
	// instead of "-". Off by default.
	LogClientIPs bool `json:"log_client_ips" env:"LOG_CLIENT_IPS"`
	// TrustedProxyHeader names the header a reverse proxy in front of the
	// site puts the client's address in (X-Real-IP), used for rate limits
	// only. Empty (default) when clients connect directly.
	TrustedProxyHeader string `json:"trusted_proxy_header" env:"TRUSTED_PROXY_HEADER"`
	// ReportsPerHour limits reports per IPv4 address or IPv6 /64.
	ReportsPerHour int `json:"reports_per_hour" env:"REPORTS_PER_HOUR"`
	// ReportsPerDay limits reports from everyone together (bounds storage
	// under a distributed flood).
	ReportsPerDay int `json:"reports_per_day" env:"REPORTS_PER_DAY"`
	// ReportPoWBits is the proof-of-work difficulty of the report forms
	// (0 off, up to 24; each bit doubles the work).
	ReportPoWBits int `json:"report_pow_bits" env:"REPORT_POW_BITS"`
	// ContactCommunity names the community reporters are asked to share
	// with the team, so the team can DM them (shown on the forms and
	// filled in for them). Empty: reporters name one.
	ContactCommunity string `json:"contact_community" env:"CONTACT_COMMUNITY"`
	// DownloadURL and SourceURL add Download and Source code links to the
	// landing page once set (https only). Empty (default): no links.
	DownloadURL string `json:"download_url" env:"DOWNLOAD_URL"`
	SourceURL   string `json:"source_url" env:"SOURCE_URL"`
	// WebClientDir is the built web client (clients/web/build.py), served
	// at /app/ with a link on the landing page. Empty (default): no web
	// client. The site image sets it.
	WebClientDir string `json:"webclient_dir" env:"WEBCLIENT_DIR"`
	// SessionTTL is how long an admin sign-in lasts at most; SessionIdle
	// ends it sooner after no activity.
	SessionTTL  config.Duration `json:"session_ttl" env:"SESSION_TTL"`
	SessionIdle config.Duration `json:"session_idle" env:"SESSION_IDLE"`

	ops.Settings
	ops.TLSSettings
}

// DefaultConfig is the documented defaults.
func DefaultConfig() Config {
	tls := ops.DefaultTLSSettings()
	return Config{
		Listen:         ":7300",
		AccessLog:      "stderr",
		ReportsPerHour: 5,
		ReportsPerDay:  500,
		ReportPoWBits:  18,
		SessionTTL:     config.Duration{Duration: 12 * time.Hour},
		SessionIdle:    config.Duration{Duration: time.Hour},
		Settings:       ops.DefaultSettings(),
		TLSSettings:    tls,
	}
}

// KeyFile is storage_key_file or its default.
func (c *Config) KeyFile() string {
	if c.StorageKeyFile != "" {
		return c.StorageKeyFile
	}
	return filepath.Join(c.DataDir, "storage.key")
}

// ACMECache is acme_cache_dir or its default, DATA/acme.
func (c *Config) ACMECache() string {
	if c.ACMECacheDir != "" {
		return c.ACMECacheDir
	}
	return filepath.Join(c.DataDir, "acme")
}

// Validate rejects a config the site can't run with, naming the key.
func (c *Config) Validate() error {
	if err := c.Settings.Validate(); err != nil {
		return err
	}
	if err := c.TLSSettings.Validate(); err != nil {
		return err
	}
	if c.TLSMode == ops.TLSFiles && len(c.TLSDomains) == 0 {
		return errors.New("tls_mode files needs tls_domains: the names the site answers for (and redirects to)")
	}
	if c.DataDir == "" {
		return errors.New("data_dir is required")
	}
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return fmt.Errorf("listen %q must be host:port (for example :7300)", c.Listen)
	}
	if c.HTTPListen != "" {
		if _, _, err := net.SplitHostPort(c.HTTPListen); err != nil {
			return fmt.Errorf("http_listen %q must be host:port (for example :80)", c.HTTPListen)
		}
		if !c.Enabled() {
			return errors.New("http_listen redirects to https://, so it needs tls_mode acme or files")
		}
	}
	if c.AccessLog == "" {
		return errors.New(`access_log must be "stderr", "off" or a file path`)
	}
	if c.TrustedProxyHeader != "" && strings.Trim(c.TrustedProxyHeader, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-") != "" {
		return fmt.Errorf("trusted_proxy_header %q is not a header name (letters, digits and dashes)", c.TrustedProxyHeader)
	}
	switch {
	case c.ReportsPerHour < 1 || c.ReportsPerHour > 1000:
		return errors.New("reports_per_hour must be between 1 and 1000")
	case c.ReportsPerDay < 1 || c.ReportsPerDay > 1_000_000:
		return errors.New("reports_per_day must be between 1 and 1000000")
	case c.ReportPoWBits < 0 || c.ReportPoWBits > pow.MaxBits:
		return fmt.Errorf("report_pow_bits must be between 0 and %d", pow.MaxBits)
	case len(c.ContactCommunity) > maxCommunity:
		return fmt.Errorf("contact_community must be at most %d characters", maxCommunity)
	case c.SessionTTL.Duration < 5*time.Minute || c.SessionTTL.Duration > 30*24*time.Hour:
		return errors.New("session_ttl must be between 5m and 720h")
	case c.SessionIdle.Duration < time.Minute || c.SessionIdle.Duration > c.SessionTTL.Duration:
		return errors.New("session_idle must be at least 1m and at most session_ttl")
	}
	for key, v := range map[string]string{"download_url": c.DownloadURL, "source_url": c.SourceURL} {
		if v == "" {
			continue
		}
		u, err := url.Parse(v)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			return fmt.Errorf("%s %q must be an https:// URL", key, v)
		}
	}
	return nil
}
