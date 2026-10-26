package meta_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/config"
	"github.com/hyperkubeorg/openarcanum/internal/meta"
	"github.com/hyperkubeorg/openarcanum/internal/store"
	"github.com/hyperkubeorg/openarcanum/internal/version"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// postFrom sends a request as if from remote (host:port), straight to the
// node's handler.
func (n *node) postFrom(t *testing.T, remote, path string, m proto.Message) int {
	t.Helper()
	b, _ := proto.Marshal(m)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.RemoteAddr = remote
	req.Header.Set(version.Header, version.Format(version.Supported()))
	rec := httptest.NewRecorder()
	n.srv.Handler().ServeHTTP(rec, req)
	return rec.Code
}

func dropTo(i int, size int) *wire.MailboxDrop {
	inbox := meta.InboxAddress(bytes.Repeat([]byte{byte(i), byte(i >> 8), 7}, 11)[:32])
	return &wire.MailboxDrop{Inbox: inbox, Blob: bytes.Repeat([]byte{1}, size)}
}

// All mailboxes together are bounded (mailbox_store_max): once full, drops
// to any inbox are refused with 507, other writes keep working, and space
// freed by a delete is usable again.
func TestMailboxStoreMax(t *testing.T) {
	n := startNode(t, func(c *meta.Config) {
		c.MailboxMaxItem = 64 << 10
		c.MailboxStoreMax = 256 << 10
	})
	token := bytes.Repeat([]byte{3}, 32)
	inbox := meta.InboxAddress(token)
	for i := range 4 {
		d := dropTo(i, 64<<10)
		if i == 0 {
			d.Inbox = inbox
		}
		if code := n.postFrom(t, fmt.Sprintf("192.0.2.%d:1", i+1), "/v1/mailbox/drop", d); code != 200 {
			t.Fatalf("drop %d: %d", i, code)
		}
	}
	// A fresh inbox, from a fresh network: the store is full.
	if code := n.postFrom(t, "198.51.100.9:1", "/v1/mailbox/drop", dropTo(99, 1)); code != http.StatusInsufficientStorage {
		t.Fatalf("drop into a full store: %d", code)
	}
	// Other writes still work.
	if code := n.post(t, "/v1/nameplates", &wire.NameplateRequest{Relay: "ws://relay.example/ws"}, nil); code != 200 {
		t.Fatalf("nameplate with a full mailbox store: %d", code)
	}
	var items wire.MailboxItems
	if code := n.post(t, "/v1/mailbox/fetch", &wire.MailboxFetch{Token: token}, &items); code != 200 || len(items.Items) != 1 {
		t.Fatalf("fetch: %d, %d items", code, len(items.Items))
	}
	if code := n.post(t, "/v1/mailbox/delete", &wire.MailboxDelete{Token: token, Ids: []int64{items.Items[0].Id}}, nil); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if code := n.postFrom(t, "198.51.100.9:1", "/v1/mailbox/drop", dropTo(99, 1)); code != 200 {
		t.Fatalf("drop after space was freed: %d", code)
	}
}

// One network (an IPv4 address, or an IPv6 /64 however many addresses it
// uses) may drop at most mailbox_bytes_per_ip per rolling 24 hours, spread
// over as many inboxes as it likes; another network is unaffected.
func TestMailboxBytesPerNetwork(t *testing.T) {
	n := startNode(t, func(c *meta.Config) {
		c.MailboxMaxItem = 64 << 10
		c.MailboxBytesPerIP = 128 << 10
	})
	for i, remote := range []string{"[2001:db8:1:2::1]:1", "[2001:db8:1:2:ffff::7]:1"} {
		if code := n.postFrom(t, remote, "/v1/mailbox/drop", dropTo(i, 64<<10)); code != 200 {
			t.Fatalf("drop %d: %d", i, code)
		}
	}
	if code := n.postFrom(t, "[2001:db8:1:2:abcd::9]:1", "/v1/mailbox/drop", dropTo(5, 1)); code != http.StatusTooManyRequests {
		t.Fatalf("drop over the /64's budget: %d", code)
	}
	if code := n.postFrom(t, "[2001:db8:1:3::1]:1", "/v1/mailbox/drop", dropTo(6, 64<<10)); code != 200 {
		t.Fatalf("drop from the next /64: %d", code)
	}
	for i := range 2 {
		if code := n.postFrom(t, "192.0.2.1:1", "/v1/mailbox/drop", dropTo(10+i, 64<<10)); code != 200 {
			t.Fatalf("IPv4 drop %d: %d", i, code)
		}
	}
	if code := n.postFrom(t, "192.0.2.1:2", "/v1/mailbox/drop", dropTo(20, 1)); code != http.StatusTooManyRequests {
		t.Fatalf("drop over the address's budget: %d", code)
	}
}

