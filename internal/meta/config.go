package meta

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/config"
	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/ops"
)

// Config holds the maintainer node's settings (docs/settings.md). Env
// overrides use the OPENARCANUM_META_ prefix.
type Config struct {
	Listen  string `json:"listen" env:"LISTEN"`
	DataDir string `json:"data_dir" env:"DATA_DIR"`

	// Other maintainer nodes to replicate listings, routes and the relay
	// registry from.
	Peers               []string        `json:"peers" env:"PEERS"`
	ReplicationInterval config.Duration `json:"replication_interval" env:"REPLICATION_INTERVAL"`

	// Maintainer cluster (docs/operations.md "Maintainer clusters"): nodes
	// this node trusts with private data (DM mailboxes, blob metadata,
	// followed moderation decisions, community tombstones), as URL#NODEKEY,
	// besides the cluster's maintainer set and `meta trust add`; and how
	// often it pulls each trusted node's log.
	TrustedPeers    []string        `json:"trusted_peers" env:"TRUSTED_PEERS"`
	TrustedInterval config.Duration `json:"trusted_interval" env:"TRUSTED_INTERVAL"`
	// The cluster's operator key (base64url): the node trusts the members
	// of the newest valid maintainer set signed by it, and serves that set
	// to clients. maintainer_set_file is a signed set (`meta operator
	// sign-set`) stored at start if it is newer than the one held.
	ClusterOperatorKey string `json:"cluster_operator_key" env:"CLUSTER_OPERATOR_KEY"`
	// Where node.key and node.noise are read from, if not data_dir (keys
	// made beforehand, such as a mounted Kubernetes Secret; never created
	// there).
	NodeKeyDir        string `json:"node_key_dir" env:"NODE_KEY_DIR"`
	MaintainerSetFile string `json:"maintainer_set_file" env:"MAINTAINER_SET_FILE"`
	// What one trusted peer may push: replicated mailbox and blob bytes per
	// rolling day, items and tombstones per minute. Above a limit the node
	// stops pulling from that peer until the window ends.
	PeerBytesPerDay         uint64 `json:"peer_bytes_per_day" env:"PEER_BYTES_PER_DAY"`
	PeerItemsPerMinute      int    `json:"peer_items_per_minute" env:"PEER_ITEMS_PER_MINUTE"`
	PeerTombstonesPerMinute int    `json:"peer_tombstones_per_minute" env:"PEER_TOMBSTONES_PER_MINUTE"`
	// Node keys (base64url) of the cluster members whose moderation
	// decisions (delistings, relay removals) this node applies too; its
	// own decisions always win.
	FollowDecisions []string `json:"follow_decisions" env:"FOLLOW_DECISIONS"`
	// DM blob content copied from cluster peers in the background, per
	// hour (blobs a client asks for are fetched at once regardless).
	PeerBlobBytesPerHour uint64 `json:"peer_blob_bytes_per_hour" env:"PEER_BLOB_BYTES_PER_HOUR"`

	// The public directory (listings, search, snapshots). Off by default:
	// the node lists no communities and joining relies on invites shared
	// another way. Only the config turns it on; `meta directory disable`
	// switches it off again on a running node (and `enable` lifts that).
	DirectoryEnabled bool `json:"directory_enabled" env:"DIRECTORY_ENABLED"`

	// Listing rate limit: accepted listing updates per community per hour.
	ListingsPerHour int `json:"listings_per_hour" env:"LISTINGS_PER_HOUR"`
	// Write requests per client IP per minute (all write endpoints).
	WritesPerMinute int `json:"writes_per_minute" env:"WRITES_PER_MINUTE"`
	// Delisted community ids (owner keys, base64url) and relay URLs.
	Delisted       []string `json:"delisted" env:"DELISTED"`
	DelistedRelays []string `json:"delisted_relays" env:"DELISTED_RELAYS"`

	// Relays without a heartbeat for this long drop out of the registry.
	RelayTTL config.Duration `json:"relay_ttl" env:"RELAY_TTL"`
	// Development setups only (scripts/dev-up.sh, kind, the tests): accept
	// relay URLs on loopback, private and link-local addresses and local
	// names, which otherwise are refused (community servers dial registry
	// relays, so such URLs would reach into their networks).
	AllowPrivateRelays bool `json:"allow_private_relays" env:"ALLOW_PRIVATE_RELAYS"`
	// Proof of control (docs/protocol.md §7.3): a relay URL is listed only
	// once a plain GET of it returned the heartbeat's key. Can be off only
	// with allow_private_relays (kind, where the node can't reach the relay
	// at the 127.0.0.1 URL clients use).
	RelayProof bool `json:"relay_proof" env:"RELAY_PROOF"`

	// Community status (docs/protocol.md §7.4): a community whose server
	// hasn't reported for community_offline_after shows as offline; one
	// silent for longer than community_reap_after is terminated, and the node
	// drops its route, listing and status, keeping a tombstone for
	// community_tombstone_ttl so clients learn it was shut down and an old
	// copy can't come back without an explicit re-registration.
	CommunityOfflineAfter config.Duration `json:"community_offline_after" env:"COMMUNITY_OFFLINE_AFTER"`
	CommunityReapAfter    config.Duration `json:"community_reap_after" env:"COMMUNITY_REAP_AFTER"`
	CommunityTombstoneTTL config.Duration `json:"community_tombstone_ttl" env:"COMMUNITY_TOMBSTONE_TTL"`

	// DM mailboxes (docs/protocol.md §7.1): item lifetime, bytes per inbox,
	// largest item, drops per inbox per minute, bytes all inboxes may hold
	// together, and bytes one network (IPv4 address or IPv6 /64) may drop
	// per rolling 24 hours.
	MailboxTTL         config.Duration `json:"mailbox_ttl" env:"MAILBOX_TTL"`
	MailboxQuota       uint64          `json:"mailbox_quota" env:"MAILBOX_QUOTA"`
	MailboxMaxItem     uint64          `json:"mailbox_max_item" env:"MAILBOX_MAX_ITEM"`
	MailboxDropsPerMin int             `json:"mailbox_drops_per_minute" env:"MAILBOX_DROPS_PER_MINUTE"`
	MailboxStoreMax    uint64          `json:"mailbox_store_max" env:"MAILBOX_STORE_MAX"`
	MailboxBytesPerIP  uint64          `json:"mailbox_bytes_per_ip" env:"MAILBOX_BYTES_PER_IP"`

	// Device-link nameplates (docs/protocol.md §10.1).
	NameplateTTL        config.Duration `json:"nameplate_ttl" env:"NAMEPLATE_TTL"`
	NameplatesPerMinute int             `json:"nameplates_per_minute" env:"NAMEPLATES_PER_MINUTE"`

	// DM blob store: lifetime from upload, largest blob, per-network
	// upload quotas per rolling 24 hours (bytes, uploads, fetch tokens),
	// total store size, and new uploads per network per minute
	// (docs/protocol.md §11).
	BlobTTL              config.Duration `json:"blob_ttl" env:"BLOB_TTL"`
	BlobMaxSize          uint64          `json:"blob_max_size" env:"BLOB_MAX_SIZE"`
	BlobQuotaPerIP       uint64          `json:"blob_quota_per_ip" env:"BLOB_QUOTA_PER_IP"`
	BlobUploadsPerIP     int             `json:"blob_uploads_per_ip" env:"BLOB_UPLOADS_PER_IP"`
	BlobTokensPerIP      uint64          `json:"blob_tokens_per_ip" env:"BLOB_TOKENS_PER_IP"`
	BlobStoreMax         uint64          `json:"blob_store_max" env:"BLOB_STORE_MAX"`
	BlobUploadsPerMinute int             `json:"blob_uploads_per_minute" env:"BLOB_UPLOADS_PER_MINUTE"`

	// Logging and the status listener (log_*, metrics_listen; internal/ops).
	ops.Settings
	// TLS on listen (tls_*, acme_*; docs/operations.md "TLS and
	// browsers"). acme_cache_dir defaults to data_dir/acme.
	ops.TLSSettings
	// Origins of web pages that may call the API from a browser (CORS):
	// "*" (any, the default) or origins such as https://app.example.invalid;
	// empty turns CORS off. Safe open: the API uses no cookies or other
	// ambient credentials, every request carries its own proof (cors.go).
	CORSOrigins []string `json:"cors_origins" env:"CORS_ORIGINS"`
}

