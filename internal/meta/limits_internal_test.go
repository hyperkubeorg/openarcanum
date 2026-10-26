package meta

import (
	"fmt"
	"testing"
	"time"
)

// A limit table holds at most its maximum number of keys. When every held
// key is still within the window, a new key evicts the least recently used
// one (refusing it instead let anyone with enough addresses, such as the
// /64s of one IPv6 /48, lock every newcomer out; per-prefix limits bound
// what such a client gets, TestPrefixLimits); keys whose window has passed
// are dropped from the old end as the table is used.
func TestLimitTableIsBounded(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	r := &rateLimit{limit: 2, t: newKeyed[fixedWindow](time.Minute, 3)}
	for _, k := range []string{"a", "b", "c"} {
		if !r.allow(k, now) {
			t.Fatalf("%s refused", k)
		}
	}
	// A held key still works, and its count holds; a is now the most
	// recently used.
	if !r.allow("a", now.Add(2*time.Second)) || r.allow("a", now.Add(3*time.Second)) {
		t.Fatal("held key's limit")
	}
	if !r.allow("d", now.Add(4*time.Second)) {
		t.Fatal("a new key was refused by a full table")
	}
	if n := r.t.len(); n != 3 {
		t.Fatalf("table holds %d keys, want 3", n)
	}
	// b (least recently used) went; a's count is kept.
	if r.allow("a", now.Add(5*time.Second)) {
		t.Fatal("the most recently used key's usage was forgotten")
	}
	// b, c and d are a minute old: they go; a was used since.
	if !r.allow("e", now.Add(time.Minute+4*time.Second)) {
		t.Fatal("new key refused after old ones expired")
	}
	if n := r.t.len(); n != 2 {
		t.Fatalf("table holds %d keys, want 2 (a, e)", n)
	}
	// a's window started at now: a minute later it counts afresh.
	if !r.allow("a", now.Add(time.Minute+2*time.Second)) {
		t.Fatal("a's new window")
	}
}

// Per-network limits (an IPv4 address, an IPv6 /64) are capped per IPv6
// /48 too: the 65 536 /64s of one /48 share prefixFactor networks' worth.
func TestPrefixLimits(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	r := newNetRateLimit(2, time.Minute)
	allowed := 0
	for i := range 100 {
		ip := fmt.Sprintf("2001:db8:1:%x::1", i)
		for range 2 {
			if r.allow(ip, now) {
				allowed++
			}
		}
	}
	if allowed != 2*prefixFactor {
		t.Fatalf("one /48 got %d, want %d", allowed, 2*prefixFactor)
	}
	if !r.allow("2001:db8:2::1", now) || !r.allow("192.0.2.1", now) || !r.allow("192.0.2.2", now) {
		t.Fatal("other networks refused")
	}
	b := newNetByteBudget(100, time.Hour)
	charged := 0
	for i := range 100 {
		if b.charge(fmt.Sprintf("2001:db8:1:%x::1", i), 100, now) {
			charged++
		}
	}
	if charged != prefixFactor {
		t.Fatalf("one /48 charged %d full budgets, want %d", charged, prefixFactor)
	}
}

// Many distinct keys over time never grow a table past its bound.
func TestLimitTableChurn(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	r := &rateLimit{limit: 1, t: newKeyed[fixedWindow](time.Minute, 1000)}
	for i := range 100_000 {
		r.allow(fmt.Sprint(i), now.Add(time.Duration(i)*time.Millisecond*10))
		if r.t.len() > 1000 {
			t.Fatalf("table grew to %d", r.t.len())
		}
	}
}

func TestByteBudgetRollingWindow(t *testing.T) {
	now := time.Unix(1_000_000, 0).Truncate(time.Hour)
	b := newByteBudget(100, 24*time.Hour)
	if !b.charge("k", 60, now) || !b.charge("k", 40, now.Add(time.Hour)) {
		t.Fatal("within the budget")
	}
	if b.charge("k", 1, now.Add(2*time.Hour)) {
		t.Fatal("over the budget")
	}
	if !b.charge("other", 100, now) {
		t.Fatal("another key's budget")
	}
	// 24 hours after the first charge its 60 bytes are out of the window.
	if !b.charge("k", 60, now.Add(24*time.Hour)) {
		t.Fatal("the first charge didn't leave the window")
	}
	if b.charge("k", 1, now.Add(24*time.Hour+time.Minute)) {
		t.Fatal("the second charge left the window too early")
	}
	// Long idle: everything is out.
	if !b.charge("k", 100, now.Add(30*24*time.Hour)) {
		t.Fatal("after a month idle")
	}
}
