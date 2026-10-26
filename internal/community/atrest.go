package community

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"golang.org/x/crypto/chacha20poly1305"

	"github.com/hyperkubeorg/openarcanum/internal/atrest"
)

// Encryption at rest (docs/operations.md "Data at rest").
//
// Every sealed value is
//
//	nonce (24 random bytes) || XChaCha20-Poly1305(key, nonce, plaintext, aad)
//
// with aad = "openarcanum/at-rest/v1" 0x00 table 0x00 column 0x00 row, where row
// is the row's integer id in decimal, or for rows keyed by a lookup hash
// (members, devices, bans, contact cards, key packages) that hash in hex. The
// AAD binds each ciphertext to its table, column and row, so ciphertexts
// can't be moved between rows or columns without failing authentication.
//
// Keys:
//   - The storage key (storage_key_file) seals the member roll, roles,
//     channels (names, keys, topics, overrides), join keys, bans, devices,
//     avatars, the audit log, contact cards, key packages, every meta value
//     (settings, certificate, owner keys), the lookup key and the month keys.
//   - Month keys are random 32-byte keys, one per UTC calendar month, stored
//     sealed in month_keys. They seal message bodies and file metadata
//     (including each file's own chunk key), chosen by the row's created_at.
//     Deleting a month key makes that month's messages and files
//     unrecoverable; the retention job does so once the whole month is past
//     the longest retention in effect (jobs.go).
//   - The lookup key is a random 32-byte key stored sealed in meta. Public
//     keys that rows are looked up by (member and device keys) are stored as
//     HMAC-SHA256(lookup key, domain 0x00 public key), never in plaintext.

const atRestContext = "openarcanum/at-rest/v1"

// keyCheckPlaintext is sealed under the storage key at meta "key_check"; it
// tells "wrong storage key" apart from a damaged row at startup.
const keyCheckPlaintext = "openarcanum storage key check v1"

// errWrongStorageKey is returned by Open and Restore when the storage key
// can't open the database.
var errWrongStorageKey = errors.New("the storage key does not match this database (check storage_key_file)")

// errMonthKeyDeleted means the data belongs to a month whose key the
// retention job has already destroyed.
var errMonthKeyDeleted = errors.New("the month key for this data was deleted by retention")

// TamperError reports a stored value that failed authentication.
type TamperError struct {
	Table, Column, Row string
}

func (e *TamperError) Error() string {
	return fmt.Sprintf("stored %s.%s for row %s failed to decrypt: the database was modified or damaged", e.Table, e.Column, e.Row)
}

func rowID(id int64) string    { return strconv.FormatInt(id, 10) }
func rowKey(kid []byte) string { return hex.EncodeToString(kid) }

// sealWith and openWith are the shared construction (internal/atrest, also
// used by the website) under this format's context.
func sealWith(key []byte, table, column, row string, pt []byte) []byte {
	return atrest.Seal(key, atRestContext, table, column, row, pt)
}

func openWith(key []byte, table, column, row string, ct []byte) ([]byte, error) {
	pt, err := atrest.Open(key, atRestContext, table, column, row, ct)
	if errors.Is(err, atrest.ErrAuth) {
		return nil, &TamperError{table, column, row}
	}
	return pt, err
}

// seal and open use the storage key.
func (s *Server) seal(table, column, row string, pt []byte) []byte {
	return sealWith(s.storageKey, table, column, row, pt)
}

func (s *Server) open(table, column, row string, ct []byte) ([]byte, error) {
	return openWith(s.storageKey, table, column, row, ct)
}

func (s *Server) sealJSON(table, column, row string, v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return s.seal(table, column, row, b)
}

func (s *Server) openJSON(table, column, row string, ct []byte, v any) error {
	pt, err := s.open(table, column, row, ct)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(pt, v); err != nil {
		return &TamperError{table, column, row}
	}
	return nil
}

// Lookup-hash domains.
const (
	lookupMember = "member"
	lookupDevice = "device"
	// A DM device (Olm curve25519 key) that one-time keys are stored under.
	lookupDMDevice = "dm-device"
)

// lookupID is the keyed hash a public key is stored and looked up under.
func lookupID(lookupKey []byte, domain string, pub []byte) []byte {
	m := hmac.New(sha256.New, lookupKey)
	m.Write([]byte(domain))
	m.Write([]byte{0})
	m.Write(pub)
	return m.Sum(nil)
}

