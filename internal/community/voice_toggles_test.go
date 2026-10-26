package community_test

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Video and screen share off: a member who has Video and Screen Share
// joins without them (session and signed grant), turning them off mid-call
// takes them away, and asking for camera or screen doesn't show either.
func TestToggleVideoAndScreenShareOffInCalls(t *testing.T) {
	e := setup(t, nil)
	alice := e.member("Alice", e.invite.Secret)
	bob := e.member("Bob", e.invite.Secret)
	waitAttached(t, e)
	e.must(assign(alice, e.must(createRole("Streamer", community.PermVideo|community.PermScreenShare)).Id))
	vc := voiceChannel(e, "talk")
	as, err := voiceJoin(alice, vc)
	if err != nil {
		t.Fatal(err)
	}
	if g := grantOf(t, as); !as.CanVideo || !as.CanScreen || !g.Video || !g.Screen {
		t.Fatalf("with both on: %v grant %v", as, g)
	}
	// Turned off during the call: the next session and grant lack them.
	e.update(t, func(s *wire.CommunitySettings) { s.VideoEnabled = proto.Bool(false) })
	vs := nextVoice(t, alice, func(vs *wire.VoiceSession) bool { return !vs.CanVideo })
	if g := grantOf(t, vs); g.Video || !g.Screen || !vs.CanScreen {
		t.Fatalf("video off mid-call: %v grant %v", vs, g)
	}
	e.update(t, func(s *wire.CommunitySettings) { s.ScreenShareEnabled = proto.Bool(false) })
	vs = nextVoice(t, alice, func(vs *wire.VoiceSession) bool { return !vs.CanScreen })
	if g := grantOf(t, vs); g.Video || g.Screen || vs.CanVideo {
		t.Fatalf("screen share off mid-call: %v grant %v", vs, g)
	}
	// Joining while both are off: no video or screen in the grant, and
	// camera and screen requests don't reach the others.
	if err := voiceCall(alice, &wire.ClientMessage{Body: &wire.ClientMessage_VoiceLeave{VoiceLeave: &wire.VoiceLeave{ChannelId: vc}}}); err != nil {
		t.Fatal(err)
	}
	as, err = voiceJoin(alice, vc)
	if err != nil {
		t.Fatal(err)
	}
	if g := grantOf(t, as); as.CanVideo || as.CanScreen || g.Video || g.Screen || !g.Audio {
		t.Fatalf("joined with both off: %v grant %v", as, g)
	}
	if _, err := voiceJoin(bob, vc); err != nil {
		t.Fatal(err)
	}
	set := &wire.ClientMessage{Body: &wire.ClientMessage_VoiceState{VoiceState: &wire.VoiceState{ChannelId: vc, SelfDeaf: true, Camera: true, Screen: true}}}
	if err := voiceCall(alice, set); err != nil {
		t.Fatal(err)
	}
	bu := nextVoice(t, bob, func(vs *wire.VoiceSession) bool {
		p := participantOf(vs, alice.Identity.Public)
		return p != nil && p.SelfDeaf
	})
	if p := participantOf(bu, alice.Identity.Public); p.Camera || p.Screen {
		t.Fatalf("alice shown with camera or screen while they're off: %v", p)
	}
}
