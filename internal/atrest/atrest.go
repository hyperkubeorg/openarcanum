// Package atrest seals values stored in a node's database (docs/operations.md
// "Data at rest"). Every sealed value is
//
//	nonce (24 random bytes) || XChaCha20-Poly1305(key, nonce, plaintext, aad)
//
// with aad = context 0x00 table 0x00 column 0x00 row. The context names the
// node and format version ("openarcanum/at-rest/v1" for the community server);
// table, column and row bind each ciphertext to where it is stored, so it
// can't be moved to another row or column without failing authentication.
//
// The key is 32 random bytes kept hex-encoded in a key file (mode 0600)
// outside the database, so a copy of the database alone opens nothing.
package atrest

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
)

// KeySize is the length of a storage key.
const KeySize = chacha20poly1305.KeySize

// ErrAuth means a stored value failed authentication: the database was
// modified or damaged, or the key is wrong.
var ErrAuth = errors.New("atrest: stored value failed authentication")

// AAD is the associated data a value at (table, column, row) is sealed with.
func AAD(context, table, column, row string) []byte {
	return []byte(context + "\x00" + table + "\x00" + column + "\x00" + row)
}

// Seal encrypts pt for (table, column, row) under key.
func Seal(key []byte, context, table, column, row string, pt []byte) []byte {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		panic(err)
	}
	nonce := make([]byte, aead.NonceSize(), aead.NonceSize()+len(pt)+aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	return aead.Seal(nonce, nonce, pt, AAD(context, table, column, row))
}

// Open decrypts a value Seal made for (table, column, row). Any failure is
// ErrAuth.
func Open(key []byte, context, table, column, row string, ct []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	if len(ct) < aead.NonceSize()+aead.Overhead() {
		return nil, ErrAuth
	}
	pt, err := aead.Open(nil, ct[:aead.NonceSize()], ct[aead.NonceSize():], AAD(context, table, column, row))
	if err != nil {
		return nil, ErrAuth
	}
	return pt, nil
}

// ReadKeyFile reads a hex-encoded storage key.
func ReadKeyFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	v, err := hex.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(v) != KeySize {
		return nil, fmt.Errorf("%s: expected %d hex-encoded bytes", path, KeySize)
	}
	return v, nil
}

// LoadOrCreateKeyFile reads the key at path, first writing a new random one
// (mode 0600, its directory 0700) if the file doesn't exist.
func LoadOrCreateKeyFile(path string) ([]byte, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		k := make([]byte, KeySize)
		if _, err := rand.Read(k); err != nil {
			return nil, err
		}
		if err := identity.WriteFileAtomic(path, []byte(hex.EncodeToString(k)+"\n"), 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	return ReadKeyFile(path)
}
