// Package relay is the relay core: topic publish/subscribe and byte streams to
// registered endpoints, over WebSocket. It keeps nothing on disk, has no
// accounts, and never looks inside payloads (docs/protocol.md §2).
package relay

import (
	"container/list"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/ops"
	"github.com/hyperkubeorg/openarcanum/internal/relayid"
	"github.com/hyperkubeorg/openarcanum/internal/relayurl"
	"github.com/hyperkubeorg/openarcanum/internal/sfu"
	"github.com/hyperkubeorg/openarcanum/internal/version"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// InboundStreamBit is set on every stream id the relay assigns for streams
// arriving at an endpoint. Ids chosen by clients must not have it set.
const InboundStreamBit = 0x80000000

const maxTopicLen = 64

// Server is a relay instance. Create one with New and mount Handler.
type Server struct {
	cfg Config
	log *slog.Logger
	// registryKey is served on a plain GET of the relay URL (registrykey.go).
	registryKey atomic.Pointer[[]byte]

	mu        sync.RWMutex
	conns     map[*conn]struct{}
	topics    map[string]map[*conn]struct{}
	endpoints map[string]*conn
	// Connections being accepted (counted against max_connections before
	// they are in conns), and connections that haven't started yet: in
	// all (being accepted included) and per network (ops.NetworkKey), and
	// the open ones oldest first. Guarded by mu.
	accepting     int
	unstarted     int
	unstartedBy   map[string]int
	unstartedList *list.List // *conn
	// Connections (being accepted included) and subscriptions per network,
	// and subscriptions in all (max_connections_per_ip,
	// max_subscriptions_per_ip, max_subscriptions_total). Guarded by mu.
	connsBy   map[string]int
	subsBy    map[string]int
	subsTotal int

	nextConn   atomic.Uint64
	nextStream atomic.Uint32

	stats Stats
	sfu   *sfu.SFU

	versions []uint32
	arrivals arrivals
	cache    *chunkCache

	// Dedicated relays (dedicated.go): the server key served (nil until
	// known), the block list, and why it couldn't be read at start.
	dedicated atomic.Pointer[[]byte]
	blocks    *blockList
	blocksErr error

	// The relay's identity (docs/protocol.md §2 "Relay identity"): its key
	// (registry_key_file, or a fresh one per run), why the key file
	// couldn't be read, and its own canonical URL (public_url; empty when
	// unknown). Everything clients sign for a connection is bound to them.
	key    *identity.Key
	keyErr error
	url    atomic.Pointer[string]
}

// Stats are counters exposed for tests and the health endpoint.
type Stats struct {
	SlowConsumers atomic.Int64
	Delivered     atomic.Int64
	// Refused counts connections turned away at max_connections;
	// UnstartedRefused those turned away because too many connections (in
	// all, or from their network) hadn't started yet; NetworkRefused those
	// turned away at max_connections_per_ip.
	// UnstartedEvicted counts unstarted connections closed to make room
	// for a newcomer.
	Refused          atomic.Int64
	UnstartedRefused atomic.Int64
	NetworkRefused   atomic.Int64
	UnstartedEvicted atomic.Int64
	// Abuse controls: connections asked for a proof-of-work, refused
	// proofs, connections closed for not starting in time, and publishes
	// refused for lack of a fan-out token.
	PowAsked      atomic.Int64
	PowRefused    atomic.Int64
	StartTimeouts atomic.Int64
	FanoutRefused atomic.Int64
	// Connections refused because their network is blocked (dedicated
	// relays).
	BlockedRefused atomic.Int64
}

// New creates a relay. cfg must already be validated.
func New(cfg Config, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	s := &Server{
		cfg:       cfg,
		log:       log,
		conns:     make(map[*conn]struct{}),
		topics:    make(map[string]map[*conn]struct{}),
		endpoints: make(map[string]*conn),
		versions:  version.Supported(),

		unstartedBy:   make(map[string]int),
		unstartedList: list.New(),
		connsBy:       make(map[string]int),
		subsBy:        make(map[string]int),
		cache:         newChunkCache(cfg.ChunkCacheBytes, cfg.ChunkCacheTopicBytes, cfg.chunkCacheConnBytes(), cfg.ChunkCacheTTL.Duration),
	}
	s.key, s.keyErr = loadKey(cfg.RegistryKeyFile)
	if s.keyErr != nil {
		log.Error("relay key file unreadable; refusing every connection", "err", s.keyErr)
	}
	if u := cfg.AnnouncedURL(); u != "" {
		// Validate checked it.
		if err := s.SetURL(u); err != nil {
			log.Error("public_url", "err", err)
		}
	} else {
		log.Warn("relay has no public_url: endpoint registrations and fan-out tokens are bound to its key but not to a URL; set public_url on a public relay")
	}
	if cfg.Dedicated() {
		s.blocks, s.blocksErr = loadBlockList(cfg.BlockFile, cfg.MaxBlockedNetworks, time.Now())
		if s.blocksErr != nil {
			log.Error("dedicated relay: block list unreadable; refusing every connection", "err", s.blocksErr)
		}
		if cfg.DedicatedServerKey != "" {
			k, _ := hex.DecodeString(cfg.DedicatedServerKey)
			s.SetDedicatedKey(k)
		} else {
			s.loadDedicatedKeyFile()
		}
	}
	return s
}

// Stats returns live counters.
func (s *Server) Stats() *Stats { return &s.stats }

// Connections returns the number of open connections.
func (s *Server) Connections() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.conns)
}

