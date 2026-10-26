package meta

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/relayurl"
	"github.com/hyperkubeorg/openarcanum/internal/store"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Maintainer moderation (`meta directory` and `meta relays` in cmd/meta;
// docs/operations.md "Maintainer admin commands"): delisting communities and removing relays from the
// registry. Decisions live in the node's database (the `delisted` table),
// next to the config file's `delisted` and `delisted_relays` lists, so the
// admin commands work while the node runs: the node consults the table on
// every listing, route and heartbeat it stores and on every relay list it
// serves. Each maintainer decides for its own node; inside a maintainer
// cluster, decisions are passed between members and applied by those that
// follow their origin (decisions.go).

const delistedSchema = `
CREATE TABLE delisted (
	kind TEXT NOT NULL,
	key TEXT NOT NULL,
	reason TEXT NOT NULL DEFAULT '',
	at INTEGER NOT NULL,
	PRIMARY KEY (kind, key)
);
`

const (
	kindCommunity = "community"
	kindRelay     = "relay"
)

// MaxReason bounds an admin's note on a decision.
const MaxReason = 500

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// communityDelisted reports whether this node delisted community (config or
// admin command).
func (s *Server) communityDelisted(ctx context.Context, q querier, community []byte) (bool, error) {
	if s.delisted[string(community)] {
		return true, nil
	}
	return inTable(ctx, q, kindCommunity, identity.Encode(community))
}

// relayRemoved reports whether this node removed the relay at the
// canonical URL url: a removal covers every URL on the removed URL's host
// (any port or path), so a removed relay can't come back by spelling its
// URL differently.
func (s *Server) relayRemoved(ctx context.Context, q queryer, url string) (bool, error) {
	host := relayurl.Host(url)
	if s.delistedRelays[host] {
		return true, nil
	}
	removed, err := removedRelayHosts(ctx, q)
	return removed[host], err
}

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// removedRelayHosts is the set of hosts of the admin's relay removals.
func removedRelayHosts(ctx context.Context, q queryer) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT key FROM delisted WHERE kind = ?`, kindRelay)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[relayurl.Host(k)] = true
	}
	return out, rows.Err()
}

func inTable(ctx context.Context, q querier, kind, key string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM delisted WHERE kind = ? AND key = ?`, kind, key).Scan(&n)
	return n > 0, err
}

// Admin manages a node's moderation decisions through its data directory.
// It is safe to use while the node runs (SQLite transactions, WAL).
type Admin struct {
	db     *sql.DB
	dbPath string
	cfg    Config
	now    func() time.Time
}

// OpenAdmin opens the node's database. cfg is the node's config, read for
// the decisions it already lists; it may be the zero Config.
func OpenAdmin(ctx context.Context, dataDir string, cfg Config) (*Admin, error) {
	dbPath := filepath.Join(dataDir, "meta.db")
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("no maintainer database in %s: %w", dataDir, err)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return nil, err
	}
	// Never migrate: the node may be running an older release on this
	// database right now.
	if err := store.Check(ctx, db, len(migrations)); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s: %w", dbPath, err)
	}
	return &Admin{db: db, dbPath: dbPath, cfg: cfg, now: time.Now}, nil
}

// Close releases the database.
func (a *Admin) Close() error { return a.db.Close() }

