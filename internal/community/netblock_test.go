package community_test

import (
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Network blocks on a dedicated relay (docs/protocol.md §2 "Dedicated
// relays", §4.7 "Network blocks").

func dedicatedEnv(t *testing.T, blockFile string) *env {
	t.Helper()
	rs, c := testutil.StartDedicated(t, blockFile)
	return claimedOn(t, rs[0], c, nil)
}

// memberFrom joins a member whose relay connection leaves from source.
func (e *env) memberFrom(name, source string) *testutil.Member {
	e.t.Helper()
	m := testutil.NewMember(e.t, name)
	m.HTTP = testutil.From(source)
	ctx := testutil.Ctx(e.t)
	if err := m.Connect(ctx, e.relay.URL, e.serverKey()); err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(m.Close)
	res, err := m.Join(ctx, e.invite.Secret)
	if err != nil || res.Status != wire.JoinStatus_JOIN_STATUS_ADMITTED {
		e.t.Fatalf("%s not admitted: %v %v", name, res, err)
	}
	return m
}

func banWithBlock(member []byte, seconds uint32) *wire.AdminAction {
	return &wire.AdminAction{Op: &wire.AdminAction_Ban{Ban: &wire.BanMember{Member: member, BlockNetwork: true, BlockSeconds: seconds}}}
}

func unban(member []byte) *wire.AdminAction {
	return &wire.AdminAction{Op: &wire.AdminAction_Unban{Unban: &wire.Unban{Member: member}}}
}

func (e *env) bans() []*wire.Ban {
	e.t.Helper()
	r, err := e.owner.Call(testutil.Ctx(e.t), &wire.ClientMessage{Body: &wire.ClientMessage_ListBans{ListBans: &wire.ListBans{}}})
	if err != nil {
		e.t.Fatal(err)
	}
	return r.GetBans().Bans
}

func waitDisconnected(t *testing.T, m *testutil.Member) {
	t.Helper()
	select {
	case <-m.Relay.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("member's relay connection wasn't closed")
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// auditClean checks the audit log names the block by id and holds no
// address.
func (e *env) auditClean(t *testing.T, addrs ...string) {
	t.Helper()
	got, err := e.comm.Server.AuditSummaries(testutil.Ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range got {
		for _, a := range addrs {
			if strings.Contains(s, a) {
				t.Fatalf("audit entry %q contains the address %s", s, a)
			}
		}
	}
}

func TestBanBlocksNetworkOnDedicatedRelay(t *testing.T) {
	e := dedicatedEnv(t, "")
	ctx := testutil.Ctx(t)
	if !fresh(t, e.owner).DedicatedRelay {
		t.Fatal("state doesn't say the community is on a dedicated relay")
	}
	mallory := e.memberFrom("Mallory", "127.0.0.2")
	bystander := e.memberFrom("Bystander", "127.0.0.3")

	res := e.mustAdmin(e.owner, banWithBlock(mallory.Identity.Public, 0))
	if res.NetworkBlock != wire.NetworkBlockStatus_NETWORK_BLOCK_STATUS_BLOCKED {
		t.Fatalf("network block: %v", res.NetworkBlock)
	}
	waitDisconnected(t, mallory)
	// She was told she's banned before the relay cut her off.
	notified := false
	for ev := range mallory.Events {
		if ev.GetStateChanged().GetStatus() == wire.MemberStatus_MEMBER_STATUS_BANNED {
			notified = true
		}
	}
	if !notified {
		t.Fatal("the banned member never got the ban notice")
	}
	if !e.relay.Server.Blocked("127.0.0.2") {
		t.Fatal("relay doesn't block the address")
	}
	// Coming back from the same address is refused by the relay itself.
	again := testutil.NewMember(t, "Mallory again")
	again.Identity = mallory.Identity
	again.HTTP = testutil.From("127.0.0.2")
	if err := again.Connect(ctx, e.relay.URL, e.serverKey()); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("reconnect from the blocked address: %v", err)
	}
	if _, err := bystander.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_Sync{Sync: &wire.Sync{}}}); err != nil {
		t.Fatalf("a member on another address lost their session: %v", err)
	}

	bans := e.bans()
	if len(bans) != 1 || bans[0].NetworkBlock != wire.NetworkBlockStatus_NETWORK_BLOCK_STATUS_BLOCKED || bans[0].NetworkBlockUntil != 0 {
		t.Fatalf("ban list: %v", bans)
	}
	e.auditHas(t, "blocked the network address of Mallory on the community's relay until the ban is lifted (block ")
	e.auditHas(t, "banned Mallory (network address block requested)")

	// Unbanning lifts the block by its id.
	e.mustAdmin(e.owner, unban(mallory.Identity.Public))
	if e.relay.Server.Blocked("127.0.0.2") {
		t.Fatal("block still there after the unban")
	}
	e.auditHas(t, "lifted the network block on Mallory (block ")
	e.auditClean(t, "127.0.0.2", "127.0.0.3", "127.0.0.1")
	// The relay lets the address in again.
	if err := again.Connect(ctx, e.relay.URL, e.serverKey()); err != nil {
		t.Fatalf("reconnect after the unban: %v", err)
	}
	again.Close()
}

func TestTimedNetworkBlock(t *testing.T) {
	e := dedicatedEnv(t, "")
	mallory := e.memberFrom("Mallory", "127.0.0.2")
	e.mustAdmin(e.owner, banWithBlock(mallory.Identity.Public, 86400))
	bans := e.bans()
	day := time.Now().Add(24 * time.Hour).UnixMilli()
	if bans[0].NetworkBlock != wire.NetworkBlockStatus_NETWORK_BLOCK_STATUS_BLOCKED || bans[0].NetworkBlockUntil < day-60_000 || bans[0].NetworkBlockUntil > day+60_000 {
		t.Fatalf("ban list: %v", bans)
	}
	e.auditHas(t, "blocked the network address of Mallory on the community's relay for 1 day")
}

func TestNetworkBlockPendingUntilReconnect(t *testing.T) {
	e := dedicatedEnv(t, "")
	ctx := testutil.Ctx(t)
	mallory := e.memberFrom("Mallory", "127.0.0.2")
	mallory.Close()
	eventually(t, "member still online", func() bool {
		for _, ss := range e.comm.Server.OnlineMembers() {
			if string(ss) == string(mallory.Identity.Public) {
				return false
			}
		}
		return true
	})

	res := e.mustAdmin(e.owner, banWithBlock(mallory.Identity.Public, 0))
	if res.NetworkBlock != wire.NetworkBlockStatus_NETWORK_BLOCK_STATUS_PENDING {
		t.Fatalf("offline member's block: %v", res.NetworkBlock)
	}
	if b := e.bans(); b[0].NetworkBlock != wire.NetworkBlockStatus_NETWORK_BLOCK_STATUS_PENDING {
		t.Fatalf("ban list: %v", b)
	}
	// They come back from another address: refused as banned, and that
	// address is blocked.
	back := testutil.NewMember(t, "Mallory")
	back.Identity = mallory.Identity
	back.HTTP = testutil.From("127.0.0.4")
	if err := back.Connect(ctx, e.relay.URL, e.serverKey()); err == nil && back.Status.Status != wire.MemberStatus_MEMBER_STATUS_BANNED {
		t.Fatalf("banned member admitted: %v", back.Status)
	}
	eventually(t, "address not blocked on reconnect", func() bool { return e.relay.Server.Blocked("127.0.0.4") })
	if b := e.bans(); b[0].NetworkBlock != wire.NetworkBlockStatus_NETWORK_BLOCK_STATUS_BLOCKED {
		t.Fatalf("ban list after reconnect: %v", b)
	}
	e.auditHas(t, "when they tried to reconnect")
}

func TestNetworkBlockSkipsServersOwnNetwork(t *testing.T) {
	e := dedicatedEnv(t, "")
	neighbour := e.memberFrom("Neighbour", "127.0.0.1")
	res := e.mustAdmin(e.owner, banWithBlock(neighbour.Identity.Public, 0))
	if res.NetworkBlock != wire.NetworkBlockStatus_NETWORK_BLOCK_STATUS_SKIPPED {
		t.Fatalf("got %v", res.NetworkBlock)
	}
	if e.relay.Server.BlockedNetworks() != 0 {
		t.Fatal("the server's own network was blocked")
	}
	if b := e.bans(); b[0].NetworkBlock != wire.NetworkBlockStatus_NETWORK_BLOCK_STATUS_UNSPECIFIED {
		t.Fatalf("ban list: %v", b)
	}
	e.auditHas(t, "it is the community server's own")
}

func TestNetworkBlockEnforcement(t *testing.T) {
	e := dedicatedEnv(t, "")
	mod := e.memberFrom("Mod", "127.0.0.5")
	victim := e.memberFrom("Victim", "127.0.0.6")
	e.role("kickers", community.PermKick, mod)

	// Ban Members is needed, also for the network block.
	if _, err := e.adminAs(mod, banWithBlock(victim.Identity.Public, 0)); err == nil || !strings.Contains(err.Error(), "permission") {
		t.Fatalf("block without Ban Members: %v", err)
	}
	if e.relay.Server.BlockedNetworks() != 0 {
		t.Fatal("blocked without permission")
	}
	// Only the documented durations.
	if _, err := e.adminAs(e.owner, banWithBlock(victim.Identity.Public, 3600)); err == nil {
		t.Fatal("a 1-hour block was accepted")
	}
	// A member with Ban Members may.
	e.role("banners", community.PermBan, mod)
	if res := e.mustAdmin(mod, banWithBlock(victim.Identity.Public, 604800)); res.NetworkBlock != wire.NetworkBlockStatus_NETWORK_BLOCK_STATUS_BLOCKED {
		t.Fatalf("got %v", res.NetworkBlock)
	}
}

func TestNetworkBlockNeedsDedicatedRelay(t *testing.T) {
	e := setup(t, nil)
	if fresh(t, e.owner).DedicatedRelay {
		t.Fatal("an ordinary relay counts as dedicated")
	}
	victim := e.member("Victim", e.invite.Secret)
	_, err := e.adminAs(e.owner, banWithBlock(victim.Identity.Public, 0))
	if err == nil || !strings.Contains(err.Error(), "dedicated relay") {
		t.Fatalf("got %v", err)
	}
	// The ban didn't happen either.
	if len(e.bans()) != 0 {
		t.Fatal("refused request still banned")
	}
	// A plain ban is unaffected.
	if res := e.mustAdmin(e.owner, &wire.AdminAction{Op: &wire.AdminAction_Ban{Ban: &wire.BanMember{Member: victim.Identity.Public}}}); res.NetworkBlock != wire.NetworkBlockStatus_NETWORK_BLOCK_STATUS_UNSPECIFIED {
		t.Fatalf("plain ban: %v", res.NetworkBlock)
	}
}

// A relay keeps its blocks across a restart, and the server, attached again,
// lifts them by id.
func TestNetworkBlockSurvivesRelayRestart(t *testing.T) {
	blockFile := filepath.Join(t.TempDir(), "blocks.json")
	e := dedicatedEnv(t, blockFile)
	mallory := e.memberFrom("Mallory", "127.0.0.2")
	e.mustAdmin(e.owner, banWithBlock(mallory.Identity.Public, 0))
	r2 := restartRelay(t, e.comm, e.relay, blockFile, 0)
	if !r2.Server.Blocked("127.0.0.2") {
		t.Fatal("block lost across the relay restart")
	}
	e.relay = r2
	e.owner = reconnect(t, e.owner, r2, e.serverKey())
	e.mustAdmin(e.owner, unban(mallory.Identity.Public))
	if r2.Server.Blocked("127.0.0.2") {
		t.Fatal("block not lifted")
	}
}

// Unbanning while the relay holding the block is down: the server asks it
// again once it is attached to it again.
func TestNetworkUnblockRetriedWhenRelayReturns(t *testing.T) {
	fileA := filepath.Join(t.TempDir(), "a.json")
	rs, c := testutil.StartDedicated(t, fileA, "")
	a, b := rs[0], rs[1]
	e := claimedOn(t, b, c, nil) // the owner connects through b
	if !fresh(t, e.owner).DedicatedRelay {
		t.Fatal("two dedicated relays don't count as dedicated")
	}
	e.relay = a
	mallory := e.memberFrom("Mallory", "127.0.0.2") // through a
	if res := e.mustAdmin(e.owner, banWithBlock(mallory.Identity.Public, 0)); res.NetworkBlock != wire.NetworkBlockStatus_NETWORK_BLOCK_STATUS_BLOCKED {
		t.Fatalf("got %v", res.NetworkBlock)
	}
	a.Stop()
	eventually(t, "server still attached to the stopped relay", func() bool { return len(c.Server.AttachedRelays()) == 1 })
	e.mustAdmin(e.owner, unban(mallory.Identity.Public))
	e.auditHas(t, "and asked the relay to lift their network block")

	a2 := restartRelay(t, c, a, fileA, 2)
	eventually(t, "block not lifted after the relay came back", func() bool { return !a2.Server.Blocked("127.0.0.2") })
	eventually(t, "lift not audited", func() bool {
		got, _ := c.Server.AuditSummaries(testutil.Ctx(t))
		for _, s := range got {
			if strings.Contains(s, "lifted the network block on Mallory (block ") {
				return true
			}
		}
		return false
	})
}

// A member with devices on two dedicated relays is blocked on both, each
// block lifted by its own id.
func TestNetworkBlockOnEveryDedicatedRelay(t *testing.T) {
	rs, c := testutil.StartDedicated(t, "", "")
	a, b := rs[0], rs[1]
	e := claimedOn(t, a, c, nil)
	e.relay = a
	mallory := e.memberFrom("Mallory", "127.0.0.2")
	second := testutil.NewMember(t, "Mallory")
	second.Identity = mallory.Identity // another device of hers, through b
	second.HTTP = testutil.From("127.0.0.3")
	if err := second.Connect(testutil.Ctx(t), b.URL, e.serverKey()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(second.Close)
	if res := e.mustAdmin(e.owner, banWithBlock(mallory.Identity.Public, 0)); res.NetworkBlock != wire.NetworkBlockStatus_NETWORK_BLOCK_STATUS_BLOCKED {
		t.Fatalf("got %v", res.NetworkBlock)
	}
	if !a.Server.Blocked("127.0.0.2") || !b.Server.Blocked("127.0.0.3") {
		t.Fatal("not blocked on both relays")
	}
	if bans := e.bans(); bans[0].NetworkBlock != wire.NetworkBlockStatus_NETWORK_BLOCK_STATUS_BLOCKED {
		t.Fatalf("ban list: %v", bans)
	}
	e.mustAdmin(e.owner, unban(mallory.Identity.Public))
	if a.Server.Blocked("127.0.0.2") || b.Server.Blocked("127.0.0.3") {
		t.Fatal("not lifted on both relays")
	}
}

// restartRelay stops r and serves a relay dedicated to the same server on
// its address with blockFile, waiting until c is attached to want relays.
func restartRelay(t *testing.T, c *testutil.Community, r *testutil.Relay, blockFile string, want int) *testutil.Relay {
	t.Helper()
	addr := strings.TrimSuffix(strings.TrimPrefix(r.URL, "ws://"), "/ws")
	attached := len(c.Server.AttachedRelays())
	r.Stop()
	if want == 0 {
		want = attached
		eventually(t, "server still attached", func() bool { return len(c.Server.AttachedRelays()) < attached })
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	r2 := testutil.StartDedicatedRelay(t, ln, c.Server.ServerKey(), blockFile)
	eventually(t, "server didn't reattach", func() bool { return len(c.Server.AttachedRelays()) == want })
	return r2
}

// reconnect opens a new session for m (same keys) through r.
func reconnect(t *testing.T, m *testutil.Member, r *testutil.Relay, serverKey []byte) *testutil.Member {
	t.Helper()
	n := testutil.NewMember(t, m.Name)
	n.Identity, n.Static = m.Identity, m.Static
	if err := n.Connect(testutil.Ctx(t), r.URL, serverKey); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(n.Close)
	return n
}

func TestServerPublicKeyFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "server.pub")
	r := testutil.StartRelay(t)
	c := testutil.StartCommunity(t, func(cfg *community.Config) { cfg.ServerPublicKeyFile = file }, r.URL)
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(b)) != hex.EncodeToString(c.Server.ServerKey()) {
		t.Fatalf("file holds %q", b)
	}
}
