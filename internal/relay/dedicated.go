package relay

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/hyperkubeorg/openarcanum/internal/callauth"
	"github.com/hyperkubeorg/openarcanum/internal/fanout"
	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/relaymod"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Dedicated relays (docs/protocol.md §2 "Dedicated relays";
// docs/operations.md "Dedicated relays"). A relay dedicated to one community
// server carries only that server's streams, its committed topics and its
// calls, and takes signed moderation commands from it: block the networks
// behind some of its inbound streams, or lift a block. The server never
// learns an address; it names streams, and gets back an opaque block id.
//
// Blocks are kept in block_file so they survive restarts. It is the only
// thing a relay writes to disk, and it holds network addresses (an IPv4
// address or an IPv6 /64), which is why the file is private to the relay's
// user. Addresses are never logged.

// dedicatedKeyPoll is how often dedicated_server_key_file is re-read.
var dedicatedKeyPoll = 10 * time.Second

// maxSeenCommands bounds the command ids one connection remembers (until
// they expire).
const maxSeenCommands = 1024

// dedicatedKey is the server key this relay is dedicated to, or nil (not
// dedicated, or its key file isn't there yet).
func (s *Server) dedicatedKey() []byte {
	if p := s.dedicated.Load(); p != nil {
		return *p
	}
	return nil
}

// DedicatedKnown reports whether a dedicated relay knows its server's key
// (its key file may not exist yet).
func (s *Server) DedicatedKnown() bool { return s.dedicatedKey() != nil }

// SetDedicatedKey replaces the server key a dedicated relay serves (its key
// file changed; tests). The connection that hosted the old key's endpoint
// is closed: its streams and topics belong to a key this relay no longer
// serves.
func (s *Server) SetDedicatedKey(key []byte) {
	old := s.dedicatedKey()
	if bytes.Equal(old, key) {
		return
	}
	k := append([]byte(nil), key...)
	s.dedicated.Store(&k)
	if old == nil {
		s.log.Info("dedicated relay serving its community server", "server", identity.Fingerprint(k))
		return
	}
	s.log.Info("dedicated relay's community server key changed", "server", identity.Fingerprint(k))
	s.mu.RLock()
	ep := s.endpoints[string(old)]
	s.mu.RUnlock()
	if ep != nil {
		ep.close(websocket.StatusPolicyViolation, "dedicated server key changed", nil)
	}
}

// readKeyFile reads a hex Ed25519 public key (surrounding whitespace allowed).
func readKeyFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	k, err := hex.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || !identity.ValidPublic(k) {
		return nil, fmt.Errorf("%s doesn't hold a 64-character hex server key", path)
	}
	return k, nil
}

// loadDedicatedKeyFile reads dedicated_server_key_file once; a missing file
// leaves the relay waiting (serving nothing), and a bad one is logged and
// ignored.
func (s *Server) loadDedicatedKeyFile() {
	k, err := readKeyFile(s.cfg.DedicatedServerKeyFile)
	switch {
	case err == nil:
		s.SetDedicatedKey(k)
	case errors.Is(err, os.ErrNotExist):
	default:
		s.log.Warn("dedicated server key file unreadable", "err", err)
	}
}

// WatchDedicatedKey re-reads dedicated_server_key_file until ctx ends, so a
// relay started before its community server (which writes the file) picks
// the key up, and a key rotation reaches it.
func (s *Server) WatchDedicatedKey(ctx context.Context) {
	if s.cfg.DedicatedServerKeyFile == "" {
		return
	}
	t := time.NewTicker(dedicatedKeyPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.loadDedicatedKeyFile()
		}
	}
}

// Err reports a relay key file (registry_key_file) or a dedicated relay's
// block list that couldn't be read at start. Such a relay refuses every
// connection rather than run without its identity or its blocks; the relay
// command exits with it.
func (s *Server) Err() error {
	if s.blocksErr != nil {
		return fmt.Errorf("block_file: %w", s.blocksErr)
	}
	return s.keyErr
}

