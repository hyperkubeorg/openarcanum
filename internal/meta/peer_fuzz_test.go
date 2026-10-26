package meta

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// FuzzPeerBatch applies arbitrary peer batches (what a compromised or
// broken cluster member could send). The invariants: no panic, and every
// item stored is filed under the global id of its own content and inbox
// (a peer can't choose an item's id), with an expiry no later than this
// node's mailbox_ttl from now.
func FuzzPeerBatch(f *testing.F) {
	cfg := DefaultConfig()
	cfg.DirectoryEnabled = true // the tests exercise the directory; it's off by default
	cfg.DataDir = f.TempDir()
	cfg.MailboxQuota, cfg.MailboxMaxItem = 1<<20, 64<<10
	s, err := Open(context.Background(), cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { s.Close() })
	now := time.Now()
	inbox := bytes.Repeat([]byte{1}, 32)
	item, _ := proto.Marshal(&wire.PeerBatch{Cursor: 2, Entries: []*wire.PeerEntry{
		{Mrev: 1, Entry: &wire.PeerEntry_Item{Item: &wire.PeerMailboxItem{Inbox: inbox, Blob: []byte("x"), ExpiresAt: now.Add(time.Hour).UnixMilli()}}},
		{Mrev: 2, Entry: &wire.PeerEntry_Tombstone{Tombstone: &wire.PeerMailboxTombstone{Gid: MailboxGID(inbox, []byte("y")), Inbox: inbox, ExpiresAt: now.Add(time.Hour).UnixMilli()}}},
	}})
	f.Add(item)
	far, _ := proto.Marshal(&wire.PeerBatch{Entries: []*wire.PeerEntry{
		{Mrev: 9, Entry: &wire.PeerEntry_Item{Item: &wire.PeerMailboxItem{Inbox: inbox, Blob: []byte("z"), ExpiresAt: 1 << 62}}},
	}})
	f.Add(far)
	f.Add([]byte{})
	l := &peerLink{key: bytes.Repeat([]byte{9}, 32), url: "http://peer.invalid", pending: map[uint64]chan *wire.PeerMessage{}}
	f.Fuzz(func(t *testing.T, data []byte) {
		var b wire.PeerBatch
		if proto.Unmarshal(data, &b) != nil {
			return
		}
		ctx := context.Background()
		_, _, _ = s.applyPeerBatch(ctx, l, &b)
		rows, err := s.db.QueryContext(ctx, `SELECT inbox, blob, gid, expires_at FROM mailbox`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		limit := s.now().Add(cfg.MailboxTTL.Duration).UnixMilli()
		for rows.Next() {
			var in, blob, gid []byte
			var exp int64
			if err := rows.Scan(&in, &blob, &gid, &exp); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(gid, MailboxGID(in, blob)) {
				t.Fatal("an item is filed under a global id that isn't its own")
			}
			if exp > limit {
				t.Fatal("a peer extended an item's lifetime")
			}
		}
	})
}
