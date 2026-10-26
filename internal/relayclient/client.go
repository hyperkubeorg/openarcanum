// Package relayclient is how Go nodes (community servers, tests) attach to a
// relay: publish/subscribe with chunking, endpoint registration, and streams.
package relayclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/fanout"
	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/pow"
	"github.com/hyperkubeorg/openarcanum/internal/relayid"
	"github.com/hyperkubeorg/openarcanum/internal/relaymod"
	"github.com/hyperkubeorg/openarcanum/internal/version"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// ErrClosed is returned once the relay connection is gone.
var ErrClosed = errors.New("relayclient: connection closed")

// Handler receives each fully reassembled message published to a topic.
type Handler func(topic, data []byte)

// Client is one WebSocket connection to a relay.
type Client struct {
	URL     string
	ws      *websocket.Conn
	hello   *wire.Hello
	version uint32
	keys    *KeyBook

	ctx     context.Context
	cancel  context.CancelFunc
	errOnce sync.Once
	err     error

	wmu sync.Mutex

	nextReq atomic.Uint32
	pmu     sync.Mutex
	pending map[uint32]chan *wire.RelayFrame

	smu      sync.RWMutex
	handlers map[string]Handler

	// Messages being reassembled (read loop only): by sender and id, how
	// many each sender has, and the bytes they hold together.
	partial      map[partialKey]*partialMsg
	partialBy    map[uint64]int
	partialBytes int

	nextMsg    atomic.Uint64
	nextStream atomic.Uint32
	stmu       sync.Mutex
	streams    map[uint32]*Stream
	incoming   chan *Stream
	opening    map[uint32]chan error

	// StreamBufferBytes caps unread data per stream before the stream is reset.
	StreamBufferBytes int
	// PartialBytes caps what partly received messages hold together; past
	// it the oldest are dropped (the relay picks max_message, and nothing
	// makes it finish a message it started).
	PartialBytes int
}

// maxMessage is the protocol's ceiling for one message (25 MiB,
// relay.ProtocolMaxMessage); a relay's Hello can't raise it.
const maxMessage = 25 << 20

// maxPartials bounds the messages being reassembled, in all and from one
// sender (a relay allows a publisher max_partial in flight).
const (
	maxPartials         = 1024
	maxPartialsBySender = 64
)

type partialKey struct {
	sender, id uint64
}

type partialMsg struct {
	topic []byte
	next  uint32
	buf   []byte
	at    time.Time
}

// Dial connects, reads the relay's Hello, picks the highest protocol version
// both speak, solves the proof-of-work if the relay asks for one, and sends
// Start (docs/protocol.md §2 "Starting").
func Dial(ctx context.Context, url string) (*Client, error) {
	return DialVersions(ctx, url, version.Supported())
}

// DialVersions is Dial speaking only the given protocol versions (tests of
// version negotiation).
func DialVersions(ctx context.Context, url string, versions []uint32) (*Client, error) {
	return dial(ctx, url, versions, http.DefaultClient)
}

// DialHTTP is Dial through hc, whose transport decides how the connection
// is made (a source address, a proxy).
func DialHTTP(ctx context.Context, url string, hc *http.Client) (*Client, error) {
	return dial(ctx, url, version.Supported(), hc)
}

