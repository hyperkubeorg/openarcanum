package community_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// ---------------------------------------------------------------- helpers

// rpcCode is the error code of a failed call (UNSPECIFIED when it worked).
func rpcCode(r *wire.ServerMessage, err error) wire.RpcErrorCode {
	if err == nil {
		return wire.RpcErrorCode_RPC_ERROR_CODE_UNSPECIFIED
	}
	if r != nil && r.GetError() != nil {
		return r.GetError().Code
	}
	return wire.RpcErrorCode_RPC_ERROR_CODE_INTERNAL
}

const (
	codeDisabled = wire.RpcErrorCode_RPC_ERROR_CODE_DISABLED
	codeDenied   = wire.RpcErrorCode_RPC_ERROR_CODE_PERMISSION_DENIED
)

func call(t *testing.T, m *testutil.Member, body any) (*wire.ServerMessage, error) {
	t.Helper()
	msg := &wire.ClientMessage{}
	switch b := body.(type) {
	case *wire.React:
		msg.Body = &wire.ClientMessage_React{React: b}
	case *wire.Pin:
		msg.Body = &wire.ClientMessage_Pin{Pin: b}
	case *wire.ListPins:
		msg.Body = &wire.ClientMessage_ListPins{ListPins: b}
	case *wire.Typing:
		msg.Body = &wire.ClientMessage_Typing{Typing: b}
	case *wire.UpdateProfile:
		msg.Body = &wire.ClientMessage_UpdateProfile{UpdateProfile: b}
	case *wire.Post:
		msg.Body = &wire.ClientMessage_Post{Post: b}
	case *wire.Edit:
		msg.Body = &wire.ClientMessage_Edit{Edit: b}
	case *wire.DeleteMessage:
		msg.Body = &wire.ClientMessage_Delete{Delete: b}
	case *wire.History:
		msg.Body = &wire.ClientMessage_History{History: b}
	case *wire.ListMembers:
		msg.Body = &wire.ClientMessage_ListMembers{ListMembers: b}
	default:
		t.Fatalf("call: unsupported %T", body)
	}
	return m.Call(testutil.Ctx(t), msg)
}

// messaging changes the community's settings (the feature toggles).
func (e *env) messaging(t *testing.T, mut func(*wire.CommunitySettings)) {
	t.Helper()
	e.update(t, mut)
}

func (e *env) admin(t *testing.T, a *wire.AdminAction) {
	t.Helper()
	if _, err := e.owner.Admin(testutil.Ctx(t), e.id(), a); err != nil {
		t.Fatal(err)
	}
}

// chOff lists the channel-level feature settings to turn off; the others
// inherit the community toggle.
type chOff struct{ attachments, previews, reactions bool }

func toggle(off bool) *wire.ChannelToggle {
	if off {
		return wire.ChannelToggle_CHANNEL_TOGGLE_OFF.Enum()
	}
	return wire.ChannelToggle_CHANNEL_TOGGLE_INHERIT.Enum()
}

// channelFeatureEdit is the EditChannel that sets a channel's attachments,
// link previews and reactions settings (keeping its position).
func (e *env) channelFeatureEdit(t *testing.T, id int64, f chOff) *wire.AdminAction {
	t.Helper()
	if err := e.owner.Sync(testutil.Ctx(t)); err != nil {
		t.Fatal(err)
	}
	var pos int32
	for _, c := range e.owner.State.Channels {
		if c.Id == id {
			pos = c.Position
		}
	}
	return &wire.AdminAction{Op: &wire.AdminAction_EditChannel{EditChannel: &wire.EditChannel{
		Id: id, Position: pos, Attachments: toggle(f.attachments), LinkPreviews: toggle(f.previews), Reactions: toggle(f.reactions),
	}}}
}

func (e *env) channelFeatures(t *testing.T, id int64, f chOff) {
	t.Helper()
	e.admin(t, e.channelFeatureEdit(t, id, f))
}

// everyonePerms changes @everyone's permissions.
func (e *env) everyonePerms(t *testing.T, mut func(uint64) uint64) {
	t.Helper()
	ctx := testutil.Ctx(t)
	if err := e.owner.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	for _, r := range e.owner.State.Roles {
		if r.Builtin && r.Position == 0 {
			e.admin(t, &wire.AdminAction{Op: &wire.AdminAction_EditRole{EditRole: &wire.EditRole{Id: r.Id, Name: r.Name, Permissions: mut(r.Permissions)}}})
			return
		}
	}
	t.Fatal("no @everyone")
}

func (e *env) auditHas(t *testing.T, want string) {
	t.Helper()
	got, err := e.comm.Server.AuditSummaries(testutil.Ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range got {
		if strings.Contains(s, want) {
			return
		}
	}
	t.Fatalf("no audit entry with %q in %q", want, got)
}

func post(t *testing.T, m *testutil.Member, channelID int64, text string) *wire.Message {
	t.Helper()
	msg, err := m.Post(testutil.Ctx(t), channelID, text)
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

func stage(t *testing.T, m *testutil.Member, channelID int64, name, mime string, data []byte) (*wire.FileRef, error) {
	t.Helper()
	ctx := testutil.Ctx(t)
	r, err := m.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_UploadBegin{UploadBegin: &wire.UploadBegin{
		ChannelId: channelID, Name: name, Size: uint64(len(data)), Mime: mime, Staged: true,
	}}})
	if err != nil {
		return nil, err
	}
	st := r.GetUploadStarted()
	if _, err := m.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_UploadChunk{UploadChunk: &wire.UploadChunk{UploadId: st.UploadId, Data: data}}}); err != nil {
		return nil, err
	}
	r, err = m.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_UploadFinish{UploadFinish: &wire.UploadFinish{UploadId: st.UploadId, Sha256: sum(data)}}})
	if err != nil {
		return nil, err
	}
	return r.GetStagedFile().File, nil
}

// pngBytes is a PNG header declaring a 1 × 1 canvas (images are checked by
// their header: format and size in pixels; image_bomb_test.go).
var pngBytes = pngHeader(1, 1)

// ---------------------------------------------------------------- reactions