// topicAllowed: a dedicated relay carries only topics that commit to its
// server's key (channel topics, §2 "Large fan-outs").
func (s *Server) topicAllowed(topic []byte) bool {
	if !s.cfg.Dedicated() {
		return true
	}
	k := s.dedicatedKey()
	return k != nil && fanout.Commits(topic, k)
}

// endpointAllowed: a dedicated relay hosts only its server's endpoint.
func (s *Server) endpointAllowed(key []byte) bool {
	if !s.cfg.Dedicated() {
		return true
	}
	k := s.dedicatedKey()
	return k != nil && bytes.Equal(k, key)
}

// callAllowed: a dedicated relay carries only its server's calls.
func (s *Server) callAllowed(g *wire.SignedCallGrant) bool {
	if !s.cfg.Dedicated() {
		return true
	}
	v, err := callauth.VerifyGrant(g, time.Now())
	return err == nil && s.endpointAllowed(v.ServerKey)
}

const notDedicatedMsg = "this relay is dedicated to another community server"

// ---------------------------------------------------------------- commands

func (c *conn) moderation(m *wire.RelayModeration) {
	s := c.srv
	if !s.cfg.Dedicated() {
		c.sendError(m.Req, wire.ErrorCode_ERROR_CODE_NOT_DEDICATED, "this relay isn't dedicated to a community server, so it takes no moderation commands")
		return
	}
	key := s.dedicatedKey()
	s.mu.RLock()
	host := key != nil && c.endpointKey == string(key) && s.endpoints[string(key)] == c
	s.mu.RUnlock()
	if !host {
		c.sendError(m.Req, wire.ErrorCode_ERROR_CODE_NOT_DEDICATED, "moderation commands come only from the connection hosting this relay's community server")
		return
	}
	now := time.Now()
	cmd, err := relaymod.Verify(m.Command, key, c.local(), now)
	if err != nil {
		code := wire.ErrorCode_ERROR_CODE_BAD_COMMAND
		if errors.Is(err, relaymod.ErrWrongServer) {
			code = wire.ErrorCode_ERROR_CODE_NOT_DEDICATED
		}
		c.sendError(m.Req, code, err.Error())
		return
	}
	if code, msg := c.rememberCommand(cmd.CommandId, time.UnixMilli(cmd.ExpiresAt), now); code != 0 {
		c.sendError(m.Req, code, msg)
		return
	}
	switch a := cmd.Action.(type) {
	case *wire.ModerationCommand_Block:
		c.blockStreams(m.Req, a.Block, now)
	case *wire.ModerationCommand_Unblock:
		found, err := s.blocks.remove(a.Unblock.BlockId)
		if err != nil {
			s.log.Error("saving the block list failed", "err", err)
			c.sendError(m.Req, wire.ErrorCode_ERROR_CODE_LIMIT, "the relay couldn't save its block list")
			return
		}
		if found {
			s.log.Info("network block lifted", "block", blockIDString(a.Unblock.BlockId))
		}
		c.sendFrame(&wire.RelayFrame{Kind: &wire.RelayFrame_ModerationResult{ModerationResult: &wire.ModerationResult{
			Req: m.Req, NotFound: !found,
		}}})
	}
}

// rememberCommand records a command id until its expiry, refusing one seen
// before (replay on the same connection; another connection's nonce
// differs, so Verify refuses it there).
func (c *conn) rememberCommand(id []byte, exp, now time.Time) (wire.ErrorCode, string) {
	c.fmu.Lock()
	defer c.fmu.Unlock()
	if c.seenCommands == nil {
		c.seenCommands = make(map[string]time.Time)
	}
	for k, e := range c.seenCommands {
		if !now.Before(e) {
			delete(c.seenCommands, k)
		}
	}
	if _, ok := c.seenCommands[string(id)]; ok {
		return wire.ErrorCode_ERROR_CODE_BAD_COMMAND, "moderation command was already applied"
	}
	if len(c.seenCommands) >= maxSeenCommands {
		return wire.ErrorCode_ERROR_CODE_LIMIT, "too many moderation commands at once"
	}
	c.seenCommands[string(id)] = exp
	return 0, ""
}

