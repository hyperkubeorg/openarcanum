package community_test

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/callauth"
	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

func voiceChannel(e *env, name string) int64 {
	return e.must(&wire.AdminAction{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: name, Kind: wire.ChannelKind_CHANNEL_KIND_VOICE}}}).Id
}

// nextVoice waits for m's next voice update matching ok.
func nextVoice(t *testing.T, m *testutil.Member, ok func(*wire.VoiceSession) bool) *wire.VoiceSession {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev, open := <-m.Events:
			if !open {
				t.Fatalf("%s: session closed", m.Name)
			}
			if vs := ev.GetVoiceUpdate().GetSession(); vs != nil && ok(vs) {
				return vs
			}
		case <-deadline:
			t.Fatalf("%s: no matching voice update", m.Name)
		}
	}
}

func voiceCall(m *testutil.Member, msg *wire.ClientMessage) error {
	_, err := m.Call(context.Background(), msg)
	return err
}

func participantOf(vs *wire.VoiceSession, member []byte) *wire.VoiceParticipant {
	for _, p := range vs.Participants {
		if bytes.Equal(p.Member, member) {
			return p
		}
	}
	return nil
}

// Joining gives a grant the relay will accept, for a call id that commits to
// this server's key; every join and leave changes the call key; each
// participant has its own id and slot.
func TestCallJoinGrantsAndRekeys(t *testing.T) {
	e := setup(t, nil)
	alice := e.member("Alice", e.invite.Secret)
	bob := e.member("Bob", e.invite.Secret)
	waitAttached(t, e)
	vc := voiceChannel(e, "talk")

	as, err := voiceJoin(alice, vc)
	if err != nil {
		t.Fatal(err)
	}
	g := grantOf(t, as)
	if !bytes.Equal(g.ServerKey, e.serverKey()) || !bytes.Equal(g.CallId, callauth.CallID(e.serverKey(), g.CallNonce)) {
		t.Fatalf("grant isn't for this server's call: %v", g)
	}
	if !bytes.Equal(g.Participant, as.Participant) || g.Kid != as.Slot || as.Slot != 1 || as.Relay == "" {
		t.Fatalf("session: %v grant: %v", as, g)
	}
	// Short of the longest lifetime, so a relay whose clock is a little
	// behind still takes it.
	if exp := time.UnixMilli(g.ExpiresAt); time.Until(exp) > callauth.MaxGrantLifetime-community.GrantClockMargin || time.Until(exp) < time.Minute {
		t.Fatalf("grant expiry %v", exp)
	}

	bs, err := voiceJoin(bob, vc)
	if err != nil {
		t.Fatal(err)
	}
	if bs.KeyEpoch != as.KeyEpoch+1 || bytes.Equal(bs.CallKey, as.CallKey) || bs.Slot != 2 || bytes.Equal(bs.Participant, as.Participant) {
		t.Fatalf("bob's session: %v", bs)
	}
	au := nextVoice(t, alice, func(vs *wire.VoiceSession) bool { return len(vs.Participants) == 2 })
	if !bytes.Equal(au.CallKey, bs.CallKey) || au.KeyEpoch != bs.KeyEpoch {
		t.Fatal("alice didn't get the new key")
	}
	// A rejoin (reconnecting to the relay) is a fresh grant for the same
	// participant and a new key: the client's SFrame counters start again,
	// so the old key must never be used again (no nonce reuse).
	r, err := alice.Call(context.Background(), &wire.ClientMessage{Body: &wire.ClientMessage_VoiceJoin{VoiceJoin: &wire.VoiceJoin{ChannelId: vc, Rejoin: true}}})
	rs := r.GetVoiceSession()
	if err != nil || rs.KeyEpoch != bs.KeyEpoch+1 || bytes.Equal(rs.CallKey, bs.CallKey) || !bytes.Equal(rs.Participant, as.Participant) {
		t.Fatalf("rejoin: %v %v", r, err)
	}
	nextVoice(t, bob, func(vs *wire.VoiceSession) bool { return vs.KeyEpoch == rs.KeyEpoch })
	// Every session carries the call's current control, signed by the
	// server, for the relay.
	if ctl, err := callauth.VerifyControl(rs.Control, e.serverKey()); err != nil || !bytes.Equal(ctl.CallId, g.CallId) {
		t.Fatalf("control in the session: %v %v", ctl, err)
	}
	bs = rs
	// Bob leaves: alice gets a new key bob never saw.
	if err := voiceCall(bob, &wire.ClientMessage{Body: &wire.ClientMessage_VoiceLeave{VoiceLeave: &wire.VoiceLeave{ChannelId: vc}}}); err != nil {
		t.Fatal(err)
	}
	au = nextVoice(t, alice, func(vs *wire.VoiceSession) bool { return len(vs.Participants) == 1 })
	if au.KeyEpoch != bs.KeyEpoch+1 || bytes.Equal(au.CallKey, bs.CallKey) {
		t.Fatalf("after bob left: %v", au)
	}
}

