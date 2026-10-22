package rawdb

import (
	"fmt"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// ReadReceiptsWithError derives receipt metadata from the caller's actual stored
// block without reading its body again. Unlike the legacy reader, it propagates
// storage, archive, decoding and metadata errors to authorization callers.
func ReadReceiptsWithError(tx kv.Tx, b *block.Block, senders []types.Address) (block.Receipts, error) {
	return readReceiptsWithError(tx, b, senders, true)
}

// ReadReceiptsForExecutionWithError reads and validates the stored receipts needed
// to reconstruct an execution result. It preserves the actual transaction types
// but does not derive transaction hashes or RPC location fields. The returned
// metadata is unsuitable for RPC consumers and may contain legacy stored values.
func ReadReceiptsForExecutionWithError(tx kv.Tx, b *block.Block, senders []types.Address) (block.Receipts, error) {
	return readReceiptsWithError(tx, b, senders, false)
}

func readReceiptsWithError(tx kv.Tx, b *block.Block, senders []types.Address, deriveMetadata bool) (block.Receipts, error) {
	if b == nil {
		return nil, fmt.Errorf("receipt metadata requires a stored block")
	}
	number, err := requireBlockNumber(b, "checked receipts")
	if err != nil {
		return nil, err
	}
	raw, err := tx.GetOne(modules.Receipts, modules.EncodeBlockNumber(number.Uint64()))
	if err != nil {
		return nil, fmt.Errorf("read receipts: %w", err)
	}
	if len(raw) == 0 {
		if archive := ancientSealed(number.Uint64()); archive != nil {
			record, readErr := archive.Exec(number.Uint64())
			if readErr != nil {
				return nil, fmt.Errorf("read archived receipts: %w", readErr)
			}
			raw = record.ReceiptsRaw
		}
	}
	var receipts block.Receipts
	if len(raw) > 0 {
		if err := receipts.Unmarshal(raw); err != nil {
			return nil, fmt.Errorf("decode receipts: %w", err)
		}
	}
	// Empty receipt lists may encode to zero bytes. They are valid only for an
	// empty body; absent or truncated receipts for a nonempty body must fail.
	if len(receipts) != len(b.Transactions()) {
		return nil, fmt.Errorf("receipt count differs from stored body")
	}
	if len(senders) > 0 {
		b.SendersToTxs(senders)
	}
	if deriveMetadata {
		if !deriveReceiptMetadata(receipts, b, number.Uint64()) {
			return nil, fmt.Errorf("invalid stored receipt metadata")
		}
	} else if !deriveReceiptExecutionFields(receipts, b) {
		return nil, fmt.Errorf("invalid stored receipt metadata")
	}
	return receipts, nil
}

// deriveReceiptExecutionFields retains the validation predicates of metadata
// derivation while filling only the actual type used by Ethereum receipt roots.
func deriveReceiptExecutionFields(receipts block.Receipts, b *block.Block) bool {
	txs := b.Transactions()
	if len(receipts) != len(txs) {
		return false
	}
	var previousCumulativeGas uint64
	for i, receipt := range receipts {
		if receipt == nil || txs[i] == nil || receipt.CumulativeGasUsed < previousCumulativeGas {
			return false
		}
		receipt.Type = txs[i].Type()
		previousCumulativeGas = receipt.CumulativeGasUsed
		for _, lg := range receipt.Logs {
			if lg == nil {
				return false
			}
		}
	}
	return true
}
