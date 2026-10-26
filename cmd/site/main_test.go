package main

import (
	"bytes"
	"context"
	"encoding/base32"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/site"
)

func admin(t *testing.T, input string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := adminCommand(context.Background(), args, strings.NewReader(input), &out)
	return out.String(), err
}

func TestAdminCommands(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	if _, err := admin(t, "short\n", "add", "alice", "-data", data); err == nil || !strings.Contains(err.Error(), "at least") {
		t.Fatalf("short passphrase: %v", err)
	}
	if _, err := admin(t, "a long enough passphrase\n", "add", "bad name!", "-data", data); err == nil {
		t.Fatal("bad name accepted")
	}
	out, err := admin(t, "a long enough passphrase\n", "add", "alice", "-data", data)
	if err != nil || !strings.Contains(out, "Admin alice added") {
		t.Fatal(out, err)
	}
	if _, err := admin(t, "another long passphrase\n", "add", "-data", data, "alice"); err == nil {
		t.Fatal("duplicate admin")
	}
	out, err = admin(t, "", "list", "-data", data)
	if err != nil || !strings.HasPrefix(out, "alice\ttwo-factor off\tlast sign-in never") {
		t.Fatal(out, err)
	}
	if out, err := admin(t, "a different passphrase\n", "passwd", "alice", "-data", data); err != nil || !strings.Contains(out, "changed") {
		t.Fatal(out, err)
	}
	if _, err := admin(t, "", "unlock", "alice", "-data", data); err != nil {
		t.Fatal(err)
	}
	if _, err := admin(t, "", "unlock", "nobody", "-data", data); err == nil {
		t.Fatal("unlock of a missing admin")
	}
	// The data directory and key are private.
	for p, mode := range map[string]os.FileMode{data: 0o700, filepath.Join(data, "storage.key"): 0o600, filepath.Join(data, site.DBFile): 0o600} {
		if st, err := os.Stat(p); err != nil || st.Mode().Perm() != mode {
			t.Errorf("%s: %v %v", p, st.Mode(), err)
		}
	}

	// TOTP: a wrong code changes nothing; the right one turns it on.
	if _, err := admin(t, "000000\n", "totp", "alice", "-data", data); err == nil {
		t.Fatal("wrong code accepted")
	}
	pr, pw := io.Pipe()
	out2 := &keyWatcher{key: make(chan string, 1)}
	go func() {
		key := <-out2.key
		secret, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(key)
		_, _ = io.WriteString(pw, site.TOTPCode(secret, time.Now())+"\n")
	}()
	if err := adminCommand(context.Background(), []string{"totp", "alice", "-data", data}, pr, out2); err != nil {
		t.Fatalf("totp: %v\n%s", err, out2.String())
	}
	if out, _ := admin(t, "", "list", "-data", data); !strings.Contains(out, "two-factor on") {
		t.Fatal(out)
	}
	if _, err := admin(t, "", "totp", "alice", "-off", "-data", data); err != nil {
		t.Fatal(err)
	}
	if out, _ := admin(t, "", "remove", "alice", "-data", data); !strings.Contains(out, "removed") {
		t.Fatal(out)
	}
	if out, _ := admin(t, "", "list", "-data", data); !strings.Contains(out, "No admins") {
		t.Fatal(out)
	}
	if _, err := admin(t, ""); err == nil {
		t.Fatal("no subcommand")
	}
	if _, err := admin(t, "", "list"); err == nil || !strings.Contains(err.Error(), "-data") {
		t.Fatalf("no data dir: %v", err)
	}
}

type keyWatcher struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	key  chan string
	sent bool
}

var keyLine = regexp.MustCompile(`(?m)^  ([A-Z2-7]{32})$`)

func (k *keyWatcher) Write(p []byte) (int, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.buf.Write(p)
	if m := keyLine.FindStringSubmatch(k.buf.String()); m != nil && !k.sent {
		k.sent = true
		k.key <- m[1]
	}
	return len(p), nil
}

func (k *keyWatcher) String() string { k.mu.Lock(); defer k.mu.Unlock(); return k.buf.String() }

func TestBackupCommand(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	var out bytes.Buffer
	if err := backupCommand(context.Background(), []string{"-data", data}, &out); err == nil {
		t.Fatal("backup of a site that never ran")
	}
	if _, err := admin(t, "a long enough passphrase\n", "add", "alice", "-data", data); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "b")
	if err := backupCommand(context.Background(), []string{"-data", data, "-out", dest}, &out); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dest)
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), site.BackupPrefix) || !strings.Contains(out.String(), "storage.key") {
		t.Fatal(entries, out.String())
	}
}
