package community_test

import (
	"bytes"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// noSecret fails when a reply or decrypted topic event carries the text.
func noSecret(t *testing.T, what string, m proto.Message, secret string) {
	t.Helper()
	if m == nil {
		return
	}
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte(secret)) {
		t.Fatalf("%s reveals the message text: %v", what, m)
	}
}

// Reactions and pins follow the history gate: a member who may not read a
// message (new member with "new members see history" off, or no Read
// Message History) can't react to it, remove a reaction from it, or list it
// as a pin, and nothing they receive carries its text. Updates published to
// the channel topic carry the change only, never the text.
func TestReactionsAndPinsFollowHistoryGate(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	gen := e.owner.Channel("general").Id
	now := time.Now()
	e.comm.Server.SetClock(func() time.Time { return now })
	const secret = "the vault code is 4711"
	old := post(t, e.owner, gen, secret)
	if _, err := call(t, e.owner, &wire.Pin{MessageId: old.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, e.owner, &wire.React{MessageId: old.Id, Emoji: "👍"}); err != nil {
		t.Fatal(err)
	}
	early := e.member("Early", e.invite.Secret)
	e.update(t, func(s *wire.CommunitySettings) { s.NewMembersSeeHistory = proto.Bool(false) })
	now = now.Add(time.Second)
	late := e.member("Late", e.invite.Secret)
	var genCh *wire.Channel
	for _, c := range fresh(t, late).Channels {
		if c.Id == gen {
			genCh = c
		}
	}
	if err := late.Watch(ctx, genCh, e.serverKey()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)

	// The new member: reacting, removing a reaction and the pin list refuse
	// or leave out the old message.
	for _, rx := range []*wire.React{{MessageId: old.Id, Emoji: "🎉"}, {MessageId: old.Id, Emoji: "👍", Remove: true}, {MessageId: old.Id, Emoji: "🎉", Remove: true}} {
		r, err := call(t, late, rx)
		if err == nil {
			t.Fatalf("new member reacted to a message from before they joined: %v", r)
		}
		noSecret(t, "react reply", r, secret)
	}
	r, err := call(t, late, &wire.ListPins{ChannelId: gen})
	if err != nil {
		t.Fatal(err)
	}
	noSecret(t, "pin list", r, secret)
	if n := len(r.GetPins().Messages); n != 0 {
		t.Fatalf("new member listed %d pins from before they joined", n)
	}
	// The member who joined earlier still sees it.
	r, err = call(t, early, &wire.ListPins{ChannelId: gen})
	if err != nil || len(r.GetPins().Messages) != 1 {
		t.Fatalf("early member's pins: %v %v", r, err)
	}
	// A message from after the join is fine to react to.
	fresh := post(t, e.owner, gen, "after you joined")
	if _, err := call(t, late, &wire.React{MessageId: fresh.Id, Emoji: "🎉"}); err != nil {
		t.Fatalf("react to a new message: %v", err)
	}

	// What others do to the old message reaches the new member's topic as
	// an update without the text.
	if _, err := call(t, early, &wire.React{MessageId: old.Id, Emoji: "🔥"}); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, e.owner, &wire.Pin{MessageId: old.Id, Unpin: true}); err != nil {
		t.Fatal(err)
	}
	if late.WaitEvent(gen, func(ev *wire.ChannelEvent) bool {
		return ev.GetUpdate().GetMessageId() == old.Id && ev.GetUpdate().GetReactions() != nil
	}, 5*time.Second) == nil {
		t.Fatal("no reactions update on the topic")
	}
	if late.WaitEvent(gen, func(ev *wire.ChannelEvent) bool {
		u := ev.GetUpdate()
		_, isPin := u.GetChange().(*wire.MessageUpdate_Pinned)
		return u.GetMessageId() == old.Id && isPin && !u.GetPinned()
	}, 5*time.Second) == nil {
		t.Fatal("no pin update on the topic")
	}
	if ev := late.WaitEvent(gen, func(ev *wire.ChannelEvent) bool {
		b, _ := proto.Marshal(ev)
		return bytes.Contains(b, []byte(secret))
	}, 200*time.Millisecond); ev != nil {
		t.Fatalf("a topic event reveals the message text: %v", ev)
	}

	// Read Message History denied: no reactions and no pin list at all.
	e.must(override(&wire.SetChannelOverride{ChannelId: gen, Member: early.Identity.Public, Deny: community.PermReadMessageHistory}))
	for _, rx := range []*wire.React{{MessageId: old.Id, Emoji: "🎉"}, {MessageId: old.Id, Emoji: "🔥", Remove: true}} {
		r, err := call(t, early, rx)
		permDenied(t, "react without Read Message History", err)
		noSecret(t, "react reply", r, secret)
	}
	r, err = call(t, early, &wire.ListPins{ChannelId: gen})
	permDenied(t, "pins without Read Message History", err)
	noSecret(t, "pin list", r, secret)
}
