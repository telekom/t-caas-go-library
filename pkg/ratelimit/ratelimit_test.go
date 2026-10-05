// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package ratelimit

import (
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"golang.org/x/time/rate"
	clocktesting "k8s.io/utils/clock/testing"
)

func TestBuckets(t *testing.T) {
	c := clocktesting.NewFakeClock(time.Now())
	l, err := New(Config{Rate: 2, Burst: 1, MaxKeys: 2, IdleTTL: time.Minute, Clock: c})
	if err != nil {
		t.Fatal(err)
	}
	if ok, d := l.Allow("a"); !ok || d != 0 {
		t.Fatal(ok, d)
	}
	for range 3 {
		if ok, d := l.Allow("a"); ok || d != 500*time.Millisecond {
			t.Fatal(ok, d)
		}
	}
	c.Step(250 * time.Millisecond)
	if ok, d := l.Allow("a"); ok || d != 250*time.Millisecond {
		t.Fatal(ok, d)
	}
	c.Step(250 * time.Millisecond)
	if ok, _ := l.Allow("a"); !ok {
		t.Fatal("did not refill")
	}
	l.Allow("b")
	l.Allow("a") // make a most-recently-used
	l.Allow("c") // evicts b
	if l.Len() != 2 {
		t.Fatal(l.Len())
	}
	if ok, _ := l.Allow("a"); ok {
		t.Fatal("wrong key evicted")
	}
	if ok, _ := l.Allow("b"); !ok {
		t.Fatal("evicted key should get fresh bucket")
	}
	c.Step(time.Minute)
	if l.Len() != 0 {
		t.Fatal("idle TTL")
	}
}

func TestConfig(t *testing.T) {
	for _, cfg := range []Config{{}, {Rate: -1, Burst: 1, MaxKeys: 1, IdleTTL: 1},
		{Rate: rate.Limit(math.NaN()), Burst: 1, MaxKeys: 1, IdleTTL: 1},
		{Rate: rate.Inf, Burst: 1, MaxKeys: 1, IdleTTL: 1},
		{Rate: 2e9, Burst: 1, MaxKeys: 1, IdleTTL: 1},
		{Rate: rate.Limit(math.Nextafter(1e9, math.Inf(1))), Burst: 1, MaxKeys: 1, IdleTTL: 1},
		{Rate: rate.Limit(math.Inf(1)), Burst: 1, MaxKeys: 1, IdleTTL: 1}} {
		if _, err := New(cfg); err == nil {
			t.Fatal("accepted invalid config")
		}
	}
	l, err := New(Config{Rate: rate.Limit(math.SmallestNonzeroFloat64), Burst: 1, MaxKeys: 1, IdleTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	l.Allow("a")
	if ok, d := l.Allow("a"); ok || d != time.Duration(math.MaxInt64) {
		t.Fatal(ok, d)
	}
}

func TestNanosecondRate(t *testing.T) {
	c := clocktesting.NewFakeClock(time.Now())
	l, err := New(Config{Rate: 1e9, Burst: 1, MaxKeys: 1, IdleTTL: time.Minute, Clock: c})
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := l.Allow("a"); !ok {
		t.Fatal("initial token denied")
	}
	if ok, delay := l.Allow("a"); ok || delay != time.Nanosecond {
		t.Fatal(ok, delay)
	}
	c.Step(time.Nanosecond)
	if ok, _ := l.Allow("a"); !ok {
		t.Fatal("did not refill")
	}
}

func TestConcurrent(t *testing.T) {
	l, _ := New(Config{Rate: 1, Burst: 1, MaxKeys: 4, IdleTTL: time.Minute})
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			for range 100 {
				l.Allow(fmt.Sprint(i))
				if l.Len() > 4 {
					t.Error("cardinality exceeded")
				}
			}
		})
	}
	wg.Wait()
}

func ExampleLimiter_Allow() {
	l, _ := New(Config{Rate: 1, Burst: 1, MaxKeys: 100, IdleTTL: time.Hour})
	ok, _ := l.Allow("user")
	fmt.Println(ok)
	// Output: true
}
