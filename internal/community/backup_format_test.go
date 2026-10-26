package community_test

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"io"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/hyperkubeorg/openarcanum/internal/community"
)

func TestBackupFormat(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)
	plain := bytes.Repeat([]byte("snapshot "), 300000)
	sealed := community.EncryptBackup(key, plain)
	got, err := community.DecryptBackup(key, sealed)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("round trip: %v", err)
	}
	other := make([]byte, 32)
	rand.Read(other)
	if _, err := community.DecryptBackup(other, sealed); err == nil {
		t.Fatal("wrong storage key accepted")
	}
	bad := bytes.Clone(sealed)
	bad[len(bad)-20] ^= 1
	if _, err := community.DecryptBackup(key, bad); err == nil {
		t.Fatal("tampered backup accepted")
	}
	for _, cut := range []int{len(sealed) - 1, len(sealed) - 17, 64 << 10, 200, 9, 3} {
		if _, err := community.DecryptBackup(key, sealed[:cut]); err == nil {
			t.Fatalf("backup cut to %d bytes accepted", cut)
		}
	}
	for _, old := range []string{"FRAGBK1\x00anything", "FRAGBK2\x00anything"} {
		if _, err := community.DecryptBackup(key, []byte(old)); err == nil || !strings.Contains(err.Error(), "pre-release") {
			t.Fatalf("old format: %v", err)
		}
	}
	// The published age format: the stock age tool reads a backup given
	// the storage key in hex as the passphrase.
	if !bytes.HasPrefix(sealed, []byte("age-encryption.org/v1\n-> scrypt ")) {
		t.Fatalf("not an age scrypt file: %q", sealed[:40])
	}
	id, err := age.NewScryptIdentity(hex.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	r, err := age.Decrypt(bytes.NewReader(sealed), id)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := io.ReadAll(r); err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("age library read: %v", err)
	}
	if _, err := community.DecryptBackup(key, []byte("something else entirely")); err == nil {
		t.Fatal("non-backup accepted")
	}
}
