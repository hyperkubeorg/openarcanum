package relay

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/config"
	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/meta/clusterset"
	"github.com/hyperkubeorg/openarcanum/internal/ops"
	"github.com/hyperkubeorg/openarcanum/internal/pow"
	"github.com/hyperkubeorg/openarcanum/internal/relayurl"
)

// ProtocolMaxMessage is the protocol ceiling for one published message (25 MiB).
const ProtocolMaxMessage = 25 << 20

// Config holds the relay's node settings (docs/settings.md). Env overrides
// use the OPENARCANUM_RELAY_ prefix.
type Config struct {
	Listen string `json:"listen" env:"LISTEN"`
	// Region is advertised in Hello and to the relay registry.
	Region string `json:"region" env:"REGION"`

	MaxChunk   uint32 `json:"max_chunk" env:"MAX_CHUNK"`
	MaxMessage uint64 `json:"max_message" env:"MAX_MESSAGE"`

	// Per-connection limits. Senders over their rate are slowed down (reads
	// pause); subscribers that can't keep up are disconnected.
	FramesPerSecond float64 `json:"frames_per_second" env:"FRAMES_PER_SECOND"`
	FrameBurst      float64 `json:"frame_burst" env:"FRAME_BURST"`
	BytesPerSecond  float64 `json:"bytes_per_second" env:"BYTES_PER_SECOND"`
	ByteBurst       float64 `json:"byte_burst" env:"BYTE_BURST"`
	SendQueueBytes  uint64  `json:"send_queue_bytes" env:"SEND_QUEUE_BYTES"`

	MaxSubscriptions int `json:"max_subscriptions" env:"MAX_SUBSCRIPTIONS"`
	MaxStreams       int `json:"max_streams" env:"MAX_STREAMS"`
	MaxPartial       int `json:"max_partial" env:"MAX_PARTIAL"`
	MaxConnections   int `json:"max_connections" env:"MAX_CONNECTIONS"`

	WriteTimeout config.Duration `json:"write_timeout" env:"WRITE_TIMEOUT"`
	PingInterval config.Duration `json:"ping_interval" env:"PING_INTERVAL"`

	// Relay registry (docs/protocol.md §7.3). The relay sends signed
	// heartbeats to each URL.
	// An entry is a maintainer node's URL, or URL#OPERATORKEY for a node of
	// a maintainer cluster: heartbeats then go to every member of the
	// cluster's verified member list (docs/protocol.md §7.5);
	// maintainer_operator_key applies to entries without a key.
	RegistryURLs          []string        `json:"registry_urls" env:"REGISTRY_URLS"`
	MaintainerOperatorKey string          `json:"maintainer_operator_key" env:"MAINTAINER_OPERATOR_KEY"`
	PublicURL             string          `json:"public_url" env:"PUBLIC_URL"`
	RegistryKeyFile       string          `json:"registry_key_file" env:"REGISTRY_KEY_FILE"`
	HeartbeatInterval     config.Duration `json:"heartbeat_interval" env:"HEARTBEAT_INTERVAL"`

	// Calls (docs/operations.md). Media uses one UDP port; behind NAT, list
	// the public addresses clients should send to.
	Calls          bool     `json:"calls" env:"CALLS"`
	MediaUDPPort   int      `json:"media_udp_port" env:"MEDIA_UDP_PORT"`
	MediaListenIP  string   `json:"media_listen_ip" env:"MEDIA_LISTEN_IP"`
	MediaPublicIPs []string `json:"media_public_ips" env:"MEDIA_PUBLIC_IPS"`
	MediaLoopback  bool     `json:"media_loopback" env:"MEDIA_LOOPBACK"`
	MaxCalls       int      `json:"max_calls" env:"MAX_CALLS"`
	// Participants in all calls, and from one address; what one participant
	// may send (bit/s); and, optionally, the only community server keys
	// (hex) whose calls this relay carries.
	MaxCallPeers      int      `json:"max_call_peers" env:"MAX_CALL_PEERS"`
	MaxCallPeersPerIP int      `json:"max_call_peers_per_ip" env:"MAX_CALL_PEERS_PER_IP"`
	CallPeerBitrate   int      `json:"call_peer_bitrate" env:"CALL_PEER_BITRATE"`
	CallServers       []string `json:"call_servers" env:"CALL_SERVERS"`

	// Logging and the status listener (log_*, metrics_listen; internal/ops).
	ops.Settings
	// TLS on listen (tls_*, acme_*; docs/operations.md "TLS and
	// browsers"). With it on, public_url defaults to wss://<first of
	// tls_domains>/ws. acme_cache_dir is required with acme: the relay
	// keeps no other state.
	ops.TLSSettings
	// Abuse controls (docs/operations.md "Abuse controls"; abuse.go).
	// Proof-of-work for new connections: "load" (only while under load),
	// "always" or "off"; its difficulty in leading zero bits; the open
	// connections and the new connections per second at which the relay
	// counts as under load (0 = three quarters of max_connections); and how
	// long a new connection has to send Start.
	PowMode            string          `json:"pow_mode" env:"POW_MODE"`
	PowBits            uint32          `json:"pow_bits" env:"POW_BITS"`
	PowLoadConnections int             `json:"pow_load_connections" env:"POW_LOAD_CONNECTIONS"`
	PowNewPerSecond    int             `json:"pow_new_per_second" env:"POW_NEW_PER_SECOND"`
	StartWindow        config.Duration `json:"start_window" env:"START_WINDOW"`
	// Connections that haven't sent Start yet hold a slot for free: at
	// most max_unstarted of them in all (0 = a tenth of max_connections)
	// and max_unstarted_per_ip per network (an IPv4 address or IPv6 /64).
	// Under load, or with half the unstarted slots taken, new connections
	// get start_window_under_load instead of start_window.
	MaxUnstarted         int             `json:"max_unstarted" env:"MAX_UNSTARTED"`
	MaxUnstartedPerIP    int             `json:"max_unstarted_per_ip" env:"MAX_UNSTARTED_PER_IP"`
	StartWindowUnderLoad config.Duration `json:"start_window_under_load" env:"START_WINDOW_UNDER_LOAD"`
	// Per network (an IPv4 address or IPv6 /64): connections, started or
	// not (more are refused with 429), and subscriptions across its
	// connections; and subscriptions in all. Each subscription costs
	// memory, so these bound what connections that start can hold.
	MaxConnectionsPerIP   int `json:"max_connections_per_ip" env:"MAX_CONNECTIONS_PER_IP"`
	MaxSubscriptionsPerIP int `json:"max_subscriptions_per_ip" env:"MAX_SUBSCRIPTIONS_PER_IP"`
	MaxSubscriptionsTotal int `json:"max_subscriptions_total" env:"MAX_SUBSCRIPTIONS_TOTAL"`
	// In pow_mode "load", a new connection from a network that already
	// holds this many open connections must solve the proof-of-work even
	// when the relay isn't under load.
	PowIPConnections int `json:"pow_ip_connections" env:"POW_IP_CONNECTIONS"`
	// Publishing to a topic with more subscribers than this needs a fan-out
	// token from the server the topic commits to (0 = never).
	FanoutThreshold int `json:"fanout_threshold" env:"FANOUT_THRESHOLD"`
	// Optional chunk cache (off when chunk_cache_bytes is 0): memory only,
	// end-to-end encrypted messages their publisher marked cacheable, kept
	// for chunk_cache_ttl, at most chunk_cache_topic_bytes per topic and
	// chunk_cache_max_message per message.
	ChunkCacheBytes      uint64          `json:"chunk_cache_bytes" env:"CHUNK_CACHE_BYTES"`
	ChunkCacheTTL        config.Duration `json:"chunk_cache_ttl" env:"CHUNK_CACHE_TTL"`
	ChunkCacheTopicBytes uint64          `json:"chunk_cache_topic_bytes" env:"CHUNK_CACHE_TOPIC_BYTES"`
	ChunkCacheMaxMessage uint64          `json:"chunk_cache_max_message" env:"CHUNK_CACHE_MAX_MESSAGE"`
	// What one publishing connection's messages may take of the cache
	// (0 = a quarter of chunk_cache_bytes). Sizes count each message's
	// bookkeeping overhead, not only its payload.
	ChunkCacheConnBytes uint64 `json:"chunk_cache_conn_bytes" env:"CHUNK_CACHE_CONN_BYTES"`

	// Endpoint hosts (a community server carries every member's session on
	// its one connection): the per-connection budget plus an allowance per
	// open inbound stream, capped at the host_* totals.
	HostFramesPerStream float64 `json:"host_frames_per_stream" env:"HOST_FRAMES_PER_STREAM"`
	HostBytesPerStream  float64 `json:"host_bytes_per_stream" env:"HOST_BYTES_PER_STREAM"`
	HostFramesPerSecond float64 `json:"host_frames_per_second" env:"HOST_FRAMES_PER_SECOND"`
	HostFrameBurst      float64 `json:"host_frame_burst" env:"HOST_FRAME_BURST"`
	HostBytesPerSecond  float64 `json:"host_bytes_per_second" env:"HOST_BYTES_PER_SECOND"`
	HostByteBurst       float64 `json:"host_byte_burst" env:"HOST_BYTE_BURST"`

	// A relay dedicated to one community server (docs/operations.md
	// "Dedicated relays"; dedicated.go): that server's key (64 hex
	// characters), or a file holding it (re-read every 10 s; until it
	// exists the relay serves nothing). It then carries only that server's
	// streams, topics and calls, and takes network blocks from it, kept in
	// block_file (required) up to max_blocked_networks networks.
	DedicatedServerKey     string `json:"dedicated_server_key" env:"DEDICATED_SERVER_KEY"`
	DedicatedServerKeyFile string `json:"dedicated_server_key_file" env:"DEDICATED_SERVER_KEY_FILE"`
	BlockFile              string `json:"block_file" env:"BLOCK_FILE"`
	MaxBlockedNetworks     int    `json:"max_blocked_networks" env:"MAX_BLOCKED_NETWORKS"`
}

