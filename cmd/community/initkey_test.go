package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// In the container image OPENARCANUM_COMMUNITY_STORAGE_KEY_FILE points at the
// separate /keys volume: init writes the storage key there, not into the
// data directory (which holds the backups), and the server opens with it.
func TestInitHonorsStorageKeyEnv(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	key := filepath.Join(t.TempDir(), "keys", "storage.key")
	if err := os.MkdirAll(filepath.Dir(key), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENARCANUM_COMMUNITY_STORAGE_KEY_FILE", key)
	var out bytes.Buffer
	if err := initCommand([]string{"-data", dir, "-relay", "wss://relay.example.invalid/ws"}, strings.NewReader(""), &out, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(key); err != nil {
		t.Fatalf("storage key not written to the key volume: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "storage.key")); !os.IsNotExist(err) {
		t.Fatalf("storage key written into the data directory: %v", err)
	}
	opens(t, dir)
}
