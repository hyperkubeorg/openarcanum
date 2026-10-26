// Package sframe is SFrame (RFC 9605) for test tools: the suite calls use,
// AES_128_GCM_SHA256_128, checked against the RFC's test vectors. The
// client's implementation is ui/js/sframe.js; this one lets synthetic call
// participants (cmd/callload) send frames real clients can open.
package sframe

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

// Suite is AES_128_GCM_SHA256_128 (RFC 9605 §4.5).
const Suite = 4

// The kind tags clients put first in a frame's SFrame metadata, binding
// each frame to the kind of media it carries (ui/js/call.js CALL.KIND_TAGS).
const (
	KindAudio       byte = 1
	KindCamera      byte = 2
	KindScreen      byte = 3
	KindScreenAudio byte = 4
)

// Metadata is a frame's SFrame metadata as clients build it: the kind's tag,
// then the bytes the frame leaves in the clear.
func Metadata(kind byte, clear []byte) []byte {
	return append([]byte{kind}, clear...)
}

const (
	nk = 16
	nn = 12
)

// Key is one sender's key: the base key and key id, derived per §4.4.2.
type Key struct {
	KID  uint64
	aead cipher.AEAD
	salt []byte
}

func label(kind string, kid uint64) []byte {
	b := []byte("SFrame 1.0 Secret " + kind + " ")
	b = binary.BigEndian.AppendUint64(b, kid)
	return binary.BigEndian.AppendUint16(b, Suite)
}

// NewKey derives the SFrame key and salt for kid from baseKey.
func NewKey(baseKey []byte, kid uint64) (*Key, error) {
	secret, err := hkdf.Extract(sha256.New, baseKey, nil)
	if err != nil {
		return nil, err
	}
	k, err := hkdf.Expand(sha256.New, secret, string(label("key", kid)), nk)
	if err != nil {
		return nil, err
	}
	salt, err := hkdf.Expand(sha256.New, secret, string(label("salt", kid)), nn)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Key{KID: kid, aead: aead, salt: salt}, nil
}

// minBytes is v in the fewest big-endian bytes (at least one).
func minBytes(v uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	i := 0
	for i < 7 && b[i] == 0 {
		i++
	}
	return b[i:]
}

// Header encodes the SFrame header for kid and ctr (§4.3).
func Header(kid, ctr uint64) []byte {
	var config byte
	var rest []byte
	if kid < 8 {
		config |= byte(kid) << 4
	} else {
		k := minBytes(kid)
		config |= 0x80 | byte(len(k)-1)<<4
		rest = append(rest, k...)
	}
	if ctr < 8 {
		config |= byte(ctr)
	} else {
		c := minBytes(ctr)
		config |= 0x08 | byte(len(c)-1)
		rest = append(rest, c...)
	}
	return append([]byte{config}, rest...)
}

func (k *Key) nonce(ctr uint64) []byte {
	n := make([]byte, nn)
	binary.BigEndian.PutUint64(n[nn-8:], ctr)
	for i := range n {
		n[i] ^= k.salt[i]
	}
	return n
}

// Seal encrypts one frame: header || AEAD(key, nonce(ctr), header || metadata, pt).
func (k *Key) Seal(ctr uint64, metadata, pt []byte) []byte {
	h := Header(k.KID, ctr)
	aad := append(append([]byte{}, h...), metadata...)
	return k.aead.Seal(h, k.nonce(ctr), pt, aad)
}

// ParseHeader reads a frame's key id and counter, and the header length.
func ParseHeader(b []byte) (kid, ctr uint64, n int, err error) {
	if len(b) == 0 {
		return 0, 0, 0, errors.New("empty frame")
	}
	c := b[0]
	n = 1
	read := func(l int) (uint64, error) {
		if n+l > len(b) {
			return 0, errors.New("truncated header")
		}
		var v uint64
		for _, x := range b[n : n+l] {
			v = v<<8 | uint64(x)
		}
		n += l
		return v, nil
	}
	if c&0x80 != 0 {
		if kid, err = read(int(c>>4&7) + 1); err != nil {
			return
		}
	} else {
		kid = uint64(c >> 4 & 7)
	}
	if c&0x08 != 0 {
		ctr, err = read(int(c&7) + 1)
	} else {
		ctr = uint64(c & 7)
	}
	return
}

// Open decrypts a frame sealed under this key.
func (k *Key) Open(frame, metadata []byte) ([]byte, error) {
	kid, ctr, n, err := ParseHeader(frame)
	if err != nil {
		return nil, err
	}
	if kid != k.KID {
		return nil, errors.New("wrong key id")
	}
	aad := append(append([]byte{}, frame[:n]...), metadata...)
	return k.aead.Open(nil, k.nonce(ctr), frame[n:], aad)
}
