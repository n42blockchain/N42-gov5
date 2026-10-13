package frame_test

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/frametest"
)

func TestAuditFrameGasEstimateCannotWrap(t *testing.T) {
	key := frametest.Keys(t, 1, 217)[0]
	addr := types.Address{19: 1}
	for _, codec := range []uint8{frame.TxTypeEthereum, frame.TxTypeNative} {
		var raws [][]byte
		for _, gas := range []uint64{1, ^uint64(0), 21000} {
			tx := transaction.NewTransaction(0, addr, &addr, uint256.NewInt(0), gas, uint256.NewInt(1), nil)
			var raw []byte
			var err error
			if codec == frame.TxTypeNative {
				raw, err = tx.Marshal()
			} else {
				raw, err = tx.EthEncoded()
			}
			if err != nil {
				t.Fatal(err)
			}
			raws = append(raws, raw)
		}
		raw, _, err := frame.Encode(key, 0, codec, raws)
		if err != nil {
			t.Fatal(err)
		}
		f, err := frame.Decode(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := frame.GasEstimate(f); got != ^uint64(0) {
			t.Fatalf("codec=%d wrapped gas to %d", codec, got)
		}
	}
}