func dial(ctx context.Context, url string, versions []uint32, hc *http.Client) (*Client, error) {
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(dctx, url, &websocket.DialOptions{HTTPClient: hc})
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(64 << 20)
	f, err := readFrame(dctx, ws)
	if err != nil || f.GetHello() == nil {
		ws.CloseNow()
		return nil, errors.New("relayclient: relay did not send hello")
	}
	hello := f.GetHello()
	// The relay proves its key for this connection (docs/protocol.md §2
	// "Relay identity"); what this client signs is bound to it.
	if err := relayid.VerifyHello(hello); err != nil {
		ws.Close(websocket.StatusPolicyViolation, "relay identity")
		return nil, fmt.Errorf("relayclient: %w", err)
	}
	hello.MaxMessage = min(hello.MaxMessage, maxMessage)
	chosen, err := version.Negotiate(versions, hello.Versions)
	if err != nil {
		ws.Close(websocket.StatusNormalClosure, "no common protocol version")
		return nil, fmt.Errorf("relayclient: %w", err)
	}
	if hello.PowBits > pow.MaxBits {
		ws.Close(websocket.StatusNormalClosure, "proof-of-work too hard")
		return nil, fmt.Errorf("relayclient: relay asks for a %d-bit proof-of-work (at most %d)", hello.PowBits, pow.MaxBits)
	}
	// The window is the relay's; solving must finish inside it.
	sctx, scancel := context.WithTimeout(dctx, time.Duration(hello.StartWindowMs)*time.Millisecond)
	counter, err := pow.Solve(sctx, hello.Nonce, hello.PowBits)
	scancel()
	if err != nil {
		ws.CloseNow()
		return nil, fmt.Errorf("relayclient: proof-of-work: %w", err)
	}
	start, _ := proto.Marshal(&wire.RelayFrame{Kind: &wire.RelayFrame_Start{Start: &wire.Start{Version: chosen, Pow: counter, Req: 1}}})
	if err := ws.Write(dctx, websocket.MessageBinary, start); err != nil {
		ws.CloseNow()
		return nil, err
	}
	reply, err := readFrame(dctx, ws)
	if err != nil {
		ws.CloseNow()
		return nil, fmt.Errorf("relayclient: relay closed during start: %w", err)
	}
	if e := reply.GetError(); e != nil {
		ws.CloseNow()
		return nil, &StartError{Code: e.Code, Message: e.Message}
	}
	if reply.GetAck().GetReq() != 1 {
		ws.CloseNow()
		return nil, errors.New("relayclient: relay did not acknowledge start")
	}
	cctx, ccancel := context.WithCancel(context.Background())
	c := &Client{
		URL:               url,
		ws:                ws,
		hello:             hello,
		version:           chosen,
		keys:              Keys,
		ctx:               cctx,
		cancel:            ccancel,
		pending:           make(map[uint32]chan *wire.RelayFrame),
		handlers:          make(map[string]Handler),
		partial:           make(map[partialKey]*partialMsg),
		partialBy:         make(map[uint64]int),
		streams:           make(map[uint32]*Stream),
		incoming:          make(chan *Stream, 256),
		opening:           make(map[uint32]chan error),
		StreamBufferBytes: 64 << 20,
		PartialBytes:      64 << 20,
	}
	go c.readLoop()
	return c, nil
}

// StartError is the relay's refusal of Start (version or proof-of-work).
type StartError struct {
	Code    wire.ErrorCode
	Message string
}

func (e *StartError) Error() string {
	return fmt.Sprintf("relay refused the connection: %s (%s)", e.Message, e.Code)
}

func readFrame(ctx context.Context, ws *websocket.Conn) (*wire.RelayFrame, error) {
	typ, data, err := ws.Read(ctx)
	if err != nil {
		return nil, err
	}
	var f wire.RelayFrame
	if typ != websocket.MessageBinary || proto.Unmarshal(data, &f) != nil {
		return nil, errors.New("relayclient: malformed frame")
	}
	return &f, nil
}

// Version is the protocol version chosen with the relay.
func (c *Client) Version() uint32 { return c.version }

// FanoutAuth hands the relay a fan-out token for this connection
// (internal/fanout), signed by key, valid for lifetime.
func (c *Client) FanoutAuth(ctx context.Context, key *identity.Key, lifetime time.Duration) error {
	b, err := c.Binding()
	if err != nil {
		return err
	}
	tok, err := fanout.Sign(key, b, time.Now().Add(lifetime))
	if err != nil {
		return err
	}
	return c.SendFanoutToken(ctx, tok)
}

// SendFanoutToken hands the relay an already signed token.
func (c *Client) SendFanoutToken(ctx context.Context, tok *wire.SignedFanoutToken) error {
	r := c.req()
	return c.request(ctx, r, &wire.RelayFrame{Kind: &wire.RelayFrame_FanoutAuth{FanoutAuth: &wire.FanoutAuth{Token: tok, Req: r}}})
}

// Hello returns the relay's greeting (limits, nonce, region).
func (c *Client) Hello() *wire.Hello { return c.hello }

// Done is closed when the connection ends.
func (c *Client) Done() <-chan struct{} { return c.ctx.Done() }