// CloseAll drops every connection (shutdown, and tests that kill a relay).
func (s *Server) CloseAll() {
	s.mu.RLock()
	conns := make([]*conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.RUnlock()
	for _, c := range conns {
		c.close(websocket.StatusGoingAway, "relay shutting down", nil)
	}
}

// Handler serves the WebSocket endpoint at /ws and a health check at /healthz.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.serveRelayURL)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	return mux
}

func (s *Server) serveWS(w http.ResponseWriter, r *http.Request) {
	ip := ops.RemoteIP(r)
	key := ops.NetworkKey(ip)
	if s.keyErr != nil {
		http.Error(w, "relay unavailable", http.StatusServiceUnavailable)
		return
	}
	if s.cfg.Dedicated() {
		if s.blocksErr != nil {
			http.Error(w, "relay unavailable", http.StatusServiceUnavailable)
			return
		}
		if s.blocks.blocked(key, time.Now()) {
			s.stats.BlockedRefused.Add(1)
			http.Error(w, "your network is blocked on this relay", http.StatusForbidden)
			return
		}
	}
	status, msg, evicted := s.admit(key)
	if evicted != nil {
		go evicted.close(websocket.StatusTryAgainLater, "relay busy: unstarted connection evicted", nil)
	}
	if status != 0 {
		http.Error(w, msg, status)
		return
	}
	// Relays are open by design: knowing a topic is the permission, so any
	// origin may connect.
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		s.mu.Lock()
		s.accepting--
		s.releaseUnstartedLocked(key)
		s.releaseConnLocked(key)
		s.mu.Unlock()
		return
	}
	ws.SetReadLimit(int64(s.cfg.MaxChunk) + 4096)
	c := s.newConn(r.Context(), ws, ip, key)
	c.run()
}

// admit reserves a connection slot and an unstarted slot for a new
// connection from network key, or says why not (an HTTP status and
// message). A connection holds its unstarted slot until it starts or
// closes, so connections that never send Start hold at most
// max_unstarted_per_ip slots per network and max_unstarted in all, whatever
// max_connections is. When every unstarted slot is taken, the newcomer
// takes the oldest unstarted connection's slot, and admit returns that
// connection for the caller to close: a flood of connections that never
// start then only delays real clients by the time they need to send Start,
// instead of locking them out for the start window.
func (s *Server) admit(key string) (int, string, *conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case len(s.conns)+s.accepting >= s.cfg.MaxConnections:
		s.stats.Refused.Add(1)
		return http.StatusServiceUnavailable, "relay full", nil
	case s.connsBy[key] >= s.cfg.MaxConnectionsPerIP:
		s.stats.NetworkRefused.Add(1)
		return http.StatusTooManyRequests, "too many connections from your network", nil
	case s.unstartedBy[key] >= s.cfg.MaxUnstartedPerIP:
		s.stats.UnstartedRefused.Add(1)
		return http.StatusTooManyRequests, "too many connections from your network are still starting", nil
	}
	var evicted *conn
	if s.unstarted >= s.cfg.maxUnstarted() {
		front := s.unstartedList.Front()
		if front == nil {
			// Every slot is a connection still being accepted.
			s.stats.UnstartedRefused.Add(1)
			return http.StatusServiceUnavailable, "relay busy: too many connections are starting", nil
		}
		evicted = front.Value.(*conn)
		s.dropUnstartedLocked(evicted)
		s.stats.UnstartedEvicted.Add(1)
	}
	s.accepting++
	s.unstarted++
	s.unstartedBy[key]++
	s.connsBy[key]++
	return 0, "", evicted
}

// releaseConnLocked gives back a connection's place in its network's
// count. Callers hold s.mu.
func (s *Server) releaseConnLocked(key string) {
	if s.connsBy[key]--; s.connsBy[key] <= 0 {
		delete(s.connsBy, key)
	}
}

// dropSubsLocked gives back n subscriptions of network key. Callers hold
// s.mu.
func (s *Server) dropSubsLocked(key string, n int) {
	s.subsTotal -= n
	if s.subsBy[key] -= n; s.subsBy[key] <= 0 {
		delete(s.subsBy, key)
	}
}