// DefaultConfig returns the documented defaults.
func DefaultConfig() Config {
	return Config{
		Listen:            ":7000",
		MaxChunk:          1 << 20,
		MaxMessage:        ProtocolMaxMessage,
		FramesPerSecond:   1000,
		FrameBurst:        2000,
		BytesPerSecond:    16 << 20,
		ByteBurst:         32 << 20,
		SendQueueBytes:    64 << 20,
		MaxSubscriptions:  4096,
		MaxStreams:        1024,
		MaxPartial:        64,
		MaxConnections:    10000,
		WriteTimeout:      config.Duration{Duration: 15 * time.Second},
		PingInterval:      config.Duration{Duration: 20 * time.Second},
		HeartbeatInterval: config.Duration{Duration: 30 * time.Second},
		Calls:             true,
		MediaUDPPort:      7000,
		MaxCalls:          1000,
		MaxCallPeers:      5000,
		MaxCallPeersPerIP: 20,
		CallPeerBitrate:   8_000_000,
		Settings:          ops.DefaultSettings(),
		TLSSettings:       ops.DefaultTLSSettings(),

		PowMode:               "load",
		PowBits:               18,
		PowNewPerSecond:       200,
		StartWindow:           config.Duration{Duration: 30 * time.Second},
		MaxUnstartedPerIP:     128,
		MaxConnectionsPerIP:   256,
		MaxSubscriptionsPerIP: 65536,
		MaxSubscriptionsTotal: 2_000_000,
		PowIPConnections:      32,
		StartWindowUnderLoad:  config.Duration{Duration: 10 * time.Second},
		FanoutThreshold:       100,
		ChunkCacheTTL:         config.Duration{Duration: time.Minute},
		ChunkCacheTopicBytes:  4 << 20,
		ChunkCacheMaxMessage:  256 << 10,

		HostFramesPerStream: 50,
		HostBytesPerStream:  256 << 10,
		HostFramesPerSecond: 50_000,
		HostFrameBurst:      100_000,
		HostBytesPerSecond:  128 << 20,
		HostByteBurst:       256 << 20,

		MaxBlockedNetworks: 10000,
	}
}

