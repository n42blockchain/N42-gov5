package state

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// First writes on a newly created contract bypass committed-storage caching.
// Exercise that path as well as reads, reverts, and the next transaction's
// committed view, with fresh objects guaranteed by disabling pooling.
func TestLazyStorageAcrossTransactions(t *testing.T) {
	withPooling(t, false, func() {
		for _, created := range []bool{false, true} {
			addr := types.HexToAddress("0x1234")
			slot := types.HexToHash("0x42")
			s := New(slotReader{slots: map[types.Hash][]byte{slot: {7}}})
			if created {
				s.CreateAccount(addr, true)
			}
			s.SetNonce(addr, 1) // Keep the account live at finalization.
			var got uint256.Int
			wantOriginal := uint64(7)
			if created {
				wantOriginal = 0
			}
			rev := s.Snapshot()
			s.SetState(addr, &slot, *uint256.NewInt(42))
			s.GetState(addr, &slot, &got)
			if got.Uint64() != 42 {
				t.Fatalf("created=%v first write: %d", created, got.Uint64())
			}
			s.GetCommittedState(addr, &slot, &got)
			if got.Uint64() != wantOriginal {
				t.Fatalf("created=%v original: %d", created, got.Uint64())
			}
			s.RevertToSnapshot(rev)
			s.GetState(addr, &slot, &got)
			if got.Uint64() != wantOriginal {
				t.Fatalf("created=%v revert: %d", created, got.Uint64())
			}
			s.SetState(addr, &slot, *uint256.NewInt(21))
			if err := s.FinalizeTx(&params.Rules{}, NewNoopWriter()); err != nil {
				t.Fatal(err)
			}
			s.GetCommittedState(addr, &slot, &got)
			if got.Uint64() != 21 {
				t.Fatalf("created=%v next transaction: %d", created, got.Uint64())
			}
		}
	})
}