// Replies: reply_to is kept and returned in history; a reply to a message
// that doesn't exist, or to one in another channel (one the member can't
// even see included), is refused and nothing is stored.
func TestReplies(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	gen := alice.Channel("general")
	other, err := e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: "staff"}}})
	if err != nil {
		t.Fatal(err)
	}
	e.admin(t, &wire.AdminAction{Op: &wire.AdminAction_SetChannelOverride{SetChannelOverride: &wire.SetChannelOverride{
		ChannelId: other.Id, RoleId: 1, Deny: community.PermViewChannel,
	}}})
	original := post(t, e.owner, gen.Id, "the original")
	hidden := post(t, e.owner, other.Id, "staff only")

	reply := func(channel, to int64, text string) (*wire.Message, error) {
		r, err := call(t, alice, &wire.Post{ChannelId: channel, Text: text, ReplyTo: to})
		if err != nil {
			return nil, err
		}
		return r.GetMessage(), nil
	}
	m, err := reply(gen.Id, original.Id, "a reply")
	if err != nil {
		t.Fatal(err)
	}
	if m.ReplyTo != original.Id {
		t.Fatalf("reply_to %d, want %d", m.ReplyTo, original.Id)
	}
	_, err = reply(gen.Id, original.Id+1000, "to nothing")
	wantCode(t, "reply to a missing message", err, wire.RpcErrorCode_RPC_ERROR_CODE_NOT_FOUND)
	_, err = reply(gen.Id, hidden.Id, "to another channel")
	wantCode(t, "reply to a message in another channel", err, wire.RpcErrorCode_RPC_ERROR_CODE_NOT_FOUND)
	// The owner can see both channels; the cross-channel rule still holds.
	_, err = call(t, e.owner, &wire.Post{ChannelId: gen.Id, Text: "cross", ReplyTo: hidden.Id})
	wantCode(t, "owner replying across channels", err, wire.RpcErrorCode_RPC_ERROR_CODE_NOT_FOUND)

	h, err := call(t, alice, &wire.History{ChannelId: gen.Id})
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, msg := range userMessages(h.GetMessages().Messages) {
		texts = append(texts, msg.Text)
		if msg.Id == m.Id && msg.ReplyTo != original.Id {
			t.Fatalf("reply_to in history: %d, want %d", msg.ReplyTo, original.Id)
		}
		if msg.Id == original.Id && msg.ReplyTo != 0 {
			t.Fatalf("the original has reply_to %d", msg.ReplyTo)
		}
	}
	if strings.Join(texts, "|") != "the original|a reply" {
		t.Fatalf("history after refused replies: %q", texts)
	}
}

func TestReactions(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	gen := alice.Channel("general")
	if err := e.owner.Watch(ctx, e.owner.Channel("general"), e.serverKey()); err != nil {
		t.Fatal(err)
	}
	m := post(t, e.owner, gen.Id, "react to me")
	r, err := call(t, alice, &wire.React{MessageId: m.Id, Emoji: "👍"})
	if err != nil {
		t.Fatal(err)
	}
	// The reply is the reactions only: never the message text.
	if u := r.GetMessageUpdate(); u == nil || u.MessageId != m.Id || u.GetReactions() == nil {
		t.Fatalf("react reply: %v", r)
	}
	if rx := r.GetMessageUpdate().GetReactions().Reactions; len(rx) != 1 || rx[0].Emoji != "👍" || len(rx[0].Members) != 1 || !bytes.Equal(rx[0].Members[0], alice.Identity.Public) {
		t.Fatalf("reactions: %v", rx)
	}
	// Reacting twice is one reaction; the owner adds theirs.
	call(t, alice, &wire.React{MessageId: m.Id, Emoji: "👍"})
	r, _ = call(t, e.owner, &wire.React{MessageId: m.Id, Emoji: "👍"})
	if rx := r.GetMessageUpdate().GetReactions().Reactions; len(rx) != 1 || len(rx[0].Members) != 2 {
		t.Fatalf("after two reactors: %v", rx)
	}
	// Members watching the channel get an `update` event with the reactions.
	if e.owner.WaitEvent(gen.Id, func(ev *wire.ChannelEvent) bool {
		u := ev.GetUpdate()
		rx := u.GetReactions().GetReactions()
		return u != nil && u.MessageId == m.Id && u.ChannelId == gen.Id && len(rx) == 1 && len(rx[0].Members) == 2
	}, 5*time.Second) == nil {
		t.Fatal("no updated event for the reaction")
	}
	// Custom emoji references to this community's emoji are accepted (an
	// unknown one isn't); text and junk aren't.
	em, err := uploadEmoji(e.owner, e.id(), "party_blob", pngOf(t, 32, 32, ""))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, alice, &wire.React{MessageId: m.Id, Emoji: ":party_blob:" + itoa(em.Id)}); err != nil {
		t.Fatalf("custom emoji: %v", err)
	}
	// Emoji sequences keep their joiners and variation selectors.
	for _, ok := range []string{"👨\u200d👩\u200d👧", "❤\ufe0f", "🏳\ufe0f\u200d🌈"} {
		if _, err := call(t, alice, &wire.React{MessageId: m.Id, Emoji: ok}); err != nil {
			t.Fatalf("emoji sequence %q refused: %v", ok, err)
		}
		call(t, alice, &wire.React{MessageId: m.Id, Emoji: ok, Remove: true})
	}
	if _, err := call(t, alice, &wire.React{MessageId: m.Id, Emoji: ":party_blob:12345"}); err == nil {
		t.Error("unknown custom emoji accepted")
	}
	for _, bad := range []string{"", "lol", "a b", ":x:1", ":name:0", strings.Repeat("😀", 33), "👍\n",
		"👍\u202e", "\u202d👍", "👍\u2066", "👍\u2069", "\u200f👍", "👍\u200e", "👍\u0085", "👍\u0007", "\u061c👍"} {
		if _, err := call(t, alice, &wire.React{MessageId: m.Id, Emoji: bad}); err == nil {
			t.Errorf("emoji %q accepted", bad)
		}
	}
	// Removing.
	r, _ = call(t, alice, &wire.React{MessageId: m.Id, Emoji: "👍", Remove: true})
	if rx := r.GetMessageUpdate().GetReactions().Reactions; len(rx) != 2 || len(rx[0].Members) != 1 {
		t.Fatalf("after removal: %v", rx)
	}
	// At most 20 different emoji.
	emoji := "😀😁😂🤣😃😄😅😆😉😊😋😎😍😘🥰😗😙😚🙂🤗"
	n := 2
	for _, em := range emoji {
		_, err := call(t, alice, &wire.React{MessageId: m.Id, Emoji: string(em)})
		if n < community.MaxReactionEmoji && err != nil {
			t.Fatalf("reaction %d refused: %v", n, err)
		}
		if n >= community.MaxReactionEmoji && err == nil {
			t.Fatalf("reaction %d accepted over the limit", n+1)
		}
		n++
	}
	// History carries the reactions.
	h, _ := call(t, alice, &wire.History{ChannelId: gen.Id})
	for _, hm := range h.GetMessages().Messages {
		if hm.Id == m.Id && len(hm.Reactions) != community.MaxReactionEmoji {
			t.Fatalf("history has %d reactions", len(hm.Reactions))
		}
	}
}

