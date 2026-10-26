// Package identity holds Ed25519 keys and the domain-separated signatures used
// across the protocol. Every signature covers context || 0x00 || message, so a
// signature made for one purpose can never be replayed as another.
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Signature contexts. Python mirrors these in clients/desktop/src/openarcanum/keys.py.
const (
	// Endpoint registration, bound to the relay's key, URL and connection
	// nonce (a RelayBinding; docs/protocol.md §2 "Relay identity"). v1
	// signed only the nonce, which a malicious relay could relay from
	// another relay.
	CtxRelayEndpoint = "openarcanum/relay-endpoint/v2"
	// A relay's proof of its key for one connection (Hello.relay_signature
	// over a RelayBinding).
	CtxRelayHello        = "openarcanum/relay-hello/v1"
	CtxNoiseStatic       = "openarcanum/noise-static/v1"
	CtxServerCert        = "openarcanum/server-cert/v1"
	CtxChannelEnvelope   = "openarcanum/channel-envelope/v1"
	CtxAdminAction       = "openarcanum/admin-action/v1"
	CtxDeviceRevocation  = "openarcanum/device-revocation/v1"
	CtxRoute             = "openarcanum/route/v1"
	CtxListing           = "openarcanum/listing/v1"
	CtxRelayHeartbeat    = "openarcanum/relay-heartbeat/v1"
	CtxContactCard       = "openarcanum/contact-card/v1"
	CtxSignedTimestamped = "openarcanum/timestamped/v1"
	CtxCallGrant         = "openarcanum/call-grant/v1"
	CtxCallControl       = "openarcanum/call-control/v1"
	// Fan-out tokens and relay moderation commands, bound to the relay's
	// key, URL and connection nonce (v1: the nonce only).
	CtxFanoutToken     = "openarcanum/fanout-token/v2"
	CtxRelayModeration = "openarcanum/relay-moderation/v2"
	// A community server's liveness report to maintainer nodes
	// (docs/protocol.md §7.4).
	CtxCommunityHeartbeat = "openarcanum/community-heartbeat/v1"
	// A device able to receive statuses in a community, vouched for by the
	// member key (docs/protocol.md §9.8).
	CtxStatusDevice = "openarcanum/status-device/v1"
	// A maintainer cluster's member list, signed by its operator key
	// (docs/protocol.md §7.5).
	CtxMaintainerSet = "openarcanum/maintainer-set/v1"
)

// ErrBadSignature is returned for any signature that fails verification.
var ErrBadSignature = errors.New("identity: bad signature")

// Key is an Ed25519 keypair.
type Key struct {
	Private ed25519.PrivateKey
	Public  ed25519.PublicKey
}

func Generate() (*Key, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Key{Private: priv, Public: pub}, nil
}

// FromSeed rebuilds a keypair from its 32-byte seed.
func FromSeed(seed []byte) (*Key, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("identity: seed must be %d bytes", ed25519.SeedSize)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	return &Key{Private: priv, Public: priv.Public().(ed25519.PublicKey)}, nil
}

// Seed returns the 32-byte seed for storage.
func (k *Key) Seed() []byte { return k.Private.Seed() }

func signedBytes(ctx string, msg []byte) []byte {
	b := make([]byte, 0, len(ctx)+1+len(msg))
	b = append(b, ctx...)
	b = append(b, 0)
	return append(b, msg...)
}

// Sign signs msg under the given context.
func (k *Key) Sign(ctx string, msg []byte) []byte {
	return ed25519.Sign(k.Private, signedBytes(ctx, msg))
}

// Verify checks sig over msg under ctx for the given public key.
func Verify(pub []byte, ctx string, msg, sig []byte) error {
	if len(pub) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize {
		return ErrBadSignature
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), signedBytes(ctx, msg), sig) {
		return ErrBadSignature
	}
	return nil
}

// ValidPublic reports whether b has the length of an Ed25519 public key.
func ValidPublic(b []byte) bool { return len(b) == ed25519.PublicKeySize }

// Encode renders bytes as unpadded base64url, the form used in pasted strings.
func Encode(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// Decode parses unpadded (or padded) base64url.
func Decode(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(strings.TrimSpace(s), "="))
}

// Fingerprint is a short, stable label for a key, safe to log.
func Fingerprint(pub []byte) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:6])
}

// LoadOrCreate reads a seed file, creating it (mode 0600) when missing.
func LoadOrCreate(path string) (*Key, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		seed, derr := hex.DecodeString(strings.TrimSpace(string(b)))
		if derr != nil {
			return nil, fmt.Errorf("identity: %s: %w", path, derr)
		}
		return FromSeed(seed)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	k, err := Generate()
	if err != nil {
		return nil, err
	}
	if err := WriteSeed(path, k); err != nil {
		return nil, err
	}
	return k, nil
}

// WriteSeed stores the key's seed as hex, atomically, with mode 0600.
func WriteSeed(path string, k *Key) error {
	return WriteFileAtomic(path, []byte(hex.EncodeToString(k.Seed())+"\n"), 0o600)
}

// WriteFileAtomic writes to a temp file in the same directory and renames it.
func WriteFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
