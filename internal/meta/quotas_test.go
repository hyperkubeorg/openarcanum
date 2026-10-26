package meta_test

import (
	"bytes"
	"fmt"
	"net/http"
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/meta"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Every maintainer quota that isn't covered elsewhere, hit on purpose. The blob store's are in blobs_test.go and
// blobs_limits_test.go; the mailbox byte quota in TestMailbox.

func TestMailboxItemAndDropRate(t *testing.T) {
	n := startNode(t, func(c *meta.Config) { c.MailboxMaxItem = 4 << 10; c.MailboxDropsPerMin = 3 })
	inbox := meta.InboxAddress(bytes.Repeat([]byte{9}, 32))
	if code := n.post(t, "/v1/mailbox/drop", &wire.MailboxDrop{Inbox: inbox, Blob: make([]byte, 4<<10+1)}, nil); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("item over mailbox_max_item: %d", code)
	}
	for i := range 3 {
		if code := n.post(t, "/v1/mailbox/drop", &wire.MailboxDrop{Inbox: inbox, Blob: []byte{byte(i)}}, nil); code != 200 {
			t.Fatalf("drop %d: %d", i, code)
		}
	}
	if code := n.post(t, "/v1/mailbox/drop", &wire.MailboxDrop{Inbox: inbox, Blob: []byte("more")}, nil); code != http.StatusTooManyRequests {
		t.Fatalf("drop over mailbox_drops_per_minute: %d", code)
	}
	// The limit is per inbox: another inbox still takes mail.
	other := meta.InboxAddress(bytes.Repeat([]byte{8}, 32))
	if code := n.post(t, "/v1/mailbox/drop", &wire.MailboxDrop{Inbox: other, Blob: []byte("x")}, nil); code != 200 {
		t.Fatalf("other inbox: %d", code)
	}
}

func TestListingRatePerCommunity(t *testing.T) {
	n := startNode(t, func(c *meta.Config) { c.ListingsPerHour = 2 })
	c := newCommunity(t)
	for seq := uint64(1); seq <= 2; seq++ {
		if code := n.post(t, "/v1/listings", c.listing("Rate", "", nil, seq), nil); code != 200 {
			t.Fatalf("listing %d: %d", seq, code)
		}
	}
	if code := n.post(t, "/v1/listings", c.listing("Rate", "", nil, 3), nil); code != http.StatusTooManyRequests {
		t.Fatalf("listing over listings_per_hour: %d", code)
	}
	d := newCommunity(t)
	if code := n.post(t, "/v1/listings", d.listing("Other", "", nil, 1), nil); code != 200 {
		t.Fatalf("another community: %d", code)
	}
}

func TestNameplateRateAndWritesPerIP(t *testing.T) {
	n := startNode(t, func(c *meta.Config) { c.NameplatesPerMinute = 2 })
	for i := range 2 {
		if code := n.post(t, "/v1/nameplates", &wire.NameplateRequest{Relay: "ws://r/ws"}, nil); code != 200 {
			t.Fatalf("nameplate %d: %d", i, code)
		}
	}
	if code := n.post(t, "/v1/nameplates", &wire.NameplateRequest{Relay: "ws://r/ws"}, nil); code != http.StatusTooManyRequests {
		t.Fatalf("nameplate over nameplates_per_minute: %d", code)
	}

	w := startNode(t, func(c *meta.Config) { c.WritesPerMinute = 5 })
	for i := range 5 {
		inbox := meta.InboxAddress(bytes.Repeat([]byte{byte(i)}, 32))
		if code := w.post(t, "/v1/mailbox/drop", &wire.MailboxDrop{Inbox: inbox, Blob: []byte("x")}, nil); code != 200 {
			t.Fatalf("write %d: %d", i, code)
		}
	}
	if code := w.post(t, "/v1/mailbox/drop", &wire.MailboxDrop{Inbox: meta.InboxAddress(make([]byte, 32)), Blob: []byte("x")}, nil); code != http.StatusTooManyRequests {
		t.Fatalf("write over writes_per_minute: %d", code)
	}
	// Reads aren't writes.
	if code := w.get(t, fmt.Sprintf("/v1/search?q=%s", "x"), nil); code != 200 {
		t.Fatalf("search after the write limit: %d", code)
	}
}
