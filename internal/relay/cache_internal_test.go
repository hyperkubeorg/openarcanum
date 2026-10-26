package relay

import (
	"fmt"
	"runtime"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

func heapInUse() uint64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// A million cacheable messages that are empty or a byte long. Empty ones
// aren't cached at all; one-byte ones are
// charged their real cost, so the entry count and the heap the cache holds
// stay within chunk_cache_bytes.
func TestChunkCacheTinyMessagesBounded(t *testing.T) {
	const max = 1 << 20
	cc := newChunkCache(max, max, max, time.Minute)
	now := time.Now()
	for i := range 1_000_000 {
		cc.put("t", 1, []*wire.Deliver{{Topic: []byte("t"), MessageId: uint64(i), Final: true}}, 0, now)
	}
	if cc.entries() != 0 || cc.Bytes() != 0 {
		t.Fatalf("empty messages cached: %d entries, %d bytes", cc.entries(), cc.Bytes())
	}
	before := heapInUse()
	topic := "a topic name that takes up some room"
	for i := range 1_000_000 {
		cc.put(topic, 1, []*wire.Deliver{{Topic: []byte(topic), MessageId: uint64(i), Final: true, Data: []byte{1}}}, 1, now)
	}
	limit := max / (cacheEntryOverhead + cacheChunkOverhead)
	if n := cc.entries(); n == 0 || n > limit {
		t.Fatalf("%d one-byte messages cached, bound %d", n, limit)
	}
	if cc.Bytes() > max {
		t.Fatalf("cache charged %d bytes, over %d", cc.Bytes(), max)
	}
	if grew := int64(heapInUse()) - int64(before); grew > 2*max {
		t.Fatalf("cache holds %d bytes of heap for a %d-byte budget", grew, max)
	}
	runtime.KeepAlive(cc)
}

// Evicting from a topic with very many entries is constant time: filling a
// topic far past its bound takes about as long per put as filling a small
// one (a linear scan per eviction made this quadratic).
func TestChunkCacheEvictionIsConstantTime(t *testing.T) {
	run := func(n int) time.Duration {
		cc := newChunkCache(1<<30, 1<<30, 1<<30, time.Hour)
		// Fill to the bound, then measure puts that each evict one entry.
		perEntry := cacheCharge("t", 1, 1)
		cc.perTopic = uint64(n) * perEntry
		now := time.Now()
		d := []*wire.Deliver{{Data: []byte{1}}}
		for range n {
			cc.put("t", 1, d, 1, now)
		}
		start := time.Now()
		for range 20_000 {
			cc.put("t", 1, d, 1, now)
		}
		return time.Since(start)
	}
	small, large := run(1_000), run(200_000)
	if large > 10*small+50*time.Millisecond {
		t.Fatalf("20k evicting puts: %v with 1k entries, %v with 200k", small, large)
	}
}

// One publishing connection gets at most its share of the cache; it can't
// push everyone else's messages out.
func TestChunkCachePerConnectionShare(t *testing.T) {
	cc := newChunkCache(64<<10, 64<<10, 16<<10, time.Minute)
	now := time.Now()
	d := []*wire.Deliver{{Data: make([]byte, 1000)}}
	cc.put("legit", 2, d, 1000, now)
	for i := range 1000 {
		cc.put(fmt.Sprint("spam", i), 1, d, 1000, now)
	}
	if got := cc.since("legit", time.Minute, now); len(got) != 1 {
		t.Fatal("another connection's messages pushed a message out")
	}
	if n := cc.conns[1].size; n > 16<<10 || n == 0 {
		t.Fatalf("one connection holds %d bytes", n)
	}
	// Its newest messages are the ones kept.
	if got := cc.since("spam999", time.Minute, now); len(got) != 1 {
		t.Fatal("the publisher's newest message wasn't kept")
	}
}

// Replay frames are serialized once when cached and shared by every
// subscriber's queue, so subscriptions with replay don't multiply cache
// memory per subscriber.
func TestReplayFramesSharedBySubscribers(t *testing.T) {
	now := time.Now()
	cc := newChunkCache(1<<20, 1<<20, 1<<20, time.Minute)
	cc.put("t", 1, []*wire.Deliver{{Topic: []byte("t"), MessageId: 1, Final: true, Data: make([]byte, 1000)}}, 1000, now)
	a, b := cc.since("t", time.Minute, now), cc.since("t", time.Minute, now)
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("got %d and %d frames", len(a), len(b))
	}
	if &a[0][0] != &b[0][0] {
		t.Fatal("each replay got its own copy of the cached frame")
	}
	var f wire.RelayFrame
	if err := proto.Unmarshal(a[0], &f); err != nil || !f.GetDeliver().GetReplayed() || len(f.GetDeliver().Data) != 1000 {
		t.Fatalf("replay frame %v (%v)", &f, err)
	}
	// A thousand replays cost slice headers, not copies of the payload.
	before := heapInUse()
	keep := make([][][]byte, 0, 1000)
	for range 1000 {
		keep = append(keep, cc.since("t", time.Minute, now))
	}
	if grown := heapInUse() - before; grown > 200<<10 {
		t.Fatalf("1000 replays of a 1000-byte message grew the heap by %d bytes", grown)
	}
	runtime.KeepAlive(keep)
}

