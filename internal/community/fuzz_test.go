package community_test

import (
	"context"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Fuzz targets for the community server's request handling: every
// ClientMessage a member, a stranger or the owner can send after the Noise
// handshake (claim, join, posts, uploads, contact cards, voice, ...), and
// every admin action an owner can sign. They run against a real server and
// database through community.DetachedSession (fuzz_export_test.go), which is
// serveStream's loop body without the transport. The invariants: no panic,
// a reply for every well-formed request, and no internal error, because
// malformed or hostile input is always the client's error.

type fuzzCommunity struct {
	srv      *community.Server
	owner    *identity.Key // community owner key (signs server certs)
	member   *identity.Key // the owner's member identity (signs admin actions)
	ownerS   *community.DetachedSession
	stranger *community.DetachedSession
	joinKey  string
}

func randKey() []byte {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return b
}

func startFuzzCommunity(f *testing.F) *fuzzCommunity {
	ctx := context.Background()
	cfg := community.DefaultConfig(f.TempDir())
	cfg.BackupInterval.Duration = 0
	cfg.Relays = []string{"ws://relay.invalid/ws"} // never dialled: the server isn't Run
	res, err := community.Init(ctx, cfg)
	if err != nil {
		f.Fatal(err)
	}
	b, err := identity.Decode(strings.TrimPrefix(res.ClaimCode, community.ClaimPrefix))
	if err != nil {
		f.Fatal(err)
	}
	var code wire.ClaimCode
	if err := proto.Unmarshal(b, &code); err != nil {
		f.Fatal(err)
	}
	// No Run: the server never attaches to relays, so channel events queue
	// and drop instead of being published.
	srv, err := community.Open(ctx, cfg, testutil.Quiet())
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { srv.Close() })

	fc := &fuzzCommunity{srv: srv}
	fc.owner, _ = identity.Generate()
	fc.member, _ = identity.Generate()
	fc.ownerS = srv.NewDetachedSession(ctx, fc.member.Public, randKey(), "owner device")
	f.Cleanup(fc.ownerS.Close)
	cert, err := identity.IssueServerCert(fc.owner, code.ServerKey, 1, nil, nil)
	if err != nil {
		f.Fatal(err)
	}
	r := fc.ownerS.Handle(ctx, mustMarshal(&wire.ClientMessage{Id: 1, Body: &wire.ClientMessage_Claim{Claim: &wire.Claim{
		Secret: code.Secret, Cert: cert, DisplayName: "Owner",
		Settings: &wire.CommunitySettings{Name: "Fuzz", SearchEnabled: true},
	}}}))
	if r.GetClaimResult() == nil {
		f.Fatalf("claim: %v", r)
	}
	fc.joinKey = r.GetClaimResult().GetFirstInvite().GetSecret()
	// The harness itself must reach member-only paths.
	post := fc.ownerS.Handle(ctx, mustMarshal(&wire.ClientMessage{Id: 2, Body: &wire.ClientMessage_Post{Post: &wire.Post{ChannelId: 1, Text: "hi"}}}))
	if fc.ownerS.Status() != wire.MemberStatus_MEMBER_STATUS_ACTIVE || post.GetError() != nil || fc.joinKey == "" {
		f.Fatalf("owner session not usable: status %v, post %v, join key %q", fc.ownerS.Status(), post, fc.joinKey)
	}
	stranger, _ := identity.Generate()
	fc.stranger = srv.NewDetachedSession(ctx, stranger.Public, randKey(), "stranger")
	f.Cleanup(fc.stranger.Close)
	return fc
}

func mustMarshal(m proto.Message) []byte {
	b, err := proto.Marshal(m)
	if err != nil {
		panic(err)
	}
	return b
}

func checkReply(t *testing.T, id uint64, r *wire.ServerMessage) {
	t.Helper()
	if r == nil {
		t.Fatal("no reply")
	}
	if id != 0 && r.ReplyTo != id {
		t.Fatalf("reply_to %d, want %d", r.ReplyTo, id)
	}
	if e := r.GetError(); e != nil && e.Code == wire.RpcErrorCode_RPC_ERROR_CODE_INTERNAL {
		t.Fatalf("internal error for client input: %s", e.Message)
	}
}