// Err reports why the connection ended.
func (c *Client) Err() error {
	if c.ctx.Err() == nil {
		return nil
	}
	return c.err
}

// Close ends the connection.
func (c *Client) Close() { c.shutdown(ErrClosed) }

func (c *Client) shutdown(err error) {
	c.errOnce.Do(func() { c.err = err })
	c.cancel()
	c.ws.Close(websocket.StatusNormalClosure, "")
	c.stmu.Lock()
	streams := c.streams
	c.streams = map[uint32]*Stream{}
	for _, ch := range c.opening {
		select {
		case ch <- ErrClosed:
		default:
		}
	}
	c.stmu.Unlock()
	for _, s := range streams {
		s.remoteClose("relay connection closed")
	}
}

func (c *Client) write(f *wire.RelayFrame) error {
	b, err := proto.Marshal(f)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.ctx.Err() != nil {
		return ErrClosed
	}
	ctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
	defer cancel()
	if err := c.ws.Write(ctx, websocket.MessageBinary, b); err != nil {
		c.shutdown(err)
		return err
	}
	return nil
}

func (c *Client) request(ctx context.Context, req uint32, f *wire.RelayFrame) error {
	_, err := c.roundTrip(ctx, req, f)
	return err
}

// roundTrip sends a request and returns the relay's answer to it.
func (c *Client) roundTrip(ctx context.Context, req uint32, f *wire.RelayFrame) (*wire.RelayFrame, error) {
	ch := make(chan *wire.RelayFrame, 1)
	c.pmu.Lock()
	c.pending[req] = ch
	c.pmu.Unlock()
	defer func() {
		c.pmu.Lock()
		delete(c.pending, req)
		c.pmu.Unlock()
	}()
	if err := c.write(f); err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		if e := r.GetError(); e != nil {
			return nil, &RelayError{Code: e.Code, Message: e.Message}
		}
		return r, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.ctx.Done():
		return nil, ErrClosed
	}
}

// RelayError is the relay's Error answer to a request.
type RelayError struct {
	Code    wire.ErrorCode
	Message string
}

func (e *RelayError) Error() string { return fmt.Sprintf("relay: %s (%s)", e.Message, e.Code) }

// DedicatedTo is the community server key the relay is dedicated to (empty:
// an ordinary relay; docs/protocol.md §2 "Dedicated relays").
func (c *Client) DedicatedTo() []byte { return c.hello.GetDedicatedTo() }

// Moderate sends a moderation command, signed by key for this connection,
// to the relay dedicated to key (internal/relaymod), and returns its result.
func (c *Client) Moderate(ctx context.Context, key *identity.Key, cmd *wire.ModerationCommand) (*wire.ModerationResult, error) {
	b, err := c.Binding()
	if err != nil {
		return nil, err
	}
	sc, err := relaymod.Sign(key, b, time.Now(), cmd)
	if err != nil {
		return nil, err
	}
	return c.SendModeration(ctx, sc)
}

// SendModeration sends an already signed moderation command.
func (c *Client) SendModeration(ctx context.Context, sc *wire.SignedModerationCommand) (*wire.ModerationResult, error) {
	r := c.req()
	f, err := c.roundTrip(ctx, r, &wire.RelayFrame{Kind: &wire.RelayFrame_Moderation{Moderation: &wire.RelayModeration{Command: sc, Req: r}}})
	if err != nil {
		return nil, err
	}
	res := f.GetModerationResult()
	if res == nil {
		return nil, errors.New("relayclient: relay answered a moderation command with an Ack")
	}
	return res, nil
}

// Nonce is the relay's Hello nonce for this connection.
func (c *Client) Nonce() []byte { return c.hello.GetNonce() }

func (c *Client) req() uint32 {
	for {
		if r := c.nextReq.Add(1); r != 0 {
			return r
		}
	}
}

// CallUpdate sends a signed moderator control to the relay's SFU
// (community servers only; docs/protocol.md §12).
func (c *Client) CallUpdate(ctl *wire.SignedCallControl) error {
	return c.write(&wire.RelayFrame{Kind: &wire.RelayFrame_CallUpdate{CallUpdate: &wire.CallUpdate{Control: ctl}}})
}

