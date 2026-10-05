// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

// Package ratelimit provides bounded, framework-neutral keyed token buckets.
// Keys are evicted least-recently-used in O(1); idle pruning is amortized O(1).
// Eviction resets a key's budget. Untrusted key churn can defeat per-key limits:
// use authenticated keys and an independent global limiter at trust boundaries.
package ratelimit

import (
	"container/list"
	"fmt"
	"math"
	"sync"
	"time"

	"golang.org/x/time/rate"
	"k8s.io/utils/clock"
)

// Config defines bounded bucket storage.
type Config struct {
	// Rate is tokens per second and must be positive and at most 1e9.
	// Higher rates exceed x/time/rate's nanosecond scheduling resolution.
	Rate rate.Limit
	// Burst is each key's initial and maximum tokens; must be positive.
	Burst int
	// MaxKeys is the maximum stored keys; must be positive.
	MaxKeys int
	// IdleTTL expires inactive keys; must be positive.
	IdleTTL time.Duration
	// Clock defaults to the real clock and must advance monotonically.
	Clock clock.PassiveClock
}

type entry struct {
	key    string
	seen   time.Time
	bucket *rate.Limiter
}

// Limiter manages independent buckets using an LRU list.
type Limiter struct {
	mu    sync.Mutex
	cfg   Config
	items map[string]*list.Element
	lru   *list.List
}

// New validates cfg and constructs a limiter without background goroutines.
func New(cfg Config) (*Limiter, error) {
	if cfg.Rate <= 0 || cfg.Rate > rate.Limit(time.Second) || math.IsNaN(float64(cfg.Rate)) ||
		cfg.Burst <= 0 || cfg.MaxKeys <= 0 || cfg.IdleTTL <= 0 {
		return nil, fmt.Errorf("rate must be in (0, 1e9]; burst, max keys and idle TTL must be positive")
	}
	if cfg.Clock == nil {
		cfg.Clock = clock.RealClock{}
	}
	return &Limiter{cfg: cfg, items: make(map[string]*list.Element), lru: list.New()}, nil
}

func (l *Limiter) removeOldest() {
	e := l.lru.Back()
	delete(l.items, e.Value.(*entry).key)
	l.lru.Remove(e)
}

func (l *Limiter) prune(now time.Time) {
	for e := l.lru.Back(); e != nil && now.Sub(e.Value.(*entry).seen) >= l.cfg.IdleTTL; e = l.lru.Back() {
		l.removeOldest()
	}
}

// Allow consumes one token or returns the time until one becomes available.
// Rejected requests do not reserve future tokens. RetryAfter saturates at the
// maximum duration for rates too small to represent the wait.
func (l *Limiter) Allow(key string) (allowed bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.cfg.Clock.Now()
	l.prune(now)
	e := l.items[key]
	if e == nil {
		if len(l.items) == l.cfg.MaxKeys {
			l.removeOldest()
		}
		e = l.lru.PushFront(&entry{key: key, bucket: rate.NewLimiter(l.cfg.Rate, l.cfg.Burst)})
		l.items[key] = e
	}
	item := e.Value.(*entry)
	item.seen = now
	l.lru.MoveToFront(e)
	if item.bucket.AllowN(now, 1) {
		return true, 0
	}
	nanos := (1 - item.bucket.TokensAt(now)) / float64(l.cfg.Rate) * float64(time.Second)
	if nanos >= float64(math.MaxInt64) {
		return false, time.Duration(math.MaxInt64)
	}
	return false, time.Duration(math.Ceil(nanos))
}

// Len prunes expired keys and returns the current cardinality.
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(l.cfg.Clock.Now())
	return len(l.items)
}