// blockStreams blocks the networks of the client connections behind the
// named inbound streams of c (the server's endpoint connection), closes
// every connection from them, and answers with the block's id. The server's
// own network is never blocked.
func (c *conn) blockStreams(req uint32, b *wire.NetworkBlock, now time.Time) {
	s := c.srv
	nets := map[string]bool{}
	known, skipped := 0, 0
	c.smu.Lock()
	for _, id := range b.StreamIds {
		l := c.inStreams[id]
		if l == nil || l.closed.Load() {
			continue
		}
		known++
		if l.client.netKey == c.netKey {
			skipped++
			continue
		}
		nets[l.client.netKey] = true
	}
	c.smu.Unlock()
	if known == 0 {
		c.sendError(req, wire.ErrorCode_ERROR_CODE_BAD_COMMAND, "no such stream on this connection")
		return
	}
	res := &wire.ModerationResult{Req: req, Skipped: uint32(skipped)}
	if len(nets) > 0 {
		id := make([]byte, relaymod.BlockIDLen)
		if _, err := rand.Read(id); err != nil {
			c.sendError(req, wire.ErrorCode_ERROR_CODE_UNSPECIFIED, "no randomness")
			return
		}
		var expires int64
		if b.Seconds > 0 {
			expires = now.Add(time.Duration(b.Seconds) * time.Second).UnixMilli()
		}
		list := make([]string, 0, len(nets))
		for n := range nets {
			list = append(list, n)
		}
		if err := s.blocks.add(&blockEntry{ID: id, Networks: list, CreatedAt: now.UnixMilli(), ExpiresAt: expires}, now); err != nil {
			if !errors.Is(err, errBlockListFull) {
				s.log.Error("saving the block list failed", "err", err)
			}
			c.sendError(req, wire.ErrorCode_ERROR_CODE_LIMIT, err.Error())
			return
		}
		res.BlockId = id
		res.Networks = uint32(len(list))
		res.Closed = uint32(s.closeNetworks(nets, c))
		s.log.Info("network block added", "block", blockIDString(id), "networks", len(list), "closed", res.Closed, "seconds", b.Seconds)
	}
	c.sendFrame(&wire.RelayFrame{Kind: &wire.RelayFrame_ModerationResult{ModerationResult: res}})
}

// closeNetworks closes every connection from nets except keep, and returns
// how many it closed. What a connection already has queued (the server's
// ban notice on the member's session) is sent first, for up to a second.
func (s *Server) closeNetworks(nets map[string]bool, keep *conn) int {
	s.mu.RLock()
	var victims []*conn
	for c := range s.conns {
		if c != keep && nets[c.netKey] {
			victims = append(victims, c)
		}
	}
	s.mu.RUnlock()
	for _, c := range victims {
		c.closeAfterFlush("network blocked")
	}
	return len(victims)
}

func blockIDString(id []byte) string { return base64.RawURLEncoding.EncodeToString(id) }

// Blocked reports whether connections from network (ops.NetworkKey) are
// refused now (tests and the WebSocket upgrade).
func (s *Server) Blocked(network string) bool {
	return s.blocks.blocked(network, time.Now())
}

// BlockedNetworks is how many networks the block list holds.
func (s *Server) BlockedNetworks() int { return s.blocks.count(time.Now()) }

// ---------------------------------------------------------------- block list

var errBlockListFull = errors.New("the relay's block list is full (max_blocked_networks)")

// blockEntry is one block: the networks it covers, when it was made and when
// it ends (Unix milliseconds; 0 = until lifted). It is also the file format.
type blockEntry struct {
	ID        []byte   `json:"id"`
	Networks  []string `json:"networks"`
	CreatedAt int64    `json:"created_at"`
	ExpiresAt int64    `json:"expires_at"`
}

type blockFile struct {
	Version int           `json:"version"`
	Blocks  []*blockEntry `json:"blocks"`
}