func TestReactionsGatedAndToggled(t *testing.T) {
	e := setup(t, nil)
	alice := e.member("Alice", e.invite.Secret)
	gen := alice.Channel("general")
	m := post(t, e.owner, gen.Id, "hi")
	// Add Reactions is an @everyone default; without it, members can't react.
	e.everyonePerms(t, func(p uint64) uint64 { return p &^ community.PermAddReactions })
	if c := rpcCode(call(t, alice, &wire.React{MessageId: m.Id, Emoji: "👍"})); c != codeDenied {
		t.Fatalf("react without permission: %v", c)
	}
	e.everyonePerms(t, func(p uint64) uint64 { return p | community.PermAddReactions })
	if _, err := call(t, alice, &wire.React{MessageId: m.Id, Emoji: "👍"}); err != nil {
		t.Fatal(err)
	}
	// Community toggle off: refused as turned off, for everyone (even owners).
	e.messaging(t, func(m *wire.CommunitySettings) { m.ReactionsEnabled = proto.Bool(false) })
	e.auditHas(t, "reactions off")
	for _, who := range []*testutil.Member{alice, e.owner} {
		if c := rpcCode(call(t, who, &wire.React{MessageId: m.Id, Emoji: "🎉"})); c != codeDisabled {
			t.Fatalf("react with reactions off: %v", c)
		}
	}
	e.messaging(t, func(m *wire.CommunitySettings) { m.ReactionsEnabled = proto.Bool(true) })
	// Channel setting off.
	e.channelFeatures(t, gen.Id, chOff{reactions: true})
	e.auditHas(t, "reactions off")
	if c := rpcCode(call(t, alice, &wire.React{MessageId: m.Id, Emoji: "🎉"})); c != codeDisabled {
		t.Fatalf("react in a channel with reactions off: %v", c)
	}
	e.channelFeatures(t, gen.Id, chOff{})
	if _, err := call(t, alice, &wire.React{MessageId: m.Id, Emoji: "🎉"}); err != nil {
		t.Fatal(err)
	}
	// Only Manage Channels may change channel features.
	if _, err := alice.Admin(testutil.Ctx(t), e.id(), e.channelFeatureEdit(t, gen.Id, chOff{reactions: true})); err == nil {
		t.Fatal("member changed channel features")
	}
	// The channel flags reach members in their state.
	e.channelFeatures(t, gen.Id, chOff{attachments: true, previews: true})
	alice.Sync(testutil.Ctx(t))
	if c := alice.Channel("general"); c.Attachments != wire.ChannelToggle_CHANNEL_TOGGLE_OFF || c.LinkPreviews != wire.ChannelToggle_CHANNEL_TOGGLE_OFF || c.Reactions != wire.ChannelToggle_CHANNEL_TOGGLE_INHERIT {
		t.Fatalf("channel flags in state: %v", c)
	}
}

// ---------------------------------------------------------------- pins

func TestPins(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	gen := alice.Channel("general")
	first := post(t, alice, gen.Id, "pin me first")
	second := post(t, alice, gen.Id, "pin me second")
	// Pinning needs Manage Messages.
	if c := rpcCode(call(t, alice, &wire.Pin{MessageId: first.Id})); c != codeDenied {
		t.Fatalf("member pinned: %v", c)
	}
	for _, m := range []*wire.Message{first, second} {
		r, err := call(t, e.owner, &wire.Pin{MessageId: m.Id})
		if u := r.GetMessageUpdate(); err != nil || u.GetMessageId() != m.Id || !u.GetPinned() {
			t.Fatalf("pin: %v %v", r, err)
		}
	}
	r, err := call(t, alice, &wire.ListPins{ChannelId: gen.Id})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.GetPins().Messages; len(got) != 2 || got[0].Text != "pin me second" || got[1].Text != "pin me first" {
		t.Fatalf("pins, newest first: %v", got)
	}
	h, _ := call(t, alice, &wire.History{ChannelId: gen.Id})
	pinned := 0
	for _, m := range h.GetMessages().Messages {
		if m.Pinned {
			pinned++
		}
	}
	if pinned != 2 {
		t.Fatalf("history shows %d pinned", pinned)
	}
	// Unpin, and a deleted message leaves the list.
	call(t, e.owner, &wire.Pin{MessageId: first.Id, Unpin: true})
	if _, err := call(t, alice, &wire.DeleteMessage{MessageId: second.Id}); err != nil {
		t.Fatal(err)
	}
	r, _ = call(t, alice, &wire.ListPins{ChannelId: gen.Id})
	if got := r.GetPins().Messages; len(got) != 0 {
		t.Fatalf("pins after unpin and delete: %v", got)
	}
	// Pins survive a restart.
	third := post(t, alice, gen.Id, "pinned across restarts")
	call(t, e.owner, &wire.Pin{MessageId: third.Id})
	e.owner.Close()
	alice.Close()
	e.comm.Restart(t, e.relay.URL)
	owner := testutil.NewMember(t, "Owner")
	owner.Identity, owner.Static = e.owner.Identity, e.owner.Static
	if err := owner.Connect(ctx, e.relay.URL, e.serverKey()); err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	r, _ = call(t, owner, &wire.ListPins{ChannelId: gen.Id})
	if got := r.GetPins().Messages; len(got) != 1 || !got[0].Pinned || got[0].Text != "pinned across restarts" {
		t.Fatalf("pins after restart: %v", got)
	}
}

func TestPinLimit(t *testing.T) {
	e := setup(t, nil)
	gen := e.owner.Channel("general")
	for i := 0; i < community.MaxPinsPerChannel; i++ {
		m := post(t, e.owner, gen.Id, fmt.Sprintf("m%d", i))
		if _, err := call(t, e.owner, &wire.Pin{MessageId: m.Id}); err != nil {
			t.Fatalf("pin %d: %v", i, err)
		}
	}
	m := post(t, e.owner, gen.Id, "one too many")
	_, err := call(t, e.owner, &wire.Pin{MessageId: m.Id})
	wantErr(t, err, fmt.Sprintf("a channel can have at most %d pinned messages", community.MaxPinsPerChannel))
}

// ---------------------------------------------------------------- mentions

