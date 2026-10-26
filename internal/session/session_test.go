package session

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// pipe is an in-memory Transport pair.
type pipe struct {
	in, out chan []byte
	done    chan struct{}
}

func newPipe() (*pipe, *pipe) {
	a, b := make(chan []byte, 1024), make(chan []byte, 1024)
	done := make(chan struct{})
	return &pipe{in: a, out: b, done: done}, &pipe{in: b, out: a, done: done}
}

func (p *pipe) Send(b []byte) error {
	select {
	case p.out <- append([]byte(nil), b...):
		return nil
	case <-p.done:
		return errors.New("closed")
	}
}

func (p *pipe) Recv(ctx context.Context) ([]byte, error) {
	// Data sent before a close is still delivered, like a relay stream.
	select {
	case b := <-p.in:
		return b, nil
	default:
	}
	select {
	case b := <-p.in:
		return b, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.done:
		return nil, errors.New("closed")
	}
}

func (p *pipe) Close(string) {
	select {
	case <-p.done:
	default:
		close(p.done)
	}
}

type fixture struct {
	serverID, clientID *identity.Key
	srv                ServerConfig
	cli                ClientConfig
}

func newFixture(t *testing.T) *fixture {
	sid, _ := identity.Generate()
	cid, _ := identity.Generate()
	sst, err := GenerateStatic()
	if err != nil {
		t.Fatal(err)
	}
	cst, _ := GenerateStatic()
	f := &fixture{serverID: sid, clientID: cid}
	f.srv = ServerConfig{Static: sst, Identity: sid}
	f.cli = ClientConfig{
		Static:      cst,
		IdentityKey: cid.Public,
		StaticSig:   SignStatic(cid, cst.Public),
		DeviceName:  "laptop",
		VerifyServer: func(si *wire.ServerIdentity) error {
			if !bytes.Equal(si.ServerKey, sid.Public) {
				return errors.New("unexpected server key")
			}
			return nil
		},
	}
	return f
}

func handshake(t *testing.T, f *fixture) (*Session, *Session, error, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a, b := newPipe()
	type res struct {
		s   *Session
		err error
	}
	ch := make(chan res, 1)
	go func() {
		s, err := Accept(ctx, b, f.srv)
		if err != nil {
			b.Close("handshake failed")
		}
		ch <- res{s, err}
	}()
	cs, cerr := Dial(ctx, a, f.cli)
	if cerr != nil {
		a.Close("handshake failed")
	}
	r := <-ch
	return cs, r.s, cerr, r.err
}

