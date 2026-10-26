// Package testutil runs relays, community servers and maintainer nodes
// in-process for integration tests, plus a minimal Go member client.
package testutil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flynn/noise"
	"golang.org/x/crypto/chacha20poly1305"
	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/meta"
	"github.com/hyperkubeorg/openarcanum/internal/relay"
	"github.com/hyperkubeorg/openarcanum/internal/relayclient"
	"github.com/hyperkubeorg/openarcanum/internal/session"
	"github.com/hyperkubeorg/openarcanum/internal/sfu"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Quiet is a logger that drops everything unless OPENARCANUM_TEST_LOG is set.
func Quiet() *slog.Logger {
	if os.Getenv("OPENARCANUM_TEST_LOG") != "" {
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return slog.New(slog.NewTextHandler(discard{}, nil))
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// Relay is an in-process relay.
type Relay struct {
	URL    string
	Server *relay.Server
	hs     *httptest.Server
}

// StartRelay runs a relay with default limits (carrying calls) until the
// test ends.
func StartRelay(t testing.TB) *Relay { return StartRelayCalls(t, true) }

// StartRelayCalls runs a relay with or without its SFU.
func StartRelayCalls(t testing.TB, calls bool) *Relay { return StartRelayWith(t, calls, nil) }

// StartRelayWith runs a relay whose config mutate changes (abuse controls,
// chunk cache).
func StartRelayWith(t testing.TB, calls bool, mutate func(*relay.Config)) *Relay {
	t.Helper()
	cfg := relay.DefaultConfig()
	if mutate != nil {
		mutate(&cfg)
		if err := cfg.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	srv := relay.New(cfg, Quiet())
	if calls {
		media, err := sfu.New(sfu.Config{IncludeLoopback: true, Log: Quiet()})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = media.Close() })
		srv.SetSFU(media)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	url := "ws" + strings.TrimPrefix(hs.URL, "http") + "/ws"
	// Its public_url, as on a public relay (what servers sign for it is
	// bound to it).
	if err := srv.SetURL(url); err != nil {
		t.Fatal(err)
	}
	return &Relay{URL: url, Server: srv, hs: hs}
}

// Stop kills the relay and every connection on it.
func (r *Relay) Stop() {
	_ = r.hs.Listener.Close()
	r.Server.CloseAll()
	r.hs.CloseClientConnections()
}

// Community is an in-process community server.
type Community struct {
	Server    *community.Server
	Dir       string
	ClaimCode *wire.ClaimCode
	// Log receives the server's log (Quiet by default).
	Log    *slog.Logger
	cancel context.CancelFunc
	done   chan struct{}
}

// StartCommunity initializes and runs a community server attached to relays.
func StartCommunity(t testing.TB, mutate func(*community.Config), relays ...string) *Community {
	t.Helper()
	return StartCommunityWithLog(t, Quiet(), mutate, relays...)
}

// StartCommunityWithLog is StartCommunity with the server logging to log.
func StartCommunityWithLog(t testing.TB, log *slog.Logger, mutate func(*community.Config), relays ...string) *Community {
	t.Helper()
	dir := t.TempDir()
	cfg := community.DefaultConfig(dir)
	cfg.Relays = relays
	cfg.BackupInterval.Duration = 0
	if mutate != nil {
		mutate(&cfg)
	}
	res, err := community.Init(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	c := &Community{Dir: dir, ClaimCode: claimCode(t, res), Log: log}
	c.start(t, cfg)
	return c
}

func claimCode(t testing.TB, res *community.InitResult) *wire.ClaimCode {
	t.Helper()
	b, err := identity.Decode(strings.TrimPrefix(res.ClaimCode, community.ClaimPrefix))
	if err != nil {
		t.Fatal(err)
	}
	var code wire.ClaimCode
	if err := proto.Unmarshal(b, &code); err != nil {
		t.Fatal(err)
	}
	return &code
}

// StartDedicated runs a community server pinned to relays dedicated to it
// (docs/operations.md "Dedicated relays"), one per block file given (a
// fresh file for ""; one relay when none is given). The relays listen on
// 127.0.0.1; members connecting from other loopback addresses (From) look
// like other networks to them.
func StartDedicated(t testing.TB, blockFiles ...string) ([]*Relay, *Community) {
	t.Helper()
	if len(blockFiles) == 0 {
		blockFiles = []string{""}
	}
	var lns []net.Listener
	dir := t.TempDir()
	cfg := community.DefaultConfig(dir)
	cfg.BackupInterval.Duration = 0
	for range blockFiles {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		lns = append(lns, ln)
		cfg.Relays = append(cfg.Relays, "ws://"+ln.Addr().String()+"/ws")
	}
	res, err := community.Init(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	var relays []*Relay
	for i, f := range blockFiles {
		if f == "" {
			f = filepath.Join(t.TempDir(), "blocks.json")
		}
		relays = append(relays, StartDedicatedRelay(t, lns[i], res.ServerKey, f))
	}
	c := &Community{Dir: dir, ClaimCode: claimCode(t, res), Log: Quiet()}
	c.start(t, cfg)
	return relays, c
}

// StartDedicatedRelay serves a relay dedicated to serverKey on ln.
func StartDedicatedRelay(t testing.TB, ln net.Listener, serverKey []byte, blockFile string) *Relay {
	t.Helper()
	cfg := relay.DefaultConfig()
	cfg.DedicatedServerKey = hex.EncodeToString(serverKey)
	cfg.BlockFile = blockFile
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	srv := relay.New(cfg, Quiet())
	if err := srv.Err(); err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewUnstartedServer(srv.Handler())
	_ = hs.Listener.Close()
	hs.Listener = ln
	hs.Start()
	t.Cleanup(hs.Close)
	url := "ws://" + ln.Addr().String() + "/ws"
	if err := srv.SetURL(url); err != nil {
		t.Fatal(err)
	}
	return &Relay{URL: url, Server: srv, hs: hs}
}

// From is an HTTP client whose connections leave from the local address
// source (any 127.0.0.0/8 address reaches a loopback listener on Linux and
// Windows). macOS has only 127.0.0.1 on lo0 unless more are aliased (CI does:
// .github/workflows/ci.yml), so the test is skipped when source can't be bound.
func From(t testing.TB, source string) *http.Client {
	t.Helper()
	l, err := net.Listen("tcp", net.JoinHostPort(source, "0"))
	if err != nil {
		t.Skipf("can't use %s as a source address (on macOS: sudo ifconfig lo0 alias %s up): %v", source, source, err)
	}
	_ = l.Close()
	d := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(source)}, Timeout: 10 * time.Second}
	return &http.Client{Transport: &http.Transport{DialContext: d.DialContext}}
}

func (c *Community) start(t testing.TB, cfg community.Config) {
	t.Helper()
	srv, err := community.Open(context.Background(), cfg, c.Log)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.Server, c.cancel, c.done = srv, cancel, make(chan struct{})
	go func() {
		_ = srv.Run(ctx)
		_ = srv.Close()
		close(c.done)
	}()
	t.Cleanup(c.Stop)
	deadline := time.Now().Add(10 * time.Second)
	for len(srv.AttachedRelays()) < len(cfg.Relays) && cfg.RelayMode == "pinned" {
		if time.Now().After(deadline) {
			t.Fatal("community never attached to its relays")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Stop shuts the server down.
func (c *Community) Stop() {
	if c.cancel != nil {
		c.cancel()
		<-c.done
		c.cancel = nil
	}
}

// Restart stops and reopens the server from the same data directory.
func (c *Community) Restart(t testing.TB, relays ...string) {
	c.Stop()
	cfg := community.DefaultConfig(c.Dir)
	cfg.Relays = relays
	cfg.BackupInterval.Duration = 0
	c.start(t, cfg)
}

// ---------------------------------------------------------------- member client

// Member is a minimal Go client: one identity, one device, one session.
type Member struct {
	t        testing.TB
	Identity *identity.Key
	Static   noise.DHKey
	Name     string

	// HTTP, when set, is how Connect reaches the relay (From: another
	// source address).
	HTTP *http.Client

	Relay   *relayclient.Client
	Session *session.Session
	Status  *wire.SessionStatus
	State   *wire.SyncState

	nextID  atomic.Uint64
	mu      sync.Mutex
	pending map[uint64]chan *wire.ServerMessage
	Events  chan *wire.ServerMessage

	cmu      sync.Mutex
	Received map[int64][]*wire.Message // channel id -> messages from topics
	// Every channel event from watched topics (messages, edits, deletions,
	// typing, updates), per channel id.
	ChannelEvents map[int64][]*wire.ChannelEvent
	recvCh        chan struct{}
}

// NewMember makes a member with fresh keys.
func NewMember(t testing.TB, name string) *Member {
	id, _ := identity.Generate()
	st, _ := session.GenerateStatic()
	return &Member{t: t, Identity: id, Static: st, Name: name}
}

// Connect opens a session to the server through relayURL. serverKey is the
// pinned key.
func (m *Member) Connect(ctx context.Context, relayURL string, serverKey []byte) error {
	hc := m.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	rc, err := relayclient.DialHTTP(ctx, relayURL, hc)
	if err != nil {
		return err
	}
	st, err := rc.OpenStream(ctx, serverKey)
	if err != nil {
		rc.Close()
		return err
	}
	sn, err := session.Dial(ctx, st, session.ClientConfig{
		Static: m.Static, IdentityKey: m.Identity.Public, StaticSig: session.SignStatic(m.Identity, m.Static.Public),
		DeviceName: m.Name + "-device",
		VerifyServer: func(si *wire.ServerIdentity) error {
			if string(si.ServerKey) != string(serverKey) {
				return errors.New("unexpected server key")
			}
			return nil
		},
	})
	if err != nil {
		rc.Close()
		return err
	}
	m.Relay, m.Session = rc, sn
	m.pending = map[uint64]chan *wire.ServerMessage{}
	m.Events = make(chan *wire.ServerMessage, 256)
	m.Received = map[int64][]*wire.Message{}
	m.ChannelEvents = map[int64][]*wire.ChannelEvent{}
	m.recvCh = make(chan struct{}, 1024)
	first, err := sn.Recv(ctx)
	if err != nil {
		rc.Close()
		return err
	}
	var sm wire.ServerMessage
	if err := proto.Unmarshal(first, &sm); err != nil || sm.GetStatus() == nil {
		rc.Close()
		return errors.New("server did not send a session status")
	}
	m.Status = sm.GetStatus()
	go m.readLoop()
	return nil
}

// Close ends the session and relay connection.
func (m *Member) Close() {
	if m.Relay != nil {
		m.Relay.Close()
	}
}

func (m *Member) readLoop() {
	for {
		b, err := m.Session.Recv(context.Background())
		if err != nil {
			m.mu.Lock()
			for _, ch := range m.pending {
				close(ch)
			}
			m.pending = map[uint64]chan *wire.ServerMessage{}
			m.mu.Unlock()
			close(m.Events)
			return
		}
		var sm wire.ServerMessage
		if proto.Unmarshal(b, &sm) != nil {
			continue
		}
		if sm.ReplyTo == 0 {
			if sc := sm.GetStateChanged(); sc != nil && sc.State != nil {
				m.State = sc.State
			}
			select {
			case m.Events <- &sm:
			default:
			}
			continue
		}
		m.mu.Lock()
		ch := m.pending[sm.ReplyTo]
		delete(m.pending, sm.ReplyTo)
		m.mu.Unlock()
		if ch != nil {
			ch <- &sm
		}
	}
}

// Call sends a request and waits for the reply. RPC errors become Go errors.
func (m *Member) Call(ctx context.Context, msg *wire.ClientMessage) (*wire.ServerMessage, error) {
	msg.Id = m.nextID.Add(1)
	ch := make(chan *wire.ServerMessage, 1)
	m.mu.Lock()
	m.pending[msg.Id] = ch
	m.mu.Unlock()
	b, _ := proto.Marshal(msg)
	if err := m.Session.Send(b); err != nil {
		return nil, err
	}
	select {
	case r, ok := <-ch:
		if !ok {
			return nil, errors.New("session closed")
		}
		if e := r.GetError(); e != nil {
			return r, fmt.Errorf("rpc %s: %s", e.Code, e.Message)
		}
		return r, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Admin signs and sends an admin action.
func (m *Member) Admin(ctx context.Context, community []byte, a *wire.AdminAction) (*wire.AdminResult, error) {
	a.Community = community
	a.Actor = m.Identity.Public
	a.At = time.Now().UnixMilli()
	a.Nonce = make([]byte, 16)
	rand.Read(a.Nonce)
	b, _ := proto.Marshal(a)
	r, err := m.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_Admin{Admin: &wire.SignedAction{
		Action: b, Signature: m.Identity.Sign(identity.CtxAdminAction, b),
	}}})
	if err != nil {
		return nil, err
	}
	return r.GetAdminResult(), nil
}

// Claim claims the server as owner and returns the first invite.
func (m *Member) Claim(ctx context.Context, owner *identity.Key, code *wire.ClaimCode, settings *wire.CommunitySettings) (*wire.ClaimResult, error) {
	cert, err := identity.IssueServerCert(owner, code.ServerKey, 1, nil, nil)
	if err != nil {
		return nil, err
	}
	r, err := m.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_Claim{Claim: &wire.Claim{
		Secret: code.Secret, Cert: cert, Settings: settings, DisplayName: m.Name,
	}}})
	if err != nil {
		return nil, err
	}
	m.State = r.GetClaimResult().State
	return r.GetClaimResult(), nil
}

// Join submits a join request.
func (m *Member) Join(ctx context.Context, joinKey string) (*wire.JoinResult, error) {
	r, err := m.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_Join{Join: &wire.Join{JoinKey: joinKey, DisplayName: m.Name}}})
	if err != nil {
		return nil, err
	}
	if s := r.GetJoinResult().State; s != nil {
		m.State = s
	}
	return r.GetJoinResult(), nil
}

// Sync refreshes State.
func (m *Member) Sync(ctx context.Context) error {
	r, err := m.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_Sync{Sync: &wire.Sync{}}})
	if err != nil {
		return err
	}
	m.State = r.GetState()
	return nil
}

