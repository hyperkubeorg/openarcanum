package meta_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/hyperkubeorg/openarcanum/internal/meta"
	"github.com/hyperkubeorg/openarcanum/internal/version"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// A mailbox watch says "check now" for mail already waiting and for each
// drop into its inbox, and nothing for other inboxes.
func TestMailboxWatch(t *testing.T) {
	n := startNode(t, nil)
	token := bytes.Repeat([]byte{7}, 32)
	inbox := meta.InboxAddress(token)
	other := meta.InboxAddress(bytes.Repeat([]byte{8}, 32))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dial := func() *websocket.Conn {
		ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(n.url, "http")+"/v1/mailbox/watch?v="+version.Format(version.Supported()), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := ws.Write(ctx, websocket.MessageBinary, token); err != nil {
			t.Fatal(err)
		}
		return ws
	}
	signal := func(ws *websocket.Conn, want bool) {
		t.Helper()
		rctx, rcancel := context.WithTimeout(ctx, 700*time.Millisecond)
		defer rcancel()
		_, b, err := ws.Read(rctx)
		if want && (err != nil || !bytes.Equal(b, []byte{0x01})) {
			t.Fatalf("no signal: %v %v", b, err)
		}
		if !want && err == nil {
			t.Fatalf("an unexpected signal: %v", b)
		}
	}

	ws := dial()
	signal(ws, false) // nothing waiting
	if code := n.post(t, "/v1/mailbox/drop", &wire.MailboxDrop{Inbox: other, Blob: []byte("not yours")}, nil); code != 200 {
		t.Fatal(code)
	}
	if code := n.post(t, "/v1/mailbox/drop", &wire.MailboxDrop{Inbox: inbox, Blob: []byte("hello")}, nil); code != 200 {
		t.Fatal(code)
	}
	ws.CloseNow()
	// The read timed out above, which closes a coder/websocket conn: a new
	// watch sees the mail already waiting at once.
	ws = dial()
	defer ws.CloseNow()
	signal(ws, true)
	if code := n.post(t, "/v1/mailbox/drop", &wire.MailboxDrop{Inbox: inbox, Blob: []byte("again")}, nil); code != 200 {
		t.Fatal(code)
	}
	signal(ws, true)

	// Only the token opens a watch.
	bad, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(n.url, "http")+"/v1/mailbox/watch?v="+version.Format(version.Supported()), nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = bad.Write(ctx, websocket.MessageText, []byte("hi"))
	if _, _, err := bad.Read(ctx); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("a watch without a token: %v", err)
	}
}