func TestMentions(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	bob := e.member("Bob", e.invite.Secret)
	gen := alice.Channel("general")
	tok := func(m *testutil.Member) string { return "<@" + identity.Encode(m.Identity.Public) + ">" }

	m := post(t, alice, gen.Id, "hey "+tok(bob)+" and "+tok(bob)+" and `"+tok(e.owner)+"`")
	if len(m.Mentions) != 1 || !bytes.Equal(m.Mentions[0], bob.Identity.Public) {
		t.Fatalf("member mention (once, not in code): %v", m.Mentions)
	}
	// A member without the permission can write @everyone, @here and a role,
	// but nobody is notified.
	rr, err := e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_CreateRole{CreateRole: &wire.CreateRole{Name: "Mods"}}})
	if err != nil {
		t.Fatal(err)
	}
	text := fmt.Sprintf("@everyone @here @channel <@&%d> look", rr.Id)
	m = post(t, alice, gen.Id, text)
	if m.Text != text || m.MentionEveryone || m.MentionHere || m.MentionChannel || len(m.MentionRoles) != 0 {
		t.Fatalf("unpermitted mentions marked: %v", m)
	}
	// The owner (and anyone with Mention @everyone) can.
	m = post(t, e.owner, gen.Id, text)
	if !m.MentionEveryone || !m.MentionHere || !m.MentionChannel || len(m.MentionRoles) != 1 || m.MentionRoles[0] != rr.Id {
		t.Fatalf("owner mentions: %v", m)
	}
	// Grant it to @everyone: now Alice's mentions count.
	e.everyonePerms(t, func(p uint64) uint64 { return p | community.PermMentionEveryone })
	m = post(t, alice, gen.Id, "@everyone ping")
	if !m.MentionEveryone {
		t.Fatal("permitted @everyone not marked")
	}
	// A member who can't read the channel isn't mentioned there.
	res, err := e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: "staff"}}})
	if err != nil {
		t.Fatal(err)
	}
	e.admin(t, &wire.AdminAction{Op: &wire.AdminAction_SetChannelOverride{SetChannelOverride: &wire.SetChannelOverride{ChannelId: res.Id, RoleId: 1, Deny: community.PermViewChannel}}})
	m = post(t, e.owner, res.Id, "secret "+tok(bob))
	if len(m.Mentions) != 0 {
		t.Fatalf("mentioned a member who can't read the channel: %v", m.Mentions)
	}
	// Edits re-evaluate mentions.
	m = post(t, alice, gen.Id, "no one")
	r, err := call(t, alice, &wire.Edit{MessageId: m.Id, Text: "now " + tok(e.owner)})
	if err != nil || len(r.GetMessage().Mentions) != 1 {
		t.Fatalf("edit mentions: %v %v", r, err)
	}
}

// ---------------------------------------------------------------- typing

func TestTyping(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	gen := alice.Channel("general")
	if err := e.owner.Watch(ctx, e.owner.Channel("general"), e.serverKey()); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, alice, &wire.Typing{ChannelId: gen.Id}); err != nil {
		t.Fatal(err)
	}
	ev := e.owner.WaitEvent(gen.Id, func(ev *wire.ChannelEvent) bool { return ev.GetTyping() != nil }, 5*time.Second)
	if ev == nil || ev.GetTyping().Name != "Alice" || !bytes.Equal(ev.GetTyping().Member, alice.Identity.Public) {
		t.Fatalf("typing event: %v", ev)
	}
	// Rate limited per session and channel.
	if c := rpcCode(call(t, alice, &wire.Typing{ChannelId: gen.Id})); c != wire.RpcErrorCode_RPC_ERROR_CODE_RATE_LIMITED {
		t.Fatalf("second typing event: %v", c)
	}
	// Nothing is stored.
	h, _ := call(t, alice, &wire.History{ChannelId: gen.Id})
	if n := len(userMessages(h.GetMessages().Messages)); n != 0 {
		t.Fatalf("typing stored %d messages", n)
	}
	// Posting ends typing, so the next message can be announced at once.
	post(t, alice, gen.Id, "done typing")
	if _, err := call(t, alice, &wire.Typing{ChannelId: gen.Id}); err != nil {
		t.Fatalf("typing right after posting: %v", err)
	}
	// Turned off: refused.
	e.messaging(t, func(m *wire.CommunitySettings) { m.TypingEnabled = proto.Bool(false) })
	e.auditHas(t, "typing indicators off")
	time.Sleep(community.TypingInterval)
	if c := rpcCode(call(t, alice, &wire.Typing{ChannelId: gen.Id})); c != codeDisabled {
		t.Fatalf("typing with indicators off: %v", c)
	}
	// Without Send Messages, no typing either.
	e.messaging(t, func(m *wire.CommunitySettings) { m.TypingEnabled = proto.Bool(true) })
	e.everyonePerms(t, func(p uint64) uint64 { return p &^ community.PermSendMessages })
	if c := rpcCode(call(t, alice, &wire.Typing{ChannelId: gen.Id})); c != codeDenied {
		t.Fatalf("typing without send permission: %v", c)
	}
}

// ---------------------------------------------------------------- presence

func TestPresence(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	online := func(of []byte) bool {
		r, err := call(t, e.owner, &wire.ListMembers{})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range r.GetMembers().Members {
			if bytes.Equal(m.Key, of) {
				return m.Online
			}
		}
		t.Fatal("member not listed")
		return false
	}
	waitPresence := func(of []byte, want bool) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case ev := <-e.owner.Events:
				for _, p := range ev.GetPresenceBatch().GetUpdates() {
					if bytes.Equal(p.Member, of) && p.Online == want {
						return
					}
				}
			case <-deadline:
				t.Fatalf("no presence update (online=%v)", want)
			}
		}
	}
	if !online(e.owner.Identity.Public) {
		t.Fatal("owner not online")
	}
	alice := e.member("Alice", e.invite.Secret)
	waitPresence(alice.Identity.Public, true)
	if !online(alice.Identity.Public) {
		t.Fatal("alice not listed online")
	}
	// A second device doesn't announce again; closing one of two keeps her online.
	d2 := testutil.NewMember(t, "Alice")
	d2.Identity = alice.Identity
	if err := d2.Connect(ctx, e.relay.URL, e.serverKey()); err != nil {
		t.Fatal(err)
	}
	d2.Close()
	time.Sleep(300 * time.Millisecond)
	if !online(alice.Identity.Public) {
		t.Fatal("alice offline while one device is connected")
	}
	alice.Close()
	waitPresence(alice.Identity.Public, false)
	if online(alice.Identity.Public) {
		t.Fatal("alice still online after closing")
	}
}

// A burst of members coming and going reaches each online session as a few
// batches, one entry per member, not one message per change.
func TestPresenceBatched(t *testing.T) {
	e := setup(t, nil)
	var members []*testutil.Member
	for i := 0; i < 30; i++ {
		members = append(members, e.member(fmt.Sprintf("m%d", i), e.invite.Secret))
	}
	// Half leave again straight away.
	for _, m := range members[:15] {
		m.Close()
	}
	latest := map[string]bool{}
	messages := 0
	deadline := time.After(5 * time.Second)
	for len(latest) < 30 || countOnline(latest) != 15 {
		select {
		case ev := <-e.owner.Events:
			if ev.GetPresence() != nil {
				t.Fatal("an unbatched presence update")
			}
			b := ev.GetPresenceBatch()
			if b == nil {
				continue
			}
			messages++
			seen := map[string]bool{}
			for _, p := range b.Updates {
				if seen[string(p.Member)] {
					t.Fatal("a member twice in one batch")
				}
				seen[string(p.Member)] = true
				latest[string(p.Member)] = p.Online
			}
		case <-deadline:
			t.Fatalf("after %d batches: %d members, %d online", messages, len(latest), countOnline(latest))
		}
	}
	// 45 changes (30 joins, 15 leaves) in a handful of batches.
	if messages > 10 {
		t.Fatalf("%d presence messages for 45 changes", messages)
	}
	for _, m := range members[15:] {
		if !latest[string(m.Identity.Public)] {
			t.Fatal("a connected member shown offline")
		}
	}
}