// Validate rejects settings the relay can't run with.
func (c *Config) Validate() error {
	switch {
	case c.Listen == "":
		return errors.New("listen address is required")
	case c.MaxChunk < 1024:
		return errors.New("max_chunk must be at least 1024")
	case c.MaxMessage < uint64(c.MaxChunk):
		return errors.New("max_message must be at least max_chunk")
	case c.MaxMessage > ProtocolMaxMessage:
		return errors.New("max_message may not exceed 25 MiB")
	case c.FramesPerSecond <= 0 || c.FrameBurst < 1:
		return errors.New("frames_per_second and frame_burst must be positive")
	case c.BytesPerSecond <= 0 || c.ByteBurst < float64(c.MaxChunk):
		return errors.New("bytes_per_second must be positive and byte_burst at least max_chunk")
	case c.SendQueueBytes < c.MaxMessage:
		return errors.New("send_queue_bytes must be at least max_message")
	case c.MaxSubscriptions < 1 || c.MaxStreams < 1 || c.MaxPartial < 1 || c.MaxConnections < 1:
		return errors.New("connection limits must be positive")
	case c.WriteTimeout.Duration <= 0 || c.PingInterval.Duration <= 0:
		return errors.New("write_timeout and ping_interval must be positive")
	case c.MediaListenIP != "" && net.ParseIP(c.MediaListenIP) == nil:
		return errors.New("media_listen_ip must be an IP address")
	case c.Calls && (c.MediaUDPPort < 0 || c.MediaUDPPort > 65535 || c.MaxCalls < 1):
		return errors.New("media_udp_port must be a port and max_calls positive")
	case c.Calls && (c.MaxCallPeers < 1 || c.MaxCallPeersPerIP < 1 || c.CallPeerBitrate < 64_000):
		return errors.New("max_call_peers and max_call_peers_per_ip must be positive and call_peer_bitrate at least 64000")
	case !validServerKeys(c.CallServers):
		return errors.New("call_servers must be 64-character hex server keys")
	case len(c.RegistryURLs) > 0 && c.AnnouncedURL() == "":
		return errors.New("public_url is required when registry_urls is set (or turn on TLS with tls_domains: it is then wss://<first domain>/ws)")
	}
	if err := c.TLSSettings.Validate(); err != nil {
		return err
	}
	switch {
	case c.TLSMode == ops.TLSACME && c.ACMECacheDir == "":
		return errors.New("tls_mode acme needs acme_cache_dir: a directory kept across restarts for the certificates (the relay has no data directory)")
	case c.Enabled() && c.PublicURL != "" && !strings.HasPrefix(strings.ToLower(c.PublicURL), "wss://"):
		return errors.New("public_url must be wss:// when the relay serves TLS (tls_mode)")
	}
	for _, r := range c.RegistryURLs {
		if _, _, err := clusterset.ParseEntry(r); err != nil {
			return fmt.Errorf("registry_urls entry %q must be an http(s) URL, or URL#OPERATORKEY", r)
		}
	}
	if c.MaintainerOperatorKey != "" {
		if k, err := identity.Decode(c.MaintainerOperatorKey); err != nil || !identity.ValidPublic(k) {
			return errors.New("maintainer_operator_key must be a base64url Ed25519 public key")
		}
	}
	if c.PublicURL != "" {
		if _, err := relayurl.Canonical(c.PublicURL); err != nil {
			return fmt.Errorf("public_url: %w", err)
		}
	}
	if err := c.validateAbuse(); err != nil {
		return err
	}
	if err := c.validateDedicated(); err != nil {
		return err
	}
	return c.Settings.Validate()
}

