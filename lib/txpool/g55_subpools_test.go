package txpool

import (
	"testing"

	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/types"
)

// TestPendingPool_PopWorstUpdatedDebugPrint exercises PendingPool's
// PopWorst, Updated, and DebugPrint, none of which the existing suite
// reaches directly.
func TestPendingPool_PopWorstUpdatedDebugPrint(t *testing.T) {
	logger := log.New()
	pool := NewPendingSubPool(PendingSubPool, 16)

	mt1 := g55NewMetaTxSimple(1)
	mt2 := g55NewMetaTxSimple(2)
	mt3 := g55NewMetaTxSimple(3)

	pool.Add(mt1, logger)
	pool.Add(mt2, logger)
	pool.Add(mt3, logger)

	if pool.Len() != 3 {
		t.Fatalf("Len() = %d, want 3", pool.Len())
	}

	// Mutate a transaction's ordering key then fix the worst-heap position.
	mt2.timestamp = 100
	pool.Updated(mt2)

	// DebugPrint must not panic over a non-empty pool.
	pool.DebugPrint("test")

	popped := pool.PopWorst()
	if popped == nil {
		t.Fatal("PopWorst returned nil")
	}
	if pool.Len() != 2 {
		t.Fatalf("Len() after PopWorst = %d, want 2", pool.Len())
	}
	// The popped element must also have been removed from the best slice.
	for _, mt := range pool.best.ms {
		if mt == popped {
			t.Fatal("PopWorst left the element in the best slice")
		}
	}

	// Drain the rest; must not panic and must reach Len()==0.
	pool.PopWorst()
	pool.PopWorst()
	if pool.Len() != 0 {
		t.Fatalf("Len() after draining = %d, want 0", pool.Len())
	}

	// DebugPrint on an empty pool must also be safe.
	pool.DebugPrint("empty")
}

func g55NewMetaTxSimple(nonce uint64) *metaTx {
	slot := &types.TxSlot{Nonce: nonce, Gas: 21000}
	return newMetaTx(slot, false, uint64(nonce))
}

// TestSubPool_PopWorstUpdatedDebugPrint exercises the heap-backed SubPool's
// PopWorst, Updated, and DebugPrint (used for the baseFee/queued subpools).
func TestSubPool_PopWorstUpdatedDebugPrint(t *testing.T) {
	logger := log.New()
	pool := NewSubPool(BaseFeeSubPool, 16)

	mt1 := g55NewMetaTxSimple(1)
	mt2 := g55NewMetaTxSimple(2)
	mt3 := g55NewMetaTxSimple(3)

	pool.Add(mt1, "test", logger)
	pool.Add(mt2, "test", logger)
	pool.Add(mt3, "test", logger)

	if pool.Len() != 3 {
		t.Fatalf("Len() = %d, want 3", pool.Len())
	}

	mt2.timestamp = 100
	pool.Updated(mt2)

	pool.DebugPrint("subpool-test")

	popped := pool.PopWorst()
	if popped == nil {
		t.Fatal("PopWorst returned nil")
	}
	if pool.Len() != 2 {
		t.Fatalf("Len() after PopWorst = %d, want 2", pool.Len())
	}

	pool.PopWorst()
	pool.PopWorst()
	if pool.Len() != 0 {
		t.Fatalf("Len() after draining = %d, want 0", pool.Len())
	}
	pool.DebugPrint("subpool-empty")
}

// TestSubPool_PopBestThenRemove exercises PopBest together with Remove to
// cover the worst-queue cross-removal bookkeeping.
func TestSubPool_PopBestThenRemove(t *testing.T) {
	logger := log.New()
	pool := NewSubPool(QueuedSubPool, 16)

	mt1 := g55NewMetaTxSimple(1)
	mt2 := g55NewMetaTxSimple(2)
	pool.Add(mt1, "test", logger)
	pool.Add(mt2, "test", logger)

	best := pool.PopBest()
	if best == nil {
		t.Fatal("PopBest returned nil")
	}
	if pool.Len() != 1 {
		t.Fatalf("Len() after PopBest = %d, want 1", pool.Len())
	}

	pool.Remove(mt2, "test", logger)
	if pool.Len() != 0 {
		t.Fatalf("Len() after Remove = %d, want 0", pool.Len())
	}
}
