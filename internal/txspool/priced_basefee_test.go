package txspool

import (
	"sync/atomic"
	"testing"

	"github.com/holiman/uint256"
)

// An unchanged base fee must not reheap the priced lists: the reorg pays
// that walk over every remote transaction under the pool lock, on every
// block, and the fleet's fee sits flat on the target block after block.
func TestPricedListSetBaseFeeSkipsReheapWhenUnchanged(t *testing.T) {
	l := newTxPricedList(newTxLookup())
	l.SetBaseFee(uint256.NewInt(7))
	first := atomic.LoadInt64(&l.reheaps)
	if first != 1 {
		t.Fatalf("first SetBaseFee ran %d reheaps, want 1", first)
	}
	l.SetBaseFee(uint256.NewInt(7))
	if n := atomic.LoadInt64(&l.reheaps); n != first {
		t.Fatalf("SetBaseFee with the same fee reheaped (%d -> %d)", first, n)
	}
	l.SetBaseFee(uint256.NewInt(8))
	if n := atomic.LoadInt64(&l.reheaps); n != first+1 {
		t.Fatalf("SetBaseFee with a new fee did not reheap (%d -> %d)", first, n)
	}
}
