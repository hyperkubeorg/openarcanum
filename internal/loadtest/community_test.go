//go:build load

package loadtest

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// TestCommunityServer: 5,000 members, 500 of them online watching #general,
// and 50 messages/second sustained, against the community server pinned to
// 2 vCPUs (LOAD_SERVER_CPUS). Targets are in docs/load-tests.md; sizes
// and duration can be changed with LOAD_MEMBERS, LOAD_ONLINE, LOAD_RATE
// and LOAD_DURATION.
func TestCommunityServer(t *testing.T) {
	members := envInt("LOAD_MEMBERS", 5000)
	online := envInt("LOAD_ONLINE", 500)
	rate := envInt("LOAD_RATE", 50)
	duration := envDuration("LOAD_DURATION", 5*time.Minute)

	sampled := min(25, online) // watchers that decrypt every message to time delivery

	relayURL := startRelay(t)
	dir := t.TempDir()
	out, err := exec.Command(serverBinary(t, "community"), "init", "-data", dir, "-relay", relayURL).Output()
	if err != nil {
		t.Fatalf("community init: %v", err)
	}
	code := claimCode(t, string(out))
	srv := startServer(t, nil, serverBinary(t, "community"), "run", "-data", dir)

	ctx, cancel := context.WithTimeout(context.Background(), duration+60*time.Minute)
	defer cancel()
	owner := connect(t, ctx, relayURL, code.ServerKey, "Owner")
	ownerKey, _ := identity.Generate()
	settings := &wire.CommunitySettings{Name: "Load", Visibility: wire.Visibility_VISIBILITY_PRIVATE,
		JoinPolicy: wire.JoinPolicy_JOIN_POLICY_INVITE_ONLY, MessageRetentionDays: 30, FileRetentionDays: 7, SearchEnabled: true}
	if _, err := owner.Claim(ctx, ownerKey, code, settings); err != nil {
		t.Fatal(err)
	}
	res, err := owner.Admin(ctx, ownerKey.Public, &wire.AdminAction{Op: &wire.AdminAction_CreateJoinKey{
		CreateJoinKey: &wire.CreateJoinKey{Label: "load"}}})
	if err != nil {
		t.Fatal(err)
	}
	joinKey := res.JoinKey.Secret

	// Everyone connects and joins (LOAD_JOIN_CONCURRENCY at a time, 64 by
	// default); the first `online` stay connected.
	start := time.Now()
	var joinLat, connectLat latencies
	kept := make([]*testutil.Member, online)
	var failed atomic.Int64
	sem := make(chan struct{}, envInt("LOAD_JOIN_CONCURRENCY", 64))
	var wg sync.WaitGroup
	for i := 0; i < members; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			m := testutil.NewMember(t, fmt.Sprintf("m%05d", i))
			t0 := time.Now()
			if err := m.Connect(ctx, relayURL, code.ServerKey); err != nil {
				failed.Add(1)
				return
			}
			connectLat.add(time.Since(t0))
			t1 := time.Now()
			r, err := m.Join(ctx, joinKey)
			if err != nil || r.Status != wire.JoinStatus_JOIN_STATUS_ADMITTED {
				failed.Add(1)
				m.Close()
				return
			}
			joinLat.add(time.Since(t1))
			if i < online {
				kept[i] = m
			} else {
				m.Close()
			}
		}(i)
	}
	wg.Wait()
	joinTime := time.Since(start)
	if n := failed.Load(); n > 0 {
		t.Fatalf("%d of %d joins failed", n, members)
	}
	t.Logf("%d members joined in %v", members, joinTime.Round(time.Second))

	// Online members watch #general; a few decrypt everything to time it.
	general := kept[0].Channel("general")
	if general == nil {
		t.Fatal("no #general in the joined state")
	}
	var sent sync.Map // text -> send time
	var delivery latencies
	var delivered atomic.Int64
	for i, m := range kept {
		if i < sampled {
			key, epoch := append([]byte(nil), general.Key...), general.KeyEpoch
			err = m.Relay.Subscribe(ctx, general.Topic, func(_, data []byte) {
				ev, err := testutil.OpenChannelEvent(data, code.ServerKey, key, epoch)
				if err != nil || ev.GetMessage() == nil {
					return
				}
				if at, ok := sent.Load(ev.GetMessage().Text); ok {
					delivery.add(time.Since(at.(time.Time)))
				}
			})
		} else {
			err = m.Relay.Subscribe(ctx, general.Topic, func(_, _ []byte) { delivered.Add(1) })
		}
		if err != nil {
			t.Fatal(err)
		}
	}

	// rate messages a second for duration, from the online members in turn.
	var postLat latencies
	var slowMu sync.Mutex
	var slowAt []float64 // seconds into the run of posts slower than 100 ms
	var postErr atomic.Int64
	var firstErr atomic.Value
	cpu0, wall0 := srv.cpuSeconds(t), time.Now()
	tick := time.NewTicker(time.Second / time.Duration(rate))
	n := 0
	var posts sync.WaitGroup
	for deadline := time.Now().Add(duration); time.Now().Before(deadline); n++ {
		<-tick.C
		m := kept[n%online]
		text := fmt.Sprintf("load message %d", n)
		posts.Add(1)
		go func() {
			defer posts.Done()
			pctx, pcancel := context.WithTimeout(ctx, 30*time.Second)
			defer pcancel()
			t0 := time.Now()
			sent.Store(text, t0)
			if _, err := m.Post(pctx, general.Id, text); err != nil {
				postErr.Add(1)
				firstErr.CompareAndSwap(nil, err.Error())
				return
			}
			d := time.Since(t0)
			postLat.add(d)
			if d > 100*time.Millisecond {
				slowMu.Lock()
				slowAt = append(slowAt, float64(t0.Sub(wall0).Milliseconds())/1000)
				slowMu.Unlock()
			}
		}()
	}
	tick.Stop()
	posts.Wait()
	elapsed := time.Since(wall0)
	cpu := srv.cpuSeconds(t) - cpu0
	time.Sleep(3 * time.Second) // the last deliveries
	rss, peak := srv.memory(t)
	for _, m := range kept {
		m.Close()
	}
	owner.Close()

	result := map[string]any{
		"members": members, "online": online, "target_rate": rate, "duration_s": duration.Seconds(),
		"server_cpus":        srv.cpus,
		"join_seconds":       joinTime.Seconds(),
		"connect":            connectLat.summary(),
		"join":               joinLat.summary(),
		"posted":             n,
		"post_errors":        postErr.Load(),
		"achieved_rate":      float64(n-int(postErr.Load())) / elapsed.Seconds(),
		"post_rtt":           postLat.summary(),
		"slow_posts_at_s":    slowAt[:min(len(slowAt), 40)],
		"delivery":           delivery.summary(),
		"deliveries_counted": delivered.Load(),
		"server_cpu_cores":   cpu / elapsed.Seconds(),
		"server_rss_mib":     rss,
		"server_peak_mib":    peak,
	}
	report(t, "community", result)

	if e := postErr.Load(); e > 0 {
		t.Errorf("%d of %d posts failed (first: %v)", e, n, firstErr.Load())
	}
	ok := int64(n) - postErr.Load()
	if want := ok * int64(online-sampled); delivered.Load() < want {
		t.Errorf("watchers got %d deliveries, want %d", delivered.Load(), want)
	}
	if got, want := delivery.summary().Count, int(ok)*sampled; got < want {
		t.Errorf("sampled watchers decrypted %d messages, want %d", got, want)
	}
	if s := postLat.summary(); s.P95 > 500 {
		t.Errorf("post p95 %.0f ms (over 500 ms)", s.P95)
	}
	if s := delivery.summary(); s.P95 > 1000 {
		t.Errorf("delivery p95 %.0f ms (over 1 s)", s.P95)
	}
	if peak > 4096 {
		t.Errorf("server peak memory %.0f MiB (over 4 GiB)", peak)
	}
}

