package community

import (
	"context"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
)

// A host runs several communities in one process (docs/operations.md
// "Several communities on one server"). Its data directory holds one
// directory per community, communities/<id>/, each what a single server's
// data directory is: keys, database, backups. They share the host's
// settings (relays, maintainer nodes, limits) and its storage key: each
// community's own key is derived from it, unless a key file for that
// community sits next to the host's (communities/<id>.key, for a community
// restored from a backup). An owner of any of them can start another from
// the app (AdminAction create_community), up to max_communities.

const hostedDir = "communities"

// Host runs the communities in Config.DataDir.
type Host struct {
	cfg Config
	log *slog.Logger
	key []byte // the host's storage key
	// first is the community init made: a relay dedicated to this server
	// (server_public_key_file) serves it, and /metrics shows it.
	first string

	mu      sync.Mutex
	servers map[string]*Server
	ctx     context.Context // set by Run: communities created later start under it
	wg      sync.WaitGroup
}

// HostInitResult is what InitHost made: the host and its first community.
type HostInitResult struct {
	Community string
	*InitResult
}

// InitHost creates a host in cfg.DataDir with one community, whose claim
// code it returns (and writes to cfg.ClaimFile, when set).
func InitHost(ctx context.Context, cfg Config) (*HostInitResult, error) {
	cfg.Resolve()
	if err := os.MkdirAll(filepath.Join(cfg.DataDir, hostedDir), 0o700); err != nil {
		return nil, err
	}
	if ids, _ := hostedIDs(cfg.DataDir); len(ids) > 0 {
		return nil, fmt.Errorf("%s is already initialized (it has communities: start more from the app)", cfg.DataDir)
	}
	if _, err := os.Stat(cfg.StorageKeyFile); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(cfg.StorageKeyFile), 0o700); err != nil {
			return nil, err
		}
		if err := writeHexFile(cfg.StorageKeyFile, randomBytes(32)); err != nil {
			return nil, err
		}
	}
	h := &Host{cfg: cfg}
	if err := h.loadKey(); err != nil {
		return nil, err
	}
	id := NewCommunityID()
	h.first = id
	res, err := Init(ctx, h.communityConfig(id))
	if err != nil {
		return nil, err
	}
	if cfg.ClaimFile != "" {
		if err := os.WriteFile(cfg.ClaimFile, []byte(res.ClaimCode+"\n"), 0o600); err != nil {
			return nil, err
		}
	}
	return &HostInitResult{Community: id, InitResult: res}, nil
}

// OpenHost opens every community in cfg.DataDir.
func OpenHost(ctx context.Context, cfg Config, log *slog.Logger) (*Host, error) {
	cfg.Resolve()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	h := &Host{cfg: cfg, log: log, servers: map[string]*Server{}}
	if err := h.loadKey(); err != nil {
		return nil, err
	}
	ids, err := hostedIDs(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("%s has no communities: run community init first", cfg.DataDir)
	}
	h.first = firstCreated(cfg.DataDir, ids)
	for _, id := range ids {
		s, err := h.open(ctx, id)
		if err != nil {
			_ = h.Close()
			return nil, fmt.Errorf("community %s: %w", id, err)
		}
		h.servers[id] = s
	}
	// The claim file `init -claim-file` wrote belongs to the community not
	// claimed yet: it deletes the file once it's claimed.
	if cfg.ClaimFile != "" {
		if _, err := os.Stat(cfg.ClaimFile); err == nil {
			for _, id := range ids {
				if s := h.servers[id]; !s.Claimed() {
					s.cfg.ClaimFile = cfg.ClaimFile
					break
				}
			}
		}
	}
	return h, nil
}

// Run runs every community until ctx ends. One that stops with an error
// is logged; the others go on.
func (h *Host) Run(ctx context.Context) error {
	h.mu.Lock()
	h.ctx = ctx
	for id, s := range h.servers {
		h.start(id, s)
	}
	h.mu.Unlock()
	<-ctx.Done()
	h.wg.Wait()
	return nil
}

// start runs one community (h.mu held).
func (h *Host) start(id string, s *Server) {
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		if err := s.Run(h.ctx); err != nil && h.ctx.Err() == nil {
			h.log.Error("community stopped", "community", id, "err", err)
		}
	}()
}

// Create starts a new community on this host and returns its claim code
// (the owner who asked claims it at once).
func (h *Host) Create(ctx context.Context) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.servers) >= h.cfg.MaxCommunities {
		return "", fmt.Errorf("this server already runs %d communities, its limit (max_communities)", len(h.servers))
	}
	if h.ctx == nil {
		return "", errors.New("the server is starting; try again in a moment")
	}
	id := NewCommunityID()
	res, err := Init(ctx, h.communityConfig(id))
	if err != nil {
		return "", err
	}
	s, err := h.open(h.ctx, id)
	if err != nil {
		return "", err
	}
	h.servers[id] = s
	h.start(id, s)
	h.log.Info("community created", "community", id)
	return res.ClaimCode, nil
}

