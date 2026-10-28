package ops

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A status listener a monitoring server reaches over the network:
// /metrics needs the token, /healthz and /readyz don't.
func TestMetricsToken(t *testing.T) {
	reg := NewRegistry()
	reg.GaugeFunc("openarcanum_test_members", "Members.", func() float64 { return 7 })
	tok := strings.Repeat("t", MinMetricsToken)
	h := Status{Registry: reg, Token: tok}.Handler()
	get := func(path, auth string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, auth := range []string{"", "Bearer wrong", "Basic " + tok, tok} {
		if w := get("/metrics", auth); w.Code != http.StatusUnauthorized {
			t.Fatalf("/metrics with %q: %d", auth, w.Code)
		}
	}
	w := get("/metrics", "Bearer "+tok)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "openarcanum_test_members 7\n") {
		t.Fatalf("/metrics with the token: %d\n%s", w.Code, w.Body.String())
	}
	if w := get("/healthz", ""); w.Code != http.StatusOK {
		t.Fatalf("/healthz: %d", w.Code)
	}

	// The token file: one line, long enough.
	dir := t.TempDir()
	s := DefaultSettings()
	s.MetricsTokenFile = filepath.Join(dir, "token")
	if _, err := s.MetricsToken(); err == nil {
		t.Fatal("a missing token file was accepted")
	}
	for body, ok := range map[string]bool{"short\n": false, tok + "\n": true, "two words " + tok: false} {
		if err := os.WriteFile(s.MetricsTokenFile, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := s.MetricsToken()
		if (err == nil) != ok || (ok && got != tok) {
			t.Fatalf("%q: %q %v", body, got, err)
		}
	}
}
