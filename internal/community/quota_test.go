package community_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Quotas (docs/operations.md "Quotas").

// beginUpload starts an upload of size bytes and returns its id; the
// quotas are charged and checked when an upload begins.
func beginUpload(t *testing.T, m *testutil.Member, channelID int64, size uint64) (int64, error) {
	t.Helper()
	r, err := m.Call(testutil.Ctx(t), &wire.ClientMessage{Body: &wire.ClientMessage_UploadBegin{UploadBegin: &wire.UploadBegin{
		ChannelId: channelID, Name: "big.bin", Size: size, Mime: "application/octet-stream",
	}}})
	if err != nil {
		return 0, err
	}
	return r.GetUploadStarted().UploadId, nil
}

func TestQuotaConfig(t *testing.T) {
	cfg := community.DefaultConfig(t.TempDir())
	cfg.Relays = []string{"ws://127.0.0.1:1"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if cfg.UploadQuotaPerMember != 1<<30 || cfg.FileStoreMax != 20<<30 {
		t.Fatalf("defaults: quota %d, store %d", cfg.UploadQuotaPerMember, cfg.FileStoreMax)
	}
	for _, mut := range []func(*community.Config){
		func(c *community.Config) { c.UploadQuotaPerMember = 0 },
		func(c *community.Config) { c.UploadQuotaPerMember = community.MaxFileSizeLimit - 1 },
		func(c *community.Config) { c.FileStoreMax = 0 },
		func(c *community.Config) { c.FileStoreMax = -1 },
	} {
		c := cfg
		mut(&c)
		if err := c.Validate(); err == nil {
			t.Fatalf("accepted quota %d, store %d", c.UploadQuotaPerMember, c.FileStoreMax)
		}
	}
}

// A community that allows the largest files (25 MB), as the quota tests need.
func largeFiles() *wire.CommunitySettings {
	return &wire.CommunitySettings{Name: "Test", Visibility: wire.Visibility_VISIBILITY_PRIVATE, JoinPolicy: wire.JoinPolicy_JOIN_POLICY_INVITE_ONLY,
		MessageRetentionDays: 30, FileRetentionDays: 7, SearchEnabled: true, MaxFileSize: community.MaxFileSizeLimit}
}

func TestUploadQuotaPerMember(t *testing.T) {
	e := setupWithConfig(t, largeFiles(), testutil.Quiet(), func(c *community.Config) { c.UploadQuotaPerMember = community.MaxFileSizeLimit })
	var mu sync.Mutex
	now := time.Now()
	e.comm.Server.SetClock(func() time.Time { mu.Lock(); defer mu.Unlock(); return now })
	gen := e.owner.Channel("general").Id
	alice := e.member("Alice", e.invite.Secret)
	bob := e.member("Bob", e.invite.Secret)

	// A finished upload counts, and so does one only begun.
	if _, err := upload(t, alice, gen, "small.bin", make([]byte, 4<<20)); err != nil {
		t.Fatal(err)
	}
	if _, err := beginUpload(t, alice, gen, 16<<20); err != nil {
		t.Fatal(err)
	}
	_, err := beginUpload(t, alice, gen, 6<<20)
	wantCode(t, "upload over the member's quota", err, wire.RpcErrorCode_RPC_ERROR_CODE_RATE_LIMITED)
	wantErr(t, err, "you've uploaded 20 MB in the last 24 hours; this community allows 25 MB per member per day")
	// Up to the quota exactly is fine.
	if _, err := beginUpload(t, alice, gen, 5<<20); err != nil {
		t.Fatalf("upload up to the quota: %v", err)
	}
	_, err = beginUpload(t, alice, gen, 1)
	wantErr(t, err, "you've uploaded 25 MB in the last 24 hours")
	// The quota is per member.
	if _, err := beginUpload(t, bob, gen, 6<<20); err != nil {
		t.Fatalf("another member blocked by alice's quota: %v", err)
	}
	// The window rolls: a day later alice may upload again.
	mu.Lock()
	now = now.Add(community.UploadQuotaWindow + time.Second)
	mu.Unlock()
	if _, err := beginUpload(t, alice, gen, 6<<20); err != nil {
		t.Fatalf("upload a day later: %v", err)
	}
}

func TestFileStoreFull(t *testing.T) {
	e := setupWithConfig(t, largeFiles(), testutil.Quiet(), func(c *community.Config) { c.FileStoreMax = community.MaxFileSizeLimit })
	gen := e.owner.Channel("general").Id
	alice := e.member("Alice", e.invite.Secret)
	bob := e.member("Bob", e.invite.Secret)

	m, err := upload(t, alice, gen, "big.bin", make([]byte, 20<<20))
	if err != nil {
		t.Fatal(err)
	}
	_, err = beginUpload(t, bob, gen, 6<<20)
	wantCode(t, "upload into a full store", err, wire.RpcErrorCode_RPC_ERROR_CODE_TOO_LARGE)
	wantErr(t, err, "this community's file storage is full")
	// What fits still goes in, and uploads in progress hold their room.
	if _, err := beginUpload(t, bob, gen, 4<<20); err != nil {
		t.Fatalf("upload that fits: %v", err)
	}
	_, err = beginUpload(t, alice, gen, 1<<20)
	wantErr(t, err, "this community's file storage is full")
	// Deleting the big file frees its room.
	if _, err := call(t, alice, &wire.DeleteMessage{MessageId: m.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := beginUpload(t, alice, gen, 6<<20); err != nil {
		t.Fatalf("upload after a delete freed room: %v", err)
	}
}

// ---------------------------------------------------------------- count limits

func TestRoleLimit(t *testing.T) {
	e := setup(t, nil)
	for i := len(fresh(t, e.owner).Roles); i < community.MaxRoles; i++ {
		e.must(createRole(fmt.Sprintf("role %d", i), 0))
	}
	_, err := e.act(e.owner, createRole("one too many", 0))
	wantErr(t, err, fmt.Sprintf("a community has at most %d roles", community.MaxRoles))
}

func TestChannelLimit(t *testing.T) {
	e := setup(t, nil)
	for i := len(fresh(t, e.owner).Channels); i < community.MaxChannels; i++ {
		e.must(&wire.AdminAction{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: fmt.Sprintf("c%d", i)}}})
	}
	_, err := e.act(e.owner, &wire.AdminAction{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: "one-too-many"}}})
	wantErr(t, err, fmt.Sprintf("a community has at most %d channels", community.MaxChannels))
}

func TestCategoryLimit(t *testing.T) {
	e := setup(t, nil)
	for i := len(fresh(t, e.owner).Categories); i < community.MaxCategories; i++ {
		e.must(createCategory(fmt.Sprintf("cat %d", i)))
	}
	_, err := e.act(e.owner, createCategory("one too many"))
	wantErr(t, err, fmt.Sprintf("a community has at most %d categories", community.MaxCategories))
}