// writes_per_minute and nameplates_per_minute count an IPv6 /64 as one
// client, so rotating addresses within it doesn't escape them.
func TestPerIPLimitsGroupIPv6Slash64(t *testing.T) {
	n := startNode(t, func(c *meta.Config) { c.NameplatesPerMinute = 2; c.WritesPerMinute = 4 })
	np := &wire.NameplateRequest{Relay: "ws://relay.example/ws"}
	for i := range 2 {
		if code := n.postFrom(t, fmt.Sprintf("[2001:db8:5:6::%x]:1", i+1), "/v1/nameplates", np); code != 200 {
			t.Fatalf("nameplate %d: %d", i, code)
		}
	}
	if code := n.postFrom(t, "[2001:db8:5:6::99]:1", "/v1/nameplates", np); code != http.StatusTooManyRequests {
		t.Fatalf("nameplate from another address in the /64: %d", code)
	}
	if code := n.postFrom(t, "[2001:db8:5:7::1]:1", "/v1/nameplates", np); code != 200 {
		t.Fatalf("nameplate from the next /64: %d", code)
	}
	// Writes: the /64 has made 3 of its 4; then everything is refused.
	if code := n.postFrom(t, "[2001:db8:5:6::abc]:1", "/v1/mailbox/drop", dropTo(1, 10)); code != 200 {
		t.Fatalf("4th write: %d", code)
	}
	if code := n.postFrom(t, "[2001:db8:5:6::abd]:1", "/v1/mailbox/drop", dropTo(2, 10)); code != http.StatusTooManyRequests {
		t.Fatalf("write over writes_per_minute from the /64: %d", code)
	}
}

// Admin commands never migrate a database (the node may be running an
// older release on it right now): they refuse one at any schema version but
// this build's, and leave it as it was.
func TestAdminRefusesOtherSchema(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	older := meta.SchemaVersion() - 1
	if _, err := store.Apply(ctx, db, meta.Migrations()[:older]); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := meta.OpenAdmin(ctx, dir, meta.Config{}); err == nil || !strings.Contains(err.Error(), "node's own release") {
		t.Fatalf("admin on an older schema: %v", err)
	}
	if _, err := meta.OpenBlobAdmin(ctx, dir, nil); err == nil || !strings.Contains(err.Error(), "node's own release") {
		t.Fatalf("blob admin on an older schema: %v", err)
	}
	db, _ = store.Open(filepath.Join(dir, "meta.db"))
	defer db.Close()
	if v, _ := store.Version(ctx, db); v != older {
		t.Fatalf("an admin command migrated the database to version %d", v)
	}
}

func TestMailboxLimitConfig(t *testing.T) {
	c := meta.DefaultConfig()
	c.DirectoryEnabled = true // the tests exercise the directory; it's off by default
	if c.MailboxStoreMax != 10<<30 || c.MailboxBytesPerIP != 1<<30 || c.MailboxMaxItem != 2<<20 {
		t.Fatalf("defaults: %d %d %d", c.MailboxStoreMax, c.MailboxBytesPerIP, c.MailboxMaxItem)
	}
	c.DataDir = t.TempDir()
	for name, mut := range map[string]func(*meta.Config){
		"store below an item":  func(c *meta.Config) { c.MailboxStoreMax = c.MailboxMaxItem - 1 },
		"budget below an item": func(c *meta.Config) { c.MailboxBytesPerIP = c.MailboxMaxItem - 1 },
	} {
		bad := c
		mut(&bad)
		if bad.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
	t.Setenv("OPENARCANUM_META_MAILBOX_STORE_MAX", "12345678")
	if err := config.Load("", "OPENARCANUM_META_", &c); err != nil || c.MailboxStoreMax != 12345678 {
		t.Fatalf("env override: %d %v", c.MailboxStoreMax, err)
	}
}
