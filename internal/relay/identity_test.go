package relay_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/fanout"
	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/relay"
	"github.com/hyperkubeorg/openarcanum/internal/relayclient"
	"github.com/hyperkubeorg/openarcanum/internal/relayid"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Cross-relay endpoint hijack (docs/protocol.md §2 "Relay identity"): a
// malicious relay A presents relay B's Hello nonce as its own, gets a
// community server's endpoint registration and fan-out token signed for
// it, and replays both on B so that members' streams reach the attacker.
// B must refuse them.

func rawRead(t *testing.T, ctx context.Context, ws *websocket.Conn) *wire.RelayFrame {
	t.Helper()
	_, b, err := ws.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var f wire.RelayFrame
	if err := proto.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return &f
}

func rawWrite(t *testing.T, ctx context.Context, ws *websocket.Conn, f *wire.RelayFrame) {
	t.Helper()
	b, _ := proto.Marshal(f)
	if err := ws.Write(ctx, websocket.MessageBinary, b); err != nil {
		t.Fatal(err)
	}
}

// fakeRelay serves a relay that sends hello and acknowledges everything,
// handing what clients sign (registrations, fan-out tokens) to got.
func fakeRelay(t *testing.T, hello func(url string) *wire.Hello, got chan<- *wire.RelayFrame) string {
	t.Helper()
	var url string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		c := context.Background()
		hb, _ := proto.Marshal(&wire.RelayFrame{Kind: &wire.RelayFrame_Hello{Hello: hello(url)}})
		_ = ws.Write(c, websocket.MessageBinary, hb)
		for {
			_, b, err := ws.Read(c)
			if err != nil {
				return
			}
			var f wire.RelayFrame
			_ = proto.Unmarshal(b, &f)
			var req uint32
			switch k := f.Kind.(type) {
			case *wire.RelayFrame_Start:
				req = k.Start.Req
			case *wire.RelayFrame_RegisterEndpoint:
				req = k.RegisterEndpoint.Req
				got <- &f
			case *wire.RelayFrame_FanoutAuth:
				req = k.FanoutAuth.Req
				got <- &f
			}
			ab, _ := proto.Marshal(&wire.RelayFrame{Kind: &wire.RelayFrame_Ack{Ack: &wire.Ack{Req: req}}})
			_ = ws.Write(c, websocket.MessageBinary, ab)
		}
	}))
	t.Cleanup(hs.Close)
	url = "ws" + strings.TrimPrefix(hs.URL, "http") + "/ws"
	return url
}

// attackerOn opens a raw, started connection to the relay at url and
// returns it with the relay's Hello.
func attackerOn(t *testing.T, url string) (*websocket.Conn, *wire.Hello) {
	t.Helper()
	cx := ctx(t)
	atk, _, err := websocket.Dial(cx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { atk.CloseNow() })
	atk.SetReadLimit(64 << 20)
	hello := rawRead(t, cx, atk).GetHello()
	rawWrite(t, cx, atk, &wire.RelayFrame{Kind: &wire.RelayFrame_Start{Start: &wire.Start{Version: hello.Versions[len(hello.Versions)-1], Req: 1}}})
	if rawRead(t, cx, atk).GetAck() == nil {
		t.Fatal("no start ack")
	}
	return atk, hello
}