// A state that changes nothing is acknowledged without a push; changes are
// allowed a few at once, then one a second.
func TestVoiceStateChangesLimited(t *testing.T) {
	e := setup(t, nil)
	alice := e.member("Alice", e.invite.Secret)
	waitAttached(t, e)
	vc := voiceChannel(e, "talk")
	if _, err := voiceJoin(alice, vc); err != nil {
		t.Fatal(err)
	}
	state := func(mute bool) *wire.ClientMessage {
		return &wire.ClientMessage{Body: &wire.ClientMessage_VoiceState{VoiceState: &wire.VoiceState{ChannelId: vc, SelfMute: mute}}}
	}
	for i := 0; i < 20; i++ {
		if err := voiceCall(alice, state(false)); err != nil {
			t.Fatalf("the same state again (%d): %v", i, err)
		}
	}
	var limited error
	for i := 0; i < 8 && limited == nil; i++ {
		limited = voiceCall(alice, state(i%2 == 0))
	}
	wantErr(t, limited, "limited")
}

// Self mute, deafen, camera and screen reach everyone; camera and screen
// only count with Video and Screen Share.
func TestVoiceStateFlags(t *testing.T) {
	e := setup(t, nil)
	alice := e.member("Alice", e.invite.Secret)
	bob := e.member("Bob", e.invite.Secret)
	waitAttached(t, e)
	vc := voiceChannel(e, "talk")
	if _, err := voiceJoin(alice, vc); err != nil {
		t.Fatal(err)
	}
	if _, err := voiceJoin(bob, vc); err != nil {
		t.Fatal(err)
	}
	// Screen Share is off for @everyone by default; Video is on.
	set := &wire.ClientMessage{Body: &wire.ClientMessage_VoiceState{VoiceState: &wire.VoiceState{ChannelId: vc, SelfDeaf: true, Camera: true, Screen: true}}}
	if err := voiceCall(alice, set); err != nil {
		t.Fatal(err)
	}
	bu := nextVoice(t, bob, func(vs *wire.VoiceSession) bool {
		p := participantOf(vs, alice.Identity.Public)
		return p != nil && p.SelfDeaf
	})
	p := participantOf(bu, alice.Identity.Public)
	if !p.SelfMute || !p.Camera || p.Screen {
		t.Fatalf("alice's flags: %v", p)
	}
	// Not in the call: refused.
	carol := e.member("Carol", e.invite.Secret)
	if err := voiceCall(carol, set); err == nil {
		t.Fatal("state for a call carol isn't in")
	}
}

