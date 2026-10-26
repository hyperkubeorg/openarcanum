package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/meta"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
)

func idCmd(ctx context.Context, a []string, b *bytes.Buffer) error    { return idCommand(ctx, a, b) }
func trustCmd(ctx context.Context, a []string, b *bytes.Buffer) error { return trustCommand(ctx, a, b) }

func openMeta(t *testing.T, dir string) *meta.Server {
	t.Helper()
	cfg := meta.DefaultConfig()
	cfg.DirectoryEnabled = true // the tests exercise the directory; it's off by default
	cfg.DataDir = dir
	srv, err := meta.Open(context.Background(), cfg, testutil.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv
}

// `meta id` prints the running node's key; `meta trust` adds, lists and
// removes trusted nodes on a live node's data directory.
func TestIDAndTrustCommands(t *testing.T) {
	dir := t.TempDir()
	srv := openMeta(t, dir)
	out, err := runAdmin(t, idCmd, "-data", dir)
	if err != nil || !strings.Contains(out, identity.Encode(srv.NodeKey())) || !strings.Contains(out, identity.Fingerprint(srv.NodeKey())) {
		t.Fatalf("meta id: %v\n%s", err, out)
	}
	other, _ := identity.Generate()
	key := identity.Encode(other.Public)
	if out, err := runAdmin(t, trustCmd, "add", "-data", dir, "https://m2.example.invalid/", key); err != nil || !strings.Contains(out, "trusting") {
		t.Fatalf("trust add: %v %s", err, out)
	}
	out, err = runAdmin(t, trustCmd, "list", "-data", dir)
	if err != nil || !strings.Contains(out, key+"  https://m2.example.invalid  trust") {
		t.Fatalf("trust list: %v\n%s", err, out)
	}
	if _, err := runAdmin(t, trustCmd, "add", "-data", dir, "ftp://x", key); err == nil {
		t.Fatal("a non-http URL was trusted")
	}
	if _, err := runAdmin(t, trustCmd, "add", "-data", dir, "https://m3.example.invalid", "short"); err == nil {
		t.Fatal("a malformed key was trusted")
	}
	if _, err := runAdmin(t, trustCmd, "remove", "-data", dir, key); err != nil {
		t.Fatal(err)
	}
	if _, err := runAdmin(t, trustCmd, "remove", "-data", dir, key); err == nil {
		t.Fatal("removing an untrusted key succeeded")
	}
}

func operatorCmd(ctx context.Context, a []string, b *bytes.Buffer) error {
	return operatorCommand(ctx, a, b)
}
func setCmd(ctx context.Context, a []string, b *bytes.Buffer) error { return setCommand(ctx, a, b) }

// An operator key signs a set naming two nodes; publishing it on one node
// makes it serve the set.
func TestOperatorAndSetCommands(t *testing.T) {
	work := t.TempDir()
	opPath := filepath.Join(work, "op.key")
	out, err := runAdmin(t, operatorCmd, "keygen", "-out", opPath)
	if err != nil || !strings.Contains(out, "public key: ") {
		t.Fatalf("keygen: %v %s", err, out)
	}
	opPub := strings.TrimSpace(out[strings.Index(out, "public key: ")+len("public key: "):])
	if _, err := runAdmin(t, operatorCmd, "keygen", "-out", opPath); err == nil {
		t.Fatal("keygen overwrote an operator key")
	}
	dir := t.TempDir()
	srv := openMeta(t, dir)
	m2, _ := identity.Generate()
	setPath := filepath.Join(work, "set.bin")
	out, err = runAdmin(t, operatorCmd, "sign-set", "-key", opPath, "-seq", "1", "-out", setPath,
		"-node", "https://m1.example.invalid="+identity.Encode(srv.NodeKey())+":root",
		"-node", "https://m2.example.invalid="+identity.Encode(m2.Public)+":nodirectory")
	if err != nil || !strings.Contains(out, "2 nodes") || !strings.Contains(out, opPub) {
		t.Fatalf("sign-set: %v %s", err, out)
	}
	for _, bad := range [][]string{
		{"sign-set", "-key", opPath, "-out", setPath, "-node", "https://x.invalid=" + opPub},      // no -seq
		{"sign-set", "-key", opPath, "-seq", "2", "-out", setPath, "-node", "https://x.invalid"},  // no key
		{"sign-set", "-key", opPath, "-seq", "2", "-out", setPath, "-node", "x.invalid=" + opPub}, // no scheme
		{"sign-set", "-key", opPath, "-seq", "2", "-out", setPath, "-node", "https://x.invalid=" + opPub + ":boss"},
	} {
		if _, err := runAdmin(t, operatorCmd, bad...); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	cfgPath := filepath.Join(work, "meta.json")
	if err := os.WriteFile(cfgPath, []byte(`{"data_dir": "`+dir+`", "cluster_operator_key": "`+opPub+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = runAdmin(t, setCmd, "publish", "-data", dir, "-config", cfgPath, setPath)
	if err != nil || !strings.Contains(out, "maintainer set #1 (2 nodes") {
		t.Fatalf("set publish: %v %s", err, out)
	}
	if _, err := runAdmin(t, setCmd, "publish", "-data", dir, setPath); err == nil {
		t.Fatal("the same seq was published twice")
	}
	other := filepath.Join(work, "other.key")
	_, _ = runAdmin(t, operatorCmd, "keygen", "-out", other)
	_, _ = runAdmin(t, operatorCmd, "sign-set", "-key", other, "-seq", "5", "-out", setPath, "-node", "https://m1.example.invalid="+identity.Encode(srv.NodeKey()))
	if _, err := runAdmin(t, setCmd, "publish", "-data", dir, "-config", cfgPath, setPath); err == nil {
		t.Fatal("a set by another operator key was published")
	}
}
