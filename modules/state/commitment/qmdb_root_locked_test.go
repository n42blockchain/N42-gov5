package commitment

import (
	"testing"
	"time"
)

// TestRootLockedInsideReadersSpan: a caller inside LockReaders must be able to
// read the root. Root itself takes the readers lock for writing, so calling it
// there deadlocks the goroutine and everything behind it — which is what the
// deferred includability check did in round 35zzq: one block in eighteen
// minutes, every import stuck behind the held read lock.
func TestRootLockedInsideReadersSpan(t *testing.T) {
	rc := NewQMDBRootComputer()

	done := make(chan struct{})
	go func() {
		defer close(done)
		unlock := rc.LockReaders()
		defer unlock()
		_ = rc.RootLocked()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("reading the root inside a LockReaders span did not return: the readers lock is held twice")
	}

	// Outside the span the two accessors agree.
	locked := func() [32]byte {
		unlock := rc.LockReaders()
		defer unlock()
		return rc.RootLocked()
	}()
	if plain := rc.Root(); plain != locked {
		t.Fatalf("RootLocked %x, Root %x", locked, plain)
	}
}