// canonicalRelayURLs (schema version 7) puts the stored relay URLs in
// canonical form (internal/relayurl), the form heartbeats and admin
// commands use. A registry entry whose URL isn't a usable relay URL is dropped
// (a live relay registers again with its next heartbeat); of entries that
// turn out to be the same relay, the freshest stays. Removal decisions keep
// a key that doesn't parse as it was.
func canonicalRelayURLs(ctx context.Context, tx store.Tx) error {
	type relayRow struct {
		url string
		at  int64
	}
	rows, err := tx.QueryContext(ctx, `SELECT url, at FROM relays ORDER BY at DESC`)
	if err != nil {
		return err
	}
	var relays []relayRow
	for rows.Next() {
		var r relayRow
		if err := rows.Scan(&r.url, &r.at); err != nil {
			rows.Close()
			return err
		}
		relays = append(relays, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	// relays is freshest first, so the first row of each canonical URL is
	// the one kept.
	keep := map[string]string{} // canonical URL -> stored URL kept
	var drop []string
	for _, r := range relays {
		c, err := relayurl.Canonical(r.url)
		if _, dup := keep[c]; err != nil || dup {
			drop = append(drop, r.url)
			continue
		}
		keep[c] = r.url
	}
	for _, u := range drop {
		if _, err := tx.ExecContext(ctx, `DELETE FROM relays WHERE url = ?`, u); err != nil {
			return err
		}
	}
	for c, u := range keep {
		if c == u {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE relays SET url = ? WHERE url = ?`, c, u); err != nil {
			return err
		}
	}
	rows, err = tx.QueryContext(ctx, `SELECT key FROM delisted WHERE kind = ?`, kindRelay)
	if err != nil {
		return err
	}
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return err
		}
		keys = append(keys, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, k := range keys {
		c, err := relayurl.Canonical(k)
		if err != nil || c == k {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE OR IGNORE delisted SET key = ? WHERE kind = ? AND key = ?`, c, kindRelay, k); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM delisted WHERE kind = ? AND key = ?`, kindRelay, k); err != nil {
			return err
		}
	}
	return nil
}

func cleanReason(r string) (string, error) {
	r = strings.TrimSpace(r)
	if len(r) > MaxReason {
		return "", fmt.Errorf("the reason may be at most %d bytes", MaxReason)
	}
	return r, nil
}

// Delist removes a community's listing and routes from this node and
// refuses them from now on (also when they arrive through replication).
// It returns how many entries were removed.
func (a *Admin) Delist(ctx context.Context, community []byte, reason string) (int, error) {
	if !identity.ValidPublic(community) {
		return 0, errors.New("not a community id")
	}
	reason, err := cleanReason(reason)
	if err != nil {
		return 0, err
	}
	origin, err := a.adminKey()
	if err != nil {
		return 0, err
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := a.now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `INSERT INTO delisted (kind, key, reason, at, source) VALUES (?, ?, ?, ?, 'local')
ON CONFLICT (kind, key) DO UPDATE SET reason = excluded.reason, source = 'local'`, kindCommunity, identity.Encode(community), reason, now); err != nil {
		return 0, err
	}
	if err := recordDecision(ctx, tx, origin, kindCommunity, identity.Encode(community), actionDelist, now); err != nil {
		return 0, err
	}
	removed, err := deleteCommunity(ctx, tx, community)
	if err != nil {
		return 0, err
	}
	return removed, tx.Commit()
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// deleteCommunity removes a community's listing (with its search rows) and
// its routes.
func deleteCommunity(ctx context.Context, tx execer, community []byte) (int, error) {
	removed := 0
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM listings WHERE community = ?`, community).Scan(&id)
	switch {
	case err == nil:
		for _, q := range []string{`DELETE FROM listings_fts WHERE rowid = ?`, `DELETE FROM listing_tags WHERE listing_id = ?`, `DELETE FROM listings WHERE id = ?`} {
			if _, err := tx.ExecContext(ctx, q, id); err != nil {
				return 0, err
			}
		}
		removed++
	case !errors.Is(err, sql.ErrNoRows):
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM routes WHERE owner = ?`, community)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return removed + int(n), nil
}

// ErrInConfig means a decision comes from the config file, which only the
// operator's editor can change.
var ErrInConfig = errors.New("this entry is in the node's config file; remove it there and restart the node")

// Relist lifts an admin delisting; the community's next publication lists
// it again. It reports whether there was one.
func (a *Admin) Relist(ctx context.Context, community []byte) (bool, error) {
	for _, d := range a.cfg.Delisted {
		if b, err := identity.Decode(d); err == nil && string(b) == string(community) {
			return false, ErrInConfig
		}
	}
	origin, err := a.adminKey()
	if err != nil {
		return false, err
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM delisted WHERE kind = ? AND key = ?`, kindCommunity, identity.Encode(community))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return false, nil
	}
	if err := recordDecision(ctx, tx, origin, kindCommunity, identity.Encode(community), actionRelist, a.now().UnixMilli()); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// RemoveRelay drops a relay from this node's registry and refuses its
// heartbeats from now on. The removal covers every URL on the relay's host
// (any port or path, compared in canonical form; internal/relayurl). It
// reports whether any registered relay was dropped.
func (a *Admin) RemoveRelay(ctx context.Context, url, reason string) (bool, error) {
	url, err := relayurl.Canonical(strings.TrimSpace(url))
	if err != nil {
		return false, fmt.Errorf("a relay is named by its ws:// or wss:// URL: %w", err)
	}
	reason, err = cleanReason(reason)
	if err != nil {
		return false, err
	}
	origin, err := a.adminKey()
	if err != nil {
		return false, err
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	now := a.now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `INSERT INTO delisted (kind, key, reason, at, source) VALUES (?, ?, ?, ?, 'local')
ON CONFLICT (kind, key) DO UPDATE SET reason = excluded.reason, source = 'local'`, kindRelay, url, reason, now); err != nil {
		return false, err
	}
	if err := recordDecision(ctx, tx, origin, kindRelay, url, actionDelist, now); err != nil {
		return false, err
	}
	n, err := deleteRelayHost(ctx, tx, relayurl.Host(url))
	if err != nil {
		return false, err
	}
	return n > 0, tx.Commit()
}

// deleteRelayHost drops every registered relay on host.
func deleteRelayHost(ctx context.Context, tx *sql.Tx, host string) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT url FROM relays`)
	if err != nil {
		return 0, err
	}
	var victims []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			rows.Close()
			return 0, err
		}
		if relayurl.Host(u) == host {
			victims = append(victims, u)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, u := range victims {
		if _, err := tx.ExecContext(ctx, `DELETE FROM relays WHERE url = ?`, u); err != nil {
			return 0, err
		}
	}
	return len(victims), nil
}

// RestoreRelay lifts the removals of the relay's host; the relay's next
// heartbeat registers it.
func (a *Admin) RestoreRelay(ctx context.Context, url string) (bool, error) {
	url, err := relayurl.Canonical(strings.TrimSpace(url))
	if err != nil {
		return false, fmt.Errorf("a relay is named by its ws:// or wss:// URL: %w", err)
	}
	host := relayurl.Host(url)
	for _, r := range a.cfg.DelistedRelays {
		if c, err := relayurl.Canonical(r); err == nil && relayurl.Host(c) == host {
			return false, ErrInConfig
		}
	}
	rows, err := a.db.QueryContext(ctx, `SELECT key FROM delisted WHERE kind = ?`, kindRelay)
	if err != nil {
		return false, err
	}
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return false, err
		}
		if relayurl.Host(k) == host {
			keys = append(keys, k)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	origin, err := a.adminKey()
	if err != nil {
		return false, err
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	for _, k := range keys {
		if _, err := tx.ExecContext(ctx, `DELETE FROM delisted WHERE kind = ? AND key = ?`, kindRelay, k); err != nil {
			return false, err
		}
		if err := recordDecision(ctx, tx, origin, kindRelay, k, actionRelist, a.now().UnixMilli()); err != nil {
			return false, err
		}
	}
	return len(keys) > 0, tx.Commit()
}

// Decision is one delisted community or removed relay.
type Decision struct {
	Kind, Key, Reason string
	At                time.Time
	FromConfig        bool
	// Source is "local" (made on this node) or the node key of the
	// cluster member it was followed from (follow_decisions).
	Source string
}

// Decisions lists every delisting and relay removal, config entries first.
func (a *Admin) Decisions(ctx context.Context) ([]Decision, error) {
	var out []Decision
	for _, d := range a.cfg.Delisted {
		out = append(out, Decision{Kind: kindCommunity, Key: d, FromConfig: true})
	}
	for _, r := range a.cfg.DelistedRelays {
		out = append(out, Decision{Kind: kindRelay, Key: r, FromConfig: true})
	}
	rows, err := a.db.QueryContext(ctx, `SELECT kind, key, reason, at, source FROM delisted ORDER BY kind, at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var d Decision
		var at int64
		if err := rows.Scan(&d.Kind, &d.Key, &d.Reason, &at, &d.Source); err != nil {
			return nil, err
		}
		d.At = time.UnixMilli(at)
		out = append(out, d)
	}
	return out, rows.Err()
}