// hijack has the victim (a community server's relay client) attach to the
// fake relay at fakeURL the way the server does on every attach, replays
// whatever it got signed on the attacker's connection to the real relay,
// and checks the real relay refused all of it: a member's stream still
// reaches the real server, and never the attacker.
func hijack(t *testing.T, key *identity.Key, realB *relayclient.Client, urlB, fakeURL string, atk *websocket.Conn, got chan *wire.RelayFrame) (replayed int) {
	t.Helper()
	cx := ctx(t)
	if victim, err := relayclient.Dial(cx, fakeURL); err == nil {
		t.Cleanup(victim.Close)
		// What the community server does on every attach (attachLoop).
		// Refusing to sign is the expected outcome; anything signed is
		// replayed below.
		_ = victim.RegisterEndpoint(cx, key)
		_ = victim.FanoutAuth(cx, key, fanout.TokenLifetime)
	}
	req := uint32(100)
	for len(got) > 0 {
		replayed++
		f := <-got
		req++
		switch k := f.Kind.(type) {
		case *wire.RelayFrame_RegisterEndpoint:
			k.RegisterEndpoint.Req = req
		case *wire.RelayFrame_FanoutAuth:
			k.FanoutAuth.Req = req
		}
		rawWrite(t, cx, atk, f)
		r := rawRead(t, cx, atk)
		if r.GetAck().GetReq() == req {
			t.Errorf("the real relay accepted a replayed %T signed through the fake relay", f.Kind)
		}
	}

	// A member opening a stream to the community reaches the real server.
	member := dial(t, urlB)
	if _, err := member.OpenStream(cx, key.Public); err != nil {
		t.Fatal(err)
	}
	if _, err := realB.Accept(cx); err != nil {
		t.Fatalf("member stream didn't reach the real server: %v", err)
	}
	rctx, cancel := context.WithTimeout(cx, 300*time.Millisecond)
	defer cancel()
	if _, b, err := atk.Read(rctx); err == nil {
		var f wire.RelayFrame
		_ = proto.Unmarshal(b, &f)
		if f.GetStreamOpened() != nil {
			t.Fatal("HIJACKED: a member's stream to the community was delivered to the attacker")
		}
	}
	if realB.Err() != nil {
		t.Fatalf("real server's connection closed: %v", realB.Err())
	}
	return replayed
}

// The fake relay sends the real relay's whole Hello (its key, URL, nonce
// and proof) as its own.
func TestCrossRelayHijackCopiedHello(t *testing.T) {
	_, urlB := start(t, nil)
	key, _ := identity.Generate()
	realB := dial(t, urlB)
	if err := realB.RegisterEndpoint(ctx(t), key); err != nil {
		t.Fatal(err)
	}
	atk, helloB := attackerOn(t, urlB)
	got := make(chan *wire.RelayFrame, 8)
	fake := fakeRelay(t, func(string) *wire.Hello { return proto.Clone(helloB).(*wire.Hello) }, got)
	hijack(t, key, realB, urlB, fake, atk, got)
}

// The fake relay proves its own key and URL, with the real relay's nonce.
func TestCrossRelayHijackOwnKey(t *testing.T) {
	_, urlB := start(t, nil)
	key, _ := identity.Generate()
	realB := dial(t, urlB)
	if err := realB.RegisterEndpoint(ctx(t), key); err != nil {
		t.Fatal(err)
	}
	atk, helloB := attackerOn(t, urlB)
	got := make(chan *wire.RelayFrame, 8)
	relayKey, _ := identity.Generate()
	fake := fakeRelay(t, func(url string) *wire.Hello {
		h := proto.Clone(helloB).(*wire.Hello)
		h.RelayKey, h.Url = relayKey.Public, url
		h.RelaySignature = relayid.SignHello(relayKey, url, h.Nonce)
		return h
	}, got)
	// The victim does sign here (for the fake relay, which is what it
	// is), so the replays reach the real relay, which refuses them.
	if n := hijack(t, key, realB, urlB, fake, atk, got); n != 2 {
		t.Fatalf("replayed %d signed frames, want the registration and the token", n)
	}
}

// The real relay has no public_url, so it can't check URLs: a fake relay
// presenting its key, nonce and proof (which name no URL) at another URL
// gets nothing signed from a server that already knows that key at the
// real relay's URL.
func TestCrossRelayHijackRelayWithoutURL(t *testing.T) {
	cfg := relay.DefaultConfig()
	srv := relay.New(cfg, testutil.Quiet())
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	urlB := "ws" + strings.TrimPrefix(hs.URL, "http") + "/ws"
	if srv.URL() != "" {
		t.Fatal("relay has a URL")
	}
	key, _ := identity.Generate()
	realB := dial(t, urlB)
	if err := realB.RegisterEndpoint(ctx(t), key); err != nil {
		t.Fatal(err)
	}
	atk, helloB := attackerOn(t, urlB)
	got := make(chan *wire.RelayFrame, 8)
	fake := fakeRelay(t, func(string) *wire.Hello { return proto.Clone(helloB).(*wire.Hello) }, got)
	hijack(t, key, realB, urlB, fake, atk, got)
}

