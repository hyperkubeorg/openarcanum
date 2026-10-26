package community_test

import (
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// A message's reactions are bounded in all as well as in distinct emoji,
// so members adding themselves to every emoji can't grow the sealed body
// (re-sent on each change) without limit.
func TestReactionsBoundedInAll(t *testing.T) {
	old := community.MaxReactors
	community.MaxReactors = 3
	t.Cleanup(func() { community.MaxReactors = old })
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	gen := e.owner.Channel("general").Id
	msg, err := e.owner.Post(ctx, gen, "react to this")
	if err != nil {
		t.Fatal(err)
	}
	react := func(m *testutil.Member, emoji string, remove bool) error {
		_, err := m.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_React{React: &wire.React{MessageId: msg.Id, Emoji: emoji, Remove: remove}}})
		return err
	}
	a := e.member("A", e.invite.Secret)
	b := e.member("B", e.invite.Secret)
	for _, err := range []error{react(e.owner, "👍", false), react(a, "👍", false), react(b, "🎉", false)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	wantErr(t, react(a, "🎉", false), "at most 3 reactions")
	wantErr(t, react(e.owner, "🔥", false), "at most 3 reactions")
	// Taking one back makes room.
	if err := react(b, "🎉", true); err != nil {
		t.Fatal(err)
	}
	if err := react(a, "🎉", false); err != nil {
		t.Fatal(err)
	}
}
