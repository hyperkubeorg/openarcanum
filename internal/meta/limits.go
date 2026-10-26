package meta

import (
	"container/list"
	"net/netip"
	"sync"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/ops"
)

// Per-client limits (docs/operations.md "Maintainer node"). Every limit
// keyed by a client address uses netKey: an IPv4 address, or the /64 of an
// IPv6 address, since one host usually owns a whole /64 and per-address
// keys would be free to multiply.
//
// Each limit's state lives in a keyed table that holds at most maxKeys
// keys. Entries are kept in the order they were last used; an entry unused
// for the table's whole window can't affect a decision any more, so every
// access first drops such entries from the old end (amortized constant
// time, no scan of the table). When the table is full of live entries, a
// new key evicts the least recently used one: refusing new keys instead let
// anyone with enough addresses (the 65 536 /64s of one IPv6 /48) lock
// every newcomer out. Limits keyed by client address are also capped per
// IPv6 /48 (netRateLimit, netByteBudget), so many /64s of one site share
// prefixFactor networks' worth, whatever eviction forgets.

// maxLimitKeys bounds every limit table.
const maxLimitKeys = 100_000

type keyedEntry[T any] struct {
	key  string
	last time.Time
	val  T
}

// keyed is a bounded table of per-key state, oldest use first.
type keyed[T any] struct {
	mu     sync.Mutex
	window time.Duration
	max    int
	m      map[string]*list.Element
	order  *list.List
}

func newKeyed[T any](window time.Duration, max int) *keyed[T] {
	return &keyed[T]{window: window, max: max, m: map[string]*list.Element{}, order: list.New()}
}

// with runs f on key's state (created if missing, evicting the least
// recently used key when the table is full) under the table's lock and
// marks the key used at now. It reports true.
func (k *keyed[T]) with(key string, now time.Time, f func(v *T)) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	for e := k.order.Front(); e != nil; e = k.order.Front() {
		ent := e.Value.(*keyedEntry[T])
		if now.Sub(ent.last) < k.window {
			break
		}
		k.order.Remove(e)
		delete(k.m, ent.key)
	}
	e := k.m[key]
	if e == nil {
		if len(k.m) >= k.max {
			old := k.order.Front()
			k.order.Remove(old)
			delete(k.m, old.Value.(*keyedEntry[T]).key)
		}
		e = k.order.PushBack(&keyedEntry[T]{key: key})
		k.m[key] = e
	} else {
		k.order.MoveToBack(e)
	}
	ent := e.Value.(*keyedEntry[T])
	if now.After(ent.last) {
		ent.last = now
	}
	f(&ent.val)
	return true
}

// len reports how many keys the table holds (tests).
func (k *keyed[T]) len() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.m)
}

// rateLimit allows limit events per key in each fixed window.
type rateLimit struct {
	limit int
	t     *keyed[fixedWindow]
}

type fixedWindow struct {
	start time.Time
	n     int
}

func newRateLimit(limit int, per time.Duration) *rateLimit {
	return &rateLimit{limit: limit, t: newKeyed[fixedWindow](per, maxLimitKeys)}
}

func (r *rateLimit) allow(key string, now time.Time) bool {
	ok := false
	if !r.t.with(key, now, func(w *fixedWindow) {
		if w.start.IsZero() || now.Sub(w.start) >= r.t.window {
			*w = fixedWindow{start: now}
		}
		if w.n < r.limit {
			w.n++
			ok = true
		}
	}) {
		return false
	}
	return ok
}

// byteBudget allows max bytes per key over a rolling window, counted in
// slots of window/budgetSlots (so state per key is fixed, however many
// requests it makes).
type byteBudget struct {
	max uint64
	t   *keyed[budgetSlots]
}

const budgetSlotCount = 24

type budgetSlots struct {
	// slot[i] counts the bytes of slot number base+i.
	base int64
	slot [budgetSlotCount]uint64
}

func newByteBudget(max uint64, window time.Duration) *byteBudget {
	return &byteBudget{max: max, t: newKeyed[budgetSlots](window, maxLimitKeys)}
}

func (b *byteBudget) slotLen() time.Duration { return b.t.window / budgetSlotCount }

// charge adds n bytes to key's use if that stays within the budget.
func (b *byteBudget) charge(key string, n uint64, now time.Time) bool {
	ok := false
	if !b.t.with(key, now, func(s *budgetSlots) {
		cur := now.UnixNano() / int64(b.slotLen())
		// Slide the window so the current slot is the last one.
		if shift := cur - (s.base + budgetSlotCount - 1); shift > 0 {
			if shift >= budgetSlotCount {
				s.slot = [budgetSlotCount]uint64{}
			} else {
				copy(s.slot[:], s.slot[shift:])
				clear(s.slot[budgetSlotCount-shift:])
			}
			s.base += shift
		}
		var used uint64
		for _, v := range s.slot {
			used += v
		}
		if used+n <= b.max {
			s.slot[budgetSlotCount-1] += n
			ok = true
		}
	}) {
		return false
	}
	return ok
}

// prefixFactor is how many networks' worth of a per-network limit one IPv6
// /48 gets in all.
const prefixFactor = 16

// netKeys is the network key of a client address (ops.NetworkKey) and, for
// IPv6, its /48 ("" for IPv4).
func netKeys(ip string) (network, prefix string) {
	network = ops.NetworkKey(ip)
	if a, err := netip.ParseAddr(ip); err == nil && a.Is6() && !a.Is4In6() {
		p, _ := a.WithZone("").Prefix(48)
		prefix = p.String()
	}
	return network, prefix
}

// netRateLimit is a rateLimit per client network, capped per IPv6 /48.
type netRateLimit struct{ network, prefix *rateLimit }

func newNetRateLimit(limit int, per time.Duration) *netRateLimit {
	return &netRateLimit{newRateLimit(limit, per), newRateLimit(limit*prefixFactor, per)}
}

// allow takes one event for client address ip.
func (l *netRateLimit) allow(ip string, now time.Time) bool {
	network, prefix := netKeys(ip)
	if prefix != "" && !l.prefix.allow(prefix, now) {
		return false
	}
	return l.network.allow(network, now)
}

// netByteBudget is a byteBudget per client network, capped per IPv6 /48.
type netByteBudget struct{ network, prefix *byteBudget }

func newNetByteBudget(max uint64, window time.Duration) *netByteBudget {
	return &netByteBudget{newByteBudget(max, window), newByteBudget(max*prefixFactor, window)}
}

// charge adds n bytes for client address ip if that stays within both
// budgets.
func (b *netByteBudget) charge(ip string, n uint64, now time.Time) bool {
	network, prefix := netKeys(ip)
	if prefix != "" && !b.prefix.charge(prefix, n, now) {
		return false
	}
	return b.network.charge(network, n, now)
}
