package relay_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/fanout"
	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/pow"
	"github.com/hyperkubeorg/openarcanum/internal/relay"
	"github.com/hyperkubeorg/openarcanum/internal/relayclient"
	"github.com/hyperkubeorg/openarcanum/internal/version"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// rawHello dials by hand and returns the connection and its Hello, without
// starting.
func rawHello(t *testing.T, url string) (*websocket.Conn, *wire.Hello) {
	t.Helper()
	ws, _, err := websocket.Dial(ctx(t), url, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.CloseNow() })
	f := readRaw(t, ws)
	if f.GetHello() == nil {
		t.Fatal("no hello")
	}
	return ws, f.GetHello()
}

func readRaw(t *testing.T, ws *websocket.Conn) *wire.RelayFrame {
	t.Helper()
	_, b, err := ws.Read(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	var f wire.RelayFrame
	if err := proto.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return &f
}

func writeRaw(t *testing.T, ws *websocket.Conn, f *wire.RelayFrame) {
	t.Helper()
	b, _ := proto.Marshal(f)
	if err := ws.Write(ctx(t), websocket.MessageBinary, b); err != nil {
		t.Fatal(err)
	}
}

func startFrame(v uint32, counter []byte) *wire.RelayFrame {
	return &wire.RelayFrame{Kind: &wire.RelayFrame_Start{Start: &wire.Start{Version: v, Pow: counter, Req: 1}}}
}

// expectRefusal reads an Error with code, then the connection must close.
func expectRefusal(t *testing.T, ws *websocket.Conn, code wire.ErrorCode) {
	t.Helper()
	f := readRaw(t, ws)
	if f.GetError().GetCode() != code {
		t.Fatalf("expected %v, got %v", code, f)
	}
	if _, _, err := ws.Read(ctx(t)); err == nil {
		t.Fatal("relay kept the connection open after refusing it")
	}
}

// ---------------------------------------------------------------- versions

func TestVersionNegotiation(t *testing.T) {
	// A relay one version ahead (speaking 1 and 2) still serves clients that
	// speak only 1, and clients that speak 2 get 2.
	srv, url := start(t, nil)
	srv.SetVersions(version.SupportedFor(2))
	c1, err := relayclient.DialVersions(ctx(t), url, []uint32{1})
	if err != nil {
		t.Fatal(err)
	}
	defer c1.Close()
	if c1.Version() != 1 {
		t.Fatalf("v1 client got version %d", c1.Version())
	}
	c2, err := relayclient.DialVersions(ctx(t), url, version.SupportedFor(2))
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if c2.Version() != 2 {
		t.Fatalf("v2 client got version %d", c2.Version())
	}
	// The v1 client still works end to end with the v2 relay.
	b := newInbox()
	if err := c2.Subscribe(ctx(t), []byte("t"), b.handler); err != nil {
		t.Fatal(err)
	}
	if err := c1.Publish([]byte("t"), []byte("hi")); err != nil {
		t.Fatal(err)
	}
	b.wait(t, 1)

	// Version 0 and unknown versions are refused by the relay.
	for _, v := range []uint32{0, 3, 99} {
		ws, _ := rawHello(t, url)
		writeRaw(t, ws, startFrame(v, nil))
		expectRefusal(t, ws, wire.ErrorCode_ERROR_CODE_UNSUPPORTED_VERSION)
	}
}

func TestVersionMismatchSaysWhoIsOutdated(t *testing.T) {
	srv, url := start(t, nil)
	srv.SetVersions([]uint32{2, 3})
	_, err := relayclient.DialVersions(ctx(t), url, []uint32{1})
	var me *version.MismatchError
	if !errors.As(err, &me) || me.PeerOutdated || !strings.Contains(err.Error(), "update OpenArcanum") {
		t.Fatalf("an old client must be told to update, got %v", err)
	}
	srv.SetVersions([]uint32{1})
	_, err = relayclient.DialVersions(ctx(t), url, []uint32{2, 3})
	if !errors.As(err, &me) || !me.PeerOutdated || !strings.Contains(err.Error(), "outdated") {
		t.Fatalf("an old relay must be reported as outdated, got %v", err)
	}
}

func TestFramesBeforeStartAreRefused(t *testing.T) {
	_, url := start(t, nil)
	ws, _ := rawHello(t, url)
	writeRaw(t, ws, &wire.RelayFrame{Kind: &wire.RelayFrame_Subscribe{Subscribe: &wire.Subscribe{Topic: []byte("t"), Req: 1}}})
	expectRefusal(t, ws, wire.ErrorCode_ERROR_CODE_UNSUPPORTED_VERSION)
}

// ---------------------------------------------------------------- proof-of-work

func TestProofOfWorkAlways(t *testing.T) {
	srv, url := start(t, func(c *relay.Config) { c.PowMode = "always"; c.PowBits = 16 })
	// The client solves it.
	c := dial(t, url)
	if c.Hello().PowBits != 16 {
		t.Fatalf("hello asked for %d bits", c.Hello().PowBits)
	}
	// Missing proof.
	ws, _ := rawHello(t, url)
	writeRaw(t, ws, startFrame(1, nil))
	expectRefusal(t, ws, wire.ErrorCode_ERROR_CODE_POW_REQUIRED)
	// A wrong proof: a counter that fails this connection's challenge.
	ws, h := rawHello(t, url)
	bad := []byte{0, 0, 0, 0, 0, 0, 0, 0}
	for i := 0; pow.Verify(h.Nonce, bad, 16); i++ {
		bad[7] = byte(i + 1)
	}
	writeRaw(t, ws, startFrame(1, bad))
	expectRefusal(t, ws, wire.ErrorCode_ERROR_CODE_POW_REQUIRED)
	if srv.Stats().PowRefused.Load() < 2 {
		t.Fatalf("refusals not counted: %d", srv.Stats().PowRefused.Load())
	}
}

func TestProofOfWorkCantBeReplayed(t *testing.T) {
	_, url := start(t, func(c *relay.Config) { c.PowMode = "always"; c.PowBits = 20 })
	wsA, hA := rawHello(t, url)
	counter, err := pow.Solve(ctx(t), hA.Nonce, hA.PowBits)
	if err != nil {
		t.Fatal(err)
	}
	wsB, hB := rawHello(t, url)
	if bytes.Equal(hA.Nonce, hB.Nonce) {
		t.Fatal("two connections got the same challenge")
	}
	if pow.Verify(hB.Nonce, counter, hB.PowBits) {
		t.Skip("the counter happens to solve both challenges (chance 2^-20)")
	}
	writeRaw(t, wsB, startFrame(1, counter))
	expectRefusal(t, wsB, wire.ErrorCode_ERROR_CODE_POW_REQUIRED)
	// It still works on the connection it was made for.
	writeRaw(t, wsA, startFrame(1, counter))
	if f := readRaw(t, wsA); f.GetAck().GetReq() != 1 {
		t.Fatalf("valid proof refused: %v", f)
	}
}

func TestStartWindowExpires(t *testing.T) {
	srv, url := start(t, func(c *relay.Config) {
		c.PowMode = "always"
		c.PowBits = 8
		c.StartWindow.Duration = time.Second
	})
	ws, h := rawHello(t, url)
	if h.StartWindowMs != 1000 {
		t.Fatalf("hello start window %d", h.StartWindowMs)
	}
	counter, _ := pow.Solve(ctx(t), h.Nonce, h.PowBits)
	time.Sleep(1500 * time.Millisecond)
	// The relay gave up: an Error, then the connection is gone, and a late
	// (even valid) Start doesn't revive it.
	_ = ws.Write(ctx(t), websocket.MessageBinary, func() []byte { b, _ := proto.Marshal(startFrame(1, counter)); return b }())
	f := readRaw(t, ws)
	if f.GetError().GetCode() != wire.ErrorCode_ERROR_CODE_POW_REQUIRED {
		t.Fatalf("expected the window error, got %v", f)
	}
	if _, _, err := ws.Read(ctx(t)); err == nil {
		t.Fatal("connection still open after the start window")
	}
	if srv.Stats().StartTimeouts.Load() != 1 {
		t.Fatalf("start timeouts %d", srv.Stats().StartTimeouts.Load())
	}
}

func TestProofOfWorkOnlyUnderLoad(t *testing.T) {
	// Trigger 1: open connections reach pow_load_connections.
	srv, url := start(t, func(c *relay.Config) {
		c.PowBits = 10
		c.PowLoadConnections = 3
		c.PowNewPerSecond = 1000
	})
	a, b := dial(t, url), dial(t, url)
	if a.Hello().PowBits != 0 || b.Hello().PowBits != 0 {
		t.Fatal("proof-of-work asked for while not under load")
	}
	c := dial(t, url)
	d := dial(t, url)
	if c.Hello().PowBits != 0 || d.Hello().PowBits != 10 {
		t.Fatalf("expected the 4th connection (3 open) to need proof-of-work: %d %d", c.Hello().PowBits, d.Hello().PowBits)
	}
	if srv.Stats().PowAsked.Load() != 1 {
		t.Fatalf("pow asked %d", srv.Stats().PowAsked.Load())
	}
	// Load goes away: new connections are free again.
	c.Close()
	d.Close()
	waitConns(t, srv, 2)
	if e := dial(t, url); e.Hello().PowBits != 0 {
		t.Fatal("proof-of-work still asked for after load dropped")
	}

	// Trigger 2: a burst of new connections.
	_, url2 := start(t, func(c *relay.Config) {
		c.PowBits = 10
		c.PowNewPerSecond = 5
	})
	asked := 0
	for range 8 {
		cl := dial(t, url2)
		if cl.Hello().PowBits > 0 {
			asked++
		}
		cl.Close()
	}
	if asked == 0 {
		t.Fatal("a burst of new connections never triggered proof-of-work")
	}

	// pow_mode off never asks.
	_, url3 := start(t, func(c *relay.Config) { c.PowMode = "off"; c.PowLoadConnections = 1 })
	for range 3 {
		if dial(t, url3).Hello().PowBits != 0 {
			t.Fatal("pow_mode off asked for proof-of-work")
		}
	}
}

func waitConns(t *testing.T, srv *relay.Server, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for srv.Connections() != n {
		if time.Now().After(deadline) {
			t.Fatalf("connections %d, want %d", srv.Connections(), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPowConfigBounds(t *testing.T) {
	for _, m := range []func(*relay.Config){
		func(c *relay.Config) { c.PowMode = "sometimes" },
		func(c *relay.Config) { c.PowBits = 0 },
		func(c *relay.Config) { c.PowBits = pow.MaxBits + 1 },
		func(c *relay.Config) { c.StartWindow.Duration = 100 * time.Millisecond },
		func(c *relay.Config) { c.PowLoadConnections = c.MaxConnections + 1 },
		func(c *relay.Config) { c.FanoutThreshold = -1 },
		func(c *relay.Config) { c.ChunkCacheBytes = 1 << 20; c.ChunkCacheTopicBytes = 2 << 20 },
		func(c *relay.Config) { c.HostFramesPerStream = -1 },
	} {
		cfg := relay.DefaultConfig()
		m(&cfg)
		if cfg.Validate() == nil {
			t.Errorf("accepted %+v", cfg)
		}
	}
}

// ---------------------------------------------------------------- fan-out tokens

func TestLargeFanoutNeedsToken(t *testing.T) {
	srv, url := start(t, func(c *relay.Config) { c.FanoutThreshold = 2 })
	issuer, _ := identity.Generate()
	other, _ := identity.Generate()
	topic := fanout.NewTopic(issuer.Public)

	boxes := make([]*inbox, 3)
	for i := range boxes {
		boxes[i] = newInbox()
		if err := dial(t, url).Subscribe(ctx(t), topic, boxes[i].handler); err != nil {
			t.Fatal(err)
		}
	}
	pub := dial(t, url)

	// No token: refused, nobody receives it.
	if err := pub.Publish(topic, []byte("spam")); err != nil {
		t.Fatal(err)
	}
	waitStat(t, func() int64 { return srv.Stats().FanoutRefused.Load() }, 1)

	// A token from a key the topic doesn't commit to doesn't help.
	if err := pub.FanoutAuth(ctx(t), other, 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := pub.Publish(topic, []byte("spam")); err != nil {
		t.Fatal(err)
	}
	waitStat(t, func() int64 { return srv.Stats().FanoutRefused.Load() }, 2)

	// The issuer's token: delivered to all three.
	if err := pub.FanoutAuth(ctx(t), issuer, 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := pub.Publish(topic, []byte("news")); err != nil {
		t.Fatal(err)
	}
	for _, b := range boxes {
		got := b.wait(t, 1)
		if len(got) != 1 || string(got[0]) != "news" {
			t.Fatalf("subscriber got %q", got)
		}
	}

	// A small topic needs no token.
	small := []byte("small-topic")
	sb := newInbox()
	if err := dial(t, url).Subscribe(ctx(t), small, sb.handler); err != nil {
		t.Fatal(err)
	}
	if err := dial(t, url).Publish(small, []byte("ok")); err != nil {
		t.Fatal(err)
	}
	sb.wait(t, 1)
}

// The checks run on a message's first chunk, so every later chunk must
// name the same topic: one that switches to a large topic is refused, and
// nothing of it reaches that topic's subscribers. A message refused on its
// first chunk stays refused, whatever topic its later chunks name.
func TestChunksStayOnTheirTopic(t *testing.T) {
	srv, url := start(t, func(c *relay.Config) { c.FanoutThreshold = 2 })
	issuer, _ := identity.Generate()
	big := fanout.NewTopic(issuer.Public)
	boxes := make([]*inbox, 3)
	for i := range boxes {
		boxes[i] = newInbox()
		if err := dial(t, url).Subscribe(ctx(t), big, boxes[i].handler); err != nil {
			t.Fatal(err)
		}
	}
	small := newInbox()
	if err := dial(t, url).Subscribe(ctx(t), []byte("small"), small.handler); err != nil {
		t.Fatal(err)
	}
	ws, hello := rawHello(t, url)
	writeRaw(t, ws, startFrame(hello.Versions[len(hello.Versions)-1], nil))
	if f := readRaw(t, ws); f.GetAck() == nil {
		t.Fatalf("start: %v", f)
	}
	publish := func(topic []byte, id uint64, seq uint32, final bool, data string) {
		writeRaw(t, ws, &wire.RelayFrame{Kind: &wire.RelayFrame_Publish{Publish: &wire.Publish{Topic: topic, MessageId: id, Seq: seq, Final: final, Data: []byte(data)}}})
	}
	// Started on the small topic, finished on the large one.
	publish([]byte("small"), 1, 0, false, "x")
	publish(big, 1, 1, true, "spam")
	if f := readRaw(t, ws); f.GetError().GetCode() != wire.ErrorCode_ERROR_CODE_BAD_FRAME {
		t.Fatalf("chunk on another topic: %v", f)
	}
	// Refused on the large topic, continued on the small one.
	publish(big, 2, 0, false, "spam")
	if f := readRaw(t, ws); f.GetError().GetCode() != wire.ErrorCode_ERROR_CODE_FANOUT_TOKEN_REQUIRED {
		t.Fatalf("large topic without a token: %v", f)
	}
	publish([]byte("small"), 2, 1, true, "y")
	// A whole message on the small topic still goes through: the two
	// above never did.
	publish([]byte("small"), 3, 0, false, "o")
	publish([]byte("small"), 3, 1, true, "k")
	if got := small.wait(t, 1); string(got[0]) != "ok" {
		t.Fatalf("small topic got %q", got)
	}
	for _, b := range boxes {
		b.mu.Lock()
		n := len(b.msgs)
		b.mu.Unlock()
		if n != 0 {
			t.Fatal("a chunk reached the large topic without a token")
		}
	}
	if n := srv.Stats().FanoutRefused.Load(); n != 1 {
		t.Fatalf("FanoutRefused = %d", n)
	}
}

func TestFanoutTokenChecks(t *testing.T) {
	_, url := start(t, nil)
	issuer, _ := identity.Generate()
	c := dial(t, url)
	// Expired.
	tok, _ := fanout.Sign(issuer, binding(t, c), time.Now().Add(-time.Second))
	if err := fanoutAuth(t, c, tok); err == nil || !strings.Contains(err.Error(), "BAD_TOKEN") {
		t.Fatalf("expired token: %v", err)
	}
	// Too long.
	tok, _ = fanout.Sign(issuer, binding(t, c), time.Now().Add(2*fanout.MaxTokenLifetime))
	if err := fanoutAuth(t, c, tok); err == nil {
		t.Fatal("accepted a token valid for too long")
	}
	// For another connection (replay).
	d := dial(t, url)
	tok, _ = fanout.Sign(issuer, binding(t, d), time.Now().Add(time.Minute))
	if err := fanoutAuth(t, c, tok); err == nil || !strings.Contains(err.Error(), "BAD_TOKEN") {
		t.Fatalf("token for another connection: %v", err)
	}
	if err := fanoutAuth(t, d, tok); err != nil {
		t.Fatalf("token refused on its own connection: %v", err)
	}
	// Bad signature.
	tok, _ = fanout.Sign(issuer, binding(t, c), time.Now().Add(time.Minute))
	tok.Signature[0] ^= 1
	if err := fanoutAuth(t, c, tok); err == nil {
		t.Fatal("accepted a forged token")
	}
}

// fanoutAuth sends a prepared token through the client's request plumbing.
func fanoutAuth(t *testing.T, c *relayclient.Client, tok *wire.SignedFanoutToken) error {
	t.Helper()
	return c.SendFanoutToken(ctx(t), tok)
}

func TestTopicCommitment(t *testing.T) {
	k, _ := identity.Generate()
	o, _ := identity.Generate()
	topic := fanout.NewTopic(k.Public)
	if !fanout.Commits(topic, k.Public) || fanout.Commits(topic, o.Public) {
		t.Fatal("commitment check wrong")
	}
	topic[3] ^= 1
	if fanout.Commits(topic, k.Public) {
		t.Fatal("altered topic still commits")
	}
	if fanout.Commits([]byte("short"), k.Public) {
		t.Fatal("short topic commits")
	}
}

func waitStat(t *testing.T, get func() int64, want int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for get() < want {
		if time.Now().After(deadline) {
			t.Fatalf("stat %d, want %d", get(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ---------------------------------------------------------------- chunk cache

func TestChunkCacheOffByDefault(t *testing.T) {
	srv, url := start(t, nil)
	pub := dial(t, url)
	if pub.Hello().CacheTtlMs != 0 {
		t.Fatal("hello advertises a cache that's off")
	}
	if err := pub.PublishCacheable([]byte("t"), []byte("x")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	b := newInbox()
	if err := dial(t, url).SubscribeReplay(ctx(t), []byte("t"), b.handler, time.Minute); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if len(b.msgs) != 0 || srv.CacheBytes() != 0 {
		t.Fatal("cache off but something was cached")
	}
}

func cacheRelay(t *testing.T, mutate func(*relay.Config)) (*relay.Server, string) {
	return start(t, func(c *relay.Config) {
		c.ChunkCacheBytes = 64 << 10
		c.ChunkCacheTopicBytes = 16 << 10
		c.ChunkCacheMaxMessage = 8 << 10
		c.MaxChunk = 1024
		if mutate != nil {
			mutate(c)
		}
	})
}

func TestChunkCacheReplaysOnlyCacheable(t *testing.T) {
	srv, url := cacheRelay(t, nil)
	pub := dial(t, url)
	if pub.Hello().CacheTtlMs != 60_000 {
		t.Fatalf("hello cache ttl %d", pub.Hello().CacheTtlMs)
	}
	topic := []byte("chan")
	big := bytes.Repeat([]byte{7}, 3000) // three chunks
	if err := pub.PublishCacheable(topic, big); err != nil {
		t.Fatal(err)
	}
	if err := pub.Publish(topic, []byte("not cacheable")); err != nil {
		t.Fatal(err)
	}
	if err := pub.PublishCacheable(topic, []byte("second")); err != nil {
		t.Fatal(err)
	}
	waitStat(t, func() int64 { return int64(srv.CacheBytes()) }, 3006)

	late := newInbox()
	if err := dial(t, url).SubscribeReplay(ctx(t), topic, late.handler, time.Minute); err != nil {
		t.Fatal(err)
	}
	got := late.wait(t, 2)
	if !bytes.Equal(got[0], big) || string(got[1]) != "second" {
		t.Fatalf("replay wrong: %d messages", len(got))
	}
	time.Sleep(100 * time.Millisecond)
	if len(late.wait(t, 2)) != 2 {
		t.Fatal("a message that wasn't cacheable was replayed")
	}
	// A subscriber that doesn't ask gets nothing old.
	plain := newInbox()
	if err := dial(t, url).Subscribe(ctx(t), topic, plain.handler); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if len(plain.msgs) != 0 {
		t.Fatal("replayed without being asked")
	}
}

func TestChunkCacheNeverHoldsStreams(t *testing.T) {
	srv, url := cacheRelay(t, nil)
	key, _ := identity.Generate()
	ep := dial(t, url)
	if err := ep.RegisterEndpoint(ctx(t), key); err != nil {
		t.Fatal(err)
	}
	cl := dial(t, url)
	st, err := cl.OpenStream(ctx(t), key.Public)
	if err != nil {
		t.Fatal(err)
	}
	in, err := ep.Accept(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		if err := st.Send([]byte("session bytes")); err != nil {
			t.Fatal(err)
		}
		if _, err := in.Recv(ctx(t)); err != nil {
			t.Fatal(err)
		}
	}
	if srv.CacheBytes() != 0 {
		t.Fatalf("stream data reached the cache (%d bytes)", srv.CacheBytes())
	}
}

func TestChunkCacheBounded(t *testing.T) {
	// One publisher may use the whole cache here (its share is tested in
	// TestChunkCachePerConnectionShare).
	srv, url := cacheRelay(t, func(c *relay.Config) { c.ChunkCacheConnBytes = c.ChunkCacheBytes })
	pub := dial(t, url)
	// Over the per-message cap: not cached.
	if err := pub.PublishCacheable([]byte("a"), make([]byte, 9<<10)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if srv.CacheBytes() != 0 {
		t.Fatal("cached a message over chunk_cache_max_message")
	}
	// Many topics and messages: never over the total or per-topic caps.
	for i := range 200 {
		topic := []byte{'t', byte(i % 10)}
		if err := pub.PublishCacheable(topic, make([]byte, 4<<10)); err != nil {
			t.Fatal(err)
		}
		if srv.CacheBytes() > 64<<10 {
			t.Fatalf("cache at %d bytes, over its cap", srv.CacheBytes())
		}
	}
	// A round trip on the publisher's connection: the relay has handled every
	// publish before it (frames on a connection are handled in order), so the
	// late subscriber below gets the cache only, nothing still in flight.
	if err := pub.Subscribe(ctx(t), []byte("barrier"), newInbox().handler); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if n := srv.CacheBytes(); n > 64<<10 || n == 0 {
		t.Fatalf("cache at %d bytes", n)
	}
	late := newInbox()
	if err := dial(t, url).SubscribeReplay(ctx(t), []byte{'t', 3}, late.handler, time.Minute); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	late.mu.Lock()
	n := len(late.msgs)
	late.mu.Unlock()
	if n == 0 || n*(4<<10) > 16<<10 {
		t.Fatalf("topic replayed %d messages; per-topic cap is 4", n)
	}
}

func TestChunkCacheExpires(t *testing.T) {
	srv, url := cacheRelay(t, func(c *relay.Config) { c.ChunkCacheTTL.Duration = time.Second })
	pub := dial(t, url)
	if err := pub.PublishCacheable([]byte("t"), []byte("soon gone")); err != nil {
		t.Fatal(err)
	}
	waitStat(t, func() int64 { return int64(srv.CacheBytes()) }, 1)
	time.Sleep(1200 * time.Millisecond)
	late := newInbox()
	if err := dial(t, url).SubscribeReplay(ctx(t), []byte("t"), late.handler, time.Minute); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if len(late.msgs) != 0 || srv.CacheBytes() != 0 {
		t.Fatal("expired message replayed")
	}
}

// ---------------------------------------------------------------- endpoint hosts

func TestHostBudgetScalesWithStreams(t *testing.T) {
	srv, url := start(t, func(c *relay.Config) {
		c.FramesPerSecond = 20
		c.FrameBurst = 20
		c.HostFramesPerStream = 50
		c.HostFramesPerSecond = 5000
		c.HostFrameBurst = 10000
	})
	key, _ := identity.Generate()
	ep := dial(t, url)
	if err := ep.RegisterEndpoint(ctx(t), key); err != nil {
		t.Fatal(err)
	}
	if r, b, _ := srv.FrameBudget(key.Public); r != 20 || b != 20 {
		t.Fatalf("host with no streams: %v/%v", r, b)
	}
	// Members on ten networks (the host is on 127.0.0.1).
	var ins []*relayclient.Stream
	for i := range 10 {
		cl := mustDialFrom(t, url, fmt.Sprintf("127.0.0.%d", 2+i))
		if _, err := cl.OpenStream(ctx(t), key.Public); err != nil {
			t.Fatal(err)
		}
		in, err := ep.Accept(ctx(t))
		if err != nil {
			t.Fatal(err)
		}
		ins = append(ins, in)
	}
	if r, b, _ := srv.FrameBudget(key.Public); r != 20+10*50 || b != 20+2*10*50 {
		t.Fatalf("host with 10 streams: %v/%v", r, b)
	}
	// 600 frames from the host go through without throttling (a plain
	// client would need about 30 s at 20 frames/s).
	t0 := time.Now()
	for i := range 600 {
		if err := ins[i%len(ins)].Send([]byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	// A round trip after them proves the relay has read them all.
	if err := ep.Subscribe(ctx(t), []byte("sync"), func(_, _ []byte) {}); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(t0); d > 3*time.Second {
		t.Fatalf("host throttled: 600 frames took %v", d)
	}
	// Streams closing shrink the budget again.
	for _, in := range ins {
		in.Close("done")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if r, _, _ := srv.FrameBudget(key.Public); r == 20 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("budget didn't shrink after streams closed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHostBudgetCapped(t *testing.T) {
	srv, url := start(t, func(c *relay.Config) {
		c.FramesPerSecond = 20
		c.FrameBurst = 20
		c.HostFramesPerStream = 50
		c.HostFramesPerSecond = 300
		c.HostFrameBurst = 400
	})
	key, _ := identity.Generate()
	ep := dial(t, url)
	if err := ep.RegisterEndpoint(ctx(t), key); err != nil {
		t.Fatal(err)
	}
	// Four streams from each of five networks: twenty counted streams.
	for i := range 20 {
		cl := mustDialFrom(t, url, fmt.Sprintf("127.0.0.%d", 2+i%5))
		if _, err := cl.OpenStream(ctx(t), key.Public); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		r, b, _ := srv.FrameBudget(key.Public)
		if r == 300 && b == 400 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("host budget %v/%v, want the 300/400 cap", r, b)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A client can't raise its own budget by registering a fresh key and
// opening streams to itself: streams from the host's own network don't
// count, nor more than four from any one network.
func TestHostBudgetNeedsMembersOnOtherNetworks(t *testing.T) {
	srv, url := start(t, func(c *relay.Config) {
		c.FramesPerSecond = 20
		c.FrameBurst = 20
		c.HostFramesPerStream = 50
		c.HostFramesPerSecond = 5000
		c.HostFrameBurst = 10000
	})
	key, _ := identity.Generate()
	ep := mustDialFrom(t, url, "127.0.0.1")
	if err := ep.RegisterEndpoint(ctx(t), key); err != nil {
		t.Fatal(err)
	}
	budget := func(want float64) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			r, _, _ := srv.FrameBudget(key.Public)
			if r == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("host budget %v, want %v", r, want)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	self := mustDialFrom(t, url, "127.0.0.1")
	for range 10 {
		if _, err := self.OpenStream(ctx(t), key.Public); err != nil {
			t.Fatal(err)
		}
	}
	budget(20)
	other := mustDialFrom(t, url, "127.0.0.2")
	var outs []*relayclient.Stream
	for range 10 {
		s, err := other.OpenStream(ctx(t), key.Public)
		if err != nil {
			t.Fatal(err)
		}
		outs = append(outs, s)
	}
	budget(20 + 4*50)
	// Closing streams beyond the four counted ones changes nothing; the
	// last ones shrink the budget.
	for _, s := range outs[:6] {
		s.Close("done")
	}
	time.Sleep(100 * time.Millisecond)
	budget(20 + 4*50)
	for _, s := range outs[6:] {
		s.Close("done")
	}
	budget(20)
}

func TestPlainClientKeepsConnectionBudget(t *testing.T) {
	_, url := start(t, func(c *relay.Config) {
		c.FramesPerSecond = 20
		c.FrameBurst = 20
		c.HostFramesPerStream = 50
		c.HostFramesPerSecond = 5000
	})
	key, _ := identity.Generate()
	ep := dial(t, url)
	if err := ep.RegisterEndpoint(ctx(t), key); err != nil {
		t.Fatal(err)
	}
	// A client with open streams is not a host: 60 frames at 20/s with a
	// burst of 20 take about two seconds.
	cl := dial(t, url)
	var outs []*relayclient.Stream
	for range 5 {
		s, err := cl.OpenStream(ctx(t), key.Public)
		if err != nil {
			t.Fatal(err)
		}
		outs = append(outs, s)
	}
	t0 := time.Now()
	for i := range 60 {
		if err := outs[i%len(outs)].Send([]byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := cl.Subscribe(ctx(t), []byte("sync"), func(_, _ []byte) {}); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(t0); d < 1500*time.Millisecond {
		t.Fatalf("plain client wasn't held to its budget: 60 frames in %v", d)
	}
}

// A message started while its topic was small doesn't carry on without a
// token once the topic has grown: the check runs on every chunk.
func TestFanoutTokenCheckedOnEveryChunk(t *testing.T) {
	srv, url := start(t, func(c *relay.Config) { c.FanoutThreshold = 2 })
	issuer, _ := identity.Generate()
	big := fanout.NewTopic(issuer.Public)
	first := newInbox()
	if err := dial(t, url).Subscribe(ctx(t), big, first.handler); err != nil {
		t.Fatal(err)
	}
	ws, hello := rawHello(t, url)
	writeRaw(t, ws, startFrame(hello.Versions[len(hello.Versions)-1], nil))
	if f := readRaw(t, ws); f.GetAck() == nil {
		t.Fatalf("start: %v", f)
	}
	publish := func(id uint64, seq uint32, final bool, data string) {
		writeRaw(t, ws, &wire.RelayFrame{Kind: &wire.RelayFrame_Publish{Publish: &wire.Publish{Topic: big, MessageId: id, Seq: seq, Final: final, Data: []byte(data)}}})
	}
	publish(1, 0, false, "sp")
	waitStat(t, func() int64 { return srv.Stats().Delivered.Load() }, 1)
	// Two more subscribers: the topic is large now.
	for range 2 {
		if err := dial(t, url).Subscribe(ctx(t), big, newInbox().handler); err != nil {
			t.Fatal(err)
		}
	}
	publish(1, 1, true, "am")
	if f := readRaw(t, ws); f.GetError().GetCode() != wire.ErrorCode_ERROR_CODE_FANOUT_TOKEN_REQUIRED {
		t.Fatalf("later chunk on a grown topic: %v", f)
	}
	waitStat(t, func() int64 { return srv.Stats().FanoutRefused.Load() }, 1)
}

// A message whose chunks stop coming for the start window is forgotten: a
// late chunk is refused, and the slot it held is free for a new message.
func TestStalledMessagesAgeOut(t *testing.T) {
	srv, url := start(t, func(c *relay.Config) {
		c.StartWindow.Duration = time.Second
		c.MaxPartial = 1
	})
	box := newInbox()
	if err := dial(t, url).Subscribe(ctx(t), []byte("t"), box.handler); err != nil {
		t.Fatal(err)
	}
	ws, hello := rawHello(t, url)
	writeRaw(t, ws, startFrame(hello.Versions[len(hello.Versions)-1], nil))
	if f := readRaw(t, ws); f.GetAck() == nil {
		t.Fatalf("start: %v", f)
	}
	publish := func(id uint64, seq uint32, final bool, data string) {
		writeRaw(t, ws, &wire.RelayFrame{Kind: &wire.RelayFrame_Publish{Publish: &wire.Publish{Topic: []byte("t"), MessageId: id, Seq: seq, Final: final, Data: []byte(data)}}})
	}
	publish(1, 0, false, "a")
	waitStat(t, func() int64 { return srv.Stats().Delivered.Load() }, 1)
	// The one slot is taken: a second message is refused.
	publish(2, 0, false, "b")
	if f := readRaw(t, ws); f.GetError().GetCode() != wire.ErrorCode_ERROR_CODE_LIMIT {
		t.Fatalf("second message in flight: %v", f)
	}
	time.Sleep(1500 * time.Millisecond)
	// Past the window the first message is gone: its late chunk is
	// refused and a new message takes the slot.
	publish(1, 1, true, "b")
	if f := readRaw(t, ws); f.GetError().GetCode() != wire.ErrorCode_ERROR_CODE_LIMIT {
		t.Fatalf("late chunk of a stalled message: %v", f)
	}
	publish(3, 0, false, "o")
	publish(3, 1, true, "k")
	if got := box.wait(t, 1); string(got[0]) != "ok" {
		t.Fatalf("got %q", got)
	}
}

// An unfinished cacheable message on a connection that goes quiet is
// dropped after chunk_cache_ttl all the same.
func TestChunkCachePendingExpiresWhileIdle(t *testing.T) {
	srv, url := start(t, func(c *relay.Config) {
		c.ChunkCacheBytes = 8 << 20
		c.ChunkCacheTTL.Duration = time.Second
		c.PingInterval.Duration = 100 * time.Millisecond
	})
	ws, hello := rawHello(t, url)
	writeRaw(t, ws, startFrame(hello.Versions[len(hello.Versions)-1], nil))
	if f := readRaw(t, ws); f.GetAck() == nil {
		t.Fatalf("start: %v", f)
	}
	writeRaw(t, ws, &wire.RelayFrame{Kind: &wire.RelayFrame_Publish{Publish: &wire.Publish{Topic: []byte("t"), MessageId: 1, Cacheable: true, Data: make([]byte, 4096)}}})
	waitStat(t, func() int64 { return int64(srv.CachePendingBytes()) }, 4096)
	deadline := time.Now().Add(5 * time.Second)
	for srv.CachePendingBytes() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("idle connection still holds %d pending bytes after the TTL", srv.CachePendingBytes())
		}
		time.Sleep(50 * time.Millisecond)
	}
}
