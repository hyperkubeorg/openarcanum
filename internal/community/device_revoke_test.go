package community_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Revoking a device rotates the keys and topics of the channels its member
// can see, as removing a member does: the revoked device (stolen, lost)
// already holds the old ones and could keep reading new messages from the
// relays. The member's other devices get the new keys.
func TestDeviceRevokeRotatesChannels(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	d2 := testutil.NewMember(t, "Alice")
	d2.Identity = alice.Identity
	if err := d2.Connect(ctx, e.relay.URL, e.serverKey()); err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	gen := e.owner.Channel("general").Id
	old := e.channelKey(gen)
	rev, _ := protoMarshal(&wire.DeviceRevocation{Community: e.id(), IdentityKey: alice.Identity.Public, DeviceKey: d2.Static.Public, At: time.Now().UnixMilli()})
	if _, err := alice.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_RevokeDevice{RevokeDevice: &wire.SignedDeviceRevocation{
		Revocation: rev, Signature: alice.Identity.Sign(identity.CtxDeviceRevocation, rev),
	}}}); err != nil {
		t.Fatal(err)
	}
	next := e.channelKey(gen)
	if bytes.Equal(old, next) {
		t.Fatal("the channel key survived the device's revocation")
	}
	for _, c := range fresh(t, alice).Channels {
		if c.Id == gen && !bytes.Equal(c.Key, next) {
			t.Fatal("alice's remaining device didn't get the new key")
		}
	}
	// Revoking it again changes nothing more.
	rev2, _ := protoMarshal(&wire.DeviceRevocation{Community: e.id(), IdentityKey: alice.Identity.Public, DeviceKey: d2.Static.Public, At: time.Now().UnixMilli() + 1})
	if _, err := alice.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_RevokeDevice{RevokeDevice: &wire.SignedDeviceRevocation{
		Revocation: rev2, Signature: alice.Identity.Sign(identity.CtxDeviceRevocation, rev2),
	}}}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(e.channelKey(gen), next) {
		t.Fatal("a repeated revocation rotated again")
	}
}