func countOnline(m map[string]bool) int {
	n := 0
	for _, on := range m {
		if on {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------- nicknames and profiles

func TestNicknamesAndProfiles(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	bob := e.member("Bob", e.invite.Secret)
	gen := alice.Channel("general")
	nick := func(actor *testutil.Member, of *testutil.Member, n string) error {
		_, err := actor.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_SetNickname{SetNickname: &wire.SetNickname{Member: of.Identity.Public, Nickname: n}}})
		return err
	}
	// Own nickname (Change Nickname is an @everyone default).
	if err := nick(alice, alice, "Ally"); err != nil {
		t.Fatal(err)
	}
	e.auditHas(t, `changed their nickname to "Ally"`)
	if m := post(t, alice, gen.Id, "hi"); m.AuthorName != "Ally" {
		t.Fatalf("author name with a nickname: %q", m.AuthorName)
	}
	// Others' nicknames need Manage Nicknames.
	if err := nick(alice, bob, "Bobby"); err == nil {
		t.Fatal("member changed someone else's nickname")
	}
	if err := nick(e.owner, bob, "Robert"); err != nil {
		t.Fatal(err)
	}
	e.auditHas(t, `changed Bob's nickname to "Robert"`)
	// Without Change Nickname, not even your own.
	e.everyonePerms(t, func(p uint64) uint64 { return p &^ community.PermChangeNickname })
	if err := nick(bob, bob, "B"); err == nil {
		t.Fatal("nickname changed without Change Nickname")
	}
	if err := nick(e.owner, alice, strings.Repeat("x", community.MaxNicknameChars+1)); err == nil {
		t.Fatal("over-long nickname accepted")
	}
	// Profiles.
	if _, err := call(t, alice, &wire.UpdateProfile{About: "Bird watcher.\nTea drinker.", Pronouns: "she/her"}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []*wire.UpdateProfile{
		{About: strings.Repeat("a", community.MaxAboutChars+1)},
		{Pronouns: strings.Repeat("p", community.MaxPronounsChars+1)},
		{Pronouns: "a\nb"},
	} {
		if _, err := call(t, alice, bad); err == nil {
			t.Errorf("bad profile accepted: %v", bad)
		}
	}
	r, _ := call(t, bob, &wire.ListMembers{})
	found := 0
	for _, m := range r.GetMembers().Members {
		switch {
		case bytes.Equal(m.Key, alice.Identity.Public):
			if m.Nickname != "Ally" || m.DisplayName != "Alice" || m.About != "Bird watcher.\nTea drinker." || m.Pronouns != "she/her" {
				t.Fatalf("alice in the member list: %v", m)
			}
			found++
		case bytes.Equal(m.Key, bob.Identity.Public):
			if m.Nickname != "Robert" {
				t.Fatalf("bob in the member list: %v", m)
			}
			found++
		}
	}
	if found != 2 {
		t.Fatal("members missing")
	}
}

// ---------------------------------------------------------------- attachments

func TestMultipleAttachments(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	gen := alice.Channel("general")
	var ids []int64
	for i := 0; i < 3; i++ {
		ref, err := stage(t, alice, gen.Id, fmt.Sprintf("f%d.txt", i), "text/plain", []byte(fmt.Sprintf("file number %d", i)))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, ref.FileId)
	}
	r, err := call(t, alice, &wire.Post{ChannelId: gen.Id, Text: "three files", FileIds: ids})
	if err != nil {
		t.Fatal(err)
	}
	m := r.GetMessage()
	if len(m.Files) != 3 {
		t.Fatalf("files: %v", m.Files)
	}
	for i, f := range m.Files {
		got, err := download(t, e.owner, f)
		if err != nil || string(got) != fmt.Sprintf("file number %d", i) || f.Name != fmt.Sprintf("f%d.txt", i) {
			t.Fatalf("file %d: %q %v", i, got, err)
		}
	}
	// A file posts once; someone else's staged file can't be taken.
	if _, err := call(t, alice, &wire.Post{ChannelId: gen.Id, FileIds: ids[:1]}); err == nil {
		t.Fatal("posted a file twice")
	}
	other, err := stage(t, e.owner, gen.Id, "owner.txt", "text/plain", []byte("owner's"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, alice, &wire.Post{ChannelId: gen.Id, FileIds: []int64{other.FileId}}); err == nil {
		t.Fatal("posted someone else's upload")
	}
	// At most 10 files.
	var many []int64
	for i := 0; i < community.MaxAttachments+1; i++ {
		ref, err := stage(t, alice, gen.Id, "x.txt", "text/plain", []byte("x"))
		if err != nil {
			t.Fatal(err)
		}
		many = append(many, ref.FileId)
	}
	if c := rpcCode(call(t, alice, &wire.Post{ChannelId: gen.Id, FileIds: many})); c != wire.RpcErrorCode_RPC_ERROR_CODE_TOO_LARGE {
		t.Fatalf("11 files: %v", c)
	}
	if r, err := call(t, alice, &wire.Post{ChannelId: gen.Id, FileIds: many[:community.MaxAttachments]}); err != nil || len(r.GetMessage().Files) != 10 {
		t.Fatalf("10 files: %v %v", r, err)
	}
	// Unposted uploads expire.
	n, _ := e.comm.Server.StagedCount(ctx)
	if n != 2 {
		t.Fatalf("staged files: %d", n)
	}
	if got, _ := e.comm.Server.ExpireStaged(ctx, time.Now()); got != 0 {
		t.Fatalf("fresh staged files expired: %d", got)
	}
	if got, _ := e.comm.Server.ExpireStaged(ctx, time.Now().Add(community.StagedFileTTL+time.Minute)); got != 2 {
		t.Fatalf("expired %d staged files", got)
	}
	if _, err := download(t, e.owner, other); err == nil {
		t.Fatal("expired staged file still downloadable")
	}
	// Deleting the message deletes its files.
	if _, err := call(t, alice, &wire.DeleteMessage{MessageId: m.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := download(t, e.owner, m.Files[0]); err == nil {
		t.Fatal("file of a deleted message still downloadable")
	}
}

func TestAttachmentToggles(t *testing.T) {
	e := setup(t, nil)
	alice := e.member("Alice", e.invite.Secret)
	gen := alice.Channel("general")
	staged, err := stage(t, alice, gen.Id, "early.txt", "text/plain", []byte("early"))
	if err != nil {
		t.Fatal(err)
	}
	// Community toggle off: uploads and posts with files are refused.
	e.messaging(t, func(m *wire.CommunitySettings) { m.AttachmentsEnabled = proto.Bool(false) })
	e.auditHas(t, "attachments off")
	if _, err := stage(t, alice, gen.Id, "a.txt", "text/plain", []byte("a")); !strings.Contains(fmt.Sprint(err), "DISABLED") {
		t.Fatalf("upload with attachments off: %v", err)
	}
	if c := rpcCode(call(t, alice, &wire.Post{ChannelId: gen.Id, FileIds: []int64{staged.FileId}})); c != codeDisabled {
		t.Fatalf("post with attachments off: %v", c)
	}
	if _, err := upload(t, alice, gen.Id, "single.bin", []byte("x")); !strings.Contains(fmt.Sprint(err), "DISABLED") {
		t.Fatalf("one-step upload with attachments off: %v", err)
	}
	e.messaging(t, func(m *wire.CommunitySettings) { m.AttachmentsEnabled = proto.Bool(true) })
	// Channel off.
	e.channelFeatures(t, gen.Id, chOff{attachments: true})
	if _, err := stage(t, alice, gen.Id, "a.txt", "text/plain", []byte("a")); !strings.Contains(fmt.Sprint(err), "DISABLED") {
		t.Fatalf("upload in a channel with attachments off: %v", err)
	}
	e.channelFeatures(t, gen.Id, chOff{})
	// Images only: a non-image is refused by type and by content.
	e.messaging(t, func(m *wire.CommunitySettings) { m.AttachmentKinds = wire.AttachmentKinds_ATTACHMENT_KINDS_IMAGES_ONLY })
	e.auditHas(t, "allowed attachments: images only")
	if _, err := stage(t, alice, gen.Id, "a.txt", "text/plain", []byte("a")); !strings.Contains(fmt.Sprint(err), "DISABLED") {
		t.Fatalf("text file with images only: %v", err)
	}
	if _, err := stage(t, alice, gen.Id, "fake.png", "image/png", []byte("not really a png")); !strings.Contains(fmt.Sprint(err), "DISABLED") {
		t.Fatalf("fake image with images only: %v", err)
	}
	if c := rpcCode(call(t, alice, &wire.Post{ChannelId: gen.Id, FileIds: []int64{staged.FileId}})); c != codeDisabled {
		t.Fatalf("staged text file posted with images only: %v", c)
	}
	img, err := stage(t, alice, gen.Id, "pic.png", "image/png", pngBytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, alice, &wire.Post{ChannelId: gen.Id, FileIds: []int64{img.FileId}}); err != nil {
		t.Fatal(err)
	}
	// The community's max file size applies per file.
	e.update(t, func(s *wire.CommunitySettings) { s.MaxFileSize = 1024 })
	if _, err := stage(t, alice, gen.Id, "big.png", "image/png", append(pngBytes, make([]byte, 2048)...)); !strings.Contains(fmt.Sprint(err), "TOO_LARGE") {
		t.Fatalf("over-size file: %v", err)
	}
	// Attach Files permission.
	e.everyonePerms(t, func(p uint64) uint64 { return p &^ community.PermAttachFiles })
	if _, err := stage(t, alice, gen.Id, "pic.png", "image/png", pngBytes); !strings.Contains(fmt.Sprint(err), "PERMISSION_DENIED") {
		t.Fatalf("upload without Attach Files: %v", err)
	}
}

// ---------------------------------------------------------------- edit/delete toggles

func TestEditAndDeleteOwnToggles(t *testing.T) {
	e := setup(t, nil)
	alice := e.member("Alice", e.invite.Secret)
	gen := alice.Channel("general")
	m := post(t, alice, gen.Id, "first draft")
	e.messaging(t, func(s *wire.CommunitySettings) { s.EditOwnEnabled = proto.Bool(false) })
	e.auditHas(t, "editing own messages off")
	if c := rpcCode(call(t, alice, &wire.Edit{MessageId: m.Id, Text: "second draft"})); c != codeDisabled {
		t.Fatalf("edit with editing off: %v", c)
	}
	e.messaging(t, func(s *wire.CommunitySettings) { s.EditOwnEnabled = proto.Bool(true) })
	if r, err := call(t, alice, &wire.Edit{MessageId: m.Id, Text: "second draft"}); err != nil || r.GetMessage().EditedAt == 0 {
		t.Fatalf("edit: %v %v", r, err)
	}
	e.messaging(t, func(s *wire.CommunitySettings) { s.DeleteOwnEnabled = proto.Bool(false) })
	e.auditHas(t, "deleting own messages off")
	if c := rpcCode(call(t, alice, &wire.DeleteMessage{MessageId: m.Id})); c != codeDisabled {
		t.Fatalf("delete with deleting off: %v", c)
	}
	// Manage Messages still deletes.
	if _, err := call(t, e.owner, &wire.DeleteMessage{MessageId: m.Id}); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------- join/leave messages

func TestJoinLeaveMessages(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	gen := e.owner.Channel("general")
	systems := func(channelID int64) []*wire.Message {
		r, err := call(t, e.owner, &wire.History{ChannelId: channelID})
		if err != nil {
			t.Fatal(err)
		}
		var out []*wire.Message
		for _, m := range r.GetMessages().Messages {
			if m.System != nil {
				out = append(out, m)
			}
		}
		return out
	}
	alice := e.member("Alice", e.invite.Secret)
	got := systems(gen.Id)
	if len(got) != 1 || got[0].System.Kind != wire.SystemEventKind_SYSTEM_EVENT_KIND_JOINED || !bytes.Equal(got[0].System.Member, alice.Identity.Public) || got[0].AuthorName != "Alice" {
		t.Fatalf("join message: %v", got)
	}
	// System messages can't be edited.
	if _, err := call(t, alice, &wire.Edit{MessageId: got[0].Id, Text: "x"}); err == nil {
		t.Fatal("edited a system message")
	}
	if _, err := alice.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_Leave{Leave: &wire.Leave{}}}); err != nil {
		t.Fatal(err)
	}
	got = systems(gen.Id)
	if len(got) != 2 || got[1].System.Kind != wire.SystemEventKind_SYSTEM_EVENT_KIND_LEFT {
		t.Fatalf("leave message: %v", got)
	}
	// Another channel; then off.
	res, err := e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: "lobby"}}})
	if err != nil {
		t.Fatal(err)
	}
	e.messaging(t, func(m *wire.CommunitySettings) { m.JoinLeaveChannel = res.Id })
	e.auditHas(t, "join and leave channel")
	e.member("Bob", e.invite.Secret)
	if len(systems(res.Id)) != 1 || len(systems(gen.Id)) != 2 {
		t.Fatal("join message not in the chosen channel")
	}
	e.messaging(t, func(m *wire.CommunitySettings) { m.JoinLeaveMessages = proto.Bool(false) })
	e.auditHas(t, "join and leave messages off")
	e.member("Carol", e.invite.Secret)
	if len(systems(res.Id)) != 1 {
		t.Fatal("join message posted while turned off")
	}
	// The channel must be a text channel that exists.
	e.owner.Sync(ctx)
	s := cloneSettings(e.owner.State.Settings)
	s.JoinLeaveChannel = 9999
	if _, err := e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_UpdateSettings{UpdateSettings: s}}); err == nil {
		t.Fatal("join/leave channel that doesn't exist accepted")
	}
}

