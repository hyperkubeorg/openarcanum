package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// posixModes: Windows has no POSIX mode bits (files there report 0666, or
// 0444 when read-only; privacy comes from the profile's ACLs), so mode
// assertions only hold elsewhere.
const posixModes = runtime.GOOS != "windows"

func readConfig(t *testing.T, dir string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// opens checks the created directory is a working host with one community.
func opens(t *testing.T, dir string) {
	t.Helper()
	cfg, err := loadConfig(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	host, err := community.OpenHost(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("created server doesn't open: %v", err)
	}
	defer host.Close()
	if n := len(host.Servers()); n != 1 {
		t.Fatalf("%d communities, want 1", n)
	}
}

func TestInitFromFlags(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "srv")
	var out bytes.Buffer
	err := initCommand([]string{"-data", dir, "-relay", "wss://relay.example.invalid/ws", "-maintainer", "https://maint.example.invalid",
		"-install", "vps", "-metrics-listen", "127.0.0.1:7201"}, strings.NewReader(""), &out, false)
	if err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{"openarcanum-claim:", "Server key:", "hex, for a dedicated relay: ", "no inbound port", "http://127.0.0.1:7201/healthz", filepath.Join(dir, "storage.key")} {
		if !strings.Contains(s, want) {
			t.Errorf("output lacks %q:\n%s", want, s)
		}
	}
	c := readConfig(t, dir)
	if c["metrics_listen"] != "127.0.0.1:7201" || c["relay_mode"] != "pinned" || c["log_format"] != "text" {
		t.Fatalf("config: %v", c)
	}
	opens(t, dir)
	// A second init of the same directory is refused.
	if err := initCommand([]string{"-data", dir, "-relay", "wss://relay.example.invalid/ws"}, strings.NewReader(""), &out, false); err == nil || !strings.Contains(err.Error(), "already initialized") {
		t.Fatalf("re-init: %v", err)
	}
}

// For container start-up: -if-needed makes a second run a no-op, and the
// claim code kept in -claim-file is printed again until the file is gone.
func TestInitIfNeededAndClaimFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "srv")
	claim := filepath.Join(t.TempDir(), "claim.txt")
	args := []string{"-data", dir, "-relay", "ws://127.0.0.1:7000/ws", "-claim-file", claim, "-if-needed"}
	var first bytes.Buffer
	if err := initCommand(args, strings.NewReader(""), &first, false); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(claim)
	if err != nil {
		t.Fatal(err)
	}
	code := strings.TrimSpace(string(b))
	if !strings.HasPrefix(code, "openarcanum-claim:") || !strings.Contains(first.String(), code) {
		t.Fatalf("claim file %q, output:\n%s", code, first.String())
	}
	if st, err := os.Stat(claim); err != nil || (posixModes && st.Mode().Perm() != 0o600) {
		t.Fatalf("claim file mode: %v %v", st.Mode(), err)
	}
	key, _ := os.ReadFile(filepath.Join(dir, "server.key"))
	var again bytes.Buffer
	if err := initCommand(args, strings.NewReader(""), &again, false); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if !strings.Contains(again.String(), "already initialized") || !strings.Contains(again.String(), code) {
		t.Fatalf("second run output:\n%s", again.String())
	}
	if key2, _ := os.ReadFile(filepath.Join(dir, "server.key")); !bytes.Equal(key, key2) {
		t.Fatal("second run replaced the server key")
	}
	// Once the file is deleted (after claiming), nothing is printed.
	if err := os.Remove(claim); err != nil {
		t.Fatal(err)
	}
	var third bytes.Buffer
	if err := initCommand(args, strings.NewReader(""), &third, false); err != nil || strings.Contains(third.String(), "openarcanum-claim:") {
		t.Fatalf("third run: %v\n%s", err, third.String())
	}
	opens(t, dir)
}

func TestInitFlagErrors(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "srv")
	cases := map[string][]string{
		"-interactive":                {"-relay", "wss://r.example.invalid/ws"},
		"needs at least one relay":    {"-data", dir},
		"must be a ws:// or wss://":   {"-data", dir, "-relay", "http://r.example.invalid"},
		"-install must be":            {"-data", dir, "-relay", "wss://r.example.invalid/ws", "-install", "dmz"},
		"metrics_listen":              {"-data", dir, "-relay", "wss://r.example.invalid/ws", "-metrics-listen", "7201"},
		"needs at least one maintain": {"-data", dir, "-automatic-relays"},
		"unexpected argument":         {"-data", dir, "extra"},
	}
	for want, args := range cases {
		err := initCommand(args, strings.NewReader(""), &bytes.Buffer{}, false)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v: err = %v, want %q", args, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "server.key")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a refused init created a server")
	}
}

