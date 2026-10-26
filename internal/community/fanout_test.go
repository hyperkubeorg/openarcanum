package community_test

import (
	"testing"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/fanout"
	"github.com/hyperkubeorg/openarcanum/internal/relay"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// On a relay that wants fan-out tokens for any topic with more than one
// other subscriber, the community server still reaches every member (it
// hands each relay connection a token for its key, and its channel topics
// commit to that key), while a member who knows the topic can't publish to
// it.
func TestLargeChannelFanoutUsesToken(t *testing.T) {
	r := testutil.StartRelayWith(t, true, func(c *relay.Config) { c.FanoutThreshold = 1 })
	comm := testutil.StartCommunity(t, nil, r.URL)
	e := claimedOn(t, r, comm, nil)
	ctx := testutil.Ctx(t)
	a := e.member("A", e.invite.Secret)
	b := e.member("B", e.invite.Secret)
	general := e.owner.Channel("general")
	if !fanout.Commits(general.Topic, e.serverKey()) {
		t.Fatal("channel topic doesn't commit to the server key")
	}
	for _, m := range []*testutil.Member{e.owner, a, b} {
		if err := m.Watch(ctx, m.Channel("general"), e.serverKey()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.Post(ctx, general.Id, "hello everyone"); err != nil {
		t.Fatal(err)
	}
	for _, m := range []*testutil.Member{e.owner, a, b} {
		if got := m.WaitReceived(general.Id, 1, 10*time.Second); len(got) == 0 || got[len(got)-1].Text != "hello everyone" {
			t.Fatalf("%s didn't get the message", m.Name)
		}
	}
	// A member publishing straight to the topic is refused by the relay.
	before := r.Server.Stats().FanoutRefused.Load()
	if err := a.Relay.Publish(general.Topic, []byte("forged fan-out")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for r.Server.Stats().FanoutRefused.Load() == before {
		if time.Now().After(deadline) {
			t.Fatal("relay let a member fan out to a large channel topic")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// With a relay chunk cache, a member that (re)subscribes asks for a replay
// and gets the channel events it missed; they are the server's signed,
// encrypted envelopes, unchanged.
func TestChunkCacheReplaysChannelEvents(t *testing.T) {
	r := testutil.StartRelayWith(t, true, func(c *relay.Config) { c.ChunkCacheBytes = 64 << 20 })
	comm := testutil.StartCommunity(t, nil, r.URL)
	e := claimedOn(t, r, comm, nil)
	ctx := testutil.Ctx(t)
	a := e.member("A", e.invite.Secret)
	general := e.owner.Channel("general")
	if _, err := e.owner.Post(ctx, general.Id, "while you were away"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	ch := a.Channel("general")
	got := make(chan *wire.Message, 64)
	if err := a.Relay.SubscribeReplay(ctx, ch.Topic, func(_, data []byte) {
		if m, err := testutil.OpenEnvelope(data, e.serverKey(), ch.Key, ch.KeyEpoch); err == nil {
			got <- m
		}
	}, time.Minute); err != nil {
		t.Fatal(err)
	}
	// Earlier events (the join message) come first; the post must follow.
	timeout := time.After(10 * time.Second)
	for found := false; !found; {
		select {
		case m := <-got:
			found = m.Text == "while you were away"
		case <-timeout:
			t.Fatal("no replay of the post from the chunk cache")
		}
	}
	if r.Server.CacheBytes() == 0 {
		t.Fatal("cache empty")
	}
}