// Server mute and deafen: Mute Members / Deafen Members, below the actor
// only, audited, kept across calls.
func TestVoiceServerMuteAndDeafen(t *testing.T) {
	e := setup(t, nil)
	alice := e.member("Alice", e.invite.Secret)
	bob := e.member("Bob", e.invite.Secret)
	waitAttached(t, e)
	vc := voiceChannel(e, "talk")
	if _, err := voiceJoin(alice, vc); err != nil {
		t.Fatal(err)
	}
	if _, err := voiceJoin(bob, vc); err != nil {
		t.Fatal(err)
	}
	mute := func(by *testutil.Member, who []byte, on bool) error {
		_, err := e.act(by, &wire.AdminAction{Op: &wire.AdminAction_VoiceMute{VoiceMute: &wire.VoiceMute{Member: who, Muted: on}}})
		return err
	}
	permDenied(t, "bob muting alice without Mute Members", mute(bob, alice.Identity.Public, true))
	if err := mute(e.owner, alice.Identity.Public, true); err != nil {
		t.Fatal(err)
	}
	nextVoice(t, alice, func(vs *wire.VoiceSession) bool { return vs.ServerMute })
	nextVoice(t, bob, func(vs *wire.VoiceSession) bool {
		p := participantOf(vs, alice.Identity.Public)
		return p != nil && p.ServerMute
	})
	auditHas(t, e, "server muted Alice")
	// Still muted in the next call.
	if err := voiceCall(alice, &wire.ClientMessage{Body: &wire.ClientMessage_VoiceLeave{VoiceLeave: &wire.VoiceLeave{ChannelId: vc}}}); err != nil {
		t.Fatal(err)
	}
	as, err := voiceJoin(alice, vc)
	if err != nil || !as.ServerMute {
		t.Fatalf("rejoined: %v %v", as, err)
	}
	if err := mute(e.owner, alice.Identity.Public, false); err != nil {
		t.Fatal(err)
	}
	nextVoice(t, alice, func(vs *wire.VoiceSession) bool { return !vs.ServerMute && len(vs.Participants) == 2 })

	deafen := &wire.AdminAction{Op: &wire.AdminAction_VoiceDeafen{VoiceDeafen: &wire.VoiceDeafen{Member: bob.Identity.Public, Deafened: true}}}
	_, err = e.act(alice, deafen)
	permDenied(t, "alice deafening without Deafen Members", err)
	e.must(deafen)
	nextVoice(t, bob, func(vs *wire.VoiceSession) bool { return vs.ServerDeaf })
	auditHas(t, e, "server deafened Bob")
	// Nobody acts on the owner.
	_, err = e.act(alice, &wire.AdminAction{Op: &wire.AdminAction_VoiceMute{VoiceMute: &wire.VoiceMute{Member: e.owner.Identity.Public, Muted: true}}})
	permDenied(t, "muting the owner", err)
}

// Move Members: the member's call ends with where to go; channel 0
// disconnects them. The mover must be able to connect there, and so must
// the member.
func TestVoiceMoveAndDisconnect(t *testing.T) {
	e := setup(t, nil)
	alice := e.member("Alice", e.invite.Secret)
	bob := e.member("Bob", e.invite.Secret)
	waitAttached(t, e)
	a, b := voiceChannel(e, "a"), voiceChannel(e, "b")
	closed := voiceChannel(e, "closed")
	e.must(override(&wire.SetChannelOverride{ChannelId: closed, Member: alice.Identity.Public, Deny: community.PermConnectVoice}))
	if _, err := voiceJoin(alice, a); err != nil {
		t.Fatal(err)
	}
	if _, err := voiceJoin(bob, a); err != nil {
		t.Fatal(err)
	}
	move := func(by *testutil.Member, to int64) error {
		_, err := e.act(by, &wire.AdminAction{Op: &wire.AdminAction_VoiceMove{VoiceMove: &wire.VoiceMove{Member: alice.Identity.Public, ChannelId: to}}})
		return err
	}
	permDenied(t, "bob moving alice", move(bob, b))
	permDenied(t, "moving alice where she can't connect", move(e.owner, closed))
	if err := move(e.owner, b); err != nil {
		t.Fatal(err)
	}
	end := nextVoice(t, alice, func(vs *wire.VoiceSession) bool { return vs.Ended != "" })
	if end.MovedTo != b || end.ChannelId != a {
		t.Fatalf("moved: %v", end)
	}
	nextVoice(t, bob, func(vs *wire.VoiceSession) bool { return len(vs.Participants) == 1 })
	auditHas(t, e, "moved Alice to b")
	// Alice's client joins b; then she's disconnected.
	if _, err := voiceJoin(alice, b); err != nil {
		t.Fatal(err)
	}
	if err := move(e.owner, 0); err != nil {
		t.Fatal(err)
	}
	end = nextVoice(t, alice, func(vs *wire.VoiceSession) bool { return vs.Ended != "" })
	if end.MovedTo != 0 || end.ChannelId != b {
		t.Fatalf("disconnected: %v", end)
	}
	auditHas(t, e, "disconnected Alice from voice")
}

