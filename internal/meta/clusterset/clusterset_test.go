package clusterset

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

func node(url string) *wire.MaintainerNode {
	k, _ := identity.Generate()
	return &wire.MaintainerNode{Url: url, NodeKey: k.Public, Role: RoleMember}
}

func sign(t *testing.T, op *identity.Key, seq uint64, issued time.Time, life time.Duration, nodes ...*wire.MaintainerNode) *wire.SignedMaintainerSet {
	t.Helper()
	s, err := Sign(op, &wire.MaintainerSet{Seq: seq, IssuedAt: issued.UnixMilli(), ExpiresAt: issued.Add(life).UnixMilli(), Nodes: nodes})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestVerify(t *testing.T) {
	now := time.Now()
	op, _ := identity.Generate()
	other, _ := identity.Generate()
	good := sign(t, op, 1, now, DefaultLifetime, node("https://m1.example.invalid"), node("https://m2.example.invalid"))
	if set, err := Verify(good, op.Public, now); err != nil || len(set.Nodes) != 2 {
		t.Fatalf("a good set: %v", err)
	}
	if _, err := Verify(good, nil, now); err != nil {
		t.Fatalf("unpinned verification: %v", err)
	}
	if _, err := Verify(good, other.Public, now); err == nil {
		t.Fatal("a set for another operator key was accepted")
	}
	forged := sign(t, other, 1, now, DefaultLifetime, node("https://m1.example.invalid"))
	var inner wire.MaintainerSet
	_ = proto.Unmarshal(forged.Set, &inner)
	inner.OperatorKey = op.Public
	forged.Set, _ = proto.Marshal(&inner)
	if _, err := Verify(forged, op.Public, now); err == nil {
		t.Fatal("a set signed by another key was accepted")
	}
	tampered := proto.Clone(good).(*wire.SignedMaintainerSet)
	tampered.Set = append(bytes.Clone(tampered.Set), 0x28, 0x01)
	if _, err := Verify(tampered, op.Public, now); err == nil {
		t.Fatal("a tampered set was accepted")
	}
	for name, s := range map[string]*wire.SignedMaintainerSet{
		"expired":      sign(t, op, 1, now.Add(-2*time.Hour), time.Hour, node("https://m1.example.invalid")),
		"future":       sign(t, op, 1, now.Add(48*time.Hour), DefaultLifetime, node("https://m1.example.invalid")),
		"too long":     sign(t, op, 1, now, 3*365*24*time.Hour, node("https://m1.example.invalid")),
		"empty":        sign(t, op, 1, now, DefaultLifetime),
		"bad url":      sign(t, op, 1, now, DefaultLifetime, node("ftp://m1.example.invalid")),
		"trailing":     sign(t, op, 1, now, DefaultLifetime, node("https://m1.example.invalid/")),
		"nine":         sign(t, op, 1, now, DefaultLifetime, node("https://1.invalid"), node("https://2.invalid"), node("https://3.invalid"), node("https://4.invalid"), node("https://5.invalid"), node("https://6.invalid"), node("https://7.invalid"), node("https://8.invalid"), node("https://9.invalid")),
		"dup url":      sign(t, op, 1, now, DefaultLifetime, node("https://m1.example.invalid"), node("https://m1.example.invalid")),
		"bad role":     sign(t, op, 1, now, DefaultLifetime, &wire.MaintainerNode{Url: "https://m1.example.invalid", NodeKey: op.Public, Role: "boss"}),
		"short key":    sign(t, op, 1, now, DefaultLifetime, &wire.MaintainerNode{Url: "https://m1.example.invalid", NodeKey: []byte{1}, Role: RoleRoot}),
		"not a proto":  {Set: []byte{0xff}, Signature: op.Sign(identity.CtxMaintainerSet, []byte{0xff})},
		"wrong ctx":    {Set: good.Set, Signature: op.Sign(identity.CtxRoute, good.Set)},
		"no signature": {Set: good.Set},
	} {
		if _, err := Verify(s, op.Public, now); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseEntry(t *testing.T) {
	op, _ := identity.Generate()
	u, k, err := ParseEntry("https://m.example.invalid/#" + identity.Encode(op.Public))
	if err != nil || u != "https://m.example.invalid" || !bytes.Equal(k, op.Public) {
		t.Fatalf("%q %v %v", u, k, err)
	}
	if u, k, err := ParseEntry("http://m.example.invalid"); err != nil || k != nil || u != "http://m.example.invalid" {
		t.Fatal("a plain URL entry")
	}
	for _, bad := range []string{"m.example.invalid", "https://m#short", "ftp://m"} {
		if _, _, err := ParseEntry(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// The cache expands a cluster entry into its members, keeps the newest seq
// and serves the cached set when the node it came from is down.
func TestCacheExpands(t *testing.T) {
	now := time.Now()
	op, _ := identity.Generate()
	var serve atomicSet
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := serve.get()
		if s == nil || r.URL.Path != "/v1/maintainers" {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		b, _ := proto.Marshal(s)
		_, _ = w.Write(b)
	}))
	defer hs.Close()
	serve.set(sign(t, op, 2, now, DefaultLifetime, &wire.MaintainerNode{Url: hs.URL, NodeKey: op.Public, Role: RoleRoot}, node("https://m2.example.invalid")))
	c := NewCache(hs.Client(), time.Hour)
	entry := hs.URL + "#" + identity.Encode(op.Public)
	got := c.Expand(context.Background(), []string{entry, "https://plain.example.invalid"}, nil)
	if strings.Join(got, " ") != hs.URL+" https://m2.example.invalid https://plain.example.invalid" {
		t.Fatalf("expanded: %v", got)
	}
	serve.set(nil)
	c.Now = func() time.Time { return now.Add(2 * time.Hour) }
	if got := c.Expand(context.Background(), []string{entry}, nil); len(got) != 2 {
		t.Fatalf("cached set lost while its node is down: %v", got)
	}
}

type atomicSet struct {
	ch chan *wire.SignedMaintainerSet
}

func (a *atomicSet) set(s *wire.SignedMaintainerSet) {
	if a.ch == nil {
		a.ch = make(chan *wire.SignedMaintainerSet, 1)
	}
	select {
	case <-a.ch:
	default:
	}
	a.ch <- s
}

func (a *atomicSet) get() *wire.SignedMaintainerSet {
	s := <-a.ch
	a.ch <- s
	return s
}
