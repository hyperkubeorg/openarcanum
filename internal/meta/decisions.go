package meta

// Moderation decisions inside a maintainer cluster (docs/protocol.md
// §7.5). Each node still decides for itself: decisions made with `meta
// directory`/`meta relays` are recorded with the node's key as their origin
// and go into the peer log, every member passes on what it receives (so a
// cluster that isn't fully connected still converges), and a member
// applies the decisions of the origins in its follow_decisions only. The
// newest decision per origin and key wins; this node's own decisions always
// win over followed ones, and a followed relist lifts only that origin's
// delisting.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/relayurl"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

const logDecision = "d"

// Decision actions.
const (
	actionDelist = "delist"
	actionRelist = "relist"
)

// decisionsSchema (schema version 13) records where each effective
// delisting comes from ("local" or the origin's node key, base64url) and
// keeps every decision seen, by origin, for passing on.
const decisionsSchema = `
ALTER TABLE delisted ADD COLUMN source TEXT NOT NULL DEFAULT 'local';
CREATE TABLE decisions (
	origin BLOB NOT NULL,
	kind TEXT NOT NULL,
	key TEXT NOT NULL,
	action TEXT NOT NULL,
	at INTEGER NOT NULL,
	PRIMARY KEY (origin, kind, key)
);
`

const sourceLocal = "local"

// decisionKey is a decision's peer log key: origin || kind || 0x00 || key.
func decisionKey(origin []byte, kind, key string) []byte {
	b := append(bytes.Clone(origin), kind...)
	b = append(b, 0)
	return append(b, key...)
}

func splitDecisionKey(k []byte) (origin []byte, kind, key string, ok bool) {
	if len(k) < 33 {
		return nil, "", "", false
	}
	kind, key, ok = strings.Cut(string(k[32:]), "\x00")
	return k[:32], kind, key, ok
}

// ctxPeerDecision is the signature context of a moderation decision, signed
// by the node key of the member where it was made (maintainer nodes only;
// clients never see decisions).
const ctxPeerDecision = "openarcanum/peer-decision/v1"

// decisionsSignedSchema keeps each decision's signature, to pass it on.
const decisionsSignedSchema = `ALTER TABLE decisions ADD COLUMN signature BLOB NOT NULL DEFAULT x''`

// decisionAhead bounds how far a decision's time may be ahead of this
// node's clock: a decision dated in the far future would win over every
// later one of its origin.
const decisionAhead = 10 * time.Minute

// decisionLogTTL is how long a decision stays in the peer log (the
// decision itself is kept): a member that was away for longer catches up
// from a backup (`meta init -replica-of ... -from-backup`).
const decisionLogTTL = 90 * 24 * time.Hour

// decisionBytes is what a decision's signature covers.
func decisionBytes(d *wire.PeerDecision) []byte {
	b := append(bytes.Clone(d.Origin), d.Kind...)
	b = append(b, 0)
	b = append(b, d.Key...)
	b = append(b, 0)
	b = append(b, d.Action...)
	b = append(b, 0)
	return binary.BigEndian.AppendUint64(b, uint64(d.At))
}

// verifyDecision checks a decision's signature by its origin.
func verifyDecision(d *wire.PeerDecision) error {
	return identity.Verify(d.Origin, ctxPeerDecision, decisionBytes(d), d.Signature)
}

// recordDecision signs and stores a decision made on this node with its
// node key (inside tx), and logs it for the peers.
func recordDecision(ctx context.Context, tx execer, node *identity.Key, kind, key, action string, at int64) error {
	d := &wire.PeerDecision{Origin: node.Public, Kind: kind, Key: key, Action: action, At: at}
	d.Signature = node.Sign(ctxPeerDecision, decisionBytes(d))
	return storeDecision(ctx, tx, d)
}

