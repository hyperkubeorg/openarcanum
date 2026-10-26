package community_test

import (
	"sync"
	"testing"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Unfinished uploads are held in memory, so what may be in flight is
// bounded per member and on the server, and an upload that stops sending
// chunks gives its room back. A count of uploads per member alone would
// still let every member hold several times the largest file.
func TestUploadsInFlightBounded(t *testing.T) {
	oldMember, oldAll := community.MaxUploadBytesPerMember, community.MaxUploadBytesInFlight
	community.MaxUploadBytesPerMember, community.MaxUploadBytesInFlight = 3<<20, 5<<20
	t.Cleanup(func() { community.MaxUploadBytesPerMember, community.MaxUploadBytesInFlight = oldMember, oldAll })
	e := setupWithConfig(t, largeFiles(), testutil.Quiet(), nil)
	var mu sync.Mutex
	now := time.Now()
	e.comm.Server.SetClock(func() time.Time { mu.Lock(); defer mu.Unlock(); return now })
	gen := e.owner.Channel("general").Id
	alice := e.member("Alice", e.invite.Secret)
	bob := e.member("Bob", e.invite.Secret)

	if _, err := beginUpload(t, alice, gen, 2<<20); err != nil {
		t.Fatal(err)
	}
	_, err := beginUpload(t, alice, gen, 2<<20)
	wantCode(t, "over the member's in-flight bound", err, wire.RpcErrorCode_RPC_ERROR_CODE_RATE_LIMITED)
	if _, err := beginUpload(t, bob, gen, 3<<20); err != nil {
		t.Fatal(err)
	}
	_, err = beginUpload(t, e.owner, gen, 1<<20)
	wantErr(t, err, "the server is busy with other uploads")
	// Idle uploads expire and free their room.
	mu.Lock()
	now = now.Add(community.UploadIdleTimeout + time.Second)
	mu.Unlock()
	if _, err := beginUpload(t, e.owner, gen, 1<<20); err != nil {
		t.Fatalf("after idle uploads expired: %v", err)
	}
}