// releaseUnstartedLocked gives back an unstarted slot. Callers hold s.mu.
func (s *Server) releaseUnstartedLocked(key string) {
	s.unstarted--
	if s.unstartedBy[key]--; s.unstartedBy[key] <= 0 {
		delete(s.unstartedBy, key)
	}
}

// dropUnstartedLocked gives back c's unstarted slot if it holds one.
// Callers hold s.mu.
func (s *Server) dropUnstartedLocked(c *conn) {
	if !c.holdsUnstarted {
		return
	}
	c.holdsUnstarted = false
	s.unstartedList.Remove(c.unstartedElem)
	c.unstartedElem = nil
	s.releaseUnstartedLocked(c.netKey)
}

// Unstarted reports the connections that haven't started yet (tests).
func (s *Server) Unstarted() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.unstarted
}

// ---------------------------------------------------------------- connections

type link struct {
	client   *conn
	clientID uint32
	endpoint *conn
	inID     uint32
	closed   atomic.Bool
}

type conn struct {
	id     uint64
	ip     string // the client's address (per-address call limits)
	netKey string // its network (ops.NetworkKey; unstarted limits)
	srv    *Server
	ws     *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	nonce  []byte

	// Start (abuse.go): the proof-of-work asked for in Hello, how long
	// the client has to send Start, and the version it chose. holdsUnstarted
	// (guarded by srv.mu) is true until the connection gives back its
	// unstarted slot (on Start or close).
	powBits        uint32
	startWindow    time.Duration
	startTimer     *time.Timer
	started        atomic.Bool
	version        uint32
	holdsUnstarted bool
	unstartedElem  *list.Element

	// Fan-out token issuers and their expiry (abuse.go), and moderation
	// command ids seen on this connection until they expire (dedicated.go).
	fmu          sync.Mutex
	issuers      map[string]time.Time
	seenCommands map[string]time.Time

	// The send queue: frames waiting for the writer, grown on demand (a
	// connection that never starts never allocates much), with their
	// total size in queued.
	qmu    sync.Mutex
	q      [][]byte
	wake   chan struct{}
	queued atomic.Int64

	frames *bucket
	bytes  *bucket

	closeOnce sync.Once

	// Guarded by srv.mu.
	subs        map[string]struct{}
	endpointKey string

	// Only touched by the read loop.
	partial      map[uint64]inflight
	pendingCache map[uint64]*cachePending

	smu        sync.Mutex
	outStreams map[uint32]*link // streams this conn opened, by client id
	inStreams  map[uint32]*link // streams arriving at this endpoint, by relay id
	// Inbound streams by the client's network, and how many of them earn
	// the host allowance (hostBudget): streams from networks other than
	// this connection's, at most hostStreamsPerNetwork per network.
	inByNet     map[string]int
	hostStreams int
	calls       map[string]*sfu.Peer
	joining     int
	closing     bool
}

func (s *Server) newConn(parent context.Context, ws *websocket.Conn, ip, netKey string) *conn {
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	nonce := make([]byte, 32)
	rand.Read(nonce)
	s.mu.RLock()
	// Others from its network: this one is counted since admit.
	open, unstarted, fromNet := len(s.conns), s.unstarted, s.connsBy[netKey]-1
	s.mu.RUnlock()
	bits := s.powBits(open, s.arrivals.add(time.Now()), fromNet)
	if bits > 0 {
		s.stats.PowAsked.Add(1)
	}
	c := &conn{
		powBits:        bits,
		startWindow:    s.startWindow(bits, unstarted),
		holdsUnstarted: true,
		issuers:        make(map[string]time.Time),
		pendingCache:   make(map[uint64]*cachePending),
		id:             s.nextConn.Add(1),
		ip:             ip,
		netKey:         netKey,
		srv:            s,
		ws:             ws,
		ctx:            ctx,
		cancel:         cancel,
		nonce:          nonce,
		wake:           make(chan struct{}, 1),
		frames:         newBucket(s.cfg.FramesPerSecond, s.cfg.FrameBurst),
		bytes:          newBucket(s.cfg.BytesPerSecond, s.cfg.ByteBurst),
		subs:           make(map[string]struct{}),
		partial:        make(map[uint64]inflight),
		outStreams:     make(map[uint32]*link),
		inStreams:      make(map[uint32]*link),
		inByNet:        make(map[string]int),
		calls:          make(map[string]*sfu.Peer),
	}
	s.mu.Lock()
	s.accepting--
	s.conns[c] = struct{}{}
	c.unstartedElem = s.unstartedList.PushBack(c)
	s.mu.Unlock()
	return c
}

