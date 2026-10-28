package community_test

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/ops"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func sample(t *testing.T, text, name string) string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if v, ok := strings.CutPrefix(line, name+" "); ok {
			return v
		}
	}
	t.Fatalf("no %s in\n%s", name, text)
	return ""
}

// The community server's status listener: metrics count sessions,
// members, messages and backups without naming anyone; /healthz follows
// the database and /readyz whether a relay is attached.
func TestCommunityMetricsAndHealth(t *testing.T) {
	settings := &wire.CommunitySettings{Name: "Quokka Collective", Visibility: wire.Visibility_VISIBILITY_PRIVATE,
		JoinPolicy: wire.JoinPolicy_JOIN_POLICY_INVITE_ONLY, MessageRetentionDays: 30, FileRetentionDays: 7, SearchEnabled: true}
	e := setup(t, settings)
	srv := e.comm.Server
	reg := ops.NewRegistry()
	srv.RegisterMetrics(reg)
	hs := httptest.NewServer(ops.Status{Registry: reg, Live: srv.Live, Ready: srv.Ready}.Handler())
	defer hs.Close()

	ctx := testutil.Ctx(t)
	bob := e.member("Bob Pademelon", e.invite.Secret)
	general := e.owner.Channel("general").Id
	for _, text := range []string{"a quokka secret", "another wallaby secret"} {
		if _, err := bob.Post(ctx, general, text); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := srv.Backup(context.Background()); err != nil {
		t.Fatal(err)
	}

	code, out := get(t, hs.URL+"/metrics")
	if code != 200 {
		t.Fatalf("metrics: %d", code)
	}
	if _, err := ops.CheckExposition(out); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if v := sample(t, out, "openarcanum_community_sessions"); v != "2" {
		t.Errorf("sessions = %s", v)
	}
	if v := sample(t, out, "openarcanum_community_members_online"); v != "2" {
		t.Errorf("members online = %s", v)
	}
	if v := sample(t, out, "openarcanum_community_members"); v != "2" {
		t.Errorf("members = %s", v)
	}
	if v := sample(t, out, "openarcanum_community_relays_attached"); v != "1" {
		t.Errorf("relays attached = %s", v)
	}
	// Two posts plus Bob's join notice.
	if v := sample(t, out, "openarcanum_community_messages_total"); v != "3" {
		t.Errorf("messages = %s", v)
	}
	if v := sample(t, out, "openarcanum_community_backups_total"); v != "1" {
		t.Errorf("backups = %s", v)
	}
	if v := sample(t, out, "openarcanum_community_backup_failures_total"); v != "0" {
		t.Errorf("backup failures = %s", v)
	}
	if v := sample(t, out, "openarcanum_community_unready"); v != "0" {
		t.Errorf("communities without a relay = %s", v)
	}
	if v := sample(t, out, "openarcanum_community_last_backup_timestamp_seconds"); v == "0" {
		t.Error("last backup time not set")
	}
	for _, secret := range [][]byte{[]byte("Quokka Collective"), []byte("Bob Pademelon"), []byte("general"), []byte("quokka secret"),
		bob.Identity.Public, e.ownerK.Public, srv.ServerKey(), []byte(e.invite.Secret)} {
		for _, form := range []string{string(secret), hex.EncodeToString(secret), base64.RawURLEncoding.EncodeToString(secret), identity.Fingerprint(secret)} {
			if strings.Contains(out, form) {
				t.Errorf("metrics contain %q", form)
			}
		}
	}
	if code, body := get(t, hs.URL+"/healthz"); code != 200 || body != "ok\n" {
		t.Fatalf("healthz: %d %q", code, body)
	}
	if code, _ := get(t, hs.URL+"/readyz"); code != 200 {
		t.Fatalf("readyz: %d", code)
	}

	// A failed backup is counted as such. A file where the backup directory
	// should be fails it for root and on Windows too, where a read-only
	// directory still takes new files.
	dir := srv.BackupDir()
	if err := os.Rename(dir, dir+".aside"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Backup(context.Background()); err == nil {
		t.Fatal("backup into a file instead of a directory succeeded")
	}
	_, out = get(t, hs.URL+"/metrics")
	if v := sample(t, out, "openarcanum_community_backup_failures_total"); v != "1" {
		t.Errorf("backups failed = %s", v)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir+".aside", dir); err != nil {
		t.Fatal(err)
	}

	// Without its relay the server is alive but not ready.
	e.relay.Stop()
	deadline := time.Now().Add(10 * time.Second)
	for {
		code, body := get(t, hs.URL+"/readyz")
		if code == 503 && strings.Contains(body, "no relay attached") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("readyz with the relay gone: %d %q", code, body)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if code, _ := get(t, hs.URL+"/healthz"); code != 200 {
		t.Fatalf("healthz with the relay gone: %d", code)
	}
	// Without its database it isn't alive either.
	e.comm.Stop()
	if code, body := get(t, hs.URL+"/healthz"); code != 503 || !strings.Contains(body, "database unavailable") {
		t.Fatalf("healthz after shutdown: %d %q", code, body)
	}
}

// Node settings shared by every server: a bad log or metrics setting in
// the community config is refused at start.
func TestCommunityOpsSettingsValidated(t *testing.T) {
	cfg := community.DefaultConfig(t.TempDir())
	cfg.Relays = []string{"ws://127.0.0.1:1/ws"}
	cfg.LogFormat = "yaml"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "log_format") {
		t.Fatalf("err = %v", err)
	}
	cfg.LogFormat = "json"
	cfg.MetricsListen = "not an address"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "metrics_listen") {
		t.Fatalf("err = %v", err)
	}
}
