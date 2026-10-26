package site

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// ---------------------------------------------------------------- passphrases

// Argon2id parameters: RFC 9106 §4's second recommended option (64 MiB, 3
// passes, 4 lanes), a 32-byte hash and a 16-byte salt. Stored with each
// hash in the PHC string format, so they can be raised later without
// invalidating existing hashes. Tests lower them (argonParams is a var
// for that alone).
var argonParams = struct {
	time, memory uint32
	threads      uint8
}{3, 64 * 1024, 4}

const (
	argonKeyLen  = 32
	argonSaltLen = 16
)

// MinPassphrase is the shortest admin passphrase accepted (characters).
const MinPassphrase = 12

// maxPassphrase bounds what is hashed (a huge input costs only hashing time
// with Argon2, but there is no reason to accept it).
const maxPassphrase = 1024

// hashSlots bounds concurrent Argon2 computations (64 MiB each), so a burst
// of sign-in attempts can't exhaust memory.
var hashSlots = make(chan struct{}, 2)

// CheckPassphraseStrength rejects passphrases too short to be one.
func CheckPassphraseStrength(p string) error {
	if utf8.RuneCountInString(p) < MinPassphrase {
		return fmt.Errorf("the passphrase must be at least %d characters", MinPassphrase)
	}
	if len(p) > maxPassphrase {
		return fmt.Errorf("the passphrase must be at most %d bytes", maxPassphrase)
	}
	return nil
}

// HashPassphrase hashes p with Argon2id and a random salt.
func HashPassphrase(p string) string {
	salt := randomBytes(argonSaltLen)
	hashSlots <- struct{}{}
	ap := argonParams
	key := argon2.IDKey([]byte(p), salt, ap.time, ap.memory, ap.threads, argonKeyLen)
	<-hashSlots
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, ap.memory, ap.time, ap.threads,
		b64.EncodeToString(salt), b64.EncodeToString(key))
}

// verifyPassphrase checks p against a HashPassphrase string in constant
// time (for the hash comparison).
func verifyPassphrase(encoded, p string) bool {
	if len(p) > maxPassphrase {
		return false
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return false
	}
	var m, t uint32
	var par uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &par); err != nil || m > 1<<20 || t > 16 || par == 0 {
		return false
	}
	b64 := base64.RawStdEncoding
	salt, err1 := b64.DecodeString(parts[4])
	want, err2 := b64.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(want) == 0 || len(want) > 64 {
		return false
	}
	hashSlots <- struct{}{}
	got := argon2.IDKey([]byte(p), salt, t, m, par, uint32(len(want)))
	<-hashSlots
	return subtle.ConstantTimeCompare(got, want) == 1
}

// dummyHash is checked when the admin name doesn't exist, so a wrong name
// costs the same time as a wrong passphrase.
var dummyHash = sync.OnceValue(func() string { return HashPassphrase("not a real passphrase, never accepted") })

// ---------------------------------------------------------------- TOTP (RFC 6238)

// TOTP is RFC 6238 with its defaults (HMAC-SHA1, 30-second steps, 6 digits:
// what authenticator apps support), one step of clock skew either way,
// and each step usable once.
const (
	totpPeriod = 30
	totpDigits = 6
	totpSkew   = 1
)

// TOTPSecretSize is 160 bits (RFC 4226 §4 recommends at least 128).
const TOTPSecretSize = 20

var totpEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret is a fresh random secret.
func NewTOTPSecret() []byte { return randomBytes(TOTPSecretSize) }

// TOTPKeyText is the secret as authenticator apps take it (base32).
func TOTPKeyText(secret []byte) string { return totpEncoding.EncodeToString(secret) }

// TOTPURI is the otpauth:// URI for enrolling secret in an authenticator.
func TOTPURI(issuer, account string, secret []byte) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{"secret": {TOTPKeyText(secret)}, "issuer": {issuer}, "algorithm": {"SHA1"},
		"digits": {strconv.Itoa(totpDigits)}, "period": {strconv.Itoa(totpPeriod)}}
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// hotp is RFC 4226's HOTP value for counter.
func hotp(secret []byte, counter uint64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	m := hmac.New(sha1.New, secret)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	code := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, code%1_000_000)
}

// TOTPCode is the code for time t (for tests and the enrolment check).
func TOTPCode(secret []byte, t time.Time) string {
	return hotp(secret, uint64(t.Unix()/totpPeriod))
}

// totpMatch returns the time step code matches at t (within the skew), or
// -1. All candidates are compared, in constant time each.
func totpMatch(secret []byte, code string, t time.Time) int64 {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != totpDigits {
		return -1
	}
	now := t.Unix() / totpPeriod
	found := int64(-1)
	for d := int64(-totpSkew); d <= totpSkew; d++ {
		step := now + d
		if step < 0 {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(hotp(secret, uint64(step))), []byte(code)) == 1 {
			found = step
		}
	}
	return found
}

// VerifyTOTPOnce checks a code at enrolment (no replay record yet).
func VerifyTOTPOnce(secret []byte, code string, t time.Time) bool {
	return totpMatch(secret, code, t) >= 0
}

// ---------------------------------------------------------------- sign-in

// Lockout: after lockAfter failed sign-ins in a row an account is locked
// for 1 minute, doubling with each further failure up to lockMax. A
// successful sign-in (or `site admin unlock`) resets it.
const (
	lockAfter = 5
	lockMax   = time.Hour
)

func lockFor(failures int) time.Duration {
	if failures < lockAfter {
		return 0
	}
	d := time.Minute << min(failures-lockAfter, 10)
	return min(d, lockMax)
}

// errSignIn is the one answer to every failed sign-in: it doesn't say
// whether the name exists, the passphrase or the code was wrong, or the
// account is locked.
var errSignIn = errors.New("sign-in failed")

// authenticate checks a sign-in. It always costs one Argon2 computation.
func (s *Store) authenticate(ctx context.Context, name, pass, code string) (*Admin, error) {
	a, err := s.AdminByName(ctx, name)
	if errors.Is(err, ErrNotFound) || ValidAdminName(name) != nil {
		verifyPassphrase(dummyHash(), pass)
		return nil, errSignIn
	}
	if err != nil {
		return nil, err
	}
	now := s.now()
	if now.Before(a.LockedUntil) {
		verifyPassphrase(dummyHash(), pass)
		return nil, errSignIn
	}
	ok := verifyPassphrase(a.PassHash, pass)
	if ok {
		secret, _, err := s.totpSecret(ctx, a.ID)
		if err != nil {
			return nil, err
		}
		if secret != nil {
			step := totpMatch(secret, code, now)
			ok = step >= 0
			if ok {
				// A code already used (seen by someone over the
				// admin's shoulder) doesn't work twice.
				if ok, err = s.useTOTPStep(ctx, a.ID, step); err != nil {
					return nil, err
				}
			}
		}
	}
	if !ok {
		if err := s.recordFailure(ctx, a.ID, lockFor); err != nil {
			return nil, err
		}
		return nil, errSignIn
	}
	if err := s.recordLogin(ctx, a.ID); err != nil {
		return nil, err
	}
	return a, nil
}

// tokenHash is how session tokens are stored: a copy of the database
// doesn't hold usable sessions.
func tokenHash(token string) []byte {
	h := sha256.Sum256([]byte("openarcanum/site-session/v1\x00" + token))
	return h[:]
}

// newToken is 256 random bits, base64url.
func newToken() string { return base64.RawURLEncoding.EncodeToString(randomBytes(32)) }