// startRelay runs the relay binary on its own cores (LOAD_RELAY_CPUS, "2,3"
// by default), apart from the server under test and from the clients.
func startRelay(t testing.TB) string {
	t.Helper()
	port := freePort(t)
	startPinned(t, envString("LOAD_RELAY_CPUS", "2,3"), []string{fmt.Sprintf("OPENARCANUM_RELAY_LISTEN=127.0.0.1:%d", port)},
		serverBinary(t, "relay"))
	waitPort(t, port)
	return fmt.Sprintf("ws://127.0.0.1:%d/ws", port)
}

func claimCode(t testing.TB, out string) *wire.ClaimCode {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, community.ClaimPrefix) {
			continue
		}
		b, err := identity.Decode(strings.TrimPrefix(strings.TrimSpace(line), community.ClaimPrefix))
		if err != nil {
			t.Fatal(err)
		}
		var code wire.ClaimCode
		if err := proto.Unmarshal(b, &code); err != nil {
			t.Fatal(err)
		}
		return &code
	}
	t.Fatalf("no claim code in `community init` output:\n%s", out)
	return nil
}

// connect retries while the server attaches to its relay.
func connect(t testing.TB, ctx context.Context, relayURL string, serverKey []byte, name string) *testutil.Member {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		m := testutil.NewMember(t, name)
		err := m.Connect(ctx, relayURL, serverKey)
		if err == nil {
			return m
		}
		if time.Now().After(deadline) {
			t.Fatalf("connect: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
