package meta

// The public directory can be switched off (directory_enabled, or `meta
// directory disable|enable` on a running node): the node then lists no
// communities. Listing posts are refused with 501 "directory disabled",
// search, snapshots and single listings answer the same, listings neither
// replicate out nor are taken in, and the stored ones are deleted. Routes
// are unaffected: invites and existing members keep working, and people
// join through invites shared another way. GET /v1/node says whether the
// directory is on, so apps hide "Find a community" when no node of theirs
// offers it (docs/protocol.md §7.2).

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
)

// directoryOffKey is set (1) by `meta directory disable`.
const directoryOffKey = "directory_off"

// errDirectoryOff is the refusal of every directory request while it is
// off: a status of its own, so clients and community servers recognise it.
var errDirectoryOff = &httpError{http.StatusNotImplemented, "directory disabled: this maintainer node doesn't list communities; join through an invite"}

// DirectoryOff is the status of directory requests to a node whose
// directory is switched off.
const DirectoryOff = http.StatusNotImplemented

// directoryOn reports whether this node serves the public directory.
func (s *Server) directoryOn(ctx context.Context) bool {
	if !s.cfg.DirectoryEnabled {
		return false
	}
	off, err := directoryDisabled(ctx, s.db)
	return err == nil && !off
}

func directoryDisabled(ctx context.Context, q querier) (bool, error) {
	var v int64
	err := q.QueryRowContext(ctx, `SELECT v FROM counters WHERE k = ?`, directoryOffKey).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return v != 0, err
}

// refuseDirectory answers a directory request with 501 when it is off.
func (s *Server) refuseDirectory(w http.ResponseWriter, r *http.Request) bool {
	if s.directoryOn(r.Context()) {
		return false
	}
	http.Error(w, errDirectoryOff.msg, errDirectoryOff.status)
	return true
}

// deleteListings removes every stored listing and its search rows.
func deleteListings(ctx context.Context, x execer) error {
	for _, q := range []string{`DELETE FROM listings_fts`, `DELETE FROM listing_tags`, `DELETE FROM listings`} {
		if _, err := x.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// DirectoryState is whether the node's directory is on, and why not.
type DirectoryState struct {
	On bool
	// OffInConfig: directory_enabled is false (only the config can change it).
	OffInConfig bool
}

// configDirectory is the config's directory_enabled; an Admin opened
// without the node's config (the zero Config) assumes the default, off.
func (a *Admin) configDirectory() bool { return a.cfg.DirectoryEnabled }

// Directory reports the node's directory state.
func (a *Admin) Directory(ctx context.Context) (DirectoryState, error) {
	off, err := directoryDisabled(ctx, a.db)
	return DirectoryState{On: a.configDirectory() && !off, OffInConfig: !a.configDirectory()}, err
}

// DisableDirectory switches the public directory off on a running node and
// deletes the stored listings. It returns how many it deleted.
func (a *Admin) DisableDirectory(ctx context.Context) (int, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO counters (k, v) VALUES (?, 1) ON CONFLICT (k) DO UPDATE SET v = 1`, directoryOffKey); err != nil {
		return 0, err
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM listings`).Scan(&n); err != nil {
		return 0, err
	}
	if err := deleteListings(ctx, tx); err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

// EnableDirectory switches it on again (listings come back with the
// communities' next publications). ErrInConfig if the config keeps it off.
func (a *Admin) EnableDirectory(ctx context.Context) error {
	if !a.configDirectory() {
		return ErrInConfig
	}
	_, err := a.db.ExecContext(ctx, `DELETE FROM counters WHERE k = ?`, directoryOffKey)
	return err
}
