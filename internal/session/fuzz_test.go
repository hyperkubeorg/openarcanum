package session

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/flynn/noise"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
)

// frames is a Transport that replays fixed frames, then reports io.EOF.
type frames struct {
	in     [][]byte
	sent   [][]byte
	closed string
}

func (f *frames) Send(b []byte) error { f.sent = append(f.sent, append([]byte(nil), b...)); return nil }

func (f *frames) Recv(context.Context) ([]byte, error) {
	if len(f.in) == 0 {
		return nil, io.EOF
	}
	b := f.in[0]
	f.in = f.in[1:]
	return b, nil
}

func (f *frames) Close(reason string) { f.closed = reason }

// established runs a raw XX handshake and returns both ends of the
// initiator→responder direction: the initiator's encrypting cipher state and
// the responder's decrypting one.
func established(t *testing.T) (*noise.CipherState, *noise.CipherState) {
	si, _ := GenerateStatic()
	sr, _ := GenerateStatic()
	i, err := noise.NewHandshakeState(noise.Config{CipherSuite: suite, Pattern: noise.HandshakeXX, Initiator: true, Prologue: []byte(Prologue), StaticKeypair: si})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := noise.NewHandshakeState(noise.Config{CipherSuite: suite, Pattern: noise.HandshakeXX, Prologue: []byte(Prologue), StaticKeypair: sr})
	m1, _, _, _ := i.WriteMessage(nil, nil)
	if _, _, _, err := r.ReadMessage(nil, m1); err != nil {
		t.Fatal(err)
	}
	m2, _, _, _ := r.WriteMessage(nil, nil)
	if _, _, _, err := i.ReadMessage(nil, m2); err != nil {
		t.Fatal(err)
	}
	m3, send, _, _ := i.WriteMessage(nil, nil)
	_, recv, _, err := r.ReadMessage(nil, m3)
	if err != nil {
		t.Fatal(err)
	}
	return send, recv
}

// splitFrames cuts data into chunks, each prefixed by a 1-byte length.
func splitFrames(data []byte) [][]byte {
	var out [][]byte
	for len(data) > 0 {
		n := min(int(data[0]), len(data)-1)
		out = append(out, data[1:1+n])
		data = data[1+n:]
	}
	return out
}

// FuzzRecv feeds Session.Recv the frames a relay stream could deliver. With
// authentic=true each chunk is a plaintext fragment (flag byte + body) that
// the peer encrypts, and Recv must reassemble exactly what a reference model
// says; otherwise the chunks are raw, unauthenticated frames and Recv must
// fail closed. Recv must never panic.
func FuzzRecv(f *testing.F) {
	f.Add([]byte{}, true)
	f.Add([]byte{5, 0, 'h', 'e', 'l', 'o'}, true)
	f.Add([]byte{3, flagMore, 'a', 'b', 2, 0, 'c'}, true)
	f.Add([]byte{3, flagMore, 'a', 'b', 0, 2, 0, 'c'}, true)
	f.Add([]byte{1, 0xff, 1, 0xfe}, true)
	f.Add([]byte{4, 1, 2, 3, 4}, false)
	f.Add(bytes.Repeat([]byte{40, 7}, 41), false)
	f.Fuzz(func(t *testing.T, data []byte, authentic bool) {
		send, recv := established(t)
		chunks := splitFrames(data)
		tr := &frames{}
		for _, c := range chunks {
			if authentic {
				ct, err := send.Encrypt(nil, nil, c)
				if err != nil {
					t.Fatal(err)
				}
				tr.in = append(tr.in, ct)
			} else {
				tr.in = append(tr.in, c)
			}
		}
		s := &Session{t: tr, recv: recv}

		if !authentic {
			_, err := s.Recv(context.Background())
			if len(chunks) == 0 {
				if !errors.Is(err, io.EOF) {
					t.Fatalf("no frames: got %v", err)
				}
				return
			}
			if err == nil || tr.closed != "decrypt failed" {
				t.Fatalf("unauthenticated frame accepted: err=%v closed=%q", err, tr.closed)
			}
			return
		}

		// Reference model: messages end at a fragment whose flag lacks
		// flagMore; an empty fragment is an error.
		var want [][]byte
		var cur []byte
		modelErr := false
		for _, c := range chunks {
			if len(c) == 0 {
				modelErr = true
				break
			}
			cur = append(cur, c[1:]...)
			if c[0]&flagMore == 0 {
				want = append(want, cur)
				cur = nil
			}
		}
		for _, w := range want {
			got, err := s.Recv(context.Background())
			if err != nil {
				t.Fatalf("Recv: %v", err)
			}
			if !bytes.Equal(got, w) {
				t.Fatalf("got %x, want %x", got, w)
			}
		}
		_, err := s.Recv(context.Background())
		switch {
		case modelErr:
			if err == nil || errors.Is(err, io.EOF) {
				t.Fatalf("empty fragment: got %v", err)
			}
		case !errors.Is(err, io.EOF):
			t.Fatalf("trailing frames: got %v, want EOF", err)
		}
	})
}

// FuzzAccept feeds the server's handshake the two messages a client sends.
// Whatever arrives, Accept must not panic, and random bytes can never
// complete a handshake (message 3 is authenticated).
func FuzzAccept(f *testing.F) {
	id, _ := identity.Generate()
	static, _ := GenerateStatic()
	cfg := ServerConfig{Static: static, Identity: id}
	f.Add([]byte{}, []byte{})
	f.Add(make([]byte, 32), make([]byte, 64))
	f.Add(append(make([]byte, 32), 0x08, 0x01), make([]byte, 96))
	f.Add(bytes.Repeat([]byte{0xff}, 40), []byte{1, 2, 3})
	f.Fuzz(func(t *testing.T, m1, m3 []byte) {
		tr := &frames{in: [][]byte{m1, m3}}
		s, err := Accept(context.Background(), tr, cfg)
		if err == nil || s != nil {
			t.Fatalf("handshake accepted from fuzzed bytes")
		}
	})
}
