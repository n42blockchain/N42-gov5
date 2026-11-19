// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package commitment

import (
	"time"

	"github.com/n42blockchain/N42/lib/qmdb"
)

// FlushCapture is a FlushTo running on its own goroutine (StartFlushTo).
type FlushCapture struct {
	done chan struct{}
	n    int
	err  error
	took time.Duration
	st   FlushCaptureStats
}

// FlushCaptureStats is the split of one capture (stamps): the eviction of
// the previous window (whole, and its twig walk), the history settle, and
// the flush's own split.
type FlushCaptureStats struct {
	Evict     time.Duration // evictLaggedLocked as a whole (entries + twigs)
	EvictTwig qmdb.EvictStats
	Hist      time.Duration // FlushHistory
	Flush     qmdb.FlushStats
	Evicted   bool // the capture evicted first
}

// Stats returns the capture's split; valid after Wait.
func (c *FlushCapture) Stats() FlushCaptureStats {
	<-c.done
	return c.st
}

// Wait returns the flush's result once it finished.
func (c *FlushCapture) Wait() (int, error) {
	<-c.done
	return c.n, c.err
}

// Done is closed once the flush finished.
func (c *FlushCapture) Done() <-chan struct{} { return c.done }

// Took is how long the flush (and the eviction before it) ran; valid
// after Wait.
func (c *FlushCapture) Took() time.Duration {
	<-c.done
	return c.took
}

// StartFlushTo is FlushTo -- optionally preceded by EvictFlushed -- run on
// its own goroutine, ordered exactly where the call is made: the readers
// lock is taken here, on the caller's goroutine, and handed to the
// goroutine, which releases it when the flush is done (a locked
// sync.RWMutex belongs to no goroutine). Every later call on r -- a
// mutation, a SetCold, a Lookup -- therefore waits for the flush and sees
// the tree it left, as after a synchronous EvictFlushed + FlushTo here;
// the caller only stops waiting for it.
//
// On a flush error the staged flush is aborted (AbortFlushed) before the
// lock is released.
func (r *QMDBRootComputer) StartFlushTo(p qmdb.Putter, evictFirst bool) *FlushCapture {
	c := &FlushCapture{done: make(chan struct{})}
	unlock := r.lockWriter(holderFlushAsync)
	go func() {
		t0 := time.Now()
		if evictFirst {
			r.evictLaggedLocked(r.flushedThrough)
			c.st.Evicted = true
			c.st.EvictTwig = r.t.LastEvictStats()
		}
		c.st.Evict = time.Since(t0)
		c.n, c.err = r.flushToLocked(p)
		c.st.Flush = r.t.LastFlushStats()
		c.st.Hist = r.lastFlushHist
		if c.err != nil {
			r.abortFlushedLocked()
		}
		c.took = time.Since(t0)
		unlock()
		close(c.done)
	}()
	return c
}