// AnnouncedURL is the URL the relay registers and clients use: public_url,
// or, when the relay serves TLS and has tls_domains, wss://<first>/ws
// (port 443: the port ACME's tls-alpn-01 challenge reaches). Empty when
// neither is set.
func (c *Config) AnnouncedURL() string {
	if c.PublicURL != "" || !c.Enabled() || c.PublicHost() == "" {
		return c.PublicURL
	}
	return "wss://" + c.PublicHost() + "/ws"
}

// Dedicated reports whether the relay is dedicated to one community server.
func (c *Config) Dedicated() bool {
	return c.DedicatedServerKey != "" || c.DedicatedServerKeyFile != ""
}

func (c *Config) validateDedicated() error {
	switch {
	case c.DedicatedServerKey != "" && c.DedicatedServerKeyFile != "":
		return errors.New("set dedicated_server_key or dedicated_server_key_file, not both")
	case c.DedicatedServerKey != "" && !validServerKeys([]string{c.DedicatedServerKey}):
		return errors.New("dedicated_server_key must be a 64-character hex server key")
	case c.MaxBlockedNetworks < 1:
		return errors.New("max_blocked_networks must be positive")
	case !c.Dedicated():
		if c.BlockFile != "" {
			return errors.New("block_file is only used by a dedicated relay (dedicated_server_key or dedicated_server_key_file)")
		}
		return nil
	case c.BlockFile == "":
		return errors.New("a dedicated relay needs block_file, where it keeps its network blocks")
	case len(c.RegistryURLs) > 0:
		return errors.New("a dedicated relay doesn't register with maintainer nodes (other communities and DMs couldn't use it): remove registry_urls")
	}
	return nil
}

