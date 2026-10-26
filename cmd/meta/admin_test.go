package main

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/meta"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

func runAdmin(t *testing.T, cmd func(context.Context, []string, *bytes.Buffer) error, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := cmd(context.Background(), args, &out)
	return out.String(), err
}

func dirCmd(ctx context.Context, a []string, b *bytes.Buffer) error {
	return directoryCommand(ctx, a, b)
}
func relayCmd(ctx context.Context, a []string, b *bytes.Buffer) error {
	return relaysCommand(ctx, a, b)
}

// The directory and relays commands run against a live node's data
// directory.
func TestDirectoryAndRelaysCommands(t *testing.T) {
	dir := t.TempDir()
	cfg := meta.DefaultConfig()
	cfg.DirectoryEnabled = true // the tests exercise the directory; it's off by default
	cfg.DataDir = dir
	srv, err := meta.Open(context.Background(), cfg, testutil.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	// The relay URL below points nowhere; proof of control is tested in
	// internal/meta.
	srv.SetRelayProof(func(context.Context, string, []byte) error { return nil })
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	owner, _ := identity.Generate()
	server, _ := identity.Generate()
	cert, _ := identity.IssueServerCert(owner, server.Public, 1, nil, nil)
	l, _ := proto.Marshal(&wire.Listing{Community: owner.Public, ServerKey: server.Public, RouteId: bytes.Repeat([]byte{1}, 16), Name: "Chess Club", Seq: 1,
		PublishedAt: time.Now().UnixMilli()})
	post(t, hs.URL+"/v1/listings", &wire.SignedListing{Listing: l, Signature: server.Sign(identity.CtxListing, l), Cert: cert})
	relayKey, _ := identity.Generate()
	hb, _ := proto.Marshal(&wire.RelayHeartbeat{Url: "wss://r1.example.invalid/ws", RelayKey: relayKey.Public, At: time.Now().UnixMilli()})
	post(t, hs.URL+"/v1/relays/heartbeat", &wire.SignedHeartbeat{Heartbeat: hb, Signature: relayKey.Sign(identity.CtxRelayHeartbeat, hb)})

	id := identity.Encode(owner.Public)
	out, err := runAdmin(t, dirCmd, "list", "-data", dir, "-q", "chess")
	if err != nil || !strings.Contains(out, id) || !strings.Contains(out, "1 listings") {
		t.Fatalf("list: %q %v", out, err)
	}
	out, err = runAdmin(t, dirCmd, "delist", "-data", dir, "-id", id, "-reason", "abuse report 12")
	if err != nil || !strings.Contains(out, "removed 1") {
		t.Fatalf("delist: %q %v", out, err)
	}
	out, _ = runAdmin(t, dirCmd, "list", "-data", dir)
	if !strings.Contains(out, "0 listings") {
		t.Fatalf("after delist: %q", out)
	}
	out, _ = runAdmin(t, dirCmd, "decisions", "-data", dir)
	if !strings.Contains(out, "abuse report 12") {
		t.Fatalf("decisions: %q", out)
	}
	if out, err = runAdmin(t, dirCmd, "relist", "-data", dir, "-id", id); err != nil || !strings.Contains(out, "relisted") {
		t.Fatalf("relist: %q %v", out, err)
	}
	if _, err = runAdmin(t, dirCmd, "relist", "-data", dir, "-id", id); err == nil {
		t.Fatal("relisting twice succeeded")
	}

	out, err = runAdmin(t, relayCmd, "list", "-data", dir)
	if err != nil || !strings.Contains(out, "wss://r1.example.invalid/ws") {
		t.Fatalf("relays list: %q %v", out, err)
	}
	if out, err = runAdmin(t, relayCmd, "remove", "-data", dir, "-url", "wss://r1.example.invalid/ws"); err != nil || !strings.Contains(out, "removed") {
		t.Fatalf("remove: %q %v", out, err)
	}
	if out, _ = runAdmin(t, relayCmd, "list", "-data", dir); !strings.Contains(out, "0 relays") {
		t.Fatalf("after remove: %q", out)
	}
	if _, err = runAdmin(t, relayCmd, "restore", "-data", dir, "-url", "wss://r1.example.invalid/ws"); err != nil {
		t.Fatal(err)
	}

	// A config file's entries are shown and can't be lifted from here.
	cfgFile := filepath.Join(t.TempDir(), "meta.json")
	os.WriteFile(cfgFile, []byte(`{"data_dir": "`+dir+`", "delisted": ["`+id+`"]}`), 0o600)
	if out, _ = runAdmin(t, dirCmd, "decisions", "-data", dir, "-config", cfgFile); !strings.Contains(out, "config file") {
		t.Fatalf("config decisions: %q", out)
	}
	if _, err = runAdmin(t, dirCmd, "relist", "-data", dir, "-config", cfgFile, "-id", id); err == nil || !strings.Contains(err.Error(), "config file") {
		t.Fatalf("relisting a config entry: %v", err)
	}

	// Usage errors.
	for _, args := range [][]string{{}, {"list"}, {"frobnicate", "-data", dir}, {"delist", "-data", dir}, {"delist", "-data", dir, "-id", "nope"}} {
		if _, err := runAdmin(t, dirCmd, args...); err == nil {
			t.Errorf("directory %v succeeded", args)
		}
	}
	for _, args := range [][]string{{"remove", "-data", dir}, {"zap", "-data", dir}} {
		if _, err := runAdmin(t, relayCmd, args...); err == nil {
			t.Errorf("relays %v succeeded", args)
		}
	}
	if _, err := runAdmin(t, dirCmd, "list", "-data", t.TempDir()); err == nil {
		t.Error("ran on a directory without a database")
	}
}