// Losing a permission mid-call takes effect at once: Video goes from the
// session (and the relay stops forwarding it); Connect ends the call.
func TestVoicePermissionsRecheckedMidCall(t *testing.T) {
	e := setup(t, nil)
	alice := e.member("Alice", e.invite.Secret)
	waitAttached(t, e)
	vc := voiceChannel(e, "talk")
	as, err := voiceJoin(alice, vc)
	if err != nil || !as.CanVideo {
		t.Fatalf("join: %v %v", as, err)
	}
	e.must(override(&wire.SetChannelOverride{ChannelId: vc, Member: alice.Identity.Public, Deny: community.PermVideo}))
	vs := nextVoice(t, alice, func(vs *wire.VoiceSession) bool { return !vs.CanVideo })
	if grantOf(t, vs).Video {
		t.Fatal("grant still allows video")
	}
	e.must(override(&wire.SetChannelOverride{ChannelId: vc, Member: alice.Identity.Public, Deny: community.PermVideo | community.PermConnectVoice}))
	end := nextVoice(t, alice, func(vs *wire.VoiceSession) bool { return vs.Ended != "" })
	if end.ChannelId != vc {
		t.Fatalf("ended: %v", end)
	}
}

// Relay failover: a call moves when half its participants can't reach the
// relay, or when the server loses the relay itself.
func TestCallMovesToAnotherRelay(t *testing.T) {
	sessions := testutil.StartRelayCalls(t, false)
	r1, r2 := testutil.StartRelay(t), testutil.StartRelay(t)
	c := testutil.StartCommunity(t, nil, sessions.URL, r1.URL, r2.URL)
	e := claimedOn(t, sessions, c, nil)
	alice := e.member("Alice", e.invite.Secret)
	bob := e.member("Bob", e.invite.Secret)
	carol := e.member("Carol", e.invite.Secret)
	waitFor(t, "three relays", func() bool { return len(c.Server.AttachedRelays()) == 3 })
	vc := voiceChannel(e, "talk")
	as, err := voiceJoin(alice, vc)
	if err != nil {
		t.Fatal(err)
	}
	if as.Relay == sessions.URL {
		t.Fatal("call put on a relay without calls")
	}
	for _, m := range []*testutil.Member{bob, carol} {
		if _, err := voiceJoin(m, vc); err != nil {
			t.Fatal(err)
		}
	}
	report := func(m *testutil.Member, relay string) *wire.VoiceSession {
		r, err := m.Call(context.Background(), &wire.ClientMessage{Body: &wire.ClientMessage_VoiceJoin{VoiceJoin: &wire.VoiceJoin{ChannelId: vc, FailedRelay: relay}}})
		if err != nil {
			t.Fatal(err)
		}
		return r.GetVoiceSession()
	}
	// One of three can't reach it: probably their network, the call stays.
	if vs := report(alice, as.Relay); vs.Relay != as.Relay {
		t.Fatal("moved on one report out of three")
	}
	// Two of three: it moves, with a new key and fresh grants for everyone.
	vs := report(bob, as.Relay)
	if vs.Relay == as.Relay || vs.Relay == sessions.URL {
		t.Fatalf("not moved: %v", vs.Relay)
	}
	cu := nextVoice(t, carol, func(x *wire.VoiceSession) bool { return x.Relay == vs.Relay })
	if cu.KeyEpoch <= as.KeyEpoch+2 || !bytes.Equal(grantOf(t, cu).CallId, grantOf(t, as).CallId) {
		t.Fatalf("after the move: %v", cu)
	}
	// The server loses the new relay: the call goes back to the other one
	// (after the minimum gap between moves).
	time.Sleep(11 * time.Second)
	moved := vs.Relay
	if moved == r1.URL {
		r1.Stop()
	} else {
		r2.Stop()
	}
	cu = nextVoice(t, carol, func(x *wire.VoiceSession) bool { return x.Relay != moved })
	if cu.Relay == sessions.URL {
		t.Fatal("moved onto the relay without calls")
	}
}