// blockList holds a dedicated relay's blocks, persisted to path after
// every change (written to a temporary file and renamed, mode 0600).
// Expired blocks are dropped as they are found. A nil list (a relay that
// isn't dedicated) blocks nothing.
type blockList struct {
	mu       sync.Mutex
	path     string
	max      int
	entries  map[string]*blockEntry
	nets     map[string]int // network -> blocks naming it
	networks int            // networks across all blocks
	// nextExpiry is the earliest expiry among the blocks (0 = none).
	nextExpiry int64
}

// loadBlockList reads path (a missing file is an empty list).
func loadBlockList(path string, max int, now time.Time) (*blockList, error) {
	b := &blockList{path: path, max: max, entries: map[string]*blockEntry{}, nets: map[string]int{}}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return b, nil
	}
	if err != nil {
		return nil, err
	}
	var f blockFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("block file %s: %w", path, err)
	}
	if f.Version != 1 {
		return nil, fmt.Errorf("block file %s: unknown version %d", path, f.Version)
	}
	for _, e := range f.Blocks {
		if len(e.ID) != relaymod.BlockIDLen || len(e.Networks) == 0 {
			return nil, fmt.Errorf("block file %s: malformed block", path)
		}
		if e.ExpiresAt != 0 && e.ExpiresAt <= now.UnixMilli() {
			continue
		}
		b.insertLocked(e)
	}
	return b, nil
}

func (b *blockList) insertLocked(e *blockEntry) {
	b.entries[string(e.ID)] = e
	for _, n := range e.Networks {
		b.nets[n]++
	}
	b.networks += len(e.Networks)
	if e.ExpiresAt != 0 && (b.nextExpiry == 0 || e.ExpiresAt < b.nextExpiry) {
		b.nextExpiry = e.ExpiresAt
	}
}

func (b *blockList) deleteLocked(e *blockEntry) {
	delete(b.entries, string(e.ID))
	for _, n := range e.Networks {
		if b.nets[n]--; b.nets[n] <= 0 {
			delete(b.nets, n)
		}
	}
	b.networks -= len(e.Networks)
}

// expireLocked drops blocks that have ended.
func (b *blockList) expireLocked(now time.Time) {
	ms := now.UnixMilli()
	if b.nextExpiry == 0 || ms < b.nextExpiry {
		return
	}
	b.nextExpiry = 0
	for _, e := range b.entries {
		switch {
		case e.ExpiresAt == 0:
		case e.ExpiresAt <= ms:
			b.deleteLocked(e)
		case b.nextExpiry == 0 || e.ExpiresAt < b.nextExpiry:
			b.nextExpiry = e.ExpiresAt
		}
	}
}

func (b *blockList) blocked(network string, now time.Time) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expireLocked(now)
	return b.nets[network] > 0
}

func (b *blockList) count(now time.Time) int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expireLocked(now)
	return len(b.nets)
}

// add stores a block and saves the list; on a failed save nothing changes.
func (b *blockList) add(e *blockEntry, now time.Time) error {
	if b == nil {
		return errors.New("this relay keeps no block list")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expireLocked(now)
	if b.networks+len(e.Networks) > b.max {
		return errBlockListFull
	}
	b.insertLocked(e)
	if err := b.saveLocked(); err != nil {
		b.deleteLocked(e)
		return err
	}
	return nil
}

// remove lifts a block and saves the list; false if there was none.
func (b *blockList) remove(id []byte) (bool, error) {
	if b == nil {
		return false, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.entries[string(id)]
	if e == nil {
		return false, nil
	}
	b.deleteLocked(e)
	if err := b.saveLocked(); err != nil {
		b.insertLocked(e)
		return false, err
	}
	return true, nil
}

func (b *blockList) saveLocked() error {
	f := blockFile{Version: 1, Blocks: make([]*blockEntry, 0, len(b.entries))}
	for _, e := range b.entries {
		f.Blocks = append(f.Blocks, e)
	}
	raw, err := json.MarshalIndent(f, "", " ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(b.path), ".blocks-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, b.path)
}
