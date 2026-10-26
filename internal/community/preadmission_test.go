package community_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/session"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// session.Recv reassembles a message before the request limiter sees it,
// so a peer that isn't an active member is held to
// session.PreAdmissionMaxMessage: otherwise anyone could pin 32 MiB of
// server memory per session. Members keep the full size.
func TestPreAdmissionMessageCap(t *testing.T) {
	r := testutil.StartRelay(t)
	comm := testutil.StartCommunity(t, nil, r.URL)
	e := claimedOn(t, r, comm, nil)
	ctx := testutil.Ctx(t)

	stranger := testutil.NewMember(t, "stranger")
	if err := stranger.Connect(ctx, r.URL, e.serverKey()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stranger.Close)
	big := strings.Repeat("x", session.PreAdmissionMaxMessage+1)
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := stranger.Call(cctx, &wire.ClientMessage{Body: &wire.ClientMessage_Join{Join: &wire.Join{DisplayName: big}}})
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("an oversized message from a non-member: %v", err)
	}

	// A member's session takes the same message (and refuses the request
	// itself, as too large a name), and stays up.
	a := e.member("A", e.invite.Secret)
	_, err = a.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_Join{Join: &wire.Join{DisplayName: big}}})
	if err != nil && strings.Contains(err.Error(), "closed") {
		t.Fatalf("member's session closed on a large message: %v", err)
	}
	if err := a.Sync(ctx); err != nil {
		t.Fatalf("member's session after a large message: %v", err)
	}
}
