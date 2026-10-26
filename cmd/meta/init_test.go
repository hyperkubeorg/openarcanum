package main

import (
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/config"
	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/meta"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

func initCmd(ctx context.Context, a []string, b *bytes.Buffer) error { return initCommand(ctx, a, b) }
func peersCmd(ctx context.Context, a []string, b *bytes.Buffer) error {
	return peersCommand(ctx, a, b)
}

func fetchItems(t *testing.T, url string, token []byte) []*wire.MailboxItem {
	t.Helper()
	b, _ := proto.Marshal(&wire.MailboxFetch{Token: token})
	resp, err := apiClient.Post(url+"/v1/mailbox/fetch", "application/x-protobuf", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var items wire.MailboxItems
	if err := proto.Unmarshal(body, &items); err != nil {
		t.Fatal(err)
	}
	return items.Items
}

// `meta init -replica-of` writes a config that trusts the root and follows
// its decisions, and prints the command to run on the root; once the root
// trusts the replica, the replica's first round pulls the root's mail.
// `meta peers status` then shows the root caught up.
func TestInitReplicaOf(t *testing.T) {
	ctx := context.Background()
	rootDir := t.TempDir()
	root := openMeta(t, rootDir)
	rs := httptest.NewServer(root.Handler())
	defer rs.Close()
	token := bytes.Repeat([]byte{7}, 32)
	post(t, rs.URL+"/v1/mailbox/drop", &wire.MailboxDrop{Inbox: meta.InboxAddress(token), Blob: []byte("waiting on the root")})

	dir := t.TempDir()
	out, err := runAdmin(t, initCmd, "-replica-of", rs.URL, "-data", dir, "-url", "https://m2.example.invalid")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	key, _, _ := meta.NodeKeys(dir)
	want := "meta trust add -data ROOT_DATA https://m2.example.invalid " + identity.Encode(key)
	if !strings.Contains(out, want) {
		t.Fatalf("init doesn't print the root's command %q:\n%s", want, out)
	}
	cfgPath := filepath.Join(dir, "meta.json")
	cfg := meta.DefaultConfig()
	cfg.DirectoryEnabled = true // the tests exercise the directory; it's off by default
	if err := config.Load(cfgPath, "OPENARCANUM_TEST_UNSET_", &cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	rootKey := identity.Encode(root.NodeKey())
	if len(cfg.TrustedPeers) != 1 || cfg.TrustedPeers[0] != rs.URL+"#"+rootKey || len(cfg.FollowDecisions) != 1 || cfg.FollowDecisions[0] != rootKey {
		t.Fatalf("written config: %+v", cfg)
	}
	if _, err := runAdmin(t, initCmd, "-replica-of", rs.URL, "-data", dir); err == nil {
		t.Fatal("init overwrote its config")
	}

	replica, err := meta.Open(ctx, cfg, testutil.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	defer replica.Close()
	rh := httptest.NewServer(replica.Handler())
	defer rh.Close()
	if _, err := runAdmin(t, trustCmd, "add", "-data", rootDir, rh.URL, identity.Encode(key)); err != nil {
		t.Fatal(err)
	}
	replica.PeerSyncOnce(ctx)
	if got := fetchItems(t, rh.URL, token); len(got) != 1 || string(got[0].Blob) != "waiting on the root" {
		t.Fatalf("the replica's first round didn't pull the root's mail: %v", got)
	}
	out, err = runAdmin(t, peersCmd, "status", "-data", dir)
	if err != nil || !strings.Contains(out, rs.URL+"  "+identity.Fingerprint(root.NodeKey())+"  lag 0") || !strings.Contains(out, "1 peers") {
		t.Fatalf("peers status: %v\n%s", err, out)
	}
}

// `meta directory disable|enable|status` on a running node's data
// directory.
func TestDirectorySwitchCommands(t *testing.T) {
	dir := t.TempDir()
	srv := openMeta(t, dir)
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()
	// The node's config turns the directory on (it's off by default).
	cfgFile := filepath.Join(t.TempDir(), "meta.json")
	if err := os.WriteFile(cfgFile, []byte(`{"directory_enabled": true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runAdmin(t, dirCmd, "status", "-data", dir, "-config", cfgFile)
	if err != nil || !strings.Contains(out, "public directory: on") {
		t.Fatalf("status: %v %s", err, out)
	}
	if out, err := runAdmin(t, dirCmd, "disable", "-data", dir, "-config", cfgFile); err != nil || !strings.Contains(out, "public directory off") {
		t.Fatalf("disable: %v %s", err, out)
	}
	resp, err := apiClient.Get(hs.URL + "/v1/search?q=x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != meta.DirectoryOff {
		t.Fatalf("search after disable: %d", resp.StatusCode)
	}
	if out, _ := runAdmin(t, dirCmd, "status", "-data", dir, "-config", cfgFile); !strings.Contains(out, "off (meta directory disable)") {
		t.Fatalf("status: %s", out)
	}
	if out, err := runAdmin(t, dirCmd, "enable", "-data", dir, "-config", cfgFile); err != nil || !strings.Contains(out, "public directory on") {
		t.Fatalf("enable: %v %s", err, out)
	}
}

// Without directory_enabled in its config a node lists nothing: the public
// directory is off by default.
func TestDirectoryOffByDefault(t *testing.T) {
	if meta.DefaultConfig().DirectoryEnabled {
		t.Fatal("directory_enabled defaults to on")
	}
	dir := t.TempDir()
	cfg := meta.DefaultConfig()
	cfg.DataDir = dir
	srv, err := meta.Open(context.Background(), cfg, testutil.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()
	resp, err := apiClient.Get(hs.URL + "/v1/search?q=x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != meta.DirectoryOff {
		t.Fatalf("search on a default node: %d", resp.StatusCode)
	}
	if out, _ := runAdmin(t, dirCmd, "status", "-data", dir); !strings.Contains(out, "public directory: off") {
		t.Fatalf("status: %s", out)
	}
}

// `meta init -from-backup` seeds a replica with the root's backup: the
// mail is there before the first round, and the replica has keys of its
// own.
func TestInitFromBackup(t *testing.T) {
	ctx := context.Background()
	rootDir := t.TempDir()
	root := openMeta(t, rootDir)
	rs := httptest.NewServer(root.Handler())
	defer rs.Close()
	token := bytes.Repeat([]byte{8}, 32)
	post(t, rs.URL+"/v1/mailbox/drop", &wire.MailboxDrop{Inbox: meta.InboxAddress(token), Blob: []byte("in the backup")})
	res, err := meta.Backup(ctx, rootDir, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "replica")
	out, err := runAdmin(t, initCmd, "-replica-of", rs.URL, "-data", dir, "-from-backup", res.Path)
	if err != nil || !strings.Contains(out, "seeded from") {
		t.Fatalf("init -from-backup: %v\n%s", err, out)
	}
	key, _, _ := meta.NodeKeys(dir)
	if bytes.Equal(key, root.NodeKey()) {
		t.Fatal("the seeded replica has the root's key")
	}
	cfg := meta.DefaultConfig()
	cfg.DirectoryEnabled = true // the tests exercise the directory; it's off by default
	if err := config.Load(filepath.Join(dir, "meta.json"), "OPENARCANUM_TEST_UNSET_", &cfg); err != nil {
		t.Fatal(err)
	}
	replica, err := meta.Open(ctx, cfg, testutil.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	defer replica.Close()
	rh := httptest.NewServer(replica.Handler())
	defer rh.Close()
	if got := fetchItems(t, rh.URL, token); len(got) != 1 {
		t.Fatal("the seeded replica doesn't hold the backup's mail")
	}
}
