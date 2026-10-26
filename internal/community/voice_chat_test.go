package community_test

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// A voice channel's text chat (docs/protocol.md §4.3): members post, read
// and react in a voice channel with the same rules as a text channel, and
// its messages go out on the voice channel's own topic under its key.
func TestVoiceChannelTextChat(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	waitAttached(t, e)
	vc := voiceChannel(e, "lounge")
	ch := syncState(t, alice)
	var lounge *wire.Channel
	for _, c := range ch.Channels {
		if c.Id == vc {
			lounge = c
		}
	}
	if lounge == nil || len(lounge.Key) == 0 || len(lounge.Topic) == 0 {
		t.Fatalf("the voice channel has no key or topic for its chat: %v", lounge)
	}
	if err := alice.Watch(ctx, lounge, e.serverKey()); err != nil {
		t.Fatal(err)
	}

	// Posting, live delivery and history.
	m := post(t, e.owner, vc, "who's up for a game?")
	if got := alice.WaitReceived(vc, 1, 5*time.Second); len(got) != 1 || got[0].Text != "who's up for a game?" {
		t.Fatalf("live message in the voice channel: %v", got)
	}
	h, err := call(t, alice, &wire.History{ChannelId: vc})
	if err != nil {
		t.Fatal(err)
	}
	if msgs := userMessages(h.GetMessages().Messages); len(msgs) != 1 || msgs[0].Id != m.Id {
		t.Fatalf("voice channel history: %v", msgs)
	}
	// Replies, reactions, typing and staged attachments work there too.
	if _, err := call(t, alice, &wire.Post{ChannelId: vc, Text: "me!", ReplyTo: m.Id}); err != nil {
		t.Fatalf("reply in a voice channel: %v", err)
	}
	if _, err := call(t, alice, &wire.React{MessageId: m.Id, Emoji: "🎲"}); err != nil {
		t.Fatalf("reaction in a voice channel: %v", err)
	}
	if _, err := call(t, alice, &wire.Typing{ChannelId: vc}); err != nil {
		t.Fatalf("typing in a voice channel: %v", err)
	}
	f, err := stage(t, alice, vc, "map.png", "image/png", pngBytes)
	if err != nil {
		t.Fatalf("staging a file in a voice channel: %v", err)
	}
	if _, err := call(t, alice, &wire.Post{ChannelId: vc, FileIds: []int64{f.FileId}}); err != nil {
		t.Fatalf("posting a file in a voice channel: %v", err)
	}
	// A reply can't point at a message in another channel.
	gen := e.owner.Channel("general").Id
	other := post(t, e.owner, gen, "in general")
	if _, err := call(t, alice, &wire.Post{ChannelId: vc, Text: "x", ReplyTo: other.Id}); err == nil {
		t.Fatal("a reply in the voice channel to a message in #general was accepted")
	}
	// Reports and search find voice channel messages like any other.
	if _, err := alice.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_ReportMessage{ReportMessage: &wire.ReportMessage{MessageId: m.Id, Reason: "spam"}}}); err != nil {
		t.Fatalf("reporting a voice channel message: %v", err)
	}
	r, err := e.owner.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_Search{Search: &wire.SearchQuery{Query: "game"}}})
	if err != nil || len(r.GetSearchResults().Messages) != 1 || r.GetSearchResults().Messages[0].ChannelId != vc {
		t.Fatalf("search for a voice channel message: %v %v", r, err)
	}
	// Moderators clean up with bulk delete.
	if _, err := e.act(e.owner, &wire.AdminAction{Op: &wire.AdminAction_BulkDelete{BulkDelete: &wire.BulkDelete{ChannelId: vc, Last: 10}}}); err != nil {
		t.Fatalf("bulk delete in a voice channel: %v", err)
	}
	h, _ = call(t, alice, &wire.History{ChannelId: vc})
	for _, m := range userMessages(h.GetMessages().Messages) {
		if !m.Deleted || m.Text != "" {
			t.Fatalf("a message left after the bulk delete: %v", m)
		}
	}
}

