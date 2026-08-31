package transaction

import (
	"testing"

	"github.com/holiman/uint256"
)

// TestEffectiveGasTipCmpMatchesValue pins the allocation-free comparators to the
// value-returning EffectiveGasTip they replaced, including the branch where
// gasFeeCap < baseFee and the subtraction underflows (the min then selects
// gasTipCap, which is what ErrGasFeeCapTooLow reports on).
func TestEffectiveGasTipCmpMatchesValue(t *testing.T) {
	u := func(v uint64) *uint256.Int { return uint256.NewInt(v) }
	caps := []struct{ feeCap, tipCap uint64 }{
		{0, 0}, {1, 1}, {100, 1}, {100, 50}, {100, 100}, {100, 200},
		{50, 10}, {50, 60}, {1 << 40, 3}, {7, 7},
	}
	baseFees := []*uint256.Int{nil, u(0), u(1), u(10), u(50), u(100), u(1 << 40)}

	mk := func(feeCap, tipCap uint64) *Transaction {
		return NewTx(&DynamicFeeTx{
			ChainID:   uint256.NewInt(1),
			Nonce:     1,
			GasTipCap: u(tipCap),
			GasFeeCap: u(feeCap),
			Gas:       21000,
		})
	}

	for _, bf := range baseFees {
		for _, x := range caps {
			for _, y := range caps {
				a, b := mk(x.feeCap, x.tipCap), mk(y.feeCap, y.tipCap)

				var want int
				if bf == nil {
					want = a.GasTipCapCmp(b)
				} else {
					want = a.EffectiveGasTipValue(bf).Cmp(b.EffectiveGasTipValue(bf))
				}
				if got := a.EffectiveGasTipCmp(b, bf); got != want {
					t.Fatalf("EffectiveGasTipCmp(baseFee=%v, %v/%v vs %v/%v) = %d, want %d",
						bf, x.feeCap, x.tipCap, y.feeCap, y.tipCap, got, want)
				}

				other := u(y.tipCap)
				if bf == nil {
					want = a.GasTipCapIntCmp(other)
				} else {
					want = a.EffectiveGasTipValue(bf).Cmp(other)
				}
				if got := a.EffectiveGasTipIntCmp(other, bf); got != want {
					t.Fatalf("EffectiveGasTipIntCmp(baseFee=%v, %v/%v vs %v) = %d, want %d",
						bf, x.feeCap, x.tipCap, y.tipCap, got, want)
				}
			}
		}
	}
}

// TestEffectiveGasTipCmpDoesNotAllocate is the point of the change.
func TestEffectiveGasTipCmpDoesNotAllocate(t *testing.T) {
	a := NewTx(&DynamicFeeTx{ChainID: uint256.NewInt(1), GasTipCap: uint256.NewInt(3),
		GasFeeCap: uint256.NewInt(100), Gas: 21000})
	b := NewTx(&DynamicFeeTx{ChainID: uint256.NewInt(1), GasTipCap: uint256.NewInt(7),
		GasFeeCap: uint256.NewInt(90), Gas: 21000})
	baseFee := uint256.NewInt(10)
	if n := testing.AllocsPerRun(200, func() { _ = a.EffectiveGasTipCmp(b, baseFee) }); n != 0 {
		t.Fatalf("EffectiveGasTipCmp allocated %v times per run, want 0", n)
	}
	if n := testing.AllocsPerRun(200, func() { _ = a.EffectiveGasTipIntCmp(baseFee, baseFee) }); n != 0 {
		t.Fatalf("EffectiveGasTipIntCmp allocated %v times per run, want 0", n)
	}
}
