package internal

import (
	"errors"
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state"
)

type executionStorageFailureReader struct {
	state.StateReader
	fault error
}

func (reader executionStorageFailureReader) ReadAccountStorage(types.Address, *types.Hash) ([]byte, error) {
	return nil, reader.fault
}

func TestApplyTransactionRejectsStateReadError(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "missing-slot"
		if fail {
			name = "disk-error"
		}
		t.Run(name, func(t *testing.T) {
			_, dbtx := memdb.NewTestTx(t)
			key, err := crypto.GenerateKey()
			require.NoError(t, err)
			sender := crypto.PubkeyToAddress(key.PublicKey)
			contract, beneficiary := types.Address{18: 0xc0, 19: 3}, types.Address{18: 0xc0, 19: 1}
			funded := account.NewAccount()
			funded.Balance.SetUint64(1_000_000_000_000_000_000)
			require.NoError(t, dbtx.Put(modules.Account, sender[:], funded.MarshalV2()))
			// The account must be persisted, not newly created in this execution:
			// SLOAD must consult the reader rather than take the new-account zero path.
			code := []byte{0x60, 0x00, 0x54, 0x50, 0x00} // PUSH1 0; SLOAD; POP; STOP
			deployed := account.NewAccount()
			deployed.Nonce = 1
			deployed.CodeHash = crypto.Keccak256Hash(code)
			require.NoError(t, dbtx.Put(modules.Account, contract[:], deployed.MarshalV2()))
			require.NoError(t, dbtx.Put(modules.Code, deployed.CodeHash[:], code))
			var reader state.StateReader = state.NewPlainStateReader(dbtx)
			fault := errors.New("SLOAD database I/O failure")
			if fail {
				reader = executionStorageFailureReader{StateReader: reader, fault: fault}
			}
			ibs := state.New(reader)
			cfg := testStateTransitionChainConfig()
			header := &block.Header{Number: uint256.NewInt(1), Time: 1, GasLimit: 30_000_000,
				BaseFee: uint256.NewInt(7), Difficulty: uint256.NewInt(0)}
			tx, err := transaction.SignTx(transaction.NewTransaction(0, sender, &contract,
				uint256.NewInt(0), 100_000, uint256.NewInt(9), nil), transaction.NewLondonSigner(big.NewInt(1)), key)
			require.NoError(t, err)
			var used uint64
			receipt, _, err := ApplyTransaction(cfg, func(uint64) types.Hash { return types.Hash{} }, nil,
				&beneficiary, new(common.GasPool).AddGas(header.GasLimit), ibs,
				state.NewPlainStateWriter(dbtx, dbtx, 1), header, tx, &used, vm2.Config{})
			if !fail {
				require.NoError(t, err)
				require.NotNil(t, receipt)
				require.Equal(t, uint64(block.ReceiptStatusSuccessful), receipt.Status)
				require.Greater(t, used, uint64(21_000))
				return
			}
			require.ErrorIs(t, err, fault, "read failure must not become an EVM success or revert receipt")
			require.ErrorIs(t, ibs.Error(), fault)
			require.Nil(t, receipt)
			require.Zero(t, used, "failed execution published cumulative receipt gas")
			persisted, readErr := dbtx.GetOne(modules.Account, sender[:])
			require.NoError(t, readErr)
			require.Equal(t, funded.MarshalV2(), persisted, "failed execution wrote sender state")
		})
	}
}