// ACMECache is where tls_mode acme keeps its account key and certificates.
func (c *Config) ACMECache() string {
	if c.ACMECacheDir != "" {
		return c.ACMECacheDir
	}
	return filepath.Join(c.DataDir, "acme")
}

// MailboxBudgetWindow is the rolling window of mailbox_bytes_per_ip.
const MailboxBudgetWindow = 24 * time.Hour

// DefaultConfig returns the documented defaults.
func DefaultConfig() Config {
	return Config{
		Listen:                  ":7100",
		ReplicationInterval:     config.Duration{Duration: 30 * time.Second},
		TrustedInterval:         config.Duration{Duration: 5 * time.Second},
		PeerBytesPerDay:         2 << 30,
		PeerItemsPerMinute:      5000,
		PeerTombstonesPerMinute: 5000,
		PeerBlobBytesPerHour:    2 << 30,
		ListingsPerHour:         60,
		WritesPerMinute:         600,
		RelayTTL:                config.Duration{Duration: 2 * time.Minute},
		RelayProof:              true,
		CommunityOfflineAfter:   config.Duration{Duration: 3 * time.Minute},
		CommunityReapAfter:      config.Duration{Duration: 7 * 24 * time.Hour},
		CommunityTombstoneTTL:   config.Duration{Duration: 30 * 24 * time.Hour},
		MailboxTTL:              config.Duration{Duration: 14 * 24 * time.Hour},
		MailboxQuota:            100 << 20,
		MailboxMaxItem:          2 << 20,
		MailboxDropsPerMin:      120,
		MailboxStoreMax:         10 << 30,
		MailboxBytesPerIP:       1 << 30,
		NameplateTTL:            config.Duration{Duration: 5 * time.Minute},
		NameplatesPerMinute:     10,
		BlobTTL:                 config.Duration{Duration: 72 * time.Hour},
		BlobMaxSize:             26 << 20,
		BlobQuotaPerIP:          250 << 20,
		BlobUploadsPerIP:        500,
		BlobTokensPerIP:         10_000,
		BlobStoreMax:            10 << 30,
		BlobUploadsPerMinute:    30,
		Settings:                ops.DefaultSettings(),
		TLSSettings:             ops.DefaultTLSSettings(),
		CORSOrigins:             []string{"*"},
	}
}

