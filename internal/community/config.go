package community

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/config"
	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/meta/clusterset"
	"github.com/hyperkubeorg/openarcanum/internal/ops"
	"github.com/hyperkubeorg/openarcanum/internal/relayurl"
)

// Config holds the community server's node settings (docs/settings.md).
// Env overrides use the OPENARCANUM_COMMUNITY_ prefix.
type Config struct {
	DataDir string `json:"data_dir" env:"DATA_DIR"`
	// StorageKeyFile holds the key that encrypts content at rest. Keep it off
	// the data disk where possible; default: <data_dir>/storage.key.
	StorageKeyFile string `json:"storage_key_file" env:"STORAGE_KEY_FILE"`
	// ClaimFile holds the claim code where `community init -claim-file`
	// wrote it; the server deletes it once it's claimed.
	ClaimFile string `json:"claim_file" env:"CLAIM_FILE"`
	// ServerPublicKeyFile, when set, is where the server writes its public
	// key (hex) at start and after every key rotation, for a relay
	// dedicated to it to read (docs/operations.md "Dedicated relays").
	ServerPublicKeyFile string `json:"server_public_key_file" env:"SERVER_PUBLIC_KEY_FILE"`

	// Maintainer nodes: route and listing publication, relay registry.
	// An entry is a node's URL, or URL#OPERATORKEY for a node of a
	// maintainer cluster: the server then publishes to every member of the
	// cluster's verified member list (docs/protocol.md §7.5).
	// maintainer_operator_key applies to entries without a key.
	Maintainers           []string `json:"maintainers" env:"MAINTAINERS"`
	MaintainerOperatorKey string   `json:"maintainer_operator_key" env:"MAINTAINER_OPERATOR_KEY"`
	// Relays: "automatic" picks from the registry; "pinned" uses Relays only.
	RelayMode string   `json:"relay_mode" env:"RELAY_MODE"`
	Relays    []string `json:"relays" env:"RELAYS"`
	// How many relays to stay attached to in automatic mode.
	RelayCount int `json:"relay_count" env:"RELAY_COUNT"`

	BackupDir      string          `json:"backup_dir" env:"BACKUP_DIR"`
	BackupInterval config.Duration `json:"backup_interval" env:"BACKUP_INTERVAL"`
	BackupKeep     int             `json:"backup_keep" env:"BACKUP_KEEP"`

	PurgeInterval   config.Duration `json:"purge_interval" env:"PURGE_INTERVAL"`
	PublishInterval config.Duration `json:"publish_interval" env:"PUBLISH_INTERVAL"`
	// How often the server reports to maintainer nodes that it's up
	// (docs/protocol.md §7.4); keep it well under their
	// community_offline_after (3m by default).
	HeartbeatInterval config.Duration `json:"heartbeat_interval" env:"HEARTBEAT_INTERVAL"`

	// Link-preview fetcher (preview.go, docs/operations.md "Link previews").
	// The server, never a client, fetches pages that members link to, and
	// only when the community turned previews on.
	LinkPreviewsDisabled bool            `json:"link_previews_disabled" env:"LINK_PREVIEWS_DISABLED"`
	PreviewTimeout       config.Duration `json:"preview_timeout" env:"PREVIEW_TIMEOUT"`
	PreviewMaxBytes      int64           `json:"preview_max_bytes" env:"PREVIEW_MAX_BYTES"`
	PreviewImageMaxBytes int64           `json:"preview_image_max_bytes" env:"PREVIEW_IMAGE_MAX_BYTES"`
	PreviewSchemes       []string        `json:"preview_schemes" env:"PREVIEW_SCHEMES"`
	// PreviewAllowLoopback lets the fetcher reach loopback addresses. Go
	// tests set it to fetch from a local test server; it has no config key
	// or environment variable, so a deployed server can never turn it on.
	PreviewAllowLoopback bool `json:"-"`

	// Logging and the status listener (log_*, metrics_listen; internal/ops).
	// The community server has no other listener: metrics_listen is its
	// only port, and only when set.
	ops.Settings

	// File quotas (files.go, docs/operations.md "Quotas").
	// UploadQuotaPerMember caps the bytes one member may upload in any
	// rolling 24 hours; FileStoreMax caps the encrypted bytes the whole
	// community stores. Both must be at least MaxFileSizeLimit.
	UploadQuotaPerMember int64 `json:"upload_quota_per_member" env:"UPLOAD_QUOTA_PER_MEMBER"`
	FileStoreMax         int64 `json:"file_store_max" env:"FILE_STORE_MAX"`
}

