package site

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/pow"
)

// ---------------------------------------------------------------- rate limits

// limiter counts events per key in a sliding window, in memory (a restart
// forgets; the per-account sign-in lockout is in the database).
type limiter struct {
	n      int
	window time.Duration
	now    func() time.Time

	mu        sync.Mutex
	hits      map[string][]time.Time
	lastPrune time.Time
}

// limiterMaxKeys bounds the memory a flood from many addresses can take;
// when full, keys whose events have all expired are dropped, and if none
// have, new keys are refused (treated as over the limit).
const limiterMaxKeys = 100_000

func newLimiter(n int, window time.Duration) *limiter {
	return &limiter{n: n, window: window, now: time.Now, hits: map[string][]time.Time{}}
}

func (l *limiter) live(key string, now time.Time) []time.Time {
	h := l.hits[key]
	cut := 0
	for cut < len(h) && now.Sub(h[cut]) >= l.window {
		cut++
	}
	if cut > 0 {
		h = append(h[:0], h[cut:]...)
		if len(h) == 0 {
			delete(l.hits, key)
			return nil
		}
		l.hits[key] = h
	}
	return h
}

func (l *limiter) prune(now time.Time) {
	if now.Sub(l.lastPrune) < l.window/4 && len(l.hits) < limiterMaxKeys {
		return
	}
	l.lastPrune = now
	for k := range l.hits {
		l.live(k, now)
	}
}

// full reports whether key is at its limit.
func (l *limiter) full(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.prune(now)
	h := l.live(key, now)
	if h == nil && len(l.hits) >= limiterMaxKeys {
		return true
	}
	return len(h) >= l.n
}

// take records an event if key is under its limit, reporting whether it was.
func (l *limiter) take(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.prune(now)
	h := l.live(key, now)
	if len(h) >= l.n || (h == nil && len(l.hits) >= limiterMaxKeys) {
		return false
	}
	l.hits[key] = append(h, now)
	return true
}

// ---------------------------------------------------------------- proof of work

// powContext is the report forms' Hashcash context (internal/pow): the
// browser searches for an 8-byte counter such that
// SHA-256(powContext || 0x00 || challenge || counter) starts with `bits`
// zero bits. static/pow.js implements the same search.
const powContext = "openarcanum/site-pow/v1"

// powTTL is how long a form's challenge stays valid.
const powTTL = time.Hour

// powGate issues challenges for the report forms and accepts each solved
// one once. A challenge is
//
//	base64url(issued (8 bytes, Unix seconds) || nonce (16) || HMAC-SHA256(key, issued || nonce)[:16])
//
// so the server keeps nothing per form shown, only the challenges already
// used (until they expire).
type powGate struct {
	key  []byte
	bits uint32
	now  func() time.Time

	mu   sync.Mutex
	used map[string]time.Time
}

const powUsedMax = 200_000

var errPoW = errors.New("the anti-spam check didn't pass; reload the page and try again")

func newPoWGate(key []byte, bits int) *powGate {
	return &powGate{key: key, bits: uint32(bits), now: time.Now, used: map[string]time.Time{}}
}

func (g *powGate) mac(body []byte) []byte {
	m := hmac.New(sha256.New, g.key)
	m.Write([]byte("openarcanum/site-pow-challenge/v1\x00"))
	m.Write(body)
	return m.Sum(nil)[:16]
}

// challenge is a fresh challenge.
func (g *powGate) challenge() string {
	body := make([]byte, 8, 40)
	binary.BigEndian.PutUint64(body, uint64(g.now().Unix()))
	body = append(body, randomBytes(16)...)
	return base64.RawURLEncoding.EncodeToString(append(body, g.mac(body)...))
}

// verify checks a solved challenge: ours, fresh, solved, not used up. It
// consumes nothing, so a form that then fails validation can be sent again
// with the same solution; consume marks it used once the report is taken.
func (g *powGate) verify(challenge, counterHex string) (issued time.Time, err error) {
	raw, err := base64.RawURLEncoding.DecodeString(challenge)
	if err != nil || len(raw) != 40 || !hmac.Equal(raw[24:], g.mac(raw[:24])) {
		return time.Time{}, errPoW
	}
	issued = time.Unix(int64(binary.BigEndian.Uint64(raw[:8])), 0)
	now := g.now()
	if now.Sub(issued) > powTTL || issued.Sub(now) > time.Minute {
		return time.Time{}, errPoW
	}
	counter, err := hex.DecodeString(counterHex)
	if err != nil || !pow.VerifyIn(powContext, []byte(challenge), counter, g.bits) {
		return time.Time{}, errPoW
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, dup := g.used[challenge]; dup {
		return time.Time{}, errPoW
	}
	return issued, nil
}

// consume uses up a verified challenge. Expired entries go whenever one
// is added, so a flood of solved challenges can't keep the table full.
func (g *powGate) consume(challenge string, issued time.Time) error {
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, dup := g.used[challenge]; dup {
		return errPoW
	}
	for k, exp := range g.used {
		if now.After(exp) {
			delete(g.used, k)
		}
	}
	if len(g.used) >= powUsedMax {
		return errPoW
	}
	g.used[challenge] = issued.Add(powTTL)
	return nil
}

// check accepts a solved challenge once (verify and consume together).
func (g *powGate) check(challenge, counterHex string) error {
	issued, err := g.verify(challenge, counterHex)
	if err != nil {
		return err
	}
	return g.consume(challenge, issued)
}