// With PreAdmission, a peer the server hasn't admitted yet can make it
// reassemble at most PreAdmissionMaxMessage, not MaxMessage (32 MiB),
// until Admit.
func TestPreAdmissionCap(t *testing.T) {
	f := newFixture(t)
	f.srv.PreAdmission = true
	cs, ss, cerr, serr := handshake(t, f)
	if cerr != nil || serr != nil {
		t.Fatalf("handshake failed: %v / %v", cerr, serr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	fits := make([]byte, PreAdmissionMaxMessage)
	if err := cs.Send(fits); err != nil {
		t.Fatal(err)
	}
	if got, err := ss.Recv(ctx); err != nil || len(got) != len(fits) {
		t.Fatalf("a message at the cap: %d bytes, %v", len(got), err)
	}
	// Admitted, the peer may send more.
	ss.Admit()
	big := make([]byte, 2*PreAdmissionMaxMessage)
	if err := cs.Send(big); err != nil {
		t.Fatal(err)
	}
	if got, err := ss.Recv(ctx); err != nil || len(got) != len(big) {
		t.Fatalf("after Admit: %d bytes, %v", len(got), err)
	}

	// Not admitted: one byte over the cap ends the session.
	f2 := newFixture(t)
	f2.srv.PreAdmission = true
	cs2, ss2, cerr, serr := handshake(t, f2)
	if cerr != nil || serr != nil {
		t.Fatalf("handshake failed: %v / %v", cerr, serr)
	}
	_ = cs2.Send(make([]byte, PreAdmissionMaxMessage+1))
	if _, err := ss2.Recv(ctx); err == nil {
		t.Fatal("a message over the pre-admission cap was reassembled")
	}

	// Without PreAdmission (clients, maintainer peers) the cap is MaxMessage.
	f3 := newFixture(t)
	cs3, ss3, cerr, serr := handshake(t, f3)
	if cerr != nil || serr != nil {
		t.Fatalf("handshake failed: %v / %v", cerr, serr)
	}
	if err := cs3.Send(big); err != nil {
		t.Fatal(err)
	}
	if got, err := ss3.Recv(ctx); err != nil || len(got) != len(big) {
		t.Fatalf("default cap: %d bytes, %v", len(got), err)
	}
}

func TestHandshakeAndTransport(t *testing.T) {
	f := newFixture(t)
	cs, ss, cerr, serr := handshake(t, f)
	if cerr != nil || serr != nil {
		t.Fatalf("handshake failed: %v / %v", cerr, serr)
	}
	if !bytes.Equal(ss.Client.IdentityKey, f.clientID.Public) || ss.Client.DeviceName != "laptop" {
		t.Fatal("server saw the wrong client identity")
	}
	if !bytes.Equal(ss.PeerStatic, f.cli.Static.Public) {
		t.Fatal("server saw the wrong device key")
	}
	ctx := context.Background()
	big := make([]byte, 300_000)
	rand.Read(big)
	for _, msg := range [][]byte{[]byte("hi"), {}, big} {
		if err := cs.Send(msg); err != nil {
			t.Fatal(err)
		}
		got, err := ss.Recv(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, msg) {
			t.Fatalf("message of %d bytes corrupted", len(msg))
		}
	}
	if err := ss.Send([]byte("back")); err != nil {
		t.Fatal(err)
	}
	if got, err := cs.Recv(ctx); err != nil || string(got) != "back" {
		t.Fatalf("server->client failed: %q %v", got, err)
	}
}

func TestTamperedClientSignatureRejected(t *testing.T) {
	f := newFixture(t)
	f.cli.StaticSig = append([]byte(nil), f.cli.StaticSig...)
	f.cli.StaticSig[3] ^= 0x40
	_, _, _, serr := handshake(t, f)
	if !errors.Is(serr, ErrIdentity) {
		t.Fatalf("tampered client signature: server error = %v", serr)
	}
}

func TestClientSignatureForOtherDeviceRejected(t *testing.T) {
	f := newFixture(t)
	other, _ := GenerateStatic()
	f.cli.StaticSig = SignStatic(f.clientID, other.Public)
	_, _, _, serr := handshake(t, f)
	if !errors.Is(serr, ErrIdentity) {
		t.Fatalf("signature for another static key accepted: %v", serr)
	}
}

func TestUnpinnedServerRejected(t *testing.T) {
	f := newFixture(t)
	imposter, _ := identity.Generate()
	f.srv.Identity = imposter
	_, _, cerr, _ := handshake(t, f)
	if cerr == nil {
		t.Fatal("client accepted an unpinned server key")
	}
}

func TestTamperedServerSignatureRejected(t *testing.T) {
	f := newFixture(t)
	other, _ := GenerateStatic()
	f.srv.StaticSig = SignStatic(f.serverID, other.Public)
	_, _, cerr, _ := handshake(t, f)
	if !errors.Is(cerr, ErrIdentity) {
		t.Fatalf("tampered server signature: client error = %v", cerr)
	}
}

// Sessions for different purposes use different prologues and can't be
// mixed up: a handshake whose two sides disagree on the prologue fails.
func TestPrologueSeparatesUses(t *testing.T) {
	f := newFixture(t)
	f.srv.Prologue, f.cli.Prologue = "openarcanum/meta-peer/v1", "openarcanum/meta-peer/v1"
	if _, _, cerr, serr := handshake(t, f); cerr != nil || serr != nil {
		t.Fatalf("same custom prologue: %v / %v", cerr, serr)
	}
	f.cli.Prologue = ""
	if _, _, cerr, serr := handshake(t, f); cerr == nil && serr == nil {
		t.Fatal("a community session was accepted by a responder using another prologue")
	}
}

// VerifyClient refuses a client whose proven identity isn't wanted.
func TestVerifyClientRefuses(t *testing.T) {
	f := newFixture(t)
	refused := errors.New("not trusted")
	f.srv.VerifyClient = func(ci *wire.ClientIdentity) error {
		if bytes.Equal(ci.IdentityKey, f.clientID.Public) {
			return refused
		}
		return nil
	}
	if _, _, _, serr := handshake(t, f); !errors.Is(serr, refused) {
		t.Fatalf("VerifyClient's refusal: %v", serr)
	}
}

func TestStaticFromPrivate(t *testing.T) {
	k, _ := GenerateStatic()
	r, err := StaticFromPrivate(k.Private)
	if err != nil || !bytes.Equal(r.Public, k.Public) {
		t.Fatal("static key rebuild mismatch")
	}
}