// What a connection's unfinished cacheable messages hold is bounded by its
// share of the cache and the cache's TTL, and a message the relay refuses
// leaves nothing behind.
func TestChunkCachePendingBounded(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ChunkCacheBytes, cfg.ChunkCacheTopicBytes, cfg.ChunkCacheConnBytes = 64<<10, 16<<10, 4<<10
	cfg.ChunkCacheMaxMessage, cfg.MaxChunk, cfg.MaxPartial = 2<<10, 1024, 8
	cfg.ChunkCacheTTL.Duration = time.Second
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	s := New(cfg, nil)
	c := &conn{srv: s, partial: map[uint64]inflight{}, pendingCache: map[uint64]*cachePending{}}
	chunk := func(id uint64, seq uint32, final bool, n int) {
		m := &wire.Publish{Topic: []byte("t"), MessageId: id, Seq: seq, Final: final, Cacheable: true, Data: make([]byte, n)}
		c.cacheChunk(m, &wire.Deliver{Topic: m.Topic, MessageId: id, Seq: seq, Final: final, Data: m.Data})
	}
	// Eight messages of a kilobyte each started: past the connection's
	// share the rest hold no chunks.
	for id := uint64(1); id <= 8; id++ {
		chunk(id, 0, false, 1024)
	}
	if n := c.pendingBytes(); n > 4<<10 {
		t.Fatalf("pending messages hold %d bytes, share is %d", n, 4<<10)
	}
	// Finished, the ones within the share are cached, the others not.
	for id := uint64(1); id <= 8; id++ {
		chunk(id, 1, true, 1)
	}
	if n := s.cache.entries(); n == 0 || n > 4 {
		t.Fatalf("%d messages cached", n)
	}
	if len(c.pendingCache) != 0 || c.pendingBytes() != 0 {
		t.Fatal("finished messages still pending")
	}
	// A refused message (too large, another topic) is dropped.
	chunk(9, 0, false, 1024)
	c.reject(&wire.Publish{MessageId: 9})
	if len(c.pendingCache) != 0 {
		t.Fatal("refused message still pending")
	}
	// Left unfinished for the TTL, a message is dropped when another
	// starts.
	chunk(10, 0, false, 1024)
	c.pendingCache[10].at = time.Now().Add(-2 * time.Second)
	chunk(11, 0, false, 1024)
	if _, ok := c.pendingCache[10]; ok || len(c.pendingCache) != 1 {
		t.Fatalf("stale pending message kept: %d pending", len(c.pendingCache))
	}
}
