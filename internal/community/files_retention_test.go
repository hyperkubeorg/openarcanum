package community_test

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

func protoMarshal(m proto.Message) ([]byte, error) { return proto.Marshal(m) }

func cloneSettings(s *wire.CommunitySettings) *wire.CommunitySettings {
	return proto.Clone(s).(*wire.CommunitySettings)
}

func upload(t *testing.T, m *testutil.Member, channelID int64, name string, data []byte) (*wire.Message, error) {
	t.Helper()
	ctx := testutil.Ctx(t)
	r, err := m.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_UploadBegin{UploadBegin: &wire.UploadBegin{
		ChannelId: channelID, Name: name, Size: uint64(len(data)), Mime: "application/octet-stream", Text: "a file",
	}}})
	if err != nil {
		return nil, err
	}
	st := r.GetUploadStarted()
	for off := 0; off < len(data); off += int(st.ChunkSize) {
		end := min(off+int(st.ChunkSize), len(data))
		if _, err := m.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_UploadChunk{UploadChunk: &wire.UploadChunk{
			UploadId: st.UploadId, Offset: uint64(off), Data: data[off:end],
		}}}); err != nil {
			return nil, err
		}
	}
	r, err = m.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_UploadFinish{UploadFinish: &wire.UploadFinish{UploadId: st.UploadId, Sha256: sum(data)}}})
	if err != nil {
		return nil, err
	}
	return r.GetMessage(), nil
}

func download(t *testing.T, m *testutil.Member, ref *wire.FileRef) ([]byte, error) {
	t.Helper()
	ctx := testutil.Ctx(t)
	aead, _ := chacha20poly1305.New(ref.Key)
	var out []byte
	for i := uint32(0); ; i++ {
		r, err := m.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_FileGet{FileGet: &wire.FileGet{FileId: ref.FileId, Chunk: i}}})
		if err != nil {
			return nil, err
		}
		fc := r.GetFileChunk()
		nonce := make([]byte, 12)
		nonce[8], nonce[9], nonce[10], nonce[11] = byte(i>>24), byte(i>>16), byte(i>>8), byte(i)
		aad := []byte("openarcanum/file-chunk/v1")
		aad = append(aad, be64(ref.FileId)...)
		aad = append(aad, byte(i>>24), byte(i>>16), byte(i>>8), byte(i))
		pt, err := aead.Open(nil, nonce, fc.Data, aad)
		if err != nil {
			return nil, err
		}
		out = append(out, pt...)
		if i+1 == fc.Chunks {
			return out, nil
		}
	}
}

func be64(v int64) []byte {
	b := make([]byte, 8)
	for i := 0; i < 8; i++ {
		b[i] = byte(uint64(v) >> (56 - 8*i))
	}
	return b
}

func TestFileUploadAndDownload(t *testing.T) {
	e := setup(t, nil)
	alice := e.member("Alice", e.invite.Secret)
	gen := alice.Channel("general")
	data := make([]byte, 3<<20+123)
	rand.Read(data)
	msg, err := upload(t, alice, gen.Id, "photo.bin", data)
	if err != nil {
		t.Fatal(err)
	}
	if firstFile(msg) == nil || firstFile(msg).Size != uint64(len(data)) || firstFile(msg).Name != "photo.bin" || len(firstFile(msg).Key) != 32 {
		t.Fatalf("bad file ref: %v", firstFile(msg))
	}
	got, err := download(t, e.owner, firstFile(msg))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("downloaded file differs")
	}
}

// An upload over the community's limit (10 MB unless raised, 25 MB at
// most) is refused up front, before any data is sent.
func TestUploadOverLimitRefused(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	gen := e.owner.Channel("general")
	begin := func(size uint64) error {
		_, err := e.owner.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_UploadBegin{UploadBegin: &wire.UploadBegin{
			ChannelId: gen.Id, Name: "file.iso", Size: size,
		}}})
		return err
	}
	if err := begin(10<<20 + 1); err == nil || !strings.Contains(err.Error(), "TOO_LARGE") || !strings.Contains(err.Error(), "10 MB") {
		t.Fatalf("10 MB + 1 byte in a new community: %v", err)
	}
	if err := begin(10 << 20); err != nil {
		t.Fatalf("exactly 10 MB refused: %v", err)
	}
	raised := cloneSettings(e.owner.State.Settings)
	raised.MaxFileSize = community.MaxFileSizeLimit
	if _, err := e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_UpdateSettings{UpdateSettings: raised}}); err != nil {
		t.Fatal(err)
	}
	if err := begin(25<<20 + 1); err == nil || !strings.Contains(err.Error(), "TOO_LARGE") {
		t.Fatalf("25 MB + 1 byte: %v", err)
	}
	if err := begin(25 << 20); err != nil {
		t.Fatalf("exactly 25 MB refused: %v", err)
	}
	s := cloneSettings(e.owner.State.Settings)
	s.MaxFileSize = 1 << 20
	if _, err := e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_UpdateSettings{UpdateSettings: s}}); err != nil {
		t.Fatal(err)
	}
	if _, err := upload(t, e.owner, gen.Id, "two.bin", make([]byte, 2<<20)); err == nil {
		t.Fatal("upload over the community's limit accepted")
	}
	// Lying about the size doesn't help.
	r, err := e.owner.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_UploadBegin{UploadBegin: &wire.UploadBegin{ChannelId: gen.Id, Name: "liar", Size: 10}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.owner.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_UploadChunk{UploadChunk: &wire.UploadChunk{
		UploadId: r.GetUploadStarted().UploadId, Data: make([]byte, 11),
	}}}); err == nil {
		t.Fatal("chunk beyond announced size accepted")
	}
}

