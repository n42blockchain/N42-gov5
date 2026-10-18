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
	if !deriveReceiptMetadata(receipts, b, number.Uint64()) {
		return nil, fmt.Errorf("invalid stored receipt metadata")
	}
	return receipts, nil
}
