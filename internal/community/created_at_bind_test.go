package community_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// messages.created_at decides what new members see (and what a ban's
// history deletion covers), so it is bound to the sealed body: a message
// made to look newer on disk is reported, not shown to a member who joined
// after it.
func TestTamperedCreatedAtIsRefused(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	gen := e.owner.Channel("general").Id
	post(t, e.owner, gen, "from before you joined")
	e.update(t, func(s *wire.CommunitySettings) { s.NewMembersSeeHistory = proto.Bool(false) })
	time.Sleep(5 * time.Millisecond)
	late := e.member("Late", e.invite.Secret)
	if got, err := history(t, late, gen); err != nil || len(got) != 0 {
		t.Fatalf("history before tampering: %q %v", got, err)
	}
	late.Close()
	e.owner.Close()
	e.comm.Stop()
	dir := e.comm.Dir
	pristine, err := os.ReadFile(filepath.Join(dir, "community.db"))
	if err != nil {
		t.Fatal(err)
	}

	// Moved a day into the future, past the late member's join time.
	execRaw(t, dir, `UPDATE messages SET created_at = created_at + 86400000`)
	srv := reopen(t, dir, e.relay.URL, testutil.Quiet())
	var te *community.TamperError
	if _, err := srv.Messages(context.Background(), gen); !errors.As(err, &te) || te.Table != "messages" || te.Column != "created_at" {
		t.Fatalf("tampered created_at: %v", err)
	}
	srv.Close()
	e.comm.Restart(t, e.relay.URL)
	m := testutil.NewMember(t, "Late")
	m.Identity, m.Static = late.Identity, late.Static
	if err := m.Connect(ctx, e.relay.URL, e.serverKey()); err != nil {
		t.Fatal(err)
	}
	if got, err := history(t, m, gen); err == nil {
		t.Fatalf("history with a tampered created_at: %q", got)
	}
	m.Close()
	e.comm.Stop()

	// Restored, it reads again.
	for _, f := range []string{"community.db-wal", "community.db-shm"} {
		os.Remove(filepath.Join(dir, f))
	}
	if err := os.WriteFile(filepath.Join(dir, "community.db"), pristine, 0o600); err != nil {
		t.Fatal(err)
	}
	srv = reopen(t, dir, e.relay.URL, testutil.Quiet())
	if msgs, err := srv.Messages(context.Background(), gen); err != nil || len(msgs) == 0 {
		t.Fatalf("untouched messages: %v %v", msgs, err)
	}
}
