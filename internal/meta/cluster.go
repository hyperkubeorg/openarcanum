package meta

// Maintainer sets (docs/protocol.md §7.5): the signed member list of a
// maintainer cluster. The operator key (kept offline, `meta operator
// keygen`) signs a MaintainerSet naming each member's URL and node key; a
// node configured with the cluster's cluster_operator_key trusts exactly
// the members of the newest valid set for that key (besides trusted_peers
// and `meta trust`), serves it at GET /v1/maintainers for clients, and
// replicates it to its peers like routes (kind 'm'), so publishing it on
// one member reaches them all.

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/meta/clusterset"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// maintainerSetSchema holds the newest set per operator key; rev puts it
// in the public replication log.
const maintainerSetSchema = `
CREATE TABLE maintainer_sets (
	operator_key BLOB PRIMARY KEY,
	seq INTEGER NOT NULL,
	signed BLOB NOT NULL,
	rev INTEGER NOT NULL
);
`

// storeSet keeps a verified set if it is newer than the one held for its
// operator key. operator, when set, is the only operator key accepted.
// Callers hold s.mu if they are the server.
func storeSet(ctx context.Context, db *sql.DB, signed *wire.SignedMaintainerSet, operator []byte, now time.Time) error {
	set, err := clusterset.Verify(signed, operator, now)
	if err != nil {
		return forbidden(err.Error())
	}
	raw, err := proto.Marshal(signed)
	if err != nil {
		return err
	}
	tx, err := beginWrite(ctx, db)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var seq uint64
	err = tx.QueryRowContext(ctx, `SELECT seq FROM maintainer_sets WHERE operator_key = ?`, set.OperatorKey).Scan(&seq)
	switch {
	case err == nil && set.Seq <= seq:
		return conflict(clusterset.ErrNotNewer.Error())
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return err
	}
	var rev int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO counters (k, v) VALUES ('rev', 1) ON CONFLICT (k) DO UPDATE SET v = v + 1 RETURNING v`).Scan(&rev); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO maintainer_sets (operator_key, seq, signed, rev) VALUES (?, ?, ?, ?)
ON CONFLICT (operator_key) DO UPDATE SET seq = excluded.seq, signed = excluded.signed, rev = excluded.rev`,
		set.OperatorKey, set.Seq, raw, rev); err != nil {
		return err
	}
	return tx.Commit()
}

// storeSetLocked is storeSet for the server (under s.mu).
func (s *Server) storeSet(ctx context.Context, signed *wire.SignedMaintainerSet) error {
	if s.operator == nil {
		return forbidden("this node isn't in a maintainer cluster (no cluster_operator_key)")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return storeSet(ctx, s.db, signed, s.operator, s.now())
}

// loadSet reads the stored set for operator (nil, nil if none).
func loadSet(ctx context.Context, q querier, operator []byte) (*wire.SignedMaintainerSet, error) {
	var raw []byte
	err := q.QueryRowContext(ctx, `SELECT signed FROM maintainer_sets WHERE operator_key = ?`, operator).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var signed wire.SignedMaintainerSet
	if err := proto.Unmarshal(raw, &signed); err != nil {
		return nil, err
	}
	return &signed, nil
}

// newestSet is the set stored last, whatever its operator key (nil, nil
// if none).
func newestSet(ctx context.Context, q querier) (*wire.SignedMaintainerSet, error) {
	var op []byte
	err := q.QueryRowContext(ctx, `SELECT operator_key FROM maintainer_sets ORDER BY rev DESC LIMIT 1`).Scan(&op)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return loadSet(ctx, q, op)
}

// currentSet is the valid set of this node's cluster (nil: none, or
// expired).
func (s *Server) currentSet(ctx context.Context) *wire.MaintainerSet {
	if s.operator == nil {
		return nil
	}
	signed, err := loadSet(ctx, s.db, s.operator)
	if err != nil || signed == nil {
		return nil
	}
	set, err := clusterset.Verify(signed, s.operator, s.now())
	if err != nil {
		return nil
	}
	return set
}

// setPeers are the members of the cluster's newest valid maintainer set.
func (s *Server) setPeers(ctx context.Context) []TrustedPeer {
	set := s.currentSet(ctx)
	if set == nil {
		return nil
	}
	var out []TrustedPeer
	for _, n := range set.Nodes {
		out = append(out, TrustedPeer{Key: n.NodeKey, URL: n.Url, Source: "set"})
	}
	return out
}

// setIndex is this node's position in its cluster's set (-1: not in one).
func (s *Server) setIndex(ctx context.Context) int {
	set := s.currentSet(ctx)
	if set == nil {
		return -1
	}
	for i, n := range set.Nodes {
		if bytes.Equal(n.NodeKey, s.id.key.Public) {
			return i
		}
	}
	return -1
}

// getMaintainers serves the newest set for ?operator= (default: this
// node's cluster).
func (s *Server) getMaintainers(w http.ResponseWriter, r *http.Request) {
	op := s.operator
	if q := r.URL.Query().Get("operator"); q != "" {
		k, err := identity.Decode(q)
		if err != nil || !identity.ValidPublic(k) {
			http.Error(w, "bad operator key", http.StatusBadRequest)
			return
		}
		op = k
	}
	var signed *wire.SignedMaintainerSet
	var err error
	if op == nil {
		// No cluster_operator_key (its members are trusted another way):
		// the set published here last, if any.
		signed, err = newestSet(r.Context(), s.db)
	} else {
		signed, err = loadSet(r.Context(), s.db, op)
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if signed == nil {
		http.Error(w, "no maintainer set for this operator key", http.StatusNotFound)
		return
	}
	writeProto(w, signed)
}

// publishSetFile stores maintainer_set_file at start, if newer.
func (s *Server) publishSetFile(ctx context.Context) error {
	if s.cfg.MaintainerSetFile == "" {
		return nil
	}
	b, err := os.ReadFile(s.cfg.MaintainerSetFile)
	if err != nil {
		return fmt.Errorf("maintainer_set_file: %w", err)
	}
	var signed wire.SignedMaintainerSet
	if err := proto.Unmarshal(b, &signed); err != nil {
		return fmt.Errorf("maintainer_set_file: not a signed maintainer set")
	}
	err = s.storeSet(ctx, &signed)
	var he *httpError
	if errors.As(err, &he) && he.status == http.StatusConflict {
		return nil // the node already holds this set or a newer one
	}
	if err != nil {
		return fmt.Errorf("maintainer_set_file: %w", err)
	}
	return nil
}

// PublishSet stores a signed set in the node's database (`meta set
// publish`); the node serves and replicates it from then on. operator,
// when set, is the only operator key accepted.
func (a *Admin) PublishSet(ctx context.Context, raw []byte, operator []byte) (*wire.MaintainerSet, error) {
	var signed wire.SignedMaintainerSet
	if err := proto.Unmarshal(raw, &signed); err != nil {
		return nil, errors.New("not a signed maintainer set")
	}
	if err := storeSet(ctx, a.db, &signed, operator, a.now()); err != nil {
		return nil, err
	}
	var set wire.MaintainerSet
	_ = proto.Unmarshal(signed.Set, &set)
	return &set, nil
}
