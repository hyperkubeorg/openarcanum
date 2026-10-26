// Package session implements the Noise XX handshake and the framed, encrypted
// transport that runs over a relay stream (docs/protocol.md §3).
//
// Noise_XX_25519_ChaChaPoly_SHA256 with prologue "openarcanum/noise/v1" (other
// uses of the same handshake pick their own prologue, so a session made for
// one can never be taken for another: maintainer peers use
// "openarcanum/meta-peer/v1").
// Message 1 carries ClientHello in the clear, message 2 the server's
// ServerIdentity, message 3 the client's ClientIdentity. Each identity payload
// signs the sender's Noise static key with its Ed25519 key.
package session

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/flynn/noise"
	"golang.org/x/crypto/curve25519"
	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/version"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

const (
	Prologue = "openarcanum/noise/v1"

	// fragmentSize is the plaintext per Noise transport message, leaving room
	// for the flag byte and the 16-byte tag under Noise's 65535-byte limit.
	fragmentSize = 65000
	// MaxMessage caps one reassembled application message.
	MaxMessage = 32 << 20
	// PreAdmissionMaxMessage caps what a peer that hasn't been admitted
	// yet (not a member of the community) can make the server reassemble:
	// a claim with its icon or a join request fits, a 32 MiB buffer per
	// unauthenticated session doesn't (ServerConfig.PreAdmission).
	PreAdmissionMaxMessage = 1 << 20

	flagMore = 0x01
)

var suite = noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashSHA256)

// prologueOr is the configured prologue, or the community one.
func prologueOr(p string) []byte {
	if p == "" {
		return []byte(Prologue)
	}
	return []byte(p)
}

// ErrIdentity is returned when a handshake identity proof fails.
var ErrIdentity = errors.New("session: identity proof failed")

// Transport carries whole messages in order (a relay stream does).
type Transport interface {
	Send([]byte) error
	Recv(ctx context.Context) ([]byte, error)
	Close(reason string)
}

// GenerateStatic makes a new X25519 Noise static key.
func GenerateStatic() (noise.DHKey, error) { return suite.GenerateKeypair(rand.Reader) }

// StaticFromPrivate rebuilds a static keypair from its 32-byte private half.
func StaticFromPrivate(priv []byte) (noise.DHKey, error) {
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return noise.DHKey{}, err
	}
	return noise.DHKey{Private: append([]byte(nil), priv...), Public: pub}, nil
}

// SignStatic is the identity proof over a Noise static public key.
func SignStatic(id *identity.Key, staticPub []byte) []byte {
	return id.Sign(identity.CtxNoiseStatic, staticPub)
}

// Session is an established, encrypted session.
type Session struct {
	t        Transport
	smu, rmu sync.Mutex
	send     *noise.CipherState
	recv     *noise.CipherState

	// PeerStatic is the peer's Noise static public key.
	PeerStatic []byte
	// Client is set on the server side, Server on the client side.
	Client *wire.ClientIdentity
	Server *wire.ServerIdentity
	// Version is the protocol version both sides chose (§3 "Versions").
	Version uint32

	// recvMax caps one reassembled message (0: MaxMessage).
	recvMax atomic.Int64
}

// SetRecvMax caps the messages Recv reassembles at n bytes (at most
// MaxMessage); a larger one ends the session.
func (s *Session) SetRecvMax(n int) { s.recvMax.Store(int64(min(n, MaxMessage))) }

// Admit lifts the pre-admission cap: the peer may now send messages up to
// MaxMessage.
func (s *Session) Admit() { s.recvMax.Store(MaxMessage) }

func (s *Session) recvLimit() int {
	if n := s.recvMax.Load(); n > 0 {
		return int(n)
	}
	return MaxMessage
}

// offered is what a ClientHello announces: its version list, or the single
// version field of a client from before lists existed.
func offered(h *wire.ClientHello) []uint32 {
	if len(h.Versions) > 0 {
		return h.Versions
	}
	if h.Version != 0 {
		return []uint32{h.Version}
	}
	return nil
}

func versionsOr(vs []uint32) []uint32 {
	if len(vs) == 0 {
		return version.Supported()
	}
	return vs
}