func TestMessagingDefaults(t *testing.T) {
	m := community.DefaultSettings()
	// Attachments, reactions, edit/delete own, typing and join/leave are on by
	// default; previews are off.
	if !m.GetAttachmentsEnabled() || m.GetLinkPreviewsEnabled() || !m.GetReactionsEnabled() || !m.GetEditOwnEnabled() || !m.GetDeleteOwnEnabled() ||
		!m.GetTypingEnabled() || !m.GetJoinLeaveMessages() || m.GetAttachmentKinds() != wire.AttachmentKinds_ATTACHMENT_KINDS_ALL {
		t.Fatalf("messaging defaults: %v", m)
	}
	if community.DefaultEveryonePerms&(community.PermAddReactions|community.PermEmbedLinks|community.PermChangeNickname) == 0 ||
		community.DefaultEveryonePerms&(community.PermMentionEveryone|community.PermManageNicknames) != 0 {
		t.Fatal("@everyone messaging defaults")
	}
}

// ---------------------------------------------------------------- link previews

// previewSite serves a page with Open Graph tags and an image, counting hits.
type previewSite struct {
	srv  *httptest.Server
	hits atomic.Int32
}

func newPreviewSite(t *testing.T) *previewSite {
	ps := &previewSite{}
	mux := http.NewServeMux()
	page := `<!doctype html><html><head><title>Fallback</title>
<meta property="og:title" content="Herons &amp; egrets">
<meta property="og:description" content="Wading birds of the marsh.">
<meta property="og:image" content="/heron.png"></head><body>hi</body></html>`
	mux.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
		ps.hits.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, page)
	})
	mux.HandleFunc("/heron.png", func(w http.ResponseWriter, r *http.Request) {
		ps.hits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		w.Write(pngBytes)
	})
	mux.HandleFunc("/big.png", func(w http.ResponseWriter, r *http.Request) {
		ps.hits.Add(1)
		w.Write(append(append([]byte(nil), pngBytes...), make([]byte, 300<<10)...))
	})
	mux.HandleFunc("/bigimage", func(w http.ResponseWriter, r *http.Request) {
		ps.hits.Add(1)
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<meta property="og:title" content="Big image"><meta property="og:image" content="/big.png">`)
	})
	mux.HandleFunc("/json", func(w http.ResponseWriter, r *http.Request) {
		ps.hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"og:title": "nope"}`)
	})
	mux.HandleFunc("/huge", func(w http.ResponseWriter, r *http.Request) {
		ps.hits.Add(1)
		w.Header().Set("Content-Type", "text/html")
		w.Write(bytes.Repeat([]byte("<p>filler</p>\n"), (1<<20)/14+10))
		fmt.Fprint(w, `<meta property="og:title" content="Past the read limit">`)
	})
	mux.HandleFunc("/redirect/", func(w http.ResponseWriter, r *http.Request) {
		ps.hits.Add(1)
		var n int
		fmt.Sscanf(strings.TrimPrefix(r.URL.Path, "/redirect/"), "%d", &n)
		if n == 0 {
			http.Redirect(w, r, "/page", http.StatusFound)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/redirect/%d", n-1), http.StatusFound)
	})
	ps.srv = httptest.NewServer(mux)
	t.Cleanup(ps.srv.Close)
	return ps
}

