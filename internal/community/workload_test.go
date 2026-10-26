package community_test

import (
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// workloadSettings are community settings with recognizable plaintext.
func workloadSettings() *wire.CommunitySettings {
	return &wire.CommunitySettings{
		Name: "Zephyr Okapi Society", Description: "A description mentioning the blue axolotl",
		Visibility: wire.Visibility_VISIBILITY_PRIVATE, JoinPolicy: wire.JoinPolicy_JOIN_POLICY_INVITE_ONLY,
		MessageRetentionDays: 30, FileRetentionDays: 7, SearchEnabled: true,
	}
}

// workloadResult is what a realistic workload stored: every string and key
// that must never appear in the database file in plaintext.
type workloadResult struct {
	Strings [][]byte
	Alice   *testutil.Member
	Heron   int64
}

// runWorkload exercises the member roll, roles, channels, overrides, join keys
// (private and public), bans, messages, edits, deletions, files, avatars,
// contact cards and the audit log. e must have been set up with
// workloadSettings.
func runWorkload(t *testing.T, e *env) *workloadResult {
	t.Helper()
	ctx := testutil.Ctx(t)
	admin := func(a *wire.AdminAction) *wire.AdminResult {
		t.Helper()
		res, err := e.owner.Admin(ctx, e.id(), a)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	alice := e.member("Alice Marmot", e.invite.Secret)
	mallory := e.member("Mallory Narwhal", e.invite.Secret)

	role := admin(&wire.AdminAction{Op: &wire.AdminAction_CreateRole{CreateRole: &wire.CreateRole{Name: "Pelican Wranglers", Permissions: community.PermViewChannel}}})
	admin(&wire.AdminAction{Op: &wire.AdminAction_AssignRoles{AssignRoles: &wire.AssignRoles{Member: alice.Identity.Public, RoleIds: []int64{role.Id}}}})
	heron := admin(&wire.AdminAction{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: "heron-lounge"}}})
	admin(&wire.AdminAction{Op: &wire.AdminAction_SetChannelOverride{SetChannelOverride: &wire.SetChannelOverride{
		ChannelId: heron.Id, RoleId: role.Id, Allow: community.PermSendMessages,
	}}})
	// Categories, topics and per-member overrides (schema 4).
	cat := admin(&wire.AdminAction{Op: &wire.AdminAction_CreateCategory{CreateCategory: &wire.CreateCategory{Name: "Cormorant Category"}}})
	admin(&wire.AdminAction{Op: &wire.AdminAction_SetChannelOverride{SetChannelOverride: &wire.SetChannelOverride{
		CategoryId: cat.Id, Member: mallory.Identity.Public, Deny: community.PermAddReactions,
	}}})
	admin(&wire.AdminAction{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{
		Name: "kestrel-den", CategoryId: cat.Id, Description: "osprey topic text",
	}}})
	private := admin(&wire.AdminAction{Op: &wire.AdminAction_CreateJoinKey{CreateJoinKey: &wire.CreateJoinKey{Label: "Walrus invite label", RoleIds: []int64{role.Id}}}}).JoinKey
	public := admin(&wire.AdminAction{Op: &wire.AdminAction_CreateJoinKey{CreateJoinKey: &wire.CreateJoinKey{Label: "Gannet public label", Public: true}}}).JoinKey
	admin(&wire.AdminAction{Op: &wire.AdminAction_Ban{Ban: &wire.BanMember{Member: mallory.Identity.Public, Reason: "posting ibex spam"}}})

	gen := e.owner.Channel("general")
	if _, err := e.owner.Post(ctx, gen.Id, "the secret capybara message"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.owner.Post(ctx, heron.Id, "heron lounge chatter about quokkas"); err != nil {
		t.Fatal(err)
	}
	am, err := alice.Post(ctx, gen.Id, "alice says flamingo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := alice.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_Edit{Edit: &wire.Edit{MessageId: am.Id, Text: "alice says flamingo, edited"}}}); err != nil {
		t.Fatal(err)
	}
	gone, _ := alice.Post(ctx, gen.Id, "deleted dugong remark")
	if _, err := alice.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_Delete{Delete: &wire.DeleteMessage{MessageId: gone.Id}}}); err != nil {
		t.Fatal(err)
	}

	fileData := []byte("tapir file contents, confidential")
	r, err := e.owner.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_UploadBegin{UploadBegin: &wire.UploadBegin{
		ChannelId: gen.Id, Name: "tapir-plans.txt", Size: uint64(len(fileData)), Mime: "application/x-jackalope", Text: "caption about a gecko",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	up := r.GetUploadStarted().UploadId
	if _, err := e.owner.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_UploadChunk{UploadChunk: &wire.UploadChunk{UploadId: up, Data: fileData}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.owner.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_UploadFinish{UploadFinish: &wire.UploadFinish{UploadId: up, Sha256: sum(fileData)}}}); err != nil {
		t.Fatal(err)
	}

	avatar := append(pngHeader(1, 1), []byte("avatar-lemur-pixels")...) // a 1 × 1 PNG header (images are checked by size)
	if _, err := alice.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_SetProfile{SetProfile: &wire.SetProfile{Avatar: avatar}}}); err != nil {
		t.Fatal(err)
	}
	card := []byte("contact-card-quetzal-inbox")
	pkg := []byte("key-package-pangolin")
	dmDevice := []byte("dm-device-curve-tapir")
	if _, err := alice.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_PublishContactCard{PublishContactCard: &wire.PublishContactCard{
		Card: card, Devices: [][]byte{dmDevice}, KeyPackages: []*wire.KeyPackage{{Device: dmDevice, Package: pkg}},
	}}}); err != nil {
		t.Fatal(err)
	}
	// Moderation: a report with a reason, AutoMod rules, a custom emoji and
	// a timeout reason.
	if err := report(alice, am.Id-1, "report about a rude iguana"); err != nil {
		t.Fatal(err)
	}
	admin(&wire.AdminAction{Op: &wire.AdminAction_SaveAutomodRule{SaveAutomodRule: &wire.SaveAutoModRule{Rule: &wire.AutoModRule{
		Name: "Wolverine filter", Enabled: true, Trigger: wire.AutoModTrigger_AUTOMOD_TRIGGER_KEYWORD,
		Keywords: []string{"wolverine"}, Patterns: []string{"chinchilla+"}, Block: true,
	}}}})
	admin(&wire.AdminAction{Op: &wire.AdminAction_SaveAutomodRule{SaveAutomodRule: &wire.SaveAutoModRule{Rule: &wire.AutoModRule{
		Name: "Link rule", Enabled: true, Trigger: wire.AutoModTrigger_AUTOMOD_TRIGGER_LINKS, AllowedDomains: []string{"marten.example"}, Alert: true,
	}}}})
	if _, err := uploadEmoji(e.owner, e.id(), "narwhal_wave", pngOf(t, 32, 32, "emoji-ocelot-pixels")); err != nil {
		t.Fatal(err)
	}
	admin(&wire.AdminAction{Op: &wire.AdminAction_Timeout{Timeout: &wire.TimeoutMember{Member: alice.Identity.Public, Seconds: 60, Reason: "timeout for badger talk"}}})
	admin(&wire.AdminAction{Op: &wire.AdminAction_Timeout{Timeout: &wire.TimeoutMember{Member: alice.Identity.Public}}})
	if err := alice.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	var heronKey []byte
	for _, c := range alice.State.Channels {
		if c.Id == heron.Id {
			heronKey = c.Key
		}
	}
	if err := e.owner.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	strs := []string{
		"Zephyr Okapi Society", "blue axolotl", "Owner", "Alice Marmot", "Mallory Narwhal", "Pelican Wranglers",
		"heron-lounge", "Walrus invite label", "Gannet public label", "posting ibex spam",
		"secret capybara", "quokkas", "flamingo", "dugong", "tapir", "jackalope", "gecko",
		"avatar-lemur", "quetzal", "pangolin", "tapir", private.Secret, public.Secret,
		"Cormorant Category", "kestrel-den", "osprey topic text",
		"rude iguana", "Wolverine filter", "wolverine", "chinchilla", "marten.example", "narwhal_wave",
		"emoji-ocelot-pixels", "badger talk",
	}
	out := &workloadResult{Alice: alice, Heron: heron.Id}
	for _, s := range strs {
		out.Strings = append(out.Strings, []byte(s))
	}
	out.Strings = append(out.Strings,
		e.owner.Identity.Public, alice.Identity.Public, mallory.Identity.Public, e.ownerK.Public,
		alice.Static.Public, e.owner.Static.Public,
		e.owner.Channel("general").Key, heronKey,
	)
	return out
}
