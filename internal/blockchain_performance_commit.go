package internal

import (
	"errors"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// CommitExecutedExtension is the performance executor's append-only commit.
// It serializes the final applied-lineage check and canonicalization with all
// state mutations, including BlockChain's own background future-block retries.
// The caller must authorize finality before calling; this method verifies no QC.
// Existing native/ETH CommitToCanonical callers retain their existing behavior.
func (bc *BlockChain) CommitExecutedExtension(hash types.Hash) error {
	bc.lock.Lock()
	defer bc.lock.Unlock()
	if bc.chainConfig == nil || bc.chainConfig.Performance == nil || !bc.qmdbEnabled {
		return errors.New("executed extension requires explicit performance QMDB chain")
	}
	if err := bc.joinLagWrite(); err != nil {
		return err
	}
	var number uint64
	err := bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		b, err := rawdb.ReadBlockByHash(tx, hash)
		if err != nil {
			return err
		}
		if b == nil || b.Hash() != hash {
			return errors.New("executed extension block missing")
		}
		number = b.Number64().Uint64()
		head := rawdb.ReadHeadBlockHash(tx)
		hn := rawdb.ReadHeaderNumber(tx, head)
		if hn == nil {
			return errors.New("executed extension canonical head missing")
		}
		canonical, err := rawdb.ReadCanonicalHash(tx, *hn)
		if err != nil {
			return err
		}
		if canonical != head {
			return errors.New("executed extension canonical head inconsistent")
		}
		if number <= *hn {
			canonical, err := rawdb.ReadCanonicalHash(tx, number)
			if err != nil {
				return err
			}
			if canonical != hash {
				return errors.New("executed extension conflicts with canonical prefix")
			}
		} else if number != *hn+1 || b.ParentHash() != head {
			return errors.New("executed extension requires direct canonical parent")
		}
		_, _, applied, err := rawdb.ReadQMDBApplied(tx)
		if err != nil {
			return err
		}
		if !applied {
			return errors.New("executed extension applied marker missing")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !bc.HasAppliedBlock(hash, number) {
		return errors.New("executed extension is no longer on applied lineage; retry")
	}
	return bc.CommitToCanonical(hash)
}