// ServerConfig is the responder's side of the handshake.
type ServerConfig struct {
	Static   noise.DHKey
	Identity *identity.Key
	// Cert returns the owner's cert for the server key, or nil before a claim.
	Cert func() *wire.SignedServerCert
	// StaticSig is the identity proof over Static.Public; nil means compute
	// SignStatic(Identity, Static.Public) on each handshake.
	StaticSig []byte
	// Versions overrides the protocol versions spoken (tests); empty means
	// version.Supported().
	Versions []uint32
	// Prologue overrides the Noise prologue; empty means Prologue.
	Prologue string
	// VerifyClient, if set, checks the client's proven identity once the
	// handshake is done; an error refuses the session.
	VerifyClient func(*wire.ClientIdentity) error
	// PreAdmission starts the session with PreAdmissionMaxMessage as its
	// receive cap, until the server calls Admit (community servers: the
	// peer isn't a member yet).
	PreAdmission bool
}

// Accept runs the responder side of the handshake.
func Accept(ctx context.Context, t Transport, cfg ServerConfig) (*Session, error) {
	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:   suite,
		Pattern:       noise.HandshakeXX,
		Initiator:     false,
		Prologue:      prologueOr(cfg.Prologue),
		StaticKeypair: cfg.Static,
	})
	if err != nil {
		return nil, err
	}
	m1, err := t.Recv(ctx)
	if err != nil {
		return nil, err
	}
	p1, _, _, err := hs.ReadMessage(nil, m1)
	if err != nil {
		return nil, fmt.Errorf("session: message 1: %w", err)
	}
	var hello wire.ClientHello
	if err := proto.Unmarshal(p1, &hello); err != nil {
		return nil, errors.New("session: malformed client hello")
	}
	ours := versionsOr(cfg.Versions)
	chosen, verr := version.Negotiate(ours, offered(&hello))

	var cert *wire.SignedServerCert
	if cfg.Cert != nil {
		cert = cfg.Cert()
	}
	staticSig := cfg.StaticSig
	if staticSig == nil {
		staticSig = SignStatic(cfg.Identity, cfg.Static.Public)
	}
	// With no version in common, message 2 still goes out (version 0 and
	// this server's list), so the client can say which side is outdated.
	p2, err := proto.Marshal(&wire.ServerIdentity{
		ServerKey: cfg.Identity.Public,
		StaticSig: staticSig,
		Cert:      cert,
		Version:   chosen,
		Versions:  ours,
	})
	if err != nil {
		return nil, err
	}
	m2, _, _, err := hs.WriteMessage(nil, p2)
	if err != nil {
		return nil, err
	}
	if err := t.Send(m2); err != nil {
		return nil, err
	}
	if verr != nil {
		return nil, fmt.Errorf("session: %w", verr)
	}

	m3, err := t.Recv(ctx)
	if err != nil {
		return nil, err
	}
	p3, cs1, cs2, err := hs.ReadMessage(nil, m3)
	if err != nil {
		return nil, fmt.Errorf("session: message 3: %w", err)
	}
	var client wire.ClientIdentity
	if err := proto.Unmarshal(p3, &client); err != nil {
		return nil, err
	}
	peerStatic := hs.PeerStatic()
	if err := identity.Verify(client.IdentityKey, identity.CtxNoiseStatic, peerStatic, client.StaticSig); err != nil {
		return nil, ErrIdentity
	}
	if cfg.VerifyClient != nil {
		if err := cfg.VerifyClient(&client); err != nil {
			return nil, err
		}
	}
	sn := &Session{t: t, send: cs2, recv: cs1, PeerStatic: peerStatic, Client: &client, Version: chosen}
	if cfg.PreAdmission {
		sn.SetRecvMax(PreAdmissionMaxMessage)
	}
	return sn, nil
}

// ClientConfig is the initiator's side of the handshake.
type ClientConfig struct {
	Static      noise.DHKey
	IdentityKey []byte
	// StaticSig = SignStatic(identity, Static.Public), kept in the vault so
	// sessions can reconnect while the profile is locked.
	StaticSig  []byte
	DeviceName string
	// VerifyServer checks the server's identity (pinned key or owner cert).
	VerifyServer func(*wire.ServerIdentity) error
	// Versions overrides the protocol versions spoken (tests); empty means
	// version.Supported().
	Versions []uint32
	// Prologue overrides the Noise prologue; empty means Prologue.
	Prologue string
}