func (c *conn) run() {
	// Until Start, a connection runs only its writer (and the read loop on
	// this goroutine); the pinger starts with Start.
	go c.writer()
	c.armStartTimer()
	cfg := &c.srv.cfg
	var cacheTTL uint32
	if c.srv.cache != nil {
		cacheTTL = uint32(cfg.ChunkCacheTTL.Milliseconds())
	}
	url := c.srv.URL()
	c.sendFrame(&wire.RelayFrame{Kind: &wire.RelayFrame_Hello{Hello: &wire.Hello{
		RelayKey:        c.srv.key.Public,
		Url:             url,
		RelaySignature:  relayid.SignHello(c.srv.key, url, c.nonce),
		Nonce:           c.nonce,
		MaxChunk:        cfg.MaxChunk,
		MaxMessage:      cfg.MaxMessage,
		Region:          cfg.Region,
		Calls:           c.srv.sfu != nil,
		Versions:        c.srv.versions,
		PowBits:         c.powBits,
		StartWindowMs:   uint32(c.startWindow.Milliseconds()),
		CacheTtlMs:      cacheTTL,
		FanoutThreshold: uint32(cfg.FanoutThreshold),
		DedicatedTo:     c.srv.dedicatedKey(),
	}}})
	err := c.readLoop()
	c.close(websocket.StatusNormalClosure, "", err)
}