// Channel finds a channel in State by name.
func (m *Member) Channel(name string) *wire.Channel {
	if m.State == nil {
		return nil
	}
	for _, c := range m.State.Channels {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// Post sends a message.
func (m *Member) Post(ctx context.Context, channelID int64, text string) (*wire.Message, error) {
	r, err := m.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_Post{Post: &wire.Post{ChannelId: channelID, Text: text}}})
	if err != nil {
		return nil, err
	}
	return r.GetMessage(), nil
}

// Watch subscribes to a channel's topic with the given key, verifying the
// server's signature and decrypting each event into Received.
func (m *Member) Watch(ctx context.Context, ch *wire.Channel, serverKey []byte) error {
	key := append([]byte(nil), ch.Key...)
	epoch := ch.KeyEpoch
	return m.Relay.Subscribe(ctx, ch.Topic, func(_, data []byte) {
		ev, err := OpenChannelEvent(data, serverKey, key, epoch)
		if err != nil {
			return
		}
		m.cmu.Lock()
		m.ChannelEvents[ch.Id] = append(m.ChannelEvents[ch.Id], ev)
		if msg := ev.GetMessage(); msg != nil {
			m.Received[ch.Id] = append(m.Received[ch.Id], msg)
		}
		m.cmu.Unlock()
		select {
		case m.recvCh <- struct{}{}:
		default:
		}
	})
}