// Dial runs the initiator side of the handshake.
func Dial(ctx context.Context, t Transport, cfg ClientConfig) (*Session, error) {
	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:   suite,
		Pattern:       noise.HandshakeXX,
		Initiator:     true,
		Prologue:      prologueOr(cfg.Prologue),
		StaticKeypair: cfg.Static,
	})
	if err != nil {
		return nil, err
	}
	ours := versionsOr(cfg.Versions)
	p1, _ := proto.Marshal(&wire.ClientHello{Versions: ours})
	m1, _, _, err := hs.WriteMessage(nil, p1)
	if err != nil {
		return nil, err
	}
	if err := t.Send(m1); err != nil {
		return nil, err
	}
	m2, err := t.Recv(ctx)
	if err != nil {
		return nil, err
	}
	p2, _, _, err := hs.ReadMessage(nil, m2)
	if err != nil {
		return nil, fmt.Errorf("session: message 2: %w", err)
	}
	var server wire.ServerIdentity
	if err := proto.Unmarshal(p2, &server); err != nil {
		return nil, err
	}
	if server.Version == 0 || !slices.Contains(ours, server.Version) {
		// Version 0: no version in common; the server's list says who is
		// behind. A version we didn't offer is a broken server.
		_, err := version.Negotiate(ours, server.Versions)
		if err == nil {
			err = fmt.Errorf("server chose version %d, which this build didn't offer", server.Version)
		}
		return nil, fmt.Errorf("session: %w", err)
	}
	peerStatic := hs.PeerStatic()
	if err := identity.Verify(server.ServerKey, identity.CtxNoiseStatic, peerStatic, server.StaticSig); err != nil {
		return nil, ErrIdentity
	}
	if cfg.VerifyServer != nil {
		if err := cfg.VerifyServer(&server); err != nil {
			return nil, err
		}
	}
	p3, err := proto.Marshal(&wire.ClientIdentity{
		IdentityKey: cfg.IdentityKey,
		StaticSig:   cfg.StaticSig,
		DeviceName:  cfg.DeviceName,
	})
	if err != nil {
		return nil, err
	}
	m3, cs1, cs2, err := hs.WriteMessage(nil, p3)
	if err != nil {
		return nil, err
	}
	if err := t.Send(m3); err != nil {
		return nil, err
	}
	return &Session{t: t, send: cs1, recv: cs2, PeerStatic: peerStatic, Server: &server, Version: server.Version}, nil
}

// Send encrypts and sends one application message, fragmenting as needed.
func (s *Session) Send(msg []byte) error {
	if len(msg) > MaxMessage {
		return fmt.Errorf("session: message of %d bytes exceeds %d", len(msg), MaxMessage)
	}
	s.smu.Lock()
	defer s.smu.Unlock()
	buf := make([]byte, 0, fragmentSize+1)
	for off := 0; ; off += fragmentSize {
		end := min(off+fragmentSize, len(msg))
		flag := byte(0)
		if end < len(msg) {
			flag = flagMore
		}
		buf = append(buf[:0], flag)
		buf = append(buf, msg[off:end]...)
		ct, err := s.send.Encrypt(nil, nil, buf)
		if err != nil {
			return err
		}
		if err := s.t.Send(ct); err != nil {
			return err
		}
		if flag == 0 {
			return nil
		}
	}
}

// Recv returns the next application message.
func (s *Session) Recv(ctx context.Context) ([]byte, error) {
	s.rmu.Lock()
	defer s.rmu.Unlock()
	var msg []byte
	for {
		ct, err := s.t.Recv(ctx)
		if err != nil {
			return nil, err
		}
		pt, err := s.recv.Decrypt(nil, nil, ct)
		if err != nil {
			s.t.Close("decrypt failed")
			return nil, fmt.Errorf("session: decrypt: %w", err)
		}
		if len(pt) == 0 {
			return nil, errors.New("session: empty fragment")
		}
		if len(msg)+len(pt)-1 > s.recvLimit() {
			s.t.Close("message too large")
			return nil, errors.New("session: message too large")
		}
		msg = append(msg, pt[1:]...)
		if pt[0]&flagMore == 0 {
			return msg, nil
		}
	}
}

// Close ends the underlying transport.
func (s *Session) Close(reason string) { s.t.Close(reason) }
