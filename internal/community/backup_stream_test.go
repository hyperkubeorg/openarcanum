package community_test

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
)

// peakHeap runs f and reports how far the live heap rose above where it
// started, sampled every few milliseconds, with the garbage collector set
// to run often so the figure tracks what f holds, not garbage awaiting
// collection.
func peakHeap(f func()) uint64 {
	defer debug.SetGCPercent(debug.SetGCPercent(10))
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	base, peak := m.HeapAlloc, m.HeapAlloc
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(2 * time.Millisecond)
		defer t.Stop()
		for {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			peak = max(peak, m.HeapAlloc)
			select {
			case <-stop:
				return
			case <-t.C:
			}
		}
	}()
	f()
	close(stop)
	<-done
	return peak - base
}

// A backup of a large database is written and restored in bounded memory
// (the snapshot streams through age's STREAM encryption instead of being
// read and sealed whole), and the server keeps serving while it runs: the
// backup doesn't hold the server's lock.
func TestBackupStreamsAndKeepsServing(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	gen := e.owner.Channel("general")
	db := e.comm.Server.DB()
	if _, err := db.Exec(`CREATE TABLE pad (b BLOB NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	const mib, size = 1 << 20, 48
	chunk := make([]byte, mib)
	for range size {
		rand.Read(chunk)
		if _, err := db.Exec(`INSERT INTO pad (b) VALUES (?)`, chunk); err != nil {
			t.Fatal(err)
		}
	}
	served := make(chan error, 1)
	e.comm.Server.SetBackupHook(func() {
		if !e.comm.Server.TryLockServer() {
			served <- errors.New("the server's lock is held during the backup")
			return
		}
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		_, err := e.owner.Post(pctx, gen.Id, "posted while the backup runs")
		served <- err
	})
	var path string
	var err error
	held := peakHeap(func() { path, err = e.comm.Server.Backup(ctx) })
	if err != nil {
		t.Fatal(err)
	}
	if err := <-served; err != nil {
		t.Fatalf("serving during the backup: %v", err)
	}
	t.Logf("backup of a %d MiB database: heap rose %d KiB", size, held>>10)
	if held > 16*mib {
		t.Fatalf("backup of a %d MiB database held %d MiB of heap", size, held/mib)
	}
	if st, _ := os.Stat(path); st.Size() < size*mib {
		t.Fatalf("backup is only %d bytes", st.Size())
	}

	dir := t.TempDir()
	cfg := community.DefaultConfig(dir)
	cfg.Relays = []string{e.relay.URL}
	held = peakHeap(func() { _, err = community.Restore(ctx, cfg, path, filepath.Join(e.comm.Dir, "storage.key")) })
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("restore of a %d MiB database: heap rose %d KiB", size, held>>10)
	if held > 16*mib {
		t.Fatalf("restore of a %d MiB database held %d MiB of heap", size, held/mib)
	}
	srv := reopen(t, dir, e.relay.URL, testutil.Quiet())
	var n, total int64
	if err := srv.DB().QueryRow(`SELECT count(*), sum(length(b)) FROM pad`).Scan(&n, &total); err != nil || n != size || total != size*mib {
		t.Fatalf("restored pad: %d rows, %d bytes, %v", n, total, err)
	}
}

// A restore that fails part way leaves the data directory as it was (here:
// the backup is of an unclaimed server, found only after decrypting and
// opening it), so it can be run again; and a restore never replaces an
// existing storage key file that holds another key.
func TestRestoreIsStagedAndKeepsStorageKey(t *testing.T) {
	ctx := testutil.Ctx(t)
	e := setup(t, nil)
	good, err := e.comm.Server.Backup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(e.comm.Dir, "storage.key")

	key, _ := os.ReadFile(keyPath)
	// An unclaimed server's backup.
	udir := t.TempDir()
	ucfg := community.DefaultConfig(udir)
	ucfg.Relays = []string{e.relay.URL}
	ucfg.BackupInterval.Duration = 0
	if _, err := community.Init(ctx, ucfg); err != nil {
		t.Fatal(err)
	}
	usrv, err := community.Open(ctx, ucfg, testutil.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	unclaimed, err := usrv.Backup(ctx)
	usrv.Close()
	if err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(t.TempDir(), "new")
	cfg := community.DefaultConfig(dir)
	cfg.Relays = []string{e.relay.URL}
	cfg.StorageKeyFile = filepath.Join(t.TempDir(), "secrets", "storage.key")
	if _, err := community.Restore(ctx, cfg, unclaimed, filepath.Join(udir, "storage.key")); err == nil || !strings.Contains(err.Error(), "unclaimed") {
		t.Fatalf("restore of an unclaimed server's backup: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	for _, en := range entries {
		t.Errorf("a failed restore left %s behind", en.Name())
	}
	if _, err := os.Stat(cfg.StorageKeyFile); !os.IsNotExist(err) {
		t.Errorf("a failed restore wrote the storage key: %v", err)
	}

	// A storage key file holding another key is never replaced.
	if err := os.MkdirAll(filepath.Dir(cfg.StorageKeyFile), 0o700); err != nil {
		t.Fatal(err)
	}
	writeKey(t, cfg.StorageKeyFile)
	other, _ := os.ReadFile(cfg.StorageKeyFile)
	if _, err := community.Restore(ctx, cfg, good, keyPath); err == nil || !strings.Contains(err.Error(), "different storage key") {
		t.Fatalf("restore over another storage key: %v", err)
	}
	if now, _ := os.ReadFile(cfg.StorageKeyFile); string(now) != string(other) {
		t.Fatal("the existing storage key was replaced")
	}
	if _, err := os.Stat(filepath.Join(dir, "community.db")); !os.IsNotExist(err) {
		t.Fatal("a refused restore left a database")
	}

	// With the key file out of the way, the same restore succeeds; the key
	// is written where the config says.
	if err := os.Remove(cfg.StorageKeyFile); err != nil {
		t.Fatal(err)
	}
	if _, err := community.Restore(ctx, cfg, good, keyPath); err != nil {
		t.Fatalf("restore after a failed one: %v", err)
	}
	if got, _ := os.ReadFile(cfg.StorageKeyFile); strings.TrimSpace(string(got)) != strings.TrimSpace(string(key)) {
		t.Fatal("the storage key wasn't written")
	}
	for _, f := range []string{"community.db", "server.key", "noise.key"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("restored data directory lacks %s", f)
		}
	}
}