// Calls reports whether the relay carries calls.
func (c *Client) Calls() bool { return c.hello != nil && c.hello.Calls }

// Subscribe registers h for messages on topic.
func (c *Client) Subscribe(ctx context.Context, topic []byte, h Handler) error {
	return c.SubscribeReplay(ctx, topic, h, 0)
}

// SubscribeReplay is Subscribe that also asks for cacheable messages
// published in the last `replay` (if the relay has a chunk cache).
func (c *Client) SubscribeReplay(ctx context.Context, topic []byte, h Handler, replay time.Duration) error {
	c.smu.Lock()
	c.handlers[string(topic)] = h
	c.smu.Unlock()
	r := c.req()
	return c.request(ctx, r, &wire.RelayFrame{Kind: &wire.RelayFrame_Subscribe{Subscribe: &wire.Subscribe{
		Topic: topic, Req: r, ReplayMs: uint32(replay.Milliseconds()),
	}}})
}

// Unsubscribe stops delivery for topic.
func (c *Client) Unsubscribe(ctx context.Context, topic []byte) error {
	c.smu.Lock()
	delete(c.handlers, string(topic))
	c.smu.Unlock()
	r := c.req()
	return c.request(ctx, r, &wire.RelayFrame{Kind: &wire.RelayFrame_Unsubscribe{Unsubscribe: &wire.Unsubscribe{Topic: topic, Req: r}}})
}

// Publish sends data to topic, split into chunks no larger than the relay's
// max_chunk.
func (c *Client) Publish(topic, data []byte) error { return c.publish(topic, data, false) }

// PublishCacheable is Publish for end-to-end encrypted payloads the relay may
// keep in its chunk cache.
func (c *Client) PublishCacheable(topic, data []byte) error { return c.publish(topic, data, true) }

func (c *Client) publish(topic, data []byte, cacheable bool) error {
	if uint64(len(data)) > c.hello.MaxMessage {
		return fmt.Errorf("relayclient: message of %d bytes exceeds relay limit %d", len(data), c.hello.MaxMessage)
	}
	id := c.nextMsg.Add(1)
	chunk := int(c.hello.MaxChunk)
	seq := uint32(0)
	for off := 0; ; off += chunk {
		end := min(off+chunk, len(data))
		final := end == len(data)
		if err := c.write(&wire.RelayFrame{Kind: &wire.RelayFrame_Publish{Publish: &wire.Publish{
			Topic: topic, MessageId: id, Seq: seq, Final: final, Data: data[off:end], Cacheable: cacheable,
		}}}); err != nil {
			return err
		}
		if final {
			return nil
		}
		seq++
	}
}

// RegisterEndpoint makes this connection the stream endpoint for key. The
// signature is bound to the relay's key, the URL it was dialled at and this
// connection (Binding).
func (c *Client) RegisterEndpoint(ctx context.Context, key *identity.Key) error {
	b, err := c.Binding()
	if err != nil {
		return err
	}
	r := c.req()
	return c.request(ctx, r, &wire.RelayFrame{Kind: &wire.RelayFrame_RegisterEndpoint{RegisterEndpoint: &wire.RegisterEndpoint{
		EndpointKey: key.Public,
		Signature:   key.Sign(identity.CtxRelayEndpoint, b.Bytes()),
		Url:         b.URL,
		Req:         r,
	}}})
}

// Accept waits for the next stream opened to this endpoint.
func (c *Client) Accept(ctx context.Context) (*Stream, error) {
	select {
	case s := <-c.incoming:
		return s, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.ctx.Done():
		return nil, ErrClosed
	}
}

// OpenStream opens a byte stream to the endpoint registered for key.
func (c *Client) OpenStream(ctx context.Context, endpointKey []byte) (*Stream, error) {
	id := c.nextStream.Add(1) & 0x7fffffff
	s := c.newStream(id)
	ch := make(chan error, 1)
	c.stmu.Lock()
	c.streams[id] = s
	c.opening[id] = ch
	c.stmu.Unlock()
	defer func() {
		c.stmu.Lock()
		delete(c.opening, id)
		c.stmu.Unlock()
	}()
	if err := c.write(&wire.RelayFrame{Kind: &wire.RelayFrame_OpenStream{OpenStream: &wire.OpenStream{EndpointKey: endpointKey, StreamId: id}}}); err != nil {
		return nil, err
	}
	select {
	case err := <-ch:
		if err != nil {
			return nil, err
		}
		return s, nil
	case <-ctx.Done():
		s.Close("cancelled")
		return nil, ctx.Err()
	}
}

