package meta_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/meta"
	"github.com/hyperkubeorg/openarcanum/internal/ops"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// Typical maintainer traffic (listings, search, routes, mailboxes, blobs,
// device-link nameplates) is counted in the metrics, and neither the debug
// log nor the metrics carry names, queries, tokens, inbox addresses, ids
// from request paths or content.
func TestMetaMetricsAndLogsCarryNoContent(t *testing.T) {
	var logs syncBuf
	cfg := meta.DefaultConfig()
	cfg.DirectoryEnabled = true // the tests exercise the directory; it's off by default
	cfg.DataDir = t.TempDir()
	srv, err := meta.Open(context.Background(), cfg, slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if err != nil {
		t.Fatal(err)
	}
	reg := ops.NewRegistry()
	hs := httptest.NewServer(srv.RegisterMetrics(reg))
	st := httptest.NewServer(ops.Status{Registry: reg, Live: srv.Live}.Handler())
	t.Cleanup(func() { hs.Close(); st.Close(); srv.Close() })
	n := &node{srv: srv, url: hs.URL}

	c := newCommunity(t)
	if code := n.post(t, "/v1/listings", c.listing("Zebra Finch Hideout", "a very private description", []string{"finches"}, 1), nil); code != 200 {
		t.Fatalf("listing: %d", code)
	}
	var sr wire.SearchResponse
	if code := n.get(t, "/v1/search?q=finchquerysecret", &sr); code != 200 {
		t.Fatalf("search: %d", code)
	}
	if code := n.post(t, "/v1/routes", c.signedRoute(1), nil); code != 200 {
		t.Fatalf("route: %d", code)
	}
	n.get(t, "/v1/routes/"+identity.Encode(c.route), nil)
	n.get(t, "/v1/listings/"+identity.Encode(c.owner.Public), nil)
	mailToken := blobToken(0x5e)
	mail := []byte("sealed mailbox payload walrus")
	n.drop(t, mailToken, mail)
	items := n.fetchMail(t, mailToken)
	blobData := append([]byte("dm file ciphertext narwhal "), randBytes(2000)...)
	fileToken := blobToken(0x7a)
	hash := n.upload(t, blobData, fileToken)
	if _, code := n.download(t, hash, fileToken); code != 200 {
		t.Fatal("download")
	}
	// A refused fetch with a wrong token is counted under its route too.
	if _, code := n.download(t, hash, blobToken(0x01)); code == 200 {
		t.Fatal("wrong token fetched a blob")
	}
	var np wire.Nameplate
	if code := n.post(t, "/v1/nameplates", &wire.NameplateRequest{Relay: "wss://relay.example.invalid/ws"}, &np); code != 200 {
		t.Fatalf("nameplate: %d", code)
	}

	res, err := http.Get(st.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	out := string(body)
	if _, err := ops.CheckExposition(out); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"openarcanum_meta_listings 1\n",
		"openarcanum_meta_routes 1\n",
		"openarcanum_meta_mailbox_items 1\n",
		"openarcanum_meta_mailbox_bytes " + strconv.Itoa(len(mail)) + "\n",
		"openarcanum_meta_blobs_stored 1\n",
		"openarcanum_meta_blobs_stored_bytes " + strconv.Itoa(len(blobData)) + "\n",
		"openarcanum_meta_nameplates 1\n",
		`openarcanum_meta_http_requests_total{route="POST /v1/mailbox/drop",code="200"} 1`,
		`openarcanum_meta_http_requests_total{route="GET /v1/search",code="200"} 1`,
		`openarcanum_meta_http_requests_total{route="GET /v1/routes/{id}",code="200"} 1`,
		`openarcanum_meta_http_requests_total{route="POST /v1/blobs/fetch",code="404"} 1`,
		`openarcanum_meta_http_request_duration_seconds_count{route="POST /v1/blobs/chunk"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics lack %q", want)
		}
	}
	if code, _ := httpGet(t, st.URL+"/healthz"); code != 200 {
		t.Fatalf("healthz: %d", code)
	}

	all := logs.String() + out
	if !strings.Contains(logs.String(), `"level":"INFO"`) {
		t.Fatalf("expected log records; log:\n%s", logs.String())
	}
	secrets := [][]byte{
		[]byte("Zebra Finch Hideout"), []byte("a very private description"), []byte("finchquerysecret"),
		mail, []byte("narwhal"), mailToken, fileToken, meta.InboxAddress(mailToken), meta.BlobTokenHash(fileToken),
		c.owner.Public, c.server.Public, c.route, np.Topic, items[0].Blob,
	}
	for _, s := range secrets {
		for _, form := range []string{string(s), hex.EncodeToString(s), base64.StdEncoding.EncodeToString(s), base64.RawURLEncoding.EncodeToString(s)} {
			if len(form) >= 6 && strings.Contains(all, form) {
				t.Errorf("log or metrics contain %q", form)
			}
		}
	}
}

func httpGet(t *testing.T, url string) (int, string) {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

// The node's health check fails once its database is gone.
func TestMetaHealthFailsWithoutDatabase(t *testing.T) {
	n := startNode(t, nil)
	if code, body := httpGet(t, n.url+"/healthz"); code != 200 || body != "ok\n" {
		t.Fatalf("healthz: %d %q", code, body)
	}
	n.srv.Close()
	if code, body := httpGet(t, n.url+"/healthz"); code != 503 || !strings.Contains(body, "database unavailable") {
		t.Fatalf("healthz with the database closed: %d %q", code, body)
	}
	if err := n.srv.Live(context.Background()); err == nil {
		t.Fatal("Live passed without a database")
	}
}

func (n *node) drop(t *testing.T, token, blob []byte) {
	t.Helper()
	if code := n.post(t, "/v1/mailbox/drop", &wire.MailboxDrop{Inbox: meta.InboxAddress(token), Blob: blob}, nil); code != 200 {
		t.Fatalf("drop: %d", code)
	}
}

func (n *node) fetchMail(t *testing.T, token []byte) []*wire.MailboxItem {
	t.Helper()
	var items wire.MailboxItems
	if code := n.post(t, "/v1/mailbox/fetch", &wire.MailboxFetch{Token: token}, &items); code != 200 {
		t.Fatalf("fetch: %d", code)
	}
	return items.Items
}
