package community_test

import (
	"strings"
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Someone who isn't an admitted member can't make the server reassemble
// large requests (the session layer buffers up to 32 MiB per message): past
// 512 KiB the server hangs up.
func TestPreAdmissionRequestSizeBounded(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	stranger := testutil.NewMember(t, "Stranger")
	if err := stranger.Connect(ctx, e.relay.URL, e.serverKey()); err != nil {
		t.Fatal(err)
	}
	defer stranger.Close()
	big := strings.Repeat("x", 2<<20)
	_, err := stranger.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_Join{Join: &wire.Join{JoinKey: e.invite.Secret, DisplayName: big}}})
	if err == nil || strings.Contains(err.Error(), "rpc ") {
		t.Fatalf("a 2 MiB request before admission was read and answered: %v", err)
	}

	// A member's large requests are still read (up to 8 MiB).
	alice := e.member("Alice", e.invite.Secret)
	_, err = alice.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_UpdateProfile{UpdateProfile: &wire.UpdateProfile{About: big}}})
	if err == nil || !strings.Contains(err.Error(), "rpc ") {
		t.Fatalf("a member's 2 MiB request: %v", err)
	}
	if _, err := alice.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_Sync{Sync: &wire.Sync{}}}); err != nil {
		t.Fatalf("member session after a large request: %v", err)
	}
}