func (c *Client) readLoop() {
	for {
		typ, data, err := c.ws.Read(c.ctx)
		if err != nil {
			c.shutdown(err)
			return
		}
		if typ != websocket.MessageBinary {
			continue
		}
		var f wire.RelayFrame
		if proto.Unmarshal(data, &f) != nil {
			continue
		}
		c.dispatch(&f)
	}
}

func (c *Client) dispatch(f *wire.RelayFrame) {
	switch k := f.Kind.(type) {
	case *wire.RelayFrame_Ack:
		c.resolve(k.Ack.Req, f)
	case *wire.RelayFrame_ModerationResult:
		c.resolve(k.ModerationResult.Req, f)
	case *wire.RelayFrame_Error:
		if k.Error.Req != 0 {
			c.resolve(k.Error.Req, f)
		}
	case *wire.RelayFrame_Deliver:
		c.deliver(k.Deliver)
	case *wire.RelayFrame_StreamOpened:
		id := k.StreamOpened.StreamId
		if id&0x80000000 != 0 {
			c.stmu.Lock()
			if _, dup := c.streams[id]; dup || c.ctx.Err() != nil {
				// A relay must not reuse a live stream id; replacing the
				// stream would orphan the one already handed to Accept.
				c.stmu.Unlock()
				return
			}
			s := c.newStream(id)
			c.streams[id] = s
			c.stmu.Unlock()
			select {
			case c.incoming <- s:
			default:
				s.Close("endpoint backlog full")
			}
			return
		}
		c.stmu.Lock()
		ch := c.opening[id]
		c.stmu.Unlock()
		if ch != nil {
			// Never block the read loop: a repeated StreamOpened (or one after
			// a StreamClose) finds the one-slot channel already answered.
			select {
			case ch <- nil:
			default:
			}
		}
	case *wire.RelayFrame_StreamData:
		c.stmu.Lock()
		s := c.streams[k.StreamData.StreamId]
		c.stmu.Unlock()
		if s != nil {
			s.push(k.StreamData.Data)
		}
	case *wire.RelayFrame_StreamClose:
		id := k.StreamClose.StreamId
		c.stmu.Lock()
		s := c.streams[id]
		delete(c.streams, id)
		ch := c.opening[id]
		c.stmu.Unlock()
		if ch != nil {
			select {
			case ch <- fmt.Errorf("relayclient: stream refused: %s", k.StreamClose.Reason):
			default:
			}
		}
		if s != nil {
			s.remoteClose(k.StreamClose.Reason)
		}
	}
}

func (c *Client) resolve(req uint32, f *wire.RelayFrame) {
	c.pmu.Lock()
	ch := c.pending[req]
	c.pmu.Unlock()
	if ch != nil {
		ch <- f
	}
}

func (c *Client) deliver(d *wire.Deliver) {
	key := partialKey{d.Sender, d.MessageId}
	p := c.partial[key]
	if p == nil {
		if d.Seq != 0 || c.partialBy[d.Sender] >= maxPartialsBySender {
			return
		}
		if len(c.partial) >= maxPartials {
			c.gcPartial()
		}
		for len(c.partial) >= maxPartials {
			c.dropPartial(c.oldestPartial(key))
		}
		p = &partialMsg{topic: d.Topic, at: time.Now()}
		c.partial[key] = p
		c.partialBy[d.Sender]++
	}
	// Every chunk belongs to the topic the first one named.
	if d.Seq != p.next || !bytes.Equal(d.Topic, p.topic) || uint64(len(p.buf)+len(d.Data)) > c.hello.MaxMessage {
		c.dropPartial(key)
		return
	}
	for c.partialBytes+len(d.Data) > c.PartialBytes && len(c.partial) > 1 {
		c.dropPartial(c.oldestPartial(key))
	}
	if c.partialBytes+len(d.Data) > c.PartialBytes {
		c.dropPartial(key)
		return
	}
	p.next++
	p.buf = append(p.buf, d.Data...)
	c.partialBytes += len(d.Data)
	if !d.Final {
		return
	}
	c.dropPartial(key)
	c.smu.RLock()
	h := c.handlers[string(p.topic)]
	c.smu.RUnlock()
	if h != nil {
		h(p.topic, p.buf)
	}
}

