package community

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// The search index lives in memory and is rebuilt from the encrypted
// messages at every start (and when search is turned on). These measure what
// that costs for a large history (docs/load-tests.md "Search index rebuild",
// docs/operations.md "Search index") and check that the rebuild streams rows
// into the index instead of holding the whole history in memory first.

// searchServer opens an unclaimed server in a fresh data directory with
// search on and n stored messages in two channels; every 1000th message
// carries the word "zebrafinch" and every 7th is deleted.
func searchServer(tb testing.TB, n int) *Server {
	tb.Helper()
	ctx := context.Background()
	cfg := DefaultConfig(tb.TempDir())
	cfg.BackupInterval.Duration = 0
	cfg.Relays = []string{"ws://relay.invalid/ws"} // never dialled: the server isn't Run
	if _, err := Init(ctx, cfg); err != nil {
		tb.Fatal(err)
	}
	s, err := Open(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { s.Close() })
	s.mu.Lock()
	s.settings.SearchEnabled = true
	s.mu.Unlock()

	words := strings.Fields("the a quick brown fox jumps over lazy dog meeting tomorrow lunch plans photo " +
		"release build server channel role invite voice call weekend garden coffee music project update " +
		"question answer thanks hello later maybe sure great idea review deploy bug fix test")
	rng := rand.New(rand.NewSource(1))
	now := time.Now().UnixMilli()
	month := monthOf(now)
	if _, err := s.monthKey(ctx, month, true); err != nil {
		tb.Fatal(err)
	}
	const batch = 5000
	for start := 0; start < n; start += batch {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			tb.Fatal(err)
		}
		for i := start; i < min(start+batch, n); i++ {
			cid := int64(1 + i%2)
			created := now + int64(i)
			res, err := tx.ExecContext(ctx, `INSERT INTO messages (channel_id, created_at, month) VALUES (?, ?, ?)`, cid, created, month)
			if err != nil {
				tb.Fatal(err)
			}
			id, _ := res.LastInsertId()
			var text strings.Builder
			for w := 0; w < 12+rng.Intn(12); w++ {
				text.WriteString(words[rng.Intn(len(words))])
				text.WriteByte(' ')
			}
			if i%1000 == 0 {
				text.WriteString("zebrafinch")
			}
			body := &wire.Message{ChannelId: cid, CreatedAt: created, Author: []byte("author-key-placeholder-32-bytes!"), Text: text.String()}
			sealed, err := s.sealBody(ctx, id, month, body, false)
			if err != nil {
				tb.Fatal(err)
			}
			deleted := 0
			if i%7 == 3 {
				deleted = 1
			}
			if _, err := tx.ExecContext(ctx, `UPDATE messages SET body = ?, deleted = ? WHERE id = ?`, sealed, deleted, id); err != nil {
				tb.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			tb.Fatal(err)
		}
	}
	return s
}

// rebuildCost is one measured rebuild: wall time, bytes the Go heap
// allocated, the Go heap's peak above where it started, and the process's
// resident memory growth (which includes SQLite's own allocations, so the
// index itself; Linux only, else 0).
type rebuildCost struct {
	took              time.Duration
	allocated         uint64
	peakHeapAboveBase uint64
	rssGrowth         int64
}

func measureRebuild(tb testing.TB, s *Server) rebuildCost {
	tb.Helper()
	s.search.close()
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	rss0 := residentBytes()
	var peak atomic.Uint64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var m runtime.MemStats
		for {
			runtime.ReadMemStats(&m)
			if m.HeapInuse > peak.Load() {
				peak.Store(m.HeapInuse)
			}
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()
	start := time.Now()
	if err := s.rebuildSearch(context.Background()); err != nil {
		tb.Fatal(err)
	}
	took := time.Since(start)
	close(stop)
	wg.Wait()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	c := rebuildCost{took: took, allocated: after.TotalAlloc - before.TotalAlloc, rssGrowth: residentBytes() - rss0}
	if p := peak.Load(); p > before.HeapInuse {
		c.peakHeapAboveBase = p - before.HeapInuse
	}
	return c
}

// residentBytes is the process's resident set size, from /proc (Linux).
func residentBytes() int64 {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return 0
	}
	pages, _ := strconv.ParseInt(f[1], 10, 64)
	return pages * int64(os.Getpagesize())
}

func searchCount(tb testing.TB, s *Server, q string) int {
	tb.Helper()
	s.search.mu.Lock()
	defer s.search.mu.Unlock()
	var n int
	if err := s.search.db.QueryRow(`SELECT count(*) FROM idx WHERE idx MATCH ?`, ftsQuery(q)).Scan(&n); err != nil {
		tb.Fatal(err)
	}
	return n
}

// TestSearchRebuildStreamsEveryMessage rebuilds the index over more rows
// than one index transaction takes and checks that every undeleted message,
// and no deleted one, is indexed. OPENARCANUM_SEARCH_REBUILD_N sets the
// message count (default 6000, so `go test ./...` stays quick); the cost is
// logged with -v.
func TestSearchRebuildStreamsEveryMessage(t *testing.T) {
	n := 6000
	if v := os.Getenv("OPENARCANUM_SEARCH_REBUILD_N"); v != "" {
		var err error
		if n, err = strconv.Atoi(v); err != nil || n <= 0 {
			t.Fatalf("OPENARCANUM_SEARCH_REBUILD_N=%q", v)
		}
	}
	s := searchServer(t, n)
	c := measureRebuild(t, s)
	want := 0
	for i := 0; i < n; i++ {
		if i%7 != 3 {
			want++
		}
	}
	s.search.mu.Lock()
	var indexed int
	if err := s.search.db.QueryRow(`SELECT count(*) FROM idx`).Scan(&indexed); err != nil {
		t.Fatal(err)
	}
	s.search.mu.Unlock()
	if indexed != want {
		t.Fatalf("indexed %d messages, want %d (of %d stored)", indexed, want, n)
	}
	finches := 0
	for i := 0; i < n; i += 1000 {
		if i%7 != 3 {
			finches++
		}
	}
	if got := searchCount(t, s, "zebrafinch"); got != finches {
		t.Fatalf("search found %d, want %d", got, finches)
	}
	t.Logf("search index rebuild: %d messages (%d indexed) in %v; Go heap allocated %.1f MiB, Go heap peak +%.1f MiB, RSS +%.1f MiB",
		n, indexed, c.took.Round(time.Millisecond), mib(c.allocated), mib(c.peakHeapAboveBase), float64(c.rssGrowth)/(1<<20))
}

func mib(b uint64) float64 { return float64(b) / (1 << 20) }

// BenchmarkSearchRebuild times one rebuild for several history sizes
// (go test -run '^$' -bench SearchRebuild ./internal/community/), reporting
// Go heap peak and resident growth per rebuild alongside allocations.
func BenchmarkSearchRebuild(b *testing.B) {
	for _, n := range []int{10_000, 50_000, 200_000} {
		b.Run(fmt.Sprintf("messages=%d", n), func(b *testing.B) {
			s := searchServer(b, n)
			b.ReportAllocs()
			b.ResetTimer()
			var peak, rss float64
			for i := 0; i < b.N; i++ {
				c := measureRebuild(b, s)
				peak = max(peak, mib(c.peakHeapAboveBase))
				rss = max(rss, float64(c.rssGrowth)/(1<<20))
			}
			b.ReportMetric(peak, "heap-peak-MiB")
			b.ReportMetric(rss, "rss-growth-MiB")
		})
	}
}
