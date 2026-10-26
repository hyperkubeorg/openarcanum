package community_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// A moderator deleting someone else's message, pinning and unpinning are in
// the audit log: who, whose message and which channel, never the text.
// Deleting your own message isn't an admin action and isn't logged.
func TestMessageModerationAudited(t *testing.T) {
	e := setup(t, nil)
	gen := e.owner.Channel("general").Id
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	mod := e.member("Mod", e.invite.Secret)
	e.role("Mods", community.PermManageMessages|community.PermViewAuditLog, mod)
	const text = "confidential walrus plans"
	m := post(t, alice, gen, text)
	own := post(t, mod, gen, "my own words")
	for _, p := range []*wire.Pin{{MessageId: m.Id}, {MessageId: m.Id, Unpin: true}} {
		if _, err := call(t, mod, p); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := call(t, mod, &wire.DeleteMessage{MessageId: own.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, mod, &wire.DeleteMessage{MessageId: m.Id}); err != nil {
		t.Fatal(err)
	}
	r, err := mod.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_AuditLog{AuditLog: &wire.AuditQuery{Actor: mod.Identity.Public}}})
	if err != nil {
		t.Fatal(err)
	}
	var ops []string
	for _, en := range r.GetAudit().Entries {
		ops = append(ops, en.ActionName)
		if !en.System || en.ActorName != "Mod" || !bytes.Equal(en.Target, alice.Identity.Public) || en.TargetName != "Alice" {
			t.Errorf("entry: %v", en)
		}
		if !strings.Contains(en.Summary, "Alice") || !strings.Contains(en.Summary, "#general") || strings.Contains(en.Summary, "walrus") {
			t.Errorf("summary: %q", en.Summary)
		}
	}
	if strings.Join(ops, ",") != "delete_message,unpin,pin" {
		t.Fatalf("audited ops, newest first: %v", ops)
	}
	// Filterable by action like any other entry.
	r, _ = mod.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_AuditLog{AuditLog: &wire.AuditQuery{Action: "delete_message"}}})
	if n := len(r.GetAudit().Entries); n != 1 {
		t.Fatalf("by action: %d", n)
	}
	// Alice deleting her own message adds nothing.
	before, _ := e.comm.Server.AuditSummaries(ctx)
	mine := post(t, alice, gen, "oops")
	if _, err := call(t, alice, &wire.DeleteMessage{MessageId: mine.Id}); err != nil {
		t.Fatal(err)
	}
	if after, _ := e.comm.Server.AuditSummaries(ctx); len(after) != len(before) {
		t.Fatalf("own delete audited: %q", after[len(before):])
	}
}
