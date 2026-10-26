package community_test

import (
	"bytes"
	"context"
	"slices"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/callauth"
	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

func auditHas(t *testing.T, e *env, want string) {
	t.Helper()
	audit, err := e.comm.Server.AuditSummaries(testutil.Ctx(t))
	if err != nil || !slices.Contains(audit, want) {
		t.Fatalf("audit lacks %q: %q %v", want, audit, err)
	}
}

func voiceJoin(m *testutil.Member, id int64) (*wire.VoiceSession, error) {
	r, err := m.Call(context.Background(), &wire.ClientMessage{Body: &wire.ClientMessage_VoiceJoin{VoiceJoin: &wire.VoiceJoin{ChannelId: id}}})
	return r.GetVoiceSession(), err
}

func waitAttached(t *testing.T, e *env) {
	t.Helper()
	waitFor(t, "relay attached", func() bool { return len(e.comm.Server.AttachedRelays()) > 0 })
}

// Every toggle has its documented default, and a settings value that
// predates a toggle reads as that default.
func TestToggleDefaults(t *testing.T) {
	e := setup(t, nil)
	s := syncState(t, e.owner).Settings
	on := []*bool{s.AttachmentsEnabled, s.ReactionsEnabled, s.CustomEmojiEnabled, s.VoiceEnabled, s.VideoEnabled,
		s.ScreenShareEnabled, s.EditOwnEnabled, s.DeleteOwnEnabled, s.TypingEnabled, s.NewMembersSeeHistory, s.JoinLeaveMessages}
	for i, v := range on {
		if v == nil || !*v {
			t.Errorf("toggle %d isn't on by default", i)
		}
	}
	if s.LinkPreviewsEnabled == nil || *s.LinkPreviewsEnabled {
		t.Error("link previews aren't off by default")
	}
	if !s.SearchEnabled || !s.AllowContactCards || s.AttachmentKinds != wire.AttachmentKinds_ATTACHMENT_KINDS_ALL || s.MaxFileSize != 10<<20 {
		t.Errorf("other defaults: %v", s)
	}
	def := community.DefaultSettings()
	if def.VoiceEnabled == nil || !*def.VoiceEnabled {
		t.Error("DefaultSettings lacks the toggles")
	}
	bad := proto.Clone(s).(*wire.CommunitySettings)
	bad.AttachmentKinds = 9
	if _, err := e.act(e.owner, &wire.AdminAction{Op: &wire.AdminAction_UpdateSettings{UpdateSettings: bad}}); err == nil {
		t.Fatal("unknown attachment kinds accepted")
	}
}

// Voice channels off: creating and joining voice channels is refused as
// DISABLED (not denied), and the change is audited.
func TestToggleVoiceOff(t *testing.T) {
	e := setup(t, nil)
	alice := e.member("Alice", e.invite.Secret)
	waitAttached(t, e)
	vc := e.must(&wire.AdminAction{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: "talk", Kind: wire.ChannelKind_CHANNEL_KIND_VOICE}}}).Id
	if _, err := voiceJoin(alice, vc); err != nil {
		t.Fatalf("voice on: %v", err)
	}
	e.update(t, func(s *wire.CommunitySettings) { s.VoiceEnabled = proto.Bool(false) })
	auditHas(t, e, "changed turned voice channels off")
	_, err := voiceJoin(alice, vc)
	disabled(t, "joining a voice channel", err)
	_, err = voiceJoin(e.owner, vc)
	disabled(t, "the owner joining a voice channel", err)
	_, err = e.act(e.owner, &wire.AdminAction{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: "more", Kind: wire.ChannelKind_CHANNEL_KIND_VOICE}}})
	disabled(t, "creating a voice channel", err)
	// Text channels are unaffected; turning it back on works.
	if _, err := e.act(e.owner, &wire.AdminAction{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: "text"}}}); err != nil {
		t.Fatal(err)
	}
	e.update(t, func(s *wire.CommunitySettings) { s.VoiceEnabled = proto.Bool(true) })
	if _, err := voiceJoin(alice, vc); err != nil {
		t.Fatalf("voice back on: %v", err)
	}
}

