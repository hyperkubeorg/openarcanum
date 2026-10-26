package relay

import (
	"container/list"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/fanout"
	"github.com/hyperkubeorg/openarcanum/internal/pow"
	"github.com/hyperkubeorg/openarcanum/internal/version"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// ---------------------------------------------------------------- load

// arrivals counts new connections over the last second (two one-second
// buckets, weighted), so "under load" reacts to a burst within a second.
type arrivals struct {
	mu        sync.Mutex
	sec       int64
	cur, prev int
}

func (a *arrivals) add(now time.Time) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.roll(now)
	a.cur++
	return a.rateLocked(now)
}

func (a *arrivals) roll(now time.Time) {
	sec := now.Unix()
	switch sec {
	case a.sec:
	case a.sec + 1:
		a.prev, a.cur, a.sec = a.cur, 0, sec
	default:
		a.prev, a.cur, a.sec = 0, 0, sec
	}
}

// rateLocked estimates arrivals in the trailing second.
func (a *arrivals) rateLocked(now time.Time) int {
	frac := float64(now.UnixNano()%int64(time.Second)) / float64(time.Second)
	return a.cur + int(float64(a.prev)*(1-frac))
}

// powBits decides the proof-of-work for a connection arriving now: pow_bits
// when pow_mode is "always", or when it is "load" and the relay is under
// load (open connections at pow_load_connections, or new connections per
// second at pow_new_per_second) or the connection's network already holds
// pow_ip_connections open connections (fromNet); otherwise none.
func (s *Server) powBits(open, perSecond, fromNet int) uint32 {
	switch s.cfg.PowMode {
	case "always":
		return s.cfg.PowBits
	case "load":
		if open >= s.cfg.powLoadConnections() || perSecond >= s.cfg.PowNewPerSecond || fromNet >= s.cfg.PowIPConnections {
			return s.cfg.PowBits
		}
	}
	return 0
}

// SetVersions replaces the protocol versions the relay speaks (tests prove
// that a relay one version ahead still serves current clients).
func (s *Server) SetVersions(vs []uint32) { s.versions = vs }

// ---------------------------------------------------------------- start

// start handles the client's Start: a version from Hello.versions and, when
// asked for, a proof-of-work over this connection's nonce. Anything wrong
// ends the connection after an Error saying why.
func (c *conn) start(m *wire.Start) bool {
	if !containsVersion(c.srv.versions, m.Version) {
		_, err := version.Negotiate(c.srv.versions, []uint32{m.Version})
		c.sendError(m.Req, wire.ErrorCode_ERROR_CODE_UNSUPPORTED_VERSION, "relay: "+err.Error())
		return false
	}
	if !pow.Verify(c.nonce, m.Pow, c.powBits) {
		c.srv.stats.PowRefused.Add(1)
		c.sendError(m.Req, wire.ErrorCode_ERROR_CODE_POW_REQUIRED, "proof-of-work missing or wrong")
		return false
	}
	c.srv.mu.Lock()
	if !c.holdsUnstarted {
		// Evicted while its Start was on the way: the connection is
		// closing.
		c.srv.mu.Unlock()
		return false
	}
	c.srv.dropUnstartedLocked(c)
	startTimer := c.startTimer
	c.version = m.Version
	c.started.Store(true)
	c.srv.mu.Unlock()
	if startTimer != nil {
		startTimer.Stop()
	}
	go c.pinger()
	c.ack(m.Req)
	return true
}

// startWindow is how long a new connection has to send Start: start_window,
// or start_window_under_load while the relay is under load (it asks for a
// proof-of-work) or half its unstarted slots are taken, so slots held by
// connections that never start turn over quickly when they matter. The
// shorter window still leaves a slow device time to solve the puzzle
// (slowSolverRate hashes per second), and is never longer than
// start_window.
func (s *Server) startWindow(powBits uint32, unstarted int) time.Duration {
	if powBits > 0 || unstarted >= s.cfg.maxUnstarted()/2 {
		solve := time.Duration(float64(uint64(1)<<powBits) / slowSolverRate * float64(time.Second))
		return min(s.cfg.StartWindow.Duration, max(s.cfg.StartWindowUnderLoad.Duration, solve))
	}
	return s.cfg.StartWindow.Duration
}

// slowSolverRate is the proof-of-work speed (hashes per second) the
// shortened start window allows for: a slow device running the Python
// client. The default 18 bits then take about 5 s.
const slowSolverRate = 50_000

func containsVersion(vs []uint32, v uint32) bool {
	for _, x := range vs {
		if x == v && v != 0 {
			return true
		}
	}
	return false
}