// Voice channel names keep their capitals and spaces (as in Discord); text
// channel names are still lowercase without spaces.
func TestVoiceChannelNames(t *testing.T) {
	e := setup(t, nil)
	id := e.must(&wire.AdminAction{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: "  Game Night 🎲 ", Kind: wire.ChannelKind_CHANNEL_KIND_VOICE}}}).Id
	if err := e.owner.Sync(testutil.Ctx(t)); err != nil {
		t.Fatal(err)
	}
	for _, c := range e.owner.State.Channels {
		if c.Id == id && c.Name != "Game Night 🎲" {
			t.Fatalf("voice channel name: %q", c.Name)
		}
	}
	_, err := e.act(e.owner, &wire.AdminAction{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: "bad\x07name", Kind: wire.ChannelKind_CHANNEL_KIND_VOICE}}})
	if err == nil {
		t.Fatal("control character accepted")
	}
	tid := e.must(&wire.AdminAction{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: "Big News"}}}).Id
	if err := e.owner.Sync(testutil.Ctx(t)); err != nil {
		t.Fatal(err)
	}
	for _, c := range e.owner.State.Channels {
		if c.Id == tid && c.Name != "big-news" {
			t.Fatalf("text channel name: %q", c.Name)
		}
	}
}

// A timeout takes Speak (and video and screen share) away in a call the
// member is already in: the recheck after the admin action makes them
// listen-only with a grant that no longer allows audio, and lifting the
// timeout gives Speak back.
func TestTimeoutMakesCallListenOnly(t *testing.T) {
	e := setup(t, nil)
	alice := e.member("Alice", e.invite.Secret)
	waitAttached(t, e)
	vc := voiceChannel(e, "talk")
	as, err := voiceJoin(alice, vc)
	if err != nil || as.ListenOnly || !grantOf(t, as).Audio || !as.CanVideo {
		t.Fatalf("join: %v %v", as, err)
	}
	e.mustAdmin(e.owner, timeoutOp(alice, 600))
	vs := nextVoice(t, alice, func(vs *wire.VoiceSession) bool { return vs.ListenOnly })
	if grantOf(t, vs).Audio || vs.CanVideo || vs.CanScreen {
		t.Fatalf("timed out in a call: %v", vs)
	}
	if p := participantOf(vs, alice.Identity.Public); p == nil || !p.ListenOnly {
		t.Fatalf("participant entry while timed out: %v", p)
	}
	e.mustAdmin(e.owner, timeoutOp(alice, 0))
	vs = nextVoice(t, alice, func(vs *wire.VoiceSession) bool { return !vs.ListenOnly })
	if !grantOf(t, vs).Audio || !vs.CanVideo {
		t.Fatalf("after the timeout: %v", vs)
	}
}

