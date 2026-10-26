package relayurl

import "testing"

func TestCanonicalVariantsAgree(t *testing.T) {
	const want = "wss://relay.example/ws"
	for _, in := range []string{
		"wss://relay.example/ws",
		"WSS://Relay.Example/ws",
		"wss://RELAY.EXAMPLE:443/ws",
		"wss://relay.example:0443/ws",
		"wss://relay.example./ws",
		"wss://relay.example/ws/",
		"wss://relay.example//ws",
		"wss://relay.example/x/../ws",
		"wss://relay.example/./ws",
	} {
		got, err := Canonical(in)
		if err != nil || got != want {
			t.Errorf("Canonical(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for in, want := range map[string]string{
		"ws://relay.example:80/ws":            "ws://relay.example/ws",
		"ws://relay.example:443/ws":           "ws://relay.example:443/ws",
		"wss://relay.example:8443/ws":         "wss://relay.example:8443/ws",
		"wss://relay.example":                 "wss://relay.example",
		"wss://relay.example/":                "wss://relay.example",
		"ws://127.0.0.1:7000/ws":              "ws://127.0.0.1:7000/ws",
		"ws://[2001:DB8::0001]:7000/ws":       "ws://[2001:db8::1]:7000/ws",
		"ws://[::ffff:192.0.2.1]/ws":          "ws://192.0.2.1/ws",
		"wss://relay.example/OpenArcanum/WS/": "wss://relay.example/OpenArcanum/WS",
	} {
		got, err := Canonical(in)
		if err != nil || got != want {
			t.Errorf("Canonical(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestCanonicalRefuses(t *testing.T) {
	for _, in := range []string{
		"",
		"https://relay.example/ws",
		"relay.example/ws",
		"wss:relay.example",
		"wss://user:pw@relay.example/ws",
		"wss://relay.example/ws?x=1",
		"wss://relay.example/ws?",
		"wss://relay.example/ws#frag",
		"wss://relay.example/w%73",
		"wss://relay.example:0/ws",
		"wss://relay.example:65536/ws",
		"wss://relay.example:/ws",
		"wss:///ws",
		"wss://-bad.example/ws",
		"wss://bad..example/ws",
		"wss://réseau.example/ws",
		"ws://[fe80::1%25eth0]/ws",
		"wss://" + string(make([]byte, 300)),
	} {
		if got, err := Canonical(in); err == nil {
			t.Errorf("Canonical(%q) = %q, want an error", in, got)
		}
	}
}

func TestHost(t *testing.T) {
	for in, want := range map[string]string{
		"wss://relay.example/ws":      "relay.example",
		"wss://relay.example:8443/ws": "relay.example",
		"ws://[2001:db8::1]:7000/ws":  "2001:db8::1",
	} {
		if got := Host(in); got != want {
			t.Errorf("Host(%q) = %q, want %q", in, got, want)
		}
	}
}

func FuzzCanonical(f *testing.F) {
	f.Add("wss://relay.example:443/ws/")
	f.Add("ws://[::1]:80/a/../b")
	f.Fuzz(func(t *testing.T, s string) {
		c, err := Canonical(s)
		if err != nil {
			return
		}
		// Canonical is idempotent.
		if again, err := Canonical(c); err != nil || again != c {
			t.Fatalf("Canonical(%q) = %q, then %q, %v", s, c, again, err)
		}
	})
}