// DefaultConfig returns the documented defaults for a data directory.
func DefaultConfig(dataDir string) Config {
	return Config{
		DataDir:         dataDir,
		RelayMode:       "pinned",
		RelayCount:      2,
		BackupInterval:  config.Duration{Duration: 24 * time.Hour},
		BackupKeep:      7,
		PurgeInterval:   config.Duration{Duration: time.Hour},
		PublishInterval: config.Duration{Duration: 10 * time.Minute},

		HeartbeatInterval: config.Duration{Duration: time.Minute},

		PreviewTimeout:       config.Duration{Duration: 5 * time.Second},
		PreviewMaxBytes:      1 << 20,
		PreviewImageMaxBytes: 256 << 10,
		PreviewSchemes:       []string{"https"},

		Settings:             ops.DefaultSettings(),
		UploadQuotaPerMember: 1 << 30,
		FileStoreMax:         20 << 30,
	}
}

// Resolve fills derived defaults (paths inside the data directory).
func (c *Config) Resolve() {
	if c.StorageKeyFile == "" {
		c.StorageKeyFile = filepath.Join(c.DataDir, "storage.key")
	}
	if c.BackupDir == "" {
		c.BackupDir = filepath.Join(c.DataDir, "backups")
	}
	// Relay URLs in canonical form (internal/relayurl); Validate reports
	// the ones that aren't relay URLs.
	for i, r := range c.Relays {
		if u, err := relayurl.Canonical(r); err == nil {
			c.Relays[i] = u
		}
	}
}

// Validate rejects settings the server can't run with.
func (c *Config) Validate() error {
	if c.DataDir == "" {
		return errors.New("data_dir is required")
	}
	switch c.RelayMode {
	case "pinned":
		if len(c.Relays) == 0 {
			return errors.New("relay_mode \"pinned\" needs at least one relay")
		}
	case "automatic":
		if len(c.Maintainers) == 0 {
			return errors.New("relay_mode \"automatic\" needs at least one maintainer node")
		}
		if c.RelayCount < 1 {
			return errors.New("relay_count must be at least 1")
		}
	default:
		return fmt.Errorf("relay_mode must be \"automatic\" or \"pinned\", not %q", c.RelayMode)
	}
	for _, r := range c.Relays {
		if err := ValidRelayURL(r); err != nil {
			return err
		}
	}
	for _, m := range c.Maintainers {
		base, _, err := clusterset.ParseEntry(m)
		if err != nil {
			return fmt.Errorf("maintainer %q must be an http(s) URL, or URL#OPERATORKEY", m)
		}
		if u, err := url.Parse(base); err != nil || u.Host == "" {
			return fmt.Errorf("maintainer %q must be an http(s) URL", m)
		}
	}
	if c.MaintainerOperatorKey != "" {
		if k, err := identity.Decode(c.MaintainerOperatorKey); err != nil || !identity.ValidPublic(k) {
			return errors.New("maintainer_operator_key must be a base64url Ed25519 public key")
		}
	}
	if c.BackupInterval.Duration < 0 || c.BackupKeep < 1 {
		return errors.New("backup_interval must be >= 0 and backup_keep >= 1")
	}
	if c.PurgeInterval.Duration <= 0 || c.PublishInterval.Duration <= 0 {
		return errors.New("purge_interval and publish_interval must be positive")
	}
	if c.HeartbeatInterval.Duration < time.Second || c.HeartbeatInterval.Duration > 10*time.Minute {
		return errors.New("heartbeat_interval must be between 1s and 10m")
	}
	if c.PreviewTimeout.Duration < 500*time.Millisecond || c.PreviewTimeout.Duration > 30*time.Second {
		return errors.New("preview_timeout must be between 500ms and 30s")
	}
	if c.PreviewMaxBytes < 4<<10 || c.PreviewMaxBytes > 8<<20 {
		return errors.New("preview_max_bytes must be between 4096 and 8388608")
	}
	if c.PreviewImageMaxBytes < 0 || c.PreviewImageMaxBytes > 1<<20 {
		return errors.New("preview_image_max_bytes must be between 0 (no images) and 1048576")
	}
	if len(c.PreviewSchemes) == 0 {
		return errors.New("preview_schemes needs at least one scheme (https, http)")
	}
	for _, sc := range c.PreviewSchemes {
		if sc != "https" && sc != "http" {
			return fmt.Errorf("preview_schemes: %q is not allowed (only https and http)", sc)
		}
	}
	if c.UploadQuotaPerMember < MaxFileSizeLimit || c.FileStoreMax < MaxFileSizeLimit {
		return fmt.Errorf("upload_quota_per_member and file_store_max must each be at least %d bytes (the largest file size)", MaxFileSizeLimit)
	}
	return c.Settings.Validate()
}

// ValidRelayURL accepts ws:// and wss:// relay URLs (internal/relayurl:
// no user information, query or fragment).
func ValidRelayURL(s string) error {
	if _, err := relayurl.Canonical(s); err != nil {
		return fmt.Errorf("relay %q must be a ws:// or wss:// URL: %w", s, err)
	}
	return nil
}
