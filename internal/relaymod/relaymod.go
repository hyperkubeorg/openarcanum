// Package relaymod signs and checks the moderation commands a community
// server sends to the relay dedicated to it (docs/protocol.md §2 "Dedicated
// relays"): block the networks behind some of the server's inbound streams,
// or lift such a block.
//
// A command is a serialized ModerationCommand signed with the server key
// under the context "openarcanum/relay-moderation/v2", the same Ed25519
// signature with domain separation that fan-out tokens (internal/fanout) and
// call controls (internal/callauth) use. It names the server key, the relay
// connection it is sent on (the relay's key, the URL the server dialled and
// the connection's Hello nonce; internal/relayid), an expiry at most MaxLifetime ahead, and a random
// command id the relay accepts once. The relay checks the signature against
// the one key it is dedicated to.
package relaymod

import (
	"bytes"
	"crypto/rand"
	"errors"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/relayid"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// MaxLifetime bounds how far ahead a command may expire.
const MaxLifetime = 5 * time.Minute

// Lifetime is what community servers ask for.
const Lifetime = time.Minute

// CommandIDLen is the length of a command id.
const CommandIDLen = 16

// MaxStreams bounds the streams one block names.
const MaxStreams = 32

// BlockIDLen is the length of the block ids relays hand out.
const BlockIDLen = 16

// MaxBlockSeconds bounds a block's duration (0, "until lifted", aside).
const MaxBlockSeconds = 400 * 24 * 3600

// Sign makes a command for the relay connection b names. The command's
// server key, relay binding, id and expiry are filled in here; cmd carries
// only the action.
func Sign(k *identity.Key, b relayid.Binding, now time.Time, cmd *wire.ModerationCommand) (*wire.SignedModerationCommand, error) {
	c := proto.Clone(cmd).(*wire.ModerationCommand)
	c.ServerKey = k.Public
	c.RelayNonce = b.Nonce
	c.RelayKey = b.RelayKey
	c.Url = b.URL
	c.CommandId = make([]byte, CommandIDLen)
	if _, err := rand.Read(c.CommandId); err != nil {
		return nil, err
	}
	c.ExpiresAt = now.Add(Lifetime).UnixMilli()
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(c)
	if err != nil {
		return nil, err
	}
	return &wire.SignedModerationCommand{Command: raw, Signature: k.Sign(identity.CtxRelayModeration, raw)}, nil
}

// Errors Verify returns; relays send their text back in Error.message.
var (
	ErrMalformed    = errors.New("malformed moderation command")
	ErrWrongServer  = errors.New("this relay is dedicated to another community server")
	ErrSignature    = errors.New("moderation command signature doesn't verify")
	ErrWrongRelay   = errors.New("moderation command is for another relay connection")
	ErrExpired      = errors.New("moderation command has expired")
	ErrTooLong      = errors.New("moderation command is valid for too long")
	ErrNoAction     = errors.New("moderation command has no action")
	ErrTooManyItems = errors.New("moderation command names too many streams")
	ErrDuration     = errors.New("a network block lasts at most 400 days")
)

// Verify checks a command against the key the relay is dedicated to and the
// connection it arrived on (local: the relay's key and URL, the
// connection's Hello nonce). It does not check replay: the relay remembers
// command ids until they expire.
func Verify(sc *wire.SignedModerationCommand, dedicatedTo []byte, local relayid.Local, now time.Time) (*wire.ModerationCommand, error) {
	if sc == nil || len(sc.Command) == 0 {
		return nil, ErrMalformed
	}
	c := &wire.ModerationCommand{}
	if err := proto.Unmarshal(sc.Command, c); err != nil || len(c.CommandId) != CommandIDLen {
		return nil, ErrMalformed
	}
	if len(dedicatedTo) == 0 || !bytes.Equal(c.ServerKey, dedicatedTo) {
		return nil, ErrWrongServer
	}
	if err := identity.Verify(dedicatedTo, identity.CtxRelayModeration, sc.Command, sc.Signature); err != nil {
		return nil, ErrSignature
	}
	if local.Check(c.RelayKey, c.Url, c.RelayNonce) != nil {
		return nil, ErrWrongRelay
	}
	exp := time.UnixMilli(c.ExpiresAt)
	if !now.Before(exp) {
		return nil, ErrExpired
	}
	if exp.After(now.Add(MaxLifetime)) {
		return nil, ErrTooLong
	}
	switch a := c.Action.(type) {
	case *wire.ModerationCommand_Block:
		if a.Block == nil || len(a.Block.StreamIds) == 0 {
			return nil, ErrNoAction
		}
		if len(a.Block.StreamIds) > MaxStreams {
			return nil, ErrTooManyItems
		}
		if a.Block.Seconds > MaxBlockSeconds {
			return nil, ErrDuration
		}
	case *wire.ModerationCommand_Unblock:
		if a.Unblock == nil || len(a.Unblock.BlockId) != BlockIDLen {
			return nil, ErrNoAction
		}
	default:
		return nil, ErrNoAction
	}
	return c, nil
}