// Connect and Speak are enforced: without Connect a member can't join;
// without Speak they join listen-only: their grant lets them send no audio,
// and everyone sees them as listening only.
func TestVoiceConnectAndSpeak(t *testing.T) {
	e := setup(t, nil)
	alice := e.member("Alice", e.invite.Secret)
	bob := e.member("Bob", e.invite.Secret)
	waitAttached(t, e)
	vc := e.must(&wire.AdminAction{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: "talk", Kind: wire.ChannelKind_CHANNEL_KIND_VOICE}}}).Id
	e.must(override(&wire.SetChannelOverride{ChannelId: vc, Member: bob.Identity.Public, Deny: community.PermSpeak}))
	cs := e.member("Carol", e.invite.Secret)
	e.must(override(&wire.SetChannelOverride{ChannelId: vc, Member: cs.Identity.Public, Deny: community.PermConnectVoice}))
	_, err := voiceJoin(cs, vc)
	permDenied(t, "joining without Connect", err)

	as, err := voiceJoin(alice, vc)
	if err != nil || as.ListenOnly || !grantOf(t, as).Audio {
		t.Fatalf("alice: %v %v", as, err)
	}
	bs, err := voiceJoin(bob, vc)
	if err != nil {
		t.Fatal(err)
	}
	if !bs.ListenOnly || grantOf(t, bs).Audio || len(bs.CallKey) == 0 {
		t.Fatalf("bob without Speak: %v", bs)
	}
	for _, p := range bs.Participants {
		if bytes.Equal(p.Member, bob.Identity.Public) != p.ListenOnly {
			t.Fatalf("participant entry: %v", p)
		}
	}
}

func grantOf(t *testing.T, vs *wire.VoiceSession) *wire.CallGrant {
	t.Helper()
	g, err := callauth.VerifyGrant(vs.Grant, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// Search off: refused as DISABLED.
func TestToggleSearchOff(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	gen := e.owner.Channel("general").Id
	if _, err := e.owner.Post(ctx, gen, "findable walrus"); err != nil {
		t.Fatal(err)
	}
	search := func() (*wire.ServerMessage, error) {
		return e.owner.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_Search{Search: &wire.SearchQuery{Query: "walrus"}}})
	}
	waitFor(t, "indexed", func() bool { r, err := search(); return err == nil && len(r.GetSearchResults().Messages) == 1 })
	e.update(t, func(s *wire.CommunitySettings) { s.SearchEnabled = false })
	auditHas(t, e, "changed turned search off")
	_, err := search()
	disabled(t, "search", err)
}

// Contact cards off: publishing one is refused as DISABLED.
func TestToggleContactCardsOff(t *testing.T) {
	e := setup(t, nil)
	e.update(t, func(s *wire.CommunitySettings) { s.AllowContactCards = false })
	auditHas(t, e, "changed turned member contact cards off")
	_, err := e.owner.Call(testutil.Ctx(t), &wire.ClientMessage{Body: &wire.ClientMessage_PublishContactCard{PublishContactCard: &wire.PublishContactCard{Card: []byte("card")}}})
	disabled(t, "publishing a contact card", err)
}

// Video and screen share toggles are stored, sent to members and audited
// (voice_toggles_test.go covers calls).
func TestToggleVideoAndScreenShareStored(t *testing.T) {
	e := setup(t, nil)
	alice := e.member("Alice", e.invite.Secret)
	e.update(t, func(s *wire.CommunitySettings) {
		s.VideoEnabled, s.ScreenShareEnabled = proto.Bool(false), proto.Bool(false)
	})
	auditHas(t, e, "changed turned video off, turned screen share off")
	s := syncState(t, alice).Settings
	if s.GetVideoEnabled() || s.GetScreenShareEnabled() || !s.GetVoiceEnabled() {
		t.Fatalf("settings: %v", s)
	}
	e.comm.Restart(t, e.relay.URL)
	if got := e.comm.Server.Snapshot().Settings; got.GetVideoEnabled() || got.GetScreenShareEnabled() {
		t.Fatalf("after restart: %v", got)
	}
}

func history(t *testing.T, m *testutil.Member, ch int64) ([]string, error) {
	t.Helper()
	r, err := m.Call(testutil.Ctx(t), &wire.ClientMessage{Body: &wire.ClientMessage_History{History: &wire.History{ChannelId: ch, Limit: 50}}})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, msg := range r.GetMessages().Messages {
		if msg.System != nil {
			continue // join and leave messages
		}
		out = append(out, msg.Text)
	}
	return out, nil
}