// ListedCommunity is one entry of the directory as stored on this node.
type ListedCommunity struct {
	Community string
	Name      string
	Withdrawn bool
	UpdatedAt time.Time
}

// Listings lists the stored listings whose name contains match (any case;
// empty lists all), at most limit.
func (a *Admin) Listings(ctx context.Context, match string, limit int) ([]ListedCommunity, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT signed, withdrawn, updated_at FROM listings ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ListedCommunity
	match = strings.ToLower(match)
	for rows.Next() && len(out) < limit {
		var raw []byte
		var withdrawn int
		var at int64
		if err := rows.Scan(&raw, &withdrawn, &at); err != nil {
			return nil, err
		}
		var sl wire.SignedListing
		var l wire.Listing
		if proto.Unmarshal(raw, &sl) != nil || proto.Unmarshal(sl.Listing, &l) != nil {
			continue
		}
		if match != "" && !strings.Contains(strings.ToLower(l.Name), match) {
			continue
		}
		out = append(out, ListedCommunity{Community: identity.Encode(l.Community), Name: l.Name, Withdrawn: withdrawn != 0, UpdatedAt: time.UnixMilli(at)})
	}
	return out, rows.Err()
}

// RegisteredRelay is one relay in this node's registry.
type RegisteredRelay struct {
	URL, Region string
	Capacity    uint32
	Connections uint32
	LastSeen    time.Time
}

// Relays lists the registry as stored (fresh or not).
func (a *Admin) Relays(ctx context.Context) ([]RegisteredRelay, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT url, region, capacity, connections, at FROM relays ORDER BY url`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RegisteredRelay
	for rows.Next() {
		var r RegisteredRelay
		var at int64
		if err := rows.Scan(&r.URL, &r.Region, &r.Capacity, &r.Connections, &at); err != nil {
			return nil, err
		}
		r.LastSeen = time.UnixMilli(at)
		out = append(out, r)
	}
	return out, rows.Err()
}