// Validate rejects settings the node can't run with.
func (c *Config) Validate() error {
	switch {
	case c.Listen == "":
		return errors.New("listen address is required")
	case c.DataDir == "":
		return errors.New("data_dir is required")
	case c.ListingsPerHour < 1 || c.WritesPerMinute < 1 || c.MailboxDropsPerMin < 1 || c.NameplatesPerMinute < 1:
		return errors.New("rate limits must be positive")
	case c.RelayTTL.Duration < 10*time.Second:
		return errors.New("relay_ttl must be at least 10s")
	case !c.RelayProof && !c.AllowPrivateRelays:
		return errors.New("relay_proof can be off only together with allow_private_relays (development setups)")
	case c.CommunityOfflineAfter.Duration < 10*time.Second || c.CommunityOfflineAfter.Duration > 24*time.Hour:
		return errors.New("community_offline_after must be between 10s and 24h")
	case c.CommunityReapAfter.Duration < c.CommunityOfflineAfter.Duration || c.CommunityReapAfter.Duration > 365*24*time.Hour:
		return errors.New("community_reap_after must be at least community_offline_after and at most 365 days")
	case c.CommunityTombstoneTTL.Duration < time.Minute || c.CommunityTombstoneTTL.Duration > 365*24*time.Hour:
		return errors.New("community_tombstone_ttl must be between 1m and 365 days")
	case c.MailboxTTL.Duration < time.Minute:
		return errors.New("mailbox_ttl must be at least a minute")
	case c.MailboxQuota < 1<<20 || c.MailboxMaxItem < 1<<10 || c.MailboxMaxItem > c.MailboxQuota:
		return errors.New("mailbox_quota must be at least 1 MiB and mailbox_max_item between 1 KiB and the quota")
	case c.MailboxStoreMax < c.MailboxMaxItem || c.MailboxBytesPerIP < c.MailboxMaxItem:
		return errors.New("mailbox_store_max and mailbox_bytes_per_ip must each be at least mailbox_max_item")
	case c.NameplateTTL.Duration < 5*time.Second || c.NameplateTTL.Duration > time.Hour:
		return errors.New("nameplate_ttl must be between 5s and 1h")
	case c.ReplicationInterval.Duration < time.Second:
		return errors.New("replication_interval must be at least 1s")
	case c.TrustedInterval.Duration < 100*time.Millisecond || c.TrustedInterval.Duration > time.Hour:
		return errors.New("trusted_interval must be between 100ms and 1h")
	case c.PeerItemsPerMinute < 1 || c.PeerTombstonesPerMinute < 1:
		return errors.New("peer_items_per_minute and peer_tombstones_per_minute must be positive")
	case c.PeerBytesPerDay < c.MailboxMaxItem:
		return errors.New("peer_bytes_per_day must be at least mailbox_max_item")
	case c.BlobTTL.Duration < time.Minute || c.BlobTTL.Duration > 30*24*time.Hour:
		return errors.New("blob_ttl must be between 1m and 30 days")
	case c.BlobMaxSize < 1<<20 || c.BlobMaxSize > 1<<30:
		return errors.New("blob_max_size must be between 1 MiB and 1 GiB")
	case c.BlobQuotaPerIP < c.BlobMaxSize:
		return errors.New("blob_quota_per_ip must be at least blob_max_size")
	case c.BlobStoreMax < c.BlobMaxSize:
		return errors.New("blob_store_max must be at least blob_max_size")
	case c.BlobUploadsPerMinute < 1:
		return errors.New("blob_uploads_per_minute must be positive")
	case c.BlobUploadsPerIP < 1:
		return errors.New("blob_uploads_per_ip must be positive")
	case c.BlobTokensPerIP < MaxBlobTokens:
		return fmt.Errorf("blob_tokens_per_ip must be at least %d (one upload to the largest group)", MaxBlobTokens)
	}
	for _, p := range c.Peers {
		u, err := url.Parse(p)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("peer %q must be an http(s) URL", p)
		}
	}
	if err := c.TLSSettings.Validate(); err != nil {
		return err
	}
	if err := validCORSOrigins(c.CORSOrigins); err != nil {
		return err
	}
	if c.ClusterOperatorKey != "" {
		if k, err := identity.Decode(c.ClusterOperatorKey); err != nil || !identity.ValidPublic(k) {
			return errors.New("cluster_operator_key must be a base64url Ed25519 public key (`meta operator keygen` prints it)")
		}
	}
	if c.MaintainerSetFile != "" && c.ClusterOperatorKey == "" {
		return errors.New("maintainer_set_file needs cluster_operator_key")
	}
	for _, f := range c.FollowDecisions {
		if k, err := identity.Decode(f); err != nil || !identity.ValidPublic(k) {
			return fmt.Errorf("follow_decisions entry %q isn't a node key (base64url, as `meta id` prints it)", f)
		}
	}
	for _, p := range c.TrustedPeers {
		if _, _, err := ParseTrustedPeer(p); err != nil {
			return err
		}
	}
	return c.Settings.Validate()
}