func (c *conn) readLoop() error {
	for {
		typ, data, err := c.ws.Read(c.ctx)
		if err != nil {
			return err
		}
		if wait := c.frames.take(1); wait > 0 {
			if !sleepCtx(c.ctx, wait) {
				return c.ctx.Err()
			}
		}
		if wait := c.bytes.take(float64(len(data))); wait > 0 {
			if !sleepCtx(c.ctx, wait) {
				return c.ctx.Err()
			}
		}
		if typ != websocket.MessageBinary {
			return errors.New("text frame")
		}
		var f wire.RelayFrame
		if err := proto.Unmarshal(data, &f); err != nil {
			c.sendError(0, wire.ErrorCode_ERROR_CODE_BAD_FRAME, "unparseable frame")
			return err
		}
		if !c.started.Load() {
			// Nothing but Start until the client has chosen a version (and
			// shown its proof-of-work).
			st := f.GetStart()
			if st == nil {
				c.sendError(0, wire.ErrorCode_ERROR_CODE_UNSUPPORTED_VERSION, "send Start (a protocol version) first; update OpenArcanum")
				c.closeAfterFlush("no start")
				<-c.ctx.Done() // closed once the Error is written
				return nil
			}
			if !c.start(st) {
				c.closeAfterFlush("start refused")
				<-c.ctx.Done() // closed once the Error is written
				return nil
			}
			continue
		}
		if err := c.handle(&f); err != nil {
			return err
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (c *conn) writer() {
	var batch [][]byte
	for {
		select {
		case <-c.wake:
		case <-c.ctx.Done():
			return
		}
		// Take everything queued; the two slices swap, so neither is
		// reallocated once grown.
		c.qmu.Lock()
		batch, c.q = c.q, batch[:0]
		c.qmu.Unlock()
		for i, b := range batch {
			ctx, cancel := context.WithTimeout(c.ctx, c.srv.cfg.WriteTimeout.Duration)
			err := c.ws.Write(ctx, websocket.MessageBinary, b)
			cancel()
			c.queued.Add(-int64(len(b)))
			batch[i] = nil
			if err != nil {
				c.close(websocket.StatusGoingAway, "write failed", err)
				return
			}
		}
	}
}

// maxQueuedFrames bounds a connection's send queue in frames (its bytes
// are bounded by send_queue_bytes).
const maxQueuedFrames = 8192

func (c *conn) pinger() {
	t := time.NewTicker(c.srv.cfg.PingInterval.Duration)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			ctx, cancel := context.WithTimeout(c.ctx, c.srv.cfg.PingInterval.Duration)
			err := c.ws.Ping(ctx)
			cancel()
			if err != nil {
				c.close(websocket.StatusGoingAway, "ping timeout", err)
				return
			}
		case <-c.ctx.Done():
			return
		}
	}
}

// enqueue queues an already-serialized frame. A connection whose queue would
// grow past the limit is a slow consumer and gets disconnected.
func (c *conn) enqueue(b []byte) bool {
	if c.ctx.Err() != nil {
		return false
	}
	if uint64(c.queued.Load())+uint64(len(b)) > c.srv.cfg.SendQueueBytes {
		c.slow()
		return false
	}
	c.qmu.Lock()
	if len(c.q) >= maxQueuedFrames {
		c.qmu.Unlock()
		c.slow()
		return false
	}
	c.queued.Add(int64(len(b)))
	c.q = append(c.q, b)
	c.qmu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
	return true
}

func (c *conn) slow() {
	c.srv.stats.SlowConsumers.Add(1)
	go c.close(websocket.StatusPolicyViolation, "slow consumer", nil)
}

func (c *conn) sendFrame(f *wire.RelayFrame) bool {
	b, err := proto.Marshal(f)
	if err != nil {
		return false
	}
	return c.enqueue(b)
}

func (c *conn) sendError(req uint32, code wire.ErrorCode, msg string) {
	c.sendFrame(&wire.RelayFrame{Kind: &wire.RelayFrame_Error{Error: &wire.Error{Req: req, Code: code, Message: msg}}})
}

func (c *conn) ack(req uint32) {
	if req != 0 {
		c.sendFrame(&wire.RelayFrame{Kind: &wire.RelayFrame_Ack{Ack: &wire.Ack{Req: req}}})
	}
}

func (c *conn) close(code websocket.StatusCode, reason string, err error) {
	c.closeOnce.Do(func() {
		s := c.srv
		s.mu.Lock()
		delete(s.conns, c)
		s.dropUnstartedLocked(c)
		s.releaseConnLocked(c.netKey)
		s.dropSubsLocked(c.netKey, len(c.subs))
		startTimer := c.startTimer
		for t := range c.subs {
			if set := s.topics[t]; set != nil {
				delete(set, c)
				if len(set) == 0 {
					delete(s.topics, t)
				}
			}
		}
		if c.endpointKey != "" && s.endpoints[c.endpointKey] == c {
			delete(s.endpoints, c.endpointKey)
		}
		s.mu.Unlock()

		c.smu.Lock()
		var links []*link
		for _, l := range c.outStreams {
			links = append(links, l)
		}
		for _, l := range c.inStreams {
			links = append(links, l)
		}
		c.smu.Unlock()
		for _, l := range links {
			s.closeLink(l, c, "peer disconnected")
		}
		c.leaveCalls()

		if reason != "" {
			s.log.Debug("relay connection closed", "conn", c.id, "reason", reason, "err", errKind(err))
		}
		c.ws.Close(code, reason)
		c.cancel()
		if startTimer != nil {
			startTimer.Stop()
		}
	})
}

// errKind describes a connection error without the peer's address (a
// net.OpError prints both ends): the close status, a known sentinel, the
// operation that failed, or the error's type.
func errKind(err error) string {
	if err == nil {
		return ""
	}
	if st := websocket.CloseStatus(err); st != -1 {
		return st.String()
	}
	for _, e := range []error{context.Canceled, context.DeadlineExceeded, io.EOF, io.ErrUnexpectedEOF, net.ErrClosed} {
		if errors.Is(err, e) {
			return e.Error()
		}
	}
	var op *net.OpError
	if errors.As(err, &op) {
		var se *os.SyscallError
		if errors.As(op.Err, &se) {
			return op.Op + ": " + se.Err.Error()
		}
		return op.Op + ": " + fmt.Sprintf("%T", op.Err)
	}
	return fmt.Sprintf("%T", err)
}

// ---------------------------------------------------------------- frames

func (c *conn) handle(f *wire.RelayFrame) error {
	switch k := f.Kind.(type) {
	case *wire.RelayFrame_Subscribe:
		c.subscribe(k.Subscribe)
	case *wire.RelayFrame_Unsubscribe:
		c.unsubscribe(k.Unsubscribe)
	case *wire.RelayFrame_Publish:
		c.publish(k.Publish)
	case *wire.RelayFrame_RegisterEndpoint:
		c.register(k.RegisterEndpoint)
	case *wire.RelayFrame_OpenStream:
		c.openStream(k.OpenStream)
	case *wire.RelayFrame_StreamData:
		c.streamData(k.StreamData)
	case *wire.RelayFrame_StreamClose:
		c.streamClose(k.StreamClose)
	case *wire.RelayFrame_CallJoin:
		c.callJoin(k.CallJoin)
	case *wire.RelayFrame_CallSignal:
		c.callSignal(k.CallSignal)
	case *wire.RelayFrame_CallLayer:
		c.callLayer(k.CallLayer)
	case *wire.RelayFrame_CallLeave:
		c.callLeave(k.CallLeave)
	case *wire.RelayFrame_CallListen:
		c.callListen(k.CallListen)
	case *wire.RelayFrame_CallUpdate:
		c.callUpdate(k.CallUpdate)
	case *wire.RelayFrame_FanoutAuth:
		c.fanoutAuth(k.FanoutAuth)
	case *wire.RelayFrame_Moderation:
		c.moderation(k.Moderation)
	default:
		c.sendError(0, wire.ErrorCode_ERROR_CODE_BAD_FRAME, "unexpected frame")
	}
	return nil
}

func validTopic(t []byte) bool { return len(t) > 0 && len(t) <= maxTopicLen }

func (c *conn) subscribe(m *wire.Subscribe) {
	if !validTopic(m.Topic) {
		c.sendError(m.Req, wire.ErrorCode_ERROR_CODE_BAD_FRAME, "bad topic")
		return
	}
	s := c.srv
	if !s.topicAllowed(m.Topic) {
		c.sendError(m.Req, wire.ErrorCode_ERROR_CODE_NOT_DEDICATED, notDedicatedMsg)
		return
	}
	t := string(m.Topic)
	s.mu.Lock()
	if _, ok := c.subs[t]; !ok {
		var msg string
		switch {
		case len(c.subs) >= s.cfg.MaxSubscriptions:
			msg = "too many subscriptions"
		case s.subsBy[c.netKey] >= s.cfg.MaxSubscriptionsPerIP:
			msg = "too many subscriptions from your network"
		case s.subsTotal >= s.cfg.MaxSubscriptionsTotal:
			msg = "relay full: too many subscriptions"
		}
		if msg != "" {
			s.mu.Unlock()
			c.sendError(m.Req, wire.ErrorCode_ERROR_CODE_LIMIT, msg)
			return
		}
		s.subsBy[c.netKey]++
		s.subsTotal++
	}
	c.subs[t] = struct{}{}
	set := s.topics[t]
	if set == nil {
		set = make(map[*conn]struct{})
		s.topics[t] = set
	}
	set[c] = struct{}{}
	s.mu.Unlock()
	c.ack(m.Req)
	if m.ReplayMs > 0 {
		// The cache holds each replayed frame serialized once; every
		// subscriber's queue shares those bytes, so a replay costs a
		// subscriber slice headers, not a copy of the topic's cache.
		window := min(time.Duration(m.ReplayMs)*time.Millisecond, s.cfg.ChunkCacheTTL.Duration)
		for _, b := range s.cache.since(t, window, time.Now()) {
			if !c.enqueue(b) {
				break
			}
		}
	}
}

func (c *conn) unsubscribe(m *wire.Unsubscribe) {
	s := c.srv
	t := string(m.Topic)
	s.mu.Lock()
	if _, ok := c.subs[t]; ok {
		delete(c.subs, t)
		s.dropSubsLocked(c.netKey, 1)
	}
	if set := s.topics[t]; set != nil {
		delete(set, c)
		if len(set) == 0 {
			delete(s.topics, t)
		}
	}
	s.mu.Unlock()
	c.ack(m.Req)
}

func (c *conn) publish(m *wire.Publish) {
	if !validTopic(m.Topic) {
		c.sendError(0, wire.ErrorCode_ERROR_CODE_BAD_FRAME, "bad topic")
		return
	}
	s := c.srv
	p, inFlight := c.partial[m.MessageId]
	if inFlight && p.size == rejected {
		if m.Final {
			delete(c.partial, m.MessageId)
		}
		return
	}
	if inFlight && p.topic != string(m.Topic) {
		// Every chunk of a message goes where its first chunk went: the
		// topic's checks ran on that one.
		c.sendError(0, wire.ErrorCode_ERROR_CODE_BAD_FRAME, "chunk for another topic")
		c.reject(m)
		return
	}
	if !inFlight && len(c.partial) >= s.cfg.MaxPartial {
		c.sendError(0, wire.ErrorCode_ERROR_CODE_LIMIT, "too many messages in flight")
		return
	}
	if !inFlight && !s.topicAllowed(m.Topic) {
		c.sendError(0, wire.ErrorCode_ERROR_CODE_NOT_DEDICATED, notDedicatedMsg)
		c.reject(m)
		return
	}
	if !inFlight && s.cfg.FanoutThreshold > 0 && s.subscribersBesides(m.Topic, c) > s.cfg.FanoutThreshold && !c.mayFanOut(m.Topic) {
		s.stats.FanoutRefused.Add(1)
		c.sendError(0, wire.ErrorCode_ERROR_CODE_FANOUT_TOKEN_REQUIRED, "this topic is large; publishing needs a fan-out token")
		c.reject(m)
		return
	}
	total := p.size + uint64(len(m.Data))
	if uint64(len(m.Data)) > uint64(s.cfg.MaxChunk) || total > s.cfg.MaxMessage {
		c.sendError(0, wire.ErrorCode_ERROR_CODE_TOO_LARGE, "message too large")
		c.reject(m)
		return
	}
	if m.Final {
		delete(c.partial, m.MessageId)
	} else {
		c.partial[m.MessageId] = inflight{topic: string(m.Topic), size: total}
	}

	d := &wire.Deliver{Topic: m.Topic, Sender: c.id, MessageId: m.MessageId, Seq: m.Seq, Final: m.Final, Data: m.Data}
	b, err := proto.Marshal(&wire.RelayFrame{Kind: &wire.RelayFrame_Deliver{Deliver: d}})
	if err != nil {
		return
	}
	c.cacheChunk(m, d)
	s.mu.RLock()
	targets := make([]*conn, 0, len(s.topics[string(m.Topic)]))
	for sub := range s.topics[string(m.Topic)] {
		if sub != c {
			targets = append(targets, sub)
		}
	}
	s.mu.RUnlock()
	for _, sub := range targets {
		if sub.enqueue(b) {
			s.stats.Delivered.Add(1)
		}
	}
}

// reject drops the rest of a message one of whose chunks was refused: its
// remaining chunks are ignored, whatever topic they name, and nothing of it
// is cached.
func (c *conn) reject(m *wire.Publish) {
	delete(c.pendingCache, m.MessageId)
	if m.Final {
		delete(c.partial, m.MessageId)
	} else {
		c.partial[m.MessageId] = inflight{size: rejected}
	}
}

// subscribersBesides counts a topic's subscribers other than c.
func (s *Server) subscribersBesides(topic []byte, c *conn) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	set := s.topics[string(topic)]
	n := len(set)
	if _, ok := set[c]; ok {
		n--
	}
	return n
}

