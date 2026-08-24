// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package txspool

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/params"
)

// AddBatch must reject a non-batch tx up front (before touching the chain), so a
// minimal pool (no bc) does not panic. The happy-path expand+insert reuses the
// already-tested addTxsLocked path (full e2e harness is a productionize follow-up).
func TestAddBatchRejectsNonBatch(t *testing.T) {
	pool := &TxsPool{chainconfig: &params.ChainConfig{ChainID: big.NewInt(1)}}
	to := types.HexToAddress("0x00000000000000000000000000000000000000bb")
	normal := transaction.NewTx(&transaction.DynamicFeeTx{
		ChainID: uint256.NewInt(1), Nonce: 0, GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(1), Gas: 21000, To: &to, Value: uint256.NewInt(0),
	})
	if err := pool.AddBatch(normal); err != internal.ErrTxTypeNotSupported {
		t.Fatalf("AddBatch(non-batch) = %v, want ErrTxTypeNotSupported", err)
	}
}