func clientSeeds(joinKey string) []*wire.ClientMessage {
	b := func(m *wire.ClientMessage) *wire.ClientMessage { m.Id = 7; return m }
	return []*wire.ClientMessage{
		b(&wire.ClientMessage{Body: &wire.ClientMessage_GetInfo{GetInfo: &wire.GetInfo{}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_Join{Join: &wire.Join{JoinKey: joinKey, DisplayName: "Newcomer"}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_Join{Join: &wire.Join{JoinKey: "openarcanum-invite:AAAA", DisplayName: "x"}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_CheckStatus{CheckStatus: &wire.CheckStatus{}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_Sync{Sync: &wire.Sync{}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_Post{Post: &wire.Post{ChannelId: 1, Text: "hello"}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_Post{Post: &wire.Post{ChannelId: 1, Text: "re", ReplyTo: 1}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_History{History: &wire.History{ChannelId: 1, Limit: 50}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_Edit{Edit: &wire.Edit{MessageId: 1, Text: "edited"}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_Delete{Delete: &wire.DeleteMessage{MessageId: 1}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_Search{Search: &wire.SearchQuery{Query: "hello OR \"x\" NEAR(a b) *", Limit: 5}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_SetProfile{SetProfile: &wire.SetProfile{DisplayName: "Owner 2", Avatar: []byte{0x89, 'P', 'N', 'G'}}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_GetAvatar{GetAvatar: &wire.GetAvatar{Member: make([]byte, 32)}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_UploadBegin{UploadBegin: &wire.UploadBegin{ChannelId: 1, Name: "a.txt", Size: 5, Mime: "text/plain"}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_UploadChunk{UploadChunk: &wire.UploadChunk{UploadId: 1, Data: []byte("hello")}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_UploadFinish{UploadFinish: &wire.UploadFinish{UploadId: 1, Sha256: make([]byte, 32)}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_FileGet{FileGet: &wire.FileGet{FileId: 1}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_ListMembers{ListMembers: &wire.ListMembers{}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_ListJoinKeys{ListJoinKeys: &wire.ListJoinKeys{}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_ListPending{ListPending: &wire.ListPending{}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_ListBans{ListBans: &wire.ListBans{}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_AuditLog{AuditLog: &wire.AuditQuery{Limit: 10}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_ListDevices{ListDevices: &wire.ListDevices{}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_RevokeDevice{RevokeDevice: &wire.SignedDeviceRevocation{Revocation: []byte{1}, Signature: make([]byte, 64)}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_PublishContactCard{PublishContactCard: &wire.PublishContactCard{
			Card: []byte{1, 2}, Devices: [][]byte{make([]byte, 32)}, KeyPackages: []*wire.KeyPackage{{Device: make([]byte, 32), Package: []byte{3}}},
		}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_AddKeyPackages{AddKeyPackages: &wire.AddKeyPackages{KeyPackages: []*wire.KeyPackage{{Device: make([]byte, 32)}}}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_GetContactCard{GetContactCard: &wire.GetContactCard{Member: make([]byte, 32)}}}),
		// Statuses (status.go): opaque entries, blobs and key drops.
		b(&wire.ClientMessage{Body: &wire.ClientMessage_SetStatusDevice{SetStatusDevice: &wire.SetStatusDevice{Entry: []byte{0x0a, 0x02, 0x0a, 0x00}, Device: make([]byte, 32)}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_SetStatus{SetStatus: &wire.SetStatus{Blob: []byte("FDM2\x12\x00"), Invisible: true}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_ListStatuses{ListStatuses: &wire.ListStatuses{After: []byte{1}}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_SendStatusKeys{SendStatusKeys: &wire.SendStatusKeys{Drops: []*wire.StatusKeyDrop{{Member: make([]byte, 32), Device: []byte{1}, Envelope: []byte{2}}}}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_FetchStatusKeys{FetchStatusKeys: &wire.FetchStatusKeys{Ack: []uint64{1, 2}, Device: []byte{1}}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_RequestStatusKey{RequestStatusKey: &wire.RequestStatusKey{Member: make([]byte, 32), Device: []byte{1}}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_VoiceJoin{VoiceJoin: &wire.VoiceJoin{ChannelId: 1}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_VoiceLeave{VoiceLeave: &wire.VoiceLeave{ChannelId: 1}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_Admin{Admin: &wire.SignedAction{Action: []byte{1}, Signature: make([]byte, 64)}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_Claim{Claim: &wire.Claim{Secret: []byte("x"), DisplayName: "Thief"}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_AdoptServer{AdoptServer: &wire.AdoptServer{}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_React{React: &wire.React{MessageId: 1, Emoji: "👍"}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_React{React: &wire.React{MessageId: 1, Emoji: ":blob:42", Remove: true}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_Pin{Pin: &wire.Pin{MessageId: 1}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_ListPins{ListPins: &wire.ListPins{ChannelId: 1}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_Typing{Typing: &wire.Typing{ChannelId: 1}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_UpdateProfile{UpdateProfile: &wire.UpdateProfile{About: "hi", Pronouns: "they/them"}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_Post{Post: &wire.Post{ChannelId: 1, Text: "@everyone <@&2> <@AAAA> https://example.invalid/", FileIds: []int64{1, 2}}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_UploadBegin{UploadBegin: &wire.UploadBegin{ChannelId: 1, Name: "a.png", Size: 5, Mime: "image/png", Staged: true}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_ReportMessage{ReportMessage: &wire.ReportMessage{MessageId: 1, Reason: "rude"}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_ListReports{ListReports: &wire.ListReports{IncludeClosed: true, Limit: 3}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_ListAutomodRules{ListAutomodRules: &wire.ListAutoModRules{}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_GetEmoji{GetEmoji: &wire.GetEmoji{Id: 1}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_EmojiUpload{EmojiUpload: &wire.EmojiUpload{
			Action: &wire.SignedAction{Action: []byte{1}, Signature: make([]byte, 64)}, Image: []byte("GIF89a"),
		}}}),
		b(&wire.ClientMessage{Body: &wire.ClientMessage_AuditLog{AuditLog: &wire.AuditQuery{Action: "ban", Actor: make([]byte, 32), Since: 1, Until: 2}}}),
	}
}

// FuzzClientMessage sends arbitrary request bytes as the owner or as a
// stranger (no membership yet: only claim, join, info and status apply).
func FuzzClientMessage(f *testing.F) {
	fc := startFuzzCommunity(f)
	for i, m := range clientSeeds(fc.joinKey) {
		f.Add(mustMarshal(m), i%3 == 0)
	}
	f.Add([]byte{}, false)
	f.Add([]byte{0x08, 0x00, 0x12, 0x00}, true)
	ctx := context.Background()
	f.Fuzz(func(t *testing.T, raw []byte, asStranger bool) {
		s := fc.ownerS
		if asStranger {
			s = fc.stranger
		}
		var m wire.ClientMessage
		id := uint64(0)
		if proto.Unmarshal(raw, &m) == nil {
			id = m.Id
		}
		checkReply(t, id, s.Handle(ctx, raw))
	})
}

// FuzzAdminAction signs arbitrary admin action bodies with the owner's member
// key (fresh nonce and timestamp, right community), so every operation's
// validation and storage sees hostile content from a fully authorised actor.
func FuzzAdminAction(f *testing.F) {
	fc := startFuzzCommunity(f)
	ops := []*wire.AdminAction{
		{Op: &wire.AdminAction_UpdateSettings{UpdateSettings: &wire.CommunitySettings{Name: "Renamed", MaxMembers: 3, Tags: []string{"a", "b"}}}},
		{Op: &wire.AdminAction_CreateJoinKey{CreateJoinKey: &wire.CreateJoinKey{Label: "k", MaxUses: 2, RoleIds: []int64{2, 99}}}},
		{Op: &wire.AdminAction_RevokeJoinKey{RevokeJoinKey: &wire.RevokeJoinKey{Id: 1}}},
		{Op: &wire.AdminAction_CreateChannel{CreateChannel: &wire.CreateChannel{Name: "new", Kind: wire.ChannelKind_CHANNEL_KIND_VOICE}}},
		{Op: &wire.AdminAction_EditChannel{EditChannel: &wire.EditChannel{Id: 1, Name: "renamed", Position: -3}}},
		{Op: &wire.AdminAction_DeleteChannel{DeleteChannel: &wire.DeleteChannel{Id: 2}}},
		{Op: &wire.AdminAction_CreateRole{CreateRole: &wire.CreateRole{Name: "mods", Permissions: ^uint64(0), Color: 0xffffff}}},
		{Op: &wire.AdminAction_AssignRoles{AssignRoles: &wire.AssignRoles{Member: fc.member.Public, RoleIds: []int64{1, 2, 3}}}},
		{Op: &wire.AdminAction_Ban{Ban: &wire.BanMember{Member: randKey(), DeleteMessageSeconds: 86400}}},
		{Op: &wire.AdminAction_Timeout{Timeout: &wire.TimeoutMember{Member: fc.member.Public, Seconds: 60}}},
		{Op: &wire.AdminAction_BulkDelete{BulkDelete: &wire.BulkDelete{ChannelId: 1, Last: 5}}},
		{Op: &wire.AdminAction_BulkDelete{BulkDelete: &wire.BulkDelete{ChannelId: 1, MessageIds: []int64{1, 2, 99}}}},
		{Op: &wire.AdminAction_SaveAutomodRule{SaveAutomodRule: &wire.SaveAutoModRule{Rule: &wire.AutoModRule{
			Name: "r", Enabled: true, Trigger: wire.AutoModTrigger_AUTOMOD_TRIGGER_KEYWORD, Keywords: []string{"x*"}, Patterns: []string{"a+b"}, Block: true,
		}}}},
		{Op: &wire.AdminAction_SaveAutomodRule{SaveAutomodRule: &wire.SaveAutoModRule{Rule: &wire.AutoModRule{
			Name: "l", Trigger: wire.AutoModTrigger_AUTOMOD_TRIGGER_LINKS, AllowedDomains: []string{"example.org"}, Alert: true, TimeoutSeconds: 60,
		}}}},
		{Op: &wire.AdminAction_DeleteAutomodRule{DeleteAutomodRule: &wire.DeleteAutoModRule{Id: 1}}},
		{Op: &wire.AdminAction_ResolveReport{ResolveReport: &wire.ResolveReport{Id: 1, Dismiss: true}}},
		{Op: &wire.AdminAction_AddEmoji{AddEmoji: &wire.AddEmoji{Name: "ok", Sha256: make([]byte, 32), Size: 3}}},
		{Op: &wire.AdminAction_RenameEmoji{RenameEmoji: &wire.RenameEmoji{Id: 1, Name: "renamed"}}},
		{Op: &wire.AdminAction_DeleteEmoji{DeleteEmoji: &wire.DeleteEmoji{Id: 1}}},
	}
	for _, op := range ops {
		f.Add(mustMarshal(op))
	}
	f.Add([]byte{})
	ctx := context.Background()
	f.Fuzz(func(t *testing.T, body []byte) {
		var a wire.AdminAction
		if proto.Unmarshal(body, &a) != nil {
			return // the server's own unmarshal of unsigned bytes is FuzzClientMessage's job
		}
		a.Community = fc.owner.Public
		a.Actor = fc.member.Public
		a.At = time.Now().UnixMilli()
		a.Nonce = randKey()
		raw := mustMarshal(&a)
		msg := mustMarshal(&wire.ClientMessage{Id: 9, Body: &wire.ClientMessage_Admin{Admin: &wire.SignedAction{
			Action: raw, Signature: fc.member.Sign(identity.CtxAdminAction, raw),
		}}})
		r := fc.ownerS.Handle(ctx, msg)
		checkReply(t, 9, r)
		// The same signed action is never applied twice.
		if r.GetError() == nil {
			again := fc.ownerS.Handle(ctx, msg)
			if again.GetError() == nil && fc.ownerS.Status() == wire.MemberStatus_MEMBER_STATUS_ACTIVE {
				t.Fatal("a replayed admin action was accepted")
			}
		}
	})
}