func TestRetentionPurgeBoundaries(t *testing.T) {
	e := setup(t, nil) // 30 days messages, 7 days files
	ctx := testutil.Ctx(t)
	gen := e.owner.Channel("general")
	base := time.Now()
	clock := base
	e.comm.Server.SetClock(func() time.Time { return clock })
	if _, err := e.owner.Post(ctx, gen.Id, "old message"); err != nil {
		t.Fatal(err)
	}
	fileMsg, err := upload(t, e.owner, gen.Id, "f.txt", []byte("file body"))
	if err != nil {
		t.Fatal(err)
	}
	clock = base.Add(time.Millisecond)
	if _, err := e.owner.Post(ctx, gen.Id, "newer message"); err != nil {
		t.Fatal(err)
	}
	day := 24 * time.Hour
	srv := e.comm.Server

	// One millisecond before 7 days: the file stays.
	if _, f, _ := srv.Purge(ctx, base.Add(7*day-time.Millisecond)); f != 0 {
		t.Fatalf("file purged %d early", f)
	}
	// Exactly 7 days: the file goes, its message stays.
	if _, f, _ := srv.Purge(ctx, base.Add(7*day)); f != 1 {
		t.Fatalf("file not purged at 7 days (%d)", f)
	}
	if _, err := download(t, e.owner, firstFile(fileMsg)); err == nil {
		t.Fatal("purged file still downloadable")
	}
	// 30 days minus 1 ms: nothing.
	if m, _, _ := srv.Purge(ctx, base.Add(30*day-time.Millisecond)); m != 0 {
		t.Fatalf("messages purged early (%d)", m)
	}
	// Exactly 30 days after the first two messages: those two go, the newer stays.
	if m, _, _ := srv.Purge(ctx, base.Add(30*day)); m != 2 {
		t.Fatalf("purged %d messages at 30 days, want 2", m)
	}
	r, _ := e.owner.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_History{History: &wire.History{ChannelId: gen.Id}}})
	if msgs := r.GetMessages().Messages; len(msgs) != 1 || msgs[0].Text != "newer message" {
		t.Fatalf("history after purge: %v", msgs)
	}

	// A per-channel override shorter than the community setting wins.
	one := uint32(1)
	if _, err := e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_EditChannel{EditChannel: &wire.EditChannel{
		Id: gen.Id, Position: gen.Position, MessageRetentionDays: &one,
	}}}); err != nil {
		t.Fatal(err)
	}
	if m, _, _ := srv.Purge(ctx, base.Add(day+time.Millisecond)); m != 1 {
		t.Fatalf("channel override not applied (%d)", m)
	}
	// Retention 0 keeps forever.
	zero := uint32(0)
	e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_EditChannel{EditChannel: &wire.EditChannel{Id: gen.Id, Position: gen.Position, MessageRetentionDays: &zero}}})
	clock = base
	e.owner.Post(ctx, gen.Id, "forever")
	if m, _, _ := srv.Purge(ctx, base.Add(1000*day)); m != 0 {
		t.Fatal("retention 0 purged a message")
	}
}