// waitPreview waits for the message's preview to arrive in history.
func waitPreview(t *testing.T, m *testutil.Member, channelID, id int64, wait time.Duration) *wire.LinkPreview {
	t.Helper()
	deadline := time.Now().Add(wait)
	for {
		r, err := call(t, m, &wire.History{ChannelId: channelID})
		if err != nil {
			t.Fatal(err)
		}
		for _, hm := range r.GetMessages().Messages {
			if hm.Id == id && hm.Preview != nil {
				return hm.Preview
			}
		}
		if time.Now().After(deadline) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func previewEnv(t *testing.T, allowLoopback bool) *env {
	return setupWithConfig(t, nil, testutil.Quiet(), func(c *community.Config) {
		c.PreviewSchemes = []string{"http", "https"}
		c.PreviewAllowLoopback = allowLoopback
	})
}

func TestLinkPreviews(t *testing.T) {
	site := newPreviewSite(t)
	e := previewEnv(t, true)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	gen := alice.Channel("general")
	link := site.srv.URL + "/page"

	// Off by default: nothing is fetched.
	m := post(t, alice, gen.Id, "look "+link)
	if p := waitPreview(t, alice, gen.Id, m.Id, 700*time.Millisecond); p != nil || site.hits.Load() != 0 {
		t.Fatalf("preview with previews off: %v (%d hits)", p, site.hits.Load())
	}
	e.messaging(t, func(s *wire.CommunitySettings) { s.LinkPreviewsEnabled = proto.Bool(true) })
	e.auditHas(t, "link previews on")

	if err := e.owner.Watch(ctx, e.owner.Channel("general"), e.serverKey()); err != nil {
		t.Fatal(err)
	}
	m = post(t, alice, gen.Id, "look "+link+".")
	p := waitPreview(t, alice, gen.Id, m.Id, 5*time.Second)
	if p == nil || p.Title != "Herons & egrets" || p.Description != "Wading birds of the marsh." || p.Url != link {
		t.Fatalf("preview: %v", p)
	}
	if p.Image == nil {
		t.Fatal("preview without its image")
	}
	img, err := download(t, e.owner, p.Image)
	if err != nil || !bytes.Equal(img, pngBytes) {
		t.Fatalf("preview image: %v", err)
	}
	if e.owner.WaitEvent(gen.Id, func(ev *wire.ChannelEvent) bool { return ev.GetUpdate().GetPreview() != nil }, 5*time.Second) == nil {
		t.Fatal("no updated event with the preview")
	}
	// The preview is sealed with the message: it survives a restart.
	// (Checked through history below after the other cases.)

	// <link> suppresses, code doesn't count, and a spoilered link isn't fetched.
	before := site.hits.Load()
	for _, text := range []string{"<" + link + ">", "`" + link + "`", "||" + link + "||"} {
		m := post(t, alice, gen.Id, text)
		if p := waitPreview(t, alice, gen.Id, m.Id, 300*time.Millisecond); p != nil {
			t.Fatalf("preview for %q", text)
		}
	}
	if site.hits.Load() != before {
		t.Fatal("suppressed links were fetched")
	}
	// Not HTML, past the read limit, too many redirects: no preview.
	for _, path := range []string{"/json", "/huge", "/redirect/3"} {
		m := post(t, alice, gen.Id, site.srv.URL+path)
		if p := waitPreview(t, alice, gen.Id, m.Id, 1500*time.Millisecond); p != nil {
			t.Fatalf("preview for %s: %v", path, p)
		}
	}
	// Up to 3 redirects are followed.
	m = post(t, alice, gen.Id, site.srv.URL+"/redirect/2")
	if p := waitPreview(t, alice, gen.Id, m.Id, 5*time.Second); p == nil || p.Title != "Herons & egrets" {
		t.Fatalf("preview after 3 redirects: %v", p)
	}
	// An image over the limit is left out; the preview stays.
	m = post(t, alice, gen.Id, site.srv.URL+"/bigimage")
	if p := waitPreview(t, alice, gen.Id, m.Id, 5*time.Second); p == nil || p.Title != "Big image" || p.Image != nil {
		t.Fatalf("preview with an over-size image: %v", p)
	}
	// The sender needs Embed Links.
	e.everyonePerms(t, func(p uint64) uint64 { return p &^ community.PermEmbedLinks })
	m = post(t, alice, gen.Id, link)
	if p := waitPreview(t, alice, gen.Id, m.Id, 700*time.Millisecond); p != nil {
		t.Fatal("preview without Embed Links")
	}
	e.everyonePerms(t, func(p uint64) uint64 { return p | community.PermEmbedLinks })
	// Channel setting off.
	e.channelFeatures(t, gen.Id, chOff{previews: true})
	m = post(t, alice, gen.Id, link)
	if p := waitPreview(t, alice, gen.Id, m.Id, 700*time.Millisecond); p != nil {
		t.Fatal("preview in a channel with previews off")
	}
}

// The fetcher refuses loopback (and every other non-public address) unless
// the test-only config allowance is set: the linked server is never asked.
func TestLinkPreviewRefusesLoopback(t *testing.T) {
	site := newPreviewSite(t)
	e := previewEnv(t, false)
	alice := e.member("Alice", e.invite.Secret)
	gen := alice.Channel("general")
	e.messaging(t, func(s *wire.CommunitySettings) { s.LinkPreviewsEnabled = proto.Bool(true) })
	port := site.srv.URL[strings.LastIndex(site.srv.URL, ":")+1:]
	for _, link := range []string{site.srv.URL + "/page", "http://localhost:" + port + "/page", "http://[::1]:" + port + "/page"} {
		m := post(t, alice, gen.Id, link)
		if p := waitPreview(t, alice, gen.Id, m.Id, 1500*time.Millisecond); p != nil {
			t.Fatalf("preview of %s: %v", link, p)
		}
	}
	if n := site.hits.Load(); n != 0 {
		t.Fatalf("the loopback server was contacted %d times", n)
	}
}

// http links get no preview unless the operator allows the scheme.
func TestLinkPreviewHTTPSOnlyByDefault(t *testing.T) {
	site := newPreviewSite(t)
	e := setupWithConfig(t, nil, testutil.Quiet(), func(c *community.Config) { c.PreviewAllowLoopback = true })
	alice := e.member("Alice", e.invite.Secret)
	gen := alice.Channel("general")
	e.messaging(t, func(s *wire.CommunitySettings) { s.LinkPreviewsEnabled = proto.Bool(true) })
	m := post(t, alice, gen.Id, site.srv.URL+"/page")
	if p := waitPreview(t, alice, gen.Id, m.Id, 1500*time.Millisecond); p != nil || site.hits.Load() != 0 {
		t.Fatalf("http preview with https-only: %v", p)
	}
}

// An operator can turn the fetcher off entirely; the community toggle then
// can't be turned on.
func TestLinkPreviewsDisabledByOperator(t *testing.T) {
	e := setupWithConfig(t, nil, testutil.Quiet(), func(c *community.Config) { c.LinkPreviewsDisabled = true })
	ctx := testutil.Ctx(t)
	e.owner.Sync(ctx)
	s := cloneSettings(e.owner.State.Settings)
	s.LinkPreviewsEnabled = proto.Bool(true)
	if _, err := e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_UpdateSettings{UpdateSettings: s}}); err == nil || !strings.Contains(err.Error(), "operator") {
		t.Fatalf("previews turned on against the node config: %v", err)
	}
}

