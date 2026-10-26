package relayurl

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCheckPublic(t *testing.T) {
	for _, in := range []string{
		"wss://relay.example.org/ws",
		"ws://relay.example:7000/ws",
		"wss://8.8.8.8/ws",
		"wss://[2606:4700::1111]/ws",
		"wss://a.b.c.example.net",
	} {
		c, err := Canonical(in)
		if err != nil {
			t.Fatal(err)
		}
		if err := CheckPublic(c); err != nil {
			t.Errorf("CheckPublic(%q) = %v", c, err)
		}
	}
	for _, in := range []string{
		"ws://127.0.0.1:7000/ws",
		"ws://127.8.9.10/ws",
		"ws://[::1]/ws",
		"ws://10.0.0.5/ws",
		"ws://172.16.3.4/ws",
		"ws://192.168.1.1/ws",
		"ws://169.254.169.254/ws",
		"ws://[fe80::1]/ws",
		"ws://[fd00::1]/ws",
		"ws://0.0.0.0/ws",
		"ws://100.64.1.1/ws",
		"ws://224.0.0.1/ws",
		"ws://[::ffff:10.0.0.1]/ws",
		"ws://[64:ff9b::a00:1]/ws",
		"ws://localhost/ws",
		"ws://LOCALHOST./ws",
		"ws://foo.localhost/ws",
		"ws://printer.local/ws",
		"ws://metadata.google.internal/ws",
		"ws://router.lan/ws",
		"ws://nas.home.arpa/ws",
		"ws://redis:6379/ws",
		"ws://2130706433/ws",
		"ws://0x7f.1/ws",
		"ws://127.1/ws",
	} {
		c, err := Canonical(in)
		if err != nil {
			continue // not a relay URL at all
		}
		if err := CheckPublic(c); !errors.Is(err, ErrNotPublic) {
			t.Errorf("CheckPublic(%q) = %v, want ErrNotPublic", c, err)
		}
	}
}

// A dialer for relay URLs taken from others refuses non-public addresses
// after resolution; the development setting lets them through.
func TestDialerRefusesPrivateAddresses(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer hs.Close()
	get := func(allowPrivate bool) error {
		c := &http.Client{Transport: &http.Transport{DialContext: Dialer(allowPrivate, time.Second)}, Timeout: 5 * time.Second}
		resp, err := c.Get(hs.URL)
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	if err := get(false); !errors.Is(err, ErrNotPublic) {
		t.Fatalf("dial to %s: %v, want ErrNotPublic", hs.URL, err)
	}
	if err := get(true); err != nil {
		t.Fatalf("dial with private addresses allowed: %v", err)
	}
	// A name is judged by the address it resolves to.
	_, port, _ := net.SplitHostPort(hs.Listener.Addr().String())
	if _, err := Dialer(false, time.Second)(context.Background(), "tcp", net.JoinHostPort("localhost", port)); !errors.Is(err, ErrNotPublic) {
		t.Fatalf("dial to localhost: %v", err)
	}
}

func TestHTTPURL(t *testing.T) {
	for in, want := range map[string]string{
		"wss://relay.example/ws":      "https://relay.example/ws",
		"ws://127.0.0.1:7000/ws":      "http://127.0.0.1:7000/ws",
		"wss://[2001:db8::1]:8443/ws": "https://[2001:db8::1]:8443/ws",
	} {
		if got := HTTPURL(in); got != want {
			t.Errorf("HTTPURL(%q) = %q, want %q", in, got, want)
		}
	}
}
