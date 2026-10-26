package community_test

import (
	"crypto/rand"
	"testing"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

func (e *env) update(t *testing.T, mut func(*wire.CommunitySettings)) {
	t.Helper()
	ctx := testutil.Ctx(t)
	if err := e.owner.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	s := cloneSettings(e.owner.State.Settings)
	mut(s)
	if _, err := e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_UpdateSettings{UpdateSettings: s}}); err != nil {
		t.Fatal(err)
	}
}

func TestMaxMembers(t *testing.T) {
	e := setup(t, nil)
	e.update(t, func(s *wire.CommunitySettings) { s.MaxMembers = 2 })
	e.member("Two", e.invite.Secret)
	three := e.member("Three", "")
	r, err := three.Join(testutil.Ctx(t), e.invite.Secret)
	if err != nil || r.Reason != wire.RefusalReason_REFUSAL_REASON_FULL {
		t.Fatalf("join over the limit: %v %v", r, err)
	}
}

func TestAuditVisibilityEveryone(t *testing.T) {
	e := setup(t, nil)
	alice := e.member("Alice", e.invite.Secret)
	ctx := testutil.Ctx(t)
	q := &wire.ClientMessage{Body: &wire.ClientMessage_AuditLog{AuditLog: &wire.AuditQuery{}}}
	if _, err := alice.Call(ctx, q); err == nil {
		t.Fatal("member read the admin-only audit log")
	}
	e.update(t, func(s *wire.CommunitySettings) { s.AuditVisibility = wire.AuditVisibility_AUDIT_VISIBILITY_EVERYONE })
	r, err := alice.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_AuditLog{AuditLog: &wire.AuditQuery{}}})
	if err != nil || len(r.GetAudit().Entries) == 0 {
		t.Fatalf("audit log for everyone: %v %v", r, err)
	}
}

func TestFileBandwidthLimit(t *testing.T) {
	e := setup(t, nil)
	data := make([]byte, 2<<20)
	rand.Read(data)
	gen := e.owner.Channel("general")
	msg, err := upload(t, e.owner, gen.Id, "f.bin", data)
	if err != nil {
		t.Fatal(err)
	}
	e.update(t, func(s *wire.CommunitySettings) { s.FileBandwidthLimit = 1 << 20 }) // 1 MiB/s, 1 MiB burst
	start := time.Now()
	if _, err := download(t, e.owner, firstFile(msg)); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < 800*time.Millisecond {
		t.Fatalf("2 MiB at 1 MiB/s took %v", d)
	}
	e.update(t, func(s *wire.CommunitySettings) { s.FileBandwidthLimit = 0 })
	start = time.Now()
	download(t, e.owner, firstFile(msg))
	if d := time.Since(start); d > 800*time.Millisecond {
		t.Fatalf("unlimited download took %v", d)
	}
	bad := cloneSettings(e.owner.State.Settings)
	bad.FileBandwidthLimit = 100
	if _, err := e.owner.Admin(testutil.Ctx(t), e.id(), &wire.AdminAction{Op: &wire.AdminAction_UpdateSettings{UpdateSettings: bad}}); err == nil {
		t.Fatal("bandwidth limit under 16 KB/s accepted")
	}
}

// The community's pinned relay list overrides the node's configured relays.
func TestPinnedRelaysSetting(t *testing.T) {
	e := setup(t, nil)
	other := testutil.StartRelay(t)
	e.update(t, func(s *wire.CommunitySettings) {
		s.RelayMode = wire.RelayMode_RELAY_MODE_PINNED
		s.PinnedRelays = []string{other.URL}
	})
	waitFor(t, "move to the pinned relay", func() bool {
		r := e.comm.Server.AttachedRelays()
		return len(r) == 1 && r[0] == other.URL
	})
}
