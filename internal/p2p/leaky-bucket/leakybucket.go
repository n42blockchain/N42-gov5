/*
Package leakybucket implements a scalable leaky bucket algorithm.

There are at least two different definitions of the leaky bucket algorithm.
This package implements the leaky bucket as a meter. For more details see:

https://en.wikipedia.org/wiki/Leaky_bucket#As_a_meter

This means it is the exact mirror of a token bucket.

	// New LeakyBucket that leaks at the rate of 0.5/sec and a total capacity of 10.
	b := NewLeakyBucket(0.5, 10, time.Second)

	b.Add(5)
	b.Add(5)
	// Bucket is now full!

	n := b.Add(1)
	// n == 0

A Collector is a convenient way to keep track of multiple LeakyBucket's.
Buckets are associated with string keys for fast lookup. It can dynamically
add new buckets and automatically remove them as they become empty, freeing
up resources.

	// New Collector that leaks at 1 MiB/sec, a total capacity of 10 MiB and
	// automatic removal of buckets when they become empty.
	const megabyte = 1 << 20
	c := NewCollector(megabyte, megabyte*10, time.Second, true)

	// Attempt to add 100 MiB to a bucket associated with an IP.
	n := c.Add("192.168.0.42", megabyte*100)

	// 100 MiB is over the capacity, so only 10 MiB is actually added.
	// n equals 10 MiB.
*/
package leakybucket

import (
	"math"
	"time"
)

// now is a package-level variable to facilitate testing with deterministic time.
var now = time.Now

// LeakyBucket represents a bucket that leaks at a constant rate.
type LeakyBucket struct {
	key      string        // identifying key for map lookups
	capacity int64         // maximum bucket size
	rate     float64       // leak amount per period
	period   time.Duration // time window for the leak rate

	// p is the exact time the bucket will be fully drained. Used as the
	// priority in a min-heap so that soon-to-drain buckets can be pruned
	// efficiently. Adjusted whenever items are added.
	p time.Time

	// index is maintained by heap.Interface methods.
	index int
}

// NewLeakyBucket creates a new LeakyBucket with the given rate and capacity.
func NewLeakyBucket(rate float64, capacity int64, period time.Duration) *LeakyBucket {
	return &LeakyBucket{
		rate:     rate,
		capacity: capacity,
		period:   period,
		p:        now(),
	}
}

// nsPerDrip returns the nanosecond cost of a single drip. Returns 0 if rate is zero.
func (b *LeakyBucket) nsPerDrip() float64 {
	if b.rate == 0 {
		return 0
	}
	return float64(b.period) / b.rate
}

// Count returns the bucket's current count.
func (b *LeakyBucket) Count() int64 {
	t := now()
	if !t.Before(b.p) {
		return 0
	}

	drip := b.nsPerDrip()
	if drip == 0 {
		return 0
	}

	nsRemaining := float64(b.p.Sub(t))
	return int64(math.Ceil(nsRemaining / drip))
}

// Rate returns the bucket's leak rate per period.
func (b *LeakyBucket) Rate() float64 {
	return b.rate
}

// Capacity returns the bucket's capacity.
func (b *LeakyBucket) Capacity() int64 {
	return b.capacity
}

// Remaining returns the bucket's remaining capacity.
func (b *LeakyBucket) Remaining() int64 {
	return b.capacity - b.Count()
}

// ChangeCapacity changes the bucket's capacity. If the current count exceeds
// the new capacity, the count is reduced to match.
func (b *LeakyBucket) ChangeCapacity(capacity int64) {
	if capacity < b.capacity && b.Count() > capacity {
		drip := b.nsPerDrip()
		if drip == 0 {
			b.p = now()
		} else {
			b.p = now().Add(time.Duration(drip * float64(capacity)))
		}
	}
	b.capacity = capacity
}

// TillEmpty returns how much time must pass until the bucket is empty.
func (b *LeakyBucket) TillEmpty() time.Duration {
	return b.p.Sub(now())
}

// Add adds amount to the bucket's count, capped at capacity. Returns the
// amount actually added. A return value less than amount means the bucket
// reached capacity.
func (b *LeakyBucket) Add(amount int64) int64 {
	if b.rate == 0 {
		return 0
	}

	count := b.Count()
	if count >= b.capacity {
		return 0
	}

	t := now()
	if !t.Before(b.p) {
		b.p = t
	}

	remaining := b.capacity - count
	if amount > remaining {
		amount = remaining
	}
	b.p = b.p.Add(time.Duration(float64(b.period) * (float64(amount) / b.rate)))
	return amount
}