// New members see history off (community or channel): history and search
// return nothing from before the member joined; owners still see all.
func TestNewMembersSeeHistory(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	gen := e.owner.Channel("general").Id
	other := e.must(&wire.AdminAction{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: "other"}}}).Id
	start := time.Now()
	now := start
	e.comm.Server.SetClock(func() time.Time { return now })
	for _, c := range []int64{gen, other} {
		if _, err := e.owner.Post(ctx, c, "before walrus"); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(time.Second)
	early := e.member("Early", e.invite.Secret)
	if got, _ := history(t, early, gen); len(got) != 1 {
		t.Fatalf("history on: %q", got)
	}
	// Channel level: off in #other only.
	e.must(editChannel(&wire.EditChannel{Id: other, Name: "other", Position: 1, HistoryForNewMembers: ptr(wire.ChannelToggle_CHANNEL_TOGGLE_OFF)}))
	auditHas(t, e, "edited #other: new members see history off in this channel")
	if got, _ := history(t, early, other); len(got) != 0 {
		t.Fatalf("channel off: %q", got)
	}
	if got, _ := history(t, early, gen); len(got) != 1 {
		t.Fatalf("other channel unaffected: %q", got)
	}
	// Community level: off everywhere.
	e.update(t, func(s *wire.CommunitySettings) { s.NewMembersSeeHistory = proto.Bool(false) })
	auditHas(t, e, "changed turned new members see history off")
	now = now.Add(time.Second)
	late := e.member("Late", e.invite.Secret)
	now = now.Add(time.Second)
	if _, err := late.Post(ctx, gen, "after walrus"); err != nil {
		t.Fatal(err)
	}
	if got, _ := history(t, late, gen); !slices.Equal(got, []string{"after walrus"}) {
		t.Fatalf("community off: %q", got)
	}
	if got, _ := history(t, e.owner, gen); len(got) != 2 {
		t.Fatalf("owner: %q", got)
	}
	r, err := late.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_Search{Search: &wire.SearchQuery{Query: "walrus"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range r.GetSearchResults().Messages {
		if m.Text == "before walrus" {
			t.Fatal("search returned a message from before the member joined")
		}
	}
	// Read Message History denied: no history at all.
	e.must(override(&wire.SetChannelOverride{ChannelId: gen, Member: late.Identity.Public, Deny: community.PermReadMessageHistory}))
	_, err = history(t, late, gen)
	permDenied(t, "history without Read Message History", err)
}

// Channel-level toggles are validated and stored, and the channel state
// carries them.
func TestChannelTogglesStored(t *testing.T) {
	e := setup(t, nil)
	gen := e.owner.Channel("general").Id
	off := wire.ChannelToggle_CHANNEL_TOGGLE_OFF
	e.must(editChannel(&wire.EditChannel{Id: gen, Name: "general", Attachments: &off, LinkPreviews: &off, Reactions: &off}))
	auditHas(t, e, "edited #general: attachments off in this channel, link previews off in this channel, reactions off in this channel")
	c := syncState(t, e.owner).Channels[0]
	if c.Attachments != off || c.LinkPreviews != off || c.Reactions != off {
		t.Fatalf("channel toggles: %v", c)
	}
	bad := wire.ChannelToggle(7)
	if _, err := e.act(e.owner, editChannel(&wire.EditChannel{Id: gen, Name: "general", Reactions: &bad})); err == nil {
		t.Fatal("unknown channel toggle accepted")
	}
	alice := e.member("Alice", e.invite.Secret)
	if _, err := e.act(alice, editChannel(&wire.EditChannel{Id: gen, Name: "general", Reactions: ptr(wire.ChannelToggle_CHANNEL_TOGGLE_INHERIT)})); err == nil {
		t.Fatal("a member changed a channel toggle")
	}
}

// Change Nickname gates renaming yourself.
func TestChangeNicknamePermission(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	rename := func(n string) error {
		_, err := alice.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_SetProfile{SetProfile: &wire.SetProfile{DisplayName: n}}})
		return err
	}
	if err := rename("Alicia"); err != nil {
		t.Fatal(err)
	}
	ev := e.everyoneID()
	e.must(&wire.AdminAction{Op: &wire.AdminAction_EditRole{EditRole: &wire.EditRole{Id: ev, Permissions: community.DefaultEveryonePerms &^ community.PermChangeNickname}}})
	permDenied(t, "renaming without Change Nickname", rename("Al"))
}
