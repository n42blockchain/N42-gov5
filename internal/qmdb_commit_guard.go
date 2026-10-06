package internal

import (
	"context"
	"fmt"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// requireAppliedCommitLineage runs inside the canonicalization write transaction.
// A CommitQC proves consensus finality, not this node's execution of that branch.
// Neither a stored body, a canonical row, nor an applied height on a sibling is
// sufficient to publish a new canonical head against the current world state.
func requireAppliedCommitLineage(ctx context.Context, tx kv.Getter, hash types.Hash, number uint64) error {
	appliedNumber, appliedHash, ok, err := rawdb.ReadQMDBApplied(tx)
	if err != nil {
		return fmt.Errorf("commit-to-canonical: read applied head: %w", err)
	}
	if !ok {
		return fmt.Errorf("commit-to-canonical: QMDB applied head missing")
	}
	if number > appliedNumber {
		return fmt.Errorf("commit-to-canonical %d ahead of applied head %d (block stored but not executed)", number, appliedNumber)
	}
	currentNumber, currentHash := appliedNumber, types.Hash(appliedHash)
	for currentNumber > number {
		if (appliedNumber-currentNumber)%256 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		header := rawdb.ReadHeader(tx, currentHash, currentNumber)
		if header == nil || header.Number == nil || header.Number.BitLen() > 64 ||
			header.Number.Uint64() != currentNumber || header.Hash() != currentHash {
			return fmt.Errorf("commit-to-canonical: applied lineage header %d/%s unavailable or inconsistent", currentNumber, currentHash)
		}
		currentHash = header.ParentHash
		currentNumber--
	}
	if currentHash != hash {
		return fmt.Errorf("commit-to-canonical: block %d/%s is not on applied lineage %d/%x", number, hash, appliedNumber, appliedHash)
	}
	return nil
}
