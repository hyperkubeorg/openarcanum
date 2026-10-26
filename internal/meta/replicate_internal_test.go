package meta

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/version"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// syncBuffer is a log sink safe for concurrent writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func openTestNode(t *testing.T, peers []string, log *slog.Logger) *Server {
	t.Helper()
	cfg := DefaultConfig()
	cfg.DirectoryEnabled = true // the tests exercise the directory; it's off by default
	cfg.DataDir = t.TempDir()
	cfg.Peers = peers
	s, err := Open(context.Background(), cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func testRoute(t *testing.T) (*wire.SignedRoute, []byte) {
	t.Helper()
	owner, _ := identity.Generate()
	server, _ := identity.Generate()
	cert, err := identity.IssueServerCert(owner, server.Public, 1, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	id := bytes.Repeat([]byte{7}, 16)
	r, _ := proto.Marshal(&wire.Route{RouteId: id, ServerKey: server.Public, Relays: []string{"ws://r/ws"}, Seq: 1, PublishedAt: time.Now().UnixMilli()})
	return &wire.SignedRoute{Route: r, Signature: server.Sign(identity.CtxRoute, r), Cert: cert}, id
}

// An entry that fails to store for a non-routine reason (here a
// database error) keeps the peer's cursor where it was, so the entry is
// pulled again; the failure is logged.
func TestReplicationRetriesEntryThatFailedToStore(t *testing.T) {
	a := openTestNode(t, nil, slog.New(slog.DiscardHandler))
	peer := httptest.NewServer(a.Handler())
	t.Cleanup(peer.Close)
	sr, id := testRoute(t)
	raw, _ := proto.Marshal(sr)
	resp, err := apiClient.Post(peer.URL+"/v1/routes", "application/x-protobuf", bytes.NewReader(raw))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("post route: %v %v", err, resp)
	}
	resp.Body.Close()

	var logs syncBuffer
	b := openTestNode(t, []string{peer.URL}, slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	ctx := context.Background()
	if _, err := b.db.ExecContext(ctx, `CREATE TRIGGER broken BEFORE INSERT ON routes BEGIN SELECT RAISE(ABORT, 'disk trouble'); END`); err != nil {
		t.Fatal(err)
	}
	b.ReplicateOnce(ctx)
	if !strings.Contains(logs.String(), "replication: entry not stored") || !strings.Contains(logs.String(), "cursor not advanced") {
		t.Fatalf("store failure not logged:\n%s", logs.String())
	}
	var n int
	if err := b.db.QueryRowContext(ctx, `SELECT count(*) FROM counters WHERE k = ?`, "cursor:"+peer.URL).Scan(&n); err != nil || n != 0 {
		t.Fatalf("cursor saved past a failed entry (%d rows, %v)", n, err)
	}
	if _, err := b.db.ExecContext(ctx, `DROP TRIGGER broken`); err != nil {
		t.Fatal(err)
	}
	b.ReplicateOnce(ctx)
	var got int
	if err := b.db.QueryRowContext(ctx, `SELECT count(*) FROM routes WHERE route_id = ?`, id).Scan(&got); err != nil || got != 1 {
		t.Fatalf("the failed entry was never pulled again (%d, %v)", got, err)
	}
	if err := b.db.QueryRowContext(ctx, `SELECT count(*) FROM counters WHERE k = ?`, "cursor:"+peer.URL).Scan(&n); err != nil || n != 1 {
		t.Fatalf("cursor not saved after a clean round (%d, %v)", n, err)
	}
}

// A refused pull, a reply that can't be read and a batch that can't
// be parsed are logged, not dropped silently.
func TestReplicationPullFailuresAreLogged(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		handler    http.HandlerFunc
	}{
		{"status", "replication pull refused", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "nope", http.StatusInternalServerError)
		}},
		{"garbage", "unparseable batch", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte{0xff, 0xff, 0xff})
		}},
		{"truncated", "reading the reply failed", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "1000")
			_, _ = w.Write([]byte{1, 2, 3})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer := httptest.NewServer(tc.handler)
			t.Cleanup(peer.Close)
			var logs syncBuffer
			b := openTestNode(t, []string{peer.URL}, slog.New(slog.NewTextHandler(&logs, nil)))
			b.http.Timeout = 5 * time.Second
			b.ReplicateOnce(context.Background())
			if !strings.Contains(logs.String(), tc.want) {
				t.Fatalf("want %q in logs:\n%s", tc.want, logs.String())
			}
		})
	}
}

// apiClient announces this build's protocol versions (the maintainer API
// refuses requests without them).
var apiClient = &http.Client{Transport: version.Transport{}}
