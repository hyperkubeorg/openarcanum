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

	"github.com/hyperkubeorg/openarcanum/internal/meta"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// `meta backup` on a running node and `meta restore` into a new
// directory, as an operator (or the systemd timer) runs them.
func TestBackupAndRestoreCommands(t *testing.T) {
	dir := t.TempDir()
	cfg := meta.DefaultConfig()
	cfg.DirectoryEnabled = true // the tests exercise the directory; it's off by default
	cfg.DataDir = dir
	srv, err := meta.Open(context.Background(), cfg, testutil.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	token := bytes.Repeat([]byte{7}, 32)
	post(t, hs.URL+"/v1/mailbox/drop", &wire.MailboxDrop{Inbox: meta.InboxAddress(token), Blob: []byte("kept across the move")})
	id := uploadAt(t, srv, hs.URL, time.Now(), bytes.Repeat([]byte("blob"), 1000))

	backup := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := backupCommand(context.Background(), args, &out)
		return out.String(), err
	}
	out1, err := backup("-data", dir, "-keep", "1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out1, "blobs: 1") || !strings.Contains(out1, filepath.Join(dir, "backups", meta.BackupPrefix)) {
		t.Fatalf("backup output: %q", out1)
	}
	time.Sleep(5 * time.Millisecond)
	out2, err := backup("-data", dir, "-keep", "1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out2, "1 old backup(s) deleted") {
		t.Fatalf("second backup output: %q", out2)
	}
	hs.Close()
	srv.Close()
	path := strings.SplitN(out2, "\n", 2)[0]

	newDir := filepath.Join(t.TempDir(), "restored")
	var out bytes.Buffer
	if err := restoreCommand(context.Background(), []string{"-data", newDir, "-backup", path}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "1 blobs") {
		t.Fatalf("restore output: %q", out.String())
	}
	cfg.DataDir = newDir
	srv2, err := meta.Open(context.Background(), cfg, testutil.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	defer srv2.Close()
	adm, err := meta.OpenBlobAdmin(context.Background(), newDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer adm.Close()
	st, err := adm.Stats(context.Background())
	if err != nil || st.Stored != 1 || st.Blobs[0].ID != id {
		t.Fatalf("restored blobs: %+v %v", st, err)
	}

	// Usage errors and refusals.
	for _, args := range [][]string{{}, {"-data"}, {"-data", dir, "-keep", "0"}, {"-data", dir, "extra"}, {"-bogus"}} {
		if _, err := backup(args...); err == nil {
			t.Errorf("backup %v: no error", args)
		}
	}
	for _, args := range [][]string{{}, {"-data", newDir}, {"-backup", path}} {
		if err := restoreCommand(context.Background(), args, &out); err == nil {
			t.Errorf("restore %v: no error", args)
		}
	}
	if err := restoreCommand(context.Background(), []string{"-data", newDir, "-backup", path}, &out); err == nil || !strings.Contains(err.Error(), "already has a database") {
		t.Fatalf("restore over a node: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("restore consumed the backup")
	}
}
