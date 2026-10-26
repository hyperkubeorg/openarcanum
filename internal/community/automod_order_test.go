package community_test

import (
	"strings"
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Permission and other checks come before AutoMod: a request refused for
// another reason gets that error, and AutoMod neither alerts nor times the
// member out.
func TestAutoModRunsAfterPermissionChecks(t *testing.T) {
	e := setup(t, nil)
	gen := e.owner.Channel("general").Id
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	if _, err := saveRule(e, &wire.AutoModRule{Name: "Words", Enabled: true, Trigger: wire.AutoModTrigger_AUTOMOD_TRIGGER_KEYWORD,
		Keywords: []string{"badword"}, Block: true, Alert: true, TimeoutSeconds: 600}); err != nil {
		t.Fatal(err)
	}
	notAutoMod := func(what string, err error) {
		t.Helper()
		if err == nil || strings.Contains(err.Error(), "AutoMod") {
			t.Fatalf("%s: got %v, want a non-AutoMod refusal", what, err)
		}
	}
	// Attach Files taken away after staging a file.
	f, err := stage(t, alice, gen, "a.txt", "text/plain", []byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	e.must(override(&wire.SetChannelOverride{ChannelId: gen, Member: alice.Identity.Public, Deny: community.PermAttachFiles}))
	_, err = call(t, alice, &wire.Post{ChannelId: gen, Text: "badword", FileIds: []int64{f.FileId}})
	permDenied(t, "posting a file without Attach Files", err)
	notAutoMod("posting a file without Attach Files", err)
	// A caption on a file over the size limit.
	_, err = alice.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_UploadBegin{UploadBegin: &wire.UploadBegin{ChannelId: gen, Name: "big.bin", Size: 1 << 40, Text: "badword"}}})
	notAutoMod("uploading without Attach Files", err)
	e.must(override(&wire.SetChannelOverride{ChannelId: gen, Member: alice.Identity.Public}))
	_, err = alice.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_UploadBegin{UploadBegin: &wire.UploadBegin{ChannelId: gen, Name: "big.bin", Size: 1 << 40, Text: "badword"}}})
	wantCode(t, "a caption on a file over the limit", err, wire.RpcErrorCode_RPC_ERROR_CODE_TOO_LARGE)
	// Send Messages denied: posting and editing get the permission error.
	ok := post(t, alice, gen, "harmless")
	e.must(override(&wire.SetChannelOverride{ChannelId: gen, Member: alice.Identity.Public, Deny: community.PermSendMessages}))
	_, err = alice.Post(ctx, gen, "badword")
	permDenied(t, "posting without Send Messages", err)
	_, err = call(t, alice, &wire.Edit{MessageId: ok.Id, Text: "badword"})
	permDenied(t, "editing without Send Messages", err)

	// None of it reached AutoMod: no alerts, no timeout.
	if reps := openReports(t, e.owner); len(reps) != 0 {
		t.Fatalf("AutoMod alerted on refused requests: %v", reps)
	}
	e.must(override(&wire.SetChannelOverride{ChannelId: gen, Member: alice.Identity.Public}))
	if _, err := alice.Post(ctx, gen, "still allowed to talk"); err != nil {
		t.Fatalf("alice was timed out by AutoMod: %v", err)
	}
	// AutoMod still applies to an allowed post.
	_, err = alice.Post(ctx, gen, "badword")
	wantErr(t, err, "AutoMod blocked")
}
