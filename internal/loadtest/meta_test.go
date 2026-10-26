//go:build load

package loadtest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/version"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

var (
	words = strings.Fields(`rust python gardening knitting chess astronomy cooking photography cycling hiking
		music jazz metal guitar piano drawing painting anime gaming retro linux security privacy books poetry
		history science physics biology robots electronics radio woodworking pottery baking coffee tea running
		climbing sailing fishing birds cats dogs film theatre dance language french spanish japanese math`)
	tags = strings.Fields(`tech arts crafts games music science outdoors food pets languages books film`)
)

// TestMaintainer: 10,000 directory listings and search p95 under 200 ms
// on a maintainer node pinned to LOAD_SERVER_CPUS.
// Listings are real owner-signed listings posted through the API; searches
// mix word queries, tag filters, both, and browsing, from LOAD_SEARCHERS
// concurrent clients for LOAD_DURATION.
func TestMaintainer(t *testing.T) {
	listings := envInt("LOAD_LISTINGS", 10000)
	searchers := envInt("LOAD_SEARCHERS", 16)
	duration := envDuration("LOAD_DURATION", time.Minute)

	port := freePort(t)
	srv := startServer(t, []string{
		fmt.Sprintf("OPENARCANUM_META_LISTEN=127.0.0.1:%d", port),
		"OPENARCANUM_META_DATA_DIR=" + t.TempDir(),
		// Every listing comes from this one address; the per-address limits
		// are for abuse, not what's measured here.
		"OPENARCANUM_META_LISTINGS_PER_HOUR=1000000",
		"OPENARCANUM_META_WRITES_PER_MINUTE=1000000",
		"OPENARCANUM_META_READS_PER_MINUTE=1000000",
	}, serverBinary(t, "meta"))
	waitPort(t, port)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 64}, Timeout: 30 * time.Second}
	ctx := context.Background()

	start := time.Now()
	var failed atomic.Int64
	var firstErr atomic.Value
	sem := make(chan struct{}, 32)
	var wg sync.WaitGroup
	for i := 0; i < listings; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			body, err := proto.Marshal(newListing(i))
			if err == nil {
				err = post(ctx, client, base+"/v1/listings", body)
			}
			if err != nil {
				failed.Add(1)
				firstErr.CompareAndSwap(nil, err.Error())
			}
		}(i)
	}
	wg.Wait()
	if n := failed.Load(); n > 0 {
		t.Fatalf("%d of %d listings refused (first: %v)", n, listings, firstErr.Load())
	}
	loadTime := time.Since(start)
	t.Logf("%d listings stored in %v", listings, loadTime.Round(time.Millisecond))

	var lat latencies
	var searchErr atomic.Int64
	var empty atomic.Int64
	cpu0, wall0 := srv.cpuSeconds(t), time.Now()
	deadline := time.Now().Add(duration)
	for w := 0; w < searchers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			r := rand.New(rand.NewPCG(uint64(w), 7))
			for time.Now().Before(deadline) {
				q := url.Values{}
				switch r.IntN(4) {
				case 0:
					q.Set("q", words[r.IntN(len(words))])
				case 1:
					q.Set("tag", tags[r.IntN(len(tags))])
				case 2:
					q.Set("q", words[r.IntN(len(words))])
					q.Set("tag", tags[r.IntN(len(tags))])
				default: // browse
				}
				t0 := time.Now()
				n, err := search(ctx, client, base+"/v1/search?"+q.Encode())
				if err != nil {
					searchErr.Add(1)
					firstErr.CompareAndSwap(nil, err.Error())
					continue
				}
				lat.add(time.Since(t0))
				if n == 0 {
					empty.Add(1)
				}
			}
		}(w)
	}
	wg.Wait()
	elapsed := time.Since(wall0)
	cpu := srv.cpuSeconds(t) - cpu0
	rss, peak := srv.memory(t)

	s := lat.summary()
	report(t, "maintainer", map[string]any{
		"listings": listings, "searchers": searchers, "duration_s": duration.Seconds(), "server_cpus": srv.cpus,
		"load_seconds":     loadTime.Seconds(),
		"searches":         s.Count,
		"searches_per_s":   float64(s.Count) / elapsed.Seconds(),
		"search_errors":    searchErr.Load(),
		"empty_results":    empty.Load(),
		"search":           s,
		"server_cpu_cores": cpu / elapsed.Seconds(),
		"server_rss_mib":   rss,
		"server_peak_mib":  peak,
	})
	if e := searchErr.Load(); e > 0 {
		t.Errorf("%d searches failed (first: %v)", e, firstErr.Load())
	}
	if s.P95 > 200 {
		t.Errorf("search p95 %.0f ms (over 200 ms)", s.P95)
	}
	// Every word and tag is used by many listings: an empty result is a bug.
	if n := empty.Load(); n > 0 {
		t.Errorf("%d searches found nothing", n)
	}
}

// newListing is community i's owner-signed listing: three words in its name
// and description, one or two tags.
func newListing(i int) *wire.SignedListing {
	r := rand.New(rand.NewPCG(uint64(i), 1))
	owner, _ := identity.Generate()
	server, _ := identity.Generate()
	cert, _ := identity.IssueServerCert(owner, server.Public, 1, nil, nil)
	pick := func() string { return words[r.IntN(len(words))] }
	lt := []string{tags[r.IntN(len(tags))]}
	if r.IntN(2) == 0 {
		lt = append(lt, tags[r.IntN(len(tags))])
	}
	route := make([]byte, 16)
	for j := range route {
		route[j] = byte(r.IntN(256))
	}
	l, _ := proto.Marshal(&wire.Listing{
		Community: owner.Public, ServerKey: server.Public, RouteId: route,
		Name:        fmt.Sprintf("%s %s %d", pick(), pick(), i),
		Description: fmt.Sprintf("A community about %s, %s and %s.", pick(), pick(), pick()),
		Tags:        lt, Seq: 1, Relays: []string{"wss://relay.example.invalid/ws"}, JoinPolicy: wire.JoinPolicy_JOIN_POLICY_OPEN,
		PublishedAt: time.Now().UnixMilli(),
	})
	return &wire.SignedListing{Listing: l, Signature: server.Sign(identity.CtxListing, l), Cert: cert}
}

func post(ctx context.Context, c *http.Client, url string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	version.SetRequest(req)
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode != 200 {
		return fmt.Errorf("%d: %s", resp.StatusCode, b)
	}
	return nil
}

func search(ctx context.Context, c *http.Client, url string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return 0, err
	}
	version.SetRequest(req)
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("%d: %s", resp.StatusCode, b[:min(len(b), 200)])
	}
	var out wire.SearchResponse
	if err := proto.Unmarshal(b, &out); err != nil {
		return 0, err
	}
	return len(out.Listings), nil
}
