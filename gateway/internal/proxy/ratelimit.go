package proxy

import (
	"container/heap"
	"container/list"
	"sync"
	"time"
)

const (
	defaultRateLimit = 50
	defaultRateBurst = 100
	maxRateKids      = 10000
	rateIdleTTL      = 5 * time.Minute
)

type rateBucket struct {
	kid     string
	tokens  float64
	last    time.Time
	expires float64 // seconds from the limiter's origin, including time to refill
	index   int
	recent  *list.Element
}

type rateExpiryHeap []*rateBucket

func (h rateExpiryHeap) Len() int           { return len(h) }
func (h rateExpiryHeap) Less(i, j int) bool { return h[i].expires < h[j].expires }
func (h rateExpiryHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index, h[j].index = i, j
}
func (h *rateExpiryHeap) Push(value any) {
	b := value.(*rateBucket)
	b.index = len(*h)
	*h = append(*h, b)
}
func (h *rateExpiryHeap) Pop() any {
	old := *h
	b := old[len(old)-1]
	old[len(old)-1] = nil
	b.index = -1
	*h = old[:len(old)-1]
	return b
}

// rateLimiter bounds per-credential state and serializes token/LRU/expiry updates.
// Expiry is lazy: the next request removes entries full and idle for five minutes.
type rateLimiter struct {
	mu      sync.Mutex
	rate    float64
	burst   float64
	now     func() time.Time
	origin  time.Time
	buckets map[string]*rateBucket
	recent  list.List
	expiry  rateExpiryHeap
}

func newRateLimiter(rate float64, burst int, now func() time.Time) *rateLimiter {
	if rate == 0 {
		return nil
	}
	if now == nil {
		now = time.Now
	}
	return &rateLimiter{
		rate: rate, burst: float64(burst), now: now, origin: now(),
		buckets: make(map[string]*rateBucket),
	}
}

func (l *rateLimiter) remove(b *rateBucket) {
	heap.Remove(&l.expiry, b.index)
	l.recent.Remove(b.recent)
	delete(l.buckets, b.kid)
}

// evict runs under mu, keeping both expiry and LRU bookkeeping bounded.
func (l *rateLimiter) evict(seconds float64, kid string) {
	for len(l.expiry) > 0 && l.expiry[0].expires <= seconds {
		l.remove(l.expiry[0])
	}
	if l.buckets[kid] == nil && len(l.buckets) >= maxRateKids {
		l.remove(l.recent.Back().Value.(*rateBucket))
	}
}

func (l *rateLimiter) allow(kid string) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	seconds := now.Sub(l.origin).Seconds()
	l.evict(seconds, kid)
	b := l.buckets[kid]
	if b == nil {
		b = &rateBucket{kid: kid, tokens: l.burst, last: now}
		b.recent = l.recent.PushFront(b)
		l.buckets[kid] = b
		heap.Push(&l.expiry, b)
	} else if now.After(b.last) {
		b.tokens = min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	}
	allowed := b.tokens >= 1
	if allowed {
		b.tokens--
	}
	// A backward clock jump must not give the same refill interval twice.
	if now.After(b.last) {
		b.last = now
	}
	b.expires = b.last.Sub(l.origin).Seconds() + (l.burst-b.tokens)/l.rate + rateIdleTTL.Seconds()
	heap.Fix(&l.expiry, b.index)
	l.recent.MoveToFront(b.recent)
	return allowed
}