// armStartTimer closes the connection if it hasn't started within its
// window, so an unstarted connection (or an unsolved proof-of-work) can't
// be held. A timer, not a goroutine, so unstarted connections stay cheap.
func (c *conn) armStartTimer() {
	t := time.AfterFunc(c.startWindow, func() {
		if c.started.Load() || c.ctx.Err() != nil {
			return
		}
		c.srv.stats.StartTimeouts.Add(1)
		c.sendError(0, wire.ErrorCode_ERROR_CODE_POW_REQUIRED, "Start not received in time")
		c.closeAfterFlush("start window passed")
	})
	c.srv.mu.Lock()
	c.startTimer = t
	c.srv.mu.Unlock()
}

// closeAfterFlush gives the writer a moment to send a final Error.
func (c *conn) closeAfterFlush(reason string) {
	go func() {
		deadline := time.Now().Add(time.Second)
		for c.queued.Load() > 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		c.close(4000, reason, nil)
	}()
}

// ---------------------------------------------------------------- fan-out tokens

// maxIssuers bounds the fan-out tokens one connection holds.
const maxIssuers = 8

func (c *conn) fanoutAuth(m *wire.FanoutAuth) {
	issuer, exp, err := fanout.Verify(m.Token, c.local(), time.Now())
	if err != nil {
		c.sendError(m.Req, wire.ErrorCode_ERROR_CODE_BAD_TOKEN, err.Error())
		return
	}
	c.fmu.Lock()
	now := time.Now()
	for k, e := range c.issuers {
		if !now.Before(e) {
			delete(c.issuers, k)
		}
	}
	if _, ok := c.issuers[string(issuer)]; !ok && len(c.issuers) >= maxIssuers {
		c.fmu.Unlock()
		c.sendError(m.Req, wire.ErrorCode_ERROR_CODE_LIMIT, "too many fan-out tokens on one connection")
		return
	}
	c.issuers[string(issuer)] = exp
	c.fmu.Unlock()
	c.ack(m.Req)
}

