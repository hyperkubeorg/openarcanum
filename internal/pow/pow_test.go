package pow

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"testing"
)

// Each context is its own puzzle: a solution under one rarely counts under
// another (the website's forms against the relay's connections).
func TestContextsAreSeparate(t *testing.T) {
	ch := []byte("challenge")
	c, err := SolveIn(context.Background(), "openarcanum/site-pow/v1", ch, 12)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyIn("openarcanum/site-pow/v1", ch, c, 12) {
		t.Fatal("own solution rejected")
	}
	ok := 0
	for i := range 50 {
		ci := append([]byte("challenge"), byte(i))
		c, _ := SolveIn(context.Background(), "openarcanum/site-pow/v1", ci, 12)
		if Verify(ci, c, 12) {
			ok++
		}
	}
	if ok > 5 {
		t.Fatalf("%d of 50 site solutions also passed the relay check", ok)
	}
}

// Vectors pin the construction; clients/desktop/tests/test_pow.py checks the same
// values against the Python implementation.
var (
	vectorChallenge = func() []byte {
		b := make([]byte, 32)
		for i := range b {
			b[i] = byte(i)
		}
		return b
	}()
	vectorDigest42 = "bc3b5acf788a7e5f93eb19cd9fc853a43500432f875027b4fef417087335645a"
	// The first counter giving 12 zero bits for vectorChallenge.
	vectorFirst12 = uint64(5386)
)

func TestVectors(t *testing.T) {
	ctr := make([]byte, 8)
	binary.BigEndian.PutUint64(ctr, 42)
	d := Digest(vectorChallenge, ctr)
	if hex.EncodeToString(d[:]) != vectorDigest42 {
		t.Fatalf("digest %x", d)
	}
	got, err := Solve(context.Background(), vectorChallenge, 12)
	if err != nil {
		t.Fatal(err)
	}
	if binary.BigEndian.Uint64(got) != vectorFirst12 {
		t.Fatalf("first 12-bit counter %d, want %d", binary.BigEndian.Uint64(got), vectorFirst12)
	}
}

func TestLeadingZeroBits(t *testing.T) {
	for _, c := range []struct {
		in   []byte
		want int
	}{{[]byte{0x80}, 0}, {[]byte{0x01}, 7}, {[]byte{0, 0x40}, 9}, {[]byte{0, 0}, 16}} {
		if got := LeadingZeroBits(c.in); got != c.want {
			t.Errorf("%x: %d, want %d", c.in, got, c.want)
		}
	}
}

func TestSolveVerify(t *testing.T) {
	ch := []byte("challenge-challenge-challenge-32")
	ctr, err := Solve(context.Background(), ch, 16)
	if err != nil {
		t.Fatal(err)
	}
	if !Verify(ch, ctr, 16) {
		t.Fatal("own solution fails")
	}
	d := Digest(ch, ctr)
	if LeadingZeroBits(d[:]) < 16 {
		t.Fatal("not enough zero bits")
	}
	if Verify(ch, nil, 1) || Verify(ch, make([]byte, MaxCounter+1), 1) || Verify(ch, ctr, MaxBits+1) {
		t.Fatal("bounds not enforced")
	}
	if !Verify(ch, nil, 0) {
		t.Fatal("0 bits must accept anything")
	}
	if _, err := Solve(context.Background(), ch, MaxBits+1); err != ErrTooHard {
		t.Fatal("solved above the maximum")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Solve(ctx, ch, MaxBits); err == nil {
		t.Fatal("a cancelled search kept going")
	}
}