// The guided setup, driven through stdin as a person would: invalid
// answers are explained and asked again.
func TestInitGuidedHome(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "srv")
	keyFile := filepath.Join(base, "usb", "storage.key")
	answers := strings.Join([]string{
		"",                               // install type: home (default)
		dir,                              // data directory
		"sideways",                       // relays: invalid choice
		"pinned",                         //
		"http://relay.example.invalid",   // not a relay URL
		"wss://relay.example.invalid/ws", //
		"",                               // end of relay list
		"ftp://maint.example.invalid",    // not a maintainer URL
		"https://maint.example.invalid, https://maint2.example.invalid",
		"",             // end of maintainer list
		"relative.key", // storage key: not absolute
		keyFile,        //
		"y",            // health/metrics listener
		"",             // create: yes (default)
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := initCommand(nil, strings.NewReader(answers), &out, true); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	s := out.String()
	for _, want := range []string{
		"do NOT put this machine in your router's DMZ",
		"you do NOT need port forwarding",
		"answer one of: auto, pinned",
		`"http://relay.example.invalid" must be a ws:// or wss:// URL`,
		`"ftp://maint.example.invalid" is not an http(s):// URL`,
		"use an absolute path",
		"Summary",
		"openarcanum-claim:",
		"no port forwarding and no DMZ",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("output lacks %q:\n%s", want, s)
		}
	}
	c := readConfig(t, dir)
	if c["storage_key_file"] != keyFile || c["metrics_listen"] != defaultCommunityMetrics || c["relay_mode"] != "pinned" {
		t.Fatalf("config: %v", c)
	}
	if r := c["relays"].([]any); len(r) != 1 || r[0] != "wss://relay.example.invalid/ws" {
		t.Fatalf("relays: %v", r)
	}
	if m := c["maintainers"].([]any); len(m) != 2 {
		t.Fatalf("maintainers: %v", m)
	}
	if st, err := os.Stat(keyFile); err != nil || (posixModes && st.Mode().Perm() != 0o600) {
		t.Fatalf("storage key: %v %v", st, err)
	}
	opens(t, dir)
}

// VPS from a flag, automatic relays: a maintainer is required, and the
// claim code names the relays it lists (the app reaches the unclaimed
// server through them).
func TestInitGuidedVPSAutomatic(t *testing.T) {
	maint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/relays" {
			http.NotFound(w, r)
			return
		}
		b, _ := proto.Marshal(&wire.RelayList{Relays: []*wire.RelayInfo{{Url: "wss://r1.example.invalid/ws"}}})
		_, _ = w.Write(b)
	}))
	defer maint.Close()
	dir := filepath.Join(t.TempDir(), "srv")
	answers := strings.Join([]string{
		"auto",
		"", // no maintainer: refused, automatic relays need one
		maint.URL,
		"",
		"", // storage key: default
		"", // health listener: no (default)
		"y",
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := initCommand([]string{"-interactive", "-install", "vps", "-data", dir}, strings.NewReader(answers), &out, false); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	s := out.String()
	if strings.Contains(s, "Where does this server run?") || !strings.Contains(s, "Open no inbound port") || !strings.Contains(s, "enter at least 1") {
		t.Fatalf("output:\n%s", s)
	}
	c := readConfig(t, dir)
	if c["relay_mode"] != "automatic" || c["metrics_listen"] != "" || c["storage_key_file"] != filepath.Join(dir, "storage.key") {
		t.Fatalf("config: %v", c)
	}
	if code := claimCode(t, s); len(code.Relays) != 1 || code.Relays[0] != "wss://r1.example.invalid/ws" {
		t.Fatalf("claim code relays %v", code.Relays)
	}
	opens(t, dir)
}

// claimCode reads the claim code init printed.
func claimCode(t *testing.T, out string) *wire.ClaimCode {
	t.Helper()
	i := strings.Index(out, community.ClaimPrefix)
	if i < 0 {
		t.Fatalf("no claim code in:\n%s", out)
	}
	raw, err := identity.Decode(strings.Fields(out[i+len(community.ClaimPrefix):])[0])
	if err != nil {
		t.Fatal(err)
	}
	var c wire.ClaimCode
	if err := proto.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return &c
}

// Cancelling, or stdin ending early, creates nothing.
func TestInitGuidedCancelAndEOF(t *testing.T) {
	for name, answers := range map[string]string{
		"cancel":       "vps\nDIR\npinned\nwss://r.example.invalid/ws\n\n\n\nn\nn\n",
		"eof":          "home\nDIR\npinned\n",
		"eof midline":  "home\nDIR\npinned\nwss://r.example.invalid/ws",
		"empty stdin":  "",
		"bad then eof": "home\nDIR\nsideways",
	} {
		dir := filepath.Join(t.TempDir(), "srv")
		var out bytes.Buffer
		err := initCommand([]string{"-interactive"}, strings.NewReader(strings.ReplaceAll(answers, "DIR", dir)), &out, false)
		if err == nil {
			t.Errorf("%s: no error\n%s", name, out.String())
			continue
		}
		if !strings.Contains(err.Error(), "nothing was created") {
			t.Errorf("%s: err = %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "server.key")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: a server was created", name)
		}
	}
}
