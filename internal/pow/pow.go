// Package pow is the relay's proof-of-work for new connections under load
// (docs/protocol.md §2 "Starting"). It is Hashcash (A. Back, "Hashcash - A
// Denial of Service Counter-Measure", 2002, http://www.hashcash.org/papers/hashcash.pdf)
// with SHA-256 in place of SHA-1: the client searches for a counter such that
//
//	SHA-256("openarcanum/relay-pow/v1" || 0x00 || challenge || counter)
//
// starts with `bits` zero bits; checking takes one hash. The challenge is the
// relay's fresh random per-connection Hello nonce, so a solution only works
// on the connection it was made for (no replay), and the relay only accepts
// it within the Start window after Hello (expiry).
package pow

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math/bits"
)

// Context separates these hashes from every other use of SHA-256 here.
const Context = "openarcanum/relay-pow/v1"

// MaxBits bounds the difficulty a relay may ask for (about 2^24 hashes
// expected: a couple of seconds on a slow device) and a client will try.
const MaxBits = 24

// MaxCounter is the longest counter accepted.
const MaxCounter = 16

// Digest is the hash a counter is judged by.
func Digest(challenge, counter []byte) [32]byte {
	return DigestIn(Context, challenge, counter)
}

// DigestIn is Digest under another context string, for other uses of the
// same Hashcash scheme (the website's report forms use their own, so a
// solution for one can never count for the other).
func DigestIn(label string, challenge, counter []byte) [32]byte {
	h := sha256.New()
	h.Write([]byte(label))
	h.Write([]byte{0})
	h.Write(challenge)
	h.Write(counter)
	var out [32]byte
	h.Sum(out[:0])
	return out
}

// LeadingZeroBits counts the zero bits at the start of h.
func LeadingZeroBits(h []byte) int {
	n := 0
	for _, b := range h {
		if b != 0 {
			return n + bits.LeadingZeros8(b)
		}
		n += 8
	}
	return n
}

// Verify checks a counter against a challenge. bits 0 accepts anything.
func Verify(challenge, counter []byte, need uint32) bool {
	return VerifyIn(Context, challenge, counter, need)
}

// VerifyIn is Verify under another context string (DigestIn).
func VerifyIn(label string, challenge, counter []byte, need uint32) bool {
	if need == 0 {
		return true
	}
	if need > MaxBits || len(counter) == 0 || len(counter) > MaxCounter {
		return false
	}
	d := DigestIn(label, challenge, counter)
	return LeadingZeroBits(d[:]) >= int(need)
}

// ErrTooHard is returned for a difficulty above MaxBits.
var ErrTooHard = errors.New("pow: difficulty above the maximum")

// Solve finds a counter (8 bytes, big-endian) for the challenge. It checks
// ctx every 4096 attempts.
func Solve(ctx context.Context, challenge []byte, need uint32) ([]byte, error) {
	return SolveIn(ctx, Context, challenge, need)
}

// SolveIn is Solve under another context string (DigestIn).
func SolveIn(ctx context.Context, label string, challenge []byte, need uint32) ([]byte, error) {
	if need > MaxBits {
		return nil, ErrTooHard
	}
	counter := make([]byte, 8)
	if need == 0 {
		return counter, nil
	}
	buf := append(append([]byte(label+"\x00"), challenge...), counter...)
	tail := buf[len(buf)-8:]
	for i := uint64(0); ; i++ {
		if i&4095 == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		binary.BigEndian.PutUint64(tail, i)
		h := sha256.Sum256(buf)
		if LeadingZeroBits(h[:]) >= int(need) {
			return append([]byte(nil), tail...), nil
		}
	}
}