// Search returns only messages from channels the searcher can read, and stops
// working when the community turns it off.
func TestSearchRespectsPermissions(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	res, _ := e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: "secret"}}})
	e.owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_SetChannelOverride{SetChannelOverride: &wire.SetChannelOverride{
		ChannelId: res.Id, RoleId: 1, Deny: community.PermViewChannel,
	}}})
	gen := e.owner.Channel("general")
	e.owner.Post(ctx, gen.Id, "the pineapple pizza debate")
	e.owner.Post(ctx, res.Id, "pineapple plans are secret")
	alice.Post(ctx, gen.Id, "unrelated")

	search := func(m *testutil.Member, q string) ([]*wire.Message, error) {
		r, err := m.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_Search{Search: &wire.SearchQuery{Query: q}}})
		if err != nil {
			return nil, err
		}
		return r.GetSearchResults().Messages, nil
	}
	got, err := search(alice, "pineapple")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ChannelId != gen.Id {
		t.Fatalf("alice's search returned %d results (%v)", len(got), got)
	}
	if got, _ := search(e.owner, "pinea"); len(got) != 2 {
		t.Fatalf("owner prefix search returned %d", len(got))
	}
	if got, _ := search(alice, `") OR "*`); len(got) != 0 {
		t.Fatal("query injection matched something")
	}
	// Search survives a restart (index rebuilt from encrypted storage).
	e.comm.Restart(t, e.relay.URL)
	again := testutil.NewMember(t, "Alice")
	again.Identity, again.Static = alice.Identity, alice.Static
	if err := again.Connect(ctx, e.relay.URL, e.serverKey()); err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if got, _ := search(again, "pineapple"); len(got) != 1 {
		t.Fatalf("after restart: %d results", len(got))
	}
	// Turning search off.
	e.owner.Close()
	owner := testutil.NewMember(t, "Owner")
	owner.Identity, owner.Static = e.owner.Identity, e.owner.Static
	owner.Connect(ctx, e.relay.URL, e.serverKey())
	defer owner.Close()
	owner.Sync(ctx)
	s := cloneSettings(owner.State.Settings)
	s.SearchEnabled = false
	if _, err := owner.Admin(ctx, e.id(), &wire.AdminAction{Op: &wire.AdminAction_UpdateSettings{UpdateSettings: s}}); err != nil {
		t.Fatal(err)
	}
	if _, err := search(again, "pineapple"); err == nil || !strings.Contains(err.Error(), "DISABLED") {
		t.Fatalf("search while disabled: %v", err)
	}
}

func TestEditDeleteAndHistoryPaging(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	gen := alice.Channel("general")
	m, _ := alice.Post(ctx, gen.Id, "tpyo")
	if _, err := e.owner.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_Edit{Edit: &wire.Edit{MessageId: m.Id, Text: "hijack"}}}); err == nil {
		t.Fatal("edited someone else's message")
	}
	r, err := alice.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_Edit{Edit: &wire.Edit{MessageId: m.Id, Text: "typo"}}})
	if err != nil || r.GetMessage().Text != "typo" || r.GetMessage().EditedAt == 0 {
		t.Fatalf("edit: %v %v", r, err)
	}
	// Admins (ManageMessages) may delete others' messages.
	if _, err := e.owner.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_Delete{Delete: &wire.DeleteMessage{MessageId: m.Id}}}); err != nil {
		t.Fatal(err)
	}
	h, _ := alice.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_History{History: &wire.History{ChannelId: gen.Id}}})
	if msgs := userMessages(h.GetMessages().Messages); len(msgs) != 1 || !msgs[0].Deleted || msgs[0].Text != "" {
		t.Fatalf("deleted message in history: %v", msgs)
	}
	for i := 0; i < 5; i++ {
		alice.Post(ctx, gen.Id, fmt.Sprintf("p%d", i))
	}
	h, _ = alice.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_History{History: &wire.History{ChannelId: gen.Id, AfterId: m.Id, Limit: 2}}})
	if msgs := h.GetMessages().Messages; len(msgs) != 2 || msgs[0].Text != "p0" || !h.GetMessages().More {
		t.Fatalf("after-id page: %v", msgs)
	}
	if _, err := alice.Post(ctx, gen.Id, strings.Repeat("x", 4001)); err == nil {
		t.Fatal("overlong message accepted")
	}
	if _, err := alice.Post(ctx, gen.Id, "   "); err == nil {
		t.Fatal("blank message accepted")
	}
}

func TestBackupAndRestoreRoundTrip(t *testing.T) {
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	gen := e.owner.Channel("general")
	e.owner.Post(ctx, gen.Id, "remember me")
	path, err := e.comm.Server.Backup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg := community.DefaultConfig(dir)
	cfg.Relays = []string{e.relay.URL}
	if _, err := community.Restore(ctx, cfg, path, e.comm.Dir+"/storage.key"); err != nil {
		t.Fatal(err)
	}
	// The wrong storage key can't open it.
	other := t.TempDir() + "/k"
	writeKey(t, other)
	cfg2 := community.DefaultConfig(t.TempDir())
	cfg2.Relays = cfg.Relays
	if _, err := community.Restore(ctx, cfg2, path, other); err == nil {
		t.Fatal("backup opened with the wrong storage key")
	}
}

func writeKey(t *testing.T, path string) {
	b := make([]byte, 32)
	rand.Read(b)
	if err := writeHex(path, b); err != nil {
		t.Fatal(err)
	}
}

func writeHex(path string, b []byte) error {
	return os.WriteFile(path, []byte(hex.EncodeToString(b)+"\n"), 0o600)
}

// firstFile is a message's first attachment, or nil.
func firstFile(m *wire.Message) *wire.FileRef {
	if m == nil || len(m.Files) == 0 {
		return nil
	}
	return m.Files[0]
}

// userMessages drops join/leave system messages (on by default).
func userMessages(msgs []*wire.Message) []*wire.Message {
	var out []*wire.Message
	for _, m := range msgs {
		if m.System == nil {
			out = append(out, m)
		}
	}
	return out
}
