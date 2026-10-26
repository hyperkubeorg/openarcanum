package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/config"
)

// posixModes: Windows has no POSIX mode bits (files there report 0666, or
// 0444 when read-only; privacy comes from the profile's ACLs), so mode
// assertions only hold elsewhere.
const posixModes = runtime.GOOS != "windows"

func CheckExpositionT(t testing.TB, text string) map[string]string {
	t.Helper()
	types, err := CheckExposition(text)
	if err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	return types
}

// The checker itself refuses what isn't the format.
func TestCheckExpositionRefuses(t *testing.T) {
	for _, bad := range []string{
		"x 1\n",                               // sample before HELP/TYPE
		"# HELP x h\n# TYPE x summary\nx 1\n", // unsupported type
		"# HELP x h\n# TYPE x counter\ny 1\n", // sample outside its family
		"# HELP x h\n# TYPE x counter\nx{a=\"b} 1\n",
		"# HELP x h\n# TYPE x counter\nx 1\n# HELP x h\n# TYPE x counter\n",
		"# HELP x h\n# TYPE x counter\nx one\n",
	} {
		if _, err := CheckExposition(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestExpositionFormat(t *testing.T) {
	r := &Registry{metrics: map[string]collector{}}
	c := r.Counter("test_events_total", "Events.\nSecond line with \\ backslash.")
	c.Add(3)
	g := r.Gauge("test_level", "Level.")
	g.Set(-1.5)
	r.GaugeFunc("test_func", "From a function.", func() float64 { return 7 })
	v := r.CounterVec("test_results_total", "Results.", Labels{Names: []string{"outcome"}, Values: [][]string{{"ok", `fa"il`}}})
	v.With("ok").Inc()
	v.With(`fa"il`).Add(2)
	h := r.HistogramVec("test_seconds", "Durations.", []float64{0.1, 1}, Labels{Names: []string{"route"}, Values: [][]string{{"a"}}})
	h.With("a").Observe(0.05)
	h.With("a").Observe(0.1)
	h.With("a").Observe(5)
	var buf bytes.Buffer
	if _, err := r.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	want := `# HELP test_events_total Events.\nSecond line with \\ backslash.
# TYPE test_events_total counter
test_events_total 3
# HELP test_func From a function.
# TYPE test_func gauge
test_func 7
# HELP test_level Level.
# TYPE test_level gauge
test_level -1.5
# HELP test_results_total Results.
# TYPE test_results_total counter
test_results_total{outcome="fa\"il"} 2
test_results_total{outcome="ok"} 1
# HELP test_seconds Durations.
# TYPE test_seconds histogram
test_seconds_bucket{route="a",le="0.1"} 2
test_seconds_bucket{route="a",le="1"} 2
test_seconds_bucket{route="a",le="+Inf"} 3
test_seconds_sum{route="a"} 5.15
test_seconds_count{route="a"} 3
`
	if buf.String() != want {
		t.Fatalf("exposition:\n%s\nwant:\n%s", buf.String(), want)
	}
	CheckExpositionT(t, buf.String())
	// The standard registry's process metrics are valid too.
	buf.Reset()
	if _, err := NewRegistry().WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	if types := CheckExpositionT(t, buf.String()); types["go_goroutines"] != "gauge" || types["process_start_time_seconds"] != "gauge" {
		t.Fatalf("process metrics: %v", types)
	}
}

func mustPanic(t *testing.T, what string, f func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s did not panic", what)
		}
	}()
	f()
}

// Label values are declared up front; anything else is a programming
// error, so request data can never become a label.
func TestLabelValuesAreDeclared(t *testing.T) {
	r := NewRegistry()
	v := r.CounterVec("x_total", "x", Labels{Names: []string{"outcome"}, Values: [][]string{{"ok"}}})
	mustPanic(t, "undeclared value", func() { v.With("someone@example.invalid") })
	mustPanic(t, "wrong arity", func() { v.With("ok", "ok") })
	mustPanic(t, "duplicate metric", func() { r.Counter("x_total", "again") })
	mustPanic(t, "bad metric name", func() { r.Counter("x-y", "bad") })
	mustPanic(t, "reserved label", func() { r.CounterVec("y_total", "y", Labels{Names: []string{"le"}, Values: [][]string{{"1"}}}) })
	mustPanic(t, "negative counter", func() { r.Counter("z_total", "z").Add(-1) })
}

// The route label is the mux pattern, never the request path (which can
// carry ids).
func TestInstrumentUsesRoutePatterns(t *testing.T) {
	r := NewRegistry()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/listings/{id}", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("x")) })
	mux.HandleFunc("POST /v1/drop", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTooManyRequests) })
	h := Instrument(mux, r, "test")
	for _, req := range []*http.Request{
		httptest.NewRequest("GET", "/v1/listings/c2VjcmV0LWNvbW11bml0eS1pZA", nil),
		httptest.NewRequest("POST", "/v1/drop", nil),
		httptest.NewRequest("GET", "/nothing/here?q=private-search", nil),
	} {
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	var buf bytes.Buffer
	_, _ = r.WriteTo(&buf)
	out := buf.String()
	CheckExpositionT(t, out)
	for _, want := range []string{
		`test_http_requests_total{route="GET /v1/listings/{id}",code="200"} 1`,
		`test_http_requests_total{route="POST /v1/drop",code="429"} 1`,
		`test_http_requests_total{route="unmatched",code="404"} 1`,
		`test_http_request_duration_seconds_count{route="POST /v1/drop"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in\n%s", want, out)
		}
	}
	for _, leak := range []string{"c2VjcmV0", "private-search", "/nothing"} {
		if strings.Contains(out, leak) {
			t.Errorf("metrics contain request data %q", leak)
		}
	}
}

func TestStatusEndpoints(t *testing.T) {
	r := NewRegistry()
	r.Counter("node_things_total", "Things.").Inc()
	ready := errors.New("no relay attached")
	st := Status{Registry: r, Live: func(context.Context) error { return nil }, Ready: func(context.Context) error { return ready }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr, err := ServeStatus(ctx, "127.0.0.1:0", st, nil)
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + addr.String()
	get := func(path string) (int, string, string) {
		res, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, res.Header.Get("Content-Type"), string(b)
	}
	if code, _, body := get("/healthz"); code != 200 || body != "ok\n" {
		t.Fatalf("healthz %d %q", code, body)
	}
	if code, _, body := get("/readyz"); code != 503 || !strings.Contains(body, "no relay attached") {
		t.Fatalf("readyz %d %q", code, body)
	}
	ready = nil
	if code, _, _ := get("/readyz"); code != 200 {
		t.Fatalf("readyz after recovery %d", code)
	}
	code, ctype, body := get("/metrics")
	if code != 200 || ctype != ContentType || !strings.Contains(body, "node_things_total 1") {
		t.Fatalf("metrics %d %q %q", code, ctype, body)
	}
	CheckExpositionT(t, body)
	if code, _, _ := get("/other"); code != 404 {
		t.Fatalf("unknown path %d", code)
	}
	// The healthcheck command (container HEALTHCHECK) agrees.
	var out, errOut bytes.Buffer
	if rc := Healthcheck([]string{base + "/healthz"}, &out, &errOut); rc != 0 || out.String() != "200 ok\n" {
		t.Fatalf("healthcheck rc %d out %q err %q", rc, out.String(), errOut.String())
	}
	ready = errors.New("database unavailable")
	if rc := Healthcheck([]string{base + "/readyz"}, &out, &errOut); rc != 1 {
		t.Fatalf("healthcheck of a failing check: rc %d", rc)
	}
	if rc := Healthcheck(nil, &out, &errOut); rc != 2 {
		t.Fatalf("healthcheck without URL: rc %d", rc)
	}
	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if rc := Healthcheck([]string{base + "/healthz"}, io.Discard, io.Discard); rc == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("status listener still up after its context ended")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// An address in use is an error, not a silent no-op.
	busy, err := ServeStatus(context.Background(), "127.0.0.1:0", st, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ServeStatus(context.Background(), busy.String(), st, nil); err == nil {
		t.Fatal("second listener on a busy address")
	}
}

func TestExposedAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:7101": false, "localhost:7101": false, "[::1]:7101": false, "127.0.0.2:7101": false,
		":7100": true, "0.0.0.0:7100": true, "[::]:7100": true, "10.0.0.5:7100": true, "203.0.113.7:443": true,
		"not an address": false,
	} {
		if got := ExposedAddr(addr); got != want {
			t.Errorf("ExposedAddr(%q) = %v, want %v", addr, got, want)
		}
	}
}

// The relay and the maintainer node warn at start when they serve other
// machines without TLS; the status listener when it isn't on loopback.
func TestListenerWarnings(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	plain := DefaultTLSSettings()
	acme := TLSSettings{TLSMode: TLSACME, TLSDomains: []string{"relay.example.invalid"}}
	for _, c := range []struct {
		addr string
		tls  TLSSettings
		warn bool
	}{
		{":7000", plain, true},
		{"0.0.0.0:7000", plain, true},
		{"127.0.0.1:7000", plain, false},
		{":443", acme, false},
	} {
		buf.Reset()
		WarnPlainListener(log, c.addr, c.tls)
		if got := strings.Contains(buf.String(), "tls_mode is off"); got != c.warn {
			t.Errorf("WarnPlainListener(%q, %s): warned %v, want %v: %s", c.addr, c.tls.TLSMode, got, c.warn, buf.String())
		}
	}
	WarnPlainListener(nil, ":7000", plain) // no logger: nothing to do

	st := Status{Registry: NewRegistry()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	buf.Reset()
	if _, err := ServeStatus(ctx, "127.0.0.1:0", st, log); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "metrics_listen") {
		t.Fatalf("loopback status listener warned: %s", buf.String())
	}
	if _, err := ServeStatus(ctx, ":0", st, log); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "metrics_listen is not on loopback") {
		t.Fatalf("status listener on every interface did not warn: %s", buf.String())
	}
}

func TestSettingsValidate(t *testing.T) {
	ok := DefaultSettings()
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*Settings){
		"format":       func(s *Settings) { s.LogFormat = "xml" },
		"level":        func(s *Settings) { s.LogLevel = "trace" },
		"size":         func(s *Settings) { s.LogMaxSize = 1000 },
		"age":          func(s *Settings) { s.LogMaxAge = config.Duration{Duration: time.Second} },
		"keep":         func(s *Settings) { s.LogKeep = 0 },
		"listen":       func(s *Settings) { s.MetricsListen = "7101" },
		"listen host":  func(s *Settings) { s.MetricsListen = "metrics.example.invalid:7101" },
		"listen empty": func(s *Settings) { s.MetricsListen = ":" },
	} {
		s := DefaultSettings()
		mut(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: accepted %+v", name, s)
		}
	}
	for _, addr := range []string{"127.0.0.1:7101", "[::1]:7101", ":7101", "localhost:0"} {
		s := DefaultSettings()
		s.MetricsListen = addr
		if err := s.Validate(); err != nil {
			t.Errorf("%s refused: %v", addr, err)
		}
	}
}

// ---------------------------------------------------------------- rotation

func TestRotationBySize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node.log")
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	r, err := OpenRotating(path, 100, 0, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.now = func() time.Time { now = now.Add(time.Millisecond); return now }
	defer r.Close()
	record := []byte(strings.Repeat("x", 39) + "\n") // 40 bytes
	for range 10 {
		if _, err := r.Write(record); err != nil {
			t.Fatal(err)
		}
	}
	rot := r.Rotated()
	if len(rot) != 2 {
		t.Fatalf("rotated files kept: %v", rot)
	}
	for _, p := range append(rot, path) {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if len(b) == 0 || len(b) > 100 || len(b)%40 != 0 {
			t.Fatalf("%s holds %d bytes: records split or over the limit", p, len(b))
		}
		if st, _ := os.Stat(p); posixModes && st.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v", p, st.Mode())
		}
	}
	// 10 records, 2 per file: the live file plus 2 rotated hold the newest 6.
	if cur, _ := os.ReadFile(path); len(cur) != 80 {
		t.Fatalf("current file %d bytes", len(cur))
	}
}

func TestRotationByAge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node.log")
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	r, err := OpenRotating(path, 1<<20, time.Hour, 5, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.now = clock
	r.opened = now
	_, _ = r.Write([]byte("first\n"))
	now = now.Add(59 * time.Minute)
	_, _ = r.Write([]byte("second\n"))
	if len(r.Rotated()) != 0 {
		t.Fatal("rotated before log_max_age")
	}
	now = now.Add(time.Minute)
	_, _ = r.Write([]byte("third\n"))
	rot := r.Rotated()
	if len(rot) != 1 || !strings.HasSuffix(rot[0], ".20261024T130000.000Z") {
		t.Fatalf("rotated: %v", rot)
	}
	if b, _ := os.ReadFile(rot[0]); string(b) != "first\nsecond\n" {
		t.Fatalf("rotated file %q", b)
	}
	if b, _ := os.ReadFile(path); string(b) != "third\n" {
		t.Fatalf("current file %q", b)
	}
	r.Close()

	// Reopening a non-empty file older than log_max_age rotates it first;
	// a recent one is appended to.
	old := now.Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	r2, err := OpenRotating(path, 1<<20, time.Hour, 5, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = r2.Write([]byte("after restart\n"))
	r2.Close()
	if b, _ := os.ReadFile(path); string(b) != "after restart\n" || len(r2.Rotated()) != 2 {
		t.Fatalf("reopen of an old file: %q, %v", b, r2.Rotated())
	}
	r3, err := OpenRotating(path, 1<<20, time.Hour, 5, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = r3.Write([]byte("appended\n"))
	r3.Close()
	if b, _ := os.ReadFile(path); string(b) != "after restart\nappended\n" {
		t.Fatalf("reopen of a recent file: %q", b)
	}
}

// When the directory can't be written (rotation can't rename), logging
// carries on in the current file and the failure is reported once.
func TestRotationFailureKeepsLogging(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := filepath.Join(t.TempDir(), "logs")
	path := filepath.Join(dir, "node.log")
	var notices bytes.Buffer
	r, err := OpenRotating(path, 1<<20, 0, 3, &notices)
	if err != nil {
		t.Fatal(err)
	}
	r.maxSize = 10
	defer r.Close()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dir, 0o700) }()
	for range 3 {
		if _, err := r.Write([]byte("0123456789\n")); err != nil {
			t.Fatalf("write after failed rotation: %v", err)
		}
	}
	if n := strings.Count(notices.String(), "log rotation failed"); n != 1 {
		t.Fatalf("notices: %q", notices.String())
	}
	if b, _ := os.ReadFile(path); len(b) != 33 {
		t.Fatalf("current file has %d bytes", len(b))
	}
}

func TestNewLoggerJSONToFile(t *testing.T) {
	s := DefaultSettings()
	s.LogFormat = "json"
	s.LogFile = filepath.Join(t.TempDir(), "sub", "relay.log")
	log, closeFn, err := NewLogger(s, false, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	log.Debug("hidden at info")
	log.Info("relay listening", "addr", "127.0.0.1:7000")
	if err := closeFn(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(s.LogFile)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 1 {
		t.Fatalf("lines: %q", lines)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil || rec["msg"] != "relay listening" || rec["level"] != "INFO" {
		t.Fatalf("record %q: %v", lines[0], err)
	}
	// Debug level from the setting, text format to stderr.
	var stderr bytes.Buffer
	s2 := DefaultSettings()
	s2.LogLevel = "debug"
	log2, _, err := NewLogger(s2, false, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	log2.Debug("visible", "k", "v")
	if !strings.Contains(stderr.String(), "level=DEBUG msg=visible k=v") {
		t.Fatalf("text debug: %q", stderr.String())
	}
	// A log file in a place that can't be created is an error at start.
	s3 := DefaultSettings()
	s3.LogFile = filepath.Join(s.LogFile, "not-a-dir", "x.log")
	if _, _, err := NewLogger(s3, false, io.Discard); err == nil {
		t.Fatal("unusable log_file accepted")
	}
}