// storeDecision keeps a decision (inside tx) and logs it for the peers.
func storeDecision(ctx context.Context, tx execer, d *wire.PeerDecision) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO decisions (origin, kind, key, action, at, signature) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (origin, kind, key) DO UPDATE SET action = excluded.action, at = excluded.at, signature = excluded.signature`,
		d.Origin, d.Kind, d.Key, d.Action, d.At, d.Signature); err != nil {
		return err
	}
	return logPeer(ctx, tx, logDecision, decisionKey(d.Origin, d.Kind, d.Key), time.UnixMilli(d.At).Add(decisionLogTTL).UnixMilli())
}

// adminKey is the node key decisions made through the data directory are
// recorded under and signed with: the node's own (node_key_dir when the
// config, or the environment, names one).
func (a *Admin) adminKey() (*identity.Key, error) {
	if a.cfg.NodeKeyDir != "" {
		id, err := loadKeysFrom(a.cfg.NodeKeyDir)
		if err != nil {
			return nil, err
		}
		return id.key, nil
	}
	id, err := loadNodeIdentity(filepath.Dir(a.dbPath))
	if err != nil {
		return nil, err
	}
	return id.key, nil
}

func (s *Server) decisionEntry(ctx context.Context, k []byte) (*wire.PeerEntry, error) {
	origin, kind, key, ok := splitDecisionKey(k)
	if !ok {
		return nil, nil
	}
	d := &wire.PeerDecision{Origin: origin, Kind: kind, Key: key}
	err := s.db.QueryRowContext(ctx, `SELECT action, at, signature FROM decisions WHERE origin = ? AND kind = ? AND key = ?`, origin, kind, key).
		Scan(&d.Action, &d.At, &d.Signature)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &wire.PeerEntry{Entry: &wire.PeerEntry_Decision{Decision: d}}, nil
}

// follows reports whether this node applies origin's decisions.
func (s *Server) follows(origin []byte) bool {
	for _, f := range s.cfg.FollowDecisions {
		if k, err := identity.Decode(f); err == nil && bytes.Equal(k, origin) {
			return true
		}
	}
	return false
}

// checkDecision validates a peer's decision: well formed, signed by its
// origin, and not dated ahead of now by more than decisionAhead.
func checkDecision(d *wire.PeerDecision, now time.Time) bool {
	if !identity.ValidPublic(d.Origin) || (d.Action != actionDelist && d.Action != actionRelist) || d.At <= 0 ||
		d.At > now.Add(decisionAhead).UnixMilli() || verifyDecision(d) != nil {
		return false
	}
	switch d.Kind {
	case kindCommunity:
		k, err := identity.Decode(d.Key)
		return err == nil && identity.ValidPublic(k) && identity.Encode(k) == d.Key
	case kindRelay:
		c, err := relayurl.Canonical(d.Key)
		return err == nil && c == d.Key
	}
	return false
}

// applyDecision keeps a peer's decision (newest per origin and key), passes
// it on, and applies it when this node follows its origin. Only decisions
// of origins this node trusts now are kept: anyone's signed decisions
// would otherwise travel the cluster for good.
func (s *Server) applyDecision(ctx context.Context, d *wire.PeerDecision) (bool, error) {
	if !checkDecision(d, s.now()) {
		s.refused(refusedInvalid)
		return false, nil
	}
	if bytes.Equal(d.Origin, s.id.key.Public) {
		return false, nil // this node's own decision, back from a peer
	}
	if !s.isTrusted(ctx, d.Origin) {
		s.refused(refusedInvalid)
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := beginWrite(ctx, s.db)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var at int64
	err = tx.QueryRowContext(ctx, `SELECT at FROM decisions WHERE origin = ? AND kind = ? AND key = ?`, d.Origin, d.Kind, d.Key).Scan(&at)
	if err == nil && at >= d.At {
		return false, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if err := storeDecision(ctx, tx, d); err != nil {
		return false, err
	}
	if s.follows(d.Origin) {
		if err := s.followDecision(ctx, tx, d); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

// pruneDecisions drops the decisions of origins this node no longer trusts
// (Expire), so they stop being passed on; what they delisted stays, under
// their key as its source, until relisted.
func (s *Server) pruneDecisions(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT origin FROM decisions`)
	if err != nil {
		return err
	}
	var origins [][]byte
	for rows.Next() {
		var o []byte
		if err := rows.Scan(&o); err != nil {
			rows.Close()
			return err
		}
		origins = append(origins, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	trusted := s.trustedPeers(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range origins {
		if _, ok := trusted[string(o)]; ok || bytes.Equal(o, s.id.key.Public) {
			continue
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM decisions WHERE origin = ?`, o); err != nil {
			return err
		}
	}
	return nil
}

// followDecision applies a followed origin's decision (inside tx): a
// delisting this node made itself is never changed.
func (s *Server) followDecision(ctx context.Context, tx *sql.Tx, d *wire.PeerDecision) error {
	source := identity.Encode(d.Origin)
	var have string
	err := tx.QueryRowContext(ctx, `SELECT source FROM delisted WHERE kind = ? AND key = ?`, d.Kind, d.Key).Scan(&have)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	local := err == nil && have == sourceLocal
	switch d.Action {
	case actionDelist:
		if local {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO delisted (kind, key, reason, at, source) VALUES (?, ?, '', ?, ?)
ON CONFLICT (kind, key) DO UPDATE SET at = excluded.at, source = excluded.source`, d.Kind, d.Key, d.At, source); err != nil {
			return err
		}
		if d.Kind == kindCommunity {
			k, _ := identity.Decode(d.Key)
			_, err := deleteCommunity(ctx, tx, k)
			return err
		}
		_, err := deleteRelayHost(ctx, tx, relayurl.Host(d.Key))
		return err
	case actionRelist:
		_, err := tx.ExecContext(ctx, `DELETE FROM delisted WHERE kind = ? AND key = ? AND source = ?`, d.Kind, d.Key, source)
		return err
	}
	return nil
}
