package parallel

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

// A recipient the block only credits is recorded as a DELTA write, whose
// Value is nil. The sequential path (small blocks, and the wave-limit
// fallback) used to replay every write through MVS.Write, and a nil value
// there means "deleted": applyMVSToIBS then selfdestructed the recipient.
// The fleet hit this for real -- the leader's build of block 13659302 fell
// back, 17,036 credited accounts came out empty, and its state root diverged
// from all six followers. Both sequential entries must keep the delta.
func TestSequentialPathKeepsDeltaWrites(t *testing.T) {
	var recipient types.Address
	recipient[0] = 0xbe

	key := LocationKey{Field: FieldBalance, Address: recipient}

	// Two transactions, each crediting the same recipient: numTxs <= 2 takes
	// the sequential path inside Run.
	exec := func(txIndex int, rw *ReadWriteSet) error {
		rw.RecordDeltaWrite(key, uint256.NewInt(uint64(txIndex)+1))
		return nil
	}
	e := NewExecutor(2, 1, exec)
	e.Run()
	assertCredited(t, e.MVS(), 2, key, 3) // 1 + 2

	// And again through the explicit fallback entry point, with enough
	// transactions that Run would have gone parallel.
	e2 := NewExecutor(8, 4, exec)
	e2.runSequential()
	assertCredited(t, e2.MVS(), 8, key, 36) // 1+2+...+8
}

func assertCredited(t *testing.T, m *MVS, numTxs int, key LocationKey, want uint64) {
	t.Helper()
	var seen int
	err := m.ApplyAll(numTxs, func(k LocationKey, value []byte, delta *uint256.Int) error {
		if k != key {
			return nil
		}
		seen++
		if delta == nil {
			t.Fatalf("recipient folded as value=%v delta=nil: applyMVSToIBS would selfdestruct it", value)
		}
		if delta.Uint64() != want {
			t.Fatalf("delta = %d, want %d", delta.Uint64(), want)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ApplyAll: %v", err)
	}
	if seen != 1 {
		t.Fatalf("recipient entries = %d, want 1", seen)
	}
}