// Clients refuse a Hello without a valid proof of the relay's key.
func TestHelloProofRequired(t *testing.T) {
	_, urlB := start(t, nil)
	_, helloB := attackerOn(t, urlB)
	other, _ := identity.Generate()
	for name, mut := range map[string]func(h *wire.Hello){
		"no proof":      func(h *wire.Hello) { h.RelayKey, h.RelaySignature = nil, nil },
		"no key":        func(h *wire.Hello) { h.RelayKey = nil },
		"another key":   func(h *wire.Hello) { h.RelayKey = other.Public },
		"URL changed":   func(h *wire.Hello) { h.Url = "ws://127.0.0.1:1/ws" },
		"URL removed":   func(h *wire.Hello) { h.Url = "" },
		"nonce changed": func(h *wire.Hello) { h.Nonce = append([]byte{1}, h.Nonce[1:]...) },
		"bad signature": func(h *wire.Hello) {
			h.RelaySignature = append([]byte{h.RelaySignature[0] ^ 1}, h.RelaySignature[1:]...)
		},
		"short key":                 func(h *wire.Hello) { h.RelayKey = h.RelayKey[:16] },
		"unclean URL":               func(h *wire.Hello) { h.Url += "/" },
		"proof without the binding": func(h *wire.Hello) { h.RelaySignature = other.Sign(identity.CtxRelayHello, h.Nonce) },
	} {
		t.Run(name, func(t *testing.T) {
			got := make(chan *wire.RelayFrame, 8)
			fake := fakeRelay(t, func(string) *wire.Hello {
				h := proto.Clone(helloB).(*wire.Hello)
				mut(h)
				return h
			}, got)
			c, err := relayclient.Dial(ctx(t), fake)
			if err == nil {
				c.Close()
				t.Fatal("accepted")
			}
		})
	}
}

// A relay refuses registrations bound to another URL, another relay key,
// or in the v1 form (the nonce only).
func TestEndpointRegistrationBoundToRelay(t *testing.T) {
	srv, url := start(t, nil)
	key, _ := identity.Generate()
	other, _ := identity.Generate()
	atk, hello := attackerOn(t, url)
	cx := ctx(t)
	try := func(name string, msg []byte, regURL string, want bool) {
		t.Helper()
		rawWrite(t, cx, atk, &wire.RelayFrame{Kind: &wire.RelayFrame_RegisterEndpoint{RegisterEndpoint: &wire.RegisterEndpoint{
			EndpointKey: key.Public, Signature: key.Sign(identity.CtxRelayEndpoint, msg), Url: regURL, Req: 9,
		}}})
		r := rawRead(t, cx, atk)
		if ok := r.GetAck().GetReq() == 9; ok != want {
			t.Fatalf("%s: accepted %v, want %v (%v)", name, ok, want, r)
		}
	}
	b := relayid.Binding{RelayKey: hello.RelayKey, URL: url, Nonce: hello.Nonce}
	try("v1: nonce only", hello.Nonce, "", false)
	try("another relay's key", relayid.Binding{RelayKey: other.Public, URL: url, Nonce: hello.Nonce}.Bytes(), url, false)
	try("another URL", relayid.Binding{RelayKey: hello.RelayKey, URL: "ws://127.0.0.1:1/ws", Nonce: hello.Nonce}.Bytes(), "ws://127.0.0.1:1/ws", false)
	try("URL in the frame differs from the signed one", b.Bytes(), "ws://127.0.0.1:1/ws", false)
	try("bound", b.Bytes(), url, true)
	if srv.Key() == nil || string(srv.Key().Public) != string(hello.RelayKey) {
		t.Fatal("Hello doesn't carry the relay's key")
	}
}

// A client dialling a relay at a URL other than the one it announces gets
// a clear error instead of a registration the relay would refuse.
func TestRegisterAtAnotherURLFailsClearly(t *testing.T) {
	srv, url := start(t, nil)
	if err := srv.SetURL("wss://relay.example/ws"); err != nil {
		t.Fatal(err)
	}
	key, _ := identity.Generate()
	c := dial(t, url)
	err := c.RegisterEndpoint(ctx(t), key)
	if err == nil || !strings.Contains(err.Error(), "dial it at its own URL") {
		t.Fatalf("got %v", err)
	}
}