func (c *Config) validateAbuse() error {
	switch {
	case c.PowMode != "load" && c.PowMode != "always" && c.PowMode != "off":
		return errors.New(`pow_mode must be "load", "always" or "off"`)
	case c.PowMode != "off" && (c.PowBits < 1 || c.PowBits > pow.MaxBits):
		return fmt.Errorf("pow_bits must be between 1 and %d", pow.MaxBits)
	case c.PowLoadConnections < 0 || c.PowLoadConnections > c.MaxConnections:
		return errors.New("pow_load_connections must be between 0 (three quarters of max_connections) and max_connections")
	case c.PowNewPerSecond < 1:
		return errors.New("pow_new_per_second must be positive")
	case c.StartWindow.Duration < time.Second || c.StartWindow.Duration > 5*time.Minute:
		return errors.New("start_window must be between 1s and 5m")
	case c.StartWindowUnderLoad.Duration < time.Second || c.StartWindowUnderLoad.Duration > 5*time.Minute:
		return errors.New("start_window_under_load must be between 1s and 5m (the shorter of it and start_window applies under load)")
	case c.MaxUnstarted < 0 || c.MaxUnstarted > c.MaxConnections:
		return errors.New("max_unstarted must be between 0 (a tenth of max_connections) and max_connections")
	case c.MaxUnstartedPerIP < 1:
		return errors.New("max_unstarted_per_ip must be positive")
	case c.MaxConnectionsPerIP < 1:
		return errors.New("max_connections_per_ip must be positive")
	case c.MaxSubscriptionsPerIP < 1 || c.MaxSubscriptionsTotal < 1:
		return errors.New("max_subscriptions_per_ip and max_subscriptions_total must be positive")
	case c.PowIPConnections < 1:
		return errors.New("pow_ip_connections must be positive")
	case c.FanoutThreshold < 0:
		return errors.New("fanout_threshold must be 0 (off) or positive")
	case c.ChunkCacheBytes > 0 && (c.ChunkCacheTTL.Duration < time.Second || c.ChunkCacheTTL.Duration > time.Hour):
		return errors.New("chunk_cache_ttl must be between 1s and 1h")
	case c.ChunkCacheBytes > 0 && (c.ChunkCacheTopicBytes < 1<<10 || c.ChunkCacheTopicBytes > c.ChunkCacheBytes):
		return errors.New("chunk_cache_topic_bytes must be between 1 KiB and chunk_cache_bytes")
	case c.HostFramesPerStream < 0 || c.HostBytesPerStream < 0:
		return errors.New("host_frames_per_stream and host_bytes_per_stream must not be negative")
	case c.HostFramesPerSecond < 0 || c.HostFrameBurst < 0 || c.HostBytesPerSecond < 0 || c.HostByteBurst < 0:
		return errors.New("host_frames_per_second, host_frame_burst, host_bytes_per_second and host_byte_burst must not be negative")
	case c.ChunkCacheBytes > 0 && (c.ChunkCacheMaxMessage < 1<<10 || c.ChunkCacheMaxMessage > c.ChunkCacheTopicBytes):
		return errors.New("chunk_cache_max_message must be between 1 KiB and chunk_cache_topic_bytes")
	case c.ChunkCacheBytes > 0 && c.ChunkCacheConnBytes > c.ChunkCacheBytes:
		return errors.New("chunk_cache_conn_bytes must be at most chunk_cache_bytes (0: a quarter of it)")
	}
	return nil
}

// chunkCacheConnBytes is one publishing connection's share of the cache.
func (c *Config) chunkCacheConnBytes() uint64 {
	if c.ChunkCacheConnBytes > 0 {
		return c.ChunkCacheConnBytes
	}
	return max(c.ChunkCacheBytes/4, c.ChunkCacheTopicBytes)
}

// maxUnstarted is how many connections may be unstarted at once.
func (c *Config) maxUnstarted() int {
	if c.MaxUnstarted > 0 {
		return c.MaxUnstarted
	}
	return max(1, c.MaxConnections/10)
}

// powLoadConnections is the open-connection count that means "under load".
func (c *Config) powLoadConnections() int {
	if c.PowLoadConnections > 0 {
		return c.PowLoadConnections
	}
	return max(1, c.MaxConnections*3/4)
}

func validServerKeys(keys []string) bool {
	for _, k := range keys {
		if b, err := hex.DecodeString(k); err != nil || len(b) != 32 {
			return false
		}
	}
	return true
}

// CallServerKeys is call_servers decoded (validated by Validate).
func (c *Config) CallServerKeys() [][]byte {
	var out [][]byte
	for _, k := range c.CallServers {
		if b, err := hex.DecodeString(k); err == nil {
			out = append(out, b)
		}
	}
	return out
}
