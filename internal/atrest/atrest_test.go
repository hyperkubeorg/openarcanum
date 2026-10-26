package atrest

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// posixModes: Windows has no POSIX mode bits (files there report 0666, or
// 0444 when read-only; privacy comes from the profile's ACLs), so mode
// assertions only hold elsewhere.
const posixModes = runtime.GOOS != "windows"

func TestSealOpenBindsPlace(t *testing.T) {
	key := bytes.Repeat([]byte{7}, KeySize)
	ct := Seal(key, "ctx/v1", "reports", "content", "1", []byte("secret report"))
	if bytes.Contains(ct, []byte("secret")) {
		t.Fatal("plaintext visible")
	}
	pt, err := Open(key, "ctx/v1", "reports", "content", "1", ct)
	if err != nil || string(pt) != "secret report" {
		t.Fatalf("%q %v", pt, err)
	}
	for _, place := range [][4]string{
		{"ctx/v2", "reports", "content", "1"},
		{"ctx/v1", "notes", "content", "1"},
		{"ctx/v1", "reports", "title", "1"},
		{"ctx/v1", "reports", "content", "2"},
	} {
		if _, err := Open(key, place[0], place[1], place[2], place[3], ct); !errors.Is(err, ErrAuth) {
			t.Errorf("%v: %v", place, err)
		}
	}
	if _, err := Open(bytes.Repeat([]byte{8}, KeySize), "ctx/v1", "reports", "content", "1", ct); !errors.Is(err, ErrAuth) {
		t.Error("wrong key opened it")
	}
	if _, err := Open(key, "ctx/v1", "reports", "content", "1", ct[:10]); !errors.Is(err, ErrAuth) {
		t.Error("short value")
	}
}

func TestLoadOrCreateKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys", "storage.key")
	k1, err := LoadOrCreateKeyFile(path)
	if err != nil || len(k1) != KeySize {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	if posixModes && st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", st.Mode().Perm())
	}
	k2, err := LoadOrCreateKeyFile(path)
	if err != nil || !bytes.Equal(k1, k2) {
		t.Fatal("key changed on second load")
	}
	if err := os.WriteFile(path, []byte("zz\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadKeyFile(path); err == nil {
		t.Fatal("bad key file accepted")
	}
}
