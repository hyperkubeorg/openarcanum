package meta

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/hyperkubeorg/openarcanum/internal/ops"
)

// Mailbox watch (docs/protocol.md §7.1 "Watching an inbox"): a WebSocket
// on which a client names one inbox, by its token as in a fetch, and is
// told "check now" (one byte, 0x01) whenever that inbox may have something
// new: at once if it already holds mail, and after each drop into it here
// or each item a cluster peer brings. It carries nothing else: the client
// fetches as it always does. One inbox per socket, so a watch links inboxes
// no more than separate fetches do. The client still polls now and then
// (a missed signal costs a poll's delay, nothing more).

const (
	// watchHello bounds the wait for the token after the upgrade.
	watchHello = 10 * time.Second
	// watchPing keeps the connection (and proxies on the way) alive.
	watchPing = 30 * time.Second
	// watchLifetime closes a watch now and then; the client reconnects.
	watchLifetime = time.Hour
	// Open watches at most, in all and per network (an IPv4 address or an
	// IPv6 /64): a client keeps one per active conversation.
	watchMax        = 20000
	watchMaxPerNet  = 256
	watchSignal     = 0x01
	watchTokenBytes = 32
)

type watchers struct {
	mu    sync.Mutex
	inbox map[string]map[chan struct{}]struct{}
	open  int
	byNet map[string]int
}

// wake tells every watch of inbox to check now.
func (s *Server) wake(inbox []byte) {
	w := &s.watch
	w.mu.Lock()
	defer w.mu.Unlock()
	for ch := range w.inbox[string(inbox)] {
		select {
		case ch <- struct{}{}:
		default: // a signal is already waiting
		}
	}
}

func (s *Server) watchOpen(net string) (func(), bool) {
	w := &s.watch
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.open >= watchMax || w.byNet[net] >= watchMaxPerNet {
		return nil, false
	}
	if w.byNet == nil {
		w.byNet = map[string]int{}
	}
	w.open++
	w.byNet[net]++
	return func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.open--
		if w.byNet[net]--; w.byNet[net] <= 0 {
			delete(w.byNet, net)
		}
	}, true
}

func (s *Server) watchAdd(inbox []byte, ch chan struct{}) func() {
	w := &s.watch
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.inbox == nil {
		w.inbox = map[string]map[chan struct{}]struct{}{}
	}
	set := w.inbox[string(inbox)]
	if set == nil {
		set = map[chan struct{}]struct{}{}
		w.inbox[string(inbox)] = set
	}
	set[ch] = struct{}{}
	return func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		delete(set, ch)
		if len(set) == 0 {
			delete(w.inbox, string(inbox))
		}
	}
}

// Watches open now (metrics).
func (s *Server) watchCount() int {
	s.watch.mu.Lock()
	defer s.watch.mu.Unlock()
	return s.watch.open
}

func (s *Server) mailboxWatch(w http.ResponseWriter, r *http.Request) {
	done, ok := s.watchOpen(ops.NetworkKey(ops.RemoteIP(r)))
	if !ok {
		http.Error(w, "too many watches; poll instead", http.StatusTooManyRequests)
		return
	}
	defer done()
	// The node's request timeouts (httpserver.go) don't apply to a watch,
	// which stays open: its pings and lifetime bound it instead.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})
	// Any origin, as for fetches: the token is the permission.
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ws.SetReadLimit(watchTokenBytes)
	ctx, cancel := context.WithTimeout(r.Context(), watchLifetime)
	defer cancel()

	hctx, hcancel := context.WithTimeout(ctx, watchHello)
	typ, token, err := ws.Read(hctx)
	hcancel()
	if err != nil || typ != websocket.MessageBinary || len(token) != watchTokenBytes {
		ws.Close(websocket.StatusPolicyViolation, "send the inbox token")
		return
	}
	inbox := InboxAddress(token)
	ch := make(chan struct{}, 1)
	defer s.watchAdd(inbox, ch)()
	// Mail already waiting: say so now.
	var waiting int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM mailbox WHERE inbox = ? AND expires_at > ?`, inbox, s.now().UnixMilli()).Scan(&waiting); err == nil && waiting > 0 {
		ch <- struct{}{}
	}
	// The client sends nothing more; reading notices it leaving.
	rctx := ws.CloseRead(ctx)
	ping := time.NewTicker(watchPing)
	defer ping.Stop()
	for {
		select {
		case <-rctx.Done():
			ws.Close(websocket.StatusNormalClosure, "")
			return
		case <-ch:
			if err := ws.Write(rctx, websocket.MessageBinary, []byte{watchSignal}); err != nil {
				return
			}
		case <-ping.C:
			pctx, pcancel := context.WithTimeout(rctx, 10*time.Second)
			err := ws.Ping(pctx)
			pcancel()
			if err != nil {
				return
			}
		}
	}
}