// inflight is a message whose chunks are still arriving: the topic its
// first chunk named, and its bytes so far.
type inflight struct {
	topic string
	size  uint64
}

// rejected as a size marks a message whose remaining chunks are dropped.
const rejected = ^uint64(0)

func (c *conn) register(m *wire.RegisterEndpoint) {
	if !identity.ValidPublic(m.EndpointKey) {
		c.sendError(m.Req, wire.ErrorCode_ERROR_CODE_BAD_FRAME, "bad endpoint key")
		return
	}
	local := c.local()
	if err := local.Check(local.Key, m.Url, c.nonce); err != nil {
		c.sendError(m.Req, wire.ErrorCode_ERROR_CODE_BAD_SIGNATURE, "endpoint registration "+err.Error())
		return
	}
	if err := identity.Verify(m.EndpointKey, identity.CtxRelayEndpoint, local.Binding(m.Url).Bytes(), m.Signature); err != nil {
		c.sendError(m.Req, wire.ErrorCode_ERROR_CODE_BAD_SIGNATURE, "bad endpoint signature")
		return
	}
	s := c.srv
	if !s.endpointAllowed(m.EndpointKey) {
		c.sendError(m.Req, wire.ErrorCode_ERROR_CODE_NOT_DEDICATED, notDedicatedMsg)
		return
	}
	key := string(m.EndpointKey)
	s.mu.Lock()
	old := s.endpoints[key]
	if c.endpointKey != "" && c.endpointKey != key && s.endpoints[c.endpointKey] == c {
		// A connection hosts one endpoint: the key it registered before
		// (rotated away) is no longer reachable.
		delete(s.endpoints, c.endpointKey)
	}
	s.endpoints[key] = c
	c.endpointKey = key
	s.mu.Unlock()
	if old != nil && old != c {
		// The endpoint moved to a new connection (a reconnect, or new hardware
		// holding the same key). Streams to the old connection end.
		old.smu.Lock()
		var links []*link
		for _, l := range old.inStreams {
			links = append(links, l)
		}
		old.smu.Unlock()
		for _, l := range links {
			s.closeLink(l, nil, "endpoint moved")
		}
		s.mu.Lock()
		if old.endpointKey == key {
			old.endpointKey = ""
		}
		s.mu.Unlock()
		old.hostBudget()
	}
	c.hostBudget()
	s.log.Debug("endpoint registered", "conn", c.id, "endpoint", identity.Fingerprint(m.EndpointKey))
	c.ack(m.Req)
}

