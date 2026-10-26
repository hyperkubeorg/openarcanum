package relay_test

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/ops"
	"github.com/hyperkubeorg/openarcanum/internal/relay"
	"github.com/hyperkubeorg/openarcanum/internal/relayclient"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func scrape(t *testing.T, reg *ops.Registry) string {
	t.Helper()
	var buf bytes.Buffer
	if _, err := reg.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.CheckExposition(buf.String()); err != nil {
		t.Fatalf("%v\n%s", err, buf.String())
	}
	return buf.String()
}

func metricValue(t *testing.T, text, sample string) string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if v, ok := strings.CutPrefix(line, sample+" "); ok {
			return v
		}
	}
	t.Fatalf("no sample %s in\n%s", sample, text)
	return ""
}

// Typical traffic shows up in the metrics as counts, and neither the
// metrics nor the debug log carry topics, payloads or endpoint keys
// (docs/operations.md "Logs").
func TestRelayMetricsAndLogsCarryNoContent(t *testing.T) {
	var logs lockedBuffer
	log := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := relay.DefaultConfig()
	cfg.Listen = "127.0.0.1:0"
	cfg.MaxConnections = 3
	srv := relay.New(cfg, log)
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()
	url := "ws" + strings.TrimPrefix(hs.URL, "http") + "/ws"
	reg := ops.NewRegistry()
	srv.RegisterMetrics(reg)

	topic := []byte("secret-topic-armadillo")
	payload := []byte("secret payload about a pangolin")
	sub := dial(t, url)
	box := newInbox()
	if err := sub.Subscribe(ctx(t), topic, box.handler); err != nil {
		t.Fatal(err)
	}
	endpoint := dial(t, url)
	key, _ := identity.Generate()
	if err := endpoint.RegisterEndpoint(ctx(t), key); err != nil {
		t.Fatal(err)
	}
	pub := dial(t, url)
	for range 4 {
		if err := pub.Publish(topic, payload); err != nil {
			t.Fatal(err)
		}
	}
	box.wait(t, 4)
	// A fourth connection is over max_connections.
	if _, err := relayclient.Dial(ctx(t), url); err == nil {
		t.Fatal("connection over max_connections accepted")
	}

	out := scrape(t, reg)
	for sample, want := range map[string]string{
		"openarcanum_relay_connections":                "3",
		"openarcanum_relay_connections_accepted_total": "3",
		"openarcanum_relay_connections_refused_total":  "1",
		"openarcanum_relay_topics":                     "1",
		"openarcanum_relay_endpoints":                  "1",
		"openarcanum_relay_deliveries_total":           "4",
		"openarcanum_relay_slow_consumers_total":       "0",
		"openarcanum_relay_calls":                      "0",
		"openarcanum_relay_connections_unstarted":      "0",
		"openarcanum_relay_unstarted_refused_total":    "0",
		"openarcanum_relay_unstarted_evicted_total":    "0",
		"openarcanum_relay_cache_bytes":                "0",
		"openarcanum_relay_network_refused_total":      "0",
		"openarcanum_relay_subscriptions":              "1",
	} {
		if got := metricValue(t, out, sample); got != want {
			t.Errorf("%s = %s, want %s", sample, got, want)
		}
	}
	sub.Close()
	pub.Close()
	endpoint.Close()
	deadline := time.Now().Add(5 * time.Second)
	for srv.Connections() > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := metricValue(t, scrape(t, reg), "openarcanum_relay_connections"); got != "0" {
		t.Fatalf("connections after close = %s", got)
	}

	all := logs.String() + out
	if !strings.Contains(logs.String(), "endpoint registered") {
		t.Fatalf("expected debug records; log:\n%s", logs.String())
	}
	for _, secret := range [][]byte{topic, payload, key.Public} {
		for _, form := range []string{string(secret), hex.EncodeToString(secret), base64.StdEncoding.EncodeToString(secret),
			base64.RawURLEncoding.EncodeToString(secret), identity.Encode(secret)} {
			if strings.Contains(all, form) {
				t.Errorf("log or metrics contain %q", form)
			}
		}
	}
}

// The status listener on a relay answers health checks and serves the
// relay's metrics next to the process metrics.
func TestRelayStatusListener(t *testing.T) {
	srv, _ := start(t, nil)
	reg := ops.NewRegistry()
	srv.RegisterMetrics(reg)
	hs := httptest.NewServer(ops.Status{Registry: reg}.Handler())
	defer hs.Close()
	res, err := http.Get(hs.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(res.Body)
	types, err := ops.CheckExposition(buf.String())
	if err != nil {
		t.Fatal(err)
	}
	if types["openarcanum_relay_connections"] != "gauge" || types["openarcanum_relay_deliveries_total"] != "counter" || types["go_goroutines"] != "gauge" {
		t.Fatalf("families: %v", types)
	}
	for _, p := range []string{"/healthz", "/readyz"} {
		r, err := http.Get(hs.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != 200 {
			t.Fatalf("%s: %d", p, r.StatusCode)
		}
	}
}