// ---------------------------------------------------------------- migration

// The messaging migration on a schema 3 database (testdata/v3msg, written
// by the schema 3 build's tests): the attached file moves
// into the message, deleted messages stay deleted, and @everyone gains the
// new default permissions.
func TestMessagingMigrationFromV3(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"community.db", "storage.key", "server.key", "noise.key"} {
		b, err := os.ReadFile(filepath.Join("testdata", "v3msg", f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var fx struct {
		FileMessage int64  `json:"file_message"`
		FileID      int64  `json:"file_id"`
		Deleted     int64  `json:"deleted"`
		Alice       string `json:"alice"`
	}
	b, _ := os.ReadFile(filepath.Join("testdata", "v3msg", "fixture.json"))
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatal(err)
	}
	if v := schemaVersion(t, dir); v != 3 {
		t.Fatalf("fixture at schema %d, want 3", v)
	}
	srv := reopen(t, dir, "ws://127.0.0.1:1/ws", testutil.Quiet())
	if v := schemaVersion(t, dir); v != community.LatestSchema() {
		t.Fatalf("schema after migration: %d", v)
	}
	ctx := context.Background()
	gen := srv.Snapshot().Channels["general"]
	msgs, err := srv.Messages(ctx, gen)
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, m := range msgs {
		texts = append(texts, m.Text)
		switch m.Id {
		case fx.FileMessage:
			if len(m.Files) != 1 || m.Files[0].FileId != fx.FileID || m.Files[0].Name != "notes.txt" || len(m.Files[0].Key) != 32 {
				t.Fatalf("file after migration: %v", m.Files)
			}
		case fx.Deleted:
			if !m.Deleted || m.Text != "" || len(m.Files) != 0 {
				t.Fatalf("deleted message after migration: %v", m)
			}
		}
	}
	if strings.Join(texts, "|") != "owner says hello from v3|alice says hi|a file|" {
		t.Fatalf("messages after migration: %q", texts)
	}
	if p, _ := srv.RolePerms("@everyone"); p&(community.PermAddReactions|community.PermEmbedLinks|community.PermChangeNickname) != community.PermAddReactions|community.PermEmbedLinks|community.PermChangeNickname {
		t.Fatalf("@everyone after migration: %b", p)
	}
	if n, _ := srv.StagedCount(ctx); n != 0 {
		t.Fatalf("the posted file looks staged after migration (%d)", n)
	}
	// Reopening is a no-op.
	srv.Close()
	srv = reopen(t, dir, "ws://127.0.0.1:1/ws", testutil.Quiet())
	if msgs, err := srv.Messages(ctx, gen); err != nil || len(msgs) != 4 {
		t.Fatalf("after reopening: %d %v", len(msgs), err)
	}
}
