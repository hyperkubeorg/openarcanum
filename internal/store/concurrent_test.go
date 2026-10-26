package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Two processes migrating the same file at once never both run a step: each
// step reads the version under the write lock (BEGIN IMMEDIATE), so the
// second waits for the first, sees the step done and skips it. A Go data
// step that ran twice would count 2 here.
func TestConcurrentApplyRunsEachStepOnce(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "s.db")
	setup, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	base := []Migration{{SQL: `CREATE TABLE runs (n INTEGER NOT NULL); INSERT INTO runs VALUES (0)`}}
	if _, err := Apply(ctx, setup, base); err != nil {
		t.Fatal(err)
	}
	setup.Close()
	steps := append(base, Migration{Go: func(ctx context.Context, tx Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET n = n + 1`); err != nil {
			return err
		}
		// Hold the step open so the other processes start meanwhile.
		time.Sleep(200 * time.Millisecond)
		return nil
	}})
	const procs = 4
	dbs := make([]*sql.DB, procs)
	for i := range dbs {
		if dbs[i], err = Open(path); err != nil {
			t.Fatal(err)
		}
		defer dbs[i].Close()
	}
	var wg sync.WaitGroup
	ran := make([]int, procs)
	errs := make([]error, procs)
	for i := range dbs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ran[i], errs[i] = Apply(ctx, dbs[i], steps)
		}()
	}
	wg.Wait()
	total := 0
	for i := range dbs {
		if errs[i] != nil {
			t.Fatalf("process %d: %v", i, errs[i])
		}
		total += ran[i]
	}
	var n int
	if err := dbs[0].QueryRow(`SELECT n FROM runs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 || total != 1 {
		t.Fatalf("the data step ran %d times (Apply reported %d)", n, total)
	}
	if v, _ := Version(ctx, dbs[0]); v != 2 {
		t.Fatalf("version = %d", v)
	}
}

// Check refuses a database at any version but this build's, and changes
// nothing (admin tools use it instead of migrating a live database).
func TestCheckDoesNotMigrate(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	steps := []string{`CREATE TABLE a (x INTEGER)`, `CREATE TABLE b (y INTEGER)`}
	if err := Migrate(ctx, db, steps[:1]); err != nil {
		t.Fatal(err)
	}
	if err := Check(ctx, db, len(steps)); err == nil || !strings.Contains(err.Error(), "schema version 1") {
		t.Fatalf("older schema: %v", err)
	}
	if v, _ := Version(ctx, db); v != 1 {
		t.Fatalf("Check changed the version to %d", v)
	}
	if err := Check(ctx, db, 1); err != nil {
		t.Fatal(err)
	}
	if err := Check(ctx, db, 0); err == nil {
		t.Fatal("newer schema accepted")
	}
}
