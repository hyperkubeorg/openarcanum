package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMigrateIsIncremental(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "s.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	m := []string{`CREATE TABLE a (x INTEGER)`}
	if err := Migrate(ctx, db, m); err != nil {
		t.Fatal(err)
	}
	m = append(m, `CREATE TABLE b (y INTEGER)`)
	if err := Migrate(ctx, db, m); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db, m); err != nil {
		t.Fatal("re-running migrations failed:", err)
	}
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != 2 {
		t.Fatalf("user_version = %d, %v", v, err)
	}
	db.Close()
}

func TestApplyRunsGoStepsInOrder(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	steps := []Migration{
		{SQL: `CREATE TABLE a (x TEXT)`},
		{SQL: `INSERT INTO a VALUES ('old')`, Go: func(ctx context.Context, tx Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE a SET x = 'converted'`)
			return err
		}},
	}
	n, err := Apply(ctx, db, steps)
	if err != nil || n != 2 {
		t.Fatalf("applied %d, %v", n, err)
	}
	var x string
	if err := db.QueryRow(`SELECT x FROM a`).Scan(&x); err != nil || x != "converted" {
		t.Fatalf("x = %q, %v", x, err)
	}
	if n, err := Apply(ctx, db, steps); err != nil || n != 0 {
		t.Fatalf("re-apply ran %d steps, %v", n, err)
	}
}

// A failing step rolls back its SQL and leaves the version where it was.
func TestApplyFailedStepRollsBack(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	steps := []Migration{
		{SQL: `CREATE TABLE a (x INTEGER)`},
		{SQL: `CREATE TABLE b (y INTEGER)`, Go: func(context.Context, Tx) error { return errors.New("boom") }},
	}
	if _, err := Apply(ctx, db, steps); err == nil || !strings.Contains(err.Error(), "migration 2: boom") {
		t.Fatalf("err = %v", err)
	}
	if v, _ := Version(ctx, db); v != 1 {
		t.Fatalf("version = %d after failed step", v)
	}
	if _, err := db.Exec(`SELECT * FROM b`); err == nil {
		t.Fatal("table from the failed step exists")
	}
}

func TestApplyRefusesNewerSchema(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA user_version = 5`); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(ctx, db, []Migration{{SQL: `CREATE TABLE a (x INTEGER)`}}); err == nil || !strings.Contains(err.Error(), "newer than this build") {
		t.Fatalf("err = %v", err)
	}
}

// FTS5 is what community and directory search are built on.
func TestFTS5Available(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE VIRTUAL TABLE f USING fts5(body); INSERT INTO f VALUES ('hello world'), ('other')`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM f WHERE f MATCH 'hello'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("fts5 match count = %d, %v", n, err)
	}
}

// A database file (and so its WAL) is created readable by its owner only.
func TestOpenCreatesPrivateFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes")
	}
	path := filepath.Join(t.TempDir(), "s.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE a (x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{path, path + "-wal"} {
		st, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		if m := st.Mode().Perm(); m != 0o600 {
			t.Fatalf("%s has mode %o", f, m)
		}
	}
}
