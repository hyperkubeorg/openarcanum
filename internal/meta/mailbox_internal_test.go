package meta

import (
	"bytes"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

func countRows(t *testing.T, s *Server, table string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A tombstone counts only for the inbox whose token made it: someone who
// learned an item's gid can't pre-delete it with their own token (the drop
// still arrives), nor stop the real recipient's delete.
func TestMailboxTombstoneBelongsToItsInbox(t *testing.T) {
	s := openTestNode(t, nil, nil)
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(hs.Close)
	post := func(path string, m proto.Message, out proto.Message) {
		t.Helper()
		b, _ := proto.Marshal(m)
		resp, err := apiClient.Post(hs.URL+path, "application/x-protobuf", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d", path, resp.StatusCode)
		}
		if out != nil {
			body := new(bytes.Buffer)
			_, _ = body.ReadFrom(resp.Body)
			if err := proto.Unmarshal(body.Bytes(), out); err != nil {
				t.Fatal(err)
			}
		}
	}
	victim, attacker := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)
	inbox := InboxAddress(victim)
	blob := []byte("sealed message")
	gid := MailboxGID(inbox, blob)
	post("/v1/mailbox/delete", &wire.MailboxDelete{Token: attacker, Gids: [][]byte{gid}}, nil)
	post("/v1/mailbox/drop", &wire.MailboxDrop{Inbox: inbox, Blob: blob}, nil)
	var items wire.MailboxItems
	post("/v1/mailbox/fetch", &wire.MailboxFetch{Token: victim}, &items)
	if len(items.Items) != 1 {
		t.Fatalf("the drop after someone else's tombstone: %d items", len(items.Items))
	}
	post("/v1/mailbox/delete", &wire.MailboxDelete{Token: victim, Ids: []int64{items.Items[0].Id}}, nil)
	post("/v1/mailbox/drop", &wire.MailboxDrop{Inbox: inbox, Blob: blob}, nil)
	items.Reset()
	post("/v1/mailbox/fetch", &wire.MailboxFetch{Token: victim}, &items)
	if len(items.Items) != 0 {
		t.Fatalf("deleted item came back: %d items", len(items.Items))
	}
}

// Deleting gids this node doesn't hold (any random 32 bytes) leaves
// tombstones, which are kept and replicated for weeks: bounded per network,
// so one client can't fill the node with them.
func TestMailboxDeleteOfAbsentItemsIsBounded(t *testing.T) {
	s := openTestNode(t, nil, nil)
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(hs.Close)
	for range 4 {
		token := make([]byte, 32)
		_, _ = rand.Read(token)
		d := &wire.MailboxDelete{Token: token}
		for range 1000 {
			g := make([]byte, 32)
			_, _ = rand.Read(g)
			d.Gids = append(d.Gids, g)
		}
		b, _ := proto.Marshal(d)
		resp, err := apiClient.Post(hs.URL+"/v1/mailbox/delete", "application/x-protobuf", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("delete: %d", resp.StatusCode)
		}
	}
	if n := countRows(t, s, "mailbox_tombstones"); n > absentTombstonesPerHour {
		t.Fatalf("%d tombstones from one network in an hour", n)
	}
}
