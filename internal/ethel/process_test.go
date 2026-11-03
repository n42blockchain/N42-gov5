package ethel

import (
	"context"
	"errors"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

func TestProcessBlockEmpty(t *testing.T) {
	// Process a block with no transactions (like early Ethereum blocks).
	chainCfg := params.EthereumMainnetChainConfig
	engine := NewEthReplayEngine(chainCfg)

	header := &block.Header{
		Number:     uint256.NewInt(1),
		GasLimit:   5000,
		Difficulty: uint256.NewInt(17179869184),
		Time:       1438269988,
		Coinbase:   types.HexToAddress("0x05a56E2D52c817161883f50c441c3228CFe54d9f"),
	}

	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)
	tx, _ := db.BeginRo(context.Background())
	defer tx.Rollback()
	ibs := state.New(state.NewPlainStateReader(tx))
	blockHashFunc := func(n uint64) types.Hash { return types.Hash{} }

	result, err := ProcessBlock(chainCfg, engine, header,
		[]*transaction.Transaction{}, nil, nil, ibs, blockHashFunc, nil)
	if err != nil {
		t.Fatalf("ProcessBlock empty: %v", err)
	}
	if result.GasUsed != 0 {
		t.Errorf("gasUsed: got %d, want 0", result.GasUsed)
	}
	if len(result.Receipts) != 0 {
		t.Errorf("receipts: got %d, want 0", len(result.Receipts))
	}
	if len(result.Senders) != 0 {
		t.Errorf("senders: got %d, want 0", len(result.Senders))
	}
}

// EIP-7934: from Osaka a block whose RLP encoding exceeds 8 MiB is invalid.
// Batch replay goes through ProcessBlock, so the cap is enforced there as
// geth's ValidateBody enforces it; before Osaka the same block is accepted.
func TestProcessBlockEnforcesRLPBlockSizeFromOsaka(t *testing.T) {
	chainCfg := params.EthereumMainnetChainConfig
	engine := NewEthReplayEngine(chainCfg)
	osaka := chainCfg.OsakaTime.Uint64()

	newHeader := func(time uint64, extra int) *block.Header {
		h := types.Hash{1}
		blobGas := uint64(0)
		return &block.Header{
			Number:           uint256.NewInt(23_935_694),
			GasLimit:         60_000_000,
			Difficulty:       uint256.NewInt(0),
			Time:             time,
			BaseFee:          uint256.NewInt(7),
			Extra:            make([]byte, extra),
			WithdrawalsHash:  &h,
			BlobGasUsed:      &blobGas,
			ExcessBlobGas:    &blobGas,
			ParentBeaconRoot: &h,
			RequestsHash:     &h,
		}
	}
	// Size the extra data so the block encodes to exactly the target size.
	sizedHeader := func(time, target uint64) *block.Header {
		extra := int(target) - 1024
		for i := 0; i < 8; i++ {
			size, err := BlockRLPSize(newHeader(time, extra), nil, nil, nil)
			if err != nil {
				t.Fatalf("blockRLPSize: %v", err)
			}
			if size == target {
				return newHeader(time, extra)
			}
			extra += int(target) - int(size)
		}
		t.Fatalf("could not size a block to %d bytes", target)
		return nil
	}

	for _, tc := range []struct {
		name    string
		header  *block.Header
		wantErr bool
	}{
		{"osaka at limit", sizedHeader(osaka, vm2.MaxRLPBlockSizeFusaka), false},
		{"osaka above limit", sizedHeader(osaka, vm2.MaxRLPBlockSizeFusaka+1), true},
		{"prague above limit", sizedHeader(osaka-1, vm2.MaxRLPBlockSizeFusaka+1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := memdb.New(t.TempDir())
			t.Cleanup(db.Close)
			tx, _ := db.BeginRo(context.Background())
			defer tx.Rollback()
			ibs := state.New(state.NewPlainStateReader(tx))
			_, err := ProcessBlock(chainCfg, engine, tc.header, nil, nil, nil, ibs,
				func(uint64) types.Hash { return types.Hash{} }, nil)
			if got := errors.Is(err, vm2.ErrRLPBlockSizeTooLarge); got != tc.wantErr {
				t.Fatalf("err = %v, want size error %v", err, tc.wantErr)
			}
		})
	}
}
