package community_test

import (
	"strings"
	"testing"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Community Admin → Insights: the day's joins, members who connected or
// posted, messages per channel; counts only, for admins only.
func TestInsights(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	gen := e.owner.Channel("general")
	for _, text := range []string{"one", "two"} {
		post(t, alice, gen.Id, text)
	}
	post(t, e.owner, gen.Id, "three")

	ask := func(m *testutil.Member) (*wire.Insights, error) {
		r, err := m.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_GetInsights{GetInsights: &wire.GetInsights{}}})
		if err != nil {
			return nil, err
		}
		return r.GetInsights(), nil
	}
	in, err := ask(e.owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Days) != 1 || in.Days[0].Day != time.Now().UTC().Format(time.DateOnly) {
		t.Fatalf("days: %v", in.Days)
	}
	d := in.Days[0]
	// Alice joined (the owner claimed: not a join); both posted and connected.
	if d.Joined != 1 || d.Posters != 2 || d.Active != 2 || d.Members != 2 || d.Messages < 3 || d.PeakOnline < 1 {
		t.Fatalf("today: %+v", d)
	}
	if in.Members != 2 || in.Online != 2 {
		t.Fatalf("now: members %d online %d", in.Members, in.Online)
	}
	if len(in.Channels) == 0 || in.Channels[0].ChannelId != gen.Id || in.Channels[0].Messages < 3 {
		t.Fatalf("channels: %v", in.Channels)
	}
	// Posting again doesn't count Alice twice.
	post(t, alice, gen.Id, "four")
	in, _ = ask(e.owner)
	if in.Days[0].Posters != 2 {
		t.Fatalf("posters after a second post: %d", in.Days[0].Posters)
	}
	// A member without Manage community or View audit log can't see them.
	if _, err := ask(alice); err == nil || !strings.Contains(err.Error(), "insights are for admins") {
		t.Fatalf("a member's insights: %v", err)
	}
}
