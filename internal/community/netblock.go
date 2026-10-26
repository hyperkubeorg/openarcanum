package community

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/relayclient"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Network blocks on a dedicated relay (docs/protocol.md §2 "Dedicated
// relays", §4.7 "Network blocks"). A community pinned to relays that are all
// dedicated to this server can ask them to block a banned member's network
// address. The server never learns the address: it names the member's
// session streams on the relay, and keeps only the opaque block id the relay
// returns. A member who isn't connected when banned is blocked the next
// time they try to connect through the relay (their session is refused as
// banned, and its stream names their address once more). Unbanning lifts the
// block by its id; a relay that can't be reached is asked again when the
// server attaches to it.

// Network block durations a ban may ask for (0: until the ban is lifted).
var netBlockDurations = map[uint32]bool{0: true, 86400: true, 604800: true, 2592000: true}

// netBlockTimeout bounds one moderation command's round trip.
const netBlockTimeout = 10 * time.Second

// Network block states.
const (
	netBlockPending = "pending" // asked for; the member hasn't been seen on a dedicated relay since
	netBlockActive  = "active"  // the relay holds BlockID
	netBlockLift    = "lift"    // the ban was lifted; the relay still has to drop BlockID
)

// netBlockRec is one network block, sealed in net_blocks (schema 9), bound
// to its row id; the kid column is the member's lookup hash.
type netBlockRec struct {
	Member  []byte `json:"member"`
	By      []byte `json:"by"`
	At      int64  `json:"at"`
	Seconds uint32 `json:"seconds"`
	State   string `json:"state"`
	// Set once the relay holds the block.
	Relay   string `json:"relay,omitempty"`
	BlockID []byte `json:"block_id,omitempty"`
	// When the relay drops the block by itself (0: when lifted).
	Until int64 `json:"until,omitempty"`
}

type netBlockRow struct {
	id  int64
	rec netBlockRec
}

const schemaV9Tables = `
CREATE TABLE net_blocks (
	id INTEGER PRIMARY KEY,
	kid BLOB NOT NULL,
	data BLOB NOT NULL
);
CREATE INDEX net_blocks_kid ON net_blocks (kid);
`

