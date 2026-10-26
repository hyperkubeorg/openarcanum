package community_test

import (
	"fmt"
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// The unsent-attachments cap is per member: one member filling theirs
// doesn't stop anyone else attaching files in the channel.
func TestStagedUploadsCappedPerMember(t *testing.T) {
	e := setup(t, nil)
	gen := e.owner.Channel("general").Id
	alice := e.member("Alice", e.invite.Secret)
	bob := e.member("Bob", e.invite.Secret)
	other := e.must(&wire.AdminAction{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: "other"}}}).Id
	for i := 0; i < community.MaxStagedPerMember; i++ {
		if _, err := stage(t, alice, gen, fmt.Sprintf("f%d.txt", i), "text/plain", []byte("x")); err != nil {
			t.Fatalf("staged upload %d: %v", i, err)
		}
	}
	_, err := stage(t, alice, gen, "one-more.txt", "text/plain", []byte("x"))
	wantCode(t, "staging over the member's cap", err, wire.RpcErrorCode_RPC_ERROR_CODE_RATE_LIMITED)
	// Bob isn't affected, and posting frees room for alice.
	f, err := stage(t, bob, gen, "bob.txt", "text/plain", []byte("bob"))
	if err != nil {
		t.Fatalf("another member blocked by alice's unsent files: %v", err)
	}
	if _, err := call(t, bob, &wire.Post{ChannelId: gen, FileIds: []int64{f.FileId}}); err != nil {
		t.Fatal(err)
	}
	if _, err := stage(t, alice, other, "elsewhere.txt", "text/plain", []byte("x")); err != nil {
		t.Fatalf("alice in another channel: %v", err)
	}
}