func (s *Server) kid(pub []byte) []byte      { return lookupID(s.lookupKey, lookupMember, pub) }
func (s *Server) did(pub []byte) []byte      { return lookupID(s.lookupKey, lookupDevice, pub) }
func (s *Server) dmDevice(pub []byte) []byte { return lookupID(s.lookupKey, lookupDMDevice, pub) }

// ---------------------------------------------------------------- month keys

// monthOf returns the UTC calendar month of a millisecond timestamp as
// YYYYMM.
func monthOf(ms int64) int {
	t := time.UnixMilli(ms).UTC()
	return t.Year()*100 + int(t.Month())
}

// monthEnd returns the first millisecond after month m (YYYYMM).
func monthEnd(m int) int64 {
	return time.Date(m/100, time.Month(m%100)+1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
}

func monthRow(m int) string { return strconv.Itoa(m) }

// monthKey returns the key for month m, creating it if create is set. It
// runs its own statements on s.db, so callers create keys before opening a
// transaction that uses them (a rolled-back transaction must never leave a
// cached key that isn't on disk).
func (s *Server) monthKey(ctx context.Context, m int, create bool) ([]byte, error) {
	s.kmu.Lock()
	defer s.kmu.Unlock()
	if k := s.monthKeys[m]; k != nil {
		return k, nil
	}
	if create {
		k := randomBytes(chacha20poly1305.KeySize)
		if _, err := s.db.ExecContext(ctx, `INSERT INTO month_keys (month, key_enc) VALUES (?, ?) ON CONFLICT (month) DO NOTHING`,
			m, s.seal("month_keys", "key_enc", monthRow(m), k)); err != nil {
			return nil, err
		}
	}
	var enc []byte
	err := s.db.QueryRowContext(ctx, `SELECT key_enc FROM month_keys WHERE month = ?`, m).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errMonthKeyDeleted
	}
	if err != nil {
		return nil, err
	}
	k, err := s.open("month_keys", "key_enc", monthRow(m), enc)
	if err != nil {
		return nil, err
	}
	if s.monthKeys == nil {
		s.monthKeys = map[int][]byte{}
	}
	s.monthKeys[m] = k
	return k, nil
}

func (s *Server) openMonth(ctx context.Context, m int, table, column, row string, ct []byte) ([]byte, error) {
	k, err := s.monthKey(ctx, m, false)
	if err != nil {
		return nil, err
	}
	return openWith(k, table, column, row, ct)
}

// forgetMonthKey drops a deleted key from the cache.
func (s *Server) forgetMonthKey(m int) {
	s.kmu.Lock()
	delete(s.monthKeys, m)
	s.kmu.Unlock()
}

// ---------------------------------------------------------------- meta

// getMeta reads and decrypts a meta value; missing keys return nil.
func (s *Server) getMeta(ctx context.Context, k string) ([]byte, error) {
	var v []byte
	err := s.db.QueryRowContext(ctx, `SELECT v FROM meta WHERE k = ?`, k).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.open("meta", "v", k, v)
}

func (s *Server) setMeta(ctx context.Context, x execer, k string, v []byte) error {
	_, err := x.ExecContext(ctx, `INSERT INTO meta (k, v) VALUES (?, ?) ON CONFLICT (k) DO UPDATE SET v = excluded.v`, k, s.seal("meta", "v", k, v))
	return err
}

// loadKeys checks the storage key against the database and loads the lookup
// key. A wrong storage key fails here with errWrongStorageKey.
func (s *Server) loadKeys(ctx context.Context) error {
	var check []byte
	if err := s.db.QueryRowContext(ctx, `SELECT v FROM meta WHERE k = 'key_check'`).Scan(&check); err != nil {
		return fmt.Errorf("database has no storage key check: %w", err)
	}
	pt, err := s.open("meta", "v", "key_check", check)
	if err != nil || string(pt) != keyCheckPlaintext {
		return errWrongStorageKey
	}
	lk, err := s.getMeta(ctx, "lookup_key")
	if err != nil {
		return err
	}
	if len(lk) != 32 {
		return errors.New("database has no lookup key")
	}
	s.lookupKey = lk
	return nil
}