// loadNetBlocks returns member's network blocks, or every one (member nil).
func (s *Server) loadNetBlocks(ctx context.Context, q dbtx, member []byte) ([]netBlockRow, error) {
	query, args := `SELECT id, kid, data FROM net_blocks ORDER BY id`, []any{}
	if member != nil {
		query, args = `SELECT id, kid, data FROM net_blocks WHERE kid = ? ORDER BY id`, []any{s.kid(member)}
	}
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []netBlockRow
	for rows.Next() {
		var r netBlockRow
		var kid, data []byte
		if err := rows.Scan(&r.id, &kid, &data); err != nil {
			return nil, err
		}
		if err := s.openJSON("net_blocks", "data", rowID(r.id), data, &r.rec); err != nil {
			return nil, err
		}
		if !bytes.Equal(s.kid(r.rec.Member), kid) {
			return nil, &TamperError{"net_blocks", "kid", rowID(r.id)}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Server) insertNetBlock(ctx context.Context, rec *netBlockRec) error {
	id, err := s.nextID(ctx, s.db, "net_blocks")
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO net_blocks (id, kid, data) VALUES (?, ?, ?)`,
		id, s.kid(rec.Member), s.sealJSON("net_blocks", "data", rowID(id), rec))
	return err
}

func (s *Server) updateNetBlock(ctx context.Context, r *netBlockRow) error {
	_, err := s.db.ExecContext(ctx, `UPDATE net_blocks SET data = ? WHERE id = ?`, s.sealJSON("net_blocks", "data", rowID(r.id), &r.rec), r.id)
	return err
}

func (s *Server) deleteNetBlock(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM net_blocks WHERE id = ?`, id)
	return err
}

// ---------------------------------------------------------------- dedicated relays

// noteRelay records whether the relay at url is dedicated to key (its
// Hello), and pushes fresh state when that changes whether bans can block
// network addresses.
func (s *Server) noteRelay(url string, c *relayclient.Client, key []byte) {
	s.mu.RLock()
	before := s.dedicatedRelayActiveLocked()
	s.mu.RUnlock()
	s.rmu.Lock()
	s.dedicated[url] = len(c.DedicatedTo()) > 0 && bytes.Equal(c.DedicatedTo(), key)
	s.rmu.Unlock()
	s.mu.RLock()
	after := s.dedicatedRelayActiveLocked()
	s.mu.RUnlock()
	if before != after {
		s.broadcastState()
	}
}

// pinnedRelayURLs is the relay list when the server uses pinned relays (the
// first two cases of desiredRelays), or nil in automatic mode. Callers hold
// s.mu.
func (s *Server) pinnedRelayURLsLocked() []string {
	if s.settings.RelayMode == wire.RelayMode_RELAY_MODE_PINNED && len(s.settings.PinnedRelays) > 0 {
		return s.settings.PinnedRelays
	}
	if s.cfg.RelayMode == "pinned" {
		return s.cfg.Relays
	}
	return nil
}

// dedicatedRelayActiveLocked reports whether the community can block network
// addresses: it is pinned to relays, and every one of them said (when the
// server last attached to it) that it is dedicated to this server. So every
// member connects through a relay that enforces blocks. Callers hold s.mu.
func (s *Server) dedicatedRelayActiveLocked() bool {
	urls := s.pinnedRelayURLsLocked()
	if len(urls) == 0 {
		return false
	}
	s.rmu.RLock()
	defer s.rmu.RUnlock()
	for _, u := range urls {
		if !s.dedicated[u] {
			return false
		}
	}
	return true
}

// dedicatedClient returns the attached client for url if that relay is
// dedicated to this server.
func (s *Server) dedicatedClient(url string) *relayclient.Client {
	s.rmu.RLock()
	defer s.rmu.RUnlock()
	if !s.dedicated[url] {
		return nil
	}
	return s.relays[url]
}

func (s *Server) isDedicated(c *relayclient.Client) bool {
	return c != nil && s.dedicatedClient(c.URL) == c
}

// ---------------------------------------------------------------- blocking

// checkNetBlock validates a ban's network block request; callers hold s.mu.
func (s *Server) checkNetBlock(seconds uint32) error {
	if !netBlockDurations[seconds] {
		return errInvalid("a network block lasts 1 day, 7 days, 30 days or until the ban is lifted")
	}
	if !s.dedicatedRelayActiveLocked() {
		return errInvalid("this community isn't on a dedicated relay, so network addresses can't be blocked")
	}
	return nil
}

// requestNetBlock records a pending network block for a ban; callers hold
// s.mu. applyNetBlock carries it out once s.mu is released.
func (s *Server) requestNetBlock(ctx context.Context, member, by []byte, seconds uint32) error {
	return s.insertNetBlock(ctx, &netBlockRec{Member: member, By: by, At: s.now().UnixMilli(), Seconds: seconds, State: netBlockPending})
}

// applyNetBlock asks the dedicated relays the banned member is connected
// through to block their networks, and sets result's status. Runs after the
// ban, before the member's sessions are closed.
func (s *Server) applyNetBlock(ctx context.Context, member []byte, result *wire.AdminResult) {
	streams := map[*relayclient.Client][]uint32{}
	sessions := s.sessionsFor(member)
	// Tell the member they're banned first: the relay delivers what its
	// connection already has queued before closing it (docs/protocol.md
	// §2), so the notice has to be on the wire before the block command.
	for _, ss := range sessions {
		ss.setStatus(wire.MemberStatus_MEMBER_STATUS_BANNED)
		ss.send(&wire.ServerMessage{Body: &wire.ServerMessage_StateChanged{StateChanged: &wire.StateChanged{Status: wire.MemberStatus_MEMBER_STATUS_BANNED}}})
	}
	deadline := time.Now().Add(time.Second)
	for _, ss := range sessions {
		for len(ss.out) > 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if len(sessions) > 0 {
		time.Sleep(20 * time.Millisecond) // the writer's last Send
	}
	for _, ss := range sessions {
		if ss.stream != nil && s.isDedicated(ss.stream.Client()) {
			c := ss.stream.Client()
			streams[c] = append(streams[c], ss.stream.ID)
		}
	}
	result.NetworkBlock = wire.NetworkBlockStatus_NETWORK_BLOCK_STATUS_PENDING
	req := s.pendingNetBlock(ctx, member)
	if req == nil || len(streams) == 0 {
		return
	}
	done := false
	for c, ids := range streams {
		st := s.blockOn(ctx, c, req, ids, "")
		if st != wire.NetworkBlockStatus_NETWORK_BLOCK_STATUS_PENDING {
			done = true
		}
		// BLOCKED on any relay wins over SKIPPED on another.
		if st == wire.NetworkBlockStatus_NETWORK_BLOCK_STATUS_BLOCKED || result.NetworkBlock == wire.NetworkBlockStatus_NETWORK_BLOCK_STATUS_PENDING {
			result.NetworkBlock = st
		}
	}
	if done {
		s.dropNetBlockRequest(ctx, req)
	}
}

// applyPendingNetBlock: a banned member's session was refused on stream st;
// if their ban asked for a network block that no relay holds yet and st runs
// through a dedicated relay, block it now.
func (s *Server) applyPendingNetBlock(ctx context.Context, member []byte, st *relayclient.Stream) {
	if st == nil || !s.isDedicated(st.Client()) {
		return
	}
	req := s.pendingNetBlock(ctx, member)
	if req == nil {
		return
	}
	if s.blockOn(ctx, st.Client(), req, []uint32{st.ID}, " when they tried to reconnect") != wire.NetworkBlockStatus_NETWORK_BLOCK_STATUS_PENDING {
		s.dropNetBlockRequest(ctx, req)
	}
}

// pendingNetBlock is member's network block request that no relay has
// carried out yet, or nil.
func (s *Server) pendingNetBlock(ctx context.Context, member []byte) *netBlockRow {
	s.mu.RLock()
	rows, err := s.loadNetBlocks(ctx, s.db, member)
	s.mu.RUnlock()
	if err != nil {
		s.log.Error("reading network blocks failed", "err", err)
		return nil
	}
	for i := range rows {
		if rows[i].rec.State == netBlockPending {
			return &rows[i]
		}
	}
	return nil
}

// dropNetBlockRequest forgets a request once relays have carried it out.
func (s *Server) dropNetBlockRequest(ctx context.Context, req *netBlockRow) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.deleteNetBlock(ctx, req.id); err != nil {
		s.log.Error("database write failed", "err", err)
	}
}

// blockOn sends one block command for request req, naming the member's
// streams on the dedicated relay c, and records the outcome: an active block
// row for that relay (audited with the block id, never an address); only an
// audit entry when the member's network is the server's own; or PENDING
// when the relay didn't take it (the request stays for the next time the
// member connects).
func (s *Server) blockOn(ctx context.Context, c *relayclient.Client, req *netBlockRow, streams []uint32, when string) wire.NetworkBlockStatus {
	s.mu.RLock()
	key := s.key
	s.mu.RUnlock()
	cctx, cancel := context.WithTimeout(ctx, netBlockTimeout)
	defer cancel()
	res, err := c.Moderate(cctx, key, &wire.ModerationCommand{Action: &wire.ModerationCommand_Block{Block: &wire.NetworkBlock{
		StreamIds: streams, Seconds: req.rec.Seconds,
	}}})
	if err != nil {
		s.log.Warn("network block refused or unanswered; it stays pending", "relay", c.URL, "err", err)
		return wire.NetworkBlockStatus_NETWORK_BLOCK_STATUS_PENDING
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	member := req.rec.Member
	name := s.nameOf(member)
	if len(res.BlockId) == 0 {
		// Every stream came from the server's own network.
		s.recordAudit(ctx, req.rec.By, "network_block", member, fmt.Sprintf("couldn't block the network address of %s: it is the community server's own", name))
		return wire.NetworkBlockStatus_NETWORK_BLOCK_STATUS_SKIPPED
	}
	held := req.rec
	held.State, held.Relay, held.BlockID = netBlockActive, c.URL, res.BlockId
	if held.Seconds > 0 {
		held.Until = s.now().Add(time.Duration(held.Seconds) * time.Second).UnixMilli()
	}
	if err := s.insertNetBlock(ctx, &held); err != nil {
		s.log.Error("database write failed", "err", err)
	}
	span := "until the ban is lifted"
	if held.Seconds > 0 {
		span = "for " + humanSeconds(held.Seconds)
	}
	s.recordAudit(ctx, req.rec.By, "network_block", member, fmt.Sprintf("blocked the network address of %s on the community's relay %s%s (block %s)",
		name, span, when, blockIDString(res.BlockId)))
	return wire.NetworkBlockStatus_NETWORK_BLOCK_STATUS_BLOCKED
}

func blockIDString(id []byte) string { return base64.RawURLEncoding.EncodeToString(id) }

// ---------------------------------------------------------------- lifting

// releaseNetBlocks marks member's network blocks for lifting when their ban
// is lifted: pending requests are dropped, held blocks are to be lifted.
// Reports whether any relay has a block to drop. Callers hold s.mu.
func (s *Server) releaseNetBlocks(ctx context.Context, member []byte) (bool, error) {
	rows, err := s.loadNetBlocks(ctx, s.db, member)
	if err != nil {
		return false, err
	}
	lift := false
	for i := range rows {
		r := &rows[i]
		switch r.rec.State {
		case netBlockPending:
			if err := s.deleteNetBlock(ctx, r.id); err != nil {
				return false, err
			}
		case netBlockActive:
			r.rec.State = netBlockLift
			if err := s.updateNetBlock(ctx, r); err != nil {
				return false, err
			}
			lift = true
		case netBlockLift:
			lift = true
		}
	}
	return lift, nil
}

// liftNetBlocks asks the relays to drop the blocks marked for lifting (of
// member, or every member when nil, on relay url or every relay when
// empty), and forgets blocks that ended by themselves. A relay that isn't
// attached is asked again when the server next attaches to it.
func (s *Server) liftNetBlocks(ctx context.Context, member []byte, url string) {
	s.mu.RLock()
	rows, err := s.loadNetBlocks(ctx, s.db, member)
	key := s.key
	now := s.now().UnixMilli()
	s.mu.RUnlock()
	if err != nil {
		s.log.Error("reading network blocks failed", "err", err)
		return
	}
	for _, r := range rows {
		if url != "" && r.rec.Relay != url {
			continue
		}
		if r.rec.State == netBlockActive && r.rec.Until != 0 && r.rec.Until <= now {
			s.mu.Lock()
			if err := s.deleteNetBlock(ctx, r.id); err != nil {
				s.log.Error("database write failed", "err", err)
			}
			s.mu.Unlock()
			continue
		}
		if r.rec.State != netBlockLift {
			continue
		}
		c := s.dedicatedClient(r.rec.Relay)
		if c == nil {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, netBlockTimeout)
		res, err := c.Moderate(cctx, key, &wire.ModerationCommand{Action: &wire.ModerationCommand_Unblock{Unblock: &wire.NetworkUnblock{BlockId: r.rec.BlockID}}})
		cancel()
		if err != nil {
			s.log.Warn("lifting a network block failed; it will be retried", "relay", c.URL, "err", err)
			continue
		}
		s.mu.Lock()
		if err := s.deleteNetBlock(ctx, r.id); err != nil {
			s.log.Error("database write failed", "err", err)
		}
		summary := fmt.Sprintf("lifted the network block on %s (block %s)", s.nameOf(r.rec.Member), blockIDString(r.rec.BlockID))
		if res.NotFound {
			summary += "; the relay had already dropped it"
		}
		s.recordAudit(ctx, nil, "network_unblock", r.rec.Member, summary)
		s.mu.Unlock()
	}
}

// netBlockStatus summarizes member's network blocks for the ban list: held
// (and until when; 0 = until lifted), else pending. Callers hold s.mu.
func netBlockStatus(rows []netBlockRow) (wire.NetworkBlockStatus, int64) {
	st, until := wire.NetworkBlockStatus_NETWORK_BLOCK_STATUS_UNSPECIFIED, int64(-1)
	for _, r := range rows {
		switch r.rec.State {
		case netBlockActive:
			st = wire.NetworkBlockStatus_NETWORK_BLOCK_STATUS_BLOCKED
			if r.rec.Until == 0 || until == 0 {
				until = 0
			} else {
				until = max(until, r.rec.Until)
			}
		case netBlockPending:
			if st == wire.NetworkBlockStatus_NETWORK_BLOCK_STATUS_UNSPECIFIED {
				st = wire.NetworkBlockStatus_NETWORK_BLOCK_STATUS_PENDING
			}
		}
	}
	if until < 0 {
		until = 0
	}
	return st, until
}

// ---------------------------------------------------------------- the server's public key file

// writePublicKeyFile writes the current server public key (hex) to
// server_public_key_file, where a relay dedicated to this server can read
// it (dedicated_server_key_file; docs/kubernetes.md). Written at start and
// after every key rotation, by a rename so a reader never sees half a key.
func (s *Server) writePublicKeyFile(pub []byte) error {
	path := s.cfg.ServerPublicKeyFile
	if path == "" {
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".server-pub-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(hex.EncodeToString(pub) + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
