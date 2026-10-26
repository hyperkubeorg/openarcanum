package relay

import (
	"testing"
	"time"
)

// A larger burst fills at the rate, it isn't credited at once: a host
// whose streams come and go (each resizing its budget) earns nothing by
// it. A smaller burst drops the tokens above it.
func TestBucketSetMintsNothing(t *testing.T) {
	b := newBucket(1, 10)
	if b.take(10) != 0 {
		t.Fatal("a full bucket refused its burst")
	}
	for range 5 {
		b.set(1, 100)
		b.set(1, 10)
	}
	b.set(1, 100)
	if wait := b.take(1); wait < 500*time.Millisecond {
		t.Fatalf("tokens minted by resizing: a take after an empty bucket waits %v", wait)
	}
	b = newBucket(1, 10)
	b.set(1, 2)
	if wait := b.take(3); wait < 500*time.Millisecond {
		t.Fatalf("tokens kept above a smaller burst: waited %v", wait)
	}
}
