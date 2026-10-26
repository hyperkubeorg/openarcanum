package relay_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/fanout"
	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/relay"
	"github.com/hyperkubeorg/openarcanum/internal/relayclient"
	"github.com/hyperkubeorg/openarcanum/internal/relaymod"
	"github.com/hyperkubeorg/openarcanum/internal/sfu"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Dedicated relays (docs/protocol.md §2 "Dedicated relays"): one community
// server's streams, topics and calls only, and network blocks it asks for.

type dedicatedRelay struct {
	srv  *relay.Server
	hs   *http.Server
	port string
	file string
	key  *identity.Key
}

// startDedicated runs a relay dedicated to key on every loopback address
// (IPv4 and IPv6), so test clients can connect from distinct addresses.
func startDedicated(t *testing.T, key *identity.Key, blockFile string, mutate func(*relay.Config)) *dedicatedRelay {
	t.Helper()
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	return serveDedicated(t, ln, key, blockFile, mutate)
}

func serveDedicated(t testing.TB, ln net.Listener, key *identity.Key, blockFile string, mutate func(*relay.Config)) *dedicatedRelay {
	t.Helper()
	cfg := relay.DefaultConfig()
	cfg.Listen = ln.Addr().String()
	cfg.DedicatedServerKey = hexKey(key)
	cfg.BlockFile = blockFile
	if mutate != nil {
		mutate(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	srv := relay.New(cfg, testutil.Quiet())
	if err := srv.Err(); err != nil {
		t.Fatal(err)
	}
	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = hs.Serve(ln) }()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	d := &dedicatedRelay{srv: srv, hs: hs, port: port, file: blockFile, key: key}
	t.Cleanup(d.stop)
	// Its public URL: the server (d.host) dials it there.
	if err := srv.SetURL(d.url("127.0.0.1")); err != nil {
		t.Fatal(err)
	}
	return d
}

func (d *dedicatedRelay) stop() {
	_ = d.hs.Close()
	d.srv.CloseAll()
}

func hexKey(k *identity.Key) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, 64)
	for _, b := range k.Public {
		out = append(out, digits[b>>4], digits[b&15])
	}
	return string(out)
}

// url is the relay's URL at host (an IPv4 address or a bracketed IPv6 one).
func (d *dedicatedRelay) url(host string) string { return "ws://" + host + ":" + d.port + "/ws" }

// from is an HTTP client whose connections leave from source.
func from(source string) *http.Client {
	dialer := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(source)}, Timeout: 5 * time.Second}
	return &http.Client{Transport: &http.Transport{DialContext: dialer.DialContext}}
}

func clientFrom(t *testing.T, url, source string) (*relayclient.Client, error) {
	t.Helper()
	c, err := relayclient.DialHTTP(context.Background(), url, from(source))
	if err == nil {
		t.Cleanup(c.Close)
	}
	return c, err
}

