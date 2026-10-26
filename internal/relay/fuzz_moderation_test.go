package relay_test

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/relayclient"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// FuzzModerationFrames feeds a dedicated relay's host connection arbitrary
// moderation commands (docs/protocol.md §2 "Dedicated relays"): the command
// bytes as given, or, with fix set, decoded and given a valid server key,
// nonce and expiry and then signed, so the action handling (stream ids,
// block ids, durations) is reached too. The relay must answer every one
// (result or error), never panic, and keep the host connected; a member
// connection from another address must survive anything but a block that
// names its stream.
func FuzzModerationFrames(f *testing.F) {
	key, _ := identity.Generate()
	seed := func(c *wire.ModerationCommand) []byte {
		b, _ := proto.Marshal(c)
		return b
	}
	f.Add([]byte{}, false, false)
	f.Add([]byte{0xff, 0x01}, true, false)
	f.Add(seed(block([]uint32{1 | 0x80000000}, 60)), true, false)
	f.Add(seed(block(make([]uint32, 40), 0)), true, false)
	f.Add(seed(unblock(make([]byte, 16))), true, false)
	f.Add(seed(unblock([]byte{1})), true, true)
	f.Add(seed(&wire.ModerationCommand{ExpiresAt: -1}), false, true)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		f.Fatal(err)
	}
	d := serveDedicated(f, ln, key, filepath.Join(f.TempDir(), "blocks.json"), nil)
	ctx := context.Background()
	host, err := relayclient.DialHTTP(ctx, d.url("127.0.0.1"), from("127.0.0.1"))
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(host.Close)
	if err := host.RegisterEndpoint(ctx, key); err != nil {
		f.Fatal(err)
	}
	m, err := relayclient.DialHTTP(ctx, d.url("127.0.0.1"), from("127.0.0.9"))
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(m.Close)
	if _, err := m.OpenStream(ctx, key.Public); err != nil {
		f.Fatal(err)
	}
	st, err := host.Accept(ctx)
	if err != nil {
		f.Fatal(err)
	}
	hostBinding, err := host.Binding()
	if err != nil {
		f.Fatal(err)
	}

	f.Fuzz(func(t *testing.T, raw []byte, fix, signRaw bool) {
		cmd := raw
		if fix {
			c := &wire.ModerationCommand{}
			if proto.Unmarshal(raw, c) != nil {
				return
			}
			// Never the member's stream: blocking it is allowed and
			// would end the run's member connection.
			if b := c.GetBlock(); b != nil {
				for i, id := range b.StreamIds {
					if id == st.ID {
						b.StreamIds[i]++
					}
				}
			}
			c.ServerKey, c.RelayNonce, c.RelayKey, c.Url = key.Public, hostBinding.Nonce, hostBinding.RelayKey, hostBinding.URL
			if len(c.CommandId) != 16 {
				c.CommandId = make([]byte, 16)
			}
			copy(c.CommandId, raw)
			c.CommandId[15] ^= byte(time.Now().UnixNano())
			c.ExpiresAt = time.Now().Add(time.Minute).UnixMilli()
			cmd, _ = proto.Marshal(c)
		}
		sig := make([]byte, 64)
		if fix || signRaw {
			sig = key.Sign(identity.CtxRelayModeration, cmd)
		}
		tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		_, err := host.SendModeration(tctx, &wire.SignedModerationCommand{Command: cmd, Signature: sig})
		var re *relayclient.RelayError
		if err != nil && !errors.As(err, &re) {
			t.Fatalf("no answer from the relay: %v", err)
		}
		select {
		case <-host.Done():
			t.Fatal("a moderation command disconnected the host")
		case <-m.Done():
			t.Fatal("a command that didn't name the member's stream disconnected it")
		default:
		}
	})
}
