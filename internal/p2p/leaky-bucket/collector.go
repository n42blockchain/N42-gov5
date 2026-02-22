package leakybucket

import (
	"container/heap"
	"sync"
	"time"
)

// Collector keeps track of multiple LeakyBucket instances, addressed by string
// keys (e.g. IP address, hostname, hash). All methods are goroutine safe.
type Collector struct {
	buckets  map[string]*LeakyBucket
	heap     priorityQueue
	rate     float64
	capacity int64
	period   time.Duration
	mu       sync.RWMutex
	quit     chan struct{}
}

// NewCollector creates a new Collector. New buckets created within the
// Collector inherit its capacity and rate.
//
// If deleteEmptyBuckets is true, a background goroutine periodically removes
// drained buckets to free memory.
func NewCollector(rate float64, capacity int64, period time.Duration, deleteEmptyBuckets bool) *Collector {
	c := &Collector{
		buckets:  make(map[string]*LeakyBucket),
		heap:     make(priorityQueue, 0, 4096),
		rate:     rate,
		capacity: capacity,
		period:   period,
		quit:     make(chan struct{}),
	}

	if deleteEmptyBuckets {
		c.startPeriodicPrune()
	}

	return c
}

// Free releases the collector's resources and stops the background pruning
// goroutine if one was started.
func (c *Collector) Free() {
	c.Reset()
	close(c.quit)
}

// Reset removes all internal buckets, restoring the collector to its initial state.
func (c *Collector) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.buckets = make(map[string]*LeakyBucket)
	c.heap = make(priorityQueue, 0, 4096)
}

// Capacity returns the collector's bucket capacity.
func (c *Collector) Capacity() int64 {
	return c.capacity
}

// Rate returns the collector's leak rate.
func (c *Collector) Rate() float64 {
	return c.rate
}

// Remaining returns the remaining capacity of the bucket associated with key.
// Unknown keys are treated as empty buckets.
func (c *Collector) Remaining(key string) int64 {
	return c.capacity - c.Count(key)
}

// Count returns the current count of the bucket associated with key.
// Unknown keys are treated as empty buckets.
func (c *Collector) Count(key string) int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()

	b := c.buckets[key]
	if b == nil {
		return 0
	}
	return b.Count()
}

// TillEmpty returns the duration until the bucket associated with key is empty.
// Unknown keys are treated as empty buckets.
func (c *Collector) TillEmpty(key string) time.Duration {
	c.mu.RLock()
	defer c.mu.RUnlock()

	b := c.buckets[key]
	if b == nil {
		return 0
	}
	return b.TillEmpty()
}

// Remove deletes the bucket associated with key. No-op for unknown keys.
func (c *Collector) Remove(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	b := c.buckets[key]
	if b == nil {
		return
	}

	delete(c.buckets, key)
	heap.Remove(&c.heap, b.index)
}

// Add adds amount to the bucket associated with key, up to its capacity.
// Returns the amount actually added. If the return is less than amount, the
// bucket's capacity was reached.
//
// If key has no associated bucket, a new one is created first.
func (c *Collector) Add(key string, amount int64) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	b := c.buckets[key]
	if b == nil {
		b = &LeakyBucket{
			key:      key,
			capacity: c.capacity,
			rate:     c.rate,
			period:   c.period,
			p:        now(),
		}
		c.heap.Push(b)
		c.buckets[key] = b
	}

	n := b.Add(amount)
	if n > 0 {
		heap.Fix(&c.heap, b.index)
	}
	return n
}

// Prune removes all fully drained buckets from the collector.
func (c *Collector) Prune() {
	c.mu.Lock()
	defer c.mu.Unlock()

	t := now()
	for {
		b := c.heap.Peek()
		if b == nil || t.Before(b.p) {
			break
		}
		delete(c.buckets, b.key)
		heap.Remove(&c.heap, b.index)
	}
}

// startPeriodicPrune launches a background goroutine that calls Prune at the
// collector's period interval until Free is called.
func (c *Collector) startPeriodicPrune() {
	go func() {
		ticker := time.NewTicker(c.period)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				c.Prune()
			case <-c.quit:
				return
			}
		}
	}()
}
