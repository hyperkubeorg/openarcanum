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

func channelIn(t *testing.T, st *wire.SyncState, id int64) *wire.Channel {
	t.Helper()
	for _, c := range st.Channels {
		if c.Id == id {
			return c
		}
	}
	t.Fatalf("channel %d not in state", id)
	return nil
}

// With "New members see history" off, a member who joins gets a channel
// key and topic made after they joined, so envelopes a relay's chunk cache
// still holds from before can't be fetched or decrypted with what they
// were given; the members already in get the new key too. With history on
// (or in a channel quiet for longer than any relay caches) nothing rotates.
func TestJoinRotatesChannelsWhenHistoryHidden(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	gen := e.owner.Channel("general").Id
	early := e.member("Early", e.invite.Secret)

	// History visible: a join rotates nothing.
	post(t, e.owner, gen, "before anyone new")
	before := channelIn(t, fresh(t, e.owner), gen)
	e.member("Visible", e.invite.Secret)
	if after := channelIn(t, fresh(t, e.owner), gen); after.KeyEpoch != before.KeyEpoch {
		t.Fatalf("join with history visible rotated the channel (%d -> %d)", before.KeyEpoch, after.KeyEpoch)
	}

	e.update(t, func(s *wire.CommunitySettings) { s.NewMembersSeeHistory = proto.Bool(false) })
	post(t, e.owner, gen, "the vault code is 4711")
	before = channelIn(t, fresh(t, e.owner), gen)
	late := e.member("Late", e.invite.Secret)
	got := channelIn(t, fresh(t, late), gen)
	if got.KeyEpoch == before.KeyEpoch || bytes.Equal(got.Key, before.Key) || bytes.Equal(got.Topic, before.Topic) {
		t.Fatalf("new member got the channel's pre-join key or topic (epoch %d -> %d)", before.KeyEpoch, got.KeyEpoch)
	}
	// Members already in follow the rotation.
	for _, m := range []*testutil.Member{e.owner, early} {
		if c := channelIn(t, fresh(t, m), gen); c.KeyEpoch != got.KeyEpoch || !bytes.Equal(c.Key, got.Key) || !bytes.Equal(c.Topic, got.Topic) {
			t.Fatalf("an existing member has epoch %d, the new member %d", c.KeyEpoch, got.KeyEpoch)
		}
	}
	// And messaging carries on under the new key.
	if err := late.Watch(ctx, got, e.serverKey()); err != nil {
		t.Fatal(err)
	}
	post(t, e.owner, gen, "welcome")

	// Quiet for longer than any relay may cache: no rotation is needed.
	later := time.Now().Add(community.MaxRelayCacheTTL + time.Minute)
	e.comm.Server.SetClock(func() time.Time { return later })
	quiet := channelIn(t, fresh(t, e.owner), gen)
	e.member("Quiet", e.invite.Secret)
	if c := channelIn(t, fresh(t, e.owner), gen); c.KeyEpoch != quiet.KeyEpoch {
		t.Fatal("a join rotated a channel nothing was published to within the cache lifetime")
	}
}