// WaitReceived waits until n messages have arrived on a watched channel.
func (m *Member) WaitReceived(channelID int64, n int, timeout time.Duration) []*wire.Message {
	deadline := time.After(timeout)
	for {
		m.cmu.Lock()
		got := append([]*wire.Message(nil), m.Received[channelID]...)
		m.cmu.Unlock()
		if len(got) >= n {
			return got
		}
		select {
		case <-m.recvCh:
		case <-deadline:
			return got
		}
	}
}

// WaitEvent waits for a channel event on a watched channel that matches
// pred, and returns it (nil on timeout).
func (m *Member) WaitEvent(channelID int64, pred func(*wire.ChannelEvent) bool, timeout time.Duration) *wire.ChannelEvent {
	deadline := time.After(timeout)
	for {
		m.cmu.Lock()
		for _, ev := range m.ChannelEvents[channelID] {
			if pred(ev) {
				m.cmu.Unlock()
				return ev
			}
		}
		m.cmu.Unlock()
		select {
		case <-m.recvCh:
		case <-deadline:
			return nil
		}
	}
}

// OpenEnvelope verifies and decrypts a channel topic payload carrying a new
// message.
func OpenEnvelope(data, serverKey, channelKey []byte, epoch uint32) (*wire.Message, error) {
	ev, err := OpenChannelEvent(data, serverKey, channelKey, epoch)
	if err != nil {
		return nil, err
	}
	if msg := ev.GetMessage(); msg != nil {
		return msg, nil
	}
	return nil, errors.New("not a new-message event")
}