// local is what a binding presented on this connection must match: the
// relay's key and URL, and this connection's nonce.
func (c *conn) local() relayid.Local {
	return relayid.Local{Key: c.srv.key.Public, URL: c.srv.URL(), Nonce: c.nonce}
}

// Key is the relay's identity key (its registry key).
func (s *Server) Key() *identity.Key { return s.key }

// URL is the relay's own canonical URL, or "" when it has none.
func (s *Server) URL() string {
	if p := s.url.Load(); p != nil {
		return *p
	}
	return ""
}

// SetURL sets the URL the relay is reached at (public_url; tests that only
// know it once listening). Endpoint registrations, fan-out tokens and
// moderation commands must then name it.
func (s *Server) SetURL(u string) error {
	c, err := relayurl.Canonical(u)
	if err != nil {
		return err
	}
	s.url.Store(&c)
	return nil
}

// loadKey reads (or creates) the relay key file, or makes a fresh key.
func loadKey(path string) (*identity.Key, error) {
	if path != "" {
		k, err := identity.LoadOrCreate(path)
		if err == nil {
			return k, nil
		}
		fresh, _ := identity.Generate()
		return fresh, fmt.Errorf("registry_key_file: %w", err)
	}
	return identity.Generate()
}

func (c *conn) openStream(m *wire.OpenStream) {
	s := c.srv
	if m.StreamId == 0 || m.StreamId&InboundStreamBit != 0 {
		c.sendFrame(closeFrame(m.StreamId, "bad stream id"))
		return
	}
	if !s.endpointAllowed(m.EndpointKey) {
		c.sendFrame(closeFrame(m.StreamId, "not_dedicated"))
		return
	}
	s.mu.RLock()
	ep := s.endpoints[string(m.EndpointKey)]
	s.mu.RUnlock()
	if ep == nil || ep.ctx.Err() != nil {
		c.sendFrame(closeFrame(m.StreamId, "no_endpoint"))
		return
	}
	c.smu.Lock()
	if _, dup := c.outStreams[m.StreamId]; dup || len(c.outStreams) >= s.cfg.MaxStreams {
		c.smu.Unlock()
		c.sendFrame(closeFrame(m.StreamId, "stream limit"))
		return
	}
	l := &link{client: c, clientID: m.StreamId, endpoint: ep}
	c.outStreams[m.StreamId] = l
	c.smu.Unlock()

	ep.smu.Lock()
	if len(ep.inStreams) >= s.cfg.MaxStreams*16 {
		ep.smu.Unlock()
		c.smu.Lock()
		delete(c.outStreams, m.StreamId)
		c.smu.Unlock()
		c.sendFrame(closeFrame(m.StreamId, "endpoint busy"))
		return
	}
	l.inID = (s.nextStream.Add(1) & ^uint32(InboundStreamBit)) | InboundStreamBit
	ep.inStreams[l.inID] = l
	ep.countInboundLocked(c.netKey, 1)
	ep.smu.Unlock()
	ep.hostBudget()

	ep.sendFrame(&wire.RelayFrame{Kind: &wire.RelayFrame_StreamOpened{StreamOpened: &wire.StreamOpened{
		StreamId: l.inID, EndpointKey: m.EndpointKey,
	}}})
	c.sendFrame(&wire.RelayFrame{Kind: &wire.RelayFrame_StreamOpened{StreamOpened: &wire.StreamOpened{
		StreamId: m.StreamId, EndpointKey: m.EndpointKey,
	}}})
}

