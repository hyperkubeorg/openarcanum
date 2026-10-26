package community

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A search holding the index (a slow query) must not stall posting: posts,
// edits and deletes update the index while holding the server's model lock,
// so waiting for the search would queue every other request behind it.
func TestSearchDoesNotBlockIndexing(t *testing.T) {
	s := searchServer(t, 100)
	if err := s.rebuildSearch(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.search.mu.Lock() // a search in progress
	done := make(chan struct{})
	go func() {
		s.mu.Lock()
		s.indexMessage(1_000_001, 1, "a fresh capybara remark", nil)
		s.unindexMessage(1_000_002)
		s.unindexChannel(999)
		s.mu.Unlock()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		s.search.mu.Unlock()
		t.Fatal("indexing waited for a search holding the index")
	}
	s.search.unlock()
	// The queued change is visible to the next search.
	if n := searchCount(t, s, "capybara"); n != 1 {
		t.Fatalf("found %d, want 1", n)
	}
	// And with the index free, a change lands at once.
	s.indexMessage(1_000_003, 1, "a second capybara remark", nil)
	s.search.mu.Lock()
	var n int
	if err := s.search.db.QueryRow(`SELECT count(*) FROM idx WHERE idx MATCH '"capybara"'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	s.search.mu.Unlock()
	if n != 2 {
		t.Fatalf("indexed %d, want 2", n)
	}
}

// Short words don't expand into prefix scans, queries have few terms and a
// bounded length, and a search that runs too long is cut off.
func TestSearchQueryBounds(t *testing.T) {
	q := ftsQuery("a a a a a a a a")
	if strings.Contains(q, "*") || strings.Count(q, `"a"`) != maxSearchTerms {
		t.Fatalf("query %q", q)
	}
	if q := ftsQuery("zebrafinch ab"); q != `"zebrafinch"* "ab"` {
		t.Fatalf("query %q", q)
	}
	if q := ftsQuery(strings.Repeat("é", 100)); len(q) > maxSearchTerm+3 || !strings.HasSuffix(q, `"*`) {
		t.Fatalf("long word not trimmed: %q", q)
	}

	s := searchServer(t, 2000)
	if err := s.rebuildSearch(context.Background()); err != nil {
		t.Fatal(err)
	}
	old := searchTimeout
	searchTimeout = time.Nanosecond
	t.Cleanup(func() { searchTimeout = old })
	_, err := s.search.query(context.Background(), ftsQuery("the quick brown"), map[int64]bool{1: true, 2: true}, 50)
	if err == nil || !strings.Contains(err.Error(), "took too long") {
		t.Fatalf("a search past the deadline: %v", err)
	}
}
