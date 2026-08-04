package transaction

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

// BenchmarkUnmarshalCompactStorage measures the decode that dominated a loaded
// node's live heap: every block read back from storage runs it once per
// transaction.
func BenchmarkUnmarshalCompactStorage(b *testing.B) {
	to := types.Address{0x11, 0x22}
	tx := NewTx(&DynamicFeeTx{
		ChainID:   uint256.NewInt(94),
		Nonce:     7,
		GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(1e10),
		Gas:       21000,
		To:        &to,
		Value:     uint256.NewInt(12345),
		V:         uint256.NewInt(1),
		R:         uint256.NewInt(0x1234567890abcdef),
		S:         uint256.NewInt(0x0fedcba098765432),
	})
	enc := tx.MarshalCompactStorage()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var got Transaction
		if err := got.unmarshalCompactStorage(enc); err != nil {
			b.Fatal(err)
		}
	}
}