func (c *Client) dropPartial(key partialKey) {
	p := c.partial[key]
	if p == nil {
		return
	}
	delete(c.partial, key)
	c.partialBytes -= len(p.buf)
	if c.partialBy[key.sender]--; c.partialBy[key.sender] <= 0 {
		delete(c.partialBy, key.sender)
	}
}

// oldestPartial is the key of the partial message started longest ago,
// other than except.
func (c *Client) oldestPartial(except partialKey) partialKey {
	var oldest partialKey
	var at time.Time
	for k, p := range c.partial {
		if k != except && (at.IsZero() || p.at.Before(at)) {
			oldest, at = k, p.at
		}
	}
	return oldest
}

func (c *Client) gcPartial() {
	cutoff := time.Now().Add(-2 * time.Minute)
	for k, p := range c.partial {
		if p.at.Before(cutoff) {
			c.dropPartial(k)
		}
	}
}

// ---------------------------------------------------------------- streams

// Stream is a message-preserving byte stream through the relay. Each Send is
// delivered to the peer as one Recv.
type Stream struct {
	c  *Client
	ID uint32

	mu       sync.Mutex
	queue    [][]byte
	buffered int
	notify   chan struct{}
	closed   bool
	reason   string
}

func (c *Client) newStream(id uint32) *Stream {
	return &Stream{c: c, ID: id, notify: make(chan struct{}, 1)}
}

// Client is the relay connection the stream runs on.
func (s *Stream) Client() *Client { return s.c }

// MaxChunk is the largest payload one Send may carry.
func (s *Stream) MaxChunk() int { return int(s.c.hello.MaxChunk) }

// Send writes one message to the peer.
func (s *Stream) Send(data []byte) error {
	s.mu.Lock()
	closed, reason := s.closed, s.reason
	s.mu.Unlock()
	if closed {
		return fmt.Errorf("relayclient: stream closed: %s", reason)
	}
	if len(data) > s.MaxChunk() {
		return fmt.Errorf("relayclient: stream chunk of %d bytes exceeds %d", len(data), s.MaxChunk())
	}
	return s.c.write(&wire.RelayFrame{Kind: &wire.RelayFrame_StreamData{StreamData: &wire.StreamData{StreamId: s.ID, Data: data}}})
}

// Recv returns the next message from the peer.
func (s *Stream) Recv(ctx context.Context) ([]byte, error) {
	for {
		s.mu.Lock()
		if len(s.queue) > 0 {
			b := s.queue[0]
			s.queue[0] = nil
			s.queue = s.queue[1:]
			s.buffered -= len(b)
			s.mu.Unlock()
			return b, nil
		}
		if s.closed {
			reason := s.reason
			s.mu.Unlock()
			return nil, fmt.Errorf("relayclient: stream closed: %s", reason)
		}
		s.mu.Unlock()
		select {
		case <-s.notify:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (s *Stream) push(b []byte) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if s.buffered+len(b) > s.c.StreamBufferBytes {
		s.mu.Unlock()
		s.Close("receiver overflow")
		return
	}
	s.queue = append(s.queue, b)
	s.buffered += len(b)
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *Stream) remoteClose(reason string) {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.reason = reason
	}
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

// Close ends the stream and tells the peer.
func (s *Stream) Close(reason string) {
	s.mu.Lock()
	already := s.closed
	s.closed = true
	if s.reason == "" {
		s.reason = reason
	}
	s.mu.Unlock()
	s.c.stmu.Lock()
	delete(s.c.streams, s.ID)
	s.c.stmu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
	if !already {
		_ = s.c.write(&wire.RelayFrame{Kind: &wire.RelayFrame_StreamClose{StreamClose: &wire.StreamClose{StreamId: s.ID, Reason: reason}}})
	}
}

// Closed reports whether the stream has ended.
func (s *Stream) Closed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}
