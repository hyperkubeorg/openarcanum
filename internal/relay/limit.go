package relay

import (
	"sync"
	"time"
)

// bucket is a token bucket that may go into debt: take never refuses, it
// returns how long the caller should wait before continuing.
type bucket struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
}

func newBucket(rate, burst float64) *bucket {
	return &bucket{rate: rate, burst: burst, tokens: burst, last: time.Now()}
}

func (b *bucket) take(n float64) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.tokens += now.Sub(b.last).Seconds() * b.rate
	if b.tokens > b.burst {
		b.tokens = b.burst
	}
	b.last = now
	b.tokens -= n
	if b.tokens >= 0 {
		return 0
	}
	return time.Duration(-b.tokens / b.rate * float64(time.Second))
}

// set changes the rate and burst (a connection that becomes an endpoint
// host gets a larger budget). Growing the burst makes the extra available at
// once; tokens above a smaller burst are dropped.
func (b *bucket) set(rate, burst float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.tokens += now.Sub(b.last).Seconds() * b.rate
	b.last = now
	if burst > b.burst {
		b.tokens += burst - b.burst
	}
	b.rate, b.burst = rate, burst
	if b.tokens > b.burst {
		b.tokens = b.burst
	}
}

func (b *bucket) params() (rate, burst float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.rate, b.burst
}
