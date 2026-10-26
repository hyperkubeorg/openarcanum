// Package store opens SQLite databases (pure Go, via modernc.org/sqlite) and
// applies versioned schema migrations tracked in PRAGMA user_version.
package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

// Open opens (creating if needed) the database at path with WAL, foreign keys
// and a busy timeout. Use ":memory:" for an in-memory database. Extra
// pragmas (for example "secure_delete(1)") apply to every pooled
// connection.
func Open(path string, pragmas ...string) (*sql.DB, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	for _, p := range pragmas {
		dsn += "&_pragma=" + p
	}
	if path != ":memory:" {
		dsn += "&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
		// A new database file is made here, mode 0600, before SQLite would
		// make it with the default mode (the WAL and journal files take
		// the database file's mode).
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			return nil, err
		}
		f.Close()
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if path == ":memory:" {
		// Every connection to ":memory:" is a separate database.
		db.SetMaxOpenConns(1)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// Tx is what a migration's Go step runs against: the migration's own
// connection, inside its write transaction.
type Tx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Migration is one schema version step. Schema version N is reached by
// applying migrations[0..N-1] in order. SQL runs first, then Go (either may be
// empty); both run inside the same transaction as the user_version bump, so a
// failing step leaves the database at the previous version.
type Migration struct {
	SQL string
	Go  func(ctx context.Context, tx Tx) error
}

// Version returns the database's schema version (PRAGMA user_version).
func Version(ctx context.Context, db *sql.DB) (int, error) {
	var v int
	err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v)
	return v, err
}

// Apply runs every migration past the current user_version, one transaction
// per step, and returns how many steps ran. A database whose version is
// newer than len(migrations) was written by a newer build and is refused.
//
// Each step takes the database's write lock first (BEGIN IMMEDIATE) and
// reads user_version under it, so two processes migrating the same file at
// once never both run a step: the second waits, sees the step done and
// skips it.
func Apply(ctx context.Context, db *sql.DB, migrations []Migration) (int, error) {
	version, err := Version(ctx, db)
	if err != nil {
		return 0, err
	}
	if version > len(migrations) {
		return 0, newerSchema(version, len(migrations))
	}
	applied := 0
	for i := version; i < len(migrations); i++ {
		ran, err := applyOne(ctx, db, i, migrations)
		if err != nil {
			return applied, err
		}
		if ran {
			applied++
		}
	}
	return applied, nil
}

func newerSchema(version, supported int) error {
	return fmt.Errorf("database schema version %d is newer than this build supports (%d): it was written by a newer release; "+
		"run that release or a newer one (going back to an older release needs a backup taken before the upgrade)", version, supported)
}

// applyOne runs migrations[i] if the database is still at version i,
// reporting whether it ran.
func applyOne(ctx context.Context, db *sql.DB, i int, migrations []Migration) (bool, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return false, err
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if _, err := conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK"); err != nil {
			// A connection that may still be inside the transaction must
			// not go back to the pool.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	var version int
	if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return false, err
	}
	switch {
	case version > len(migrations):
		return false, newerSchema(version, len(migrations))
	case version > i:
		// Another process applied this step while we waited for the lock.
		return false, nil
	case version < i:
		return false, fmt.Errorf("migration %d: the database is at version %d", i+1, version)
	}
	m := migrations[i]
	if m.SQL != "" {
		if _, err := conn.ExecContext(ctx, m.SQL); err != nil {
			return false, fmt.Errorf("migration %d: %w", i+1, err)
		}
	}
	if m.Go != nil {
		if err := m.Go(ctx, conn); err != nil {
			return false, fmt.Errorf("migration %d: %w", i+1, err)
		}
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
		return false, err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return false, err
	}
	committed = true
	return true, nil
}

// Check reports whether the database is at exactly migrations, the schema
// version this build uses, without changing it. Tools that open a database
// another process may be serving (admin commands, backups) use it instead of
// migrating: a newer binary must never upgrade the schema under an older
// running node.
func Check(ctx context.Context, db *sql.DB, migrations int) error {
	v, err := Version(ctx, db)
	if err != nil {
		return err
	}
	if v != migrations {
		return fmt.Errorf("the database is at schema version %d and this build uses %d: "+
			"run this command with the node's own release (or restart the node with this release first)", v, migrations)
	}
	return nil
}

// Migrate applies SQL-only migrations (see Apply).
func Migrate(ctx context.Context, db *sql.DB, migrations []string) error {
	steps := make([]Migration, len(migrations))
	for i, m := range migrations {
		steps[i] = Migration{SQL: m}
	}
	_, err := Apply(ctx, db, steps)
	return err
}