// The voice channel's chat follows the same permissions as a text channel:
// Send Messages to post (and type), Read Message History for history,
// View Channel for anything; the history gate for new members; slowmode;
// the channel's attachment setting; and AutoMod.
func TestVoiceChannelChatPermissions(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	waitAttached(t, e)
	vc := voiceChannel(e, "lounge")
	old := post(t, e.owner, vc, "from before")

	// Without Send Messages: no posting, typing or uploading. Connect is
	// unaffected (the call is a separate permission).
	e.must(override(&wire.SetChannelOverride{ChannelId: vc, Member: alice.Identity.Public, Deny: community.PermSendMessages}))
	_, err := alice.Post(ctx, vc, "hello")
	permDenied(t, "posting in a voice channel without Send Messages", err)
	_, err = call(t, alice, &wire.Typing{ChannelId: vc})
	permDenied(t, "typing in a voice channel without Send Messages", err)
	_, err = stage(t, alice, vc, "a.txt", "text/plain", []byte("a"))
	permDenied(t, "uploading in a voice channel without Send Messages", err)
	if _, err := voiceJoin(alice, vc); err != nil {
		t.Fatalf("joining the call without Send Messages: %v", err)
	}

	// Without Read Message History: no history.
	e.must(override(&wire.SetChannelOverride{ChannelId: vc, Member: alice.Identity.Public, Deny: community.PermReadMessageHistory}))
	_, err = call(t, alice, &wire.History{ChannelId: vc})
	permDenied(t, "voice channel history without Read Message History", err)

	// Without View Channel: nothing at all, and the channel isn't listed.
	e.must(override(&wire.SetChannelOverride{ChannelId: vc, Member: alice.Identity.Public, Deny: community.PermViewChannel}))
	if _, err := alice.Post(ctx, vc, "hello"); err == nil {
		t.Fatal("posted in a voice channel the member can't see")
	}
	for _, c := range syncState(t, alice).Channels {
		if c.Id == vc {
			t.Fatal("a hidden voice channel is in the member's state")
		}
	}
	e.must(override(&wire.SetChannelOverride{ChannelId: vc, Member: alice.Identity.Public}))

	// The history gate: with new members not seeing history, a member who
	// joins later doesn't get the earlier voice channel messages.
	now := time.Now()
	e.comm.Server.SetClock(func() time.Time { return now })
	e.update(t, func(s *wire.CommunitySettings) { s.NewMembersSeeHistory = proto.Bool(false) })
	now = now.Add(time.Second)
	late := e.member("Late", e.invite.Secret)
	now = now.Add(time.Second)
	newer := post(t, e.owner, vc, "after you joined")
	h, err := call(t, late, &wire.History{ChannelId: vc})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range h.GetMessages().Messages {
		if m.Id == old.Id || strings.Contains(m.Text, "from before") {
			t.Fatal("the history gate let a new member read the voice channel's earlier messages")
		}
	}
	if msgs := userMessages(h.GetMessages().Messages); len(msgs) != 1 || msgs[0].Id != newer.Id {
		t.Fatalf("new member's voice channel history: %v", msgs)
	}

	// Slowmode on the voice channel.
	e.must(editChannel(&wire.EditChannel{Id: vc, Name: "lounge", SlowmodeSeconds: ptr(uint32(60))}))
	if _, err := alice.Post(ctx, vc, "one"); err != nil {
		t.Fatal(err)
	}
	_, err = alice.Post(ctx, vc, "two")
	wantCode(t, "second post in a voice channel under slowmode", err, wire.RpcErrorCode_RPC_ERROR_CODE_RATE_LIMITED)
	now = now.Add(61 * time.Second)
	e.must(editChannel(&wire.EditChannel{Id: vc, Name: "lounge", SlowmodeSeconds: ptr(uint32(0))}))

	// The channel's attachments setting.
	e.channelFeatures(t, vc, chOff{attachments: true})
	_, err = stage(t, alice, vc, "a.png", "image/png", pngBytes)
	wantCode(t, "a file in a voice channel with attachments off", err, codeDisabled)
	e.channelFeatures(t, vc, chOff{})

	// AutoMod applies to the voice channel's chat.
	if _, err := saveRule(e, &wire.AutoModRule{Name: "Words", Enabled: true, Trigger: wire.AutoModTrigger_AUTOMOD_TRIGGER_KEYWORD,
		Keywords: []string{"badword"}, Block: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.Post(ctx, vc, "a badword here"); err == nil || !strings.Contains(err.Error(), "AutoMod") {
		t.Fatalf("AutoMod in a voice channel: %v", err)
	}
}

// With voice channels turned off for the community, their chat is turned
// off too (DISABLED, like joining); history stays readable, and moderators
// can still clean up.
func TestVoiceChannelChatFollowsVoiceToggle(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	waitAttached(t, e)
	vc := voiceChannel(e, "lounge")
	post(t, alice, vc, "before")
	e.update(t, func(s *wire.CommunitySettings) { s.VoiceEnabled = proto.Bool(false) })
	_, err := alice.Post(ctx, vc, "after")
	disabled(t, "posting in a voice channel with voice off", err)
	_, err = e.owner.Post(ctx, vc, "after")
	disabled(t, "the owner posting in a voice channel with voice off", err)
	_, err = call(t, alice, &wire.Typing{ChannelId: vc})
	disabled(t, "typing in a voice channel with voice off", err)
	_, err = stage(t, alice, vc, "a.txt", "text/plain", []byte("a"))
	disabled(t, "uploading in a voice channel with voice off", err)
	h, err := call(t, alice, &wire.History{ChannelId: vc})
	if err != nil || len(userMessages(h.GetMessages().Messages)) != 1 {
		t.Fatalf("voice channel history with voice off: %v %v", h, err)
	}
	if _, err := e.act(e.owner, &wire.AdminAction{Op: &wire.AdminAction_BulkDelete{BulkDelete: &wire.BulkDelete{ChannelId: vc, Last: 10}}}); err != nil {
		t.Fatalf("bulk delete in a voice channel with voice off: %v", err)
	}
	e.update(t, func(s *wire.CommunitySettings) { s.VoiceEnabled = proto.Bool(true) })
	if _, err := alice.Post(ctx, vc, "back"); err != nil {
		t.Fatalf("voice back on: %v", err)
	}
}

// Messages in a voice channel follow its retention like a text channel's.
func TestVoiceChannelChatRetention(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	waitAttached(t, e)
	vc := voiceChannel(e, "lounge")
	base := time.Now()
	clock := base
	e.comm.Server.SetClock(func() time.Time { return clock })
	one := uint32(1)
	e.must(editChannel(&wire.EditChannel{Id: vc, Name: "lounge", MessageRetentionDays: &one}))
	post(t, e.owner, vc, "short-lived")
	clock = base.Add(time.Minute)
	if m, _, _ := e.comm.Server.Purge(ctx, base.Add(24*time.Hour-time.Millisecond)); m != 0 {
		t.Fatalf("purged %d voice channel messages early", m)
	}
	if m, _, _ := e.comm.Server.Purge(ctx, base.Add(24*time.Hour)); m != 1 {
		t.Fatalf("purged %d voice channel messages at 1 day, want 1", m)
	}
}