// OpenChannelEvent verifies and decrypts any channel topic payload.
func OpenChannelEvent(data, serverKey, channelKey []byte, epoch uint32) (*wire.ChannelEvent, error) {
	var se wire.SignedEnvelope
	if err := proto.Unmarshal(data, &se); err != nil {
		return nil, err
	}
	if err := identity.Verify(serverKey, identity.CtxChannelEnvelope, se.Envelope, se.Signature); err != nil {
		return nil, err
	}
	var env wire.ChannelEnvelope
	if err := proto.Unmarshal(se.Envelope, &env); err != nil {
		return nil, err
	}
	if env.KeyEpoch != epoch {
		return nil, errors.New("wrong key epoch")
	}
	aead, err := chacha20poly1305.New(channelKey)
	if err != nil {
		return nil, err
	}
	pt, err := aead.Open(nil, env.Nonce, env.Ciphertext, community.ChannelAAD(env.ChannelId, env.KeyEpoch))
	if err != nil {
		return nil, err
	}
	var ev wire.ChannelEvent
	if err := proto.Unmarshal(pt, &ev); err != nil {
		return nil, err
	}
	return &ev, nil
}

// Ctx is a test context with a timeout.
func Ctx(t testing.TB) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// Meta is an in-process maintainer node.
type Meta struct {
	URL    string
	Server *meta.Server
}

// StartMeta runs a maintainer node until the test ends.
func StartMeta(t testing.TB, mutate func(*meta.Config)) *Meta {
	t.Helper()
	cfg := meta.DefaultConfig()
	cfg.DirectoryEnabled = true // the tests exercise the directory; it's off by default
	cfg.DataDir = t.TempDir()
	cfg.AllowPrivateRelays = true // test relays listen on 127.0.0.1
	if mutate != nil {
		mutate(&cfg)
	}
	srv, err := meta.Open(context.Background(), cfg, Quiet())
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		hs.Close()
		_ = srv.Close()
	})
	return &Meta{URL: hs.URL, Server: srv}
}