func mustDialFrom(t *testing.T, url, source string) *relayclient.Client {
	t.Helper()
	c, err := clientFrom(t, url, source)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// host attaches key's endpoint from 127.0.0.1 (the community server).
func (d *dedicatedRelay) host(t *testing.T) *relayclient.Client {
	t.Helper()
	c := mustDialFrom(t, d.url("127.0.0.1"), "127.0.0.1")
	if err := c.RegisterEndpoint(ctx(t), d.key); err != nil {
		t.Fatal(err)
	}
	return c
}

// member opens a stream to the server from source, and returns the client
// and the server's side of the stream.
func member(t *testing.T, host *relayclient.Client, url, source string, key *identity.Key) (*relayclient.Client, *relayclient.Stream) {
	t.Helper()
	c := mustDialFrom(t, url, source)
	if _, err := c.OpenStream(ctx(t), key.Public); err != nil {
		t.Fatal(err)
	}
	st, err := host.Accept(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	return c, st
}

func block(streams []uint32, seconds uint32) *wire.ModerationCommand {
	return &wire.ModerationCommand{Action: &wire.ModerationCommand_Block{Block: &wire.NetworkBlock{StreamIds: streams, Seconds: seconds}}}
}

func unblock(id []byte) *wire.ModerationCommand {
	return &wire.ModerationCommand{Action: &wire.ModerationCommand_Unblock{Unblock: &wire.NetworkUnblock{BlockId: id}}}
}

func relayCode(t *testing.T, err error) wire.ErrorCode {
	t.Helper()
	var re *relayclient.RelayError
	if !errors.As(err, &re) {
		t.Fatalf("want a relay error, got %v", err)
	}
	return re.Code
}

func waitDone(t *testing.T, c *relayclient.Client) {
	t.Helper()
	select {
	case <-c.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("connection wasn't closed")
	}
}

func TestDedicatedRelayServesOnlyItsServer(t *testing.T) {
	key, _ := identity.Generate()
	other, _ := identity.Generate()
	d := startDedicated(t, key, filepath.Join(t.TempDir(), "blocks.json"), nil)
	url := d.url("127.0.0.1")

	c := dial(t, url)
	if string(c.DedicatedTo()) != string(key.Public) {
		t.Fatal("Hello doesn't name the server the relay is dedicated to")
	}
	if code := relayCode(t, c.RegisterEndpoint(ctx(t), other)); code != wire.ErrorCode_ERROR_CODE_NOT_DEDICATED {
		t.Fatalf("another server registered: %v", code)
	}
	host := d.host(t)
	if _, err := c.OpenStream(ctx(t), other.Public); err == nil || !strings.Contains(err.Error(), "not_dedicated") {
		t.Fatalf("stream to another server: %v", err)
	}
	if _, err := c.OpenStream(ctx(t), key.Public); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Accept(ctx(t)); err != nil {
		t.Fatal(err)
	}

	// Topics: only ones committed to the server's key.
	if code := relayCode(t, c.Subscribe(ctx(t), []byte("some dm topic"), nil)); code != wire.ErrorCode_ERROR_CODE_NOT_DEDICATED {
		t.Fatalf("foreign topic: %v", code)
	}
	if code := relayCode(t, c.Subscribe(ctx(t), fanout.NewTopic(other.Public), nil)); code != wire.ErrorCode_ERROR_CODE_NOT_DEDICATED {
		t.Fatalf("another server's topic: %v", code)
	}
	topic := fanout.NewTopic(key.Public)
	box := newInbox()
	if err := c.Subscribe(ctx(t), topic, box.handler); err != nil {
		t.Fatal(err)
	}
	if err := host.Publish(topic, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	box.wait(t, 1)
	// A message can't start on the server's topic and continue on another.
	raw := rawDial(t, url)
	raw.send(&wire.RelayFrame{Kind: &wire.RelayFrame_Publish{Publish: &wire.Publish{Topic: topic, MessageId: 1, Data: []byte("a")}}})
	raw.send(&wire.RelayFrame{Kind: &wire.RelayFrame_Publish{Publish: &wire.Publish{Topic: []byte("some dm topic"), MessageId: 1, Seq: 1, Final: true, Data: []byte("b")}}})
	if e := raw.next(isErr).GetError(); e.Code != wire.ErrorCode_ERROR_CODE_BAD_FRAME {
		t.Fatalf("chunk on a foreign topic: %v", e)
	}
}

func TestDedicatedRelayCarriesOnlyItsServersCalls(t *testing.T) {
	key, _ := identity.Generate()
	other, _ := identity.Generate()
	media, err := sfu.New(sfu.Config{IncludeLoopback: true, Log: testutil.Quiet()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = media.Close() })
	d := startDedicated(t, key, filepath.Join(t.TempDir(), "blocks.json"), nil)
	d.srv.SetSFU(media)
	c := rawDial(t, d.url("127.0.0.1"))
	cs := &callServer{key: other, nonce: make([]byte, 16)}
	g, _ := cs.grant(t, "a")
	c.send(&wire.RelayFrame{Kind: &wire.RelayFrame_CallJoin{CallJoin: &wire.CallJoin{Grant: g, Req: 1}}})
	if e := c.next(isErr).GetError(); e.Code != wire.ErrorCode_ERROR_CODE_CALL_REFUSED || !strings.Contains(e.Message, "dedicated") {
		t.Fatalf("another server's call: %v", e)
	}
}

func TestOrdinaryRelayRefusesModeration(t *testing.T) {
	key, _ := identity.Generate()
	_, url := start(t, nil)
	host := dial(t, url)
	if err := host.RegisterEndpoint(ctx(t), key); err != nil {
		t.Fatal(err)
	}
	if len(host.DedicatedTo()) != 0 {
		t.Fatal("an ordinary relay says it's dedicated")
	}
	_, err := host.Moderate(ctx(t), key, block([]uint32{relay.InboundStreamBit | 1}, 0))
	if code := relayCode(t, err); code != wire.ErrorCode_ERROR_CODE_NOT_DEDICATED {
		t.Fatalf("got %v", code)
	}
}

func TestModerationCommandChecks(t *testing.T) {
	key, _ := identity.Generate()
	forger, _ := identity.Generate()
	d := startDedicated(t, key, filepath.Join(t.TempDir(), "blocks.json"), nil)
	url := d.url("127.0.0.1")
	host := d.host(t)
	_, st := member(t, host, url, "127.0.0.2", key)
	ids := []uint32{st.ID}
	now := time.Now()

	sign := func(k *identity.Key, on *relayclient.Client, mutate func(*wire.ModerationCommand)) *wire.SignedModerationCommand {
		c := block(ids, 60)
		b := binding(t, on)
		c.ServerKey, c.RelayNonce, c.CommandId = key.Public, b.Nonce, make([]byte, relaymod.CommandIDLen)
		c.RelayKey, c.Url = b.RelayKey, b.URL
		c.CommandId[0] = byte(now.UnixNano())
		c.ExpiresAt = now.Add(time.Minute).UnixMilli()
		if mutate != nil {
			mutate(c)
		}
		raw, _ := proto.Marshal(c)
		return &wire.SignedModerationCommand{Command: raw, Signature: k.Sign(identity.CtxRelayModeration, raw)}
	}
	expect := func(name string, sc *wire.SignedModerationCommand, want wire.ErrorCode) {
		t.Helper()
		_, err := host.SendModeration(ctx(t), sc)
		if err == nil {
			t.Fatalf("%s: accepted", name)
		}
		if got := relayCode(t, err); got != want {
			t.Fatalf("%s: got %v, want %v (%v)", name, got, want, err)
		}
	}

	expect("forged", sign(forger, host, nil), wire.ErrorCode_ERROR_CODE_BAD_COMMAND)
	expect("another server's", sign(forger, host, func(c *wire.ModerationCommand) { c.ServerKey = forger.Public }), wire.ErrorCode_ERROR_CODE_NOT_DEDICATED)
	expect("expired", sign(key, host, func(c *wire.ModerationCommand) { c.ExpiresAt = now.Add(-time.Second).UnixMilli() }), wire.ErrorCode_ERROR_CODE_BAD_COMMAND)
	expect("too long", sign(key, host, func(c *wire.ModerationCommand) { c.ExpiresAt = now.Add(time.Hour).UnixMilli() }), wire.ErrorCode_ERROR_CODE_BAD_COMMAND)
	other := dial(t, url)
	expect("wrong relay connection", sign(key, other, nil), wire.ErrorCode_ERROR_CODE_BAD_COMMAND)
	elsewhere := startDedicated(t, key, filepath.Join(t.TempDir(), "blocks.json"), nil)
	elsewhereHost := elsewhere.host(t)
	expect("another relay", sign(key, elsewhereHost, nil), wire.ErrorCode_ERROR_CODE_BAD_COMMAND)
	expect("unknown stream", sign(key, host, func(c *wire.ModerationCommand) {
		c.Action = &wire.ModerationCommand_Block{Block: &wire.NetworkBlock{StreamIds: []uint32{relay.InboundStreamBit | 999999}}}
	}), wire.ErrorCode_ERROR_CODE_BAD_COMMAND)
	expect("no action", sign(key, host, func(c *wire.ModerationCommand) { c.Action = nil }), wire.ErrorCode_ERROR_CODE_BAD_COMMAND)
	expect("too many streams", sign(key, host, func(c *wire.ModerationCommand) {
		c.Action = &wire.ModerationCommand_Block{Block: &wire.NetworkBlock{StreamIds: make([]uint32, relaymod.MaxStreams+1)}}
	}), wire.ErrorCode_ERROR_CODE_BAD_COMMAND)

	// A valid command from a connection that doesn't host the server (its
	// own nonce, the server's signature) is refused.
	_, err := other.SendModeration(ctx(t), sign(key, other, nil))
	if code := relayCode(t, err); code != wire.ErrorCode_ERROR_CODE_NOT_DEDICATED {
		t.Fatalf("command from a non-host connection: %v", code)
	}

	// Replay: the same signed command twice on the host's connection.
	good := sign(key, host, func(c *wire.ModerationCommand) { c.CommandId[1] = 0xee })
	res, err := host.SendModeration(ctx(t), good)
	if err != nil || len(res.BlockId) != relaymod.BlockIDLen || res.Networks != 1 {
		t.Fatalf("valid command: %v %v", res, err)
	}
	expect("replayed", good, wire.ErrorCode_ERROR_CODE_BAD_COMMAND)
	if d.srv.BlockedNetworks() != 1 {
		t.Fatalf("blocked networks = %d", d.srv.BlockedNetworks())
	}
}

func TestNetworkBlockAndUnblock(t *testing.T) {
	key, _ := identity.Generate()
	d := startDedicated(t, key, filepath.Join(t.TempDir(), "blocks.json"), nil)
	url := d.url("127.0.0.1")
	host := d.host(t)
	victim, st := member(t, host, url, "127.0.0.2", key)
	// Another connection from the same address (another device, a
	// neighbour behind the same NAT) is closed too; one elsewhere isn't.
	sameNet := mustDialFrom(t, url, "127.0.0.2")
	bystander, _ := member(t, host, url, "127.0.0.3", key)

	res, err := host.Moderate(ctx(t), key, block([]uint32{st.ID}, 0))
	if err != nil {
		t.Fatal(err)
	}
	if res.Networks != 1 || res.Closed != 2 || res.Skipped != 0 || len(res.BlockId) != relaymod.BlockIDLen {
		t.Fatalf("result: %v", res)
	}
	waitDone(t, victim)
	waitDone(t, sameNet)
	if _, err := clientFrom(t, url, "127.0.0.2"); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("reconnect from the blocked address: %v", err)
	}
	waitStat(t, d.srv.Stats().BlockedRefused.Load, 1)
	select {
	case <-bystander.Done():
		t.Fatal("a member on another address was disconnected")
	case <-host.Done():
		t.Fatal("the server's own connection was closed")
	default:
	}
	mustDialFrom(t, url, "127.0.0.4")

	// Lifting it by id lets the address back in; lifting again says so.
	un, err := host.Moderate(ctx(t), key, unblock(res.BlockId))
	if err != nil || un.NotFound {
		t.Fatalf("unblock: %v %v", un, err)
	}
	mustDialFrom(t, url, "127.0.0.2")
	un, err = host.Moderate(ctx(t), key, unblock(res.BlockId))
	if err != nil || !un.NotFound {
		t.Fatalf("second unblock: %v %v", un, err)
	}
	if d.srv.BlockedNetworks() != 0 {
		t.Fatal("block list not empty")
	}
}

func TestServersOwnNetworkIsNeverBlocked(t *testing.T) {
	key, _ := identity.Generate()
	d := startDedicated(t, key, filepath.Join(t.TempDir(), "blocks.json"), nil)
	url := d.url("127.0.0.1")
	host := d.host(t)
	c, st := member(t, host, url, "127.0.0.1", key)
	res, err := host.Moderate(ctx(t), key, block([]uint32{st.ID}, 0))
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped != 1 || res.Networks != 0 || len(res.BlockId) != 0 || res.Closed != 0 {
		t.Fatalf("result: %v", res)
	}
	select {
	case <-c.Done():
		t.Fatal("member on the server's network was disconnected")
	default:
	}
}

func TestIPv6BlocksTheWhole64(t *testing.T) {
	key, _ := identity.Generate()
	d := startDedicated(t, key, filepath.Join(t.TempDir(), "blocks.json"), nil)
	host := d.host(t)
	url6 := d.url("[::1]")
	victim, st := member(t, host, url6, "::1", key)
	res, err := host.Moderate(ctx(t), key, block([]uint32{st.ID}, 0))
	if err != nil || res.Networks != 1 {
		t.Fatalf("%v %v", res, err)
	}
	waitDone(t, victim)
	if !d.srv.Blocked("::/64") {
		t.Fatal("the /64 isn't blocked")
	}
	if _, err := clientFrom(t, url6, "::1"); err == nil {
		t.Fatal("reconnect from the blocked /64")
	}
	// IPv4 clients are unaffected.
	mustDialFrom(t, d.url("127.0.0.1"), "127.0.0.5")
}

func TestBlocksSurviveRestart(t *testing.T) {
	key, _ := identity.Generate()
	file := filepath.Join(t.TempDir(), "blocks.json")
	d := startDedicated(t, key, file, nil)
	url := d.url("127.0.0.1")
	host := d.host(t)
	_, forever := member(t, host, url, "127.0.0.2", key)
	_, brief := member(t, host, url, "127.0.0.3", key)
	_, lifted := member(t, host, url, "127.0.0.6", key)
	if _, err := host.Moderate(ctx(t), key, block([]uint32{forever.ID}, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Moderate(ctx(t), key, block([]uint32{brief.ID}, 1)); err != nil {
		t.Fatal(err)
	}
	res, err := host.Moderate(ctx(t), key, block([]uint32{lifted.ID}, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.Moderate(ctx(t), key, unblock(res.BlockId)); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("block file mode %v", fi.Mode().Perm())
	}
	d.stop()

	time.Sleep(1100 * time.Millisecond) // the 1-second block runs out
	ln, err := net.Listen("tcp", ":"+d.port)
	if err != nil {
		t.Fatal(err)
	}
	again := serveDedicated(t, ln, key, file, nil)
	if !again.srv.Blocked("127.0.0.2") {
		t.Fatal("block lost across a restart")
	}
	if again.srv.Blocked("127.0.0.3") || again.srv.Blocked("127.0.0.6") {
		t.Fatal("an expired or lifted block came back")
	}
	if _, err := clientFrom(t, url, "127.0.0.2"); err == nil {
		t.Fatal("blocked address connected after a restart")
	}
	mustDialFrom(t, url, "127.0.0.3")
}

func TestUnreadableBlockFileRefusesEverything(t *testing.T) {
	key, _ := identity.Generate()
	file := filepath.Join(t.TempDir(), "blocks.json")
	if err := os.WriteFile(file, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := relay.DefaultConfig()
	cfg.DedicatedServerKey = hexKey(key)
	cfg.BlockFile = file
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	srv := relay.New(cfg, testutil.Quiet())
	if srv.Err() == nil {
		t.Fatal("a corrupt block file was accepted")
	}
}

func TestBlockListLimit(t *testing.T) {
	key, _ := identity.Generate()
	d := startDedicated(t, key, filepath.Join(t.TempDir(), "blocks.json"), func(c *relay.Config) { c.MaxBlockedNetworks = 1 })
	url := d.url("127.0.0.1")
	host := d.host(t)
	_, a := member(t, host, url, "127.0.0.2", key)
	_, b := member(t, host, url, "127.0.0.3", key)
	if _, err := host.Moderate(ctx(t), key, block([]uint32{a.ID}, 0)); err != nil {
		t.Fatal(err)
	}
	_, err := host.Moderate(ctx(t), key, block([]uint32{b.ID}, 0))
	if code := relayCode(t, err); code != wire.ErrorCode_ERROR_CODE_LIMIT {
		t.Fatalf("got %v", code)
	}
}

func TestDedicatedConfig(t *testing.T) {
	key, _ := identity.Generate()
	for name, mutate := range map[string]func(*relay.Config){
		"no block file": func(c *relay.Config) { c.DedicatedServerKey = hexKey(key) },
		"key and file": func(c *relay.Config) {
			c.DedicatedServerKey, c.DedicatedServerKeyFile, c.BlockFile = hexKey(key), "/k", "/b"
		},
		"bad key": func(c *relay.Config) { c.DedicatedServerKey, c.BlockFile = "abcd", "/b" },
		"registers": func(c *relay.Config) {
			c.DedicatedServerKey, c.BlockFile, c.RegistryURLs, c.PublicURL = hexKey(key), "/b", []string{"http://m"}, "ws://r/ws"
		},
		"block file alone": func(c *relay.Config) { c.BlockFile = "/b" },
		"no networks":      func(c *relay.Config) { c.DedicatedServerKey, c.BlockFile, c.MaxBlockedNetworks = hexKey(key), "/b", 0 },
	} {
		cfg := relay.DefaultConfig()
		mutate(&cfg)
		if cfg.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