func closeFrame(id uint32, reason string) *wire.RelayFrame {
	return &wire.RelayFrame{Kind: &wire.RelayFrame_StreamClose{StreamClose: &wire.StreamClose{StreamId: id, Reason: reason}}}
}

func (c *conn) lookup(id uint32) *link {
	c.smu.Lock()
	defer c.smu.Unlock()
	if id&InboundStreamBit != 0 {
		return c.inStreams[id]
	}
	return c.outStreams[id]
}

func (c *conn) streamData(m *wire.StreamData) {
	if uint64(len(m.Data)) > uint64(c.srv.cfg.MaxChunk) {
		c.sendError(0, wire.ErrorCode_ERROR_CODE_TOO_LARGE, "stream chunk too large")
		return
	}
	l := c.lookup(m.StreamId)
	if l == nil || l.closed.Load() {
		return
	}
	var peer *conn
	var id uint32
	if c == l.client && m.StreamId == l.clientID {
		peer, id = l.endpoint, l.inID
	} else {
		peer, id = l.client, l.clientID
	}
	peer.sendFrame(&wire.RelayFrame{Kind: &wire.RelayFrame_StreamData{StreamData: &wire.StreamData{StreamId: id, Data: m.Data}}})
}

func (c *conn) streamClose(m *wire.StreamClose) {
	if l := c.lookup(m.StreamId); l != nil {
		c.srv.closeLink(l, c, m.Reason)
	}
}

// closeLink tears down a stream and tells whichever side didn't close it.
func (s *Server) closeLink(l *link, closer *conn, reason string) {
	if !l.closed.CompareAndSwap(false, true) {
		return
	}
	l.client.smu.Lock()
	delete(l.client.outStreams, l.clientID)
	l.client.smu.Unlock()
	l.endpoint.smu.Lock()
	if _, ok := l.endpoint.inStreams[l.inID]; ok {
		delete(l.endpoint.inStreams, l.inID)
		l.endpoint.countInboundLocked(l.client.netKey, -1)
	}
	l.endpoint.smu.Unlock()
	l.endpoint.hostBudget()
	if closer != l.client {
		l.client.sendFrame(closeFrame(l.clientID, reason))
	}
	if closer != l.endpoint {
		l.endpoint.sendFrame(closeFrame(l.inID, reason))
	}
}