// First is the community init made (the oldest here).
func (h *Host) First() *Server { return h.Servers()[h.first] }

// Servers are the communities this host runs, by id.
func (h *Host) Servers() map[string]*Server {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]*Server, len(h.servers))
	for id, s := range h.servers {
		out[id] = s
	}
	return out
}

// Live fails when any community isn't live; Ready when any isn't ready.
func (h *Host) Live(ctx context.Context) error {
	for id, s := range h.Servers() {
		if err := s.Live(ctx); err != nil {
			return fmt.Errorf("community %s: %w", id, err)
		}
	}
	return nil
}

func (h *Host) Ready(ctx context.Context) error {
	for id, s := range h.Servers() {
		if err := s.Ready(ctx); err != nil {
			return fmt.Errorf("community %s: %w", id, err)
		}
	}
	return nil
}

// Close closes every community's database.
func (h *Host) Close() error {
	var errs []error
	for _, s := range h.Servers() {
		errs = append(errs, s.Close())
	}
	return errors.Join(errs...)
}

// CommunityConfig is the configuration a community on this host runs with
// (for backup and restore of one of them).
func CommunityConfig(host Config, id string) (Config, error) {
	host.Resolve()
	h := &Host{cfg: host}
	if err := h.loadKey(); err != nil {
		return Config{}, err
	}
	return h.communityConfig(id), nil
}

// RestoredKeyFile is where a community restored onto a host keeps its own
// storage key (the one its backup was made with).
func RestoredKeyFile(host Config, id string) string {
	host.Resolve()
	return filepath.Join(filepath.Dir(host.StorageKeyFile), hostedDir, id+".key")
}

func (h *Host) open(ctx context.Context, id string) (*Server, error) {
	s, err := Open(ctx, h.communityConfig(id), h.log.With("community", id))
	if err != nil {
		return nil, err
	}
	s.host = h
	return s, nil
}

func (h *Host) loadKey() error {
	key, err := readHexFile(h.cfg.StorageKeyFile, chacha20poly1305.KeySize)
	if err != nil {
		return fmt.Errorf("storage key: %w", err)
	}
	h.key = key
	return nil
}

// communityConfig: the host's settings, the community's own directory and
// storage key, and no claim file or status listener of its own.
func (h *Host) communityConfig(id string) Config {
	c := h.cfg
	c.DataDir = filepath.Join(h.cfg.DataDir, hostedDir, id)
	c.BackupDir = ""
	c.ClaimFile = ""
	c.MetricsListen = ""
	if id != h.first {
		c.ServerPublicKeyFile = ""
	}
	c.Relays = append([]string(nil), h.cfg.Relays...)
	c.StorageKeyFile = RestoredKeyFile(h.cfg, id)
	if key, err := readHexFile(c.StorageKeyFile, chacha20poly1305.KeySize); err == nil {
		c.StorageKey = key
	} else {
		c.StorageKey = deriveCommunityKey(h.key, id)
	}
	c.Resolve()
	return c
}

// deriveCommunityKey: HKDF-SHA256 of the host's storage key, per community.
func deriveCommunityKey(hostKey []byte, id string) []byte {
	key, err := hkdf.Key(sha256.New, hostKey, nil, "openarcanum/hosted-community/v1\x00"+id, chacha20poly1305.KeySize)
	if err != nil {
		panic(err) // only for impossible lengths
	}
	return key
}

// NewCommunityID is a new community's directory name on a host.
func NewCommunityID() string { return hex.EncodeToString(randomBytes(8)) }

// WriteStorageKey writes a new random storage key to path.
func WriteStorageKey(path string) error { return writeHexFile(path, randomBytes(32)) }

// firstCreated is the community whose server key is oldest.
func firstCreated(dataDir string, ids []string) string {
	first, at := "", time.Time{}
	for _, id := range ids {
		st, err := os.Stat(filepath.Join(dataDir, hostedDir, id, "server.key"))
		if err == nil && (first == "" || st.ModTime().Before(at)) {
			first, at = id, st.ModTime()
		}
	}
	return first
}

// hostedIDs lists the communities in a host's data directory.
func hostedIDs(dataDir string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(dataDir, hostedDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			if _, err := os.Stat(filepath.Join(dataDir, hostedDir, e.Name(), "server.key")); err == nil {
				ids = append(ids, e.Name())
			}
		}
	}
	sort.Strings(ids)
	return ids, nil
}