// mayFanOut says whether this connection holds a live token from a key the
// topic commits to.
func (c *conn) mayFanOut(topic []byte) bool {
	c.fmu.Lock()
	defer c.fmu.Unlock()
	now := time.Now()
	for k, exp := range c.issuers {
		if now.Before(exp) && fanout.Commits(topic, []byte(k)) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- chunk cache

// chunkCache keeps recent cacheable messages per topic, in memory only, for
// subscribers that ask for a replay. Bounded in total bytes, per topic, per
// publishing connection and per message; entries expire after the TTL.
// Only whole, non-empty messages a publisher marked cacheable (end-to-end
// encrypted payloads) are kept; streams and call media never pass through
// it.
//
// Every entry is charged what it costs in memory, not only its payload:
// cacheEntryOverhead, cacheChunkOverhead per chunk, and the topic name. So
// tiny messages can't hold more entries than the byte bounds allow, and the
// number of entries is bounded too (at most max / cacheEntryOverhead).
// Each entry sits in three age-ordered lists (the cache's, its topic's and
// its publisher's), so any eviction or expiry is constant time and every put
// amortized constant time.
type chunkCache struct {
	mu       sync.Mutex
	max      uint64
	perTopic uint64
	perConn  uint64
	ttl      time.Duration
	size     uint64
	topics   map[string]*topicCache
	conns    map[uint64]*topicCache // per publishing connection
	order    *list.List             // *cacheEntry, oldest first, across topics
}

const (
	// cacheEntryOverhead is charged per cached message: the entry, its
	// list elements and its share of the topic's bookkeeping.
	cacheEntryOverhead = 256
	// cacheChunkOverhead is charged per chunk: the Deliver message and
	// slice headers around its payload.
	cacheChunkOverhead = 256
)

// cacheCharge is what a message of chunks chunks and data payload bytes on
// topic costs the cache.
func cacheCharge(topic string, chunks int, data uint64) uint64 {
	return data + uint64(chunks)*cacheChunkOverhead + uint64(len(topic)) + cacheEntryOverhead
}

// topicCache is one topic's (or one publishing connection's) entries.
type topicCache struct {
	size    uint64
	entries *list.List // *cacheEntry, oldest first
}

type cacheEntry struct {
	topic     string
	conn      uint64
	at        time.Time
	size      uint64
	frames    [][]byte      // RelayFrame{Deliver{replayed}}, serialized once
	elem      *list.Element // in chunkCache.order
	topicElem *list.Element // in its topic's entries
	connElem  *list.Element // in its publisher's entries
}

func newChunkCache(max, perTopic, perConn uint64, ttl time.Duration) *chunkCache {
	if max == 0 {
		return nil
	}
	return &chunkCache{max: max, perTopic: perTopic, perConn: perConn, ttl: ttl,
		topics: map[string]*topicCache{}, conns: map[uint64]*topicCache{}, order: list.New()}
}

// put stores one complete message published by connection conn, evicting
// that connection's oldest messages while it would hold more than its
// share (so one publisher can't push everyone else's messages out), then
// the topic's oldest, then the cache's oldest, until it fits.
func (cc *chunkCache) put(topic string, conn uint64, chunks []*wire.Deliver, data uint64, now time.Time) {
	if cc == nil || data == 0 || len(chunks) == 0 {
		return
	}
	size := cacheCharge(topic, len(chunks), data)
	if size > cc.perTopic || size > cc.max || size > cc.perConn {
		return
	}
	cc.mu.Lock()
	defer cc.mu.Unlock()
	cc.expireLocked(now)
	for pc := cc.conns[conn]; pc != nil && pc.size+size > cc.perConn; pc = cc.conns[conn] {
		cc.dropLocked(pc.entries.Front().Value.(*cacheEntry))
	}
	for tc := cc.topics[topic]; tc != nil && tc.size+size > cc.perTopic; tc = cc.topics[topic] {
		cc.dropLocked(tc.entries.Front().Value.(*cacheEntry))
	}
	for cc.size+size > cc.max {
		cc.dropLocked(cc.order.Front().Value.(*cacheEntry))
	}
	tc := cc.topics[topic]
	if tc == nil {
		tc = &topicCache{entries: list.New()}
		cc.topics[topic] = tc
	}
	pc := cc.conns[conn]
	if pc == nil {
		pc = &topicCache{entries: list.New()}
		cc.conns[conn] = pc
	}
	frames := make([][]byte, 0, len(chunks))
	for _, d := range chunks {
		r := proto.Clone(d).(*wire.Deliver)
		r.Replayed = true
		b, err := proto.Marshal(&wire.RelayFrame{Kind: &wire.RelayFrame_Deliver{Deliver: r}})
		if err != nil {
			return
		}
		frames = append(frames, b)
	}
	e := &cacheEntry{topic: topic, conn: conn, at: now, size: size, frames: frames}
	e.elem = cc.order.PushBack(e)
	e.topicElem = tc.entries.PushBack(e)
	e.connElem = pc.entries.PushBack(e)
	tc.size += size
	pc.size += size
	cc.size += size
}

func (cc *chunkCache) dropLocked(e *cacheEntry) {
	cc.order.Remove(e.elem)
	cc.size -= e.size
	pc := cc.conns[e.conn]
	pc.entries.Remove(e.connElem)
	pc.size -= e.size
	if pc.entries.Len() == 0 {
		delete(cc.conns, e.conn)
	}
	tc := cc.topics[e.topic]
	tc.entries.Remove(e.topicElem)
	tc.size -= e.size
	if tc.entries.Len() == 0 {
		delete(cc.topics, e.topic)
	}
}

func (cc *chunkCache) expireLocked(now time.Time) {
	for f := cc.order.Front(); f != nil && now.Sub(f.Value.(*cacheEntry).at) >= cc.ttl; f = cc.order.Front() {
		cc.dropLocked(f.Value.(*cacheEntry))
	}
}

// since returns the replay frames of messages on topic cached within
// window. They are shared: callers must not modify them.
func (cc *chunkCache) since(topic string, window time.Duration, now time.Time) [][]byte {
	if cc == nil || window <= 0 {
		return nil
	}
	cc.mu.Lock()
	defer cc.mu.Unlock()
	cc.expireLocked(now)
	tc := cc.topics[topic]
	if tc == nil {
		return nil
	}
	var out [][]byte
	for el := tc.entries.Front(); el != nil; el = el.Next() {
		if e := el.Value.(*cacheEntry); now.Sub(e.at) <= window {
			out = append(out, e.frames...)
		}
	}
	return out
}

// Bytes is what the cache holds now, overhead included (tests and stats).
func (cc *chunkCache) Bytes() uint64 {
	if cc == nil {
		return 0
	}
	cc.mu.Lock()
	defer cc.mu.Unlock()
	return cc.size
}

// entries counts cached messages (tests).
func (cc *chunkCache) entries() int {
	if cc == nil {
		return 0
	}
	cc.mu.Lock()
	defer cc.mu.Unlock()
	return cc.order.Len()
}

// CacheBytes reports the chunk cache's current size.
func (s *Server) CacheBytes() uint64 { return s.cache.Bytes() }

// cachePending accumulates the chunks of a cacheable message until its final
// chunk; a message that grows past chunk_cache_max_message, or past what
// the connection's pending messages may hold together (its share of the
// cache), isn't cached. One left unfinished for the cache's TTL is dropped.
type cachePending struct {
	chunks []*wire.Deliver
	size   uint64
	over   bool
	at     time.Time
}

func (c *conn) cacheChunk(m *wire.Publish, d *wire.Deliver) {
	s := c.srv
	if s.cache == nil || !m.Cacheable {
		return
	}
	now := time.Now()
	p := c.pendingCache[m.MessageId]
	if p == nil {
		for id, q := range c.pendingCache {
			if now.Sub(q.at) >= s.cache.ttl {
				delete(c.pendingCache, id)
			}
		}
		if len(c.pendingCache) >= s.cfg.MaxPartial {
			return
		}
		p = &cachePending{at: now}
		c.pendingCache[m.MessageId] = p
	}
	if !p.over {
		p.size += uint64(len(d.Data))
		if p.size > s.cfg.ChunkCacheMaxMessage || c.pendingBytes() > s.cache.perConn {
			p.over, p.chunks = true, nil
		} else {
			p.chunks = append(p.chunks, d)
		}
	}
	if m.Final {
		delete(c.pendingCache, m.MessageId)
		if !p.over {
			s.cache.put(string(m.Topic), c.id, p.chunks, p.size, now)
		}
	}
}

// pendingBytes is what the connection's unfinished cacheable messages hold.
func (c *conn) pendingBytes() uint64 {
	var n uint64
	for _, p := range c.pendingCache {
		if !p.over {
			n += p.size
		}
	}
	return n
}

// ---------------------------------------------------------------- endpoint hosts

// hostStreamsPerNetwork is how many inbound streams from one client
// network earn an endpoint host its per-stream allowance.
const hostStreamsPerNetwork = 4

// countInboundLocked records an inbound stream (delta 1) or its end (-1)
// from client network net. Only streams from other networks than the
// host's own, at most hostStreamsPerNetwork per network, count toward the
// host allowance: a host can't raise its own budget by opening streams to
// itself, and needs members on many networks to reach the cap. Callers
// hold c.smu.
func (c *conn) countInboundLocked(net string, delta int) {
	old := c.inByNet[net]
	if n := old + delta; n > 0 {
		c.inByNet[net] = n
	} else {
		delete(c.inByNet, net)
	}
	if net == c.netKey {
		return
	}
	switch {
	case delta > 0 && old < hostStreamsPerNetwork:
		c.hostStreams++
	case delta < 0 && old <= hostStreamsPerNetwork:
		c.hostStreams--
	}
}

// hostBudget sizes a connection's frame and byte buckets. A connection that
// hosts a registered endpoint carries every inbound stream's traffic (a
// community server's members), so it gets the plain per-connection budget
// plus host_*_per_stream for each counted inbound stream (from another
// network, at most hostStreamsPerNetwork per network), capped at
// host_*_per_second; bursts grow by twice the per-stream allowance up to
// host_*_burst. A dedicated relay gives it only to its server's endpoint.
// Plain clients keep the per-connection budget.
func (c *conn) hostBudget() {
	cfg := &c.srv.cfg
	c.srv.mu.RLock()
	host := c.endpointKey != "" && c.srv.endpointAllowed([]byte(c.endpointKey))
	c.srv.mu.RUnlock()
	c.smu.Lock()
	streams := float64(c.hostStreams)
	c.smu.Unlock()
	fr, fb, br, bb := cfg.FramesPerSecond, cfg.FrameBurst, cfg.BytesPerSecond, cfg.ByteBurst
	if host {
		// A host cap below the plain budget leaves the plain budget.
		fr = max(fr, min(cfg.HostFramesPerSecond, fr+streams*cfg.HostFramesPerStream))
		fb = max(fb, min(cfg.HostFrameBurst, fb+2*streams*cfg.HostFramesPerStream))
		br = max(br, min(cfg.HostBytesPerSecond, br+streams*cfg.HostBytesPerStream))
		bb = max(bb, min(cfg.HostByteBurst, bb+2*streams*cfg.HostBytesPerStream))
	}
	if r, b := c.frames.params(); r != fr || b != fb {
		c.frames.set(fr, fb)
	}
	if r, b := c.bytes.params(); r != br || b != bb {
		c.bytes.set(br, bb)
	}
}

// FrameBudget reports the frame rate and burst of the connection hosting
// endpointKey (tests).
func (s *Server) FrameBudget(endpointKey []byte) (rate, burst float64, ok bool) {
	s.mu.RLock()
	c := s.endpoints[string(endpointKey)]
	s.mu.RUnlock()
	if c == nil {
		return 0, 0, false
	}
	rate, burst = c.frames.params()
	return rate, burst, true
}
