package relaymod

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/relayid"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

var now = time.UnixMilli(1_700_000_000_000)

func key(t testing.TB, b byte) *identity.Key {
	k, err := identity.FromSeed(bytes.Repeat([]byte{b}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// The relay the tests' commands are for.
var testRelayKey = bytes.Repeat([]byte{9}, 32)

const testRelayURL = "wss://relay.example/ws"

func bind(nonce []byte) relayid.Binding {
	return relayid.Binding{RelayKey: testRelayKey, URL: testRelayURL, Nonce: nonce}
}

func local(nonce []byte) relayid.Local {
	return relayid.Local{Key: testRelayKey, URL: testRelayURL, Nonce: nonce}
}

// A command is bound to the relay's key and URL, not only the connection's
// nonce: one signed for a relay that presented another relay's nonce is
// refused by that other relay.
func TestCommandBoundToRelay(t *testing.T) {
	k := key(t, 1)
	nonce := bytes.Repeat([]byte{5}, 32)
	for name, b := range map[string]relayid.Binding{
		"another relay's key": {RelayKey: bytes.Repeat([]byte{8}, 32), URL: testRelayURL, Nonce: nonce},
		"another relay's URL": {RelayKey: testRelayKey, URL: "wss://evil.example/ws", Nonce: nonce},
		"nonce only (v1)":     {Nonce: nonce},
	} {
		sc, err := Sign(k, b, now, blockCmd())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Verify(sc, k.Public, local(nonce), now); !errors.Is(err, ErrWrongRelay) {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

func blockCmd() *wire.ModerationCommand {
	return &wire.ModerationCommand{Action: &wire.ModerationCommand_Block{Block: &wire.NetworkBlock{StreamIds: []uint32{0x80000001}, Seconds: 3600}}}
}

func TestSignVerify(t *testing.T) {
	k := key(t, 1)
	nonce := bytes.Repeat([]byte{5}, 32)
	sc, err := Sign(k, bind(nonce), now, blockCmd())
	if err != nil {
		t.Fatal(err)
	}
	c, err := Verify(sc, k.Public, local(nonce), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.CommandId) != CommandIDLen || c.GetBlock().Seconds != 3600 {
		t.Fatalf("got %v", c)
	}
	// Each command gets its own id.
	sc2, _ := Sign(k, bind(nonce), now, blockCmd())
	c2, _ := Verify(sc2, k.Public, local(nonce), now)
	if bytes.Equal(c.CommandId, c2.CommandId) {
		t.Fatal("two commands share an id")
	}

	check := func(name string, err, want error) {
		t.Helper()
		if !errors.Is(err, want) {
			t.Fatalf("%s: got %v, want %v", name, err, want)
		}
	}
	_, err = Verify(sc, key(t, 2).Public, local(nonce), now)
	check("other relay's server", err, ErrWrongServer)
	_, err = Verify(sc, k.Public, local(bytes.Repeat([]byte{6}, 32)), now)
	check("other connection", err, ErrWrongRelay)
	_, err = Verify(sc, k.Public, local(nonce), now.Add(Lifetime))
	check("expired", err, ErrExpired)
	bad := proto.Clone(sc).(*wire.SignedModerationCommand)
	bad.Signature[0] ^= 1
	_, err = Verify(bad, k.Public, local(nonce), now)
	check("tampered signature", err, ErrSignature)
	// The same bytes under another context don't verify.
	other := &wire.SignedModerationCommand{Command: sc.Command, Signature: k.Sign(identity.CtxFanoutToken, sc.Command)}
	_, err = Verify(other, k.Public, local(nonce), now)
	check("other context", err, ErrSignature)

	resign := func(mut func(*wire.ModerationCommand)) *wire.SignedModerationCommand {
		c := &wire.ModerationCommand{}
		_ = proto.Unmarshal(sc.Command, c)
		mut(c)
		raw, _ := proto.Marshal(c)
		return &wire.SignedModerationCommand{Command: raw, Signature: k.Sign(identity.CtxRelayModeration, raw)}
	}
	_, err = Verify(resign(func(c *wire.ModerationCommand) { c.ExpiresAt = now.Add(time.Hour).UnixMilli() }), k.Public, local(nonce), now)
	check("too long", err, ErrTooLong)
	_, err = Verify(resign(func(c *wire.ModerationCommand) { c.Action = nil }), k.Public, local(nonce), now)
	check("no action", err, ErrNoAction)
	_, err = Verify(resign(func(c *wire.ModerationCommand) { c.GetBlock().StreamIds = make([]uint32, MaxStreams+1) }), k.Public, local(nonce), now)
	check("too many streams", err, ErrTooManyItems)
	_, err = Verify(resign(func(c *wire.ModerationCommand) { c.GetBlock().Seconds = MaxBlockSeconds + 1 }), k.Public, local(nonce), now)
	check("too long a block", err, ErrDuration)
	_, err = Verify(resign(func(c *wire.ModerationCommand) {
		c.Action = &wire.ModerationCommand_Unblock{Unblock: &wire.NetworkUnblock{BlockId: []byte{1, 2}}}
	}), k.Public, local(nonce), now)
	check("short block id", err, ErrNoAction)
	_, err = Verify(resign(func(c *wire.ModerationCommand) { c.CommandId = c.CommandId[:4] }), k.Public, local(nonce), now)
	check("short command id", err, ErrMalformed)
	_, err = Verify(nil, k.Public, local(nonce), now)
	check("nil", err, ErrMalformed)
	_, err = Verify(sc, nil, local(nonce), now)
	check("relay without a key", err, ErrWrongServer)
}

// FuzzVerify checks the decoder relays run on a command's bytes: it never
// panics, and anything it accepts is signed by the key, for the nonce, and
// has a well-formed action.
func FuzzVerify(f *testing.F) {
	k := key(f, 1)
	nonce := bytes.Repeat([]byte{5}, 32)
	sc, _ := Sign(k, bind(nonce), now, blockCmd())
	f.Add(sc.Command, sc.Signature)
	un, _ := Sign(k, bind(nonce), now, &wire.ModerationCommand{Action: &wire.ModerationCommand_Unblock{Unblock: &wire.NetworkUnblock{BlockId: make([]byte, 16)}}})
	f.Add(un.Command, un.Signature)
	f.Add([]byte{0x0a, 0xff}, make([]byte, 64))
	f.Fuzz(func(t *testing.T, cmd, sig []byte) {
		c, err := Verify(&wire.SignedModerationCommand{Command: cmd, Signature: sig}, k.Public, local(nonce), now)
		if err != nil {
			return
		}
		if identity.Verify(k.Public, identity.CtxRelayModeration, cmd, sig) != nil || !bytes.Equal(c.RelayNonce, nonce) {
			t.Fatal("accepted a command that isn't signed for this connection")
		}
		if c.GetBlock() == nil && c.GetUnblock() == nil {
			t.Fatal("accepted a command without an action")
		}
	})
}