// Two participants report the relay failed at the same moment (they time
// out together): the call moves once, and never back to the failed relay.
func TestConcurrentFailureReportsMoveOnce(t *testing.T) {
	sessions := testutil.StartRelayCalls(t, false)
	r1, r2 := testutil.StartRelay(t), testutil.StartRelay(t)
	c := testutil.StartCommunity(t, nil, sessions.URL, r1.URL, r2.URL)
	e := claimedOn(t, sessions, c, nil)
	alice := e.member("Alice", e.invite.Secret)
	bob := e.member("Bob", e.invite.Secret)
	waitFor(t, "three relays", func() bool { return len(c.Server.AttachedRelays()) == 3 })
	vc := voiceChannel(e, "talk")
	as, err := voiceJoin(alice, vc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := voiceJoin(bob, vc); err != nil {
		t.Fatal(err)
	}
	failed := as.Relay
	var wg sync.WaitGroup
	relays := make([]string, 2)
	for i, m := range []*testutil.Member{alice, bob} {
		wg.Add(1)
		go func(i int, m *testutil.Member) {
			defer wg.Done()
			r, err := m.Call(context.Background(), &wire.ClientMessage{Body: &wire.ClientMessage_VoiceJoin{VoiceJoin: &wire.VoiceJoin{ChannelId: vc, FailedRelay: failed}}})
			if err != nil {
				t.Error(err)
				return
			}
			relays[i] = r.GetVoiceSession().Relay
		}(i, m)
	}
	wg.Wait()
	for _, r := range relays {
		if r == failed || r == sessions.URL {
			t.Fatalf("call on %s after reports about %s (relays %v)", r, failed, relays)
		}
	}
	if relays[0] != relays[1] {
		t.Fatalf("moved twice: %v", relays)
	}
}

// Priority Speaker: the permission (from roles or a channel override) says a
// participant may switch it on, in their session and in the grant the relay
// sees; it's off until they do, and everyone's view of the call shows it only
// while it's both switched on and permitted. A channel override or taking the
// role away mid-call clears it.
func TestPrioritySpeaker(t *testing.T) {
	e := setup(t, nil)
	alice := e.member("Alice", e.invite.Secret)
	bob := e.member("Bob", e.invite.Secret)
	waitAttached(t, e)
	vc := voiceChannel(e, "talk")
	as, err := voiceJoin(alice, vc)
	if err != nil {
		t.Fatal(err)
	}
	if as.Priority || grantOf(t, as).Priority {
		t.Fatal("@everyone has Priority Speaker by default")
	}
	_, err = voiceJoin(bob, vc)
	if err != nil {
		t.Fatal(err)
	}
	setPriority := func(on bool) {
		t.Helper()
		set := &wire.ClientMessage{Body: &wire.ClientMessage_VoiceState{VoiceState: &wire.VoiceState{ChannelId: vc, Priority: on}}}
		if err := voiceCall(alice, set); err != nil {
			t.Fatal(err)
		}
	}
	// Switching it on without the permission does nothing.
	setPriority(true)
	bs := nextVoice(t, bob, func(*wire.VoiceSession) bool { return true })
	if bs.Priority || participantOf(bs, alice.Identity.Public).Priority {
		t.Fatalf("priority without the permission: %v", bs)
	}
	setPriority(false)

	role := e.must(createRole("Host", community.PermPrioritySpeaker)).Id
	e.must(assign(alice, role))
	au := nextVoice(t, alice, func(vs *wire.VoiceSession) bool { return vs.Priority })
	if g := grantOf(t, au); !g.Priority || !bytes.Equal(g.Participant, au.Participant) {
		t.Fatalf("alice's grant after the role: %v", g)
	}
	// Permitted, but off until she switches it on.
	if participantOf(au, alice.Identity.Public).Priority {
		t.Fatalf("priority on by default: %v", au.Participants)
	}
	setPriority(true)
	bu := nextVoice(t, bob, func(vs *wire.VoiceSession) bool {
		p := participantOf(vs, alice.Identity.Public)
		return p != nil && p.Priority
	})
	if bu.Priority || grantOf(t, bu).Priority || participantOf(bu, bob.Identity.Public).Priority {
		t.Fatalf("bob got priority from alice's role: %v", bu)
	}
	// And off again.
	setPriority(false)
	nextVoice(t, bob, func(vs *wire.VoiceSession) bool {
		p := participantOf(vs, alice.Identity.Public)
		return p != nil && !p.Priority
	})
	setPriority(true)
	nextVoice(t, bob, func(vs *wire.VoiceSession) bool {
		p := participantOf(vs, alice.Identity.Public)
		return p != nil && p.Priority
	})

	// A channel override takes it away in this channel.
	e.must(override(&wire.SetChannelOverride{ChannelId: vc, Member: alice.Identity.Public, Deny: community.PermPrioritySpeaker}))
	au = nextVoice(t, alice, func(vs *wire.VoiceSession) bool { return !vs.Priority })
	if grantOf(t, au).Priority || participantOf(au, alice.Identity.Public).Priority {
		t.Fatal("override didn't clear priority")
	}
	e.must(override(&wire.SetChannelOverride{ChannelId: vc, Member: alice.Identity.Public}))
	nextVoice(t, alice, func(vs *wire.VoiceSession) bool { return vs.Priority })

	// Removing the role clears it, for alice and in bob's view.
	e.must(assign(alice))
	au = nextVoice(t, alice, func(vs *wire.VoiceSession) bool { return !vs.Priority })
	if grantOf(t, au).Priority {
		t.Fatal("grant keeps priority after the role was removed")
	}
	nextVoice(t, bob, func(vs *wire.VoiceSession) bool {
		p := participantOf(vs, alice.Identity.Public)
		return p != nil && !p.Priority
	})
}
